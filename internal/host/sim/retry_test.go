package sim

import (
	"context"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/voocel/agentcore"
)

func TestStructuredJSONCallUsesIndependentModelAndStructureBudgets(t *testing.T) {
	llm := &scriptedLLM{
		responses: []string{"bad json", "", "", `{"ok":true}`},
		errors:    []error{nil, io.ErrUnexpectedEOF, io.ErrUnexpectedEOF, nil},
	}
	var retries []structuredJSONRetryEvent

	got, err := runStructuredJSONCall(context.Background(), llm, []agentcore.Message{agentcore.UserMsg("start")}, parseOKFlag, structuredJSONCallOptions{
		ModelCallMaxAttempts:       3,
		StructureRepairMaxAttempts: 1,
		OnRetry: func(ev structuredJSONRetryEvent) {
			retries = append(retries, ev)
		},
		Sleep: noStructuredJSONTestSleep,
	})
	if err != nil {
		t.Fatalf("runStructuredJSONCall: %v", err)
	}
	if !got {
		t.Fatal("parsed value = false, want true")
	}
	if calls := llm.calls.Load(); calls != 4 {
		t.Fatalf("calls = %d, want initial structure failure plus 3 independent model attempts", calls)
	}
	if len(retries) != 3 {
		t.Fatalf("retry events = %+v, want structure repair plus two model retries", retries)
	}
	if retries[0].Kind != structuredJSONRetryKindStructureRepair || retries[0].Attempt != 1 || retries[0].MaxAttempts != 1 {
		t.Fatalf("first retry = %+v, want structure repair 1/1", retries[0])
	}
	for i, ev := range retries[1:] {
		if ev.Kind != structuredJSONRetryKindModelCall || ev.MaxAttempts != 3 || ev.Attempt != i+2 {
			t.Fatalf("model retry %d = %+v, want model_call %d/3", i+1, ev, i+2)
		}
	}
}

func TestStructuredJSONCallHonorsStructureRepairBudget(t *testing.T) {
	llm := &scriptedLLM{responses: []string{"bad json", "still bad", `{"ok":true}`}}
	var retries []structuredJSONRetryEvent

	got, err := runStructuredJSONCall(context.Background(), llm, []agentcore.Message{agentcore.UserMsg("start")}, parseOKFlag, structuredJSONCallOptions{
		ModelCallMaxAttempts:       1,
		StructureRepairMaxAttempts: 2,
		OnRetry: func(ev structuredJSONRetryEvent) {
			retries = append(retries, ev)
		},
		Sleep: noStructuredJSONTestSleep,
	})
	if err != nil {
		t.Fatalf("runStructuredJSONCall: %v", err)
	}
	if !got {
		t.Fatal("parsed value = false, want true")
	}
	if calls := llm.calls.Load(); calls != 3 {
		t.Fatalf("calls = %d, want initial response plus two structure repairs", calls)
	}
	if len(retries) != 2 {
		t.Fatalf("retry events = %+v, want two structure repairs", retries)
	}
	for i, ev := range retries {
		if ev.Kind != structuredJSONRetryKindStructureRepair || ev.Attempt != i+1 || ev.MaxAttempts != 2 {
			t.Fatalf("structure retry %d = %+v, want structure_repair %d/2", i+1, ev, i+1)
		}
	}
}

func TestStructuredJSONCallRetriesProviderGatewayErrors(t *testing.T) {
	llm := &scriptedLLM{
		responses: []string{"", `{"ok":true}`},
		errors:    []error{fmt.Errorf("provider gateway error: 503 Service Unavailable"), nil},
	}
	var retries []structuredJSONRetryEvent

	got, err := runStructuredJSONCall(context.Background(), llm, []agentcore.Message{agentcore.UserMsg("start")}, parseOKFlag, structuredJSONCallOptions{
		ModelCallMaxAttempts:       3,
		StructureRepairMaxAttempts: 1,
		OnRetry: func(ev structuredJSONRetryEvent) {
			retries = append(retries, ev)
		},
		Sleep: noStructuredJSONTestSleep,
	})
	if err != nil {
		t.Fatalf("runStructuredJSONCall: %v", err)
	}
	if !got {
		t.Fatal("parsed value = false, want true")
	}
	if calls := llm.calls.Load(); calls != 2 {
		t.Fatalf("calls = %d, want retry after gateway error", calls)
	}
	if len(retries) != 1 {
		t.Fatalf("retry events = %+v, want one model-call retry", retries)
	}
	if retries[0].Kind != structuredJSONRetryKindModelCall || retries[0].Attempt != 2 || retries[0].MaxAttempts != 3 {
		t.Fatalf("retry event = %+v, want model_call 2/3", retries[0])
	}
	if !strings.Contains(formatStructuredJSONRetryMessage(retries[0]), "provider gateway error: 503 Service Unavailable") {
		t.Fatalf("retry message did not include sanitized 503 detail: %q", formatStructuredJSONRetryMessage(retries[0]))
	}
}

func parseOKFlag(text string) (bool, error) {
	if text == `{"ok":true}` {
		return true, nil
	}
	return false, fmt.Errorf("invalid JSON object")
}

func noStructuredJSONTestSleep(context.Context, time.Duration) error { return nil }
