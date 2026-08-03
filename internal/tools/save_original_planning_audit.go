package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/voocel/agentcore/schema"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/errs"
	"github.com/voocel/ainovel-cli/internal/store"
)

// SaveOriginalPlanningAuditTool persists the independent editorial gates used
// while a normal-original long-form outline is being expanded. It deliberately
// excludes adaptation/source-fidelity criteria.
type SaveOriginalPlanningAuditTool struct{ store *store.Store }

type observedPlanningSceneCount struct {
	Chapter int `json:"chapter"`
	Count   int `json:"count"`
}

func NewSaveOriginalPlanningAuditTool(st *store.Store) *SaveOriginalPlanningAuditTool {
	return &SaveOriginalPlanningAuditTool{store: st}
}

func (t *SaveOriginalPlanningAuditTool) Name() string  { return "save_original_planning_audit" }
func (t *SaveOriginalPlanningAuditTool) Label() string { return "保存原创细纲审核" }
func (t *SaveOriginalPlanningAuditTool) Description() string {
	return "保存普通原创长篇的分批质量审核。分卷交给用户前使用 skeleton_volume / skeleton_book_batch / skeleton_book；详细细纲交给用户前使用 chapter / arc / volume / book_batch / book。每次只审一章、一个卷、一个3-4章弧或最多2卷摘要；不通过必须定点返修并复审。原创审核只判断因果、人物、节奏、冲突、情感、伏笔回收、世界规则、原创性和结局兑现，不得引用原著覆盖率。"
}
func (t *SaveOriginalPlanningAuditTool) ReadOnly(json.RawMessage) bool        { return false }
func (t *SaveOriginalPlanningAuditTool) ConcurrencySafe(json.RawMessage) bool { return false }
func (t *SaveOriginalPlanningAuditTool) StrictSchema() bool                   { return true }

func (t *SaveOriginalPlanningAuditTool) Schema() map[string]any {
	dimensionSchema := schema.Object(
		schema.Property("name", schema.String("审核维度名称；必须使用当前 scope 指定的维度名")).Required(),
		schema.Property("score", schema.Number("评分（0-10）")).Required(),
		schema.Property("comment", schema.String("基于当前审核范围的证据结论")).Required(),
	)
	issueSchema := schema.Object(
		schema.Property("severity", schema.Enum("问题严重度", "blocking", "major", "minor")).Required(),
		schema.Property("volume", schema.Int("问题所在卷号；不适用时传 0")).Required(),
		schema.Property("arc", schema.Int("问题所在弧号；不适用时传 0")).Required(),
		schema.Property("from_chapter", schema.Int("问题起始章节；不适用时传 0")).Required(),
		schema.Property("to_chapter", schema.Int("问题结束章节；不适用时传 0")).Required(),
		schema.Property("description", schema.String("有证据的问题描述")).Required(),
		schema.Property("repair_instruction", schema.String("可执行的定点修复指令")).Required(),
	)
	observedSceneCountSchema := schema.Object(
		schema.Property("chapter", schema.Int("planning_audit 返回的章节号")).Required(),
		schema.Property("count", schema.Int("该章明确返回的 scene_count；不得凭摘要猜测")).Required(),
	)
	return schema.Object(
		schema.Property("scope", schema.Enum("审核层级", "skeleton_volume", "skeleton_book_batch", "skeleton_book", "chapter", "arc", "volume", "book_batch", "book")).Required(),
		schema.Property("scope_id", schema.String("chapter 必须填写 novel_context 返回的当前章节稳定 ID；其他 scope 传空字符串")).Required(),
		schema.Property("volume", schema.Int("弧/卷审核的卷号；不适用时传 0")).Required(),
		schema.Property("arc", schema.Int("弧审核的弧号；不适用时传 0")).Required(),
		schema.Property("from_volume", schema.Int("book_batch 起始卷号；不适用时传 0")).Required(),
		schema.Property("to_volume", schema.Int("book_batch 结束卷号（最多2卷）；不适用时传 0")).Required(),
		schema.Property("from_chapter", schema.Int("审核证据起始章节；不适用时传 0")).Required(),
		schema.Property("to_chapter", schema.Int("审核证据结束章节；不适用时传 0")).Required(),
		schema.Property("verdict", schema.Enum("审核结论", "pass", "revise")).Required(),
		schema.Property("summary", schema.String("有证据的审核结论")).Required(),
		schema.Property("dimensions", schema.Array("审核维度数组", dimensionSchema)).Required(),
		schema.Property("issues", schema.Array("问题数组；pass 且无问题时传 []", issueSchema)).Required(),
		schema.Property(
			"observed_scene_counts",
			schema.Array(
				"chapter/arc 细纲审核必须逐章回传 planning_audit 的 scene_count；其他 scope 传 []",
				observedSceneCountSchema,
			),
		).Required(),
	)
}

