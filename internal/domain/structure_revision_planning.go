package domain

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

type StructureRevisionOperation string

const (
	StructureRevisionExpandChapter StructureRevisionOperation = "expand_chapter"
	StructureRevisionInsertChapter StructureRevisionOperation = "insert_chapter"
	StructureRevisionAppendChapter StructureRevisionOperation = "append_chapter"
	StructureRevisionAppendArc     StructureRevisionOperation = "append_arc"
	StructureRevisionAppendVolume  StructureRevisionOperation = "append_volume"
	StructureRevisionSplitChapter  StructureRevisionOperation = "split_chapter"
	StructureRevisionMoveChapter   StructureRevisionOperation = "move_unwritten_chapter"
)

func (o StructureRevisionOperation) Valid() bool {
	switch o {
	case StructureRevisionExpandChapter,
		StructureRevisionInsertChapter,
		StructureRevisionAppendChapter,
		StructureRevisionAppendArc,
		StructureRevisionAppendVolume,
		StructureRevisionSplitChapter,
		StructureRevisionMoveChapter:
		return true
	default:
		return false
	}
}

type ManuscriptStage string

const (
	ManuscriptStageProposalComplete ManuscriptStage = "proposal_complete"
	ManuscriptStageOutlineComplete  ManuscriptStage = "outline_complete"
	ManuscriptStageWriting          ManuscriptStage = "writing"
	ManuscriptStageComplete         ManuscriptStage = "complete"
)

func (s ManuscriptStage) Valid() bool {
	switch s {
	case ManuscriptStageProposalComplete, ManuscriptStageOutlineComplete, ManuscriptStageWriting, ManuscriptStageComplete:
		return true
	default:
		return false
	}
}

// StructureRevisionRequest is shared by original-fiction and adaptation
// planners. Mode-specific semantics belong to the planner implementation.
type StructureRevisionRequest struct {
	Operation           StructureRevisionOperation `json:"operation"`
	Intent              string                     `json:"intent"`
	Stage               ManuscriptStage            `json:"stage"`
	TargetID            string                     `json:"target_id,omitempty"`
	DestinationID       string                     `json:"destination_id,omitempty"`
	BaseRevision        int                        `json:"base_revision"`
	Current             []VolumeOutline            `json:"current"`
	CompletedChapterIDs []string                   `json:"completed_chapter_ids,omitempty"`
	CurrentSoftBudget   *DynamicSoftBudget         `json:"current_soft_budget,omitempty"`
}

func (r StructureRevisionRequest) Validate() error {
	if !r.Operation.Valid() {
		return fmt.Errorf("unsupported structure revision operation %q", r.Operation)
	}
	if strings.TrimSpace(r.Intent) == "" {
		return fmt.Errorf("structure revision intent is required")
	}
	if !r.Stage.Valid() {
		return fmt.Errorf("unsupported manuscript stage %q", r.Stage)
	}
	if r.BaseRevision <= 0 {
		return fmt.Errorf("base revision must be positive")
	}
	if err := ValidateStructureSnapshotForStage(r.Current, r.Stage); err != nil {
		return fmt.Errorf("validate current structure: %w", err)
	}
	chapters := StructureChapterIDs(r.Current)
	completed := make(map[string]struct{}, len(r.CompletedChapterIDs))
	for _, completedID := range r.CompletedChapterIDs {
		completedID = strings.TrimSpace(completedID)
		if !slices.Contains(chapters, completedID) {
			return fmt.Errorf("completed chapter %q is not present in the current structure", completedID)
		}
		if _, duplicate := completed[completedID]; duplicate {
			return fmt.Errorf("completed chapter %q is duplicated", completedID)
		}
		completed[completedID] = struct{}{}
	}
	if requiresStructureTarget(r.Operation) && !slices.Contains(chapters, strings.TrimSpace(r.TargetID)) {
		return fmt.Errorf("target chapter %q is not present in the current structure", r.TargetID)
	}
	if r.Operation == StructureRevisionMoveChapter {
		if strings.TrimSpace(r.DestinationID) == "" {
			return fmt.Errorf("move destination is required")
		}
		if !slices.Contains(StructureNodeIDs(r.Current), strings.TrimSpace(r.DestinationID)) {
			return fmt.Errorf("move destination %q is not present in the current structure", r.DestinationID)
		}
		if slices.Contains(r.CompletedChapterIDs, strings.TrimSpace(r.TargetID)) {
			return fmt.Errorf("written chapter %q cannot be moved", r.TargetID)
		}
	}
	if r.CurrentSoftBudget != nil {
		if err := r.CurrentSoftBudget.Validate(); err != nil {
			return fmt.Errorf("validate current soft budget: %w", err)
		}
	}
	return nil
}

