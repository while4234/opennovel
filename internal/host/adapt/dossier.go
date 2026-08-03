package adapt

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/modeldiag"
	"github.com/voocel/ainovel-cli/internal/store"
)

const (
	CoCreateDossierBatchSize      = 40
	CoCreateDossierBatchRuneLimit = 40000
	CoCreateDossierPromptVersion  = "v3"

	coCreateDossierVersion   = 1
	coCreateDossierMaxTokens = 6144
)

const coCreateDossierSystemPrompt = `You are an adaptation continuity analyst for long Chinese web novels.
You create a neutral, broad-purpose source dossier for later co-creation requests.
You read compact per-chapter source reports, not raw prose. Extract only facts supported by the reports.

Return one JSON object with this shape:
{
  "plot_phase": "brief phase summary for this source range",
  "key_causality": ["major causal chain or irreversible source fact"],
  "plot_threads": ["active or resolved plot thread, antagonist move, mystery, war/political arc, cultivation/business/legal arc, etc."],
  "character_arcs": ["important character change, motivation, role shift, betrayal, alliance, growth, downfall, or supporting-character hook"],
  "world_constraints": ["setting, faction, power-system, timeline, geography, legal, political, family, or continuity constraint"],
  "major_characters": ["names that matter in this source range; batch-local evidence only, never whole-book formal-card eligibility"],
  "relationship_signals": [{"chapters":[1],"characters":["A","B"],"type":"trust/conflict/family/political/mentor/rival/romance/etc","summary":"what changed","evidence":"chapter evidence"}],
  "heroine_signals": [{"chapters":[1],"characters":["lead","heroine"],"type":"interaction/status/milestone","summary":"heroine-relevant beat when supported by source evidence","evidence":"chapter evidence"}],
  "ambiguity_risks": [{"chapters":[1],"characters":["lead","side character"],"risk":"relationship ambiguity, audience-confusion, harem/body-contact risk, or other source-supported ambiguity/continuity risk","evidence":"chapter evidence","severity":"low|medium|high"}],
  "couple_milestones": [{"chapters":[1],"characters":["lead","heroine"],"type":"meet/ambiguous/confession/couple/etc","summary":"relationship milestone when supported by source evidence","evidence":"chapter evidence"}]
}

Rules:
- Preserve source causality and chapter references.
- Be neutral and reusable for many adaptation goals: main plot, supporting characters, factions, relationships, pacing, mysteries, world rules, and romance are all valid.
- Do not let heroine/romance concerns crowd out important non-romance plot or supporting-character material.
- Do not invent romance. Record heroine/couple/ambiguity items only when supported by reports.
- Keep each array compact: usually 3-8 items, prioritizing facts useful across future user requests.
- major_characters is local to this batch. It does not declare whole-book protagonist status or grant permission to create a formal character card.`

func coCreateDossierBatchJSONSchema() map[string]any {
	stringArray := map[string]any{
		"type":  "array",
		"items": map[string]any{"type": "string"},
	}
	integerArray := map[string]any{
		"type":  "array",
		"items": map[string]any{"type": "integer"},
	}
	signal := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"chapters":   integerArray,
			"characters": stringArray,
			"type":       map[string]any{"type": "string"},
			"summary":    map[string]any{"type": "string"},
			"evidence":   map[string]any{"type": "string"},
		},
		"required": []string{"chapters", "characters", "type", "summary", "evidence"},
	}
	risk := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"chapters":   integerArray,
			"characters": stringArray,
			"risk":       map[string]any{"type": "string"},
			"evidence":   map[string]any{"type": "string"},
			"severity":   map[string]any{"type": "string", "enum": []string{"low", "medium", "high"}},
		},
		"required": []string{"chapters", "characters", "risk", "evidence", "severity"},
	}
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"plot_phase":           map[string]any{"type": "string", "minLength": 1},
			"key_causality":        stringArray,
			"plot_threads":         stringArray,
			"character_arcs":       stringArray,
			"world_constraints":    stringArray,
			"major_characters":     stringArray,
			"relationship_signals": map[string]any{"type": "array", "items": signal},
			"heroine_signals":      map[string]any{"type": "array", "items": signal},
			"ambiguity_risks":      map[string]any{"type": "array", "items": risk},
			"couple_milestones":    map[string]any{"type": "array", "items": signal},
		},
		"required": []string{
			"plot_phase",
			"key_causality",
			"plot_threads",
			"character_arcs",
			"world_constraints",
			"major_characters",
			"relationship_signals",
			"heroine_signals",
			"ambiguity_risks",
			"couple_milestones",
		},
	}
}

