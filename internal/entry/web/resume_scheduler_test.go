package web

import (
	"context"
	"encoding/json"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/voocel/ainovel-cli/internal/bootstrap"
)

func TestDueOccurrenceKeysCoalescesOnlyUnprocessedTimesToday(t *testing.T) {
	location := mustShanghai(t)
	now := time.Date(2026, 7, 11, 17, 30, 0, 0, location)
	completed := map[string]time.Time{"2026-07-11T15:00": now.Add(-2 * time.Hour)}

	got := dueOccurrenceKeys(now, location, []string{"15:00", "16:00", "20:00"}, completed)
	assertStrings(t, got, []string{"2026-07-11T16:00"})
}

func TestNextOccurrenceCrossesMidnight(t *testing.T) {
	location := mustShanghai(t)
	now := time.Date(2026, 7, 11, 23, 59, 0, 0, location)
	want := time.Date(2026, 7, 12, 15, 0, 0, 0, location)
	if got := nextOccurrence(now, location, []string{"15:00", "16:00"}, nil); !got.Equal(want) {
		t.Fatalf("next occurrence = %s, want %s", got, want)
	}
}

func TestResumeSchedulerCoalescesCatchUpAndLimitsConcurrency(t *testing.T) {
	location := mustShanghai(t)
	now := time.Date(2026, 7, 11, 17, 0, 0, 0, location)
	projects := []ProjectManifest{{ID: "one"}, {ID: "two"}, {ID: "three"}, {ID: "four"}, {ID: "five"}}
	var mu sync.Mutex
	active, maximum, calls := 0, 0, 0
	release := make(chan struct{})
	started := make(chan struct{}, len(projects))
	scheduler := NewResumeScheduler(t.TempDir(), ResumeSchedulerDeps{
		Now: func() time.Time { return now },
		LoadConfig: func() (bootstrap.ResumeScheduleConfig, error) {
			return bootstrap.ResumeScheduleConfig{DailyTimes: []string{"15:00", "16:00"}, Timezone: "Asia/Shanghai"}, nil
		},
		ListProjects: func() ([]ProjectManifest, error) { return projects, nil },
		ResumeProject: func(context.Context, ProjectManifest) (ScheduledResumeResult, error) {
			mu.Lock()
			active++
			calls++
			maximum = max(maximum, active)
			mu.Unlock()
			started <- struct{}{}
			<-release
			mu.Lock()
			active--
			mu.Unlock()
			return ScheduledResumeResult{Outcome: ScheduledResumeStarted, Action: "resume"}, nil
		},
	})
	state := &scheduledResumeState{Version: scheduledResumeStateVersion, CompletedOccurrences: map[string]time.Time{}}
	done := make(chan error, 1)
	go func() { done <- scheduler.evaluate(context.Background(), state) }()
	for range defaultResumeConcurrency {
		<-started
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if maximum != defaultResumeConcurrency {
		t.Fatalf("maximum concurrency = %d, want %d", maximum, defaultResumeConcurrency)
	}
	if calls != len(projects) {
		t.Fatalf("resume calls = %d, want %d", calls, len(projects))
	}
	assertStrings(t, state.LastBatch.Occurrences, []string{"2026-07-11T15:00", "2026-07-11T16:00"})
	if state.LastBatch.Started != len(projects) || state.LastBatch.Skipped != 0 || state.LastBatch.Failed != 0 {
		t.Fatalf("unexpected summary: %+v", state.LastBatch)
	}

	if err := scheduler.evaluate(context.Background(), state); err != nil {
		t.Fatalf("second evaluate: %v", err)
	}
	if calls != len(projects) {
		t.Fatalf("processed occurrences ran twice: calls = %d", calls)
	}
	data, err := os.ReadFile(scheduler.statePath())
	if err != nil {
		t.Fatalf("read state: %v", err)
	}
	var persisted scheduledResumeState
	if err := json.Unmarshal(data, &persisted); err != nil {
		t.Fatalf("decode state: %v", err)
	}
	if persisted.ActiveBatch != nil || len(persisted.CompletedOccurrences) != 2 {
		t.Fatalf("unexpected persisted state: %+v", persisted)
	}
}

func TestResumeSchedulerRestartsOnlyPendingProjects(t *testing.T) {
	location := mustShanghai(t)
	now := time.Date(2026, 7, 11, 17, 0, 0, 0, location)
	scheduler := NewResumeScheduler(t.TempDir(), ResumeSchedulerDeps{
		Now:        func() time.Time { return now },
		LoadConfig: func() (bootstrap.ResumeScheduleConfig, error) { return bootstrap.ResumeScheduleConfig{}, nil },
		ListProjects: func() ([]ProjectManifest, error) {
			return []ProjectManifest{{ID: "done"}, {ID: "pending"}}, nil
		},
	})
	var called []string
	scheduler.deps.ResumeProject = func(_ context.Context, project ProjectManifest) (ScheduledResumeResult, error) {
		called = append(called, project.ID)
		return ScheduledResumeResult{Outcome: ScheduledResumeSkipped, ReasonCode: "wait_user"}, nil
	}
	finished := now.Add(-time.Minute)
	state := &scheduledResumeState{
		Version: scheduledResumeStateVersion, CompletedOccurrences: map[string]time.Time{},
		ActiveBatch: &scheduledResumeBatch{
			StartedAt: now.Add(-2 * time.Minute), Occurrences: []string{"2026-07-11T16:00"},
			Projects: []scheduledResumeProjectState{
				{ProjectID: "done", Status: "done", Result: &ScheduledResumeResult{Outcome: ScheduledResumeStarted}, FinishedAt: &finished},
				{ProjectID: "pending", Status: "pending"},
			},
		},
	}
	if err := scheduler.saveState(state); err != nil {
		t.Fatalf("save active state: %v", err)
	}
	loaded, err := scheduler.loadState()
	if err != nil {
		t.Fatalf("load active state: %v", err)
	}
	if err := scheduler.evaluate(context.Background(), loaded); err != nil {
		t.Fatalf("resume active batch: %v", err)
	}
	assertStrings(t, called, []string{"pending"})
	if loaded.LastBatch.Started != 1 || loaded.LastBatch.Skipped != 1 {
		t.Fatalf("unexpected recovered summary: %+v", loaded.LastBatch)
	}
}

func TestResumeSchedulerRunStopsCleanly(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	scheduler := NewResumeScheduler(t.TempDir(), ResumeSchedulerDeps{
		LoadConfig:   func() (bootstrap.ResumeScheduleConfig, error) { return bootstrap.ResumeScheduleConfig{}, nil },
		ListProjects: func() ([]ProjectManifest, error) { return nil, nil },
		ResumeProject: func(context.Context, ProjectManifest) (ScheduledResumeResult, error) {
			return ScheduledResumeResult{}, nil
		},
	})
	done := make(chan error, 1)
	go func() { done <- scheduler.Run(ctx) }()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run did not stop after cancellation")
	}
}

func mustShanghai(t *testing.T) *time.Location {
	t.Helper()
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatalf("load timezone: %v", err)
	}
	return location
}

func assertStrings(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("slice = %#v, want %#v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("slice = %#v, want %#v", got, want)
		}
	}
}
