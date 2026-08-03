package web

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/voocel/ainovel-cli/internal/bootstrap"
)

const legacyImportMarkerPath = ".ainovel/legacy-import.json"

const (
	legacyMigrationJournalVersion = 2
	legacyImportMarkerVersion     = 2
)

var (
	errLegacySourceRequired = errors.New("source_dir is required")
	errLegacySourceMissing  = errors.New("legacy source directory does not exist")
	errLegacySourceInvalid  = errors.New("legacy source directory is invalid")
	legacyMigrationMu       sync.Mutex
)

type legacyMigrationRequest struct {
	SourceDir            string `json:"source_dir"`
	Name                 string `json:"name"`
	ExpectedSourceSHA256 string `json:"expected_source_sha256"`
}

type legacyMigrationResult struct {
	Project      ProjectManifest `json:"project"`
	Created      bool            `json:"created"`
	SourceHash   string          `json:"source_hash"`
	CopiedFiles  int             `json:"copied_files"`
	SkippedFiles []string        `json:"skipped_files,omitempty"`
}

type legacyImportMarker struct {
	Version     int       `json:"version"`
	SourceDir   string    `json:"source_dir"`
	SourceHash  string    `json:"source_hash"`
	ImportedAt  time.Time `json:"imported_at"`
	CopiedFiles int       `json:"copied_files"`
}

type legacyMigrationJournal struct {
	Version              int       `json:"version"`
	ProjectID            string    `json:"project_id"`
	StagingRoot          string    `json:"staging_root"`
	FinalRoot            string    `json:"final_root"`
	SourceDir            string    `json:"source_dir"`
	ExpectedSourceSHA256 string    `json:"expected_source_sha256"`
	CreatedAt            time.Time `json:"created_at"`
}

type legacyImportEntry struct {
	RelativePath string
	SourcePath   string
	Mode         fs.FileMode
	IsDir        bool
	Content      []byte
}

type legacyImportPlan struct {
	SourceDir    string
	Entries      []legacyImportEntry
	SafeConfig   []byte
	SkippedFiles []string
	SourceHash   string
}

type LegacyMigrationPreview struct {
	SourceSHA256 string   `json:"source_sha256"`
	FileCount    int      `json:"file_count"`
	SkippedFiles []string `json:"skipped_files,omitempty"`
}

// DryRunLegacyProjectMigration validates and hashes an explicitly selected
// legacy directory without creating a project or writing into the source.
func (s *ProjectStore) DryRunLegacyProjectMigration(sourceDir string) (LegacyMigrationPreview, error) {
	if err := s.requireStartupRecovery(); err != nil {
		return LegacyMigrationPreview{}, err
	}
	plan, err := s.buildLegacyImportPlan(sourceDir)
	if err != nil {
		return LegacyMigrationPreview{}, err
	}
	files := 0
	for _, entry := range plan.Entries {
		if !entry.IsDir {
			files++
		}
	}
	return LegacyMigrationPreview{SourceSHA256: plan.SourceHash, FileCount: files, SkippedFiles: append([]string(nil), plan.SkippedFiles...)}, nil
}

// RollbackLegacyProjectMigration removes only a project created by the legacy
// importer and only when its durable marker matches the caller's dry-run hash.
// It never modifies the legacy source directory.
func (s *ProjectStore) RollbackLegacyProjectMigration(projectID, expectedSourceHash string) error {
	if err := s.requireStartupRecovery(); err != nil {
		return err
	}
	legacyMigrationMu.Lock()
	defer legacyMigrationMu.Unlock()
	manifest, err := s.projectManifestWithoutTouch(strings.TrimSpace(projectID))
	if err != nil {
		return err
	}
	payload, err := os.ReadFile(filepath.Join(manifest.RootDir, filepath.FromSlash(legacyImportMarkerPath)))
	if err != nil {
		return fmt.Errorf("project is not a staged legacy migration: %w", err)
	}
	var marker legacyImportMarker
	if err := json.Unmarshal(payload, &marker); err != nil {
		return fmt.Errorf("invalid legacy migration marker: %w", err)
	}
	if marker.Version != legacyImportMarkerVersion || len(expectedSourceHash) != 64 || marker.SourceHash != expectedSourceHash {
		return fmt.Errorf("legacy migration rollback checksum mismatch")
	}
	return removeAllWithRetry(manifest.RootDir)
}

