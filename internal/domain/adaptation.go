package domain

import (
	"encoding/json"
	"fmt"
	"strings"
)

const (
	AdaptationGranularityChapter = "chapter"
	AdaptationGranularityArc     = "arc"
	AdaptationGranularityFree    = "free"
)

const (
	AdaptationPlanStatusProposal     = "proposal"
	AdaptationPlanStatusConfirmed    = "confirmed"
	AdaptationPlanStatusVolumeReview = "volume_review"
)

const (
	AdaptationRewriteFullRewrite     = "full_rewrite"
	AdaptationRewritePreserveDetails = "preserve_details"
)

const (
	AdaptationModelChapterTargetRunes      = 4000
	AdaptationModelChapterMaxRunes         = 5000
	AdaptationModelChapterTolerance        = 0.15
	AdaptationRevisionBatchContextMaxUnits = 10000
)

// AdaptationSourceManifest records the imported source novel identity.
type AdaptationSourceManifest struct {
	SourcePath   string             `json:"source_path"`
	ChapterCount int                `json:"chapter_count"`
	Chapters     []AdaptationSource `json:"chapters"`
}

// AdaptationSource is one source chapter snapshot saved under meta/adaptation.
type AdaptationSource struct {
	Chapter int    `json:"chapter"`
	Title   string `json:"title"`
	SHA256  string `json:"sha256"`
	Path    string `json:"path"`
	Runes   int    `json:"runes"`
}

// AdaptationSourceReport stores source-chapter analysis for adaptation planning.
type AdaptationSourceReport struct {
	Chapter           int                 `json:"chapter"`
	Title             string              `json:"title"`
	SourceSHA256      string              `json:"source_sha256,omitempty"`
	AnalyzerVersion   int                 `json:"analyzer_version,omitempty"`
	AnalyzerSignature string              `json:"analyzer_signature,omitempty"`
	Summary           string              `json:"summary"`
	Characters        []string            `json:"characters,omitempty"`
	CharacterProfiles []Character         `json:"character_profiles,omitempty"`
	CharacterFacts    []string            `json:"character_facts,omitempty"`
	KeyEvents         []string            `json:"key_events,omitempty"`
	SourceEvents      []AdaptationEvent   `json:"source_events,omitempty"`
	WorldRules        []string            `json:"world_rules,omitempty"`
	HookType          string              `json:"hook_type,omitempty"`
	DominantStrand    string              `json:"dominant_strand,omitempty"`
	Timeline          []TimelineEvent     `json:"timeline,omitempty"`
	Foreshadow        []ForeshadowUpdate  `json:"foreshadow,omitempty"`
	Relationships     []RelationshipEntry `json:"relationships,omitempty"`
	StateChanges      []StateChange       `json:"state_changes,omitempty"`
}

// AdaptationSourceFoundation is the reusable foundation inferred from the source.
type AdaptationSourceFoundation struct {
	Version            int                     `json:"version,omitempty"`
	GeneratedAt        string                  `json:"generated_at,omitempty"`
	SourcePath         string                  `json:"source_path,omitempty"`
	SourceChapterCount int                     `json:"source_chapter_count,omitempty"`
	SourceSignature    string                  `json:"source_signature,omitempty"`
	ReportSignature    string                  `json:"report_signature,omitempty"`
	PromptVersion      string                  `json:"prompt_version,omitempty"`
	BatchRuneLimit     int                     `json:"batch_rune_limit,omitempty"`
	Premise            string                  `json:"premise"`
	Characters         []Character             `json:"characters"`
	Relationships      []CharacterRelationship `json:"relationships"`
	WorldRules         []WorldRule             `json:"world_rules"`
	Volumes            []VolumeOutline         `json:"volumes"`
	Compass            *StoryCompass           `json:"compass,omitempty"`
}

// AdaptationSourceFoundationBatch is a resumable checkpoint for source
// foundation aggregation. Level 0 batches are direct source-report merges;
// higher levels are summary merges over previous batches.
type AdaptationSourceFoundationBatch struct {
	Version            int                        `json:"version"`
	Kind               string                     `json:"kind"`
	Level              int                        `json:"level"`
	Index              int                        `json:"index"`
	SourceFrom         int                        `json:"source_from"`
	SourceTo           int                        `json:"source_to"`
	SourcePath         string                     `json:"source_path,omitempty"`
	SourceChapterCount int                        `json:"source_chapter_count,omitempty"`
	SourceSignature    string                     `json:"source_signature"`
	InputSignature     string                     `json:"input_signature"`
	PromptVersion      string                     `json:"prompt_version"`
	BatchRuneLimit     int                        `json:"batch_rune_limit,omitempty"`
	GeneratedAt        string                     `json:"generated_at,omitempty"`
	Foundation         AdaptationSourceFoundation `json:"foundation"`
}

