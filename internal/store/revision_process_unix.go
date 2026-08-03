//go:build !windows

package store

import (
	"errors"
	"syscall"
)

func revisionProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
