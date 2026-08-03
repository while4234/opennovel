package web

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/voocel/ainovel-cli/assets"
	"github.com/voocel/ainovel-cli/internal/bootstrap"
)

func TestLegacyMigrationCopiesOutputSanitizesConfigAndIsIdempotent(t *testing.T) {
	t.Parallel()
	base := testTempDir(t)
	runtimeRoot := filepath.Join(base, "runtime")
	source := filepath.Join(base, "old-output")
	writeLegacyFixture(t, source)
	writeTestFile(t, filepath.Join(source, ".ainovel", "config.json"), `{
  "provider":"private-provider",
  "model":"writer-v1",
  "style":"fantasy",
  "providers":{"private-provider":{"label":"Private","api_key":"do-not-copy","base_url":"https://user:pass@example.test/v1","models":["writer-v1"]}},
  "roles":{"writer":{"provider":"private-provider","model":"writer-v1","reasoning_effort":"high"}},
  "notify":{"command":"run-secret-helper"},
  "proxy":"https://proxy-user:proxy-pass@example.test"
}`)

	server := NewServer(bootstrap.Config{}, assets.Bundle{}, runtimeRoot)
	defer server.Close()
	first := performLegacyMigration(t, server, source, "Imported Novel")
	if first.Code != http.StatusCreated {
		t.Fatalf("first status = %d body=%s", first.Code, first.Body.String())
	}
	var created legacyMigrationResult
	decodeRecorderJSON(t, first, &created)
	if !created.Created || created.Project.Name != "Imported Novel" || created.SourceHash == "" {
		t.Fatalf("created result = %+v", created)
	}
	for relative, want := range map[string]string{
		"chapters/01.md":               "第一章\n正文",
		"outline.json":                 `[{"chapter":1}]`,
		"meta/checkpoints.jsonl":       `{"seq":1}`,
		"meta/sessions/main.jsonl":     `{"role":"coordinator"}`,
		"meta/adaptation/plan.json":    `{"status":"confirmed"}`,
		"meta/continuation/state.json": `{"status":"approved"}`,
		"meta/usage.json":              `{"version":2,"total":{"input_tokens":10}}`,
	} {
		data, err := os.ReadFile(filepath.Join(created.Project.OutputDir, filepath.FromSlash(relative)))
		if err != nil || string(data) != want {
			t.Fatalf("copied %s = %q, err=%v, want %q", relative, data, err, want)
		}
	}
	configData, err := os.ReadFile(ProjectConfigPath(created.Project))
	if err != nil {
		t.Fatalf("read sanitized config: %v", err)
	}
	configText := string(configData)
	for _, secret := range []string{"do-not-copy", "user:pass", "proxy-pass", "run-secret-helper", "api_key", "base_url"} {
		if strings.Contains(configText, secret) {
			t.Fatalf("sanitized config leaks %q: %s", secret, configText)
		}
	}
	safeConfig, err := bootstrap.LoadConfigFile(ProjectConfigPath(created.Project))
	if err != nil {
		t.Fatalf("parse sanitized config: %v", err)
	}
	if safeConfig.Provider != "private-provider" || safeConfig.ModelName != "writer-v1" || safeConfig.Style != "fantasy" {
		t.Fatalf("safe config fields not preserved: %+v", safeConfig)
	}
	if provider := safeConfig.Providers["private-provider"]; provider.Label != "Private" || len(provider.Models) != 1 || provider.APIKey != "" || provider.BaseURL != "" {
		t.Fatalf("provider was not sanitized: %+v", provider)
	}
	markerData, err := os.ReadFile(filepath.Join(created.Project.RootDir, filepath.FromSlash(legacyImportMarkerPath)))
	if err != nil || !bytes.Contains(markerData, []byte(created.SourceHash)) {
		t.Fatalf("migration marker missing hash: %s err=%v", markerData, err)
	}

	second := performLegacyMigration(t, server, source, "A Different Name")
	if second.Code != http.StatusOK {
		t.Fatalf("second status = %d body=%s", second.Code, second.Body.String())
	}
	var duplicate legacyMigrationResult
	decodeRecorderJSON(t, second, &duplicate)
	if duplicate.Created || duplicate.Project.ID != created.Project.ID || duplicate.SourceHash != created.SourceHash {
		t.Fatalf("idempotent result = %+v, first = %+v", duplicate, created)
	}
	projects, err := server.store.ListProjects()
	if err != nil || len(projects) != 1 {
		t.Fatalf("projects after retry = %d, err=%v", len(projects), err)
	}
}

