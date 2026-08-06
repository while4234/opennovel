package tools

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"sort"
	"strings"
	"sync"

	"github.com/voocel/agentcore/schema"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/errs"
	"github.com/voocel/ainovel-cli/internal/rules"
	"github.com/voocel/ainovel-cli/internal/store"
)

type CharacterRunMode string

const (
	CharacterRunAnalyze CharacterRunMode = "analyze"
	CharacterRunReview  CharacterRunMode = "review"
)

const (
	characterContextReportLimit  = 4
	characterContextListLimit    = 12
	characterContextTextLimit    = 600
	characterContextFactLimit    = 3
	characterContextAliasLimit   = 6
	characterContextMaxBytes     = 40 * 1024
	characterContextItemMaxBytes = 16 * 1024
)

// CharacterRunRegistry binds every run ID to one mode and to the exact
// evidence snapshot returned by character_context. It is shared by all three
// Character Agent tools and survives model failover within the live Agent.
type CharacterRunRegistry struct {
	mu   sync.Mutex
	runs map[string]characterRunState
}

type characterRunState struct {
	Mode            CharacterRunMode
	Context         domain.CharacterCardBinding
	Attempt         int
	Submitted       bool
	Tool            string
	EvidenceDigest  string
	TotalPages      int
	NextPage        int
	ContextComplete bool
}

func NewCharacterRunRegistry() *CharacterRunRegistry {
	return &CharacterRunRegistry{runs: make(map[string]characterRunState)}
}

func (r *CharacterRunRegistry) bindContextPageAttempt(
	runID string,
	mode CharacterRunMode,
	binding domain.CharacterCardBinding,
	attempt int,
	evidenceDigest string,
	page int,
	totalPages int,
) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	state, exists := r.runs[runID]
	if exists && state.Mode != mode {
		return fmt.Errorf("character run %q is already bound to mode %q: %w", runID, state.Mode, errs.ErrToolConflict)
	}
	if exists && attempt > state.Attempt {
		state = characterRunState{}
	}
	if exists && attempt < state.Attempt {
		return fmt.Errorf("character run %q attempt %d is stale: %w", runID, attempt, errs.ErrToolConflict)
	}
	if state.Submitted {
		return fmt.Errorf("character run %q already submitted through %s: %w", runID, state.Tool, errs.ErrToolConflict)
	}
	if totalPages <= 0 || page < 0 || page >= totalPages {
		return fmt.Errorf("character run %q has invalid context page %d/%d: %w", runID, page, totalPages, errs.ErrToolArgs)
	}
	if exists && attempt == state.Attempt && state.EvidenceDigest != "" {
		if !sameCharacterBinding(state.Context, binding) ||
			state.EvidenceDigest != evidenceDigest || state.TotalPages != totalPages {
			return fmt.Errorf("character run %q context snapshot changed while paging: %w", runID, errs.ErrToolConflict)
		}
	}
	if page != state.NextPage {
		return fmt.Errorf("character run %q must read context page %d next, got %d: %w", runID, state.NextPage, page, errs.ErrToolConflict)
	}
	state.Mode = mode
	state.Context = binding
	state.Attempt = attempt
	state.EvidenceDigest = evidenceDigest
	state.TotalPages = totalPages
	state.NextPage = page + 1
	state.ContextComplete = state.NextPage == totalPages
	r.runs[runID] = state
	return nil
}

func (r *CharacterRunRegistry) requireSubmission(
	runID string,
	mode CharacterRunMode,
	tool string,
	expected domain.CharacterCardBinding,
) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	state, exists := r.runs[runID]
	if !exists {
		return fmt.Errorf("character run %q must call character_context before %s: %w", runID, tool, errs.ErrToolPrecondition)
	}
	if state.Mode != mode {
		return fmt.Errorf("character run %q is mode %q, not %q: %w", runID, state.Mode, mode, errs.ErrToolConflict)
	}
	if state.Submitted {
		return fmt.Errorf("character run %q already submitted through %s: %w", runID, state.Tool, errs.ErrToolConflict)
	}
	if !state.ContextComplete {
		return fmt.Errorf("character run %q must read all %d character_context pages before %s: %w", runID, state.TotalPages, tool, errs.ErrToolPrecondition)
	}
	if !sameCharacterBinding(state.Context, expected) {
		return fmt.Errorf("character run %q evidence snapshot is stale; call character_context in a new run: %w", runID, errs.ErrToolConflict)
	}
	return nil
}

func (r *CharacterRunRegistry) markSubmitted(runID, tool string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	state := r.runs[runID]
	state.Submitted = true
	state.Tool = tool
	r.runs[runID] = state
}

func sameCharacterBinding(left, right domain.CharacterCardBinding) bool {
	return left.Candidate == right.Candidate && left.InputDigest == right.InputDigest
}

type characterToolBase struct {
	store    *store.Store
	registry *CharacterRunRegistry
}

func newCharacterToolBase(st *store.Store, registry *CharacterRunRegistry) characterToolBase {
	if registry == nil {
		registry = NewCharacterRunRegistry()
	}
	return characterToolBase{store: st, registry: registry}
}

type CharacterContextTool struct {
	characterToolBase
}

func NewCharacterContextTool(st *store.Store, registry *CharacterRunRegistry) *CharacterContextTool {
	return &CharacterContextTool{characterToolBase: newCharacterToolBase(st, registry)}
}

func (t *CharacterContextTool) Name() string { return "character_context" }
func (t *CharacterContextTool) Description() string {
	return "Read the bounded, current character evidence packet for exactly one analyze or review run. " +
		"It returns the Foundation revision/audit, candidate digest, input digest, current candidate, user constraints, " +
		"and adaptation-only structured source evidence without raw source chapters. When next_cursor is present, " +
		"call character_context again with that exact cursor until context_page.complete is true before saving."
}
func (t *CharacterContextTool) Label() string                        { return "读取角色证据" }
func (t *CharacterContextTool) ReadOnly(json.RawMessage) bool        { return true }
func (t *CharacterContextTool) ConcurrencySafe(json.RawMessage) bool { return true }
func (t *CharacterContextTool) StrictSchema() bool                   { return true }
func (t *CharacterContextTool) Schema() map[string]any {
	return schema.Object(
		schema.Property("run_id", schema.String("Stable non-empty ID for this Character Agent run")).Required(),
		schema.Property("mode", schema.Enum("Single responsibility for this run", string(CharacterRunAnalyze), string(CharacterRunReview))).Required(),
		schema.Property("cursor", schema.String("Opaque next_cursor from the immediately preceding page; omit for the first page")),
	)
}

type characterContextArgs struct {
	RunID  string           `json:"run_id"`
	Mode   CharacterRunMode `json:"mode"`
	Cursor string           `json:"cursor,omitempty"`
}

func (t *CharacterContextTool) Execute(_ context.Context, args json.RawMessage) (json.RawMessage, error) {
	var request characterContextArgs
	if err := decodeCharacterToolArgs(args, &request); err != nil {
		return nil, err
	}
	if err := validateCharacterRunIdentity(request.RunID, request.Mode); err != nil {
		return nil, err
	}
	packet, binding, err := buildCharacterContextEvidence(t.store, request.RunID, request.Mode)
	if err != nil {
		return nil, err
	}
	if pending, pendingErr := t.store.Cast.PendingPromotions(); pendingErr == nil && len(pending) > 0 {
		packet["cast_promotion"] = map[string]any{
			"entry":       pending[0],
			"instruction": "Use the cast-promotion save tool for this incremental task; preserve every existing canonical character and relationship.",
		}
		if workflow, workflowErr := t.store.Cast.LoadPromotionWorkflow(); workflowErr == nil && workflow != nil {
			packet["cast_promotion_workflow"] = workflow
		}
	}
	attempt := 0
	if workspaceRun, loadErr := t.store.CharacterWorkspace.Load(request.RunID); loadErr != nil {
		return nil, fmt.Errorf("load Character workspace attempt: %w", loadErr)
	} else if workspaceRun != nil {
		attempt = workspaceRun.Attempt
	}
	packet["run_id"] = strings.TrimSpace(request.RunID)
	packet["mode"] = request.Mode
	page, pageState, err := buildCharacterContextPage(packet, request, binding, attempt)
	if err != nil {
		return nil, err
	}
	if err := t.registry.bindContextPageAttempt(
		request.RunID,
		request.Mode,
		binding,
		attempt,
		pageState.EvidenceDigest,
		pageState.Index,
		pageState.Total,
	); err != nil {
		return nil, err
	}
	return json.Marshal(page)
}

type SaveCharacterCandidateTool struct {
	characterToolBase
}

func NewSaveCharacterCandidateTool(st *store.Store, registry *CharacterRunRegistry) *SaveCharacterCandidateTool {
	return &SaveCharacterCandidateTool{characterToolBase: newCharacterToolBase(st, registry)}
}

func (t *SaveCharacterCandidateTool) Name() string { return "save_character_candidate" }
func (t *SaveCharacterCandidateTool) Description() string {
	return "Analyze-mode only. Atomically stages characters and planned relationships without changing canonical StoryFoundation, " +
		"then stores the signature-bound CharacterCard candidate lifecycle, completeness, CoreCast projection, rationale, and source mappings."
}
func (t *SaveCharacterCandidateTool) Label() string                        { return "保存角色候选" }
func (t *SaveCharacterCandidateTool) ReadOnly(json.RawMessage) bool        { return false }
func (t *SaveCharacterCandidateTool) ConcurrencySafe(json.RawMessage) bool { return false }
func (t *SaveCharacterCandidateTool) StrictSchema() bool                   { return true }
func (t *SaveCharacterCandidateTool) Schema() map[string]any {
	return schema.Object(
		characterRunProperties(CharacterRunAnalyze)...,
	)
}

func characterRunProperties(mode CharacterRunMode) []schema.Prop {
	common := []schema.Prop{
		schema.Property("run_id", schema.String("Stable non-empty Character Agent run ID")).Required(),
		schema.Property("mode", schema.Enum("Single run mode", string(mode))).Required(),
		schema.Property("idempotency_key", schema.String("Non-empty key stable across retries of the same submission")).Required(),
		schema.Property("base_revision", schema.Int("Foundation revision returned by character_context")).Required(),
		schema.Property("base_audit_signature", schema.String("Foundation audit signature returned by character_context")).Required(),
		schema.Property("candidate_digest", schema.String("Character content digest returned by character_context")).Required(),
		schema.Property("input_digest", schema.String("Applicable input digest returned by character_context")).Required(),
	}
	if mode == CharacterRunReview {
		return append(common,
			schema.Property("verdict", schema.Enum("Requested review verdict", "pass", "needs_revision")).Required(),
			schema.Property("summary", schema.String("Evidence-grounded review summary")).Required(),
			schema.Property("findings", schema.Array("Structured review findings", characterFindingSchema())).Required(),
		)
	}
	return append(common,
		schema.Property("analysis_summary", schema.String("Compact generation rationale and uncertain decisions")).Required(),
		schema.Property("characters", schema.Array("Unified original/adaptation character cards", characterSchema())).Required(),
		schema.Property("relationships", schema.Array("Complete planned relationships", characterRelationshipSchema())).Required(),
		schema.Property("relationships_reviewed", schema.Bool("True when absence of additional relationships was explicitly reviewed")).Required(),
		schema.Property("source_mappings", schema.Array("Adaptation source mappings; [] for original projects", characterSourceMappingSchema())).Required(),
	)
}

