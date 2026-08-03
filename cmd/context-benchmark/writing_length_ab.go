package main

import (
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/voocel/ainovel-cli/internal/bootstrap"
)

const writingLengthABVersion = "writing-length-ab-v1"

type writingLengthBand struct {
	ID              string
	Label           string
	MinRunes        int
	MaxRunes        int
	MaxOutputTokens int
}

var writingLengthBands = []writingLengthBand{
	{ID: "2600_3200", Label: "2600–3200 字", MinRunes: 2600, MaxRunes: 3200, MaxOutputTokens: 8000},
	{ID: "4000_4500", Label: "4000–4500 字", MinRunes: 4000, MaxRunes: 4500, MaxOutputTokens: 11000},
	{ID: "5000_5500", Label: "5000–5500 字", MinRunes: 5000, MaxRunes: 5500, MaxOutputTokens: 14000},
}

type writingLengthResult struct {
	CaseID           string  `json:"case_id"`
	FixtureID        string  `json:"fixture_id"`
	LengthBand       string  `json:"length_band"`
	Candidate        string  `json:"candidate"`
	OutputRunes      int     `json:"output_runes"`
	MinimumRunes     int     `json:"minimum_runes"`
	MaximumRunes     int     `json:"maximum_runes"`
	LengthRatio      float64 `json:"length_ratio"`
	LengthPassed     bool    `json:"length_passed"`
	HardScore        float64 `json:"hard_score"`
	ContentScore     float64 `json:"content_score"`
	PreGateScore     float64 `json:"pre_gate_score"`
	GatedFinalScore  float64 `json:"gated_final_score"`
	LengthGateReason string  `json:"length_gate_reason,omitempty"`
}

type writingLengthBandSummary struct {
	LengthBand      string  `json:"length_band"`
	Samples         int     `json:"samples"`
	Passes          int     `json:"passes"`
	PassRate        float64 `json:"pass_rate"`
	MeanOutputRunes float64 `json:"mean_output_runes"`
	ContentMean     float64 `json:"content_mean"`
	GatedFinalMean  float64 `json:"gated_final_mean"`
}

type writingLengthCandidateSummary struct {
	Candidate       string                     `json:"candidate"`
	Samples         int                        `json:"samples"`
	Passes          int                        `json:"passes"`
	PassRate        float64                    `json:"pass_rate"`
	ContentMean     float64                    `json:"content_mean"`
	GatedFinalMean  float64                    `json:"gated_final_mean"`
	MeanOutputRunes float64                    `json:"mean_output_runes"`
	Bands           []writingLengthBandSummary `json:"bands"`
}

type writingLengthABSummary struct {
	Version          string                          `json:"version"`
	GeneratedAt      time.Time                       `json:"generated_at"`
	Judge            modelSpec                       `json:"judge"`
	Candidates       []writingLengthCandidateSummary `json:"candidates"`
	Results          []writingLengthResult           `json:"results"`
	ContentDelta     float64                         `json:"content_delta_high_minus_xhigh"`
	GatedFinalDelta  float64                         `json:"gated_final_delta_high_minus_xhigh"`
	ConfidenceLow    float64                         `json:"confidence_low"`
	ConfidenceHigh   float64                         `json:"confidence_high"`
	LengthGatePolicy string                          `json:"length_gate_policy"`
	Conclusion       string                          `json:"conclusion"`
}