func TestLegacyMigrationNeverOverwritesExistingProject(t *testing.T) {
	t.Parallel()
	base := testTempDir(t)
	server := NewServer(bootstrap.Config{}, assets.Bundle{}, filepath.Join(base, "runtime"))
	defer server.Close()
	existing, err := server.store.CreateProject("Same Name")
	if err != nil {
		t.Fatalf("create existing project: %v", err)
	}
	existingPath := filepath.Join(existing.OutputDir, "chapters", "01.md")
	writeTestFile(t, existingPath, "existing")
	source := filepath.Join(base, "legacy")
	writeLegacyFixture(t, source)

	response := performLegacyMigration(t, server, source, "Same Name")
	if response.Code != http.StatusCreated {
		t.Fatalf("status = %d body=%s", response.Code, response.Body.String())
	}
	var imported legacyMigrationResult
	decodeRecorderJSON(t, response, &imported)
	if imported.Project.ID == existing.ID || imported.Project.RootDir == existing.RootDir {
		t.Fatalf("migration reused existing project: existing=%+v imported=%+v", existing, imported.Project)
	}
	data, err := os.ReadFile(existingPath)
	if err != nil || string(data) != "existing" {
		t.Fatalf("existing project was changed: %q err=%v", data, err)
	}
}

func TestLegacyMigrationRejectsUnsafeSources(t *testing.T) {
	t.Parallel()
	base := testTempDir(t)
	runtimeRoot := filepath.Join(base, "runtime")
	server := NewServer(bootstrap.Config{}, assets.Bundle{}, runtimeRoot)
	defer server.Close()

	t.Run("missing explicit directory", func(t *testing.T) {
		response := performLegacyMigration(t, server, "", "")
		if response.Code != http.StatusBadRequest {
			t.Fatalf("status = %d body=%s", response.Code, response.Body.String())
		}
	})

	t.Run("unrecognized directory", func(t *testing.T) {
		source := filepath.Join(base, "unrecognized")
		writeTestFile(t, filepath.Join(source, "random.txt"), "not a novel output")
		response := performLegacyMigration(t, server, source, "")
		if response.Code != http.StatusBadRequest {
			t.Fatalf("status = %d body=%s", response.Code, response.Body.String())
		}
	})

	t.Run("runtime overlap", func(t *testing.T) {
		source := filepath.Join(runtimeRoot, "old-output")
		writeLegacyFixture(t, source)
		response := performLegacyMigration(t, server, source, "")
		if response.Code != http.StatusBadRequest {
			t.Fatalf("status = %d body=%s", response.Code, response.Body.String())
		}
	})

	t.Run("symbolic link in source", func(t *testing.T) {
		source := filepath.Join(base, "linked-output")
		writeLegacyFixture(t, source)
		external := filepath.Join(base, "outside.txt")
		writeTestFile(t, external, "outside")
		if err := os.Symlink(external, filepath.Join(source, "chapters", "02.md")); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
		response := performLegacyMigration(t, server, source, "")
		if response.Code != http.StatusBadRequest {
			t.Fatalf("status = %d body=%s", response.Code, response.Body.String())
		}
	})
}