func EnsureCoCreateDossier(ctx context.Context, deps Deps, manifest *domain.AdaptationSourceManifest, reports []domain.AdaptationSourceReport, emit ProgressEmitter) (*domain.AdaptationCoCreateDossier, error) {
	ctx = modeldiag.WithStore(ctx, deps.Store)
	if deps.Store == nil || deps.LLM == nil {
		return nil, fmt.Errorf("deps incomplete")
	}
	if manifest == nil || manifest.ChapterCount <= 0 {
		return nil, fmt.Errorf("source manifest is required")
	}
	if len(reports) != manifest.ChapterCount {
		return nil, fmt.Errorf("source reports incomplete: got %d, want %d", len(reports), manifest.ChapterCount)
	}

	current, err := deps.Store.Adaptation.LoadCoCreateDossier()
	if err != nil {
		return nil, fmt.Errorf("load co-create dossier: %w", err)
	}
	if current != nil && store.CoCreateDossierMatchesManifest(*current, *manifest, CoCreateDossierPromptVersion, CoCreateDossierBatchSize, CoCreateDossierBatchRuneLimit) {
		emitAdaptProgress(emit, StageDossier, manifest.ChapterCount, manifest.ChapterCount, "全书改编资料包已存在，跳过生成", nil)
		return current, nil
	}

	emitAdaptProgress(emit, StageDossier, 0, manifest.ChapterCount, "生成全书改编资料包...", nil)
	batches, err := ensureCoCreateDossierBatches(ctx, deps, manifest, reports, emit)
	if err != nil {
		return nil, err
	}
	if len(batches) != len(dossierBatchSpecs(*manifest, CoCreateDossierBatchSize)) {
		return nil, fmt.Errorf("co-create dossier batches incomplete: got %d", len(batches))
	}

	dossier := assembleCoCreateDossier(*manifest, batches)
	if err := deps.Store.Adaptation.SaveCoCreateDossier(dossier); err != nil {
		return nil, fmt.Errorf("save co-create dossier: %w", err)
	}
	emitAdaptProgress(emit, StageDossier, manifest.ChapterCount, manifest.ChapterCount, fmt.Sprintf("全书改编资料包已生成：%d 批 / %d 章", len(batches), manifest.ChapterCount), nil)
	return &dossier, nil
}