type saveCharacterCandidateArgs struct {
	RunID                 string                          `json:"run_id"`
	Mode                  CharacterRunMode                `json:"mode"`
	IdempotencyKey        string                          `json:"idempotency_key"`
	BaseRevision          int64                           `json:"base_revision"`
	BaseAuditSignature    string                          `json:"base_audit_signature"`
	CandidateDigest       string                          `json:"candidate_digest"`
	InputDigest           string                          `json:"input_digest"`
	AnalysisSummary       string                          `json:"analysis_summary"`
	Characters            []domain.Character              `json:"characters"`
	Relationships         []domain.CharacterRelationship  `json:"relationships"`
	RelationshipsReviewed bool                            `json:"relationships_reviewed"`
	SourceMappings        []domain.CharacterSourceMapping `json:"source_mappings"`
}

func (t *SaveCharacterCandidateTool) Execute(_ context.Context, args json.RawMessage) (json.RawMessage, error) {
	var request saveCharacterCandidateArgs
	if err := decodeCharacterToolArgs(args, &request); err != nil {
		return nil, err
	}
	if err := validateCharacterRunIdentity(request.RunID, request.Mode); err != nil {
		return nil, err
	}
	if request.Mode != CharacterRunAnalyze {
		return nil, fmt.Errorf("save_character_candidate requires mode=analyze: %w", errs.ErrToolConflict)
	}
	if err := validateCharacterSubmissionIdentity(request.IdempotencyKey, request.BaseRevision, request.BaseAuditSignature, request.CandidateDigest, request.InputDigest); err != nil {
		return nil, err
	}
	current, binding, inputs, projectMode, coreCast, err := currentCharacterBinding(t.store)
	if err != nil {
		return nil, err
	}
	candidate := domain.CloneStoryFoundation(current)
	candidate.Characters = request.Characters
	candidate.Relationships = request.Relationships
	candidate.RelationshipsReviewed = request.RelationshipsReviewed
	normalized, err := domain.NormalizeStoryFoundation(candidate)
	if err != nil {
		return nil, fmt.Errorf("normalize character candidate: %w: %w", errs.ErrToolArgs, err)
	}
	submissionDigest, err := characterSubmissionDigest(request)
	if err != nil {
		return nil, err
	}
	existingCandidate, err := t.store.CharacterCards.LoadCandidate()
	if err != nil {
		return nil, fmt.Errorf("load staged character candidate before save: %w", err)
	}
	if existingCandidate != nil {
		existingBinding, bindErr := domain.CharacterCardBindingFromFoundation(existingCandidate.Foundation, inputs)
		if bindErr == nil {
			existing, loadErr := t.store.CharacterCards.Load(existingBinding)
			if loadErr != nil {
				return nil, fmt.Errorf("load character lifecycle before candidate retry: %w", loadErr)
			}
			if characterCandidateRetryMatches(existing, request, normalized, existingBinding, submissionDigest) {
				return characterCandidateResult(*existing, existingBinding, true)
			}
		}
	}
	if err := requireCharacterBinding(request.BaseRevision, request.BaseAuditSignature, request.CandidateDigest, request.InputDigest, binding); err != nil {
		return nil, err
	}
	if err := t.registry.requireSubmission(request.RunID, request.Mode, t.Name(), binding); err != nil {
		return nil, err
	}
	if projectMode == domain.CharacterCardProjectOriginal && len(request.SourceMappings) != 0 {
		return nil, fmt.Errorf("original character candidate must not fabricate source mappings: %w", errs.ErrToolArgs)
	}
	if projectMode == domain.CharacterCardProjectAdaptation {
		index, indexErr := buildAdaptationSourceCharacterIndex(t.store, coreCast)
		if indexErr != nil {
			return nil, indexErr
		}
		sourceIDs := adaptationSourceCharacterIDs(index)
		targetIDs := foundationCharacterIDs(normalized)
		if err := domain.ValidateCharacterSourceCoverage(request.SourceMappings, sourceIDs, targetIDs); err != nil {
			return nil, fmt.Errorf("adaptation character source coverage: %w: %w", errs.ErrToolArgs, err)
		}
	}
	projected, projectionFindings, err := domain.ProjectCharacterCandidateCoreCast(normalized, coreCast)
	if err != nil {
		return nil, fmt.Errorf("project character candidate core cast: %w", err)
	}
	if err := bindCharacterCoreCastProjection(t.store, &projected, projectMode); err != nil {
		return nil, err
	}
	completeness, err := domain.EvaluateCharacterCardCompleteness(normalized, &projected)
	if err != nil {
		return nil, fmt.Errorf("evaluate character candidate completeness: %w", err)
	}
	var coverage *domain.AdaptationCharacterCoverage
	if projectMode == domain.CharacterCardProjectAdaptation {
		index, indexErr := buildAdaptationSourceCharacterIndex(t.store, coreCast)
		if indexErr != nil {
			return nil, indexErr
		}
		evaluated, coverageErr := domain.EvaluateAdaptationCharacterCoverage(index, request.SourceMappings)
		if coverageErr != nil {
			return nil, fmt.Errorf("evaluate adaptation character coverage: %w", coverageErr)
		}
		if eligibilityErr := domain.ValidateAdaptationCharacterCardEligibility(
			index,
			request.SourceMappings,
			normalized.Characters,
		); eligibilityErr != nil {
			return nil, fmt.Errorf("adaptation formal character eligibility: %w: %w", errs.ErrToolArgs, eligibilityErr)
		}
		coverage = &evaluated
	}
	expectedCandidateRevision := int64(0)
	if existingCandidate != nil {
		expectedCandidateRevision = existingCandidate.Revision
	}
	staged, err := t.store.CharacterCards.SaveCandidateCAS(domain.CharacterCardCandidate{
		Version:       domain.CharacterCardCandidateVersion,
		Base:          binding,
		Foundation:    normalized,
		ProjectedCast: projected,
	}, expectedCandidateRevision)
	if err != nil {
		return nil, fmt.Errorf("save staged character candidate conflict/stale: %w: %w", errs.ErrToolConflict, err)
	}
	savedBinding, err := domain.CharacterCardBindingFromFoundation(staged.Foundation, inputs)
	if err != nil {
		return nil, fmt.Errorf("bind saved character candidate: %w", err)
	}
	existing, err := t.store.CharacterCards.Load(savedBinding)
	if err != nil {
		return nil, fmt.Errorf("load character lifecycle after candidate save: %w", err)
	}
	expectedLifecycleRevision := int64(0)
	createdAt := ""
	if existing != nil {
		expectedLifecycleRevision = existing.Revision
		createdAt = existing.CreatedAt
	}
	lifecycle := domain.CharacterCardLifecycle{
		Version:            domain.CharacterCardLifecycleVersion,
		Mode:               projectMode,
		Candidate:          savedBinding.Candidate,
		Inputs:             savedBinding.Inputs,
		InputDigest:        savedBinding.InputDigest,
		AnalysisSummary:    strings.TrimSpace(request.AnalysisSummary),
		Completeness:       completeness,
		AnalysisStatus:     domain.CharacterCardAnalysisCandidateReady,
		ReviewStatus:       domain.CharacterCardReviewNotReviewed,
		Findings:           projectionFindings,
		ConfirmationStatus: domain.CharacterCardUnconfirmed,
		RunID:              strings.TrimSpace(request.RunID),
		IdempotencyKey:     strings.TrimSpace(request.IdempotencyKey),
		SubmissionDigest:   submissionDigest,
		SourceMappings:     request.SourceMappings,
		Coverage:           coverage,
		CreatedAt:          createdAt,
	}
	savedLifecycle, err := t.store.CharacterCards.SaveCAS(lifecycle, expectedLifecycleRevision, savedBinding)
	if err != nil {
		return nil, fmt.Errorf("save character candidate lifecycle conflict/stale: %w: %w", errs.ErrToolConflict, err)
	}
	t.registry.markSubmitted(request.RunID, t.Name())
	return characterCandidateResult(savedLifecycle, savedBinding, false)
}

type SaveCharacterReviewTool struct {
	characterToolBase
}

func NewSaveCharacterReviewTool(st *store.Store, registry *CharacterRunRegistry) *SaveCharacterReviewTool {
	return &SaveCharacterReviewTool{characterToolBase: newCharacterToolBase(st, registry)}
}

func (t *SaveCharacterReviewTool) Name() string { return "save_character_review" }
func (t *SaveCharacterReviewTool) Description() string {
	return "Review-mode only. Saves findings without modifying candidate content. A requested pass is deterministically " +
		"downgraded to needs_revision when any blocking finding or CharacterCard completeness failure exists."
}
func (t *SaveCharacterReviewTool) Label() string                        { return "保存角色审核" }
func (t *SaveCharacterReviewTool) ReadOnly(json.RawMessage) bool        { return false }
func (t *SaveCharacterReviewTool) ConcurrencySafe(json.RawMessage) bool { return false }
func (t *SaveCharacterReviewTool) StrictSchema() bool                   { return true }
func (t *SaveCharacterReviewTool) Schema() map[string]any {
	return schema.Object(characterRunProperties(CharacterRunReview)...)
}

type saveCharacterReviewArgs struct {
	RunID              string                              `json:"run_id"`
	Mode               CharacterRunMode                    `json:"mode"`
	IdempotencyKey     string                              `json:"idempotency_key"`
	BaseRevision       int64                               `json:"base_revision"`
	BaseAuditSignature string                              `json:"base_audit_signature"`
	CandidateDigest    string                              `json:"candidate_digest"`
	InputDigest        string                              `json:"input_digest"`
	Verdict            string                              `json:"verdict"`
	Summary            string                              `json:"summary"`
	Findings           []domain.CharacterCardReviewFinding `json:"findings"`
}

