package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/voocel/ainovel-cli/internal/retrypolicy"
)

const (
	actionRegistryVersion = 1
	actionRegistryRelPath = "meta/web-actions.json"
)

type ActionStatus string

const (
	ActionStatusRunning     ActionStatus = "running"
	ActionStatusCompleted   ActionStatus = "completed"
	ActionStatusFailed      ActionStatus = "failed"
	ActionStatusInterrupted ActionStatus = "interrupted"
)

var (
	ErrActionNotFound       = errors.New("action not found")
	ErrActionKeyRequired    = errors.New("idempotency_key is required for async actions")
	ErrActionRegistryClosed = errors.New("action registry is closed")
)

type ActionRecord struct {
	ActionID       string       `json:"action_id"`
	ProjectID      string       `json:"project_id"`
	Kind           string       `json:"kind"`
	IdempotencyKey string       `json:"idempotency_key"`
	Status         ActionStatus `json:"status"`
	Recoverable    bool         `json:"recoverable"`
	Error          string       `json:"error,omitempty"`
	CreatedAt      time.Time    `json:"created_at"`
	StartedAt      time.Time    `json:"started_at"`
	FinishedAt     *time.Time   `json:"finished_at,omitempty"`
}

type actionRegistryState struct {
	Version int            `json:"version"`
	Actions []ActionRecord `json:"actions"`
}

type ActionRegistry struct {
	mu        sync.Mutex
	projectID string
	path      string
	actions   map[string]ActionRecord
	byKey     map[string]string
	closed    bool
}

type actionLifecycle struct {
	started  func()
	finished func()
}

func NewActionRegistry(projectID, path string) (*ActionRegistry, error) {
	registry := &ActionRegistry{
		projectID: strings.TrimSpace(projectID),
		path:      strings.TrimSpace(path),
		actions:   make(map[string]ActionRecord),
		byKey:     make(map[string]string),
	}
	if err := registry.load(); err != nil {
		return nil, err
	}
	return registry, nil
}

func projectActionRegistryPath(manifest ProjectManifest) string {
	if strings.TrimSpace(manifest.RootDir) == "" {
		return ""
	}
	return filepath.Join(manifest.RootDir, filepath.FromSlash(actionRegistryRelPath))
}

func (r *ActionRegistry) Start(
	kind string,
	idempotencyKey string,
	run func(context.Context) error,
	lifecycle actionLifecycle,
) (ActionRecord, bool, error) {
	if r == nil {
		return ActionRecord{}, false, fmt.Errorf("action registry is unavailable")
	}
	kind = strings.TrimSpace(kind)
	idempotencyKey = strings.TrimSpace(idempotencyKey)
	if idempotencyKey == "" {
		return ActionRecord{}, false, ErrActionKeyRequired
	}
	if run == nil {
		return ActionRecord{}, false, fmt.Errorf("action runner is required")
	}

	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return ActionRecord{}, false, ErrActionRegistryClosed
	}
	scope := actionKeyScope(kind, idempotencyKey)
	if actionID, ok := r.byKey[scope]; ok {
		action := r.actions[actionID]
		r.mu.Unlock()
		return action, false, nil
	}
	now := time.Now().UTC()
	action := ActionRecord{
		ActionID:       "act_" + shortWorkflowHash(r.projectID, kind, idempotencyKey),
		ProjectID:      r.projectID,
		Kind:           kind,
		IdempotencyKey: idempotencyKey,
		Status:         ActionStatusRunning,
		CreatedAt:      now,
		StartedAt:      now,
	}
	r.actions[action.ActionID] = action
	r.byKey[scope] = action.ActionID
	if err := r.persistLocked(); err != nil {
		delete(r.actions, action.ActionID)
		delete(r.byKey, scope)
		r.mu.Unlock()
		return ActionRecord{}, false, err
	}
	r.mu.Unlock()

	if lifecycle.started != nil {
		lifecycle.started()
	}
	go r.execute(action.ActionID, run, lifecycle.finished)
	return action, true, nil
}

