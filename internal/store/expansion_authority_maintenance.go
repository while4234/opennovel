package store

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain"
)

const (
	authorityCreationJournalVersion = 2
	authorityCreationPending        = "pending"
	authorityCreationAccepted       = "accepted"
	authorityMaintenanceLockFile    = "maintenance/transaction.lock"
	maxAuthorityCreationJournals    = 1024
	maxAuthorityProjectRecords      = 16384
	authorityAcceptanceVersion      = 1
	authorityOutcomeAccepted        = "accepted"
	authorityOutcomeAborted         = "aborted"
)

var errAuthorityOutcomeUnproven = errors.New("publication authority outcome is not independently proven")

var authorityOrphanRetention = 24 * time.Hour
var authorityDirectorySyncFault func(string) error
var authorityOutcomeUnlinkFault func(string, string) error
var authorityOutcomeInventoryLimit = maxAuthorityProjectRecords

// SetExpansionAuthorityOrphanRetentionForTesting overrides the process-local
// age gate so integration tests can exercise unavailable-project recovery.
func SetExpansionAuthorityOrphanRetentionForTesting(retention time.Duration) func() {
	previous := authorityOrphanRetention
	authorityOrphanRetention = retention
	return func() { authorityOrphanRetention = previous }
}

func syncAuthorityDirectory(path string) error {
	if authorityDirectorySyncFault != nil {
		if err := authorityDirectorySyncFault(path); err != nil {
			return err
		}
	}
	return syncAuthorityDirectoryPlatform(path)
}

// ExpansionAuthorityMaintenanceReport deliberately contains counts only. It
// is safe to print from an operator CLI without disclosing project paths,
// identifiers, journal names, or protected key material.
type ExpansionAuthorityMaintenanceReport struct {
	Examined  int
	Recovered int
	Finalized int
	Deferred  int
}

func withExpansionAuthorityRootOperation(fn func() error) error {
	return withExpansionAuthorityRootLifecycleOperation(expansionAuthorityInitializationAllowed, func(string) error {
		return fn()
	})
}