func (s *Server) handleLegacyProjectMigration(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req legacyMigrationRequest
	if err := decodeJSONBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid migration request: "+err.Error())
		return
	}
	result, err := s.store.MigrateLegacyProject(req.SourceDir, req.Name, req.ExpectedSourceSHA256)
	if err != nil {
		switch {
		case errors.Is(err, errLegacySourceRequired), errors.Is(err, errLegacySourceInvalid):
			writeError(w, http.StatusBadRequest, err.Error())
		case errors.Is(err, errLegacySourceMissing):
			writeError(w, http.StatusNotFound, err.Error())
		default:
			writeError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}
	status := http.StatusCreated
	if !result.Created {
		status = http.StatusOK
	}
	writeJSON(w, status, result)
}

// MigrateLegacyProject imports one explicitly selected legacy output directory.
// It never writes into an existing project and treats a matching content hash as
// an idempotent retry.
func (s *ProjectStore) MigrateLegacyProject(sourceDir, name, expectedSourceSHA256 string) (legacyMigrationResult, error) {
	if err := s.requireStartupRecovery(); err != nil {
		return legacyMigrationResult{}, err
	}
	plan, err := s.buildLegacyImportPlan(sourceDir)
	if err != nil {
		return legacyMigrationResult{}, err
	}
	expectedSourceSHA256 = strings.ToLower(strings.TrimSpace(expectedSourceSHA256))
	if len(expectedSourceSHA256) != 64 || expectedSourceSHA256 != plan.SourceHash {
		return legacyMigrationResult{}, fmt.Errorf("%w: source does not match expected dry-run SHA-256", errLegacySourceInvalid)
	}

	legacyMigrationMu.Lock()
	defer legacyMigrationMu.Unlock()
	if err := s.recoverLegacyMigrationJournals(); err != nil {
		return legacyMigrationResult{}, err
	}

	if existing, marker, found, err := s.findLegacyImport(plan.SourceHash); err != nil {
		return legacyMigrationResult{}, err
	} else if found {
		return legacyMigrationResult{
			Project:     existing,
			Created:     false,
			SourceHash:  marker.SourceHash,
			CopiedFiles: marker.CopiedFiles,
		}, nil
	}

	name = strings.TrimSpace(name)
	if name == "" {
		name = filepath.Base(plan.SourceDir)
	}
	if err := EnsureRuntimeRoot(s.RuntimeRoot); err != nil {
		return legacyMigrationResult{}, err
	}
	if err := os.MkdirAll(s.ProjectsDir(), 0o755); err != nil {
		return legacyMigrationResult{}, err
	}
	now := time.Now().UTC()
	projectID, err := newProjectID(name, now)
	if err != nil {
		return legacyMigrationResult{}, err
	}
	finalRoot := filepath.Join(s.ProjectsDir(), projectID)
	stagingRoot, err := os.MkdirTemp(s.ProjectsDir(), ".legacy-"+projectID+"-")
	if err != nil {
		return legacyMigrationResult{}, fmt.Errorf("create legacy migration staging root: %w", err)
	}
	manifest := ProjectManifest{Version: manifestVersion, ID: projectID, Name: name, RootDir: finalRoot, OutputDir: filepath.Join(finalRoot, "output"), CreatedAt: now, UpdatedAt: now, LastAccessedAt: now}
	stagingOutput := filepath.Join(stagingRoot, "output")
	journal := legacyMigrationJournal{Version: legacyMigrationJournalVersion, ProjectID: projectID, StagingRoot: stagingRoot, FinalRoot: finalRoot, SourceDir: plan.SourceDir, ExpectedSourceSHA256: expectedSourceSHA256, CreatedAt: now}
	journalPath, err := s.writeLegacyMigrationJournal(journal)
	if err != nil {
		_ = removeAllWithRetry(stagingRoot)
		return legacyMigrationResult{}, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = removeAllWithRetry(stagingRoot)
			_ = removeAllWithRetry(finalRoot)
			_ = os.Remove(journalPath)
		}
	}()

	copied, copiedHash, err := copyLegacyImportPlan(plan, stagingOutput)
	if err != nil {
		return legacyMigrationResult{}, err
	}
	if copiedHash != plan.SourceHash {
		return legacyMigrationResult{}, fmt.Errorf("%w: source changed while it was being imported", errLegacySourceInvalid)
	}
	if len(plan.SafeConfig) > 0 {
		if err := writeFileAtomically(filepath.Join(stagingRoot, ".ainovel", "config.json"), plan.SafeConfig, 0o600); err != nil {
			return legacyMigrationResult{}, fmt.Errorf("write sanitized project config: %w", err)
		}
	}
	marker := legacyImportMarker{
		Version:     legacyImportMarkerVersion,
		SourceDir:   plan.SourceDir,
		SourceHash:  plan.SourceHash,
		ImportedAt:  time.Now().UTC(),
		CopiedFiles: copied,
	}
	if err := writeLegacyImportMarkerAt(stagingRoot, marker); err != nil {
		return legacyMigrationResult{}, err
	}
	if err := writeProjectManifestAt(stagingRoot, manifest); err != nil {
		return legacyMigrationResult{}, fmt.Errorf("write staged project manifest: %w", err)
	}
	if err := verifyStagedLegacyMigration(stagingRoot, marker); err != nil {
		return legacyMigrationResult{}, err
	}
	if err := os.Rename(stagingRoot, finalRoot); err != nil {
		return legacyMigrationResult{}, fmt.Errorf("atomically install legacy migration: %w", err)
	}
	stagingRoot = ""
	// The project is now a fully verified atomic install. A journal cleanup
	// failure must never roll it back; startup recovery recognizes its marker.
	committed = true
	if err := os.Remove(journalPath); err != nil && !os.IsNotExist(err) {
		return legacyMigrationResult{}, fmt.Errorf("complete legacy migration journal: %w", err)
	}
	return legacyMigrationResult{
		Project:      manifest,
		Created:      true,
		SourceHash:   plan.SourceHash,
		CopiedFiles:  copied,
		SkippedFiles: plan.SkippedFiles,
	}, nil
}

