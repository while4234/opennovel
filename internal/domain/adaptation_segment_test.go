package domain

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestMinimumAdaptationSourceSegmentCountUsesChapterMaximum(t *testing.T) {
	tests := []struct {
		name        string
		sourceRunes int
		want        int
	}{
		{name: "empty", sourceRunes: 0, want: 0},
		{name: "short chapter", sourceRunes: AdaptationModelChapterMaxRunes - 1, want: 1},
		{name: "exact maximum", sourceRunes: AdaptationModelChapterMaxRunes, want: 1},
		{name: "ten thousand runes", sourceRunes: 10000, want: 2},
		{name: "one rune over two chapters", sourceRunes: 10001, want: 3},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := MinimumAdaptationSourceSegmentCount(test.sourceRunes); got != test.want {
				t.Fatalf("MinimumAdaptationSourceSegmentCount(%d) = %d, want %d", test.sourceRunes, got, test.want)
			}
		})
	}
}

func TestValidateAdaptationSourceSegmentsAcceptsLongChapterSplit(t *testing.T) {
	segments := []AdaptationSourceSegment{
		{
			SourceChapter: 7,
			Sequence:      1,
			EventIDs:      []string{"source-007-event-001"},
			RuneShare:     AdaptationSourceRuneShare{Start: 0, End: 4800},
			EntryState:    AdaptationSegmentState{},
			ExitState: AdaptationSegmentState{
				"relationship.leads": "met",
			},
			BoundaryReason: "first encounter reaches a complete stage result",
		},
		{
			SourceChapter: 7,
			Sequence:      2,
			EventIDs:      []string{"source-007-event-002"},
			RuneShare:     AdaptationSourceRuneShare{Start: 4800, End: 10000},
			EntryState: AdaptationSegmentState{
				"relationship.leads": "met",
			},
			ExitState: AdaptationSegmentState{
				"relationship.leads": "mutual_debt",
			},
		},
	}

	if err := ValidateAdaptationSourceSegments(7, 10000, segments); err != nil {
		t.Fatalf("ValidateAdaptationSourceSegments: %v", err)
	}
}

func TestValidateAdaptationSourceSegmentsAllowsExtraSceneBoundaries(t *testing.T) {
	segments := []AdaptationSourceSegment{
		newTestAdaptationSourceSegment(9, 1, 0, 2500, "event-1", "start", "scene-1"),
		newTestAdaptationSourceSegment(9, 2, 2500, 5100, "event-2", "scene-1", "scene-2"),
		newTestAdaptationSourceSegment(9, 3, 5100, 7600, "event-3", "scene-2", "scene-3"),
		newTestAdaptationSourceSegment(9, 4, 7600, 10001, "event-4", "scene-3", "done"),
	}

	if err := ValidateAdaptationSourceSegments(9, 10001, segments); err != nil {
		t.Fatalf("scene-aware split should be valid: %v", err)
	}
}

func TestValidateAdaptationSourceSegmentsRejectsSingleTargetForLongChapter(t *testing.T) {
	segments := []AdaptationSourceSegment{
		newTestAdaptationSourceSegment(3, 1, 0, 10000, "event-1", "start", "done"),
	}

	err := ValidateAdaptationSourceSegments(3, 10000, segments)
	if err == nil {
		t.Fatal("expected long chapter split validation to fail")
	}
	var validationErr *AdaptationSourceSegmentValidationError
	if !errors.As(err, &validationErr) {
		t.Fatalf("error type = %T, want *AdaptationSourceSegmentValidationError", err)
	}
	if !adaptationSegmentIssuesContain(validationErr.Issues, AdaptationSegmentIssueInsufficientSegments) {
		t.Fatalf("issues = %+v, want %q", validationErr.Issues, AdaptationSegmentIssueInsufficientSegments)
	}
}

func TestCheckAdaptationSourceSegmentsReportsCoverageOrderAndStateFailures(t *testing.T) {
	segments := []AdaptationSourceSegment{
		newTestAdaptationSourceSegment(4, 1, 0, 4000, "event-1", "start", "scene-1"),
		newTestAdaptationSourceSegment(4, 3, 5000, 8000, "event-1", "wrong-state", "scene-2"),
		newTestAdaptationSourceSegment(4, 2, 7000, 10000, "event-3", "scene-2", "done"),
	}

	issues := CheckAdaptationSourceSegments(4, 10000, segments)
	for _, want := range []string{
		AdaptationSegmentIssueSequence,
		AdaptationSegmentIssueShareGap,
		AdaptationSegmentIssueShareOverlap,
		AdaptationSegmentIssueDuplicateEvent,
		AdaptationSegmentIssueStateDiscontinuity,
	} {
		if !adaptationSegmentIssuesContain(issues, want) {
			t.Errorf("issues = %+v, want code %q", issues, want)
		}
	}
}

func TestValidateAdaptationSourceSegmentsTreatsDifferentStateKeysAsDiscontinuity(t *testing.T) {
	segments := []AdaptationSourceSegment{
		newTestAdaptationSourceSegment(5, 1, 0, 2000, "event-1", "start", ""),
		newTestAdaptationSourceSegment(5, 2, 2000, 4000, "event-2", "", "done"),
	}
	segments[0].ExitState = AdaptationSegmentState{"character.a.status": ""}
	segments[1].EntryState = AdaptationSegmentState{"character.b.status": ""}

	issues := CheckAdaptationSourceSegments(5, 4000, segments)
	if !adaptationSegmentIssuesContain(issues, AdaptationSegmentIssueStateDiscontinuity) {
		t.Fatalf("issues = %+v, want %q", issues, AdaptationSegmentIssueStateDiscontinuity)
	}
}

func TestAdaptationChapterPlanJSONPersistsSourceSegments(t *testing.T) {
	plan := AdaptationPlan{
		Granularity:   AdaptationGranularityChapter,
		RewritePolicy: AdaptationRewritePreserveDetails,
		Chapters: []AdaptationChapterPlan{
			{
				Chapter:        1,
				SourceChapters: []int{1},
				SourceSegments: []AdaptationSourceSegment{
					newTestAdaptationSourceSegment(1, 1, 0, 2000, "source-001-event-001", "start", "done"),
				},
			},
		},
	}

	raw, err := json.Marshal(plan)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var decoded AdaptationPlan
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	segment := decoded.Chapters[0].SourceSegments[0]
	if segment.SourceChapter != 1 || segment.Sequence != 1 || segment.RuneShare.End != 2000 {
		t.Fatalf("source segment mismatch after round trip: %+v", segment)
	}
}

func newTestAdaptationSourceSegment(
	sourceChapter int,
	sequence int,
	start int,
	end int,
	eventID string,
	entry string,
	exit string,
) AdaptationSourceSegment {
	return AdaptationSourceSegment{
		SourceChapter: sourceChapter,
		Sequence:      sequence,
		EventIDs:      []string{eventID},
		RuneShare:     AdaptationSourceRuneShare{Start: start, End: end},
		EntryState:    AdaptationSegmentState{"story.phase": entry},
		ExitState:     AdaptationSegmentState{"story.phase": exit},
	}
}

func adaptationSegmentIssuesContain(issues []AdaptationSourceSegmentIssue, code string) bool {
	for _, issue := range issues {
		if issue.Code == code {
			return true
		}
	}
	return false
}