func TestLegacyMigrationRevalidatesFilesBeforeCopy(t *testing.T) {
	t.Parallel()
	base := testTempDir(t)
	source := filepath.Join(base, "legacy")
	writeLegacyFixture(t, source)
	store := NewProjectStore(filepath.Join(base, "runtime"))
	plan, err := store.buildLegacyImportPlan(source)
	if err != nil {
		t.Fatalf("build plan: %v", err)
	}
	chapterPath := filepath.Join(source, "chapters", "01.md")
	if err := os.Remove(chapterPath); err != nil {
		t.Fatal(err)
	}
	external := filepath.Join(base, "outside.md")
	writeTestFile(t, external, "outside")
	if err := os.Symlink(external, chapterPath); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	_, _, err = copyLegacyImportPlan(plan, filepath.Join(base, "destination"))
	if !errors.Is(err, errLegacySourceInvalid) {
		t.Fatalf("copy error = %v, want invalid source", err)
	}
}

func writeLegacyFixture(t *testing.T, source string) {
	t.Helper()
	files := map[string]string{
		"chapters/01.md":               "第一章\n正文",
		"outline.json":                 `[{"chapter":1}]`,
		"meta/checkpoints.jsonl":       `{"seq":1}`,
		"meta/sessions/main.jsonl":     `{"role":"coordinator"}`,
		"meta/adaptation/plan.json":    `{"status":"confirmed"}`,
		"meta/continuation/state.json": `{"status":"approved"}`,
		"meta/usage.json":              `{"version":2,"total":{"input_tokens":10}}`,
	}
	for relative, body := range files {
		writeTestFile(t, filepath.Join(source, filepath.FromSlash(relative)), body)
	}
}

func writeTestFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func performLegacyMigration(t *testing.T, server *Server, source, name string) *httptest.ResponseRecorder {
	t.Helper()
	expected := ""
	if preview, err := server.store.DryRunLegacyProjectMigration(source); err == nil {
		expected = preview.SourceSHA256
	}
	body, err := json.Marshal(legacyMigrationRequest{SourceDir: source, Name: name, ExpectedSourceSHA256: expected})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/projects/migrate-legacy", bytes.NewReader(body))
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, req)
	return recorder
}

func decodeRecorderJSON(t *testing.T, recorder *httptest.ResponseRecorder, target any) {
	t.Helper()
	if err := json.Unmarshal(recorder.Body.Bytes(), target); err != nil {
		t.Fatalf("decode response %s: %v", recorder.Body.String(), err)
	}
}

func TestLegacyMigrationDryRunApplyAndChecksumRollback(t *testing.T) {
	runtimeRoot := filepath.Join(testTempDir(t), "runtime")
	source := filepath.Join(testTempDir(t), "legacy")
	writeTestFile(t, filepath.Join(source, "outline.json"), `[{"chapter":1,"title":"one"}]`)
	writeTestFile(t, filepath.Join(source, "chapters", "0001", "final.md"), "formal prose")
	store := NewProjectStore(runtimeRoot)
	preview, err := store.DryRunLegacyProjectMigration(source)
	if err != nil || len(preview.SourceSHA256) != 64 || preview.FileCount != 2 {
		t.Fatalf("preview = %+v, err = %v", preview, err)
	}
	sourceBefore := preview.SourceSHA256
	result, err := store.MigrateLegacyProject(source, "Migrated", preview.SourceSHA256)
	if err != nil {
		t.Fatal(err)
	}
	if after, err := store.DryRunLegacyProjectMigration(source); err != nil || after.SourceSHA256 != sourceBefore {
		t.Fatalf("source changed during atomic migration: before=%s after=%+v err=%v", sourceBefore, after, err)
	}
	if err := store.RollbackLegacyProjectMigration(result.Project.ID, strings.Repeat("0", 64)); err == nil {
		t.Fatal("rollback accepted a mismatched dry-run checksum")
	}
	if _, err := os.Stat(result.Project.RootDir); err != nil {
		t.Fatalf("mismatched rollback changed target: %v", err)
	}
	if err := store.RollbackLegacyProjectMigration(result.Project.ID, preview.SourceSHA256); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(result.Project.RootDir); !os.IsNotExist(err) {
		t.Fatalf("migrated target remains after rollback: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(source, "chapters", "0001", "final.md")); err != nil || string(got) != "formal prose" {
		t.Fatalf("source changed during migration rollback: %q %v", got, err)
	}
	if after, err := store.DryRunLegacyProjectMigration(source); err != nil || after.SourceSHA256 != sourceBefore {
		t.Fatalf("source changed during rollback: before=%s after=%+v err=%v", sourceBefore, after, err)
	}
}

func TestLegacyMigrationRejectsSourceDriftAfterDryRun(t *testing.T) {
	base := testTempDir(t)
	source := filepath.Join(base, "legacy")
	writeLegacyFixture(t, source)
	store := NewProjectStore(filepath.Join(base, "runtime"))
	preview, err := store.DryRunLegacyProjectMigration(source)
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(source, "chapters", "01.md"), "changed after dry-run")
	if _, err := store.MigrateLegacyProject(source, "Must Reject", preview.SourceSHA256); !errors.Is(err, errLegacySourceInvalid) {
		t.Fatalf("migration error = %v, want invalid source", err)
	}
	projects, err := store.ListProjects()
	if err != nil || len(projects) != 0 {
		t.Fatalf("projects after rejected drift = %v, err=%v", projects, err)
	}
}

