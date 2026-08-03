package adapt

import (
	"slices"
	"testing"

	"github.com/voocel/ainovel-cli/internal/adaptaudit"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/store"
)

func TestAuditProjectArcAndConfirmedRepairQueue(t *testing.T) {
	const addedBody = "绑匪打来电话。"
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	meet := domain.AdaptationEvent{ID: "meet", Description: "百里冰遇劫，林逸飞出手相救并相识", Origin: domain.AdaptationEventOriginSource, Importance: domain.AdaptationEventMainline, SourceChapter: 1, Required: true}
	caseEvent := domain.AdaptationEvent{ID: "case", Description: "案件线索进入医院", Origin: domain.AdaptationEventOriginSource, Importance: domain.AdaptationEventMainline, SourceChapter: 2, Required: true}
	plan := domain.AdaptationPlan{
		Granularity:   domain.AdaptationGranularityArc,
		RewritePolicy: domain.AdaptationRewriteFullRewrite,
		Status:        domain.AdaptationPlanStatusConfirmed,
		SourceEvents:  []domain.AdaptationEvent{meet, caseEvent},
		Volumes: []domain.AdaptationVolumePlan{{
			Index: 1, Title: "初遇与案件", TargetFrom: 1, TargetTo: 2, SourceFrom: 1, SourceTo: 2,
			MainlineEventIDs: []string{"meet", "case"},
		}},
		Chapters: []domain.AdaptationChapterPlan{
			{Chapter: 1, Title: "案件", SourceChapters: []int{1, 2}, SourceRange: domain.SourceRange{From: 1, To: 2}, EventIDs: []string{"case"}},
			{Chapter: 2, Title: "绑架", SourceChapters: []int{1, 2}, SourceRange: domain.SourceRange{From: 1, To: 2}, AddedEventIDs: []string{"kidnap"}},
		},
	}
	if err := st.Adaptation.SavePlan(plan); err != nil {
		t.Fatalf("SavePlan: %v", err)
	}
	if err := st.Drafts.SaveFinalChapter(1, "案件线索进入医院，众人追查病历。"); err != nil {
		t.Fatalf("SaveFinalChapter 1: %v", err)
	}
	if err := st.Drafts.SaveFinalChapter(2, addedBody); err != nil {
		t.Fatalf("SaveFinalChapter 2: %v", err)
	}
	if err := st.Adaptation.SaveCheck(domain.AdaptationCheck{
		Chapter: 1, DraftSHA256: store.TextSHA256("案件线索进入医院，众人追查病历。"), Passed: true,
		BodyEvidence: []domain.AdaptationBodyEvidence{{EventID: "case", Quote: "案件线索进入医院"}}, CheckedAt: "2026-07-10T00:00:00Z",
	}); err != nil {
		t.Fatalf("SaveCheck: %v", err)
	}
	if err := st.Progress.Save(&domain.Progress{
		NovelName: "audit", Phase: domain.PhaseWriting, Flow: domain.FlowWriting,
		TotalChapters: 2, CurrentChapter: 2, CompletedChapters: []int{1, 2},
	}); err != nil {
		t.Fatalf("Save progress: %v", err)
	}

	report, err := AuditProject(st, AuditOptions{SourceTo: 2})
	if err != nil {
		t.Fatalf("AuditProject: %v", err)
	}
	for _, code := range []string{"missing_mainline_plan_binding", "missing_mainline_body_evidence", "added_event_displaces_mainline"} {
		if !reportHasFinding(report, code) {
			t.Fatalf("missing %s: %+v", code, report.Findings)
		}
	}
	if err := st.Drafts.SaveFinalChapter(2, addedBody+"审计后变化"); err != nil {
		t.Fatalf("modify chapter after audit: %v", err)
	}
	staleRequest := adaptaudit.ConfirmationRequest{
		ReportDigest: report.Digest, Decision: "apply",
		AcknowledgedFindingIDs: append([]string(nil), report.Confirmation.BlockingFindingIDs...),
	}
	if _, err := ApplyProjectAuditRepair(st, staleRequest); err == nil {
		t.Fatal("audit created before a draft change must be rejected as stale")
	}
	if err := st.Drafts.SaveFinalChapter(2, addedBody); err != nil {
		t.Fatalf("restore chapter after stale check: %v", err)
	}
	application, err := ApplyProjectAuditRepair(st, adaptaudit.ConfirmationRequest{
		ReportDigest: report.Digest, Decision: "apply",
		AcknowledgedFindingIDs: append([]string(nil), report.Confirmation.BlockingFindingIDs...),
	})
	if err != nil {
		t.Fatalf("ApplyProjectAuditRepair: %v", err)
	}
	if application.BackupPath == "" || !slices.Contains(application.QueuedChapters, 1) {
		t.Fatalf("application=%+v", application)
	}
	repaired, err := st.Adaptation.LoadPlan()
	if err != nil || repaired == nil {
		t.Fatalf("LoadPlan: %v plan=%+v", err, repaired)
	}
	if !slices.Contains(repaired.Chapters[0].EventIDs, "meet") || len(repaired.Chapters[0].RequiredChanges) == 0 {
		t.Fatalf("missing mainline event was not inserted into structured chapter duty: %+v", repaired.Chapters[0])
	}
}

