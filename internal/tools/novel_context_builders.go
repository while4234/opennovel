package tools

import (
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/rules"
	"github.com/voocel/ainovel-cli/internal/store"
	"github.com/voocel/ainovel-cli/internal/stylestat"
)

type contextBuildState struct {
	chapter         int
	profile         domain.ContextProfile
	progress        *domain.Progress
	runMeta         *domain.RunMeta
	currentEntry    *domain.OutlineEntry
	chapterPlan     *domain.ChapterPlan
	storyThreads    []domain.RecallItem
	foreshadow      []domain.ForeshadowEntry
	relationships   []domain.RelationshipEntry
	allStateChanges []domain.StateChange
	styleRules      *domain.WritingStyleRules
	purpose         chapterContextPurpose
}

type chapterContextPurpose string

const (
	chapterContextWriting    chapterContextPurpose = "writing"
	chapterContextRewriting  chapterContextPurpose = "rewriting"
	chapterContextPolishing  chapterContextPurpose = "polishing"
	chapterContextRecovering chapterContextPurpose = "recovering"
)

func resolveChapterContextPurpose(progress *domain.Progress, chapter int) chapterContextPurpose {
	if progress == nil || !slices.Contains(progress.PendingRewrites, chapter) {
		return chapterContextWriting
	}
	if progress.Flow == domain.FlowPolishing {
		return chapterContextPolishing
	}
	return chapterContextRewriting
}

func chapterPurposeNeedsDraftingContext(purpose chapterContextPurpose) bool {
	return purpose != chapterContextPolishing && purpose != chapterContextRecovering
}

type chapterContextEnvelope struct {
	Working    map[string]any
	Episodic   map[string]any
	References map[string]any
	Selected   map[string]any
}

type architectContextEnvelope struct {
	Planning   map[string]any
	Foundation map[string]any
	References map[string]any
}

func newChapterContextEnvelope() chapterContextEnvelope {
	return chapterContextEnvelope{
		Working:    make(map[string]any),
		Episodic:   make(map[string]any),
		References: make(map[string]any),
		Selected:   make(map[string]any),
	}
}

func newArchitectContextEnvelope() architectContextEnvelope {
	return architectContextEnvelope{
		Planning:   make(map[string]any),
		Foundation: make(map[string]any),
		References: make(map[string]any),
	}
}

func (e chapterContextEnvelope) apply(result map[string]any) {
	// 合并而非替换：Execute 的章节路径会先后 apply 两个信封（seed + buildChapterContext），
	// 整体赋值会让第二次 apply 丢弃 seed 的容器内容，working_memory.* 等 canonical
	// 路径随之失效（prompt 指针指向空气，模型只能靠顶层镜像模糊容错）。
	mergeEnvelopeSection(result, "working_memory", e.Working)
	mergeEnvelopeSection(result, "episodic_memory", e.Episodic)
	mergeEnvelopeSection(result, "reference_pack", e.References)
	if len(e.Selected) > 0 {
		mergeEnvelopeSection(result, "selected_memory", e.Selected)
	}
	// Chapter consumers use the canonical memory sections. Mirroring the same
	// payload at the top level used to duplicate large references and contracts
	// in every Writer request (20 KiB of references twice in a mature project).
}

// mergeEnvelopeSection 把 section 合并进 result[key] 的既有容器；容器不存在时直接挂载。
func mergeEnvelopeSection(result map[string]any, key string, section map[string]any) {
	if existing, ok := result[key].(map[string]any); ok {
		for k, v := range section {
			existing[k] = v
		}
		return
	}
	result[key] = section
}

func (e architectContextEnvelope) apply(result map[string]any) {
	result["planning_memory"] = e.Planning
	result["foundation_memory"] = e.Foundation
	result["reference_pack"] = e.References
	// Architect consumers use the canonical memory sections. Top-level mirrors
	// duplicated the complete outline, foundation and reference payload, adding
	// more than 50 KiB to a mature project's model request.
}

// buildProgressStatus 仅在 Coordinator 调用（不传 chapter）时返回进度摘要,
// Writer 不需要这些信息,避免干扰写作。
func (t *ContextTool) buildProgressStatus(result map[string]any) {
	progress, err := t.store.Progress.Load()
	if err != nil || progress == nil {
		return
	}
	status := map[string]any{
		"phase":              string(progress.Phase),
		"flow":               string(progress.Flow),
		"completed_chapters": len(progress.CompletedChapters),
		"total_chapters":     progress.TotalChapters,
		"next_chapter":       progress.NextChapter(),
		"total_word_count":   progress.TotalWordCount,
	}
	if progress.InProgressChapter > 0 {
		status["in_progress_chapter"] = progress.InProgressChapter
	}
	if len(progress.PendingRewrites) > 0 {
		status["pending_rewrites"] = progress.PendingRewrites
		status["rewrite_reason"] = progress.RewriteReason
	}
	if progress.Layered {
		status["layered"] = true
		status["current_volume"] = progress.CurrentVolume
		status["current_arc"] = progress.CurrentArc
	}
	if progress.Phase == domain.PhaseComplete {
		status["finished"] = true
	}
	result["progress_status"] = status
}

// buildUserRules 把合并后的 Bundle 注入 working_memory.user_rules（canonical 路径）。
//
// 单点注入：writer / editor / architect / coordinator 任一路径调用 novel_context
// 都能在 working_memory.user_rules 拿到一致的偏好。architect 路径原本没有 working_memory，
// 由本函数按需新建（仅装 user_rules）；chapter > 0 路径下 working_memory 已存在，直接嵌入。
//
// 即便 Bundle 为空也注入，保持字段稳定，避免 LLM 看到 user_rules=null 而走异常分支。
//
// 注入策略：只给 LLM 看 structured + preferences——这两项才是创作时需要遵循的偏好。
// sources / conflicts 是诊断信息（用户冲突排查），不进 LLM；由 CLI 启动诊断面板按需展示。
func (t *ContextTool) buildUserRules(result map[string]any, omitRedundantPreferences bool) {
	snap, err := t.store.UserRules.Load()
	if err != nil || snap == nil {
		// 快照未生成（老书首次/异常）：退到代码内置默认，保证机械底线（字数/禁语/疲劳词）始终存在。
		def := rules.BuildSnapshot([]rules.Candidate{rules.SystemDefaults()})
		snap = &def
	}
	working, ok := result["working_memory"].(map[string]any)
	if !ok {
		working = map[string]any{}
		result["working_memory"] = working
	}
	payload := compactUserRulesPayload(snap.Payload())
	if omitRedundantPreferences {
		// Once a detailed chapter contract exists, the raw startup prompt has
		// already been normalized into the confirmed foundation, character
		// workset, chapter beats, and durable world rules. Replaying it here
		// duplicates story facts and can crowd the signed chapter package out of
		// provider byte limits. Keep the mechanical rules used by validators;
		// the original preferences remain durable and available to planning.
		delete(payload, "preferences")
	}
	working["user_rules"] = payload
}

func (t *ContextTool) buildWordBudget(result map[string]any, chapter int) {
	meta, err := t.store.RunMeta.Load()
	if err != nil || meta == nil || meta.WordBudget == nil || meta.WordBudget.TargetTotalWords <= 0 {
		return
	}
	progress, perr := t.store.Progress.Load()
	if perr != nil {
		return
	}
	payload, ok := meta.WordBudget.Runtime(progress, chapter)
	if !ok {
		return
	}
	working, ok := result["working_memory"].(map[string]any)
	if !ok {
		working = map[string]any{}
		result["working_memory"] = working
	}
	working["word_budget"] = payload
	result["word_budget"] = payload
}

func (t *ContextTool) buildSimulationProfile(result map[string]any, sectionKey string, warn func(string, error)) {
	profile, err := t.store.Simulation.Load()
	if err != nil {
		warn("simulation_profile", err)
		return
	}
	compact := t.compactArchitectSimulationProfile(profile)
	if compact == nil {
		return
	}
	section, ok := result[sectionKey].(map[string]any)
	if !ok {
		section = map[string]any{}
		result[sectionKey] = section
	}
	section["simulation_profile"] = compact
	result["simulation_profile"] = true
	if t.simulationMode == contextSimulationModeReinforced {
		result["simulation_mode"] = contextSimulationModeReinforced
	}
}

func (t *ContextTool) buildPlanningReviewSimulationProfile(result map[string]any, warn func(string, error)) {
	profile, err := t.store.Simulation.Load()
	if err != nil {
		warn("simulation_profile", err)
		return
	}
	compact := t.compactArchitectSimulationProfile(profile)
	if compact == nil {
		return
	}
	// Skeleton review needs the same structural signals as Architect, but the
	// role-specific instruction must belong to Editor rather than Architect.
	full := t.compactSimulationProfile(profile)
	if full != nil {
		compact.RoleGuidance = domain.SimulationRoleGuidance{
			Editor: compactStringList(full.RoleGuidance.Editor, 1, 60),
		}
	}
	section, ok := result["planning_memory"].(map[string]any)
	if !ok {
		section = map[string]any{}
		result["planning_memory"] = section
	}
	section["simulation_profile"] = compact
	result["simulation_profile"] = true
	if t.simulationMode == contextSimulationModeReinforced {
		result["simulation_mode"] = contextSimulationModeReinforced
	}
}

