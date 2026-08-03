//go:build !windows

package store

import (
	"fmt"
	"os"
	"syscall"
)

func validationCloneFileIdentity(_ string, info os.FileInfo) (string, uint64, error) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return "", 0, fmt.Errorf("validation clone file identity is unavailable")
	}
	return fmt.Sprintf("%x:%x", uint64(stat.Dev), uint64(stat.Ino)), uint64(stat.Nlink), nil
}
