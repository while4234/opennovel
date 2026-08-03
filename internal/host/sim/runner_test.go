package sim

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/store"
)

type scriptedLLM struct {
	responses []string
	errors    []error
	got       [][]agentcore.Message
	calls     atomic.Int32
}

func (s *scriptedLLM) Generate(_ context.Context, messages []agentcore.Message, _ []agentcore.ToolSpec, _ ...agentcore.CallOption) (*agentcore.LLMResponse, error) {
	idx := int(s.calls.Add(1)) - 1
	s.got = append(s.got, append([]agentcore.Message(nil), messages...))
	if idx < len(s.errors) && s.errors[idx] != nil {
		return nil, s.errors[idx]
	}
	if idx >= len(s.responses) {
		return nil, fmt.Errorf("scriptedLLM exhausted at call %d", idx+1)
	}
	return &agentcore.LLMResponse{
		Message: agentcore.Message{
			Role:      agentcore.RoleAssistant,
			Content:   []agentcore.ContentBlock{agentcore.TextBlock(s.responses[idx])},
			Timestamp: time.Now(),
		},
	}, nil
}

func TestRunnerGeneratesProfileThenSkipsUnchanged(t *testing.T) {
	dir := t.TempDir()
	sourceDir := filepath.Join(dir, "simulate")
	if err := os.MkdirAll(sourceDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sourceDir, "a.txt"), []byte("A tense opening hook.\nA quick reveal.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(sourceDir, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sourceDir, "nested", "b.md"), []byte("# B\n\nSecond sample with cliffhanger."), 0o644); err != nil {
		t.Fatal(err)
	}

	st := store.NewStore(filepath.Join(dir, "output", "novel"))
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	llm := &scriptedLLM{responses: []string{
		validSourceReportJSON("a tone"),
		validSourceReportJSON("b tone"),
		validSynthesisJSON("tight pacing"),
	}}

	events, err := Run(context.Background(), Deps{
		Store:   st,
		LLM:     llm,
		Prompts: Prompts{Source: "source prompt", Merge: "merge prompt"},
	}, Options{SourceDir: sourceDir})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	var last Event
	for ev := range events {
		if ev.Err != nil {
			t.Fatalf("simulate errored: %v", ev.Err)
		}
		last = ev
	}
	if last.Stage != StageDone {
		t.Fatalf("last stage = %s, want %s", last.Stage, StageDone)
	}
	if got := llm.calls.Load(); got != 3 {
		t.Fatalf("first run LLM calls = %d, want 3", got)
	}
	profile, err := st.Simulation.Load()
	if err != nil {
		t.Fatalf("Load profile: %v", err)
	}
	if profile == nil || len(profile.Corpus.Sources) != 2 || len(profile.SourceReports) != 2 {
		t.Fatalf("profile not persisted with two sources: %+v", profile)
	}

	llm2 := &scriptedLLM{}
	events, err = Run(context.Background(), Deps{
		Store:   st,
		LLM:     llm2,
		Prompts: Prompts{Source: "source prompt", Merge: "merge prompt"},
	}, Options{SourceDir: sourceDir})
	if err != nil {
		t.Fatalf("rerun Run: %v", err)
	}
	var upToDate bool
	for ev := range events {
		if ev.Err != nil {
			t.Fatalf("rerun errored: %v", ev.Err)
		}
		if strings.Contains(ev.Message, "画像已是最新") {
			upToDate = true
		}
	}
	if !upToDate {
		t.Fatal("expected up-to-date message")
	}
	if got := llm2.calls.Load(); got != 0 {
		t.Fatalf("unchanged rerun LLM calls = %d, want 0", got)
	}
}

func TestRunnerExplicitRefreshActionsKeepAnalysisAndSynthesisDistinct(t *testing.T) {
	dir := t.TempDir()
	sourceDir := filepath.Join(dir, "simulate")
	writeSimulationSource(t, sourceDir, "a.txt", "synthetic source")
	st := store.NewStore(filepath.Join(dir, "output", "novel"))
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	drainRun(t, st, &scriptedLLM{responses: []string{
		validSourceReportJSON("initial"), validSynthesisJSON("initial"),
	}}, sourceDir)

	resynthesis := &scriptedLLM{responses: []string{validSynthesisJSON("resynthesized")}}
	drainRunWithAction(t, st, resynthesis, sourceDir, ActionResynthesize)
	if calls := resynthesis.calls.Load(); calls != 1 {
		t.Fatalf("resynthesis calls = %d, want merge-only call", calls)
	}

	reanalysis := &scriptedLLM{responses: []string{
		validSourceReportJSON("reanalyzed"), validSynthesisJSON("reanalyzed"),
	}}
	drainRunWithAction(t, st, reanalysis, sourceDir, ActionReanalyze)
	if calls := reanalysis.calls.Load(); calls != 2 {
		t.Fatalf("reanalysis calls = %d, want source plus merge calls", calls)
	}
}