func ensureCoCreateDossierBatches(ctx context.Context, deps Deps, manifest *domain.AdaptationSourceManifest, reports []domain.AdaptationSourceReport, emit ProgressEmitter) ([]domain.AdaptationCoCreateDossierBatch, error) {
	if deps.Store == nil || deps.LLM == nil {
		return nil, fmt.Errorf("deps incomplete")
	}
	if manifest == nil || manifest.ChapterCount <= 0 {
		return nil, fmt.Errorf("source manifest is required")
	}
	sourceSHAByChapter := make(map[int]string, len(manifest.Chapters))
	for _, source := range manifest.Chapters {
		sourceSHAByChapter[source.Chapter] = source.SHA256
	}
	reportByChapter := make(map[int]domain.AdaptationSourceReport, len(reports))
	for _, report := range reports {
		if sha := sourceSHAByChapter[report.Chapter]; sha != "" && reusableSourceReport(&report, sha) {
			reportByChapter[report.Chapter] = report
		}
	}

	specs := dossierBatchSpecs(*manifest, CoCreateDossierBatchSize)
	batches := make([]domain.AdaptationCoCreateDossierBatch, 0, len(specs))
	for _, spec := range specs {
		if err := ctx.Err(); err != nil {
			return batches, err
		}
		batchReports, ok := sourceReportsForDossierBatch(spec, reportByChapter)
		if !ok {
			continue
		}
		existing, err := deps.Store.Adaptation.LoadCoCreateDossierBatch(spec.Index)
		if err != nil {
			return batches, fmt.Errorf("load co-create dossier batch %d: %w", spec.Index, err)
		}
		if coCreateDossierBatchCurrent(existing, spec) {
			batches = append(batches, *existing)
			continue
		}

		emitAdaptProgress(emit, StageDossier, spec.SourceFrom, manifest.ChapterCount, fmt.Sprintf("分析资料包第 %d/%d 批：原书第 %d-%d 章", spec.Index, len(specs), spec.SourceFrom, spec.SourceTo), nil)
		batch, err := buildCoCreateDossierBatch(ctx, deps, spec, batchReports, len(specs), emit)
		if err != nil {
			return batches, fmt.Errorf("build co-create dossier batch %d: %w", spec.Index, err)
		}
		if err := deps.Store.Adaptation.SaveCoCreateDossierBatch(batch); err != nil {
			return batches, fmt.Errorf("save co-create dossier batch %d: %w", spec.Index, err)
		}
		batches = append(batches, batch)
		emitAdaptProgress(emit, StageDossier, spec.SourceTo, manifest.ChapterCount, fmt.Sprintf("资料包第 %d/%d 批完成", spec.Index, len(specs)), nil)
	}
	return batches, nil
}

func sourceReportsForDossierBatch(spec coCreateDossierBatchSpec, reportByChapter map[int]domain.AdaptationSourceReport) ([]domain.AdaptationSourceReport, bool) {
	reports := make([]domain.AdaptationSourceReport, 0, spec.SourceTo-spec.SourceFrom+1)
	for chapter := spec.SourceFrom; chapter <= spec.SourceTo; chapter++ {
		report, ok := reportByChapter[chapter]
		if !ok {
			return nil, false
		}
		reports = append(reports, report)
	}
	return reports, true
}

type coCreateDossierBatchSpec = store.AdaptationDossierBatchSpec

func dossierBatchSpecs(manifest domain.AdaptationSourceManifest, batchSize int) []coCreateDossierBatchSpec {
	if batchSize <= 0 {
		batchSize = CoCreateDossierBatchSize
	}
	return store.AdaptationDossierBatchSpecs(manifest, batchSize, CoCreateDossierBatchRuneLimit)
}

func coCreateDossierBatchCurrent(batch *domain.AdaptationCoCreateDossierBatch, spec coCreateDossierBatchSpec) bool {
	if batch == nil {
		return false
	}
	return batch.Index == spec.Index &&
		batch.SourceFrom == spec.SourceFrom &&
		batch.SourceTo == spec.SourceTo &&
		strings.TrimSpace(batch.SourceSignature) == spec.SourceSignature &&
		strings.TrimSpace(batch.PromptVersion) == CoCreateDossierPromptVersion
}

