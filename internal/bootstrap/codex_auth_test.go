package bootstrap

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/voocel/agentcore"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestCodexAuthTransportAddsAccountHeaderAndRewritesResponsesPath(t *testing.T) {
	authPath := filepath.Join(t.TempDir(), "auth.json")
	data, err := json.Marshal(map[string]any{
		"tokens": map[string]any{
			"access_token": "codex-access-token",
			"account_id":   "acct-test",
		},
	})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if err := os.WriteFile(authPath, data, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	t.Setenv("CODEX_AUTH_FILE", authPath)

	var gotPath string
	var gotAccount string
	var gotBody map[string]any
	transport := newCodexAuthTransport(roundTripFunc(func(req *http.Request) (*http.Response, error) {
		gotPath = req.URL.Path
		gotAccount = req.Header.Get("chatgpt-account-id")
		if err := json.NewDecoder(req.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader("")),
		}, nil
	}), "")

	req, err := http.NewRequest(http.MethodPost, "https://chatgpt.com/backend-api/codex/v1/responses", strings.NewReader(`{"model":"gpt-5.5","input":"ping","max_output_tokens":8}`))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	if _, err := transport.RoundTrip(req); err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	if gotPath != "/backend-api/codex/responses" {
		t.Fatalf("path = %q", gotPath)
	}
	if gotAccount != "acct-test" {
		t.Fatalf("account header = %q", gotAccount)
	}
	if req.Header.Get("x-client-request-id") == "" {
		t.Fatal("missing request id header")
	}
	input, ok := gotBody["input"].([]any)
	if !ok || len(input) != 1 {
		t.Fatalf("Codex input = %#v, want one-item list", gotBody["input"])
	}
	message, _ := input[0].(map[string]any)
	if message["type"] != "message" || message["role"] != "user" {
		t.Fatalf("Codex input message = %#v", message)
	}
	if store, ok := gotBody["store"].(bool); !ok || store {
		t.Fatalf("Codex store = %#v, want false", gotBody["store"])
	}
	if stream, ok := gotBody["stream"].(bool); !ok || !stream {
		t.Fatalf("Codex stream = %#v, want true", gotBody["stream"])
	}
	if _, ok := gotBody["max_output_tokens"]; ok {
		t.Fatalf("Codex max_output_tokens should be omitted: %#v", gotBody)
	}
}

type codexStreamTestModel struct {
	generateCalled bool
	streamCalled   bool
}

func (m *codexStreamTestModel) Generate(context.Context, []agentcore.Message, []agentcore.ToolSpec, ...agentcore.CallOption) (*agentcore.LLMResponse, error) {
	m.generateCalled = true
	return nil, fmt.Errorf("non-stream generation must not be used")
}

func (m *codexStreamTestModel) GenerateStream(context.Context, []agentcore.Message, []agentcore.ToolSpec, ...agentcore.CallOption) (<-chan agentcore.StreamEvent, error) {
	m.streamCalled = true
	events := make(chan agentcore.StreamEvent, 1)
	message := agentcore.Message{Role: agentcore.RoleAssistant, Content: []agentcore.ContentBlock{agentcore.TextBlock("OK")}}
	events <- agentcore.StreamEvent{Type: agentcore.StreamEventDone, Message: message, StopReason: agentcore.StopReasonStop}
	close(events)
	return events, nil
}

func (*codexStreamTestModel) SupportsTools() bool { return true }

func TestCodexStreamModelUsesStreamingForGenerate(t *testing.T) {
	inner := &codexStreamTestModel{}
	model := &codexStreamModel{model: inner}
	response, err := model.Generate(context.Background(), []agentcore.Message{agentcore.UserMsg("ping")}, nil)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if inner.generateCalled || !inner.streamCalled {
		t.Fatalf("generateCalled=%v streamCalled=%v", inner.generateCalled, inner.streamCalled)
	}
	if response == nil || response.Message.TextContent() != "OK" {
		t.Fatalf("response = %#v", response)
	}
}

func TestCodexAuthProviderConfigValidatesWithoutAPIKey(t *testing.T) {
	cfg := Config{
		Provider:  "codex-login",
		ModelName: "gpt-5.5",
		Providers: map[string]ProviderConfig{
			"codex-login": {
				Type:    "openai",
				Auth:    ProviderAuthCodex,
				API:     "responses",
				BaseURL: "https://chatgpt.com/backend-api/codex",
			},
		},
	}
	cfg.FillDefaults()
	if err := cfg.ValidateBase(); err != nil {
		t.Fatalf("ValidateBase: %v", err)
	}
}
