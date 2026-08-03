package domain

import (
	"fmt"
	"strings"
)

const (
	AdaptationSegmentIssueInvalidSourceChapter = "invalid_source_chapter"
	AdaptationSegmentIssueInvalidSourceRunes   = "invalid_source_runes"
	AdaptationSegmentIssueInsufficientSegments = "insufficient_segments"
	AdaptationSegmentIssueSourceMismatch       = "source_chapter_mismatch"
	AdaptationSegmentIssueSequence             = "segment_sequence"
	AdaptationSegmentIssueInvalidShare         = "invalid_source_share"
	AdaptationSegmentIssueShareGap             = "source_share_gap"
	AdaptationSegmentIssueShareOverlap         = "source_share_overlap"
	AdaptationSegmentIssueShareOverflow        = "source_share_overflow"
	AdaptationSegmentIssueShareIncomplete      = "source_share_incomplete"
	AdaptationSegmentIssueShareTotal           = "source_share_total"
	AdaptationSegmentIssueMissingEvent         = "missing_event"
	AdaptationSegmentIssueDuplicateEvent       = "duplicate_event"
	AdaptationSegmentIssueMissingEntryState    = "missing_entry_state"
	AdaptationSegmentIssueMissingExitState     = "missing_exit_state"
	AdaptationSegmentIssueInvalidState         = "invalid_state"
	AdaptationSegmentIssueStateDiscontinuity   = "state_discontinuity"
)

// AdaptationSourceRuneShare is a zero-based, half-open source-rune accounting
// span. Its boundaries describe scene ownership; they do not authorize cutting
// prose at an arbitrary character.
type AdaptationSourceRuneShare struct {
	Start int `json:"start"`
	End   int `json:"end"`
}

func (s AdaptationSourceRuneShare) Runes() int {
	if s.End <= s.Start {
		return 0
	}
	return s.End - s.Start
}

// AdaptationSegmentState is the boundary snapshot that adjacent target
// chapters must agree on. Keys should be stable domain paths such as
// "character.lin_yi_location" or "relationship.lin_yi_bai_libing".
type AdaptationSegmentState map[string]string

// AdaptationSourceSegment assigns one ordered portion of a source chapter to
// one target chapter in preserve-details mode. More segments than the minimum
// are allowed when complete scene boundaries require them.
type AdaptationSourceSegment struct {
	SourceChapter  int                       `json:"source_chapter"`
	Sequence       int                       `json:"sequence"`
	EventIDs       []string                  `json:"event_ids"`
	RuneShare      AdaptationSourceRuneShare `json:"source_rune_share"`
	EntryState     AdaptationSegmentState    `json:"entry_state"`
	ExitState      AdaptationSegmentState    `json:"exit_state"`
	BoundaryReason string                    `json:"boundary_reason,omitempty"`
}

// MinimumAdaptationSourceSegmentCount returns the minimum number of target
// chapter responsibilities needed for one source chapter. Scene-aware planning
// may increase this count, but must never reduce it.
func MinimumAdaptationSourceSegmentCount(sourceRunes int) int {
	if sourceRunes <= 0 {
		return 0
	}
	return (sourceRunes-1)/AdaptationModelChapterMaxRunes + 1
}

// AdaptationSourceSegmentIssue is a stable, machine-readable validation
// finding that can be reused by audit and repair workflows.
type AdaptationSourceSegmentIssue struct {
	Code          string `json:"code"`
	SourceChapter int    `json:"source_chapter,omitempty"`
	Sequence      int    `json:"sequence,omitempty"`
	Detail        string `json:"detail"`
}

func (i AdaptationSourceSegmentIssue) Error() string {
	location := ""
	if i.SourceChapter > 0 {
		location = fmt.Sprintf(" source chapter %d", i.SourceChapter)
	}
	if i.Sequence > 0 {
		location += fmt.Sprintf(" segment %d", i.Sequence)
	}
	return fmt.Sprintf("[%s]%s: %s", i.Code, location, i.Detail)
}

// AdaptationSourceSegmentValidationError preserves every deterministic issue
// instead of hiding all but the first failure.
type AdaptationSourceSegmentValidationError struct {
	Issues []AdaptationSourceSegmentIssue
}

func (e *AdaptationSourceSegmentValidationError) Error() string {
	if e == nil || len(e.Issues) == 0 {
		return "adaptation source segment validation failed"
	}
	parts := make([]string, 0, len(e.Issues))
	for _, issue := range e.Issues {
		parts = append(parts, issue.Error())
	}
	return "adaptation source segment validation failed: " + strings.Join(parts, "; ")
}

// ValidateAdaptationSourceSegments verifies one source chapter's complete,
// ordered segment allocation. Callers must pass segments in narrative order.
func ValidateAdaptationSourceSegments(
	sourceChapter int,
	sourceRunes int,
	segments []AdaptationSourceSegment,
) error {
	issues := CheckAdaptationSourceSegments(sourceChapter, sourceRunes, segments)
	if len(issues) == 0 {
		return nil
	}
	return &AdaptationSourceSegmentValidationError{Issues: issues}
}

