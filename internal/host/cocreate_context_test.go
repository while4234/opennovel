package host

import (
	"strings"
	"testing"

	"github.com/voocel/agentcore"
)

func TestCompileCoCreateMessagesCompactsRepeatedDraftsWithoutMutatingHistory(t *testing.T) {
	const repeated = 900
	history := []CoCreateMessage{{Role: "user", Content: "最初设定：都市婚姻悬疑。"}}
	for round := 1; round <= 6; round++ {
		draft := strings.Repeat("既有设定", repeated) + "\n草稿轮次标记：" + strings.Repeat("轮", round)
		if round == 6 {
			draft += "\n最终草稿唯一标记：男主逃婚后进入公司商战。"
		}
		history = append(history,
			CoCreateMessage{Role: "assistant", Content: coCreateTestResponse("第"+strings.Repeat("一", round)+"轮回复", draft, true, "")},
			CoCreateMessage{Role: "user", Content: "第" + strings.Repeat("一", round) + "轮用户补充"},
		)
	}
	originalLast := history[len(history)-2].Content

	messages := compileCoCreateMessages("system", history)
	compiledBytes := coCreateCompiledRequestBytes("system", messages)
	if compiledBytes > 40*1024 {
		t.Fatalf("compiled request = %d bytes, target %d", compiledBytes, 40*1024)
	}
	compiled := coCreateMessagesText(messages)
	wants := []string{
		"最初设定：都市婚姻悬疑。",
		"最终草稿唯一标记：男主逃婚后进入公司商战。",
	}
	for round := 1; round <= 6; round++ {
		wants = append(wants,
			"第"+strings.Repeat("一", round)+"轮回复",
			"第"+strings.Repeat("一", round)+"轮用户补充",
		)
	}
	for _, want := range wants {
		if !strings.Contains(compiled, want) {
			t.Fatalf("compacted request lost %q", want)
		}
	}
	for round := 1; round < 6; round++ {
		if strings.Contains(compiled, "草稿轮次标记："+strings.Repeat("轮", round)+"\n") {
			t.Fatalf("compacted request still contains superseded draft %d", round)
		}
	}
	if history[len(history)-2].Content != originalLast {
		t.Fatal("request compaction mutated durable co-create history")
	}
}

func TestCompileCoCreateMessagesDropsLegacyCastButKeepsLatestDraft(t *testing.T) {
	legacyCast := `{"members":[` + strings.Repeat(`{"notes":"旧角色资料"},`, 3000) + `{}]}`
	history := []CoCreateMessage{
		{Role: "user", Content: "初始需求"},
		{Role: "assistant", Content: coCreateTestResponse("已整理", "完整当前设定", true, legacyCast)},
		{Role: "user", Content: "加个男主心腹助理"},
	}

	system := strings.Repeat("system", 1000)
	messages := compileCoCreateMessages(system, history)
	compiled := coCreateMessagesText(messages)
	if !strings.Contains(compiled, "完整当前设定") || !strings.Contains(compiled, "加个男主心腹助理") {
		t.Fatal("compacted legacy request lost the current draft or latest user turn")
	}
	if strings.Contains(compiled, "旧角色资料") {
		t.Fatal("legacy cast was resent even though it is persisted outside the co-create prompt")
	}
	if coCreateCompiledRequestBytes(system, messages) > 40*1024 {
		t.Fatal("legacy cast compaction did not restore request headroom")
	}
}

func TestCompileCoCreateMessagesCanonicalizesSmallHistoryWithoutLosingDiscussion(t *testing.T) {
	history := []CoCreateMessage{
		{Role: "user", Content: "初始需求"},
		{Role: "assistant", Content: coCreateTestResponse("回复", "草稿", false, "")},
		{Role: "user", Content: "补充"},
	}
	messages := compileCoCreateMessages("system", history)
	if len(messages) != len(history)+1 {
		t.Fatalf("message count = %d, want %d", len(messages), len(history)+1)
	}
	compiled := coCreateMessagesText(messages)
	for _, want := range []string{"初始需求", "回复", "草稿", "补充"} {
		if !strings.Contains(compiled, want) {
			t.Fatalf("canonical small history lost %q", want)
		}
	}
}

func TestCompileCoCreateMessagesPreservesUnstructuredAssistantReplies(t *testing.T) {
	history := []CoCreateMessage{
		{Role: "user", Content: "初始需求"},
		{Role: "assistant", Content: "这是一条旧格式但对用户可见的完整回复"},
		{Role: "user", Content: "继续"},
		{Role: "assistant", Content: coCreateTestResponse("已整理", "最新完整草稿", true, "")},
		{Role: "user", Content: "再补充"},
	}
	compiled := coCreateMessagesText(compileCoCreateMessages("system", history))
	if !strings.Contains(compiled, "这是一条旧格式但对用户可见的完整回复") {
		t.Fatal("canonical history lost an unstructured assistant reply")
	}
}

func coCreateTestResponse(reply, draft string, ready bool, cast string) string {
	parts := []string{"<reply>" + reply + "</reply>", "<draft>" + draft + "</draft>"}
	if cast != "" {
		parts = append(parts, "<cast>"+cast+"</cast>")
	}
	readyText := "false"
	if ready {
		readyText = "true"
	}
	return strings.Join(append(parts,
		"<ready>"+readyText+"</ready>",
		"<suggestions>- 下一步</suggestions>",
	), "")
}

func coCreateMessagesText(messages []agentcore.Message) string {
	var text strings.Builder
	for _, message := range messages {
		text.WriteString(message.TextContent())
		text.WriteByte('\n')
	}
	return text.String()
}
