package globalprompt

import (
	_ "embed"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
)

const MaxOverrideBytes = 64 << 10

const (
	FamilyClaude   = "claude"
	FamilyDeepSeek = "deepseek"
	FamilyGemini   = "gemini"
	FamilyGPT      = "gpt"
	FamilyGrok     = "grok"
	FamilyKimi     = "kimi"
)

type FamilyDefinition struct {
	Family   string
	Label    string
	Aliases  []string
	Fallback bool
}

var familyDefinitions = []FamilyDefinition{
	{Family: FamilyClaude, Label: "Claude", Aliases: []string{"claude", "anthropic", "opus"}},
	{Family: FamilyDeepSeek, Label: "DeepSeek", Aliases: []string{"deepseek"}, Fallback: true},
	{Family: FamilyGemini, Label: "Gemini", Aliases: []string{"gemini"}},
	{Family: FamilyGPT, Label: "GPT", Aliases: []string{"gpt", "openai", "zapi"}},
	{Family: FamilyGrok, Label: "Grok", Aliases: []string{"grok", "xai"}},
	{Family: FamilyKimi, Label: "Kimi", Aliases: []string{"kimi", "moonshot"}},
}

//go:embed global-prompt-claude.md
var embeddedClaudePrompt string

//go:embed global-prompt-deepseek.md
var embeddedDeepSeekPrompt string

//go:embed global-prompt-gemini.md
var embeddedGeminiPrompt string

//go:embed global-prompt-gpt.md
var embeddedGPTPrompt string

//go:embed global-prompt-grok.md
var embeddedGrokPrompt string

//go:embed global-prompt-kimi.md
var embeddedKimiPrompt string

type promptSnapshot struct {
	overrides map[string]string
	effective map[string]string
	prefixes  []string
}

type Registry struct {
	updateMu sync.Mutex
	state    atomic.Pointer[promptSnapshot]
}

func NewRegistry() *Registry {
	registry := &Registry{}
	registry.state.Store(buildSnapshot(nil, nil))
	return registry
}

var defaultRegistry = NewRegistry()

func Families() []FamilyDefinition {
	definitions := make([]FamilyDefinition, len(familyDefinitions))
	for i, definition := range familyDefinitions {
		definitions[i] = definition
		definitions[i].Aliases = append([]string(nil), definition.Aliases...)
	}
	return definitions
}

func BuiltIn(family string) (string, bool) {
	prompt, ok := builtInPrompts()[normalizeFamily(family)]
	return prompt, ok
}

func ValidateOverride(family, content string) error {
	normalized := normalizeFamily(family)
	if family != normalized {
		return fmt.Errorf("unsupported global prompt family %q", family)
	}
	if _, ok := BuiltIn(normalized); !ok {
		return fmt.Errorf("unsupported global prompt family %q", family)
	}
	if len(content) > MaxOverrideBytes {
		return fmt.Errorf("global prompt content exceeds %d bytes", MaxOverrideBytes)
	}
	if strings.ContainsRune(content, '\x00') {
		return fmt.Errorf("global prompt content must not contain NUL")
	}
	if strings.TrimSpace(content) == "" {
		return fmt.Errorf("global prompt content must not be empty")
	}
	return nil
}

func ValidateOverrides(overrides map[string]string) error {
	for family, content := range overrides {
		if err := ValidateOverride(family, content); err != nil {
			return err
		}
	}
	return nil
}

func ReplaceOverrides(overrides map[string]string) error {
	return defaultRegistry.ReplaceOverrides(overrides)
}

func Overrides() map[string]string {
	return defaultRegistry.Overrides()
}

func (r *Registry) ReplaceOverrides(overrides map[string]string) error {
	if err := ValidateOverrides(overrides); err != nil {
		return err
	}
	r.updateMu.Lock()
	defer r.updateMu.Unlock()
	// Hosts can retain a system prompt prepared before a Web edit. Keep every
	// previously active prefix for this process lifetime so the next model call
	// can remove that exact source before applying the new family prompt.
	previous := r.snapshot()
	r.state.Store(buildSnapshot(overrides, previous.prefixes))
	return nil
}

func (r *Registry) Overrides() map[string]string {
	snapshot := r.snapshot()
	return cloneStrings(snapshot.overrides)
}

// Text keeps the historical DeepSeek fallback.
func Text() string {
	return TextForModel("")
}

func TextForModel(model string) string {
	return defaultRegistry.textForModel(model)
}

