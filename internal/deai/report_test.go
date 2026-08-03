package deai

import (
	"strings"
	"testing"
)

func TestAnalyzeFlagsConcreteDeAISymptoms(t *testing.T) {
	text := `# 第一章

## 一
然后他没有说话——不是因为不想说，而是因为不能说——像一盏灯一样。

然后他没有回答——不是因为害怕，而是因为没有办法——仿佛一块石头。

然后他沉默了——不是因为冷静，而是因为无法离开——如同一扇门，没有灯，没有门，没有路。`
	report := Analyze(text)
	if report.Passed() {
		t.Fatalf("expected repair findings, got %+v", report)
	}
	if report.Metrics.MarkdownSubheadings != 1 {
		t.Fatalf("subheadings = %d", report.Metrics.MarkdownSubheadings)
	}
	if report.Metrics.EmDashes == 0 || report.Metrics.TripleParallelPatterns == 0 {
		t.Fatalf("expected dash/parallel metrics, got %+v", report.Metrics)
	}
}

func TestAnalyzeAllowsAPlainChapter(t *testing.T) {
	text := `# 第一章

窗外的雨刚停。许舟把湿伞靠在门边，没急着进屋。

厨房里传来碗筷碰撞声。他听了一会儿，才抬手敲门。`
	if report := Analyze(text); !report.Passed() {
		t.Fatalf("plain chapter should pass: %+v", report)
	}
}

func TestAnalyzeRequiresAUniqueH1ChapterTitle(t *testing.T) {
	report := Analyze("## 第一章\n\n正文。")
	if report.Metrics.InvalidChapterTitles != 1 || report.Passed() {
		t.Fatalf("invalid title report = %+v", report)
	}
}

func TestAuditRepairSummary(t *testing.T) {
	report := Report{Findings: []Finding{{Code: "x", Severity: SeverityAttention}, {Code: "y", Severity: SeverityRepair, RepairHint: "fix it"}}}
	if got := report.RepairSummary(); got != "y: fix it" {
		t.Fatalf("RepairSummary = %q", got)
	}
}

func TestAnalyzeIncludesVerbatimRepairExamples(t *testing.T) {
	report := Analyze("# 第一章\n\n他停在门外——没有进去。\n\n他又走开——没有回头。\n\n他第三次停下——仍旧没有进去。\n\n他第四次停下——没有说话。")
	var dash Finding
	for _, finding := range report.Findings {
		if finding.Code == "em_dash_overuse" {
			dash = finding
			break
		}
	}
	if len(dash.Examples) == 0 || !strings.Contains(dash.Examples[0], "——") {
		t.Fatalf("dash examples = %+v", dash)
	}
}

func TestAnalyzeFlagsAdjacentContradictionAndSubjectRun(t *testing.T) {
	text := `# 第三章

他不知道自己为什么要留着它。

他知道自己为什么要留着它。

他想起她站在窗边。

他想起她低头看表。

他想起她说过的话。

他会搬到她对面去。`
	report := Analyze(text)
	if report.Metrics.AdjacentContradictions != 1 {
		t.Fatalf("adjacent contradictions = %d", report.Metrics.AdjacentContradictions)
	}
	if report.Metrics.SubjectOpeningRuns == 0 {
		t.Fatalf("expected subject opening run: %+v", report.Metrics)
	}
	for _, code := range []string{"adjacent_contradiction", "subject_opening_run", "repeated_sentence_opening"} {
		found := false
		for _, finding := range report.Findings {
			found = found || finding.Code == code
		}
		if !found {
			t.Fatalf("missing finding %q: %+v", code, report.Findings)
		}
	}
}

func TestAnalyzeFlagsManualLikeSpecificationBlocks(t *testing.T) {
	text := `# 第四章

摄像头采用 Sony IMX415 传感器，4K 分辨率，配 12mm 镜头，SSID 连续重试 3 次。

安装麦克风后把缓存写入服务器，码率设为 8Mbps，存储保留 90 天。`
	report := Analyze(text)
	if report.Metrics.ManualSpecParagraphs != 2 {
		t.Fatalf("manual spec paragraphs = %d", report.Metrics.ManualSpecParagraphs)
	}
	if report.Passed() {
		t.Fatalf("manual-like exposition must require repair: %+v", report.Findings)
	}
}

func TestAnalyzeFlagsOperationalRuleRecitalAsManualLike(t *testing.T) {
	text := `# 第十七章

“远程抽查，整点与错峰各一次。应答不得超过四十秒。活动区不变，异常直接记录。”`
	report := Analyze(text)
	if report.Metrics.ManualSpecParagraphs != 1 || report.Passed() {
		t.Fatalf("operational rule recital must require repair: %+v", report)
	}
}

func TestAnalyzeDoesNotTreatNarrativeServerReferenceAsManualLike(t *testing.T) {
	text := `# 第十六章

同一时刻，公司的服务器里，一封新邮件滑进收件箱。协作方第二次催促，限时四十八小时。`
	report := Analyze(text)
	if report.Metrics.ManualSpecParagraphs != 0 {
		t.Fatalf("narrative server reference was misclassified: %+v", report)
	}
}

func TestAnalyzeMakesSevereFragmentationBlocking(t *testing.T) {
	var paragraphs []string
	for index := 0; index < 24; index++ {
		paragraphs = append(paragraphs, "雨还在下。")
	}
	report := Analyze("# 第一章\n\n" + strings.Join(paragraphs, "\n\n"))
	for _, finding := range report.Findings {
		if finding.Code == "fragmented_paragraph_rhythm" && finding.Severity == SeverityRepair {
			return
		}
	}
	t.Fatalf("severe fragmentation was not blocking: %+v", report.Findings)
}

func TestAnalyzeDoesNotBlockDialogueHeavyChapterByMeanLengthAlone(t *testing.T) {
	var paragraphs []string
	for index := 0; index < 24; index++ {
		paragraphs = append(paragraphs, "“我知道。”", "他看了她一眼，随后移开视线。")
	}
	report := Analyze("# 第一章\n\n" + strings.Join(paragraphs, "\n\n"))
	for _, finding := range report.Findings {
		if finding.Code == "fragmented_paragraph_rhythm" && finding.Severity == SeverityRepair {
			t.Fatalf("dialogue rhythm must not be blocked by a low global mean: %+v", finding)
		}
	}
}

func TestAnalyzeFlagsRepeatedRoomNumberAnchors(t *testing.T) {
	text := "# 第四章\n\n" + strings.Repeat("他从602走到604，对照602与604的位置。\n\n", 3)
	report := Analyze(text)
	if report.Metrics.MaxNumericAnchorRepeat <= 5 {
		t.Fatalf("numeric anchor repeat = %d", report.Metrics.MaxNumericAnchorRepeat)
	}
	for _, finding := range report.Findings {
		if finding.Code == "numeric_anchor_overuse" && len(finding.Examples) > 0 {
			return
		}
	}
	t.Fatalf("numeric anchor finding missing: %+v", report.Findings)
}
