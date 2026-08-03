package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode"

	"github.com/voocel/agentcore"
	"github.com/voocel/agentcore/schema"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/errs"
	"github.com/voocel/ainovel-cli/internal/store"
)

// CheckConsistencyTool records that Writer reviewed the current draft against
// its already-loaded chapter contract and continuity evidence. It deliberately
// returns a receipt instead of echoing the draft and global state back into the
// same model turn: novel_context + read_chapter are the authoritative inputs.
type CheckConsistencyTool struct {
	store             *store.Store
	continuityAuditor agentcore.ChatModel
}

type consistencySceneCheck struct {
	Scene             int    `json:"scene"`
	Evidence          string `json:"evidence"`
	TimeAndPlaceMatch bool   `json:"time_and_place_match"`
	POVMatch          bool   `json:"pov_match"`
	CharactersMatch   bool   `json:"characters_match"`
	EventOrderMatch   bool   `json:"event_order_match"`
	KnowledgeMatch    bool   `json:"knowledge_match"`
	IrreversibleMatch bool   `json:"irreversible_result_match"`
}

func NewCheckConsistencyTool(store *store.Store, continuityAuditors ...agentcore.ChatModel) *CheckConsistencyTool {
	var auditor agentcore.ChatModel
	if len(continuityAuditors) > 0 {
		auditor = continuityAuditors[0]
	}
	return &CheckConsistencyTool{store: store, continuityAuditor: auditor}
}

func (t *CheckConsistencyTool) Name() string { return "check_consistency" }
func (t *CheckConsistencyTool) Description() string {
	return "记录当前草稿已按 novel_context 的章节契约与连续性证据完成一致性审核。必须先 novel_context(chapter=N) 并 read_chapter；每个计划场景都必须提交可在草稿中精确找到的原文 evidence，并逐项核对时间地点、POV、人物身份与性别代词、事件顺序、信息边界和不可逆结果。还必须对照 recent_summaries：人物不得无触发地重复询问已经获知的事实，空间移动必须成立。只有全部核对无矛盾时 findings 才能为空"
}
func (t *CheckConsistencyTool) Label() string { return "一致性检查" }

// 只读工具（仅追加 checkpoint 事件，不改状态），可被并发调度。
func (t *CheckConsistencyTool) ReadOnly(_ json.RawMessage) bool        { return true }
func (t *CheckConsistencyTool) ConcurrencySafe(_ json.RawMessage) bool { return true }

func (t *CheckConsistencyTool) Schema() map[string]any {
	sceneCheckSchema := schema.Object(
		schema.Property("scene", schema.Int("章节契约中的场景序号，从 1 开始")).Required(),
		schema.Property("evidence", schema.String(`当前草稿中可精确检索的原文短句；不是概述。若计划场景完全缺失，必须填固定值 "MISSING_FROM_DRAFT"，把对应 match 字段设为 false，并添加同场景的 critical/error finding`)).Required(),
		schema.Property("time_and_place_match", schema.Bool("时间与命名地点是否符合该场景契约")).Required(),
		schema.Property("pov_match", schema.Bool("POV 是否符合该场景契约")).Required(),
		schema.Property("characters_match", schema.Bool("参与人物及其身份是否符合该场景契约")).Required(),
		schema.Property("event_order_match", schema.Bool("关键事件与先后顺序是否符合该场景契约")).Required(),
		schema.Property("knowledge_match", schema.Bool("人物知情边界是否符合该场景契约")).Required(),
		schema.Property("irreversible_result_match", schema.Bool("该场景承担的不可逆结果或交接是否落地")).Required(),
	)
	findingSchema := schema.Object(
		schema.Property("type", schema.Enum("character finding type", "ooc", "voice_drift", "motivation_break", "knowledge_leak", "relationship_jump", "arc_beat_miss", "supporting_character_flat", "static_dynamic_conflict", "continuity_repeat", "identity_pronoun_drift", "spatial_contradiction", "adaptation_source_confusion")).Required(),
		schema.Property("severity", schema.Enum("severity", "critical", "error", "warning")).Required(),
		schema.Property("character_id", schema.String("stable character ID")).Required(),
		schema.Property("scene", schema.String("chapter or scene locator")).Required(),
		schema.Property("evidence", schema.String("concise draft evidence")).Required(),
		schema.Property("violated_field", schema.String("character card, knowledge boundary, outline beat, or chapter contract field")).Required(),
		schema.Property("description", schema.String("what is inconsistent")).Required(),
		schema.Property("suggestion", schema.String("executable repair instruction")).Required(),
	)
	sceneChecksSchema := schema.Array("逐场景、以当前草稿原文为证据的契约核对；数量必须等于章节细纲场景数", sceneCheckSchema)
	if sceneCount, contracts := t.activeSceneContracts(); sceneCount > 0 {
		sceneChecksSchema["minItems"] = sceneCount
		sceneChecksSchema["maxItems"] = sceneCount
		sceneChecksSchema["description"] = fmt.Sprintf(
			"当前活动章节必须且只能提交 %d 项，scene 依次为 1-%d；每项对应一个计划场景，不是正文小节。计划场景：%s",
			sceneCount, sceneCount, contracts,
		)
	}
	return schema.Object(
		schema.Property("chapter", schema.Int("要检查的章节号")).Required(),
		schema.Property("scene_checks", sceneChecksSchema).Required(),
		schema.Property("findings", schema.Array("structured character and continuity findings; [] means no finding", findingSchema)).Required(),
	)
}

