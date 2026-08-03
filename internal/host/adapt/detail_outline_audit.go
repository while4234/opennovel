package adapt

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/internal/domain"
)

const (
	detailOutlineAuditMaxTokens         = 3600
	detailOutlineGlobalAuditMaxTokens   = 8192
	detailOutlineAuditFormatMaxAttempts = 3
)

type detailRepairObserverContextKey struct{}

type detailRepairObserver func([]domain.AdaptationChapterPlan, error) error

func withDetailRepairObserver(ctx context.Context, observer detailRepairObserver) context.Context {
	if observer == nil {
		return ctx
	}
	return context.WithValue(ctx, detailRepairObserverContextKey{}, observer)
}

func observeDetailRepairFailure(ctx context.Context, chapters []domain.AdaptationChapterPlan, err error) error {
	if ctx == nil || err == nil {
		return nil
	}
	observer, _ := ctx.Value(detailRepairObserverContextKey{}).(detailRepairObserver)
	if observer != nil {
		return observer(chapters, err)
	}
	return nil
}

func persistDetailRepairFailureObserver(
	deps Deps,
	runtime *domain.AdaptationProposalRuntime,
	batch plannerSkeletonBatch,
) detailRepairObserver {
	return func(chapters []domain.AdaptationChapterPlan, repairErr error) error {
		if runtime == nil || repairErr == nil {
			return nil
		}
		previous := plannerRuntimeRawBatch(runtime, batch)
		attempts := 1
		consecutive := 1
		previousCategory := ""
		if previous != nil && previous.Audit != nil {
			attempts = previous.Audit.RepairAttempts + 1
			previousCategory = previous.Audit.LastErrorCategory
			if previousCategory == detailRepairErrorCategory(repairErr) {
				consecutive = previous.Audit.ConsecutiveCategoryFailures + 1
			}
		}
		category := detailRepairErrorCategory(repairErr)
		audit := &domain.AdaptationDetailBatchAudit{
			Version: domain.AdaptationDetailAuditVersion, Status: domain.AdaptationDetailAuditRepairPending,
			RepairAttempts: attempts, LastError: repairErr.Error(), LastErrorCategory: category,
			ExactErrorFingerprint:       detailOutlineTextSHA256(repairErr.Error()),
			CategoryFingerprint:         detailOutlineTextSHA256(fmt.Sprintf("%s:%d-%d", category, batch.TargetFrom, batch.TargetTo)),
			ConsecutiveCategoryFailures: consecutive,
			Findings: []domain.AdaptationDetailAuditFinding{{
				Code: category, Severity: "blocking", Blocking: true, Message: repairErr.Error(),
				RepairInstruction: "discard the failed candidate and regenerate the complete batch from the original clean contract",
				TargetChapters:    detailAuditChapterRange(batch.TargetFrom, batch.TargetTo),
			}},
		}
		if len(chapters) == 0 && previous != nil {
			chapters = previous.Chapters
		}
		upsertPlannerProposalRuntimeBatchWithAudit(runtime, batch, chapters, audit)
		return savePlannerProposalRuntime(deps, runtime)
	}
}

func detailRepairErrorCategory(err error) string {
	if err == nil {
		return ""
	}
	message := strings.ToLower(strings.TrimSpace(err.Error()))
	switch {
	case strings.Contains(message, "not assigned to detail batch"), strings.Contains(message, "unknown source event"):
		return "foreign_event_id"
	case strings.Contains(message, "promised by the parent plan but missing"), strings.Contains(message, "missing_mainline"):
		return "missing_event_id"
	case strings.Contains(message, "assigned to") && strings.Contains(message, "detail chapters"):
		return "duplicate_event_id"
	case strings.Contains(message, "duplicates outline beats"):
		return "duplicate_outline"
	case strings.Contains(message, "word_budget"), strings.Contains(message, "source_range") && strings.Contains(message, "runes"):
		return "budget_contract"
	case strings.Contains(message, "missing chapters"), strings.Contains(message, "no chapters"):
		return "missing_chapters"
	case strings.Contains(message, "json"), strings.Contains(message, "parse"), strings.Contains(message, "decode"):
		return "invalid_json"
	default:
		return "detail_contract"
	}
}

func detailBatchRepairMaxAttempts(deps Deps) int {
	structure := deps.structureRepairMaxAttempts()
	quality := deps.adaptationOutlineAuditRetryMaxAttempts()
	if structure <= 0 {
		return max(1, quality)
	}
	if quality <= 0 {
		return max(1, structure)
	}
	return max(1, min(structure, quality))
}

func plannerRuntimeRawBatch(runtime *domain.AdaptationProposalRuntime, batch plannerSkeletonBatch) *domain.AdaptationProposalRuntimeBatch {
	if runtime == nil {
		return nil
	}
	for index := range runtime.CompletedBatches {
		if plannerRuntimeBatchMatches(runtime.CompletedBatches[index], batch) {
			return &runtime.CompletedBatches[index]
		}
	}
	return nil
}

type detailAuditArtifact struct {
	ID             string `json:"id"`
	SHA256         string `json:"sha256"`
	Text           string `json:"text"`
	TargetChapters []int  `json:"target_chapters,omitempty"`
}

type detailAuditModelFinding struct {
	Code              string                                 `json:"code"`
	Severity          string                                 `json:"severity"`
	Message           string                                 `json:"message"`
	RepairInstruction string                                 `json:"repair_instruction"`
	TargetChapters    []int                                  `json:"target_chapters"`
	Evidence          []domain.AdaptationDetailAuditEvidence `json:"evidence"`
}

type detailAuditModelResponse struct {
	Verdict  string                    `json:"verdict"`
	Summary  string                    `json:"summary"`
	Findings []detailAuditModelFinding `json:"findings"`
}

type detailBatchAuditFailure struct {
	Findings []domain.AdaptationDetailAuditFinding
}

func (e *detailBatchAuditFailure) Error() string {
	if e == nil || len(e.Findings) == 0 {
		return "detail outline audit failed"
	}
	parts := make([]string, 0, len(e.Findings))
	for _, finding := range e.Findings {
		parts = append(parts, fmt.Sprintf("[%s] chapters=%v %s repair=%s", finding.Code, finding.TargetChapters, finding.Message, finding.RepairInstruction))
	}
	return "detail outline audit failed: " + strings.Join(parts, "; ")
}

