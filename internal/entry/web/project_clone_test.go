package web

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/voocel/ainovel-cli/assets"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/host"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
)

func TestCloneProjectCopiesFilesAndRewritesProjectPaths(t *testing.T) {
	store := NewProjectStore(filepath.Join(testTempDir(t), "runtime"))
	source, err := store.CreateProject("Source Novel")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	sourceUpload := filepath.Join(source.RootDir, "uploads", "adaptation", "source.txt")
	cloneTestWriteFile(t, sourceUpload, []byte("original novel text"))
	cloneTestWriteJSON(t, filepath.Join(source.OutputDir, "meta", "adaptation", "source_manifest.json"), map[string]any{
		"source_path": sourceUpload,
		"title":       "Source Novel",
	})
	cloneTestWriteJSON(t, filepath.Join(source.OutputDir, filepath.FromSlash(webCoCreateCheckpointRelPath)), map[string]any{
		"version":     1,
		"kind":        "adapt",
		"source_file": "source.txt",
		"source_path": sourceUpload,
	})
	chapterPath := filepath.Join(source.OutputDir, "chapters", "chapter-001.md")
	cloneTestWriteFile(t, chapterPath, []byte("chapter body"))
	cloneTestWriteFile(t, filepath.Join(source.RootDir, "uploads", "custom.json"), []byte("not program-owned JSON"))
	cloneTestWriteFile(t, filepath.Join(source.RootDir, filepath.FromSlash(actionRegistryRelPath)), []byte(`{"project_id":"old"}`))
	cloneTestWriteFile(t, filepath.Join(source.OutputDir, "meta", "runtime", "queue.jsonl"), []byte("running task"))
	cloneTestWriteFile(t, filepath.Join(source.RootDir, ".tmp-clone-staging"), []byte("temporary"))
	cloneTestWriteFile(t, filepath.Join(source.OutputDir, ".tmp-draft"), []byte("temporary"))

	sourceManifestBefore := cloneTestReadFile(t, filepath.Join(source.RootDir, "project.json"))
	sourceAdaptationBefore := cloneTestReadFile(t, filepath.Join(source.OutputDir, "meta", "adaptation", "source_manifest.json"))
	time.Sleep(2 * time.Millisecond)

	cloned, err := store.CloneProject(source.ID, "Source Novel - Copy")
	if err != nil {
		t.Fatalf("CloneProject: %v", err)
	}

	if cloned.ID == source.ID || cloned.ID == "" {
		t.Fatalf("cloned ID = %q, source ID = %q", cloned.ID, source.ID)
	}
	if cloned.Name != "Source Novel - Copy" {
		t.Fatalf("cloned name = %q", cloned.Name)
	}
	if filepath.Clean(cloned.RootDir) == filepath.Clean(source.RootDir) {
		t.Fatalf("clone shares source root %q", cloned.RootDir)
	}
	if filepath.Clean(cloned.OutputDir) == filepath.Clean(source.OutputDir) {
		t.Fatalf("clone shares source output dir %q", cloned.OutputDir)
	}
	if !cloned.CreatedAt.After(source.CreatedAt) || !cloned.UpdatedAt.After(source.UpdatedAt) || !cloned.LastAccessedAt.After(source.LastAccessedAt) {
		t.Fatalf("clone timestamps were not regenerated: source=%+v clone=%+v", source, cloned)
	}
	if cloned.DeletedAt != nil {
		t.Fatalf("cloned project is deleted: %+v", cloned)
	}

	clonedChapter := filepath.Join(cloned.OutputDir, "chapters", "chapter-001.md")
	if got := string(cloneTestReadFile(t, clonedChapter)); got != "chapter body" {
		t.Fatalf("cloned chapter = %q", got)
	}
	clonedUpload := filepath.Join(cloned.RootDir, "uploads", "adaptation", "source.txt")
	if got := string(cloneTestReadFile(t, clonedUpload)); got != "original novel text" {
		t.Fatalf("cloned upload = %q", got)
	}
	if _, err := os.Stat(filepath.Join(cloned.RootDir, "uploads", "custom.json")); !os.IsNotExist(err) {
		t.Fatalf("unowned upload was cloned: %v", err)
	}
	cloneTestAssertSourcePath(t, filepath.Join(cloned.OutputDir, "meta", "adaptation", "source_manifest.json"), clonedUpload)
	cloneTestAssertSourcePath(t, filepath.Join(cloned.OutputDir, filepath.FromSlash(webCoCreateCheckpointRelPath)), clonedUpload)

	for _, ignored := range []string{
		filepath.Join(cloned.RootDir, ".tmp-clone-staging"),
		filepath.Join(cloned.OutputDir, ".tmp-draft"),
		filepath.Join(cloned.RootDir, filepath.FromSlash(actionRegistryRelPath)),
		filepath.Join(cloned.OutputDir, "meta", "runtime", "queue.jsonl"),
	} {
		if _, err := os.Stat(ignored); !os.IsNotExist(err) {
			t.Fatalf("temporary file %q was cloned: %v", ignored, err)
		}
	}

	if after := cloneTestReadFile(t, filepath.Join(source.RootDir, "project.json")); !bytes.Equal(after, sourceManifestBefore) {
		t.Fatal("source project manifest changed during clone")
	}
	if after := cloneTestReadFile(t, filepath.Join(source.OutputDir, "meta", "adaptation", "source_manifest.json")); !bytes.Equal(after, sourceAdaptationBefore) {
		t.Fatal("source adaptation manifest changed during clone")
	}
	if got := string(cloneTestReadFile(t, chapterPath)); got != "chapter body" {
		t.Fatalf("source chapter changed to %q", got)
	}
}

func TestCloneProjectFailureLeavesNoPartialProject(t *testing.T) {
	store := NewProjectStore(filepath.Join(testTempDir(t), "runtime"))
	source, err := store.CreateProject("Broken Source")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if err := os.WriteFile(filepath.Join(source.RootDir, "project.json"), []byte("not-json"), 0o644); err != nil {
		t.Fatalf("corrupt source manifest: %v", err)
	}

	before := cloneTestProjectEntries(t, store.ProjectsDir())
	if _, err := store.CloneProject(source.ID, "Should Fail"); err == nil {
		t.Fatal("CloneProject succeeded with a corrupt source manifest")
	}
	after := cloneTestProjectEntries(t, store.ProjectsDir())
	if len(after) != len(before) {
		t.Fatalf("project directories after failed clone = %v, before = %v", after, before)
	}
	for i := range before {
		if after[i] != before[i] {
			t.Fatalf("project directories after failed clone = %v, before = %v", after, before)
		}
	}
}

