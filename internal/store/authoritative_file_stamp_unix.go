//go:build linux || freebsd || openbsd || netbsd || dragonfly

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
	return fmt.Sprintf("%d:%d", stat.Ctim.Sec, stat.Ctim.Nsec), nil
}
