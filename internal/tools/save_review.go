package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/voocel/agentcore/schema"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/store"
)

// SaveReviewTool 保存 Editor 的审阅结果。
type SaveReviewTool struct {
	store *store.Store
}

func NewSaveReviewTool(store *store.Store) *SaveReviewTool {
	return &SaveReviewTool{store: store}
}

func (t *SaveReviewTool) Name() string { return "save_review" }
func (t *SaveReviewTool) Description() string {
	return "保存审阅结果并更新流程状态。verdict 为 accept/polish/rewrite 之一。" +
		"工具内部执行评分卡门禁（可能升级 verdict），直接更新 Progress 的 flow 和 pending_rewrites。" +
		"返回结构化事实：final_verdict / affected_chapters / escalation_reason / next_flow / next_chapter"
}
func (t *SaveReviewTool) Label() string { return "保存审阅" }

// 写工具（同时更新 reviews/ 与 Progress 的 PendingRewrites/Flow），禁止并发。
func (t *SaveReviewTool) ReadOnly(_ json.RawMessage) bool        { return false }
func (t *SaveReviewTool) ConcurrencySafe(_ json.RawMessage) bool { return false }

func (t *SaveReviewTool) Schema() map[string]any {
	issueSchema := schema.Object(
		schema.Property("type", schema.Enum("问题维度", "consistency", "character", "pacing", "continuity", "foreshadow", "hook", "aesthetic")).Required(),
		schema.Property("severity", schema.Enum("严重程度", "critical", "error", "warning")).Required(),
		schema.Property("description", schema.String("问题描述")).Required(),
		schema.Property("evidence", schema.String("证据：原文片段、具体情节或状态数据")).Required(),
		schema.Property("suggestion", schema.String("修改建议；无则传空字符串")).Required(),
	)
	dimensionSchema := schema.Object(
		schema.Property("dimension", schema.Enum("维度", "consistency", "character", "pacing", "continuity", "foreshadow", "hook", "aesthetic")).Required(),
		schema.Property("score", schema.Int("评分（0-100）")).Required(),
		schema.Property("verdict", schema.Enum("维度结论；系统会按 score 自动覆盖，≥80 pass / ≥60 warning / <60 fail", "pass", "warning", "fail")).Required(),
		schema.Property("comment", schema.String("该维度的简要结论；每个维度必填，aesthetic 必须引用原文或具体统计事实")).Required(),
	)
	simulationShouldSchema := schema.Object(
		schema.Property("feature_id", schema.String("Editor review view 中的 should feature ID")).Required(),
		schema.Property("evidence", schema.String("当前草稿中的主观偏离证据")).Required(),
		schema.Property("suggestion", schema.String("非阻塞、可执行的改进建议")).Required(),
	)
	return schema.Object(
		schema.Property("chapter", schema.Int("审阅的章节号（全局审阅填最新章节号）")).Required(),
		schema.Property("scope", schema.Enum("审阅范围", "chapter", "global", "arc", "arc_batch")).Required(),
		schema.Property("volume", schema.Int("scope=arc_batch 时所属卷号；其他 scope 传 0")).Required(),
		schema.Property("arc", schema.Int("scope=arc_batch 时所属弧号；其他 scope 传 0")).Required(),
		schema.Property("batch_from", schema.Int("scope=arc_batch 时本批起始章节；其他 scope 传 0")).Required(),
		schema.Property("batch_to", schema.Int("scope=arc_batch 时本批结束章节；其他 scope 传 0")).Required(),
		schema.Property("dimensions", schema.Array("分维度评分（七个维度各一条）", dimensionSchema)).Required(),
		schema.Property("issues", schema.Array("发现的问题", issueSchema)).Required(),
		schema.Property("contract_status", schema.Enum("章节契约完成度；无明确缺漏时传 met", "met", "partial", "missed")).Required(),
		schema.Property("contract_misses", schema.Array("未完成或违背的 contract 条目；无则传 []", schema.String(""))).Required(),
		schema.Property("contract_notes", schema.String("对 contract 履行情况的简要说明；无则传空字符串")).Required(),
		schema.Property("simulation_should_findings", schema.Array("仿写 should 的主观审阅建议；无则传 []。不得在此重判确定性复制风险或 measurable must", simulationShouldSchema)).Required(),
		schema.Property("verdict", schema.Enum("审阅结论", "accept", "polish", "rewrite")).Required(),
		schema.Property("summary", schema.String("审阅总结")).Required(),
		schema.Property("affected_chapters", schema.Array("需要重写或打磨的章节号列表；accept 传 []，polish/rewrite 必须填目标章节", schema.Int(""))).Required(),
	)
}