func TestCloneProjectCopiesCompletedStoryFoundationAndRejectsPendingJournal(t *testing.T) {
	projects := NewProjectStore(filepath.Join(testTempDir(t), "runtime"))
	source, err := projects.CreateProject("Foundation Source")
	if err != nil {
		t.Fatal(err)
	}
	st := storepkg.NewStore(source.OutputDir)
	if _, err := st.Foundation.SaveCAS(domain.StoryFoundation{
		Premise:    "A complete foundation.",
		Characters: []domain.Character{{ID: "lin", Name: "Lin"}},
		WorldRules: []domain.WorldRule{{ID: "rule", Rule: "No reset"}},
	}, 0); err != nil {
		t.Fatal(err)
	}
	for rel, kind := range map[string]string{
		"output/story_foundation.json":            "formal_root",
		"output/characters.json":                  "formal_root",
		"output/world_rules.json":                 "formal_root",
		"output/planned_relationships.json":       "formal_root",
		"output/meta/foundation/projections.json": "foundation_manifest",
	} {
		data := cloneTestReadFile(t, filepath.Join(source.RootDir, filepath.FromSlash(rel)))
		if err := validateCloneArtifact(source.RootDir, rel, kind, data); err != nil {
			t.Fatalf("foundation artifact %s is not clone-valid: %v", rel, err)
		}
	}
	cloned, err := projects.CloneProject(source.ID, "Foundation Copy")
	if err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{
		"story_foundation.json", "premise.md", "characters.json", "characters.md",
		"world_rules.json", "world_rules.md", "planned_relationships.json", "planned_relationships.md",
		"meta/foundation/projections.json",
	} {
		if _, err := os.Stat(filepath.Join(cloned.OutputDir, filepath.FromSlash(rel))); err != nil {
			t.Fatalf("clone did not copy %s: %v", rel, err)
		}
	}

	journal := filepath.Join(source.OutputDir, filepath.FromSlash("meta/foundation/journal.json"))
	cloneTestWriteJSON(t, journal, map[string]any{"version": 1, "stage": "prepared"})
	if _, err := projects.CloneProject(source.ID, "Pending Copy"); err == nil || !strings.Contains(err.Error(), "pending story foundation transaction") {
		t.Fatalf("clone accepted pending foundation transaction: %v", err)
	}
	validationRoot := filepath.Join(testTempDir(t), "validation-pending")
	if _, _, err := projects.CloneProjectForValidation(source.ID, validationRoot, "normal-fedcba"); err == nil || !strings.Contains(err.Error(), "foundation_transaction_pending") {
		t.Fatalf("validation clone accepted pending foundation transaction: %v", err)
	}
}

func TestValidationCloneVerifiesStoryFoundationProjectionSet(t *testing.T) {
	projects := NewProjectStore(filepath.Join(testTempDir(t), "runtime"))
	source, err := projects.CreateProject("Foundation Validation")
	if err != nil {
		t.Fatal(err)
	}
	st := storepkg.NewStore(source.OutputDir)
	if _, err := st.Foundation.SaveCAS(domain.StoryFoundation{
		Premise:    "A valid foundation.",
		Characters: []domain.Character{{ID: "lin", Name: "Lin"}},
		WorldRules: []domain.WorldRule{{ID: "rule", Rule: "No reset"}},
	}, 0); err != nil {
		t.Fatal(err)
	}
	validationRoot := filepath.Join(testTempDir(t), "validation-foundation")
	clone, _, err := projects.CloneProjectForValidation(source.ID, validationRoot, "normal-aabbcc")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(clone.RootDir) })
	if _, err := os.Stat(filepath.Join(clone.OutputDir, "story_foundation.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source.OutputDir, "premise.md"), []byte("mixed projection"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := projects.CloneProject(source.ID, "Mixed Foundation Copy"); err == nil || !strings.Contains(err.Error(), "not clone-ready") {
		t.Fatalf("regular clone accepted a mixed projection: %v", err)
	}
	if _, _, err := projects.CloneProjectForValidation(source.ID, filepath.Join(testTempDir(t), "validation-mixed"), "normal-bbccdd"); err == nil || !strings.Contains(err.Error(), "foundation_projection_invalid") {
		t.Fatalf("validation clone accepted a mixed projection: %v", err)
	}
}

func TestRegularAndValidationCloneRejectOrphanFoundationArtifacts(t *testing.T) {
	for _, test := range []struct {
		name string
		rel  string
		data []byte
	}{
		{name: "planned relationships", rel: "planned_relationships.json", data: []byte("[]")},
		{name: "projection manifest", rel: "meta/foundation/projections.json", data: orphanFoundationManifest(t)},
	} {
		t.Run(test.name, func(t *testing.T) {
			projects := NewProjectStore(filepath.Join(testTempDir(t), "runtime"))
			source, err := projects.CreateProject("Orphan Foundation " + test.name)
			if err != nil {
				t.Fatal(err)
			}
			cloneTestWriteFile(t, filepath.Join(source.OutputDir, filepath.FromSlash(test.rel)), test.data)
			if _, err := projects.CloneProject(source.ID, "Rejected Orphan"); err == nil || !strings.Contains(err.Error(), "without canonical foundation") {
				t.Fatalf("regular clone accepted orphan %s: %v", test.rel, err)
			}
			validationRoot := filepath.Join(testTempDir(t), "validation-orphan")
			if _, _, err := projects.CloneProjectForValidation(source.ID, validationRoot, "normal-acde12"); err == nil || !strings.Contains(err.Error(), "foundation_projection_invalid") {
				t.Fatalf("validation clone accepted orphan %s: %v", test.rel, err)
			}
		})
	}
}

func TestCloneProjectGetsConsistentFoundationDuringConcurrentSave(t *testing.T) {
	projects := NewProjectStore(filepath.Join(testTempDir(t), "runtime"))
	source, err := projects.CreateProject("Concurrent Foundation Source")
	if err != nil {
		t.Fatal(err)
	}
	store := storepkg.NewStore(source.OutputDir)
	base, err := store.Foundation.SaveCAS(domain.StoryFoundation{
		Premise:    "base foundation",
		Characters: []domain.Character{{ID: "lin", Name: "Lin"}},
		WorldRules: []domain.WorldRule{{ID: "rule", Rule: "No reset"}},
	}, 0)
	if err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	saveDone := make(chan error, 1)
	go func() {
		<-start
		for revision := base.Revision; revision < base.Revision+8; revision++ {
			current, loadErr := store.Foundation.Load()
			if loadErr != nil {
				saveDone <- loadErr
				return
			}
			candidate := domain.CloneStoryFoundation(current)
			candidate.Premise = fmt.Sprintf("concurrent foundation %d", revision)
			if _, saveErr := store.Foundation.SaveCAS(candidate, current.Revision); saveErr != nil {
				saveDone <- saveErr
				return
			}
		}
		saveDone <- nil
	}()
	close(start)

	for i := 0; i < 4; i++ {
		cloned, err := projects.CloneProject(source.ID, fmt.Sprintf("Concurrent Copy %d", i))
		if err != nil {
			t.Fatal(err)
		}
		clonedFoundation, err := storepkg.NewStore(cloned.OutputDir).Foundation.Load()
		if err != nil {
			t.Fatalf("clone %d has a mixed foundation snapshot: %v", i, err)
		}
		if clonedFoundation.Revision < base.Revision {
			t.Fatalf("clone %d revision = %d", i, clonedFoundation.Revision)
		}
	}
	if err := <-saveDone; err != nil {
		t.Fatal(err)
	}
}

