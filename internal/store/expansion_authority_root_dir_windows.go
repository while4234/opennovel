//go:build windows

package store

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"unsafe"
)

var (
	authorityShell32                  = syscall.NewLazyDLL("shell32.dll")
	authorityIsUserAnAdmin            = authorityShell32.NewProc("IsUserAnAdmin")
	authorityAdvapi32                 = syscall.NewLazyDLL("advapi32.dll")
	authorityGetNamedSecurityInfo     = authorityAdvapi32.NewProc("GetNamedSecurityInfoW")
	authorityGetSecurityDescriptorCtl = authorityAdvapi32.NewProc("GetSecurityDescriptorControl")
	authorityIsWellKnownSid           = authorityAdvapi32.NewProc("IsWellKnownSid")
	authorityOpenPathAccess           = openAuthorityPathAccess
)

const (
	authoritySEFileObject          = 1
	authorityOwnerSecurityInfo     = 0x00000001
	authorityDaclSecurityInfo      = 0x00000004
	authorityProtectedDaclSecurity = 0x1000
	authorityWinLocalSystemSid     = 22
	authorityWinBuiltinAdminsSid   = 26
)

func platformExpansionAuthorityRootDir() (string, error) {
	return filepath.Clean(`C:\ProgramData\AINovel\publication-authority-v1`), nil
}

func platformExpansionAuthorityInstallationDir(string) (string, error) {
	return filepath.Clean(`C:\ProgramData\AINovel\publication-authority-installation-v1`), nil
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
	resolved, err := filepath.EvalSymlinks(clean)
	if err != nil {
		return fmt.Errorf("resolve application authority root: %w", err)
	}
	if !filepath.IsAbs(resolved) {
		return fmt.Errorf("application authority root resolution is invalid")
	}
	if expansionAuthorityTestConfigurationActive {
		return nil
	}
	if err := verifyAdministratorOwnedProtectedDACL(clean); err != nil {
		return fmt.Errorf("authority runtime root is replaceable: %w", err)
	}
	admin, _, _ := authorityIsUserAnAdmin.Call()
	if admin == 0 {
		if err := verifyAuthorityRuntimeCannotAccess(clean, "runtime root", authorityRootObjectDangerousAccess); err != nil {
			return err
		}
	}
	return nil
}

func verifyExpansionAuthorityBootstrapPrivileges() error {
	admin, _, _ := authorityIsUserAnAdmin.Call()
	if admin == 0 {
		return fmt.Errorf("authority bootstrap requires an elevated administrator token")
	}
	return nil
}

func prepareExpansionAuthorityInstallationDir(path string) error {
	if err := os.MkdirAll(path, 0o755); err != nil {
		return err
	}
	return nil
}

func verifyExpansionAuthorityInstallationDir(path string) error {
	info, err := os.Lstat(filepath.Clean(path))
	if err != nil {
		return fmt.Errorf("inspect authority installation anchor: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("authority installation anchor must be a physical directory")
	}
	if err := verifyAdministratorOwnedProtectedDACL(filepath.Clean(path)); err != nil {
		return err
	}
	if err := verifyAdministratorOwnedProtectedDACL(filepath.Dir(filepath.Clean(path))); err != nil {
		return fmt.Errorf("authority installation parent permits replacement: %w", err)
	}
	admin, _, _ := authorityIsUserAnAdmin.Call()
	if admin == 0 {
		if err := verifyAuthorityRuntimeCannotMutate(filepath.Dir(filepath.Clean(path)), "parent"); err != nil {
			return err
		}
		if err := verifyAuthorityRuntimeCannotMutate(filepath.Clean(path), "installation"); err != nil {
			return err
		}
	}
	return nil
}

var authorityDangerousAccess = []struct {
	name   string
	access uint32
}{
	{"DELETE", 0x00010000},
	{"DELETE_CHILD", 0x00000040},
	{"WRITE_DAC", 0x00040000},
	{"WRITE_OWNER", 0x00080000},
}

var authorityRootObjectDangerousAccess = []struct {
	name   string
	access uint32
}{
	{"DELETE", 0x00010000},
	{"WRITE_DAC", 0x00040000},
	{"WRITE_OWNER", 0x00080000},
}

func verifyAuthorityRuntimeCannotMutate(path, object string) error {
	return verifyAuthorityRuntimeCannotAccess(path, object, authorityDangerousAccess)
}

func verifyAuthorityRuntimeCannotAccess(path, object string, rights []struct {
	name   string
	access uint32
}) error {
	for _, right := range rights {
		allowed, err := authorityOpenPathAccess(path, right.access)
		if err != nil {
			return fmt.Errorf("probe authority installation %s %s access: %w", object, right.name, err)
		}
		if allowed {
			return fmt.Errorf("runtime service account has %s on the authority installation %s", right.name, object)
		}
	}
	return nil
}

func openAuthorityPathAccess(path string, access uint32) (bool, error) {
	pathPtr, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return false, err
	}
	handle, openErr := syscall.CreateFile(
		pathPtr,
		access,
		syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE|syscall.FILE_SHARE_DELETE,
		nil,
		syscall.OPEN_EXISTING,
		syscall.FILE_FLAG_BACKUP_SEMANTICS,
		0,
	)
	if openErr == nil {
		_ = syscall.CloseHandle(handle)
		return true, nil
	}
	if errors.Is(openErr, syscall.ERROR_ACCESS_DENIED) {
		return false, nil
	}
	return false, openErr
}

func verifyAdministratorOwnedProtectedDACL(path string) error {
	pathPtr, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	var owner, dacl, descriptor uintptr
	result, _, _ := authorityGetNamedSecurityInfo.Call(
		uintptr(unsafe.Pointer(pathPtr)), authoritySEFileObject,
		authorityOwnerSecurityInfo|authorityDaclSecurityInfo,
		uintptr(unsafe.Pointer(&owner)), 0,
		uintptr(unsafe.Pointer(&dacl)), 0,
		uintptr(unsafe.Pointer(&descriptor)),
	)
	if result != 0 || owner == 0 || dacl == 0 || descriptor == 0 {
		return fmt.Errorf("read authority installation owner/DACL: win32 error %d", result)
	}
	isSystem, _, _ := authorityIsWellKnownSid.Call(owner, authorityWinLocalSystemSid)
	isAdmin, _, _ := authorityIsWellKnownSid.Call(owner, authorityWinBuiltinAdminsSid)
	if isSystem == 0 && isAdmin == 0 {
		return fmt.Errorf("authority installation owner is not LocalSystem or Builtin Administrators")
	}
	var control uint16
	var revision uint32
	result, _, _ = authorityGetSecurityDescriptorCtl.Call(descriptor, uintptr(unsafe.Pointer(&control)), uintptr(unsafe.Pointer(&revision)))
	if result == 0 || control&authorityProtectedDaclSecurity == 0 {
		return fmt.Errorf("authority installation DACL must be explicitly protected from inherited service-account replacement rights")
	}
	return nil
}
