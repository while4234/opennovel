package flow

import (
	"fmt"
	"strings"

	"github.com/voocel/ainovel-cli/internal/adaptaudit"
	"github.com/voocel/ainovel-cli/internal/domain"
	adaptpkg "github.com/voocel/ainovel-cli/internal/host/adapt"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
	"github.com/voocel/ainovel-cli/internal/tools"
)

// LoadState 从 Store 读取 Route 所需的全部事实。
// 这是路由的"IO 边界"：所有读取集中在这里，Route 保持纯。
// 读取失败按保守默认填充（has*=false, boundary=nil），让 Router 倾向重派而非跳过。
func LoadState(store *storepkg.Store) State {
	s := State{
		FoundationMissing: store.FoundationMissing(),
	}
	if active, err := store.Revisions.Active(); err != nil {
		// Corrupt or unreadable revision state must fail closed. Treat it as an
		// unroutable active revision so ordinary creation cannot overwrite work.
		s.RevisionActive = true
	} else if active != nil {
		s.RevisionActive = true
		s.RevisionMode = active.Mode
		if active.Route != nil {
			route := *active.Route
			s.RevisionRoute = &route
		}
	}
	if review, err := store.RunMeta.PlanningReview(); err == nil {
		s.PlanningReview = review
		if review != nil && review.Status == domain.PlanningReviewStatusCollecting {
			switch review.Kind {
			case domain.PlanningReviewKindBlueprint:
				loadOriginalSkeletonState(&s, store, review)
			case domain.PlanningReviewKindVolumeSplit:
				s.OriginalPlanningWork, _ = store.OriginalPlanningAudits.NextWork(store.Outline)
			}
		}
	}
	loadCharacterWorkflowState(&s, store)
	loadCastPromotionState(&s, store)
	progress, err := store.Progress.Load()
	if err != nil || progress == nil {
		return s
	}
	s.Progress = progress
	loadWriterResumeState(&s, store, progress)
	loadAdaptationState(&s, store, progress)
	loadContinuationState(&s, store)

	if repair, rerr := store.FindDuplicateOutlineRepairBatch(progress); rerr == nil && repair != nil {
		s.OutlineRepair = repair
	}

	if n := len(progress.CompletedChapters); n > 0 {
		s.LastCompleted = progress.CompletedChapters[n-1]
	}

	if progress.Layered && len(progress.PendingRewrites) == 0 {
		if boundary, berr := store.FindPendingArcPostprocess(progress); berr == nil && boundary != nil {
			s.ArcBoundary = boundary
			s.HasArcReview = store.World.HasArcReview(boundary.LastChapter) ||
				store.Checkpoints.LatestByStep(domain.ArcScope(boundary.Volume, boundary.Arc), "review") != nil
			if !s.HasArcReview {
				s.ArcReviewBatch, _ = store.NextArcReviewBatch(boundary, domain.ArcReviewBatchRuneBudget)
			}
			s.HasArcSummary = store.Summaries.HasArcSummary(boundary.Volume, boundary.Arc)
			if boundary.IsVolumeEnd {
				s.HasVolumeSummary = store.Summaries.HasVolumeSummary(boundary.Volume)
			}
			return s
		}
	}

	// 弧边界仅在分层模式且有已完成章节时才计算
	if progress.Layered && s.LastCompleted > 0 {
		if boundary, berr := store.Outline.CheckArcBoundary(s.LastCompleted); berr == nil && boundary != nil {
			s.ArcBoundary = boundary
			if boundary.IsArcEnd {
				s.HasArcReview = store.World.HasArcReview(s.LastCompleted) ||
					store.Checkpoints.LatestByStep(domain.ArcScope(boundary.Volume, boundary.Arc), "review") != nil
				if !s.HasArcReview {
					s.ArcReviewBatch, _ = store.NextArcReviewBatch(boundary, domain.ArcReviewBatchRuneBudget)
				}
				s.HasArcSummary = store.Summaries.HasArcSummary(boundary.Volume, boundary.Arc)
				if boundary.IsVolumeEnd {
					s.HasVolumeSummary = store.Summaries.HasVolumeSummary(boundary.Volume)
				}
			}
		}
	}

	return s
}

func loadCastPromotionState(state *State, st *storepkg.Store) {
	if state == nil || st == nil {
		return
	}
	pending, err := st.Cast.PendingPromotions()
	if err != nil || len(pending) == 0 {
		return
	}
	entry := pending[0]
	state.CastPromotionEntry = &entry
	state.CastPromotion, _ = st.Cast.LoadPromotionWorkflow()
}