func TestLegacyMigrationRecoversInterruptedStagingAndInstall(t *testing.T) {
	base := testTempDir(t)
	store := NewProjectStore(filepath.Join(base, "runtime"))
	if err := os.MkdirAll(store.ProjectsDir(), 0o755); err != nil {
		t.Fatal(err)
	}

	t.Run("incomplete staging is removed", func(t *testing.T) {
		projectID := "legacy-interrupted-stage"
		staging, err := os.MkdirTemp(store.ProjectsDir(), ".legacy-"+projectID+"-")
		if err != nil {
			t.Fatal(err)
		}
		writeTestFile(t, filepath.Join(staging, "partial"), "partial")
		finalRoot := filepath.Join(store.ProjectsDir(), projectID)
		journal := legacyMigrationJournal{Version: legacyMigrationJournalVersion, ProjectID: projectID, StagingRoot: staging, FinalRoot: finalRoot, ExpectedSourceSHA256: strings.Repeat("1", 64)}
		if _, err := store.writeLegacyMigrationJournal(journal); err != nil {
			t.Fatal(err)
		}
		if err := store.recoverLegacyMigrationJournals(); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(staging); !os.IsNotExist(err) {
			t.Fatalf("staging remains after recovery: %v", err)
		}
	})

	t.Run("complete atomic install is retained", func(t *testing.T) {
		projectID := "legacy-installed-stage"
		staging := filepath.Join(store.ProjectsDir(), ".legacy-"+projectID+"-gone")
		finalRoot := filepath.Join(store.ProjectsDir(), projectID)
		writeTestFile(t, filepath.Join(finalRoot, "output", "chapters", "01.md"), "complete payload")
		payloadHash, copiedFiles, err := hashLegacyStagedPayload(finalRoot)
		if err != nil {
			t.Fatal(err)
		}
		marker := legacyImportMarker{Version: legacyImportMarkerVersion, SourceHash: payloadHash, CopiedFiles: copiedFiles}
		if err := writeLegacyImportMarkerAt(finalRoot, marker); err != nil {
			t.Fatal(err)
		}
		manifest := ProjectManifest{Version: manifestVersion, ID: projectID, Name: "installed", RootDir: finalRoot, OutputDir: filepath.Join(finalRoot, "output")}
		if err := writeProjectManifestAt(finalRoot, manifest); err != nil {
			t.Fatal(err)
		}
		journal := legacyMigrationJournal{Version: legacyMigrationJournalVersion, ProjectID: projectID, StagingRoot: staging, FinalRoot: finalRoot, ExpectedSourceSHA256: marker.SourceHash}
		journalPath, err := store.writeLegacyMigrationJournal(journal)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.recoverLegacyMigrationJournals(); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(finalRoot); err != nil {
			t.Fatalf("complete install removed: %v", err)
		}
		if _, err := os.Stat(journalPath); !os.IsNotExist(err) {
			t.Fatalf("journal remains: %v", err)
		}
	})

	t.Run("invalid final install and staging are removed", func(t *testing.T) {
		projectID := "legacy-invalid-install"
		staging, err := os.MkdirTemp(store.ProjectsDir(), ".legacy-"+projectID+"-")
		if err != nil {
			t.Fatal(err)
		}
		finalRoot := filepath.Join(store.ProjectsDir(), projectID)
		writeTestFile(t, filepath.Join(staging, "partial"), "partial")
		writeTestFile(t, filepath.Join(finalRoot, "project.json"), "partial-visible-install")
		journal := legacyMigrationJournal{Version: legacyMigrationJournalVersion, ProjectID: projectID, StagingRoot: staging, FinalRoot: finalRoot, ExpectedSourceSHA256: strings.Repeat("4", 64)}
		if _, err := store.writeLegacyMigrationJournal(journal); err != nil {
			t.Fatal(err)
		}
		if err := store.recoverLegacyMigrationJournals(); err != nil {
			t.Fatal(err)
		}
		for _, path := range []string{staging, finalRoot} {
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatalf("interrupted path remains %s: %v", path, err)
			}
		}
	})
}

