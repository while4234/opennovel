package adaptaudit

// Mode selects the quality contract used by an adaptation audit.
type Mode string

const (
	ModeChapter Mode = "chapter"
	ModeArc     Mode = "arc"
	ModeFree    Mode = "free"
)

type ArtifactKind string

const (
	ArtifactSourceSegment ArtifactKind = "source_segment"
	ArtifactHighPlan      ArtifactKind = "high_plan"
	ArtifactTargetPlan    ArtifactKind = "target_plan"
	ArtifactTargetChapter ArtifactKind = "target_chapter"
)

type Artifact struct {
	ID        string       `json:"id"`
	Kind      ArtifactKind `json:"kind"`
	Chapter   int          `json:"chapter,omitempty"`
	SegmentID string       `json:"segment_id,omitempty"`
	Text      string       `json:"text"`
}

// Evidence is valid only when Quote is present verbatim in the referenced
// artifact. Model-written pass flags and summaries are deliberately excluded.
type Evidence struct {
	ArtifactID string `json:"artifact_id"`
	Quote      string `json:"quote"`
}

type EventOrigin string

const (
	OriginSource EventOrigin = "source"
	OriginAdded  EventOrigin = "added"
	OriginTarget EventOrigin = "target"
)

type EventClass string

const (
	ClassOrdinary     EventClass = "ordinary"
	ClassMainline     EventClass = "mainline"
	ClassRelationship EventClass = "relationship"
	ClassSetting      EventClass = "setting"
)

type Event struct {
	ID               string              `json:"id"`
	Origin           EventOrigin         `json:"origin"`
	Class            EventClass          `json:"class"`
	Required         bool                `json:"required,omitempty"`
	Importance       string              `json:"importance,omitempty"`
	SourceSegmentIDs []string            `json:"source_segment_ids,omitempty"`
	DependsOn        []string            `json:"depends_on,omitempty"`
	HighPlanEvidence []Evidence          `json:"high_plan_evidence,omitempty"`
	Relationship     *RelationshipChange `json:"relationship,omitempty"`
	SettingClaims    []SettingClaim      `json:"setting_claims,omitempty"`
}

type Binding struct {
	EventID          string     `json:"event_id,omitempty"`
	SourceSegmentIDs []string   `json:"source_segment_ids,omitempty"`
	TargetChapters   []int      `json:"target_chapters,omitempty"`
	PlanEvidence     []Evidence `json:"plan_evidence,omitempty"`
	BodyEvidence     []Evidence `json:"body_evidence,omitempty"`
	ServesEventIDs   []string   `json:"serves_event_ids,omitempty"`
}

// Rune offsets use half-open ranges [FromRune, ToRune).
type SourceSegment struct {
	ID              string            `json:"id"`
	Chapter         int               `json:"chapter"`
	Sequence        int               `json:"sequence,omitempty"`
	TargetChapter   int               `json:"target_chapter,omitempty"`
	FromRune        int               `json:"from_rune"`
	ToRune          int               `json:"to_rune"`
	LongPart        bool              `json:"long_part,omitempty"`
	Required        bool              `json:"required,omitempty"`
	ContractPresent bool              `json:"contract_present"`
	TotalRunes      int               `json:"total_runes,omitempty"`
	MaxRunes        int               `json:"max_runes,omitempty"`
	EntryState      map[string]string `json:"entry_state,omitempty"`
	ExitState       map[string]string `json:"exit_state,omitempty"`
}

type Scope struct {
	SourceFrom int `json:"source_from,omitempty"`
	SourceTo   int `json:"source_to,omitempty"`
	TargetFrom int `json:"target_from,omitempty"`
	TargetTo   int `json:"target_to,omitempty"`
}

type RelationshipChange struct {
	Pair             string   `json:"pair"`
	From             string   `json:"from"`
	To               string   `json:"to"`
	AllowedFrom      []string `json:"allowed_from,omitempty"`
	RequiresEventIDs []string `json:"requires_event_ids,omitempty"`
}

type SettingLock struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

type SettingClaim struct {
	Key      string     `json:"key"`
	Value    string     `json:"value"`
	Evidence []Evidence `json:"evidence,omitempty"`
}

type Input struct {
	Mode               Mode              `json:"mode"`
	Scope              Scope             `json:"scope,omitempty"`
	Artifacts          []Artifact        `json:"artifacts,omitempty"`
	Events             []Event           `json:"events,omitempty"`
	Bindings           []Binding         `json:"bindings,omitempty"`
	SourceSegments     []SourceSegment   `json:"source_segments,omitempty"`
	RelationshipStates map[string]string `json:"relationship_states,omitempty"`
	SettingLocks       []SettingLock     `json:"setting_locks,omitempty"`
}

type Finding struct {
	ID             string     `json:"id"`
	Fingerprint    string     `json:"fingerprint,omitempty"`
	Code           string     `json:"code"`
	Severity       string     `json:"severity"`
	Blocking       bool       `json:"blocking"`
	Message        string     `json:"message"`
	EventID        string     `json:"event_id,omitempty"`
	SegmentID      string     `json:"segment_id,omitempty"`
	TargetChapters []int      `json:"target_chapters,omitempty"`
	Evidence       []Evidence `json:"evidence,omitempty"`
	Source         string     `json:"source,omitempty"`
}

