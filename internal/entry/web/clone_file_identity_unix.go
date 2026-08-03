//go:build !windows

package web

import (
	"fmt"
	"io/fs"
	"syscall"
)

func cloneFileIdentity(_ string, info fs.FileInfo) (string, uint64, error) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return "", 0, fmt.Errorf("unsupported file identity metadata")
	}
	return fmt.Sprintf("%x:%x", uint64(stat.Dev), uint64(stat.Ino)), uint64(stat.Nlink), nil
}

func validationCloneFileGeneration(info fs.FileInfo) string {
	return fmt.Sprintf("%d:%d", info.Size(), info.ModTime().UnixNano())
}