// CheckAdaptationSourceSegments returns all deterministic contract violations
// for one source chapter without mutating or reordering the input.
func CheckAdaptationSourceSegments(
	sourceChapter int,
	sourceRunes int,
	segments []AdaptationSourceSegment,
) []AdaptationSourceSegmentIssue {
	issues := make([]AdaptationSourceSegmentIssue, 0)
	if sourceChapter <= 0 {
		issues = append(issues, newAdaptationSegmentIssue(
			AdaptationSegmentIssueInvalidSourceChapter,
			sourceChapter,
			0,
			"source chapter must be positive",
		))
	}
	if sourceRunes <= 0 {
		issues = append(issues, newAdaptationSegmentIssue(
			AdaptationSegmentIssueInvalidSourceRunes,
			sourceChapter,
			0,
			"source runes must be positive",
		))
	}

	minimum := MinimumAdaptationSourceSegmentCount(sourceRunes)
	if len(segments) < minimum {
		issues = append(issues, newAdaptationSegmentIssue(
			AdaptationSegmentIssueInsufficientSegments,
			sourceChapter,
			0,
			fmt.Sprintf("got %d segments, need at least %d for %d source runes", len(segments), minimum, sourceRunes),
		))
	}

	expectedStart := 0
	totalRunes := 0
	seenEvents := make(map[string]int)
	var previousExit AdaptationSegmentState
	for index, segment := range segments {
		expectedSequence := index + 1
		issues = append(issues, checkAdaptationSourceSegmentIdentity(
			sourceChapter,
			expectedSequence,
			segment,
		)...)
		issues = append(issues, checkAdaptationSourceSegmentEvents(segment, seenEvents)...)
		issues = append(issues, checkAdaptationSourceSegmentStates(segment, previousExit)...)
		issues = append(issues, checkAdaptationSourceRuneShare(
			sourceChapter,
			segment.Sequence,
			sourceRunes,
			expectedStart,
			segment.RuneShare,
		)...)

		totalRunes += segment.RuneShare.Runes()
		if segment.RuneShare.End > expectedStart {
			expectedStart = segment.RuneShare.End
		}
		previousExit = segment.ExitState
	}

	if sourceRunes > 0 && expectedStart < sourceRunes {
		issues = append(issues, newAdaptationSegmentIssue(
			AdaptationSegmentIssueShareIncomplete,
			sourceChapter,
			0,
			fmt.Sprintf("source coverage ends at rune %d, want %d", expectedStart, sourceRunes),
		))
	}
	if sourceRunes > 0 && totalRunes != sourceRunes {
		issues = append(issues, newAdaptationSegmentIssue(
			AdaptationSegmentIssueShareTotal,
			sourceChapter,
			0,
			fmt.Sprintf("source rune shares total %d, want %d", totalRunes, sourceRunes),
		))
	}
	return issues
}

func checkAdaptationSourceSegmentIdentity(
	sourceChapter int,
	expectedSequence int,
	segment AdaptationSourceSegment,
) []AdaptationSourceSegmentIssue {
	issues := make([]AdaptationSourceSegmentIssue, 0, 2)
	if segment.SourceChapter != sourceChapter {
		issues = append(issues, newAdaptationSegmentIssue(
			AdaptationSegmentIssueSourceMismatch,
			sourceChapter,
			segment.Sequence,
			fmt.Sprintf("segment points to source chapter %d", segment.SourceChapter),
		))
	}
	if segment.Sequence != expectedSequence {
		issues = append(issues, newAdaptationSegmentIssue(
			AdaptationSegmentIssueSequence,
			sourceChapter,
			segment.Sequence,
			fmt.Sprintf("segment at position %d must have sequence %d", expectedSequence, expectedSequence),
		))
	}
	return issues
}

func checkAdaptationSourceSegmentEvents(
	segment AdaptationSourceSegment,
	seenEvents map[string]int,
) []AdaptationSourceSegmentIssue {
	issues := make([]AdaptationSourceSegmentIssue, 0)
	if len(segment.EventIDs) == 0 {
		return append(issues, newAdaptationSegmentIssue(
			AdaptationSegmentIssueMissingEvent,
			segment.SourceChapter,
			segment.Sequence,
			"segment must own at least one source event",
		))
	}
	for _, rawID := range segment.EventIDs {
		eventID := strings.TrimSpace(rawID)
		if eventID == "" {
			issues = append(issues, newAdaptationSegmentIssue(
				AdaptationSegmentIssueMissingEvent,
				segment.SourceChapter,
				segment.Sequence,
				"event id must not be blank",
			))
			continue
		}
		if owner, exists := seenEvents[eventID]; exists {
			issues = append(issues, newAdaptationSegmentIssue(
				AdaptationSegmentIssueDuplicateEvent,
				segment.SourceChapter,
				segment.Sequence,
				fmt.Sprintf("event %q is already owned by segment %d", eventID, owner),
			))
			continue
		}
		seenEvents[eventID] = segment.Sequence
	}
	return issues
}

