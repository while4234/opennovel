package web

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/voocel/ainovel-cli/assets"
	"github.com/voocel/ainovel-cli/internal/bootstrap"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/entry/startup"
	"github.com/voocel/ainovel-cli/internal/expansionauditorclient"
	"github.com/voocel/ainovel-cli/internal/grokauth"
	"github.com/voocel/ainovel-cli/internal/host"
	"github.com/voocel/ainovel-cli/internal/host/adapt"
	continuationpkg "github.com/voocel/ainovel-cli/internal/host/continuation"
	"github.com/voocel/ainovel-cli/internal/host/exp"
	"github.com/voocel/ainovel-cli/internal/host/imp"
	"github.com/voocel/ainovel-cli/internal/host/sim"
	"github.com/voocel/ainovel-cli/internal/retrypolicy"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
	"github.com/voocel/ainovel-cli/internal/tools"
)

var (
	ErrProjectNotFound         = errors.New("project not found")
	ErrSessionActionInProgress = errors.New("project action already in progress")
	ErrProjectStyleLocked      = errors.New("project style is locked")
	ErrProjectSimulationLocked = errors.New("project simulation mode is locked")
)

const (
	webEventTypeHostEvent   = "host_event"
	webEventTypeStreamDelta = "stream_delta"
	webEventTypeStreamClear = "stream_clear"
	webEventTypeSnapshot    = "snapshot"
	webEventTypeCoCreate    = "cocreate_state"
	webEventTypeAction      = "action"

	webEventHistoryLimit = 1000

	projectActionKindAdaptationAnalysis = "adaptation_analysis"
	projectActionKindAdaptationUpload   = "adaptation_upload"
	projectActionKindAdaptationProposal = "adaptation_proposal"
	projectActionKindAdaptationRevision = "adaptation_proposal_revision"
	projectActionKindContinuation       = "continuation_planning"
	projectActionKindSimulationAnalysis = "simulation_analysis"
	projectActionKindSimulationImport   = "simulation_import"
	projectActionKindSimulationUpload   = "simulation_upload"
	projectActionKindSemanticAudit      = "semantic_audit"
	projectActionKindCharacterAnalyze   = "character_analyze"
	projectActionKindCharacterReview    = "character_review"
	projectActionKindCharacterRetry     = "character_retry"
	projectActionKindPlanningRevision   = "planning_revision"
	webCoCreateCheckpointRelPath        = "meta/sessions/web-cocreate-checkpoint.json"
	webCoCreateLogRelPath               = "meta/sessions/cocreate.jsonl"
	webEventSeqRelPath                  = "meta/runtime/web-event-seq.json"
	rollbackActionWaitTimeout           = 30 * time.Second
	sseHeartbeatInterval                = 15 * time.Second
)

type webResumeActionKind string

const (
	webResumeActionAdaptationAnalysis        webResumeActionKind = "adaptation_analysis"
	webResumeActionAdaptationProposal        webResumeActionKind = "adaptation_proposal"
	webResumeActionAdaptationProposalDetails webResumeActionKind = "adaptation_proposal_details"
)

type webResumeAction struct {
	Kind            webResumeActionKind
	Label           string
	SourcePath      string
	ProposalOptions adapt.ProposalOptions
}

type SessionManager struct {
	cfg      bootstrap.Config
	bundle   assets.Bundle
	store    *ProjectStore
	openHost func(bootstrap.Config, assets.Bundle, ProjectManifest) (projectHost, error)

	mu       sync.Mutex
	sessions map[string]*ProjectSession
	openings map[string]*projectSessionOpening
}

type projectSessionOpening struct {
	done    chan struct{}
	session *ProjectSession
	err     error
}

type projectHost interface {
	Snapshot() host.UISnapshot
	PrepareUserRules(string) error
	PrepareExternalSourceUserRules(string) error
	SetWordBudget(*domain.WordBudget) error
	StartPrepared(string) error
	Abort() bool
	RollbackPreview() (domain.RollbackPreview, error)
	Rollback(domain.RollbackRequest) (domain.RollbackResult, error)
	Resume() (string, error)
	ReviseChapter(host.ChapterRevisionRequest) (host.ChapterRevisionResult, error)
	ReviseChapterOutline(context.Context, host.ChapterOutlineRevisionRequest) (host.ChapterOutlineRevisionResult, error)
	Continue(string) error
	Steer(string) error
	CoCreateStream(context.Context, []host.CoCreateMessage, func(kind, text string)) (host.CoCreateReply, error)
	StageCoCreateStream(context.Context, []host.CoCreateMessage, func(kind, text string)) (host.CoCreateReply, error)
	ContinuationCoCreateStream(context.Context, []host.CoCreateMessage, func(kind, text string)) (host.CoCreateReply, error)
	ContinuationSnapshot() (*domain.ContinuationSnapshot, error)
	BeginContinuationDraft(int) (*domain.ContinuationSnapshot, error)
	CommitContinuationDraft(string, int) (*domain.ContinuationSnapshot, error)
	GenerateContinuationProposal(context.Context, int) (*domain.ContinuationSnapshot, error)
	ReviseContinuationProposal(context.Context, string, int) (*domain.ContinuationSnapshot, error)
	ApproveContinuationProposal(context.Context, int) (*domain.ContinuationSnapshot, error)
	ReviseContinuationVolumes(context.Context, string, int, int) (*domain.ContinuationSnapshot, error)
	ApproveContinuationVolumes(context.Context, int) (*domain.ContinuationSnapshot, error)
	GenerateContinuationOutlines(context.Context, int) (*domain.ContinuationSnapshot, error)
	ReviseContinuationOutlines(context.Context, continuationpkg.OutlineRevisionInput, int) (*domain.ContinuationSnapshot, error)
	ApproveContinuationOutlines(int) (*domain.ContinuationSnapshot, error)
	RetryContinuation(context.Context, int) (*domain.ContinuationSnapshot, error)
	StartContinuation(int) (string, *domain.ContinuationSnapshot, error)
	AdaptCoCreateStream(context.Context, []host.CoCreateMessage, func(kind, text string)) (host.CoCreateReply, error)
	EnsureAdaptationCoCreateBriefing(context.Context, string, domain.AdaptationCoCreateIntent) (*domain.AdaptationCoCreateBriefing, error)
	EnsureAdaptationProposalCoCreateBriefing(context.Context, string, domain.AdaptationCoCreateIntent) (*domain.AdaptationCoCreateBriefing, error)
	ResolveAdaptationCoCreateDecision(string, string, string) (*domain.AdaptationCoCreateBriefing, error)
	ResolveAdaptationCoCreateDecisions([]domain.AdaptationResolvedDecision) (*domain.AdaptationCoCreateBriefing, error)
	PauseForCoCreate() bool
	ResumeFromCoCreate(string) error
	CancelCoCreate()
	ImportFrom(context.Context, imp.Options) (<-chan imp.Event, error)
	SimulateFromDir(context.Context, string) (<-chan sim.Event, error)
	ImportSimulationProfile(context.Context, string) (<-chan sim.Event, error)
	PrepareAdaptationSource(context.Context, string) (<-chan adapt.Event, error)
	BuildAdaptationProposalContext(context.Context, adapt.ProposalOptions) (*domain.AdaptationPlan, error)
	BuildAdaptationProposalVolumesContext(context.Context, adapt.ProposalOptions) (*adapt.ProposalStageResult, error)
	ReviseAdaptationProposalContext(context.Context, adapt.ProposalRevisionOptions) (*domain.AdaptationPlan, error)
	ReviseAdaptationVolumeReviewContext(context.Context, adapt.ProposalRevisionOptions) (*domain.AdaptationVolumeReview, error)
	BuildAdaptationProposalDetailsContext(context.Context, adapt.ProposalDetailsOptions) (*domain.AdaptationPlan, error)
	GenerateAdaptationTargetFoundationContext(context.Context, adapt.TargetFoundationOptions) (*domain.AdaptationFoundationReview, error)
	StartAdaptationCharacterWorkflow(string) error
	ConfirmAdaptationProposal() (*domain.AdaptationPlan, error)
	StartAdaptationPreparedWithOptions(adapt.ProposalOptions) error
	Export(context.Context, exp.Options) (*exp.Result, error)
	ReplayQueue(int64) ([]domain.RuntimeQueueItem, error)
	ConfiguredProviders() []string
	ConfiguredModels(string) []string
	ProviderConfig(string) (bootstrap.ProviderConfig, bool)
	ModelAutoSwitchConfig() bootstrap.ModelAutoSwitchConfig
	CurrentModelSelection(string) (string, string, bool)
	SwitchModel(string, string, string) error
	ClearModelRoute(string) error
	AddProviderModel(string, string, bootstrap.ProviderConfig, string) error
	ConfigureProviderModel(context.Context, host.ProviderModelUpdate) error
	SyncModelSettingsFromGlobal(bootstrap.Config) error
	SyncInheritedProviderFromGlobal(bootstrap.Config, string, string) error
	SyncInheritedProviderModelRemovalFromGlobal(bootstrap.Config, string, string) error
	TestProviderModel(context.Context, string, string, bootstrap.ProviderConfig, string) (host.ProviderModelTestResult, error)
	TestConfiguredProviderModel(context.Context, host.ProviderModelUpdate) (host.ProviderModelTestResult, error)
	DiscoverProviderModels(context.Context, string, bootstrap.ProviderConfig, string) (host.ProviderModelDiscoveryResult, error)
	DiscoverConfiguredProviderModels(context.Context, host.ProviderModelUpdate) (host.ProviderModelDiscoveryResult, error)
	RemoveProviderModel(string, string) error
	StartGrokLogin(string, string) (grokauth.LoginStart, error)
	PollGrokLogin() (grokauth.LoginPoll, error)
	CompleteGrokLogin(string) (grokauth.AuthStatus, error)
	GrokLoginStatus(string) grokauth.AuthStatus
	CurrentThinking(string) string
	SetRoleThinking(string, string) error
	CurrentCoCreateTimeoutSeconds() int
	SetCoCreateTimeoutSeconds(int) error
	CurrentCoCreateMaxTokens() int
	SetCoCreateMaxTokens(int) error
	CurrentStructureRepairMaxAttempts() int
	CurrentBudgetQualityMaxAttempts() int
	CurrentAdaptationOutlineAuditRetryMaxAttempts() int
	SetRetrySettings(int, int, int, int) error
	Events() <-chan host.Event
	Stream() <-chan string
	Done() <-chan struct{}
	Close()
}

type simulationActionHost interface {
	SimulateFromDirWithAction(context.Context, string, string) (<-chan sim.Event, error)
}

type normalFlowActionHost interface {
	BeginNormalFlowAction(string) (func(), error)
}

type normalFlowActionContextHost interface {
	NormalFlowActionContext(context.Context) (context.Context, error)
}

type foundationRevisionRouteHost interface {
	ResumeFoundationRevision() (string, error)
}

type foundationAdaptationRevisionHost interface {
	BuildAdaptationProposalVolumesForFoundationRevision(context.Context, adapt.ProposalOptions) (*adapt.ProposalStageResult, error)
	BuildAdaptationProposalDetailsForFoundationRevision(context.Context) (*domain.AdaptationPlan, error)
	ConfirmAdaptationProposalForFoundationRevision() (*domain.AdaptationPlan, error)
}

type scheduledResumeHost interface {
	ScheduledResumeEnabled() bool
	SetScheduledResumeEnabled(bool) error
}

type manuscriptRevisionHost interface {
	ManuscriptRevisionService() *host.ManuscriptRevisionService
}

type manuscriptActionDialogueHost interface {
	ClarifyManuscriptAction(context.Context, host.ManuscriptActionClarificationRequest) (host.ManuscriptActionClarification, error)
}

type characterWorkspaceHost interface {
	CharacterWorkspaceService() *host.CharacterWorkspaceService
}

func (s *ProjectSession) ManuscriptRevisionService() *host.ManuscriptRevisionService {
	if s == nil {
		return nil
	}
	h, ok := s.host.(manuscriptRevisionHost)
	if !ok {
		return nil
	}
	return h.ManuscriptRevisionService()
}

func (s *ProjectSession) ClarifyManuscriptAction(ctx context.Context, request host.ManuscriptActionClarificationRequest) (host.ManuscriptActionClarification, error) {
	if s == nil {
		return host.ManuscriptActionClarification{}, fmt.Errorf("project session is unavailable")
	}
	clarifier, ok := s.host.(manuscriptActionDialogueHost)
	if !ok {
		return host.ManuscriptActionClarification{}, fmt.Errorf("manuscript action clarification is unavailable")
	}
	return clarifier.ClarifyManuscriptAction(ctx, request)
}

func NewSessionManager(cfg bootstrap.Config, bundle assets.Bundle, store *ProjectStore) *SessionManager {
	manager := &SessionManager{
		cfg:      cfg,
		bundle:   bundle,
		store:    store,
		sessions: make(map[string]*ProjectSession),
		openings: make(map[string]*projectSessionOpening),
	}
	manager.openHost = func(cfg bootstrap.Config, bundle assets.Bundle, manifest ProjectManifest) (projectHost, error) {
		return store.OpenProjectHost(cfg, bundle, manifest)
	}
	return manager
}

func (m *SessionManager) SetConfig(cfg bootstrap.Config) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cfg = cloneWebConfig(cfg)
}