func TestLegacyMigrationStartupRecoveryAndStagedPayloadVerification(t *testing.T) {
	base := testTempDir(t)
	runtimeRoot := filepath.Join(base, "runtime")
	store := NewProjectStore(runtimeRoot)
	if err := os.MkdirAll(store.ProjectsDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	projectID := "legacy-startup-recovery"
	staging, err := os.MkdirTemp(store.ProjectsDir(), ".legacy-"+projectID+"-")
	if err != nil {
		t.Fatal(err)
	}
	finalRoot := filepath.Join(store.ProjectsDir(), projectID)
	writeTestFile(t, filepath.Join(staging, "output", "chapters", "01.md"), "partial")
	journal := legacyMigrationJournal{Version: legacyMigrationJournalVersion, ProjectID: projectID, StagingRoot: staging, FinalRoot: finalRoot, ExpectedSourceSHA256: strings.Repeat("a", 64)}
	journalPath, err := store.writeLegacyMigrationJournal(journal)
	if err != nil {
		t.Fatal(err)
	}

	reopened := NewProjectStore(runtimeRoot)
	if projects, err := reopened.ListProjects(); err != nil || len(projects) != 0 {
		t.Fatalf("startup recovery ListProjects = %v, err=%v", projects, err)
	}
	for _, path := range []string{staging, finalRoot, journalPath} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("startup recovery left %s: %v", path, err)
		}
	}
}

func TestLegacyMigrationRejectsTamperedOrDeletedStagedPayload(t *testing.T) {
	base := testTempDir(t)
	source := filepath.Join(base, "legacy")
	writeLegacyFixture(t, source)
	store := NewProjectStore(filepath.Join(base, "runtime"))
	plan, err := store.buildLegacyImportPlan(source)
	if err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name   string
		mutate func(t *testing.T, root string)
	}{
		{name: "content tamper", mutate: func(t *testing.T, root string) {
			writeTestFile(t, filepath.Join(root, "output", "chapters", "01.md"), "tampered")
		}},
		{name: "payload deletion", mutate: func(t *testing.T, root string) {
			if err := os.Remove(filepath.Join(root, "output", "chapters", "01.md")); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := filepath.Join(base, strings.ReplaceAll(test.name, " ", "-"))
			copied, copiedHash, err := copyLegacyImportPlan(plan, filepath.Join(root, "output"))
			if err != nil || copiedHash != plan.SourceHash {
				t.Fatalf("copy = %d %s, err=%v", copied, copiedHash, err)
			}
			marker := legacyImportMarker{Version: legacyImportMarkerVersion, SourceHash: plan.SourceHash, CopiedFiles: copied}
			if err := writeLegacyImportMarkerAt(root, marker); err != nil {
				t.Fatal(err)
			}
			if err := writeProjectManifestAt(root, ProjectManifest{Version: manifestVersion, ID: "staged", RootDir: root, OutputDir: filepath.Join(root, "output")}); err != nil {
				t.Fatal(err)
			}
			test.mutate(t, root)
			if err := verifyStagedLegacyMigration(root, marker); err == nil {
				t.Fatal("tampered staged payload passed verification")
			}
		})
	}
}

