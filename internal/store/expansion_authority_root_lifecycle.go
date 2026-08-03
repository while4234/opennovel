package store

import (
	"crypto/aes"
	"crypto/cipher"
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
	"slices"
	"sort"
	"strings"
	"time"
)

const (
	expansionAuthorityRootLifecycleVersion = 2
	rootSigningSecretName                  = "root-signing-secret.json"
	rootPublicKeyringName                  = "root-keyring.json"
	rootPublicAnchorName                   = "root-trust-anchor.json"
	rootRotationJournalName                = "root-rotation-journal.json"
	rootMigrationAuditName                 = "root-migration-audit.jsonl"
)

type expansionAuthorityRootSecret struct {
	Version    int    `json:"version"`
	KeyID      string `json:"key_id"`
	Epoch      uint64 `json:"epoch"`
	PrivateKey string `json:"private_key"`
	PublicKey  string `json:"public_key"`
	AnchorHash string `json:"anchor_hash"`
}

type expansionAuthorityRootKey struct {
	KeyID              string `json:"key_id"`
	Epoch              uint64 `json:"epoch"`
	PublicKey          string `json:"public_key"`
	State              string `json:"state"`
	AcceptThroughEpoch uint64 `json:"accept_through_epoch,omitempty"`
	SignedByKeyID      string `json:"signed_by_key_id"`
	CrossSignature     string `json:"cross_signature"`
}

type expansionAuthorityRootAnchor struct {
	Version      int    `json:"version"`
	KeyID        string `json:"key_id"`
	Epoch        uint64 `json:"epoch"`
	PublicKey    string `json:"public_key"`
	CurrentKeyID string `json:"current_key_id"`
	CurrentEpoch uint64 `json:"current_epoch"`
}

type expansionAuthorityRootKeyring struct {
	Version      int                         `json:"version"`
	Generation   uint64                      `json:"generation"`
	CurrentKeyID string                      `json:"current_key_id"`
	CurrentEpoch uint64                      `json:"current_epoch"`
	Keys         []expansionAuthorityRootKey `json:"keys"`
	Signature    string                      `json:"signature"`
}

type expansionAuthorityRootRotationJournal struct {
	Version       int    `json:"version"`
	Operation     string `json:"operation"`
	OldSecret     []byte `json:"old_secret"`
	OldAnchor     []byte `json:"old_anchor"`
	OldKeyring    []byte `json:"old_keyring"`
	NewSecret     []byte `json:"new_secret"`
	NewAnchor     []byte `json:"new_anchor"`
	NewKeyring    []byte `json:"new_keyring"`
	OldPin        []byte `json:"old_pin,omitempty"`
	OldCheckpoint []byte `json:"old_checkpoint,omitempty"`
	NewPin        []byte `json:"new_pin"`
	NewCheckpoint []byte `json:"new_checkpoint"`
	FreshInstall  bool   `json:"fresh_install,omitempty"`
	NewKeyID      string `json:"new_key_id"`
	NewEpoch      uint64 `json:"new_epoch"`
	CreatedAtUTC  string `json:"created_at_utc"`
}

type expansionAuthorityRootMigrationBundle struct {
	Version    int    `json:"version"`
	Nonce      string `json:"nonce"`
	Ciphertext string `json:"ciphertext"`
	Digest     string `json:"digest"`
}

type expansionAuthorityRootMigrationPayload struct {
	Version    int                           `json:"version"`
	Secret     expansionAuthorityRootSecret  `json:"secret"`
	Anchor     expansionAuthorityRootAnchor  `json:"anchor"`
	Keyring    expansionAuthorityRootKeyring `json:"keyring"`
	ExportedAt string                        `json:"exported_at"`
}

// InitializeExpansionAuthorityRoot performs the one-time administrator trust
// bootstrap. Normal runtime signing fails closed when this ceremony has not
// been completed.
func InitializeExpansionAuthorityRoot() (string, error) {
	if err := expansionAuthorityBootstrapVerifier(); err != nil {
		return "", err
	}
	var keyID string
	err := withExpansionAuthorityRootLifecycleOperation(true, func(string) error {
		expansionAuthorityInitializationAllowed = true
		defer func() { expansionAuthorityInitializationAllowed = false }()
		secret, err := loadOrCreateExpansionAuthorityRootSecretLocked()
		keyID = secret.KeyID
		return err
	})
	return keyID, err
}