func orphanFoundationManifest(t *testing.T) []byte {
	t.Helper()
	files := make(map[string]string, 8)
	for i := 0; i < 8; i++ {
		files[fmt.Sprintf("artifact-%d", i)] = strings.Repeat("0", 64)
	}
	data, err := json.Marshal(map[string]any{
		"version":           1,
		"revision":          1,
		"content_signature": strings.Repeat("1", 64),
		"audit_signature":   strings.Repeat("2", 64),
		"files":             files,
	})
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestValidationCloneUsesExplicitExternalRootAndExcludesCredentials(t *testing.T) {
	store := NewProjectStore(filepath.Join(testTempDir(t), "runtime"))
	source, err := store.CreateProject("Private Source")
	if err != nil {
		t.Fatal(err)
	}
	cloneTestWriteFile(t, filepath.Join(source.OutputDir, "chapters", "0001", "final.md"), []byte("formal prose"))
	cloneTestWriteFile(t, filepath.Join(source.RootDir, ".ainovel", "config.json"), []byte(`{"api_key":"secret"}`))
	for _, sensitive := range []string{
		filepath.Join(source.RootDir, "output", ".ENV"),
		filepath.Join(source.RootDir, "uploads", "nested", "AUTH.JSON"),
		filepath.Join(source.RootDir, "uploads", "credentials", "provider.json"),
		filepath.Join(source.RootDir, "uploads", "nested", "secrets.production"),
		filepath.Join(source.RootDir, "uploads", "nested", "client.pem"),
		filepath.Join(source.RootDir, "uploads", "nested", "token.json"),
		filepath.Join(source.RootDir, "uploads", "nested", "OAuth-cache.json"),
		filepath.Join(source.RootDir, "uploads", "nested", ".netrc"),
		filepath.Join(source.RootDir, "uploads", "nested", ".NPMRC"),
		filepath.Join(source.RootDir, "uploads", "nested", ".git-credentials"),
		filepath.Join(source.RootDir, "uploads", "nested", "signing.keystore"),
	} {
		cloneTestWriteFile(t, sensitive, []byte("must-not-copy"))
	}
	validationRoot := filepath.Join(testTempDir(t), "validation")
	clone, report, err := store.CloneProjectForValidation(source.ID, validationRoot, "normal-a1b2c3")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(clone.RootDir) })
	if report.SourceSHA256 == "" || report.SourceSHA256 != report.AfterSHA256 || report.FileCount == 0 {
		t.Fatalf("report = %+v", report)
	}
	if filepath.Clean(filepath.Dir(clone.RootDir)) != filepath.Clean(validationRoot) || clone.ID != "normal-a1b2c3" || clone.ClonedFromID != source.ID {
		t.Fatalf("clone = %+v", clone)
	}
	if _, err := os.Stat(filepath.Join(clone.RootDir, ".ainovel", "config.json")); !os.IsNotExist(err) {
		t.Fatalf("credential file was cloned: %v", err)
	}
	for _, sensitive := range []string{
		filepath.Join(clone.RootDir, "output", ".ENV"),
		filepath.Join(clone.RootDir, "uploads", "nested", "AUTH.JSON"),
		filepath.Join(clone.RootDir, "uploads", "credentials", "provider.json"),
		filepath.Join(clone.RootDir, "uploads", "nested", "secrets.production"),
		filepath.Join(clone.RootDir, "uploads", "nested", "client.pem"),
		filepath.Join(clone.RootDir, "uploads", "nested", "token.json"),
		filepath.Join(clone.RootDir, "uploads", "nested", "OAuth-cache.json"),
		filepath.Join(clone.RootDir, "uploads", "nested", ".netrc"),
		filepath.Join(clone.RootDir, "uploads", "nested", ".NPMRC"),
		filepath.Join(clone.RootDir, "uploads", "nested", ".git-credentials"),
		filepath.Join(clone.RootDir, "uploads", "nested", "signing.keystore"),
	} {
		if _, err := os.Stat(sensitive); !os.IsNotExist(err) {
			t.Fatalf("nested credential %q was cloned: %v", sensitive, err)
		}
	}
	if got := string(cloneTestReadFile(t, filepath.Join(clone.OutputDir, "chapters", "0001", "final.md"))); got != "formal prose" {
		t.Fatalf("clone prose = %q", got)
	}
}

