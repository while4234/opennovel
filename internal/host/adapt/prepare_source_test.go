package adapt

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/host/imp"
	"github.com/voocel/ainovel-cli/internal/store"
)

type scriptedAdaptLLM struct {
	responses []adaptLLMResponse
	calls     int
	got       [][]agentcore.Message
}

type adaptLLMResponse struct {
	text string
	err  error
}

func (m *scriptedAdaptLLM) Generate(_ context.Context, msgs []agentcore.Message, _ []agentcore.ToolSpec, _ ...agentcore.CallOption) (*agentcore.LLMResponse, error) {
	if len(msgs) > 0 && strings.Contains(msgs[0].TextContent(), "independent pre-writing auditor") {
		return &agentcore.LLMResponse{Message: agentcore.Message{
			Role: agentcore.RoleAssistant, Content: []agentcore.ContentBlock{agentcore.TextBlock(`{"verdict":"pass","summary":"test audit pass","findings":[]}`)}, Timestamp: time.Now(),
		}}, nil
	}
	m.got = append(m.got, msgs)
	if m.calls >= len(m.responses) {
		return nil, context.Canceled
	}
	resp := m.responses[m.calls]
	m.calls++
	if resp.err != nil {
		return nil, resp.err
	}
	return &agentcore.LLMResponse{Message: agentcore.Message{
		Role:      agentcore.RoleAssistant,
		Content:   []agentcore.ContentBlock{agentcore.TextBlock(resp.text)},
		Timestamp: time.Now(),
	}}, nil
}

func TestPrepareSourceResumesMissingChapterAndMergesWithoutRawBody(t *testing.T) {
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	sourcePath := writeAdaptSource(t, t.TempDir(), []string{
		"RAW_BODY_ONE_UNIQUE",
		"RAW_BODY_TWO_UNIQUE",
		"RAW_BODY_THREE_UNIQUE",
	})

	first := &scriptedAdaptLLM{responses: []adaptLLMResponse{
		{text: adaptAnalyzerEnvelope(1)},
		{text: adaptAnalyzerEnvelope(2)},
		{err: context.Canceled},
	}}
	err := PrepareSource(context.Background(), Deps{
		Store: st,
		LLM:   first,
		Prompts: Prompts{
			Analyzer:        "analyzer",
			FoundationMerge: "merge",
		},
	}, sourcePath, nil)
	if err == nil {
		t.Fatal("want first interrupted run to fail")
	}
	if first.calls != 3 {
		t.Fatalf("first calls=%d, want 3", first.calls)
	}
	if report, err := st.Adaptation.LoadSourceReport(1); err != nil || report == nil {
		t.Fatalf("chapter 1 report should be saved: report=%+v err=%v", report, err)
	}
	if report, err := st.Adaptation.LoadSourceReport(2); err != nil || report == nil {
		t.Fatalf("chapter 2 report should be saved: report=%+v err=%v", report, err)
	}
	if report, err := st.Adaptation.LoadSourceReport(3); err != nil || report != nil {
		t.Fatalf("chapter 3 report should not be saved: report=%+v err=%v", report, err)
	}
	if foundation, err := st.Adaptation.LoadSourceFoundation(); err != nil || foundation != nil {
		t.Fatalf("foundation should not be saved after interrupted run: foundation=%+v err=%v", foundation, err)
	}

	second := &scriptedAdaptLLM{responses: []adaptLLMResponse{
		{text: adaptAnalyzerEnvelope(3)},
		{text: adaptFoundationMergeEnvelope()},
		{text: adaptDossierBatchEnvelope()},
	}}
	if err := PrepareSource(context.Background(), Deps{
		Store: st,
		LLM:   second,
		Prompts: Prompts{
			Analyzer:        "analyzer",
			FoundationMerge: "merge",
		},
	}, sourcePath, nil); err != nil {
		t.Fatalf("PrepareSource resume: %v", err)
	}
	if second.calls != 3 {
		t.Fatalf("resume calls=%d, want missing chapter plus merge plus dossier", second.calls)
	}
	reports, err := st.Adaptation.LoadCompleteSourceReports()
	if err != nil {
		t.Fatalf("LoadCompleteSourceReports: %v", err)
	}
	if len(reports) != 3 {
		t.Fatalf("reports=%d, want 3", len(reports))
	}
	foundation, err := st.Adaptation.LoadSourceFoundation()
	if err != nil {
		t.Fatalf("LoadSourceFoundation: %v", err)
	}
	if foundation == nil || len(domain.FlattenOutline(foundation.Volumes)) != 3 {
		t.Fatalf("foundation outline should have 3 chapters: %+v", foundation)
	}

	mergePrompt := second.got[len(second.got)-1][1].TextContent()
	for _, raw := range []string{"RAW_BODY_ONE_UNIQUE", "RAW_BODY_TWO_UNIQUE", "RAW_BODY_THREE_UNIQUE"} {
		if strings.Contains(mergePrompt, raw) {
			t.Fatalf("merge prompt must not contain raw source body %q: %s", raw, mergePrompt)
		}
	}
}

