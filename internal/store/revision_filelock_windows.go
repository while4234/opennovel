//go:build windows

package store

import (
	"errors"
	"os"
	"syscall"
	"unsafe"
)

const (
	revisionLockFileFailImmediately = 0x00000001
	revisionLockFileExclusive       = 0x00000002
	revisionLockViolation           = syscall.Errno(33)
)

var (
	revisionKernel32   = syscall.NewLazyDLL("kernel32.dll")
	revisionLockFileEx = revisionKernel32.NewProc("LockFileEx")
	revisionUnlockFile = revisionKernel32.NewProc("UnlockFileEx")
)

func tryRevisionFileLock(file *os.File) (bool, error) {
	overlapped := new(syscall.Overlapped)
	result, _, callErr := revisionLockFileEx.Call(
		file.Fd(),
		revisionLockFileFailImmediately|revisionLockFileExclusive,
		0,
		1,
		0,
		uintptr(unsafe.Pointer(overlapped)),
	)
	if result != 0 {
		return true, nil
	}
	if errors.Is(callErr, revisionLockViolation) {
		return false, nil
	}
	return false, callErr
}

func unlockRevisionFile(file *os.File) error {
	overlapped := new(syscall.Overlapped)
	result, _, callErr := revisionUnlockFile.Call(
		file.Fd(),
		0,
		1,
		0,
		uintptr(unsafe.Pointer(overlapped)),
	)
	if result != 0 {
		return nil
	}
	return callErr
}
