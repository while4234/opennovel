package sim

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/modeldiag"
	"github.com/voocel/ainovel-cli/internal/retrypolicy"
)

const (
	maxSourceRunes = 15000

	maxMergePromptBytes           = 60 << 10
	maxMergeReportsPerBatch       = 24
	maxMergeReportTitleRunes      = 120
	maxMergeReportSummaryRunes    = 600
	maxMergeReportItemRunes       = 240
	maxMergeReportItemsPerList    = 8
	maxMergeSynthesisItemsPerList = 10
	maxMergeSynthesisItemRunes    = 240
)

type mergeSynthesisOptions struct {
	Call               structuredJSONCallOptions
	Checkpoint         *domain.SimulationMergeCheckpoint
	SynthesisSignature string
	OnBatch            func(mergeSynthesisProgress)
	OnCheckpoint       func(mergeSynthesisCheckpoint) error
}

type mergeSynthesisProgress struct {
	Current    int
	Total      int
	BatchIndex int
	BatchTotal int
	BatchSize  int
}

type mergeSynthesisCheckpoint struct {
	ProcessedReportCount int
	TotalReportCount     int
	ProcessedBatchCount  int
	Synthesis            domain.SimulationSynthesis
}

func Run(ctx context.Context, deps Deps, opts Options) (<-chan Event, error) {
	ctx = modeldiag.WithStore(ctx, deps.Store)
	if deps.Store == nil || deps.LLM == nil {
		return nil, fmt.Errorf("deps incomplete")
	}
	if strings.TrimSpace(opts.SourceDir) == "" {
		return nil, fmt.Errorf("source dir is required")
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

		signatures := buildSimulationAnalysisSignatures(deps)
		emit(StageScan, 0, 0, "扫描 simulate 语料并计算分析签名...", nil)
		sources, err := scanSources(opts.SourceDir)
		if err != nil {
			emit(StageError, 0, 0, "扫描 simulate 目录失败", err)
			return
		}
		if len(sources) == 0 {
			if err := deps.Store.Simulation.Clear(); err != nil {
				emit(StageError, 0, 0, "清除空语料画像失败", err)
				return
			}
			emit(StageDone, 0, 0, "simulate 目录为空，已清除画像及本地证据", nil)
			return
		}

		portable, err := deps.Store.Simulation.LoadPortable()
		if err != nil {
			emit(StageError, 0, len(sources), "读取画像元数据失败", err)
			return
		}
		existing, err := deps.Store.Simulation.Load()
		if err != nil {
			emit(StageError, 0, len(sources), "读取既有画像失败", err)
			return
		}
		existing, prunedExisting := pruneProfileToScannedSources(existing, sources)
		if prunedExisting && existing != nil {
			existing.Synthesis = domain.SimulationSynthesis{}
		}
		analysisMetadata := signatures.metadata
		reportSignature := analysisMetadata.SourceAnalysisSignature
		if opts.Action == ActionResynthesize && portable != nil && !portable.Analysis.Legacy {
			reportSignature = portable.Analysis.SourceAnalysisSignature
			analysisMetadata.SourceAnalysisSignature = portable.Analysis.SourceAnalysisSignature
			analysisMetadata.SplitterSignature = portable.Analysis.SplitterSignature
			analysisMetadata.SchemaSignature = portable.Analysis.SchemaSignature
			analysisMetadata.ModelIdentity = portable.Analysis.ModelIdentity
		}
		allowLegacyReports := portable != nil && portable.Analysis.Legacy
		pending := pendingSourcesForSignature(existing, sources, reportSignature, allowLegacyReports)
		switch opts.Action {
		case "", ActionScan:
		case ActionResynthesize:
			if len(pending) > 0 {
				emit(StageError, 0, len(pending), "现有逐篇报告已失效，不能仅重合成；请重新分析语料", fmt.Errorf("simulation reports require reanalysis"))
				return
			}
			existing.Synthesis = domain.SimulationSynthesis{}
		case ActionReanalyze:
			pending = append([]scannedSource(nil), sources...)
			existing.Synthesis = domain.SimulationSynthesis{}
		default:
			emit(StageError, 0, len(sources), "不支持的画像刷新操作", fmt.Errorf("unsupported simulation action"))
			return
		}
		synthesisSignatureChanged := portable == nil ||
			portable.Analysis.SynthesisSignature != signatures.metadata.SynthesisSignature ||
			portable.Analysis.AggregationSignature != signatures.metadata.AggregationSignature
		needsSynthesis := prunedExisting || synthesisSignatureChanged || len(pending) > 0 || profileNeedsSynthesis(existing)
		if opts.Action == ActionResynthesize {
			needsSynthesis = true
		}
		if portable != nil && portable.Health.State != "fresh" {
			needsSynthesis = true
		}
		if len(pending) > 0 || prunedExisting || synthesisSignatureChanged {
			if err := deps.Store.Simulation.ClearMergeCheckpoint(); err != nil {
				emit(StageError, 0, len(sources), "清理过期画像合并断点失败", err)
				return
			}
		}
		if len(pending) == 0 && !needsSynthesis {
			emit(StageDone, 0, len(sources), "画像已是最新，语料与分析签名均未变化", nil)
			return
		}
		if len(pending) == 0 {
			reason := "仅合成签名变化，复用有效逐篇报告重合成画像..."
			if prunedExisting {
				reason = "检测到语料删除，旧画像已失效，正在从剩余报告重合成..."
			}
			emit(StageMerge, len(sources), len(sources), reason, nil)
		} else {
			emit(StageAnalyze, 0, len(pending), fmt.Sprintf("重算计划：重分析 %d 篇，随后重合成画像", len(pending)), nil)
		}

		reports := make([]domain.SimulationSourceReport, 0, len(pending))
		for i, source := range pending {
			if err := ctx.Err(); err != nil {
				emit(StageError, i, len(pending), "用户取消画像分析", err)
				return
			}
			emit(StageAnalyze, i+1, len(pending), fmt.Sprintf("分析仿写语料 %d/%d", i+1, len(pending)), nil)
			report, err := analyzeSourceWithSignatureOptions(ctx, deps.LLM, deps.Prompts.Source, source, signatures.metadata.SourceAnalysisSignature, structuredJSONCallOptions{
				ModelCallMaxAttempts:       deps.modelCallMaxAttempts(),
				StructureRepairMaxAttempts: deps.structureRepairMaxAttempts(),
				OnRetry: func(ev structuredJSONRetryEvent) {
					emit(StageAnalyze, i+1, len(pending), formatStructuredJSONRetryMessage(ev), ev.Err)
				},
			})
			if err != nil {
				emit(StageError, i+1, len(pending), "语料分析失败", err)
				return
			}
			reports = append(reports, *report)
			profile := buildProfile(existing, opts.SourceDir, []scannedSource{source}, []domain.SimulationSourceReport{*report}, domain.SimulationSynthesis{}, time.Now())
			if err := saveSimulationAnalysisState(
				deps.Store.Simulation,
				profile,
				analysisMetadata,
				domain.SimulationProfileHealth{State: "stale", Reasons: []string{"synthesis_pending"}},
				time.Now(),
			); err != nil {
				emit(StageError, i+1, len(pending), "保存逐篇分析进度失败", err)
				return
			}
			existing = &profile
		}

		allReports := mergeSourceReports(existing, reports)
		mergeCurrent, mergeTotal := 0, len(allReports)
		checkpoint, err := deps.Store.Simulation.LoadMergeCheckpoint()
		if err != nil {
			emit(StageError, mergeCurrent, mergeTotal, "读取画像合并断点失败", err)
			return
		}
		if checkpoint != nil {
			if _, ok := validMergeCheckpointWithSignature(checkpoint, allReports, signatures.metadata.SynthesisSignature); !ok {
				if err := deps.Store.Simulation.ClearMergeCheckpoint(); err != nil {
					emit(StageError, mergeCurrent, mergeTotal, "清理过期画像合并断点失败", err)
					return
				}
				checkpoint = nil
			}
		}
		emit(StageMerge, mergeCurrent, mergeTotal, "合并仿写画像...", nil)
		synthesis, err := mergeSynthesisBatchedWithOptions(ctx, deps.LLM, deps.Prompts.Merge, existing, allReports, mergeSynthesisOptions{
			Call: structuredJSONCallOptions{
				ModelCallMaxAttempts:       deps.modelCallMaxAttempts(),
				StructureRepairMaxAttempts: deps.structureRepairMaxAttempts(),
				OnRetry: func(ev structuredJSONRetryEvent) {
					emit(StageMerge, mergeCurrent, mergeTotal, formatStructuredJSONRetryMessage(ev), ev.Err)
				},
			},
			OnBatch: func(progress mergeSynthesisProgress) {
				mergeCurrent, mergeTotal = progress.Current, progress.Total
				msg := "合并仿写画像..."
				if progress.BatchTotal > 1 {
					msg = fmt.Sprintf("分批合并仿写画像 %d/%d（批次 %d/%d，%d 篇）...", progress.Current, progress.Total, progress.BatchIndex, progress.BatchTotal, progress.BatchSize)
				}
				emit(StageMerge, progress.Current, progress.Total, msg, nil)
			},
			Checkpoint:         checkpoint,
			SynthesisSignature: signatures.metadata.SynthesisSignature,
			OnCheckpoint: func(checkpoint mergeSynthesisCheckpoint) error {
				domainCheckpoint := buildSimulationMergeCheckpointWithSignature(allReports, maxMergePromptBytes, signatures.metadata.SynthesisSignature, checkpoint, time.Now())
				if domainCheckpoint == nil {
					return nil
				}
				return deps.Store.Simulation.SaveMergeCheckpoint(*domainCheckpoint)
			},
		})
		if err != nil {
			emit(StageError, mergeCurrent, mergeTotal, "画像合并失败", err)
			return
		}
		profile := buildProfile(existing, opts.SourceDir, nil, nil, *synthesis, time.Now())
		if err := saveSimulationAnalysisState(
			deps.Store.Simulation,
			profile,
			analysisMetadata,
			domain.SimulationProfileHealth{State: "fresh"},
			time.Now(),
		); err != nil {
			emit(StageError, mergeCurrent, mergeTotal, "保存仿写画像失败", err)
			return
		}
		if err := deps.Store.Simulation.ClearMergeCheckpoint(); err != nil {
			emit(StageError, mergeCurrent, mergeTotal, "清理已完成画像合并断点失败", err)
			return
		}
		emit(StageDone, len(pending), len(pending), fmt.Sprintf("仿写画像已更新：新增/变更 %d 篇，累计 %d 篇", len(pending), len(profile.Corpus.Sources)), nil)
	}()
	return events, nil
}

