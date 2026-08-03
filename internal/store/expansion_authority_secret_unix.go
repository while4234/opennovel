//go:build !windows

package store

import (
	"fmt"
	"os"
)

func protectAuthoritySecret(plaintext []byte) ([]byte, error) {
	return append([]byte(nil), plaintext...), nil
}

func unprotectAuthoritySecret(payload []byte) ([]byte, error) {
	return append([]byte(nil), payload...), nil
}

func restrictAuthorityFile(path string) error { return os.Chmod(path, 0o600) }

func verifyProtectedAuthorityFile(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.Mode().Perm() != 0o600 {
		return fmt.Errorf("publication authority permissions are %o, want 600", info.Mode().Perm())
	}
	return nil
}