func loadOrCreateExpansionAuthorityRootSecretLocked() (expansionAuthorityRootSecret, error) {
	rootDir, err := expansionAuthorityRootDir()
	if err != nil {
		return expansionAuthorityRootSecret{}, err
	}
	if err := prepareExpansionAuthorityRootDir(rootDir); err != nil {
		return expansionAuthorityRootSecret{}, err
	}
	if err := recoverExpansionAuthorityRootRotationLocked(rootDir); err != nil {
		return expansionAuthorityRootSecret{}, err
	}
	secretPath := filepath.Join(rootDir, rootSigningSecretName)
	var secret expansionAuthorityRootSecret
	if err := readProtectedAuthorityJSON(secretPath, &secret); err == nil {
		if secret.AnchorHash == "" {
			if err := migratePreAnchorExpansionAuthorityRoot(rootDir, &secret); err != nil {
				return secret, err
			}
		}
		if _, err := decodeExpansionAuthorityRootSecret(secret); err != nil {
			return secret, err
		}
		keyring, err := readExpansionAuthorityRootKeyring(rootDir)
		if err != nil {
			return secret, fmt.Errorf("trusted application public keyring is unavailable: %w", err)
		}
		if keyring.CurrentKeyID != secret.KeyID || keyring.CurrentEpoch != secret.Epoch {
			return secret, fmt.Errorf("application root secret/keyring generation mismatch")
		}
		return secret, nil
	} else if !os.IsNotExist(err) {
		return secret, err
	}

	// A public keyring without its secret is deletion/corruption, not authority
	// initialization. Never silently replace an established trust anchor.
	if _, err := os.Stat(filepath.Join(rootDir, rootPublicKeyringName)); err == nil {
		return secret, fmt.Errorf("application root signing secret is unavailable")
	} else if !os.IsNotExist(err) {
		return secret, err
	}
	if _, err := os.Stat(filepath.Join(rootDir, rootPublicAnchorName)); err == nil {
		return secret, fmt.Errorf("application root signing secret is unavailable while a trust anchor exists")
	} else if !os.IsNotExist(err) {
		return secret, err
	}

	legacyPath := filepath.Join(rootDir, "root-authority.json")
	var legacy expansionAuthorityRootRecord
	if err := readProtectedAuthorityJSON(legacyPath, &legacy); err == nil {
		privateKey, err := decodeExpansionAuthorityRoot(legacy)
		if err != nil {
			return secret, err
		}
		secret = expansionAuthorityRootSecret{
			Version: expansionAuthorityRootLifecycleVersion, KeyID: legacy.KeyID, Epoch: 1,
			PrivateKey: base64.StdEncoding.EncodeToString(privateKey), PublicKey: legacy.PublicKey,
		}
		secret.AnchorHash = expansionAuthorityRootAnchorHash(expansionAuthorityRootAnchor{Version: expansionAuthorityRootLifecycleVersion, KeyID: legacy.KeyID, Epoch: 1, PublicKey: legacy.PublicKey})
		if err := initializeExpansionAuthorityRootPair(rootDir, secret); err != nil {
			return secret, err
		}
		if err := os.Remove(legacyPath); err != nil && !os.IsNotExist(err) {
			return secret, fmt.Errorf("remove migrated legacy application root: %w", err)
		}
		return secret, nil
	} else if !os.IsNotExist(err) {
		return secret, err
	}
	if !expansionAuthorityInitializationAllowed {
		return secret, fmt.Errorf("application authority root is not initialized; an administrator must run the release-managed authority bootstrap")
	}

	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return secret, err
	}
	secret = newExpansionAuthorityRootSecret(1, publicKey, privateKey)
	if err := initializeExpansionAuthorityRootPair(rootDir, secret); err != nil {
		return secret, err
	}
	return secret, nil
}

func newExpansionAuthorityRootSecret(epoch uint64, publicKey ed25519.PublicKey, privateKey ed25519.PrivateKey) expansionAuthorityRootSecret {
	digest := sha256.Sum256(publicKey)
	secret := expansionAuthorityRootSecret{
		Version: expansionAuthorityRootLifecycleVersion,
		KeyID:   "root-" + hex.EncodeToString(digest[:12]), Epoch: epoch,
		PrivateKey: base64.StdEncoding.EncodeToString(privateKey),
		PublicKey:  base64.StdEncoding.EncodeToString(publicKey),
	}
	if epoch == 1 {
		secret.AnchorHash = expansionAuthorityRootAnchorHash(expansionAuthorityRootAnchor{Version: expansionAuthorityRootLifecycleVersion, KeyID: secret.KeyID, Epoch: 1, PublicKey: secret.PublicKey})
	}
	return secret
}

func initializeExpansionAuthorityRootPair(rootDir string, secret expansionAuthorityRootSecret) error {
	if err := requireExpansionAuthorityInstallationEmpty(rootDir); err != nil {
		return err
	}
	privateKey, err := decodeExpansionAuthorityRootSecret(secret)
	if err != nil {
		return err
	}
	entry := expansionAuthorityRootKey{
		KeyID: secret.KeyID, Epoch: secret.Epoch, PublicKey: secret.PublicKey,
		State: "active", SignedByKeyID: secret.KeyID,
	}
	entryPayload, _ := expansionAuthorityRootCrossSigningPayload(entry)
	entry.CrossSignature = base64.StdEncoding.EncodeToString(ed25519.Sign(privateKey, entryPayload))
	keyring := expansionAuthorityRootKeyring{
		Version: expansionAuthorityRootLifecycleVersion, Generation: 1,
		CurrentKeyID: secret.KeyID, CurrentEpoch: secret.Epoch, Keys: []expansionAuthorityRootKey{entry},
	}
	if err := signExpansionAuthorityRootKeyring(&keyring, privateKey); err != nil {
		return err
	}
	anchor := expansionAuthorityRootAnchor{Version: expansionAuthorityRootLifecycleVersion, KeyID: secret.KeyID, Epoch: secret.Epoch, PublicKey: secret.PublicKey, CurrentKeyID: secret.KeyID, CurrentEpoch: secret.Epoch}
	return replaceExpansionAuthorityRootPairLocked(rootDir, "init", secret, anchor, keyring, secret, anchor, keyring)
}

func decodeExpansionAuthorityRootSecret(secret expansionAuthorityRootSecret) (ed25519.PrivateKey, error) {
	privateKey, privateErr := base64.StdEncoding.DecodeString(secret.PrivateKey)
	publicKey, publicErr := base64.StdEncoding.DecodeString(secret.PublicKey)
	if secret.Version != expansionAuthorityRootLifecycleVersion || secret.Epoch == 0 || !strings.HasPrefix(secret.KeyID, "root-") || len(secret.AnchorHash) != 64 ||
		privateErr != nil || publicErr != nil || len(privateKey) != ed25519.PrivateKeySize || len(publicKey) != ed25519.PublicKeySize ||
		!ed25519.PublicKey(privateKey[32:]).Equal(ed25519.PublicKey(publicKey)) {
		return nil, fmt.Errorf("trusted application root signing secret is invalid")
	}
	return ed25519.PrivateKey(privateKey), nil
}

func loadExpansionAuthorityRootPublicKey(keyID string) (expansionAuthorityRootRecord, ed25519.PublicKey, error) {
	var root expansionAuthorityRootRecord
	var publicKey ed25519.PublicKey
	err := withExpansionAuthorityRootLifecycleOperation(false, func(string) error {
		var err error
		root, publicKey, err = loadExpansionAuthorityRootPublicKeyLocked(keyID)
		return err
	})
	return root, publicKey, err
}