func TestRunnerResynthesisReusesSignedReportsAcrossAnalyzerChanges(t *testing.T) {
	dir := t.TempDir()
	sourceDir := filepath.Join(dir, "simulate")
	writeSimulationSource(t, sourceDir, "a.txt", "synthetic source")
	st := store.NewStore(filepath.Join(dir, "output", "novel"))
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	initial := &scriptedLLM{responses: []string{
		validSourceReportJSON("initial"), validSynthesisJSON("initial"),
	}}
	events, err := Run(context.Background(), Deps{
		Store: st, LLM: initial, ModelIdentity: "provider/model-a",
		Prompts: Prompts{Source: "source prompt v1", Merge: "merge prompt"},
	}, Options{SourceDir: sourceDir})
	if err != nil {
		t.Fatal(err)
	}
	for event := range events {
		if event.Err != nil {
			t.Fatal(event.Err)
		}
	}
	before, err := st.Simulation.LoadPortable()
	if err != nil {
		t.Fatal(err)
	}

	resynthesis := &scriptedLLM{responses: []string{validSynthesisJSON("resynthesized")}}
	events, err = Run(context.Background(), Deps{
		Store: st, LLM: resynthesis, ModelIdentity: "provider/model-b",
		Prompts: Prompts{Source: "source prompt v2", Merge: "merge prompt"},
	}, Options{SourceDir: sourceDir, Action: ActionResynthesize})
	if err != nil {
		t.Fatal(err)
	}
	for event := range events {
		if event.Err != nil {
			t.Fatal(event.Err)
		}
	}
	if calls := resynthesis.calls.Load(); calls != 1 {
		t.Fatalf("resynthesis calls = %d, want merge-only call", calls)
	}
	after, err := st.Simulation.LoadPortable()
	if err != nil {
		t.Fatal(err)
	}
	if after.Analysis.SourceAnalysisSignature != before.Analysis.SourceAnalysisSignature ||
		after.Analysis.ModelIdentity != before.Analysis.ModelIdentity {
		t.Fatalf("resynthesis rewrote source evidence identity: before=%+v after=%+v", before.Analysis, after.Analysis)
	}
}

func TestRunnerIncrementallyAnalyzesNewAndChangedSources(t *testing.T) {
	dir := t.TempDir()
	sourceDir := filepath.Join(dir, "simulate")
	if err := os.MkdirAll(sourceDir, 0o755); err != nil {
		t.Fatal(err)
	}
	aPath := filepath.Join(sourceDir, "a.txt")
	if err := os.WriteFile(aPath, []byte("first version"), 0o644); err != nil {
		t.Fatal(err)
	}

	st := store.NewStore(filepath.Join(dir, "output", "novel"))
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	firstLLM := &scriptedLLM{responses: []string{
		validSourceReportJSON("first tone"),
		validSynthesisJSON("first synthesis"),
	}}
	drainRun(t, st, firstLLM, sourceDir)
	if got := firstLLM.calls.Load(); got != 2 {
		t.Fatalf("first run calls = %d, want 2", got)
	}

	if err := os.WriteFile(filepath.Join(sourceDir, "c.markdown"), []byte("new material"), 0o644); err != nil {
		t.Fatal(err)
	}
	addLLM := &scriptedLLM{responses: []string{
		validSourceReportJSON("new tone"),
		validSynthesisJSON("expanded synthesis"),
	}}
	drainRun(t, st, addLLM, sourceDir)
	if got := addLLM.calls.Load(); got != 2 {
		t.Fatalf("new source run calls = %d, want 2", got)
	}
	profile, _ := st.Simulation.Load()
	if len(profile.Corpus.Sources) != 2 {
		t.Fatalf("after adding source, source count = %d, want 2", len(profile.Corpus.Sources))
	}

	oldHash := sourceHashForPath(t, profile, "a.txt")
	if err := os.WriteFile(aPath, []byte("changed version"), 0o644); err != nil {
		t.Fatal(err)
	}
	changeLLM := &scriptedLLM{responses: []string{
		validSourceReportJSON("changed tone"),
		validSynthesisJSON("changed synthesis"),
	}}
	drainRun(t, st, changeLLM, sourceDir)
	if got := changeLLM.calls.Load(); got != 2 {
		t.Fatalf("changed source run calls = %d, want 2", got)
	}
	profile, _ = st.Simulation.Load()
	if len(profile.Corpus.Sources) != 2 {
		t.Fatalf("changed source should replace same path, got %d sources", len(profile.Corpus.Sources))
	}
	if newHash := sourceHashForPath(t, profile, "a.txt"); newHash == oldHash {
		t.Fatal("expected changed source hash to update")
	}
}

