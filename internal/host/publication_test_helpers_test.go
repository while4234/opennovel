package host

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	storepkg "github.com/voocel/ainovel-cli/internal/store"
)

var publicationTestAuthorityRoot string

func TestMain(m *testing.M) {
	authorityRoot, err := os.MkdirTemp("", "ainovel-host-authority-test-")
	if err != nil {
		panic(fmt.Sprintf("create host authority test root: %v", err))
	}
	restore, err := storepkg.ConfigureExpansionAuthorityForTestProcess(authorityRoot)
	if err != nil {
		_ = os.RemoveAll(authorityRoot)
		panic(fmt.Sprintf("configure host authority test root: %v", err))
	}
	publicationTestAuthorityRoot = authorityRoot
	code := m.Run()
	restore()
	_ = os.RemoveAll(authorityRoot)
	os.Exit(code)
}

type publicationAuthorityRegistrySnapshot struct {
	Bytes  []byte
	Mode   os.FileMode
	SHA256 [sha256.Size]byte
}

func snapshotPublicationAuthorityRegistry(t *testing.T) map[string]publicationAuthorityRegistrySnapshot {
	t.Helper()
	result := make(map[string]publicationAuthorityRegistrySnapshot)
	root := filepath.Join(publicationTestAuthorityRoot, "projects")
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			if os.IsNotExist(walkErr) {
				return nil
			}
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		result[filepath.Base(path)] = publicationAuthorityRegistrySnapshot{
			Bytes: append([]byte(nil), data...), Mode: info.Mode().Perm(), SHA256: sha256.Sum256(data),
		}
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return result
}

func newPublicationTestStore(t *testing.T) *storepkg.Store {
	t.Helper()
	outputDir := filepath.Join(t.TempDir(), "output")
	if err := os.MkdirAll(outputDir, 0o700); err != nil {
		t.Fatal(err)
	}
	rootDir := filepath.Dir(outputDir)
	projectID := "host-test-" + filepath.Base(rootDir)
	now := time.Now().UTC()
	manifest := struct {
		Version        int       `json:"version"`
		ID             string    `json:"id"`
		Name           string    `json:"name"`
		RootDir        string    `json:"root_dir"`
		OutputDir      string    `json:"output_dir"`
		CreatedAt      time.Time `json:"created_at"`
		UpdatedAt      time.Time `json:"updated_at"`
		LastAccessedAt time.Time `json:"last_accessed_at"`
	}{1, projectID, projectID, rootDir, outputDir, now, now, now}
	payload, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rootDir, "project.json"), payload, 0o600); err != nil {
		t.Fatal(err)
	}
	return storepkg.NewStore(outputDir)
}
