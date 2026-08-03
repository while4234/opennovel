package adapt

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/globalprompt"
	"github.com/voocel/ainovel-cli/internal/host/imp"
	"github.com/voocel/ainovel-cli/internal/modeldiag"
	"github.com/voocel/ainovel-cli/internal/modelprofile"
	"github.com/voocel/ainovel-cli/internal/promptcompile"
	"github.com/voocel/ainovel-cli/internal/retrypolicy"
	"github.com/voocel/ainovel-cli/internal/store"
)

const DefaultWordTolerance = 0.15

const (
	adaptationPlannerPromptName              = "adaptation-planner"
	adaptationPlannerPromptVersion           = "v1"
	adaptationPlannerMaxTokens               = 8192
	adaptationPlannerSkeletonMaxTokens       = 4096
	adaptationPlannerChunkedMinChapters      = 18
	adaptationPlannerRecommendedBatchMax     = 4
	adaptationPlannerContinuityChapterMax    = adaptationPlannerRecommendedBatchMax * 2
	adaptationGeneratedOutlineBatchMax       = adaptationPlannerRecommendedBatchMax
	adaptationPlannerSourceMapExpansionMax   = 6
	adaptationPlannerSourceChunkedMin        = adaptationPlannerRecommendedBatchMax * 2
	adaptationPlannerTargetChapterMax        = 5000
	adaptationPlannerModelChapterTargetRunes = domain.AdaptationModelChapterTargetRunes
	adaptationPlannerModelChapterMaxRunes    = domain.AdaptationModelChapterMaxRunes
	adaptationPlannerModelChapterTolerance   = domain.AdaptationModelChapterTolerance
	adaptationPlannerRevisionBatchMax        = 8
	adaptationPlannerRevisionExpansionMax    = 12
	adaptationPlannerRepairMaxAttempts       = 2
	adaptationPlannerBudgetQualityAttempts   = 2
	// adaptationOutlineAuditRetryDefaultAttempts is intentionally distinct
	// from structural JSON repair even though both defaults are presently two.
	adaptationOutlineAuditRetryDefaultAttempts = 2
	adaptationVolumeBudgetRepairNeighborMax    = 2
	adaptationVolumeBudgetRepairReportMax      = 12
	adaptationVolumeBudgetRepairChapterMax     = 24
	adaptationPlannerGenerateMaxAttempts       = retrypolicy.MaxAttempts
	// Relay backends can surface a temporary upstream rejection as the bare
	// message "authorization failed". Give that ambiguous response a short
	// retry budget without treating explicit invalid-token/401 failures as
	// transient or spending the full provider retry budget.
	adaptationPlannerAuthorizationMaxAttempts = 3
	adaptationProposalRuntimeVersion          = 2
	adaptationProposalRuntimeLegacyVersion    = 1
	adaptationPlannerDefaultInputLimitBytes   = 60 * 1024
	adaptationPlannerAuditInputLimitBytes     = 128 * 1024
	adaptationSourceFoundationVersion         = 4
	adaptationSourceFoundationPromptVersion   = "source-foundation-merge-v3"
	adaptationSourceAnalyzerVersion           = 2
	adaptationSourceAnalyzerPromptVersion     = "source-chapter-analyzer-v2"
	sourceFoundationBatchKindReports          = "reports"
	sourceFoundationBatchKindSummary          = "summary"
	sourceFoundationBatchKindAssembled        = "assembled-v2"
)

const plannerBudgetDeviationAcceptedNote = "budget_deviation_accepted:source_range_capacity_reviewed"

const (
	plannerBudgetDecisionBalanced        = "balanced"
	plannerBudgetDecisionCompressOrMerge = "compress_or_merge"
	plannerBudgetDecisionExpandOrSplit   = "expand_or_split"
)

var plannerRetrySleep = retrypolicy.Wait

var errPlannerSourceMapMultipleJSON = errors.New("ambiguous planner source-map skeleton: multiple complete JSON objects found")
var errPlannerProposalMultipleJSON = errors.New("ambiguous planner proposal: multiple complete JSON objects found")

var (
	targetChapterRangePattern        = regexp.MustCompile(`(\d{1,3})\s*(?:[-~～—–－至到]|\s+)\s*(\d{1,3})\s*(?:个)?(?:章节|章)`)
	targetChapterSinglePattern       = regexp.MustCompile(`(\d{1,3})\s*(多|余|左右|上下)?\s*(?:个)?(?:章节|章)`)
	targetChapterChineseLoosePattern = regexp.MustCompile(`([一二两三四五六七八九])([一二两三四五六七八九])十\s*(?:个)?(?:章节|章)`)
	targetChapterChinesePattern      = regexp.MustCompile(`([一二两三四五六七八九十百]{1,8})(多|余|左右|上下)?\s*(?:个)?(?:章节|章)`)
)

type Deps struct {
	Store                                          *store.Store
	LLM                                            imp.LLMChat
	Auditor                                        imp.LLMChat
	Prompts                                        Prompts
	ModelCallMaxAttempts                           int
	StructureRepairMaxAttempts                     int
	BudgetQualityMaxAttempts                       int
	AdaptationOutlineAuditRetryMaxAttempts         int
	ModelCallMaxAttemptsProvider                   func() int
	StructureRepairMaxAttemptsProvider             func() int
	BudgetQualityMaxAttemptsProvider               func() int
	AdaptationOutlineAuditRetryMaxAttemptsProvider func() int
	PromptTokenCounter                             promptcompile.TokenCounter
	ModelName                                      string
	ModelForStage                                  func(string) imp.LLMChat
	ConfirmationFailpoint                          func(string) error
}

func (d Deps) foundationMergeBatchRunes() int {
	return modelprofile.Resolve(d.ModelName).FoundationMergeBatchRunes
}

func (d Deps) modelForStage(stage string) imp.LLMChat {
	if d.ModelForStage != nil {
		if model := d.ModelForStage(stage); model != nil {
			return model
		}
	}
	return d.LLM
}

func (d Deps) detailAuditModel() imp.LLMChat {
	if d.Auditor != nil {
		return d.Auditor
	}
	// Non-Host callers historically supplied one model only. Keep that API
	// usable, while the Web Host always injects the independent auditor route.
	return d.LLM
}

func RunSource(ctx context.Context, deps Deps, opts Options) (<-chan Event, error) {
	ctx = modeldiag.WithStore(ctx, deps.Store)
	if deps.Store == nil || deps.LLM == nil {
		return nil, fmt.Errorf("deps incomplete")
	}
	if strings.TrimSpace(opts.SourcePath) == "" {
		return nil, fmt.Errorf("source path is required")
	}

	events := make(chan Event, 32)
	go func() {
		defer close(events)
		emit := func(stage Stage, current, total int, msg string, err error) {
			ev := Event{Time: time.Now(), Stage: stage, Current: current, Total: total, Message: msg, Err: err}
			select {
			case events <- ev:
			case <-ctx.Done():
			}
		}
		if err := PrepareSource(ctx, deps, opts.SourcePath, emit); err != nil {
			emit(StageError, 0, 0, "改编源书分析失败", err)
			return
		}
	}()
	return events, nil
}

func PrepareSource(ctx context.Context, deps Deps, sourcePath string, emit func(Stage, int, int, string, error)) error {
	ctx = modeldiag.WithStore(ctx, deps.Store)
	if deps.Store == nil || deps.LLM == nil {
		return fmt.Errorf("deps incomplete")
	}
	if emit == nil {
		emit = func(Stage, int, int, string, error) {}
	}
	sourcePath = strings.TrimSpace(sourcePath)
	if sourcePath == "" {
		return fmt.Errorf("source path is required")
	}
	absPath, err := filepath.Abs(sourcePath)
	if err == nil {
		sourcePath = absPath
	}

	if handled, err := ensurePreparedSourceDossierIfReady(ctx, deps, sourcePath, emit); err != nil {
		return err
	} else if handled {
		return nil
	}

	emit(StageSplitting, 0, 0, "切分原文章节...", nil)
	chapters, err := imp.SplitFile(sourcePath)
	if err != nil {
		return fmt.Errorf("split source: %w", err)
	}
	if len(chapters) == 0 {
		return fmt.Errorf("未识别到任何章节，请确认文件为分章小说文本")
	}
	total := len(chapters)
	emit(StageSplitting, 0, total, fmt.Sprintf("原文切分完成：%d 章", total), nil)

	manifest, sourceChanged, err := ensureSourceSnapshot(deps.Store.Adaptation, sourcePath, chapters)
	if err != nil {
		return err
	}
	if !sourceChanged {
		emit(StageSplitting, total, total, "源书快照匹配，继续使用已有分析产物", nil)
	}
	reportsChanged, err := repairReusableSourceReports(deps.Store.Adaptation, manifest, emit)
	if err != nil {
		return err
	}
	if reports, err := deps.Store.Adaptation.LoadSourceReports(); err != nil {
		return fmt.Errorf("load source reports for co-create dossier batches: %w", err)
	} else if _, err := ensureCoCreateDossierBatches(ctx, deps, manifest, reports, emit); err != nil {
		return fmt.Errorf("ensure co-create dossier batches: %w", err)
	}

	for i, ch := range chapters {
		if err := ctx.Err(); err != nil {
			return err
		}
		chapterNum := i + 1
		source := manifest.Chapters[i]
		existing, err := deps.Store.Adaptation.LoadSourceReport(chapterNum)
		if err != nil {
			return fmt.Errorf("load source report %d: %w", chapterNum, err)
		}
		if currentSourceReport(existing, source.SHA256, deps.Prompts.Analyzer) {
			emit(StageChapter, chapterNum, total, fmt.Sprintf("跳过第 %d/%d 章，单章分析已完成：%s", chapterNum, total, ch.Title), nil)
			continue
		}
		emit(StageChapter, chapterNum, total, fmt.Sprintf("分析原文第 %d/%d 章：%s", chapterNum, total, ch.Title), nil)
		analysis, err := analyzeSourceChapterInSceneBatches(ctx, deps, chapterNum, ch.Title, ch.Content, total, emit)
		if err != nil {
			return fmt.Errorf("analyze source chapter %d: %w", chapterNum, err)
		}
		report := toSourceReport(chapterNum, ch.Title, analysis)
		report.SourceSHA256 = source.SHA256
		report.AnalyzerVersion = adaptationSourceAnalyzerVersion
		report.AnalyzerSignature = sourceAnalyzerPromptSignature(deps.Prompts.Analyzer)
		if err := deps.Store.Adaptation.SaveSourceReport(report); err != nil {
			return fmt.Errorf("save source report %d: %w", chapterNum, err)
		}
		reportsChanged = true
		if reports, err := deps.Store.Adaptation.LoadSourceReports(); err == nil {
			_ = deps.Store.Adaptation.SaveSourceReports(reports)
		}
		if shouldRefreshCoCreateDossierBatches(chapterNum, manifest) {
			reports, err := deps.Store.Adaptation.LoadSourceReports()
			if err != nil {
				return fmt.Errorf("load source reports for co-create dossier batches: %w", err)
			}
			if _, err := ensureCoCreateDossierBatches(ctx, deps, manifest, reports, emit); err != nil {
				return fmt.Errorf("ensure co-create dossier batches: %w", err)
			}
		}
	}
	reports, err := deps.Store.Adaptation.LoadCompleteSourceReports()
	if err != nil {
		return fmt.Errorf("load complete source reports: %w", err)
	}
	if len(reports) != total {
		return fmt.Errorf("source reports incomplete: got %d, want %d", len(reports), total)
	}
	if err := deps.Store.Adaptation.SaveSourceReports(reports); err != nil {
		return fmt.Errorf("save source reports: %w", err)
	}
	foundation, err := deps.Store.Adaptation.LoadSourceFoundation()
	if err != nil {
		return fmt.Errorf("load source foundation: %w", err)
	}
	shouldMergeFoundation := true
	if foundation != nil && !sourceChanged && !reportsChanged {
		reportSignature := sourceReportsSignature(reports)
		promptSignature := sourceFoundationPromptSignature(deps.Prompts.FoundationMerge)
		shouldMergeFoundation = !sourceFoundationCurrent(foundation, manifest, reportSignature, promptSignature, deps.foundationMergeBatchRunes())
	}
	if !shouldMergeFoundation {
		emit(StageFoundation, total, total, "源书 foundation 已存在，跳过聚合", nil)
	} else {
		emit(StageFoundation, total, total, "聚合逐章事实，生成源书 foundation...", nil)
		fr, err := mergeSourceFoundationResumable(ctx, deps, manifest, reports, deps.foundationMergeBatchRunes(), emit)
		if err != nil {
			return fmt.Errorf("merge source foundation: %w", err)
		}
		foundation := sourceFoundationWithMetadata(toSourceFoundation(fr), manifest, reports, deps.Prompts.FoundationMerge, deps.foundationMergeBatchRunes())
		foundation, _, err = domain.ApplyAdaptationSourceCharacterPolicy(foundation, reports)
		if err != nil {
			return fmt.Errorf("apply formal source character policy: %w", err)
		}
		if err := deps.Store.Adaptation.SaveSourceFoundation(foundation); err != nil {
			return fmt.Errorf("save source foundation: %w", err)
		}
	}
	if _, err := EnsureCoCreateDossier(ctx, deps, manifest, reports, emit); err != nil {
		return fmt.Errorf("ensure co-create dossier: %w", err)
	}
	emit(StageDone, total, total, fmt.Sprintf("原书分析完成：%d 章快照已保存", total), nil)
	return nil
}

func ensurePreparedSourceDossierIfReady(ctx context.Context, deps Deps, sourcePath string, emit func(Stage, int, int, string, error)) (bool, error) {
	manifest, err := deps.Store.Adaptation.LoadSourceManifest()
	if err != nil {
		return false, fmt.Errorf("load source manifest: %w", err)
	}
	if manifest == nil || manifest.ChapterCount <= 0 || !sameSourcePath(manifest.SourcePath, sourcePath) {
		return false, nil
	}
	reports, err := deps.Store.Adaptation.LoadCompleteSourceReports()
	if err != nil {
		return false, fmt.Errorf("load complete source reports: %w", err)
	}
	if len(reports) != manifest.ChapterCount {
		return false, nil
	}
	foundation, err := deps.Store.Adaptation.LoadSourceFoundation()
	if err != nil {
		return false, fmt.Errorf("load source foundation: %w", err)
	}
	if foundation == nil {
		return false, nil
	}
	reportSignature := sourceReportsSignature(reports)
	promptSignature := sourceFoundationPromptSignature(deps.Prompts.FoundationMerge)
	migratedLegacyFoundation := false
	if legacySourceFoundationCanMigrate(foundation, manifest, reportSignature) {
		emit(StageFoundation, manifest.ChapterCount, manifest.ChapterCount, "根据已存逐章报告升级正式角色范围，无需重新分析原文...", nil)
		upgraded := sourceFoundationWithMetadata(
			*foundation,
			manifest,
			reports,
			deps.Prompts.FoundationMerge,
			deps.foundationMergeBatchRunes(),
		)
		upgraded, _, err = domain.ApplyAdaptationSourceCharacterPolicy(upgraded, reports)
		if err != nil {
			return true, fmt.Errorf("apply legacy prepared formal source character policy: %w", err)
		}
		if err = deps.Store.Adaptation.SaveSourceFoundation(upgraded); err != nil {
			return true, fmt.Errorf("save migrated prepared source foundation: %w", err)
		}
		foundation = &upgraded
		migratedLegacyFoundation = true
	}
	repairedFormalCast := false
	if !migratedLegacyFoundation && foundation.Version == adaptationSourceFoundationVersion {
		repaired, _, policyErr := domain.ApplyAdaptationSourceCharacterPolicy(*foundation, reports)
		if policyErr != nil {
			return true, fmt.Errorf("check prepared formal source character policy: %w", policyErr)
		}
		if !sameFormalSourceCast(*foundation, repaired) {
			emit(StageFoundation, manifest.ChapterCount, manifest.ChapterCount, "根据完整证据索引修复源书正式角色范围...", nil)
			if policyErr = deps.Store.Adaptation.SaveSourceFoundation(repaired); policyErr != nil {
				return true, fmt.Errorf("save repaired prepared source foundation: %w", policyErr)
			}
			foundation = &repaired
			repairedFormalCast = true
		}
	}
	current, err := deps.Store.Adaptation.CoCreateDossierCurrent(CoCreateDossierPromptVersion, CoCreateDossierBatchSize, CoCreateDossierBatchRuneLimit)
	if err != nil {
		return false, fmt.Errorf("check co-create dossier: %w", err)
	}
	if current {
		if migratedLegacyFoundation || repairedFormalCast {
			emit(StageDone, manifest.ChapterCount, manifest.ChapterCount, "已复用逐章报告并完成源书正式角色范围升级", nil)
			return true, nil
		}
		return false, nil
	}
	if _, err := EnsureCoCreateDossier(ctx, deps, manifest, reports, emit); err != nil {
		return true, err
	}
	if !sourceFoundationCurrent(
		foundation,
		manifest,
		reportSignature,
		promptSignature,
		deps.foundationMergeBatchRunes(),
	) {
		emit(StageFoundation, manifest.ChapterCount, manifest.ChapterCount, "升级源书正式角色卡策略并重建 foundation...", nil)
		result, mergeErr := mergeSourceFoundationResumable(
			ctx,
			deps,
			manifest,
			reports,
			deps.foundationMergeBatchRunes(),
			emit,
		)
		if mergeErr != nil {
			return true, fmt.Errorf("upgrade prepared source foundation: %w", mergeErr)
		}
		upgraded := sourceFoundationWithMetadata(
			toSourceFoundation(result),
			manifest,
			reports,
			deps.Prompts.FoundationMerge,
			deps.foundationMergeBatchRunes(),
		)
		upgraded, _, mergeErr = domain.ApplyAdaptationSourceCharacterPolicy(upgraded, reports)
		if mergeErr != nil {
			return true, fmt.Errorf("apply prepared formal source character policy: %w", mergeErr)
		}
		if mergeErr = deps.Store.Adaptation.SaveSourceFoundation(upgraded); mergeErr != nil {
			return true, fmt.Errorf("save upgraded prepared source foundation: %w", mergeErr)
		}
	}
	emit(StageDone, manifest.ChapterCount, manifest.ChapterCount, "已复用逐章报告并完成源书角色策略升级", nil)
	return true, nil
}

func sameFormalSourceCast(left, right domain.AdaptationSourceFoundation) bool {
	leftData, leftErr := json.Marshal(struct {
		Characters    []domain.Character             `json:"characters"`
		Relationships []domain.CharacterRelationship `json:"relationships"`
	}{left.Characters, left.Relationships})
	rightData, rightErr := json.Marshal(struct {
		Characters    []domain.Character             `json:"characters"`
		Relationships []domain.CharacterRelationship `json:"relationships"`
	}{right.Characters, right.Relationships})
	return leftErr == nil && rightErr == nil && string(leftData) == string(rightData)
}

func legacySourceFoundationCanMigrate(
	foundation *domain.AdaptationSourceFoundation,
	manifest *domain.AdaptationSourceManifest,
	reportSignature string,
) bool {
	if foundation == nil || manifest == nil {
		return false
	}
	return foundation.Version == adaptationSourceFoundationVersion-1 &&
		foundation.SourceChapterCount == manifest.ChapterCount &&
		strings.TrimSpace(foundation.SourceSignature) == store.AdaptationSourceSignature(*manifest) &&
		strings.TrimSpace(foundation.ReportSignature) == strings.TrimSpace(reportSignature)
}

func mergeSourceFoundationResumable(
	ctx context.Context,
	deps Deps,
	manifest *domain.AdaptationSourceManifest,
	reports []domain.AdaptationSourceReport,
	batchRuneLimit int,
	emit func(Stage, int, int, string, error),
) (*imp.FoundationResult, error) {
	if deps.Store == nil || deps.LLM == nil {
		return nil, fmt.Errorf("deps incomplete")
	}
	if manifest == nil || manifest.ChapterCount <= 0 {
		return nil, fmt.Errorf("source manifest is required")
	}
	if len(reports) == 0 {
		return nil, fmt.Errorf("source reports are required")
	}
	if emit == nil {
		emit = func(Stage, int, int, string, error) {}
	}
	if batchRuneLimit <= 0 {
		batchRuneLimit = imp.DefaultFoundationMergeRunes
	}
	total := len(reports)
	promptSignature := sourceFoundationPromptSignature(deps.Prompts.FoundationMerge)
	sourceSignature := store.AdaptationSourceSignature(*manifest)
	opts := structuredCallOptionsWithDeps(deps, StageFoundation, total, total, emit)
	reportBatches := imp.FoundationMergeReportBatchesForPrompt(reports, batchRuneLimit, deps.Prompts.FoundationMerge)
	partials := make([]imp.FoundationMergePartial, 0, len(reportBatches))
	for i, batchReports := range reportBatches {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		index := i + 1
		from := batchReports[0].Chapter
		to := batchReports[len(batchReports)-1].Chapter
		inputSignature := sourceReportsSignature(batchReports)
		existing, err := deps.Store.Adaptation.LoadSourceFoundationBatch(0, index)
		if err != nil {
			return nil, fmt.Errorf("load source foundation batch %d: %w", index, err)
		}
		if sourceFoundationBatchCurrent(existing, sourceFoundationBatchKindReports, 0, index, from, to, sourceSignature, inputSignature, promptSignature, batchRuneLimit) {
			partials = append(partials, imp.FoundationMergePartial{
				Index:          index,
				From:           existing.SourceFrom,
				To:             existing.SourceTo,
				InputSignature: existing.InputSignature,
				Result:         toFoundationResult(&existing.Foundation),
			})
			emit(StageFoundation, to, total, fmt.Sprintf("reuse source foundation checkpoint %d/%d: chapters %d-%d", index, len(reportBatches), from, to), nil)
			continue
		}

		emit(StageFoundation, to, total, fmt.Sprintf("merge source foundation batch %d/%d: chapters %d-%d", index, len(reportBatches), from, to), nil)
		result, err := imp.MergeFoundationFromReports(ctx, deps.modelForStage("source_analysis"), deps.Prompts.FoundationMerge, batchReports, opts)
		if err != nil {
			return nil, fmt.Errorf("merge source foundation batch %d/%d (chapters %d-%d): %w", index, len(reportBatches), from, to, err)
		}
		batch := sourceFoundationBatchFromResult(sourceFoundationBatchKindReports, 0, index, from, to, sourceSignature, inputSignature, promptSignature, batchRuneLimit, manifest, result)
		if err := deps.Store.Adaptation.SaveSourceFoundationBatch(batch); err != nil {
			return nil, fmt.Errorf("save source foundation batch %d: %w", index, err)
		}
		partials = append(partials, imp.FoundationMergePartial{
			Index:          index,
			From:           from,
			To:             to,
			InputSignature: inputSignature,
			Result:         result,
		})
	}

	result, err := mergeSourceFoundationPartialsResumable(ctx, deps, manifest, partials, total, batchRuneLimit, sourceSignature, promptSignature, emit)
	if err != nil {
		return nil, err
	}
	result.Volumes = imp.BuildSourceOutlineFromReports(reports)
	if got := len(domain.FlattenOutline(result.Volumes)); got != len(reports) {
		return nil, fmt.Errorf("generated source outline chapter count mismatch: got %d, want %d", got, len(reports))
	}
	if result.Compass != nil && result.Compass.LastUpdated == 0 {
		result.Compass.LastUpdated = len(reports)
	}
	return result, nil
}

func mergeSourceFoundationPartialsResumable(
	ctx context.Context,
	deps Deps,
	manifest *domain.AdaptationSourceManifest,
	partials []imp.FoundationMergePartial,
	totalReports int,
	batchRuneLimit int,
	sourceSignature string,
	promptSignature string,
	emit func(Stage, int, int, string, error),
) (*imp.FoundationResult, error) {
	if len(partials) == 0 {
		return nil, fmt.Errorf("no source foundation batches to merge")
	}
	if len(partials) == 1 {
		return partials[0].Result, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	const level = 1
	const index = 1
	from := partials[0].From
	to := partials[len(partials)-1].To
	inputSignature := sourceFoundationPartialGroupSignature(level, partials)
	existing, err := deps.Store.Adaptation.LoadSourceFoundationBatch(level, index)
	if err != nil {
		return nil, fmt.Errorf("load assembled source foundation checkpoint: %w", err)
	}
	if sourceFoundationBatchCurrent(existing, sourceFoundationBatchKindAssembled, level, index, from, to, sourceSignature, inputSignature, promptSignature, batchRuneLimit) {
		emit(StageFoundation, to, totalReports, fmt.Sprintf("reuse assembled source foundation checkpoint: chapters %d-%d", from, to), nil)
		return toFoundationResult(&existing.Foundation), nil
	}

	emit(StageFoundation, to, totalReports, fmt.Sprintf("assemble %d bounded source foundation batches in code: chapters %d-%d", len(partials), from, to), nil)
	result, err := imp.MergeFoundationPartialsDeterministic(partials, totalReports)
	if err != nil {
		return nil, fmt.Errorf("assemble source foundation checkpoints: %w", err)
	}
	batch := sourceFoundationBatchFromResult(sourceFoundationBatchKindAssembled, level, index, from, to, sourceSignature, inputSignature, promptSignature, batchRuneLimit, manifest, result)
	if err := deps.Store.Adaptation.SaveSourceFoundationBatch(batch); err != nil {
		return nil, fmt.Errorf("save assembled source foundation checkpoint: %w", err)
	}
	return result, nil
}

func sourceFoundationBatchFromResult(
	kind string,
	level int,
	index int,
	from int,
	to int,
	sourceSignature string,
	inputSignature string,
	promptSignature string,
	batchRuneLimit int,
	manifest *domain.AdaptationSourceManifest,
	result *imp.FoundationResult,
) domain.AdaptationSourceFoundationBatch {
	foundation := sourceFoundationWithMetadata(toSourceFoundation(result), manifest, nil, "", batchRuneLimit)
	foundation.ReportSignature = inputSignature
	foundation.PromptVersion = promptSignature
	return domain.AdaptationSourceFoundationBatch{
		Version:            adaptationSourceFoundationVersion,
		Kind:               kind,
		Level:              level,
		Index:              index,
		SourceFrom:         from,
		SourceTo:           to,
		SourcePath:         foundation.SourcePath,
		SourceChapterCount: foundation.SourceChapterCount,
		SourceSignature:    sourceSignature,
		InputSignature:     inputSignature,
		PromptVersion:      promptSignature,
		BatchRuneLimit:     batchRuneLimit,
		GeneratedAt:        foundation.GeneratedAt,
		Foundation:         foundation,
	}
}

func sourceFoundationCurrent(
	foundation *domain.AdaptationSourceFoundation,
	manifest *domain.AdaptationSourceManifest,
	reportSignature string,
	promptSignature string,
	batchRuneLimit int,
) bool {
	if !sourceFoundationUsable(foundation) || manifest == nil {
		return false
	}
	return foundation.Version == adaptationSourceFoundationVersion &&
		foundation.SourceChapterCount == manifest.ChapterCount &&
		strings.TrimSpace(foundation.SourceSignature) == store.AdaptationSourceSignature(*manifest) &&
		strings.TrimSpace(foundation.ReportSignature) == strings.TrimSpace(reportSignature) &&
		strings.TrimSpace(foundation.PromptVersion) == strings.TrimSpace(promptSignature) &&
		foundation.BatchRuneLimit == batchRuneLimit
}

// SourceFoundationHasVersionedMetadata reports whether a loaded source
// foundation can prove which source and analysis process produced it. Legacy
// foundations without these bindings must be incrementally rebuilt before
// they are reused by a new adaptation project.
func SourceFoundationHasVersionedMetadata(
	foundation *domain.AdaptationSourceFoundation,
	manifest *domain.AdaptationSourceManifest,
) bool {
	if !sourceFoundationUsable(foundation) || manifest == nil {
		return false
	}
	return foundation.Version == adaptationSourceFoundationVersion &&
		foundation.SourceChapterCount == manifest.ChapterCount &&
		strings.TrimSpace(foundation.SourceSignature) == store.AdaptationSourceSignature(*manifest) &&
		strings.TrimSpace(foundation.ReportSignature) != "" &&
		strings.HasPrefix(strings.TrimSpace(foundation.PromptVersion), adaptationSourceFoundationPromptVersion+":") &&
		foundation.BatchRuneLimit > 0 &&
		len(domain.FlattenOutline(foundation.Volumes)) == manifest.ChapterCount
}

func sourceFoundationBatchCurrent(
	batch *domain.AdaptationSourceFoundationBatch,
	kind string,
	level int,
	index int,
	from int,
	to int,
	sourceSignature string,
	inputSignature string,
	promptSignature string,
	batchRuneLimit int,
) bool {
	if batch == nil || !sourceFoundationUsable(&batch.Foundation) {
		return false
	}
	versionCurrent := batch.Version == adaptationSourceFoundationVersion ||
		(kind == sourceFoundationBatchKindReports && batch.Version == adaptationSourceFoundationVersion-1)
	return versionCurrent &&
		strings.TrimSpace(batch.Kind) == kind &&
		batch.Level == level &&
		batch.Index == index &&
		batch.SourceFrom == from &&
		batch.SourceTo == to &&
		strings.TrimSpace(batch.SourceSignature) == strings.TrimSpace(sourceSignature) &&
		strings.TrimSpace(batch.InputSignature) == strings.TrimSpace(inputSignature) &&
		strings.TrimSpace(batch.PromptVersion) == strings.TrimSpace(promptSignature) &&
		batch.BatchRuneLimit == batchRuneLimit
}

func sourceFoundationWithMetadata(
	foundation domain.AdaptationSourceFoundation,
	manifest *domain.AdaptationSourceManifest,
	reports []domain.AdaptationSourceReport,
	systemPrompt string,
	batchRuneLimit int,
) domain.AdaptationSourceFoundation {
	foundation.Version = adaptationSourceFoundationVersion
	if strings.TrimSpace(foundation.GeneratedAt) == "" {
		foundation.GeneratedAt = time.Now().UTC().Format(time.RFC3339)
	}
	if manifest != nil {
		foundation.SourcePath = manifest.SourcePath
		foundation.SourceChapterCount = manifest.ChapterCount
		foundation.SourceSignature = store.AdaptationSourceSignature(*manifest)
	}
	if reports != nil {
		foundation.ReportSignature = sourceReportsSignature(reports)
	}
	if strings.TrimSpace(systemPrompt) != "" {
		foundation.PromptVersion = sourceFoundationPromptSignature(systemPrompt)
	}
	foundation.BatchRuneLimit = batchRuneLimit
	return foundation
}

func sourceFoundationUsable(foundation *domain.AdaptationSourceFoundation) bool {
	return foundation != nil &&
		strings.TrimSpace(foundation.Premise) != "" &&
		len(foundation.Characters) > 0
}

func sourceFoundationPromptSignature(systemPrompt string) string {
	return adaptationSourceFoundationPromptVersion + ":" + store.TextSHA256(strings.TrimSpace(systemPrompt))
}

func sourceAnalyzerPromptSignature(systemPrompt string) string {
	return adaptationSourceAnalyzerPromptVersion + ":" + store.TextSHA256(strings.TrimSpace(systemPrompt))
}

func sourceReportsSignature(reports []domain.AdaptationSourceReport) string {
	ordered := append([]domain.AdaptationSourceReport(nil), reports...)
	sort.SliceStable(ordered, func(i, j int) bool {
		return ordered[i].Chapter < ordered[j].Chapter
	})
	data, err := json.Marshal(struct {
		Version int                             `json:"version"`
		Reports []domain.AdaptationSourceReport `json:"reports"`
	}{Version: 1, Reports: ordered})
	if err != nil {
		return store.TextSHA256(fmt.Sprintf("%+v", ordered))
	}
	return store.TextSHA256(string(data))
}

func sourceFoundationPartialGroupSignature(level int, partials []imp.FoundationMergePartial) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "level=%d\n", level)
	for _, partial := range partials {
		fmt.Fprintf(&sb, "%d:%d-%d:%s:%s\n",
			partial.Index,
			partial.From,
			partial.To,
			partial.InputSignature,
			sourceFoundationContentSignature(partial.Result),
		)
	}
	return store.TextSHA256(sb.String())
}

func sourceFoundationContentSignature(result *imp.FoundationResult) string {
	data, err := json.Marshal(toSourceFoundation(result))
	if err != nil {
		return store.TextSHA256(fmt.Sprintf("%+v", result))
	}
	return store.TextSHA256(string(data))
}

func shouldRefreshCoCreateDossierBatches(chapter int, manifest *domain.AdaptationSourceManifest) bool {
	if chapter <= 0 || manifest == nil || chapter >= manifest.ChapterCount {
		return false
	}
	for _, spec := range dossierBatchSpecs(*manifest, CoCreateDossierBatchSize) {
		if spec.SourceTo == chapter {
			return true
		}
	}
	return false
}

func ensureSourceSnapshot(adaptation *store.AdaptationStore, sourcePath string, chapters []imp.Chapter) (*domain.AdaptationSourceManifest, bool, error) {
	next := buildSourceManifest(sourcePath, chapters)
	existing, err := adaptation.LoadSourceManifest()
	if err != nil {
		return nil, false, fmt.Errorf("load source manifest: %w", err)
	}
	if sourceManifestMatches(existing, next) {
		return existing, false, nil
	}

	var legacyReports []domain.AdaptationSourceReport
	legacySourceText := make(map[int]string)
	if existing != nil {
		legacyReports, err = adaptation.LoadSourceReports()
		if err != nil {
			return nil, false, fmt.Errorf("load legacy source reports: %w", err)
		}
		for _, source := range existing.Chapters {
			text, _, err := adaptation.LoadSourceChapter(source.Chapter)
			if err != nil {
				return nil, false, fmt.Errorf("load legacy source chapter %d: %w", source.Chapter, err)
			}
			if strings.TrimSpace(text) != "" {
				legacySourceText[source.Chapter] = text
			}
		}
		if _, err := adaptation.Backup("source-snapshot-change"); err != nil {
			return nil, false, fmt.Errorf("backup adaptation store before source reset: %w", err)
		}
	}
	if err := adaptation.Reset(); err != nil {
		return nil, false, fmt.Errorf("reset adaptation store: %w", err)
	}
	sources := make([]domain.AdaptationSource, 0, len(chapters))
	for i, ch := range chapters {
		source, err := adaptation.SaveSourceChapter(i+1, ch.Title, ch.Content)
		if err != nil {
			return nil, false, fmt.Errorf("save source chapter %d: %w", i+1, err)
		}
		sources = append(sources, source)
	}
	next.Chapters = sources
	if err := adaptation.SaveSourceManifest(next); err != nil {
		return nil, false, fmt.Errorf("save source manifest: %w", err)
	}
	if _, err := migrateLegacySourceReportsAfterSnapshotChange(adaptation, legacyReports, legacySourceText, next, chapters); err != nil {
		return nil, false, err
	}
	return &next, true, nil
}

func buildSourceManifest(sourcePath string, chapters []imp.Chapter) domain.AdaptationSourceManifest {
	sources := make([]domain.AdaptationSource, 0, len(chapters))
	for i, ch := range chapters {
		content := strings.TrimSpace(ch.Content)
		chapter := i + 1
		sources = append(sources, domain.AdaptationSource{
			Chapter: chapter,
			Title:   strings.TrimSpace(ch.Title),
			SHA256:  store.TextSHA256(content),
			Path:    store.SourceChapterRelPath(chapter),
			Runes:   utf8.RuneCountInString(content),
		})
	}
	return domain.AdaptationSourceManifest{
		SourcePath:   sourcePath,
		ChapterCount: len(chapters),
		Chapters:     sources,
	}
}

func sourceManifestMatches(existing *domain.AdaptationSourceManifest, next domain.AdaptationSourceManifest) bool {
	if existing == nil || existing.ChapterCount != next.ChapterCount || len(existing.Chapters) != len(next.Chapters) {
		return false
	}
	if !sameSourcePath(existing.SourcePath, next.SourcePath) {
		return false
	}
	for i := range next.Chapters {
		if existing.Chapters[i].Chapter != next.Chapters[i].Chapter || existing.Chapters[i].SHA256 != next.Chapters[i].SHA256 {
			return false
		}
	}
	return true
}

func sameSourcePath(a, b string) bool {
	a = strings.TrimSpace(a)
	b = strings.TrimSpace(b)
	if a == "" || b == "" {
		return a == b
	}
	return strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
}

func reusableSourceReport(report *domain.AdaptationSourceReport, sourceSHA256 string) bool {
	return report != nil &&
		strings.TrimSpace(report.SourceSHA256) != "" &&
		report.SourceSHA256 == sourceSHA256 &&
		reportHasReusableAnalysis(report)
}

func currentSourceReport(report *domain.AdaptationSourceReport, sourceSHA256, analyzerPrompt string) bool {
	return reusableSourceReport(report, sourceSHA256) &&
		report.AnalyzerVersion == adaptationSourceAnalyzerVersion &&
		strings.TrimSpace(report.AnalyzerSignature) == sourceAnalyzerPromptSignature(analyzerPrompt)
}

func repairReusableSourceReports(adaptation *store.AdaptationStore, manifest *domain.AdaptationSourceManifest, emit func(Stage, int, int, string, error)) (bool, error) {
	if adaptation == nil || manifest == nil {
		return false, nil
	}
	changed := false
	for _, source := range manifest.Chapters {
		report, err := adaptation.LoadSourceReport(source.Chapter)
		if err != nil {
			return false, fmt.Errorf("load source report %d for migration: %w", source.Chapter, err)
		}
		if reusableSourceReport(report, source.SHA256) {
			continue
		}
		if !legacyReusableSourceReport(report, source) {
			continue
		}
		next := migratedSourceReport(*report, source)
		if err := adaptation.SaveSourceReport(next); err != nil {
			return false, fmt.Errorf("save migrated source report %d: %w", source.Chapter, err)
		}
		changed = true
		if emit != nil {
			emit(StageChapter, source.Chapter, manifest.ChapterCount, fmt.Sprintf("沿用第 %d/%d 章旧分析报告：%s", source.Chapter, manifest.ChapterCount, source.Title), nil)
		}
	}
	if changed {
		if reports, err := adaptation.LoadSourceReports(); err == nil {
			_ = adaptation.SaveSourceReports(reports)
		}
	}
	return changed, nil
}

func migrateLegacySourceReportsAfterSnapshotChange(adaptation *store.AdaptationStore, reports []domain.AdaptationSourceReport, oldSourceText map[int]string, manifest domain.AdaptationSourceManifest, chapters []imp.Chapter) (int, error) {
	if adaptation == nil || len(reports) == 0 || len(chapters) == 0 {
		return 0, nil
	}
	reportByChapter := make(map[int]domain.AdaptationSourceReport, len(reports))
	for _, report := range reports {
		if report.Chapter <= 0 || !reportHasReusableAnalysis(&report) {
			continue
		}
		reportByChapter[report.Chapter] = report
	}
	migrated := 0
	for i, source := range manifest.Chapters {
		if i >= len(chapters) {
			break
		}
		report, ok := reportByChapter[source.Chapter]
		if !ok || !legacyReusableSourceReport(&report, source) {
			continue
		}
		if !sourceTextsSimilar(oldSourceText[source.Chapter], chapters[i].Content) {
			continue
		}
		if err := adaptation.SaveSourceReport(migratedSourceReport(report, source)); err != nil {
			return migrated, fmt.Errorf("save migrated source report %d after source reset: %w", source.Chapter, err)
		}
		migrated++
	}
	if migrated > 0 {
		if nextReports, err := adaptation.LoadSourceReports(); err == nil {
			_ = adaptation.SaveSourceReports(nextReports)
		}
	}
	return migrated, nil
}

func legacyReusableSourceReport(report *domain.AdaptationSourceReport, source domain.AdaptationSource) bool {
	return report != nil &&
		report.Chapter == source.Chapter &&
		reportHasReusableAnalysis(report) &&
		reportTitleMatchesSource(report.Title, source.Title)
}

func migratedSourceReport(report domain.AdaptationSourceReport, source domain.AdaptationSource) domain.AdaptationSourceReport {
	report.Chapter = source.Chapter
	report.Title = source.Title
	report.SourceSHA256 = source.SHA256
	return report
}

func reportHasReusableAnalysis(report *domain.AdaptationSourceReport) bool {
	return report != nil &&
		strings.TrimSpace(report.Summary) != "" &&
		len(report.KeyEvents) > 0
}

func reportTitleMatchesSource(reportTitle, sourceTitle string) bool {
	return normalizeSourceReportTitle(reportTitle) == normalizeSourceReportTitle(sourceTitle)
}

func normalizeSourceReportTitle(title string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(title)), "")
}

func sourceTextsSimilar(oldText, newText string) bool {
	oldText = normalizeSourceTextForReuse(oldText)
	newText = normalizeSourceTextForReuse(newText)
	if oldText == "" || newText == "" {
		return false
	}
	if oldText == newText {
		return true
	}
	shorter, longer := oldText, newText
	if len(shorter) > len(longer) {
		shorter, longer = longer, shorter
	}
	if len(shorter) < 200 {
		return false
	}
	ratio := float64(len(shorter)) / float64(len(longer))
	if ratio >= 0.90 && strings.Contains(longer, shorter) {
		return true
	}
	return ratio >= 0.90 && commonPrefixBytes(shorter, longer) >= int(float64(len(shorter))*0.90)
}

func normalizeSourceTextForReuse(text string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(text)), "")
}

func commonPrefixBytes(a, b string) int {
	max := len(a)
	if len(b) < max {
		max = len(b)
	}
	for i := 0; i < max; i++ {
		if a[i] != b[i] {
			return i
		}
	}
	return max
}

func structuredCallOptions(stage Stage, current, total int, emit func(Stage, int, int, string, error)) imp.StructuredCallOptions {
	maxTokens := 4096
	if stage == StageFoundation {
		maxTokens = 8192
	}
	return imp.StructuredCallOptions{
		MaxAttempts: retrypolicy.MaxAttempts,
		MaxTokens:   maxTokens,
		OnRetry: func(ev imp.StructuredRetryEvent) {
			if emit == nil {
				return
			}
			emit(stage, current, total, fmt.Sprintf("重试 %d/%d：%v", ev.Attempt, ev.MaxAttempts, ev.Err), ev.Err)
		},
	}
}

func structuredCallOptionsWithDeps(deps Deps, stage Stage, current, total int, emit func(Stage, int, int, string, error)) imp.StructuredCallOptions {
	opts := structuredCallOptions(stage, current, total, emit)
	opts.MaxAttempts = deps.modelCallMaxAttempts()
	return opts
}

func (d Deps) modelCallMaxAttempts() int {
	if d.ModelCallMaxAttemptsProvider != nil {
		if attempts := d.ModelCallMaxAttemptsProvider(); attempts > 0 {
			return attempts
		}
	}
	if d.ModelCallMaxAttempts > 0 {
		return d.ModelCallMaxAttempts
	}
	return adaptationPlannerGenerateMaxAttempts
}

func (d Deps) structureRepairMaxAttempts() int {
	if d.StructureRepairMaxAttemptsProvider != nil {
		if attempts := d.StructureRepairMaxAttemptsProvider(); attempts > 0 {
			return attempts
		}
	}
	if d.StructureRepairMaxAttempts > 0 {
		return d.StructureRepairMaxAttempts
	}
	return adaptationPlannerRepairMaxAttempts
}

func (d Deps) budgetQualityMaxAttempts() int {
	if d.BudgetQualityMaxAttemptsProvider != nil {
		if attempts := d.BudgetQualityMaxAttemptsProvider(); attempts > 0 {
			return attempts
		}
	}
	if d.BudgetQualityMaxAttempts > 0 {
		return d.BudgetQualityMaxAttempts
	}
	return adaptationPlannerBudgetQualityAttempts
}

func (d Deps) adaptationOutlineAuditRetryMaxAttempts() int {
	if d.AdaptationOutlineAuditRetryMaxAttemptsProvider != nil {
		if attempts := d.AdaptationOutlineAuditRetryMaxAttemptsProvider(); attempts > 0 {
			return attempts
		}
	}
	if d.AdaptationOutlineAuditRetryMaxAttempts > 0 {
		return d.AdaptationOutlineAuditRetryMaxAttempts
	}
	return adaptationOutlineAuditRetryDefaultAttempts
}

func PrepareRun(ctx context.Context, deps Deps, brief string) (*domain.AdaptationPlan, error) {
	ctx = modeldiag.WithStore(ctx, deps.Store)
	proposal, err := BuildAdaptationProposal(deps, ProposalOptions{
		Brief:         brief,
		Granularity:   inferGranularity(brief),
		RewritePolicy: domain.AdaptationRewriteFullRewrite,
		WordTolerance: DefaultWordTolerance,
	})
	if err != nil {
		return nil, err
	}
	return ConfirmAdaptationProposal(ctx, deps, *proposal)
}

func BuildPlanFromBrief(brief string, reports []domain.AdaptationSourceReport) domain.AdaptationPlan {
	return buildPlanFromInputs(ProposalOptions{
		Brief:         brief,
		Granularity:   inferGranularity(brief),
		RewritePolicy: domain.AdaptationRewriteFullRewrite,
		WordTolerance: DefaultWordTolerance,
	}, reports, nil, domain.AdaptationPlanStatusConfirmed)
}

func BuildAdaptationProposal(deps Deps, opts ProposalOptions) (*domain.AdaptationPlan, error) {
	return BuildAdaptationProposalContext(context.Background(), deps, opts)
}

func prepareProposalPlannerInputs(ctx context.Context, deps Deps, opts ProposalOptions) (ProposalOptions, *domain.AdaptationSourceManifest, []domain.AdaptationSourceReport, *domain.AdaptationSourceFoundation, error) {
	if deps.Store == nil {
		return opts, nil, nil, nil, fmt.Errorf("store is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if _, err := deps.Store.RequireConfirmedAdaptationFoundation(); err != nil {
		return opts, nil, nil, nil, fmt.Errorf("adaptation target foundation gate: %w", err)
	}
	opts.Brief = strings.TrimSpace(opts.Brief)
	if opts.Brief == "" {
		return opts, nil, nil, nil, fmt.Errorf("adaptation brief is required")
	}
	granularity, ok := domain.StrictAdaptationGranularity(opts.Granularity)
	if !ok {
		return opts, nil, nil, nil, fmt.Errorf("adaptation mode must be one of chapter, arc, free")
	}
	opts.Granularity = granularity
	opts.RewritePolicy = domain.AdaptationRewritePolicyForGranularity(opts.Granularity)
	opts.WordTolerance = normalizeProposalWordTolerance(opts.Granularity, opts.WordTolerance)
	compiledRules := domain.CompileAdaptationRules(opts.Brief, opts.Granularity)
	if err := domain.ValidateAdaptationRules(compiledRules); err != nil {
		return opts, nil, nil, nil, err
	}
	manifest, reports, err := ValidatePreparedSource(deps.Store, opts.SourcePath)
	if err != nil {
		return opts, nil, nil, nil, err
	}
	sourceFoundation, err := deps.Store.Adaptation.LoadSourceFoundation()
	if err != nil {
		return opts, nil, nil, nil, fmt.Errorf("load source foundation: %w", err)
	}
	if sourceFoundation == nil {
		return opts, nil, nil, nil, fmt.Errorf("source foundation missing; import source first")
	}
	return opts, manifest, reports, sourceFoundation, nil
}

func BuildAdaptationProposalContext(ctx context.Context, deps Deps, opts ProposalOptions) (*domain.AdaptationPlan, error) {
	ctx = modeldiag.WithStore(ctx, deps.Store)
	opts, manifest, reports, sourceFoundation, err := prepareProposalPlannerInputs(ctx, deps, opts)
	if err != nil {
		return nil, err
	}
	ctx = withAdaptationPromptContract(ctx, deps.PromptTokenCounter, opts.Granularity, opts.Brief)
	emitAdaptProgress(opts.EmitProgress, StagePlan, 0, 0, "改编规划准备完成，正在选择提案生成方式", nil)

	var proposal domain.AdaptationPlan
	if opts.Granularity == domain.AdaptationGranularityChapter {
		emitAdaptProgress(opts.EmitProgress, StagePlan, len(reports), len(reports), "按逐章模式生成改编提案", nil)
		proposal = buildPlanFromInputs(opts, reports, manifest, domain.AdaptationPlanStatusProposal)
	} else {
		proposal, err = buildPlanFromPlanner(ctx, deps, opts, reports, manifest, sourceFoundation)
		if err != nil {
			return nil, fmt.Errorf("build %s adaptation proposal from planner: %w", opts.Granularity, err)
		}
	}
	if err := ValidateAdaptationOutlineQuality(&proposal, manifest); err != nil {
		return nil, err
	}
	if !domain.AdaptationOutlineQualityPassed(proposal) {
		domain.MarkAdaptationOutlineQualityPassed(&proposal)
	}
	if err := bindAdaptationPlanFoundation(deps, &proposal); err != nil {
		return nil, err
	}
	emitAdaptProgress(opts.EmitProgress, StagePlan, len(proposal.Chapters), len(proposal.Chapters), fmt.Sprintf("改编提案已生成，正在保存：%d 章", len(proposal.Chapters)), nil)
	if err := deps.Store.Adaptation.SaveProposal(proposal); err != nil {
		return nil, fmt.Errorf("save adaptation proposal: %w", err)
	}
	emitAdaptProgress(opts.EmitProgress, StageDone, len(proposal.Chapters), len(proposal.Chapters), fmt.Sprintf("改编提案已保存：%d 章", len(proposal.Chapters)), nil)
	return &proposal, nil
}

func BuildAdaptationProposalVolumesContext(ctx context.Context, deps Deps, opts ProposalOptions) (*ProposalStageResult, error) {
	ctx = modeldiag.WithStore(ctx, deps.Store)
	if err := refuseProposalVolumeStageRegression(deps); err != nil {
		return nil, err
	}
	opts, manifest, reports, sourceFoundation, err := prepareProposalPlannerInputs(ctx, deps, opts)
	if err != nil {
		return nil, err
	}
	ctx = withAdaptationPromptContract(ctx, deps.PromptTokenCounter, opts.Granularity, opts.Brief)
	emitAdaptProgress(opts.EmitProgress, StagePlan, 0, 0, "改编规划准备完成，正在判断是否需要分卷审核", nil)
	targetChapterCount := plannerTargetChapterCount(opts, manifest)
	if !shouldUseChunkedPlanner(opts, manifest, targetChapterCount) {
		proposal, err := BuildAdaptationProposalContext(ctx, deps, opts)
		if err != nil {
			return nil, err
		}
		return &ProposalStageResult{Proposal: proposal}, nil
	}
	skeleton, runtime, err := buildPlannerVolumeSkeleton(ctx, deps, opts, reports, manifest, sourceFoundation, targetChapterCount)
	if err != nil {
		return nil, fmt.Errorf("build %s adaptation volume review: %w", opts.Granularity, err)
	}
	review := volumeReviewFromSkeleton(opts, manifest, skeleton)
	if err := bindAdaptationVolumeFoundation(deps, &review); err != nil {
		return nil, err
	}
	if err := validateAdaptationVolumeReview(review, manifest); err != nil {
		return nil, fmt.Errorf("validate adaptation volume review: %w", err)
	}
	emitAdaptProgress(opts.EmitProgress, StagePlan, len(review.Volumes), len(review.Volumes), fmt.Sprintf("分卷剧情已生成，正在保存：%d 卷", len(review.Volumes)), nil)
	if err := deps.Store.Adaptation.SaveVolumeReview(review); err != nil {
		return nil, fmt.Errorf("save adaptation volume review: %w", err)
	}
	if runtime != nil {
		runtime.Skeleton = plannerRuntimeOutlineFromSkeleton(skeleton)
		runtime.CompletedBatches = nil
		if err := savePlannerProposalRuntime(deps, runtime); err != nil {
			return nil, fmt.Errorf("save proposal runtime skeleton: %w", err)
		}
	}
	emitAdaptProgress(opts.EmitProgress, StageDone, len(review.Volumes), len(review.Volumes), fmt.Sprintf("分卷剧情已保存，等待审核：%d 卷", len(review.Volumes)), nil)
	return &ProposalStageResult{VolumeReview: &review}, nil
}

func refuseProposalVolumeStageRegression(deps Deps) error {
	if deps.Store == nil {
		return nil
	}
	workflow, err := deps.Store.Adaptation.LoadPlanningWorkflow()
	if err != nil {
		return fmt.Errorf("load adaptation planning workflow: %w", err)
	}
	runtime, err := deps.Store.Adaptation.LoadProposalRuntime()
	if err != nil {
		return fmt.Errorf("load proposal runtime before volume generation: %w", err)
	}
	if (workflow != nil && workflow.Stage == domain.AdaptationPlanningStageDetailsGenerating) ||
		(runtime != nil && len(runtime.CompletedBatches) > 0) {
		return fmt.Errorf("chapter detail outline generation already started; resume the detail stage instead of regenerating volumes")
	}
	return nil
}

func ReviseAdaptationProposal(ctx context.Context, deps Deps, opts ProposalRevisionOptions) (*domain.AdaptationPlan, error) {
	ctx = modeldiag.WithStore(ctx, deps.Store)
	return ReviseAdaptationProposalContext(ctx, deps, opts)
}

func ReviseAdaptationProposalContext(ctx context.Context, deps Deps, opts ProposalRevisionOptions) (*domain.AdaptationPlan, error) {
	ctx = modeldiag.WithStore(ctx, deps.Store)
	if deps.Store == nil {
		return nil, fmt.Errorf("store is required")
	}
	if _, err := deps.Store.RequireConfirmedAdaptationFoundation(); err != nil {
		return nil, fmt.Errorf("adaptation target foundation gate: %w", err)
	}
	if deps.LLM == nil {
		return nil, fmt.Errorf("planner llm is required for adaptation proposal revision")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	opts.Instruction = strings.TrimSpace(opts.Instruction)
	if opts.Instruction == "" {
		return nil, fmt.Errorf("revision instruction is required")
	}
	proposal, err := deps.Store.Adaptation.LoadProposal()
	if err != nil {
		return nil, fmt.Errorf("load adaptation proposal: %w", err)
	}
	if proposal == nil || len(proposal.Chapters) == 0 {
		return nil, fmt.Errorf("adaptation proposal is required")
	}
	ctx = withAdaptationPromptContract(ctx, deps.PromptTokenCounter, proposal.Granularity, proposal.Brief)
	from, to, err := resolveProposalRevisionRange(*proposal, opts)
	if err != nil {
		return nil, err
	}
	manifest, reports, err := ValidatePreparedSource(deps.Store, "")
	if err != nil {
		return nil, err
	}
	sourceFoundation, err := deps.Store.Adaptation.LoadSourceFoundation()
	if err != nil {
		return nil, fmt.Errorf("load source foundation: %w", err)
	}
	if sourceFoundation == nil {
		return nil, fmt.Errorf("source foundation missing; analyze source first")
	}
	emitAdaptProgress(opts.EmitProgress, StagePlan, from, to, fmt.Sprintf("准备修订改编提案：第 %d-%d 章", from, to), nil)

	systemPrompt := adaptationPlannerSystemPrompt(deps)
	if systemPrompt == "" {
		systemPrompt = "# Adaptation Planner\n\nReturn only JSON for the requested proposal revision."
	}
	if opts.VolumeIndex > 0 {
		return reviseAdaptationProposalVolumeContext(ctx, deps, opts, *proposal, from, to, manifest, sourceFoundation, reports, systemPrompt)
	}
	updated := cloneAdaptationPlan(*proposal)
	updated.Volumes = normalizeAdaptationProposalVolumes(updated.Volumes, len(updated.Chapters))
	allowExpansion := shouldAllowProposalRevisionExpansion(*proposal, opts, from, to)
	totalBatches := revisionBatchCount(from, to, adaptationPlannerRevisionBatchMax)
	batchOrdinal := 0
	for chunkFrom := from; chunkFrom <= to; chunkFrom += adaptationPlannerRevisionBatchMax {
		chunkTo := min(to, chunkFrom+adaptationPlannerRevisionBatchMax-1)
		batchOrdinal++
		batch := proposalRevisionBatch(updated, chunkFrom, chunkTo)
		expansionMaxTo := chunkTo
		if allowExpansion && chunkTo == to {
			expansionMaxTo = to + adaptationPlannerRevisionExpansionMax
		}
		revisionPrompt, err := buildAdaptationProposalRevisionUserPrompt(opts, updated, batch, expansionMaxTo, manifest, sourceFoundation, reportsForPlannerBatch(reports, batch))
		if err != nil {
			return nil, err
		}
		emitAdaptProgress(opts.EmitProgress, StagePlan, batchOrdinal, totalBatches, fmt.Sprintf("请求修订第 %d/%d 批：第 %d-%d 章", batchOrdinal, totalBatches, chunkFrom, chunkTo), nil)
		revisionText, err := generatePlannerText(
			ctx,
			deps.modelForStage("detail_outline"),
			systemPrompt,
			revisionPrompt,
			adaptationPlannerMaxTokens,
			opts.EmitProgress,
			batchOrdinal,
			totalBatches,
			fmt.Sprintf("修订第 %d/%d 批", batchOrdinal, totalBatches),
			deps.modelCallMaxAttempts(),
		)
		if err != nil {
			return nil, fmt.Errorf("planner revision %d-%d llm generate: %w", chunkFrom, chunkTo, err)
		}
		emitAdaptProgress(opts.EmitProgress, StagePlan, batchOrdinal, totalBatches, fmt.Sprintf("修订模型已返回第 %d/%d 批，正在解析校验", batchOrdinal, totalBatches), nil)
		revisedChapters, err := collectProposalRevisionBatchChaptersWithRepair(
			ctx,
			deps.modelForStage("detail_outline"),
			systemPrompt,
			revisionPrompt,
			revisionText,
			batch,
			expansionMaxTo,
			plannerBatchChapterValidator(proposalOptionsFromPlan(updated), manifest, batch),
			opts.EmitProgress,
			batchOrdinal,
			totalBatches,
			fmt.Sprintf("修订第 %d/%d 批", batchOrdinal, totalBatches),
			deps.structureRepairMaxAttempts(),
			deps.modelCallMaxAttempts(),
		)
		if err != nil {
			return nil, fmt.Errorf("planner revision %d-%d: %w", chunkFrom, chunkTo, err)
		}
		revisedTo, err := replaceProposalChapterRange(&updated, chunkFrom, chunkTo, expansionMaxTo, revisedChapters)
		if err != nil {
			return nil, err
		}
		emitAdaptProgress(opts.EmitProgress, StagePlan, batchOrdinal, totalBatches, fmt.Sprintf("修订第 %d/%d 批完成：第 %d-%d 章", batchOrdinal, totalBatches, chunkFrom, revisedTo), nil)
	}
	return finalizeRevisedAdaptationProposal(deps, opts, *proposal, updated, from, to, reports, manifest, totalBatches, totalBatches)
}

func ReviseAdaptationVolumeReviewContext(ctx context.Context, deps Deps, opts ProposalRevisionOptions) (*domain.AdaptationVolumeReview, error) {
	ctx = modeldiag.WithStore(ctx, deps.Store)
	if deps.Store == nil {
		return nil, fmt.Errorf("store is required")
	}
	if _, err := deps.Store.RequireConfirmedAdaptationFoundation(); err != nil {
		return nil, fmt.Errorf("adaptation target foundation gate: %w", err)
	}
	if deps.LLM == nil {
		return nil, fmt.Errorf("planner llm is required for adaptation volume review revision")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	opts.Instruction = strings.TrimSpace(opts.Instruction)
	if opts.Instruction == "" {
		return nil, fmt.Errorf("revision instruction is required")
	}
	if opts.VolumeIndex <= 0 {
		return nil, fmt.Errorf("volume_index must name one volume")
	}
	review, err := deps.Store.Adaptation.LoadVolumeReview()
	if err != nil {
		return nil, fmt.Errorf("load adaptation volume review: %w", err)
	}
	if review == nil || len(review.Volumes) == 0 {
		return nil, fmt.Errorf("adaptation volume review is required")
	}
	ctx = withAdaptationPromptContract(ctx, deps.PromptTokenCounter, review.Granularity, review.Brief)
	manifest, reports, err := ValidatePreparedSource(deps.Store, review.SourcePath)
	if err != nil {
		return nil, err
	}
	sourceFoundation, err := deps.Store.Adaptation.LoadSourceFoundation()
	if err != nil {
		return nil, fmt.Errorf("load source foundation: %w", err)
	}
	if sourceFoundation == nil {
		return nil, fmt.Errorf("source foundation missing; analyze source first")
	}
	originalBatch, err := volumeReviewBatch(*review, opts.VolumeIndex)
	if err != nil {
		return nil, err
	}
	systemPrompt := adaptationPlannerSystemPrompt(deps)
	if systemPrompt == "" {
		systemPrompt = "# Adaptation Planner\n\nReturn only JSON for the requested volume review revision."
	}
	expansionMaxTo := originalBatch.TargetTo + adaptationPlannerRevisionExpansionMax
	emitAdaptProgress(opts.EmitProgress, StagePlan, 0, 0, fmt.Sprintf("请求第 %d 卷剧情修正：第 %d-%d 章", opts.VolumeIndex, originalBatch.TargetFrom, originalBatch.TargetTo), nil)
	revisionPrompt, err := buildAdaptationVolumeReviewRevisionPrompt(opts, *review, originalBatch, expansionMaxTo, manifest, sourceFoundation, reportsForPlannerBatch(reports, originalBatch))
	if err != nil {
		return nil, err
	}
	skeletonModel := deps.modelForStage("skeleton")
	revisionText, err := generatePlannerText(
		ctx,
		skeletonModel,
		systemPrompt,
		revisionPrompt,
		adaptationPlannerSkeletonMaxTokens,
		opts.EmitProgress,
		0,
		0,
		fmt.Sprintf("第 %d 卷剧情修正", opts.VolumeIndex),
		deps.modelCallMaxAttempts(),
	)
	if err != nil {
		return nil, fmt.Errorf("planner volume review revision llm generate: %w", err)
	}
	minTargetTo := plannerSkeletonBatchMinTargetTo(originalBatch, proposalOptionsFromVolumeReview(*review), manifest)
	revisedBatch, err := collectProposalVolumeRevisionSkeletonWithRepair(ctx, skeletonModel, systemPrompt, revisionPrompt, revisionText, originalBatch, expansionMaxTo, true, manifest, minTargetTo, false, opts.EmitProgress, deps.structureRepairMaxAttempts(), deps.modelCallMaxAttempts())
	if err != nil {
		return nil, fmt.Errorf("planner volume review revision skeleton: %w", err)
	}
	updated := cloneAdaptationVolumeReview(*review)
	applyVolumeReviewBatchRevision(&updated, originalBatch, revisedBatch)
	if err := validateAdaptationVolumeReview(updated, manifest); err != nil {
		return nil, err
	}
	if updated.Planner == nil {
		updated.Planner = &domain.AdaptationPlannerMeta{}
	}
	updated.Planner.Notes = append(updated.Planner.Notes,
		fmt.Sprintf("volume review revised for volume %d: %s", opts.VolumeIndex, opts.Instruction),
	)
	if err := deps.Store.Adaptation.SaveVolumeReview(updated); err != nil {
		return nil, fmt.Errorf("save revised adaptation volume review: %w", err)
	}
	emitAdaptProgress(opts.EmitProgress, StageDone, len(updated.Volumes), len(updated.Volumes), fmt.Sprintf("第 %d 卷剧情已修订，等待审核", opts.VolumeIndex), nil)
	return &updated, nil
}

func BuildAdaptationProposalDetailsContext(ctx context.Context, deps Deps, opts ProposalDetailsOptions) (*domain.AdaptationPlan, error) {
	ctx = modeldiag.WithStore(ctx, deps.Store)
	if deps.Store == nil {
		return nil, fmt.Errorf("store is required")
	}
	if _, err := deps.Store.RequireConfirmedAdaptationFoundation(); err != nil {
		return nil, fmt.Errorf("adaptation target foundation gate: %w", err)
	}
	if deps.LLM == nil {
		return nil, fmt.Errorf("planner llm is required for adaptation proposal details")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	review, err := deps.Store.Adaptation.LoadVolumeReview()
	if err != nil {
		return nil, fmt.Errorf("load adaptation volume review: %w", err)
	}
	if review == nil || len(review.Volumes) == 0 {
		return nil, fmt.Errorf("adaptation volume review is required")
	}
	ctx = withAdaptationPromptContract(ctx, deps.PromptTokenCounter, review.Granularity, review.Brief)
	proposalOpts := proposalOptionsFromVolumeReview(*review)
	proposalOpts.EmitProgress = opts.EmitProgress
	manifest, reports, err := ValidatePreparedSource(deps.Store, review.SourcePath)
	if err != nil {
		return nil, err
	}
	sourceFoundation, err := deps.Store.Adaptation.LoadSourceFoundation()
	if err != nil {
		return nil, fmt.Errorf("load source foundation: %w", err)
	}
	if sourceFoundation == nil {
		return nil, fmt.Errorf("source foundation missing; analyze source first")
	}
	repairedReview, repairedSkeleton, repaired, err := repairVolumeReviewBudgetSplitsBeforeDetails(ctx, deps, *review, proposalOpts, manifest, reports, opts.EmitProgress)
	if err != nil {
		return nil, err
	}
	if repaired {
		review = &repairedReview
	}
	skeleton := repairedSkeleton
	if len(skeleton.Batches) == 0 {
		skeleton = plannerSkeletonFromVolumeReview(*review)
	}
	if err := normalizePlannerSkeleton(&skeleton, proposalOpts, manifest, review.TargetChapterCount); err != nil {
		return nil, fmt.Errorf("volume review skeleton invalid: %w", err)
	}
	runtime, _, err := loadPlannerProposalRuntime(deps, proposalOpts, manifest, review.TargetChapterCount, opts.EmitProgress)
	if err != nil {
		return nil, err
	}
	if runtime == nil {
		runtime = newPlannerProposalRuntime(proposalOpts, manifest, review.TargetChapterCount)
	}
	inheritPlannerRuntimeDetailEventContracts(&skeleton, runtime.Skeleton)
	enablePlannerDetailEventContractsForFreshRuntime(&skeleton, runtime)
	attachSkeletonMainlineEvents(&skeleton, reports)
	if runtime.Skeleton != nil && !plannerRuntimeOutlineMatchesSkeleton(runtime.Skeleton, skeleton) {
		emitAdaptProgress(opts.EmitProgress, StagePlan, 0, 0, "分卷剧情已变化，清除旧章节细纲断点后重新生成", nil)
		runtime.CompletedBatches = nil
	}
	runtime.Skeleton = plannerRuntimeOutlineFromSkeleton(skeleton)
	if err := savePlannerProposalRuntime(deps, runtime); err != nil {
		return nil, fmt.Errorf("save proposal runtime skeleton: %w", err)
	}
	proposal, err := buildAdaptationProposalDetailsWithQualityRetries(ctx, deps, proposalOpts, reports, manifest, sourceFoundation, skeleton, runtime)
	if err != nil {
		return nil, err
	}
	if err := ValidateAdaptationOutlineQuality(&proposal, manifest); err != nil {
		return nil, err
	}
	digest := layeredDetailAuditDigest(runtime, len(plannerDetailBatches(skeleton.Batches, adaptationPlannerRecommendedBatchMax)))
	if digest == "" {
		return nil, fmt.Errorf("chapter detail audit layers are incomplete; proposal cannot enter review")
	}
	domain.MarkAdaptationOutlineQualityPassedWithLayers(&proposal, digest)
	if err := bindAdaptationPlanFoundation(deps, &proposal); err != nil {
		return nil, err
	}
	emitAdaptProgress(opts.EmitProgress, StagePlan, len(proposal.Chapters), len(proposal.Chapters), fmt.Sprintf("改编章节细纲已生成，正在保存：%d 章", len(proposal.Chapters)), nil)
	if err := deps.Store.Adaptation.SaveProposal(proposal); err != nil {
		return nil, fmt.Errorf("save adaptation proposal: %w", err)
	}
	emitAdaptProgress(opts.EmitProgress, StageDone, len(proposal.Chapters), len(proposal.Chapters), fmt.Sprintf("改编提案已保存：%d 章", len(proposal.Chapters)), nil)
	return &proposal, nil
}

func buildAdaptationProposalDetailsWithQualityRetries(
	ctx context.Context,
	deps Deps,
	opts ProposalOptions,
	reports []domain.AdaptationSourceReport,
	manifest *domain.AdaptationSourceManifest,
	sourceFoundation *domain.AdaptationSourceFoundation,
	skeleton plannerSkeleton,
	runtime *domain.AdaptationProposalRuntime,
) (domain.AdaptationPlan, error) {
	return retryAdaptationOutlineQualityDynamic(deps.adaptationOutlineAuditRetryMaxAttempts,
		func() (domain.AdaptationPlan, error) {
			return buildPlanFromPlannerSkeletonDetails(ctx, deps, opts, reports, manifest, sourceFoundation, skeleton, runtime)
		},
		func(retry int, qualityErr *AdaptationOutlineQualityError) error {
			opts.outlineQualityFeedback = formatAdaptationOutlineQualityFeedback(qualityErr)
			if runtime != nil {
				affected := markPlannerRuntimeQualityIssues(runtime, skeleton, qualityErr)
				if affected == 0 {
					return fmt.Errorf("final outline audit could not locate its findings to a detail batch; runtime was preserved for diagnosis: %w", qualityErr)
				}
				if err := savePlannerProposalRuntime(deps, runtime); err != nil {
					return fmt.Errorf("save targeted detail-outline audit retry: %w", err)
				}
				emitAdaptProgress(
					opts.EmitProgress, StageAudit, 0, len(runtime.CompletedBatches),
					fmt.Sprintf("完整详纲审计发现 %d 项问题，已定位 %d 个批次；仅修复这些批次并立即复审", len(qualityErr.Issues), affected),
					qualityErr,
				)
			}
			emitAdaptProgress(opts.EmitProgress, StageAudit, 0, 0, fmt.Sprintf("开始第 %d/%d 次定点修复", retry, deps.adaptationOutlineAuditRetryMaxAttempts()), nil)
			return nil
		},
	)
}

func retryAdaptationOutlineQuality(
	maxRetries int,
	generate func() (domain.AdaptationPlan, error),
	prepareRetry func(retry int, qualityErr *AdaptationOutlineQualityError) error,
) (domain.AdaptationPlan, error) {
	return retryAdaptationOutlineQualityDynamic(func() int { return maxRetries }, generate, prepareRetry)
}

func retryAdaptationOutlineQualityDynamic(
	maxRetries func() int,
	generate func() (domain.AdaptationPlan, error),
	prepareRetry func(retry int, qualityErr *AdaptationOutlineQualityError) error,
) (domain.AdaptationPlan, error) {
	var zero domain.AdaptationPlan
	currentMaxRetries := func() int {
		if maxRetries == nil {
			return adaptationOutlineAuditRetryDefaultAttempts
		}
		attempts := maxRetries()
		if attempts <= 0 {
			return adaptationOutlineAuditRetryDefaultAttempts
		}
		return attempts
	}
	proposal, err := generate()
	if err == nil {
		return proposal, nil
	}
	for retry := 1; retry <= currentMaxRetries(); retry++ {
		var qualityErr *AdaptationOutlineQualityError
		if !errors.As(err, &qualityErr) {
			return zero, err
		}
		if prepareRetry != nil {
			if retryErr := prepareRetry(retry, qualityErr); retryErr != nil {
				return zero, retryErr
			}
		}
		proposal, err = generate()
		if err == nil {
			return proposal, nil
		}
	}
	return zero, err
}

func reviseAdaptationProposalVolumeContext(
	ctx context.Context,
	deps Deps,
	opts ProposalRevisionOptions,
	proposal domain.AdaptationPlan,
	from int,
	to int,
	manifest *domain.AdaptationSourceManifest,
	sourceFoundation *domain.AdaptationSourceFoundation,
	reports []domain.AdaptationSourceReport,
	systemPrompt string,
) (*domain.AdaptationPlan, error) {
	updated := cloneAdaptationPlan(proposal)
	updated.Volumes = normalizeAdaptationProposalVolumes(updated.Volumes, len(updated.Chapters))
	originalBatch := proposalRevisionVolumeBatch(updated, opts.VolumeIndex, from, to)
	allowExpansion := shouldAllowProposalRevisionExpansion(proposal, opts, from, to)
	expansionMaxTo := to
	if allowExpansion {
		expansionMaxTo = to + adaptationPlannerRevisionExpansionMax
	}
	emitAdaptProgress(opts.EmitProgress, StagePlan, 0, 0, fmt.Sprintf("请求第 %d 卷剧情重规划：第 %d-%d 章", opts.VolumeIndex, from, to), nil)
	skeletonPrompt, err := buildAdaptationProposalVolumeRevisionSkeletonPrompt(opts, updated, originalBatch, expansionMaxTo, manifest, sourceFoundation, reportsForPlannerBatch(reports, originalBatch))
	if err != nil {
		return nil, err
	}
	skeletonModel := deps.modelForStage("skeleton")
	skeletonText, err := generatePlannerText(
		ctx,
		skeletonModel,
		systemPrompt,
		skeletonPrompt,
		adaptationPlannerSkeletonMaxTokens,
		opts.EmitProgress,
		0,
		0,
		fmt.Sprintf("第 %d 卷剧情重规划", opts.VolumeIndex),
		deps.modelCallMaxAttempts(),
	)
	if err != nil {
		return nil, fmt.Errorf("planner volume revision skeleton llm generate: %w", err)
	}
	minTargetTo := plannerSkeletonBatchMinTargetTo(originalBatch, proposalOptionsFromPlan(updated), manifest)
	revisedBatch, err := collectProposalVolumeRevisionSkeletonWithRepair(ctx, skeletonModel, systemPrompt, skeletonPrompt, skeletonText, originalBatch, expansionMaxTo, allowExpansion, manifest, minTargetTo, false, opts.EmitProgress, deps.structureRepairMaxAttempts(), deps.modelCallMaxAttempts())
	if err != nil {
		return nil, fmt.Errorf("planner volume revision skeleton: %w", err)
	}
	revisedBatch.Notes = append(revisedBatch.Notes, "revision instruction: "+opts.Instruction)
	revisedSkeleton := plannerSkeleton{
		Granularity:        updated.Granularity,
		Status:             domain.AdaptationPlanStatusProposal,
		RewritePolicy:      updated.RewritePolicy,
		Brief:              updated.Brief,
		TargetChapterCount: len(updated.Chapters) + max(0, revisedBatch.TargetTo-to),
		MainlineRules:      append([]string(nil), updated.MainlineRules...),
		RelationshipGoals:  append([]string(nil), updated.RelationshipGoals...),
		Batches:            []plannerSkeletonBatch{revisedBatch},
		Planner:            clonePlannerRuntimeMeta(updated.Planner),
	}
	detailBatches := plannerDetailBatches([]plannerSkeletonBatch{revisedBatch}, adaptationPlannerRecommendedBatchMax)
	emitAdaptProgress(opts.EmitProgress, StagePlan, 0, len(detailBatches), fmt.Sprintf("第 %d 卷剧情重规划完成：第 %d-%d 章，正在生成详细章节提纲", opts.VolumeIndex, revisedBatch.TargetFrom, revisedBatch.TargetTo), nil)
	revisedChapters := make([]domain.AdaptationChapterPlan, 0, revisedBatch.TargetTo-revisedBatch.TargetFrom+1)
	detailOpts := proposalOptionsFromPlan(updated)
	detailModel := deps.modelForStage("detail_outline")
	for idx, detailBatch := range detailBatches {
		batchPrompt, err := buildAdaptationPlannerBatchUserPrompt(detailOpts, manifest, sourceFoundation, revisedSkeleton, detailBatch, reportsForPlannerDetailBatch(reports, detailBatch), revisedChapters)
		if err != nil {
			return nil, err
		}
		emitAdaptProgress(opts.EmitProgress, StagePlan, idx+1, len(detailBatches), fmt.Sprintf("请求第 %d 卷章节详情第 %d/%d 批：第 %d-%d 章", opts.VolumeIndex, idx+1, len(detailBatches), detailBatch.TargetFrom, detailBatch.TargetTo), nil)
		batchText, err := generatePlannerText(
			ctx,
			detailModel,
			systemPrompt,
			batchPrompt,
			adaptationPlannerMaxTokens,
			opts.EmitProgress,
			idx+1,
			len(detailBatches),
			fmt.Sprintf("第 %d 卷章节详情第 %d/%d 批", opts.VolumeIndex, idx+1, len(detailBatches)),
			deps.modelCallMaxAttempts(),
		)
		if err != nil {
			return nil, fmt.Errorf("planner volume revision detail %d-%d llm generate: %w", detailBatch.TargetFrom, detailBatch.TargetTo, err)
		}
		batchChapters, err := collectPlannerBatchChaptersWithRepair(
			ctx,
			detailModel,
			systemPrompt,
			batchPrompt,
			batchText,
			detailBatch,
			plannerBatchChapterValidator(detailOpts, manifest, detailBatch, revisedChapters),
			revisedChapters,
			opts.EmitProgress,
			idx+1,
			len(detailBatches),
			fmt.Sprintf("第 %d 卷章节详情第 %d/%d 批", opts.VolumeIndex, idx+1, len(detailBatches)),
			deps.structureRepairMaxAttempts(),
			deps.modelCallMaxAttempts(),
		)
		if err != nil {
			return nil, fmt.Errorf("planner volume revision detail %d-%d: %w", detailBatch.TargetFrom, detailBatch.TargetTo, err)
		}
		revisedChapters = append(revisedChapters, batchChapters...)
		emitAdaptProgress(opts.EmitProgress, StagePlan, idx+1, len(detailBatches), fmt.Sprintf("第 %d 卷章节详情第 %d/%d 批完成：第 %d-%d 章", opts.VolumeIndex, idx+1, len(detailBatches), detailBatch.TargetFrom, detailBatch.TargetTo), nil)
	}
	if _, err := replaceProposalChapterRange(&updated, from, to, revisedBatch.TargetTo, revisedChapters); err != nil {
		return nil, err
	}
	applyProposalVolumeRevisionMetadata(&updated, opts.VolumeIndex, revisedBatch)
	return finalizeRevisedAdaptationProposal(deps, opts, proposal, updated, from, to, reports, manifest, len(detailBatches), len(detailBatches))
}

func finalizeRevisedAdaptationProposal(
	deps Deps,
	opts ProposalRevisionOptions,
	original domain.AdaptationPlan,
	updated domain.AdaptationPlan,
	from int,
	to int,
	reports []domain.AdaptationSourceReport,
	manifest *domain.AdaptationSourceManifest,
	progressCurrent int,
	progressTotal int,
) (*domain.AdaptationPlan, error) {
	validateOpts := proposalOptionsFromPlan(updated)
	updated.SourceTotalRunes = 0
	updated.TargetTotalRunes = 0
	updated.TargetMinRunes = 0
	updated.TargetMaxRunes = 0
	emitAdaptProgress(opts.EmitProgress, StagePlan, progressCurrent, progressTotal, "修订章节已合并，正在校验完整提案", nil)
	detailModel := deps.modelForStage("detail_outline")
	if err := validatePlannerProposal(&updated, validateOpts, reports, manifest, detailModel); err != nil {
		return nil, fmt.Errorf("revised adaptation proposal invalid: %w", err)
	}
	domain.MarkAdaptationOutlineQualityPassed(&updated)
	updated.Volumes = normalizeAdaptationProposalVolumes(updated.Volumes, len(updated.Chapters))
	originalForCompare := normalizedProposalForRevisionCompare(original, reports, manifest, detailModel)
	updatedForCompare := normalizedProposalForRevisionCompare(updated, reports, manifest, detailModel)
	if !proposalRevisionChanged(originalForCompare, updatedForCompare) {
		return nil, fmt.Errorf("revision produced no proposal changes; please make the instruction more specific or request added ending chapters")
	}
	if updated.Planner == nil {
		updated.Planner = &domain.AdaptationPlannerMeta{}
	}
	updated.Planner.Notes = append(updated.Planner.Notes,
		fmt.Sprintf("proposal revised for target %s (%d-%d): %s", firstNonEmptyString(strings.TrimSpace(opts.Target), fmt.Sprintf("%d-%d", from, to)), from, to, opts.Instruction),
	)
	if err := bindAdaptationPlanFoundation(deps, &updated); err != nil {
		return nil, err
	}
	if err := deps.Store.Adaptation.SaveProposal(updated); err != nil {
		return nil, fmt.Errorf("save revised adaptation proposal: %w", err)
	}
	emitAdaptProgress(opts.EmitProgress, StageDone, len(updated.Chapters), len(updated.Chapters), fmt.Sprintf("改编提案修订已保存：%d 章", len(updated.Chapters)), nil)
	return &updated, nil
}

func normalizedProposalForRevisionCompare(proposal domain.AdaptationPlan, reports []domain.AdaptationSourceReport, manifest *domain.AdaptationSourceManifest, llm imp.LLMChat) domain.AdaptationPlan {
	proposal.SourceTotalRunes = 0
	proposal.TargetTotalRunes = 0
	proposal.TargetMinRunes = 0
	proposal.TargetMaxRunes = 0
	if err := validatePlannerProposal(&proposal, proposalOptionsFromPlan(proposal), reports, manifest, llm); err != nil {
		return proposal
	}
	proposal.Volumes = normalizeAdaptationProposalVolumes(proposal.Volumes, len(proposal.Chapters))
	normalizeProposalChapterCoverageForRevisionCompare(&proposal)
	return proposal
}

func normalizeProposalChapterCoverageForRevisionCompare(proposal *domain.AdaptationPlan) {
	if proposal == nil || len(proposal.Chapters) == 0 || len(proposal.Volumes) == 0 {
		return
	}
	volumes := normalizeAdaptationProposalVolumes(proposal.Volumes, len(proposal.Chapters))
	for _, volume := range volumes {
		if volume.SourceFrom <= 0 || volume.SourceTo < volume.SourceFrom {
			continue
		}
		for idx := range proposal.Chapters {
			chapter := &proposal.Chapters[idx]
			if chapter.Chapter < volume.TargetFrom || chapter.Chapter > volume.TargetTo {
				continue
			}
			chapter.SourceRange = domain.SourceRange{From: volume.SourceFrom, To: volume.SourceTo}
			chapter.SourceChapters = expandSourceChaptersForRange(chapter.SourceChapters, volume.SourceFrom, volume.SourceTo)
		}
	}
}

func buildPlanFromPlanner(
	ctx context.Context,
	deps Deps,
	opts ProposalOptions,
	reports []domain.AdaptationSourceReport,
	manifest *domain.AdaptationSourceManifest,
	sourceFoundation *domain.AdaptationSourceFoundation,
) (domain.AdaptationPlan, error) {
	targetChapterCount := plannerTargetChapterCount(opts, manifest)
	if shouldUseChunkedPlanner(opts, manifest, targetChapterCount) {
		return buildPlanFromPlannerChunked(ctx, deps, opts, reports, manifest, sourceFoundation, targetChapterCount)
	}
	plan, err := buildPlanFromPlannerSingle(ctx, deps, opts, reports, manifest, sourceFoundation)
	var splitRequired *promptcompile.SplitRequiredError
	if errors.As(err, &splitRequired) {
		sourceChapterCount := 0
		if manifest != nil {
			sourceChapterCount = manifest.ChapterCount
		}
		targetChapterCount = max(targetChapterCount, max(sourceChapterCount, adaptationPlannerChunkedMinChapters))
		return buildPlanFromPlannerChunked(ctx, deps, opts, reports, manifest, sourceFoundation, targetChapterCount)
	}
	return plan, err
}

func shouldUseChunkedPlanner(opts ProposalOptions, manifest *domain.AdaptationSourceManifest, targetChapterCount int) bool {
	switch domain.NormalizeAdaptationGranularity(opts.Granularity) {
	case domain.AdaptationGranularityChapter:
		return false
	case domain.AdaptationGranularityArc, domain.AdaptationGranularityFree:
		if targetChapterCount >= adaptationPlannerChunkedMinChapters {
			return true
		}
		return plannerInputRequiresChunking(manifest)
	default:
		return false
	}
}

func plannerInputRequiresChunking(manifest *domain.AdaptationSourceManifest) bool {
	if manifest == nil {
		return false
	}
	if manifest.ChapterCount >= adaptationPlannerSourceChunkedMin {
		return true
	}
	return plannerManifestTotalRunes(manifest) >= CoCreateDossierBatchRuneLimit
}

func plannerManifestTotalRunes(manifest *domain.AdaptationSourceManifest) int {
	if manifest == nil {
		return 0
	}
	total := 0
	for _, chapter := range manifest.Chapters {
		if chapter.Runes > 0 {
			total += chapter.Runes
		}
	}
	return total
}

func plannerTargetChapterCount(opts ProposalOptions, manifest *domain.AdaptationSourceManifest) int {
	if explicit := normalizeTargetChapterCount(opts.TargetChapterCount, inferTargetChapterCount(opts.Brief)); explicit > 0 {
		return explicit
	}
	if manifest == nil || manifest.ChapterCount < adaptationPlannerSourceChunkedMin {
		return 0
	}
	switch domain.NormalizeAdaptationGranularity(opts.Granularity) {
	case domain.AdaptationGranularityArc, domain.AdaptationGranularityFree:
		return max(manifest.ChapterCount, adaptationPlannerChunkedMinChapters)
	default:
		return 0
	}
}

func plannerTargetChapterHintRole(opts ProposalOptions, manifest *domain.AdaptationSourceManifest, targetChapterHint int) string {
	if targetChapterHint <= 0 {
		return ""
	}
	if explicit := normalizeTargetChapterCount(opts.TargetChapterCount, inferTargetChapterCount(opts.Brief)); explicit > 0 {
		return "explicit_target_scale"
	}
	if manifest != nil && manifest.ChapterCount > 0 && targetChapterHint == manifest.ChapterCount {
		return "source_scale_minimum"
	}
	return "long_form_scale_hint"
}

func plannerSkeletonRequestMessage(opts ProposalOptions, manifest *domain.AdaptationSourceManifest, targetChapterHint int) string {
	switch plannerTargetChapterHintRole(opts, manifest, targetChapterHint) {
	case "source_scale_minimum":
		return fmt.Sprintf("请求长篇改编骨架规划：源书 %d 章，模型将决定目标章数", targetChapterHint)
	case "explicit_target_scale":
		return fmt.Sprintf("请求长篇改编骨架规划：目标规模参考 %d 章", targetChapterHint)
	default:
		return fmt.Sprintf("请求长篇改编骨架规划：长篇规模参考 %d 章", targetChapterHint)
	}
}

func cloneAdaptationPlan(plan domain.AdaptationPlan) domain.AdaptationPlan {
	out := plan
	out.MainlineRules = append([]string(nil), plan.MainlineRules...)
	out.RelationshipGoals = append([]string(nil), plan.RelationshipGoals...)
	out.Rules = append([]domain.AdaptationRule(nil), plan.Rules...)
	out.SourceEvents = append([]domain.AdaptationEvent(nil), plan.SourceEvents...)
	out.TargetEventLedger = append([]domain.AdaptationEvent(nil), plan.TargetEventLedger...)
	out.TargetSettingLocks = append([]domain.AdaptationSettingLock(nil), plan.TargetSettingLocks...)
	if plan.TargetRelationshipStates != nil {
		out.TargetRelationshipStates = make(map[string]string, len(plan.TargetRelationshipStates))
		for key, value := range plan.TargetRelationshipStates {
			out.TargetRelationshipStates[key] = value
		}
	}
	out.Volumes = append([]domain.AdaptationVolumePlan(nil), plan.Volumes...)
	out.Chapters = make([]domain.AdaptationChapterPlan, len(plan.Chapters))
	for i := range plan.Chapters {
		out.Chapters[i] = cloneAdaptationChapterPlan(plan.Chapters[i])
	}
	if plan.Planner != nil {
		planner := *plan.Planner
		planner.Notes = append(domain.TextList(nil), plan.Planner.Notes...)
		out.Planner = &planner
	}
	return out
}

func cloneAdaptationChapterPlan(chapter domain.AdaptationChapterPlan) domain.AdaptationChapterPlan {
	out := chapter
	out.SourceChapters = append([]int(nil), chapter.SourceChapters...)
	out.SourceSegments = append([]domain.AdaptationSourceSegment(nil), chapter.SourceSegments...)
	out.EventIDs = append([]string(nil), chapter.EventIDs...)
	out.AddedEventIDs = append([]string(nil), chapter.AddedEventIDs...)
	out.DependsOnEventIDs = append([]string(nil), chapter.DependsOnEventIDs...)
	out.RuleIDs = append([]string(nil), chapter.RuleIDs...)
	out.SettingClaims = append([]domain.AdaptationSettingClaim(nil), chapter.SettingClaims...)
	if chapter.Relationship != nil {
		relationship := *chapter.Relationship
		relationship.AllowedFrom = append([]string(nil), chapter.Relationship.AllowedFrom...)
		relationship.RequiresEventIDs = append([]string(nil), chapter.Relationship.RequiresEventIDs...)
		out.Relationship = &relationship
	}
	out.Scenes = append([]string(nil), chapter.Scenes...)
	out.PreserveEvents = append([]string(nil), chapter.PreserveEvents...)
	out.RequiredChanges = append([]string(nil), chapter.RequiredChanges...)
	out.ForbiddenMoves = append([]string(nil), chapter.ForbiddenMoves...)
	if chapter.WordBudget != nil {
		budget := *chapter.WordBudget
		out.WordBudget = &budget
	}
	return out
}

func resolveProposalRevisionRange(proposal domain.AdaptationPlan, opts ProposalRevisionOptions) (int, int, error) {
	chapterCount := len(proposal.Chapters)
	if chapterCount == 0 {
		return 0, 0, fmt.Errorf("adaptation proposal has no chapters")
	}
	if opts.VolumeIndex > 0 {
		return revisionRangeFromVolume(proposal, opts.VolumeIndex)
	}
	if opts.VolumeIndex < 0 {
		return 1, chapterCount, nil
	}
	if opts.FromChapter > 0 || opts.ToChapter > 0 {
		from := opts.FromChapter
		to := opts.ToChapter
		if to <= 0 {
			to = from
		}
		return normalizeRevisionChapterRange(from, to, chapterCount)
	}
	target := strings.TrimSpace(opts.Target)
	if target == "" {
		return 0, 0, fmt.Errorf("revision target is required")
	}
	lower := strings.ToLower(target)
	if target == "全卷" || target == "全部卷" || strings.Contains(lower, "all volumes") || strings.Contains(lower, "all-volumes") || strings.Contains(lower, "all_volumes") {
		return 1, chapterCount, nil
	}
	if strings.Contains(target, "卷") || strings.Contains(lower, "volume") || strings.HasPrefix(lower, "vol") {
		index := parseFlexiblePositiveInt(target)
		if index <= 0 {
			return 0, 0, fmt.Errorf("revision volume target %q is invalid", target)
		}
		return revisionRangeFromVolume(proposal, index)
	}
	numbers := positiveIntsFromText(target)
	if len(numbers) == 0 {
		return 0, 0, fmt.Errorf("revision target %q must name a chapter, range, or volume", target)
	}
	from := numbers[0]
	to := from
	if len(numbers) > 1 {
		to = numbers[1]
	}
	return normalizeRevisionChapterRange(from, to, chapterCount)
}

func revisionRangeFromVolume(proposal domain.AdaptationPlan, index int) (int, int, error) {
	volumes := normalizeAdaptationProposalVolumes(proposal.Volumes, len(proposal.Chapters))
	for _, volume := range volumes {
		if volume.Index == index {
			return normalizeRevisionChapterRange(volume.TargetFrom, volume.TargetTo, len(proposal.Chapters))
		}
	}
	return 0, 0, fmt.Errorf("volume %d not found in adaptation proposal", index)
}

func normalizeRevisionChapterRange(from, to, chapterCount int) (int, int, error) {
	if from <= 0 {
		return 0, 0, fmt.Errorf("revision chapter range must start at a positive chapter")
	}
	if to <= 0 {
		to = from
	}
	if from > to {
		from, to = to, from
	}
	if to > chapterCount {
		return 0, 0, fmt.Errorf("revision chapter range %d-%d exceeds proposal chapter count %d", from, to, chapterCount)
	}
	return from, to, nil
}

func positiveIntsFromText(text string) []int {
	var numbers []int
	var token strings.Builder
	flush := func() {
		if token.Len() == 0 {
			return
		}
		if value := parseFlexiblePositiveInt(token.String()); value > 0 {
			numbers = append(numbers, value)
		}
		token.Reset()
	}
	for _, r := range text {
		if (r >= '0' && r <= '9') || isChineseChapterNumberRune(r) {
			token.WriteRune(r)
			continue
		}
		flush()
	}
	flush()
	return numbers
}

func proposalRevisionBatch(plan domain.AdaptationPlan, from, to int) plannerSkeletonBatch {
	sourceFrom, sourceTo := sourceRangeForProposalChapters(plan.Chapters, from, to)
	return plannerSkeletonBatch{
		Index:              1,
		TargetFrom:         from,
		TargetTo:           to,
		TargetChapterCount: to - from + 1,
		SourceFrom:         sourceFrom,
		SourceTo:           sourceTo,
		Title:              fmt.Sprintf("revision %d-%d", from, to),
		Summary:            "revise the selected proposal chapters",
	}
}

func proposalRevisionVolumeBatch(plan domain.AdaptationPlan, volumeIndex, from, to int) plannerSkeletonBatch {
	batch := proposalRevisionBatch(plan, from, to)
	batch.Index = volumeIndex
	for _, volume := range normalizeAdaptationProposalVolumes(plan.Volumes, len(plan.Chapters)) {
		if volume.Index != volumeIndex {
			continue
		}
		batch.Title = volume.Title
		batch.Theme = volume.Theme
		batch.Goal = volume.Goal
		batch.Summary = volume.Summary
		batch.BudgetDecision = volume.BudgetDecision
		batch.BudgetReason = volume.BudgetReason
		batch.Notes = append([]string(nil), volume.Notes...)
		if volume.SourceFrom > 0 {
			batch.SourceFrom = volume.SourceFrom
		}
		if volume.SourceTo > 0 {
			batch.SourceTo = volume.SourceTo
		}
		return batch
	}
	return batch
}

func volumeReviewBatch(review domain.AdaptationVolumeReview, volumeIndex int) (plannerSkeletonBatch, error) {
	volumes := normalizeAdaptationProposalVolumes(review.Volumes, review.TargetChapterCount)
	for _, volume := range volumes {
		if volume.Index != volumeIndex {
			continue
		}
		return plannerSkeletonBatch{
			Index:              volume.Index,
			Title:              volume.Title,
			Theme:              volume.Theme,
			Goal:               volume.Goal,
			Summary:            volume.Summary,
			BudgetDecision:     volume.BudgetDecision,
			BudgetReason:       volume.BudgetReason,
			TargetFrom:         volume.TargetFrom,
			TargetTo:           volume.TargetTo,
			TargetChapterCount: volume.TargetTo - volume.TargetFrom + 1,
			SourceFrom:         volume.SourceFrom,
			SourceTo:           volume.SourceTo,
			MainlineEventIDs:   append([]string(nil), volume.MainlineEventIDs...),
			Notes:              append([]string(nil), volume.Notes...),
		}, nil
	}
	return plannerSkeletonBatch{}, fmt.Errorf("volume %d not found in adaptation volume review", volumeIndex)
}

func cloneAdaptationVolumeReview(review domain.AdaptationVolumeReview) domain.AdaptationVolumeReview {
	out := review
	out.MainlineRules = append([]string(nil), review.MainlineRules...)
	out.RelationshipGoals = append([]string(nil), review.RelationshipGoals...)
	out.Volumes = append([]domain.AdaptationVolumePlan(nil), review.Volumes...)
	out.Planner = clonePlannerRuntimeMeta(review.Planner)
	return out
}

func applyVolumeReviewBatchRevision(review *domain.AdaptationVolumeReview, original, revised plannerSkeletonBatch) {
	if review == nil {
		return
	}
	delta := revised.TargetTo - original.TargetTo
	for idx := range review.Volumes {
		volume := &review.Volumes[idx]
		switch {
		case volume.Index == original.Index:
			if title := strings.TrimSpace(revised.Title); title != "" {
				volume.Title = title
			}
			if theme := strings.TrimSpace(revised.Theme); theme != "" {
				volume.Theme = theme
			}
			if goal := strings.TrimSpace(revised.Goal); goal != "" {
				volume.Goal = goal
			}
			if summary := strings.TrimSpace(revised.Summary); summary != "" {
				volume.Summary = summary
			}
			volume.BudgetDecision = normalizePlannerBudgetDecision(revised.BudgetDecision)
			volume.BudgetReason = strings.TrimSpace(revised.BudgetReason)
			volume.Notes = append(domain.TextList(nil), revised.Notes...)
			volume.TargetFrom = revised.TargetFrom
			volume.TargetTo = revised.TargetTo
			if revised.SourceFrom > 0 {
				volume.SourceFrom = revised.SourceFrom
			}
			if revised.SourceTo > 0 {
				volume.SourceTo = revised.SourceTo
			}
		case volume.TargetFrom > original.TargetTo:
			volume.TargetFrom += delta
			volume.TargetTo += delta
		}
	}
	review.TargetChapterCount += delta
	if review.TargetChapterCount < 0 {
		review.TargetChapterCount = revised.TargetTo
	}
	review.Volumes = normalizeAdaptationProposalVolumes(review.Volumes, review.TargetChapterCount)
	review.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
}

func repairVolumeReviewBudgetSplitsBeforeDetails(
	ctx context.Context,
	deps Deps,
	review domain.AdaptationVolumeReview,
	opts ProposalOptions,
	manifest *domain.AdaptationSourceManifest,
	reports []domain.AdaptationSourceReport,
	emit ProgressEmitter,
) (domain.AdaptationVolumeReview, plannerSkeleton, bool, error) {
	skeleton := plannerSkeletonFromVolumeReview(review)
	repaired := false
	budgetQualityAttempts := deps.budgetQualityMaxAttempts()
	maxRepairs := max(1, len(review.Volumes)) * budgetQualityAttempts
	if maxRepairs <= 0 {
		maxRepairs = adaptationPlannerBudgetQualityAttempts
	}
	rangeRepairAttempts := map[string]int{}
	for attempt := 0; attempt <= maxRepairs; attempt++ {
		splitErrs := plannerSkeletonBudgetSplitErrors(skeleton.Batches, opts, manifest)
		if len(splitErrs) == 0 {
			if repaired {
				if review.Planner == nil {
					review.Planner = &domain.AdaptationPlannerMeta{}
				}
				review.Planner.Notes = append(review.Planner.Notes, "auto repaired source-range chapter budget before detail planning")
				if err := validateAdaptationVolumeReview(review, manifest); err != nil {
					return review, skeleton, repaired, err
				}
				if err := deps.Store.Adaptation.SaveVolumeReview(review); err != nil {
					return review, skeleton, repaired, fmt.Errorf("save auto-repaired adaptation volume review: %w", err)
				}
			}
			return review, skeleton, repaired, nil
		}
		if attempt >= maxRepairs {
			sortPlannerProposalBudgetSplitErrors(splitErrs)
			return review, skeleton, repaired, splitErrs
		}
		sortPlannerProposalBudgetSplitErrors(splitErrs)
		budgetErr := splitErrs[0]
		originalBatch, ok := plannerSkeletonBatchForBudgetSplitError(skeleton.Batches, budgetErr)
		if !ok {
			return review, skeleton, repaired, fmt.Errorf("no volume found for budget-invalid source range %d-%d", budgetErr.SourceFrom, budgetErr.SourceTo)
		}
		rangeKey := plannerBudgetRangeKey(budgetErr.SourceFrom, budgetErr.SourceTo)
		rangeRepairAttempts[rangeKey]++
		minTargetTo := originalBatch.TargetFrom + budgetErr.MinChapters - 1
		expansionMaxTo := max(originalBatch.TargetTo+adaptationPlannerRevisionExpansionMax, minTargetTo)
		systemPrompt := adaptationPlannerSystemPrompt(deps)
		if systemPrompt == "" {
			systemPrompt = "# Adaptation Planner\n\nReturn only JSON for the requested volume review budget repair."
		}
		repairOpts := ProposalRevisionOptions{
			VolumeIndex: originalBatch.Index,
			Instruction: fmt.Sprintf(
				"Repair this volume before chapter-detail generation: source range %d-%d has %d source_runes and needs at least %d target chapters under the chapter budget. Keep the same source range and expand or rebalance this volume; later volumes will shift automatically.",
				budgetErr.SourceFrom,
				budgetErr.SourceTo,
				budgetErr.SourceRunes,
				budgetErr.MinChapters,
			),
			EmitProgress: emit,
		}
		emitAdaptProgress(emit, StagePlan, attempt+1, maxRepairs, fmt.Sprintf("Volume %d source range %d-%d needs at least %d target chapters before detail planning; repairing volume skeleton", originalBatch.Index, budgetErr.SourceFrom, budgetErr.SourceTo, budgetErr.MinChapters), &budgetErr)
		prompt, err := buildAdaptationVolumeBudgetRepairPrompt(repairOpts, review, originalBatch, budgetErr, expansionMaxTo, manifest, reportsForPlannerBatch(reports, originalBatch))
		if err != nil {
			return review, skeleton, repaired, err
		}
		text, err := generatePlannerText(
			ctx,
			deps.modelForStage("skeleton"),
			systemPrompt,
			prompt,
			adaptationPlannerSkeletonMaxTokens,
			emit,
			attempt+1,
			maxRepairs,
			fmt.Sprintf("volume %d budget repair", originalBatch.Index),
			deps.modelCallMaxAttempts(),
		)
		if err != nil {
			return review, skeleton, repaired, fmt.Errorf("planner volume %d budget repair llm generate: %w", originalBatch.Index, err)
		}
		revisedBatch, err := collectProposalVolumeRevisionSkeletonWithRepair(
			ctx,
			deps.modelForStage("skeleton"),
			systemPrompt,
			prompt,
			text,
			originalBatch,
			expansionMaxTo,
			true,
			manifest,
			minTargetTo,
			true,
			emit,
			deps.structureRepairMaxAttempts(),
			deps.modelCallMaxAttempts(),
		)
		if err != nil {
			return review, skeleton, repaired, fmt.Errorf("planner volume %d budget repair skeleton: %w", originalBatch.Index, err)
		}
		if revisedBatch.TargetTo < minTargetTo && rangeRepairAttempts[rangeKey] >= budgetQualityAttempts {
			markPlannerBatchBudgetDeviationAccepted(&revisedBatch)
		}
		applyVolumeReviewBatchRevision(&review, originalBatch, revisedBatch)
		skeleton = plannerSkeletonFromVolumeReview(review)
		repaired = true
	}
	return review, skeleton, repaired, fmt.Errorf("volume budget repair exhausted")
}

func plannerSkeletonBatchForBudgetSplitError(batches []plannerSkeletonBatch, budgetErr plannerProposalBudgetSplitError) (plannerSkeletonBatch, bool) {
	for _, batch := range batches {
		if budgetErr.FirstChapter > 0 && budgetErr.FirstChapter >= batch.TargetFrom && budgetErr.FirstChapter <= batch.TargetTo {
			return batch, true
		}
		if budgetErr.SourceFrom >= batch.SourceFrom && budgetErr.SourceTo <= batch.SourceTo {
			return batch, true
		}
	}
	return plannerSkeletonBatch{}, false
}

func validateAdaptationVolumeReview(review domain.AdaptationVolumeReview, manifest *domain.AdaptationSourceManifest) error {
	if review.Status == "" {
		review.Status = domain.AdaptationPlanStatusVolumeReview
	}
	if review.Status != domain.AdaptationPlanStatusVolumeReview {
		return fmt.Errorf("volume review status=%q, want volume_review", review.Status)
	}
	if strings.TrimSpace(review.Brief) == "" {
		return fmt.Errorf("volume review brief is empty")
	}
	if review.TargetChapterCount <= 0 {
		return fmt.Errorf("volume review target_chapter_count must be > 0")
	}
	if !adaptationVolumesCoverChapters(review.Volumes, review.TargetChapterCount) {
		return fmt.Errorf("volume review volumes must continuously cover chapters 1-%d", review.TargetChapterCount)
	}
	if manifest != nil && manifest.ChapterCount > 0 {
		for _, volume := range review.Volumes {
			if volume.SourceFrom <= 0 || volume.SourceTo < volume.SourceFrom || volume.SourceTo > manifest.ChapterCount {
				return fmt.Errorf("volume %d has invalid source range %d-%d", volume.Index, volume.SourceFrom, volume.SourceTo)
			}
		}
	}
	if budgetErrs := plannerSkeletonBudgetSplitErrors(plannerSkeletonFromVolumeReview(review).Batches, proposalOptionsFromVolumeReview(review), manifest); len(budgetErrs) > 0 {
		sortPlannerProposalBudgetSplitErrors(budgetErrs)
		return budgetErrs
	}
	return nil
}

func sourceRangeForProposalChapters(chapters []domain.AdaptationChapterPlan, from, to int) (int, int) {
	sourceFrom, sourceTo := 0, 0
	for _, chapter := range chapters {
		if chapter.Chapter < from || chapter.Chapter > to {
			continue
		}
		values := append([]int(nil), chapter.SourceChapters...)
		if chapter.SourceRange.From > 0 {
			values = append(values, chapter.SourceRange.From)
		}
		if chapter.SourceRange.To > 0 {
			values = append(values, chapter.SourceRange.To)
		}
		minSource, maxSource := minMaxPositive(values)
		if minSource > 0 && (sourceFrom == 0 || minSource < sourceFrom) {
			sourceFrom = minSource
		}
		if maxSource > sourceTo {
			sourceTo = maxSource
		}
	}
	return sourceFrom, sourceTo
}

func proposalOptionsFromPlan(plan domain.AdaptationPlan) ProposalOptions {
	granularity := domain.NormalizeAdaptationGranularity(plan.Granularity)
	return ProposalOptions{
		Brief:         strings.TrimSpace(plan.Brief),
		Granularity:   granularity,
		RewritePolicy: domain.AdaptationRewritePolicyForGranularity(granularity),
		WordTolerance: plan.WordTolerance,
	}
}

func proposalOptionsFromVolumeReview(review domain.AdaptationVolumeReview) ProposalOptions {
	granularity := domain.NormalizeAdaptationGranularity(review.Granularity)
	return ProposalOptions{
		Brief:              strings.TrimSpace(review.Brief),
		SourcePath:         strings.TrimSpace(review.SourcePath),
		Granularity:        granularity,
		RewritePolicy:      domain.AdaptationRewritePolicyForGranularity(granularity),
		WordTolerance:      review.WordTolerance,
		TargetChapterCount: review.TargetChapterCount,
	}
}

func plannerSkeletonFromVolumeReview(review domain.AdaptationVolumeReview) plannerSkeleton {
	batches := make([]plannerSkeletonBatch, 0, len(review.Volumes))
	for _, volume := range normalizeAdaptationProposalVolumes(review.Volumes, review.TargetChapterCount) {
		batches = append(batches, plannerSkeletonBatch{
			Index:              volume.Index,
			Title:              volume.Title,
			Theme:              volume.Theme,
			Goal:               volume.Goal,
			Summary:            volume.Summary,
			BudgetDecision:     volume.BudgetDecision,
			BudgetReason:       volume.BudgetReason,
			TargetFrom:         volume.TargetFrom,
			TargetTo:           volume.TargetTo,
			TargetChapterCount: volume.TargetTo - volume.TargetFrom + 1,
			SourceFrom:         volume.SourceFrom,
			SourceTo:           volume.SourceTo,
			MainlineEventIDs:   append([]string(nil), volume.MainlineEventIDs...),
			Notes:              append([]string(nil), volume.Notes...),
		})
	}
	return plannerSkeleton{
		Granularity:        review.Granularity,
		Status:             domain.AdaptationPlanStatusProposal,
		RewritePolicy:      review.RewritePolicy,
		Brief:              review.Brief,
		TargetChapterCount: review.TargetChapterCount,
		MainlineRules:      append([]string(nil), review.MainlineRules...),
		RelationshipGoals:  append([]string(nil), review.RelationshipGoals...),
		Batches:            batches,
		Planner:            clonePlannerRuntimeMeta(review.Planner),
	}
}

func replaceProposalChapterRange(plan *domain.AdaptationPlan, from, to, maxTo int, chapters []domain.AdaptationChapterPlan) (int, error) {
	if plan == nil {
		return 0, fmt.Errorf("proposal is nil")
	}
	if maxTo < to {
		maxTo = to
	}
	minCount := to - from + 1
	maxCount := maxTo - from + 1
	if len(chapters) < minCount || len(chapters) > maxCount {
		if minCount == maxCount {
			return 0, fmt.Errorf("revised chapter count=%d, want %d", len(chapters), minCount)
		}
		return 0, fmt.Errorf("revised chapter count=%d, want %d-%d", len(chapters), minCount, maxCount)
	}
	revisedTo := from + len(chapters) - 1
	for idx := range chapters {
		want := from + idx
		if chapters[idx].Chapter != want {
			return 0, fmt.Errorf("revised chapter %d at index %d, want %d", chapters[idx].Chapter, idx, want)
		}
	}
	delta := len(chapters) - minCount
	next := make([]domain.AdaptationChapterPlan, 0, len(plan.Chapters)+delta)
	for _, existing := range plan.Chapters {
		if existing.Chapter < from {
			next = append(next, cloneAdaptationChapterPlan(existing))
		}
	}
	for _, revised := range chapters {
		next = append(next, cloneAdaptationChapterPlan(revised))
	}
	for _, existing := range plan.Chapters {
		if existing.Chapter <= to {
			continue
		}
		shifted := cloneAdaptationChapterPlan(existing)
		shiftAdaptationChapterPlanNumber(&shifted, delta)
		next = append(next, shifted)
	}
	sort.SliceStable(next, func(i, j int) bool {
		return next[i].Chapter < next[j].Chapter
	})
	plan.Chapters = next
	shiftProposalVolumesForReplacement(plan, from, to, revisedTo, delta)
	return revisedTo, nil
}

func shiftAdaptationChapterPlanNumber(chapter *domain.AdaptationChapterPlan, delta int) {
	if chapter == nil || delta == 0 {
		return
	}
	chapter.Chapter += delta
	if chapter.OutlineEntry.Chapter > 0 {
		chapter.OutlineEntry.Chapter += delta
	}
}

func shiftProposalVolumesForReplacement(plan *domain.AdaptationPlan, from, to, revisedTo, delta int) {
	if plan == nil || len(plan.Volumes) == 0 {
		return
	}
	for idx := range plan.Volumes {
		volume := &plan.Volumes[idx]
		switch {
		case volume.TargetFrom == from && volume.TargetTo == to:
			volume.TargetTo = revisedTo
		case volume.TargetFrom > to:
			volume.TargetFrom += delta
			volume.TargetTo += delta
		case volume.TargetFrom <= from && volume.TargetTo >= to:
			volume.TargetTo += delta
		case volume.TargetTo > to:
			volume.TargetTo += delta
		}
		if volume.TargetFrom > 0 && volume.TargetTo >= volume.TargetFrom {
			sourceFrom, sourceTo := sourceRangeForProposalChapters(plan.Chapters, volume.TargetFrom, volume.TargetTo)
			if sourceFrom > 0 {
				volume.SourceFrom = sourceFrom
			}
			if sourceTo > 0 {
				volume.SourceTo = sourceTo
			}
		}
	}
}

func applyProposalVolumeRevisionMetadata(plan *domain.AdaptationPlan, volumeIndex int, batch plannerSkeletonBatch) {
	if plan == nil || volumeIndex <= 0 {
		return
	}
	for idx := range plan.Volumes {
		if plan.Volumes[idx].Index != volumeIndex {
			continue
		}
		volume := &plan.Volumes[idx]
		if title := strings.TrimSpace(batch.Title); title != "" {
			volume.Title = title
		}
		if theme := strings.TrimSpace(batch.Theme); theme != "" {
			volume.Theme = theme
		}
		if goal := strings.TrimSpace(batch.Goal); goal != "" {
			volume.Goal = goal
		}
		if summary := strings.TrimSpace(batch.Summary); summary != "" {
			volume.Summary = summary
		}
		volume.TargetFrom = batch.TargetFrom
		volume.TargetTo = batch.TargetTo
		if batch.SourceFrom > 0 {
			volume.SourceFrom = batch.SourceFrom
		}
		if batch.SourceTo > 0 {
			volume.SourceTo = batch.SourceTo
		}
		return
	}
}

func shouldAllowProposalRevisionExpansion(proposal domain.AdaptationPlan, opts ProposalRevisionOptions, from, to int) bool {
	if len(proposal.Chapters) == 0 {
		return false
	}
	if from <= 0 || from > to {
		return false
	}
	if opts.VolumeIndex > 0 {
		return true
	}
	if opts.VolumeIndex <= 0 && to != len(proposal.Chapters) {
		return false
	}
	return proposalRevisionInstructionRequestsExpansion(opts.Instruction)
}

func proposalRevisionInstructionRequestsExpansion(instruction string) bool {
	text := strings.ToLower(strings.TrimSpace(instruction))
	if text == "" {
		return false
	}
	keywords := []string{
		"add chapter",
		"add chapters",
		"append chapter",
		"append chapters",
		"extra chapter",
		"extra chapters",
		"new chapter",
		"new chapters",
		"extend",
		"expand",
		"epilogue",
		"ending",
		"finale",
		"补充",
		"新增",
		"添加",
		"增加",
		"扩展",
		"扩写",
		"加章",
		"补章",
		"结尾",
		"尾声",
		"终章",
		"收束",
	}
	for _, keyword := range keywords {
		if strings.Contains(text, keyword) {
			return true
		}
	}
	return false
}

func normalizeProposalVolumeExpansionDecision(decision string) string {
	text := strings.ToLower(strings.TrimSpace(decision))
	if text == "" {
		return ""
	}
	keepWords := []string{
		"keep", "same", "unchanged", "no expansion", "no change", "remain", "fixed",
		"保持", "不变", "不扩", "无需", "原章数", "维持",
	}
	for _, word := range keepWords {
		if strings.Contains(text, word) {
			return "keep"
		}
	}
	expandWords := []string{
		"expand", "expanded", "increase", "increased", "add", "append", "extra", "more", "new",
		"扩章", "增加", "新增", "添加", "加章", "补章", "扩展", "扩写",
	}
	for _, word := range expandWords {
		if strings.Contains(text, word) {
			return "expand"
		}
	}
	return text
}

func proposalRevisionChanged(original, updated domain.AdaptationPlan) bool {
	if len(original.Chapters) != len(updated.Chapters) {
		return true
	}
	for idx := range original.Chapters {
		if !adaptationChapterPlansEqual(original.Chapters[idx], updated.Chapters[idx]) {
			return true
		}
	}
	return false
}

func adaptationChapterPlansEqual(left, right domain.AdaptationChapterPlan) bool {
	leftJSON, leftErr := json.Marshal(left)
	rightJSON, rightErr := json.Marshal(right)
	if leftErr != nil || rightErr != nil {
		return false
	}
	return string(leftJSON) == string(rightJSON)
}

func buildAdaptationProposalRevisionUserPrompt(
	opts ProposalRevisionOptions,
	proposal domain.AdaptationPlan,
	batch plannerSkeletonBatch,
	expansionMaxTo int,
	manifest *domain.AdaptationSourceManifest,
	sourceFoundation *domain.AdaptationSourceFoundation,
	reports []domain.AdaptationSourceReport,
) (string, error) {
	if expansionMaxTo < batch.TargetTo {
		expansionMaxTo = batch.TargetTo
	}
	expansionAllowed := expansionMaxTo > batch.TargetTo
	selected := proposalChaptersInRange(proposal.Chapters, batch.TargetFrom, batch.TargetTo)
	before := proposalChapterByNumber(proposal.Chapters, batch.TargetFrom-1)
	after := proposalChapterByNumber(proposal.Chapters, batch.TargetTo+1)
	requirements := []string{
		"Return exactly one JSON object and no prose.",
		"The top-level JSON object must be {\"chapters\":[...]} and must not be a single chapter object.",
		"Keep source_chapters anchors valid and preserve essential source events unless the user's instruction explicitly changes emphasis.",
		"Maintain continuity with neighbor_before and neighbor_after.",
		"Every returned chapter must include chapter, title, core_event, hook, scenes, source_chapters, source_range, word_budget, preserve_events, required_changes, and forbidden_moves.",
	}
	outputContract := fmt.Sprintf(
		"Return exactly %d chapter objects, numbered with integer chapter values from %d through %d.",
		batch.TargetTo-batch.TargetFrom+1,
		batch.TargetFrom,
		batch.TargetTo,
	)
	if expansionAllowed {
		requirements = append(requirements,
			"Return the complete selected range and any appended ending chapters as one continuous chapter list.",
			"Existing selected chapters must keep their original chapter numbers.",
			"Append new ending chapters only if needed by the user's instruction.",
			"Appended chapters must continue sequentially after original_target_to and must not exceed target_to_max.",
			"Do not leave chapter-number gaps.",
		)
		outputContract = fmt.Sprintf(
			"Return at least %d and at most %d chapter objects, numbered continuously from %d through the final returned chapter. The final returned chapter must be between %d and %d.",
			batch.TargetTo-batch.TargetFrom+1,
			expansionMaxTo-batch.TargetFrom+1,
			batch.TargetFrom,
			batch.TargetTo,
			expansionMaxTo,
		)
	} else {
		requirements = append(requirements,
			"Return only the selected target chapters, but return the complete selected range.",
			"Do not change chapter numbers or chapter count.",
			"Use integer chapter values from target_from through target_to.",
		)
	}
	input := struct {
		Instruction       string                             `json:"instruction"`
		TargetFrom        int                                `json:"target_from"`
		TargetTo          int                                `json:"target_to"`
		ExpansionAllowed  bool                               `json:"expansion_allowed,omitempty"`
		OriginalTargetTo  int                                `json:"original_target_to,omitempty"`
		TargetToMax       int                                `json:"target_to_max,omitempty"`
		Granularity       string                             `json:"granularity"`
		RewritePolicy     string                             `json:"rewrite_policy"`
		Brief             string                             `json:"brief"`
		MainlineRules     []string                           `json:"mainline_rules,omitempty"`
		RelationshipGoals []string                           `json:"relationship_goals,omitempty"`
		Volumes           []domain.AdaptationVolumePlan      `json:"volumes,omitempty"`
		NeighborBefore    *domain.AdaptationChapterPlan      `json:"neighbor_before,omitempty"`
		NeighborAfter     *domain.AdaptationChapterPlan      `json:"neighbor_after,omitempty"`
		SelectedChapters  []domain.AdaptationChapterPlan     `json:"selected_chapters"`
		SourceManifest    *domain.AdaptationSourceManifest   `json:"source_manifest"`
		SourceFoundation  *domain.AdaptationSourceFoundation `json:"source_foundation"`
		SourceReports     []domain.AdaptationSourceReport    `json:"source_reports"`
		Requirements      []string                           `json:"requirements"`
	}{
		Instruction:       strings.TrimSpace(opts.Instruction),
		TargetFrom:        batch.TargetFrom,
		TargetTo:          batch.TargetTo,
		ExpansionAllowed:  expansionAllowed,
		OriginalTargetTo:  batch.TargetTo,
		TargetToMax:       expansionMaxTo,
		Granularity:       proposal.Granularity,
		RewritePolicy:     proposal.RewritePolicy,
		Brief:             proposal.Brief,
		MainlineRules:     append([]string(nil), proposal.MainlineRules...),
		RelationshipGoals: append([]string(nil), proposal.RelationshipGoals...),
		Volumes:           normalizeAdaptationProposalVolumes(proposal.Volumes, len(proposal.Chapters)),
		NeighborBefore:    before,
		NeighborAfter:     after,
		SelectedChapters:  selected,
		SourceManifest:    manifest,
		SourceFoundation:  sourceFoundation,
		SourceReports:     reports,
		Requirements:      requirements,
	}
	raw, err := json.MarshalIndent(input, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal proposal revision input: %w", err)
	}
	return fmt.Sprintf(
		"Revise the selected adaptation proposal chapters using the user's instruction. Keep the rest of the proposal unchanged.\n\n"+
			"Output contract: return exactly one JSON object, no prose and no markdown. The top-level object must be {\"chapters\":[...]}.\n"+
			"%s\n"+
			"Invalid shapes: {\"chapter\":%d,...}; {\"summary\":\"...\"}; {\"key_turns\":[...]}; markdown text outside JSON.\n\n"+
			"Revision input:\n```json\n%s\n```",
		outputContract,
		batch.TargetFrom,
		string(raw),
	), nil
}

func buildAdaptationProposalVolumeRevisionSkeletonPrompt(
	opts ProposalRevisionOptions,
	proposal domain.AdaptationPlan,
	volume plannerSkeletonBatch,
	expansionMaxTo int,
	manifest *domain.AdaptationSourceManifest,
	sourceFoundation *domain.AdaptationSourceFoundation,
	reports []domain.AdaptationSourceReport,
) (string, error) {
	if expansionMaxTo < volume.TargetTo {
		expansionMaxTo = volume.TargetTo
	}
	expansionAllowed := expansionMaxTo > volume.TargetTo
	selected := proposalChaptersInRange(proposal.Chapters, volume.TargetFrom, volume.TargetTo)
	before := proposalChapterByNumber(proposal.Chapters, volume.TargetFrom-1)
	after := proposalChapterByNumber(proposal.Chapters, volume.TargetTo+1)
	requirements := []string{
		"Return exactly one JSON object and no prose.",
		"Do not wrap the JSON in markdown fences.",
		"Return only a high-level revised volume/batch skeleton; do not include chapter details.",
		"The top-level object must contain a batches array with exactly one batch.",
		"That batch must keep target_from equal to the original volume target_from.",
		"That batch must include target_from, target_to, source_from, source_to, title, theme or goal, and summary.",
		"source_from and source_to must stay within the analyzed source manifest.",
		"Use the user's revision instruction to re-plan the volume's plot structure before detailed chapter planning.",
	}
	if expansionAllowed {
		requirements = append(requirements,
			`You must decide whether the revision needs more chapter slots. Set expansion_decision to "expand" or "keep".`,
			`Use "expand" when the requested story change needs added chapters, extra relationship beats, daily romance scenes, epilogue-like life stages, marriage, pregnancy, childbirth, or other new plot space.`,
			`Use "keep" only when the requested change can be fully handled inside the current chapter count without compressing or losing the user's intent.`,
			`If expansion_decision is "expand", increase target_to for this volume; target_to must not exceed target_to_max.`,
			`If expansion_decision is "keep", target_to must remain original_target_to.`,
			"Do not leave gaps; later volumes will be shifted by the application.",
		)
	} else {
		requirements = append(requirements,
			"Do not change target_to or chapter count for this volume.",
		)
	}
	input := struct {
		Instruction       string                             `json:"instruction"`
		ExpansionAllowed  bool                               `json:"expansion_allowed"`
		OriginalTargetTo  int                                `json:"original_target_to"`
		TargetToMax       int                                `json:"target_to_max"`
		Granularity       string                             `json:"granularity"`
		RewritePolicy     string                             `json:"rewrite_policy"`
		Brief             string                             `json:"brief"`
		MainlineRules     []string                           `json:"mainline_rules,omitempty"`
		RelationshipGoals []string                           `json:"relationship_goals,omitempty"`
		CurrentVolume     plannerSkeletonBatch               `json:"current_volume"`
		AllVolumes        []domain.AdaptationVolumePlan      `json:"all_volumes,omitempty"`
		NeighborBefore    *domain.AdaptationChapterPlan      `json:"neighbor_before,omitempty"`
		NeighborAfter     *domain.AdaptationChapterPlan      `json:"neighbor_after,omitempty"`
		SelectedChapters  []domain.AdaptationChapterPlan     `json:"selected_chapters"`
		SourceManifest    *domain.AdaptationSourceManifest   `json:"source_manifest"`
		SourceFoundation  *domain.AdaptationSourceFoundation `json:"source_foundation"`
		SourceReports     []domain.AdaptationSourceReport    `json:"source_reports"`
		Requirements      []string                           `json:"requirements"`
	}{
		Instruction:       strings.TrimSpace(opts.Instruction),
		ExpansionAllowed:  expansionAllowed,
		OriginalTargetTo:  volume.TargetTo,
		TargetToMax:       expansionMaxTo,
		Granularity:       proposal.Granularity,
		RewritePolicy:     proposal.RewritePolicy,
		Brief:             proposal.Brief,
		MainlineRules:     append([]string(nil), proposal.MainlineRules...),
		RelationshipGoals: append([]string(nil), proposal.RelationshipGoals...),
		CurrentVolume:     volume,
		AllVolumes:        normalizeAdaptationProposalVolumes(proposal.Volumes, len(proposal.Chapters)),
		NeighborBefore:    before,
		NeighborAfter:     after,
		SelectedChapters:  selected,
		SourceManifest:    manifest,
		SourceFoundation:  sourceFoundation,
		SourceReports:     reports,
		Requirements:      requirements,
	}
	raw, err := json.MarshalIndent(input, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal volume revision skeleton input: %w", err)
	}
	return fmt.Sprintf(
		"Re-plan the selected adaptation proposal volume before detailed chapter planning.\n\n"+
			"Output contract: return exactly one JSON object, no prose and no markdown. The top-level object must contain a batches array with exactly one revised volume batch. Do not return chapter details here.\n"+
			"Required JSON shape: {\"granularity\":\"%s\",\"status\":\"proposal\",\"rewrite_policy\":\"%s\",\"brief\":\"...\",\"target_chapter_count\":%d,\"batches\":[{\"index\":%d,\"title\":\"...\",\"theme\":\"...\",\"expansion_decision\":\"expand|keep\",\"expansion_reason\":\"...\",\"target_from\":%d,\"target_to\":%d,\"source_from\":%d,\"source_to\":%d,\"summary\":\"...\"}]}.\n\n"+
			"Volume revision input:\n```json\n%s\n```",
		proposal.Granularity,
		proposal.RewritePolicy,
		volume.TargetTo-volume.TargetFrom+1,
		volume.Index,
		volume.TargetFrom,
		volume.TargetTo,
		volume.SourceFrom,
		volume.SourceTo,
		string(raw),
	), nil
}

func buildAdaptationVolumeReviewRevisionPrompt(
	opts ProposalRevisionOptions,
	review domain.AdaptationVolumeReview,
	volume plannerSkeletonBatch,
	expansionMaxTo int,
	manifest *domain.AdaptationSourceManifest,
	sourceFoundation *domain.AdaptationSourceFoundation,
	reports []domain.AdaptationSourceReport,
) (string, error) {
	if expansionMaxTo < volume.TargetTo {
		expansionMaxTo = volume.TargetTo
	}
	requirements := []string{
		"Return exactly one JSON object and no prose.",
		"Do not wrap the JSON in markdown fences.",
		"Return only a high-level revised volume/batch skeleton; do not include chapter details.",
		"The top-level object must contain a batches array with exactly one batch.",
		"That batch must keep target_from equal to the original volume target_from.",
		"That batch must include target_from, target_to, source_from, source_to, title, theme or goal, summary, and expansion_decision.",
		"source_from and source_to must stay within the analyzed source manifest.",
		"Use the user's revision instruction to re-plan this volume's plot structure before detailed chapter planning.",
		`You must decide whether the revision needs more chapter slots. Set expansion_decision to "expand" or "keep".`,
		`Use "expand" when the requested story change needs added chapters, extra relationship beats, daily romance scenes, epilogue-like life stages, marriage, pregnancy, childbirth, or other new plot space.`,
		`Use "keep" only when the requested change can be fully handled inside the current chapter count without compressing or losing the user's intent.`,
		`If expansion_decision is "expand", increase target_to for this volume; target_to must not exceed target_to_max.`,
		`If expansion_decision is "keep", target_to must remain original_target_to.`,
		"Do not leave gaps; later volumes will be shifted by the application.",
	}
	input := struct {
		Instruction       string                             `json:"instruction"`
		ExpansionAllowed  bool                               `json:"expansion_allowed"`
		OriginalTargetTo  int                                `json:"original_target_to"`
		TargetToMax       int                                `json:"target_to_max"`
		Granularity       string                             `json:"granularity"`
		RewritePolicy     string                             `json:"rewrite_policy"`
		Brief             string                             `json:"brief"`
		MainlineRules     []string                           `json:"mainline_rules,omitempty"`
		RelationshipGoals []string                           `json:"relationship_goals,omitempty"`
		CurrentVolume     plannerSkeletonBatch               `json:"current_volume"`
		AllVolumes        []domain.AdaptationVolumePlan      `json:"all_volumes"`
		SourceManifest    *domain.AdaptationSourceManifest   `json:"source_manifest"`
		SourceFoundation  *domain.AdaptationSourceFoundation `json:"source_foundation"`
		SourceReports     []domain.AdaptationSourceReport    `json:"source_reports"`
		Requirements      []string                           `json:"requirements"`
	}{
		Instruction:       strings.TrimSpace(opts.Instruction),
		ExpansionAllowed:  true,
		OriginalTargetTo:  volume.TargetTo,
		TargetToMax:       expansionMaxTo,
		Granularity:       review.Granularity,
		RewritePolicy:     review.RewritePolicy,
		Brief:             review.Brief,
		MainlineRules:     append([]string(nil), review.MainlineRules...),
		RelationshipGoals: append([]string(nil), review.RelationshipGoals...),
		CurrentVolume:     volume,
		AllVolumes:        normalizeAdaptationProposalVolumes(review.Volumes, review.TargetChapterCount),
		SourceManifest:    manifest,
		SourceFoundation:  sourceFoundation,
		SourceReports:     reports,
		Requirements:      requirements,
	}
	raw, err := json.MarshalIndent(input, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal volume review revision input: %w", err)
	}
	return fmt.Sprintf(
		"Revise the selected adaptation volume review before detailed chapter planning.\n\n"+
			"Output contract: return exactly one JSON object, no prose and no markdown. The top-level object must contain a batches array with exactly one revised volume batch. Do not return chapter details here.\n"+
			"Required JSON shape: {\"granularity\":\"%s\",\"status\":\"volume_review\",\"rewrite_policy\":\"%s\",\"brief\":\"...\",\"target_chapter_count\":%d,\"batches\":[{\"index\":%d,\"title\":\"...\",\"theme\":\"...\",\"expansion_decision\":\"expand|keep\",\"expansion_reason\":\"...\",\"target_from\":%d,\"target_to\":%d,\"source_from\":%d,\"source_to\":%d,\"summary\":\"...\"}]}.\n\n"+
			"Volume review revision input:\n```json\n%s\n```",
		review.Granularity,
		review.RewritePolicy,
		volume.TargetTo-volume.TargetFrom+1,
		volume.Index,
		volume.TargetFrom,
		volume.TargetTo,
		volume.SourceFrom,
		volume.SourceTo,
		string(raw),
	), nil
}

type plannerVolumeBudgetRepairIssue struct {
	SourceFrom            int `json:"source_from"`
	SourceTo              int `json:"source_to"`
	SourceRunes           int `json:"source_runes"`
	CurrentTargetChapters int `json:"current_target_chapters"`
	MinTargetChapters     int `json:"min_target_chapters"`
	OriginalTargetTo      int `json:"original_target_to"`
	MinTargetTo           int `json:"min_target_to"`
	TargetToMax           int `json:"target_to_max"`
}

type plannerVolumeBudgetRepairSourceContext struct {
	SourceFrom              int                           `json:"source_from"`
	SourceTo                int                           `json:"source_to"`
	SourceRunes             int                           `json:"source_runes"`
	ChapterSummaries        []plannerSourceChapterSummary `json:"chapter_summaries,omitempty"`
	OmittedChapterSummaries int                           `json:"omitted_chapter_summaries,omitempty"`
	ReportExcerpts          []plannerSourceReportExcerpt  `json:"report_excerpts,omitempty"`
	OmittedReportExcerpts   int                           `json:"omitted_report_excerpts,omitempty"`
}

type plannerVolumeBudgetRepairVolumeSummary struct {
	Index          int    `json:"index"`
	Title          string `json:"title,omitempty"`
	Theme          string `json:"theme,omitempty"`
	Goal           string `json:"goal,omitempty"`
	Summary        string `json:"summary,omitempty"`
	BudgetDecision string `json:"budget_decision,omitempty"`
	BudgetReason   string `json:"budget_reason,omitempty"`
	TargetFrom     int    `json:"target_from"`
	TargetTo       int    `json:"target_to"`
	SourceFrom     int    `json:"source_from,omitempty"`
	SourceTo       int    `json:"source_to,omitempty"`
}

func buildAdaptationVolumeBudgetRepairPrompt(
	opts ProposalRevisionOptions,
	review domain.AdaptationVolumeReview,
	volume plannerSkeletonBatch,
	budgetErr plannerProposalBudgetSplitError,
	expansionMaxTo int,
	manifest *domain.AdaptationSourceManifest,
	reports []domain.AdaptationSourceReport,
) (string, error) {
	if expansionMaxTo < volume.TargetTo {
		expansionMaxTo = volume.TargetTo
	}
	currentChapters := plannerSkeletonBatchChapterCount(volume)
	minTargetTo := volume.TargetFrom + budgetErr.MinChapters - 1
	sourceRunes := budgetErr.SourceRunes
	if sourceRunes <= 0 {
		sourceRunes = sourceRunesForRange(sourceRunesByChapter(manifest), budgetErr.SourceFrom, budgetErr.SourceTo)
	}
	requirements := []string{
		"Return exactly one JSON object and no prose.",
		"Do not wrap the JSON in markdown fences.",
		"Return only a high-level revised volume/batch skeleton; do not include chapter details.",
		"The top-level object must contain a batches array with exactly one batch.",
		"That batch must keep target_from equal to the original volume target_from.",
		"That batch must keep source_from and source_to equal to the budget issue source range.",
		`Set expansion_decision to "expand" only when increasing target_to; otherwise use "keep".`,
		"If the source range is still closely rewritten or preserved, expand target_to enough to satisfy min_target_chapters.",
		"If the same lower chapter count is intentional because the rewrite deletes, merges, or compresses source material, keep target_to unchanged and state that rationale in summary or expansion_reason.",
		"Do not solve the budget issue by shrinking source_from/source_to; the host will reject that.",
		"Use only this compact volume context; do not ask for raw source text or all-book context.",
	}
	chapterSummaries, omittedChapters := plannerSourceChapterSummariesInRange(manifest, budgetErr.SourceFrom, budgetErr.SourceTo, adaptationVolumeBudgetRepairChapterMax)
	reportExcerpts, omittedReports := plannerBudgetRepairReportExcerpts(reports, adaptationVolumeBudgetRepairReportMax)
	input := struct {
		Instruction       string                                   `json:"instruction"`
		Granularity       string                                   `json:"granularity"`
		RewritePolicy     string                                   `json:"rewrite_policy"`
		Brief             string                                   `json:"brief"`
		MainlineRules     []string                                 `json:"mainline_rules,omitempty"`
		RelationshipGoals []string                                 `json:"relationship_goals,omitempty"`
		BudgetIssue       plannerVolumeBudgetRepairIssue           `json:"budget_issue"`
		CurrentVolume     plannerVolumeBudgetRepairVolumeSummary   `json:"current_volume"`
		NeighborVolumes   []plannerVolumeBudgetRepairVolumeSummary `json:"neighbor_volumes,omitempty"`
		SourceManifest    plannerSourceManifestSummary             `json:"source_manifest"`
		SourceRange       plannerVolumeBudgetRepairSourceContext   `json:"source_range"`
		Requirements      []string                                 `json:"requirements"`
	}{
		Instruction:       strings.TrimSpace(opts.Instruction),
		Granularity:       review.Granularity,
		RewritePolicy:     review.RewritePolicy,
		Brief:             clipText(review.Brief, 3000),
		MainlineRules:     clippedStringList(review.MainlineRules, 16, 240),
		RelationshipGoals: clippedStringList(review.RelationshipGoals, 16, 240),
		BudgetIssue: plannerVolumeBudgetRepairIssue{
			SourceFrom:            budgetErr.SourceFrom,
			SourceTo:              budgetErr.SourceTo,
			SourceRunes:           sourceRunes,
			CurrentTargetChapters: currentChapters,
			MinTargetChapters:     budgetErr.MinChapters,
			OriginalTargetTo:      volume.TargetTo,
			MinTargetTo:           minTargetTo,
			TargetToMax:           expansionMaxTo,
		},
		CurrentVolume:   plannerSkeletonBudgetRepairVolumeSummary(volume),
		NeighborVolumes: plannerVolumeBudgetRepairNeighborSummaries(review.Volumes, volume.Index, adaptationVolumeBudgetRepairNeighborMax),
		SourceManifest:  plannerManifestSummary(manifest),
		SourceRange: plannerVolumeBudgetRepairSourceContext{
			SourceFrom:              budgetErr.SourceFrom,
			SourceTo:                budgetErr.SourceTo,
			SourceRunes:             sourceRunes,
			ChapterSummaries:        chapterSummaries,
			OmittedChapterSummaries: omittedChapters,
			ReportExcerpts:          reportExcerpts,
			OmittedReportExcerpts:   omittedReports,
		},
		Requirements: requirements,
	}
	raw, err := json.MarshalIndent(input, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal volume budget repair input: %w", err)
	}
	return fmt.Sprintf(
		"Repair one adaptation volume budget before detailed chapter planning.\n\n"+
			"Output contract: return exactly one JSON object, no prose and no markdown. The top-level object must contain a batches array with exactly one revised volume batch. Do not return chapter details here.\n"+
			"Required JSON shape: {\"granularity\":\"%s\",\"status\":\"volume_review\",\"rewrite_policy\":\"%s\",\"brief\":\"...\",\"target_chapter_count\":%d,\"batches\":[{\"index\":%d,\"title\":\"...\",\"theme\":\"...\",\"expansion_decision\":\"expand|keep\",\"expansion_reason\":\"...\",\"target_from\":%d,\"target_to\":%d,\"source_from\":%d,\"source_to\":%d,\"summary\":\"...\"}]}.\n\n"+
			"Compact budget repair input:\n```json\n%s\n```",
		review.Granularity,
		review.RewritePolicy,
		currentChapters,
		volume.Index,
		volume.TargetFrom,
		volume.TargetTo,
		budgetErr.SourceFrom,
		budgetErr.SourceTo,
		string(raw),
	), nil
}

func proposalChaptersInRange(chapters []domain.AdaptationChapterPlan, from, to int) []domain.AdaptationChapterPlan {
	out := make([]domain.AdaptationChapterPlan, 0, to-from+1)
	for _, chapter := range chapters {
		if chapter.Chapter >= from && chapter.Chapter <= to {
			out = append(out, cloneAdaptationChapterPlan(chapter))
		}
	}
	return out
}

func proposalChapterByNumber(chapters []domain.AdaptationChapterPlan, number int) *domain.AdaptationChapterPlan {
	if number <= 0 {
		return nil
	}
	for _, chapter := range chapters {
		if chapter.Chapter == number {
			copy := cloneAdaptationChapterPlan(chapter)
			return &copy
		}
	}
	return nil
}

func normalizeTargetChapterCount(values ...int) int {
	for _, value := range values {
		if value <= 0 {
			continue
		}
		if value > adaptationPlannerTargetChapterMax {
			return adaptationPlannerTargetChapterMax
		}
		return value
	}
	return 0
}

func inferTargetChapterCount(brief string) int {
	brief = strings.TrimSpace(brief)
	if brief == "" {
		return 0
	}
	best := 0
	for _, match := range targetChapterRangePattern.FindAllStringSubmatchIndex(brief, -1) {
		if precededByChapterAnchorPrefix(brief, match[0]) {
			continue
		}
		from := parseRegexInt(brief, match, 1)
		to := parseRegexInt(brief, match, 2)
		if from <= 0 || to <= 0 {
			continue
		}
		if from > to {
			from, to = to, from
		}
		best = max(best, to)
	}
	for _, match := range targetChapterSinglePattern.FindAllStringSubmatchIndex(brief, -1) {
		if precededByChapterAnchorPrefix(brief, match[0]) {
			continue
		}
		value := parseRegexInt(brief, match, 1)
		if value <= 0 {
			continue
		}
		if parseRegexText(brief, match, 2) != "" {
			value += 5
		}
		best = max(best, value)
	}
	for _, match := range targetChapterChineseLoosePattern.FindAllStringSubmatchIndex(brief, -1) {
		if precededByChapterAnchorPrefix(brief, match[0]) {
			continue
		}
		high := parseChineseChapterNumber(parseRegexText(brief, match, 2) + "十")
		best = max(best, high)
	}
	for _, match := range targetChapterChinesePattern.FindAllStringSubmatchIndex(brief, -1) {
		if precededByChapterAnchorPrefix(brief, match[0]) {
			continue
		}
		value := parseChineseChapterNumber(parseRegexText(brief, match, 1))
		if value <= 0 {
			continue
		}
		if parseRegexText(brief, match, 2) != "" {
			value += 5
		}
		best = max(best, value)
	}
	return normalizeTargetChapterCount(best)
}

func parseRegexInt(text string, match []int, group int) int {
	value, _ := strconv.Atoi(parseRegexText(text, match, group))
	return value
}

func parseRegexText(text string, match []int, group int) string {
	offset := group * 2
	if offset+1 >= len(match) || match[offset] < 0 || match[offset+1] < 0 {
		return ""
	}
	return strings.TrimSpace(text[match[offset]:match[offset+1]])
}

func precededByOrdinalPrefix(text string, start int) bool {
	if start <= 0 || start > len(text) {
		return false
	}
	prefix := strings.TrimRightFunc(text[:start], unicode.IsSpace)
	r, _ := utf8.DecodeLastRuneInString(prefix)
	return r == '第'
}

func precededByChapterAnchorPrefix(text string, start int) bool {
	if precededByOrdinalPrefix(text, start) {
		return true
	}
	if start <= 0 || start > len(text) {
		return false
	}
	prefix := strings.ToLower(strings.TrimRightFunc(text[:start], unicode.IsSpace))
	return hasChapterAnchorPrefix(prefix) || precededByChapterAnchorRangeContinuation(prefix)
}

func hasChapterAnchorPrefix(prefix string) bool {
	return strings.HasSuffix(prefix, "第") || strings.HasSuffix(prefix, "ch") || strings.HasSuffix(prefix, "chapter")
}

func precededByChapterAnchorRangeContinuation(prefix string) bool {
	if prefix == "" {
		return false
	}
	trimmed := strings.TrimRightFunc(prefix, unicode.IsSpace)
	if trimmed == "" {
		return false
	}
	last, size := utf8.DecodeLastRuneInString(trimmed)
	if !isChapterRangeSeparator(last) {
		return false
	}
	beforeSeparator := strings.TrimRightFunc(trimmed[:len(trimmed)-size], unicode.IsSpace)
	beforeNumber := strings.TrimRightFunc(beforeSeparator, func(r rune) bool {
		return r >= '0' && r <= '9'
	})
	if beforeNumber == beforeSeparator {
		return false
	}
	return hasChapterAnchorPrefix(strings.TrimRightFunc(beforeNumber, unicode.IsSpace))
}

func isChapterRangeSeparator(r rune) bool {
	switch r {
	case '-', '~', '～', '—', '–', '－', '至', '到':
		return true
	default:
		return false
	}
}

func parseChineseChapterNumber(text string) int {
	text = strings.TrimSpace(text)
	if text == "" {
		return 0
	}
	if strings.Contains(text, "百") {
		parts := strings.SplitN(text, "百", 2)
		hundreds := parseChineseDigit(parts[0])
		if hundreds <= 0 {
			hundreds = 1
		}
		return hundreds*100 + parseChineseChapterNumber(parts[1])
	}
	if strings.Contains(text, "十") {
		parts := strings.SplitN(text, "十", 2)
		tens := parseChineseDigit(parts[0])
		if tens <= 0 {
			tens = 1
		}
		return tens*10 + parseChineseDigit(parts[1])
	}
	return parseChineseDigit(text)
}

func parseChineseDigit(text string) int {
	switch strings.TrimSpace(text) {
	case "":
		return 0
	case "一":
		return 1
	case "二", "两":
		return 2
	case "三":
		return 3
	case "四":
		return 4
	case "五":
		return 5
	case "六":
		return 6
	case "七":
		return 7
	case "八":
		return 8
	case "九":
		return 9
	default:
		return 0
	}
}

func buildPlanFromPlannerSingle(
	ctx context.Context,
	deps Deps,
	opts ProposalOptions,
	reports []domain.AdaptationSourceReport,
	manifest *domain.AdaptationSourceManifest,
	sourceFoundation *domain.AdaptationSourceFoundation,
) (domain.AdaptationPlan, error) {
	var zero domain.AdaptationPlan
	if deps.LLM == nil {
		return zero, fmt.Errorf("planner llm is required for %s adaptation proposals", opts.Granularity)
	}
	systemPrompt := adaptationPlannerSystemPrompt(deps)
	if systemPrompt == "" {
		systemPrompt = "# Adaptation Planner\n\nReturn only one JSON adaptation plan proposal."
	}
	userPrompt, err := buildAdaptationPlannerUserPrompt(opts, reports, manifest, sourceFoundation)
	if err != nil {
		return zero, err
	}
	detailModel := deps.modelForStage("detail_outline")
	responseText, err := generatePlannerTextForStage(
		ctx, StagePlan, detailModel, systemPrompt, userPrompt, adaptationPlannerMaxTokens,
		opts.EmitProgress, 0, 0, "短篇改编规划", deps.modelCallMaxAttempts(),
	)
	if err != nil {
		return zero, fmt.Errorf("planner llm generate: %w", err)
	}
	proposal, err := parsePlannerProposal(responseText)
	if err != nil {
		return zero, plannerUnusableOutputError{err: err}
	}
	if err := validatePlannerProposal(&proposal, opts, reports, manifest, detailModel); err != nil {
		_ = deps.Store.Adaptation.ClearProposalRuntime()
		return zero, err
	}
	return proposal, nil
}

type plannerSkeleton struct {
	Granularity        string                        `json:"granularity"`
	Status             string                        `json:"status"`
	RewritePolicy      string                        `json:"rewrite_policy"`
	Brief              string                        `json:"brief"`
	TargetChapterCount int                           `json:"target_chapter_count"`
	MainlineRules      []string                      `json:"mainline_rules,omitempty"`
	RelationshipGoals  []string                      `json:"relationship_goals,omitempty"`
	Batches            []plannerSkeletonBatch        `json:"batches"`
	Planner            *domain.AdaptationPlannerMeta `json:"planner,omitempty"`
}

type plannerSkeletonBatch struct {
	Index                      int      `json:"index"`
	Title                      string   `json:"title,omitempty"`
	Theme                      string   `json:"theme,omitempty"`
	Goal                       string   `json:"goal,omitempty"`
	Summary                    string   `json:"summary,omitempty"`
	ExpansionDecision          string   `json:"expansion_decision,omitempty"`
	ExpansionReason            string   `json:"expansion_reason,omitempty"`
	BudgetDecision             string   `json:"budget_decision,omitempty"`
	BudgetReason               string   `json:"budget_reason,omitempty"`
	TargetFrom                 int      `json:"target_from"`
	TargetTo                   int      `json:"target_to"`
	TargetChapterCount         int      `json:"chapter_count,omitempty"`
	DetailParentFrom           int      `json:"-"`
	DetailParentTo             int      `json:"-"`
	SourceFrom                 int      `json:"source_from"`
	SourceTo                   int      `json:"source_to"`
	SourceChapters             []int    `json:"source_chapters,omitempty"`
	MainlineEventIDs           []string `json:"mainline_event_ids,omitempty"`
	AllowedEventIDs            []string `json:"allowed_event_ids,omitempty"`
	DetailEventContractVersion int      `json:"detail_event_contract_version,omitempty"`
	// PriorOwnedEventIDs is populated only for an in-flight detail call. It
	// makes already accepted source-event ownership explicit to the planner
	// rather than asking a later global audit to infer it from a collision.
	PriorOwnedEventIDs []string `json:"prior_owned_event_ids,omitempty"`
	Notes              []string `json:"notes,omitempty"`
}

func (b *plannerSkeletonBatch) UnmarshalJSON(data []byte) error {
	type batchAlias plannerSkeletonBatch
	var raw batchAlias
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	*b = plannerSkeletonBatch(raw)
	var object map[string]json.RawMessage
	if err := json.Unmarshal(data, &object); err != nil {
		return nil
	}
	b.TargetFrom = firstJSONInt(object, b.TargetFrom, "target_from", "targetStart", "target_start", "start_chapter", "from_chapter", "first_chapter")
	b.TargetTo = firstJSONInt(object, b.TargetTo, "target_to", "targetEnd", "target_end", "end_chapter", "to_chapter", "last_chapter")
	b.TargetChapterCount = firstJSONInt(object, b.TargetChapterCount, "chapter_count", "target_chapter_count", "chapters", "count")
	b.SourceFrom = firstJSONInt(object, b.SourceFrom, "source_from", "sourceStart", "source_start", "source_chapter_from")
	b.SourceTo = firstJSONInt(object, b.SourceTo, "source_to", "sourceEnd", "source_end", "source_chapter_to")
	b.ExpansionDecision = firstJSONString(object, b.ExpansionDecision, "expansion_decision", "expansionDecision", "chapter_count_decision", "chapterCountDecision")
	b.ExpansionReason = firstJSONString(object, b.ExpansionReason, "expansion_reason", "expansionReason", "chapter_count_reason", "chapterCountReason")
	b.BudgetDecision = normalizePlannerBudgetDecision(firstJSONString(object, b.BudgetDecision, "budget_decision", "budgetDecision", "budget_review_decision", "budgetReviewDecision"))
	b.BudgetReason = strings.TrimSpace(firstJSONString(object, b.BudgetReason, "budget_reason", "budgetReason", "budget_review_reason", "budgetReviewReason"))
	if rawRange := object["target_range"]; len(rawRange) > 0 {
		var r struct {
			From  int `json:"from"`
			To    int `json:"to"`
			Start int `json:"start"`
			End   int `json:"end"`
		}
		if err := json.Unmarshal(rawRange, &r); err == nil {
			if b.TargetFrom <= 0 {
				b.TargetFrom = firstPositiveInt(r.From, r.Start)
			}
			if b.TargetTo <= 0 {
				b.TargetTo = firstPositiveInt(r.To, r.End)
			}
		}
	}
	if rawRange := object["source_range"]; len(rawRange) > 0 {
		var r struct {
			From  int `json:"from"`
			To    int `json:"to"`
			Start int `json:"start"`
			End   int `json:"end"`
		}
		if err := json.Unmarshal(rawRange, &r); err == nil {
			if b.SourceFrom <= 0 {
				b.SourceFrom = firstPositiveInt(r.From, r.Start)
			}
			if b.SourceTo <= 0 {
				b.SourceTo = firstPositiveInt(r.To, r.End)
			}
		}
	}
	return nil
}

func buildPlanFromPlannerChunked(
	ctx context.Context,
	deps Deps,
	opts ProposalOptions,
	reports []domain.AdaptationSourceReport,
	manifest *domain.AdaptationSourceManifest,
	sourceFoundation *domain.AdaptationSourceFoundation,
	targetChapterHint int,
) (domain.AdaptationPlan, error) {
	var zero domain.AdaptationPlan
	skeleton, runtime, err := buildPlannerVolumeSkeleton(ctx, deps, opts, reports, manifest, sourceFoundation, targetChapterHint)
	if err != nil {
		return zero, err
	}
	return buildPlanFromPlannerSkeletonDetails(ctx, deps, opts, reports, manifest, sourceFoundation, skeleton, runtime)
}

func buildPlannerVolumeSkeleton(
	ctx context.Context,
	deps Deps,
	opts ProposalOptions,
	reports []domain.AdaptationSourceReport,
	manifest *domain.AdaptationSourceManifest,
	sourceFoundation *domain.AdaptationSourceFoundation,
	targetChapterHint int,
) (plannerSkeleton, *domain.AdaptationProposalRuntime, error) {
	var zero plannerSkeleton
	if deps.LLM == nil {
		return zero, nil, fmt.Errorf("planner llm is required for %s adaptation proposals", opts.Granularity)
	}
	systemPrompt := adaptationPlannerSystemPrompt(deps)
	if systemPrompt == "" {
		systemPrompt = "# Adaptation Planner\n\nReturn only JSON for the requested adaptation planning step."
	}
	runtime, runtimeSkeleton, err := loadPlannerProposalRuntime(deps, opts, manifest, targetChapterHint, opts.EmitProgress)
	if err != nil {
		return zero, nil, err
	}
	var skeleton plannerSkeleton
	if runtimeSkeleton != nil {
		skeleton = *runtimeSkeleton
		emitAdaptProgress(opts.EmitProgress, StagePlan, 0, 0, fmt.Sprintf("Resuming proposal skeleton runtime: %d target chapters", skeleton.TargetChapterCount), nil)
	} else {
		dossier, err := EnsureCoCreateDossier(ctx, deps, manifest, reports, opts.EmitProgress)
		if err != nil {
			return zero, nil, fmt.Errorf("prepare planner source map: %w", err)
		}
		emitAdaptProgress(opts.EmitProgress, StagePlan, 0, 0, plannerSkeletonRequestMessage(opts, manifest, targetChapterHint), nil)
		skeleton, err = buildPlannerVolumeSkeletonFromSourceMap(ctx, deps, opts, reports, manifest, sourceFoundation, dossier, runtime, targetChapterHint, systemPrompt)
		if err != nil {
			return zero, nil, err
		}
		runtime.Skeleton = plannerRuntimeOutlineFromSkeleton(skeleton)
		runtime.CompletedBatches = nil
		runtime.SkeletonBatches = nil
		if err := savePlannerProposalRuntime(deps, runtime); err != nil {
			return zero, nil, fmt.Errorf("save proposal runtime skeleton: %w", err)
		}
	}
	attachSkeletonMainlineEvents(&skeleton, reports)
	enablePlannerDetailEventContractsForFreshRuntime(&skeleton, runtime)
	if runtime != nil && !plannerRuntimeOutlineMatchesSkeleton(runtime.Skeleton, skeleton) {
		runtime.Skeleton = plannerRuntimeOutlineFromSkeleton(skeleton)
		if err := savePlannerProposalRuntime(deps, runtime); err != nil {
			return zero, nil, fmt.Errorf("save planner detail event contracts: %w", err)
		}
	}
	return skeleton, runtime, nil
}

func buildPlannerVolumeSkeletonFromSourceMap(
	ctx context.Context,
	deps Deps,
	opts ProposalOptions,
	reports []domain.AdaptationSourceReport,
	manifest *domain.AdaptationSourceManifest,
	sourceFoundation *domain.AdaptationSourceFoundation,
	dossier *domain.AdaptationCoCreateDossier,
	runtime *domain.AdaptationProposalRuntime,
	targetChapterHint int,
	systemPrompt string,
) (plannerSkeleton, error) {
	sourceMap := plannerSourceMapFromDossier(dossier, manifest, reports)
	if len(sourceMap) == 0 {
		return plannerSkeleton{}, fmt.Errorf("planner source map is empty")
	}
	sourceRunesByChapter := sourceRunesByChapter(manifest)
	batches := make([]plannerSkeletonBatch, 0, len(sourceMap))
	for sourceIndex, entry := range sourceMap {
		if err := ctx.Err(); err != nil {
			return plannerSkeleton{}, err
		}
		if reused, ok, reuseErr := plannerRuntimeSkeletonBatchesForSource(runtime, entry, opts.Granularity, sourceRunesByChapter); reuseErr != nil {
			removed := removePlannerProposalRuntimeSkeletonBatchesForSource(runtime, entry)
			if removed > 0 {
				if err := savePlannerProposalRuntime(deps, runtime); err != nil {
					return plannerSkeleton{}, fmt.Errorf("save proposal runtime after invalid skeleton batch %d: %w", entry.Index, err)
				}
			}
			emitAdaptProgress(opts.EmitProgress, StagePlan, sourceIndex+1, len(sourceMap), fmt.Sprintf("Discarded invalid cached skeleton planning batch %d/%d for source chapters %d-%d; replanning this source-map range", sourceIndex+1, len(sourceMap), entry.SourceFrom, entry.SourceTo), reuseErr)
		} else if ok {
			offsetPlannerSkeletonBatches(reused, nextPlannerSkeletonTarget(batches), len(batches)+1)
			batches = append(batches, reused...)
			upsertPlannerProposalRuntimeSkeletonBatches(runtime, entry, reused)
			if err := savePlannerProposalRuntime(deps, runtime); err != nil {
				return plannerSkeleton{}, fmt.Errorf("save reused proposal runtime skeleton batch %d: %w", entry.Index, err)
			}
			emitAdaptProgress(opts.EmitProgress, StagePlan, sourceIndex+1, len(sourceMap), fmt.Sprintf("复用骨架规划第 %d/%d 批：原书第 %d-%d 章", sourceIndex+1, len(sourceMap), entry.SourceFrom, entry.SourceTo), nil)
			continue
		}
		prompt, err := buildAdaptationPlannerSkeletonUserPrompt(opts, manifest, sourceFoundation, []plannerSourceMapEntry{entry}, targetChapterHint)
		if err != nil {
			return plannerSkeleton{}, err
		}
		emitAdaptProgress(opts.EmitProgress, StagePlan, sourceIndex+1, len(sourceMap), fmt.Sprintf("请求骨架规划第 %d/%d 批：原书第 %d-%d 章", sourceIndex+1, len(sourceMap), entry.SourceFrom, entry.SourceTo), nil)
		text, err := generatePlannerText(
			ctx,
			deps.modelForStage("skeleton"),
			systemPrompt,
			prompt,
			adaptationPlannerSkeletonMaxTokens,
			opts.EmitProgress,
			sourceIndex+1,
			len(sourceMap),
			fmt.Sprintf("骨架规划第 %d/%d 批", sourceIndex+1, len(sourceMap)),
			deps.modelCallMaxAttempts(),
		)
		if err != nil {
			return plannerSkeleton{}, fmt.Errorf("planner skeleton batch %d llm generate: %w", entry.Index, err)
		}
		local, err := collectPlannerSourceMapSkeletonBatches(ctx, deps.modelForStage("skeleton"), systemPrompt, prompt, text, entry, opts.Granularity, sourceRunesByChapter, opts.EmitProgress, sourceIndex+1, len(sourceMap), deps.budgetQualityMaxAttempts(), deps.structureRepairMaxAttempts(), deps.modelCallMaxAttempts())
		if err != nil {
			return plannerSkeleton{}, fmt.Errorf("planner skeleton batch %d: %w", entry.Index, err)
		}
		nextTarget := nextPlannerSkeletonTarget(batches)
		offsetPlannerSkeletonBatches(local, nextTarget, len(batches)+1)
		batches = append(batches, local...)
		upsertPlannerProposalRuntimeSkeletonBatches(runtime, entry, local)
		if err := savePlannerProposalRuntime(deps, runtime); err != nil {
			return plannerSkeleton{}, fmt.Errorf("save proposal runtime skeleton batch %d: %w", entry.Index, err)
		}
		emitAdaptProgress(opts.EmitProgress, StagePlan, sourceIndex+1, len(sourceMap), fmt.Sprintf("骨架规划第 %d/%d 批完成：新增目标第 %d-%d 章", sourceIndex+1, len(sourceMap), local[0].TargetFrom, local[len(local)-1].TargetTo), nil)
	}
	skeleton := plannerSkeleton{
		Granularity:        opts.Granularity,
		Status:             domain.AdaptationPlanStatusProposal,
		RewritePolicy:      opts.RewritePolicy,
		Brief:              opts.Brief,
		TargetChapterCount: nextPlannerSkeletonTarget(batches) - 1,
		Batches:            batches,
		Planner: &domain.AdaptationPlannerMeta{
			Prompt:        adaptationPlannerPromptName,
			PromptVersion: adaptationPlannerPromptVersion + "-source-map",
			GeneratedAt:   time.Now().UTC().Format(time.RFC3339),
			Notes: domain.TextList{
				fmt.Sprintf("source-map skeleton: %d source batches; model chose %d target chapters", len(sourceMap), nextPlannerSkeletonTarget(batches)-1),
			},
		},
	}
	if err := normalizePlannerSkeleton(&skeleton, opts, manifest, targetChapterHint); err != nil {
		return plannerSkeleton{}, fmt.Errorf("planner skeleton: %w", err)
	}
	return skeleton, nil
}

func collectPlannerSourceMapSkeletonBatches(
	ctx context.Context,
	llm imp.LLMChat,
	systemPrompt string,
	originalPrompt string,
	initialText string,
	entry plannerSourceMapEntry,
	granularity string,
	sourceRunesByChapter map[int]int,
	emit ProgressEmitter,
	current int,
	total int,
	maxQualityAttempts int,
	maxRepairAttempts int,
	maxModelCallAttempts int,
) ([]plannerSkeletonBatch, error) {
	if maxQualityAttempts <= 0 {
		maxQualityAttempts = adaptationPlannerBudgetQualityAttempts
	}
	if maxRepairAttempts <= 0 {
		maxRepairAttempts = adaptationPlannerRepairMaxAttempts
	}
	text := initialText
	var lastErr error
	qualityAttempts := 0
	structureAttempts := 0
	for {
		skeleton, err := parsePlannerSourceMapSkeleton(text)
		if err == nil {
			batches, berr := normalizePlannerSourceMapSkeletonBatchesForGranularityWithSourceRunes(skeleton.Batches, entry, granularity, sourceRunesByChapter)
			if berr == nil {
				return batches, nil
			}
			var budgetErr *plannerChapterBudgetQualityError
			if errors.As(berr, &budgetErr) {
				lastErr = budgetErr
				if qualityAttempts >= maxQualityAttempts {
					accepted, acceptErr := normalizePlannerSourceMapSkeletonBatchesAllowBudgetDeviationForGranularityWithSourceRunes(skeleton.Batches, entry, granularity, sourceRunesByChapter)
					if acceptErr == nil {
						emitAdaptProgress(emit, StagePlan, current, total, fmt.Sprintf("骨架规划第 %d/%d 批章节预算连续偏离预期，已按模型改编判断继续：%v", current, total, lastErr), lastErr)
						return accepted, nil
					}
					return nil, lastErr
				}
				qualityAttempts++
				emitAdaptProgress(emit, StagePlan, current, total, fmt.Sprintf("骨架规划第 %d/%d 批章节预算偏离预期，质量重试第 %d/%d 次：%v", current, total, qualityAttempts, maxQualityAttempts, lastErr), lastErr)
				reconsidered, err := retryPlannerSkeletonChapterBudget(ctx, llm, systemPrompt, originalPrompt, text, lastErr, granularity, emit, current, total, maxModelCallAttempts)
				if err != nil {
					return nil, err
				}
				text = reconsidered
				continue
			}
			err = berr
		}
		lastErr = err
		if !plannerSkeletonErrorRepairable(err) || structureAttempts >= maxRepairAttempts {
			return nil, lastErr
		}
		structureAttempts++
		emitAdaptProgress(emit, StagePlan, current, total, fmt.Sprintf("骨架规划第 %d/%d 批结构无效，正在修复第 %d/%d 次：%v", current, total, structureAttempts, maxRepairAttempts, lastErr), lastErr)
		repaired, err := repairPlannerSkeletonText(ctx, llm, systemPrompt, originalPrompt, text, lastErr, granularity, structureAttempts > 1, emit, current, total, maxModelCallAttempts)
		if err != nil {
			return nil, err
		}
		qualityAttempts = 0
		text = repaired
	}
}

func parsePlannerSourceMapSkeleton(text string) (plannerSkeleton, error) {
	data, err := extractPlannerSourceMapSkeletonJSON(text)
	if err != nil {
		return plannerSkeleton{}, fmt.Errorf("extract planner source-map skeleton JSON: %w", err)
	}
	skeleton, err := decodePlannerSourceMapSkeletonJSON(data)
	if err != nil {
		return plannerSkeleton{}, fmt.Errorf("decode planner source-map skeleton JSON: %w", err)
	}
	return skeleton, nil
}

func extractPlannerSourceMapSkeletonJSON(text string) ([]byte, error) {
	trimmed := strings.TrimSpace(strings.TrimPrefix(strings.ToValidUTF8(text, "\uFFFD"), "\uFEFF"))
	if trimmed == "" {
		return nil, fmt.Errorf("no JSON object found")
	}
	if strings.HasPrefix(trimmed, "{") {
		if end, ok := scanPlannerJSONEnd(trimmed); ok && end == len(trimmed) {
			return []byte(trimmed), nil
		}
	}
	segments, err := extractPlannerJSONSegmentRanges(text)
	if err != nil {
		return nil, fmt.Errorf("no JSON object found: %w", err)
	}
	segments = outermostPlannerJSONSegments(segments)
	switch len(segments) {
	case 0:
		return nil, fmt.Errorf("no JSON object found")
	case 1:
		return []byte(segments[0].text), nil
	default:
		return nil, errPlannerSourceMapMultipleJSON
	}
}

func outermostPlannerJSONSegments(segments []plannerJSONSegment) []plannerJSONSegment {
	out := make([]plannerJSONSegment, 0, len(segments))
	for i, segment := range segments {
		nested := false
		for j, other := range segments {
			if i == j {
				continue
			}
			if other.start < segment.start && segment.end < other.end {
				nested = true
				break
			}
		}
		if !nested {
			out = append(out, segment)
		}
	}
	return out
}

func decodePlannerSourceMapSkeletonJSON(data []byte) (plannerSkeleton, error) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(data, &object); err != nil {
		return plannerSkeleton{}, fmt.Errorf("invalid JSON decode: %w", err)
	}
	if key, raw, ok := firstPlannerSkeletonBatchAliasRaw(object); ok {
		return decodePlannerSourceMapSkeletonEnvelope(data, key, raw)
	}

	var firstNestedErr error
	for _, key := range plannerSourceMapSkeletonEnvelopeKeys() {
		raw := object[key]
		if rawJSONFirstByte(raw) != '{' {
			continue
		}
		nested, err := decodePlannerSourceMapSkeletonJSON(raw)
		if err == nil {
			return nested, nil
		}
		if firstNestedErr == nil {
			firstNestedErr = err
		}
	}
	if firstNestedErr != nil {
		return plannerSkeleton{}, firstNestedErr
	}
	if plannerSkeletonBatchObjectShape(object) {
		return plannerSkeleton{}, fmt.Errorf("planner source-map skeleton is a standalone batch object; expected top-level batches array")
	}
	return plannerSkeleton{}, fmt.Errorf("planner source-map skeleton missing top-level batches array (%s)", plannerProposalShapeSummary(data))
}

func decodePlannerSourceMapSkeletonEnvelope(data []byte, batchKey string, rawBatches json.RawMessage) (plannerSkeleton, error) {
	if rawJSONFirstByte(rawBatches) != '[' {
		if batchKey == "batches" {
			return plannerSkeleton{}, fmt.Errorf("planner source-map skeleton batches must be an array")
		}
		return plannerSkeleton{}, fmt.Errorf("planner source-map skeleton %s alias must be an array", batchKey)
	}
	var header struct {
		Granularity        string                        `json:"granularity"`
		Status             string                        `json:"status"`
		RewritePolicy      string                        `json:"rewrite_policy"`
		Brief              string                        `json:"brief"`
		TargetChapterCount int                           `json:"target_chapter_count"`
		MainlineRules      json.RawMessage               `json:"mainline_rules"`
		RelationshipGoals  json.RawMessage               `json:"relationship_goals"`
		Planner            *domain.AdaptationPlannerMeta `json:"planner,omitempty"`
	}
	if err := json.Unmarshal(data, &header); err != nil {
		return plannerSkeleton{}, fmt.Errorf("invalid JSON decode: %w", err)
	}
	var batches []plannerSkeletonBatch
	if err := json.Unmarshal(rawBatches, &batches); err != nil {
		return plannerSkeleton{}, fmt.Errorf("decode planner source-map skeleton %s array: %w", batchKey, err)
	}
	if len(batches) == 0 {
		return plannerSkeleton{}, fmt.Errorf("planner source-map skeleton has empty batches array")
	}
	mainlineRules, err := decodePlannerSourceMapStringList(header.MainlineRules, "mainline_rules")
	if err != nil {
		return plannerSkeleton{}, err
	}
	relationshipGoals, err := decodePlannerSourceMapStringList(header.RelationshipGoals, "relationship_goals")
	if err != nil {
		return plannerSkeleton{}, err
	}
	return plannerSkeleton{
		Granularity:        header.Granularity,
		Status:             header.Status,
		RewritePolicy:      header.RewritePolicy,
		Brief:              header.Brief,
		TargetChapterCount: header.TargetChapterCount,
		MainlineRules:      mainlineRules,
		RelationshipGoals:  relationshipGoals,
		Batches:            batches,
		Planner:            header.Planner,
	}, nil
}

func firstPlannerSkeletonBatchAliasRaw(object map[string]json.RawMessage) (string, json.RawMessage, bool) {
	for _, key := range plannerSkeletonBatchAliasKeys {
		raw, ok := object[key]
		if ok {
			return key, raw, true
		}
	}
	return "", nil, false
}

func plannerSourceMapSkeletonEnvelopeKeys() []string {
	keys := append([]string{}, plannerEnvelopeKeys...)
	keys = append(keys, "skeleton", "structure")
	return keys
}

func decodePlannerSourceMapStringList(raw json.RawMessage, field string) ([]string, error) {
	switch rawJSONFirstByte(raw) {
	case 0, 'n':
		return nil, nil
	case '[':
		var values []string
		if err := json.Unmarshal(raw, &values); err != nil {
			return nil, fmt.Errorf("planner source-map skeleton %s array must contain strings: %w", field, err)
		}
		return values, nil
	default:
		return nil, fmt.Errorf("planner source-map skeleton %s must be an array containing strings", field)
	}
}

func rawJSONFirstByte(raw json.RawMessage) byte {
	for _, b := range raw {
		if !unicode.IsSpace(rune(b)) {
			return b
		}
	}
	return 0
}

type plannerChapterBudgetQualityError struct {
	BatchIndex  int
	Count       int
	MinCount    int
	MaxCount    int
	SourceFrom  int
	SourceTo    int
	SourceRunes int
	Direction   string
}

func (e *plannerChapterBudgetQualityError) Error() string {
	if e == nil {
		return "chapter budget quality review required"
	}
	switch e.Direction {
	case "low":
		if e.SourceRunes > 0 {
			return fmt.Sprintf("source-map range %d-%d has %d source_runes but skeleton produced %d target chapters; arc/full_rewrite source-map skeleton capacity review expected at least %d target chapters when preserving detail; final word_budget.max_runes is validated separately",
				e.SourceFrom, e.SourceTo, e.SourceRunes, e.Count, e.MinCount)
		}
		return fmt.Sprintf("batch %d chapter_count=%d is below expected review floor %d for source range %d-%d", e.BatchIndex, e.Count, e.MinCount, e.SourceFrom, e.SourceTo)
	case "high":
		return fmt.Sprintf("batch %d chapter_count=%d is above expected review ceiling %d for source range %d-%d", e.BatchIndex, e.Count, e.MaxCount, e.SourceFrom, e.SourceTo)
	default:
		return fmt.Sprintf("batch %d chapter_count=%d needs budget quality review for source range %d-%d", e.BatchIndex, e.Count, e.SourceFrom, e.SourceTo)
	}
}

func plannerChapterBudgetRepairInstructions(err error) []string {
	var budgetErr *plannerChapterBudgetQualityError
	if !errors.As(err, &budgetErr) || budgetErr == nil {
		return nil
	}
	switch budgetErr.Direction {
	case "low":
		instructions := []string{
			fmt.Sprintf("The previous budget review says source range %d-%d may be under-planned: if this range keeps, expands, or closely rewrites the source material, the sum of chapter_count across returned batches covering that range should be at least %d.", budgetErr.SourceFrom, budgetErr.SourceTo, budgetErr.MinCount),
			"If this adaptation intentionally deletes, merges, weakens, skips, restructures, or compresses the source material, a lower chapter_count is allowed only when the batch sets budget_decision=\"compress_or_merge\" with non-empty budget_reason, or summary/notes/expansion_reason explicitly state that compression/deletion/merge/restructure rationale.",
		}
		if budgetErr.SourceRunes > 0 {
			instructions = append(instructions, fmt.Sprintf("That range has source_runes=%d, so the skeleton capacity review points to about %d target chapters when preserving detail; treat this as a review target, not a final word_budget.max_runes override.", budgetErr.SourceRunes, budgetErr.MinCount))
		}
		return instructions
	case "high":
		return []string{
			fmt.Sprintf("The previous budget review says source range %d-%d is over-planned: reduce chapter_count toward at most %d unless the batch sets budget_decision=\"expand_or_split\" with non-empty budget_reason, or summary/notes/expansion_reason gives a concrete expansion, split, new relationship, transition, or pacing reason.", budgetErr.SourceFrom, budgetErr.SourceTo, budgetErr.MaxCount),
			"Do not repeat the full source budget into every target chapter; each target chapter should own a distinct slice of the adapted material.",
		}
	default:
		return []string{
			fmt.Sprintf("Rebalance chapter_count for source range %d-%d according to the previous budget review error.", budgetErr.SourceFrom, budgetErr.SourceTo),
		}
	}
}

func plannerSourceMapBudgetNotes(entries []plannerSourceMapEntry, granularity string) []string {
	notes := make([]string, 0, len(entries))
	enforceSourceRuneSplitting := plannerEnforcesSourceRuneSplitting(granularity)
	for _, entry := range entries {
		minTargetCount := plannerSourceMapBudgetMinTargetChapters(entry, granularity)
		reviewCapacityRunes := plannerSourceMapSkeletonReviewCapacityRunes(granularity)
		if minTargetCount <= 1 || entry.SourceRunes <= reviewCapacityRunes {
			continue
		}
		if !enforceSourceRuneSplitting {
			notes = append(notes, fmt.Sprintf(
				"source_map entry %d range %d-%d has source_runes=%d; for free/full_rewrite, treat source_runes as density and context scale only, not as a minimum target chapter count. Choose chapter_count from the new story structure while keeping each target chapter word_budget.max_runes within %d runes.",
				entry.Index,
				entry.SourceFrom,
				entry.SourceTo,
				entry.SourceRunes,
				adaptationPlannerModelChapterMaxRunes,
			))
			continue
		}
		notes = append(notes, fmt.Sprintf(
			"source_map entry %d range %d-%d has source_runes=%d; for arc/full_rewrite source-map skeleton review, treat about %d source_runes as one target chapter of planning capacity, so returned batches covering this range should total at least %d chapter_count when preserving detail. Final chapter word_budget.max_runes still must stay within %d. If the adaptation intentionally deletes, merges, weakens, skips, restructures, or compresses this material, a lower chapter_count is allowed only with budget_decision=\"compress_or_merge\" plus budget_reason or an explicit compression/deletion/merge/restructure rationale in summary/notes/expansion_reason.",
			entry.Index,
			entry.SourceFrom,
			entry.SourceTo,
			entry.SourceRunes,
			reviewCapacityRunes,
			minTargetCount,
			adaptationPlannerModelChapterMaxRunes,
		))
	}
	return notes
}

func normalizePlannerSourceMapSkeletonBatches(batches []plannerSkeletonBatch, entry plannerSourceMapEntry) ([]plannerSkeletonBatch, error) {
	return normalizePlannerSourceMapSkeletonBatchesWithOptions(batches, entry, domain.AdaptationGranularityArc, false, true, nil)
}

func normalizePlannerSourceMapSkeletonBatchesAllowBudgetDeviation(batches []plannerSkeletonBatch, entry plannerSourceMapEntry) ([]plannerSkeletonBatch, error) {
	return normalizePlannerSourceMapSkeletonBatchesWithOptions(batches, entry, domain.AdaptationGranularityArc, true, true, nil)
}

func normalizePlannerSourceMapSkeletonBatchesForGranularity(batches []plannerSkeletonBatch, entry plannerSourceMapEntry, granularity string) ([]plannerSkeletonBatch, error) {
	return normalizePlannerSourceMapSkeletonBatchesForGranularityWithSourceRunes(batches, entry, granularity, nil)
}

func normalizePlannerSourceMapSkeletonBatchesForGranularityWithSourceRunes(batches []plannerSkeletonBatch, entry plannerSourceMapEntry, granularity string, sourceRunesByChapter map[int]int) ([]plannerSkeletonBatch, error) {
	if plannerAllowsSharedSourceMapEntryRanges(granularity) {
		return normalizePlannerSourceMapSkeletonBatchesAllowSharedRanges(batches, entry)
	}
	return normalizePlannerSourceMapSkeletonBatchesWithOptions(batches, entry, granularity, false, plannerEnforcesSourceRuneSplitting(granularity), sourceRunesByChapter)
}

func normalizePlannerSourceMapSkeletonBatchesAllowBudgetDeviationForGranularity(batches []plannerSkeletonBatch, entry plannerSourceMapEntry, granularity string) ([]plannerSkeletonBatch, error) {
	return normalizePlannerSourceMapSkeletonBatchesAllowBudgetDeviationForGranularityWithSourceRunes(batches, entry, granularity, nil)
}

func normalizePlannerSourceMapSkeletonBatchesAllowBudgetDeviationForGranularityWithSourceRunes(batches []plannerSkeletonBatch, entry plannerSourceMapEntry, granularity string, sourceRunesByChapter map[int]int) ([]plannerSkeletonBatch, error) {
	if plannerAllowsSharedSourceMapEntryRanges(granularity) {
		return normalizePlannerSourceMapSkeletonBatchesAllowSharedRanges(batches, entry)
	}
	return normalizePlannerSourceMapSkeletonBatchesWithOptions(batches, entry, granularity, true, plannerEnforcesSourceRuneSplitting(granularity), sourceRunesByChapter)
}

func normalizePlannerSourceMapSkeletonBatchesAllowSharedRanges(batches []plannerSkeletonBatch, entry plannerSourceMapEntry) ([]plannerSkeletonBatch, error) {
	if len(batches) == 0 {
		return nil, fmt.Errorf("no batches")
	}
	out := make([]plannerSkeletonBatch, 0, len(batches))
	for idx, batch := range batches {
		batch.BudgetDecision = normalizePlannerBudgetDecision(batch.BudgetDecision)
		batch.BudgetReason = strings.TrimSpace(batch.BudgetReason)
		if batch.SourceFrom <= 0 {
			batch.SourceFrom = entry.SourceFrom
		}
		if batch.SourceTo <= 0 {
			batch.SourceTo = entry.SourceTo
		}
		if batch.SourceTo < entry.SourceFrom || batch.SourceFrom > entry.SourceTo {
			continue
		}
		if batch.SourceFrom < entry.SourceFrom {
			batch.SourceFrom = entry.SourceFrom
		}
		if batch.SourceTo > entry.SourceTo {
			batch.SourceTo = entry.SourceTo
		}
		if batch.SourceTo < batch.SourceFrom {
			continue
		}
		if strings.TrimSpace(batch.Title) == "" {
			return nil, fmt.Errorf("batch %d title is empty", idx+1)
		}
		if strings.TrimSpace(batch.Theme) == "" && strings.TrimSpace(batch.Goal) == "" {
			return nil, fmt.Errorf("batch %d theme or goal is required", idx+1)
		}
		if strings.TrimSpace(batch.Summary) == "" {
			return nil, fmt.Errorf("batch %d summary is empty", idx+1)
		}
		count := batch.TargetChapterCount
		if batch.TargetFrom > 0 && batch.TargetTo >= batch.TargetFrom {
			if count <= 0 {
				count = batch.TargetTo - batch.TargetFrom + 1
			}
		}
		if count <= 0 {
			return nil, fmt.Errorf("batch %d chapter_count must be > 0", idx+1)
		}
		batch.TargetChapterCount = count
		batch.TargetFrom = 0
		batch.TargetTo = 0
		out = append(out, batch)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("source-map range %d-%d returned no in-range batches", entry.SourceFrom, entry.SourceTo)
	}
	return out, nil
}

func normalizePlannerSourceMapSkeletonBatchesWithOptions(batches []plannerSkeletonBatch, entry plannerSourceMapEntry, granularity string, allowBudgetDeviation bool, enforceSourceRuneSplitting bool, sourceRunesByChapter map[int]int) ([]plannerSkeletonBatch, error) {
	if len(batches) == 0 {
		return nil, fmt.Errorf("no batches")
	}
	_ = allowBudgetDeviation
	batches = plannerSourceMapCandidatesInRange(batches, entry)
	sort.SliceStable(batches, func(i, j int) bool {
		if batches[i].SourceFrom == batches[j].SourceFrom {
			if batches[i].SourceTo == batches[j].SourceTo {
				return batches[i].Index < batches[j].Index
			}
			return batches[i].SourceTo < batches[j].SourceTo
		}
		return batches[i].SourceFrom < batches[j].SourceFrom
	})
	batches = normalizePlannerStrictSourceRangePartition(batches, entry)
	out := make([]plannerSkeletonBatch, 0, len(batches))
	for idx, batch := range batches {
		batch.BudgetDecision = normalizePlannerBudgetDecision(batch.BudgetDecision)
		batch.BudgetReason = strings.TrimSpace(batch.BudgetReason)
		if batch.SourceFrom <= 0 || batch.SourceTo <= 0 {
			return nil, fmt.Errorf("batch %d must include source_from and source_to", idx+1)
		}
		if batch.SourceTo < entry.SourceFrom || batch.SourceFrom > entry.SourceTo {
			continue
		}
		if strings.TrimSpace(batch.Title) == "" {
			return nil, fmt.Errorf("batch %d title is empty", idx+1)
		}
		if strings.TrimSpace(batch.Theme) == "" && strings.TrimSpace(batch.Goal) == "" {
			return nil, fmt.Errorf("batch %d theme or goal is required", idx+1)
		}
		if strings.TrimSpace(batch.Summary) == "" {
			return nil, fmt.Errorf("batch %d summary is empty", idx+1)
		}
		if batch.SourceFrom < entry.SourceFrom || batch.SourceTo > entry.SourceTo || batch.SourceTo < batch.SourceFrom {
			return nil, fmt.Errorf("batch %d source range %d-%d outside source-map range %d-%d", idx+1, batch.SourceFrom, batch.SourceTo, entry.SourceFrom, entry.SourceTo)
		}
		count := batch.TargetChapterCount
		if batch.TargetFrom > 0 && batch.TargetTo >= batch.TargetFrom {
			if count <= 0 {
				count = batch.TargetTo - batch.TargetFrom + 1
			}
		}
		if count <= 0 {
			return nil, fmt.Errorf("batch %d chapter_count must be > 0", idx+1)
		}
		sourceSpan := batch.SourceTo - batch.SourceFrom + 1
		minCount, maxCount := plannerSourceMapChapterBudgetReviewRange(sourceSpan)
		sourceRunes := plannerSourceMapRunesForRange(entry, sourceRunesByChapter, batch.SourceFrom, batch.SourceTo)
		hardMinCount := plannerSourceMapBudgetMinTargetChaptersForRunes(sourceRunes, granularity)
		if sourceRunes > 0 {
			maxCount = max(maxCount, hardMinCount*adaptationPlannerSourceMapExpansionMax)
		}
		if enforceSourceRuneSplitting {
			minCount = max(minCount, hardMinCount)
		}
		maxCount = max(maxCount, minCount)
		if enforceSourceRuneSplitting && count < hardMinCount {
			if plannerSkeletonBatchAcceptsBudgetDeviation(batch, "low") {
				markPlannerBatchBudgetDeviationAccepted(&batch)
			} else {
				count = hardMinCount
				markPlannerBatchCapacityFloorApplied(&batch, hardMinCount, sourceRunes)
			}
		}
		if enforceSourceRuneSplitting && count < minCount {
			budgetErr := &plannerChapterBudgetQualityError{
				BatchIndex: idx + 1,
				Count:      count,
				MinCount:   minCount,
				MaxCount:   maxCount,
				SourceFrom: batch.SourceFrom,
				SourceTo:   batch.SourceTo,
				Direction:  "low",
			}
			if !plannerSkeletonBatchAcceptsBudgetDeviation(batch, "low") {
				return nil, budgetErr
			}
			markPlannerBatchBudgetDeviationAccepted(&batch)
		}
		if enforceSourceRuneSplitting && count > maxCount {
			budgetErr := &plannerChapterBudgetQualityError{
				BatchIndex: idx + 1,
				Count:      count,
				MinCount:   minCount,
				MaxCount:   maxCount,
				SourceFrom: batch.SourceFrom,
				SourceTo:   batch.SourceTo,
				Direction:  "high",
			}
			if !plannerSkeletonBatchAcceptsBudgetDeviation(batch, "high") {
				return nil, budgetErr
			}
			markPlannerBatchBudgetDeviationAccepted(&batch)
		}
		batch.TargetChapterCount = count
		batch.TargetFrom = 0
		batch.TargetTo = 0
		out = append(out, batch)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("source-map range %d-%d returned no in-range batches", entry.SourceFrom, entry.SourceTo)
	}
	coveredTo := entry.SourceFrom - 1
	for _, batch := range out {
		if batch.SourceFrom <= coveredTo {
			return nil, fmt.Errorf("source-map range %d-%d overlaps at source chapter %d", entry.SourceFrom, entry.SourceTo, batch.SourceFrom)
		}
		if batch.SourceFrom > coveredTo+1 {
			return nil, fmt.Errorf("source-map range %d-%d has gap before source chapter %d", entry.SourceFrom, entry.SourceTo, batch.SourceFrom)
		}
		if batch.SourceTo <= coveredTo {
			return nil, fmt.Errorf("source-map range %d-%d does not advance past source chapter %d", entry.SourceFrom, entry.SourceTo, coveredTo)
		}
		coveredTo = batch.SourceTo
	}
	if coveredTo < entry.SourceTo {
		return nil, fmt.Errorf("source-map range %d-%d ends coverage at %d", entry.SourceFrom, entry.SourceTo, coveredTo)
	}
	targetCount := plannerSkeletonBatchTargetCount(out)
	sourceRunes := plannerSourceMapRunesForRange(entry, sourceRunesByChapter, entry.SourceFrom, entry.SourceTo)
	minTargetCount := plannerSourceMapBudgetMinTargetChaptersForRunes(sourceRunes, granularity)
	if enforceSourceRuneSplitting && minTargetCount > 0 && targetCount < minTargetCount {
		if !markPlannerSkeletonBudgetDeviationAcceptedForDirection(out, "low") {
			last := len(out) - 1
			out[last].TargetChapterCount += minTargetCount - targetCount
			markPlannerBatchCapacityFloorApplied(&out[last], minTargetCount, sourceRunes)
		}
	}
	return out, nil
}

func plannerSourceMapCandidatesInRange(batches []plannerSkeletonBatch, entry plannerSourceMapEntry) []plannerSkeletonBatch {
	out := make([]plannerSkeletonBatch, 0, len(batches))
	for _, batch := range batches {
		if batch.SourceTo < entry.SourceFrom || batch.SourceFrom > entry.SourceTo {
			continue
		}
		out = append(out, batch)
	}
	return out
}

func normalizePlannerStrictSourceRangePartition(batches []plannerSkeletonBatch, entry plannerSourceMapEntry) []plannerSkeletonBatch {
	if len(batches) == 0 || len(batches) > entry.SourceTo-entry.SourceFrom+1 {
		return batches
	}
	coveredTo := entry.SourceFrom - 1
	for index := range batches {
		remaining := len(batches) - index - 1
		from := coveredTo + 1
		maxTo := entry.SourceTo - remaining
		to := batches[index].SourceTo
		if to < from {
			to = from
		}
		if to > maxTo || index == len(batches)-1 {
			to = maxTo
		}
		batches[index].SourceFrom = from
		batches[index].SourceTo = to
		coveredTo = to
	}
	return batches
}

func markPlannerBatchCapacityFloorApplied(batch *plannerSkeletonBatch, minimum, sourceRunes int) {
	if batch == nil {
		return
	}
	batch.BudgetDecision = "expand_or_split"
	reason := fmt.Sprintf("host capacity floor raised this source range to at least %d target chapters", minimum)
	if sourceRunes > 0 {
		reason += fmt.Sprintf(" for %d source_runes", sourceRunes)
	}
	batch.BudgetReason = reason
}

func plannerSourceMapChapterBudgetReviewRange(sourceSpan int) (int, int) {
	if sourceSpan < 1 {
		sourceSpan = 1
	}
	minCount := 1
	if sourceSpan >= adaptationPlannerSourceChunkedMin {
		minCount = max(1, sourceSpan/10)
	}
	maxCount := max(adaptationPlannerRecommendedBatchMax, sourceSpan*adaptationPlannerSourceMapExpansionMax)
	return minCount, maxCount
}

func plannerSkeletonBatchTargetCount(batches []plannerSkeletonBatch) int {
	total := 0
	for _, batch := range batches {
		if batch.TargetChapterCount > 0 {
			total += batch.TargetChapterCount
			continue
		}
		if batch.TargetTo >= batch.TargetFrom {
			total += batch.TargetTo - batch.TargetFrom + 1
		}
	}
	return total
}

func markPlannerBatchBudgetDeviationAccepted(batch *plannerSkeletonBatch) {
	if batch == nil || plannerBudgetDeviationAccepted(*batch) {
		return
	}
	batch.Notes = append(batch.Notes, plannerBudgetDeviationAcceptedNote)
}

func plannerBudgetDeviationAccepted(batch plannerSkeletonBatch) bool {
	return plannerNotesAcceptBudgetDeviation(batch.Notes)
}

func plannerVolumeBudgetDeviationAccepted(volume domain.AdaptationVolumePlan) bool {
	return plannerNotesAcceptBudgetDeviation(volume.Notes)
}

func plannerNotesAcceptBudgetDeviation(notes []string) bool {
	for _, note := range notes {
		note = strings.TrimSpace(note)
		if note == plannerBudgetDeviationAcceptedNote || strings.HasPrefix(note, plannerBudgetDeviationAcceptedNote+":") {
			return true
		}
	}
	return false
}

func normalizePlannerBudgetDecision(decision string) string {
	decision = strings.ToLower(strings.TrimSpace(decision))
	decision = strings.ReplaceAll(decision, "-", "_")
	decision = strings.ReplaceAll(decision, " ", "_")
	switch decision {
	case plannerBudgetDecisionBalanced:
		return plannerBudgetDecisionBalanced
	case plannerBudgetDecisionCompressOrMerge, "compress", "compression", "compressed", "merge", "merged", "delete", "deletion", "skip", "weaken", "restructure":
		return plannerBudgetDecisionCompressOrMerge
	case plannerBudgetDecisionExpandOrSplit, "expand", "expanded", "expansion", "split", "splitting", "increase", "pacing":
		return plannerBudgetDecisionExpandOrSplit
	default:
		return ""
	}
}

func plannerSkeletonBatchAcceptsBudgetDeviation(batch plannerSkeletonBatch, direction string) bool {
	return plannerSkeletonBatchBudgetDeviationRationale(batch, direction) != ""
}

func plannerSkeletonBatchBudgetDeviationRationale(batch plannerSkeletonBatch, direction string) string {
	decision := normalizePlannerBudgetDecision(batch.BudgetDecision)
	reason := strings.TrimSpace(batch.BudgetReason)
	switch direction {
	case "low":
		if decision == plannerBudgetDecisionCompressOrMerge && reason != "" {
			return reason
		}
		return plannerExplicitBudgetRationaleText(batch, plannerLowBudgetRationaleKeywords())
	case "high":
		if decision == plannerBudgetDecisionExpandOrSplit && reason != "" {
			return reason
		}
		return plannerExplicitBudgetRationaleText(batch, plannerHighBudgetRationaleKeywords())
	default:
		return ""
	}
}

func plannerExplicitBudgetRationaleText(batch plannerSkeletonBatch, keywords []string) string {
	text := strings.ToLower(strings.Join([]string{
		batch.Summary,
		batch.ExpansionReason,
		strings.Join(batch.Notes, "\n"),
	}, "\n"))
	for _, keyword := range keywords {
		if strings.Contains(text, keyword) {
			return keyword
		}
	}
	return ""
}

func plannerLowBudgetRationaleKeywords() []string {
	return []string{
		"compress", "compression", "compressed",
		"delete", "deletion", "deleted",
		"merge", "merged", "merging",
		"weaken", "weakened",
		"skip", "skipped",
		"restructure", "restructured", "restructuring",
	}
}

func plannerHighBudgetRationaleKeywords() []string {
	return []string{
		"expand", "expanded", "expansion",
		"split", "splitting",
		"new relationship", "relationship arc", "relationship beat",
		"transition", "transition scene",
		"pacing", "added plot", "add plot", "extra scene",
	}
}

func markPlannerSkeletonBudgetDeviationAcceptedForDirection(batches []plannerSkeletonBatch, direction string) bool {
	accepted := false
	for idx := range batches {
		if !plannerSkeletonBatchAcceptsBudgetDeviation(batches[idx], direction) {
			continue
		}
		markPlannerBatchBudgetDeviationAccepted(&batches[idx])
		accepted = true
	}
	return accepted
}

func plannerBudgetRangeKey(sourceFrom, sourceTo int) string {
	return fmt.Sprintf("%d-%d", sourceFrom, sourceTo)
}

func plannerSkeletonBudgetSplitErrors(batches []plannerSkeletonBatch, opts ProposalOptions, manifest *domain.AdaptationSourceManifest) plannerProposalBudgetSplitErrors {
	policy := plannerChapterBudgetPolicyForGranularity(opts.Granularity)
	sourceRunesByChapter := sourceRunesByChapter(manifest)
	if policy == nil || !plannerEnforcesSourceRuneSplitting(opts.Granularity) || len(sourceRunesByChapter) == 0 {
		return nil
	}
	var splitErrs plannerProposalBudgetSplitErrors
	for _, batch := range batches {
		if plannerBudgetDeviationAccepted(batch) {
			continue
		}
		count := plannerSkeletonBatchChapterCount(batch)
		if count <= 0 || batch.SourceFrom <= 0 || batch.SourceTo < batch.SourceFrom {
			continue
		}
		chapters := make([]domain.AdaptationChapterPlan, count)
		indexes := make([]int, count)
		for idx := 0; idx < count; idx++ {
			chapters[idx].Chapter = batch.TargetFrom + idx
			indexes[idx] = idx
		}
		group := plannerChapterBudgetGroup{
			Indexes:     indexes,
			SourceFrom:  batch.SourceFrom,
			SourceTo:    batch.SourceTo,
			SourceRunes: sourceRunesForRange(sourceRunesByChapter, batch.SourceFrom, batch.SourceTo),
		}
		if err := plannerBudgetGroupSplitError(chapters, group, *policy); err != nil {
			var splitErr *plannerProposalBudgetSplitError
			if errors.As(err, &splitErr) && splitErr != nil {
				splitErrs = appendPlannerBudgetSplitErrorsUnique(splitErrs, *splitErr)
			}
		}
	}
	sortPlannerProposalBudgetSplitErrors(splitErrs)
	return splitErrs
}

func plannerSkeletonBatchMinTargetTo(batch plannerSkeletonBatch, opts ProposalOptions, manifest *domain.AdaptationSourceManifest) int {
	minCount := plannerSkeletonBatchMinTargetCount(batch, opts, manifest)
	currentCount := plannerSkeletonBatchChapterCount(batch)
	if minCount <= currentCount {
		return batch.TargetTo
	}
	return batch.TargetFrom + minCount - 1
}

func plannerSkeletonBatchMinTargetCount(batch plannerSkeletonBatch, opts ProposalOptions, manifest *domain.AdaptationSourceManifest) int {
	policy := plannerChapterBudgetPolicyForGranularity(opts.Granularity)
	if policy == nil || !plannerEnforcesSourceRuneSplitting(opts.Granularity) || manifest == nil {
		return 0
	}
	sourceRunes := sourceRunesForRange(sourceRunesByChapter(manifest), batch.SourceFrom, batch.SourceTo)
	reviewCapacityRunes := plannerBudgetPolicySourceReviewCapacityRunes(*policy)
	if sourceRunes <= reviewCapacityRunes {
		return 0
	}
	return ceilPositiveDiv(sourceRunes, reviewCapacityRunes)
}

func plannerSkeletonBatchChapterCount(batch plannerSkeletonBatch) int {
	if batch.TargetChapterCount > 0 {
		return batch.TargetChapterCount
	}
	if batch.TargetFrom > 0 && batch.TargetTo >= batch.TargetFrom {
		return batch.TargetTo - batch.TargetFrom + 1
	}
	return 0
}

func plannerSourceMapBudgetMinTargetChapters(entry plannerSourceMapEntry, granularity string) int {
	return plannerSourceMapBudgetMinTargetChaptersForRunes(entry.SourceRunes, granularity)
}

func plannerSourceMapBudgetMinTargetChaptersForRunes(sourceRunes int, granularity string) int {
	reviewCapacityRunes := plannerSourceMapSkeletonReviewCapacityRunes(granularity)
	if sourceRunes <= reviewCapacityRunes {
		return 0
	}
	return ceilPositiveDiv(sourceRunes, reviewCapacityRunes)
}

func plannerSourceMapSkeletonReviewCapacityRunes(granularity string) int {
	if domain.NormalizeAdaptationGranularity(granularity) != domain.AdaptationGranularityArc {
		return adaptationPlannerModelChapterMaxRunes
	}
	return int(math.Ceil(float64(adaptationPlannerModelChapterMaxRunes) * (1 + adaptationPlannerModelChapterTolerance)))
}

func plannerSourceMapRunesForRange(entry plannerSourceMapEntry, sourceRunesByChapter map[int]int, from, to int) int {
	if exactRunes, ok := exactSourceRunesForRange(sourceRunesByChapter, from, to); ok {
		return exactRunes
	}
	return plannerSourceMapEstimatedRunesForRange(entry, from, to)
}

func exactSourceRunesForRange(sourceRunesByChapter map[int]int, from, to int) (int, bool) {
	if len(sourceRunesByChapter) == 0 || from <= 0 || to < from {
		return 0, false
	}
	total := 0
	for sourceChapter := from; sourceChapter <= to; sourceChapter++ {
		runes := sourceRunesByChapter[sourceChapter]
		if runes <= 0 {
			return 0, false
		}
		total += runes
	}
	return total, true
}

func plannerSourceMapEstimatedRunesForRange(entry plannerSourceMapEntry, from, to int) int {
	if entry.SourceRunes <= 0 || from <= 0 || to < from {
		return 0
	}
	if from == entry.SourceFrom && to == entry.SourceTo {
		return entry.SourceRunes
	}
	sourceCount := entry.SourceTo - entry.SourceFrom + 1
	if sourceCount <= 0 {
		return 0
	}
	rangeCount := to - from + 1
	return ceilPositiveDiv(entry.SourceRunes*rangeCount, sourceCount)
}

func ceilPositiveDiv(value, divisor int) int {
	if value <= 0 || divisor <= 0 {
		return 0
	}
	return (value + divisor - 1) / divisor
}

func nextPlannerSkeletonTarget(batches []plannerSkeletonBatch) int {
	next := 1
	for _, batch := range batches {
		if batch.TargetTo >= next {
			next = batch.TargetTo + 1
		}
	}
	return next
}

func offsetPlannerSkeletonBatches(batches []plannerSkeletonBatch, targetFrom int, batchIndexFrom int) {
	nextTarget := targetFrom
	for idx := range batches {
		count := batches[idx].TargetChapterCount
		if count <= 0 && batches[idx].TargetTo >= batches[idx].TargetFrom {
			count = batches[idx].TargetTo - batches[idx].TargetFrom + 1
		}
		batches[idx].Index = batchIndexFrom + idx
		batches[idx].TargetFrom = nextTarget
		batches[idx].TargetTo = nextTarget + count - 1
		batches[idx].TargetChapterCount = count
		nextTarget = batches[idx].TargetTo + 1
	}
}

func buildPlanFromPlannerSkeletonDetails(
	ctx context.Context,
	deps Deps,
	opts ProposalOptions,
	reports []domain.AdaptationSourceReport,
	manifest *domain.AdaptationSourceManifest,
	sourceFoundation *domain.AdaptationSourceFoundation,
	skeleton plannerSkeleton,
	runtime *domain.AdaptationProposalRuntime,
) (domain.AdaptationPlan, error) {
	return buildPlanFromPlannerSkeletonDetailsWithFinalRepairs(
		ctx, deps, opts, reports, manifest, sourceFoundation, skeleton, runtime,
		max(1, deps.structureRepairMaxAttempts()),
	)
}

func buildPlanFromPlannerSkeletonDetailsWithFinalRepairs(
	ctx context.Context,
	deps Deps,
	opts ProposalOptions,
	reports []domain.AdaptationSourceReport,
	manifest *domain.AdaptationSourceManifest,
	sourceFoundation *domain.AdaptationSourceFoundation,
	skeleton plannerSkeleton,
	runtime *domain.AdaptationProposalRuntime,
	remainingFinalRepairs int,
) (domain.AdaptationPlan, error) {
	var zero domain.AdaptationPlan
	if runtime == nil {
		runtime = newPlannerProposalRuntime(opts, manifest, skeleton.TargetChapterCount)
		runtime.Skeleton = plannerRuntimeOutlineFromSkeleton(skeleton)
	}
	systemPrompt := adaptationPlannerSystemPrompt(deps)
	if systemPrompt == "" {
		systemPrompt = "# Adaptation Planner\n\nReturn only JSON for the requested adaptation planning step."
	}
	detailBatches := plannerDetailBatches(skeleton.Batches, adaptationPlannerRecommendedBatchMax)
	if parent, removed, migrated := migrateLegacyDetailEventContractForBlockedBatch(runtime, &skeleton); migrated {
		runtime.Skeleton = plannerRuntimeOutlineFromSkeleton(skeleton)
		if err := savePlannerProposalRuntime(deps, runtime); err != nil {
			return zero, fmt.Errorf("migrate legacy detail event contract for chapters %d-%d: %w", parent.TargetFrom, parent.TargetTo, err)
		}
		detailBatches = plannerDetailBatches(skeleton.Batches, adaptationPlannerRecommendedBatchMax)
		emitAdaptProgress(
			opts.EmitProgress,
			StageAudit,
			0,
			len(detailBatches),
			fmt.Sprintf("已升级第 %d-%d 章的旧版事件归属合同，重新生成 %d 个相关详情批次", parent.TargetFrom, parent.TargetTo, removed),
			nil,
		)
	}
	if len(runtime.CompletedBatches) == 0 {
		emitAdaptProgress(opts.EmitProgress, StagePlan, 0, len(detailBatches), fmt.Sprintf("骨架规划完成：%d 章，%d 个模型规划段，拆为 %d 个详情子批次", skeleton.TargetChapterCount, len(skeleton.Batches), len(detailBatches)), nil)
	} else {
		emitAdaptProgress(opts.EmitProgress, StagePlan, 0, len(detailBatches), fmt.Sprintf("已加载骨架规划，正在校验 %d 个已保存详情批次", len(runtime.CompletedBatches)), nil)
	}

	if budgetErrs := plannerRuntimeCompletedBudgetSplitErrors(runtime, opts, manifest); len(budgetErrs) > 0 {
		if err := preparePlannerRuntimeAfterValidationError(deps, runtime, budgetErrs, opts, manifest, opts.EmitProgress); err != nil {
			return zero, fmt.Errorf("preflight proposal runtime budget review: %w", err)
		}
	}

	chapters := make([]domain.AdaptationChapterPlan, 0, skeleton.TargetChapterCount)
	reusedBatchCount := 0
	for batchOrdinal, batch := range detailBatches {
		raw := plannerRuntimeRawBatch(runtime, batch)
		// Existing passed legacy batches remain immutable and keep their
		// original audit signature. Apply the ownership overlay only to a
		// new/pending batch, where it prevents another cross-batch collision
		// without forcing every historical batch through a fresh model audit.
		if raw == nil || raw.Audit == nil || raw.Audit.Status != domain.AdaptationDetailAuditPassed {
			batch = plannerDetailBatchWithPriorEventOwnership(batch, chapters)
		}
		validateBatch := plannerBatchChapterValidator(opts, manifest, batch, chapters)
		batchPrompt, err := buildAdaptationPlannerBatchUserPrompt(opts, manifest, sourceFoundation, skeleton, batch, reportsForPlannerDetailBatch(reports, batch), chapters)
		if err != nil {
			return zero, err
		}
		label := fmt.Sprintf("章节详情第 %d/%d 批", batchOrdinal+1, len(detailBatches))
		batchCtx := withDetailRepairObserver(ctx, persistDetailRepairFailureObserver(deps, runtime, batch))
		if raw != nil && raw.Audit != nil {
			if migrated, ok := resetStaleCrossBatchOwnershipAudit(raw.Audit, batch); ok {
				raw.Audit = migrated
				if err := savePlannerProposalRuntime(deps, runtime); err != nil {
					return zero, fmt.Errorf("migrate stale detail audit scope for %s: %w", label, err)
				}
				emitAdaptProgress(opts.EmitProgress, StageAudit, batchOrdinal+1, len(detailBatches), fmt.Sprintf("%s已迁移旧版跨批次事件归属审核记录，准备重新审核", label), nil)
			}
			if migrated, ok := resetStaleDetailBatchContractAudit(raw.Audit, opts, reports, batch, chapters, raw.Chapters); ok {
				raw.Audit = migrated
				if err := savePlannerProposalRuntime(deps, runtime); err != nil {
					return zero, fmt.Errorf("migrate detail event contract for %s: %w", label, err)
				}
				emitAdaptProgress(opts.EmitProgress, StageAudit, batchOrdinal+1, len(detailBatches), fmt.Sprintf("%s事件归属合同已更新，准备重新审核", label), nil)
			}
			if raw.Audit.Status == domain.AdaptationDetailAuditRepairPending &&
				raw.Audit.RepairAttempts >= deps.adaptationOutlineAuditRetryMaxAttempts() {
				if _, _, usable := plannerRuntimeBatchCandidate(runtime, batch); !usable {
					return zero, fmt.Errorf("%s has exhausted %d persisted repair attempts for %s", label, raw.Audit.RepairAttempts, raw.Audit.LastErrorCategory)
				}
			}
		}
		if batchChapters, existingAudit, ok := plannerRuntimeBatchCandidate(runtime, batch); ok {
			if err := validateBatch(batchChapters); err == nil {
				auditedChapters, _, auditErr := auditAndRepairDetailBatch(
					batchCtx, deps, opts, reports, manifest, skeleton, batch, chapters, batchChapters, existingAudit,
					systemPrompt, batchPrompt, validateBatch, runtime, opts.EmitProgress, batchOrdinal+1, len(detailBatches), label,
				)
				if auditErr != nil {
					return zero, fmt.Errorf("planner batch %d pre-writing audit: %w", batch.Index, auditErr)
				}
				chapters = append(chapters, auditedChapters...)
				if err := runClosedDetailScopeAudits(batchCtx, deps, runtime, opts, reports, skeleton, batch, chapters, opts.EmitProgress, batchOrdinal+1, len(detailBatches)); err != nil {
					return zero, fmt.Errorf("planner batch %d parent/volume audit: %w", batch.Index, err)
				}
				reusedBatchCount++
				continue
			} else {
				if existingAudit != nil && existingAudit.Status == domain.AdaptationDetailAuditRepairPending &&
					existingAudit.RepairAttempts >= deps.adaptationOutlineAuditRetryMaxAttempts() {
					return zero, fmt.Errorf("%s has exhausted %d persisted repair attempts for %s: %w", label, existingAudit.RepairAttempts, existingAudit.LastErrorCategory, err)
				}
				var budgetErr *plannerProposalBudgetSplitError
				if errors.As(err, &budgetErr) {
					if updateErr := preparePlannerRuntimeAfterValidationError(deps, runtime, err, opts, manifest, opts.EmitProgress); updateErr != nil {
						return zero, fmt.Errorf("planner batch %d: %w (also failed to update proposal runtime: %v)", batch.Index, err, updateErr)
					}
					return zero, fmt.Errorf("planner batch %d: %w", batch.Index, err)
				}
				if observeErr := observeDetailRepairFailure(batchCtx, batchChapters, err); observeErr != nil {
					return zero, fmt.Errorf("persist invalid detail batch fingerprint: %w", observeErr)
				}
				emitAdaptProgress(opts.EmitProgress, StagePlan, batchOrdinal+1, len(detailBatches), fmt.Sprintf("已保留无效详情批次 %d/%d 的错误指纹，正在从干净上下文重生成：第 %d-%d 章", batchOrdinal+1, len(detailBatches), batch.TargetFrom, batch.TargetTo), err)
			}
		}
		emitAdaptProgress(opts.EmitProgress, StagePlan, batchOrdinal+1, len(detailBatches), fmt.Sprintf("请求章节详情第 %d/%d 批：第 %d-%d 章", batchOrdinal+1, len(detailBatches), batch.TargetFrom, batch.TargetTo), nil)
		batchText, err := generatePlannerText(
			batchCtx,
			deps.modelForStage("detail_outline"),
			systemPrompt,
			batchPrompt,
			adaptationPlannerMaxTokens,
			opts.EmitProgress,
			batchOrdinal+1,
			len(detailBatches),
			label,
			deps.modelCallMaxAttempts(),
		)
		if err != nil {
			return zero, fmt.Errorf("planner batch %d llm generate: %w", batch.Index, err)
		}
		emitAdaptProgress(opts.EmitProgress, StagePlan, batchOrdinal+1, len(detailBatches), fmt.Sprintf("章节详情第 %d/%d 批已返回，正在解析校验", batchOrdinal+1, len(detailBatches)), nil)
		batchChapters, err := collectPlannerBatchChaptersWithRepair(
			batchCtx,
			deps.modelForStage("detail_outline"),
			systemPrompt,
			batchPrompt,
			batchText,
			batch,
			validateBatch,
			chapters,
			opts.EmitProgress,
			batchOrdinal+1,
			len(detailBatches),
			label,
			detailBatchRepairMaxAttempts(deps),
			deps.modelCallMaxAttempts(),
		)
		if err != nil {
			var budgetErr *plannerProposalBudgetSplitError
			if errors.As(err, &budgetErr) {
				if updateErr := preparePlannerRuntimeAfterValidationError(deps, runtime, err, opts, manifest, opts.EmitProgress); updateErr != nil {
					return zero, fmt.Errorf("planner batch %d: %w (also failed to update proposal runtime: %v)", batch.Index, err, updateErr)
				}
			}
			return zero, fmt.Errorf("planner batch %d: %w", batch.Index, err)
		}
		if len(batchChapters) == 0 {
			if err != nil {
				return zero, fmt.Errorf("planner batch %d: %w", batch.Index, err)
			}
			return zero, fmt.Errorf("planner batch %d: no chapters", batch.Index)
		}
		var candidateAudit *domain.AdaptationDetailBatchAudit
		if raw := plannerRuntimeRawBatch(runtime, batch); raw != nil {
			candidateAudit = raw.Audit
		}
		auditedChapters, _, auditErr := auditAndRepairDetailBatch(
			batchCtx, deps, opts, reports, manifest, skeleton, batch, chapters, batchChapters, candidateAudit,
			systemPrompt, batchPrompt, validateBatch, runtime, opts.EmitProgress, batchOrdinal+1, len(detailBatches), label,
		)
		if auditErr != nil {
			return zero, fmt.Errorf("planner batch %d pre-writing audit: %w", batch.Index, auditErr)
		}
		chapters = append(chapters, auditedChapters...)
		if err := runClosedDetailScopeAudits(batchCtx, deps, runtime, opts, reports, skeleton, batch, chapters, opts.EmitProgress, batchOrdinal+1, len(detailBatches)); err != nil {
			return zero, fmt.Errorf("planner batch %d parent/volume audit: %w", batch.Index, err)
		}
		emitAdaptProgress(opts.EmitProgress, StagePlan, batchOrdinal+1, len(detailBatches), fmt.Sprintf("章节详情第 %d/%d 批完成：第 %d-%d 章", batchOrdinal+1, len(detailBatches), batch.TargetFrom, batch.TargetTo), nil)
	}
	if reusedBatchCount > 0 {
		emitAdaptProgress(opts.EmitProgress, StagePlan, len(detailBatches), len(detailBatches), fmt.Sprintf("已复用并校验 %d/%d 个章节详情批次", reusedBatchCount, len(detailBatches)), nil)
	}

	proposal := domain.AdaptationPlan{
		Granularity:       opts.Granularity,
		Status:            domain.AdaptationPlanStatusProposal,
		RewritePolicy:     opts.RewritePolicy,
		Brief:             opts.Brief,
		Volumes:           adaptationVolumesFromSkeleton(skeleton),
		WordTolerance:     opts.WordTolerance,
		MainlineRules:     append([]string(nil), skeleton.MainlineRules...),
		RelationshipGoals: append([]string(nil), skeleton.RelationshipGoals...),
		Chapters:          chapters,
		Planner:           skeleton.Planner,
	}
	if proposal.Planner == nil {
		proposal.Planner = &domain.AdaptationPlannerMeta{}
	}
	proposal.Planner.Prompt = adaptationPlannerPromptName
	proposal.Planner.PromptVersion = adaptationPlannerPromptVersion + "-chunked"
	proposal.Planner.Notes = append(proposal.Planner.Notes,
		fmt.Sprintf("chunked planner: %d target chapters across %d model-planned batches", skeleton.TargetChapterCount, len(skeleton.Batches)),
	)
	if err := validatePlannerProposal(&proposal, opts, reports, manifest, deps.modelForStage("detail_outline")); err != nil {
		completedBeforeRepair := len(runtime.CompletedBatches)
		if updateErr := preparePlannerRuntimeAfterValidationError(deps, runtime, err, opts, manifest, opts.EmitProgress); updateErr != nil {
			return zero, fmt.Errorf("%w (also failed to update proposal runtime: %v)", err, updateErr)
		}
		if len(runtime.CompletedBatches) < completedBeforeRepair && remainingFinalRepairs > 0 {
			emitAdaptProgress(opts.EmitProgress, StagePlan, 0, len(detailBatches), fmt.Sprintf("Final proposal validation discarded invalid detail batches; regenerating them automatically (%d repair attempt(s) remain)", remainingFinalRepairs), err)
			return buildPlanFromPlannerSkeletonDetailsWithFinalRepairs(
				ctx, deps, opts, reports, manifest, sourceFoundation, skeleton, runtime,
				remainingFinalRepairs-1,
			)
		}
		return zero, err
	}
	if err := ensureGlobalDetailAuditCheckpoint(ctx, deps, runtime, opts, skeleton, proposal.Chapters, opts.EmitProgress); err != nil {
		return zero, fmt.Errorf("global pre-writing outline audit: %w", err)
	}
	digest := layeredDetailAuditDigest(runtime, len(detailBatches))
	if digest == "" {
		return zero, fmt.Errorf("pre-writing outline audit checkpoints are incomplete")
	}
	domain.MarkAdaptationOutlineQualityPassedWithLayers(&proposal, digest)
	return proposal, nil
}

func volumeReviewFromSkeleton(opts ProposalOptions, manifest *domain.AdaptationSourceManifest, skeleton plannerSkeleton) domain.AdaptationVolumeReview {
	review := domain.AdaptationVolumeReview{
		Status:             domain.AdaptationPlanStatusVolumeReview,
		UpdatedAt:          time.Now().UTC().Format(time.RFC3339),
		Brief:              strings.TrimSpace(opts.Brief),
		SourcePath:         plannerProposalRuntimeSourcePath(opts, manifest),
		SourceChapterCount: plannerProposalRuntimeSourceChapterCount(manifest),
		Granularity:        strings.TrimSpace(opts.Granularity),
		RewritePolicy:      strings.TrimSpace(opts.RewritePolicy),
		WordTolerance:      opts.WordTolerance,
		TargetChapterCount: skeleton.TargetChapterCount,
		MainlineRules:      append([]string(nil), skeleton.MainlineRules...),
		RelationshipGoals:  append([]string(nil), skeleton.RelationshipGoals...),
		Volumes:            adaptationVolumesFromSkeleton(skeleton),
		Planner:            clonePlannerRuntimeMeta(skeleton.Planner),
	}
	if review.Planner == nil {
		review.Planner = &domain.AdaptationPlannerMeta{}
	}
	review.Planner.Prompt = adaptationPlannerPromptName
	review.Planner.PromptVersion = adaptationPlannerPromptVersion + "-volume-review"
	if strings.TrimSpace(review.Planner.GeneratedAt) == "" {
		review.Planner.GeneratedAt = review.UpdatedAt
	}
	return review
}

func loadPlannerProposalRuntime(
	deps Deps,
	opts ProposalOptions,
	manifest *domain.AdaptationSourceManifest,
	targetChapterHint int,
	emit ProgressEmitter,
) (*domain.AdaptationProposalRuntime, *plannerSkeleton, error) {
	runtime := newPlannerProposalRuntime(opts, manifest, targetChapterHint)
	existing, err := deps.Store.Adaptation.LoadProposalRuntime()
	if err != nil {
		return nil, nil, fmt.Errorf("load proposal runtime: %w", err)
	}
	if existing == nil {
		return runtime, nil, nil
	}
	if !plannerProposalRuntimeMatches(existing, opts, manifest, targetChapterHint) {
		emitAdaptProgress(emit, StagePlan, 0, 0, "Discarding stale proposal runtime checkpoint", nil)
		if err := deps.Store.Adaptation.ClearProposalRuntime(); err != nil {
			return nil, nil, fmt.Errorf("clear stale proposal runtime: %w", err)
		}
		return runtime, nil, nil
	}
	if existing.Skeleton == nil {
		existing.Version = adaptationProposalRuntimeVersion
		return existing, nil, nil
	}
	skeleton := plannerSkeletonFromRuntime(existing)
	if err := normalizePlannerSkeleton(&skeleton, opts, manifest, targetChapterHint); err != nil {
		emitAdaptProgress(emit, StagePlan, 0, 0, fmt.Sprintf("Discarding invalid proposal runtime skeleton: %v", err), err)
		if clearErr := deps.Store.Adaptation.ClearProposalRuntime(); clearErr != nil {
			return nil, nil, fmt.Errorf("clear invalid proposal runtime: %w", clearErr)
		}
		return runtime, nil, nil
	}
	existing.Skeleton = plannerRuntimeOutlineFromSkeleton(skeleton)
	existing.Version = adaptationProposalRuntimeVersion
	return existing, &skeleton, nil
}

func newPlannerProposalRuntime(opts ProposalOptions, manifest *domain.AdaptationSourceManifest, targetChapterHint int) *domain.AdaptationProposalRuntime {
	sourcePath := plannerProposalRuntimeSourcePath(opts, manifest)
	return &domain.AdaptationProposalRuntime{
		Version:            adaptationProposalRuntimeVersion,
		Brief:              strings.TrimSpace(opts.Brief),
		SourcePath:         sourcePath,
		SourceChapterCount: plannerProposalRuntimeSourceChapterCount(manifest),
		Granularity:        strings.TrimSpace(opts.Granularity),
		RewritePolicy:      strings.TrimSpace(opts.RewritePolicy),
		WordTolerance:      opts.WordTolerance,
		TargetChapterCount: targetChapterHint,
	}
}

func plannerProposalRuntimeMatches(runtime *domain.AdaptationProposalRuntime, opts ProposalOptions, manifest *domain.AdaptationSourceManifest, targetChapterHint int) bool {
	if runtime == nil || (runtime.Version != adaptationProposalRuntimeVersion && runtime.Version != adaptationProposalRuntimeLegacyVersion) {
		return false
	}
	if strings.TrimSpace(runtime.Brief) != strings.TrimSpace(opts.Brief) {
		return false
	}
	if strings.TrimSpace(runtime.Granularity) != strings.TrimSpace(opts.Granularity) {
		return false
	}
	if strings.TrimSpace(runtime.RewritePolicy) != strings.TrimSpace(opts.RewritePolicy) {
		return false
	}
	if math.Abs(runtime.WordTolerance-opts.WordTolerance) > 0.000001 {
		return false
	}
	if !plannerProposalRuntimeTargetMatches(runtime, opts, targetChapterHint) {
		return false
	}
	if runtime.SourceChapterCount != plannerProposalRuntimeSourceChapterCount(manifest) {
		return false
	}
	return sameSourcePath(runtime.SourcePath, plannerProposalRuntimeSourcePath(opts, manifest))
}

func plannerProposalRuntimeTargetMatches(runtime *domain.AdaptationProposalRuntime, opts ProposalOptions, targetChapterHint int) bool {
	if runtime == nil {
		return false
	}
	if explicit := normalizeTargetChapterCount(opts.TargetChapterCount, inferTargetChapterCount(opts.Brief)); explicit > 0 {
		return runtime.TargetChapterCount == explicit
	}
	if runtime.Skeleton != nil || len(runtime.SkeletonBatches) > 0 || len(runtime.CompletedBatches) > 0 {
		return runtime.TargetChapterCount > 0
	}
	return runtime.TargetChapterCount == targetChapterHint
}

func plannerProposalRuntimeSourcePath(opts ProposalOptions, manifest *domain.AdaptationSourceManifest) string {
	if manifest != nil && strings.TrimSpace(manifest.SourcePath) != "" {
		return strings.TrimSpace(manifest.SourcePath)
	}
	return strings.TrimSpace(opts.SourcePath)
}

func plannerProposalRuntimeSourceChapterCount(manifest *domain.AdaptationSourceManifest) int {
	if manifest == nil {
		return 0
	}
	return manifest.ChapterCount
}

func savePlannerProposalRuntime(deps Deps, runtime *domain.AdaptationProposalRuntime) error {
	if deps.Store == nil {
		return fmt.Errorf("store is required")
	}
	if runtime == nil {
		return nil
	}
	if err := bindProposalCoCreateDependency(deps, runtime); err != nil {
		return err
	}
	binding, err := deps.Store.CurrentAdaptationArtifactBinding()
	if err != nil {
		return fmt.Errorf("bind proposal runtime to target foundation: %w", err)
	}
	runtime.FoundationBinding = &binding
	runtime.Version = adaptationProposalRuntimeVersion
	runtime.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	return deps.Store.Adaptation.SaveProposalRuntime(*runtime)
}

func bindAdaptationPlanFoundation(deps Deps, plan *domain.AdaptationPlan) error {
	if plan == nil || deps.Store == nil {
		return fmt.Errorf("adaptation plan and store are required")
	}
	binding, err := deps.Store.CurrentAdaptationArtifactBinding()
	if err != nil {
		return fmt.Errorf("bind adaptation plan to target foundation: %w", err)
	}
	plan.FoundationBinding = &binding
	return nil
}

func bindAdaptationVolumeFoundation(deps Deps, review *domain.AdaptationVolumeReview) error {
	if review == nil || deps.Store == nil {
		return fmt.Errorf("adaptation volume review and store are required")
	}
	binding, err := deps.Store.CurrentAdaptationArtifactBinding()
	if err != nil {
		return fmt.Errorf("bind adaptation volume review to target foundation: %w", err)
	}
	review.FoundationBinding = &binding
	return nil
}

func plannerSkeletonFromRuntime(runtime *domain.AdaptationProposalRuntime) plannerSkeleton {
	if runtime == nil || runtime.Skeleton == nil {
		return plannerSkeleton{}
	}
	outline := runtime.Skeleton
	batches := make([]plannerSkeletonBatch, 0, len(outline.Batches))
	for _, batch := range outline.Batches {
		batches = append(batches, plannerSkeletonBatch{
			Index:                      batch.Index,
			Title:                      batch.Title,
			Theme:                      batch.Theme,
			Goal:                       batch.Goal,
			Summary:                    batch.Summary,
			BudgetDecision:             batch.BudgetDecision,
			BudgetReason:               batch.BudgetReason,
			TargetFrom:                 batch.TargetFrom,
			TargetTo:                   batch.TargetTo,
			TargetChapterCount:         batch.TargetChapterCount,
			SourceFrom:                 batch.SourceFrom,
			SourceTo:                   batch.SourceTo,
			SourceChapters:             append([]int(nil), batch.SourceChapters...),
			MainlineEventIDs:           append([]string(nil), batch.MainlineEventIDs...),
			AllowedEventIDs:            append([]string(nil), batch.AllowedEventIDs...),
			DetailEventContractVersion: batch.DetailEventContractVersion,
			Notes:                      append([]string(nil), batch.Notes...),
		})
	}
	return plannerSkeleton{
		Granularity:        runtime.Granularity,
		Status:             domain.AdaptationPlanStatusProposal,
		RewritePolicy:      runtime.RewritePolicy,
		Brief:              runtime.Brief,
		TargetChapterCount: outline.TargetChapterCount,
		MainlineRules:      append([]string(nil), outline.MainlineRules...),
		RelationshipGoals:  append([]string(nil), outline.RelationshipGoals...),
		Batches:            batches,
		Planner:            clonePlannerRuntimeMeta(outline.Planner),
	}
}

func plannerRuntimeOutlineFromSkeleton(skeleton plannerSkeleton) *domain.AdaptationProposalRuntimeOutline {
	batches := make([]domain.AdaptationProposalRuntimeSkeletonBatch, 0, len(skeleton.Batches))
	for _, batch := range skeleton.Batches {
		batches = append(batches, domain.AdaptationProposalRuntimeSkeletonBatch{
			Index:                      batch.Index,
			Title:                      batch.Title,
			Theme:                      batch.Theme,
			Goal:                       batch.Goal,
			Summary:                    batch.Summary,
			BudgetDecision:             batch.BudgetDecision,
			BudgetReason:               batch.BudgetReason,
			TargetFrom:                 batch.TargetFrom,
			TargetTo:                   batch.TargetTo,
			TargetChapterCount:         batch.TargetChapterCount,
			SourceFrom:                 batch.SourceFrom,
			SourceTo:                   batch.SourceTo,
			SourceChapters:             append([]int(nil), batch.SourceChapters...),
			MainlineEventIDs:           append([]string(nil), batch.MainlineEventIDs...),
			AllowedEventIDs:            append([]string(nil), batch.AllowedEventIDs...),
			DetailEventContractVersion: batch.DetailEventContractVersion,
			Notes:                      append([]string(nil), batch.Notes...),
		})
	}
	return &domain.AdaptationProposalRuntimeOutline{
		TargetChapterCount: skeleton.TargetChapterCount,
		MainlineRules:      append([]string(nil), skeleton.MainlineRules...),
		RelationshipGoals:  append([]string(nil), skeleton.RelationshipGoals...),
		Batches:            batches,
		Planner:            clonePlannerRuntimeMeta(skeleton.Planner),
	}
}

func plannerRuntimeOutlineMatchesSkeleton(outline *domain.AdaptationProposalRuntimeOutline, skeleton plannerSkeleton) bool {
	if outline == nil {
		return false
	}
	expected := plannerRuntimeOutlineFromSkeleton(skeleton)
	if expected == nil || outline.TargetChapterCount != expected.TargetChapterCount {
		return false
	}
	if len(outline.Batches) != len(expected.Batches) {
		return false
	}
	for idx := range outline.Batches {
		if !plannerRuntimeSkeletonBatchMatches(outline.Batches[idx], expected.Batches[idx]) {
			return false
		}
	}
	return true
}

func plannerRuntimeSkeletonBatchMatches(a, b domain.AdaptationProposalRuntimeSkeletonBatch) bool {
	return a.Index == b.Index &&
		a.TargetFrom == b.TargetFrom &&
		a.TargetTo == b.TargetTo &&
		a.TargetChapterCount == b.TargetChapterCount &&
		a.SourceFrom == b.SourceFrom &&
		a.SourceTo == b.SourceTo &&
		a.DetailEventContractVersion == b.DetailEventContractVersion
}

func plannerRuntimeBatchChapters(runtime *domain.AdaptationProposalRuntime, batch plannerSkeletonBatch) ([]domain.AdaptationChapterPlan, bool) {
	chapters, _, ok := plannerRuntimeBatchCandidate(runtime, batch)
	return chapters, ok
}

func plannerRuntimeBatchCandidate(
	runtime *domain.AdaptationProposalRuntime,
	batch plannerSkeletonBatch,
) ([]domain.AdaptationChapterPlan, *domain.AdaptationDetailBatchAudit, bool) {
	if runtime == nil {
		return nil, nil, false
	}
	for _, completed := range runtime.CompletedBatches {
		if !plannerRuntimeBatchMatches(completed, batch) {
			continue
		}
		chapters := make([]domain.AdaptationChapterPlan, 0, len(completed.Chapters))
		for _, chapter := range completed.Chapters {
			chapters = append(chapters, cloneAdaptationChapterPlan(chapter))
		}
		normalized, err := normalizePlannerBatchChapters(chapters, batch)
		if err == nil {
			return normalized, cloneAdaptationDetailBatchAudit(completed.Audit), true
		}
	}
	return nil, nil, false
}

func plannerRuntimeBatchMatches(completed domain.AdaptationProposalRuntimeBatch, batch plannerSkeletonBatch) bool {
	return completed.TargetFrom == batch.TargetFrom &&
		completed.TargetTo == batch.TargetTo &&
		completed.SourceFrom == batch.SourceFrom &&
		completed.SourceTo == batch.SourceTo
}

func removePlannerProposalRuntimeBatch(runtime *domain.AdaptationProposalRuntime, batch plannerSkeletonBatch) {
	if runtime == nil || len(runtime.CompletedBatches) == 0 {
		return
	}
	out := runtime.CompletedBatches[:0]
	for _, completed := range runtime.CompletedBatches {
		if plannerRuntimeBatchMatches(completed, batch) {
			continue
		}
		out = append(out, completed)
	}
	runtime.CompletedBatches = out
}

func preparePlannerRuntimeAfterValidationError(
	deps Deps,
	runtime *domain.AdaptationProposalRuntime,
	validationErr error,
	opts ProposalOptions,
	manifest *domain.AdaptationSourceManifest,
	emit ProgressEmitter,
) error {
	if runtime == nil {
		return nil
	}
	var bindingErr *arcMainlineBindingError
	if errors.As(validationErr, &bindingErr) {
		removed := removePlannerProposalRuntimeBatchesForMainlineBinding(runtime, bindingErr)
		if removed > 0 {
			emitAdaptProgress(emit, StagePlan, 0, 0, fmt.Sprintf("Discarded %d detail batch(es) that duplicated mainline event %s in target chapters %v", removed, bindingErr.EventID, bindingErr.Chapters), validationErr)
			return savePlannerProposalRuntime(deps, runtime)
		}
	}
	budgetErrs := plannerBudgetSplitErrorsFromError(validationErr)
	if len(budgetErrs) > 0 {
		budgetErrs = appendPlannerBudgetSplitErrorsUnique(budgetErrs, plannerRuntimeCompletedBudgetSplitErrors(runtime, opts, manifest)...)
		sortPlannerProposalBudgetSplitErrors(budgetErrs)
		removed := removePlannerProposalRuntimeBatchesForBudgetSplitErrors(runtime, budgetErrs)
		if removed > 0 {
			emitAdaptProgress(emit, StagePlan, 0, 0, fmt.Sprintf("Retained proposal runtime and discarded %d completed detail batch(es) covering %d budget-invalid source range(s): %s", removed, len(budgetErrs), formatPlannerBudgetSplitRanges(budgetErrs)), validationErr)
			return savePlannerProposalRuntime(deps, runtime)
		}
	}
	emitAdaptProgress(emit, StagePlan, 0, 0, "Retained proposal runtime after final validation failure for retry", validationErr)
	return savePlannerProposalRuntime(deps, runtime)
}

func removePlannerProposalRuntimeBatchesForMainlineBinding(runtime *domain.AdaptationProposalRuntime, bindingErr *arcMainlineBindingError) int {
	if runtime == nil || bindingErr == nil || strings.TrimSpace(bindingErr.EventID) == "" {
		return 0
	}
	eventID := strings.TrimSpace(bindingErr.EventID)
	removed := 0
	out := runtime.CompletedBatches[:0]
	for _, completed := range runtime.CompletedBatches {
		containsEvent := false
		for _, chapter := range completed.Chapters {
			for _, candidate := range chapter.EventIDs {
				if strings.TrimSpace(candidate) == eventID {
					containsEvent = true
					break
				}
			}
			if containsEvent {
				break
			}
		}
		if containsEvent {
			removed++
			continue
		}
		out = append(out, completed)
	}
	runtime.CompletedBatches = out
	return removed
}

func plannerBudgetSplitErrorsFromError(err error) plannerProposalBudgetSplitErrors {
	var budgetErrs plannerProposalBudgetSplitErrors
	if errors.As(err, &budgetErrs) && len(budgetErrs) > 0 {
		return appendPlannerBudgetSplitErrorsUnique(nil, budgetErrs...)
	}
	var budgetErr *plannerProposalBudgetSplitError
	if errors.As(err, &budgetErr) && budgetErr != nil {
		return appendPlannerBudgetSplitErrorsUnique(nil, *budgetErr)
	}
	return nil
}

func plannerRuntimeCompletedBudgetSplitErrors(runtime *domain.AdaptationProposalRuntime, opts ProposalOptions, manifest *domain.AdaptationSourceManifest) plannerProposalBudgetSplitErrors {
	policy := plannerChapterBudgetPolicyForGranularity(opts.Granularity)
	if runtime == nil || runtime.Skeleton == nil || policy == nil || !plannerEnforcesSourceRuneSplitting(opts.Granularity) {
		return nil
	}
	sourceRunesByChapter := sourceRunesByChapter(manifest)
	if len(sourceRunesByChapter) == 0 {
		return nil
	}
	skeleton := plannerSkeletonFromRuntime(runtime)
	var splitErrs plannerProposalBudgetSplitErrors
	for _, parent := range skeleton.Batches {
		chapters, complete := plannerRuntimeCompletedParentChapters(runtime, parent)
		if !complete {
			continue
		}
		if plannerUsesSharedBatchSourceRange(opts, parent) {
			if plannerBudgetDeviationAccepted(parent) {
				continue
			}
			group := plannerParentBatchBudgetGroup(chapters, parent, sourceRunesByChapter)
			if err := plannerBudgetGroupSplitError(chapters, group, *policy); err != nil {
				var splitErr *plannerProposalBudgetSplitError
				if errors.As(err, &splitErr) && splitErr != nil {
					splitErrs = appendPlannerBudgetSplitErrorsUnique(splitErrs, *splitErr)
				}
			}
			continue
		}
		groups := plannerChapterBudgetGroups(chapters, sourceRunesByChapter)
		for _, group := range groups {
			if err := plannerBudgetGroupSplitError(chapters, group, *policy); err != nil {
				var splitErr *plannerProposalBudgetSplitError
				if errors.As(err, &splitErr) && splitErr != nil {
					splitErrs = appendPlannerBudgetSplitErrorsUnique(splitErrs, *splitErr)
				}
			}
		}
	}
	sortPlannerProposalBudgetSplitErrors(splitErrs)
	return splitErrs
}

func plannerRuntimeCompletedParentChapters(runtime *domain.AdaptationProposalRuntime, parent plannerSkeletonBatch) ([]domain.AdaptationChapterPlan, bool) {
	if runtime == nil || parent.TargetFrom <= 0 || parent.TargetTo < parent.TargetFrom {
		return nil, false
	}
	chapters := make([]domain.AdaptationChapterPlan, 0, parent.TargetTo-parent.TargetFrom+1)
	covered := make(map[int]bool, parent.TargetTo-parent.TargetFrom+1)
	for _, completed := range runtime.CompletedBatches {
		if completed.TargetFrom < parent.TargetFrom || completed.TargetTo > parent.TargetTo {
			continue
		}
		for _, chapter := range completed.Chapters {
			if chapter.Chapter < parent.TargetFrom || chapter.Chapter > parent.TargetTo {
				continue
			}
			covered[chapter.Chapter] = true
			chapters = append(chapters, cloneAdaptationChapterPlan(chapter))
		}
	}
	for chapter := parent.TargetFrom; chapter <= parent.TargetTo; chapter++ {
		if !covered[chapter] {
			return nil, false
		}
	}
	sort.SliceStable(chapters, func(i, j int) bool {
		return chapters[i].Chapter < chapters[j].Chapter
	})
	return chapters, true
}

func appendPlannerBudgetSplitErrorsUnique(base plannerProposalBudgetSplitErrors, adds ...plannerProposalBudgetSplitError) plannerProposalBudgetSplitErrors {
	out := append(plannerProposalBudgetSplitErrors(nil), base...)
	seen := make(map[string]int, len(out)+len(adds))
	for idx, err := range out {
		seen[plannerBudgetSplitErrorKey(err)] = idx
	}
	for _, err := range adds {
		if err.SourceFrom <= 0 || err.SourceTo < err.SourceFrom {
			continue
		}
		key := plannerBudgetSplitErrorKey(err)
		if existing, ok := seen[key]; ok {
			if out[existing].FirstChapter <= 0 || (err.FirstChapter > 0 && err.FirstChapter < out[existing].FirstChapter) {
				out[existing].FirstChapter = err.FirstChapter
			}
			if err.SourceRunes > out[existing].SourceRunes {
				out[existing].SourceRunes = err.SourceRunes
			}
			if err.MinChapters > out[existing].MinChapters {
				out[existing].MinChapters = err.MinChapters
			}
			continue
		}
		seen[key] = len(out)
		out = append(out, err)
	}
	return out
}

func plannerBudgetSplitErrorKey(err plannerProposalBudgetSplitError) string {
	return fmt.Sprintf("%d:%d", err.SourceFrom, err.SourceTo)
}

func removePlannerProposalRuntimeBatchesForSourceRange(runtime *domain.AdaptationProposalRuntime, sourceFrom, sourceTo int) int {
	if runtime == nil || len(runtime.CompletedBatches) == 0 || sourceFrom <= 0 || sourceTo < sourceFrom {
		return 0
	}
	out := runtime.CompletedBatches[:0]
	removed := 0
	for _, completed := range runtime.CompletedBatches {
		if completed.SourceFrom <= sourceTo && completed.SourceTo >= sourceFrom {
			removed++
			continue
		}
		out = append(out, completed)
	}
	runtime.CompletedBatches = out
	return removed
}

func removePlannerProposalRuntimeBatchesForBudgetSplitErrors(runtime *domain.AdaptationProposalRuntime, budgetErrs plannerProposalBudgetSplitErrors) int {
	removed := 0
	seen := map[string]bool{}
	for _, budgetErr := range budgetErrs {
		key := fmt.Sprintf("%d:%d", budgetErr.SourceFrom, budgetErr.SourceTo)
		if seen[key] {
			continue
		}
		seen[key] = true
		removed += removePlannerProposalRuntimeBatchesForSourceRange(runtime, budgetErr.SourceFrom, budgetErr.SourceTo)
	}
	return removed
}

func plannerRuntimeSkeletonBatchesForSource(runtime *domain.AdaptationProposalRuntime, entry plannerSourceMapEntry, granularity string, sourceRunesByChapter map[int]int) ([]plannerSkeletonBatch, bool, error) {
	if runtime == nil || len(runtime.SkeletonBatches) == 0 {
		return nil, false, nil
	}
	batches := make([]plannerSkeletonBatch, 0)
	for _, completed := range runtime.SkeletonBatches {
		if completed.SourceFrom < entry.SourceFrom || completed.SourceTo > entry.SourceTo {
			continue
		}
		batches = append(batches, plannerSkeletonBatch{
			Index:                      completed.Index,
			Title:                      completed.Title,
			Theme:                      completed.Theme,
			Goal:                       completed.Goal,
			Summary:                    completed.Summary,
			BudgetDecision:             completed.BudgetDecision,
			BudgetReason:               completed.BudgetReason,
			TargetFrom:                 completed.TargetFrom,
			TargetTo:                   completed.TargetTo,
			TargetChapterCount:         completed.TargetChapterCount,
			SourceFrom:                 completed.SourceFrom,
			SourceTo:                   completed.SourceTo,
			SourceChapters:             append([]int(nil), completed.SourceChapters...),
			DetailEventContractVersion: completed.DetailEventContractVersion,
			Notes:                      append([]string(nil), completed.Notes...),
		})
	}
	if len(batches) == 0 {
		return nil, false, nil
	}
	sort.SliceStable(batches, func(i, j int) bool {
		if batches[i].SourceFrom == batches[j].SourceFrom {
			if batches[i].SourceTo == batches[j].SourceTo {
				return batches[i].TargetFrom < batches[j].TargetFrom
			}
			return batches[i].SourceTo < batches[j].SourceTo
		}
		return batches[i].SourceFrom < batches[j].SourceFrom
	})
	normalized, err := normalizePlannerSourceMapSkeletonBatchesForGranularityWithSourceRunes(batches, entry, granularity, sourceRunesByChapter)
	if err != nil {
		return nil, false, err
	}
	return normalized, true, nil
}

func removePlannerProposalRuntimeSkeletonBatchesForSource(runtime *domain.AdaptationProposalRuntime, entry plannerSourceMapEntry) int {
	if runtime == nil || len(runtime.SkeletonBatches) == 0 {
		return 0
	}
	out := runtime.SkeletonBatches[:0]
	removed := 0
	for _, completed := range runtime.SkeletonBatches {
		if completed.SourceFrom >= entry.SourceFrom && completed.SourceTo <= entry.SourceTo {
			removed++
			continue
		}
		out = append(out, completed)
	}
	runtime.SkeletonBatches = out
	runtime.TargetChapterCount = plannerRuntimeSkeletonTargetChapterCount(out)
	return removed
}

func upsertPlannerProposalRuntimeSkeletonBatches(runtime *domain.AdaptationProposalRuntime, entry plannerSourceMapEntry, batches []plannerSkeletonBatch) {
	if runtime == nil {
		return
	}
	out := runtime.SkeletonBatches[:0]
	for _, completed := range runtime.SkeletonBatches {
		if completed.SourceFrom >= entry.SourceFrom && completed.SourceTo <= entry.SourceTo {
			continue
		}
		out = append(out, completed)
	}
	for _, batch := range batches {
		out = append(out, domain.AdaptationProposalRuntimeSkeletonBatch{
			Index:                      batch.Index,
			Title:                      batch.Title,
			Theme:                      batch.Theme,
			Goal:                       batch.Goal,
			Summary:                    batch.Summary,
			BudgetDecision:             batch.BudgetDecision,
			BudgetReason:               batch.BudgetReason,
			TargetFrom:                 batch.TargetFrom,
			TargetTo:                   batch.TargetTo,
			TargetChapterCount:         batch.TargetChapterCount,
			SourceFrom:                 batch.SourceFrom,
			SourceTo:                   batch.SourceTo,
			SourceChapters:             append([]int(nil), batch.SourceChapters...),
			MainlineEventIDs:           append([]string(nil), batch.MainlineEventIDs...),
			AllowedEventIDs:            append([]string(nil), batch.AllowedEventIDs...),
			DetailEventContractVersion: batch.DetailEventContractVersion,
			Notes:                      append([]string(nil), batch.Notes...),
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].TargetFrom == out[j].TargetFrom {
			return out[i].SourceFrom < out[j].SourceFrom
		}
		return out[i].TargetFrom < out[j].TargetFrom
	})
	runtime.SkeletonBatches = out
	runtime.TargetChapterCount = plannerRuntimeSkeletonTargetChapterCount(out)
}

func plannerRuntimeSkeletonTargetChapterCount(batches []domain.AdaptationProposalRuntimeSkeletonBatch) int {
	total := 0
	for _, batch := range batches {
		if batch.TargetTo > total {
			total = batch.TargetTo
		}
	}
	return total
}

func upsertPlannerProposalRuntimeBatch(runtime *domain.AdaptationProposalRuntime, batch plannerSkeletonBatch, chapters []domain.AdaptationChapterPlan) {
	upsertPlannerProposalRuntimeBatchWithAudit(runtime, batch, chapters, nil)
}

func upsertPlannerProposalRuntimeBatchWithAudit(
	runtime *domain.AdaptationProposalRuntime,
	batch plannerSkeletonBatch,
	chapters []domain.AdaptationChapterPlan,
	audit *domain.AdaptationDetailBatchAudit,
) {
	if runtime == nil {
		return
	}
	out := runtime.CompletedBatches[:0]
	for _, completed := range runtime.CompletedBatches {
		if plannerRuntimeBatchMatches(completed, batch) {
			continue
		}
		out = append(out, completed)
	}
	storedChapters := make([]domain.AdaptationChapterPlan, 0, len(chapters))
	for _, chapter := range chapters {
		storedChapters = append(storedChapters, cloneAdaptationChapterPlan(chapter))
	}
	out = append(out, domain.AdaptationProposalRuntimeBatch{
		Index:       batch.Index,
		TargetFrom:  batch.TargetFrom,
		TargetTo:    batch.TargetTo,
		SourceFrom:  batch.SourceFrom,
		SourceTo:    batch.SourceTo,
		CompletedAt: time.Now().UTC().Format(time.RFC3339),
		Chapters:    storedChapters,
		Audit:       cloneAdaptationDetailBatchAudit(audit),
	})
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].TargetFrom == out[j].TargetFrom {
			return out[i].TargetTo < out[j].TargetTo
		}
		return out[i].TargetFrom < out[j].TargetFrom
	})
	runtime.CompletedBatches = out
}

func cloneAdaptationDetailBatchAudit(audit *domain.AdaptationDetailBatchAudit) *domain.AdaptationDetailBatchAudit {
	if audit == nil {
		return nil
	}
	out := *audit
	out.Findings = append([]domain.AdaptationDetailAuditFinding(nil), audit.Findings...)
	for index := range out.Findings {
		out.Findings[index].TargetChapters = append([]int(nil), audit.Findings[index].TargetChapters...)
		out.Findings[index].Evidence = append([]domain.AdaptationDetailAuditEvidence(nil), audit.Findings[index].Evidence...)
	}
	return &out
}

func clonePlannerRuntimeMeta(planner *domain.AdaptationPlannerMeta) *domain.AdaptationPlannerMeta {
	if planner == nil {
		return nil
	}
	out := *planner
	out.Notes = append(domain.TextList(nil), planner.Notes...)
	return &out
}

func plannerSkeletonErrorRepairable(err error) bool {
	if err == nil {
		return false
	}
	message := err.Error()
	return !strings.Contains(message, "ignores long-form scale hint")
}

func emitAdaptProgress(emit ProgressEmitter, stage Stage, current int, total int, msg string, err error) {
	if emit == nil {
		return
	}
	emit(stage, current, total, msg, err)
}

func revisionBatchCount(from, to, batchMax int) int {
	if batchMax <= 0 {
		batchMax = adaptationPlannerRevisionBatchMax
	}
	if from > to {
		from, to = to, from
	}
	count := to - from + 1
	if count <= 0 {
		return 0
	}
	return (count + batchMax - 1) / batchMax
}

func plannerDetailBatches(batches []plannerSkeletonBatch, batchMax int) []plannerSkeletonBatch {
	if batchMax <= 0 {
		batchMax = adaptationPlannerRecommendedBatchMax
	}
	var out []plannerSkeletonBatch
	for _, batch := range batches {
		if batch.TargetFrom <= 0 || batch.TargetTo < batch.TargetFrom {
			continue
		}
		partCount := (batch.TargetTo - batch.TargetFrom + batchMax) / batchMax
		partIndex := 0
		for from := batch.TargetFrom; from <= batch.TargetTo; from += batchMax {
			to := min(batch.TargetTo, from+batchMax-1)
			sub := batch
			sub.Index = len(out) + 1
			sub.DetailParentFrom = batch.TargetFrom
			sub.DetailParentTo = batch.TargetTo
			sub.TargetFrom = from
			sub.TargetTo = to
			sub.TargetChapterCount = to - from + 1
			if batch.DetailEventContractVersion >= plannerDetailEventContractVersionPartitioned {
				sub.MainlineEventIDs, sub.AllowedEventIDs = plannerDetailBatchEventContract(batch, partCount, partIndex)
			} else {
				sub.MainlineEventIDs = splitEventIDsForBatch(batch.MainlineEventIDs, partCount, partIndex)
			}
			out = append(out, sub)
			partIndex++
		}
	}
	return out
}

// migrateLegacyDetailEventContractForBlockedBatch upgrades only the affected
// parent range of a resumable legacy proposal. Legacy detail sub-batches shared
// one broad optional-event whitelist, so an earlier accepted child could claim
// events that a later child still needed. Reopening the small parent range and
// applying the partitioned event contract preserves all work outside that
// range while repairing ownership at the detailed-proposal stage.
func migrateLegacyDetailEventContractForBlockedBatch(
	runtime *domain.AdaptationProposalRuntime,
	skeleton *plannerSkeleton,
) (plannerSkeletonBatch, int, bool) {
	if runtime == nil || skeleton == nil || len(runtime.CompletedBatches) == 0 {
		return plannerSkeletonBatch{}, 0, false
	}
	for _, completed := range runtime.CompletedBatches {
		if !detailBatchAuditHasLegacyDuplicateOwnership(completed.Audit) {
			continue
		}
		for index := range skeleton.Batches {
			parent := &skeleton.Batches[index]
			if parent.DetailEventContractVersion >= plannerDetailEventContractVersionPartitioned ||
				parent.TargetTo-parent.TargetFrom+1 <= adaptationPlannerRecommendedBatchMax ||
				completed.TargetFrom < parent.TargetFrom || completed.TargetTo > parent.TargetTo {
				continue
			}
			parent.DetailEventContractVersion = plannerDetailEventContractVersionPartitioned
			removed := removePlannerProposalRuntimeBatchesForTargetRange(runtime, parent.TargetFrom, parent.TargetTo)
			invalidatePlannerRuntimeAuditCheckpointsForTargetRange(runtime, parent.TargetFrom, parent.TargetTo)
			return *parent, removed, removed > 0
		}
	}
	return plannerSkeletonBatch{}, 0, false
}

func detailBatchAuditHasLegacyDuplicateOwnership(audit *domain.AdaptationDetailBatchAudit) bool {
	if audit == nil || audit.Status != domain.AdaptationDetailAuditRepairPending {
		return false
	}
	for _, finding := range audit.Findings {
		if finding.Code == outlineQualityIssueArcDuplicateEvent {
			return true
		}
	}
	return strings.Contains(strings.ToLower(audit.LastError), outlineQualityIssueArcDuplicateEvent)
}

func removePlannerProposalRuntimeBatchesForTargetRange(runtime *domain.AdaptationProposalRuntime, targetFrom, targetTo int) int {
	if runtime == nil || targetFrom <= 0 || targetTo < targetFrom || len(runtime.CompletedBatches) == 0 {
		return 0
	}
	out := runtime.CompletedBatches[:0]
	removed := 0
	for _, completed := range runtime.CompletedBatches {
		if completed.TargetFrom <= targetTo && completed.TargetTo >= targetFrom {
			removed++
			continue
		}
		out = append(out, completed)
	}
	runtime.CompletedBatches = out
	return removed
}

func invalidatePlannerRuntimeAuditCheckpointsForTargetRange(runtime *domain.AdaptationProposalRuntime, targetFrom, targetTo int) {
	if runtime == nil {
		return
	}
	out := runtime.AuditCheckpoints[:0]
	for _, checkpoint := range runtime.AuditCheckpoints {
		if checkpoint.Kind == "global" ||
			(checkpoint.TargetFrom > 0 && checkpoint.TargetTo >= checkpoint.TargetFrom && checkpoint.TargetFrom <= targetTo && checkpoint.TargetTo >= targetFrom) {
			continue
		}
		out = append(out, checkpoint)
	}
	runtime.AuditCheckpoints = out
}

func generatePlannerText(
	ctx context.Context,
	llm imp.LLMChat,
	systemPrompt string,
	userPrompt string,
	maxTokens int,
	emit ProgressEmitter,
	current int,
	total int,
	label string,
	maxAttemptsOverride ...int,
) (string, error) {
	return generatePlannerTextForStage(ctx, StagePlan, llm, systemPrompt, userPrompt, maxTokens, emit, current, total, label, maxAttemptsOverride...)
}

func plannerInputLimitBytes(stage Stage) int {
	if stage == StagePlan {
		// Planner calls have already passed promptcompile's tokenizer-aware
		// 40k-token hard limit. A second byte ceiling is not equivalent for
		// UTF-8 prompts and can reject valid compiled Chinese context.
		return 0
	}
	if stage == StageAudit {
		// Pre-writing audits own either one bounded detail batch plus its
		// source/event evidence or the bounded full-book outline assembled from
		// those batches. Valid UTF-8 audit packages can cross the shared 60 KiB
		// extraction ceiling while remaining bounded by planning construction.
		return adaptationPlannerAuditInputLimitBytes
	}
	return adaptationPlannerDefaultInputLimitBytes
}

func generatePlannerTextForStage(
	ctx context.Context,
	stage Stage,
	llm imp.LLMChat,
	systemPrompt string,
	userPrompt string,
	maxTokens int,
	emit ProgressEmitter,
	current int,
	total int,
	label string,
	maxAttemptsOverride ...int,
) (string, error) {
	if llm == nil {
		return "", fmt.Errorf("planner llm is nil")
	}
	if stage == "" {
		stage = StagePlan
	}
	label = strings.TrimSpace(label)
	if label == "" {
		label = "改编规划模型调用"
	}
	modelIdentity := plannerPromptModelIdentity(llm)
	grok45Dossier := stage == StageDossier && isGrok45ModelIdentity(modelIdentity)
	if grok45Dossier {
		ctx = globalprompt.WithoutModelPrompt(ctx)
	} else {
		systemPrompt = globalprompt.ApplyForModel(modelIdentity, systemPrompt)
	}
	compiledSystem, compiledUser := systemPrompt, userPrompt
	var diagnostics *promptcompile.Diagnostics
	if stage == StagePlan {
		var err error
		compiledSystem, compiledUser, diagnostics, err = compilePlannerCall(ctx, systemPrompt, userPrompt, promptTokenCounterFromContext(ctx))
		if err != nil {
			return "", err
		}
	}
	if diagnostics != nil {
		slog.Debug("adaptation prompt compiled",
			"agent", diagnostics.Agent,
			"mode", diagnostics.Mode,
			"component_tokens", diagnostics.Components,
			"total_tokens", diagnostics.TotalTokens,
			"target_tokens", diagnostics.TargetTokens,
			"hard_tokens", diagnostics.HardTokens,
			"rules", diagnostics.RuleCount,
			"deduplicated_rules", diagnostics.DeduplicatedRuleCount,
			"strategy", diagnostics.Strategy,
			"static_prefix_hash", diagnostics.StaticPrefixHash,
		)
	}
	messages := []agentcore.Message{
		agentcore.SystemMsg(compiledSystem),
		agentcore.UserMsg(compiledUser),
	}
	callOpts := []agentcore.CallOption{agentcore.WithMaxTokens(maxTokens), agentcore.WithJSONMode()}
	if grok45Dossier {
		callOpts = []agentcore.CallOption{
			agentcore.WithMaxTokens(maxTokens),
			agentcore.WithThinking(agentcore.ThinkingHigh),
			agentcore.WithJSONSchema(
				"adaptation_co_create_dossier_batch",
				"Source-grounded adaptation continuity dossier batch.",
				coCreateDossierBatchJSONSchema(),
				true,
			),
		}
	}
	var lastErr error
	maxAttempts := adaptationPlannerGenerateMaxAttempts
	if len(maxAttemptsOverride) > 0 && maxAttemptsOverride[0] > 0 {
		maxAttempts = maxAttemptsOverride[0]
	}
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		recorder, beginErr := modeldiag.Begin(modeldiag.Request{Store: modeldiag.StoreFromContext(ctx), Task: "adapt_planner_" + string(stage), Batch: attempt, System: compiledSystem, User: []byte(compiledUser), InputLimitBytes: plannerInputLimitBytes(stage), OutputLimitTokens: maxTokens, SelectorCounts: map[string]int{"model_calls": 1}, SplitReason: string(stage)})
		if beginErr != nil {
			return "", beginErr
		}
		resp, err := llm.Generate(ctx, messages, nil, callOpts...)
		if err == nil && resp == nil {
			_ = recorder.Finish(modeldiag.StatusEmptyResponse, "", nil)
			err = fmt.Errorf("planner llm returned nil response")
		}
		if err == nil {
			text := resp.Message.TextContent()
			if strings.TrimSpace(text) != "" {
				if diagnosticErr := recorder.Finish(modeldiag.StatusCompleted, text, resp.Message.Usage); diagnosticErr != nil {
					return "", diagnosticErr
				}
				return text, nil
			}
			_ = recorder.Finish(modeldiag.StatusEmptyResponse, "", resp.Message.Usage)
			err = fmt.Errorf("planner llm returned empty response")
		} else {
			_ = recorder.Finish(modeldiag.StatusProviderError, "", nil)
		}
		lastErr = err
		attemptLimit := plannerGenerateAttemptLimit(err, maxAttempts)
		if !shouldRetryPlannerGenerate(ctx, err, attempt, attemptLimit) {
			return "", err
		}
		nextAttempt := attempt + 1
		displayErr := retrypolicy.SanitizeProviderError(err)
		emitAdaptProgress(
			emit,
			stage,
			current,
			total,
			fmt.Sprintf("%s模型调用失败，准备重试 %d/%d：%s", label, nextAttempt, attemptLimit, displayErr),
			fmt.Errorf("%s", displayErr),
		)
		if err := plannerRetrySleep(ctx, retrypolicy.Delay(attempt)); err != nil {
			return "", err
		}
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("planner llm generate exhausted")
	}
	return "", fmt.Errorf("planner llm generate exhausted %d attempts: %w", maxAttempts, lastErr)
}

func plannerGenerateAttemptLimit(err error, configured int) int {
	if configured <= 0 {
		return configured
	}
	if isTransientPlannerAuthorizationError(err) && configured > adaptationPlannerAuthorizationMaxAttempts {
		return adaptationPlannerAuthorizationMaxAttempts
	}
	return configured
}

func isTransientPlannerAuthorizationError(err error) bool {
	if err == nil {
		return false
	}
	return strings.TrimSpace(strings.ToLower(err.Error())) == "authorization failed"
}

func plannerPromptModelIdentity(llm imp.LLMChat) string {
	if llm == nil {
		return ""
	}
	var parts []string
	if provider, ok := llm.(interface{ ProviderName() string }); ok {
		parts = append(parts, provider.ProviderName())
	}
	if model, ok := llm.(interface{ ModelName() string }); ok {
		parts = append(parts, model.ModelName())
	}
	return strings.Join(parts, "/")
}

func isGrok45ModelIdentity(identity string) bool {
	normalized := strings.ToLower(strings.TrimSpace(identity))
	return strings.Contains(normalized, "grok-4.5")
}

func shouldRetryPlannerGenerate(ctx context.Context, err error, attempt, maxAttempts int) bool {
	if err == nil || ctx.Err() != nil || attempt >= maxAttempts {
		return false
	}
	if isTransientPlannerAuthorizationError(err) {
		return true
	}
	var retryable interface{ Retryable() bool }
	if errors.As(err, &retryable) && !retryable.Retryable() {
		return false
	}
	if isPlannerHardLimitError(err) {
		return false
	}
	if agentcore.IsFailoverEligible(err) {
		return true
	}
	if retrypolicy.IsProviderGatewayError(err) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "nil response") ||
		strings.Contains(msg, "empty response") ||
		strings.Contains(msg, "system is busy") ||
		strings.Contains(msg, "try again later") ||
		strings.Contains(msg, "overloaded") ||
		strings.Contains(msg, "temporarily unavailable") ||
		strings.Contains(msg, "too many requests") ||
		strings.Contains(msg, "rate limit") ||
		strings.Contains(msg, "429") ||
		strings.Contains(msg, "503")
}

func isPlannerHardLimitError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "rate_limit_exceeded") ||
		strings.Contains(msg, "insufficient_quota") ||
		strings.Contains(msg, "quota_exceeded") ||
		strings.Contains(msg, "quota exhausted") ||
		strings.Contains(msg, "usage limit reached") ||
		strings.Contains(msg, "usage limit exceeded") ||
		strings.Contains(msg, "monthly usage limit") ||
		strings.Contains(msg, "balance not enough") ||
		strings.Contains(msg, "insufficient balance")
}

type plannerBatchChapterValidatorFunc func([]domain.AdaptationChapterPlan) error

func plannerBatchChapterValidator(opts ProposalOptions, manifest *domain.AdaptationSourceManifest, batch plannerSkeletonBatch, previousChapters ...[]domain.AdaptationChapterPlan) plannerBatchChapterValidatorFunc {
	opts.WordTolerance = normalizeProposalWordTolerance(opts.Granularity, opts.WordTolerance)
	sourceRunesByChapter := sourceRunesByChapter(manifest)
	priorChapters := plannerPreviousBatchChapters(previousChapters...)
	parentPriorChapters := plannerPreviousChaptersInParentBatch(priorChapters, batch)
	chapterCount := 0
	if manifest != nil {
		chapterCount = manifest.ChapterCount
	}
	return func(chapters []domain.AdaptationChapterPlan) error {
		for idx := range chapters {
			chapter := &chapters[idx]
			if strings.TrimSpace(chapter.Title) == "" {
				return fmt.Errorf("planner chapter %d title is empty", chapter.Chapter)
			}
			if strings.TrimSpace(chapter.CoreEvent) == "" {
				return fmt.Errorf("planner chapter %d core_event is empty", chapter.Chapter)
			}
			if strings.TrimSpace(chapter.Hook) == "" {
				return fmt.Errorf("planner chapter %d hook is empty", chapter.Chapter)
			}
			if len(trimmedNonEmpty(chapter.Scenes)) == 0 {
				return fmt.Errorf("planner chapter %d scenes are empty", chapter.Chapter)
			}
			chapter.Scenes = trimmedNonEmpty(chapter.Scenes)
			if err := normalizePlannerChapterSourceCoverage(chapter, chapterCount, &batch, plannerUsesSharedBatchSourceRange(opts, batch)); err != nil {
				return err
			}
			fillPlannerChapterWordBudgetDefaults(chapter, sourceRunesByChapter, opts.WordTolerance)
			if err := validatePlannerWordBudget(chapter, opts.WordTolerance); err != nil {
				return err
			}
		}
		if duplicate, ok := domain.FindDuplicateAdaptationChapterOutline(chapters, parentPriorChapters); ok {
			return duplicate
		}
		if opts.Granularity == domain.AdaptationGranularityArc {
			if err := validateArcBatchEventCoverage(chapters, batch); err != nil {
				return err
			}
		}
		return validatePlannerBatchChapterBudgetGroups(chapters, priorChapters, opts, sourceRunesByChapter, batch)
	}
}

func plannerPreviousChaptersInParentBatch(chapters []domain.AdaptationChapterPlan, batch plannerSkeletonBatch) []domain.AdaptationChapterPlan {
	if len(chapters) == 0 {
		return nil
	}
	from, to := plannerBatchParentRange(batch)
	if from <= 0 || to < from {
		return nil
	}
	out := make([]domain.AdaptationChapterPlan, 0, len(chapters))
	for _, chapter := range chapters {
		if chapter.Chapter >= from && chapter.Chapter <= to {
			out = append(out, chapter)
		}
	}
	return out
}

func plannerBatchParentRange(batch plannerSkeletonBatch) (int, int) {
	if batch.DetailParentFrom > 0 && batch.DetailParentTo >= batch.DetailParentFrom {
		return batch.DetailParentFrom, batch.DetailParentTo
	}
	return batch.TargetFrom, batch.TargetTo
}

func plannerUsesSharedBatchSourceRange(opts ProposalOptions, batch plannerSkeletonBatch) bool {
	return plannerChapterBudgetPolicyForGranularity(opts.Granularity) != nil &&
		batch.SourceFrom > 0 &&
		batch.SourceTo >= batch.SourceFrom
}

func normalizePlannerChapterSourceCoverage(chapter *domain.AdaptationChapterPlan, chapterCount int, batch *plannerSkeletonBatch, useBatchSourceRange bool) error {
	if chapter == nil {
		return fmt.Errorf("planner chapter is nil")
	}
	if len(chapter.SourceChapters) == 0 {
		if chapter.IsAdded {
			return fmt.Errorf("planner added chapter %d has no source anchors", chapter.Chapter)
		}
		return fmt.Errorf("planner chapter %d has no source coverage", chapter.Chapter)
	}
	minSource, maxSource := 0, 0
	seenInChapter := map[int]bool{}
	for _, sourceChapter := range chapter.SourceChapters {
		if sourceChapter <= 0 || (chapterCount > 0 && sourceChapter > chapterCount) {
			return fmt.Errorf("planner chapter %d references invalid source chapter %d", chapter.Chapter, sourceChapter)
		}
		if batch != nil && batch.SourceFrom > 0 && batch.SourceTo >= batch.SourceFrom &&
			(sourceChapter < batch.SourceFrom || sourceChapter > batch.SourceTo) {
			return fmt.Errorf("planner chapter %d source chapter %d falls outside batch source range %d-%d", chapter.Chapter, sourceChapter, batch.SourceFrom, batch.SourceTo)
		}
		if seenInChapter[sourceChapter] {
			return fmt.Errorf("planner chapter %d repeats source chapter %d", chapter.Chapter, sourceChapter)
		}
		seenInChapter[sourceChapter] = true
		if minSource == 0 || sourceChapter < minSource {
			minSource = sourceChapter
		}
		if sourceChapter > maxSource {
			maxSource = sourceChapter
		}
	}

	sourceRange := domain.SourceRange{}
	if useBatchSourceRange && batch != nil && batch.SourceFrom > 0 && batch.SourceTo >= batch.SourceFrom {
		sourceRange = domain.SourceRange{From: batch.SourceFrom, To: batch.SourceTo}
	} else {
		sourceRange = chapter.SourceRange
		if sourceRange.From == 0 && sourceRange.To == 0 {
			sourceRange = domain.SourceRange{From: minSource, To: maxSource}
		}
	}
	if sourceRange.From <= 0 || sourceRange.To < sourceRange.From || (chapterCount > 0 && sourceRange.To > chapterCount) {
		return fmt.Errorf("planner chapter %d has invalid source_range %d-%d", chapter.Chapter, sourceRange.From, sourceRange.To)
	}
	if batch != nil && batch.SourceFrom > 0 && batch.SourceTo >= batch.SourceFrom &&
		(sourceRange.From < batch.SourceFrom || sourceRange.To > batch.SourceTo) {
		return fmt.Errorf("planner chapter %d source_range %d-%d falls outside batch source range %d-%d", chapter.Chapter, sourceRange.From, sourceRange.To, batch.SourceFrom, batch.SourceTo)
	}
	if minSource < sourceRange.From {
		sourceRange.From = minSource
	}
	if maxSource > sourceRange.To {
		sourceRange.To = maxSource
	}
	if batch != nil && batch.SourceFrom > 0 && batch.SourceTo >= batch.SourceFrom &&
		(sourceRange.From < batch.SourceFrom || sourceRange.To > batch.SourceTo) {
		return fmt.Errorf("planner chapter %d source anchors expand source_range outside batch source range %d-%d", chapter.Chapter, batch.SourceFrom, batch.SourceTo)
	}
	if chapterCount > 0 && sourceRange.To > chapterCount {
		return fmt.Errorf("planner chapter %d source anchors expand source_range outside analyzed source chapter count %d", chapter.Chapter, chapterCount)
	}
	chapter.SourceRange = sourceRange
	return nil
}

func plannerPreviousBatchChapters(groups ...[]domain.AdaptationChapterPlan) []domain.AdaptationChapterPlan {
	for _, chapters := range groups {
		if len(chapters) == 0 {
			continue
		}
		return append([]domain.AdaptationChapterPlan(nil), chapters...)
	}
	return nil
}

func collectPlannerBatchChaptersWithRepair(
	ctx context.Context,
	llm imp.LLMChat,
	systemPrompt string,
	originalPrompt string,
	initialText string,
	batch plannerSkeletonBatch,
	validate plannerBatchChapterValidatorFunc,
	previousChapters []domain.AdaptationChapterPlan,
	emit ProgressEmitter,
	current int,
	total int,
	label string,
	maxRepairAttempts int,
	maxModelCallAttempts int,
) ([]domain.AdaptationChapterPlan, error) {
	if maxRepairAttempts <= 0 {
		maxRepairAttempts = adaptationPlannerRepairMaxAttempts
	}
	text := initialText
	var lastErr error
	for attempt := 0; ; attempt++ {
		chapters, missing, partial, parseErr := parsePlannerBatchPartial(text, batch)
		if partial && len(missing) == 0 {
			if err := validateAndReviewPlannerBatchChapters(ctx, llm, systemPrompt, chapters, validate, previousChapters, emit, current, total, label, maxModelCallAttempts); err == nil {
				return chapters, nil
			} else {
				lastErr = err
			}
		}
		if partial && len(missing) > 0 {
			missingErr := parseErr
			if missingErr == nil {
				missingErr = fmt.Errorf("missing chapters %s for target range %d-%d", formatPlannerChapterList(missing), batch.TargetFrom, batch.TargetTo)
			}
			filled, fillErr := fillMissingPlannerBatchChapters(ctx, llm, systemPrompt, originalPrompt, text, batch, chapters, missing, missingErr, emit, current, total, label, maxRepairAttempts, maxModelCallAttempts)
			if fillErr == nil {
				if err := validateAndReviewPlannerBatchChapters(ctx, llm, systemPrompt, filled, validate, previousChapters, emit, current, total, label, maxModelCallAttempts); err == nil {
					return filled, nil
				} else {
					lastErr = err
				}
			}
			if fillErr != nil {
				lastErr = fillErr
			}
		} else {
			if lastErr == nil {
				lastErr = parseErr
			}
		}
		if lastErr == nil {
			lastErr = fmt.Errorf("planner batch returned no usable chapters")
		}
		if observeErr := observeDetailRepairFailure(ctx, chapters, lastErr); observeErr != nil {
			return nil, fmt.Errorf("persist detail repair failure: %w", observeErr)
		}
		if attempt >= maxRepairAttempts {
			return nil, lastErr
		}
		emitAdaptProgress(emit, StagePlan, current, total, fmt.Sprintf("%s不能直接使用，正在整批修复第 %d/%d 次：%v", label, attempt+1, maxRepairAttempts, lastErr), lastErr)
		regenerate := shouldRegeneratePlannerBatchFromOriginal(attempt, lastErr)
		repairedText, err := repairPlannerBatchText(ctx, llm, systemPrompt, originalPrompt, text, batch, lastErr, regenerate, emit, current, total, label, maxModelCallAttempts)
		if err != nil {
			return nil, err
		}
		text = repairedText
	}
}

func validateAndReviewPlannerBatchChapters(
	ctx context.Context,
	llm imp.LLMChat,
	systemPrompt string,
	chapters []domain.AdaptationChapterPlan,
	validate plannerBatchChapterValidatorFunc,
	previousChapters []domain.AdaptationChapterPlan,
	emit ProgressEmitter,
	current int,
	total int,
	label string,
	maxModelCallAttempts int,
) error {
	if validate != nil {
		if err := validate(chapters); err != nil {
			return err
		}
	}
	return reviewPlannerBatchOutlineSimilarity(ctx, llm, systemPrompt, chapters, emit, current, total, label, maxModelCallAttempts, previousChapters)
}

type plannerOutlineReviewChapter struct {
	Chapter   int      `json:"chapter"`
	Title     string   `json:"title"`
	CoreEvent string   `json:"core_event"`
	Hook      string   `json:"hook"`
	Scenes    []string `json:"scenes"`
}

type plannerOutlineReviewCandidate struct {
	Chapter          int                         `json:"chapter"`
	ExistingChapter  int                         `json:"existing_chapter"`
	Reason           string                      `json:"reason"`
	DetailSimilarity float64                     `json:"detail_similarity"`
	FullSimilarity   float64                     `json:"full_similarity"`
	Existing         plannerOutlineReviewChapter `json:"existing"`
	Current          plannerOutlineReviewChapter `json:"current"`
}

type plannerOutlineReviewResponse struct {
	Pairs      []plannerOutlineReviewVerdict `json:"pairs"`
	Duplicates []plannerOutlineReviewVerdict `json:"duplicates"`
}

type plannerOutlineReviewVerdict struct {
	Chapter         int    `json:"chapter"`
	ExistingChapter int    `json:"existing_chapter"`
	Duplicate       bool   `json:"duplicate"`
	Reason          string `json:"reason"`
}

type plannerOutlineSimilarityDuplicateError struct {
	Chapter          int
	ExistingChapter  int
	Reason           string
	DetailSimilarity float64
	FullSimilarity   float64
}

func (e plannerOutlineSimilarityDuplicateError) Error() string {
	reason := strings.TrimSpace(e.Reason)
	if reason == "" {
		reason = "model judged the detailed outlines as the same story promise"
	}
	return fmt.Sprintf(
		"chapter %d duplicates outline beats from chapter %d after model review: %s (detail_similarity=%.3f full_similarity=%.3f)",
		e.Chapter,
		e.ExistingChapter,
		reason,
		e.DetailSimilarity,
		e.FullSimilarity,
	)
}

func reviewPlannerBatchOutlineSimilarity(
	ctx context.Context,
	llm imp.LLMChat,
	systemPrompt string,
	chapters []domain.AdaptationChapterPlan,
	emit ProgressEmitter,
	current int,
	total int,
	label string,
	maxModelCallAttempts int,
	previousChapters []domain.AdaptationChapterPlan,
) error {
	candidates := domain.FindAdaptationChapterOutlineReviewCandidates(chapters, previousChapters)
	if len(candidates) == 0 {
		return nil
	}
	emitAdaptProgress(emit, StagePlan, current, total, fmt.Sprintf("%s has %d borderline similar outline pair(s); requesting model review", label, len(candidates)), nil)
	reviewPrompt, err := buildPlannerOutlineSimilarityReviewPrompt(chapters, candidates, previousChapters)
	if err != nil {
		return err
	}
	text, err := generatePlannerText(ctx, llm, systemPrompt, reviewPrompt, 2048, emit, current, total, label+" outline duplicate review", maxModelCallAttempts)
	if err != nil {
		return fmt.Errorf("planner outline duplicate review llm generate: %w", err)
	}
	verdicts, err := parsePlannerOutlineSimilarityReview(text)
	if err != nil {
		return fmt.Errorf("planner outline duplicate review parse: %w", err)
	}
	candidateByPair := plannerOutlineCandidateByPair(candidates)
	for _, verdict := range verdicts {
		if !verdict.Duplicate {
			continue
		}
		candidate, ok := candidateByPair[plannerOutlinePairKey(verdict.ExistingChapter, verdict.Chapter)]
		if !ok {
			return fmt.Errorf("planner outline duplicate review returned unknown pair chapter %d vs %d", verdict.Chapter, verdict.ExistingChapter)
		}
		return plannerOutlineSimilarityDuplicateError{
			Chapter:          verdict.Chapter,
			ExistingChapter:  verdict.ExistingChapter,
			Reason:           verdict.Reason,
			DetailSimilarity: candidate.DetailSimilarity,
			FullSimilarity:   candidate.FullSimilarity,
		}
	}
	return nil
}

func buildPlannerOutlineSimilarityReviewPrompt(chapters []domain.AdaptationChapterPlan, candidates []domain.OutlineSimilarityCandidate, previousChapters []domain.AdaptationChapterPlan) (string, error) {
	chapterByNumber := make(map[int]domain.AdaptationChapterPlan, len(chapters)+len(previousChapters))
	for _, chapter := range previousChapters {
		chapterByNumber[chapter.Chapter] = chapter
	}
	for _, chapter := range chapters {
		chapterByNumber[chapter.Chapter] = chapter
	}
	reviewCandidates := make([]plannerOutlineReviewCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		existing, ok := chapterByNumber[candidate.ExistingChapter]
		if !ok {
			return "", fmt.Errorf("outline review candidate references missing existing chapter %d", candidate.ExistingChapter)
		}
		current, ok := chapterByNumber[candidate.Chapter]
		if !ok {
			return "", fmt.Errorf("outline review candidate references missing chapter %d", candidate.Chapter)
		}
		reviewCandidates = append(reviewCandidates, plannerOutlineReviewCandidate{
			Chapter:          candidate.Chapter,
			ExistingChapter:  candidate.ExistingChapter,
			Reason:           candidate.Reason,
			DetailSimilarity: candidate.DetailSimilarity,
			FullSimilarity:   candidate.FullSimilarity,
			Existing:         plannerOutlineReviewChapterFromPlan(existing),
			Current:          plannerOutlineReviewChapterFromPlan(current),
		})
	}
	payload, err := json.MarshalIndent(struct {
		Candidates []plannerOutlineReviewCandidate `json:"candidates"`
	}{Candidates: reviewCandidates}, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal outline review candidates: %w", err)
	}
	return strings.TrimSpace(fmt.Sprintf(`
Review the generated detailed chapter outlines for duplicate story promises.

You are checking only the candidate pairs below. They came from one generated batch and have borderline text similarity: high enough to be suspicious, but not high enough for deterministic rejection.

Judge duplicate=true only when the two chapters are essentially the same chapter outline: same central event, same dramatic hook, and same scene progression, even if wording or names changed.
Judge duplicate=false when they merely share setting, cast, theme, investigation method, or adjacent continuity while advancing distinct events.

Return exactly one JSON object and no prose:
{"pairs":[{"chapter":8,"existing_chapter":3,"duplicate":true,"reason":"short reason"}]}

Candidate pairs:
%s`, string(payload))), nil
}

func plannerOutlineReviewChapterFromPlan(chapter domain.AdaptationChapterPlan) plannerOutlineReviewChapter {
	return plannerOutlineReviewChapter{
		Chapter:   chapter.Chapter,
		Title:     truncatePlannerOutlineReviewText(firstNonEmptyString(chapter.Title, chapter.OutlineEntry.Title)),
		CoreEvent: truncatePlannerOutlineReviewText(firstNonEmptyString(chapter.CoreEvent, chapter.OutlineEntry.CoreEvent)),
		Hook:      truncatePlannerOutlineReviewText(firstNonEmptyString(chapter.Hook, chapter.OutlineEntry.Hook)),
		Scenes:    truncatePlannerOutlineReviewScenes(chapter.Scenes),
	}
}

func truncatePlannerOutlineReviewScenes(scenes []string) []string {
	scenes = trimmedNonEmpty(scenes)
	if len(scenes) > 8 {
		scenes = scenes[:8]
	}
	out := make([]string, 0, len(scenes))
	for _, scene := range scenes {
		out = append(out, truncatePlannerOutlineReviewText(scene))
	}
	return out
}

func truncatePlannerOutlineReviewText(text string) string {
	text = strings.TrimSpace(text)
	runes := []rune(text)
	if len(runes) <= 360 {
		return text
	}
	return string(runes[:360])
}

func parsePlannerOutlineSimilarityReview(text string) ([]plannerOutlineReviewVerdict, error) {
	segments, err := extractPlannerJSONSegments(text)
	if err != nil {
		return nil, err
	}
	var firstErr error
	for _, segment := range segments {
		var response plannerOutlineReviewResponse
		if err := json.Unmarshal([]byte(segment), &response); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		verdicts := append([]plannerOutlineReviewVerdict(nil), response.Pairs...)
		for _, duplicate := range response.Duplicates {
			duplicate.Duplicate = true
			verdicts = append(verdicts, duplicate)
		}
		if len(verdicts) > 0 {
			return verdicts, nil
		}
		if firstErr == nil {
			firstErr = fmt.Errorf("review JSON has no pairs")
		}
	}
	if firstErr != nil {
		return nil, firstErr
	}
	return nil, fmt.Errorf("review JSON has no decodable object")
}

func plannerOutlineCandidateByPair(candidates []domain.OutlineSimilarityCandidate) map[string]domain.OutlineSimilarityCandidate {
	out := make(map[string]domain.OutlineSimilarityCandidate, len(candidates))
	for _, candidate := range candidates {
		out[plannerOutlinePairKey(candidate.ExistingChapter, candidate.Chapter)] = candidate
	}
	return out
}

func plannerOutlinePairKey(existingChapter, chapter int) string {
	return fmt.Sprintf("%d:%d", existingChapter, chapter)
}

func collectProposalVolumeRevisionSkeletonWithRepair(
	ctx context.Context,
	llm imp.LLMChat,
	systemPrompt string,
	originalPrompt string,
	initialText string,
	originalBatch plannerSkeletonBatch,
	expansionMaxTo int,
	allowExpansion bool,
	manifest *domain.AdaptationSourceManifest,
	minTargetTo int,
	requireSameSourceRange bool,
	emit ProgressEmitter,
	maxRepairAttempts int,
	maxModelCallAttempts int,
) (plannerSkeletonBatch, error) {
	if maxRepairAttempts <= 0 {
		maxRepairAttempts = adaptationPlannerRepairMaxAttempts
	}
	text := initialText
	var lastErr error
	for attempt := 0; ; attempt++ {
		batch, err := parseProposalVolumeRevisionSkeleton(text, originalBatch, expansionMaxTo, allowExpansion, manifest, minTargetTo, requireSameSourceRange)
		if err == nil {
			return batch, nil
		}
		lastErr = err
		if attempt >= maxRepairAttempts {
			return plannerSkeletonBatch{}, lastErr
		}
		emitAdaptProgress(emit, StagePlan, attempt+1, maxRepairAttempts, fmt.Sprintf("卷剧情重规划返回不符合结构，正在修复第 %d/%d 次：%v", attempt+1, maxRepairAttempts, lastErr), lastErr)
		repairedText, repairErr := repairProposalVolumeRevisionSkeletonText(ctx, llm, systemPrompt, originalPrompt, text, originalBatch, expansionMaxTo, allowExpansion, minTargetTo, requireSameSourceRange, lastErr, emit, maxModelCallAttempts)
		if repairErr != nil {
			return plannerSkeletonBatch{}, repairErr
		}
		text = repairedText
	}
}

func parseProposalVolumeRevisionSkeleton(text string, originalBatch plannerSkeletonBatch, expansionMaxTo int, allowExpansion bool, manifest *domain.AdaptationSourceManifest, minTargetTo int, requireSameSourceRange bool) (plannerSkeletonBatch, error) {
	skeleton, err := parsePlannerSkeleton(text)
	if err != nil {
		return plannerSkeletonBatch{}, err
	}
	return normalizeProposalVolumeRevisionSkeletonBatch(skeleton, originalBatch, expansionMaxTo, allowExpansion, manifest, minTargetTo, requireSameSourceRange)
}

func normalizeProposalVolumeRevisionSkeletonBatch(skeleton plannerSkeleton, originalBatch plannerSkeletonBatch, expansionMaxTo int, allowExpansion bool, manifest *domain.AdaptationSourceManifest, minTargetTo int, requireSameSourceRange bool) (plannerSkeletonBatch, error) {
	if manifest == nil || manifest.ChapterCount <= 0 {
		return plannerSkeletonBatch{}, fmt.Errorf("source manifest missing")
	}
	if len(skeleton.Batches) != 1 {
		return plannerSkeletonBatch{}, fmt.Errorf("volume revision skeleton must contain exactly one batch, got %d", len(skeleton.Batches))
	}
	if expansionMaxTo < originalBatch.TargetTo {
		expansionMaxTo = originalBatch.TargetTo
	}
	batch := skeleton.Batches[0]
	if batch.Index <= 0 {
		batch.Index = originalBatch.Index
	}
	if batch.TargetFrom <= 0 {
		batch.TargetFrom = originalBatch.TargetFrom
	}
	if batch.TargetTo <= 0 && batch.TargetChapterCount > 0 {
		batch.TargetTo = batch.TargetFrom + batch.TargetChapterCount - 1
	}
	if batch.TargetTo <= 0 {
		batch.TargetTo = originalBatch.TargetTo
	}
	if batch.TargetFrom != originalBatch.TargetFrom {
		return plannerSkeletonBatch{}, fmt.Errorf("volume revision target_from=%d, want %d", batch.TargetFrom, originalBatch.TargetFrom)
	}
	if batch.TargetTo < originalBatch.TargetTo {
		return plannerSkeletonBatch{}, fmt.Errorf("volume revision target_to=%d shrinks original target_to %d", batch.TargetTo, originalBatch.TargetTo)
	}
	if !allowExpansion && batch.TargetTo != originalBatch.TargetTo {
		return plannerSkeletonBatch{}, fmt.Errorf("volume revision changed chapter count without an expansion request")
	}
	if batch.TargetTo > expansionMaxTo {
		return plannerSkeletonBatch{}, fmt.Errorf("volume revision target_to=%d exceeds max %d", batch.TargetTo, expansionMaxTo)
	}
	if allowExpansion {
		decision := normalizeProposalVolumeExpansionDecision(batch.ExpansionDecision)
		if decision == "" {
			return plannerSkeletonBatch{}, fmt.Errorf("volume revision skeleton missing expansion_decision")
		}
		batch.ExpansionDecision = decision
		switch decision {
		case "expand":
			if batch.TargetTo <= originalBatch.TargetTo {
				return plannerSkeletonBatch{}, fmt.Errorf("volume revision model chose expansion but target_to=%d did not exceed original target_to %d", batch.TargetTo, originalBatch.TargetTo)
			}
		case "keep":
			if batch.TargetTo != originalBatch.TargetTo {
				return plannerSkeletonBatch{}, fmt.Errorf("volume revision model chose keep but changed target_to from %d to %d", originalBatch.TargetTo, batch.TargetTo)
			}
		default:
			return plannerSkeletonBatch{}, fmt.Errorf("volume revision expansion_decision=%q, want expand or keep", batch.ExpansionDecision)
		}
	}
	batch.TargetChapterCount = batch.TargetTo - batch.TargetFrom + 1
	if batch.SourceFrom <= 0 || batch.SourceTo <= 0 {
		minSource, maxSource := minMaxPositive(batch.SourceChapters)
		if batch.SourceFrom <= 0 {
			batch.SourceFrom = firstPositiveInt(minSource, originalBatch.SourceFrom)
		}
		if batch.SourceTo <= 0 {
			batch.SourceTo = firstPositiveInt(maxSource, originalBatch.SourceTo)
		}
	}
	if batch.SourceFrom <= 0 {
		batch.SourceFrom = originalBatch.SourceFrom
	}
	if batch.SourceTo <= 0 {
		batch.SourceTo = originalBatch.SourceTo
	}
	if batch.SourceFrom <= 0 || batch.SourceTo < batch.SourceFrom || batch.SourceTo > manifest.ChapterCount {
		return plannerSkeletonBatch{}, fmt.Errorf("volume revision has invalid source range %d-%d", batch.SourceFrom, batch.SourceTo)
	}
	if requireSameSourceRange && (batch.SourceFrom != originalBatch.SourceFrom || batch.SourceTo != originalBatch.SourceTo) {
		return plannerSkeletonBatch{}, fmt.Errorf("volume revision source range must remain %d-%d, got %d-%d", originalBatch.SourceFrom, originalBatch.SourceTo, batch.SourceFrom, batch.SourceTo)
	}
	if strings.TrimSpace(batch.Title) == "" {
		batch.Title = originalBatch.Title
	}
	if strings.TrimSpace(batch.Summary) == "" {
		batch.Summary = originalBatch.Summary
	}
	return batch, nil
}

func repairProposalVolumeRevisionSkeletonText(
	ctx context.Context,
	llm imp.LLMChat,
	systemPrompt string,
	originalPrompt string,
	previousText string,
	originalBatch plannerSkeletonBatch,
	expansionMaxTo int,
	allowExpansion bool,
	minTargetTo int,
	requireSameSourceRange bool,
	previousErr error,
	emit ProgressEmitter,
	maxModelCallAttempts int,
) (string, error) {
	requirements := []string{
		"Return exactly one JSON skeleton object and no prose.",
		"The JSON must have a top-level batches array with exactly one batch.",
		fmt.Sprintf("The batch target_from must be %d.", originalBatch.TargetFrom),
		fmt.Sprintf("The batch target_to must be at least %d.", originalBatch.TargetTo),
		"Do not return chapter details or a chapters array.",
		"Do not return markdown or explanations.",
	}
	if minTargetTo > originalBatch.TargetTo {
		requirements = append(requirements,
			fmt.Sprintf("If preserving or closely rewriting source range %d-%d, prefer increasing target_to to at least %d so target chapters stay within the model chapter budget.", originalBatch.SourceFrom, originalBatch.SourceTo, minTargetTo),
			`If the same lower chapter count is intentional because this rewrite deletes, merges, or compresses source material, keep target_to unchanged and state that rationale in summary or expansion_reason.`,
			`Set expansion_decision to "expand" only when increasing target_to; otherwise use "keep".`,
		)
	}
	if requireSameSourceRange {
		requirements = append(requirements,
			fmt.Sprintf("The batch source_from must remain %d and source_to must remain %d; do not shrink the source range to pass budget validation.", originalBatch.SourceFrom, originalBatch.SourceTo),
		)
	}
	if allowExpansion {
		requirements = append(requirements,
			`The batch must include expansion_decision as either "expand" or "keep".`,
			fmt.Sprintf(`If expansion_decision is "expand", target_to must be greater than %d and must not exceed %d.`, originalBatch.TargetTo, expansionMaxTo),
			fmt.Sprintf(`If expansion_decision is "keep", target_to must remain %d.`, originalBatch.TargetTo),
		)
	} else {
		requirements = append(requirements, fmt.Sprintf("The batch target_to must remain %d.", originalBatch.TargetTo))
	}
	repairPrompt := buildPlannerRepairPrompt(
		fmt.Sprintf("volume revision skeleton %d", originalBatch.Index),
		originalPrompt,
		previousText,
		previousErr,
		requirements,
	)
	text, err := generatePlannerText(ctx, llm, systemPrompt, repairPrompt, adaptationPlannerSkeletonMaxTokens, emit, 0, 0, "卷剧情重规划修复", maxModelCallAttempts)
	if err != nil {
		return "", fmt.Errorf("planner volume revision skeleton repair llm generate: %w", err)
	}
	return text, nil
}

func collectProposalRevisionBatchChaptersWithRepair(
	ctx context.Context,
	llm imp.LLMChat,
	systemPrompt string,
	originalPrompt string,
	initialText string,
	batch plannerSkeletonBatch,
	expansionMaxTo int,
	validate plannerBatchChapterValidatorFunc,
	emit ProgressEmitter,
	current int,
	total int,
	label string,
	maxRepairAttempts int,
	maxModelCallAttempts int,
) ([]domain.AdaptationChapterPlan, error) {
	if maxRepairAttempts <= 0 {
		maxRepairAttempts = adaptationPlannerRepairMaxAttempts
	}
	if expansionMaxTo <= batch.TargetTo {
		expansionMaxTo = batch.TargetTo
	}
	text := initialText
	var lastErr error
	for attempt := 0; ; attempt++ {
		chapters, missing, partial, parseErr := parseProposalRevisionBatchPartial(text, batch, expansionMaxTo)
		if partial && len(missing) == 0 {
			if validate == nil {
				return chapters, nil
			}
			if err := validate(chapters); err == nil {
				return chapters, nil
			} else {
				lastErr = err
			}
		}
		if partial && len(missing) > 0 {
			missingErr := parseErr
			if missingErr == nil {
				missingErr = fmt.Errorf("missing chapters %s for revision range %d-%d", formatPlannerChapterList(missing), batch.TargetFrom, max(batch.TargetTo, maxChapterInPlans(chapters)))
			}
			fillBatch := batch
			fillBatch.TargetTo = max(batch.TargetTo, maxChapterInPlans(chapters))
			filled, fillErr := fillMissingPlannerBatchChapters(ctx, llm, systemPrompt, originalPrompt, text, fillBatch, chapters, missing, missingErr, emit, current, total, label, maxRepairAttempts, maxModelCallAttempts)
			if fillErr == nil {
				if validate == nil {
					return filled, nil
				}
				if err := validate(filled); err == nil {
					return filled, nil
				} else {
					lastErr = err
				}
			}
			if fillErr != nil {
				lastErr = fillErr
			}
		} else {
			if lastErr == nil {
				lastErr = parseErr
			}
		}
		if lastErr == nil {
			lastErr = fmt.Errorf("planner revision returned no usable chapters")
		}
		if attempt >= maxRepairAttempts {
			return nil, lastErr
		}
		emitAdaptProgress(emit, StagePlan, current, total, fmt.Sprintf("%s不能直接使用，正在整批修复第 %d/%d 次：%v", label, attempt+1, maxRepairAttempts, lastErr), lastErr)
		repairedText, err := repairProposalRevisionBatchText(ctx, llm, systemPrompt, originalPrompt, text, batch, expansionMaxTo, lastErr, attempt > 0, emit, current, total, label, maxModelCallAttempts)
		if err != nil {
			return nil, err
		}
		text = repairedText
	}
}

func parsePlannerBatchPartial(text string, batch plannerSkeletonBatch) ([]domain.AdaptationChapterPlan, []int, bool, error) {
	plan, err := parsePlannerProposalStrict(text)
	if err != nil {
		return nil, nil, false, err
	}
	chapters, missing, err := normalizePlannerBatchChapterSubset(plan.Chapters, batch)
	if err == nil {
		return chapters, missing, true, nil
	}
	salvaged, salvageMissing := salvagePlannerBatchChapterSubset(plan.Chapters, batch)
	if len(salvaged) == 0 {
		return nil, nil, false, err
	}
	return salvaged, salvageMissing, true, err
}

func parseProposalRevisionBatchPartial(text string, batch plannerSkeletonBatch, expansionMaxTo int) ([]domain.AdaptationChapterPlan, []int, bool, error) {
	plan, err := parsePlannerProposalStrict(text)
	if err != nil {
		return nil, nil, false, err
	}
	chapters, missing, err := normalizeProposalRevisionBatchChapterSubset(plan.Chapters, batch, expansionMaxTo)
	if err == nil {
		return chapters, missing, true, nil
	}
	return nil, nil, false, err
}

func fillMissingPlannerBatchChapters(
	ctx context.Context,
	llm imp.LLMChat,
	systemPrompt string,
	originalPrompt string,
	previousText string,
	batch plannerSkeletonBatch,
	existing []domain.AdaptationChapterPlan,
	missing []int,
	previousErr error,
	emit ProgressEmitter,
	current int,
	total int,
	label string,
	maxRepairAttempts int,
	maxModelCallAttempts int,
) ([]domain.AdaptationChapterPlan, error) {
	if maxRepairAttempts <= 0 {
		maxRepairAttempts = adaptationPlannerRepairMaxAttempts
	}
	currentChapters := append([]domain.AdaptationChapterPlan(nil), existing...)
	currentMissing := append([]int(nil), missing...)
	feedbackText := previousText
	lastErr := previousErr
	if lastErr == nil {
		lastErr = fmt.Errorf("missing chapters %s", formatPlannerChapterList(currentMissing))
	}
	for attempt := 0; attempt < maxRepairAttempts; attempt++ {
		emitAdaptProgress(emit, StagePlan, current, total, fmt.Sprintf("%s缺少章节 %s，正在补齐第 %d/%d 次", label, formatPlannerChapterList(currentMissing), attempt+1, maxRepairAttempts), lastErr)
		fillText, err := repairPlannerMissingChaptersText(ctx, llm, systemPrompt, originalPrompt, feedbackText, batch, currentChapters, currentMissing, lastErr, attempt > 0, emit, current, total, label, maxModelCallAttempts)
		if err != nil {
			lastErr = err
			feedbackText = ""
			continue
		}
		incoming, stillMissing, parseErr := parsePlannerMissingChapterResponse(fillText, batch, currentMissing)
		if len(incoming) > 0 {
			merged, mergedMissing, mergeErr := mergePlannerBatchChapterSubsets(currentChapters, incoming, batch)
			if mergeErr != nil {
				lastErr = mergeErr
			} else {
				currentChapters = merged
				if len(mergedMissing) < len(stillMissing) || len(stillMissing) == 0 {
					currentMissing = mergedMissing
				} else {
					currentMissing = stillMissing
				}
				if len(currentMissing) == 0 {
					return currentChapters, nil
				}
				lastErr = fmt.Errorf("missing chapters still %s after repair", formatPlannerChapterList(currentMissing))
			}
		} else if parseErr != nil {
			lastErr = parseErr
		} else {
			lastErr = fmt.Errorf("missing repair returned no requested chapters")
		}
		if parseErr != nil {
			lastErr = parseErr
		}
		feedbackText = fillText
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("missing chapters %s were not repaired", formatPlannerChapterList(currentMissing))
	}
	return nil, lastErr
}

func parsePlannerMissingChapterResponse(text string, batch plannerSkeletonBatch, missing []int) ([]domain.AdaptationChapterPlan, []int, error) {
	plan, err := parsePlannerProposalStrict(text)
	if err != nil {
		return nil, append([]int(nil), missing...), err
	}
	normalized := normalizePlannerBatchChapterNumbers(plan.Chapters, batch)
	allowed := make(map[int]struct{}, len(missing))
	for _, chapter := range missing {
		allowed[chapter] = struct{}{}
	}
	found := make(map[int]domain.AdaptationChapterPlan, len(missing))
	var wrong []int
	for _, chapter := range normalized {
		if _, ok := allowed[chapter.Chapter]; !ok {
			wrong = append(wrong, chapter.Chapter)
			continue
		}
		if _, exists := found[chapter.Chapter]; exists {
			return nil, append([]int(nil), missing...), fmt.Errorf("duplicate missing chapter %d in repair response", chapter.Chapter)
		}
		found[chapter.Chapter] = chapter
	}
	accepted := sortedPlannerBatchChapters(found)
	stillMissing := make([]int, 0, len(missing))
	for _, chapter := range missing {
		if _, ok := found[chapter]; !ok {
			stillMissing = append(stillMissing, chapter)
		}
	}
	if len(accepted) == 0 {
		if len(wrong) > 0 {
			return nil, stillMissing, fmt.Errorf("missing repair returned wrong chapters %s, want %s", formatPlannerChapterList(wrong), formatPlannerChapterList(missing))
		}
		return nil, stillMissing, fmt.Errorf("missing repair returned no requested chapters, want %s", formatPlannerChapterList(missing))
	}
	if len(stillMissing) > 0 {
		return accepted, stillMissing, fmt.Errorf("missing repair returned partial chapters; still missing %s", formatPlannerChapterList(stillMissing))
	}
	return accepted, nil, nil
}

func repairPlannerSkeletonText(
	ctx context.Context,
	llm imp.LLMChat,
	systemPrompt string,
	originalPrompt string,
	previousText string,
	previousErr error,
	granularity string,
	regenerateFromOriginal bool,
	emit ProgressEmitter,
	current int,
	total int,
	maxModelCallAttempts int,
) (string, error) {
	ctx = withAdaptationPromptModeIfMissing(ctx, granularity)
	instructions := []string{
		"Return exactly one JSON skeleton object and no prose.",
		"The JSON must have a top-level batches array.",
		"mainline_rules and relationship_goals must each be a JSON array containing strings only; use [] when empty.",
		"Each batch must have chapter_count, source_from, source_to, title, theme or goal, and summary.",
		"Each batch may include budget_decision as balanced, compress_or_merge, or expand_or_split; include budget_reason for intentional low or high chapter budgets.",
		"Do not calculate target_from or target_to in this source-map skeleton step; the host will assign continuous target chapter ranges from chapter_count.",
		"source_from and source_to are model-owned source coverage, not target numbering; the host only assigns target chapter numbers after this step.",
	}
	instructions = append(instructions, plannerSkeletonRepairSourceRangeInstructions(granularity)...)
	instructions = append(instructions,
		"If the previous error says source_runes needs more target chapters or chapter_count is below the review floor, increase chapter_count so each target chapter stays within the model chapter budget.",
		"Do not return only overall_arc, key_turns, pair, notes, markdown, or explanation.",
	)
	repairPrompt := buildPlannerRepairPromptWithOptions(
		"skeleton",
		originalPrompt,
		previousText,
		previousErr,
		instructions,
		plannerRepairPromptOptions{RegenerateFromOriginal: regenerateFromOriginal},
	)
	text, err := generatePlannerText(ctx, llm, systemPrompt, repairPrompt, adaptationPlannerSkeletonMaxTokens, emit, current, total, "骨架规划修复", maxModelCallAttempts)
	if err != nil {
		return "", fmt.Errorf("planner skeleton repair llm generate: %w", err)
	}
	return text, nil
}

func plannerSkeletonRepairSourceRangeInstructions(granularity string) []string {
	if plannerAllowsSharedSourceMapEntryRanges(granularity) {
		return []string{
			"For free/full_rewrite, keep source_from/source_to inside the requested source-map range; sharing or overlapping source ranges across returned batches is valid.",
			"Do not repair free/full_rewrite by forcing the source range into a strict partition; express the rewrite structure with chapter_count, title, theme/goal, and summary.",
		}
	}
	return []string{
		"Suggest sorted source ranges that cover the requested source-map range. The host will normalize minor gaps, overlaps, and duplicated boundary chapters into a strict inclusive partition.",
		"Focus repairs on story structure and chapter_count. Do not spend repeated attempts on exact source-range boundary arithmetic.",
	}
}

func retryPlannerSkeletonChapterBudget(
	ctx context.Context,
	llm imp.LLMChat,
	systemPrompt string,
	originalPrompt string,
	previousText string,
	previousErr error,
	granularity string,
	emit ProgressEmitter,
	current int,
	total int,
	maxModelCallAttempts int,
) (string, error) {
	ctx = withAdaptationPromptModeIfMissing(ctx, granularity)
	instructions := []string{
		"Return exactly one JSON skeleton object and no prose.",
		"The JSON must have a top-level batches array.",
		"Each batch must have chapter_count, source_from, source_to, title, theme or goal, and summary.",
		"Each batch may include budget_decision as balanced, compress_or_merge, or expand_or_split, plus budget_reason when the count intentionally deviates from source_runes capacity.",
		"Do not calculate target_from or target_to; the host will assign continuous target chapter ranges from chapter_count.",
		"Reconsider chapter_count for every batch. A very high count is allowed when added plot, relationship arcs, transition scenes, or long source chapters need splitting. A very low count is allowed when the adaptation intentionally deletes, merges, or compresses source material.",
		"Keep a short reason for any unusually high or low chapter budget in budget_reason, and mirror it in the batch summary when helpful.",
	}
	instructions = append(instructions, plannerSkeletonRepairSourceRangeInstructions(granularity)...)
	instructions = append(instructions, plannerChapterBudgetRepairInstructions(previousErr)...)
	repairPrompt := buildPlannerRepairPrompt("skeleton chapter budget quality review", originalPrompt, previousText, previousErr, instructions)
	text, err := generatePlannerText(ctx, llm, systemPrompt, repairPrompt, adaptationPlannerSkeletonMaxTokens, emit, current, total, "骨架章节预算质量复核", maxModelCallAttempts)
	if err != nil {
		return "", fmt.Errorf("planner skeleton chapter budget review llm generate: %w", err)
	}
	return text, nil
}

func repairPlannerBatchText(
	ctx context.Context,
	llm imp.LLMChat,
	systemPrompt string,
	originalPrompt string,
	previousText string,
	batch plannerSkeletonBatch,
	previousErr error,
	regenerateFromOriginal bool,
	emit ProgressEmitter,
	current int,
	total int,
	label string,
	maxModelCallAttempts int,
) (string, error) {
	requirements := []string{
		"Return exactly one JSON object and no prose.",
		fmt.Sprintf("The top-level object must be shaped exactly like {\"chapters\":[...]} with exactly chapters %d through %d.", batch.TargetFrom, batch.TargetTo),
		"Do not return a single chapter object. Do not return only the missing chapter. Return the full requested batch.",
		"Every chapter must include chapter, title, core_event, hook, scenes, source_chapters, source_range, word_budget, preserve_events, required_changes, and forbidden_moves.",
		"If the previous error says a source_range needs more target chapters, this is a shared source_range budget coverage failure, not a numeric word_budget failure.",
		"Do not fix a source_range budget error by lowering source_runes, lowering word_budget.max_runes, or moving source anchors outside the batch source range.",
		"Repair the offending source_range across the full requested batch: chapters adapting the same coherent source arc may share one broad source_range, but each chapter's word_budget.source_runes must be only that chapter's share and source_chapters must stay inside the shared range.",
		"If the previous error says outline beats are duplicated after model review, regenerate the full requested batch so each repeated chapter has a distinct core_event, hook, and scene progression. Do not only rename the chapter.",
		"Do not return only summaries, key_turns, overall_arc, markdown, or explanation.",
	}
	requirements = append(requirements, plannerBatchEventRepairRequirements(batch, previousErr)...)
	repairPrompt := buildPlannerRepairPromptWithOptions(
		fmt.Sprintf("batch %d", batch.Index),
		originalPrompt,
		previousText,
		previousErr,
		requirements,
		plannerRepairPromptOptions{RegenerateFromOriginal: regenerateFromOriginal},
	)
	text, err := generatePlannerText(ctx, llm, systemPrompt, repairPrompt, adaptationPlannerMaxTokens, emit, current, total, label+"整批修复", maxModelCallAttempts)
	if err != nil {
		return "", fmt.Errorf("planner batch repair llm generate: %w", err)
	}
	return text, nil
}

func shouldRegeneratePlannerBatchFromOriginal(attempt int, previousErr error) bool {
	if attempt > 0 || previousErr == nil {
		return attempt > 0
	}
	message := strings.ToLower(previousErr.Error())
	return strings.Contains(message, "not assigned to detail batch") ||
		strings.Contains(message, "duplicates outline beats")
}

func plannerBatchEventRepairRequirements(batch plannerSkeletonBatch, previousErr error) []string {
	if (len(batch.MainlineEventIDs) == 0 && len(batch.AllowedEventIDs) == 0) || previousErr == nil {
		return nil
	}
	message := strings.ToLower(previousErr.Error())
	if !strings.Contains(message, "event") && !strings.Contains(message, "outline beats") {
		return nil
	}
	mainline, _ := json.Marshal(batch.MainlineEventIDs)
	allowed, _ := json.Marshal(batch.AllowedEventIDs)
	requirements := []string{
		fmt.Sprintf("For this isolated detail batch, the required mainline event_ids are %s. Across all returned chapters, use every required ID exactly once.", mainline),
		fmt.Sprintf("The complete stable source-event whitelist is %s. Every event_ids value must come from this list. Never invent a source ID; put genuinely new target-story events in added_event_ids.", allowed),
		"When preserve_events references a stable source event, use the exact stable ID as the whole array item. Do not append a colon or event description to that ID; put readable descriptions in core_event and scenes.",
		"When the validation error reports an event bound to both an earlier accepted chapter and this batch, retain the prior chapter as context and repair this returned batch's ownership. Move event_ids, preserve_events, core_event, required_changes, and the matching scene beat together; never silence the error by deleting only the ID.",
		"If the failed response pulled an event from another detail batch, rebuild the affected title, core_event, hook, and scenes around this batch's assigned events. Do not merely delete the foreign ID while keeping its future plot beat in prose.",
		"Before returning JSON, verify that every required mainline ID appears exactly once, optional stable IDs appear at most once, and no foreign or invented source ID appears.",
	}
	if len(batch.PriorOwnedEventIDs) > 0 {
		priorOwned, _ := json.Marshal(batch.PriorOwnedEventIDs)
		requirements = append(requirements,
			fmt.Sprintf("These stable source IDs are already owned by earlier accepted detail chapters and are forbidden in this batch: %s. Remove them from event_ids, preserve_events, core_event, required_changes, and scenes together; use only this batch's whitelist for source-event IDs.", priorOwned),
		)
	}
	return requirements
}

func repairProposalRevisionBatchText(
	ctx context.Context,
	llm imp.LLMChat,
	systemPrompt string,
	originalPrompt string,
	previousText string,
	batch plannerSkeletonBatch,
	expansionMaxTo int,
	previousErr error,
	regenerateFromOriginal bool,
	emit ProgressEmitter,
	current int,
	total int,
	label string,
	maxModelCallAttempts int,
) (string, error) {
	if expansionMaxTo < batch.TargetTo {
		expansionMaxTo = batch.TargetTo
	}
	repairPrompt := buildPlannerRepairPromptWithOptions(
		fmt.Sprintf("revision batch %d", batch.Index),
		originalPrompt,
		previousText,
		previousErr,
		[]string{
			"Return exactly one JSON object and no prose.",
			fmt.Sprintf("The top-level object must be shaped exactly like {\"chapters\":[...]} with chapters starting at %d.", batch.TargetFrom),
			fmt.Sprintf("Return the full original revision range %d through %d.", batch.TargetFrom, batch.TargetTo),
			fmt.Sprintf("If ending chapters are appended, they must continue sequentially after %d and must not exceed %d.", batch.TargetTo, expansionMaxTo),
			"Do not return a single chapter object. Return the full revised range plus any appended ending chapters.",
			"Every chapter must include chapter, title, core_event, hook, scenes, source_chapters, source_range, word_budget, preserve_events, required_changes, and forbidden_moves.",
			"Do not return only summaries, key_turns, overall_arc, markdown, or explanation.",
		},
		plannerRepairPromptOptions{RegenerateFromOriginal: regenerateFromOriginal},
	)
	text, err := generatePlannerText(ctx, llm, systemPrompt, repairPrompt, adaptationPlannerMaxTokens, emit, current, total, label+"整批修复", maxModelCallAttempts)
	if err != nil {
		return "", fmt.Errorf("planner revision repair llm generate: %w", err)
	}
	return text, nil
}

func repairPlannerMissingChaptersText(
	ctx context.Context,
	llm imp.LLMChat,
	systemPrompt string,
	originalPrompt string,
	previousText string,
	batch plannerSkeletonBatch,
	existing []domain.AdaptationChapterPlan,
	missing []int,
	previousErr error,
	regenerateFromOriginal bool,
	emit ProgressEmitter,
	current int,
	total int,
	label string,
	maxModelCallAttempts int,
) (string, error) {
	input := struct {
		Step             string                         `json:"step"`
		Error            string                         `json:"error"`
		MissingChapters  []int                          `json:"missing_chapters"`
		Batch            plannerSkeletonBatch           `json:"batch"`
		ExistingChapters []domain.AdaptationChapterPlan `json:"existing_chapters"`
		PreviousOutput   string                         `json:"previous_output,omitempty"`
		Regenerate       bool                           `json:"regenerate_from_original,omitempty"`
		Requirements     []string                       `json:"requirements"`
	}{
		Step:             fmt.Sprintf("batch %d missing chapters", batch.Index),
		Error:            fmt.Sprint(previousErr),
		MissingChapters:  append([]int(nil), missing...),
		Batch:            batch,
		ExistingChapters: append([]domain.AdaptationChapterPlan(nil), existing...),
		PreviousOutput:   truncatePlannerFeedback(previousText),
		Regenerate:       regenerateFromOriginal || errors.Is(previousErr, errPlannerProposalMultipleJSON),
		Requirements: []string{
			"Return exactly one JSON object and no prose.",
			"The top-level object must be shaped exactly like {\"chapters\":[...]}",
			"Return only the chapters listed in missing_chapters; do not repeat existing_chapters.",
			"Keep the missing chapters continuous with existing_chapters and the batch goal.",
			"Every returned chapter must include chapter, title, core_event, hook, scenes, source_chapters, source_range, word_budget, preserve_events, required_changes, and forbidden_moves.",
			"Use integer absolute target chapter numbers from missing_chapters.",
		},
	}
	if input.Regenerate {
		input.PreviousOutput = ""
	}
	raw, err := json.MarshalIndent(input, "", "  ")
	if err != nil {
		raw = []byte(`{"error":"marshal missing chapter repair input failed"}`)
	}
	repairPrompt := "The previous planner response produced a usable partial batch but omitted required chapter plans. Fill only the missing chapters using the original planning request and the already accepted chapter plans below.\n\n" +
		"Original planning request:\n```text\n" + originalPrompt + "\n```\n\n" +
		"Missing chapter repair input:\n```json\n" + string(raw) + "\n```"
	text, err := generatePlannerText(ctx, llm, systemPrompt, repairPrompt, adaptationPlannerMaxTokens, emit, current, total, label+"缺章补齐", maxModelCallAttempts)
	if err != nil {
		return "", fmt.Errorf("planner missing chapter repair llm generate: %w", err)
	}
	return text, nil
}

func buildPlannerRepairPrompt(step string, originalPrompt string, previousText string, previousErr error, requirements []string) string {
	return buildPlannerRepairPromptWithOptions(step, originalPrompt, previousText, previousErr, requirements, plannerRepairPromptOptions{})
}

type plannerRepairPromptOptions struct {
	RegenerateFromOriginal bool
}

func buildPlannerRepairPromptWithOptions(step string, originalPrompt string, previousText string, previousErr error, requirements []string, opts plannerRepairPromptOptions) string {
	input := struct {
		Step                   string   `json:"step"`
		Error                  string   `json:"error"`
		Requirements           []string `json:"requirements"`
		PreviousOutput         string   `json:"previous_output,omitempty"`
		RegenerateFromOriginal bool     `json:"regenerate_from_original,omitempty"`
	}{
		Step:           step,
		Error:          fmt.Sprint(previousErr),
		Requirements:   requirements,
		PreviousOutput: truncatePlannerFeedback(previousText),
		RegenerateFromOriginal: opts.RegenerateFromOriginal ||
			errors.Is(previousErr, errPlannerSourceMapMultipleJSON) ||
			errors.Is(previousErr, errPlannerProposalMultipleJSON),
	}
	if input.RegenerateFromOriginal {
		// Multiple complete objects are not a trustworthy repair source. Feeding
		// them back makes the model repeat both candidates on every retry.
		input.PreviousOutput = ""
	}
	raw, err := json.MarshalIndent(input, "", "  ")
	if err != nil {
		raw = []byte(`{"error":"marshal repair input failed"}`)
	}
	return "The previous planner response could not be used by the application schema. Repair the response using the original planning request and the error feedback below.\n\n" +
		"Original planning request:\n```text\n" + originalPrompt + "\n```\n\n" +
		"Repair feedback:\n```json\n" + string(raw) + "\n```"
}

func truncatePlannerFeedback(text string) string {
	text = strings.TrimSpace(text)
	const maxRunes = 6000
	if len([]rune(text)) <= maxRunes {
		return text
	}
	runes := []rune(text)
	return string(runes[:maxRunes]) + "\n...[truncated]"
}

type plannerSourceManifestSummary struct {
	SourcePath   string                      `json:"source_path,omitempty"`
	ChapterCount int                         `json:"chapter_count"`
	TotalRunes   int                         `json:"total_runes,omitempty"`
	AverageRunes int                         `json:"average_runes,omitempty"`
	FirstChapter plannerSourceChapterSummary `json:"first_chapter,omitempty"`
	LastChapter  plannerSourceChapterSummary `json:"last_chapter,omitempty"`
}

type plannerSourceChapterSummary struct {
	Chapter int    `json:"chapter,omitempty"`
	Title   string `json:"title,omitempty"`
	Runes   int    `json:"runes,omitempty"`
}

type plannerChapterBudgetPolicy struct {
	TargetRunes               int      `json:"target_runes"`
	MaxRunes                  int      `json:"max_runes"`
	SourceReviewCapacityRunes int      `json:"source_review_capacity_runes,omitempty"`
	Tolerance                 float64  `json:"tolerance"`
	Notes                     []string `json:"notes"`
}

func plannerChapterBudgetPolicyForGranularity(granularity string) *plannerChapterBudgetPolicy {
	if domain.AdaptationRewritePolicyForGranularity(granularity) != domain.AdaptationRewriteFullRewrite {
		return nil
	}
	notes := []string{
		"source_range is a coverage envelope for planning, not a strict one-target-chapter-to-one-source-chapter mapping.",
		"Several target chapters in the same batch may share the same broad source_range when they adapt one coherent source arc.",
		"When one long source chapter or source range is covered by multiple target chapters, divide its source runes and story beats across those targets instead of assigning the full source length to every target chapter.",
		"Set each word_budget.target_runes near target_runes when possible, and never set word_budget.max_runes above max_runes.",
	}
	if plannerEnforcesSourceRuneSplitting(granularity) {
		notes = append([]string{
			"For arc full-rewrite plans, source_runes is a source-map skeleton capacity review signal; choose enough target chapters for coverage using source_review_capacity_runes, while final word_budget.max_runes remains capped at max_runes.",
		}, notes...)
	} else {
		notes = append([]string{
			"For free full-rewrite plans, this policy limits each generated target chapter; source_runes is not a minimum target chapter count.",
		}, notes...)
	}
	return &plannerChapterBudgetPolicy{
		TargetRunes:               adaptationPlannerModelChapterTargetRunes,
		MaxRunes:                  adaptationPlannerModelChapterMaxRunes,
		SourceReviewCapacityRunes: plannerSourceMapSkeletonReviewCapacityRunes(granularity),
		Tolerance:                 adaptationPlannerModelChapterTolerance,
		Notes:                     notes,
	}
}

func plannerBudgetPolicySourceReviewCapacityRunes(policy plannerChapterBudgetPolicy) int {
	if policy.SourceReviewCapacityRunes > 0 {
		return policy.SourceReviewCapacityRunes
	}
	return policy.MaxRunes
}

func plannerEnforcesSourceRuneSplitting(granularity string) bool {
	return domain.NormalizeAdaptationGranularity(granularity) == domain.AdaptationGranularityArc
}

func plannerAllowsSharedSourceMapEntryRanges(granularity string) bool {
	return domain.NormalizeAdaptationGranularity(granularity) == domain.AdaptationGranularityFree
}

type plannerSourceMapEntry struct {
	Index               int                      `json:"index"`
	SourceFrom          int                      `json:"source_from"`
	SourceTo            int                      `json:"source_to"`
	SourceRunes         int                      `json:"source_runes,omitempty"`
	PlotPhase           string                   `json:"plot_phase,omitempty"`
	KeyCausality        []string                 `json:"key_causality,omitempty"`
	PlotThreads         []string                 `json:"plot_threads,omitempty"`
	CharacterArcs       []string                 `json:"character_arcs,omitempty"`
	WorldConstraints    []string                 `json:"world_constraints,omitempty"`
	MajorCharacters     []string                 `json:"major_characters,omitempty"`
	RelationshipSignals []plannerSourceSignal    `json:"relationship_signals,omitempty"`
	HeroineSignals      []plannerSourceSignal    `json:"heroine_signals,omitempty"`
	AmbiguityRisks      []plannerSourceRisk      `json:"ambiguity_risks,omitempty"`
	CoupleMilestones    []plannerSourceSignal    `json:"couple_milestones,omitempty"`
	MainlineEvents      []domain.AdaptationEvent `json:"mainline_events,omitempty"`
}

type plannerSourceSignal struct {
	Chapters   []int    `json:"chapters,omitempty"`
	Characters []string `json:"characters,omitempty"`
	Type       string   `json:"type,omitempty"`
	Summary    string   `json:"summary"`
	Evidence   string   `json:"evidence,omitempty"`
}

type plannerSourceRisk struct {
	Chapters   []int    `json:"chapters,omitempty"`
	Characters []string `json:"characters,omitempty"`
	Severity   string   `json:"severity,omitempty"`
	Risk       string   `json:"risk"`
	Evidence   string   `json:"evidence,omitempty"`
	Suggestion string   `json:"suggestion,omitempty"`
}

type plannerSourceFoundationSummary struct {
	Premise    string                           `json:"premise,omitempty"`
	Characters []domain.Character               `json:"characters,omitempty"`
	WorldRules []domain.WorldRule               `json:"world_rules,omitempty"`
	Volumes    []plannerFoundationVolumeSummary `json:"volumes,omitempty"`
	Compass    *domain.StoryCompass             `json:"compass,omitempty"`
}

type plannerFoundationVolumeSummary struct {
	Index int                           `json:"index"`
	Title string                        `json:"title,omitempty"`
	Theme string                        `json:"theme,omitempty"`
	Arcs  []plannerFoundationArcSummary `json:"arcs,omitempty"`
}

type plannerFoundationArcSummary struct {
	Index             int    `json:"index"`
	Title             string `json:"title,omitempty"`
	Goal              string `json:"goal,omitempty"`
	EstimatedChapters int    `json:"estimated_chapters,omitempty"`
}

type plannerSourceReportExcerpt struct {
	Chapter        int                      `json:"chapter"`
	Title          string                   `json:"title,omitempty"`
	Summary        string                   `json:"summary,omitempty"`
	Characters     []string                 `json:"characters,omitempty"`
	CharacterFacts []string                 `json:"character_facts,omitempty"`
	KeyEvents      []string                 `json:"key_events,omitempty"`
	SourceEvents   []domain.AdaptationEvent `json:"source_events,omitempty"`
	WorldRules     []string                 `json:"world_rules,omitempty"`
	HookType       string                   `json:"hook_type,omitempty"`
	DominantStrand string                   `json:"dominant_strand,omitempty"`
	Relationships  []plannerRelationNote    `json:"relationships,omitempty"`
	StateChanges   []plannerStateNote       `json:"state_changes,omitempty"`
}

type plannerRelationNote struct {
	CharacterA string `json:"character_a,omitempty"`
	CharacterB string `json:"character_b,omitempty"`
	Relation   string `json:"relation,omitempty"`
}

type plannerStateNote struct {
	Entity   string `json:"entity,omitempty"`
	Field    string `json:"field,omitempty"`
	OldValue string `json:"old_value,omitempty"`
	NewValue string `json:"new_value,omitempty"`
	Reason   string `json:"reason,omitempty"`
}

func plannerManifestSummary(manifest *domain.AdaptationSourceManifest) plannerSourceManifestSummary {
	if manifest == nil {
		return plannerSourceManifestSummary{}
	}
	summary := plannerSourceManifestSummary{
		SourcePath:   strings.TrimSpace(manifest.SourcePath),
		ChapterCount: manifest.ChapterCount,
	}
	for _, chapter := range manifest.Chapters {
		summary.TotalRunes += chapter.Runes
		if summary.FirstChapter.Chapter == 0 {
			summary.FirstChapter = plannerSourceChapterSummary{Chapter: chapter.Chapter, Title: clipText(chapter.Title, 80), Runes: chapter.Runes}
		}
		summary.LastChapter = plannerSourceChapterSummary{Chapter: chapter.Chapter, Title: clipText(chapter.Title, 80), Runes: chapter.Runes}
	}
	if summary.ChapterCount <= 0 {
		summary.ChapterCount = len(manifest.Chapters)
	}
	if summary.ChapterCount > 0 {
		summary.AverageRunes = summary.TotalRunes / summary.ChapterCount
	}
	return summary
}

func plannerSourceMapFromDossier(dossier *domain.AdaptationCoCreateDossier, manifest *domain.AdaptationSourceManifest, reports []domain.AdaptationSourceReport) []plannerSourceMapEntry {
	if dossier == nil {
		return nil
	}
	sourceRunesByChapter := sourceRunesByChapter(manifest)
	entries := make([]plannerSourceMapEntry, 0, len(dossier.Batches))
	for _, batch := range dossier.Batches {
		entry := plannerSourceMapEntry{
			Index:               batch.Index,
			SourceFrom:          batch.SourceFrom,
			SourceTo:            batch.SourceTo,
			SourceRunes:         sourceRunesForRange(sourceRunesByChapter, batch.SourceFrom, batch.SourceTo),
			PlotPhase:           clipText(batch.PlotPhase, 220),
			KeyCausality:        clippedStringList(batch.KeyCausality, 6, 160),
			PlotThreads:         clippedStringList(batch.PlotThreads, 6, 150),
			CharacterArcs:       clippedStringList(batch.CharacterArcs, 6, 150),
			WorldConstraints:    clippedStringList(batch.WorldConstraints, 5, 150),
			MajorCharacters:     clippedStringList(batch.MajorCharacters, 16, 60),
			RelationshipSignals: plannerSignals(batch.RelationshipSignals, 5),
			HeroineSignals:      plannerSignals(batch.HeroineSignals, 5),
			AmbiguityRisks:      plannerRisks(batch.AmbiguityRisks, 4),
			CoupleMilestones:    plannerSignals(batch.CoupleMilestones, 5),
			MainlineEvents:      mainlineSourceEventsInRange(reports, batch.SourceFrom, batch.SourceTo),
		}
		entries = append(entries, entry)
	}
	return entries
}

func plannerSourceReportExcerpts(reports []domain.AdaptationSourceReport) []plannerSourceReportExcerpt {
	excerpts := make([]plannerSourceReportExcerpt, 0, len(reports))
	for index := range reports {
		report := reports[index]
		events := domain.EnsureAdaptationSourceEvents(&report)
		excerpts = append(excerpts, plannerSourceReportExcerpt{
			Chapter:        report.Chapter,
			Title:          clipText(report.Title, 80),
			Summary:        clipText(report.Summary, 220),
			Characters:     clippedStringList(report.Characters, 12, 60),
			CharacterFacts: clippedStringList(report.CharacterFacts, 4, 120),
			KeyEvents:      clippedStringList(report.KeyEvents, 5, 120),
			SourceEvents:   compactPlannerSourceEvents(events, 8),
			WorldRules:     clippedStringList(report.WorldRules, 4, 120),
			HookType:       clipText(report.HookType, 60),
			DominantStrand: clipText(report.DominantStrand, 80),
			Relationships:  plannerRelationNotes(report.Relationships, 6),
			StateChanges:   plannerStateNotes(report.StateChanges, 6),
		})
	}
	return excerpts
}

func plannerSourceReportExcerptsForDetail(reports []domain.AdaptationSourceReport, granularity string, batch plannerSkeletonBatch) []plannerSourceReportExcerpt {
	excerpts := plannerSourceReportExcerpts(reports)
	if domain.NormalizeAdaptationGranularity(granularity) != domain.AdaptationGranularityArc {
		return excerpts
	}
	allowedEventIDs := make(map[string]struct{}, len(batch.MainlineEventIDs)+len(batch.AllowedEventIDs))
	for _, eventID := range batch.MainlineEventIDs {
		eventID = strings.TrimSpace(eventID)
		if eventID != "" {
			allowedEventIDs[eventID] = struct{}{}
		}
	}
	for _, eventID := range batch.AllowedEventIDs {
		eventID = strings.TrimSpace(eventID)
		if eventID != "" {
			allowedEventIDs[eventID] = struct{}{}
		}
	}
	for index := range excerpts {
		events := excerpts[index].SourceEvents[:0]
		for _, event := range excerpts[index].SourceEvents {
			if _, ok := allowedEventIDs[strings.TrimSpace(event.ID)]; !ok {
				continue
			}
			events = append(events, event)
		}
		excerpts[index].SourceEvents = events
	}
	return excerpts
}

func plannerSourceChapterSummariesInRange(manifest *domain.AdaptationSourceManifest, from, to, maxItems int) ([]plannerSourceChapterSummary, int) {
	if manifest == nil || from <= 0 || to < from {
		return nil, 0
	}
	chapters := make([]plannerSourceChapterSummary, 0, min(to-from+1, len(manifest.Chapters)))
	for _, chapter := range manifest.Chapters {
		if chapter.Chapter < from || chapter.Chapter > to {
			continue
		}
		chapters = append(chapters, plannerSourceChapterSummary{
			Chapter: chapter.Chapter,
			Title:   clipText(chapter.Title, 80),
			Runes:   chapter.Runes,
		})
	}
	if len(chapters) <= maxItems || maxItems <= 0 {
		return chapters, 0
	}
	head := maxItems / 2
	tail := maxItems - head
	out := make([]plannerSourceChapterSummary, 0, maxItems)
	out = append(out, chapters[:head]...)
	out = append(out, chapters[len(chapters)-tail:]...)
	return out, len(chapters) - len(out)
}

func plannerBudgetRepairReportExcerpts(reports []domain.AdaptationSourceReport, maxItems int) ([]plannerSourceReportExcerpt, int) {
	excerpts := plannerSourceReportExcerpts(reports)
	if len(excerpts) <= maxItems || maxItems <= 0 {
		return excerpts, 0
	}
	head := maxItems / 2
	tail := maxItems - head
	out := make([]plannerSourceReportExcerpt, 0, maxItems)
	out = append(out, excerpts[:head]...)
	out = append(out, excerpts[len(excerpts)-tail:]...)
	return out, len(excerpts) - len(out)
}

func plannerSkeletonBudgetRepairVolumeSummary(batch plannerSkeletonBatch) plannerVolumeBudgetRepairVolumeSummary {
	return plannerVolumeBudgetRepairVolumeSummary{
		Index:          batch.Index,
		Title:          clipText(batch.Title, 100),
		Theme:          clipText(batch.Theme, 100),
		Goal:           clipText(batch.Goal, 140),
		Summary:        clipText(batch.Summary, 600),
		BudgetDecision: normalizePlannerBudgetDecision(batch.BudgetDecision),
		BudgetReason:   clipText(batch.BudgetReason, 240),
		TargetFrom:     batch.TargetFrom,
		TargetTo:       batch.TargetTo,
		SourceFrom:     batch.SourceFrom,
		SourceTo:       batch.SourceTo,
	}
}

func summarizePlannerVolumeForBudgetRepair(volume domain.AdaptationVolumePlan) plannerVolumeBudgetRepairVolumeSummary {
	return plannerVolumeBudgetRepairVolumeSummary{
		Index:          volume.Index,
		Title:          clipText(volume.Title, 100),
		Theme:          clipText(volume.Theme, 100),
		Goal:           clipText(volume.Goal, 140),
		Summary:        clipText(volume.Summary, 600),
		BudgetDecision: normalizePlannerBudgetDecision(volume.BudgetDecision),
		BudgetReason:   clipText(volume.BudgetReason, 240),
		TargetFrom:     volume.TargetFrom,
		TargetTo:       volume.TargetTo,
		SourceFrom:     volume.SourceFrom,
		SourceTo:       volume.SourceTo,
	}
}

func plannerVolumeBudgetRepairNeighborSummaries(volumes []domain.AdaptationVolumePlan, selectedIndex, neighborMax int) []plannerVolumeBudgetRepairVolumeSummary {
	if len(volumes) == 0 || neighborMax <= 0 {
		return nil
	}
	selectedPos := -1
	for idx, volume := range volumes {
		if volume.Index == selectedIndex {
			selectedPos = idx
			break
		}
	}
	if selectedPos < 0 {
		return nil
	}
	from := max(0, selectedPos-neighborMax)
	to := min(len(volumes)-1, selectedPos+neighborMax)
	out := make([]plannerVolumeBudgetRepairVolumeSummary, 0, to-from)
	for idx := from; idx <= to; idx++ {
		if idx == selectedPos {
			continue
		}
		out = append(out, summarizePlannerVolumeForBudgetRepair(volumes[idx]))
	}
	return out
}

func clippedStringList(values []string, maxItems, maxRunes int) []string {
	values = limitStrings(trimmedNonEmpty(values), maxItems)
	out := make([]string, 0, len(values))
	for _, value := range values {
		out = append(out, clipText(value, maxRunes))
	}
	return out
}

func plannerSignals(values []domain.AdaptationRelationshipSignal, maxItems int) []plannerSourceSignal {
	values = limitSignals(values, maxItems)
	out := make([]plannerSourceSignal, 0, len(values))
	for _, value := range values {
		if strings.TrimSpace(value.Summary) == "" {
			continue
		}
		out = append(out, plannerSourceSignal{
			Chapters:   normalizeChapterRefs(value.Chapters),
			Characters: clippedStringList(value.Characters, 6, 60),
			Type:       clipText(value.Type, 60),
			Summary:    clipText(value.Summary, 140),
			Evidence:   clipText(value.Evidence, 120),
		})
	}
	return out
}

func plannerRisks(values []domain.AdaptationRelationshipRisk, maxItems int) []plannerSourceRisk {
	values = limitRisks(values, maxItems)
	out := make([]plannerSourceRisk, 0, len(values))
	for _, value := range values {
		if strings.TrimSpace(value.Risk) == "" {
			continue
		}
		out = append(out, plannerSourceRisk{
			Chapters:   normalizeChapterRefs(value.Chapters),
			Characters: clippedStringList(value.Characters, 6, 60),
			Severity:   clipText(value.Severity, 40),
			Risk:       clipText(value.Risk, 140),
			Evidence:   clipText(value.Evidence, 120),
			Suggestion: clipText(value.Suggestion, 120),
		})
	}
	return out
}

func plannerRelationNotes(values []domain.RelationshipEntry, maxItems int) []plannerRelationNote {
	out := make([]plannerRelationNote, 0, min(len(values), maxItems))
	for _, value := range values {
		if maxItems > 0 && len(out) >= maxItems {
			break
		}
		if strings.TrimSpace(value.Relation) == "" {
			continue
		}
		out = append(out, plannerRelationNote{
			CharacterA: clipText(value.CharacterA, 60),
			CharacterB: clipText(value.CharacterB, 60),
			Relation:   clipText(value.Relation, 120),
		})
	}
	return out
}

func plannerStateNotes(values []domain.StateChange, maxItems int) []plannerStateNote {
	out := make([]plannerStateNote, 0, min(len(values), maxItems))
	for _, value := range values {
		if maxItems > 0 && len(out) >= maxItems {
			break
		}
		if strings.TrimSpace(value.Entity) == "" && strings.TrimSpace(value.Field) == "" {
			continue
		}
		out = append(out, plannerStateNote{
			Entity:   clipText(value.Entity, 60),
			Field:    clipText(value.Field, 60),
			OldValue: clipText(value.OldValue, 80),
			NewValue: clipText(value.NewValue, 80),
			Reason:   clipText(value.Reason, 120),
		})
	}
	return out
}

func plannerSourceFoundationDigest(sourceFoundation *domain.AdaptationSourceFoundation) *plannerSourceFoundationSummary {
	if sourceFoundation == nil {
		return nil
	}
	digest := &plannerSourceFoundationSummary{
		Premise:    strings.TrimSpace(sourceFoundation.Premise),
		Characters: append([]domain.Character(nil), sourceFoundation.Characters...),
		WorldRules: append([]domain.WorldRule(nil), sourceFoundation.WorldRules...),
		Volumes:    make([]plannerFoundationVolumeSummary, 0, len(sourceFoundation.Volumes)),
	}
	for _, volume := range sourceFoundation.Volumes {
		nextVolume := plannerFoundationVolumeSummary{
			Index: volume.Index,
			Title: strings.TrimSpace(volume.Title),
			Theme: strings.TrimSpace(volume.Theme),
			Arcs:  make([]plannerFoundationArcSummary, 0, len(volume.Arcs)),
		}
		for _, arc := range volume.Arcs {
			nextVolume.Arcs = append(nextVolume.Arcs, plannerFoundationArcSummary{
				Index:             arc.Index,
				Title:             strings.TrimSpace(arc.Title),
				Goal:              strings.TrimSpace(arc.Goal),
				EstimatedChapters: arc.EstimatedChapters,
			})
		}
		digest.Volumes = append(digest.Volumes, nextVolume)
	}
	if sourceFoundation.Compass != nil {
		compass := *sourceFoundation.Compass
		compass.OpenThreads = append([]string(nil), sourceFoundation.Compass.OpenThreads...)
		digest.Compass = &compass
	}
	return digest
}

func buildAdaptationPlannerSkeletonUserPrompt(
	opts ProposalOptions,
	manifest *domain.AdaptationSourceManifest,
	sourceFoundation *domain.AdaptationSourceFoundation,
	sourceMap []plannerSourceMapEntry,
	targetChapterHint int,
) (string, error) {
	requirements := []string{
		"Return exactly one JSON skeleton object and no prose.",
		"Do not wrap the JSON in markdown fences.",
		"Do not return the final AdaptationPlan here.",
		"Do not include a chapters array in the skeleton step; chapter details are generated in later batch calls.",
		"mainline_rules and relationship_goals must each be a JSON array containing strings only; use [] when empty and never place objects in these arrays.",
		"Choose how many target chapters this source-map range needs, then divide it into one or more model-planned batches/volumes.",
	}
	requirements = append(requirements, plannerSkeletonBudgetRequirements(opts.Granularity)...)
	requirements = append(requirements,
		"If target_chapter_hint_role is source_scale_minimum, treat target_chapter_hint as anti-shrink long-form scale guidance, not an exact final chapter count.",
		"Choose final target_chapter_count after analyzing source_map and the user's additions; increase above target_chapter_hint when added plot, relationship, or transition arcs require more chapters.",
		"If target_chapter_hint_role is explicit_target_scale, honor that requested scale unless the source map and user changes make a different count necessary; explain the choice in batch summaries.",
		"Use source_map ranges to preserve the full-book structure without requesting raw source_reports.",
		"Each batch must include chapter_count, source_from, source_to, title, theme/goal, and summary.",
		"Each batch may include budget_decision as balanced, compress_or_merge, or expand_or_split; include budget_reason when the chapter_count intentionally compresses/merges/deletes source material or expands/splits/adds pacing beyond the review range.",
		"Do not calculate target_from or target_to in this source-map skeleton step; the host will assign continuous target chapter ranges from chapter_count.",
		"Choose skeleton batches by coherent story arc, volume beat, or major plot movement; recommended_batch_max is a detail/persistence limit, not a reason to fragment a coherent movement.",
		"The host will split oversized skeleton batches into detail calls and final generated outline batches of about recommended_batch_max target chapters.",
	)
	requirements = append(requirements, plannerSkeletonSourceRangeRequirements(opts.Granularity)...)
	if opts.Granularity == domain.AdaptationGranularityArc {
		requirements = append(requirements,
			"source_map.mainline_events are high-level promises. Reserve enough target chapter positions for all of them before adding new plot.",
			"Do not replace a required initial meeting, core case, identity reveal, fate turn, relationship milestone, major turn, foreshadowing, or payoff with an invented event.",
		)
	}
	input := struct {
		Brief                string                          `json:"brief"`
		Granularity          string                          `json:"granularity"`
		RewritePolicy        string                          `json:"rewrite_policy"`
		WordTolerance        float64                         `json:"word_tolerance"`
		TargetChapterHint    int                             `json:"target_chapter_hint,omitempty"`
		TargetChapterRole    string                          `json:"target_chapter_hint_role,omitempty"`
		RecommendedBatchMax  int                             `json:"recommended_batch_max"`
		ChapterBudgetPolicy  *plannerChapterBudgetPolicy     `json:"chapter_budget_policy,omitempty"`
		SourceManifest       plannerSourceManifestSummary    `json:"source_manifest"`
		SourceFoundation     *plannerSourceFoundationSummary `json:"source_foundation"`
		SourceMap            []plannerSourceMapEntry         `json:"source_map"`
		SourceMapNotes       []string                        `json:"source_map_notes"`
		SourceMapBudgetNotes []string                        `json:"source_map_budget_notes,omitempty"`
		Requirements         []string                        `json:"requirements"`
	}{
		Brief:               opts.Brief,
		Granularity:         opts.Granularity,
		RewritePolicy:       opts.RewritePolicy,
		WordTolerance:       opts.WordTolerance,
		TargetChapterHint:   targetChapterHint,
		TargetChapterRole:   plannerTargetChapterHintRole(opts, manifest, targetChapterHint),
		RecommendedBatchMax: adaptationPlannerRecommendedBatchMax,
		ChapterBudgetPolicy: plannerChapterBudgetPolicyForGranularity(opts.Granularity),
		SourceManifest:      plannerManifestSummary(manifest),
		SourceFoundation:    plannerSourceFoundationDigest(sourceFoundation),
		SourceMap:           sourceMap,
		SourceMapNotes: []string{
			"source_map is a compact, resumable dossier built from source-report batches; use it instead of raw per-chapter reports for this skeleton step.",
			"Each source_map entry covers an inclusive source range and preserves high-level causality, plot threads, character arcs, world constraints, and relationship signals.",
		},
		SourceMapBudgetNotes: plannerSourceMapBudgetNotes(sourceMap, opts.Granularity),
		Requirements:         requirements,
	}
	raw, err := json.MarshalIndent(input, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal planner skeleton input: %w", err)
	}
	return "Plan one source-map portion of the high-level long-form adaptation skeleton. Use the current model to decide how many target chapters this source range needs; do not mechanically mirror or compress source chapters.\n\n" +
		"Output contract: return exactly one JSON object, no prose and no markdown. The top-level object must contain a batches array. Do not return chapter details in this step. The host will concatenate all source-map portions and renumber target chapters globally.\n\n" +
		"Required JSON shape:\n" +
		"{\"granularity\":\"...\",\"status\":\"proposal\",\"rewrite_policy\":\"...\",\"brief\":\"...\",\"target_chapter_count\":60,\"mainline_rules\":[],\"relationship_goals\":[],\"batches\":[{\"index\":1,\"title\":\"...\",\"theme\":\"...\",\"chapter_count\":8,\"source_from\":1,\"source_to\":3,\"budget_decision\":\"balanced\",\"budget_reason\":\"...\",\"summary\":\"...\"}]}.\n\n" +
		"Planning input:\n```json\n" + string(raw) + "\n```", nil
}

func plannerSkeletonBudgetRequirements(granularity string) []string {
	if plannerEnforcesSourceRuneSplitting(granularity) {
		return []string{
			"If chapter_budget_policy is present, source_map.source_runes is a capacity review signal for arc/full_rewrite skeleton planning: a long single source chapter may need multiple target chapters, using the source_map_budget_notes review capacity, while final word_budget.max_runes remains capped at chapter_budget_policy.max_runes.",
			"Read source_map_budget_notes before choosing chapter_count. Treat those notes as first-pass budget guidance, not repair-only feedback.",
		}
	}
	return []string{
		"If chapter_budget_policy is present, it limits target chapter budgets only: keep each target word_budget.max_runes within chapter_budget_policy.max_runes, but do not turn source_map.source_runes into a minimum target chapter count for free/full_rewrite.",
		"Read source_map_budget_notes before choosing chapter_count. For free/full_rewrite, treat those notes as source density and context guidance; choose chapter_count from the new story structure.",
	}
}

func plannerSkeletonSourceRangeRequirements(granularity string) []string {
	if plannerAllowsSharedSourceMapEntryRanges(granularity) {
		return []string{
			"For free/full_rewrite, this request already has a fixed source_map range. Keep every returned batch source_from/source_to inside that provided range; multiple returned batches may share or overlap the same source range when they represent different new-story beats.",
			"Do not spend structure-repair effort on making free/full_rewrite source ranges a strict partition; use chapter_count, title, theme/goal, and summary to express the new story structure.",
		}
	}
	return []string{
		"Suggest source ranges across returned batches in story order. The host will normalize them into a strict inclusive partition that covers every source chapter once.",
	}
}

type plannerPreviousChapterContext struct {
	Chapter        int      `json:"chapter"`
	Title          string   `json:"title,omitempty"`
	CoreEvent      string   `json:"core_event,omitempty"`
	Hook           string   `json:"hook,omitempty"`
	Scenes         []string `json:"scenes,omitempty"`
	SourceChapters []int    `json:"source_chapters,omitempty"`
}

func plannerPreviousChapterContexts(chapters []domain.AdaptationChapterPlan, maxItems int) []plannerPreviousChapterContext {
	if maxItems <= 0 || len(chapters) == 0 {
		return nil
	}
	start := len(chapters) - maxItems
	if start < 0 {
		start = 0
	}
	out := make([]plannerPreviousChapterContext, 0, len(chapters)-start)
	for _, chapter := range chapters[start:] {
		out = append(out, plannerPreviousChapterContext{
			Chapter:        chapter.Chapter,
			Title:          clipText(chapter.Title, 80),
			CoreEvent:      clipText(chapter.CoreEvent, 160),
			Hook:           clipText(chapter.Hook, 120),
			Scenes:         clippedStringList(chapter.Scenes, 4, 80),
			SourceChapters: append([]int(nil), chapter.SourceChapters...),
		})
	}
	return out
}

func buildAdaptationPlannerBatchUserPrompt(
	opts ProposalOptions,
	manifest *domain.AdaptationSourceManifest,
	sourceFoundation *domain.AdaptationSourceFoundation,
	skeleton plannerSkeleton,
	batch plannerSkeletonBatch,
	reports []domain.AdaptationSourceReport,
	previousChapters []domain.AdaptationChapterPlan,
) (string, error) {
	requirements := []string{
		"Return exactly one JSON object and no prose.",
		"The top-level JSON object must be shaped exactly like {\"chapters\":[...]} and must not be a single chapter object.",
		"Return only the chapters for the requested batch, but return the complete requested batch.",
		"chapters length must equal expected_chapters.",
		"Use absolute target chapter numbers from batch.target_from through batch.target_to.",
		"Every chapter field must be an integer absolute target chapter number, not a string label like \"第1章\".",
		"Every returned chapter must include chapter, title, core_event, hook, scenes, source_chapters, source_range, word_budget, preserve_events, required_changes, and forbidden_moves.",
		"Every source_chapters value must be within the batch source range and valid for the analyzed source.",
		"Added/bridging chapters must still include source_chapters anchors.",
		"Treat batch.source_from/source_to as the parent source coverage for this detail call. For full_rewrite, prefer setting every returned chapter's source_range to the full batch.source_from/source_to range unless the chapter is explicitly outside that source arc.",
		"source_chapters should be sparse representative anchors inside source_range, not an exhaustive per-target mapping. Reusing the same anchors across chapters is allowed when the chapters share one source arc.",
		"Do not use the target chapter number as a source chapter number unless that source chapter is actually inside batch.source_from/source_to.",
	}
	requirements = append(requirements, plannerBatchBudgetRequirements(opts.Granularity)...)
	switch opts.Granularity {
	case domain.AdaptationGranularityArc:
		requirements = append(requirements,
			"Every chapter must include event_ids and added_event_ids arrays.",
			"Assign every batch.mainline_event_ids value to exactly one target chapter event_ids entry; do not omit, duplicate, paraphrase, or replace these stable IDs.",
			"Use only this detail batch's batch.mainline_event_ids for required mainline bindings. Do not copy mainline IDs from the parent skeleton or previous detail batches.",
			"Supporting and texture source event_ids are optional references, but they must come from batch.allowed_event_ids. If you include one, assign it exactly once to the target chapter whose title/core_event/hook/scenes actually contain that event's plot beat.",
			"When preserve_events references a stable source event, use the exact stable ID as the whole array item. Do not append a colon or event description to that ID; put readable descriptions in core_event and scenes.",
			"Never invent a source event ID. A genuinely new target-story event belongs in added_event_ids, not event_ids.",
			"Do not copy a later chapter's supporting/texture event_id into the current chapter. Move the event_id, preserve_events, required_changes, and matching story beat together; do not keep a future beat after deleting only its ID.",
			"Keep scene density and output capacity consistent: for arc chapters, word_budget.max_runes must be at least 300 runes per planned scene, or reduce/split the scenes before returning the outline.",
			"Put new plot IDs only in added_event_ids; added events may support assigned mainline events but cannot take their chapter space.",
		)
	case domain.AdaptationGranularityFree:
		requirements = append(requirements,
			"Every chapter must include stable target event_ids; use added_event_ids for events invented beyond the current target skeleton.",
			"Plan target-story prerequisites with depends_on_event_ids and structured relationship/setting transitions; ordinary source events are optional references, not coverage obligations.",
		)
	}
	if len(batch.PriorOwnedEventIDs) > 0 {
		requirements = append(requirements,
			"batch.prior_owned_event_ids are already owned by earlier accepted detail chapters. They are forbidden in this batch: never reuse them in event_ids or preserve_events, and do not retain their matching story beat after removing an ID.",
		)
	}
	requirements = append(requirements,
		"Use the user's adaptation brief and the skeleton batch goal; do not ignore earlier user planning.",
		"Use previous_detail_chapters only for continuity, callbacks, and handoff hooks; do not duplicate already generated chapters.",
		"Continue from the latest previous_detail_chapters hook and state changes so this small detail batch reads as the next part of the same parent volume.",
	)
	if strings.TrimSpace(opts.outlineQualityFeedback) != "" {
		requirements = append(requirements, opts.outlineQualityFeedback)
	}
	input := struct {
		Brief                  string                          `json:"brief"`
		Granularity            string                          `json:"granularity"`
		RewritePolicy          string                          `json:"rewrite_policy"`
		WordTolerance          float64                         `json:"word_tolerance"`
		ExpectedChapters       int                             `json:"expected_chapters"`
		ChapterBudgetPolicy    *plannerChapterBudgetPolicy     `json:"chapter_budget_policy,omitempty"`
		SourceManifest         plannerSourceManifestSummary    `json:"source_manifest"`
		SourceFoundation       *plannerSourceFoundationSummary `json:"source_foundation"`
		Skeleton               plannerSkeleton                 `json:"skeleton"`
		Batch                  plannerSkeletonBatch            `json:"batch"`
		PreviousDetailChapters []plannerPreviousChapterContext `json:"previous_detail_chapters,omitempty"`
		SourceReports          []plannerSourceReportExcerpt    `json:"source_reports"`
		SourceReportNotes      []string                        `json:"source_report_notes"`
		Requirements           []string                        `json:"requirements"`
		OutlineQualityFeedback string                          `json:"outline_quality_feedback,omitempty"`
	}{
		Brief:                  opts.Brief,
		Granularity:            opts.Granularity,
		RewritePolicy:          opts.RewritePolicy,
		WordTolerance:          opts.WordTolerance,
		ExpectedChapters:       batch.TargetTo - batch.TargetFrom + 1,
		ChapterBudgetPolicy:    plannerChapterBudgetPolicyForGranularity(opts.Granularity),
		SourceManifest:         plannerManifestSummary(manifest),
		SourceFoundation:       plannerSourceFoundationDigestForDetail(sourceFoundation),
		Skeleton:               plannerSkeletonForDetailPrompt(skeleton, batch),
		Batch:                  batch,
		PreviousDetailChapters: plannerPreviousChapterContexts(previousChapters, adaptationPlannerContinuityChapterMax),
		SourceReports:          plannerSourceReportExcerptsForDetail(reports, opts.Granularity, batch),
		SourceReportNotes: []string{
			"source_reports are clipped excerpts for the requested source range, not full raw reports.",
			"Use source_range and source_chapters as factual anchors; do not copy source prose.",
			"For arc/free full_rewrite, source_range is a broad coverage envelope, not a claim that the target chapter corresponds exactly to one original chapter.",
		},
		Requirements:           requirements,
		OutlineQualityFeedback: strings.TrimSpace(opts.outlineQualityFeedback),
	}
	raw, err := json.MarshalIndent(input, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal planner batch input: %w", err)
	}
	expected := batch.TargetTo - batch.TargetFrom + 1
	return fmt.Sprintf(
		"Expand model-planned adaptation batch %d into concrete chapter plans.\n\n"+
			"Output contract: return exactly one JSON object, no prose and no markdown. The top-level object must be {\"chapters\":[...]}.\n"+
			"Return exactly %d chapter objects, numbered with integer chapter values from %d through %d. Do not return one standalone chapter object. Do not return only a summary, outline, or key_turns.\n\n"+
			"Minimal valid shape:\n"+
			"{\"chapters\":[{\"chapter\":%d,\"title\":\"...\",\"core_event\":\"...\",\"hook\":\"...\",\"scenes\":[\"...\"],\"source_chapters\":[%d],\"source_range\":{\"from\":%d,\"to\":%d},\"word_budget\":{\"source_runes\":1000,\"target_runes\":1500,\"min_runes\":1400,\"max_runes\":1600,\"tolerance\":0.15},\"preserve_events\":[\"...\"],\"required_changes\":[\"...\"],\"forbidden_moves\":[\"...\"]}]}\n\n"+
			"Invalid shapes: {\"chapter\":%d,...}; {\"summary\":\"...\"}; {\"key_turns\":[...]}; markdown text outside JSON.\n\n"+
			"Planning input:\n```json\n%s\n```",
		batch.Index,
		expected,
		batch.TargetFrom,
		batch.TargetTo,
		batch.TargetFrom,
		batch.SourceFrom,
		batch.SourceFrom,
		batch.SourceTo,
		batch.TargetFrom,
		string(raw),
	), nil
}

func plannerSkeletonForDetailPrompt(skeleton plannerSkeleton, detailBatch plannerSkeletonBatch) plannerSkeleton {
	context := skeleton
	context.Batches = nil
	for _, candidate := range skeleton.Batches {
		if candidate.TargetFrom <= detailBatch.TargetFrom && candidate.TargetTo >= detailBatch.TargetTo {
			candidate.MainlineEventIDs = append([]string(nil), detailBatch.MainlineEventIDs...)
			context.Batches = []plannerSkeletonBatch{candidate}
			return context
		}
	}
	context.Batches = []plannerSkeletonBatch{detailBatch}
	return context
}

func plannerSourceFoundationDigestForDetail(sourceFoundation *domain.AdaptationSourceFoundation) *plannerSourceFoundationSummary {
	digest := plannerSourceFoundationDigest(sourceFoundation)
	if digest == nil {
		return nil
	}
	// The current parent skeleton batch already carries the relevant volume goal.
	// Repeating the source foundation's full chapter/arc tree in every 3-4 chapter
	// detail call can exceed the planner context before the model is invoked.
	digest.Volumes = nil
	return digest
}

func plannerBatchBudgetRequirements(granularity string) []string {
	requirements := []string{
		"If chapter_budget_policy is present, keep every word_budget.max_runes within chapter_budget_policy.max_runes; set word_budget.source_runes to this target chapter's share of the covered source material, not the full broad source_range total.",
	}
	if domain.NormalizeAdaptationGranularity(granularity) == domain.AdaptationGranularityArc {
		requirements = append(requirements, "For arc chapters, word_budget.max_runes must be at least 300 runes per planned scene; if a chapter needs a smaller budget, reduce or split its scenes before returning the outline.")
	}
	if plannerEnforcesSourceRuneSplitting(granularity) {
		return append(requirements, "If the full batch source range has more source_runes than chapter_budget_policy.max_runes, cover it with enough target chapters in the parent skeleton batch; the host validates this at the parent batch level.")
	}
	return append(requirements, "For free/full_rewrite, do not add target chapters solely because the broad batch source range has more source_runes than chapter_budget_policy.max_runes; source_runes is background density, while word_budget.max_runes remains the per-target chapter output limit.")
}

func parsePlannerSkeleton(text string) (plannerSkeleton, error) {
	segments, err := extractPlannerJSONSegments(text)
	if err != nil {
		return plannerSkeleton{}, fmt.Errorf("extract planner skeleton JSON: %w", err)
	}
	var first plannerSkeleton
	var firstShape string
	var firstErr error
	for _, segment := range segments {
		skeleton, err := decodePlannerSkeletonJSON([]byte(segment))
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if firstShape == "" {
			first = skeleton
			firstShape = plannerProposalShapeSummary([]byte(segment))
		}
		if len(skeleton.Batches) > 0 {
			return skeleton, nil
		}
	}
	if firstErr != nil && firstShape == "" {
		return plannerSkeleton{}, fmt.Errorf("parse planner skeleton JSON: %w", firstErr)
	}
	if firstShape != "" {
		return first, fmt.Errorf("planner skeleton has no batches (%s)", firstShape)
	}
	return plannerSkeleton{}, fmt.Errorf("planner skeleton has no decodable JSON object")
}

func decodePlannerSkeletonJSON(data []byte) (plannerSkeleton, error) {
	var skeleton plannerSkeleton
	if err := json.Unmarshal(data, &skeleton); err != nil {
		return skeleton, err
	}
	fillPlannerSkeletonAliases(data, &skeleton)
	if len(skeleton.Batches) > 0 {
		return skeleton, nil
	}
	if batch, ok := decodePlannerSkeletonBatchJSON(data); ok {
		skeleton.Batches = []plannerSkeletonBatch{batch}
		if skeleton.TargetChapterCount <= 0 {
			skeleton.TargetChapterCount = batch.TargetChapterCount
		}
		if skeleton.TargetChapterCount <= 0 && batch.TargetTo >= batch.TargetFrom {
			skeleton.TargetChapterCount = batch.TargetTo - batch.TargetFrom + 1
		}
		return skeleton, nil
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(data, &envelope); err != nil {
		return skeleton, nil
	}
	envelopeKeys := append([]string{}, plannerEnvelopeKeys...)
	envelopeKeys = append(envelopeKeys, "skeleton", "structure")
	for _, key := range envelopeKeys {
		raw := envelope[key]
		if len(raw) == 0 || raw[0] != '{' {
			continue
		}
		nested, err := decodePlannerSkeletonJSON(raw)
		if err == nil && len(nested.Batches) > 0 {
			return nested, nil
		}
	}
	return skeleton, nil
}

func decodePlannerSkeletonBatchJSON(data []byte) (plannerSkeletonBatch, bool) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(data, &object); err != nil || !plannerSkeletonBatchObjectShape(object) {
		return plannerSkeletonBatch{}, false
	}
	var batch plannerSkeletonBatch
	if err := json.Unmarshal(data, &batch); err != nil {
		return plannerSkeletonBatch{}, false
	}
	return batch, true
}

func plannerSkeletonBatchObjectShape(object map[string]json.RawMessage) bool {
	if len(object) == 0 || len(object["batches"]) > 0 {
		return false
	}
	hasTarget := len(object["target_from"]) > 0 || len(object["target_to"]) > 0 || len(object["target_range"]) > 0
	hasSource := len(object["source_from"]) > 0 || len(object["source_to"]) > 0 || len(object["source_range"]) > 0 || len(object["source_chapters"]) > 0
	hasStory := len(object["title"]) > 0 || len(object["theme"]) > 0 || len(object["goal"]) > 0 || len(object["summary"]) > 0
	return hasTarget && hasSource && hasStory
}

var plannerSkeletonBatchAliasKeys = []string{"batches", "chunks", "parts", "volumes", "arcs", "segments"}

func fillPlannerSkeletonAliases(data []byte, skeleton *plannerSkeleton) {
	if skeleton == nil {
		return
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(data, &object); err != nil {
		return
	}
	skeleton.TargetChapterCount = firstJSONInt(object, skeleton.TargetChapterCount,
		"target_chapter_count", "targetChapterCount", "total_chapters", "chapter_count", "chapters_count", "target_count")
	for _, key := range plannerSkeletonBatchAliasKeys {
		raw := object[key]
		if len(raw) == 0 || raw[0] != '[' {
			continue
		}
		var batches []plannerSkeletonBatch
		if err := json.Unmarshal(raw, &batches); err != nil {
			continue
		}
		if len(batches) > 0 {
			skeleton.Batches = batches
			return
		}
	}
}

func normalizePlannerSkeleton(skeleton *plannerSkeleton, opts ProposalOptions, manifest *domain.AdaptationSourceManifest, targetChapterHint int) error {
	if skeleton == nil {
		return fmt.Errorf("nil skeleton")
	}
	if manifest == nil || manifest.ChapterCount <= 0 {
		return fmt.Errorf("source manifest missing")
	}
	if strings.TrimSpace(skeleton.Granularity) == "" {
		skeleton.Granularity = opts.Granularity
	}
	if strings.TrimSpace(skeleton.Status) == "" {
		skeleton.Status = domain.AdaptationPlanStatusProposal
	}
	if strings.TrimSpace(skeleton.RewritePolicy) == "" {
		skeleton.RewritePolicy = opts.RewritePolicy
	}
	skeleton.Brief = opts.Brief
	if skeleton.Granularity != opts.Granularity {
		return fmt.Errorf("granularity=%q, want %q", skeleton.Granularity, opts.Granularity)
	}
	if skeleton.Status != domain.AdaptationPlanStatusProposal {
		return fmt.Errorf("status=%q, want proposal", skeleton.Status)
	}
	if skeleton.RewritePolicy != opts.RewritePolicy {
		return fmt.Errorf("rewrite_policy=%q, want %q", skeleton.RewritePolicy, opts.RewritePolicy)
	}
	if len(skeleton.Batches) == 0 {
		return fmt.Errorf("no batches")
	}
	if skeleton.TargetChapterCount <= 0 {
		skeleton.TargetChapterCount = targetChapterHint
	}
	if plannerTargetChapterHintRole(opts, manifest, targetChapterHint) == "explicit_target_scale" && targetChapterHint >= adaptationPlannerChunkedMinChapters && skeleton.TargetChapterCount > 0 {
		minAccepted := targetChapterHint * 4 / 5
		if minAccepted < adaptationPlannerChunkedMinChapters {
			minAccepted = adaptationPlannerChunkedMinChapters
		}
		if skeleton.TargetChapterCount < minAccepted {
			return fmt.Errorf("target_chapter_count=%d ignores long-form scale hint %d", skeleton.TargetChapterCount, targetChapterHint)
		}
	}
	nextTarget := 1
	for idx := range skeleton.Batches {
		batch := &skeleton.Batches[idx]
		batch.BudgetDecision = normalizePlannerBudgetDecision(batch.BudgetDecision)
		batch.BudgetReason = strings.TrimSpace(batch.BudgetReason)
		if batch.Index <= 0 {
			batch.Index = idx + 1
		}
		count := batch.TargetChapterCount
		if count <= 0 {
			if batch.TargetFrom <= 0 || batch.TargetTo < batch.TargetFrom {
				return fmt.Errorf("batch %d must include chapter_count or a valid target range", batch.Index)
			}
			count = batch.TargetTo - batch.TargetFrom + 1
		}
		batch.TargetChapterCount = count
		batch.TargetFrom = nextTarget
		batch.TargetTo = nextTarget + count - 1
		if batch.SourceFrom <= 0 || batch.SourceTo <= 0 {
			minSource, maxSource := minMaxPositive(batch.SourceChapters)
			if batch.SourceFrom <= 0 {
				batch.SourceFrom = minSource
			}
			if batch.SourceTo <= 0 {
				batch.SourceTo = maxSource
			}
		}
		if batch.SourceFrom <= 0 || batch.SourceTo < batch.SourceFrom || batch.SourceTo > manifest.ChapterCount {
			return fmt.Errorf("batch %d has invalid source range %d-%d", batch.Index, batch.SourceFrom, batch.SourceTo)
		}
		nextTarget = batch.TargetTo + 1
	}
	lastTarget := nextTarget - 1
	if skeleton.TargetChapterCount <= 0 {
		skeleton.TargetChapterCount = lastTarget
	}
	if lastTarget != skeleton.TargetChapterCount {
		skeleton.TargetChapterCount = lastTarget
	}
	if budgetErrs := plannerSkeletonBudgetSplitErrors(skeleton.Batches, opts, manifest); len(budgetErrs) > 0 {
		return budgetErrs
	}
	return nil
}

func normalizePlannerBatchChapters(chapters []domain.AdaptationChapterPlan, batch plannerSkeletonBatch) ([]domain.AdaptationChapterPlan, error) {
	out, missing, err := normalizePlannerBatchChapterSubset(chapters, batch)
	if err != nil {
		return nil, err
	}
	if len(missing) > 0 {
		wantCount := batch.TargetTo - batch.TargetFrom + 1
		return nil, fmt.Errorf("chapter count=%d, want %d for target range %d-%d; missing chapters %s", len(out), wantCount, batch.TargetFrom, batch.TargetTo, formatPlannerChapterList(missing))
	}
	return out, nil
}

func normalizePlannerBatchChapterSubset(chapters []domain.AdaptationChapterPlan, batch plannerSkeletonBatch) ([]domain.AdaptationChapterPlan, []int, error) {
	if len(chapters) == 0 {
		return nil, nil, fmt.Errorf("no chapters")
	}
	out := normalizePlannerBatchChapterNumbers(chapters, batch)
	byChapter := make(map[int]domain.AdaptationChapterPlan, len(out))
	for _, chapter := range out {
		if chapter.Chapter < batch.TargetFrom || chapter.Chapter > batch.TargetTo {
			return nil, nil, fmt.Errorf("chapter %d outside target range %d-%d", chapter.Chapter, batch.TargetFrom, batch.TargetTo)
		}
		if _, exists := byChapter[chapter.Chapter]; exists {
			return nil, nil, fmt.Errorf("duplicate chapter %d in target range %d-%d", chapter.Chapter, batch.TargetFrom, batch.TargetTo)
		}
		byChapter[chapter.Chapter] = chapter
	}
	return sortedPlannerBatchChapters(byChapter), missingPlannerBatchChapters(byChapter, batch), nil
}

func normalizePlannerBatchChapterNumbers(chapters []domain.AdaptationChapterPlan, batch plannerSkeletonBatch) []domain.AdaptationChapterPlan {
	out := append([]domain.AdaptationChapterPlan(nil), chapters...)
	for index := range out {
		out[index].PreserveEvents = domain.NormalizeSourceEventReferences(out[index].PreserveEvents)
	}
	if batch.TargetFrom <= 1 || len(out) == 0 {
		return out
	}
	wantCount := batch.TargetTo - batch.TargetFrom + 1
	allRelative := true
	for _, chapter := range out {
		if chapter.Chapter < 1 || chapter.Chapter > wantCount {
			allRelative = false
			break
		}
	}
	if allRelative {
		offset := batch.TargetFrom - 1
		for idx := range out {
			out[idx].Chapter += offset
		}
	}
	return out
}

func sortedPlannerBatchChapters(byChapter map[int]domain.AdaptationChapterPlan) []domain.AdaptationChapterPlan {
	chapters := make([]domain.AdaptationChapterPlan, 0, len(byChapter))
	for _, chapter := range byChapter {
		chapters = append(chapters, chapter)
	}
	sort.Slice(chapters, func(i, j int) bool {
		return chapters[i].Chapter < chapters[j].Chapter
	})
	return chapters
}

func missingPlannerBatchChapters(byChapter map[int]domain.AdaptationChapterPlan, batch plannerSkeletonBatch) []int {
	var missing []int
	for chapter := batch.TargetFrom; chapter <= batch.TargetTo; chapter++ {
		if _, exists := byChapter[chapter]; !exists {
			missing = append(missing, chapter)
		}
	}
	return missing
}

func salvagePlannerBatchChapterSubset(chapters []domain.AdaptationChapterPlan, batch plannerSkeletonBatch) ([]domain.AdaptationChapterPlan, []int) {
	out := normalizePlannerBatchChapterNumbers(chapters, batch)
	byChapter := make(map[int]domain.AdaptationChapterPlan, len(out))
	for _, chapter := range out {
		if chapter.Chapter < batch.TargetFrom || chapter.Chapter > batch.TargetTo {
			continue
		}
		if _, exists := byChapter[chapter.Chapter]; exists {
			continue
		}
		byChapter[chapter.Chapter] = chapter
	}
	return sortedPlannerBatchChapters(byChapter), missingPlannerBatchChapters(byChapter, batch)
}

func normalizeProposalRevisionBatchChapterSubset(chapters []domain.AdaptationChapterPlan, batch plannerSkeletonBatch, expansionMaxTo int) ([]domain.AdaptationChapterPlan, []int, error) {
	if len(chapters) == 0 {
		return nil, nil, fmt.Errorf("no chapters")
	}
	if expansionMaxTo < batch.TargetTo {
		expansionMaxTo = batch.TargetTo
	}
	out := normalizeProposalRevisionBatchChapterNumbers(chapters, batch, expansionMaxTo)
	byChapter := make(map[int]domain.AdaptationChapterPlan, len(out))
	for _, chapter := range out {
		if chapter.Chapter < batch.TargetFrom || chapter.Chapter > expansionMaxTo {
			return nil, nil, fmt.Errorf("chapter %d outside revision range %d-%d", chapter.Chapter, batch.TargetFrom, expansionMaxTo)
		}
		if _, exists := byChapter[chapter.Chapter]; exists {
			return nil, nil, fmt.Errorf("duplicate chapter %d in revision range %d-%d", chapter.Chapter, batch.TargetFrom, expansionMaxTo)
		}
		byChapter[chapter.Chapter] = chapter
	}
	expectedTo := max(batch.TargetTo, maxChapterInMap(byChapter))
	return sortedPlannerBatchChapters(byChapter), missingProposalRevisionChapters(byChapter, batch.TargetFrom, expectedTo), nil
}

func normalizeProposalRevisionBatchChapterNumbers(chapters []domain.AdaptationChapterPlan, batch plannerSkeletonBatch, expansionMaxTo int) []domain.AdaptationChapterPlan {
	out := append([]domain.AdaptationChapterPlan(nil), chapters...)
	if batch.TargetFrom <= 1 || len(out) == 0 {
		return out
	}
	if expansionMaxTo < batch.TargetTo {
		expansionMaxTo = batch.TargetTo
	}
	wantCount := expansionMaxTo - batch.TargetFrom + 1
	allRelative := true
	for _, chapter := range out {
		if chapter.Chapter < 1 || chapter.Chapter > wantCount {
			allRelative = false
			break
		}
	}
	if allRelative {
		offset := batch.TargetFrom - 1
		for idx := range out {
			out[idx].Chapter += offset
		}
	}
	return out
}

func missingProposalRevisionChapters(byChapter map[int]domain.AdaptationChapterPlan, from, to int) []int {
	var missing []int
	for chapter := from; chapter <= to; chapter++ {
		if _, exists := byChapter[chapter]; !exists {
			missing = append(missing, chapter)
		}
	}
	return missing
}

func salvageProposalRevisionBatchChapterSubset(chapters []domain.AdaptationChapterPlan, batch plannerSkeletonBatch, expansionMaxTo int) ([]domain.AdaptationChapterPlan, []int) {
	if expansionMaxTo < batch.TargetTo {
		expansionMaxTo = batch.TargetTo
	}
	out := normalizeProposalRevisionBatchChapterNumbers(chapters, batch, expansionMaxTo)
	byChapter := make(map[int]domain.AdaptationChapterPlan, len(out))
	for _, chapter := range out {
		if chapter.Chapter < batch.TargetFrom || chapter.Chapter > expansionMaxTo {
			continue
		}
		if _, exists := byChapter[chapter.Chapter]; exists {
			continue
		}
		byChapter[chapter.Chapter] = chapter
	}
	expectedTo := max(batch.TargetTo, maxChapterInMap(byChapter))
	return sortedPlannerBatchChapters(byChapter), missingProposalRevisionChapters(byChapter, batch.TargetFrom, expectedTo)
}

func maxChapterInPlans(chapters []domain.AdaptationChapterPlan) int {
	maxChapter := 0
	for _, chapter := range chapters {
		if chapter.Chapter > maxChapter {
			maxChapter = chapter.Chapter
		}
	}
	return maxChapter
}

func maxChapterInMap(chapters map[int]domain.AdaptationChapterPlan) int {
	maxChapter := 0
	for chapter := range chapters {
		if chapter > maxChapter {
			maxChapter = chapter
		}
	}
	return maxChapter
}

func mergePlannerBatchChapterSubsets(existing []domain.AdaptationChapterPlan, incoming []domain.AdaptationChapterPlan, batch plannerSkeletonBatch) ([]domain.AdaptationChapterPlan, []int, error) {
	current, _, err := normalizePlannerBatchChapterSubset(existing, batch)
	if err != nil {
		return nil, nil, err
	}
	next, _, err := normalizePlannerBatchChapterSubset(incoming, batch)
	if err != nil {
		return current, nil, err
	}
	byChapter := make(map[int]domain.AdaptationChapterPlan, len(current)+len(next))
	for _, chapter := range current {
		byChapter[chapter.Chapter] = chapter
	}
	for _, chapter := range next {
		if _, exists := byChapter[chapter.Chapter]; exists {
			continue
		}
		byChapter[chapter.Chapter] = chapter
	}
	merged := sortedPlannerBatchChapters(byChapter)
	return merged, missingPlannerBatchChapters(byChapter, batch), nil
}

func formatPlannerChapterList(chapters []int) string {
	if len(chapters) == 0 {
		return "[]"
	}
	parts := make([]string, len(chapters))
	for idx, chapter := range chapters {
		parts[idx] = strconv.Itoa(chapter)
	}
	return "[" + strings.Join(parts, ",") + "]"
}

func adaptationVolumesFromSkeleton(skeleton plannerSkeleton) []domain.AdaptationVolumePlan {
	volumes := make([]domain.AdaptationVolumePlan, 0, len(skeleton.Batches))
	for _, batch := range skeleton.Batches {
		title := strings.TrimSpace(batch.Title)
		theme := strings.TrimSpace(batch.Theme)
		goal := strings.TrimSpace(batch.Goal)
		summary := strings.TrimSpace(batch.Summary)
		if title == "" {
			title = fmt.Sprintf("第 %d-%d 章", batch.TargetFrom, batch.TargetTo)
		}
		volumes = append(volumes, domain.AdaptationVolumePlan{
			Index:            batch.Index,
			Title:            title,
			Theme:            theme,
			Goal:             goal,
			Summary:          summary,
			BudgetDecision:   normalizePlannerBudgetDecision(batch.BudgetDecision),
			BudgetReason:     strings.TrimSpace(batch.BudgetReason),
			TargetFrom:       batch.TargetFrom,
			TargetTo:         batch.TargetTo,
			SourceFrom:       batch.SourceFrom,
			SourceTo:         batch.SourceTo,
			MainlineEventIDs: append([]string(nil), batch.MainlineEventIDs...),
			Notes:            append(domain.TextList(nil), batch.Notes...),
		})
	}
	return normalizeAdaptationProposalVolumes(volumes, skeleton.TargetChapterCount)
}

func normalizeAdaptationProposalVolumes(volumes []domain.AdaptationVolumePlan, chapterCount int) []domain.AdaptationVolumePlan {
	if len(volumes) == 0 || chapterCount <= 0 {
		return nil
	}
	out := make([]domain.AdaptationVolumePlan, 0, len(volumes))
	for _, volume := range volumes {
		if volume.TargetFrom <= 0 || volume.TargetTo < volume.TargetFrom || volume.TargetTo > chapterCount {
			continue
		}
		if volume.Index <= 0 {
			volume.Index = len(out) + 1
		}
		volume.Title = strings.TrimSpace(volume.Title)
		volume.Theme = strings.TrimSpace(volume.Theme)
		volume.Goal = strings.TrimSpace(volume.Goal)
		volume.Summary = strings.TrimSpace(volume.Summary)
		volume.BudgetDecision = normalizePlannerBudgetDecision(volume.BudgetDecision)
		volume.BudgetReason = strings.TrimSpace(volume.BudgetReason)
		if volume.Title == "" {
			volume.Title = fmt.Sprintf("第 %d-%d 章", volume.TargetFrom, volume.TargetTo)
		}
		out = append(out, volume)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].TargetFrom == out[j].TargetFrom {
			return out[i].Index < out[j].Index
		}
		return out[i].TargetFrom < out[j].TargetFrom
	})
	for i := range out {
		if out[i].Index <= 0 {
			out[i].Index = i + 1
		}
	}
	return out
}

func adaptationVolumesCoverChapters(volumes []domain.AdaptationVolumePlan, chapterCount int) bool {
	if len(volumes) == 0 || chapterCount <= 0 {
		return false
	}
	next := 1
	for _, volume := range normalizeAdaptationProposalVolumes(volumes, chapterCount) {
		if volume.TargetFrom != next {
			return false
		}
		next = volume.TargetTo + 1
	}
	return next == chapterCount+1
}

func reportsForPlannerBatch(reports []domain.AdaptationSourceReport, batch plannerSkeletonBatch) []domain.AdaptationSourceReport {
	out := make([]domain.AdaptationSourceReport, 0, len(reports))
	for _, report := range reports {
		if report.Chapter >= batch.SourceFrom && report.Chapter <= batch.SourceTo {
			out = append(out, report)
		}
	}
	return out
}

func reportsForPlannerDetailBatch(reports []domain.AdaptationSourceReport, batch plannerSkeletonBatch) []domain.AdaptationSourceReport {
	parentReports := reportsForPlannerBatch(reports, batch)
	parentFrom := batch.DetailParentFrom
	parentTo := batch.DetailParentTo
	if parentFrom <= 0 || parentTo < parentFrom || batch.TargetFrom < parentFrom || batch.TargetTo > parentTo {
		return parentReports
	}
	parentTargetCount := parentTo - parentFrom + 1
	if parentTargetCount <= 0 || len(parentReports) <= 1 {
		return parentReports
	}
	startOffset := batch.TargetFrom - parentFrom
	endOffset := batch.TargetTo - parentFrom + 1
	start := startOffset * len(parentReports) / parentTargetCount
	end := ceilPositiveDiv(endOffset*len(parentReports), parentTargetCount)
	start = max(0, min(start, len(parentReports)-1))
	end = max(start+1, min(end, len(parentReports)))
	return append([]domain.AdaptationSourceReport(nil), parentReports[start:end]...)
}

func hasAnyRawKey(object map[string]json.RawMessage, keys ...string) bool {
	for _, key := range keys {
		if _, ok := object[key]; ok {
			return true
		}
	}
	return false
}

func firstJSONRaw(object map[string]json.RawMessage, keys ...string) json.RawMessage {
	for _, key := range keys {
		if raw := object[key]; len(raw) > 0 {
			return raw
		}
	}
	return nil
}

func firstJSONInt(object map[string]json.RawMessage, current int, keys ...string) int {
	if current > 0 {
		return current
	}
	for _, key := range keys {
		raw := object[key]
		if len(raw) == 0 {
			continue
		}
		var number int
		if err := json.Unmarshal(raw, &number); err == nil && number > 0 {
			return number
		}
		var floatNumber float64
		if err := json.Unmarshal(raw, &floatNumber); err == nil && floatNumber > 0 {
			return int(math.Round(floatNumber))
		}
		var text string
		if err := json.Unmarshal(raw, &text); err == nil {
			if parsed := parseFlexiblePositiveInt(text); parsed > 0 {
				return parsed
			}
		}
	}
	return current
}

func parseFlexiblePositiveInt(text string) int {
	text = strings.TrimSpace(text)
	if text == "" {
		return 0
	}
	if parsed, err := strconv.Atoi(text); err == nil && parsed > 0 {
		return parsed
	}
	var digits strings.Builder
	for _, r := range text {
		if r >= '0' && r <= '9' {
			digits.WriteRune(r)
			continue
		}
		if digits.Len() > 0 {
			break
		}
	}
	if digits.Len() > 0 {
		if parsed, err := strconv.Atoi(digits.String()); err == nil && parsed > 0 {
			return parsed
		}
	}
	var chinese strings.Builder
	for _, r := range text {
		if isChineseChapterNumberRune(r) {
			chinese.WriteRune(r)
			continue
		}
		if chinese.Len() > 0 {
			break
		}
	}
	if chinese.Len() > 0 {
		return parseChineseChapterNumber(chinese.String())
	}
	return 0
}

func isChineseChapterNumberRune(r rune) bool {
	switch r {
	case '一', '二', '两', '三', '四', '五', '六', '七', '八', '九', '十', '百':
		return true
	default:
		return false
	}
}

func firstJSONString(object map[string]json.RawMessage, current string, keys ...string) string {
	if strings.TrimSpace(current) != "" {
		return current
	}
	for _, key := range keys {
		raw := object[key]
		if len(raw) == 0 {
			continue
		}
		var value string
		if err := json.Unmarshal(raw, &value); err == nil {
			value = strings.TrimSpace(value)
			if value != "" {
				return value
			}
		}
	}
	return current
}

func firstJSONStringArray(object map[string]json.RawMessage, current []string, keys ...string) []string {
	if len(current) > 0 {
		return current
	}
	for _, key := range keys {
		raw := object[key]
		if len(raw) == 0 {
			continue
		}
		var values []string
		if err := json.Unmarshal(raw, &values); err == nil && len(values) > 0 {
			return values
		}
		var value string
		if err := json.Unmarshal(raw, &value); err == nil {
			value = strings.TrimSpace(value)
			if value != "" {
				return []string{value}
			}
		}
	}
	return current
}

func firstJSONIntArray(object map[string]json.RawMessage, current []int, keys ...string) []int {
	if len(current) > 0 {
		return current
	}
	for _, key := range keys {
		raw := object[key]
		if len(raw) == 0 {
			continue
		}
		var values []int
		if err := json.Unmarshal(raw, &values); err == nil && len(values) > 0 {
			return values
		}
		var value int
		if err := json.Unmarshal(raw, &value); err == nil && value > 0 {
			return []int{value}
		}
	}
	return current
}

func firstPositiveInt(values ...int) int {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}
	return 0
}

func minMaxPositive(values []int) (int, int) {
	minValue, maxValue := 0, 0
	for _, value := range values {
		if value <= 0 {
			continue
		}
		if minValue == 0 || value < minValue {
			minValue = value
		}
		if value > maxValue {
			maxValue = value
		}
	}
	return minValue, maxValue
}

type plannerUnusableOutputError struct {
	err error
}

func (e plannerUnusableOutputError) Error() string {
	return e.err.Error()
}

func (e plannerUnusableOutputError) Unwrap() error {
	return e.err
}

func buildAdaptationPlannerUserPrompt(
	opts ProposalOptions,
	reports []domain.AdaptationSourceReport,
	manifest *domain.AdaptationSourceManifest,
	sourceFoundation *domain.AdaptationSourceFoundation,
) (string, error) {
	input := struct {
		Brief               string                          `json:"brief"`
		Granularity         string                          `json:"granularity"`
		RewritePolicy       string                          `json:"rewrite_policy"`
		WordTolerance       float64                         `json:"word_tolerance"`
		ChapterBudgetPolicy *plannerChapterBudgetPolicy     `json:"chapter_budget_policy,omitempty"`
		SourceManifest      plannerSourceManifestSummary    `json:"source_manifest"`
		SourceFoundation    *plannerSourceFoundationSummary `json:"source_foundation"`
		SourceReports       []plannerSourceReportExcerpt    `json:"source_reports"`
		SourceEvents        []domain.AdaptationEvent        `json:"source_events,omitempty"`
		Requirements        []string                        `json:"requirements"`
	}{
		Brief:               opts.Brief,
		Granularity:         opts.Granularity,
		RewritePolicy:       opts.RewritePolicy,
		WordTolerance:       opts.WordTolerance,
		ChapterBudgetPolicy: plannerChapterBudgetPolicyForGranularity(opts.Granularity),
		SourceManifest:      plannerManifestSummary(manifest),
		SourceFoundation:    plannerSourceFoundationDigest(sourceFoundation),
		SourceReports:       plannerSourceReportExcerpts(reports),
		SourceEvents:        compactPlannerSourceEvents(sourceEventsFromReports(reports), 24),
		Requirements: []string{
			"Return exactly one JSON AdaptationPlan object and no prose.",
			"Do not wrap the JSON in markdown fences.",
			"The top-level JSON object must contain a chapters array and must not be a single chapter object.",
			"status must be proposal; rewrite_policy must be full_rewrite for arc/free.",
			"Target chapters must be numbered continuously from 1.",
			"Every chapter field must be an integer, not a string label like \"第1章\".",
			"Every target chapter must include legal source_chapters anchors within the analyzed source range.",
			"Added chapters must still include source_chapters anchors.",
			"Every chapter must include chapter, title, non-empty core_event, hook, scenes, source_chapters, source_range, word_budget, event_ids, added_event_ids, rule_ids, preserve_events, required_changes, and forbidden_moves.",
			"If chapter_budget_policy is present, long source chapters must be split into enough target chapters so no target chapter budget exceeds chapter_budget_policy.max_runes.",
		},
	}
	if opts.Granularity == domain.AdaptationGranularityArc {
		input.Requirements = append(input.Requirements,
			"Every source chapter must remain covered by at least one target chapter.",
			"Every required mainline source_events event_id must appear exactly once across chapter event_ids before added_event_ids receive story space.",
		)
	} else {
		input.Requirements = append(input.Requirements,
			"Ordinary source events may be omitted; build coherent target event_ids and do not force a strict source partition.",
			"Represent target causality with depends_on_event_ids; relationship changes with relationship {pair,from,to,allowed_from,requires_event_ids}; and changed setting facts with setting_claims.",
			"When the brief locks target-world facts or starting relationships, persist target_setting_locks and target_relationship_states at the plan level.",
		)
	}
	raw, err := json.MarshalIndent(input, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal planner input: %w", err)
	}
	return "Use the following analyzed source foundation and reports to plan the adaptation proposal.\n\n" +
		"Output contract: return exactly one JSON object, no prose and no markdown. The top-level object must contain a chapters array.\n" +
		"Required shape: {\"granularity\":\"...\",\"status\":\"proposal\",\"rewrite_policy\":\"...\",\"brief\":\"...\",\"chapters\":[{\"chapter\":1,\"title\":\"...\",\"core_event\":\"...\",\"hook\":\"...\",\"scenes\":[\"...\"],\"source_chapters\":[1],\"source_range\":{\"from\":1,\"to\":1},\"word_budget\":{\"source_runes\":1000,\"target_runes\":1500,\"min_runes\":1400,\"max_runes\":1600,\"tolerance\":0.15},\"event_ids\":[\"evt-...\"],\"added_event_ids\":[],\"depends_on_event_ids\":[],\"rule_ids\":[],\"preserve_events\":[\"...\"],\"required_changes\":[],\"forbidden_moves\":[]}]}.\n" +
		"Invalid shapes: {\"chapter\":1,...}; {\"summary\":\"...\"}; {\"key_turns\":[...]}; markdown text outside JSON.\n\n" +
		"Planning input:\n```json\n" +
		string(raw) + "\n```", nil
}

func parsePlannerProposal(text string) (domain.AdaptationPlan, error) {
	segments, err := extractPlannerJSONSegments(text)
	if err != nil {
		return domain.AdaptationPlan{}, fmt.Errorf("extract planner proposal JSON: %w", err)
	}
	var firstProposal domain.AdaptationPlan
	var firstShape string
	var firstErr error
	var looseChapters []domain.AdaptationChapterPlan
	for _, segment := range segments {
		proposal, err := decodePlannerProposalJSON([]byte(segment))
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if firstShape == "" {
			firstShape = plannerProposalShapeSummary([]byte(segment))
			firstProposal = proposal
		}
		if len(proposal.Chapters) > 0 {
			return proposal, nil
		}
		if chapter, ok := decodePlannerChapterJSON([]byte(segment)); ok {
			looseChapters = append(looseChapters, chapter)
		}
	}
	if len(looseChapters) > 0 {
		return domain.AdaptationPlan{Chapters: looseChapters}, nil
	}
	if firstErr != nil && firstShape == "" {
		return domain.AdaptationPlan{}, fmt.Errorf("parse planner proposal JSON: %w", firstErr)
	}
	if firstShape != "" {
		return firstProposal, fmt.Errorf("planner proposal has no chapters (%s)", firstShape)
	}
	return domain.AdaptationPlan{}, fmt.Errorf("planner proposal has no decodable JSON object")
}

func parsePlannerProposalStrict(text string) (domain.AdaptationPlan, error) {
	segments, err := extractPlannerJSONSegmentRanges(text)
	if err != nil {
		return domain.AdaptationPlan{}, fmt.Errorf("extract planner proposal JSON: %w", err)
	}
	segments = outermostPlannerJSONSegments(segments)
	switch len(segments) {
	case 0:
		return domain.AdaptationPlan{}, fmt.Errorf("extract planner proposal JSON: no complete JSON object found")
	case 1:
		return parsePlannerProposal(segments[0].text)
	default:
		return domain.AdaptationPlan{}, errPlannerProposalMultipleJSON
	}
}

var plannerEnvelopeKeys = []string{
	"proposal",
	"adaptation_proposal",
	"adaptationProposal",
	"adaptation_plan",
	"adaptationPlan",
	"plan",
	"result",
	"data",
	"output",
	"draft",
}

var plannerChapterAliasKeys = []string{
	"chapters",
	"chapter_plans",
	"chapterPlans",
	"target_chapters",
	"targetChapters",
	"target_chapter_plans",
	"targetChapterPlans",
	"planned_chapters",
	"plannedChapters",
	"adaptation_chapters",
	"adaptationChapters",
	"adapted_chapters",
	"adaptedChapters",
	"rewrite_chapters",
	"rewriteChapters",
	"chapter_outline",
	"chapterOutline",
	"chapter_outlines",
	"chapterOutlines",
	"outline_chapters",
	"outlineChapters",
	"planned_outline",
	"plannedOutline",
	"target_outline",
	"targetOutline",
	"chapter_proposals",
	"chapterProposals",
	"sections",
	"outline",
}

func decodePlannerProposalJSON(data []byte) (domain.AdaptationPlan, error) {
	var proposal domain.AdaptationPlan
	if err := json.Unmarshal(data, &proposal); err != nil {
		return proposal, err
	}
	fillPlannerChapterAliases(data, &proposal)
	if len(proposal.Chapters) > 0 {
		return proposal, nil
	}

	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(data, &envelope); err != nil {
		return proposal, nil
	}
	for _, key := range plannerEnvelopeKeys {
		raw := envelope[key]
		if len(raw) == 0 || raw[0] != '{' {
			continue
		}
		nested, err := decodePlannerProposalJSON(raw)
		if err == nil && len(nested.Chapters) > 0 {
			return nested, nil
		}
	}
	return proposal, nil
}

func decodePlannerChapterJSON(data []byte) (domain.AdaptationChapterPlan, bool) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(data, &object); err != nil || !plannerChapterObjectShape(object) {
		return domain.AdaptationChapterPlan{}, false
	}
	var chapter domain.AdaptationChapterPlan
	_ = json.Unmarshal(data, &chapter)
	fillPlannerSingleChapterAliases(object, &chapter)
	if chapter.Chapter <= 0 {
		return domain.AdaptationChapterPlan{}, false
	}
	if strings.TrimSpace(chapter.Title) == "" &&
		strings.TrimSpace(chapter.CoreEvent) == "" &&
		strings.TrimSpace(chapter.Hook) == "" &&
		len(chapter.Scenes) == 0 {
		return domain.AdaptationChapterPlan{}, false
	}
	return chapter, true
}

func plannerChapterObjectShape(object map[string]json.RawMessage) bool {
	if len(object) == 0 || !hasAnyRawKey(object, "chapter", "Chapter") {
		return false
	}
	return hasAnyRawKey(object,
		"title", "Title",
		"core_event", "coreEvent", "CoreEvent",
		"hook", "Hook",
		"scenes", "Scenes",
		"source_chapters", "sourceChapters", "SourceChapters",
		"source_range", "sourceRange", "SourceRange",
		"word_budget", "wordBudget", "WordBudget",
		"preserve_events", "preserveEvents", "PreserveEvents",
		"required_changes", "requiredChanges", "RequiredChanges",
		"forbidden_moves", "forbiddenMoves", "ForbiddenMoves",
	)
}

func fillPlannerSingleChapterAliases(object map[string]json.RawMessage, chapter *domain.AdaptationChapterPlan) {
	if chapter == nil {
		return
	}
	chapter.Chapter = firstJSONInt(object, chapter.Chapter, "chapter", "Chapter")
	chapter.Title = firstJSONString(object, chapter.Title, "title", "Title")
	chapter.CoreEvent = firstJSONString(object, chapter.CoreEvent, "core_event", "coreEvent", "CoreEvent")
	chapter.Hook = firstJSONString(object, chapter.Hook, "hook", "Hook")
	chapter.Scenes = firstJSONStringArray(object, chapter.Scenes, "scenes", "Scenes")
	chapter.SourceChapters = firstJSONIntArray(object, chapter.SourceChapters, "source_chapters", "sourceChapters", "SourceChapters")
	chapter.PreserveEvents = firstJSONStringArray(object, chapter.PreserveEvents, "preserve_events", "preserveEvents", "PreserveEvents")
	chapter.RequiredChanges = firstJSONStringArray(object, chapter.RequiredChanges, "required_changes", "requiredChanges", "RequiredChanges")
	chapter.ForbiddenMoves = firstJSONStringArray(object, chapter.ForbiddenMoves, "forbidden_moves", "forbiddenMoves", "ForbiddenMoves")
	if chapter.WordBudget == nil {
		if raw := firstJSONRaw(object, "word_budget", "wordBudget", "WordBudget"); len(raw) > 0 {
			var budget domain.AdaptationChapterWordBudget
			if err := json.Unmarshal(raw, &budget); err == nil {
				chapter.WordBudget = &budget
			}
		}
	}
	if chapter.SourceRange.From == 0 && chapter.SourceRange.To == 0 {
		if raw := firstJSONRaw(object, "source_range", "sourceRange", "SourceRange"); len(raw) > 0 {
			var sourceRange domain.SourceRange
			if err := json.Unmarshal(raw, &sourceRange); err == nil {
				chapter.SourceRange = sourceRange
			}
		}
	}
}

func fillPlannerChapterAliases(data []byte, proposal *domain.AdaptationPlan) {
	if proposal == nil || len(proposal.Chapters) > 0 {
		return
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(data, &object); err != nil {
		return
	}
	for _, key := range plannerChapterAliasKeys {
		raw := object[key]
		if len(raw) == 0 || raw[0] != '[' {
			continue
		}
		var chapters []domain.AdaptationChapterPlan
		if err := json.Unmarshal(raw, &chapters); err != nil {
			continue
		}
		if len(chapters) > 0 {
			proposal.Chapters = chapters
			return
		}
	}
}

func plannerProposalShapeSummary(data []byte) string {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(data, &object); err != nil {
		return "invalid object"
	}
	keys := sortedRawMessageKeys(object)
	parts := []string{"top-level keys: " + strings.Join(keys, ",")}
	for _, key := range plannerEnvelopeKeys {
		raw := object[key]
		if len(raw) == 0 || raw[0] != '{' {
			continue
		}
		var nested map[string]json.RawMessage
		if err := json.Unmarshal(raw, &nested); err == nil {
			parts = append(parts, key+" keys: "+strings.Join(sortedRawMessageKeys(nested), ","))
		}
	}
	return strings.Join(parts, "; ")
}

func sortedRawMessageKeys(object map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(object))
	for key := range object {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

type plannerJSONSegment struct {
	text  string
	start int
	end   int
}

func extractPlannerJSONSegments(text string) ([]string, error) {
	segments, err := extractPlannerJSONSegmentRanges(text)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(segments))
	for _, segment := range segments {
		out = append(out, segment.text)
	}
	return out, nil
}

func extractPlannerJSONSegmentRanges(text string) ([]plannerJSONSegment, error) {
	text = strings.TrimSpace(strings.TrimPrefix(strings.ToValidUTF8(text, "\uFFFD"), "\uFEFF"))
	var firstInvalid string
	var firstInvalidStart int
	var firstInvalidEnd int
	var segments []plannerJSONSegment
	for start := 0; start < len(text); start++ {
		if text[start] != '{' {
			continue
		}
		end, ok := scanPlannerJSONEnd(text[start:])
		if !ok {
			continue
		}
		candidate := strings.TrimSpace(text[start : start+end])
		if json.Valid([]byte(candidate)) {
			segments = append(segments, plannerJSONSegment{text: candidate, start: start, end: start + end})
			continue
		}
		if firstInvalid == "" {
			firstInvalid = candidate
			firstInvalidStart = start
			firstInvalidEnd = start + end
		}
	}
	if len(segments) > 0 {
		return segments, nil
	}
	if firstInvalid != "" {
		return []plannerJSONSegment{{text: firstInvalid, start: firstInvalidStart, end: firstInvalidEnd}}, nil
	}
	return nil, fmt.Errorf("no complete JSON object found")
}

func scanPlannerJSONEnd(s string) (int, bool) {
	stack := []byte{'}'}
	inString := false
	escaped := false
	for i := 1; i < len(s); i++ {
		c := s[i]
		if inString {
			if escaped {
				escaped = false
				continue
			}
			if c == '\\' {
				escaped = true
				continue
			}
			if c == '"' {
				inString = false
			}
			continue
		}
		switch c {
		case '"':
			inString = true
		case '{':
			stack = append(stack, '}')
		case '[':
			stack = append(stack, ']')
		case '}', ']':
			if len(stack) == 0 || c != stack[len(stack)-1] {
				return 0, false
			}
			stack = stack[:len(stack)-1]
			if len(stack) == 0 {
				return i + 1, true
			}
		}
	}
	return 0, false
}

func validatePlannerProposal(
	proposal *domain.AdaptationPlan,
	opts ProposalOptions,
	reports []domain.AdaptationSourceReport,
	manifest *domain.AdaptationSourceManifest,
	llm imp.LLMChat,
) error {
	if proposal == nil {
		return fmt.Errorf("planner proposal is nil")
	}
	if manifest == nil || manifest.ChapterCount <= 0 {
		return fmt.Errorf("source manifest missing")
	}
	fillMissingPlannerProposalConstants(proposal, opts)
	if strings.TrimSpace(proposal.Granularity) != opts.Granularity {
		return fmt.Errorf("planner granularity=%q, want %q", proposal.Granularity, opts.Granularity)
	}
	if strings.TrimSpace(proposal.Status) != domain.AdaptationPlanStatusProposal {
		return fmt.Errorf("planner status=%q, want proposal", proposal.Status)
	}
	if strings.TrimSpace(proposal.RewritePolicy) != opts.RewritePolicy {
		return fmt.Errorf("planner rewrite_policy=%q, want %q", proposal.RewritePolicy, opts.RewritePolicy)
	}
	if strings.TrimSpace(proposal.Brief) != opts.Brief {
		return fmt.Errorf("planner brief does not match requested brief")
	}
	if len(proposal.Chapters) == 0 {
		return fmt.Errorf("planner proposal has no chapters")
	}
	if err := ValidateProposalOutlineUniqueness(*proposal); err != nil {
		return err
	}
	opts.WordTolerance = normalizeProposalWordTolerance(opts.Granularity, opts.WordTolerance)

	sourceRunesByChapter := sourceRunesByChapter(manifest)
	covered := make(map[int]bool, manifest.ChapterCount)
	sourceTotalRunes := 0
	for _, report := range reports {
		sourceTotalRunes += sourceRunesForReport(report, sourceRunesByChapter)
	}

	for i := range proposal.Chapters {
		chapter := &proposal.Chapters[i]
		sourceRangeExplicit := chapter.SourceRange.From > 0 || chapter.SourceRange.To > 0
		if chapter.Chapter != i+1 {
			return fmt.Errorf("planner target chapters must be continuous: got chapter %d at index %d", chapter.Chapter, i)
		}
		if strings.TrimSpace(chapter.Title) == "" {
			return fmt.Errorf("planner chapter %d title is empty", chapter.Chapter)
		}
		if strings.TrimSpace(chapter.CoreEvent) == "" {
			return fmt.Errorf("planner chapter %d core_event is empty", chapter.Chapter)
		}
		if strings.TrimSpace(chapter.Hook) == "" {
			return fmt.Errorf("planner chapter %d hook is empty", chapter.Chapter)
		}
		if len(trimmedNonEmpty(chapter.Scenes)) == 0 {
			return fmt.Errorf("planner chapter %d scenes are empty", chapter.Chapter)
		}
		chapter.Scenes = trimmedNonEmpty(chapter.Scenes)
		if err := normalizePlannerChapterSourceCoverage(chapter, manifest.ChapterCount, nil, false); err != nil {
			return err
		}
		for _, sourceChapter := range chapter.SourceChapters {
			covered[sourceChapter] = true
		}
		if sourceRangeExplicit {
			chapter.SourceChapters = expandSourceChaptersForRange(chapter.SourceChapters, chapter.SourceRange.From, chapter.SourceRange.To)
			for sourceChapter := chapter.SourceRange.From; sourceChapter <= chapter.SourceRange.To; sourceChapter++ {
				covered[sourceChapter] = true
			}
		}
		fillPlannerChapterWordBudgetDefaults(chapter, sourceRunesByChapter, opts.WordTolerance)
		if err := validatePlannerWordBudget(chapter, opts.WordTolerance); err != nil {
			return err
		}
	}
	if opts.Granularity != domain.AdaptationGranularityFree {
		for sourceChapter := 1; sourceChapter <= manifest.ChapterCount; sourceChapter++ {
			if !covered[sourceChapter] {
				return fmt.Errorf("planner proposal does not cover source chapter %d", sourceChapter)
			}
		}
	}
	acceptedBudgetRanges := plannerAcceptedBudgetDeviationRanges(normalizeAdaptationProposalVolumes(proposal.Volumes, len(proposal.Chapters)))
	budgetNormalized, err := normalizePlannerProposalChapterBudgets(proposal.Chapters, opts, sourceRunesByChapter, acceptedBudgetRanges)
	if err != nil {
		return err
	}
	if budgetNormalized {
		proposal.TargetTotalRunes = 0
		proposal.TargetMinRunes = 0
		proposal.TargetMaxRunes = 0
	}
	targetTotalRunes := 0
	targetMinRunes := 0
	targetMaxRunes := 0
	for i := range proposal.Chapters {
		chapter := &proposal.Chapters[i]
		if err := validatePlannerWordBudget(chapter, opts.WordTolerance); err != nil {
			return err
		}
		targetTotalRunes += chapter.WordBudget.TargetRunes
		targetMinRunes += chapter.WordBudget.MinRunes
		targetMaxRunes += chapter.WordBudget.MaxRunes
	}
	if err := validatePlannerProposalTotal("target_total_runes", proposal.TargetTotalRunes, targetTotalRunes); err != nil {
		return err
	}
	if err := validatePlannerProposalTotal("target_min_runes", proposal.TargetMinRunes, targetMinRunes); err != nil {
		return err
	}
	if err := validatePlannerProposalTotal("target_max_runes", proposal.TargetMaxRunes, targetMaxRunes); err != nil {
		return err
	}

	proposal.Status = domain.AdaptationPlanStatusProposal
	proposal.Granularity = opts.Granularity
	proposal.RewritePolicy = domain.AdaptationRewriteFullRewrite
	proposal.Brief = opts.Brief
	proposal.WordTolerance = opts.WordTolerance
	proposal.SourceTotalRunes = sourceTotalRunes
	proposal.TargetTotalRunes = targetTotalRunes
	proposal.TargetMinRunes = targetMinRunes
	proposal.TargetMaxRunes = targetMaxRunes
	proposal.Volumes = normalizeAdaptationProposalVolumes(proposal.Volumes, len(proposal.Chapters))
	if proposal.Planner == nil {
		proposal.Planner = &domain.AdaptationPlannerMeta{}
	}
	if strings.TrimSpace(proposal.Planner.Prompt) == "" {
		proposal.Planner.Prompt = adaptationPlannerPromptName
	}
	if strings.TrimSpace(proposal.Planner.PromptVersion) == "" {
		proposal.Planner.PromptVersion = adaptationPlannerPromptVersion
	}
	if strings.TrimSpace(proposal.Planner.GeneratedAt) == "" {
		proposal.Planner.GeneratedAt = time.Now().UTC().Format(time.RFC3339)
	}
	if strings.TrimSpace(proposal.Planner.Model) == "" {
		if namer, ok := llm.(interface{ ModelName() string }); ok {
			proposal.Planner.Model = namer.ModelName()
		}
	}
	if err := finalizePlannerEventContracts(proposal, opts, reports); err != nil {
		return err
	}
	return ValidateAdaptationOutlineQuality(proposal, manifest)
}

type plannerChapterBudgetGroup struct {
	Indexes     []int
	SourceFrom  int
	SourceTo    int
	SourceRunes int
}

type plannerProposalBudgetSplitError struct {
	FirstChapter int
	SourceFrom   int
	SourceTo     int
	SourceRunes  int
	MinChapters  int
}

func (e *plannerProposalBudgetSplitError) Error() string {
	if e == nil {
		return "planner source range needs more target chapters before assigning word_budget"
	}
	return fmt.Sprintf("planner chapter %d source_range %d-%d has %d source_runes; split this source range into at least %d target chapters before assigning word_budget",
		e.FirstChapter, e.SourceFrom, e.SourceTo, e.SourceRunes, e.MinChapters)
}

type plannerProposalBudgetSplitErrors []plannerProposalBudgetSplitError

func (e plannerProposalBudgetSplitErrors) Error() string {
	if len(e) == 0 {
		return "planner source ranges need more target chapters before assigning word_budget"
	}
	if len(e) == 1 {
		return (&e[0]).Error()
	}
	return fmt.Sprintf("%s; %d more source ranges need budget splits", (&e[0]).Error(), len(e)-1)
}

func (e plannerProposalBudgetSplitErrors) Unwrap() []error {
	errs := make([]error, 0, len(e))
	for idx := range e {
		errs = append(errs, &e[idx])
	}
	return errs
}

func formatPlannerBudgetSplitRanges(errs plannerProposalBudgetSplitErrors) string {
	parts := make([]string, 0, min(len(errs), 4))
	for idx, budgetErr := range errs {
		if idx >= 4 {
			parts = append(parts, fmt.Sprintf("+%d more", len(errs)-idx))
			break
		}
		parts = append(parts, fmt.Sprintf("%d-%d", budgetErr.SourceFrom, budgetErr.SourceTo))
	}
	return strings.Join(parts, ", ")
}

func normalizePlannerProposalChapterBudgets(chapters []domain.AdaptationChapterPlan, opts ProposalOptions, sourceRunesByChapter map[int]int, acceptedBudgetRanges []domain.SourceRange) (bool, error) {
	policy := plannerChapterBudgetPolicyForGranularity(opts.Granularity)
	if policy == nil {
		return false, nil
	}
	normalized := false
	groups := plannerChapterBudgetGroups(chapters, sourceRunesByChapter)
	if plannerEnforcesSourceRuneSplitting(opts.Granularity) {
		var splitErrs plannerProposalBudgetSplitErrors
		for _, group := range groups {
			if plannerBudgetGroupAcceptedByRange(group, acceptedBudgetRanges) {
				continue
			}
			if err := plannerBudgetGroupSplitError(chapters, group, *policy); err != nil {
				var splitErr *plannerProposalBudgetSplitError
				if errors.As(err, &splitErr) && splitErr != nil {
					splitErrs = append(splitErrs, *splitErr)
					continue
				}
				return false, err
			}
		}
		if len(splitErrs) > 0 {
			sortPlannerProposalBudgetSplitErrors(splitErrs)
			return false, splitErrs
		}
	}
	for _, group := range groups {
		if len(group.Indexes) == 0 || !plannerBudgetGroupNeedsNormalization(chapters, group, *policy) {
			continue
		}
		applyPlannerBudgetGroup(chapters, group, *policy)
		normalized = true
	}
	return normalized, nil
}

func plannerAcceptedBudgetDeviationRanges(volumes []domain.AdaptationVolumePlan) []domain.SourceRange {
	ranges := make([]domain.SourceRange, 0, len(volumes))
	for _, volume := range volumes {
		if !plannerVolumeBudgetDeviationAccepted(volume) {
			continue
		}
		if volume.SourceFrom <= 0 || volume.SourceTo < volume.SourceFrom {
			continue
		}
		ranges = append(ranges, domain.SourceRange{From: volume.SourceFrom, To: volume.SourceTo})
	}
	return ranges
}

func plannerBudgetGroupAcceptedByRange(group plannerChapterBudgetGroup, acceptedRanges []domain.SourceRange) bool {
	if group.SourceFrom <= 0 || group.SourceTo < group.SourceFrom {
		return false
	}
	for _, acceptedRange := range acceptedRanges {
		if acceptedRange.From <= 0 || acceptedRange.To < acceptedRange.From {
			continue
		}
		if group.SourceFrom >= acceptedRange.From && group.SourceTo <= acceptedRange.To {
			return true
		}
	}
	return false
}

func validatePlannerBatchChapterBudgetGroups(chapters []domain.AdaptationChapterPlan, previousChapters []domain.AdaptationChapterPlan, opts ProposalOptions, sourceRunesByChapter map[int]int, batch plannerSkeletonBatch) error {
	policy := plannerChapterBudgetPolicyForGranularity(opts.Granularity)
	if policy == nil || !plannerEnforcesSourceRuneSplitting(opts.Granularity) {
		return nil
	}
	if !plannerDetailBatchClosesParent(batch) {
		return nil
	}
	parentChapters := plannerDetailParentChapters(previousChapters, chapters, batch)
	if plannerUsesSharedBatchSourceRange(opts, batch) {
		if plannerBudgetDeviationAccepted(batch) {
			return nil
		}
		if group := plannerParentBatchBudgetGroup(parentChapters, batch, sourceRunesByChapter); len(group.Indexes) > 0 {
			if err := plannerBudgetGroupSplitError(parentChapters, group, *policy); err != nil {
				return err
			}
		}
		return nil
	}
	groups := plannerChapterBudgetGroups(parentChapters, sourceRunesByChapter)
	var splitErrs plannerProposalBudgetSplitErrors
	for _, group := range groups {
		if err := plannerBudgetGroupSplitError(parentChapters, group, *policy); err != nil {
			var splitErr *plannerProposalBudgetSplitError
			if errors.As(err, &splitErr) && splitErr != nil {
				splitErrs = append(splitErrs, *splitErr)
				continue
			}
			return err
		}
	}
	if len(splitErrs) > 0 {
		sortPlannerProposalBudgetSplitErrors(splitErrs)
		return splitErrs
	}
	return nil
}

func plannerParentBatchBudgetGroup(chapters []domain.AdaptationChapterPlan, batch plannerSkeletonBatch, sourceRunesByChapter map[int]int) plannerChapterBudgetGroup {
	group := plannerChapterBudgetGroup{
		SourceFrom:  batch.SourceFrom,
		SourceTo:    batch.SourceTo,
		SourceRunes: sourceRunesForRange(sourceRunesByChapter, batch.SourceFrom, batch.SourceTo),
	}
	parentFrom, parentTo := plannerDetailParentTargetRange(batch)
	for index := range chapters {
		chapter := chapters[index]
		if parentFrom > 0 && parentTo >= parentFrom && (chapter.Chapter < parentFrom || chapter.Chapter > parentTo) {
			continue
		}
		group.Indexes = append(group.Indexes, index)
	}
	return group
}

func plannerBudgetGroupSplitError(chapters []domain.AdaptationChapterPlan, group plannerChapterBudgetGroup, policy plannerChapterBudgetPolicy) error {
	reviewCapacityRunes := plannerBudgetPolicySourceReviewCapacityRunes(policy)
	if len(group.Indexes) == 0 || group.SourceRunes <= reviewCapacityRunes {
		return nil
	}
	minChapters := ceilPositiveDiv(group.SourceRunes, reviewCapacityRunes)
	if minChapters <= len(group.Indexes) {
		return nil
	}
	first := chapters[group.Indexes[0]].Chapter
	return &plannerProposalBudgetSplitError{
		FirstChapter: first,
		SourceFrom:   group.SourceFrom,
		SourceTo:     group.SourceTo,
		SourceRunes:  group.SourceRunes,
		MinChapters:  minChapters,
	}
}

func sortPlannerProposalBudgetSplitErrors(splitErrs plannerProposalBudgetSplitErrors) {
	sort.SliceStable(splitErrs, func(i, j int) bool {
		if splitErrs[i].FirstChapter == splitErrs[j].FirstChapter {
			if splitErrs[i].SourceFrom == splitErrs[j].SourceFrom {
				return splitErrs[i].SourceTo < splitErrs[j].SourceTo
			}
			return splitErrs[i].SourceFrom < splitErrs[j].SourceFrom
		}
		return splitErrs[i].FirstChapter < splitErrs[j].FirstChapter
	})
}

func plannerDetailBatchClosesParent(batch plannerSkeletonBatch) bool {
	_, parentTo := plannerDetailParentTargetRange(batch)
	return parentTo <= 0 || batch.TargetTo >= parentTo
}

func plannerDetailParentTargetRange(batch plannerSkeletonBatch) (int, int) {
	if batch.DetailParentFrom > 0 && batch.DetailParentTo >= batch.DetailParentFrom {
		return batch.DetailParentFrom, batch.DetailParentTo
	}
	return batch.TargetFrom, batch.TargetTo
}

func plannerDetailParentChapters(previousChapters []domain.AdaptationChapterPlan, currentChapters []domain.AdaptationChapterPlan, batch plannerSkeletonBatch) []domain.AdaptationChapterPlan {
	parentFrom, parentTo := plannerDetailParentTargetRange(batch)
	out := make([]domain.AdaptationChapterPlan, 0, len(previousChapters)+len(currentChapters))
	for _, chapter := range previousChapters {
		if parentFrom > 0 && parentTo >= parentFrom && (chapter.Chapter < parentFrom || chapter.Chapter > parentTo) {
			continue
		}
		out = append(out, chapter)
	}
	out = append(out, currentChapters...)
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].Chapter < out[j].Chapter
	})
	return out
}

func plannerChapterBudgetGroups(chapters []domain.AdaptationChapterPlan, sourceRunesByChapter map[int]int) map[string]plannerChapterBudgetGroup {
	groups := make(map[string]plannerChapterBudgetGroup)
	for index := range chapters {
		chapter := &chapters[index]
		key, from, to := plannerChapterBudgetGroupKey(*chapter)
		group := groups[key]
		group.Indexes = append(group.Indexes, index)
		if group.SourceFrom == 0 || from < group.SourceFrom {
			group.SourceFrom = from
		}
		if to > group.SourceTo {
			group.SourceTo = to
		}
		if group.SourceRunes <= 0 {
			group.SourceRunes = sourceRunesForRange(sourceRunesByChapter, from, to)
		}
		groups[key] = group
	}
	return groups
}

func plannerChapterBudgetGroupKey(chapter domain.AdaptationChapterPlan) (string, int, int) {
	if chapter.SourceRange.From > 0 && chapter.SourceRange.To >= chapter.SourceRange.From {
		return fmt.Sprintf("range:%d:%d", chapter.SourceRange.From, chapter.SourceRange.To), chapter.SourceRange.From, chapter.SourceRange.To
	}
	from, to := minMaxPositive(chapter.SourceChapters)
	if from > 0 && to >= from {
		return fmt.Sprintf("anchors:%d:%d:%s", from, to, intListKey(chapter.SourceChapters)), from, to
	}
	return fmt.Sprintf("chapter:%d", chapter.Chapter), 0, 0
}

func intListKey(values []int) string {
	clean := appendSortedUniqueInts(nil, values...)
	parts := make([]string, 0, len(clean))
	for _, value := range clean {
		parts = append(parts, strconv.Itoa(value))
	}
	return strings.Join(parts, ",")
}

func appendSortedUniqueInts(base []int, values ...int) []int {
	seen := make(map[int]bool, len(base)+len(values))
	out := make([]int, 0, len(base)+len(values))
	for _, value := range append(base, values...) {
		if value <= 0 || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	sort.Ints(out)
	return out
}

func plannerBudgetGroupNeedsNormalization(chapters []domain.AdaptationChapterPlan, group plannerChapterBudgetGroup, policy plannerChapterBudgetPolicy) bool {
	for _, index := range group.Indexes {
		chapter := chapters[index]
		if chapter.TargetRunes > policy.MaxRunes || chapter.TargetMaxRunes > policy.MaxRunes {
			return true
		}
		if chapter.WordBudget != nil &&
			(chapter.WordBudget.TargetRunes > policy.MaxRunes ||
				chapter.WordBudget.MaxRunes > policy.MaxRunes ||
				(len(group.Indexes) > 1 && chapter.WordBudget.SourceRunes > policy.MaxRunes)) {
			return true
		}
	}
	return false
}

func applyPlannerBudgetGroup(chapters []domain.AdaptationChapterPlan, group plannerChapterBudgetGroup, policy plannerChapterBudgetPolicy) {
	count := len(group.Indexes)
	if count == 0 {
		return
	}
	totalRunes := group.SourceRunes
	if totalRunes <= 0 {
		totalRunes = policy.TargetRunes * count
	}
	for offset, index := range group.Indexes {
		sourceRunes := splitRunesForIndex(totalRunes, count, offset)
		targetRunes := sourceRunes
		if targetRunes <= 0 {
			targetRunes = policy.TargetRunes
		}
		if targetRunes > policy.MaxRunes {
			targetRunes = policy.MaxRunes
		}
		minRunes, maxRunes := modelChapterBudgetRange(targetRunes, policy)
		chapter := &chapters[index]
		chapter.SourceRunes = sourceRunes
		chapter.TargetRunes = targetRunes
		chapter.TargetMinRunes = minRunes
		chapter.TargetMaxRunes = maxRunes
		chapter.WordBudget = &domain.AdaptationChapterWordBudget{
			SourceRunes: sourceRunes,
			TargetRunes: targetRunes,
			MinRunes:    minRunes,
			MaxRunes:    maxRunes,
			Tolerance:   policy.Tolerance,
		}
	}
}

func splitRunesForIndex(totalRunes, count, index int) int {
	if totalRunes <= 0 || count <= 0 || index < 0 {
		return 0
	}
	base := totalRunes / count
	remainder := totalRunes % count
	if index < remainder {
		return base + 1
	}
	return base
}

func modelChapterBudgetRange(targetRunes int, policy plannerChapterBudgetPolicy) (int, int) {
	minRunes, maxRunes := runeRange(targetRunes, policy.Tolerance)
	if maxRunes > policy.MaxRunes {
		maxRunes = policy.MaxRunes
	}
	if minRunes > targetRunes {
		minRunes = targetRunes
	}
	if maxRunes < targetRunes {
		maxRunes = targetRunes
	}
	if minRunes <= 0 {
		minRunes = targetRunes
	}
	if maxRunes <= 0 {
		maxRunes = targetRunes
	}
	return minRunes, maxRunes
}

func validatePlannerWordBudget(chapter *domain.AdaptationChapterPlan, tolerance float64) error {
	if chapter.WordBudget == nil {
		return fmt.Errorf("planner chapter %d word_budget is missing", chapter.Chapter)
	}
	if chapter.WordBudget.TargetRunes <= 0 {
		return fmt.Errorf("planner chapter %d word_budget.target_runes must be > 0", chapter.Chapter)
	}
	if chapter.WordBudget.MinRunes <= 0 {
		return fmt.Errorf("planner chapter %d word_budget.min_runes must be > 0", chapter.Chapter)
	}
	if chapter.WordBudget.MaxRunes < chapter.WordBudget.MinRunes {
		return fmt.Errorf("planner chapter %d word_budget max < min", chapter.Chapter)
	}
	if chapter.WordBudget.TargetRunes < chapter.WordBudget.MinRunes || chapter.WordBudget.TargetRunes > chapter.WordBudget.MaxRunes {
		return fmt.Errorf("planner chapter %d word_budget.target_runes must be within min_runes..max_runes", chapter.Chapter)
	}
	if chapter.WordBudget.Tolerance <= 0 {
		chapter.WordBudget.Tolerance = tolerance
	}
	if chapter.SourceRunes <= 0 {
		chapter.SourceRunes = chapter.WordBudget.SourceRunes
	}
	if err := validatePlannerChapterBudgetField(chapter.Chapter, "target_runes", &chapter.TargetRunes, "word_budget.target_runes", chapter.WordBudget.TargetRunes); err != nil {
		return err
	}
	if err := validatePlannerChapterBudgetField(chapter.Chapter, "target_min_runes", &chapter.TargetMinRunes, "word_budget.min_runes", chapter.WordBudget.MinRunes); err != nil {
		return err
	}
	if err := validatePlannerChapterBudgetField(chapter.Chapter, "target_max_runes", &chapter.TargetMaxRunes, "word_budget.max_runes", chapter.WordBudget.MaxRunes); err != nil {
		return err
	}
	return nil
}

func fillPlannerChapterWordBudgetDefaults(chapter *domain.AdaptationChapterPlan, sourceRunesByChapter map[int]int, tolerance float64) {
	if chapter == nil {
		return
	}
	sourceRunes := chapter.SourceRunes
	if sourceRunes <= 0 && chapter.WordBudget != nil {
		sourceRunes = chapter.WordBudget.SourceRunes
	}
	if sourceRunes <= 0 {
		sourceRunes = sourceRunesForChapterAnchors(chapter, sourceRunesByChapter)
	}
	targetRunes := chapter.TargetRunes
	if targetRunes <= 0 && chapter.WordBudget != nil {
		targetRunes = chapter.WordBudget.TargetRunes
	}
	if targetRunes <= 0 {
		targetRunes = sourceRunes
	}
	minRunes := chapter.TargetMinRunes
	if minRunes <= 0 && chapter.WordBudget != nil {
		minRunes = chapter.WordBudget.MinRunes
	}
	maxRunes := chapter.TargetMaxRunes
	if maxRunes <= 0 && chapter.WordBudget != nil {
		maxRunes = chapter.WordBudget.MaxRunes
	}
	if minRunes <= 0 || maxRunes <= 0 {
		if tolerance > 0 {
			defaultMin, defaultMax := runeRange(targetRunes, tolerance)
			if minRunes <= 0 {
				minRunes = defaultMin
			}
			if maxRunes <= 0 {
				maxRunes = defaultMax
			}
		} else {
			if minRunes <= 0 {
				minRunes = targetRunes
			}
			if maxRunes <= 0 {
				maxRunes = targetRunes
			}
		}
	}
	if sourceRunes <= 0 || targetRunes <= 0 || minRunes <= 0 || maxRunes <= 0 {
		return
	}
	if chapter.WordBudget == nil {
		chapter.WordBudget = &domain.AdaptationChapterWordBudget{}
	}
	if chapter.WordBudget.SourceRunes <= 0 {
		chapter.WordBudget.SourceRunes = sourceRunes
	}
	if chapter.WordBudget.TargetRunes <= 0 {
		chapter.WordBudget.TargetRunes = targetRunes
	}
	if chapter.WordBudget.MinRunes <= 0 {
		chapter.WordBudget.MinRunes = minRunes
	}
	if chapter.WordBudget.MaxRunes <= 0 {
		chapter.WordBudget.MaxRunes = maxRunes
	}
	if chapter.SourceRunes <= 0 {
		chapter.SourceRunes = sourceRunes
	}
}

func expandSourceChaptersForRange(chapters []int, from, to int) []int {
	if from <= 0 || to < from {
		return append([]int(nil), chapters...)
	}
	seen := make(map[int]bool, len(chapters)+to-from+1)
	out := make([]int, 0, len(chapters)+to-from+1)
	for _, chapter := range chapters {
		if chapter <= 0 || seen[chapter] {
			continue
		}
		seen[chapter] = true
		out = append(out, chapter)
	}
	for chapter := from; chapter <= to; chapter++ {
		if seen[chapter] {
			continue
		}
		seen[chapter] = true
		out = append(out, chapter)
	}
	sort.Ints(out)
	return out
}

func sourceRunesForChapterAnchors(chapter *domain.AdaptationChapterPlan, sourceRunesByChapter map[int]int) int {
	if chapter == nil || len(sourceRunesByChapter) == 0 {
		return 0
	}
	total := 0
	if chapter.SourceRange.From > 0 && chapter.SourceRange.To >= chapter.SourceRange.From {
		total = sourceRunesForRange(sourceRunesByChapter, chapter.SourceRange.From, chapter.SourceRange.To)
		if total > 0 {
			return total
		}
	}
	seen := map[int]bool{}
	for _, sourceChapter := range chapter.SourceChapters {
		if sourceChapter <= 0 || seen[sourceChapter] {
			continue
		}
		seen[sourceChapter] = true
		total += sourceRunesByChapter[sourceChapter]
	}
	if total > 0 {
		return total
	}
	return 0
}

func sourceRunesForRange(sourceRunesByChapter map[int]int, from, to int) int {
	if len(sourceRunesByChapter) == 0 || from <= 0 || to < from {
		return 0
	}
	total := 0
	for sourceChapter := from; sourceChapter <= to; sourceChapter++ {
		total += sourceRunesByChapter[sourceChapter]
	}
	return total
}

func validatePlannerChapterBudgetField(chapter int, field string, legacy *int, nestedField string, nestedValue int) error {
	if legacy == nil {
		return nil
	}
	if *legacy > 0 {
		if *legacy != nestedValue {
			return fmt.Errorf("planner chapter %d %s=%d conflicts with %s=%d", chapter, field, *legacy, nestedField, nestedValue)
		}
		return nil
	}
	*legacy = nestedValue
	return nil
}

func fillMissingPlannerProposalConstants(proposal *domain.AdaptationPlan, opts ProposalOptions) {
	if proposal == nil {
		return
	}
	if strings.TrimSpace(proposal.Granularity) == "" {
		proposal.Granularity = opts.Granularity
	}
	if strings.TrimSpace(proposal.Status) == "" {
		proposal.Status = domain.AdaptationPlanStatusProposal
	}
	if strings.TrimSpace(proposal.RewritePolicy) == "" {
		proposal.RewritePolicy = opts.RewritePolicy
	}
	proposal.Brief = opts.Brief
}

func normalizeProposalWordTolerance(granularity string, wordTolerance float64) float64 {
	if domain.AdaptationRewritePolicyForGranularity(granularity) != domain.AdaptationRewritePreserveDetails {
		return 0
	}
	if wordTolerance <= 0 {
		return DefaultWordTolerance
	}
	return wordTolerance
}

func validatePlannerProposalTotal(field string, provided int, derived int) error {
	if provided > 0 && provided != derived {
		return fmt.Errorf("planner %s=%d conflicts with derived chapter total %d", field, provided, derived)
	}
	return nil
}

func trimmedNonEmpty(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			out = append(out, value)
		}
	}
	return out
}

func ValidatePreparedSource(st *store.Store, sourcePath string) (*domain.AdaptationSourceManifest, []domain.AdaptationSourceReport, error) {
	if st == nil {
		return nil, nil, fmt.Errorf("store is required")
	}
	manifest, err := st.Adaptation.LoadSourceManifest()
	if err != nil {
		return nil, nil, fmt.Errorf("load source manifest: %w", err)
	}
	if manifest == nil || manifest.ChapterCount <= 0 || len(manifest.Chapters) != manifest.ChapterCount {
		return nil, nil, fmt.Errorf("source manifest missing or incomplete; analyze source first")
	}
	reports, err := st.Adaptation.LoadCompleteSourceReports()
	if err != nil {
		return nil, nil, fmt.Errorf("load source reports: %w", err)
	}
	if len(reports) != manifest.ChapterCount {
		return nil, nil, fmt.Errorf("source reports incomplete or stale; analyze source first")
	}
	if sourcePath = strings.TrimSpace(sourcePath); sourcePath != "" {
		absPath, err := filepath.Abs(sourcePath)
		if err == nil {
			sourcePath = absPath
		}
		if !sameSourcePath(manifest.SourcePath, sourcePath) {
			chapters, err := imp.SplitFile(sourcePath)
			if err != nil {
				return nil, nil, fmt.Errorf("split selected adaptation source: %w", err)
			}
			next := buildSourceManifest(sourcePath, chapters)
			if !sourceManifestMatches(manifest, next) {
				return nil, nil, fmt.Errorf("selected adaptation source has not been analyzed; run adaptation source analysis first")
			}
		}
	}
	foundation, err := st.Adaptation.LoadSourceFoundation()
	if err != nil {
		return nil, nil, fmt.Errorf("load source foundation: %w", err)
	}
	if foundation == nil {
		return nil, nil, fmt.Errorf("source foundation missing; analyze source first")
	}
	return manifest, reports, nil
}

func ConfirmAdaptationProposal(ctx context.Context, deps Deps, proposal domain.AdaptationPlan) (*domain.AdaptationPlan, error) {
	if deps.Store == nil {
		return nil, fmt.Errorf("store is required")
	}
	if strings.TrimSpace(proposal.Brief) == "" {
		return nil, fmt.Errorf("adaptation proposal brief is required")
	}
	if len(proposal.Chapters) == 0 {
		return nil, fmt.Errorf("adaptation proposal has no chapters")
	}
	if err := ValidateProposalOutlineUniqueness(proposal); err != nil {
		return nil, err
	}
	targetFoundation, err := deps.Store.Foundation.Load()
	if err != nil {
		return nil, fmt.Errorf("load confirmed target foundation: %w", err)
	}

	proposal.Status = domain.AdaptationPlanStatusConfirmed
	proposal.Granularity = domain.NormalizeAdaptationGranularity(proposal.Granularity)
	proposal.RewritePolicy = domain.AdaptationRewritePolicyForGranularity(proposal.Granularity)
	manifest, err := deps.Store.Adaptation.LoadSourceManifest()
	if err != nil {
		return nil, fmt.Errorf("load source manifest for outline quality gate: %w", err)
	}
	if err := ValidateAdaptationOutlineQuality(&proposal, manifest); err != nil {
		return nil, err
	}
	if _, err := deps.Store.RequireConfirmedAdaptationFoundation(); err != nil {
		return nil, fmt.Errorf("adaptation target foundation gate: %w", err)
	}
	if err := deps.Store.ValidateAdaptationArtifactBinding(proposal.FoundationBinding); err != nil {
		return nil, fmt.Errorf("adaptation proposal binding: %w", err)
	}
	domain.MarkAdaptationOutlineQualityPassed(&proposal)
	fr := &imp.FoundationResult{
		Premise: targetFoundation.Premise, Characters: targetFoundation.Characters,
		WorldRules: targetFoundation.WorldRules,
	}
	fr.Volumes = adaptationTargetVolumes(proposal)
	if err := validateAdaptationGeneratedParentBatches(fr.Volumes); err != nil {
		return nil, err
	}
	if fr.Compass == nil {
		fr.Compass = &domain.StoryCompass{
			EndingDirection: strings.TrimSpace(proposal.Brief),
			EstimatedScale:  fmt.Sprintf("%d chapters", len(proposal.Chapters)),
		}
	}
	if err := imp.PersistAdaptationOutline(ctx, deps.Store, planningTier(len(proposal.Chapters)), fr); err != nil {
		return nil, fmt.Errorf("persist adaptation outline from confirmed target foundation: %w", err)
	}
	if deps.ConfirmationFailpoint != nil {
		if err := deps.ConfirmationFailpoint("after_foundation"); err != nil {
			return nil, err
		}
	}
	if err := deps.Store.Adaptation.SavePlan(proposal); err != nil {
		return nil, fmt.Errorf("save adaptation plan: %w", err)
	}
	if err := deps.Store.Adaptation.ClearProposal(); err != nil {
		return nil, fmt.Errorf("clear adaptation proposal: %w", err)
	}
	return &proposal, nil
}

func ValidateProposalOutlineUniqueness(proposal domain.AdaptationPlan) error {
	if len(proposal.Chapters) == 0 {
		return nil
	}
	volumes := normalizeAdaptationProposalVolumes(proposal.Volumes, len(proposal.Chapters))
	if adaptationVolumesCoverChapters(volumes, len(proposal.Chapters)) {
		for _, volume := range volumes {
			chapters := adaptationChapterPlansInTargetRange(proposal.Chapters, volume.TargetFrom, volume.TargetTo)
			if duplicate, ok := domain.FindDuplicateAdaptationChapterOutline(chapters); ok {
				return fmt.Errorf("adaptation proposal volume %d parent batch contains duplicate chapter outline: %w", volume.Index, duplicate)
			}
		}
		return nil
	}
	if duplicate, ok := domain.FindDuplicateAdaptationChapterOutline(proposal.Chapters); ok {
		return fmt.Errorf("adaptation proposal parent batch contains duplicate chapter outline: %w", duplicate)
	}
	return nil
}

func adaptationChapterPlansInTargetRange(chapters []domain.AdaptationChapterPlan, from, to int) []domain.AdaptationChapterPlan {
	out := make([]domain.AdaptationChapterPlan, 0, to-from+1)
	for idx, chapter := range chapters {
		number := chapter.Chapter
		if number <= 0 {
			number = idx + 1
			chapter.Chapter = number
		}
		if number >= from && number <= to {
			out = append(out, chapter)
		}
	}
	return out
}

func validateAdaptationGeneratedParentBatches(volumes []domain.VolumeOutline) error {
	for _, volume := range volumes {
		var entries []domain.OutlineEntry
		for _, arc := range volume.Arcs {
			entries = append(entries, arc.Chapters...)
		}
		if duplicate, ok := domain.FindDuplicateOutlineEntries(entries); ok {
			return fmt.Errorf("adaptation target volume %d parent batch contains duplicate chapter outline: %w", volume.Index, duplicate)
		}
	}
	return nil
}

func adaptationTargetVolumes(plan domain.AdaptationPlan) []domain.VolumeOutline {
	entries := adaptationTargetOutline(plan)
	if len(entries) == 0 {
		return nil
	}
	volumes := normalizeAdaptationProposalVolumes(plan.Volumes, len(entries))
	if adaptationVolumesCoverChapters(volumes, len(entries)) {
		return adaptationVolumeOutlinesFromPlans(plan, entries, volumes)
	}
	fallbackVolumes := normalizeAdaptationProposalVolumes([]domain.AdaptationVolumePlan{{
		Title:      "Adaptation",
		Theme:      firstNonEmptyString(strings.TrimSpace(plan.Brief), "Confirmed adaptation plan"),
		Goal:       strings.TrimSpace(plan.Brief),
		TargetFrom: 1,
		TargetTo:   len(entries),
	}}, len(entries))
	return adaptationVolumeOutlinesFromPlans(plan, entries, fallbackVolumes)
}

func adaptationVolumeOutlinesFromPlans(plan domain.AdaptationPlan, entries []domain.OutlineEntry, volumes []domain.AdaptationVolumePlan) []domain.VolumeOutline {
	out := make([]domain.VolumeOutline, 0, len(volumes))
	for _, volume := range volumes {
		volumeEntries := append([]domain.OutlineEntry(nil), entries[volume.TargetFrom-1:volume.TargetTo]...)
		title := firstNonEmptyString(volume.Title, fmt.Sprintf("Volume %d", volume.Index))
		goal := firstNonEmptyString(volume.Goal, volume.Summary, volume.Theme, strings.TrimSpace(plan.Brief))
		out = append(out, domain.VolumeOutline{
			ID:    volume.ID,
			Index: volume.Index,
			Title: title,
			Theme: firstNonEmptyString(volume.Theme, volume.Summary, strings.TrimSpace(plan.Brief)),
			Arcs:  adaptationGeneratedOutlineArcs(volume, volumeEntries, title, goal),
		})
	}
	return out
}

func adaptationGeneratedOutlineArcs(volume domain.AdaptationVolumePlan, entries []domain.OutlineEntry, title, goal string) []domain.ArcOutline {
	if len(entries) == 0 {
		return nil
	}
	batchMax := adaptationGeneratedOutlineBatchMax
	if batchMax <= 0 {
		batchMax = adaptationPlannerRecommendedBatchMax
	}
	arcs := make([]domain.ArcOutline, 0, (len(entries)+batchMax-1)/batchMax)
	for offset := 0; offset < len(entries); offset += batchMax {
		end := min(len(entries), offset+batchMax)
		fromChapter := volume.TargetFrom + offset
		toChapter := volume.TargetFrom + end - 1
		arcTitle := title
		arcGoal := goal
		if len(entries) > batchMax {
			arcTitle = adaptationGeneratedOutlineArcTitle(title, fromChapter, toChapter)
			arcGoal = adaptationGeneratedOutlineArcGoal(goal, fromChapter, toChapter)
		}
		arcs = append(arcs, domain.ArcOutline{
			Index:    len(arcs) + 1,
			Title:    arcTitle,
			Goal:     arcGoal,
			Chapters: append([]domain.OutlineEntry(nil), entries[offset:end]...),
		})
	}
	return arcs
}

func adaptationGeneratedOutlineArcTitle(title string, fromChapter, toChapter int) string {
	title = strings.TrimSpace(title)
	if title == "" {
		return fmt.Sprintf("Chapters %d-%d", fromChapter, toChapter)
	}
	return fmt.Sprintf("%s (%d-%d)", title, fromChapter, toChapter)
}

func adaptationGeneratedOutlineArcGoal(goal string, fromChapter, toChapter int) string {
	goal = strings.TrimSpace(goal)
	if goal == "" {
		return fmt.Sprintf("Cover target chapters %d-%d.", fromChapter, toChapter)
	}
	return fmt.Sprintf("%s Cover target chapters %d-%d.", goal, fromChapter, toChapter)
}

func adaptationTargetOutline(plan domain.AdaptationPlan) []domain.OutlineEntry {
	entries := make([]domain.OutlineEntry, 0, len(plan.Chapters))
	for idx, chapter := range plan.Chapters {
		number := chapter.Chapter
		if number <= 0 {
			number = idx + 1
		}
		title := firstNonEmptyString(chapter.Title, chapter.OutlineEntry.Title, fmt.Sprintf("Chapter %d", number))
		coreEvent := firstNonEmptyString(chapter.CoreEvent, chapter.CoverageNote, strings.Join(chapter.PreserveEvents, "；"))
		entries = append(entries, domain.OutlineEntry{
			ID:        chapter.OutlineEntry.ID,
			Chapter:   number,
			Title:     title,
			CoreEvent: coreEvent,
			Hook:      chapter.Hook,
			Scenes:    append([]string(nil), chapter.Scenes...),
		})
	}
	return entries
}

func buildPlanFromInputs(opts ProposalOptions, reports []domain.AdaptationSourceReport, manifest *domain.AdaptationSourceManifest, status string) domain.AdaptationPlan {
	opts.Brief = strings.TrimSpace(opts.Brief)
	opts.Granularity = domain.NormalizeAdaptationGranularity(firstNonEmptyString(opts.Granularity, inferGranularity(opts.Brief)))
	opts.RewritePolicy = domain.AdaptationRewritePolicyForGranularity(opts.Granularity)
	opts.WordTolerance = normalizeWordToleranceForRewritePolicy(opts.RewritePolicy, opts.WordTolerance)

	sourceRunesByChapter := sourceRunesByChapter(manifest)
	sourceTotalRunes := 0
	for _, report := range reports {
		sourceTotalRunes += sourceRunesForReport(report, sourceRunesByChapter)
	}

	plan := domain.AdaptationPlan{
		Granularity:      opts.Granularity,
		ModePolicy:       domain.AdaptationModePolicyForGranularity(opts.Granularity),
		Status:           domain.NormalizeAdaptationPlanStatus(status),
		RewritePolicy:    opts.RewritePolicy,
		Brief:            opts.Brief,
		WordTolerance:    opts.WordTolerance,
		SourceTotalRunes: sourceTotalRunes,
		TargetTotalRunes: sourceTotalRunes,
		MainlineRules: []string{
			"保留原书核心事件的因果顺序，不凭空跳过主线转折。",
			"每章写作前先读取 source refs，对照必须保留事件和禁止偏离事项。",
			"改动关系线时必须用场景行动承接，不能破坏原书主线动机。",
		},
		RelationshipGoals: extractRelationshipGoals(opts.Brief),
		Rules:             domain.CompileAdaptationRules(opts.Brief, opts.Granularity),
		Chapters:          make([]domain.AdaptationChapterPlan, 0, len(reports)),
	}
	if opts.RewritePolicy == domain.AdaptationRewritePreserveDetails {
		plan.TargetMinRunes, plan.TargetMaxRunes = runeRange(sourceTotalRunes, opts.WordTolerance)
		plan.MainlineRules = append(plan.MainlineRules,
			"原著细节优先：未受改编目标影响的剧情、场景和段落允许复用原文；受影响部分再重写。",
			"字数契约为来源字数 ±15%（或用户显式容差），超出硬区间必须重新规划或重写。",
		)
	} else {
		plan.MainlineRules = append(plan.MainlineRules,
			"完全重写：不得直接搬运原文段落或逐段同义替换；只锁定来源映射、主线事件和用户改编目标。",
		)
	}
	nextTargetChapter := 1
	for index := range reports {
		report := reports[index]
		events := domain.EnsureAdaptationSourceEvents(&report)
		plan.SourceEvents = append(plan.SourceEvents, events...)
		if opts.Granularity == domain.AdaptationGranularityChapter {
			chapters := buildChapterSegmentPlans(report, events, opts, sourceRunesByChapter, plan.Rules, nextTargetChapter)
			plan.Chapters = append(plan.Chapters, chapters...)
			nextTargetChapter += len(chapters)
			continue
		}
		chapter := buildChapterPlan(report, opts, sourceRunesByChapter)
		chapter.Chapter = nextTargetChapter
		chapter.OutlineEntry.Chapter = nextTargetChapter
		chapter.EventIDs = adaptationEventIDs(events)
		chapter.RuleIDs = domain.AdaptationRuleIDs(domain.ApplicableAdaptationRules(plan.Rules, opts.Granularity, nextTargetChapter))
		plan.Chapters = append(plan.Chapters, chapter)
		nextTargetChapter++
	}
	if plan.TargetMinRunes <= 0 {
		plan.TargetMinRunes = plan.TargetTotalRunes
	}
	if plan.TargetMaxRunes <= 0 {
		plan.TargetMaxRunes = plan.TargetTotalRunes
	}
	return plan
}

func buildPlannerFallbackPlan(opts ProposalOptions, reports []domain.AdaptationSourceReport, manifest *domain.AdaptationSourceManifest, plannerErr error) domain.AdaptationPlan {
	plan := buildPlanFromInputs(opts, reports, manifest, domain.AdaptationPlanStatusProposal)
	plan.Planner = &domain.AdaptationPlannerMeta{
		Prompt:        adaptationPlannerPromptName,
		PromptVersion: adaptationPlannerPromptVersion,
		GeneratedAt:   time.Now().UTC().Format(time.RFC3339),
		Notes: domain.TextList{
			"planner output was unusable; generated a deterministic proposal from analyzed source reports",
			"planner error: " + plannerErr.Error(),
		},
	}
	return plan
}

func buildChapterPlan(report domain.AdaptationSourceReport, opts ProposalOptions, sourceRunesByChapter map[int]int) domain.AdaptationChapterPlan {
	sourceChapters := []int{report.Chapter}
	sourceRunes := sourceRunesForReport(report, sourceRunesByChapter)
	chapterPlan := domain.AdaptationChapterPlan{
		Chapter:         report.Chapter,
		Title:           report.Title,
		SourceChapters:  sourceChapters,
		SourceRunes:     sourceRunes,
		TargetRunes:     sourceRunes,
		SourceRange:     domain.SourceRange{From: report.Chapter, To: report.Chapter},
		CoverageNote:    coverageNote(opts.Granularity, report.Chapter, report.Chapter),
		PreserveEvents:  append([]string(nil), report.KeyEvents...),
		RequiredChanges: nil,
		ForbiddenMoves: []string{
			"不要遗漏原章关键事件。",
			"不要改变原章核心因果顺序，除非 brief 明确要求。",
		},
	}
	if opts.RewritePolicy == domain.AdaptationRewritePreserveDetails {
		chapterPlan.TargetMinRunes, chapterPlan.TargetMaxRunes = runeRange(sourceRunes, opts.WordTolerance)
		chapterPlan.ForbiddenMoves = append(chapterPlan.ForbiddenMoves,
			"不要无故删除未受改编目标影响的原文细节。",
		)
	} else {
		chapterPlan.ForbiddenMoves = append(chapterPlan.ForbiddenMoves,
			"不要把原文直接同义替换成新正文。",
		)
	}
	if chapterPlan.TargetMinRunes <= 0 {
		chapterPlan.TargetMinRunes = chapterPlan.TargetRunes
	}
	if chapterPlan.TargetMaxRunes <= 0 {
		chapterPlan.TargetMaxRunes = chapterPlan.TargetRunes
	}
	chapterPlan.WordBudget = &domain.AdaptationChapterWordBudget{
		SourceRunes: sourceRunes,
		TargetRunes: chapterPlan.TargetRunes,
		MinRunes:    chapterPlan.TargetMinRunes,
		MaxRunes:    chapterPlan.TargetMaxRunes,
		Tolerance:   opts.WordTolerance,
	}
	return chapterPlan
}

func sourceRunesByChapter(manifest *domain.AdaptationSourceManifest) map[int]int {
	if manifest == nil {
		return nil
	}
	out := make(map[int]int, len(manifest.Chapters))
	for _, source := range manifest.Chapters {
		out[source.Chapter] = source.Runes
	}
	return out
}

func sourceRunesForReport(report domain.AdaptationSourceReport, sourceRunesByChapter map[int]int) int {
	if sourceRunesByChapter == nil {
		return 0
	}
	return sourceRunesByChapter[report.Chapter]
}

func runeRange(sourceRunes int, tolerance float64) (int, int) {
	if sourceRunes <= 0 {
		return 0, 0
	}
	if tolerance <= 0 {
		tolerance = DefaultWordTolerance
	}
	minRunes := int(math.Round(float64(sourceRunes) * (1 - tolerance)))
	maxRunes := int(math.Round(float64(sourceRunes) * (1 + tolerance)))
	if minRunes < 0 {
		minRunes = 0
	}
	if maxRunes < minRunes {
		maxRunes = minRunes
	}
	return minRunes, maxRunes
}

func normalizeWordToleranceForRewritePolicy(rewritePolicy string, tolerance float64) float64 {
	if domain.NormalizeAdaptationRewritePolicy(rewritePolicy) != domain.AdaptationRewritePreserveDetails {
		return 0
	}
	if tolerance <= 0 {
		return DefaultWordTolerance
	}
	return tolerance
}

func coverageNote(granularity string, from, to int) string {
	if from == to {
		if granularity == domain.AdaptationGranularityChapter {
			return fmt.Sprintf("目标章与原文第 %d 章一一对应。", from)
		}
		return fmt.Sprintf("目标章覆盖原文第 %d 章。", from)
	}
	return fmt.Sprintf("目标章覆盖原文第 %d-%d 章。", from, to)
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func inferGranularity(brief string) string {
	lower := strings.ToLower(brief)
	switch {
	case strings.Contains(lower, "free") || strings.Contains(brief, "自由") || strings.Contains(brief, "重构"):
		return domain.AdaptationGranularityFree
	case strings.Contains(lower, "arc") || strings.Contains(brief, "弧") || strings.Contains(brief, "合并") || strings.Contains(brief, "拆分"):
		return domain.AdaptationGranularityArc
	default:
		return domain.AdaptationGranularityChapter
	}
}

func extractRelationshipGoals(brief string) []string {
	brief = strings.TrimSpace(brief)
	if brief == "" {
		return nil
	}
	keywords := []string{"女主", "男主", "感情", "纯爱", "单女主", "虐", "关系", "互动"}
	for _, keyword := range keywords {
		if strings.Contains(brief, keyword) {
			return []string{brief}
		}
	}
	return nil
}

func toSourceFoundation(fr *imp.FoundationResult) domain.AdaptationSourceFoundation {
	if fr == nil {
		return domain.AdaptationSourceFoundation{}
	}
	return domain.AdaptationSourceFoundation{
		Premise:       fr.Premise,
		Characters:    append([]domain.Character(nil), fr.Characters...),
		Relationships: append([]domain.CharacterRelationship(nil), fr.Relationships...),
		WorldRules:    append([]domain.WorldRule(nil), fr.WorldRules...),
		Volumes:       append([]domain.VolumeOutline(nil), fr.Volumes...),
		Compass:       fr.Compass,
	}
}

func toFoundationResult(fr *domain.AdaptationSourceFoundation) *imp.FoundationResult {
	if fr == nil {
		return nil
	}
	return &imp.FoundationResult{
		Premise:       fr.Premise,
		Characters:    append([]domain.Character(nil), fr.Characters...),
		Relationships: append([]domain.CharacterRelationship(nil), fr.Relationships...),
		WorldRules:    append([]domain.WorldRule(nil), fr.WorldRules...),
		Volumes:       append([]domain.VolumeOutline(nil), fr.Volumes...),
		Compass:       fr.Compass,
	}
}

func toSourceReport(chapter int, title string, analysis *imp.ChapterAnalysis) domain.AdaptationSourceReport {
	if analysis == nil {
		return domain.AdaptationSourceReport{Chapter: chapter, Title: title}
	}
	return domain.AdaptationSourceReport{
		Chapter:           chapter,
		Title:             title,
		Summary:           analysis.Summary,
		Characters:        append([]string(nil), analysis.Characters...),
		CharacterProfiles: append([]domain.Character(nil), analysis.CharacterProfiles...),
		CharacterFacts:    append([]string(nil), analysis.CharacterFacts...),
		KeyEvents:         append([]string(nil), analysis.KeyEvents...),
		WorldRules:        append([]string(nil), analysis.WorldRules...),
		HookType:          analysis.HookType,
		DominantStrand:    analysis.DominantStrand,
		Timeline:          append([]domain.TimelineEvent(nil), analysis.TimelineEvents...),
		Foreshadow:        append([]domain.ForeshadowUpdate(nil), analysis.ForeshadowUpdates...),
		Relationships:     append([]domain.RelationshipEntry(nil), analysis.RelationshipChanges...),
		StateChanges:      append([]domain.StateChange(nil), analysis.StateChanges...),
	}
}

func charactersBlock(chars []domain.Character) string {
	var sb strings.Builder
	for _, c := range chars {
		fmt.Fprintf(&sb, "- **%s**（%s）：%s\n", c.Name, c.Role, oneLine(c.Description))
	}
	return sb.String()
}

func adaptationPremise(sourcePremise, brief string, plan domain.AdaptationPlan) string {
	var sb strings.Builder
	sourcePremise = strings.TrimSpace(sourcePremise)
	if sourcePremise == "" {
		sb.WriteString("# 改编作品\n")
	} else {
		sb.WriteString(sourcePremise)
		sb.WriteString("\n")
	}
	sb.WriteString("\n## 改编契约\n\n")
	fmt.Fprintf(&sb, "- 契约状态：%s\n", plan.Status)
	fmt.Fprintf(&sb, "- 改编粒度：%s\n", plan.Granularity)
	fmt.Fprintf(&sb, "- 改写策略：%s\n", plan.RewritePolicy)
	if plan.SourceTotalRunes > 0 {
		fmt.Fprintf(&sb, "- 来源总字数：%d 字\n", plan.SourceTotalRunes)
	}
	if plan.TargetMinRunes > 0 || plan.TargetMaxRunes > 0 {
		fmt.Fprintf(&sb, "- 目标总字数硬区间：%d-%d 字\n", plan.TargetMinRunes, plan.TargetMaxRunes)
	}
	fmt.Fprintf(&sb, "- 用户 brief：%s\n", strings.TrimSpace(brief))
	for _, rule := range plan.MainlineRules {
		fmt.Fprintf(&sb, "- 主线规则：%s\n", rule)
	}
	return sb.String()
}

func planningTier(total int) domain.PlanningTier {
	switch {
	case total <= 25:
		return domain.PlanningTierShort
	case total <= 80:
		return domain.PlanningTierMid
	default:
		return domain.PlanningTierLong
	}
}

func oneLine(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	runes := []rune(s)
	if len(runes) > 200 {
		return string(runes[:200]) + "..."
	}
	return s
}