func loadExpansionAuthorityRootPublicKeyLocked(keyID string) (expansionAuthorityRootRecord, ed25519.PublicKey, error) {
	rootDir, err := expansionAuthorityRootDir()
	if err != nil {
		return expansionAuthorityRootRecord{}, nil, err
	}
	if err := verifyExpansionAuthorityRootDir(rootDir); err != nil {
		return expansionAuthorityRootRecord{}, nil, err
	}
	if _, err := os.Stat(filepath.Join(rootDir, rootRotationJournalName)); err == nil {
		return expansionAuthorityRootRecord{}, nil, fmt.Errorf("application root rotation recovery is required")
	} else if !os.IsNotExist(err) {
		return expansionAuthorityRootRecord{}, nil, err
	}
	keyring, err := readExpansionAuthorityRootKeyring(rootDir)
	if err != nil {
		return expansionAuthorityRootRecord{}, nil, fmt.Errorf("trusted application public keyring is unavailable: %w", err)
	}
	if keyID == "" {
		keyID = keyring.CurrentKeyID
	}
	for _, key := range keyring.Keys {
		if key.KeyID != keyID {
			continue
		}
		if key.State == "revoked" || (key.State == "grace" && keyring.CurrentEpoch > key.AcceptThroughEpoch) {
			return expansionAuthorityRootRecord{}, nil, fmt.Errorf("trusted application root key is revoked or expired")
		}
		publicKey, err := base64.StdEncoding.DecodeString(key.PublicKey)
		if err != nil || len(publicKey) != ed25519.PublicKeySize {
			return expansionAuthorityRootRecord{}, nil, fmt.Errorf("trusted application root public key is invalid")
		}
		return expansionAuthorityRootRecord{Version: expansionAuthorityRootLifecycleVersion, KeyID: key.KeyID, PublicKey: key.PublicKey}, ed25519.PublicKey(publicKey), nil
	}
	return expansionAuthorityRootRecord{}, nil, fmt.Errorf("trusted application root key %q is unavailable", keyID)
}

func readExpansionAuthorityRootKeyring(rootDir string) (expansionAuthorityRootKeyring, error) {
	keyring, err := readExpansionAuthorityRootKeyringUnanchored(rootDir)
	if err != nil {
		return keyring, err
	}
	anchor, err := readExpansionAuthorityRootAnchor(rootDir)
	if err != nil {
		return keyring, err
	}
	first := keyring.Keys[0]
	if first.KeyID != anchor.KeyID || first.Epoch != anchor.Epoch || first.PublicKey != anchor.PublicKey || keyring.CurrentKeyID != anchor.CurrentKeyID || keyring.CurrentEpoch != anchor.CurrentEpoch {
		return keyring, fmt.Errorf("trusted application public keyring does not descend from the installed anchor")
	}
	if err := verifyAndAdvanceExpansionAuthorityCheckpoint(rootDir, anchor, keyring); err != nil {
		return keyring, err
	}
	return keyring, nil
}

func readExpansionAuthorityRootKeyringUnanchored(rootDir string) (expansionAuthorityRootKeyring, error) {
	var keyring expansionAuthorityRootKeyring
	payload, err := os.ReadFile(filepath.Join(rootDir, rootPublicKeyringName))
	if err != nil {
		return keyring, err
	}
	if err := json.Unmarshal(payload, &keyring); err != nil {
		return keyring, err
	}
	if err := validateExpansionAuthorityRootKeyring(keyring); err != nil {
		return keyring, err
	}
	return keyring, nil
}

func migratePreAnchorExpansionAuthorityRoot(rootDir string, secret *expansionAuthorityRootSecret) error {
	keyring, err := readExpansionAuthorityRootKeyringUnanchored(rootDir)
	if err != nil || keyring.Generation != 1 || keyring.CurrentEpoch != 1 || len(keyring.Keys) != 1 ||
		keyring.CurrentKeyID != secret.KeyID || keyring.Keys[0].PublicKey != secret.PublicKey {
		return fmt.Errorf("legacy application root cannot be anchored safely")
	}
	anchor := expansionAuthorityRootAnchor{Version: expansionAuthorityRootLifecycleVersion, KeyID: secret.KeyID, Epoch: 1, PublicKey: secret.PublicKey, CurrentKeyID: secret.KeyID, CurrentEpoch: 1}
	anchorBytes, _ := json.MarshalIndent(anchor, "", "  ")
	if err := writeAuthorityPublicAtomic(filepath.Join(rootDir, rootPublicAnchorName), anchorBytes); err != nil {
		return err
	}
	secret.AnchorHash = expansionAuthorityRootAnchorHash(anchor)
	if err := writeProtectedAuthorityJSON(filepath.Join(rootDir, rootSigningSecretName), *secret); err != nil {
		return err
	}
	return initializeExpansionAuthorityInstallation(rootDir, anchor, keyring)
}

func expansionAuthorityRootAnchorHash(anchor expansionAuthorityRootAnchor) string {
	payload, _ := json.Marshal(struct {
		Version   int    `json:"version"`
		KeyID     string `json:"key_id"`
		Epoch     uint64 `json:"epoch"`
		PublicKey string `json:"public_key"`
	}{anchor.Version, anchor.KeyID, anchor.Epoch, anchor.PublicKey})
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:])
}

func readExpansionAuthorityRootAnchor(rootDir string) (expansionAuthorityRootAnchor, error) {
	var anchor expansionAuthorityRootAnchor
	payload, err := os.ReadFile(filepath.Join(rootDir, rootPublicAnchorName))
	if err != nil {
		return anchor, fmt.Errorf("trusted application root anchor is unavailable: %w", err)
	}
	if err := json.Unmarshal(payload, &anchor); err != nil {
		return anchor, err
	}
	if anchor.CurrentKeyID == "" && anchor.CurrentEpoch == 0 {
		anchor.CurrentKeyID, anchor.CurrentEpoch = anchor.KeyID, anchor.Epoch
	}
	publicKey, decodeErr := base64.StdEncoding.DecodeString(anchor.PublicKey)
	if anchor.Version != expansionAuthorityRootLifecycleVersion || anchor.Epoch != 1 || anchor.CurrentEpoch < anchor.Epoch || anchor.CurrentKeyID == "" || !strings.HasPrefix(anchor.KeyID, "root-") || decodeErr != nil || len(publicKey) != ed25519.PublicKeySize {
		return anchor, fmt.Errorf("trusted application root anchor is invalid")
	}
	if err := verifyExpansionAuthorityTrustPin(rootDir, anchor); err != nil {
		return anchor, err
	}
	return anchor, nil
}