func AnalyzeSource(ctx context.Context, llm LLMChat, systemPrompt string, source scannedSource) (*domain.SimulationSourceReport, error) {
	return analyzeSourceWithSignatureOptions(ctx, llm, systemPrompt, source, simulationSignature(systemPrompt, "unspecified"), structuredJSONCallOptions{})
}

func (d Deps) modelCallMaxAttempts() int {
	if d.ModelCallMaxAttempts > 0 {
		return d.ModelCallMaxAttempts
	}
	return retrypolicy.MaxAttempts
}

func (d Deps) structureRepairMaxAttempts() int {
	if d.StructureRepairMaxAttempts > 0 {
		return d.StructureRepairMaxAttempts
	}
	return defaultStructureRepairMaxAttempts
}

func formatStructuredJSONRetryMessage(ev structuredJSONRetryEvent) string {
	label := "重试"
	switch ev.Kind {
	case structuredJSONRetryKindModelCall:
		label = "模型调用重试"
	case structuredJSONRetryKindStructureRepair:
		label = "结构修复"
	}
	detail := retrypolicy.SanitizeProviderError(ev.Err)
	if strings.TrimSpace(detail) == "" && ev.Err != nil {
		detail = ev.Err.Error()
	}
	return fmt.Sprintf("%s %d/%d：%s", label, ev.Attempt, ev.MaxAttempts, detail)
}