func TestRunnerPersistsAnalyzedSourcesBeforeFailureAndResumes(t *testing.T) {
	dir := t.TempDir()
	sourceDir := filepath.Join(dir, "simulate")
	writeSimulationSource(t, sourceDir, "a.txt", "first")
	writeSimulationSource(t, sourceDir, "b.txt", "second")
	writeSimulationSource(t, sourceDir, "c.txt", "third")

	st := store.NewStore(filepath.Join(dir, "output", "novel"))
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	firstLLM := &scriptedLLM{
		responses: []string{
			validSourceReportJSON("a tone"),
			validSourceReportJSON("b tone"),
		},
		errors: []error{nil, nil, fmt.Errorf("out of credits")},
	}
	events := collectRun(t, st, firstLLM, sourceDir)
	if err := lastRunError(events); err == nil || !strings.Contains(err.Error(), "out of credits") {
		t.Fatalf("first run error = %v, want out of credits", err)
	}
	if got := firstLLM.calls.Load(); got != 3 {
		t.Fatalf("first run calls = %d, want 3", got)
	}
	profile, err := st.Simulation.Load()
	if err != nil {
		t.Fatalf("Load profile: %v", err)
	}
	if profile == nil || len(profile.Corpus.Sources) != 2 || len(profile.SourceReports) != 2 {
		t.Fatalf("partial profile should persist two successful sources: %+v", profile)
	}
	portable, err := st.Simulation.LoadPortable()
	if err != nil || portable == nil || portable.Health.State != "stale" {
		t.Fatalf("partial profile health = %+v, err = %v, want stale", portable, err)
	}

	resumeLLM := &scriptedLLM{responses: []string{
		validSourceReportJSON("c tone"),
		validSynthesisJSON("resumed synthesis"),
	}}
	drainRun(t, st, resumeLLM, sourceDir)
	if got := resumeLLM.calls.Load(); got != 2 {
		t.Fatalf("resume calls = %d, want only remaining source plus synthesis", got)
	}
	profile, err = st.Simulation.Load()
	if err != nil {
		t.Fatalf("Load resumed profile: %v", err)
	}
	if len(profile.Corpus.Sources) != 3 || len(profile.SourceReports) != 3 {
		t.Fatalf("resumed profile should contain three sources: %+v", profile)
	}
	if synthesisIsEmpty(profile.Synthesis) {
		t.Fatal("resumed profile should have synthesis")
	}
}

func TestRunnerResumesMergeWithoutReanalyzingCompletedSources(t *testing.T) {
	dir := t.TempDir()
	sourceDir := filepath.Join(dir, "simulate")
	writeSimulationSource(t, sourceDir, "a.txt", "first")
	writeSimulationSource(t, sourceDir, "b.txt", "second")

	st := store.NewStore(filepath.Join(dir, "output", "novel"))
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	firstLLM := &scriptedLLM{
		responses: []string{
			validSourceReportJSON("a tone"),
			validSourceReportJSON("b tone"),
		},
		errors: []error{nil, nil, fmt.Errorf("merge credits exhausted")},
	}
	events := collectRun(t, st, firstLLM, sourceDir)
	if err := lastRunError(events); err == nil || !strings.Contains(err.Error(), "merge credits exhausted") {
		t.Fatalf("first run error = %v, want merge credits exhausted", err)
	}
	profile, err := st.Simulation.Load()
	if err != nil {
		t.Fatalf("Load profile: %v", err)
	}
	portable, portableErr := st.Simulation.LoadPortable()
	if profile == nil || len(profile.SourceReports) != 2 || portableErr != nil || portable.Health.State != "stale" {
		t.Fatalf("merge-failed profile should keep stale evidence: profile=%+v portable=%+v err=%v", profile, portable, portableErr)
	}

	resumeLLM := &scriptedLLM{responses: []string{validSynthesisJSON("merge only")}}
	drainRun(t, st, resumeLLM, sourceDir)
	if got := resumeLLM.calls.Load(); got != 1 {
		t.Fatalf("resume calls = %d, want only synthesis", got)
	}
}

