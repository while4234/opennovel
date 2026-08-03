package store

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain"
)

const (
	expansionValidationCloneManifestVersion = 1
	expansionValidationCloneManifestFile    = "meta/revisions/expansion-validation-clone-manifest.json"
	expansionValidationCloneReportFile      = "meta/revisions/expansion-validation-clone-report.json"
)

type ExpansionValidationCloneFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

type ExpansionValidationCloneReport struct {
	Version          int    `json:"version"`
	AnonymousID      string `json:"anonymous_id"`
	SourceTreeSHA256 string `json:"source_tree_sha256"`
	CloneTreeSHA256  string `json:"clone_tree_sha256"`
	FileCount        int    `json:"file_count"`
	Bytes            int64  `json:"bytes"`
	ManifestDigest   string `json:"manifest_digest"`
	LineageDigest    string `json:"lineage_digest"`
}

type expansionValidationCloneSnapshotFile struct {
	Path        string
	Info        fs.FileInfo
	Identity    string
	ChangeStamp string
	Links       uint64
	SHA256      string
}

type expansionValidationCloneSnapshot struct {
	Root  string
	Files []expansionValidationCloneSnapshotFile
}

// ExpansionValidationCloneManifest is an exact inventory of the anonymous
// clone before lineage metadata is added. The source authority signs the raw
// persisted manifest digest through ExpansionPublicationCloneLineage.
type ExpansionValidationCloneManifest struct {
	Version             int                            `json:"version"`
	AnonymousID         string                         `json:"anonymous_id"`
	ClonedFromProjectID string                         `json:"cloned_from_project_id"`
	CloneRootHash       string                         `json:"clone_root_hash"`
	SourceTreeSHA256    string                         `json:"source_tree_sha256"`
	CloneTreeSHA256     string                         `json:"clone_tree_sha256"`
	SourceReceiptDigest string                         `json:"source_receipt_digest"`
	FileCount           int                            `json:"file_count"`
	Bytes               int64                          `json:"bytes"`
	Files               []ExpansionValidationCloneFile `json:"files"`
}

// BuildExpansionValidationCloneManifest inventories only the paths approved
// by the validation-clone schema. Callers must pass the complete approved set.
func BuildExpansionValidationCloneManifest(cloneRoot, anonymousID, sourceProjectID, sourceTreeSHA string, approvedPaths []string) ([]byte, error) {
	paths := append([]string(nil), approvedPaths...)
	sort.Strings(paths)
	paths = compactCloneManifestPaths(paths)
	manifest := ExpansionValidationCloneManifest{
		Version: expansionValidationCloneManifestVersion, AnonymousID: strings.TrimSpace(anonymousID),
		ClonedFromProjectID: strings.TrimSpace(sourceProjectID), SourceTreeSHA256: sourceTreeSHA,
		Files: make([]ExpansionValidationCloneFile, 0, len(paths)),
	}
	cloneRootHash, err := expansionPublicationRootHash(cloneRoot)
	if err != nil {
		return nil, err
	}
	manifest.CloneRootHash = cloneRootHash
	for _, rel := range paths {
		entry, err := expansionCloneManifestFile(cloneRoot, rel)
		if err != nil {
			return nil, err
		}
		manifest.Files = append(manifest.Files, entry)
		manifest.Bytes += entry.Size
	}
	manifest.FileCount = len(manifest.Files)
	cloneTreeDigest, err := expansionCloneManifestTreeDigest(cloneRoot, manifest.Files)
	if err != nil {
		return nil, err
	}
	manifest.CloneTreeSHA256 = cloneTreeDigest
	receiptData, err := os.ReadFile(filepath.Join(cloneRoot, "output", filepath.FromSlash(expansionPublicationReceiptFile)))
	if err != nil {
		return nil, fmt.Errorf("read validation clone publication receipt: %w", err)
	}
	var receipt ExpansionPublicationReceipt
	if err := decodeExactJSON(receiptData, &receipt); err != nil {
		return nil, fmt.Errorf("decode validation clone publication receipt: %w", err)
	}
	receiptPayload, err := json.Marshal(receipt)
	if err != nil {
		return nil, err
	}
	manifest.SourceReceiptDigest = domain.ContentSignature(receiptPayload)
	if err := validateExpansionValidationCloneManifest(manifest); err != nil {
		return nil, err
	}
	return json.MarshalIndent(manifest, "", "  ")
}

