package host

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/voocel/ainovel-cli/internal/bootstrap"
	"github.com/voocel/ainovel-cli/internal/store"
)

func TestReportModelFailoverEmitsStructuredSafeEvent(t *testing.T) {
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	h := &Host{store: st, events: make(chan Event, 2)}
	h.reportModelFailover(bootstrap.FailoverEvent{
		Role:         bootstrap.StageWriting,
		Reason:       "rate_limit",
		FromProvider: "deepseek-yuanyu-0",
		FromModel:    "deepseek-v4-pro",
		ToProvider:   "deepseek-suifeng-1",
		ToModel:      "deepseek-v4-pro",
	})

	event := <-h.events
	if event.Category != "SYSTEM" || event.Kind != "model_auto_switch" || event.Level != "warn" {
		t.Fatalf("event metadata = %+v", event)
	}
	if !strings.Contains(event.Summary, "模型自动切换（正文写作）") || !strings.Contains(event.Summary, "deepseek-yuanyu-0/deepseek-v4-pro") || !strings.Contains(event.Summary, "deepseek-suifeng-1/deepseek-v4-pro") {
		t.Fatalf("event summary = %q", event.Summary)
	}
	if event.Detail != "原因：服务限流" {
		t.Fatalf("event detail = %q", event.Detail)
	}
}

func TestReportModelFailoverUsesTheActualFailureReason(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{name: "quota", err: fmt.Errorf("insufficient_user_quota: precharge failed"), want: "原因：额度不足"},
		{name: "timeout", err: context.DeadlineExceeded, want: "原因：响应超时"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			h := &Host{events: make(chan Event, 1)}
			h.reportModelFailover(bootstrap.FailoverEvent{
				Role:         bootstrap.StageWriting,
				Reason:       bootstrap.RuntimeFallbackPoolReasonPrefix + ":from->to",
				FromProvider: "from",
				FromModel:    "model",
				ToProvider:   "to",
				ToModel:      "model",
				Err:          test.err,
			})
			if event := <-h.events; event.Detail != test.want {
				t.Fatalf("event detail = %q, want %q", event.Detail, test.want)
			}
		})
	}
}

func TestReportModelFailoverIgnoresUnchangedRoute(t *testing.T) {
	h := &Host{events: make(chan Event, 1)}
	h.reportModelFailover(bootstrap.FailoverEvent{
		Role:         bootstrap.StageWriting,
		FromProvider: "deepseek",
		FromModel:    "deepseek-v4-pro",
		ToProvider:   "deepseek",
		ToModel:      "deepseek-v4-pro",
	})
	select {
	case event := <-h.events:
		t.Fatalf("unchanged route emitted event: %+v", event)
	default:
	}
}
