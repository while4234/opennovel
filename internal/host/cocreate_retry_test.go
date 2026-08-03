package host

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/internal/bootstrap"
	"github.com/voocel/ainovel-cli/internal/retrypolicy"
	"github.com/voocel/litellm"
)

func TestCoCreateRetryUsesSharedPolicy(t *testing.T) {
	if coCreateMaxAttempts != retrypolicy.MaxAttempts {
		t.Fatalf("coCreateMaxAttempts=%d, want %d", coCreateMaxAttempts, retrypolicy.MaxAttempts)
	}
	if got := coCreateRetryDelay(1); got != retrypolicy.Delay(1) {
		t.Fatalf("retry delay=%s, want %s", got, retrypolicy.Delay(1))
	}
}

func TestAdaptCoCreatePromptKeepsDraftBriefLevel(t *testing.T) {
	for _, want := range []string{
		"压缩执行契约",
		"不要写逐章策略",
		"原著章节号只作为锚点",
	} {
		if !strings.Contains(adaptCoCreateSystemPrompt, want) {
			t.Fatalf("adapt co-create prompt should contain %q", want)
		}
	}
	for _, oldHeading := range []string{
		"\"## 用户目标\"",
		"\"## 主线保留规则\"",
		"\"## 角色/关系改动\"",
	} {
		if strings.Contains(adaptCoCreateSystemPrompt, oldHeading) {
			t.Fatalf("adapt co-create prompt should not recommend old verbose heading %q", oldHeading)
		}
	}
}

func TestCoCreateStreamRetriesTransientStreamEOF(t *testing.T) {
	restore := stubCoCreateRetrySleep(t)
	defer restore()

	model := &scriptedCoCreateModel{
		streams: [][]agentcore.StreamEvent{
			{
				{Type: agentcore.StreamEventTextDelta, Delta: "<reply>partial"},
				{Type: agentcore.StreamEventError, Err: io.ErrUnexpectedEOF},
			},
			{
				{Type: agentcore.StreamEventTextDelta, Delta: validCoCreateXML("ok")},
				{Type: agentcore.StreamEventDone},
			},
		},
	}
	var progress []coCreateProgress

	reply, err := coCreateStream(
		context.Background(),
		newCoCreateModelSet(model),
		nil,
		time.Second,
		bootstrap.DefaultCoCreateMaxTokens,
		"system",
		[]CoCreateMessage{{Role: "user", Content: "start"}},
		func(kind, text string) {
			progress = append(progress, coCreateProgress{kind: kind, text: text})
		},
	)
	if err != nil {
		t.Fatalf("coCreateStream: %v", err)
	}
	if model.streamCalls != 2 {
		t.Fatalf("stream calls = %d, want 2", model.streamCalls)
	}
	if reply.Message != "ok" || reply.Prompt != "## plan" || !reply.Ready {
		t.Fatalf("reply = %+v", reply)
	}
	if !hasCoCreateProgress(progress, CoCreateProgressReply, "") {
		t.Fatalf("retry should clear the partial reply preview, progress=%+v", progress)
	}
}