func (t *SaveReviewTool) StrictSchema() bool { return true }

func (t *SaveReviewTool) Execute(_ context.Context, args json.RawMessage) (json.RawMessage, error) {
	var r domain.ReviewEntry
	if err := json.Unmarshal(args, &r); err != nil {
		return nil, fmt.Errorf("invalid args: %w", err)
	}
	if r.Chapter <= 0 {
		return nil, fmt.Errorf("chapter must be > 0")
	}
	// verdict 是 score 的纯函数（≥80 pass / ≥60 warning / <60 fail），由代码确定性推导——
	// 不让 LLM 重复提供再校验一致性。既消除冗余，也根除"score=85 却给 warning"这类自相矛盾的参数。
	for i := range r.Dimensions {
		r.Dimensions[i].Verdict = expectedDimensionVerdict(r.Dimensions[i].Score)
	}
	if err := validateReviewEntry(r); err != nil {
		return nil, err
	}
	if err := t.bindSimulationReview(&r); err != nil {
		return nil, err
	}

	if r.Scope == "arc_batch" {
		return t.saveArcBatchReview(r)
	}
	return t.saveFinalReview(r)
}

func (t *SaveReviewTool) bindSimulationReview(review *domain.ReviewEntry) error {
	content, _, err := t.store.Drafts.LoadChapterContent(review.Chapter)
	if err != nil {
		return fmt.Errorf("load reviewed draft: %w", err)
	}
	if strings.TrimSpace(content) == "" {
		content, err = t.store.Drafts.LoadChapterText(review.Chapter)
		if err != nil {
			return fmt.Errorf("load reviewed final chapter: %w", err)
		}
	}
	if strings.TrimSpace(content) == "" {
		if len(review.SimulationShouldFindings) > 0 {
			return fmt.Errorf("simulation should findings require a current chapter draft")
		}
		return nil
	}
	review.DraftSHA256 = store.TextSHA256(content)
	report, err := t.store.SimulationChecks.Load(review.Chapter)
	if err != nil {
		return fmt.Errorf("load simulation check for review: %w", err)
	}
	if report != nil && report.DraftDigest == review.DraftSHA256 {
		review.SimulationCheckDigest = report.ReportDigest
	}
	if len(review.SimulationShouldFindings) == 0 {
		return nil
	}
	if report == nil || report.DraftDigest != review.DraftSHA256 {
		return fmt.Errorf("simulation should findings require a current check_simulation report")
	}
	allowed := make(map[string]struct{}, len(report.ShouldAdvisories))
	for _, advisory := range report.ShouldAdvisories {
		allowed[advisory.FeatureID] = struct{}{}
	}
	seen := make(map[string]struct{}, len(review.SimulationShouldFindings))
	for _, finding := range review.SimulationShouldFindings {
		if _, ok := allowed[finding.FeatureID]; !ok {
			return fmt.Errorf("simulation should feature %q is not in the current Editor advisory set", finding.FeatureID)
		}
		if _, duplicate := seen[finding.FeatureID]; duplicate {
			return fmt.Errorf("simulation should feature %q is duplicated", finding.FeatureID)
		}
		if strings.TrimSpace(finding.Evidence) == "" || strings.TrimSpace(finding.Suggestion) == "" {
			return fmt.Errorf("simulation should finding %q requires evidence and suggestion", finding.FeatureID)
		}
		seen[finding.FeatureID] = struct{}{}
	}
	return nil
}