// scopePlanningReviewContext derives an Editor-owned model view from the full
// canonical planning context. It never mutates the persisted Foundation. All
// characters, relationships and rules remain represented, while rich
// character biography fields and out-of-scope volume bodies are replaced by
// stable review facts so an audit cannot overflow before it starts.
func (t *ContextTool) scopePlanningReviewContext(result map[string]any, volume, fromVolume, toVolume int) error {
	if volume > 0 {
		if fromVolume > 0 || toVolume > 0 {
			return fmt.Errorf("planning_review accepts volume or from_volume/to_volume, not both")
		}
		fromVolume, toVolume = volume, volume
	}
	if (fromVolume == 0) != (toVolume == 0) || fromVolume < 0 || toVolume < fromVolume {
		return fmt.Errorf("planning_review requires a valid inclusive volume range")
	}

	planning, _ := result["planning_memory"].(map[string]any)
	if planning != nil {
		if layered, err := t.store.Outline.LoadLayeredOutline(); err == nil && len(layered) > 0 {
			progress, _ := t.store.Progress.Load()
			planning["layered_outline"] = compactLayeredOutlineForPlanningReviewVolumes(
				layered,
				progress,
				fromVolume,
				toVolume,
			)
			planning["volume_history_index"] = compactVolumeHistoryIndex(layered)
			planning["volume_history_index_schema"] = []string{"index", "title", "chapter_count", "arc_count"}
			planning["volume_theme_milestones"] = compactVolumeThemeMilestones(layered)
			planning["volume_theme_milestones_schema"] = []string{"index", "theme"}
		}
		if audits, err := t.store.OriginalPlanningAudits.Load(); err == nil {
			planning["planning_audit_index"] = compactSkeletonAuditIndex(
				audits,
				fromVolume,
				toVolume,
			)
		}
		planning["review_scope"] = map[string]any{
			"kind":        "volume_skeleton",
			"from_volume": fromVolume,
			"to_volume":   toVolume,
		}
	}

	foundation, _ := result["foundation_memory"].(map[string]any)
	if foundation != nil {
		allCharactersFocused := false
		if characters, ok := foundation["characters"].([]domain.Character); ok {
			focused := planningReviewCharacterFocus(characters, planning)
			foundation["character_index"] = compactCharacterIndexForPlanningReview(characters)
			foundation["character_index_schema"] = []string{"id", "name", "role", "tier", "faction"}
			foundation["characters"] = compactCharacterContractsForPlanningReview(characters, focused)
			allCharactersFocused = len(focused) == len(characters)
		}
		if rules, ok := foundation["world_rules"].([]domain.WorldRule); ok {
			foundation["world_rules"] = compactWorldRulesForPlanningReview(rules)
		}
		if canonical, loadErr := t.store.Foundation.Load(); loadErr == nil {
			foundation["planned_relationships"] = compactRelationshipsForPlanningAudit(canonical.Relationships)
			foundation["relationship_schema"] = []string{
				"id", "source_character_id", "target_character_id", "type", "direction",
				"status", "label", "description", "since", "tags", "constraints",
			}
			foundation["world_rules"] = compactWorldRulesForPlanningAudit(canonical.WorldRules)
			foundation["world_rule_schema"] = []string{"id", "category", "strength", "rule", "boundary"}
		}
		delete(foundation, "character_snapshots")
		delete(foundation, "hard_world_rule_constraints")
		delete(foundation, "relationship_contract")
		delete(foundation, "premise_structure")
		delete(foundation, "foundation_status")
		delete(foundation, "foundation_revision")
		delete(foundation, "foundation_audit_signature")
		if allCharactersFocused {
			delete(foundation, "character_index")
			delete(foundation, "character_index_schema")
		}
	}
	// Architect templates do not provide evidence for an Editor verdict. The
	// Editor prompt and audit tool schema already define the scoring contract.
	delete(result, "reference_pack")
	delete(result, "memory_policy")
	delete(result, "word_budget")
	result["context_profile"] = "planning_review"
	return nil
}

// scopeGeneralPlanningContext returns a stable aggregate used while building
// Foundation artifacts and the whole-book compass. Canonical data remains on
// disk; this projection keeps identities and signatures without serializing
// every biography and reference template into each Architect turn.
func (t *ContextTool) scopeGeneralPlanningContext(result map[string]any) error {
	planning, _ := result["planning_memory"].(map[string]any)
	if planning != nil {
		if layered, err := t.store.Outline.LoadLayeredOutline(); err == nil && len(layered) > 0 {
			progress, _ := t.store.Progress.Load()
			planning["layered_outline"] = compactLayeredOutlineForPlanning(layered, progress)
			planning["volume_history_index"] = compactVolumeHistoryIndex(layered)
			planning["volume_history_index_schema"] = []string{"index", "title", "chapter_count", "arc_count"}
			planning["volume_theme_milestones"] = compactVolumeThemeMilestones(layered)
			planning["volume_theme_milestones_schema"] = []string{"index", "theme"}
		}
		delete(planning, "volume_summaries")
	}

	if err := t.compactPlanningFoundation(result, planning); err != nil {
		return err
	}
	if foundation, err := t.store.Foundation.Load(); err == nil && strings.TrimSpace(foundation.Premise) == "" {
		compactPrePremisePlanning(result)
	}
	result["context_profile"] = "planning"
	return nil
}

func compactPrePremisePlanning(result map[string]any) {
	foundation, _ := result["foundation_memory"].(map[string]any)
	if foundation != nil {
		if characters, ok := foundation["characters"].([]map[string]any); ok {
			identities := make([]map[string]any, 0, len(characters))
			for _, character := range characters {
				identities = append(identities, map[string]any{
					"id":      character["id"],
					"name":    character["name"],
					"role":    character["role"],
					"tier":    character["tier"],
					"faction": character["faction"],
				})
			}
			foundation["characters"] = identities
		}
		if relationships, ok := foundation["planned_relationships"].([][]any); ok {
			index := make([][]any, 0, len(relationships))
			for _, relationship := range relationships {
				if len(relationship) >= 4 {
					index = append(index, relationship[:4])
				}
			}
			foundation["planned_relationships"] = index
			foundation["relationship_schema"] = []string{"id", "source_character_id", "target_character_id", "type"}
		}
	}
	// Before the premise exists, the complete user-confirmed creative brief is
	// the source of truth. Embedded templates are already represented by the
	// Architect system prompt and must not displace that canonical input.
	delete(result, "reference_pack")
}

// scopePlanningVolumeContext is the bounded Architect view for creating,
// appending, or repairing one volume skeleton. It includes the target and
// adjacent volume contracts plus a complete stable index for the rest.
func (t *ContextTool) scopePlanningVolumeContext(result map[string]any, volume int) error {
	if volume <= 0 {
		return fmt.Errorf("planning_volume requires a positive volume")
	}
	layered, err := t.store.Outline.LoadLayeredOutline()
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("load layered outline for planning_volume: %w", err)
	}
	maxTarget := len(layered) + 1
	if volume > maxTarget {
		return fmt.Errorf("planning_volume target %d is outside existing or next volume range 1-%d", volume, maxTarget)
	}

	planning, _ := result["planning_memory"].(map[string]any)
	if planning == nil {
		planning = make(map[string]any)
		result["planning_memory"] = planning
	}
	progress, _ := t.store.Progress.Load()
	planning["layered_outline"] = compactLayeredOutlineForPlanningVolume(layered, progress, volume)
	planning["volume_history_index"] = compactVolumeHistoryIndex(layered)
	planning["volume_history_index_schema"] = []string{"index", "title", "chapter_count", "arc_count"}
	planning["volume_theme_milestones"] = compactVolumeThemeMilestones(layered)
	planning["volume_theme_milestones_schema"] = []string{"index", "theme"}
	planning["volume_scope"] = map[string]any{
		"target_volume":   volume,
		"target_exists":   volume <= len(layered),
		"previous_volume": max(volume-1, 0),
		"next_existing_volume": func() int {
			if volume < len(layered) {
				return volume + 1
			}
			return 0
		}(),
		"content_path": "planning_memory.layered_outline",
	}
	if audits, loadErr := t.store.OriginalPlanningAudits.Load(); loadErr == nil {
		planning["planning_audit_index"] = compactSkeletonAuditIndex(audits, volume, volume)
	}
	delete(planning, "volume_summaries")
	delete(planning, "completion_signals")

	if err := t.compactPlanningFoundation(result, planning); err != nil {
		return err
	}
	delete(result, "reference_pack")
	result["context_profile"] = "planning_volume"
	return nil
}

func (t *ContextTool) compactPlanningFoundation(result map[string]any, planning map[string]any) error {
	canonical, err := t.store.Foundation.Load()
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("load Foundation for planning context: %w", err)
	}
	foundation, _ := result["foundation_memory"].(map[string]any)
	if foundation == nil {
		foundation = make(map[string]any)
		result["foundation_memory"] = foundation
	}
	focused := planningReviewCharacterFocus(canonical.Characters, planning)
	foundation["character_index"] = compactCharacterIndexForPlanningReview(canonical.Characters)
	foundation["character_index_schema"] = []string{"id", "name", "role", "tier", "faction"}
	foundation["characters"] = compactCharacterContractsForPlanningReview(canonical.Characters, focused)
	foundation["planned_relationships"] = compactRelationshipsForPlanningAudit(canonical.Relationships)
	foundation["relationship_schema"] = []string{
		"id", "source_character_id", "target_character_id", "type", "direction",
		"status", "label", "description", "since", "tags", "constraints",
	}
	foundation["world_rules"] = compactWorldRulesForPlanningAudit(canonical.WorldRules)
	foundation["world_rule_schema"] = []string{"id", "category", "strength", "rule", "boundary"}
	hardRuleIDs := make([]string, 0, len(canonical.WorldRules))
	for _, rule := range canonical.WorldRules {
		if rule.Strength == domain.WorldRuleStrengthHard {
			hardRuleIDs = append(hardRuleIDs, rule.ID)
		}
	}
	if len(hardRuleIDs) > 0 {
		foundation["hard_world_rule_constraints"] = hardRuleIDs
	}
	foundation["relationship_contract"] = "planned_relationships are pre-writing canonical intent; relationship_state is runtime chapter evidence and must never replace or rewrite the plan"
	foundation["foundation_status"] = t.foundationStatus()
	if canonical.Revision > 0 {
		foundation["foundation_revision"] = canonical.Revision
		if signature, signatureErr := domain.FoundationAuditSignature(canonical); signatureErr == nil {
			foundation["foundation_audit_signature"] = signature
		}
	}
	delete(foundation, "character_snapshots")
	delete(foundation, "foreshadow_ledger")
	delete(foundation, "premise_structure")
	return nil
}