func validateExpansionAuthorityRootKeyring(keyring expansionAuthorityRootKeyring) error {
	if keyring.Version != expansionAuthorityRootLifecycleVersion || keyring.Generation == 0 || keyring.CurrentEpoch == 0 || len(keyring.Keys) == 0 {
		return fmt.Errorf("trusted application public keyring is invalid")
	}
	keys := make(map[string]ed25519.PublicKey, len(keyring.Keys))
	var active int
	var previousEpoch uint64
	for _, key := range keyring.Keys {
		decoded, err := base64.StdEncoding.DecodeString(key.PublicKey)
		if err != nil || len(decoded) != ed25519.PublicKeySize || key.Epoch == 0 || key.Epoch <= previousEpoch || keys[key.KeyID] != nil {
			return fmt.Errorf("trusted application public keyring entry is invalid")
		}
		if key.State != "active" && key.State != "grace" && key.State != "revoked" {
			return fmt.Errorf("trusted application public keyring state is invalid")
		}
		if key.State == "active" {
			active++
			if key.KeyID != keyring.CurrentKeyID || key.Epoch != keyring.CurrentEpoch {
				return fmt.Errorf("trusted application public keyring active key is inconsistent")
			}
		}
		keys[key.KeyID] = ed25519.PublicKey(decoded)
		previousEpoch = key.Epoch
	}
	if active != 1 {
		return fmt.Errorf("trusted application public keyring must contain exactly one active key")
	}
	for _, key := range keyring.Keys {
		signer := keys[key.SignedByKeyID]
		signature, err := base64.StdEncoding.DecodeString(key.CrossSignature)
		payload, payloadErr := expansionAuthorityRootCrossSigningPayload(key)
		if signer == nil || err != nil || payloadErr != nil || len(signature) != ed25519.SignatureSize || !ed25519.Verify(signer, payload, signature) {
			return fmt.Errorf("trusted application public keyring cross-signature is invalid")
		}
	}
	current := keys[keyring.CurrentKeyID]
	signature, err := base64.StdEncoding.DecodeString(keyring.Signature)
	payload, payloadErr := expansionAuthorityRootKeyringSigningPayload(keyring)
	if err != nil || payloadErr != nil || len(signature) != ed25519.SignatureSize || !ed25519.Verify(current, payload, signature) {
		return fmt.Errorf("trusted application public keyring signature is invalid")
	}
	return nil
}

func expansionAuthorityRootCrossSigningPayload(key expansionAuthorityRootKey) ([]byte, error) {
	return json.Marshal(struct {
		KeyID         string `json:"key_id"`
		Epoch         uint64 `json:"epoch"`
		PublicKey     string `json:"public_key"`
		SignedByKeyID string `json:"signed_by_key_id"`
	}{key.KeyID, key.Epoch, key.PublicKey, key.SignedByKeyID})
}

func expansionAuthorityRootKeyringSigningPayload(keyring expansionAuthorityRootKeyring) ([]byte, error) {
	keyring.Signature = ""
	return json.Marshal(keyring)
}

func signExpansionAuthorityRootKeyring(keyring *expansionAuthorityRootKeyring, privateKey ed25519.PrivateKey) error {
	payload, err := expansionAuthorityRootKeyringSigningPayload(*keyring)
	if err != nil {
		return err
	}
	keyring.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(privateKey, payload))
	return nil
}

func writeExpansionAuthorityRootKeyring(rootDir string, keyring expansionAuthorityRootKeyring) error {
	payload, err := json.MarshalIndent(keyring, "", "  ")
	if err != nil {
		return err
	}
	return writeAuthorityPublicAtomic(filepath.Join(rootDir, rootPublicKeyringName), payload)
}

func writeAuthorityPublicAtomic(path string, payload []byte) error {
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
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0o644); err != nil {
		_ = tmp.Close()
		return err
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
	return nil
}

// RotateExpansionAuthorityRoot starts a new application-root epoch. The
// preceding root remains valid for one epoch so already-issued project
// certificates can be deliberately re-signed; older roots are revoked.
func RotateExpansionAuthorityRoot() error {
	return withExpansionAuthorityRootLifecycleOperation(false, func(rootDir string) error {
		oldSecret, err := loadOrCreateExpansionAuthorityRootSecretLockedWithoutMutex()
		if err != nil {
			return err
		}
		oldPrivate, err := decodeExpansionAuthorityRootSecret(oldSecret)
		if err != nil {
			return err
		}
		oldKeyring, err := readExpansionAuthorityRootKeyring(rootDir)
		if err != nil {
			return err
		}
		oldAnchor, err := readExpansionAuthorityRootAnchor(rootDir)
		if err != nil {
			return err
		}
		publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return err
		}
		newSecret := newExpansionAuthorityRootSecret(oldSecret.Epoch+1, publicKey, privateKey)
		newSecret.AnchorHash = oldSecret.AnchorHash
		newKeyring := oldKeyring
		newKeyring.Generation++
		newKeyring.CurrentKeyID = newSecret.KeyID
		newKeyring.CurrentEpoch = newSecret.Epoch
		for i := range newKeyring.Keys {
			switch newKeyring.Keys[i].State {
			case "active":
				newKeyring.Keys[i].State = "grace"
				newKeyring.Keys[i].AcceptThroughEpoch = newSecret.Epoch
			case "grace":
				if newKeyring.Keys[i].AcceptThroughEpoch < newSecret.Epoch {
					newKeyring.Keys[i].State = "revoked"
				}
			}
		}
		newEntry := expansionAuthorityRootKey{
			KeyID: newSecret.KeyID, Epoch: newSecret.Epoch, PublicKey: newSecret.PublicKey,
			State: "active", SignedByKeyID: oldSecret.KeyID,
		}
		entryPayload, _ := expansionAuthorityRootCrossSigningPayload(newEntry)
		newEntry.CrossSignature = base64.StdEncoding.EncodeToString(ed25519.Sign(oldPrivate, entryPayload))
		newKeyring.Keys = append(newKeyring.Keys, newEntry)
		if err := signExpansionAuthorityRootKeyring(&newKeyring, privateKey); err != nil {
			return err
		}
		newAnchor := oldAnchor
		newAnchor.CurrentKeyID = newSecret.KeyID
		newAnchor.CurrentEpoch = newSecret.Epoch
		return replaceExpansionAuthorityRootPairLocked(rootDir, "rotate", oldSecret, oldAnchor, oldKeyring, newSecret, newAnchor, newKeyring)
	})
}

