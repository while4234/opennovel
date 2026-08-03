package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/voocel/agentcore/schema"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/errs"
	"github.com/voocel/ainovel-cli/internal/store"
)

// SaveFoundationTool 保存基础设定（premise/outline/characters），Architect 专用。
type SaveFoundationTool struct {
	store          *store.Store
	completionGate CompletionGate
}

type outlineSimilarityReviewVerdict struct {
	Chapter         int    `json:"chapter"`
	ExistingChapter int    `json:"existing_chapter"`
	Duplicate       bool   `json:"duplicate"`
	Reason          string `json:"reason"`
}

func NewSaveFoundationTool(store *store.Store, gates ...CompletionGate) *SaveFoundationTool {
	return &SaveFoundationTool{store: store, completionGate: completionGateFrom(gates)}
}

// ArchitectSaveFoundationTool keeps the reusable persistence tool intact while
// enforcing the Architect role's narrower original-planning authority.
type ArchitectSaveFoundationTool struct {
	inner *SaveFoundationTool
}

var confirmedCharacterRebindLocks sync.Map

func NewArchitectSaveFoundationTool(store *store.Store, gates ...CompletionGate) *ArchitectSaveFoundationTool {
	return &ArchitectSaveFoundationTool{inner: NewSaveFoundationTool(store, gates...)}
}

func (t *ArchitectSaveFoundationTool) Name() string { return t.inner.Name() }
func (t *ArchitectSaveFoundationTool) Description() string {
	return t.inner.Description() +
		" Architect authority excludes type=characters and type=planned_relationships; those sections are owned exclusively by the Character workflow."
}
func (t *ArchitectSaveFoundationTool) Label() string { return t.inner.Label() }
func (t *ArchitectSaveFoundationTool) ReadOnly(args json.RawMessage) bool {
	return t.inner.ReadOnly(args)
}
func (t *ArchitectSaveFoundationTool) ConcurrencySafe(args json.RawMessage) bool {
	return t.inner.ConcurrencySafe(args)
}
func (t *ArchitectSaveFoundationTool) Schema() map[string]any { return t.inner.Schema() }
func (t *ArchitectSaveFoundationTool) Execute(ctx context.Context, args json.RawMessage) (json.RawMessage, error) {
	var request struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(args, &request); err != nil {
		return nil, fmt.Errorf("invalid args: %w: %w", errs.ErrToolArgs, err)
	}
	switch strings.TrimSpace(request.Type) {
	case "characters", "planned_relationships":
		return nil, fmt.Errorf(
			"Architect cannot write Foundation section %q; dispatch Character Agent analyze/review and wait for explicit user confirmation: %w",
			strings.TrimSpace(request.Type),
			errs.ErrToolPrecondition,
		)
	}
	if !t.inner.store.Adaptation.Active() {
		if err := requireConfirmedOriginalCharacterWorkflow(t.inner.store); err != nil {
			return nil, fmt.Errorf(
				"Architect must wait for the Character Agent candidate, independent review, and explicit user confirmation before writing Foundation section %q: %w: %w",
				strings.TrimSpace(request.Type),
				errs.ErrToolPrecondition,
				err,
			)
		}
	}
	result, err := t.inner.Execute(ctx, args)
	if err != nil {
		return nil, err
	}
	switch strings.TrimSpace(request.Type) {
	case "premise", "world_rules":
		if !t.inner.store.Adaptation.Active() {
			if err := rebindConfirmedCharacterWorkflow(t.inner.store); err != nil {
				return nil, fmt.Errorf(
					"Foundation section %q was saved but the confirmed Character workflow could not be rebound: %w",
					strings.TrimSpace(request.Type),
					err,
				)
			}
		}
	}
	return result, nil
}

func requireConfirmedOriginalCharacterWorkflow(st *store.Store) error {
	candidate, lifecycle, binding, err := CurrentCharacterWorkflow(st)
	if err != nil {
		return err
	}
	if candidate == nil || lifecycle == nil {
		return fmt.Errorf("character candidate or lifecycle is missing")
	}
	if lifecycle.AnalysisStatus != domain.CharacterCardAnalysisCandidateReady ||
		lifecycle.ReviewStatus != domain.CharacterCardReviewPassed ||
		lifecycle.ConfirmationStatus != domain.CharacterCardConfirmed ||
		lifecycle.Candidate != binding.Candidate ||
		lifecycle.ReviewedCandidate != binding.Candidate ||
		lifecycle.ReviewedInputDigest != binding.InputDigest {
		return fmt.Errorf("character workflow is not currently reviewed and confirmed")
	}
	return nil
}

// RepairConfirmedCharacterWorkflowForResume upgrades metadata on an already
// reviewed and confirmed Character publication before an old project resumes.
// It is deliberately a no-op for missing or unconfirmed candidates: those
// projects must continue through the normal Character review and user gate.
func RepairConfirmedCharacterWorkflowForResume(st *store.Store) error {
	if st == nil {
		return fmt.Errorf("character workflow store is nil")
	}
	candidate, err := st.CharacterCards.LoadCandidate()
	if err != nil || candidate == nil {
		return err
	}
	lifecycle, err := st.CharacterCards.Load(candidate.Base)
	if err != nil {
		return err
	}
	if lifecycle == nil || lifecycle.ConfirmationStatus != domain.CharacterCardConfirmed {
		return nil
	}
	return rebindConfirmedCharacterWorkflow(st)
}

// rebindConfirmedCharacterWorkflow advances the Character candidate and
// lifecycle to a canonical Foundation revision whose character and
// relationship content is unchanged. Premise/world-rule generation must not
// invalidate an already reviewed and user-confirmed cast.
func rebindConfirmedCharacterWorkflow(st *store.Store) error {
	lockKey := strings.ToLower(strings.TrimSpace(st.Dir()))
	lockValue, _ := confirmedCharacterRebindLocks.LoadOrStore(lockKey, &sync.Mutex{})
	rebindLock := lockValue.(*sync.Mutex)
	rebindLock.Lock()
	defer rebindLock.Unlock()

	candidate, err := st.CharacterCards.LoadCandidate()
	if err != nil {
		return err
	}
	if candidate == nil {
		return fmt.Errorf("character candidate is missing")
	}
	lifecycle, err := st.CharacterCards.Load(candidate.Base)
	if err != nil {
		return err
	}
	if lifecycle == nil ||
		lifecycle.AnalysisStatus != domain.CharacterCardAnalysisCandidateReady ||
		lifecycle.ReviewStatus != domain.CharacterCardReviewPassed ||
		lifecycle.ConfirmationStatus != domain.CharacterCardConfirmed ||
		lifecycle.Candidate != candidate.Base.Candidate ||
		lifecycle.ReviewedCandidate != candidate.Base.Candidate ||
		lifecycle.ReviewedInputDigest != candidate.Base.InputDigest {
		return fmt.Errorf("character workflow is not a confirmed publication")
	}
	if lifecycle.Mode == domain.CharacterCardProjectOriginal {
		if err := completeConfirmedCharacterMetadata(st); err != nil {
			return err
		}
	}
	canonical, _, inputs, coreCast, err := CurrentCharacterCanonicalBinding(st)
	if err != nil {
		return err
	}
	rebound, err := domain.CharacterCardBindingFromFoundation(canonical, inputs)
	if err != nil {
		return err
	}
	if !confirmedCharacterRebindCompatible(*candidate, canonical, rebound, coreCast) {
		return fmt.Errorf("canonical character content or Character inputs changed")
	}
	completeness, err := domain.EvaluateCharacterCardCompleteness(canonical, coreCast)
	if err != nil {
		return err
	}
	for _, result := range completeness {
		if result.Status != domain.CharacterCardComplete {
			return fmt.Errorf("confirmed Character metadata upgrade is incomplete for %q", result.CharacterID)
		}
	}
	if candidate.Base.Candidate == rebound.Candidate &&
		candidate.Base.InputDigest == rebound.InputDigest &&
		candidate.Foundation.Revision == canonical.Revision &&
		lifecycle.Candidate == rebound.Candidate &&
		lifecycle.InputDigest == rebound.InputDigest &&
		lifecycle.ReviewedCandidate == rebound.Candidate &&
		lifecycle.ReviewedInputDigest == rebound.InputDigest &&
		reflect.DeepEqual(lifecycle.Completeness, completeness) {
		return nil
	}
	reboundCandidate := *candidate
	reboundCandidate.Foundation = canonical
	reboundCandidate.Base = rebound
	if coreCast != nil {
		reboundCandidate.ProjectedCast = cloneCoreCastForCharacterRepair(*coreCast)
	}
	savedCandidate, err := st.CharacterCards.SaveCandidateCAS(reboundCandidate, candidate.Revision)
	if err != nil {
		return err
	}
	reboundLifecycle := *lifecycle
	reboundLifecycle.Candidate = rebound.Candidate
	reboundLifecycle.Inputs = rebound.Inputs
	reboundLifecycle.InputDigest = rebound.InputDigest
	reboundLifecycle.ReviewedCandidate = rebound.Candidate
	reboundLifecycle.ReviewedInputDigest = rebound.InputDigest
	reboundLifecycle.Completeness = completeness
	if _, err := st.CharacterCards.SaveCAS(reboundLifecycle, lifecycle.Revision, rebound); err != nil {
		return err
	}
	*candidate = savedCandidate
	return nil
}

