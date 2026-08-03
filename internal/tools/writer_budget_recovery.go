package tools

import (
	"strconv"
	"strings"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/store"
)

const (
	writerBudgetSegmentLines     = 48
	writerBudgetCheckpointPrefix = "word_budget_edit_segment_"
)

type writerBudgetWindow struct {
	Segment   int
	FromLine  int
	ToLine    int
	LineCount int
	MinWords  int
	MaxWords  int
	WordCount int
}

func currentWriterBudgetWindow(st *store.Store, chapter int, content string) (writerBudgetWindow, bool) {
	if st == nil {
		return writerBudgetWindow{}, false
	}
	progress, err := st.Progress.Load()
	if err != nil || progress == nil || progress.InProgressChapter != chapter {
		return writerBudgetWindow{}, false
	}
	if progress.Flow == domain.FlowPolishing || len(progress.PendingRewrites) > 0 {
		return writerBudgetWindow{}, false
	}
	_, policy, ok, err := st.ChapterWordBudgetPolicy(progress, chapter)
	if err != nil || !ok {
		return writerBudgetWindow{}, false
	}
	wordCount := len([]rune(content))
	minWords := policy.HardMinWords
	maxWords := policy.HardMaxWords
	if wordCount >= minWords && wordCount <= maxWords {
		return writerBudgetWindow{}, false
	}
	lineCount := max(len(strings.Split(content, "\n")), 1)
	segmentCount := (lineCount + writerBudgetSegmentLines - 1) / writerBudgetSegmentLines
	segment := segmentCount - 1
	if checkpoint := st.Checkpoints.LatestByStepPrefix(domain.ChapterScope(chapter), writerBudgetCheckpointPrefix); checkpoint != nil {
		if completed, valid := parseWriterBudgetSegment(checkpoint.Step); valid && completed > 0 {
			segment = min(completed-1, segmentCount-1)
		}
	}
	fromLine := segment*writerBudgetSegmentLines + 1
	return writerBudgetWindow{
		Segment:   segment,
		FromLine:  fromLine,
		ToLine:    min(fromLine+writerBudgetSegmentLines-1, lineCount),
		LineCount: lineCount,
		MinWords:  minWords,
		MaxWords:  maxWords,
		WordCount: wordCount,
	}, true
}

func parseWriterBudgetSegment(step string) (int, bool) {
	if !strings.HasPrefix(strings.TrimSpace(step), writerBudgetCheckpointPrefix) {
		return 0, false
	}
	value, err := strconv.Atoi(strings.TrimPrefix(strings.TrimSpace(step), writerBudgetCheckpointPrefix))
	return value, err == nil && value >= 0
}

func wordBudgetDistance(wordCount, minWords, maxWords int) int {
	switch {
	case wordCount < minWords:
		return minWords - wordCount
	case wordCount > maxWords:
		return wordCount - maxWords
	default:
		return 0
	}
}
