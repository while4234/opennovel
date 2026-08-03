package bootstrap

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/voocel/agentcore"
	"github.com/voocel/agentcore/llm"
	"github.com/voocel/ainovel-cli/internal/codexauth"
	"github.com/voocel/ainovel-cli/internal/errs"
	"github.com/voocel/ainovel-cli/internal/globalprompt"
	"github.com/voocel/ainovel-cli/internal/grokauth"
	"github.com/voocel/litellm"
	"github.com/voocel/litellm/provider/anthropic"
	"github.com/voocel/litellm/provider/bedrock"
	"github.com/voocel/litellm/provider/compat"
	"github.com/voocel/litellm/provider/deepseek"
	"github.com/voocel/litellm/provider/gemini"
	"github.com/voocel/litellm/provider/glm"
	"github.com/voocel/litellm/provider/grok"
	"github.com/voocel/litellm/provider/mimo"
	"github.com/voocel/litellm/provider/minimax"
	"github.com/voocel/litellm/provider/ollama"
	"github.com/voocel/litellm/provider/openai"
	"github.com/voocel/litellm/provider/openrouter"
	"github.com/voocel/litellm/provider/qwen"
)

func newProviderModelWithRuntimeOptions(cfg Config, providerKey, model string, pc ProviderConfig) (agentcore.ChatModel, bool, error) {
	transport, _, err := ProviderTransport(cfg, providerKey, model, pc)
	if err != nil {
		return nil, true, err
	}
	if transport == nil {
		return nil, false, nil
	}
	client, err := newProviderClientWithTransport(cfg, providerKey, model, pc, transport)
	if err != nil {
		return nil, true, err
	}
	wrapped := globalprompt.WrapModel(llm.NewLiteLLMAdapter(model, client))
	if timeout := ProviderRequestTimeout(pc); timeout > 0 {
		wrapped = &requestTimeoutModel{model: wrapped, timeout: timeout}
	}
	return wrapped, true, nil
}

func newProviderClientWithTransport(cfg Config, providerKey, model string, pc ProviderConfig, transport http.RoundTripper) (*litellm.Client, error) {
	provider, err := newLiteLLMProviderWithTransport(cfg, providerKey, model, pc, transport)
	if err != nil {
		return nil, err
	}
	return litellm.New(provider, litellm.WithStreamIdleTimeout(streamIdleTimeout))
}

// DiscoverProviderModels returns a live provider model list when the provider
// exposes one. supported=false means the provider has no list-models endpoint.
func DiscoverProviderModels(ctx context.Context, cfg Config, providerKey, model string, pc ProviderConfig) ([]string, bool, error) {
	if pc.UsesCodexAuth() {
		if timeout := ProviderConnectivityTimeout(pc); timeout > 0 {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, timeout)
			defer cancel()
		}
		catalog, err := codexauth.ReadModelCatalog(ctx, pc.AuthFile)
		models := make([]string, 0, len(catalog)+len(codexauth.OfficialGPT56Models))
		seen := make(map[string]bool)
		for _, entry := range catalog {
			id := strings.TrimSpace(entry.ID)
			if id != "" && !seen[id] {
				seen[id] = true
				models = append(models, id)
			}
		}
		for _, id := range codexauth.OfficialGPT56Models {
			if !seen[id] {
				seen[id] = true
				models = append(models, id)
			}
		}
		return models, true, err
	}
	transport, _, err := ProviderTransport(cfg, providerKey, model, pc)
	if err != nil {
		return nil, true, err
	}
	client, err := newProviderClientWithTransport(cfg, providerKey, model, pc, transport)
	if err != nil {
		return nil, true, err
	}
	if timeout := ProviderConnectivityTimeout(pc); timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	modelInfos, err := client.ListModels(ctx)
	if err != nil {
		if isModelListingUnsupported(err) {
			return nil, false, nil
		}
		return nil, true, err
	}
	models := make([]string, 0, len(modelInfos))
	seen := make(map[string]bool, len(modelInfos))
	for _, info := range modelInfos {
		id := strings.TrimSpace(info.ID)
		if id == "" {
			id = strings.TrimSpace(info.Name)
		}
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		models = append(models, id)
	}
	sort.Strings(models)
	return models, true, nil
}