// RevokeExpansionAuthorityRootKey immediately closes a non-current root grace
// window. Revocation is signed by the current root and installed through the
// same crash-safe authority transaction as rotation.
func RevokeExpansionAuthorityRootKey(keyID string) error {
	keyID = strings.TrimSpace(keyID)
	if keyID == "" {
		return fmt.Errorf("application root key id is required")
	}
	return withExpansionAuthorityRootLifecycleOperation(false, func(rootDir string) error {
		secret, err := loadOrCreateExpansionAuthorityRootSecretLockedWithoutMutex()
		if err != nil {
			return err
		}
		if keyID == secret.KeyID {
			return fmt.Errorf("current application root cannot be revoked before rotation")
		}
		privateKey, err := decodeExpansionAuthorityRootSecret(secret)
		if err != nil {
			return err
		}
		keyring, err := readExpansionAuthorityRootKeyring(rootDir)
		if err != nil {
			return err
		}
		anchor, err := readExpansionAuthorityRootAnchor(rootDir)
		if err != nil {
			return err
		}
		updated := keyring
		found := false
		for i := range updated.Keys {
			if updated.Keys[i].KeyID == keyID {
				if updated.Keys[i].State == "revoked" {
					return nil
				}
				updated.Keys[i].State = "revoked"
				updated.Keys[i].AcceptThroughEpoch = 0
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("application root key %q is unavailable", keyID)
		}
		updated.Generation++
		if err := signExpansionAuthorityRootKeyring(&updated, privateKey); err != nil {
			return err
		}
		return replaceExpansionAuthorityRootPairLocked(rootDir, "revoke", secret, anchor, keyring, secret, anchor, updated)
	})
}

func loadOrCreateExpansionAuthorityRootSecretLockedWithoutMutex() (expansionAuthorityRootSecret, error) {
	rootDir, err := expansionAuthorityRootDir()
	if err != nil {
		return expansionAuthorityRootSecret{}, err
	}
	if err := recoverExpansionAuthorityRootRotationLocked(rootDir); err != nil {
		return expansionAuthorityRootSecret{}, err
	}
	var secret expansionAuthorityRootSecret
	if err := readProtectedAuthorityJSON(filepath.Join(rootDir, rootSigningSecretName), &secret); err != nil {
		return secret, err
	}
	if _, err := decodeExpansionAuthorityRootSecret(secret); err != nil {
		return secret, err
	}
	return secret, nil
}

func replaceExpansionAuthorityRootPairLocked(rootDir, operation string, oldSecret expansionAuthorityRootSecret, oldAnchor expansionAuthorityRootAnchor, oldKeyring expansionAuthorityRootKeyring, newSecret expansionAuthorityRootSecret, newAnchor expansionAuthorityRootAnchor, newKeyring expansionAuthorityRootKeyring) error {
	secretPath := filepath.Join(rootDir, rootSigningSecretName)
	anchorPath := filepath.Join(rootDir, rootPublicAnchorName)
	keyringPath := filepath.Join(rootDir, rootPublicKeyringName)
	installationDir, err := expansionAuthorityInstallationDir(rootDir)
	if err != nil {
		return err
	}
	pinPath := filepath.Join(installationDir, authorityTrustPinName)
	checkpointPath := filepath.Join(installationDir, authorityCheckpointName)
	freshInstall := filesAllAbsent(secretPath, anchorPath, keyringPath, pinPath, checkpointPath)
	oldSecretBytes, err := os.ReadFile(filepath.Join(rootDir, rootSigningSecretName))
	if err != nil {
		// A fresh cross-account import has no readable predecessor. Preserve a
		// valid rollback image under the importing account instead.
		oldSecretBytes, err = protectedAuthorityJSONBytes(oldSecret)
		if err != nil {
			return err
		}
	}
	newSecretBytes, err := protectedAuthorityJSONBytes(newSecret)
	if err != nil {
		return err
	}
	oldAnchorBytes, err := os.ReadFile(filepath.Join(rootDir, rootPublicAnchorName))
	if err != nil {
		oldAnchorBytes, err = json.MarshalIndent(oldAnchor, "", "  ")
		if err != nil {
			return err
		}
	}
	newAnchorBytes, err := json.MarshalIndent(newAnchor, "", "  ")
	if err != nil {
		return err
	}
	oldKeyringBytes, err := os.ReadFile(filepath.Join(rootDir, rootPublicKeyringName))
	if err != nil {
		oldKeyringBytes, err = json.MarshalIndent(oldKeyring, "", "  ")
		if err != nil {
			return err
		}
	}
	newKeyringBytes, err := json.MarshalIndent(newKeyring, "", "  ")
	if err != nil {
		return err
	}
	newPinBytes, err := expansionAuthorityTrustPinBytes(newAnchor)
	if err != nil {
		return err
	}
	newCheckpointBytes, err := expansionAuthorityCheckpointBytes(checkpointFromAuthority(newAnchor, newKeyring))
	if err != nil {
		return err
	}
	var oldPinBytes, oldCheckpointBytes []byte
	if !freshInstall {
		oldPinBytes, err = os.ReadFile(pinPath)
		if err != nil {
			return fmt.Errorf("read installed authority pin for transaction: %w", err)
		}
		oldCheckpointBytes, err = os.ReadFile(checkpointPath)
		if err != nil {
			return fmt.Errorf("read installed authority checkpoint for transaction: %w", err)
		}
		if !slices.Equal(oldPinBytes, newPinBytes) {
			return fmt.Errorf("authority transaction cannot replace the installed trust pin")
		}
	}
	journal := expansionAuthorityRootRotationJournal{
		Version: expansionAuthorityRootLifecycleVersion, Operation: operation,
		OldSecret: oldSecretBytes, OldAnchor: oldAnchorBytes, OldKeyring: oldKeyringBytes,
		NewSecret: newSecretBytes, NewAnchor: newAnchorBytes, NewKeyring: newKeyringBytes,
		OldPin: oldPinBytes, OldCheckpoint: oldCheckpointBytes, NewPin: newPinBytes, NewCheckpoint: newCheckpointBytes, FreshInstall: freshInstall,
		NewKeyID: newSecret.KeyID, NewEpoch: newSecret.Epoch, CreatedAtUTC: time.Now().UTC().Format(time.RFC3339Nano),
	}
	journalPath := filepath.Join(rootDir, rootRotationJournalName)
	if err := writeProtectedAuthorityJSON(journalPath, journal); err != nil {
		return err
	}
	if err := writeRestrictedAtomic(filepath.Join(rootDir, rootSigningSecretName), newSecretBytes); err != nil {
		return recoverExpansionAuthorityRootRotationAfterFailure(rootDir, err)
	}
	if err := writeAuthorityPublicAtomic(filepath.Join(rootDir, rootPublicKeyringName), newKeyringBytes); err != nil {
		return recoverExpansionAuthorityRootRotationAfterFailure(rootDir, err)
	}
	if err := writeAuthorityPublicAtomic(filepath.Join(rootDir, rootPublicAnchorName), newAnchorBytes); err != nil {
		return recoverExpansionAuthorityRootRotationAfterFailure(rootDir, err)
	}
	if err := installExpansionAuthorityPinBytes(pinPath, newPinBytes, freshInstall); err != nil {
		return recoverExpansionAuthorityRootRotationAfterFailure(rootDir, err)
	}
	// The protected journal remains until this external monotonic commit point
	// is durable. Once advanced, recovery may only complete the new authority.
	if err := writeAuthorityPublicAtomic(checkpointPath, newCheckpointBytes); err != nil {
		return recoverExpansionAuthorityRootRotationAfterFailure(rootDir, err)
	}
	if err := verifyAuthorityTransactionBytes(newSecretBytes, newAnchorBytes, newKeyringBytes, newPinBytes, newCheckpointBytes, secretPath, anchorPath, keyringPath, pinPath, checkpointPath); err != nil {
		return err
	}
	if err := os.Remove(journalPath); err != nil {
		return err
	}
	return appendExpansionAuthorityRootAudit(rootDir, operation, newSecret.KeyID, newSecret.Epoch)
}

func protectedAuthorityJSONBytes(value any) ([]byte, error) {
	plaintext, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return nil, err
	}
	return protectAuthoritySecret(plaintext)
}

