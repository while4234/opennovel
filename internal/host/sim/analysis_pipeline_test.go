package sim

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/store"
)

func TestBuildSourceAnalysisCoverageUsesHeadMiddleTailWindows(t *testing.T) {
	content := strings.Repeat("头", 7000) + strings.Repeat("中", 7000) + strings.Repeat("尾", 7000)
	coverage := buildSourceAnalysisCoverage(content)
	if coverage.Strategy != "head_middle_tail" || len(coverage.Windows) != 3 || coverage.Ratio >= 1 {
		t.Fatalf("coverage = %+v", coverage)
	}
	if !strings.Contains(coverage.Windows[1].Content, "中") {
		t.Fatal("middle window does not cover source middle")
	}
	if coverage.UsedRunes != maxSourceRunes {
		t.Fatalf("used runes = %d, want %d", coverage.UsedRunes, maxSourceRunes)
	}
}

func TestSaveSimulationAnalysisStateIncludesCanonicalSynthesisGuidance(t *testing.T) {
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	profile := domain.SimulationProfile{
		Synthesis: domain.SimulationSynthesis{
			PlotDesign: domain.SimulationPlotDesign{
				OpeningPatterns: []string{"begin with an unresolved choice"},
			},
		},
	}
	metadata := domain.SimulationAnalysisMetadata{
		SourceAnalysisSignature: "source",
		SplitterSignature:       "splitter",
		SchemaSignature:         "schema",
		SynthesisSignature:      "synthesis",
		AggregationSignature:    "aggregation",
	}

	if err := saveSimulationAnalysisState(
		st.Simulation,
		profile,
		metadata,
		domain.SimulationProfileHealth{State: "fresh"},
		time.Unix(100, 0),
	); err != nil {
		t.Fatal(err)
	}
	portable, err := st.Simulation.LoadPortable()
	if err != nil {
		t.Fatal(err)
	}
	if portable == nil || len(portable.Features) != 1 ||
		portable.Features[0].Dimension != "plot_design.opening_patterns" {
		t.Fatalf("canonical synthesis guidance missing: %+v", portable)
	}
}

func TestRunnerSeparatesSourceAndSynthesisSignatureInvalidation(t *testing.T) {
	dir := t.TempDir()
	sourceDir := filepath.Join(dir, "simulate")
	writeSimulationSource(t, sourceDir, "a.txt", "synthetic body")
	st := store.NewStore(filepath.Join(dir, "output", "novel"))
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	runWithPrompts := func(sourcePrompt, mergePrompt string, responses ...string) int32 {
		t.Helper()
		llm := &scriptedLLM{responses: responses}
		events, err := Run(context.Background(), Deps{
			Store: st, LLM: llm, ModelIdentity: "provider/model",
			Prompts: Prompts{Source: sourcePrompt, Merge: mergePrompt},
		}, Options{SourceDir: sourceDir})
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		for event := range events {
			if event.Err != nil {
				t.Fatalf("run event: %v", event.Err)
			}
		}
		return llm.calls.Load()
	}
	if calls := runWithPrompts("source-v1", "merge-v1", validSourceReportJSON("first"), validSynthesisJSON("first")); calls != 2 {
		t.Fatalf("initial calls = %d, want 2", calls)
	}
	if calls := runWithPrompts("source-v1", "merge-v2", validSynthesisJSON("merge only")); calls != 1 {
		t.Fatalf("synthesis-only change calls = %d, want 1", calls)
	}
	if calls := runWithPrompts("source-v2", "merge-v2", validSourceReportJSON("reanalyzed"), validSynthesisJSON("reanalyzed")); calls != 2 {
		t.Fatalf("source signature change calls = %d, want 2", calls)
	}
}

func TestRunnerDeletionRemovesDeletedSourceFeature(t *testing.T) {
	dir := t.TempDir()
	sourceDir := filepath.Join(dir, "simulate")
	writeSimulationSource(t, sourceDir, "a.txt", "synthetic a")
	writeSimulationSource(t, sourceDir, "b.txt", "synthetic b")
	st := store.NewStore(filepath.Join(dir, "output", "novel"))
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	first := &scriptedLLM{responses: []string{
		structuredSourceReportJSON("A technique", "pace through short action beats"),
		structuredSourceReportJSON("B technique", "repeat the deleted-source reveal cadence"),
		validSynthesisJSON("initial"),
	}}
	runSimulationForTest(t, st, first, sourceDir, "source", "merge")
	if err := os.Remove(filepath.Join(sourceDir, "b.txt")); err != nil {
		t.Fatal(err)
	}
	second := &scriptedLLM{responses: []string{validSynthesisJSON("after deletion")}}
	runSimulationForTest(t, st, second, sourceDir, "source", "merge")
	if calls := second.calls.Load(); calls != 1 {
		t.Fatalf("deletion calls = %d, want merge only", calls)
	}
	portable, err := st.Simulation.LoadPortable()
	if err != nil {
		t.Fatal(err)
	}
	if portable.Corpus.SourceCount != 1 || portable.Health.State != "fresh" {
		t.Fatalf("portable after deletion = %+v", portable)
	}
	for _, feature := range portable.Features {
		if strings.Contains(feature.Statement, "deleted-source") {
			t.Fatalf("deleted source feature survived: %+v", feature)
		}
	}
}