func reportHasFinding(report *adaptaudit.Report, code string) bool {
	for _, finding := range report.Findings {
		if finding.Code == code {
			return true
		}
	}
	return false
}

func TestMergeRewriteReasonPreservesExistingQueueContext(t *testing.T) {
	if got := mergeRewriteReason("manual chapter repair", "adaptation audit repair"); got != "manual chapter repair; adaptation audit repair" {
		t.Fatalf("mergeRewriteReason() = %q", got)
	}
	if got := mergeRewriteReason("manual; adaptation audit repair", "adaptation audit repair"); got != "manual; adaptation audit repair" {
		t.Fatalf("duplicate reason was not preserved: %q", got)
	}
}

func TestBestChapterForSourceEventUsesNarrativeEvidence(t *testing.T) {
	chapters := []domain.AdaptationChapterPlan{
		{Chapter: 1, Title: "新增追逐", SourceRange: domain.SourceRange{From: 1, To: 20}, AddedEventIDs: []string{"chase"}},
		{Chapter: 2, Title: "二人初遇", SourceRange: domain.SourceRange{From: 1, To: 20}},
	}
	event := domain.AdaptationEvent{SourceChapter: 13, Description: "百里冰与林逸飞初遇"}
	if got := bestChapterForSourceEvent(chapters, event); got != 1 {
		t.Fatalf("best chapter index=%d", got)
	}
}

func TestAuditProjectFreeUsesTargetRelationshipAndSettingContracts(t *testing.T) {
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	body := "Their relationship becomes lovers, and magic is allowed."
	plan := domain.AdaptationPlan{
		Granularity: domain.AdaptationGranularityFree, RewritePolicy: domain.AdaptationRewriteFullRewrite,
		Status:                   domain.AdaptationPlanStatusConfirmed,
		TargetRelationshipStates: map[string]string{"hero|partner": "strangers"},
		TargetSettingLocks:       []domain.AdaptationSettingLock{{Key: "magic", Value: "forbidden"}},
		TargetEventLedger: []domain.AdaptationEvent{{
			ID: "love", Origin: domain.AdaptationEventOriginTarget, Description: "become lovers",
			Relationship: &domain.AdaptationRelationshipTransition{
				Pair: "hero|partner", From: "strangers", To: "lovers",
				AllowedFrom: []string{"trust"}, RequiresEventIDs: []string{"meet"},
			},
			SettingClaims: []domain.AdaptationSettingClaim{{Key: "magic", Value: "allowed"}},
		}},
		Chapters: []domain.AdaptationChapterPlan{{Chapter: 1, Title: "jump", EventIDs: []string{"love"}}},
	}
	if err := st.Adaptation.SavePlan(plan); err != nil {
		t.Fatalf("SavePlan: %v", err)
	}
	if err := st.Drafts.SaveFinalChapter(1, body); err != nil {
		t.Fatalf("SaveFinalChapter: %v", err)
	}
	if err := st.Adaptation.SaveCheck(domain.AdaptationCheck{
		Chapter: 1, DraftSHA256: store.TextSHA256(body), Passed: true,
		BodyEvidence: []domain.AdaptationBodyEvidence{{EventID: "love", Quote: "relationship becomes lovers, and magic is allowed"}},
	}); err != nil {
		t.Fatalf("SaveCheck: %v", err)
	}
	if err := st.Progress.Save(&domain.Progress{
		NovelName: "free audit", Phase: domain.PhaseWriting, Flow: domain.FlowWriting,
		TotalChapters: 1, CurrentChapter: 2, CompletedChapters: []int{1},
	}); err != nil {
		t.Fatalf("Save progress: %v", err)
	}
	report, err := AuditProject(st, AuditOptions{})
	if err != nil {
		t.Fatalf("AuditProject: %v", err)
	}
	for _, code := range []string{"relationship_state_jump", "setting_lock_conflict"} {
		if !reportHasFinding(report, code) {
			t.Fatalf("missing %s: %+v", code, report.Findings)
		}
	}
}

