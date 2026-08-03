package domain

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"
)

type ExpansionLocationKind string

const (
	ExpansionInside    ExpansionLocationKind = "inside"
	ExpansionBefore    ExpansionLocationKind = "before"
	ExpansionAfter     ExpansionLocationKind = "after"
	ExpansionBetween   ExpansionLocationKind = "between"
	ExpansionEndArc    ExpansionLocationKind = "end_arc"
	ExpansionEndVolume ExpansionLocationKind = "end_volume"
	ExpansionBookEnd   ExpansionLocationKind = "book_end"
)

func (kind ExpansionLocationKind) Valid() bool {
	switch kind {
	case ExpansionInside, ExpansionBefore, ExpansionAfter, ExpansionBetween, ExpansionEndArc, ExpansionEndVolume, ExpansionBookEnd:
		return true
	default:
		return false
	}
}

type ExpansionAdjustment string

const (
	ExpansionAdjustmentDefault        ExpansionAdjustment = "default"
	ExpansionAdjustmentCompact        ExpansionAdjustment = "compact"
	ExpansionAdjustmentFull           ExpansionAdjustment = "full"
	ExpansionAdjustmentSeparateVolume ExpansionAdjustment = "separate_volume"
)

func (adjustment ExpansionAdjustment) Valid() bool {
	switch adjustment {
	case ExpansionAdjustmentDefault, ExpansionAdjustmentCompact, ExpansionAdjustmentFull, ExpansionAdjustmentSeparateVolume:
		return true
	default:
		return false
	}
}

type ExpansionForm string

const (
	ExpansionFormExpandCurrent ExpansionForm = "expand_current"
	ExpansionFormInsertOne     ExpansionForm = "insert_one"
	ExpansionFormInsertMany    ExpansionForm = "insert_multiple"
	ExpansionFormNewArc        ExpansionForm = "new_arc"
	ExpansionFormNewVolume     ExpansionForm = "new_volume"
	ExpansionFormEpilogue      ExpansionForm = "epilogue"
)

func (form ExpansionForm) Valid() bool {
	switch form {
	case ExpansionFormExpandCurrent, ExpansionFormInsertOne, ExpansionFormInsertMany, ExpansionFormNewArc, ExpansionFormNewVolume, ExpansionFormEpilogue:
		return true
	default:
		return false
	}
}

// ExpansionRequest deliberately omits mode. Mode is read from project truth.
type ExpansionRequest struct {
	Location                   ExpansionLocationKind `json:"location"`
	ReferenceIDs               []string              `json:"reference_ids,omitempty"`
	Sentence                   string                `json:"sentence"`
	Adjustment                 ExpansionAdjustment   `json:"adjustment,omitempty"`
	ExpectedStructureRevision  int                   `json:"expected_structure_revision"`
	ExpectedStructureSignature string                `json:"expected_structure_signature"`
	IdempotencyKey             string                `json:"idempotency_key"`
	ClientRequestID            string                `json:"client_request_id,omitempty"`
}

func (request ExpansionRequest) Validate() error {
	if !request.Location.Valid() {
		return fmt.Errorf("unsupported expansion location %q", request.Location)
	}
	if strings.TrimSpace(request.Sentence) == "" {
		return fmt.Errorf("one-line expansion description is required")
	}
	if len([]rune(strings.TrimSpace(request.Sentence))) > 500 {
		return fmt.Errorf("one-line expansion description exceeds 500 characters")
	}
	if request.Adjustment == "" {
		request.Adjustment = ExpansionAdjustmentDefault
	}
	if !request.Adjustment.Valid() {
		return fmt.Errorf("unsupported expansion adjustment %q", request.Adjustment)
	}
	if request.ExpectedStructureRevision <= 0 || strings.TrimSpace(request.ExpectedStructureSignature) == "" {
		return fmt.Errorf("expected structure revision and signature are required")
	}
	if strings.TrimSpace(request.IdempotencyKey) == "" {
		return fmt.Errorf("expansion idempotency key is required")
	}
	seen := make(map[string]struct{}, len(request.ReferenceIDs))
	for _, id := range request.ReferenceIDs {
		id = strings.TrimSpace(id)
		if id == "" {
			return fmt.Errorf("expansion reference ID is empty")
		}
		if _, duplicate := seen[id]; duplicate {
			return fmt.Errorf("expansion reference ID %q is duplicated", id)
		}
		seen[id] = struct{}{}
	}
	return nil
}