func isModelListingUnsupported(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "does not support model listing")
}

func newLiteLLMProviderWithTransport(cfg Config, providerKey, model string, pc ProviderConfig, transport http.RoundTripper) (litellm.Provider, error) {
	providerType, err := pc.ProviderType(providerKey)
	if err != nil {
		return nil, fmt.Errorf("resolve provider type: %w", err)
	}
	if strings.EqualFold(strings.TrimSpace(pc.Auth), ProviderAuthGrokOAuth) {
		if strings.ToLower(strings.TrimSpace(providerType)) != "grok" {
			return nil, fmt.Errorf("provider %s auth %q requires grok type: %w", providerKey, pc.Auth, errs.ErrConfig)
		}
		return newGrokOAuthProviderWithTransport(cfg, providerKey, model, pc, transport)
	}
	if pc.UsesCodexAuth() {
		if strings.ToLower(strings.TrimSpace(providerType)) != "openai" {
			return nil, fmt.Errorf("provider %s auth %q requires openai type: %w", providerKey, pc.Auth, errs.ErrConfig)
		}
		return newCodexAuthProviderWithTransport(providerKey, pc, transport)
	}

	headers, err := headersFromProviderExtra(pc.Extra)
	if err != nil {
		return nil, fmt.Errorf("provider %s extra.headers: %w", providerKey, err)
	}
	userAgent := stringFromProviderExtra(pc.Extra, "user_agent")
	providerType = strings.ToLower(strings.TrimSpace(providerType))
	switch providerType {
	case "openai":
		return openai.New(openai.Config{
			API:        providerAPI(pc),
			APIKeyFunc: staticAPIKeyFunc(pc.APIKey),
			BaseURL:    pc.BaseURL,
			Headers:    headers,
			Transport:  transport,
			UserAgent:  userAgent,
		})
	case "anthropic":
		return anthropic.New(anthropic.Config{
			APIKeyFunc: staticAPIKeyFunc(pc.APIKey),
			BaseURL:    pc.BaseURL,
			Beta:       stringFromProviderExtra(pc.Extra, "anthropic_beta"),
			Headers:    headers,
			Transport:  transport,
			UserAgent:  userAgent,
		})
	case "bedrock":
		return bedrock.New(bedrockConfigWithTransport(pc, transport))
	case "gemini":
		return gemini.New(gemini.Config{
			APIKeyFunc: staticAPIKeyFunc(pc.APIKey),
			BaseURL:    pc.BaseURL,
			Transport:  transport,
		})
	case "deepseek":
		return deepseek.New(compatConfigWithTransport(pc, headers, userAgent, transport))
	case "glm":
		return glm.New(compatConfigWithTransport(pc, headers, userAgent, transport))
	case "grok":
		return newGrokProvider(model, compatConfigWithTransport(pc, headers, userAgent, transport))
	case "minimax":
		return minimax.New(compatConfigWithTransport(pc, headers, userAgent, transport))
	case "mimo":
		return mimo.New(compatConfigWithTransport(pc, headers, userAgent, transport))
	case "ollama":
		return ollama.New(compatConfigWithTransport(pc, headers, userAgent, transport))
	case "openrouter":
		return openrouter.New(compatConfigWithTransport(pc, headers, userAgent, transport))
	case "qwen":
		return qwen.New(compatConfigWithTransport(pc, headers, userAgent, transport))
	default:
		return nil, fmt.Errorf("unknown provider %q", providerType)
	}
}

func createCodexAuthModel(cfg Config, providerKey, model string, pc ProviderConfig) (agentcore.ChatModel, error) {
	transport, _, err := ProviderTransport(cfg, providerKey, model, pc)
	if err != nil {
		return nil, err
	}
	provider, err := newCodexAuthProviderWithTransport(providerKey, pc, transport)
	if err != nil {
		return nil, fmt.Errorf("provider %s (codex auth): %w: %w", providerKey, errs.ErrProvider, err)
	}
	client, err := litellm.New(provider, litellm.WithStreamIdleTimeout(streamIdleTimeout))
	if err != nil {
		return nil, fmt.Errorf("provider %s (codex auth): %w: %w", providerKey, errs.ErrProvider, err)
	}
	wrapped := globalprompt.WrapModel(&codexStreamModel{model: llm.NewLiteLLMAdapter(model, client)})
	if timeout := ProviderRequestTimeout(pc); timeout > 0 {
		return &requestTimeoutModel{model: wrapped, timeout: timeout}, nil
	}
	return wrapped, nil
}