func (s *ProjectStore) legacyMigrationJournalDir() string {
	return filepath.Join(s.RuntimeRoot, "migrations", "legacy")
}

func (s *ProjectStore) writeLegacyMigrationJournal(journal legacyMigrationJournal) (string, error) {
	if err := os.MkdirAll(s.legacyMigrationJournalDir(), 0o700); err != nil {
		return "", err
	}
	data, err := json.MarshalIndent(journal, "", "  ")
	if err != nil {
		return "", err
	}
	path := filepath.Join(s.legacyMigrationJournalDir(), journal.ProjectID+".json")
	if err := writeFileAtomically(path, data, 0o600); err != nil {
		return "", fmt.Errorf("write legacy migration journal: %w", err)
	}
	return path, nil
}

func (s *ProjectStore) recoverLegacyMigrationJournals() error {
	entries, err := os.ReadDir(s.legacyMigrationJournalDir())
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read legacy migration journals: %w", err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(strings.ToLower(entry.Name()), ".json") {
			continue
		}
		path := filepath.Join(s.legacyMigrationJournalDir(), entry.Name())
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		var journal legacyMigrationJournal
		if json.Unmarshal(data, &journal) != nil || journal.Version != legacyMigrationJournalVersion ||
			validateProjectID(journal.ProjectID) != nil || !isSameOrChild(s.ProjectsDir(), journal.StagingRoot) ||
			!isSameOrChild(s.ProjectsDir(), journal.FinalRoot) || filepath.Base(journal.FinalRoot) != journal.ProjectID ||
			!strings.HasPrefix(filepath.Base(journal.StagingRoot), ".legacy-"+journal.ProjectID+"-") {
			return fmt.Errorf("invalid legacy migration journal %s", entry.Name())
		}
		if marker, markerErr := readLegacyImportMarkerAt(journal.FinalRoot); markerErr == nil &&
			marker.Version == legacyImportMarkerVersion && marker.SourceHash == journal.ExpectedSourceSHA256 {
			if verifyErr := verifyStagedLegacyMigration(journal.FinalRoot, marker); verifyErr != nil {
				if err := removeAllWithRetry(journal.FinalRoot); err != nil {
					return err
				}
			} else {
				_ = removeAllWithRetry(journal.StagingRoot)
				if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
					return err
				}
				continue
			}
		}
		if err := removeAllWithRetry(journal.StagingRoot); err != nil {
			return err
		}
		if err := removeAllWithRetry(journal.FinalRoot); err != nil {
			return err
		}
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