// scopePlanningAuditContext builds one self-contained evidence pack for a
// detailed arc audit or a bounded repair. It combines compact canonical facts
// with the exact 1-4 chapter window in one tool result, so Editor quality does
// not depend on stacking planning_detail and outline_range payloads.
func (t *ContextTool) scopePlanningAuditContext(
	result map[string]any,
	volume, arc, from, to int,
	warn func(string, error),
) error {
	if volume <= 0 || arc <= 0 || from <= 0 || to < from {
		return fmt.Errorf("planning_audit requires positive volume, arc and inclusive from/to")
	}
	if to-from+1 > 4 {
		return fmt.Errorf("planning_audit accepts at most four chapters")
	}
	layered, err := t.store.Outline.LoadLayeredOutline()
	if err != nil {
		return fmt.Errorf("load layered outline for planning_audit: %w", err)
	}
	_, targetArc, arcFrom, arcTo, ok := findPlanningDetailTarget(layered, volume, arc)
	if !ok {
		return fmt.Errorf("planning_audit target V%d A%d does not exist", volume, arc)
	}
	if from < arcFrom || to > arcTo {
		return fmt.Errorf(
			"planning_audit range %d-%d is outside V%d A%d (%d-%d)",
			from, to, volume, arc, arcFrom, arcTo,
		)
	}
	if err := t.scopePlanningReviewContext(result, volume, 0, 0); err != nil {
		return err
	}
	arcEntries := make([]domain.OutlineEntry, 0, len(targetArc.Chapters))
	for index, entry := range targetArc.Chapters {
		entry.Chapter = arcFrom + index
		arcEntries = append(arcEntries, entry)
	}
	entries := outlineEntriesInRange(arcEntries, from, to)
	// repair_arc persists the authoritative repaired chapters to the flat
	// outline before the layered planning skeleton is refreshed. Prefer that
	// newer flat window when it is complete, otherwise retain the layered arc.
	if flat, flatErr := t.store.Outline.LoadOutline(); flatErr == nil && len(flat) > 0 {
		repaired := outlineEntriesInRange(normalizeOutlineEntries(flat), from, to)
		if len(repaired) == to-from+1 {
			entries = repaired
		}
	}
	result["outline"] = compactOutlineEntriesForPlanningAudit(entries)
	sceneCounts := make(map[string]int, len(entries))
	for _, entry := range entries {
		sceneCounts[strconv.Itoa(entry.Chapter)] = len(entry.Scenes)
	}
	result["outline_scope"] = map[string]any{
		"mode":                 "planning_audit_range",
		"from":                 from,
		"to":                   to,
		"returned_chapters":    len(entries),
		"scene_counts":         sceneCounts,
		"total_arc_chapters":   len(targetArc.Chapters),
		"full_outline_omitted": true,
	}
	result["outline_character_beat_schema"] = []string{
		"character_id", "scene", "goal", "obstacle", "choice_cost", "advance",
	}
	result["outline_relationship_beat_schema"] = []string{
		"relationship_id", "source_character_id", "target_character_id",
		"scene", "start", "expected_advance", "forbidden_jump",
	}
	result["outline_temporary_role_schema"] = []string{"role", "scene", "purpose", "important"}

	planning, _ := result["planning_memory"].(map[string]any)
	if planning != nil {
		// Detailed audit carries the exact selected chapters in result["outline"].
		// Keep only a compact target-volume skeleton here so it does not compete
		// with those chapter facts for the planning_audit budget.
		progress, _ := t.store.Progress.Load()
		planning["layered_outline"] = compactLayeredOutlineWithFocus(
			layered,
			progress,
			map[int]bool{volume: true},
		)
		removePlanningReviewNearbyChapters(planning["layered_outline"])
		delete(planning, "volume_summaries")
		delete(planning, "completion_signals")
		planning["review_scope"] = map[string]any{
			"kind":         "detailed_arc",
			"volume":       volume,
			"arc":          arc,
			"from_chapter": from,
			"to_chapter":   to,
			"content_path": "outline",
			"quality_contract": "Audit the supplied chapters against every represented canonical " +
				"character, relationship, world-rule and passed volume contract. The explicit scene_count " +
				"and outline_scope.scene_counts values are authoritative; inspect every supplied scene and " +
				"do not infer omitted prose.",
		}
	}
	foundation, _ := result["foundation_memory"].(map[string]any)
	if foundation != nil {
		if canonical, loadErr := t.store.Foundation.Load(); loadErr == nil {
			foundation["planned_relationships"] = compactRelationshipsForPlanningAudit(canonical.Relationships)
			foundation["relationship_schema"] = []string{
				"id", "source_character_id", "target_character_id", "type", "direction",
				"status", "label", "description", "since", "tags", "constraints",
			}
			foundation["world_rules"] = compactWorldRulesForPlanningAudit(canonical.WorldRules)
			foundation["world_rule_schema"] = []string{"id", "category", "strength", "rule", "boundary"}
		}
		delete(foundation, "character_index")
		delete(foundation, "character_index_schema")
		delete(foundation, "hard_world_rule_constraints")
		delete(foundation, "relationship_contract")
		delete(foundation, "premise_structure")
		delete(foundation, "foundation_status")
		delete(foundation, "foundation_revision")
		delete(foundation, "foundation_audit_signature")
	}
	delete(result, "memory_policy")
	result["context_profile"] = "planning_audit"
	return nil
}

func removePlanningReviewNearbyChapters(value any) {
	volumes, ok := value.([]map[string]any)
	if !ok {
		return
	}
	for _, volume := range volumes {
		arcs, ok := volume["arcs"].([]map[string]any)
		if !ok {
			continue
		}
		for _, arc := range arcs {
			delete(arc, "nearby_chapters")
		}
	}
}

// scopePlanningDetailContext derives a fresh, high-fidelity Architect view for
// one skeleton arc. The complete canonical Foundation remains durable; this
// view keeps every character, planned relationship and world-rule identity
// represented while replacing unrelated volume prose and runtime snapshots
// with hierarchical indexes. Its size is therefore independent of book length.
func (t *ContextTool) scopePlanningDetailContext(result map[string]any, volume, arc int) error {
	if volume <= 0 || arc <= 0 {
		return fmt.Errorf("planning_detail requires positive volume and arc")
	}
	layered, err := t.store.Outline.LoadLayeredOutline()
	if err != nil {
		return fmt.Errorf("load layered outline for planning_detail: %w", err)
	}
	_, _, fromChapter, toChapter, ok := findPlanningDetailTarget(layered, volume, arc)
	if !ok {
		return fmt.Errorf("planning_detail target V%d A%d does not exist", volume, arc)
	}

	planning, _ := result["planning_memory"].(map[string]any)
	if planning == nil {
		planning = make(map[string]any)
		result["planning_memory"] = planning
	}
	planning["layered_outline"] = compactLayeredOutlineForPlanningDetail(layered, volume, arc)
	planning["volume_history_index"] = compactVolumeHistoryIndex(layered)
	planning["volume_history_index_schema"] = []string{"index", "title", "chapter_count", "arc_count"}
	planning["volume_theme_milestones"] = compactVolumeThemeMilestones(layered)
	planning["volume_theme_milestones_schema"] = []string{"index", "theme"}
	planning["detail_scope"] = map[string]any{
		"volume":        volume,
		"arc":           arc,
		"from_chapter":  fromChapter,
		"to_chapter":    toChapter,
		"chapter_count": toChapter - fromChapter + 1,
		"content_path":  "planning_memory.layered_outline[target=true]",
		"quality_contract": "Preserve every canonical character/rule/relationship fact and implement the passed " +
			"volume audit. Generate only this arc; do not invent replacement identities.",
	}
	if audit := selectSkeletonVolumeAudit(t.store, volume); audit != nil {
		planning["approved_volume_audit"] = compactPlanningDetailAudit(*audit)
	}
	if audits := selectPassedPriorArcAudits(t.store, volume, arc); len(audits) > 0 {
		planning["approved_prior_arc_audits"] = audits
	}
	delete(planning, "volume_summaries")
	delete(planning, "completion_signals")

	foundation, _ := result["foundation_memory"].(map[string]any)
	if foundation == nil {
		foundation = make(map[string]any)
		result["foundation_memory"] = foundation
	}
	if canonical, loadErr := t.store.Foundation.Load(); loadErr == nil {
		foundation["characters"] = compactCharacterContractsForPlanningDetail(canonical.Characters)
		foundation["planned_relationships"] = compactRelationshipsForPlanningAudit(canonical.Relationships)
		foundation["relationship_schema"] = []string{
			"id", "source_character_id", "target_character_id", "type", "direction",
			"status", "label", "description", "since", "tags", "constraints",
		}
		foundation["world_rules"] = compactWorldRulesForPlanningAudit(canonical.WorldRules)
		foundation["world_rule_schema"] = []string{"id", "category", "strength", "rule", "boundary"}
	}
	delete(foundation, "character_snapshots")
	delete(foundation, "foreshadow_ledger")
	delete(foundation, "hard_world_rule_constraints")
	delete(foundation, "relationship_contract")
	delete(foundation, "premise_structure")
	delete(foundation, "foundation_status")
	delete(foundation, "foundation_revision")
	delete(foundation, "foundation_audit_signature")
	delete(result, "memory_policy")
	result["context_profile"] = "planning_detail"
	return nil
}

// deduplicatePlanningDetailContext retains the structured prose constraints and
// points the Architect at the already-present creative brief instead of
// carrying the same startup prompt twice.
func deduplicatePlanningDetailContext(result map[string]any) {
	working, _ := result["working_memory"].(map[string]any)
	if working != nil {
		if userRules, ok := working["user_rules"].(map[string]any); ok {
			delete(userRules, "preferences")
			userRules["preferences_source"] = "planning_memory.creative_brief"
		}
	}
	delete(result, "word_budget")
}

func findPlanningDetailTarget(
	volumes []domain.VolumeOutline,
	volumeNumber, arcNumber int,
) (domain.VolumeOutline, domain.ArcOutline, int, int, bool) {
	chapter := 1
	for _, volume := range volumes {
		for _, arc := range volume.Arcs {
			count := len(arc.Chapters)
			if count == 0 {
				count = arc.EstimatedChapters
			}
			from := chapter
			to := chapter + max(count-1, 0)
			if volume.Index == volumeNumber && arc.Index == arcNumber && count > 0 {
				return volume, arc, from, to, true
			}
			chapter += count
		}
	}
	return domain.VolumeOutline{}, domain.ArcOutline{}, 0, 0, false
}

func selectSkeletonVolumeAudit(st *store.Store, volume int) *domain.OriginalPlanningAudit {
	if st == nil || st.OriginalPlanningAudits == nil {
		return nil
	}
	audits, err := st.OriginalPlanningAudits.Load()
	if err != nil {
		return nil
	}
	for index := len(audits) - 1; index >= 0; index-- {
		audit := audits[index]
		if audit.Scope == "skeleton_volume" && audit.Volume == volume && audit.Verdict == "pass" {
			return &audit
		}
	}
	return nil
}