// AdaptationCoCreateDossier is the compact all-book packet used by adaptation
// co-create. It is derived from source reports, not raw source prose.
type AdaptationCoCreateDossier struct {
	Version            int                                `json:"version"`
	PromptVersion      string                             `json:"prompt_version"`
	SourcePath         string                             `json:"source_path,omitempty"`
	SourceChapterCount int                                `json:"source_chapter_count"`
	SourceSignature    string                             `json:"source_signature"`
	BatchSize          int                                `json:"batch_size"`
	BatchRuneLimit     int                                `json:"batch_rune_limit,omitempty"`
	GeneratedAt        string                             `json:"generated_at,omitempty"`
	Overview           string                             `json:"overview,omitempty"`
	Mainline           []string                           `json:"mainline,omitempty"`
	PlotThreads        []string                           `json:"plot_threads,omitempty"`
	CharacterArcs      []string                           `json:"character_arcs,omitempty"`
	WorldConstraints   []string                           `json:"world_constraints,omitempty"`
	RelationshipMap    []AdaptationRelationshipSignal     `json:"relationship_map,omitempty"`
	HeroineSignals     []AdaptationRelationshipSignal     `json:"heroine_signals,omitempty"`
	AmbiguityRisks     []AdaptationRelationshipRisk       `json:"ambiguity_risks,omitempty"`
	CoupleMilestones   []AdaptationRelationshipSignal     `json:"couple_milestones,omitempty"`
	AdaptationNotes    []string                           `json:"adaptation_notes,omitempty"`
	Batches            []AdaptationCoCreateDossierBatch   `json:"batches"`
	SourceChapters     []AdaptationDossierSourceSignature `json:"source_chapters,omitempty"`
}

// AdaptationCoCreateDossierBatch is one resumable source-report analysis batch.
type AdaptationCoCreateDossierBatch struct {
	Index               int                            `json:"index"`
	SourceFrom          int                            `json:"source_from"`
	SourceTo            int                            `json:"source_to"`
	SourceSignature     string                         `json:"source_signature"`
	PromptVersion       string                         `json:"prompt_version"`
	GeneratedAt         string                         `json:"generated_at,omitempty"`
	PlotPhase           string                         `json:"plot_phase,omitempty"`
	KeyCausality        []string                       `json:"key_causality,omitempty"`
	PlotThreads         []string                       `json:"plot_threads,omitempty"`
	CharacterArcs       []string                       `json:"character_arcs,omitempty"`
	WorldConstraints    []string                       `json:"world_constraints,omitempty"`
	MajorCharacters     []string                       `json:"major_characters,omitempty"`
	RelationshipSignals []AdaptationRelationshipSignal `json:"relationship_signals,omitempty"`
	HeroineSignals      []AdaptationRelationshipSignal `json:"heroine_signals,omitempty"`
	AmbiguityRisks      []AdaptationRelationshipRisk   `json:"ambiguity_risks,omitempty"`
	CoupleMilestones    []AdaptationRelationshipSignal `json:"couple_milestones,omitempty"`
	AdaptationNotes     []string                       `json:"adaptation_notes,omitempty"`
}

type AdaptationDossierSourceSignature struct {
	Chapter int    `json:"chapter"`
	SHA256  string `json:"sha256"`
}

type AdaptationRelationshipSignal struct {
	Chapters   []int    `json:"chapters,omitempty"`
	Characters []string `json:"characters,omitempty"`
	Type       string   `json:"type,omitempty"`
	Summary    string   `json:"summary"`
	Evidence   string   `json:"evidence,omitempty"`
}

type AdaptationRelationshipRisk struct {
	Chapters   []int    `json:"chapters,omitempty"`
	Characters []string `json:"characters,omitempty"`
	Risk       string   `json:"risk"`
	Evidence   string   `json:"evidence,omitempty"`
	Severity   string   `json:"severity,omitempty"`
	Suggestion string   `json:"suggestion,omitempty"`
}

// AdaptationCoCreateIntent captures the user's adaptation goal for the
// pre-draft co-create briefing. The raw request stays available so the model
// can adapt its attention instead of following a fixed risk checklist.
type AdaptationCoCreateIntent struct {
	Version           int      `json:"version"`
	RawRequest        string   `json:"raw_request"`
	Granularity       string   `json:"granularity,omitempty"`
	RewritePolicy     string   `json:"rewrite_policy,omitempty"`
	WordTolerance     float64  `json:"word_tolerance,omitempty"`
	Goals             []string `json:"goals,omitempty"`
	HeroineNames      []string `json:"heroine_names,omitempty"`
	RestrictedNames   []string `json:"restricted_names,omitempty"`
	RelationshipRules []string `json:"relationship_rules,omitempty"`
	PreserveRules     []string `json:"preserve_rules,omitempty"`
	IntentHash        string   `json:"intent_hash,omitempty"`
	GeneratedAt       string   `json:"generated_at,omitempty"`
}