type ExpansionDramaticAssessment struct {
	Goal                 string                    `json:"goal"`
	Conflict             string                    `json:"conflict"`
	Choice               string                    `json:"choice"`
	Cost                 string                    `json:"cost"`
	Result               string                    `json:"result"`
	CharacterStageChange string                    `json:"character_stage_change"`
	CharacterBeforeStage string                    `json:"character_before_stage"`
	CharacterAfterStage  string                    `json:"character_after_stage"`
	IndependentClimax    string                    `json:"independent_climax,omitempty"`
	IrreversibleExit     string                    `json:"irreversible_exit,omitempty"`
	CurrentFit           string                    `json:"current_fit"`
	VolumePacingEffect   string                    `json:"volume_pacing_effect"`
	AdaptationEffect     string                    `json:"adaptation_effect,omitempty"`
	TypedClaims          *ExpansionDramaticFactSet `json:"typed_claims,omitempty"`
}

// ExpansionDramaticFactSet is the executable dramatic schema shared by an
// assessment and the candidate chapter it describes. Free-form prose remains
// presentation material; audit decisions are made only from these enums.
type ExpansionDramaticFactSet struct {
	SchemaVersion   string `json:"schema_version"`
	GoalState       string `json:"goal_state"`
	ConflictState   string `json:"conflict_state"`
	ChoiceState     string `json:"choice_state"`
	CostState       string `json:"cost_state"`
	ResultState     string `json:"result_state"`
	CharacterBefore string `json:"character_before"`
	CharacterAfter  string `json:"character_after"`
	ClimaxState     string `json:"climax_state"`
	ExitState       string `json:"exit_state"`
	ImpactState     string `json:"impact_state"`
}

func (facts ExpansionDramaticFactSet) Validate() error {
	if facts.SchemaVersion != ExpansionDramaticFactsSchemaV1 {
		return fmt.Errorf("unsupported dramatic typed fact schema %q", facts.SchemaVersion)
	}
	allowed := []struct {
		name  string
		value string
		set   []string
	}{
		{"goal_state", facts.GoalState, []string{"pursued", "abandoned"}},
		{"conflict_state", facts.ConflictState, []string{"active", "resolved"}},
		{"choice_state", facts.ChoiceState, []string{"committed", "deferred"}},
		{"cost_state", facts.CostState, []string{"paid", "avoided"}},
		{"result_state", facts.ResultState, []string{"achieved", "failed"}},
		{"character_before", facts.CharacterBefore, []string{"passive", "reactive", "dependent", "active", "proactive", "independent"}},
		{"character_after", facts.CharacterAfter, []string{"passive", "reactive", "dependent", "active", "proactive", "independent"}},
		{"climax_state", facts.ClimaxState, []string{"occurred", "absent"}},
		{"exit_state", facts.ExitState, []string{"irreversible", "reversible"}},
		{"impact_state", facts.ImpactState, []string{"required", "recommended", "none"}},
	}
	for _, field := range allowed {
		if !slices.Contains(field.set, field.value) {
			return fmt.Errorf("dramatic typed fact %s has unsupported state %q", field.name, field.value)
		}
	}
	if facts.CharacterBefore == facts.CharacterAfter {
		return fmt.Errorf("dramatic typed character transition must change state")
	}
	return nil
}

const ExpansionDramaticFactsSchemaV1 = "expansion-dramatic-facts/v1"

const (
	ExpansionOriginSchemaV1 = "expansion-origin/v1"
	ExpansionOriginOneLine  = "one-line-expansion"
)

// ExpansionOrigin is content-independent provenance for a chapter created or
// revised by the one-line expansion workflow. PreviewID is a stable journal
// identity, while SourceContractSignature binds the exact executable dramatic
// facts approved by that preview. The formal structure signature covers this
// value, so display-number projections cannot manufacture the provenance.
type ExpansionOrigin struct {
	SchemaVersion             string `json:"schema_version"`
	Kind                      string `json:"kind"`
	PreviewID                 string `json:"preview_id"`
	DramaticContractSignature string `json:"dramatic_contract_signature"`
}

func ExpansionDramaticFactsSignature(facts ExpansionDramaticFactSet) string {
	payload, _ := json.Marshal(facts)
	return ContentSignature(payload)
}

func NewExpansionOrigin(previewID string, facts ExpansionDramaticFactSet) (ExpansionOrigin, error) {
	if err := facts.Validate(); err != nil {
		return ExpansionOrigin{}, err
	}
	previewID = strings.TrimSpace(previewID)
	if previewID == "" {
		return ExpansionOrigin{}, fmt.Errorf("expansion origin preview identity is required")
	}
	return ExpansionOrigin{
		SchemaVersion:             ExpansionOriginSchemaV1,
		Kind:                      ExpansionOriginOneLine,
		PreviewID:                 previewID,
		DramaticContractSignature: ExpansionDramaticFactsSignature(facts),
	}, nil
}

