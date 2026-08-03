package imp

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/internal/modeldiag"
	"github.com/voocel/ainovel-cli/internal/store"
)

func TestStructuredCallPersistsOneInvalidSchemaDiagnostic(t *testing.T) {
	root := t.TempDir()
	st := store.NewStore(root)
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	model := &impDiagnosticModel{output: "well-formed but contract-invalid", usage: &agentcore.Usage{Input: 21, Output: 8}}
	ctx := modeldiag.WithStore(t.Context(), st)
	_, err := runStructuredCall(ctx, model, []agentcore.Message{agentcore.SystemMsg("private system"), agentcore.UserMsg("private chapter")}, func(string) (string, error) {
		return "", errors.New("required summary is empty")
	}, StructuredCallOptions{MaxAttempts: 1, DisableStream: true, MaxTokens: 200})
	if err == nil {
		t.Fatal("invalid structured output was accepted")
	}
	raw, err := os.ReadFile(filepath.Join(root, "meta", "manuscript", "context-diagnostics.json"))
	if err != nil {
		t.Fatal(err)
	}
	var records []store.ManuscriptContextDiagnostic
	if err := json.Unmarshal(raw, &records); err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].Status != modeldiag.StatusInvalidSchema || records[0].ActualInputTokens != 21 || records[0].ActualOutputTokens != 8 {
		t.Fatalf("diagnostics = %+v", records)
	}
}

type impDiagnosticModel struct {
	output string
	usage  *agentcore.Usage
}

func (m *impDiagnosticModel) Generate(context.Context, []agentcore.Message, []agentcore.ToolSpec, ...agentcore.CallOption) (*agentcore.LLMResponse, error) {
	return &agentcore.LLMResponse{Message: agentcore.Message{Role: agentcore.RoleAssistant, Content: []agentcore.ContentBlock{agentcore.TextBlock(m.output)}, Usage: m.usage}}, nil
}
