package store

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const manuscriptMutationGenerationPath = "meta/manuscript/content-mutation-generation.json"

type manuscriptMutationGeneration struct {
	Version           int    `json:"version"`
	Generation        uint64 `json:"generation"`
	LastPath          string `json:"last_path"`
	LastContentSHA256 string `json:"last_content_sha256"`
}

var manuscriptMutationGenerationLocks sync.Map

func recordManuscriptMutation(root, relative string, content []byte) error {
	relative = filepath.ToSlash(filepath.Clean(relative))
	if !isAuthoritativeManuscriptPath(relative) || relative == manuscriptMutationGenerationPath {
		return nil
	}
	lockValue, _ := manuscriptMutationGenerationLocks.LoadOrStore(filepath.Clean(root), &sync.Mutex{})
	lock := lockValue.(*sync.Mutex)
	lock.Lock()
	defer lock.Unlock()
	path := filepath.Join(root, filepath.FromSlash(manuscriptMutationGenerationPath))
	var journal manuscriptMutationGeneration
	if payload, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(payload, &journal); err != nil {
			return fmt.Errorf("decode manuscript mutation generation: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	digest := sha256.Sum256(content)
	journal.Version = 1
	journal.Generation++
	journal.LastPath = relative
	journal.LastContentSHA256 = hex.EncodeToString(digest[:])
	payload, err := json.Marshal(journal)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".content-generation-*")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if _, err := temp.Write(payload); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return replaceAtomicFile(tempPath, path)
}

func isAuthoritativeManuscriptPath(relative string) bool {
	path := strings.ToLower(filepath.ToSlash(relative))
	if path == "outline.json" || path == "layered_outline.json" {
		return true
	}
	for _, prefix := range []string{"chapters/", "summaries/", "reviews/", "meta/structure/chapters/", "meta/revisions/manuscript/"} {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	return false
}

func replaceAtomicFile(tempPath, targetPath string) error {
	if err := os.Rename(tempPath, targetPath); err == nil {
		return nil
	}
	if err := os.Remove(targetPath); err != nil && !os.IsNotExist(err) {
		return err
	}
	return os.Rename(tempPath, targetPath)
}