func completeConfirmedCharacterMetadata(st *store.Store) error {
	canonical, err := st.Foundation.Load()
	if err != nil {
		return err
	}
	coreCast, err := st.CoreCast.Load()
	if err != nil {
		return err
	}
	coreGenders := make(map[string]string)
	if coreCast != nil {
		for _, member := range coreCast.Members {
			if gender := validCharacterGender(member.Character.Gender); gender != "" {
				coreGenders[member.Character.ID] = gender
			}
		}
	}
	missingFoundationGenders := make(map[string]string)
	for _, character := range canonical.Characters {
		if validCharacterGender(character.Gender) != "" {
			continue
		}
		gender := coreGenders[character.ID]
		if gender == "" {
			gender = inferLegacyCharacterGender(character)
		}
		missingFoundationGenders[character.ID] = gender
	}
	if len(missingFoundationGenders) > 0 {
		canonical, err = st.CompleteMissingCharacterGenders(canonical.Revision, missingFoundationGenders)
		if err != nil {
			return fmt.Errorf("complete legacy Foundation character metadata: %w", err)
		}
	}
	if coreCast == nil {
		return nil
	}
	canonicalGenders := make(map[string]string, len(canonical.Characters))
	for _, character := range canonical.Characters {
		canonicalGenders[character.ID] = validCharacterGender(character.Gender)
	}
	missingCoreGenders := make(map[string]string)
	for _, member := range coreCast.Members {
		if validCharacterGender(member.Character.Gender) != "" {
			continue
		}
		gender := canonicalGenders[member.Character.ID]
		if gender == "" {
			gender = inferLegacyCharacterGender(member.Character)
		}
		missingCoreGenders[member.Character.ID] = gender
	}
	if len(missingCoreGenders) == 0 {
		return nil
	}
	if _, err := st.CompleteMissingCoreCastGenders(coreCast.Revision, missingCoreGenders); err != nil {
		return fmt.Errorf("complete legacy CoreCast character metadata: %w", err)
	}
	return nil
}

func confirmedCharacterRebindCompatible(
	candidate domain.CharacterCardCandidate,
	canonical domain.StoryFoundation,
	current domain.CharacterCardBinding,
	currentCoreCast *domain.CoreCastContract,
) bool {
	if candidate.Base.Candidate.CharacterContentDigest != current.Candidate.CharacterContentDigest {
		enriched := domain.CloneStoryFoundation(candidate.Foundation)
		currentGenders := make(map[string]string, len(canonical.Characters))
		for _, character := range canonical.Characters {
			currentGenders[character.ID] = character.Gender
		}
		for index := range enriched.Characters {
			legacy := &enriched.Characters[index]
			gender, exists := currentGenders[legacy.ID]
			if !exists || !completeCompatibleGender(&legacy.Gender, gender) {
				return false
			}
		}
		digest, err := domain.CharacterCardContentDigest(enriched)
		if err != nil || digest != current.Candidate.CharacterContentDigest {
			return false
		}
	}
	if candidate.Base.InputDigest == current.InputDigest {
		return true
	}
	legacyInputs := candidate.Base.Inputs
	currentInputs := current.Inputs
	legacyCoreSignature := legacyInputs.CoreCast
	legacyInputs.CoreCast = currentInputs.CoreCast
	if !confirmedCharacterResumeInputsCompatible(legacyInputs, currentInputs) {
		return false
	}
	if legacyCoreSignature == currentInputs.CoreCast {
		return true
	}
	if currentCoreCast == nil || currentInputs.CoreCast != currentCoreCast.ContentSignature {
		return false
	}
	enrichedCore := cloneCoreCastForCharacterRepair(candidate.ProjectedCast)
	currentMembers := make(map[string]domain.Character, len(currentCoreCast.Members))
	for _, member := range currentCoreCast.Members {
		currentMembers[member.Character.ID] = member.Character
	}
	for index := range enrichedCore.Members {
		legacy := &enrichedCore.Members[index].Character
		currentCharacter, exists := currentMembers[legacy.ID]
		if !exists || !completeCompatibleGender(&legacy.Gender, currentCharacter.Gender) {
			return false
		}
	}
	signature, err := domain.CoreCastContentSignature(enrichedCore)
	return err == nil && signature == currentCoreCast.ContentSignature
}

// confirmedCharacterResumeInputsCompatible keeps durable story evidence strict
// while allowing the runtime user-rules snapshot to advance independently.
// Once the canonical Character content and CoreCast have already proved equal,
// writing-only rule/schema refreshes must not invalidate the user-confirmed
// cast. Any other named evidence remains fail-closed.
func confirmedCharacterResumeInputsCompatible(
	legacy domain.CharacterCardInputSignatures,
	current domain.CharacterCardInputSignatures,
) bool {
	legacyAdditional := legacy.Additional
	currentAdditional := current.Additional
	legacy.Additional = nil
	current.Additional = nil
	if !reflect.DeepEqual(legacy, current) {
		return false
	}
	legacyNamed := make(map[string]string, len(legacyAdditional))
	for _, signature := range legacyAdditional {
		if name := strings.TrimSpace(signature.Name); name != "" && name != "user_rules" {
			legacyNamed[name] = strings.TrimSpace(signature.Signature)
		}
	}
	currentNamed := make(map[string]string, len(currentAdditional))
	for _, signature := range currentAdditional {
		if name := strings.TrimSpace(signature.Name); name != "" && name != "user_rules" {
			currentNamed[name] = strings.TrimSpace(signature.Signature)
		}
	}
	return reflect.DeepEqual(legacyNamed, currentNamed)
}

func completeCompatibleGender(legacy *string, current string) bool {
	current = validCharacterGender(current)
	if current == "" {
		return false
	}
	existing := validCharacterGender(*legacy)
	if existing != "" && existing != current {
		return false
	}
	*legacy = current
	return true
}

func validCharacterGender(value string) string {
	switch value = strings.ToLower(strings.TrimSpace(value)); value {
	case "male", "female", "nonbinary", "unspecified":
		return value
	default:
		return ""
	}
}

func inferLegacyCharacterGender(character domain.Character) string {
	role := strings.ToLower(strings.TrimSpace(character.Role))
	female := containsAnyCharacterMarker(role,
		"女主", "女配", "女性", "未婚妻", "妻子", "母亲", "妈妈", "姐姐", "妹妹",
		"female", "woman", "wife", "mother", "sister",
	)
	male := containsAnyCharacterMarker(role,
		"男主", "男配", "男性", "未婚夫", "丈夫", "父亲", "爸爸", "哥哥", "弟弟",
		"male", "man", "husband", "father", "brother",
	)
	if female != male {
		if female {
			return "female"
		}
		return "male"
	}
	return "unspecified"
}

func containsAnyCharacterMarker(value string, markers ...string) bool {
	for _, marker := range markers {
		if strings.Contains(value, marker) {
			return true
		}
	}
	return false
}

func cloneCoreCastForCharacterRepair(value domain.CoreCastContract) domain.CoreCastContract {
	cloned := value
	cloned.Members = append([]domain.CoreCastMember(nil), value.Members...)
	for index := range cloned.Members {
		cloned.Members[index].Character = domain.CloneCharacter(value.Members[index].Character)
		cloned.Members[index].SourceCharacterIDs = append(
			[]string(nil),
			value.Members[index].SourceCharacterIDs...,
		)
	}
	cloned.PlannedRelationships = append(
		[]domain.CharacterRelationship(nil),
		value.PlannedRelationships...,
	)
	cloned.SourceDispositions = append(
		[]domain.SourceCharacterDisposition(nil),
		value.SourceDispositions...,
	)
	return cloned
}

func (t *SaveFoundationTool) Name() string { return "save_foundation" }
func (t *SaveFoundationTool) Description() string {
	return "保存小说基础设定（premise/outline/characters/planned_relationships/world_rules/compass 等）。**这是唯一持久化入口**：未经此工具调用保存的内容不会进入 store，只在消息里输出 Markdown/JSON 等于丢失。Foundation 生成轮次中的 premise / characters / planned_relationships / world_rules 必须原样携带 Host 指令给出的 foundation_generation 与 foundation_base_revision；旧轮次、重复或并发响应会被拒绝。type 可选 premise / outline / layered_outline / characters / planned_relationships / world_rules / expand_arc / repair_arc / repair_volume / append_volume / update_compass / complete_book。premise 时 content 必须是 Markdown 字符串。普通 world_rules 写入可传 WorldRule 数组或对象 {\"hard_rules\":[WorldRule...],\"soft_rules\":[WorldRule...]}；Foundation 生成轮次不接受数组，必须使用该对象且 hard_rules、soft_rules 均非空。每条必须包含非空 rule 和 boundary，不接受自定义 setting/reality_rules/narrative_rules 等字段。其他类型 content 优先直接传 JSON 数组或对象。planned_relationships 必须使用 characters 中的稳定 ID，且只保存创作前计划关系，不写入 relationship_state。outline/expand_arc/repair_arc 的 character_beats 必须使用稳定 character_id，relationship_beats 必须使用已确认 relationship_id 及其 source_character_id/target_character_id，不能用姓名代替 ID。expand_arc 展开骨架弧的详细章节（需 volume + arc，普通原创审核规划每批严格 3-4 章）；repair_arc 修复已展开弧；repair_volume 按自动审核或用户意见完整替换一个尚未展开的分卷骨架且不得改变该卷预估总章数；append_volume 追加新卷（普通原创分卷规划阶段每次只追加一个骨架卷，每弧3-4章）；update_compass 更新终局方向；complete_book 宣告全书完结。scale 可选，仅允许 short / mid / long。"
}
func (t *SaveFoundationTool) Label() string { return "保存设定" }

