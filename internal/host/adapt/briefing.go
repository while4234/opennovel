package adapt

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/modeldiag"
	"github.com/voocel/ainovel-cli/internal/store"
)

const (
	CoCreateBriefingPromptVersion = "v1"

	coCreateBriefingVersion               = 1
	coCreateIntentVersion                 = 1
	coCreateBriefingMaxTokens             = 4096
	coCreateBriefingDossierBatchGroupSize = 5
	coCreateBriefingChapterThreshold      = 320
	coCreateBriefingBatchThreshold        = 8
	coCreateBriefingSnapshotRuneLimit     = 20000
)

var heroineNamePattern = regexp.MustCompile(`(?:女主|唯一女主|第一女主)[是为叫：:\s]*([\p{Han}A-Za-z0-9_·]{2,12})`)

const coCreateBriefingSystemPrompt = `You are an intent-driven adaptation co-create analyst for very long Chinese web novels.
You read already-generated all-book dossier batches, not raw prose. Use source chapter evidence from the dossier only.

Return one JSON object:
{
  "confirmed_facts": ["facts that are clear and should not be asked back to the user"],
  "intent_relevant_risks": [{"chapters":[1],"characters":["A","B"],"risk":"risk related to this user's adaptation intent","evidence":"chapter evidence","severity":"low|medium|high","suggestion":"handling without breaking source causality"}],
  "adaptation_suggestions": ["actionable handling advice for the user's intent"],
  "decision_questions": [{
    "id":"stable short id",
    "question":"one concrete uncertainty that changes adaptation direction",
    "reason":"why this is genuinely uncertain",
    "chapters":[1],
    "evidence":"source evidence",
    "impact":"what later planning changes depending on the answer",
    "required":true,
    "options":[{"id":"a","label":"short option","description":"effect"}],
    "recommended_option_id":"a"
  }]
}

Rules:
- The output containers are fixed, but attention must follow the user's intent. Do not force harem/side-character questions unless the user asked for single-heroine cleanup or the dossier makes it directly relevant.
- Ask only questions that are genuinely uncertain and materially affect the adaptation plan.
- Do not ask generic questions like whether the user is satisfied.
- Do not ask about facts that are already clear; put those in confirmed_facts.
- Every decision question must have source evidence, impact, 2-3 concrete options, and a recommended option.
- Keep the output compact.`

func BuildCoCreateIntent(rawRequest, granularity, rewritePolicy string, wordTolerance float64) domain.AdaptationCoCreateIntent {
	intent := domain.AdaptationCoCreateIntent{
		Version:       coCreateIntentVersion,
		RawRequest:    strings.TrimSpace(rawRequest),
		Granularity:   strings.TrimSpace(granularity),
		RewritePolicy: strings.TrimSpace(rewritePolicy),
		WordTolerance: wordTolerance,
		GeneratedAt:   time.Now().UTC().Format(time.RFC3339),
	}
	intent.Goals = inferCoCreateIntentGoals(intent.RawRequest)
	intent.HeroineNames = inferHeroineNames(intent.RawRequest)
	intent.RelationshipRules = inferCoCreateRelationshipRules(intent.RawRequest)
	intent.PreserveRules = []string{
		"Preserve the source mainline, world rules, irreversible plot facts, and non-romance causality unless the user explicitly changes them.",
		"Only change relationship-facing scenes that are necessary for the user's adaptation goal.",
	}
	intent.IntentHash = CoCreateIntentHash(intent)
	return intent
}