func auditDetailBatch(
	ctx context.Context,
	deps Deps,
	opts ProposalOptions,
	reports []domain.AdaptationSourceReport,
	manifest *domain.AdaptationSourceManifest,
	skeleton plannerSkeleton,
	batch plannerSkeletonBatch,
	previous []domain.AdaptationChapterPlan,
	chapters []domain.AdaptationChapterPlan,
	emit ProgressEmitter,
	current int,
	total int,
) (*domain.AdaptationDetailBatchAudit, error) {
	contentSignature, contextSignature, inputSignature := detailBatchAuditSignatures(opts, reports, batch, previous, chapters)
	plan := detailAuditPrefixPlan(opts, skeleton, reports, previous, chapters)
	audit := &domain.AdaptationDetailBatchAudit{
		Version:          domain.AdaptationDetailAuditVersion,
		Status:           domain.AdaptationDetailAuditPending,
		ContentSignature: contentSignature,
		InputSignature:   inputSignature,
		ContextSignature: contextSignature,
	}
	if err := ValidateAdaptationDetailPrefixQuality(&plan, manifest, batch.SourceFrom, batch.SourceTo); err != nil {
		audit.Status = domain.AdaptationDetailAuditRepairPending
		audit.Findings = deterministicDetailAuditFindings(err, batch)
		audit.LastError = err.Error()
		return audit, &detailBatchAuditFailure{Findings: audit.Findings}
	}
	audit.DeterministicPassed = true
	model := deps.detailAuditModel()
	if model == nil {
		audit.LastError = "independent detail-outline auditor is unavailable"
		return audit, fmt.Errorf("%s", audit.LastError)
	}
	artifacts := detailBatchAuditArtifacts(opts, reports, skeleton, batch, previous, chapters)
	response, err := callDetailOutlineAuditor(ctx, deps, model, "detail_batch", artifacts, batch.TargetFrom, batch.TargetTo, emit, current, total)
	if err != nil {
		audit.LastError = err.Error()
		return audit, err
	}
	audit.Provider, audit.Model = detailAuditModelIdentity(model)
	audit.Findings = verifiedDetailAuditFindings(response.Findings, artifacts, batch.TargetFrom, batch.TargetTo)
	blocking := blockingDetailAuditFindings(audit.Findings)
	if len(blocking) > 0 {
		audit.Status = domain.AdaptationDetailAuditRepairPending
		audit.LastError = (&detailBatchAuditFailure{Findings: blocking}).Error()
		return audit, &detailBatchAuditFailure{Findings: blocking}
	}
	audit.SemanticPassed = true
	audit.Status = domain.AdaptationDetailAuditPassed
	audit.CheckedAt = time.Now().UTC().Format(time.RFC3339)
	return audit, nil
}

func detailBatchAuditSignatures(
	opts ProposalOptions,
	reports []domain.AdaptationSourceReport,
	batch plannerSkeletonBatch,
	previous []domain.AdaptationChapterPlan,
	chapters []domain.AdaptationChapterPlan,
) (string, string, string) {
	contentSignature := detailOutlineValueSignature(chapters)
	contextSignature := detailOutlineValueSignature(plannerPreviousChapterContexts(previous, adaptationPlannerContinuityChapterMax))
	inputSignature := detailOutlineValueSignature(struct {
		Content string
		Context string
		Brief   string
		Batch   plannerSkeletonBatch
		Reports []plannerSourceReportExcerpt
	}{contentSignature, contextSignature, opts.Brief, batch, plannerSourceReportExcerptsForDetail(reportsForPlannerDetailBatch(reports, batch), opts.Granularity, batch)})
	return contentSignature, contextSignature, inputSignature
}

func detailBatchAuditIsCurrent(
	audit *domain.AdaptationDetailBatchAudit,
	opts ProposalOptions,
	reports []domain.AdaptationSourceReport,
	batch plannerSkeletonBatch,
	previous []domain.AdaptationChapterPlan,
	chapters []domain.AdaptationChapterPlan,
) bool {
	if audit == nil || audit.Version != domain.AdaptationDetailAuditVersion || audit.Status != domain.AdaptationDetailAuditPassed || !audit.DeterministicPassed || !audit.SemanticPassed {
		return false
	}
	content, contextSignature, input := detailBatchAuditSignatures(opts, reports, batch, previous, chapters)
	return audit.ContentSignature == content && audit.ContextSignature == contextSignature && audit.InputSignature == input
}