// 写工具（跨域更新 Outline/Progress/Characters），禁止并发。
func (t *SaveFoundationTool) ReadOnly(_ json.RawMessage) bool        { return false }
func (t *SaveFoundationTool) ConcurrencySafe(_ json.RawMessage) bool { return false }

func (t *SaveFoundationTool) Schema() map[string]any {
	return schema.Object(
		schema.Property("type", schema.Enum("设定类型。建议显式传；若缺失，工具会在内容和当前缺失项唯一明确时自动推断。", "premise", "outline", "layered_outline", "characters", "planned_relationships", "world_rules", "expand_arc", "repair_arc", "repair_volume", "append_volume", "update_compass", "complete_book")),
		schema.Property("content", map[string]any{
			"description": "内容。premise 传 Markdown 字符串；普通 world_rules 可传 WorldRule 数组或 {\"hard_rules\":[WorldRule...],\"soft_rules\":[WorldRule...]}。Foundation 生成轮次的 world_rules 不接受数组，必须使用该对象、两组均非空；每条使用 id/category/title/rule/boundary/strength/priority/tags 且 rule、boundary 非空。其他类型直接传 JSON 数组或对象，也兼容 JSON 字符串。expand_arc / repair_arc 时传章节数组。",
		}).Required(),
		schema.Property("scale", schema.Enum("规划级别", "short", "mid", "long")),
		schema.Property("foundation_generation", schema.Int("Foundation 生成轮次；生成四个 Foundation section 时必须与 Host 指令完全一致")),
		schema.Property("foundation_base_revision", schema.Int("Foundation 生成轮次的固定基础 revision；生成四个 Foundation section 时必须与 Host 指令完全一致")),
		schema.Property("volume", schema.Int("目标卷序号（expand_arc / repair_arc 时必传）")),
		schema.Property("arc", schema.Int("目标弧序号（expand_arc / repair_arc 时必传）")),
		schema.Property("from_chapter", schema.Int("repair_arc 局部修复窗口起始全局章节号；由 Host 指令提供时必须原样传入")),
		schema.Property("to_chapter", schema.Int("repair_arc 局部修复窗口结束全局章节号；由 Host 指令提供时必须原样传入")),
		schema.Property("similarity_review", map[string]any{
			"description": "Optional verdicts for borderline similar outline pairs: [{chapter, existing_chapter, duplicate, reason}]. Use only after model review.",
		}),
	)
}

