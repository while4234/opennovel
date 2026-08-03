package web

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/voocel/ainovel-cli/internal/adaptaudit"
	"github.com/voocel/ainovel-cli/internal/domain"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
)

type ValidationCloneReport struct {
	SchemaVersion  int    `json:"clone_schema_version"`
	AnonymousID    string `json:"anonymous_id"`
	SourceSHA256   string `json:"source_sha256"`
	AfterSHA256    string `json:"after_sha256"`
	FileCount      int    `json:"file_count"`
	Bytes          int64  `json:"bytes"`
	ManifestDigest string `json:"manifest_digest,omitempty"`
	LineageDigest  string `json:"lineage_digest,omitempty"`
}

type validationCloneSnapshotFile struct {
	Relative   string
	Data       []byte
	Info       fs.FileInfo
	Identity   string
	Generation string
	SHA256     string
}

type validationCloneSnapshot struct {
	Files  []validationCloneSnapshotFile
	SHA256 string
	Bytes  int64
}

// CloneProjectForValidation creates a disposable clone under an explicit
// external root. It never auto-selects a source project and returns only
// anonymous counts/signatures. The caller owns cleanup of manifest.RootDir.
func (s *ProjectStore) CloneProjectForValidation(sourceID, validationRoot, anonymousID string) (ProjectManifest, ValidationCloneReport, error) {
	if err := s.requireStartupRecovery(); err != nil {
		return ProjectManifest{}, ValidationCloneReport{}, err
	}
	sourceID, anonymousID = strings.TrimSpace(sourceID), strings.TrimSpace(anonymousID)
	if err := validateProjectID(sourceID); err != nil {
		return ProjectManifest{}, ValidationCloneReport{}, err
	}
	if err := validateProjectID(anonymousID); err != nil {
		return ProjectManifest{}, ValidationCloneReport{}, fmt.Errorf("invalid anonymous clone id: %w", err)
	}
	if !filepath.IsAbs(validationRoot) {
		return ProjectManifest{}, ValidationCloneReport{}, fmt.Errorf("validation root must be absolute")
	}
	source, err := s.projectManifestWithoutTouch(sourceID)
	if err != nil {
		return ProjectManifest{}, ValidationCloneReport{}, err
	}
	validationRoot, err = filepath.Abs(validationRoot)
	if err != nil {
		return ProjectManifest{}, ValidationCloneReport{}, err
	}
	canonicalSource, err := canonicalExistingPath(source.RootDir)
	if err != nil {
		return ProjectManifest{}, ValidationCloneReport{}, fmt.Errorf("resolve source project root: %w", err)
	}
	canonicalValidationRoot, err := canonicalPathThroughExistingAncestor(validationRoot)
	if err != nil {
		return ProjectManifest{}, ValidationCloneReport{}, fmt.Errorf("resolve validation root: %w", err)
	}
	if validationPathsOverlap(canonicalValidationRoot, canonicalSource) {
		return ProjectManifest{}, ValidationCloneReport{}, fmt.Errorf("validation root must be independent from source project")
	}
	validationAncestor, err := nearestExistingAncestor(validationRoot)
	if err != nil {
		return ProjectManifest{}, ValidationCloneReport{}, fmt.Errorf("inspect validation root ancestor: %w", err)
	}
	aliasesSource, err := directoryAliasesTree(validationAncestor, canonicalSource)
	if err != nil {
		return ProjectManifest{}, ValidationCloneReport{}, fmt.Errorf("compare validation root identity: %w", err)
	}
	if aliasesSource {
		return ProjectManifest{}, ValidationCloneReport{}, fmt.Errorf("validation root ancestor aliases source project")
	}
	if err := os.MkdirAll(validationRoot, 0o700); err != nil {
		return ProjectManifest{}, ValidationCloneReport{}, err
	}
	canonicalValidationRoot, err = canonicalExistingPath(validationRoot)
	if err != nil {
		return ProjectManifest{}, ValidationCloneReport{}, fmt.Errorf("verify validation root: %w", err)
	}
	if validationPathsOverlap(canonicalValidationRoot, canonicalSource) {
		return ProjectManifest{}, ValidationCloneReport{}, fmt.Errorf("validation root resolves into source project")
	}
	finalRoot := filepath.Join(validationRoot, anonymousID)
	if _, err := os.Stat(finalRoot); !os.IsNotExist(err) {
		return ProjectManifest{}, ValidationCloneReport{}, fmt.Errorf("validation clone already exists")
	}
	allowed, err := validationCloneManifest(source.RootDir)
	if err != nil {
		return ProjectManifest{}, ValidationCloneReport{}, err
	}
	snapshot, err := captureValidationCloneSnapshot(source.RootDir, allowed)
	if err != nil {
		return ProjectManifest{}, ValidationCloneReport{}, err
	}
	if _, err := validationCloneManifest(source.RootDir); err != nil {
		return ProjectManifest{}, ValidationCloneReport{}, err
	}
	if _, err := verifyValidationCloneSnapshot(source.RootDir, snapshot); err != nil {
		return ProjectManifest{}, ValidationCloneReport{}, err
	}
	before, files, bytes := snapshot.SHA256, len(snapshot.Files), snapshot.Bytes
	stagingRoot, err := os.MkdirTemp(validationRoot, ".clone-"+anonymousID+"-")
	if err != nil {
		return ProjectManifest{}, ValidationCloneReport{}, err
	}
	installed := false
	defer func() {
		if !installed {
			_ = os.RemoveAll(stagingRoot)
			_ = os.RemoveAll(finalRoot)
		}
	}()
	if err := cloneValidationProjectSnapshot(stagingRoot, snapshot); err != nil {
		return ProjectManifest{}, ValidationCloneReport{}, err
	}
	if err := rebaseClonedProjectJSON(stagingRoot, source.RootDir, finalRoot); err != nil {
		return ProjectManifest{}, ValidationCloneReport{}, err
	}
	if err := verifyCloneFileIdentity(source.RootDir, stagingRoot); err != nil {
		return ProjectManifest{}, ValidationCloneReport{}, err
	}
	if err := os.Rename(stagingRoot, finalRoot); err != nil {
		return ProjectManifest{}, ValidationCloneReport{}, err
	}
	stagingRoot = ""
	now := time.Now().UTC()
	manifest := ProjectManifest{Version: manifestVersion, ID: anonymousID, Name: "validation-" + anonymousID, RootDir: finalRoot, OutputDir: filepath.Join(finalRoot, "output"), CreatedAt: now, UpdatedAt: now, LastAccessedAt: now, ClonedFromID: source.ID, ClonedAt: &now}
	if err := s.ensureProjectDirs(manifest); err != nil {
		return ProjectManifest{}, ValidationCloneReport{}, err
	}
	if err := writeProjectManifest(manifest); err != nil {
		return ProjectManifest{}, ValidationCloneReport{}, err
	}
	lockPath := filepath.Join(manifest.OutputDir, "meta", "revisions", "transaction.lock")
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o755); err != nil {
		return ProjectManifest{}, ValidationCloneReport{}, err
	}
	lockFile, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return ProjectManifest{}, ValidationCloneReport{}, fmt.Errorf("create validation clone transaction lock: %w", err)
	}
	if err := lockFile.Close(); err != nil {
		return ProjectManifest{}, ValidationCloneReport{}, err
	}
	lineageDigest, manifestDigest := "", ""
	if _, err := os.Stat(filepath.Join(source.OutputDir, "meta", "revisions", "expansion-publication-receipt.json")); err == nil {
		approvedPaths := make([]string, 0, len(allowed))
		for rel := range allowed {
			approvedPaths = append(approvedPaths, rel)
		}
		cloneManifest, manifestErr := storepkg.BuildExpansionValidationCloneManifest(finalRoot, anonymousID, source.ID, before, approvedPaths)
		if manifestErr != nil {
			return ProjectManifest{}, ValidationCloneReport{}, manifestErr
		}
		cloneManifestPath := filepath.Join(manifest.OutputDir, "meta", "revisions", "expansion-validation-clone-manifest.json")
		if err := os.MkdirAll(filepath.Dir(cloneManifestPath), 0o755); err != nil {
			return ProjectManifest{}, ValidationCloneReport{}, err
		}
		if err := os.WriteFile(cloneManifestPath, cloneManifest, 0o444); err != nil {
			return ProjectManifest{}, ValidationCloneReport{}, err
		}
		manifestDigest = domain.ContentSignature(cloneManifest)
		lineage, lineageErr := storepkg.CreateExpansionPublicationCloneLineage(source.OutputDir, anonymousID, cloneManifest)
		if lineageErr != nil {
			return ProjectManifest{}, ValidationCloneReport{}, lineageErr
		}
		lineagePath := filepath.Join(manifest.OutputDir, "meta", "revisions", "expansion-publication-lineage.json")
		if err := os.MkdirAll(filepath.Dir(lineagePath), 0o755); err != nil {
			return ProjectManifest{}, ValidationCloneReport{}, err
		}
		if err := os.WriteFile(lineagePath, lineage, 0o644); err != nil {
			return ProjectManifest{}, ValidationCloneReport{}, err
		}
		lineageDigest = domain.ContentSignature(lineage)
		cloneReport, reportErr := storepkg.BuildExpansionValidationCloneReport(cloneManifest, lineage)
		if reportErr != nil {
			return ProjectManifest{}, ValidationCloneReport{}, reportErr
		}
		cloneReportPath := filepath.Join(manifest.OutputDir, "meta", "revisions", "expansion-validation-clone-report.json")
		if err := os.WriteFile(cloneReportPath, cloneReport, 0o444); err != nil {
			return ProjectManifest{}, ValidationCloneReport{}, err
		}
	} else if !os.IsNotExist(err) {
		return ProjectManifest{}, ValidationCloneReport{}, err
	}
	after, err := verifyValidationCloneSnapshot(source.RootDir, snapshot)
	if err != nil || after != before {
		return ProjectManifest{}, ValidationCloneReport{}, fmt.Errorf("source project changed during validation clone")
	}
	installed = true
	return manifest, ValidationCloneReport{SchemaVersion: validationCloneSchemaVersion, AnonymousID: anonymousID, SourceSHA256: before, AfterSHA256: after, FileCount: files, Bytes: bytes, ManifestDigest: manifestDigest, LineageDigest: lineageDigest}, nil
}