func TestMergeSynthesisResumesFromSavedBatchCheckpoint(t *testing.T) {
	reports := make([]domain.SimulationSourceReport, 9)
	for i := range reports {
		reports[i] = verboseSourceReport(i + 1)
	}
	limit := len(buildMergeUserPrompt(nil, reports[:2]))
	firstLLM := &scriptedLLM{
		responses: []string{validSynthesisJSON("first checkpoint")},
		errors:    []error{nil, fmt.Errorf("interrupted after checkpoint")},
	}
	var saved *domain.SimulationMergeCheckpoint

	_, err := mergeSynthesisBatchedWithLimit(context.Background(), firstLLM, "merge prompt", nil, reports, limit, mergeSynthesisOptions{
		OnCheckpoint: func(checkpoint mergeSynthesisCheckpoint) error {
			saved = buildSimulationMergeCheckpoint(reports, limit, checkpoint, time.Now())
			return nil
		},
	})
	if err == nil || !strings.Contains(err.Error(), "interrupted after checkpoint") {
		t.Fatalf("first merge error = %v, want interrupted after checkpoint", err)
	}
	if saved == nil || saved.ProcessedReportCount == 0 || synthesisIsEmpty(saved.RollingSynthesis) {
		t.Fatalf("expected checkpoint after first successful batch, got %+v", saved)
	}

	existing := &domain.SimulationProfile{
		Version:       domain.SimulationProfileVersion,
		SourceReports: reports,
	}
	resumeLLM := &scriptedLLM{responses: []string{
		validSynthesisJSON("resume checkpoint 1"),
		validSynthesisJSON("resume checkpoint 2"),
		validSynthesisJSON("resume checkpoint 3"),
		validSynthesisJSON("resume checkpoint 4"),
		validSynthesisJSON("resume checkpoint 5"),
		validSynthesisJSON("resume checkpoint 6"),
		validSynthesisJSON("resume checkpoint 7"),
		validSynthesisJSON("resume checkpoint 8"),
	}}
	var progress []mergeSynthesisProgress
	synthesis, err := mergeSynthesisBatchedWithLimit(context.Background(), resumeLLM, "merge prompt", existing, reports, limit, mergeSynthesisOptions{
		Checkpoint: saved,
		OnBatch: func(ev mergeSynthesisProgress) {
			progress = append(progress, ev)
		},
	})
	if err != nil {
		t.Fatalf("resume merge: %v", err)
	}
	if synthesisIsEmpty(*synthesis) {
		t.Fatal("expected resumed merge to produce synthesis")
	}
	if len(progress) == 0 {
		t.Fatal("expected resume progress events")
	}
	if progress[0].Current <= saved.ProcessedReportCount {
		t.Fatalf("resume progress current = %d, want > saved processed %d", progress[0].Current, saved.ProcessedReportCount)
	}
	firstResumePrompt := resumeLLM.got[0][1].TextContent()
	if strings.Contains(firstResumePrompt, reports[0].RelativePath) {
		t.Fatalf("resume prompt included already checkpointed report %q", reports[0].RelativePath)
	}
}