func (origin ExpansionOrigin) Validate(facts *ExpansionDramaticFactSet) error {
	if origin.SchemaVersion != ExpansionOriginSchemaV1 || origin.Kind != ExpansionOriginOneLine ||
		strings.TrimSpace(origin.PreviewID) == "" || len(origin.DramaticContractSignature) != 64 {
		return fmt.Errorf("expansion origin identity is invalid")
	}
	if facts == nil || facts.Validate() != nil || ExpansionDramaticFactsSignature(*facts) != origin.DramaticContractSignature {
		return fmt.Errorf("expansion origin dramatic source contract is invalid")
	}
	return nil
}

func (assessment ExpansionDramaticAssessment) Validate(mode RevisionMode) error {
	required := []string{assessment.Goal, assessment.Conflict, assessment.Choice, assessment.Cost, assessment.Result, assessment.CharacterStageChange, assessment.CharacterBeforeStage, assessment.CharacterAfterStage, assessment.IndependentClimax, assessment.IrreversibleExit, assessment.CurrentFit, assessment.VolumePacingEffect}
	if slices.ContainsFunc(required, func(value string) bool { return strings.TrimSpace(value) == "" }) {
		return fmt.Errorf("expansion assessment requires causality, character before/after, climax, irreversible exit, fit, and pacing evidence")
	}
	if strings.EqualFold(strings.TrimSpace(assessment.CharacterBeforeStage), strings.TrimSpace(assessment.CharacterAfterStage)) {
		return fmt.Errorf("expansion assessment character before and after stages must differ")
	}
	if mode == RevisionModeAdaptation && strings.TrimSpace(assessment.AdaptationEffect) == "" {
		return fmt.Errorf("adaptation expansion requires coverage and protected-contract effect")
	}
	if mode == RevisionModeNormal && strings.TrimSpace(assessment.AdaptationEffect) != "" {
		return fmt.Errorf("normal expansion cannot carry adaptation evidence")
	}
	if assessment.TypedClaims != nil {
		if err := assessment.TypedClaims.Validate(); err != nil {
			return err
		}
	}
	return nil
}

type ExpansionOperation struct {
	Operation     StructureRevisionOperation `json:"operation"`
	Intent        string                     `json:"intent"`
	TargetID      string                     `json:"target_id,omitempty"`
	DestinationID string                     `json:"destination_id,omitempty"`
	Proposal      StructureRevisionProposal  `json:"proposal"`
}

type ExpansionRecommendation struct {
	Form                ExpansionForm               `json:"form"`
	Reason              string                      `json:"reason"`
	Location            ExpansionLocationKind       `json:"location"`
	ChapterCount        int                         `json:"chapter_count"`
	ChapterMinWords     int                         `json:"chapter_min_words"`
	ChapterMaxWords     int                         `json:"chapter_max_words"`
	TotalMinWords       int                         `json:"total_min_words"`
	TotalMaxWords       int                         `json:"total_max_words"`
	NewArc              bool                        `json:"new_arc,omitempty"`
	NewVolume           bool                        `json:"new_volume,omitempty"`
	OldSummary          string                      `json:"old_summary"`
	NewSummary          string                      `json:"new_summary"`
	Assessment          ExpansionDramaticAssessment `json:"assessment"`
	Impacts             []StructureImpactItem       `json:"impacts"`
	AuditChain          []string                    `json:"audit_chain"`
	ModeConstraints     []string                    `json:"mode_constraints"`
	OrderedOperations   []ExpansionOperation        `json:"ordered_operations"`
	SoftBudgetDelta     DynamicSoftBudget           `json:"soft_budget_delta"`
	AdaptationCandidate *AdaptationPlan             `json:"adaptation_candidate,omitempty"`
}

