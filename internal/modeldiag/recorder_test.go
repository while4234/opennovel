package modeldiag

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/store"
)

func TestRecorderPersistsActualUsageAndOnlyMetadata(t *testing.T) {
	root := t.TempDir()
	st := store.NewStore(root)
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	requestText := "private request prose"
	outputText := "private model output"
	recorder, err := Begin(Request{Store: st, Task: "test_model", System: "private system", User: []byte(requestText), InputLimitBytes: 1024, OutputLimitTokens: 50})
	if err != nil {
		t.Fatal(err)
	}
	usage := &agentcore.Usage{Input: 12, Output: 7}
	if err := recorder.Finish(StatusCompleted, outputText, usage); err != nil {
		t.Fatal(err)
	}
	if err := recorder.Finish(StatusProviderError, "must not append twice", nil); err != nil {
		t.Fatal(err)
	}
	records := readRecords(t, root)
	if len(records) != 1 {
		t.Fatalf("records=%d, want exactly one final record", len(records))
	}
	got := records[0]
	if !got.UsagePresent || got.ActualInputTokens != 12 || got.ActualOutputTokens != 7 || got.OutputRunes != len([]rune(outputText)) {
		t.Fatalf("actual usage/output metadata = %+v", got)
	}
	if got.ContentSignature != domain.ContentSignature([]byte(requestText)) || got.OutputSignature != domain.ContentSignature([]byte(outputText)) {
		t.Fatalf("signatures = request %q output %q", got.ContentSignature, got.OutputSignature)
	}
	raw, err := os.ReadFile(filepath.Join(root, "meta", "manuscript", "context-diagnostics.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), requestText) || strings.Contains(string(raw), outputText) || strings.Contains(string(raw), "private system") {
		t.Fatalf("diagnostic leaked model content: %s", raw)
	}
}

func TestRecorderRejectsBudgetBeforeAnyModelCall(t *testing.T) {
	root := t.TempDir()
	st := store.NewStore(root)
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	if recorder, err := Begin(Request{Store: st, Task: "over_budget", System: "12345", User: []byte("67890"), InputLimitBytes: 9}); err == nil || recorder != nil {
		t.Fatalf("Begin recorder=%v err=%v", recorder, err)
	}
	records := readRecords(t, root)
	if len(records) != 1 || records[0].Status != StatusRejectedBudget || records[0].InputBytes != 10 {
		t.Fatalf("budget records = %+v", records)
	}
}

func readRecords(t *testing.T, root string) []store.ManuscriptContextDiagnostic {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(root, "meta", "manuscript", "context-diagnostics.json"))
	if err != nil {
		t.Fatal(err)
	}
	var records []store.ManuscriptContextDiagnostic
	if err := json.Unmarshal(raw, &records); err != nil {
		t.Fatal(err)
	}
	return records
}