func TestValidationCloneRejectsHandBuiltSelfSignedPublicationAuthority(t *testing.T) {
	projects := NewProjectStore(filepath.Join(testTempDir(t), "runtime"))
	source, err := projects.CreateProject("Accepted Expansion Authority")
	if err != nil {
		t.Fatal(err)
	}
	chapterID := domain.LegacyStructureID(source.ID, domain.StructureKindChapter, "chapter/1")
	payload, _ := json.Marshal(map[string]any{"id": chapterID, "chapter": 1})
	version := domain.ArtifactVersion{
		ID: "version-authority", ArtifactID: chapterID, ArtifactKind: domain.StructureKindChapter,
		RevisionID: "revision-authority", Sequence: 1, Round: 1, Payload: payload,
		ContentSignature: domain.JSONContentSignature(payload), CreatedAt: "2026-07-17T00:00:00Z",
	}
	impact, err := domain.NewRevisionImpact("accepted expansion", []domain.RevisionImpactItem{{
		ArtifactID: chapterID, ArtifactKind: domain.StructureKindChapter, Change: "accept expansion",
		Requirement: domain.StructureImpactRequired, Cause: domain.StructureImpactStructureChange,
		DependencyEvidence: []string{"accepted candidate"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	candidateSignature := domain.CandidateSignature([]domain.ArtifactVersion{version})
	session := domain.RevisionSession{
		Version: domain.RevisionSchemaVersion, ID: "revision-authority", Mode: domain.RevisionModeNormal,
		Stage: domain.RevisionStageCompleted, Revision: 4, Generation: 1,
		PolicyID: domain.NormalRevisionPolicyID, PolicyVersion: domain.NormalRevisionPolicyVersion, Intent: "expand", Impact: impact,
		PreviewSignature:    strings.Repeat("a", 64),
		ApprovalStages:      []domain.RevisionApprovalStage{{ID: "structure", Label: "Structure"}},
		Approvals:           []domain.RevisionApproval{{StageID: "structure", ApprovedAt: "2026-07-17T00:00:00Z"}},
		CandidateVersionIDs: []string{version.ID}, CandidateSignature: candidateSignature, Round: 1,
		Audits:    []domain.RevisionAudit{{Round: 1, CandidateSignature: candidateSignature, Passed: true, CreatedAt: "2026-07-17T00:00:00Z"}},
		CreatedAt: "2026-07-17T00:00:00Z", UpdatedAt: "2026-07-17T00:00:00Z", CompletedAt: "2026-07-17T00:00:00Z",
	}
	state := map[string]any{
		"version": 1, "generation": 1, "next_session": 0, "next_version": 0,
		"sessions":          map[string]domain.RevisionSession{session.ID: session},
		"versions":          map[string]domain.ArtifactVersion{version.ID: version},
		"current_artifacts": map[string]string{version.ArtifactID: version.ID}, "receipts": map[string]any{},
	}
	cloneTestWriteJSON(t, filepath.Join(source.OutputDir, "meta", "revisions", "state.json"), state)
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	trust := storepkg.ExpansionPublicationTrust{Version: 1, Algorithm: "ed25519", ProjectID: source.ID, PublicKey: base64.StdEncoding.EncodeToString(publicKey)}
	receipt := storepkg.ExpansionPublicationReceipt{
		Version: 1, ProjectID: source.ID, Mode: domain.RevisionModeNormal,
		PolicyID: domain.NormalRevisionPolicyID, PolicyVersion: domain.NormalRevisionPolicyVersion,
		SessionID: session.ID, SessionRevision: session.Revision, PublicationGeneration: 1,
		AcceptedRevisionID: session.ID, AcceptedVersionSignature: strings.Repeat("b", 64), AcceptedVersionIDs: []string{version.ID},
		CurrentArtifacts:      []storepkg.ExpansionPublicationArtifactBinding{{ArtifactID: version.ArtifactID, ArtifactKind: version.ArtifactKind, VersionID: version.ID, ContentSignature: version.ContentSignature}},
		StructureArtifactKind: "layered_outline", StructureSchemaVersion: 1, StructureSignature: strings.Repeat("c", 64),
		PreviewSignature: session.PreviewSignature, Chapters: []storepkg.ExpansionPublicationChapterBinding{}, PublishedAt: session.CompletedAt,
	}
	unsigned := receipt
	unsigned.Signature = ""
	signedPayload, _ := json.Marshal(unsigned)
	receipt.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(privateKey, signedPayload))
	cloneTestWriteJSON(t, filepath.Join(source.OutputDir, "meta", "revisions", "expansion-publication-trust.json"), trust)
	cloneTestWriteJSON(t, filepath.Join(source.OutputDir, "meta", "revisions", "expansion-publication-receipt.json"), receipt)
	cloneTestWriteJSON(t, filepath.Join(source.OutputDir, "meta", "runtime", "expansion-publication-authority.json"), map[string]any{"private_key": "private"})

	if _, _, err := projects.CloneProjectForValidation(source.ID, filepath.Join(testTempDir(t), "validation"), "normal-authority"); err == nil || !strings.Contains(err.Error(), "validation_clone:artifact_schema_invalid") {
		t.Fatalf("validation clone accepted hand-built self-signed publication authority: %v", err)
	}
}

func TestValidationCloneRejectsCredentialHardlinkAliasAndCleansStaging(t *testing.T) {
	store := NewProjectStore(filepath.Join(testTempDir(t), "runtime"))
	source, err := store.CreateProject("Hardlink Source")
	if err != nil {
		t.Fatal(err)
	}
	credential := filepath.Join(source.RootDir, "uploads", "token.json")
	alias := filepath.Join(source.OutputDir, "chapters", "innocent.md")
	cloneTestWriteFile(t, credential, []byte("credential bytes"))
	if err := os.MkdirAll(filepath.Dir(alias), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(credential, alias); err != nil {
		t.Skipf("hardlinks unavailable: %v", err)
	}
	validationRoot := filepath.Join(testTempDir(t), "validation")
	if _, _, err := store.CloneProjectForValidation(source.ID, validationRoot, "normal-hardlink"); err == nil {
		t.Fatal("validation clone accepted a hardlink alias to an excluded credential")
	}
	entries, err := os.ReadDir(validationRoot)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".clone-normal-hardlink-") || entry.Name() == "normal-hardlink" {
			t.Fatalf("failed clone left staging artifact %s", entry.Name())
		}
	}
}

func TestValidationCloneRejectsAnyHardlinkIdentity(t *testing.T) {
	store := NewProjectStore(filepath.Join(testTempDir(t), "runtime"))
	source, err := store.CreateProject("Hardlink Owned Source")
	if err != nil {
		t.Fatal(err)
	}
	first := filepath.Join(source.OutputDir, "chapters", "first.md")
	second := filepath.Join(source.OutputDir, "chapters", "second.md")
	cloneTestWriteFile(t, first, []byte("same inode"))
	if err := os.Link(first, second); err != nil {
		t.Skipf("hardlinks unavailable: %v", err)
	}
	validationRoot := filepath.Join(testTempDir(t), "validation")
	if _, _, err := store.CloneProjectForValidation(source.ID, validationRoot, "normal-hardlink-owned"); err == nil {
		t.Fatal("validation clone accepted two allowlisted hardlink names")
	}
}

func TestValidationCloneRejectsUnknownCredentialNamesBySchema(t *testing.T) {
	store := NewProjectStore(filepath.Join(testTempDir(t), "runtime"))
	source, err := store.CreateProject("Unknown Credentials")
	if err != nil {
		t.Fatal(err)
	}
	for _, relative := range []string{"uploads/nested/login.json", "uploads/apikey.txt", "profiles/password.txt", "simulate/session.json"} {
		cloneTestWriteFile(t, filepath.Join(source.RootDir, filepath.FromSlash(relative)), []byte("private"))
	}
	cloneTestWriteFile(t, filepath.Join(source.OutputDir, "chapters", "one.md"), []byte("owned"))
	clone, _, err := store.CloneProjectForValidation(source.ID, filepath.Join(testTempDir(t), "validation"), "normal-schema")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(clone.RootDir)
	for _, relative := range []string{"uploads/nested/login.json", "uploads/apikey.txt", "profiles/password.txt", "simulate/session.json"} {
		if _, err := os.Stat(filepath.Join(clone.RootDir, filepath.FromSlash(relative))); !os.IsNotExist(err) {
			t.Fatalf("unowned credential-like file %s was cloned: %v", relative, err)
		}
	}
}

func TestValidationCloneCopiesOnlyManifestReferencedAdaptationArtifacts(t *testing.T) {
	store := NewProjectStore(filepath.Join(testTempDir(t), "runtime"))
	source, err := store.CreateProject("Exact Adaptation Schema")
	if err != nil {
		t.Fatal(err)
	}
	sourceFile := filepath.Join(source.RootDir, "uploads", "adaptation", "source.txt")
	sourceBody := []byte("complete source")
	cloneTestWriteFile(t, sourceFile, sourceBody)
	sourceDigest := sha256.Sum256(sourceBody)
	chapter := filepath.Join(source.OutputDir, "meta", "adaptation", "source_chapters", "0001.md")
	body := []byte("source chapter")
	cloneTestWriteFile(t, chapter, body)
	digest := sha256.Sum256(body)
	cloneTestWriteJSON(t, filepath.Join(source.OutputDir, "meta", "adaptation", "source_manifest.json"), map[string]any{
		"source_path":   sourceFile,
		"source_sha256": hex.EncodeToString(sourceDigest[:]),
		"chapter_count": 1,
		"chapters":      []map[string]any{{"chapter": 1, "path": chapter, "sha256": hex.EncodeToString(digest[:])}},
	})
	for _, rel := range []string{"output/meta/adaptation/private.json", "output/meta/Other.JSON"} {
		cloneTestWriteFile(t, filepath.Join(source.RootDir, filepath.FromSlash(rel)), []byte("must not copy"))
	}
	clone, _, err := store.CloneProjectForValidation(source.ID, filepath.Join(testTempDir(t), "validation"), "adaptation-exact")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(clone.RootDir)
	if got := string(cloneTestReadFile(t, filepath.Join(clone.OutputDir, "meta", "adaptation", "source_chapters", "0001.md"))); got != string(body) {
		t.Fatalf("referenced blob = %q", got)
	}
	for _, rel := range []string{"output/meta/adaptation/private.json", "output/meta/Other.JSON"} {
		if _, statErr := os.Stat(filepath.Join(clone.RootDir, filepath.FromSlash(rel))); !os.IsNotExist(statErr) {
			t.Fatalf("unknown artifact %s was copied", rel)
		}
	}
}

func TestValidationCloneRejectsTypedSchemaViolationsAnonymously(t *testing.T) {
	tests := []struct {
		name   string
		code   string
		mutate func(t *testing.T, source ProjectManifest)
	}{
		{
			name: "arbitrary revision JSON in valid path shell",
			code: "artifact_schema_invalid",
			mutate: func(t *testing.T, source ProjectManifest) {
				cloneTestWriteJSON(t, filepath.Join(source.OutputDir, "meta", "revisions", "manuscript", "private.json"), map[string]any{"private": "must not clone"})
			},
		},
		{
			name: "oversized JSON",
			code: "artifact_schema_invalid",
			mutate: func(t *testing.T, source ProjectManifest) {
				payload := []byte(`{"summary":"` + strings.Repeat("x", validationCloneMaxJSONBytes) + `"}`)
				cloneTestWriteFile(t, filepath.Join(source.OutputDir, "summaries", "1.json"), payload)
			},
		},
		{
			name: "case variant of owned path",
			code: "artifact_path_case_invalid",
			mutate: func(t *testing.T, source ProjectManifest) {
				cloneTestWriteFile(t, filepath.Join(source.OutputDir, "Chapters", "0001.md"), []byte("formal prose"))
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := NewProjectStore(filepath.Join(testTempDir(t), "runtime"))
			source, err := store.CreateProject("Typed Clone Schema")
			if err != nil {
				t.Fatal(err)
			}
			test.mutate(t, source)
			_, _, err = store.CloneProjectForValidation(source.ID, filepath.Join(testTempDir(t), "validation"), "normal-schema-invalid")
			if err == nil || !strings.Contains(err.Error(), "validation_clone:"+test.code) || strings.Contains(err.Error(), source.RootDir) {
				t.Fatalf("anonymous typed-schema error = %v", err)
			}
		})
	}
}

func TestValidationCloneRejectsUnknownFieldsInsideOwnedArtifacts(t *testing.T) {
	tests := []struct {
		name string
		rel  string
		body map[string]any
	}{
		{"summary private field", "summaries/1.json", map[string]any{"chapter": 1, "summary": "safe summary", "private": "must not clone"}},
		{"review source excerpt", "reviews/1.json", map[string]any{"chapter": 1, "verdict": "accept", "source_excerpt": "must not clone"}},
		{"formal state nested envelope", "meta/progress.json", map[string]any{"phase": "writing", "total_chapters": 1, "completed_chapters": []int{1}, "unknown": map[string]any{"private": "must not clone"}}},
		{"adaptation contract nested private field", "meta/adaptation/plan.json", map[string]any{"version": 1, "plan": map[string]any{"source_excerpt": "must not clone"}}},
		{"adaptation contract nested unknown field", "meta/adaptation/plan.json", map[string]any{"version": 1, "plan": map[string]any{"mystery": "must not clone"}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := NewProjectStore(filepath.Join(testTempDir(t), "runtime"))
			source, err := store.CreateProject("Unknown Field Envelope")
			if err != nil {
				t.Fatal(err)
			}
			cloneTestWriteJSON(t, filepath.Join(source.OutputDir, filepath.FromSlash(test.rel)), test.body)
			_, _, err = store.CloneProjectForValidation(source.ID, filepath.Join(testTempDir(t), "validation"), "unknown-fields")
			if err == nil || !strings.Contains(err.Error(), "validation_clone:artifact_schema_invalid") ||
				strings.Contains(err.Error(), "must not clone") || strings.Contains(err.Error(), source.RootDir) {
				t.Fatalf("unknown-field failure was not anonymous: %v", err)
			}
		})
	}
}

func TestValidationCloneExactSchemasRejectUnknownAtNamedNestedPaths(t *testing.T) {
	sha := strings.Repeat("a", 64)
	tests := []struct {
		name string
		rel  string
		kind string
		body map[string]any
	}{
		{
			name: "revision index provenance nested unknown",
			rel:  "output/meta/revisions/manuscript/index.json", kind: "revision",
			body: map[string]any{"revisions": map[string]any{}, "content_provenance": map[string]any{"chapter:sha": map[string]any{"chapter_id": "chapter", "content_sha256": sha, "approved_outline_sha256": sha, "mode": "normal", "private": "reject"}}},
		},
		{
			name: "publication candidate nested unknown",
			rel:  "output/meta/revisions/manuscript/publication.json", kind: "revision",
			body: map[string]any{"revision_id": "revision", "expected_revision": 1, "idempotency_key": "key", "status": "prepared", "candidate": map[string]any{"private": "reject"}},
		},
		{
			name: "adaptation command journal top unknown",
			rel:  "output/meta/revisions/adaptation-command-journal.json", kind: "revision",
			body: map[string]any{"version": 1, "key": "key", "operation": "save", "fingerprint": sha, "files": []string{"meta/adaptation/plan.json"}, "private": "reject"},
		},
		{
			name: "completion checkpoint nested unknown",
			rel:  "output/meta/progress.json", kind: "formal_state",
			body: map[string]any{"phase": "writing", "total_chapters": 1, "completed_chapters": []int{1}, "completion_revalidation": map[string]any{"version": 1, "status": "pending", "mode": "normal", "accepted_revision_id": "revision", "accepted_version_signature": sha, "previous_structure_signature": sha, "previous_stable_order": []string{"chapter"}, "current_structure_signature": sha, "current_stable_order": []string{"chapter"}, "created_at": "2026-07-17T00:00:00Z", "private": "reject"}},
		},
		{
			name: "timeline event nested unknown",
			rel:  "output/timeline.json", kind: "formal_root",
			body: map[string]any{"not_used": true},
		},
		{
			name: "chapter plan cross-kind allowed field",
			rel:  "output/drafts/1.plan.json", kind: "draft",
			body: map[string]any{"chapter": 1, "title": "chapter", "goal": "goal", "conflict": "conflict", "hook": "hook", "summary": "valid elsewhere but not in plan"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var payload []byte
			if test.name == "timeline event nested unknown" {
				payload = []byte(`[{"chapter":1,"time":"now","event":"event","unknown":"reject"}]`)
			} else {
				var err error
				payload, err = json.Marshal(test.body)
				if err != nil {
					t.Fatal(err)
				}
			}
			if err := validateCloneArtifact("", test.rel, test.kind, payload); err == nil {
				t.Fatalf("exact schema accepted unknown field at %s", test.rel)
			}
		})
	}
}

func TestValidationCloneRequiredFieldsAndNumericProgressKeys(t *testing.T) {
	validProgress := map[string]any{
		"novel_name": "book", "phase": "writing", "current_chapter": 1, "total_chapters": 1,
		"completed_chapters": []int{1}, "total_word_count": 321, "chapter_word_counts": map[string]int{"1": 321},
	}
	payload, err := json.Marshal(validProgress)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateCloneArtifact("", "output/meta/progress.json", "formal_state", payload); err != nil {
		t.Fatalf("valid numeric-key progress rejected: %v", err)
	}

	for _, test := range []struct {
		name, rel, kind string
		body            map[string]any
	}{
		{"empty continuation proposal", "output/meta/continuation/proposal.json", "continuation", map[string]any{}},
		{"workflow missing version", "output/meta/continuation/workflow.json", "continuation", map[string]any{"stage": "source_ready", "source_signature": "sha", "base_chapter_count": 1, "revision": 1}},
		{"progress noncanonical integer key", "output/meta/progress.json", "formal_state", map[string]any{"novel_name": "book", "phase": "writing", "current_chapter": 1, "total_chapters": 1, "completed_chapters": []int{1}, "total_word_count": 321, "chapter_word_counts": map[string]int{"01": 321}}},
		{"completion missing nested required", "output/meta/progress.json", "formal_state", map[string]any{"novel_name": "book", "phase": "writing", "current_chapter": 1, "total_chapters": 1, "completed_chapters": []int{1}, "total_word_count": 321, "completion_revalidation": map[string]any{"version": 1, "status": "pending", "mode": "normal", "accepted_revision_id": "revision", "accepted_version_signature": strings.Repeat("a", 64), "previous_structure_signature": strings.Repeat("a", 64), "previous_stable_order": []string{"chapter"}, "current_structure_signature": strings.Repeat("a", 64), "created_at": "2026-07-17T00:00:00Z"}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			payload, err := json.Marshal(test.body)
			if err != nil {
				t.Fatal(err)
			}
			if err := validateCloneArtifact("", test.rel, test.kind, payload); err == nil {
				t.Fatal("required/path-specific schema accepted invalid artifact")
			}
		})
	}
}

func TestValidationCloneLayeredIdentityIsRecursiveAndOrdered(t *testing.T) {
	volumeID := domain.LegacyStructureID("clone-test", domain.StructureKindVolume, "volume/1")
	arcOneID := domain.LegacyStructureID("clone-test", domain.StructureKindArc, "volume/1/arc/1")
	arcTwoID := domain.LegacyStructureID("clone-test", domain.StructureKindArc, "volume/1/arc/2")
	chapterOneID := domain.LegacyStructureID("clone-test", domain.StructureKindChapter, "chapter/1")
	chapterTwoID := domain.LegacyStructureID("clone-test", domain.StructureKindChapter, "chapter/2")
	current := []domain.VolumeOutline{{
		ID: volumeID, Index: 1, Title: "volume", Theme: "theme",
		Arcs: []domain.ArcOutline{
			{ID: arcOneID, Index: 1, Title: "arc one", Goal: "goal", Chapters: []domain.OutlineEntry{{ID: chapterOneID, Chapter: 1, Title: "one", Scenes: []string{}}}},
			{ID: arcTwoID, Index: 2, Title: "arc two", Goal: "goal", Chapters: []domain.OutlineEntry{{ID: chapterTwoID, Chapter: 2, Title: "two", Scenes: []string{}}}},
		},
	}}
	if err := validateCloneLayeredIdentity(current); err != nil {
		t.Fatalf("valid current structure rejected: %v", err)
	}
	currentPayload, _ := json.Marshal(current)
	if err := validateCloneArtifact("", "output/layered_outline.json", "formal_root", currentPayload); err != nil {
		t.Fatalf("formal layered path rejected valid current structure: %v", err)
	}
	projectionRoot := t.TempDir()
	cloneTestWriteJSON(t, filepath.Join(projectionRoot, "output", "layered_outline.json"), current)
	cloneTestWriteJSON(t, filepath.Join(projectionRoot, "output", "outline.json"), domain.FlattenOutline(current))
	projectionArtifacts := map[string]validationCloneArtifact{
		"output/layered_outline.json": {Kind: "formal_root", SchemaVersion: validationCloneSchemaVersion},
		"output/outline.json":         {Kind: "formal_root", SchemaVersion: validationCloneSchemaVersion},
	}
	if err := validateCloneStructureProjection(projectionRoot, projectionArtifacts); err != nil {
		t.Fatalf("matching flat/layered projection rejected: %v", err)
	}
	drifted := domain.FlattenOutline(current)
	drifted[0].ID = domain.LegacyStructureID("clone-test", domain.StructureKindChapter, "chapter/drift")
	cloneTestWriteJSON(t, filepath.Join(projectionRoot, "output", "outline.json"), drifted)
	if err := validateCloneStructureProjection(projectionRoot, projectionArtifacts); err == nil {
		t.Fatal("flat/layered parent projection drift was accepted")
	}
	legacySkeleton := []domain.VolumeOutline{{
		ID: volumeID, Index: 1, Title: "legacy volume", Theme: "theme",
		Arcs: []domain.ArcOutline{{ID: arcOneID, Index: 1, Title: "reserved arc", Goal: "goal", EstimatedChapters: 3, Chapters: []domain.OutlineEntry{}}},
	}}
	if err := validateCloneLayeredIdentity(legacySkeleton); err != nil {
		t.Fatalf("explicit reserved legacy skeleton rejected: %v", err)
	}
	legacyPayload, _ := json.Marshal(legacySkeleton)
	if err := validateCloneArtifact("", "output/meta/continuation/volumes.json", "continuation", legacyPayload); err != nil {
		t.Fatalf("continuation path rejected explicit reserved legacy skeleton: %v", err)
	}

	for _, test := range []struct {
		name   string
		mutate func([]domain.VolumeOutline)
	}{
		{"arc ID missing", func(value []domain.VolumeOutline) { value[0].Arcs[0].ID = "" }},
		{"chapter ID missing", func(value []domain.VolumeOutline) { value[0].Arcs[0].Chapters[0].ID = "" }},
		{"duplicate ID across parents", func(value []domain.VolumeOutline) { value[0].Arcs[1].Chapters[0].ID = value[0].Arcs[0].Chapters[0].ID }},
		{"wrong parent ownership", func(value []domain.VolumeOutline) { value[0].Arcs[1].ID = value[0].Arcs[0].ID }},
		{"arc order gap", func(value []domain.VolumeOutline) { value[0].Arcs[1].Index = 3 }},
		{"chapter display disorder", func(value []domain.VolumeOutline) { value[0].Arcs[1].Chapters[0].Chapter = 3 }},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidate := domain.CloneStructureSnapshot(current)
			test.mutate(candidate)
			payload, _ := json.Marshal(candidate)
			if err := validateCloneArtifact("", "output/layered_outline.json", "formal_root", payload); err == nil {
				t.Fatal("recursive identity contract accepted invalid structure")
			}
		})
	}
}