func CoCreateIntentHash(intent domain.AdaptationCoCreateIntent) string {
	hashInput := struct {
		RawRequest        string   `json:"raw_request"`
		Granularity       string   `json:"granularity,omitempty"`
		RewritePolicy     string   `json:"rewrite_policy,omitempty"`
		WordTolerance     float64  `json:"word_tolerance,omitempty"`
		Goals             []string `json:"goals,omitempty"`
		HeroineNames      []string `json:"heroine_names,omitempty"`
		RestrictedNames   []string `json:"restricted_names,omitempty"`
		RelationshipRules []string `json:"relationship_rules,omitempty"`
		PreserveRules     []string `json:"preserve_rules,omitempty"`
	}{
		RawRequest:        strings.TrimSpace(intent.RawRequest),
		Granularity:       strings.TrimSpace(intent.Granularity),
		RewritePolicy:     strings.TrimSpace(intent.RewritePolicy),
		WordTolerance:     intent.WordTolerance,
		Goals:             trimmedNonEmpty(intent.Goals),
		HeroineNames:      trimmedNonEmpty(intent.HeroineNames),
		RestrictedNames:   trimmedNonEmpty(intent.RestrictedNames),
		RelationshipRules: trimmedNonEmpty(intent.RelationshipRules),
		PreserveRules:     trimmedNonEmpty(intent.PreserveRules),
	}
	data, _ := json.Marshal(hashInput)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func CoCreateBriefingTriggerReason(dossier domain.AdaptationCoCreateDossier) string {
	switch {
	case dossier.SourceChapterCount > coCreateBriefingChapterThreshold:
		return fmt.Sprintf("source_chapter_count>%d", coCreateBriefingChapterThreshold)
	case len(dossier.Batches) > coCreateBriefingBatchThreshold:
		return fmt.Sprintf("dossier_batches>%d", coCreateBriefingBatchThreshold)
	case estimateCoCreateDossierRunes(dossier) > coCreateBriefingSnapshotRuneLimit:
		return fmt.Sprintf("dossier_snapshot>%d_runes", coCreateBriefingSnapshotRuneLimit)
	default:
		return ""
	}
}

func EnsureCoCreateBriefing(ctx context.Context, deps Deps, intent domain.AdaptationCoCreateIntent, emit ProgressEmitter) (*domain.AdaptationCoCreateBriefing, error) {
	ctx = modeldiag.WithStore(ctx, deps.Store)
	if deps.Store == nil || deps.LLM == nil {
		return nil, fmt.Errorf("deps incomplete")
	}
	manifest, err := deps.Store.Adaptation.LoadSourceManifest()
	if err != nil || manifest == nil {
		return nil, err
	}
	dossier, err := deps.Store.Adaptation.LoadCoCreateDossier()
	if err != nil || dossier == nil {
		return nil, err
	}
	if !store.CoCreateDossierMatchesManifest(*dossier, *manifest, CoCreateDossierPromptVersion, CoCreateDossierBatchSize, CoCreateDossierBatchRuneLimit) {
		return nil, fmt.Errorf("co-create dossier missing or stale")
	}
	intent.IntentHash = CoCreateIntentHash(intent)
	if err := deps.Store.Adaptation.SaveCoCreateIntent(intent); err != nil {
		return nil, fmt.Errorf("save co-create intent: %w", err)
	}
	triggerReason := CoCreateBriefingTriggerReason(*dossier)
	if triggerReason == "" {
		return nil, nil
	}
	current, err := deps.Store.Adaptation.LoadCoCreateBriefing()
	if err != nil {
		return nil, fmt.Errorf("load co-create briefing: %w", err)
	}
	if current != nil && store.CoCreateBriefingMatches(*current, *manifest, *dossier, CoCreateBriefingPromptVersion, CoCreateDossierPromptVersion, intent.IntentHash) {
		emitAdaptProgress(emit, StageBriefing, len(dossier.Batches), len(dossier.Batches), "co-create briefing already exists", nil)
		return current, nil
	}

	specs := coCreateBriefingBatchSpecs(*dossier, intent.IntentHash)
	batches := make([]domain.AdaptationCoCreateBriefingBatch, 0, len(specs))
	emitAdaptProgress(emit, StageBriefing, 0, len(specs), "generating co-create briefing", nil)
	for _, spec := range specs {
		existing, err := deps.Store.Adaptation.LoadCoCreateBriefingBatch(spec.Index)
		if err != nil {
			return nil, fmt.Errorf("load co-create briefing batch %d: %w", spec.Index, err)
		}
		if coCreateBriefingBatchCurrent(existing, spec) {
			batches = append(batches, *existing)
			emitAdaptProgress(emit, StageBriefing, spec.Index, len(specs), fmt.Sprintf("skip co-create briefing batch %d/%d", spec.Index, len(specs)), nil)
			continue
		}
		batch, err := buildCoCreateBriefingBatch(ctx, deps, intent, spec, *dossier, len(specs), emit)
		if err != nil {
			return nil, fmt.Errorf("build co-create briefing batch %d: %w", spec.Index, err)
		}
		if err := deps.Store.Adaptation.SaveCoCreateBriefingBatch(batch); err != nil {
			return nil, fmt.Errorf("save co-create briefing batch %d: %w", spec.Index, err)
		}
		batches = append(batches, batch)
		emitAdaptProgress(emit, StageBriefing, spec.Index, len(specs), fmt.Sprintf("co-create briefing batch %d/%d done", spec.Index, len(specs)), nil)
	}
	briefing := assembleCoCreateBriefing(*manifest, *dossier, intent, triggerReason, batches)
	if err := deps.Store.Adaptation.SaveCoCreateBriefing(briefing); err != nil {
		return nil, fmt.Errorf("save co-create briefing: %w", err)
	}
	emitAdaptProgress(emit, StageBriefing, len(specs), len(specs), "co-create briefing generated", nil)
	return &briefing, nil
}

// EnsureProposalCoCreateBriefing resolves the co-create artifact at the
// proposal boundary. Once planner work exists, its upstream dependency is
// immutable: resume either reuses that exact artifact or fails closed.
func EnsureProposalCoCreateBriefing(
	ctx context.Context,
	deps Deps,
	incoming domain.AdaptationCoCreateIntent,
	emit ProgressEmitter,
) (*domain.AdaptationCoCreateBriefing, error) {
	if deps.Store == nil {
		return nil, fmt.Errorf("store is required")
	}
	manifest, err := deps.Store.Adaptation.LoadSourceManifest()
	if err != nil {
		return nil, fmt.Errorf("load adaptation source manifest: %w", err)
	}
	briefing, err := deps.Store.Adaptation.LoadCoCreateBriefing()
	if err != nil {
		return nil, fmt.Errorf("load co-create briefing: %w", err)
	}
	storedIntent, err := deps.Store.Adaptation.LoadCoCreateIntent()
	if err != nil {
		return nil, fmt.Errorf("load co-create intent for proposal: %w", err)
	}
	runtime, err := deps.Store.Adaptation.LoadProposalRuntime()
	if err != nil {
		return nil, fmt.Errorf("load proposal runtime for co-create briefing: %w", err)
	}
	if proposalRuntimeHasPlannerWork(runtime) {
		dependency, valid := coCreateDependencyFromArtifacts(manifest, storedIntent, briefing)
		if !valid || !proposalRuntimeSourceMatches(runtime, manifest) {
			return nil, fmt.Errorf("in-progress proposal's pinned co-create briefing is missing or stale; explicitly restart proposal generation")
		}
		if runtime.CoCreateDependency == nil {
			runtime.CoCreateDependency = dependency
			if err := deps.Store.Adaptation.SaveProposalRuntime(*runtime); err != nil {
				return nil, fmt.Errorf("migrate proposal co-create dependency: %w", err)
			}
		} else if !sameCoCreateDependency(runtime.CoCreateDependency, dependency) {
			return nil, fmt.Errorf("in-progress proposal's pinned co-create briefing changed; explicitly restart proposal generation")
		}
		emitAdaptProgress(emit, StageBriefing, 1, 1, "reuse co-create briefing pinned by proposal runtime", nil)
		return briefing, nil
	}
	if _, valid := coCreateDependencyFromArtifacts(manifest, storedIntent, briefing); valid {
		emitAdaptProgress(emit, StageBriefing, 1, 1, "reuse completed co-create briefing for proposal", nil)
		return briefing, nil
	}
	return EnsureCoCreateBriefing(ctx, deps, incoming, emit)
}

func proposalRuntimeHasPlannerWork(runtime *domain.AdaptationProposalRuntime) bool {
	return runtime != nil && (runtime.Skeleton != nil || len(runtime.SkeletonBatches) > 0 || len(runtime.CompletedBatches) > 0)
}

func coCreateDependencyFromArtifacts(
	manifest *domain.AdaptationSourceManifest,
	intent *domain.AdaptationCoCreateIntent,
	briefing *domain.AdaptationCoCreateBriefing,
) (*domain.AdaptationProposalCoCreateDependency, bool) {
	if manifest == nil || intent == nil || briefing == nil || strings.TrimSpace(intent.IntentHash) == "" || briefing.IntentHash != intent.IntentHash {
		return nil, false
	}
	if briefing.SourceChapterCount != manifest.ChapterCount || briefing.SourceSignature != store.AdaptationSourceSignature(*manifest) {
		return nil, false
	}
	return &domain.AdaptationProposalCoCreateDependency{
		IntentHash:            briefing.IntentHash,
		SourceSignature:       briefing.SourceSignature,
		BriefingPromptVersion: briefing.PromptVersion,
		DossierPromptVersion:  briefing.DossierPromptVersion,
	}, true
}

func sameCoCreateDependency(a, b *domain.AdaptationProposalCoCreateDependency) bool {
	return a != nil && b != nil &&
		a.IntentHash == b.IntentHash &&
		a.SourceSignature == b.SourceSignature &&
		a.BriefingPromptVersion == b.BriefingPromptVersion &&
		a.DossierPromptVersion == b.DossierPromptVersion
}

func proposalRuntimeSourceMatches(runtime *domain.AdaptationProposalRuntime, manifest *domain.AdaptationSourceManifest) bool {
	return runtime != nil && manifest != nil &&
		runtime.SourceChapterCount == manifest.ChapterCount &&
		sameSourcePath(runtime.SourcePath, manifest.SourcePath)
}

func bindProposalCoCreateDependency(deps Deps, runtime *domain.AdaptationProposalRuntime) error {
	if deps.Store == nil || runtime == nil {
		return nil
	}
	manifest, err := deps.Store.Adaptation.LoadSourceManifest()
	if err != nil {
		return fmt.Errorf("load source manifest for proposal dependency: %w", err)
	}
	intent, err := deps.Store.Adaptation.LoadCoCreateIntent()
	if err != nil {
		return fmt.Errorf("load co-create intent for proposal dependency: %w", err)
	}
	briefing, err := deps.Store.Adaptation.LoadCoCreateBriefing()
	if err != nil {
		return fmt.Errorf("load co-create briefing for proposal dependency: %w", err)
	}
	dependency, valid := coCreateDependencyFromArtifacts(manifest, intent, briefing)
	if runtime.CoCreateDependency != nil {
		if !valid || !sameCoCreateDependency(runtime.CoCreateDependency, dependency) {
			return fmt.Errorf("proposal's pinned co-create dependency changed")
		}
		return nil
	}
	if valid && proposalRuntimeSourceMatches(runtime, manifest) {
		runtime.CoCreateDependency = dependency
	}
	return nil
}

type coCreateBriefingBatchSpec struct {
	Index            int
	DossierBatchFrom int
	DossierBatchTo   int
	SourceFrom       int
	SourceTo         int
	DossierSignature string
	IntentHash       string
}

func coCreateBriefingBatchSpecs(dossier domain.AdaptationCoCreateDossier, intentHash string) []coCreateBriefingBatchSpec {
	var specs []coCreateBriefingBatchSpec
	for start, index := 0, 1; start < len(dossier.Batches); start, index = start+coCreateBriefingDossierBatchGroupSize, index+1 {
		end := start + coCreateBriefingDossierBatchGroupSize
		if end > len(dossier.Batches) {
			end = len(dossier.Batches)
		}
		group := dossier.Batches[start:end]
		specs = append(specs, coCreateBriefingBatchSpec{
			Index:            index,
			DossierBatchFrom: group[0].Index,
			DossierBatchTo:   group[len(group)-1].Index,
			SourceFrom:       group[0].SourceFrom,
			SourceTo:         group[len(group)-1].SourceTo,
			DossierSignature: coCreateBriefingDossierSignature(group),
			IntentHash:       strings.TrimSpace(intentHash),
		})
	}
	return specs
}

func coCreateBriefingDossierSignature(batches []domain.AdaptationCoCreateDossierBatch) string {
	data, _ := json.Marshal(batches)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func coCreateBriefingBatchCurrent(batch *domain.AdaptationCoCreateBriefingBatch, spec coCreateBriefingBatchSpec) bool {
	if batch == nil {
		return false
	}
	return batch.Index == spec.Index &&
		batch.DossierBatchFrom == spec.DossierBatchFrom &&
		batch.DossierBatchTo == spec.DossierBatchTo &&
		batch.SourceFrom == spec.SourceFrom &&
		batch.SourceTo == spec.SourceTo &&
		strings.TrimSpace(batch.DossierSignature) == spec.DossierSignature &&
		strings.TrimSpace(batch.PromptVersion) == CoCreateBriefingPromptVersion &&
		strings.TrimSpace(batch.IntentHash) == spec.IntentHash
}

func buildCoCreateBriefingBatch(ctx context.Context, deps Deps, intent domain.AdaptationCoCreateIntent, spec coCreateBriefingBatchSpec, dossier domain.AdaptationCoCreateDossier, totalBatches int, emit ProgressEmitter) (domain.AdaptationCoCreateBriefingBatch, error) {
	userPrompt := buildCoCreateBriefingBatchPrompt(intent, spec, dossier)
	var batch domain.AdaptationCoCreateBriefingBatch
	var lastErr error
	regenerateAttempts := max(1, deps.structureRepairMaxAttempts())
	for attempt := 1; attempt <= regenerateAttempts; attempt++ {
		text, err := generatePlannerTextForStage(ctx, StageBriefing, deps.modelForStage("co_create"), coCreateBriefingSystemPrompt, userPrompt, coCreateBriefingMaxTokens, emit, spec.Index, totalBatches, "前置摘要", deps.modelCallMaxAttempts())
		if err != nil {
			return domain.AdaptationCoCreateBriefingBatch{}, err
		}
		batch, err = collectCoCreateBriefingBatchWithRepair(ctx, deps, userPrompt, text, spec, totalBatches, emit)
		if err == nil {
			lastErr = nil
			break
		}
		lastErr = err
		if attempt >= regenerateAttempts {
			return domain.AdaptationCoCreateBriefingBatch{}, err
		}
		emitAdaptProgress(emit, StageBriefing, spec.Index, totalBatches, fmt.Sprintf("前置摘要第 %d/%d 批结构修复后仍无效，重新生成第 %d/%d 次：%v", spec.Index, totalBatches, attempt+1, regenerateAttempts, err), err)
	}
	if lastErr != nil {
		return domain.AdaptationCoCreateBriefingBatch{}, lastErr
	}
	batch.Index = spec.Index
	batch.DossierBatchFrom = spec.DossierBatchFrom
	batch.DossierBatchTo = spec.DossierBatchTo
	batch.SourceFrom = spec.SourceFrom
	batch.SourceTo = spec.SourceTo
	batch.DossierSignature = spec.DossierSignature
	batch.PromptVersion = CoCreateBriefingPromptVersion
	batch.IntentHash = spec.IntentHash
	batch.GeneratedAt = time.Now().UTC().Format(time.RFC3339)
	normalizeCoCreateBriefingBatch(&batch)
	return batch, nil
}

func collectCoCreateBriefingBatchWithRepair(ctx context.Context, deps Deps, originalPrompt string, initialText string, spec coCreateBriefingBatchSpec, totalBatches int, emit ProgressEmitter) (domain.AdaptationCoCreateBriefingBatch, error) {
	text := initialText
	var lastErr error
	maxRepairAttempts := deps.structureRepairMaxAttempts()
	for attempt := 0; ; attempt++ {
		batch, err := parseCoCreateBriefingBatch(text)
		if err == nil {
			return batch, nil
		}
		lastErr = err
		if attempt >= maxRepairAttempts {
			return domain.AdaptationCoCreateBriefingBatch{}, lastErr
		}
		emitAdaptProgress(emit, StageBriefing, spec.Index, totalBatches, fmt.Sprintf("前置摘要第 %d/%d 批结构无效，正在修复第 %d/%d 次：%v", spec.Index, totalBatches, attempt+1, maxRepairAttempts, lastErr), lastErr)
		repairedText, err := repairCoCreateBriefingBatchText(ctx, deps, originalPrompt, text, spec, lastErr, totalBatches, emit)
		if err != nil {
			return domain.AdaptationCoCreateBriefingBatch{}, err
		}
		text = repairedText
	}
}

func repairCoCreateBriefingBatchText(ctx context.Context, deps Deps, originalPrompt string, previousText string, spec coCreateBriefingBatchSpec, previousErr error, totalBatches int, emit ProgressEmitter) (string, error) {
	repairPrompt := buildPlannerRepairPrompt(
		fmt.Sprintf("co-create briefing batch %d", spec.Index),
		originalPrompt,
		previousText,
		previousErr,
		[]string{
			"Return exactly one JSON object and no markdown, prose, or explanation.",
			"The object must include useful briefing content in at least one of confirmed_facts, intent_relevant_risks, adaptation_suggestions, or decision_questions.",
			"Do not return only metadata, an empty envelope, markdown, or explanatory text.",
			fmt.Sprintf("Use only dossier evidence from dossier batches %d through %d and source chapters %d through %d.", spec.DossierBatchFrom, spec.DossierBatchTo, spec.SourceFrom, spec.SourceTo),
			"Preserve the user's adaptation intent, granularity, rewrite policy, and source causality.",
			"When decision_questions are present, each question must keep clickable decision fields: id, question, reason, chapters, evidence, impact, required, options, and recommended_option_id.",
			"Every decision question must include at least two concrete options with stable ids and short labels.",
			"Keep arrays compact, source-grounded, and directly relevant to the user's intent.",
		},
	)
	text, err := generatePlannerTextForStage(ctx, StageBriefing, deps.modelForStage("co_create"), coCreateBriefingSystemPrompt, repairPrompt, coCreateBriefingMaxTokens, emit, spec.Index, totalBatches, "前置摘要修复", deps.modelCallMaxAttempts())
	if err != nil {
		return "", fmt.Errorf("co-create briefing batch repair llm generate: %w", err)
	}
	return text, nil
}

func buildCoCreateBriefingBatchPrompt(intent domain.AdaptationCoCreateIntent, spec coCreateBriefingBatchSpec, dossier domain.AdaptationCoCreateDossier) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "Source chapters: %d-%d\n", spec.SourceFrom, spec.SourceTo)
	fmt.Fprintf(&sb, "Dossier batches: %d-%d\n\n", spec.DossierBatchFrom, spec.DossierBatchTo)
	sb.WriteString("## User adaptation intent\n")
	writeBriefingIntent(&sb, intent)
	sb.WriteString("\n## Dossier batch excerpts\n")
	for _, batch := range dossier.Batches {
		if batch.Index < spec.DossierBatchFrom || batch.Index > spec.DossierBatchTo {
			continue
		}
		fmt.Fprintf(&sb, "\n### Dossier batch %d, source chapters %d-%d\n", batch.Index, batch.SourceFrom, batch.SourceTo)
		if batch.PlotPhase != "" {
			fmt.Fprintf(&sb, "Plot phase: %s\n", clipText(batch.PlotPhase, 260))
		}
		writeStringList(&sb, "Key causality", batch.KeyCausality, 8, 140)
		writeStringList(&sb, "Plot threads", batch.PlotThreads, 8, 140)
		writeStringList(&sb, "Character arcs", batch.CharacterArcs, 8, 140)
		writeStringList(&sb, "World constraints", batch.WorldConstraints, 8, 140)
		writeBriefingSignals(&sb, "Relationship signals", batch.RelationshipSignals, 10)
		writeBriefingSignals(&sb, "Heroine signals", batch.HeroineSignals, 10)
		writeBriefingRisks(&sb, "Ambiguity risks", batch.AmbiguityRisks, 10)
		writeBriefingSignals(&sb, "Couple milestones", batch.CoupleMilestones, 8)
	}
	return sb.String()
}