func analyzeSourceWithOptions(ctx context.Context, llm LLMChat, systemPrompt string, source scannedSource, opts structuredJSONCallOptions) (*domain.SimulationSourceReport, error) {
	return analyzeSourceWithSignatureOptions(ctx, llm, systemPrompt, source, simulationSignature(systemPrompt, "unspecified"), opts)
}

func analyzeSourceWithSignatureOptions(ctx context.Context, llm LLMChat, systemPrompt string, source scannedSource, analysisSignature string, opts structuredJSONCallOptions) (*domain.SimulationSourceReport, error) {
	if strings.TrimSpace(systemPrompt) == "" {
		return nil, fmt.Errorf("source prompt is required")
	}
	userPrompt, coverage := buildStructuredSourceUserPrompt(source)
	messages := []agentcore.Message{
		agentcore.SystemMsg(systemPrompt),
		agentcore.UserMsg(userPrompt),
	}
	report, err := runStructuredJSONCall(ctx, llm, messages, func(text string) (domain.SimulationSourceReport, error) {
		var report domain.SimulationSourceReport
		if err := parseJSONPayload(text, &report); err != nil {
			return report, fmt.Errorf("parse source report: %w", err)
		}
		ratio := coverage.Ratio
		report.Coverage = &ratio
		report.Health = reportHealthForCoverage(strings.ToLower(strings.TrimSpace(report.ContentType)), coverage)
		if err := domain.NormalizeAndValidateSimulationSourceReport(&report); err != nil {
			return report, fmt.Errorf("source report validation: %w", err)
		}
		return report, nil
	}, opts)
	if err != nil {
		return nil, fmt.Errorf("analyze source: %w", err)
	}
	now := time.Now().Format(time.RFC3339)
	report.RelativePath = source.RelativePath
	report.SHA256 = source.SHA256
	report.Fingerprint = source.Fingerprint
	report.AnalyzedAt = now
	report.AnalysisSignature = analysisSignature
	return &report, nil
}

func MergeSynthesis(ctx context.Context, llm LLMChat, systemPrompt string, existing *domain.SimulationProfile, reports []domain.SimulationSourceReport) (*domain.SimulationSynthesis, error) {
	return mergeSynthesisBatchedWithOptions(ctx, llm, systemPrompt, existing, reports, mergeSynthesisOptions{})
}

func mergeSynthesisBatchedWithOptions(ctx context.Context, llm LLMChat, systemPrompt string, existing *domain.SimulationProfile, reports []domain.SimulationSourceReport, opts mergeSynthesisOptions) (*domain.SimulationSynthesis, error) {
	return mergeSynthesisBatchedWithLimit(ctx, llm, systemPrompt, existing, reports, maxMergePromptBytes, opts)
}

func mergeSynthesisBatchedWithLimit(ctx context.Context, llm LLMChat, systemPrompt string, existing *domain.SimulationProfile, reports []domain.SimulationSourceReport, promptLimitBytes int, opts mergeSynthesisOptions) (*domain.SimulationSynthesis, error) {
	compactReports := compactSourceReportsForMerge(reports)
	if len(compactReports) == 0 {
		return mergeSynthesisWithOptions(ctx, llm, systemPrompt, existing, nil, opts.Call)
	}
	estimatedBatchTotal := len(splitMergeReportBatches(systemPrompt, existing, compactReports, promptLimitBytes))
	if estimatedBatchTotal == 0 {
		estimatedBatchTotal = 1
	}

	synthesis := existingSynthesis(existing)
	processed := 0
	batchIndex := 0
	total := len(compactReports)
	if checkpoint, ok := validMergeCheckpointWithSignature(opts.Checkpoint, reports, opts.SynthesisSignature); ok {
		synthesis = checkpoint.RollingSynthesis
		processed = checkpoint.ProcessedReportCount
		batchIndex = checkpoint.ProcessedBatchCount
		if batchIndex > estimatedBatchTotal {
			estimatedBatchTotal = batchIndex
		}
	}
	for processed < total {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		mergeBase := profileWithSynthesisForMerge(existing, reports, synthesis)
		batch := nextMergeReportBatch(systemPrompt, mergeBase, compactReports[processed:], promptLimitBytes)
		if len(batch) == 0 {
			return nil, fmt.Errorf("merge profile: empty merge batch")
		}
		batchIndex++
		if batchIndex > estimatedBatchTotal {
			estimatedBatchTotal = batchIndex
		}
		if opts.OnBatch != nil {
			opts.OnBatch(mergeSynthesisProgress{
				Current:    processed + len(batch),
				Total:      total,
				BatchIndex: batchIndex,
				BatchTotal: estimatedBatchTotal,
				BatchSize:  len(batch),
			})
		}
		next, err := mergeSynthesisWithOptions(ctx, llm, systemPrompt, mergeBase, batch, opts.Call)
		if err != nil {
			start := processed + 1
			end := processed + len(batch)
			return nil, fmt.Errorf("merge profile batch %d/%d (%d-%d/%d reports): %w", batchIndex, estimatedBatchTotal, start, end, total, err)
		}
		synthesis = *next
		processed += len(batch)
		if opts.OnCheckpoint != nil {
			if err := opts.OnCheckpoint(mergeSynthesisCheckpoint{
				ProcessedReportCount: processed,
				TotalReportCount:     total,
				ProcessedBatchCount:  batchIndex,
				Synthesis:            synthesis,
			}); err != nil {
				return nil, fmt.Errorf("save merge checkpoint: %w", err)
			}
		}
	}
	return &synthesis, nil
}