func captureValidationCloneSnapshot(root string, allowed map[string]validationCloneArtifact) (validationCloneSnapshot, error) {
	keys := make([]string, 0, len(allowed))
	for rel := range allowed {
		keys = append(keys, rel)
	}
	sort.Strings(keys)
	snapshot := validationCloneSnapshot{Files: make([]validationCloneSnapshotFile, 0, len(keys))}
	treeHash := sha256.New()
	for _, rel := range keys {
		path := filepath.Join(root, filepath.FromSlash(rel))
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return validationCloneSnapshot{}, anonymousCloneError("source_snapshot_invalid", err)
		}
		identity, links, err := cloneFileIdentity(path, info)
		if err != nil || links != 1 {
			return validationCloneSnapshot{}, anonymousCloneError("source_snapshot_identity_invalid", err)
		}
		file, err := os.Open(path)
		if err != nil {
			return validationCloneSnapshot{}, anonymousCloneError("source_snapshot_unreadable", err)
		}
		openedInfo, statErr := file.Stat()
		data, readErr := io.ReadAll(file)
		closeErr := file.Close()
		if statErr != nil || readErr != nil || closeErr != nil || !os.SameFile(info, openedInfo) || openedInfo.Size() != int64(len(data)) {
			return validationCloneSnapshot{}, anonymousCloneError("source_snapshot_changed", errors.Join(statErr, readErr, closeErr))
		}
		digest := sha256.Sum256(data)
		_, _ = io.WriteString(treeHash, rel+"\x00")
		_, _ = treeHash.Write(data)
		snapshot.Files = append(snapshot.Files, validationCloneSnapshotFile{Relative: rel, Data: data, Info: openedInfo, Identity: identity, Generation: validationCloneFileGeneration(openedInfo), SHA256: hex.EncodeToString(digest[:])})
		snapshot.Bytes += int64(len(data))
	}
	snapshot.SHA256 = hex.EncodeToString(treeHash.Sum(nil))
	return snapshot, nil
}

func cloneValidationProjectSnapshot(targetRoot string, snapshot validationCloneSnapshot) error {
	for _, item := range snapshot.Files {
		target := filepath.Join(targetRoot, filepath.FromSlash(item.Relative))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return anonymousCloneError("artifact_copy_failed", err)
		}
		if err := os.WriteFile(target, item.Data, 0o600); err != nil {
			return anonymousCloneError("artifact_copy_failed", err)
		}
	}
	return nil
}

func verifyValidationCloneSnapshot(root string, snapshot validationCloneSnapshot) (string, error) {
	treeHash := sha256.New()
	for _, item := range snapshot.Files {
		path := filepath.Join(root, filepath.FromSlash(item.Relative))
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || !os.SameFile(item.Info, info) {
			return "", anonymousCloneError("source_snapshot_replaced", err)
		}
		identity, links, err := cloneFileIdentity(path, info)
		if err != nil || links != 1 || identity != item.Identity || validationCloneFileGeneration(info) != item.Generation {
			return "", anonymousCloneError("source_snapshot_identity_changed", err)
		}
		data, err := os.ReadFile(path)
		if err != nil || int64(len(data)) != info.Size() {
			return "", anonymousCloneError("source_snapshot_unreadable", err)
		}
		digest := sha256.Sum256(data)
		if hex.EncodeToString(digest[:]) != item.SHA256 {
			return "", anonymousCloneError("source_snapshot_content_changed", nil)
		}
		_, _ = io.WriteString(treeHash, item.Relative+"\x00")
		_, _ = treeHash.Write(data)
	}
	return hex.EncodeToString(treeHash.Sum(nil)), nil
}

func nearestExistingAncestor(path string) (string, error) {
	current := filepath.Clean(path)
	for {
		if _, err := os.Lstat(current); err == nil {
			return current, nil
		} else if !os.IsNotExist(err) {
			return "", err
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", os.ErrNotExist
		}
		current = parent
	}
}

func directoryAliasesTree(candidate, root string) (bool, error) {
	candidateInfo, err := os.Stat(candidate)
	if err != nil {
		return false, err
	}
	aliased := false
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !entry.IsDir() {
			return nil
		}
		info, err := os.Stat(path)
		if err != nil {
			return err
		}
		if os.SameFile(candidateInfo, info) {
			aliased = true
			return fs.SkipAll
		}
		return nil
	})
	return aliased, err
}

func hashValidationProjectTree(root string, allowed map[string]validationCloneArtifact) (string, int, int64, error) {
	paths := make([]string, 0)
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(root, path)
		if err != nil || rel == "." {
			return err
		}
		normalized := strings.ToLower(filepath.ToSlash(rel))
		if entry.IsDir() {
			return nil
		}
		if _, ok := allowed[normalized]; !ok {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("source project contains unsupported symbolic link")
		}
		if !entry.IsDir() {
			paths = append(paths, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		return "", 0, 0, err
	}
	sort.Strings(paths)
	hash := sha256.New()
	var bytes int64
	for _, rel := range paths {
		file, err := os.Open(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			return "", 0, 0, err
		}
		_, _ = io.WriteString(hash, rel+"\x00")
		written, copyErr := io.Copy(hash, file)
		closeErr := file.Close()
		if copyErr != nil {
			return "", 0, 0, copyErr
		}
		if closeErr != nil {
			return "", 0, 0, closeErr
		}
		bytes += written
	}
	return hex.EncodeToString(hash.Sum(nil)), len(paths), bytes, nil
}

const validationCloneSchemaVersion = 1

const (
	validationCloneMaxJSONBytes     = 8 << 20
	validationCloneMaxMarkdownBytes = 32 << 20
)

type validationCloneArtifact struct {
	Kind          string
	SchemaVersion int
}

type validationClonePathContract struct {
	Kind    string
	Pattern *regexp.Regexp
}

var validationClonePathContracts = []validationClonePathContract{
	{"project_manifest", regexp.MustCompile(`^project\.json$`)},
	{"formal_root", regexp.MustCompile(`^output/(story_foundation\.json|premise\.md|characters\.(json|md)|world_rules\.(json|md)|planned_relationships\.(json|md)|outline\.(json|md)|layered_outline\.(json|md)|timeline\.json|relationships\.json|foreshadow\.json)$`)},
	{"foundation_manifest", regexp.MustCompile(`^output/meta/foundation/projections\.json$`)},
	{"formal_chapter", regexp.MustCompile(`^output/chapters/(?:[0-9]{1,6}\.md|[a-z0-9][a-z0-9_-]{0,127}/final\.md)$`)},
	{"draft", regexp.MustCompile(`^output/drafts/[0-9]{1,6}\.(?:draft\.md|plan\.json)$`)},
	{"summary", regexp.MustCompile(`^output/summaries/[0-9]{1,6}\.json$`)},
	{"review", regexp.MustCompile(`^output/reviews/[0-9]{1,6}(?:\.global)?\.json$`)},
	{"stable_structure", regexp.MustCompile(`^output/meta/structure/(?:index|migration|facts)\.json$`)},
	{"stable_structure_node", regexp.MustCompile(`^output/meta/structure/(?:chapters|arcs|volumes)/[a-z0-9][a-z0-9_-]{0,127}/(?:final\.md|draft\.md|plan\.json|summary\.json|review(?:-global)?\.json|adaptation-check\.json)$`)},
	{"formal_state", regexp.MustCompile(`^output/meta/(?:progress|checkpoints|cast_ledger|compass|state_changes|last_review|last_commit|simulation_profile|simulation_merge_checkpoint|usage|usage_daily|manuscript/context-diagnostics|manuscript/content-index|manuscript/content-mutation-generation)\.(?:json|jsonl)$`)},
	{"revision", regexp.MustCompile(`^output/meta/revisions/(?:manuscript/(?:index|publication|[a-z0-9][a-z0-9_-]{0,127})\.json|adaptation-command-journal\.json)$`)},
	{"expansion_publication_trust", regexp.MustCompile(`^output/meta/revisions/expansion-publication-trust\.json$`)},
	{"expansion_publication_receipt", regexp.MustCompile(`^output/meta/revisions/expansion-publication-receipt\.json$`)},
	{"expansion_publication_lineage", regexp.MustCompile(`^output/meta/revisions/expansion-publication-lineage\.json$`)},
	{"expansion_validation_clone_manifest", regexp.MustCompile(`^output/meta/revisions/expansion-validation-clone-manifest\.json$`)},
	{"expansion_validation_clone_report", regexp.MustCompile(`^output/meta/revisions/expansion-validation-clone-report\.json$`)},
	{"revision_state", regexp.MustCompile(`^output/meta/revisions/state\.json$`)},
	{"continuation", regexp.MustCompile(`^output/meta/continuation/(?:workflow|proposal|volumes|outlines|plan|commit_journal)\.json$`)},
	{"adaptation_manifest", regexp.MustCompile(`^output/meta/adaptation/source_manifest\.json$`)},
	{"adaptation_contract", regexp.MustCompile(`^output/meta/adaptation/(?:source_reports|source_foundation|cocreate_dossier|cocreate_intent|cocreate_briefing|planning_workflow|proposal|proposal_volume_review|proposal_runtime|plan|revision_runtime|revision_service_receipts)\.json$`)},
	{"adaptation_batch", regexp.MustCompile(`^output/meta/adaptation/(?:source_chapters|source_reports|checks|source_foundation_batches|cocreate_dossier_batches|cocreate_briefing_batches)/[a-z0-9][a-z0-9_-]{0,127}\.(?:json|md)$`)},
	{"adaptation_audit", regexp.MustCompile(`^output/meta/adaptation/audits/(?:latest|index|latest_application|runs/[a-z0-9][a-z0-9_-]{0,127}|protections/[a-z0-9][a-z0-9_-]{0,127})\.json$`)},
}

func validationCloneManifest(root string) (map[string]validationCloneArtifact, error) {
	allowed := make(map[string]validationCloneArtifact)
	if err := validateCloneSourceTree(root); err != nil {
		return nil, anonymousCloneError("source_identity_invalid", err)
	}
	if _, err := os.Lstat(filepath.Join(root, "output", filepath.FromSlash("meta/foundation/journal.json"))); err == nil {
		return nil, anonymousCloneError("foundation_transaction_pending", nil)
	} else if !os.IsNotExist(err) {
		return nil, anonymousCloneError("foundation_transaction_unreadable", err)
	}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return anonymousCloneError("source_scan_failed", walkErr)
		}
		if entry.IsDir() {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return anonymousCloneError("source_scan_failed", relErr)
		}
		rawRelative := filepath.ToSlash(rel)
		normalized := strings.ToLower(rawRelative)
		for _, contract := range validationClonePathContracts {
			if contract.Pattern.MatchString(normalized) {
				if rawRelative != normalized {
					return anonymousCloneError("artifact_path_case_invalid", nil)
				}
				if _, exists := allowed[normalized]; exists {
					return anonymousCloneError("artifact_path_case_collision", nil)
				}
				allowed[normalized] = validationCloneArtifact{Kind: contract.Kind, SchemaVersion: validationCloneSchemaVersion}
				break
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if _, ok := allowed["project.json"]; !ok {
		return nil, anonymousCloneError("project_manifest_missing", nil)
	}
	if err := addManifestReferencedAdaptationArtifacts(root, allowed); err != nil {
		return nil, err
	}
	if err := validateCloneArtifacts(root, allowed); err != nil {
		return nil, err
	}
	if err := validateCloneFoundationSet(root, allowed); err != nil {
		return nil, anonymousCloneError("foundation_projection_invalid", err)
	}
	if err := validateCloneExpansionPublicationAuthority(root, allowed); err != nil {
		return nil, anonymousCloneError("artifact_schema_invalid", err)
	}
	return allowed, nil
}

func validateCloneExpansionPublicationAuthority(root string, allowed map[string]validationCloneArtifact) error {
	trustRel := "output/meta/revisions/expansion-publication-trust.json"
	receiptRel := "output/meta/revisions/expansion-publication-receipt.json"
	_, hasTrust := allowed[trustRel]
	_, hasReceipt := allowed[receiptRel]
	if _, hasLineage := allowed["output/meta/revisions/expansion-publication-lineage.json"]; hasLineage {
		return fmt.Errorf("validation clones cannot delegate clone authority")
	}
	if hasTrust != hasReceipt {
		return fmt.Errorf("expansion publication public authority is incomplete")
	}
	if !hasTrust {
		return nil
	}
	trust, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(trustRel)))
	if err != nil {
		return err
	}
	receipt, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(receiptRel)))
	if err != nil {
		return err
	}
	manifest, err := readProjectManifest(filepath.Join(root, "project.json"))
	if err != nil {
		return err
	}
	return storepkg.ValidateExpansionPublicationAuthorityForClone(trust, receipt, manifest.ID, filepath.Join(root, "output"))
}

