package bootstrap

import (
	"testing"

	"github.com/voocel/litellm"
)

func TestMapGrok45ThinkingHigh(t *testing.T) {
	body, err := mapGrok45Thinking(&litellm.Thinking{
		Mode:   litellm.ThinkingEnabled,
		Effort: "high",
	}, "grok-4.5")
	if err != nil {
		t.Fatalf("mapGrok45Thinking: %v", err)
	}
	if got := body["reasoning_effort"]; got != "high" {
		t.Fatalf("reasoning_effort = %#v, want high", got)
	}
}

func TestMapGrok45ThinkingXHigh(t *testing.T) {
	body, err := mapGrok45Thinking(&litellm.Thinking{
		Mode:   litellm.ThinkingEnabled,
		Effort: "xhigh",
	}, "grok-4.5")
	if err != nil {
		t.Fatalf("mapGrok45Thinking: %v", err)
	}
	if got := body["reasoning_effort"]; got != "xhigh" {
		t.Fatalf("reasoning_effort = %#v, want xhigh", got)
	}
}

func TestMapGrok45ThinkingCannotDisableReasoning(t *testing.T) {
	_, err := mapGrok45Thinking(&litellm.Thinking{Mode: litellm.ThinkingDisabled}, "grok-4.5")
	if err == nil {
		t.Fatal("expected disabling Grok 4.5 reasoning to fail")
	}
}

func TestGrok45DetectionDoesNotChangeOtherGrokModels(t *testing.T) {
	if !isGrok45("grok-4.5") || !isGrok45("GROK-4.5-latest") {
		t.Fatal("Grok 4.5 variants should use the compatibility provider")
	}
	if isGrok45("grok-4.3-latest") {
		t.Fatal("Grok 4.3 should keep using the upstream provider")
	}
}
