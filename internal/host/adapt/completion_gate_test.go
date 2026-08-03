package adapt

import (
	"slices"
	"testing"

	"github.com/voocel/ainovel-cli/internal/adaptaudit"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/store"
)

func TestCompletionGateAllowsOnlyLegacyInconclusive(t *testing.T) {
	t.Run("legacy contract warning", func(t *testing.T) {
		st := completionGateStore(t, domain.AdaptationChapterPlan{
			Chapter: 1, SourceChapters: []int{1}, SourceRange: domain.SourceRange{From: 1, To: 1},
		})
		result, err := NewCompletionGate(st).EvaluateCompletion()
		if err != nil {
			t.Fatal(err)
		}
		if !result.Allowed || result.Status != "inconclusive" || result.Warning == "" {
			t.Fatalf("legacy result=%+v", result)
		}
		runs, err := st.Adaptation.ListAuditRuns()
		if err != nil || len(runs) == 0 || runs[0].Trigger != adaptaudit.AuditTriggerCompletion || !slices.Contains(runs[0].ProtectedReasons, "completion") {
			t.Fatalf("completion run not protected: runs=%+v err=%v", runs, err)
		}
	})

	t.Run("current contract failure", func(t *testing.T) {
		st := completionGateStore(t, domain.AdaptationChapterPlan{
			Chapter: 1,
			SourceSegments: []domain.AdaptationSourceSegment{{
				SourceChapter: 1, Sequence: 1,
				EventIDs:   []string{"source-event-1"},
				RuneShare:  domain.AdaptationSourceRuneShare{Start: 0, End: 6},
				EntryState: domain.AdaptationSegmentState{}, ExitState: domain.AdaptationSegmentState{},
			}},
		})
		result, err := NewCompletionGate(st).EvaluateCompletion()
		if err != nil {
			t.Fatal(err)
		}
		if result.Allowed || result.Status != "fail" || result.ReportDigest == "" {
			t.Fatalf("current-contract result=%+v", result)
		}
	})
}

func TestCompletionGateRejectsPartialAdaptationBeforeAuditing(t *testing.T) {
	st := completionGateStore(t, domain.AdaptationChapterPlan{
		Chapter: 1, SourceChapters: []int{1}, SourceRange: domain.SourceRange{From: 1, To: 1},
	})
	plan, _ := st.Adaptation.LoadPlan()
	plan.Chapters = append(plan.Chapters, domain.AdaptationChapterPlan{Chapter: 2, SourceChapters: []int{1}})
	if err := st.Adaptation.SavePlan(*plan); err != nil {
		t.Fatal(err)
	}
	result, err := NewCompletionGate(st).EvaluateCompletion()
	if err != nil {
		t.Fatal(err)
	}
	if result.Allowed || result.Status != "incomplete" {
		t.Fatalf("partial result=%+v", result)
	}
}

func completionGateStore(t *testing.T, chapter domain.AdaptationChapterPlan) *store.Store {
	t.Helper()
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	if err := st.Adaptation.SaveSourceManifest(domain.AdaptationSourceManifest{
		ChapterCount: 1, Chapters: []domain.AdaptationSource{{Chapter: 1, Runes: 6}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.Adaptation.SavePlan(domain.AdaptationPlan{
		Status: domain.AdaptationPlanStatusConfirmed, Granularity: domain.AdaptationGranularityChapter,
		RewritePolicy: domain.AdaptationRewritePreserveDetails, Chapters: []domain.AdaptationChapterPlan{chapter},
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.Drafts.SaveFinalChapter(1, "target prose"); err != nil {
		t.Fatal(err)
	}
	if err := st.Progress.Save(&domain.Progress{
		Phase: domain.PhaseWriting, Flow: domain.FlowWriting, TotalChapters: 1, CompletedChapters: []int{1},
	}); err != nil {
		t.Fatal(err)
	}
	return st
}