func mergeSynthesisWithOptions(ctx context.Context, llm LLMChat, systemPrompt string, existing *domain.SimulationProfile, reports []domain.SimulationSourceReport, opts structuredJSONCallOptions) (*domain.SimulationSynthesis, error) {
	if strings.TrimSpace(systemPrompt) == "" {
		return nil, fmt.Errorf("merge prompt is required")
	}
	messages := []agentcore.Message{
		agentcore.SystemMsg(systemPrompt),
		agentcore.UserMsg(buildMergeUserPrompt(existing, reports)),
	}
	synthesis, err := runStructuredJSONCall(ctx, llm, messages, func(text string) (domain.SimulationSynthesis, error) {
		var synthesis domain.SimulationSynthesis
		if err := parseJSONPayload(text, &synthesis); err != nil {
			return synthesis, fmt.Errorf("parse synthesis: %w", err)
		}
		return synthesis, nil
	}, opts)
	if err != nil {
		return nil, fmt.Errorf("merge profile: %w", err)
	}
	return &synthesis, nil
}

func pruneProfileToScannedSources(existing *domain.SimulationProfile, sources []scannedSource) (*domain.SimulationProfile, bool) {
	if existing == nil {
		return nil, false
	}
	current := make(map[string]struct{}, len(sources))
	for _, source := range sources {
		if strings.TrimSpace(source.Fingerprint) != "" {
			current[source.Fingerprint] = struct{}{}
		}
	}
	next := *existing
	next.Corpus.Sources = pruneSimulationSources(existing.Corpus.Sources, current)
	next.SourceReports = pruneSimulationSourceReports(existing.SourceReports, current)
	changed := len(next.Corpus.Sources) != len(existing.Corpus.Sources) ||
		len(next.SourceReports) != len(existing.SourceReports)
	return &next, changed
}

func pruneSimulationSources(sources []domain.SimulationSource, current map[string]struct{}) []domain.SimulationSource {
	out := make([]domain.SimulationSource, 0, len(sources))
	for _, source := range sources {
		fingerprint := strings.TrimSpace(source.Fingerprint)
		if fingerprint == "" && source.RelativePath != "" && source.SHA256 != "" {
			fingerprint = domain.SimulationSourceFingerprint(source.RelativePath, source.SHA256)
		}
		if _, ok := current[fingerprint]; ok {
			out = append(out, source)
		}
	}
	return out
}

func pruneSimulationSourceReports(reports []domain.SimulationSourceReport, current map[string]struct{}) []domain.SimulationSourceReport {
	out := make([]domain.SimulationSourceReport, 0, len(reports))
	for _, report := range reports {
		fingerprint := strings.TrimSpace(report.Fingerprint)
		if fingerprint == "" && report.RelativePath != "" && report.SHA256 != "" {
			fingerprint = domain.SimulationSourceFingerprint(report.RelativePath, report.SHA256)
		}
		if _, ok := current[fingerprint]; ok {
			out = append(out, report)
		}
	}
	return out
}

func existingSynthesis(existing *domain.SimulationProfile) domain.SimulationSynthesis {
	if existing == nil {
		return domain.SimulationSynthesis{}
	}
	return existing.Synthesis
}

func profileNeedsSynthesis(existing *domain.SimulationProfile) bool {
	return existing != nil && len(existing.SourceReports) > 0 && synthesisIsEmpty(existing.Synthesis)
}

func saveFinalSimulationProfile(st interface {
	Save(domain.SimulationProfile) error
	ClearMergeCheckpoint() error
}, profile domain.SimulationProfile) error {
	if st == nil {
		return fmt.Errorf("simulation store is nil")
	}
	if err := st.Save(profile); err != nil {
		return err
	}
	return st.ClearMergeCheckpoint()
}

func buildSimulationMergeCheckpoint(reports []domain.SimulationSourceReport, promptLimitBytes int, checkpoint mergeSynthesisCheckpoint, now time.Time) *domain.SimulationMergeCheckpoint {
	return buildSimulationMergeCheckpointWithSignature(reports, promptLimitBytes, "", checkpoint, now)
}

