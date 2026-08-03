// Package deai provides deterministic, prose-focused checks for the distinct
// post-writing de-AI stage. It intentionally reports observable symptoms, not
// a claim that a text was or was not written by a human.
package deai

import (
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

const PolicyVersion = 1

// Severity distinguishes a mandatory repair from a signal that should inform
// the next draft but is not safe to mechanically gate on its own.
type Severity string

const (
	SeverityRepair    Severity = "repair"
	SeverityAttention Severity = "attention"
)

// Finding is a deterministic, localized symptom. It does not pretend to be a
// literary judgment; the Writer remains responsible for choosing the rewrite.
type Finding struct {
	Code       string   `json:"code"`
	Severity   Severity `json:"severity"`
	Actual     int      `json:"actual"`
	Limit      int      `json:"limit,omitempty"`
	Example    string   `json:"example,omitempty"`
	Examples   []string `json:"examples,omitempty"`
	RepairHint string   `json:"repair_hint"`
}

// Metrics is deliberately compact enough to enter a Writer tool result while
// retaining the signals needed for a chapter-level quality decision.
type Metrics struct {
	BodyRunes              int     `json:"body_runes"`
	Paragraphs             int     `json:"paragraphs"`
	Sentences              int     `json:"sentences"`
	MeanSentenceRunes      float64 `json:"mean_sentence_runes"`
	P90SentenceRunes       int     `json:"p90_sentence_runes"`
	MeanParagraphRunes     float64 `json:"mean_paragraph_runes"`
	MaxShortNarrativeRun   int     `json:"max_short_narrative_run"`
	InvalidChapterTitles   int     `json:"invalid_chapter_titles"`
	MarkdownSubheadings    int     `json:"markdown_subheadings"`
	EmDashes               int     `json:"em_dashes"`
	EmDashPerKRunes        float64 `json:"em_dash_per_k_runes"`
	Ellipses               int     `json:"ellipses"`
	Similes                int     `json:"similes"`
	CorrectionSentences    int     `json:"correction_sentences"`
	AbstractHedges         int     `json:"abstract_hedges"`
	ThenParagraphOpeners   int     `json:"then_paragraph_openers"`
	ReactionTemplates      int     `json:"reaction_templates"`
	TripleParallelPatterns int     `json:"triple_parallel_patterns"`
	RepeatedOpeningChains  int     `json:"repeated_opening_chains"`
	AdjacentContradictions int     `json:"adjacent_contradictions"`
	SubjectOpeningRuns     int     `json:"subject_opening_runs"`
	ManualSpecParagraphs   int     `json:"manual_spec_paragraphs"`
	MaxNumericAnchorRepeat int     `json:"max_numeric_anchor_repeat"`
}

// Report is the deterministic output of one post-draft pass.
type Report struct {
	Metrics  Metrics   `json:"metrics"`
	Findings []Finding `json:"findings,omitempty"`
}

// Passed reports whether every objective repair threshold has been met.
func (r Report) Passed() bool {
	for _, finding := range r.Findings {
		if finding.Severity == SeverityRepair {
			return false
		}
	}
	return true
}

// RepairSummary returns the first actionable finding for a concise tool error.
func (r Report) RepairSummary() string {
	for _, finding := range r.Findings {
		if finding.Severity == SeverityRepair {
			return finding.Code + ": " + finding.RepairHint
		}
	}
	return ""
}

// Audit persists the exact draft fingerprint that passed the separate stage.
// A later edit invalidates it by changing DraftSHA256.
type Audit struct {
	Version     int       `json:"version"`
	Chapter     int       `json:"chapter"`
	DraftSHA256 string    `json:"draft_sha256"`
	Passed      bool      `json:"passed"`
	Report      Report    `json:"report"`
	CheckedAt   time.Time `json:"checked_at"`
}

var (
	sentenceSplitRE      = regexp.MustCompile(`[。！？!?]+`)
	simileRE             = regexp.MustCompile(`像一|仿佛|如同|宛如|好似`)
	correctionRE         = regexp.MustCompile(`不是[^。！？\n]{1,28}?[，、]?(?:而)?是`)
	abstractHedgeRE      = regexp.MustCompile(`一种|某种|说不清|道不明|不知为何|难以言说|值得注意的是`)
	reactionRE           = regexp.MustCompile(`沉默了|没有说话|没有回答|没有接话|没有回头`)
	tripleParallelRE     = regexp.MustCompile(`(?:没有|不是|不再|无法|不能)[^。！？\n]{0,32}[，、；](?:没有|不是|不再|无法|不能)[^。！？\n]{0,32}[，、；](?:没有|不是|不再|无法|不能)`)
	manualSpecTermRE     = regexp.MustCompile(`摄像头|麦克风|传感器|分辨率|像素|焦距|镜头|型号|协议|端口|缓存|服务器|SSID|红外|帧率|码率|存储|硬盘|带宽|覆盖率|安装`)
	manualProtocolTermRE = regexp.MustCompile(`抽查|核验|应答|活动区|禁止|触碰|违规|累计|取消|加锁|执行|签收|备档|权限|时段|阈值|流程|步骤|清单|编号|批次|记录|调试`)
	manualConfigVerbRE   = regexp.MustCompile(`采用|配备|配置|设置|设为|加装|接入|回传|保留|覆盖|安装|调试|校准`)
	manualSpecDataRE     = regexp.MustCompile(`(?i)(?:\d+(?:\.\d+)?\s*(?:mm|cm|m|gb|tb|kbps|mbps|k|p|米|分钟|小时|秒|次|度|层|号|组|%|％)?|[零〇一二两三四五六七八九十百半]+\s*(?:米|分钟|小时|秒|次|度|层|号|组))`)
	numericAnchorRE      = regexp.MustCompile(`(?:^|[^\d])(\d{3,4})(?:[^\d]|$)`)
)

// Analyze measures the current chapter body. The first non-empty Markdown
// heading is a legal chapter title; every later heading is an export leak.
func Analyze(text string) Report {
	body, invalidTitle, subheadings := chapterBody(text)
	paragraphs := splitParagraphs(body)
	sentences := splitSentences(body)

	metrics := Metrics{
		BodyRunes:              utf8.RuneCountInString(body),
		Paragraphs:             len(paragraphs),
		Sentences:              len(sentences),
		InvalidChapterTitles:   invalidTitle,
		MarkdownSubheadings:    subheadings,
		EmDashes:               strings.Count(body, "——"),
		Ellipses:               strings.Count(body, "……"),
		Similes:                countMatches(simileRE, body),
		CorrectionSentences:    countMatches(correctionRE, body),
		AbstractHedges:         countMatches(abstractHedgeRE, body),
		ReactionTemplates:      countMatches(reactionRE, body),
		TripleParallelPatterns: countMatches(tripleParallelRE, body),
		ThenParagraphOpeners:   countThenOpeners(paragraphs),
		RepeatedOpeningChains:  repeatedOpeningChains(sentences),
		AdjacentContradictions: adjacentContradictions(sentences),
		SubjectOpeningRuns:     subjectOpeningRuns(paragraphs),
		ManualSpecParagraphs:   manualSpecParagraphs(paragraphs),
		MaxNumericAnchorRepeat: maxNumericAnchorRepeat(body),
		MaxShortNarrativeRun:   maxShortNarrativeRun(paragraphs),
	}
	metrics.MeanSentenceRunes, metrics.P90SentenceRunes = sentenceLengths(sentences)
	metrics.MeanParagraphRunes = paragraphMean(paragraphs)
	if metrics.BodyRunes > 0 {
		metrics.EmDashPerKRunes = round2(float64(metrics.EmDashes) * 1000 / float64(metrics.BodyRunes))
	}

	report := Report{Metrics: metrics}
	report.Findings = append(report.Findings, findingsFor(metrics)...)
	attachExamples(report.Findings, text)
	return report
}

// attachExamples gives the model verbatim, bounded evidence rather than only
// aggregate counters. A de-AI report without locations repeatedly caused
// models to guess at edit_chapter arguments, which creates a retry loop instead
// of a real prose repair.
func attachExamples(findings []Finding, text string) {
	for i := range findings {
		findings[i].Examples = findingExamples(findings[i].Code, text)
		if len(findings[i].Examples) > 0 {
			findings[i].Example = findings[i].Examples[0]
		}
	}
}

func findingExamples(code, text string) []string {
	var examples []string
	switch code {
	case "chapter_title_format":
		for _, line := range strings.Split(text, "\n") {
			if line = strings.TrimSpace(line); line != "" {
				examples = append(examples, line)
				break
			}
		}
	case "markdown_subheading":
		seenFirst := false
		for _, line := range strings.Split(text, "\n") {
			trimmed := strings.TrimSpace(line)
			if trimmed == "" {
				continue
			}
			if !seenFirst {
				seenFirst = true
				continue
			}
			if strings.HasPrefix(trimmed, "#") {
				examples = append(examples, trimmed)
			}
		}
	case "em_dash_overuse":
		examples = examplesAroundLiteral(text, "——")
	case "correction_sentence_overuse":
		examples = examplesAroundMatches(text, correctionRE)
	case "simile_overuse":
		examples = examplesAroundMatches(text, simileRE)
	case "abstract_hedge_overuse":
		examples = examplesAroundMatches(text, abstractHedgeRE)
	case "reaction_template_overuse":
		examples = examplesAroundMatches(text, reactionRE)
	case "triple_parallelism":
		examples = examplesAroundMatches(text, tripleParallelRE)
	case "then_opener_overuse":
		for _, paragraph := range splitParagraphs(text) {
			trimmed := strings.TrimLeft(strings.TrimSpace(paragraph), "“”‘’\"'「」『』…—-，、；：")
			if strings.HasPrefix(trimmed, "然后") {
				examples = append(examples, truncateExample(paragraph))
			}
		}
	case "repeated_sentence_opening":
		examples = repeatedOpeningChainExamples(splitSentences(text))
	case "adjacent_contradiction":
		sentences := splitSentences(text)
		for index := 0; index+1 < len(sentences); index++ {
			if oppositeAssertion(sentences[index], sentences[index+1]) {
				examples = append(examples, truncateExample(sentences[index]+"。"+sentences[index+1]+"。"))
			}
		}
	case "subject_opening_run":
		examples = subjectOpeningRunExamples(splitParagraphs(text))
	case "manual_spec_exposition":
		for _, paragraph := range splitParagraphs(text) {
			if isManualSpecParagraph(paragraph) {
				examples = append(examples, truncateExample(paragraph))
			}
		}
	case "numeric_anchor_overuse":
		anchor, _ := dominantNumericAnchor(text)
		if anchor != "" {
			examples = examplesAroundLiteral(text, anchor)
		}
	}
	return uniqueExamples(examples)
}

func examplesAroundLiteral(text, literal string) []string {
	var examples []string
	start := 0
	for {
		idx := strings.Index(text[start:], literal)
		if idx < 0 {
			break
		}
		idx += start
		examples = append(examples, sentenceAround(text, idx))
		start = idx + len(literal)
	}
	return uniqueExamples(examples)
}

func examplesAroundMatches(text string, re *regexp.Regexp) []string {
	var examples []string
	for _, loc := range re.FindAllStringIndex(text, -1) {
		examples = append(examples, sentenceAround(text, loc[0]))
	}
	return uniqueExamples(examples)
}

func sentenceAround(text string, at int) string {
	left := 0
	if previous := strings.LastIndexAny(text[:at], "。！？!?\n"); previous >= 0 {
		_, width := utf8.DecodeRuneInString(text[previous:])
		left = previous + width
	}
	right := len(text)
	if next := strings.IndexAny(text[at:], "。！？!?\n"); next >= 0 {
		idx := at + next
		_, width := utf8.DecodeRuneInString(text[idx:])
		right = idx + width
	}
	return truncateExample(strings.TrimSpace(text[left:right]))
}

func uniqueExamples(examples []string) []string {
	seen := make(map[string]struct{}, len(examples))
	result := make([]string, 0, 3)
	for _, example := range examples {
		example = strings.TrimSpace(example)
		if example == "" {
			continue
		}
		if _, ok := seen[example]; ok {
			continue
		}
		seen[example] = struct{}{}
		result = append(result, example)
		if len(result) == 3 {
			break
		}
	}
	return result
}

func truncateExample(text string) string {
	runes := []rune(text)
	if len(runes) <= 180 {
		return text
	}
	return string(runes[:180])
}

func findingsFor(m Metrics) []Finding {
	var findings []Finding
	if m.InvalidChapterTitles > 0 {
		findings = append(findings, Finding{
			Code:       "chapter_title_format",
			Severity:   SeverityRepair,
			Actual:     m.InvalidChapterTitles,
			RepairHint: "首个非空行必须是唯一的一级章标题（# 标题）；不要用 ## 第…章或无标题正文。",
		})
	}
	if m.MarkdownSubheadings > 0 {
		findings = append(findings, Finding{
			Code:       "markdown_subheading",
			Severity:   SeverityRepair,
			Actual:     m.MarkdownSubheadings,
			RepairHint: "正文只能保留章标题；删去 ##/### 和章内编号小标题，用空行或场景动作自然转场。",
		})
	}
	if limit := max(3, ceilDiv(m.BodyRunes, 500)); m.EmDashes > limit {
		findings = append(findings, Finding{
			Code:       "em_dash_overuse",
			Severity:   SeverityRepair,
			Actual:     m.EmDashes,
			Limit:      limit,
			RepairHint: "只保留真正的语气中断；把解释性破折号改为动作、短句、环境细节或换段。",
		})
	}
	if m.TripleParallelPatterns > 1 {
		findings = append(findings, Finding{
			Code:       "triple_parallelism",
			Severity:   SeverityRepair,
			Actual:     m.TripleParallelPatterns,
			Limit:      1,
			RepairHint: "每处只保留最有力的一项，把其余并列否定改为具体后果、动作或留白。",
		})
	}
	if limit := max(2, ceilDiv(m.BodyRunes, 1000)); m.CorrectionSentences > limit {
		findings = append(findings, Finding{
			Code:       "correction_sentence_overuse",
			Severity:   SeverityRepair,
			Actual:     m.CorrectionSentences,
			Limit:      limit,
			RepairHint: "减少“不是……而是……”式替读者点题；用人物选择、失败动作或未说出口的话呈现差别。",
		})
	}
	if limit := max(4, ceilDiv(m.BodyRunes, 650)); m.Similes > limit {
		findings = append(findings, Finding{
			Code:       "simile_overuse",
			Severity:   SeverityRepair,
			Actual:     m.Similes,
			Limit:      limit,
			RepairHint: "删去自动生成的明喻；保留的比喻必须来自角色经验，其他地方用准确动词或白描。",
		})
	}
	if limit := max(3, ceilDiv(m.BodyRunes, 800)); m.AbstractHedges > limit {
		findings = append(findings, Finding{
			Code:       "abstract_hedge_overuse",
			Severity:   SeverityRepair,
			Actual:     m.AbstractHedges,
			Limit:      limit,
			RepairHint: "删除“一种/说不清”等替读者概括的缓冲语，让可观察的事实和选择自己成立。",
		})
	}
	if limit := max(2, ceilDiv(m.Paragraphs, 35)); m.ThenParagraphOpeners > limit {
		findings = append(findings, Finding{
			Code:       "then_opener_overuse",
			Severity:   SeverityRepair,
			Actual:     m.ThenParagraphOpeners,
			Limit:      limit,
			RepairHint: "不要连续用“然后”驱动叙述；让上一动作的结果、物件、声音或人物反应接住下一段。",
		})
	}
	if limit := max(2, ceilDiv(m.Paragraphs, 25)); m.ReactionTemplates > limit {
		findings = append(findings, Finding{
			Code:       "reaction_template_overuse",
			Severity:   SeverityRepair,
			Actual:     m.ReactionTemplates,
			Limit:      limit,
			RepairHint: "不要用“沉默了/没有回答”替代反应；为关键停顿换成带关系、目的或身体状态的具体行为。",
		})
	}
	if m.RepeatedOpeningChains > 0 {
		findings = append(findings, Finding{
			Code:       "repeated_sentence_opening",
			Severity:   SeverityRepair,
			Actual:     m.RepeatedOpeningChains,
			Limit:      0,
			RepairHint: "拆开连续同起手句；改变观察焦点和句法，而不是只替换一个形容词。",
		})
	}
	if m.AdjacentContradictions > 0 {
		findings = append(findings, Finding{
			Code:       "adjacent_contradiction",
			Severity:   SeverityRepair,
			Actual:     m.AdjacentContradictions,
			Limit:      0,
			RepairHint: "相邻句对同一事实作出肯定与否定时，保留符合人物当下认知的一句；若要表现转念，必须写出触发转变的新证据。",
		})
	}
	if m.SubjectOpeningRuns > 0 {
		findings = append(findings, Finding{
			Code:       "subject_opening_run",
			Severity:   SeverityRepair,
			Actual:     m.SubjectOpeningRuns,
			Limit:      0,
			RepairHint: "连续四段以上以“他/她/我”起笔会形成模型式匀速铺陈；合并同一动作链，并让物件、声音、对话或动作后果接管部分段首。",
		})
	}
	if m.ManualSpecParagraphs > 0 {
		findings = append(findings, Finding{
			Code:       "manual_spec_exposition",
			Severity:   SeverityRepair,
			Actual:     m.ManualSpecParagraphs,
			Limit:      0,
			RepairHint: "删除型号、尺寸、协议、覆盖率、操作时限和执行规则的清单式说明；把必要信息藏进人物受阻、误判或选择，只保留会改变关系或危险感的一两个细节。",
		})
	}
	if m.MaxNumericAnchorRepeat > 5 {
		findings = append(findings, Finding{
			Code:       "numeric_anchor_overuse",
			Severity:   SeverityRepair,
			Actual:     m.MaxNumericAnchorRepeat,
			Limit:      5,
			RepairHint: "房号等数字地点在首次建立空间关系后，改用“对门、隔壁、屋内、走廊”等自然指代；只有换场或可能混淆时才重提编号。",
		})
	}
	if (m.MeanParagraphRunes > 0 && m.MeanParagraphRunes < 45) || m.MaxShortNarrativeRun >= 3 {
		severity := SeverityAttention
		actual := int(m.MeanParagraphRunes + 0.5)
		limit := 45
		if m.MaxShortNarrativeRun >= 5 {
			severity = SeverityRepair
			actual = m.MaxShortNarrativeRun
			limit = 4
		}
		findings = append(findings, Finding{
			Code:       "fragmented_paragraph_rhythm",
			Severity:   severity,
			Actual:     actual,
			Limit:      limit,
			RepairHint: "段落过碎时，合并同一动作链中的孤立短句；保留真正的冲击、转场和对白停顿。",
		})
	}
	return findings
}

func chapterBody(text string) (string, int, int) {
	lines := strings.Split(text, "\n")
	seenFirst := false
	invalidTitle := 0
	subheadings := 0
	body := make([]string, 0, len(lines))
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			if seenFirst {
				body = append(body, "")
			}
			continue
		}
		if !seenFirst {
			seenFirst = true
			if strings.HasPrefix(trimmed, "# ") {
				continue
			}
			invalidTitle = 1
			if strings.HasPrefix(trimmed, "#") {
				continue
			}
		}
		if strings.HasPrefix(trimmed, "#") {
			subheadings++
			continue
		}
		body = append(body, line)
	}
	return strings.TrimSpace(strings.Join(body, "\n")), invalidTitle, subheadings
}

