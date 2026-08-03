//go:build darwin

package store

import (
	"fmt"
	"os"
	"syscall"
)

func authoritativeFileChangeStamp(_ string, info os.FileInfo) (string, error) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return "", fmt.Errorf("unsupported stat metadata")
	}
	return fmt.Sprintf("%d:%d", stat.Ctimespec.Sec, stat.Ctimespec.Nsec), nil
}
