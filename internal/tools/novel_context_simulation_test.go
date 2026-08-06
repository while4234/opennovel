package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/store"
)

func TestRoleBoundSimulationContractDiffersByModeAndRole(t *testing.T) {
	st := simulationContextStore(t)
	saveSimulationContextProfile(t, st, simulationContextFeatures())

	normalArchitect := executeSimulationContext(t, st, ContextToolOptions{
		SimulationMode: domain.SimulationModeNormal, Role: domain.SimulationRoleArchitect,
	}, `{"scope":"planning"}`)
	normalView := simulationView(t, normalArchitect, "planning_memory")
	if len(normalView["must"].([]any)) != 0 {
		t.Fatal("normal simulation features must remain advisory")
	}
	normalCount := int(normalView["selected_count"].(float64))

	reinforcedArchitect := executeSimulationContext(t, st, ContextToolOptions{
		SimulationMode: domain.SimulationModeReinforced, Role: domain.SimulationRoleArchitect,
	}, `{"scope":"planning"}`)
	reinforcedView := simulationView(t, reinforcedArchitect, "planning_memory")
	reinforcedCount := int(reinforcedView["selected_count"].(float64))
	if reinforcedCount <= normalCount {
		t.Fatalf("reinforced selected_count=%d, want more than normal=%d", reinforcedCount, normalCount)
	}
	if len(reinforcedView["must"].([]any)) == 0 {
		t.Fatal("reinforced Architect view should contain objectively eligible must features")
	}

	writer := executeSimulationContext(t, st, ContextToolOptions{
		SimulationMode: domain.SimulationModeReinforced, Role: domain.SimulationRoleWriter,
	}, `{"chapter":1}`)
	writerView := simulationView(t, writer, "working_memory")
	assertSimulationDimensions(t, writerView, "style.", "pacing_density.", "lexicon.transition_words")
	if bytes.Contains(mustSimulationJSON(t, writer), []byte("style.perspective")) {
		t.Fatal("Writer view must not receive corpus viewpoint ownership")
	}

	editor := executeSimulationContext(t, st, ContextToolOptions{
		SimulationMode: domain.SimulationModeReinforced, Role: domain.SimulationRoleEditor,
	}, `{"chapter":1}`)
	editorView := simulationView(t, editor, "working_memory")
	assertSimulationDimensions(t, editorView, "reader_engagement.", "style.")
	if fmt.Sprint(editorView) == fmt.Sprint(writerView) {
		t.Fatal("Editor must not reuse the Writer contract view")
	}
}

func TestSimulationContractStatusAndNoLeakage(t *testing.T) {
	st := simulationContextStore(t)
	features := simulationContextFeatures()
	features = append(features, domain.SimulationFeature{
		ID: "surface", Dimension: "lexicon.signature_phrases", Statement: "source-only catchphrase",
		Classification: "stable", SupportCount: 4, Confidence: floatPointer(0.99),
		Coverage: floatPointer(0.8), Safety: "guidance",
	})
	saveSimulationContextProfile(t, st, features)

	coordinator := executeSimulationContext(t, st, ContextToolOptions{
		SimulationMode: domain.SimulationModeReinforced, Role: domain.SimulationRoleCoordinator,
	}, `{"scope":"status"}`)
	effective := coordinator["simulation_effective"].(map[string]any)
	if int(effective["selected_count"].(float64)) != 0 {
		t.Fatal("Coordinator status must not contain simulation guidance")
	}

	editorRaw := mustSimulationJSON(t, executePlanningReviewPacket(t, st, References{}, "default", ContextToolOptions{
		SimulationMode: domain.SimulationModeReinforced, Role: domain.SimulationRoleEditor,
	}, PlanningReviewSelector{}))
	for _, forbidden := range [][]byte{
		[]byte("source_reports"), []byte("source_dir"), []byte("safety_index"),
		[]byte("source-only catchphrase"), []byte("lexicon.signature_phrases"),
	} {
		if bytes.Contains(editorRaw, forbidden) {
			t.Fatalf("role context leaked forbidden data %q", forbidden)
		}
	}
}

func TestReinforcedUnavailableProfileReportsInactive(t *testing.T) {
	st := simulationContextStore(t)
	payload := executeSimulationContext(t, st, ContextToolOptions{
		SimulationMode: domain.SimulationModeReinforced, Role: domain.SimulationRoleArchitect,
	}, `{"scope":"planning"}`)
	effective := payload["simulation_effective"].(map[string]any)
	if effective["status"] != domain.SimulationContractInactive {
		t.Fatalf("status=%v, want inactive", effective["status"])
	}
	reasons := effective["reasons"].([]any)
	if len(reasons) == 0 || reasons[0] != "profile_missing" {
		t.Fatalf("reasons=%v, want profile_missing", reasons)
	}
	if _, exists := payload["simulation_mode"]; exists {
		t.Fatal("inactive reinforced mode must not publish a compatibility activation marker")
	}
}

