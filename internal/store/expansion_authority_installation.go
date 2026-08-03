package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

const (
	authorityTrustPinName    = "trust-pin.json"
	authorityCheckpointName  = "monotonic-checkpoint.json"
	authorityInstallationVer = 1
)

type expansionAuthorityTrustPin struct {
	Version    int    `json:"version"`
	KeyID      string `json:"key_id"`
	PublicKey  string `json:"public_key"`
	AnchorHash string `json:"anchor_hash"`
}

type expansionAuthorityCheckpoint struct {
	Version       int      `json:"version"`
	AnchorHash    string   `json:"anchor_hash"`
	CurrentEpoch  uint64   `json:"current_epoch"`
	Generation    uint64   `json:"generation"`
	RevokedKeyIDs []string `json:"revoked_key_ids"`
}

func initializeExpansionAuthorityInstallation(rootDir string, anchor expansionAuthorityRootAnchor, keyring expansionAuthorityRootKeyring) error {
	dir, err := expansionAuthorityInstallationDir(rootDir)
	if err != nil {
		return err
	}
	if err := prepareExpansionAuthorityInstallationDir(dir); err != nil {
		return err
	}
	if !expansionAuthorityTestConfigurationActive {
		if err := verifyExpansionAuthorityInstallationDir(dir); err != nil {
			return err
		}
	}
	pin := expansionAuthorityTrustPin{Version: authorityInstallationVer, KeyID: anchor.KeyID, PublicKey: anchor.PublicKey, AnchorHash: expansionAuthorityRootAnchorHash(anchor)}
	pinData, _ := json.MarshalIndent(pin, "", "  ")
	pinPath := filepath.Join(dir, authorityTrustPinName)
	file, err := os.OpenFile(pinPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o444)
	if err != nil {
		return fmt.Errorf("create immutable authority trust pin: %w", err)
	}
	if _, err = file.Write(pinData); err == nil {
		err = file.Sync()
	}
	err = errorsJoin(err, file.Close())
	if err != nil {
		return err
	}
	return writeExpansionAuthorityCheckpoint(rootDir, checkpointFromAuthority(anchor, keyring))
}

func expansionAuthorityTrustPinBytes(anchor expansionAuthorityRootAnchor) ([]byte, error) {
	pin := expansionAuthorityTrustPin{Version: authorityInstallationVer, KeyID: anchor.KeyID, PublicKey: anchor.PublicKey, AnchorHash: expansionAuthorityRootAnchorHash(anchor)}
	return json.MarshalIndent(pin, "", "  ")
}

func expansionAuthorityCheckpointBytes(checkpoint expansionAuthorityCheckpoint) ([]byte, error) {
	return json.MarshalIndent(checkpoint, "", "  ")
}

func requireExpansionAuthorityInstallationEmpty(rootDir string) error {
	dir, err := expansionAuthorityInstallationDir(rootDir)
	if err != nil {
		return err
	}
	if !expansionAuthorityTestConfigurationActive {
		if err := verifyExpansionAuthorityInstallationDir(dir); err != nil {
			return err
		}
	}
	for _, name := range []string{authorityTrustPinName, authorityCheckpointName} {
		if _, err := os.Lstat(filepath.Join(dir, name)); err == nil {
			return fmt.Errorf("authority installation state already exists; refusing to establish a replacement trust domain")
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

func verifyExpansionAuthorityTrustPin(rootDir string, anchor expansionAuthorityRootAnchor) error {
	dir, err := expansionAuthorityInstallationDir(rootDir)
	if err != nil {
		return err
	}
	if !expansionAuthorityTestConfigurationActive {
		if err := verifyExpansionAuthorityInstallationDir(dir); err != nil {
			return err
		}
	}
	data, err := os.ReadFile(filepath.Join(dir, authorityTrustPinName))
	if err != nil {
		return fmt.Errorf("immutable authority trust pin is unavailable: %w", err)
	}
	var pin expansionAuthorityTrustPin
	if err := decodeExactJSON(data, &pin); err != nil {
		return err
	}
	if pin.Version != authorityInstallationVer || pin.KeyID != anchor.KeyID || pin.PublicKey != anchor.PublicKey || pin.AnchorHash != expansionAuthorityRootAnchorHash(anchor) {
		return fmt.Errorf("authority trust anchor does not match the release installation pin")
	}
	return nil
}

func checkpointFromAuthority(anchor expansionAuthorityRootAnchor, keyring expansionAuthorityRootKeyring) expansionAuthorityCheckpoint {
	revoked := make([]string, 0)
	for _, key := range keyring.Keys {
		if key.State == "revoked" {
			revoked = append(revoked, key.KeyID)
		}
	}
	sort.Strings(revoked)
	return expansionAuthorityCheckpoint{Version: authorityInstallationVer, AnchorHash: expansionAuthorityRootAnchorHash(anchor), CurrentEpoch: keyring.CurrentEpoch, Generation: keyring.Generation, RevokedKeyIDs: revoked}
}

func verifyAndAdvanceExpansionAuthorityCheckpoint(rootDir string, anchor expansionAuthorityRootAnchor, keyring expansionAuthorityRootKeyring) error {
	dir, err := expansionAuthorityInstallationDir(rootDir)
	if err != nil {
		return err
	}
	path := filepath.Join(dir, authorityCheckpointName)
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("authority monotonic checkpoint is unavailable: %w", err)
	}
	var installed expansionAuthorityCheckpoint
	if err := decodeExactJSON(data, &installed); err != nil {
		return err
	}
	candidate := checkpointFromAuthority(anchor, keyring)
	if installed.Version != authorityInstallationVer || installed.AnchorHash != candidate.AnchorHash || candidate.CurrentEpoch < installed.CurrentEpoch || candidate.Generation < installed.Generation || !stringSliceContainsAll(candidate.RevokedKeyIDs, installed.RevokedKeyIDs) {
		return fmt.Errorf("authority public state would roll back its monotonic checkpoint")
	}
	if candidate.CurrentEpoch > installed.CurrentEpoch || candidate.Generation > installed.Generation || len(candidate.RevokedKeyIDs) > len(installed.RevokedKeyIDs) {
		return writeExpansionAuthorityCheckpoint(rootDir, candidate)
	}
	return nil
}

func writeExpansionAuthorityCheckpoint(rootDir string, checkpoint expansionAuthorityCheckpoint) error {
	dir, err := expansionAuthorityInstallationDir(rootDir)
	if err != nil {
		return err
	}
	if err := prepareExpansionAuthorityInstallationDir(dir); err != nil {
		return err
	}
	data, _ := json.MarshalIndent(checkpoint, "", "  ")
	return writeAuthorityPublicAtomic(filepath.Join(dir, authorityCheckpointName), data)
}

func stringSliceContainsAll(have, required []string) bool {
	set := make(map[string]struct{}, len(have))
	for _, value := range have {
		set[value] = struct{}{}
	}
	for _, value := range required {
		if _, ok := set[value]; !ok {
			return false
		}
	}
	return true
}

func errorsJoin(left, right error) error {
	if left != nil {
		return left
	}
	return right
}
