package globalprompt

import (
	_ "embed"
	"sort"
	"strings"
)

const (
	familyDeepSeek = "deepseek"
	familyGPT      = "gpt"
	familyGrok     = "grok"
)

//go:embed global-prompt-deepseek.md
var embeddedDeepSeekPrompt string

//go:embed global-prompt-gpt.md
var embeddedGPTPrompt string

//go:embed global-prompt-grok.md
var embeddedGrokPrompt string

// Text returns the embedded global prompt template after trimming surrounding
// whitespace. It keeps the historical default as the DeepSeek prompt.
func Text() string {
	return TextForModel("")
}

// TextForModel returns the prompt template selected for a provider/model name.
// GPT/OpenAI-like models use global-prompt-gpt.md; everything else keeps the
// DeepSeek-oriented default for backward compatibility.
func TextForModel(model string) string {
	switch promptFamily(model) {
	case familyGPT:
		return strings.TrimSpace(embeddedGPTPrompt)
	case familyGrok:
		return strings.TrimSpace(embeddedGrokPrompt)
	default:
		return strings.TrimSpace(embeddedDeepSeekPrompt)
	}
}

// Apply prepends the role-agnostic global prompt to a system prompt. It is
// intentionally idempotent so callers can use it at both resource-loading and
// LLM-call boundaries without duplicating the prefix. Writing-only rules must
// live in the Writer/Editor prompt or a dedicated tool gate: this prefix also
// enters bounded planner calls.
func Apply(systemPrompt string) string {
	return ApplyForModel("", systemPrompt)
}

// ApplyForModel prepends the model-selected global prompt to a system prompt.
// If a different known global prompt is already present, it is replaced so
// runtime /model switches can follow the active model.
func ApplyForModel(model, systemPrompt string) string {
	prefix := TextForModel(model)
	if prefix == "" {
		return Strip(systemPrompt)
	}

	body := Strip(systemPrompt)
	if body == "" {
		return prefix
	}
	return prefix + "\n\n" + body
}

// Strip removes any known global prompt prefix from a system prompt.
func Strip(systemPrompt string) string {
	body := strings.TrimSpace(systemPrompt)
	for _, prefix := range knownPrefixes() {
		if prefix == "" {
			continue
		}
		if body == prefix {
			return ""
		}
		if strings.HasPrefix(body, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(body, prefix))
		}
	}
	return body
}

func knownPrefixes() []string {
	deepSeek := strings.TrimSpace(embeddedDeepSeekPrompt)
	gpt := strings.TrimSpace(embeddedGPTPrompt)
	grok := strings.TrimSpace(embeddedGrokPrompt)
	prefixes := make([]string, 0, 3)
	if deepSeek != "" {
		prefixes = append(prefixes, deepSeek)
	}
	if gpt != "" && gpt != deepSeek {
		prefixes = append(prefixes, gpt)
	}
	if grok != "" && grok != deepSeek && grok != gpt {
		prefixes = append(prefixes, grok)
	}
	sort.SliceStable(prefixes, func(i, j int) bool {
		return len(prefixes[i]) > len(prefixes[j])
	})
	return prefixes
}

func promptFamily(model string) string {
	model = strings.ToLower(strings.TrimSpace(model))
	switch {
	case strings.Contains(model, "grok"):
		return familyGrok
	case strings.Contains(model, "xai"):
		return familyGrok
	case strings.Contains(model, "gpt"):
		return familyGPT
	case strings.Contains(model, "openai"):
		return familyGPT
	case strings.Contains(model, "zapi"):
		return familyGPT
	default:
		return familyDeepSeek
	}
}
