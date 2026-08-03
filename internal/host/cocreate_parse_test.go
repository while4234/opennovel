package host

import (
	"strings"
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/host/adapt"
	"github.com/voocel/ainovel-cli/internal/store"
)

func TestCoCreateCastProtocolIsOptionalLegacySeedForAllNewCoCreateModes(t *testing.T) {
	validCast := `{"version":1,"mode":"normal","draft_revision":1,"members":[],"planned_relationships":[],"source_dispositions":[]}`
	raw := "<reply>ok</reply><draft>draft</draft><cast>" + validCast + "</cast><ready>true</ready><suggestions></suggestions>"
	reply, err := parseCoCreateResponseForProtocol(raw, false)
	if err != nil || reply.CoreCast == nil {
		t.Fatalf("legacy five-part parse = %+v, %v", reply, err)
	}
	normal, err := parseCoCreateResponseForProtocol("<reply>ok</reply><draft>draft</draft><ready>true</ready><suggestions></suggestions>", false)
	if err != nil || normal.CoreCast != nil {
		t.Fatalf("normal four-part parse = %+v, %v", normal, err)
	}
	adaptation, err := parseCoCreateResponseForProtocol("<reply>ok</reply><draft>draft</draft><ready>true</ready><suggestions></suggestions>", false)
	if err != nil || adaptation.CoreCast != nil {
		t.Fatalf("adaptation four-part parse = %+v, %v", adaptation, err)
	}
	stage, err := parseCoCreateResponseForProtocol("<reply>ok</reply><draft>draft</draft><ready>true</ready><suggestions></suggestions>", false)
	if err != nil || stage.CoreCast != nil {
		t.Fatalf("four-part stage parse = %+v, %v", stage, err)
	}
	if !strings.Contains(coCreateSystemPrompt, "##") || strings.Contains(coCreateSystemPrompt, "<cast>\n") {
		t.Fatal("normal prompt does not define the four-section creative-brief protocol")
	}
	if coCreatePromptRequiresCast(coCreateSystemPrompt) {
		t.Fatal("normal prompt was misclassified as requiring a final cast")
	}
	if strings.Contains(adaptCoCreateSystemPrompt, "\n<cast>\n") {
		t.Fatal("adaptation prompt still requires a cast")
	}
	if coCreatePromptRequiresCast(adaptCoCreateSystemPrompt) {
		t.Fatal("adaptation prompt was misclassified as requiring a cast")
	}
	if strings.Contains(stageCoCreateSystemPrompt, "<cast>") {
		t.Fatal("stage protocol was forced to include cast")
	}
	if legacy := LegacyCoCreateCast(raw); legacy == nil || legacy.Mode != domain.CoreCastModeNormal {
		t.Fatalf("legacy CoreCast seed = %+v", legacy)
	}
}

func TestCoCreateCastProtocolRejectsDuplicateInvalidAndOversizedCast(t *testing.T) {
	validCast := `{"version":1,"mode":"normal","draft_revision":1,"members":[],"planned_relationships":[],"source_dispositions":[]}`
	duplicate := "<reply>ok</reply><draft>draft</draft><cast>" + validCast + "</cast><cast>" + validCast + "</cast><ready>false</ready><suggestions></suggestions>"
	if err := rejectIncompleteCoCreateXML(duplicate, true); err == nil {
		t.Fatal("duplicate cast tag was accepted")
	}
	invalid := "<reply>ok</reply><draft>draft</draft><cast>{</cast><ready>false</ready><suggestions></suggestions>"
	if _, err := parseCoCreateResponseForProtocol(invalid, true); err == nil {
		t.Fatal("invalid cast json was accepted")
	}
	unknownField := "<reply>ok</reply><draft>draft</draft><cast>" +
		`{"version":1,"mode":"normal","draft_revision":1,"members":[{"character":{"name":"Lin","age":25},"importance":"protagonist","origin":"original","mainline_function":"lead"}],"planned_relationships":[],"source_dispositions":[]}` +
		"</cast><ready>false</ready><suggestions></suggestions>"
	if _, err := parseCoCreateResponseForProtocol(unknownField, true); err == nil || !strings.Contains(err.Error(), `unknown field "age"`) {
		t.Fatalf("unknown cast field error = %v", err)
	}
	oversized := "<reply>ok</reply><draft>draft</draft><cast>" + strings.Repeat(" ", coCreateCastMaxBytes+1) + "{}</cast><ready>false</ready><suggestions></suggestions>"
	if _, err := parseCoCreateResponseForProtocol(oversized, true); err == nil {
		t.Fatal("oversized cast was accepted")
	}
}

func TestCoCreateCastPromptDefinesExactCharacterFieldBoundary(t *testing.T) {
	for _, want := range []string{
		"character：id, name, aliases, role, gender, description, arc, traits, tier, faction, goal, motivation, conflict, voice, constraints, notes",
		"不得添加 age、appearance、background",
		"gender 必须为 male、female、nonbinary 或 unspecified",
		"年龄、外貌、经历等信息必须写入 description 或 notes",
		"mode 只能是字符串 normal（普通原创）或 adaptation（改编）",
		"constraints、source_character_ids、tags、target_character_ids 都必须是 JSON 字符串数组",
	} {
		if !strings.Contains(coCreateCastJSONFieldContract, want) {
			t.Fatalf("legacy cast compatibility boundary missing exact field guidance %q", want)
		}
	}
}