func auditAndRepairDetailBatch(
	ctx context.Context,
	deps Deps,
	opts ProposalOptions,
	reports []domain.AdaptationSourceReport,
	manifest *domain.AdaptationSourceManifest,
	skeleton plannerSkeleton,
	batch plannerSkeletonBatch,
	previous []domain.AdaptationChapterPlan,
	chapters []domain.AdaptationChapterPlan,
	existingAudit *domain.AdaptationDetailBatchAudit,
	systemPrompt string,
	originalPrompt string,
	validate plannerBatchChapterValidatorFunc,
	runtime *domain.AdaptationProposalRuntime,
	emit ProgressEmitter,
	current int,
	total int,
	label string,
) ([]domain.AdaptationChapterPlan, *domain.AdaptationDetailBatchAudit, error) {
	if detailBatchAuditIsCurrent(existingAudit, opts, reports, batch, previous, chapters) {
		return chapters, cloneAdaptationDetailBatchAudit(existingAudit), nil
	}
	var migrated bool
	existingAudit, migrated = resetStaleCrossBatchOwnershipAudit(existingAudit, batch)
	if migrated {
		emitAdaptProgress(emit, StageAudit, current, total, fmt.Sprintf("%s检测到旧版跨批次事件归属审核记录，已按当前批次重新审核", label), nil)
	}
	var contractMigrated bool
	existingAudit, contractMigrated = resetStaleDetailBatchContractAudit(existingAudit, opts, reports, batch, previous, chapters)
	if contractMigrated {
		emitAdaptProgress(emit, StageAudit, current, total, fmt.Sprintf("%s事件归属合同已更新，已按新合同重新审核", label), nil)
	}
	repairAttempts := 0
	if existingAudit != nil {
		repairAttempts = existingAudit.RepairAttempts
	}
	pending := &domain.AdaptationDetailBatchAudit{
		Version: domain.AdaptationDetailAuditVersion, Status: domain.AdaptationDetailAuditPending,
		RepairAttempts: repairAttempts,
	}
	upsertPlannerProposalRuntimeBatchWithAudit(runtime, batch, chapters, pending)
	if err := savePlannerProposalRuntime(deps, runtime); err != nil {
		return nil, pending, fmt.Errorf("save pending detail batch audit: %w", err)
	}
	maxRepairs := deps.adaptationOutlineAuditRetryMaxAttempts()
	storedRepairPending := existingAudit != nil && existingAudit.Status == domain.AdaptationDetailAuditRepairPending &&
		existingAudit.ContentSignature == detailOutlineValueSignature(chapters) && len(blockingDetailAuditFindings(existingAudit.Findings)) > 0
	lastBlockingSignature := ""
	unchangedBlockingRounds := 0
	for {
		var audit *domain.AdaptationDetailBatchAudit
		var auditErr error
		if storedRepairPending {
			audit = cloneAdaptationDetailBatchAudit(existingAudit)
			auditErr = &detailBatchAuditFailure{Findings: blockingDetailAuditFindings(existingAudit.Findings)}
			storedRepairPending = false
		} else {
			audit, auditErr = auditDetailBatch(ctx, deps, opts, reports, manifest, skeleton, batch, previous, chapters, emit, current, total)
		}
		audit.RepairAttempts = repairAttempts
		upsertPlannerProposalRuntimeBatchWithAudit(runtime, batch, chapters, audit)
		if saveErr := savePlannerProposalRuntime(deps, runtime); saveErr != nil {
			return nil, audit, fmt.Errorf("save detail batch audit result: %w", saveErr)
		}
		if auditErr == nil {
			if repairAttempts > 0 {
				emitAdaptProgress(emit, StageAudit, current, total, fmt.Sprintf("%s复审通过，本批次修复完成", label), nil)
			} else {
				emitAdaptProgress(emit, StageAudit, current, total, fmt.Sprintf("%s双重审核通过", label), nil)
			}
			return chapters, audit, nil
		}
		var contentErr *detailBatchAuditFailure
		if !errors.As(auditErr, &contentErr) {
			// Transport and provider failures keep the candidate pending and do
			// not consume the content-repair budget.
			return nil, audit, auditErr
		}
		blockingSignature := blockingDetailAuditFingerprint(contentErr.Findings)
		if blockingSignature == lastBlockingSignature {
			unchangedBlockingRounds++
		} else {
			lastBlockingSignature = blockingSignature
			unchangedBlockingRounds = 0
		}
		if unchangedBlockingRounds >= 2 {
			return nil, audit, fmt.Errorf("%s made no audit progress after %d content repair attempts: %w", label, repairAttempts, auditErr)
		}
		if repairAttempts >= maxRepairs {
			return nil, audit, fmt.Errorf("%s exhausted %d content repair attempts: %w", label, maxRepairs, auditErr)
		}
		repairAttempts++
		emitAdaptProgress(emit, StageAudit, current, total, fmt.Sprintf("%s审核发现 %d 项阻断问题，准备第 %d/%d 次整批修复", label, len(contentErr.Findings), repairAttempts, maxRepairs), auditErr)
		previousText, _ := json.Marshal(struct {
			Chapters []domain.AdaptationChapterPlan `json:"chapters"`
		}{chapters})
		repairedText, repairErr := repairPlannerBatchText(
			ctx, deps.modelForStage("detail_outline"), systemPrompt, originalPrompt, string(previousText), batch,
			auditErr, true, emit, current, total, label, deps.modelCallMaxAttempts(),
		)
		if repairErr != nil {
			return nil, audit, repairErr
		}
		repaired, collectErr := collectPlannerBatchChaptersWithRepair(
			ctx, deps.modelForStage("detail_outline"), systemPrompt, originalPrompt, repairedText, batch, validate,
			previous, emit, current, total, label+"修复稿", detailBatchRepairMaxAttempts(deps), deps.modelCallMaxAttempts(),
		)
		if collectErr != nil {
			return nil, audit, collectErr
		}
		if detailOutlineValueSignature(repaired) == detailOutlineValueSignature(chapters) {
			audit.Status = domain.AdaptationDetailAuditRepairPending
			audit.RepairAttempts = repairAttempts
			audit.LastError = "repair output is unchanged"
			upsertPlannerProposalRuntimeBatchWithAudit(runtime, batch, chapters, audit)
			if saveErr := savePlannerProposalRuntime(deps, runtime); saveErr != nil {
				return nil, audit, saveErr
			}
			if repairAttempts >= maxRepairs {
				return nil, audit, fmt.Errorf("%s repair output remained unchanged", label)
			}
			continue
		}
		chapters = repaired
		pending = &domain.AdaptationDetailBatchAudit{
			Version: domain.AdaptationDetailAuditVersion, Status: domain.AdaptationDetailAuditPending,
			RepairAttempts: repairAttempts,
		}
		upsertPlannerProposalRuntimeBatchWithAudit(runtime, batch, chapters, pending)
		if saveErr := savePlannerProposalRuntime(deps, runtime); saveErr != nil {
			return nil, pending, saveErr
		}
		emitAdaptProgress(emit, StageAudit, current, total, fmt.Sprintf("%s修复稿已生成，正在复审", label), nil)
	}
}

func detailAuditPrefixPlan(
	opts ProposalOptions,
	skeleton plannerSkeleton,
	reports []domain.AdaptationSourceReport,
	previous []domain.AdaptationChapterPlan,
	chapters []domain.AdaptationChapterPlan,
) domain.AdaptationPlan {
	plan := domain.AdaptationPlan{
		Granularity:       opts.Granularity,
		RewritePolicy:     opts.RewritePolicy,
		Brief:             opts.Brief,
		WordTolerance:     opts.WordTolerance,
		Volumes:           adaptationVolumesFromSkeleton(skeleton),
		SourceEvents:      sourceEventsFromReports(reports),
		MainlineRules:     append([]string(nil), skeleton.MainlineRules...),
		RelationshipGoals: append([]string(nil), skeleton.RelationshipGoals...),
		Chapters:          appendClonedAdaptationChapters(previous, chapters),
	}
	if domain.NormalizeAdaptationGranularity(opts.Granularity) == domain.AdaptationGranularityFree {
		buildFreeTargetEventLedger(&plan)
	}
	return plan
}

