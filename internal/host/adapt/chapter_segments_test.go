package adapt

import (
	"slices"
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
)

func TestBuildPlanFromInputsSplitsLongChapterWithoutChangingSourceOrder(t *testing.T) {
	reports := []domain.AdaptationSourceReport{{
		Chapter: 1,
		Title:   "长章",
		KeyEvents: []string{
			"主角进入案发现场",
			"主角发现身份线索",
		},
		StateChanges: []domain.StateChange{{Entity: "主角", Field: "location", NewValue: "医院"}},
	}}
	manifest := &domain.AdaptationSourceManifest{
		ChapterCount: 1,
		Chapters:     []domain.AdaptationSource{{Chapter: 1, Runes: 10_000}},
	}
	plan := buildPlanFromInputs(ProposalOptions{
		Brief:         "尽可能保留细节",
		Granularity:   domain.AdaptationGranularityChapter,
		RewritePolicy: domain.AdaptationRewritePreserveDetails,
		WordTolerance: DefaultWordTolerance,
	}, reports, manifest, domain.AdaptationPlanStatusProposal)
	if len(plan.Chapters) != 2 {
		t.Fatalf("target chapters=%d want=2", len(plan.Chapters))
	}
	var segments []domain.AdaptationSourceSegment
	for index, chapter := range plan.Chapters {
		if chapter.Chapter != index+1 || chapter.SourceChapters[0] != 1 {
			t.Fatalf("chapter mapping[%d]=%+v", index, chapter)
		}
		if chapter.TargetMaxRunes > domain.AdaptationModelChapterMaxRunes {
			t.Fatalf("chapter %d max runes=%d", chapter.Chapter, chapter.TargetMaxRunes)
		}
		segments = append(segments, chapter.SourceSegments...)
	}
	if err := domain.ValidateAdaptationSourceSegments(1, 10_000, segments); err != nil {
		t.Fatalf("segments invalid: %v", err)
	}
	if segments[0].ExitState["state:主角:location"] != "医院" || segments[1].EntryState["state:主角:location"] != "医院" {
		t.Fatalf("state boundary was not carried across segments: %+v", segments)
	}
}

func TestBuildPlanFromInputsUsesThreeTargetsFor10001Runes(t *testing.T) {
	manifest := &domain.AdaptationSourceManifest{ChapterCount: 1, Chapters: []domain.AdaptationSource{{Chapter: 1, Runes: 10_001}}}
	reports := []domain.AdaptationSourceReport{{Chapter: 1, Title: "长章", KeyEvents: []string{"案件发生"}}}
	plan := buildPlanFromInputs(ProposalOptions{Brief: "保留细节", Granularity: domain.AdaptationGranularityChapter, WordTolerance: DefaultWordTolerance}, reports, manifest, domain.AdaptationPlanStatusProposal)
	if len(plan.Chapters) != 3 {
		t.Fatalf("target chapters=%d want=3", len(plan.Chapters))
	}
}

func TestBuildPlanStoresBriefOnceAndUsesRuleIDs(t *testing.T) {
	manifest := &domain.AdaptationSourceManifest{ChapterCount: 1, Chapters: []domain.AdaptationSource{{Chapter: 1, Runes: 1000}}}
	reports := []domain.AdaptationSourceReport{{Chapter: 1, KeyEvents: []string{"初遇"}}}
	plan := buildPlanFromInputs(ProposalOptions{Brief: "必须保留初遇", Granularity: domain.AdaptationGranularityChapter}, reports, manifest, domain.AdaptationPlanStatusProposal)
	if len(plan.Rules) != 1 || len(plan.Chapters[0].RuleIDs) != 1 {
		t.Fatalf("rules=%+v chapter=%+v", plan.Rules, plan.Chapters[0])
	}
	if len(plan.Chapters[0].RequiredChanges) != 0 {
		t.Fatalf("raw brief should not be copied into required_changes: %+v", plan.Chapters[0].RequiredChanges)
	}
}

func TestSourceEventsForSegmentsStayInNarrativeOrder(t *testing.T) {
	events := []domain.AdaptationEvent{
		{ID: "event-1", Description: "first"},
		{ID: "event-2", Description: "second"},
		{ID: "event-3", Description: "third"},
	}
	first, _ := sourceEventsForSegment(domain.AdaptationSourceReport{Chapter: 1}, events, 2, 0)
	second, _ := sourceEventsForSegment(domain.AdaptationSourceReport{Chapter: 1}, events, 2, 1)
	if got := append(first, second...); !slices.Equal(got, []string{"event-1", "event-2", "event-3"}) {
		t.Fatalf("segment events are out of order: %v", got)
	}
}