type AdaptationCharacterBrief struct {
	Version           int    `json:"version"`
	Brief             string `json:"brief"`
	SourceSignature   string `json:"source_signature"`
	IntentHash        string `json:"intent_hash"`
	CoreCastSignature string `json:"core_cast_signature"`
	UpdatedAt         string `json:"updated_at,omitempty"`
}

// AdaptationCoCreateBriefing is the intent-driven, second-level compression
// used before adaptation co-create draft generation for very long novels.
type AdaptationCoCreateBriefing struct {
	Version               int                               `json:"version"`
	PromptVersion         string                            `json:"prompt_version"`
	SourceSignature       string                            `json:"source_signature"`
	SourceChapterCount    int                               `json:"source_chapter_count"`
	DossierPromptVersion  string                            `json:"dossier_prompt_version"`
	DossierBatchCount     int                               `json:"dossier_batch_count"`
	IntentHash            string                            `json:"intent_hash"`
	GeneratedAt           string                            `json:"generated_at,omitempty"`
	TriggerReason         string                            `json:"trigger_reason,omitempty"`
	Overview              string                            `json:"overview,omitempty"`
	ConfirmedFacts        []string                          `json:"confirmed_facts,omitempty"`
	IntentRelevantRisks   []AdaptationBriefingRisk          `json:"intent_relevant_risks,omitempty"`
	AdaptationSuggestions []string                          `json:"adaptation_suggestions,omitempty"`
	Decisions             []AdaptationBriefingDecision      `json:"decision_questions,omitempty"`
	ResolvedDecisions     []AdaptationResolvedDecision      `json:"resolved_decisions,omitempty"`
	Batches               []AdaptationCoCreateBriefingBatch `json:"batches"`
}

type AdaptationCoCreateBriefingBatch struct {
	Index                 int                          `json:"index"`
	DossierBatchFrom      int                          `json:"dossier_batch_from"`
	DossierBatchTo        int                          `json:"dossier_batch_to"`
	SourceFrom            int                          `json:"source_from"`
	SourceTo              int                          `json:"source_to"`
	DossierSignature      string                       `json:"dossier_signature"`
	PromptVersion         string                       `json:"prompt_version"`
	IntentHash            string                       `json:"intent_hash"`
	GeneratedAt           string                       `json:"generated_at,omitempty"`
	ConfirmedFacts        []string                     `json:"confirmed_facts,omitempty"`
	IntentRelevantRisks   []AdaptationBriefingRisk     `json:"intent_relevant_risks,omitempty"`
	AdaptationSuggestions []string                     `json:"adaptation_suggestions,omitempty"`
	DecisionQuestions     []AdaptationBriefingDecision `json:"decision_questions,omitempty"`
}

type AdaptationBriefingRisk struct {
	ID         string   `json:"id,omitempty"`
	Chapters   []int    `json:"chapters,omitempty"`
	Characters []string `json:"characters,omitempty"`
	Risk       string   `json:"risk"`
	Evidence   string   `json:"evidence,omitempty"`
	Severity   string   `json:"severity,omitempty"`
	Suggestion string   `json:"suggestion,omitempty"`
}

type AdaptationBriefingDecision struct {
	ID                  string                     `json:"id"`
	Question            string                     `json:"question"`
	Reason              string                     `json:"reason,omitempty"`
	Chapters            []int                      `json:"chapters,omitempty"`
	Evidence            string                     `json:"evidence,omitempty"`
	Impact              string                     `json:"impact,omitempty"`
	Required            bool                       `json:"required"`
	Options             []AdaptationDecisionOption `json:"options"`
	RecommendedOptionID string                     `json:"recommended_option_id,omitempty"`
	Status              string                     `json:"status,omitempty"`
}

type AdaptationDecisionOption struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

type AdaptationResolvedDecision struct {
	DecisionID   string `json:"decision_id"`
	OptionID     string `json:"option_id,omitempty"`
	CustomAnswer string `json:"custom_answer,omitempty"`
	ResolvedAt   string `json:"resolved_at,omitempty"`
}