func (m *SessionManager) SyncInheritedProviderFromGlobal(cfg bootstrap.Config, originalProvider, provider string) error {
	m.mu.Lock()
	m.cfg = cloneWebConfig(cfg)
	sessions := make([]*ProjectSession, 0, len(m.sessions))
	for _, session := range m.sessions {
		sessions = append(sessions, session)
	}
	m.mu.Unlock()

	var errs []error
	for _, session := range sessions {
		if err := session.SyncInheritedProviderFromGlobal(cfg, originalProvider, provider); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (m *SessionManager) SyncInheritedProviderModelRemovalFromGlobal(cfg bootstrap.Config, provider, model string) error {
	m.mu.Lock()
	m.cfg = cloneWebConfig(cfg)
	sessions := make([]*ProjectSession, 0, len(m.sessions))
	for _, session := range m.sessions {
		sessions = append(sessions, session)
	}
	m.mu.Unlock()

	var errs []error
	for _, session := range sessions {
		if err := session.SyncInheritedProviderModelRemovalFromGlobal(cfg, provider, model); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (m *SessionManager) SyncModelSettingsFromGlobal(cfg bootstrap.Config) error {
	m.mu.Lock()
	m.cfg = cloneWebConfig(cfg)
	sessions := make([]*ProjectSession, 0, len(m.sessions))
	for _, session := range m.sessions {
		sessions = append(sessions, session)
	}
	m.mu.Unlock()

	var errs []error
	for _, session := range sessions {
		if err := session.SyncModelSettingsFromGlobal(cfg); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (m *SessionManager) Open(id string) (*ProjectSession, ProjectManifest, error) {
	id = strings.TrimSpace(id)
	if err := validateProjectID(id); err != nil {
		return nil, ProjectManifest{}, fmt.Errorf("%w: %v", ErrProjectNotFound, err)
	}
	if session := m.Project(id); session != nil {
		return session, session.Manifest(), nil
	}

	manifest, err := m.store.OpenProject(id)
	if err != nil {
		return nil, ProjectManifest{}, fmt.Errorf("%w: %v", ErrProjectNotFound, err)
	}
	session, err := m.openSession(manifest)
	if err != nil {
		return nil, ProjectManifest{}, err
	}
	session.SetManifest(manifest)
	return session, manifest, nil
}

func (m *SessionManager) openSession(manifest ProjectManifest) (*ProjectSession, error) {
	id := manifest.ID
	m.mu.Lock()
	if session, ok := m.sessions[id]; ok {
		m.mu.Unlock()
		return session, nil
	}
	if opening, ok := m.openings[id]; ok {
		m.mu.Unlock()
		<-opening.done
		return opening.session, opening.err
	}
	opening := &projectSessionOpening{done: make(chan struct{})}
	m.openings[id] = opening
	cfg := cloneWebConfig(m.cfg)
	m.mu.Unlock()

	h, err := m.openHost(cfg, m.bundle, manifest)
	if err != nil {
		opening.err = err
	} else {
		opening.session, opening.err = NewProjectSession(manifest, h)
		if opening.err != nil {
			h.Close()
		}
	}

	m.mu.Lock()
	if opening.err == nil {
		m.sessions[id] = opening.session
	}
	delete(m.openings, id)
	close(opening.done)
	m.mu.Unlock()
	return opening.session, opening.err
}

// OpenScheduled opens a manifest discovered by the scheduler without touching
// project.json timestamps. Scheduled checks must not reorder the user's recent
// project list.
func (m *SessionManager) OpenScheduled(manifest ProjectManifest) (*ProjectSession, error) {
	id := strings.TrimSpace(manifest.ID)
	if err := validateProjectID(id); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrProjectNotFound, err)
	}
	return m.openSession(manifest)
}

func (m *SessionManager) SetProjectStyle(id, style string) (*ProjectSession, ProjectManifest, error) {
	id = strings.TrimSpace(id)
	if err := validateProjectID(id); err != nil {
		return nil, ProjectManifest{}, fmt.Errorf("%w: %v", ErrProjectNotFound, err)
	}
	style = assets.NormalizeStyleID(style)

	m.mu.Lock()
	defer m.mu.Unlock()

	manifest, err := m.store.OpenProject(id)
	if err != nil {
		return nil, ProjectManifest{}, fmt.Errorf("%w: %v", ErrProjectNotFound, err)
	}
	session, ok := m.sessions[id]
	if !ok {
		h, err := m.store.OpenProjectHost(m.cfg, m.bundle, manifest)
		if err != nil {
			return nil, ProjectManifest{}, err
		}
		session, err = NewProjectSession(manifest, h)
		if err != nil {
			h.Close()
			return nil, ProjectManifest{}, err
		}
		m.sessions[id] = session
	}

	unlock, err := session.beginAction()
	if err != nil {
		return nil, ProjectManifest{}, err
	}
	defer unlock()

	snapshot := session.Snapshot()
	if snapshotHasStartedWriting(snapshot) {
		return nil, ProjectManifest{}, fmt.Errorf("%w: cannot change style after writing has started", ErrProjectStyleLocked)
	}
	if snapshot.IsRunning {
		return nil, ProjectManifest{}, fmt.Errorf("%w: stop or pause the current project task before changing style", ErrSessionActionInProgress)
	}

	session.Close()
	delete(m.sessions, id)
	if err := m.store.SaveProjectStyle(manifest, style); err != nil {
		return nil, ProjectManifest{}, err
	}
	h, err := m.store.OpenProjectHost(m.cfg, m.bundle, manifest)
	if err != nil {
		return nil, ProjectManifest{}, err
	}
	next, err := NewProjectSession(manifest, h)
	if err != nil {
		h.Close()
		return nil, ProjectManifest{}, err
	}
	m.sessions[id] = next
	return next, manifest, nil
}

func (m *SessionManager) SetProjectSimulationMode(id, mode string) (*ProjectSession, ProjectManifest, error) {
	id = strings.TrimSpace(id)
	if err := validateProjectID(id); err != nil {
		return nil, ProjectManifest{}, fmt.Errorf("%w: %v", ErrProjectNotFound, err)
	}
	normalized, err := bootstrap.NormalizeSimulationMode(mode)
	if err != nil {
		return nil, ProjectManifest{}, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	manifest, err := m.store.OpenProject(id)
	if err != nil {
		return nil, ProjectManifest{}, fmt.Errorf("%w: %v", ErrProjectNotFound, err)
	}
	session, ok := m.sessions[id]
	if !ok {
		h, err := m.store.OpenProjectHost(cloneWebConfig(m.cfg), m.bundle, manifest)
		if err != nil {
			return nil, ProjectManifest{}, err
		}
		session, err = NewProjectSession(manifest, h)
		if err != nil {
			h.Close()
			return nil, ProjectManifest{}, err
		}
		m.sessions[id] = session
	}

	unlock, err := session.beginAction()
	if err != nil {
		return nil, ProjectManifest{}, err
	}
	defer unlock()

	if session.Snapshot().IsRunning {
		return nil, ProjectManifest{}, fmt.Errorf("%w: cannot change simulation mode while project is running", ErrProjectSimulationLocked)
	}
	if session.cocreate != nil {
		return nil, ProjectManifest{}, fmt.Errorf("%w: cannot change simulation mode during active co-create", ErrProjectSimulationLocked)
	}

	session.Close()
	delete(m.sessions, id)
	if err := m.store.SaveProjectSimulationMode(manifest, normalized); err != nil {
		return nil, ProjectManifest{}, err
	}
	h, err := m.store.OpenProjectHost(cloneWebConfig(m.cfg), m.bundle, manifest)
	if err != nil {
		return nil, ProjectManifest{}, err
	}
	next, err := NewProjectSession(manifest, h)
	if err != nil {
		h.Close()
		return nil, ProjectManifest{}, err
	}
	m.sessions[id] = next
	return next, manifest, nil
}

func (m *SessionManager) ActiveProjectIDs() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	ids := make([]string, 0, len(m.sessions))
	for id := range m.sessions {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func (m *SessionManager) Project(id string) *ProjectSession {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sessions[strings.TrimSpace(id)]
}

func (m *SessionManager) CloseProject(id string) bool {
	id = strings.TrimSpace(id)
	m.mu.Lock()
	session, ok := m.sessions[id]
	if ok {
		delete(m.sessions, id)
	}
	m.mu.Unlock()
	if ok {
		session.Close()
	}
	return ok
}

func (m *SessionManager) CloseAll() {
	m.mu.Lock()
	sessions := make([]*ProjectSession, 0, len(m.sessions))
	for id, session := range m.sessions {
		sessions = append(sessions, session)
		delete(m.sessions, id)
	}
	m.mu.Unlock()

	for _, session := range sessions {
		session.Close()
	}
}

type ProjectSession struct {
	manifest            ProjectManifest
	host                projectHost
	normalRevisions     *host.NormalRevisionService
	adaptationRevisions *host.AdaptationRevisionService
	characterWorkspace  *host.CharacterWorkspaceService
	expansionPlanner    *host.ExpansionPlanner
	expansionAuditorErr error

	mu                  sync.Mutex
	autoResumeMu        sync.Mutex
	actionMu            sync.Mutex
	actionKinds         map[string]int
	actionRevisionStore *storepkg.RevisionStore
	actionRevisionLease *storepkg.NormalFlowLease
	actionLeaseRelease  func()
	actionCancelMu      sync.Mutex
	actionCancel        context.CancelFunc
	actionKind          string
	nextSeq             int64
	history             []WebEvent
	hostEventAt         map[string]int
	subscribers         map[chan WebEvent]struct{}
	sequencePath        string
	cocreate            *webCoCreateSession
	actions             *ActionRegistry
	closed              bool
}

func NewProjectSession(manifest ProjectManifest, h projectHost) (*ProjectSession, error) {
	actions, err := NewActionRegistry(manifest.ID, projectActionRegistryPath(manifest))
	if err != nil {
		return nil, fmt.Errorf("open project action registry: %w", err)
	}
	sequencePath := ""
	if strings.TrimSpace(manifest.OutputDir) != "" {
		sequencePath = filepath.Join(manifest.OutputDir, filepath.FromSlash(webEventSeqRelPath))
	}
	session := &ProjectSession{
		manifest:            manifest,
		host:                h,
		normalRevisions:     host.NewNormalRevisionService(storepkg.NewStore(manifest.OutputDir)),
		adaptationRevisions: host.NewAdaptationRevisionService(storepkg.NewStore(manifest.OutputDir)),
		actions:             actions,
		actionKinds:         make(map[string]int),
		hostEventAt:         make(map[string]int),
		subscribers:         make(map[chan WebEvent]struct{}),
		sequencePath:        sequencePath,
	}
	if characterHost, ok := h.(characterWorkspaceHost); ok {
		session.characterWorkspace = characterHost.CharacterWorkspaceService()
	}
	if session.characterWorkspace == nil {
		session.characterWorkspace = host.NewCharacterWorkspaceService(storepkg.NewStore(manifest.OutputDir), nil)
	}
	if expansionHost, ok := h.(interface{ ExpansionPlanner() *host.ExpansionPlanner }); ok {
		client, clientErr := expansionauditorclient.New()
		if clientErr != nil {
			session.expansionAuditorErr = fmt.Errorf("initialize required expansion auditor component: %w", clientErr)
		} else if clientErr = client.Init(context.Background(), manifest.OutputDir); clientErr != nil {
			session.expansionAuditorErr = fmt.Errorf("initialize required expansion auditor component %q: %w", client.Command(), clientErr)
		}
		if session.expansionAuditorErr != nil {
			slog.Error("required expansion auditor unavailable", "module", "web", "project", manifest.ID, "err", session.expansionAuditorErr)
		}
		session.expansionPlanner = expansionHost.ExpansionPlanner()
		if session.expansionPlanner == nil {
			return nil, fmt.Errorf("initialize expansion planner after auditor bootstrap")
		}
	}
	if err := session.loadPersistedSequence(); err != nil {
		return nil, fmt.Errorf("load web event sequence: %w", err)
	}
	if err := session.seedHistory(); err != nil {
		return nil, err
	}
	if err := session.restoreCoCreateCheckpoint(); err != nil {
		slog.Warn("restore co-create checkpoint failed", "module", "web", "project", manifest.ID, "err", err)
	}
	go session.pump()
	return session, nil
}

func (s *ProjectSession) CharacterWorkspaceService() *host.CharacterWorkspaceService {
	if s == nil {
		return nil
	}
	return s.characterWorkspace
}

func (s *ProjectSession) ExpansionPlanner() *host.ExpansionPlanner {
	if s == nil {
		return nil
	}
	return s.expansionPlanner
}

func (s *ProjectSession) ExpansionAuditorError() error {
	if s == nil {
		return fmt.Errorf("expansion project session is unavailable")
	}
	return s.expansionAuditorErr
}

// PreviewNormalStructureRevision is the web/host production boundary for
// normal co-creation revisions. HTTP presentation can remain separate; every
// caller receives the same persistent impact-preview and kernel-sealed result.
func (s *ProjectSession) PreviewNormalStructureRevision(ctx context.Context, planner host.StructureRevisionPlanner, request domain.StructureRevisionRequest, idempotencyKey string) (*host.NormalStructureRevisionPreview, error) {
	if s == nil || s.normalRevisions == nil {
		return nil, fmt.Errorf("normal revision service is unavailable")
	}
	return s.normalRevisions.Preview(ctx, planner, request, idempotencyKey)
}

func (s *ProjectSession) NormalRevisionService() *host.NormalRevisionService {
	if s == nil {
		return nil
	}
	return s.normalRevisions
}

func (s *ProjectSession) AdaptationRevisionService() *host.AdaptationRevisionService {
	if s == nil {
		return nil
	}
	return s.adaptationRevisions
}

func (s *ProjectSession) SetManifest(manifest ProjectManifest) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.manifest = manifest
}

func (s *ProjectSession) Manifest() ProjectManifest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.manifest
}

func (s *ProjectSession) Snapshot() host.UISnapshot {
	snap := s.host.Snapshot()
	return s.overlayActionSnapshot(snap)
}

func (s *ProjectSession) ScheduledResumeEnabled() bool {
	h, ok := s.host.(scheduledResumeHost)
	return !ok || h.ScheduledResumeEnabled()
}

func (s *ProjectSession) SetScheduledResumeEnabled(enabled bool) error {
	h, ok := s.host.(scheduledResumeHost)
	if !ok {
		return fmt.Errorf("project host does not support scheduled resume settings")
	}
	return h.SetScheduledResumeEnabled(enabled)
}

func (s *ProjectSession) ModelConfig() apiModelConfig {
	providers := s.host.ConfiguredProviders()
	autoSwitch := s.host.ModelAutoSwitchConfig()
	outProviders := make([]apiModelProvider, 0, len(providers))
	for _, provider := range providers {
		pc, _ := s.host.ProviderConfig(provider)
		outProviders = append(outProviders, apiProviderFromConfig(provider, pc, s.host.ConfiguredModels(provider), autoSwitch))
	}
	roles := make([]apiModelRoute, 0, len(modelConfigRoles))
	for _, role := range modelConfigRoles {
		provider, model, explicit := s.host.CurrentModelSelection(role)
		roles = append(roles, apiModelRoute{
			Role:            normalizeModelRole(role),
			Provider:        provider,
			Model:           model,
			Explicit:        explicit,
			ReasoningEffort: s.host.CurrentThinking(role),
		})
	}
	stages := make([]apiModelRoute, 0, len(bootstrap.KnownModelStages))
	for _, stage := range bootstrap.KnownModelStages {
		key := bootstrap.StageRouteKey(stage)
		provider, model, explicit := s.host.CurrentModelSelection(key)
		stages = append(stages, apiModelRoute{
			Role:            key,
			Label:           globalModelStageLabels[stage],
			FallbackRole:    bootstrap.StageFallbackRole(stage),
			Provider:        provider,
			Model:           model,
			Explicit:        explicit,
			ReasoningEffort: s.host.CurrentThinking(key),
		})
	}
	return apiModelConfig{
		Providers:                              outProviders,
		Roles:                                  roles,
		Stages:                                 stages,
		CoCreateTimeoutSeconds:                 s.host.CurrentCoCreateTimeoutSeconds(),
		CoCreateMaxTokens:                      s.host.CurrentCoCreateMaxTokens(),
		StructureRepairMaxAttempts:             s.host.CurrentStructureRepairMaxAttempts(),
		BudgetQualityMaxAttempts:               s.host.CurrentBudgetQualityMaxAttempts(),
		AdaptationOutlineAuditRetryMaxAttempts: s.host.CurrentAdaptationOutlineAuditRetryMaxAttempts(),
		ThinkingLevels: []string{
			"",
			"off",
			"low",
			"medium",
			"high",
			"xhigh",
			"max",
		},
		ThinkingRule:    "default applies to coordinator, architect, character, writer, and editor unless that agent has its own model or reasoning setting",
		ModelAutoSwitch: apiModelAutoSwitchFromConfig(autoSwitch),
	}
}

func (s *ProjectSession) SwitchModel(role, provider, model string) (apiModelConfig, error) {
	if err := s.host.SwitchModel(normalizeModelRole(role), provider, model); err != nil {
		return apiModelConfig{}, err
	}
	s.AppendSnapshot()
	return s.ModelConfig(), nil
}

func (s *ProjectSession) ClearModelRoute(role string) (apiModelConfig, error) {
	if err := s.host.ClearModelRoute(normalizeModelRole(role)); err != nil {
		return apiModelConfig{}, err
	}
	s.AppendSnapshot()
	return s.ModelConfig(), nil
}

func (s *ProjectSession) SetRoleThinking(role, level string) (apiModelConfig, error) {
	if err := s.host.SetRoleThinking(normalizeModelRole(role), level); err != nil {
		return apiModelConfig{}, err
	}
	s.AppendSnapshot()
	return s.ModelConfig(), nil
}

func (s *ProjectSession) SetCoCreateTimeoutSeconds(seconds int) (apiModelConfig, error) {
	if err := s.host.SetCoCreateTimeoutSeconds(seconds); err != nil {
		return apiModelConfig{}, err
	}
	s.AppendSnapshot()
	return s.ModelConfig(), nil
}

func (s *ProjectSession) SetCoCreateMaxTokens(tokens int) (apiModelConfig, error) {
	if err := s.host.SetCoCreateMaxTokens(tokens); err != nil {
		return apiModelConfig{}, err
	}
	s.AppendSnapshot()
	return s.ModelConfig(), nil
}

func (s *ProjectSession) SetRetrySettings(modelCallMaxAttempts, structureRepairMaxAttempts, budgetQualityMaxAttempts, adaptationOutlineAuditRetryMaxAttempts int) (apiModelConfig, error) {
	if err := s.host.SetRetrySettings(modelCallMaxAttempts, structureRepairMaxAttempts, budgetQualityMaxAttempts, adaptationOutlineAuditRetryMaxAttempts); err != nil {
		return apiModelConfig{}, err
	}
	s.AppendSnapshot()
	return s.ModelConfig(), nil
}

func (s *ProjectSession) AddOpenAICompatibleModel(role, provider, model, baseURL, apiKey, api string) (apiModelConfig, error) {
	pc := bootstrap.ProviderConfig{
		Type:    "openai",
		API:     strings.TrimSpace(api),
		APIKey:  strings.TrimSpace(apiKey),
		BaseURL: strings.TrimSpace(baseURL),
	}
	if pc.API == "" {
		pc.API = "chat"
	}
	return s.AddProviderModel(role, provider, model, pc)
}

func (s *ProjectSession) AddProviderModel(role, provider, model string, pc bootstrap.ProviderConfig) (apiModelConfig, error) {
	if err := s.host.AddProviderModel(normalizeModelRole(role), provider, pc, model); err != nil {
		return apiModelConfig{}, err
	}
	s.AppendSnapshot()
	return s.ModelConfig(), nil
}

func (s *ProjectSession) ConfigureProviderModel(ctx context.Context, req modelProviderRequest) (apiModelConfig, error) {
	if err := s.host.ConfigureProviderModel(ctx, req.providerModelSaveUpdate()); err != nil {
		return apiModelConfig{}, err
	}
	s.AppendSnapshot()
	return s.ModelConfig(), nil
}

func (s *ProjectSession) SyncModelSettingsFromGlobal(cfg bootstrap.Config) error {
	if err := s.host.SyncModelSettingsFromGlobal(cfg); err != nil {
		return err
	}
	s.AppendSnapshot()
	return nil
}

func (s *ProjectSession) SyncInheritedProviderFromGlobal(cfg bootstrap.Config, originalProvider, provider string) error {
	if err := s.host.SyncInheritedProviderFromGlobal(cfg, originalProvider, provider); err != nil {
		return err
	}
	s.AppendSnapshot()
	return nil
}

func (s *ProjectSession) SyncInheritedProviderModelRemovalFromGlobal(cfg bootstrap.Config, provider, model string) error {
	if err := s.host.SyncInheritedProviderModelRemovalFromGlobal(cfg, provider, model); err != nil {
		return err
	}
	s.AppendSnapshot()
	return nil
}

func (s *ProjectSession) TestProviderModel(ctx context.Context, req modelProviderRequest) (host.ProviderModelTestResult, error) {
	if strings.TrimSpace(req.OriginalProvider) != "" {
		return s.host.TestConfiguredProviderModel(ctx, req.providerModelUpdate())
	}
	return s.host.TestProviderModel(ctx, normalizeModelRole(req.Role), req.Provider, req.providerConfig(), req.Model)
}

func (s *ProjectSession) DiscoverProviderModels(ctx context.Context, req modelProviderRequest) (host.ProviderModelDiscoveryResult, error) {
	if strings.TrimSpace(req.OriginalProvider) != "" {
		return s.host.DiscoverConfiguredProviderModels(ctx, req.providerModelUpdate())
	}
	return s.host.DiscoverProviderModels(ctx, req.Provider, req.providerConfig(), req.Model)
}

func (s *ProjectSession) RemoveProviderModel(provider, model string) (apiModelConfig, error) {
	if err := s.host.RemoveProviderModel(provider, model); err != nil {
		return apiModelConfig{}, err
	}
	s.AppendSnapshot()
	return s.ModelConfig(), nil
}

func (s *ProjectSession) StartGrokLogin(accountID, accountName string) (grokauth.LoginStart, error) {
	return s.host.StartGrokLogin(accountID, accountName)
}

func (s *ProjectSession) PollGrokLogin() (grokauth.LoginPoll, error) {
	return s.host.PollGrokLogin()
}

func (s *ProjectSession) CompleteGrokLogin(callbackInput string) (grokauth.AuthStatus, error) {
	return s.host.CompleteGrokLogin(callbackInput)
}

func (s *ProjectSession) GrokLoginStatus(accountID string) grokauth.AuthStatus {
	return s.host.GrokLoginStatus(accountID)
}

func (s *ProjectSession) StartQuick(text string, targetTotalWords int) error {
	unlock, err := s.beginAction()
	if err != nil {
		return err
	}
	defer unlock()

	plan, err := startup.PrepareQuick(startup.Request{
		Mode:             startup.ModeQuick,
		UserPrompt:       text,
		OutputDir:        s.manifest.OutputDir,
		Interactive:      false,
		TargetTotalWords: targetTotalWords,
	})
	if err != nil {
		return err
	}
	if err := s.host.PrepareUserRules(plan.RawPrompt); err != nil {
		return err
	}
	if err := s.persistWordBudget(plan.WordBudget); err != nil {
		return err
	}
	if err := s.host.StartPrepared(plan.StartPrompt); err != nil {
		return err
	}
	s.AppendSnapshot()
	return nil
}

func (s *ProjectSession) Pause() bool {
	canceledKind, canceledAction := s.cancelCurrentAction()
	stopped := s.host.Abort()
	if canceledAction {
		s.appendActionCanceledEvent(canceledKind)
	}
	if stopped {
		s.AppendSnapshot()
	}
	if canceledAction && !stopped {
		s.AppendSnapshot()
	}
	return stopped || canceledAction
}

func (s *ProjectSession) RollbackPreview() (domain.RollbackPreview, error) {
	if preview, ok := s.coCreateRollbackPreview(); ok {
		return preview, nil
	}
	return s.host.RollbackPreview()
}

func (s *ProjectSession) Rollback(req domain.RollbackRequest) (domain.RollbackResult, error) {
	canceledKind, canceledAction := s.cancelCurrentAction()
	if canceledAction {
		s.appendActionCanceledEvent(canceledKind)
	}
	if !s.waitForActionsIdle(rollbackActionWaitTimeout) {
		return domain.RollbackResult{}, fmt.Errorf("project action did not stop before rollback")
	}

	unlock, err := s.beginAction()
	if err != nil {
		return domain.RollbackResult{}, err
	}
	defer unlock()

	if preview, ok := s.coCreateRollbackPreview(); ok {
		if !req.Confirm {
			return domain.RollbackResult{Preview: preview}, fmt.Errorf("rollback confirmation is required")
		}
		if strings.TrimSpace(req.PreviewHash) != "" && req.PreviewHash != preview.PreviewHash {
			return domain.RollbackResult{Preview: preview}, fmt.Errorf("rollback preview expired; refresh and confirm again")
		}
		s.cocreate = nil
		s.clearCoCreateCheckpoint()

		storePreview, err := s.host.RollbackPreview()
		if err == nil && storePreview.CanRollback {
			result, err := s.host.Rollback(domain.RollbackRequest{Confirm: true})
			s.AppendSnapshot()
			return result, err
		}
		result := domain.RollbackResult{Preview: preview}
		s.AppendSnapshot()
		return result, nil
	}

	result, err := s.host.Rollback(req)
	if err == nil && result.Preview.TargetStage == domain.RollbackStageDraft {
		expectedKind := rollbackCoCreateKind(result.Preview.Mode)
		if restoreErr := s.restoreCoCreateCheckpointKind(expectedKind); restoreErr != nil {
			slog.Warn("restore co-create state after rollback failed", "module", "web", "project", s.projectID(), "kind", expectedKind, "err", restoreErr)
		}
	}
	s.AppendSnapshot()
	return result, err
}

func (s *ProjectSession) coCreateRollbackPreview() (domain.RollbackPreview, bool) {
	if s == nil || s.cocreate == nil {
		return domain.RollbackPreview{}, false
	}
	kind := strings.TrimSpace(s.cocreate.kind)
	draft := s.cocreate.draftPrompt()
	preview := domain.RollbackPreview{
		CanRollback:   true,
		Mode:          kind,
		CurrentStage:  "draft",
		TargetStage:   domain.RollbackStageBlank,
		TargetLabel:   "空白项目",
		Warning:       "回退会清除当前共创 draft，此操作不可撤销。",
		DeletePaths:   []string{webCoCreateCheckpointRelPath, "当前共创 draft"},
		PreservePaths: []string{"project manifest", "style/config", "uploads/"},
		StateSignature: fmt.Sprintf("web-cocreate:%s:%d:%t",
			kind,
			len(draft),
			s.cocreate.failed,
		),
	}
	return domain.RollbackPreviewWithHash(preview), true
}

func (s *ProjectSession) waitForActionsIdle(timeout time.Duration) bool {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		if !s.hasActiveAction() {
			return true
		}
		select {
		case <-deadline.C:
			return !s.hasActiveAction()
		case <-ticker.C:
		}
	}
}

func (s *ProjectSession) hasActiveAction() bool {
	s.actionMu.Lock()
	defer s.actionMu.Unlock()
	return len(s.actionKinds) > 0
}

func (s *ProjectSession) Resume() (string, error) {
	s.mu.Lock()
	outputDir := s.manifest.OutputDir
	cocreate := s.cocreate
	s.mu.Unlock()
	if strings.TrimSpace(outputDir) != "" {
		st := storepkg.NewStore(outputDir)
		active, err := st.Revisions.Active()
		if err != nil {
			return "", fmt.Errorf("read active revision before resume: %w", err)
		}
		if active != nil {
			return "", fmt.Errorf("%w: %s", storepkg.ErrActiveRevisionBlocksNormalFlow, active.ID)
		}
		if required, err := host.CharacterConfirmationRequired(st); err != nil {
			return "", fmt.Errorf("read Character confirmation state before resume: %w", err)
		} else if required {
			return "角色卡已审核通过，请确认后继续生成完整设定", nil
		}
		if !cocreate.coreCastResumeExempt() {
			if err := host.RequireResumeCoreCastGate(st, true); err != nil {
				return "", err
			}
		}
	}
	if label, resumed, err := s.resumePendingWebAction(context.Background()); resumed || err != nil {
		return label, err
	}
	decision, err := s.AutoResumeDecision()
	if err != nil {
		return "", err
	}
	switch decision.Disposition {
	case AutoResumeWaitUser, AutoResumeBlocked, AutoResumeBusy:
		return decision.Label, nil
	}
	switch decision.Action {
	case AutoResumeActionCoCreate:
		_, err := s.ResumeCoCreate(context.Background())
		return decision.Label, err
	case AutoResumeActionContinuationPlanning:
		_, err := s.RetryContinuation(context.Background(), decision.WorkflowRevision)
		return decision.Label, err
	}

	var unlock func()
	if decision.ReasonCode == "legacy_completion_revalidation" {
		unlock, err = s.beginActionWithoutNormalFlowLease("legacy_completion_revalidation")
	} else {
		unlock, err = s.beginAction()
	}
	if err != nil {
		return "", err
	}
	defer unlock()

	label, err := s.host.Resume()
	if err == nil {
		s.AppendSnapshot()
	}
	return label, err
}

func (s *ProjectSession) ResumeFoundationRevision() (string, error) {
	runner, ok := s.host.(foundationRevisionRouteHost)
	if !ok {
		return "", fmt.Errorf("Foundation revision route is unavailable")
	}
	label, err := runner.ResumeFoundationRevision()
	if err == nil {
		s.AppendSnapshot()
	}
	return label, err
}

func (s *ProjectSession) ResumeAdaptationFoundationRevision() (string, error) {
	runner, ok := s.host.(foundationAdaptationRevisionHost)
	if !ok {
		return "", fmt.Errorf("adaptation Foundation revision route is unavailable")
	}
	st := storepkg.NewStore(s.manifest.OutputDir)
	runtime, err := st.FoundationRevisions.LoadRuntime()
	if err != nil || runtime == nil || runtime.ProjectMode != "adaptation" || runtime.Stage != "regenerating" {
		return "", errors.Join(fmt.Errorf("regenerating adaptation Foundation revision is required"), err)
	}
	review, err := st.Adaptation.LoadTargetFoundationReview()
	if err != nil || review == nil {
		return "", errors.Join(fmt.Errorf("adaptation target Foundation review is required"), err)
	}
	manifest, err := st.Adaptation.LoadSourceManifest()
	if err != nil || manifest == nil {
		return "", errors.Join(fmt.Errorf("adaptation source manifest is required"), err)
	}
	intent, err := st.Adaptation.LoadCoCreateIntent()
	if err != nil || intent == nil {
		return "", errors.Join(fmt.Errorf("adaptation intent is required"), err)
	}
	options := adapt.ProposalOptions{Brief: review.Brief, SourcePath: manifest.SourcePath, Granularity: intent.Granularity, RewritePolicy: intent.RewritePolicy, WordTolerance: intent.WordTolerance}
	var result *adapt.ProposalStageResult
	err = st.WithFoundationAdaptationRevisionCommand(runtime.SessionID, "regenerate-proposal", func() error {
		workflow, loadErr := st.Adaptation.LoadPlanningWorkflow()
		if loadErr != nil || workflow == nil {
			return errors.Join(fmt.Errorf("adaptation workflow is required"), loadErr)
		}
		if workflow.Stage != domain.AdaptationPlanningStageSkeletonGenerating {
			if _, loadErr = st.Adaptation.SetPlanningWorkflowStage(domain.AdaptationPlanningStageSkeletonGenerating, workflow.Revision); loadErr != nil {
				return loadErr
			}
		}
		result, loadErr = runner.BuildAdaptationProposalVolumesForFoundationRevision(context.Background(), options)
		if loadErr != nil {
			return loadErr
		}
		next := domain.AdaptationPlanningStageProposalReviewPending
		if result != nil && result.VolumeReview != nil {
			next = domain.AdaptationPlanningStageVolumeReviewPending
		}
		_, loadErr = st.Adaptation.SetPlanningWorkflowStage(next, -1)
		return loadErr
	})
	if err != nil {
		return "", err
	}
	if err := host.NewFoundationRevisionService(st).MarkAdaptationRegenerationReady(); err != nil {
		return "", err
	}
	s.AppendSnapshot()
	if result != nil && result.VolumeReview != nil {
		return "adaptation Foundation revised; volume proposal awaits existing review", nil
	}
	return "adaptation Foundation revised; detailed proposal awaits existing review", nil
}

func (s *ProjectSession) resumePendingWebAction(ctx context.Context) (string, bool, error) {
	action, err := s.pendingWebResumeAction()
	if err != nil || action == nil {
		return "", false, err
	}

	switch action.Kind {
	case webResumeActionAdaptationAnalysis:
		return action.Label, true, s.StartPrepareAdaptationSource(action.SourcePath)
	case webResumeActionAdaptationProposal:
		_, err := s.BuildAdaptationProposalVolumesContext(ctx, action.ProposalOptions)
		return action.Label, true, err
	case webResumeActionAdaptationProposalDetails:
		_, err := s.BuildAdaptationProposalDetailsContext(ctx)
		return action.Label, true, err
	default:
		return "", false, nil
	}
}

func (s *ProjectSession) pendingWebResumeAction() (*webResumeAction, error) {
	s.mu.Lock()
	manifest := s.manifest
	s.mu.Unlock()

	if strings.TrimSpace(manifest.OutputDir) == "" {
		return nil, nil
	}
	st := storepkg.NewStore(manifest.OutputDir)
	if action, err := pendingAdaptationProposalResumeAction(st); err != nil || action != nil {
		return action, err
	}
	hasPlanningState, err := hasDurableAdaptationPlanningState(st)
	if err != nil {
		return nil, err
	}
	if hasPlanningState {
		return nil, nil
	}
	return pendingAdaptationAnalysisResumeAction(manifest)
}

func hasDurableAdaptationPlanningState(st *storepkg.Store) (bool, error) {
	plan, err := st.Adaptation.LoadPlan()
	if err != nil {
		return false, fmt.Errorf("load adaptation plan: %w", err)
	}
	if plan != nil {
		return true, nil
	}
	proposal, err := st.Adaptation.LoadProposal()
	if err != nil {
		return false, fmt.Errorf("load adaptation proposal: %w", err)
	}
	if proposal != nil {
		return true, nil
	}
	review, err := st.Adaptation.LoadVolumeReview()
	if err != nil {
		return false, fmt.Errorf("load adaptation volume review: %w", err)
	}
	if review != nil {
		return true, nil
	}
	runtime, err := st.Adaptation.LoadProposalRuntime()
	if err != nil {
		return false, fmt.Errorf("load adaptation proposal runtime: %w", err)
	}
	return runtime != nil, nil
}

func pendingAdaptationProposalResumeAction(st *storepkg.Store) (*webResumeAction, error) {
	workflow, err := st.Adaptation.LoadPlanningWorkflow()
	if err != nil {
		return nil, fmt.Errorf("load adaptation planning workflow: %w", err)
	}
	plan, err := st.Adaptation.LoadPlan()
	if err != nil {
		return nil, fmt.Errorf("load adaptation plan: %w", err)
	}
	if plan != nil && len(plan.Chapters) > 0 {
		return nil, nil
	}
	proposal, err := st.Adaptation.LoadProposal()
	if err != nil {
		return nil, fmt.Errorf("load adaptation proposal: %w", err)
	}
	if proposal != nil && len(proposal.Chapters) > 0 {
		return nil, nil
	}
	review, err := st.Adaptation.LoadVolumeReview()
	if err != nil {
		return nil, fmt.Errorf("load adaptation volume review: %w", err)
	}
	if review != nil && len(review.Volumes) > 0 {
		if workflow == nil || workflow.Stage != domain.AdaptationPlanningStageDetailsGenerating {
			return nil, nil
		}
		return &webResumeAction{
			Kind:  webResumeActionAdaptationProposalDetails,
			Label: "恢复：生成章节细纲",
		}, nil
	}
	runtime, err := st.Adaptation.LoadProposalRuntime()
	if err != nil {
		return nil, fmt.Errorf("load adaptation proposal runtime: %w", err)
	}
	if runtime == nil {
		if workflow != nil && workflow.Stage == domain.AdaptationPlanningStageSkeletonGenerating {
			review, reviewErr := st.Adaptation.LoadTargetFoundationReview()
			intent, intentErr := st.Adaptation.LoadCoCreateIntent()
			manifest, manifestErr := st.Adaptation.LoadSourceManifest()
			if reviewErr != nil || intentErr != nil || manifestErr != nil {
				return nil, errors.Join(reviewErr, intentErr, manifestErr)
			}
			if review != nil && review.State == domain.AdaptationFoundationReviewApproved && intent != nil && manifest != nil {
				return &webResumeAction{Kind: webResumeActionAdaptationProposal, Label: "恢复：生成改编提案", ProposalOptions: adapt.ProposalOptions{
					Brief: review.Brief, SourcePath: manifest.SourcePath, Granularity: intent.Granularity,
					RewritePolicy: intent.RewritePolicy, WordTolerance: intent.WordTolerance,
				}}, nil
			}
		}
		return nil, nil
	}
	if workflow != nil && workflow.Stage != domain.AdaptationPlanningStageSkeletonGenerating {
		return nil, nil
	}
	options, err := proposalOptionsFromRuntime(runtime)
	if err != nil {
		return nil, err
	}
	return &webResumeAction{
		Kind:            webResumeActionAdaptationProposal,
		Label:           "恢复：生成改编提案",
		ProposalOptions: options,
	}, nil
}

func pendingAdaptationAnalysisResumeAction(manifest ProjectManifest) (*webResumeAction, error) {
	status, err := projectAdaptationStatus(manifest, false)
	if err != nil {
		return nil, err
	}
	if status.AnalysisStatus != "paused" || status.SourceFile == nil {
		return nil, nil
	}
	sourcePath, err := adaptationSourcePathFromName(status.SourceFile.RelativePath, manifest, false)
	if err != nil {
		return nil, err
	}
	return &webResumeAction{
		Kind:       webResumeActionAdaptationAnalysis,
		Label:      "恢复：原文分析",
		SourcePath: sourcePath,
	}, nil
}

func proposalOptionsFromRuntime(runtime *domain.AdaptationProposalRuntime) (adapt.ProposalOptions, error) {
	if runtime == nil {
		return adapt.ProposalOptions{}, fmt.Errorf("adaptation proposal runtime is missing")
	}
	granularity, ok := domain.StrictAdaptationGranularity(runtime.Granularity)
	if !ok {
		return adapt.ProposalOptions{}, fmt.Errorf("adaptation proposal runtime has invalid granularity %q", runtime.Granularity)
	}
	brief := strings.TrimSpace(runtime.Brief)
	if brief == "" {
		return adapt.ProposalOptions{}, fmt.Errorf("adaptation proposal runtime brief is empty")
	}
	rewritePolicy := strings.TrimSpace(runtime.RewritePolicy)
	if rewritePolicy == "" {
		rewritePolicy = domain.AdaptationRewritePolicyForGranularity(granularity)
	}
	return adapt.ProposalOptions{
		Brief:              brief,
		SourcePath:         strings.TrimSpace(runtime.SourcePath),
		Granularity:        granularity,
		RewritePolicy:      rewritePolicy,
		WordTolerance:      runtime.WordTolerance,
		TargetChapterCount: runtime.TargetChapterCount,
	}, nil
}

func (s *ProjectSession) ReviseChapter(req host.ChapterRevisionRequest) (host.ChapterRevisionResult, error) {
	unlock, err := s.beginAction()
	if err != nil {
		return host.ChapterRevisionResult{}, err
	}
	defer unlock()

	result, err := s.host.ReviseChapter(req)
	s.AppendSnapshot()
	return result, err
}

func (s *ProjectSession) ReviseChapterOutline(ctx context.Context, req host.ChapterOutlineRevisionRequest) (host.ChapterOutlineRevisionResult, error) {
	unlock, err := s.beginAction()
	if err != nil {
		return host.ChapterOutlineRevisionResult{}, err
	}
	defer unlock()

	result, err := s.host.ReviseChapterOutline(ctx, req)
	s.AppendSnapshot()
	return result, err
}

func (s *ProjectSession) ImportExternalNovel(ctx context.Context, sourcePath string, resumeFrom int) ([]apiImportEvent, string, error) {
	unlock, err := s.beginActionKind(projectActionKindContinuation)
	if err != nil {
		return nil, "", err
	}
	defer unlock()
	return s.importExternalNovelOwned(ctx, sourcePath, resumeFrom)
}

func (s *ProjectSession) importExternalNovelOwned(ctx context.Context, sourcePath string, resumeFrom int) ([]apiImportEvent, string, error) {
	events, err := s.host.ImportFrom(ctx, imp.Options{
		SourcePath: sourcePath,
		ResumeFrom: resumeFrom,
	})
	if err != nil {
		return nil, "", err
	}
	if events == nil {
		return nil, "", fmt.Errorf("import event stream is nil")
	}
	apiEvents, err := s.consumeImportEvents(ctx, events)
	if err != nil {
		s.AppendSnapshot()
		return apiEvents, "", err
	}
	s.AppendSnapshot()
	return apiEvents, "", nil
}

func (s *ProjectSession) ContinuationSnapshot() (*domain.ContinuationSnapshot, error) {
	return s.host.ContinuationSnapshot()
}

func (s *ProjectSession) runContinuationAction(
	ctx context.Context,
	action func(context.Context) (*domain.ContinuationSnapshot, error),
) (*domain.ContinuationSnapshot, error) {
	actionCtx, unlock, err := s.beginCancellableAction(ctx, projectActionKindContinuation)
	if err != nil {
		return nil, err
	}
	defer unlock()

	snapshot, err := action(actionCtx)
	s.AppendSnapshot()
	return snapshot, err
}

func (s *ProjectSession) GenerateContinuationProposal(ctx context.Context, expectedRevision int) (*domain.ContinuationSnapshot, error) {
	return s.runContinuationAction(ctx, func(actionCtx context.Context) (*domain.ContinuationSnapshot, error) {
		return s.host.GenerateContinuationProposal(actionCtx, expectedRevision)
	})
}

func (s *ProjectSession) ReviseContinuationProposal(ctx context.Context, expectedRevision int, instruction string) (*domain.ContinuationSnapshot, error) {
	return s.runContinuationAction(ctx, func(actionCtx context.Context) (*domain.ContinuationSnapshot, error) {
		return s.host.ReviseContinuationProposal(actionCtx, instruction, expectedRevision)
	})
}

func (s *ProjectSession) ApproveContinuationProposal(ctx context.Context, expectedRevision int) (*domain.ContinuationSnapshot, error) {
	return s.runContinuationAction(ctx, func(actionCtx context.Context) (*domain.ContinuationSnapshot, error) {
		return s.host.ApproveContinuationProposal(actionCtx, expectedRevision)
	})
}

func (s *ProjectSession) ReviseContinuationVolumes(ctx context.Context, expectedRevision int, instruction string, volumeIndex int) (*domain.ContinuationSnapshot, error) {
	return s.runContinuationAction(ctx, func(actionCtx context.Context) (*domain.ContinuationSnapshot, error) {
		return s.host.ReviseContinuationVolumes(actionCtx, instruction, volumeIndex, expectedRevision)
	})
}

func (s *ProjectSession) ApproveContinuationVolumes(ctx context.Context, expectedRevision int) (*domain.ContinuationSnapshot, error) {
	return s.runContinuationAction(ctx, func(actionCtx context.Context) (*domain.ContinuationSnapshot, error) {
		return s.host.ApproveContinuationVolumes(actionCtx, expectedRevision)
	})
}

func (s *ProjectSession) GenerateContinuationOutlines(ctx context.Context, expectedRevision int) (*domain.ContinuationSnapshot, error) {
	return s.runContinuationAction(ctx, func(actionCtx context.Context) (*domain.ContinuationSnapshot, error) {
		return s.host.GenerateContinuationOutlines(actionCtx, expectedRevision)
	})
}

func (s *ProjectSession) ReviseContinuationOutlines(
	ctx context.Context,
	expectedRevision int,
	volumeIndex int,
	fromChapter int,
	toChapter int,
	instruction string,
) (*domain.ContinuationSnapshot, error) {
	return s.runContinuationAction(ctx, func(actionCtx context.Context) (*domain.ContinuationSnapshot, error) {
		return s.host.ReviseContinuationOutlines(actionCtx, continuationpkg.OutlineRevisionInput{
			Instruction: instruction,
			VolumeIndex: volumeIndex,
			ChapterFrom: fromChapter,
			ChapterTo:   toChapter,
		}, expectedRevision)
	})
}

func (s *ProjectSession) ApproveContinuationOutlines(ctx context.Context, expectedRevision int) (*domain.ContinuationSnapshot, error) {
	return s.runContinuationAction(ctx, func(actionCtx context.Context) (*domain.ContinuationSnapshot, error) {
		return s.host.ApproveContinuationOutlines(expectedRevision)
	})
}

func (s *ProjectSession) RetryContinuation(ctx context.Context, expectedRevision int) (*domain.ContinuationSnapshot, error) {
	return s.runContinuationAction(ctx, func(actionCtx context.Context) (*domain.ContinuationSnapshot, error) {
		return s.host.RetryContinuation(actionCtx, expectedRevision)
	})
}

func (s *ProjectSession) StartContinuation(expectedRevision int) (*domain.ContinuationSnapshot, string, error) {
	unlock, err := s.beginAction()
	if err != nil {
		return nil, "", err
	}
	defer unlock()

	label, snapshot, err := s.host.StartContinuation(expectedRevision)
	s.AppendSnapshot()
	return snapshot, label, err
}

func (s *ProjectSession) Continue(text string) error {
	unlock, err := s.beginAction()
	if err != nil {
		return err
	}
	defer unlock()

	if err := s.host.Continue(text); err != nil {
		return err
	}
	s.AppendSnapshot()
	return nil
}

func (s *ProjectSession) Steer(text string) error {
	unlock, err := s.beginAction()
	if err != nil {
		return err
	}
	defer unlock()

	if err := s.host.Steer(text); err != nil {
		return err
	}
	s.AppendSnapshot()
	return nil
}

func (s *ProjectSession) SimulateFromDir(ctx context.Context, dir string) ([]apiSimulationEvent, error) {
	unlock, err := s.beginActionKind(projectActionKindSimulationAnalysis)
	if err != nil {
		return nil, err
	}
	defer unlock()

	events, err := s.host.SimulateFromDir(ctx, dir)
	if err != nil {
		return nil, err
	}
	if events == nil {
		return nil, fmt.Errorf("simulation event stream is nil")
	}
	apiEvents, err := s.consumeSimulationEvents(ctx, events)
	s.AppendSnapshot()
	return apiEvents, err
}

func (s *ProjectSession) StartSimulateFromDir(dir string) error {
	return s.StartSimulateFromDirAction(dir, sim.ActionScan)
}

func (s *ProjectSession) StartSimulateFromDirAction(dir, action string) error {
	unlock, err := s.beginActionKind(projectActionKindSimulationAnalysis)
	if err != nil {
		return err
	}
	return s.startSimulateFromDirOwnedAction(dir, action, unlock)
}

func (s *ProjectSession) startSimulateFromDirOwned(dir string, unlock func()) error {
	return s.startSimulateFromDirOwnedAction(dir, sim.ActionScan, unlock)
}

func (s *ProjectSession) startSimulateFromDirOwnedAction(dir, action string, unlock func()) error {
	return s.startSimulateFromDirOwnedActionWithCompletion(dir, action, unlock, nil)
}

func (s *ProjectSession) startSimulateFromDirOwnedActionWithCompletion(
	dir string,
	action string,
	unlock func(),
	onComplete func([]apiSimulationEvent, error),
) error {
	s.AppendSnapshot()
	go func() {
		var completionEvents []apiSimulationEvent
		var completionErr error
		defer func() {
			unlock()
			if onComplete != nil {
				onComplete(completionEvents, completionErr)
			}
			s.AppendSnapshot()
		}()
		var events <-chan sim.Event
		var err error
		if actionHost, ok := s.host.(simulationActionHost); ok {
			events, err = actionHost.SimulateFromDirWithAction(context.Background(), dir, action)
		} else {
			events, err = s.host.SimulateFromDir(context.Background(), dir)
		}
		if err != nil {
			completionErr = err
			s.appendSimulationActionError(sim.StageError, "仿写画像分析启动失败", err)
			return
		}
		if events == nil {
			completionErr = fmt.Errorf("simulation event stream is nil")
			s.appendSimulationActionError(sim.StageError, "仿写画像分析失败", completionErr)
			return
		}
		completionEvents, completionErr = s.consumeSimulationEvents(context.Background(), events)
		if completionErr != nil {
			var runErr simulationRunError
			if !errors.As(completionErr, &runErr) {
				s.appendSimulationActionError(sim.StageError, "仿写画像分析失败", completionErr)
			}
		}
	}()
	return nil
}

func (s *ProjectSession) ImportSimulationProfile(ctx context.Context, path string) ([]apiSimulationEvent, error) {
	unlock, err := s.beginActionKind(projectActionKindSimulationImport)
	if err != nil {
		return nil, err
	}
	defer unlock()

	events, err := s.host.ImportSimulationProfile(ctx, path)
	if err != nil {
		return nil, err
	}
	if events == nil {
		return nil, fmt.Errorf("simulation import event stream is nil")
	}
	apiEvents, err := s.consumeSimulationEvents(ctx, events)
	s.AppendSnapshot()
	return apiEvents, err
}

func (s *ProjectSession) StartImportSimulationProfile(path string) error {
	unlock, err := s.beginActionKind(projectActionKindSimulationImport)
	if err != nil {
		return err
	}
	return s.startImportSimulationProfileOwned(path, unlock)
}

func (s *ProjectSession) startImportSimulationProfileOwned(path string, unlock func()) error {
	s.AppendSnapshot()
	go func() {
		defer func() {
			unlock()
			s.AppendSnapshot()
		}()
		events, err := s.host.ImportSimulationProfile(context.Background(), path)
		if err != nil {
			s.appendSimulationActionError(sim.StageError, "仿写画像导入启动失败", err)
			return
		}
		if events == nil {
			s.appendSimulationActionError(sim.StageError, "仿写画像导入失败", fmt.Errorf("simulation import event stream is nil"))
			return
		}
		if _, err := s.consumeSimulationEvents(context.Background(), events); err != nil {
			var runErr simulationRunError
			if !errors.As(err, &runErr) {
				s.appendSimulationActionError(sim.StageError, "仿写画像导入失败", err)
			}
		}
	}()
	return nil
}

func (s *ProjectSession) PrepareAdaptationSource(ctx context.Context, sourcePath string) ([]apiAdaptationEvent, error) {
	ctx, unlock, err := s.beginCancellableAction(ctx, projectActionKindAdaptationAnalysis)
	if err != nil {
		return nil, err
	}
	defer unlock()

	events, err := s.host.PrepareAdaptationSource(ctx, sourcePath)
	if err != nil {
		return nil, err
	}
	if events == nil {
		return nil, fmt.Errorf("adaptation event stream is nil")
	}
	apiEvents, err := s.consumeAdaptationEvents(ctx, events)
	s.AppendSnapshot()
	return apiEvents, err
}

func (s *ProjectSession) StartPrepareAdaptationSource(sourcePath string) error {
	return s.StartPrepareAdaptationSourceWithCompletion(sourcePath, nil)
}

func (s *ProjectSession) StartPrepareAdaptationSourceWithCompletion(sourcePath string, onSuccess func() error) error {
	ctx, unlock, err := s.beginCancellableAction(context.Background(), projectActionKindAdaptationAnalysis)
	if err != nil {
		return err
	}
	s.AppendSnapshot()
	go func() {
		defer func() {
			unlock()
			s.AppendSnapshot()
		}()
		events, err := s.host.PrepareAdaptationSource(ctx, sourcePath)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				s.appendAdaptationPausedEvent()
				return
			}
			s.appendAdaptationActionError(adapt.StageError, "原文分析启动失败", err)
			return
		}
		if events == nil {
			s.appendAdaptationActionError(adapt.StageError, "原文分析失败", fmt.Errorf("adaptation event stream is nil"))
			return
		}
		analysisOK := true
		if _, err := s.consumeAdaptationEvents(ctx, events); err != nil {
			analysisOK = false
			var runErr adaptationRunError
			var pausedErr adaptationPausedError
			if !errors.As(err, &runErr) && !errors.As(err, &pausedErr) && !errors.Is(err, context.Canceled) {
				s.appendAdaptationActionError(adapt.StageError, "原文分析失败", err)
			}
		}
		if analysisOK && onSuccess != nil {
			if err := onSuccess(); err != nil {
				s.appendLibraryEvent("novel_sync_error", "小说库同步失败", err.Error(), "error")
			}
		}
	}()
	return nil
}

func (s *ProjectSession) StartAdaptationPrepared(options adapt.ProposalOptions) error {
	unlock, err := s.beginAction()
	if err != nil {
		return err
	}
	defer unlock()
	if err := s.host.PrepareExternalSourceUserRules(options.Brief); err != nil {
		return err
	}
	if err := s.host.StartAdaptationPreparedWithOptions(options); err != nil {
		return err
	}
	s.AppendSnapshot()
	return nil
}

func (s *ProjectSession) BuildAdaptationProposal(options adapt.ProposalOptions) (*domain.AdaptationPlan, error) {
	return s.BuildAdaptationProposalContext(context.Background(), options)
}

func (s *ProjectSession) BuildAdaptationProposalContext(ctx context.Context, options adapt.ProposalOptions) (*domain.AdaptationPlan, error) {
	actionCtx, unlock, err := s.beginCancellableAction(ctx, projectActionKindAdaptationProposal)
	if err != nil {
		return nil, err
	}
	finished := false
	defer func() {
		if !finished {
			unlock()
			s.AppendSnapshot()
		}
	}()
	s.AppendSnapshot()
	proposal, err := s.buildAdaptationProposal(actionCtx, options)
	unlock()
	finished = true
	s.AppendSnapshot()
	if err != nil {
		return nil, err
	}
	return proposal, nil
}

func (s *ProjectSession) BuildAdaptationProposalVolumesContext(ctx context.Context, options adapt.ProposalOptions) (*adapt.ProposalStageResult, error) {
	actionCtx, unlock, err := s.beginCancellableAction(ctx, projectActionKindAdaptationProposal)
	if err != nil {
		return nil, err
	}
	defer func() {
		s.AppendSnapshot()
		unlock()
	}()
	st := storepkg.NewStore(s.manifest.OutputDir)
	if _, err := st.Adaptation.SetPlanningWorkflowStage(domain.AdaptationPlanningStageSkeletonGenerating, -1); err != nil {
		return nil, fmt.Errorf("begin adaptation skeleton workflow: %w", err)
	}
	s.AppendSnapshot()
	result, err := s.buildAdaptationProposalVolumes(actionCtx, options)
	if err != nil {
		return nil, err
	}
	nextStage := domain.AdaptationPlanningStageProposalReviewPending
	if result != nil && result.VolumeReview != nil {
		nextStage = domain.AdaptationPlanningStageVolumeReviewPending
	}
	if _, err := st.Adaptation.SetPlanningWorkflowStage(nextStage, -1); err != nil {
		return nil, fmt.Errorf("finish adaptation skeleton workflow: %w", err)
	}
	return result, nil
}

func (s *ProjectSession) buildAdaptationProposal(ctx context.Context, options adapt.ProposalOptions) (*domain.AdaptationPlan, error) {
	eventID, startedAt := s.appendAdaptationProposalStarted(options)
	if err := s.host.PrepareExternalSourceUserRules(options.Brief); err != nil {
		s.appendAdaptationProposalFinished(eventID, startedAt, options, err)
		return nil, err
	}
	s.appendAdaptationProposalPlannerRequested(options)
	options.EmitProgress = s.adaptationProposalProgressEmitter()
	proposal, err := s.host.BuildAdaptationProposalContext(ctx, options)
	if err != nil {
		err = adaptationProposalRunError(err)
		s.appendAdaptationProposalFinished(eventID, startedAt, options, err)
		return nil, err
	}
	s.appendAdaptationProposalFinished(eventID, startedAt, options, nil)
	return proposal, nil
}

func (s *ProjectSession) buildAdaptationProposalVolumes(ctx context.Context, options adapt.ProposalOptions) (*adapt.ProposalStageResult, error) {
	eventID, startedAt := s.appendAdaptationProposalStarted(options)
	if err := s.host.PrepareExternalSourceUserRules(options.Brief); err != nil {
		s.appendAdaptationProposalFinished(eventID, startedAt, options, err)
		return nil, err
	}
	s.appendAdaptationProposalPlannerRequested(options)
	options.EmitProgress = s.adaptationProposalProgressEmitter()
	result, err := s.host.BuildAdaptationProposalVolumesContext(ctx, options)
	if err != nil {
		err = adaptationProposalRunError(err)
		s.appendAdaptationProposalFinished(eventID, startedAt, options, err)
		return nil, err
	}
	s.appendAdaptationProposalFinished(eventID, startedAt, options, nil)
	return result, nil
}

func adaptationProposalRunError(err error) error {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return fmt.Errorf("改编提案生成遇到上游或单次请求超时，已保留已完成的规划断点，可直接重试继续: %w", err)
	case errors.Is(err, context.Canceled):
		return fmt.Errorf("改编提案生成已取消: %w", err)
	default:
		return err
	}
}

