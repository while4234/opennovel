package assets

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path"
	"sort"
	"strings"

	"github.com/voocel/ainovel-cli/internal/globalprompt"
	"github.com/voocel/ainovel-cli/internal/tools"
)

//go:embed prompts/*.md
var promptsFS embed.FS

//go:embed references
var referencesFS embed.FS

//go:embed styles/*.md
var stylesFS embed.FS

// Prompts 表示嵌入的提示词集合。
type Prompts struct {
	Coordinator                 string
	ArchitectShort              string
	ArchitectLong               string
	Character                   string
	Writer                      string
	Editor                      string
	ImportFoundation            string
	ImportFoundationMerge       string
	ImportAnalyzer              string
	AdaptationPlanner           string
	SimulationSource            string
	SimulationMerge             string
	NormalManuscriptPolish      string
	NormalManuscriptRewrite     string
	NormalManuscriptAudit       string
	AdaptationManuscriptPolish  string
	AdaptationManuscriptRewrite string
	AdaptationManuscriptAudit   string
	NormalExpansionPlanner      string
	AdaptationExpansionPlanner  string
}

// Bundle 表示运行所需的静态资源集合。
type Bundle struct {
	References tools.References
	Prompts    Prompts
	Styles     map[string]string
}

// StyleDescriptor 是 Web/API 展示用的写作风格条目。
type StyleDescriptor struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

// StyleSource merges runtime Markdown files over the embedded style defaults.
// An empty directory keeps the release-safe embedded-only behavior.
type StyleSource struct {
	directory string
}

func NewStyleSource(directory string) StyleSource {
	return StyleSource{directory: strings.TrimSpace(directory)}
}

func EmbeddedStyleSource() StyleSource {
	return StyleSource{}
}

func (s StyleSource) Load(style string) Bundle {
	bundle := Load(style)
	for id, content := range s.runtimeStyles() {
		bundle.Styles[id] = content
	}
	return bundle
}

func (s StyleSource) Catalog() []StyleDescriptor {
	styles := loadStylesFromFS(stylesFS, "styles")
	for id, content := range s.runtimeStyles() {
		styles[id] = content
	}
	return styleCatalogFromStyles(styles)
}

func (s StyleSource) HasStyle(style string) bool {
	style = NormalizeStyleID(style)
	for _, item := range s.Catalog() {
		if item.ID == style {
			return true
		}
	}
	return false
}

func (s StyleSource) runtimeStyles() map[string]string {
	if s.directory == "" {
		return nil
	}
	return loadStylesFromFS(os.DirFS(s.directory), ".")
}

// Load 返回指定风格对应的资源集合。
func Load(style string) Bundle {
	style = NormalizeStyleID(style)
	return Bundle{
		References: loadReferences(style),
		Prompts:    loadPrompts(),
		Styles:     loadStyles(),
	}
}

// NormalizeStyleID 返回配置中实际使用的 style id。
func NormalizeStyleID(style string) string {
	style = strings.TrimSpace(style)
	if style == "" {
		return "default"
	}
	return style
}

// StyleCatalog 返回嵌入的写作风格目录，显示名来自 Markdown 第一行标题。
func StyleCatalog() []StyleDescriptor {
	return styleCatalogFromFS(stylesFS, "styles")
}

// HasStyle 判断 style id 是否存在于嵌入资源中。
func HasStyle(style string) bool {
	style = NormalizeStyleID(style)
	for _, item := range StyleCatalog() {
		if item.ID == style {
			return true
		}
	}
	return false
}

func loadReferences(style string) tools.References {
	style = NormalizeStyleID(style)
	refs := tools.References{
		ChapterGuide:                    mustRead(referencesFS, "references/chapter-guide.md"),
		HookTechniques:                  mustRead(referencesFS, "references/hook-techniques.md"),
		QualityChecklist:                mustRead(referencesFS, "references/quality-checklist.md"),
		OutlineTemplate:                 mustRead(referencesFS, "references/outline-template.md"),
		CharacterTemplate:               mustRead(referencesFS, "references/character-template.md"),
		ChapterTemplate:                 mustRead(referencesFS, "references/chapter-template.md"),
		Consistency:                     mustRead(referencesFS, "references/consistency.md"),
		ContentExpansion:                mustRead(referencesFS, "references/content-expansion.md"),
		DialogueWriting:                 mustRead(referencesFS, "references/dialogue-writing.md"),
		LongformPlanning:                mustRead(referencesFS, "references/longform-planning.md"),
		Differentiation:                 mustRead(referencesFS, "references/differentiation.md"),
		AntiAITone:                      mustRead(referencesFS, "references/anti-ai-tone.md"),
		AdaptationWriter:                mustRead(promptsFS, "prompts/writer-adaptation.md"),
		AdaptationEditorPreserveDetails: mustRead(promptsFS, "prompts/editor-adaptation-preserve_details.md"),
		AdaptationEditorFullRewrite:     mustRead(promptsFS, "prompts/editor-adaptation-full_rewrite.md"),
	}
	if style != "" && style != "default" {
		genreDir := "references/genres/" + style + "/"
		if data, err := referencesFS.ReadFile(genreDir + "style-references.md"); err == nil {
			refs.StyleReference = string(data)
		}
		if data, err := referencesFS.ReadFile(genreDir + "arc-templates.md"); err == nil {
			refs.ArcTemplates = string(data)
		}
	}
	return refs
}