func (t *SaveReviewTool) saveFinalReview(r domain.ReviewEntry) (json.RawMessage, error) {
	// 评分卡门禁 — 内联原 policy/review.go 的升级逻辑
	finalVerdict := r.Verdict
	var escalationReason string

	if r.Verdict == "accept" {
		// 合同状态检查
		if r.ContractStatus == "missed" {
			finalVerdict = "rewrite"
			escalationReason = "合同履约状态为 missed，升级为重写"
		} else if r.ContractStatus == "partial" {
			finalVerdict = "polish"
			escalationReason = "合同履约状态为 partial，升级为打磨"
		}
		// 评分卡门禁
		if finalVerdict == "accept" {
			if gate := evaluateScorecardGate(r.Dimensions); gate != "" {
				if strings.Contains(gate, "rewrite") {
					finalVerdict = "rewrite"
				} else {
					finalVerdict = "polish"
				}
				escalationReason = gate
			}
		}
	}
	if gate, issueVerdict := evaluateIssueSeverityGate(r.Issues); gate != "" {
		upgradedVerdict := strongerReviewVerdict(finalVerdict, issueVerdict)
		if upgradedVerdict != finalVerdict {
			finalVerdict = upgradedVerdict
			escalationReason = gate
		}
	}

	affected := r.AffectedChapters
	if finalVerdict == "rewrite" || finalVerdict == "polish" {
		if len(affected) == 0 {
			affected = inferAffectedChaptersFromIssues(r.Issues)
		}
		if len(affected) == 0 && r.Chapter > 0 {
			affected = []int{r.Chapter}
		}
		if err := t.store.Progress.ValidatePendingRewrites(affected); err != nil {
			return nil, fmt.Errorf("validate pending rewrites: %w", err)
		}
	}

	if err := t.store.World.SaveReview(r); err != nil {
		return nil, fmt.Errorf("save review: %w", err)
	}

	// 根据最终 verdict 更新 Progress。
	// 写失败必须早返回——后续会 append review checkpoint，若此处吞 err 会让 Coordinator
	// 看到 saved:true 但 Store 仍处于旧 Flow / 缺失 PendingRewrites 的中间态。
	progress, _ := t.store.Progress.Load()
	if finalVerdict == "rewrite" || finalVerdict == "polish" {
		flow := domain.FlowRewriting
		if finalVerdict == "polish" {
			flow = domain.FlowPolishing
		}
		if err := t.store.Progress.SetPendingRewrites(affected, r.Summary); err != nil {
			return nil, fmt.Errorf("set pending rewrites: %w", err)
		}
		if err := t.store.Progress.SetFlow(flow); err != nil {
			return nil, fmt.Errorf("set flow %s: %w", flow, err)
		}
	} else {
		if err := t.store.Progress.SetFlow(domain.FlowWriting); err != nil {
			return nil, fmt.Errorf("set flow writing: %w", err)
		}
	}

	// 读取更新后的 Progress 快照作为事实
	latest, _ := t.store.Progress.Load()
	nextFlow := string(domain.FlowWriting)
	nextChapter := 0
	if latest != nil {
		nextFlow = string(latest.Flow)
		nextChapter = latest.NextChapter()
	}

	// 追加 checkpoint
	scope := domain.ChapterScope(r.Chapter)
	if r.Scope == "arc" {
		vol, arc := 0, 0
		if progress != nil {
			vol, arc = progress.CurrentVolume, progress.CurrentArc
		}
		scope = domain.ArcScope(vol, arc)
	}
	artifact := fmt.Sprintf("reviews/%02d.json", r.Chapter)
	if r.Scope == "global" {
		artifact = fmt.Sprintf("reviews/%02d-global.json", r.Chapter)
	}
	if _, err := t.store.Checkpoints.AppendArtifact(scope, "review", artifact); err != nil {
		return nil, fmt.Errorf("checkpoint review: %w", err)
	}

	return json.Marshal(map[string]any{
		"saved":             true,
		"chapter":           r.Chapter,
		"scope":             r.Scope,
		"verdict":           r.Verdict,
		"final_verdict":     finalVerdict,
		"escalation_reason": escalationReason,
		"affected_chapters": affected,
		"issues":            len(r.Issues),
		"next_flow":         nextFlow,
		"next_chapter":      nextChapter,
	})
}