func (t *SaveOriginalPlanningAuditTool) Execute(ctx context.Context, args json.RawMessage) (json.RawMessage, error) {
	if t.store == nil {
		return nil, fmt.Errorf("store is required")
	}
	var input struct {
		domain.OriginalPlanningAudit
		ObservedSceneCounts []observedPlanningSceneCount `json:"observed_scene_counts"`
	}
	if err := json.Unmarshal(args, &input); err != nil {
		return nil, fmt.Errorf("invalid original planning audit: %w: %w", errs.ErrToolArgs, err)
	}
	audit := input.OriginalPlanningAudit
	if err := validateOriginalPlanningAudit(audit); err != nil {
		return nil, fmt.Errorf("invalid original planning audit: %w: %w", errs.ErrToolArgs, err)
	}
	var foundationOwner *store.FoundationPlanningOwner
	if active, activeErr := t.store.Revisions.Active(); activeErr != nil {
		return nil, fmt.Errorf("read active revision before planning audit: %w", activeErr)
	} else if active != nil {
		if active.Mode != domain.RevisionModeFoundation {
			return nil, fmt.Errorf("planning audit is blocked by active revision %s: %w", active.ID, errs.ErrToolPrecondition)
		}
		artifactID, scopeErr := foundationAuditArtifactID(t.store, audit)
		if scopeErr != nil {
			return nil, fmt.Errorf("resolve Foundation audit scope: %w", scopeErr)
		}
		foundationOwner, scopeErr = t.store.Revisions.AuthorizeFoundationPlanning(ctx, artifactID)
		if scopeErr != nil {
			return nil, fmt.Errorf("authorize Foundation planning audit: %w: %w", errs.ErrToolPrecondition, scopeErr)
		}
	}
	volumes, err := t.store.Outline.LoadLayeredOutline()
	if err != nil {
		return nil, fmt.Errorf("load original planning audit structure: %w", err)
	}
	foundationRevision, foundationSignature, err := t.store.CurrentApprovedFoundationBinding()
	if err != nil {
		return nil, fmt.Errorf("foundation confirmation gate: %w: %w", errs.ErrToolPrecondition, err)
	}
	if err := domain.BindOriginalPlanningAudit(&audit, volumes, foundationSignature); err != nil {
		return nil, fmt.Errorf("bind original planning audit: %w: %w", errs.ErrToolPrecondition, err)
	}
	if err := validateObservedPlanningSceneCounts(
		volumes,
		audit,
		input.ObservedSceneCounts,
	); err != nil {
		return nil, fmt.Errorf("original planning audit scene evidence: %w: %w", errs.ErrToolPrecondition, err)
	}
	audit.FoundationRevision = foundationRevision
	if err := validateOriginalPlanningAuditEvidence(t.store, audit); err != nil {
		return nil, fmt.Errorf("original planning audit evidence: %w: %w", errs.ErrToolPrecondition, err)
	}
	audit.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	var saveErr error
	if foundationOwner != nil {
		saveErr = t.store.OriginalPlanningAudits.SaveForFoundationRevision(foundationOwner, audit)
	} else {
		saveErr = t.store.OriginalPlanningAudits.Save(audit)
	}
	if saveErr != nil {
		return nil, fmt.Errorf("save original planning audit: %w: %w", errs.ErrStoreWrite, saveErr)
	}
	artifact := "meta/original_planning/audits.json"
	if _, err := t.store.Checkpoints.AppendArtifact(domain.GlobalScope(), "original_planning_audit", artifact); err != nil {
		return nil, fmt.Errorf("checkpoint original planning audit: %w", err)
	}
	result := map[string]any{
		"saved": true, "scope": audit.Scope, "verdict": audit.Verdict,
		"volume": audit.Volume, "arc": audit.Arc,
	}
	if audit.Verdict == "revise" {
		result["repair_required"] = true
		result["issues"] = audit.Issues
	} else {
		result["continue_planning"] = audit.Scope != "book"
	}
	if audit.Scope == "book" && audit.Verdict == "pass" {
		review, err := t.store.RunMeta.PlanningReview()
		if err != nil {
			return nil, err
		}
		if review == nil || review.Status != domain.PlanningReviewStatusCollecting || review.Kind != domain.PlanningReviewKindVolumeSplit {
			return nil, fmt.Errorf("book audit passed outside detailed-outline collection: %w", errs.ErrToolPrecondition)
		}
		review.Status = domain.PlanningReviewStatusPending
		review.Kind = domain.PlanningReviewKindChapterOutline
		review.UpdatedAt = audit.UpdatedAt
		if err := t.store.RunMeta.SetPlanningReview(review); err != nil {
			return nil, fmt.Errorf("open chapter outline review: %w", err)
		}
		result["planning_review"] = domain.PlanningReviewStatusPending
		result["planning_review_kind"] = domain.PlanningReviewKindChapterOutline
	}
	if audit.Scope == "skeleton_book" && audit.Verdict == "pass" {
		review, err := t.store.RunMeta.PlanningReview()
		if err != nil {
			return nil, err
		}
		if review == nil || review.Status != domain.PlanningReviewStatusCollecting || review.Kind != domain.PlanningReviewKindBlueprint {
			return nil, fmt.Errorf("skeleton book audit passed outside blueprint collection: %w", errs.ErrToolPrecondition)
		}
		review.Status = domain.PlanningReviewStatusPending
		review.Kind = domain.PlanningReviewKindVolumeSplit
		review.UpdatedAt = audit.UpdatedAt
		if err := t.store.RunMeta.SetPlanningReview(review); err != nil {
			return nil, fmt.Errorf("open volume review: %w", err)
		}
		result["planning_review"] = domain.PlanningReviewStatusPending
		result["planning_review_kind"] = domain.PlanningReviewKindVolumeSplit
	}
	return json.Marshal(result)
}

