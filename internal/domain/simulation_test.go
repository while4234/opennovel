package domain

import (
	"strconv"
	"strings"
	"testing"
)

func TestCompactSimulationProfileCapsInjectedArrays(t *testing.T) {
	profile := &SimulationProfile{
		Version: SimulationProfileVersion,
		Corpus: SimulationCorpusManifest{
			Sources: make([]SimulationSource, 25),
		},
		Synthesis: SimulationSynthesis{
			Style: SimulationStyle{
				NarrativeVoice: longSimulationList("voice", maxCompactSimulationItems+5),
				DoNotCopy:      longSimulationList("copy", maxCompactSimulationItems+5),
			},
			Lexicon: SimulationLexicon{
				CommonWords: longSimulationList("word", maxCompactSimulationItems+5),
			},
			PlotDesign: SimulationPlotDesign{
				OpeningPatterns: longSimulationList("opening", maxCompactSimulationItems+5),
			},
			HookDesign: SimulationHookDesign{
				HookTypes: longSimulationList("hook", maxCompactSimulationItems+5),
			},
			PacingDensity: SimulationPacingDensity{
				SceneDensity: longSimulationList("density", maxCompactSimulationItems+5),
			},
			ReaderEngagement: SimulationReaderEngagement{
				Methods: longSimulationList("method", maxCompactSimulationItems+5),
			},
			RoleGuidance: SimulationRoleGuidance{
				Writer: longSimulationList("writer", maxCompactSimulationItems+5),
			},
		},
	}
	for i := range profile.Corpus.Sources {
		profile.Corpus.Sources[i] = SimulationSource{RelativePath: "source-" + strconv.Itoa(i)}
	}

	compact := CompactSimulationProfile(profile)
	if compact == nil {
		t.Fatal("compact profile is nil")
	}
	if got := len(compact.SourceFiles); got != maxCompactSimulationSourceFiles {
		t.Fatalf("SourceFiles len = %d, want %d", got, maxCompactSimulationSourceFiles)
	}
	if compact.Mode != "" {
		t.Fatalf("normal compact mode = %q, want empty", compact.Mode)
	}
	assertCompactLen(t, "Style.NarrativeVoice", compact.Style.NarrativeVoice)
	assertCompactLen(t, "Style.DoNotCopy", compact.Style.DoNotCopy)
	assertCompactLen(t, "Lexicon.CommonWords", compact.Lexicon.CommonWords)
	assertCompactLen(t, "PlotDesign.OpeningPatterns", compact.PlotDesign.OpeningPatterns)
	assertCompactLen(t, "HookDesign.HookTypes", compact.HookDesign.HookTypes)
	assertCompactLen(t, "PacingDensity.SceneDensity", compact.PacingDensity.SceneDensity)
	assertCompactLen(t, "ReaderEngagement.Methods", compact.ReaderEngagement.Methods)
	assertCompactLen(t, "RoleGuidance.Writer", compact.RoleGuidance.Writer)
	if got := len(profile.Synthesis.Style.NarrativeVoice); got != maxCompactSimulationItems+5 {
		t.Fatalf("CompactSimulationProfile mutated source profile, len = %d", got)
	}
}

func TestCompactSimulationProfileForReinforcedModeUsesExpandedLimits(t *testing.T) {
	profile := &SimulationProfile{
		Version: SimulationProfileVersion,
		Corpus: SimulationCorpusManifest{
			Sources: make([]SimulationSource, reinforcedSimulationSourceFiles+2),
		},
		Synthesis: SimulationSynthesis{
			Style: SimulationStyle{
				NarrativeVoice: append([]string{
					strings.Repeat("声", reinforcedSimulationItemRunes+20),
				}, longSimulationList("voice", reinforcedSimulationItems+2)...),
			},
			PlotDesign: SimulationPlotDesign{
				OpeningPatterns: longSimulationList("opening", reinforcedSimulationItems+2),
			},
			HookDesign: SimulationHookDesign{
				HookTypes: longSimulationList("hook", reinforcedSimulationItems+2),
			},
		},
	}
	for i := range profile.Corpus.Sources {
		profile.Corpus.Sources[i] = SimulationSource{RelativePath: "reinforced-source-" + strconv.Itoa(i)}
	}

	compact := CompactSimulationProfileForMode(profile, "reinforced")
	if compact == nil {
		t.Fatal("compact profile is nil")
	}
	if compact.Mode != "reinforced" {
		t.Fatalf("Mode = %q, want reinforced", compact.Mode)
	}
	if got := len(compact.SourceFiles); got != reinforcedSimulationSourceFiles {
		t.Fatalf("SourceFiles len = %d, want %d", got, reinforcedSimulationSourceFiles)
	}
	if got := len(compact.Style.NarrativeVoice); got != reinforcedSimulationItems {
		t.Fatalf("Style.NarrativeVoice len = %d, want %d", got, reinforcedSimulationItems)
	}
	if got := len(compact.PlotDesign.OpeningPatterns); got != reinforcedSimulationItems {
		t.Fatalf("PlotDesign.OpeningPatterns len = %d, want %d", got, reinforcedSimulationItems)
	}
	if got := len(compact.HookDesign.HookTypes); got != reinforcedSimulationItems {
		t.Fatalf("HookDesign.HookTypes len = %d, want %d", got, reinforcedSimulationItems)
	}
	first := compact.Style.NarrativeVoice[0]
	if !strings.HasSuffix(first, "...") || len([]rune(first)) != reinforcedSimulationItemRunes+3 {
		t.Fatalf("reinforced item was not truncated at expanded rune limit: len=%d value=%q", len([]rune(first)), first)
	}
}

func assertCompactLen(t *testing.T, name string, got []string) {
	t.Helper()
	if len(got) != maxCompactSimulationItems {
		t.Fatalf("%s len = %d, want %d", name, len(got), maxCompactSimulationItems)
	}
}

func longSimulationList(prefix string, n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = prefix + "-" + strconv.Itoa(i)
	}
	return out
}
