//go:build windows

package store

import (
	"fmt"
	"syscall"
)

// syncAuthorityDirectoryPlatform opens a directory with backup semantics and
// flushes its metadata handle. This is the Windows durability equivalent of a
// Unix directory fsync and is required before deleting the recovery journal
// that proves a private registry unlink still needs to be replayed.
func syncAuthorityDirectoryPlatform(path string) error {
	pathPtr, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	handle, err := syscall.CreateFile(
		pathPtr,
		syscall.GENERIC_READ|syscall.GENERIC_WRITE,
		syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE|syscall.FILE_SHARE_DELETE,
		nil,
		syscall.OPEN_EXISTING,
		syscall.FILE_FLAG_BACKUP_SEMANTICS,
		0,
	)
	if err != nil {
		return fmt.Errorf("open authority directory for durability: %w", err)
	}
	defer syscall.CloseHandle(handle)
	if err := syscall.FlushFileBuffers(handle); err != nil {
		return fmt.Errorf("flush authority directory metadata: %w", err)
	}
	return nil
}
