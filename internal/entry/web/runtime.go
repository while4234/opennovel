package web

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/voocel/ainovel-cli/internal/bootstrap"
)

const (
	DefaultHost    = "127.0.0.1"
	DefaultPort    = 9999
	EnvRuntimeRoot = "AINOVEL_RUNTIME_ROOT"
)

type RuntimeRootSource string

const (
	RuntimeRootSourceFlag    RuntimeRootSource = "flag"
	RuntimeRootSourceEnv     RuntimeRootSource = "env"
	RuntimeRootSourceConfig  RuntimeRootSource = "config"
	RuntimeRootSourceDefault RuntimeRootSource = "default"
)

func DefaultRuntimeRoot() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home dir for default runtime root: %w", err)
	}
	return filepath.Join(home, ".ainovel", "novels-preview"), nil
}

func ResolveRuntimeRoot(flagValue string, cfg bootstrap.Config, repoRoot string) (string, RuntimeRootSource, error) {
	raw := strings.TrimSpace(flagValue)
	source := RuntimeRootSourceFlag
	if raw == "" {
		raw = strings.TrimSpace(os.Getenv(EnvRuntimeRoot))
		source = RuntimeRootSourceEnv
	}
	if raw == "" {
		raw = strings.TrimSpace(cfg.RuntimeRoot)
		source = RuntimeRootSourceConfig
	}
	if raw == "" {
		var err error
		raw, err = DefaultRuntimeRoot()
		if err != nil {
			return "", "", err
		}
		source = RuntimeRootSourceDefault
	}

	root, err := canonicalPathForContainment(raw)
	if err != nil {
		return "", "", fmt.Errorf("resolve runtime root %q: %w", raw, err)
	}
	if err := rejectRepoPath(root, repoRoot); err != nil {
		return "", "", err
	}
	return root, source, nil
}

func EnsureRuntimeRoot(root string) error {
	root = strings.TrimSpace(root)
	if root == "" {
		return fmt.Errorf("runtime root is required")
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return fmt.Errorf("create runtime root %s: %w", root, err)
	}
	for _, dir := range []string{
		filepath.Join(root, "simulation_library"),
		filepath.Join(root, "novel_library"),
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create runtime library dir %s: %w", dir, err)
		}
	}
	info, err := os.Stat(root)
	if err != nil {
		return fmt.Errorf("inspect runtime root %s: %w", root, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("runtime root path is not a directory: %s", root)
	}
	probe, err := os.CreateTemp(root, ".ainovel-write-test-*")
	if err != nil {
		return fmt.Errorf("runtime root is not writable: %s: %w", root, err)
	}
	probePath := probe.Name()
	closeErr := probe.Close()
	removeErr := os.Remove(probePath)
	if closeErr != nil {
		return fmt.Errorf("runtime root write test failed: %s: %w", root, closeErr)
	}
	if removeErr != nil {
		return fmt.Errorf("runtime root cleanup failed: %s: %w", root, removeErr)
	}
	return nil
}

func FindRepositoryRoot(start string) string {
	start = strings.TrimSpace(start)
	if start == "" {
		var err error
		start, err = os.Getwd()
		if err != nil {
			return ""
		}
	}
	dir, err := filepath.Abs(start)
	if err != nil {
		return ""
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

func rejectRepoPath(root, repoRoot string) error {
	repoRoot = strings.TrimSpace(repoRoot)
	if repoRoot == "" {
		return nil
	}
	repo, err := canonicalPathForContainment(repoRoot)
	if err != nil {
		return fmt.Errorf("resolve repository root %q: %w", repoRoot, err)
	}
	if isSameOrChild(repo, root) {
		return fmt.Errorf("runtime root %s is inside repository %s; choose a path outside the repo", root, repo)
	}
	return nil
}

func canonicalPathForContainment(path string) (string, error) {
	expanded, err := expandHome(path)
	if err != nil {
		return "", err
	}
	abs, err := filepath.Abs(expanded)
	if err != nil {
		return "", err
	}
	abs = filepath.Clean(abs)
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return filepath.Clean(resolved), nil
	}

	var missing []string
	probe := abs
	for {
		if _, err := os.Lstat(probe); err == nil {
			resolved, err := filepath.EvalSymlinks(probe)
			if err != nil {
				return "", err
			}
			for i := len(missing) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, missing[i])
			}
			return filepath.Clean(resolved), nil
		} else if !os.IsNotExist(err) {
			return "", err
		}
		parent := filepath.Dir(probe)
		if parent == probe {
			return abs, nil
		}
		missing = append(missing, filepath.Base(probe))
		probe = parent
	}
}

func expandHome(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", fmt.Errorf("path is empty")
	}
	if path == "~" || strings.HasPrefix(path, "~/") || strings.HasPrefix(path, `~\`) {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		if path == "~" {
			return home, nil
		}
		return filepath.Join(home, path[2:]), nil
	}
	return path, nil
}

func isSameOrChild(parent, child string) bool {
	parent = normalizePathForCompare(parent)
	child = normalizePathForCompare(child)
	if parent == child {
		return true
	}
	rel, err := filepath.Rel(parent, child)
	if err != nil {
		return false
	}
	return rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func normalizePathForCompare(path string) string {
	path = filepath.Clean(path)
	if runtime.GOOS == "windows" {
		path = strings.ToLower(path)
	}
	return path
}
