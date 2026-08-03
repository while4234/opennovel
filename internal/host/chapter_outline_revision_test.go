package host

import (
	"context"
	"testing"
	"time"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/internal/domain"
)

func TestGenerateChapterOutlineRevisionRetriesMalformedJSON(t *testing.T) {
	model := &chapterOutlineScriptedModel{responses: []string{
		`{"chapter":2,"title":"Broken"}`,
		`{"chapter":99,"title":"新标题","core_event":"新的核心推进","hook":"新的章末悬念","scenes":["新场景一","新场景二"]}`,
	}}
	previousSleep := chapterOutlineRevisionRetrySleep
	chapterOutlineRevisionRetrySleep = func(context.Context, time.Duration) error { return nil }
	t.Cleanup(func() { chapterOutlineRevisionRetrySleep = previousSleep })

	got, err := generateChapterOutlineRevision(context.Background(), model, normalChapterOutlineRevisionSystemPrompt, chapterOutlineRevisionContext{
		Current:     domain.OutlineEntry{Chapter: 2, Title: "旧标题", CoreEvent: "旧事件", Hook: "旧悬念", Scenes: []string{"旧场景"}},
		Instruction: "改成一次谈判失败",
	}, 2)
	if err != nil {
		t.Fatalf("generateChapterOutlineRevision: %v", err)
	}
	if model.calls != 2 {
		t.Fatalf("model calls = %d, want 2", model.calls)
	}
	if got.Chapter != 2 || got.Title != "新标题" || len(got.Scenes) != 2 {
		t.Fatalf("unexpected revised outline: %+v", got)
	}
}

func TestGenerateChapterOutlineRevisionIncludesAdaptationContract(t *testing.T) {
	model := &chapterOutlineScriptedModel{responses: []string{
		`{"chapter":4,"title":"改编新章","core_event":"保留原事件但改换结构","hook":"证物来源成谜","scenes":["重组原事件","加入新的对抗"]}`,
	}}
	_, err := generateChapterOutlineRevision(context.Background(), model, adaptationChapterOutlineRevisionSystemPrompt, chapterOutlineRevisionContext{
		Current: domain.OutlineEntry{Chapter: 4, Title: "旧章", CoreEvent: "旧事件", Hook: "旧钩子", Scenes: []string{"旧场景"}},
		Adaptation: &domain.AdaptationChapterPlan{
			Chapter:         4,
			SourceChapters:  []int{7, 8},
			PreserveEvents:  []string{"保留证物发现"},
			RequiredChanges: []string{"重排叙事顺序"},
			ForbiddenMoves:  []string{"不得照抄原文"},
		},
		Instruction: "加强对抗",
	}, 1)
	if err != nil {
		t.Fatalf("generateChapterOutlineRevision: %v", err)
	}
	if len(model.messages) != 1 {
		t.Fatalf("captured calls = %d, want 1", len(model.messages))
	}
	input := model.messages[0][1].TextContent()
	for _, expected := range []string{"source_chapters", "preserve_events", "required_changes", "forbidden_moves", "加强对抗"} {
		if !containsText(input, expected) {
			t.Fatalf("model input missing %q: %s", expected, input)
		}
	}
}

func TestEqualFormalOutlineEntryNormalizesWhitespace(t *testing.T) {
	a := domain.OutlineEntry{Chapter: 1, Title: " 标题 ", CoreEvent: "事件", Hook: "钩子", Scenes: []string{" 场景一 ", "", "场景二"}}
	b := domain.OutlineEntry{Chapter: 1, Title: "标题", CoreEvent: " 事件 ", Hook: "钩子 ", Scenes: []string{"场景一", "场景二"}}
	if !equalFormalOutlineEntry(a, b) {
		t.Fatal("equivalent outline values should compare equal")
	}
	b.Hook = "新钩子"
	if equalFormalOutlineEntry(a, b) {
		t.Fatal("changed hook should not compare equal")
	}
}

func TestBuildChapterOutlineRevisionResultClassifiesChapterState(t *testing.T) {
	req := ChapterOutlineRevisionRequest{Chapter: 2, Instruction: "change"}
	outline := domain.OutlineEntry{Chapter: 2, Title: "new"}

	completed := buildChapterOutlineRevisionResult(req, outline, &domain.Progress{CompletedChapters: []int{1, 2}})
	if !completed.RewriteQueued || completed.DraftReset || completed.StaleNotice == "" {
		t.Fatalf("completed result = %+v", completed)
	}
	inProgress := buildChapterOutlineRevisionResult(req, outline, &domain.Progress{InProgressChapter: 2})
	if inProgress.RewriteQueued || !inProgress.DraftReset {
		t.Fatalf("in-progress result = %+v", inProgress)
	}
	future := buildChapterOutlineRevisionResult(req, outline, &domain.Progress{CurrentChapter: 1})
	if future.RewriteQueued || future.DraftReset {
		t.Fatalf("future result = %+v", future)
	}
}

type chapterOutlineScriptedModel struct {
	responses []string
	calls     int
	messages  [][]agentcore.Message
}

func (m *chapterOutlineScriptedModel) Generate(_ context.Context, messages []agentcore.Message, _ []agentcore.ToolSpec, _ ...agentcore.CallOption) (*agentcore.LLMResponse, error) {
	m.messages = append(m.messages, append([]agentcore.Message(nil), messages...))
	response := m.responses[m.calls]
	m.calls++
	return &agentcore.LLMResponse{Message: agentcore.Message{
		Role:      agentcore.RoleAssistant,
		Content:   []agentcore.ContentBlock{agentcore.TextBlock(response)},
		Timestamp: time.Now(),
	}}, nil
}

func (m *chapterOutlineScriptedModel) GenerateStream(context.Context, []agentcore.Message, []agentcore.ToolSpec, ...agentcore.CallOption) (<-chan agentcore.StreamEvent, error) {
	stream := make(chan agentcore.StreamEvent)
	close(stream)
	return stream, nil
}

func (m *chapterOutlineScriptedModel) SupportsTools() bool { return false }

func containsText(text, expected string) bool {
	for i := 0; i+len(expected) <= len(text); i++ {
		if text[i:i+len(expected)] == expected {
			return true
		}
	}
	return false
}