// withExpansionAuthorityRootLifecycleOperation is the sole release-root
// maintenance fence. Project-owned callers must already hold their revision
// transaction before entering it; root lifecycle callers must never acquire a
// project lock from the callback. This establishes the one lock order:
// project revision -> release root.
func withExpansionAuthorityRootLifecycleOperation(allowCreate bool, fn func(string) error) error {
	expansionAuthorityMu.Lock()
	defer expansionAuthorityMu.Unlock()
	rootDir, err := expansionAuthorityRootDir()
	if err != nil {
		return err
	}
	if _, err := os.Stat(rootDir); os.IsNotExist(err) && allowCreate {
		if err := prepareExpansionAuthorityRootDir(rootDir); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	if err := verifyExpansionAuthorityRootDir(rootDir); err != nil {
		return err
	}
	return withRevisionFileTransaction(newIO(rootDir), authorityMaintenanceLockFile, func() error {
		return fn(rootDir)
	})
}

func newAuthorityCreationID() (string, error) {
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}

func isLowerHex64(value string) bool {
	if len(value) != 64 || value != strings.ToLower(value) {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func validateAuthorityCreationJournal(journal expansionAuthorityCreationJournal, expectedRootHash string, requireOrphanMetadata bool) (time.Time, error) {
	if journal.Version != 1 && journal.Version != authorityCreationJournalVersion {
		return time.Time{}, fmt.Errorf("publication authority creation journal version is invalid")
	}
	if !isLowerHex64(journal.ProjectRootHash) || journal.ProjectRootHash != expectedRootHash ||
		!isLowerHex64(journal.ProjectInstance) || journal.ProjectInstance != journal.NewRecord.ProjectInstance ||
		journal.ProjectID == "" || journal.ProjectID != journal.NewRecord.ProjectID {
		return time.Time{}, fmt.Errorf("publication authority creation journal identity is invalid")
	}
	if !filepath.IsAbs(journal.OutputDir) || filepath.Clean(journal.OutputDir) != journal.OutputDir {
		return time.Time{}, fmt.Errorf("publication authority creation journal path is invalid")
	}
	if journal.Version == 1 {
		if requireOrphanMetadata {
			return time.Time{}, fmt.Errorf("legacy publication journal requires its project output")
		}
		return time.Time{}, nil
	}
	if !isLowerHex64(journal.JournalID) || journal.NewRecord.CreationID != journal.JournalID ||
		!isLowerHex64(journal.NewRecordDigest) || journal.NewRecordDigest != authorityProjectRecordDigest(journal.NewRecord) ||
		(journal.State != authorityCreationPending && journal.State != authorityCreationAccepted) {
		return time.Time{}, fmt.Errorf("publication authority creation journal state is invalid")
	}
	createdAt, err := time.Parse(time.RFC3339Nano, journal.CreatedAtUTC)
	if err != nil || createdAt.After(time.Now().UTC().Add(time.Minute)) {
		return time.Time{}, fmt.Errorf("publication authority creation journal time is invalid")
	}
	return createdAt, nil
}

func authorityProjectRecordDigest(record expansionAuthorityProjectRecord) string {
	payload, err := json.Marshal(record)
	if err != nil {
		return ""
	}
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:])
}

func authorityAcceptancePath(creationID string) (string, error) {
	if !isLowerHex64(creationID) {
		return "", fmt.Errorf("publication authority acceptance identity is invalid")
	}
	rootDir, err := expansionAuthorityRootDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(rootDir, "acceptances", creationID+".json"), nil
}

func authorityAcceptanceSigningPayload(evidence expansionAuthorityAcceptanceEvidence) ([]byte, error) {
	evidence.Signature = ""
	return json.Marshal(evidence)
}

func writeExpansionAuthorityAcceptanceEvidenceLocked(outputDir string, journal expansionAuthorityCreationJournal) error {
	if !expansionAuthorityCreationCommitted(outputDir, journal) {
		return fmt.Errorf("publication authority creation is not durably committed")
	}
	recordPath, err := authorityProjectRecordPath(journal.ProjectInstance)
	if err != nil {
		return err
	}
	var record expansionAuthorityProjectRecord
	if err := readProtectedAuthorityJSONStrict(recordPath, &record); err != nil {
		return err
	}
	if record != journal.NewRecord || authorityProjectRecordDigest(record) != journal.NewRecordDigest {
		return fmt.Errorf("committed publication authority record changed before acceptance")
	}
	var receipt ExpansionPublicationReceipt
	if err := newIO(outputDir).ReadJSON(expansionPublicationReceiptFile, &receipt); err != nil {
		return err
	}
	receiptPayload, err := json.Marshal(receipt)
	if err != nil {
		return err
	}
	privateKey, err := base64.StdEncoding.DecodeString(record.PrivateKey)
	if err != nil || len(privateKey) != ed25519.PrivateKeySize {
		return fmt.Errorf("publication authority acceptance signing key is invalid")
	}
	evidence := expansionAuthorityAcceptanceEvidence{
		Version: authorityAcceptanceVersion, Outcome: authorityOutcomeAccepted, CreationID: journal.JournalID,
		ProjectRootHash: journal.ProjectRootHash, ProjectID: journal.ProjectID,
		ProjectInstance: journal.ProjectInstance, ProjectKeyID: record.KeyID, ProjectKeyEpoch: record.Epoch,
		ProjectPublicKey: record.PublicKey,
		RecordDigest:     journal.NewRecordDigest, PublicationGeneration: receipt.PublicationGeneration,
		AcceptedRevisionID: receipt.AcceptedRevisionID, ReceiptDigest: domain.ContentSignature(receiptPayload),
		CommittedAtUTC: time.Now().UTC().Format(time.RFC3339Nano),
	}
	payload, err := authorityAcceptanceSigningPayload(evidence)
	if err != nil {
		return err
	}
	evidence.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(ed25519.PrivateKey(privateKey), payload))
	path, err := authorityAcceptancePath(journal.JournalID)
	if err != nil {
		return err
	}
	if _, err := os.Lstat(path); err == nil {
		outcome, verifyErr := expansionAuthorityOutcomeEvidenceLocked(journal, record)
		if verifyErr != nil || outcome != authorityOutcomeAccepted {
			return errors.Join(fmt.Errorf("publication authority acceptance evidence changed"), verifyErr)
		}
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := writeProtectedAuthorityJSON(path, evidence); err != nil {
		return err
	}
	return syncAuthorityDirectory(filepath.Dir(path))
}

func writeExpansionAuthorityAbortEvidenceLocked(journal expansionAuthorityCreationJournal) error {
	record := journal.NewRecord
	privateKey, err := base64.StdEncoding.DecodeString(record.PrivateKey)
	if err != nil || len(privateKey) != ed25519.PrivateKeySize {
		return fmt.Errorf("publication authority abort signing key is invalid")
	}
	evidence := expansionAuthorityAcceptanceEvidence{
		Version: authorityAcceptanceVersion, Outcome: authorityOutcomeAborted, CreationID: journal.JournalID,
		ProjectRootHash: journal.ProjectRootHash, ProjectID: journal.ProjectID,
		ProjectInstance: journal.ProjectInstance, ProjectKeyID: record.KeyID, ProjectKeyEpoch: record.Epoch,
		ProjectPublicKey: record.PublicKey,
		RecordDigest:     journal.NewRecordDigest, CommittedAtUTC: time.Now().UTC().Format(time.RFC3339Nano),
	}
	payload, err := authorityAcceptanceSigningPayload(evidence)
	if err != nil {
		return err
	}
	evidence.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(ed25519.PrivateKey(privateKey), payload))
	path, err := authorityAcceptancePath(journal.JournalID)
	if err != nil {
		return err
	}
	if _, err := os.Lstat(path); err == nil {
		outcome, verifyErr := expansionAuthorityOutcomeEvidenceLocked(journal, record)
		if verifyErr != nil || outcome != authorityOutcomeAborted {
			return errors.Join(fmt.Errorf("publication authority outcome evidence changed"), verifyErr)
		}
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := writeProtectedAuthorityJSON(path, evidence); err != nil {
		return err
	}
	return syncAuthorityDirectory(filepath.Dir(path))
}

func expansionAuthorityOutcomeEvidenceLocked(journal expansionAuthorityCreationJournal, record expansionAuthorityProjectRecord) (string, error) {
	path, err := authorityAcceptancePath(journal.JournalID)
	if err != nil {
		return "", err
	}
	var evidence expansionAuthorityAcceptanceEvidence
	if err := readProtectedAuthorityJSONStrict(path, &evidence); err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	if err := validateExpansionAuthorityOutcomeEvidence(evidence); err != nil {
		return "", err
	}
	valid := evidence.CreationID == journal.JournalID &&
		evidence.ProjectRootHash == journal.ProjectRootHash && evidence.ProjectID == journal.ProjectID &&
		evidence.ProjectInstance == journal.ProjectInstance && evidence.ProjectKeyID == record.KeyID &&
		evidence.ProjectKeyEpoch == record.Epoch && evidence.ProjectPublicKey == record.PublicKey &&
		evidence.RecordDigest == journal.NewRecordDigest
	if !valid {
		return "", fmt.Errorf("publication authority outcome evidence is invalid")
	}
	return evidence.Outcome, nil
}

func validateExpansionAuthorityOutcomeEvidence(evidence expansionAuthorityAcceptanceEvidence) error {
	committedAt, timeErr := time.Parse(time.RFC3339Nano, evidence.CommittedAtUTC)
	publicKey, keyErr := base64.StdEncoding.DecodeString(evidence.ProjectPublicKey)
	signature, signatureErr := base64.StdEncoding.DecodeString(evidence.Signature)
	payload, payloadErr := authorityAcceptanceSigningPayload(evidence)
	valid := evidence.Version == authorityAcceptanceVersion &&
		(evidence.Outcome == authorityOutcomeAccepted || evidence.Outcome == authorityOutcomeAborted) &&
		isLowerHex64(evidence.CreationID) && isLowerHex64(evidence.ProjectRootHash) && evidence.ProjectID != "" &&
		isLowerHex64(evidence.ProjectInstance) && evidence.ProjectKeyID != "" && evidence.ProjectKeyEpoch > 0 &&
		isLowerHex64(evidence.RecordDigest) && timeErr == nil && !committedAt.After(time.Now().UTC().Add(time.Minute)) &&
		keyErr == nil && signatureErr == nil && payloadErr == nil && len(publicKey) == ed25519.PublicKeySize &&
		len(signature) == ed25519.SignatureSize && ed25519.Verify(ed25519.PublicKey(publicKey), payload, signature)
	if !valid {
		return fmt.Errorf("publication authority outcome evidence is invalid")
	}
	if evidence.Outcome == authorityOutcomeAccepted &&
		(evidence.PublicationGeneration == 0 || evidence.AcceptedRevisionID == "" || !isLowerHex64(evidence.ReceiptDigest)) {
		return fmt.Errorf("publication authority acceptance evidence is incomplete")
	}
	if evidence.Outcome == authorityOutcomeAborted &&
		(evidence.PublicationGeneration != 0 || evidence.AcceptedRevisionID != "" || evidence.ReceiptDigest != "") {
		return fmt.Errorf("publication authority abort evidence is inconsistent")
	}
	return nil
}

// ReconcileExpansionAuthorityOrphans is the bounded operator/startup
// maintenance boundary for release-managed creation journals.
func ReconcileExpansionAuthorityOrphans() (ExpansionAuthorityMaintenanceReport, error) {
	var report ExpansionAuthorityMaintenanceReport
	rootDir, err := expansionAuthorityRootDir()
	if err != nil {
		return report, err
	}
	if _, err := os.Stat(rootDir); os.IsNotExist(err) {
		return report, nil
	} else if err != nil {
		return report, err
	}
	var names []string
	err = withExpansionAuthorityRootOperation(func() error {
		publicationsDir := filepath.Join(rootDir, "publications")
		if err := verifyAuthorityInventoryDirectory(rootDir, publicationsDir, true); err != nil {
			return err
		}
		entries, err := os.ReadDir(publicationsDir)
		if os.IsNotExist(err) {
			entries = nil
			err = nil
		}
		if err != nil {
			return err
		}
		if len(entries) > maxAuthorityCreationJournals {
			return fmt.Errorf("publication authority journal inventory exceeds its safety bound")
		}
		for _, entry := range entries {
			name := entry.Name()
			if entry.IsDir() || filepath.Ext(name) != ".json" || !isLowerHex64(strings.TrimSuffix(name, ".json")) {
				return fmt.Errorf("publication authority journal inventory contains an unknown entry")
			}
			names = append(names, name)
		}
		journalsByCreation := make(map[string]expansionAuthorityCreationJournal, len(names))
		for _, name := range names {
			rootHash := strings.TrimSuffix(name, ".json")
			var journal expansionAuthorityCreationJournal
			if err := readProtectedAuthorityJSONStrict(filepath.Join(publicationsDir, name), &journal); err != nil {
				return err
			}
			if _, err := validateAuthorityCreationJournal(journal, rootHash, false); err != nil {
				return err
			}
			if journal.Version == authorityCreationJournalVersion {
				if _, exists := journalsByCreation[journal.JournalID]; exists {
					return fmt.Errorf("publication authority journal inventory contains a duplicate creation identity")
				}
				journalsByCreation[journal.JournalID] = journal
			}
		}
		projectsDir := filepath.Join(rootDir, "projects")
		if err := verifyAuthorityInventoryDirectory(rootDir, projectsDir, true); err != nil {
			return err
		}
		projects, err := os.ReadDir(projectsDir)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		if len(projects) > maxAuthorityProjectRecords {
			return fmt.Errorf("publication authority registry exceeds its safety bound")
		}
		for _, entry := range projects {
			name := entry.Name()
			instanceID := strings.TrimSuffix(name, ".json")
			if entry.IsDir() || filepath.Ext(name) != ".json" || !isLowerHex64(instanceID) {
				return fmt.Errorf("publication authority registry contains an unknown entry")
			}
			var record expansionAuthorityProjectRecord
			if err := readProtectedAuthorityJSONStrict(filepath.Join(projectsDir, name), &record); err != nil {
				return err
			}
			if record.Version != expansionAuthorityRootVersion || record.ProjectInstance != instanceID || record.ProjectID == "" {
				return fmt.Errorf("publication authority registry identity is invalid")
			}
		}
		acceptancesDir := filepath.Join(rootDir, "acceptances")
		if err := verifyAuthorityInventoryDirectory(rootDir, acceptancesDir, true); err != nil {
			return err
		}
		acceptances, err := os.ReadDir(acceptancesDir)
		acceptancesDirExists := err == nil
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		retainedOutcomes := 0
		for _, entry := range acceptances {
			name := entry.Name()
			creationID := strings.TrimSuffix(name, ".json")
			if entry.IsDir() || filepath.Ext(name) != ".json" || !isLowerHex64(creationID) {
				return fmt.Errorf("publication authority outcome inventory contains an unknown entry")
			}
			var evidence expansionAuthorityAcceptanceEvidence
			if err := readProtectedAuthorityJSONStrict(filepath.Join(acceptancesDir, name), &evidence); err != nil {
				return err
			}
			if evidence.CreationID != creationID {
				return fmt.Errorf("publication authority outcome inventory identity is invalid")
			}
			if err := validateExpansionAuthorityOutcomeEvidence(evidence); err != nil {
				return err
			}
			if journal, live := journalsByCreation[creationID]; live {
				if _, err := expansionAuthorityOutcomeEvidenceLocked(journal, journal.NewRecord); err != nil {
					return err
				}
				retainedOutcomes++
				continue
			}
			if err := reclaimTerminalExpansionAuthorityOutcomeLocked(evidence); err != nil {
				return err
			}
		}
		// A previous unlink may have completed while its directory sync failed.
		// Sync even when the inventory is now empty so a retry can durably finish
		// that terminal transition without relying on the deleted file as a marker.
		if acceptancesDirExists {
			if err := syncAuthorityDirectory(acceptancesDir); err != nil {
				return err
			}
		}
		if err := enforceAuthorityOutcomeInventoryBound(retainedOutcomes); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return report, err
	}
	for _, name := range names {
		report.Examined++
		rootHash := strings.TrimSuffix(name, ".json")
		path := filepath.Join(rootDir, "publications", name)
		var journal expansionAuthorityCreationJournal
		if err := withExpansionAuthorityRootOperation(func() error {
			return readProtectedAuthorityJSONStrict(path, &journal)
		}); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return report, err
		}
		if _, err := validateAuthorityCreationJournal(journal, rootHash, false); err != nil {
			return report, err
		}
		if info, statErr := os.Stat(journal.OutputDir); statErr == nil && info.IsDir() {
			revisions := NewRevisionStore(journal.OutputDir)
			if err := revisions.withRevisionTransaction(func() error {
				return withExpansionAuthorityRootOperation(func() error {
					return recoverExpansionAuthorityCreationForOutputLocked(journal.OutputDir)
				})
			}); err != nil {
				return report, err
			}
			report.Recovered++
			continue
		} else if statErr != nil && !os.IsNotExist(statErr) {
			return report, fmt.Errorf("inspect publication output for authority recovery: %w", statErr)
		}
		createdAt, err := validateAuthorityCreationJournal(journal, rootHash, true)
		if err != nil {
			return report, err
		}
		if time.Since(createdAt) < authorityOrphanRetention {
			report.Deferred++
			continue
		}
		acceptedEvidence := false
		if err := withExpansionAuthorityRootOperation(func() error {
			if journal.State == authorityCreationPending {
				recordPath, pathErr := authorityProjectRecordPath(journal.ProjectInstance)
				if pathErr != nil {
					return pathErr
				}
				var record expansionAuthorityProjectRecord
				if readErr := readProtectedAuthorityJSONStrict(recordPath, &record); readErr == nil {
					outcome, evidenceErr := expansionAuthorityOutcomeEvidenceLocked(journal, record)
					if evidenceErr != nil {
						return evidenceErr
					}
					acceptedEvidence = outcome == authorityOutcomeAccepted
				} else if !os.IsNotExist(readErr) {
					return readErr
				}
			}
			return reconcileUnavailableAuthorityCreationJournal(path, journal)
		}); err != nil {
			if errors.Is(err, errAuthorityOutcomeUnproven) {
				report.Deferred++
				continue
			}
			return report, err
		}
		if journal.State == authorityCreationAccepted || acceptedEvidence {
			report.Finalized++
		} else {
			report.Recovered++
		}
	}
	return report, nil
}

func verifyAuthorityInventoryDirectory(rootDir, path string, allowMissing bool) error {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) && allowMissing {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("publication authority inventory directory is unsafe")
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return err
	}
	resolvedRoot, err := filepath.EvalSymlinks(rootDir)
	if err != nil {
		return err
	}
	relative, err := filepath.Rel(filepath.Clean(resolvedRoot), filepath.Clean(resolved))
	if err != nil || relative != filepath.Base(path) {
		return fmt.Errorf("publication authority inventory directory escapes its root")
	}
	return nil
}