func writeBriefingIntent(sb *strings.Builder, intent domain.AdaptationCoCreateIntent) {
	fmt.Fprintf(sb, "Raw request: %s\n", clipText(intent.RawRequest, 1200))
	fmt.Fprintf(sb, "Mode: granularity=%s rewrite_policy=%s word_tolerance=%.3f\n", intent.Granularity, intent.RewritePolicy, intent.WordTolerance)
	writeStringList(sb, "Inferred goals", intent.Goals, 12, 120)
	writeStringList(sb, "Heroine names", intent.HeroineNames, 8, 80)
	writeStringList(sb, "Restricted or sensitive names", intent.RestrictedNames, 8, 80)
	writeStringList(sb, "Relationship rules", intent.RelationshipRules, 12, 140)
	writeStringList(sb, "Preserve rules", intent.PreserveRules, 8, 160)
}

func writeBriefingSignals(sb *strings.Builder, label string, values []domain.AdaptationRelationshipSignal, max int) {
	values = limitSignals(values, max)
	if len(values) == 0 {
		return
	}
	fmt.Fprintf(sb, "%s:\n", label)
	for _, value := range values {
		fmt.Fprintf(sb, "- ch=%v chars=%s type=%s summary=%s evidence=%s\n", value.Chapters, strings.Join(value.Characters, "/"), value.Type, clipText(value.Summary, 140), clipText(value.Evidence, 120))
	}
}

