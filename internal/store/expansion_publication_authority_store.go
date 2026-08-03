package store

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	stdio "io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

const (
	expansionAuthorityRootVersion = 1
	expansionAuthorityAlgorithm   = "ed25519"
)

var expansionAuthorityMu sync.Mutex

var expansionAuthorityWriteFault func(path, stage string) error

// SetExpansionAuthorityWriteFaultForTesting installs a process-local fault at
// the protected release-authority atomic-write seam. It is intended only for
// cross-package integration tests that must exercise the real service path.
func SetExpansionAuthorityWriteFaultForTesting(fault func(path, stage string) error) func() {
	previous := expansionAuthorityWriteFault
	expansionAuthorityWriteFault = fault
	return func() { expansionAuthorityWriteFault = previous }
}

var expansionAuthorityInitializationAllowed bool
var expansionAuthorityTestConfigurationActive bool
var expansionAuthorityBootstrapVerifier = verifyExpansionAuthorityBootstrapPrivileges

// These unexported resolvers are fixed to release-managed paths in production.
// Same-package tests replace them explicitly; no environment variable, argv
// spelling, project file, or runtime request can select another trust domain.
var expansionAuthorityRootDirResolver = platformExpansionAuthorityRootDir
var expansionAuthorityInstallationDirResolver = platformExpansionAuthorityInstallationDir

// ConfigureExpansionAuthorityForTestProcess is callable only from a Go test
// binary. It gives black-box package tests one explicit dependency-injection
// boundary without restoring any environment or argv-triggered product path.
func ConfigureExpansionAuthorityForTestProcess(rootDir string) (func(), error) {
	if !testing.Testing() {
		return nil, fmt.Errorf("authority test configuration is unavailable outside a Go test process")
	}
	return configureExpansionAuthorityForIsolatedProcess(rootDir)
}

func configureExpansionAuthorityForIsolatedProcess(rootDir string) (func(), error) {
	if !filepath.IsAbs(rootDir) {
		return nil, fmt.Errorf("authority test root must be absolute")
	}
	expansionAuthorityMu.Lock()
	previousRoot := expansionAuthorityRootDirResolver
	previousInstallation := expansionAuthorityInstallationDirResolver
	previousInitializationAllowed := expansionAuthorityInitializationAllowed
	previousTestConfigurationActive := expansionAuthorityTestConfigurationActive
	previousBootstrapVerifier := expansionAuthorityBootstrapVerifier
	rootDir = filepath.Clean(rootDir)
	expansionAuthorityRootDirResolver = func() (string, error) { return rootDir, nil }
	expansionAuthorityInstallationDirResolver = func(string) (string, error) { return rootDir + ".installation", nil }
	expansionAuthorityInitializationAllowed = true
	expansionAuthorityTestConfigurationActive = true
	expansionAuthorityBootstrapVerifier = func() error { return nil }
	expansionAuthorityMu.Unlock()
	return func() {
		expansionAuthorityMu.Lock()
		defer expansionAuthorityMu.Unlock()
		expansionAuthorityRootDirResolver = previousRoot
		expansionAuthorityInstallationDirResolver = previousInstallation
		expansionAuthorityInitializationAllowed = previousInitializationAllowed
		expansionAuthorityTestConfigurationActive = previousTestConfigurationActive
		expansionAuthorityBootstrapVerifier = previousBootstrapVerifier
	}, nil
}

type expansionAuthorityRootRecord struct {
	Version    int    `json:"version"`
	KeyID      string `json:"key_id"`
	PrivateKey string `json:"private_key"`
	PublicKey  string `json:"public_key"`
}

type expansionAuthorityProjectRecord struct {
	Version         int    `json:"version"`
	ProjectID       string `json:"project_id"`
	ProjectInstance string `json:"project_instance"`
	KeyID           string `json:"key_id"`
	Epoch           uint64 `json:"epoch"`
	PrivateKey      string `json:"private_key"`
	PublicKey       string `json:"public_key"`
	RevokedBefore   uint64 `json:"revoked_before"`
	CreationID      string `json:"creation_id,omitempty"`
}