func buildSimulationMergeCheckpointWithSignature(reports []domain.SimulationSourceReport, promptLimitBytes int, synthesisSignature string, checkpoint mergeSynthesisCheckpoint, now time.Time) *domain.SimulationMergeCheckpoint {
	if checkpoint.ProcessedReportCount <= 0 || synthesisIsEmpty(checkpoint.Synthesis) {
		return nil
	}
	total := len(reports)
	if checkpoint.TotalReportCount > 0 {
		total = checkpoint.TotalReportCount
	}
	if total != len(reports) {
		return nil
	}
	processed := checkpoint.ProcessedReportCount
	if processed > total {
		return nil
	}
	return &domain.SimulationMergeCheckpoint{
		Version:              domain.SimulationMergeCheckpointVersion,
		UpdatedAt:            now.Format(time.RFC3339),
		PromptLimitBytes:     promptLimitBytes,
		TotalReportCount:     total,
		ProcessedReportCount: processed,
		ProcessedBatchCount:  checkpoint.ProcessedBatchCount,
		Reports:              canonicalReportIdentities(reports),
		SynthesisSignature:   synthesisSignature,
		RollingSynthesis:     checkpoint.Synthesis,
	}
}

func validMergeCheckpoint(checkpoint *domain.SimulationMergeCheckpoint, reports []domain.SimulationSourceReport) (*domain.SimulationMergeCheckpoint, bool) {
	return validMergeCheckpointWithSignature(checkpoint, reports, "")
}

func validMergeCheckpointWithSignature(checkpoint *domain.SimulationMergeCheckpoint, reports []domain.SimulationSourceReport, synthesisSignature string) (*domain.SimulationMergeCheckpoint, bool) {
	if checkpoint == nil {
		return nil, false
	}
	if err := domain.ValidateSimulationMergeCheckpoint(checkpoint); err != nil {
		return nil, false
	}
	if checkpoint.TotalReportCount != len(reports) || synthesisIsEmpty(checkpoint.RollingSynthesis) {
		return nil, false
	}
	if checkpoint.ProcessedReportCount > len(reports) {
		return nil, false
	}
	if checkpoint.SynthesisSignature != synthesisSignature {
		return nil, false
	}
	if !sameReportIdentities(checkpoint.Reports, canonicalReportIdentities(reports)) {
		return nil, false
	}
	return checkpoint, true
}

func reportIdentitiesForMerge(reports []domain.SimulationSourceReport) []domain.SimulationReportIdentity {
	if len(reports) == 0 {
		return nil
	}
	out := make([]domain.SimulationReportIdentity, 0, len(reports))
	for _, report := range reports {
		relativePath := strings.TrimSpace(report.RelativePath)
		sha := strings.TrimSpace(report.SHA256)
		fingerprint := strings.TrimSpace(report.Fingerprint)
		if fingerprint == "" && relativePath != "" && sha != "" {
			fingerprint = domain.SimulationSourceFingerprint(relativePath, sha)
		}
		out = append(out, domain.SimulationReportIdentity{
			RelativePath: relativePath,
			SHA256:       sha,
			Fingerprint:  fingerprint,
		})
	}
	return out
}

func sameReportIdentities(a, b []domain.SimulationReportIdentity) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].RelativePath != b[i].RelativePath || a[i].SHA256 != b[i].SHA256 || a[i].Fingerprint != b[i].Fingerprint {
			return false
		}
	}
	return true
}

func synthesisIsEmpty(s domain.SimulationSynthesis) bool {
	return len(s.Style.NarrativeVoice) == 0 &&
		len(s.Style.SentenceRhythm) == 0 &&
		len(s.Style.ProseTexture) == 0 &&
		len(s.Style.Perspective) == 0 &&
		len(s.Style.Mood) == 0 &&
		len(s.Style.DoNotCopy) == 0 &&
		len(s.Lexicon.CommonWords) == 0 &&
		len(s.Lexicon.EmotionWords) == 0 &&
		len(s.Lexicon.SceneWords) == 0 &&
		len(s.Lexicon.TransitionWords) == 0 &&
		len(s.Lexicon.SignaturePhrases) == 0 &&
		len(s.PlotDesign.OpeningPatterns) == 0 &&
		len(s.PlotDesign.EscalationPatterns) == 0 &&
		len(s.PlotDesign.TurningPointPatterns) == 0 &&
		len(s.PlotDesign.PayoffPatterns) == 0 &&
		len(s.HookDesign.HookTypes) == 0 &&
		len(s.HookDesign.Placement) == 0 &&
		len(s.HookDesign.CliffhangerPatterns) == 0 &&
		len(s.HookDesign.PayoffRules) == 0 &&
		len(s.PacingDensity.SceneDensity) == 0 &&
		len(s.PacingDensity.InformationRelease) == 0 &&
		len(s.PacingDensity.DialogueActionRatio) == 0 &&
		len(s.PacingDensity.CompressionRules) == 0 &&
		len(s.ReaderEngagement.Methods) == 0 &&
		len(s.ReaderEngagement.EmotionalDrivers) == 0 &&
		len(s.ReaderEngagement.ProgressionRewards) == 0 &&
		len(s.ReaderEngagement.AntiPatterns) == 0 &&
		len(s.RoleGuidance.Coordinator) == 0 &&
		len(s.RoleGuidance.Architect) == 0 &&
		len(s.RoleGuidance.Writer) == 0 &&
		len(s.RoleGuidance.Editor) == 0
}