func selectPassedPriorArcAudits(
	st *store.Store,
	volume, targetArc int,
) []map[string]any {
	if st == nil || st.OriginalPlanningAudits == nil {
		return nil
	}
	audits, err := st.OriginalPlanningAudits.Load()
	if err != nil {
		return nil
	}
	latest := make(map[int]domain.OriginalPlanningAudit)
	for _, audit := range audits {
		if audit.Scope == "arc" && audit.Volume == volume &&
			audit.Arc > 0 && audit.Arc < targetArc && audit.Verdict == "pass" {
			latest[audit.Arc] = audit
		}
	}
	out := make([]map[string]any, 0, len(latest))
	for arc := 1; arc < targetArc; arc++ {
		if audit, ok := latest[arc]; ok {
			out = append(out, compactPlanningDetailAudit(audit))
		}
	}
	return out
}

func compactPlanningDetailAudit(audit domain.OriginalPlanningAudit) map[string]any {
	dimensions := make([]map[string]any, 0, len(audit.Dimensions))
	for _, dimension := range audit.Dimensions {
		dimensions = append(dimensions, map[string]any{
			"name":    dimension.Name,
			"score":   dimension.Score,
			"comment": truncateRunes(dimension.Comment, 40),
		})
	}
	issues := make([]map[string]any, 0, len(audit.Issues))
	for _, issue := range audit.Issues {
		issues = append(issues, map[string]any{
			"severity":           issue.Severity,
			"arc":                issue.Arc,
			"from_chapter":       issue.FromChapter,
			"to_chapter":         issue.ToChapter,
			"description":        truncateRunes(issue.Description, 240),
			"repair_instruction": truncateRunes(issue.RepairInstruction, 300),
		})
	}
	return map[string]any{
		"scope":      audit.Scope,
		"volume":     audit.Volume,
		"verdict":    audit.Verdict,
		"summary":    truncateRunes(audit.Summary, 600),
		"dimensions": dimensions,
		"issues":     issues,
	}
}

func compactSkeletonAuditIndex(
	audits []domain.OriginalPlanningAudit,
	fromVolume, toVolume int,
) []map[string]any {
	selected := make([]domain.OriginalPlanningAudit, 0, len(audits))
	for _, audit := range audits {
		if audit.Verdict != "pass" {
			continue
		}
		if fromVolume > 0 {
			if (audit.Scope == "skeleton_volume" ||
				audit.Scope == "arc" ||
				audit.Scope == "volume") &&
				audit.Volume >= fromVolume && audit.Volume <= toVolume {
				selected = append(selected, audit)
			}
			continue
		}
		if audit.Scope == "skeleton_book_batch" || audit.Scope == "book_batch" {
			selected = append(selected, audit)
		}
	}

	index := make([]map[string]any, 0, len(selected))
	for position, audit := range selected {
		entry := map[string]any{
			"scope":     audit.Scope,
			"verdict":   audit.Verdict,
			"min_score": minimumPlanningAuditScore(audit.Dimensions),
		}
		if audit.Volume > 0 {
			entry["volume"] = audit.Volume
		}
		if audit.Arc > 0 {
			entry["arc"] = audit.Arc
		}
		if audit.FromVolume > 0 {
			entry["from_volume"] = audit.FromVolume
			entry["to_volume"] = audit.ToVolume
		}
		// Small books retain every editorial summary. Huge books retain all
		// verdict/range/score facts, plus representative summaries across the
		// sequence, so global continuity remains visible without linear prose
		// growth from every prior audit.
		if len(selected) <= 12 ||
			position < 3 ||
			position >= len(selected)-3 ||
			position%max(1, len(selected)/6) == 0 {
			entry["summary"] = truncateRunes(audit.Summary, 240)
		}
		index = append(index, entry)
	}
	return index
}

func minimumPlanningAuditScore(dimensions []domain.OriginalPlanningAuditDimension) float64 {
	if len(dimensions) == 0 {
		return 0
	}
	minimum := dimensions[0].Score
	for _, dimension := range dimensions[1:] {
		if dimension.Score < minimum {
			minimum = dimension.Score
		}
	}
	return minimum
}

func planningReviewCharacterFocus(
	characters []domain.Character,
	planning map[string]any,
) map[string]bool {
	focused := make(map[string]bool)
	text := ""
	if planning != nil {
		text = fmt.Sprint(planning["layered_outline"])
	}
	for _, character := range characters {
		if strings.Contains(text, character.Name) ||
			character.Tier == "core" ||
			character.Tier == "protagonist" ||
			len(characters) <= 8 {
			focused[character.ID] = true
		}
	}
	for _, character := range characters {
		if len(focused) >= 3 {
			break
		}
		focused[character.ID] = true
	}
	return focused
}

func (t *ContextTool) compactArchitectSimulationProfile(profile *domain.SimulationProfile) *domain.SimulationCompactProfile {
	compact := t.compactSimulationProfile(profile)
	if compact == nil {
		return nil
	}
	// Planning needs structural imitation only. Prose lexicon, sentence-level
	// style, pacing and non-Architect role instructions belong to chapter work.
	return &domain.SimulationCompactProfile{
		Version:     compact.Version,
		Mode:        compact.Mode,
		SourceCount: compact.SourceCount,
		PlotDesign: domain.SimulationPlotDesign{
			OpeningPatterns:      compactStringList(compact.PlotDesign.OpeningPatterns, 1, 60),
			EscalationPatterns:   compactStringList(compact.PlotDesign.EscalationPatterns, 1, 60),
			TurningPointPatterns: compactStringList(compact.PlotDesign.TurningPointPatterns, 1, 60),
			PayoffPatterns:       compactStringList(compact.PlotDesign.PayoffPatterns, 1, 60),
		},
		HookDesign: domain.SimulationHookDesign{
			HookTypes:           compactStringList(compact.HookDesign.HookTypes, 1, 60),
			Placement:           compactStringList(compact.HookDesign.Placement, 1, 60),
			CliffhangerPatterns: compactStringList(compact.HookDesign.CliffhangerPatterns, 1, 60),
			PayoffRules:         compactStringList(compact.HookDesign.PayoffRules, 1, 60),
		},
		ReaderEngagement: domain.SimulationReaderEngagement{
			Methods:            compactStringList(compact.ReaderEngagement.Methods, 1, 60),
			EmotionalDrivers:   compactStringList(compact.ReaderEngagement.EmotionalDrivers, 1, 60),
			ProgressionRewards: compactStringList(compact.ReaderEngagement.ProgressionRewards, 1, 60),
			AntiPatterns:       compactStringList(compact.ReaderEngagement.AntiPatterns, 1, 60),
		},
		RoleGuidance: domain.SimulationRoleGuidance{
			Architect: compactStringList(compact.RoleGuidance.Architect, 1, 60),
		},
	}
}

func (t *ContextTool) compactSimulationProfile(profile *domain.SimulationProfile) *domain.SimulationCompactProfile {
	if t.simulationMode == contextSimulationModeReinforced {
		return domain.CompactSimulationProfileForMode(profile, contextSimulationModeReinforced)
	}
	return domain.CompactSimulationProfile(profile)
}

func (t *ContextTool) buildAdaptationChapterContext(result map[string]any, chapter int, warn func(string, error)) {
	plan, err := t.store.Adaptation.LoadPlan()
	warn("adaptation_plan", err)
	if err != nil || plan == nil {
		return
	}
	chapterPlan, ok := findAdaptationChapterPlan(plan, chapter)
	if !ok {
		return
	}
	working, ok := result["working_memory"].(map[string]any)
	if !ok {
		working = map[string]any{}
		result["working_memory"] = working
	}
	result["adaptation_mode"] = true
	actualRunes := 0
	if _, words, draftErr := t.store.Drafts.LoadChapterContent(chapter); draftErr == nil {
		actualRunes = words
	} else {
		warn("adaptation_draft_words", draftErr)
	}
	working["adaptation"] = compactAdaptationPlanSummary(plan)
	working["adaptation_effective_mode"] = buildAdaptationEffectiveMode(plan, chapterPlan)
	working["adaptation_contract"] = compactAdaptationChapterPlan(chapterPlan)
	working["adaptation_word_contract"] = buildAdaptationWordContract(t.store, plan, chapterPlan, chapter, actualRunes)
	working["adaptation_source_coverage"] = map[string]any{
		"chapter":         chapterPlan.Chapter,
		"source_chapters": chapterPlan.SourceChapters,
		"source_range":    chapterPlan.SourceRange,
		"source_segments": chapterPlan.SourceSegments,
		"event_ids":       chapterPlan.EventIDs,
		"is_added":        chapterPlan.IsAdded,
		"coverage_note":   chapterPlan.CoverageNote,
		"source_role":     adaptationSourceRoleForGranularity(plan.Granularity),
	}
	rules := plan.Rules
	if len(rules) == 0 && strings.TrimSpace(plan.Brief) != "" {
		rules = domain.CompileAdaptationRules(plan.Brief, plan.Granularity)
	}
	if activeRules := domain.ApplicableAdaptationRules(rules, plan.Granularity, chapter); len(activeRules) > 0 {
		working["adaptation_active_rules"] = compactActiveAdaptationRules(activeRules)
	}

	reports, reportErr := t.store.Adaptation.LoadSourceReports()
	warn("adaptation_source_reports", reportErr)
	if reportErr == nil && len(reports) > 0 {
		working["source_ref_reports"] = compactSourceReportsForContext(reports, chapterPlan.SourceChapters)
	}
}

func compactActiveAdaptationRules(rules []domain.AdaptationRule) []map[string]any {
	const (
		maxRuleTextRunes = 300
	)
	selected := domain.SelectAdaptationPromptRules(rules, domain.AdaptationPromptMaxRules, domain.AdaptationPromptMaxForbiddenRules)
	out := make([]map[string]any, 0, len(selected))
	for _, rule := range selected {
		text := strings.TrimSpace(rule.Text)
		payload := map[string]any{
			"rule_id": rule.ID,
			"kind":    rule.Kind,
			"text":    truncateRunes(text, maxRuleTextRunes),
		}
		if len([]rune(text)) > maxRuleTextRunes {
			payload["truncated"] = true
		}
		out = append(out, payload)
	}
	return out
}