func (s *ProjectSession) ReviseAdaptationProposalContext(ctx context.Context, options adapt.ProposalRevisionOptions) (*domain.AdaptationPlan, error) {
	actionCtx, unlock, err := s.beginCancellableAction(ctx, projectActionKindAdaptationRevision)
	if err != nil {
		return nil, err
	}
	finished := false
	defer func() {
		if !finished {
			unlock()
			s.AppendSnapshot()
		}
	}()
	s.AppendSnapshot()
	eventID, startedAt := s.appendAdaptationProposalRevisionStarted(options)
	options.EmitProgress = s.adaptationProposalProgressEmitter()
	proposal, err := s.host.ReviseAdaptationProposalContext(actionCtx, options)
	unlock()
	finished = true
	s.AppendSnapshot()
	if err != nil {
		err = adaptationProposalRevisionRunError(err)
		s.appendAdaptationProposalRevisionFinished(eventID, startedAt, options, err)
		return nil, err
	}
	s.appendAdaptationProposalRevisionFinished(eventID, startedAt, options, nil)
	return proposal, nil
}

func (s *ProjectSession) ReviseAdaptationVolumeReviewContext(ctx context.Context, options adapt.ProposalRevisionOptions) (*domain.AdaptationVolumeReview, error) {
	actionCtx, unlock, err := s.beginCancellableAction(ctx, projectActionKindAdaptationRevision)
	if err != nil {
		return nil, err
	}
	finished := false
	defer func() {
		if !finished {
			unlock()
			s.AppendSnapshot()
		}
	}()
	s.AppendSnapshot()
	eventID, startedAt := s.appendAdaptationProposalRevisionStarted(options)
	options.EmitProgress = s.adaptationProposalProgressEmitter()
	review, err := s.host.ReviseAdaptationVolumeReviewContext(actionCtx, options)
	unlock()
	finished = true
	s.AppendSnapshot()
	if err != nil {
		err = adaptationProposalRevisionRunError(err)
		s.appendAdaptationProposalRevisionFinished(eventID, startedAt, options, err)
		return nil, err
	}
	s.appendAdaptationProposalRevisionFinished(eventID, startedAt, options, nil)
	return review, nil
}

func (s *ProjectSession) BuildAdaptationProposalDetailsContext(ctx context.Context) (*domain.AdaptationPlan, error) {
	actionCtx, unlock, err := s.beginCancellableAction(ctx, projectActionKindAdaptationProposal)
	if err != nil {
		return nil, err
	}
	defer func() {
		s.AppendSnapshot()
		unlock()
	}()
	st := storepkg.NewStore(s.manifest.OutputDir)
	if runtime, loadErr := st.FoundationRevisions.LoadRuntime(); loadErr == nil && runtime != nil && runtime.ProjectMode == "adaptation" &&
		(runtime.Stage == "awaiting_adaptation_plan_confirmation" || runtime.Stage == "awaiting_outline_approval") {
		runner, ok := s.host.(foundationAdaptationRevisionHost)
		if !ok {
			return nil, fmt.Errorf("adaptation Foundation detail route is unavailable")
		}
		var proposal *domain.AdaptationPlan
		err := st.WithFoundationAdaptationRevisionCommand(runtime.SessionID, "regenerate-details", func() error {
			workflow, commandErr := st.Adaptation.LoadPlanningWorkflow()
			if commandErr != nil || workflow == nil || (workflow.Stage != domain.AdaptationPlanningStageVolumeReviewPending && workflow.Stage != domain.AdaptationPlanningStageDetailsGenerating) {
				return errors.Join(fmt.Errorf("adaptation volume review must be approved before generating chapter details"), commandErr)
			}
			if workflow.Stage == domain.AdaptationPlanningStageVolumeReviewPending {
				if _, commandErr = st.Adaptation.SetPlanningWorkflowStage(domain.AdaptationPlanningStageDetailsGenerating, workflow.Revision); commandErr != nil {
					return commandErr
				}
			}
			proposal, commandErr = runner.BuildAdaptationProposalDetailsForFoundationRevision(actionCtx)
			if commandErr != nil {
				return commandErr
			}
			_, commandErr = st.Adaptation.SetPlanningWorkflowStage(domain.AdaptationPlanningStageProposalReviewPending, -1)
			return commandErr
		})
		return proposal, err
	}
	workflow, err := st.Adaptation.LoadPlanningWorkflow()
	if err != nil {
		return nil, fmt.Errorf("load adaptation planning workflow: %w", err)
	}
	if workflow == nil || (workflow.Stage != domain.AdaptationPlanningStageVolumeReviewPending && workflow.Stage != domain.AdaptationPlanningStageDetailsGenerating) {
		return nil, fmt.Errorf("adaptation volume review must be approved before generating chapter details")
	}
	if workflow.Stage == domain.AdaptationPlanningStageVolumeReviewPending {
		expectedRevision := workflow.Revision
		if workflow.UpdatedAt == "" { // conservatively inferred legacy workflow
			expectedRevision = -1
		}
		if _, err := st.Adaptation.SetPlanningWorkflowStage(domain.AdaptationPlanningStageDetailsGenerating, expectedRevision); err != nil {
			return nil, fmt.Errorf("approve adaptation volume review: %w", err)
		}
	}
	s.AppendSnapshot()
	eventID, startedAt := s.appendAdaptationProposalStarted(adapt.ProposalOptions{})
	proposal, err := s.host.BuildAdaptationProposalDetailsContext(actionCtx, adapt.ProposalDetailsOptions{
		EmitProgress: s.adaptationProposalProgressEmitter(),
	})
	if err != nil {
		err = adaptationProposalRunError(err)
		s.appendAdaptationProposalFinished(eventID, startedAt, adapt.ProposalOptions{}, err)
		return nil, err
	}
	if _, err := st.Adaptation.SetPlanningWorkflowStage(domain.AdaptationPlanningStageProposalReviewPending, -1); err != nil {
		return nil, fmt.Errorf("finish adaptation details workflow: %w", err)
	}
	s.appendAdaptationProposalFinished(eventID, startedAt, adapt.ProposalOptions{}, nil)
	return proposal, nil
}

