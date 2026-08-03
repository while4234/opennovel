package modeldiag

import (
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
)

func TestProductionModelCallInventoryIsExplicit(t *testing.T) {
	root := repositoryRoot(t)
	want := []string{
		"internal/agents/character_model.go",
		"internal/agents/context_manager.go",
		"internal/agents/model_boundary.go",
		"internal/agents/toolcall_repair_model.go",
		"internal/bootstrap/models.go",
		"internal/bootstrap/provider_factory.go",
		"internal/bootstrap/runtime_fallback.go",
		"internal/globalprompt/model.go",
		"internal/host/adapt/runner.go",
		"internal/host/adapt/semantic_audit.go",
		"internal/host/chapter_outline_revision.go",
		"internal/host/cocreate.go",
		"internal/host/continuation_planner.go",
		"internal/host/expansion_model.go",
		"internal/host/imp/analyzer.go",
		"internal/host/imp/foundation.go",
		"internal/host/imp/structured_call.go",
		"internal/host/manuscript_model.go",
		"internal/host/manuscript_action_dialogue.go",
		"internal/host/model_probe.go",
		"internal/host/semantic_audit.go",
		"internal/host/sim/retry.go",
		"internal/tools/check_consistency.go",
		"internal/userrules/normalize.go",
	}
	got := directModelCallFiles(t, root)
	sort.Strings(want)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("production model-call inventory changed; classify every new direct site before accepting it\nwant:\n%s\ngot:\n%s", strings.Join(want, "\n"), strings.Join(got, "\n"))
	}

	build, err := os.ReadFile(filepath.Join(root, "internal", "agents", "build.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, owner := range []string{"agent_coordinator", "agent_architect_short", "agent_architect_long", "agent_writer", "agent_editor"} {
		if !strings.Contains(string(build), "withProductionAgentBoundary") || !strings.Contains(string(build), `"`+owner+`"`) {
			t.Fatalf("normal agentcore route %s has no explicit production boundary", owner)
		}
	}
}

func directModelCallFiles(t *testing.T, root string) []string {
	t.Helper()
	var files []string
	err := filepath.Walk(filepath.Join(root, "internal"), func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		payload, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		text := string(payload)
		if strings.Contains(text, ".Generate(") || strings.Contains(text, ".GenerateStream(") {
			rel, relErr := filepath.Rel(root, path)
			if relErr != nil {
				return relErr
			}
			files = append(files, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(files)
	return files
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot resolve inventory source path")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(source), "..", ".."))
}
