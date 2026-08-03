package host

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/voocel/ainovel-cli/assets"
	"github.com/voocel/ainovel-cli/internal/bootstrap"
	"github.com/voocel/ainovel-cli/internal/host/sim"
)

func TestSimulateFromDirUsesExplicitDir(t *testing.T) {
	h := newSimulationTestHost(t)
	sourceDir := filepath.Join(t.TempDir(), "explicit-simulate")

	events, err := h.SimulateFromDir(context.Background(), sourceDir)
	if err != nil {
		t.Fatalf("SimulateFromDir: %v", err)
	}
	err = firstSimulationError(events)
	if err == nil || !strings.Contains(err.Error(), sourceDir) {
		t.Fatalf("expected error to reference explicit dir %q, got %v", sourceDir, err)
	}
}

func TestSimulateKeepsWorkingDirectorySimulateDir(t *testing.T) {
	h := newSimulationTestHost(t)
	wd := t.TempDir()
	t.Chdir(wd)
	expected := filepath.Join(wd, "simulate")

	events, err := h.Simulate(context.Background())
	if err != nil {
		t.Fatalf("Simulate: %v", err)
	}
	err = firstSimulationError(events)
	if err == nil || !strings.Contains(err.Error(), expected) {
		t.Fatalf("expected error to reference cwd simulate dir %q, got %v", expected, err)
	}
}

func newSimulationTestHost(t *testing.T) *Host {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	cfg := bootstrap.Config{
		OutputDir: filepath.Join(t.TempDir(), "output"),
		Provider:  "openai",
		ModelName: "gpt-test",
		Providers: map[string]bootstrap.ProviderConfig{
			"openai": {Type: "openai", APIKey: "sk-test"},
		},
	}
	h, err := New(cfg, assets.Load("default"))
	if err != nil {
		t.Fatalf("host.New: %v", err)
	}
	t.Cleanup(h.Close)
	return h
}

func firstSimulationError(events <-chan sim.Event) error {
	var first error
	for ev := range events {
		if first == nil && ev.Err != nil {
			first = ev.Err
		}
	}
	return first
}
