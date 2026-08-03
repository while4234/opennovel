package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/voocel/ainovel-cli/internal/bootstrap"
)

const (
	scheduledResumeStateVersion = 1
	defaultResumeConcurrency    = 4
	defaultSchedulerRetryDelay  = time.Minute
)

type ScheduledResumeOutcome string

const (
	ScheduledResumeStarted ScheduledResumeOutcome = "started"
	ScheduledResumeSkipped ScheduledResumeOutcome = "skipped"
	ScheduledResumeFailed  ScheduledResumeOutcome = "failed"
)

// ScheduledResumeResult is the durable, non-sensitive result of evaluating one project.
type ScheduledResumeResult struct {
	Outcome    ScheduledResumeOutcome `json:"outcome"`
	Action     string                 `json:"action,omitempty"`
	ReasonCode string                 `json:"reason_code,omitempty"`
	Label      string                 `json:"label,omitempty"`
}

type ResumeSchedulerDeps struct {
	LoadConfig    func() (bootstrap.ResumeScheduleConfig, error)
	ListProjects  func() ([]ProjectManifest, error)
	ResumeProject func(context.Context, ProjectManifest) (ScheduledResumeResult, error)
	Now           func() time.Time
	LogError      func(string, error)
	MaxConcurrent int
	RetryDelay    time.Duration
}

type ResumeSchedulerStatus struct {
	NextRunAt     *time.Time                   `json:"next_run_at,omitempty"`
	NextTriggerAt *time.Time                   `json:"next_trigger_at,omitempty"`
	LastBatch     *ScheduledResumeBatchSummary `json:"last_batch,omitempty"`
}

type ScheduledResumeBatchSummary struct {
	StartedAt   time.Time `json:"started_at"`
	FinishedAt  time.Time `json:"finished_at"`
	Occurrences []string  `json:"occurrences"`
	Started     int       `json:"started"`
	Skipped     int       `json:"skipped"`
	Failed      int       `json:"failed"`
}

type ResumeScheduler struct {
	runtimeRoot string
	deps        ResumeSchedulerDeps
	wake        chan struct{}
	stateMu     sync.Mutex
	statusMu    sync.RWMutex
	status      ResumeSchedulerStatus
}

type scheduledResumeState struct {
	Version              int                          `json:"version"`
	CompletedOccurrences map[string]time.Time         `json:"completed_occurrences,omitempty"`
	ActiveBatch          *scheduledResumeBatch        `json:"active_batch,omitempty"`
	LastBatch            *ScheduledResumeBatchSummary `json:"last_batch,omitempty"`
}

type scheduledResumeBatch struct {
	StartedAt   time.Time                     `json:"started_at"`
	Occurrences []string                      `json:"occurrences"`
	Projects    []scheduledResumeProjectState `json:"projects"`
}

type scheduledResumeProjectState struct {
	ProjectID  string                 `json:"project_id"`
	Status     string                 `json:"status"`
	Result     *ScheduledResumeResult `json:"result,omitempty"`
	Error      string                 `json:"error,omitempty"`
	FinishedAt *time.Time             `json:"finished_at,omitempty"`
}

func NewResumeScheduler(runtimeRoot string, deps ResumeSchedulerDeps) *ResumeScheduler {
	if deps.Now == nil {
		deps.Now = time.Now
	}
	if deps.MaxConcurrent <= 0 {
		deps.MaxConcurrent = defaultResumeConcurrency
	}
	if deps.RetryDelay <= 0 {
		deps.RetryDelay = defaultSchedulerRetryDelay
	}
	return &ResumeScheduler{
		runtimeRoot: filepath.Clean(runtimeRoot),
		deps:        deps,
		wake:        make(chan struct{}, 1),
	}
}

