package promptcompile

import (
	"encoding/json"
	"fmt"
)

type adaptationModeContract struct {
	Mode           Mode     `json:"mode"`
	Policy         string   `json:"policy"`
	SourceRole     string   `json:"source_role"`
	RequiredChecks []string `json:"required_checks"`
}

// AdaptationModeContract returns only the selected mode's compact contract.
// Event IDs, source prose, character state, and user rules belong in the task
// and evidence components, not in this static prefix.
func AdaptationModeContract(mode Mode, agent Agent) (string, error) {
	contract := adaptationModeContract{Mode: mode}
	switch mode {
	case ModeChapter:
		contract.Policy = "detail_preservation_with_split"
		contract.SourceRole = "complete source chapter as background; current source_segment is the only writing responsibility"
		contract.RequiredChecks = []string{
			"preserve unaffected details",
			"rewrite only affected complete scenes",
			"keep ordered segments gap-free and non-duplicated",
		}
	case ModeArc:
		contract.Policy = "mainline_preservation"
		contract.SourceRole = "mainline evidence and causal anchors"
		contract.RequiredChecks = []string{
			"bind every required mainline event to a target chapter",
			"do not let added events displace unfulfilled mainline events",
			"verify event evidence independently of writer self-report",
		}
	case ModeFree:
		contract.Policy = "target_coherence"
		contract.SourceRole = "optional reference except user-locked facts"
		contract.RequiredChecks = []string{
			"preserve user-locked facts only",
			"verify target causality and information sources",
			"require relationship and setting transitions before later states",
		}
	default:
		return "", validationError("invalid_mode", "mode %q is not supported", mode)
	}
	if agent == AgentSourceAnalyzer {
		contract.RequiredChecks = []string{"extract only evidence-backed source facts, scenes, dependencies, and state changes"}
	}
	raw, err := json.Marshal(contract)
	if err != nil {
		return "", fmt.Errorf("promptcompile: marshal adaptation mode contract: %w", err)
	}
	return string(raw), nil
}