func newCodexAuthProviderWithTransport(providerKey string, pc ProviderConfig, transport http.RoundTripper) (litellm.Provider, error) {
	headers, err := codexHeadersFromProviderExtra(pc.Extra)
	if err != nil {
		return nil, fmt.Errorf("provider %s extra.headers: %w", providerKey, err)
	}
	userAgent := firstProviderExtraString(pc.Extra, "codex_user_agent", "codex_direct_user_agent", "user_agent")
	if userAgent == "" {
		userAgent = codexauth.DefaultUserAgent
	}
	baseURL := strings.TrimSpace(pc.BaseURL)
	if baseURL == "" {
		baseURL = codexauth.DefaultBaseURL
	}
	return openai.New(openai.Config{
		API: openai.APIResponses,
		APIKeyFunc: func(ctx context.Context) (string, error) {
			credentials, err := codexauth.ResolveRuntimeCredentials(ctx, pc.AuthFile)
			if err != nil {
				return "", err
			}
			return credentials.APIKey, nil
		},
		BaseURL:   baseURL,
		Headers:   headers,
		Transport: newCodexAuthTransport(transport, pc.AuthFile),
		UserAgent: userAgent,
	})
}

func codexHeadersFromProviderExtra(extra map[string]any) (map[string]string, error) {
	headers, err := headersFromProviderExtra(extra)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(headers)+2)
	out["Accept"] = "text/event-stream"
	out["originator"] = codexauth.DefaultOriginator
	if configured := firstProviderExtraString(extra, "codex_originator", "codex_direct_originator", "originator"); configured != "" {
		out["originator"] = configured
	}
	for key, value := range headers {
		name := strings.TrimSpace(key)
		if codexHeaderBlocked(name) {
			continue
		}
		out[name] = value
	}
	return out, nil
}

func codexHeaderBlocked(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "authorization", "connection", "content-length", "content-type", "host", "proxy-authorization", "te", "trailer", "transfer-encoding", "upgrade":
		return true
	default:
		return false
	}
}

type codexAuthTransport struct {
	base     http.RoundTripper
	authFile string
}

func newCodexAuthTransport(base http.RoundTripper, authFile string) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	return codexAuthTransport{base: base, authFile: strings.TrimSpace(authFile)}
}

func (t codexAuthTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	credentials, err := codexauth.ResolveRuntimeCredentials(req.Context(), t.authFile)
	if err != nil {
		return nil, err
	}
	if credentials.AccountID != "" {
		req.Header.Set("chatgpt-account-id", credentials.AccountID)
	}
	if req.Header.Get("x-client-request-id") == "" {
		req.Header.Set("x-client-request-id", newCodexRequestID())
	}
	rewriteCodexBackendPath(req)
	if err := normalizeCodexResponsesInput(req); err != nil {
		return nil, err
	}
	return t.base.RoundTrip(req)
}

func rewriteCodexBackendPath(req *http.Request) {
	if req == nil || req.URL == nil {
		return
	}
	if strings.HasSuffix(req.URL.Path, "/v1/responses") {
		req.URL.Path = strings.TrimSuffix(req.URL.Path, "/v1/responses") + "/responses"
		req.URL.RawPath = ""
	}
}

func normalizeCodexResponsesInput(req *http.Request) error {
	if req == nil || req.Body == nil || req.Method != http.MethodPost || !strings.HasSuffix(req.URL.Path, "/responses") {
		return nil
	}
	body, err := io.ReadAll(req.Body)
	if err != nil {
		return fmt.Errorf("read Codex responses request: %w", err)
	}
	_ = req.Body.Close()

	var payload map[string]json.RawMessage
	if err := json.Unmarshal(body, &payload); err != nil {
		return fmt.Errorf("decode Codex responses request: %w", err)
	}
	if input, ok := payload["input"]; ok {
		var text string
		if err := json.Unmarshal(input, &text); err == nil {
			wrapped, err := json.Marshal([]map[string]any{{
				"type": "message",
				"role": "user",
				"content": []map[string]string{{
					"type": "input_text",
					"text": text,
				}},
			}})
			if err != nil {
				return fmt.Errorf("encode Codex responses input: %w", err)
			}
			payload["input"] = wrapped
		}
	}
	payload["store"] = json.RawMessage("false")
	payload["stream"] = json.RawMessage("true")
	delete(payload, "max_output_tokens")
	body, err = json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode Codex responses request: %w", err)
	}
	resetRequestBody(req, body)
	return nil
}