// AdaptationPlan is the durable contract for rewriting the source as a new book.
type AdaptationPlan struct {
	FoundationBinding        *AdaptationFoundationBinding   `json:"foundation_binding,omitempty"`
	Granularity              string                         `json:"granularity"`
	ModePolicy               AdaptationModePolicy           `json:"mode_policy,omitempty"`
	Status                   string                         `json:"status"`
	RewritePolicy            string                         `json:"rewrite_policy"`
	OutlineQualityAudit      *AdaptationOutlineQualityAudit `json:"outline_quality_audit,omitempty"`
	BudgetRepair             *AdaptationBudgetRepairRecord  `json:"budget_repair,omitempty"`
	Brief                    string                         `json:"brief"`
	Planner                  *AdaptationPlannerMeta         `json:"planner,omitempty"`
	Volumes                  []AdaptationVolumePlan         `json:"volumes,omitempty"`
	WordTolerance            float64                        `json:"word_tolerance,omitempty"`
	SourceTotalRunes         int                            `json:"source_total_runes,omitempty"`
	TargetTotalRunes         int                            `json:"target_total_runes,omitempty"`
	TargetMinRunes           int                            `json:"target_min_runes,omitempty"`
	TargetMaxRunes           int                            `json:"target_max_runes,omitempty"`
	MainlineRules            []string                       `json:"mainline_rules,omitempty"`
	RelationshipGoals        []string                       `json:"relationship_goals,omitempty"`
	Rules                    []AdaptationRule               `json:"rules,omitempty"`
	SourceEvents             []AdaptationEvent              `json:"source_events,omitempty"`
	TargetEventLedger        []AdaptationEvent              `json:"target_event_ledger,omitempty"`
	TargetRelationshipStates map[string]string              `json:"target_relationship_states,omitempty"`
	TargetSettingLocks       []AdaptationSettingLock        `json:"target_setting_locks,omitempty"`
	Chapters                 []AdaptationChapterPlan        `json:"chapters"`
}

// AdaptationBudgetRepairRecord records the one-time migration of a legacy
// confirmed plan whose chapter budget could not express its own scene list.
// Keeping this on the plan makes the repair mode and retry count auditable and
// prevents a later Resume from treating an already repaired plan as unseen.
type AdaptationBudgetRepairRecord struct {
	Version       int    `json:"version"`
	Mode          string `json:"mode"` // model / deterministic_fallback
	Attempts      int    `json:"attempts,omitempty"`
	Chapters      []int  `json:"chapters,omitempty"`
	Reason        string `json:"reason,omitempty"`
	CompletedAt   string `json:"completed_at"`
	PlanSignature string `json:"plan_signature,omitempty"`
}

// AdaptationPlanningStage is the durable state of the human-gated adaptation
// planning workflow.  It is intentionally separate from proposal artifacts so
// their mere presence never implies that a user approved the next stage.
type AdaptationPlanningStage string

const (
	AdaptationPlanningStageTargetFoundationGenerating AdaptationPlanningStage = "target_foundation_generating"
	AdaptationPlanningStageFoundationReviewPending    AdaptationPlanningStage = "foundation_review_pending"
	AdaptationPlanningStageSkeletonGenerating         AdaptationPlanningStage = "skeleton_generating"
	AdaptationPlanningStageVolumeReviewPending        AdaptationPlanningStage = "volume_review_pending"
	AdaptationPlanningStageDetailsGenerating          AdaptationPlanningStage = "details_generating"
	AdaptationPlanningStageProposalReviewPending      AdaptationPlanningStage = "proposal_review_pending"
	AdaptationPlanningStageConfirmed                  AdaptationPlanningStage = "confirmed"
)

const AdaptationPlanningWorkflowVersion = 2

type AdaptationPlanningWorkflow struct {
	Version   int                     `json:"version"`
	Stage     AdaptationPlanningStage `json:"stage"`
	Revision  int                     `json:"revision"`
	UpdatedAt string                  `json:"updated_at,omitempty"`
}

func (s AdaptationPlanningStage) Valid() bool {
	switch s {
	case AdaptationPlanningStageTargetFoundationGenerating,
		AdaptationPlanningStageFoundationReviewPending,
		AdaptationPlanningStageSkeletonGenerating,
		AdaptationPlanningStageVolumeReviewPending,
		AdaptationPlanningStageDetailsGenerating,
		AdaptationPlanningStageProposalReviewPending,
		AdaptationPlanningStageConfirmed:
		return true
	default:
		return false
	}
}

func (w AdaptationPlanningWorkflow) Validate() error {
	if w.Version != AdaptationPlanningWorkflowVersion {
		return fmt.Errorf("unsupported adaptation planning workflow version %d", w.Version)
	}
	if !w.Stage.Valid() {
		return fmt.Errorf("invalid adaptation planning stage %q", w.Stage)
	}
	if w.Revision <= 0 {
		return fmt.Errorf("adaptation planning workflow revision must be > 0")
	}
	return nil
}

