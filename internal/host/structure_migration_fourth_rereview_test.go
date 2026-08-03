package host

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/voocel/ainovel-cli/assets"
	"github.com/voocel/ainovel-cli/internal/bootstrap"
)

func TestHostNewFailsClosedWhenStructureRecoveryFails(t *testing.T) {
	outputDir := filepath.Join(t.TempDir(), "output")
	writeBrokenStructureMigration(t, outputDir)
	cfg := fourthRereviewHostConfig(outputDir)
	h, err := New(cfg, assets.Load("default"))
	if h != nil {
		t.Cleanup(h.Close)
	}
	if err == nil || !strings.Contains(err.Error(), "recover structure migration") {
		t.Fatalf("Host.New did not fail closed on recovery error: host=%v err=%v", h != nil, err)
	}
}

func TestHostSnapshotFailsClosedWhenStructureRecoveryFails(t *testing.T) {
	h := newSimulationTestHost(t)
	if err := h.store.RunMeta.SetPendingSteer("must-not-leak"); err != nil {
		t.Fatal(err)
	}
	writeBrokenStructureMigration(t, h.store.Dir())

	snapshot := h.Snapshot()
	if snapshot.PendingSteer != "" || snapshot.NovelName != "" || len(snapshot.Outline) != 0 {
		t.Fatalf("Snapshot aggregated state after recovery failure: %+v", snapshot)
	}
	recoveryError := reflect.ValueOf(snapshot).FieldByName("RecoveryError")
	if !recoveryError.IsValid() || recoveryError.Kind() != reflect.String || recoveryError.String() == "" {
		t.Fatalf("Snapshot swallowed recovery failure: field=%v", recoveryError.IsValid())
	}
}

func fourthRereviewHostConfig(outputDir string) bootstrap.Config {
	return bootstrap.Config{
		OutputDir: outputDir,
		Provider:  "openai",
		ModelName: "gpt-test",
		Providers: map[string]bootstrap.ProviderConfig{
			"openai": {Type: "openai", APIKey: "sk-test"},
		},
	}
}

func writeBrokenStructureMigration(t *testing.T, outputDir string) {
	t.Helper()
	path := filepath.Join(outputDir, "meta", "structure", "migration.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"version":`), 0o644); err != nil {
		t.Fatal(err)
	}
}
