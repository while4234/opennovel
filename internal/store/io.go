package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// IO 封装文件系统读写操作，提供加锁和原子写入。
// 每个子存储持有独立的 IO 实例，拥有各自的 sync.RWMutex。
type IO struct {
	dir        string
	mu         sync.RWMutex
	writeFault func(rel, stage string) error
}

func newIO(dir string) *IO {
	return &IO{dir: dir}
}

func (io *IO) path(rel string) string {
	return filepath.Join(io.dir, rel)
}

func (io *IO) ReadFile(rel string) ([]byte, error) {
	io.mu.RLock()
	defer io.mu.RUnlock()
	return io.ReadFileUnlocked(rel)
}

func (io *IO) ReadFileUnlocked(rel string) ([]byte, error) {
	p, err := io.safeRelPath(rel)
	if err != nil {
		return nil, err
	}
	if err := recoverInterruptedReplacement(p); err != nil {
		return nil, err
	}
	return os.ReadFile(p)
}

func (io *IO) WriteFile(rel string, data []byte) error {
	io.mu.Lock()
	defer io.mu.Unlock()
	return io.WriteFileUnlocked(rel, data)
}

func (io *IO) WriteFileUnlocked(rel string, data []byte) error {
	p, err := io.safeRelPath(rel)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(p), filepath.Base(p)+".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o644); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := io.injectWriteFault(rel, "after_temp_sync"); err != nil {
		return fmt.Errorf("write %s after temp sync: %w", rel, err)
	}
	if err := io.replaceFile(rel, tmpPath, p); err != nil {
		return err
	}
	return recordManuscriptMutation(io.dir, rel, data)
}

func (io *IO) injectWriteFault(rel, stage string) error {
	if io.writeFault == nil {
		return nil
	}
	return io.writeFault(filepath.ToSlash(rel), stage)
}

func (io *IO) ReadJSON(rel string, v any) error {
	io.mu.RLock()
	defer io.mu.RUnlock()
	return io.ReadJSONUnlocked(rel, v)
}

func (io *IO) ReadJSONUnlocked(rel string, v any) error {
	data, err := io.ReadFileUnlocked(rel)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, v)
}

func (io *IO) WriteJSON(rel string, v any) error {
	io.mu.Lock()
	defer io.mu.Unlock()
	return io.WriteJSONUnlocked(rel, v)
}

func (io *IO) WriteJSONUnlocked(rel string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return io.WriteFileUnlocked(rel, data)
}

func (io *IO) WriteMarkdown(rel string, content string) error {
	io.mu.Lock()
	defer io.mu.Unlock()
	return io.WriteFileUnlocked(rel, []byte(content))
}

func (io *IO) WriteMarkdownUnlocked(rel string, content string) error {
	return io.WriteFileUnlocked(rel, []byte(content))
}

func (io *IO) AppendLine(rel string, data []byte) error {
	io.mu.Lock()
	defer io.mu.Unlock()
	return io.AppendLineUnlocked(rel, data)
}

func (io *IO) AppendLineUnlocked(rel string, data []byte) error {
	p, err := io.safeRelPath(rel)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	if _, err = f.Write(data); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	return recordManuscriptMutation(io.dir, rel, data)
}

func (io *IO) RemoveFile(rel string) error {
	io.mu.Lock()
	defer io.mu.Unlock()
	return io.RemoveFileUnlocked(rel)
}

func (io *IO) RemoveFileUnlocked(rel string) error {
	p, pathErr := io.safeRelPath(rel)
	if pathErr != nil {
		return pathErr
	}
	if _, err := removeInterruptedReplacementState(p); err != nil {
		return err
	}
	err := os.Remove(p)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	return recordManuscriptMutation(io.dir, rel, nil)
}