func (t *SaveCharacterReviewTool) Execute(_ context.Context, args json.RawMessage) (json.RawMessage, error) {
	var request saveCharacterReviewArgs
	if err := decodeCharacterToolArgs(args, &request); err != nil {
		return nil, err
	}
	if err := validateCharacterRunIdentity(request.RunID, request.Mode); err != nil {
		return nil, err
	}
	if request.Mode != CharacterRunReview {
		return nil, fmt.Errorf("save_character_review requires mode=review: %w", errs.ErrToolConflict)
	}
	if err := validateCharacterSubmissionIdentity(request.IdempotencyKey, request.BaseRevision, request.BaseAuditSignature, request.CandidateDigest, request.InputDigest); err != nil {
		return nil, err
	}
	if request.Verdict != "pass" && request.Verdict != "needs_revision" {
		return nil, fmt.Errorf("character review verdict %q is invalid: %w", request.Verdict, errs.ErrToolArgs)
	}
	if strings.TrimSpace(request.Summary) == "" {
		return nil, fmt.Errorf("character review summary is required: %w", errs.ErrToolArgs)
	}
	submissionDigest, err := characterSubmissionDigest(request)
	if err != nil {
		return nil, err
	}
	foundation, binding, _, _, coreCast, err := currentCharacterRunBinding(t.store, CharacterRunReview)
	if err != nil {
		return nil, err
	}
	lifecycle, err := t.store.CharacterCards.Load(binding)
	if err != nil {
		return nil, fmt.Errorf("load character candidate for review: %w", err)
	}
	if lifecycle == nil || lifecycle.AnalysisStatus != domain.CharacterCardAnalysisCandidateReady ||
		lifecycle.Candidate != binding.Candidate || lifecycle.InputDigest != binding.InputDigest {
		return nil, fmt.Errorf("character candidate is missing or stale; run analyze first: %w", errs.ErrToolConflict)
	}
	candidate, err := t.store.CharacterCards.LoadCandidate()
	if err != nil {
		return nil, fmt.Errorf("load staged character candidate for review: %w", err)
	}
	if candidate == nil {
		return nil, fmt.Errorf("staged character candidate is missing: %w", errs.ErrToolPrecondition)
	}
	projected := candidate.ProjectedCast
	completeness, err := domain.EvaluateCharacterCardCompleteness(foundation, &projected)
	if err != nil {
		return nil, fmt.Errorf("evaluate reviewed character completeness: %w", err)
	}
	findings := append([]domain.CharacterCardReviewFinding(nil), request.Findings...)
	_, projectionFindings, projectionErr := domain.ProjectCharacterCandidateCoreCast(foundation, coreCast)
	if projectionErr != nil {
		return nil, fmt.Errorf("validate reviewed CoreCast projection: %w", projectionErr)
	}
	findings = append(findings, projectionFindings...)
	findings = appendCompletenessFindings(findings, completeness)
	var coverage *domain.AdaptationCharacterCoverage
	if lifecycle.Mode == domain.CharacterCardProjectAdaptation {
		index, indexErr := buildAdaptationSourceCharacterIndex(t.store, coreCast)
		if indexErr != nil {
			return nil, indexErr
		}
		evaluated, coverageErr := domain.EvaluateAdaptationCharacterCoverage(index, lifecycle.SourceMappings)
		if coverageErr != nil {
			return nil, fmt.Errorf("evaluate reviewed adaptation character coverage: %w", coverageErr)
		}
		coverage = &evaluated
		findings = appendCoverageFindings(findings, evaluated)
		if eligibilityErr := domain.ValidateAdaptationCharacterCardEligibility(
			index,
			lifecycle.SourceMappings,
			foundation.Characters,
		); eligibilityErr != nil {
			findings = append(findings, domain.CharacterCardReviewFinding{
				ID:              "adaptation:formal_character_eligibility",
				Scope:           domain.CharacterCardFindingGlobal,
				Location:        "source_mappings",
				Severity:        domain.CharacterCardSeverityBlocking,
				IssueType:       "formal_character_eligibility",
				Description:     eligibilityErr.Error(),
				EvidenceSummary: "deterministic whole-book formal character policy",
				Suggestion:      "exclude evidence-only entries, merge proven aliases into an eligible identity, or strengthen the target-original mainline contract",
				Blocking:        true,
			})
		}
	}
	finalStatus := domain.CharacterCardReviewPassed
	if request.Verdict != "pass" || hasBlockingCharacterFinding(findings) {
		finalStatus = domain.CharacterCardReviewNeedsRevision
	}
	if lifecycle.RunID == strings.TrimSpace(request.RunID) {
		if lifecycle.IdempotencyKey == strings.TrimSpace(request.IdempotencyKey) &&
			lifecycle.SubmissionDigest == submissionDigest &&
			lifecycle.ReviewedCandidate == binding.Candidate &&
			lifecycle.ReviewedInputDigest == binding.InputDigest {
			return characterReviewResult(*lifecycle, binding, true)
		}
		return nil, fmt.Errorf("character review run is already submitted with different content: %w", errs.ErrToolConflict)
	}
	if err := requireCharacterBinding(request.BaseRevision, request.BaseAuditSignature, request.CandidateDigest, request.InputDigest, binding); err != nil {
		return nil, err
	}
	if err := t.registry.requireSubmission(request.RunID, request.Mode, t.Name(), binding); err != nil {
		return nil, err
	}
	reviewed := *lifecycle
	reviewed.Completeness = completeness
	reviewed.ReviewStatus = finalStatus
	reviewed.ReviewedCandidate = binding.Candidate
	reviewed.ReviewedInputDigest = binding.InputDigest
	reviewed.ReviewSummary = strings.TrimSpace(request.Summary)
	reviewed.Findings = findings
	reviewed.Coverage = coverage
	reviewed.ConfirmationStatus = domain.CharacterCardUnconfirmed
	reviewed.RunID = strings.TrimSpace(request.RunID)
	reviewed.IdempotencyKey = strings.TrimSpace(request.IdempotencyKey)
	reviewed.SubmissionDigest = submissionDigest
	reviewed.Error = nil
	saved, err := t.store.CharacterCards.SaveCAS(reviewed, lifecycle.Revision, binding)
	if err != nil {
		return nil, fmt.Errorf("save character review conflict/stale: %w: %w", errs.ErrToolConflict, err)
	}
	t.registry.markSubmitted(request.RunID, t.Name())
	return characterReviewResult(saved, binding, false)
}

func characterCandidateRetryMatches(
	existing *domain.CharacterCardLifecycle,
	request saveCharacterCandidateArgs,
	candidate domain.StoryFoundation,
	current domain.CharacterCardBinding,
	submissionDigest string,
) bool {
	if existing == nil ||
		existing.RunID != strings.TrimSpace(request.RunID) ||
		existing.IdempotencyKey != strings.TrimSpace(request.IdempotencyKey) ||
		existing.SubmissionDigest != submissionDigest ||
		existing.AnalysisStatus != domain.CharacterCardAnalysisCandidateReady ||
		existing.Candidate != current.Candidate ||
		existing.InputDigest != current.InputDigest ||
		existing.AnalysisSummary != strings.TrimSpace(request.AnalysisSummary) {
		return false
	}
	digest, err := domain.CharacterCardContentDigest(candidate)
	if err != nil || digest != current.Candidate.CharacterContentDigest {
		return false
	}
	retryLifecycle := *existing
	retryLifecycle.SourceMappings = request.SourceMappings
	normalized, err := domain.NormalizeCharacterCardLifecycle(retryLifecycle)
	return err == nil && reflect.DeepEqual(normalized.SourceMappings, existing.SourceMappings)
}

func characterSubmissionDigest(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("encode character submission digest: %w", err)
	}
	return store.TextSHA256(string(encoded)), nil
}

func characterCandidateResult(
	lifecycle domain.CharacterCardLifecycle,
	binding domain.CharacterCardBinding,
	idempotent bool,
) (json.RawMessage, error) {
	return json.Marshal(map[string]any{
		"saved":               true,
		"idempotent":          idempotent,
		"mode":                CharacterRunAnalyze,
		"run_id":              lifecycle.RunID,
		"foundation_revision": binding.Candidate.FoundationRevision,
		"candidate_digest":    binding.Candidate.CharacterContentDigest,
		"input_digest":        binding.InputDigest,
		"lifecycle_revision":  lifecycle.Revision,
		"completeness":        lifecycle.Completeness,
		"ready_for_review":    true,
	})
}

func characterReviewResult(
	lifecycle domain.CharacterCardLifecycle,
	binding domain.CharacterCardBinding,
	idempotent bool,
) (json.RawMessage, error) {
	return json.Marshal(map[string]any{
		"saved":              true,
		"idempotent":         idempotent,
		"mode":               CharacterRunReview,
		"run_id":             lifecycle.RunID,
		"final_status":       lifecycle.ReviewStatus,
		"passed":             lifecycle.ReviewStatus == domain.CharacterCardReviewPassed,
		"candidate_digest":   binding.Candidate.CharacterContentDigest,
		"input_digest":       binding.InputDigest,
		"lifecycle_revision": lifecycle.Revision,
		"completeness":       lifecycle.Completeness,
		"findings":           lifecycle.Findings,
	})
}

func buildCharacterContext(st *store.Store, runID string, mode CharacterRunMode) (map[string]any, domain.CharacterCardBinding, error) {
	packet, binding, err := buildCharacterContextEvidence(st, runID, mode)
	if err != nil {
		return nil, domain.CharacterCardBinding{}, err
	}
	if err := validateCharacterContextBudget(packet); err != nil {
		return nil, domain.CharacterCardBinding{}, err
	}
	return packet, binding, nil
}

func buildCharacterContextEvidence(st *store.Store, runID string, mode CharacterRunMode) (map[string]any, domain.CharacterCardBinding, error) {
	foundation, binding, _, projectMode, coreCast, err := currentCharacterRunBinding(st, mode)
	if err != nil {
		return nil, domain.CharacterCardBinding{}, err
	}
	workspaceRun, err := st.CharacterWorkspace.Load(runID)
	if err != nil {
		return nil, domain.CharacterCardBinding{}, fmt.Errorf("load Character workspace run: %w", err)
	}
	if workspaceRun != nil {
		expectedMode := domain.CharacterWorkspaceAnalyze
		if mode == CharacterRunReview {
			expectedMode = domain.CharacterWorkspaceReview
		}
		bindingMatches := workspaceRun.Base.Candidate == binding.Candidate
		if mode == CharacterRunReview {
			candidate, candidateErr := st.CharacterCards.LoadCandidate()
			if candidateErr != nil {
				return nil, domain.CharacterCardBinding{},
					fmt.Errorf("load Character workspace review candidate: %w", candidateErr)
			}
			bindingMatches = candidate != nil &&
				candidate.Base.Candidate == workspaceRun.Base.Candidate &&
				candidate.Base.InputDigest == workspaceRun.Base.InputDigest &&
				workspaceRun.InputCandidateDigest == binding.Candidate.CharacterContentDigest
		}
		if workspaceRun.Mode != expectedMode ||
			!bindingMatches ||
			workspaceRun.Base.InputDigest != binding.InputDigest {
			return nil, domain.CharacterCardBinding{},
				fmt.Errorf("Character workspace run binding is stale or mode-mismatched: %w", errs.ErrToolConflict)
		}
		if mode == CharacterRunAnalyze {
			foundation = domain.CloneStoryFoundation(workspaceRun.InputCandidate)
		}
	}
	lifecycle, err := st.CharacterCards.Load(binding)
	if err != nil {
		return nil, domain.CharacterCardBinding{}, fmt.Errorf("load character lifecycle: %w", err)
	}
	if mode == CharacterRunReview && (lifecycle == nil || lifecycle.AnalysisStatus != domain.CharacterCardAnalysisCandidateReady) {
		return nil, domain.CharacterCardBinding{}, fmt.Errorf("review requires a current persisted character candidate: %w", errs.ErrToolPrecondition)
	}
	userRules, err := st.UserRules.Load()
	if err != nil {
		return nil, domain.CharacterCardBinding{}, fmt.Errorf("load character user constraints: %w", err)
	}
	packet := map[string]any{
		"project_mode":           projectMode,
		"base_revision":          binding.Candidate.FoundationRevision,
		"base_audit_signature":   binding.Candidate.FoundationAuditSignature,
		"candidate_digest":       binding.Candidate.CharacterContentDigest,
		"input_digest":           binding.InputDigest,
		"input_signatures":       binding.Inputs,
		"premise":                compactCharacterText(foundation.Premise),
		"world_rules":            compactCharacterJSON(foundation.WorldRules),
		"current_characters":     foundation.Characters,
		"current_relationships":  foundation.Relationships,
		"relationships_reviewed": foundation.RelationshipsReviewed,
		"lifecycle":              compactCharacterLifecycle(lifecycle),
		"evidence_policy": map[string]any{
			"raw_source_included": false,
			"review_must_reread":  mode == CharacterRunReview,
		},
	}
	if mode == CharacterRunAnalyze {
		packet["current_characters"] = compactCharacterProfiles(foundation.Characters)
		packet["current_relationships"] = compactCharacterSourceRelationships(foundation.Relationships)
	}
	if workspaceRun != nil {
		packet["workspace_request"] = map[string]any{
			"run_id":                      workspaceRun.RunID,
			"requested_character_ids":     workspaceRun.RequestedCharacterIDs,
			"instruction":                 workspaceRun.Instruction,
			"allow_supporting_characters": workspaceRun.AllowSupportingCharacters,
			"input_candidate_digest":      workspaceRun.InputCandidateDigest,
		}
	}
	if projectMode == domain.CharacterCardProjectAdaptation {
		packet["core_cast"] = compactCharacterCoreCast(coreCast)
		if userRules != nil {
			packet["user_constraints"] = compactCharacterJSON(userRules.Payload())
		}
		adaptation, adaptationErr := buildAdaptationCharacterEvidence(st)
		if adaptationErr != nil {
			return nil, domain.CharacterCardBinding{}, adaptationErr
		}
		packet["adaptation_evidence"] = adaptation
	} else {
		review, reviewErr := st.RunMeta.PlanningReview()
		if reviewErr != nil {
			return nil, domain.CharacterCardBinding{}, fmt.Errorf("load original character brief: %w", reviewErr)
		}
		packet["creative_brief"] = compactOriginalCharacterBrief(review)
		packet["user_constraints"] = compactOriginalCharacterRules(userRules, review)
		if coreCast != nil {
			packet["legacy_core_cast_binding"] = map[string]any{
				"content_signature": coreCast.ContentSignature,
				"draft_revision":    coreCast.DraftRevision,
				"draft_hash":        coreCast.DraftHash,
				"authoritative":     false,
			}
		}
	}
	return packet, binding, nil
}

