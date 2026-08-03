package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/voocel/agentcore/schema"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/errs"
	"github.com/voocel/ainovel-cli/internal/store"
)

// PlanChapterTool saves the writer's chapter plan before drafting.
type PlanChapterTool struct {
	store *store.Store
}

func NewPlanChapterTool(store *store.Store) *PlanChapterTool {
	return &PlanChapterTool{store: store}
}

func (t *PlanChapterTool) Name() string { return "plan_chapter" }
func (t *PlanChapterTool) Description() string {
	return "Save a chapter writing plan and contract before drafting."
}
func (t *PlanChapterTool) Label() string { return "plan chapter" }

func (t *PlanChapterTool) ReadOnly(_ json.RawMessage) bool        { return false }
func (t *PlanChapterTool) ConcurrencySafe(_ json.RawMessage) bool { return false }

func (t *PlanChapterTool) Schema() map[string]any {
	characterContractSchema := schema.Object(
		schema.Property("character_id", schema.String("confirmed StoryFoundation character ID")).Required(),
		schema.Property("beat", schema.String("matching outline character beat")),
		schema.Property("scene", schema.String("scene locator")),
		schema.Property("goal", schema.String("immediate chapter or scene goal")).Required(),
		schema.Property("immediate_motivation", schema.String("why the character pursues this goal now")).Required(),
		schema.Property("start_state", schema.String("dynamic state at chapter start")).Required(),
		schema.Property("allowed_changes", schema.Array("runtime state changes allowed in this chapter", schema.String(""))),
		schema.Property("voice_behavior", schema.Array("actionable voice and behavior cues", schema.String(""))).Required(),
		schema.Property("known", schema.Array("information known at chapter start", schema.String(""))),
		schema.Property("unknown", schema.Array("information still unknown", schema.String(""))),
		schema.Property("misconceptions", schema.Array("current misconceptions", schema.String(""))),
		schema.Property("may_learn", schema.Array("information that may be acquired on-page", schema.String(""))),
		schema.Property("must_preserve", schema.Array("static identity and behavior constraints", schema.String(""))).Required(),
		schema.Property("relationship_start", schema.String("runtime relationship starting point")),
		schema.Property("relationship_advance", schema.String("expected evidence-backed progress")),
		schema.Property("forbidden_jumps", schema.Array("relationship or state jumps forbidden without transition", schema.String(""))),
	)
	return schema.Object(
		schema.Property("chapter", schema.Int("chapter number")).Required(),
		schema.Property("title", schema.String("chapter title")).Required(),
		schema.Property("goal", schema.String("chapter goal")).Required(),
		schema.Property("conflict", schema.String("core conflict")).Required(),
		schema.Property("hook", schema.String("ending hook")).Required(),
		schema.Property("emotion_arc", schema.String("emotion arc")),
		schema.Property("notes", schema.String("free-form planning notes")),
		schema.Property("required_beats", schema.Array("required beats", schema.String(""))),
		schema.Property("forbidden_moves", schema.Array("forbidden moves", schema.String(""))),
		schema.Property("continuity_checks", schema.Array("continuity checks", schema.String(""))),
		schema.Property("evaluation_focus", schema.Array("editor evaluation focus", schema.String(""))),
		schema.Property("emotion_target", schema.String("optional target reader emotion")),
		schema.Property("payoff_points", schema.Array("optional payoff points", schema.String(""))),
		schema.Property("hook_goal", schema.String("optional hook goal")),
		schema.Property("character_contracts", schema.Array("per-character executable contracts; required when outline has stable character IDs", characterContractSchema)),
	)
}