func (io *IO) replaceFile(rel, tempPath, targetPath string) error {
	backupPath := targetPath + ".replace-backup"
	if err := recoverInterruptedReplacement(targetPath); err != nil {
		return err
	}
	backedUp := false
	if _, err := os.Stat(targetPath); err == nil {
		if err := os.Rename(targetPath, backupPath); err != nil {
			return fmt.Errorf("prepare file replacement: %w", err)
		}
		backedUp = true
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := io.injectWriteFault(rel, "after_backup"); err != nil {
		if backedUp {
			if rollbackErr := os.Rename(backupPath, targetPath); rollbackErr != nil {
				return fmt.Errorf("write %s after backup: %v (rollback: %w)", rel, err, rollbackErr)
			}
		}
		return fmt.Errorf("write %s after backup: %w", rel, err)
	}
	if err := os.Rename(tempPath, targetPath); err != nil {
		if backedUp {
			_ = os.Rename(backupPath, targetPath)
		}
		return err
	}
	if err := io.injectWriteFault(rel, "after_replace"); err != nil {
		return fmt.Errorf("write %s after replace: %w", rel, err)
	}
	if err := os.Remove(backupPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("clean file replacement backup: %w", err)
	}
	return nil
}

func recoverInterruptedReplacement(targetPath string) error {
	backupPath := targetPath + ".replace-backup"
	_, backupErr := os.Stat(backupPath)
	if os.IsNotExist(backupErr) {
		return nil
	}
	if backupErr != nil {
		return backupErr
	}
	_, targetErr := os.Stat(targetPath)
	switch {
	case os.IsNotExist(targetErr):
		if err := os.Rename(backupPath, targetPath); err != nil {
			return fmt.Errorf("restore interrupted file replacement: %w", err)
		}
		return nil
	case targetErr != nil:
		return targetErr
	default:
		if err := os.Remove(backupPath); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove completed file replacement backup: %w", err)
		}
		return nil
	}
}

func (io *IO) RemoveAllRel(rel string) (bool, error) {
	io.mu.Lock()
	defer io.mu.Unlock()
	return io.RemoveAllRelUnlocked(rel)
}

func (io *IO) RemoveAllRelUnlocked(rel string) (bool, error) {
	target, err := io.safeRelPath(rel)
	if err != nil {
		return false, err
	}
	interruptedExisted, err := removeInterruptedReplacementState(target)
	if err != nil {
		return false, err
	}
	if _, err := os.Lstat(target); err != nil {
		if os.IsNotExist(err) {
			return interruptedExisted, nil
		}
		return false, err
	}
	if err := os.RemoveAll(target); err != nil {
		return true, err
	}
	return true, recordManuscriptMutation(io.dir, rel, nil)
}

func removeInterruptedReplacementState(targetPath string) (bool, error) {
	paths := []string{targetPath + ".replace-backup"}
	temporary, err := filepath.Glob(targetPath + ".tmp-*")
	if err != nil {
		return false, err
	}
	paths = append(paths, temporary...)
	existed := false
	for _, path := range paths {
		if _, err := os.Lstat(path); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return existed, err
		}
		existed = true
		if err := os.RemoveAll(path); err != nil {
			return existed, fmt.Errorf("remove interrupted replacement state %s: %w", filepath.Base(path), err)
		}
	}
	return existed, nil
}

func (io *IO) safeRelPath(rel string) (string, error) {
	trimmed := strings.TrimSpace(rel)
	if trimmed == "" {
		return "", fmt.Errorf("relative path is required")
	}
	cleanRel := filepath.Clean(filepath.FromSlash(trimmed))
	if cleanRel == "." || cleanRel == string(filepath.Separator) {
		return "", fmt.Errorf("refuse to remove project root")
	}
	if filepath.IsAbs(cleanRel) {
		return "", fmt.Errorf("absolute path is not allowed: %s", rel)
	}
	if cleanRel == ".." || strings.HasPrefix(cleanRel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("parent traversal is not allowed: %s", rel)
	}
	root, err := filepath.Abs(io.dir)
	if err != nil {
		return "", err
	}
	target, err := filepath.Abs(filepath.Join(root, cleanRel))
	if err != nil {
		return "", err
	}
	inside, err := pathWithin(root, target)
	if err != nil {
		return "", err
	}
	if !inside {
		return "", fmt.Errorf("path escapes project output: %s", rel)
	}
	return target, nil
}

func pathWithin(root, target string) (bool, error) {
	root = filepath.Clean(root)
	target = filepath.Clean(target)
	if root == target {
		return false, nil
	}
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return false, err
	}
	return rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)), nil
}

func (io *IO) WithWriteLock(fn func() error) error {
	io.mu.Lock()
	defer io.mu.Unlock()
	return fn()
}

// EnsureDirs 创建指定的子目录。
func (io *IO) EnsureDirs(dirs []string) error {
	for _, d := range dirs {
		if err := os.MkdirAll(filepath.Join(io.dir, d), 0o755); err != nil {
			return fmt.Errorf("create dir %s: %w", d, err)
		}
	}
	return nil
}
