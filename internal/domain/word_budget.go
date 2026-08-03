package domain

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	WordBudgetSourcePrompt = "prompt"
	WordBudgetSourceAPI    = "api"

	defaultWordBudgetTolerance = 0.10
	minPromptTotalWords        = 5000
)

// WordBudget is the persisted book-level word budget contract.
type WordBudget struct {
	TargetTotalWords int    `json:"target_total_words"`
	TotalMinWords    int    `json:"total_min_words"`
	TotalMaxWords    int    `json:"total_max_words"`
	PlannedChapters  int    `json:"planned_chapters"`
	ChapterMinWords  int    `json:"chapter_min_words"`
	ChapterMaxWords  int    `json:"chapter_max_words"`
	Source           string `json:"source"`
}

type WordBudgetTarget struct {
	TargetTotalWords int    `json:"target_total_words"`
	TotalMinWords    int    `json:"total_min_words"`
	TotalMaxWords    int    `json:"total_max_words"`
	PlannedChapters  int    `json:"planned_chapters,omitempty"`
	ChapterMinWords  int    `json:"chapter_min_words,omitempty"`
	ChapterMaxWords  int    `json:"chapter_max_words,omitempty"`
	Source           string `json:"source"`
}

type WordBudgetProgress struct {
	CompletedChapters int `json:"completed_chapters"`
	CompletedWords    int `json:"completed_words"`
}

type WordBudgetRemaining struct {
	TargetWords int `json:"target_words"`
	MinWords    int `json:"min_words"`
	MaxWords    int `json:"max_words"`
	Chapters    int `json:"chapters"`
}

type CurrentChapterWordBudget struct {
	Chapter             int `json:"chapter"`
	RecommendedMinWords int `json:"recommended_min_words"`
	RecommendedMaxWords int `json:"recommended_max_words"`
}

// ChapterWordBudgetPolicy separates the rolling recommendation from the
// enforceable chapter range. Recommendations help pace a book toward its
// total target; they must not become a destructive per-chapter rewrite loop.
type ChapterWordBudgetPolicy struct {
	Chapter             int `json:"chapter"`
	RecommendedMinWords int `json:"recommended_min_words"`
	RecommendedMaxWords int `json:"recommended_max_words"`
	HardMinWords        int `json:"hard_min_words"`
	HardMaxWords        int `json:"hard_max_words"`
}

func (p ChapterWordBudgetPolicy) WithinRecommendation(wordCount int) bool {
	return wordCount >= p.RecommendedMinWords && wordCount <= p.RecommendedMaxWords
}

func (p ChapterWordBudgetPolicy) WithinHardRange(wordCount int) bool {
	return wordCount >= p.HardMinWords && wordCount <= p.HardMaxWords
}

// ResolveChapterWordBudgetPolicy treats both the rolling allocation and a
// declared per-chapter range as planning guidance. The wider hard range is
// only a runaway-output safety fence. This keeps a content-complete chapter
// from entering a destructive trim loop merely because it modestly exceeds
// the estimate, while still catching obviously truncated or inflated drafts.
//
// The approximate book total never shrinks the safety range: later chapters
// must not be compressed because earlier chapters legitimately needed room.
func ResolveChapterWordBudgetPolicy(runtime WordBudgetRuntime, declaredMin, declaredMax int, hasDeclaredRange bool) (ChapterWordBudgetPolicy, bool) {
	current := runtime.CurrentChapter
	if current.Chapter <= 0 || current.RecommendedMinWords <= 0 || current.RecommendedMaxWords < current.RecommendedMinWords {
		return ChapterWordBudgetPolicy{}, false
	}
	softMin := current.RecommendedMinWords
	softMax := current.RecommendedMaxWords
	if hasDeclaredRange {
		if declaredMin > 0 {
			softMin = declaredMin
		}
		if declaredMax > 0 {
			softMax = declaredMax
		}
		if softMin <= 0 {
			softMin = current.RecommendedMinWords
		}
		if softMax <= 0 {
			softMax = current.RecommendedMaxWords
		}
	}
	hardMin := softMin * 2 / 3
	hardMax := softMax * 5 / 4
	if hardMin <= 0 || hardMax < hardMin {
		return ChapterWordBudgetPolicy{}, false
	}
	return ChapterWordBudgetPolicy{
		Chapter:             current.Chapter,
		RecommendedMinWords: current.RecommendedMinWords,
		RecommendedMaxWords: current.RecommendedMaxWords,
		HardMinWords:        hardMin,
		HardMaxWords:        hardMax,
	}, true
}