func compactOriginalCharacterBrief(review *domain.PlanningReview) any {
	if review == nil {
		return nil
	}
	return map[string]any{
		"brief":              review.Brief,
		"target_total_words": review.TargetTotalWords,
		"status":             review.Status,
	}
}

func compactOriginalCharacterRules(userRules *rules.Snapshot, review *domain.PlanningReview) map[string]any {
	if userRules == nil {
		return map[string]any{}
	}
	payload := userRules.Payload()
	preferences, _ := payload["preferences"].(string)
	if review != nil {
		for _, duplicate := range []string{review.StartPrompt, review.Brief} {
			if duplicate = strings.TrimSpace(duplicate); duplicate != "" {
				preferences = strings.ReplaceAll(preferences, duplicate, "")
			}
		}
	}
	payload["preferences"] = strings.TrimSpace(preferences)
	return payload
}

// CurrentCharacterWorkflow returns durable Character state for deterministic
// Host routing without exposing model evidence or relying on Coordinator
// interpretation.
func CurrentCharacterWorkflow(st *store.Store) (
	*domain.CharacterCardCandidate,
	*domain.CharacterCardLifecycle,
	domain.CharacterCardBinding,
	error,
) {
	if st == nil {
		return nil, nil, domain.CharacterCardBinding{}, fmt.Errorf("character workflow store is nil")
	}
	candidate, err := st.CharacterCards.LoadCandidate()
	if err != nil || candidate == nil {
		return candidate, nil, domain.CharacterCardBinding{}, err
	}
	foundation, binding, _, _, _, err := currentCharacterRunBinding(st, CharacterRunReview)
	if err != nil {
		// A confirmed publication remains authoritative when Architect changes
		// only premise/world rules. Repair the persisted Foundation revision
		// binding once, then re-read through the normal strict path. Genuine
		// character, relationship, CoreCast, or input changes still fail closed.
		if rebindErr := rebindConfirmedCharacterWorkflow(st); rebindErr != nil {
			return candidate, nil, domain.CharacterCardBinding{}, err
		}
		candidate, err = st.CharacterCards.LoadCandidate()
		if err != nil || candidate == nil {
			return candidate, nil, domain.CharacterCardBinding{}, err
		}
		foundation, binding, _, _, _, err = currentCharacterRunBinding(st, CharacterRunReview)
		if err != nil {
			return candidate, nil, domain.CharacterCardBinding{}, err
		}
	}
	binding, err = domain.CharacterCardBindingFromFoundation(foundation, binding.Inputs)
	if err != nil {
		return candidate, nil, domain.CharacterCardBinding{}, err
	}
	lifecycle, err := st.CharacterCards.Load(binding)
	return candidate, lifecycle, binding, err
}

func CurrentCharacterCanonicalBinding(
	st *store.Store,
) (
	domain.StoryFoundation,
	domain.CharacterCardBinding,
	domain.CharacterCardInputSignatures,
	*domain.CoreCastContract,
	error,
) {
	foundation, binding, inputs, _, coreCast, err := currentCharacterBinding(st)
	return foundation, binding, inputs, coreCast, err
}

func currentCharacterRunBinding(
	st *store.Store,
	mode CharacterRunMode,
) (
	domain.StoryFoundation,
	domain.CharacterCardBinding,
	domain.CharacterCardInputSignatures,
	domain.CharacterCardProjectMode,
	*domain.CoreCastContract,
	error,
) {
	canonical, canonicalBinding, inputs, projectMode, coreCast, err := currentCharacterBinding(st)
	if err != nil || mode != CharacterRunReview {
		return canonical, canonicalBinding, inputs, projectMode, coreCast, err
	}
	candidate, err := st.CharacterCards.LoadCandidate()
	if err != nil {
		return domain.StoryFoundation{}, domain.CharacterCardBinding{}, inputs, projectMode, coreCast,
			fmt.Errorf("load staged character candidate: %w", err)
	}
	if candidate == nil {
		return domain.StoryFoundation{}, domain.CharacterCardBinding{}, inputs, projectMode, coreCast,
			fmt.Errorf("review requires a staged character candidate: %w", errs.ErrToolPrecondition)
	}
	if candidate.Base.Candidate != canonicalBinding.Candidate ||
		candidate.Base.InputDigest != canonicalBinding.InputDigest {
		return domain.StoryFoundation{}, domain.CharacterCardBinding{}, inputs, projectMode, coreCast,
			fmt.Errorf("staged character candidate is stale for current Foundation or inputs: %w", errs.ErrToolConflict)
	}
	binding, err := domain.CharacterCardBindingFromFoundation(candidate.Foundation, inputs)
	if err != nil {
		return domain.StoryFoundation{}, domain.CharacterCardBinding{}, inputs, projectMode, coreCast,
			fmt.Errorf("bind staged character candidate: %w", err)
	}
	return candidate.Foundation, binding, inputs, projectMode, coreCast, nil
}

func currentCharacterBinding(
	st *store.Store,
) (
	domain.StoryFoundation,
	domain.CharacterCardBinding,
	domain.CharacterCardInputSignatures,
	domain.CharacterCardProjectMode,
	*domain.CoreCastContract,
	error,
) {
	foundation, err := st.Foundation.Load()
	if err != nil {
		return domain.StoryFoundation{}, domain.CharacterCardBinding{}, domain.CharacterCardInputSignatures{}, "", nil,
			fmt.Errorf("load character foundation: %w", err)
	}
	coreCast, err := st.CoreCast.LoadWithLegacySignatureRepair()
	if err != nil {
		return domain.StoryFoundation{}, domain.CharacterCardBinding{}, domain.CharacterCardInputSignatures{}, "", nil,
			fmt.Errorf("load character core cast: %w", err)
	}
	inputs, mode, err := currentCharacterInputs(st, coreCast)
	if err != nil {
		return domain.StoryFoundation{}, domain.CharacterCardBinding{}, domain.CharacterCardInputSignatures{}, "", nil, err
	}
	binding, err := domain.CharacterCardBindingFromFoundation(foundation, inputs)
	if err != nil {
		return domain.StoryFoundation{}, domain.CharacterCardBinding{}, domain.CharacterCardInputSignatures{}, "", nil,
			fmt.Errorf("bind current character evidence: %w", err)
	}
	return foundation, binding, inputs, mode, coreCast, nil
}

func currentCharacterInputs(
	st *store.Store,
	coreCast *domain.CoreCastContract,
) (domain.CharacterCardInputSignatures, domain.CharacterCardProjectMode, error) {
	inputs := domain.CharacterCardInputSignatures{}
	mode := domain.CharacterCardProjectOriginal
	if coreCast != nil {
		inputs.CoreCast = coreCast.ContentSignature
		if coreCast.Mode == domain.CoreCastModeAdaptation {
			mode = domain.CharacterCardProjectAdaptation
		}
	} else {
		gate, err := st.CoreCast.LoadGateBinding()
		if err != nil {
			return inputs, mode, fmt.Errorf("load character CoreCast gate binding: %w", err)
		}
		if (gate != nil && gate.Mode == domain.CoreCastModeAdaptation) || st.Adaptation.Exists() {
			mode = domain.CharacterCardProjectAdaptation
		}
	}
	userRules, err := st.UserRules.Load()
	if err != nil {
		return inputs, mode, fmt.Errorf("load character input user rules: %w", err)
	}
	appendNamedCharacterSignature(&inputs, "user_rules", userRules)
	if mode == domain.CharacterCardProjectOriginal {
		review, reviewErr := st.RunMeta.PlanningReview()
		if reviewErr != nil {
			return inputs, mode, fmt.Errorf("load original character input brief: %w", reviewErr)
		}
		if review != nil {
			currentBrief := signatureForCharacterInput(struct {
				Brief       string `json:"brief"`
				StartPrompt string `json:"start_prompt"`
			}{review.Brief, review.StartPrompt})
			inputs.CreativeBrief = currentBrief
			confirmedBrief, confirmed, err := confirmedCharacterCreativeBriefInput(st)
			if err != nil {
				return inputs, mode, fmt.Errorf("load confirmed original character brief: %w", err)
			}
			if confirmed {
				// Once Character has passed independent review and explicit user
				// confirmation, later planning feedback is downstream of that gate.
				// Keep the exact creative-brief evidence the user approved;
				// user_rules may still advance through the compatibility check.
				inputs.CreativeBrief = confirmedBrief
			}
		}
		return inputs, mode, nil
	}
	sourceFoundation, err := st.Adaptation.LoadSourceFoundation()
	if err != nil {
		return inputs, mode, fmt.Errorf("load adaptation character source foundation: %w", err)
	}
	if sourceFoundation != nil {
		// SourceSignature identifies the imported manuscript, while the full
		// snapshot digest invalidates character work when source facts change
		// without a manifest change.
		inputs.SourceFoundation = signatureForCharacterInput(sourceFoundation)
	}
	intent, err := st.Adaptation.LoadCoCreateIntent()
	if err != nil {
		return inputs, mode, fmt.Errorf("load adaptation character intent: %w", err)
	}
	if intent != nil {
		inputs.AdaptationIntent = intent.IntentHash
		if inputs.AdaptationIntent == "" {
			inputs.AdaptationIntent = signatureForCharacterInput(intent)
		}
	}
	dossier, err := st.Adaptation.LoadCoCreateDossier()
	if err != nil {
		return inputs, mode, fmt.Errorf("load adaptation character dossier: %w", err)
	}
	appendNamedCharacterSignature(&inputs, "adaptation_dossier", dossier)
	briefing, err := st.Adaptation.LoadCoCreateBriefing()
	if err != nil {
		return inputs, mode, fmt.Errorf("load adaptation character briefing: %w", err)
	}
	appendNamedCharacterSignature(&inputs, "adaptation_briefing", briefing)
	characterBrief, err := st.Adaptation.LoadCharacterBrief()
	if err != nil {
		return inputs, mode, fmt.Errorf("load adaptation character brief: %w", err)
	}
	if characterBrief != nil {
		inputs.CreativeBrief = signatureForCharacterInput(characterBrief)
	}
	reports, err := st.Adaptation.LoadCompleteSourceReports()
	if err != nil {
		return inputs, mode, fmt.Errorf("load adaptation character reports: %w", err)
	}
	appendNamedCharacterSignature(&inputs, "source_reports", reports)
	index, err := domain.BuildAdaptationSourceCharacterIndex(sourceFoundation, reports, dossier, coreCast)
	if err != nil {
		return inputs, mode, fmt.Errorf("build adaptation source character index signature: %w", err)
	}
	appendNamedCharacterSignature(&inputs, "source_character_index", index)
	return inputs, mode, nil
}