func (t *SaveReviewTool) saveArcBatchReview(r domain.ReviewEntry) (json.RawMessage, error) {
	boundary, err := t.store.Outline.CheckArcBoundary(r.Chapter)
	if err != nil {
		return nil, fmt.Errorf("load arc boundary for batch review: %w", err)
	}
	if boundary == nil || !boundary.IsArcEnd {
		return nil, fmt.Errorf("arc_batch review chapter %d is not an arc end", r.Chapter)
	}
	if r.Volume <= 0 {
		r.Volume = boundary.Volume
	}
	if r.Arc <= 0 {
		r.Arc = boundary.Arc
	}
	if r.Volume != boundary.Volume || r.Arc != boundary.Arc {
		return nil, fmt.Errorf("arc_batch review targets V%d A%d, want V%d A%d", r.Volume, r.Arc, boundary.Volume, boundary.Arc)
	}
	if r.BatchFrom < boundary.FirstChapter || r.BatchTo > boundary.LastChapter || r.BatchTo < r.BatchFrom {
		return nil, fmt.Errorf("arc_batch range %d-%d outside arc range %d-%d", r.BatchFrom, r.BatchTo, boundary.FirstChapter, boundary.LastChapter)
	}

	if err := t.store.World.SaveReview(r); err != nil {
		return nil, fmt.Errorf("save arc batch review: %w", err)
	}
	artifact := store.ArcBatchReviewRelPath(r.Volume, r.Arc, r.BatchFrom, r.BatchTo)
	if _, err := t.store.Checkpoints.AppendArtifact(domain.ArcScope(r.Volume, r.Arc), "arc_review_batch", artifact); err != nil {
		return nil, fmt.Errorf("checkpoint arc batch review: %w", err)
	}

	merged, complete, err := t.mergeArcReviewIfComplete(boundary)
	if err != nil {
		return nil, err
	}
	if !complete {
		next, _ := t.store.NextArcReviewBatch(boundary, domain.ArcReviewBatchRuneBudget)
		result := map[string]any{
			"saved":               true,
			"scope":               r.Scope,
			"chapter":             r.Chapter,
			"volume":              r.Volume,
			"arc":                 r.Arc,
			"batch_from":          r.BatchFrom,
			"batch_to":            r.BatchTo,
			"arc_review_complete": false,
		}
		if next != nil {
			result["next_batch_from"] = next.From
			result["next_batch_to"] = next.To
			result["next_batch_index"] = next.Index
			result["batch_total"] = next.Total
		}
		return json.Marshal(result)
	}

	out, err := t.saveFinalReview(merged)
	if err != nil {
		return nil, err
	}
	var result map[string]any
	if json.Unmarshal(out, &result) == nil {
		result["merged_from_batches"] = true
		result["arc_review_complete"] = true
		result["batch_from"] = boundary.FirstChapter
		result["batch_to"] = boundary.LastChapter
		return json.Marshal(result)
	}
	return out, nil
}

func (t *SaveReviewTool) mergeArcReviewIfComplete(boundary *store.ArcBoundary) (domain.ReviewEntry, bool, error) {
	ranges, err := t.store.ArcReviewBatchRanges(boundary.FirstChapter, boundary.LastChapter, domain.ArcReviewBatchRuneBudget)
	if err != nil {
		return domain.ReviewEntry{}, false, fmt.Errorf("build arc review ranges: %w", err)
	}
	reviews, err := t.store.World.LoadArcBatchReviews(boundary.Volume, boundary.Arc)
	if err != nil {
		return domain.ReviewEntry{}, false, fmt.Errorf("load arc batch reviews: %w", err)
	}
	byRange := make(map[[2]int]domain.ReviewEntry, len(reviews))
	for _, review := range reviews {
		byRange[[2]int{review.BatchFrom, review.BatchTo}] = review
	}
	ordered := make([]domain.ReviewEntry, 0, len(ranges))
	for _, r := range ranges {
		review, ok := byRange[[2]int{r.From, r.To}]
		if !ok {
			return domain.ReviewEntry{}, false, nil
		}
		ordered = append(ordered, review)
	}
	return mergeArcBatchReviews(boundary, ordered), true, nil
}

