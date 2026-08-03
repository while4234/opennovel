//go:build !windows && !linux && !freebsd && !openbsd && !netbsd && !dragonfly && !darwin

package store

import (
	"fmt"
	"os"
)

func authoritativeFileChangeStamp(_ string, info os.FileInfo) (string, error) {
	return fmt.Sprintf("mtime:%d", info.ModTime().UnixNano()), nil
}