func TestMergeSourceFoundationAssemblesReportBatchesWithoutFinalModelCall(t *testing.T) {
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	manifest := &domain.AdaptationSourceManifest{
		SourcePath:   "source.txt",
		ChapterCount: 3,
		Chapters: []domain.AdaptationSource{
			{Chapter: 1, SHA256: "sha-1", Runes: 1000},
			{Chapter: 2, SHA256: "sha-2", Runes: 1000},
			{Chapter: 3, SHA256: "sha-3", Runes: 1000},
		},
	}
	reports := []domain.AdaptationSourceReport{
		adaptFoundationSourceReport(1, "Alpha", "sha-1"),
		adaptFoundationSourceReport(2, "Beta", "sha-2"),
		adaptFoundationSourceReport(3, "Gamma", "sha-3"),
	}
	const runeLimit = 1800
	if batches := imp.FoundationMergeReportBatches(reports, runeLimit); len(batches) != 3 {
		t.Fatalf("test setup should split into 3 batches, got %d", len(batches))
	}

	first := &scriptedAdaptLLM{responses: []adaptLLMResponse{
		{text: adaptFoundationMergeEnvelope()},
		{text: adaptFoundationMergeEnvelope()},
		{text: adaptFoundationMergeEnvelope()},
	}}
	firstResult, err := mergeSourceFoundationResumable(context.Background(), Deps{
		Store:                st,
		LLM:                  first,
		ModelCallMaxAttempts: 1,
		Prompts: Prompts{
			FoundationMerge: "merge",
		},
	}, manifest, reports, runeLimit, nil)
	if err != nil {
		t.Fatalf("first merge: %v", err)
	}
	if first.calls != 3 {
		t.Fatalf("first calls=%d, want only 3 bounded report batches", first.calls)
	}
	if firstResult == nil || len(firstResult.Characters) == 0 {
		t.Fatalf("assembled foundation missing: %#v", firstResult)
	}
	if batch, err := st.Adaptation.LoadSourceFoundationBatch(0, 3); err != nil || batch == nil {
		t.Fatalf("report checkpoint should be saved: batch=%+v err=%v", batch, err)
	}
	if batch, err := st.Adaptation.LoadSourceFoundationBatch(1, 1); err != nil || batch == nil || batch.Kind != sourceFoundationBatchKindAssembled {
		t.Fatalf("assembled checkpoint should be saved: batch=%+v err=%v", batch, err)
	}

	second := &scriptedAdaptLLM{}
	got, err := mergeSourceFoundationResumable(context.Background(), Deps{
		Store:                st,
		LLM:                  second,
		ModelCallMaxAttempts: 1,
		Prompts: Prompts{
			FoundationMerge: "merge",
		},
	}, manifest, reports, runeLimit, nil)
	if err != nil {
		t.Fatalf("resume merge: %v", err)
	}
	if second.calls != 0 {
		t.Fatalf("resume should reuse report and assembled checkpoints without model calls, calls=%d", second.calls)
	}
	if got == nil || len(domain.FlattenOutline(got.Volumes)) != 3 {
		t.Fatalf("resumed foundation outline mismatch: %+v", got)
	}
	if batch, err := st.Adaptation.LoadSourceFoundationBatch(1, 1); err != nil || batch == nil || batch.Kind != sourceFoundationBatchKindAssembled {
		t.Fatalf("assembled checkpoint should be reusable on resume: batch=%+v err=%v", batch, err)
	}
}