func writeBriefingRisks(sb *strings.Builder, label string, values []domain.AdaptationRelationshipRisk, max int) {
	values = limitRisks(values, max)
	if len(values) == 0 {
		return
	}
	fmt.Fprintf(sb, "%s:\n", label)
	for _, value := range values {
		fmt.Fprintf(sb, "- ch=%v chars=%s severity=%s risk=%s evidence=%s suggestion=%s\n", value.Chapters, strings.Join(value.Characters, "/"), value.Severity, clipText(value.Risk, 140), clipText(value.Evidence, 120), clipText(value.Suggestion, 120))
	}
}

func parseCoCreateBriefingBatch(text string) (domain.AdaptationCoCreateBriefingBatch, error) {
	segments, err := extractPlannerJSONSegments(text)
	if err != nil {
		return domain.AdaptationCoCreateBriefingBatch{}, err
	}
	var firstErr error
	for _, segment := range segments {
		batch, err := decodeCoCreateBriefingBatchJSON([]byte(segment))
		if err == nil {
			return batch, nil
		}
		if firstErr == nil {
			firstErr = err
		}
	}
	if firstErr != nil {
		return domain.AdaptationCoCreateBriefingBatch{}, firstErr
	}
	return domain.AdaptationCoCreateBriefingBatch{}, fmt.Errorf("co-create briefing batch has no decodable JSON object")
}