// AdaptationProposalRuntime keeps resumable planner state while a proposal is
// being generated. It is cleared when a proposal or confirmed plan is saved.
type AdaptationProposalRuntime struct {
	Version            int                                      `json:"version"`
	UpdatedAt          string                                   `json:"updated_at,omitempty"`
	Brief              string                                   `json:"brief"`
	SourcePath         string                                   `json:"source_path,omitempty"`
	SourceChapterCount int                                      `json:"source_chapter_count,omitempty"`
	Granularity        string                                   `json:"granularity"`
	RewritePolicy      string                                   `json:"rewrite_policy"`
	WordTolerance      float64                                  `json:"word_tolerance,omitempty"`
	TargetChapterCount int                                      `json:"target_chapter_count"`
	CoCreateDependency *AdaptationProposalCoCreateDependency    `json:"co_create_dependency,omitempty"`
	FoundationBinding  *AdaptationFoundationBinding             `json:"foundation_binding,omitempty"`
	Skeleton           *AdaptationProposalRuntimeOutline        `json:"skeleton,omitempty"`
	SkeletonBatches    []AdaptationProposalRuntimeSkeletonBatch `json:"skeleton_batches,omitempty"`
	CompletedBatches   []AdaptationProposalRuntimeBatch         `json:"completed_batches,omitempty"`
	AuditCheckpoints   []AdaptationDetailAuditCheckpoint        `json:"audit_checkpoints,omitempty"`
}

// AdaptationProposalCoCreateDependency pins the upstream co-create artifact
// used when proposal generation starts. A resumable proposal must never
// silently regenerate or switch this dependency after planner work exists.
type AdaptationProposalCoCreateDependency struct {
	IntentHash            string `json:"intent_hash"`
	SourceSignature       string `json:"source_signature"`
	BriefingPromptVersion string `json:"briefing_prompt_version"`
	DossierPromptVersion  string `json:"dossier_prompt_version"`
}

const (
	AdaptationDetailAuditVersion       = 1
	AdaptationDetailAuditGenerated     = "generated"
	AdaptationDetailAuditPending       = "audit_pending"
	AdaptationDetailAuditRepairPending = "repair_pending"
	AdaptationDetailAuditPassed        = "passed"
)

// AdaptationDetailAuditEvidence is a server-verified quote used by the
// independent outline auditor. Offsets are absolute rune offsets inside the
// identified immutable input artifact.
type AdaptationDetailAuditEvidence struct {
	ArtifactID     string `json:"artifact_id"`
	ArtifactSHA256 string `json:"artifact_sha256"`
	Quote          string `json:"quote"`
	FromRune       int    `json:"from_rune"`
	ToRune         int    `json:"to_rune"`
}

type AdaptationDetailAuditFinding struct {
	Code              string                          `json:"code"`
	Severity          string                          `json:"severity"`
	Blocking          bool                            `json:"blocking"`
	Message           string                          `json:"message"`
	RepairInstruction string                          `json:"repair_instruction,omitempty"`
	TargetChapters    []int                           `json:"target_chapters,omitempty"`
	Evidence          []AdaptationDetailAuditEvidence `json:"evidence,omitempty"`
}

// AdaptationDetailBatchAudit is resumable proof that one detail batch passed
// both the deterministic contract gate and the independent semantic audit.
// InputSignature includes the preceding accepted context, so repairing an
// upstream batch automatically makes dependent audit results stale.
type AdaptationDetailBatchAudit struct {
	Version                     int                            `json:"version"`
	Status                      string                         `json:"status"`
	ContentSignature            string                         `json:"content_signature,omitempty"`
	InputSignature              string                         `json:"input_signature,omitempty"`
	ContextSignature            string                         `json:"context_signature,omitempty"`
	DeterministicPassed         bool                           `json:"deterministic_passed"`
	SemanticPassed              bool                           `json:"semantic_passed"`
	RepairAttempts              int                            `json:"repair_attempts,omitempty"`
	Provider                    string                         `json:"provider,omitempty"`
	Model                       string                         `json:"model,omitempty"`
	CheckedAt                   string                         `json:"checked_at,omitempty"`
	LastError                   string                         `json:"last_error,omitempty"`
	LastErrorCategory           string                         `json:"last_error_category,omitempty"`
	ExactErrorFingerprint       string                         `json:"exact_error_fingerprint,omitempty"`
	CategoryFingerprint         string                         `json:"category_fingerprint,omitempty"`
	ConsecutiveCategoryFailures int                            `json:"consecutive_category_failures,omitempty"`
	Findings                    []AdaptationDetailAuditFinding `json:"findings,omitempty"`
}