func TestPrepareSourceSourceChangeResetsOldReports(t *testing.T) {
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	dir := t.TempDir()
	sourcePath := writeAdaptSource(t, dir, []string{"OLD_BODY_ONE", "OLD_BODY_TWO"})
	first := &scriptedAdaptLLM{responses: []adaptLLMResponse{
		{text: adaptAnalyzerEnvelope(1)},
		{text: adaptAnalyzerEnvelope(2)},
		{text: adaptFoundationMergeEnvelope()},
		{text: adaptDossierBatchEnvelope()},
	}}
	if err := PrepareSource(context.Background(), Deps{
		Store: st,
		LLM:   first,
		Prompts: Prompts{
			Analyzer:        "analyzer",
			FoundationMerge: "merge",
		},
	}, sourcePath, nil); err != nil {
		t.Fatalf("PrepareSource first: %v", err)
	}

	sourcePath = writeAdaptSource(t, dir, []string{"OLD_BODY_ONE", "NEW_BODY_TWO"})
	second := &scriptedAdaptLLM{responses: []adaptLLMResponse{
		{text: adaptAnalyzerEnvelope(1)},
		{text: adaptAnalyzerEnvelope(2)},
		{text: adaptFoundationMergeEnvelope()},
		{text: adaptDossierBatchEnvelope()},
	}}
	if err := PrepareSource(context.Background(), Deps{
		Store: st,
		LLM:   second,
		Prompts: Prompts{
			Analyzer:        "analyzer",
			FoundationMerge: "merge",
		},
	}, sourcePath, nil); err != nil {
		t.Fatalf("PrepareSource changed source: %v", err)
	}
	if second.calls != 4 {
		t.Fatalf("changed source should reanalyze all chapters and merge, calls=%d", second.calls)
	}
}

func TestPrepareSourceBuildsMissingCoCreateDossierWithoutReanalyzingPreparedSource(t *testing.T) {
	root := t.TempDir()
	st := store.NewStore(root)
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	sourcePath := writeAdaptSource(t, t.TempDir(), []string{"BODY_ONE", "BODY_TWO"})
	first := &scriptedAdaptLLM{responses: []adaptLLMResponse{
		{text: adaptAnalyzerEnvelope(1)},
		{text: adaptAnalyzerEnvelope(2)},
		{text: adaptFoundationMergeEnvelope()},
		{text: adaptDossierBatchEnvelope()},
	}}
	if err := PrepareSource(context.Background(), Deps{
		Store: st,
		LLM:   first,
		Prompts: Prompts{
			Analyzer:        "analyzer",
			FoundationMerge: "merge",
		},
	}, sourcePath, nil); err != nil {
		t.Fatalf("PrepareSource first: %v", err)
	}
	if err := os.Remove(filepath.Join(root, "meta", "adaptation", "cocreate_dossier.json")); err != nil {
		t.Fatalf("remove dossier: %v", err)
	}
	if err := os.RemoveAll(filepath.Join(root, "meta", "adaptation", "cocreate_dossier_batches")); err != nil {
		t.Fatalf("remove dossier batches: %v", err)
	}

	second := &scriptedAdaptLLM{responses: []adaptLLMResponse{{text: adaptDossierBatchEnvelope()}}}
	if err := PrepareSource(context.Background(), Deps{
		Store: st,
		LLM:   second,
		Prompts: Prompts{
			Analyzer:        "analyzer",
			FoundationMerge: "merge",
		},
	}, sourcePath, nil); err != nil {
		t.Fatalf("PrepareSource rebuild dossier: %v", err)
	}
	if second.calls != 1 {
		t.Fatalf("rebuild should only call dossier model, calls=%d", second.calls)
	}
	current, err := st.Adaptation.CoCreateDossierCurrent(CoCreateDossierPromptVersion, CoCreateDossierBatchSize)
	if err != nil || !current {
		t.Fatalf("dossier should be current: current=%v err=%v", current, err)
	}
}