func decodeCoCreateBriefingBatchJSON(data []byte) (domain.AdaptationCoCreateBriefingBatch, error) {
	var batch domain.AdaptationCoCreateBriefingBatch
	if err := json.Unmarshal(data, &batch); err == nil && coCreateBriefingBatchHasContent(batch) {
		return batch, nil
	}

	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(data, &envelope); err != nil {
		return domain.AdaptationCoCreateBriefingBatch{}, err
	}
	for _, key := range []string{"briefing_batch", "briefingBatch", "batch", "result", "data", "output"} {
		raw, ok := envelope[key]
		if !ok {
			continue
		}
		if err := json.Unmarshal(raw, &batch); err == nil && coCreateBriefingBatchHasContent(batch) {
			return batch, nil
		}
	}
	return domain.AdaptationCoCreateBriefingBatch{}, fmt.Errorf("co-create briefing batch missing content")
}

func coCreateBriefingBatchHasContent(batch domain.AdaptationCoCreateBriefingBatch) bool {
	return len(batch.ConfirmedFacts) > 0 ||
		len(batch.IntentRelevantRisks) > 0 ||
		len(batch.AdaptationSuggestions) > 0 ||
		len(batch.DecisionQuestions) > 0
}

func normalizeCoCreateBriefingBatch(batch *domain.AdaptationCoCreateBriefingBatch) {
	if batch == nil {
		return
	}
	batch.ConfirmedFacts = limitStrings(dedupeStrings(batch.ConfirmedFacts), 24)
	batch.IntentRelevantRisks = limitBriefingRisks(batch.IntentRelevantRisks, 20)
	batch.AdaptationSuggestions = limitStrings(dedupeStrings(batch.AdaptationSuggestions), 20)
	batch.DecisionQuestions = normalizeBriefingDecisions(batch.DecisionQuestions, fmt.Sprintf("b%02d", batch.Index), 8)
}