func runWritingLengthAB(ctx context.Context, opts options) error {
	fixtures, err := loadQualityFixtures()
	if err != nil {
		return err
	}
	cases := buildWritingLengthABCases(fixtures)
	if err := validateWritingLengthABCases(cases); err != nil {
		return err
	}
	cfg, err := bootstrap.LoadConfig(opts.ConfigPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	if opts.AllConfigured {
		return errors.New("--all-configured is not supported by writing-length-ab-v1")
	}
	opts.ModelSpecs = resolveQualityModelSpecs(cfg, opts.ModelSpecs)
	opts.JudgeSpec = resolveQualityModelSpec(cfg, opts.JudgeSpec)
	if len(opts.ModelSpecs) < 2 {
		return fmt.Errorf("%s requires at least two candidates", writingLengthABVersion)
	}
	if !opts.DryRun && opts.JudgeSpec.Model == "" {
		return fmt.Errorf("%s requires --judge", writingLengthABVersion)
	}
	if err := os.MkdirAll(opts.OutputDir, 0o755); err != nil {
		return fmt.Errorf("create output dir: %w", err)
	}
	if err := writeQualityManifest(opts, cases); err != nil {
		return err
	}
	if opts.DryRun {
		fmt.Printf("dry-run: suite=%s cases/model=%d models=%d candidate_calls=%d judge_calls=%d output=%s\n",
			writingLengthABVersion, len(cases), len(opts.ModelSpecs), len(cases)*len(opts.ModelSpecs), len(cases), opts.OutputDir)
		for _, spec := range opts.ModelSpecs {
			fmt.Printf("candidate: %s\n", qualityModelLabel(spec))
		}
		fmt.Printf("judge: %s\n", qualityModelLabel(opts.JudgeSpec))
		return nil
	}
	candidateModels, err := createQualityModels(cfg, opts.ModelSpecs, opts.RequestTimeout)
	if err != nil {
		return err
	}
	if err := runQualityCandidates(ctx, opts, cases, candidateModels); err != nil {
		return err
	}
	judgeModels, err := createQualityModels(cfg, []modelSpec{opts.JudgeSpec}, opts.RequestTimeout)
	if err != nil {
		return err
	}
	if err := runQualityJudges(ctx, opts, cases, judgeModels[opts.JudgeSpec.ID()]); err != nil {
		return err
	}
	summary, err := aggregateWritingLengthAB(opts, cases)
	if err != nil {
		return err
	}
	return writeWritingLengthABReports(opts, summary)
}

func buildWritingLengthABCases(fixtures []qualityFixture) []qualityCase {
	result := make([]qualityCase, 0, len(fixtures)*len(writingLengthBands))
	for _, band := range writingLengthBands {
		for _, fixture := range fixtures {
			result = append(result, qualityCase{
				ID:        fixture.ID + "__writing__" + band.ID,
				Stage:     bootstrap.StageWriting,
				FixtureID: fixture.ID,
				Title:     fixture.Title + "（" + band.Label + "）",
				SystemPrompt: "你是中文长篇小说作者。直接写场景正文，不解释创作过程。字数是交付约束，" +
					"不得用提纲、总结、重复段落或元叙述凑字数。",
				TaskPrompt: fmt.Sprintf(
					"续写给定开篇，形成一个 %s的完整章节。不得少于 %d 个中文字符，也不得超过 %d 个中文字符。"+
						"通过行动推进矛盾，保持人物动机与世界规则；关键发现必须有可见取得过程，新增篇幅必须承载场景、因果或人物变化。"+
						"结尾留下可执行悬念。不要总结、提纲、重复前文或元叙述。",
					band.Label, band.MinRunes, band.MaxRunes),
				Context:         qualityFixtureContext(fixture),
				Rubric:          []string{"场景与文笔质量", "人物声音", "因果推进", "连续性", "长篇展开的有效信息密度"},
				MinRunes:        band.MinRunes,
				MaxRunes:        band.MaxRunes,
				MaxOutputTokens: band.MaxOutputTokens,
				EnforceLength:   true,
			})
		}
	}
	return result
}

func validateWritingLengthABCases(cases []qualityCase) error {
	want := len(writingLengthBands) * 3
	if len(cases) != want {
		return fmt.Errorf("writing length A/B case count = %d, want %d", len(cases), want)
	}
	seen := make(map[string]bool, len(cases))
	for _, testCase := range cases {
		if seen[testCase.ID] {
			return fmt.Errorf("duplicate writing length A/B case %q", testCase.ID)
		}
		seen[testCase.ID] = true
		if testCase.Stage != bootstrap.StageWriting || !testCase.EnforceLength {
			return fmt.Errorf("case %q is not a length-gated writing case", testCase.ID)
		}
		if !strings.Contains(testCase.TaskPrompt, strconv.Itoa(testCase.MinRunes)) ||
			!strings.Contains(testCase.TaskPrompt, strconv.Itoa(testCase.MaxRunes)) {
			return fmt.Errorf("case %q does not explicitly state its length bounds", testCase.ID)
		}
	}
	return nil
}

func aggregateWritingLengthAB(opts options, cases []qualityCase) (writingLengthABSummary, error) {
	summary := writingLengthABSummary{
		Version:          writingLengthABVersion,
		GeneratedAt:      time.Now(),
		Judge:            opts.JudgeSpec,
		LengthGatePolicy: "最低字数是硬门槛：低于下限时综合分上限为 50×完成比例；最高字数仅为软目标，超出不触发门槛扣分，冗长或灌水由内容盲评的信息密度维度扣分。",
	}
	resultsByCandidate := make(map[string][]writingLengthResult)
	for _, testCase := range cases {
		var judgeRecord qualityJudgeRecord
		if err := readJSONFile(qualityJudgePath(opts.OutputDir, testCase.ID), &judgeRecord); err != nil {
			return summary, err
		}
		judgeByID := make(map[string]float64)
		for _, score := range judgeRecord.Judgement.Scores {
			judgeByID[judgeRecord.AnonymousMapping[score.Candidate]] = score.Overall
		}
		for _, spec := range opts.ModelSpecs {
			var attempt qualityAttempt
			if err := readJSONFile(qualityCandidatePath(opts.OutputDir, spec, testCase.ID), &attempt); err != nil {
				return summary, err
			}
			runes := utf8.RuneCountInString(strings.TrimSpace(attempt.Response))
			hardScore, _ := scoreQualityHardChecks(testCase, attempt.Response, attempt.StopReason)
			contentScore := judgeByID[qualityAttemptID(attempt)]
			preGate := roundQuality(0.30*hardScore + 0.70*contentScore)
			finalScore, ratio, reason := applyWritingLengthGate(preGate, runes, testCase.MinRunes)
			result := writingLengthResult{
				CaseID: testCase.ID, FixtureID: testCase.FixtureID,
				LengthBand: fmt.Sprintf("%d–%d", testCase.MinRunes, testCase.MaxRunes),
				Candidate:  qualityModelLabel(spec), OutputRunes: runes,
				MinimumRunes: testCase.MinRunes, MaximumRunes: testCase.MaxRunes,
				LengthRatio: roundQuality(ratio), LengthPassed: reason == "",
				HardScore: hardScore, ContentScore: roundQuality(contentScore),
				PreGateScore: preGate, GatedFinalScore: finalScore, LengthGateReason: reason,
			}
			summary.Results = append(summary.Results, result)
			resultsByCandidate[spec.ID()] = append(resultsByCandidate[spec.ID()], result)
		}
	}
	for _, spec := range opts.ModelSpecs {
		results := resultsByCandidate[spec.ID()]
		candidate := summarizeWritingLengthCandidate(qualityModelLabel(spec), results)
		summary.Candidates = append(summary.Candidates, candidate)
	}
	sort.SliceStable(summary.Candidates, func(i, j int) bool {
		return summary.Candidates[i].GatedFinalMean > summary.Candidates[j].GatedFinalMean
	})
	if len(opts.ModelSpecs) >= 2 {
		left := resultsByCandidate[opts.ModelSpecs[0].ID()]
		right := resultsByCandidate[opts.ModelSpecs[1].ID()]
		var deltas []float64
		for index := range left {
			deltas = append(deltas, left[index].GatedFinalScore-right[index].GatedFinalScore)
		}
		summary.ContentDelta = roundQuality(meanWritingResult(left, func(result writingLengthResult) float64 {
			return result.ContentScore
		}) - meanWritingResult(right, func(result writingLengthResult) float64 {
			return result.ContentScore
		}))
		summary.GatedFinalDelta = roundQuality(meanQuality(deltas))
		summary.ConfidenceLow, summary.ConfidenceHigh = bootstrapQualityCI(deltas, 10_000)
		summary.ConfidenceLow = roundQuality(summary.ConfidenceLow)
		summary.ConfidenceHigh = roundQuality(summary.ConfidenceHigh)
	}
	if len(summary.Candidates) >= 2 {
		leader := summary.Candidates[0]
		var details []string
		for _, candidate := range summary.Candidates {
			details = append(details, fmt.Sprintf("%s：门槛综合分 %.1f、达标率 %.0f%%、内容 %.1f",
				candidate.Candidate, candidate.GatedFinalMean, candidate.PassRate, candidate.ContentMean))
		}
		summary.Conclusion = fmt.Sprintf(
			"%s 在字数硬门槛后的综合排名第一。%s。",
			leader.Candidate, strings.Join(details, "；"))
	}
	return summary, nil
}

func applyWritingLengthGate(preGate float64, runes, minRunes int) (float64, float64, string) {
	if runes < minRunes {
		ratio := float64(runes) / float64(minRunes)
		return roundQuality(math.Min(preGate, 50*ratio)), ratio, "below_minimum"
	}
	return roundQuality(preGate), 1, ""
}

func summarizeWritingLengthCandidate(label string, results []writingLengthResult) writingLengthCandidateSummary {
	summary := writingLengthCandidateSummary{Candidate: label, Samples: len(results)}
	for _, result := range results {
		if result.LengthPassed {
			summary.Passes++
		}
	}
	summary.PassRate = roundQuality(100 * float64(summary.Passes) / float64(maxInt(len(results), 1)))
	summary.ContentMean = roundQuality(meanWritingResult(results, func(result writingLengthResult) float64 { return result.ContentScore }))
	summary.GatedFinalMean = roundQuality(meanWritingResult(results, func(result writingLengthResult) float64 { return result.GatedFinalScore }))
	summary.MeanOutputRunes = roundQuality(meanWritingResult(results, func(result writingLengthResult) float64 { return float64(result.OutputRunes) }))
	for _, band := range writingLengthBands {
		var bandResults []writingLengthResult
		want := fmt.Sprintf("%d–%d", band.MinRunes, band.MaxRunes)
		for _, result := range results {
			if result.LengthBand == want {
				bandResults = append(bandResults, result)
			}
		}
		bandSummary := writingLengthBandSummary{LengthBand: want, Samples: len(bandResults)}
		for _, result := range bandResults {
			if result.LengthPassed {
				bandSummary.Passes++
			}
		}
		bandSummary.PassRate = roundQuality(100 * float64(bandSummary.Passes) / float64(maxInt(len(bandResults), 1)))
		bandSummary.MeanOutputRunes = roundQuality(meanWritingResult(bandResults, func(result writingLengthResult) float64 { return float64(result.OutputRunes) }))
		bandSummary.ContentMean = roundQuality(meanWritingResult(bandResults, func(result writingLengthResult) float64 { return result.ContentScore }))
		bandSummary.GatedFinalMean = roundQuality(meanWritingResult(bandResults, func(result writingLengthResult) float64 { return result.GatedFinalScore }))
		summary.Bands = append(summary.Bands, bandSummary)
	}
	return summary
}

func meanWritingResult(results []writingLengthResult, value func(writingLengthResult) float64) float64 {
	if len(results) == 0 {
		return 0
	}
	total := 0.0
	for _, result := range results {
		total += value(result)
	}
	return total / float64(len(results))
}

func maxInt(left, right int) int {
	if left > right {
		return left
	}
	return right
}

func writeWritingLengthABReports(opts options, summary writingLengthABSummary) error {
	localDir := filepath.Join(opts.OutputDir, "aggregate")
	if err := writeWritingLengthABReportSet(localDir, summary); err != nil {
		return err
	}
	if strings.TrimSpace(opts.ReportDir) != "" {
		if err := writeWritingLengthABReportSet(opts.ReportDir, summary); err != nil {
			return err
		}
	}
	fmt.Printf("writing-length A/B report: %s\n%s\n", localDir, summary.Conclusion)
	return nil
}

func writeWritingLengthABReportSet(dir string, summary writingLengthABSummary) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create report dir: %w", err)
	}
	if err := writeJSONAtomic(filepath.Join(dir, "summary.json"), summary); err != nil {
		return err
	}
	var csvBuffer bytes.Buffer
	writer := csv.NewWriter(&csvBuffer)
	_ = writer.Write([]string{"candidate", "fixture", "length_band", "output_runes", "length_passed", "content_score", "pre_gate_score", "gated_final_score", "gate_reason"})
	for _, result := range summary.Results {
		_ = writer.Write([]string{
			result.Candidate, result.FixtureID, result.LengthBand, strconv.Itoa(result.OutputRunes),
			strconv.FormatBool(result.LengthPassed), fmt.Sprintf("%.1f", result.ContentScore),
			fmt.Sprintf("%.1f", result.PreGateScore), fmt.Sprintf("%.1f", result.GatedFinalScore), result.LengthGateReason,
		})
	}
	writer.Flush()
	if err := os.WriteFile(filepath.Join(dir, "scores.csv"), append([]byte{0xEF, 0xBB, 0xBF}, csvBuffer.Bytes()...), 0o644); err != nil {
		return fmt.Errorf("write writing-length CSV: %w", err)
	}
	var markdown strings.Builder
	markdown.WriteString("# 正文字数与模型推理等级对比\n\n")
	fmt.Fprintf(&markdown, "- 套件：`%s`\n- 裁判：`%s`\n- 样本：3 套原创脱敏语料 × 3 个显式字数档 × %d 个候选\n",
		summary.Version, qualityModelLabel(summary.Judge), len(summary.Candidates))
	markdown.WriteString("- 评分：内容盲评单列；综合分先按硬指标 30% + 内容 70%，再应用最低字数硬门槛；最高字数为软目标\n\n")
	markdown.WriteString("| 排名 | 候选 | 达标率 | 内容盲评 | 字数门槛综合分 | 平均输出字符 |\n|---:|---|---:|---:|---:|---:|\n")
	for index, candidate := range summary.Candidates {
		fmt.Fprintf(&markdown, "| %d | `%s` | %.0f%% | %.1f | %.1f | %.0f |\n",
			index+1, candidate.Candidate, candidate.PassRate, candidate.ContentMean, candidate.GatedFinalMean, candidate.MeanOutputRunes)
	}
	markdown.WriteString("\n## 分字数档\n\n")
	markdown.WriteString("| 候选 | 目标字数 | 达标 | 平均输出字符 | 内容盲评 | 门槛综合分 |\n|---|---|---:|---:|---:|---:|\n")
	for _, candidate := range summary.Candidates {
		for _, band := range candidate.Bands {
			fmt.Fprintf(&markdown, "| `%s` | %s | %d/%d | %.0f | %.1f | %.1f |\n",
				candidate.Candidate, band.LengthBand, band.Passes, band.Samples,
				band.MeanOutputRunes, band.ContentMean, band.GatedFinalMean)
		}
	}
	markdown.WriteString("\n## 逐题结果\n\n")
	markdown.WriteString("| 候选 | 语料 | 目标字数 | 实际字符 | 达标 | 内容盲评 | 门槛综合分 |\n|---|---|---|---:|---|---:|---:|\n")
	for _, result := range summary.Results {
		pass := "否"
		if result.LengthPassed {
			pass = "是"
		}
		fmt.Fprintf(&markdown, "| `%s` | %s | %s | %d | %s | %.1f | %.1f |\n",
			result.Candidate, result.FixtureID, result.LengthBand, result.OutputRunes,
			pass, result.ContentScore, result.GatedFinalScore)
	}
	fmt.Fprintf(&markdown, "\n## 统计与结论\n\n- `high - xhigh` 内容均分差：%.1f\n- `high - xhigh` 门槛综合分差：%.1f\n- 配对 bootstrap 95%% 区间：[%.1f, %.1f]\n- 字数门槛：%s\n\n%s\n",
		summary.ContentDelta, summary.GatedFinalDelta, summary.ConfidenceLow, summary.ConfidenceHigh,
		summary.LengthGatePolicy, summary.Conclusion)
	markdown.WriteString("\n本报告不包含小说正文、完整模型响应或凭证；原始记录仅保存在本地 `.ainovel/benchmarks/writing-length-ab-v1/`。\n")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte(markdown.String()), 0o644); err != nil {
		return fmt.Errorf("write writing-length Markdown: %w", err)
	}
	return nil
}