func addManifestReferencedAdaptationArtifacts(root string, allowed map[string]validationCloneArtifact) error {
	manifestRel := "output/meta/adaptation/source_manifest.json"
	if _, ok := allowed[manifestRel]; !ok {
		return nil
	}
	payload, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(manifestRel)))
	if err != nil {
		return anonymousCloneError("adaptation_manifest_invalid", err)
	}
	type adaptationReference struct {
		Chapter int    `json:"chapter"`
		Title   string `json:"title,omitempty"`
		Path    string `json:"path"`
		SHA256  string `json:"sha256"`
		Runes   int    `json:"runes,omitempty"`
	}
	var manifest struct {
		SourcePath   string                `json:"source_path"`
		SourceSHA256 string                `json:"source_sha256"`
		ChapterCount int                   `json:"chapter_count"`
		Chapters     []adaptationReference `json:"chapters"`
	}
	if err := decodeCloneJSON(payload, &manifest); err != nil {
		return anonymousCloneError("adaptation_manifest_invalid", err)
	}
	if manifest.ChapterCount <= 0 || manifest.ChapterCount != len(manifest.Chapters) {
		return anonymousCloneError("adaptation_manifest_invalid", nil)
	}
	refs := append([]adaptationReference(nil), manifest.Chapters...)
	if !validCloneSHA256(manifest.SourceSHA256) {
		return anonymousCloneError("adaptation_reference_signature_invalid", nil)
	}
	refs = append([]adaptationReference{{Path: manifest.SourcePath, SHA256: manifest.SourceSHA256}}, refs...)
	referenced := make(map[string]struct{}, len(refs))
	for _, ref := range refs {
		if strings.TrimSpace(ref.Path) == "" || !validCloneSHA256(ref.SHA256) {
			return anonymousCloneError("adaptation_reference_signature_invalid", nil)
		}
		absolute := adaptationCloneReferencePath(root, ref.Path)
		rel, relErr := filepath.Rel(root, absolute)
		if relErr != nil || rel == "." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
			return anonymousCloneError("adaptation_reference_outside_project", relErr)
		}
		rawRelative := filepath.ToSlash(rel)
		normalized := strings.ToLower(rawRelative)
		if rawRelative != normalized {
			return anonymousCloneError("adaptation_reference_path_case_invalid", nil)
		}
		if !strings.HasPrefix(normalized, "uploads/adaptation/") && !strings.HasPrefix(normalized, "output/meta/adaptation/source_chapters/") {
			return anonymousCloneError("adaptation_reference_not_allowed", nil)
		}
		if _, duplicate := referenced[normalized]; duplicate {
			return anonymousCloneError("adaptation_reference_duplicate", nil)
		}
		referenced[normalized] = struct{}{}
		data, readErr := os.ReadFile(filepath.Join(root, rel))
		if readErr != nil {
			return anonymousCloneError("adaptation_reference_unreadable", readErr)
		}
		sum := sha256.Sum256(data)
		if hex.EncodeToString(sum[:]) != strings.TrimSpace(ref.SHA256) {
			return anonymousCloneError("adaptation_reference_signature_mismatch", nil)
		}
		allowed[normalized] = validationCloneArtifact{Kind: "adaptation_manifest_blob", SchemaVersion: validationCloneSchemaVersion}
	}
	for _, prefix := range []string{"output/meta/adaptation/source_chapters/"} {
		unreferenced, err := containsUnreferencedAdaptationBlob(root, prefix, referenced)
		if err != nil {
			return anonymousCloneError("adaptation_reference_scan_failed", err)
		}
		if unreferenced {
			return anonymousCloneError("adaptation_blob_unreferenced", nil)
		}
	}
	return validateCloneStructureProjection(root, allowed)
}

func validateCloneStructureProjection(root string, allowed map[string]validationCloneArtifact) error {
	const layeredRel = "output/layered_outline.json"
	const outlineRel = "output/outline.json"
	if _, layeredExists := allowed[layeredRel]; !layeredExists {
		return nil
	}
	if _, outlineExists := allowed[outlineRel]; !outlineExists {
		return nil
	}
	var layered []domain.VolumeOutline
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(layeredRel)))
	if err != nil {
		return fmt.Errorf("layered structure projection is unreadable: %w", err)
	}
	if err := decodeCloneJSON(data, &layered); err != nil {
		return fmt.Errorf("layered structure projection is invalid: %w", err)
	}
	var outline []domain.OutlineEntry
	data, err = os.ReadFile(filepath.Join(root, filepath.FromSlash(outlineRel)))
	if err != nil {
		return fmt.Errorf("flat structure projection is unreadable: %w", err)
	}
	if err := decodeCloneJSON(data, &outline); err != nil {
		return fmt.Errorf("flat structure projection is invalid: %w", err)
	}
	flattened := domain.FlattenOutline(layered)
	if len(flattened) != len(outline) {
		return fmt.Errorf("flat and layered structure projections differ")
	}
	for index := range flattened {
		if flattened[index].ID != outline[index].ID || flattened[index].Chapter != outline[index].Chapter {
			return fmt.Errorf("flat and layered stable chapter projections differ")
		}
	}
	return nil
}

