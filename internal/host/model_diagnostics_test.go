package host

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/assets"
	"github.com/voocel/ainovel-cli/internal/bootstrap"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/modeldiag"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
)

func TestWriterDiagnosticKeepsRequestBindingOnDecodeFailure(t *testing.T) {
	root := t.TempDir()
	st := storepkg.NewStore(root)
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	responseText := `{"chapter_id":"ch_expected","attempt":1,"segment":1,"prose":"candidate","complete":false,"unexpected":true}`
	model := &diagnosticScriptedModel{output: responseText, usage: &agentcore.Usage{Input: 41, Output: 17}}
	writer := &modelManuscriptWriter{model: model, prompts: assets.Load("default").Prompts, store: st}
	_, err := writer.GenerateManuscriptSegment(t.Context(), domain.ManuscriptRevisionRuntime{Mode: domain.RevisionModeNormal}, domain.ManuscriptReworkItem{ChapterID: "ch_expected"}, ManuscriptGenerationContext{CurrentProse: "authoritative prose"}, 1, 1, "")
	if err == nil {
		t.Fatal("invalid writer envelope was accepted")
	}
	records := readHostDiagnostics(t, root)
	if len(records) != 1 {
		t.Fatalf("records=%d, want one", len(records))
	}
	got := records[0]
	if got.Status != modeldiag.StatusDecodeError || got.ContentSignature != domain.ContentSignature([]byte(model.userPayload)) {
		t.Fatalf("decode diagnostic lost request binding: %+v", got)
	}
	if got.OutputSignature != domain.ContentSignature([]byte(responseText)) || !got.UsagePresent || got.ActualInputTokens != 41 || got.ActualOutputTokens != 17 {
		t.Fatalf("decode diagnostic output/usage = %+v", got)
	}
}

func TestCoCreateStreamDiagnosticUsesFinalStreamUsage(t *testing.T) {
	root := t.TempDir()
	st := storepkg.NewStore(root)
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	output := coCreateXMLWithSuggestions("ready", "continue")
	usage := &agentcore.Usage{Input: 33, Output: 9}
	model := &scriptedCoCreateModel{streams: [][]agentcore.StreamEvent{{
		{Type: agentcore.StreamEventTextDelta, Delta: output},
		{Type: agentcore.StreamEventDone, Message: agentcore.Message{Usage: usage}},
	}}}
	if _, err := coCreateStream(t.Context(), newCoCreateModelSet(model), nil, time.Second, bootstrap.DefaultCoCreateMaxTokens, "system", []CoCreateMessage{{Role: "user", Content: "private idea"}}, nil, st); err != nil {
		t.Fatal(err)
	}
	records := readHostDiagnostics(t, root)
	if len(records) != 1 || records[0].Task != "cocreate_stream" || records[0].ActualInputTokens != 33 || records[0].ActualOutputTokens != 9 || records[0].OutputSignature != domain.ContentSignature([]byte(output)) {
		t.Fatalf("co-create diagnostic = %+v", records)
	}
}

type diagnosticScriptedModel struct {
	output      string
	usage       *agentcore.Usage
	userPayload string
}

func (m *diagnosticScriptedModel) Generate(_ context.Context, messages []agentcore.Message, _ []agentcore.ToolSpec, _ ...agentcore.CallOption) (*agentcore.LLMResponse, error) {
	if len(messages) > 1 {
		m.userPayload = messages[len(messages)-1].TextContent()
	}
	return &agentcore.LLMResponse{Message: agentcore.Message{Role: agentcore.RoleAssistant, Content: []agentcore.ContentBlock{agentcore.TextBlock(m.output)}, Usage: m.usage}}, nil
}

func (*diagnosticScriptedModel) GenerateStream(context.Context, []agentcore.Message, []agentcore.ToolSpec, ...agentcore.CallOption) (<-chan agentcore.StreamEvent, error) {
	return nil, errors.New("unused")
}

func (*diagnosticScriptedModel) SupportsTools() bool { return false }

func readHostDiagnostics(t *testing.T, root string) []storepkg.ManuscriptContextDiagnostic {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(root, "meta", "manuscript", "context-diagnostics.json"))
	if err != nil {
		t.Fatal(err)
	}
	var records []storepkg.ManuscriptContextDiagnostic
	if err := json.Unmarshal(raw, &records); err != nil {
		t.Fatal(err)
	}
	return records
}