func (t *ContextTool) buildAdaptationPlanningContext(result map[string]any, warn func(string, error)) {
	plan, err := t.store.Adaptation.LoadPlan()
	warn("adaptation_plan", err)
	if err != nil || plan == nil {
		return
	}
	section, ok := result["planning_memory"].(map[string]any)
	if !ok {
		section = map[string]any{}
		result["planning_memory"] = section
	}
	result["adaptation_mode"] = true
	summary := compactAdaptationPlanSummary(plan)
	summary["target_chapters"] = len(plan.Chapters)
	if manifest, manifestErr := t.store.Adaptation.LoadSourceManifest(); manifestErr == nil && manifest != nil {
		summary["source_path"] = manifest.SourcePath
		summary["source_chapters"] = manifest.ChapterCount
	} else {
		warn("adaptation_source_manifest", manifestErr)
	}
	section["adaptation_plan"] = summary
}

func buildAdaptationEffectiveMode(plan *domain.AdaptationPlan, chapterPlan domain.AdaptationChapterPlan) map[string]any {
	if plan == nil {
		return nil
	}
	granularity := domain.NormalizeAdaptationGranularity(plan.Granularity)
	rewritePolicy := domain.AdaptationRewritePolicyForGranularity(granularity)
	mode := map[string]any{
		"granularity":                  granularity,
		"rewrite_policy":               rewritePolicy,
		"mode_contract":                granularity + "/" + rewritePolicy,
		"current_mode_only":            true,
		"preserve_details_applicable":  granularity == domain.AdaptationGranularityChapter && rewritePolicy == domain.AdaptationRewritePreserveDetails,
		"source_chapters":              chapterPlan.SourceChapters,
		"source_range":                 chapterPlan.SourceRange,
		"source_reference_policy":      adaptationSourceReferencePolicy(granularity),
		"source_mapping_meaning":       adaptationSourceMappingMeaning(granularity),
		"source_read_instruction":      adaptationSourceReadInstruction(granularity),
		"writer_instruction":           adaptationModeWriterInstruction(granularity),
		"budget_instruction":           adaptationBudgetWriterInstruction(granularity),
		"legacy_rewrite_policy_notice": adaptationLegacyRewritePolicyNotice(plan.Brief),
	}
	switch granularity {
	case domain.AdaptationGranularityFree:
		mode["must_not"] = []string{
			"不要把 source_chapters/source_range 理解为本章对应原著第几章",
			"不要把本章称为 preserve_details 策略",
			"不要因为存在 source refs 就反复读取原文章节",
			"不要让原著旧结局覆盖已经确认的新提案、新大纲和已写剧情",
		}
	case domain.AdaptationGranularityArc:
		mode["must_not"] = []string{
			"不要把 source_chapters/source_range 理解为逐字复用许可",
			"不要把 full_rewrite 写成 preserve_details",
			"不要搬运原文段落",
		}
	default:
		mode["must_not"] = []string{
			"不要只写改动片段",
			"不要把改编内容写成提示性括注或补丁标签",
		}
	}
	return mode
}

func adaptationSourceRoleForGranularity(granularity string) string {
	switch domain.NormalizeAdaptationGranularity(granularity) {
	case domain.AdaptationGranularityFree:
		return "background_anchor_only"
	case domain.AdaptationGranularityArc:
		return "mainline_anchor"
	default:
		return "ordered_source_segment_ownership"
	}
}

func adaptationSourceReferencePolicy(granularity string) string {
	switch domain.NormalizeAdaptationGranularity(granularity) {
	case domain.AdaptationGranularityFree:
		return "optional_background_anchor"
	case domain.AdaptationGranularityArc:
		return "mainline_anchor"
	default:
		return "required_source_segment"
	}
}

func adaptationSourceMappingMeaning(granularity string) string {
	switch domain.NormalizeAdaptationGranularity(granularity) {
	case domain.AdaptationGranularityFree:
		return "后台覆盖率与必要事实锚点；不表示目标章对应原著章节"
	case domain.AdaptationGranularityArc:
		return "主线与卷弧覆盖锚点；不要求目标章与原文章节一一对应"
	default:
		return "短来源章通常对应一个目标章；长来源章由多个有序 SourceSegment 连续承接"
	}
}

func adaptationSourceReadInstruction(granularity string) string {
	switch domain.NormalizeAdaptationGranularity(granularity) {
	case domain.AdaptationGranularityFree:
		return "不要因为 source_chapters/source_range 存在就读取原文；只有缺少必要事实时才按需读取一次 source anchor"
	case domain.AdaptationGranularityArc:
		return "必要时读取 source anchors 核对主线因果；读取后仍必须写 full_rewrite 原创正文"
	default:
		return "写作前按 source_chapters 读取原文并对照事实"
	}
}

func adaptationModeWriterInstruction(granularity string) string {
	switch domain.NormalizeAdaptationGranularity(granularity) {
	case domain.AdaptationGranularityFree:
		return "当前章按 free/full_rewrite 写作：以改编提案、章节细纲和已写新剧情为准，source refs 只是背景锚点"
	case domain.AdaptationGranularityArc:
		return "当前章按 arc/full_rewrite 写作：保留主线因果与弧线功能，用新章节组织写原创正文"
	default:
		return "当前章按 chapter/preserve_details 写作：逐章对照原文，未受影响内容可承接，受影响完整场景单元原创重写"
	}
}

func adaptationBudgetWriterInstruction(granularity string) string {
	if domain.NormalizeAdaptationGranularity(granularity) == domain.AdaptationGranularityChapter {
		return "当前章按 preserve_details 执行字数硬契约；超出或不足硬区间先修正文，再重新检查。"
	}
	return "当前章按 full_rewrite 执行：word_budget 是提案规划参考而非正文硬上限；完整正文只要质量和契约通过，适度超过 max_runes 可以保留并提交，不要仅为压回预估值而重写。只有明显超过 soft_max_runes 才报告预算规划异常，后续只重规划预算，不改剧情。"
}

func adaptationLegacyRewritePolicyNotice(brief string) string {
	if strings.Contains(brief, "rewrite_policy_rule=") {
		return "brief 中的 rewrite_policy_rule 是历史模式映射说明；当前章只执行 adaptation_effective_mode.mode_contract"
	}
	return ""
}

func adaptationWordToleranceForContext(plan *domain.AdaptationPlan) any {
	if plan == nil || domain.AdaptationRewritePolicyForGranularity(plan.Granularity) != domain.AdaptationRewritePreserveDetails {
		return "disabled"
	}
	return plan.WordTolerance
}

func selectSourceReports(reports []domain.AdaptationSourceReport, refs []int) []domain.AdaptationSourceReport {
	if len(reports) == 0 || len(refs) == 0 {
		return nil
	}
	want := make(map[int]struct{}, len(refs))
	for _, ref := range refs {
		want[ref] = struct{}{}
	}
	var selected []domain.AdaptationSourceReport
	for _, report := range reports {
		if _, ok := want[report.Chapter]; ok {
			selected = append(selected, report)
		}
	}
	return selected
}

func (t *ContextTool) buildBaseContext(result map[string]any, state contextBuildState, warn func(string, error)) {
	longform := state.usesWindowedOutline()
	t.buildPremiseContext(result, longform, warn)
	t.buildOutlineContext(result, state, longform, warn)
	t.buildWorldRuleContext(result, longform, warn)
}

func (t *ContextTool) buildPremiseContext(result map[string]any, compact bool, warn func(string, error)) {
	premise, err := t.store.Outline.LoadPremise()
	if err != nil || premise == "" {
		warn("premise", err)
		return
	}
	if !compact {
		result["premise"] = truncateRunes(premise, 5000)
	}
	if sections := parsePremiseSections(premise); len(sections) > 0 {
		if compact {
			result["premise_sections"] = compactPremiseSections(sections, 600)
		} else {
			result["premise_sections"] = compactPremiseSections(sections, 1200)
		}
	}
	tier := domain.PlanningTier("")
	if meta, err := t.store.RunMeta.Load(); err == nil && meta != nil {
		tier = meta.PlanningTier
	}
	result["premise_structure"] = premiseStructure(premise, tier)
}

func (t *ContextTool) buildOutlineContext(result map[string]any, state contextBuildState, windowed bool, warn func(string, error)) {
	if !windowed {
		if outline, err := t.store.Outline.LoadOutline(); err == nil && outline != nil {
			result["outline"] = outline
		} else {
			warn("outline", err)
		}
		return
	}
	outline, err := t.loadCanonicalOutline()
	if err != nil {
		warn("outline", err)
		return
	}
	if len(outline) == 0 {
		return
	}
	from := max(state.chapter-nearbyOutlineBeforeChapters, 1)
	to := min(state.chapter+nearbyOutlineAfterChapters, len(outline))
	nearby := compactOutlineEntries(outlineEntriesInRange(outline, from, to))
	if len(nearby) > 0 {
		result["nearby_outline"] = nearby
	}
	result["outline_scope"] = map[string]any{
		"mode":                 "windowed",
		"chapter":              state.chapter,
		"from":                 from,
		"to":                   to,
		"total_chapters":       len(outline),
		"full_outline_omitted": true,
	}
	t.attachCurrentArcOutline(result, state, outline)
}

func (t *ContextTool) buildWorldRuleContext(result map[string]any, compact bool, warn func(string, error)) {
	rules, err := t.store.World.LoadWorldRules()
	if err != nil || len(rules) == 0 {
		warn("world_rules", err)
		return
	}
	result["world_rules"] = compactWorldRules(rules, 40)
}

func (state contextBuildState) usesWindowedOutline() bool {
	if state.progress != nil && state.progress.TotalChapters > 50 {
		return true
	}
	return state.profile.Layered
}

func compactPremiseSections(sections map[string]string, maxRunes int) map[string]string {
	if len(sections) == 0 {
		return nil
	}
	out := make(map[string]string, len(sections))
	for key, value := range sections {
		out[key] = truncateRunes(value, maxRunes)
	}
	return out
}

func compactWorldRules(rules []domain.WorldRule, maxRules int) []domain.WorldRule {
	if len(rules) == 0 || maxRules <= 0 {
		return nil
	}
	limit := min(len(rules), maxRules)
	out := make([]domain.WorldRule, 0, limit)
	for _, rule := range rules[:limit] {
		rule.Rule = truncateRunes(rule.Rule, 75)
		rule.Boundary = truncateRunes(rule.Boundary, 20)
		out = append(out, rule)
	}
	return out
}

