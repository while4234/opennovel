//go:build !windows

package store

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

func platformExpansionAuthorityRootDir() (string, error) {
	return "/var/lib/ainovel/publication-authority-v1", nil
}

func platformExpansionAuthorityInstallationDir(string) (string, error) {
	return "/var/lib/ainovel/publication-authority-installation-v1", nil
}

func verifyExpansionAuthorityRootDir(path string) error {
	clean := filepath.Clean(path)
	info, err := os.Lstat(clean)
	if err != nil {
		return fmt.Errorf("inspect application authority root: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("application authority root must be a physical directory")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || (os.Geteuid() != 0 && stat.Uid != uint32(os.Geteuid())) {
		return fmt.Errorf("application authority root is not owned by the current user")
	}
	if info.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("application authority root permissions %o allow group/world writes", info.Mode().Perm())
	}
	return nil
}

func verifyExpansionAuthorityBootstrapPrivileges() error {
	if os.Geteuid() != 0 {
		return fmt.Errorf("authority bootstrap requires administrator (root) privileges")
	}
	return nil
}

func prepareExpansionAuthorityInstallationDir(path string) error {
	if err := os.MkdirAll(path, 0o755); err != nil {
		return err
	}
	if err := os.Chmod(path, 0o755); err != nil {
		return fmt.Errorf("set authority installation permissions: %w", err)
	}
	return nil
}

func verifyExpansionAuthorityInstallationDir(path string) error {
	clean := filepath.Clean(path)
	info, err := os.Lstat(clean)
	if err != nil {
		return fmt.Errorf("inspect authority installation anchor: %w", err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || !ok || stat.Uid != 0 {
		return fmt.Errorf("authority installation anchor must be a physical administrator-owned directory")
	}
	if info.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("authority installation anchor permissions %o allow service-account replacement", info.Mode().Perm())
	}
	parent := filepath.Dir(clean)
	parentInfo, err := os.Lstat(parent)
	if err != nil {
		return fmt.Errorf("inspect authority installation parent: %w", err)
	}
	parentStat, ok := parentInfo.Sys().(*syscall.Stat_t)
	if !parentInfo.IsDir() || parentInfo.Mode()&os.ModeSymlink != 0 || !ok || parentStat.Uid != 0 || parentInfo.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("authority installation parent must prevent service-account delete or rename")
	}
	return nil
}