func loadCharacterWorkflowState(state *State, st *storepkg.Store) {
	if state == nil || st == nil {
		return
	}
	originalPending := state.PlanningReview != nil &&
		state.PlanningReview.Kind == domain.PlanningReviewKindFoundation
	adaptationPending := false
	if workflow, err := st.Adaptation.LoadPlanningWorkflow(); err == nil && workflow != nil &&
		workflow.Stage == domain.AdaptationPlanningStageTargetFoundationGenerating {
		if gate, gateErr := st.CoreCast.LoadGateBinding(); gateErr == nil && gate != nil &&
			gate.Mode == domain.CoreCastModeAdaptation {
			adaptationPending = true
		}
	}
	if !originalPending && !adaptationPending {
		return
	}
	state.AdaptationCharacterPending = adaptationPending
	candidate, lifecycle, binding, err := tools.CurrentCharacterWorkflow(st)
	state.CharacterCandidate = candidate
	state.CharacterLifecycle = lifecycle
	state.CharacterBinding = binding
	if err != nil {
		state.CharacterStateErr = err.Error()
		_, canonicalBinding, _, _, canonicalErr := tools.CurrentCharacterCanonicalBinding(st)
		if canonicalErr == nil {
			state.CharacterBinding = canonicalBinding
		}
	} else if candidate == nil {
		_, canonicalBinding, _, _, canonicalErr := tools.CurrentCharacterCanonicalBinding(st)
		if canonicalErr == nil {
			state.CharacterBinding = canonicalBinding
		}
	}
}

func loadWriterResumeState(s *State, st *storepkg.Store, progress *domain.Progress) {
	if s == nil || st == nil || progress == nil || progress.InProgressChapter <= 0 {
		return
	}
	chapter := progress.InProgressChapter
	draft, err := st.Drafts.LoadDraft(chapter)
	if err != nil || draft == "" {
		return
	}
	s.InProgressDraftExists = true
	if final, finalErr := st.Drafts.LoadChapterText(chapter); finalErr == nil {
		s.InProgressDraftDiffersFinal = draft != final
	}
	s.InProgressWordCount = len([]rune(draft))
	s.InProgressLineCount = len(strings.Split(draft, "\n"))
	draftSHA := storepkg.TextSHA256(draft)
	if checkpoint := st.Checkpoints.Latest(domain.ChapterScope(chapter)); checkpoint != nil {
		s.InProgressCheckpoint = checkpoint.Step
	}
	if checkpoint := st.Checkpoints.LatestByStepPrefix(domain.ChapterScope(chapter), "word_budget_edit_segment_"); checkpoint != nil {
		s.InProgressBudgetCheckpoint = checkpoint.Step
	}
	if checkpoint := st.Checkpoints.LatestByStep(domain.ChapterScope(chapter), "consistency_check"); checkpoint != nil {
		s.InProgressConsistencyValid = checkpoint.Digest == "sha256:"+draftSHA
	}
	if progress.Flow != domain.FlowPolishing && len(progress.PendingRewrites) == 0 {
		loadWriterWordBudgetState(s, st, progress, chapter)
	}

	s.InProgressDeAIState = writerDeAIStateMissing
	audit, auditErr := st.DeAI.LoadAudit(chapter)
	if auditErr != nil || audit == nil {
		return
	}
	if audit.DraftSHA256 != draftSHA {
		s.InProgressDeAIState = writerDeAIStateStale
		return
	}
	if audit.Passed {
		s.InProgressDeAIState = writerDeAIStatePassed
		return
	}
	s.InProgressDeAIState = writerDeAIStateFailed
}

func loadWriterWordBudgetState(s *State, st *storepkg.Store, progress *domain.Progress, chapter int) {
	_, policy, ok, err := st.ChapterWordBudgetPolicy(progress, chapter)
	if err != nil || !ok {
		return
	}
	s.InProgressWordMin = policy.HardMinWords
	s.InProgressWordMax = policy.HardMaxWords
	s.InProgressRecommendedMin = policy.RecommendedMinWords
	s.InProgressRecommendedMax = policy.RecommendedMaxWords
	s.InProgressWordBudgetValid = policy.WithinHardRange(s.InProgressWordCount)
}

