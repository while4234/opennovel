package store

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestExpansionAuthorityRootPublicVerificationDoesNotReadSigningSecret(t *testing.T) {
	rootDir := isolatedExpansionAuthorityRoot(t)
	trust := signedExpansionAuthorityTrust(t)
	secretPath := filepath.Join(rootDir, rootSigningSecretName)
	secretBytes, err := os.ReadFile(secretPath)
	if err != nil {
		t.Fatal(err)
	}
	if bytesContainPrivateAuthorityField(t, filepath.Join(rootDir, rootPublicKeyringName)) || bytesContainPrivateAuthorityField(t, filepath.Join(rootDir, rootPublicAnchorName)) {
		t.Fatal("public root material exposed a private key field")
	}
	if err := os.Remove(secretPath); err != nil {
		t.Fatal(err)
	}
	if err := VerifyExpansionPublicationCertificate(trust); err != nil {
		t.Fatalf("public-only verifier required signing secret: %v", err)
	}
	if _, _, err := loadOrCreateExpansionAuthorityRoot(); err == nil {
		t.Fatal("signing silently regenerated a deleted established root secret")
	}
	if err := os.WriteFile(secretPath, secretBytes, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestExpansionAuthorityRootRotationGraceRevocationAndReplay(t *testing.T) {
	rootDir := isolatedExpansionAuthorityRoot(t)
	oldTrust := signedExpansionAuthorityTrust(t)
	oldKeyring, err := os.ReadFile(filepath.Join(rootDir, rootPublicKeyringName))
	if err != nil {
		t.Fatal(err)
	}
	if err := RotateExpansionAuthorityRoot(); err != nil {
		t.Fatal(err)
	}
	if err := VerifyExpansionPublicationCertificate(oldTrust); err != nil {
		t.Fatalf("preceding root was not available during its finite grace epoch: %v", err)
	}
	if err := RevokeExpansionAuthorityRootKey(oldTrust.RootKeyID); err != nil {
		t.Fatal(err)
	}
	if err := VerifyExpansionPublicationCertificate(oldTrust); err == nil {
		t.Fatal("explicitly revoked root certificate remained valid")
	}
	if err := os.WriteFile(filepath.Join(rootDir, rootPublicKeyringName), oldKeyring, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := VerifyExpansionPublicationCertificate(oldTrust); err == nil {
		t.Fatal("old public keyring replay bypassed the monotonic anchor epoch")
	}
	if err := RotateExpansionAuthorityRoot(); err == nil {
		t.Fatal("signing continued with a replayed keyring/anchor generation mismatch")
	}
	// Restore the current keyring by exercising an isolated second root for the
	// two-rotation revocation assertion; never manufacture trust state.
	isolatedExpansionAuthorityRoot(t)
	oldTrust = signedExpansionAuthorityTrust(t)
	if err := RotateExpansionAuthorityRoot(); err != nil {
		t.Fatal(err)
	}
	if err := RotateExpansionAuthorityRoot(); err != nil {
		t.Fatal(err)
	}
	if err := VerifyExpansionPublicationCertificate(oldTrust); err == nil {
		t.Fatal("root certificate remained valid after its grace epoch")
	}
}

func TestExpansionAuthorityRootReplacementDeletionAndPartialRotationFailClosed(t *testing.T) {
	rootDir := isolatedExpansionAuthorityRoot(t)
	trust := signedExpansionAuthorityTrust(t)

	t.Run("whole self-signed keyring replacement", func(t *testing.T) {
		original, _ := os.ReadFile(filepath.Join(rootDir, rootPublicKeyringName))
		attackerPublic, attackerPrivate, _ := ed25519.GenerateKey(rand.Reader)
		attackerSecret := newExpansionAuthorityRootSecret(1, attackerPublic, attackerPrivate)
		entry := expansionAuthorityRootKey{KeyID: attackerSecret.KeyID, Epoch: 1, PublicKey: attackerSecret.PublicKey, State: "active", SignedByKeyID: attackerSecret.KeyID}
		entryPayload, _ := expansionAuthorityRootCrossSigningPayload(entry)
		entry.CrossSignature = base64.StdEncoding.EncodeToString(ed25519.Sign(attackerPrivate, entryPayload))
		keyring := expansionAuthorityRootKeyring{Version: expansionAuthorityRootLifecycleVersion, Generation: 1, CurrentKeyID: entry.KeyID, CurrentEpoch: 1, Keys: []expansionAuthorityRootKey{entry}}
		if err := signExpansionAuthorityRootKeyring(&keyring, attackerPrivate); err != nil {
			t.Fatal(err)
		}
		if err := writeExpansionAuthorityRootKeyring(rootDir, keyring); err != nil {
			t.Fatal(err)
		}
		if err := VerifyExpansionPublicationCertificate(trust); err == nil {
			t.Fatal("self-signed replacement keyring escaped the installed anchor")
		}
		if err := os.WriteFile(filepath.Join(rootDir, rootPublicKeyringName), original, 0o644); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("public keyring deletion", func(t *testing.T) {
		path := filepath.Join(rootDir, rootPublicKeyringName)
		original, _ := os.ReadFile(path)
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		if err := VerifyExpansionPublicationCertificate(trust); err == nil {
			t.Fatal("verification accepted a deleted public keyring")
		}
		if err := os.WriteFile(path, original, 0o644); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("partial secret replacement rolls back", func(t *testing.T) {
		beforeSecret, _ := os.ReadFile(filepath.Join(rootDir, rootSigningSecretName))
		beforeKeyring, _ := os.ReadFile(filepath.Join(rootDir, rootPublicKeyringName))
		faulted := false
		expansionAuthorityWriteFault = func(path, stage string) error {
			if !faulted && filepath.Base(path) == rootSigningSecretName && stage == "after_sync" {
				faulted = true
				return errors.New("injected root secret replacement failure")
			}
			return nil
		}
		defer func() { expansionAuthorityWriteFault = nil }()
		if err := RotateExpansionAuthorityRoot(); err == nil {
			t.Fatal("partial root rotation fault did not fire")
		}
		expansionAuthorityWriteFault = nil
		afterSecret, _ := os.ReadFile(filepath.Join(rootDir, rootSigningSecretName))
		afterKeyring, _ := os.ReadFile(filepath.Join(rootDir, rootPublicKeyringName))
		if string(beforeSecret) != string(afterSecret) || string(beforeKeyring) != string(afterKeyring) {
			t.Fatal("partial root rotation did not restore exact prior bytes")
		}
		if _, err := os.Stat(filepath.Join(rootDir, rootRotationJournalName)); !os.IsNotExist(err) {
			t.Fatalf("root recovery journal remains after rollback: %v", err)
		}
		if err := VerifyExpansionPublicationCertificate(trust); err != nil {
			t.Fatalf("rollback invalidated prior public trust: %v", err)
		}
	})
}

func TestExpansionAuthorityRootExactNewRecoveryKeepsAdvancedCheckpoint(t *testing.T) {
	rootDir := isolatedExpansionAuthorityRoot(t)
	_ = signedExpansionAuthorityTrust(t)
	installationDir, err := expansionAuthorityInstallationDir(rootDir)
	if err != nil {
		t.Fatal(err)
	}
	readState := func() (expansionAuthorityRootSecret, expansionAuthorityRootAnchor, expansionAuthorityRootKeyring, []byte, []byte, []byte, []byte, []byte) {
		var secret expansionAuthorityRootSecret
		if err := readProtectedAuthorityJSON(filepath.Join(rootDir, rootSigningSecretName), &secret); err != nil {
			t.Fatal(err)
		}
		anchor, err := readExpansionAuthorityRootAnchor(rootDir)
		if err != nil {
			t.Fatal(err)
		}
		keyring, err := readExpansionAuthorityRootKeyring(rootDir)
		if err != nil {
			t.Fatal(err)
		}
		secretBytes, _ := os.ReadFile(filepath.Join(rootDir, rootSigningSecretName))
		anchorBytes, _ := os.ReadFile(filepath.Join(rootDir, rootPublicAnchorName))
		keyringBytes, _ := os.ReadFile(filepath.Join(rootDir, rootPublicKeyringName))
		pinBytes, _ := os.ReadFile(filepath.Join(installationDir, authorityTrustPinName))
		checkpointBytes, _ := os.ReadFile(filepath.Join(installationDir, authorityCheckpointName))
		return secret, anchor, keyring, secretBytes, anchorBytes, keyringBytes, pinBytes, checkpointBytes
	}
	oldSecret, oldAnchor, oldKeyring, oldSecretBytes, oldAnchorBytes, oldKeyringBytes, oldPinBytes, oldCheckpointBytes := readState()
	if err := RotateExpansionAuthorityRoot(); err != nil {
		t.Fatal(err)
	}
	newSecret, _, _, newSecretBytes, newAnchorBytes, newKeyringBytes, newPinBytes, newCheckpointBytes := readState()
	journal := expansionAuthorityRootRotationJournal{
		Version: expansionAuthorityRootLifecycleVersion, Operation: "rotate", OldSecret: oldSecretBytes, OldAnchor: oldAnchorBytes, OldKeyring: oldKeyringBytes,
		NewSecret: newSecretBytes, NewAnchor: newAnchorBytes, NewKeyring: newKeyringBytes, OldPin: oldPinBytes, OldCheckpoint: oldCheckpointBytes,
		NewPin: newPinBytes, NewCheckpoint: newCheckpointBytes, NewKeyID: newSecret.KeyID, NewEpoch: newSecret.Epoch, CreatedAtUTC: time.Now().UTC().Format(time.RFC3339Nano),
	}
	if oldSecret.KeyID != oldAnchor.CurrentKeyID || oldKeyring.CurrentKeyID != oldSecret.KeyID {
		t.Fatal("invalid old authority fixture")
	}
	if err := writeProtectedAuthorityJSON(filepath.Join(rootDir, rootRotationJournalName), journal); err != nil {
		t.Fatal(err)
	}
	if err := recoverExpansionAuthorityRootRotationLocked(rootDir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(rootDir, rootRotationJournalName)); !os.IsNotExist(err) {
		t.Fatalf("exact-new recovery retained its journal: %v", err)
	}
	afterCheckpoint, _ := os.ReadFile(filepath.Join(installationDir, authorityCheckpointName))
	if !bytes.Equal(afterCheckpoint, newCheckpointBytes) {
		t.Fatal("exact-new recovery rolled back the monotonic checkpoint")
	}
	if _, _, err := loadExpansionAuthorityRootPublicKey(newSecret.KeyID); err != nil {
		t.Fatalf("exact-new recovery did not retain the new root: %v", err)
	}
}

func TestExpansionAuthorityRootSuccessfulRotationRejectsImmediateOldRootReplay(t *testing.T) {
	rootDir := isolatedExpansionAuthorityRoot(t)
	_ = signedExpansionAuthorityTrust(t)
	oldSecret, _ := os.ReadFile(filepath.Join(rootDir, rootSigningSecretName))
	oldAnchor, _ := os.ReadFile(filepath.Join(rootDir, rootPublicAnchorName))
	oldKeyring, _ := os.ReadFile(filepath.Join(rootDir, rootPublicKeyringName))
	if err := RotateExpansionAuthorityRoot(); err != nil {
		t.Fatal(err)
	}
	for path, payload := range map[string][]byte{
		filepath.Join(rootDir, rootSigningSecretName): oldSecret,
		filepath.Join(rootDir, rootPublicAnchorName):  oldAnchor,
		filepath.Join(rootDir, rootPublicKeyringName): oldKeyring,
	} {
		if err := os.WriteFile(path, payload, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := loadExpansionAuthorityRootPublicKey(""); err == nil {
		t.Fatal("successful rotation allowed an immediate full old-root replay before another public read")
	}
}

func TestExpansionAuthorityRootExportImportReprotect(t *testing.T) {
	sourceRoot := isolatedExpansionAuthorityRoot(t)
	trust := signedExpansionAuthorityTrust(t)
	wrappingKey := []byte("0123456789abcdef0123456789abcdef")
	bundlePath := filepath.Join(t.TempDir(), "authority-root-export.json")
	if err := ExportExpansionAuthorityRoot(bundlePath, wrappingKey); err != nil {
		t.Fatal(err)
	}
	bundleBytes, err := os.ReadFile(bundlePath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(bundleBytes), "private_key") {
		t.Fatal("encrypted export exposed plaintext secret fields")
	}
	destinationRoot := t.TempDir()
	useExpansionAuthorityRootForTest(t, destinationRoot)
	if err := ImportExpansionAuthorityRoot(bundlePath, wrappingKey); err != nil {
		t.Fatal(err)
	}
	if err := VerifyExpansionPublicationCertificate(trust); err != nil {
		t.Fatalf("imported public keyring could not verify existing certificate: %v", err)
	}
	var secret expansionAuthorityRootSecret
	if err := readProtectedAuthorityJSON(filepath.Join(destinationRoot, rootSigningSecretName), &secret); err != nil {
		t.Fatalf("import did not re-protect secret for the current account: %v", err)
	}
	if _, err := os.Stat(filepath.Join(sourceRoot, rootMigrationAuditName)); err != nil {
		t.Fatalf("export was not audited: %v", err)
	}
	if _, err := os.Stat(filepath.Join(destinationRoot, rootMigrationAuditName)); err != nil {
		t.Fatalf("import was not audited: %v", err)
	}
}

func TestExpansionAuthorityRootImportRejectsPublicStateRollbackWithoutSecret(t *testing.T) {
	rootDir := isolatedExpansionAuthorityRoot(t)
	_ = signedExpansionAuthorityTrust(t)
	wrappingKey := []byte("0123456789abcdef0123456789abcdef")
	oldBundle := filepath.Join(t.TempDir(), "old-authority.json")
	if err := ExportExpansionAuthorityRoot(oldBundle, wrappingKey); err != nil {
		t.Fatal(err)
	}
	oldKeyring, err := readExpansionAuthorityRootKeyring(rootDir)
	if err != nil {
		t.Fatal(err)
	}
	oldKeyID := oldKeyring.CurrentKeyID
	if err := RotateExpansionAuthorityRoot(); err != nil {
		t.Fatal(err)
	}
	if err := RevokeExpansionAuthorityRootKey(oldKeyID); err != nil {
		t.Fatal(err)
	}
	anchorBefore, _ := os.ReadFile(filepath.Join(rootDir, rootPublicAnchorName))
	keyringBefore, _ := os.ReadFile(filepath.Join(rootDir, rootPublicKeyringName))
	if err := os.Remove(filepath.Join(rootDir, rootSigningSecretName)); err != nil {
		t.Fatal(err)
	}
	if err := ImportExpansionAuthorityRoot(oldBundle, wrappingKey); err == nil {
		t.Fatal("old bundle rolled back installed public epoch/revocation after secret became unreadable")
	}
	anchorAfter, _ := os.ReadFile(filepath.Join(rootDir, rootPublicAnchorName))
	keyringAfter, _ := os.ReadFile(filepath.Join(rootDir, rootPublicKeyringName))
	if !bytes.Equal(anchorBefore, anchorAfter) || !bytes.Equal(keyringBefore, keyringAfter) {
		t.Fatal("rejected import modified installed public authority state")
	}
}

func TestExpansionAuthorityRootFreshImportFaultIsRetryable(t *testing.T) {
	isolatedExpansionAuthorityRoot(t)
	_ = signedExpansionAuthorityTrust(t)
	wrappingKey := []byte("0123456789abcdef0123456789abcdef")
	bundlePath := filepath.Join(t.TempDir(), "authority-root-export.json")
	if err := ExportExpansionAuthorityRoot(bundlePath, wrappingKey); err != nil {
		t.Fatal(err)
	}
	destinationRoot := useExpansionAuthorityRootForTest(t, filepath.Join(t.TempDir(), "fresh-authority"))
	faulted := false
	expansionAuthorityWriteFault = func(path, stage string) error {
		if !faulted && filepath.Base(path) == rootSigningSecretName && stage == "after_sync" {
			faulted = true
			return errors.New("injected fresh import failure")
		}
		return nil
	}
	t.Cleanup(func() { expansionAuthorityWriteFault = nil })
	if err := ImportExpansionAuthorityRoot(bundlePath, wrappingKey); err == nil {
		t.Fatal("fresh import fault did not fire")
	}
	expansionAuthorityWriteFault = nil
	if _, err := os.Stat(filepath.Join(destinationRoot, rootRotationJournalName)); !os.IsNotExist(err) {
		t.Fatalf("fresh import rollback retained its journal: %v", err)
	}
	if err := ImportExpansionAuthorityRoot(bundlePath, wrappingKey); err != nil {
		t.Fatalf("fresh import was not retryable: %v", err)
	}
	anchor, err := readExpansionAuthorityRootAnchor(destinationRoot)
	if err != nil {
		t.Fatal(err)
	}
	keyring, err := readExpansionAuthorityRootKeyring(destinationRoot)
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyAndAdvanceExpansionAuthorityCheckpoint(destinationRoot, anchor, keyring); err != nil {
		t.Fatalf("fresh import did not atomically install its checkpoint: %v", err)
	}
}

func TestExpansionAuthorityRootRotationFaultStagesRecoverConsistently(t *testing.T) {
	for _, target := range []string{rootSigningSecretName, rootPublicKeyringName, rootPublicAnchorName, authorityCheckpointName} {
		t.Run(target, func(t *testing.T) {
			rootDir := isolatedExpansionAuthorityRoot(t)
			_ = signedExpansionAuthorityTrust(t)
			faulted := false
			expansionAuthorityWriteFault = func(path, stage string) error {
				if !faulted && filepath.Base(path) == target && stage == "after_sync" {
					faulted = true
					return errors.New("injected authority transaction stage failure")
				}
				return nil
			}
			t.Cleanup(func() { expansionAuthorityWriteFault = nil })
			if err := RotateExpansionAuthorityRoot(); err == nil || !faulted {
				t.Fatalf("authority transaction fault did not fire for %s", target)
			}
			expansionAuthorityWriteFault = nil
			if err := recoverExpansionAuthorityRootRotationLocked(rootDir); err != nil {
				t.Fatal(err)
			}
			anchor, err := readExpansionAuthorityRootAnchor(rootDir)
			if err != nil {
				t.Fatal(err)
			}
			keyring, err := readExpansionAuthorityRootKeyring(rootDir)
			if err != nil {
				t.Fatal(err)
			}
			if anchor.CurrentKeyID != keyring.CurrentKeyID || anchor.CurrentEpoch != keyring.CurrentEpoch {
				t.Fatal("recovered authority checkpoint/root generation diverged")
			}
			if _, err := os.Stat(filepath.Join(rootDir, rootRotationJournalName)); !os.IsNotExist(err) {
				t.Fatalf("recovery retained journal for %s: %v", target, err)
			}
		})
	}
}

func TestExpansionAuthorityProductionPathIgnoresEnvironmentAndArgv(t *testing.T) {
	previousRoot := expansionAuthorityRootDirResolver
	previousInstallation := expansionAuthorityInstallationDirResolver
	expansionAuthorityRootDirResolver = platformExpansionAuthorityRootDir
	expansionAuthorityInstallationDirResolver = platformExpansionAuthorityInstallationDir
	t.Cleanup(func() {
		expansionAuthorityRootDirResolver = previousRoot
		expansionAuthorityInstallationDirResolver = previousInstallation
	})
	wantRoot, _ := platformExpansionAuthorityRootDir()
	wantInstallation, _ := platformExpansionAuthorityInstallationDir(wantRoot)
	t.Setenv("AINOVEL_AUTHORITY_ROOT", filepath.Join(t.TempDir(), "attacker-root"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "attacker-xdg"))
	t.Setenv("APPDATA", filepath.Join(t.TempDir(), "attacker-appdata"))
	oldArgv := os.Args
	os.Args = []string{"renamed-release.test.exe"}
	t.Cleanup(func() { os.Args = oldArgv })
	gotRoot, err := expansionAuthorityRootDir()
	if err != nil || gotRoot != wantRoot {
		t.Fatalf("environment/argv changed production authority root: got %q want %q err=%v", gotRoot, wantRoot, err)
	}
	gotInstallation, err := expansionAuthorityInstallationDir(gotRoot)
	if err != nil || gotInstallation != wantInstallation {
		t.Fatalf("environment/argv changed production authority installation: got %q want %q err=%v", gotInstallation, wantInstallation, err)
	}
}

func TestExpansionAuthorityDeletedInstallationCannotBeRebootstrappedByRuntimeAccount(t *testing.T) {
	rootDir := isolatedExpansionAuthorityRoot(t)
	if _, err := InitializeExpansionAuthorityRoot(); err != nil {
		t.Fatal(err)
	}
	installationDir, err := expansionAuthorityInstallationDir(rootDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(rootDir); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(installationDir); err != nil {
		t.Fatal(err)
	}
	expansionAuthorityBootstrapVerifier = func() error {
		return fmt.Errorf("administrator bootstrap required")
	}
	if _, err := InitializeExpansionAuthorityRoot(); err == nil || !strings.Contains(err.Error(), "administrator bootstrap required") {
		t.Fatalf("runtime account established a replacement trust domain: %v", err)
	}
	if _, err := os.Lstat(rootDir); !os.IsNotExist(err) {
		t.Fatalf("failed bootstrap recreated authority root: %v", err)
	}
	if _, err := os.Lstat(installationDir); !os.IsNotExist(err) {
		t.Fatalf("failed bootstrap recreated installation anchor: %v", err)
	}
}

func isolatedExpansionAuthorityRoot(t *testing.T) string {
	t.Helper()
	return useExpansionAuthorityRootForTest(t, t.TempDir())
}

func signedExpansionAuthorityTrust(t *testing.T) ExpansionPublicationTrust {
	t.Helper()
	root, privateKey, err := loadOrCreateExpansionAuthorityRoot()
	if err != nil {
		t.Fatal(err)
	}
	projectPublic, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	trust := ExpansionPublicationTrust{
		Version: expansionPublicationReceiptVersion, Algorithm: expansionAuthorityAlgorithm,
		ProjectID: "authority-lifecycle-test", ProjectInstance: strings.Repeat("1", 64), ProjectRootHash: strings.Repeat("2", 64),
		KeyID: "project-lifecycle-test", Epoch: 1, RootKeyID: root.KeyID, PublicKey: base64.StdEncoding.EncodeToString(projectPublic),
	}
	payload, err := expansionPublicationTrustPayload(trust)
	if err != nil {
		t.Fatal(err)
	}
	trust.Certificate = base64.StdEncoding.EncodeToString(ed25519.Sign(privateKey, payload))
	return trust
}

func bytesContainPrivateAuthorityField(t *testing.T, path string) bool {
	t.Helper()
	payload, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var decoded any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatal(err)
	}
	return strings.Contains(string(payload), "private_key")
}