func TestRunnerFinalizesCompletedMergeCheckpointWithoutLLM(t *testing.T) {
	dir := t.TempDir()
	sourceDir := filepath.Join(dir, "simulate")
	writeSimulationSource(t, sourceDir, "a.txt", "first")
	writeSimulationSource(t, sourceDir, "b.txt", "second")
	scanned, err := scanSources(sourceDir)
	if err != nil {
		t.Fatalf("scanSources: %v", err)
	}

	st := store.NewStore(filepath.Join(dir, "output", "novel"))
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	reports := reportsForScannedSources(scanned)
	deps := Deps{Prompts: Prompts{Source: "source prompt", Merge: "merge prompt"}}
	signatures := buildSimulationAnalysisSignatures(deps)
	for i := range reports {
		coverage := 1.0
		reports[i].AnalysisSignature = signatures.metadata.SourceAnalysisSignature
		reports[i].ContentType = "body"
		reports[i].Coverage = &coverage
		reports[i].Health = "complete"
		reports[i].Candidates = []domain.SimulationTechniqueCandidate{{
			Dimension: "style.sentence_rhythm", Statement: "alternate sentence lengths",
			Scope: "global", Confidence: 0.8, Tendency: "stable", Safety: "guidance",
		}}
	}
	synthesis := synthesisFixture("complete checkpoint")
	profile := domain.SimulationProfile{
		Version:       domain.SimulationProfileVersion,
		CreatedAt:     time.Now().Format(time.RFC3339),
		UpdatedAt:     time.Now().Format(time.RFC3339),
		Corpus:        domain.SimulationCorpusManifest{SourceDir: filepath.ToSlash(sourceDir), Sources: sourcesForScannedSources(scanned)},
		SourceReports: reports,
	}
	if err := saveSimulationAnalysisState(st.Simulation, profile, signatures.metadata, domain.SimulationProfileHealth{State: "stale", Reasons: []string{"synthesis_pending"}}, time.Now()); err != nil {
		t.Fatalf("save profile: %v", err)
	}
	checkpoint := buildSimulationMergeCheckpointWithSignature(reports, maxMergePromptBytes, signatures.metadata.SynthesisSignature, mergeSynthesisCheckpoint{
		ProcessedReportCount: len(reports),
		TotalReportCount:     len(reports),
		ProcessedBatchCount:  1,
		Synthesis:            synthesis,
	}, time.Now())
	if checkpoint == nil {
		t.Fatal("expected checkpoint")
	}
	if err := st.Simulation.SaveMergeCheckpoint(*checkpoint); err != nil {
		t.Fatalf("save checkpoint: %v", err)
	}

	llm := &scriptedLLM{}
	events, err := Run(context.Background(), Deps{
		Store: st, LLM: llm, Prompts: deps.Prompts,
	}, Options{SourceDir: sourceDir})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	for event := range events {
		if event.Err != nil {
			t.Fatalf("simulate errored: %v", event.Err)
		}
	}
	if got := llm.calls.Load(); got != 0 {
		t.Fatalf("LLM calls = %d, want 0 because checkpoint already completed", got)
	}
	finalProfile, err := st.Simulation.Load()
	if err != nil {
		t.Fatalf("load final profile: %v", err)
	}
	finalCheckpoint, err := st.Simulation.LoadMergeCheckpoint()
	if err != nil {
		t.Fatalf("load final checkpoint: %v", err)
	}
	if finalCheckpoint != nil {
		t.Fatalf("final store should clear merge checkpoint: %+v", finalCheckpoint)
	}
	if synthesisIsEmpty(finalProfile.Synthesis) {
		t.Fatal("final profile should use checkpoint synthesis")
	}
}

func TestRunnerPersistsMergeCheckpointBeforeBatchFailure(t *testing.T) {
	dir := t.TempDir()
	sourceDir := filepath.Join(dir, "simulate")
	for i := 0; i < maxMergeReportsPerBatch+1; i++ {
		writeSimulationSource(t, sourceDir, fmt.Sprintf("part_%03d.txt", i+1), strings.Repeat("text ", 20))
	}
	scanned, err := scanSources(sourceDir)
	if err != nil {
		t.Fatalf("scanSources: %v", err)
	}

	st := store.NewStore(filepath.Join(dir, "output", "novel"))
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	profile := domain.SimulationProfile{
		Version:       domain.SimulationProfileVersion,
		CreatedAt:     time.Now().Format(time.RFC3339),
		UpdatedAt:     time.Now().Format(time.RFC3339),
		Corpus:        domain.SimulationCorpusManifest{SourceDir: filepath.ToSlash(sourceDir), Sources: sourcesForScannedSources(scanned)},
		SourceReports: reportsForScannedSources(scanned),
	}
	if err := st.Simulation.Save(profile); err != nil {
		t.Fatalf("save profile: %v", err)
	}

	llm := &scriptedLLM{
		responses: []string{validSynthesisJSON("first persisted batch")},
		errors:    []error{nil, fmt.Errorf("merge interrupted")},
	}
	events := collectRun(t, st, llm, sourceDir)
	if err := lastRunError(events); err == nil || !strings.Contains(err.Error(), "merge interrupted") {
		t.Fatalf("run error = %v, want merge interrupted", err)
	}
	checkpoint, err := st.Simulation.LoadMergeCheckpoint()
	if err != nil {
		t.Fatalf("LoadMergeCheckpoint: %v", err)
	}
	if checkpoint == nil {
		t.Fatal("expected persisted merge checkpoint")
	}
	if checkpoint.ProcessedReportCount != maxMergeReportsPerBatch {
		t.Fatalf("processed reports = %d, want %d", checkpoint.ProcessedReportCount, maxMergeReportsPerBatch)
	}
	if synthesisIsEmpty(checkpoint.RollingSynthesis) {
		t.Fatal("checkpoint should keep rolling synthesis")
	}
}