func (r *ActionRegistry) Get(actionID string) (ActionRecord, error) {
	if r == nil {
		return ActionRecord{}, ErrActionNotFound
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	action, ok := r.actions[strings.TrimSpace(actionID)]
	if !ok {
		return ActionRecord{}, ErrActionNotFound
	}
	return action, nil
}

func (r *ActionRegistry) find(kind, idempotencyKey string) (ActionRecord, bool) {
	if r == nil {
		return ActionRecord{}, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	actionID, ok := r.byKey[actionKeyScope(kind, idempotencyKey)]
	if !ok {
		return ActionRecord{}, false
	}
	action, ok := r.actions[actionID]
	return action, ok
}

func (r *ActionRegistry) Latest() *ActionRecord {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	var latest *ActionRecord
	for _, candidate := range r.actions {
		candidate := candidate
		if latest == nil || candidate.CreatedAt.After(latest.CreatedAt) {
			latest = &candidate
		}
	}
	return latest
}

func (r *ActionRegistry) Close() {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.closed = true
	r.mu.Unlock()
}

func (r *ActionRegistry) execute(actionID string, run func(context.Context) error, finished func()) {
	err := runBackgroundActionSafely(run)
	finishedAt := time.Now().UTC()

	r.mu.Lock()
	action, ok := r.actions[actionID]
	if ok {
		action.FinishedAt = &finishedAt
		if err != nil {
			action.Status = ActionStatusFailed
			action.Recoverable = true
			action.Error = strings.TrimSpace(retrypolicy.SanitizeProviderError(err))
		} else {
			action.Status = ActionStatusCompleted
			action.Recoverable = false
			action.Error = ""
		}
		r.actions[actionID] = action
		if persistErr := r.persistLocked(); persistErr != nil {
			slog.Error("persist web action status failed", "module", "web", "project", r.projectID, "action", actionID, "err", persistErr)
		}
	}
	r.mu.Unlock()
	if finished != nil {
		finished()
	}
}

func runBackgroundActionSafely(run func(context.Context) error) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("background action panicked: %v", recovered)
		}
	}()
	return run(context.Background())
}

func (r *ActionRegistry) load() error {
	if r.path == "" {
		return nil
	}
	data, err := os.ReadFile(r.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read action registry: %w", err)
	}
	var state actionRegistryState
	if err := json.Unmarshal(data, &state); err != nil {
		return fmt.Errorf("decode action registry: %w", err)
	}
	if state.Version != actionRegistryVersion {
		return fmt.Errorf("unsupported action registry version %d", state.Version)
	}
	interrupted := false
	now := time.Now().UTC()
	for _, action := range state.Actions {
		action.ActionID = strings.TrimSpace(action.ActionID)
		action.Kind = strings.TrimSpace(action.Kind)
		action.IdempotencyKey = strings.TrimSpace(action.IdempotencyKey)
		if action.ActionID == "" || action.Kind == "" || action.IdempotencyKey == "" {
			continue
		}
		if action.Status == ActionStatusRunning {
			action.Status = ActionStatusInterrupted
			action.Recoverable = true
			action.Error = "service restarted before the action completed"
			action.FinishedAt = &now
			interrupted = true
		}
		r.actions[action.ActionID] = action
		r.byKey[actionKeyScope(action.Kind, action.IdempotencyKey)] = action.ActionID
	}
	if interrupted {
		return r.persistLocked()
	}
	return nil
}