func requiresStructureTarget(operation StructureRevisionOperation) bool {
	switch operation {
	case StructureRevisionExpandChapter, StructureRevisionInsertChapter, StructureRevisionSplitChapter, StructureRevisionMoveChapter:
		return true
	default:
		return false
	}
}

type DramaticStageEvidence struct {
	EntryState             string `json:"entry_state"`
	IndependentConflict    string `json:"independent_conflict"`
	ArcProgression         string `json:"arc_progression"`
	Climax                 string `json:"climax"`
	IrreversibleOutcome    string `json:"irreversible_outcome"`
	CannotFitCurrentVolume string `json:"cannot_fit_current_volume"`
}

func (e DramaticStageEvidence) SupportsNewVolume() bool {
	return strings.TrimSpace(e.EntryState) != "" &&
		strings.TrimSpace(e.IndependentConflict) != "" &&
		strings.TrimSpace(e.ArcProgression) != "" &&
		strings.TrimSpace(e.Climax) != "" &&
		strings.TrimSpace(e.IrreversibleOutcome) != "" &&
		strings.TrimSpace(e.CannotFitCurrentVolume) != ""
}

type ContentAdditionAssessment struct {
	NeedsAdditionalChapters bool                   `json:"needs_additional_chapters"`
	Reason                  string                 `json:"reason"`
	NewVolume               *DramaticStageEvidence `json:"new_volume,omitempty"`
}

func (a ContentAdditionAssessment) Validate() error {
	if strings.TrimSpace(a.Reason) == "" {
		return fmt.Errorf("content-addition assessment reason is required")
	}
	if a.NewVolume != nil && !a.NewVolume.SupportsNewVolume() {
		return fmt.Errorf("new volume requires an entry state, independent conflict, arc progression, climax, irreversible outcome, and cannot-fit evidence")
	}
	return nil
}

// DynamicSoftBudget is an estimate, not an admission gate. A planner may
// increase it whenever the revised dramatic units justify more chapters.
type DynamicSoftBudget struct {
	EstimatedChapters int `json:"estimated_chapters"`
	ChapterMinWords   int `json:"chapter_min_words"`
	ChapterMaxWords   int `json:"chapter_max_words"`
	TargetTotalWords  int `json:"target_total_words"`
	TotalMinWords     int `json:"total_min_words"`
	TotalMaxWords     int `json:"total_max_words"`
}

func NewDynamicSoftBudget(chapters, chapterMinWords, chapterMaxWords int) (DynamicSoftBudget, error) {
	budget := DynamicSoftBudget{
		EstimatedChapters: chapters,
		ChapterMinWords:   chapterMinWords,
		ChapterMaxWords:   chapterMaxWords,
	}
	if chapters > 0 && chapterMinWords > 0 && chapterMaxWords >= chapterMinWords {
		budget.TotalMinWords = chapters * chapterMinWords
		budget.TotalMaxWords = chapters * chapterMaxWords
		budget.TargetTotalWords = chapters * ((chapterMinWords + chapterMaxWords) / 2)
	}
	if err := budget.Validate(); err != nil {
		return DynamicSoftBudget{}, err
	}
	return budget, nil
}