func appendClonedAdaptationChapters(groups ...[]domain.AdaptationChapterPlan) []domain.AdaptationChapterPlan {
	var out []domain.AdaptationChapterPlan
	for _, group := range groups {
		for _, chapter := range group {
			out = append(out, cloneAdaptationChapterPlan(chapter))
		}
	}
	return out
}

func deterministicDetailAuditFindings(err error, batch plannerSkeletonBatch) []domain.AdaptationDetailAuditFinding {
	qualityErr, ok := err.(*AdaptationOutlineQualityError)
	if !ok {
		return []domain.AdaptationDetailAuditFinding{{
			Code: "deterministic_detail_contract", Severity: "blocking", Blocking: true,
			Message: err.Error(), RepairInstruction: "regenerate the complete detail batch and satisfy the deterministic contract",
			TargetChapters: detailAuditChapterRange(batch.TargetFrom, batch.TargetTo),
		}}
	}
	findings := make([]domain.AdaptationDetailAuditFinding, 0, len(qualityErr.Issues))
	for _, issue := range qualityErr.Issues {
		targets := detailAuditTargetsInRange(outlineIssueTargetChapters(issue), batch.TargetFrom, batch.TargetTo)
		if len(targets) == 0 {
			targets = detailAuditChapterRange(batch.TargetFrom, batch.TargetTo)
		}
		findings = append(findings, domain.AdaptationDetailAuditFinding{
			Code: issue.Code, Severity: "blocking", Blocking: true, Message: issue.Detail,
			RepairInstruction: "repair the complete batch so the reported contract violation no longer exists",
			TargetChapters:    targets,
		})
	}
	return findings
}

// resetStaleCrossBatchOwnershipAudit migrates only an audit record produced
// before duplicate-event issues carried every owner. In that old shape a
// detail batch could be told to repair a chapter from an earlier, immutable
// batch, so spending more retries could never resolve the reported conflict.
// The candidate itself remains intact; the current batch is re-audited with
// the corrected ownership scope and a fresh, narrowly justified budget.
func resetStaleCrossBatchOwnershipAudit(
	audit *domain.AdaptationDetailBatchAudit,
	batch plannerSkeletonBatch,
) (*domain.AdaptationDetailBatchAudit, bool) {
	if audit == nil || audit.Status != domain.AdaptationDetailAuditRepairPending {
		return audit, false
	}
	for _, finding := range audit.Findings {
		if finding.Code != outlineQualityIssueArcDuplicateEvent ||
			len(detailAuditTargetsInRange(finding.TargetChapters, batch.TargetFrom, batch.TargetTo)) > 0 {
			continue
		}
		return resetDetailBatchAuditForReaudit(audit), true
	}
	return audit, false
}

// resetStaleDetailBatchContractAudit gives a candidate a fresh repair budget
// only when the persisted audit was produced under a different deterministic
// batch contract. This is used by the legacy ownership migration; ordinary
// model failures under the same contract keep their bounded retry budget.
func resetStaleDetailBatchContractAudit(
	audit *domain.AdaptationDetailBatchAudit,
	opts ProposalOptions,
	reports []domain.AdaptationSourceReport,
	batch plannerSkeletonBatch,
	previous []domain.AdaptationChapterPlan,
	chapters []domain.AdaptationChapterPlan,
) (*domain.AdaptationDetailBatchAudit, bool) {
	if audit == nil || audit.Status != domain.AdaptationDetailAuditRepairPending || strings.TrimSpace(audit.InputSignature) == "" {
		return audit, false
	}
	_, _, currentInputSignature := detailBatchAuditSignatures(opts, reports, batch, previous, chapters)
	if audit.InputSignature == currentInputSignature {
		return audit, false
	}
	return resetDetailBatchAuditForReaudit(audit), true
}

func resetDetailBatchAuditForReaudit(audit *domain.AdaptationDetailBatchAudit) *domain.AdaptationDetailBatchAudit {
	migrated := cloneAdaptationDetailBatchAudit(audit)
	migrated.Status = domain.AdaptationDetailAuditPending
	migrated.DeterministicPassed = false
	migrated.SemanticPassed = false
	migrated.RepairAttempts = 0
	migrated.LastError = ""
	migrated.LastErrorCategory = ""
	migrated.ExactErrorFingerprint = ""
	migrated.CategoryFingerprint = ""
	migrated.ConsecutiveCategoryFailures = 0
	migrated.Findings = nil
	return migrated
}

func detailBatchAuditArtifacts(
	opts ProposalOptions,
	reports []domain.AdaptationSourceReport,
	skeleton plannerSkeleton,
	batch plannerSkeletonBatch,
	previous []domain.AdaptationChapterPlan,
	chapters []domain.AdaptationChapterPlan,
) []detailAuditArtifact {
	values := []struct {
		id      string
		value   any
		targets []int
	}{
		{"contract", struct {
			Brief         string   `json:"brief"`
			Granularity   string   `json:"granularity"`
			RewritePolicy string   `json:"rewrite_policy"`
			Rules         []string `json:"mainline_rules,omitempty"`
		}{opts.Brief, opts.Granularity, opts.RewritePolicy, skeleton.MainlineRules}, nil},
		{"source", plannerSourceReportExcerptsForDetail(reportsForPlannerDetailBatch(reports, batch), opts.Granularity, batch), nil},
		{"skeleton", plannerSkeletonForDetailPrompt(skeleton, batch), nil},
		{"previous", plannerPreviousChapterContexts(previous, adaptationPlannerContinuityChapterMax), nil},
		{"candidate", chapters, detailAuditChapterRange(batch.TargetFrom, batch.TargetTo)},
	}
	artifacts := make([]detailAuditArtifact, 0, len(values))
	for _, value := range values {
		data, _ := json.Marshal(value.value)
		text := string(data)
		artifacts = append(artifacts, detailAuditArtifact{ID: value.id, SHA256: detailOutlineTextSHA256(text), Text: text, TargetChapters: value.targets})
	}
	return artifacts
}