func confirmedCharacterCreativeBriefInput(st *store.Store) (string, bool, error) {
	candidate, err := st.CharacterCards.LoadCandidate()
	if err != nil || candidate == nil {
		return "", false, err
	}
	lifecycle, err := st.CharacterCards.Load(candidate.Base)
	if err != nil || lifecycle == nil {
		return "", false, err
	}
	confirmed := lifecycle.AnalysisStatus == domain.CharacterCardAnalysisCandidateReady &&
		lifecycle.ReviewStatus == domain.CharacterCardReviewPassed &&
		lifecycle.ConfirmationStatus == domain.CharacterCardConfirmed &&
		lifecycle.Candidate == candidate.Base.Candidate &&
		lifecycle.ReviewedCandidate == candidate.Base.Candidate &&
		lifecycle.ReviewedInputDigest == candidate.Base.InputDigest
	if !confirmed {
		return "", false, nil
	}
	return lifecycle.Inputs.CreativeBrief, true, nil
}

func bindCharacterCoreCastProjection(
	st *store.Store,
	projected *domain.CoreCastContract,
	mode domain.CharacterCardProjectMode,
) error {
	gate, err := st.CoreCast.LoadGateBinding()
	if err != nil {
		return fmt.Errorf("load Character gate binding: %w", err)
	}
	if gate == nil {
		if mode == domain.CharacterCardProjectOriginal {
			return nil
		}
		if projected.Mode == domain.CoreCastModeAdaptation &&
			projected.DraftRevision > 0 &&
			strings.TrimSpace(projected.DraftHash) != "" &&
			len(strings.TrimSpace(projected.SourceSignature)) == 64 &&
			len(strings.TrimSpace(projected.AdaptationIntentHash)) == 64 {
			return nil
		}
		return fmt.Errorf("adaptation Character gate binding is missing")
	}
	expectedMode := domain.CoreCastModeNormal
	if mode == domain.CharacterCardProjectAdaptation {
		expectedMode = domain.CoreCastModeAdaptation
	}
	if gate.Mode != expectedMode {
		return fmt.Errorf("Character gate binding mode is stale")
	}
	projected.Mode = expectedMode
	projected.DraftRevision = gate.DraftRevision
	projected.DraftHash = gate.DraftHash
	if expectedMode == domain.CoreCastModeAdaptation {
		projected.SourceSignature = gate.SourceSignature
		projected.AdaptationIntentHash = gate.AdaptationIntentHash
	} else {
		projected.SourceSignature = ""
		projected.AdaptationIntentHash = ""
	}
	return nil
}

func appendNamedCharacterSignature(inputs *domain.CharacterCardInputSignatures, name string, value any) {
	if value == nil {
		return
	}
	inputs.Additional = append(inputs.Additional, domain.CharacterCardNamedSignature{
		Name:      name,
		Signature: signatureForCharacterInput(value),
	})
}

func signatureForCharacterInput(value any) string {
	data, _ := json.Marshal(value)
	return domain.ContentSignature(data)
}

func buildAdaptationCharacterEvidence(st *store.Store) (map[string]any, error) {
	sourceFoundation, err := st.Adaptation.LoadSourceFoundation()
	if err != nil {
		return nil, fmt.Errorf("load adaptation source foundation: %w", err)
	}
	dossier, err := st.Adaptation.LoadCoCreateDossier()
	if err != nil {
		return nil, fmt.Errorf("load adaptation dossier: %w", err)
	}
	intent, err := st.Adaptation.LoadCoCreateIntent()
	if err != nil {
		return nil, fmt.Errorf("load adaptation intent: %w", err)
	}
	briefing, err := st.Adaptation.LoadCoCreateBriefing()
	if err != nil {
		return nil, fmt.Errorf("load adaptation briefing: %w", err)
	}
	characterBrief, err := st.Adaptation.LoadCharacterBrief()
	if err != nil {
		return nil, fmt.Errorf("load adaptation character brief: %w", err)
	}
	reports, err := st.Adaptation.LoadCompleteSourceReports()
	if err != nil {
		return nil, fmt.Errorf("load adaptation source reports: %w", err)
	}
	coreCast, err := st.CoreCast.LoadWithLegacySignatureRepair()
	if err != nil {
		return nil, fmt.Errorf("load adaptation core cast: %w", err)
	}
	index, err := domain.BuildAdaptationSourceCharacterIndex(sourceFoundation, reports, dossier, coreCast)
	if err != nil {
		return nil, fmt.Errorf("build adaptation source character index: %w", err)
	}
	var mappings []domain.CharacterSourceMapping
	if candidate, lifecycle, _, workflowErr := CurrentCharacterWorkflow(st); workflowErr == nil && candidate != nil && lifecycle != nil {
		mappings = lifecycle.SourceMappings
	}
	coverage, err := domain.EvaluateAdaptationCharacterCoverage(index, mappings)
	if err != nil {
		return nil, fmt.Errorf("evaluate adaptation source character coverage: %w", err)
	}
	return map[string]any{
		"source_foundation":         compactCharacterSourceFoundation(sourceFoundation),
		"source_character_index":    compactCharacterSourceIndex(index),
		"source_character_coverage": compactCharacterCoverage(coverage),
		"dossier":                   compactCharacterDossier(dossier),
		"intent":                    compactCharacterIntent(intent),
		"briefing":                  compactCharacterBriefing(briefing),
		"adaptation_brief":          compactCharacterAdaptationBrief(characterBrief),
		"report_count":              len(reports),
		"chapter_reports_omitted":   true,
		"report_evidence_note":      "all completed reports are deterministically aggregated into source_character_index",
	}, nil
}

type characterSourceReport struct {
	Chapter        int                        `json:"chapter"`
	Title          string                     `json:"title"`
	Characters     []string                   `json:"characters"`
	CharacterFacts []string                   `json:"character_facts"`
	Relationships  []domain.RelationshipEntry `json:"relationships"`
}

func compactCharacterReports(reports []domain.AdaptationSourceReport) []characterSourceReport {
	if len(reports) > characterContextReportLimit {
		reports = reports[:characterContextReportLimit]
	}
	out := make([]characterSourceReport, 0, len(reports))
	for _, report := range reports {
		out = append(out, characterSourceReport{
			Chapter:        report.Chapter,
			Title:          compactCharacterText(report.Title),
			Characters:     compactCharacterStrings(report.Characters, characterContextListLimit),
			CharacterFacts: compactCharacterStrings(report.CharacterFacts, characterContextFactLimit),
			Relationships:  compactCharacterRelationshipEntries(report.Relationships, characterContextFactLimit),
		})
	}
	return out
}

func compactCharacterDossier(value *domain.AdaptationCoCreateDossier) any {
	if value == nil {
		return nil
	}
	return compactCharacterJSON(map[string]any{
		"source_signature": value.SourceSignature,
		"overview":         compactCharacterText(value.Overview),
		"ambiguity_risks":  limitSlice(value.AmbiguityRisks, 1),
		"details_omitted":  true,
	})
}

func compactCharacterBriefing(value *domain.AdaptationCoCreateBriefing) any {
	if value == nil {
		return nil
	}
	return compactCharacterJSON(map[string]any{
		"source_signature":      value.SourceSignature,
		"intent_hash":           value.IntentHash,
		"overview":              compactCharacterText(value.Overview),
		"intent_relevant_risks": limitSlice(value.IntentRelevantRisks, 1),
		"decisions":             limitSlice(value.Decisions, 1),
		"details_omitted":       true,
	})
}

func compactCharacterSourceFoundation(value *domain.AdaptationSourceFoundation) any {
	if value == nil {
		return nil
	}
	characters := make([]map[string]any, 0, len(value.Characters))
	for _, character := range value.Characters {
		characters = append(characters, map[string]any{
			"id":   character.ID,
			"name": compactCharacterText(character.Name),
			"role": compactCharacterText(character.Role),
			"tier": character.Tier,
		})
	}
	return map[string]any{
		"version":               value.Version,
		"source_signature":      value.SourceSignature,
		"source_chapter_count":  value.SourceChapterCount,
		"premise":               compactCharacterText(value.Premise),
		"formal_characters":     characters,
		"relationships_omitted": true,
		"world_rules_omitted":   true,
		"outline_omitted":       true,
	}
}

type compactCharacterIndexEntry struct {
	ID                 string            `json:"id"`
	CanonicalName      string            `json:"canonical_name"`
	Aliases            []string          `json:"aliases,omitempty"`
	Profile            *domain.Character `json:"profile,omitempty"`
	Chapters           []int             `json:"chapters,omitempty"`
	AppearanceCount    int               `json:"appearance_count"`
	CausalEventCount   int               `json:"causal_event_count"`
	DossierMajor       bool              `json:"dossier_major"`
	CoreCast           bool              `json:"core_cast"`
	Named              bool              `json:"named"`
	CardEligible       bool              `json:"card_eligible"`
	IdentityUncertain  bool              `json:"identity_uncertain"`
	EligibilityReasons []string          `json:"eligibility_reasons,omitempty"`
	Facts              []string          `json:"facts,omitempty"`
	Conflicts          []string          `json:"conflicts,omitempty"`
	Uncertainties      []string          `json:"uncertainties,omitempty"`
}

func compactCharacterSourceIndex(value domain.AdaptationSourceCharacterIndex) map[string]any {
	characters := make([]compactCharacterIndexEntry, 0, len(value.Characters))
	for _, entry := range value.Characters {
		var profile *domain.Character
		var facts []string
		var eligibilityReasons []string
		var conflicts []string
		var uncertainties []string
		if entry.CardEligible {
			compacted := compactCharacterProfile(entry.Profile)
			profile = &compacted
			facts = compactCharacterStrings(entry.Facts, characterContextFactLimit)
			eligibilityReasons = compactCharacterStrings(entry.EligibilityReasons, characterContextFactLimit)
			conflicts = compactCharacterStrings(entry.Conflicts, characterContextFactLimit)
			uncertainties = compactCharacterStrings(entry.Uncertainties, characterContextFactLimit)
		} else {
			eligibilityReasons = compactCharacterStrings(entry.EligibilityReasons, 1)
		}
		characters = append(characters, compactCharacterIndexEntry{
			ID:                 entry.ID,
			CanonicalName:      compactCharacterText(entry.CanonicalName),
			Aliases:            compactCharacterStrings(entry.Aliases, characterContextAliasLimit),
			Profile:            profile,
			Chapters:           limitSlice(entry.Chapters, characterContextListLimit),
			AppearanceCount:    entry.AppearanceCount,
			CausalEventCount:   entry.CausalEventCount,
			DossierMajor:       entry.DossierMajor,
			CoreCast:           entry.CoreCast,
			Named:              entry.Named,
			CardEligible:       entry.CardEligible,
			IdentityUncertain:  entry.IdentityUncertain,
			EligibilityReasons: eligibilityReasons,
			Facts:              facts,
			Conflicts:          conflicts,
			Uncertainties:      uncertainties,
		})
	}
	return map[string]any{
		"version":         value.Version,
		"input_signature": value.InputSignature,
		"source_chapters": value.SourceChapters,
		"characters":      characters,
		"evidence_policy": "all source IDs are listed; detailed chapter evidence is omitted from this bounded packet",
	}
}