func validateObservedPlanningSceneCounts(
	volumes []domain.VolumeOutline,
	audit domain.OriginalPlanningAudit,
	observed []observedPlanningSceneCount,
) error {
	if audit.Scope != "chapter" && audit.Scope != "arc" {
		return nil
	}
	expected := make(map[int]int, audit.ToChapter-audit.FromChapter+1)
	nextChapter := 1
	for _, volume := range volumes {
		for _, arc := range volume.Arcs {
			count := len(arc.Chapters)
			if count == 0 {
				count = arc.EstimatedChapters
			}
			for index, chapter := range arc.Chapters {
				number := nextChapter + index
				if number >= audit.FromChapter && number <= audit.ToChapter {
					expected[number] = len(chapter.Scenes)
				}
			}
			nextChapter += count
		}
	}
	if len(expected) != audit.ToChapter-audit.FromChapter+1 {
		return fmt.Errorf(
			"detailed outline range %d-%d is incomplete",
			audit.FromChapter,
			audit.ToChapter,
		)
	}
	actual := make(map[int]int, len(observed))
	for _, item := range observed {
		if _, duplicate := actual[item.Chapter]; duplicate {
			return fmt.Errorf("chapter %d scene_count was submitted more than once", item.Chapter)
		}
		actual[item.Chapter] = item.Count
	}
	for chapter := audit.FromChapter; chapter <= audit.ToChapter; chapter++ {
		count, ok := actual[chapter]
		if !ok {
			return fmt.Errorf(
				"chapter %d scene_count is required; reload planning_audit and inspect every scene",
				chapter,
			)
		}
		if count != expected[chapter] {
			return fmt.Errorf(
				"chapter %d observed scene_count=%d contradicts durable outline scene_count=%d; "+
					"reload planning_audit and audit every supplied scene before resubmitting",
				chapter,
				count,
				expected[chapter],
			)
		}
	}
	if len(actual) != len(expected) {
		return fmt.Errorf(
			"observed_scene_counts must contain exactly chapters %d-%d",
			audit.FromChapter,
			audit.ToChapter,
		)
	}
	return nil
}