type expansionAuthorityRotationJournal struct {
	Version        int                             `json:"version"`
	OutputDir      string                          `json:"output_dir"`
	OldRecord      expansionAuthorityProjectRecord `json:"old_record"`
	NewRecord      expansionAuthorityProjectRecord `json:"new_record"`
	PublicSnapshot publicationAuthoritySnapshot    `json:"public_snapshot"`
	NewTrust       []byte                          `json:"new_trust"`
	NewReceipt     []byte                          `json:"new_receipt"`
}

type expansionAuthorityCreationJournal struct {
	Version         int                             `json:"version"`
	OutputDir       string                          `json:"output_dir"`
	ProjectRootHash string                          `json:"project_root_hash"`
	ProjectID       string                          `json:"project_id"`
	ProjectInstance string                          `json:"project_instance"`
	Before          publicationAuthoritySnapshot    `json:"before"`
	NewRecord       expansionAuthorityProjectRecord `json:"new_record"`
	NewRecordDigest string                          `json:"new_record_digest,omitempty"`
	JournalID       string                          `json:"journal_id,omitempty"`
	State           string                          `json:"state,omitempty"`
	CreatedAtUTC    string                          `json:"created_at_utc,omitempty"`
}

type expansionAuthorityAcceptanceEvidence struct {
	Version               int    `json:"version"`
	Outcome               string `json:"outcome"`
	CreationID            string `json:"creation_id"`
	ProjectRootHash       string `json:"project_root_hash"`
	ProjectID             string `json:"project_id"`
	ProjectInstance       string `json:"project_instance"`
	ProjectKeyID          string `json:"project_key_id"`
	ProjectKeyEpoch       uint64 `json:"project_key_epoch"`
	ProjectPublicKey      string `json:"project_public_key"`
	RecordDigest          string `json:"record_digest"`
	PublicationGeneration uint64 `json:"publication_generation"`
	AcceptedRevisionID    string `json:"accepted_revision_id"`
	ReceiptDigest         string `json:"receipt_digest"`
	CommittedAtUTC        string `json:"committed_at_utc"`
	Signature             string `json:"signature"`
}

func expansionAuthorityRootDir() (string, error) {
	return expansionAuthorityRootDirResolver()
}

func expansionAuthorityInstallationDir(rootDir string) (string, error) {
	return expansionAuthorityInstallationDirResolver(rootDir)
}

func loadOrCreateExpansionAuthorityRoot() (expansionAuthorityRootRecord, ed25519.PrivateKey, error) {
	var root expansionAuthorityRootRecord
	var privateKey ed25519.PrivateKey
	err := withExpansionAuthorityRootLifecycleOperation(expansionAuthorityInitializationAllowed, func(string) error {
		secret, err := loadOrCreateExpansionAuthorityRootSecretLocked()
		if err != nil {
			return err
		}
		privateKey, err = decodeExpansionAuthorityRootSecret(secret)
		if err != nil {
			return err
		}
		root = expansionAuthorityRootRecord{Version: secret.Version, KeyID: secret.KeyID, PublicKey: secret.PublicKey}
		return nil
	})
	return root, privateKey, err
}

func loadExpansionAuthorityRootPublic() (expansionAuthorityRootRecord, ed25519.PublicKey, error) {
	return loadExpansionAuthorityRootPublicKey("")
}

func decodeExpansionAuthorityRoot(record expansionAuthorityRootRecord) (ed25519.PrivateKey, error) {
	privateKey, privateErr := base64.StdEncoding.DecodeString(record.PrivateKey)
	publicKey, publicErr := base64.StdEncoding.DecodeString(record.PublicKey)
	if record.Version != expansionAuthorityRootVersion || !strings.HasPrefix(record.KeyID, "root-") ||
		privateErr != nil || publicErr != nil || len(privateKey) != ed25519.PrivateKeySize || len(publicKey) != ed25519.PublicKeySize ||
		!ed25519.PublicKey(privateKey[32:]).Equal(ed25519.PublicKey(publicKey)) {
		return nil, fmt.Errorf("trusted application publication root is invalid")
	}
	return ed25519.PrivateKey(privateKey), nil
}

