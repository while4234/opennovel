package adapt

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/store"
)

func TestValidateAdaptationOutlineQualityChapterRequiresCompleteLongSourceSegments(t *testing.T) {
	manifest := &domain.AdaptationSourceManifest{
		ChapterCount: 1,
		Chapters:     []domain.AdaptationSource{{Chapter: 1, Runes: 10_000}},
	}
	valid := domain.AdaptationPlan{
		Granularity: domain.AdaptationGranularityChapter,
		Chapters: []domain.AdaptationChapterPlan{
			{Chapter: 1, SourceSegments: []domain.AdaptationSourceSegment{{
				SourceChapter: 1, Sequence: 1, EventIDs: []string{"src-1-a"},
				RuneShare:  domain.AdaptationSourceRuneShare{Start: 0, End: 5_000},
				EntryState: domain.AdaptationSegmentState{}, ExitState: domain.AdaptationSegmentState{"place": "inn"},
			}}},
			{Chapter: 2, SourceSegments: []domain.AdaptationSourceSegment{{
				SourceChapter: 1, Sequence: 2, EventIDs: []string{"src-1-b"},
				RuneShare:  domain.AdaptationSourceRuneShare{Start: 5_000, End: 10_000},
				EntryState: domain.AdaptationSegmentState{"place": "inn"}, ExitState: domain.AdaptationSegmentState{},
			}}},
		},
	}
	if err := ValidateAdaptationOutlineQuality(&valid, manifest); err != nil {
		t.Fatalf("valid Chapter source segments: %v", err)
	}

	invalid := valid
	invalid.Chapters = invalid.Chapters[:1]
	if err := ValidateAdaptationOutlineQuality(&invalid, manifest); !outlineQualityHasCode(err, outlineQualityIssueChapterInvalidSegment) {
		t.Fatalf("long source chapter without all segments error=%v, want %s", err, outlineQualityIssueChapterInvalidSegment)
	}
}

func TestValidateAdaptationOutlineQualityArcBindsVolumeMainlineExactlyOnce(t *testing.T) {
	plan := domain.AdaptationPlan{
		Granularity: domain.AdaptationGranularityArc,
		SourceEvents: []domain.AdaptationEvent{{
			ID: "src-0001-e01-introduction", SourceChapter: 1, Importance: domain.AdaptationEventMainline, Required: true,
		}},
		Volumes: []domain.AdaptationVolumePlan{{
			Index: 1, SourceFrom: 1, SourceTo: 1, TargetFrom: 1, TargetTo: 1,
			MainlineEventIDs: []string{"src-0001-e01-introduction"},
		}},
		Chapters: []domain.AdaptationChapterPlan{{Chapter: 1}},
	}
	if err := ValidateAdaptationOutlineQuality(&plan, nil); !outlineQualityHasCode(err, outlineQualityIssueArcMissingMainline) {
		t.Fatalf("unmapped volume mainline error=%v, want %s", err, outlineQualityIssueArcMissingMainline)
	}

	plan.Chapters = []domain.AdaptationChapterPlan{{Chapter: 1, EventIDs: []string{"src-0001-e01-introduction"}}}
	if err := ValidateAdaptationOutlineQuality(&plan, nil); err != nil {
		t.Fatalf("mainline bound once in its volume: %v", err)
	}

	plan.Chapters = append(plan.Chapters, domain.AdaptationChapterPlan{Chapter: 2, EventIDs: []string{"src-0001-e01-introduction"}})
	if err := ValidateAdaptationOutlineQuality(&plan, nil); !outlineQualityHasCode(err, outlineQualityIssueArcDuplicateMainline) {
		t.Fatalf("duplicated mainline error=%v, want %s", err, outlineQualityIssueArcDuplicateMainline)
	}
}

