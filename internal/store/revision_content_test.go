package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRevisionContentStoreRejectsTamperedContent(t *testing.T) {
	root := t.TempDir()
	content := NewRevisionContentStore(newIO(root))
	ref, err := content.PutMarkdown(strings.Repeat("正文", 4096))
	if err != nil {
		t.Fatalf("PutMarkdown: %v", err)
	}
	if _, err := content.Read(ref); err != nil {
		t.Fatalf("Read: %v", err)
	}
	path := filepath.Join(root, filepath.FromSlash(revisionContentPath(ref.SHA256, ".md")))
	if err := os.WriteFile(path, []byte("tampered"), 0o644); err != nil {
		t.Fatalf("tamper: %v", err)
	}
	if _, err := content.Read(ref); err == nil || !strings.Contains(err.Error(), "SHA-256") {
		t.Fatalf("tampered content error = %v", err)
	}
}
