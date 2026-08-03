package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/store"
)

func TestCheckConsistencyReturnsCompactSameDraftReceipt(t *testing.T) {
	st := store.NewStore(testStoreDir(t))
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	draft := strings.Repeat("正文段落。", 1000)
	if err := st.Drafts.SaveDraft(39, draft); err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}
	raw, err := NewCheckConsistencyTool(st).Execute(context.Background(), json.RawMessage(`{"chapter":39}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(raw) > 2048 {
		t.Fatalf("consistency receipt = %d bytes, want <= 2048", len(raw))
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if _, exists := payload["content"]; exists {
		t.Fatal("consistency receipt must not echo the already-read draft")
	}
	if payload["draft_sha256"] != store.TextSHA256(draft) || payload["reviewed"] != true {
		t.Fatalf("unexpected receipt: %+v", payload)
	}
}

func TestCheckConsistencyRequiresGroundedEvidenceForEveryPlannedScene(t *testing.T) {
	st := store.NewStore(testStoreDir(t))
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := st.Outline.SaveOutline([]domain.OutlineEntry{{
		Chapter: 1,
		Scenes:  []string{"机场初见", "公司入职"},
	}}); err != nil {
		t.Fatalf("SaveOutline: %v", err)
	}
	draft := "# 第一章\n\n机场到达厅里，她拖着白色行李箱从他面前经过。\n\n第二天清晨，他进入维纳斯集团报到。"
	if err := st.Drafts.SaveDraft(1, draft); err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}
	tool := NewCheckConsistencyTool(st)
	if _, err := tool.Execute(context.Background(), json.RawMessage(`{
		"chapter":1,
		"scene_checks":[],
		"findings":[]
	}`)); err == nil || !strings.Contains(err.Error(), "scene_checks count") {
		t.Fatalf("expected missing scene evidence to fail, got %v", err)
	}
	if _, err := tool.Execute(context.Background(), json.RawMessage(`{
		"chapter":1,
		"scene_checks":[
			{"scene":1,"evidence":"机场到达厅里，她拖着白色行李箱","time_and_place_match":true,"pov_match":true,"characters_match":true,"event_order_match":true,"knowledge_match":true,"irreversible_result_match":true},
			{"scene":2,"evidence":"第二天清晨，他进入维纳斯集团报到","time_and_place_match":true,"pov_match":true,"characters_match":true,"event_order_match":true,"knowledge_match":true,"irreversible_result_match":true}
		],
		"findings":[]
	}`)); err != nil {
		t.Fatalf("grounded scene checks should pass: %v", err)
	}
}

func TestCheckConsistencySchemaPinsActiveChapterSceneCount(t *testing.T) {
	st := store.NewStore(testStoreDir(t))
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := st.Progress.Init("test", 3); err != nil {
		t.Fatalf("Init progress: %v", err)
	}
	if err := st.Progress.StartChapter(2); err != nil {
		t.Fatalf("StartChapter: %v", err)
	}
	if err := st.Outline.SaveOutline([]domain.OutlineEntry{{
		Chapter: 2,
		Scenes:  []string{"第一场抵达", "第二场试探", "第三场决定", "第四场钩子"},
	}}); err != nil {
		t.Fatalf("SaveOutline: %v", err)
	}

	toolSchema := NewCheckConsistencyTool(st).Schema()
	properties, ok := toolSchema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("properties=%T", toolSchema["properties"])
	}
	sceneChecks, ok := properties["scene_checks"].(map[string]any)
	if !ok {
		t.Fatalf("scene_checks=%T", properties["scene_checks"])
	}
	if sceneChecks["minItems"] != 4 || sceneChecks["maxItems"] != 4 {
		t.Fatalf("scene count bounds=%v-%v, want 4-4", sceneChecks["minItems"], sceneChecks["maxItems"])
	}
	description, _ := sceneChecks["description"].(string)
	if !strings.Contains(description, "只能提交 4 项") ||
		!strings.Contains(description, "1:第一场抵达") ||
		!strings.Contains(description, "4:第四场钩子") {
		t.Fatalf("dynamic scene contract missing from description: %q", description)
	}
}

func TestCheckConsistencyRejectsInventedSceneEvidence(t *testing.T) {
	st := store.NewStore(testStoreDir(t))
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := st.Outline.SaveOutline([]domain.OutlineEntry{{
		Chapter: 1,
		Scenes:  []string{"机场初见"},
	}}); err != nil {
		t.Fatalf("SaveOutline: %v", err)
	}
	if err := st.Drafts.SaveDraft(1, "# 第一章\n\n机场到达厅里，她从他面前经过。"); err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}
	_, err := NewCheckConsistencyTool(st).Execute(context.Background(), json.RawMessage(`{
		"chapter":1,
		"scene_checks":[
			{"scene":1,"evidence":"苏家商业晚宴上两人第一次相见","time_and_place_match":true,"pov_match":true,"characters_match":true,"event_order_match":true,"knowledge_match":true,"irreversible_result_match":true}
		],
		"findings":[]
	}`))
	if err == nil || !strings.Contains(err.Error(), "exact current-draft quote") {
		t.Fatalf("expected invented evidence to fail, got %v", err)
	}
}

func TestCheckConsistencyAcceptsExactEvidenceAcrossParagraphWhitespace(t *testing.T) {
	st := store.NewStore(testStoreDir(t))
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := st.Outline.SaveOutline([]domain.OutlineEntry{{
		Chapter: 1,
		Scenes:  []string{"车内决定"},
	}}); err != nil {
		t.Fatalf("SaveOutline: %v", err)
	}
	draft := "# 第一章\n\n滨河路绕完一圈，车没有停。\n\n苏瑾琛靠在后排座椅上，闭着眼。\n"
	if err := st.Drafts.SaveDraft(1, draft); err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}
	_, err := NewCheckConsistencyTool(st).Execute(context.Background(), json.RawMessage(`{
		"chapter":1,
		"scene_checks":[{
			"scene":1,
			"evidence":"滨河路绕完一圈，车没有停。苏瑾琛靠在后排座椅上，闭着眼。",
			"time_and_place_match":true,
			"pov_match":true,
			"characters_match":true,
			"event_order_match":true,
			"knowledge_match":true,
			"irreversible_result_match":true
		}],
		"findings":[]
	}`))
	if err != nil {
		t.Fatalf("paragraph whitespace must not invalidate an otherwise exact quote: %v", err)
	}
}

func TestFindConsistencyEvidenceStillRejectsParaphrase(t *testing.T) {
	content := "滨河路绕完一圈，车没有停。\n\n苏瑾琛靠在后排座椅上，闭着眼。"
	if _, found := findConsistencyEvidence(content, "滨河路绕完后，汽车没有停下。苏瑾琛闭目靠着后座。"); found {
		t.Fatal("semantic paraphrase must not pass exact evidence matching")
	}
}

func TestGroundConsistencyEvidenceCanonicalizesUniqueExactSpan(t *testing.T) {
	content := "林舒然蹲在侧庭院的矮冬青后面，膝盖压在石砖上。"
	offset, grounded, found := groundConsistencyEvidence(
		content,
		"她蹲在侧庭院的矮冬青后面，膝盖压在石砖上",
	)
	if !found {
		t.Fatal("expected the unique verbatim span to ground the evidence")
	}
	if offset <= 0 {
		t.Fatalf("offset = %d, want the grounded span after the character name", offset)
	}
	if grounded != "蹲在侧庭院的矮冬青后面，膝盖压在石砖上" {
		t.Fatalf("grounded evidence = %q", grounded)
	}
	if !strings.Contains(content, grounded) {
		t.Fatalf("grounded evidence is not verbatim draft text: %q", grounded)
	}
}

func TestGroundConsistencyEvidenceRejectsAmbiguousShortAnchor(t *testing.T) {
	content := "她推开房门走进院子。片刻后，她推开房门走进院子。"
	if _, _, found := groundConsistencyEvidence(content, "随后她推开房门走进院子"); found {
		t.Fatal("ambiguous repeated anchor must not be accepted")
	}
}

func TestCheckConsistencyRequiresEvidenceInPlannedSceneOrder(t *testing.T) {
	st := store.NewStore(testStoreDir(t))
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := st.Outline.SaveOutline([]domain.OutlineEntry{{
		Chapter: 1,
		Scenes:  []string{"清晨公寓", "午间公司"},
	}}); err != nil {
		t.Fatalf("SaveOutline: %v", err)
	}
	draft := "# 第一章\n\n清晨公寓里，两人准备早餐。\n\n午间公司里，新同事完成报到。"
	if err := st.Drafts.SaveDraft(1, draft); err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}
	_, err := NewCheckConsistencyTool(st).Execute(context.Background(), json.RawMessage(`{
		"chapter":1,
		"scene_checks":[
			{"scene":1,"evidence":"午间公司里，新同事完成报到","time_and_place_match":true,"pov_match":true,"characters_match":true,"event_order_match":true,"knowledge_match":true,"irreversible_result_match":true},
			{"scene":2,"evidence":"清晨公寓里，两人准备早餐","time_and_place_match":true,"pov_match":true,"characters_match":true,"event_order_match":true,"knowledge_match":true,"irreversible_result_match":true}
		],
		"findings":[]
	}`))
	if err == nil || !strings.Contains(err.Error(), "not in planned scene order") {
		t.Fatalf("expected out-of-order evidence to fail, got %v", err)
	}
}

func TestCompactIndexedSceneContractsBoundsErrorContext(t *testing.T) {
	scenes := []string{
		"scene: 1; pov: hero; setting: home; summary: " + strings.Repeat("甲", 300),
		"scene: 2; pov: rival; setting: office; summary: " + strings.Repeat("乙", 300),
	}
	got := compactIndexedSceneContracts(scenes)
	if len([]rune(got)) > 205 {
		t.Fatalf("compact contracts too large: %d runes", len([]rune(got)))
	}
	if !strings.Contains(got, "pov: hero") || !strings.Contains(got, "pov: rival") {
		t.Fatalf("compact contracts lost identity labels: %q", got)
	}
}

func TestCheckConsistencyRecordsMissingPlannedSceneAsBlockingFinding(t *testing.T) {
	st := store.NewStore(testStoreDir(t))
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := st.Characters.Save([]domain.Character{{
		ID: "lin_shuran", Name: "林舒然",
	}}); err != nil {
		t.Fatalf("Save characters: %v", err)
	}
	if err := st.Outline.SaveOutline([]domain.OutlineEntry{{
		Chapter: 1,
		Scenes:  []string{"清晨公寓", "晚间公寓"},
	}}); err != nil {
		t.Fatalf("SaveOutline: %v", err)
	}
	if err := st.Drafts.SaveDraft(1, "# 第一章\n\n清晨公寓里，林舒然准备早餐。"); err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}
	raw, err := NewCheckConsistencyTool(st).Execute(context.Background(), json.RawMessage(`{
		"chapter":1,
		"scene_checks":[
			{"scene":1,"evidence":"清晨公寓里，林舒然准备早餐","time_and_place_match":true,"pov_match":true,"characters_match":true,"event_order_match":true,"knowledge_match":true,"irreversible_result_match":true},
			{"scene":2,"evidence":"MISSING_FROM_DRAFT","time_and_place_match":false,"pov_match":false,"characters_match":false,"event_order_match":false,"knowledge_match":false,"irreversible_result_match":false}
		],
		"findings":[{
			"type":"arc_beat_miss",
			"severity":"error",
			"character_id":"lin_shuran",
			"scene":"scene 2",
			"evidence":"MISSING_FROM_DRAFT",
			"violated_field":"chapter_contract.scenes[2]",
			"description":"晚间公寓场景缺失",
			"suggestion":"在章末补写晚间公寓复盘与周末采买计划"
		}]
	}`))
	if err != nil {
		t.Fatalf("missing-scene finding should be recorded, got %v", err)
	}
	var result map[string]any
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	if result["passed"] != false || result["blocking"] != true {
		t.Fatalf("missing scene must block consistency: %s", raw)
	}
	if st.Checkpoints.LatestByStep(domain.ChapterScope(1), "consistency_check") != nil {
		t.Fatal("blocking missing scene must not create a passing checkpoint")
	}
	audit, err := st.Consistency.LoadAudit(1)
	if err != nil || audit == nil || audit.Passed || len(audit.Findings) != 1 {
		t.Fatalf("blocking audit was not persisted for recovery: audit=%+v err=%v", audit, err)
	}
}

func TestCheckConsistencyMissingMarkerRequiresBlockingFinding(t *testing.T) {
	st := store.NewStore(testStoreDir(t))
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := st.Outline.SaveOutline([]domain.OutlineEntry{{
		Chapter: 1,
		Scenes:  []string{"晚间公寓"},
	}}); err != nil {
		t.Fatalf("SaveOutline: %v", err)
	}
	if err := st.Drafts.SaveDraft(1, "# 第一章\n\n当前正文没有晚间场景。"); err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}
	_, err := NewCheckConsistencyTool(st).Execute(context.Background(), json.RawMessage(`{
		"chapter":1,
		"scene_checks":[
			{"scene":1,"evidence":"MISSING_FROM_DRAFT","time_and_place_match":false,"pov_match":false,"characters_match":false,"event_order_match":false,"knowledge_match":false,"irreversible_result_match":false}
		],
		"findings":[]
	}`))
	if err == nil || !strings.Contains(err.Error(), "add a critical/error finding") {
		t.Fatalf("expected missing marker without finding to fail, got %v", err)
	}
}

func TestCheckConsistencyInvalidCharacterIDListsStableFoundationIDs(t *testing.T) {
	st := store.NewStore(testStoreDir(t))
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := st.Characters.Save([]domain.Character{
		{ID: "char_lin_wanqing", Name: "林晚晴"},
		{ID: "char_gu_zhong", Name: "顾钟"},
	}); err != nil {
		t.Fatalf("Save characters: %v", err)
	}

	err := NewCheckConsistencyTool(st).validateCharacterFindings(1, []domain.ConsistencyIssue{{
		CharacterID:   "lin_wanqing",
		Scene:         "scene 1",
		Evidence:      "evidence",
		ViolatedField: "characters",
		Description:   "description",
		Suggestion:    "suggestion",
	}})
	if err == nil {
		t.Fatal("invalid character ID must fail")
	}
	for _, want := range []string{"lin_wanqing", "char_gu_zhong, char_lin_wanqing"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error must include %q, got %v", want, err)
		}
	}
}

func TestCheckConsistencyNormalizesCharacterNameToStableID(t *testing.T) {
	st := store.NewStore(testStoreDir(t))
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := st.Characters.Save([]domain.Character{
		{ID: "char_lin_wanqing", Name: "林晚晴"},
		{ID: "char_qi_shoukun", Name: "戚守坤"},
	}); err != nil {
		t.Fatalf("Save characters: %v", err)
	}
	findings := []domain.ConsistencyIssue{{
		CharacterID:   "林晚晴",
		Scene:         "scene 1",
		Evidence:      "evidence",
		ViolatedField: "characters",
		Description:   "description",
		Suggestion:    "suggestion",
	}}

	if err := NewCheckConsistencyTool(st).validateCharacterFindings(1, findings); err != nil {
		t.Fatalf("unique StoryFoundation name should normalize: %v", err)
	}
	if findings[0].CharacterID != "char_lin_wanqing" {
		t.Fatalf("character ID = %q, want stable StoryFoundation ID", findings[0].CharacterID)
	}
}

func TestCheckConsistencyIndependentAuditFindsRepeatedKnownFact(t *testing.T) {
	st := prepareIndependentContinuityTestStore(t)
	model := &consistencyAuditModel{response: `{"findings":[{
		"type":"continuity_repeat",
		"severity":"error",
		"character_id":"su",
		"scene":"coffee counter",
		"current_evidence":"苏瑾琛问：“你结婚了吗？”",
		"prior_evidence":"刘子昊说：“我订婚了，和未婚妻住一起。”",
		"prior_actor":"苏瑾琛",
		"prior_recipient":"刘子昊",
		"current_actor":"苏瑾琛",
		"current_recipient":"刘子昊",
		"description":"苏瑾琛再次询问已经明确获知的婚恋状态",
		"suggestion":"删除重复问答，保留后续婚期新信息"
	}]}`}
	raw, err := NewCheckConsistencyTool(st, model).Execute(
		context.Background(),
		json.RawMessage(`{"chapter":2,"scene_checks":[],"findings":[]}`),
	)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var result struct {
		Passed   bool                      `json:"passed"`
		Findings []domain.ConsistencyIssue `json:"findings"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	if result.Passed || len(result.Findings) != 1 ||
		result.Findings[0].Type != "continuity_repeat" {
		t.Fatalf("independent repeat was not blocking: %s", raw)
	}
	if model.calls != 1 || !strings.Contains(model.userPrompt, `"recent_summaries"`) ||
		!strings.Contains(model.userPrompt, "json") {
		t.Fatalf("independent reviewer did not receive recent summaries: calls=%d", model.calls)
	}
}

func TestCheckConsistencyIndependentAuditDropsRoleSwappedQuestionFalsePositive(t *testing.T) {
	st := prepareIndependentContinuityTestStore(t)
	model := &consistencyAuditModel{response: `{"findings":[{
		"type":"continuity_repeat",
		"severity":"error",
		"character_id":"liu",
		"scene":"coffee counter",
		"current_evidence":"刘子昊问：“你住哪边？”",
		"prior_evidence":"苏瑾琛问：“你现在住哪里？”",
		"prior_actor":"苏瑾琛",
		"prior_recipient":"刘子昊",
		"current_actor":"刘子昊",
		"current_recipient":"苏瑾琛",
		"description":"两章都询问住址",
		"suggestion":"删除当前问题"
	}]}`}
	raw, err := NewCheckConsistencyTool(st, model).Execute(
		context.Background(),
		json.RawMessage(`{"chapter":2,"scene_checks":[],"findings":[]}`),
	)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var result struct {
		Passed   bool                      `json:"passed"`
		Findings []domain.ConsistencyIssue `json:"findings"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	if !result.Passed || len(result.Findings) != 0 {
		t.Fatalf("role-swapped question should not be a repeat: %s", raw)
	}
}

func TestCheckConsistencyIndependentAuditDropsUngroundedFinding(t *testing.T) {
	st := prepareIndependentContinuityTestStore(t)
	model := &consistencyAuditModel{response: `{"findings":[{
		"type":"continuity_repeat",
		"severity":"error",
		"character_id":"su",
		"scene":"coffee counter",
		"current_evidence":"苏瑾琛问：“你结婚了吗？”",
		"prior_evidence":"模型改写的上一章证据",
		"prior_actor":"苏瑾琛",
		"prior_recipient":"刘子昊",
		"current_actor":"苏瑾琛",
		"current_recipient":"刘子昊",
		"description":"未经原文支持的判断",
		"suggestion":"删除当前问题"
	}]}`}
	raw, err := NewCheckConsistencyTool(st, model).Execute(
		context.Background(),
		json.RawMessage(`{"chapter":2,"scene_checks":[],"findings":[]}`),
	)
	if err != nil {
		t.Fatalf("ungrounded independent finding should be ignored, got %v", err)
	}
	var result struct {
		Passed   bool                      `json:"passed"`
		Findings []domain.ConsistencyIssue `json:"findings"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	if !result.Passed || len(result.Findings) != 0 {
		t.Fatalf("ungrounded independent finding should not block Writer: %s", raw)
	}
}

func prepareIndependentContinuityTestStore(t *testing.T) *store.Store {
	t.Helper()
	st := store.NewStore(testStoreDir(t))
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := st.Characters.Save([]domain.Character{
		{ID: "liu", Name: "刘子昊", Gender: "male"},
		{ID: "su", Name: "苏瑾琛", Gender: "male"},
	}); err != nil {
		t.Fatalf("Save characters: %v", err)
	}
	prior := "苏瑾琛问：“你现在住哪里？”\n\n刘子昊说：“我订婚了，和未婚妻住一起。”"
	if err := st.Drafts.SaveFinalChapter(1, prior); err != nil {
		t.Fatalf("SaveFinalChapter: %v", err)
	}
	if err := st.Summaries.SaveSummary(domain.ChapterSummary{
		Chapter:    1,
		Summary:    "苏瑾琛得知刘子昊已经订婚并与未婚妻同住。",
		Characters: []string{"刘子昊", "苏瑾琛"},
		KeyEvents:  []string{"刘子昊告知订婚事实"},
	}); err != nil {
		t.Fatalf("SaveSummary: %v", err)
	}
	current := "苏瑾琛问：“你结婚了吗？”\n\n刘子昊问：“你住哪边？”\n\n刘子昊又问：“你们什么时候结婚？”"
	if err := st.Drafts.SaveDraft(2, current); err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}
	return st
}

type consistencyAuditModel struct {
	response   string
	calls      int
	userPrompt string
}

func (m *consistencyAuditModel) Generate(
	_ context.Context,
	messages []agentcore.Message,
	_ []agentcore.ToolSpec,
	_ ...agentcore.CallOption,
) (*agentcore.LLMResponse, error) {
	m.calls++
	if len(messages) > 1 {
		m.userPrompt = messages[1].TextContent()
	}
	return &agentcore.LLMResponse{Message: agentcore.Message{
		Role:      agentcore.RoleAssistant,
		Content:   []agentcore.ContentBlock{agentcore.TextBlock(m.response)},
		Timestamp: time.Now(),
	}}, nil
}

func (m *consistencyAuditModel) GenerateStream(
	context.Context,
	[]agentcore.Message,
	[]agentcore.ToolSpec,
	...agentcore.CallOption,
) (<-chan agentcore.StreamEvent, error) {
	stream := make(chan agentcore.StreamEvent)
	close(stream)
	return stream, nil
}

func (m *consistencyAuditModel) SupportsTools() bool { return false }