func TestValidateAdaptationOutlineQualityArcChecksSupportingEventOwnershipAndTheme(t *testing.T) {
	plan := domain.AdaptationPlan{
		Granularity: domain.AdaptationGranularityArc,
		SourceEvents: []domain.AdaptationEvent{
			{ID: "support-conflict", SourceChapter: 13, Description: "百里冰被迫交出手机", Importance: domain.AdaptationEventSupporting},
			{ID: "support-rescue", SourceChapter: 13, Description: "林逸飞空手夺刀并放走黑衣人", Importance: domain.AdaptationEventSupporting},
			{ID: "texture-meeting", SourceChapter: 13, Description: "百里冰与林逸飞结识，以请客吃饭为由带其进入大学", Importance: domain.AdaptationEventTexture},
		},
		Chapters: []domain.AdaptationChapterPlan{
			{
				Chapter: 13,
				Title:   "重返人间",
				OutlineEntry: domain.OutlineEntry{
					CoreEvent: "主角出院返校，与室友重逢。",
					Hook:      "明天将在校园遇见百里冰。",
				},
				EventIDs: []string{"support-conflict", "support-rescue", "texture-meeting"},
			},
			{
				Chapter: 14,
				Title:   "冰临",
				OutlineEntry: domain.OutlineEntry{
					CoreEvent: "百里冰首次登场，篮球场围堵中主角出手救下同学。",
					Hook:      "她走上前直视主角，询问他的姓名并邀请吃饭。",
				},
				EventIDs: []string{"texture-meeting"},
			},
		},
	}

	err := ValidateAdaptationOutlineQuality(&plan, nil)
	if !outlineQualityHasCode(err, outlineQualityIssueArcEventMismatch) {
		t.Fatalf("supporting/texture event moved to later plot was not rejected: %v", err)
	}
	if !outlineQualityHasCode(err, outlineQualityIssueArcDuplicateEvent) {
		t.Fatalf("supporting event duplicate binding was not rejected: %v", err)
	}
}

func TestValidateAdaptationOutlineQualityArcAcceptsSupportingEventInMatchingChapter(t *testing.T) {
	plan := domain.AdaptationPlan{
		Granularity: domain.AdaptationGranularityArc,
		SourceEvents: []domain.AdaptationEvent{{
			ID: "support-conflict", SourceChapter: 1, Description: "黑衣人拦路抢劫", Importance: domain.AdaptationEventSupporting,
		}},
		Chapters: []domain.AdaptationChapterPlan{{
			Chapter:      1,
			OutlineEntry: domain.OutlineEntry{CoreEvent: "黑衣人拦路抢劫，主角出手制止。"},
			EventIDs:     []string{"support-conflict"},
		}},
	}
	if err := ValidateAdaptationOutlineQuality(&plan, nil); err != nil {
		t.Fatalf("matching supporting event should pass: %v", err)
	}
}

func TestValidateAdaptationOutlineQualityArcRejectsDenseChapterBudget(t *testing.T) {
	plan := domain.AdaptationPlan{
		Granularity: domain.AdaptationGranularityArc,
		Chapters: []domain.AdaptationChapterPlan{{
			Chapter: 39,
			OutlineEntry: domain.OutlineEntry{
				CoreEvent: "九个连续场景组成一章。",
				Scenes:    []string{"1", "2", "3", "4", "5", "6", "7", "8", "9"},
			},
			TargetRunes: 1400, TargetMinRunes: 1200, TargetMaxRunes: 1600,
		}},
	}
	if err := ValidateAdaptationOutlineQuality(&plan, nil); !outlineQualityHasCode(err, outlineQualityIssueArcBudgetDensity) {
		t.Fatalf("dense chapter budget should be rejected: %v", err)
	}
	plan.Chapters[0].TargetRunes = 4000
	plan.Chapters[0].TargetMinRunes = 3400
	plan.Chapters[0].TargetMaxRunes = 4600
	if err := ValidateAdaptationOutlineQuality(&plan, nil); err != nil {
		t.Fatalf("scene-capable chapter budget should pass: %v", err)
	}
}

func TestValidateAdaptationChapterOutlineQualityScopesLegacyPlanToNextChapter(t *testing.T) {
	plan := domain.AdaptationPlan{
		Granularity: domain.AdaptationGranularityArc,
		SourceEvents: []domain.AdaptationEvent{
			{ID: "src-1", SourceChapter: 1, Description: "黑衣人拦路抢劫", Importance: domain.AdaptationEventSupporting},
			{ID: "src-2", SourceChapter: 2, Description: "黑衣人再次拦路抢劫", Importance: domain.AdaptationEventSupporting},
		},
		Chapters: []domain.AdaptationChapterPlan{
			{Chapter: 1, OutlineEntry: domain.OutlineEntry{CoreEvent: "黑衣人拦路抢劫。"}, EventIDs: []string{"src-1"}},
			{Chapter: 2, OutlineEntry: domain.OutlineEntry{CoreEvent: "返校与室友重逢。"}, EventIDs: []string{"src-2"}},
		},
	}
	if err := ValidateAdaptationChapterOutlineQuality(&plan, 1); err != nil {
		t.Fatalf("valid current chapter should not inherit future legacy error: %v", err)
	}
	if err := ValidateAdaptationChapterOutlineQuality(&plan, 2); err == nil {
		t.Fatal("current chapter with a mismatched source event should be blocked")
	}
}