func assembleCoCreateBriefing(
	manifest domain.AdaptationSourceManifest,
	dossier domain.AdaptationCoCreateDossier,
	intent domain.AdaptationCoCreateIntent,
	triggerReason string,
	batches []domain.AdaptationCoCreateBriefingBatch,
) domain.AdaptationCoCreateBriefing {
	sort.SliceStable(batches, func(i, j int) bool {
		return batches[i].Index < batches[j].Index
	})
	var facts, suggestions []string
	var risks []domain.AdaptationBriefingRisk
	var decisions []domain.AdaptationBriefingDecision
	for _, batch := range batches {
		facts = append(facts, batch.ConfirmedFacts...)
		risks = append(risks, batch.IntentRelevantRisks...)
		suggestions = append(suggestions, batch.AdaptationSuggestions...)
		decisions = append(decisions, batch.DecisionQuestions...)
	}
	decisions = normalizeBriefingDecisions(decisions, "q", 32)
	return domain.AdaptationCoCreateBriefing{
		Version:               coCreateBriefingVersion,
		PromptVersion:         CoCreateBriefingPromptVersion,
		SourceSignature:       store.AdaptationSourceSignature(manifest),
		SourceChapterCount:    manifest.ChapterCount,
		DossierPromptVersion:  CoCreateDossierPromptVersion,
		DossierBatchCount:     len(dossier.Batches),
		IntentHash:            intent.IntentHash,
		GeneratedAt:           time.Now().UTC().Format(time.RFC3339),
		TriggerReason:         triggerReason,
		Overview:              fmt.Sprintf("Intent-driven briefing generated from %d dossier batches for %d source chapters.", len(dossier.Batches), manifest.ChapterCount),
		ConfirmedFacts:        limitStrings(dedupeStrings(facts), 160),
		IntentRelevantRisks:   limitBriefingRisks(risks, 120),
		AdaptationSuggestions: limitStrings(dedupeStrings(suggestions), 120),
		Decisions:             decisions,
		Batches:               batches,
	}
}

