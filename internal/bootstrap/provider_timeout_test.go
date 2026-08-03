package bootstrap

import (
	"context"
	"testing"
	"time"

	"github.com/voocel/agentcore"
)

func TestRequestTimeoutModelDoesNotShortenCallerStreamDeadline(t *testing.T) {
	model := &delayedStreamModel{delay: 60 * time.Millisecond}
	wrapped := &requestTimeoutModel{model: model, timeout: 20 * time.Millisecond}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	stream, err := wrapped.GenerateStream(ctx, nil, nil)
	if err != nil {
		t.Fatalf("GenerateStream: %v", err)
	}
	for event := range stream {
		if event.Type == agentcore.StreamEventError {
			t.Fatalf("stream error = %v", event.Err)
		}
		if event.Type == agentcore.StreamEventDone {
			return
		}
	}
	t.Fatal("stream ended without done event")
}

func TestRequestTimeoutModelAppliesStreamTimeoutWithoutCallerDeadline(t *testing.T) {
	model := &delayedStreamModel{delay: time.Second}
	wrapped := &requestTimeoutModel{model: model, timeout: 20 * time.Millisecond}

	stream, err := wrapped.GenerateStream(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("GenerateStream: %v", err)
	}
	for event := range stream {
		if event.Type == agentcore.StreamEventError && event.Err == context.DeadlineExceeded {
			return
		}
	}
	t.Fatal("stream ended without provider timeout")
}

type delayedStreamModel struct {
	delay time.Duration
}

func (m *delayedStreamModel) Generate(context.Context, []agentcore.Message, []agentcore.ToolSpec, ...agentcore.CallOption) (*agentcore.LLMResponse, error) {
	return nil, nil
}

func (m *delayedStreamModel) GenerateStream(ctx context.Context, _ []agentcore.Message, _ []agentcore.ToolSpec, _ ...agentcore.CallOption) (<-chan agentcore.StreamEvent, error) {
	ch := make(chan agentcore.StreamEvent, 1)
	go func() {
		defer close(ch)
		select {
		case <-time.After(m.delay):
			ch <- agentcore.StreamEvent{Type: agentcore.StreamEventDone}
		case <-ctx.Done():
			ch <- agentcore.StreamEvent{Type: agentcore.StreamEventError, Err: ctx.Err()}
		}
	}()
	return ch, nil
}

func (m *delayedStreamModel) SupportsTools() bool { return false }
