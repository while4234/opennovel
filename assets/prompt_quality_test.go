package assets

import (
	"strings"
	"testing"

	"github.com/voocel/ainovel-cli/internal/globalprompt"
	"github.com/voocel/ainovel-cli/internal/promptcompile"
	"github.com/voocel/ainovel-cli/internal/promptreview"
)

func TestCompactRolePromptsRetainQualityCapabilities(t *testing.T) {
	bundle := Load("")
	policies := []struct {
		name   string
		prompt string
		policy promptreview.Policy
	}{
		{
			name: "writer", prompt: bundle.Prompts.Writer,
			policy: promptreview.Policy{Role: "writer", MaxTokens: 2500, ForbiddenPhrases: []string{"Chapter 模式", "Arc 模式", "Free 模式"}, Capabilities: []promptreview.Capability{
				{ID: "workflow", Description: "plan/draft/check/commit workflow", AnyOf: []string{"Never call `check_de_ai` before"}},
				{ID: "full_prose", Description: "complete prose must be persisted", AnyOf: []string{"完整正文"}},
				{ID: "state", Description: "causal and relationship state continuity", AnyOf: []string{"关系变化有事件前因"}},
				{ID: "anti_ai", Description: "human-like prose correction", AnyOf: []string{"解释性复盘"}},
				{ID: "scene_emotion", Description: "emotion is carried by scene behavior", AnyOf: []string{"动作、对白、感官和选择承载情绪"}},
				{ID: "dialogue_voice", Description: "dialogue reflects identity and pressure", AnyOf: []string{"身份、利益和当下压力"}},
				{ID: "adaptation_evidence", Description: "independent adaptation evidence", AnyOf: []string{"Writer 自报通过不算证据"}},
				{ID: "simulation", Description: "role-bound simulation safety boundary", AnyOf: []string{"simulation_contract"}},
				{ID: "chapter_contract", Description: "scene-level chapter contract review", AnyOf: []string{"chronology, named locations, POV"}},
				{ID: "draft_budget", Description: "first draft targets the planned range before generation", AnyOf: []string{"allocate a concrete first-draft word target", "Do not knowingly generate an oversized chapter"}},
			}},
		},
		{
			name: "editor", prompt: bundle.Prompts.Editor,
			policy: promptreview.Policy{Role: "editor", MaxTokens: 2500, ForbiddenPhrases: []string{"Chapter 模式", "Arc 模式", "Free 模式"}, Capabilities: []promptreview.Capability{
				{ID: "read_body", Description: "must read complete prose", AnyOf: []string{"阅读完整正文"}},
				{ID: "seven_dimensions", Description: "seven quality dimensions", AnyOf: []string{"设定一致性、人物动机、节奏、因果与场景衔接、伏笔、钩子、审美品质"}},
				{ID: "evidence", Description: "issues require evidence", AnyOf: []string{"正文片段或状态证据"}},
				{ID: "severity", Description: "critical/error/warning verdicts", AnyOf: []string{"critical"}},
				{ID: "writer_independence", Description: "writer self-report is not evidence", AnyOf: []string{"summary/passed"}},
				{ID: "chapter_source", Description: "segmented detail mode reads full source and boundaries", AnyOf: []string{"完整来源章、当前职责及相邻 segment 边界"}},
				{ID: "simulation", Description: "simulation contract review", AnyOf: []string{"Editor review view"}},
				{ID: "planning_review_scope", Description: "volume skeleton review uses only the bounded review scope", AnyOf: []string{"Editor MUST NOT call `planning_volume`"}},
			}},
		},
		{
			name: "architect_short", prompt: bundle.Prompts.ArchitectShort,
			policy: promptreview.Policy{Role: "architect", MaxTokens: 2500, ForbiddenPhrases: []string{"Chapter 模式", "Arc 模式", "Free 模式"}, Capabilities: []promptreview.Capability{
				{ID: "structured", Description: "structured planning artifacts", AnyOf: []string{"结构化字段"}},
				{ID: "distinct", Description: "non-duplicated chapter duties", AnyOf: []string{"不能只换标题重复"}},
				{ID: "relationship", Description: "relationship milestone causality", AnyOf: []string{"关系里程碑"}},
				{ID: "mode", Description: "current-mode-only planning", AnyOf: []string{"当前 mode contract", "adaptation mode contract"}},
				{ID: "simulation", Description: "simulation structure boundary", AnyOf: []string{"planning view"}},
				{ID: "persist", Description: "short foundation persistence lifecycle", AnyOf: []string{"premise → world_rules → outline"}},
				{ID: "ready", Description: "must persist until foundation ready", AnyOf: []string{"foundation_ready=true"}},
			}},
		},
		{
			name: "architect_long", prompt: bundle.Prompts.ArchitectLong,
			policy: promptreview.Policy{Role: "architect", MaxTokens: 2500, ForbiddenPhrases: []string{"Chapter 模式", "Arc 模式", "Free 模式"}, Capabilities: []promptreview.Capability{
				{ID: "layers", Description: "long-form hierarchy", AnyOf: []string{"layered_outline"}},
				{ID: "window", Description: "semantic batching", AnyOf: []string{"当前需要的卷/弧/章节批次"}},
				{ID: "event_contract", Description: "event and state fields", AnyOf: []string{"关系/状态变化"}},
				{ID: "budget", Description: "split instead of truncate", AnyOf: []string{"禁止静默截断"}},
				{ID: "mode", Description: "current-mode-only planning", AnyOf: []string{"当前 mode contract", "adaptation mode contract"}},
				{ID: "simulation", Description: "simulation structure boundary", AnyOf: []string{"planning view"}},
				{ID: "persist", Description: "long foundation persistence lifecycle", AnyOf: []string{"premise → world_rules → layered_outline → update_compass"}},
				{ID: "expand_arc", Description: "expands skeleton arcs", AnyOf: []string{"expand_arc"}},
				{ID: "repair_arc", Description: "repairs duplicated arc promises", AnyOf: []string{"repair_arc"}},
				{ID: "append_volume", Description: "adds a planned volume", AnyOf: []string{"append_volume"}},
				{ID: "complete_book", Description: "guards completion", AnyOf: []string{"complete_book"}},
			}},
		},
		{
			name: "coordinator", prompt: bundle.Prompts.Coordinator,
			policy: promptreview.Policy{Role: "coordinator", MaxTokens: 2500, Capabilities: []promptreview.Capability{
				{ID: "host_direct", Description: "host command dispatches without lookup", AnyOf: []string{"不先查 `novel_context`"}},
				{ID: "roles", Description: "correct role routing", AnyOf: []string{"Architect"}},
				{ID: "no_rules", Description: "does not duplicate writing rules", AnyOf: []string{"不重复携带具体写作规则"}},
				{ID: "failure", Description: "routes failure type only", AnyOf: []string{"失败任务只携带失败类型"}},
				{ID: "restore", Description: "resume waits for host command", AnyOf: []string{"收到 `[恢复]` 只确认进度并等待 Host 指令"}},
				{ID: "rewrite_queue", Description: "completed chapter repair goes through editor", AnyOf: []string{"先派 Editor 用 `save_review`"}},
				{ID: "style_rules", Description: "writing style routes to durable user rules", AnyOf: []string{"调用 `save_user_rules`"}},
			}},
		},
		{
			name: "source_analyzer", prompt: bundle.Prompts.ImportAnalyzer,
			policy: promptreview.Policy{Role: "source_analyzer", MaxTokens: 2500, Capabilities: []promptreview.Capability{
				{ID: "evidence", Description: "source-only facts", AnyOf: []string{"所有结论必须能在本章正文中定位"}},
				{ID: "events", Description: "mainline-worthy key events", AnyOf: []string{"初遇、案件核心、身份揭示"}},
				{ID: "relations", Description: "relationship transitions", AnyOf: []string{"RELATIONSHIPS"}},
				{ID: "state", Description: "state transitions", AnyOf: []string{"STATE_CHANGES"}},
			}},
		},
		{
			name: "adaptation_planner", prompt: bundle.Prompts.AdaptationPlanner,
			policy: promptreview.Policy{Role: "planner", MaxTokens: 2500, ForbiddenPhrases: []string{"arc/full_rewrite"}, Capabilities: []promptreview.Capability{
				{ID: "proposal", Description: "proposal remains unconfirmed until user action", AnyOf: []string{"status` must be `proposal`"}},
				{ID: "events", Description: "stable source/target event bindings", AnyOf: []string{"event_ids"}},
				{ID: "added_events", Description: "added plot is separately identified", AnyOf: []string{"added_event_ids"}},
				{ID: "rules", Description: "stable rule bindings", AnyOf: []string{"rule_ids"}},
				{ID: "mode", Description: "only current adaptation mode applies", AnyOf: []string{"current-mode contract"}},
			}},
		},
	}
	counter := promptcompile.AgentcoreEstimateCounter{}
	for _, test := range policies {
		t.Run(test.name, func(t *testing.T) {
			report, err := promptreview.Review(t.Context(), globalprompt.Strip(test.prompt), counter, test.policy)
			if err != nil {
				t.Fatalf("Review: %v", err)
			}
			if !report.Passed {
				t.Fatalf("prompt quality review failed: %+v", report)
			}
			for _, capability := range test.policy.Capabilities {
				mutated := globalprompt.Strip(test.prompt)
				for _, marker := range capability.AnyOf {
					mutated = strings.ReplaceAll(mutated, marker, "")
				}
				mutatedReport, err := promptreview.Review(t.Context(), mutated, counter, test.policy)
				if err != nil {
					t.Fatalf("Review mutation %s: %v", capability.ID, err)
				}
				if !promptReviewHasMissingCapability(mutatedReport, capability.ID) {
					t.Fatalf("removing %s markers did not fail that capability: %+v", capability.ID, mutatedReport)
				}
			}
		})
	}
}

func TestWriterPromptCanStripModelGlobalPrefix(t *testing.T) {
	prompt := Load("").Prompts.Writer
	stripped := globalprompt.Strip(prompt)
	if strings.Contains(stripped, "系统核心指令") {
		t.Fatalf("writer prompt retained global prefix after Strip")
	}
	t.Logf("writer prompt runes: full=%d stripped=%d", len([]rune(prompt)), len([]rune(stripped)))
}

func promptReviewHasMissingCapability(report promptreview.Report, capabilityID string) bool {
	for _, finding := range report.Findings {
		if finding.Code == "missing_capability" && finding.Subject == capabilityID {
			return true
		}
	}
	return false
}
