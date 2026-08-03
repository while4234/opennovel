//go:build !windows

package store

import "os"

func syncAuthorityDirectoryPlatform(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
