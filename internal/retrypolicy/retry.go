package retrypolicy

import (
	"context"
	"time"
)

const (
	MaxAttempts = 7
	baseDelay   = 2 * time.Second
	maxDelay    = 30 * time.Second
)

func Delay(attempt int) time.Duration {
	if attempt <= 0 {
		return baseDelay
	}
	delay := baseDelay << (attempt - 1)
	if delay > maxDelay {
		return maxDelay
	}
	return delay
}

func Wait(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