func verifyStagedLegacyMigration(root string, marker legacyImportMarker) error {
	actual, err := readLegacyImportMarkerAt(root)
	if err != nil {
		return fmt.Errorf("verify staged legacy migration marker: %w", err)
	}
	if actual.Version != legacyImportMarkerVersion || actual.SourceHash != marker.SourceHash || actual.CopiedFiles != marker.CopiedFiles {
		return fmt.Errorf("verify staged legacy migration marker: mismatch")
	}
	if _, err := readProjectManifest(filepath.Join(root, "project.json")); err != nil {
		return fmt.Errorf("verify staged project manifest: %w", err)
	}
	payloadHash, copiedFiles, err := hashLegacyStagedPayload(root)
	if err != nil {
		return fmt.Errorf("verify staged legacy migration payload: %w", err)
	}
	if payloadHash != marker.SourceHash || copiedFiles != marker.CopiedFiles {
		return fmt.Errorf("verify staged legacy migration payload: path, type, or content mismatch")
	}
	return nil
}

func hashLegacyStagedPayload(root string) (string, int, error) {
	type stagedEntry struct {
		rel  string
		path string
		info fs.FileInfo
	}
	outputRoot := filepath.Join(root, "output")
	entries := make([]stagedEntry, 0)
	err := filepath.WalkDir(outputRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(outputRoot, path)
		if err != nil || rel == "." {
			return err
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || (!info.IsDir() && !info.Mode().IsRegular()) {
			return fmt.Errorf("unsupported staged entry type: %s", rel)
		}
		entries = append(entries, stagedEntry{rel: filepath.ToSlash(rel), path: path, info: info})
		return nil
	})
	if os.IsNotExist(err) {
		return "", 0, fmt.Errorf("missing staged output")
	}
	if err != nil {
		return "", 0, err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].rel < entries[j].rel })
	h := sha256.New()
	files := 0
	for _, entry := range entries {
		kind := "file"
		if entry.info.IsDir() {
			kind = "dir"
		}
		if err := hashLegacyCanonicalEntry(h, kind, entry.rel, entry.info.Mode().Perm(), entry.info.Size(), entry.path); err != nil {
			return "", 0, err
		}
		if entry.info.IsDir() {
			continue
		}
		files++
	}
	configPath := filepath.Join(root, ".ainovel", "config.json")
	if info, err := os.Stat(configPath); err == nil {
		if err := hashLegacyCanonicalEntry(h, "config", ".ainovel/config.json", info.Mode().Perm(), info.Size(), configPath); err != nil {
			return "", 0, err
		}
	} else if !os.IsNotExist(err) {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), files, nil
}