func (t *PlanChapterTool) Execute(_ context.Context, args json.RawMessage) (json.RawMessage, error) {
	plan, err := decodeChapterPlanArgs(args)
	if err != nil {
		return nil, fmt.Errorf("invalid args: %w: %w", errs.ErrToolArgs, err)
	}
	if plan.Chapter <= 0 {
		return nil, fmt.Errorf("chapter must be > 0: %w", errs.ErrToolArgs)
	}
	if t.store.Progress.IsChapterCompleted(plan.Chapter) {
		return json.Marshal(map[string]any{
			"chapter":   plan.Chapter,
			"skipped":   true,
			"completed": true,
			"reason":    fmt.Sprintf("chapter %d is already completed and cannot be replanned", plan.Chapter),
		})
	}
	if err := t.store.Progress.ValidateChapterWork(plan.Chapter); err != nil {
		return nil, err
	}
	if err := EnsureAdaptationChapterPlanned(t.store, plan.Chapter); err != nil {
		return nil, err
	}
	if err := EnsureChapterExpanded(t.store, plan.Chapter); err != nil {
		return nil, err
	}
	plan = t.enrichAdaptationContract(plan)
	if err := t.validateCharacterContracts(&plan); err != nil {
		return nil, err
	}

	if err := t.store.Drafts.SaveChapterPlan(plan); err != nil {
		return nil, fmt.Errorf("save chapter plan: %w", err)
	}
	if err := t.store.Progress.StartChapter(plan.Chapter); err != nil {
		return nil, fmt.Errorf("mark chapter in progress: %w", err)
	}

	if _, err := t.store.Checkpoints.AppendArtifact(
		domain.ChapterScope(plan.Chapter), "plan",
		fmt.Sprintf("drafts/%02d.plan.json", plan.Chapter),
	); err != nil {
		return nil, fmt.Errorf("checkpoint chapter plan: %w", err)
	}

	return json.Marshal(map[string]any{
		"planned":   true,
		"chapter":   plan.Chapter,
		"next_step": t.nextStepAfterPlan(plan.Chapter),
	})
}

func (t *PlanChapterTool) nextStepAfterPlan(chapter int) string {
	defaultStep := "Call draft_chapter(chapter=current, mode=\"write\", content=full chapter prose). Do not re-plan the same chapter."
	if !t.store.Adaptation.Active() {
		return defaultStep
	}
	plan, err := t.store.Adaptation.LoadPlan()
	if err != nil || plan == nil || plan.Status != domain.AdaptationPlanStatusConfirmed {
		return defaultStep
	}
	chapterPlan, ok := findAdaptationChapterPlan(plan, chapter)
	if !ok {
		return defaultStep
	}
	if domain.AdaptationRewritePolicyForGranularity(plan.Granularity) == domain.AdaptationRewritePreserveDetails {
		return fmt.Sprintf(
			"preserve_details next step: first call read_chapter(source=\"source\", chapter=source_ref) for source_chapters=%v; then call draft_chapter(mode=\"write\") with 完整章节正文. Unaffected source paragraphs may be preserved, but required_changes scene units must be rewritten as prose; 不能只写改动片段 or add inner-monologue/meta labels. Hard range: %d-%d runes.",
			chapterPlan.SourceChapters, chapterPlan.TargetMinRunes, chapterPlan.TargetMaxRunes,
		)
	}
	switch domain.NormalizeAdaptationGranularity(plan.Granularity) {
	case domain.AdaptationGranularityFree:
		return fmt.Sprintf(
			"free/full_rewrite next step: source_chapters=%v are optional background anchors, not a target-to-source chapter mapping. Do not read source only because refs exist; if a concrete fact is missing, call read_chapter(source=\"source\", chapter=source_ref) once for the needed anchor, then call draft_chapter(mode=\"write\") with complete new prose based on the confirmed proposal and current outline.",
			chapterPlan.SourceChapters,
		)
	case domain.AdaptationGranularityArc:
		return fmt.Sprintf(
			"arc/full_rewrite next step: source_chapters=%v are mainline/arc anchors. Read source refs only as needed to verify causal facts, then call draft_chapter(mode=\"write\") with complete new prose; do not preserve source paragraphs.",
			chapterPlan.SourceChapters,
		)
	}
	return fmt.Sprintf(
		"full_rewrite next step: first call read_chapter(source=\"source\", chapter=source_ref) for source_chapters=%v, then call draft_chapter(mode=\"write\") with complete new prose.",
		chapterPlan.SourceChapters,
	)
}