func (b DynamicSoftBudget) Validate() error {
	if b.EstimatedChapters <= 0 {
		return fmt.Errorf("estimated chapter count must be positive")
	}
	if b.ChapterMinWords <= 0 || b.ChapterMaxWords < b.ChapterMinWords {
		return fmt.Errorf("chapter word range is invalid")
	}
	if b.TotalMinWords <= 0 || b.TargetTotalWords < b.TotalMinWords || b.TotalMaxWords < b.TargetTotalWords {
		return fmt.Errorf("soft total word range is invalid")
	}
	return nil
}

type StructureImpactLevel string

const (
	StructureImpactRequired    StructureImpactLevel = "required"
	StructureImpactRecommended StructureImpactLevel = "recommended"
)

type StructureImpactCause string

const (
	StructureImpactContentDependency StructureImpactCause = "content_dependency"
	StructureImpactStructureChange   StructureImpactCause = "structure_change"
	StructureImpactDisplayRenumber   StructureImpactCause = "display_renumber"
)

type StructureImpactItem struct {
	ArtifactID          string               `json:"artifact_id"`
	ArtifactKind        string               `json:"artifact_kind"`
	Change              string               `json:"change"`
	Level               StructureImpactLevel `json:"level"`
	Cause               StructureImpactCause `json:"cause"`
	RequiresBodyRewrite bool                 `json:"requires_body_rewrite,omitempty"`
	DependencyEvidence  []string             `json:"dependency_evidence"`
	DependencySourceIDs []string             `json:"dependency_source_ids,omitempty"`
}

func (i StructureImpactItem) Validate() error {
	if strings.TrimSpace(i.ArtifactID) == "" || strings.TrimSpace(i.ArtifactKind) == "" || strings.TrimSpace(i.Change) == "" {
		return fmt.Errorf("structure impact requires artifact identity, kind, and change")
	}
	if i.Level != StructureImpactRequired && i.Level != StructureImpactRecommended {
		return fmt.Errorf("structure impact %q has invalid level %q", i.ArtifactID, i.Level)
	}
	switch i.Cause {
	case StructureImpactContentDependency, StructureImpactStructureChange, StructureImpactDisplayRenumber:
	default:
		return fmt.Errorf("structure impact %q has invalid cause %q", i.ArtifactID, i.Cause)
	}
	if len(i.DependencyEvidence) == 0 {
		return fmt.Errorf("structure impact %q requires dependency evidence", i.ArtifactID)
	}
	for _, evidence := range i.DependencyEvidence {
		if strings.TrimSpace(evidence) == "" {
			return fmt.Errorf("structure impact %q contains empty dependency evidence", i.ArtifactID)
		}
	}
	sources := make(map[string]struct{}, len(i.DependencySourceIDs))
	for _, sourceID := range i.DependencySourceIDs {
		sourceID = strings.TrimSpace(sourceID)
		if sourceID == "" {
			return fmt.Errorf("structure impact %q contains an empty dependency source ID", i.ArtifactID)
		}
		if _, duplicate := sources[sourceID]; duplicate {
			return fmt.Errorf("structure impact %q contains duplicate dependency source ID %q", i.ArtifactID, sourceID)
		}
		sources[sourceID] = struct{}{}
	}
	if i.Cause == StructureImpactDisplayRenumber && i.RequiresBodyRewrite {
		return fmt.Errorf("display renumbering cannot require body rewriting")
	}
	if i.RequiresBodyRewrite && i.ArtifactKind != StructureKindChapter {
		return fmt.Errorf("only chapter impacts can require body rewriting")
	}
	return nil
}

type StructureRevisionProposal struct {
	Assessment ContentAdditionAssessment `json:"assessment"`
	Candidate  []VolumeOutline           `json:"candidate"`
	SoftBudget DynamicSoftBudget         `json:"soft_budget"`
	Impacts    []StructureImpactItem     `json:"impacts"`
}