func loadPrompts() Prompts {
	return Prompts{
		Coordinator:                 loadRolePrompt("prompts/coordinator.md", "coordinator"),
		ArchitectShort:              loadRolePrompt("prompts/architect-short.md", "architect"),
		ArchitectLong:               loadRolePrompt("prompts/architect-long.md", "architect"),
		Character:                   loadRolePrompt("prompts/character.md", "character"),
		Writer:                      loadRolePrompt("prompts/writer.md", "writer"),
		Editor:                      loadRolePrompt("prompts/editor.md", "editor"),
		ImportFoundation:            loadSystemPrompt("prompts/import-foundation.md"),
		ImportFoundationMerge:       loadSystemPrompt("prompts/import-foundation-merge.md"),
		ImportAnalyzer:              loadSystemPrompt("prompts/import-chapter-analyzer.md"),
		AdaptationPlanner:           loadSystemPrompt("prompts/adaptation-planner.md"),
		SimulationSource:            loadSystemPrompt("prompts/simulation-source.md"),
		SimulationMerge:             loadSystemPrompt("prompts/simulation-merge.md"),
		NormalManuscriptPolish:      loadSystemPrompt("prompts/manuscript-normal-polish.md"),
		NormalManuscriptRewrite:     loadSystemPrompt("prompts/manuscript-normal-rewrite.md"),
		NormalManuscriptAudit:       loadSystemPrompt("prompts/manuscript-normal-audit.md"),
		AdaptationManuscriptPolish:  loadSystemPrompt("prompts/manuscript-adaptation-polish.md"),
		AdaptationManuscriptRewrite: loadSystemPrompt("prompts/manuscript-adaptation-rewrite.md"),
		AdaptationManuscriptAudit:   loadSystemPrompt("prompts/manuscript-adaptation-audit.md"),
		NormalExpansionPlanner:      loadSystemPrompt("prompts/normal-expansion-planner.md"),
		AdaptationExpansionPlanner:  loadSystemPrompt("prompts/adaptation-expansion-planner.md"),
	}
}

func loadRolePrompt(path, role string) string {
	return globalprompt.Apply(withSimulationGuidance(mustRead(promptsFS, path), role))
}

func loadSystemPrompt(path string) string {
	return globalprompt.Apply(mustRead(promptsFS, path))
}

func withSimulationGuidance(prompt, role string) string {
	return prompt + "\n\n" + strings.ReplaceAll(simulationGuidance, "{{role}}", role)
}

const simulationGuidance = `## 仿写契约

仿写的唯一运行时事实源是 novel_context 返回的 role-bound simulation_contract / simulation_effective。{{role}} 只读取当前角色视图里的 must、should、avoid；不得从整份画像自行挑选规则，也不得用兼容字段推导 mode。

- 只以 simulation_effective.effective_mode 和 status 判断是否生效。status=inactive 时不使用仿写 guidance；status=degraded 时遵守 reasons 并保持保守。
- normal：should 仅为建议，偏离主观风格建议不阻塞创作；avoid 始终有效。
- reinforced：只执行当前角色视图中明确列出的 obligations；must 也不能覆盖用户要求、creative brief、已确认 foundation、改编合同、章节合同或当前 POV。
- Coordinator 只读取 health/status；Architect 读取 planning view；Writer 读取 chapter view；Editor 读取 review view。不要索取其他角色视图。
- 上下文只含 portable 抽象 feature。禁止索取或依赖 raw source、source_reports、本地路径、安全索引、来源专名或 signature phrase 库；禁止复制来源句子、人物、地名、专有设定或固定桥段。

旧 simulation_profile / simulation_mode 仅是由 simulation_effective 同源生成的迁移字段，不得独立解释。`

func loadStyles() map[string]string {
	return loadStylesFromFS(stylesFS, "styles")
}

func loadStylesFromFS(fsys fs.FS, dir string) map[string]string {
	styles := make(map[string]string)
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return styles
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ".md") {
			continue
		}
		name := e.Name()[:len(e.Name())-len(".md")]
		data, err := fs.ReadFile(fsys, path.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		styles[name] = string(data)
	}
	return styles
}

func styleCatalogFromFS(fsys fs.FS, dir string) []StyleDescriptor {
	return styleCatalogFromStyles(loadStylesFromFS(fsys, dir))
}

func styleCatalogFromStyles(contents map[string]string) []StyleDescriptor {
	styles := make([]StyleDescriptor, 0, len(contents))
	for id, content := range contents {
		styles = append(styles, StyleDescriptor{ID: id, Label: styleLabel(id, content)})
	}
	sort.Slice(styles, func(i, j int) bool {
		if styles[i].Label != styles[j].Label {
			return styles[i].Label < styles[j].Label
		}
		return styles[i].ID < styles[j].ID
	})
	return styles
}

func styleLabel(id, content string) string {
	firstLine := content
	if idx := strings.IndexAny(firstLine, "\r\n"); idx >= 0 {
		firstLine = firstLine[:idx]
	}
	firstLine = strings.TrimPrefix(firstLine, "\ufeff")
	label := strings.TrimSpace(firstLine)
	label = strings.TrimLeft(label, "#")
	label = strings.TrimSpace(label)
	if label == "" {
		return id
	}
	return label
}

func mustRead(fs embed.FS, path string) string {
	data, err := fs.ReadFile(path)
	if err != nil {
		panic(fmt.Sprintf("embed read %s: %v", path, err))
	}
	return string(data)
}
