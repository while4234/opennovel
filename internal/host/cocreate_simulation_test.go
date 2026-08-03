package host

import (
	"strings"
	"testing"
	"time"

	"github.com/voocel/ainovel-cli/internal/bootstrap"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/store"
)

func TestCoCreateSimulationNormalRemainsUninjected(t *testing.T) {
	st := newSimulationPromptTestStore(t, true)
	if got := coCreateSystemPromptWithSimulation(st, bootstrap.SimulationModeNormal); got != coCreateSystemPrompt {
		t.Fatal("normal cold co-create prompt changed")
	}
	if err := st.Progress.Init("test", 12); err != nil {
		t.Fatal(err)
	}
	if got, want := stageSystemPromptWithSimulation(st, bootstrap.SimulationModeNormal), stageSystemPrompt(st); got != want {
		t.Fatal("normal stage co-create prompt changed")
	}
}

func TestCoCreateReinforcedUsesSanitizedContractCandidate(t *testing.T) {
	st := newSimulationPromptTestStore(t, true)
	got := coCreateSystemPromptWithSimulation(st, bootstrap.SimulationModeReinforced)
	for _, want := range []string{
		"## 仿写方向（结构化契约候选）",
		`"effective_mode": "reinforced"`,
		`"status": "active"`,
		`"contract_revision": 1`,
		`"id": "plot-opening"`,
		"open with an unresolved choice",
		"`## 仿写方向`",
		"portable v2",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("reinforced prompt missing %q:\n%s", want, got)
		}
	}
	assertNoRawSimulationSourceInfo(t, got)
}

func TestCoCreateReinforcedWithoutProfileReportsInactive(t *testing.T) {
	st := newSimulationPromptTestStore(t, false)
	got := coCreateSystemPromptWithSimulation(st, bootstrap.SimulationModeReinforced)
	for _, want := range []string{`"effective_mode": "reinforced"`, `"status": "inactive"`, `"profile_missing"`} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing truthful unavailable-profile diagnostic %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, `"must"`) || strings.Contains(got, `"should"`) {
		t.Fatal("inactive reinforced co-create must not inject obligations")
	}
}

func TestStageCoCreateReinforcedPreservesStoryStateAndContract(t *testing.T) {
	st := newSimulationPromptTestStore(t, true)
	if err := st.Progress.Init("test-project", 12); err != nil {
		t.Fatal(err)
	}
	got := stageSystemPromptWithSimulation(st, bootstrap.SimulationModeReinforced)
	for _, want := range []string{"test-project", "## 仿写方向（结构化契约候选）", `"status": "active"`} {
		if !strings.Contains(got, want) {
			t.Fatalf("stage prompt missing %q", want)
		}
	}
	assertNoRawSimulationSourceInfo(t, got)
}

func TestAdaptSystemPromptIgnoresSimulationProfile(t *testing.T) {
	st := newSimulationPromptTestStore(t, false)
	before := adaptSystemPrompt(st)
	saveSimulationPromptProfile(t, st)
	if after := adaptSystemPrompt(st); after != before {
		t.Fatal("adapt prompt changed after saving simulation profile")
	}
}

func newSimulationPromptTestStore(t *testing.T, withProfile bool) *store.Store {
	t.Helper()
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	if withProfile {
		saveSimulationPromptProfile(t, st)
	}
	return st
}

func saveSimulationPromptProfile(t *testing.T, st *store.Store) {
	t.Helper()
	confidence, coverage := 0.96, 0.8
	profile := domain.SimulationProfileV2{
		Version:   domain.SimulationPortableProfileVersion,
		CreatedAt: time.Unix(1, 0).UTC().Format(time.RFC3339),
		UpdatedAt: time.Unix(1, 0).UTC().Format(time.RFC3339),
		Corpus: domain.SimulationPortableCorpus{
			Digest: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", SourceCount: 6,
		},
		Analysis: domain.SimulationAnalysisMetadata{
			SourceAnalysisSignature: "source", SchemaSignature: "schema", AggregationSignature: "aggregate",
		},
		Features: []domain.SimulationFeature{
			{ID: "plot-opening", Dimension: "plot_design.opening_patterns", Statement: "open with an unresolved choice", Classification: "stable", SupportCount: 5, Coverage: &coverage, Confidence: &confidence, Safety: "guidance"},
			{ID: "hook-payoff", Dimension: "hook_design.payoff_rules", Statement: "answer one question before raising another", Classification: "stable", SupportCount: 5, Coverage: &coverage, Confidence: &confidence, Safety: "guidance"},
			{ID: "surface", Dimension: "lexicon.signature_phrases", Statement: "RAW_SIGNATURE_PHRASE", Classification: "stable", SupportCount: 5, Coverage: &coverage, Confidence: &confidence, Safety: "guidance"},
		},
		Capabilities: domain.SimulationCapabilities{Portable: true, LocalEvidence: true, AnalysisSigned: true, SafetyIndex: true},
		Health:       domain.SimulationProfileHealth{State: "fresh"},
	}
	if err := domain.SetSimulationProfileDigest(&profile); err != nil {
		t.Fatal(err)
	}
	if err := st.Simulation.SavePortable(profile); err != nil {
		t.Fatal(err)
	}
}

func assertNoRawSimulationSourceInfo(t *testing.T, prompt string) {
	t.Helper()
	for _, blocked := range []string{
		"source_files", "simulate/", "source_reports\":", "source_dir\":",
		"safety_index\":", "RAW_SIGNATURE_PHRASE", "lexicon.signature_phrases",
	} {
		if strings.Contains(prompt, blocked) {
			t.Fatalf("prompt leaked simulation-local or surface data %q", blocked)
		}
	}
}