var originalPlanningAuditDimensions = map[string][]string{
	"skeleton_volume":     {"volume_function", "arc_causality", "character_progression", "conflict_escalation", "budget_capacity", "payoff_and_handoff"},
	"skeleton_book_batch": {"cross_volume_continuity", "escalation", "character_progression", "setup_payoff", "pacing_balance", "plot_diversity"},
	"skeleton_book":       {"mainline_completeness", "ending_closure", "character_arc_completeness", "setup_payoff", "volume_balance", "budget_capacity", "originality"},
	"chapter":             {"causal_value", "character_logic", "continuity", "scene_progression", "hook_and_pacing", "originality"},
	"arc":                 {"causal_progression", "character_logic", "chapter_value", "continuity", "hook_and_pacing", "originality"},
	"volume":              {"structure_pacing", "theme_conflict", "climax_payoff", "character_arc", "budget_capacity", "next_volume_drive"},
	"book_batch":          {"cross_volume_continuity", "escalation", "character_progression", "setup_payoff", "pacing_balance", "originality"},
	"book":                {"mainline_closure", "character_closure", "setup_payoff", "escalation_pacing", "world_consistency", "originality", "ending_delivery"},
}

func validateOriginalPlanningAudit(audit domain.OriginalPlanningAudit) error {
	required, ok := originalPlanningAuditDimensions[audit.Scope]
	if !ok {
		return fmt.Errorf("unsupported scope %q", audit.Scope)
	}
	if strings.TrimSpace(audit.Summary) == "" {
		return fmt.Errorf("summary is required")
	}
	if audit.Verdict != "pass" && audit.Verdict != "revise" {
		return fmt.Errorf("verdict must be pass or revise")
	}
	switch audit.Scope {
	case "skeleton_volume":
		if audit.Volume <= 0 {
			return fmt.Errorf("skeleton_volume audit requires volume")
		}
	case "skeleton_book_batch":
		if audit.FromVolume <= 0 || audit.ToVolume < audit.FromVolume || audit.ToVolume-audit.FromVolume+1 > 2 {
			return fmt.Errorf("skeleton_book_batch must cover one or two volumes")
		}
	case "arc":
		if audit.Volume <= 0 || audit.Arc <= 0 {
			return fmt.Errorf("arc audit requires volume and arc")
		}
		if audit.FromChapter <= 0 || audit.ToChapter < audit.FromChapter || audit.ToChapter-audit.FromChapter+1 > 4 {
			return fmt.Errorf("arc audit must cite one batch of at most 4 chapters")
		}
	case "chapter":
		if strings.TrimSpace(audit.ScopeID) == "" || audit.FromChapter <= 0 || audit.ToChapter != audit.FromChapter {
			return fmt.Errorf("chapter audit must cite a stable scope_id and exactly one current chapter number")
		}
	case "volume":
		if audit.Volume <= 0 {
			return fmt.Errorf("volume audit requires volume")
		}
	case "book_batch":
		if audit.FromVolume <= 0 || audit.ToVolume < audit.FromVolume || audit.ToVolume-audit.FromVolume+1 > 2 {
			return fmt.Errorf("book_batch must cover one or two volumes")
		}
	}
	seen := make(map[string]bool, len(audit.Dimensions))
	for _, dimension := range audit.Dimensions {
		name := strings.TrimSpace(dimension.Name)
		if name == "" || dimension.Score < 0 || dimension.Score > 10 || strings.TrimSpace(dimension.Comment) == "" {
			return fmt.Errorf("every dimension needs name, score 0-10, and comment")
		}
		seen[name] = true
	}
	for _, name := range required {
		if !seen[name] {
			return fmt.Errorf("missing required %s dimension %q", audit.Scope, name)
		}
	}
	if audit.Verdict == "pass" {
		for _, dimension := range audit.Dimensions {
			if dimension.Score < 7 {
				return fmt.Errorf("pass requires every dimension score >=7; %s=%g", dimension.Name, dimension.Score)
			}
		}
		for _, issue := range audit.Issues {
			if issue.Severity == "blocking" || issue.Severity == "major" {
				return fmt.Errorf("pass cannot contain %s issues", issue.Severity)
			}
		}
	} else {
		if len(audit.Issues) == 0 {
			return fmt.Errorf("revise requires at least one issue")
		}
		first := audit.Issues[0]
		if first.Volume <= 0 || strings.TrimSpace(first.Description) == "" || strings.TrimSpace(first.RepairInstruction) == "" {
			return fmt.Errorf("the first revise issue must locate a volume and include description plus repair_instruction")
		}
		if !isOriginalSkeletonAuditScope(audit.Scope) && first.Arc <= 0 {
			return fmt.Errorf("the first detailed-outline revise issue must locate a volume/arc")
		}
	}
	return nil
}

