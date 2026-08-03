package domain

import (
	"testing"
	"time"
)

func TestCompileSimulationContractModeBudgetsAndObligations(t *testing.T) {
	profile := simulationContractTestProfile(t, "fresh")
	normal, err := CompileSimulationContract(SimulationContractInput{
		Profile: &profile, RequestedMode: SimulationModeNormal, Now: time.Unix(1, 0).UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	reinforced, err := CompileSimulationContract(SimulationContractInput{
		Profile: &profile, RequestedMode: SimulationModeReinforced, PreviousRevision: normal.Revision,
		Now: time.Unix(2, 0).UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	normalArchitect := normal.View(SimulationRoleArchitect, "planning")
	reinforcedArchitect := reinforced.View(SimulationRoleArchitect, "planning")
	if normalArchitect == nil || reinforcedArchitect == nil {
		t.Fatal("missing Architect views")
	}
	if len(normalArchitect.Must) != 0 {
		t.Fatal("normal mode must not promote subjective imitation features to must")
	}
	if simulationContractViewCount(*reinforcedArchitect) <= simulationContractViewCount(*normalArchitect) {
		t.Fatal("reinforced mode must select measurably more Architect features")
	}
	if len(reinforcedArchitect.Must) == 0 {
		t.Fatal("reinforced mode should promote eligible objective features")
	}
	writer := reinforced.View(SimulationRoleWriter, "chapter")
	if writer == nil || containsSimulationString(append(append(writer.Must, writer.Should...), writer.Avoid...), "viewpoint") {
		t.Fatal("Writer view must not own corpus viewpoint")
	}
	for _, id := range append(append(writer.Must, writer.Should...), writer.Avoid...) {
		if id == "surface" {
			t.Fatal("surface phrase feature must not enter a contract view")
		}
	}
}

func TestCompileSimulationContractUsesExplicitPhaseAndBalancesSafety(t *testing.T) {
	confidence, coverage := 0.9, 0.5
	features := make([]SimulationFeature, 0, 12)
	for i := 0; i < 12; i++ {
		safety := "guidance"
		if i >= 6 {
			safety = "avoid"
		}
		features = append(features, SimulationFeature{
			ID: "phase-feature-" + string(rune('a'+i)), Dimension: "dialogue.turns",
			Statement:      "phase-routed abstract technique " + string(rune('a'+i)),
			Classification: "local", Phases: []string{"chapter"},
			SupportCount: 1, Confidence: &confidence, Coverage: &coverage, Safety: safety,
		})
	}
	profile := simulationContractTestProfile(t, "fresh")
	profile.Features = features
	if err := SetSimulationProfileDigest(&profile); err != nil {
		t.Fatal(err)
	}

	contract, err := CompileSimulationContract(SimulationContractInput{
		Profile: &profile, RequestedMode: SimulationModeReinforced, Now: time.Unix(1, 0).UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	writer := contract.View(SimulationRoleWriter, "chapter")
	if writer == nil || len(writer.Should) == 0 {
		t.Fatalf("explicit chapter phase did not route guidance: %+v", writer)
	}
	if len(writer.Avoid) == 0 || len(writer.Avoid) > 3 {
		t.Fatalf("safety guidance was not retained: %+v", writer)
	}
	selectedCount := simulationContractViewCount(*writer)
	if selectedCount != 9 {
		t.Fatalf("writer feature count = %d, want reinforced budget 9", selectedCount)
	}
	if len(writer.Avoid) >= selectedCount {
		t.Fatalf("avoid features crowded out all positive guidance: %+v", writer)
	}
}

func TestSimulationContractStalenessBindings(t *testing.T) {
	profile := simulationContractTestProfile(t, "fresh")
	contract, err := CompileSimulationContract(SimulationContractInput{
		Profile: &profile, RequestedMode: SimulationModeNormal,
		FoundationRevision: 3, FoundationDigest: "foundation-a", CreativeBriefDigest: "brief-a",
		Now: time.Unix(1, 0).UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if ok, reason := SimulationContractCurrent(&contract, &profile, SimulationModeNormal, 3, "foundation-a", "brief-a"); !ok || reason != "" {
		t.Fatalf("current contract rejected: ok=%t reason=%q", ok, reason)
	}
	cases := []struct {
		name       string
		profile    *SimulationProfileV2
		mode       string
		revision   int64
		foundation string
		brief      string
		reason     string
	}{
		{"profile", mutatedSimulationContractProfile(t, profile), SimulationModeNormal, 3, "foundation-a", "brief-a", "profile_digest_changed"},
		{"mode", &profile, SimulationModeReinforced, 3, "foundation-a", "brief-a", "mode_changed"},
		{"foundation", &profile, SimulationModeNormal, 4, "foundation-b", "brief-a", "foundation_revision_changed"},
		{"brief", &profile, SimulationModeNormal, 3, "foundation-a", "brief-b", "creative_brief_changed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if ok, reason := SimulationContractCurrent(&contract, tc.profile, tc.mode, tc.revision, tc.foundation, tc.brief); ok || reason != tc.reason {
				t.Fatalf("ok=%t reason=%q, want false/%q", ok, reason, tc.reason)
			}
		})
	}
}

func TestSimulationContractHealthDegradesOrInactivatesTruthfully(t *testing.T) {
	for _, tc := range []struct {
		health string
		status string
	}{
		{"legacy", SimulationContractDegraded},
		{"portable_only", SimulationContractDegraded},
		{"stale", SimulationContractInactive},
		{"unknown", SimulationContractInactive},
	} {
		t.Run(tc.health, func(t *testing.T) {
			profile := simulationContractTestProfile(t, tc.health)
			contract, err := CompileSimulationContract(SimulationContractInput{
				Profile: &profile, RequestedMode: SimulationModeReinforced, Now: time.Unix(1, 0).UTC(),
			})
			if err != nil {
				t.Fatal(err)
			}
			if contract.Status != tc.status || len(contract.Reasons) == 0 {
				t.Fatalf("status=%q reasons=%v, want %q with reason", contract.Status, contract.Reasons, tc.status)
			}
		})
	}
}

func simulationContractTestProfile(t *testing.T, health string) SimulationProfileV2 {
	t.Helper()
	confidence, coverage := 0.95, 0.75
	features := []SimulationFeature{
		{ID: "plot-1", Dimension: "plot_design.opening_patterns", Statement: "open on an unresolved choice", Classification: "stable", SupportCount: 6, Confidence: &confidence, Coverage: &coverage, Safety: "guidance"},
		{ID: "plot-2", Dimension: "plot_design.escalation_patterns", Statement: "raise cost after each decision", Classification: "stable", SupportCount: 6, Confidence: &confidence, Coverage: &coverage, Safety: "guidance"},
		{ID: "hook-1", Dimension: "hook_design.placement", Statement: "place a question near scene transitions", Classification: "stable", SupportCount: 6, Confidence: &confidence, Coverage: &coverage, Safety: "guidance"},
		{ID: "pace-1", Dimension: "pacing_density.information_release", Statement: "alternate answer and complication", Classification: "stable", SupportCount: 6, Confidence: &confidence, Coverage: &coverage, Safety: "guidance"},
		{ID: "style-1", Dimension: "style.sentence_rhythm", Statement: "vary sentence length by tension", Classification: "stable", SupportCount: 6, Confidence: &confidence, Coverage: &coverage, Safety: "guidance"},
		{ID: "viewpoint", Dimension: "style.perspective", Statement: "follow source protagonist viewpoint", Classification: "stable", SupportCount: 6, Confidence: &confidence, Coverage: &coverage, Safety: "guidance"},
		{ID: "surface", Dimension: "lexicon.signature_phrases", Statement: "source catchphrase", Classification: "stable", SupportCount: 6, Confidence: &confidence, Coverage: &coverage, Safety: "guidance"},
		{ID: "avoid-1", Dimension: "reader_engagement.anti_patterns", Statement: "avoid copied set pieces", Classification: "stable", SupportCount: 6, Confidence: &confidence, Coverage: &coverage, Safety: "avoid"},
	}
	profile := SimulationProfileV2{
		Version:   SimulationPortableProfileVersion,
		CreatedAt: time.Unix(1, 0).UTC().Format(time.RFC3339),
		UpdatedAt: time.Unix(1, 0).UTC().Format(time.RFC3339),
		Corpus:    SimulationPortableCorpus{Digest: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", SourceCount: 8},
		Analysis: SimulationAnalysisMetadata{
			SourceAnalysisSignature: "source", SchemaSignature: "schema", AggregationSignature: "aggregation",
		},
		Features:     features,
		Capabilities: SimulationCapabilities{Portable: true, LocalEvidence: true, AnalysisSigned: true, SafetyIndex: true},
		Health:       SimulationProfileHealth{State: health},
	}
	if health == "legacy" {
		profile.Analysis.Legacy = true
		profile.Capabilities.AnalysisSigned = false
	}
	if health == "portable_only" {
		profile.Capabilities.LocalEvidence = false
		profile.Capabilities.SafetyIndex = false
	}
	if err := SetSimulationProfileDigest(&profile); err != nil {
		t.Fatal(err)
	}
	if err := ValidateSimulationProfileV2(&profile); err != nil {
		t.Fatal(err)
	}
	return profile
}

func mutatedSimulationContractProfile(t *testing.T, profile SimulationProfileV2) *SimulationProfileV2 {
	t.Helper()
	profile.Features[0].Statement += " changed"
	if err := SetSimulationProfileDigest(&profile); err != nil {
		t.Fatal(err)
	}
	return &profile
}

func simulationContractViewCount(view SimulationContractView) int {
	return len(view.Must) + len(view.Should) + len(view.Avoid)
}