type Metrics struct {
	Events             int `json:"events"`
	RequiredEvents     int `json:"required_events"`
	BoundEvents        int `json:"bound_events"`
	SourceSegments     int `json:"source_segments"`
	CoveredSegments    int `json:"covered_segments"`
	ValidEvidenceItems int `json:"valid_evidence_items"`
}

type Confirmation struct {
	Required           bool     `json:"required"`
	ReportDigest       string   `json:"report_digest"`
	BlockingFindingIDs []string `json:"blocking_finding_ids,omitempty"`
	SuggestedAction    string   `json:"suggested_action,omitempty"`
}

type Report struct {
	Version      int          `json:"version"`
	Mode         Mode         `json:"mode"`
	InputDigest  string       `json:"input_digest"`
	Digest       string       `json:"digest"`
	Status       string       `json:"status"`
	ReadOnly     bool         `json:"read_only"`
	Scope        Scope        `json:"scope,omitempty"`
	Findings     []Finding    `json:"findings,omitempty"`
	Metrics      Metrics      `json:"metrics"`
	Confirmation Confirmation `json:"confirmation"`
}

type ConfirmationRequest struct {
	RunID                  string   `json:"run_id,omitempty"`
	ReportDigest           string   `json:"report_digest"`
	Decision               string   `json:"decision"`
	AcknowledgedFindingIDs []string `json:"acknowledged_finding_ids,omitempty"`
}

type RepairApplication struct {
	RunID            string `json:"run_id,omitempty"`
	ReportDigest     string `json:"report_digest"`
	BackupPath       string `json:"backup_path"`
	AffectedChapters []int  `json:"affected_chapters,omitempty"`
	QueuedChapters   []int  `json:"queued_chapters,omitempty"`
	Status           string `json:"status"`
	AppliedAt        string `json:"applied_at"`
}

type AuditKind string

const (
	AuditKindContract        AuditKind = "contract"
	AuditKindModelSecondPass AuditKind = "model_second_pass"
	AuditKindHybrid          AuditKind = "hybrid"
)

type AuditTrigger string

const (
	AuditTriggerManual     AuditTrigger = "manual"
	AuditTriggerCompletion AuditTrigger = "completion"
	AuditTriggerExport     AuditTrigger = "export"
	AuditTriggerLegacy     AuditTrigger = "legacy"
)

type ModelSnapshot struct {
	Provider        string `json:"provider,omitempty"`
	Model           string `json:"model,omitempty"`
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
}

type AuditUsage struct {
	InputTokens  int      `json:"input_tokens,omitempty"`
	OutputTokens int      `json:"output_tokens,omitempty"`
	TotalTokens  int      `json:"total_tokens,omitempty"`
	CallCount    int      `json:"call_count,omitempty"`
	CostUSD      *float64 `json:"cost_usd,omitempty"`
	PriceKnown   bool     `json:"price_known,omitempty"`
}

// AuditRun is an immutable snapshot. Mutable lifecycle facts such as retention
// protection are kept in AuditRunIndexEntry rather than rewriting the run.
type AuditRun struct {
	RunID         string         `json:"run_id"`
	Kind          AuditKind      `json:"kind"`
	Trigger       AuditTrigger   `json:"trigger"`
	StartedAt     string         `json:"started_at"`
	CompletedAt   string         `json:"completed_at,omitempty"`
	Status        string         `json:"status"`
	Scope         Scope          `json:"scope,omitempty"`
	InputDigest   string         `json:"input_digest"`
	ReportDigest  string         `json:"report_digest"`
	EngineVersion string         `json:"engine_version,omitempty"`
	Model         *ModelSnapshot `json:"model,omitempty"`
	Usage         AuditUsage     `json:"usage,omitempty"`
	Report        Report         `json:"report"`
}

type AuditRunIndexEntry struct {
	RunID            string         `json:"run_id"`
	Kind             AuditKind      `json:"kind"`
	Trigger          AuditTrigger   `json:"trigger"`
	CompletedAt      string         `json:"completed_at,omitempty"`
	Status           string         `json:"status"`
	Scope            Scope          `json:"scope,omitempty"`
	InputDigest      string         `json:"input_digest"`
	ReportDigest     string         `json:"report_digest"`
	Model            *ModelSnapshot `json:"model,omitempty"`
	Usage            AuditUsage     `json:"usage,omitempty"`
	ProtectedReasons []string       `json:"protected_reasons,omitempty"`
	AppliedAt        string         `json:"applied_at,omitempty"`
}

type AuditRunIndex struct {
	Version int                  `json:"version"`
	Runs    []AuditRunIndexEntry `json:"runs"`
}

type FindingChange struct {
	Fingerprint string   `json:"fingerprint"`
	Change      string   `json:"change"`
	Before      *Finding `json:"before,omitempty"`
	After       *Finding `json:"after,omitempty"`
}

type AuditComparison struct {
	BaseRunID           string          `json:"base_run_id"`
	CandidateRunID      string          `json:"candidate_run_id"`
	Confidence          string          `json:"confidence"`
	AttributableToModel bool            `json:"attributable_to_model"`
	ComparedScope       Scope           `json:"compared_scope"`
	Warnings            []string        `json:"warnings,omitempty"`
	Changes             []FindingChange `json:"changes"`
}
