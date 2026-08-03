package adapt

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/store"
	"github.com/voocel/litellm"
)

func TestBuildCoCreateIntentInfersGoalFromUserRequest(t *testing.T) {
	heroineOnly := BuildCoCreateIntent("只增加女主戏份和男女主互动，不清理女配", domain.AdaptationGranularityArc, domain.AdaptationRewriteFullRewrite, 0)
	if !containsText(heroineOnly.Goals, "heroine presence") {
		t.Fatalf("heroine-only goals = %v, want heroine presence goal", heroineOnly.Goals)
	}
	if containsText(heroineOnly.Goals, "strict single-heroine") {
		t.Fatalf("heroine-only goals = %v, should not force strict single-heroine cleanup", heroineOnly.Goals)
	}

	strictSingle := BuildCoCreateIntent("后宫改严格单女主，女配不能有暧昧和身体接触", domain.AdaptationGranularityFree, domain.AdaptationRewriteFullRewrite, 0)
	if !containsText(strictSingle.Goals, "strict single-heroine") {
		t.Fatalf("strict-single goals = %v, want strict single-heroine goal", strictSingle.Goals)
	}
}

func TestCoCreateBriefingTriggerReasonUsesLongNovelThresholds(t *testing.T) {
	if reason := CoCreateBriefingTriggerReason(domain.AdaptationCoCreateDossier{SourceChapterCount: 40, Batches: make([]domain.AdaptationCoCreateDossierBatch, 1)}); reason != "" {
		t.Fatalf("short dossier trigger = %q, want empty", reason)
	}
	if reason := CoCreateBriefingTriggerReason(domain.AdaptationCoCreateDossier{SourceChapterCount: 321, Batches: make([]domain.AdaptationCoCreateDossierBatch, 9)}); !strings.Contains(reason, "source_chapter_count") {
		t.Fatalf("long dossier trigger = %q, want source chapter threshold", reason)
	}
}

func TestNormalizeBriefingDecisionsRejectsVagueQuestions(t *testing.T) {
	decisions := normalizeBriefingDecisions([]domain.AdaptationBriefingDecision{
		{
			ID:       "bad",
			Question: "是否符合预期？",
			Evidence: "chapter 1",
			Impact:   "none",
			Options: []domain.AdaptationDecisionOption{
				{ID: "a", Label: "Yes"},
				{ID: "b", Label: "No"},
			},
		},
		{
			ID:       "good",
			Question: "Should the side confession be removed or rewritten as ordinary trust?",
			Evidence: "chapter 90 confession",
			Impact:   "changes single-heroine cleanup in late arcs",
			Options: []domain.AdaptationDecisionOption{
				{ID: "a", Label: "Remove"},
				{ID: "b", Label: "Rewrite as trust"},
			},
		},
	}, "q", 8)
	if len(decisions) != 1 || decisions[0].ID != "good" {
		t.Fatalf("decisions = %+v, want only the concrete question", decisions)
	}
}

func TestEnsureCoCreateBriefingRetriesProviderGatewayError(t *testing.T) {
	defer stubPlannerRetrySleep(t)()
	st := newLongBriefingTestStore(t, 321)
	llm := &scriptedAdaptLLM{responses: []adaptLLMResponse{
		{err: litellm.NewHTTPError("deepseek", 502, "<html><body>502 Bad Gateway</body></html>")},
		{text: coCreateBriefingBatchJSON("retry recovered batch 1")},
		{text: coCreateBriefingBatchJSON("batch 2")},
	}}
	var progress []Event
	briefing, err := EnsureCoCreateBriefing(context.Background(), Deps{
		Store:                st,
		LLM:                  llm,
		ModelCallMaxAttempts: 2,
	}, BuildCoCreateIntent("strict single heroine", domain.AdaptationGranularityFree, domain.AdaptationRewriteFullRewrite, 0), captureAdaptProgress(&progress))
	if err != nil {
		t.Fatalf("EnsureCoCreateBriefing: %v", err)
	}
	if llm.calls != 3 {
		t.Fatalf("llm calls = %d, want failed attempt + retry + second batch", llm.calls)
	}
	if briefing == nil || len(briefing.Batches) != 2 {
		t.Fatalf("briefing batches = %+v, want two batches", briefing)
	}
	if !hasAdaptProgressWithStage(progress, StageBriefing, "重试 2/2") || !hasAdaptProgress(progress, "provider gateway error: 502 Bad Gateway") {
		t.Fatalf("progress should expose sanitized retry event: %+v", progress)
	}
}