func splitParagraphs(text string) []string {
	parts := strings.FieldsFunc(text, func(r rune) bool { return r == '\n' || r == '\r' })
	paragraphs := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			paragraphs = append(paragraphs, part)
		}
	}
	return paragraphs
}

func maxShortNarrativeRun(paragraphs []string) int {
	maxRun := 0
	currentRun := 0
	for _, paragraph := range paragraphs {
		trimmed := strings.TrimSpace(paragraph)
		if isDialogueParagraph(trimmed) || utf8.RuneCountInString(trimmed) > 18 {
			currentRun = 0
			continue
		}
		currentRun++
		if currentRun > maxRun {
			maxRun = currentRun
		}
	}
	return maxRun
}

func isDialogueParagraph(paragraph string) bool {
	if paragraph == "" {
		return false
	}
	_, size := utf8.DecodeRuneInString(paragraph)
	switch paragraph[:size] {
	case "“", "「", "『", "‘", "\"", "'":
		return true
	default:
		return false
	}
}

func splitSentences(text string) []string {
	raw := sentenceSplitRE.Split(text, -1)
	sentences := make([]string, 0, len(raw))
	for _, sentence := range raw {
		sentence = strings.Trim(strings.TrimSpace(sentence), "“”‘’\"'「」『』")
		if utf8.RuneCountInString(sentence) >= 2 {
			sentences = append(sentences, sentence)
		}
	}
	return sentences
}