func TestMergeSynthesisBatchesOversizedReportSet(t *testing.T) {
	reports := make([]domain.SimulationSourceReport, 9)
	for i := range reports {
		reports[i] = verboseSourceReport(i + 1)
	}
	limit := len(buildMergeUserPrompt(nil, reports[:2]))
	responses := make([]string, len(reports))
	for i := range responses {
		responses[i] = validSynthesisJSON(fmt.Sprintf("batched synthesis %d", i+1))
	}
	llm := &scriptedLLM{responses: responses}
	var progress []mergeSynthesisProgress

	synthesis, err := mergeSynthesisBatchedWithLimit(context.Background(), llm, "merge prompt", nil, reports, limit, mergeSynthesisOptions{
		OnBatch: func(ev mergeSynthesisProgress) {
			progress = append(progress, ev)
		},
	})
	if err != nil {
		t.Fatalf("mergeSynthesisBatchedWithLimit: %v", err)
	}
	if synthesisIsEmpty(*synthesis) {
		t.Fatal("expected non-empty synthesis")
	}
	if got := llm.calls.Load(); got <= 1 {
		t.Fatalf("LLM calls = %d, want batched merge calls", got)
	}
	if len(progress) != int(llm.calls.Load()) {
		t.Fatalf("progress events = %d, want %d", len(progress), llm.calls.Load())
	}
	for i, messages := range llm.got {
		if len(messages) < 2 {
			t.Fatalf("call %d messages = %+v, want system and user messages", i+1, messages)
		}
		if strings.Count(messages[1].TextContent(), `"relative_path"`) >= len(reports) {
			t.Fatalf("call %d received all reports instead of a batch", i+1)
		}
	}
}

func TestBuildMergeUserPromptCompactsReportsWithoutMutating(t *testing.T) {
	longSummary := strings.Repeat("x", maxMergeReportSummaryRunes+100)
	longItems := longReportItems("style-item", maxMergeReportItemsPerList+5, maxMergeReportItemRunes+80)
	report := domain.SimulationSourceReport{
		RelativePath:      "oversized.txt",
		SHA256:            "sha-oversized",
		Fingerprint:       domain.SimulationSourceFingerprint("oversized.txt", "sha-oversized"),
		Summary:           longSummary,
		StyleObservations: longItems,
	}

	prompt := buildMergeUserPrompt(nil, []domain.SimulationSourceReport{report})
	if strings.Contains(prompt, longSummary) {
		t.Fatal("merge prompt should not include an oversized report summary verbatim")
	}
	if got := strings.Count(prompt, "style-item"); got != maxMergeReportItemsPerList {
		t.Fatalf("style item count in prompt = %d, want %d", got, maxMergeReportItemsPerList)
	}
	if report.Summary != longSummary {
		t.Fatal("buildMergeUserPrompt mutated the source report summary")
	}
	if len(report.StyleObservations) != len(longItems) || report.StyleObservations[0] != longItems[0] {
		t.Fatal("buildMergeUserPrompt mutated source report list items")
	}
}

func TestAnalyzeSourceRetriesMalformedJSON(t *testing.T) {
	defer stubStructuredJSONRetrySleep(t)()
	source := scannedSource{
		SimulationSource: domain.SimulationSource{
			RelativePath: "a.txt",
			SHA256:       "abc",
			Fingerprint:  domain.SimulationSourceFingerprint("a.txt", "abc"),
		},
		content: "body",
	}
	llm := &scriptedLLM{responses: []string{
		"not json",
		validSourceReportJSON("fixed tone"),
	}}

	report, err := AnalyzeSource(context.Background(), llm, "source prompt", source)
	if err != nil {
		t.Fatalf("AnalyzeSource: %v", err)
	}
	if report.Summary != "fixed tone" {
		t.Fatalf("summary = %q, want fixed tone", report.Summary)
	}
	if got := llm.calls.Load(); got != 2 {
		t.Fatalf("calls = %d, want 2", got)
	}
	if len(llm.got) != 2 || len(llm.got[1]) != 3 || !strings.Contains(llm.got[1][2].TextContent(), "could not be parsed") {
		t.Fatalf("retry prompt not appended: %+v", llm.got)
	}
}

func drainRun(t *testing.T, st *store.Store, llm *scriptedLLM, sourceDir string) {
	t.Helper()
	for _, ev := range collectRun(t, st, llm, sourceDir) {
		if ev.Err != nil {
			t.Fatalf("simulate errored: %v", ev.Err)
		}
	}
}