func isOriginalSkeletonAuditScope(scope string) bool {
	return scope == "skeleton_volume" || scope == "skeleton_book_batch" || scope == "skeleton_book"
}

func validateOriginalPlanningAuditEvidence(st *store.Store, audit domain.OriginalPlanningAudit) error {
	volumes, err := st.Outline.LoadLayeredOutline()
	if err != nil || len(volumes) == 0 {
		return fmt.Errorf("load layered outline: %w", err)
	}
	if audit.Scope == "skeleton_volume" {
		for _, volume := range volumes {
			if volume.Index != audit.Volume {
				continue
			}
			// Detailed projects are intentionally projected back to their
			// volume/arc skeleton for a structure-only re-review.
			return nil
		}
		return fmt.Errorf("skeleton volume %d not found", audit.Volume)
	}
	if audit.Scope == "skeleton_book_batch" {
		for volume := audit.FromVolume; volume <= audit.ToVolume; volume++ {
			prior, err := st.OriginalPlanningAudits.Get("skeleton_volume", volume, 0)
			if err != nil || prior == nil || prior.Verdict != "pass" {
				return fmt.Errorf("skeleton volume %d audit must pass before batch synthesis", volume)
			}
		}
	}
	if audit.Scope == "skeleton_book" {
		for from := 1; from <= len(volumes); from += 2 {
			to := min(from+1, len(volumes))
			prior, err := findOriginalSkeletonBookBatch(st, from, to)
			if err != nil || prior == nil || prior.Verdict != "pass" {
				return fmt.Errorf("skeleton book batch V%d-V%d must pass before final synthesis", from, to)
			}
		}
	}
	if audit.Scope == "arc" {
		from, to, found := originalPlanningArcRange(volumes, audit.Volume, audit.Arc)
		if !found {
			return fmt.Errorf("V%d A%d not found or not expanded", audit.Volume, audit.Arc)
		}
		if audit.FromChapter != from || audit.ToChapter != to {
			return fmt.Errorf("V%d A%d evidence range is %d-%d, got %d-%d", audit.Volume, audit.Arc, from, to, audit.FromChapter, audit.ToChapter)
		}
	}
	if audit.Scope == "chapter" {
		if _, _, from, to, id, found := originalPlanningChapterLocation(volumes, audit.FromChapter); !found || from != to || id != audit.ScopeID {
			return fmt.Errorf("chapter %d is not present in the detailed outline", audit.FromChapter)
		}
	}
	if audit.Scope == "volume" {
		for _, volume := range volumes {
			if volume.Index != audit.Volume {
				continue
			}
			for _, arc := range volume.Arcs {
				prior, err := st.OriginalPlanningAudits.Get("arc", volume.Index, arc.Index)
				if err != nil || prior == nil || prior.Verdict != "pass" {
					return fmt.Errorf("V%d A%d arc audit must pass before volume synthesis", volume.Index, arc.Index)
				}
			}
			return nil
		}
		return fmt.Errorf("volume %d not found", audit.Volume)
	}
	if audit.Scope == "book_batch" {
		for volume := audit.FromVolume; volume <= audit.ToVolume; volume++ {
			prior, err := st.OriginalPlanningAudits.Get("volume", volume, 0)
			if err != nil || prior == nil || prior.Verdict != "pass" {
				return fmt.Errorf("volume %d audit must pass before book batch synthesis", volume)
			}
		}
	}
	if audit.Scope == "book" {
		for from := 1; from <= len(volumes); from += 2 {
			to := min(from+1, len(volumes))
			prior, err := st.OriginalPlanningAudits.GetBookBatch(from, to)
			if err != nil || prior == nil || prior.Verdict != "pass" {
				return fmt.Errorf("book batch V%d-V%d must pass before final synthesis", from, to)
			}
		}
	}
	return nil
}