func TestFormatAdaptationOutlineQualityFeedbackKeepsRepairAtPlanningLayer(t *testing.T) {
	feedback := formatAdaptationOutlineQualityFeedback(&AdaptationOutlineQualityError{Issues: []AdaptationOutlineQualityIssue{{
		Code: outlineQualityIssueArcEventMismatch, Detail: "event belongs to chapter 14, not chapter 13",
	}}})
	if !strings.Contains(feedback, "plan-only adaptation quality gate") ||
		!strings.Contains(feedback, "Do not silence the error") ||
		!strings.Contains(feedback, "chapter 14") {
		t.Fatalf("feedback does not direct planner-level repair: %q", feedback)
	}
}

func TestValidateAdaptationOutlineQualityFreeChecksTargetCausalityRelationshipAndSettings(t *testing.T) {
	plan := domain.AdaptationPlan{
		Granularity:              domain.AdaptationGranularityFree,
		TargetSettingLocks:       []domain.AdaptationSettingLock{{Key: "city", Value: "洛阳"}},
		TargetRelationshipStates: map[string]string{"林逸飞|百里冰": "信任"},
		TargetEventLedger: []domain.AdaptationEvent{
			{
				ID: "trust", DependsOn: []string{"rescue"},
				Relationship:  &domain.AdaptationRelationshipTransition{Pair: "林逸飞|百里冰", From: "陌生", To: "信任", RequiresEventIDs: []string{"rescue"}},
				SettingClaims: []domain.AdaptationSettingClaim{{Key: "city", Value: "长安"}},
			},
			{ID: "rescue"},
		},
		Chapters: []domain.AdaptationChapterPlan{
			{Chapter: 1, EventIDs: []string{"trust"}},
			{Chapter: 2, EventIDs: []string{"rescue"}},
		},
	}
	err := ValidateAdaptationOutlineQuality(&plan, nil)
	if !outlineQualityHasCode(err, outlineQualityIssueFreeDependency) {
		t.Fatalf("future dependency error=%v, want %s", err, outlineQualityIssueFreeDependency)
	}
	if !outlineQualityHasCode(err, outlineQualityIssueFreeSetting) {
		t.Fatalf("setting lock conflict error=%v, want %s", err, outlineQualityIssueFreeSetting)
	}

	plan.TargetEventLedger[0].DependsOn = nil
	plan.TargetEventLedger[0].Relationship.RequiresEventIDs = nil
	plan.TargetEventLedger[0].SettingClaims[0].Value = "洛阳"
	plan.Chapters = []domain.AdaptationChapterPlan{
		{Chapter: 1, EventIDs: []string{"rescue"}},
		{Chapter: 2, EventIDs: []string{"trust"}},
	}
	if err := ValidateAdaptationOutlineQuality(&plan, nil); err != nil {
		t.Fatalf("coherent target ledger: %v", err)
	}
}

func TestConfirmAdaptationProposalRunsPlanOnlyChapterQualityGate(t *testing.T) {
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := st.Adaptation.SaveSourceFoundation(testSourceFoundation()); err != nil {
		t.Fatalf("SaveSourceFoundation: %v", err)
	}
	if err := st.Adaptation.SaveSourceManifest(domain.AdaptationSourceManifest{
		ChapterCount: 1,
		Chapters:     []domain.AdaptationSource{{Chapter: 1, Runes: 10_000}},
	}); err != nil {
		t.Fatalf("SaveSourceManifest: %v", err)
	}
	proposal := domain.AdaptationPlan{
		Granularity: domain.AdaptationGranularityChapter,
		Status:      domain.AdaptationPlanStatusProposal,
		Brief:       "preserve source scenes",
		Chapters: []domain.AdaptationChapterPlan{{
			Chapter: 1, Title: "Opening", SourceChapters: []int{1},
			OutlineEntry: domain.OutlineEntry{CoreEvent: "The first conflict starts.", Hook: "A witness arrives.", Scenes: []string{"scene"}},
		}},
	}
	_, err := ConfirmAdaptationProposal(context.Background(), Deps{Store: st}, proposal)
	if !outlineQualityHasCode(err, outlineQualityIssueChapterMissingSegment) {
		t.Fatalf("ConfirmAdaptationProposal error=%v, want plan-only %s", err, outlineQualityIssueChapterMissingSegment)
	}
}