func TestPrepareSourceBackfillsDossierFromPreparedSnapshotWithoutResplitting(t *testing.T) {
	root := t.TempDir()
	st := store.NewStore(root)
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	sourcePath := writeAdaptSource(t, t.TempDir(), []string{"BODY_ONE", "BODY_TWO"})
	chapters, err := imp.SplitFile(sourcePath)
	if err != nil {
		t.Fatalf("SplitFile: %v", err)
	}
	manifest, _, err := ensureSourceSnapshot(st.Adaptation, sourcePath, chapters)
	if err != nil {
		t.Fatalf("ensureSourceSnapshot: %v", err)
	}
	var reports []domain.AdaptationSourceReport
	for _, source := range manifest.Chapters {
		report := domain.AdaptationSourceReport{
			Chapter:      source.Chapter,
			Title:        source.Title,
			SourceSHA256: source.SHA256,
			Summary:      fmt.Sprintf("chapter %d summary", source.Chapter),
			Characters:   []string{"Ari"},
			KeyEvents:    []string{fmt.Sprintf("Ari drives chapter %d event", source.Chapter)},
		}
		if err := st.Adaptation.SaveSourceReport(report); err != nil {
			t.Fatalf("SaveSourceReport %d: %v", source.Chapter, err)
		}
		reports = append(reports, report)
	}
	if err := st.Adaptation.SaveSourceReports(reports); err != nil {
		t.Fatalf("SaveSourceReports: %v", err)
	}
	if err := st.Adaptation.SaveSourceFoundation(domain.AdaptationSourceFoundation{Premise: "prepared source"}); err != nil {
		t.Fatalf("SaveSourceFoundation: %v", err)
	}
	if err := os.WriteFile(sourcePath, []byte("Chapter 1: Changed\nTHIS WOULD NOT MATCH THE PREPARED SNAPSHOT\n"), 0o644); err != nil {
		t.Fatalf("rewrite source: %v", err)
	}
	if _, _, err := ValidatePreparedSource(st, sourcePath); err != nil {
		t.Fatalf("prepared source validation should trust the stored snapshot for the selected source path: %v", err)
	}

	llm := &scriptedAdaptLLM{responses: []adaptLLMResponse{
		{text: adaptDossierBatchEnvelope()},
		{text: adaptFoundationMergeEnvelope()},
	}}
	var events []Event
	if err := PrepareSource(context.Background(), Deps{
		Store: st,
		LLM:   llm,
		Prompts: Prompts{
			Analyzer:        "analyzer",
			FoundationMerge: "merge",
		},
	}, sourcePath, captureAdaptProgress(&events)); err != nil {
		t.Fatalf("PrepareSource backfill dossier: %v", err)
	}
	if llm.calls != 2 {
		t.Fatalf("calls=%d, want dossier backfill plus formal-cast policy upgrade", llm.calls)
	}
	if indexAdaptEvent(events, "切分原文章节") >= 0 {
		t.Fatalf("prepared dossier backfill should not split source: %+v", events)
	}
	if indexAdaptEvent(events, "分析原文第") >= 0 {
		t.Fatalf("prepared dossier backfill should not reanalyze chapters: %+v", events)
	}
	current, err := st.Adaptation.CoCreateDossierCurrent(CoCreateDossierPromptVersion, CoCreateDossierBatchSize, CoCreateDossierBatchRuneLimit)
	if err != nil || !current {
		t.Fatalf("dossier should be current: current=%v err=%v", current, err)
	}
}

