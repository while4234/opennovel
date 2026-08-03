//go:build !windows

package store

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAuthorityInstallationRejectsServiceReplaceableUnixDirectories(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("root ownership fixture requires an elevated test process")
	}
	parent := filepath.Join(t.TempDir(), "managed")
	installation := filepath.Join(parent, "publication-authority-installation-v1")
	if err := os.MkdirAll(installation, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := verifyExpansionAuthorityInstallationDir(installation); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(parent, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := verifyExpansionAuthorityInstallationDir(installation); err == nil {
		t.Fatal("group/world-writable parent allowed service-account rename")
	}
	if err := os.Chmod(parent, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(installation, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := verifyExpansionAuthorityInstallationDir(installation); err == nil {
		t.Fatal("group/world-writable installation anchor was accepted")
	}
}