func countMatches(re *regexp.Regexp, text string) int { return len(re.FindAllStringIndex(text, -1)) }

func countThenOpeners(paragraphs []string) int {
	count := 0
	for _, paragraph := range paragraphs {
		trimmed := strings.TrimLeft(strings.TrimSpace(paragraph), "“”‘’\"'「」『』…—-，、；：")
		if strings.HasPrefix(trimmed, "然后") {
			count++
		}
	}
	return count
}

func repeatedOpeningChains(sentences []string) int {
	chains := 0
	last := ""
	run := 0
	for _, sentence := range sentences {
		key := openingKey(sentence)
		if key == "" {
			last, run = "", 0
			continue
		}
		if key == last {
			run++
		} else {
			last, run = key, 1
		}
		if run == 3 {
			chains++
		}
	}
	return chains
}

func repeatedOpeningChainExamples(sentences []string) []string {
	var examples []string
	for index := 0; index+2 < len(sentences); index++ {
		key := openingKey(sentences[index])
		if key != "" && openingKey(sentences[index+1]) == key && openingKey(sentences[index+2]) == key {
			examples = append(examples, truncateExample(strings.Join(sentences[index:index+3], "。")+"。"))
			index += 2
		}
	}
	return uniqueExamples(examples)
}

func openingKey(sentence string) string {
	trimmed := strings.TrimLeft(strings.TrimSpace(sentence), "“”‘’\"'「」『』…—-，、；：")
	runes := []rune(trimmed)
	if len(runes) < 3 {
		return ""
	}
	return string(runes[:3])
}