func adaptationProposalRevisionRunError(err error) error {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return fmt.Errorf("改编提案修订遇到上游或单次请求超时，已保留已完成的修改进度，可直接重试继续: %w", err)
	case errors.Is(err, context.Canceled):
		return fmt.Errorf("改编提案修订已取消: %w", err)
	default:
		return err
	}
}

func (s *ProjectSession) ConfirmAdaptationProposal() (*domain.AdaptationPlan, error) {
	unlock, err := s.beginAction()
	if err != nil {
		return nil, err
	}
	defer unlock()
	st := storepkg.NewStore(s.manifest.OutputDir)
	if runtime, loadErr := st.FoundationRevisions.LoadRuntime(); loadErr == nil && runtime != nil && runtime.ProjectMode == "adaptation" &&
		(runtime.Stage == "awaiting_adaptation_plan_confirmation" || runtime.Stage == "awaiting_outline_approval") {
		runner, ok := s.host.(foundationAdaptationRevisionHost)
		if !ok {
			return nil, fmt.Errorf("adaptation Foundation confirmation route is unavailable")
		}
		var plan *domain.AdaptationPlan
		err := st.WithFoundationAdaptationRevisionCommand(runtime.SessionID, "confirm-proposal", func() error {
			var confirmErr error
			plan, confirmErr = runner.ConfirmAdaptationProposalForFoundationRevision()
			return confirmErr
		})
		if err != nil {
			return nil, err
		}
		if err := host.NewFoundationRevisionService(st).CompleteAdaptationReview(); err != nil {
			return nil, err
		}
		s.cocreate = nil
		s.clearCoCreateCheckpoint()
		s.AppendSnapshot()
		return plan, nil
	}

	plan, err := s.host.ConfirmAdaptationProposal()
	if err != nil {
		return nil, err
	}
	if _, err := st.Adaptation.SetPlanningWorkflowStage(domain.AdaptationPlanningStageConfirmed, -1); err != nil {
		return nil, fmt.Errorf("confirm adaptation planning workflow: %w", err)
	}
	s.cocreate = nil
	s.clearCoCreateCheckpoint()
	s.AppendSnapshot()
	return plan, nil
}

func (s *ProjectSession) Export(ctx context.Context, opts exp.Options) (*exp.Result, error) {
	result, err := s.host.Export(ctx, opts)
	if err == nil {
		s.AppendSnapshot()
	}
	return result, err
}

func (s *ProjectSession) BeginCoCreate(ctx context.Context, req webCoCreateBeginRequest) (webCoCreateState, error) {
	if strings.TrimSpace(req.Kind) == webCoCreateKindContinuation {
		return s.BeginContinuationCoCreate(ctx, req)
	}
	unlock, err := s.beginAction()
	if err != nil {
		return webCoCreateState{}, err
	}
	defer unlock()

	if state, err := s.resetRestartableCoCreateLocked(); err != nil {
		return state, err
	}
	state, err := newWebCoCreateSession(req)
	if err != nil {
		return webCoCreateState{}, err
	}
	if err := s.seedCoreCastDraftRevision(state); err != nil {
		return webCoCreateState{}, err
	}
	if isPausedCoCreateKind(state.kind) {
		if !s.host.PauseForCoCreate() {
			return webCoCreateState{}, fmt.Errorf("cannot enter stage co-create")
		}
		s.AppendSnapshot()
	}
	s.cocreate = state
	if err := s.refreshCoreCastLocked(state, true); err != nil {
		return state.apiState(), err
	}
	s.saveCoCreateCheckpoint()
	if s.cocreate.hasPendingBriefingDecisions() {
		api := s.cocreate.apiState()
		s.appendCoCreateState(api)
		return api, nil
	}
	return s.runCoCreateLocked(ctx)
}

func (s *ProjectSession) BeginContinuationCoCreate(ctx context.Context, req webCoCreateBeginRequest) (webCoCreateState, error) {
	unlock, err := s.beginAction()
	if err != nil {
		return webCoCreateState{}, err
	}
	defer unlock()

	if state, err := s.resetRestartableCoCreateLocked(); err != nil {
		return state, err
	}
	snapshot, err := s.host.ContinuationSnapshot()
	if err != nil {
		return webCoCreateState{}, err
	}
	if snapshot == nil {
		return webCoCreateState{}, fmt.Errorf("continuation source has not been imported")
	}
	if _, err := s.host.BeginContinuationDraft(snapshot.Workflow.Revision); err != nil {
		return webCoCreateState{}, err
	}
	req.Kind = webCoCreateKindContinuation
	req.ExpectedRevision = snapshot.Workflow.Revision
	state, err := newWebCoCreateSession(req)
	if err != nil {
		return webCoCreateState{}, err
	}
	if err := s.seedCoreCastDraftRevision(state); err != nil {
		return webCoCreateState{}, err
	}
	if !s.host.PauseForCoCreate() {
		return webCoCreateState{}, fmt.Errorf("cannot enter continuation co-create")
	}
	s.cocreate = state
	s.saveCoCreateCheckpoint()
	s.AppendSnapshot()
	return s.runCoCreateLocked(ctx)
}

func (s *ProjectSession) BeginAdaptCoCreate(ctx context.Context, req webCoCreateBeginRequest) (webCoCreateState, error) {
	unlock, err := s.beginAction()
	if err != nil {
		return webCoCreateState{}, err
	}
	defer unlock()

	req.Kind = webCoCreateKindAdapt
	if state, err := s.resetRestartableCoCreateLocked(); err != nil {
		return state, err
	}
	state, err := newWebCoCreateSession(req)
	if err != nil {
		return webCoCreateState{}, err
	}
	if err := s.seedCoreCastDraftRevision(state); err != nil {
		return webCoCreateState{}, err
	}
	s.cocreate = state
	if err := s.refreshCoreCastLocked(state, true); err != nil {
		return state.apiState(), err
	}
	s.saveRecoverableCoCreateCheckpoint()
	if err := s.ensureAdaptCoCreateBriefingLocked(ctx, false); err != nil {
		return s.cocreate.apiState(), err
	}
	if s.cocreate.hasPendingBriefingDecisions() {
		api := s.cocreate.apiState()
		s.appendCoCreateState(api)
		return api, nil
	}
	return s.runCoCreateLocked(ctx)
}

func (s *ProjectSession) resetRestartableCoCreateLocked() (webCoCreateState, error) {
	if s.cocreate == nil {
		return webCoCreateState{}, nil
	}
	if !s.cocreate.failed {
		return s.cocreate.apiState(), fmt.Errorf("co-create already started")
	}
	if isPausedCoCreateKind(s.cocreate.kind) {
		s.host.CancelCoCreate()
		s.AppendSnapshot()
	}
	s.cocreate = nil
	return webCoCreateState{}, nil
}

func (s *ProjectSession) ensureAdaptCoCreateBriefingLocked(ctx context.Context, force bool) error {
	if s.cocreate == nil || !s.cocreate.needsAdaptBriefingRefresh(force) {
		return nil
	}
	sourcePath := strings.TrimSpace(s.cocreate.sourcePath)
	if sourcePath == "" {
		return fmt.Errorf("adaptation source path is required")
	}
	eventID, startedAt := s.appendCoCreateBriefingStarted()
	briefing, err := s.host.EnsureAdaptationCoCreateBriefing(ctx, sourcePath, s.cocreate.adaptBriefingIntent())
	if err != nil {
		s.cocreate.failed = true
		s.saveCoCreateCheckpoint()
		s.appendCoCreateState(s.cocreate.apiState())
		runErr := fmt.Errorf("prepare adaptation co-create briefing: %w", err)
		s.appendCoCreateBriefingFinished(eventID, startedAt, runErr)
		return runErr
	}
	s.cocreate.adaptationBriefing = briefing
	s.cocreate.failed = false
	s.saveCoCreateCheckpoint()
	s.appendCoCreateBriefingFinished(eventID, startedAt, nil)
	return nil
}

func (s *ProjectSession) SendCoCreate(ctx context.Context, req webCoCreateSendRequest) (webCoCreateState, error) {
	unlock, err := s.beginAction()
	if err != nil {
		return webCoCreateState{}, err
	}
	defer unlock()

	if s.cocreate == nil {
		return webCoCreateState{}, fmt.Errorf("co-create has not started")
	}
	if s.cocreate.hasPendingBriefingDecisions() {
		return s.cocreate.apiState(), fmt.Errorf("resolve adaptation briefing decisions before continuing co-create")
	}
	if err := s.cocreate.appendUser(req.Text, req.Source); err != nil {
		return webCoCreateState{}, err
	}
	s.saveCoCreateCheckpoint()
	if err := s.ensureAdaptCoCreateBriefingLocked(ctx, req.ForceRebrief); err != nil {
		return s.cocreate.apiState(), err
	}
	if s.cocreate.hasPendingBriefingDecisions() {
		api := s.cocreate.apiState()
		s.appendCoCreateState(api)
		return api, nil
	}
	return s.runCoCreateLocked(ctx)
}

func (s *ProjectSession) ReviseCoCreate(ctx context.Context, req webCoCreateReviseRequest) (webCoCreateState, error) {
	unlock, err := s.beginAction()
	if err != nil {
		return webCoCreateState{}, err
	}
	defer unlock()

	if s.cocreate == nil {
		return webCoCreateState{}, fmt.Errorf("co-create has not started")
	}
	if s.cocreate.hasPendingBriefingDecisions() {
		return s.cocreate.apiState(), fmt.Errorf("resolve adaptation briefing decisions before revising co-create")
	}
	if err := s.cocreate.reviseUser(req.MessageID, req.Text); err != nil {
		return webCoCreateState{}, err
	}
	s.saveCoCreateCheckpoint()
	if err := s.ensureAdaptCoCreateBriefingLocked(ctx, false); err != nil {
		return s.cocreate.apiState(), err
	}
	if s.cocreate.hasPendingBriefingDecisions() {
		api := s.cocreate.apiState()
		s.appendCoCreateState(api)
		return api, nil
	}
	return s.runCoCreateLocked(ctx)
}

func (s *ProjectSession) ResolveCoCreateDecision(ctx context.Context, req webCoCreateDecisionRequest) (webCoCreateState, error) {
	unlock, err := s.beginAction()
	if err != nil {
		return webCoCreateState{}, err
	}
	defer unlock()

	if s.cocreate == nil {
		return webCoCreateState{}, fmt.Errorf("co-create has not started")
	}
	if s.cocreate.kind != webCoCreateKindAdapt {
		return s.cocreate.apiState(), fmt.Errorf("co-create decisions are only supported for adaptation co-create")
	}
	briefing, err := s.host.ResolveAdaptationCoCreateDecisions(req.resolvedDecisionItems())
	if err != nil {
		return s.cocreate.apiState(), err
	}
	s.cocreate.adaptationBriefing = briefing
	s.saveCoCreateCheckpoint()
	if s.cocreate.hasPendingBriefingDecisions() {
		api := s.cocreate.apiState()
		s.appendCoCreateState(api)
		return api, nil
	}
	return s.runAdaptDecisionDraftBatchesLocked(ctx)
}

func (s *ProjectSession) ResumeCoCreate(ctx context.Context) (webCoCreateState, error) {
	unlock, err := s.beginAction()
	if err != nil {
		return webCoCreateState{}, err
	}
	defer unlock()

	if s.cocreate == nil {
		return webCoCreateState{}, fmt.Errorf("co-create has not started")
	}
	if err := s.ensureAdaptCoCreateBriefingLocked(ctx, false); err != nil {
		return s.cocreate.apiState(), err
	}
	if s.cocreate.hasPendingBriefingDecisions() {
		return s.cocreate.apiState(), fmt.Errorf("resolve adaptation briefing decisions before resuming co-create")
	}
	s.saveCoCreateCheckpoint()
	return s.runCoCreateLocked(ctx)
}

func (s *ProjectSession) CommitCoCreate(ctx context.Context) (webCoCreateState, error) {
	if s.coCreateKind() == webCoCreateKindAdapt {
		return s.commitAdaptCoCreate(ctx)
	}
	if s.coCreateKind() == webCoCreateKindContinuation {
		return s.commitContinuationCoCreate(ctx)
	}

	unlock, err := s.beginAction()
	if err != nil {
		return webCoCreateState{}, err
	}
	defer unlock()

	if s.cocreate == nil {
		return webCoCreateState{}, fmt.Errorf("co-create has not started")
	}
	state := s.cocreate
	if state.hasPendingBriefingDecisions() {
		return state.apiState(), fmt.Errorf("resolve adaptation briefing decisions before starting adaptation")
	}
	needsRepair, repairBaseDraft := state.currentDraftNeedsRepair()
	needsFinalConsolidation := false
	if state.session == nil || !state.session.DraftFresh() {
		needsRepair = true
		if repairBaseDraft == "" {
			repairBaseDraft = state.draftPrompt()
		}
	}
	if !needsRepair && state.shouldConsolidateDraftBeforeCommit() {
		needsFinalConsolidation = true
		if repairBaseDraft == "" {
			repairBaseDraft = state.draftPrompt()
		}
	}
	if needsRepair || needsFinalConsolidation {
		if err := s.repairCoCreateDraftForCommitLocked(ctx, state, repairBaseDraft); err != nil {
			return state.apiState(), err
		}
	}
	if err := s.cocreate.requireReadyDraft(); err != nil {
		return webCoCreateState{}, err
	}
	if state.kind == webCoCreateKindNormal && state.coreCast != nil {
		if err := s.persistReplyCoreCastLocked(host.CoCreateReply{CoreCast: state.coreCast}); err != nil {
			return state.apiState(), fmt.Errorf("persist legacy CoreCast seed: %w", err)
		}
	}
	if needsFinalConsolidation {
		state.draftConsolidated = true
		s.saveCoCreateCheckpoint()
	}
	switch state.kind {
	case webCoCreateKindStage:
		if err := s.host.ResumeFromCoCreate(state.draftPrompt()); err != nil {
			return state.apiState(), err
		}
	default:
		plan, err := state.session.BuildPlanWithWordBudget(state.targetTotalWords)
		if err != nil {
			return state.apiState(), err
		}
		if err := s.prepareNormalFoundationGeneration(plan, ""); err != nil {
			return state.apiState(), err
		}
	}
	api := state.apiState()
	s.cocreate = nil
	api.Active = false
	s.AppendSnapshot()
	return api, nil
}

func (s *ProjectSession) prepareNormalFoundationGeneration(plan startup.Plan, createdAt string) error {
	if err := s.host.PrepareUserRules(plan.RawPrompt); err != nil {
		return err
	}
	if err := s.persistWordBudget(plan.WordBudget); err != nil {
		return err
	}
	st := storepkg.NewStore(s.manifest.OutputDir)
	if _, err := st.CoreCast.PublishConfirmed(st.Foundation, nil, nil, nil); err != nil {
		return fmt.Errorf("restore confirmed core cast before Foundation generation: %w", err)
	}
	if _, err := st.SaveFoundationPremise(nil, strings.TrimSpace(plan.RawPrompt)); err != nil {
		return fmt.Errorf("save confirmed co-create premise: %w", err)
	}
	review, err := s.normalCoCreatePlanningReview(plan, createdAt, domain.PlanningReviewStatusCollecting)
	if err != nil {
		return err
	}
	transition, err := st.BeginOriginalCharacterReview(review)
	if err != nil {
		return fmt.Errorf("begin foundation review: %w", err)
	}
	if err := s.host.StartPrepared(plan.StartPrompt); err != nil {
		if rollbackErr := st.RollbackFoundationReview(transition); rollbackErr != nil {
			return fmt.Errorf("start prepared: %v; rollback foundation review: %w", err, rollbackErr)
		}
		return err
	}
	return nil
}

func (s *ProjectSession) commitContinuationCoCreate(ctx context.Context) (webCoCreateState, error) {
	unlock, err := s.beginAction()
	if err != nil {
		return webCoCreateState{}, err
	}
	defer unlock()

	if s.cocreate == nil || s.cocreate.kind != webCoCreateKindContinuation {
		return webCoCreateState{}, fmt.Errorf("continuation co-create has not started")
	}
	state := s.cocreate
	needsRepair, repairBaseDraft := state.currentDraftNeedsRepair()
	if state.session == nil || !state.session.DraftFresh() {
		needsRepair = true
		if repairBaseDraft == "" {
			repairBaseDraft = state.draftPrompt()
		}
	}
	if needsRepair {
		if err := s.repairCoCreateDraftForCommitLocked(ctx, state, repairBaseDraft); err != nil {
			return state.apiState(), err
		}
	}
	if err := state.requireReadyDraft(); err != nil {
		return state.apiState(), err
	}
	snapshot, err := s.host.ContinuationSnapshot()
	if err != nil {
		return state.apiState(), err
	}
	if snapshot == nil {
		return state.apiState(), fmt.Errorf("continuation source has not been imported")
	}
	if _, err := s.host.CommitContinuationDraft(state.draftPrompt(), snapshot.Workflow.Revision); err != nil {
		return state.apiState(), err
	}
	s.host.CancelCoCreate()
	api := state.apiState()
	api.Active = false
	s.cocreate = nil
	s.clearCoCreateCheckpoint()
	s.AppendSnapshot()
	return api, nil
}

func (s *ProjectSession) commitAdaptCoCreate(ctx context.Context) (webCoCreateState, error) {
	actionCtx, unlock, err := s.beginCancellableAction(ctx, projectActionKindAdaptationProposal)
	if err != nil {
		return webCoCreateState{}, err
	}
	finished := false
	defer func() {
		if !finished {
			unlock()
			s.AppendSnapshot()
		}
	}()

	s.AppendSnapshot()
	if s.cocreate == nil {
		return webCoCreateState{}, fmt.Errorf("co-create has not started")
	}
	state := s.cocreate
	if state.hasPendingBriefingDecisions() {
		return state.apiState(), fmt.Errorf("resolve adaptation briefing decisions before starting adaptation")
	}
	needsRepair, repairBaseDraft := state.currentDraftNeedsRepair()
	needsFinalConsolidation := false
	if state.session == nil || !state.session.DraftFresh() {
		needsRepair = true
		if repairBaseDraft == "" {
			repairBaseDraft = state.draftPrompt()
		}
	}
	if !needsRepair && state.shouldConsolidateDraftBeforeCommit() {
		needsFinalConsolidation = true
		if repairBaseDraft == "" {
			repairBaseDraft = state.draftPrompt()
		}
	}
	if needsRepair || needsFinalConsolidation {
		if err := s.repairCoCreateDraftForCommitLocked(actionCtx, state, repairBaseDraft); err != nil {
			return state.apiState(), err
		}
	}
	if err := state.requireReadyDraft(); err != nil {
		return webCoCreateState{}, err
	}
	if needsFinalConsolidation {
		state.draftConsolidated = true
		s.saveCoCreateCheckpoint()
	}

	st := storepkg.NewStore(s.manifest.OutputDir)
	_, err = st.Adaptation.SetPlanningWorkflowStage(domain.AdaptationPlanningStageTargetFoundationGenerating, -1)
	if err != nil {
		return state.apiState(), err
	}
	if err := s.host.StartAdaptationCharacterWorkflow(state.draftPrompt()); err != nil {
		return state.apiState(), err
	}
	api := state.apiState()
	s.cocreate = nil
	s.clearCoCreateCheckpoint()
	api.Active = false
	unlock()
	finished = true
	s.AppendSnapshot()
	return api, nil
}

// prepareNormalCoCreateDraft persists the completed co-create brief without
// starting the planner. The user deliberately starts proposal generation from
// the planning review checkpoint.
func (s *ProjectSession) prepareNormalCoCreateDraft(plan startup.Plan, createdAt string) error {
	if err := s.host.PrepareUserRules(plan.RawPrompt); err != nil {
		return err
	}
	if err := s.persistWordBudget(plan.WordBudget); err != nil {
		return err
	}
	return s.saveNormalCoCreatePlanningReview(plan, createdAt, domain.PlanningReviewStatusPending)
}

func (s *ProjectSession) prepareNormalCoCreatePlanning(plan startup.Plan, createdAt string, rollback *domain.PlanningReview) error {
	if err := storepkg.NewStore(s.manifest.OutputDir).RequireConfirmedFoundation(); err != nil {
		return err
	}
	if err := s.host.PrepareUserRules(plan.RawPrompt); err != nil {
		return err
	}
	if err := s.persistWordBudget(plan.WordBudget); err != nil {
		return err
	}
	if err := s.saveNormalCoCreatePlanningReview(plan, createdAt, domain.PlanningReviewStatusCollecting); err != nil {
		return err
	}
	if err := s.host.StartPrepared(plan.StartPrompt); err != nil {
		st := storepkg.NewStore(s.manifest.OutputDir)
		if rollback != nil {
			_ = st.RunMeta.SetPlanningReview(rollback)
		} else {
			_ = st.RunMeta.ClearPlanningReview()
		}
		return err
	}
	return nil
}

func (s *ProjectSession) saveNormalCoCreatePlanningReview(plan startup.Plan, createdAt, status string) error {
	review, err := s.normalCoCreatePlanningReview(plan, createdAt, status)
	if err != nil {
		return err
	}
	st := storepkg.NewStore(s.manifest.OutputDir)
	if draftBeforeFoundation, err := isCoCreateDraftBeforeFoundation(st, review); err != nil {
		return err
	} else if draftBeforeFoundation {
		return st.SavePreFoundationCoCreateDraftReview(review)
	}
	return st.RunMeta.SetPlanningReview(review)
}

func (s *ProjectSession) normalCoCreatePlanningReview(plan startup.Plan, createdAt, status string) (*domain.PlanningReview, error) {
	if s == nil {
		return nil, fmt.Errorf("project session is nil")
	}
	st := storepkg.NewStore(s.manifest.OutputDir)
	budget := plan.WordBudget
	if budget == nil || budget.TargetTotalWords <= 0 {
		if meta, err := st.RunMeta.Load(); err == nil && meta != nil && meta.WordBudget != nil && meta.WordBudget.TargetTotalWords > 0 {
			copy := *meta.WordBudget
			budget = &copy
			plan.StartPrompt = host.BuildStartPromptWithBudget(plan.RawPrompt, budget)
		}
	}
	now := time.Now().UTC().Format(time.RFC3339)
	createdAt = strings.TrimSpace(createdAt)
	if createdAt == "" {
		createdAt = now
	}
	targetWords := 0
	if budget != nil {
		targetWords = budget.TargetTotalWords
	}
	review := &domain.PlanningReview{
		Status:           status,
		Kind:             domain.PlanningReviewKindBlueprint,
		Brief:            strings.TrimSpace(plan.RawPrompt),
		StartPrompt:      strings.TrimSpace(plan.StartPrompt),
		TargetTotalWords: targetWords,
		CreatedAt:        createdAt,
		UpdatedAt:        now,
	}
	if existing, err := st.RunMeta.PlanningReview(); err == nil && existing != nil {
		if err := st.RequireConfirmedFoundation(); err == nil {
			preserveFoundationReviewBinding(review, existing)
		}
	} else if err != nil {
		return nil, err
	}
	return review, nil
}

func isCoCreateDraftBeforeFoundation(st *storepkg.Store, review *domain.PlanningReview) (bool, error) {
	if st == nil || review == nil ||
		review.Kind != domain.PlanningReviewKindBlueprint ||
		review.Status != domain.PlanningReviewStatusPending {
		return false, nil
	}
	foundation, err := st.Foundation.Load()
	if err != nil {
		return false, fmt.Errorf("load co-create draft foundation state: %w", err)
	}
	return strings.TrimSpace(foundation.Premise) == "" &&
		len(foundation.Characters) == 0 &&
		len(foundation.WorldRules) == 0 &&
		len(foundation.Relationships) == 0, nil
}

func preserveFoundationReviewBinding(target, source *domain.PlanningReview) {
	if target == nil || source == nil {
		return
	}
	target.FoundationStatus = source.FoundationStatus
	target.FoundationRevision = source.FoundationRevision
	target.FoundationAuditSignature = source.FoundationAuditSignature
	target.CoreCastSignature = source.CoreCastSignature
	target.FoundationGeneration = source.FoundationGeneration
	target.FoundationBaseRevision = source.FoundationBaseRevision
	target.FoundationSections = append([]string(nil), source.FoundationSections...)
	target.FoundationFeedback = source.FoundationFeedback
	target.FoundationConfirmedAt = source.FoundationConfirmedAt
}

func (s *ProjectSession) clearNormalCoCreatePlanningReview() error {
	if s == nil {
		return nil
	}
	st := storepkg.NewStore(s.manifest.OutputDir)
	return st.RunMeta.ClearPlanningReview()
}

type coCreatePlanningRevisionTarget struct {
	Instruction string
	Scope       string
	Label       string
	StableID    string
	VolumeIndex int
	FromChapter int
	ToChapter   int
}

func (s *ProjectSession) ReviseCoCreatePlanning(ctx context.Context, req webCoCreatePlanningRevisionRequest) error {
	instruction := strings.TrimSpace(req.Feedback)
	if instruction == "" {
		instruction = strings.TrimSpace(req.Instruction)
	}
	if instruction == "" {
		return fmt.Errorf("feedback is required")
	}
	unlock, err := s.beginAction()
	if err != nil {
		return err
	}
	defer unlock()
	return s.reviseCoCreatePlanningWithinAction(ctx, req, instruction)
}