func TestOriginalCoCreateFakeModelCompletesWithoutFinalCast(t *testing.T) {
	model := &scriptedCoCreateModel{streams: [][]agentcore.StreamEvent{{
		{Type: agentcore.StreamEventTextDelta, Delta: coCreateXMLWithSuggestions("ready", "keep the investigator")},
		{Type: agentcore.StreamEventDone},
	}}}
	reply, err := coCreateStream(
		context.Background(),
		newCoCreateModelSet(model),
		nil,
		time.Second,
		bootstrap.DefaultCoCreateMaxTokens,
		coCreateSystemPrompt,
		[]CoCreateMessage{{Role: "user", Content: "a mystery about family loyalty"}},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if reply.CoreCast != nil || reply.Prompt != "## plan" || !reply.Ready || model.streamCalls != 1 {
		t.Fatalf("original fake-model reply = %+v, stream_calls=%d", reply, model.streamCalls)
	}
}

func TestCoCreateStreamRetriesProviderGatewayError(t *testing.T) {
	restore := stubCoCreateRetrySleep(t)
	defer restore()

	model := &scriptedCoCreateModel{
		streams: [][]agentcore.StreamEvent{
			{
				{Type: agentcore.StreamEventError, Err: litellm.NewHTTPError("deepseek", 502, "<html><body>502 Bad Gateway</body></html>")},
			},
			{
				{Type: agentcore.StreamEventTextDelta, Delta: validCoCreateXML("ok")},
				{Type: agentcore.StreamEventDone},
			},
		},
	}

	reply, err := coCreateStream(
		context.Background(),
		newCoCreateModelSet(model),
		nil,
		time.Second,
		bootstrap.DefaultCoCreateMaxTokens,
		"system",
		[]CoCreateMessage{{Role: "user", Content: "start"}},
		nil,
	)
	if err != nil {
		t.Fatalf("coCreateStream: %v", err)
	}
	if model.streamCalls != 2 {
		t.Fatalf("stream calls = %d, want 2", model.streamCalls)
	}
	if reply.Message != "ok" {
		t.Fatalf("reply = %+v", reply)
	}
}

func TestCoCreateStreamRepairsUnknownCoreCastField(t *testing.T) {
	invalid := "<reply>ok</reply><draft>## plan</draft><cast>" +
		`{"version":1,"mode":"normal","draft_revision":1,"members":[{"character":{"name":"Lin","age":25},"importance":"protagonist","origin":"original","mainline_function":"lead"}],"planned_relationships":[],"source_dispositions":[]}` +
		"</cast><ready>true</ready><suggestions></suggestions>"
	valid := "<reply>ok</reply><draft>## plan</draft><cast>" +
		`{"version":1,"mode":"normal","draft_revision":1,"members":[],"planned_relationships":[],"source_dispositions":[]}` +
		"</cast><ready>true</ready><suggestions></suggestions>"
	model := &scriptedCoCreateModel{streams: [][]agentcore.StreamEvent{
		{{Type: agentcore.StreamEventTextDelta, Delta: invalid}, {Type: agentcore.StreamEventDone}},
		{{Type: agentcore.StreamEventTextDelta, Delta: valid}, {Type: agentcore.StreamEventDone}},
	}}

	reply, err := coCreateStream(
		context.Background(),
		newCoCreateModelSet(model),
		nil,
		time.Second,
		bootstrap.DefaultCoCreateMaxTokens,
		coCreateSystemPrompt,
		[]CoCreateMessage{{Role: "user", Content: "start"}},
		nil,
	)
	if err != nil {
		t.Fatalf("coCreateStream: %v", err)
	}
	if model.streamCalls != 2 || reply.CoreCast == nil {
		t.Fatalf("stream calls = %d, reply = %+v", model.streamCalls, reply)
	}
	if len(model.streamMessages) != 2 || len(model.streamMessages[1]) < 4 {
		t.Fatalf("repair messages = %+v", model.streamMessages)
	}
	repairText := model.streamMessages[1][len(model.streamMessages[1])-1].TextContent()
	for _, want := range []string{`unknown field "age"`, "完整的 <reply><draft><ready><suggestions> 四段协议"} {
		if !strings.Contains(repairText, want) {
			t.Fatalf("repair instruction missing %q: %s", want, repairText)
		}
	}
}

func TestCoCreateStreamDoesNotRepeatAnAttemptThatHitItsConfiguredTimeout(t *testing.T) {
	restore := stubCoCreateRetrySleep(t)
	defer restore()

	model := &timeoutThenSuccessCoCreateModel{}
	_, err := coCreateStream(
		context.Background(),
		newCoCreateModelSet(model),
		nil,
		20*time.Millisecond,
		bootstrap.DefaultCoCreateMaxTokens,
		"system",
		[]CoCreateMessage{{Role: "user", Content: "start"}},
		nil,
	)
	if err == nil || !strings.Contains(err.Error(), "increase cocreate_timeout_seconds") {
		t.Fatalf("coCreateStream error = %v, want actionable timeout", err)
	}
	if model.streamCalls != 1 {
		t.Fatalf("stream calls = %d, want 1 to avoid repeating a predictably timed-out request", model.streamCalls)
	}
}

func TestCoCreateStreamUsesSuggestionJudgeWhenModelOmitsSuggestions(t *testing.T) {
	model := &scriptedCoCreateModel{
		streams: [][]agentcore.StreamEvent{{
			{Type: agentcore.StreamEventTextDelta, Delta: validCoCreateXML("你希望保持黑暗基调，还是往纯爱方向调整？")},
			{Type: agentcore.StreamEventDone},
		}},
		generateResponses: []string{`{"suggestions":["保持黑暗基调","改成纯爱方向"]}`},
	}

	reply, err := coCreateStream(
		context.Background(),
		newCoCreateModelSet(model),
		nil,
		time.Second,
		bootstrap.DefaultCoCreateMaxTokens,
		"system",
		[]CoCreateMessage{{Role: "user", Content: "start"}},
		nil,
	)
	if err != nil {
		t.Fatalf("coCreateStream: %v", err)
	}
	if model.generateCalls != 1 {
		t.Fatalf("suggestion judge calls = %d, want 1", model.generateCalls)
	}
	want := []string{"保持黑暗基调", "改成纯爱方向"}
	if len(reply.Suggestions) != len(want) {
		t.Fatalf("suggestions = %v, want %v", reply.Suggestions, want)
	}
	for i := range want {
		if reply.Suggestions[i] != want[i] {
			t.Fatalf("suggestion[%d] = %q, want %q", i, reply.Suggestions[i], want[i])
		}
	}
}

func TestCoCreateStreamSkipsSuggestionJudgeWhenSuggestionsAreExplicit(t *testing.T) {
	model := &scriptedCoCreateModel{
		streams: [][]agentcore.StreamEvent{{
			{Type: agentcore.StreamEventTextDelta, Delta: coCreateXMLWithSuggestions("请选择下一步", "加强女主线")},
			{Type: agentcore.StreamEventDone},
		}},
		generateResponses: []string{`{"suggestions":["不应使用"]}`},
	}

	reply, err := coCreateStream(
		context.Background(),
		newCoCreateModelSet(model),
		nil,
		time.Second,
		bootstrap.DefaultCoCreateMaxTokens,
		"system",
		[]CoCreateMessage{{Role: "user", Content: "start"}},
		nil,
	)
	if err != nil {
		t.Fatalf("coCreateStream: %v", err)
	}
	if model.generateCalls != 0 {
		t.Fatalf("suggestion judge calls = %d, want 0", model.generateCalls)
	}
	if len(reply.Suggestions) != 1 || reply.Suggestions[0] != "加强女主线" {
		t.Fatalf("suggestions = %v", reply.Suggestions)
	}
}

func TestCoCreateStreamRepairsDoneWithLengthStop(t *testing.T) {
	model := &scriptedCoCreateModel{
		streams: [][]agentcore.StreamEvent{
			{
				{Type: agentcore.StreamEventTextDelta, Delta: "<reply>ok</reply><draft>partial"},
				{Type: agentcore.StreamEventDone, StopReason: agentcore.StopReasonLength},
			},
			{
				{Type: agentcore.StreamEventTextDelta, Delta: validCoCreateXML("compact")},
				{Type: agentcore.StreamEventDone},
			},
		},
		generateResponses: []string{`{"suggestions":["不应使用"]}`},
	}

	reply, err := coCreateStream(
		context.Background(),
		newCoCreateModelSet(model),
		nil,
		time.Second,
		bootstrap.DefaultCoCreateMaxTokens,
		"system",
		[]CoCreateMessage{{Role: "user", Content: "start"}},
		nil,
	)
	if err != nil {
		t.Fatalf("coCreateStream: %v", err)
	}
	if reply.Message != "compact" || model.streamCalls != 2 {
		t.Fatalf("reply=%+v stream calls=%d", reply, model.streamCalls)
	}
	if model.generateCalls != 1 {
		t.Fatalf("suggestion judge calls = %d, want 1 after successful repair", model.generateCalls)
	}
	repairText := model.streamMessages[1][len(model.streamMessages[1])-1].TextContent()
	for _, want := range []string{"紧凑修复批次", "reply 最多 120 字", "保留已经形成的全部创作结论"} {
		if !strings.Contains(repairText, want) {
			t.Fatalf("truncation repair instruction missing %q: %s", want, repairText)
		}
	}
}

func TestCoCreateStreamRejectsRepeatedLengthStops(t *testing.T) {
	truncated := []agentcore.StreamEvent{
		{Type: agentcore.StreamEventTextDelta, Delta: "<reply>ok</reply><draft>partial"},
		{Type: agentcore.StreamEventDone, StopReason: agentcore.StopReasonLength},
	}
	model := &scriptedCoCreateModel{streams: [][]agentcore.StreamEvent{truncated, truncated, truncated}}

	_, err := coCreateStream(
		context.Background(),
		newCoCreateModelSet(model),
		nil,
		time.Second,
		bootstrap.DefaultCoCreateMaxTokens,
		"system",
		[]CoCreateMessage{{Role: "user", Content: "start"}},
		nil,
	)
	if err == nil || !strings.Contains(err.Error(), "truncated") {
		t.Fatalf("err = %v, want truncated error", err)
	}
	if model.streamCalls != 1+coCreateMaxStructureRepairs {
		t.Fatalf("stream calls = %d, want initial response plus %d repairs", model.streamCalls, coCreateMaxStructureRepairs)
	}
}

func TestCoCreateStreamRejectsIncompleteXML(t *testing.T) {
	incomplete := []agentcore.StreamEvent{
		{Type: agentcore.StreamEventTextDelta, Delta: "<reply>ok</reply><draft>partial"},
		{Type: agentcore.StreamEventDone},
	}
	model := &scriptedCoCreateModel{
		streams: [][]agentcore.StreamEvent{incomplete, incomplete, incomplete},
	}

	_, err := coCreateStream(
		context.Background(),
		newCoCreateModelSet(model),
		nil,
		time.Second,
		bootstrap.DefaultCoCreateMaxTokens,
		"system",
		[]CoCreateMessage{{Role: "user", Content: "start"}},
		nil,
	)
	if err == nil || !strings.Contains(err.Error(), "incomplete") {
		t.Fatalf("err = %v, want incomplete XML error", err)
	}
	if model.streamCalls != 1+coCreateMaxStructureRepairs {
		t.Fatalf("stream calls = %d, want initial response plus %d repairs", model.streamCalls, coCreateMaxStructureRepairs)
	}
}

func TestCoCreateStreamDoesNotRetryCancellation(t *testing.T) {
	restore := stubCoCreateRetrySleep(t)
	defer restore()

	model := &scriptedCoCreateModel{
		streams: [][]agentcore.StreamEvent{{
			{Type: agentcore.StreamEventError, Err: context.Canceled},
		}},
	}

	_, err := coCreateStream(
		context.Background(),
		newCoCreateModelSet(model),
		nil,
		time.Second,
		bootstrap.DefaultCoCreateMaxTokens,
		"system",
		[]CoCreateMessage{{Role: "user", Content: "start"}},
		nil,
	)
	if err == nil || !strings.Contains(err.Error(), context.Canceled.Error()) {
		t.Fatalf("err = %v, want context canceled", err)
	}
	if model.streamCalls != 1 {
		t.Fatalf("stream calls = %d, want 1", model.streamCalls)
	}
}

func TestCoCreateStreamErrorIncludesSelectedModel(t *testing.T) {
	restore := stubCoCreateRetrySleep(t)
	defer restore()

	model := &scriptedCoCreateModel{
		streams: [][]agentcore.StreamEvent{{
			{Type: agentcore.StreamEventError, Err: context.Canceled},
		}},
	}

	_, err := coCreateStream(
		context.Background(),
		newCoCreateModelSet(model),
		nil,
		time.Second,
		bootstrap.DefaultCoCreateMaxTokens,
		"system",
		[]CoCreateMessage{{Role: "user", Content: "start"}},
		nil,
	)
	if err == nil {
		t.Fatal("coCreateStream should fail")
	}
	if !strings.Contains(err.Error(), "selected model test/scripted-cocreate") {
		t.Fatalf("err = %v, want selected model label", err)
	}
}

func TestCoCreateStreamGatewayErrorDoesNotLeakHTML(t *testing.T) {
	restore := stubCoCreateRetrySleep(t)
	defer restore()

	streams := make([][]agentcore.StreamEvent, coCreateMaxAttempts)
	for index := range streams {
		streams[index] = []agentcore.StreamEvent{{
			Type: agentcore.StreamEventError,
			Err:  litellm.NewHTTPError("deepseek", 502, "<html><head><title>502 Bad Gateway</title></head><body>nginx</body></html>"),
		}}
	}
	model := &scriptedCoCreateModel{streams: streams}

	_, err := coCreateStream(
		context.Background(),
		newCoCreateModelSet(model),
		nil,
		time.Second,
		bootstrap.DefaultCoCreateMaxTokens,
		"system",
		[]CoCreateMessage{{Role: "user", Content: "start"}},
		nil,
	)
	if err == nil {
		t.Fatal("coCreateStream should fail")
	}
	message := err.Error()
	if !strings.Contains(message, "selected model test/scripted-cocreate") ||
		!strings.Contains(message, "provider gateway error: 502 Bad Gateway") {
		t.Fatalf("err = %v, want selected model and sanitized gateway error", err)
	}
	if strings.Contains(strings.ToLower(message), "<html") || strings.Contains(message, "nginx") {
		t.Fatalf("err leaked raw html: %v", err)
	}
}

type scriptedCoCreateModel struct {
	streams           [][]agentcore.StreamEvent
	generateResponses []string
	streamMessages    [][]agentcore.Message
	streamCalls       int
	generateCalls     int
}

type timeoutThenSuccessCoCreateModel struct {
	streamCalls int
}

func (m *timeoutThenSuccessCoCreateModel) Generate(context.Context, []agentcore.Message, []agentcore.ToolSpec, ...agentcore.CallOption) (*agentcore.LLMResponse, error) {
	return nil, io.ErrUnexpectedEOF
}

func (m *timeoutThenSuccessCoCreateModel) GenerateStream(ctx context.Context, _ []agentcore.Message, _ []agentcore.ToolSpec, _ ...agentcore.CallOption) (<-chan agentcore.StreamEvent, error) {
	m.streamCalls++
	ch := make(chan agentcore.StreamEvent, 2)
	go func(call int) {
		defer close(ch)
		if call == 1 {
			<-ctx.Done()
			ch <- agentcore.StreamEvent{Type: agentcore.StreamEventError, Err: ctx.Err()}
			return
		}
		ch <- agentcore.StreamEvent{Type: agentcore.StreamEventTextDelta, Delta: validCoCreateXML("ok")}
		ch <- agentcore.StreamEvent{Type: agentcore.StreamEventDone}
	}(m.streamCalls)
	return ch, nil
}

func (m *timeoutThenSuccessCoCreateModel) SupportsTools() bool { return false }

func (m *scriptedCoCreateModel) Generate(context.Context, []agentcore.Message, []agentcore.ToolSpec, ...agentcore.CallOption) (*agentcore.LLMResponse, error) {
	if m.generateCalls >= len(m.generateResponses) {
		m.generateCalls++
		return nil, io.ErrUnexpectedEOF
	}
	text := m.generateResponses[m.generateCalls]
	m.generateCalls++
	return &agentcore.LLMResponse{Message: agentcore.Message{
		Role:      agentcore.RoleAssistant,
		Content:   []agentcore.ContentBlock{agentcore.TextBlock(text)},
		Timestamp: time.Now(),
	}}, nil
}

func (m *scriptedCoCreateModel) GenerateStream(_ context.Context, messages []agentcore.Message, _ []agentcore.ToolSpec, _ ...agentcore.CallOption) (<-chan agentcore.StreamEvent, error) {
	ch := make(chan agentcore.StreamEvent, 8)
	m.streamMessages = append(m.streamMessages, append([]agentcore.Message(nil), messages...))
	if m.streamCalls >= len(m.streams) {
		close(ch)
		return ch, nil
	}
	events := m.streams[m.streamCalls]
	m.streamCalls++
	go func() {
		defer close(ch)
		for _, ev := range events {
			ch <- ev
		}
	}()
	return ch, nil
}

func (m *scriptedCoCreateModel) SupportsTools() bool { return false }

type coCreateProgress struct {
	kind string
	text string
}

func newCoCreateModelSet(model agentcore.ChatModel) *bootstrap.ModelSet {
	return &bootstrap.ModelSet{
		Default: bootstrap.NewSwappableModel("test", "scripted-cocreate", model),
	}
}

func stubCoCreateRetrySleep(t *testing.T) func() {
	t.Helper()
	original := coCreateRetrySleep
	coCreateRetrySleep = func(context.Context, time.Duration) error { return nil }
	return func() { coCreateRetrySleep = original }
}

func validCoCreateXML(message string) string {
	return coCreateXMLWithSuggestions(message)
}

func coCreateXMLWithSuggestions(message string, suggestions ...string) string {
	var suggestionBody strings.Builder
	for _, suggestion := range suggestions {
		suggestionBody.WriteString("- ")
		suggestionBody.WriteString(suggestion)
		suggestionBody.WriteByte('\n')
	}
	return "<reply>" + message + "</reply>" +
		"<draft>## plan</draft>" +
		"<ready>true</ready>" +
		"<suggestions>" + suggestionBody.String() + "</suggestions>"
}

func hasCoCreateProgress(progress []coCreateProgress, kind, text string) bool {
	for _, item := range progress {
		if item.kind == kind && item.text == text {
			return true
		}
	}
	return false
}