func adjacentContradictions(sentences []string) int {
	count := 0
	for index := 0; index+1 < len(sentences); index++ {
		if oppositeAssertion(sentences[index], sentences[index+1]) {
			count++
		}
	}
	return count
}

func oppositeAssertion(left, right string) bool {
	pairs := [][2]string{
		{"不知道", "知道"},
		{"不明白", "明白"},
		{"不记得", "记得"},
		{"不认识", "认识"},
	}
	left = compactAssertion(left)
	right = compactAssertion(right)
	for _, pair := range pairs {
		leftNegative := strings.Replace(left, pair[0], pair[1], 1)
		rightNegative := strings.Replace(right, pair[0], pair[1], 1)
		if (leftNegative == right && leftNegative != left) || (rightNegative == left && rightNegative != right) {
			return true
		}
	}
	return false
}

func compactAssertion(value string) string {
	return strings.Map(func(r rune) rune {
		if strings.ContainsRune(" \t\r\n，、；：。“”‘’\"'！？?!", r) {
			return -1
		}
		return r
	}, strings.TrimSpace(value))
}

func subjectOpeningRuns(paragraphs []string) int {
	runs := 0
	current := ""
	length := 0
	for _, paragraph := range paragraphs {
		subject := paragraphOpeningSubject(paragraph)
		if subject == "" {
			current, length = "", 0
			continue
		}
		if subject == current {
			length++
		} else {
			current, length = subject, 1
		}
		if length == 4 {
			runs++
		}
	}
	return runs
}