func TestValidationCloneRejectsWrongArtifactVersionAndType(t *testing.T) {
	for _, test := range []struct {
		name string
		body map[string]any
	}{
		{"wrong version", map[string]any{"version": 2, "chapter": 1, "summary": "safe summary"}},
		{"wrong stable type", map[string]any{"version": 1, "chapter": "one", "summary": "safe summary"}},
		{"path identity mismatch", map[string]any{"version": 1, "chapter": 2, "summary": "safe summary"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := NewProjectStore(filepath.Join(testTempDir(t), "runtime"))
			source, err := store.CreateProject("Invalid Typed Artifact")
			if err != nil {
				t.Fatal(err)
			}
			cloneTestWriteJSON(t, filepath.Join(source.OutputDir, "summaries", "1.json"), test.body)
			_, _, err = store.CloneProjectForValidation(source.ID, filepath.Join(testTempDir(t), "validation"), "invalid-artifact")
			if err == nil || !strings.Contains(err.Error(), "validation_clone:artifact_schema_invalid") || strings.Contains(err.Error(), source.RootDir) {
				t.Fatalf("typed artifact failure was not anonymous: %v", err)
			}
		})
	}
}

func TestValidationCloneRejectsInvalidAdaptationManifestReferences(t *testing.T) {
	tests := []struct {
		name   string
		code   string
		mutate func(manifest map[string]any, source ProjectManifest, sourceFile, chapter string)
	}{
		{
			name: "missing source SHA",
			code: "adaptation_reference_signature_invalid",
			mutate: func(manifest map[string]any, _ ProjectManifest, _, _ string) {
				manifest["source_sha256"] = ""
			},
		},
		{
			name: "missing chapter SHA",
			code: "adaptation_reference_signature_invalid",
			mutate: func(manifest map[string]any, _ ProjectManifest, _, _ string) {
				manifest["chapters"].([]map[string]any)[0]["sha256"] = ""
			},
		},
		{
			name: "malformed chapter SHA",
			code: "adaptation_reference_signature_invalid",
			mutate: func(manifest map[string]any, _ ProjectManifest, _, _ string) {
				manifest["chapters"].([]map[string]any)[0]["sha256"] = "not-a-sha"
			},
		},
		{
			name: "uppercase SHA",
			code: "adaptation_reference_signature_invalid",
			mutate: func(manifest map[string]any, _ ProjectManifest, _, _ string) {
				manifest["chapters"].([]map[string]any)[0]["sha256"] = strings.ToUpper(manifest["chapters"].([]map[string]any)[0]["sha256"].(string))
			},
		},
		{
			name: "duplicate reference",
			code: "adaptation_reference_duplicate",
			mutate: func(manifest map[string]any, _ ProjectManifest, _, chapter string) {
				chapters := manifest["chapters"].([]map[string]any)
				duplicate := map[string]any{"chapter": 2, "path": chapter, "sha256": chapters[0]["sha256"]}
				manifest["chapters"] = append(chapters, duplicate)
				manifest["chapter_count"] = 2
			},
		},
		{
			name: "unreferenced blob",
			code: "adaptation_blob_unreferenced",
			mutate: func(_ map[string]any, source ProjectManifest, _, _ string) {
				cloneTestWriteFile(t, filepath.Join(source.OutputDir, "meta", "adaptation", "source_chapters", "orphan.md"), []byte("orphan"))
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := NewProjectStore(filepath.Join(testTempDir(t), "runtime"))
			source, err := store.CreateProject("Adaptation Reference Schema")
			if err != nil {
				t.Fatal(err)
			}
			sourceFile := filepath.Join(source.RootDir, "uploads", "adaptation", "source.txt")
			sourceBody := []byte("whole source")
			cloneTestWriteFile(t, sourceFile, sourceBody)
			sourceDigest := sha256.Sum256(sourceBody)
			chapter := filepath.Join(source.OutputDir, "meta", "adaptation", "source_chapters", "0001.md")
			chapterBody := []byte("chapter source")
			cloneTestWriteFile(t, chapter, chapterBody)
			chapterDigest := sha256.Sum256(chapterBody)
			manifest := map[string]any{
				"source_path": sourceFile, "source_sha256": hex.EncodeToString(sourceDigest[:]), "chapter_count": 1,
				"chapters": []map[string]any{{"chapter": 1, "path": chapter, "sha256": hex.EncodeToString(chapterDigest[:])}},
			}
			test.mutate(manifest, source, sourceFile, chapter)
			cloneTestWriteJSON(t, filepath.Join(source.OutputDir, "meta", "adaptation", "source_manifest.json"), manifest)
			_, _, err = store.CloneProjectForValidation(source.ID, filepath.Join(testTempDir(t), "validation"), "adaptation-invalid")
			if err == nil || !strings.Contains(err.Error(), "validation_clone:"+test.code) || strings.Contains(err.Error(), source.RootDir) {
				t.Fatalf("anonymous adaptation-schema error = %v", err)
			}
		})
	}
}

