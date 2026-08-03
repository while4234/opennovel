package store

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func authorityMaintenanceFixture(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "authority")
	restore, err := ConfigureExpansionAuthorityForTestProcess(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(restore)
	if _, err := InitializeExpansionAuthorityRoot(); err != nil {
		t.Fatal(err)
	}
	previousRetention := authorityOrphanRetention
	authorityOrphanRetention = 0
	t.Cleanup(func() { authorityOrphanRetention = previousRetention })
	return root
}

func writeAuthorityCreationFixture(t *testing.T, outputDir, state string) (expansionAuthorityCreationJournal, string, string) {
	t.Helper()
	rootHash, err := expansionPublicationRootHash(outputDir)
	if err != nil {
		t.Fatal(err)
	}
	journalID, err := newAuthorityCreationID()
	if err != nil {
		t.Fatal(err)
	}
	instanceID, err := newAuthorityCreationID()
	if err != nil {
		t.Fatal(err)
	}
	canonicalOutput, err := canonicalPublicationPath(outputDir)
	if err != nil {
		t.Fatal(err)
	}
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	record := expansionAuthorityProjectRecord{
		Version: expansionAuthorityRootVersion, ProjectID: "test-project", ProjectInstance: instanceID,
		KeyID: "project-test", Epoch: 1, PrivateKey: base64.StdEncoding.EncodeToString(privateKey), PublicKey: base64.StdEncoding.EncodeToString(publicKey), CreationID: journalID,
	}
	journal := expansionAuthorityCreationJournal{
		Version: authorityCreationJournalVersion, OutputDir: canonicalOutput, ProjectRootHash: rootHash,
		ProjectID: record.ProjectID, ProjectInstance: instanceID, NewRecord: record,
		NewRecordDigest: authorityProjectRecordDigest(record),
		JournalID:       journalID, State: state, CreatedAtUTC: time.Now().UTC().Add(-time.Hour).Format(time.RFC3339Nano),
	}
	journalPath, _ := authorityCreationJournalPath(rootHash)
	recordPath, _ := authorityProjectRecordPath(instanceID)
	if err := writeProtectedAuthorityJSON(journalPath, journal); err != nil {
		t.Fatal(err)
	}
	if err := writeProtectedAuthorityJSON(recordPath, record); err != nil {
		t.Fatal(err)
	}
	if state == authorityCreationPending {
		if err := writeExpansionAuthorityAbortEvidenceLocked(journal); err != nil {
			t.Fatal(err)
		}
	}
	return journal, journalPath, recordPath
}

func TestConcurrentNewStoreWaitsForLivePublicationTransaction(t *testing.T) {
	authorityMaintenanceFixture(t)
	output := filepath.Join(t.TempDir(), "project", "output")
	if err := os.MkdirAll(output, 0o755); err != nil {
		t.Fatal(err)
	}
	_, journalPath, recordPath := writeAuthorityCreationFixture(t, output, authorityCreationPending)
	revisions := NewRevisionStore(output)
	owned := make(chan struct{})
	release := make(chan struct{})
	ownerDone := make(chan error, 1)
	go func() {
		ownerDone <- revisions.withRevisionTransaction(func() error {
			close(owned)
			<-release
			return withExpansionAuthorityRootOperation(func() error {
				return removeAuthorityJournalDurably(journalPath)
			})
		})
	}()
	<-owned
	competitorDone := make(chan *Store, 1)
	go func() { competitorDone <- NewStore(output) }()
	select {
	case <-competitorDone:
		t.Fatal("concurrent NewStore bypassed the live project transaction")
	case <-time.After(150 * time.Millisecond):
	}
	if _, err := os.Stat(recordPath); err != nil {
		t.Fatalf("live authority record was rolled back: %v", err)
	}
	close(release)
	if err := <-ownerDone; err != nil {
		t.Fatal(err)
	}
	select {
	case reopened := <-competitorDone:
		if err := reopened.RecoverStructureMigration(); err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("concurrent NewStore deadlocked after publication completed")
	}
	if _, err := os.Stat(recordPath); err != nil {
		t.Fatalf("completed live authority record was removed: %v", err)
	}
}