func ContentForFamily(family string) (string, bool) {
	snapshot := defaultRegistry.snapshot()
	content, ok := snapshot.effective[normalizeFamily(family)]
	return content, ok
}

func Apply(systemPrompt string) string {
	return ApplyForModel("", systemPrompt)
}

// ApplyForModel uses one immutable snapshot for selection and stripping, so a
// concurrent Web update cannot mix old and new prompts in a single call.
func ApplyForModel(model, systemPrompt string) string {
	snapshot := defaultRegistry.snapshot()
	prefix := snapshot.effective[promptFamily(model)]
	body := stripWithSnapshot(snapshot, systemPrompt)
	if prefix == "" {
		return body
	}
	if body == "" {
		return prefix
	}
	return prefix + "\n\n" + body
}

func Strip(systemPrompt string) string {
	return stripWithSnapshot(defaultRegistry.snapshot(), systemPrompt)
}

func stripWithSnapshot(snapshot *promptSnapshot, systemPrompt string) string {
	body := strings.TrimSpace(systemPrompt)
	for _, prefix := range snapshot.prefixes {
		if body == prefix {
			return ""
		}
		if strings.HasPrefix(body, prefix+"\n\n") {
			return strings.TrimSpace(strings.TrimPrefix(body, prefix))
		}
	}
	return body
}

func (r *Registry) textForModel(model string) string {
	return r.snapshot().effective[promptFamily(model)]
}

func (r *Registry) snapshot() *promptSnapshot {
	if snapshot := r.state.Load(); snapshot != nil {
		return snapshot
	}
	snapshot := buildSnapshot(nil, nil)
	r.state.CompareAndSwap(nil, snapshot)
	return r.state.Load()
}

func buildSnapshot(overrides map[string]string, retainedPrefixes []string) *promptSnapshot {
	overrideCopy := cloneStrings(overrides)
	effective := builtInPrompts()
	for family, content := range overrideCopy {
		effective[normalizeFamily(family)] = strings.TrimSpace(content)
	}
	prefixes := make([]string, 0, len(retainedPrefixes)+len(effective)+len(builtInPrompts()))
	seen := make(map[string]bool)
	for _, prompt := range retainedPrefixes {
		appendPrefix(&prefixes, seen, prompt)
	}
	for _, prompt := range builtInPrompts() {
		appendPrefix(&prefixes, seen, prompt)
	}
	for _, prompt := range effective {
		appendPrefix(&prefixes, seen, prompt)
	}
	sort.SliceStable(prefixes, func(i, j int) bool {
		return len(prefixes[i]) > len(prefixes[j])
	})
	return &promptSnapshot{overrides: overrideCopy, effective: effective, prefixes: prefixes}
}

func appendPrefix(prefixes *[]string, seen map[string]bool, value string) {
	prefix := strings.TrimSpace(value)
	if prefix == "" || seen[prefix] {
		return
	}
	seen[prefix] = true
	*prefixes = append(*prefixes, prefix)
}

func builtInPrompts() map[string]string {
	return map[string]string{
		FamilyClaude:   strings.TrimSpace(embeddedClaudePrompt),
		FamilyDeepSeek: strings.TrimSpace(embeddedDeepSeekPrompt),
		FamilyGemini:   strings.TrimSpace(embeddedGeminiPrompt),
		FamilyGPT:      strings.TrimSpace(embeddedGPTPrompt),
		FamilyGrok:     strings.TrimSpace(embeddedGrokPrompt),
		FamilyKimi:     strings.TrimSpace(embeddedKimiPrompt),
	}
}

func cloneStrings(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	clone := make(map[string]string, len(values))
	for key, value := range values {
		clone[normalizeFamily(key)] = value
	}
	return clone
}

func normalizeFamily(family string) string {
	return strings.ToLower(strings.TrimSpace(family))
}

func promptFamily(model string) string {
	model = strings.ToLower(strings.TrimSpace(model))
	switch {
	case strings.Contains(model, "claude"), strings.Contains(model, "anthropic"), strings.Contains(model, "opus"):
		return FamilyClaude
	case strings.Contains(model, "gemini"):
		return FamilyGemini
	case strings.Contains(model, "grok"), strings.Contains(model, "xai"):
		return FamilyGrok
	case strings.Contains(model, "kimi"), strings.Contains(model, "moonshot"):
		return FamilyKimi
	case strings.Contains(model, "gpt"), strings.Contains(model, "openai"), strings.Contains(model, "zapi"):
		return FamilyGPT
	default:
		return FamilyDeepSeek
	}
}