func adaptationCloneReferencePath(root, reference string) string {
	if filepath.IsAbs(reference) {
		return reference
	}
	normalized := filepath.ToSlash(filepath.Clean(filepath.FromSlash(reference)))
	if strings.HasPrefix(normalized, "meta/adaptation/source_chapters/") {
		return filepath.Join(root, "output", filepath.FromSlash(normalized))
	}
	return filepath.Join(root, filepath.FromSlash(normalized))
}

func validCloneSHA256(value string) bool {
	value = strings.TrimSpace(value)
	if len(value) != sha256.Size*2 || value != strings.ToLower(value) {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func containsUnreferencedAdaptationBlob(root, prefix string, referenced map[string]struct{}) (bool, error) {
	directory := filepath.Join(root, filepath.FromSlash(strings.TrimSuffix(prefix, "/")))
	if _, err := os.Stat(directory); os.IsNotExist(err) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	unreferenced := false
	err := filepath.WalkDir(directory, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() {
			return walkErr
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		normalized := strings.ToLower(filepath.ToSlash(rel))
		if _, ok := referenced[normalized]; !ok {
			unreferenced = true
			return fs.SkipAll
		}
		return nil
	})
	return unreferenced, err
}

func validateCloneArtifacts(root string, allowed map[string]validationCloneArtifact) error {
	paths := make([]string, 0, len(allowed))
	for rel := range allowed {
		paths = append(paths, rel)
	}
	sort.Strings(paths)
	for _, rel := range paths {
		artifact := allowed[rel]
		if artifact.SchemaVersion != validationCloneSchemaVersion {
			return anonymousCloneError("artifact_schema_version_invalid", nil)
		}
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			return anonymousCloneError("artifact_unreadable", err)
		}
		if err := validateCloneArtifact(root, rel, artifact.Kind, data); err != nil {
			return anonymousCloneError("artifact_schema_invalid", err)
		}
	}
	return nil
}

func validateCloneArtifact(root, rel, kind string, data []byte) error {
	if kind == "adaptation_manifest_blob" {
		return validateCloneTextBlob(data)
	}
	extension := strings.ToLower(filepath.Ext(rel))
	if extension == ".md" {
		return validateCloneMarkdown(data)
	}
	if extension == ".jsonl" {
		return validateCloneJSONLines(rel, data)
	}
	if extension != ".json" {
		return fmt.Errorf("unsupported artifact format")
	}
	if len(data) == 0 || len(data) > validationCloneMaxJSONBytes {
		return fmt.Errorf("JSON artifact size is outside contract")
	}
	switch kind {
	case "project_manifest":
		return validateCloneProjectManifest(root, data)
	case "adaptation_manifest":
		return validateCloneAdaptationManifest(data)
	case "revision":
		return validateCloneRevision(rel, data)
	case "revision_state":
		return storepkg.ValidateRevisionStateForClone(data)
	case "expansion_publication_trust":
		return storepkg.ValidateExpansionPublicationTrustForClone(data)
	case "expansion_publication_receipt":
		return storepkg.ValidateExpansionPublicationReceiptForClone(data)
	case "expansion_publication_lineage":
		return storepkg.ValidateExpansionPublicationLineageForClone(data)
	case "expansion_validation_clone_manifest":
		return storepkg.ValidateExpansionValidationCloneManifestForClone(data)
	case "expansion_validation_clone_report":
		return storepkg.ValidateExpansionValidationCloneReportForClone(data)
	case "formal_root":
		return validateCloneFormalRoot(rel, data)
	case "foundation_manifest":
		return validateCloneFoundationManifest(data)
	case "formal_state":
		return validateCloneFormalState(rel, data)
	default:
		return validateProgramOwnedJSONObject(rel, kind, data)
	}
}

func validateCloneTextBlob(data []byte) error {
	if len(data) == 0 || len(data) > validationCloneMaxMarkdownBytes || !utf8.Valid(data) || strings.IndexByte(string(data), 0) >= 0 || strings.TrimSpace(string(data)) == "" {
		return fmt.Errorf("adaptation blob is not valid bounded UTF-8 content")
	}
	return nil
}

func validateCloneMarkdown(data []byte) error {
	if len(data) == 0 || len(data) > validationCloneMaxMarkdownBytes || !utf8.Valid(data) || strings.IndexByte(string(data), 0) >= 0 || strings.TrimSpace(string(data)) == "" {
		return fmt.Errorf("markdown artifact is not valid bounded UTF-8 content")
	}
	return nil
}

func validateCloneJSONLines(rel string, data []byte) error {
	if len(data) == 0 || len(data) > validationCloneMaxJSONBytes || !utf8.Valid(data) {
		return fmt.Errorf("JSONL artifact size or encoding is invalid")
	}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var target any
		switch filepath.Base(rel) {
		case "checkpoints.jsonl":
			target = &domain.Checkpoint{}
		default:
			return fmt.Errorf("JSONL artifact path has no exact record schema")
		}
		if err := decodeCloneJSON([]byte(line), target); err != nil {
			return err
		}
	}
	return nil
}

func validateCloneProjectManifest(root string, data []byte) error {
	var manifest ProjectManifest
	if err := decodeCloneJSON(data, &manifest); err != nil {
		return err
	}
	if manifest.Version != manifestVersion || validateProjectID(manifest.ID) != nil || strings.TrimSpace(manifest.Name) == "" {
		return fmt.Errorf("project manifest identity is invalid")
	}
	if filepath.Clean(manifest.RootDir) != filepath.Clean(root) || filepath.Clean(manifest.OutputDir) != filepath.Join(filepath.Clean(root), "output") {
		return fmt.Errorf("project manifest paths escape source root")
	}
	if manifest.CreatedAt.IsZero() || manifest.UpdatedAt.IsZero() || manifest.LastAccessedAt.IsZero() {
		return fmt.Errorf("project manifest timestamps are required")
	}
	return nil
}

func validateCloneAdaptationManifest(data []byte) error {
	var manifest struct {
		SourcePath   string `json:"source_path"`
		SourceSHA256 string `json:"source_sha256"`
		ChapterCount int    `json:"chapter_count"`
		Chapters     []struct {
			Chapter int    `json:"chapter"`
			Title   string `json:"title,omitempty"`
			Path    string `json:"path"`
			SHA256  string `json:"sha256"`
			Runes   int    `json:"runes,omitempty"`
		} `json:"chapters"`
	}
	if err := decodeCloneJSON(data, &manifest); err != nil {
		return err
	}
	if strings.TrimSpace(manifest.SourcePath) == "" || !validCloneSHA256(manifest.SourceSHA256) || manifest.ChapterCount <= 0 || manifest.ChapterCount != len(manifest.Chapters) {
		return fmt.Errorf("adaptation manifest identity is incomplete")
	}
	for index, chapter := range manifest.Chapters {
		if chapter.Chapter != index+1 || strings.TrimSpace(chapter.Path) == "" || !validCloneSHA256(chapter.SHA256) {
			return fmt.Errorf("adaptation chapter reference is invalid")
		}
	}
	return nil
}

func validateCloneFormalRoot(rel string, data []byte) error {
	switch filepath.Base(rel) {
	case "story_foundation.json":
		var foundation domain.StoryFoundation
		if err := decodeCloneJSON(data, &foundation); err != nil {
			return err
		}
		_, err := domain.NormalizeStoryFoundation(foundation)
		return err
	case "characters.json":
		return validateNonEmptyCloneSlice(data, &[]domain.Character{})
	case "world_rules.json":
		return validateNonEmptyCloneSlice(data, &[]domain.WorldRule{})
	case "planned_relationships.json":
		var relationships []domain.CharacterRelationship
		return decodeCloneJSON(data, &relationships)
	case "outline.json":
		var outline []domain.OutlineEntry
		if err := decodeCloneJSON(data, &outline); err != nil || len(outline) == 0 {
			return fmt.Errorf("outline must be a non-empty typed chapter array")
		}
		seen := make(map[string]string, len(outline))
		for index, chapter := range outline {
			if chapter.Chapter != index+1 || strings.TrimSpace(chapter.Title) == "" || addCloneStableIdentity(seen, chapter.ID, domain.StructureKindChapter) != nil {
				return fmt.Errorf("outline chapter identity is invalid")
			}
		}
		return nil
	case "layered_outline.json":
		var volumes []domain.VolumeOutline
		if err := decodeCloneJSON(data, &volumes); err != nil || len(volumes) == 0 {
			return fmt.Errorf("layered outline must be a non-empty typed volume array")
		}
		return validateCloneLayeredIdentity(volumes)
	case "timeline.json":
		return validateNonEmptyCloneSlice(data, &[]cloneTimelineEvent{})
	case "relationships.json":
		return validateNonEmptyCloneSlice(data, &[]domain.RelationshipEntry{})
	case "foreshadow.json":
		return validateNonEmptyCloneSlice(data, &[]domain.ForeshadowEntry{})
	}
	return fmt.Errorf("formal root path has no exact schema")
}

type cloneFoundationManifest struct {
	Version          int               `json:"version"`
	Revision         int64             `json:"revision"`
	ContentSignature string            `json:"content_signature"`
	AuditSignature   string            `json:"audit_signature"`
	Files            map[string]string `json:"files"`
}

func validateCloneFoundationManifest(data []byte) error {
	var manifest cloneFoundationManifest
	if err := decodeCloneJSON(data, &manifest); err != nil {
		return err
	}
	if manifest.Version != 1 || manifest.Revision <= 0 || !validCloneSHA256(manifest.ContentSignature) || !validCloneSHA256(manifest.AuditSignature) {
		return fmt.Errorf("foundation projection manifest identity is invalid")
	}
	if len(manifest.Files) != 8 {
		return fmt.Errorf("foundation projection manifest file set is incomplete")
	}
	return nil
}