func (t *ContextTool) loadCanonicalOutline() ([]domain.OutlineEntry, error) {
	flat, flatErr := t.store.Outline.LoadOutline()
	if flatErr != nil {
		return nil, flatErr
	}
	if len(flat) > 0 {
		return normalizeOutlineEntries(flat), nil
	}
	layered, layeredErr := t.store.Outline.LoadLayeredOutline()
	if layeredErr != nil {
		return nil, layeredErr
	}
	return domain.FlattenOutline(layered), nil
}

func normalizeOutlineEntries(entries []domain.OutlineEntry) []domain.OutlineEntry {
	out := make([]domain.OutlineEntry, len(entries))
	for i, entry := range entries {
		if entry.Chapter <= 0 {
			entry.Chapter = i + 1
		}
		out[i] = entry
	}
	return out
}

func outlineEntriesInRange(entries []domain.OutlineEntry, from, to int) []domain.OutlineEntry {
	if from > to || len(entries) == 0 {
		return nil
	}
	out := make([]domain.OutlineEntry, 0, to-from+1)
	for _, entry := range entries {
		if entry.Chapter >= from && entry.Chapter <= to {
			out = append(out, entry)
		}
	}
	return out
}

func (t *ContextTool) buildOutlineRangeContext(result map[string]any, from, to int, warn func(string, error)) error {
	if from <= 0 || to <= 0 {
		return fmt.Errorf("outline_range requires positive from and to")
	}
	if from > to {
		from, to = to, from
	}
	if to-from+1 > maxOutlineRangeChapters {
		to = from + maxOutlineRangeChapters - 1
	}
	outline, err := t.loadCanonicalOutline()
	if err != nil {
		warn("outline", err)
		return nil
	}
	if len(outline) == 0 {
		result["outline"] = []domain.OutlineEntry{}
		result["outline_scope"] = map[string]any{
			"mode":           "outline_range",
			"from":           from,
			"to":             to,
			"total_chapters": 0,
		}
		return nil
	}
	if from > len(outline) {
		from = len(outline)
	}
	if to > len(outline) {
		to = len(outline)
	}
	entries := compactOutlineEntries(outlineEntriesInRange(outline, from, to))
	result["outline"] = entries
	result["outline_scope"] = map[string]any{
		"mode":                 "outline_range",
		"from":                 from,
		"to":                   to,
		"returned_chapters":    len(entries),
		"total_chapters":       len(outline),
		"full_outline_omitted": len(entries) < len(outline),
	}
	return nil
}

func (t *ContextTool) attachCurrentArcOutline(result map[string]any, state contextBuildState, fallback []domain.OutlineEntry) {
	volumes, err := t.store.Outline.LoadLayeredOutline()
	if err != nil || len(volumes) == 0 {
		attachFlatArcCompact(result, state, fallback)
		return
	}

	chapter := 1
	for _, volume := range volumes {
		for _, arc := range volume.Arcs {
			arcStart := chapter
			chapters := make([]domain.OutlineEntry, 0, len(arc.Chapters))
			for _, entry := range arc.Chapters {
				entry.Chapter = chapter
				chapters = append(chapters, entry)
				chapter++
			}
			arcEnd := chapter - 1
			if state.chapter < arcStart || state.chapter > arcEnd {
				continue
			}
			payload := map[string]any{
				"volume":       volume.Index,
				"volume_title": volume.Title,
				"arc":          arc.Index,
				"arc_title":    arc.Title,
				"arc_goal":     arc.Goal,
				"from":         arcStart,
				"to":           arcEnd,
			}
			from := max(state.chapter-nearbyOutlineBeforeChapters, arcStart)
			to := min(state.chapter+nearbyOutlineAfterChapters, arcEnd)
			payload["chapters"] = compactOutlineEntries(outlineEntriesInRange(chapters, from, to))
			payload["total_arc_chapters"] = len(chapters)
			payload["compacted"] = true
			result["arc_outline_compact"] = payload
			return
		}
	}
	attachFlatArcCompact(result, state, fallback)
}

func attachFlatArcCompact(result map[string]any, state contextBuildState, outline []domain.OutlineEntry) {
	if len(outline) == 0 {
		return
	}
	from := max(state.chapter-nearbyOutlineBeforeChapters, 1)
	to := min(state.chapter+nearbyOutlineAfterChapters, len(outline))
	result["arc_outline_compact"] = map[string]any{
		"mode":           "flat",
		"from":           from,
		"to":             to,
		"total_chapters": len(outline),
		"chapters":       compactOutlineEntries(outlineEntriesInRange(outline, from, to)),
	}
}

func (t *ContextTool) prepareChapterContext(chapter int, envelope *chapterContextEnvelope, warn func(string, error)) contextBuildState {
	state := contextBuildState{
		chapter: chapter,
		profile: domain.NewContextProfile(0),
	}

	progress, err := t.store.Progress.Load()
	warn("progress", err)
	runMeta, err := t.store.RunMeta.Load()
	warn("run_meta", err)
	state.progress = progress
	state.runMeta = runMeta
	state.purpose = resolveChapterContextPurpose(progress, chapter)
	_, draftWords, draftErr := t.store.Drafts.LoadChapterContent(chapter)
	if draftErr != nil {
		warn("chapter_draft", draftErr)
	}
	if state.purpose == chapterContextWriting && draftWords > 0 {
		// A restarted normal-writing turn with stored prose is no longer in the
		// source-gathering phase. Its full draft is loaded separately through
		// read_chapter, so the context package must become a validation/repair
		// contract rather than repeat all drafting inputs beside that prose.
		state.purpose = chapterContextRecovering
	}

	if runMeta != nil && runMeta.PlanningTier != "" {
		envelope.Episodic["planning_tier"] = runMeta.PlanningTier
	}
	if progress != nil && progress.TotalChapters > 0 {
		state.profile = domain.NewContextProfile(progress.TotalChapters)
	}
	if progress == nil || !progress.Layered {
		state.profile.Layered = false
	}

	currentEntry, currentEntryErr := t.store.Outline.GetChapterOutline(chapter)
	if currentEntryErr == nil {
		envelope.Working["current_chapter_outline"] = compactOutlineEntry(*currentEntry)
	} else {
		warn("current_chapter_outline", currentEntryErr)
	}
	state.currentEntry = currentEntry
	if chapterPurposeNeedsDraftingContext(state.purpose) {
		t.attachFutureChapterPromises(envelope, chapter, warn)
	}

	chapterPlan, chapterPlanErr := t.store.Drafts.LoadChapterPlan(chapter)
	if chapterPlanErr == nil && chapterPlan != nil {
		compactPlan := compactChapterPlan(*chapterPlan)
		if len(chapterPlan.Contract.RequiredBeats) > 0 ||
			len(chapterPlan.Contract.ForbiddenMoves) > 0 ||
			len(chapterPlan.Contract.ContinuityChecks) > 0 ||
			len(chapterPlan.Contract.EvaluationFocus) > 0 ||
			chapterPlan.Contract.EmotionTarget != "" ||
			len(chapterPlan.Contract.PayoffPoints) > 0 ||
			chapterPlan.Contract.HookGoal != "" {
			envelope.Working["chapter_contract"] = compactChapterContract(chapterPlan.Contract)
			// Contract has a dedicated canonical field. Do not serialize it again
			// inside chapter_plan.
			compactPlan.Contract = domain.ChapterContract{}
		}
		if chapterPurposeNeedsDraftingContext(state.purpose) {
			envelope.Working["chapter_plan"] = compactPlan
		}
	} else {
		warn("chapter_plan", chapterPlanErr)
	}
	state.chapterPlan = chapterPlan

	// 是否正在重写本章：决定 novel_context 是否补"重写专用"事实。
	isRewrite := progress != nil && slices.Contains(progress.PendingRewrites, chapter)

	// 暴露 draft 是否已存在的事实：让 writer 被重派时能自行判断跳过重写还是覆盖。
	// 只暴露 exists + word_count，不注入正文（正文让 writer 按需用 read_chapter 拉）。
	if draftWords > 0 {
		envelope.Working["chapter_draft"] = map[string]any{
			"exists":     true,
			"word_count": draftWords,
		}
	}

	// 重写时把"为什么改 + 改哪里"交给 writer：理由来自返工队列，具体批评来自本章评审
	// （selectReviewLessons 只召回 chapter-1..chapter-3，恰好漏掉本章本身，writer 又无读评审的工具）。
	// 正文不在此注入——保持"正文按需 read_chapter 拉"的约定不破。
	if isRewrite {
		brief := map[string]any{"reason": progress.RewriteReason}
		if review, reviewErr := t.store.World.LoadReview(chapter); reviewErr == nil && review != nil {
			if review.Summary != "" {
				brief["review_summary"] = truncateRunes(review.Summary, maxContextSummaryRunes)
			}
			if len(review.Issues) > 0 {
				brief["issues"] = compactReviewIssues(review.Issues, 5)
			}
			if len(review.ContractMisses) > 0 {
				brief["contract_misses"] = compactStringList(review.ContractMisses, maxContextContractItems, maxContextContractItemRunes)
			}
		} else if reviewErr != nil {
			warn("rewrite_review", reviewErr)
		}
		envelope.Working["rewrite_brief"] = brief
	}

	foreshadow, foreshadowErr := t.store.World.LoadActiveForeshadow()
	warn("foreshadow_ledger", foreshadowErr)
	state.foreshadow = foreshadow

	relationships, relErr := t.store.World.LoadRelationships()
	warn("relationship_state", relErr)
	if len(relationships) > 0 {
		envelope.Episodic["relationship_state"] = compactRelationshipEntries(relationships, chapter, 12)
	}
	state.relationships = relationships

	allStateChanges, scErr := t.store.World.LoadStateChanges()
	warn("recent_state_changes", scErr)
	state.allStateChanges = allStateChanges
	if len(allStateChanges) > 0 {
		start := max(chapter-2, 1)
		var recent []domain.StateChange
		for _, c := range allStateChanges {
			if c.Chapter >= start && c.Chapter < chapter {
				recent = append(recent, c)
			}
		}
		if len(recent) > 0 {
			envelope.Episodic["recent_state_changes"] = compactStateChanges(recent, 12)
		}
	}

	styleRules, styleErr := t.store.World.LoadStyleRules()
	warn("style_rules", styleErr)
	state.styleRules = styleRules
	state.storyThreads = t.selectStoryThreads(state)
	if len(state.storyThreads) > 0 && len(state.storyThreads) < storyThreadRecallMinSelected {
		state.storyThreads = nil
	}

	return state
}