func compactCharacterCoverage(value domain.AdaptationCharacterCoverage) map[string]any {
	return map[string]any{
		"source_total":        value.SourceTotal,
		"decision_required":   value.DecisionRequired,
		"mapped":              value.Mapped,
		"explicitly_excluded": value.ExplicitlyExcluded,
		"pending":             value.Pending,
		"blocking_gaps":       value.BlockingGaps,
		"decisions_omitted":   true,
		"decision_source":     "source_character_index plus lifecycle.source_mappings",
	}
}

func compactCharacterIntent(value *domain.AdaptationCoCreateIntent) any {
	if value == nil {
		return nil
	}
	return map[string]any{
		"raw_request":        compactCharacterText(value.RawRequest),
		"granularity":        compactCharacterText(value.Granularity),
		"rewrite_policy":     compactCharacterText(value.RewritePolicy),
		"goals":              compactCharacterStrings(value.Goals, characterContextListLimit),
		"heroine_names":      compactCharacterStrings(value.HeroineNames, characterContextListLimit),
		"restricted_names":   compactCharacterStrings(value.RestrictedNames, characterContextListLimit),
		"relationship_rules": compactCharacterStrings(value.RelationshipRules, characterContextListLimit),
		"preserve_rules":     compactCharacterStrings(value.PreserveRules, characterContextListLimit),
		"intent_hash":        value.IntentHash,
	}
}

func compactCharacterAdaptationBrief(value *domain.AdaptationCharacterBrief) any {
	if value == nil {
		return nil
	}
	return map[string]any{
		"brief":               compactCharacterText(value.Brief),
		"source_signature":    value.SourceSignature,
		"intent_hash":         value.IntentHash,
		"core_cast_signature": value.CoreCastSignature,
	}
}

func compactCharacterCoreCast(value *domain.CoreCastContract) any {
	if value == nil {
		return nil
	}
	members := make([]map[string]any, 0, len(value.Members))
	for _, member := range value.Members {
		members = append(members, map[string]any{
			"id":                   member.Character.ID,
			"name":                 compactCharacterText(member.Character.Name),
			"aliases":              compactCharacterStrings(member.Character.Aliases, characterContextAliasLimit),
			"role":                 compactCharacterText(member.Character.Role),
			"gender":               member.Character.Gender,
			"tier":                 member.Character.Tier,
			"description":          compactCharacterText(member.Character.Description),
			"goal":                 compactCharacterText(member.Character.Goal),
			"conflict":             compactCharacterText(member.Character.Conflict),
			"importance":           member.Importance,
			"origin":               member.Origin,
			"mainline_function":    compactCharacterText(member.MainlineFunction),
			"source_character_ids": member.SourceCharacterIDs,
		})
	}
	return compactCharacterJSON(map[string]any{
		"mode":                   value.Mode,
		"members":                members,
		"planned_relationships":  compactCharacterSourceRelationships(value.PlannedRelationships),
		"source_dispositions":    value.SourceDispositions,
		"content_signature":      value.ContentSignature,
		"source_signature":       value.SourceSignature,
		"adaptation_intent_hash": value.AdaptationIntentHash,
	})
}

func compactCharacterLifecycle(value *domain.CharacterCardLifecycle) any {
	if value == nil {
		return nil
	}
	mappings := make([]map[string]any, 0, len(value.SourceMappings))
	for _, mapping := range value.SourceMappings {
		mappings = append(mappings, map[string]any{
			"id":                   mapping.ID,
			"action":               mapping.Action,
			"source_character_ids": mapping.SourceCharacterIDs,
			"target_character_ids": mapping.TargetCharacterIDs,
			"rationale":            compactCharacterShortText(mapping.Rationale),
			"evidence_omitted":     len(mapping.Evidence) > 0,
		})
	}
	return map[string]any{
		"version":              value.Version,
		"revision":             value.Revision,
		"mode":                 value.Mode,
		"candidate":            value.Candidate,
		"input_digest":         value.InputDigest,
		"analysis_summary":     compactCharacterText(value.AnalysisSummary),
		"completeness":         value.Completeness,
		"analysis_status":      value.AnalysisStatus,
		"review_status":        value.ReviewStatus,
		"reviewed_candidate":   value.ReviewedCandidate,
		"review_summary":       compactCharacterText(value.ReviewSummary),
		"findings":             compactCharacterJSON(value.Findings),
		"confirmation_status":  value.ConfirmationStatus,
		"error":                value.Error,
		"source_mappings":      mappings,
		"coverage":             compactCharacterCoverageValue(value.Coverage),
		"source_evidence_note": "mapping evidence bodies omitted; independently review against source_character_index",
	}
}

func compactCharacterCoverageValue(value *domain.AdaptationCharacterCoverage) any {
	if value == nil {
		return nil
	}
	return compactCharacterCoverage(*value)
}

func compactCharacterProfiles(values []domain.Character) []domain.Character {
	out := make([]domain.Character, 0, len(values))
	for _, value := range values {
		out = append(out, compactCharacterProfile(value))
	}
	return out
}

func compactCharacterProfile(value domain.Character) domain.Character {
	value.Name = compactCharacterText(value.Name)
	value.Aliases = compactCharacterStrings(value.Aliases, characterContextAliasLimit)
	value.Role = compactCharacterText(value.Role)
	value.Description = compactCharacterText(value.Description)
	value.Arc = compactCharacterText(value.Arc)
	value.Traits = compactCharacterStrings(value.Traits, characterContextFactLimit)
	value.Goal = compactCharacterText(value.Goal)
	value.Motivation = compactCharacterText(value.Motivation)
	value.Conflict = compactCharacterText(value.Conflict)
	value.Voice = compactCharacterText(value.Voice)
	value.Constraints = compactCharacterStrings(value.Constraints, characterContextFactLimit)
	value.ContrastDetails = nil
	value.KeyBackstory = nil
	value.InitialState = nil
	value.KnowledgeBoundary = nil
	value.Notes = ""
	return value
}

func compactCharacterSourceRelationships(values []domain.CharacterRelationship) []domain.CharacterRelationship {
	values = limitSlice(values, characterContextListLimit)
	for i := range values {
		values[i].Label = compactCharacterText(values[i].Label)
		values[i].Description = compactCharacterText(values[i].Description)
		values[i].Since = compactCharacterText(values[i].Since)
		values[i].Tags = compactCharacterStrings(values[i].Tags, characterContextFactLimit)
		values[i].Constraints = compactCharacterStrings(values[i].Constraints, characterContextFactLimit)
	}
	return values
}

func compactCharacterRelationshipEntries(values []domain.RelationshipEntry, limit int) []domain.RelationshipEntry {
	values = limitSlice(values, limit)
	for i := range values {
		values[i].CharacterA = compactCharacterText(values[i].CharacterA)
		values[i].CharacterB = compactCharacterText(values[i].CharacterB)
		values[i].Relation = compactCharacterText(values[i].Relation)
	}
	return values
}

func compactCharacterStrings(values []string, limit int) []string {
	values = limitStrings(values, limit)
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value = compactCharacterText(value); value != "" {
			out = append(out, value)
		}
	}
	return out
}

func compactCharacterText(value string) string {
	value = strings.TrimSpace(value)
	runes := []rune(value)
	if len(runes) <= characterContextTextLimit {
		return value
	}
	return string(runes[:characterContextTextLimit]) + "…"
}

func compactCharacterShortText(value string) string {
	value = strings.TrimSpace(value)
	const limit = 180
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit]) + "…"
}

func compactCharacterJSON(value any) any {
	data, err := json.Marshal(value)
	if err != nil {
		return value
	}
	var decoded any
	if err := json.Unmarshal(data, &decoded); err != nil {
		return value
	}
	return compactCharacterJSONValue(decoded)
}

func compactCharacterJSONValue(value any) any {
	switch typed := value.(type) {
	case string:
		return compactCharacterText(typed)
	case []any:
		if len(typed) > characterContextListLimit {
			typed = typed[:characterContextListLimit]
		}
		out := make([]any, 0, len(typed))
		for _, item := range typed {
			out = append(out, compactCharacterJSONValue(item))
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			out[key] = compactCharacterJSONValue(item)
		}
		return out
	default:
		return value
	}
}

type characterContextPageState struct {
	Index          int
	Total          int
	EvidenceDigest string
}

type characterContextCursor struct {
	Version                  int              `json:"version"`
	RunID                    string           `json:"run_id"`
	Mode                     CharacterRunMode `json:"mode"`
	Page                     int              `json:"page"`
	Attempt                  int              `json:"attempt"`
	EvidenceDigest           string           `json:"evidence_digest"`
	FoundationRevision       int64            `json:"foundation_revision"`
	FoundationAuditSignature string           `json:"foundation_audit_signature"`
	InputDigest              string           `json:"input_digest"`
}

type characterContextEvidenceItem struct {
	Path  string                 `json:"path"`
	Value any                    `json:"value"`
	Chunk *characterContextChunk `json:"chunk,omitempty"`
}

type characterContextChunk struct {
	Index int `json:"index"`
	Total int `json:"total"`
}

var characterContextCommonKeys = map[string]struct{}{
	"project_mode":           {},
	"base_revision":          {},
	"base_audit_signature":   {},
	"candidate_digest":       {},
	"input_digest":           {},
	"input_signatures":       {},
	"relationships_reviewed": {},
	"evidence_policy":        {},
	"run_id":                 {},
	"mode":                   {},
	"workspace_request":      {},
}

var characterContextEvidenceOrder = []string{
	"premise",
	"world_rules",
	"current_characters",
	"current_relationships",
	"lifecycle",
	"creative_brief",
	"user_constraints",
	"legacy_core_cast_binding",
	"core_cast",
	"adaptation_evidence",
	"cast_promotion",
	"cast_promotion_workflow",
}

func buildCharacterContextPage(
	packet map[string]any,
	request characterContextArgs,
	binding domain.CharacterCardBinding,
	attempt int,
) (map[string]any, characterContextPageState, error) {
	digest, err := characterContextEvidenceDigest(packet)
	if err != nil {
		return nil, characterContextPageState{}, err
	}
	plain := cloneCharacterContextMap(packet)
	if err := finalizeCharacterContextBudget(plain); err == nil {
		if strings.TrimSpace(request.Cursor) != "" {
			return nil, characterContextPageState{}, fmt.Errorf("character_context cursor is not valid for an unpaged snapshot: %w", errs.ErrToolConflict)
		}
		return plain, characterContextPageState{Index: 0, Total: 1, EvidenceDigest: digest}, nil
	}

	common, items, err := splitCharacterContextEvidence(packet)
	if err != nil {
		return nil, characterContextPageState{}, err
	}
	pages, err := packCharacterContextPages(common, items, request, binding, attempt, digest)
	if err != nil {
		return nil, characterContextPageState{}, err
	}
	pageIndex := 0
	if strings.TrimSpace(request.Cursor) != "" {
		cursor, decodeErr := decodeCharacterContextCursor(request.Cursor)
		if decodeErr != nil {
			return nil, characterContextPageState{}, decodeErr
		}
		if err := validateCharacterContextCursor(cursor, request, binding, attempt, digest); err != nil {
			return nil, characterContextPageState{}, err
		}
		pageIndex = cursor.Page
	}
	if pageIndex < 0 || pageIndex >= len(pages) {
		return nil, characterContextPageState{}, fmt.Errorf("character_context cursor page %d is outside snapshot page count %d: %w", pageIndex, len(pages), errs.ErrToolConflict)
	}
	return pages[pageIndex], characterContextPageState{
		Index: pageIndex, Total: len(pages), EvidenceDigest: digest,
	}, nil
}