func validateCloneFoundationSet(root string, allowed map[string]validationCloneArtifact) error {
	const canonical = "output/story_foundation.json"
	_, canonicalExists := allowed[canonical]
	required := []string{
		canonical,
		"output/premise.md",
		"output/characters.json",
		"output/characters.md",
		"output/world_rules.json",
		"output/world_rules.md",
		"output/planned_relationships.json",
		"output/planned_relationships.md",
		"output/meta/foundation/projections.json",
	}
	if !canonicalExists {
		for _, rel := range required[6:] {
			if _, exists := allowed[rel]; exists {
				return fmt.Errorf("foundation projection %s exists without canonical foundation", rel)
			}
		}
		return nil
	}
	for _, rel := range required {
		if _, exists := allowed[rel]; !exists {
			return fmt.Errorf("canonical foundation clone is missing %s", rel)
		}
	}
	canonicalData, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(canonical)))
	if err != nil {
		return err
	}
	var foundation domain.StoryFoundation
	if err := decodeCloneJSON(canonicalData, &foundation); err != nil {
		return err
	}
	normalized, err := domain.NormalizeStoryFoundation(foundation)
	if err != nil {
		return err
	}
	manifestData, err := os.ReadFile(filepath.Join(root, filepath.FromSlash("output/meta/foundation/projections.json")))
	if err != nil {
		return err
	}
	var manifest cloneFoundationManifest
	if err := decodeCloneJSON(manifestData, &manifest); err != nil {
		return err
	}
	content, err := domain.FoundationContentSignature(normalized)
	if err != nil {
		return err
	}
	audit, err := domain.FoundationAuditSignature(normalized)
	if err != nil {
		return err
	}
	if manifest.Revision != foundation.Revision || manifest.ContentSignature != content || manifest.AuditSignature != audit {
		return fmt.Errorf("canonical foundation and projection manifest are not the same revision")
	}
	expectedFiles := []string{
		"story_foundation.json", "premise.md", "characters.json", "characters.md",
		"world_rules.json", "world_rules.md", "planned_relationships.json", "planned_relationships.md",
	}
	if len(manifest.Files) != len(expectedFiles) {
		return fmt.Errorf("foundation projection manifest file set is incomplete")
	}
	for _, rel := range expectedFiles {
		data, err := os.ReadFile(filepath.Join(root, "output", filepath.FromSlash(rel)))
		if err != nil {
			return err
		}
		digest := sha256.Sum256(data)
		if manifest.Files[rel] != hex.EncodeToString(digest[:]) {
			return fmt.Errorf("foundation clone file %s signature mismatch", rel)
		}
	}
	return nil
}

type cloneTimelineEvent struct {
	Chapter    int      `json:"chapter"`
	Time       string   `json:"time"`
	Event      string   `json:"event"`
	Characters []string `json:"characters,omitempty"`
}

func validateNonEmptyCloneSlice(data []byte, target any) error {
	if err := decodeCloneJSON(data, target); err != nil {
		return err
	}
	value := reflect.ValueOf(target)
	if value.Kind() != reflect.Pointer || value.Elem().Kind() != reflect.Slice || value.Elem().Len() == 0 {
		return fmt.Errorf("typed artifact array must be non-empty")
	}
	return nil
}

func validateCloneFormalState(rel string, data []byte) error {
	base := filepath.Base(rel)
	switch base {
	case "progress.json":
		var progress domain.Progress
		if err := decodeCloneJSON(data, &progress); err != nil {
			return err
		}
		if progress.Phase == "" || progress.TotalChapters < 0 || progress.CompletedChapters == nil {
			return fmt.Errorf("progress exact schema identity is invalid")
		}
		return nil
	case "content-index.json":
		var index storepkg.ManuscriptContentIndex
		if err := decodeCloneJSON(data, &index); err != nil {
			return err
		}
		return index.Validate()
	case "content-mutation-generation.json":
		var generation struct {
			Version           int    `json:"version"`
			Generation        uint64 `json:"generation"`
			LastPath          string `json:"last_path"`
			LastContentSHA256 string `json:"last_content_sha256"`
		}
		if err := decodeCloneJSON(data, &generation); err != nil {
			return err
		}
		if generation.Version != validationCloneSchemaVersion || generation.Generation == 0 || strings.TrimSpace(generation.LastPath) == "" || !validCloneSHA256(generation.LastContentSHA256) {
			return fmt.Errorf("content mutation generation identity is invalid")
		}
		return nil
	case "context-diagnostics.json":
		var records []storepkg.ManuscriptContextDiagnostic
		if err := decodeCloneJSON(data, &records); err != nil || len(records) == 0 {
			return fmt.Errorf("context diagnostics must be a non-empty typed array")
		}
		for _, record := range records {
			if record.Version != validationCloneSchemaVersion || strings.TrimSpace(record.Task) == "" || record.InputBytes < 0 || !validCloneSHA256(record.ContentSignature) || strings.TrimSpace(record.Status) == "" || strings.TrimSpace(record.RecordedAt) == "" {
				return fmt.Errorf("context diagnostic exact schema identity is invalid")
			}
		}
		return nil
	case "cast_ledger.json":
		return validateNonEmptyCloneSlice(data, &[]domain.CastEntry{})
	case "compass.json":
		var compass domain.StoryCompass
		return decodeCloneJSON(data, &compass)
	case "state_changes.json":
		return validateNonEmptyCloneSlice(data, &[]domain.StateChange{})
	case "last_review.json":
		var review domain.ReviewEntry
		return decodeCloneJSON(data, &review)
	case "last_commit.json":
		var commit domain.CommitResult
		return decodeCloneJSON(data, &commit)
	case "simulation_profile.json":
		var profile domain.SimulationProfile
		if err := decodeCloneJSON(data, &profile); err != nil {
			return err
		}
		return domain.ValidateSimulationProfile(&profile)
	case "simulation_merge_checkpoint.json":
		var checkpoint domain.SimulationMergeCheckpoint
		return decodeCloneJSON(data, &checkpoint)
	case "usage.json":
		var usage domain.UsageState
		if err := decodeCloneJSON(data, &usage); err != nil {
			return err
		}
		if usage.Schema != domain.UsageSchemaVersion && usage.Schema != 2 {
			return fmt.Errorf("usage schema version is unsupported")
		}
		return nil
	case "usage_daily.json":
		return validateNonEmptyCloneSlice(data, &[]domain.UsageDailyAggregate{})
	default:
		return fmt.Errorf("formal state path has no exact schema")
	}
}