func buildCoCreateDossierBatch(ctx context.Context, deps Deps, spec coCreateDossierBatchSpec, reports []domain.AdaptationSourceReport, totalBatches int, emit ProgressEmitter) (domain.AdaptationCoCreateDossierBatch, error) {
	userPrompt := buildCoCreateDossierBatchPrompt(spec, reports)
	var batch domain.AdaptationCoCreateDossierBatch
	var lastErr error
	regenerateAttempts := max(1, deps.structureRepairMaxAttempts())
	for attempt := 1; attempt <= regenerateAttempts; attempt++ {
		text, err := generatePlannerTextForStage(ctx, StageDossier, deps.modelForStage("source_analysis"), coCreateDossierSystemPrompt, userPrompt, coCreateDossierMaxTokens, emit, spec.Index, totalBatches, "资料包", deps.modelCallMaxAttempts())
		if err != nil {
			return domain.AdaptationCoCreateDossierBatch{}, err
		}
		batch, err = collectCoCreateDossierBatchWithRepair(ctx, deps, userPrompt, text, spec, totalBatches, emit)
		if err == nil {
			lastErr = nil
			break
		}
		lastErr = err
		if attempt >= regenerateAttempts {
			return domain.AdaptationCoCreateDossierBatch{}, err
		}
		emitAdaptProgress(emit, StageDossier, spec.Index, totalBatches, fmt.Sprintf("资料包第 %d/%d 批结构修复后仍无效，重新生成第 %d/%d 次：%v", spec.Index, totalBatches, attempt+1, regenerateAttempts, err), err)
	}
	if lastErr != nil {
		return domain.AdaptationCoCreateDossierBatch{}, lastErr
	}
	batch.Index = spec.Index
	batch.SourceFrom = spec.SourceFrom
	batch.SourceTo = spec.SourceTo
	batch.SourceSignature = spec.SourceSignature
	batch.PromptVersion = CoCreateDossierPromptVersion
	batch.GeneratedAt = time.Now().UTC().Format(time.RFC3339)
	normalizeCoCreateDossierBatch(&batch)
	return batch, nil
}

func collectCoCreateDossierBatchWithRepair(ctx context.Context, deps Deps, originalPrompt string, initialText string, spec coCreateDossierBatchSpec, totalBatches int, emit ProgressEmitter) (domain.AdaptationCoCreateDossierBatch, error) {
	text := initialText
	var lastErr error
	maxRepairAttempts := deps.structureRepairMaxAttempts()
	for attempt := 0; ; attempt++ {
		batch, err := parseCoCreateDossierBatch(text)
		if err == nil {
			return batch, nil
		}
		lastErr = err
		if attempt >= maxRepairAttempts {
			return domain.AdaptationCoCreateDossierBatch{}, lastErr
		}
		emitAdaptProgress(emit, StageDossier, spec.Index, totalBatches, fmt.Sprintf("资料包第 %d/%d 批结构无效，正在修复第 %d/%d 次：%v", spec.Index, totalBatches, attempt+1, maxRepairAttempts, lastErr), lastErr)
		repairedText, err := repairCoCreateDossierBatchText(ctx, deps, originalPrompt, text, spec, lastErr, totalBatches, emit)
		if err != nil {
			return domain.AdaptationCoCreateDossierBatch{}, err
		}
		text = repairedText
	}
}

func repairCoCreateDossierBatchText(ctx context.Context, deps Deps, originalPrompt string, previousText string, spec coCreateDossierBatchSpec, previousErr error, totalBatches int, emit ProgressEmitter) (string, error) {
	repairPrompt := buildPlannerRepairPrompt(
		fmt.Sprintf("co-create dossier batch %d", spec.Index),
		originalPrompt,
		previousText,
		previousErr,
		[]string{
			"Return exactly one JSON object and no prose.",
			"The top-level object must include useful source-dossier content in plot_phase, key_causality, plot_threads, character_arcs, world_constraints, major_characters, relationship_signals, heroine_signals, ambiguity_risks, and couple_milestones.",
			"Do not include adaptation advice, adaptation_notes, adaptation_handles, suggestions, rewrite plans, or handling instructions.",
			fmt.Sprintf("Use only source report evidence from chapters %d through %d.", spec.SourceFrom, spec.SourceTo),
			"Do not return only metadata, an empty envelope, markdown, or explanations.",
			"Keep arrays compact but non-empty when the source reports support facts.",
		},
	)
	text, err := generatePlannerTextForStage(ctx, StageDossier, deps.modelForStage("source_analysis"), coCreateDossierSystemPrompt, repairPrompt, coCreateDossierMaxTokens, emit, spec.Index, totalBatches, "资料包修复", deps.modelCallMaxAttempts())
	if err != nil {
		return "", fmt.Errorf("co-create dossier batch repair llm generate: %w", err)
	}
	return text, nil
}