func TestAuditProjectChapterMarksLegacyLongChapterInconclusive(t *testing.T) {
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := st.Adaptation.SaveSourceManifest(domain.AdaptationSourceManifest{
		ChapterCount: 1, Chapters: []domain.AdaptationSource{{Chapter: 1, Runes: 10_000}},
	}); err != nil {
		t.Fatalf("SaveSourceManifest: %v", err)
	}
	if err := st.Adaptation.SavePlan(domain.AdaptationPlan{
		Granularity: domain.AdaptationGranularityChapter, Status: domain.AdaptationPlanStatusConfirmed,
		RewritePolicy: domain.AdaptationRewritePreserveDetails,
		Chapters:      []domain.AdaptationChapterPlan{{Chapter: 1, SourceChapters: []int{1}, SourceRange: domain.SourceRange{From: 1, To: 1}}},
	}); err != nil {
		t.Fatalf("SavePlan: %v", err)
	}
	if err := st.Progress.Save(&domain.Progress{
		NovelName: "legacy chapter audit", Phase: domain.PhaseWriting, Flow: domain.FlowWriting,
		TotalChapters: 1, CurrentChapter: 2, CompletedChapters: []int{1},
	}); err != nil {
		t.Fatalf("Save progress: %v", err)
	}
	report, err := AuditProject(st, AuditOptions{})
	if err != nil {
		t.Fatalf("AuditProject: %v", err)
	}
	if report.Status != "inconclusive" || !reportHasFinding(report, "audit_contract_unavailable") {
		t.Fatalf("legacy report should be inconclusive and non-repairable: %+v", report)
	}
	if report.Confirmation.Required {
		t.Fatalf("legacy report must not offer auto-repair: %+v", report.Confirmation)
	}
	if _, err := ApplyProjectAuditRepair(st, adaptaudit.ConfirmationRequest{
		ReportDigest: report.Digest,
		Decision:     "apply",
	}); err == nil {
		t.Fatal("direct apply must reject an inconclusive legacy report")
	}
}

func TestResolveArcAuditScopeStopsBeforePartialBatch(t *testing.T) {
	plan := domain.AdaptationPlan{
		Granularity: domain.AdaptationGranularityArc,
		Volumes: []domain.AdaptationVolumePlan{
			{Index: 1, SourceFrom: 1, SourceTo: 4, TargetFrom: 1, TargetTo: 2},
			{Index: 2, SourceFrom: 5, SourceTo: 9, TargetFrom: 3, TargetTo: 5},
		},
	}
	progress := &domain.Progress{CompletedChapters: []int{1, 2, 3, 4}}
	scope, err := resolveAuditScope(plan, &domain.AdaptationSourceManifest{ChapterCount: 9}, progress, 9)
	if err != nil {
		t.Fatalf("resolveAuditScope: %v", err)
	}
	if scope.SourceFrom != 1 || scope.SourceTo != 4 || scope.TargetFrom != 1 || scope.TargetTo != 2 {
		t.Fatalf("scope=%+v, want source 1-4 / target 1-2", scope)
	}
}

