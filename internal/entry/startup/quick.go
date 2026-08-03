package startup

import (
	"fmt"
	"strings"

	"github.com/voocel/ainovel-cli/internal/host"
)

// PrepareQuick 将直接输入整理为可进入 Engine 的快速启动计划。
func PrepareQuick(req Request) (Plan, error) {
	prompt := strings.TrimSpace(req.UserPrompt)
	if prompt == "" {
		return Plan{}, fmt.Errorf("prompt is required")
	}
	budget, err := wordBudgetForPrompt(req.TargetTotalWords, prompt)
	if err != nil {
		return Plan{}, err
	}
	return Plan{
		Mode:        ModeQuick,
		DisplayName: "快速开始",
		StartPrompt: host.BuildStartPromptWithBudget(prompt, budget),
		RawPrompt:   prompt,
		WordBudget:  budget,
	}, nil
}