func findOriginalSkeletonBookBatch(st *store.Store, fromVolume, toVolume int) (*domain.OriginalPlanningAudit, error) {
	audits, err := st.OriginalPlanningAudits.Load()
	if err != nil {
		return nil, err
	}
	volumes, err := st.Outline.LoadLayeredOutline()
	if err != nil {
		return nil, err
	}
	foundationRevision, foundationSignature, err := st.CurrentApprovedFoundationBinding()
	if err != nil {
		return nil, err
	}
	for i := range audits {
		audit := &audits[i]
		if audit.Scope == "skeleton_book_batch" && audit.FromVolume == fromVolume && audit.ToVolume == toVolume && audit.FoundationRevision == foundationRevision &&
			domain.OriginalPlanningAuditCurrent(*audit, volumes, foundationSignature) {
			return audit, nil
		}
	}
	return nil, nil
}

func originalPlanningChapterLocation(volumes []domain.VolumeOutline, chapter int) (volume, arc, from, to int, id string, found bool) {
	next := 1
	for _, item := range volumes {
		for _, storyArc := range item.Arcs {
			count := len(storyArc.Chapters)
			if count == 0 {
				count = storyArc.EstimatedChapters
			}
			end := next + count - 1
			if chapter >= next && chapter <= end && len(storyArc.Chapters) > 0 {
				return item.Index, storyArc.Index, chapter, chapter, storyArc.Chapters[chapter-next].ID, true
			}
			next = end + 1
		}
	}
	return 0, 0, 0, 0, "", false
}

func originalPlanningArcRange(volumes []domain.VolumeOutline, volumeIndex, arcIndex int) (int, int, bool) {
	next := 1
	for _, volume := range volumes {
		for _, arc := range volume.Arcs {
			count := len(arc.Chapters)
			if count == 0 {
				count = arc.EstimatedChapters
			}
			if volume.Index == volumeIndex && arc.Index == arcIndex && len(arc.Chapters) > 0 {
				return next, next + len(arc.Chapters) - 1, true
			}
			next += count
		}
	}
	return 0, 0, false
}

func originalPlanningAuditKey(audit domain.OriginalPlanningAudit) string {
	switch audit.Scope {
	case "chapter":
		return fmt.Sprintf("ch%d", audit.FromChapter)
	case "arc":
		return fmt.Sprintf("v%d-a%d", audit.Volume, audit.Arc)
	case "volume":
		return fmt.Sprintf("v%d", audit.Volume)
	case "book_batch":
		return fmt.Sprintf("v%d-v%d", audit.FromVolume, audit.ToVolume)
	default:
		return "book"
	}
}

func foundationAuditArtifactID(st *store.Store, audit domain.OriginalPlanningAudit) (string, error) {
	if audit.ScopeID != "" {
		return audit.ScopeID, nil
	}
	if audit.Scope == "book" || audit.Scope == "book_batch" || audit.Scope == "skeleton_book" || audit.Scope == "skeleton_book_batch" {
		return "book", nil
	}
	volumes, err := st.Outline.LoadLayeredOutline()
	if err != nil {
		return "", err
	}
	for _, volume := range volumes {
		if volume.Index != audit.Volume {
			continue
		}
		if audit.Scope == "volume" || audit.Scope == "skeleton_volume" {
			return volume.ID, nil
		}
		for _, arc := range volume.Arcs {
			if arc.Index == audit.Arc {
				return arc.ID, nil
			}
		}
	}
	return "", fmt.Errorf("audit scope %s V%d/A%d is missing", audit.Scope, audit.Volume, audit.Arc)
}
