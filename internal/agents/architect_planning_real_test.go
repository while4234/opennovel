package agents

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/assets"
	"github.com/voocel/ainovel-cli/internal/globalprompt"
	"github.com/voocel/ainovel-cli/internal/store"
	toolpkg "github.com/voocel/ainovel-cli/internal/tools"
)

func TestRealProjectPlanningVolumeFitsCompiledArchitectRequest(t *testing.T) {
	source := strings.TrimSpace(os.Getenv("AINOVEL_REAL_PLANNING_OUTPUT"))
	if source == "" {
		t.Skip("set AINOVEL_REAL_PLANNING_OUTPUT to a project output directory")
	}
	copyRoot := filepath.Join(t.TempDir(), "output")
	if err := copyArchitectPlanningFixture(source, copyRoot); err != nil {
		t.Fatalf("copy real planning fixture: %v", err)
	}

	st := store.NewStore(copyRoot)
	bundle := assets.Load("suspense")
	contextTool := toolpkg.NewContextToolWithOptions(st, bundle.References, "suspense", toolpkg.ContextToolOptions{
		Role: "architect",
	})
	args := json.RawMessage(`{"scope":"planning_volume","volume":1}`)
	contextResult, err := contextTool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("execute planning_volume: %v", err)
	}

	task := readFirstArchitectTask(t, filepath.Join(copyRoot, "meta", "sessions", "agents", "architect_long-011.jsonl"))
	task = strings.ReplaceAll(task, "novel_context(scope=planning)", "novel_context(scope=planning_volume, volume=1)")
	callID := "real-planning-volume"
	messages := []agentcore.Message{
		agentcore.SystemMsg(globalprompt.ApplyForModel("grok-4.5", bundle.Prompts.ArchitectLong)),
		agentcore.UserMsg(task),
		{
			Role: agentcore.RoleAssistant,
			Content: []agentcore.ContentBlock{agentcore.ToolCallBlock(agentcore.ToolCall{
				ID: callID, Name: contextTool.Name(), Args: args,
			})},
		},
		agentcore.ToolResultMsg(callID, contextResult, false),
	}
	saveTool := toolpkg.NewArchitectSaveFoundationTool(st)
	toolSpecs := []agentcore.ToolSpec{
		{Name: contextTool.Name(), Description: contextTool.Description(), Parameters: contextTool.Schema()},
		{Name: saveTool.Name(), Description: saveTool.Description(), Parameters: saveTool.Schema()},
	}
	compiled, err := compileAgentInput(messages, toolSpecs)
	if err != nil {
		t.Fatalf("compile real Architect request: %v", err)
	}
	const acceptanceHeadroomBytes = 80 * 1024
	if len(compiled) > acceptanceHeadroomBytes {
		t.Fatalf("compiled Architect request=%d bytes, want <=%d with headroom below production limit %d", len(compiled), acceptanceHeadroomBytes, architectLongInputLimitBytes)
	}
	t.Logf("planning_volume=%d compiled=%d limit=%d", len(contextResult), len(compiled), architectLongInputLimitBytes)
}

func readFirstArchitectTask(t *testing.T, path string) string {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open real Architect session: %v", err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	if !scanner.Scan() {
		t.Fatalf("read first Architect session message: %v", scanner.Err())
	}
	var message agentcore.Message
	if err := json.Unmarshal(scanner.Bytes(), &message); err != nil {
		t.Fatalf("decode first Architect task: %v", err)
	}
	return message.TextContent()
}

func copyArchitectPlanningFixture(source, target string) error {
	return filepath.Walk(source, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		destination := filepath.Join(target, relative)
		if info.IsDir() {
			return os.MkdirAll(destination, info.Mode())
		}
		if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
			return err
		}
		input, err := os.Open(path)
		if err != nil {
			return err
		}
		output, err := os.OpenFile(destination, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, info.Mode())
		if err != nil {
			input.Close()
			return err
		}
		_, copyErr := io.Copy(output, input)
		inputCloseErr := input.Close()
		outputCloseErr := output.Close()
		if copyErr != nil {
			return copyErr
		}
		if inputCloseErr != nil {
			return inputCloseErr
		}
		return outputCloseErr
	})
}