func subjectOpeningRunExamples(paragraphs []string) []string {
	var examples []string
	for index := 0; index+3 < len(paragraphs); index++ {
		subject := paragraphOpeningSubject(paragraphs[index])
		if subject == "" {
			continue
		}
		matched := true
		for next := index + 1; next <= index+3; next++ {
			if paragraphOpeningSubject(paragraphs[next]) != subject {
				matched = false
				break
			}
		}
		if matched {
			examples = append(examples, truncateExample(strings.Join(paragraphs[index:index+4], "\n")))
			index += 3
		}
	}
	return uniqueExamples(examples)
}

func paragraphOpeningSubject(paragraph string) string {
	trimmed := strings.TrimLeft(strings.TrimSpace(paragraph), "“”‘’\"'「」『』…—-，、；：")
	for _, subject := range []string{"他", "她", "我"} {
		if strings.HasPrefix(trimmed, subject) {
			return subject
		}
	}
	return ""
}

func manualSpecParagraphs(paragraphs []string) int {
	count := 0
	for _, paragraph := range paragraphs {
		if isManualSpecParagraph(paragraph) {
			count++
		}
	}
	return count
}

func isManualSpecParagraph(paragraph string) bool {
	terms := manualSpecTermRE.FindAllString(paragraph, -1)
	protocolTerms := manualProtocolTermRE.FindAllString(paragraph, -1)
	data := manualSpecDataRE.FindAllString(paragraph, -1)
	return len(terms) >= 2 ||
		(len(terms) >= 1 && len(data) >= 2 && manualConfigVerbRE.MatchString(paragraph)) ||
		(len(protocolTerms) >= 3 && len(data) >= 1)
}

