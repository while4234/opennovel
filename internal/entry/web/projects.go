package web

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/voocel/ainovel-cli/assets"
	"github.com/voocel/ainovel-cli/internal/bootstrap"
	"github.com/voocel/ainovel-cli/internal/host"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
)

const manifestVersion = 1

type ProjectManifest struct {
	Version        int        `json:"version"`
	ID             string     `json:"id"`
	Name           string     `json:"name"`
	RootDir        string     `json:"root_dir"`
	OutputDir      string     `json:"output_dir"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
	LastAccessedAt time.Time  `json:"last_accessed_at"`
	DeletedAt      *time.Time `json:"deleted_at,omitempty"`
	ClonedFromID   string     `json:"cloned_from_id,omitempty"`
	ClonedAt       *time.Time `json:"cloned_at,omitempty"`
}

type ProjectStore struct {
	RuntimeRoot string
	styleSource assets.StyleSource
	openMu      sync.Mutex
	configMu    sync.Mutex
	startupErr  error
}

var ErrProjectStartupRecovery = fmt.Errorf("project startup recovery required")

func (s *ProjectStore) requireStartupRecovery() error {
	if s == nil {
		return fmt.Errorf("%w: project store is unavailable", ErrProjectStartupRecovery)
	}
	if s.startupErr != nil {
		return fmt.Errorf("%w: legacy migration recovery failed", ErrProjectStartupRecovery)
	}
	return nil
}

func (s *ProjectStore) ProjectScheduledResumeEnabled(manifest ProjectManifest) (bool, error) {
	if err := s.requireStartupRecovery(); err != nil {
		return false, err
	}
	s.configMu.Lock()
	defer s.configMu.Unlock()
	cfg, found, err := s.loadProjectConfig(manifest)
	if err != nil {
		return false, err
	}
	if !found {
		return true, nil
	}
	return cfg.EffectiveScheduledResumeEnabled(), nil
}

func (s *ProjectStore) SaveProjectScheduledResumeEnabled(manifest ProjectManifest, enabled bool) error {
	if err := s.requireStartupRecovery(); err != nil {
		return err
	}
	s.configMu.Lock()
	defer s.configMu.Unlock()
	cfg, found, err := s.loadProjectConfig(manifest)
	if err != nil {
		return err
	}
	if !found {
		cfg = bootstrap.Config{}
	}
	cfg.ScheduledResumeEnabled = &enabled
	return bootstrap.SaveConfig(ProjectConfigPath(manifest), cfg)
}

func NewProjectStore(runtimeRoot string) *ProjectStore {
	return newProjectStore(runtimeRoot, assets.EmbeddedStyleSource())
}

func newProjectStore(runtimeRoot string, styleSource assets.StyleSource) *ProjectStore {
	store := &ProjectStore{RuntimeRoot: filepath.Clean(runtimeRoot), styleSource: styleSource}
	legacyMigrationMu.Lock()
	store.startupErr = store.recoverLegacyMigrationJournals()
	legacyMigrationMu.Unlock()
	return store
}

func (s *ProjectStore) ProjectsDir() string {
	return filepath.Join(s.RuntimeRoot, "projects")
}

func (s *ProjectStore) ProjectTrashDir() string {
	return filepath.Join(s.RuntimeRoot, "trash", "projects")
}

func (s *ProjectStore) CreateProject(name string) (ProjectManifest, error) {
	if err := s.requireStartupRecovery(); err != nil {
		return ProjectManifest{}, err
	}
	return s.createProject(name)
}

func (s *ProjectStore) CreateProjectWithStyle(name, style string) (ProjectManifest, error) {
	if err := s.requireStartupRecovery(); err != nil {
		return ProjectManifest{}, err
	}
	style = assets.NormalizeStyleID(style)
	if !s.styleSource.HasStyle(style) {
		return ProjectManifest{}, fmt.Errorf("unknown style %q", style)
	}
	manifest, err := s.createProject(name)
	if err != nil {
		return ProjectManifest{}, err
	}
	if err := s.SaveProjectStyle(manifest, style); err != nil {
		return ProjectManifest{}, err
	}
	return manifest, nil
}

func (s *ProjectStore) createProject(name string) (ProjectManifest, error) {
	if err := EnsureRuntimeRoot(s.RuntimeRoot); err != nil {
		return ProjectManifest{}, err
	}
	now := time.Now().UTC()
	name = strings.TrimSpace(name)
	if name == "" {
		name = "Untitled Novel"
	}
	id, err := newProjectID(name, now)
	if err != nil {
		return ProjectManifest{}, err
	}
	root := filepath.Join(s.ProjectsDir(), id)
	manifest := ProjectManifest{
		Version:        manifestVersion,
		ID:             id,
		Name:           name,
		RootDir:        root,
		OutputDir:      filepath.Join(root, "output"),
		CreatedAt:      now,
		UpdatedAt:      now,
		LastAccessedAt: now,
	}
	if err := s.ensureProjectDirs(manifest); err != nil {
		return ProjectManifest{}, err
	}
	if err := writeProjectManifest(manifest); err != nil {
		return ProjectManifest{}, err
	}
	return manifest, nil
}

func (s *ProjectStore) CloneProject(sourceID, name string) (ProjectManifest, error) {
	if err := s.requireStartupRecovery(); err != nil {
		return ProjectManifest{}, err
	}
	sourceID = strings.TrimSpace(sourceID)
	if err := validateProjectID(sourceID); err != nil {
		return ProjectManifest{}, err
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return ProjectManifest{}, fmt.Errorf("project name is required")
	}
	if err := EnsureRuntimeRoot(s.RuntimeRoot); err != nil {
		return ProjectManifest{}, err
	}

	source, err := s.projectManifestWithoutTouch(sourceID)
	if err != nil {
		return ProjectManifest{}, err
	}
	now := time.Now().UTC()
	id, err := newProjectID(name, now)
	if err != nil {
		return ProjectManifest{}, err
	}
	finalRoot := filepath.Join(s.ProjectsDir(), id)
	stagingRoot, err := os.MkdirTemp(s.ProjectsDir(), ".clone-"+id+"-")
	if err != nil {
		return ProjectManifest{}, fmt.Errorf("create clone staging directory: %w", err)
	}
	installed := false
	defer func() {
		if installed {
			return
		}
		_ = os.RemoveAll(stagingRoot)
		_ = os.RemoveAll(finalRoot)
	}()

	if err := storepkg.WithCloneReadyStoryFoundationSnapshot(source.OutputDir, func() error {
		return cloneProjectTree(source.RootDir, stagingRoot)
	}); err != nil {
		return ProjectManifest{}, err
	}
	if err := rebaseClonedProjectJSON(stagingRoot, source.RootDir, finalRoot); err != nil {
		return ProjectManifest{}, err
	}
	clonedRevisions := storepkg.NewRevisionStore(filepath.Join(stagingRoot, "output"))
	if err := clonedRevisions.DetachClonedNormalFlowLease(); err != nil {
		return ProjectManifest{}, fmt.Errorf("detach cloned normal-flow lease: %w", err)
	}
	if err := os.Rename(stagingRoot, finalRoot); err != nil {
		return ProjectManifest{}, fmt.Errorf("install cloned project: %w", err)
	}
	stagingRoot = ""

	clonedAt := now
	manifest := ProjectManifest{
		Version:        manifestVersion,
		ID:             id,
		Name:           name,
		RootDir:        finalRoot,
		OutputDir:      filepath.Join(finalRoot, "output"),
		CreatedAt:      now,
		UpdatedAt:      now,
		LastAccessedAt: now,
		ClonedFromID:   source.ID,
		ClonedAt:       &clonedAt,
	}
	if err := s.ensureProjectDirs(manifest); err != nil {
		return ProjectManifest{}, err
	}
	if err := writeProjectManifest(manifest); err != nil {
		return ProjectManifest{}, err
	}
	installed = true
	return manifest, nil
}

func (s *ProjectStore) projectManifestWithoutTouch(id string) (ProjectManifest, error) {
	if err := s.requireStartupRecovery(); err != nil {
		return ProjectManifest{}, err
	}
	root := filepath.Join(s.ProjectsDir(), id)
	manifest, err := readProjectManifest(filepath.Join(root, "project.json"))
	if err != nil {
		return ProjectManifest{}, err
	}
	if manifest.DeletedAt != nil {
		return ProjectManifest{}, os.ErrNotExist
	}
	return s.normalizeManifest(root, manifest), nil
}

func cloneProjectTree(sourceRoot, targetRoot string) error {
	if err := validateCloneSourceTree(sourceRoot); err != nil {
		return err
	}
	return filepath.WalkDir(sourceRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(sourceRoot, path)
		if err != nil {
			return err
		}
		if relative == "." {
			return nil
		}
		if entry.IsDir() && !cloneProgramOwnedDirectory(relative) {
			return filepath.SkipDir
		}
		if !entry.IsDir() && (cloneExcludedPath(relative) || cloneTemporaryName(entry.Name())) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("clone project source contains an unsupported symbolic link")
		}
		target := filepath.Join(targetRoot, relative)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if err := copyFileOverwrite(path, target); err != nil {
			return err
		}
		if info, err := entry.Info(); err == nil {
			_ = os.Chmod(target, info.Mode().Perm())
		}
		return nil
	})
}

func cloneProgramOwnedDirectory(relative string) bool {
	path := strings.Trim(strings.ToLower(filepath.ToSlash(filepath.Clean(relative))), "/")
	if path == "output" || path == "uploads" || path == "uploads/adaptation" || path == "exports" {
		return true
	}
	if strings.HasPrefix(path, "uploads/adaptation/") || strings.HasPrefix(path, "exports/") {
		return true
	}
	if !strings.HasPrefix(path, "output/") {
		return false
	}
	rel := strings.TrimPrefix(path, "output/")
	if rel == "meta/runtime" || strings.HasPrefix(rel, "meta/runtime/") {
		return false
	}
	if rel == "meta/foundation/stage" || strings.HasPrefix(rel, "meta/foundation/stage/") {
		return false
	}
	root := strings.Split(rel, "/")[0]
	switch root {
	case "chapters", "summaries", "structure", "meta", "novel", "logs", "checkpoints", "sessions", "exports":
		return true
	default:
		return false
	}
}

func cloneExcludedPath(relative string) bool {
	path := strings.ToLower(filepath.ToSlash(filepath.Clean(relative)))
	return !cloneProgramOwnedPath(path)
}

// cloneProgramOwnedPath is an allowlist of persisted application schemas. It
// intentionally rejects arbitrary user files even when they are placed under
// a familiar top-level directory. New durable schemas must be added here
// explicitly before validation clones may copy them.
func cloneProgramOwnedPath(path string) bool {
	path = strings.Trim(strings.ToLower(filepath.ToSlash(filepath.Clean(path))), "/")
	if path == "" || path == "." || path == "project.json" || path == filepath.ToSlash(actionRegistryRelPath) {
		return false
	}
	for _, component := range strings.Split(path, "/") {
		if cloneSensitivePathComponent(component) || cloneTemporaryName(component) {
			return false
		}
	}
	if strings.HasPrefix(path, "uploads/adaptation/") {
		return cloneAllowedExtension(path, ".txt", ".md", ".json", ".epub")
	}
	if strings.HasPrefix(path, "exports/") {
		return cloneAllowedExtension(path, ".txt", ".epub", ".json")
	}
	if !strings.HasPrefix(path, "output/") {
		return false
	}
	rel := strings.TrimPrefix(path, "output/")
	if rel == "meta/runtime" || strings.HasPrefix(rel, "meta/runtime/") {
		return false
	}
	if strings.HasPrefix(rel, "meta/foundation/") {
		return rel == "meta/foundation/projections.json"
	}
	root := strings.Split(rel, "/")[0]
	switch root {
	case "chapters", "summaries", "structure", "meta", "novel", "logs", "checkpoints", "sessions", "exports":
		return cloneAllowedExtension(rel, ".json", ".jsonl", ".md", ".txt", ".epub", ".log")
	case "story_foundation.json", "premise.md", "characters.json", "characters.md", "world_rules.json", "world_rules.md", "planned_relationships.json", "planned_relationships.md", "outline.json", "layered_outline.json", "progress.json", "world.json", "cast.json", "signals.json", "user_rules.json":
		return !strings.Contains(rel, "/")
	default:
		return false
	}
}

func cloneAllowedExtension(path string, extensions ...string) bool {
	extension := strings.ToLower(filepath.Ext(path))
	for _, allowed := range extensions {
		if extension == allowed {
			return true
		}
	}
	return false
}

func cloneSensitivePathComponent(component string) bool {
	component = strings.ToLower(strings.TrimSpace(component))
	if component == ".ainovel" || component == ".ssh" || component == ".aws" ||
		component == "auth" || component == "credentials" || component == "secrets" {
		return true
	}
	if component == ".env" || strings.HasPrefix(component, ".env.") ||
		component == "auth.json" || strings.HasPrefix(component, "credentials.") ||
		component == "credentials.json" || strings.HasPrefix(component, "secrets.") ||
		component == "secrets.json" || strings.HasPrefix(component, "id_rsa") ||
		strings.HasPrefix(component, "id_ed25519") || component == "token" ||
		strings.HasPrefix(component, "token.") || strings.Contains(component, "oauth") ||
		component == ".netrc" || component == ".npmrc" || component == ".git-credentials" ||
		strings.Contains(component, "keystore") || strings.HasPrefix(component, "secret.") {
		return true
	}
	switch filepath.Ext(component) {
	case ".pem", ".key", ".p12", ".pfx":
		return true
	default:
		return false
	}
}

func validateCloneSourceTree(sourceRoot string) error {
	identities := make(map[string]string)
	err := filepath.WalkDir(sourceRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(sourceRoot, path)
		if err != nil || relative == "." {
			return err
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("clone project source contains an unsupported link or junction")
		}
		if entry.IsDir() {
			return nil
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("clone project source contains an unsupported special file")
		}
		identity, links, identityErr := cloneFileIdentity(path, info)
		if identityErr != nil {
			return fmt.Errorf("clone project source file identity is unavailable")
		}
		if links > 1 {
			return fmt.Errorf("clone project source contains an unsupported hardlink identity")
		}
		if previous, exists := identities[identity]; exists {
			_ = previous
			return fmt.Errorf("clone project source contains a duplicate file identity")
		}
		identities[identity] = relative
		return nil
	})
	return err
}

func cloneTemporaryName(name string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	return strings.HasPrefix(name, ".clone-") ||
		strings.Contains(name, ".tmp-") ||
		strings.HasSuffix(name, ".tmp")
}

func rebaseClonedProjectJSON(root, oldRoot, newRoot string) error {
	return filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if !shouldRebaseClonedJSON(relative) {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read cloned JSON %s: %w", path, err)
		}
		var value any
		if err := json.Unmarshal(data, &value); err != nil {
			return fmt.Errorf("parse cloned JSON %s: %w", path, err)
		}
		rebased, changed := rebaseProjectValue(value, oldRoot, newRoot)
		if !changed {
			return nil
		}
		encoded, err := json.MarshalIndent(rebased, "", "  ")
		if err != nil {
			return fmt.Errorf("encode cloned JSON %s: %w", path, err)
		}
		encoded = append(encoded, '\n')
		if err := os.WriteFile(path, encoded, 0o644); err != nil {
			return fmt.Errorf("write cloned JSON %s: %w", path, err)
		}
		return nil
	})
}

func shouldRebaseClonedJSON(relative string) bool {
	path := strings.ToLower(filepath.ToSlash(relative))
	if path == ".ainovel/config.json" ||
		path == "output/meta/simulation_profile.json" ||
		path == "output/meta/sessions/web-cocreate-checkpoint.json" {
		return true
	}
	if strings.HasPrefix(path, "output/meta/adaptation_backups/") && strings.HasSuffix(path, ".json") {
		return true
	}
	if !strings.HasPrefix(path, "output/meta/adaptation/") {
		return false
	}
	relativeAdaptationPath := strings.TrimPrefix(path, "output/meta/adaptation/")
	if strings.HasPrefix(relativeAdaptationPath, "source_foundation_batches/") ||
		strings.HasPrefix(relativeAdaptationPath, "cocreate_dossier_batches/") {
		return strings.HasSuffix(relativeAdaptationPath, ".json")
	}
	switch relativeAdaptationPath {
	case "source_manifest.json",
		"source_foundation.json",
		"cocreate_dossier.json",
		"cocreate_briefing.json",
		"proposal_volume_review.json",
		"proposal_runtime.json",
		"audits/latest_application.json":
		return true
	default:
		return false
	}
}

func rebaseProjectValue(value any, oldRoot, newRoot string) (any, bool) {
	switch typed := value.(type) {
	case string:
		rebased, ok := rebaseProjectPath(typed, oldRoot, newRoot)
		return rebased, ok
	case []any:
		changed := false
		for index, item := range typed {
			rebased, itemChanged := rebaseProjectValue(item, oldRoot, newRoot)
			typed[index] = rebased
			changed = changed || itemChanged
		}
		return typed, changed
	case map[string]any:
		changed := false
		for key, item := range typed {
			rebased, itemChanged := rebaseProjectValue(item, oldRoot, newRoot)
			typed[key] = rebased
			changed = changed || itemChanged
		}
		return typed, changed
	default:
		return value, false
	}
}

func rebaseProjectPath(value, oldRoot, newRoot string) (string, bool) {
	if !filepath.IsAbs(value) {
		return value, false
	}
	relative, err := filepath.Rel(oldRoot, value)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return value, false
	}
	return filepath.Join(newRoot, relative), true
}

func (s *ProjectStore) ListProjects() ([]ProjectManifest, error) {
	if err := s.requireStartupRecovery(); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(s.ProjectsDir())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("list projects: %w", err)
	}
	projects := make([]ProjectManifest, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		root := filepath.Join(s.ProjectsDir(), entry.Name())
		manifest, err := readProjectManifest(filepath.Join(root, "project.json"))
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		if manifest.DeletedAt != nil {
			continue
		}
		manifest = s.normalizeManifest(root, manifest)
		projects = append(projects, manifest)
	}
	sort.Slice(projects, func(i, j int) bool {
		if !projects[i].LastAccessedAt.Equal(projects[j].LastAccessedAt) {
			return projects[i].LastAccessedAt.After(projects[j].LastAccessedAt)
		}
		if !projects[i].CreatedAt.Equal(projects[j].CreatedAt) {
			return projects[i].CreatedAt.After(projects[j].CreatedAt)
		}
		return projects[i].ID < projects[j].ID
	})
	return projects, nil
}

func (s *ProjectStore) OpenProject(id string) (ProjectManifest, error) {
	if err := s.requireStartupRecovery(); err != nil {
		return ProjectManifest{}, err
	}
	// Opening refreshes project.json. Serialize the read-modify-rename cycle so
	// concurrent project-scoped HTTP requests cannot race on Windows.
	s.openMu.Lock()
	defer s.openMu.Unlock()
	if err := validateProjectID(id); err != nil {
		return ProjectManifest{}, err
	}
	root := filepath.Join(s.ProjectsDir(), id)
	manifest, err := readProjectManifest(filepath.Join(root, "project.json"))
	if err != nil {
		return ProjectManifest{}, err
	}
	if manifest.DeletedAt != nil {
		return ProjectManifest{}, os.ErrNotExist
	}
	manifest = s.normalizeManifest(root, manifest)
	now := time.Now().UTC()
	manifest.LastAccessedAt = now
	manifest.UpdatedAt = now
	if err := s.ensureProjectDirs(manifest); err != nil {
		return ProjectManifest{}, err
	}
	if err := writeProjectManifest(manifest); err != nil {
		return ProjectManifest{}, err
	}
	return manifest, nil
}

func (s *ProjectStore) RenameProject(id, name string) (ProjectManifest, error) {
	if err := s.requireStartupRecovery(); err != nil {
		return ProjectManifest{}, err
	}
	if err := validateProjectID(strings.TrimSpace(id)); err != nil {
		return ProjectManifest{}, err
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return ProjectManifest{}, fmt.Errorf("project name is required")
	}
	root := filepath.Join(s.ProjectsDir(), strings.TrimSpace(id))
	manifest, err := readProjectManifest(filepath.Join(root, "project.json"))
	if err != nil {
		return ProjectManifest{}, err
	}
	if manifest.DeletedAt != nil {
		return ProjectManifest{}, os.ErrNotExist
	}
	manifest = s.normalizeManifest(root, manifest)
	manifest.Name = name
	manifest.UpdatedAt = time.Now().UTC()
	if err := writeProjectManifest(manifest); err != nil {
		return ProjectManifest{}, err
	}
	return manifest, nil
}

func (s *ProjectStore) TrashProject(id string) (ProjectManifest, string, error) {
	if err := s.requireStartupRecovery(); err != nil {
		return ProjectManifest{}, "", err
	}
	id = strings.TrimSpace(id)
	if err := validateProjectID(id); err != nil {
		return ProjectManifest{}, "", err
	}
	root := filepath.Join(s.ProjectsDir(), id)
	manifest, err := readProjectManifest(filepath.Join(root, "project.json"))
	if err != nil {
		return ProjectManifest{}, "", err
	}
	if manifest.DeletedAt != nil {
		return ProjectManifest{}, "", os.ErrNotExist
	}
	manifest = s.normalizeManifest(root, manifest)
	deletedAt := time.Now().UTC()
	manifest.DeletedAt = &deletedAt
	manifest.UpdatedAt = deletedAt
	if err := writeProjectManifest(manifest); err != nil {
		return ProjectManifest{}, "", err
	}

	trashDir := s.ProjectTrashDir()
	if err := os.MkdirAll(trashDir, 0o755); err != nil {
		return ProjectManifest{}, "", fmt.Errorf("create trash dir: %w", err)
	}
	target := s.uniqueTrashProjectPath(trashDir, id, deletedAt)
	if err := os.Rename(root, target); err != nil {
		manifest.DeletedAt = nil
		_ = writeProjectManifest(manifest)
		return ProjectManifest{}, "", fmt.Errorf("move project to trash: %w", err)
	}
	manifest.RootDir = target
	manifest.OutputDir = filepath.Join(target, "output")
	if err := writeProjectManifest(manifest); err != nil {
		return ProjectManifest{}, "", err
	}
	return manifest, target, nil
}

func (s *ProjectStore) ListTrashedProjects() ([]ProjectManifest, error) {
	if err := s.requireStartupRecovery(); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(s.ProjectTrashDir())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("list project trash: %w", err)
	}
	projects := make([]ProjectManifest, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		root := filepath.Join(s.ProjectTrashDir(), entry.Name())
		manifest, err := readProjectManifest(filepath.Join(root, "project.json"))
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		manifest = s.normalizeManifest(root, manifest)
		if manifest.DeletedAt == nil {
			deletedAt := manifest.UpdatedAt
			manifest.DeletedAt = &deletedAt
		}
		projects = append(projects, manifest)
	}
	sort.Slice(projects, func(i, j int) bool {
		left := projects[i].UpdatedAt
		right := projects[j].UpdatedAt
		if projects[i].DeletedAt != nil {
			left = *projects[i].DeletedAt
		}
		if projects[j].DeletedAt != nil {
			right = *projects[j].DeletedAt
		}
		if !left.Equal(right) {
			return left.After(right)
		}
		return projects[i].ID < projects[j].ID
	})
	return projects, nil
}

func (s *ProjectStore) ClearProjectTrash() (int, error) {
	if err := s.requireStartupRecovery(); err != nil {
		return 0, err
	}
	trashDir := s.ProjectTrashDir()
	entries, err := os.ReadDir(trashDir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("read project trash: %w", err)
	}
	count := 0
	for _, entry := range entries {
		if entry.IsDir() {
			count++
		}
	}
	if err := removeAllWithRetry(trashDir); err != nil {
		return 0, fmt.Errorf("clear project trash: %w", err)
	}
	return count, nil
}

func (s *ProjectStore) ListTrashProjects() ([]ProjectManifest, error) {
	if err := s.requireStartupRecovery(); err != nil {
		return nil, err
	}
	return s.ListTrashedProjects()
}

func (s *ProjectStore) RestoreTrashProject(id string) (ProjectManifest, error) {
	if err := s.requireStartupRecovery(); err != nil {
		return ProjectManifest{}, err
	}
	if err := validateProjectID(strings.TrimSpace(id)); err != nil {
		return ProjectManifest{}, err
	}
	manifest, root, err := s.findTrashedProject(id)
	if err != nil {
		return ProjectManifest{}, err
	}
	target := filepath.Join(s.ProjectsDir(), id)
	if _, err := os.Stat(target); err == nil {
		return ProjectManifest{}, fmt.Errorf("project %s already exists", id)
	} else if !os.IsNotExist(err) {
		return ProjectManifest{}, err
	}
	if err := os.MkdirAll(s.ProjectsDir(), 0o755); err != nil {
		return ProjectManifest{}, fmt.Errorf("create projects dir: %w", err)
	}
	now := time.Now().UTC()
	manifest.RootDir = target
	manifest.OutputDir = filepath.Join(target, "output")
	manifest.DeletedAt = nil
	manifest.UpdatedAt = now
	manifest.LastAccessedAt = now
	if err := os.Rename(root, target); err != nil {
		return ProjectManifest{}, fmt.Errorf("restore project: %w", err)
	}
	if err := s.ensureProjectDirs(manifest); err != nil {
		return ProjectManifest{}, err
	}
	if err := writeProjectManifest(manifest); err != nil {
		return ProjectManifest{}, err
	}
	return manifest, nil
}

func (s *ProjectStore) EmptyTrashProjects() (int, error) {
	if err := s.requireStartupRecovery(); err != nil {
		return 0, err
	}
	return s.ClearProjectTrash()
}

func (s *ProjectStore) findTrashedProject(id string) (ProjectManifest, string, error) {
	entries, err := os.ReadDir(s.ProjectTrashDir())
	if err != nil {
		if os.IsNotExist(err) {
			return ProjectManifest{}, "", os.ErrNotExist
		}
		return ProjectManifest{}, "", fmt.Errorf("list trash projects: %w", err)
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		root := filepath.Join(s.ProjectTrashDir(), entry.Name())
		manifest, err := readProjectManifest(filepath.Join(root, "project.json"))
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return ProjectManifest{}, "", err
		}
		if manifest.ID != id {
			continue
		}
		manifest = s.normalizeManifest(root, manifest)
		if manifest.DeletedAt == nil {
			deletedAt := manifest.UpdatedAt
			manifest.DeletedAt = &deletedAt
		}
		manifest.RootDir = root
		manifest.OutputDir = filepath.Join(root, "output")
		return manifest, root, nil
	}
	return ProjectManifest{}, "", os.ErrNotExist
}

func (s *ProjectStore) uniqueTrashProjectPath(trashDir, id string, deletedAt time.Time) string {
	base := filepath.Join(trashDir, fmt.Sprintf("%s-%s", id, deletedAt.Format("20060102150405")))
	path := base
	for i := 2; ; i++ {
		if _, err := os.Stat(path); os.IsNotExist(err) {
			return path
		}
		path = fmt.Sprintf("%s-%d", base, i)
	}
}

func removeAllWithRetry(path string) error {
	var err error
	for attempt := 0; attempt < 5; attempt++ {
		err = os.RemoveAll(path)
		if err == nil || os.IsNotExist(err) {
			return nil
		}
		time.Sleep(time.Duration(attempt+1) * 25 * time.Millisecond)
	}
	return err
}

func (s *ProjectStore) OpenProjectHost(cfg bootstrap.Config, _ assets.Bundle, manifest ProjectManifest) (*host.Host, error) {
	if err := s.requireStartupRecovery(); err != nil {
		return nil, err
	}
	manifest = s.normalizeManifest(manifest.RootDir, manifest)
	if err := s.ensureProjectDirs(manifest); err != nil {
		return nil, err
	}
	projectConfigPath := ProjectConfigPath(manifest)
	projectCfg, found, err := s.loadProjectConfig(manifest)
	if err != nil {
		return nil, err
	}
	ownedProviders := projectOwnedProviders(projectCfg, cfg)
	cfg = projectBaseModelConfig(cfg)
	if found {
		cfg = bootstrap.MergeConfig(cfg, projectCfg)
	}
	cfg.Style = assets.NormalizeStyleID(cfg.Style)
	bundle := s.styleSource.Load(cfg.Style)
	cfg.OutputDir = manifest.OutputDir
	cfg.PersistPath = projectConfigPath
	cfg.PersistProjectOverlay = true
	cfg.PersistProviders = ownedProviders
	cfg.PersistProjectConfig = &projectCfg
	return host.New(cfg, bundle)
}

func projectBaseModelConfig(cfg bootstrap.Config) bootstrap.Config {
	cfg = cloneWebConfig(cfg)
	if len(cfg.Roles) == 0 {
		return cfg
	}
	nextRoles := make(map[string]bootstrap.RoleConfig, len(cfg.Roles))
	for role, rc := range cfg.Roles {
		rc.Provider = ""
		rc.Model = ""
		rc.Fallbacks = nil
		if rc.ReasoningEffort != "" {
			nextRoles[role] = rc
		}
	}
	if len(nextRoles) == 0 {
		cfg.Roles = nil
	} else {
		cfg.Roles = nextRoles
	}
	return cfg
}

func ProjectConfigPath(manifest ProjectManifest) string {
	return filepath.Join(manifest.RootDir, ".ainovel", "config.json")
}

func (s *ProjectStore) loadProjectConfig(manifest ProjectManifest) (bootstrap.Config, bool, error) {
	path := ProjectConfigPath(manifest)
	cfg, err := bootstrap.LoadConfigFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return bootstrap.Config{}, false, nil
		}
		return bootstrap.Config{}, false, fmt.Errorf("load project config %s: %w", path, err)
	}
	// Global prompt overrides belong exclusively to the global config. Ignore
	// legacy or manually added project values and avoid persisting them again.
	cfg.GlobalPrompts = nil
	return cfg, true, nil
}

func (s *ProjectStore) RefreshProjectProviderReferences(globalCfg bootstrap.Config, originalProvider, provider string) (int, error) {
	originalProvider = strings.TrimSpace(originalProvider)
	provider = strings.TrimSpace(provider)
	if provider == "" {
		return 0, fmt.Errorf("provider is required")
	}
	if originalProvider == "" {
		originalProvider = provider
	}
	projects, err := s.ListProjects()
	if err != nil {
		return 0, err
	}
	updated := 0
	for _, manifest := range projects {
		cfg, found, err := s.loadProjectConfig(manifest)
		if err != nil {
			return updated, err
		}
		if !found {
			continue
		}
		next, changed := refreshProjectProviderConfig(cfg, globalCfg, originalProvider, provider)
		if !changed {
			continue
		}
		if err := bootstrap.SaveConfig(ProjectConfigPath(manifest), next); err != nil {
			return updated, err
		}
		updated++
	}
	return updated, nil
}

// RemoveInheritedProjectProviderModel removes a globally deleted model from
// project overlays that inherited the global provider. Explicitly project-owned
// providers are left untouched. Legacy overlays without ownership metadata are
// treated as inherited only when their private provider configuration matches
// the previous global provider configuration.
func (s *ProjectStore) RemoveInheritedProjectProviderModel(previousGlobal, nextGlobal bootstrap.Config, provider, model string) (int, error) {
	provider = strings.TrimSpace(provider)
	model = strings.TrimSpace(model)
	if provider == "" || model == "" {
		return 0, fmt.Errorf("provider and model are required")
	}
	projects, err := s.ListProjects()
	if err != nil {
		return 0, err
	}
	trashed, err := s.ListTrashedProjects()
	if err != nil {
		return 0, err
	}
	projects = append(projects, trashed...)

	updated := 0
	for _, manifest := range projects {
		cfg, found, err := s.loadProjectConfig(manifest)
		if err != nil {
			return updated, err
		}
		if !found {
			continue
		}
		next, changed := removeInheritedProjectProviderModel(cfg, previousGlobal, nextGlobal, provider, model)
		if !changed {
			continue
		}
		if err := bootstrap.SaveConfig(ProjectConfigPath(manifest), next); err != nil {
			return updated, err
		}
		updated++
	}
	return updated, nil
}

func removeInheritedProjectProviderModel(cfg, previousGlobal, nextGlobal bootstrap.Config, provider, model string) (bootstrap.Config, bool) {
	localPC, hasLocalProvider := cfg.Providers[provider]
	if cfg.ProjectOwnedProviders[provider] {
		return cfg, false
	}
	if len(cfg.ProjectOwnedProviders) == 0 && hasLocalProvider && providerHasPrivateConfig(localPC) {
		previousPC, ok := previousGlobal.Providers[provider]
		if !ok || !providerPrivateConfigEqual(localPC, previousPC) {
			return cfg, false
		}
	}

	next := cloneWebConfig(cfg)
	if next.Provider == provider && next.ModelName == model {
		next.Provider = ""
		next.ModelName = ""
	}
	for role, route := range next.Roles {
		if route.Provider == provider && route.Model == model {
			route.Provider = ""
			route.Model = ""
		}
		route.Fallbacks = removeProjectModelRef(route.Fallbacks, provider, model)
		if route.Provider == "" && route.Model == "" && route.ReasoningEffort == "" && len(route.Fallbacks) == 0 {
			delete(next.Roles, role)
			continue
		}
		next.Roles[role] = route
	}

	globalPC, providerStillConfigured := nextGlobal.Providers[provider]
	if hasLocalProvider {
		if providerStillConfigured {
			next.Providers[provider] = bootstrap.ProviderConfig{
				Label:                 globalPC.Label,
				Models:                append([]string(nil), globalPC.Models...),
				ModelReasoningEfforts: cloneWebStringMap(globalPC.ModelReasoningEfforts),
			}
		} else {
			delete(next.Providers, provider)
		}
	}
	if !providerStillConfigured {
		next.ModelAutoSwitch.FallbackBackends = removeProjectProvider(next.ModelAutoSwitch.FallbackBackends, provider)
	}
	delete(next.ProjectOwnedProviders, provider)
	if len(next.ProjectOwnedProviders) == 0 {
		next.ProjectOwnedProviders = nil
	}
	syncProjectGlobalModelSettings(&next, nextGlobal)
	return next, !reflect.DeepEqual(cfg, next)
}

func removeProjectModelRef(values []bootstrap.ModelRef, provider, model string) []bootstrap.ModelRef {
	out := make([]bootstrap.ModelRef, 0, len(values))
	for _, value := range values {
		if value.Provider == provider && value.Model == model {
			continue
		}
		out = append(out, value)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func removeProjectProvider(values []string, provider string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if strings.TrimSpace(value) == provider {
			continue
		}
		out = append(out, value)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func (s *ProjectStore) RefreshProjectModelSettings(globalCfg bootstrap.Config) (int, error) {
	projects, err := s.ListProjects()
	if err != nil {
		return 0, err
	}
	updated := 0
	for _, manifest := range projects {
		cfg, found, err := s.loadProjectConfig(manifest)
		if err != nil {
			return updated, err
		}
		if !found {
			continue
		}
		next := cloneWebConfig(cfg)
		if !syncProjectGlobalModelSettings(&next, globalCfg) {
			continue
		}
		if err := bootstrap.SaveConfig(ProjectConfigPath(manifest), next); err != nil {
			return updated, err
		}
		updated++
	}
	return updated, nil
}

func refreshProjectProviderConfig(cfg bootstrap.Config, globalCfg bootstrap.Config, originalProvider, provider string) (bootstrap.Config, bool) {
	originalProvider = strings.TrimSpace(originalProvider)
	provider = strings.TrimSpace(provider)
	if provider == "" {
		return cfg, false
	}
	if originalProvider == "" {
		originalProvider = provider
	}
	next := cloneWebConfig(cfg)
	changed := false
	if syncProjectGlobalModelSettings(&next, globalCfg) {
		changed = true
	}
	if originalProvider != provider {
		var renamed bool
		next, renamed = host.RenameProviderInConfig(next, originalProvider, provider)
		changed = changed || renamed
	}
	if refreshProjectProviderDisplayMetadata(&next, globalCfg, provider) {
		changed = true
	}
	return next, changed
}

func syncProjectGlobalModelSettings(cfg *bootstrap.Config, globalCfg bootstrap.Config) bool {
	changed := false
	globalAutoSwitch := cloneWebModelAutoSwitchConfig(globalCfg.ModelAutoSwitch)
	if !reflect.DeepEqual(cfg.ModelAutoSwitch, globalAutoSwitch) {
		cfg.ModelAutoSwitch = globalAutoSwitch
		changed = true
	}
	if cfg.StructureRepairMaxAttempts != globalCfg.StructureRepairMaxAttempts {
		cfg.StructureRepairMaxAttempts = globalCfg.StructureRepairMaxAttempts
		changed = true
	}
	if cfg.BudgetQualityMaxAttempts != globalCfg.BudgetQualityMaxAttempts {
		cfg.BudgetQualityMaxAttempts = globalCfg.BudgetQualityMaxAttempts
		changed = true
	}
	if cfg.AdaptationOutlineAuditRetryMaxAttempts != globalCfg.AdaptationOutlineAuditRetryMaxAttempts {
		cfg.AdaptationOutlineAuditRetryMaxAttempts = globalCfg.AdaptationOutlineAuditRetryMaxAttempts
		changed = true
	}
	return changed
}

func refreshProjectProviderDisplayMetadata(cfg *bootstrap.Config, globalCfg bootstrap.Config, provider string) bool {
	provider = strings.TrimSpace(provider)
	if provider == "" || cfg.Providers == nil {
		return false
	}
	pc, ok := cfg.Providers[provider]
	if !ok {
		return false
	}
	globalPC, hasGlobal := globalCfg.Providers[provider]
	globalLabel := ""
	if hasGlobal {
		globalLabel = strings.TrimSpace(globalPC.Label)
	}
	if providerHasPrivateConfig(pc) {
		if pc.Label == globalLabel {
			return false
		}
		pc.Label = globalLabel
		cfg.Providers[provider] = pc
		return true
	}
	models := append([]string(nil), pc.Models...)
	if hasGlobal {
		models = mergeProviderModelMetadata(globalPC.Models, models)
	}
	safe := bootstrap.ProviderConfig{
		Label:                 globalLabel,
		Models:                models,
		ModelReasoningEfforts: cloneWebStringMap(globalPC.ModelReasoningEfforts),
	}
	if reflect.DeepEqual(pc, safe) {
		return false
	}
	cfg.Providers[provider] = safe
	return true
}

func mergeProviderModelMetadata(primary, fallback []string) []string {
	seen := make(map[string]bool, len(primary)+len(fallback))
	out := make([]string, 0, len(primary)+len(fallback))
	add := func(model string) {
		model = strings.TrimSpace(model)
		if model == "" || seen[model] {
			return
		}
		seen[model] = true
		out = append(out, model)
	}
	for _, model := range primary {
		add(model)
	}
	for _, model := range fallback {
		add(model)
	}
	return out
}

func (s *ProjectStore) SaveProjectStyle(manifest ProjectManifest, style string) error {
	if err := s.requireStartupRecovery(); err != nil {
		return err
	}
	s.configMu.Lock()
	defer s.configMu.Unlock()
	return s.saveProjectStyle(manifest, style)
}

func (s *ProjectStore) saveProjectStyle(manifest ProjectManifest, style string) error {
	style = assets.NormalizeStyleID(style)
	if !s.styleSource.HasStyle(style) {
		return fmt.Errorf("unknown style %q", style)
	}
	cfg, found, err := s.loadProjectConfig(manifest)
	if err != nil {
		return err
	}
	if !found {
		cfg = bootstrap.Config{}
	}
	cfg.Style = style
	return bootstrap.SaveConfig(ProjectConfigPath(manifest), cfg)
}

// InitializeProjectStageRoutes adds stage defaults only when a newly created
// project has not already selected that stage. Existing project preferences are
// therefore never migrated or overwritten by changes to the product defaults.
func (s *ProjectStore) InitializeProjectStageRoutes(manifest ProjectManifest, routes map[string]bootstrap.RoleConfig) error {
	if err := s.requireStartupRecovery(); err != nil {
		return err
	}
	if len(routes) == 0 {
		return nil
	}
	s.configMu.Lock()
	defer s.configMu.Unlock()

	cfg, found, err := s.loadProjectConfig(manifest)
	if err != nil {
		return err
	}
	if !found {
		cfg = bootstrap.Config{}
	}
	if cfg.Roles == nil {
		cfg.Roles = make(map[string]bootstrap.RoleConfig, len(routes))
	}
	for stage, route := range routes {
		key := bootstrap.StageRouteKey(stage)
		if _, selected := cfg.Roles[key]; selected {
			continue
		}
		cfg.Roles[key] = route
	}
	return bootstrap.SaveConfig(ProjectConfigPath(manifest), cfg)
}

func (s *ProjectStore) SaveProjectSimulationMode(manifest ProjectManifest, mode string) error {
	if err := s.requireStartupRecovery(); err != nil {
		return err
	}
	normalized, err := bootstrap.NormalizeSimulationMode(mode)
	if err != nil {
		return err
	}
	cfg, found, err := s.loadProjectConfig(manifest)
	if err != nil {
		return err
	}
	if !found {
		cfg = bootstrap.Config{}
	}
	cfg.SimulationMode = normalized
	return bootstrap.SaveConfig(ProjectConfigPath(manifest), cfg)
}

func projectOwnedProviders(cfg, globalCfg bootstrap.Config) map[string]bool {
	if len(cfg.Providers) == 0 {
		return nil
	}
	out := make(map[string]bool, len(cfg.Providers))
	for name, pc := range cfg.Providers {
		if !providerHasPrivateConfig(pc) {
			continue
		}
		if len(cfg.ProjectOwnedProviders) > 0 {
			if cfg.ProjectOwnedProviders[name] {
				out[name] = true
			}
			continue
		}
		if globalPC, ok := globalCfg.Providers[name]; ok && providerPrivateConfigEqual(pc, globalPC) {
			continue
		}
		out[name] = true
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func providerPrivateConfigEqual(left, right bootstrap.ProviderConfig) bool {
	left = cloneWebProviderConfig(left)
	right = cloneWebProviderConfig(right)
	left.Label = ""
	right.Label = ""
	left.Models = nil
	right.Models = nil
	left.ModelReasoningEfforts = nil
	right.ModelReasoningEfforts = nil
	return reflect.DeepEqual(left, right)
}

func providerHasPrivateConfig(pc bootstrap.ProviderConfig) bool {
	return pc.Type != "" ||
		pc.Auth != "" ||
		pc.AccountID != "" ||
		pc.AuthFile != "" ||
		pc.API != "" ||
		pc.APIKey != "" ||
		pc.BaseURL != "" ||
		len(pc.ExtraBody) > 0 ||
		len(pc.Extra) > 0
}

func (s *ProjectStore) normalizeManifest(root string, manifest ProjectManifest) ProjectManifest {
	if manifest.Version == 0 {
		manifest.Version = manifestVersion
	}
	if manifest.RootDir == "" {
		manifest.RootDir = root
	}
	if manifest.OutputDir == "" {
		manifest.OutputDir = filepath.Join(manifest.RootDir, "output")
	}
	if manifest.ID == "" {
		manifest.ID = filepath.Base(manifest.RootDir)
	}
	if manifest.Name == "" {
		manifest.Name = manifest.ID
	}
	return manifest
}

func (s *ProjectStore) ensureProjectDirs(manifest ProjectManifest) error {
	for _, dir := range []string{
		manifest.RootDir,
		filepath.Join(manifest.RootDir, "simulate"),
		filepath.Join(manifest.RootDir, "uploads"),
		filepath.Join(manifest.RootDir, "uploads", "adaptation"),
		filepath.Join(manifest.RootDir, "uploads", "import"),
		filepath.Join(manifest.RootDir, "profiles"),
		filepath.Join(manifest.RootDir, "profiles", "imported"),
		filepath.Join(manifest.RootDir, "exports"),
		manifest.OutputDir,
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create project dir %s: %w", dir, err)
		}
	}
	return nil
}

func writeProjectManifest(manifest ProjectManifest) error {
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(manifest.RootDir, "project.json")
	tmp, err := os.CreateTemp(filepath.Dir(path), "project.json.tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o644); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

func readProjectManifest(path string) (ProjectManifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return ProjectManifest{}, err
	}
	var manifest ProjectManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return ProjectManifest{}, fmt.Errorf("parse project manifest %s: %w", path, err)
	}
	if err := validateProjectID(manifest.ID); err != nil {
		return ProjectManifest{}, fmt.Errorf("project manifest %s has invalid id: %w", path, err)
	}
	return manifest, nil
}

func newProjectID(name string, now time.Time) (string, error) {
	slug := slugify(name)
	if slug == "" {
		slug = "project"
	}
	var suffix [3]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return "", err
	}
	return fmt.Sprintf("%s-%s-%s", slug, now.Format("20060102150405"), hex.EncodeToString(suffix[:])), nil
}

func slugify(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	lastDash := false
	for _, r := range s {
		ok := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		if ok {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash {
			b.WriteByte('-')
			lastDash = true
		}
	}
	return strings.Trim(b.String(), "-")
}

func validateProjectID(id string) error {
	if id == "" {
		return fmt.Errorf("project id is required")
	}
	for _, r := range id {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			continue
		}
		return fmt.Errorf("project id %q contains invalid character %q", id, r)
	}
	if strings.Contains(id, "..") {
		return fmt.Errorf("project id %q must not contain path traversal", id)
	}
	return nil
}