func TestAdaptationContextKeepsSimulationContractInactive(t *testing.T) {
	plan := domain.AdaptationPlan{
		Granularity:   domain.AdaptationGranularityArc,
		RewritePolicy: domain.AdaptationRewriteFullRewrite,
		Status:        domain.AdaptationPlanStatusConfirmed,
		Chapters: []domain.AdaptationChapterPlan{{
			Chapter: 1, Title: "target", SourceChapters: []int{1},
			SourceRunes: 100, TargetRunes: 100,
		}},
	}
	st := newAdaptationToolStoreWithPlan(t, plan, []string{"source event"})
	if err := st.Outline.SaveOutline([]domain.OutlineEntry{{Chapter: 1, Title: "target", CoreEvent: "rewrite event"}}); err != nil {
		t.Fatal(err)
	}
	if err := st.Progress.Init("adapt", 1); err != nil {
		t.Fatal(err)
	}
	saveSimulationContextProfile(t, st, simulationContextFeatures())
	payload := executeSimulationContext(t, st, ContextToolOptions{
		SimulationMode: domain.SimulationModeReinforced, Role: domain.SimulationRoleWriter,
	}, `{"chapter":1}`)
	effective := payload["simulation_effective"].(map[string]any)
	if effective["status"] != domain.SimulationContractInactive {
		t.Fatalf("adaptation simulation status=%v, want inactive", effective["status"])
	}
	working := payload["working_memory"].(map[string]any)
	if _, exists := working["simulation_contract"]; exists {
		t.Fatal("adaptation context must not inject simulation obligations")
	}
}

func simulationContextStore(t *testing.T) *store.Store {
	t.Helper()
	st := store.NewStore(testStoreDir(t))
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	if err := st.Outline.SaveOutline([]domain.OutlineEntry{{Chapter: 1, Title: "Start", CoreEvent: "Begin"}}); err != nil {
		t.Fatal(err)
	}
	if err := st.Progress.Init("test", 1); err != nil {
		t.Fatal(err)
	}
	return st
}

func saveSimulationContextProfile(t *testing.T, st *store.Store, features []domain.SimulationFeature) {
	t.Helper()
	profile := domain.SimulationProfileV2{
		Version:   domain.SimulationPortableProfileVersion,
		CreatedAt: time.Date(2026, 7, 28, 0, 0, 0, 0, time.UTC).Format(time.RFC3339),
		UpdatedAt: time.Date(2026, 7, 28, 0, 0, 0, 0, time.UTC).Format(time.RFC3339),
		Corpus:    domain.SimulationPortableCorpus{Digest: string(make([]byte, 64)), SourceCount: 8},
		Analysis: domain.SimulationAnalysisMetadata{
			SourceAnalysisSignature: "source-v2", SchemaSignature: "schema-v2",
			AggregationSignature: "aggregate-v2",
		},
		Features: features,
		Capabilities: domain.SimulationCapabilities{
			Portable: true, LocalEvidence: true, AnalysisSigned: true, SafetyIndex: true,
		},
		Health: domain.SimulationProfileHealth{State: "fresh"},
	}
	// A digest must be lowercase hex, not NUL bytes.
	profile.Corpus.Digest = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if err := domain.SetSimulationProfileDigest(&profile); err != nil {
		t.Fatal(err)
	}
	if err := st.Simulation.SavePortable(profile); err != nil {
		t.Fatal(err)
	}
}

func simulationContextFeatures() []domain.SimulationFeature {
	dimensions := []string{
		"plot_design.opening_patterns", "plot_design.escalation_patterns",
		"hook_design.placement", "hook_design.payoff_rules",
		"reader_engagement.methods", "reader_engagement.progression_rewards",
		"pacing_density.information_release", "pacing_density.scene_density",
		"style.sentence_rhythm", "style.prose_texture",
		"style.perspective", "lexicon.transition_words",
	}
	features := make([]domain.SimulationFeature, 0, len(dimensions))
	for i, dimension := range dimensions {
		features = append(features, domain.SimulationFeature{
			ID: fmt.Sprintf("feature-%02d", i), Dimension: dimension,
			Statement:      fmt.Sprintf("abstract reusable technique %02d", i),
			Classification: "stable", SupportCount: 6, Coverage: floatPointer(0.75),
			Confidence: floatPointer(0.95), Safety: "guidance",
		})
	}
	return features
}

func executeSimulationContext(t *testing.T, st *store.Store, opts ContextToolOptions, args string) map[string]any {
	t.Helper()
	raw, err := NewContextToolWithOptions(st, References{}, "default", opts).Execute(context.Background(), json.RawMessage(args))
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	return payload
}

func simulationView(t *testing.T, payload map[string]any, section string) map[string]any {
	t.Helper()
	sectionValue, ok := payload[section].(map[string]any)
	if !ok {
		t.Fatalf("missing section %s", section)
	}
	view, ok := sectionValue["simulation_contract"].(map[string]any)
	if !ok {
		t.Fatalf("missing role-bound simulation_contract under %s", section)
	}
	for _, key := range []string{"must", "should", "avoid"} {
		if _, exists := view[key]; !exists {
			view[key] = []any{}
		}
	}
	return view
}

func assertSimulationDimensions(t *testing.T, view map[string]any, prefixes ...string) {
	t.Helper()
	encoded := string(mustSimulationJSON(t, view))
	for _, prefix := range prefixes {
		if !bytes.Contains([]byte(encoded), []byte(prefix)) {
			t.Fatalf("view lacks dimension prefix %q: %s", prefix, encoded)
		}
	}
}

func mustSimulationJSON(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func floatPointer(value float64) *float64 { return &value }