type WordBudgetCurrentChapter struct {
	Chapter            int `json:"chapter"`
	MinWords           int `json:"min_words"`
	MaxWords           int `json:"max_words"`
	TargetWords        int `json:"target_words"`
	RemainingTarget    int `json:"remaining_target_words"`
	RemainingChapters  int `json:"remaining_chapters"`
	CompletedOtherWord int `json:"completed_other_words"`
}

type WordBudgetRuntime struct {
	Target         WordBudgetTarget         `json:"target"`
	Progress       WordBudgetProgress       `json:"progress"`
	Remaining      WordBudgetRemaining      `json:"remaining"`
	CurrentChapter CurrentChapterWordBudget `json:"current_chapter,omitempty"`
}

func NewWordBudgetFromTarget(total int, source string) (*WordBudget, bool) {
	if total <= 0 {
		return nil, false
	}
	min, max := totalRange(total)
	b := WordBudget{
		TargetTotalWords: total,
		TotalMinWords:    min,
		TotalMaxWords:    max,
		Source:           normalizeWordBudgetSource(source),
	}
	return &b, true
}

func NewWordBudget(total int, source string) *WordBudget {
	budget, _ := NewWordBudgetFromTarget(total, source)
	return budget
}

func NewWordBudgetFromRange(minWords, maxWords int, source string) (*WordBudget, bool) {
	if minWords <= 0 || maxWords <= 0 {
		return nil, false
	}
	if minWords > maxWords {
		minWords, maxWords = maxWords, minWords
	}
	b := WordBudget{
		TargetTotalWords: (minWords + maxWords) / 2,
		TotalMinWords:    minWords,
		TotalMaxWords:    maxWords,
		Source:           normalizeWordBudgetSource(source),
	}
	return &b, true
}

func WordBudgetFromAPITarget(total int) (*WordBudget, error) {
	if total < 0 {
		return nil, fmt.Errorf("target_total_words must be a non-negative integer")
	}
	if total == 0 {
		return nil, nil
	}
	b, _ := NewWordBudgetFromTarget(total, WordBudgetSourceAPI)
	return b, nil
}

func (b WordBudget) Normalized() (WordBudget, bool) {
	nb, ok := b.NormalizedNoChapterRecalc()
	if !ok {
		return WordBudget{}, false
	}
	if nb.PlannedChapters > 0 {
		nb = nb.WithPlannedChapters(nb.PlannedChapters)
	}
	return nb, true
}

func (b WordBudget) NormalizedNoChapterRecalc() (WordBudget, bool) {
	if b.TotalMinWords > 0 && b.TotalMaxWords > 0 && b.TotalMinWords > b.TotalMaxWords {
		b.TotalMinWords, b.TotalMaxWords = b.TotalMaxWords, b.TotalMinWords
	}
	if b.TargetTotalWords <= 0 && b.TotalMinWords > 0 && b.TotalMaxWords > 0 {
		b.TargetTotalWords = (b.TotalMinWords + b.TotalMaxWords) / 2
	}
	if b.TargetTotalWords <= 0 {
		return WordBudget{}, false
	}
	if b.TotalMinWords <= 0 || b.TotalMaxWords <= 0 {
		b.TotalMinWords, b.TotalMaxWords = totalRange(b.TargetTotalWords)
	}
	if b.Source == "" {
		b.Source = WordBudgetSourcePrompt
	}
	return b, true
}