func callDetailOutlineAuditor(
	ctx context.Context,
	deps Deps,
	model interface {
		Generate(context.Context, []agentcore.Message, []agentcore.ToolSpec, ...agentcore.CallOption) (*agentcore.LLMResponse, error)
	},
	scope string,
	artifacts []detailAuditArtifact,
	targetFrom int,
	targetTo int,
	emit ProgressEmitter,
	current int,
	total int,
) (detailAuditModelResponse, error) {
	payload, _ := json.Marshal(struct {
		Scope      string                `json:"scope"`
		TargetFrom int                   `json:"target_from"`
		TargetTo   int                   `json:"target_to"`
		Artifacts  []detailAuditArtifact `json:"artifacts"`
	}{scope, targetFrom, targetTo, artifacts})
	systemPrompt := detailOutlineAuditorSystemPrompt()
	label := fmt.Sprintf("章节详情第 %d-%d 章独立审核", targetFrom, targetTo)
	formatAttempts := min(max(1, deps.structureRepairMaxAttempts()), detailOutlineAuditFormatMaxAttempts)
	maxTokens := detailOutlineAuditOutputMaxTokens(scope)
	var lastDecodeErr error
	for attempt := 1; attempt <= formatAttempts; attempt++ {
		text, err := generatePlannerTextForStage(
			ctx, StageAudit, model, systemPrompt, string(payload), maxTokens,
			emit, current, total, label, deps.modelCallMaxAttempts(),
		)
		if err != nil {
			return detailAuditModelResponse{}, fmt.Errorf("detail outline auditor transport failure: %w", err)
		}
		response, decodeErr := decodeDetailOutlineAuditResponse(text)
		if decodeErr == nil {
			return response, nil
		}
		lastDecodeErr = decodeErr
		if attempt < formatAttempts {
			emitAdaptProgress(
				emit, StageAudit, current, total,
				fmt.Sprintf("%s返回格式无效，已丢弃该返回，使用干净上下文重试 %d/%d", label, attempt+1, formatAttempts),
				decodeErr,
			)
		}
	}
	return detailAuditModelResponse{}, fmt.Errorf("decode detail outline audit response after %d clean attempts: %w", formatAttempts, lastDecodeErr)
}

func detailOutlineAuditOutputMaxTokens(scope string) int {
	if scope == "global" {
		return detailOutlineGlobalAuditMaxTokens
	}
	return detailOutlineAuditMaxTokens
}

func decodeDetailOutlineAuditResponse(text string) (detailAuditModelResponse, error) {
	var firstDecodeErr error
	for offset, char := range text {
		if char != '{' {
			continue
		}
		var response detailAuditModelResponse
		decoder := json.NewDecoder(strings.NewReader(text[offset:]))
		if err := decoder.Decode(&response); err != nil {
			if firstDecodeErr == nil {
				firstDecodeErr = err
			}
			continue
		}
		response.Verdict = strings.ToLower(strings.TrimSpace(response.Verdict))
		if response.Verdict != "pass" && response.Verdict != "fail" {
			if firstDecodeErr == nil {
				firstDecodeErr = fmt.Errorf("missing or invalid verdict %q", response.Verdict)
			}
			continue
		}
		return response, nil
	}
	if firstDecodeErr == nil {
		firstDecodeErr = errors.New("no JSON object found")
	}
	return detailAuditModelResponse{}, firstDecodeErr
}

func detailOutlineAuditorSystemPrompt() string {
	return "You are the independent pre-writing auditor for novel adaptation chapter outlines. " +
		"You did not generate the candidate. Audit only the supplied target range. Check source fidelity, event ownership, causality, timeline, character and relationship state, repeated plot beats, setup/payoff placement, required and forbidden moves, scene capacity, and the handoff from previous chapters. " +
		"Return one JSON object only: {\"verdict\":\"pass|fail\",\"summary\":string,\"findings\":[{\"code\":string,\"severity\":\"warning|blocking\",\"message\":string,\"repair_instruction\":string,\"target_chapters\":[int],\"evidence\":[{\"artifact_id\":string,\"artifact_sha256\":string,\"quote\":string,\"from_rune\":int,\"to_rune\":int}]}]}. " +
		"Every blocking finding must name at least one chapter in the requested range and quote exact supplied text with absolute rune offsets. Use two evidence items when claiming a contradiction or duplication. Absence-only or subjective claims without verifiable evidence must be warnings. Do not invent facts and do not repair the outline yourself."
}

func verifiedDetailAuditFindings(
	modelFindings []detailAuditModelFinding,
	artifacts []detailAuditArtifact,
	targetFrom int,
	targetTo int,
) []domain.AdaptationDetailAuditFinding {
	byID := make(map[string]detailAuditArtifact, len(artifacts))
	for _, artifact := range artifacts {
		byID[artifact.ID] = artifact
	}
	findings := make([]domain.AdaptationDetailAuditFinding, 0, len(modelFindings))
	for _, modelFinding := range modelFindings {
		finding := domain.AdaptationDetailAuditFinding{
			Code: strings.TrimSpace(modelFinding.Code), Severity: strings.ToLower(strings.TrimSpace(modelFinding.Severity)),
			Message: strings.TrimSpace(modelFinding.Message), RepairInstruction: strings.TrimSpace(modelFinding.RepairInstruction),
			TargetChapters: detailAuditTargetsInRange(modelFinding.TargetChapters, targetFrom, targetTo),
		}
		allEvidenceValid := len(modelFinding.Evidence) > 0
		for _, evidence := range modelFinding.Evidence {
			artifact, ok := byID[evidence.ArtifactID]
			runes := []rune(artifact.Text)
			valid := ok && evidence.ArtifactSHA256 == artifact.SHA256 && evidence.FromRune >= 0 && evidence.ToRune > evidence.FromRune && evidence.ToRune <= len(runes) && string(runes[evidence.FromRune:evidence.ToRune]) == evidence.Quote
			if !valid {
				allEvidenceValid = false
				continue
			}
			finding.Evidence = append(finding.Evidence, evidence)
		}
		requestedBlocking := finding.Severity == "blocking" || finding.Severity == "critical"
		finding.Blocking = requestedBlocking && allEvidenceValid && len(finding.TargetChapters) > 0
		if requestedBlocking && !finding.Blocking {
			finding.Severity = "warning"
			finding.Message = strings.TrimSpace(finding.Message + " (downgraded: blocking evidence or target scope was not verifiable)")
		}
		findings = append(findings, finding)
	}
	return findings
}