func characterContextEvidenceDigest(packet map[string]any) (string, error) {
	data, err := json.Marshal(packet)
	if err != nil {
		return "", fmt.Errorf("marshal Character context evidence digest: %w", err)
	}
	digest := sha256.Sum256(data)
	return fmt.Sprintf("%x", digest[:]), nil
}

func splitCharacterContextEvidence(packet map[string]any) (map[string]any, []characterContextEvidenceItem, error) {
	common := make(map[string]any, len(characterContextCommonKeys))
	remaining := make(map[string]any, len(packet))
	for key, value := range packet {
		if _, ok := characterContextCommonKeys[key]; ok {
			common[key] = value
			continue
		}
		remaining[key] = value
	}
	var items []characterContextEvidenceItem
	for _, key := range characterContextEvidenceOrder {
		value, ok := remaining[key]
		if !ok {
			continue
		}
		delete(remaining, key)
		var err error
		items, err = appendCharacterContextEvidence(items, "/"+escapeCharacterContextPath(key), value)
		if err != nil {
			return nil, nil, err
		}
	}
	keys := make([]string, 0, len(remaining))
	for key := range remaining {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		var err error
		items, err = appendCharacterContextEvidence(items, "/"+escapeCharacterContextPath(key), remaining[key])
		if err != nil {
			return nil, nil, err
		}
	}
	return common, items, nil
}

func appendCharacterContextEvidence(
	items []characterContextEvidenceItem,
	path string,
	value any,
) ([]characterContextEvidenceItem, error) {
	normalized, err := normalizeCharacterContextEvidence(value)
	if err != nil {
		return nil, fmt.Errorf("normalize Character context evidence %s: %w", path, err)
	}
	item := characterContextEvidenceItem{Path: path, Value: normalized}
	if characterContextEvidenceItemSize(item) <= characterContextItemMaxBytes {
		return append(items, item), nil
	}
	switch typed := normalized.(type) {
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			items, err = appendCharacterContextEvidence(items, path+"/"+escapeCharacterContextPath(key), typed[key])
			if err != nil {
				return nil, err
			}
		}
		return items, nil
	case []any:
		for index, entry := range typed {
			items, err = appendCharacterContextEvidence(items, fmt.Sprintf("%s/%d", path, index), entry)
			if err != nil {
				return nil, err
			}
		}
		return items, nil
	case string:
		return append(items, splitCharacterContextString(path, typed)...), nil
	default:
		return nil, fmt.Errorf("Character context evidence %s cannot fit one bounded page: %w", path, errs.ErrToolPrecondition)
	}
}

func normalizeCharacterContextEvidence(value any) (any, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var normalized any
	if err := json.Unmarshal(data, &normalized); err != nil {
		return nil, err
	}
	return normalized, nil
}

func splitCharacterContextString(path, value string) []characterContextEvidenceItem {
	runes := []rune(value)
	if len(runes) == 0 {
		return []characterContextEvidenceItem{{Path: path, Value: ""}}
	}
	var chunks []string
	for len(runes) > 0 {
		length := len(runes)
		if length > 4096 {
			length = 4096
		}
		for length > 1 {
			probe := characterContextEvidenceItem{Path: path, Value: string(runes[:length])}
			if characterContextEvidenceItemSize(probe) <= characterContextItemMaxBytes {
				break
			}
			length /= 2
		}
		chunks = append(chunks, string(runes[:length]))
		runes = runes[length:]
	}
	items := make([]characterContextEvidenceItem, 0, len(chunks))
	for index, chunk := range chunks {
		items = append(items, characterContextEvidenceItem{
			Path:  path,
			Value: chunk,
			Chunk: &characterContextChunk{Index: index, Total: len(chunks)},
		})
	}
	return items
}

func characterContextEvidenceItemSize(item characterContextEvidenceItem) int {
	data, _ := json.Marshal(item)
	return len(data)
}

func packCharacterContextPages(
	common map[string]any,
	items []characterContextEvidenceItem,
	request characterContextArgs,
	binding domain.CharacterCardBinding,
	attempt int,
	evidenceDigest string,
) ([]map[string]any, error) {
	if len(items) == 0 {
		return nil, fmt.Errorf("paged Character context has no evidence items: %w", errs.ErrToolPrecondition)
	}
	groups := make([][]characterContextEvidenceItem, 0, 2)
	current := make([]characterContextEvidenceItem, 0)
	for _, item := range items {
		trial := append(append([]characterContextEvidenceItem(nil), current...), item)
		pageIndex := len(groups)
		packet := newCharacterContextPage(common, trial, pageIndex, len(items), len(items), request, binding, attempt, evidenceDigest)
		if err := finalizeCharacterContextBudget(packet); err == nil {
			current = trial
			continue
		}
		if len(current) == 0 {
			return nil, fmt.Errorf("Character context evidence item %s exceeds page budget: %w", item.Path, errs.ErrToolPrecondition)
		}
		groups = append(groups, current)
		current = []characterContextEvidenceItem{item}
		packet = newCharacterContextPage(common, current, len(groups), len(items), len(items), request, binding, attempt, evidenceDigest)
		if err := finalizeCharacterContextBudget(packet); err != nil {
			return nil, fmt.Errorf("Character context evidence item %s exceeds page budget: %w", item.Path, err)
		}
	}
	groups = append(groups, current)

	pages := make([]map[string]any, 0, len(groups))
	for index, group := range groups {
		packet := newCharacterContextPage(common, group, index, len(groups), len(items), request, binding, attempt, evidenceDigest)
		if err := finalizeCharacterContextBudget(packet); err != nil {
			return nil, fmt.Errorf("finalize Character context page %d: %w", index, err)
		}
		pages = append(pages, packet)
	}
	return pages, nil
}

func newCharacterContextPage(
	common map[string]any,
	items []characterContextEvidenceItem,
	index int,
	totalPages int,
	totalItems int,
	request characterContextArgs,
	binding domain.CharacterCardBinding,
	attempt int,
	evidenceDigest string,
) map[string]any {
	packet := cloneCharacterContextMap(common)
	page := map[string]any{
		"index":       index,
		"item_count":  len(items),
		"complete":    index == totalPages-1,
		"instruction": "Read every page in order. Evidence item paths are JSON Pointers into the compatible unpaged packet; concatenate same-path string chunks by chunk.index. If next_cursor is present, call character_context again with the same run_id/mode and that exact cursor before saving.",
	}
	if index < totalPages-1 {
		page["next_cursor"] = encodeCharacterContextCursor(characterContextCursor{
			Version:                  1,
			RunID:                    strings.TrimSpace(request.RunID),
			Mode:                     request.Mode,
			Page:                     index + 1,
			Attempt:                  attempt,
			EvidenceDigest:           evidenceDigest,
			FoundationRevision:       binding.Candidate.FoundationRevision,
			FoundationAuditSignature: binding.Candidate.FoundationAuditSignature,
			InputDigest:              binding.InputDigest,
		})
	}
	packet["context_manifest"] = map[string]any{
		"version":              1,
		"evidence_digest":      evidenceDigest,
		"total_pages":          totalPages,
		"total_evidence_items": totalItems,
		"lossless":             true,
		"path_format":          "JSON Pointer",
	}
	packet["context_page"] = page
	packet["evidence_items"] = items
	return packet
}

func encodeCharacterContextCursor(cursor characterContextCursor) string {
	payload, _ := json.Marshal(cursor)
	signature := characterContextCursorSignature(payload)
	return base64.RawURLEncoding.EncodeToString(payload) + "." + base64.RawURLEncoding.EncodeToString(signature)
}

func decodeCharacterContextCursor(value string) (characterContextCursor, error) {
	parts := strings.Split(strings.TrimSpace(value), ".")
	if len(parts) != 2 {
		return characterContextCursor{}, fmt.Errorf("character_context cursor is malformed: %w", errs.ErrToolArgs)
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return characterContextCursor{}, fmt.Errorf("decode character_context cursor payload: %w: %w", errs.ErrToolArgs, err)
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return characterContextCursor{}, fmt.Errorf("decode character_context cursor signature: %w: %w", errs.ErrToolArgs, err)
	}
	expected := characterContextCursorSignature(payload)
	if len(signature) != len(expected) || subtle.ConstantTimeCompare(signature, expected) != 1 {
		return characterContextCursor{}, fmt.Errorf("character_context cursor signature is invalid: %w", errs.ErrToolArgs)
	}
	var cursor characterContextCursor
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cursor); err != nil {
		return characterContextCursor{}, fmt.Errorf("decode character_context cursor: %w: %w", errs.ErrToolArgs, err)
	}
	return cursor, nil
}

func characterContextCursorSignature(payload []byte) []byte {
	digest := sha256.New()
	_, _ = digest.Write([]byte("character-context-cursor-v1\x00"))
	_, _ = digest.Write(payload)
	return digest.Sum(nil)
}

func validateCharacterContextCursor(
	cursor characterContextCursor,
	request characterContextArgs,
	binding domain.CharacterCardBinding,
	attempt int,
	evidenceDigest string,
) error {
	if cursor.Version != 1 || cursor.RunID != strings.TrimSpace(request.RunID) || cursor.Mode != request.Mode ||
		cursor.Attempt != attempt || cursor.EvidenceDigest != evidenceDigest ||
		cursor.FoundationRevision != binding.Candidate.FoundationRevision ||
		cursor.FoundationAuditSignature != binding.Candidate.FoundationAuditSignature ||
		cursor.InputDigest != binding.InputDigest {
		return fmt.Errorf("character_context cursor belongs to a stale or different evidence snapshot: %w", errs.ErrToolConflict)
	}
	return nil
}

func escapeCharacterContextPath(value string) string {
	value = strings.ReplaceAll(value, "~", "~0")
	return strings.ReplaceAll(value, "/", "~1")
}

func cloneCharacterContextMap(source map[string]any) map[string]any {
	clone := make(map[string]any, len(source)+1)
	for key, value := range source {
		clone[key] = value
	}
	return clone
}

func validateCharacterContextBudget(packet map[string]any) error {
	return finalizeCharacterContextBudget(packet)
}

func finalizeCharacterContextBudget(packet map[string]any) error {
	packet["context_budget"] = map[string]any{
		"bytes":     characterContextMaxBytes,
		"max_bytes": characterContextMaxBytes,
		"bounded":   true,
	}
	for range 3 {
		data, err := json.Marshal(packet)
		if err != nil {
			return fmt.Errorf("marshal bounded Character context: %w", err)
		}
		if len(data) > characterContextMaxBytes {
			delete(packet, "context_budget")
			return fmt.Errorf(
				"bounded Character context is %d bytes (budget %d); source character evidence must be compacted further: %w",
				len(data),
				characterContextMaxBytes,
				errs.ErrToolPrecondition,
			)
		}
		budget := packet["context_budget"].(map[string]any)
		if budget["bytes"] == len(data) {
			return nil
		}
		budget["bytes"] = len(data)
	}
	return nil
}

func limitStrings(values []string, limit int) []string {
	return limitSlice(values, limit)
}

func limitSlice[T any](values []T, limit int) []T {
	if len(values) > limit {
		values = values[:limit]
	}
	return append([]T(nil), values...)
}