// AdaptationDetailAuditCheckpoint records parent, volume, and global audit
// gates. A final proposal is eligible for review only when every required
// checkpoint has a current passed signature.
type AdaptationDetailAuditCheckpoint struct {
	Version        int                            `json:"version"`
	Kind           string                         `json:"kind"`
	ID             string                         `json:"id"`
	Status         string                         `json:"status"`
	TargetFrom     int                            `json:"target_from,omitempty"`
	TargetTo       int                            `json:"target_to,omitempty"`
	InputSignature string                         `json:"input_signature,omitempty"`
	Provider       string                         `json:"provider,omitempty"`
	Model          string                         `json:"model,omitempty"`
	CheckedAt      string                         `json:"checked_at,omitempty"`
	Summary        string                         `json:"summary,omitempty"`
	Findings       []AdaptationDetailAuditFinding `json:"findings,omitempty"`
}

// AdaptationProposalRuntimeOutline stores the model-planned long-form skeleton
// that detail batches are generated from.
type AdaptationProposalRuntimeOutline struct {
	TargetChapterCount int                                      `json:"target_chapter_count"`
	MainlineRules      []string                                 `json:"mainline_rules,omitempty"`
	RelationshipGoals  []string                                 `json:"relationship_goals,omitempty"`
	Batches            []AdaptationProposalRuntimeSkeletonBatch `json:"batches"`
	Planner            *AdaptationPlannerMeta                   `json:"planner,omitempty"`
}

type AdaptationProposalRuntimeSkeletonBatch struct {
	Index              int      `json:"index"`
	Title              string   `json:"title,omitempty"`
	Theme              string   `json:"theme,omitempty"`
	Goal               string   `json:"goal,omitempty"`
	Summary            string   `json:"summary,omitempty"`
	BudgetDecision     string   `json:"budget_decision,omitempty"`
	BudgetReason       string   `json:"budget_reason,omitempty"`
	TargetFrom         int      `json:"target_from"`
	TargetTo           int      `json:"target_to"`
	TargetChapterCount int      `json:"chapter_count,omitempty"`
	SourceFrom         int      `json:"source_from"`
	SourceTo           int      `json:"source_to"`
	SourceChapters     []int    `json:"source_chapters,omitempty"`
	MainlineEventIDs   []string `json:"mainline_event_ids,omitempty"`
	AllowedEventIDs    []string `json:"allowed_event_ids,omitempty"`
	// DetailEventContractVersion records the detail-batch event ownership
	// policy used when this parent skeleton was created. Zero is the legacy
	// shared-whitelist shape; later versions partition stable events across
	// detail sub-batches before any chapter outline is generated.
	DetailEventContractVersion int      `json:"detail_event_contract_version,omitempty"`
	Notes                      []string `json:"notes,omitempty"`
}

type AdaptationProposalRuntimeBatch struct {
	Index       int                         `json:"index"`
	TargetFrom  int                         `json:"target_from"`
	TargetTo    int                         `json:"target_to"`
	SourceFrom  int                         `json:"source_from"`
	SourceTo    int                         `json:"source_to"`
	CompletedAt string                      `json:"completed_at,omitempty"`
	Chapters    []AdaptationChapterPlan     `json:"chapters"`
	Audit       *AdaptationDetailBatchAudit `json:"audit,omitempty"`
}

// AdaptationVolumeReview is the user-visible high-level planning checkpoint
// before detailed chapter outlines are generated.
type AdaptationVolumeReview struct {
	FoundationBinding  *AdaptationFoundationBinding `json:"foundation_binding,omitempty"`
	Status             string                       `json:"status"`
	UpdatedAt          string                       `json:"updated_at,omitempty"`
	Brief              string                       `json:"brief"`
	SourcePath         string                       `json:"source_path,omitempty"`
	SourceChapterCount int                          `json:"source_chapter_count,omitempty"`
	Granularity        string                       `json:"granularity"`
	RewritePolicy      string                       `json:"rewrite_policy"`
	WordTolerance      float64                      `json:"word_tolerance,omitempty"`
	TargetChapterCount int                          `json:"target_chapter_count"`
	MainlineRules      []string                     `json:"mainline_rules,omitempty"`
	RelationshipGoals  []string                     `json:"relationship_goals,omitempty"`
	Volumes            []AdaptationVolumePlan       `json:"volumes"`
	Planner            *AdaptationPlannerMeta       `json:"planner,omitempty"`
}