func blockingDetailAuditFindings(findings []domain.AdaptationDetailAuditFinding) []domain.AdaptationDetailAuditFinding {
	var out []domain.AdaptationDetailAuditFinding
	for _, finding := range findings {
		if finding.Blocking {
			out = append(out, finding)
		}
	}
	return out
}

func blockingDetailAuditFingerprint(findings []domain.AdaptationDetailAuditFinding) string {
	type stableFinding struct {
		Code              string
		Severity          string
		Message           string
		RepairInstruction string
		TargetChapters    []int
		Blocking          bool
	}
	stable := make([]stableFinding, 0, len(findings))
	for _, finding := range findings {
		stable = append(stable, stableFinding{
			Code: finding.Code, Severity: finding.Severity, Message: finding.Message,
			RepairInstruction: finding.RepairInstruction,
			TargetChapters:    append([]int(nil), finding.TargetChapters...),
			Blocking:          finding.Blocking,
		})
	}
	return detailOutlineValueSignature(stable)
}

func detailAuditTargetsInRange(values []int, from int, to int) []int {
	seen := make(map[int]bool)
	var out []int
	for _, value := range values {
		if value < from || value > to || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	sort.Ints(out)
	return out
}

func detailAuditChapterRange(from int, to int) []int {
	if from <= 0 || to < from {
		return nil
	}
	out := make([]int, 0, to-from+1)
	for chapter := from; chapter <= to; chapter++ {
		out = append(out, chapter)
	}
	return out
}

func detailOutlineValueSignature(value any) string {
	data, _ := json.Marshal(value)
	return detailOutlineTextSHA256(string(data))
}

func detailOutlineTextSHA256(text string) string {
	digest := sha256.Sum256([]byte(text))
	return hex.EncodeToString(digest[:])
}

func detailAuditModelIdentity(model any) (string, string) {
	var provider, name string
	if value, ok := model.(interface{ ProviderName() string }); ok {
		provider = strings.TrimSpace(value.ProviderName())
	}
	if value, ok := model.(interface{ ModelName() string }); ok {
		name = strings.TrimSpace(value.ModelName())
	}
	return provider, name
}

func markPlannerRuntimeQualityIssues(
	runtime *domain.AdaptationProposalRuntime,
	skeleton plannerSkeleton,
	qualityErr *AdaptationOutlineQualityError,
) int {
	if runtime == nil || qualityErr == nil {
		return 0
	}
	detailBatches := plannerDetailBatches(skeleton.Batches, adaptationPlannerRecommendedBatchMax)
	affected := make(map[[2]int][]domain.AdaptationDetailAuditFinding)
	for _, issue := range qualityErr.Issues {
		for _, batch := range detailBatches {
			if !outlineIssueMatchesDetailBatch(issue, batch, runtime) {
				continue
			}
			key := [2]int{batch.TargetFrom, batch.TargetTo}
			targets := detailAuditTargetsInRange(outlineIssueTargetChapters(issue), batch.TargetFrom, batch.TargetTo)
			if len(targets) == 0 {
				targets = detailAuditChapterRange(batch.TargetFrom, batch.TargetTo)
			}
			affected[key] = append(affected[key], domain.AdaptationDetailAuditFinding{
				Code: issue.Code, Severity: "blocking", Blocking: true, Message: issue.Detail,
				RepairInstruction: "regenerate this detail batch and pass the same full audit again",
				TargetChapters:    targets,
			})
		}
	}
	count := 0
	for index := range runtime.CompletedBatches {
		completed := &runtime.CompletedBatches[index]
		findings := affected[[2]int{completed.TargetFrom, completed.TargetTo}]
		if len(findings) == 0 {
			continue
		}
		if completed.Audit == nil {
			completed.Audit = &domain.AdaptationDetailBatchAudit{Version: domain.AdaptationDetailAuditVersion}
		}
		completed.Audit.Status = domain.AdaptationDetailAuditRepairPending
		completed.Audit.SemanticPassed = false
		completed.Audit.Findings = findings
		completed.Audit.LastError = (&detailBatchAuditFailure{Findings: findings}).Error()
		count++
	}
	return count
}

func outlineIssueMatchesDetailBatch(
	issue AdaptationOutlineQualityIssue,
	batch plannerSkeletonBatch,
	runtime *domain.AdaptationProposalRuntime,
) bool {
	for _, target := range outlineIssueTargetChapters(issue) {
		if target >= batch.TargetFrom && target <= batch.TargetTo {
			return true
		}
	}
	if issue.SourceChapter >= batch.SourceFrom && issue.SourceChapter <= batch.SourceTo {
		return true
	}
	if issue.EventID != "" {
		for _, eventID := range batch.MainlineEventIDs {
			if strings.TrimSpace(eventID) == strings.TrimSpace(issue.EventID) {
				return true
			}
		}
		for _, completed := range runtime.CompletedBatches {
			if completed.TargetFrom != batch.TargetFrom || completed.TargetTo != batch.TargetTo {
				continue
			}
			for _, chapter := range completed.Chapters {
				if detailAuditContainsString(chapter.EventIDs, issue.EventID) || detailAuditContainsString(chapter.PreserveEvents, issue.EventID) {
					return true
				}
			}
		}
	}
	return false
}

func detailAuditContainsString(values []string, wanted string) bool {
	wanted = strings.TrimSpace(wanted)
	for _, value := range values {
		if strings.TrimSpace(value) == wanted {
			return true
		}
	}
	return false
}

func runClosedDetailScopeAudits(
	ctx context.Context,
	deps Deps,
	runtime *domain.AdaptationProposalRuntime,
	opts ProposalOptions,
	reports []domain.AdaptationSourceReport,
	skeleton plannerSkeleton,
	closedBatch plannerSkeletonBatch,
	accepted []domain.AdaptationChapterPlan,
	emit ProgressEmitter,
	current int,
	total int,
) error {
	parent, ok := detailAuditParentBatch(skeleton, closedBatch)
	if !ok || closedBatch.TargetTo != parent.TargetTo {
		return nil
	}
	if err := ensureDetailScopeCheckpoint(ctx, deps, runtime, "parent", fmt.Sprintf("parent-%04d-%04d", parent.TargetFrom, parent.TargetTo), opts, reports, skeleton, parent, accepted, emit, current, total); err != nil {
		return err
	}
	for _, volume := range adaptationVolumesFromSkeleton(skeleton) {
		if volume.TargetTo != closedBatch.TargetTo {
			continue
		}
		volumeBatch := plannerSkeletonBatch{
			Index: volume.Index, Title: volume.Title, Theme: volume.Theme, Goal: volume.Goal, Summary: volume.Summary,
			TargetFrom: volume.TargetFrom, TargetTo: volume.TargetTo, SourceFrom: volume.SourceFrom, SourceTo: volume.SourceTo,
			MainlineEventIDs: append([]string(nil), volume.MainlineEventIDs...),
		}
		volumeBatch.AllowedEventIDs = adaptationEventIDs(sourceEventsInRange(reports, volume.SourceFrom, volume.SourceTo))
		if err := ensureDetailScopeCheckpoint(ctx, deps, runtime, "volume", fmt.Sprintf("volume-%04d", volume.Index), opts, reports, skeleton, volumeBatch, accepted, emit, current, total); err != nil {
			return err
		}
	}
	return nil
}

func detailAuditParentBatch(skeleton plannerSkeleton, detail plannerSkeletonBatch) (plannerSkeletonBatch, bool) {
	for _, parent := range skeleton.Batches {
		if parent.TargetFrom <= detail.TargetFrom && parent.TargetTo >= detail.TargetTo {
			return parent, true
		}
	}
	return plannerSkeletonBatch{}, false
}

func ensureDetailScopeCheckpoint(
	ctx context.Context,
	deps Deps,
	runtime *domain.AdaptationProposalRuntime,
	kind string,
	id string,
	opts ProposalOptions,
	reports []domain.AdaptationSourceReport,
	skeleton plannerSkeleton,
	scope plannerSkeletonBatch,
	accepted []domain.AdaptationChapterPlan,
	emit ProgressEmitter,
	current int,
	total int,
) error {
	chapters := detailAuditChaptersInRange(accepted, scope.TargetFrom, scope.TargetTo)
	previous := detailAuditChaptersBefore(accepted, scope.TargetFrom)
	artifacts := detailBatchAuditArtifacts(opts, reportsForPlannerDetailBatch(reports, scope), skeleton, scope, previous, chapters)
	return ensureDetailCheckpointWithArtifacts(ctx, deps, runtime, kind, id, scope.TargetFrom, scope.TargetTo, artifacts, emit, current, total)
}

func ensureGlobalDetailAuditCheckpoint(
	ctx context.Context,
	deps Deps,
	runtime *domain.AdaptationProposalRuntime,
	opts ProposalOptions,
	skeleton plannerSkeleton,
	chapters []domain.AdaptationChapterPlan,
	emit ProgressEmitter,
) error {
	boundaryChapters := detailAuditVolumeBoundaryChapters(skeleton, chapters)
	values := []struct {
		id    string
		value any
	}{
		{"contract", struct {
			Brief             string
			MainlineRules     []string
			RelationshipGoals []string
			Volumes           []domain.AdaptationVolumePlan
		}{opts.Brief, skeleton.MainlineRules, skeleton.RelationshipGoals, adaptationVolumesFromSkeleton(skeleton)}},
		{"layered_checkpoints", detailAuditNonGlobalCheckpoints(runtime)},
		{"volume_boundaries", boundaryChapters},
	}
	artifacts := make([]detailAuditArtifact, 0, len(values))
	for _, value := range values {
		data, _ := json.Marshal(value.value)
		text := string(data)
		artifacts = append(artifacts, detailAuditArtifact{ID: value.id, SHA256: detailOutlineTextSHA256(text), Text: text})
	}
	return ensureDetailCheckpointWithArtifacts(ctx, deps, runtime, "global", "global-outline", 1, len(chapters), artifacts, emit, len(chapters), len(chapters))
}

func detailAuditNonGlobalCheckpoints(runtime *domain.AdaptationProposalRuntime) []domain.AdaptationDetailAuditCheckpoint {
	if runtime == nil {
		return nil
	}
	var out []domain.AdaptationDetailAuditCheckpoint
	for _, checkpoint := range runtime.AuditCheckpoints {
		if checkpoint.Kind != "global" {
			out = append(out, checkpoint)
		}
	}
	return out
}

func ensureDetailCheckpointWithArtifacts(
	ctx context.Context,
	deps Deps,
	runtime *domain.AdaptationProposalRuntime,
	kind string,
	id string,
	targetFrom int,
	targetTo int,
	artifacts []detailAuditArtifact,
	emit ProgressEmitter,
	current int,
	total int,
) error {
	signature := detailOutlineValueSignature(artifacts)
	if existing := detailAuditCheckpoint(runtime, kind, id); existing != nil && existing.Status == domain.AdaptationDetailAuditPassed && existing.InputSignature == signature {
		return nil
	}
	response, err := callDetailOutlineAuditor(ctx, deps, deps.detailAuditModel(), kind, artifacts, targetFrom, targetTo, emit, current, total)
	checkpoint := domain.AdaptationDetailAuditCheckpoint{
		Version: domain.AdaptationDetailAuditVersion, Kind: kind, ID: id, Status: domain.AdaptationDetailAuditPending,
		TargetFrom: targetFrom, TargetTo: targetTo, InputSignature: signature, Summary: strings.TrimSpace(response.Summary),
	}
	checkpoint.Provider, checkpoint.Model = detailAuditModelIdentity(deps.detailAuditModel())
	if err != nil {
		upsertDetailAuditCheckpoint(runtime, checkpoint)
		_ = savePlannerProposalRuntime(deps, runtime)
		return err
	}
	checkpoint.Findings = verifiedDetailAuditFindings(response.Findings, artifacts, targetFrom, targetTo)
	blocking := blockingDetailAuditFindings(checkpoint.Findings)
	if len(blocking) > 0 {
		checkpoint.Status = domain.AdaptationDetailAuditRepairPending
		upsertDetailAuditCheckpoint(runtime, checkpoint)
		markRuntimeBatchesForSemanticFindings(runtime, blocking)
		_ = savePlannerProposalRuntime(deps, runtime)
		return detailAuditFindingsAsQualityError(blocking)
	}
	checkpoint.Status = domain.AdaptationDetailAuditPassed
	checkpoint.CheckedAt = time.Now().UTC().Format(time.RFC3339)
	upsertDetailAuditCheckpoint(runtime, checkpoint)
	if err := savePlannerProposalRuntime(deps, runtime); err != nil {
		return err
	}
	emitAdaptProgress(emit, StageAudit, current, total, fmt.Sprintf("%s 级章节详纲审核通过：第 %d-%d 章", kind, targetFrom, targetTo), nil)
	return nil
}

func detailAuditCheckpoint(runtime *domain.AdaptationProposalRuntime, kind string, id string) *domain.AdaptationDetailAuditCheckpoint {
	if runtime == nil {
		return nil
	}
	for index := range runtime.AuditCheckpoints {
		checkpoint := &runtime.AuditCheckpoints[index]
		if checkpoint.Kind == kind && checkpoint.ID == id {
			return checkpoint
		}
	}
	return nil
}

func upsertDetailAuditCheckpoint(runtime *domain.AdaptationProposalRuntime, checkpoint domain.AdaptationDetailAuditCheckpoint) {
	if runtime == nil {
		return
	}
	for index := range runtime.AuditCheckpoints {
		if runtime.AuditCheckpoints[index].Kind == checkpoint.Kind && runtime.AuditCheckpoints[index].ID == checkpoint.ID {
			runtime.AuditCheckpoints[index] = checkpoint
			return
		}
	}
	runtime.AuditCheckpoints = append(runtime.AuditCheckpoints, checkpoint)
}

func markRuntimeBatchesForSemanticFindings(runtime *domain.AdaptationProposalRuntime, findings []domain.AdaptationDetailAuditFinding) {
	for index := range runtime.CompletedBatches {
		batch := &runtime.CompletedBatches[index]
		var owned []domain.AdaptationDetailAuditFinding
		for _, finding := range findings {
			if len(detailAuditTargetsInRange(finding.TargetChapters, batch.TargetFrom, batch.TargetTo)) > 0 {
				owned = append(owned, finding)
			}
		}
		if len(owned) == 0 {
			continue
		}
		if batch.Audit == nil {
			batch.Audit = &domain.AdaptationDetailBatchAudit{Version: domain.AdaptationDetailAuditVersion}
		}
		batch.Audit.Status = domain.AdaptationDetailAuditRepairPending
		batch.Audit.SemanticPassed = false
		batch.Audit.Findings = owned
		batch.Audit.LastError = (&detailBatchAuditFailure{Findings: owned}).Error()
	}
}

func detailAuditFindingsAsQualityError(findings []domain.AdaptationDetailAuditFinding) error {
	issues := make([]AdaptationOutlineQualityIssue, 0, len(findings))
	for _, finding := range findings {
		target := 0
		if len(finding.TargetChapters) > 0 {
			target = finding.TargetChapters[0]
		}
		issues = append(issues, AdaptationOutlineQualityIssue{Code: finding.Code, Detail: finding.Message, TargetChapter: target})
	}
	return &AdaptationOutlineQualityError{Issues: issues}
}

func detailAuditChaptersInRange(chapters []domain.AdaptationChapterPlan, from int, to int) []domain.AdaptationChapterPlan {
	var out []domain.AdaptationChapterPlan
	for _, chapter := range chapters {
		if chapter.Chapter >= from && chapter.Chapter <= to {
			out = append(out, cloneAdaptationChapterPlan(chapter))
		}
	}
	return out
}

func detailAuditChaptersBefore(chapters []domain.AdaptationChapterPlan, before int) []domain.AdaptationChapterPlan {
	var out []domain.AdaptationChapterPlan
	for _, chapter := range chapters {
		if chapter.Chapter < before {
			out = append(out, cloneAdaptationChapterPlan(chapter))
		}
	}
	return out
}

func detailAuditVolumeBoundaryChapters(skeleton plannerSkeleton, chapters []domain.AdaptationChapterPlan) []domain.AdaptationChapterPlan {
	wanted := make(map[int]bool)
	for _, volume := range adaptationVolumesFromSkeleton(skeleton) {
		wanted[volume.TargetFrom] = true
		wanted[volume.TargetTo] = true
	}
	var out []domain.AdaptationChapterPlan
	for _, chapter := range chapters {
		if wanted[chapter.Chapter] {
			out = append(out, cloneAdaptationChapterPlan(chapter))
		}
	}
	return out
}

func layeredDetailAuditDigest(runtime *domain.AdaptationProposalRuntime, detailBatchCount int) string {
	if runtime == nil || len(runtime.CompletedBatches) != detailBatchCount {
		return ""
	}
	for _, batch := range runtime.CompletedBatches {
		if batch.Audit == nil || batch.Audit.Status != domain.AdaptationDetailAuditPassed || !batch.Audit.DeterministicPassed || !batch.Audit.SemanticPassed {
			return ""
		}
	}
	requiredGlobal := false
	for _, checkpoint := range runtime.AuditCheckpoints {
		if checkpoint.Status != domain.AdaptationDetailAuditPassed {
			return ""
		}
		if checkpoint.Kind == "global" {
			requiredGlobal = true
		}
	}
	if !requiredGlobal {
		return ""
	}
	return detailOutlineValueSignature(struct {
		Batches     []domain.AdaptationProposalRuntimeBatch
		Checkpoints []domain.AdaptationDetailAuditCheckpoint
	}{runtime.CompletedBatches, runtime.AuditCheckpoints})
}