func drainRunWithAction(t *testing.T, st *store.Store, llm *scriptedLLM, sourceDir, action string) {
	t.Helper()
	events, err := Run(context.Background(), Deps{
		Store: st, LLM: llm,
		Prompts: Prompts{Source: "source prompt", Merge: "merge prompt"},
	}, Options{SourceDir: sourceDir, Action: action})
	if err != nil {
		t.Fatalf("Run action %s: %v", action, err)
	}
	for event := range events {
		if event.Err != nil {
			t.Fatalf("simulate action %s errored: %v", action, event.Err)
		}
	}
}

func collectRun(t *testing.T, st *store.Store, llm *scriptedLLM, sourceDir string) []Event {
	t.Helper()
	events, err := Run(context.Background(), Deps{
		Store:   st,
		LLM:     llm,
		Prompts: Prompts{Source: "source prompt", Merge: "merge prompt"},
	}, Options{SourceDir: sourceDir})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	var collected []Event
	for ev := range events {
		collected = append(collected, ev)
	}
	return collected
}

func lastRunError(events []Event) error {
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Err != nil {
			return events[i].Err
		}
	}
	return nil
}

func writeSimulationSource(t *testing.T, sourceDir, name, content string) {
	t.Helper()
	if err := os.MkdirAll(sourceDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sourceDir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func stubStructuredJSONRetrySleep(t *testing.T) func() {
	t.Helper()
	original := structuredJSONRetrySleep
	structuredJSONRetrySleep = func(context.Context, time.Duration) error { return nil }
	return func() { structuredJSONRetrySleep = original }
}

func sourceHashForPath(t *testing.T, profile *domain.SimulationProfile, rel string) string {
	t.Helper()
	for _, source := range profile.Corpus.Sources {
		if source.RelativePath == rel {
			return source.SHA256
		}
	}
	t.Fatalf("source %q not found", rel)
	return ""
}

func reportsForScannedSources(scanned []scannedSource) []domain.SimulationSourceReport {
	reports := make([]domain.SimulationSourceReport, 0, len(scanned))
	for _, source := range scanned {
		reports = append(reports, domain.SimulationSourceReport{
			RelativePath: source.RelativePath,
			SHA256:       source.SHA256,
			Fingerprint:  source.Fingerprint,
			Summary:      "already analyzed " + source.RelativePath,
		})
	}
	return reports
}

func TestPruneProfileToScannedSourcesDropsDeletedSplitParts(t *testing.T) {
	current := scannedSource{
		SimulationSource: domain.SimulationSource{
			RelativePath: "novel.part_001_ch0001-0002.txt",
			SHA256:       "new-sha",
			Fingerprint:  domain.SimulationSourceFingerprint("novel.part_001_ch0001-0002.txt", "new-sha"),
		},
	}
	stale := domain.SimulationSource{
		RelativePath: "novel.part_001_ch0001-0005.txt",
		SHA256:       "old-sha",
		Fingerprint:  domain.SimulationSourceFingerprint("novel.part_001_ch0001-0005.txt", "old-sha"),
	}
	profile := &domain.SimulationProfile{
		Corpus: domain.SimulationCorpusManifest{Sources: []domain.SimulationSource{stale, current.SimulationSource}},
		SourceReports: []domain.SimulationSourceReport{
			{RelativePath: stale.RelativePath, SHA256: stale.SHA256, Fingerprint: stale.Fingerprint, Summary: "stale"},
			{RelativePath: current.RelativePath, SHA256: current.SHA256, Fingerprint: current.Fingerprint, Summary: "current"},
		},
	}

	got, changed := pruneProfileToScannedSources(profile, []scannedSource{current})

	if !changed {
		t.Fatal("changed = false, want true")
	}
	if len(got.Corpus.Sources) != 1 || got.Corpus.Sources[0].RelativePath != current.RelativePath {
		t.Fatalf("sources = %+v, want only current", got.Corpus.Sources)
	}
	if len(got.SourceReports) != 1 || got.SourceReports[0].Summary != "current" {
		t.Fatalf("reports = %+v, want only current", got.SourceReports)
	}
}

func sourcesForScannedSources(scanned []scannedSource) []domain.SimulationSource {
	sources := make([]domain.SimulationSource, 0, len(scanned))
	for _, source := range scanned {
		sources = append(sources, source.SimulationSource)
	}
	return sources
}

func synthesisFixture(note string) domain.SimulationSynthesis {
	return domain.SimulationSynthesis{
		Style: domain.SimulationStyle{
			NarrativeVoice: []string{"limited close narration"},
			ProseTexture:   []string{note},
		},
		RoleGuidance: domain.SimulationRoleGuidance{
			Writer: []string{"borrow techniques, never copy text"},
		},
	}
}

func verboseSourceReport(index int) domain.SimulationSourceReport {
	path := fmt.Sprintf("part_%03d.txt", index)
	sha := fmt.Sprintf("sha-%03d", index)
	return domain.SimulationSourceReport{
		RelativePath:       path,
		SHA256:             sha,
		Fingerprint:        domain.SimulationSourceFingerprint(path, sha),
		Title:              fmt.Sprintf("Source %03d", index),
		Summary:            strings.Repeat(fmt.Sprintf("summary-%03d ", index), 30),
		StyleObservations:  longReportItems(fmt.Sprintf("style-%03d", index), 4, 80),
		CommonWords:        longReportItems(fmt.Sprintf("word-%03d", index), 4, 40),
		PlotPatterns:       longReportItems(fmt.Sprintf("plot-%03d", index), 4, 80),
		HookPatterns:       longReportItems(fmt.Sprintf("hook-%03d", index), 4, 80),
		PacingNotes:        longReportItems(fmt.Sprintf("pace-%03d", index), 4, 80),
		ReaderAppeal:       longReportItems(fmt.Sprintf("appeal-%03d", index), 4, 80),
		ReusableTechniques: longReportItems(fmt.Sprintf("tech-%03d", index), 4, 80),
		Warnings:           longReportItems(fmt.Sprintf("warn-%03d", index), 2, 80),
	}
}

func longReportItems(prefix string, count int, repeatedRunes int) []string {
	items := make([]string, 0, count)
	for i := 0; i < count; i++ {
		items = append(items, fmt.Sprintf("%s-%02d-%s", prefix, i+1, strings.Repeat("x", repeatedRunes)))
	}
	return items
}

func validSourceReportJSON(summary string) string {
	return fmt.Sprintf(`{
  "summary": %q,
  "content_type": "body",
  "candidates": [{
    "dimension": "style.sentence_rhythm",
    "statement": "alternate concise impact beats with medium action lines",
    "phases": ["chapter"],
    "scope": "global",
    "confidence": 0.9,
    "tendency": "stable",
    "safety": "guidance"
  }],
  "safety_markers": [{
    "kind": "proper_noun",
    "value": "SourceOnlyName"
  }],
  "style_observations": ["close perspective", "sensory verbs"],
  "common_words": ["door", "shadow"],
  "plot_patterns": ["scene goal turns into a sharper dilemma"],
  "hook_patterns": ["end with an unanswered choice"],
  "pacing_notes": ["short setup, fast complication"],
  "reader_appeal": ["curiosity gap", "clear stakes"],
  "reusable_techniques": ["plant a concrete object before the reveal"]
}`, summary)
}

func validSynthesisJSON(note string) string {
	return fmt.Sprintf(`{
  "style": {
    "narrative_voice": ["limited close narration"],
    "sentence_rhythm": ["mix short impact lines with medium action lines"],
    "prose_texture": [%q],
    "perspective": ["stay near the protagonist"],
    "mood": ["tense, urgent"],
    "do_not_copy": ["do not reuse original names or sentences"]
  },
  "lexicon": {
    "common_words": ["door", "shadow"],
    "emotion_words": ["hesitation"],
    "scene_words": ["corridor"],
    "transition_words": ["meanwhile"],
    "signature_phrases": ["not yet"]
  },
  "plot_design": {
    "opening_patterns": ["start inside an unresolved pressure"],
    "escalation_patterns": ["make the cost visible after each answer"],
    "turning_point_patterns": ["reframe the clue"],
    "payoff_patterns": ["pay off the object before adding the next question"]
  },
  "hook_design": {
    "hook_types": ["mystery", "choice"],
    "placement": ["open and close scenes with changed stakes"],
    "cliffhanger_patterns": ["choice before consequence"],
    "payoff_rules": ["answer one question while opening another"]
  },
  "pacing_density": {
    "scene_density": ["one scene should carry goal, obstacle, turn"],
    "information_release": ["delay explanation until after action"],
    "dialogue_action_ratio": ["dialogue must change leverage"],
    "compression_rules": ["summarize transit, dramatize decisions"]
  },
  "reader_engagement": {
    "methods": ["curiosity gap", "stakes"],
    "emotional_drivers": ["fear of loss"],
    "progression_rewards": ["visible clue gain"],
    "anti_patterns": ["flat exposition"]
  },
  "role_guidance": {
    "coordinator": ["keep later tasks aligned with the simulation profile"],
    "architect": ["design arcs with repeated cost escalation"],
    "writer": ["borrow techniques, never copy text"],
    "editor": ["check imitation stays structural rather than plagiaristic"]
  }
}`, note)
}
