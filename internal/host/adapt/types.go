// Package adapt implements source-novel adaptation preparation.
package adapt

import (
	"time"

	"github.com/voocel/ainovel-cli/internal/domain"
)

type Stage string

const (
	StageSplitting  Stage = "splitting"
	StageFoundation Stage = "foundation"
	StageChapter    Stage = "chapter"
	StageDossier    Stage = "dossier"
	StageBriefing   Stage = "briefing"
	StagePlan       Stage = "plan"
	StageAudit      Stage = "audit"
	StagePaused     Stage = "paused"
	StageDone       Stage = "done"
	StageError      Stage = "error"
)

type Event struct {
	Time    time.Time
	Stage   Stage
	Current int
	Total   int
	Message string
	Err     error
}

type ProgressEmitter func(Stage, int, int, string, error)

type Options struct {
	SourcePath string
}

type ProposalOptions struct {
	Brief              string
	SourcePath         string
	Granularity        string
	RewritePolicy      string
	WordTolerance      float64
	TargetChapterCount int
	EmitProgress       ProgressEmitter `json:"-"`
	// outlineQualityFeedback is populated only after a plan-only retry fails.
	// It is intentionally not serialized or exposed as user brief text.
	outlineQualityFeedback string
}

type ProposalRevisionOptions struct {
	Target       string
	FromChapter  int
	ToChapter    int
	VolumeIndex  int
	Instruction  string
	EmitProgress ProgressEmitter `json:"-"`
}

type ProposalStageResult struct {
	VolumeReview *domain.AdaptationVolumeReview
	Proposal     *domain.AdaptationPlan
}

type ProposalDetailsOptions struct {
	EmitProgress ProgressEmitter `json:"-"`
}

type Prompts struct {
	Foundation      string
	FoundationMerge string
	Analyzer        string
	Planner         string
}