// Wake asks Run to reload the schedule. Calls are coalesced and never block.
func (s *ResumeScheduler) Wake() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *ResumeScheduler) Status() ResumeSchedulerStatus {
	s.statusMu.RLock()
	defer s.statusMu.RUnlock()
	status := s.status
	if status.NextRunAt != nil {
		next := *status.NextRunAt
		status.NextRunAt = &next
	}
	if status.NextTriggerAt != nil {
		next := *status.NextTriggerAt
		status.NextTriggerAt = &next
	}
	if status.LastBatch != nil {
		last := *status.LastBatch
		last.Occurrences = append([]string(nil), last.Occurrences...)
		status.LastBatch = &last
	}
	return status
}

// Run blocks until ctx is cancelled. NewServer does not call it automatically;
// the production web lifecycle owns the goroutine and cancellation.
func (s *ResumeScheduler) Run(ctx context.Context) error {
	if s.deps.LoadConfig == nil || s.deps.ListProjects == nil || s.deps.ResumeProject == nil {
		return errors.New("resume scheduler dependencies are incomplete")
	}
	state, err := s.loadState()
	if err != nil {
		return err
	}
	if state.LastBatch != nil {
		s.setLastBatch(state.LastBatch)
	}

	var timer *time.Timer
	for {
		if err := s.evaluate(ctx, state); err != nil && !errors.Is(err, context.Canceled) {
			s.logError("evaluate scheduled resume", err)
		}
		if ctx.Err() != nil {
			return nil
		}

		next, err := s.nextTrigger(state)
		if err != nil {
			s.logError("calculate next scheduled resume", err)
			retryAt := s.deps.Now().Add(s.deps.RetryDelay)
			next = &retryAt
		}
		s.setNextTrigger(next)
		if next == nil {
			select {
			case <-ctx.Done():
				return nil
			case <-s.wake:
				continue
			}
		}
		delay := next.Sub(s.deps.Now())
		if delay < 0 {
			delay = 0
		}
		if timer == nil {
			timer = time.NewTimer(delay)
		} else {
			resetTimer(timer, delay)
		}

		select {
		case <-ctx.Done():
			stopTimer(timer)
			return nil
		case <-s.wake:
			stopTimer(timer)
		case <-timer.C:
		}
	}
}

func (s *ResumeScheduler) evaluate(ctx context.Context, state *scheduledResumeState) error {
	if state.ActiveBatch == nil {
		cfg, err := s.loadNormalizedConfig()
		if err != nil {
			return err
		}
		location, _ := time.LoadLocation(cfg.EffectiveTimezone())
		due := dueOccurrenceKeys(s.deps.Now(), location, cfg.DailyTimes, state.CompletedOccurrences)
		if len(due) == 0 {
			return nil
		}
		projects, err := s.deps.ListProjects()
		if err != nil {
			return fmt.Errorf("list projects: %w", err)
		}
		batch := &scheduledResumeBatch{StartedAt: s.deps.Now().UTC(), Occurrences: due}
		for _, project := range projects {
			batch.Projects = append(batch.Projects, scheduledResumeProjectState{ProjectID: project.ID, Status: "pending"})
		}
		state.ActiveBatch = batch
		if err := s.saveState(state); err != nil {
			return err
		}
	}
	return s.runActiveBatch(ctx, state)
}

func (s *ResumeScheduler) runActiveBatch(ctx context.Context, state *scheduledResumeState) error {
	projects, err := s.deps.ListProjects()
	if err != nil {
		return fmt.Errorf("list projects for active batch: %w", err)
	}
	byID := make(map[string]ProjectManifest, len(projects))
	for _, project := range projects {
		byID[project.ID] = project
	}

	jobs := make(chan int)
	var workers sync.WaitGroup
	workerCount := min(s.deps.MaxConcurrent, len(state.ActiveBatch.Projects))
	for range workerCount {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for index := range jobs {
				s.runProject(ctx, state, index, byID)
			}
		}()
	}
	for index := range state.ActiveBatch.Projects {
		if state.ActiveBatch.Projects[index].Status != "pending" {
			continue
		}
		select {
		case jobs <- index:
		case <-ctx.Done():
			close(jobs)
			workers.Wait()
			return ctx.Err()
		}
	}
	close(jobs)
	workers.Wait()
	if ctx.Err() != nil {
		return ctx.Err()
	}

	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	summary := summarizeScheduledBatch(state.ActiveBatch, s.deps.Now().UTC())
	if state.CompletedOccurrences == nil {
		state.CompletedOccurrences = make(map[string]time.Time)
	}
	for _, occurrence := range state.ActiveBatch.Occurrences {
		state.CompletedOccurrences[occurrence] = summary.FinishedAt
	}
	pruneCompletedOccurrences(state.CompletedOccurrences, s.deps.Now(), 2)
	state.LastBatch = summary
	state.ActiveBatch = nil
	if err := s.saveStateLocked(state); err != nil {
		return err
	}
	s.setLastBatch(summary)
	return nil
}

