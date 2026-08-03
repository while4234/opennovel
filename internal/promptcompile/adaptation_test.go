package promptcompile

import (
	"strings"
	"testing"
)

func TestAdaptationModeContractContainsOnlySelectedMode(t *testing.T) {
	tests := []struct {
		mode      Mode
		forbidden []string
	}{
		{mode: ModeChapter, forbidden: []string{`"mode":"arc"`, `"mode":"free"`, "mainline_preservation", "target_coherence"}},
		{mode: ModeArc, forbidden: []string{`"mode":"chapter"`, `"mode":"free"`, "detail_preservation_with_split", "target_coherence"}},
		{mode: ModeFree, forbidden: []string{`"mode":"chapter"`, `"mode":"arc"`, "detail_preservation_with_split", "mainline_preservation"}},
	}
	for _, test := range tests {
		contract, err := AdaptationModeContract(test.mode, AgentWriter)
		if err != nil {
			t.Fatalf("mode %s: %v", test.mode, err)
		}
		for _, forbidden := range test.forbidden {
			if strings.Contains(contract, forbidden) {
				t.Fatalf("mode %s contract mixes %q: %s", test.mode, forbidden, contract)
			}
		}
	}
}
