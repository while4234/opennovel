package store

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	revisionLockFile    = "meta/revisions/transaction.lock"
	revisionLockTimeout = 15 * time.Second
)

// withRevisionTransaction serializes readers and writers through a lock file
// located inside the project itself. Therefore symlink, junction, case, drive,
// and short-name aliases all contend on the same filesystem object.
func (s *RevisionStore) withRevisionTransaction(fn func() error) error {
	if s == nil || s.io == nil {
		return fmt.Errorf("revision store is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return withRevisionFileTransaction(s.io, revisionLockFile, fn)
}

func withRevisionFileTransaction(io *IO, lockFile string, fn func() error) error {
	if io == nil {
		return fmt.Errorf("revision transaction store is required")
	}
	lockPath := io.path(lockFile)
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o755); err != nil {
		return err
	}
	file, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("open revision transaction lock: %w", err)
	}
	defer file.Close()
	deadline := time.Now().Add(revisionLockTimeout)
	for {
		acquired, err := tryRevisionFileLock(file)
		if err != nil {
			return fmt.Errorf("acquire revision transaction: %w", err)
		}
		if acquired {
			defer func() {
				if unlockErr := unlockRevisionFile(file); unlockErr != nil {
					// There is no useful recovery action here. Closing the handle below
					// still releases an OS-owned lock without deleting a successor file.
					_ = unlockErr
				}
			}()
			if err := writeRevisionLockOwner(file); err != nil {
				return err
			}
			return fn()
		}
		if time.Now().After(deadline) {
			if _, err := file.Seek(0, 0); err != nil {
				return fmt.Errorf("read revision transaction owner: %w", err)
			}
			owner, _ := os.ReadFile(lockPath)
			return fmt.Errorf("revision transaction is busy (owner %s)", strings.TrimSpace(string(owner)))
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func writeRevisionLockOwner(file *os.File) error {
	if err := file.Truncate(0); err != nil {
		return fmt.Errorf("truncate revision transaction owner: %w", err)
	}
	if _, err := file.Seek(0, 0); err != nil {
		return fmt.Errorf("seek revision transaction owner: %w", err)
	}
	_, writeErr := file.WriteString(strconv.Itoa(os.Getpid()) + "\n" + time.Now().UTC().Format(time.RFC3339Nano))
	return errors.Join(writeErr, file.Sync())
}
