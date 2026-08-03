package web

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/voocel/ainovel-cli/internal/bootstrap"
)

func TestResolveRuntimeRootPriority(t *testing.T) {
	home := testTempDir(t)
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	repo := testTempDir(t)
	if err := os.Mkdir(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}

	cfgRoot := filepath.Join(testTempDir(t), "config")
	envRoot := filepath.Join(testTempDir(t), "env")
	flagRoot := filepath.Join(testTempDir(t), "flag")
	t.Setenv(EnvRuntimeRoot, envRoot)

	got, source, err := ResolveRuntimeRoot(flagRoot, bootstrap.Config{RuntimeRoot: cfgRoot}, repo)
	if err != nil {
		t.Fatalf("flag runtime root: %v", err)
	}
	if source != RuntimeRootSourceFlag || got != mustCanonicalPath(t, flagRoot) {
		t.Fatalf("flag root = %q (%s), want %q", got, source, flagRoot)
	}

	got, source, err = ResolveRuntimeRoot("", bootstrap.Config{RuntimeRoot: cfgRoot}, repo)
	if err != nil {
		t.Fatalf("env runtime root: %v", err)
	}
	if source != RuntimeRootSourceEnv || got != mustCanonicalPath(t, envRoot) {
		t.Fatalf("env root = %q (%s), want %q", got, source, envRoot)
	}

	t.Setenv(EnvRuntimeRoot, "")
	got, source, err = ResolveRuntimeRoot("", bootstrap.Config{RuntimeRoot: cfgRoot}, repo)
	if err != nil {
		t.Fatalf("config runtime root: %v", err)
	}
	if source != RuntimeRootSourceConfig || got != mustCanonicalPath(t, cfgRoot) {
		t.Fatalf("config root = %q (%s), want %q", got, source, cfgRoot)
	}

	got, source, err = ResolveRuntimeRoot("", bootstrap.Config{}, repo)
	if err != nil {
		t.Fatalf("default runtime root: %v", err)
	}
	want := filepath.Join(home, ".ainovel", "novels-preview")
	if source != RuntimeRootSourceDefault || got != mustCanonicalPath(t, want) {
		t.Fatalf("default root = %q (%s), want %q", got, source, want)
	}
}

func mustCanonicalPath(t *testing.T, path string) string {
	t.Helper()
	got, err := canonicalPathForContainment(path)
	if err != nil {
		t.Fatalf("canonicalPathForContainment(%q): %v", path, err)
	}
	return got
}

func TestResolveRuntimeRootRejectsRepositoryPath(t *testing.T) {
	repo := testTempDir(t)
	if err := os.Mkdir(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	insideRepo := filepath.Join(repo, "runtime")

	_, _, err := ResolveRuntimeRoot(insideRepo, bootstrap.Config{}, repo)
	if err == nil {
		t.Fatal("expected repo-local runtime root to be rejected")
	}
	if !strings.Contains(err.Error(), "inside repository") {
		t.Fatalf("error should explain repo rejection, got %v", err)
	}
}

func TestEnsureRuntimeRootCreatesAndRejectsFile(t *testing.T) {
	root := filepath.Join(testTempDir(t), "missing", "novels")
	if err := EnsureRuntimeRoot(root); err != nil {
		t.Fatalf("EnsureRuntimeRoot creates missing root: %v", err)
	}
	if info, err := os.Stat(root); err != nil || !info.IsDir() {
		t.Fatalf("runtime root was not created as directory: info=%v err=%v", info, err)
	}

	filePath := filepath.Join(testTempDir(t), "not-a-dir")
	if err := os.WriteFile(filePath, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := EnsureRuntimeRoot(filePath); err == nil {
		t.Fatal("expected file path runtime root to fail")
	}
}