func (p StructureRevisionProposal) RevisionImpact(summary string) (RevisionImpact, error) {
	items := make([]RevisionImpactItem, 0, len(p.Impacts))
	for _, impact := range p.Impacts {
		items = append(items, RevisionImpactItem{
			ArtifactID: impact.ArtifactID, ArtifactKind: impact.ArtifactKind, Change: impact.Change,
			Requirement: impact.Level, Cause: impact.Cause, RequiresBodyRewrite: impact.RequiresBodyRewrite,
			DependencyEvidence:  append([]string(nil), impact.DependencyEvidence...),
			DependencySourceIDs: append([]string(nil), impact.DependencySourceIDs...),
		})
	}
	return NewRevisionImpact(summary, items)
}

func (p StructureRevisionProposal) Validate() error {
	return p.ValidateForStage(ManuscriptStageOutlineComplete)
}

func (p StructureRevisionProposal) ValidateForStage(stage ManuscriptStage) error {
	if err := p.Assessment.Validate(); err != nil {
		return err
	}
	if err := ValidateStructureSnapshotForStage(p.Candidate, stage); err != nil {
		return fmt.Errorf("validate candidate structure: %w", err)
	}
	if err := p.SoftBudget.Validate(); err != nil {
		return fmt.Errorf("validate candidate soft budget: %w", err)
	}
	seen := make(map[string]struct{}, len(p.Impacts))
	for _, impact := range p.Impacts {
		if err := impact.Validate(); err != nil {
			return err
		}
		if _, exists := seen[impact.ArtifactID]; exists {
			return fmt.Errorf("duplicate structure impact for artifact %q", impact.ArtifactID)
		}
		seen[impact.ArtifactID] = struct{}{}
	}
	return nil
}

