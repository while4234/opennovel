package host

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/voocel/agentcore"
)

func TestProbeAddedModelConnectivityUsesCompleteRuntimeStream(t *testing.T) {
	model := &modelProbeTestModel{
		events: []agentcore.StreamEvent{
			{Type: agentcore.StreamEventTextDelta, Delta: "OK"},
			{Type: agentcore.StreamEventDone},
		},
	}

	if err := probeAddedModelConnectivity(context.Background(), model, time.Second); err != nil {
		t.Fatalf("probeAddedModelConnectivity: %v", err)
	}
	if model.generateCalled {
		t.Fatal("probe used non-streaming Generate")
	}
	if !model.streamCalled {
		t.Fatal("probe did not use GenerateStream")
	}
}

func TestProbeAddedModelConnectivityRejectsStreamError(t *testing.T) {
	model := &modelProbeTestModel{
		events: []agentcore.StreamEvent{
			{Type: agentcore.StreamEventTextDelta, Delta: "O"},
			{Type: agentcore.StreamEventError, Err: io.ErrUnexpectedEOF},
		},
	}

	err := probeAddedModelConnectivity(context.Background(), model, time.Second)
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("probe error = %v, want unexpected EOF", err)
	}
}

func TestProbeAddedModelConnectivityRejectsStreamWithoutDone(t *testing.T) {
	model := &modelProbeTestModel{
		events: []agentcore.StreamEvent{
			{Type: agentcore.StreamEventTextDelta, Delta: "OK"},
		},
	}

	err := probeAddedModelConnectivity(context.Background(), model, time.Second)
	if !errors.Is(err, agentcore.ErrStreamPartial) {
		t.Fatalf("probe error = %v, want partial stream", err)
	}
}

func TestProbeAddedModelConnectivityRejectsNilStreamError(t *testing.T) {
	model := &modelProbeTestModel{
		events: []agentcore.StreamEvent{
			{Type: agentcore.StreamEventError},
		},
	}

	err := probeAddedModelConnectivity(context.Background(), model, time.Second)
	if err == nil || !strings.Contains(err.Error(), "response stream failed") {
		t.Fatalf("probe error = %v, want response stream failure", err)
	}
}

type modelProbeTestModel struct {
	events         []agentcore.StreamEvent
	generateCalled bool
	streamCalled   bool
}

func (m *modelProbeTestModel) Generate(context.Context, []agentcore.Message, []agentcore.ToolSpec, ...agentcore.CallOption) (*agentcore.LLMResponse, error) {
	m.generateCalled = true
	return nil, errors.New("non-streaming Generate must not be used")
}

func (m *modelProbeTestModel) GenerateStream(context.Context, []agentcore.Message, []agentcore.ToolSpec, ...agentcore.CallOption) (<-chan agentcore.StreamEvent, error) {
	m.streamCalled = true
	stream := make(chan agentcore.StreamEvent, len(m.events))
	for _, event := range m.events {
		stream <- event
	}
	close(stream)
	return stream, nil
}

func (*modelProbeTestModel) SupportsTools() bool { return false }