func TestResolveArcAuditScopeSnapsSelectionToCompletedContainingBatch(t *testing.T) {
	plan := domain.AdaptationPlan{
		Granularity: domain.AdaptationGranularityArc,
		Volumes: []domain.AdaptationVolumePlan{
			{Index: 1, SourceFrom: 1, SourceTo: 4, TargetFrom: 1, TargetTo: 2},
			{Index: 2, SourceFrom: 5, SourceTo: 9, TargetFrom: 3, TargetTo: 5},
		},
	}
	progress := &domain.Progress{CompletedChapters: []int{1, 2, 3, 4}}
	scope, err := resolveAuditScope(plan, &domain.AdaptationSourceManifest{ChapterCount: 9}, progress, 3)
	if err != nil {
		t.Fatalf("resolveAuditScope: %v", err)
	}
	if scope.SourceTo != 4 || scope.TargetTo != 2 {
		t.Fatalf("scope=%+v, want completed containing batch source 1-4 / target 1-2", scope)
	}
}

func TestResolveChapterAuditScopeExcludesIncompleteSplit(t *testing.T) {
	plan := domain.AdaptationPlan{
		Granularity: domain.AdaptationGranularityChapter,
		Chapters: []domain.AdaptationChapterPlan{
			{Chapter: 1, SourceSegments: []domain.AdaptationSourceSegment{{SourceChapter: 1, Sequence: 1, EventIDs: []string{"s1-a"}, RuneShare: domain.AdaptationSourceRuneShare{Start: 0, End: 5000}, EntryState: domain.AdaptationSegmentState{}, ExitState: domain.AdaptationSegmentState{"state": "middle"}}}},
			{Chapter: 2, SourceSegments: []domain.AdaptationSourceSegment{{SourceChapter: 1, Sequence: 2, EventIDs: []string{"s1-b"}, RuneShare: domain.AdaptationSourceRuneShare{Start: 5000, End: 10000}, EntryState: domain.AdaptationSegmentState{"state": "middle"}, ExitState: domain.AdaptationSegmentState{"state": "done"}}}},
			{Chapter: 3, SourceSegments: []domain.AdaptationSourceSegment{{SourceChapter: 2, Sequence: 1, EventIDs: []string{"s2-a"}, RuneShare: domain.AdaptationSourceRuneShare{Start: 0, End: 5000}, EntryState: domain.AdaptationSegmentState{}, ExitState: domain.AdaptationSegmentState{"state": "middle"}}}},
			{Chapter: 4, SourceSegments: []domain.AdaptationSourceSegment{{SourceChapter: 2, Sequence: 2, EventIDs: []string{"s2-b"}, RuneShare: domain.AdaptationSourceRuneShare{Start: 5000, End: 10000}, EntryState: domain.AdaptationSegmentState{"state": "middle"}, ExitState: domain.AdaptationSegmentState{"state": "done"}}}},
		},
	}
	progress := &domain.Progress{CompletedChapters: []int{1, 2, 3}}
	manifest := &domain.AdaptationSourceManifest{ChapterCount: 2, Chapters: []domain.AdaptationSource{
		{Chapter: 1, Runes: 10000},
		{Chapter: 2, Runes: 10000},
	}}
	scope, err := resolveAuditScope(plan, manifest, progress, 2)
	if err != nil {
		t.Fatalf("resolveAuditScope: %v", err)
	}
	if scope.SourceTo != 1 || scope.TargetTo != 2 {
		t.Fatalf("scope=%+v, want only fully adapted source chapter 1", scope)
	}
}

func TestResolveChapterAuditScopeRejectsIncompleteSegmentContract(t *testing.T) {
	plan := domain.AdaptationPlan{
		Granularity: domain.AdaptationGranularityChapter,
		Chapters: []domain.AdaptationChapterPlan{{
			Chapter: 1,
			SourceSegments: []domain.AdaptationSourceSegment{{
				SourceChapter: 1, Sequence: 1, EventIDs: []string{"only-half"},
				RuneShare:  domain.AdaptationSourceRuneShare{Start: 0, End: 5000},
				EntryState: domain.AdaptationSegmentState{}, ExitState: domain.AdaptationSegmentState{},
			}},
		}},
	}
	progress := &domain.Progress{CompletedChapters: []int{1}}
	manifest := &domain.AdaptationSourceManifest{ChapterCount: 1, Chapters: []domain.AdaptationSource{{Chapter: 1, Runes: 10000}}}
	if _, err := resolveAuditScope(plan, manifest, progress, 1); err == nil {
		t.Fatal("half-covered source chapter must not be treated as fully adapted")
	}
}