func TestRunnerEmptyCorpusClearsProfileAndEvidence(t *testing.T) {
	dir := t.TempDir()
	sourceDir := filepath.Join(dir, "simulate")
	writeSimulationSource(t, sourceDir, "a.txt", "synthetic")
	st := store.NewStore(filepath.Join(dir, "output", "novel"))
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	runSimulationForTest(t, st, &scriptedLLM{responses: []string{
		validSourceReportJSON("first"), validSynthesisJSON("first"),
	}}, sourceDir, "source", "merge")
	if err := os.Remove(filepath.Join(sourceDir, "a.txt")); err != nil {
		t.Fatal(err)
	}
	runSimulationForTest(t, st, &scriptedLLM{}, sourceDir, "source", "merge")
	portable, err := st.Simulation.LoadPortable()
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := st.Simulation.LoadLocalEvidence()
	if err != nil {
		t.Fatal(err)
	}
	if portable != nil || evidence != nil {
		t.Fatalf("empty corpus retained profile=%+v evidence=%+v", portable, evidence)
	}
}

func TestMergeCheckpointBindsCanonicalReportSetAndSynthesisSignature(t *testing.T) {
	reports := []domain.SimulationSourceReport{
		{RelativePath: "b.txt", SHA256: "sha-b", Fingerprint: domain.SimulationSourceFingerprint("b.txt", "sha-b"), Summary: "b"},
		{RelativePath: "a.txt", SHA256: "sha-a", Fingerprint: domain.SimulationSourceFingerprint("a.txt", "sha-a"), Summary: "a"},
	}
	checkpoint := buildSimulationMergeCheckpointWithSignature(reports, 1024, "signature-a", mergeSynthesisCheckpoint{
		ProcessedReportCount: 1,
		TotalReportCount:     2,
		ProcessedBatchCount:  1,
		Synthesis:            synthesisFixture("checkpoint"),
	}, time.Unix(100, 0))
	if checkpoint == nil {
		t.Fatal("checkpoint is nil")
	}
	reordered := []domain.SimulationSourceReport{reports[1], reports[0]}
	if _, ok := validMergeCheckpointWithSignature(checkpoint, reordered, "signature-a"); !ok {
		t.Fatal("canonical report reorder invalidated checkpoint")
	}
	if _, ok := validMergeCheckpointWithSignature(checkpoint, reordered, "signature-b"); ok {
		t.Fatal("changed synthesis signature reused checkpoint")
	}
	reordered[0].SHA256 = "changed"
	reordered[0].Fingerprint = domain.SimulationSourceFingerprint(reordered[0].RelativePath, reordered[0].SHA256)
	if _, ok := validMergeCheckpointWithSignature(checkpoint, reordered, "signature-a"); ok {
		t.Fatal("changed report identity reused checkpoint")
	}
}

func TestRunnerEventsAndErrorsDoNotExposeSourcePathOrRawText(t *testing.T) {
	defer stubStructuredJSONRetrySleep(t)()
	dir := t.TempDir()
	sourceDir := filepath.Join(dir, "simulate")
	const raw = "private raw sentence that must stay out of events"
	writeSimulationSource(t, sourceDir, "private-name.txt", raw)
	st := store.NewStore(filepath.Join(dir, "output", "novel"))
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	llm := &scriptedLLM{
		responses: []string{`{"summary":"invalid","content_type":"metadata","candidates":[]}`},
	}
	events, err := Run(context.Background(), Deps{
		Store: st, LLM: llm, ModelCallMaxAttempts: 1, StructureRepairMaxAttempts: 1,
		ModelIdentity: "provider/model", Prompts: Prompts{Source: "source", Merge: "merge"},
	}, Options{SourceDir: sourceDir})
	if err != nil {
		t.Fatal(err)
	}
	var visible strings.Builder
	for event := range events {
		visible.WriteString(event.Message)
		if event.Err != nil {
			visible.WriteString(event.Err.Error())
		}
	}
	for _, forbidden := range []string{"private-name.txt", filepath.ToSlash(sourceDir), raw} {
		if strings.Contains(visible.String(), forbidden) {
			t.Fatalf("event stream leaked %q: %s", forbidden, visible.String())
		}
	}
}

func runSimulationForTest(t *testing.T, st *store.Store, llm *scriptedLLM, sourceDir, sourcePrompt, mergePrompt string) {
	t.Helper()
	events, err := Run(context.Background(), Deps{
		Store: st, LLM: llm, ModelIdentity: "provider/model",
		Prompts: Prompts{Source: sourcePrompt, Merge: mergePrompt},
	}, Options{SourceDir: sourceDir})
	if err != nil {
		t.Fatal(err)
	}
	for event := range events {
		if event.Err != nil {
			t.Fatalf("simulation event: %v", event.Err)
		}
	}
}

func structuredSourceReportJSON(summary, statement string) string {
	return `{
  "summary": "` + summary + `",
  "content_type": "body",
  "candidates": [{
    "dimension": "style.sentence_rhythm",
    "statement": "` + statement + `",
    "scope": "global",
    "confidence": 0.9,
    "tendency": "stable",
    "safety": "guidance"
  }]
}`
}