func (s *ProjectStore) buildLegacyImportPlan(sourceDir string) (legacyImportPlan, error) {
	sourceDir = strings.TrimSpace(sourceDir)
	if sourceDir == "" {
		return legacyImportPlan{}, errLegacySourceRequired
	}
	absSource, err := filepath.Abs(sourceDir)
	if err != nil {
		return legacyImportPlan{}, fmt.Errorf("%w: resolve source directory: %v", errLegacySourceInvalid, err)
	}
	absSource = filepath.Clean(absSource)
	info, err := os.Lstat(absSource)
	if err != nil {
		if os.IsNotExist(err) {
			return legacyImportPlan{}, fmt.Errorf("%w: %s", errLegacySourceMissing, absSource)
		}
		return legacyImportPlan{}, fmt.Errorf("inspect source directory: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return legacyImportPlan{}, fmt.Errorf("%w: source must be a real directory, not a link", errLegacySourceInvalid)
	}
	resolved, err := filepath.EvalSymlinks(absSource)
	if err != nil {
		return legacyImportPlan{}, fmt.Errorf("%w: resolve source directory: %v", errLegacySourceInvalid, err)
	}
	absSource, err = filepath.Abs(resolved)
	if err != nil {
		return legacyImportPlan{}, fmt.Errorf("%w: resolve source directory: %v", errLegacySourceInvalid, err)
	}
	absSource = filepath.Clean(absSource)
	runtimeRoot, err := canonicalPathForContainment(s.RuntimeRoot)
	if err != nil {
		return legacyImportPlan{}, fmt.Errorf("resolve runtime root: %w", err)
	}
	if pathsOverlap(absSource, runtimeRoot) {
		return legacyImportPlan{}, fmt.Errorf("%w: source directory must be outside the Web runtime root", errLegacySourceInvalid)
	}

	plan := legacyImportPlan{SourceDir: absSource}
	recognized := false
	err = filepath.WalkDir(absSource, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(absSource, path)
		if err != nil || rel == ".." || filepath.IsAbs(rel) || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return fmt.Errorf("%w: path escapes source directory", errLegacySourceInvalid)
		}
		if rel == "." {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: symbolic links are not allowed: %s", errLegacySourceInvalid, rel)
		}
		if entry.IsDir() {
			plan.Entries = append(plan.Entries, legacyImportEntry{RelativePath: rel, SourcePath: path, Mode: info.Mode(), IsDir: true})
			if isRecognizedLegacyTopLevel(rel) {
				recognized = true
			}
			return nil
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("%w: special files are not allowed: %s", errLegacySourceInvalid, rel)
		}
		if isLegacyConfigPath(rel) {
			if len(plan.SafeConfig) == 0 {
				sanitized, err := sanitizeLegacyConfig(path)
				if err == nil {
					plan.SafeConfig = sanitized
				} else {
					plan.SkippedFiles = append(plan.SkippedFiles, filepath.ToSlash(rel)+" (unsafe or invalid config)")
				}
			} else {
				plan.SkippedFiles = append(plan.SkippedFiles, filepath.ToSlash(rel)+" (duplicate config)")
			}
			return nil
		}
		plan.Entries = append(plan.Entries, legacyImportEntry{RelativePath: rel, SourcePath: path, Mode: info.Mode()})
		if isRecognizedLegacyTopLevel(rel) {
			recognized = true
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, errLegacySourceInvalid) {
			return legacyImportPlan{}, err
		}
		return legacyImportPlan{}, fmt.Errorf("scan legacy source: %w", err)
	}
	if !recognized {
		return legacyImportPlan{}, fmt.Errorf("%w: directory does not contain recognizable novel output", errLegacySourceInvalid)
	}
	sort.Slice(plan.Entries, func(i, j int) bool { return plan.Entries[i].RelativePath < plan.Entries[j].RelativePath })
	plan.SourceHash, err = hashLegacyImportPlan(plan)
	if err != nil {
		return legacyImportPlan{}, err
	}
	return plan, nil
}

func sanitizeLegacyConfig(path string) ([]byte, error) {
	cfg, err := bootstrap.LoadConfigFile(path)
	if err != nil {
		return nil, err
	}
	cfg.OutputDir = ""
	cfg.PersistPath = ""
	cfg.PersistProjectOverlay = false
	cfg.PersistProviders = nil
	cfg.PersistProjectConfig = nil
	cfg.ProjectOwnedProviders = nil
	cfg.RuntimeRoot = ""
	cfg.Proxy = ""
	cfg.Notify = bootstrap.NotifyConfig{}
	for name, provider := range cfg.Providers {
		cfg.Providers[name] = bootstrap.ProviderConfig{Label: provider.Label, Models: append([]string(nil), provider.Models...)}
	}
	return json.MarshalIndent(cfg, "", "  ")
}

