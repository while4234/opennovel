package imp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/internal/modeldiag"
	"github.com/voocel/ainovel-cli/internal/retrypolicy"
)

const structuredInputLimitBytes = 60 * 1024

type LLMStreamChat interface {
	GenerateStream(ctx context.Context, messages []agentcore.Message, tools []agentcore.ToolSpec, opts ...agentcore.CallOption) (<-chan agentcore.StreamEvent, error)
}

type StructuredRetryEvent struct {
	Attempt     int
	MaxAttempts int
	Err         error
}

type StructuredCallOptions struct {
	MaxAttempts   int
	MaxTokens     int
	DisableStream bool
	OnRetry       func(StructuredRetryEvent)
	Sleep         func(context.Context, time.Duration) error
}

func runStructuredCall[T any](
	ctx context.Context,
	llm LLMChat,
	messages []agentcore.Message,
	parse func(string) (T, error),
	opts StructuredCallOptions,
) (T, error) {
	var zero T
	if llm == nil {
		return zero, fmt.Errorf("llm is nil")
	}
	if parse == nil {
		return zero, fmt.Errorf("parse is nil")
	}

	maxAttempts := opts.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = retrypolicy.MaxAttempts
	}
	currentMessages := append([]agentcore.Message(nil), messages...)

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return zero, err
		}

		text, recorder, usage, err := generateStructuredText(ctx, llm, currentMessages, opts, attempt)
		if err != nil {
			if !agentcore.IsFailoverEligible(err) || attempt == maxAttempts {
				return zero, err
			}
			if err := waitBeforeStructuredRetry(ctx, opts, attempt, maxAttempts, err); err != nil {
				return zero, err
			}
			continue
		}

		parsed, err := parse(text)
		if err == nil {
			if diagnosticErr := recorder.Finish(modeldiag.StatusCompleted, text, usage); diagnosticErr != nil {
				return zero, diagnosticErr
			}
			return parsed, nil
		}
		_ = recorder.Finish(structuredDiagnosticStatus(err), text, usage)
		if attempt == maxAttempts {
			return zero, err
		}

		currentMessages = append(currentMessages, agentcore.UserMsg(formatRetryPrompt(err)))
		if err := waitBeforeStructuredRetry(ctx, opts, attempt, maxAttempts, err); err != nil {
			return zero, err
		}
	}
	return zero, fmt.Errorf("structured call exhausted %d attempts", maxAttempts)
}

func generateStructuredText(ctx context.Context, llm LLMChat, messages []agentcore.Message, opts StructuredCallOptions, attempt int) (string, *modeldiag.Recorder, *agentcore.Usage, error) {
	callOpts := callOptions(opts)
	if streamer, ok := llm.(LLMStreamChat); ok && !opts.DisableStream {
		recorder, beginErr := beginIMPDiagnostic(ctx, "import_structured_stream", 0, messages, opts.MaxTokens, attempt)
		if beginErr != nil {
			return "", nil, nil, beginErr
		}
		ch, err := streamer.GenerateStream(ctx, messages, nil, callOpts...)
		if err == nil {
			text, usage, collectErr := collectStreamText(ch)
			if collectErr != nil {
				_ = recorder.Finish(modeldiag.StatusProviderError, text, usage)
				return "", nil, nil, collectErr
			}
			if strings.TrimSpace(text) == "" {
				_ = recorder.Finish(modeldiag.StatusEmptyResponse, "", usage)
				return "", nil, nil, fmt.Errorf("llm returned empty response")
			}
			return text, recorder, usage, nil
		}
		_ = recorder.Finish(modeldiag.StatusProviderError, "", nil)
		if agentcore.IsFailoverEligible(err) {
			return "", nil, nil, err
		}
	}

	recorder, beginErr := beginIMPDiagnostic(ctx, "import_structured_generate", 0, messages, opts.MaxTokens, attempt)
	if beginErr != nil {
		return "", nil, nil, beginErr
	}
	resp, err := llm.Generate(ctx, messages, nil, callOpts...)
	if err != nil {
		_ = recorder.Finish(modeldiag.StatusProviderError, "", nil)
		return "", nil, nil, err
	}
	if resp == nil {
		_ = recorder.Finish(modeldiag.StatusEmptyResponse, "", nil)
		return "", nil, nil, fmt.Errorf("llm returned nil response")
	}
	text := resp.Message.TextContent()
	if strings.TrimSpace(text) == "" {
		_ = recorder.Finish(modeldiag.StatusEmptyResponse, "", resp.Message.Usage)
		return "", nil, nil, fmt.Errorf("llm returned empty response")
	}
	return text, recorder, resp.Message.Usage, nil
}