func PendingCoCreateBriefingDecisions(briefing *domain.AdaptationCoCreateBriefing) []domain.AdaptationBriefingDecision {
	if briefing == nil {
		return nil
	}
	resolved := make(map[string]struct{}, len(briefing.ResolvedDecisions))
	for _, item := range briefing.ResolvedDecisions {
		if id := strings.TrimSpace(item.DecisionID); id != "" {
			resolved[id] = struct{}{}
		}
	}
	var pending []domain.AdaptationBriefingDecision
	for _, decision := range briefing.Decisions {
		id := strings.TrimSpace(decision.ID)
		if id == "" || !decision.Required {
			continue
		}
		if _, ok := resolved[id]; ok {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(decision.Status), "resolved") {
			continue
		}
		pending = append(pending, decision)
	}
	return pending
}

func limitBriefingRisks(values []domain.AdaptationBriefingRisk, max int) []domain.AdaptationBriefingRisk {
	out := make([]domain.AdaptationBriefingRisk, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value.Risk = clipText(strings.TrimSpace(value.Risk), 220)
		value.Evidence = clipText(strings.TrimSpace(value.Evidence), 180)
		value.Suggestion = clipText(strings.TrimSpace(value.Suggestion), 180)
		value.Severity = strings.TrimSpace(value.Severity)
		value.Characters = limitStrings(trimmedNonEmpty(value.Characters), 8)
		value.Chapters = normalizeChapterRefs(value.Chapters)
		if value.Risk == "" {
			continue
		}
		key := fmt.Sprintf("%v|%s|%s", value.Chapters, strings.Join(value.Characters, "/"), value.Risk)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, value)
		if max > 0 && len(out) >= max {
			break
		}
	}
	return out
}