func resetRequestBody(req *http.Request, body []byte) {
	req.Body = io.NopCloser(bytes.NewReader(body))
	req.ContentLength = int64(len(body))
	req.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(body)), nil
	}
}

// codexStreamModel adapts ChatGPT's Codex backend, which accepts Responses
// requests only in streaming mode, to the full ChatModel contract.
type codexStreamModel struct {
	model agentcore.ChatModel
}

func (m *codexStreamModel) Generate(ctx context.Context, messages []agentcore.Message, tools []agentcore.ToolSpec, opts ...agentcore.CallOption) (*agentcore.LLMResponse, error) {
	if m == nil || m.model == nil {
		return nil, agentcore.ErrNoModel
	}
	stream, err := m.model.GenerateStream(ctx, messages, tools, opts...)
	if err != nil {
		return nil, err
	}
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case event, ok := <-stream:
			if !ok {
				return nil, fmt.Errorf("Codex response stream ended before completion")
			}
			switch event.Type {
			case agentcore.StreamEventError:
				if event.Err != nil {
					return nil, event.Err
				}
				return nil, fmt.Errorf("Codex response stream failed")
			case agentcore.StreamEventDone:
				if event.Message.StopReason == "" {
					event.Message.StopReason = event.StopReason
				}
				return &agentcore.LLMResponse{Message: event.Message}, nil
			}
		}
	}
}

func (m *codexStreamModel) GenerateStream(ctx context.Context, messages []agentcore.Message, tools []agentcore.ToolSpec, opts ...agentcore.CallOption) (<-chan agentcore.StreamEvent, error) {
	if m == nil || m.model == nil {
		return nil, agentcore.ErrNoModel
	}
	return m.model.GenerateStream(ctx, messages, tools, opts...)
}

func (m *codexStreamModel) SupportsTools() bool {
	return m != nil && m.model != nil && m.model.SupportsTools()
}

func (m *codexStreamModel) ProviderName() string {
	if m != nil {
		if namer, ok := m.model.(interface{ ProviderName() string }); ok {
			return namer.ProviderName()
		}
	}
	return ""
}

func (m *codexStreamModel) ModelName() string {
	if m != nil {
		if namer, ok := m.model.(interface{ ModelName() string }); ok {
			return namer.ModelName()
		}
	}
	return ""
}

func (m *codexStreamModel) Info() llm.ModelInfo {
	if m != nil {
		if info, ok := m.model.(interface{ Info() llm.ModelInfo }); ok {
			return info.Info()
		}
	}
	return llm.ModelInfo{}
}

func (m *codexStreamModel) Capabilities() llm.Capabilities {
	if m != nil {
		if provider, ok := m.model.(llm.CapabilityProvider); ok {
			return provider.Capabilities()
		}
	}
	return llm.Capabilities{}
}

func newCodexRequestID() string {
	var buf [12]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(buf[:])
}

func newGrokOAuthProviderWithTransport(cfg Config, providerKey, model string, pc ProviderConfig, transport http.RoundTripper) (litellm.Provider, error) {
	headers, err := headersFromProviderExtra(pc.Extra)
	if err != nil {
		return nil, fmt.Errorf("provider %s extra.headers: %w", providerKey, err)
	}
	apiKeyFunc := func(ctx context.Context) (string, error) {
		credentials, err := grokauth.ResolveRuntimeCredentials(ctx, pc.AccountID)
		if err != nil {
			return "", err
		}
		return credentials.APIKey, nil
	}
	baseURL := strings.TrimSpace(pc.BaseURL)
	if baseURL == "" {
		baseURL = grokauth.DefaultBaseURL
	}
	return newGrokProvider(model, grok.Config{
		APIKeyFunc:                  apiKeyFunc,
		BaseURL:                     baseURL,
		Headers:                     headers,
		Transport:                   transport,
		UserAgent:                   stringFromProviderExtra(pc.Extra, "user_agent"),
		AllowUnknownProviderOptions: true,
	})
}