func recoverExpansionAuthorityRootRotationAfterFailure(rootDir string, cause error) error {
	if err := recoverExpansionAuthorityRootRotationLocked(rootDir); err != nil {
		return errors.Join(cause, err)
	}
	return cause
}

func recoverExpansionAuthorityRootRotationLocked(rootDir string) error {
	journalPath := filepath.Join(rootDir, rootRotationJournalName)
	var journal expansionAuthorityRootRotationJournal
	if err := readProtectedAuthorityJSON(journalPath, &journal); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("recover application root rotation: %w", err)
	}
	if journal.Version != expansionAuthorityRootLifecycleVersion || journal.NewEpoch == 0 || journal.NewKeyID == "" ||
		len(journal.OldSecret) == 0 || len(journal.OldAnchor) == 0 || len(journal.OldKeyring) == 0 || len(journal.NewSecret) == 0 || len(journal.NewAnchor) == 0 || len(journal.NewKeyring) == 0 || len(journal.NewPin) == 0 || len(journal.NewCheckpoint) == 0 {
		return fmt.Errorf("application root rotation journal is invalid")
	}
	secretPath := filepath.Join(rootDir, rootSigningSecretName)
	anchorPath := filepath.Join(rootDir, rootPublicAnchorName)
	keyringPath := filepath.Join(rootDir, rootPublicKeyringName)
	installationDir, err := expansionAuthorityInstallationDir(rootDir)
	if err != nil {
		return err
	}
	pinPath := filepath.Join(installationDir, authorityTrustPinName)
	checkpointPath := filepath.Join(installationDir, authorityCheckpointName)
	currentSecret, secretErr := os.ReadFile(secretPath)
	currentAnchor, anchorErr := os.ReadFile(anchorPath)
	currentKeyring, keyringErr := os.ReadFile(keyringPath)
	currentCheckpoint, checkpointErr := os.ReadFile(checkpointPath)
	exactNew := secretErr == nil && anchorErr == nil && keyringErr == nil && slices.Equal(currentSecret, journal.NewSecret) && slices.Equal(currentAnchor, journal.NewAnchor) && slices.Equal(currentKeyring, journal.NewKeyring)
	checkpointAdvanced := checkpointErr == nil && slices.Equal(currentCheckpoint, journal.NewCheckpoint)
	if exactNew || checkpointAdvanced {
		// Exact-new state or an advanced commit point can only roll forward.
		if err := writeRestrictedAtomic(secretPath, journal.NewSecret); err != nil {
			return err
		}
		if err := writeAuthorityPublicAtomic(keyringPath, journal.NewKeyring); err != nil {
			return err
		}
		if err := writeAuthorityPublicAtomic(anchorPath, journal.NewAnchor); err != nil {
			return err
		}
		if err := installExpansionAuthorityPinBytes(pinPath, journal.NewPin, journal.FreshInstall); err != nil {
			return err
		}
		if err := writeAuthorityPublicAtomic(checkpointPath, journal.NewCheckpoint); err != nil {
			return err
		}
	} else {
		if journal.FreshInstall {
			for _, path := range []string{secretPath, keyringPath, anchorPath, pinPath, checkpointPath} {
				if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
					return err
				}
			}
		} else {
			if err := writeRestrictedAtomic(secretPath, journal.OldSecret); err != nil {
				return err
			}
			if err := writeAuthorityPublicAtomic(keyringPath, journal.OldKeyring); err != nil {
				return err
			}
			if err := writeAuthorityPublicAtomic(anchorPath, journal.OldAnchor); err != nil {
				return err
			}
			if err := writeAuthorityPublicAtomic(pinPath, journal.OldPin); err != nil {
				return err
			}
			if err := writeAuthorityPublicAtomic(checkpointPath, journal.OldCheckpoint); err != nil {
				return err
			}
		}
	}
	return os.Remove(journalPath)
}

