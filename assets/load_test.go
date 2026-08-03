package assets

import (
	"reflect"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/voocel/ainovel-cli/internal/globalprompt"
)

func TestLoadPromptsApplyGlobalPrompt(t *testing.T) {
	prompts := Load("").Prompts
	prefix := globalprompt.Text()
	value := reflect.ValueOf(prompts)
	typ := value.Type()

	for i := 0; i < value.NumField(); i++ {
		name := typ.Field(i).Name
		prompt := value.Field(i).String()
		if !strings.HasPrefix(prompt, prefix) {
			t.Fatalf("%s prompt does not start with the global prompt", name)
		}
	}
}

func TestLoadKeepsAdaptationGuidanceOutOfBaseWriterPrompt(t *testing.T) {
	bundle := Load("")
	if !strings.Contains(bundle.Prompts.AdaptationPlanner, "Adaptation Planner") {
		t.Fatal("adaptation planner prompt should be loaded")
	}
	if strings.Contains(bundle.Prompts.Writer, "某某内心独白") {
		t.Fatal("base writer prompt must not include adaptation-only inner-monologue label guidance")
	}
	if strings.Contains(bundle.Prompts.Editor, "preserve-details adaptation") ||
		strings.Contains(bundle.Prompts.Editor, "内心独白仅为示意") {
		t.Fatal("base editor prompt must not include adaptation-only review guidance")
	}
	if !strings.Contains(bundle.References.AdaptationWriter, "某某内心独白") {
		t.Fatal("adaptation writer guidance should carry the inner-monologue label rule")
	}
	if !strings.Contains(bundle.References.AdaptationEditorPreserveDetails, "内心独白仅为示意") {
		t.Fatal("preserve-details editor guidance should carry the label residue rule")
	}
	if !strings.Contains(bundle.References.AdaptationEditorFullRewrite, "full_rewrite") {
		t.Fatal("full-rewrite editor guidance should be loaded")
	}
}

func TestLoadPromptsIncludeCanonicalSimulationContractGuidance(t *testing.T) {
	bundle := Load("")
	cases := map[string]string{
		"ArchitectShort": bundle.Prompts.ArchitectShort,
		"ArchitectLong":  bundle.Prompts.ArchitectLong,
		"Character":      bundle.Prompts.Character,
		"Writer":         bundle.Prompts.Writer,
		"Editor":         bundle.Prompts.Editor,
	}
	for name, prompt := range cases {
		for _, want := range []string{
			"simulation_effective.effective_mode",
			"status=inactive",
			"normal",
			"reinforced",
			"role-bound",
			"source_reports",
			"raw source",
		} {
			if !strings.Contains(prompt, want) {
				t.Fatalf("%s prompt missing reinforced guidance %q", name, want)
			}
		}
	}

	roleGuidance := map[string][]string{
		"ArchitectShort": {"planning view", "pacing"},
		"ArchitectLong":  {"planning view", "pacing"},
		"Writer":         {"Writer chapter view", "当前章节 POV"},
		"Editor":         {"Editor review view", "不得复用 Writer guidance"},
	}
	for name, wants := range roleGuidance {
		for _, want := range wants {
			if !strings.Contains(cases[name], want) {
				t.Fatalf("%s prompt missing role reinforced guidance %q", name, want)
			}
		}
	}
}

func TestCharacterPromptDefinesIndependentAnalyzeAndReviewRuns(t *testing.T) {
	prompt := Load("").Prompts.Character
	for _, want := range []string{
		"Character Agent",
		"mode=analyze",
		"mode=review",
		"character_context",
		"save_character_candidate",
		"save_character_review",
		"source_fact",
		"adaptation_decision",
		"target_original_addition",
		"Non-negotiable quality floor",
		"new male lead",
		"新男主（主视角）",
		"separate quality-control run",
		"raw source",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("character prompt missing %q", want)
		}
	}
}

func TestStyleCatalogUsesMarkdownHeadingLabels(t *testing.T) {
	catalog := StyleCatalog()
	labelsByID := make(map[string]string, len(catalog))
	for _, item := range catalog {
		labelsByID[item.ID] = item.Label
	}

	want := map[string]string{
		"default":  "通用写作风格",
		"fantasy":  "奇幻冒险风格",
		"romance":  "言情风格",
		"suspense": "悬疑推理风格",
	}
	for id, label := range want {
		if labelsByID[id] != label {
			t.Fatalf("style %s label = %q, want %q; catalog=%+v", id, labelsByID[id], label, catalog)
		}
	}
}

func TestStyleCatalogFromFSDiscoversAdditionalMarkdown(t *testing.T) {
	fsys := fstest.MapFS{
		"styles/default.md":     {Data: []byte("## 通用写作风格\nbody")},
		"styles/new-style.md":   {Data: []byte("  ## 新增风格  \nbody")},
		"styles/no-heading.md":  {Data: []byte("\nbody")},
		"styles/ignored.txt":    {Data: []byte("## ignored")},
		"styles/nested/file.md": {Data: []byte("## nested")},
	}

	catalog := styleCatalogFromFS(fsys, "styles")
	labelsByID := make(map[string]string, len(catalog))
	for _, item := range catalog {
		labelsByID[item.ID] = item.Label
	}

	if labelsByID["new-style"] != "新增风格" {
		t.Fatalf("new-style label = %q, want 新增风格; catalog=%+v", labelsByID["new-style"], catalog)
	}
	if labelsByID["no-heading"] != "no-heading" {
		t.Fatalf("no-heading label = %q, want id fallback", labelsByID["no-heading"])
	}
	if _, ok := labelsByID["ignored"]; ok {
		t.Fatalf("non-markdown file should be ignored: %+v", catalog)
	}
	if _, ok := labelsByID["file"]; ok {
		t.Fatalf("nested markdown file should be ignored: %+v", catalog)
	}
}