func buildCoCreateDossierBatchPrompt(spec coCreateDossierBatchSpec, reports []domain.AdaptationSourceReport) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "Source chapter range: %d-%d\n", spec.SourceFrom, spec.SourceTo)
	fmt.Fprintf(&sb, "Task: extract adaptation co-create dossier facts for this range.\n\n")
	for _, report := range reports {
		fmt.Fprintf(&sb, "## Chapter %d: %s\n", report.Chapter, report.Title)
		fmt.Fprintf(&sb, "Summary: %s\n", clipText(report.Summary, 260))
		writeStringList(&sb, "Characters", report.Characters, 20, 80)
		writeStringList(&sb, "Character facts", report.CharacterFacts, 10, 120)
		writeStringList(&sb, "Key events", report.KeyEvents, 8, 120)
		writeRelationships(&sb, report.Relationships)
		writeStateChanges(&sb, report.StateChanges)
		sb.WriteString("\n")
	}
	return sb.String()
}

func parseCoCreateDossierBatch(text string) (domain.AdaptationCoCreateDossierBatch, error) {
	segments, err := extractPlannerJSONSegments(text)
	if err != nil {
		return domain.AdaptationCoCreateDossierBatch{}, err
	}
	var firstErr error
	for _, segment := range segments {
		batch, err := decodeCoCreateDossierBatchJSON([]byte(segment))
		if err == nil {
			return batch, nil
		}
		if firstErr == nil {
			firstErr = err
		}
	}
	if firstErr != nil {
		return domain.AdaptationCoCreateDossierBatch{}, firstErr
	}
	return domain.AdaptationCoCreateDossierBatch{}, fmt.Errorf("co-create dossier batch has no decodable JSON object")
}

func decodeCoCreateDossierBatchJSON(data []byte) (domain.AdaptationCoCreateDossierBatch, error) {
	var batch domain.AdaptationCoCreateDossierBatch
	if err := json.Unmarshal(data, &batch); err == nil && coCreateDossierBatchHasContent(batch) {
		return batch, nil
	}

	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(data, &envelope); err != nil {
		return domain.AdaptationCoCreateDossierBatch{}, err
	}
	for _, key := range []string{"batch", "dossier_batch", "dossierBatch", "result", "data", "output"} {
		raw, ok := envelope[key]
		if !ok {
			continue
		}
		if err := json.Unmarshal(raw, &batch); err == nil && coCreateDossierBatchHasContent(batch) {
			return batch, nil
		}
	}
	return domain.AdaptationCoCreateDossierBatch{}, fmt.Errorf("co-create dossier batch missing content")
}

func coCreateDossierBatchHasContent(batch domain.AdaptationCoCreateDossierBatch) bool {
	return strings.TrimSpace(batch.PlotPhase) != "" ||
		len(batch.KeyCausality) > 0 ||
		len(batch.PlotThreads) > 0 ||
		len(batch.CharacterArcs) > 0 ||
		len(batch.WorldConstraints) > 0 ||
		len(batch.RelationshipSignals) > 0 ||
		len(batch.HeroineSignals) > 0 ||
		len(batch.AmbiguityRisks) > 0 ||
		len(batch.CoupleMilestones) > 0
}

func normalizeCoCreateDossierBatch(batch *domain.AdaptationCoCreateDossierBatch) {
	if batch == nil {
		return
	}
	batch.PlotPhase = strings.TrimSpace(batch.PlotPhase)
	batch.KeyCausality = limitStrings(trimmedNonEmpty(batch.KeyCausality), 12)
	batch.PlotThreads = limitStrings(trimmedNonEmpty(batch.PlotThreads), 14)
	batch.CharacterArcs = limitStrings(trimmedNonEmpty(batch.CharacterArcs), 14)
	batch.WorldConstraints = limitStrings(trimmedNonEmpty(batch.WorldConstraints), 12)
	batch.MajorCharacters = limitStrings(trimmedNonEmpty(batch.MajorCharacters), 30)
	batch.RelationshipSignals = limitSignals(batch.RelationshipSignals, 16)
	batch.HeroineSignals = limitSignals(batch.HeroineSignals, 12)
	batch.AmbiguityRisks = limitRisks(batch.AmbiguityRisks, 12)
	for i := range batch.AmbiguityRisks {
		batch.AmbiguityRisks[i].Suggestion = ""
	}
	batch.CoupleMilestones = limitSignals(batch.CoupleMilestones, 10)
	batch.AdaptationNotes = nil
}