func TestValidationCloneRejectsAdaptationReferenceSignatureAnonymously(t *testing.T) {
	store := NewProjectStore(filepath.Join(testTempDir(t), "runtime"))
	source, err := store.CreateProject("Private Named Source")
	if err != nil {
		t.Fatal(err)
	}
	sourceFile := filepath.Join(source.RootDir, "uploads", "adaptation", "source.txt")
	sourceBody := []byte("complete source")
	cloneTestWriteFile(t, sourceFile, sourceBody)
	sourceDigest := sha256.Sum256(sourceBody)
	chapter := filepath.Join(source.OutputDir, "meta", "adaptation", "source_chapters", "0001.md")
	cloneTestWriteFile(t, chapter, []byte("private prose"))
	cloneTestWriteJSON(t, filepath.Join(source.OutputDir, "meta", "adaptation", "source_manifest.json"), map[string]any{
		"source_path": sourceFile, "source_sha256": hex.EncodeToString(sourceDigest[:]), "chapter_count": 1,
		"chapters": []map[string]any{{"chapter": 1, "path": chapter, "sha256": strings.Repeat("0", 64)}},
	})
	_, _, err = store.CloneProjectForValidation(source.ID, filepath.Join(testTempDir(t), "validation"), "adaptation-bad-signature")
	if err == nil {
		t.Fatal("signature mismatch was accepted")
	}
	message := err.Error()
	if !strings.Contains(message, "validation_clone:adaptation_reference_signature_mismatch") || strings.Contains(message, "secret-name") || strings.Contains(message, source.RootDir) {
		t.Fatalf("non-anonymous validation error: %s", message)
	}
}