func hashLegacyImportPlan(plan legacyImportPlan) (string, error) {
	h := sha256.New()
	for _, entry := range plan.Entries {
		if err := hashLegacyEntry(h, entry, entry.SourcePath); err != nil {
			return "", fmt.Errorf("hash legacy source %s: %w", entry.RelativePath, err)
		}
	}
	if len(plan.SafeConfig) > 0 {
		hashLegacyCanonicalBytes(h, "config", ".ainovel/config.json", 0o600, plan.SafeConfig)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func copyLegacyImportPlan(plan legacyImportPlan, destination string) (int, string, error) {
	h := sha256.New()
	copied := 0
	for _, entry := range plan.Entries {
		if err := validateLegacyEntryBeforeCopy(plan.SourceDir, entry); err != nil {
			return 0, "", err
		}
		target, err := safeLegacyDestination(destination, entry.RelativePath)
		if err != nil {
			return 0, "", err
		}
		if entry.IsDir {
			if err := os.MkdirAll(target, entry.Mode.Perm()); err != nil {
				return 0, "", fmt.Errorf("create imported directory %s: %w", entry.RelativePath, err)
			}
			hashLegacyCanonicalBytes(h, "dir", entry.RelativePath, entry.Mode.Perm(), nil)
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return 0, "", err
		}
		if err := copyLegacyFile(entry.SourcePath, target, entry.Mode.Perm(), h, entry.RelativePath); err != nil {
			return 0, "", err
		}
		copied++
	}
	if len(plan.SafeConfig) > 0 {
		hashLegacyCanonicalBytes(h, "config", ".ainovel/config.json", 0o600, plan.SafeConfig)
	}
	return copied, hex.EncodeToString(h.Sum(nil)), nil
}

func validateLegacyEntryBeforeCopy(sourceRoot string, entry legacyImportEntry) error {
	info, err := os.Lstat(entry.SourcePath)
	if err != nil {
		return fmt.Errorf("%w: source changed before copy: %s", errLegacySourceInvalid, entry.RelativePath)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%w: symbolic links are not allowed: %s", errLegacySourceInvalid, entry.RelativePath)
	}
	if entry.IsDir != info.IsDir() || (!entry.IsDir && !info.Mode().IsRegular()) {
		return fmt.Errorf("%w: source entry type changed before copy: %s", errLegacySourceInvalid, entry.RelativePath)
	}
	resolved, err := filepath.EvalSymlinks(entry.SourcePath)
	if err != nil {
		return fmt.Errorf("%w: resolve source entry %s: %v", errLegacySourceInvalid, entry.RelativePath, err)
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil || !isSameOrChild(sourceRoot, resolved) || !sameFilesystemPath(entry.SourcePath, resolved) {
		return fmt.Errorf("%w: source entry resolves outside its declared path: %s", errLegacySourceInvalid, entry.RelativePath)
	}
	return nil
}

func copyLegacyFile(source, destination string, mode fs.FileMode, h hash.Hash, relativePath string) error {
	in, err := os.Open(source)
	if err != nil {
		return fmt.Errorf("open legacy file %s: %w", relativePath, err)
	}
	defer in.Close()
	out, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return fmt.Errorf("create imported file %s: %w", relativePath, err)
	}
	remove := true
	defer func() {
		_ = out.Close()
		if remove {
			_ = os.Remove(destination)
		}
	}()
	contentHash := sha256.New()
	if _, err := io.Copy(io.MultiWriter(out, contentHash), in); err != nil {
		return fmt.Errorf("copy legacy file %s: %w", relativePath, err)
	}
	if err := out.Close(); err != nil {
		return err
	}
	info, err := os.Stat(destination)
	if err != nil {
		return err
	}
	hashLegacyCanonicalDigest(h, "file", relativePath, mode, info.Size(), contentHash.Sum(nil))
	remove = false
	return nil
}

func hashLegacyEntry(h hash.Hash, entry legacyImportEntry, source string) error {
	kind := "file"
	if entry.IsDir {
		kind = "dir"
	}
	info, err := os.Stat(source)
	if err != nil {
		return err
	}
	return hashLegacyCanonicalEntry(h, kind, entry.RelativePath, info.Mode().Perm(), info.Size(), source)
}

func hashLegacyCanonicalEntry(h hash.Hash, kind, relativePath string, mode fs.FileMode, size int64, source string) error {
	if kind == "dir" {
		hashLegacyCanonicalBytes(h, kind, relativePath, mode, nil)
		return nil
	}
	file, err := os.Open(source)
	if err != nil {
		return err
	}
	defer file.Close()
	contentHash := sha256.New()
	if _, err := io.Copy(contentHash, file); err != nil {
		return err
	}
	hashLegacyCanonicalDigest(h, kind, relativePath, mode, size, contentHash.Sum(nil))
	return nil
}

func hashLegacyCanonicalBytes(h hash.Hash, kind, relativePath string, mode fs.FileMode, content []byte) {
	digest := sha256.Sum256(content)
	hashLegacyCanonicalDigest(h, kind, relativePath, mode, int64(len(content)), digest[:])
}

func hashLegacyCanonicalDigest(h hash.Hash, kind, relativePath string, mode fs.FileMode, size int64, contentDigest []byte) {
	writeLegacyLengthPrefixed(h, []byte(strings.ToLower(strings.TrimSpace(kind))))
	writeLegacyLengthPrefixed(h, []byte(filepath.ToSlash(filepath.Clean(relativePath))))
	var scalar [8]byte
	mode = canonicalLegacyMode(kind, mode)
	binary.BigEndian.PutUint64(scalar[:], uint64(mode.Perm()))
	writeLegacyLengthPrefixed(h, scalar[:])
	if kind == "dir" {
		size = 0
	}
	binary.BigEndian.PutUint64(scalar[:], uint64(size))
	writeLegacyLengthPrefixed(h, scalar[:])
	writeLegacyLengthPrefixed(h, contentDigest)
}

func canonicalLegacyMode(kind string, mode fs.FileMode) fs.FileMode {
	if runtime.GOOS != "windows" {
		return mode.Perm()
	}
	switch kind {
	case "dir":
		return 0o755
	case "config":
		return 0o600
	default:
		return 0o644
	}
}

func writeLegacyLengthPrefixed(h hash.Hash, value []byte) {
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(value)))
	_, _ = h.Write(length[:])
	_, _ = h.Write(value)
}