func mergeArcBatchReviews(boundary *store.ArcBoundary, reviews []domain.ReviewEntry) domain.ReviewEntry {
	merged := domain.ReviewEntry{
		Chapter:        boundary.LastChapter,
		Scope:          "arc",
		Volume:         boundary.Volume,
		Arc:            boundary.Arc,
		BatchFrom:      boundary.FirstChapter,
		BatchTo:        boundary.LastChapter,
		Verdict:        "accept",
		ContractStatus: "met",
	}
	dimensions := make(map[string]domain.DimensionScore, len(expectedReviewDimensions))
	dimensionComments := make(map[string][]string, len(expectedReviewDimensions))
	affected := make(map[int]struct{})
	contractMisses := make(map[string]struct{})
	var summaries []string

	for _, review := range reviews {
		merged.Issues = append(merged.Issues, review.Issues...)
		merged.Verdict = strongerReviewVerdict(merged.Verdict, review.Verdict)
		merged.ContractStatus = strongerContractStatus(merged.ContractStatus, review.ContractStatus)
		if strings.TrimSpace(review.Summary) != "" {
			summaries = append(summaries, fmt.Sprintf("第%d-%d章：%s", review.BatchFrom, review.BatchTo, truncateReviewText(review.Summary, 80)))
		}
		for _, chapter := range review.AffectedChapters {
			if chapter > 0 {
				affected[chapter] = struct{}{}
			}
		}
		for _, miss := range review.ContractMisses {
			miss = strings.TrimSpace(miss)
			if miss != "" {
				contractMisses[miss] = struct{}{}
			}
		}
		for _, dim := range review.Dimensions {
			current, ok := dimensions[dim.Dimension]
			if !ok || dim.Score < current.Score {
				dimensions[dim.Dimension] = dim
			}
			if strings.TrimSpace(dim.Comment) != "" {
				dimensionComments[dim.Dimension] = append(dimensionComments[dim.Dimension],
					fmt.Sprintf("第%d-%d章：%s", review.BatchFrom, review.BatchTo, truncateReviewText(dim.Comment, 70)))
			}
		}
	}

	for chapter := range affected {
		merged.AffectedChapters = append(merged.AffectedChapters, chapter)
	}
	sortInts(merged.AffectedChapters)
	for miss := range contractMisses {
		merged.ContractMisses = append(merged.ContractMisses, miss)
	}
	sortStrings(merged.ContractMisses)
	merged.ContractNotes = buildMergedContractNotes(reviews)
	merged.Summary = buildMergedArcReviewSummary(boundary, reviews, summaries, merged.Issues)
	merged.Dimensions = mergedReviewDimensions(dimensions, dimensionComments)
	return merged
}

func mergedReviewDimensions(dimensions map[string]domain.DimensionScore, comments map[string][]string) []domain.DimensionScore {
	order := []string{"consistency", "character", "pacing", "continuity", "foreshadow", "hook", "aesthetic"}
	out := make([]domain.DimensionScore, 0, len(order))
	for _, name := range order {
		dim, ok := dimensions[name]
		if !ok {
			dim = domain.DimensionScore{Dimension: name, Score: 80, Verdict: "pass", Comment: "批次未报告该维度问题"}
		}
		if notes := comments[name]; len(notes) > 0 {
			dim.Comment = truncateReviewText(strings.Join(limitStringsForReview(notes, 3), "；"), 240)
		}
		dim.Verdict = expectedDimensionVerdict(dim.Score)
		out = append(out, dim)
	}
	return out
}

var expectedReviewDimensions = map[string]struct{}{
	"consistency": {},
	"character":   {},
	"pacing":      {},
	"continuity":  {},
	"foreshadow":  {},
	"hook":        {},
	"aesthetic":   {},
}