func TestValidationCloneIdentityScanIsLinearForLargeOwnedTree(t *testing.T) {
	store := NewProjectStore(filepath.Join(testTempDir(t), "runtime"))
	source, err := store.CreateProject("Large Schema Tree")
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 400; index++ {
		cloneTestWriteFile(t, filepath.Join(source.OutputDir, "chapters", fmt.Sprintf("%04d.md", index)), []byte("owned chapter"))
	}
	started := time.Now()
	clone, report, err := store.CloneProjectForValidation(source.ID, filepath.Join(testTempDir(t), "validation"), "normal-large-tree")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(clone.RootDir)
	if report.FileCount < 400 {
		t.Fatalf("large clone report omitted owned files: %+v", report)
	}
	if elapsed := time.Since(started); elapsed > 10*time.Second {
		t.Fatalf("linear identity scan exceeded budget: %s", elapsed)
	}
}

func TestProjectStoreStartupRecoveryFailureBlocksEveryProjectEntry(t *testing.T) {
	runtimeRoot := filepath.Join(testTempDir(t), "runtime")
	store := NewProjectStore(runtimeRoot)
	store.startupErr = errors.New("corrupt recovery journal at private path")
	if _, err := store.ListProjects(); !errors.Is(err, ErrProjectStartupRecovery) {
		t.Fatalf("ListProjects error = %v", err)
	}
	if _, err := store.CreateProject("blocked"); !errors.Is(err, ErrProjectStartupRecovery) {
		t.Fatalf("CreateProject error = %v", err)
	}
	if _, err := store.OpenProject("blocked"); !errors.Is(err, ErrProjectStartupRecovery) {
		t.Fatalf("OpenProject error = %v", err)
	}
	if _, err := store.CloneProject("blocked", "copy"); !errors.Is(err, ErrProjectStartupRecovery) {
		t.Fatalf("CloneProject error = %v", err)
	}
	server := NewServer(testWebConfig(t), assets.Load("default"), runtimeRoot)
	defer server.Close()
	server.store.startupErr = store.startupErr
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/api/projects", bytes.NewBufferString(`{"name":"blocked"}`)))
	if recorder.Code != http.StatusServiceUnavailable || strings.Contains(recorder.Body.String(), "private path") || !strings.Contains(recorder.Body.String(), `"code":"startup_recovery_required"`) {
		t.Fatalf("startup envelope status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestValidationCloneRejectsWindowsAncestorJunctionIntoSource(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows junction regression")
	}
	store := NewProjectStore(filepath.Join(testTempDir(t), "runtime"))
	source, err := store.CreateProject("Junction Source")
	if err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(testTempDir(t), "source-junction")
	if output, err := exec.Command("cmd", "/c", "mklink", "/J", alias, source.RootDir).CombinedOutput(); err != nil {
		t.Skipf("junctions unavailable: %v (%s)", err, output)
	}
	t.Cleanup(func() { _ = os.Remove(alias) })
	validationRoot := filepath.Join(alias, "validation")
	if _, _, err := store.CloneProjectForValidation(source.ID, validationRoot, "normal-junction"); err == nil {
		t.Fatal("validation clone accepted an ancestor junction resolving into source")
	}
	if _, err := os.Stat(filepath.Join(source.RootDir, "validation")); !os.IsNotExist(err) {
		t.Fatalf("junction bypass wrote into source: %v", err)
	}
}