func TestPrepareSourceRepairsMalformedCoCreateDossierBatch(t *testing.T) {
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	sourcePath := writeAdaptSource(t, t.TempDir(), []string{"BODY_ONE", "BODY_TWO"})
	llm := &scriptedAdaptLLM{responses: []adaptLLMResponse{
		{text: adaptAnalyzerEnvelope(1)},
		{text: adaptAnalyzerEnvelope(2)},
		{text: adaptFoundationMergeEnvelope()},
		{text: `{"result":{"metadata":"empty"}}`},
		{text: adaptDossierBatchEnvelope()},
	}}
	var events []Event
	if err := PrepareSource(context.Background(), Deps{
		Store: st,
		LLM:   llm,
		Prompts: Prompts{
			Analyzer:        "analyzer",
			FoundationMerge: "merge",
		},
	}, sourcePath, captureAdaptProgress(&events)); err != nil {
		t.Fatalf("PrepareSource repair dossier: %v", err)
	}
	if llm.calls != 5 {
		t.Fatalf("calls=%d, want analyzers, merge, malformed dossier, and repair", llm.calls)
	}
	if idx := indexAdaptEvent(events, "资料包第 1/1 批结构无效"); idx < 0 {
		t.Fatalf("missing dossier repair event: %+v", events)
	}
	repairPrompt := llm.got[4][1].TextContent()
	for _, fragment := range []string{"co-create dossier batch 1", "plot_phase", "previous_output", "metadata"} {
		if !strings.Contains(repairPrompt, fragment) {
			t.Fatalf("repair prompt missing %q: %s", fragment, repairPrompt)
		}
	}
	current, err := st.Adaptation.CoCreateDossierCurrent(CoCreateDossierPromptVersion, CoCreateDossierBatchSize, CoCreateDossierBatchRuneLimit)
	if err != nil || !current {
		t.Fatalf("dossier should be current after repair: current=%v err=%v", current, err)
	}
}

func TestPrepareSourceStripsDossierAdaptationAdvice(t *testing.T) {
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	sourcePath := writeAdaptSource(t, t.TempDir(), []string{"BODY_ONE"})
	llm := &scriptedAdaptLLM{responses: []adaptLLMResponse{
		{text: adaptAnalyzerEnvelope(1)},
		{text: adaptFoundationMergeEnvelope()},
		{text: `{
			"plot_phase":"Ari follows the source case.",
			"key_causality":["Ari's choice drives the next scene."],
			"ambiguity_risks":[{"chapters":[1],"characters":["Ari","Bryn"],"risk":"possible confusion","evidence":"chapter evidence","severity":"low","suggestion":"rewrite it this way"}],
			"adaptation_notes":["rewrite suggestion that should not be stored"]
		}`},
	}}
	if err := PrepareSource(context.Background(), Deps{
		Store: st,
		LLM:   llm,
		Prompts: Prompts{
			Analyzer:        "analyzer",
			FoundationMerge: "merge",
		},
	}, sourcePath, nil); err != nil {
		t.Fatalf("PrepareSource strip advice: %v", err)
	}
	batch, err := st.Adaptation.LoadCoCreateDossierBatch(1)
	if err != nil || batch == nil {
		t.Fatalf("LoadCoCreateDossierBatch: batch=%+v err=%v", batch, err)
	}
	if len(batch.AdaptationNotes) != 0 {
		t.Fatalf("dossier batch should not store adaptation notes: %+v", batch.AdaptationNotes)
	}
	if len(batch.AmbiguityRisks) != 1 || batch.AmbiguityRisks[0].Suggestion != "" {
		t.Fatalf("dossier risk suggestions should be stripped: %+v", batch.AmbiguityRisks)
	}
	dossier, err := st.Adaptation.LoadCoCreateDossier()
	if err != nil || dossier == nil {
		t.Fatalf("LoadCoCreateDossier: dossier=%+v err=%v", dossier, err)
	}
	if len(dossier.AdaptationNotes) != 0 {
		t.Fatalf("dossier should not store adaptation notes: %+v", dossier.AdaptationNotes)
	}
}