func (s *ResumeScheduler) runProject(ctx context.Context, state *scheduledResumeState, index int, projects map[string]ProjectManifest) {
	projectState := state.ActiveBatch.Projects[index]
	project, exists := projects[projectState.ProjectID]
	result := ScheduledResumeResult{Outcome: ScheduledResumeSkipped, ReasonCode: "project_missing"}
	var runErr error
	if exists {
		result, runErr = s.deps.ResumeProject(ctx, project)
		if result.Outcome == "" {
			result.Outcome = ScheduledResumeSkipped
		}
	}
	if runErr != nil {
		result.Outcome = ScheduledResumeFailed
	}
	finishedAt := s.deps.Now().UTC()

	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	projectState = state.ActiveBatch.Projects[index]
	projectState.Status = "done"
	projectState.Result = &result
	projectState.FinishedAt = &finishedAt
	if runErr != nil {
		projectState.Error = runErr.Error()
	}
	state.ActiveBatch.Projects[index] = projectState
	if err := s.saveStateLocked(state); err != nil {
		s.logError("persist scheduled resume project result", err)
	}
}

func (s *ResumeScheduler) nextTrigger(state *scheduledResumeState) (*time.Time, error) {
	if state.ActiveBatch != nil {
		now := s.deps.Now()
		return &now, nil
	}
	cfg, err := s.loadNormalizedConfig()
	if err != nil {
		return nil, err
	}
	if len(cfg.DailyTimes) == 0 {
		return nil, nil
	}
	location, err := time.LoadLocation(cfg.EffectiveTimezone())
	if err != nil {
		return nil, err
	}
	next := nextOccurrence(s.deps.Now(), location, cfg.DailyTimes, state.CompletedOccurrences)
	return &next, nil
}

func (s *ResumeScheduler) loadNormalizedConfig() (bootstrap.ResumeScheduleConfig, error) {
	cfg, err := s.deps.LoadConfig()
	if err != nil {
		return bootstrap.ResumeScheduleConfig{}, fmt.Errorf("load resume schedule: %w", err)
	}
	return bootstrap.NormalizeResumeSchedule(cfg)
}

func dueOccurrenceKeys(now time.Time, location *time.Location, dailyTimes []string, completed map[string]time.Time) []string {
	localNow := now.In(location)
	due := make([]string, 0, len(dailyTimes))
	for _, wallTime := range dailyTimes {
		occurrence := occurrenceAt(localNow, location, wallTime)
		key := occurrenceKey(occurrence, wallTime)
		if !occurrence.After(localNow) {
			if _, done := completed[key]; !done {
				due = append(due, key)
			}
		}
	}
	return due
}

func nextOccurrence(now time.Time, location *time.Location, dailyTimes []string, completed map[string]time.Time) time.Time {
	localNow := now.In(location)
	if len(dailyTimes) == 0 {
		return now.Add(24 * time.Hour)
	}
	for _, wallTime := range dailyTimes {
		candidate := occurrenceAt(localNow, location, wallTime)
		key := occurrenceKey(candidate, wallTime)
		if !candidate.Before(localNow) {
			if _, done := completed[key]; !done {
				return candidate
			}
		}
	}
	tomorrow := localNow.AddDate(0, 0, 1)
	return occurrenceAt(tomorrow, location, dailyTimes[0])
}