const futureChapterPromiseWindow = 2

// attachFutureChapterPromises exposes only the next few story promises. This
// gives Writer and Editor an explicit ownership boundary without loading the
// full outline or forbidding legitimate foreshadowing.
func (t *ContextTool) attachFutureChapterPromises(envelope *chapterContextEnvelope, chapter int, warn func(string, error)) {
	if envelope == nil || chapter <= 0 {
		return
	}
	promises := make([]map[string]any, 0, futureChapterPromiseWindow)
	for next := chapter + 1; next <= chapter+futureChapterPromiseWindow; next++ {
		entry, err := t.store.Outline.GetChapterOutline(next)
		if err != nil {
			if next == chapter+1 {
				warn("future_chapter_promises", err)
			}
			break
		}
		compact := compactOutlineEntry(*entry)
		promises = append(promises, map[string]any{
			"chapter":    entry.Chapter,
			"title":      strings.TrimSpace(compact.Title),
			"core_event": strings.TrimSpace(compact.CoreEvent),
			"hook":       strings.TrimSpace(compact.Hook),
		})
	}
	if len(promises) > 0 {
		envelope.Working["future_chapter_promises"] = promises
	}
}

func (t *ContextTool) buildChapterContext(result map[string]any, state contextBuildState, warn func(string, error)) {
	envelope := newChapterContextEnvelope()
	result["memory_policy"] = domain.NewChapterMemoryPolicy(state.progress, state.profile, state.currentEntry != nil)

	if state.purpose == chapterContextPolishing {
		t.loadPolishingCharacters(envelope.Episodic, warn)
	} else if state.purpose != chapterContextRecovering && state.profile.Layered {
		t.loadLayeredCharacters(envelope.Episodic, state.chapter, warn)
	} else if state.purpose != chapterContextRecovering {
		t.loadFilteredCharacters(envelope.Episodic, state.chapter, warn)
	}

	if chapterPurposeNeedsDraftingContext(state.purpose) {
		t.buildChapterEpisodicMemory(&envelope, state, warn)
		t.buildChapterWorkingMemory(&envelope, state, warn)
	}
	t.buildChapterReferencePack(&envelope, state)
	if chapterPurposeNeedsDraftingContext(state.purpose) {
		t.buildChapterSelectedMemory(&envelope, state, warn)
	}
	if state.purpose != chapterContextRecovering {
		t.buildStyleStats(&envelope, state)
	}
	envelope.apply(result)
}

func (t *ContextTool) buildChapterSimulationProfile(result map[string]any, purpose chapterContextPurpose, warn func(string, error)) {
	profile, err := t.store.Simulation.Load()
	if err != nil {
		warn("simulation_profile", err)
		return
	}
	compact := t.compactSimulationProfile(profile)
	if compact == nil {
		return
	}

	// Writer preserves prose voice, sentence density and Writer-specific
	// guidance. Plot, hook and reader-retention design are owned by the signed
	// chapter contract and are not duplicated from the simulation profile.
	chapterProfile := &domain.SimulationCompactProfile{
		Version:       compact.Version,
		Mode:          compact.Mode,
		SourceCount:   compact.SourceCount,
		Style:         compact.Style,
		PacingDensity: compact.PacingDensity,
		RoleGuidance: domain.SimulationRoleGuidance{
			Writer: compact.RoleGuidance.Writer,
		},
	}
	// One representative signal from each style/pacing category is enough at
	// chapter execution time: the signed chapter contract owns plot and hook
	// design, while deterministic validators own exact prose findings.
	itemLimit := 1
	chapterProfile.Style = compactPolishingSimulationStyle(chapterProfile.Style, itemLimit)
	chapterProfile.PacingDensity = compactPolishingSimulationPacing(chapterProfile.PacingDensity, itemLimit)
	chapterProfile.RoleGuidance.Writer = compactStringList(chapterProfile.RoleGuidance.Writer, itemLimit, 60)
	working, ok := result["working_memory"].(map[string]any)
	if !ok {
		working = map[string]any{}
		result["working_memory"] = working
	}
	working["simulation_profile"] = chapterProfile
	result["simulation_profile"] = true
	if t.simulationMode == contextSimulationModeReinforced {
		result["simulation_mode"] = contextSimulationModeReinforced
	}
}

func compactPolishingSimulationStyle(style domain.SimulationStyle, maxItems int) domain.SimulationStyle {
	// Corpus viewpoint ownership is a source-story fact, not a reusable prose
	// technique. The current chapter outline owns POV, so do not let a male- or
	// female-led imitation corpus override the planned focal character.
	style.NarrativeVoice = nil
	style.SentenceRhythm = compactStringList(style.SentenceRhythm, maxItems, 60)
	style.ProseTexture = compactStringList(style.ProseTexture, maxItems, 60)
	style.Perspective = nil
	style.Mood = compactStringList(style.Mood, maxItems, 60)
	style.DoNotCopy = compactStringList(style.DoNotCopy, maxItems, 60)
	return style
}

func compactPolishingSimulationPacing(pacing domain.SimulationPacingDensity, maxItems int) domain.SimulationPacingDensity {
	pacing.SceneDensity = compactStringList(pacing.SceneDensity, maxItems, 60)
	pacing.InformationRelease = compactStringList(pacing.InformationRelease, maxItems, 60)
	pacing.DialogueActionRatio = compactStringList(pacing.DialogueActionRatio, maxItems, 60)
	pacing.CompressionRules = compactStringList(pacing.CompressionRules, maxItems, 60)
	return pacing
}

func (t *ContextTool) loadPolishingCharacters(result map[string]any, warn func(string, error)) {
	snapshots, err := t.store.Characters.LoadLatestSnapshots()
	if err != nil {
		warn("character_snapshots", err)
		return
	}
	if len(snapshots) > 0 {
		result["character_snapshots"] = compactCharacterSnapshots(snapshots, 8)
	}
}

// buildStyleStats 对全部已完成章节做全书级风格统计，注入 episodic_memory.style_stats。
// 弧内评审窗口对"章均几十次的句式 tic、章末形态同构、跨章复读"天然失明，只有
// 全书统计能暴露——统计归代码（确定性），裁定归 LLM（editor 在 aesthetic 维度
// 按数字判分，writer 据此自避免）。章数不足时 stylestat 返回 nil，不注入。
func (t *ContextTool) buildStyleStats(envelope *chapterContextEnvelope, state contextBuildState) {
	if state.progress == nil || len(state.progress.CompletedChapters) == 0 {
		return
	}
	completed := slices.Clone(state.progress.CompletedChapters)
	slices.Sort(completed)
	chapters := make([]string, 0, len(completed))
	for _, ch := range completed {
		// 个别章读取失败跳过：统计是 best-effort 事实，不因单章缺失放弃全书视野
		if text, err := t.store.Drafts.LoadChapterText(ch); err == nil && text != "" {
			chapters = append(chapters, text)
		}
	}

	var titles []string
	if outline, err := t.store.Outline.LoadOutline(); err == nil {
		for _, entry := range outline {
			titles = append(titles, entry.Title)
		}
	}

	stats := stylestat.Compute(stylestat.Input{
		Chapters:  chapters,
		Titles:    titles,
		Stopwords: t.styleStopwords(),
	})
	if stats == nil {
		return
	}
	envelope.Episodic["style_stats"] = stats
}

// styleStopwords 收集角色名与别名供短语挖掘过滤——出场人名天然高频，不是文风问题。
func (t *ContextTool) styleStopwords() []string {
	var words []string
	if chars, err := t.store.Characters.Load(); err == nil {
		for _, c := range chars {
			words = append(words, c.Name)
			words = append(words, c.Aliases...)
		}
	}
	if cast, err := t.store.Cast.RecentActive(50); err == nil {
		for _, e := range cast {
			words = append(words, e.Name)
			words = append(words, e.Aliases...)
		}
	}
	return words
}

func (t *ContextTool) buildChapterWorkingMemory(envelope *chapterContextEnvelope, state contextBuildState, warn func(string, error)) {
	// The current contract and nearby chapter outlines already establish the
	// structural position. Recent chapter summaries provide continuity without
	// also loading redundant volume and arc summaries.
	if summaries, err := t.store.Summaries.LoadRecentSummaries(state.chapter, min(state.profile.SummaryWindow, 4)); err == nil && len(summaries) > 0 {
		compact := compactChapterSummaries(summaries)
		if len(compact) > 3 {
			compact = compact[len(compact)-3:]
		}
		for i := range compact {
			compact[i].Summary = truncateRunes(compact[i].Summary, 120)
			compact[i].KeyEvents = compactStringList(compact[i].KeyEvents, 3, 120)
		}
		envelope.Working["recent_summaries"] = compact
	} else {
		warn("recent_summaries", err)
	}

	if state.chapter > 1 {
		if prevText, err := t.store.Drafts.LoadChapterText(state.chapter - 1); err == nil && prevText != "" {
			runes := []rune(prevText)
			if len(runes) > 600 {
				runes = runes[len(runes)-600:]
			}
			envelope.Working["previous_tail"] = string(runes)
		}
	}
}

func (t *ContextTool) buildChapterSelectedMemory(envelope *chapterContextEnvelope, state contextBuildState, warn func(string, error)) {
	if len(state.storyThreads) > 0 {
		envelope.Selected["story_threads"] = state.storyThreads
	}
	if lessons := t.selectReviewLessons(state.chapter, warn); len(lessons) > 0 {
		envelope.Selected["review_lessons"] = lessons
	}
}

