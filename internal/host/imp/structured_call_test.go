package imp

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/voocel/agentcore"
)

type scriptedStructuredLLM struct {
	responses     []structuredLLMResponse
	streams       [][]agentcore.StreamEvent
	calls         int
	streamsCalled int
	got           [][]agentcore.Message
}

type structuredLLMResponse struct {
	text string
	err  error
}

func (m *scriptedStructuredLLM) Generate(_ context.Context, msgs []agentcore.Message, _ []agentcore.ToolSpec, _ ...agentcore.CallOption) (*agentcore.LLMResponse, error) {
	m.got = append(m.got, msgs)
	if m.calls >= len(m.responses) {
		return nil, io.ErrUnexpectedEOF
	}
	resp := m.responses[m.calls]
	m.calls++
	if resp.err != nil {
		return nil, resp.err
	}
	return &agentcore.LLMResponse{Message: agentcore.Message{
		Role:      agentcore.RoleAssistant,
		Content:   []agentcore.ContentBlock{agentcore.TextBlock(resp.text)},
		Timestamp: time.Now(),
	}}, nil
}

func (m *scriptedStructuredLLM) GenerateStream(_ context.Context, msgs []agentcore.Message, _ []agentcore.ToolSpec, _ ...agentcore.CallOption) (<-chan agentcore.StreamEvent, error) {
	m.got = append(m.got, msgs)
	ch := make(chan agentcore.StreamEvent, 8)
	if m.streamsCalled >= len(m.streams) {
		close(ch)
		return ch, nil
	}
	events := m.streams[m.streamsCalled]
	m.streamsCalled++
	go func() {
		defer close(ch)
		for _, ev := range events {
			ch <- ev
		}
	}()
	return ch, nil
}

func TestAnalyzeChapterWithOptionsParsesStreamAfterDone(t *testing.T) {
	first, second := splitForStream(validAnalyzerEnvelope)
	llm := &scriptedStructuredLLM{
		streams: [][]agentcore.StreamEvent{{
			{Type: agentcore.StreamEventTextDelta, Delta: first},
			{Type: agentcore.StreamEventTextDelta, Delta: second},
			{Type: agentcore.StreamEventDone},
		}},
	}

	got, err := AnalyzeChapterWithOptions(context.Background(), llm, "system", 1, "Opening", "body", "", "", nil,
		StructuredCallOptions{Sleep: noStructuredTestSleep})
	if err != nil {
		t.Fatalf("AnalyzeChapterWithOptions: %v", err)
	}
	if got.Summary == "" || llm.streamsCalled != 1 || llm.calls != 0 {
		t.Fatalf("stream not used or parsed: got=%+v streams=%d calls=%d", got, llm.streamsCalled, llm.calls)
	}
}

func TestAnalyzeChapterWithOptionsRetriesUnexpectedEOF(t *testing.T) {
	llm := &scriptedStructuredLLM{
		responses: []structuredLLMResponse{
			{err: io.ErrUnexpectedEOF},
			{text: validAnalyzerEnvelope},
		},
	}

	_, err := AnalyzeChapterWithOptions(context.Background(), llm, "system", 1, "Opening", "body", "", "", nil,
		StructuredCallOptions{DisableStream: true, Sleep: noStructuredTestSleep})
	if err != nil {
		t.Fatalf("AnalyzeChapterWithOptions: %v", err)
	}
	if llm.calls != 2 {
		t.Fatalf("calls=%d, want 2", llm.calls)
	}
}

func TestAnalyzeChapterWithOptionsDefaultsToSevenRetryAttempts(t *testing.T) {
	llm := &scriptedStructuredLLM{
		responses: []structuredLLMResponse{
			{err: io.ErrUnexpectedEOF},
			{err: io.ErrUnexpectedEOF},
			{err: io.ErrUnexpectedEOF},
			{err: io.ErrUnexpectedEOF},
			{err: io.ErrUnexpectedEOF},
			{err: io.ErrUnexpectedEOF},
			{text: validAnalyzerEnvelope},
		},
	}

	_, err := AnalyzeChapterWithOptions(context.Background(), llm, "system", 1, "Opening", "body", "", "", nil,
		StructuredCallOptions{DisableStream: true, Sleep: noStructuredTestSleep})
	if err != nil {
		t.Fatalf("AnalyzeChapterWithOptions: %v", err)
	}
	if llm.calls != 7 {
		t.Fatalf("calls=%d, want 7", llm.calls)
	}
}

func TestAnalyzeChapterWithOptionsDoesNotReturnPartialStreamOnError(t *testing.T) {
	llm := &scriptedStructuredLLM{
		streams: [][]agentcore.StreamEvent{{
			{Type: agentcore.StreamEventTextDelta, Delta: "=== SUMMARY ===\npartial"},
			{Type: agentcore.StreamEventError, Err: context.Canceled},
		}},
	}

	_, err := AnalyzeChapterWithOptions(context.Background(), llm, "system", 1, "Opening", "body", "", "", nil,
		StructuredCallOptions{Sleep: noStructuredTestSleep})
	if err == nil {
		t.Fatal("want stream error")
	}
}

func TestAnalyzeChapterWithOptionsRetriesFormat(t *testing.T) {
	llm := &scriptedStructuredLLM{
		responses: []structuredLLMResponse{
			{text: "not tagged"},
			{text: validAnalyzerEnvelope},
		},
	}

	_, err := AnalyzeChapterWithOptions(context.Background(), llm, "system", 1, "Opening", "body", "", "", nil,
		StructuredCallOptions{DisableStream: true, Sleep: noStructuredTestSleep})
	if err != nil {
		t.Fatalf("AnalyzeChapterWithOptions: %v", err)
	}
	if llm.calls != 2 {
		t.Fatalf("calls=%d, want 2", llm.calls)
	}
	if len(llm.got) != 2 || !strings.Contains(llm.got[1][2].TextContent(), "could not be parsed") {
		t.Fatalf("format retry message not appended: %+v", llm.got)
	}
}

func TestAnalyzeChapterWithOptionsRetriesRepeatedFormatFailures(t *testing.T) {
	llm := &scriptedStructuredLLM{
		responses: []structuredLLMResponse{
			{text: "not tagged"},
			{text: "=== SUMMARY ===\nstill missing required tags"},
			{text: validAnalyzerEnvelope},
		},
	}

	_, err := AnalyzeChapterWithOptions(context.Background(), llm, "system", 1, "Opening", "body", "", "", nil,
		StructuredCallOptions{DisableStream: true, Sleep: noStructuredTestSleep})
	if err != nil {
		t.Fatalf("AnalyzeChapterWithOptions: %v", err)
	}
	if llm.calls != 3 {
		t.Fatalf("calls=%d, want 3", llm.calls)
	}
	if len(llm.got) != 3 {
		t.Fatalf("recorded calls=%d, want 3", len(llm.got))
	}
	for i := 1; i < len(llm.got); i++ {
		lastMessage := llm.got[i][len(llm.got[i])-1]
		if !strings.Contains(lastMessage.TextContent(), "could not be parsed") {
			t.Fatalf("call %d missing format retry prompt: %+v", i+1, llm.got[i])
		}
	}
}

func splitForStream(text string) (string, string) {
	mid := len(text) / 2
	return text[:mid], text[mid:]
}

func noStructuredTestSleep(context.Context, time.Duration) error { return nil }
