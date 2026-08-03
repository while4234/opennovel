// Package promptreview audits compact role prompts for budget and semantic
// capability retention. A shorter prompt is acceptable only when every
// required capability remains represented or is explicitly delegated to a
// structured contract/tool boundary.
package promptreview

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"unicode"

	"github.com/voocel/ainovel-cli/internal/promptcompile"
)

type Capability struct {
	ID          string
	Description string
	AnyOf       []string
}

type Policy struct {
	Role             string
	MaxTokens        int
	Capabilities     []Capability
	ForbiddenPhrases []string
}

type Finding struct {
	Code    string `json:"code"`
	Subject string `json:"subject"`
	Detail  string `json:"detail"`
}

type Report struct {
	Role         string    `json:"role"`
	Tokens       int       `json:"tokens"`
	MaxTokens    int       `json:"max_tokens"`
	Capabilities int       `json:"capabilities"`
	Passed       bool      `json:"passed"`
	Findings     []Finding `json:"findings,omitempty"`
}

func Review(ctx context.Context, prompt string, counter promptcompile.TokenCounter, policy Policy) (Report, error) {
	if counter == nil {
		return Report{}, fmt.Errorf("promptreview: token counter is required")
	}
	report := Report{Role: policy.Role, MaxTokens: policy.MaxTokens, Capabilities: len(policy.Capabilities)}
	tokens, err := counter.CountTokens(ctx, prompt)
	if err != nil {
		return report, fmt.Errorf("promptreview: count tokens: %w", err)
	}
	report.Tokens = tokens
	if policy.MaxTokens > 0 && tokens > policy.MaxTokens {
		report.Findings = append(report.Findings, Finding{
			Code:    "role_core_over_budget",
			Subject: policy.Role,
			Detail:  fmt.Sprintf("prompt has %d tokens; limit is %d", tokens, policy.MaxTokens),
		})
	}
	for _, capability := range policy.Capabilities {
		if containsAny(prompt, capability.AnyOf) {
			continue
		}
		report.Findings = append(report.Findings, Finding{
			Code:    "missing_capability",
			Subject: capability.ID,
			Detail:  capability.Description,
		})
	}
	for _, phrase := range policy.ForbiddenPhrases {
		if !strings.Contains(prompt, phrase) {
			continue
		}
		report.Findings = append(report.Findings, Finding{
			Code:    "forbidden_prompt_content",
			Subject: phrase,
			Detail:  "content belongs in a mode-scoped or task-scoped component",
		})
	}
	for _, duplicate := range duplicateParagraphs(prompt) {
		report.Findings = append(report.Findings, Finding{
			Code:    "duplicate_paragraph",
			Subject: duplicate,
			Detail:  "normalized paragraph appears more than once",
		})
	}
	sort.Slice(report.Findings, func(i, j int) bool {
		if report.Findings[i].Code != report.Findings[j].Code {
			return report.Findings[i].Code < report.Findings[j].Code
		}
		return report.Findings[i].Subject < report.Findings[j].Subject
	})
	report.Passed = len(report.Findings) == 0
	return report, nil
}

func containsAny(text string, alternatives []string) bool {
	for _, alternative := range alternatives {
		if strings.TrimSpace(alternative) != "" && strings.Contains(text, alternative) {
			return true
		}
	}
	return false
}

func duplicateParagraphs(prompt string) []string {
	seen := make(map[string]bool)
	duplicates := make(map[string]bool)
	for _, paragraph := range strings.Split(prompt, "\n\n") {
		normalized := normalizeParagraph(paragraph)
		if len([]rune(normalized)) < 24 {
			continue
		}
		if seen[normalized] {
			duplicates[shortParagraph(paragraph)] = true
		}
		seen[normalized] = true
	}
	out := make([]string, 0, len(duplicates))
	for duplicate := range duplicates {
		out = append(out, duplicate)
	}
	sort.Strings(out)
	return out
}

func normalizeParagraph(text string) string {
	var builder strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(text)) {
		if unicode.IsSpace(r) || unicode.IsPunct(r) || unicode.IsSymbol(r) {
			continue
		}
		builder.WriteRune(r)
	}
	return builder.String()
}

func shortParagraph(text string) string {
	runes := []rune(strings.TrimSpace(text))
	if len(runes) > 60 {
		runes = runes[:60]
	}
	return string(runes)
}