func authorityProjectRecordPath(instanceID string) (string, error) {
	rootDir, err := expansionAuthorityRootDir()
	if err != nil {
		return "", err
	}
	if len(instanceID) != 64 {
		return "", fmt.Errorf("publication project instance is invalid")
	}
	return filepath.Join(rootDir, "projects", instanceID+".json"), nil
}

func authorityRotationJournalPath(instanceID string) (string, error) {
	rootDir, err := expansionAuthorityRootDir()
	if err != nil {
		return "", err
	}
	if len(instanceID) != 64 {
		return "", fmt.Errorf("publication project instance is invalid")
	}
	return filepath.Join(rootDir, "rotations", instanceID+".json"), nil
}

func authorityCreationJournalPath(projectRootHash string) (string, error) {
	rootDir, err := expansionAuthorityRootDir()
	if err != nil {
		return "", err
	}
	if len(projectRootHash) != 64 {
		return "", fmt.Errorf("publication project root identity is invalid")
	}
	return filepath.Join(rootDir, "publications", projectRootHash+".json"), nil
}

func readProtectedAuthorityJSON(path string, target any) error {
	if err := recoverProtectedAuthorityReplacement(path); err != nil {
		return err
	}
	payload, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	plaintext, err := unprotectAuthoritySecret(payload)
	if err != nil {
		return fmt.Errorf("unprotect publication authority: %w", err)
	}
	if err := json.Unmarshal(plaintext, target); err != nil {
		return fmt.Errorf("decode publication authority: %w", err)
	}
	return verifyProtectedAuthorityFile(path)
}

func readProtectedAuthorityJSONStrict(path string, target any) error {
	if err := recoverProtectedAuthorityReplacement(path); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("publication authority file is unsafe")
	}
	_, links, err := validationCloneFileIdentity(path, info)
	if err != nil || links != 1 {
		return fmt.Errorf("publication authority file identity is unsafe")
	}
	payload, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	plaintext, err := unprotectAuthoritySecret(payload)
	if err != nil {
		return fmt.Errorf("unprotect publication authority")
	}
	decoder := json.NewDecoder(bytes.NewReader(plaintext))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("decode publication authority")
	}
	if err := decoder.Decode(&struct{}{}); err != stdio.EOF {
		return fmt.Errorf("decode publication authority")
	}
	return verifyProtectedAuthorityFile(path)
}

func writeProtectedAuthorityJSON(path string, value any) error {
	plaintext, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	payload, err := protectAuthoritySecret(plaintext)
	if err != nil {
		return fmt.Errorf("protect publication authority: %w", err)
	}
	return writeRestrictedAtomic(path, payload)
}

func writeRestrictedAtomic(path string, payload []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if err := recoverProtectedAuthorityReplacement(path); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if err := restrictAuthorityFile(tmpPath); err != nil {
		_ = tmp.Close()
		return err
	}
	if expansionAuthorityWriteFault != nil {
		if err := expansionAuthorityWriteFault(path, "after_restrict"); err != nil {
			_ = tmp.Close()
			return err
		}
	}
	if _, err := tmp.Write(payload); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if expansionAuthorityWriteFault != nil {
		if err := expansionAuthorityWriteFault(path, "after_sync"); err != nil {
			return err
		}
	}
	backup := path + ".rotation-backup"
	hadPrevious := false
	if _, err := os.Stat(path); err == nil {
		if err := os.Rename(path, backup); err != nil {
			return err
		}
		hadPrevious = true
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		if hadPrevious {
			_ = os.Rename(backup, path)
		}
		return err
	}
	if err := os.Remove(backup); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := verifyProtectedAuthorityFile(path); err != nil {
		return err
	}
	if expansionAuthorityWriteFault != nil {
		if err := expansionAuthorityWriteFault(path, "after_replace"); err != nil {
			return err
		}
	}
	return nil
}

func recoverProtectedAuthorityReplacement(path string) error {
	backup := path + ".rotation-backup"
	if _, err := os.Stat(backup); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return os.Rename(backup, path)
	} else if err != nil {
		return err
	}
	return os.Remove(backup)
}