func compactCloneManifestPaths(paths []string) []string {
	result := paths[:0]
	for _, path := range paths {
		normalized := filepath.ToSlash(filepath.Clean(path))
		if len(result) == 0 || result[len(result)-1] != normalized {
			result = append(result, normalized)
		}
	}
	return result
}

func expansionCloneManifestFile(root, rel string) (ExpansionValidationCloneFile, error) {
	if rel == "" || filepath.IsAbs(rel) || rel == "." || strings.HasPrefix(rel, "../") || strings.Contains(rel, "/../") {
		return ExpansionValidationCloneFile{}, fmt.Errorf("validation clone manifest path is invalid")
	}
	abs := filepath.Join(root, filepath.FromSlash(rel))
	info, err := os.Lstat(abs)
	if err != nil {
		return ExpansionValidationCloneFile{}, fmt.Errorf("read validation clone artifact %q: %w", rel, err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return ExpansionValidationCloneFile{}, fmt.Errorf("validation clone artifact %q is not a regular file", rel)
	}
	file, err := os.Open(abs)
	if err != nil {
		return ExpansionValidationCloneFile{}, err
	}
	hash := sha256.New()
	size, copyErr := io.Copy(hash, file)
	closeErr := file.Close()
	if copyErr != nil {
		return ExpansionValidationCloneFile{}, copyErr
	}
	if closeErr != nil {
		return ExpansionValidationCloneFile{}, closeErr
	}
	return ExpansionValidationCloneFile{Path: filepath.ToSlash(rel), SHA256: hex.EncodeToString(hash.Sum(nil)), Size: size}, nil
}

func validateExpansionValidationCloneManifest(manifest ExpansionValidationCloneManifest) error {
	if manifest.Version != expansionValidationCloneManifestVersion || strings.TrimSpace(manifest.AnonymousID) == "" ||
		strings.TrimSpace(manifest.ClonedFromProjectID) == "" || len(manifest.CloneRootHash) != 64 || len(manifest.SourceTreeSHA256) != 64 || len(manifest.CloneTreeSHA256) != 64 ||
		len(manifest.SourceReceiptDigest) != 64 || manifest.FileCount <= 0 || manifest.FileCount != len(manifest.Files) || manifest.Bytes < 0 {
		return fmt.Errorf("validation clone manifest is incomplete")
	}
	var bytes int64
	previous := ""
	for _, entry := range manifest.Files {
		if entry.Path == "" || entry.Path <= previous || filepath.IsAbs(entry.Path) || strings.Contains(entry.Path, "\\") ||
			strings.HasPrefix(entry.Path, "../") || strings.Contains(entry.Path, "/../") || len(entry.SHA256) != 64 || entry.Size < 0 {
			return fmt.Errorf("validation clone manifest inventory is invalid")
		}
		previous = entry.Path
		bytes += entry.Size
	}
	if bytes != manifest.Bytes {
		return fmt.Errorf("validation clone manifest byte total is invalid")
	}
	return nil
}

func expansionCloneManifestTreeDigest(root string, entries []ExpansionValidationCloneFile) (string, error) {
	hash := sha256.New()
	for _, entry := range entries {
		file, err := os.Open(filepath.Join(root, filepath.FromSlash(entry.Path)))
		if err != nil {
			return "", err
		}
		_, _ = io.WriteString(hash, entry.Path+"\x00")
		_, copyErr := io.Copy(hash, file)
		closeErr := file.Close()
		if copyErr != nil {
			return "", copyErr
		}
		if closeErr != nil {
			return "", closeErr
		}
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// ValidateExpansionValidationCloneManifestForClone enforces the exact public
// schema. A validation clone still cannot be used as a clone source because it
// also carries non-delegable signed lineage.
func ValidateExpansionValidationCloneManifestForClone(data []byte) error {
	var manifest ExpansionValidationCloneManifest
	if err := decodeExactJSON(data, &manifest); err != nil {
		return err
	}
	return validateExpansionValidationCloneManifest(manifest)
}

func BuildExpansionValidationCloneReport(manifestData, lineageData []byte) ([]byte, error) {
	var manifest ExpansionValidationCloneManifest
	if err := decodeExactJSON(manifestData, &manifest); err != nil {
		return nil, err
	}
	if err := validateExpansionValidationCloneManifest(manifest); err != nil {
		return nil, err
	}
	report := ExpansionValidationCloneReport{
		Version: expansionValidationCloneManifestVersion, AnonymousID: manifest.AnonymousID,
		SourceTreeSHA256: manifest.SourceTreeSHA256, CloneTreeSHA256: manifest.CloneTreeSHA256,
		FileCount: manifest.FileCount, Bytes: manifest.Bytes,
		ManifestDigest: domain.ContentSignature(manifestData), LineageDigest: domain.ContentSignature(lineageData),
	}
	return json.MarshalIndent(report, "", "  ")
}

func ValidateExpansionValidationCloneReportForClone(data []byte) error {
	var report ExpansionValidationCloneReport
	if err := decodeExactJSON(data, &report); err != nil {
		return err
	}
	if report.Version != expansionValidationCloneManifestVersion || report.AnonymousID == "" || len(report.SourceTreeSHA256) != 64 ||
		len(report.CloneTreeSHA256) != 64 || report.FileCount <= 0 || report.Bytes < 0 || len(report.ManifestDigest) != 64 || len(report.LineageDigest) != 64 {
		return fmt.Errorf("validation clone report is incomplete")
	}
	return nil
}

func validateExpansionValidationCloneManifestAt(outputDir string, lineage ExpansionPublicationCloneLineage, receipt ExpansionPublicationReceipt) (func() error, error) {
	root := filepath.Dir(filepath.Clean(outputDir))
	manifestPath := filepath.Join(outputDir, filepath.FromSlash(expansionValidationCloneManifestFile))
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return nil, fmt.Errorf("validation clone manifest is unavailable: %w", err)
	}
	if domain.ContentSignature(data) != lineage.CloneManifestDigest {
		return nil, fmt.Errorf("validation clone manifest digest does not match signed lineage")
	}
	var manifest ExpansionValidationCloneManifest
	if err := decodeExactJSON(data, &manifest); err != nil {
		return nil, err
	}
	if err := validateExpansionValidationCloneManifest(manifest); err != nil {
		return nil, err
	}
	lineageData, err := os.ReadFile(filepath.Join(outputDir, filepath.FromSlash(expansionPublicationLineageFile)))
	if err != nil {
		return nil, err
	}
	reportData, err := os.ReadFile(filepath.Join(outputDir, filepath.FromSlash(expansionValidationCloneReportFile)))
	if err != nil {
		return nil, fmt.Errorf("validation clone report is unavailable: %w", err)
	}
	var report ExpansionValidationCloneReport
	if err := decodeExactJSON(reportData, &report); err != nil {
		return nil, err
	}
	if err := ValidateExpansionValidationCloneReportForClone(reportData); err != nil || report.AnonymousID != manifest.AnonymousID ||
		report.SourceTreeSHA256 != manifest.SourceTreeSHA256 || report.CloneTreeSHA256 != manifest.CloneTreeSHA256 ||
		report.FileCount != manifest.FileCount || report.Bytes != manifest.Bytes || report.ManifestDigest != domain.ContentSignature(data) ||
		report.LineageDigest != domain.ContentSignature(lineageData) {
		return nil, fmt.Errorf("validation clone report differs from signed manifest lineage")
	}
	receiptPayload, err := json.Marshal(receipt)
	if err != nil {
		return nil, err
	}
	if manifest.AnonymousID != lineage.CloneAnonymousID || manifest.ClonedFromProjectID != lineage.SourceProjectID ||
		manifest.SourceReceiptDigest != domain.ContentSignature(receiptPayload) {
		return nil, fmt.Errorf("validation clone manifest identity is stale or mismatched")
	}
	currentRootHash, err := expansionPublicationRootHash(root)
	if err != nil || currentRootHash != manifest.CloneRootHash {
		return nil, fmt.Errorf("validation clone physical root differs from signed manifest")
	}
	expected := make(map[string]ExpansionValidationCloneFile, len(manifest.Files))
	for _, entry := range manifest.Files {
		expected[entry.Path] = entry
		actual, err := expansionCloneManifestFile(root, entry.Path)
		if err != nil || actual != entry {
			return nil, fmt.Errorf("validation clone artifact %q differs from signed manifest", entry.Path)
		}
	}
	cloneTreeDigest, err := expansionCloneManifestTreeDigest(root, manifest.Files)
	if err != nil || cloneTreeDigest != manifest.CloneTreeSHA256 {
		return nil, fmt.Errorf("validation clone tree digest differs from signed manifest")
	}
	reserved := map[string]struct{}{
		filepath.ToSlash(filepath.Join("output", expansionValidationCloneManifestFile)): {},
		filepath.ToSlash(filepath.Join("output", expansionPublicationLineageFile)):      {},
		filepath.ToSlash(filepath.Join("output", expansionValidationCloneReportFile)):   {},
		"output/meta/revisions/transaction.lock":                                        {},
	}
	lockPath := filepath.Join(root, filepath.FromSlash("output/meta/revisions/transaction.lock"))
	lockInfo, err := os.Lstat(lockPath)
	if err != nil {
		return nil, fmt.Errorf("validation clone transaction lock is unavailable: %w", err)
	}
	if err := validateExpansionValidationCloneLock(lockPath, fs.FileInfoToDirEntry(lockInfo)); err != nil {
		return nil, err
	}
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		rel = filepath.ToSlash(rel)
		if _, ok := reserved[rel]; ok {
			return nil
		}
		if _, ok := expected[rel]; !ok {
			return fmt.Errorf("validation clone contains unmanifested artifact %q", rel)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	snapshot, err := captureExpansionValidationCloneSnapshot(root)
	if err != nil {
		return nil, err
	}
	return snapshot.Verify, nil
}

func validateExpansionValidationCloneLock(path string, entry fs.DirEntry) error {
	info, err := entry.Info()
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > 512 {
		return fmt.Errorf("validation clone transaction lock is unsafe")
	}
	_, links, err := validationCloneFileIdentity(path, info)
	if err != nil || links != 1 {
		return fmt.Errorf("validation clone transaction lock identity is unsafe")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" {
		return nil
	}
	parts := strings.Split(trimmed, "\n")
	if len(parts) != 2 {
		return fmt.Errorf("validation clone transaction lock state is invalid")
	}
	if _, err := strconv.Atoi(strings.TrimSpace(parts[0])); err != nil {
		return fmt.Errorf("validation clone transaction lock owner is invalid")
	}
	if _, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(parts[1])); err != nil {
		return fmt.Errorf("validation clone transaction lock timestamp is invalid")
	}
	return nil
}

func captureExpansionValidationCloneSnapshot(root string) (expansionValidationCloneSnapshot, error) {
	snapshot := expansionValidationCloneSnapshot{Root: root}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("validation clone snapshot contains unsafe path")
		}
		identity, links, err := validationCloneFileIdentity(path, info)
		if err != nil || links != 1 {
			return fmt.Errorf("validation clone snapshot contains aliased path")
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		digest := sha256.Sum256(data)
		rel, _ := filepath.Rel(root, path)
		changeStamp, err := authoritativeFileChangeStamp(path, info)
		if err != nil {
			return err
		}
		snapshot.Files = append(snapshot.Files, expansionValidationCloneSnapshotFile{Path: filepath.ToSlash(rel), Info: info, Identity: identity, ChangeStamp: changeStamp, Links: links, SHA256: hex.EncodeToString(digest[:])})
		return nil
	})
	return snapshot, err
}