func buildProfile(
	existing *domain.SimulationProfile,
	sourceDir string,
	pending []scannedSource,
	reports []domain.SimulationSourceReport,
	synthesis domain.SimulationSynthesis,
	now time.Time,
) domain.SimulationProfile {
	stamp := now.Format(time.RFC3339)
	profile := domain.SimulationProfile{
		Version:   domain.SimulationProfileVersion,
		CreatedAt: stamp,
		UpdatedAt: stamp,
		Corpus: domain.SimulationCorpusManifest{
			SourceDir: filepath.ToSlash(sourceDir),
		},
		Synthesis: synthesis,
	}
	if existing != nil {
		profile.CreatedAt = existing.CreatedAt
		if profile.CreatedAt == "" {
			profile.CreatedAt = stamp
		}
		profile.Corpus.Sources = append(profile.Corpus.Sources, existing.Corpus.Sources...)
		profile.SourceReports = append(profile.SourceReports, existing.SourceReports...)
	}

	for i, source := range pending {
		source.AnalyzedAt = stamp
		profile.Corpus.Sources = replaceSourceByPath(profile.Corpus.Sources, source.SimulationSource)
		if i < len(reports) {
			report := reports[i]
			report.AnalyzedAt = stamp
			profile.SourceReports = replaceReportByPath(profile.SourceReports, report)
		}
	}
	sortProfile(&profile)
	return profile
}

func mergeSourceReports(existing *domain.SimulationProfile, reports []domain.SimulationSourceReport) []domain.SimulationSourceReport {
	var merged []domain.SimulationSourceReport
	if existing != nil {
		merged = append(merged, existing.SourceReports...)
	}
	for _, report := range reports {
		merged = replaceReportByPath(merged, report)
	}
	sort.Slice(merged, func(i, j int) bool {
		if merged[i].RelativePath == merged[j].RelativePath {
			return merged[i].Fingerprint < merged[j].Fingerprint
		}
		return merged[i].RelativePath < merged[j].RelativePath
	})
	return merged
}

func replaceSourceByPath(sources []domain.SimulationSource, next domain.SimulationSource) []domain.SimulationSource {
	out := sources[:0]
	for _, source := range sources {
		if source.RelativePath == next.RelativePath {
			continue
		}
		out = append(out, source)
	}
	return append(out, next)
}

func replaceReportByPath(reports []domain.SimulationSourceReport, next domain.SimulationSourceReport) []domain.SimulationSourceReport {
	out := reports[:0]
	for _, report := range reports {
		if report.RelativePath == next.RelativePath {
			continue
		}
		out = append(out, report)
	}
	return append(out, next)
}

func sortProfile(profile *domain.SimulationProfile) {
	sort.Slice(profile.Corpus.Sources, func(i, j int) bool {
		if profile.Corpus.Sources[i].RelativePath == profile.Corpus.Sources[j].RelativePath {
			return profile.Corpus.Sources[i].Fingerprint < profile.Corpus.Sources[j].Fingerprint
		}
		return profile.Corpus.Sources[i].RelativePath < profile.Corpus.Sources[j].RelativePath
	})
	sort.Slice(profile.SourceReports, func(i, j int) bool {
		if profile.SourceReports[i].RelativePath == profile.SourceReports[j].RelativePath {
			return profile.SourceReports[i].Fingerprint < profile.SourceReports[j].Fingerprint
		}
		return profile.SourceReports[i].RelativePath < profile.SourceReports[j].RelativePath
	})
}

func buildMergeUserPrompt(existing *domain.SimulationProfile, reports []domain.SimulationSourceReport) string {
	payload := map[string]any{
		"existing_profile": compactProfileForMerge(existing),
		"source_reports":   compactSourceReportsForMerge(reports),
	}
	data, _ := json.MarshalIndent(payload, "", "  ")
	return "Merge these reports into a reusable writing simulation profile. Return only the requested JSON object.\n\n" + string(data)
}

func splitMergeReportBatches(systemPrompt string, existing *domain.SimulationProfile, reports []domain.SimulationSourceReport, promptLimitBytes int) [][]domain.SimulationSourceReport {
	compactReports := compactSourceReportsForMerge(reports)
	if len(compactReports) == 0 {
		return nil
	}
	var batches [][]domain.SimulationSourceReport
	for len(compactReports) > 0 {
		batch := nextMergeReportBatch(systemPrompt, existing, compactReports, promptLimitBytes)
		if len(batch) == 0 {
			break
		}
		batches = append(batches, batch)
		compactReports = compactReports[len(batch):]
	}
	return batches
}

func nextMergeReportBatch(systemPrompt string, existing *domain.SimulationProfile, reports []domain.SimulationSourceReport, promptLimitBytes int) []domain.SimulationSourceReport {
	if len(reports) == 0 {
		return nil
	}
	if promptLimitBytes <= 0 {
		return append([]domain.SimulationSourceReport(nil), reports...)
	}
	var current []domain.SimulationSourceReport
	for _, report := range reports {
		candidate := append(append([]domain.SimulationSourceReport(nil), current...), report)
		tooManyReports := len(candidate) > maxMergeReportsPerBatch
		messages := []agentcore.Message{agentcore.SystemMsg(systemPrompt), agentcore.UserMsg(buildMergeUserPrompt(existing, candidate))}
		diagnosticSystem, diagnosticUser := simulationDiagnosticInput(messages)
		tooManyBytes := len(diagnosticSystem)+len(diagnosticUser) > promptLimitBytes
		if len(current) > 0 && (tooManyReports || tooManyBytes) {
			break
		}
		current = candidate
	}
	if len(current) == 0 {
		return []domain.SimulationSourceReport{reports[0]}
	}
	return current
}

