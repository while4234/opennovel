package host

import "testing"

func TestManualPauseCancelsAutomaticResumeInFlight(t *testing.T) {
	h := &Host{
		lifecycle:          lifecycleIdle,
		autoResumeInFlight: true,
		pauseEpoch:         7,
	}

	_, active := h.pauseActiveRun()
	if !active {
		t.Fatal("automatic resume in flight should be treated as active")
	}
	if h.lifecycle != lifecyclePaused {
		t.Fatalf("lifecycle = %q, want %q", h.lifecycle, lifecyclePaused)
	}
	if h.pauseEpoch != 8 {
		t.Fatalf("pause epoch = %d, want 8", h.pauseEpoch)
	}
}

func TestAutomaticResumeCannotOverwriteManualPause(t *testing.T) {
	h := &Host{
		lifecycle:          lifecyclePaused,
		autoResumeInFlight: true,
		pauseEpoch:         4,
	}

	if h.publishResumedLifecycle(3) {
		t.Fatal("automatic resume should be canceled after manual pause")
	}
	if h.lifecycle != lifecyclePaused {
		t.Fatalf("lifecycle = %q, want %q", h.lifecycle, lifecyclePaused)
	}
}

func TestManualPauseCancelsManualResumeThatAlreadyStarted(t *testing.T) {
	h := &Host{
		lifecycle:      lifecycleIdle,
		resumeInFlight: 1,
		pauseEpoch:     11,
	}
	startedAtEpoch := h.pauseEpoch

	if _, active := h.pauseActiveRun(); !active {
		t.Fatal("manual resume in flight should be treated as active")
	}
	if h.publishResumedLifecycle(startedAtEpoch) {
		t.Fatal("resume that predates the pause should not publish running")
	}
	if h.lifecycle != lifecyclePaused {
		t.Fatalf("lifecycle = %q, want %q", h.lifecycle, lifecyclePaused)
	}
}

func TestManualResumeAfterPauseCanPublishRunning(t *testing.T) {
	h := &Host{
		lifecycle:  lifecyclePaused,
		pauseEpoch: 15,
	}

	if !h.publishResumedLifecycle(h.pauseEpoch) {
		t.Fatal("manual resume started after the latest pause should be allowed")
	}
	if h.lifecycle != lifecycleRunning {
		t.Fatalf("lifecycle = %q, want %q", h.lifecycle, lifecycleRunning)
	}
}