func (b WordBudget) WithPlannedChapters(chapters int) WordBudget {
	nb, ok := b.NormalizedNoChapterRecalc()
	if !ok {
		return b
	}
	if chapters <= 0 {
		nb.PlannedChapters = 0
		nb.ChapterMinWords = 0
		nb.ChapterMaxWords = 0
		return nb
	}
	nb.PlannedChapters = chapters
	nb.ChapterMinWords = ceilDiv(nb.TotalMinWords, chapters)
	nb.ChapterMaxWords = floorDiv(nb.TotalMaxWords, chapters)
	if nb.ChapterMaxWords < nb.ChapterMinWords {
		nb.ChapterMaxWords = nb.ChapterMinWords
	}
	return nb
}

func (b WordBudget) ChapterRange() (minWords, maxWords int, ok bool) {
	nb, ok := b.Normalized()
	if !ok || nb.ChapterMinWords <= 0 || nb.ChapterMaxWords <= 0 {
		return 0, 0, false
	}
	return nb.ChapterMinWords, nb.ChapterMaxWords, true
}

func (b WordBudget) Runtime(progress *Progress, chapter int) (WordBudgetRuntime, bool) {
	nb, ok := b.Normalized()
	if !ok {
		return WordBudgetRuntime{}, false
	}
	completedChapters := 0
	completedWords := 0
	totalChapters := nb.PlannedChapters
	if progress != nil {
		completedChapters = len(progress.CompletedChapters)
		completedWords = progress.TotalWordCount
		if totalChapters <= 0 {
			totalChapters = progress.TotalChapters
		}
	}
	if totalChapters < completedChapters {
		totalChapters = completedChapters
	}
	runtime := WordBudgetRuntime{
		Target: WordBudgetTarget{
			TargetTotalWords: nb.TargetTotalWords,
			TotalMinWords:    nb.TotalMinWords,
			TotalMaxWords:    nb.TotalMaxWords,
			PlannedChapters:  totalChapters,
			ChapterMinWords:  nb.ChapterMinWords,
			ChapterMaxWords:  nb.ChapterMaxWords,
			Source:           nb.Source,
		},
		Progress: WordBudgetProgress{
			CompletedChapters: completedChapters,
			CompletedWords:    completedWords,
		},
		Remaining: WordBudgetRemaining{
			TargetWords: maxInt(nb.TargetTotalWords-completedWords, 0),
			MinWords:    maxInt(nb.TotalMinWords-completedWords, 0),
			MaxWords:    maxInt(nb.TotalMaxWords-completedWords, 0),
			Chapters:    maxInt(totalChapters-completedChapters, 0),
		},
	}
	if chapter > 0 {
		minWords, maxWords, rangeOK := nb.RecommendedChapterRange(progress, chapter)
		if rangeOK {
			runtime.CurrentChapter = CurrentChapterWordBudget{
				Chapter:             chapter,
				RecommendedMinWords: minWords,
				RecommendedMaxWords: maxWords,
			}
		}
	}
	return runtime, true
}

func (b WordBudget) RecommendedChapterRange(progress *Progress, chapter int) (int, int, bool) {
	nb, ok := b.Normalized()
	if !ok {
		return 0, 0, false
	}
	staticMin, staticMax, hasStatic := nb.ChapterRange()
	if progress == nil || nb.PlannedChapters <= 0 || progress.TotalWordCount <= 0 {
		return staticMin, staticMax, hasStatic
	}
	if progress.ChapterWordCounts != nil {
		if _, completed := progress.ChapterWordCounts[chapter]; completed {
			return staticMin, staticMax, hasStatic
		}
	}
	completedChapters := len(progress.CompletedChapters)
	remainingChapters := nb.PlannedChapters - completedChapters
	if remainingChapters <= 0 {
		return staticMin, staticMax, hasStatic
	}
	dynMin := ceilDiv(maxInt(nb.TotalMinWords-progress.TotalWordCount, 0), remainingChapters)
	dynMax := floorDiv(maxInt(nb.TotalMaxWords-progress.TotalWordCount, 0), remainingChapters)
	if dynMax < dynMin {
		dynMax = dynMin
	}
	if dynMin == 0 && dynMax == 0 {
		return staticMin, staticMax, hasStatic
	}
	return dynMin, dynMax, true
}