func profileWithSynthesisForMerge(existing *domain.SimulationProfile, reports []domain.SimulationSourceReport, synthesis domain.SimulationSynthesis) *domain.SimulationProfile {
	if existing != nil {
		profile := *existing
		profile.Synthesis = synthesis
		return &profile
	}
	if synthesisIsEmpty(synthesis) {
		return nil
	}
	profile := domain.SimulationProfile{
		Version:   domain.SimulationProfileVersion,
		Synthesis: synthesis,
	}
	for _, report := range reports {
		if strings.TrimSpace(report.RelativePath) == "" {
			continue
		}
		source := domain.SimulationSource{
			RelativePath: report.RelativePath,
			SHA256:       report.SHA256,
			Fingerprint:  report.Fingerprint,
		}
		if source.Fingerprint == "" && source.RelativePath != "" && source.SHA256 != "" {
			source.Fingerprint = domain.SimulationSourceFingerprint(source.RelativePath, source.SHA256)
		}
		profile.Corpus.Sources = replaceSourceByPath(profile.Corpus.Sources, source)
	}
	sortProfile(&profile)
	return &profile
}

func compactProfileForMerge(existing *domain.SimulationProfile) *domain.SimulationCompactProfile {
	compact := domain.CompactSimulationProfile(existing)
	if compact == nil {
		return nil
	}
	synthesis := compactSynthesisForMerge(domain.SimulationSynthesis{
		Style:            compact.Style,
		Lexicon:          compact.Lexicon,
		PlotDesign:       compact.PlotDesign,
		HookDesign:       compact.HookDesign,
		PacingDensity:    compact.PacingDensity,
		ReaderEngagement: compact.ReaderEngagement,
		RoleGuidance:     compact.RoleGuidance,
	})
	compact.Style = synthesis.Style
	compact.Lexicon = synthesis.Lexicon
	compact.PlotDesign = synthesis.PlotDesign
	compact.HookDesign = synthesis.HookDesign
	compact.PacingDensity = synthesis.PacingDensity
	compact.ReaderEngagement = synthesis.ReaderEngagement
	compact.RoleGuidance = synthesis.RoleGuidance
	return compact
}

func compactSourceReportsForMerge(reports []domain.SimulationSourceReport) []domain.SimulationSourceReport {
	if len(reports) == 0 {
		return nil
	}
	out := make([]domain.SimulationSourceReport, 0, len(reports))
	for _, report := range reports {
		out = append(out, domain.SimulationSourceReport{
			RelativePath:       report.RelativePath,
			SHA256:             report.SHA256,
			Fingerprint:        report.Fingerprint,
			AnalyzedAt:         report.AnalyzedAt,
			Title:              compactMergeText(report.Title, maxMergeReportTitleRunes),
			Summary:            compactMergeText(report.Summary, maxMergeReportSummaryRunes),
			StyleObservations:  compactMergeTextList(report.StyleObservations, maxMergeReportItemsPerList, maxMergeReportItemRunes),
			CommonWords:        compactMergeTextList(report.CommonWords, maxMergeReportItemsPerList, maxMergeReportItemRunes),
			PlotPatterns:       compactMergeTextList(report.PlotPatterns, maxMergeReportItemsPerList, maxMergeReportItemRunes),
			HookPatterns:       compactMergeTextList(report.HookPatterns, maxMergeReportItemsPerList, maxMergeReportItemRunes),
			PacingNotes:        compactMergeTextList(report.PacingNotes, maxMergeReportItemsPerList, maxMergeReportItemRunes),
			ReaderAppeal:       compactMergeTextList(report.ReaderAppeal, maxMergeReportItemsPerList, maxMergeReportItemRunes),
			ReusableTechniques: compactMergeTextList(report.ReusableTechniques, maxMergeReportItemsPerList, maxMergeReportItemRunes),
			Warnings:           compactMergeTextList(report.Warnings, maxMergeReportItemsPerList, maxMergeReportItemRunes),
		})
	}
	return out
}