func checkAdaptationSourceSegmentStates(
	segment AdaptationSourceSegment,
	previousExit AdaptationSegmentState,
) []AdaptationSourceSegmentIssue {
	issues := make([]AdaptationSourceSegmentIssue, 0)
	if segment.EntryState == nil {
		issues = append(issues, newAdaptationSegmentIssue(
			AdaptationSegmentIssueMissingEntryState,
			segment.SourceChapter,
			segment.Sequence,
			"entry state must be present (an empty state is allowed)",
		))
	}
	if segment.ExitState == nil {
		issues = append(issues, newAdaptationSegmentIssue(
			AdaptationSegmentIssueMissingExitState,
			segment.SourceChapter,
			segment.Sequence,
			"exit state must be present (an empty state is allowed)",
		))
	}
	issues = append(issues, checkAdaptationSegmentState(
		segment.SourceChapter,
		segment.Sequence,
		"entry",
		segment.EntryState,
	)...)
	issues = append(issues, checkAdaptationSegmentState(
		segment.SourceChapter,
		segment.Sequence,
		"exit",
		segment.ExitState,
	)...)
	if previousExit != nil && segment.EntryState != nil && !adaptationSegmentStatesEqual(previousExit, segment.EntryState) {
		issues = append(issues, newAdaptationSegmentIssue(
			AdaptationSegmentIssueStateDiscontinuity,
			segment.SourceChapter,
			segment.Sequence,
			"entry state does not match the previous segment exit state",
		))
	}
	return issues
}

func checkAdaptationSegmentState(
	sourceChapter int,
	sequence int,
	label string,
	state AdaptationSegmentState,
) []AdaptationSourceSegmentIssue {
	issues := make([]AdaptationSourceSegmentIssue, 0)
	seenKeys := make(map[string]struct{}, len(state))
	for rawKey := range state {
		key := strings.TrimSpace(rawKey)
		if key == "" {
			issues = append(issues, newAdaptationSegmentIssue(
				AdaptationSegmentIssueInvalidState,
				sourceChapter,
				sequence,
				label+" state contains a blank key",
			))
			continue
		}
		if _, exists := seenKeys[key]; exists {
			issues = append(issues, newAdaptationSegmentIssue(
				AdaptationSegmentIssueInvalidState,
				sourceChapter,
				sequence,
				fmt.Sprintf("%s state contains duplicate normalized key %q", label, key),
			))
		}
		seenKeys[key] = struct{}{}
	}
	return issues
}

func checkAdaptationSourceRuneShare(
	sourceChapter int,
	sequence int,
	sourceRunes int,
	expectedStart int,
	share AdaptationSourceRuneShare,
) []AdaptationSourceSegmentIssue {
	issues := make([]AdaptationSourceSegmentIssue, 0, 2)
	if share.Start < 0 || share.End <= share.Start {
		issues = append(issues, newAdaptationSegmentIssue(
			AdaptationSegmentIssueInvalidShare,
			sourceChapter,
			sequence,
			fmt.Sprintf("source rune share [%d,%d) must be non-negative and non-empty", share.Start, share.End),
		))
		return issues
	}
	if share.Start > expectedStart {
		issues = append(issues, newAdaptationSegmentIssue(
			AdaptationSegmentIssueShareGap,
			sourceChapter,
			sequence,
			fmt.Sprintf("source rune gap [%d,%d)", expectedStart, share.Start),
		))
	} else if share.Start < expectedStart {
		issues = append(issues, newAdaptationSegmentIssue(
			AdaptationSegmentIssueShareOverlap,
			sourceChapter,
			sequence,
			fmt.Sprintf("source rune share starts at %d before expected %d", share.Start, expectedStart),
		))
	}
	if sourceRunes > 0 && share.End > sourceRunes {
		issues = append(issues, newAdaptationSegmentIssue(
			AdaptationSegmentIssueShareOverflow,
			sourceChapter,
			sequence,
			fmt.Sprintf("source rune share ends at %d beyond source length %d", share.End, sourceRunes),
		))
	}
	return issues
}

func adaptationSegmentStatesEqual(left, right AdaptationSegmentState) bool {
	if len(left) != len(right) {
		return false
	}
	normalizedRight := make(map[string]string, len(right))
	for key, value := range right {
		normalizedRight[strings.TrimSpace(key)] = strings.TrimSpace(value)
	}
	for key, value := range left {
		rightValue, exists := normalizedRight[strings.TrimSpace(key)]
		if !exists || rightValue != strings.TrimSpace(value) {
			return false
		}
	}
	return true
}

func newAdaptationSegmentIssue(
	code string,
	sourceChapter int,
	sequence int,
	detail string,
) AdaptationSourceSegmentIssue {
	return AdaptationSourceSegmentIssue{
		Code:          code,
		SourceChapter: sourceChapter,
		Sequence:      sequence,
		Detail:        detail,
	}
}