func TestRetryAdaptationOutlineQualityUsesIndependentAttemptBudget(t *testing.T) {
	qualityErr := &AdaptationOutlineQualityError{Issues: []AdaptationOutlineQualityIssue{{Code: outlineQualityIssueArcMissingMainline}}}
	var calls, preparations int
	proposal, err := retryAdaptationOutlineQuality(
		2,
		func() (domain.AdaptationPlan, error) {
			calls++
			if calls <= 2 {
				return domain.AdaptationPlan{}, qualityErr
			}
			return domain.AdaptationPlan{Chapters: []domain.AdaptationChapterPlan{{Chapter: 1}}}, nil
		},
		func(attempt int, got *AdaptationOutlineQualityError) error {
			preparations++
			if attempt != preparations || got != qualityErr {
				t.Fatalf("retry preparation attempt=%d error=%#v", attempt, got)
			}
			return nil
		},
	)
	if err != nil {
		t.Fatalf("retryAdaptationOutlineQuality: %v", err)
	}
	if len(proposal.Chapters) != 1 || calls != 3 || preparations != 2 {
		t.Fatalf("proposal=%+v calls=%d preparations=%d, want two quality retries after the initial generation", proposal, calls, preparations)
	}
}

func TestRetryAdaptationOutlineQualityDoesNotRetryOtherErrors(t *testing.T) {
	var calls, preparations int
	_, err := retryAdaptationOutlineQuality(
		5,
		func() (domain.AdaptationPlan, error) {
			calls++
			return domain.AdaptationPlan{}, fmt.Errorf("ordinary planner error")
		},
		func(int, *AdaptationOutlineQualityError) error {
			preparations++
			return nil
		},
	)
	if err == nil {
		t.Fatal("non-quality error should be returned")
	}
	if calls != 1 || preparations != 0 {
		t.Fatalf("calls=%d preparations=%d, want no retry for non-quality error", calls, preparations)
	}
}

func TestRetryAdaptationOutlineQualityReadsExpandedAttemptBudgetDuringRun(t *testing.T) {
	qualityErr := &AdaptationOutlineQualityError{Issues: []AdaptationOutlineQualityIssue{{Code: outlineQualityIssueArcMissingMainline}}}
	maxRetries := 1
	var calls int
	proposal, err := retryAdaptationOutlineQualityDynamic(
		func() int { return maxRetries },
		func() (domain.AdaptationPlan, error) {
			calls++
			if calls <= 3 {
				return domain.AdaptationPlan{}, qualityErr
			}
			return domain.AdaptationPlan{Chapters: []domain.AdaptationChapterPlan{{Chapter: 1}}}, nil
		},
		func(attempt int, _ *AdaptationOutlineQualityError) error {
			if attempt == 1 {
				maxRetries = 3
			}
			return nil
		},
	)
	if err != nil {
		t.Fatalf("retryAdaptationOutlineQualityDynamic: %v", err)
	}
	if len(proposal.Chapters) != 1 || calls != 4 {
		t.Fatalf("proposal=%+v calls=%d, want expanded live budget to allow four total calls", proposal, calls)
	}
}

func TestValidateArcOutlineSourceEventsRetainsEveryDuplicateOwner(t *testing.T) {
	plan := domain.AdaptationPlan{
		Granularity: domain.AdaptationGranularityArc,
		SourceEvents: []domain.AdaptationEvent{{
			ID: "src-event", SourceChapter: 6, Importance: domain.AdaptationEventSupporting,
		}},
		Chapters: []domain.AdaptationChapterPlan{
			{Chapter: 461, EventIDs: []string{"src-event"}},
			{Chapter: 462, EventIDs: []string{"src-event"}},
		},
	}
	issues := validateArcOutlineSourceEvents(plan)
	if len(issues) != 1 {
		t.Fatalf("issues=%+v, want one duplicate ownership issue", issues)
	}
	issue := issues[0]
	if issue.Code != outlineQualityIssueArcDuplicateEvent || issue.TargetChapter != 461 {
		t.Fatalf("issue=%+v", issue)
	}
	targets := outlineIssueTargetChapters(issue)
	if len(targets) != 2 || targets[0] != 461 || targets[1] != 462 {
		t.Fatalf("duplicate owner targets=%v, want [461 462]", targets)
	}
}

func outlineQualityHasCode(err error, code string) bool {
	var qualityErr *AdaptationOutlineQualityError
	if !errors.As(err, &qualityErr) {
		return false
	}
	for _, issue := range qualityErr.Issues {
		if issue.Code == code {
			return true
		}
	}
	return false
}