func reconcileUnavailableAuthorityCreationJournal(journalPath string, journal expansionAuthorityCreationJournal) error {
	var current expansionAuthorityCreationJournal
	if err := readProtectedAuthorityJSONStrict(journalPath, &current); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if !reflect.DeepEqual(current, journal) {
		return fmt.Errorf("publication authority journal changed during maintenance")
	}
	recordPath, err := authorityProjectRecordPath(journal.ProjectInstance)
	if err != nil {
		return err
	}
	var record expansionAuthorityProjectRecord
	if err := readProtectedAuthorityJSONStrict(recordPath, &record); err != nil {
		if os.IsNotExist(err) && journal.State == authorityCreationPending {
			outcome, evidenceErr := expansionAuthorityOutcomeEvidenceLocked(journal, journal.NewRecord)
			if evidenceErr != nil {
				return evidenceErr
			}
			if outcome == authorityOutcomeAccepted {
				return fmt.Errorf("committed authority acceptance exists without its private record")
			}
			if outcome == "" {
				return errAuthorityOutcomeUnproven
			}
			if syncErr := syncAuthorityDirectory(filepath.Dir(recordPath)); syncErr != nil {
				return syncErr
			}
			return removeAuthorityJournalAndOutcomeDurably(journalPath, journal)
		}
		return err
	}
	if record != journal.NewRecord {
		return fmt.Errorf("publication authority record changed during maintenance")
	}
	if journal.State == authorityCreationPending {
		outcome, err := expansionAuthorityOutcomeEvidenceLocked(journal, record)
		if err != nil {
			return err
		}
		if outcome == authorityOutcomeAccepted {
			return removeAuthorityJournalAndOutcomeDurably(journalPath, journal)
		}
		if outcome == "" {
			return errAuthorityOutcomeUnproven
		}
		if err := os.Remove(recordPath); err != nil {
			return err
		}
		// The journal is the redo evidence for the private-record unlink. Never
		// remove it until the projects directory has durably recorded deletion.
		if err := syncAuthorityDirectory(filepath.Dir(recordPath)); err != nil {
			return err
		}
	}
	return removeAuthorityJournalAndOutcomeDurably(journalPath, journal)
}

