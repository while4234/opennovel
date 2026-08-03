package agents

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/voocel/agentcore"
	corecontext "github.com/voocel/agentcore/context"
)

func TestWriterContextProjectionCommitsCompactedBaseline(t *testing.T) {
	mgr := newContextManager(contextManagerConfig{
		ContextWindow:    4_000,
		ReserveTokens:    2_000,
		KeepRecentTokens: 500,
		Agent:            "writer",
		CommitOnProject:  true,
	})
	messages := []agentcore.AgentMessage{agentcore.UserMsg("current chapter task")}
	for index := range 8 {
		callID := fmt.Sprintf("call-%d", index)
		messages = append(messages,
			agentcore.Message{
				Role: agentcore.RoleAssistant,
				Content: []agentcore.ContentBlock{agentcore.ToolCallBlock(agentcore.ToolCall{
					ID: callID, Name: "read_chapter", Args: []byte(fmt.Sprintf(`{"chapter":%d}`, index+1)),
				})},
			},
			agentcore.ToolResultMsg(callID, []byte(fmt.Sprintf(`"%s"`, strings.Repeat("chapter context ", 1_500))), false),
		)
	}

	projection, err := mgr.Project(context.Background(), messages)
	if err != nil {
		t.Fatalf("Project: %v", err)
	}
	if !projection.ShouldCommit {
		t.Fatal("writer compaction must commit so the next turn cannot restore the oversized history")
	}
	if len(projection.CommitMessages) == 0 {
		t.Fatal("committed writer projection must include the compacted messages")
	}
	snapshot := mgr.Snapshot()
	if snapshot == nil || snapshot.BaselineUsage == nil {
		t.Fatal("writer context snapshot must retain pre-compaction baseline usage")
	}
	if got, before := mgr.Usage().Tokens, snapshot.BaselineUsage.Tokens; got >= before {
		t.Fatalf("compacted usage=%d, baseline=%d; want compacted usage below baseline", got, before)
	}
}

func TestArchitectOverflowRecoveryKeepsValidationErrorAndClearsPriorContext(t *testing.T) {
	messages := []agentcore.AgentMessage{agentcore.UserMsg("expand one arc")}
	for index, toolName := range []string{"novel_context", "save_foundation"} {
		callID := fmt.Sprintf("architect-%d", index)
		messages = append(messages,
			agentcore.Message{
				Role: agentcore.RoleAssistant,
				Content: []agentcore.ContentBlock{agentcore.ToolCallBlock(agentcore.ToolCall{
					ID: callID, Name: toolName, Args: []byte(fmt.Sprintf(`{"batch":%d}`, index)),
				})},
			},
			agentcore.ToolResultMsg(callID, []byte(fmt.Sprintf(`"%s"`, strings.Repeat(toolName+" evidence ", 500))), index == 1),
		)
	}
	cfg := architectToolResultMicrocompactConfig()
	manager := newContextManager(contextManagerConfig{
		ContextWindow:    96_000,
		ReserveTokens:    12_000,
		ToolMicrocompact: cfg,
		ExtraStrategies: []corecontext.Strategy{
			newForceToolResultMicrocompact(*cfg),
		},
	})
	recovery, err := manager.RecoverOverflow(t.Context(), messages, agentcore.ErrContextOverflow)
	if err != nil {
		t.Fatal(err)
	}
	if !recovery.Changed || recovery.Strategy != "force_tool_result_microcompact" {
		t.Fatal("expected prior Architect context to be compacted")
	}
	view := recovery.View
	if first := view[2].(agentcore.Message); first.Metadata["compacted_tool_result"] != true {
		t.Fatalf("prior context result was retained: %+v", first.Metadata)
	}
	if latest := view[4].(agentcore.Message); latest.Metadata["compacted_tool_result"] == true {
		t.Fatalf("latest validation error was cleared: %+v", latest.Metadata)
	}
}

func TestWriterToolResultsCompactByValidationPhase(t *testing.T) {
	messages := []agentcore.AgentMessage{agentcore.UserMsg("polish chapter 39")}
	for index, toolName := range []string{"novel_context", "read_chapter", "check_consistency", "check_de_ai"} {
		callID := fmt.Sprintf("phase-%d", index)
		messages = append(messages,
			agentcore.Message{
				Role: agentcore.RoleAssistant,
				Content: []agentcore.ContentBlock{agentcore.ToolCallBlock(agentcore.ToolCall{
					ID: callID, Name: toolName, Args: []byte(`{"chapter":39}`),
				})},
			},
			agentcore.ToolResultMsg(callID, []byte(fmt.Sprintf(`"%s"`, strings.Repeat(toolName+" evidence ", 300))), false),
		)
	}
	strategy := newWriterValidationPhaseStrategy(*writerToolResultMicrocompactConfig())
	view, result, err := strategy.Apply(context.Background(), messages, messages, corecontext.Budget{})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !result.Applied {
		t.Fatal("expected older Writer phase results to be compacted")
	}
	for _, index := range []int{2, 4} {
		message := view[index].(agentcore.Message)
		if message.Metadata["compacted_tool_result"] != true {
			t.Fatalf("tool result at index %d was not compacted: %+v", index, message.Metadata)
		}
	}
	for _, index := range []int{6, 8} {
		message := view[index].(agentcore.Message)
		if message.Metadata["compacted_tool_result"] == true {
			t.Fatalf("recent validation result at index %d was compacted", index)
		}
	}
	for _, index := range []int{1, 3, 5, 7} {
		calls := view[index].(agentcore.Message).ToolCalls()
		if len(calls) != 1 || string(calls[0].Args) != `{"chapter":39}` {
			t.Fatalf("compaction exposed fabricated tool args at index %d: %+v", index, calls)
		}
	}
}