func compactSynthesisForMerge(s domain.SimulationSynthesis) domain.SimulationSynthesis {
	return domain.SimulationSynthesis{
		Style: domain.SimulationStyle{
			NarrativeVoice: compactMergeTextList(s.Style.NarrativeVoice, maxMergeSynthesisItemsPerList, maxMergeSynthesisItemRunes),
			SentenceRhythm: compactMergeTextList(s.Style.SentenceRhythm, maxMergeSynthesisItemsPerList, maxMergeSynthesisItemRunes),
			ProseTexture:   compactMergeTextList(s.Style.ProseTexture, maxMergeSynthesisItemsPerList, maxMergeSynthesisItemRunes),
			Perspective:    compactMergeTextList(s.Style.Perspective, maxMergeSynthesisItemsPerList, maxMergeSynthesisItemRunes),
			Mood:           compactMergeTextList(s.Style.Mood, maxMergeSynthesisItemsPerList, maxMergeSynthesisItemRunes),
			DoNotCopy:      compactMergeTextList(s.Style.DoNotCopy, maxMergeSynthesisItemsPerList, maxMergeSynthesisItemRunes),
		},
		Lexicon: domain.SimulationLexicon{
			CommonWords:      compactMergeTextList(s.Lexicon.CommonWords, maxMergeSynthesisItemsPerList, maxMergeSynthesisItemRunes),
			EmotionWords:     compactMergeTextList(s.Lexicon.EmotionWords, maxMergeSynthesisItemsPerList, maxMergeSynthesisItemRunes),
			SceneWords:       compactMergeTextList(s.Lexicon.SceneWords, maxMergeSynthesisItemsPerList, maxMergeSynthesisItemRunes),
			TransitionWords:  compactMergeTextList(s.Lexicon.TransitionWords, maxMergeSynthesisItemsPerList, maxMergeSynthesisItemRunes),
			SignaturePhrases: compactMergeTextList(s.Lexicon.SignaturePhrases, maxMergeSynthesisItemsPerList, maxMergeSynthesisItemRunes),
		},
		PlotDesign: domain.SimulationPlotDesign{
			OpeningPatterns:      compactMergeTextList(s.PlotDesign.OpeningPatterns, maxMergeSynthesisItemsPerList, maxMergeSynthesisItemRunes),
			EscalationPatterns:   compactMergeTextList(s.PlotDesign.EscalationPatterns, maxMergeSynthesisItemsPerList, maxMergeSynthesisItemRunes),
			TurningPointPatterns: compactMergeTextList(s.PlotDesign.TurningPointPatterns, maxMergeSynthesisItemsPerList, maxMergeSynthesisItemRunes),
			PayoffPatterns:       compactMergeTextList(s.PlotDesign.PayoffPatterns, maxMergeSynthesisItemsPerList, maxMergeSynthesisItemRunes),
		},
		HookDesign: domain.SimulationHookDesign{
			HookTypes:           compactMergeTextList(s.HookDesign.HookTypes, maxMergeSynthesisItemsPerList, maxMergeSynthesisItemRunes),
			Placement:           compactMergeTextList(s.HookDesign.Placement, maxMergeSynthesisItemsPerList, maxMergeSynthesisItemRunes),
			CliffhangerPatterns: compactMergeTextList(s.HookDesign.CliffhangerPatterns, maxMergeSynthesisItemsPerList, maxMergeSynthesisItemRunes),
			PayoffRules:         compactMergeTextList(s.HookDesign.PayoffRules, maxMergeSynthesisItemsPerList, maxMergeSynthesisItemRunes),
		},
		PacingDensity: domain.SimulationPacingDensity{
			SceneDensity:        compactMergeTextList(s.PacingDensity.SceneDensity, maxMergeSynthesisItemsPerList, maxMergeSynthesisItemRunes),
			InformationRelease:  compactMergeTextList(s.PacingDensity.InformationRelease, maxMergeSynthesisItemsPerList, maxMergeSynthesisItemRunes),
			DialogueActionRatio: compactMergeTextList(s.PacingDensity.DialogueActionRatio, maxMergeSynthesisItemsPerList, maxMergeSynthesisItemRunes),
			CompressionRules:    compactMergeTextList(s.PacingDensity.CompressionRules, maxMergeSynthesisItemsPerList, maxMergeSynthesisItemRunes),
		},
		ReaderEngagement: domain.SimulationReaderEngagement{
			Methods:            compactMergeTextList(s.ReaderEngagement.Methods, maxMergeSynthesisItemsPerList, maxMergeSynthesisItemRunes),
			EmotionalDrivers:   compactMergeTextList(s.ReaderEngagement.EmotionalDrivers, maxMergeSynthesisItemsPerList, maxMergeSynthesisItemRunes),
			ProgressionRewards: compactMergeTextList(s.ReaderEngagement.ProgressionRewards, maxMergeSynthesisItemsPerList, maxMergeSynthesisItemRunes),
			AntiPatterns:       compactMergeTextList(s.ReaderEngagement.AntiPatterns, maxMergeSynthesisItemsPerList, maxMergeSynthesisItemRunes),
		},
		RoleGuidance: domain.SimulationRoleGuidance{
			Coordinator: compactMergeTextList(s.RoleGuidance.Coordinator, maxMergeSynthesisItemsPerList, maxMergeSynthesisItemRunes),
			Architect:   compactMergeTextList(s.RoleGuidance.Architect, maxMergeSynthesisItemsPerList, maxMergeSynthesisItemRunes),
			Writer:      compactMergeTextList(s.RoleGuidance.Writer, maxMergeSynthesisItemsPerList, maxMergeSynthesisItemRunes),
			Editor:      compactMergeTextList(s.RoleGuidance.Editor, maxMergeSynthesisItemsPerList, maxMergeSynthesisItemRunes),
		},
	}
}

func compactMergeTextList(items []string, maxItems int, maxRunes int) []string {
	if len(items) == 0 || maxItems <= 0 {
		return nil
	}
	out := make([]string, 0, maxItems)
	seen := make(map[string]struct{}, maxItems)
	for _, item := range items {
		item = compactMergeText(item, maxRunes)
		if item == "" {
			continue
		}
		key := strings.ToLower(item)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, item)
		if len(out) >= maxItems {
			break
		}
	}
	return out
}

func compactMergeText(text string, maxRunes int) string {
	text = strings.TrimSpace(text)
	if text == "" || maxRunes <= 0 {
		return text
	}
	runes := []rune(text)
	if len(runes) <= maxRunes {
		return text
	}
	return string(runes[:maxRunes]) + "...[truncated]"
}