func (s *ProjectSession) reviseCoCreatePlanningWithinAction(
	ctx context.Context,
	req webCoCreatePlanningRevisionRequest,
	instruction string,
) error {
	if s.cocreate != nil {
		return fmt.Errorf("finish the active co-create session before revising the planning review")
	}
	st := storepkg.NewStore(s.manifest.OutputDir)
	review, err := st.RunMeta.PlanningReview()
	if err != nil {
		return fmt.Errorf("read planning review: %w", err)
	}
	if review == nil || (review.Status != domain.PlanningReviewStatusPending && review.Status != domain.PlanningReviewStatusCollecting) {
		return fmt.Errorf("no pending co-create planning review")
	}
	if strings.TrimSpace(review.Brief) == "" {
		return fmt.Errorf("pending co-create planning review has no brief")
	}
	draftBeforeFoundation, err := isCoCreateDraftBeforeFoundation(st, review)
	if err != nil {
		return err
	}
	if !draftBeforeFoundation {
		if err := st.RequireConfirmedFoundation(); err != nil {
			return err
		}
	}
	target, err := normalizeCoCreatePlanningRevisionTarget(st, review, req, instruction)
	if err != nil {
		return err
	}
	if review.Status == domain.PlanningReviewStatusCollecting {
		if err := appendNormalPlanningRevisionFeedback(st, review, target); err != nil {
			return fmt.Errorf("append planning revision feedback: %w", err)
		}
		s.AppendSnapshot()
		return nil
	}
	if review.Kind == domain.PlanningReviewKindVolumeSplit && target.Scope == "volume" {
		if err := s.prepareNormalVolumeSplitRevision(st, review, target); err != nil {
			return fmt.Errorf("start targeted volume revision: %w", err)
		}
		s.AppendSnapshot()
		return nil
	}
	if review.Kind == domain.PlanningReviewKindChapterOutline && target.Scope == "chapter" {
		if err := s.prepareNormalChapterOutlineRevision(st, review, target); err != nil {
			return fmt.Errorf("start targeted chapter-outline revision: %w", err)
		}
		s.AppendSnapshot()
		return nil
	}

	revision := newWebCoCreatePlanningRevisionSession(review, target)
	reply, err := s.runCoCreatePlanningRevision(ctx, revision)
	if err != nil {
		return err
	}
	revision.applyReply(reply)
	if err := revision.requireReadyDraft(); err != nil {
		return fmt.Errorf("revised co-create planning draft is not ready: %w", err)
	}
	plan, err := revision.session.BuildPlanWithWordBudget(review.TargetTotalWords)
	if err != nil {
		return fmt.Errorf("build revised co-create plan: %w", err)
	}
	rollback := *review
	if review.Kind == domain.PlanningReviewKindBlueprint {
		if err := s.prepareNormalCoCreateDraft(plan, review.CreatedAt); err != nil {
			return fmt.Errorf("save revised co-create draft: %w", err)
		}
	} else if err := s.prepareNormalCoCreatePlanning(plan, review.CreatedAt, &rollback); err != nil {
		return fmt.Errorf("start revised co-create planning: %w", err)
	}
	s.AppendSnapshot()
	return nil
}

func appendNormalPlanningRevisionFeedback(st *storepkg.Store, review *domain.PlanningReview, target coCreatePlanningRevisionTarget) error {
	if st == nil {
		return fmt.Errorf("planning revision store is required")
	}
	audits, err := st.OriginalPlanningAudits.Load()
	if err != nil {
		return err
	}
	for index := len(audits) - 1; index >= 0; index-- {
		audit := audits[index]
		if audit.Verdict != "revise" || len(audit.Issues) == 0 {
			continue
		}
		if target.Scope == "volume" && audit.Scope != "skeleton_book" {
			continue
		}
		if target.Scope == "chapter" && audit.Scope != "book" {
			continue
		}
		if strings.TrimSpace(target.StableID) == "" || strings.TrimSpace(audit.ScopeID) == "" || target.StableID != audit.ScopeID {
			continue
		}
		message := strings.TrimSpace(target.Instruction)
		if message == "" {
			return fmt.Errorf("feedback is required")
		}
		issue := &audit.Issues[0]
		issue.Description = appendRevisionFeedback(issue.Description, message)
		issue.RepairInstruction = appendRevisionFeedback(issue.RepairInstruction, message)
		audit.Summary = appendRevisionFeedback(audit.Summary, message)
		if err := st.OriginalPlanningAudits.Save(audit); err != nil {
			return err
		}
		review.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
		return st.RunMeta.SetPlanningReview(review)
	}
	return fmt.Errorf("no active original-fiction revision candidate accepts feedback")
}

func appendRevisionFeedback(current, message string) string {
	current = strings.TrimSpace(current)
	message = strings.TrimSpace(message)
	if current == "" || current == message {
		return message
	}
	if strings.Contains(current, message) {
		return current
	}
	return current + "\n\nAdditional feedback:\n" + message
}

func (s *ProjectSession) prepareNormalVolumeSplitRevision(st *storepkg.Store, review *domain.PlanningReview, target coCreatePlanningRevisionTarget) error {
	if st == nil || review == nil || target.VolumeIndex <= 0 {
		return fmt.Errorf("volume planning review target is required")
	}
	if err := s.host.PrepareUserRules(review.Brief); err != nil {
		return err
	}
	// A user preference enters the same repair queue as an editorial failure.
	// After the selected volume is replaced, the normal skeleton audits rerun
	// before the revised plan is exposed to the user again.
	audit := domain.OriginalPlanningAudit{
		Scope: "skeleton_book", ScopeID: target.StableID, Verdict: "revise", Summary: "user requested a reviewed volume-skeleton revision",
		Issues: []domain.OriginalPlanningAuditIssue{{
			Severity: "major", Volume: target.VolumeIndex, Arc: 1,
			Description: target.Instruction, RepairInstruction: target.Instruction,
		}},
	}
	if err := st.OriginalPlanningAudits.Save(audit); err != nil {
		return fmt.Errorf("queue targeted volume revision: %w", err)
	}
	updated := *review
	updated.Status = domain.PlanningReviewStatusCollecting
	updated.Kind = domain.PlanningReviewKindBlueprint
	updated.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	if err := st.RunMeta.SetPlanningReview(&updated); err != nil {
		return err
	}
	if _, err := s.host.Resume(); err != nil {
		return err
	}
	return nil
}

func (s *ProjectSession) prepareNormalChapterOutlineRevision(st *storepkg.Store, review *domain.PlanningReview, target coCreatePlanningRevisionTarget) error {
	if st == nil || review == nil {
		return fmt.Errorf("planning review store is required")
	}
	volume, arc, _, to, err := locateNormalPlanningArc(st, target.FromChapter)
	if err != nil {
		return err
	}
	if target.ToChapter > to {
		return fmt.Errorf("one revision batch cannot cross story arcs; selected chapter range %d-%d crosses V%d A%d ending at chapter %d", target.FromChapter, target.ToChapter, volume, arc, to)
	}
	if err := s.host.PrepareUserRules(review.Brief); err != nil {
		return err
	}
	audit := domain.OriginalPlanningAudit{
		Scope: "book", ScopeID: target.StableID, Verdict: "revise", Summary: "user requested a reviewed detailed-outline revision",
		Issues: []domain.OriginalPlanningAuditIssue{{
			Severity: "major", Volume: volume, Arc: arc, FromChapter: target.FromChapter, ToChapter: target.ToChapter,
			Description: target.Instruction, RepairInstruction: target.Instruction,
		}},
	}
	if err := st.OriginalPlanningAudits.Save(audit); err != nil {
		return fmt.Errorf("queue targeted outline revision: %w", err)
	}
	updated := *review
	updated.Status = domain.PlanningReviewStatusCollecting
	updated.Kind = domain.PlanningReviewKindVolumeSplit
	updated.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	if err := st.RunMeta.SetPlanningReview(&updated); err != nil {
		return err
	}
	if _, err := s.host.Resume(); err != nil {
		return err
	}
	return nil
}

func locateNormalPlanningArc(st *storepkg.Store, chapter int) (volume, arc, from, to int, err error) {
	if chapter <= 0 {
		return 0, 0, 0, 0, fmt.Errorf("chapter must be positive")
	}
	volumes, err := st.Outline.LoadLayeredOutline()
	if err != nil {
		return 0, 0, 0, 0, err
	}
	next := 1
	for _, item := range volumes {
		for _, storyArc := range item.Arcs {
			count := len(storyArc.Chapters)
			if count == 0 {
				count = storyArc.EstimatedChapters
			}
			end := next + count - 1
			if chapter >= next && chapter <= end && len(storyArc.Chapters) > 0 {
				return item.Index, storyArc.Index, next, end, nil
			}
			next = end + 1
		}
	}
	return 0, 0, 0, 0, fmt.Errorf("chapter %d is not in an expanded planning arc", chapter)
}

func normalizeCoCreatePlanningRevisionTarget(st *storepkg.Store, review *domain.PlanningReview, req webCoCreatePlanningRevisionRequest, instruction string) (coCreatePlanningRevisionTarget, error) {
	scope := strings.ToLower(strings.TrimSpace(req.Scope))
	if scope == "" {
		switch {
		case req.VolumeIndex > 0:
			scope = "volume"
		case req.Chapter > 0 || req.FromChapter > 0 || req.ToChapter > 0:
			scope = "chapter"
		default:
			scope = "all"
		}
	}
	if scope == "range" {
		scope = "chapter"
	}
	target := coCreatePlanningRevisionTarget{
		Instruction: instruction,
		Scope:       scope,
		Label:       strings.TrimSpace(req.Target),
		VolumeIndex: req.VolumeIndex,
		FromChapter: req.FromChapter,
		ToChapter:   req.ToChapter,
	}
	if target.FromChapter == 0 && req.Chapter > 0 {
		target.FromChapter = req.Chapter
	}
	if target.ToChapter == 0 && target.FromChapter > 0 {
		target.ToChapter = target.FromChapter
	}
	switch scope {
	case "all":
		if target.Label == "" {
			target.Label = "entire planning review"
		}
		return target, nil
	case "volume":
		if review.Kind == domain.PlanningReviewKindBlueprint && review.Status != domain.PlanningReviewStatusCollecting {
			return target, fmt.Errorf("blueprint review does not support volume-targeted revision")
		}
		if target.VolumeIndex <= 0 {
			return target, fmt.Errorf("volume_index is required for volume-targeted planning revision")
		}
		label, err := coCreatePlanningRevisionVolumeLabel(st, target.VolumeIndex)
		if err != nil {
			return target, err
		}
		if target.Label == "" {
			target.Label = label
		}
		volumes, err := st.Outline.LoadLayeredOutline()
		if err != nil {
			return target, err
		}
		for _, volume := range volumes {
			if volume.Index == target.VolumeIndex {
				target.StableID = volume.ID
				break
			}
		}
		if target.StableID == "" {
			return target, fmt.Errorf("volume target has no stable identity")
		}
		return target, nil
	case "chapter":
		if review.Kind != domain.PlanningReviewKindChapterOutline && review.Status != domain.PlanningReviewStatusCollecting {
			return target, fmt.Errorf("chapter-targeted revision requires a chapter outline review")
		}
		if target.FromChapter <= 0 || target.ToChapter <= 0 {
			return target, fmt.Errorf("chapter is required for chapter-targeted planning revision")
		}
		if target.FromChapter > target.ToChapter {
			target.FromChapter, target.ToChapter = target.ToChapter, target.FromChapter
		}
		label, err := coCreatePlanningRevisionChapterLabel(st, target.FromChapter, target.ToChapter)
		if err != nil {
			return target, err
		}
		if target.Label == "" {
			target.Label = label
		}
		if review.Status == domain.PlanningReviewStatusCollecting && target.FromChapter != target.ToChapter {
			return target, fmt.Errorf("collecting feedback requires one exact stable chapter target")
		}
		volumes, err := st.Outline.LoadLayeredOutline()
		if err != nil {
			return target, err
		}
		for _, chapter := range domain.FlattenOutline(volumes) {
			if chapter.Chapter == target.FromChapter {
				target.StableID = chapter.ID
				break
			}
		}
		if review.Status == domain.PlanningReviewStatusCollecting && target.StableID == "" {
			return target, fmt.Errorf("chapter target has no stable identity")
		}
		return target, nil
	default:
		return target, fmt.Errorf("unsupported planning revision scope %q", scope)
	}
}

func coCreatePlanningRevisionVolumeLabel(st *storepkg.Store, volumeIndex int) (string, error) {
	volumes, err := st.Outline.LoadLayeredOutline()
	if err != nil {
		return "", fmt.Errorf("load layered outline: %w", err)
	}
	for _, volume := range volumes {
		if volume.Index != volumeIndex {
			continue
		}
		title := strings.TrimSpace(volume.Title)
		if title == "" {
			return fmt.Sprintf("volume %d", volumeIndex), nil
		}
		return fmt.Sprintf("volume %d: %s", volumeIndex, title), nil
	}
	return "", fmt.Errorf("volume %d is not in the current planning review", volumeIndex)
}

func coCreatePlanningRevisionChapterLabel(st *storepkg.Store, from, to int) (string, error) {
	chapters, err := st.Outline.LoadOutline()
	if err != nil {
		return "", fmt.Errorf("load outline: %w", err)
	}
	if len(chapters) == 0 {
		volumes, lerr := st.Outline.LoadLayeredOutline()
		if lerr != nil {
			return "", fmt.Errorf("load layered outline: %w", lerr)
		}
		chapters = domain.FlattenOutline(volumes)
	}
	available := make(map[int]domain.OutlineEntry, len(chapters))
	for index, chapter := range chapters {
		number := chapter.Chapter
		if number <= 0 {
			number = index + 1
			chapter.Chapter = number
		}
		available[number] = chapter
	}
	for chapter := from; chapter <= to; chapter++ {
		if _, ok := available[chapter]; !ok {
			return "", fmt.Errorf("chapter %d is not in the current planning review", chapter)
		}
	}
	if from == to {
		title := strings.TrimSpace(available[from].Title)
		if title == "" {
			return fmt.Sprintf("chapter %d", from), nil
		}
		return fmt.Sprintf("chapter %d: %s", from, title), nil
	}
	return fmt.Sprintf("chapters %d-%d", from, to), nil
}

func (s *ProjectSession) ConfirmCoCreatePlanning() (string, error) {
	unlock, err := s.beginAction()
	if err != nil {
		return "", err
	}
	defer unlock()

	st := storepkg.NewStore(s.manifest.OutputDir)
	review, err := st.RunMeta.PlanningReview()
	if err != nil {
		return "", fmt.Errorf("read planning review: %w", err)
	}
	if review == nil || review.Status != domain.PlanningReviewStatusPending {
		return "", fmt.Errorf("no pending co-create planning review")
	}
	draftBeforeFoundation, err := isCoCreateDraftBeforeFoundation(st, review)
	if err != nil {
		return "", err
	}
	if draftBeforeFoundation {
		if active, activeErr := st.Revisions.Active(); activeErr != nil {
			return "", fmt.Errorf("read active revision before Foundation generation: %w", activeErr)
		} else if active != nil {
			return "", fmt.Errorf("Foundation generation is blocked by active revision %s", active.ID)
		}
		var budget *domain.WordBudget
		if meta, loadErr := st.RunMeta.Load(); loadErr != nil {
			return "", fmt.Errorf("load co-create draft word budget: %w", loadErr)
		} else if meta != nil && meta.WordBudget != nil {
			copy := *meta.WordBudget
			budget = &copy
		}
		plan := startup.Plan{
			RawPrompt:   strings.TrimSpace(review.Brief),
			StartPrompt: host.BuildStartPromptWithBudget(review.Brief, budget),
			WordBudget:  budget,
		}
		if err := s.prepareNormalFoundationGeneration(plan, review.CreatedAt); err != nil {
			return "", fmt.Errorf("start Foundation generation from co-create draft: %w", err)
		}
		s.AppendSnapshot()
		return "generating Foundation from revised co-create draft", nil
	}
	if err := st.RequireConfirmedFoundation(); err != nil {
		return "", err
	}
	if active, activeErr := st.Revisions.Active(); activeErr != nil {
		return "", fmt.Errorf("read active revision before planning approval: %w", activeErr)
	} else if active != nil {
		if active.Mode != domain.RevisionModeFoundation {
			return "", fmt.Errorf("planning approval is blocked by active revision %s", active.ID)
		}
		if err := host.NewFoundationRevisionService(st).ApproveOutline(); err != nil {
			return "", fmt.Errorf("approve Foundation-owned outline revision: %w", err)
		}
	}
	switch review.Kind {
	case domain.PlanningReviewKindBlueprint:
		rollback := *review
		if meta, loadErr := st.RunMeta.Load(); loadErr == nil && meta != nil && meta.WordBudget != nil && meta.WordBudget.TargetTotalWords > 0 {
			review.TargetTotalWords = meta.WordBudget.TargetTotalWords
			review.StartPrompt = host.BuildStartPromptWithBudget(review.Brief, meta.WordBudget)
		}
		review.Status = domain.PlanningReviewStatusCollecting
		review.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
		if err := st.RunMeta.SetPlanningReview(review); err != nil {
			return "", fmt.Errorf("start planning review: %w", err)
		}
		if err := s.host.StartPrepared(review.StartPrompt); err != nil {
			_ = st.RunMeta.SetPlanningReview(&rollback)
			return "", err
		}
		s.AppendSnapshot()
		return "generating detailed proposal", nil
	case domain.PlanningReviewKindVolumeSplit:
		if missing := coCreatePlanningMissingForVolumeSplit(st.FoundationMissing()); len(missing) > 0 {
			return "", fmt.Errorf("planning foundation is incomplete: %s", strings.Join(missing, ", "))
		}
		rollback := *review
		review.Status = domain.PlanningReviewStatusCollecting
		review.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
		if err := st.RunMeta.SetPlanningReview(review); err != nil {
			return "", fmt.Errorf("start chapter outline generation: %w", err)
		}
		if _, err := s.host.Resume(); err != nil {
			_ = st.RunMeta.SetPlanningReview(&rollback)
			return "", err
		}
		s.AppendSnapshot()
		return "volume outline approved; generating detailed chapter outline in batches", nil
	default:
		if missing := st.FoundationMissing(); len(missing) > 0 {
			return "", fmt.Errorf("planning foundation is incomplete: %s", strings.Join(missing, ", "))
		}
	}
	if review.FoundationStatus == domain.FoundationReviewStatusApproved {
		review.Status = domain.PlanningReviewStatusApproved
		review.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
		if err := st.RunMeta.SetPlanningReview(review); err != nil {
			return "", fmt.Errorf("retain approved foundation binding: %w", err)
		}
	} else if err := st.RunMeta.ClearPlanningReview(); err != nil {
		return "", fmt.Errorf("clear planning review: %w", err)
	}
	if review.Kind != domain.PlanningReviewKindVolumeSplit {
		progress, err := st.Progress.Load()
		if err != nil {
			return "", fmt.Errorf("load progress: %w", err)
		}
		if progress == nil {
			return "", fmt.Errorf("progress is missing")
		}
		if progress.Phase != domain.PhaseWriting {
			if err := st.Progress.UpdatePhase(domain.PhaseWriting); err != nil {
				return "", fmt.Errorf("approve planning review: %w", err)
			}
		}
	}
	label, err := s.host.Resume()
	s.AppendSnapshot()
	return label, err
}

func (s *ProjectSession) ConfirmCoCreateFoundation(expectedRevision int64, expectedAuditSignature string) (string, error) {
	unlock, err := s.beginAction()
	if err != nil {
		return "", err
	}
	defer unlock()
	st := storepkg.NewStore(s.manifest.OutputDir)
	if adaptationReview, loadErr := st.Adaptation.LoadTargetFoundationReview(); loadErr != nil {
		return "", loadErr
	} else if adaptationReview != nil {
		if _, err := st.ConfirmAdaptationTargetFoundation(expectedRevision, expectedAuditSignature); err != nil {
			return "", err
		}
		workflow, err := st.Adaptation.LoadPlanningWorkflow()
		if err != nil || workflow == nil {
			return "", fmt.Errorf("load adaptation foundation workflow: %w", err)
		}
		if _, err := st.Adaptation.SetPlanningWorkflowStage(domain.AdaptationPlanningStageSkeletonGenerating, workflow.Revision); err != nil {
			return "", err
		}
		manifest, err := st.Adaptation.LoadSourceManifest()
		if err != nil || manifest == nil {
			return "", fmt.Errorf("load adaptation source manifest: %w", err)
		}
		intent, err := st.Adaptation.LoadCoCreateIntent()
		if err != nil || intent == nil {
			return "", fmt.Errorf("load adaptation intent: %w", err)
		}
		result, err := s.buildAdaptationProposalVolumes(context.Background(), adapt.ProposalOptions{
			Brief: adaptationReview.Brief, SourcePath: manifest.SourcePath, Granularity: intent.Granularity,
			RewritePolicy: intent.RewritePolicy, WordTolerance: intent.WordTolerance,
		})
		if err != nil {
			return "", err
		}
		s.AppendSnapshot()
		if result != nil && result.VolumeReview != nil {
			return "target foundation approved; adaptation volume skeleton awaits review", nil
		}
		return "target foundation approved; adaptation proposal awaits review", nil
	}
	_, transition, err := st.ConfirmFoundationForPlanning(expectedRevision, expectedAuditSignature)
	if err != nil {
		return "", err
	}
	if active, activeErr := st.Revisions.Active(); activeErr == nil && active != nil && active.Mode == domain.RevisionModeFoundation {
		s.AppendSnapshot()
		return "Foundation approved; active revision is awaiting outline regeneration and approval", nil
	}
	label, err := s.host.Resume()
	if err != nil {
		if rollbackErr := st.RollbackFoundationConfirmation(transition); rollbackErr != nil {
			var reviewErr *storepkg.FoundationReviewError
			if !errors.As(rollbackErr, &reviewErr) || reviewErr.Code != storepkg.FoundationReviewErrorStale {
				return "", errors.Join(err, fmt.Errorf("restore retryable pending foundation review: %w", rollbackErr))
			}
		}
		return "", err
	}
	s.AppendSnapshot()
	return label, nil
}

func (s *ProjectSession) ReviseCoCreateFoundation(feedback string) (string, error) {
	unlock, err := s.beginAction()
	if err != nil {
		return "", err
	}
	defer unlock()
	st := storepkg.NewStore(s.manifest.OutputDir)
	if adaptationReview, loadErr := st.Adaptation.LoadTargetFoundationReview(); loadErr != nil {
		return "", loadErr
	} else if adaptationReview != nil {
		generating, err := st.MarkAdaptationTargetFoundationPending(feedback)
		if err != nil {
			return "", err
		}
		workflow, err := st.Adaptation.LoadPlanningWorkflow()
		if err != nil || workflow == nil {
			return "", fmt.Errorf("load adaptation foundation revision workflow: %w", err)
		}
		if _, err := s.host.GenerateAdaptationTargetFoundationContext(context.Background(), adapt.TargetFoundationOptions{
			Brief: generating.Brief, Feedback: feedback, ExpectedWorkflowRevision: workflow.Revision,
		}); err != nil {
			return "", err
		}
		s.AppendSnapshot()
		return "adaptation target foundation regenerated for review", nil
	}
	if _, err := st.ReviseFoundation(feedback); err != nil {
		return "", err
	}
	label, err := s.host.Resume()
	if err != nil {
		return "", err
	}
	s.AppendSnapshot()
	return label, nil
}

func normalDetailedOutlineInstruction(targetWords int) string {
	target := "the persisted book word budget"
	if targetWords > 0 {
		target = fmt.Sprintf("the persisted %d-word book budget", targetWords)
	}
	return "The normal-original volume plan has been approved. Resume the durable planning router. It must expand one arc at a time in exact 3-4 chapter batches, run independent original-fiction arc and volume audits, synthesize at most two volumes per book-audit batch, then run the digest-only whole-book audit. " +
		"Use " + target + "; preserve volume/arc order, causal handoffs, character state, clues and open threads across batches. Every chapter must have a distinct goal, obstacle, choice, consequence, state/relationship change, exit state and hook. Do not analyze or bind any source novel. Do not repeat chapter promises. Stop at chapter-outline user review; do not start prose writing."
}

func coCreatePlanningMissingForVolumeSplit(missing []string) []string {
	filtered := make([]string, 0, len(missing))
	for _, item := range missing {
		if item == "outline" {
			continue
		}
		filtered = append(filtered, item)
	}
	return filtered
}

func (s *ProjectSession) persistWordBudget(budget *domain.WordBudget) error {
	if budget == nil || budget.TargetTotalWords <= 0 {
		return nil
	}
	if err := s.host.SetWordBudget(budget); err != nil {
		return fmt.Errorf("save word budget: %w", err)
	}
	return nil
}

func adaptationVolumeReviewAsPlan(review domain.AdaptationVolumeReview) *domain.AdaptationPlan {
	return &domain.AdaptationPlan{
		Granularity:       review.Granularity,
		Status:            domain.AdaptationPlanStatusVolumeReview,
		RewritePolicy:     review.RewritePolicy,
		Brief:             review.Brief,
		Planner:           review.Planner,
		Volumes:           append([]domain.AdaptationVolumePlan(nil), review.Volumes...),
		WordTolerance:     review.WordTolerance,
		MainlineRules:     append([]string(nil), review.MainlineRules...),
		RelationshipGoals: append([]string(nil), review.RelationshipGoals...),
	}
}