// ExportExpansionAuthorityRoot writes an AES-GCM migration bundle. The caller
// owns the 32-byte wrapping key; no plaintext private material is written.
func ExportExpansionAuthorityRoot(destination string, wrappingKey []byte) error {
	if len(wrappingKey) != 32 {
		return fmt.Errorf("application root export requires a 32-byte wrapping key")
	}
	if !filepath.IsAbs(destination) {
		return fmt.Errorf("application root export destination must be absolute")
	}
	return withExpansionAuthorityRootLifecycleOperation(false, func(rootDir string) error {
		secret, err := loadOrCreateExpansionAuthorityRootSecretLockedWithoutMutex()
		if err != nil {
			return err
		}
		keyring, err := readExpansionAuthorityRootKeyring(rootDir)
		if err != nil {
			return err
		}
		anchor, err := readExpansionAuthorityRootAnchor(rootDir)
		if err != nil {
			return err
		}
		payload, err := json.Marshal(expansionAuthorityRootMigrationPayload{
			Version: expansionAuthorityRootLifecycleVersion, Secret: secret, Anchor: anchor, Keyring: keyring,
			ExportedAt: time.Now().UTC().Format(time.RFC3339Nano),
		})
		if err != nil {
			return err
		}
		bundle, err := sealExpansionAuthorityRootMigration(payload, wrappingKey)
		if err != nil {
			return err
		}
		encoded, err := json.MarshalIndent(bundle, "", "  ")
		if err != nil {
			return err
		}
		if err := writeRestrictedAtomic(destination, encoded); err != nil {
			return err
		}
		return appendExpansionAuthorityRootAudit(rootDir, "export", secret.KeyID, secret.Epoch)
	})
}

// ImportExpansionAuthorityRoot validates and re-protects a migration bundle
// under the current OS account. An older epoch can never replace a newer one.
func ImportExpansionAuthorityRoot(source string, wrappingKey []byte) error {
	if len(wrappingKey) != 32 {
		return fmt.Errorf("application root import requires a 32-byte wrapping key")
	}
	bundleBytes, err := os.ReadFile(source)
	if err != nil {
		return err
	}
	var bundle expansionAuthorityRootMigrationBundle
	if err := json.Unmarshal(bundleBytes, &bundle); err != nil {
		return err
	}
	payloadBytes, err := openExpansionAuthorityRootMigration(bundle, wrappingKey)
	if err != nil {
		return err
	}
	var payload expansionAuthorityRootMigrationPayload
	if err := json.Unmarshal(payloadBytes, &payload); err != nil {
		return err
	}
	privateKey, err := decodeExpansionAuthorityRootSecret(payload.Secret)
	if err != nil {
		return err
	}
	if err := validateExpansionAuthorityRootKeyring(payload.Keyring); err != nil || payload.Keyring.CurrentKeyID != payload.Secret.KeyID || payload.Keyring.CurrentEpoch != payload.Secret.Epoch {
		return fmt.Errorf("application root migration bundle is inconsistent")
	}
	if len(payload.Keyring.Keys) == 0 || payload.Anchor.Version != expansionAuthorityRootLifecycleVersion ||
		payload.Keyring.Keys[0].KeyID != payload.Anchor.KeyID || payload.Keyring.Keys[0].Epoch != payload.Anchor.Epoch || payload.Keyring.Keys[0].PublicKey != payload.Anchor.PublicKey ||
		payload.Keyring.CurrentKeyID != payload.Anchor.CurrentKeyID || payload.Keyring.CurrentEpoch != payload.Anchor.CurrentEpoch ||
		payload.Secret.AnchorHash != expansionAuthorityRootAnchorHash(payload.Anchor) {
		return fmt.Errorf("application root migration anchor is inconsistent")
	}
	if !privateKey.Public().(ed25519.PublicKey).Equal(mustDecodeExpansionAuthorityPublic(payload.Secret.PublicKey)) {
		return fmt.Errorf("application root migration bundle key binding is invalid")
	}
	return withExpansionAuthorityRootLifecycleOperation(true, func(rootDir string) error {
		if err := recoverExpansionAuthorityRootRotationLocked(rootDir); err != nil {
			return err
		}
		oldAnchor := payload.Anchor
		freshInstallation := false
		if installedAnchor, anchorErr := readExpansionAuthorityRootAnchor(rootDir); anchorErr == nil {
			if installedAnchor.KeyID != payload.Anchor.KeyID || installedAnchor.PublicKey != payload.Anchor.PublicKey {
				return fmt.Errorf("application root import does not match the installed trust anchor")
			}
			oldAnchor = installedAnchor
			installedKeyring, keyringErr := readExpansionAuthorityRootKeyring(rootDir)
			if keyringErr != nil {
				return keyringErr
			}
			if payload.Keyring.CurrentEpoch < installedKeyring.CurrentEpoch || payload.Keyring.Generation < installedKeyring.Generation ||
				!expansionAuthorityKeyringContainsRevocations(payload.Keyring, installedKeyring) {
				return fmt.Errorf("application root import would roll back installed public authority state")
			}
		} else if os.IsNotExist(errors.Unwrap(anchorErr)) || os.IsNotExist(anchorErr) {
			if err := requireExpansionAuthorityInstallationEmpty(rootDir); err != nil {
				return err
			}
			freshInstallation = true
		} else {
			return anchorErr
		}
		var oldSecret expansionAuthorityRootSecret
		var oldKeyring expansionAuthorityRootKeyring
		secretErr := readProtectedAuthorityJSON(filepath.Join(rootDir, rootSigningSecretName), &oldSecret)
		keyringErr := func() error { oldKeyring, err = readExpansionAuthorityRootKeyring(rootDir); return err }()
		if secretErr == nil && keyringErr == nil {
			if oldSecret.Epoch > payload.Secret.Epoch {
				return fmt.Errorf("application root import would roll back epoch %d to %d", oldSecret.Epoch, payload.Secret.Epoch)
			}
			return replaceExpansionAuthorityRootPairLocked(rootDir, "import", oldSecret, oldAnchor, oldKeyring, payload.Secret, payload.Anchor, payload.Keyring)
		}
		if !os.IsNotExist(secretErr) && secretErr != nil {
			// A different Windows account cannot decrypt the previous secret. Explicit
			// import is the authorized re-protection boundary; preserve raw bytes for
			// crash rollback and install the authenticated bundle.
			oldSecret = payload.Secret
		}
		if keyringErr != nil && !os.IsNotExist(keyringErr) {
			return keyringErr
		}
		if len(oldKeyring.Keys) == 0 {
			oldKeyring = payload.Keyring
		}
		operation := "import-reprotect"
		if freshInstallation {
			operation = "import-fresh"
		}
		return replaceExpansionAuthorityRootPairLocked(rootDir, operation, oldSecret, oldAnchor, oldKeyring, payload.Secret, payload.Anchor, payload.Keyring)
	})
}