func (r *ActionRegistry) persistLocked() error {
	if r.path == "" {
		return nil
	}
	actions := make([]ActionRecord, 0, len(r.actions))
	for _, action := range r.actions {
		actions = append(actions, action)
	}
	sort.Slice(actions, func(i, j int) bool {
		return actions[i].CreatedAt.Before(actions[j].CreatedAt)
	})
	data, err := json.MarshalIndent(actionRegistryState{
		Version: actionRegistryVersion,
		Actions: actions,
	}, "", "  ")
	if err != nil {
		return fmt.Errorf("encode action registry: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(r.path), 0o755); err != nil {
		return fmt.Errorf("create action registry directory: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(r.path), filepath.Base(r.path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temporary action registry: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("secure temporary action registry: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write action registry: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync action registry: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close action registry: %w", err)
	}
	if err := os.Rename(tmpPath, r.path); err != nil {
		return fmt.Errorf("replace action registry: %w", err)
	}
	return nil
}

func actionKeyScope(kind, idempotencyKey string) string {
	return strings.TrimSpace(kind) + "\x00" + strings.TrimSpace(idempotencyKey)
}

func (s *ProjectSession) StartBackgroundAction(
	kind string,
	idempotencyKey string,
	run func(context.Context) error,
) (ActionRecord, bool, error) {
	return s.startBackgroundAction(kind, idempotencyKey, run, false)
}

func (s *ProjectSession) StartCancellableBackgroundAction(
	kind string,
	idempotencyKey string,
	run func(context.Context) error,
) (ActionRecord, bool, error) {
	return s.startBackgroundAction(kind, idempotencyKey, run, true)
}

func (s *ProjectSession) startBackgroundAction(
	kind string,
	idempotencyKey string,
	run func(context.Context) error,
	cancellable bool,
) (ActionRecord, bool, error) {
	if s == nil || s.actions == nil {
		return ActionRecord{}, false, fmt.Errorf("project action registry is unavailable")
	}
	idempotencyKey = strings.TrimSpace(idempotencyKey)
	if idempotencyKey == "" {
		return ActionRecord{}, false, ErrActionKeyRequired
	}
	if run == nil {
		return ActionRecord{}, false, fmt.Errorf("action runner is required")
	}
	if action, ok := s.actions.find(kind, idempotencyKey); ok {
		return action, false, nil
	}
	finishAction, err := s.beginActionKind(kind)
	if err != nil {
		return ActionRecord{}, false, err
	}
	action, created, err := s.actions.Start(kind, idempotencyKey, func(ctx context.Context) error {
		actionCtx, contextErr := s.normalFlowActionContext(ctx)
		if contextErr != nil {
			return contextErr
		}
		if cancellable {
			actionCtx, cancel := context.WithCancel(actionCtx)
			s.setActionCancel(kind, cancel)
			defer func() {
				s.clearActionCancel()
				cancel()
			}()
			return run(actionCtx)
		}
		return run(actionCtx)
	}, actionLifecycle{
		started: func() {
			s.AppendSnapshot()
		},
		finished: func() {
			s.AppendSnapshot()
			finishAction()
		},
	})
	if err != nil || !created {
		finishAction()
	}
	return action, created, err
}

func (s *ProjectSession) LatestBackgroundAction() *ActionRecord {
	if s == nil || s.actions == nil {
		return nil
	}
	return s.actions.Latest()
}

func (s *ProjectSession) BackgroundAction(actionID string) (ActionRecord, error) {
	if s == nil || s.actions == nil {
		return ActionRecord{}, ErrActionNotFound
	}
	return s.actions.Get(actionID)
}

func (s *Server) handleProjectBackgroundAction(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	actionID := strings.TrimSpace(r.URL.Query().Get("action_id"))
	if actionID == "" {
		writeError(w, http.StatusBadRequest, "action_id is required")
		return
	}
	session, manifest, err := s.sessions.Open(id)
	if err != nil {
		writeProjectSessionError(w, err)
		return
	}
	action, err := session.BackgroundAction(actionID)
	if err != nil {
		if errors.Is(err, ErrActionNotFound) {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if action.Status != ActionStatusRunning && session.isActionRunning(action.Kind) {
		action.Status = ActionStatusRunning
		action.Recoverable = false
		action.Error = ""
		action.FinishedAt = nil
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"project":  manifest,
		"action":   action,
		"snapshot": session.WebSnapshot(),
	})
}

func writeBackgroundActionStartError(w http.ResponseWriter, err error) {
	if errors.Is(err, ErrActionKeyRequired) {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if errors.Is(err, ErrSessionActionInProgress) {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	writeError(w, http.StatusInternalServerError, err.Error())
}

func writeBackgroundActionAccepted(
	w http.ResponseWriter,
	r *http.Request,
	manifest ProjectManifest,
	session *ProjectSession,
	action ActionRecord,
	created bool,
) {
	w.Header().Set("location", r.URL.Path+"?action_id="+action.ActionID)
	writeJSON(w, http.StatusAccepted, map[string]any{
		"project":   manifest,
		"action_id": action.ActionID,
		"action":    action,
		"created":   created,
		"snapshot":  session.WebSnapshot(),
	})
}