// ValidateStructureSnapshotForStage permits a proposal-complete volume/arc
// skeleton with reserved chapter counts. Later stages require stable chapter
// slots because detailed generation and prose queues bind to those identities.
func ValidateStructureSnapshotForStage(volumes []VolumeOutline, stage ManuscriptStage) error {
	if stage != ManuscriptStageProposalComplete {
		return ValidateStructureSnapshot(volumes)
	}
	if len(volumes) == 0 {
		return fmt.Errorf("structure must contain at least one volume")
	}
	seen := make(map[string]string)
	for _, volume := range volumes {
		if err := validateStructureNodeID(seen, volume.ID, StructureKindVolume); err != nil {
			return err
		}
		if len(volume.Arcs) == 0 {
			return fmt.Errorf("proposal skeleton volume %q must contain at least one arc", volume.ID)
		}
		for _, arc := range volume.Arcs {
			if err := validateStructureNodeID(seen, arc.ID, StructureKindArc); err != nil {
				return err
			}
			if len(arc.Chapters) == 0 && arc.EstimatedChapters <= 0 {
				return fmt.Errorf("proposal skeleton arc %q must reserve chapters", arc.ID)
			}
			for _, chapter := range arc.Chapters {
				if err := validateStructureNodeID(seen, chapter.ID, StructureKindChapter); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

type StructureRevisionPreview struct {
	BaseRevision           int                        `json:"base_revision"`
	BaseStructureSignature string                     `json:"base_structure_signature"`
	Operation              StructureRevisionOperation `json:"operation"`
	Stage                  ManuscriptStage            `json:"stage"`
	Intent                 string                     `json:"intent"`
	TargetID               string                     `json:"target_id,omitempty"`
	DestinationID          string                     `json:"destination_id,omitempty"`
	CompletedChapterIDs    []string                   `json:"completed_chapter_ids,omitempty"`
	CurrentSoftBudget      *DynamicSoftBudget         `json:"current_soft_budget,omitempty"`
	Proposal               StructureRevisionProposal  `json:"proposal"`
	Signature              string                     `json:"signature"`
}

var ErrStructurePreviewStale = errors.New("structure revision preview is stale")
var ErrStructurePreviewTampered = errors.New("structure revision preview signature mismatch")

func StructureSignature(volumes []VolumeOutline) string {
	payload, _ := json.Marshal(ProjectLayeredOutlineOrder(CloneStructureSnapshot(volumes)))
	return ContentSignature(payload)
}

// StructureRevision derives the optimistic concurrency token from the same
// canonical structure bytes as StructureSignature. It is not a display
// sequence; it is an authoritative positive ETag revision that changes with
// every formal structure mutation and survives process restarts.
func StructureRevision(volumes []VolumeOutline) int {
	signature := StructureSignature(volumes)
	if len(signature) < 8 {
		return 1
	}
	value, err := strconv.ParseUint(signature[:8], 16, 32)
	if err != nil {
		return 1
	}
	return int(value%2_147_483_646) + 1
}

func CloneStructureSnapshot(volumes []VolumeOutline) []VolumeOutline {
	cloned := make([]VolumeOutline, len(volumes))
	for volumeIndex := range volumes {
		cloned[volumeIndex] = volumes[volumeIndex]
		cloned[volumeIndex].Arcs = make([]ArcOutline, len(volumes[volumeIndex].Arcs))
		for arcIndex := range volumes[volumeIndex].Arcs {
			cloned[volumeIndex].Arcs[arcIndex] = volumes[volumeIndex].Arcs[arcIndex]
			chapters := volumes[volumeIndex].Arcs[arcIndex].Chapters
			cloned[volumeIndex].Arcs[arcIndex].Chapters = make([]OutlineEntry, len(chapters))
			for chapterIndex := range chapters {
				cloned[volumeIndex].Arcs[arcIndex].Chapters[chapterIndex] = chapters[chapterIndex]
				cloned[volumeIndex].Arcs[arcIndex].Chapters[chapterIndex].Scenes = append([]string(nil), chapters[chapterIndex].Scenes...)
			}
		}
	}
	return cloned
}

func ValidateStructureSnapshot(volumes []VolumeOutline) error {
	if len(volumes) == 0 {
		return fmt.Errorf("structure must contain at least one volume")
	}
	seen := make(map[string]string)
	chapterCount := 0
	for _, volume := range volumes {
		if err := validateStructureNodeID(seen, volume.ID, StructureKindVolume); err != nil {
			return err
		}
		for _, arc := range volume.Arcs {
			if err := validateStructureNodeID(seen, arc.ID, StructureKindArc); err != nil {
				return err
			}
			for _, chapter := range arc.Chapters {
				if err := validateStructureNodeID(seen, chapter.ID, StructureKindChapter); err != nil {
					return err
				}
				chapterCount++
			}
		}
	}
	if chapterCount == 0 {
		return fmt.Errorf("structure must contain at least one chapter")
	}
	return nil
}

func validateStructureNodeID(seen map[string]string, id, kind string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return fmt.Errorf("%s stable ID is required", kind)
	}
	if previousKind, exists := seen[id]; exists {
		return fmt.Errorf("duplicate structure ID %q used by %s and %s", id, previousKind, kind)
	}
	seen[id] = kind
	return nil
}

func StructureChapterIDs(volumes []VolumeOutline) []string {
	ids := make([]string, 0)
	for _, volume := range volumes {
		for _, arc := range volume.Arcs {
			for _, chapter := range arc.Chapters {
				ids = append(ids, chapter.ID)
			}
		}
	}
	return ids
}

func StructureNodeIDs(volumes []VolumeOutline) []string {
	ids := make([]string, 0)
	for _, volume := range volumes {
		ids = append(ids, volume.ID)
		for _, arc := range volume.Arcs {
			ids = append(ids, arc.ID)
			for _, chapter := range arc.Chapters {
				ids = append(ids, chapter.ID)
			}
		}
	}
	return ids
}

func BodyRewriteChapterIDs(impacts []StructureImpactItem) []string {
	ids := make([]string, 0)
	for _, impact := range impacts {
		if impact.Level == StructureImpactRequired && impact.RequiresBodyRewrite && impact.ArtifactKind == StructureKindChapter {
			ids = append(ids, impact.ArtifactID)
		}
	}
	slices.Sort(ids)
	return slices.Compact(ids)
}

type BatchContextKind string

const (
	BatchContextAdjacentSummary BatchContextKind = "adjacent_summary"
	BatchContextCharacterState  BatchContextKind = "character_state"
	BatchContextOpenThread      BatchContextKind = "open_thread"
	BatchContextSetting         BatchContextKind = "setting"
	BatchContextFact            BatchContextKind = "fact"
	BatchContextSourceAnchor    BatchContextKind = "source_anchor"
)

type BatchContextItem struct {
	ID        string           `json:"id"`
	Kind      BatchContextKind `json:"kind"`
	Units     int              `json:"units"`
	Necessary bool             `json:"necessary"`
}

type BatchChapter struct {
	ID                   string             `json:"id"`
	VolumeID             string             `json:"volume_id"`
	ArcID                string             `json:"arc_id"`
	EstimatedOutputWords int                `json:"estimated_output_words"`
	Complexity           int                `json:"complexity"`
	CharacterCount       int                `json:"character_count"`
	SourceAnchorCount    int                `json:"source_anchor_count"`
	Context              []BatchContextItem `json:"context"`
}

type BatchStatus string

const (
	BatchStatusPending           BatchStatus = "pending"
	BatchStatusGenerating        BatchStatus = "generating"
	BatchStatusLocalAuditPending BatchStatus = "local_audit_pending"
	BatchStatusCompleted         BatchStatus = "completed"
	BatchStatusFailed            BatchStatus = "failed"
)

type BatchWork struct {
	ID                   string             `json:"id"`
	Index                int                `json:"index"`
	ChapterIDs           []string           `json:"chapter_ids"`
	VolumeID             string             `json:"volume_id"`
	ArcID                string             `json:"arc_id"`
	EstimatedOutputWords int                `json:"estimated_output_words"`
	ContextUnits         int                `json:"context_units"`
	Context              []BatchContextItem `json:"context"`
	Constrained          bool               `json:"constrained,omitempty"`
	Status               BatchStatus        `json:"status"`
	Attempts             int                `json:"attempts"`
	LastError            string             `json:"last_error,omitempty"`
}

type BatchReviewStatus string

const (
	BatchReviewPending    BatchReviewStatus = "pending"
	BatchReviewInProgress BatchReviewStatus = "in_progress"
	BatchReviewCompleted  BatchReviewStatus = "completed"
	BatchReviewFailed     BatchReviewStatus = "failed"
)

type BatchAggregateReview struct {
	ScopeID   string            `json:"scope_id"`
	Status    BatchReviewStatus `json:"status"`
	Attempts  int               `json:"attempts"`
	LastError string            `json:"last_error,omitempty"`
}

type BatchPlan struct {
	Batches         []BatchWork            `json:"batches"`
	VolumeReviews   []BatchAggregateReview `json:"volume_reviews"`
	WholeBookReview BatchAggregateReview   `json:"whole_book_review"`
}

func (p *BatchPlan) StartNext() (*BatchWork, error) {
	if p == nil {
		return nil, fmt.Errorf("batch plan is required")
	}
	for index := range p.Batches {
		if p.Batches[index].Status == BatchStatusGenerating || p.Batches[index].Status == BatchStatusLocalAuditPending {
			return nil, fmt.Errorf("batch %q is already active", p.Batches[index].ID)
		}
		if p.Batches[index].Status == BatchStatusFailed {
			return nil, fmt.Errorf("failed batch %q must be resumed before later batches can start", p.Batches[index].ID)
		}
	}
	for index := range p.Batches {
		if p.Batches[index].Status != BatchStatusPending {
			continue
		}
		if err := p.requirePriorVolumeReviews(p.Batches[index].VolumeID); err != nil {
			return nil, err
		}
		p.Batches[index].Status = BatchStatusGenerating
		p.Batches[index].Attempts++
		copy := cloneBatchWork(p.Batches[index])
		return &copy, nil
	}
	return nil, nil
}

func (p *BatchPlan) MarkGenerated(batchID string) error {
	batch, err := p.batch(batchID)
	if err != nil {
		return err
	}
	if batch.Status != BatchStatusGenerating {
		return fmt.Errorf("batch %q is not generating", batchID)
	}
	batch.Status = BatchStatusLocalAuditPending
	return nil
}

func (p *BatchPlan) MarkLocalAudit(batchID string, passed bool, report string) error {
	batch, err := p.batch(batchID)
	if err != nil {
		return err
	}
	if batch.Status != BatchStatusLocalAuditPending {
		return fmt.Errorf("batch %q is not awaiting local audit", batchID)
	}
	if passed {
		batch.Status = BatchStatusCompleted
		batch.LastError = ""
		return nil
	}
	batch.Status = BatchStatusFailed
	batch.LastError = strings.TrimSpace(report)
	if batch.LastError == "" {
		batch.LastError = "local audit failed"
	}
	return nil
}

func (p *BatchPlan) Fail(batchID, message string) error {
	batch, err := p.batch(batchID)
	if err != nil {
		return err
	}
	if batch.Status != BatchStatusGenerating && batch.Status != BatchStatusLocalAuditPending {
		return fmt.Errorf("batch %q is not active", batchID)
	}
	batch.Status = BatchStatusFailed
	batch.LastError = strings.TrimSpace(message)
	if batch.LastError == "" {
		batch.LastError = "batch failed"
	}
	return nil
}

func (p *BatchPlan) Resume(batchID string) error {
	batch, err := p.batch(batchID)
	if err != nil {
		return err
	}
	if batch.Status != BatchStatusFailed {
		return fmt.Errorf("batch %q is not failed", batchID)
	}
	batch.Status = BatchStatusPending
	batch.LastError = ""
	return nil
}

func (p BatchPlan) CanRunAggregateReview() bool {
	if len(p.VolumeReviews) == 0 || p.WholeBookReview.Status != BatchReviewPending {
		return false
	}
	for _, review := range p.VolumeReviews {
		if review.Status != BatchReviewCompleted {
			return false
		}
	}
	return true
}

func (p *BatchPlan) StartVolumeReview(volumeID string) error {
	review, index, err := p.volumeReview(volumeID)
	if err != nil {
		return err
	}
	if review.Status != BatchReviewPending {
		return fmt.Errorf("volume %q review is not pending", volumeID)
	}
	if err := p.requireNoActiveWork(); err != nil {
		return err
	}
	for batchIndex := range p.Batches {
		if p.Batches[batchIndex].VolumeID == review.ScopeID && p.Batches[batchIndex].Status != BatchStatusCompleted {
			return fmt.Errorf("volume %q review requires every local batch audit to pass", volumeID)
		}
	}
	for prior := 0; prior < index; prior++ {
		if p.VolumeReviews[prior].Status != BatchReviewCompleted {
			return fmt.Errorf("volume %q review must wait for prior volume %q", volumeID, p.VolumeReviews[prior].ScopeID)
		}
	}
	review.Status = BatchReviewInProgress
	review.Attempts++
	return nil
}

func (p *BatchPlan) MarkVolumeReview(volumeID string, passed bool, report string) error {
	review, _, err := p.volumeReview(volumeID)
	if err != nil {
		return err
	}
	return markBatchAggregateReview(review, passed, report)
}

func (p *BatchPlan) ResumeVolumeReview(volumeID string) error {
	review, _, err := p.volumeReview(volumeID)
	if err != nil {
		return err
	}
	if review.Status != BatchReviewFailed {
		return fmt.Errorf("volume %q review is not failed", volumeID)
	}
	review.Status = BatchReviewPending
	review.LastError = ""
	return nil
}

func (p *BatchPlan) StartWholeBookReview() error {
	if p == nil {
		return fmt.Errorf("batch plan is required")
	}
	if p.WholeBookReview.Status != BatchReviewPending {
		return fmt.Errorf("whole-book review is not pending")
	}
	if err := p.requireNoActiveWork(); err != nil {
		return err
	}
	for _, review := range p.VolumeReviews {
		if review.Status != BatchReviewCompleted {
			return fmt.Errorf("whole-book review must wait for volume %q review", review.ScopeID)
		}
	}
	p.WholeBookReview.Status = BatchReviewInProgress
	p.WholeBookReview.Attempts++
	return nil
}

func (p *BatchPlan) MarkWholeBookReview(passed bool, report string) error {
	if p == nil {
		return fmt.Errorf("batch plan is required")
	}
	return markBatchAggregateReview(&p.WholeBookReview, passed, report)
}

func (p *BatchPlan) ResumeWholeBookReview() error {
	if p == nil {
		return fmt.Errorf("batch plan is required")
	}
	if p.WholeBookReview.Status != BatchReviewFailed {
		return fmt.Errorf("whole-book review is not failed")
	}
	p.WholeBookReview.Status = BatchReviewPending
	p.WholeBookReview.LastError = ""
	return nil
}

func markBatchAggregateReview(review *BatchAggregateReview, passed bool, report string) error {
	if review.Status != BatchReviewInProgress {
		return fmt.Errorf("review %q is not in progress", review.ScopeID)
	}
	if passed {
		review.Status = BatchReviewCompleted
		review.LastError = ""
		return nil
	}
	review.Status = BatchReviewFailed
	review.LastError = strings.TrimSpace(report)
	if review.LastError == "" {
		review.LastError = "aggregate review failed"
	}
	return nil
}

func (p *BatchPlan) volumeReview(id string) (*BatchAggregateReview, int, error) {
	if p == nil {
		return nil, 0, fmt.Errorf("batch plan is required")
	}
	id = strings.TrimSpace(id)
	for index := range p.VolumeReviews {
		if p.VolumeReviews[index].ScopeID == id {
			return &p.VolumeReviews[index], index, nil
		}
	}
	return nil, 0, fmt.Errorf("volume %q is not present in the plan", id)
}

func (p *BatchPlan) requirePriorVolumeReviews(volumeID string) error {
	_, index, err := p.volumeReview(volumeID)
	if err != nil {
		return err
	}
	for prior := 0; prior < index; prior++ {
		if p.VolumeReviews[prior].Status != BatchReviewCompleted {
			return fmt.Errorf("volume %q batches must wait for prior volume %q review", volumeID, p.VolumeReviews[prior].ScopeID)
		}
	}
	return nil
}

func (p *BatchPlan) requireNoActiveWork() error {
	for _, batch := range p.Batches {
		if batch.Status == BatchStatusGenerating || batch.Status == BatchStatusLocalAuditPending {
			return fmt.Errorf("batch %q is active", batch.ID)
		}
	}
	for _, review := range p.VolumeReviews {
		if review.Status == BatchReviewInProgress {
			return fmt.Errorf("volume %q review is active", review.ScopeID)
		}
	}
	if p.WholeBookReview.Status == BatchReviewInProgress {
		return fmt.Errorf("whole-book review is active")
	}
	return nil
}

func (p *BatchPlan) batch(id string) (*BatchWork, error) {
	for index := range p.Batches {
		if p.Batches[index].ID == strings.TrimSpace(id) {
			return &p.Batches[index], nil
		}
	}
	return nil, fmt.Errorf("batch %q is not present in the plan", id)
}

func cloneBatchWork(batch BatchWork) BatchWork {
	batch.ChapterIDs = append([]string(nil), batch.ChapterIDs...)
	batch.Context = append([]BatchContextItem(nil), batch.Context...)
	return batch
}