func buildAdaptationSourceCharacterIndex(
	st *store.Store,
	coreCast *domain.CoreCastContract,
) (domain.AdaptationSourceCharacterIndex, error) {
	source, err := st.Adaptation.LoadSourceFoundation()
	if err != nil {
		return domain.AdaptationSourceCharacterIndex{}, fmt.Errorf("load source foundation for character index: %w", err)
	}
	reports, err := st.Adaptation.LoadCompleteSourceReports()
	if err != nil {
		return domain.AdaptationSourceCharacterIndex{}, fmt.Errorf("load source reports for character index: %w", err)
	}
	dossier, err := st.Adaptation.LoadCoCreateDossier()
	if err != nil {
		return domain.AdaptationSourceCharacterIndex{}, fmt.Errorf("load source dossier for character index: %w", err)
	}
	return domain.BuildAdaptationSourceCharacterIndex(source, reports, dossier, coreCast)
}

func adaptationSourceCharacterIDs(index domain.AdaptationSourceCharacterIndex) []string {
	ids := make([]string, 0, len(index.Characters))
	for _, character := range index.Characters {
		ids = append(ids, character.ID)
	}
	sort.Strings(ids)
	return ids
}

func foundationCharacterIDs(foundation domain.StoryFoundation) []string {
	ids := make([]string, 0, len(foundation.Characters))
	for _, character := range foundation.Characters {
		if id := strings.TrimSpace(character.ID); id != "" {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids
}

func requireCharacterBinding(
	revision int64,
	auditSignature, candidateDigest, inputDigest string,
	current domain.CharacterCardBinding,
) error {
	if revision != current.Candidate.FoundationRevision ||
		strings.TrimSpace(auditSignature) != current.Candidate.FoundationAuditSignature ||
		strings.TrimSpace(candidateDigest) != current.Candidate.CharacterContentDigest ||
		strings.TrimSpace(inputDigest) != current.InputDigest {
		return fmt.Errorf("character candidate or evidence signature is stale/conflict: %w", errs.ErrToolConflict)
	}
	return nil
}

func validateCharacterRunIdentity(runID string, mode CharacterRunMode) error {
	if strings.TrimSpace(runID) == "" {
		return fmt.Errorf("character run_id is required: %w", errs.ErrToolArgs)
	}
	if mode != CharacterRunAnalyze && mode != CharacterRunReview {
		return fmt.Errorf("character mode %q is invalid: %w", mode, errs.ErrToolArgs)
	}
	return nil
}

func validateCharacterSubmissionIdentity(idempotencyKey string, revision int64, audit, candidate, input string) error {
	if strings.TrimSpace(idempotencyKey) == "" {
		return fmt.Errorf("character idempotency_key is required: %w", errs.ErrToolArgs)
	}
	if revision < 0 || len(strings.TrimSpace(audit)) != 64 || len(strings.TrimSpace(candidate)) != 64 || len(strings.TrimSpace(input)) != 64 {
		return fmt.Errorf("character base revision/signatures are invalid: %w", errs.ErrToolArgs)
	}
	return nil
}

func appendCompletenessFindings(
	findings []domain.CharacterCardReviewFinding,
	completeness []domain.CharacterCardCompletenessResult,
) []domain.CharacterCardReviewFinding {
	existing := make(map[string]struct{}, len(findings))
	for _, finding := range findings {
		existing[finding.ID] = struct{}{}
	}
	for _, result := range completeness {
		for _, missing := range result.Missing {
			if missing.Severity != domain.CharacterCardSeverityBlocking {
				continue
			}
			id := "completeness:" + result.CharacterID + ":" + missing.Code
			if _, exists := existing[id]; exists {
				continue
			}
			findings = append(findings, domain.CharacterCardReviewFinding{
				ID:              id,
				Scope:           domain.CharacterCardFindingCharacter,
				CharacterID:     result.CharacterID,
				Location:        missing.Field,
				Severity:        domain.CharacterCardSeverityBlocking,
				IssueType:       "deterministic_completeness",
				Description:     missing.Description,
				EvidenceSummary: "CharacterCard deterministic completeness gate",
				Suggestion:      "complete the required field and run a fresh review",
				Blocking:        true,
			})
			existing[id] = struct{}{}
		}
	}
	sort.Slice(findings, func(i, j int) bool { return findings[i].ID < findings[j].ID })
	return findings
}

func appendCoverageFindings(
	findings []domain.CharacterCardReviewFinding,
	coverage domain.AdaptationCharacterCoverage,
) []domain.CharacterCardReviewFinding {
	existing := make(map[string]struct{}, len(findings))
	for _, finding := range findings {
		existing[finding.ID] = struct{}{}
	}
	for _, decision := range coverage.Decisions {
		if !decision.Blocking {
			continue
		}
		id := "coverage:" + decision.SourceCharacterID
		if _, exists := existing[id]; exists {
			continue
		}
		findings = append(findings, domain.CharacterCardReviewFinding{
			ID:              id,
			Scope:           domain.CharacterCardFindingGlobal,
			Location:        "source_mappings",
			Severity:        domain.CharacterCardSeverityBlocking,
			IssueType:       "source_character_coverage",
			Description:     fmt.Sprintf("应覆盖的来源角色 %s 缺少映射或排除决定", decision.CanonicalName),
			EvidenceSummary: strings.Join(decision.Reasons, "；"),
			Suggestion:      "补充 keep/rename/merge/split/exclude 决定并重新独立审核",
			Blocking:        true,
		})
		existing[id] = struct{}{}
	}
	sort.Slice(findings, func(i, j int) bool { return findings[i].ID < findings[j].ID })
	return findings
}

func hasBlockingCharacterFinding(findings []domain.CharacterCardReviewFinding) bool {
	for _, finding := range findings {
		if finding.Blocking || finding.Severity == domain.CharacterCardSeverityBlocking {
			return true
		}
	}
	return false
}

func decodeCharacterToolArgs(data json.RawMessage, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("invalid character tool args: %w: %w", errs.ErrToolArgs, err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return fmt.Errorf("invalid character tool args trailing JSON: %w", errs.ErrToolArgs)
	}
	return nil
}

func characterSchema() map[string]any {
	contrast := schema.Object(
		schema.Property("surface", schema.String("Observable presentation")).Required(),
		schema.Property("depth", schema.String("Contrasting motive or behavior")).Required(),
	)
	backstory := schema.Object(
		schema.Property("event", schema.String("Past event relevant to current choices")).Required(),
		schema.Property("impact", schema.String("Present causal impact")).Required(),
	)
	initial := schema.Object(
		schema.Property("identity", schema.String("Chapter-zero identity")).Required(),
		schema.Property("situation", schema.String("Chapter-zero situation")).Required(),
		schema.Property("emotion", schema.String("Chapter-zero emotional state")).Required(),
		schema.Property("resources", schema.Array("Chapter-zero resources", schema.String(""))).Required(),
		schema.Property("relationships", schema.String("Chapter-zero relationship state")).Required(),
	)
	knowledge := schema.Object(
		schema.Property("known", schema.Array("Facts the character knows", schema.String(""))).Required(),
		schema.Property("unknown", schema.Array("Facts the character does not know", schema.String(""))).Required(),
		schema.Property("misconceptions", schema.Array("False beliefs", schema.String(""))).Required(),
		schema.Property("forbidden", schema.Array("Knowledge the character must not use", schema.String(""))).Required(),
	)
	return schema.Object(
		schema.Property("id", schema.String("Stable character ID; empty only when deterministic generation is intended")).Required(),
		schema.Property("name", schema.String("Character name")).Required(),
		schema.Property("aliases", schema.Array("Aliases", schema.String(""))).Required(),
		schema.Property("role", schema.String("Identity or story responsibility")).Required(),
		schema.Property("gender", schema.Enum("Stable gender/pronoun contract", "male", "female", "nonbinary", "unspecified")).Required(),
		schema.Property("description", schema.String("Character description")).Required(),
		schema.Property("arc", schema.String("Causal character arc")).Required(),
		schema.Property("traits", schema.Array("Distinct traits", schema.String(""))).Required(),
		schema.Property("tier", schema.Enum("Information-density tier", "core", "important", "secondary", "decorative")).Required(),
		schema.Property("faction", schema.String("Faction or empty string")).Required(),
		schema.Property("goal", schema.String("External goal or empty string")).Required(),
		schema.Property("motivation", schema.String("Internal motivation or empty string")).Required(),
		schema.Property("conflict", schema.String("Core conflict or empty string")).Required(),
		schema.Property("voice", schema.String("Language/behavior voice or empty string")).Required(),
		schema.Property("constraints", schema.Array("Behavior constraints", schema.String(""))).Required(),
		schema.Property("contrast_details", schema.Array("Character contrasts", contrast)).Required(),
		schema.Property("key_backstory", schema.Array("Causally relevant backstory", backstory)).Required(),
		schema.Property("initial_state", initial).Required(),
		schema.Property("knowledge_boundary", knowledge).Required(),
		schema.Property("notes", schema.String("Compact notes or empty string")).Required(),
	)
}

func characterRelationshipSchema() map[string]any {
	return schema.Object(
		schema.Property("id", schema.String("Stable relationship ID or empty string")).Required(),
		schema.Property("source_character_id", schema.String("Source character ID")).Required(),
		schema.Property("target_character_id", schema.String("Target character ID")).Required(),
		schema.Property("type", schema.Enum("Relationship type", "ally", "rival", "family", "romantic", "mentor", "professional", "other")).Required(),
		schema.Property("label", schema.String("Readable relationship label or empty string")).Required(),
		schema.Property("direction", schema.Enum("Direction", "directed", "bidirectional", "undirected")).Required(),
		schema.Property("status", schema.Enum("Planned state", "planned", "active", "strained", "broken", "resolved")).Required(),
		schema.Property("description", schema.String("Relationship dynamics or empty string")).Required(),
		schema.Property("since", schema.String("Starting point or empty string")).Required(),
		schema.Property("tags", schema.Array("Relationship tags", schema.String(""))).Required(),
		schema.Property("constraints", schema.Array("Relationship constraints", schema.String(""))).Required(),
	)
}

func characterSourceMappingSchema() map[string]any {
	evidence := schema.Object(
		schema.Property("kind", schema.Enum("Evidence classification", "source_fact", "adaptation_decision", "target_original_addition")).Required(),
		schema.Property("reference", schema.String("Bounded evidence reference")).Required(),
		schema.Property("summary", schema.String("Compact evidence summary or empty string")).Required(),
	)
	return schema.Object(
		schema.Property("id", schema.String("Stable mapping ID")).Required(),
		schema.Property("action", schema.Enum("Mapping action", "keep", "rename", "merge", "split", "exclude", "target_original")).Required(),
		schema.Property("source_character_ids", schema.Array("Source IDs", schema.String(""))).Required(),
		schema.Property("target_character_ids", schema.Array("Target IDs", schema.String(""))).Required(),
		schema.Property("rationale", schema.String("Adaptation rationale")).Required(),
		schema.Property("evidence", schema.Array("Classified evidence", evidence)).Required(),
	)
}

func characterFindingSchema() map[string]any {
	return schema.Object(
		schema.Property("id", schema.String("Stable finding ID")).Required(),
		schema.Property("scope", schema.Enum("Finding scope", "global", "character")).Required(),
		schema.Property("character_id", schema.String("Character ID or empty string for global findings")).Required(),
		schema.Property("location", schema.String("Field/path or empty string")).Required(),
		schema.Property("severity", schema.Enum("Severity", "warning", "blocking")).Required(),
		schema.Property("issue_type", schema.String("Knowledge/voice/behavior/arc/relationship/coverage/duplication issue type")).Required(),
		schema.Property("description", schema.String("Finding description")).Required(),
		schema.Property("evidence_summary", schema.String("Compact evidence summary")).Required(),
		schema.Property("suggestion", schema.String("Repair suggestion or empty string")).Required(),
		schema.Property("blocking", schema.Bool("Whether this finding blocks pass")).Required(),
	)
}
