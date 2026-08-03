//go:build !windows

package store

import (
	"fmt"
	"os"
	"syscall"
)

func publicationRootFileIdentity(path string, info os.FileInfo) (string, error) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return "", fmt.Errorf("publication root file identity is unavailable for %q", path)
	}
	return fmt.Sprintf("%x:%x", uint64(stat.Dev), uint64(stat.Ino)), nil
}