func assembleCoCreateDossier(manifest domain.AdaptationSourceManifest, batches []domain.AdaptationCoCreateDossierBatch) domain.AdaptationCoCreateDossier {
	sort.SliceStable(batches, func(i, j int) bool {
		return batches[i].Index < batches[j].Index
	})
	mainline := make([]string, 0, len(batches)*3)
	plotThreads := make([]string, 0, len(batches)*4)
	characterArcs := make([]string, 0, len(batches)*4)
	worldConstraints := make([]string, 0, len(batches)*3)
	var relationshipSignals, heroineSignals, milestones []domain.AdaptationRelationshipSignal
	var risks []domain.AdaptationRelationshipRisk
	for _, batch := range batches {
		if batch.PlotPhase != "" {
			mainline = append(mainline, fmt.Sprintf("原书第 %d-%d 章：%s", batch.SourceFrom, batch.SourceTo, batch.PlotPhase))
		}
		mainline = append(mainline, batch.KeyCausality...)
		plotThreads = append(plotThreads, batch.PlotThreads...)
		characterArcs = append(characterArcs, batch.CharacterArcs...)
		worldConstraints = append(worldConstraints, batch.WorldConstraints...)
		relationshipSignals = append(relationshipSignals, batch.RelationshipSignals...)
		heroineSignals = append(heroineSignals, batch.HeroineSignals...)
		milestones = append(milestones, batch.CoupleMilestones...)
		risks = append(risks, batch.AmbiguityRisks...)
	}

	sourceChapters := make([]domain.AdaptationDossierSourceSignature, 0, len(manifest.Chapters))
	for _, ch := range manifest.Chapters {
		sourceChapters = append(sourceChapters, domain.AdaptationDossierSourceSignature{Chapter: ch.Chapter, SHA256: ch.SHA256})
	}
	return domain.AdaptationCoCreateDossier{
		Version:            coCreateDossierVersion,
		PromptVersion:      CoCreateDossierPromptVersion,
		SourcePath:         manifest.SourcePath,
		SourceChapterCount: manifest.ChapterCount,
		SourceSignature:    store.AdaptationSourceSignature(manifest),
		BatchSize:          CoCreateDossierBatchSize,
		BatchRuneLimit:     CoCreateDossierBatchRuneLimit,
		GeneratedAt:        time.Now().UTC().Format(time.RFC3339),
		Overview:           fmt.Sprintf("原书共 %d 章，资料包按每批最多 %d 章生成；若原文字数过长，则按约 %d 字符提前拆分，覆盖全书主线、人物弧光、阵营/世界约束、关系线，以及有源书证据的女主/暧昧风险。", manifest.ChapterCount, CoCreateDossierBatchSize, CoCreateDossierBatchRuneLimit),
		Mainline:           limitStrings(dedupeStrings(mainline), 160),
		PlotThreads:        limitStrings(dedupeStrings(plotThreads), 160),
		CharacterArcs:      limitStrings(dedupeStrings(characterArcs), 160),
		WorldConstraints:   limitStrings(dedupeStrings(worldConstraints), 120),
		RelationshipMap:    limitSignals(relationshipSignals, 160),
		HeroineSignals:     limitSignals(heroineSignals, 120),
		AmbiguityRisks:     limitRisks(risks, 120),
		CoupleMilestones:   limitSignals(milestones, 120),
		Batches:            batches,
		SourceChapters:     sourceChapters,
	}
}

