package web

import "time"

const backendRecentCallLimit = 8

type apiBackendStatus struct {
	Status                string           `json:"status"`
	Provider              string           `json:"provider"`
	Model                 string           `json:"model"`
	RuntimeState          string           `json:"runtime_state"`
	MissingAssistantUsage int              `json:"missing_assistant_usage"`
	RecentCalls           []apiBackendCall `json:"recent_calls"`
	ManualTest            *apiBackendTest  `json:"manual_test,omitempty"`
	CheckedAt             time.Time        `json:"checked_at"`
}

type apiBackendCall struct {
	Time           time.Time `json:"time"`
	Category       string    `json:"category"`
	Agent          string    `json:"agent"`
	Summary        string    `json:"summary"`
	Kind           string    `json:"kind,omitempty"`
	Level          string    `json:"level,omitempty"`
	Failed         bool      `json:"failed"`
	Running        bool      `json:"running"`
	DurationMillis int64     `json:"duration_ms"`
}

type apiBackendTest struct {
	Status      string    `json:"status"`
	Message     string    `json:"message"`
	NoTokenCall bool      `json:"no_token_call"`
	CheckedAt   time.Time `json:"checked_at"`
}

func (s *ProjectSession) BackendStatus(includeManualTest bool) apiBackendStatus {
	snapshot := s.Snapshot()
	calls := s.recentBackendCalls()
	status := "operational"
	for _, call := range calls {
		if call.Failed || call.Level == "error" {
			status = "degraded"
			break
		}
	}
	if snapshot.MissingAssistantUsage > 0 {
		status = "degraded"
	}
	checkedAt := time.Now().UTC()
	out := apiBackendStatus{
		Status:                status,
		Provider:              snapshot.Provider,
		Model:                 snapshot.ModelName,
		RuntimeState:          snapshot.RuntimeState,
		MissingAssistantUsage: snapshot.MissingAssistantUsage,
		RecentCalls:           calls,
		CheckedAt:             checkedAt,
	}
	if includeManualTest {
		out.ManualTest = &apiBackendTest{
			Status:      status,
			Message:     "configuration and in-process model route resolved without sending a token-consuming request",
			NoTokenCall: true,
			CheckedAt:   checkedAt,
		}
	}
	return out
}

func (s *ProjectSession) recentBackendCalls() []apiBackendCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]apiBackendCall, 0, backendRecentCallLimit)
	for i := len(s.history) - 1; i >= 0 && len(out) < backendRecentCallLimit; i-- {
		ev := s.history[i]
		if ev.Event == nil {
			continue
		}
		if ev.Event.DurationMillis <= 0 && !ev.Event.Failed && !ev.Event.Running {
			continue
		}
		out = append(out, apiBackendCall{
			Time:           ev.Event.Time,
			Category:       ev.Event.Category,
			Agent:          ev.Event.Agent,
			Summary:        ev.Event.Summary,
			Kind:           ev.Event.Kind,
			Level:          ev.Event.Level,
			Failed:         ev.Event.Failed,
			Running:        ev.Event.Running,
			DurationMillis: ev.Event.DurationMillis,
		})
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}