func maxNumericAnchorRepeat(text string) int {
	_, count := dominantNumericAnchor(text)
	return count
}

func dominantNumericAnchor(text string) (string, int) {
	counts := make(map[string]int)
	for _, match := range numericAnchorRE.FindAllStringSubmatch(text, -1) {
		if len(match) < 2 || isCalendarYear(match[1]) {
			continue
		}
		counts[match[1]]++
	}
	anchor, count := "", 0
	for candidate, candidateCount := range counts {
		if candidateCount > count || (candidateCount == count && candidate < anchor) {
			anchor, count = candidate, candidateCount
		}
	}
	return anchor, count
}

func isCalendarYear(value string) bool {
	return len(value) == 4 && value >= "1900" && value <= "2099"
}

func sentenceLengths(sentences []string) (float64, int) {
	if len(sentences) == 0 {
		return 0, 0
	}
	lengths := make([]int, 0, len(sentences))
	total := 0
	for _, sentence := range sentences {
		n := utf8.RuneCountInString(sentence)
		lengths = append(lengths, n)
		total += n
	}
	sort.Ints(lengths)
	idx := (len(lengths)*90+99)/100 - 1
	return round2(float64(total) / float64(len(lengths))), lengths[idx]
}

func paragraphMean(paragraphs []string) float64 {
	if len(paragraphs) == 0 {
		return 0
	}
	total := 0
	for _, paragraph := range paragraphs {
		total += utf8.RuneCountInString(paragraph)
	}
	return round2(float64(total) / float64(len(paragraphs)))
}

func ceilDiv(value, divisor int) int {
	if value <= 0 || divisor <= 0 {
		return 0
	}
	return (value + divisor - 1) / divisor
}

func round2(value float64) float64 { return float64(int(value*100+0.5)) / 100 }