// AdaptationVolumePlan is the planner's durable high-level grouping for a
// proposal. It is model-chosen for long-form plans and remains optional for
// shorter works that do not naturally need volumes.
type AdaptationVolumePlan struct {
	ID               string   `json:"id,omitempty"`
	Index            int      `json:"index"`
	Title            string   `json:"title"`
	Theme            string   `json:"theme,omitempty"`
	Goal             string   `json:"goal,omitempty"`
	Summary          string   `json:"summary,omitempty"`
	BudgetDecision   string   `json:"budget_decision,omitempty"`
	BudgetReason     string   `json:"budget_reason,omitempty"`
	TargetFrom       int      `json:"target_from"`
	TargetTo         int      `json:"target_to"`
	SourceFrom       int      `json:"source_from,omitempty"`
	SourceTo         int      `json:"source_to,omitempty"`
	MainlineEventIDs []string `json:"mainline_event_ids,omitempty"`
	Notes            TextList `json:"notes,omitempty"`
}

// AdaptationPlannerMeta records how an adaptation plan or proposal was made.
type AdaptationPlannerMeta struct {
	Prompt        string   `json:"prompt,omitempty"`
	PromptVersion string   `json:"prompt_version,omitempty"`
	Model         string   `json:"model,omitempty"`
	GeneratedAt   string   `json:"generated_at,omitempty"`
	Notes         TextList `json:"notes,omitempty"`
}

// TextList accepts either a JSON string or an array of strings. Planner models
// often compress singleton note arrays into one string; the stored shape stays
// an array after normal marshaling.
type TextList []string

func (l *TextList) UnmarshalJSON(data []byte) error {
	var values []string
	if err := json.Unmarshal(data, &values); err == nil {
		*l = values
		return nil
	}
	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	value = strings.TrimSpace(value)
	if value == "" {
		*l = nil
		return nil
	}
	*l = []string{value}
	return nil
}

// SourceRange records the inclusive source chapter coverage for one target
// chapter. It remains explicit even when SourceChapters has sparse anchors.
type SourceRange struct {
	From int `json:"from"`
	To   int `json:"to"`
}

// AdaptationChapterWordBudget is the additive nested chapter word-budget
// contract. The legacy top-level TargetRunes/TargetMinRunes/TargetMaxRunes
// fields remain authoritative for old readers and are mirrored by the store.
type AdaptationChapterWordBudget struct {
	SourceRunes int     `json:"source_runes,omitempty"`
	TargetRunes int     `json:"target_runes,omitempty"`
	MinRunes    int     `json:"min_runes,omitempty"`
	MaxRunes    int     `json:"max_runes,omitempty"`
	Tolerance   float64 `json:"tolerance,omitempty"`
}

