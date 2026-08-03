// Package promptcompile assembles bounded, mode-scoped prompts from structured
// components. It never truncates caller-owned content; oversized requests must
// be split before the model call.
package promptcompile

import "fmt"

// Layer is one of the five prompt components assembled for every model call.
type Layer string

const (
	LayerRoleCore         Layer = "role_core"
	LayerModeContract     Layer = "mode_contract"
	LayerTaskContract     Layer = "task_contract"
	LayerEvidencePacket   Layer = "evidence_packet"
	LayerActiveStyleDelta Layer = "active_style_delta"
)

var orderedLayers = [...]Layer{
	LayerRoleCore,
	LayerModeContract,
	LayerTaskContract,
	LayerEvidencePacket,
	LayerActiveStyleDelta,
}

// Mode selects the single adaptation contract loaded for a call.
type Mode string

const (
	ModeChapter Mode = "chapter"
	ModeArc     Mode = "arc"
	ModeFree    Mode = "free"
)

func (m Mode) valid() bool {
	switch m {
	case ModeChapter, ModeArc, ModeFree:
		return true
	default:
		return false
	}
}

// Agent identifies the calling role and therefore its input budget.
type Agent string

const (
	AgentCoordinator    Agent = "coordinator"
	AgentWriter         Agent = "writer"
	AgentPlanner        Agent = "planner"
	AgentArchitect      Agent = "architect"
	AgentCharacter      Agent = "character"
	AgentEditor         Agent = "editor"
	AgentAuditor        Agent = "auditor"
	AgentSourceAnalyzer Agent = "source_analyzer"
)

// Budget defines the normal target and the pre-call hard limit.
type Budget struct {
	TargetTokens int `json:"target_tokens"`
	HardTokens   int `json:"hard_tokens"`
}

var defaultBudgets = map[Agent]Budget{
	AgentCoordinator:    {TargetTokens: 12_000, HardTokens: 16_000},
	AgentWriter:         {TargetTokens: 24_000, HardTokens: 32_000},
	AgentPlanner:        {TargetTokens: 28_000, HardTokens: 40_000},
	AgentArchitect:      {TargetTokens: 28_000, HardTokens: 40_000},
	AgentCharacter:      {TargetTokens: 32_000, HardTokens: 48_000},
	AgentEditor:         {TargetTokens: 32_000, HardTokens: 48_000},
	AgentAuditor:        {TargetTokens: 32_000, HardTokens: 48_000},
	AgentSourceAnalyzer: {TargetTokens: 20_000, HardTokens: 28_000},
}

// BudgetFor returns the default input budget for an agent.
func BudgetFor(agent Agent) (Budget, bool) {
	budget, ok := defaultBudgets[agent]
	return budget, ok
}

// Component is a caller-owned prompt fragment. Mode is required only for the
// mode_contract layer; on other layers it can scope mode-specific material.
// Compile rejects any scope different from Request.Mode.
type Component struct {
	Text string
	Mode Mode
}

// RuleKind describes how a structured natural-language constraint applies.
type RuleKind string

const (
	RuleRequired  RuleKind = "required"
	RuleForbidden RuleKind = "forbidden"
	RuleGuidance  RuleKind = "guidance"
)

func (k RuleKind) valid() bool {
	switch k {
	case RuleRequired, RuleForbidden, RuleGuidance:
		return true
	default:
		return false
	}
}

// Rule is a task-scoped natural-language constraint. ID is the stable rule ID.
// SemanticKey is optional structured metadata supplied by the rule normalizer;
// it lets Compile detect synonymous rules without guessing semantics from text.
type Rule struct {
	ID          string   `json:"rule_id"`
	Kind        RuleKind `json:"kind"`
	Text        string   `json:"text"`
	SemanticKey string   `json:"-"`
	Mode        Mode     `json:"-"`
}

// StyleDelta is one active, evidence-backed style correction. At most three
// deltas may be included in a call.
type StyleDelta struct {
	ID      string `json:"id"`
	Text    string `json:"text"`
	Example string `json:"example,omitempty"`
}

// Request contains exactly the five layers assembled by Compile. Rules are
// rendered inside task_contract so they do not become a sixth prompt layer.
type Request struct {
	Agent Agent
	Mode  Mode

	RoleCore       Component
	ModeContract   Component
	TaskContract   Component
	EvidencePacket Component
	StyleDeltas    []StyleDelta
	Rules          []Rule
}

// ComponentTokens reports token counts without retaining component text.
type ComponentTokens struct {
	Layer  Layer `json:"layer"`
	Tokens int   `json:"tokens"`
}

// Strategy describes how the compiler handled the request budget.
type Strategy string

const (
	StrategyWithinTarget              Strategy = "within_target"
	StrategyAboveTargetNoTruncation   Strategy = "above_target_no_truncation"
	StrategySplitRequiredNoTruncation Strategy = "split_required_no_truncation"
)

// Diagnostics is safe for telemetry: it contains sizes and fingerprints, but
// never prompt, evidence, rule text, or other source prose.
type Diagnostics struct {
	Agent                 Agent             `json:"agent"`
	Mode                  Mode              `json:"mode"`
	Components            []ComponentTokens `json:"components"`
	TotalTokens           int               `json:"total_tokens"`
	TargetTokens          int               `json:"target_tokens"`
	HardTokens            int               `json:"hard_tokens"`
	RuleCount             int               `json:"rule_count"`
	DeduplicatedRuleCount int               `json:"deduplicated_rule_count"`
	ForbiddenRuleCount    int               `json:"forbidden_rule_count"`
	Strategy              Strategy          `json:"strategy"`
	StaticPrefixHash      string            `json:"static_prefix_hash"`
}

// Result is the complete, untruncated prompt and its privacy-safe diagnostics.
type Result struct {
	Prompt       string
	SystemPrompt string
	UserPrompt   string
	Diagnostics  Diagnostics
}

func validateBudget(agent Agent, budget Budget) error {
	if budget.TargetTokens <= 0 || budget.HardTokens <= 0 {
		return fmt.Errorf("promptcompile: agent %q has non-positive budget", agent)
	}
	if budget.TargetTokens > budget.HardTokens {
		return fmt.Errorf("promptcompile: agent %q target budget exceeds hard budget", agent)
	}
	return nil
}