func TestPrepareSourceBackfillsCompletedDossierBatchesBeforeNewChapter(t *testing.T) {
	root := t.TempDir()
	st := store.NewStore(root)
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	sourcePath := writeNumberedAdaptSource(t, t.TempDir(), 81)
	chapters, err := imp.SplitFile(sourcePath)
	if err != nil {
		t.Fatalf("SplitFile: %v", err)
	}
	manifest, _, err := ensureSourceSnapshot(st.Adaptation, sourcePath, chapters)
	if err != nil {
		t.Fatalf("ensureSourceSnapshot: %v", err)
	}
	var reports []domain.AdaptationSourceReport
	for chapter := 1; chapter <= 80; chapter++ {
		report := domain.AdaptationSourceReport{
			Chapter:           chapter,
			Title:             fmt.Sprintf("Title %d", chapter),
			SourceSHA256:      manifest.Chapters[chapter-1].SHA256,
			AnalyzerVersion:   adaptationSourceAnalyzerVersion,
			AnalyzerSignature: sourceAnalyzerPromptSignature("analyzer"),
			Summary:           fmt.Sprintf("source chapter %d summary", chapter),
			KeyEvents:         []string{fmt.Sprintf("event %d", chapter)},
		}
		if err := st.Adaptation.SaveSourceReport(report); err != nil {
			t.Fatalf("SaveSourceReport %d: %v", chapter, err)
		}
		reports = append(reports, report)
	}
	if err := st.Adaptation.SaveSourceReports(reports); err != nil {
		t.Fatalf("SaveSourceReports: %v", err)
	}

	llm := &scriptedAdaptLLM{responses: []adaptLLMResponse{
		{text: adaptDossierBatchEnvelope()},
		{text: adaptDossierBatchEnvelope()},
		{text: adaptAnalyzerEnvelope(81)},
		{text: adaptFoundationMergeEnvelope()},
		{text: adaptDossierBatchEnvelope()},
	}}
	var events []Event
	if err := PrepareSource(context.Background(), Deps{
		Store: st,
		LLM:   llm,
		Prompts: Prompts{
			Analyzer:        "analyzer",
			FoundationMerge: "merge",
		},
	}, sourcePath, captureAdaptProgress(&events)); err != nil {
		t.Fatalf("PrepareSource resume: %v", err)
	}
	if llm.calls != 5 {
		t.Fatalf("calls=%d, want two backfilled batches, chapter 81, merge, and final batch", llm.calls)
	}
	firstNewChapterEvent := indexAdaptEvent(events, "分析原文第 81/81 章")
	if firstNewChapterEvent < 0 {
		t.Fatalf("missing chapter 81 analysis event: %+v", events)
	}
	for _, fragment := range []string{"资料包第 1/3 批完成", "资料包第 2/3 批完成"} {
		idx := indexAdaptEvent(events, fragment)
		if idx < 0 {
			t.Fatalf("missing backfill event %q: %+v", fragment, events)
		}
		if idx > firstNewChapterEvent {
			t.Fatalf("backfill event %q occurred after new chapter analysis", fragment)
		}
	}
	for batch := 1; batch <= 3; batch++ {
		if got, err := st.Adaptation.LoadCoCreateDossierBatch(batch); err != nil || got == nil {
			t.Fatalf("batch %d missing after resume: batch=%+v err=%v", batch, got, err)
		}
	}
	current, err := st.Adaptation.CoCreateDossierCurrent(CoCreateDossierPromptVersion, CoCreateDossierBatchSize)
	if err != nil || !current {
		t.Fatalf("dossier should be current: current=%v err=%v", current, err)
	}
}