func safeLegacyDestination(root, relativePath string) (string, error) {
	target := filepath.Join(root, relativePath)
	if !isSameOrChild(root, target) || sameFilesystemPath(root, target) {
		return "", fmt.Errorf("%w: invalid relative path %q", errLegacySourceInvalid, relativePath)
	}
	return target, nil
}

func (s *ProjectStore) findLegacyImport(sourceHash string) (ProjectManifest, legacyImportMarker, bool, error) {
	projects, err := s.ListProjects()
	if err != nil {
		return ProjectManifest{}, legacyImportMarker{}, false, err
	}
	for _, manifest := range projects {
		data, err := os.ReadFile(filepath.Join(manifest.RootDir, filepath.FromSlash(legacyImportMarkerPath)))
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return ProjectManifest{}, legacyImportMarker{}, false, err
		}
		var marker legacyImportMarker
		if err := json.Unmarshal(data, &marker); err != nil {
			continue
		}
		if marker.SourceHash == sourceHash {
			return manifest, marker, true, nil
		}
	}
	return ProjectManifest{}, legacyImportMarker{}, false, nil
}

func writeLegacyImportMarker(manifest ProjectManifest, marker legacyImportMarker) error {
	return writeLegacyImportMarkerAt(manifest.RootDir, marker)
}

func writeLegacyImportMarkerAt(root string, marker legacyImportMarker) error {
	data, err := json.MarshalIndent(marker, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(root, filepath.FromSlash(legacyImportMarkerPath))
	if err := writeFileAtomically(path, data, 0o600); err != nil {
		return fmt.Errorf("write legacy import marker: %w", err)
	}
	return nil
}

func readLegacyImportMarkerAt(root string) (legacyImportMarker, error) {
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(legacyImportMarkerPath)))
	if err != nil {
		return legacyImportMarker{}, err
	}
	var marker legacyImportMarker
	if err := json.Unmarshal(data, &marker); err != nil {
		return legacyImportMarker{}, err
	}
	return marker, nil
}

func writeProjectManifestAt(root string, manifest ProjectManifest) error {
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomically(filepath.Join(root, "project.json"), data, 0o644)
}

func writeFileAtomically(path string, data []byte, mode fs.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

func isRecognizedLegacyTopLevel(relativePath string) bool {
	top := strings.ToLower(strings.Split(filepath.ToSlash(relativePath), "/")[0])
	switch top {
	case "chapters", "drafts", "reviews", "summaries", "meta", "progress.json", "outline.json", "layered_outline.json", "premise.md", "premise.txt":
		return true
	default:
		return false
	}
}

func isLegacyConfigPath(relativePath string) bool {
	normalized := strings.ToLower(filepath.ToSlash(relativePath))
	return normalized == ".ainovel/config.json" || normalized == "config.json" || normalized == "config.jsonc"
}

func pathsOverlap(left, right string) bool {
	return isSameOrChild(left, right) || isSameOrChild(right, left)
}

func sameFilesystemPath(left, right string) bool {
	left = filepath.Clean(left)
	right = filepath.Clean(right)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(left, right)
	}
	return left == right
}