func (s *ProjectSession) CoCreateState() *webCoCreateState {
	if s.cocreate == nil {
		return nil
	}
	state := s.cocreate.apiState()
	return &state
}

func (s *ProjectSession) restoreCoCreateCheckpoint() error {
	return s.restoreCoCreateCheckpointKind("")
}

func (s *ProjectSession) restoreCoCreateCheckpointKind(expectedKind string) error {
	path := s.coCreateCheckpointPath()
	if path == "" {
		return nil
	}
	if s.coCreateRestoreBlockedByProjectState() {
		// Once formal planning/writing has advanced, a co-create checkpoint is
		// inactive recovery state. Keep it while rollback can still reach the
		// draft boundary, but discard it once prose writing has made that
		// recovery boundary obsolete.
		if s.coCreateCheckpointPastRollbackBoundary() {
			s.clearCoCreateCheckpoint()
		}
		return nil
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var checkpoint webCoCreateCheckpoint
	if err := json.Unmarshal(data, &checkpoint); err != nil {
		return fmt.Errorf("decode %s: %w", path, err)
	}
	state, err := webCoCreateSessionFromCheckpoint(checkpoint)
	if err != nil {
		return fmt.Errorf("restore %s: %w", path, err)
	}
	if expectedKind != "" && state.kind != expectedKind {
		return fmt.Errorf("restore %s: checkpoint kind %q does not match rollback mode %q", path, state.kind, expectedKind)
	}
	if err := s.fillCoCreateAdaptationSource(state); err != nil {
		return fmt.Errorf("restore %s: %w", path, err)
	}
	if isPausedCoCreateKind(state.kind) && !s.host.PauseForCoCreate() {
		return fmt.Errorf("restore %s: cannot re-enter stage co-create", path)
	}
	s.cocreate = state
	if err := s.refreshCoreCastLocked(state, false); err != nil {
		return fmt.Errorf("restore %s core cast: %w", path, err)
	}
	s.appendCoCreateState(state.apiState())
	return nil
}

func (s *ProjectSession) coCreateRestoreBlockedByProjectState() bool {
	s.mu.Lock()
	outputDir := strings.TrimSpace(s.manifest.OutputDir)
	s.mu.Unlock()
	if outputDir == "" {
		return false
	}
	st := storepkg.NewStore(outputDir)
	if review, err := st.RunMeta.PlanningReview(); err == nil && review != nil {
		return true
	}
	if progress, err := st.Progress.Load(); err == nil && progress != nil {
		if progress.Phase == domain.PhaseWriting || progress.Phase == domain.PhaseComplete {
			return true
		}
	}
	if plan, err := st.Adaptation.LoadPlan(); err == nil && plan != nil && len(plan.Chapters) > 0 {
		return true
	}
	if proposal, err := st.Adaptation.LoadProposal(); err == nil && proposal != nil && len(proposal.Chapters) > 0 {
		return true
	}
	if review, err := st.Adaptation.LoadVolumeReview(); err == nil && review != nil && len(review.Volumes) > 0 {
		return true
	}
	return false
}

func (s *ProjectSession) coCreateCheckpointPastRollbackBoundary() bool {
	s.mu.Lock()
	outputDir := strings.TrimSpace(s.manifest.OutputDir)
	s.mu.Unlock()
	if outputDir == "" {
		return false
	}
	progress, err := storepkg.NewStore(outputDir).Progress.Load()
	if err != nil || progress == nil {
		return false
	}
	return progress.Phase == domain.PhaseWriting || progress.Phase == domain.PhaseComplete
}

func (s *ProjectSession) restoreCoCreateCheckpointFromLog() error {
	return s.restoreCoCreateCheckpointFromLogKind("")
}

func (s *ProjectSession) restoreCoCreateCheckpointFromLogKind(expectedKind string) error {
	return fmt.Errorf("redacted co-create diagnostics cannot restore business state; a durable checkpoint is required")
}

func latestWebCoCreateLogEntry(path string) (webCoCreateLogEntry, bool, error) {
	return latestWebCoCreateLogEntryForKind(path, "")
}

func latestWebCoCreateLogEntryForKind(path, expectedKind string) (webCoCreateLogEntry, bool, error) {
	file, err := os.Open(path)
	if err != nil {
		return webCoCreateLogEntry{}, false, err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 8*1024*1024)
	var latest webCoCreateLogEntry
	found := false
	for scanner.Scan() {
		var entry webCoCreateLogEntry
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			continue
		}
		if len(cleanCoCreateLogHistory(entry.InputHistory)) == 0 {
			continue
		}
		if expectedKind != "" && inferWebCoCreateKindFromLog(entry.InputHistory) != expectedKind {
			continue
		}
		latest = entry
		found = true
	}
	if err := scanner.Err(); err != nil {
		return webCoCreateLogEntry{}, false, err
	}
	return latest, found, nil
}

func rollbackCoCreateKind(mode string) string {
	switch strings.TrimSpace(mode) {
	case "adaptation":
		return webCoCreateKindAdapt
	case "normal":
		return webCoCreateKindNormal
	default:
		return ""
	}
}

func (s *ProjectSession) fillCoCreateAdaptationSource(state *webCoCreateSession) error {
	if state == nil || state.kind != webCoCreateKindAdapt || strings.TrimSpace(state.sourcePath) != "" {
		return nil
	}
	s.mu.Lock()
	manifest := s.manifest
	s.mu.Unlock()
	status, err := projectAdaptationStatus(manifest, false)
	if err != nil {
		return err
	}
	if status.SourceFile == nil || strings.TrimSpace(status.SourceFile.RelativePath) == "" {
		return nil
	}
	sourceFile := strings.TrimSpace(status.SourceFile.RelativePath)
	sourcePath, err := adaptationSourcePathFromName(sourceFile, manifest, false)
	if err != nil {
		return err
	}
	state.sourceFile = sourceFile
	state.sourcePath = sourcePath
	return nil
}

func (s *ProjectSession) saveCoCreateCheckpoint() {
	if err := s.writeCoCreateCheckpoint(); err != nil {
		slog.Warn("save co-create checkpoint failed", "module", "web", "project", s.projectID(), "err", err)
	}
}

func (s *ProjectSession) saveRecoverableCoCreateCheckpoint() {
	if err := s.writeCoCreateCheckpointWithFailed(true); err != nil {
		slog.Warn("save recoverable co-create checkpoint failed", "module", "web", "project", s.projectID(), "err", err)
	}
}

func (s *ProjectSession) writeCoCreateCheckpoint() error {
	if s.cocreate == nil {
		return nil
	}
	return s.writeCoCreateCheckpointWithFailed(s.cocreate.failed)
}