// reclaimTerminalExpansionAuthorityOutcomeLocked only reclaims evidence after
// its creation journal is absent. Accepted evidence additionally requires the
// exact committed private record; aborted evidence requires that the exact
// uncommitted record is absent. A record from a later creation is an ABA-safe
// terminal state because CreationID and the full protected-record digest differ.
func reclaimTerminalExpansionAuthorityOutcomeLocked(evidence expansionAuthorityAcceptanceEvidence) error {
	recordPath, err := authorityProjectRecordPath(evidence.ProjectInstance)
	if err != nil {
		return err
	}
	var record expansionAuthorityProjectRecord
	readErr := readProtectedAuthorityJSONStrict(recordPath, &record)
	switch evidence.Outcome {
	case authorityOutcomeAccepted:
		if readErr != nil {
			return fmt.Errorf("accepted publication authority outcome lost its committed record: %w", readErr)
		}
		if record.CreationID == evidence.CreationID &&
			(authorityProjectRecordDigest(record) != evidence.RecordDigest || record.ProjectID != evidence.ProjectID ||
				record.ProjectInstance != evidence.ProjectInstance || record.KeyID != evidence.ProjectKeyID ||
				record.Epoch != evidence.ProjectKeyEpoch || record.PublicKey != evidence.ProjectPublicKey) {
			return fmt.Errorf("accepted publication authority outcome record binding changed")
		}
	case authorityOutcomeAborted:
		if readErr == nil && record.CreationID == evidence.CreationID {
			return fmt.Errorf("aborted publication authority outcome still owns a private record")
		}
		if readErr != nil && !os.IsNotExist(readErr) {
			return readErr
		}
	default:
		return fmt.Errorf("publication authority outcome is invalid")
	}
	// The absent journal may be the crash-visible side of an unlink whose
	// parent-directory sync previously failed. Re-establish that durability
	// barrier before removing the only remaining terminal outcome evidence.
	if err := syncAuthorityDirectory(filepath.Join(filepath.Dir(filepath.Dir(recordPath)), "publications")); err != nil {
		return err
	}
	return removeAuthorityOutcomeEvidenceDurably(evidence.CreationID)
}

