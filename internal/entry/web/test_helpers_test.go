package web

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/voocel/ainovel-cli/internal/bootstrap"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
)

func TestMain(m *testing.M) {
	authorityRoot, err := os.MkdirTemp("", "ainovel-web-authority-test-")
	if err != nil {
		panic(fmt.Sprintf("create web authority test root: %v", err))
	}
	restore, err := storepkg.ConfigureExpansionAuthorityForTestProcess(authorityRoot)
	if err != nil {
		_ = os.RemoveAll(authorityRoot)
		panic(fmt.Sprintf("configure web authority test root: %v", err))
	}
	code := m.Run()
	restore()
	_ = os.RemoveAll(authorityRoot)
	os.Exit(code)
}

func testWebConfig(t *testing.T) bootstrap.Config {
	t.Helper()
	return bootstrap.Config{
		Provider:    "openai",
		ModelName:   "gpt-test",
		PersistPath: filepath.Join(testTempDir(t), "config.json"),
		Providers: map[string]bootstrap.ProviderConfig{
			"openai": {Type: "openai", APIKey: "sk-test"},
		},
	}
}

func testTempDir(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()
	t.Cleanup(func() {
		removeTempDirWithRetry(t, dir)
	})
	return dir
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
