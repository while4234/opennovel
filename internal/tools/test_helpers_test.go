package tools

import (
	"os"
	"testing"
	"time"

	"github.com/voocel/ainovel-cli/internal/rules"
	"github.com/voocel/ainovel-cli/internal/store"
)

func testStoreDir(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()
	t.Cleanup(func() {
		removeTempDirWithRetry(t, dir)
	})
	return dir
}

func saveChapterWordRange(t *testing.T, st *store.Store, minWords, maxWords int) {
	t.Helper()
	if err := st.UserRules.Save(&rules.Snapshot{
		Version: rules.SnapshotVersion,
		Status:  rules.StatusReady,
		Structured: rules.Structured{
			ChapterWords: &rules.WordRange{Min: minWords, Max: maxWords},
		},
	}); err != nil {
		t.Fatalf("save chapter word range: %v", err)
	}
}

func removeTempDirWithRetry(t *testing.T, dir string) {
	t.Helper()

	var err error
	for attempt := 0; attempt < 5; attempt++ {
		err = os.RemoveAll(dir)
		if err == nil || os.IsNotExist(err) {
			return
		}
		time.Sleep(time.Duration(attempt+1) * 25 * time.Millisecond)
	}
	t.Logf("pre-clean temp dir %q: %v", dir, err)
}