func validateCloneRevision(rel string, data []byte) error {
	base := strings.TrimSuffix(filepath.Base(rel), filepath.Ext(rel))
	if base == "index" {
		var index struct {
			ActiveRevisionID  string                                          `json:"active_revision_id,omitempty"`
			Revisions         map[string]domain.ManuscriptRevisionRuntime     `json:"revisions"`
			Receipts          map[string]cloneManuscriptRuntimeReceipt        `json:"receipts,omitempty"`
			ContentProvenance map[string]storepkg.ManuscriptContentProvenance `json:"content_provenance,omitempty"`
		}
		if err := decodeCloneJSON(data, &index); err != nil {
			return err
		}
		if index.Revisions == nil {
			return fmt.Errorf("revision index exact schema is incomplete")
		}
		for id, runtime := range index.Revisions {
			if id != runtime.RevisionID || runtime.Validate() != nil {
				return fmt.Errorf("revision index stable identity is invalid")
			}
		}
		return nil
	}
	if base == "publication" {
		var publication cloneManuscriptPublicationJournal
		if err := decodeCloneJSON(data, &publication); err != nil {
			return err
		}
		if strings.TrimSpace(publication.RevisionID) == "" || publication.ExpectedRevision <= 0 || strings.TrimSpace(publication.IdempotencyKey) == "" || publication.Status == "" {
			return fmt.Errorf("publication journal exact identity is invalid")
		}
		return nil
	}
	if base == "adaptation-command-journal" {
		var journal struct {
			Version     int      `json:"version"`
			Key         string   `json:"key"`
			Operation   string   `json:"operation"`
			Fingerprint string   `json:"fingerprint"`
			Files       []string `json:"files"`
		}
		if err := decodeCloneJSON(data, &journal); err != nil {
			return err
		}
		if journal.Version != validationCloneSchemaVersion || strings.TrimSpace(journal.Key) == "" || strings.TrimSpace(journal.Operation) == "" || strings.TrimSpace(journal.Fingerprint) == "" || len(journal.Files) == 0 {
			return fmt.Errorf("adaptation command journal exact identity is invalid")
		}
		return nil
	}
	if !regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,127}$`).MatchString(base) {
		return fmt.Errorf("revision filename is not a stable ID")
	}
	var runtime domain.ManuscriptRevisionRuntime
	if err := decodeCloneJSON(data, &runtime); err != nil || runtime.Validate() != nil {
		return fmt.Errorf("revision runtime typed contract is invalid")
	}
	if runtime.RevisionID != base {
		return fmt.Errorf("revision identity does not match artifact path")
	}
	return nil
}

type cloneManuscriptRuntimeReceipt struct {
	Operation   string `json:"operation"`
	Fingerprint string `json:"fingerprint"`
	RevisionID  string `json:"revision_id"`
	Revision    int    `json:"revision"`
}

type cloneManuscriptPublicationJournal struct {
	RevisionID         string                                 `json:"revision_id"`
	ExpectedRevision   int                                    `json:"expected_revision"`
	IdempotencyKey     string                                 `json:"idempotency_key"`
	PublishFingerprint string                                 `json:"publish_fingerprint"`
	CandidateSignature string                                 `json:"candidate_signature"`
	ChapterID          string                                 `json:"chapter_id"`
	DisplayChapter     int                                    `json:"display_chapter"`
	DisplayChapters    []int                                  `json:"display_chapters,omitempty"`
	Status             domain.ManuscriptPublicationStatus     `json:"status"`
	PreviousFiles      map[string]domain.ManuscriptContentRef `json:"previous_files"`
	Candidate          domain.ManuscriptCandidate             `json:"candidate"`
	Candidates         []domain.ManuscriptCandidate           `json:"candidates,omitempty"`
	UpdatedAt          string                                 `json:"updated_at"`
}

func validateProgramOwnedJSONObject(rel, kind string, data []byte) error {
	if err := validateCloneExactKind(rel, kind, data); err != nil {
		return err
	}
	return nil
}

func validateCloneExactKind(rel, kind string, data []byte) error {
	base := filepath.Base(rel)
	switch kind {
	case "draft":
		var plan domain.ChapterPlan
		if err := decodeCloneJSON(data, &plan); err != nil {
			return err
		}
		if err := validateCloneDisplayChapter(rel, plan.Chapter); err != nil {
			return err
		}
		return nil
	case "summary":
		var summary domain.ChapterSummary
		if err := decodeCloneJSON(data, &summary); err != nil {
			return err
		}
		return validateCloneDisplayChapter(rel, summary.Chapter)
	case "review":
		var review domain.ReviewEntry
		if err := decodeCloneJSON(data, &review); err != nil {
			return err
		}
		return validateCloneDisplayChapter(rel, review.Chapter)
	case "stable_structure":
		return validateCloneStableStructure(rel, data)
	case "stable_structure_node":
		return validateCloneStableStructureNode(rel, data)
	case "continuation":
		return validateCloneContinuation(base, data)
	case "adaptation_contract":
		return validateCloneAdaptationContract(base, data)
	case "adaptation_batch":
		return validateCloneAdaptationBatch(rel, data)
	case "adaptation_audit":
		return validateCloneAdaptationAudit(rel, data)
	}
	return fmt.Errorf("artifact kind has no path-specific exact schema")
}

func validateCloneDisplayChapter(rel string, actual int) error {
	match := regexp.MustCompile(`(?:^|/)([0-9]{1,6})(?:\.|/)`).FindStringSubmatch(filepath.ToSlash(rel))
	if len(match) != 2 {
		return fmt.Errorf("artifact display path identity is missing")
	}
	expected, err := strconv.Atoi(match[1])
	if err != nil || actual != expected {
		return fmt.Errorf("artifact display identity does not match its path")
	}
	return nil
}

type cloneStructureIndex struct {
	Version  int                        `json:"version"`
	Volumes  []cloneStructureVolumeRef  `json:"volumes,omitempty"`
	Arcs     []cloneStructureArcRef     `json:"arcs,omitempty"`
	Chapters []cloneStructureChapterRef `json:"chapters"`
}

type cloneStructureVolumeRef struct {
	ID     string `json:"id"`
	Number int    `json:"number"`
}

type cloneStructureArcRef struct {
	ID       string `json:"id"`
	VolumeID string `json:"volume_id"`
	Number   int    `json:"number"`
}

type cloneStructureChapterRef struct {
	ID       string `json:"id"`
	Number   int    `json:"number"`
	VolumeID string `json:"volume_id,omitempty"`
	ArcID    string `json:"arc_id,omitempty"`
}

type cloneCanonicalFacts struct {
	Timeline      []cloneCanonicalTimelineEvent     `json:"timeline,omitempty"`
	Foreshadow    []cloneCanonicalForeshadowEntry   `json:"foreshadow,omitempty"`
	Relationships []cloneCanonicalRelationshipEntry `json:"relationships,omitempty"`
	StateChanges  []cloneCanonicalStateChange       `json:"state_changes,omitempty"`
}

type cloneCanonicalTimelineEvent struct {
	ChapterID string               `json:"chapter_id,omitempty"`
	Event     domain.TimelineEvent `json:"event"`
}
type cloneCanonicalForeshadowEntry struct {
	ChapterID string                 `json:"chapter_id,omitempty"`
	Entry     domain.ForeshadowEntry `json:"entry"`
}
type cloneCanonicalRelationshipEntry struct {
	ChapterID string                   `json:"chapter_id,omitempty"`
	Entry     domain.RelationshipEntry `json:"entry"`
}
type cloneCanonicalStateChange struct {
	ChapterID string             `json:"chapter_id,omitempty"`
	Change    domain.StateChange `json:"change"`
}

func validateCloneStableStructure(rel string, data []byte) error {
	switch filepath.Base(rel) {
	case "index.json":
		var index cloneStructureIndex
		if err := decodeCloneJSON(data, &index); err != nil {
			return err
		}
		if index.Version != validationCloneSchemaVersion || len(index.Chapters) == 0 {
			return fmt.Errorf("stable structure index exact identity is invalid")
		}
		return nil
	case "facts.json":
		var facts cloneCanonicalFacts
		return decodeCloneJSON(data, &facts)
	case "migration.json":
		return fmt.Errorf("active structure migration is not cloneable")
	default:
		return fmt.Errorf("stable structure path has no exact schema")
	}
}

func validateCloneStableStructureNode(rel string, data []byte) error {
	parts := strings.Split(filepath.ToSlash(rel), "/")
	if len(parts) < 3 {
		return fmt.Errorf("stable structure node path is invalid")
	}
	expectedID := parts[len(parts)-2]
	base := filepath.Base(rel)
	var actualID string
	switch base {
	case "plan.json":
		var value struct {
			ChapterID string             `json:"chapter_id"`
			Plan      domain.ChapterPlan `json:"plan"`
		}
		if err := decodeCloneJSON(data, &value); err != nil {
			return err
		}
		actualID = value.ChapterID
	case "summary.json":
		scope := parts[len(parts)-3]
		switch scope {
		case "chapters":
			var value struct {
				ChapterID string                `json:"chapter_id"`
				Summary   domain.ChapterSummary `json:"summary"`
			}
			if err := decodeCloneJSON(data, &value); err != nil {
				return err
			}
			actualID = value.ChapterID
		case "arcs":
			var value struct {
				ArcID   string            `json:"arc_id"`
				Summary domain.ArcSummary `json:"summary"`
			}
			if err := decodeCloneJSON(data, &value); err != nil {
				return err
			}
			actualID = value.ArcID
		case "volumes":
			var value struct {
				VolumeID string               `json:"volume_id"`
				Summary  domain.VolumeSummary `json:"summary"`
			}
			if err := decodeCloneJSON(data, &value); err != nil {
				return err
			}
			actualID = value.VolumeID
		default:
			return fmt.Errorf("stable summary scope has no exact schema")
		}
	case "review.json", "review-global.json":
		var value struct {
			ChapterID          string             `json:"chapter_id"`
			VolumeID           string             `json:"volume_id,omitempty"`
			ArcID              string             `json:"arc_id,omitempty"`
			BatchFromID        string             `json:"batch_from_id,omitempty"`
			BatchToID          string             `json:"batch_to_id,omitempty"`
			AffectedChapterIDs []string           `json:"affected_chapter_ids,omitempty"`
			Review             domain.ReviewEntry `json:"review"`
		}
		if err := decodeCloneJSON(data, &value); err != nil {
			return err
		}
		actualID = firstNonEmpty(value.ChapterID, value.ArcID, value.VolumeID)
	case "adaptation-check.json":
		var value struct {
			ChapterID string                 `json:"chapter_id"`
			Check     domain.AdaptationCheck `json:"check"`
		}
		if err := decodeCloneJSON(data, &value); err != nil {
			return err
		}
		actualID = value.ChapterID
	default:
		return fmt.Errorf("stable structure node path has no exact schema")
	}
	if actualID != expectedID {
		return fmt.Errorf("stable structure node identity does not match path")
	}
	return nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func validateCloneContinuation(base string, data []byte) error {
	switch base {
	case "workflow.json":
		var workflow domain.ContinuationWorkflow
		if err := decodeCloneJSON(data, &workflow); err != nil {
			return err
		}
		if workflow.Version != domain.ContinuationSchemaVersion || !workflow.Stage.Valid() || strings.TrimSpace(workflow.SourceSignature) == "" || workflow.BaseChapterCount <= 0 || workflow.Revision <= 0 {
			return fmt.Errorf("continuation workflow identity is invalid")
		}
		return nil
	case "proposal.json":
		var proposal domain.ContinuationProposal
		if err := decodeCloneJSON(data, &proposal); err != nil {
			return err
		}
		return proposal.Validate()
	case "volumes.json":
		var volumes []domain.VolumeOutline
		if err := decodeCloneJSON(data, &volumes); err != nil || len(volumes) == 0 {
			return fmt.Errorf("continuation volume skeleton is invalid")
		}
		return validateCloneLayeredIdentity(volumes)
	case "outlines.json":
		var outline domain.ContinuationOutline
		if err := decodeCloneJSON(data, &outline); err != nil || !outline.Structure.Valid() || (len(outline.Volumes) == 0 && len(outline.Chapters) == 0) {
			return fmt.Errorf("continuation outline identity is invalid")
		}
		return nil
	case "plan.json":
		var plan domain.ContinuationPlan
		if err := decodeCloneJSON(data, &plan); err != nil {
			return err
		}
		if strings.TrimSpace(plan.SourceSignature) == "" || plan.BaseChapterCount <= 0 || plan.ApprovedRevision <= 0 || plan.Proposal.Validate() != nil {
			return fmt.Errorf("continuation plan identity is invalid")
		}
		chapters, err := domain.FlattenContinuationOutline(plan.BaseChapterCount, plan.Outlines)
		if err != nil || !reflect.DeepEqual(chapters, plan.Chapters) {
			return fmt.Errorf("continuation plan outline contract is invalid")
		}
		return nil
	case "commit_journal.json":
		var journal struct {
			Stage          string                      `json:"stage"`
			OutlineExisted bool                        `json:"outline_existed"`
			Outline        []domain.OutlineEntry       `json:"outline,omitempty"`
			LayeredExisted bool                        `json:"layered_existed"`
			Layered        []domain.VolumeOutline      `json:"layered,omitempty"`
			Progress       *domain.Progress            `json:"progress,omitempty"`
			Workflow       domain.ContinuationWorkflow `json:"workflow"`
			Plan           *domain.ContinuationPlan    `json:"plan,omitempty"`
		}
		if err := decodeCloneJSON(data, &journal); err != nil {
			return err
		}
		if strings.TrimSpace(journal.Stage) == "" || journal.Workflow.Version != domain.ContinuationSchemaVersion || !journal.Workflow.Stage.Valid() || strings.TrimSpace(journal.Workflow.SourceSignature) == "" {
			return fmt.Errorf("continuation commit journal identity is invalid")
		}
		return nil
	default:
		return fmt.Errorf("continuation path has no exact schema")
	}
}

var cloneStableStructureID = map[string]*regexp.Regexp{
	domain.StructureKindVolume:  regexp.MustCompile(`^vol_[a-f0-9]{32}$`),
	domain.StructureKindArc:     regexp.MustCompile(`^arc_[a-f0-9]{32}$`),
	domain.StructureKindChapter: regexp.MustCompile(`^ch_[a-f0-9]{32}$`),
}

// validateCloneLayeredIdentity applies the formal stable-identity contract to
// every nested owner. The domain structs retain omitempty for legacy reads,
// but validation clones are current artifacts and must not inherit that
// permissive decoding rule.
func validateCloneLayeredIdentity(volumes []domain.VolumeOutline) error {
	seen := make(map[string]string)
	displayChapter := 1
	for volumePosition, volume := range volumes {
		if volume.Index != volumePosition+1 || strings.TrimSpace(volume.Title) == "" || len(volume.Arcs) == 0 {
			return fmt.Errorf("layered volume order or ownership is invalid")
		}
		if err := addCloneStableIdentity(seen, volume.ID, domain.StructureKindVolume); err != nil {
			return err
		}
		for arcPosition, arc := range volume.Arcs {
			if arc.Index != arcPosition+1 || strings.TrimSpace(arc.Title) == "" ||
				(len(arc.Chapters) == 0 && arc.EstimatedChapters <= 0) {
				return fmt.Errorf("layered arc order or ownership is invalid")
			}
			if err := addCloneStableIdentity(seen, arc.ID, domain.StructureKindArc); err != nil {
				return err
			}
			for _, chapter := range arc.Chapters {
				if chapter.Chapter != displayChapter || strings.TrimSpace(chapter.Title) == "" {
					return fmt.Errorf("layered chapter display projection is invalid")
				}
				if err := addCloneStableIdentity(seen, chapter.ID, domain.StructureKindChapter); err != nil {
					return err
				}
				displayChapter++
			}
		}
	}
	return nil
}

func addCloneStableIdentity(seen map[string]string, id, kind string) error {
	id = strings.TrimSpace(id)
	pattern := cloneStableStructureID[kind]
	if pattern == nil || !pattern.MatchString(id) {
		return fmt.Errorf("%s stable identity is invalid", kind)
	}
	if owner, exists := seen[id]; exists {
		return fmt.Errorf("stable identity is shared by %s and %s", owner, kind)
	}
	seen[id] = kind
	return nil
}

func validateCloneAdaptationContract(base string, data []byte) error {
	var target any
	switch base {
	case "source_reports.json":
		target = &[]domain.AdaptationSourceReport{}
	case "source_foundation.json":
		target = &domain.AdaptationSourceFoundation{}
	case "cocreate_dossier.json":
		target = &domain.AdaptationCoCreateDossier{}
	case "cocreate_intent.json":
		target = &domain.AdaptationCoCreateIntent{}
	case "cocreate_briefing.json":
		target = &domain.AdaptationCoCreateBriefing{}
	case "planning_workflow.json":
		target = &domain.AdaptationPlanningWorkflow{}
	case "proposal.json", "plan.json":
		target = &domain.AdaptationPlan{}
	case "proposal_volume_review.json":
		target = &domain.AdaptationVolumeReview{}
	case "proposal_runtime.json":
		target = &domain.AdaptationProposalRuntime{}
	case "revision_runtime.json":
		target = &domain.AdaptationRevisionRuntime{}
	case "revision_service_receipts.json":
		return validateCloneAdaptationReceipts(data)
	default:
		return fmt.Errorf("adaptation contract path has no exact schema")
	}
	if err := decodeCloneJSON(data, target); err != nil {
		return err
	}
	switch value := target.(type) {
	case *[]domain.AdaptationSourceReport:
		if len(*value) == 0 {
			return fmt.Errorf("adaptation source reports are empty")
		}
	case *domain.AdaptationSourceFoundation:
		legacy := value.Version == 0 && strings.TrimSpace(value.Premise) != "" && len(value.Volumes) > 0
		current := value.Version == validationCloneSchemaVersion && strings.TrimSpace(value.SourceSignature) != "" && strings.TrimSpace(value.PromptVersion) != ""
		if !legacy && !current {
			return fmt.Errorf("adaptation source foundation version identity is invalid")
		}
	case *domain.AdaptationCoCreateDossier:
		if value.Version != validationCloneSchemaVersion || strings.TrimSpace(value.PromptVersion) == "" || strings.TrimSpace(value.SourceSignature) == "" || value.SourceChapterCount <= 0 {
			return fmt.Errorf("adaptation dossier version identity is invalid")
		}
	case *domain.AdaptationCoCreateIntent:
		if value.Version != validationCloneSchemaVersion || strings.TrimSpace(value.RawRequest) == "" {
			return fmt.Errorf("adaptation intent version identity is invalid")
		}
	case *domain.AdaptationCoCreateBriefing:
		if value.Version != validationCloneSchemaVersion || strings.TrimSpace(value.SourceSignature) == "" || strings.TrimSpace(value.IntentHash) == "" {
			return fmt.Errorf("adaptation briefing version identity is invalid")
		}
	case *domain.AdaptationPlanningWorkflow:
		return value.Validate()
	case *domain.AdaptationPlan:
		if strings.TrimSpace(value.Granularity) == "" || strings.TrimSpace(value.Status) == "" || strings.TrimSpace(value.RewritePolicy) == "" || len(value.Chapters) == 0 {
			return fmt.Errorf("adaptation plan identity is invalid")
		}
	case *domain.AdaptationVolumeReview:
		if strings.TrimSpace(value.Status) == "" || strings.TrimSpace(value.Brief) == "" || value.TargetChapterCount <= 0 || len(value.Volumes) == 0 {
			return fmt.Errorf("adaptation volume review identity is invalid")
		}
	case *domain.AdaptationProposalRuntime:
		if value.Version != validationCloneSchemaVersion || strings.TrimSpace(value.Brief) == "" || strings.TrimSpace(value.Granularity) == "" || strings.TrimSpace(value.RewritePolicy) == "" || value.TargetChapterCount <= 0 {
			return fmt.Errorf("adaptation proposal runtime identity is invalid")
		}
	case *domain.AdaptationRevisionRuntime:
		return value.Validate()
	}
	return nil
}

func validateCloneAdaptationReceipts(data []byte) error {
	var state struct {
		Version  int `json:"version"`
		Receipts map[string]struct {
			Operation   string          `json:"operation"`
			Fingerprint string          `json:"fingerprint"`
			Result      json.RawMessage `json:"result"`
		} `json:"receipts"`
	}
	if err := decodeCloneJSON(data, &state); err != nil {
		return err
	}
	if state.Version != validationCloneSchemaVersion || state.Receipts == nil {
		return fmt.Errorf("adaptation receipt state identity is invalid")
	}
	for _, receipt := range state.Receipts {
		if strings.TrimSpace(receipt.Operation) == "" || strings.TrimSpace(receipt.Fingerprint) == "" {
			return fmt.Errorf("adaptation receipt identity is invalid")
		}
		variants := []any{&domain.AdaptationRevisionRuntime{}, &domain.AdaptationPlan{}, &domain.RevisionSession{}, &domain.AdaptationPlanningWorkflow{}}
		valid := false
		for _, variant := range variants {
			if decodeCloneJSON(receipt.Result, variant) == nil {
				valid = true
				break
			}
		}
		if !valid {
			return fmt.Errorf("adaptation receipt result has no exact schema variant")
		}
	}
	return nil
}

func validateCloneAdaptationBatch(rel string, data []byte) error {
	parts := strings.Split(filepath.ToSlash(rel), "/")
	if len(parts) < 2 {
		return fmt.Errorf("adaptation batch path is invalid")
	}
	var target any
	switch parts[len(parts)-2] {
	case "source_reports":
		target = &domain.AdaptationSourceReport{}
	case "checks":
		target = &domain.AdaptationCheck{}
	case "source_foundation_batches":
		target = &domain.AdaptationSourceFoundationBatch{}
	case "cocreate_dossier_batches":
		target = &domain.AdaptationCoCreateDossierBatch{}
	case "cocreate_briefing_batches":
		target = &domain.AdaptationCoCreateBriefingBatch{}
	default:
		return fmt.Errorf("adaptation batch path has no exact schema")
	}
	return decodeCloneJSON(data, target)
}

func validateCloneAdaptationAudit(rel string, data []byte) error {
	parts := strings.Split(filepath.ToSlash(rel), "/")
	base := filepath.Base(rel)
	var target any
	if len(parts) >= 2 && parts[len(parts)-2] == "runs" {
		target = &adaptaudit.AuditRun{}
	} else if len(parts) >= 2 && parts[len(parts)-2] == "protections" {
		target = &struct {
			Version   int      `json:"version"`
			RunID     string   `json:"run_id"`
			Reasons   []string `json:"reasons"`
			AppliedAt string   `json:"applied_at,omitempty"`
		}{}
	} else {
		switch base {
		case "latest.json":
			target = &adaptaudit.Report{}
		case "index.json":
			target = &adaptaudit.AuditRunIndex{}
		case "latest_application.json":
			target = &adaptaudit.RepairApplication{}
		default:
			return fmt.Errorf("adaptation audit path has no exact schema")
		}
	}
	return decodeCloneJSON(data, target)
}

func decodeCloneJSON(data []byte, target any) error {
	if err := validateCloneJSONShape(data, reflect.TypeOf(target)); err != nil {
		return err
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if decoder.More() {
		return fmt.Errorf("multiple JSON values are not allowed")
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return fmt.Errorf("trailing JSON content is not allowed")
	}
	return nil
}

var (
	cloneRawMessageType = reflect.TypeOf(json.RawMessage{})
	cloneTimeType       = reflect.TypeOf(time.Time{})
	cloneTimelineType   = reflect.TypeOf(domain.TimelineEvent{})
	cloneTextListType   = reflect.TypeOf(domain.TextList{})
	cloneWordBudgetType = reflect.TypeOf(domain.AdaptationChapterWordBudget{})
)

func validateCloneJSONShape(data []byte, targetType reflect.Type) error {
	if targetType == nil {
		return fmt.Errorf("exact JSON schema target is required")
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return err
	}
	return validateCloneValueShape(value, targetType)
}

func validateCloneValueShape(value any, targetType reflect.Type) error {
	for targetType.Kind() == reflect.Pointer {
		if value == nil {
			return nil
		}
		targetType = targetType.Elem()
	}
	if targetType == cloneRawMessageType || targetType.Kind() == reflect.Interface {
		return nil
	}
	if targetType == cloneTimeType {
		if _, ok := value.(string); !ok {
			return fmt.Errorf("timestamp has the wrong JSON type")
		}
		return nil
	}
	if targetType == cloneTimelineType {
		if _, legacy := value.(string); legacy {
			return nil
		}
		return validateCloneStructShape(value, targetType, nil)
	}
	if targetType == cloneTextListType {
		if _, singleton := value.(string); singleton {
			return nil
		}
		return validateCloneValueShape(value, reflect.TypeOf([]string{}))
	}
	if targetType == cloneWordBudgetType {
		legacy := map[string]reflect.Type{
			"source_words": reflect.TypeOf(int(0)), "target_words": reflect.TypeOf(int(0)),
			"min_words": reflect.TypeOf(int(0)), "max_words": reflect.TypeOf(int(0)),
		}
		return validateCloneStructShape(value, targetType, legacy)
	}
	switch targetType.Kind() {
	case reflect.Struct:
		return validateCloneStructShape(value, targetType, nil)
	case reflect.Slice, reflect.Array:
		items, ok := value.([]any)
		if !ok {
			return fmt.Errorf("JSON array has the wrong exact schema type")
		}
		for _, item := range items {
			if err := validateCloneValueShape(item, targetType.Elem()); err != nil {
				return err
			}
		}
		return nil
	case reflect.Map:
		object, ok := value.(map[string]any)
		if !ok {
			return fmt.Errorf("JSON map has the wrong exact schema type")
		}
		for key, child := range object {
			if err := validateCloneMapKey(key, targetType.Key()); err != nil {
				return err
			}
			if err := validateCloneValueShape(child, targetType.Elem()); err != nil {
				return err
			}
		}
		return nil
	case reflect.String:
		if _, ok := value.(string); !ok {
			return fmt.Errorf("JSON string field has the wrong type")
		}
	case reflect.Bool:
		if _, ok := value.(bool); !ok {
			return fmt.Errorf("JSON boolean field has the wrong type")
		}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64:
		if _, ok := value.(json.Number); !ok {
			return fmt.Errorf("JSON number field has the wrong type")
		}
	default:
		return fmt.Errorf("unsupported exact JSON schema type")
	}
	return nil
}

func validateCloneMapKey(key string, targetType reflect.Type) error {
	switch targetType.Kind() {
	case reflect.String:
		return nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		value, err := strconv.ParseInt(key, 10, targetType.Bits())
		if err != nil || strconv.FormatInt(value, 10) != key {
			return fmt.Errorf("JSON map key is not a lossless decimal integer")
		}
		return nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		value, err := strconv.ParseUint(key, 10, targetType.Bits())
		if err != nil || strconv.FormatUint(value, 10) != key {
			return fmt.Errorf("JSON map key is not a lossless decimal integer")
		}
		return nil
	default:
		return fmt.Errorf("JSON map has an unsupported exact key type")
	}
}

func validateCloneStructShape(value any, targetType reflect.Type, extra map[string]reflect.Type) error {
	object, ok := value.(map[string]any)
	if !ok {
		return fmt.Errorf("JSON object has the wrong exact schema type")
	}
	fields := make(map[string]reflect.Type)
	required := make(map[string]struct{})
	for index := 0; index < targetType.NumField(); index++ {
		field := targetType.Field(index)
		if field.PkgPath != "" {
			continue
		}
		tag := field.Tag.Get("json")
		parts := strings.Split(tag, ",")
		name := parts[0]
		if name == "-" {
			continue
		}
		if name == "" {
			name = field.Name
		}
		fields[name] = field.Type
		if !slices.Contains(parts[1:], "omitempty") {
			required[name] = struct{}{}
		}
	}
	for name, fieldType := range extra {
		fields[name] = fieldType
	}
	for name, child := range object {
		fieldType, exists := fields[name]
		if !exists {
			return fmt.Errorf("program-owned JSON contains an undeclared nested field")
		}
		if err := validateCloneValueShape(child, fieldType); err != nil {
			return err
		}
	}
	for name := range required {
		if _, exists := object[name]; !exists {
			return fmt.Errorf("program-owned JSON is missing a required nested field")
		}
	}
	return nil
}

func cloneValidationProjectTree(sourceRoot, targetRoot string, allowed map[string]validationCloneArtifact) error {
	keys := make([]string, 0, len(allowed))
	for rel := range allowed {
		keys = append(keys, rel)
	}
	sort.Strings(keys)
	for _, rel := range keys {
		source := filepath.Join(sourceRoot, filepath.FromSlash(rel))
		target := filepath.Join(targetRoot, filepath.FromSlash(rel))
		if err := copyFileOverwrite(source, target); err != nil {
			return anonymousCloneError("artifact_copy_failed", err)
		}
	}
	return nil
}

func anonymousCloneError(code string, cause error) error {
	if cause == nil {
		return fmt.Errorf("validation_clone:%s", code)
	}
	return fmt.Errorf("validation_clone:%s", code)
}

func verifyCloneFileIdentity(sourceRoot, cloneRoot string) error {
	return filepath.WalkDir(cloneRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() {
			return walkErr
		}
		rel, err := filepath.Rel(cloneRoot, path)
		if err != nil {
			return err
		}
		sourceInfo, err := os.Stat(filepath.Join(sourceRoot, rel))
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		cloneInfo, err := os.Stat(path)
		if err != nil {
			return err
		}
		if os.SameFile(sourceInfo, cloneInfo) {
			return fmt.Errorf("clone file aliases source identity")
		}
		return nil
	})
}

func validationPathsOverlap(left, right string) bool {
	left, _ = filepath.Abs(left)
	right, _ = filepath.Abs(right)
	within := func(parent, child string) bool {
		rel, err := filepath.Rel(parent, child)
		return err == nil && (rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)))
	}
	return within(left, right) || within(right, left)
}

func canonicalExistingPath(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	canonical, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", err
	}
	return filepath.Clean(canonical), nil
}

// canonicalPathThroughExistingAncestor resolves every link/junction that can
// affect a path before the path is created. Missing suffix components are
// appended only after the nearest existing ancestor has been canonicalized.
func canonicalPathThroughExistingAncestor(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	current := filepath.Clean(absolute)
	missing := make([]string, 0)
	for {
		_, statErr := os.Lstat(current)
		if statErr == nil {
			canonical, evalErr := filepath.EvalSymlinks(current)
			if evalErr != nil {
				return "", evalErr
			}
			for index := len(missing) - 1; index >= 0; index-- {
				canonical = filepath.Join(canonical, missing[index])
			}
			return filepath.Clean(canonical), nil
		}
		if !os.IsNotExist(statErr) {
			return "", statErr
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", statErr
		}
		missing = append(missing, filepath.Base(current))
		current = parent
	}
}