func occurrenceAt(day time.Time, location *time.Location, wallTime string) time.Time {
	hour := int((wallTime[0]-'0')*10 + wallTime[1] - '0')
	minute := int((wallTime[3]-'0')*10 + wallTime[4] - '0')
	return time.Date(day.Year(), day.Month(), day.Day(), hour, minute, 0, 0, location)
}

func occurrenceKey(occurrence time.Time, wallTime string) string {
	return occurrence.Format("2006-01-02") + "T" + wallTime
}

func summarizeScheduledBatch(batch *scheduledResumeBatch, finishedAt time.Time) *ScheduledResumeBatchSummary {
	summary := &ScheduledResumeBatchSummary{
		StartedAt: batch.StartedAt, FinishedAt: finishedAt,
		Occurrences: append([]string(nil), batch.Occurrences...),
	}
	for _, project := range batch.Projects {
		if project.Result == nil {
			summary.Failed++
			continue
		}
		switch project.Result.Outcome {
		case ScheduledResumeStarted:
			summary.Started++
		case ScheduledResumeFailed:
			summary.Failed++
		default:
			summary.Skipped++
		}
	}
	return summary
}

func pruneCompletedOccurrences(completed map[string]time.Time, now time.Time, keepDays int) {
	cutoff := now.AddDate(0, 0, -keepDays)
	for key, completedAt := range completed {
		if completedAt.Before(cutoff) {
			delete(completed, key)
		}
	}
}

func (s *ResumeScheduler) statePath() string {
	return filepath.Join(s.runtimeRoot, "meta", "scheduled-resume-state.json")
}

func (s *ResumeScheduler) loadState() (*scheduledResumeState, error) {
	data, err := os.ReadFile(s.statePath())
	if errors.Is(err, os.ErrNotExist) {
		return &scheduledResumeState{Version: scheduledResumeStateVersion, CompletedOccurrences: make(map[string]time.Time)}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read scheduled resume state: %w", err)
	}
	var state scheduledResumeState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("decode scheduled resume state: %w", err)
	}
	if state.Version != scheduledResumeStateVersion {
		return nil, fmt.Errorf("unsupported scheduled resume state version %d", state.Version)
	}
	if state.CompletedOccurrences == nil {
		state.CompletedOccurrences = make(map[string]time.Time)
	}
	s.setLastBatch(state.LastBatch)
	return &state, nil
}

func (s *ResumeScheduler) saveState(state *scheduledResumeState) error {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	return s.saveStateLocked(state)
}

func (s *ResumeScheduler) saveStateLocked(state *scheduledResumeState) error {
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("encode scheduled resume state: %w", err)
	}
	path := s.statePath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create scheduled resume metadata directory: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create scheduled resume temporary state: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write scheduled resume temporary state: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync scheduled resume temporary state: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close scheduled resume temporary state: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("replace scheduled resume state: %w", err)
	}
	return nil
}

func (s *ResumeScheduler) setNextTrigger(next *time.Time) {
	s.statusMu.Lock()
	defer s.statusMu.Unlock()
	if next == nil {
		s.status.NextRunAt = nil
		s.status.NextTriggerAt = nil
		return
	}
	nextCopy := *next
	s.status.NextRunAt = &nextCopy
	s.status.NextTriggerAt = &nextCopy
}

func (s *ResumeScheduler) setLastBatch(last *ScheduledResumeBatchSummary) {
	s.statusMu.Lock()
	defer s.statusMu.Unlock()
	if last == nil {
		s.status.LastBatch = nil
		return
	}
	copy := *last
	copy.Occurrences = append([]string(nil), last.Occurrences...)
	s.status.LastBatch = &copy
}

func (s *ResumeScheduler) logError(operation string, err error) {
	if s.deps.LogError != nil {
		s.deps.LogError(operation, err)
	}
}

func resetTimer(timer *time.Timer, delay time.Duration) {
	stopTimer(timer)
	timer.Reset(delay)
}

func stopTimer(timer *time.Timer) {
	if timer == nil || timer.Stop() {
		return
	}
	select {
	case <-timer.C:
	default:
	}
}
