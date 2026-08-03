package agents

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/store"
	"github.com/voocel/ainovel-cli/internal/tools"
)

func TestWriterContextToolInfersPendingPolishChapter(t *testing.T) {
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := st.Progress.Init("test", 40); err != nil {
		t.Fatalf("Progress.Init: %v", err)
	}
	progress, _ := st.Progress.Load()
	progress.Flow = domain.FlowPolishing
	progress.PendingRewrites = []int{39}
	progress.InProgressChapter = 39
	if err := st.Progress.Save(progress); err != nil {
		t.Fatalf("Progress.Save: %v", err)
	}
	raw, err := newWriterContextTool(tools.NewContextTool(st, tools.References{}, "default"), st).Execute(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if payload["context_profile"] != "polishing" {
		t.Fatalf("context_profile = %v, want polishing", payload["context_profile"])
	}
	if _, ok := payload["planning_memory"]; ok {
		t.Fatal("writer empty context call must not fall through to planning context")
	}
}

func TestWriterContextToolRejectsFullCrossChapterWorkPackage(t *testing.T) {
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := st.Progress.Init("test", 50); err != nil {
		t.Fatalf("Progress.Init: %v", err)
	}
	if err := st.Progress.StartChapter(41); err != nil {
		t.Fatalf("StartChapter: %v", err)
	}
	tool := newWriterContextTool(tools.NewContextTool(st, tools.References{}, "default"), st)
	raw, err := tool.Execute(t.Context(), json.RawMessage(`{"chapter":40}`))
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["context_profile"] != "cross_chapter_redirect" || payload["full_context_loaded"] != false {
		t.Fatalf("unexpected cross-chapter payload: %+v", payload)
	}
	if len(raw) >= 2*1024 {
		t.Fatalf("cross-chapter redirect=%d bytes, want compact tool guidance", len(raw))
	}
}

func TestWriterContextToolRejectsPlanningScopeAndMarksActivePackageAuthoritative(t *testing.T) {
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := st.Progress.Init("test", 2); err != nil {
		t.Fatalf("Progress.Init: %v", err)
	}
	if err := st.Outline.SaveOutline([]domain.OutlineEntry{{
		Chapter: 1, Title: "Opening", CoreEvent: "The confirmed story begins",
	}}); err != nil {
		t.Fatalf("SaveOutline: %v", err)
	}
	tool := newWriterContextTool(tools.NewContextTool(st, tools.References{}, "default"), st)

	redirectRaw, err := tool.Execute(t.Context(), json.RawMessage(`{"scope":"planning"}`))
	if err != nil {
		t.Fatal(err)
	}
	var redirect map[string]any
	if err := json.Unmarshal(redirectRaw, &redirect); err != nil {
		t.Fatal(err)
	}
	if redirect["context_profile"] != "writer_scope_redirect" || redirect["full_context_loaded"] != false {
		t.Fatalf("unexpected scope redirect: %+v", redirect)
	}

	activeRaw, err := tool.Execute(t.Context(), json.RawMessage(`{"scope":"chapter","chapter":1}`))
	if err != nil {
		t.Fatal(err)
	}
	var active map[string]any
	if err := json.Unmarshal(activeRaw, &active); err != nil {
		t.Fatal(err)
	}
	contract, _ := active["writer_execution_contract"].(map[string]any)
	if contract["authoritative"] != true || contract["never_invent_replacement"] != true {
		t.Fatalf("missing authoritative Writer contract: %+v", contract)
	}
	if active["context_profile"] == "writer_scope_redirect" {
		t.Fatalf("explicit active chapter scope was rejected: %+v", active)
	}
}

func TestCompactWriterCharacterWorksetPreservesDecisionFieldsAndAllCharacters(t *testing.T) {
	payload := map[string]any{
		"episodic_memory": map[string]any{
			"character_workset": map[string]any{
				"full": []any{
					map[string]any{
						"id": "hero", "name": "主角", "role": "主角",
						"goal": "完成本章目标", "voice": "克制", "constraints": []any{"不能泄密"},
						"knowledge_boundary": map[string]any{"unknown": []any{"真相"}},
						"description":        strings.Repeat("重复传记", 2_000),
						"key_backstory":      []any{strings.Repeat("重复旧事", 1_000)},
					},
				},
				"compressed": []any{
					map[string]any{
						"id": "support", "name": "配角", "role": "协助者",
						"goal": "推进线索", "voice": "简洁", "constraints": []any{"不抢戏"},
					},
				},
				"diagnostics": map[string]any{
					"requested_ids": []any{"hero", "support"},
				},
			},
		},
	}

	compactWriterCharacterWorkset(payload)
	episodic := payload["episodic_memory"].(map[string]any)
	workset := episodic["character_workset"].(map[string]any)
	cards := workset["full"].([]any)
	if len(cards) != 2 {
		t.Fatalf("projected character count=%d, want 2", len(cards))
	}
	hero := cards[0].(map[string]any)
	for _, key := range []string{"id", "name", "role", "goal", "voice", "constraints", "knowledge_boundary"} {
		if _, ok := hero[key]; !ok {
			t.Fatalf("decision field %q was lost: %+v", key, hero)
		}
	}
	for _, duplicate := range []string{"description", "key_backstory"} {
		if _, ok := hero[duplicate]; ok {
			t.Fatalf("duplicated biography field %q crossed Writer boundary", duplicate)
		}
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) >= 2*1024 {
		t.Fatalf("projected workset=%d bytes, expected deterministic headroom", len(raw))
	}
}

func TestCompactWriterRecoveryContextKeepsValidationAuthority(t *testing.T) {
	payload := map[string]any{
		"working_memory": map[string]any{
			"current_chapter_outline": "exact contract",
			"user_rules":              "reader rules",
			"word_budget":             "soft target",
			"rewrite_brief":           "local repair",
			"previous_tail":           strings.Repeat("already represented prose", 500),
			"future_chapter_promises": strings.Repeat("drafting scaffold", 500),
			"simulation_profile":      strings.Repeat("drafting simulation", 500),
		},
		"episodic_memory": map[string]any{
			"character_workset":    "canonical cast",
			"recent_state_changes": "current state",
			"relationship_state":   "current relationships",
			"foreshadow_ledger":    "active promises",
			"planning_tier":        "chapter",
			"recent_summaries":     strings.Repeat("duplicated continuity", 500),
		},
		"reference_pack": map[string]any{
			"style_anchors": "technique only",
			"references":    strings.Repeat("source corpus duplication", 500),
		},
	}

	compactWriterRecoveryContext(payload)
	working := payload["working_memory"].(map[string]any)
	for _, key := range []string{"current_chapter_outline", "user_rules", "word_budget", "rewrite_brief"} {
		if _, ok := working[key]; !ok {
			t.Fatalf("recovery lost working_memory.%s", key)
		}
	}
	for _, removed := range []string{"previous_tail", "future_chapter_promises", "simulation_profile"} {
		if _, ok := working[removed]; ok {
			t.Fatalf("drafting-only working_memory.%s remained", removed)
		}
	}
	episodic := payload["episodic_memory"].(map[string]any)
	for _, key := range []string{"character_workset", "recent_state_changes", "relationship_state", "foreshadow_ledger"} {
		if _, ok := episodic[key]; !ok {
			t.Fatalf("recovery lost episodic_memory.%s", key)
		}
	}
	references := payload["reference_pack"].(map[string]any)
	if references["style_anchors"] != "technique only" {
		t.Fatalf("style anchors were lost: %+v", references)
	}
	if _, ok := references["references"]; ok {
		t.Fatal("source corpus duplication remained in validation context")
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) >= 4*1024 {
		t.Fatalf("recovery projection=%d bytes, expected deterministic headroom", len(raw))
	}
}

func TestWriterReadChapterToolBoundsPriorContinuityTail(t *testing.T) {
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := st.Progress.Init("test", 50); err != nil {
		t.Fatalf("Progress.Init: %v", err)
	}
	if err := st.Progress.StartChapter(41); err != nil {
		t.Fatalf("StartChapter: %v", err)
	}
	if err := st.Drafts.SaveFinalChapter(40, strings.Repeat("前章连续性正文。", 300)); err != nil {
		t.Fatalf("SaveFinalChapter: %v", err)
	}
	tool := newWriterReadChapterTool(tools.NewReadChapterTool(st), st)
	raw, err := tool.Execute(t.Context(), json.RawMessage(`{"chapter":40,"source":"final"}`))
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		ReturnedRunes              int    `json:"returned_runes"`
		Truncated                  bool   `json:"truncated"`
		ContextProfile             string `json:"context_profile"`
		ContinuityEvidenceComplete bool   `json:"continuity_evidence_complete"`
		DoNotRetryForMore          bool   `json:"do_not_retry_for_more"`
		Hint                       string `json:"hint"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	if !payload.Truncated || payload.ReturnedRunes > writerPriorChapterMaxRunes+3 {
		t.Fatalf("prior chapter was not bounded: %+v", payload)
	}
	if payload.ContextProfile != "bounded_prior_continuity_tail" || !payload.ContinuityEvidenceComplete || !payload.DoNotRetryForMore {
		t.Fatalf("prior continuity guidance is incomplete: %+v", payload)
	}
	if !strings.Contains(payload.Hint, "Proceed directly") || strings.Contains(payload.Hint, "increase the limit") {
		t.Fatalf("prior continuity hint can trigger a retry loop: %q", payload.Hint)
	}
}

func TestWriterReadChapterToolRedirectsOlderHistoryBeforeDraft(t *testing.T) {
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := st.Progress.Init("test", 50); err != nil {
		t.Fatalf("Progress.Init: %v", err)
	}
	if err := st.Progress.StartChapter(41); err != nil {
		t.Fatalf("StartChapter: %v", err)
	}
	if err := st.Drafts.SaveFinalChapter(39, "older chapter prose that must not be loaded"); err != nil {
		t.Fatalf("SaveFinalChapter: %v", err)
	}
	raw, err := newWriterReadChapterTool(tools.NewReadChapterTool(st), st).Execute(
		t.Context(),
		json.RawMessage(`{"chapter":39,"source":"final"}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Chapter                    int    `json:"chapter"`
		ActiveChapter              int    `json:"active_chapter"`
		ContextProfile             string `json:"context_profile"`
		Content                    string `json:"content"`
		ContentLoaded              bool   `json:"content_loaded"`
		ContinuityEvidenceComplete bool   `json:"continuity_evidence_complete"`
		DoNotRetryForMore          bool   `json:"do_not_retry_for_more"`
		Reason                     string `json:"reason"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Chapter != 39 || payload.ActiveChapter != 41 || payload.Content != "" || payload.ContentLoaded {
		t.Fatalf("older history was loaded instead of redirected: %+v", payload)
	}
	if payload.ContextProfile != "prior_history_redirect" ||
		!payload.ContinuityEvidenceComplete ||
		!payload.DoNotRetryForMore ||
		payload.Reason != "older_history_is_already_in_novel_context" {
		t.Fatalf("older history redirect is incomplete: %+v", payload)
	}
}

func TestWriterReadChapterToolRedirectsPriorHistoryAfterDraft(t *testing.T) {
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := st.Progress.Init("test", 50); err != nil {
		t.Fatalf("Progress.Init: %v", err)
	}
	if err := st.Progress.StartChapter(41); err != nil {
		t.Fatalf("StartChapter: %v", err)
	}
	if err := st.Drafts.SaveFinalChapter(40, "adjacent prior prose that must not be reloaded"); err != nil {
		t.Fatalf("SaveFinalChapter: %v", err)
	}
	if err := st.Drafts.SaveDraft(41, "current draft under validation"); err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}
	raw, err := newWriterReadChapterTool(tools.NewReadChapterTool(st), st).Execute(
		t.Context(),
		json.RawMessage(`{"chapter":40,"source":"final"}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		ContextProfile string `json:"context_profile"`
		Content        string `json:"content"`
		ContentLoaded  bool   `json:"content_loaded"`
		Reason         string `json:"reason"`
		NextAction     string `json:"next_action"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.ContextProfile != "prior_history_redirect" ||
		payload.Content != "" ||
		payload.ContentLoaded ||
		payload.Reason != "active_draft_is_already_available" ||
		!strings.Contains(payload.NextAction, "current draft") {
		t.Fatalf("post-draft prior read was not redirected: %+v", payload)
	}
}

func TestWriterReadChapterToolInfersCurrentDraftAndSource(t *testing.T) {
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := st.Progress.Init("test", 50); err != nil {
		t.Fatalf("Progress.Init: %v", err)
	}
	if err := st.Progress.StartChapter(41); err != nil {
		t.Fatalf("StartChapter: %v", err)
	}
	if err := st.Drafts.SaveDraft(41, "current stored draft"); err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}
	tool := newWriterReadChapterTool(tools.NewReadChapterTool(st), st)
	raw, err := tool.Execute(t.Context(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Chapter int    `json:"chapter"`
		Source  string `json:"source"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Chapter != 41 || payload.Source != "draft" || payload.Content != "current stored draft" {
		t.Fatalf("inferred current read=%+v", payload)
	}
}

func TestWriterReadChapterToolAttachesCurrentPersistedDeAIRepair(t *testing.T) {
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := st.Progress.Init("test", 2); err != nil {
		t.Fatalf("Progress.Init: %v", err)
	}
	if err := st.Progress.StartChapter(1); err != nil {
		t.Fatalf("StartChapter: %v", err)
	}
	draft := "# 第一章\n\n" + strings.Repeat("他停住——又向前走了一步。\n", 20)
	if err := st.Drafts.SaveDraft(1, draft); err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}
	if _, err := st.Checkpoints.AppendArtifact(domain.ChapterScope(1), "consistency_check", "drafts/01.draft.md"); err != nil {
		t.Fatalf("consistency checkpoint: %v", err)
	}
	if _, err := tools.NewCheckDeAITool(st).Execute(t.Context(), json.RawMessage(`{"chapter":1}`)); err != nil {
		t.Fatalf("check_de_ai: %v", err)
	}

	raw, err := newWriterReadChapterTool(tools.NewReadChapterTool(st), st).Execute(
		t.Context(),
		json.RawMessage(`{"chapter":1,"source":"draft"}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Pending *struct {
			ConsistencyCurrent bool `json:"consistency_current"`
			DoNotRepeat        bool `json:"do_not_repeat_consistency_before_edit"`
			Batch              struct {
				ID string `json:"id"`
			} `json:"batch"`
			Findings []struct {
				Code     string   `json:"code"`
				Examples []string `json:"examples"`
			} `json:"findings"`
			NextAction string `json:"next_action"`
		} `json:"pending_de_ai_repair"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Pending == nil || !payload.Pending.ConsistencyCurrent || !payload.Pending.DoNotRepeat {
		t.Fatalf("pending repair guidance missing: %s", raw)
	}
	if payload.Pending.Batch.ID != "punctuation" || len(payload.Pending.Findings) == 0 ||
		len(payload.Pending.Findings[0].Examples) == 0 ||
		!strings.Contains(payload.Pending.NextAction, "repair_de_ai_batch") {
		t.Fatalf("pending repair is not actionable: %+v", payload.Pending)
	}
}

func TestWriterReadChapterToolAttachesCurrentPersistedConsistencyRepair(t *testing.T) {
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := st.Progress.Init("test", 2); err != nil {
		t.Fatalf("Progress.Init: %v", err)
	}
	if err := st.Progress.StartChapter(1); err != nil {
		t.Fatalf("StartChapter: %v", err)
	}
	const draft = "current draft with a misplaced scene"
	if err := st.Drafts.SaveDraft(1, draft); err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}
	if err := st.Consistency.SaveAudit(store.ConsistencyAudit{
		Chapter:     1,
		DraftSHA256: store.TextSHA256(draft),
		Findings: []domain.ConsistencyIssue{{
			Type:        "arc_beat_miss",
			Severity:    "error",
			Description: "scene occurs in the wrong room",
			Evidence:    "misplaced scene",
			Suggestion:  "move the event to the contracted room",
		}},
	}); err != nil {
		t.Fatalf("SaveAudit: %v", err)
	}

	raw, err := newWriterReadChapterTool(tools.NewReadChapterTool(st), st).Execute(
		t.Context(),
		json.RawMessage(`{"chapter":1,"source":"draft"}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Pending *struct {
			DraftSHA256 string                    `json:"draft_sha256"`
			DoNotRepeat bool                      `json:"do_not_repeat_consistency_before_edit"`
			Findings    []domain.ConsistencyIssue `json:"findings"`
			NextAction  string                    `json:"next_action"`
		} `json:"pending_consistency_repair"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Pending == nil || !payload.Pending.DoNotRepeat ||
		payload.Pending.DraftSHA256 != store.TextSHA256(draft) ||
		len(payload.Pending.Findings) != 1 ||
		!strings.Contains(payload.Pending.NextAction, "edit_chapter") {
		t.Fatalf("pending consistency repair is not actionable: %s", raw)
	}
}

func TestWriterChapterInferenceToolAddsActiveChapter(t *testing.T) {
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := st.Progress.Init("test", 50); err != nil {
		t.Fatalf("Progress.Init: %v", err)
	}
	if err := st.Progress.StartChapter(41); err != nil {
		t.Fatalf("StartChapter: %v", err)
	}
	if err := st.Drafts.SaveDraft(41, strings.Repeat("自然叙事句子。", 500)); err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}
	if _, err := st.Checkpoints.AppendArtifact(domain.ChapterScope(41), "consistency_check", "drafts/41.draft.md"); err != nil {
		t.Fatalf("consistency checkpoint: %v", err)
	}
	tool := newWriterChapterInferenceTool(tools.NewCheckDeAITool(st), st)
	raw, err := tool.Execute(t.Context(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Chapter int `json:"chapter"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Chapter != 41 {
		t.Fatalf("inferred chapter=%d, want 41", payload.Chapter)
	}
}

func TestWriterPlanDefersToHostWhenAuthoritativeDraftAlreadyExists(t *testing.T) {
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := st.Progress.Init("test", 3); err != nil {
		t.Fatalf("Progress.Init: %v", err)
	}
	if err := st.Progress.StartChapter(2); err != nil {
		t.Fatalf("StartChapter: %v", err)
	}
	if err := st.Drafts.SaveDraft(2, "# 已落盘正文\n\n这一版正文必须保留。"); err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}

	raw, err := newWriterChapterInferenceTool(tools.NewPlanChapterTool(st), st).Execute(
		t.Context(),
		json.RawMessage(`{"chapter":2,"title":"错误的新计划"}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		DeferredToHost bool `json:"deferred_to_host"`
		DraftExists    bool `json:"draft_exists"`
		PlanSkipped    bool `json:"plan_skipped"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	if !payload.DeferredToHost || !payload.DraftExists || !payload.PlanSkipped {
		t.Fatalf("existing draft was not protected: %s", raw)
	}
	if plan, err := st.Drafts.LoadChapterPlan(2); err != nil || plan != nil {
		t.Fatalf("new plan crossed authoritative draft boundary: plan=%+v err=%v", plan, err)
	}
}

func TestCoordinatorContextToolDefaultsToProgressStatus(t *testing.T) {
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := st.Progress.Init("test", 40); err != nil {
		t.Fatalf("Progress.Init: %v", err)
	}
	raw, err := newCoordinatorContextTool(tools.NewContextTool(st, tools.References{}, "default")).Execute(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if _, ok := payload["progress_status"]; !ok {
		t.Fatalf("progress_status missing: %+v", payload)
	}
	if _, ok := payload["planning_memory"]; ok {
		t.Fatal("coordinator default context call must not load Architect planning context")
	}
}

func TestWriterDraftChapterToolInfersInProgressChapter(t *testing.T) {
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := st.Progress.Init("test", 4); err != nil {
		t.Fatalf("Progress.Init: %v", err)
	}
	if err := st.Progress.StartChapter(3); err != nil {
		t.Fatalf("StartChapter: %v", err)
	}

	tool := newWriterDraftChapterTool(st)
	raw, err := tool.Execute(context.Background(), writerDraftArgs(t, map[string]any{
		"content": "chapter three prose",
		"mode":    "write",
	}))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var result map[string]any
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if result["chapter"].(float64) != 3 {
		t.Fatalf("chapter = %v, want 3", result["chapter"])
	}
	if draft, _ := st.Drafts.LoadDraft(3); draft != "chapter three prose" {
		t.Fatalf("draft chapter 3 = %q", draft)
	}
}

func TestWriterDraftChapterToolDefersOverwriteOfActiveDraft(t *testing.T) {
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := st.Progress.Init("test", 4); err != nil {
		t.Fatalf("Progress.Init: %v", err)
	}
	if err := st.Progress.StartChapter(3); err != nil {
		t.Fatalf("StartChapter: %v", err)
	}
	const saved = "authoritative chapter three prose"
	if err := st.Drafts.SaveDraft(3, saved); err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}

	raw, err := newWriterDraftChapterTool(st).Execute(t.Context(), writerDraftArgs(t, map[string]any{
		"content": "replacement prose after a validation error",
		"mode":    "write",
	}))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var result struct {
		DeferredToHost bool `json:"deferred_to_host"`
		DraftExists    bool `json:"draft_exists"`
		DraftSkipped   bool `json:"draft_skipped"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if !result.DeferredToHost || !result.DraftExists || !result.DraftSkipped {
		t.Fatalf("active draft overwrite was not deferred: %s", raw)
	}
	if draft, _ := st.Drafts.LoadDraft(3); draft != saved {
		t.Fatalf("active draft was overwritten: %q", draft)
	}
}

func TestWriterDraftChapterToolInfersHostAuthorizedOversizeRegeneration(t *testing.T) {
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := st.Progress.Save(&domain.Progress{NovelName: "test", Phase: domain.PhaseWriting, TotalChapters: 1, InProgressChapter: 1}); err != nil {
		t.Fatalf("Progress.Save: %v", err)
	}
	budget := domain.NewWordBudget(100, "test").WithPlannedChapters(1)
	if err := st.RunMeta.SetWordBudget(&budget); err != nil {
		t.Fatalf("SetWordBudget: %v", err)
	}
	if err := st.Drafts.SaveDraft(1, strings.Repeat("原", 180)); err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}

	raw, err := newWriterDraftChapterTool(st).Execute(t.Context(), writerDraftArgs(t, map[string]any{
		"chapter": 1,
		"content": strings.Repeat("新", 100),
		"mode":    "write",
	}))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var result struct {
		Written             bool `json:"written"`
		ReplacedOutOfBudget bool `json:"replaced_out_of_budget"`
		DraftSkipped        bool `json:"draft_skipped"`
	}
	if err := json.Unmarshal(raw, &result); err != nil || !result.Written || !result.ReplacedOutOfBudget || result.DraftSkipped {
		t.Fatalf("host-selected regeneration was blocked when the model omitted the control flag: %+v err=%v raw=%s", result, err, raw)
	}
}

func TestWriterDraftChapterToolAllowsExplicitQueuedRewrite(t *testing.T) {
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := st.Progress.Init("test", 1); err != nil {
		t.Fatalf("Progress.Init: %v", err)
	}
	if err := st.Progress.MarkChapterComplete(1, 9, "", ""); err != nil {
		t.Fatalf("MarkChapterComplete: %v", err)
	}
	if err := st.Drafts.SaveDraft(1, "old prose"); err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}
	progress, err := st.Progress.Load()
	if err != nil {
		t.Fatalf("Load progress: %v", err)
	}
	progress.Flow = domain.FlowRewriting
	progress.PendingRewrites = []int{1}
	if err := st.Progress.Save(progress); err != nil {
		t.Fatalf("Save progress: %v", err)
	}

	raw, err := newWriterDraftChapterTool(st).Execute(t.Context(), writerDraftArgs(t, map[string]any{
		"chapter": 1,
		"content": "authorized replacement prose",
		"mode":    "write",
	}))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var result map[string]any
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if result["written"] != true {
		t.Fatalf("queued rewrite was not written: %s", raw)
	}
	if draft, _ := st.Drafts.LoadDraft(1); draft != "authorized replacement prose" {
		t.Fatalf("queued rewrite did not replace draft: %q", draft)
	}
}

func TestWriterDraftChapterToolRejectsDraftWithoutRequiredCanonicalCharacter(t *testing.T) {
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := st.Progress.Init("test", 1); err != nil {
		t.Fatalf("Progress.Init: %v", err)
	}
	if err := st.Characters.Save([]domain.Character{{
		ID: "lin_shuran", Name: "林舒然", Aliases: []string{"舒然"},
	}}); err != nil {
		t.Fatalf("Save characters: %v", err)
	}
	if err := st.Outline.SaveOutline([]domain.OutlineEntry{{
		Chapter: 1, Title: "Opening", CharacterIDs: []string{"lin_shuran"},
	}}); err != nil {
		t.Fatalf("SaveOutline: %v", err)
	}
	tool := newWriterDraftChapterTool(st)

	_, err := tool.Execute(t.Context(), writerDraftArgs(t, map[string]any{
		"content": "沈渡在灰烬之城找到一枚星痕碎片。",
		"mode":    "write",
	}))
	if err == nil || !strings.Contains(err.Error(), "林舒然") {
		t.Fatalf("expected canonical-character rejection, got %v", err)
	}
	if draft, _ := st.Drafts.LoadDraft(1); draft != "" {
		t.Fatalf("rejected draft was persisted: %q", draft)
	}

	_, err = tool.Execute(t.Context(), writerDraftArgs(t, map[string]any{
		"content": "林舒然推开会议室的门，决定直面今天的谈判。",
		"mode":    "write",
	}))
	if err != nil {
		t.Fatalf("canonical draft rejected: %v", err)
	}
}

func TestWriterCommitWrapperRechecksPersistedDraftIdentity(t *testing.T) {
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := st.Progress.Init("test", 1); err != nil {
		t.Fatalf("Progress.Init: %v", err)
	}
	if err := st.Characters.Save([]domain.Character{{
		ID: "lin_shuran", Name: "林舒然",
	}}); err != nil {
		t.Fatalf("Save characters: %v", err)
	}
	if err := st.Outline.SaveOutline([]domain.OutlineEntry{{
		Chapter: 1, CharacterIDs: []string{"lin_shuran"},
	}}); err != nil {
		t.Fatalf("SaveOutline: %v", err)
	}
	if err := st.Drafts.SaveDraft(1, "沈渡在灰烬之城找到星痕碎片。"); err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}

	probe := &namedWriterProbeTool{name: "commit_chapter"}
	tool := newWriterChapterInferenceTool(probe, st)
	_, err := tool.Execute(t.Context(), json.RawMessage(`{"chapter":1}`))
	if err == nil || !strings.Contains(err.Error(), "林舒然") {
		t.Fatalf("expected commit identity rejection, got %v", err)
	}
	if probe.called {
		t.Fatal("commit tool was called after identity rejection")
	}
}

func TestWriterDraftChapterToolDoesNotInferAmbiguousRewriteQueue(t *testing.T) {
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := st.Progress.Init("test", 2); err != nil {
		t.Fatalf("Progress.Init: %v", err)
	}
	for chapter := 1; chapter <= 2; chapter++ {
		if err := st.Progress.MarkChapterComplete(chapter, 1000, "", ""); err != nil {
			t.Fatalf("MarkChapterComplete(%d): %v", chapter, err)
		}
	}
	progress, err := st.Progress.Load()
	if err != nil {
		t.Fatalf("Load progress: %v", err)
	}
	progress.Flow = domain.FlowRewriting
	progress.PendingRewrites = []int{1, 2}
	if err := st.Progress.Save(progress); err != nil {
		t.Fatalf("Save progress: %v", err)
	}

	tool := newWriterDraftChapterTool(st)
	_, err = tool.Execute(context.Background(), writerDraftArgs(t, map[string]any{
		"content": "ambiguous rewrite prose",
		"mode":    "write",
	}))
	if err == nil || !strings.Contains(err.Error(), "cannot be inferred") {
		t.Fatalf("expected inference rejection, got %v", err)
	}
}

func TestWriterDraftChapterToolSchemaKeepsContentAndModeRequired(t *testing.T) {
	st := store.NewStore(t.TempDir())
	schema := newWriterDraftChapterTool(st).Schema()
	required := schema["required"].([]string)
	if stringSliceContains(required, "chapter") {
		t.Fatalf("chapter should not be schema-required for writer wrapper: %v", required)
	}
	for _, field := range []string{"content", "mode"} {
		if !stringSliceContains(required, field) {
			t.Fatalf("%s should remain required: %v", field, required)
		}
	}
}

func writerDraftArgs(t *testing.T, v any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	return raw
}

func stringSliceContains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

type namedWriterProbeTool struct {
	name   string
	called bool
}

func (t *namedWriterProbeTool) Name() string           { return t.name }
func (t *namedWriterProbeTool) Description() string    { return t.name }
func (t *namedWriterProbeTool) Schema() map[string]any { return map[string]any{} }
func (t *namedWriterProbeTool) Execute(context.Context, json.RawMessage) (json.RawMessage, error) {
	t.called = true
	return json.RawMessage(`{"ok":true}`), nil
}

var _ agentcore.Tool = (*namedWriterProbeTool)(nil)