func (t *PlanChapterTool) enrichAdaptationContract(plan domain.ChapterPlan) domain.ChapterPlan {
	if !t.store.Adaptation.Active() {
		return plan
	}
	adaptPlan, err := t.store.Adaptation.LoadPlan()
	if err != nil || adaptPlan == nil || adaptPlan.Status != domain.AdaptationPlanStatusConfirmed {
		return plan
	}
	chapterPlan, ok := findAdaptationChapterPlan(adaptPlan, plan.Chapter)
	if !ok {
		return plan
	}
	plan.Contract.RequiredBeats = appendUniqueStrings(plan.Contract.RequiredBeats, chapterPlan.PreserveEvents...)
	plan.Contract.RequiredBeats = appendUniqueStrings(plan.Contract.RequiredBeats, chapterPlan.RequiredChanges...)
	plan.Contract.ForbiddenMoves = appendUniqueStrings(plan.Contract.ForbiddenMoves, chapterPlan.ForbiddenMoves...)
	if domain.AdaptationRewritePolicyForGranularity(adaptPlan.Granularity) == domain.AdaptationRewritePreserveDetails {
		plan.Contract.EvaluationFocus = appendUniqueStrings(plan.Contract.EvaluationFocus,
			"preserve_details: unaffected source paragraphs may remain; required_changes full scene units must be rewritten as original prose.",
			"Do not express adaptation changes as parenthetical notes, inner-monologue labels, psychology labels, or 'only an example' text.",
			"check_adaptation must include change_evidence describing which source scene was rewritten and how it appears naturally in prose.",
		)
	} else if domain.NormalizeAdaptationGranularity(adaptPlan.Granularity) == domain.AdaptationGranularityFree {
		plan.Contract.EvaluationFocus = appendUniqueStrings(plan.Contract.EvaluationFocus,
			"free/full_rewrite: source_chapters are optional background anchors and do not mean this target chapter corresponds to a source chapter.",
			"Follow the confirmed adaptation proposal, target outline, and already written new continuity; do not relabel the chapter as preserve_details.",
		)
	} else if domain.NormalizeAdaptationGranularity(adaptPlan.Granularity) == domain.AdaptationGranularityArc {
		plan.Contract.EvaluationFocus = appendUniqueStrings(plan.Contract.EvaluationFocus,
			"arc/full_rewrite: source_chapters are mainline and arc anchors; write complete new prose without preserving source paragraphs.",
		)
	}
	return plan
}

func appendUniqueStrings(items []string, extra ...string) []string {
	seen := make(map[string]struct{}, len(items)+len(extra))
	out := make([]string, 0, len(items)+len(extra))
	for _, item := range append(items, extra...) {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		out = append(out, item)
	}
	return out
}

func decodeChapterPlanArgs(args json.RawMessage) (domain.ChapterPlan, error) {
	var a struct {
		Chapter            int                               `json:"chapter"`
		Title              string                            `json:"title"`
		Goal               string                            `json:"goal"`
		Conflict           string                            `json:"conflict"`
		Hook               string                            `json:"hook"`
		EmotionArc         string                            `json:"emotion_arc"`
		Notes              string                            `json:"notes"`
		RequiredBeats      []string                          `json:"required_beats"`
		ForbiddenMoves     []string                          `json:"forbidden_moves"`
		ContinuityChecks   []string                          `json:"continuity_checks"`
		EvaluationFocus    []string                          `json:"evaluation_focus"`
		EmotionTarget      string                            `json:"emotion_target"`
		PayoffPoints       []string                          `json:"payoff_points"`
		HookGoal           string                            `json:"hook_goal"`
		CharacterContracts []domain.ChapterCharacterContract `json:"character_contracts"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return domain.ChapterPlan{}, err
	}

	return domain.ChapterPlan{
		Chapter:    a.Chapter,
		Title:      a.Title,
		Goal:       a.Goal,
		Conflict:   a.Conflict,
		Hook:       a.Hook,
		EmotionArc: a.EmotionArc,
		Notes:      a.Notes,
		Contract: domain.ChapterContract{
			RequiredBeats:    a.RequiredBeats,
			ForbiddenMoves:   a.ForbiddenMoves,
			ContinuityChecks: a.ContinuityChecks,
			EvaluationFocus:  a.EvaluationFocus,
			EmotionTarget:    a.EmotionTarget,
			PayoffPoints:     a.PayoffPoints,
			HookGoal:         a.HookGoal,
			Characters:       a.CharacterContracts,
		},
	}, nil
}