func collectStreamText(ch <-chan agentcore.StreamEvent) (string, *agentcore.Usage, error) {
	var sb strings.Builder
	for ev := range ch {
		switch ev.Type {
		case agentcore.StreamEventTextDelta:
			sb.WriteString(ev.Delta)
		case agentcore.StreamEventDone:
			if sb.Len() > 0 {
				return sb.String(), ev.Message.Usage, nil
			}
			return ev.Message.TextContent(), ev.Message.Usage, nil
		case agentcore.StreamEventError:
			if ev.Err != nil {
				return sb.String(), nil, ev.Err
			}
			return sb.String(), nil, fmt.Errorf("llm stream returned error event")
		}
	}
	return sb.String(), nil, fmt.Errorf("llm stream closed before done")
}

func beginIMPDiagnostic(ctx context.Context, task string, chapter int, messages []agentcore.Message, maxTokens, attempt int) (*modeldiag.Recorder, error) {
	var system string
	if len(messages) > 0 {
		system = messages[0].TextContent()
	}
	user, _ := json.Marshal(messages[1:])
	return modeldiag.Begin(modeldiag.Request{Store: modeldiag.StoreFromContext(ctx), Task: task, ChapterID: impChapterDiagnosticID(chapter), Batch: attempt, System: system, User: user, InputLimitBytes: structuredInputLimitBytes, OutputLimitTokens: maxTokens})
}

func structuredInputBytes(messages []agentcore.Message) int {
	var system string
	if len(messages) > 0 {
		system = messages[0].TextContent()
	}
	user, _ := json.Marshal(messages[1:])
	return len(system) + len(user)
}

func impChapterDiagnosticID(chapter int) string {
	if chapter <= 0 {
		return ""
	}
	return fmt.Sprintf("chapter-%d", chapter)
}

func structuredDiagnosticStatus(err error) string {
	if err == nil {
		return modeldiag.StatusCompleted
	}
	message := strings.ToLower(err.Error())
	if strings.Contains(message, "json") || strings.Contains(message, "envelope") || strings.Contains(message, "tag") {
		return modeldiag.StatusDecodeError
	}
	return modeldiag.StatusInvalidSchema
}

func callOptions(opts StructuredCallOptions) []agentcore.CallOption {
	if opts.MaxTokens <= 0 {
		return nil
	}
	return []agentcore.CallOption{agentcore.WithMaxTokens(opts.MaxTokens)}
}

func waitBeforeStructuredRetry(ctx context.Context, opts StructuredCallOptions, attempt, maxAttempts int, err error) error {
	if opts.OnRetry != nil {
		opts.OnRetry(StructuredRetryEvent{Attempt: attempt + 1, MaxAttempts: maxAttempts, Err: err})
	}
	delay := structuredRetryDelay(attempt)
	if opts.Sleep != nil {
		return opts.Sleep(ctx, delay)
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func structuredRetryDelay(attempt int) time.Duration {
	return retrypolicy.Delay(attempt)
}

func formatRetryPrompt(err error) string {
	return "The previous response could not be parsed as the required structured output.\n" +
		"Parse error: " + cleanLLMText(err.Error()) + "\n\n" +
		"Return the complete answer again using only the required === TAG === sections. " +
		"All JSON sections must be valid JSON. Do not add explanations, apologies, markdown fences, or extra text."
}