func TestLegacyMigrationCanonicalHashSeparatesEntryBoundariesAndMetadata(t *testing.T) {
	hashRecord := func(records ...struct {
		kind, path string
		mode       fs.FileMode
		content    []byte
	}) string {
		h := sha256.New()
		for _, record := range records {
			hashLegacyCanonicalBytes(h, record.kind, record.path, record.mode, record.content)
		}
		return hex.EncodeToString(h.Sum(nil))
	}
	a := hashRecord(struct {
		kind, path string
		mode       fs.FileMode
		content    []byte
	}{"file", "chapters/a.md", 0o644, []byte("bc")})
	b := hashRecord(struct {
		kind, path string
		mode       fs.FileMode
		content    []byte
	}{"file", "chapters/ab.md", 0o644, []byte("c")})
	if a == b {
		t.Fatal("length-prefixed records permitted a cross-field boundary collision")
	}
	modeA := hashRecord(struct {
		kind, path string
		mode       fs.FileMode
		content    []byte
	}{"file", "chapters/a.md", 0o644, []byte("same")})
	modeB := hashRecord(struct {
		kind, path string
		mode       fs.FileMode
		content    []byte
	}{"file", "chapters/a.md", 0o600, []byte("same")})
	if runtime.GOOS != "windows" && modeA == modeB {
		t.Fatal("canonical record omitted the file mode")
	}
}

func TestLegacyMigrationStartupRefusesVersionOneJournal(t *testing.T) {
	base := testTempDir(t)
	runtimeRoot := filepath.Join(base, "runtime")
	store := NewProjectStore(runtimeRoot)
	if err := os.MkdirAll(store.legacyMigrationJournalDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	staging := filepath.Join(store.ProjectsDir(), ".legacy-old-v1-stage")
	writeTestFile(t, filepath.Join(staging, "partial"), "must remain for operator inspection")
	journalPath := filepath.Join(store.legacyMigrationJournalDir(), "old-v1.json")
	writeTestFile(t, journalPath, `{"version":1,"project_id":"old-v1"}`)

	reopened := NewProjectStore(runtimeRoot)
	if _, err := reopened.ListProjects(); err == nil {
		t.Fatal("startup accepted a version-one legacy migration journal")
	}
	for _, path := range []string{staging, journalPath} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("version-one refusal modified %s: %v", path, err)
		}
	}
}

func TestLegacyMigrationRollbackDoesNotUpgradeVersionOneMarker(t *testing.T) {
	store := NewProjectStore(filepath.Join(testTempDir(t), "runtime"))
	project, err := store.CreateProject("Legacy v1")
	if err != nil {
		t.Fatal(err)
	}
	hash := strings.Repeat("3", 64)
	if err := writeLegacyImportMarker(project, legacyImportMarker{Version: 1, SourceHash: hash}); err != nil {
		t.Fatal(err)
	}
	if err := store.RollbackLegacyProjectMigration(project.ID, hash); err == nil {
		t.Fatal("rollback accepted a version-one marker")
	}
	marker, err := readLegacyImportMarkerAt(project.RootDir)
	if err != nil || marker.Version != 1 {
		t.Fatalf("version-one marker was changed: %+v err=%v", marker, err)
	}
}