func enforceAuthorityOutcomeInventoryBound(count int) error {
	if count > authorityOutcomeInventoryLimit {
		return fmt.Errorf("publication authority outcome inventory exceeds its safety bound")
	}
	return nil
}

func removeAuthorityJournalAndOutcomeDurably(journalPath string, journal expansionAuthorityCreationJournal) error {
	if err := removeAuthorityJournalDurably(journalPath); err != nil {
		return err
	}
	if journal.Version != authorityCreationJournalVersion {
		return nil
	}
	return removeAuthorityOutcomeEvidenceDurably(journal.JournalID)
}

func removeAuthorityOutcomeEvidenceDurably(creationID string) error {
	path, err := authorityAcceptancePath(creationID)
	if err != nil {
		return err
	}
	if authorityOutcomeUnlinkFault != nil {
		if err := authorityOutcomeUnlinkFault(path, "before_unlink"); err != nil {
			return err
		}
	}
	if err := os.Remove(path); err != nil {
		if !os.IsNotExist(err) {
			return err
		}
	} else if authorityOutcomeUnlinkFault != nil {
		if err := authorityOutcomeUnlinkFault(path, "after_unlink"); err != nil {
			return err
		}
	}
	return syncAuthorityDirectory(filepath.Dir(path))
}

func removeAuthorityJournalDurably(path string) error {
	if err := os.Remove(path); err != nil {
		if !os.IsNotExist(err) {
			return err
		}
		if _, parentErr := os.Stat(filepath.Dir(path)); parentErr != nil {
			if os.IsNotExist(parentErr) {
				return nil
			}
			return parentErr
		}
	}
	// ENOENT proves only the current directory entry is absent. It does not
	// prove a prior unlink survived its failed directory sync, so every retry
	// must durably confirm the publications directory before advancing.
	return syncAuthorityDirectory(filepath.Dir(path))
}