func filesAllAbsent(paths ...string) bool {
	for _, path := range paths {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			return false
		}
	}
	return true
}

func installExpansionAuthorityPinBytes(path string, payload []byte, allowCreate bool) error {
	current, err := os.ReadFile(path)
	if err == nil {
		if !slices.Equal(current, payload) {
			return fmt.Errorf("installed authority trust pin changed")
		}
		return nil
	}
	if !os.IsNotExist(err) || !allowCreate {
		return fmt.Errorf("installed authority trust pin is unavailable: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, payload, 0o444)
}

func verifyAuthorityTransactionBytes(expectedSecret, expectedAnchor, expectedKeyring, expectedPin, expectedCheckpoint []byte, paths ...string) error {
	expected := [][]byte{expectedSecret, expectedAnchor, expectedKeyring, expectedPin, expectedCheckpoint}
	for i, path := range paths {
		actual, err := os.ReadFile(path)
		if err != nil || !slices.Equal(actual, expected[i]) {
			return fmt.Errorf("authority transaction did not durably install %q", filepath.Base(path))
		}
	}
	return nil
}

func expansionAuthorityKeyringContainsRevocations(candidate, installed expansionAuthorityRootKeyring) bool {
	revoked := make(map[string]struct{})
	for _, key := range candidate.Keys {
		if key.State == "revoked" {
			revoked[key.KeyID] = struct{}{}
		}
	}
	for _, key := range installed.Keys {
		if key.State == "revoked" {
			if _, ok := revoked[key.KeyID]; !ok {
				return false
			}
		}
	}
	return true
}

func sealExpansionAuthorityRootMigration(payload, wrappingKey []byte) (expansionAuthorityRootMigrationBundle, error) {
	block, err := aes.NewCipher(wrappingKey)
	if err != nil {
		return expansionAuthorityRootMigrationBundle{}, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return expansionAuthorityRootMigrationBundle{}, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return expansionAuthorityRootMigrationBundle{}, err
	}
	ciphertext := gcm.Seal(nil, nonce, payload, []byte("ainovel-authority-root-v2"))
	digest := sha256.Sum256(ciphertext)
	return expansionAuthorityRootMigrationBundle{
		Version: expansionAuthorityRootLifecycleVersion,
		Nonce:   base64.StdEncoding.EncodeToString(nonce), Ciphertext: base64.StdEncoding.EncodeToString(ciphertext), Digest: hex.EncodeToString(digest[:]),
	}, nil
}

func openExpansionAuthorityRootMigration(bundle expansionAuthorityRootMigrationBundle, wrappingKey []byte) ([]byte, error) {
	nonce, nonceErr := base64.StdEncoding.DecodeString(bundle.Nonce)
	ciphertext, cipherErr := base64.StdEncoding.DecodeString(bundle.Ciphertext)
	digest := sha256.Sum256(ciphertext)
	if bundle.Version != expansionAuthorityRootLifecycleVersion || nonceErr != nil || cipherErr != nil || !strings.EqualFold(bundle.Digest, hex.EncodeToString(digest[:])) {
		return nil, fmt.Errorf("application root migration bundle is invalid")
	}
	block, err := aes.NewCipher(wrappingKey)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(nonce) != gcm.NonceSize() {
		return nil, fmt.Errorf("application root migration nonce is invalid")
	}
	return gcm.Open(nil, nonce, ciphertext, []byte("ainovel-authority-root-v2"))
}

func mustDecodeExpansionAuthorityPublic(encoded string) ed25519.PublicKey {
	decoded, _ := base64.StdEncoding.DecodeString(encoded)
	return ed25519.PublicKey(decoded)
}

func appendExpansionAuthorityRootAudit(rootDir, operation, keyID string, epoch uint64) error {
	record, err := json.Marshal(struct {
		Operation string `json:"operation"`
		KeyID     string `json:"key_id"`
		Epoch     uint64 `json:"epoch"`
		AtUTC     string `json:"at_utc"`
	}{operation, keyID, epoch, time.Now().UTC().Format(time.RFC3339Nano)})
	if err != nil {
		return err
	}
	path := filepath.Join(rootDir, rootMigrationAuditName)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	if _, err := file.Write(append(record, '\n')); err != nil {
		return err
	}
	return file.Sync()
}

func prepareExpansionAuthorityRootDir(rootDir string) error {
	if err := os.MkdirAll(rootDir, 0o700); err != nil {
		return err
	}
	return verifyExpansionAuthorityRootDir(rootDir)
}

func recoverExpansionAuthorityRootLifecycle() error {
	rootDir, err := expansionAuthorityRootDir()
	if err != nil {
		return err
	}
	if _, err := os.Stat(rootDir); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	if err := verifyExpansionAuthorityRootDir(rootDir); err != nil {
		return err
	}
	return withExpansionAuthorityRootLifecycleOperation(false, func(rootDir string) error {
		return recoverExpansionAuthorityRootRotationLocked(rootDir)
	})
}

func sortedExpansionAuthorityRootKeys(keys []expansionAuthorityRootKey) []expansionAuthorityRootKey {
	result := append([]expansionAuthorityRootKey(nil), keys...)
	sort.Slice(result, func(i, j int) bool { return result[i].Epoch < result[j].Epoch })
	return result
}