// CurrentChapter preserves the legacy shape used by older internal call sites.
func (b WordBudget) CurrentChapter(progress *Progress, chapter int) (WordBudgetCurrentChapter, bool) {
	runtime, ok := b.Runtime(progress, chapter)
	if !ok || runtime.CurrentChapter.Chapter == 0 {
		return WordBudgetCurrentChapter{}, false
	}
	target := 0
	remainingChapters := runtime.Remaining.Chapters
	if remainingChapters > 0 {
		target = ceilDiv(runtime.Remaining.TargetWords, remainingChapters)
	}
	return WordBudgetCurrentChapter{
		Chapter:            runtime.CurrentChapter.Chapter,
		MinWords:           runtime.CurrentChapter.RecommendedMinWords,
		MaxWords:           runtime.CurrentChapter.RecommendedMaxWords,
		TargetWords:        target,
		RemainingTarget:    runtime.Remaining.TargetWords,
		RemainingChapters:  runtime.Remaining.Chapters,
		CompletedOtherWord: runtime.Progress.CompletedWords,
	}, true
}

func ParseWordBudgetFromText(text, source string) (*WordBudget, bool) {
	text = normalizeWordBudgetText(text)
	if strings.TrimSpace(text) == "" {
		return nil, false
	}
	source = normalizeWordBudgetSource(source)
	if b, ok := parseWordBudgetRange(text, source); ok {
		return b, true
	}
	return parseWordBudgetSingle(text, source)
}

func totalRange(target int) (int, int) {
	minWords := int(math.Round(float64(target) * (1 - defaultWordBudgetTolerance)))
	maxWords := int(math.Round(float64(target) * (1 + defaultWordBudgetTolerance)))
	if minWords < 1 {
		minWords = 1
	}
	if maxWords < minWords {
		maxWords = minWords
	}
	return minWords, maxWords
}

func ceilDiv(n, d int) int {
	if d <= 0 || n <= 0 {
		return 0
	}
	return (n + d - 1) / d
}