func (t *SaveFoundationTool) Execute(ctx context.Context, args json.RawMessage) (json.RawMessage, error) {
	var a struct {
		Type                   string                           `json:"type"`
		Content                json.RawMessage                  `json:"content"`
		Scale                  string                           `json:"scale"`
		Volume                 int                              `json:"volume"`
		Arc                    int                              `json:"arc"`
		From                   int                              `json:"from_chapter"`
		To                     int                              `json:"to_chapter"`
		Review                 []outlineSimilarityReviewVerdict `json:"similarity_review"`
		FoundationGeneration   int64                            `json:"foundation_generation"`
		FoundationBaseRevision int64                            `json:"foundation_base_revision"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return nil, fmt.Errorf("invalid args: %w: %w", errs.ErrToolArgs, err)
	}
	content, err := normalizeFoundationContent(a.Content)
	if err != nil {
		return nil, err
	}
	a.Type = strings.TrimSpace(a.Type)
	if a.Type == "" {
		inferred, inferErr := t.inferFoundationType(content)
		if inferErr != nil {
			return nil, inferErr
		}
		a.Type = inferred
	}
	var foundationOwner *store.FoundationPlanningOwner
	if blocksDuringActiveRevision(a.Type) {
		active, activeErr := t.store.Revisions.Active()
		if activeErr != nil {
			return nil, fmt.Errorf("read active revision before formal outline write: %w", activeErr)
		}
		if active != nil {
			if active.Mode != domain.RevisionModeFoundation || (a.Type != "repair_arc" && a.Type != "repair_volume") {
				return nil, fmt.Errorf("formal save_foundation type=%s is blocked by active revision %s; use revision-owned candidates: %w", a.Type, active.ID, errs.ErrToolPrecondition)
			}
			artifactID, err := foundationRepairArtifactID(t.store, a.Type, a.Volume, a.Arc)
			if err != nil {
				return nil, fmt.Errorf("resolve Foundation repair scope: %w", err)
			}
			foundationOwner, err = t.store.Revisions.AuthorizeFoundationPlanning(ctx, artifactID)
			if err != nil {
				return nil, fmt.Errorf("authorize Foundation repair: %w: %w", errs.ErrToolPrecondition, err)
			}
		}
	}
	if !t.store.Adaptation.Active() && requiresConfirmedFoundation(a.Type) {
		if err := t.store.RequireConfirmedFoundation(); err != nil {
			return nil, fmt.Errorf("save_foundation type=%s is blocked by foundation confirmation gate: %w", a.Type, err)
		}
	}
	if a.Scale != "" {
		switch domain.PlanningTier(a.Scale) {
		case domain.PlanningTierShort, domain.PlanningTierMid, domain.PlanningTierLong:
		default:
			return nil, fmt.Errorf("invalid scale %q, expected short/mid/long: %w", a.Scale, errs.ErrToolArgs)
		}
		if err := t.store.RunMeta.SetPlanningTier(domain.PlanningTier(a.Scale)); err != nil {
			return nil, fmt.Errorf("save planning tier: %w: %w", errs.ErrStoreWrite, err)
		}
	}

	result := map[string]any{"saved": true, "type": a.Type, "scale": a.Scale}
	var generationReview *domain.PlanningReview
	var generationFence *store.FoundationGenerationFence
	if a.FoundationGeneration != 0 || a.FoundationBaseRevision != 0 {
		generationFence = &store.FoundationGenerationFence{Generation: a.FoundationGeneration, BaseRevision: a.FoundationBaseRevision}
	}

	if t.store.Adaptation.Active() && (a.Type == "append_volume" || a.Type == "expand_arc") {
		return nil, fmt.Errorf("改编模式已由 confirmed plan 锁定章节规模，不允许 %s；如需增删或重排章节，请重新生成规模提案并确认: %w", a.Type, errs.ErrToolPrecondition)
	}
	if t.longBlueprintSkeletonLocked() && a.Type != "append_volume" && a.Type != "repair_volume" && a.Type != "update_compass" {
		return nil, fmt.Errorf(
			"normal-original volume skeleton is already in batched generation; existing premise, characters, world_rules and layered_outline are locked. Only append_volume, audit-directed repair_volume, or update_compass is allowed until volume review: %w",
			errs.ErrToolPrecondition,
		)
	}
	if t.collectingLongBlueprint() && t.longBlueprintArtifactExists(a.Type) {
		return nil, fmt.Errorf(
			"normal-original blueprint artifact %s is already persisted and locked; continue with the remaining missing foundation instead of overwriting it: %w",
			a.Type,
			errs.ErrToolPrecondition,
		)
	}

	// 写作阶段禁止全量覆盖大纲，只允许增量操作（expand_arc / repair_arc / append_volume）
	if (a.Type == "outline" || a.Type == "layered_outline") && t.isWriting() {
		return nil, fmt.Errorf(
			"写作阶段禁止使用 %s 全量覆盖大纲。请使用 expand_arc 展开骨架弧、repair_arc 修复已展开弧，或 append_volume 追加新卷: %w", a.Type, errs.ErrToolPrecondition)
	}

	decode := func(typeName string, out any) error {
		return decodeFoundationJSON(typeName, content, out)
	}

	switch a.Type {
	case "premise":
		if err := t.validateCreativeBriefPremise(content); err != nil {
			return nil, err
		}
		name := domain.ExtractNovelNameFromPremise(content)
		generationReview, err = t.store.SaveFoundationPremise(generationFence, content)
		if err != nil {
			return nil, foundationGenerationSaveError("premise", err)
		}
		if name != "" {
			_ = t.store.Progress.SetNovelName(name)
			result["novel_name"] = name
		}
		_ = t.store.Progress.UpdatePhase(domain.PhasePremise)

	case "outline":
		if t.collectingLongBlueprint() {
			return nil, fmt.Errorf("long normal-original planning must save a layered volume skeleton before detailed chapters; flat outline is only for shorter works: %w", errs.ErrToolPrecondition)
		}
		entries, decodeErr := decodeOutlineEntries("outline", content)
		if decodeErr != nil {
			err = decodeErr
			return nil, err
		}
		entries, err = t.prepareOutlineCharacters(entries)
		if err != nil {
			return nil, err
		}
		normalizeOutlineEntryChapters(entries)
		if err := validateGeneratedOutline("outline", entries, a.Review); err != nil {
			return nil, err
		}
		if err := t.store.Outline.SaveOutline(entries); err != nil {
			return nil, fmt.Errorf("save outline: %w: %w", errs.ErrStoreWrite, err)
		}
		_ = t.store.Progress.UpdatePhase(domain.PhaseOutline)
		_ = t.store.Progress.SetTotalChapters(len(entries))
		if domain.PlanningTier(a.Scale) != domain.PlanningTierLong {
			_ = t.store.Progress.SetLayered(false)
			_ = t.store.Progress.UpdateVolumeArc(0, 0)
			_ = t.store.Outline.ClearLayeredOutline()
		}
		result["chapters"] = len(entries)
		if err := t.updateWordBudgetPlan(len(entries), result); err != nil {
			return nil, err
		}

	case "layered_outline":
		volumes, decodeErr := decodeLayeredOutline(content)
		if decodeErr != nil {
			return nil, decodeErr
		}
		if err := t.prepareLayeredOutlineCharacters(volumes); err != nil {
			return nil, err
		}
		if err := validateGeneratedLayeredOutline(volumes, a.Review); err != nil {
			return nil, err
		}
		if t.collectingLongBlueprint() {
			if len(volumes) != 1 {
				return nil, fmt.Errorf("normal-original long planning must generate the volume skeleton in batches; the first layered_outline call must contain exactly one volume, got %d: %w", len(volumes), errs.ErrToolPrecondition)
			}
			if err := t.validateLongVolumeSkeletonStructure(volumes); err != nil {
				return nil, err
			}
			_ = t.store.OriginalPlanningAudits.Reset()
			result["volume_batch_saved"] = true
		}
		flat := domain.FlattenOutline(volumes)
		if err := t.store.Outline.SaveLayeredOutline(volumes); err != nil {
			return nil, fmt.Errorf("save layered_outline: %w: %w", errs.ErrStoreWrite, err)
		}
		if err := t.store.Outline.SaveOutline(flat); err != nil {
			return nil, fmt.Errorf("save flattened outline: %w: %w", errs.ErrStoreWrite, err)
		}
		total := domain.TotalChapters(volumes)
		_ = t.store.Progress.UpdatePhase(domain.PhaseOutline)
		_ = t.store.Progress.SetTotalChapters(total)
		_ = t.store.Progress.SetLayered(true)
		if len(volumes) > 0 && len(volumes[0].Arcs) > 0 {
			_ = t.store.Progress.UpdateVolumeArc(volumes[0].Index, volumes[0].Arcs[0].Index)
		}
		result["volumes"] = len(volumes)
		result["chapters"] = total
		if err := t.updateWordBudgetPlan(total, result); err != nil {
			return nil, err
		}

	case "characters":
		var chars []domain.Character
		if err := decode("characters", &chars); err != nil {
			return nil, err
		}
		if err := t.validateCreativeBriefCharacters(chars); err != nil {
			return nil, err
		}
		generationReview, err = t.store.SaveFoundationCharacters(generationFence, chars)
		if err != nil {
			return nil, foundationGenerationSaveError("characters", err)
		}
		result["count"] = len(chars)

	case "planned_relationships":
		var relationships []domain.CharacterRelationship
		if err := decode("planned_relationships", &relationships); err != nil {
			return nil, err
		}
		generationReview, err = t.store.SaveFoundationRelationships(generationFence, relationships)
		if err != nil {
			return nil, foundationGenerationSaveError("planned_relationships", err)
		}
		result["count"] = len(relationships)

	case "world_rules":
		var rules []domain.WorldRule
		var decodeErr error
		if generationFence != nil {
			rules, decodeErr = decodeFoundationGenerationWorldRules(content)
		} else {
			rules, decodeErr = decodeWorldRules(content)
		}
		if decodeErr != nil {
			err = decodeErr
			return nil, err
		}
		generationReview, err = t.store.SaveFoundationWorldRules(generationFence, rules)
		if err != nil {
			return nil, foundationGenerationSaveError("world_rules", err)
		}
		result["count"] = len(rules)

	case "expand_arc":
		if a.Volume <= 0 || a.Arc <= 0 {
			return nil, fmt.Errorf("expand_arc requires volume and arc parameters: %w", errs.ErrToolArgs)
		}
		chapters, decodeErr := decodeOutlineEntries("expand_arc chapters", content)
		if decodeErr != nil {
			err = decodeErr
			return nil, err
		}
		chapters, err = t.prepareOutlineCharacters(chapters)
		if err != nil {
			return nil, err
		}
		if err := validateGeneratedOutline(fmt.Sprintf("expand_arc V%d A%d", a.Volume, a.Arc), chapters, a.Review); err != nil {
			return nil, err
		}
		if err := t.store.ExpandArc(a.Volume, a.Arc, chapters); err != nil {
			return nil, fmt.Errorf("expand arc: %w: %w", errs.ErrStoreWrite, err)
		}
		result["volume"] = a.Volume
		result["arc"] = a.Arc
		result["chapters"] = len(chapters)
		if review, _ := t.store.RunMeta.PlanningReview(); review != nil && review.Status == domain.PlanningReviewStatusCollecting && review.Kind == domain.PlanningReviewKindVolumeSplit {
			result["audit_required"] = true
		}
		if err := t.refreshWordBudgetPlan(result); err != nil {
			return nil, err
		}

	case "repair_arc":
		if a.Volume <= 0 || a.Arc <= 0 {
			return nil, fmt.Errorf("repair_arc requires volume and arc parameters: %w", errs.ErrToolArgs)
		}
		chapters, decodeErr := decodeOutlineEntries("repair_arc chapters", content)
		if decodeErr != nil {
			err = decodeErr
			return nil, err
		}
		chapters, err = t.prepareOutlineCharacters(chapters)
		if err != nil {
			return nil, err
		}
		if err := validateGeneratedOutline(fmt.Sprintf("repair_arc V%d A%d", a.Volume, a.Arc), chapters, a.Review); err != nil {
			return nil, err
		}
		var repairErr error
		if foundationOwner != nil {
			repairErr = t.store.RepairArcOutlineRangeForFoundationRevision(foundationOwner, a.Volume, a.Arc, a.From, a.To, chapters)
		} else {
			repairErr = t.store.RepairArcOutlineRange(a.Volume, a.Arc, a.From, a.To, chapters)
		}
		if repairErr != nil {
			return nil, fmt.Errorf("repair arc: %w: %w", errs.ErrStoreWrite, repairErr)
		}
		if err := t.store.OriginalPlanningAudits.InvalidateRepair(a.Volume, a.Arc, a.From, a.To); err != nil {
			return nil, fmt.Errorf("invalidate repaired outline audits: %w: %w", errs.ErrStoreWrite, err)
		}
		result["volume"] = a.Volume
		result["arc"] = a.Arc
		result["chapters"] = len(chapters)
		if a.From > 0 || a.To > 0 {
			result["from_chapter"] = a.From
			result["to_chapter"] = a.To
		}
		if err := t.refreshWordBudgetPlan(result); err != nil {
			return nil, err
		}

	case "append_volume":
		if p, _ := t.store.Progress.Load(); p != nil && p.Phase == domain.PhaseComplete {
			active, err := t.store.Revisions.Active()
			if err != nil {
				return nil, fmt.Errorf("read completed-book revision gate: %w", err)
			}
			if active == nil || active.Mode != domain.RevisionModeNormal || active.Stage != domain.RevisionStageCandidateGenerating {
				return nil, fmt.Errorf("全书已完结；追加新卷必须先通过普通原创修订影响预览与人工确认: %w", errs.ErrToolPrecondition)
			}
		}
		var vol domain.VolumeOutline
		if err := decode("append_volume", &vol); err != nil {
			return nil, err
		}
		if t.collectingLongBlueprint() {
			if err := t.validateLongVolumeSkeletonStructure([]domain.VolumeOutline{vol}); err != nil {
				return nil, err
			}
			if err := t.store.AppendSkeletonVolume(vol); err != nil {
				return nil, fmt.Errorf("append skeleton volume: %w: %w", errs.ErrStoreWrite, err)
			}
			result["volume_batch_saved"] = true
		} else if err := t.store.AppendVolume(vol); err != nil {
			return nil, fmt.Errorf("append volume: %w: %w", errs.ErrStoreWrite, err)
		}
		result["volume"] = vol.Index
		result["arcs"] = len(vol.Arcs)
		chCount := 0
		for _, arc := range vol.Arcs {
			if len(arc.Chapters) > 0 {
				chCount += len(arc.Chapters)
			} else {
				chCount += arc.EstimatedChapters
			}
		}
		if chCount > 0 {
			result["chapters"] = chCount
		}
		if err := t.refreshWordBudgetPlan(result); err != nil {
			return nil, err
		}

	case "repair_volume":
		if a.Volume <= 0 {
			return nil, fmt.Errorf("repair_volume requires volume: %w", errs.ErrToolArgs)
		}
		var repaired domain.VolumeOutline
		if err := decode("repair_volume", &repaired); err != nil {
			return nil, err
		}
		if repaired.Index != a.Volume {
			return nil, fmt.Errorf("repair_volume target is V%d but content index is V%d: %w", a.Volume, repaired.Index, errs.ErrToolArgs)
		}
		if err := t.validateLongVolumeSkeletonStructure([]domain.VolumeOutline{repaired}); err != nil {
			return nil, err
		}
		volumes, err := t.store.Outline.LoadLayeredOutline()
		if err != nil {
			return nil, fmt.Errorf("load skeleton for volume repair: %w", err)
		}
		found := false
		for i := range volumes {
			if volumes[i].Index != a.Volume {
				continue
			}
			oldCount := domain.TotalChapters([]domain.VolumeOutline{volumes[i]})
			newCount := domain.TotalChapters([]domain.VolumeOutline{repaired})
			if oldCount != newCount {
				return nil, fmt.Errorf("repair_volume V%d must keep %d estimated chapters, got %d: %w", a.Volume, oldCount, newCount, errs.ErrToolPrecondition)
			}
			volumes[i] = repaired
			found = true
			break
		}
		if !found {
			return nil, fmt.Errorf("repair_volume V%d not found: %w", a.Volume, errs.ErrToolPrecondition)
		}
		if foundationOwner != nil {
			if err := t.store.RepairSkeletonVolumeForFoundationRevision(foundationOwner, repaired); err != nil {
				return nil, fmt.Errorf("save Foundation-owned repaired volume skeleton: %w", err)
			}
		} else {
			if err := t.store.Outline.SaveLayeredOutline(volumes); err != nil {
				return nil, fmt.Errorf("save repaired volume skeleton: %w", err)
			}
			if err := t.store.Outline.SaveOutline(domain.FlattenOutline(volumes)); err != nil {
				return nil, fmt.Errorf("save repaired flattened outline: %w", err)
			}
		}
		if err := t.store.OriginalPlanningAudits.InvalidateSkeletonRepair(a.Volume); err != nil {
			return nil, fmt.Errorf("invalidate repaired skeleton audits: %w", err)
		}
		result["volume"] = a.Volume
		result["volume_repaired"] = true
		if err := t.refreshWordBudgetPlan(result); err != nil {
			return nil, err
		}

	case "complete_book":
		// 全书完结的唯一入口：直接推 Phase=Complete。
		// 仅 Writing 阶段允许，防止规划阶段误调跳过整本写作。
		// 拒绝有返工队列时调用——保证 PendingRewrites 跑完才能结束。
		progress, perr := t.store.Progress.Load()
		if perr != nil {
			return nil, fmt.Errorf("load progress: %w: %w", errs.ErrStoreRead, perr)
		}
		if progress == nil {
			return nil, fmt.Errorf("progress 未初始化: %w", errs.ErrToolPrecondition)
		}
		if progress.Phase != domain.PhaseWriting {
			return nil, fmt.Errorf("complete_book 仅在 writing 阶段可调用（当前 phase=%s）: %w", progress.Phase, errs.ErrToolPrecondition)
		}
		if len(progress.PendingRewrites) > 0 {
			return nil, fmt.Errorf("还有 %d 章在返工队列中，处理完再调 complete_book: %w", len(progress.PendingRewrites), errs.ErrToolPrecondition)
		}
		if t.completionGate != nil {
			audit, auditErr := t.completionGate.EvaluateCompletion()
			if auditErr != nil {
				_ = t.store.Progress.SetCompletionAudit("error", "")
				return nil, fmt.Errorf("run completion audit: %w: %w", errs.ErrToolPrecondition, auditErr)
			}
			_ = t.store.Progress.SetCompletionAudit(audit.Status, audit.ReportDigest)
			result["completion_audit"] = audit
			if !audit.Allowed {
				// This is a successful tool outcome rather than an error so the flow
				// boundary is observed and Router can stop redispatching complete_book.
				result["book_complete"] = false
				result["phase"] = string(progress.Phase)
				result["blocked"] = true
				return json.Marshal(result)
			}
		}
		if err := t.store.Progress.MarkComplete(); err != nil {
			return nil, fmt.Errorf("mark complete: %w: %w", errs.ErrStoreWrite, err)
		}
		result["book_complete"] = true
		result["phase"] = string(domain.PhaseComplete)

	case "update_compass":
		var compass domain.StoryCompass
		if err := decode("compass", &compass); err != nil {
			return nil, err
		}
		// 工具层强制覆盖 LastUpdated 为当前已完成章节数，不信任 LLM 自填。
		// LLM 通常忘填或留 0，会让 diag.CompassDrift 误报、Router 路由失真。
		if p, _ := t.store.Progress.Load(); p != nil {
			compass.LastUpdated = p.LatestCompleted()
		}
		if err := t.store.Outline.SaveCompass(compass); err != nil {
			return nil, fmt.Errorf("save compass: %w: %w", errs.ErrStoreWrite, err)
		}
		result["ending_direction"] = compass.EndingDirection
		result["last_updated"] = compass.LastUpdated

	default:
		return nil, fmt.Errorf("unknown type %q, expected premise/outline/layered_outline/characters/planned_relationships/world_rules/expand_arc/repair_arc/repair_volume/append_volume/update_compass/complete_book: %w", a.Type, errs.ErrToolArgs)
	}

	// checkpoint
	scope := domain.GlobalScope()
	if a.Type == "expand_arc" || a.Type == "repair_arc" {
		scope = domain.ArcScope(a.Volume, a.Arc)
	} else if a.Type == "append_volume" || a.Type == "repair_volume" {
		scope = domain.GlobalScope()
	}
	if _, err := t.store.Checkpoints.AppendArtifact(scope, a.Type, foundationArtifact(a.Type)); err != nil {
		return nil, fmt.Errorf("checkpoint foundation %s: %w: %w", a.Type, errs.ErrStoreWrite, err)
	}
	if generationReview != nil {
		result["foundation_generation"] = generationReview.FoundationGeneration
		result["foundation_base_revision"] = generationReview.FoundationBaseRevision
		result["foundation_review_status"] = generationReview.FoundationStatus
		result["foundation_revision"] = generationReview.FoundationRevision
		result["foundation_audit_signature"] = generationReview.FoundationAuditSignature
		if generationReview.FoundationStatus == domain.FoundationReviewStatusPending {
			result["planning_review"] = generationReview.Status
			result["planning_review_kind"] = generationReview.Kind
			result["foundation_ready"] = true
		}
	}

	// 返回剩余未完成项，引导 Architect 继续或结束；
	// 齐全时一次性把 phase 推进到 writing，避免 Coordinator 再回来派单。
	remaining := t.store.FoundationMissing()
	ready := len(remaining) == 0
	result["remaining"] = remaining
	result["foundation_ready"] = ready
	if review, _ := t.store.RunMeta.PlanningReview(); review != nil &&
		(review.Status == domain.PlanningReviewStatusCollecting || review.Status == domain.PlanningReviewStatusPending) {
		reviewReady := false
		nextKind := review.Kind
		switch review.Kind {
		case domain.PlanningReviewKindFoundation:
			// The fenced section mutation owns the candidate-complete transition.
			reviewReady = false
		case domain.PlanningReviewKindBlueprint:
			if t.requiresLayeredPlanning() {
				var reviewErr error
				reviewReady, reviewErr = t.longVolumeSkeletonReady()
				if reviewErr != nil {
					return nil, reviewErr
				}
				if reviewReady {
					// The semantic skeleton audit owns the transition to the user
					// volume gate. Numeric coverage alone is never user-ready.
					reviewReady = false
					result["continue_planning"] = true
					result["foundation_ready"] = false
				}
			} else {
				reviewReady = ready
				nextKind = domain.PlanningReviewKindChapterOutline
			}
		case domain.PlanningReviewKindVolumeSplit:
			// The independent original-fiction audit router owns this transition.
			// Even when all arcs are expanded, arc/volume/book batch gates must pass
			// before chapter_outline can become pending.
			reviewReady = false
			if review.Status == domain.PlanningReviewStatusCollecting {
				result["continue_planning"] = true
				result["foundation_ready"] = false
			}
		case domain.PlanningReviewKindChapterOutline:
			reviewReady = ready
		}
		if reviewReady {
			now := time.Now().UTC().Format(time.RFC3339)
			review.Status = domain.PlanningReviewStatusPending
			review.Kind = nextKind
			if review.CreatedAt == "" {
				review.CreatedAt = now
			}
			review.UpdatedAt = now
			if err := t.store.RunMeta.SetPlanningReview(review); err != nil {
				return nil, fmt.Errorf("set planning review: %w: %w", errs.ErrStoreWrite, err)
			}
			result["planning_review"] = review.Status
			result["planning_review_kind"] = review.Kind
			result["foundation_ready"] = true
		}
	} else if ready {
		if p, _ := t.store.Progress.Load(); p != nil {
			if p.Phase != domain.PhaseWriting && p.Phase != domain.PhaseComplete {
				_ = t.store.Progress.UpdatePhase(domain.PhaseWriting)
				p.Phase = domain.PhaseWriting
			}
			if p.Phase == domain.PhaseWriting {
				result["phase"] = string(domain.PhaseWriting)
			}
		}
	}
	return json.Marshal(result)
}

func isFoundationGenerationSection(typeName string) bool {
	switch typeName {
	case "premise", "characters", "planned_relationships", "world_rules":
		return true
	default:
		return false
	}
}

func requiresConfirmedFoundation(typeName string) bool {
	switch typeName {
	case "outline", "layered_outline", "expand_arc", "repair_arc", "repair_volume", "append_volume", "update_compass":
		return true
	default:
		return false
	}
}

func (t *SaveFoundationTool) prepareOutlineCharacters(entries []domain.OutlineEntry) ([]domain.OutlineEntry, error) {
	foundation, err := t.store.Foundation.Load()
	if err != nil {
		return nil, fmt.Errorf("load confirmed StoryFoundation for outline characters: %w", err)
	}
	prepared, err := domain.PrepareOutlineCharactersWithRelationships(
		entries,
		foundation.Characters,
		foundation.Relationships,
	)
	if err != nil {
		if gapErr, ok := err.(*domain.OutlineCharacterGapError); ok {
			payload, _ := json.Marshal(gapErr.Gaps)
			return nil, fmt.Errorf("outline_character_gaps=%s: %w", payload, errs.ErrToolPrecondition)
		}
		return nil, err
	}
	return prepared, nil
}

func (t *SaveFoundationTool) prepareLayeredOutlineCharacters(volumes []domain.VolumeOutline) error {
	for volumeIndex := range volumes {
		for arcIndex := range volumes[volumeIndex].Arcs {
			chapters := volumes[volumeIndex].Arcs[arcIndex].Chapters
			if len(chapters) == 0 {
				continue
			}
			prepared, err := t.prepareOutlineCharacters(chapters)
			if err != nil {
				return err
			}
			volumes[volumeIndex].Arcs[arcIndex].Chapters = prepared
		}
	}
	return nil
}

func (t *SaveFoundationTool) validateCreativeBriefPremise(content string) error {
	brief, err := t.canonicalCreativeBrief()
	if err != nil || brief == "" {
		return err
	}
	if err := validatePremiseAgainstCreativeBrief(content, brief); err != nil {
		return fmt.Errorf("%w: %w", errs.ErrToolPrecondition, err)
	}
	return nil
}

func (t *SaveFoundationTool) validateCreativeBriefCharacters(characters []domain.Character) error {
	brief, err := t.canonicalCreativeBrief()
	if err != nil || brief == "" {
		return err
	}
	if err := validateCharactersAgainstCreativeBrief(characters, brief); err != nil {
		return fmt.Errorf("%w: %w", errs.ErrToolPrecondition, err)
	}
	return nil
}

func (t *SaveFoundationTool) canonicalCreativeBrief() (string, error) {
	if t == nil || t.store == nil {
		return "", nil
	}
	review, err := t.store.RunMeta.PlanningReview()
	if err != nil {
		return "", fmt.Errorf("load canonical co-create brief: %w", err)
	}
	if review == nil {
		return "", nil
	}
	return strings.TrimSpace(review.Brief), nil
}

func blocksDuringActiveRevision(kind string) bool {
	switch strings.TrimSpace(kind) {
	case "outline", "layered_outline", "expand_arc", "repair_arc", "repair_volume", "append_volume", "complete_book", "update_compass":
		return true
	default:
		return false
	}
}

func foundationRepairArtifactID(st *store.Store, kind string, volumeIndex, arcIndex int) (string, error) {
	volumes, err := st.Outline.LoadLayeredOutline()
	if err != nil {
		return "", err
	}
	for _, volume := range volumes {
		if volume.Index != volumeIndex {
			continue
		}
		if kind == "repair_volume" {
			return volume.ID, nil
		}
		for _, arc := range volume.Arcs {
			if arc.Index == arcIndex {
				return arc.ID, nil
			}
		}
	}
	return "", fmt.Errorf("repair target V%d/A%d is missing", volumeIndex, arcIndex)
}

func planningReviewKindForFoundation(st *store.Store) string {
	if st == nil {
		return domain.PlanningReviewKindBlueprint
	}
	if layered, _ := st.Outline.LoadLayeredOutline(); len(layered) > 0 {
		if layeredOutlineArcsExpanded(layered) {
			return domain.PlanningReviewKindChapterOutline
		}
		return domain.PlanningReviewKindVolumeSplit
	}
	return domain.PlanningReviewKindChapterOutline
}

func (t *SaveFoundationTool) collectingLongBlueprint() bool {
	review, _ := t.store.RunMeta.PlanningReview()
	return review != nil && review.Status == domain.PlanningReviewStatusCollecting &&
		review.Kind == domain.PlanningReviewKindBlueprint && t.requiresLayeredPlanning()
}

func (t *SaveFoundationTool) longBlueprintSkeletonLocked() bool {
	if !t.collectingLongBlueprint() {
		return false
	}
	volumes, err := t.store.Outline.LoadLayeredOutline()
	return err == nil && len(volumes) > 0
}

func (t *SaveFoundationTool) longBlueprintArtifactExists(artifactType string) bool {
	switch artifactType {
	case "premise":
		premise, err := t.store.Outline.LoadPremise()
		return err == nil && strings.TrimSpace(premise) != ""
	case "characters":
		characters, err := t.store.Characters.Load()
		return err == nil && len(characters) > 0
	case "world_rules":
		rules, err := t.store.World.LoadWorldRules()
		return err == nil && len(rules) > 0
	case "layered_outline":
		volumes, err := t.store.Outline.LoadLayeredOutline()
		return err == nil && len(volumes) > 0
	default:
		return false
	}
}

func (t *SaveFoundationTool) requiresLayeredPlanning() bool {
	meta, _ := t.store.RunMeta.Load()
	if meta == nil {
		return false
	}
	if meta.PlanningTier == domain.PlanningTierLong {
		return true
	}
	return meta.WordBudget != nil && meta.WordBudget.TargetTotalWords >= 50_000
}

func (t *SaveFoundationTool) validateLongVolumeSkeletonStructure(volumes []domain.VolumeOutline) error {
	if len(volumes) == 0 {
		return fmt.Errorf("long volume skeleton must contain at least one volume: %w", errs.ErrToolPrecondition)
	}
	for _, volume := range volumes {
		if strings.TrimSpace(volume.Title) == "" || strings.TrimSpace(volume.Theme) == "" || len(volume.Arcs) == 0 {
			return fmt.Errorf("volume %d needs a title, conflict/theme, and at least one story arc: %w", volume.Index, errs.ErrToolPrecondition)
		}
		if len(volume.Arcs) < 2 || len(volume.Arcs) > 3 {
			return fmt.Errorf("volume %d must contain 2-3 purposeful arcs so audit and pacing remain bounded, got %d: %w", volume.Index, len(volume.Arcs), errs.ErrToolPrecondition)
		}
		for _, arc := range volume.Arcs {
			if len(arc.Chapters) > 0 {
				return fmt.Errorf("initial long proposal must be skeleton-only; volume %d arc %d already contains detailed chapters: %w", volume.Index, arc.Index, errs.ErrToolPrecondition)
			}
			if strings.TrimSpace(arc.Title) == "" || strings.TrimSpace(arc.Goal) == "" {
				return fmt.Errorf("volume %d arc %d needs a distinct title and causal goal: %w", volume.Index, arc.Index, errs.ErrToolPrecondition)
			}
			if arc.EstimatedChapters < 3 || arc.EstimatedChapters > 4 {
				return fmt.Errorf("volume %d arc %d must reserve 3-4 chapters for one detailed-outline and audit batch, got %d: %w", volume.Index, arc.Index, arc.EstimatedChapters, errs.ErrToolPrecondition)
			}
		}
	}
	return nil
}

func (t *SaveFoundationTool) longVolumeSkeletonCoverage(volumes []domain.VolumeOutline) (bool, error) {
	if err := t.validateLongVolumeSkeletonStructure(volumes); err != nil {
		return false, err
	}
	meta, _ := t.store.RunMeta.Load()
	targetWords := 0
	if meta != nil && meta.WordBudget != nil {
		targetWords = meta.WordBudget.TargetTotalWords
	}
	if targetWords <= 0 {
		return len(volumes) >= 2, nil
	}
	total := domain.TotalChapters(volumes)
	minimum := (targetWords + 4_999) / 5_000
	maximum := targetWords / 3_000
	if maximum < minimum {
		maximum = minimum
	}
	minimumVolumes := (targetWords + 39_999) / 40_000
	maximumVolumes := (targetWords + 19_999) / 20_000
	if len(volumes) > maximumVolumes || total > maximum {
		return false, fmt.Errorf(
			"planned %d volumes/%d chapters exceeds the %d-word budget; expected %d-%d volumes and %d-%d chapters: %w",
			len(volumes), total, targetWords, minimumVolumes, maximumVolumes, minimum, maximum, errs.ErrToolPrecondition,
		)
	}
	return len(volumes) >= minimumVolumes && total >= minimum, nil
}

func (t *SaveFoundationTool) longVolumeSkeletonReady() (bool, error) {
	for _, missing := range t.store.FoundationMissing() {
		if missing != "outline" {
			return false, nil
		}
	}
	volumes, err := t.store.Outline.LoadLayeredOutline()
	if err != nil || len(volumes) == 0 {
		return false, err
	}
	return t.longVolumeSkeletonCoverage(volumes)
}

func allLayeredArcsExpanded(st *store.Store) bool {
	if st == nil {
		return false
	}
	volumes, err := st.Outline.LoadLayeredOutline()
	return err == nil && layeredOutlineArcsExpanded(volumes)
}

func layeredOutlineArcsExpanded(volumes []domain.VolumeOutline) bool {
	if len(volumes) == 0 {
		return false
	}
	for _, volume := range volumes {
		if len(volume.Arcs) == 0 {
			return false
		}
		for _, arc := range volume.Arcs {
			if len(arc.Chapters) == 0 {
				return false
			}
		}
	}
	return true
}

func foundationArtifact(t string) string {
	switch t {
	case "premise":
		return "premise.md"
	case "outline":
		return "outline.json"
	case "layered_outline", "expand_arc", "repair_arc", "repair_volume", "append_volume":
		return "layered_outline.json"
	case "complete_book":
		return "meta/progress.json"
	case "characters":
		return "characters.json"
	case "planned_relationships":
		return "planned_relationships.json"
	case "world_rules":
		return "world_rules.json"
	case "update_compass":
		return "meta/compass.json"
	default:
		return ""
	}
}

// decodeFoundationJSON 解析 save_foundation 的 content 字段，失败时附上行列位置
// 和最常见的修复提示，让 LLM 下一次重试能直接定位而不是盲猜。
func decodeFoundationJSON(typeName, content string, out any) error {
	err := json.Unmarshal([]byte(content), out)
	if err == nil {
		return nil
	}
	hint := `常见原因：字符串值中的双引号未转义为 \", 换行未转义为 \n, 或对象字段间漏了逗号。请整段重新生成一次。`
	if se, ok := err.(*json.SyntaxError); ok {
		line, col := offsetToLineCol(content, int(se.Offset))
		return fmt.Errorf("parse %s JSON (line %d col %d): %w — %s", typeName, line, col, err, hint)
	}
	return fmt.Errorf("parse %s JSON: %w — %s", typeName, err, hint)
}

func decodeWorldRules(content string) ([]domain.WorldRule, error) {
	var rules []domain.WorldRule
	if err := json.Unmarshal([]byte(content), &rules); err == nil {
		inferLegacyWorldRuleStrengths(rules)
		return validateDecodedWorldRules(rules)
	}
	var grouped struct {
		HardRules []domain.WorldRule `json:"hard_rules"`
		SoftRules []domain.WorldRule `json:"soft_rules"`
	}
	if err := json.Unmarshal([]byte(content), &grouped); err != nil {
		return nil, fmt.Errorf(
			"parse world_rules JSON: content must be a WorldRule array or {\"hard_rules\":[WorldRule...],\"soft_rules\":[WorldRule...]}; each rule uses id/category/title/rule/boundary/strength/priority/tags with non-empty rule and boundary; do not send custom setting/reality_rules/narrative_rules/conflict_rules/content_boundaries fields: %v: %w",
			err,
			errs.ErrToolArgs,
		)
	}
	if len(grouped.HardRules) == 0 && len(grouped.SoftRules) == 0 {
		return nil, fmt.Errorf(
			"parse world_rules JSON: content must be a WorldRule array or {\"hard_rules\":[WorldRule...],\"soft_rules\":[WorldRule...]}; each rule uses id/category/title/rule/boundary/strength/priority/tags with non-empty rule and boundary; do not send custom setting/reality_rules/narrative_rules/conflict_rules/content_boundaries fields: %w",
			errs.ErrToolArgs,
		)
	}
	for idx := range grouped.HardRules {
		grouped.HardRules[idx].Strength = domain.WorldRuleStrengthHard
	}
	for idx := range grouped.SoftRules {
		grouped.SoftRules[idx].Strength = domain.WorldRuleStrengthSoft
	}
	rules = append(rules, grouped.HardRules...)
	rules = append(rules, grouped.SoftRules...)
	return validateDecodedWorldRules(rules)
}

func decodeFoundationGenerationWorldRules(content string) ([]domain.WorldRule, error) {
	var grouped struct {
		HardRules []domain.WorldRule `json:"hard_rules"`
		SoftRules []domain.WorldRule `json:"soft_rules"`
	}
	if !json.Valid([]byte(content)) {
		return nil, fmt.Errorf(
			"parse Foundation generation world_rules JSON: content must be one valid JSON object: %w",
			errs.ErrToolArgs,
		)
	}
	decoder := json.NewDecoder(strings.NewReader(content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&grouped); err != nil {
		return nil, fmt.Errorf(
			"parse Foundation generation world_rules JSON: content must be exactly {\"hard_rules\":[WorldRule...],\"soft_rules\":[WorldRule...]}, without custom top-level fields: %v: %w",
			err,
			errs.ErrToolArgs,
		)
	}
	if len(grouped.HardRules) == 0 || len(grouped.SoftRules) == 0 {
		return nil, fmt.Errorf(
			"parse Foundation generation world_rules JSON: hard_rules and soft_rules must each contain at least one WorldRule: %w",
			errs.ErrToolArgs,
		)
	}
	for index := range grouped.HardRules {
		grouped.HardRules[index].Strength = domain.WorldRuleStrengthHard
	}
	for index := range grouped.SoftRules {
		grouped.SoftRules[index].Strength = domain.WorldRuleStrengthSoft
	}
	rules := append(grouped.HardRules, grouped.SoftRules...)
	return validateDecodedWorldRules(rules)
}

func validateDecodedWorldRules(rules []domain.WorldRule) ([]domain.WorldRule, error) {
	for index, rule := range rules {
		if strings.TrimSpace(rule.Rule) == "" {
			return nil, fmt.Errorf("parse world_rules JSON: WorldRule %d requires non-empty rule: %w", index, errs.ErrToolArgs)
		}
		if strings.TrimSpace(rule.Boundary) == "" {
			return nil, fmt.Errorf("parse world_rules JSON: WorldRule %d requires non-empty boundary: %w", index, errs.ErrToolArgs)
		}
	}
	return rules, nil
}

func foundationGenerationSaveError(section string, err error) error {
	base := fmt.Errorf("save %s: %w: %w", section, errs.ErrStoreWrite, err)
	var reviewErr *store.FoundationReviewError
	if !errors.As(err, &reviewErr) || reviewErr.Review == nil {
		return base
	}
	review := reviewErr.Review
	return fmt.Errorf(
		"%w; authoritative retry contract: keep type=%s and use foundation_generation=%d, foundation_base_revision=%d exactly; do not switch section types or substitute the current canonical Foundation revision",
		base,
		section,
		review.FoundationGeneration,
		review.FoundationBaseRevision,
	)
}

func decodeLayeredOutline(content string) ([]domain.VolumeOutline, error) {
	var volumes []domain.VolumeOutline
	if err := json.Unmarshal([]byte(content), &volumes); err == nil {
		return volumes, nil
	}
	var grouped struct {
		Volumes []domain.VolumeOutline `json:"volumes"`
		Volume  *domain.VolumeOutline  `json:"volume"`
	}
	if err := json.Unmarshal([]byte(content), &grouped); err == nil {
		if len(grouped.Volumes) > 0 {
			return grouped.Volumes, nil
		}
		if grouped.Volume != nil {
			return []domain.VolumeOutline{*grouped.Volume}, nil
		}
	}
	var single domain.VolumeOutline
	if err := json.Unmarshal([]byte(content), &single); err == nil &&
		(single.Index > 0 || strings.TrimSpace(single.ID) != "" || strings.TrimSpace(single.Title) != "") {
		return []domain.VolumeOutline{single}, nil
	}
	return nil, decodeFoundationJSON("layered_outline", content, &volumes)
}

// inferLegacyWorldRuleStrengths keeps the array form compatible with prompts
// that identify rule groups by their stable hr_/sr_ IDs but omit strength.
// Unknown IDs retain the historical hard default during Foundation
// normalization; explicit strength always wins.
func inferLegacyWorldRuleStrengths(rules []domain.WorldRule) {
	for idx := range rules {
		if rules[idx].Strength != "" {
			continue
		}
		switch {
		case strings.HasPrefix(strings.ToLower(strings.TrimSpace(rules[idx].ID)), "sr_"):
			rules[idx].Strength = domain.WorldRuleStrengthSoft
		case strings.HasPrefix(strings.ToLower(strings.TrimSpace(rules[idx].ID)), "hr_"):
			rules[idx].Strength = domain.WorldRuleStrengthHard
		}
	}
}

func offsetToLineCol(s string, offset int) (int, int) {
	if offset < 0 {
		offset = 0
	}
	if offset > len(s) {
		offset = len(s)
	}
	line, col := 1, 1
	for i := 0; i < offset; i++ {
		if s[i] == '\n' {
			line++
			col = 1
		} else {
			col++
		}
	}
	return line, col
}

func normalizeFoundationContent(raw json.RawMessage) (string, error) {
	if len(raw) == 0 {
		return "", fmt.Errorf("content is required: %w", errs.ErrToolArgs)
	}

	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text, nil
	}

	if !json.Valid(raw) {
		return "", fmt.Errorf("invalid content: expected Markdown string or valid JSON value: %w", errs.ErrToolArgs)
	}
	return string(raw), nil
}

func (t *SaveFoundationTool) inferFoundationType(content string) (string, error) {
	content = strings.TrimSpace(content)
	missing := t.store.FoundationMissing()
	if len(missing) == 1 {
		if inferred := foundationTypeFromMissing(missing[0]); inferred != "" {
			return inferred, nil
		}
	}

	if looksLikePremiseMarkdown(content) && foundationMissingAllows(missing, "premise") {
		return "premise", nil
	}
	if inferred := inferFoundationTypeFromJSON(content); inferred != "" {
		if len(missing) == 0 || foundationMissingAllows(missing, inferred) {
			return inferred, nil
		}
	}
	if looksLikePremiseMarkdown(content) && len(missing) == 0 {
		return "premise", nil
	}

	if len(missing) > 0 {
		return "", fmt.Errorf("save_foundation requires type because content is ambiguous; current missing foundation=%s: %w", strings.Join(missing, ","), errs.ErrToolArgs)
	}
	return "", fmt.Errorf("save_foundation requires type because foundation is already complete and content is ambiguous: %w", errs.ErrToolArgs)
}

func foundationTypeFromMissing(missing string) string {
	switch missing {
	case "premise", "outline", "characters", "world_rules":
		return missing
	case "compass":
		return "update_compass"
	default:
		return ""
	}
}

func foundationMissingAllows(missing []string, typeName string) bool {
	if len(missing) == 0 {
		return false
	}
	missingName := typeName
	if typeName == "update_compass" {
		missingName = "compass"
	}
	for _, item := range missing {
		if item == missingName {
			return true
		}
	}
	return false
}

func looksLikePremiseMarkdown(content string) bool {
	if content == "" || strings.HasPrefix(content, "{") || strings.HasPrefix(content, "[") {
		return false
	}
	return strings.HasPrefix(content, "#") ||
		strings.Contains(content, "\n## ") ||
		strings.Contains(content, "\n# ")
}

func inferFoundationTypeFromJSON(content string) string {
	var value any
	if err := json.Unmarshal([]byte(content), &value); err != nil {
		return ""
	}
	switch v := value.(type) {
	case []any:
		if len(v) == 0 {
			return ""
		}
		first, _ := v[0].(map[string]any)
		return inferFoundationTypeFromObject(first, true)
	case map[string]any:
		return inferFoundationTypeFromObject(v, false)
	default:
		return ""
	}
}

func inferFoundationTypeFromObject(obj map[string]any, fromArray bool) string {
	if len(obj) == 0 {
		return ""
	}
	if _, ok := obj["ending_direction"]; ok {
		return "update_compass"
	}
	if hasAnyKey(obj, "chapter", "core_event", "hook", "scenes") {
		return "outline"
	}
	if hasAnyKey(obj, "name", "role", "description", "arc", "traits", "aliases") {
		return "characters"
	}
	if hasAnyKey(obj, "source_character_id", "target_character_id", "direction", "status") {
		return "planned_relationships"
	}
	if hasAnyKey(obj, "category", "rule", "boundary") {
		return "world_rules"
	}
	if fromArray && hasAnyKey(obj, "index", "arcs") {
		return "layered_outline"
	}
	return ""
}

func hasAnyKey(obj map[string]any, keys ...string) bool {
	for _, key := range keys {
		if _, ok := obj[key]; ok {
			return true
		}
	}
	return false
}

func (t *SaveFoundationTool) isWriting() bool {
	p, _ := t.store.Progress.Load()
	return p != nil && p.Phase == domain.PhaseWriting
}

func normalizeOutlineEntryChapters(entries []domain.OutlineEntry) {
	for i := range entries {
		if entries[i].Chapter <= 0 {
			entries[i].Chapter = i + 1
		}
	}
}

func validateGeneratedOutline(typeName string, entries []domain.OutlineEntry, reviews []outlineSimilarityReviewVerdict) error {
	if duplicate, ok := domain.FindDuplicateOutlineEntries(entries); ok {
		return fmt.Errorf("%s contains duplicate chapter outline: %w", typeName, duplicate)
	}
	return validateOutlineSimilarityReview(typeName, domain.FindOutlineSimilarityReviewCandidates(entries), reviews)
}

func validateGeneratedLayeredOutline(volumes []domain.VolumeOutline, reviews []outlineSimilarityReviewVerdict) error {
	chapter := 1
	for _, volume := range volumes {
		for _, arc := range volume.Arcs {
			chapterCount := len(arc.Chapters)
			if chapterCount == 0 {
				if arc.EstimatedChapters > 0 {
					chapter += arc.EstimatedChapters
				}
				continue
			}
			entries := make([]domain.OutlineEntry, len(arc.Chapters))
			for i := range arc.Chapters {
				entries[i] = arc.Chapters[i]
				entries[i].Chapter = chapter + i
			}
			if err := validateGeneratedOutline(
				fmt.Sprintf("layered_outline V%d A%d", volume.Index, arc.Index),
				entries,
				reviews,
			); err != nil {
				return err
			}
			chapter += chapterCount
		}
	}
	return nil
}

func validateOutlineSimilarityReview(typeName string, candidates []domain.OutlineSimilarityCandidate, reviews []outlineSimilarityReviewVerdict) error {
	if len(candidates) == 0 {
		return nil
	}
	reviewByPair := outlineSimilarityReviewByPair(reviews)
	missing := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		review, ok := reviewByPair[outlineSimilarityPairKey(candidate.ExistingChapter, candidate.Chapter)]
		if !ok {
			missing = append(missing, fmt.Sprintf(
				"chapter %d vs %d (detail=%.3f full=%.3f)",
				candidate.Chapter,
				candidate.ExistingChapter,
				candidate.DetailSimilarity,
				candidate.FullSimilarity,
			))
			continue
		}
		if review.Duplicate {
			reason := strings.TrimSpace(review.Reason)
			if reason == "" {
				reason = "model review judged the outlines as duplicate"
			}
			return fmt.Errorf(
				"%s model review confirmed duplicate outline: chapter %d vs %d: %s: %w",
				typeName,
				candidate.Chapter,
				candidate.ExistingChapter,
				reason,
				errs.ErrToolPrecondition,
			)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf(
			"%s has borderline similar outline pairs requiring model review before save: %s. Resubmit with similarity_review verdicts after judging whether each pair is duplicate: %w",
			typeName,
			strings.Join(missing, "; "),
			errs.ErrToolPrecondition,
		)
	}
	return nil
}

func outlineSimilarityReviewByPair(reviews []outlineSimilarityReviewVerdict) map[string]outlineSimilarityReviewVerdict {
	out := make(map[string]outlineSimilarityReviewVerdict, len(reviews))
	for _, review := range reviews {
		out[outlineSimilarityPairKey(review.ExistingChapter, review.Chapter)] = review
	}
	return out
}

func outlineSimilarityPairKey(existingChapter, chapter int) string {
	return fmt.Sprintf("%d:%d", existingChapter, chapter)
}

func (t *SaveFoundationTool) refreshWordBudgetPlan(result map[string]any) error {
	progress, err := t.store.Progress.Load()
	if err != nil {
		return fmt.Errorf("load progress for word budget: %w: %w", errs.ErrStoreRead, err)
	}
	if progress == nil {
		return nil
	}
	return t.updateWordBudgetPlan(progress.TotalChapters, result)
}

func (t *SaveFoundationTool) updateWordBudgetPlan(chapters int, result map[string]any) error {
	if chapters <= 0 {
		return nil
	}
	meta, err := t.store.RunMeta.Load()
	if err != nil {
		return fmt.Errorf("load run meta for word budget: %w: %w", errs.ErrStoreRead, err)
	}
	if meta == nil || meta.WordBudget == nil || meta.WordBudget.TargetTotalWords <= 0 {
		return nil
	}
	next := meta.WordBudget.WithPlannedChapters(chapters)
	if err := t.store.RunMeta.SetWordBudget(&next); err != nil {
		return fmt.Errorf("save word budget plan: %w: %w", errs.ErrStoreWrite, err)
	}
	result["word_budget"] = next
	return nil
}