func loadOriginalSkeletonState(s *State, st *storepkg.Store, review *domain.PlanningReview) {
	if s == nil || st == nil || review == nil {
		return
	}
	volumes, err := st.Outline.LoadLayeredOutline()
	if err != nil || len(volumes) == 0 {
		return
	}
	s.BlueprintVolumeCount = len(volumes)
	totalChapters := domain.TotalChapters(volumes)
	missing := st.FoundationMissing()
	foundationReady := true
	for _, item := range missing {
		if item != "outline" {
			foundationReady = false
			break
		}
	}
	targetWords := review.TargetTotalWords
	if targetWords <= 0 {
		if meta, loadErr := st.RunMeta.Load(); loadErr == nil && meta != nil && meta.WordBudget != nil {
			targetWords = meta.WordBudget.TargetTotalWords
		}
	}
	minimumVolumes, minimumChapters := 2, 0
	if targetWords > 0 {
		minimumVolumes = (targetWords + 39_999) / 40_000
		minimumChapters = (targetWords + 4_999) / 5_000
	}
	skeletonReady := foundationReady && len(volumes) >= minimumVolumes &&
		(minimumChapters == 0 || totalChapters >= minimumChapters)
	if skeletonReady {
		s.SkeletonPlanningWork, _ = st.OriginalPlanningAudits.NextSkeletonWork(st.Outline)
		return
	}
	// A new skeleton volume contains at least two three-chapter arcs. Tell the
	// planner when that next bounded batch must also deliver the book ending.
	s.BlueprintNextIsFinal = len(volumes)+1 >= minimumVolumes &&
		(minimumChapters == 0 || totalChapters+6 >= minimumChapters)
}

func loadContinuationState(s *State, store *storepkg.Store) {
	if s == nil || store == nil {
		return
	}
	snapshot, err := store.Continuation.LoadSnapshot()
	if err != nil || snapshot == nil || snapshot.Plan == nil || snapshot.Workflow.Stage != domain.ContinuationStageWriting {
		return
	}
	s.ContinuationActive = true
	s.ContinuationBaseChapter = snapshot.Workflow.BaseChapterCount
}

func loadAdaptationState(s *State, store *storepkg.Store, progress *domain.Progress) {
	if s == nil || store == nil || progress == nil {
		return
	}
	plan, err := store.Adaptation.LoadPlan()
	if err != nil || plan == nil || plan.Status != domain.AdaptationPlanStatusConfirmed {
		return
	}

	s.AdaptationActive = true
	// Re-check the durable arc contract at the routing boundary. A legacy or
	// externally repaired plan must not keep dispatching Writer after its event
	// ownership becomes invalid.
	if domain.NormalizeAdaptationGranularity(plan.Granularity) == domain.AdaptationGranularityArc {
		if densityIssues := domain.ValidateArcChapterBudgetDensity(*plan); len(densityIssues) > 0 {
			s.AdaptationOutlineBlocked = true
			s.AdaptationOutlineBlockReason = fmt.Sprintf("legacy chapter-budget density requires Host budget-only model preflight: %s", densityIssues[0].Detail)
			return
		}
		if !domain.AdaptationOutlineQualityPassed(*plan) {
			targetChapter := progress.InProgressChapter
			if targetChapter <= 0 {
				targetChapter = progress.NextChapter()
			}
			if qualityErr := adaptpkg.ValidateAdaptationChapterOutlineQuality(plan, targetChapter); qualityErr != nil {
				s.AdaptationOutlineBlocked = true
				s.AdaptationOutlineBlockReason = qualityErr.Error()
			}
		}
	}
	s.AdaptationPlannedChapters = make(map[int]struct{}, len(plan.Chapters))
	completed := make(map[int]struct{}, len(progress.CompletedChapters))
	for _, chapter := range progress.CompletedChapters {
		completed[chapter] = struct{}{}
	}
	s.AdaptationComplete = len(plan.Chapters) > 0
	for _, chapterPlan := range plan.Chapters {
		s.AdaptationPlannedChapters[chapterPlan.Chapter] = struct{}{}
		if chapterPlan.Chapter > s.AdaptationMaxChapter {
			s.AdaptationMaxChapter = chapterPlan.Chapter
		}
		if _, ok := completed[chapterPlan.Chapter]; !ok {
			s.AdaptationComplete = false
		}
	}
	if progress.CompletionAuditStatus != "" && progress.CompletionAuditStatus != "pass" && progress.CompletionAuditStatus != "inconclusive" {
		s.CompletionAuditBlocked = true
	}
	if s.AdaptationComplete {
		report, reportErr := store.Adaptation.LoadAuditReport()
		if reportErr == nil && report != nil && !completionReportAllows(report) && report.Scope.TargetTo >= s.AdaptationMaxChapter {
			s.CompletionAuditBlocked = true
		}
	}
}

func completionReportAllows(report *adaptaudit.Report) bool {
	if report == nil {
		return false
	}
	if report.Status == "pass" {
		return true
	}
	if report.Status != "inconclusive" {
		return false
	}
	for _, finding := range report.Findings {
		if finding.Code == "audit_contract_unavailable" {
			return true
		}
	}
	return false
}