func (s *ProjectSession) writeCoCreateCheckpointWithFailed(failed bool) error {
	if s.cocreate == nil {
		return nil
	}
	path := s.coCreateCheckpointPath()
	if path == "" {
		return nil
	}
	if err := s.persistCoreCastGateBinding(s.cocreate); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s.cocreate.checkpointWithFailed(time.Now(), failed), "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o644); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

func (s *ProjectSession) persistCoreCastGateBinding(state *webCoCreateSession) error {
	if state == nil || state.session == nil ||
		(state.kind != webCoCreateKindNormal && state.kind != webCoCreateKindAdapt) ||
		state.session.DraftRevision() <= 0 || state.session.DraftHash() == "" {
		return nil
	}
	coreCastStore := storepkg.NewStore(s.manifest.OutputDir).CoreCast
	binding := storepkg.CoreCastGateBinding{
		Mode:          domain.CoreCastModeNormal,
		DraftRevision: state.session.DraftRevision(),
		DraftHash:     state.session.DraftHash(),
	}
	if state.kind == webCoCreateKindAdapt {
		binding.Mode = domain.CoreCastModeAdaptation
		var err error
		binding.SourceSignature, binding.AdaptationIntentHash, err = s.currentAdaptationCoreCastBinding()
		if err != nil {
			// The first recoverable checkpoint can be written before adaptation
			// briefing has persisted its intent. A later checkpoint binds both
			// values before Character generation starts.
			return nil
		}
	}
	current, err := coreCastStore.LoadGateBinding()
	if err != nil {
		return err
	}
	if current != nil && binding.DraftRevision <= current.DraftRevision {
		sameSemanticDraft := binding.Mode == current.Mode &&
			binding.DraftHash == current.DraftHash &&
			binding.SourceSignature == current.SourceSignature &&
			binding.AdaptationIntentHash == current.AdaptationIntentHash
		floor := current.DraftRevision
		if !sameSemanticDraft {
			floor++
		}
		state.session.SetDraftRevisionFloor(floor)
		binding.DraftRevision = state.session.DraftRevision()
	}
	_, err = coreCastStore.SaveGateBinding(binding)
	return err
}

func (s *ProjectSession) currentAdaptationCoreCastBinding() (string, string, error) {
	st := storepkg.NewStore(s.manifest.OutputDir)
	manifest, err := st.Adaptation.LoadSourceManifest()
	if err != nil || manifest == nil {
		return "", "", fmt.Errorf("current adaptation source manifest is required for core cast binding")
	}
	intent, err := st.Adaptation.LoadCoCreateIntent()
	if err != nil || intent == nil {
		return "", "", fmt.Errorf("current adaptation intent is required for core cast binding")
	}
	return storepkg.AdaptationSourceSignature(*manifest), adapt.CoCreateIntentHash(*intent), nil
}

func (s *ProjectSession) seedCoreCastDraftRevision(state *webCoCreateSession) error {
	if state == nil || !state.supportsLegacyCoreCast() {
		return nil
	}
	binding, err := storepkg.NewStore(s.manifest.OutputDir).CoreCast.LoadGateBinding()
	if err != nil {
		return err
	}
	if binding != nil {
		state.session.SetDraftRevisionFloor(binding.DraftRevision)
	}
	return nil
}

func (s *ProjectSession) clearCoCreateCheckpoint() {
	path := s.coCreateCheckpointPath()
	if path == "" {
		return
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		slog.Warn("clear co-create checkpoint failed", "module", "web", "project", s.projectID(), "err", err)
	}
}

func (s *ProjectSession) ResetCoCreateProgress() error {
	unlock, err := s.beginAction()
	if err != nil {
		return err
	}
	defer unlock()

	if s.cocreate != nil && isPausedCoCreateKind(s.cocreate.kind) {
		s.host.CancelCoCreate()
	}
	s.cocreate = nil
	s.clearCoCreateCheckpoint()
	return s.clearCoCreateLog()
}

func (s *ProjectSession) clearCoCreateLog() error {
	path := s.coCreateLogPath()
	if path == "" {
		return nil
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func (s *ProjectSession) coCreateCheckpointPath() string {
	s.mu.Lock()
	outputDir := strings.TrimSpace(s.manifest.OutputDir)
	s.mu.Unlock()
	if outputDir == "" {
		return ""
	}
	return filepath.Join(outputDir, filepath.FromSlash(webCoCreateCheckpointRelPath))
}

func (s *ProjectSession) coCreateLogPath() string {
	s.mu.Lock()
	outputDir := strings.TrimSpace(s.manifest.OutputDir)
	s.mu.Unlock()
	if outputDir == "" {
		return ""
	}
	return filepath.Join(outputDir, filepath.FromSlash(webCoCreateLogRelPath))
}

func (s *ProjectSession) projectID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.manifest.ID
}

func (s *ProjectSession) coCreateKind() string {
	if s == nil || s.cocreate == nil {
		return ""
	}
	return s.cocreate.kind
}

func isPausedCoCreateKind(kind string) bool {
	return kind == webCoCreateKindStage || kind == webCoCreateKindContinuation
}

func (s *ProjectSession) CancelCoCreate() (webCoCreateState, error) {
	if s.coCreateKind() == webCoCreateKindAdapt && s.currentActionKind() == projectActionKindAdaptationProposal {
		if canceledKind, canceledAction := s.cancelCurrentAction(); canceledAction {
			state := webCoCreateState{}
			if s.cocreate != nil {
				state = s.cocreate.apiState()
				state.Active = false
				s.cocreate = nil
				s.clearCoCreateCheckpoint()
			}
			s.appendActionCanceledEvent(canceledKind)
			s.AppendSnapshot()
			return state, nil
		}
	}

	unlock, err := s.beginAction()
	if err != nil {
		return webCoCreateState{}, err
	}
	defer unlock()

	if s.cocreate == nil {
		return webCoCreateState{}, fmt.Errorf("co-create has not started")
	}
	state := s.cocreate.apiState()
	if isPausedCoCreateKind(s.cocreate.kind) {
		s.host.CancelCoCreate()
		s.AppendSnapshot()
	}
	s.cocreate = nil
	s.clearCoCreateCheckpoint()
	return state, nil
}

type webCoCreateStreamFunc func(context.Context, []host.CoCreateMessage, func(kind, text string)) (host.CoCreateReply, error)

func (s *ProjectSession) coCreateStreamFuncLocked() (webCoCreateStreamFunc, error) {
	if s.cocreate == nil {
		return nil, fmt.Errorf("co-create has not started")
	}
	switch s.cocreate.kind {
	case webCoCreateKindStage:
		return s.host.StageCoCreateStream, nil
	case webCoCreateKindContinuation:
		return s.host.ContinuationCoCreateStream, nil
	case webCoCreateKindAdapt:
		return s.host.AdaptCoCreateStream, nil
	default:
		return s.host.CoCreateStream, nil
	}
}

func (s *ProjectSession) coCreateProgressHandlerLocked() func(kind, text string) {
	return func(kind, text string) {
		if s.cocreate == nil {
			return
		}
		s.cocreate.session.ApplyDelta(kind, text)
		s.appendCoCreateState(s.cocreate.apiState())
		s.saveCoCreateCheckpoint()
	}
}

func (s *ProjectSession) repairCoCreateDraftForCommitLocked(ctx context.Context, state *webCoCreateSession, previousDraft string) error {
	if state == nil {
		return fmt.Errorf("co-create has not started")
	}
	stream, err := s.coCreateStreamFuncLocked()
	if err != nil {
		return err
	}
	onProgress := s.coCreateProgressHandlerLocked()
	eventID, startedAt := s.appendCoCreateRunStarted(state.kind)
	streamReply := ""
	if state.session != nil {
		streamReply = state.session.StreamReply()
	}
	repairSeed := host.CoCreateReply{Message: streamReply, Raw: streamReply}
	repairReply, err := stream(ctx, state.draftRepairHistory(repairSeed, previousDraft), onProgress)
	if err != nil {
		state.failed = true
		api := state.apiState()
		s.saveCoCreateCheckpoint()
		s.appendCoCreateState(api)
		err = fmt.Errorf("repair co-create draft: %w", err)
		s.appendCoCreateRunFinished(eventID, startedAt, api, err)
		return err
	}
	if strings.TrimSpace(repairReply.Prompt) == "" {
		state.failed = true
		api := state.apiState()
		s.saveCoCreateCheckpoint()
		s.appendCoCreateState(api)
		err := fmt.Errorf("repair co-create draft: model did not return a draft")
		s.appendCoCreateRunFinished(eventID, startedAt, api, err)
		return err
	}
	if state.draftNeedsRepair(repairReply, previousDraft) {
		state.failed = true
		api := state.apiState()
		s.saveCoCreateCheckpoint()
		s.appendCoCreateState(api)
		err := fmt.Errorf("repair co-create draft: model returned an incomplete draft")
		s.appendCoCreateRunFinished(eventID, startedAt, api, err)
		return err
	}
	state.failed = false
	state.applyReply(repairReply)
	if err := s.persistReplyCoreCastLocked(repairReply); err != nil {
		return err
	}
	api := state.apiState()
	s.saveCoCreateCheckpoint()
	s.appendCoCreateState(api)
	s.appendCoCreateRunFinished(eventID, startedAt, api, nil)
	return nil
}

func newWebCoCreatePlanningRevisionSession(review *domain.PlanningReview, target coCreatePlanningRevisionTarget) *webCoCreateSession {
	brief := strings.TrimSpace(review.Brief)
	history := []host.CoCreateMessage{
		{
			Role:    "user",
			Content: "请根据我的需求整理一份可直接交给小说创作引擎的完整创作指令。",
		},
		{
			Role: "assistant",
			Content: strings.Join([]string{
				"<reply>当前创作规划已进入人工审核。</reply>",
				"<draft>",
				brief,
				"</draft>",
				"<ready>true</ready>",
				"<suggestions></suggestions>",
			}, "\n"),
		},
		{
			Role:    "user",
			Content: coCreatePlanningRevisionInstruction(target),
		},
	}
	return &webCoCreateSession{
		kind: webCoCreateKindNormal,
		session: startup.NewCoCreateSessionFromSnapshot(startup.CoCreateSnapshot{
			History:         history,
			DraftPrompt:     brief,
			DraftHistoryLen: 2,
			Ready:           true,
		}),
	}
}

func coCreatePlanningRevisionInstruction(target coCreatePlanningRevisionTarget) string {
	feedback := strings.TrimSpace(target.Instruction)
	if label := strings.TrimSpace(target.Label); label != "" {
		feedback = "Revision target: " + label + "\n\n" + feedback
	}
	return strings.TrimSpace(`审核未通过，请根据下面的审核意见修订上一版 <draft>。
要求：
- 只修订创作规划，不要开始写正文。
- 必须输出完整、自洽、可直接执行的新 <draft>，不要使用“同上”“保留上一版”等占位说法。
- 尽量保留未被审核意见否定的设定、人物、结构、篇幅约束和风格要求。
- 如果审核意见要求调整章节、分卷、节奏或篇幅，请同步更新相关规划细节。

审核意见：
` + strings.TrimSpace(feedback))
}

func (s *ProjectSession) runCoCreatePlanningRevision(ctx context.Context, state *webCoCreateSession) (host.CoCreateReply, error) {
	if state == nil || state.session == nil {
		return host.CoCreateReply{}, fmt.Errorf("co-create planning revision session is missing")
	}
	eventID, startedAt := s.appendCoCreateRunStarted(webCoCreateKindNormal)
	previousDraft := state.draftPrompt()
	reply, err := s.host.CoCreateStream(ctx, state.session.History(), nil)
	if err != nil {
		s.appendCoCreateRunFinished(eventID, startedAt, state.apiState(), err)
		return host.CoCreateReply{}, err
	}
	reply, err = s.repairCoCreatePlanningRevisionDraft(ctx, state, reply, previousDraft)
	if err != nil {
		s.appendCoCreateRunFinished(eventID, startedAt, state.apiState(), err)
		return host.CoCreateReply{}, err
	}
	if strings.TrimSpace(reply.Prompt) == "" {
		err := fmt.Errorf("co-create planning revision did not return a draft")
		s.appendCoCreateRunFinished(eventID, startedAt, state.apiState(), err)
		return host.CoCreateReply{}, err
	}
	finished := state.apiState()
	finished.Ready = reply.Ready
	finished.DraftPrompt = strings.TrimSpace(reply.Prompt)
	s.appendCoCreateRunFinished(eventID, startedAt, finished, nil)
	return reply, nil
}

func (s *ProjectSession) repairCoCreatePlanningRevisionDraft(
	ctx context.Context,
	state *webCoCreateSession,
	candidate host.CoCreateReply,
	previousDraft string,
) (host.CoCreateReply, error) {
	maxAttempts := max(1, s.host.CurrentStructureRepairMaxAttempts())
	for attempt := 1; state.draftNeedsRepair(candidate, previousDraft); attempt++ {
		if attempt > maxAttempts {
			return host.CoCreateReply{}, fmt.Errorf(
				"repair co-create planning revision draft: model returned an incomplete draft after %d repair attempts",
				maxAttempts,
			)
		}
		repaired, err := s.host.CoCreateStream(
			ctx,
			state.draftRepairHistory(candidate, previousDraft),
			nil,
		)
		if err != nil {
			return host.CoCreateReply{}, fmt.Errorf("repair co-create planning revision draft attempt %d: %w", attempt, err)
		}
		if strings.TrimSpace(repaired.Prompt) == "" {
			if attempt == maxAttempts {
				return host.CoCreateReply{}, fmt.Errorf(
					"repair co-create planning revision draft: model did not return a draft after %d repair attempts",
					maxAttempts,
				)
			}
			continue
		}
		candidate = repaired
	}
	return candidate, nil
}

func (s *ProjectSession) runCoCreateLocked(ctx context.Context) (webCoCreateState, error) {
	if s.cocreate == nil {
		return webCoCreateState{}, fmt.Errorf("co-create has not started")
	}
	stream, err := s.coCreateStreamFuncLocked()
	if err != nil {
		return webCoCreateState{}, err
	}
	onProgress := s.coCreateProgressHandlerLocked()
	s.cocreate.failed = false
	eventID, startedAt := s.appendCoCreateRunStarted(s.cocreate.kind)
	previousDraft := s.cocreate.draftPrompt()
	reply, err := stream(ctx, s.cocreate.session.History(), onProgress)
	if err != nil {
		s.cocreate.failed = true
		state := s.cocreate.apiState()
		s.saveCoCreateCheckpoint()
		s.appendCoCreateRunFinished(eventID, startedAt, state, err)
		return state, err
	}
	if s.cocreate.draftNeedsRepair(reply, previousDraft) {
		repairReply, repairErr := stream(ctx, s.cocreate.draftRepairHistory(reply, previousDraft), onProgress)
		if repairErr != nil {
			s.cocreate.rollbackDraftAfterRejectedReply(reply)
			s.cocreate.failed = true
			state := s.cocreate.apiState()
			s.saveCoCreateCheckpoint()
			s.appendCoCreateState(state)
			err := fmt.Errorf("repair co-create draft: %w", repairErr)
			s.appendCoCreateRunFinished(eventID, startedAt, state, err)
			return state, err
		}
		if strings.TrimSpace(repairReply.Prompt) == "" {
			s.cocreate.rollbackDraftAfterRejectedReply(reply)
			s.cocreate.failed = true
			state := s.cocreate.apiState()
			s.saveCoCreateCheckpoint()
			s.appendCoCreateState(state)
			err := fmt.Errorf("repair co-create draft: model did not return a draft")
			s.appendCoCreateRunFinished(eventID, startedAt, state, err)
			return state, err
		}
		if s.cocreate.draftNeedsRepair(repairReply, previousDraft) {
			s.cocreate.rollbackDraftAfterRejectedReply(reply)
			s.cocreate.failed = true
			state := s.cocreate.apiState()
			s.saveCoCreateCheckpoint()
			s.appendCoCreateState(state)
			err := fmt.Errorf("repair co-create draft: model returned an incomplete draft")
			s.appendCoCreateRunFinished(eventID, startedAt, state, err)
			return state, err
		}
		reply = repairReply
	}
	s.cocreate.applyReply(reply)
	if err := s.persistReplyCoreCastLocked(reply); err != nil {
		s.cocreate.failed = true
		s.cocreate.castError = err.Error()
		state := s.cocreate.apiState()
		s.saveCoCreateCheckpoint()
		s.appendCoCreateState(state)
		s.appendCoCreateRunFinished(eventID, startedAt, state, err)
		return state, err
	}
	if s.cocreate.kind == webCoCreateKindAdapt && s.cocreate.session != nil && s.cocreate.session.DraftFresh() {
		s.cocreate.draftConsolidated = true
	}
	state := s.cocreate.apiState()
	s.saveCoCreateCheckpoint()
	s.appendCoCreateState(state)
	s.appendCoCreateRunFinished(eventID, startedAt, state, nil)
	return state, nil
}

func (s *ProjectSession) refreshCoreCastLocked(state *webCoCreateSession, persistBinding bool) error {
	if state == nil || !state.supportsLegacyCoreCast() {
		return nil
	}
	if persistBinding {
		if err := s.persistCoreCastGateBinding(state); err != nil {
			return err
		}
	}
	st := storepkg.NewStore(s.manifest.OutputDir)
	contract, err := st.CoreCast.Load()
	if err != nil {
		return err
	}
	state.coreCast = contract
	state.sourceCharacters = nil
	state.sourceMajorCharacters = nil
	state.sourceResolutionMissing = nil
	if state.kind != webCoCreateKindAdapt {
		return nil
	}
	source, err := st.Adaptation.LoadSourceFoundation()
	if err != nil {
		return err
	}
	dossier, err := st.Adaptation.LoadCoCreateDossier()
	if err != nil {
		return err
	}
	if source == nil || dossier == nil {
		state.sourceResolutionMissing = []domain.CoreCastMissingItem{{Code: "source_major_set_required", Description: "adaptation source major character set is unavailable"}}
		return nil
	}
	state.sourceCharacters = domain.ResolveSourceCharacters(*source)
	state.sourceMajorCharacters, state.sourceResolutionMissing = domain.ResolveSourceMajorCharacters(*source, *dossier)
	return nil
}

func (s *ProjectSession) persistReplyCoreCastLocked(reply host.CoCreateReply) error {
	if s.cocreate == nil || reply.CoreCast == nil ||
		(s.cocreate.kind != webCoCreateKindNormal && s.cocreate.kind != webCoCreateKindAdapt) {
		return nil
	}
	st := storepkg.NewStore(s.manifest.OutputDir)
	expected := int64(0)
	if current, err := st.CoreCast.Load(); errors.Is(err, storepkg.ErrCoreCastContentSignatureMismatch) &&
		s.cocreate.kind == webCoCreateKindNormal {
		migrated, migrateErr := st.CoreCast.RepairLegacyUnconfirmedSignature()
		if migrateErr != nil {
			return fmt.Errorf("migrate legacy CoreCast seed: %w", migrateErr)
		}
		expected = migrated.Revision
	} else if err != nil {
		return err
	} else if current != nil {
		expected = current.Revision
	}
	candidate := *s.cocreate.coreCast
	candidate.DraftRevision = s.cocreate.session.DraftRevision()
	candidate.DraftHash = s.cocreate.session.DraftHash()
	if s.cocreate.kind == webCoCreateKindAdapt {
		sourceSignature, intentHash, err := s.currentAdaptationCoreCastBinding()
		if err != nil {
			return err
		}
		candidate.SourceSignature = sourceSignature
		candidate.AdaptationIntentHash = intentHash
	}
	saved, err := st.CoreCast.SaveCAS(candidate, expected)
	if err != nil {
		return err
	}
	s.cocreate.coreCast = &saved
	return nil
}

func (s *ProjectSession) UpdateCoreCast(candidate domain.CoreCastContract, expectedRevision int64) (webCoCreateState, error) {
	unlock, err := s.beginAction()
	if err != nil {
		return webCoCreateState{}, err
	}
	defer unlock()
	if s.cocreate == nil || !s.cocreate.supportsLegacyCoreCast() {
		return webCoCreateState{}, fmt.Errorf("normal or adaptation co-create has not started")
	}
	state := s.cocreate
	candidate.DraftRevision = state.session.DraftRevision()
	candidate.DraftHash = state.session.DraftHash()
	if state.kind == webCoCreateKindAdapt {
		candidate.Mode = domain.CoreCastModeAdaptation
		candidate.SourceSignature, candidate.AdaptationIntentHash, err = s.currentAdaptationCoreCastBinding()
		if err != nil {
			return state.apiState(), err
		}
	} else {
		candidate.Mode = domain.CoreCastModeNormal
		candidate.SourceSignature = ""
		candidate.AdaptationIntentHash = ""
	}
	st := storepkg.NewStore(s.manifest.OutputDir)
	saved, err := st.CoreCast.SaveCAS(candidate, expectedRevision)
	if err != nil {
		return state.apiState(), err
	}
	state.coreCast = &saved
	state.castError = ""
	s.saveCoCreateCheckpoint()
	api := state.apiState()
	s.appendCoCreateState(api)
	return api, nil
}

func (s *ProjectSession) ConfirmCoreCast(expectedRevision int64, signature string) (webCoCreateState, error) {
	unlock, err := s.beginAction()
	if err != nil {
		return webCoCreateState{}, err
	}
	defer unlock()
	if s.cocreate == nil || !s.cocreate.supportsLegacyCoreCast() {
		return webCoCreateState{}, fmt.Errorf("normal or adaptation co-create has not started")
	}
	state := s.cocreate
	if err := s.refreshCoreCastLocked(state, true); err != nil {
		return state.apiState(), err
	}
	if state.coreCast == nil || state.coreCast.DraftRevision != state.session.DraftRevision() || state.coreCast.DraftHash != state.session.DraftHash() {
		return state.apiState(), fmt.Errorf("core cast confirmation is stale for the current co-create draft")
	}
	if state.kind == webCoCreateKindAdapt && state.adaptationBriefing != nil &&
		(state.coreCast.SourceSignature != strings.TrimSpace(state.adaptationBriefing.SourceSignature) ||
			state.coreCast.AdaptationIntentHash != strings.TrimSpace(state.adaptationBriefing.IntentHash)) {
		return state.apiState(), fmt.Errorf("core cast confirmation is stale for the current adaptation source or intent")
	}
	st := storepkg.NewStore(s.manifest.OutputDir)
	confirmed, _, err := st.CoreCast.ConfirmCAS(expectedRevision, signature, state.sourceCharacters, state.sourceMajorCharacters, state.sourceResolutionMissing)
	if err != nil {
		return state.apiState(), err
	}
	state.coreCast = &confirmed
	s.saveCoCreateCheckpoint()
	api := state.apiState()
	s.appendCoCreateState(api)
	return api, nil
}

func (s *ProjectSession) ConfirmCharacterCandidate(
	request webCharacterCandidateConfirmRequest,
) (host.CharacterConfirmationResult, error) {
	unlock, err := s.beginAction()
	if err != nil {
		return host.CharacterConfirmationResult{}, err
	}
	defer unlock()
	result, err := host.ConfirmOriginalCharacterCandidate(
		storepkg.NewStore(s.manifest.OutputDir),
		host.CharacterConfirmationRequest{
			ExpectedCandidateRevision: request.ExpectedCandidateRevision,
			CandidateDigest:           request.CandidateDigest,
			IdempotencyKey:            request.IdempotencyKey,
		},
	)
	if err != nil {
		return host.CharacterConfirmationResult{}, err
	}
	st := storepkg.NewStore(s.manifest.OutputDir)
	coreCast, coreCastErr := st.CoreCast.Load()
	if coreCastErr != nil {
		return result, fmt.Errorf("character candidate published but CoreCast mode cannot be read: %w", coreCastErr)
	}
	_, lifecycle, _, workflowErr := tools.CurrentCharacterWorkflow(st)
	adaptationCharacterWorkflow := coreCast != nil && coreCast.Mode == domain.CoreCastModeAdaptation
	if adaptationCharacterWorkflow && workflowErr != nil {
		return result, fmt.Errorf("character candidate published but adaptation Character state is stale: %w", workflowErr)
	}
	if adaptationCharacterWorkflow && (lifecycle == nil || lifecycle.Mode != domain.CharacterCardProjectAdaptation) {
		return result, fmt.Errorf("character candidate published but adaptation Character lifecycle is missing")
	}
	if adaptationCharacterWorkflow {
		workflow, loadErr := st.Adaptation.LoadPlanningWorkflow()
		if loadErr != nil || workflow == nil ||
			workflow.Stage != domain.AdaptationPlanningStageTargetFoundationGenerating {
			return result, fmt.Errorf("character candidate published but adaptation target workflow is not current: %w", loadErr)
		}
		brief, loadErr := st.Adaptation.LoadCharacterBrief()
		if loadErr != nil || brief == nil {
			return result, fmt.Errorf("character candidate published but adaptation character brief is missing: %w", loadErr)
		}
		if _, generateErr := s.host.GenerateAdaptationTargetFoundationContext(context.Background(), adapt.TargetFoundationOptions{
			Brief: brief.Brief, ExpectedWorkflowRevision: workflow.Revision,
		}); generateErr != nil {
			return result, fmt.Errorf("character candidate published but target Foundation generation failed: %w", generateErr)
		}
		s.AppendSnapshot()
		return result, nil
	}
	if _, resumeErr := s.host.Resume(); resumeErr != nil {
		return result, fmt.Errorf("character candidate published but planning resume failed: %w", resumeErr)
	}
	s.AppendSnapshot()
	return result, nil
}

func (s *ProjectSession) EditCharacterCandidate(
	request webCharacterCandidateEditRequest,
) (domain.CharacterCardCandidate, domain.CharacterCardLifecycle, error) {
	unlock, err := s.beginAction()
	if err != nil {
		return domain.CharacterCardCandidate{}, domain.CharacterCardLifecycle{}, err
	}
	defer unlock()
	candidate, lifecycle, err := host.EditOriginalCharacterCandidate(
		storepkg.NewStore(s.manifest.OutputDir),
		host.CharacterCandidateEditRequest{
			ExpectedCandidateRevision: request.ExpectedCandidateRevision,
			Characters:                request.Characters,
			Relationships:             request.PlannedRelationships,
			RelationshipsReviewed:     request.RelationshipsReviewed,
		},
	)
	if err != nil {
		return domain.CharacterCardCandidate{}, domain.CharacterCardLifecycle{}, err
	}
	s.AppendSnapshot()
	return candidate, lifecycle, nil
}

func (s *ProjectSession) UnconfirmCoreCast(expectedRevision int64) (webCoCreateState, error) {
	unlock, err := s.beginAction()
	if err != nil {
		return webCoCreateState{}, err
	}
	defer unlock()
	if s.cocreate == nil || !s.cocreate.supportsLegacyCoreCast() {
		return webCoCreateState{}, fmt.Errorf("normal or adaptation co-create has not started")
	}
	state := s.cocreate
	st := storepkg.NewStore(s.manifest.OutputDir)
	unconfirmed, err := st.CoreCast.UnconfirmCAS(expectedRevision)
	if err != nil {
		return state.apiState(), err
	}
	state.coreCast = &unconfirmed
	s.saveCoCreateCheckpoint()
	api := state.apiState()
	s.appendCoCreateState(api)
	return api, nil
}

func (s *ProjectSession) runAdaptDecisionDraftBatchesLocked(ctx context.Context) (webCoCreateState, error) {
	if s.cocreate == nil {
		return webCoCreateState{}, fmt.Errorf("co-create has not started")
	}
	batches := adaptDecisionDraftBatches(s.cocreate.adaptationBriefing, adaptDecisionDraftBatchSize)
	if len(batches) == 0 {
		return s.runCoCreateLocked(ctx)
	}
	var state webCoCreateState
	for idx, batch := range batches {
		s.appendCoCreateDecisionDraftBatchStarted(idx+1, len(batches))
		instruction := adaptDecisionDraftBatchInstruction(idx+1, len(batches), batch, strings.TrimSpace(s.cocreate.draftPrompt()) != "")
		if err := s.cocreate.appendInternalUser(instruction); err != nil {
			return s.cocreate.apiState(), err
		}
		s.saveCoCreateCheckpoint()
		next, err := s.runCoCreateLocked(ctx)
		state = next
		if err != nil {
			return state, err
		}
	}
	return state, nil
}

func (s *ProjectSession) beginAction() (func(), error) {
	return s.beginActionKind("")
}

func (s *ProjectSession) beginActionKind(kind string) (func(), error) {
	return s.beginActionKindWithNormalFlowLease(kind, true)
}

func (s *ProjectSession) beginActionWithoutNormalFlowLease(kind string) (func(), error) {
	return s.beginActionKindWithNormalFlowLease(kind, false)
}

func (s *ProjectSession) beginActionKindWithNormalFlowLease(kind string, acquireLease bool) (func(), error) {
	kind = strings.TrimSpace(kind)
	s.mu.Lock()
	outputDir := strings.TrimSpace(s.manifest.OutputDir)
	s.mu.Unlock()
	s.actionMu.Lock()
	if s.actionKinds == nil {
		s.actionKinds = make(map[string]int)
	}
	for active := range s.actionKinds {
		if !projectSessionActionsCompatible(kind, active) {
			s.actionMu.Unlock()
			return nil, ErrSessionActionInProgress
		}
	}
	if acquireLease && s.actionRevisionLease == nil && s.actionLeaseRelease == nil {
		owner := "web:" + kind
		if kind == "" {
			owner = "web:action"
		}
		if leaseHost, ok := s.host.(normalFlowActionHost); ok {
			release, err := leaseHost.BeginNormalFlowAction(owner)
			if err != nil {
				s.actionMu.Unlock()
				return nil, err
			}
			s.actionLeaseRelease = release
		} else if outputDir != "" {
			revisions := storepkg.NewRevisionStore(outputDir)
			lease, err := revisions.AcquireNormalFlow(owner)
			if err != nil {
				s.actionMu.Unlock()
				return nil, err
			}
			s.actionRevisionStore = revisions
			s.actionRevisionLease = lease
		}
	}
	s.actionKinds[kind]++
	s.actionMu.Unlock()
	return func() { s.finishActionKind(kind) }, nil
}

func (s *ProjectSession) finishActionKind(kind string) {
	kind = strings.TrimSpace(kind)
	s.actionMu.Lock()
	if s.actionKinds[kind] <= 1 {
		delete(s.actionKinds, kind)
	} else {
		s.actionKinds[kind]--
	}
	if len(s.actionKinds) > 0 {
		s.actionMu.Unlock()
		return
	}
	revisions, lease, release := s.actionRevisionStore, s.actionRevisionLease, s.actionLeaseRelease
	s.actionRevisionStore, s.actionRevisionLease = nil, nil
	s.actionLeaseRelease = nil
	s.actionMu.Unlock()
	if release != nil {
		release()
	}
	if revisions != nil && lease != nil {
		if err := revisions.ReleaseNormalFlow(lease.Token); err != nil {
			slog.Warn("release web action revision fence failed", "module", "web", "err", err)
		}
	}
}

func (s *ProjectSession) beginCancellableAction(parent context.Context, kind string) (context.Context, func(), error) {
	unlock, err := s.beginActionKind(kind)
	if err != nil {
		return nil, nil, err
	}
	if parent == nil {
		parent = context.Background()
	}
	parent, err = s.normalFlowActionContext(parent)
	if err != nil {
		unlock()
		return nil, nil, err
	}
	ctx, cancel := context.WithCancel(parent)
	s.setActionCancel(kind, cancel)
	return ctx, func() {
		s.clearActionCancel()
		cancel()
		unlock()
	}, nil
}

func (s *ProjectSession) normalFlowActionContext(parent context.Context) (context.Context, error) {
	if parent == nil {
		parent = context.Background()
	}
	s.actionMu.Lock()
	revisions, lease := s.actionRevisionStore, s.actionRevisionLease
	hostOwnsLease := s.actionLeaseRelease != nil
	s.actionMu.Unlock()

	if revisions != nil && lease != nil {
		fence, err := revisions.FenceForNormalFlow(lease.Token)
		if err != nil {
			return nil, err
		}
		return storepkg.ContextWithRevisionFence(parent, fence), nil
	}
	if hostOwnsLease {
		actionHost, ok := s.host.(normalFlowActionContextHost)
		if !ok {
			return nil, fmt.Errorf("normal-flow action host cannot provide a revision-fenced context")
		}
		return actionHost.NormalFlowActionContext(parent)
	}
	return nil, fmt.Errorf("normal-flow action revision lease is not active")
}

func (s *ProjectSession) isActionRunning(kind string) bool {
	kind = strings.TrimSpace(kind)
	s.actionMu.Lock()
	defer s.actionMu.Unlock()
	return s.actionKinds[kind] > 0
}

func (s *ProjectSession) currentActionKind() string {
	s.actionCancelMu.Lock()
	defer s.actionCancelMu.Unlock()
	return s.actionKind
}

func (s *ProjectSession) overlayActionSnapshot(snap host.UISnapshot) host.UISnapshot {
	kinds := s.currentActionKinds()
	if len(kinds) == 0 {
		return snap
	}
	snap.IsRunning = true
	snap.RuntimeState = "running"
	snap.StatusLabel = "RUNNING"
	for _, kind := range kinds {
		agent, ok := sessionActionAgentSnapshot(kind)
		if ok {
			snap.Agents = append(snap.Agents, agent)
		}
	}
	return snap
}

func (s *ProjectSession) currentActionKinds() []string {
	s.actionMu.Lock()
	defer s.actionMu.Unlock()
	kinds := make([]string, 0, len(s.actionKinds))
	for kind := range s.actionKinds {
		if strings.TrimSpace(kind) == "" {
			continue
		}
		kinds = append(kinds, kind)
	}
	sort.Strings(kinds)
	return kinds
}

func sessionActionAgentSnapshot(kind string) (host.AgentSnapshot, bool) {
	now := time.Now().UTC()
	switch kind {
	case projectActionKindAdaptationAnalysis:
		return host.AgentSnapshot{
			Name:      "web",
			State:     "running",
			TaskKind:  kind,
			Summary:   "正在分析原文",
			Tool:      "原文分析",
			UpdatedAt: now,
		}, true
	case projectActionKindAdaptationProposal:
		return host.AgentSnapshot{
			Name:      "web",
			State:     "running",
			TaskKind:  kind,
			Summary:   "正在生成改编提案",
			Tool:      "生成改编提案",
			UpdatedAt: now,
		}, true
	case projectActionKindAdaptationRevision:
		return host.AgentSnapshot{
			Name:      "web",
			State:     "running",
			TaskKind:  kind,
			Summary:   "正在修订改编提案",
			Tool:      "修订改编提案",
			UpdatedAt: now,
		}, true
	case projectActionKindSimulationAnalysis:
		return host.AgentSnapshot{
			Name:      "web",
			State:     "running",
			TaskKind:  kind,
			Summary:   "正在分析仿写画像",
			Tool:      "仿写画像分析",
			UpdatedAt: now,
		}, true
	case projectActionKindSimulationImport:
		return host.AgentSnapshot{
			Name:      "web",
			State:     "running",
			TaskKind:  kind,
			Summary:   "正在导入仿写画像",
			Tool:      "导入仿写画像",
			UpdatedAt: now,
		}, true
	case projectActionKindCharacterAnalyze, projectActionKindCharacterReview, projectActionKindCharacterRetry:
		return host.AgentSnapshot{
			Name:      "character",
			State:     "running",
			TaskKind:  kind,
			Summary:   "Character Agent is analyzing or reviewing the workspace",
			Tool:      "character_workspace",
			UpdatedAt: now,
		}, true
	default:
		return host.AgentSnapshot{}, false
	}
}

func projectSessionActionsCompatible(a, b string) bool {
	a = strings.TrimSpace(a)
	b = strings.TrimSpace(b)
	if a == "" || b == "" || a == b {
		return false
	}
	return (a == projectActionKindAdaptationAnalysis && projectSessionSimulationPreparationAction(b)) ||
		(b == projectActionKindAdaptationAnalysis && projectSessionSimulationPreparationAction(a)) ||
		(a == projectActionKindAdaptationUpload && b == projectActionKindAdaptationAnalysis) ||
		(b == projectActionKindAdaptationUpload && a == projectActionKindAdaptationAnalysis) ||
		(a == projectActionKindSimulationUpload && b == projectActionKindSimulationAnalysis) ||
		(b == projectActionKindSimulationUpload && a == projectActionKindSimulationAnalysis)
}

func projectSessionSimulationPreparationAction(kind string) bool {
	switch strings.TrimSpace(kind) {
	case projectActionKindSimulationAnalysis, projectActionKindSimulationImport:
		return true
	default:
		return false
	}
}

func (s *ProjectSession) setActionCancel(kind string, cancel context.CancelFunc) {
	s.actionCancelMu.Lock()
	defer s.actionCancelMu.Unlock()
	s.actionKind = strings.TrimSpace(kind)
	s.actionCancel = cancel
}

func (s *ProjectSession) clearActionCancel() {
	s.actionCancelMu.Lock()
	defer s.actionCancelMu.Unlock()
	s.actionKind = ""
	s.actionCancel = nil
}

func (s *ProjectSession) cancelCurrentAction() (string, bool) {
	s.actionCancelMu.Lock()
	defer s.actionCancelMu.Unlock()
	if s.actionCancel == nil {
		return "", false
	}
	kind := s.actionKind
	cancel := s.actionCancel
	s.actionKind = ""
	s.actionCancel = nil
	cancel()
	return kind, true
}

func (s *ProjectSession) AppendSnapshot() WebEvent {
	progress := s.WorkflowProgress()
	return s.append(WebEvent{
		Type:             webEventTypeSnapshot,
		Snapshot:         compactUISnapshot(s.Snapshot()),
		WorkflowProgress: &progress,
	})
}

func (s *ProjectSession) ServeEvents(ctx context.Context, w http.ResponseWriter, after int64) error {
	heartbeat := time.NewTicker(sseHeartbeatInterval)
	defer heartbeat.Stop()
	return s.serveEvents(ctx, w, after, heartbeat.C)
}

func (s *ProjectSession) serveEvents(
	ctx context.Context,
	w http.ResponseWriter,
	after int64,
	heartbeat <-chan time.Time,
) error {
	flusher, ok := w.(http.Flusher)
	if !ok {
		return fmt.Errorf("response writer does not support streaming")
	}

	w.Header().Set("content-type", "text/event-stream; charset=utf-8")
	w.Header().Set("cache-control", "no-cache")
	w.Header().Set("connection", "keep-alive")
	w.Header().Set("x-accel-buffering", "no")

	s.AppendSnapshot()
	history, ch, unsubscribe := s.Subscribe(after)
	defer unsubscribe()

	for _, ev := range history {
		if err := writeSSEEvent(w, ev); err != nil {
			return err
		}
	}
	flusher.Flush()

	for {
		select {
		case <-ctx.Done():
			return nil
		case ev, ok := <-ch:
			if !ok {
				return nil
			}
			if err := writeSSEEvent(w, ev); err != nil {
				return err
			}
			flusher.Flush()
		case heartbeatTime, ok := <-heartbeat:
			if !ok {
				return nil
			}
			if err := writeSSEHeartbeat(w, heartbeatTime); err != nil {
				return err
			}
			flusher.Flush()
		}
	}
}

func (s *ProjectSession) Subscribe(after int64) ([]WebEvent, <-chan WebEvent, func()) {
	ch := make(chan WebEvent, 256)
	s.mu.Lock()
	if s.closed {
		close(ch)
		s.mu.Unlock()
		return nil, ch, func() {}
	}
	s.subscribers[ch] = struct{}{}
	history := s.historyAfterLocked(after)
	s.mu.Unlock()

	unsubscribe := func() {
		s.mu.Lock()
		if _, ok := s.subscribers[ch]; ok {
			delete(s.subscribers, ch)
			close(ch)
		}
		s.mu.Unlock()
	}
	return history, ch, unsubscribe
}

func (s *ProjectSession) HistoryAfter(after int64) []WebEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.historyAfterLocked(after)
}

func (s *ProjectSession) EventHistory(after int64) WebEventHistory {
	s.mu.Lock()
	defer s.mu.Unlock()
	var oldestSeq, latestSeq int64
	if len(s.history) > 0 {
		oldestSeq = s.history[0].Seq
		latestSeq = s.history[len(s.history)-1].Seq
	}
	return WebEventHistory{
		ProjectID:    s.manifest.ID,
		Events:       s.historyAfterLocked(after),
		OldestSeq:    oldestSeq,
		LatestSeq:    latestSeq,
		HistoryLimit: webEventHistoryLimit,
	}
}

func (s *ProjectSession) WorkbenchEventHistory(after int64) WebEventHistory {
	s.mu.Lock()
	defer s.mu.Unlock()
	var oldestSeq, latestSeq int64
	if len(s.history) > 0 {
		oldestSeq = s.history[0].Seq
		latestSeq = s.history[len(s.history)-1].Seq
	}
	events := make([]WebEvent, 0, len(s.history))
	for _, ev := range s.history {
		if ev.Seq <= after || !isWorkbenchReplayEvent(ev) {
			continue
		}
		events = append(events, ev)
	}
	return WebEventHistory{
		ProjectID:    s.manifest.ID,
		Events:       events,
		OldestSeq:    oldestSeq,
		LatestSeq:    latestSeq,
		HistoryLimit: webEventHistoryLimit,
	}
}

func isWorkbenchReplayEvent(ev WebEvent) bool {
	switch ev.Type {
	case webEventTypeHostEvent, webEventTypeStreamDelta, webEventTypeStreamClear:
		return true
	default:
		return false
	}
}

func (s *ProjectSession) Close() {
	if s.actions != nil {
		s.actions.Close()
	}
	if s.host != nil {
		s.host.Close()
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.closed = true
	for ch := range s.subscribers {
		close(ch)
		delete(s.subscribers, ch)
	}
}

func (s *ProjectSession) seedHistory() error {
	items, err := s.host.ReplayQueue(0)
	if err != nil {
		return err
	}
	items = retainedRuntimeQueueItems(items, webEventHistoryLimit)
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, item := range items {
		if item.Seq > s.nextSeq {
			s.nextSeq = item.Seq
		}
		ev, ok := webEventFromQueueItem(s.manifest.ID, item)
		if !ok {
			continue
		}
		s.upsertLocked(ev)
	}
	return nil
}

func retainedRuntimeQueueItems(items []domain.RuntimeQueueItem, limit int) []domain.RuntimeQueueItem {
	if limit <= 0 || len(items) <= limit {
		return items
	}
	return items[len(items)-limit:]
}

func (s *ProjectSession) pump() {
	events := s.host.Events()
	stream := s.host.Stream()
	done := s.host.Done()
	for events != nil || stream != nil || done != nil {
		select {
		case ev, ok := <-events:
			if !ok {
				events = nil
				continue
			}
			s.appendHostEventFromHost(ev)
			s.AppendSnapshot()
		case delta, ok := <-stream:
			if !ok {
				stream = nil
				continue
			}
			if delta == host.StreamClearSentinel {
				s.appendStreamClear()
			} else {
				s.appendStreamDelta(delta)
			}
		case _, ok := <-done:
			if !ok {
				done = nil
				continue
			}
			s.AppendSnapshot()
		}
	}
}

func (s *ProjectSession) appendHostEvent(ev host.Event) WebEvent {
	if recorder, ok := s.host.(interface{ RecordEvent(host.Event) }); ok {
		recorder.RecordEvent(ev)
		return WebEvent{}
	}
	return s.appendHostEventFromHost(ev)
}

func (s *ProjectSession) appendHostEventFromHost(ev host.Event) WebEvent {
	return s.append(WebEvent{
		Type:        webEventTypeHostEvent,
		HostEventID: strings.TrimSpace(ev.ID),
		Event:       apiHostEventFromHost(ev),
	})
}

func (s *ProjectSession) appendHostEventWithProgress(ev host.Event, current, total int) WebEvent {
	ev.Current = current
	ev.Total = total
	return s.appendHostEvent(ev)
}

func (s *ProjectSession) consumeSimulationEvents(ctx context.Context, events <-chan sim.Event) ([]apiSimulationEvent, error) {
	var out []apiSimulationEvent
	var runErr error
	for {
		select {
		case <-ctx.Done():
			return out, ctx.Err()
		case ev, ok := <-events:
			if !ok {
				return out, runErr
			}
			apiEvent := apiSimulationEventFromSim(ev)
			out = append(out, apiEvent)
			s.appendSimulationEvent(apiEvent)
			if ev.Err != nil && ev.Stage == sim.StageError {
				message := strings.TrimSpace(ev.Message)
				if message == "" {
					message = ev.Err.Error()
				} else {
					message = fmt.Sprintf("%s: %v", message, ev.Err)
				}
				runErr = simulationRunError{message: message}
			}
		}
	}
}

func (s *ProjectSession) consumeImportEvents(ctx context.Context, events <-chan imp.Event) ([]apiImportEvent, error) {
	var out []apiImportEvent
	var runErr error
	for {
		select {
		case <-ctx.Done():
			return out, ctx.Err()
		case ev, ok := <-events:
			if !ok {
				return out, runErr
			}
			apiEvent := apiImportEventFromImp(ev)
			out = append(out, apiEvent)
			s.appendImportEvent(apiEvent)
			if ev.Err != nil {
				message := strings.TrimSpace(ev.Message)
				if message == "" {
					message = ev.Err.Error()
				} else {
					message = fmt.Sprintf("%s: %v", message, ev.Err)
				}
				runErr = importRunError{message: message}
			}
		}
	}
}

func (s *ProjectSession) consumeAdaptationEvents(ctx context.Context, events <-chan adapt.Event) ([]apiAdaptationEvent, error) {
	var out []apiAdaptationEvent
	var runErr error
	for {
		select {
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.Canceled) {
				ev := apiAdaptationEvent{
					Time:    time.Now().UTC(),
					Stage:   string(adapt.StagePaused),
					Message: "原文分析已暂停，可再次点击分析继续",
				}
				out = append(out, ev)
				s.appendAdaptationEvent(ev)
				return out, adaptationPausedError{message: ev.Message}
			}
			return out, ctx.Err()
		case ev, ok := <-events:
			if !ok {
				return out, runErr
			}
			apiEvent := apiAdaptationEventFromAdapt(ev)
			out = append(out, apiEvent)
			s.appendAdaptationEvent(apiEvent)
			if ev.Err != nil && ev.Stage == adapt.StageError {
				message := strings.TrimSpace(ev.Message)
				if message == "" {
					message = ev.Err.Error()
				} else {
					message = fmt.Sprintf("%s: %v", message, ev.Err)
				}
				runErr = adaptationRunError{message: message}
			}
		}
	}
}

func (s *ProjectSession) appendSimulationEvent(ev apiSimulationEvent) WebEvent {
	level := "info"
	if ev.Error != "" && ev.Stage == string(sim.StageError) {
		level = "error"
	} else if ev.Error != "" {
		level = "warn"
	} else if ev.Stage == string(sim.StageDone) {
		level = "success"
	}
	return s.appendHostEvent(host.Event{
		Time:     ev.Time,
		Category: "SIMULATE",
		Agent:    "web",
		Summary:  ev.Message,
		Detail:   ev.Error,
		Kind:     ev.Stage,
		Level:    level,
	})
}

func (s *ProjectSession) appendSimulationActionError(stage sim.Stage, message string, err error) WebEvent {
	if err == nil {
		return s.appendSimulationEvent(apiSimulationEvent{
			Time:    time.Now().UTC(),
			Stage:   string(stage),
			Message: message,
		})
	}
	return s.appendSimulationEvent(apiSimulationEvent{
		Time:    time.Now().UTC(),
		Stage:   string(stage),
		Message: message,
		Error:   err.Error(),
	})
}

func (s *ProjectSession) appendImportEvent(ev apiImportEvent) WebEvent {
	level := "info"
	if ev.Error != "" {
		level = "error"
	} else if ev.Stage == string(imp.StageDone) {
		level = "success"
	}
	return s.appendHostEvent(host.Event{
		Time:     ev.Time,
		Category: "IMPORT",
		Agent:    "web",
		Summary:  ev.Message,
		Detail:   ev.Error,
		Kind:     ev.Stage,
		Level:    level,
	})
}

func (s *ProjectSession) appendAdaptationEvent(ev apiAdaptationEvent) WebEvent {
	level := "info"
	if ev.Error != "" {
		level = "error"
	} else if ev.Stage == string(adapt.StagePaused) {
		level = "warn"
	} else if ev.Stage == string(adapt.StageDone) {
		level = "success"
	}
	return s.appendHostEventWithProgress(host.Event{
		Time:     ev.Time,
		Category: "ADAPT",
		Agent:    "web",
		Summary:  ev.Message,
		Detail:   ev.Error,
		Kind:     ev.Stage,
		Level:    level,
	}, ev.Current, ev.Total)
}

func (s *ProjectSession) appendLibraryEvent(kind, summary, detail, level string) WebEvent {
	level = strings.TrimSpace(level)
	if level == "" {
		level = "info"
	}
	return s.appendHostEvent(host.Event{
		Time:     time.Now().UTC(),
		Category: "LIBRARY",
		Agent:    "web",
		Summary:  strings.TrimSpace(summary),
		Detail:   strings.TrimSpace(detail),
		Kind:     strings.TrimSpace(kind),
		Level:    level,
	})
}

func (s *ProjectSession) appendAdaptationPausedEvent() WebEvent {
	return s.appendAdaptationEvent(apiAdaptationEvent{
		Time:    time.Now().UTC(),
		Stage:   string(adapt.StagePaused),
		Message: "原文分析已暂停，可再次点击分析继续",
	})
}

func (s *ProjectSession) appendAdaptationActionError(stage adapt.Stage, message string, err error) WebEvent {
	if err == nil {
		return s.appendAdaptationEvent(apiAdaptationEvent{
			Time:    time.Now().UTC(),
			Stage:   string(stage),
			Message: message,
		})
	}
	return s.appendAdaptationEvent(apiAdaptationEvent{
		Time:    time.Now().UTC(),
		Stage:   string(stage),
		Message: message,
		Error:   retrypolicy.SanitizeProviderError(err),
	})
}

func (s *ProjectSession) adaptationProposalProgressEmitter() adapt.ProgressEmitter {
	return func(stage adapt.Stage, current int, total int, message string, err error) {
		level := "info"
		detail := ""
		if err != nil {
			detail = retrypolicy.SanitizeProviderError(err)
			if stage == adapt.StageError {
				level = "error"
			} else {
				level = "warn"
			}
		} else if stage == adapt.StageDone {
			level = "success"
		}
		if strings.TrimSpace(message) == "" && detail != "" {
			message = detail
		}
		s.appendHostEventWithProgress(host.Event{
			Time:     time.Now().UTC(),
			Category: "ADAPT",
			Agent:    "web",
			Summary:  message,
			Detail:   detail,
			Kind:     string(stage),
			Level:    level,
		}, current, total)
	}
}

func (s *ProjectSession) appendAdaptationProposalStarted(options adapt.ProposalOptions) (string, time.Time) {
	startedAt := time.Now().UTC()
	eventID := fmt.Sprintf("adapt-proposal-%d", startedAt.UnixNano())
	s.appendHostEvent(host.Event{
		ID:       eventID,
		Time:     startedAt,
		Category: "ADAPT",
		Agent:    "web",
		Summary:  adaptationProposalEventSummary("正在生成改编提案", options),
		Kind:     "proposal",
		Level:    "info",
	})
	return eventID, startedAt
}

func (s *ProjectSession) appendAdaptationProposalPlannerRequested(options adapt.ProposalOptions) {
	s.appendHostEvent(host.Event{
		Time:     time.Now().UTC(),
		Category: "ADAPT",
		Agent:    "web",
		Summary:  adaptationProposalEventSummary("已完成规则归一化，正在请求改编规划模型", options),
		Detail:   "planner request may take a few minutes for long free/full rewrite proposals",
		Kind:     "proposal",
		Level:    "info",
	})
}

func (s *ProjectSession) appendAdaptationProposalFinished(eventID string, startedAt time.Time, options adapt.ProposalOptions, err error) {
	if eventID == "" {
		return
	}
	finishedAt := time.Now().UTC()
	summary := adaptationProposalEventSummary("改编提案已生成", options)
	level := "success"
	var detail string
	var failed bool
	if err != nil {
		summary = adaptationProposalEventSummary("改编提案生成失败", options)
		level = "error"
		detail = retrypolicy.SanitizeProviderError(err)
		failed = true
	}
	s.appendHostEvent(host.Event{
		ID:         eventID,
		Time:       startedAt,
		FinishedAt: finishedAt,
		Failed:     failed,
		Category:   "ADAPT",
		Agent:      "web",
		Summary:    summary,
		Detail:     detail,
		Kind:       "proposal",
		Level:      level,
		Duration:   finishedAt.Sub(startedAt),
	})
}

func (s *ProjectSession) appendAdaptationProposalRevisionStarted(options adapt.ProposalRevisionOptions) (string, time.Time) {
	startedAt := time.Now().UTC()
	eventID := fmt.Sprintf("adapt-proposal-revision-%d", startedAt.UnixNano())
	s.appendHostEvent(host.Event{
		ID:       eventID,
		Time:     startedAt,
		Category: "ADAPT",
		Agent:    "web",
		Summary:  adaptationProposalRevisionEventSummary("正在修订改编提案", options),
		Kind:     "proposal_revision",
		Level:    "info",
	})
	return eventID, startedAt
}

func (s *ProjectSession) appendAdaptationProposalRevisionFinished(eventID string, startedAt time.Time, options adapt.ProposalRevisionOptions, err error) {
	if eventID == "" {
		return
	}
	finishedAt := time.Now().UTC()
	summary := adaptationProposalRevisionEventSummary("改编提案修订已完成", options)
	level := "success"
	var detail string
	var failed bool
	if err != nil {
		summary = adaptationProposalRevisionEventSummary("改编提案修订失败", options)
		level = "error"
		detail = retrypolicy.SanitizeProviderError(err)
		failed = true
	}
	s.appendHostEvent(host.Event{
		ID:         eventID,
		Time:       startedAt,
		FinishedAt: finishedAt,
		Failed:     failed,
		Category:   "ADAPT",
		Agent:      "web",
		Summary:    summary,
		Detail:     detail,
		Kind:       "proposal_revision",
		Level:      level,
		Duration:   finishedAt.Sub(startedAt),
	})
}

func adaptationProposalEventSummary(action string, options adapt.ProposalOptions) string {
	mode := strings.TrimSpace(options.Granularity)
	if mode == "" {
		return action
	}
	return fmt.Sprintf("%s（%s）", action, mode)
}

func adaptationProposalRevisionEventSummary(action string, options adapt.ProposalRevisionOptions) string {
	target := strings.TrimSpace(options.Target)
	if target == "" {
		if options.VolumeIndex < 0 {
			target = "全卷"
		} else if options.VolumeIndex > 0 {
			target = fmt.Sprintf("卷 %d", options.VolumeIndex)
		} else if options.FromChapter > 0 && options.ToChapter > 0 && options.FromChapter != options.ToChapter {
			target = fmt.Sprintf("第 %d-%d 章", options.FromChapter, options.ToChapter)
		} else if options.FromChapter > 0 {
			target = fmt.Sprintf("第 %d 章", options.FromChapter)
		}
	}
	if target == "" {
		return action
	}
	return fmt.Sprintf("%s（%s）", action, target)
}

func (s *ProjectSession) appendActionCanceledEvent(kind string) WebEvent {
	summary := "当前操作已请求暂停"
	if kind == projectActionKindAdaptationAnalysis {
		summary = "原文分析已请求暂停"
	} else if kind == projectActionKindAdaptationProposal {
		summary = "改编提案生成已请求暂停"
	} else if kind == projectActionKindAdaptationRevision {
		summary = "改编提案修订已请求暂停"
	}
	return s.appendHostEvent(host.Event{
		Time:     time.Now().UTC(),
		Category: "SYSTEM",
		Agent:    "web",
		Summary:  summary,
		Kind:     "paused",
		Level:    "warn",
	})
}

func (s *ProjectSession) appendCoCreateRunStarted(kind string) (string, time.Time) {
	startedAt := time.Now().UTC()
	eventID := fmt.Sprintf("cocreate-%s-%d", coCreateEventKind(kind), startedAt.UnixNano())
	s.appendHostEvent(host.Event{
		ID:       eventID,
		Time:     startedAt,
		Category: "COCREATE",
		Agent:    "web",
		Summary:  coCreateRunningSummary(kind),
		Kind:     coCreateEventKind(kind),
		Level:    "info",
	})
	return eventID, startedAt
}

func (s *ProjectSession) appendCoCreateDecisionDraftBatchStarted(index, total int) WebEvent {
	startedAt := time.Now().UTC()
	return s.appendHostEvent(host.Event{
		ID:       fmt.Sprintf("cocreate-adapt-draft-batch-%d-%d", index, startedAt.UnixNano()),
		Time:     startedAt,
		Category: "COCREATE",
		Agent:    "web",
		Summary:  fmt.Sprintf("改编共创：正在分批生成首次 draft（第 %d/%d 批）", index, total),
		Detail:   fmt.Sprintf("confirmed_decision_batch=%d/%d", index, total),
		Kind:     webCoCreateKindAdapt,
		Level:    "info",
	})
}

func (s *ProjectSession) appendCoCreateBriefingStarted() (string, time.Time) {
	startedAt := time.Now().UTC()
	eventID := fmt.Sprintf("cocreate-briefing-%d", startedAt.UnixNano())
	s.appendHostEvent(host.Event{
		ID:       eventID,
		Time:     startedAt,
		Category: "COCREATE",
		Agent:    "web",
		Summary:  "改编共创：正在生成前置摘要",
		Kind:     "adapt_briefing",
		Level:    "info",
	})
	return eventID, startedAt
}

func (s *ProjectSession) appendCoCreateBriefingFinished(eventID string, startedAt time.Time, runErr error) {
	if eventID == "" {
		return
	}
	finishedAt := time.Now().UTC()
	failed := runErr != nil
	level := "success"
	summary := "改编共创：前置摘要已生成"
	detail := ""
	if failed {
		level = "error"
		summary = "改编共创：前置摘要生成失败"
		detail = retrypolicy.SanitizeProviderError(runErr)
	}
	s.appendHostEvent(host.Event{
		ID:         eventID,
		Time:       startedAt,
		FinishedAt: finishedAt,
		Failed:     failed,
		Category:   "COCREATE",
		Agent:      "web",
		Summary:    summary,
		Detail:     detail,
		Kind:       "adapt_briefing",
		Level:      level,
		Duration:   finishedAt.Sub(startedAt),
	})
}

func (s *ProjectSession) appendCoCreateRunFinished(eventID string, startedAt time.Time, state webCoCreateState, runErr error) {
	if eventID == "" {
		return
	}
	finishedAt := time.Now().UTC()
	failed := runErr != nil
	level := "success"
	summary := coCreateFinishedSummary(state.Kind, state.Ready)
	detail := ""
	if failed {
		level = "error"
		summary = coCreateKindLabel(state.Kind) + "失败"
		detail = retrypolicy.SanitizeProviderError(runErr)
	}
	s.appendHostEvent(host.Event{
		ID:         eventID,
		Time:       startedAt,
		FinishedAt: finishedAt,
		Failed:     failed,
		Category:   "COCREATE",
		Agent:      "web",
		Summary:    summary,
		Detail:     detail,
		Kind:       coCreateEventKind(state.Kind),
		Level:      level,
		Duration:   finishedAt.Sub(startedAt),
	})
}

func coCreateEventKind(kind string) string {
	switch strings.TrimSpace(kind) {
	case webCoCreateKindStage:
		return webCoCreateKindStage
	case webCoCreateKindAdapt:
		return webCoCreateKindAdapt
	case webCoCreateKindContinuation:
		return webCoCreateKindContinuation
	default:
		return webCoCreateKindNormal
	}
}

func coCreateKindLabel(kind string) string {
	switch coCreateEventKind(kind) {
	case webCoCreateKindStage:
		return "阶段共创"
	case webCoCreateKindAdapt:
		return "改编共创"
	case webCoCreateKindContinuation:
		return "续写共创"
	default:
		return "普通共创"
	}
}

func coCreateRunningSummary(kind string) string {
	return coCreateKindLabel(kind) + "：AI 正在整理回复"
}

func coCreateFinishedSummary(kind string, ready bool) string {
	label := coCreateKindLabel(kind)
	if ready {
		return label + "：方向已就绪"
	}
	return label + "：AI 已回复，等待补充"
}

func (s *ProjectSession) appendStreamDelta(delta string) WebEvent {
	return s.append(WebEvent{
		Type: webEventTypeStreamDelta,
		Stream: &APIStreamEvent{
			Text: delta,
		},
	})
}

func (s *ProjectSession) appendStreamClear() WebEvent {
	return s.append(WebEvent{
		Type:   webEventTypeStreamClear,
		Stream: &APIStreamEvent{Clear: true},
	})
}

func (s *ProjectSession) appendCoCreateState(state webCoCreateState) WebEvent {
	progress := s.WorkflowProgress()
	return s.append(WebEvent{
		Type:             webEventTypeCoCreate,
		CoCreate:         &state,
		WorkflowProgress: &progress,
	})
}

func (s *ProjectSession) append(ev WebEvent) WebEvent {
	if ev.Time.IsZero() {
		ev.Time = time.Now().UTC()
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if ev.ProjectID == "" {
		ev.ProjectID = s.manifest.ID
	}
	if s.closed {
		return ev
	}
	s.nextSeq++
	ev.Seq = s.nextSeq
	if ev.Type != webEventTypeStreamDelta && ev.Type != webEventTypeStreamClear {
		if err := s.persistSequenceLocked(); err != nil {
			slog.Error("persist web event sequence failed", "module", "web", "project", s.manifest.ID, "seq", ev.Seq, "err", err)
		}
	}
	s.upsertLocked(ev)
	for ch := range s.subscribers {
		select {
		case ch <- ev:
		default:
		}
	}
	return ev
}

func (s *ProjectSession) loadPersistedSequence() error {
	if strings.TrimSpace(s.sequencePath) == "" {
		return nil
	}
	data, err := os.ReadFile(s.sequencePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var sequence int64
	if err := json.Unmarshal(data, &sequence); err != nil {
		return err
	}
	if sequence > s.nextSeq {
		s.nextSeq = sequence
	}
	return nil
}

func (s *ProjectSession) persistSequenceLocked() error {
	if strings.TrimSpace(s.sequencePath) == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(s.sequencePath), 0o755); err != nil {
		return err
	}
	data, err := json.Marshal(s.nextSeq)
	if err != nil {
		return err
	}
	tmp := s.sequencePath + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.sequencePath)
}

func (s *ProjectSession) upsertLocked(ev WebEvent) {
	if ev.Type == webEventTypeHostEvent && ev.HostEventID != "" {
		if idx, ok := s.hostEventAt[ev.HostEventID]; ok {
			s.history = append(s.history[:idx], s.history[idx+1:]...)
			s.rebuildHostEventIndexLocked()
		}
	}
	s.history = append(s.history, ev)
	if ev.Type == webEventTypeHostEvent && ev.HostEventID != "" {
		s.hostEventAt[ev.HostEventID] = len(s.history) - 1
	}
	s.trimHistoryLocked()
}

func (s *ProjectSession) trimHistoryLocked() {
	if len(s.history) <= webEventHistoryLimit {
		return
	}
	s.history = append([]WebEvent(nil), s.history[len(s.history)-webEventHistoryLimit:]...)
	s.rebuildHostEventIndexLocked()
}

func (s *ProjectSession) rebuildHostEventIndexLocked() {
	s.hostEventAt = make(map[string]int)
	for i, ev := range s.history {
		if ev.Type == webEventTypeHostEvent && ev.HostEventID != "" {
			s.hostEventAt[ev.HostEventID] = i
		}
	}
}

func (s *ProjectSession) historyAfterLocked(after int64) []WebEvent {
	out := make([]WebEvent, 0, len(s.history))
	for _, ev := range s.history {
		if ev.Seq > after {
			out = append(out, ev)
		}
	}
	return out
}

type WebEvent struct {
	Seq                int64                    `json:"seq"`
	Type               string                   `json:"type"`
	ProjectID          string                   `json:"project_id"`
	Time               time.Time                `json:"time"`
	HostEventID        string                   `json:"host_event_id,omitempty"`
	Event              *APIHostEvent            `json:"event,omitempty"`
	Stream             *APIStreamEvent          `json:"stream,omitempty"`
	Snapshot           any                      `json:"snapshot,omitempty"`
	CoCreate           *webCoCreateState        `json:"cocreate,omitempty"`
	WorkflowProgress   *WorkflowProgress        `json:"workflow_progress,omitempty"`
	ManuscriptMutation *ManuscriptMutationEvent `json:"manuscript_mutation,omitempty"`
}

// ManuscriptMutationEvent is the stable, machine-readable projection consumed
// by manuscript workspaces. It deliberately does not infer mutations from
// human-facing host-event summaries or details.
type ManuscriptMutationEvent struct {
	Scope    string `json:"scope"`
	StableID string `json:"stable_id"`
}

func (s *ProjectSession) appendManuscriptMutation(scope, stableID string) WebEvent {
	return s.append(WebEvent{
		Type: webEventTypeAction,
		ManuscriptMutation: &ManuscriptMutationEvent{
			Scope: strings.TrimSpace(scope), StableID: strings.TrimSpace(stableID),
		},
	})
}

type WebEventHistory struct {
	ProjectID    string     `json:"project_id"`
	Events       []WebEvent `json:"events"`
	OldestSeq    int64      `json:"oldest_seq"`
	LatestSeq    int64      `json:"latest_seq"`
	HistoryLimit int        `json:"history_limit"`
}

type APIHostEvent struct {
	ID             string     `json:"id,omitempty"`
	Time           time.Time  `json:"time"`
	FinishedAt     *time.Time `json:"finished_at,omitempty"`
	Failed         bool       `json:"failed,omitempty"`
	Category       string     `json:"category,omitempty"`
	Agent          string     `json:"agent,omitempty"`
	Summary        string     `json:"summary,omitempty"`
	Detail         string     `json:"detail,omitempty"`
	Kind           string     `json:"kind,omitempty"`
	Level          string     `json:"level,omitempty"`
	Depth          int        `json:"depth,omitempty"`
	DurationMillis int64      `json:"duration_ms,omitempty"`
	Running        bool       `json:"running"`
	Current        int        `json:"current,omitempty"`
	Total          int        `json:"total,omitempty"`
}

type APIStreamEvent struct {
	Text  string `json:"text,omitempty"`
	Clear bool   `json:"clear,omitempty"`
}

func apiHostEventFromHost(ev host.Event) *APIHostEvent {
	if ev.Time.IsZero() {
		ev.Time = time.Now().UTC()
	}
	api := &APIHostEvent{
		ID:             ev.ID,
		Time:           ev.Time,
		Failed:         ev.Failed,
		Category:       ev.Category,
		Agent:          ev.Agent,
		Summary:        ev.Summary,
		Detail:         ev.Detail,
		Kind:           ev.Kind,
		Level:          ev.Level,
		Depth:          ev.Depth,
		DurationMillis: ev.Duration.Milliseconds(),
		Current:        ev.Current,
		Total:          ev.Total,
		Running:        ev.Running(),
	}
	if !ev.FinishedAt.IsZero() {
		finished := ev.FinishedAt
		api.FinishedAt = &finished
	}
	return api
}

func webEventFromQueueItem(projectID string, item domain.RuntimeQueueItem) (WebEvent, bool) {
	base := WebEvent{
		Seq:       item.Seq,
		ProjectID: projectID,
		Time:      item.Time,
	}
	switch item.Kind {
	case domain.RuntimeQueueUIEvent:
		api := apiHostEventFromQueueItem(item)
		base.Type = webEventTypeHostEvent
		base.HostEventID = api.ID
		base.Event = api
		return base, true
	case domain.RuntimeQueueStreamDelta:
		text := host.ReplayDeltaText(item)
		if text == "" {
			return WebEvent{}, false
		}
		base.Type = webEventTypeStreamDelta
		base.Stream = &APIStreamEvent{Text: text}
		return base, true
	case domain.RuntimeQueueStreamClear:
		base.Type = webEventTypeStreamClear
		base.Stream = &APIStreamEvent{Clear: true}
		return base, true
	default:
		return WebEvent{}, false
	}
}

func apiHostEventFromQueueItem(item domain.RuntimeQueueItem) *APIHostEvent {
	ev := host.Event{
		Time:     item.Time,
		Category: item.Category,
		Summary:  item.Summary,
	}
	if item.Payload != nil {
		if data, err := json.Marshal(item.Payload); err == nil {
			_ = json.Unmarshal(data, &ev)
		}
	}
	if ev.Time.IsZero() {
		ev.Time = item.Time
	}
	if ev.Category == "" {
		ev.Category = item.Category
	}
	if ev.Summary == "" {
		ev.Summary = item.Summary
	}
	return apiHostEventFromHost(ev)
}

func writeSSEEvent(w http.ResponseWriter, ev WebEvent) error {
	data, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "id: %d\n", ev.Seq); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "event: %s\n", ev.Type); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "data: %s\n\n", data); err != nil {
		return err
	}
	return nil
}

func writeSSEHeartbeat(w io.Writer, heartbeatTime time.Time) error {
	data, err := json.Marshal(struct {
		Time time.Time `json:"time"`
	}{Time: heartbeatTime})
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "event: heartbeat\ndata: %s\n\n", data)
	return err
}