func sourceRangeSignature(manifest domain.AdaptationSourceManifest, from, to int) string {
	var sources []domain.AdaptationSource
	for _, ch := range manifest.Chapters {
		if ch.Chapter >= from && ch.Chapter <= to {
			sources = append(sources, ch)
		}
	}
	return store.AdaptationSourceSignature(domain.AdaptationSourceManifest{
		ChapterCount: len(sources),
		Chapters:     sources,
	})
}

func writeStringList(sb *strings.Builder, label string, values []string, maxItems, maxRunes int) {
	values = limitStrings(trimmedNonEmpty(values), maxItems)
	if len(values) == 0 {
		return
	}
	fmt.Fprintf(sb, "%s:\n", label)
	for _, value := range values {
		fmt.Fprintf(sb, "- %s\n", clipText(value, maxRunes))
	}
}

func writeRelationships(sb *strings.Builder, values []domain.RelationshipEntry) {
	if len(values) == 0 {
		return
	}
	sb.WriteString("Relationships:\n")
	for i, value := range values {
		if i >= 12 {
			break
		}
		fmt.Fprintf(sb, "- %s / %s: %s\n", value.CharacterA, value.CharacterB, clipText(value.Relation, 120))
	}
}

func writeStateChanges(sb *strings.Builder, values []domain.StateChange) {
	if len(values) == 0 {
		return
	}
	sb.WriteString("State changes:\n")
	for i, value := range values {
		if i >= 12 {
			break
		}
		if strings.TrimSpace(value.Field) != "relation" && !strings.Contains(strings.ToLower(value.Field), "relation") {
			continue
		}
		fmt.Fprintf(sb, "- %s %s: %s -> %s (%s)\n", value.Entity, value.Field, value.OldValue, value.NewValue, clipText(value.Reason, 100))
	}
}

func clipText(text string, maxRunes int) string {
	text = strings.TrimSpace(text)
	if maxRunes <= 0 {
		return text
	}
	runes := []rune(text)
	if len(runes) <= maxRunes {
		return text
	}
	return string(runes[:maxRunes]) + "..."
}

func dedupeStrings(values []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, value := range trimmedNonEmpty(values) {
		key := strings.ToLower(value)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, value)
	}
	return out
}

func limitStrings(values []string, max int) []string {
	if max > 0 && len(values) > max {
		return values[:max]
	}
	return values
}

func limitSignals(values []domain.AdaptationRelationshipSignal, max int) []domain.AdaptationRelationshipSignal {
	out := make([]domain.AdaptationRelationshipSignal, 0, len(values))
	seen := map[string]bool{}
	for _, value := range values {
		value.Summary = strings.TrimSpace(value.Summary)
		value.Type = strings.TrimSpace(value.Type)
		value.Evidence = clipText(value.Evidence, 180)
		value.Characters = limitStrings(trimmedNonEmpty(value.Characters), 8)
		if value.Summary == "" {
			continue
		}
		key := fmt.Sprintf("%v|%v|%s|%s", value.Chapters, value.Characters, strings.ToLower(value.Type), strings.ToLower(value.Summary))
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, value)
		if max > 0 && len(out) >= max {
			break
		}
	}
	return out
}

func limitRisks(values []domain.AdaptationRelationshipRisk, max int) []domain.AdaptationRelationshipRisk {
	out := make([]domain.AdaptationRelationshipRisk, 0, len(values))
	seen := map[string]bool{}
	for _, value := range values {
		value.Risk = strings.TrimSpace(value.Risk)
		value.Evidence = clipText(value.Evidence, 180)
		value.Severity = strings.TrimSpace(value.Severity)
		value.Suggestion = clipText(value.Suggestion, 180)
		value.Characters = limitStrings(trimmedNonEmpty(value.Characters), 8)
		if value.Risk == "" {
			continue
		}
		key := fmt.Sprintf("%v|%v|%s", value.Chapters, value.Characters, strings.ToLower(value.Risk))
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, value)
		if max > 0 && len(out) >= max {
			break
		}
	}
	return out
}