func validateReviewEntry(r domain.ReviewEntry) error {
	if strings.TrimSpace(r.Scope) == "" {
		return fmt.Errorf("scope is required")
	}
	if r.Scope == "arc_batch" {
		if r.BatchFrom <= 0 || r.BatchTo < r.BatchFrom {
			return fmt.Errorf("arc_batch requires batch_from and batch_to")
		}
	}
	if strings.TrimSpace(r.Summary) == "" {
		return fmt.Errorf("summary is required")
	}
	for _, issue := range r.Issues {
		if strings.TrimSpace(issue.Description) == "" {
			return fmt.Errorf("issue description is required")
		}
		if strings.TrimSpace(issue.Evidence) == "" {
			return fmt.Errorf("issue evidence is required")
		}
	}
	if err := validateDimensions(r.Dimensions); err != nil {
		return err
	}
	if (r.Verdict == "rewrite" || r.Verdict == "polish") && len(r.AffectedChapters) == 0 {
		return fmt.Errorf("affected_chapters is required when verdict=%s", r.Verdict)
	}
	return nil
}

func validateDimensions(dimensions []domain.DimensionScore) error {
	if len(dimensions) != len(expectedReviewDimensions) {
		return fmt.Errorf("dimensions must contain exactly %d entries", len(expectedReviewDimensions))
	}

	seen := make(map[string]struct{}, len(dimensions))
	for _, dim := range dimensions {
		if _, ok := expectedReviewDimensions[dim.Dimension]; !ok {
			return fmt.Errorf("unknown dimension: %s", dim.Dimension)
		}
		if _, ok := seen[dim.Dimension]; ok {
			return fmt.Errorf("duplicate dimension: %s", dim.Dimension)
		}
		seen[dim.Dimension] = struct{}{}
		if dim.Score < 0 || dim.Score > 100 {
			return fmt.Errorf("invalid score for %s: %d", dim.Dimension, dim.Score)
		}
		if strings.TrimSpace(dim.Comment) == "" {
			return fmt.Errorf("dimension comment is required: %s", dim.Dimension)
		}
	}
	return nil
}

func expectedDimensionVerdict(score int) string {
	switch {
	case score >= 80:
		return "pass"
	case score >= 60:
		return "warning"
	default:
		return "fail"
	}
}

// criticalDimensions 定义会触发 verdict 升级的关键维度。
var criticalDimensions = map[string]struct{}{
	"consistency": {},
	"character":   {},
	"continuity":  {},
}

// evaluateScorecardGate 检查评分卡是否需要升级 verdict。
// 返回空字符串表示不升级。
func evaluateScorecardGate(dimensions []domain.DimensionScore) string {
	var criticalFails []string
	var polishIssues []string

	for _, dim := range dimensions {
		_, isCritical := criticalDimensions[dim.Dimension]
		if isCritical && (dim.Verdict == "fail" || dim.Score < 60) {
			criticalFails = append(criticalFails, fmt.Sprintf("%s(%d)", dim.Dimension, dim.Score))
		} else if dim.Verdict == "warning" || (isCritical && dim.Score < 80) {
			polishIssues = append(polishIssues, fmt.Sprintf("%s(%d)", dim.Dimension, dim.Score))
		}
	}

	if len(criticalFails) > 0 {
		return fmt.Sprintf("rewrite: 关键维度不合格 %v", criticalFails)
	}
	if len(polishIssues) > 0 {
		return fmt.Sprintf("polish: 部分维度需打磨 %v", polishIssues)
	}
	return ""
}

func evaluateIssueSeverityGate(issues []domain.ConsistencyIssue) (string, string) {
	criticalCount := 0
	errorCount := 0
	for _, issue := range issues {
		switch issue.Severity {
		case "critical":
			criticalCount++
		case "error":
			errorCount++
		}
	}
	if criticalCount > 0 {
		return fmt.Sprintf("rewrite: review issues include %d critical item(s)", criticalCount), "rewrite"
	}
	if errorCount > 0 {
		return fmt.Sprintf("polish: review issues include %d error item(s)", errorCount), "polish"
	}
	return "", ""
}

func strongerReviewVerdict(current, candidate string) string {
	if reviewVerdictRank(candidate) > reviewVerdictRank(current) {
		return candidate
	}
	return current
}