func compatConfigWithTransport(pc ProviderConfig, headers map[string]string, userAgent string, transport http.RoundTripper) compat.Config {
	return compat.Config{
		APIKeyFunc:                  staticAPIKeyFunc(pc.APIKey),
		BaseURL:                     pc.BaseURL,
		Headers:                     headers,
		Transport:                   transport,
		UserAgent:                   userAgent,
		AllowUnknownProviderOptions: true,
	}
}

func bedrockConfigWithTransport(pc ProviderConfig, transport http.RoundTripper) bedrock.Config {
	return bedrock.Config{
		Region:              firstProviderExtraString(pc.Extra, "region", "aws_region"),
		BaseURL:             pc.BaseURL,
		ControlPlaneBaseURL: stringFromProviderExtra(pc.Extra, "control_plane_base_url"),
		Credentials: bedrock.StaticCredentials(
			firstProviderExtraString(pc.Extra, "access_key_id", "aws_access_key_id"),
			firstProviderExtraString(pc.Extra, "secret_access_key", "aws_secret_access_key"),
			firstProviderExtraString(pc.Extra, "session_token", "aws_session_token"),
		),
		Transport: transport,
	}
}

func staticAPIKeyFunc(key string) func(context.Context) (string, error) {
	return func(context.Context) (string, error) {
		return key, nil
	}
}

func firstProviderExtraString(extra map[string]any, keys ...string) string {
	for _, key := range keys {
		if value := stringFromProviderExtra(extra, key); value != "" {
			return value
		}
	}
	return ""
}

func providerAPI(pc ProviderConfig) string {
	if value := firstProviderExtraString(pc.Extra, "api", "api_mode"); value != "" {
		return value
	}
	return pc.API
}

type requestTimeoutModel struct {
	model   agentcore.ChatModel
	timeout time.Duration
}

func (m *requestTimeoutModel) Generate(ctx context.Context, messages []agentcore.Message, tools []agentcore.ToolSpec, opts ...agentcore.CallOption) (*agentcore.LLMResponse, error) {
	if m.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, m.timeout)
		defer cancel()
	}
	return m.model.Generate(ctx, messages, tools, opts...)
}

func (m *requestTimeoutModel) GenerateStream(ctx context.Context, messages []agentcore.Message, tools []agentcore.ToolSpec, opts ...agentcore.CallOption) (<-chan agentcore.StreamEvent, error) {
	// Streaming callers such as co-create own their stage deadline. Applying a
	// shorter provider deadline here would silently override that user-visible
	// setting while the response is still making progress.
	if m.timeout > 0 && !hasContextDeadline(ctx) {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, m.timeout)
		ch, err := m.model.GenerateStream(ctx, messages, tools, opts...)
		if err != nil {
			cancel()
			return nil, err
		}
		return cancelOnStreamDone(ch, cancel), nil
	}
	return m.model.GenerateStream(ctx, messages, tools, opts...)
}

func hasContextDeadline(ctx context.Context) bool {
	_, ok := ctx.Deadline()
	return ok
}

func (m *requestTimeoutModel) SupportsTools() bool {
	return m.model != nil && m.model.SupportsTools()
}

func (m *requestTimeoutModel) ProviderName() string {
	if namer, ok := m.model.(interface{ ProviderName() string }); ok {
		return namer.ProviderName()
	}
	return ""
}

func (m *requestTimeoutModel) Info() llm.ModelInfo {
	if info, ok := m.model.(interface{ Info() llm.ModelInfo }); ok {
		return info.Info()
	}
	return llm.ModelInfo{}
}

func (m *requestTimeoutModel) Capabilities() llm.Capabilities {
	if cp, ok := m.model.(llm.CapabilityProvider); ok {
		return cp.Capabilities()
	}
	return llm.Capabilities{}
}

func cancelOnStreamDone(source <-chan agentcore.StreamEvent, cancel context.CancelFunc) <-chan agentcore.StreamEvent {
	out := make(chan agentcore.StreamEvent, 100)
	go func() {
		defer close(out)
		defer cancel()
		for ev := range source {
			out <- ev
		}
	}()
	return out
}