func TestWriterPhaseKeepsContextAndDraftUntilValidation(t *testing.T) {
	messages := writerPhaseMessages(t, "novel_context", "read_chapter")
	strategy := newWriterValidationPhaseStrategy(*writerToolResultMicrocompactConfig())
	view, result, err := strategy.Apply(t.Context(), messages, messages, corecontext.Budget{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Applied || countCompactedToolResults(view) != 0 {
		t.Fatalf("pre-validation evidence must remain intact: result=%+v compacted=%d", result, countCompactedToolResults(view))
	}
}

func TestWriterPhaseDeduplicatesRepeatedCurrentContextBeforeBoundary(t *testing.T) {
	messages := writerPhaseMessages(t, "novel_context", "novel_context")
	strategy := newWriterValidationPhaseStrategy(*writerToolResultMicrocompactConfig())
	view, result, err := strategy.Apply(t.Context(), messages, messages, corecontext.Budget{})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Applied || countCompactedToolResults(view) != 1 {
		t.Fatalf("duplicate current context result=%+v compacted=%d", result, countCompactedToolResults(view))
	}
	if first := view[2].(agentcore.Message); first.Metadata["compacted_tool_result"] == true {
		t.Fatal("the original authoritative work package must be retained")
	}
	if duplicate := view[4].(agentcore.Message); duplicate.Metadata["compacted_tool_result"] != true {
		t.Fatal("the later duplicate work package must be cleared with its lookup rationale")
	}
}

func TestWriterPhaseBoundsRepeatedContinuityReadsBeforeBoundary(t *testing.T) {
	messages := []agentcore.AgentMessage{agentcore.UserMsg("write chapter 41")}
	for index, spec := range []struct {
		name string
		args string
	}{
		{name: "novel_context", args: `{"chapter":41}`},
		{name: "read_chapter", args: `{"chapter":39}`},
		{name: "read_chapter", args: `{"chapter":40}`},
	} {
		callID := fmt.Sprintf("continuity-%d", index)
		messages = append(messages,
			agentcore.Message{Role: agentcore.RoleAssistant, Content: []agentcore.ContentBlock{
				agentcore.ThinkingBlock(strings.Repeat("historical lookup rationale ", 200)),
				agentcore.ToolCallBlock(agentcore.ToolCall{ID: callID, Name: spec.name, Args: []byte(spec.args)}),
			}},
			agentcore.ToolResultMsg(callID, []byte(fmt.Sprintf(`"%s"`, strings.Repeat(spec.name+" evidence ", 300))), false),
		)
	}

	strategy := newWriterValidationPhaseStrategy(*writerToolResultMicrocompactConfig())
	view, result, err := strategy.Apply(t.Context(), messages, messages, corecontext.Budget{})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Applied {
		t.Fatal("a second normal continuity read must advance the bounded evidence slot")
	}
	if got := countCompactedToolResults(view); got != 1 {
		t.Fatalf("compacted results=%d, want only the older continuity read", got)
	}
	if compacted := view[4].(agentcore.Message); compacted.Metadata["compacted_tool_result"] != true {
		t.Fatalf("older continuity result was retained: %+v", compacted.Metadata)
	}
	if contextResult := view[2].(agentcore.Message); contextResult.Metadata["compacted_tool_result"] == true {
		t.Fatal("active novel_context must remain available while drafting")
	}
	if newestRead := view[6].(agentcore.Message); newestRead.Metadata["compacted_tool_result"] == true {
		t.Fatal("newest continuity tail must remain available")
	}
}

func TestWriterPhaseKeepsDistinctAdaptationSourceReads(t *testing.T) {
	messages := []agentcore.AgentMessage{agentcore.UserMsg("adapt chapter")}
	for index, chapter := range []int{12, 13} {
		callID := fmt.Sprintf("source-%d", index)
		args := []byte(fmt.Sprintf(`{"source":"source","chapter":%d}`, chapter))
		messages = append(messages,
			agentcore.Message{Role: agentcore.RoleAssistant, Content: []agentcore.ContentBlock{agentcore.ToolCallBlock(agentcore.ToolCall{ID: callID, Name: "read_chapter", Args: args})}},
			agentcore.ToolResultMsg(callID, []byte(`"source evidence"`), false),
		)
	}

	strategy := newWriterValidationPhaseStrategy(*writerToolResultMicrocompactConfig())
	view, result, err := strategy.Apply(t.Context(), messages, messages, corecontext.Budget{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Applied || countCompactedToolResults(view) != 0 {
		t.Fatalf("distinct adaptation source reads must remain intact: result=%+v", result)
	}
}

func TestWriterManagerCommitsValidationPhaseBeforeProviderProjection(t *testing.T) {
	messages := writerPhaseMessages(t, "novel_context", "read_chapter", "check_consistency")
	engine := newContextManager(contextManagerConfig{
		ContextWindow:    96_000,
		ReserveTokens:    8_000,
		KeepRecentTokens: 12_000,
		Agent:            "writer",
		CommitOnProject:  true,
	})
	manager := newWriterContextManager(engine, *writerToolResultMicrocompactConfig())
	projection, err := manager.Project(t.Context(), messages)
	if err != nil {
		t.Fatal(err)
	}
	if !projection.ShouldCommit || len(projection.CommitMessages) == 0 {
		t.Fatalf("validation boundary must commit before provider call: %+v", projection)
	}
	if countCompactedToolResults(projection.CommitMessages) != 1 {
		t.Fatalf("committed compacted results=%d, want 1", countCompactedToolResults(projection.CommitMessages))
	}
}

func TestWriterManagerUsesPhaseEvictionForOverflowRecovery(t *testing.T) {
	messages := writerPhaseMessages(t, "novel_context", "read_chapter", "check_consistency")
	engine := newContextManager(contextManagerConfig{
		ContextWindow:    96_000,
		ReserveTokens:    8_000,
		KeepRecentTokens: 12_000,
		Agent:            "writer",
		CommitOnProject:  true,
	})
	manager := newWriterContextManager(engine, *writerToolResultMicrocompactConfig())
	recovery, err := manager.RecoverOverflow(t.Context(), messages, errors.New("compiled request crossed production boundary"))
	if err != nil {
		t.Fatal(err)
	}
	if !recovery.Changed || !recovery.ShouldCommit || recovery.Strategy != "writer_validation_phase" {
		t.Fatalf("unexpected recovery: %+v", recovery)
	}
	if countCompactedToolResults(recovery.View) != 2 {
		t.Fatalf("recovered compacted results=%d, want 2", countCompactedToolResults(recovery.View))
	}
}

func TestCoordinatorForcedRecoveryKeepsOnlyLatestHostTurn(t *testing.T) {
	messages := []agentcore.AgentMessage{agentcore.UserMsg("continue planning")}
	for index, toolName := range []string{"subagent", "subagent"} {
		callID := fmt.Sprintf("coordinator-%d", index)
		messages = append(messages,
			agentcore.Message{Role: agentcore.RoleAssistant, Content: []agentcore.ContentBlock{
				agentcore.ToolCallBlock(agentcore.ToolCall{
					ID: callID, Name: toolName, Args: []byte(fmt.Sprintf(
						`{"task":%q}`, strings.Repeat(fmt.Sprintf("audit-%d-", index), 500),
					)),
				}),
			}},
			agentcore.ToolResultMsg(callID, []byte(fmt.Sprintf(
				`{"receipt":%q}`, strings.Repeat(fmt.Sprintf("durable-%d-", index), 400),
			)), false),
			agentcore.UserMsg(fmt.Sprintf(
				"[Host command] audit chapter %d: %s",
				index+13,
				strings.Repeat(fmt.Sprintf("evidence-%d-", index), 500),
			)),
		)
	}
	strategy := coordinatorLatestHostTurnCheckpoint{}
	view, result, err := strategy.ForceApply(t.Context(), messages, messages, corecontext.Budget{})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Applied {
		t.Fatal("forced Coordinator recovery did not checkpoint the latest Host turn")
	}
	if len(view) != 1 {
		t.Fatalf("recovered messages=%d, want only the current Host instruction", len(view))
	}
	if text := view[0].(agentcore.Message).TextContent(); !strings.Contains(text, "audit chapter 14") ||
		!strings.Contains(text, "evidence-1-") {
		t.Fatalf("latest Host instruction was not preserved exactly: %q", text)
	}
	compiled, err := compileAgentInput(toLLMMessages(t, view), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(compiled) >= 20*1024 {
		t.Fatalf("recovered Coordinator turn=%d bytes, want deterministic headroom", len(compiled))
	}
}

func TestLatestHostTurnManagerCommitsFreshTaskBeforeProjection(t *testing.T) {
	engine := newContextManager(contextManagerConfig{
		ContextWindow:   96_000,
		ReserveTokens:   12_000,
		CommitOnProject: true,
	})
	manager := newLatestHostTurnContextManager(engine)
	messages := []agentcore.AgentMessage{
		agentcore.UserMsg("audit stale chapter"),
		agentcore.Message{Role: agentcore.RoleAssistant, Content: []agentcore.ContentBlock{
			agentcore.ToolCallBlock(agentcore.ToolCall{ID: "old", Name: "novel_context", Args: []byte(`{}`)}),
		}},
		agentcore.ToolResultMsg("old", []byte(strings.Repeat("stale evidence ", 2_000)), false),
		agentcore.UserMsg("audit current repaired chapter"),
	}
	projection, err := manager.Project(t.Context(), messages)
	if err != nil {
		t.Fatal(err)
	}
	if !projection.ShouldCommit || len(projection.CommitMessages) != 1 {
		t.Fatalf("fresh Host turn was not committed: %+v", projection)
	}
	if text := projection.CommitMessages[0].(agentcore.Message).TextContent(); text != "audit current repaired chapter" {
		t.Fatalf("wrong Host task survived: %q", text)
	}
}

func TestWriterPhaseDropsNewestNovelContextAfterValidation(t *testing.T) {
	checkCallID := "failed-de-ai-check"
	contextCallID := "oversized-late-context"
	messages := []agentcore.AgentMessage{
		agentcore.UserMsg("continue chapter validation"),
		agentcore.Message{Role: agentcore.RoleAssistant, Content: []agentcore.ContentBlock{
			agentcore.ToolCallBlock(agentcore.ToolCall{ID: checkCallID, Name: "check_de_ai", Args: []byte(`{"chapter":45}`)}),
		}},
		agentcore.ToolResultMsg(checkCallID, []byte(`{"chapter":45,"passed":false,"finding":"repair punctuation"}`), false),
		agentcore.Message{Role: agentcore.RoleAssistant, Content: []agentcore.ContentBlock{
			agentcore.ToolCallBlock(agentcore.ToolCall{ID: contextCallID, Name: "novel_context", Args: []byte(`{"chapter":45}`)}),
		}},
		agentcore.ToolResultMsg(contextCallID, []byte(fmt.Sprintf(`{"working_memory":%q}`, strings.Repeat("full project context ", 12_000))), false),
	}

	strategy := newWriterValidationPhaseStrategy(*writerToolResultMicrocompactConfig())
	view, result, err := strategy.Apply(t.Context(), messages, messages, corecontext.Budget{})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Applied {
		t.Fatal("post-validation novel_context must not cross the next provider boundary")
	}
	contextResult := view[4].(agentcore.Message)
	if contextResult.Metadata["compacted_tool_result"] != true || !strings.Contains(contextResult.TextContent(), "Prior Writer phase cleared") {
		t.Fatalf("newest novel_context remained protected after validation: %+v", contextResult)
	}
	checkResult := view[2].(agentcore.Message)
	if !strings.Contains(checkResult.TextContent(), `"passed":false`) {
		t.Fatalf("latest validation receipt was not preserved: %q", checkResult.TextContent())
	}
	compiled, err := compileAgentInput(toLLMMessages(t, view), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(compiled) >= 20*1024 {
		t.Fatalf("post-validation request=%d bytes, newest project context was not bounded", len(compiled))
	}
}

func TestWriterPhaseKeepsFreshRecoveryContractAfterHistoricalValidation(t *testing.T) {
	oldCheckID := "old-consistency"
	newContextID := "fresh-context"
	newReadID := "fresh-read"
	messages := []agentcore.AgentMessage{
		agentcore.UserMsg("old validation turn"),
		agentcore.Message{Role: agentcore.RoleAssistant, Content: []agentcore.ContentBlock{
			agentcore.ToolCallBlock(agentcore.ToolCall{ID: oldCheckID, Name: "check_consistency", Args: []byte(`{"chapter":1}`)}),
		}},
		agentcore.ToolResultMsg(oldCheckID, []byte(`{"chapter":1,"passed":true}`), false),
		agentcore.UserMsg("resume the current draft against the authoritative contract"),
		agentcore.Message{Role: agentcore.RoleAssistant, Content: []agentcore.ContentBlock{
			agentcore.ToolCallBlock(agentcore.ToolCall{ID: newContextID, Name: "novel_context", Args: []byte(`{"chapter":1}`)}),
		}},
		agentcore.ToolResultMsg(newContextID, []byte(`{"chapter_contract":{"scenes":["scene 1","scene 2","scene 3","scene 4"]}}`), false),
		agentcore.Message{Role: agentcore.RoleAssistant, Content: []agentcore.ContentBlock{
			agentcore.ToolCallBlock(agentcore.ToolCall{ID: newReadID, Name: "read_chapter", Args: []byte(`{"chapter":1,"source":"draft"}`)}),
		}},
		agentcore.ToolResultMsg(newReadID, []byte(`{"content":"current chapter prose"}`), false),
	}

	strategy := newWriterValidationPhaseStrategy(*writerToolResultMicrocompactConfig())
	view, _, err := strategy.Apply(t.Context(), messages, messages, corecontext.Budget{})
	if err != nil {
		t.Fatal(err)
	}
	contextResult := view[5].(agentcore.Message)
	if contextResult.Metadata["compacted_tool_result"] == true ||
		!strings.Contains(contextResult.TextContent(), `"scene 4"`) {
		t.Fatalf("fresh recovery contract was cleared by a historical receipt: %+v", contextResult)
	}
	readResult := view[7].(agentcore.Message)
	if !strings.Contains(readResult.TextContent(), "current chapter prose") {
		t.Fatalf("fresh draft evidence was lost: %q", readResult.TextContent())
	}
}

func TestWriterManagerProjectsOnlyLatestHostDispatch(t *testing.T) {
	engine := newContextManager(contextManagerConfig{
		ContextWindow:   96_000,
		ReserveTokens:   12_000,
		CommitOnProject: true,
	})
	manager := newWriterContextManager(engine, *writerToolResultMicrocompactConfig())
	messages := []agentcore.AgentMessage{
		agentcore.UserMsg("恢复第 1 章旧草稿"),
		agentcore.Message{Role: agentcore.RoleAssistant, Content: []agentcore.ContentBlock{
			agentcore.TextBlock(strings.Repeat("stale writer history ", 5_000)),
		}},
		agentcore.UserMsg("第 1 章需要重新完成一致性检查"),
		agentcore.Message{Role: agentcore.RoleAssistant, Content: []agentcore.ContentBlock{
			agentcore.TextBlock(strings.Repeat("stale reminder response ", 2_000)),
		}},
		agentcore.UserMsg("恢复第 1 章现有草稿（checkpoint=consistency_check）"),
		agentcore.Message{Role: agentcore.RoleAssistant, Content: []agentcore.ContentBlock{
			agentcore.ToolCallBlock(agentcore.ToolCall{ID: "fresh-context", Name: "novel_context", Args: []byte(`{"chapter":1}`)}),
		}},
		agentcore.ToolResultMsg("fresh-context", []byte(`{"chapter_contract":{"scenes":["scene 1","scene 2","scene 3","scene 4"]}}`), false),
	}

	projection, err := manager.Project(t.Context(), messages)
	if err != nil {
		t.Fatal(err)
	}
	if !projection.ShouldCommit {
		t.Fatal("latest Writer dispatch must replace stale session history")
	}
	compiled, err := compileAgentInput(toLLMMessages(t, projection.Messages), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(compiled) >= 10*1024 {
		t.Fatalf("latest dispatch still contains stale Writer history: %d bytes", len(compiled))
	}
	var joined strings.Builder
	for _, item := range projection.Messages {
		if message, ok := item.(agentcore.Message); ok {
			joined.WriteString(message.TextContent())
		}
	}
	if strings.Contains(joined.String(), "stale writer history") ||
		!strings.Contains(joined.String(), `"scene 4"`) {
		t.Fatalf("wrong Writer dispatch projection: %q", joined.String())
	}
}

func TestWriterOverflowRecoveryDropsNewestNovelContext(t *testing.T) {
	readCallID := "current-draft"
	contextCallID := "overflowing-context"
	messages := []agentcore.AgentMessage{
		agentcore.UserMsg("continue chapter repair"),
		agentcore.Message{Role: agentcore.RoleAssistant, Content: []agentcore.ContentBlock{
			agentcore.ToolCallBlock(agentcore.ToolCall{ID: readCallID, Name: "read_chapter", Args: []byte(`{"chapter":45,"source":"draft"}`)}),
		}},
		agentcore.ToolResultMsg(readCallID, []byte(`{"chapter":45,"content":"current draft evidence"}`), false),
		agentcore.Message{Role: agentcore.RoleAssistant, Content: []agentcore.ContentBlock{
			agentcore.ToolCallBlock(agentcore.ToolCall{ID: contextCallID, Name: "novel_context", Args: []byte(`{"chapter":45}`)}),
		}},
		agentcore.ToolResultMsg(contextCallID, []byte(fmt.Sprintf(`{"working_memory":%q}`, strings.Repeat("oversized context ", 12_000))), false),
	}

	strategy := newWriterValidationPhaseStrategy(*writerToolResultMicrocompactConfig())
	view, result, err := strategy.ForceApply(t.Context(), messages, messages, corecontext.Budget{})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Applied {
		t.Fatal("overflow recovery must evict the newest novel_context result")
	}
	if contextResult := view[4].(agentcore.Message); contextResult.Metadata["compacted_tool_result"] != true {
		t.Fatalf("overflow recovery protected the largest newest result: %+v", contextResult)
	}
	if readResult := view[2].(agentcore.Message); !strings.Contains(readResult.TextContent(), "current draft evidence") {
		t.Fatalf("overflow recovery lost the current draft evidence: %q", readResult.TextContent())
	}
	compiled, err := compileAgentInput(toLLMMessages(t, view), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(compiled) >= 20*1024 {
		t.Fatalf("overflow recovery request=%d bytes, want deterministic headroom", len(compiled))
	}
}

func TestWriterPhaseCompactsPersistedDraftArgumentsImmediately(t *testing.T) {
	callID := "draft-write"
	messages := []agentcore.AgentMessage{
		agentcore.UserMsg("polish chapter"),
		agentcore.Message{Role: agentcore.RoleAssistant, Content: []agentcore.ContentBlock{
			agentcore.ThinkingBlock(strings.Repeat("old drafting rationale ", 500)),
			agentcore.ToolCallBlock(agentcore.ToolCall{
				ID: callID, Name: "draft_chapter", Args: []byte(fmt.Sprintf(`{"chapter":39,"content":%q}`, strings.Repeat("chapter prose ", 1_200))),
			}),
		}},
		agentcore.ToolResultMsg(callID, []byte(`{"written":true,"chapter":39,"runaway_safety_passed":true,"next_step":"read_chapter then validate"}`), false),
	}
	strategy := newWriterValidationPhaseStrategy(*writerToolResultMicrocompactConfig())
	before := corecontext.EstimateTotal(messages)
	view, result, err := strategy.Apply(t.Context(), messages, messages, corecontext.Budget{})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Applied {
		t.Fatal("persisted draft arguments must be evicted before the next provider call")
	}
	assistant := view[1].(agentcore.Message)
	if calls := assistant.ToolCalls(); len(calls) != 0 {
		t.Fatalf("persisted draft call must be removed from model history: %+v", calls)
	}
	if assistant.ThinkingContent() != "" {
		t.Fatal("completed drafting rationale must not cross the persisted-write boundary")
	}
	if !strings.Contains(assistant.TextContent(), "Latest completed Writer write receipt") ||
		!strings.Contains(assistant.TextContent(), `"runaway_safety_passed":true`) ||
		!strings.Contains(assistant.TextContent(), "read_chapter then validate") {
		t.Fatalf("collapsed draft turn lost its actionable receipt: %q", assistant.TextContent())
	}
	for _, message := range view {
		if typed, ok := message.(agentcore.Message); ok && typed.Role == agentcore.RoleTool {
			t.Fatal("persisted draft result must be removed with its originating call")
		}
	}
	if after := corecontext.EstimateTotal(view); after >= before-5_000 {
		t.Fatalf("draft phase saved only %d tokens, want a whole-payload reduction", before-after)
	}
	second, secondResult, err := strategy.Apply(t.Context(), view, view, corecontext.Budget{})
	if err != nil {
		t.Fatal(err)
	}
	if secondResult.Applied || corecontext.EstimateTotal(second) != corecontext.EstimateTotal(view) {
		t.Fatalf("phase compaction must be idempotent: %+v", secondResult)
	}
}

func TestWriterPhaseDropsCompletedPlanRationaleBeforeNextProviderCall(t *testing.T) {
	draftCallID := "draft-write"
	planCallID := "replacement-plan"
	messages := []agentcore.AgentMessage{
		agentcore.UserMsg("write chapter 45"),
		agentcore.Message{Role: agentcore.RoleAssistant, Content: []agentcore.ContentBlock{
			agentcore.ToolCallBlock(agentcore.ToolCall{
				ID: draftCallID, Name: "draft_chapter", Args: []byte(`{"chapter":45,"content":"stored prose"}`),
			}),
		}},
		agentcore.ToolResultMsg(draftCallID, []byte(`{"written":true,"chapter":45}`), false),
		agentcore.Message{Role: agentcore.RoleAssistant, Content: []agentcore.ContentBlock{
			agentcore.ThinkingBlock(strings.Repeat("redundant plan deliberation ", 2_000)),
			agentcore.TextBlock("I will now save the replacement plan."),
			agentcore.ToolCallBlock(agentcore.ToolCall{
				ID: planCallID, Name: "plan_chapter", Args: []byte(`{"chapter":45,"title":"Witness席"}`),
			}),
		}},
		agentcore.ToolResultMsg(planCallID, []byte(`{"planned":true,"chapter":45}`), false),
	}

	strategy := newWriterValidationPhaseStrategy(*writerToolResultMicrocompactConfig())
	before := corecontext.EstimateTotal(messages)
	view, result, err := strategy.Apply(t.Context(), messages, messages, corecontext.Budget{})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Applied {
		t.Fatal("completed plan rationale must be removed after the preceding draft is persisted")
	}
	planMessage := view[2].(agentcore.Message)
	if planMessage.ThinkingContent() != "" || planMessage.TextContent() != "" {
		t.Fatalf("completed plan retained redundant rationale: thinking=%d text=%q", len(planMessage.ThinkingContent()), planMessage.TextContent())
	}
	calls := planMessage.ToolCalls()
	if len(calls) != 1 || calls[0].Name != "plan_chapter" || string(calls[0].Args) != `{"chapter":45,"title":"Witness席"}` {
		t.Fatalf("completed plan lost its schema-valid decision record: %+v", calls)
	}
	if resultText := view[3].(agentcore.Message).TextContent(); !strings.Contains(resultText, `"planned":true`) {
		t.Fatalf("completed plan lost its receipt: %q", resultText)
	}
	if after := corecontext.EstimateTotal(view); after >= before-10_000 {
		t.Fatalf("completed plan rationale saved only %d tokens", before-after)
	}
}

func TestWriterPhaseDropsCompletedPlanRationaleDuringNewChapterCreation(t *testing.T) {
	planCallID := "new-chapter-plan"
	messages := []agentcore.AgentMessage{
		agentcore.UserMsg("write chapter 46"),
		agentcore.Message{Role: agentcore.RoleAssistant, Content: []agentcore.ContentBlock{
			agentcore.ThinkingBlock(strings.Repeat("private planning deliberation ", 2_000)),
			agentcore.TextBlock("I will save the chapter plan."),
			agentcore.ToolCallBlock(agentcore.ToolCall{
				ID: planCallID, Name: "plan_chapter", Args: []byte(`{"chapter":46,"title":"A Paper Confirmation"}`),
			}),
		}},
		agentcore.ToolResultMsg(planCallID, []byte(`{"planned":true,"chapter":46}`), false),
	}

	strategy := newWriterValidationPhaseStrategy(*writerToolResultMicrocompactConfig())
	before := corecontext.EstimateTotal(messages)
	view, result, err := strategy.Apply(t.Context(), messages, messages, corecontext.Budget{})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Applied {
		t.Fatal("a completed new-chapter plan must drop its private rationale before drafting")
	}
	planMessage := view[1].(agentcore.Message)
	if planMessage.ThinkingContent() != "" || planMessage.TextContent() != "" {
		t.Fatalf("completed new-chapter plan retained rationale: thinking=%d text=%q", len(planMessage.ThinkingContent()), planMessage.TextContent())
	}
	calls := planMessage.ToolCalls()
	if len(calls) != 1 || calls[0].Name != "plan_chapter" || string(calls[0].Args) != `{"chapter":46,"title":"A Paper Confirmation"}` {
		t.Fatalf("completed new-chapter plan lost its decision record: %+v", calls)
	}
	if resultText := view[2].(agentcore.Message).TextContent(); !strings.Contains(resultText, `"planned":true`) {
		t.Fatalf("completed new-chapter plan lost its receipt: %q", resultText)
	}
	if after := corecontext.EstimateTotal(view); after >= before-10_000 {
		t.Fatalf("completed new-chapter rationale saved only %d tokens", before-after)
	}
}

func TestWriterPhaseKeepsSchemaValidArgumentsForClearedResults(t *testing.T) {
	messages := writerPhaseMessages(t, "novel_context", "read_chapter", "check_consistency")
	legacyResult := messages[2].(agentcore.Message)
	legacyResult.Metadata["compacted_tool_result"] = true
	messages[2] = legacyResult
	strategy := newWriterValidationPhaseStrategy(*writerToolResultMicrocompactConfig())
	view, result, err := strategy.Apply(t.Context(), messages, messages, corecontext.Budget{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Applied {
		t.Fatalf("already-cleared result with small valid args must be stable: %+v", result)
	}
	call := view[1].(agentcore.Message).ToolCalls()[0]
	if string(call.Args) != `{"chapter":39}` {
		t.Fatalf("cleared result fabricated invalid call args: %s", call.Args)
	}
}

func TestWriterPhaseCleanRestartsAfterRepeatedContractError(t *testing.T) {
	messages := []agentcore.AgentMessage{agentcore.UserMsg("write chapter 11")}
	for index := 0; index < 2; index++ {
		callID := fmt.Sprintf("consistency-error-%d", index)
		messages = append(messages,
			agentcore.Message{Role: agentcore.RoleAssistant, Content: []agentcore.ContentBlock{
				agentcore.ThinkingBlock(strings.Repeat("repairing the rejected quote ", 200)),
				agentcore.ToolCallBlock(agentcore.ToolCall{
					ID: callID, Name: "check_consistency", Args: []byte(`{"chapter":11}`),
				}),
			}},
			agentcore.ToolResultMsg(callID, []byte(`scene_checks[2].evidence is not an exact current-draft quote of at least 8 characters`), true),
		)
	}

	strategy := newWriterValidationPhaseStrategy(*writerToolResultMicrocompactConfig())
	view, result, err := strategy.Apply(t.Context(), messages, messages, corecontext.Budget{})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Applied || result.Name != "writer_clean_error_recovery" {
		t.Fatalf("repeated contract error did not trigger clean recovery: %+v", result)
	}
	if len(view) != 2 {
		t.Fatalf("clean recovery retained polluted history: %d messages", len(view))
	}
	recovery := view[1].(agentcore.Message)
	if recovery.Role != agentcore.RoleUser || !strings.Contains(recovery.TextContent(), writerCleanRecoveryMarker) {
		t.Fatalf("missing clean recovery instruction: %+v", recovery)
	}
	if strings.Contains(recovery.TextContent(), "scene_checks[2]") {
		t.Fatal("clean recovery copied the rejected argument context")
	}
}

func TestWriterPhaseCountsCompactedContractErrorTowardCleanRecovery(t *testing.T) {
	firstCallID := "compacted-consistency-error"
	firstResult := agentcore.ToolResultMsg(
		firstCallID,
		[]byte(`scene_checks[0].evidence is not an exact current-draft quote of at least 8 characters`),
		true,
	)
	firstResult.Metadata["compacted_tool_result"] = true
	firstResult.Metadata[writerToolErrorFingerprintMetadata] = "check_consistency:exact_quote"

	secondCallID := "current-consistency-error"
	messages := []agentcore.AgentMessage{
		agentcore.UserMsg("write chapter 12"),
		agentcore.Message{Role: agentcore.RoleAssistant, Content: []agentcore.ContentBlock{
			agentcore.ToolCallBlock(agentcore.ToolCall{
				ID: firstCallID, Name: "check_consistency", Args: []byte(`{"chapter":12}`),
			}),
		}},
		firstResult,
		agentcore.Message{Role: agentcore.RoleAssistant, Content: []agentcore.ContentBlock{
			agentcore.ToolCallBlock(agentcore.ToolCall{
				ID: secondCallID, Name: "check_consistency", Args: []byte(`{"chapter":12}`),
			}),
		}},
		agentcore.ToolResultMsg(
			secondCallID,
			[]byte(`scene_checks[1].evidence is not an exact current-draft quote of at least 8 characters`),
			true,
		),
	}

	strategy := newWriterValidationPhaseStrategy(*writerToolResultMicrocompactConfig())
	view, result, err := strategy.Apply(t.Context(), messages, messages, corecontext.Budget{})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Applied || result.Name != "writer_clean_error_recovery" {
		t.Fatalf("compacted and current errors did not trigger clean recovery: %+v", result)
	}
	if len(view) != 2 {
		t.Fatalf("clean recovery retained polluted history: %d messages", len(view))
	}
}

func TestWriterPhasePersistsFingerprintWhenCompactingToolError(t *testing.T) {
	errorCallID := "old-consistency-error"
	messages := []agentcore.AgentMessage{
		agentcore.UserMsg("write chapter 12"),
		agentcore.Message{Role: agentcore.RoleAssistant, Content: []agentcore.ContentBlock{
			agentcore.ToolCallBlock(agentcore.ToolCall{
				ID: errorCallID, Name: "check_consistency", Args: []byte(`{"chapter":12}`),
			}),
		}},
		agentcore.ToolResultMsg(
			errorCallID,
			[]byte(`scene_checks[0].evidence is not an exact current-draft quote of at least 8 characters`),
			true,
		),
	}
	for index, toolName := range []string{"novel_context", "read_chapter"} {
		callID := fmt.Sprintf("newer-result-%d", index)
		messages = append(messages,
			agentcore.Message{Role: agentcore.RoleAssistant, Content: []agentcore.ContentBlock{
				agentcore.ToolCallBlock(agentcore.ToolCall{
					ID: callID, Name: toolName, Args: []byte(`{"chapter":12}`),
				}),
			}},
			agentcore.ToolResultMsg(callID, []byte(`{"ok":true}`), false),
		)
	}

	strategy := newWriterValidationPhaseStrategy(*writerToolResultMicrocompactConfig())
	view, result, err := strategy.ForceApply(t.Context(), messages, messages, corecontext.Budget{})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Applied {
		t.Fatalf("expected old error result to be compacted: %+v", result)
	}
	compacted := view[2].(agentcore.Message)
	if compacted.Metadata["compacted_tool_result"] != true {
		t.Fatalf("old error result was not compacted: %+v", compacted.Metadata)
	}
	if got := compacted.Metadata[writerToolErrorFingerprintMetadata]; got != "check_consistency:exact_quote" {
		t.Fatalf("error fingerprint = %v", got)
	}
}

func TestWriterPhaseStopsPollutedDispatchAfterCleanRecoveryAlsoFails(t *testing.T) {
	messages := []agentcore.AgentMessage{
		agentcore.UserMsg("write chapter 12"),
		agentcore.UserMsg(writerCleanRecoveryMarker + "\nresume from durable state"),
	}
	for index := 0; index < 2; index++ {
		callID := fmt.Sprintf("post-clean-consistency-error-%d", index)
		messages = append(messages,
			agentcore.Message{Role: agentcore.RoleAssistant, Content: []agentcore.ContentBlock{
				agentcore.ToolCallBlock(agentcore.ToolCall{
					ID: callID, Name: "check_consistency", Args: []byte(`{"chapter":12}`),
				}),
			}},
			agentcore.ToolResultMsg(callID, []byte(`scene_checks[2].evidence is not an exact current-draft quote of at least 8 characters`), true),
		)
	}

	strategy := newWriterValidationPhaseStrategy(*writerToolResultMicrocompactConfig())
	_, result, err := strategy.Apply(t.Context(), messages, messages, corecontext.Budget{})
	if !errors.Is(err, errWriterCleanRecoveryExhausted) {
		t.Fatalf("post-clean repeated errors must terminate the polluted dispatch, got %v", err)
	}
	if result.Name != "writer_clean_error_recovery_exhausted" {
		t.Fatalf("unexpected recovery strategy: %+v", result)
	}
}

func TestWriterValidationReceiptRequiresSuccessfulToolResult(t *testing.T) {
	callID := "failed-check"
	messages := []agentcore.AgentMessage{
		agentcore.UserMsg("write chapter 11"),
		agentcore.Message{Role: agentcore.RoleAssistant, Content: []agentcore.ContentBlock{
			agentcore.ToolCallBlock(agentcore.ToolCall{ID: callID, Name: "check_consistency", Args: []byte(`{"chapter":11}`)}),
		}},
		agentcore.ToolResultMsg(callID, []byte(`scene_checks count mismatch`), true),
	}
	if hasWriterValidationReceipt(messages) {
		t.Fatal("failed validation tool result must not advance the validation phase")
	}
}

func TestWriterOverflowRecoveryEvictsWholePriorPhase(t *testing.T) {
	messages := []agentcore.AgentMessage{agentcore.UserMsg(strings.Repeat("baseline ", 2_200))}
	for index, toolName := range []string{"novel_context", "read_chapter", "check_consistency"} {
		callID := fmt.Sprintf("boundary-%d", index)
		repetitions := 900
		if toolName == "read_chapter" {
			repetitions = 400
		}
		if toolName == "check_consistency" {
			repetitions = 80
		}
		messages = append(messages,
			agentcore.Message{Role: agentcore.RoleAssistant, Content: []agentcore.ContentBlock{agentcore.ToolCallBlock(agentcore.ToolCall{ID: callID, Name: toolName, Args: []byte(`{"chapter":39}`)})}},
			agentcore.ToolResultMsg(callID, []byte(fmt.Sprintf(`"%s"`, strings.Repeat(toolName+" evidence ", repetitions))), false),
		)
	}

	strategy := newWriterValidationPhaseStrategy(*writerToolResultMicrocompactConfig())
	view, result, err := strategy.ForceApply(t.Context(), messages, messages, corecontext.Budget{})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Applied || countCompactedToolResults(view) != 2 {
		t.Fatalf("forced phase eviction result=%+v compacted=%d", result, countCompactedToolResults(view))
	}
	compiled, err := compileAgentInput(toLLMMessages(t, view), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(compiled) >= 40*1024 {
		t.Fatalf("post-phase request=%d bytes, want at least 20 KiB production headroom", len(compiled))
	}
}

func writerPhaseMessages(t *testing.T, tools ...string) []agentcore.AgentMessage {
	t.Helper()
	messages := []agentcore.AgentMessage{agentcore.UserMsg("polish chapter")}
	for index, toolName := range tools {
		callID := fmt.Sprintf("phase-helper-%d", index)
		messages = append(messages,
			agentcore.Message{Role: agentcore.RoleAssistant, Content: []agentcore.ContentBlock{agentcore.ToolCallBlock(agentcore.ToolCall{ID: callID, Name: toolName, Args: []byte(`{"chapter":39}`)})}},
			agentcore.ToolResultMsg(callID, []byte(`"evidence"`), false),
		)
	}
	return messages
}

func toLLMMessages(t *testing.T, messages []agentcore.AgentMessage) []agentcore.Message {
	t.Helper()
	converted := corecontext.NewEngine(corecontext.EngineConfig{ContextWindow: 96_000}).ConvertToLLM(messages)
	if len(converted) == 0 {
		t.Fatal("converted Writer prompt is empty")
	}
	return converted
}

func TestContextSummaryKeepsChineseToolResultsValidUTF8(t *testing.T) {
	model := &utf8CheckingSummaryModel{}
	mgr := corecontext.NewEngine(corecontext.EngineConfig{
		ContextWindow: 800,
		ReserveTokens: 400,
		Strategies: []corecontext.Strategy{corecontext.NewFullSummary(corecontext.FullSummaryConfig{
			Model:            model,
			KeepRecentTokens: 5_000,
		})},
	})
	messages := []agentcore.AgentMessage{agentcore.UserMsg("继续创作")}
	for index := range 8 {
		callID := fmt.Sprintf("novel-context-%d", index)
		messages = append(messages,
			agentcore.UserMsg(fmt.Sprintf("第 %d 轮", index+1)),
			agentcore.Message{
				Role: agentcore.RoleAssistant,
				Content: []agentcore.ContentBlock{agentcore.ToolCallBlock(agentcore.ToolCall{
					ID: callID, Name: "novel_context", Args: []byte(`{}`),
				})},
			},
			agentcore.ToolResultMsg(callID, []byte(fmt.Sprintf(`"%s"`, strings.Repeat("武林上下文", 400))), false),
		)
	}

	if _, err := mgr.Compact(context.Background(), messages, agentcore.CompactReasonManual); err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if model.calls == 0 {
		t.Fatal("expected context summarization model to be called")
	}
}

func TestSummaryCompatibleModelOmitsForcedThinkingOff(t *testing.T) {
	model := &utf8CheckingSummaryModel{}
	wrapped := summaryCompatibleModel(model, "writer", nil)
	if _, err := wrapped.Generate(context.Background(), nil, nil, agentcore.WithMaxTokens(800), agentcore.WithThinking(agentcore.ThinkingOff)); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if model.callConfig.MaxTokens != 800 {
		t.Fatalf("max tokens = %d, want 800", model.callConfig.MaxTokens)
	}
	if model.callConfig.ThinkingLevel != agentcore.ThinkingAuto {
		t.Fatalf("thinking level = %q, want provider-default auto", model.callConfig.ThinkingLevel)
	}
}

func TestSummaryCompatibleModelRetriesEmptyContent(t *testing.T) {
	model := &utf8CheckingSummaryModel{emptyResponses: 1}
	var retries []int
	wrapped := summaryCompatibleModel(model, "coordinator", func(agent string, retry, maxRetries int, _ time.Duration) {
		if agent != "coordinator" || maxRetries != summaryMaxAttempts-1 {
			t.Fatalf("retry hook = agent %q max %d", agent, maxRetries)
		}
		retries = append(retries, retry)
	})
	summary := wrapped.(*summaryModel)
	summary.delay = func(int) time.Duration { return 0 }
	summary.wait = func(context.Context, time.Duration) error { return nil }

	response, err := wrapped.Generate(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if model.calls != 2 {
		t.Fatalf("calls = %d, want 2", model.calls)
	}
	if !summaryResponseHasContent(response) {
		t.Fatal("retry did not return the later non-empty summary")
	}
	if len(retries) != 1 || retries[0] != 1 {
		t.Fatalf("retries = %v, want [1]", retries)
	}
}

func TestSummaryCompatibleModelStopsRetryWhenCanceled(t *testing.T) {
	model := &utf8CheckingSummaryModel{emptyResponses: summaryMaxAttempts}
	ctx, cancel := context.WithCancel(context.Background())
	wrapped := summaryCompatibleModel(model, "coordinator", nil).(*summaryModel)
	wrapped.delay = func(int) time.Duration { return time.Second }
	wrapped.wait = func(context.Context, time.Duration) error {
		cancel()
		return ctx.Err()
	}

	if _, err := wrapped.Generate(ctx, nil, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("Generate error = %v, want context.Canceled", err)
	}
	if model.calls != 1 {
		t.Fatalf("calls = %d, want 1", model.calls)
	}
}

type utf8CheckingSummaryModel struct {
	calls          int
	callConfig     agentcore.CallConfig
	emptyResponses int
}

func (m *utf8CheckingSummaryModel) Generate(_ context.Context, messages []agentcore.Message, _ []agentcore.ToolSpec, opts ...agentcore.CallOption) (*agentcore.LLMResponse, error) {
	m.calls++
	m.callConfig = agentcore.ResolveCallConfig(opts)
	if m.calls <= m.emptyResponses {
		return &agentcore.LLMResponse{Message: agentcore.Message{Role: agentcore.RoleAssistant}}, nil
	}
	for index, message := range messages {
		for _, block := range message.Content {
			if block.Type == agentcore.ContentText && !utf8.ValidString(block.Text) {
				return nil, fmt.Errorf("message %d contains invalid UTF-8", index)
			}
		}
	}
	return &agentcore.LLMResponse{Message: agentcore.Message{
		Role:       agentcore.RoleAssistant,
		Content:    []agentcore.ContentBlock{agentcore.TextBlock("<summary>保留当前任务与关键上下文。</summary>")},
		StopReason: agentcore.StopReasonStop,
	}}, nil
}

func (m *utf8CheckingSummaryModel) GenerateStream(context.Context, []agentcore.Message, []agentcore.ToolSpec, ...agentcore.CallOption) (<-chan agentcore.StreamEvent, error) {
	return nil, errors.New("streaming is not used for context summaries")
}

func (m *utf8CheckingSummaryModel) SupportsTools() bool { return false }
