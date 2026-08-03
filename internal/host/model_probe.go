package host

import (
	"context"
	"fmt"
	"time"

	"github.com/voocel/agentcore"
)

const addedModelProbeTimeout = 15 * time.Second

type modelConnectivityProbe func(context.Context, agentcore.ChatModel, time.Duration) error

var addedModelConnectivityProbe modelConnectivityProbe = probeAddedModelConnectivity

func SetAddedModelConnectivityProbeForTest(probe func(context.Context, agentcore.ChatModel) error) func() {
	previous := addedModelConnectivityProbe
	addedModelConnectivityProbe = func(ctx context.Context, model agentcore.ChatModel, _ time.Duration) error {
		return probe(ctx, model)
	}
	return func() {
		addedModelConnectivityProbe = previous
	}
}

func probeAddedModelConnectivity(ctx context.Context, model agentcore.ChatModel, timeout time.Duration) error {
	if model == nil {
		return fmt.Errorf("model connection test failed: model is nil")
	}
	if timeout <= 0 {
		timeout = addedModelProbeTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	stream, err := model.GenerateStream(ctx, []agentcore.Message{
		agentcore.UserMsg("Reply with OK only. This is a connection test."),
	}, nil, agentcore.WithMaxTokens(8))
	if err != nil {
		return fmt.Errorf("model connection test failed: %w", err)
	}
	for event := range stream {
		switch event.Type {
		case agentcore.StreamEventDone:
			return nil
		case agentcore.StreamEventError:
			if event.Err == nil {
				return fmt.Errorf("model connection test failed: response stream failed")
			}
			return fmt.Errorf("model connection test failed: %w", event.Err)
		}
	}
	return fmt.Errorf("model connection test failed: %w", agentcore.ErrStreamPartial)
}