func TestAuthorityTransactionProcessHelper(t *testing.T) {
	if os.Getenv("AINOVEL_AUTHORITY_LOCK_HELPER") != "1" {
		t.Skip("helper process only")
	}
	restore, err := ConfigureExpansionAuthorityForTestProcess(os.Getenv("AINOVEL_AUTHORITY_ROOT"))
	if err != nil {
		t.Fatal(err)
	}
	defer restore()
	output := os.Getenv("AINOVEL_AUTHORITY_OUTPUT")
	ready := os.Getenv("AINOVEL_AUTHORITY_READY")
	commit := os.Getenv("AINOVEL_AUTHORITY_COMMIT")
	journalPath := os.Getenv("AINOVEL_AUTHORITY_JOURNAL")
	err = NewRevisionStore(output).withRevisionTransaction(func() error {
		if err := os.WriteFile(ready, []byte("ready"), 0o600); err != nil {
			return err
		}
		deadline := time.Now().Add(10 * time.Second)
		for {
			if _, err := os.Stat(commit); err == nil {
				return withExpansionAuthorityRootOperation(func() error {
					return removeAuthorityJournalDurably(journalPath)
				})
			}
			if time.Now().After(deadline) {
				return errors.New("helper timed out waiting for commit")
			}
			time.Sleep(10 * time.Millisecond)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestAuthorityMaintenanceProcessHelper(t *testing.T) {
	if os.Getenv("AINOVEL_AUTHORITY_GC_HELPER") != "1" {
		t.Skip("helper process only")
	}
	restore, err := ConfigureExpansionAuthorityForTestProcess(os.Getenv("AINOVEL_AUTHORITY_ROOT"))
	if err != nil {
		t.Fatal(err)
	}
	defer restore()
	authorityOrphanRetention = 0
	if _, err := ReconcileExpansionAuthorityOrphans(); err != nil {
		t.Fatal(err)
	}
}

func TestAuthorityRootLifecycleProcessHelper(t *testing.T) {
	if os.Getenv("AINOVEL_AUTHORITY_ROOT_LIFECYCLE_HELPER") != "1" {
		t.Skip("helper process only")
	}
	restore, err := ConfigureExpansionAuthorityForTestProcess(os.Getenv("AINOVEL_AUTHORITY_ROOT"))
	if err != nil {
		t.Fatal(err)
	}
	defer restore()
	ready, release := os.Getenv("AINOVEL_AUTHORITY_READY"), os.Getenv("AINOVEL_AUTHORITY_COMMIT")
	expansionAuthorityWriteFault = func(path, stage string) error {
		if filepath.Base(path) != rootSigningSecretName || stage != "after_sync" {
			return nil
		}
		if err := os.WriteFile(ready, []byte("ready"), 0o600); err != nil {
			return err
		}
		deadline := time.Now().Add(10 * time.Second)
		for {
			if _, err := os.Stat(release); err == nil {
				return nil
			}
			if time.Now().After(deadline) {
				return errors.New("root lifecycle helper timed out")
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	if err := RotateExpansionAuthorityRoot(); err != nil {
		t.Fatal(err)
	}
}

func TestAuthorityPublicVerificationProcessHelper(t *testing.T) {
	if os.Getenv("AINOVEL_AUTHORITY_VERIFY_HELPER") != "1" {
		t.Skip("helper process only")
	}
	restore, err := ConfigureExpansionAuthorityForTestProcess(os.Getenv("AINOVEL_AUTHORITY_ROOT"))
	if err != nil {
		t.Fatal(err)
	}
	defer restore()
	var trust ExpansionPublicationTrust
	data, err := os.ReadFile(os.Getenv("AINOVEL_AUTHORITY_TRUST"))
	if err != nil || json.Unmarshal(data, &trust) != nil {
		t.Fatalf("load verification trust: %v", err)
	}
	ready, release := os.Getenv("AINOVEL_AUTHORITY_READY"), os.Getenv("AINOVEL_AUTHORITY_COMMIT")
	expansionAuthorityWriteFault = func(path, stage string) error {
		if filepath.Base(path) != authorityCheckpointName || stage != "after_sync" {
			return nil
		}
		if err := os.WriteFile(ready, []byte("ready"), 0o600); err != nil {
			return err
		}
		deadline := time.Now().Add(10 * time.Second)
		for {
			if _, err := os.Stat(release); err == nil {
				if os.Getenv("AINOVEL_AUTHORITY_VERIFY_FAIL") == "1" {
					return errors.New("injected public verification checkpoint failure")
				}
				return nil
			}
			if time.Now().After(deadline) {
				return errors.New("public verification helper timed out")
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	if err := VerifyExpansionPublicationCertificate(trust); err != nil {
		t.Fatal(err)
	}
}

func TestCrossProcessPublicVerificationCheckpointNeverRegresses(t *testing.T) {
	root := authorityMaintenanceFixture(t)
	installation, err := expansionAuthorityInstallationDir(root)
	if err != nil {
		t.Fatal(err)
	}
	oldTrust := signedExpansionAuthorityTrust(t)
	oldCheckpoint, err := os.ReadFile(filepath.Join(installation, authorityCheckpointName))
	if err != nil {
		t.Fatal(err)
	}
	if err := RotateExpansionAuthorityRoot(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(installation, authorityCheckpointName), oldCheckpoint, 0o644); err != nil {
		t.Fatal(err)
	}
	temp := t.TempDir()
	trustPath, ready, release := filepath.Join(temp, "trust.json"), filepath.Join(temp, "ready"), filepath.Join(temp, "release")
	trustData, _ := json.Marshal(oldTrust)
	if err := os.WriteFile(trustPath, trustData, 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestAuthorityPublicVerificationProcessHelper$")
	cmd.Env = append(os.Environ(),
		"AINOVEL_AUTHORITY_VERIFY_HELPER=1", "AINOVEL_AUTHORITY_ROOT="+root,
		"AINOVEL_AUTHORITY_TRUST="+trustPath, "AINOVEL_AUTHORITY_READY="+ready, "AINOVEL_AUTHORITY_COMMIT="+release,
	)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	verifyDone := make(chan error, 1)
	go func() { verifyDone <- cmd.Wait() }()
	waitForAuthorityTestFile(t, ready)
	rotateDone := make(chan error, 1)
	go func() { rotateDone <- RotateExpansionAuthorityRoot() }()
	select {
	case err := <-rotateDone:
		t.Fatalf("rotation bypassed public verification checkpoint fence: %v", err)
	case <-time.After(150 * time.Millisecond):
	}
	if err := os.WriteFile(release, []byte("release"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := <-verifyDone; err != nil {
		t.Fatal(err)
	}
	if err := <-rotateDone; err != nil {
		t.Fatal(err)
	}
	checkpointData, err := os.ReadFile(filepath.Join(installation, authorityCheckpointName))
	if err != nil {
		t.Fatal(err)
	}
	var checkpoint expansionAuthorityCheckpoint
	if err := decodeExactJSON(checkpointData, &checkpoint); err != nil {
		t.Fatal(err)
	}
	if checkpoint.Generation != 3 || checkpoint.CurrentEpoch != 3 {
		t.Fatalf("checkpoint regressed after verifier/rotation interleave: %+v", checkpoint)
	}
	if err := VerifyExpansionPublicationCertificate(oldTrust); err == nil {
		t.Fatal("two rotations did not expire the old public certificate")
	}
}

func TestCrossProcessPublicVerificationFencesRevokeImportAndRecovery(t *testing.T) {
	t.Run("revoke", func(t *testing.T) {
		root := authorityMaintenanceFixture(t)
		installation, err := expansionAuthorityInstallationDir(root)
		if err != nil {
			t.Fatal(err)
		}
		oldTrust := signedExpansionAuthorityTrust(t)
		oldCheckpoint, err := os.ReadFile(filepath.Join(installation, authorityCheckpointName))
		if err != nil {
			t.Fatal(err)
		}
		if err := RotateExpansionAuthorityRoot(); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(installation, authorityCheckpointName), oldCheckpoint, 0o644); err != nil {
			t.Fatal(err)
		}
		release, verifyDone := startPausedAuthorityVerifier(t, root, oldTrust, false)
		mutationDone := make(chan error, 1)
		go func() { mutationDone <- RevokeExpansionAuthorityRootKey(oldTrust.RootKeyID) }()
		assertAuthorityMutationBlocked(t, mutationDone, "revocation")
		if err := os.WriteFile(release, []byte("release"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := <-verifyDone; err != nil {
			t.Fatal(err)
		}
		if err := <-mutationDone; err != nil {
			t.Fatal(err)
		}
		assertAuthorityCheckpoint(t, installation, 3, 2)
		if err := VerifyExpansionPublicationCertificate(oldTrust); err == nil {
			t.Fatal("revoked certificate remained valid after verifier interleave")
		}
	})

	t.Run("import after verifier fault and restart", func(t *testing.T) {
		root := authorityMaintenanceFixture(t)
		installation, err := expansionAuthorityInstallationDir(root)
		if err != nil {
			t.Fatal(err)
		}
		oldTrust := signedExpansionAuthorityTrust(t)
		oldCheckpoint, err := os.ReadFile(filepath.Join(installation, authorityCheckpointName))
		if err != nil {
			t.Fatal(err)
		}
		if err := RotateExpansionAuthorityRoot(); err != nil {
			t.Fatal(err)
		}
		wrappingKey := []byte("0123456789abcdef0123456789abcdef")
		bundle := filepath.Join(t.TempDir(), "authority-import.json")
		if err := ExportExpansionAuthorityRoot(bundle, wrappingKey); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(installation, authorityCheckpointName), oldCheckpoint, 0o644); err != nil {
			t.Fatal(err)
		}
		release, verifyDone := startPausedAuthorityVerifier(t, root, oldTrust, true)
		mutationDone := make(chan error, 1)
		go func() { mutationDone <- ImportExpansionAuthorityRoot(bundle, wrappingKey) }()
		assertAuthorityMutationBlocked(t, mutationDone, "import")
		if err := os.WriteFile(release, []byte("release"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := <-verifyDone; err == nil {
			t.Fatal("verification checkpoint fault did not terminate the child")
		}
		if err := <-mutationDone; err != nil {
			t.Fatal(err)
		}
		assertAuthorityCheckpoint(t, installation, 2, 2)
		if err := VerifyExpansionPublicationCertificate(oldTrust); err != nil {
			t.Fatalf("verification restart rejected grace certificate after import: %v", err)
		}
	})

	t.Run("recovery after verifier fault and restart", func(t *testing.T) {
		root := authorityMaintenanceFixture(t)
		installation, err := expansionAuthorityInstallationDir(root)
		if err != nil {
			t.Fatal(err)
		}
		oldTrust := signedExpansionAuthorityTrust(t)
		oldSecretBytes, _ := os.ReadFile(filepath.Join(root, rootSigningSecretName))
		oldAnchorBytes, _ := os.ReadFile(filepath.Join(root, rootPublicAnchorName))
		oldKeyringBytes, _ := os.ReadFile(filepath.Join(root, rootPublicKeyringName))
		oldPinBytes, _ := os.ReadFile(filepath.Join(installation, authorityTrustPinName))
		oldCheckpointBytes, _ := os.ReadFile(filepath.Join(installation, authorityCheckpointName))
		if err := RotateExpansionAuthorityRoot(); err != nil {
			t.Fatal(err)
		}
		newSecretBytes, _ := os.ReadFile(filepath.Join(root, rootSigningSecretName))
		newAnchorBytes, _ := os.ReadFile(filepath.Join(root, rootPublicAnchorName))
		newKeyringBytes, _ := os.ReadFile(filepath.Join(root, rootPublicKeyringName))
		newPinBytes, _ := os.ReadFile(filepath.Join(installation, authorityTrustPinName))
		newCheckpointBytes, _ := os.ReadFile(filepath.Join(installation, authorityCheckpointName))
		var newSecret expansionAuthorityRootSecret
		if err := readProtectedAuthorityJSON(filepath.Join(root, rootSigningSecretName), &newSecret); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(installation, authorityCheckpointName), oldCheckpointBytes, 0o644); err != nil {
			t.Fatal(err)
		}
		release, verifyDone := startPausedAuthorityVerifier(t, root, oldTrust, true)
		journal := expansionAuthorityRootRotationJournal{
			Version: expansionAuthorityRootLifecycleVersion, Operation: "rotate",
			OldSecret: oldSecretBytes, OldAnchor: oldAnchorBytes, OldKeyring: oldKeyringBytes,
			NewSecret: newSecretBytes, NewAnchor: newAnchorBytes, NewKeyring: newKeyringBytes,
			OldPin: oldPinBytes, OldCheckpoint: oldCheckpointBytes, NewPin: newPinBytes, NewCheckpoint: newCheckpointBytes,
			NewKeyID: newSecret.KeyID, NewEpoch: newSecret.Epoch, CreatedAtUTC: time.Now().UTC().Format(time.RFC3339Nano),
		}
		journalPath := filepath.Join(root, rootRotationJournalName)
		if err := writeProtectedAuthorityJSON(journalPath, journal); err != nil {
			t.Fatal(err)
		}
		mutationDone := make(chan error, 1)
		go func() { mutationDone <- recoverExpansionAuthorityRootLifecycle() }()
		assertAuthorityMutationBlocked(t, mutationDone, "recovery")
		if err := os.WriteFile(release, []byte("release"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := <-verifyDone; err == nil {
			t.Fatal("verification checkpoint fault did not terminate the child")
		}
		if err := <-mutationDone; err != nil {
			t.Fatal(err)
		}
		assertAuthorityCheckpoint(t, installation, 2, 2)
		if _, err := os.Stat(journalPath); !os.IsNotExist(err) {
			t.Fatalf("recovery retained root transaction journal: %v", err)
		}
		if err := VerifyExpansionPublicationCertificate(oldTrust); err != nil {
			t.Fatalf("verification restart rejected recovered grace certificate: %v", err)
		}
	})
}

func startPausedAuthorityVerifier(t *testing.T, root string, trust ExpansionPublicationTrust, fail bool) (string, <-chan error) {
	t.Helper()
	temp := t.TempDir()
	trustPath := filepath.Join(temp, "trust.json")
	ready := filepath.Join(temp, "ready")
	release := filepath.Join(temp, "release")
	trustData, err := json.Marshal(trust)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(trustPath, trustData, 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestAuthorityPublicVerificationProcessHelper$")
	cmd.Env = append(os.Environ(),
		"AINOVEL_AUTHORITY_VERIFY_HELPER=1", "AINOVEL_AUTHORITY_ROOT="+root,
		"AINOVEL_AUTHORITY_TRUST="+trustPath, "AINOVEL_AUTHORITY_READY="+ready, "AINOVEL_AUTHORITY_COMMIT="+release,
	)
	if fail {
		cmd.Env = append(cmd.Env, "AINOVEL_AUTHORITY_VERIFY_FAIL=1")
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	waitForAuthorityTestFile(t, ready)
	return release, done
}

func assertAuthorityMutationBlocked(t *testing.T, done <-chan error, operation string) {
	t.Helper()
	select {
	case err := <-done:
		t.Fatalf("%s bypassed public verification checkpoint fence: %v", operation, err)
	case <-time.After(150 * time.Millisecond):
	}
}

func assertAuthorityCheckpoint(t *testing.T, installation string, generation, epoch uint64) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(installation, authorityCheckpointName))
	if err != nil {
		t.Fatal(err)
	}
	var checkpoint expansionAuthorityCheckpoint
	if err := decodeExactJSON(data, &checkpoint); err != nil {
		t.Fatal(err)
	}
	if checkpoint.Generation != generation || checkpoint.CurrentEpoch != epoch {
		t.Fatalf("checkpoint generation/epoch=%d/%d, want %d/%d", checkpoint.Generation, checkpoint.CurrentEpoch, generation, epoch)
	}
}

func TestCrossProcessRootLifecycleFencesStartupGCAndProjectSigning(t *testing.T) {
	root := authorityMaintenanceFixture(t)
	temp := t.TempDir()
	ready, release := filepath.Join(temp, "ready"), filepath.Join(temp, "release")
	cmd := exec.Command(os.Args[0], "-test.run=^TestAuthorityRootLifecycleProcessHelper$")
	cmd.Env = append(os.Environ(),
		"AINOVEL_AUTHORITY_ROOT_LIFECYCLE_HELPER=1", "AINOVEL_AUTHORITY_ROOT="+root,
		"AINOVEL_AUTHORITY_READY="+ready, "AINOVEL_AUTHORITY_COMMIT="+release,
	)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	childDone := make(chan error, 1)
	go func() { childDone <- cmd.Wait() }()
	waitForAuthorityTestFile(t, ready)

	startupOutput := filepath.Join(temp, "startup", "output")
	if err := os.MkdirAll(startupOutput, 0o755); err != nil {
		t.Fatal(err)
	}
	startupDone := make(chan *Store, 1)
	go func() { startupDone <- NewStore(startupOutput) }()

	signingOutput := filepath.Join(temp, "signing", "output")
	if err := os.MkdirAll(signingOutput, 0o755); err != nil {
		t.Fatal(err)
	}
	writeStoreTestProjectManifest(t, signingOutput, "cross-process-signing")
	signingDone := make(chan error, 1)
	go func() {
		revisions := NewRevisionStore(signingOutput)
		signingDone <- revisions.withRevisionTransaction(func() error {
			_, _, err := revisions.loadOrCreateExpansionPublicationAuthority("cross-process-signing")
			return err
		})
	}()

	select {
	case <-startupDone:
		t.Fatal("startup bypassed live cross-process root lifecycle")
	case <-time.After(150 * time.Millisecond):
	}
	select {
	case <-signingDone:
		t.Fatal("signing bypassed live cross-process root lifecycle")
	case <-time.After(150 * time.Millisecond):
	}
	if err := os.WriteFile(release, []byte("release"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := <-childDone; err != nil {
		t.Fatal(err)
	}
	select {
	case reopened := <-startupDone:
		if err := reopened.RecoverStructureMigration(); err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("startup deadlocked after root rotation")
	}
	select {
	case err := <-signingDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("project signing deadlocked after root rotation")
	}
}

func waitForAuthorityTestFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(path); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("authority child did not reach its lifecycle fault boundary")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestConcurrentNewStoreWaitsForCrossProcessPublicationTransaction(t *testing.T) {
	root := authorityMaintenanceFixture(t)
	temp := t.TempDir()
	output := filepath.Join(temp, "project", "output")
	if err := os.MkdirAll(output, 0o755); err != nil {
		t.Fatal(err)
	}
	_, journalPath, recordPath := writeAuthorityCreationFixture(t, output, authorityCreationPending)
	ready, commit := filepath.Join(temp, "ready"), filepath.Join(temp, "commit")
	cmd := exec.Command(os.Args[0], "-test.run=^TestAuthorityTransactionProcessHelper$")
	cmd.Env = append(os.Environ(),
		"AINOVEL_AUTHORITY_LOCK_HELPER=1", "AINOVEL_AUTHORITY_ROOT="+root,
		"AINOVEL_AUTHORITY_OUTPUT="+output, "AINOVEL_AUTHORITY_READY="+ready,
		"AINOVEL_AUTHORITY_COMMIT="+commit, "AINOVEL_AUTHORITY_JOURNAL="+journalPath,
	)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	childDone := make(chan error, 1)
	go func() { childDone <- cmd.Wait() }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("cross-process publication helper did not acquire the lock")
		}
		time.Sleep(10 * time.Millisecond)
	}
	competitorDone := make(chan *Store, 1)
	go func() { competitorDone <- NewStore(output) }()
	select {
	case <-competitorDone:
		t.Fatal("NewStore bypassed the cross-process project transaction")
	case <-time.After(150 * time.Millisecond):
	}
	if _, err := os.Stat(recordPath); err != nil {
		t.Fatalf("cross-process live record was removed: %v", err)
	}
	if err := os.WriteFile(commit, []byte("commit"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := <-childDone; err != nil {
		t.Fatal(err)
	}
	select {
	case reopened := <-competitorDone:
		if err := reopened.RecoverStructureMigration(); err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("NewStore deadlocked after cross-process publication")
	}
	if _, err := os.Stat(recordPath); err != nil {
		t.Fatalf("cross-process completed record was removed: %v", err)
	}
}

func TestAuthorityOrphanMaintenancePendingAcceptedABAAndIdempotency(t *testing.T) {
	authorityMaintenanceFixture(t)

	t.Run("pending missing output rolls back exact record", func(t *testing.T) {
		output := filepath.Join(t.TempDir(), "project", "output")
		if err := os.MkdirAll(output, 0o755); err != nil {
			t.Fatal(err)
		}
		_, journalPath, recordPath := writeAuthorityCreationFixture(t, output, authorityCreationPending)
		if err := os.RemoveAll(filepath.Dir(output)); err != nil {
			t.Fatal(err)
		}
		report, err := ReconcileExpansionAuthorityOrphans()
		if err != nil || report.Recovered != 1 {
			t.Fatalf("report=%+v err=%v", report, err)
		}
		for _, path := range []string{journalPath, recordPath} {
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatalf("orphan remains at protected path: %v", err)
			}
		}
		if _, err := ReconcileExpansionAuthorityOrphans(); err != nil {
			t.Fatalf("maintenance is not idempotent: %v", err)
		}
	})

	t.Run("accepted missing output finalizes without deleting record", func(t *testing.T) {
		output := filepath.Join(t.TempDir(), "project", "output")
		_ = os.MkdirAll(output, 0o755)
		_, journalPath, recordPath := writeAuthorityCreationFixture(t, output, authorityCreationAccepted)
		_ = os.RemoveAll(filepath.Dir(output))
		report, err := ReconcileExpansionAuthorityOrphans()
		if err != nil || report.Finalized != 1 {
			t.Fatalf("report=%+v err=%v", report, err)
		}
		if _, err := os.Stat(journalPath); !os.IsNotExist(err) {
			t.Fatalf("accepted journal remains: %v", err)
		}
		if _, err := os.Stat(recordPath); err != nil {
			t.Fatalf("accepted authority record was deleted: %v", err)
		}
	})

	t.Run("record ABA fails closed", func(t *testing.T) {
		output := filepath.Join(t.TempDir(), "project", "output")
		_ = os.MkdirAll(output, 0o755)
		journal, journalPath, recordPath := writeAuthorityCreationFixture(t, output, authorityCreationPending)
		_ = os.RemoveAll(filepath.Dir(output))
		changed := journal.NewRecord
		changed.CreationID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		if err := writeProtectedAuthorityJSON(recordPath, changed); err != nil {
			t.Fatal(err)
		}
		if _, err := ReconcileExpansionAuthorityOrphans(); err == nil {
			t.Fatal("record ABA was accepted")
		}
		for _, path := range []string{journalPath, recordPath} {
			if _, err := os.Stat(path); err != nil {
				t.Fatalf("fail-closed evidence was removed: %v", err)
			}
		}
	})
}

func TestAuthorityOrphanMaintenanceRejectsUnknownJournalAndSerializes(t *testing.T) {
	authorityMaintenanceFixture(t)
	output := filepath.Join(t.TempDir(), "project", "output")
	_ = os.MkdirAll(output, 0o755)
	journal, journalPath, _ := writeAuthorityCreationFixture(t, output, authorityCreationPending)
	_ = os.RemoveAll(filepath.Dir(output))
	payload, err := json.Marshal(struct {
		expansionAuthorityCreationJournal
		Unknown string `json:"unknown"`
	}{journal, "tamper"})
	if err != nil {
		t.Fatal(err)
	}
	protected, err := protectAuthoritySecret(payload)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeRestrictedAtomic(journalPath, protected); err != nil {
		t.Fatal(err)
	}
	if _, err := ReconcileExpansionAuthorityOrphans(); err == nil {
		t.Fatal("unknown journal field was accepted")
	}

	// Restore valid evidence and prove concurrent maintenance has one durable,
	// idempotent outcome rather than double-removing a private record.
	if err := writeProtectedAuthorityJSON(journalPath, journal); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := ReconcileExpansionAuthorityOrphans()
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
	}
}

func TestAuthorityOrphanMaintenanceIsIdempotentAcrossProcesses(t *testing.T) {
	root := authorityMaintenanceFixture(t)
	output := filepath.Join(t.TempDir(), "project", "output")
	_ = os.MkdirAll(output, 0o755)
	_, journalPath, recordPath := writeAuthorityCreationFixture(t, output, authorityCreationPending)
	_ = os.RemoveAll(filepath.Dir(output))
	commands := make([]*exec.Cmd, 2)
	for index := range commands {
		commands[index] = exec.Command(os.Args[0], "-test.run=^TestAuthorityMaintenanceProcessHelper$")
		commands[index].Env = append(os.Environ(), "AINOVEL_AUTHORITY_GC_HELPER=1", "AINOVEL_AUTHORITY_ROOT="+root)
		if err := commands[index].Start(); err != nil {
			t.Fatal(err)
		}
	}
	for _, command := range commands {
		if err := command.Wait(); err != nil {
			t.Fatalf("cross-process maintenance failed: %v", err)
		}
	}
	for _, path := range []string{journalPath, recordPath} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("cross-process maintenance left orphan: %v", err)
		}
	}
}

func TestOpeningAnotherProjectSweepsMovedAndDeletedAuthorityOrphans(t *testing.T) {
	authorityMaintenanceFixture(t)
	for _, scenario := range []string{"moved", "deleted"} {
		t.Run(scenario, func(t *testing.T) {
			base := t.TempDir()
			output := filepath.Join(base, "source", "output")
			_ = os.MkdirAll(output, 0o755)
			_, journalPath, recordPath := writeAuthorityCreationFixture(t, output, authorityCreationPending)
			if scenario == "moved" {
				if err := os.Rename(filepath.Join(base, "source"), filepath.Join(base, "moved")); err != nil {
					t.Fatal(err)
				}
			} else if err := os.RemoveAll(filepath.Join(base, "source")); err != nil {
				t.Fatal(err)
			}
			other := filepath.Join(base, "other", "output")
			_ = os.MkdirAll(other, 0o755)
			reopened := NewStore(other)
			if err := reopened.RecoverStructureMigration(); err != nil {
				t.Fatal(err)
			}
			for _, path := range []string{journalPath, recordPath} {
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatalf("global startup sweep left an orphan: %v", err)
				}
			}
		})
	}
}

func TestAuthorityOrphanRetentionAndUnsafeFileIdentityFailClosed(t *testing.T) {
	authorityMaintenanceFixture(t)
	output := filepath.Join(t.TempDir(), "project", "output")
	_ = os.MkdirAll(output, 0o755)
	_, journalPath, recordPath := writeAuthorityCreationFixture(t, output, authorityCreationPending)
	_ = os.RemoveAll(filepath.Dir(output))
	previousRetention := authorityOrphanRetention
	authorityOrphanRetention = 2 * time.Hour
	report, err := ReconcileExpansionAuthorityOrphans()
	authorityOrphanRetention = previousRetention
	if err != nil || report.Deferred != 1 {
		t.Fatalf("young orphan report=%+v err=%v", report, err)
	}
	for _, path := range []string{journalPath, recordPath} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("young orphan was removed: %v", err)
		}
	}

	peer := filepath.Join(t.TempDir(), "journal-peer")
	if err := os.Rename(journalPath, peer); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(peer, journalPath); err != nil {
		t.Skipf("hardlink identity test unavailable: %v", err)
	}
	defer os.Remove(peer)
	if _, err := ReconcileExpansionAuthorityOrphans(); err == nil {
		t.Fatal("hardlinked journal was accepted")
	}
	if _, err := os.Stat(recordPath); err != nil {
		t.Fatalf("unsafe journal caused record deletion: %v", err)
	}
}

func TestAuthorityOrphanMaintenanceRejectsSymlinkPermissionAndPathEscape(t *testing.T) {
	authorityMaintenanceFixture(t)
	output := filepath.Join(t.TempDir(), "project", "output")
	_ = os.MkdirAll(output, 0o755)
	journal, journalPath, recordPath := writeAuthorityCreationFixture(t, output, authorityCreationPending)
	_ = os.RemoveAll(filepath.Dir(output))

	t.Run("symlink", func(t *testing.T) {
		peer := filepath.Join(t.TempDir(), "journal-peer")
		if err := os.Rename(journalPath, peer); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(peer, journalPath); err != nil {
			if err := os.Rename(peer, journalPath); err != nil {
				t.Fatal(err)
			}
			t.Skipf("symlink test unavailable: %v", err)
		}
		if _, err := ReconcileExpansionAuthorityOrphans(); err == nil {
			t.Fatal("symlinked journal was accepted")
		}
		if _, err := os.Stat(recordPath); err != nil {
			t.Fatalf("symlink rejection removed the record: %v", err)
		}
		_ = os.Remove(journalPath)
		if err := os.Rename(peer, journalPath); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("path escape", func(t *testing.T) {
		tampered := journal
		tampered.OutputDir = journal.OutputDir + string(os.PathSeparator) + ".." + string(os.PathSeparator) + "escape"
		if err := writeProtectedAuthorityJSON(journalPath, tampered); err != nil {
			t.Fatal(err)
		}
		if _, err := ReconcileExpansionAuthorityOrphans(); err == nil {
			t.Fatal("non-canonical escaping output path was accepted")
		}
		if _, err := os.Stat(recordPath); err != nil {
			t.Fatalf("path rejection removed the record: %v", err)
		}
		if err := writeProtectedAuthorityJSON(journalPath, journal); err != nil {
			t.Fatal(err)
		}
	})

	if runtime.GOOS != "windows" {
		t.Run("permissions", func(t *testing.T) {
			if err := os.Chmod(journalPath, 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := ReconcileExpansionAuthorityOrphans(); err == nil {
				t.Fatal("world-readable protected journal was accepted")
			}
			if _, err := os.Stat(recordPath); err != nil {
				t.Fatalf("permission rejection removed the record: %v", err)
			}
		})
	}
}

func TestAuthorityOrphanDeletionDurabilityAndUnprovenPendingFailClosed(t *testing.T) {
	root := authorityMaintenanceFixture(t)

	t.Run("unproven pending is retained", func(t *testing.T) {
		output := filepath.Join(t.TempDir(), "project", "output")
		_ = os.MkdirAll(output, 0o755)
		journal, journalPath, recordPath := writeAuthorityCreationFixture(t, output, authorityCreationPending)
		evidencePath, _ := authorityAcceptancePath(journal.JournalID)
		if err := os.Remove(evidencePath); err != nil {
			t.Fatal(err)
		}
		if err := syncAuthorityDirectory(filepath.Dir(evidencePath)); err != nil {
			t.Fatal(err)
		}
		_ = os.RemoveAll(filepath.Dir(output))
		report, err := ReconcileExpansionAuthorityOrphans()
		if err != nil || report.Deferred != 1 {
			t.Fatalf("unproven report=%+v err=%v", report, err)
		}
		for _, path := range []string{journalPath, recordPath} {
			if _, err := os.Stat(path); err != nil {
				t.Fatalf("unproven outcome evidence was removed: %v", err)
			}
		}
	})

	for _, faultDirectory := range []string{"projects", "publications"} {
		t.Run(faultDirectory+" directory sync failure replays safely", func(t *testing.T) {
			output := filepath.Join(t.TempDir(), "project", "output")
			_ = os.MkdirAll(output, 0o755)
			journal, journalPath, recordPath := writeAuthorityCreationFixture(t, output, authorityCreationPending)
			_ = os.RemoveAll(filepath.Dir(output))
			faulted := false
			authorityDirectorySyncFault = func(path string) error {
				if filepath.Clean(path) == filepath.Join(root, faultDirectory) && !faulted {
					faulted = true
					return errors.New("injected directory durability failure")
				}
				return nil
			}
			if _, err := ReconcileExpansionAuthorityOrphans(); err == nil || !faulted {
				t.Fatal("directory durability failure did not stop reconciliation")
			}
			authorityDirectorySyncFault = nil
			// Model a crash where an unflushed journal unlink reappears. The
			// signed abort evidence makes replay exact even if the record unlink
			// was already durable.
			if faultDirectory == "publications" {
				if err := writeProtectedAuthorityJSON(journalPath, journal); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := ReconcileExpansionAuthorityOrphans(); err != nil {
				t.Fatalf("durability replay failed: %v", err)
			}
			for _, path := range []string{journalPath, recordPath} {
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatalf("durability replay left %s: %v", filepath.Base(path), err)
				}
			}
		})
	}
	authorityDirectorySyncFault = nil
}

func TestAuthorityOutcomeInventoryRejectsUnknownTamperAndIdentity(t *testing.T) {
	authorityMaintenanceFixture(t)
	output := filepath.Join(t.TempDir(), "project", "output")
	_ = os.MkdirAll(output, 0o755)
	journal, _, _ := writeAuthorityCreationFixture(t, output, authorityCreationPending)
	path, _ := authorityAcceptancePath(journal.JournalID)
	var evidence expansionAuthorityAcceptanceEvidence
	if err := readProtectedAuthorityJSONStrict(path, &evidence); err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(struct {
		expansionAuthorityAcceptanceEvidence
		Unknown string `json:"unknown"`
	}{evidence, "tamper"})
	if err != nil {
		t.Fatal(err)
	}
	protected, err := protectAuthoritySecret(payload)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeRestrictedAtomic(path, protected); err != nil {
		t.Fatal(err)
	}
	if _, err := ReconcileExpansionAuthorityOrphans(); err == nil {
		t.Fatal("unknown outcome evidence field was accepted")
	}
	if err := writeProtectedAuthorityJSON(path, evidence); err != nil {
		t.Fatal(err)
	}
	tampered := evidence
	tampered.Signature = base64.StdEncoding.EncodeToString(make([]byte, ed25519.SignatureSize))
	if err := writeProtectedAuthorityJSON(path, tampered); err != nil {
		t.Fatal(err)
	}
	if _, err := ReconcileExpansionAuthorityOrphans(); err == nil {
		t.Fatal("tampered outcome signature was accepted")
	}
	if err := writeProtectedAuthorityJSON(path, evidence); err != nil {
		t.Fatal(err)
	}
	wrongPath := filepath.Join(filepath.Dir(path), strings.Repeat("a", 64)+".json")
	if err := os.Rename(path, wrongPath); err != nil {
		t.Fatal(err)
	}
	if _, err := ReconcileExpansionAuthorityOrphans(); err == nil {
		t.Fatal("outcome filename identity mismatch was accepted")
	}
}

func TestAuthorityOutcomeEvidenceTerminalLifecycleAndBounds(t *testing.T) {
	root := authorityMaintenanceFixture(t)
	for _, count := range []int{maxAuthorityProjectRecords - 1, maxAuthorityProjectRecords} {
		if err := enforceAuthorityOutcomeInventoryBound(count); err != nil {
			t.Fatalf("valid outcome inventory %d was rejected: %v", count, err)
		}
	}
	if err := enforceAuthorityOutcomeInventoryBound(maxAuthorityProjectRecords + 1); err == nil {
		t.Fatal("limit+1 outcome inventory was accepted")
	}

	output := filepath.Join(t.TempDir(), "terminal-abort", "output")
	if err := os.MkdirAll(output, 0o755); err != nil {
		t.Fatal(err)
	}
	journal, journalPath, recordPath := writeAuthorityCreationFixture(t, output, authorityCreationPending)
	evidencePath, _ := authorityAcceptancePath(journal.JournalID)
	if err := removeAuthorityJournalDurably(journalPath); err != nil {
		t.Fatal(err)
	}
	if _, err := ReconcileExpansionAuthorityOrphans(); err == nil {
		t.Fatal("aborted evidence with its exact private record was reclaimed")
	}
	if _, err := os.Stat(evidencePath); err != nil {
		t.Fatalf("unsafe aborted evidence was removed: %v", err)
	}
	if err := os.Remove(recordPath); err != nil {
		t.Fatal(err)
	}
	if err := syncAuthorityDirectory(filepath.Join(root, "projects")); err != nil {
		t.Fatal(err)
	}
	if _, err := ReconcileExpansionAuthorityOrphans(); err != nil {
		t.Fatalf("terminal aborted evidence was not reclaimable: %v", err)
	}
	if _, err := os.Stat(evidencePath); !os.IsNotExist(err) {
		t.Fatalf("terminal aborted evidence remains: %v", err)
	}

	for index := 0; index < 8; index++ {
		output := filepath.Join(t.TempDir(), "repeated-abort", "output")
		if err := os.MkdirAll(output, 0o755); err != nil {
			t.Fatal(err)
		}
		journal, journalPath, recordPath := writeAuthorityCreationFixture(t, output, authorityCreationPending)
		if err := os.Remove(recordPath); err != nil {
			t.Fatal(err)
		}
		if err := removeAuthorityJournalDurably(journalPath); err != nil {
			t.Fatal(err)
		}
		if _, err := ReconcileExpansionAuthorityOrphans(); err != nil {
			t.Fatalf("repeated aborted evidence %d blocked recovery: %v", index, err)
		}
		evidencePath, _ := authorityAcceptancePath(journal.JournalID)
		if _, err := os.Stat(evidencePath); !os.IsNotExist(err) {
			t.Fatalf("repeated aborted evidence %d remains: %v", index, err)
		}
	}
}

func TestAuthorityOutcomeEvidenceUnlinkAndDirectorySyncFaultsReplay(t *testing.T) {
	for _, stage := range []string{"before_unlink", "after_unlink"} {
		t.Run(stage, func(t *testing.T) {
			root := authorityMaintenanceFixture(t)
			t.Cleanup(func() {
				authorityOutcomeUnlinkFault = nil
				authorityDirectorySyncFault = nil
			})
			output := filepath.Join(t.TempDir(), "unlink-fault", "output")
			if err := os.MkdirAll(output, 0o755); err != nil {
				t.Fatal(err)
			}
			journal, journalPath, recordPath := writeAuthorityCreationFixture(t, output, authorityCreationPending)
			evidencePath, _ := authorityAcceptancePath(journal.JournalID)
			evidenceBytes, err := os.ReadFile(evidencePath)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(recordPath); err != nil {
				t.Fatal(err)
			}
			if err := syncAuthorityDirectory(filepath.Join(root, "projects")); err != nil {
				t.Fatal(err)
			}
			if err := removeAuthorityJournalDurably(journalPath); err != nil {
				t.Fatal(err)
			}
			faulted := false
			authorityOutcomeUnlinkFault = func(path, hookStage string) error {
				if filepath.Clean(path) == filepath.Clean(evidencePath) && hookStage == stage && !faulted {
					faulted = true
					return errors.New("injected outcome unlink fault")
				}
				return nil
			}
			if _, err := ReconcileExpansionAuthorityOrphans(); err == nil || !faulted {
				t.Fatalf("%s fault did not stop terminal evidence reclamation", stage)
			}
			authorityOutcomeUnlinkFault = nil
			if stage == "after_unlink" {
				// Either side of an unsynced unlink may be observed after a crash.
				// Recreate the old directory entry so replay exercises both outcomes.
				if err := os.WriteFile(evidencePath, evidenceBytes, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := ReconcileExpansionAuthorityOrphans(); err != nil {
				t.Fatalf("%s fault was not restart-replayable: %v", stage, err)
			}
			if _, err := os.Stat(evidencePath); !os.IsNotExist(err) {
				t.Fatalf("%s replay retained terminal evidence: %v", stage, err)
			}
		})
	}

	t.Run("missing evidence retry persists the acceptances directory", func(t *testing.T) {
		root := authorityMaintenanceFixture(t)
		t.Cleanup(func() { authorityDirectorySyncFault = nil })
		output := filepath.Join(t.TempDir(), "directory-fault", "output")
		if err := os.MkdirAll(output, 0o755); err != nil {
			t.Fatal(err)
		}
		journal, journalPath, recordPath := writeAuthorityCreationFixture(t, output, authorityCreationPending)
		evidencePath, _ := authorityAcceptancePath(journal.JournalID)
		if err := os.Remove(recordPath); err != nil {
			t.Fatal(err)
		}
		if err := syncAuthorityDirectory(filepath.Join(root, "projects")); err != nil {
			t.Fatal(err)
		}
		if err := removeAuthorityJournalDurably(journalPath); err != nil {
			t.Fatal(err)
		}
		acceptancesDir := filepath.Dir(evidencePath)
		faulted := false
		authorityDirectorySyncFault = func(path string) error {
			if filepath.Clean(path) == filepath.Clean(acceptancesDir) && !faulted {
				faulted = true
				return errors.New("injected acceptances directory sync fault")
			}
			return nil
		}
		if _, err := ReconcileExpansionAuthorityOrphans(); err == nil || !faulted {
			t.Fatal("acceptances directory sync fault did not stop reclamation")
		}
		if _, err := os.Stat(evidencePath); !os.IsNotExist(err) {
			t.Fatalf("outcome unlink did not precede its directory sync: %v", err)
		}
		authorityDirectorySyncFault = nil
		syncCalls := 0
		authorityDirectorySyncFault = func(path string) error {
			if filepath.Clean(path) == filepath.Clean(acceptancesDir) {
				syncCalls++
			}
			return nil
		}
		if _, err := ReconcileExpansionAuthorityOrphans(); err != nil {
			t.Fatalf("missing evidence retry did not finish durability: %v", err)
		}
		if syncCalls != 1 {
			t.Fatalf("missing evidence retry synced acceptances %d times, want 1", syncCalls)
		}
	})
}

func TestAuthorityJournalAbsenceDurabilityPrecedesOutcomeReclaim(t *testing.T) {
	for _, outcome := range []string{authorityOutcomeAborted, authorityOutcomeAccepted} {
		for _, crashView := range []string{"journal reappeared", "journal remained absent"} {
			t.Run(outcome+"/"+crashView, func(t *testing.T) {
				root := authorityMaintenanceFixture(t)
				t.Cleanup(func() {
					authorityDirectorySyncFault = nil
					authorityOutcomeUnlinkFault = nil
				})
				output := filepath.Join(t.TempDir(), "journal-sync", "output")
				if err := os.MkdirAll(output, 0o755); err != nil {
					t.Fatal(err)
				}
				journal, journalPath, recordPath := writeAuthorityCreationFixture(t, output, authorityCreationPending)
				if outcome == authorityOutcomeAccepted {
					writeAcceptedAuthorityEvidenceForTest(t, journal)
				} else {
					if err := os.Remove(recordPath); err != nil {
						t.Fatal(err)
					}
					if err := syncAuthorityDirectory(filepath.Join(root, "projects")); err != nil {
						t.Fatal(err)
					}
				}
				evidencePath, _ := authorityAcceptancePath(journal.JournalID)
				publicationsDir := filepath.Dir(journalPath)
				acceptancesDir := filepath.Dir(evidencePath)

				failSync := true
				authorityDirectorySyncFault = func(path string) error {
					if filepath.Clean(path) == filepath.Clean(publicationsDir) && failSync {
						return errors.New("injected publications directory sync failure")
					}
					return nil
				}
				if err := removeAuthorityJournalAndOutcomeDurably(journalPath, journal); err == nil {
					t.Fatal("journal unlink sync failure did not stop evidence reclaim")
				}
				if _, err := os.Stat(evidencePath); err != nil {
					t.Fatalf("publications sync failure removed outcome evidence: %v", err)
				}
				if crashView == "journal reappeared" {
					if err := writeProtectedAuthorityJSON(journalPath, journal); err != nil {
						t.Fatal(err)
					}
				} else if _, err := os.Stat(journalPath); !os.IsNotExist(err) {
					t.Fatalf("journal should remain absent after crash view: %v", err)
				}

				// A second failed retry must still retain evidence in either crash view.
				if err := removeAuthorityJournalAndOutcomeDurably(journalPath, journal); err == nil {
					t.Fatal("retry publications sync failure did not stop evidence reclaim")
				}
				if _, err := os.Stat(evidencePath); err != nil {
					t.Fatalf("retry publications sync failure removed evidence: %v", err)
				}

				failSync = false
				events := make([]string, 0, 3)
				authorityDirectorySyncFault = func(path string) error {
					switch filepath.Clean(path) {
					case filepath.Clean(publicationsDir):
						events = append(events, "publications-sync")
					case filepath.Clean(acceptancesDir):
						events = append(events, "acceptances-sync")
					}
					return nil
				}
				authorityOutcomeUnlinkFault = func(path, stage string) error {
					if filepath.Clean(path) == filepath.Clean(evidencePath) && stage == "before_unlink" {
						events = append(events, "evidence-unlink")
					}
					return nil
				}
				if err := removeAuthorityJournalAndOutcomeDurably(journalPath, journal); err != nil {
					t.Fatal(err)
				}
				want := []string{"publications-sync", "evidence-unlink", "acceptances-sync"}
				if !reflect.DeepEqual(events, want) {
					t.Fatalf("durable terminal order = %v, want %v", events, want)
				}
				if outcome == authorityOutcomeAccepted {
					if _, err := os.Stat(recordPath); err != nil {
						t.Fatalf("accepted cleanup removed committed record: %v", err)
					}
				}

				// Missing journal/evidence retries still complete both directory barriers.
				events = nil
				if err := removeAuthorityJournalAndOutcomeDurably(journalPath, journal); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(events, want) {
					t.Fatalf("idempotent terminal order = %v, want %v", events, want)
				}
			})
		}
	}
}

func TestRemoveAuthorityJournalDurablyMissingParentIsNoop(t *testing.T) {
	t.Cleanup(func() { authorityDirectorySyncFault = nil })
	path := filepath.Join(t.TempDir(), "missing", "publications", strings.Repeat("a", 64)+".json")
	syncCalled := false
	authorityDirectorySyncFault = func(string) error {
		syncCalled = true
		return errors.New("missing parent must not be opened for sync")
	}
	if err := removeAuthorityJournalDurably(path); err != nil {
		t.Fatal(err)
	}
	if syncCalled {
		t.Fatal("missing publications parent was treated as an unsynced prior unlink")
	}
}

func TestAuthorityTerminalScanSyncsAbsentJournalBeforeEvidenceUnlink(t *testing.T) {
	root := authorityMaintenanceFixture(t)
	t.Cleanup(func() {
		authorityDirectorySyncFault = nil
		authorityOutcomeUnlinkFault = nil
	})
	output := filepath.Join(t.TempDir(), "terminal-scan", "output")
	if err := os.MkdirAll(output, 0o755); err != nil {
		t.Fatal(err)
	}
	journal, journalPath, recordPath := writeAuthorityCreationFixture(t, output, authorityCreationPending)
	if err := os.Remove(recordPath); err != nil {
		t.Fatal(err)
	}
	if err := syncAuthorityDirectory(filepath.Join(root, "projects")); err != nil {
		t.Fatal(err)
	}
	if err := removeAuthorityJournalDurably(journalPath); err != nil {
		t.Fatal(err)
	}
	evidencePath, _ := authorityAcceptancePath(journal.JournalID)
	publicationsDir := filepath.Dir(journalPath)
	events := make([]string, 0, 2)
	authorityDirectorySyncFault = func(path string) error {
		if filepath.Clean(path) == filepath.Clean(publicationsDir) {
			events = append(events, "publications-sync")
		}
		return nil
	}
	authorityOutcomeUnlinkFault = func(path, stage string) error {
		if filepath.Clean(path) == filepath.Clean(evidencePath) && stage == "before_unlink" {
			events = append(events, "evidence-unlink")
		}
		return nil
	}
	if _, err := ReconcileExpansionAuthorityOrphans(); err != nil {
		t.Fatal(err)
	}
	if len(events) < 2 || events[0] != "publications-sync" || events[1] != "evidence-unlink" {
		t.Fatalf("terminal scan order = %v", events)
	}
}

func TestAuthorityOutcomeEvidenceLiveInventoryLimitMatrix(t *testing.T) {
	previousLimit := authorityOutcomeInventoryLimit
	authorityOutcomeInventoryLimit = 3
	t.Cleanup(func() { authorityOutcomeInventoryLimit = previousLimit })
	for _, count := range []int{authorityOutcomeInventoryLimit - 1, authorityOutcomeInventoryLimit, authorityOutcomeInventoryLimit + 1} {
		t.Run(fmt.Sprintf("count-%d", count), func(t *testing.T) {
			authorityMaintenanceFixture(t)
			paths := make([]string, 0, count)
			for index := 0; index < count; index++ {
				output := filepath.Join(t.TempDir(), fmt.Sprintf("live-%d", index), "output")
				if err := os.MkdirAll(output, 0o755); err != nil {
					t.Fatal(err)
				}
				journal, _, _ := writeAuthorityCreationFixture(t, output, authorityCreationPending)
				evidencePath, _ := authorityAcceptancePath(journal.JournalID)
				paths = append(paths, evidencePath)
			}
			_, err := ReconcileExpansionAuthorityOrphans()
			if count <= authorityOutcomeInventoryLimit && err != nil {
				t.Fatalf("live outcome count %d was rejected: %v", count, err)
			}
			if count > authorityOutcomeInventoryLimit && err == nil {
				t.Fatalf("live outcome count %d exceeded the bound without failure", count)
			}
			if count > authorityOutcomeInventoryLimit {
				for _, path := range paths {
					if _, statErr := os.Stat(path); statErr != nil {
						t.Fatalf("limit failure removed live outcome %s: %v", filepath.Base(path), statErr)
					}
				}
			}
		})
	}
}

func TestAuthorityOutcomeEvidenceTerminalABAStates(t *testing.T) {
	t.Run("accepted exact record is reclaimed without deleting the record", func(t *testing.T) {
		authorityMaintenanceFixture(t)
		output := filepath.Join(t.TempDir(), "accepted", "output")
		if err := os.MkdirAll(output, 0o755); err != nil {
			t.Fatal(err)
		}
		journal, journalPath, recordPath := writeAuthorityCreationFixture(t, output, authorityCreationPending)
		writeAcceptedAuthorityEvidenceForTest(t, journal)
		if err := removeAuthorityJournalDurably(journalPath); err != nil {
			t.Fatal(err)
		}
		if _, err := ReconcileExpansionAuthorityOrphans(); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(recordPath); err != nil {
			t.Fatalf("accepted terminal cleanup deleted committed record: %v", err)
		}
		evidencePath, _ := authorityAcceptancePath(journal.JournalID)
		if _, err := os.Stat(evidencePath); !os.IsNotExist(err) {
			t.Fatalf("accepted terminal evidence remains: %v", err)
		}
	})

	t.Run("same-creation record binding mismatch fails closed", func(t *testing.T) {
		authorityMaintenanceFixture(t)
		output := filepath.Join(t.TempDir(), "binding-mismatch", "output")
		if err := os.MkdirAll(output, 0o755); err != nil {
			t.Fatal(err)
		}
		journal, journalPath, recordPath := writeAuthorityCreationFixture(t, output, authorityCreationPending)
		writeAcceptedAuthorityEvidenceForTest(t, journal)
		if err := removeAuthorityJournalDurably(journalPath); err != nil {
			t.Fatal(err)
		}
		changed := journal.NewRecord
		changed.KeyID = "project-aba"
		if err := writeProtectedAuthorityJSON(recordPath, changed); err != nil {
			t.Fatal(err)
		}
		if _, err := ReconcileExpansionAuthorityOrphans(); err == nil {
			t.Fatal("same-creation record/key/digest ABA was accepted")
		}
		evidencePath, _ := authorityAcceptancePath(journal.JournalID)
		if _, err := os.Stat(evidencePath); err != nil {
			t.Fatalf("binding mismatch removed its fail-closed evidence: %v", err)
		}
	})

	t.Run("later creation makes prior aborted evidence safely stale", func(t *testing.T) {
		authorityMaintenanceFixture(t)
		output := filepath.Join(t.TempDir(), "stale-abort", "output")
		if err := os.MkdirAll(output, 0o755); err != nil {
			t.Fatal(err)
		}
		journal, journalPath, recordPath := writeAuthorityCreationFixture(t, output, authorityCreationPending)
		if err := removeAuthorityJournalDurably(journalPath); err != nil {
			t.Fatal(err)
		}
		later := journal.NewRecord
		later.CreationID = strings.Repeat("f", 64)
		later.KeyID = "project-later"
		if err := writeProtectedAuthorityJSON(recordPath, later); err != nil {
			t.Fatal(err)
		}
		if _, err := ReconcileExpansionAuthorityOrphans(); err != nil {
			t.Fatalf("later creation did not make old abort evidence stale: %v", err)
		}
		var retained expansionAuthorityProjectRecord
		if err := readProtectedAuthorityJSONStrict(recordPath, &retained); err != nil || retained.CreationID != later.CreationID {
			t.Fatalf("stale cleanup changed later record: record=%+v err=%v", retained, err)
		}
	})
}

func writeAcceptedAuthorityEvidenceForTest(t *testing.T, journal expansionAuthorityCreationJournal) {
	t.Helper()
	record := journal.NewRecord
	privateKey, err := base64.StdEncoding.DecodeString(record.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	evidence := expansionAuthorityAcceptanceEvidence{
		Version: authorityAcceptanceVersion, Outcome: authorityOutcomeAccepted, CreationID: journal.JournalID,
		ProjectRootHash: journal.ProjectRootHash, ProjectID: journal.ProjectID, ProjectInstance: journal.ProjectInstance,
		ProjectKeyID: record.KeyID, ProjectKeyEpoch: record.Epoch, ProjectPublicKey: record.PublicKey,
		RecordDigest: journal.NewRecordDigest, PublicationGeneration: 1, AcceptedRevisionID: "accepted-revision",
		ReceiptDigest: strings.Repeat("a", 64), CommittedAtUTC: time.Now().UTC().Format(time.RFC3339Nano),
	}
	payload, err := authorityAcceptanceSigningPayload(evidence)
	if err != nil {
		t.Fatal(err)
	}
	evidence.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(ed25519.PrivateKey(privateKey), payload))
	path, _ := authorityAcceptancePath(journal.JournalID)
	if err := writeProtectedAuthorityJSON(path, evidence); err != nil {
		t.Fatal(err)
	}
}

func TestAuthorityOutcomeEvidenceRejectsSymlinkAndHardlink(t *testing.T) {
	for _, kind := range []string{"symlink", "hardlink"} {
		t.Run(kind, func(t *testing.T) {
			authorityMaintenanceFixture(t)
			output := filepath.Join(t.TempDir(), "linked-evidence", "output")
			if err := os.MkdirAll(output, 0o755); err != nil {
				t.Fatal(err)
			}
			journal, _, recordPath := writeAuthorityCreationFixture(t, output, authorityCreationPending)
			evidencePath, _ := authorityAcceptancePath(journal.JournalID)
			peer := filepath.Join(t.TempDir(), "evidence-peer")
			var err error
			if kind == "symlink" {
				if err = os.Rename(evidencePath, peer); err == nil {
					err = os.Symlink(peer, evidencePath)
				}
			} else {
				err = os.Link(evidencePath, peer)
			}
			if err != nil {
				t.Skipf("%s unavailable: %v", kind, err)
			}
			if _, err := ReconcileExpansionAuthorityOrphans(); err == nil {
				t.Fatalf("%s outcome evidence was accepted", kind)
			}
			if _, err := os.Stat(recordPath); err != nil {
				t.Fatalf("%s rejection removed the private record: %v", kind, err)
			}
		})
	}
}