func TestValidationCloneRejectsRootResolvingIntoSource(t *testing.T) {
	store := NewProjectStore(filepath.Join(testTempDir(t), "runtime"))
	source, err := store.CreateProject("Symlink Source")
	if err != nil {
		t.Fatal(err)
	}
	aliasRoot := filepath.Join(testTempDir(t), "source-alias")
	if err := os.Symlink(source.RootDir, aliasRoot); err != nil {
		t.Skipf("symbolic links unavailable: %v", err)
	}
	validationRoot := filepath.Join(aliasRoot, "validation")
	if _, _, err := store.CloneProjectForValidation(source.ID, validationRoot, "normal-a1b2c3"); err == nil {
		t.Fatal("validation clone accepted a root resolving into source")
	}
	if _, err := os.Stat(filepath.Join(source.RootDir, "validation")); !os.IsNotExist(err) {
		t.Fatalf("validation root was created inside source: %v", err)
	}
}

func TestValidationCloneSnapshotRejectsABARewrite(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "project.json")
	original := []byte(`{"version":1}`)
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	snapshot, err := captureValidationCloneSnapshot(root, map[string]validationCloneArtifact{"project.json": {Kind: "project_manifest", SchemaVersion: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"version":2}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := verifyValidationCloneSnapshot(root, snapshot); err == nil {
		t.Fatal("source A-B-A rewrite preserved digest but escaped generation validation")
	}
}

func TestCloneProjectHandlerCreatesIndependentProject(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()
	source, err := server.store.CreateProject("HTTP Source")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	cloneTestWriteFile(t, filepath.Join(source.OutputDir, "chapters", "chapter.md"), []byte("http clone body"))

	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+source.ID+"/clone", bytes.NewBufferString(`{"name":"HTTP Copy"}`))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("clone status = %d body=%s", rec.Code, rec.Body.String())
	}
	var response struct {
		Project         ProjectManifest `json:"project"`
		SourceProjectID string          `json:"source_project_id"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatalf("decode clone response: %v", err)
	}
	if response.Project.ID == "" || response.Project.ID == source.ID || response.Project.Name != "HTTP Copy" {
		t.Fatalf("clone response = %+v", response)
	}
	if response.SourceProjectID != source.ID {
		t.Fatalf("source_project_id = %q, want %q", response.SourceProjectID, source.ID)
	}
	if got := string(cloneTestReadFile(t, filepath.Join(response.Project.OutputDir, "chapters", "chapter.md"))); got != "http clone body" {
		t.Fatalf("HTTP cloned chapter = %q", got)
	}
	revisions := storepkg.NewRevisionStore(response.Project.OutputDir)
	lease, err := revisions.AcquireNormalFlow("clone-rollback")
	if err != nil {
		t.Fatalf("cloned project retained source Web action lease: %v", err)
	}
	if err := revisions.ReleaseNormalFlow(lease.Token); err != nil {
		t.Fatalf("release cloned project lease: %v", err)
	}
}

func TestCloneProjectHandlerRejectsRunningProject(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()
	source, err := server.store.CreateProject("Running Source")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	fake := installFakeSession(t, server, source)
	fake.snapshot = host.UISnapshot{IsRunning: true}

	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+source.ID+"/clone", bytes.NewBufferString(`{"name":"Rejected Copy"}`))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("clone running project status = %d body=%s", rec.Code, rec.Body.String())
	}
	projects, err := server.store.ListProjects()
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	if len(projects) != 1 || projects[0].ID != source.ID {
		t.Fatalf("projects after rejected clone = %+v", projects)
	}
}

func cloneTestWriteJSON(t *testing.T, path string, value any) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal %s: %v", path, err)
	}
	cloneTestWriteFile(t, path, data)
}

func cloneTestWriteFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create parent for %s: %v", path, err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func cloneTestReadFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return data
}

func cloneTestAssertSourcePath(t *testing.T, path, want string) {
	t.Helper()
	var payload struct {
		SourcePath string `json:"source_path"`
	}
	if err := json.Unmarshal(cloneTestReadFile(t, path), &payload); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	if filepath.Clean(payload.SourcePath) != filepath.Clean(want) {
		t.Fatalf("%s source_path = %q, want %q", path, payload.SourcePath, want)
	}
}

func cloneTestProjectEntries(t *testing.T, projectsDir string) []string {
	t.Helper()
	entries, err := os.ReadDir(projectsDir)
	if err != nil {
		t.Fatalf("read projects dir: %v", err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}
