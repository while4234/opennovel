package imp

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/store"
	"github.com/voocel/ainovel-cli/internal/tools"
)

const validAnalyzerEnvelope = `=== SUMMARY ===
林晚收到匿名爆料后，在档案馆发现失踪者全部姓陈，并在祭品旁找到陈姓家族祖宅地址。

=== CHARACTERS ===
["林晚","档案馆管理员"]

=== KEY_EVENTS ===
["林晚收到匿名信","在档案馆发现陈姓共同点","找到祖宅地址"]

=== TIMELINE ===
[
  {"time":"傍晚","event":"林晚收到匿名信","characters":["林晚"]},
  {"time":"次日","event":"档案馆走访","characters":["林晚","档案馆管理员"]}
]

=== FORESHADOW ===
[
  {"id":"hk-chen-family","action":"plant","description":"陈姓家族与连环失踪案的关联"}
]

=== RELATIONSHIPS ===
[]

=== STATE_CHANGES ===
[
  {"entity":"林晚","field":"location","old_value":"编辑部","new_value":"档案馆","reason":"循迹追查"}
]

=== HOOK_TYPE ===
mystery

=== DOMINANT_STRAND ===
quest
`

func TestParseAnalyzer_Valid(t *testing.T) {
	got, err := parseAnalyzerOutput(validAnalyzerEnvelope)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got.HookType != "mystery" || got.DominantStrand != "quest" {
		t.Errorf("hook/strand: %+v", got)
	}
	if len(got.Characters) != 2 || len(got.KeyEvents) != 3 {
		t.Errorf("counts: %+v", got)
	}
	if len(got.ForeshadowUpdates) != 1 || got.ForeshadowUpdates[0].ID != "hk-chen-family" {
		t.Errorf("foreshadow: %+v", got.ForeshadowUpdates)
	}
	if len(got.TimelineEvents) != 2 {
		t.Errorf("timeline: %+v", got.TimelineEvents)
	}
	if len(got.RelationshipChanges) != 0 {
		t.Errorf("relationships should be empty: %+v", got.RelationshipChanges)
	}
	if len(got.StateChanges) != 1 || got.StateChanges[0].Field != "location" {
		t.Errorf("state changes: %+v", got.StateChanges)
	}
}

func TestParseAnalyzer_CharacterProfilesUseCanonicalSchema(t *testing.T) {
	input := strings.Replace(
		validAnalyzerEnvelope,
		"=== KEY_EVENTS ===",
		`=== CHARACTER_PROFILES ===
[{"id":"source-lin","name":"Lin Wan","aliases":["Editor Lin"],"role":"investigator","gender":"female","description":"Tracks an anonymous lead.","arc":"Learns to share evidence.","traits":["careful"],"tier":"core","goal":"Identify the sender.","motivation":"Protect the missing witnesses.","conflict":"The archive is compromised.","voice":"Evidence-first.","notes":"Bounded chapter observation.","constraints":["Cannot know the sender yet."]}]

=== KEY_EVENTS ===`,
		1,
	)
	got, err := parseAnalyzerOutput(input)
	if err != nil {
		t.Fatalf("parse profiles: %v", err)
	}
	if len(got.CharacterProfiles) != 1 {
		t.Fatalf("profiles = %+v", got.CharacterProfiles)
	}
	profile := got.CharacterProfiles[0]
	if profile.ID != "source-lin" || profile.Name != "Lin Wan" ||
		profile.Gender != "female" || profile.Goal != "Identify the sender." ||
		profile.Motivation != "Protect the missing witnesses." ||
		len(profile.Aliases) != 1 || len(profile.Constraints) != 1 {
		t.Fatalf("canonical profile fields were not preserved: %+v", profile)
	}
}

func TestParseAnalyzer_NormalizesCommonSingleValueCharacterFields(t *testing.T) {
	input := strings.Replace(
		validAnalyzerEnvelope,
		"=== KEY_EVENTS ===",
		`=== CHARACTER_PROFILES ===
[{"id":"source-lin","name":"Lin Wan","role":"investigator","gender":"female","description":"Tracks a lead.","constraints":"Cannot identify the sender yet.","contrast_details":"appears hesitant → acts decisively","key_backstory":"lost the archive => verifies every lead","initial_state":"working alone in the archive","knowledge_boundary":"does not know the sender"}]

=== KEY_EVENTS ===`,
		1,
	)
	got, err := parseAnalyzerOutput(input)
	if err != nil {
		t.Fatalf("parse compatibility profile: %v", err)
	}
	profile := got.CharacterProfiles[0]
	if len(profile.Constraints) != 1 ||
		len(profile.ContrastDetails) != 1 || profile.ContrastDetails[0].Depth != "acts decisively" ||
		len(profile.KeyBackstory) != 1 || profile.KeyBackstory[0].Impact != "verifies every lead" ||
		profile.InitialState == nil || profile.InitialState.Situation != "working alone in the archive" ||
		profile.KnowledgeBoundary == nil || len(profile.KnowledgeBoundary.Known) != 1 {
		t.Fatalf("single-value fields were not normalized: %+v", profile)
	}
}