func writeAdaptSource(t *testing.T, dir string, bodies []string) string {
	t.Helper()
	var sb strings.Builder
	for i, body := range bodies {
		if i > 0 {
			sb.WriteString("\n\n")
		}
		sb.WriteString("Chapter ")
		sb.WriteString(string(rune('1' + i)))
		sb.WriteString(": Title\n")
		sb.WriteString(body)
		sb.WriteString("\n")
	}
	path := filepath.Join(dir, "source.txt")
	if err := os.WriteFile(path, []byte(sb.String()), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}
	return path
}

func writeNumberedAdaptSource(t *testing.T, dir string, count int) string {
	t.Helper()
	var sb strings.Builder
	for chapter := 1; chapter <= count; chapter++ {
		if chapter > 1 {
			sb.WriteString("\n\n")
		}
		fmt.Fprintf(&sb, "Chapter %d: Title %d\nBODY_%03d\n", chapter, chapter, chapter)
	}
	path := filepath.Join(dir, "source.txt")
	if err := os.WriteFile(path, []byte(sb.String()), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}
	return path
}

func adaptFoundationSourceReport(chapter int, marker string, sourceSHA string) domain.AdaptationSourceReport {
	return domain.AdaptationSourceReport{
		Chapter:        chapter,
		Title:          fmt.Sprintf("Title %d", chapter),
		SourceSHA256:   sourceSHA,
		Summary:        strings.Repeat(marker+" summary fact. ", 170),
		Characters:     []string{"Ari", marker},
		CharacterFacts: []string{marker + " changes Ari's source arc."},
		KeyEvents:      []string{marker + " irreversible event"},
		WorldRules:     []string{marker + " continuity rule"},
		HookType:       "mystery",
		DominantStrand: "quest",
	}
}

func indexAdaptEvent(events []Event, fragment string) int {
	for i, event := range events {
		if strings.Contains(event.Message, fragment) {
			return i
		}
	}
	return -1
}

func adaptAnalyzerEnvelope(chapter int) string {
	return `=== SUMMARY ===
Chapter summary.

=== CHARACTERS ===
["Ari"]

=== CHARACTER_PROFILES ===
[{"id":"ari","name":"Ari","role":"lead","gender":"female","description":"Follows the central case.","traits":["focused"],"tier":"core","contrast_details":[{"surface":"reserved","depth":"acts decisively under pressure"}],"key_backstory":[{"event":"lost access to a trusted record","impact":"verifies every new lead"}]}]

=== CHARACTER_FACTS ===
["Ari advances chapter facts."]

=== WORLD_RULES ===
["The city keeps strict records."]

=== KEY_EVENTS ===
["Key event happens."]

=== TIMELINE ===
[]

=== FORESHADOW ===
[]

=== RELATIONSHIPS ===
[]

=== STATE_CHANGES ===
[]

=== HOOK_TYPE ===
mystery

=== DOMINANT_STRAND ===
quest
`
}

func adaptFoundationMergeEnvelope() string {
	return `=== PREMISE ===
# Source Book

Ari follows the source causal chain.

=== CHARACTERS ===
[{"id":"ari","name":"Ari","role":"lead","gender":"female","description":"Follows the central case.","arc":"chooses courage","traits":["focused"],"tier":"core","contrast_details":[{"surface":"reserved","depth":"acts decisively under pressure"}],"key_backstory":[{"event":"lost access to a trusted record","impact":"verifies every new lead"}]}]

=== RELATIONSHIPS ===
[]

=== WORLD_RULES ===
[{"category":"society","rule":"The city keeps strict records.","boundary":"Records cannot be ignored."}]

=== COMPASS ===
{"ending_direction":"Ari resolves the source case.","open_threads":["who controls the records"],"estimated_scale":"short"}
`
}

