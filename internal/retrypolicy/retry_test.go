package retrypolicy

import (
	"testing"
	"time"
)

func TestRetryPolicyUsesSevenAttemptsAndBackoff(t *testing.T) {
	if MaxAttempts != 7 {
		t.Fatalf("MaxAttempts=%d, want 7", MaxAttempts)
	}

	wants := []time.Duration{
		2 * time.Second,
		4 * time.Second,
		8 * time.Second,
		16 * time.Second,
		30 * time.Second,
		30 * time.Second,
	}
	for i, want := range wants {
		if got := Delay(i + 1); got != want {
			t.Fatalf("Delay(%d)=%s, want %s", i+1, got, want)
		}
	}
}