func (t *CheckConsistencyTool) activeSceneContracts() (int, string) {
	if t == nil || t.store == nil || t.store.Progress == nil || t.store.Outline == nil {
		return 0, ""
	}
	progress, err := t.store.Progress.Load()
	if err != nil || progress == nil {
		return 0, ""
	}
	chapter := progress.InProgressChapter
	if chapter <= 0 {
		chapter = progress.CurrentChapter
	}
	if chapter <= 0 {
		return 0, ""
	}
	outline, err := t.store.Outline.GetChapterOutline(chapter)
	if err != nil || outline == nil || len(outline.Scenes) == 0 {
		return 0, ""
	}
	return len(outline.Scenes), compactIndexedSceneContracts(outline.Scenes)
}

func (t *CheckConsistencyTool) Execute(ctx context.Context, args json.RawMessage) (json.RawMessage, error) {
	var a struct {
		Chapter     int                       `json:"chapter"`
		SceneChecks []consistencySceneCheck   `json:"scene_checks"`
		Findings    []domain.ConsistencyIssue `json:"findings"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return nil, fmt.Errorf("invalid args: %w: %w", errs.ErrToolArgs, err)
	}
	if a.Chapter <= 0 {
		return nil, fmt.Errorf("chapter must be > 0: %w", errs.ErrToolArgs)
	}

	content, wordCount, err := t.store.Drafts.LoadChapterContent(a.Chapter)
	if err != nil {
		return nil, fmt.Errorf("load chapter content: %w: %w", errs.ErrStoreRead, err)
	}
	if content == "" {
		return nil, fmt.Errorf("no content found for chapter %d: %w", a.Chapter, errs.ErrToolPrecondition)
	}
	if err := t.validateCharacterFindings(a.Chapter, a.Findings); err != nil {
		return nil, err
	}
	if err := t.validateSceneChecks(a.Chapter, content, a.SceneChecks, a.Findings); err != nil {
		return nil, err
	}
	independentFindings, err := t.auditCrossChapterContinuity(ctx, a.Chapter, content)
	if err != nil {
		return nil, fmt.Errorf("independent cross-chapter continuity audit: %w", err)
	}
	a.Findings = mergeConsistencyFindings(a.Findings, independentFindings)
	blocking := false
	for _, finding := range a.Findings {
		if finding.Severity == "critical" || finding.Severity == "error" {
			blocking = true
			break
		}
	}
	result := map[string]any{
		"chapter":      a.Chapter,
		"word_count":   wordCount,
		"draft_sha256": store.TextSHA256(content),
		"reviewed":     !blocking,
		"passed":       !blocking,
		"scene_checks": a.SceneChecks,
		"findings":     a.Findings,
		"review_against": []string{
			"novel_context.working_memory.chapter_contract",
			"novel_context.episodic_memory",
			"independent reviewer comparison with recent summaries and prior prose",
			"the current read_chapter draft",
		},
		"next_step": "If the comparison found a contradiction, edit the draft and rerun all checks; otherwise continue the same-draft validation sequence.",
	}
	if t.store.Consistency != nil {
		if err := t.store.Consistency.SaveAudit(store.ConsistencyAudit{
			Chapter:     a.Chapter,
			DraftSHA256: store.TextSHA256(content),
			Passed:      !blocking,
			Findings:    a.Findings,
		}); err != nil {
			return nil, fmt.Errorf("persist consistency audit: %w", err)
		}
	}
	if blocking {
		result["blocking"] = true
		result["next_step"] = "Apply every critical/error repair instruction to the current draft, then rerun check_consistency and all same-draft gates. Do not commit."
		return json.Marshal(result)
	}

	if _, err := t.store.Checkpoints.AppendArtifact(
		domain.ChapterScope(a.Chapter), "consistency_check",
		fmt.Sprintf("drafts/%02d.draft.md", a.Chapter),
	); err != nil {
		return nil, fmt.Errorf("checkpoint consistency check: %w", err)
	}

	return json.Marshal(result)
}

func (t *CheckConsistencyTool) validateSceneChecks(
	chapter int,
	content string,
	checks []consistencySceneCheck,
	findings []domain.ConsistencyIssue,
) error {
	outline, err := t.store.Outline.GetChapterOutline(chapter)
	if err != nil {
		// Legacy/isolated tests and repair projects can have a draft before a
		// formal outline exists. Preserve the historical receipt behavior for
		// that narrow case; production layered writing always has an outline.
		return nil
	}
	if outline == nil || len(outline.Scenes) == 0 {
		return nil
	}
	if len(checks) != len(outline.Scenes) {
		return fmt.Errorf(
			"scene_checks count %d does not match chapter %d contract scene count %d; submit exactly one grounded check for each indexed planned scene, not each prose subsection. expected_scene_contracts=%s: %w",
			len(checks), chapter, len(outline.Scenes), compactIndexedSceneContracts(outline.Scenes), errs.ErrToolPrecondition,
		)
	}
	seen := make(map[int]struct{}, len(checks))
	evidenceOffsets := make(map[int]int, len(checks))
	for index, check := range checks {
		if check.Scene < 1 || check.Scene > len(outline.Scenes) {
			return fmt.Errorf("scene_checks[%d].scene %d is outside 1-%d: %w", index, check.Scene, len(outline.Scenes), errs.ErrToolArgs)
		}
		if _, duplicate := seen[check.Scene]; duplicate {
			return fmt.Errorf("scene_checks contains duplicate scene %d: %w", check.Scene, errs.ErrToolArgs)
		}
		seen[check.Scene] = struct{}{}
		evidence := strings.TrimSpace(check.Evidence)
		allMatch := check.TimeAndPlaceMatch && check.POVMatch && check.CharactersMatch &&
			check.EventOrderMatch && check.KnowledgeMatch && check.IrreversibleMatch
		if !allMatch && evidence == "MISSING_FROM_DRAFT" {
			if !hasBlockingSceneFinding(findings, check.Scene) {
				return fmt.Errorf(
					"scene_checks[%d] reports planned scene %d missing; add a critical/error finding with scene=\"scene %d\", violated_field naming the failed chapter-contract field, and an executable repair: %w",
					index, check.Scene, check.Scene, errs.ErrToolPrecondition,
				)
			}
			continue
		}
		evidenceOffset, groundedEvidence, evidenceFound := groundConsistencyEvidence(content, evidence)
		if len([]rune(normalizeConsistencyEvidence(evidence))) < 8 || !evidenceFound {
			if !allMatch {
				return fmt.Errorf(
					"scene_checks[%d] reports a mismatch but evidence is not exact draft text. Quote the nearest conflicting passage, or use MISSING_FROM_DRAFT when planned scene %d is wholly absent and add a blocking finding: %w",
					index, check.Scene, errs.ErrToolPrecondition,
				)
			}
			return fmt.Errorf(
				"scene_checks[%d].evidence is not an exact current-draft quote of at least 8 characters; call read_chapter and quote the draft, never invent or summarize evidence: %w",
				index, errs.ErrToolPrecondition,
			)
		}
		// Models occasionally wrap a valid verbatim anchor in a tiny
		// paraphrased prefix or suffix (for example, replacing a character name
		// with a pronoun). Preserve the strict grounding invariant while avoiding
		// a wasteful retry: retain only the unique verbatim span found in the
		// current draft.
		checks[index].Evidence = groundedEvidence
		evidenceOffsets[check.Scene] = evidenceOffset
		if !allMatch && !hasBlockingSceneFinding(findings, check.Scene) {
			return fmt.Errorf(
				"scene_checks[%d] marks a chapter-contract dimension as failed; add a critical/error finding with scene=\"scene %d\" and repair the draft before recording a passing consistency receipt: %w",
				index, check.Scene, errs.ErrToolPrecondition,
			)
		}
	}
	orderedScenes := make([]int, 0, len(evidenceOffsets))
	for scene := range evidenceOffsets {
		orderedScenes = append(orderedScenes, scene)
	}
	sort.Ints(orderedScenes)
	previousOffset := -1
	for _, scene := range orderedScenes {
		offset := evidenceOffsets[scene]
		if offset <= previousOffset {
			return fmt.Errorf(
				"scene_checks evidence is not in planned scene order at scene %d; quote one representative passage from each planned scene in narrative order instead of mapping prose subsections to new scene numbers: %w",
				scene, errs.ErrToolPrecondition,
			)
		}
		previousOffset = offset
	}
	return nil
}

func findConsistencyEvidence(content, evidence string) (int, bool) {
	offset, _, found := groundConsistencyEvidence(content, evidence)
	return offset, found
}

func groundConsistencyEvidence(content, evidence string) (int, string, bool) {
	if offset := strings.Index(content, evidence); offset >= 0 {
		return offset, evidence, true
	}
	normalizedContent := normalizeConsistencyEvidence(content)
	normalizedEvidence := normalizeConsistencyEvidence(evidence)
	if normalizedEvidence == "" {
		return -1, "", false
	}
	if offset := strings.Index(normalizedContent, normalizedEvidence); offset >= 0 {
		return offset, evidence, true
	}

	// The contract only needs one exact quote of at least eight characters.
	// If a longer submitted phrase contains a unique exact span, canonicalize
	// to that span rather than rejecting the whole call because of a small
	// model-added wrapper. Shorter matches and ambiguous repeated phrases still
	// fail closed.
	runes := []rune(strings.TrimSpace(evidence))
	for width := min(len(runes), 32); width >= 8; width-- {
		for start := 0; start+width <= len(runes); start++ {
			candidate := string(runes[start : start+width])
			offset := strings.Index(content, candidate)
			if offset < 0 || strings.LastIndex(content, candidate) != offset {
				continue
			}
			return offset, candidate, true
		}
	}
	return -1, "", false
}

func normalizeConsistencyEvidence(value string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, value)
}

func hasBlockingSceneFinding(findings []domain.ConsistencyIssue, scene int) bool {
	want := fmt.Sprintf("scene %d", scene)
	for _, finding := range findings {
		severity := strings.ToLower(strings.TrimSpace(finding.Severity))
		if severity != "critical" && severity != "error" {
			continue
		}
		locator := strings.ToLower(strings.TrimSpace(finding.Scene))
		if locator == want || locator == fmt.Sprintf("%d", scene) {
			return true
		}
	}
	return false
}

func compactIndexedSceneContracts(scenes []string) string {
	var result strings.Builder
	for index, scene := range scenes {
		if index > 0 {
			result.WriteString(" | ")
		}
		fmt.Fprintf(&result, "%d:%s", index+1, truncateConsistencyContract(strings.TrimSpace(scene), 96))
	}
	return result.String()
}

func truncateConsistencyContract(value string, limit int) string {
	runes := []rune(value)
	if limit <= 0 || len(runes) <= limit {
		return value
	}
	return string(runes[:limit]) + "…"
}

func (t *CheckConsistencyTool) validateCharacterFindings(chapter int, findings []domain.ConsistencyIssue) error {
	foundation, err := t.store.Foundation.Load()
	if err != nil {
		return fmt.Errorf("load StoryFoundation for consistency findings: %w", err)
	}
	ids := make(map[string]struct{}, len(foundation.Characters))
	characterIDByName := make(map[string]string, len(foundation.Characters))
	validIDs := make([]string, 0, len(foundation.Characters))
	for _, character := range foundation.Characters {
		ids[character.ID] = struct{}{}
		validIDs = append(validIDs, character.ID)
		if name := strings.TrimSpace(character.Name); name != "" {
			characterIDByName[name] = character.ID
		}
	}
	sort.Strings(validIDs)
	for index, finding := range findings {
		characterID := strings.TrimSpace(finding.CharacterID)
		if stableID, ok := characterIDByName[characterID]; ok {
			characterID = stableID
			findings[index].CharacterID = stableID
		}
		if _, ok := ids[characterID]; !ok {
			return fmt.Errorf(
				"findings[%d].character_id %q is not in StoryFoundation; use one of: %s: %w",
				index,
				finding.CharacterID,
				strings.Join(validIDs, ", "),
				errs.ErrToolArgs,
			)
		}
		if strings.TrimSpace(finding.Scene) == "" ||
			strings.TrimSpace(finding.Evidence) == "" ||
			strings.TrimSpace(finding.ViolatedField) == "" ||
			strings.TrimSpace(finding.Description) == "" ||
			strings.TrimSpace(finding.Suggestion) == "" {
			return fmt.Errorf("findings[%d] requires scene, evidence, violated_field, description, and suggestion: %w", index, errs.ErrToolArgs)
		}
	}
	return nil
}

const continuityAuditSystemPrompt = `You are an independent Chinese-fiction continuity reviewer. You did not write the chapter.

Review only these defect classes:
- continuity_repeat: a character asks for or reconfirms a fact that the same character already learned, without a credible trigger such as doubt, deception, memory loss, or deliberate testing.
- identity_pronoun_drift: a pronoun conflicts with the identified character's gender or changes the apparent referent.
- spatial_contradiction: the same character's route, floor, room, or relative position cannot follow from the established location.

Avoid false positives:
1. Compare semantic roles, not just similar wording. For continuity_repeat, prior_actor means the character who knew the fact after the prior passage, and current_actor means the current asker; they must be the same person. prior_recipient/current_recipient mean the other participant. If the knower/asker changes, it is not a repeated question.
2. New details are not repeats. Knowing that someone is engaged does not mean knowing the wedding date.
3. A room number repeated as a stable anchor is not itself a spatial contradiction.
4. Use the current draft as the only source of current_evidence. For a cross-chapter finding, prior_evidence must be an exact quote from prior_chapter_text.
5. Return no finding when evidence or identity is uncertain.

Return exactly one JSON object:
{"findings":[{"type":"continuity_repeat|identity_pronoun_drift|spatial_contradiction","severity":"error|warning","character_id":"stable ID from characters","scene":"concise locator","current_evidence":"exact current-draft quote","prior_evidence":"exact prior-chapter quote or empty only for pronoun drift","prior_actor":"name","prior_recipient":"name","current_actor":"name","current_recipient":"name","description":"precise contradiction","suggestion":"minimal repair"}]}
Use severity=error only for a definite contradiction.`

type independentContinuityFinding struct {
	Type             string `json:"type"`
	Severity         string `json:"severity"`
	CharacterID      string `json:"character_id"`
	Scene            string `json:"scene"`
	CurrentEvidence  string `json:"current_evidence"`
	PriorEvidence    string `json:"prior_evidence"`
	PriorActor       string `json:"prior_actor"`
	PriorRecipient   string `json:"prior_recipient"`
	CurrentActor     string `json:"current_actor"`
	CurrentRecipient string `json:"current_recipient"`
	Description      string `json:"description"`
	Suggestion       string `json:"suggestion"`
}

func (t *CheckConsistencyTool) auditCrossChapterContinuity(
	ctx context.Context,
	chapter int,
	currentContent string,
) ([]domain.ConsistencyIssue, error) {
	if t.continuityAuditor == nil {
		return nil, nil
	}
	recentSummaries, err := t.store.Summaries.LoadRecentSummaries(chapter, 4)
	if err != nil {
		return nil, fmt.Errorf("load recent summaries: %w", err)
	}
	if chapter <= 1 || len(recentSummaries) == 0 {
		return nil, nil
	}
	priorContent, err := t.store.Drafts.LoadChapterText(chapter - 1)
	if err != nil {
		return nil, fmt.Errorf("load prior chapter: %w", err)
	}
	if strings.TrimSpace(priorContent) == "" {
		return nil, nil
	}
	foundation, err := t.store.Foundation.Load()
	if err != nil {
		return nil, fmt.Errorf("load characters: %w", err)
	}
	characters := make([]map[string]string, 0, len(foundation.Characters))
	for _, character := range foundation.Characters {
		characters = append(characters, map[string]string{
			"id":     character.ID,
			"name":   character.Name,
			"gender": character.Gender,
		})
	}
	payload, err := json.Marshal(map[string]any{
		"chapter":            chapter,
		"characters":         characters,
		"recent_summaries":   recentSummaries,
		"prior_chapter":      chapter - 1,
		"prior_chapter_text": priorContent,
		"current_draft":      currentContent,
	})
	if err != nil {
		return nil, fmt.Errorf("encode audit input: %w", err)
	}
	response, err := t.continuityAuditor.Generate(
		ctx,
		[]agentcore.Message{
			agentcore.SystemMsg(continuityAuditSystemPrompt),
			agentcore.UserMsg("Continuity audit input json:\n" + string(payload)),
		},
		nil,
		agentcore.WithMaxTokens(2200),
		agentcore.WithJSONMode(),
	)
	if err != nil {
		return nil, err
	}
	if response == nil || strings.TrimSpace(response.Message.TextContent()) == "" {
		return nil, errors.New("reviewer returned an empty response")
	}
	var output struct {
		Findings []independentContinuityFinding `json:"findings"`
	}
	raw := extractConsistencyJSONObject(response.Message.TextContent())
	if raw == "" {
		return nil, errors.New("reviewer response does not contain a JSON object")
	}
	if err := json.Unmarshal([]byte(raw), &output); err != nil {
		return nil, fmt.Errorf("decode reviewer response: %w", err)
	}
	return t.validateIndependentContinuityFindings(
		currentContent,
		priorContent,
		foundation.Characters,
		output.Findings,
	)
}

func (t *CheckConsistencyTool) validateIndependentContinuityFindings(
	currentContent string,
	priorContent string,
	characters []domain.Character,
	findings []independentContinuityFinding,
) ([]domain.ConsistencyIssue, error) {
	characterIDs := make(map[string]struct{}, len(characters))
	for _, character := range characters {
		characterIDs[character.ID] = struct{}{}
	}
	allowedTypes := map[string]struct{}{
		"continuity_repeat":      {},
		"identity_pronoun_drift": {},
		"spatial_contradiction":  {},
	}
	result := make([]domain.ConsistencyIssue, 0, len(findings))
	for _, finding := range findings {
		finding.Type = strings.TrimSpace(finding.Type)
		if _, ok := allowedTypes[finding.Type]; !ok {
			continue
		}
		if finding.Severity != "error" && finding.Severity != "warning" {
			continue
		}
		if _, ok := characterIDs[strings.TrimSpace(finding.CharacterID)]; !ok {
			continue
		}
		if strings.TrimSpace(finding.CurrentEvidence) == "" ||
			!strings.Contains(currentContent, finding.CurrentEvidence) {
			continue
		}
		if finding.Type != "identity_pronoun_drift" {
			if strings.TrimSpace(finding.PriorEvidence) == "" ||
				!strings.Contains(priorContent, finding.PriorEvidence) {
				continue
			}
			if strings.TrimSpace(finding.PriorActor) == "" ||
				strings.TrimSpace(finding.CurrentActor) == "" {
				continue
			}
		}
		if finding.Type == "continuity_repeat" &&
			!strings.EqualFold(strings.TrimSpace(finding.PriorActor), strings.TrimSpace(finding.CurrentActor)) {
			// A role-swapped question is affirmative evidence that this is not
			// the same character redundantly reacquiring the same fact. Ignore
			// the model's false positive instead of making Writer retry it.
			continue
		}
		if strings.TrimSpace(finding.Scene) == "" ||
			strings.TrimSpace(finding.Description) == "" ||
			strings.TrimSpace(finding.Suggestion) == "" {
			continue
		}
		result = append(result, domain.ConsistencyIssue{
			Type:          finding.Type,
			Severity:      finding.Severity,
			CharacterID:   finding.CharacterID,
			Scene:         finding.Scene,
			ViolatedField: "recent_summaries_and_prior_chapter",
			Description:   finding.Description,
			Evidence:      finding.CurrentEvidence,
			Suggestion:    finding.Suggestion,
		})
	}
	return result, nil
}

func extractConsistencyJSONObject(raw string) string {
	start := strings.Index(raw, "{")
	end := strings.LastIndex(raw, "}")
	if start < 0 || end <= start {
		return ""
	}
	return raw[start : end+1]
}

func mergeConsistencyFindings(
	writerFindings []domain.ConsistencyIssue,
	independentFindings []domain.ConsistencyIssue,
) []domain.ConsistencyIssue {
	result := append([]domain.ConsistencyIssue(nil), writerFindings...)
	seen := make(map[string]struct{}, len(result))
	for _, finding := range result {
		seen[finding.Type+"\x00"+finding.Evidence] = struct{}{}
	}
	for _, finding := range independentFindings {
		key := finding.Type + "\x00" + finding.Evidence
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, finding)
	}
	return result
}