func floorDiv(n, d int) int {
	if d <= 0 || n <= 0 {
		return 0
	}
	return n / d
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func normalizeWordBudgetSource(source string) string {
	source = strings.TrimSpace(source)
	if source == "" {
		return WordBudgetSourcePrompt
	}
	return source
}

var (
	budgetNumberPattern = `([0-9０-９]+(?:\.[0-9０-９]+)?|[一二三四五六七八九十百千万萬两兩]+)`
	budgetUnitPattern   = `(万|萬|w|W|k|K|千|million|m)?`
	budgetRangeRe       = regexp.MustCompile(budgetNumberPattern + `\s*` + budgetUnitPattern + `\s*(?:-|~|–|—|到|至)\s*` + budgetNumberPattern + `\s*` + budgetUnitPattern + `\s*(?:字|words?|runes?)?`)
	budgetSingleRe      = regexp.MustCompile(budgetNumberPattern + `\s*` + budgetUnitPattern + `\s*(?:字|words?|runes?)`)
	budgetUnitOnlyRe    = regexp.MustCompile(budgetNumberPattern + `\s*(万|萬|w|W|k|K|千|million|m)`)
)

func parseWordBudgetRange(text, source string) (*WordBudget, bool) {
	matches := budgetRangeRe.FindAllStringSubmatchIndex(text, -1)
	for _, match := range matches {
		if len(match) < 10 || wordBudgetLooksPerChapter(text, match[0], match[1]) {
			continue
		}
		firstRaw := text[match[2]:match[3]]
		firstUnit := submatchText(text, match[4], match[5])
		secondRaw := text[match[6]:match[7]]
		secondUnit := submatchText(text, match[8], match[9])
		if firstUnit == "" {
			firstUnit = secondUnit
		}
		if secondUnit == "" {
			secondUnit = firstUnit
		}
		minWords, ok1 := parseBudgetNumber(firstRaw, firstUnit)
		maxWords, ok2 := parseBudgetNumber(secondRaw, secondUnit)
		if !ok1 || !ok2 {
			continue
		}
		if minWords > maxWords {
			minWords, maxWords = maxWords, minWords
		}
		if maxWords < minPromptTotalWords {
			continue
		}
		return NewWordBudgetFromRange(minWords, maxWords, source)
	}
	return nil, false
}

func parseWordBudgetSingle(text, source string) (*WordBudget, bool) {
	for _, re := range []*regexp.Regexp{budgetSingleRe, budgetUnitOnlyRe} {
		matches := re.FindAllStringSubmatchIndex(text, -1)
		for _, match := range matches {
			if len(match) < 6 || wordBudgetLooksPerChapter(text, match[0], match[1]) {
				continue
			}
			raw := text[match[2]:match[3]]
			unit := submatchText(text, match[4], match[5])
			total, ok := parseBudgetNumber(raw, unit)
			if !ok || total < minPromptTotalWords {
				continue
			}
			return NewWordBudgetFromTarget(total, source)
		}
	}
	return nil, false
}

func wordBudgetLooksPerChapter(text string, start, end int) bool {
	window := strings.ToLower(wordBudgetWindow(text, start, end))
	perChapterMarkers := []string{
		"每章", "单章", "單章", "每一章", "一章", "每章节", "每個章節", "每个章节",
		"章节字数", "章字数", "/章", "per chapter", "each chapter", "per-chapter", "chapter_words",
	}
	totalMarkers := []string{
		"全书", "全書", "总字数", "總字數", "总共", "總共", "合计", "合計", "整体", "整體",
		"全文", "篇幅", "一部", "长篇", "長篇", "中篇", "短篇", "total", "overall", "book", "novel",
	}
	hasPerChapter := containsAny(window, perChapterMarkers)
	if !hasPerChapter {
		return false
	}
	return !containsAny(window, totalMarkers)
}

func containsAny(text string, needles []string) bool {
	for _, needle := range needles {
		if strings.Contains(text, needle) {
			return true
		}
	}
	return false
}

func wordBudgetWindow(text string, start, end int) string {
	lo := start - 48
	if lo < 0 {
		lo = 0
	}
	hi := end + 48
	if hi > len(text) {
		hi = len(text)
	}
	for lo > 0 && !utf8.RuneStart(text[lo]) {
		lo--
	}
	for hi < len(text) && !utf8.RuneStart(text[hi]) {
		hi++
	}
	return text[lo:hi]
}

func submatchText(text string, start, end int) string {
	if start < 0 || end < 0 || start >= end {
		return ""
	}
	return text[start:end]
}

func normalizeWordBudgetText(text string) string {
	var b strings.Builder
	b.Grow(len(text))
	for _, r := range text {
		switch {
		case r >= '０' && r <= '９':
			b.WriteRune('0' + (r - '０'))
		case r == '，' || r == ',':
			continue
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func parseBudgetNumber(raw, unit string) (int, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, false
	}
	var base float64
	if n, err := strconv.ParseFloat(raw, 64); err == nil {
		base = n
	} else {
		n, ok := parseChineseNumber(raw)
		if !ok {
			return 0, false
		}
		base = float64(n)
	}
	switch strings.ToLower(strings.TrimSpace(unit)) {
	case "万", "萬", "w":
		base *= 10000
	case "千", "k":
		base *= 1000
	case "million", "m":
		base *= 1000000
	}
	total := int(math.Round(base))
	return total, total > 0
}

func parseChineseNumber(raw string) (int, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, false
	}
	digits := map[rune]int{
		'零': 0, '〇': 0, '一': 1, '二': 2, '两': 2, '兩': 2, '三': 3, '四': 4,
		'五': 5, '六': 6, '七': 7, '八': 8, '九': 9,
	}
	units := map[rune]int{'十': 10, '百': 100, '千': 1000}
	total := 0
	section := 0
	number := 0
	seen := false
	for _, r := range raw {
		if d, ok := digits[r]; ok {
			number = d
			seen = true
			continue
		}
		if u, ok := units[r]; ok {
			if number == 0 {
				number = 1
			}
			section += number * u
			number = 0
			seen = true
			continue
		}
		if r == '万' || r == '萬' {
			section += number
			if section == 0 {
				section = 1
			}
			total += section * 10000
			section = 0
			number = 0
			seen = true
			continue
		}
		return 0, false
	}
	if !seen {
		return 0, false
	}
	return total + section + number, true
}