func TestSourceFoundationHasVersionedMetadataRejectsLegacyFoundation(t *testing.T) {
	manifest := domain.AdaptationSourceManifest{
		SourcePath:   "source.txt",
		ChapterCount: 1,
		Chapters: []domain.AdaptationSource{{
			Chapter: 1,
			Title:   "One",
			SHA256:  "chapter-signature",
		}},
	}
	foundation := domain.AdaptationSourceFoundation{
		Premise:    "premise",
		Characters: []domain.Character{{Name: "Ari", Role: "lead"}},
		Volumes: []domain.VolumeOutline{{
			Index: 1,
			Arcs: []domain.ArcOutline{{
				Index:    1,
				Chapters: []domain.OutlineEntry{{Chapter: 1, Title: "One"}},
			}},
		}},
	}
	if SourceFoundationHasVersionedMetadata(&foundation, &manifest) {
		t.Fatal("legacy source foundation without versioned bindings must require upgrade")
	}

	foundation.Version = adaptationSourceFoundationVersion
	foundation.SourceChapterCount = manifest.ChapterCount
	foundation.SourceSignature = store.AdaptationSourceSignature(manifest)
	foundation.ReportSignature = "report-signature"
	foundation.PromptVersion = adaptationSourceFoundationPromptVersion + ":prompt-signature"
	foundation.BatchRuneLimit = 70_000
	if !SourceFoundationHasVersionedMetadata(&foundation, &manifest) {
		t.Fatal("fully bound source foundation should be reusable")
	}
}

func TestLegacySourceFoundationPolicyMigrationRequiresExactStoredEvidence(t *testing.T) {
	manifest := domain.AdaptationSourceManifest{
		SourcePath:   "source.txt",
		ChapterCount: 1,
		Chapters: []domain.AdaptationSource{{
			Chapter: 1,
			Title:   "One",
			SHA256:  "chapter-signature",
		}},
	}
	foundation := domain.AdaptationSourceFoundation{
		Version:            adaptationSourceFoundationVersion - 1,
		SourceChapterCount: manifest.ChapterCount,
		SourceSignature:    store.AdaptationSourceSignature(manifest),
		ReportSignature:    "report-signature",
	}
	if !legacySourceFoundationCanMigrate(&foundation, &manifest, "report-signature") {
		t.Fatal("matching v3 foundation and stored report signature should migrate without another model call")
	}
	foundation.ReportSignature = "stale-report"
	if legacySourceFoundationCanMigrate(&foundation, &manifest, "report-signature") {
		t.Fatal("stale report evidence must not be migrated deterministically")
	}
	foundation.ReportSignature = "report-signature"
	foundation.Version = adaptationSourceFoundationVersion
	if legacySourceFoundationCanMigrate(&foundation, &manifest, "report-signature") {
		t.Fatal("current source foundation is not a legacy migration candidate")
	}
}

func adaptDossierBatchEnvelope() string {
	return `{
		"plot_phase": "Ari follows the source case.",
		"key_causality": ["Ari's choice drives the next scene."],
		"plot_threads": ["The records case remains active."],
		"character_arcs": ["Ari chooses courage under pressure."],
		"world_constraints": ["The city keeps strict records."],
		"major_characters": ["Ari"],
		"relationship_signals": [],
		"heroine_signals": [],
		"ambiguity_risks": [],
		"couple_milestones": []
	}`
}

func TestCurrentSourceReportRequiresAnalyzerContractMetadata(t *testing.T) {
	report := &domain.AdaptationSourceReport{
		Chapter: 1, SourceSHA256: "sha", Summary: "summary", KeyEvents: []string{"event"},
	}
	if currentSourceReport(report, "sha", "analyzer prompt") {
		t.Fatal("legacy report must be reanalyzed after the analyzer contract changes")
	}
	report.AnalyzerVersion = adaptationSourceAnalyzerVersion
	report.AnalyzerSignature = sourceAnalyzerPromptSignature("analyzer prompt")
	if !currentSourceReport(report, "sha", "analyzer prompt") {
		t.Fatal("report with matching source and analyzer metadata should be reusable")
	}
	if currentSourceReport(report, "sha", "changed prompt") {
		t.Fatal("prompt change must invalidate the report")
	}
}