func (b *AdaptationChapterWordBudget) UnmarshalJSON(data []byte) error {
	type budgetAlias AdaptationChapterWordBudget
	var raw struct {
		budgetAlias
		SourceWords int `json:"source_words,omitempty"`
		TargetWords int `json:"target_words,omitempty"`
		MinWords    int `json:"min_words,omitempty"`
		MaxWords    int `json:"max_words,omitempty"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	*b = AdaptationChapterWordBudget(raw.budgetAlias)
	if b.SourceRunes <= 0 {
		b.SourceRunes = raw.SourceWords
	}
	if b.TargetRunes <= 0 {
		b.TargetRunes = raw.TargetWords
	}
	if b.MinRunes <= 0 {
		b.MinRunes = raw.MinWords
	}
	if b.MaxRunes <= 0 {
		b.MaxRunes = raw.MaxWords
	}
	return nil
}

// AdaptationChapterPlan defines one target chapter's source anchors and edits.
type AdaptationChapterPlan struct {
	OutlineEntry
	Chapter           int                               `json:"chapter"`
	Title             string                            `json:"title"`
	SourceChapters    []int                             `json:"source_chapters"`
	SourceRunes       int                               `json:"source_runes,omitempty"`
	TargetRunes       int                               `json:"target_runes,omitempty"`
	TargetMinRunes    int                               `json:"target_min_runes,omitempty"`
	TargetMaxRunes    int                               `json:"target_max_runes,omitempty"`
	WordBudget        *AdaptationChapterWordBudget      `json:"word_budget,omitempty"`
	SourceRange       SourceRange                       `json:"source_range,omitempty"`
	SourceSegments    []AdaptationSourceSegment         `json:"source_segments,omitempty"`
	EventIDs          []string                          `json:"event_ids,omitempty"`
	AddedEventIDs     []string                          `json:"added_event_ids,omitempty"`
	DependsOnEventIDs []string                          `json:"depends_on_event_ids,omitempty"`
	Relationship      *AdaptationRelationshipTransition `json:"relationship,omitempty"`
	SettingClaims     []AdaptationSettingClaim          `json:"setting_claims,omitempty"`
	RuleIDs           []string                          `json:"rule_ids,omitempty"`
	IsAdded           bool                              `json:"is_added,omitempty"`
	CoverageNote      string                            `json:"coverage_note,omitempty"`
	PreserveEvents    []string                          `json:"preserve_events,omitempty"`
	RequiredChanges   []string                          `json:"required_changes,omitempty"`
	ForbiddenMoves    []string                          `json:"forbidden_moves,omitempty"`
}

// AdaptationCheck is saved after a draft has been checked against the plan.
type AdaptationCheck struct {
	Chapter                        int                                     `json:"chapter"`
	DraftSHA256                    string                                  `json:"draft_sha256"`
	Passed                         bool                                    `json:"passed"`
	Summary                        string                                  `json:"summary,omitempty"`
	Issues                         []string                                `json:"issues,omitempty"`
	ChangeEvidence                 []AdaptationChangeEvidence              `json:"change_evidence,omitempty"`
	BodyEvidence                   []AdaptationBodyEvidence                `json:"body_evidence,omitempty"`
	SemanticVerificationTaskSHA256 string                                  `json:"semantic_verification_task_sha256,omitempty"`
	SemanticVerificationSHA256     string                                  `json:"semantic_verification_sha256,omitempty"`
	SemanticVerificationReceipts   []AdaptationSemanticVerificationReceipt `json:"semantic_verification_receipts,omitempty"`
	AbsenceAuditSHA256             string                                  `json:"absence_audit_sha256,omitempty"`
	AbsenceAuditTaskSHA256         string                                  `json:"absence_audit_task_sha256,omitempty"`
	AbsenceAuditProseRunes         int                                     `json:"absence_audit_prose_runes,omitempty"`
	AbsenceAuditForbiddenIDs       []string                                `json:"absence_audit_forbidden_ids,omitempty"`
	CheckedAt                      string                                  `json:"checked_at"`
}

type AdaptationSemanticVerificationReceipt struct {
	Kind              string `json:"kind"`
	ID                string `json:"id"`
	SourceDescription string `json:"source_description,omitempty"`
	Quote             string `json:"quote"`
	StartRune         int    `json:"start_rune"`
	EndRune           int    `json:"end_rune"`
	Verdict           string `json:"verdict"`
	TaskSignature     string `json:"task_signature"`
	CandidateSHA256   string `json:"candidate_sha256"`
}

// AdaptationBodyEvidence is independently verified against the current draft;
// unlike a writer summary, Quote must occur verbatim in prose.
type AdaptationBodyEvidence struct {
	EventID         string `json:"event_id"`
	Quote           string `json:"quote"`
	StartRune       int    `json:"start_rune"`
	EndRune         int    `json:"end_rune"`
	EvidenceSHA256  string `json:"evidence_sha256"`
	CandidateSHA256 string `json:"candidate_sha256"`
}

// AdaptationChangeEvidence records how a required adaptation change was
// integrated into prose instead of merely described as a patch note.
type AdaptationChangeEvidence struct {
	SourceChapter int    `json:"source_chapter,omitempty"`
	SourceAnchor  string `json:"source_anchor,omitempty"`
	Change        string `json:"change"`
	Integration   string `json:"integration,omitempty"`
}

// NormalizeAdaptationGranularity keeps the plan granularity constrained to the
// supported modes. Empty or unknown input falls back to chapter mode.
func NormalizeAdaptationGranularity(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case AdaptationGranularityArc:
		return AdaptationGranularityArc
	case AdaptationGranularityFree:
		return AdaptationGranularityFree
	default:
		return AdaptationGranularityChapter
	}
}

func StrictAdaptationGranularity(value string) (string, bool) {
	switch strings.TrimSpace(value) {
	case AdaptationGranularityChapter:
		return AdaptationGranularityChapter, true
	case AdaptationGranularityArc:
		return AdaptationGranularityArc, true
	case AdaptationGranularityFree:
		return AdaptationGranularityFree, true
	default:
		return "", false
	}
}

// NormalizeAdaptationRewritePolicy constrains rewrite policy. Empty and
// unknown values fall back to full rewrite for compatibility with old
// brief-only adaptation starts.
func NormalizeAdaptationRewritePolicy(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case AdaptationRewritePreserveDetails:
		return AdaptationRewritePreserveDetails
	default:
		return AdaptationRewriteFullRewrite
	}
}

// AdaptationRewritePolicyForGranularity is the canonical policy mapping for
// adaptation projects. preserve_details keeps source detail ownership explicit;
// a long source chapter may map to multiple ordered target chapters.
func AdaptationRewritePolicyForGranularity(granularity string) string {
	switch NormalizeAdaptationGranularity(granularity) {
	case AdaptationGranularityChapter:
		return AdaptationRewritePreserveDetails
	default:
		return AdaptationRewriteFullRewrite
	}
}

func NormalizeAdaptationPlanStatus(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case AdaptationPlanStatusProposal:
		return AdaptationPlanStatusProposal
	default:
		return AdaptationPlanStatusConfirmed
	}
}