func normalizeBriefingDecisions(values []domain.AdaptationBriefingDecision, prefix string, max int) []domain.AdaptationBriefingDecision {
	out := make([]domain.AdaptationBriefingDecision, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for idx, value := range values {
		value.ID = strings.TrimSpace(value.ID)
		if value.ID == "" {
			value.ID = fmt.Sprintf("%s-%02d", prefix, idx+1)
		}
		value.Question = clipText(strings.TrimSpace(value.Question), 180)
		value.Reason = clipText(strings.TrimSpace(value.Reason), 180)
		value.Evidence = clipText(strings.TrimSpace(value.Evidence), 180)
		value.Impact = clipText(strings.TrimSpace(value.Impact), 180)
		value.RecommendedOptionID = strings.TrimSpace(value.RecommendedOptionID)
		value.Status = strings.TrimSpace(value.Status)
		value.Chapters = normalizeChapterRefs(value.Chapters)
		value.Options = normalizeDecisionOptions(value.Options)
		if !validBriefingDecision(value) {
			continue
		}
		if value.Status == "" {
			value.Status = "pending"
		}
		if !value.Required {
			value.Required = true
		}
		if value.RecommendedOptionID == "" && len(value.Options) > 0 {
			value.RecommendedOptionID = value.Options[0].ID
		}
		key := strings.ToLower(value.ID)
		if _, ok := seen[key]; ok {
			value.ID = fmt.Sprintf("%s-%02d", prefix, len(out)+1)
			key = strings.ToLower(value.ID)
		}
		seen[key] = struct{}{}
		out = append(out, value)
		if max > 0 && len(out) >= max {
			break
		}
	}
	return out
}

func normalizeDecisionOptions(values []domain.AdaptationDecisionOption) []domain.AdaptationDecisionOption {
	out := make([]domain.AdaptationDecisionOption, 0, len(values))
	for idx, value := range values {
		value.ID = strings.TrimSpace(value.ID)
		if value.ID == "" {
			value.ID = string(rune('a' + idx))
		}
		value.Label = clipText(strings.TrimSpace(value.Label), 60)
		value.Description = clipText(strings.TrimSpace(value.Description), 120)
		if value.Label == "" {
			continue
		}
		out = append(out, value)
		if len(out) >= 3 {
			break
		}
	}
	return out
}

func validBriefingDecision(value domain.AdaptationBriefingDecision) bool {
	if value.Question == "" || value.Evidence == "" || value.Impact == "" || len(value.Options) < 2 {
		return false
	}
	question := strings.ToLower(value.Question)
	return !strings.Contains(question, "是否符合") &&
		!strings.Contains(question, "满意") &&
		!strings.Contains(question, "需要调整")
}

func normalizeChapterRefs(values []int) []int {
	seen := make(map[int]struct{}, len(values))
	out := make([]int, 0, len(values))
	for _, value := range values {
		if value <= 0 {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
		if len(out) >= 12 {
			break
		}
	}
	sort.Ints(out)
	return out
}

func estimateCoCreateDossierRunes(dossier domain.AdaptationCoCreateDossier) int {
	data, _ := json.Marshal(dossier)
	return len([]rune(string(data)))
}

func inferCoCreateIntentGoals(raw string) []string {
	text := strings.TrimSpace(raw)
	lower := strings.ToLower(text)
	var goals []string
	if strings.Contains(text, "女主") || strings.Contains(text, "感情") || strings.Contains(text, "互动") {
		goals = append(goals, "increase heroine presence and relationship interactions only where source context supports them")
	}
	if strings.Contains(text, "单女主") || strings.Contains(text, "后宫") || strings.Contains(text, "暧昧") || strings.Contains(text, "身体接触") || strings.Contains(lower, "harem") {
		goals = append(goals, "enforce strict single-heroine relationship boundaries and remove side-character ambiguity")
	}
	if strings.Contains(text, "中期") && strings.Contains(text, "情侣") {
		goals = append(goals, "plan the main couple to become a couple around the middle if the source never establishes it")
	}
	if len(goals) == 0 {
		goals = append(goals, "clarify adaptation goals while preserving source causality")
	}
	return dedupeStrings(goals)
}

func inferCoCreateRelationshipRules(raw string) []string {
	text := strings.TrimSpace(raw)
	var rules []string
	if strings.Contains(text, "循序渐进") || strings.Contains(text, "不要突兀") {
		rules = append(rules, "Relationship additions must progress gradually and never appear as isolated forced scenes.")
	}
	if strings.Contains(text, "成为情侣") || strings.Contains(text, "情侣") {
		rules = append(rules, "Do not move a clear source couple milestone earlier unless the user explicitly asks; before that point, keep additions ambiguous or slow-burn.")
	}
	if strings.Contains(text, "身体接触") || strings.Contains(text, "暧昧") || strings.Contains(text, "单女主") || strings.Contains(text, "后宫") {
		rules = append(rules, "Treat side-character romance, confession, intimate body contact, and emotional dependency as risks when strict single-heroine intent applies.")
	}
	return dedupeStrings(rules)
}

func inferHeroineNames(raw string) []string {
	var names []string
	for _, match := range heroineNamePattern.FindAllStringSubmatch(raw, -1) {
		if len(match) < 2 {
			continue
		}
		name := strings.Trim(match[1], " ，。；;、,. ")
		if name != "" {
			names = append(names, name)
		}
	}
	return limitStrings(dedupeStrings(names), 8)
}