func (recommendation ExpansionRecommendation) Validate(mode RevisionMode) error {
	if !recommendation.Form.Valid() || !recommendation.Location.Valid() || strings.TrimSpace(recommendation.Reason) == "" {
		return fmt.Errorf("expansion recommendation form, location, and reason are required")
	}
	if err := recommendation.Assessment.Validate(mode); err != nil {
		return err
	}
	if recommendation.ChapterCount <= 0 || recommendation.ChapterMinWords <= 0 || recommendation.ChapterMaxWords < recommendation.ChapterMinWords || recommendation.TotalMinWords <= 0 || recommendation.TotalMaxWords < recommendation.TotalMinWords {
		return fmt.Errorf("expansion recommendation soft ranges are invalid")
	}
	if err := recommendation.SoftBudgetDelta.Validate(); err != nil {
		return fmt.Errorf("validate expansion soft budget: %w", err)
	}
	if len(recommendation.OrderedOperations) == 0 {
		return fmt.Errorf("expansion recommendation requires ordered kernel operations")
	}
	if len(recommendation.AuditChain) == 0 || len(recommendation.ModeConstraints) == 0 {
		return fmt.Errorf("expansion recommendation requires audit chain and mode constraints")
	}
	if recommendation.Form == ExpansionFormNewVolume || recommendation.NewVolume {
		if strings.TrimSpace(recommendation.Assessment.IndependentClimax) == "" || strings.TrimSpace(recommendation.Assessment.IrreversibleExit) == "" {
			return fmt.Errorf("new volume requires independent climax and irreversible exit evidence")
		}
	}
	if mode == RevisionModeAdaptation && recommendation.AdaptationCandidate == nil {
		return fmt.Errorf("adaptation recommendation requires a contract-aware candidate")
	}
	if mode == RevisionModeNormal && recommendation.AdaptationCandidate != nil {
		return fmt.Errorf("normal recommendation cannot carry adaptation candidate")
	}
	return nil
}

type ExpansionPreview struct {
	ID                       string                      `json:"preview_id"`
	Mode                     RevisionMode                `json:"mode"`
	Request                  ExpansionRequest            `json:"request"`
	Recommendation           ExpansionRecommendation     `json:"recommendation"`
	Candidate                []VolumeOutline             `json:"candidate"`
	BaseRevision             int                         `json:"base_revision"`
	BaseStructureSignature   string                      `json:"base_structure_signature"`
	CandidateSignature       string                      `json:"candidate_signature"`
	ExpiresAt                time.Time                   `json:"expires_at"`
	Obsolete                 bool                        `json:"obsolete"`
	Cancelled                bool                        `json:"cancelled"`
	ConfirmedRevisionID      string                      `json:"confirmed_revision_id,omitempty"`
	RevisionPreviewSignature string                      `json:"revision_preview_signature,omitempty"`
	Signature                string                      `json:"signature,omitempty"`
	KernelPreviews           []StructureRevisionPreview  `json:"kernel_previews"`
	DependencyReviews        []ExpansionDependencyReview `json:"dependency_reviews"`
}

// ExpansionDependencyReview is a durable result produced by the dependency
// reviewer. Unlike the old synthetic hash chain it records who reviewed which
// exact input, the reviewed output, the decision, and the dependency edges.
// The complete artifact is covered by both ArtifactSignature and the expansion
// preview HMAC.
type ExpansionDependencyReview struct {
	Stage             string   `json:"stage"`
	ScopeID           string   `json:"scope_id"`
	InputSignature    string   `json:"input_signature"`
	OutputSignature   string   `json:"output_signature"`
	PolicyVersion     string   `json:"policy_version"`
	ReviewerIdentity  string   `json:"reviewer_identity"`
	ReviewerPublicKey string   `json:"reviewer_public_key"`
	Decision          string   `json:"decision"`
	Findings          []string `json:"findings,omitempty"`
	DependencyIDs     []string `json:"dependency_ids,omitempty"`
	ArtifactSignature string   `json:"artifact_signature"`
}

func (review ExpansionDependencyReview) Validate() error {
	if strings.TrimSpace(review.Stage) == "" || strings.TrimSpace(review.ScopeID) == "" ||
		strings.TrimSpace(review.InputSignature) == "" || strings.TrimSpace(review.OutputSignature) == "" ||
		strings.TrimSpace(review.PolicyVersion) == "" || strings.TrimSpace(review.ReviewerIdentity) == "" ||
		strings.TrimSpace(review.ReviewerPublicKey) == "" ||
		strings.TrimSpace(review.ArtifactSignature) == "" {
		return fmt.Errorf("dependency review is missing signed identity or content bindings")
	}
	if review.Decision != "pass" && review.Decision != "needs_fix" {
		return fmt.Errorf("dependency review %s/%s has invalid decision", review.Stage, review.ScopeID)
	}
	if review.Decision == "needs_fix" && len(review.Findings) == 0 {
		return fmt.Errorf("dependency review %s/%s needs findings", review.Stage, review.ScopeID)
	}
	return nil
}