func (t *ContextTool) buildChapterEpisodicMemory(envelope *chapterContextEnvelope, state contextBuildState, warn func(string, error)) {
	if len(state.foreshadow) > 0 && len(state.storyThreads) == 0 {
		envelope.Episodic["foreshadow_ledger"] = compactForeshadowEntries(state.foreshadow, 12)
	}

	// 配角名册：召回最近活跃的次要角色，让 Writer 在引入旧角色时能保持口吻/定位一致
	// 不召回所有条目（长篇会膨胀），只给最近活跃的前 N 个，按 LastSeenChapter 倒序
	_, hasSnapshots := envelope.Episodic["character_snapshots"]
	if recentCast, err := t.store.Cast.RecentActive(15); !hasSnapshots && err == nil && len(recentCast) > 0 {
		simplified := make([]map[string]any, 0, len(recentCast))
		for _, e := range recentCast {
			item := map[string]any{
				"name":             e.Name,
				"first_seen":       e.FirstSeenChapter,
				"last_seen":        e.LastSeenChapter,
				"appearance_count": e.AppearanceCount,
			}
			if e.BriefRole != "" {
				item["brief_role"] = e.BriefRole
			}
			if len(e.Aliases) > 0 {
				item["aliases"] = compactStringList(e.Aliases, 6, 40)
			}
			simplified = append(simplified, item)
		}
		envelope.Episodic["recent_cast"] = simplified
	} else if err != nil {
		warn("recent_cast", err)
	}
	if pending, err := t.store.Cast.PendingPromotions(); err == nil && len(pending) > 0 {
		tasks := make([]map[string]any, 0, len(pending))
		for _, entry := range pending {
			tasks = append(tasks, map[string]any{
				"name": entry.Name, "aliases": entry.Aliases,
				"brief_role": entry.BriefRole, "appearance_count": entry.AppearanceCount,
				"appearance_chapters": entry.AppearanceChapters,
				"reason":              entry.PromotionReason, "route": "character",
			})
		}
		envelope.Episodic["character_promotion_tasks"] = tasks
	} else if err != nil {
		warn("character_promotion_tasks", err)
	}

	if state.progress != nil && state.progress.TotalChapters > 30 && state.currentEntry != nil {
		if related := t.buildRelatedChapters(
			state.chapter,
			state.currentEntry,
			state.foreshadow,
			state.relationships,
			state.allStateChanges,
		); len(related) > 0 {
			envelope.Episodic["related_chapters"] = related
		}
	}

}

func (t *ContextTool) buildChapterReferencePack(envelope *chapterContextEnvelope, state contextBuildState) {
	if state.styleRules != nil {
		envelope.References["style_rules"] = compactWritingStyleRules(state.styleRules)
	} else {
		var maxCompleted int
		if state.progress != nil {
			maxCompleted = maxCompletedChapter(state.progress.CompletedChapters)
		}
		if anchors := t.store.Drafts.ExtractStyleAnchors(3, maxCompleted); len(anchors) > 0 {
			envelope.References["style_anchors"] = compactStringList(anchors, 3, 300)
		}

		if state.currentEntry != nil {
			var voiceSamples []map[string]any
			chars, _ := t.store.Characters.Load()
			for _, c := range chars {
				if c.Tier == "secondary" || c.Tier == "decorative" {
					continue
				}
				samples := t.store.Drafts.ExtractDialogue(c.Name, c.Aliases, 3, maxCompleted)
				if len(samples) > 0 {
					voiceSamples = append(voiceSamples, map[string]any{
						"character": c.Name,
						"samples":   compactStringList(samples, 3, 180),
					})
				}
				if len(voiceSamples) >= 5 {
					break
				}
			}
			if len(voiceSamples) > 0 {
				envelope.References["voice_samples"] = voiceSamples
			}
		}
	}

	envelope.References["references"] = t.writerReferences(state.chapter, state.purpose)
}

func (t *ContextTool) buildArchitectContext(result map[string]any, warn func(string, error)) {
	envelope := newArchitectContextEnvelope()
	result["memory_policy"] = domain.NewArchitectMemoryPolicy()
	t.buildArchitectPlanning(&envelope, warn)
	t.buildArchitectFoundation(&envelope, warn)
	t.buildArchitectReferences(&envelope, warn)
	envelope.apply(result)
}

func (t *ContextTool) buildArchitectPlanning(envelope *architectContextEnvelope, warn func(string, error)) {
	runMeta, err := t.store.RunMeta.Load()
	warn("run_meta", err)
	if runMeta != nil && runMeta.PlanningTier != "" {
		envelope.Planning["planning_tier"] = runMeta.PlanningTier
	}
	if runMeta != nil && runMeta.PlanningReview != nil {
		if contract := newCreativeBriefContract(runMeta.PlanningReview.Brief); contract != nil {
			envelope.Planning["creative_brief"] = contract
		}
		if feedback := strings.TrimSpace(runMeta.PlanningReview.FoundationFeedback); feedback != "" {
			envelope.Planning["foundation_revision_feedback"] = feedback
		}
	}

	var layered []domain.VolumeOutline
	progress, _ := t.store.Progress.Load()
	if l, err := t.store.Outline.LoadLayeredOutline(); err == nil && len(l) > 0 {
		layered = l
		envelope.Planning["layered_outline"] = compactLayeredOutlineForPlanning(layered, progress)
		envelope.Planning["volume_history_index"] = compactVolumeHistoryIndex(layered)
		envelope.Planning["volume_history_index_schema"] = []string{"index", "title", "chapter_count", "arc_count"}
		envelope.Planning["volume_theme_milestones"] = compactVolumeThemeMilestones(layered)
		envelope.Planning["volume_theme_milestones_schema"] = []string{"index", "theme"}
	} else {
		warn("layered_outline", err)
	}

	var compass *domain.StoryCompass
	if c, err := t.store.Outline.LoadCompass(); err == nil && c != nil {
		compass = c
		envelope.Planning["compass"] = compass
	} else {
		warn("compass", err)
	}
	if volSummaries, err := t.store.Summaries.LoadAllVolumeSummaries(); err == nil && len(volSummaries) > 0 {
		envelope.Planning["volume_summaries"] = compactVolumeSummaries(volSummaries, 2)
	} else {
		warn("volume_summaries", err)
	}

	// completion_signals 把"全书是否该结尾"的关键事实集中呈现，
	// 让架构师在裁定 complete_book / append_volume 时一眼看到对照面。
	// 散落在 progress / compass / foreshadow / layered_outline 里靠 LLM 脑算容易漏。
	envelope.Planning["completion_signals"] = t.completionSignals(layered, compass)
}

func (t *ContextTool) completionSignals(layered []domain.VolumeOutline, compass *domain.StoryCompass) map[string]any {
	signals := map[string]any{}
	if progress, _ := t.store.Progress.Load(); progress != nil {
		signals["completed_chapters"] = len(progress.CompletedChapters)
		signals["total_word_count"] = progress.TotalWordCount
		signals["phase"] = string(progress.Phase)
	}
	if len(layered) > 0 {
		signals["planned_chapters"] = len(domain.FlattenOutline(layered))
		signals["volumes_total"] = len(layered)
	}
	if compass != nil {
		if compass.EstimatedScale != "" {
			signals["compass_estimated_scale"] = compass.EstimatedScale
		}
		signals["open_threads_count"] = len(compass.OpenThreads)
	}
	if active, err := t.store.World.LoadActiveForeshadow(); err == nil {
		signals["active_foreshadow_count"] = len(active)
	}
	return signals
}

func (t *ContextTool) buildArchitectFoundation(envelope *architectContextEnvelope, warn func(string, error)) {
	foundation, foundationErr := t.store.Foundation.Load()
	if foundationErr != nil {
		warn("story_foundation", foundationErr)
	}
	premise := foundation.Premise
	if premise != "" {
		if sections := parsePremiseSections(premise); len(sections) > 0 {
			envelope.Foundation["premise_sections"] = compactPremiseSections(sections, 180)
		}
		tier := domain.PlanningTier("")
		if meta, err := t.store.RunMeta.Load(); err == nil && meta != nil {
			tier = meta.PlanningTier
		}
		envelope.Foundation["premise_structure"] = premiseStructure(premise, tier)
	}
	if foundation.Revision > 0 {
		envelope.Foundation["foundation_revision"] = foundation.Revision
		if signature, err := domain.FoundationAuditSignature(foundation); err == nil {
			envelope.Foundation["foundation_audit_signature"] = signature
		}
	}
	if len(foundation.Characters) > 0 {
		envelope.Foundation["characters"] = compactCharacters(foundation.Characters, maxContextCharacters)
	}
	if len(foundation.Relationships) > 0 {
		envelope.Foundation["planned_relationships"] = compactCharacterRelationships(
			foundation.Relationships,
			maxContextRelationships,
		)
	}
	envelope.Foundation["relationship_contract"] = "planned_relationships are pre-writing canonical intent; relationship_state is runtime chapter evidence and must never replace or rewrite the plan"

	if snapshots, err := t.store.Characters.LoadLatestSnapshots(); err == nil && len(snapshots) > 0 {
		envelope.Foundation["character_snapshots"] = compactCharacterSnapshots(snapshots, maxContextCharacterSnapshots)
	} else {
		warn("character_snapshots", err)
	}
	if len(foundation.WorldRules) > 0 {
		// Architect already receives each rule's category, strength and boundary.
		// Keep the canonical prefix source-bounded for mature projects instead of
		// relying on a lossy post-build JSON trim.
		rules := compactWorldRules(foundation.WorldRules, 25)
		envelope.Foundation["world_rules"] = rules
		var hardIDs []string
		for _, rule := range rules {
			if rule.Strength == domain.WorldRuleStrengthHard {
				hardIDs = append(hardIDs, rule.ID)
			}
		}
		if len(hardIDs) > 0 {
			// world_rules already carries the canonical rule text, category,
			// boundary and strength. Keep only stable IDs in this hard-only
			// index so the same long rules are not serialized twice.
			envelope.Foundation["hard_world_rule_constraints"] = hardIDs
		}
	}
	if foreshadow, err := t.store.World.LoadActiveForeshadow(); err == nil && len(foreshadow) > 0 {
		envelope.Foundation["foreshadow_ledger"] = compactForeshadowEntries(foreshadow, maxContextForeshadowEntries)
	} else {
		warn("foreshadow_ledger", err)
	}
	envelope.Foundation["foundation_status"] = t.foundationStatus()
}

func (t *ContextTool) buildArchitectReferences(envelope *architectContextEnvelope, warn func(string, error)) {
	if styleRules, err := t.store.World.LoadStyleRules(); err == nil && styleRules != nil {
		envelope.References["style_rules"] = compactWritingStyleRules(styleRules)
	} else {
		warn("style_rules", err)
	}

	envelope.References["references"] = t.architectReferences()
}