func (s expansionValidationCloneSnapshot) Verify() error {
	remaining := make(map[string]struct{}, len(s.Files))
	for _, expected := range s.Files {
		remaining[expected.Path] = struct{}{}
	}
	if err := filepath.WalkDir(s.Root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(s.Root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if _, ok := remaining[rel]; !ok {
			return fmt.Errorf("validation clone tree gained artifact %q", rel)
		}
		delete(remaining, rel)
		return nil
	}); err != nil {
		return err
	}
	if len(remaining) != 0 {
		return fmt.Errorf("validation clone tree lost %d artifact(s)", len(remaining))
	}
	for _, expected := range s.Files {
		path := filepath.Join(s.Root, filepath.FromSlash(expected.Path))
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() || !os.SameFile(expected.Info, info) {
			return fmt.Errorf("validation clone artifact %q was replaced", expected.Path)
		}
		identity, links, err := validationCloneFileIdentity(path, info)
		changeStamp, stampErr := authoritativeFileChangeStamp(path, info)
		if err != nil || stampErr != nil || identity != expected.Identity || changeStamp != expected.ChangeStamp || links != expected.Links || links != 1 {
			return fmt.Errorf("validation clone artifact %q identity changed", expected.Path)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		digest := sha256.Sum256(data)
		if hex.EncodeToString(digest[:]) != expected.SHA256 {
			return fmt.Errorf("validation clone artifact %q content changed", expected.Path)
		}
	}
	return nil
}