func TestEnsureCoCreateBriefingRepairsInvalidBatchStructure(t *testing.T) {
	st := newLongBriefingTestStore(t, 321)
	llm := &scriptedAdaptLLM{responses: []adaptLLMResponse{
		{text: `{}`},
		{text: coCreateBriefingBatchJSON("repaired batch 1")},
		{text: coCreateBriefingBatchJSON("batch 2")},
	}}
	var progress []Event
	briefing, err := EnsureCoCreateBriefing(context.Background(), Deps{
		Store:                      st,
		LLM:                        llm,
		ModelCallMaxAttempts:       1,
		StructureRepairMaxAttempts: 3,
	}, BuildCoCreateIntent("strict single heroine", domain.AdaptationGranularityFree, domain.AdaptationRewriteFullRewrite, 0), captureAdaptProgress(&progress))
	if err != nil {
		t.Fatalf("EnsureCoCreateBriefing: %v", err)
	}
	if llm.calls != 3 {
		t.Fatalf("llm calls = %d, want bad batch + repair + second batch", llm.calls)
	}
	if briefing == nil || len(briefing.Batches) != 2 {
		t.Fatalf("briefing batches = %+v, want two batches", briefing)
	}
	if !hasAdaptProgressWithStage(progress, StageBriefing, "结构无效，正在修复第 1/3 次") {
		t.Fatalf("progress should expose briefing repair attempt budget: %+v", progress)
	}
}

func TestEnsureCoCreateBriefingDoesNotSaveInvalidBatchAfterRepairExhaustion(t *testing.T) {
	st := newLongBriefingTestStore(t, 321)
	llm := &scriptedAdaptLLM{responses: []adaptLLMResponse{
		{text: `{}`},
		{text: `{}`},
		{text: `{}`},
		{text: `{}`},
		{text: `{}`},
		{text: `{}`},
	}}
	var progress []Event
	briefing, err := EnsureCoCreateBriefing(context.Background(), Deps{
		Store:                      st,
		LLM:                        llm,
		ModelCallMaxAttempts:       1,
		StructureRepairMaxAttempts: 2,
	}, BuildCoCreateIntent("strict single heroine", domain.AdaptationGranularityFree, domain.AdaptationRewriteFullRewrite, 0), captureAdaptProgress(&progress))
	if err == nil {
		t.Fatal("EnsureCoCreateBriefing succeeded, want invalid structure failure")
	}
	if briefing != nil {
		t.Fatalf("briefing = %+v, want nil on failure", briefing)
	}
	if llm.calls != 6 {
		t.Fatalf("llm calls = %d, want two generations plus two repairs each", llm.calls)
	}
	if !strings.Contains(err.Error(), "missing content") {
		t.Fatalf("error = %v, want missing content", err)
	}
	if !hasAdaptProgressWithStage(progress, StageBriefing, "结构修复后仍无效，重新生成第 2/2 次") {
		t.Fatalf("progress should expose briefing regeneration attempt: %+v", progress)
	}
	batch, loadErr := st.Adaptation.LoadCoCreateBriefingBatch(1)
	if loadErr != nil {
		t.Fatalf("LoadCoCreateBriefingBatch: %v", loadErr)
	}
	if batch != nil {
		t.Fatalf("saved invalid briefing batch = %+v, want nil", batch)
	}
}

func TestEnsureProposalCoCreateBriefingMigratesAndPinsResumableProposal(t *testing.T) {
	st := newLongBriefingTestStore(t, 321)
	manifest, err := st.Adaptation.LoadSourceManifest()
	if err != nil || manifest == nil {
		t.Fatalf("LoadSourceManifest: manifest=%+v err=%v", manifest, err)
	}
	dossier, err := st.Adaptation.LoadCoCreateDossier()
	if err != nil || dossier == nil {
		t.Fatalf("LoadCoCreateDossier: dossier=%+v err=%v", dossier, err)
	}
	storedIntent := BuildCoCreateIntent("strict single heroine", domain.AdaptationGranularityArc, domain.AdaptationRewriteFullRewrite, 0)
	if err := st.Adaptation.SaveCoCreateIntent(storedIntent); err != nil {
		t.Fatalf("SaveCoCreateIntent: %v", err)
	}
	briefing := domain.AdaptationCoCreateBriefing{
		Version: 1, PromptVersion: CoCreateBriefingPromptVersion,
		DossierPromptVersion: "stale-v2", IntentHash: storedIntent.IntentHash,
		SourceSignature: store.AdaptationSourceSignature(*manifest), SourceChapterCount: manifest.ChapterCount,
		DossierBatchCount: len(dossier.Batches), ConfirmedFacts: []string{"pinned fact"},
	}
	if err := st.Adaptation.SaveCoCreateBriefing(briefing); err != nil {
		t.Fatalf("SaveCoCreateBriefing: %v", err)
	}
	if err := st.Adaptation.SaveProposalRuntime(domain.AdaptationProposalRuntime{
		Version: 2, Brief: "compiled brief", SourcePath: manifest.SourcePath, SourceChapterCount: manifest.ChapterCount,
		Granularity: storedIntent.Granularity, RewritePolicy: storedIntent.RewritePolicy,
		CompletedBatches: []domain.AdaptationProposalRuntimeBatch{{TargetFrom: 1, TargetTo: 4}},
	}); err != nil {
		t.Fatalf("SaveProposalRuntime: %v", err)
	}
	llm := &scriptedAdaptLLM{}
	var progress []Event
	incoming := BuildCoCreateIntent("compiled proposal brief", domain.AdaptationGranularityArc, domain.AdaptationRewriteFullRewrite, 0)
	got, err := EnsureProposalCoCreateBriefing(context.Background(), Deps{Store: st, LLM: llm}, incoming, captureAdaptProgress(&progress))
	if err != nil {
		t.Fatalf("EnsureProposalCoCreateBriefing: %v", err)
	}
	if got == nil || len(got.ConfirmedFacts) != 1 || got.ConfirmedFacts[0] != "pinned fact" {
		t.Fatalf("briefing=%+v", got)
	}
	if llm.calls != 0 {
		t.Fatalf("llm calls=%d, want no upstream regeneration during proposal resume", llm.calls)
	}
	if !hasAdaptProgressWithStage(progress, StageBriefing, "pinned by proposal runtime") {
		t.Fatalf("progress=%+v", progress)
	}
	runtime, err := st.Adaptation.LoadProposalRuntime()
	if err != nil || runtime == nil || runtime.CoCreateDependency == nil {
		t.Fatalf("runtime dependency was not migrated: runtime=%+v err=%v", runtime, err)
	}
	if runtime.CoCreateDependency.IntentHash != storedIntent.IntentHash {
		t.Fatalf("dependency intent hash=%q", runtime.CoCreateDependency.IntentHash)
	}
	briefing.PromptVersion = "unexpected-replacement"
	if err := st.Adaptation.SaveCoCreateBriefing(briefing); err != nil {
		t.Fatalf("replace briefing: %v", err)
	}
	if _, err := EnsureProposalCoCreateBriefing(context.Background(), Deps{Store: st, LLM: llm}, incoming, nil); err == nil || !strings.Contains(err.Error(), "pinned co-create briefing changed") {
		t.Fatalf("changed pinned dependency error=%v", err)
	}
	if llm.calls != 0 {
		t.Fatalf("llm calls after dependency replacement=%d, want fail closed", llm.calls)
	}
}