func reviewVerdictRank(verdict string) int {
	switch verdict {
	case "rewrite":
		return 2
	case "polish":
		return 1
	default:
		return 0
	}
}

func strongerContractStatus(current, candidate string) string {
	if contractStatusRank(candidate) > contractStatusRank(current) {
		return candidate
	}
	return current
}

func contractStatusRank(status string) int {
	switch status {
	case "missed":
		return 2
	case "partial":
		return 1
	default:
		return 0
	}
}

func buildMergedContractNotes(reviews []domain.ReviewEntry) string {
	var notes []string
	for _, review := range reviews {
		if strings.TrimSpace(review.ContractNotes) == "" {
			continue
		}
		notes = append(notes, fmt.Sprintf("第%d-%d章：%s", review.BatchFrom, review.BatchTo, truncateReviewText(review.ContractNotes, 70)))
	}
	if len(notes) == 0 {
		return "弧级批次审核未发现明确章节契约漏项。"
	}
	return truncateReviewText(strings.Join(limitStringsForReview(notes, 3), "；"), 240)
}

func buildMergedArcReviewSummary(boundary *store.ArcBoundary, reviews []domain.ReviewEntry, summaries []string, issues []domain.ConsistencyIssue) string {
	issueText := mergedIssuePreview(issues)
	summaryText := strings.Join(limitStringsForReview(summaries, 3), "；")
	if summaryText == "" {
		summaryText = "各批次未报告明显问题"
	}
	if issueText != "" {
		return truncateReviewText(fmt.Sprintf("弧级分批审核完成：第%d-%d章共%d批。%s。主要问题：%s", boundary.FirstChapter, boundary.LastChapter, len(reviews), summaryText, issueText), 200)
	}
	return truncateReviewText(fmt.Sprintf("弧级分批审核完成：第%d-%d章共%d批。%s。", boundary.FirstChapter, boundary.LastChapter, len(reviews), summaryText), 200)
}

func mergedIssuePreview(issues []domain.ConsistencyIssue) string {
	if len(issues) == 0 {
		return ""
	}
	parts := make([]string, 0, min(len(issues), 3))
	for _, issue := range issues {
		if strings.TrimSpace(issue.Description) == "" {
			continue
		}
		parts = append(parts, fmt.Sprintf("%s/%s：%s", issue.Type, issue.Severity, truncateReviewText(issue.Description, 50)))
		if len(parts) >= 3 {
			break
		}
	}
	return strings.Join(parts, "；")
}

func limitStringsForReview(values []string, maxItems int) []string {
	if maxItems <= 0 || len(values) <= maxItems {
		return values
	}
	return values[:maxItems]
}

func truncateReviewText(text string, maxRunes int) string {
	if maxRunes <= 0 {
		return text
	}
	runes := []rune(strings.TrimSpace(text))
	if len(runes) <= maxRunes {
		return string(runes)
	}
	return string(runes[:maxRunes]) + "..."
}

func sortInts(values []int) {
	sort.Ints(values)
}

func sortStrings(values []string) {
	sort.Strings(values)
}

var issueChapterPatterns = []*regexp.Regexp{
	regexp.MustCompile(`第\s*(\d+)\s*章`),
	regexp.MustCompile(`(?i)\bchapter\s+(\d+)\b`),
	regexp.MustCompile(`(?i)\bch\.?\s*(\d+)\b`),
}

func inferAffectedChaptersFromIssues(issues []domain.ConsistencyIssue) []int {
	seen := map[int]struct{}{}
	var chapters []int
	for _, issue := range issues {
		text := strings.Join([]string{issue.Description, issue.Evidence, issue.Suggestion}, "\n")
		for _, pattern := range issueChapterPatterns {
			for _, match := range pattern.FindAllStringSubmatch(text, -1) {
				chapter, err := strconv.Atoi(match[1])
				if err != nil || chapter <= 0 {
					continue
				}
				if _, ok := seen[chapter]; ok {
					continue
				}
				seen[chapter] = struct{}{}
				chapters = append(chapters, chapter)
			}
		}
	}
	return chapters
}