func TestCoCreateCastParserNormalizesCommonModelModeAliases(t *testing.T) {
	for alias, want := range map[string]domain.CoreCastMode{
		"original": domain.CoreCastModeNormal,
		"creative": domain.CoreCastModeNormal,
		"adapt":    domain.CoreCastModeAdaptation,
	} {
		cast := `{"version":1,"mode":"` + alias + `","draft_revision":1,"members":[],"planned_relationships":[],"source_dispositions":[]}`
		raw := "<reply>ok</reply><draft>draft</draft><cast>" + cast + "</cast><ready>true</ready><suggestions></suggestions>"
		reply, err := parseCoCreateResponseForProtocol(raw, true)
		if err != nil || reply.CoreCast == nil || reply.CoreCast.Mode != want {
			t.Fatalf("mode alias %q = %+v, %v; want %q", alias, reply.CoreCast, err, want)
		}
	}
}

func TestParseSuggestionsStripsListMarkersAndCapsResults(t *testing.T) {
	got := parseSuggestions(`
<uggestions>
- 增强女主线
* 改成双主角
1. 加一条悬疑暗线
2. 这一条超过上限
</suggestions>
`)
	want := []string{"增强女主线", "改成双主角", "加一条悬疑暗线"}
	if len(got) != len(want) {
		t.Fatalf("suggestions length = %d, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("suggestion[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestParseSuggestionJudgeResponseCleansAndCapsResults(t *testing.T) {
	got := parseSuggestionJudgeResponse("```json\n" + `{
		"suggestions": [
			"- 保持黑暗基调",
			"1. 改成纯爱方向",
			"保持黑暗基调",
			"这一条故意写得非常非常非常非常非常非常非常长超过按钮限制"
		]
	}` + "\n```")
	want := []string{"保持黑暗基调", "改成纯爱方向"}
	if len(got) != len(want) {
		t.Fatalf("suggestions length = %d, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("suggestion[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestAdaptSystemPromptIncludesLateDossierRelationshipRisks(t *testing.T) {
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	sources := make([]domain.AdaptationSource, 0, 40)
	for chapter := 1; chapter <= 40; chapter++ {
		sources = append(sources, domain.AdaptationSource{
			Chapter: chapter,
			Title:   "Source",
			SHA256:  store.TextSHA256(strings.Repeat("x", chapter)),
			Path:    store.SourceChapterRelPath(chapter),
			Runes:   chapter,
		})
	}
	manifest := domain.AdaptationSourceManifest{
		SourcePath:   "source.txt",
		ChapterCount: len(sources),
		Chapters:     sources,
	}
	if err := st.Adaptation.SaveSourceManifest(manifest); err != nil {
		t.Fatalf("SaveSourceManifest: %v", err)
	}
	dossier := domain.AdaptationCoCreateDossier{
		Version:            1,
		PromptVersion:      adapt.CoCreateDossierPromptVersion,
		SourcePath:         manifest.SourcePath,
		SourceChapterCount: manifest.ChapterCount,
		SourceSignature:    store.AdaptationSourceSignature(manifest),
		BatchSize:          adapt.CoCreateDossierBatchSize,
		BatchRuneLimit:     adapt.CoCreateDossierBatchRuneLimit,
		Batches: []domain.AdaptationCoCreateDossierBatch{
			{Index: 1, SourceFrom: 1, SourceTo: 40, SourceSignature: store.AdaptationDossierBatchSpecs(manifest, adapt.CoCreateDossierBatchSize, adapt.CoCreateDossierBatchRuneLimit)[0].SourceSignature},
		},
		AmbiguityRisks: []domain.AdaptationRelationshipRisk{
			{
				Chapters:   []int{35},
				Characters: []string{"男主", "小狐狸"},
				Risk:       "小狐狸向男主表达喜欢，容易形成后宫暧昧感。",
				Evidence:   "第35章小狐狸明确说喜欢男主。",
				Suggestion: "单女主改编中改为普通感激或阵营依赖。",
			},
		},
	}
	if err := st.Adaptation.SaveCoCreateDossier(dossier); err != nil {
		t.Fatalf("SaveCoCreateDossier: %v", err)
	}

	prompt := adaptSystemPrompt(st)
	for _, want := range []string{"第 35 章", "小狐狸向男主表达喜欢"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("adapt prompt missing %q:\n%s", want, prompt)
		}
	}
	if strings.Contains(prompt, "普通感激或阵营依赖") {
		t.Fatalf("adapt prompt should not include dossier-stage adaptation suggestions:\n%s", prompt)
	}
	if strings.Contains(prompt, "其余 10 章") {
		t.Fatalf("adapt prompt should not use first-30 snapshot fallback:\n%s", prompt)
	}
}

func TestRejectIncompleteCoCreateXML(t *testing.T) {
	for _, raw := range []string{
		"<reply>ok</reply><draft>half",
		"<reply>ok</reply><draft>## plan</draft><ready>true",
		"<reply>ok</reply><draft>## plan</draft><ready>true</ready><suggestions>- x",
	} {
		if err := rejectIncompleteCoCreateXML(raw); err == nil {
			t.Fatalf("rejectIncompleteCoCreateXML(%q) = nil, want error", raw)
		}
	}
	for _, raw := range []string{
		"plain natural language",
		"outside<reply>ok</reply><draft>plan</draft><ready>true</ready><suggestions></suggestions>",
		"<draft>plan</draft><reply>ok</reply><ready>true</ready><suggestions></suggestions>",
		"<reply><draft>nested</draft></reply><draft>plan</draft><ready>true</ready><suggestions></suggestions>",
	} {
		if err := rejectIncompleteCoCreateXML(raw); err == nil {
			t.Fatalf("strict protocol accepted %q", raw)
		}
	}
}