func newLongBriefingTestStore(t *testing.T, chapterCount int) *store.Store {
	t.Helper()
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	manifest := longBriefingTestManifest(chapterCount)
	if err := st.Adaptation.SaveSourceManifest(manifest); err != nil {
		t.Fatalf("SaveSourceManifest: %v", err)
	}
	dossier := longBriefingTestDossier(manifest)
	if err := st.Adaptation.SaveCoCreateDossier(dossier); err != nil {
		t.Fatalf("SaveCoCreateDossier: %v", err)
	}
	return st
}

func hasAdaptProgressWithStage(events []Event, stage Stage, fragment string) bool {
	for _, event := range events {
		if event.Stage == stage && strings.Contains(event.Message, fragment) {
			return true
		}
	}
	return false
}

func containsText(values []string, needle string) bool {
	for _, value := range values {
		if strings.Contains(value, needle) {
			return true
		}
	}
	return false
}

func longBriefingTestManifest(chapterCount int) domain.AdaptationSourceManifest {
	chapters := make([]domain.AdaptationSource, 0, chapterCount)
	for chapter := 1; chapter <= chapterCount; chapter++ {
		chapters = append(chapters, domain.AdaptationSource{
			Chapter: chapter,
			Title:   fmt.Sprintf("Chapter %d", chapter),
			SHA256:  fmt.Sprintf("sha-%d", chapter),
			Runes:   1000,
		})
	}
	return domain.AdaptationSourceManifest{
		SourcePath:   "source.txt",
		ChapterCount: chapterCount,
		Chapters:     chapters,
	}
}

func longBriefingTestDossier(manifest domain.AdaptationSourceManifest) domain.AdaptationCoCreateDossier {
	specs := store.AdaptationDossierBatchSpecs(manifest, CoCreateDossierBatchSize, CoCreateDossierBatchRuneLimit)
	batches := make([]domain.AdaptationCoCreateDossierBatch, 0, len(specs))
	for _, spec := range specs {
		batches = append(batches, domain.AdaptationCoCreateDossierBatch{
			Index:            spec.Index,
			SourceFrom:       spec.SourceFrom,
			SourceTo:         spec.SourceTo,
			SourceSignature:  spec.SourceSignature,
			PlotPhase:        "source arc",
			KeyCausality:     []string{"cause and effect"},
			PlotThreads:      []string{"main thread"},
			CharacterArcs:    []string{"heroine arc"},
			WorldConstraints: []string{"world rule"},
		})
	}
	return domain.AdaptationCoCreateDossier{
		Version:            1,
		PromptVersion:      CoCreateDossierPromptVersion,
		SourceSignature:    store.AdaptationSourceSignature(manifest),
		SourceChapterCount: manifest.ChapterCount,
		BatchSize:          CoCreateDossierBatchSize,
		BatchRuneLimit:     CoCreateDossierBatchRuneLimit,
		Batches:            batches,
	}
}

func coCreateBriefingBatchJSON(fact string) string {
	return fmt.Sprintf(`{
		"confirmed_facts": [%q],
		"adaptation_suggestions": ["keep the target relationship clear"]
	}`, fact)
}