func TestParseAnalyzer_AcceptsLegacyTimelineStringItems(t *testing.T) {
	input := `=== SUMMARY ===
summary

=== CHARACTERS ===
["A"]

=== KEY_EVENTS ===
["A enters"]

=== TIMELINE ===
["A enters the room","B leaves"]

=== FORESHADOW ===
[]

=== RELATIONSHIPS ===
[]

=== STATE_CHANGES ===
[]

=== HOOK_TYPE ===
mystery

=== DOMINANT_STRAND ===
quest
`
	got, err := parseAnalyzerOutput(input)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(got.TimelineEvents) != 2 {
		t.Fatalf("timeline count: %+v", got.TimelineEvents)
	}
	if got.TimelineEvents[0].Event != "A enters the room" {
		t.Fatalf("legacy timeline event not preserved: %+v", got.TimelineEvents[0])
	}
}

func TestParseAnalyzer_RejectsInvalidHookType(t *testing.T) {
	bad := strings.Replace(validAnalyzerEnvelope, "mystery", "weird", 1)
	if _, err := parseAnalyzerOutput(bad); err == nil ||
		!strings.Contains(err.Error(), "invalid hook_type") {
		t.Fatalf("want hook_type error, got %v", err)
	}
}

func TestParseAnalyzer_RejectsPlantWithoutDescription(t *testing.T) {
	bad := strings.Replace(
		validAnalyzerEnvelope,
		`{"id":"hk-chen-family","action":"plant","description":"陈姓家族与连环失踪案的关联"}`,
		`{"id":"hk-chen-family","action":"plant"}`,
		1,
	)
	if _, err := parseAnalyzerOutput(bad); err == nil ||
		!strings.Contains(err.Error(), "requires description") {
		t.Fatalf("want plant-without-desc error, got %v", err)
	}
}

func TestParseAnalyzer_MissingRequiredTag(t *testing.T) {
	bad := strings.Replace(validAnalyzerEnvelope, "=== HOOK_TYPE ===\nmystery\n", "", 1)
	if _, err := parseAnalyzerOutput(bad); err == nil ||
		!strings.Contains(err.Error(), "missing required tags") {
		t.Fatalf("want missing-tag error, got %v", err)
	}
}

func TestAnalyzeChapter_CleansInvalidUTF8BeforeLLM(t *testing.T) {
	invalidByte := string([]byte{0xFF})
	llm := &mockLLM{out: validAnalyzerEnvelope}

	_, err := AnalyzeChapter(
		context.Background(),
		llm,
		"system"+invalidByte,
		1,
		"初遇"+invalidByte,
		"林晚翻开匿名信"+invalidByte+"继续追查。",
		"# 前提"+invalidByte,
		"- 林晚"+invalidByte,
		[]domain.ForeshadowEntry{
			{
				ID:          "hk" + invalidByte,
				Description: "旧案线索" + invalidByte,
				PlantedAt:   1,
				Status:      "planted" + invalidByte,
			},
		},
	)
	if err != nil {
		t.Fatalf("AnalyzeChapter: %v", err)
	}
	if len(llm.got) != 2 {
		t.Fatalf("want 2 LLM messages, got %d", len(llm.got))
	}
	for i, msg := range llm.got {
		if !utf8.ValidString(msg.TextContent()) {
			t.Fatalf("message %d must be valid UTF-8", i)
		}
	}
}

func TestPersistChapter_FullPipeline(t *testing.T) {
	dir := t.TempDir()
	st := store.NewStore(dir)
	if err := st.Progress.Init("ch-test", 2); err != nil {
		t.Fatal(err)
	}

	// 准备 foundation：先用 ReverseFoundation+PersistFoundation 模拟 Phase 2 已完成
	fr := mustParse(t, validEnvelope, 2)
	if err := PersistFoundation(context.Background(), st, domain.PlanningTierShort, fr); err != nil {
		t.Fatal(err)
	}

	a, err := parseAnalyzerOutput(validAnalyzerEnvelope)
	if err != nil {
		t.Fatal(err)
	}
	commitTool := tools.NewCommitChapterTool(st)
	body := "林晚翻开匿名信，发现一行潦草字迹...\n\n（正文略，>500 字以让 LoadChapterContent 通过校验）"
	body = strings.Repeat(body, 10) // 凑够字数

	if err := PersistChapter(context.Background(), st, commitTool, 1, "初遇", body, a); err != nil {
		t.Fatalf("PersistChapter: %v", err)
	}

	prog, _ := st.Progress.Load()
	if len(prog.CompletedChapters) != 1 || prog.CompletedChapters[0] != 1 {
		t.Errorf("completed chapters wrong: %+v", prog.CompletedChapters)
	}

	hooks, err := st.World.LoadForeshadowLedger()
	if err != nil {
		t.Fatalf("load hooks: %v", err)
	}
	if len(hooks) != 1 || hooks[0].ID != "hk-chen-family" {
		t.Errorf("foreshadow not persisted: %+v", hooks)
	}

	// 二次提交同一章应是幂等（commit_chapter.IsChapterCompleted 短路）
	if err := PersistChapter(context.Background(), st, commitTool, 1, "初遇", body, a); err != nil {
		t.Errorf("re-import should be idempotent, got: %v", err)
	}
	prog2, _ := st.Progress.Load()
	if len(prog2.CompletedChapters) != 1 {
		t.Errorf("re-import duplicated completion: %+v", prog2.CompletedChapters)
	}
}
