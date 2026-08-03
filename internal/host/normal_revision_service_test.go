package host

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
)

func TestNormalRevisionServicePersistsCompletedBookExpansionThroughKernelAndSignedGates(t *testing.T) {
	st := newPublicationTestStore(t)
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	vol1 := domain.LegacyStructureID("normal-service", domain.StructureKindVolume, "volume-1")
	arc1 := domain.LegacyStructureID("normal-service", domain.StructureKindArc, "volume-1/arc-1")
	ch1 := domain.LegacyStructureID("normal-service", domain.StructureKindChapter, "volume-1/arc-1/chapter-1")
	vol2 := domain.LegacyStructureID("normal-service", domain.StructureKindVolume, "volume-2")
	arc2 := domain.LegacyStructureID("normal-service", domain.StructureKindArc, "volume-2/arc-1")
	ch2 := domain.LegacyStructureID("normal-service", domain.StructureKindChapter, "volume-2/arc-1/chapter-2")
	current := []domain.VolumeOutline{{
		ID: vol1, Index: 1, Title: "First", Theme: "phase closes",
		Arcs: []domain.ArcOutline{{ID: arc1, Index: 1, Title: "First arc", Goal: "close phase", Chapters: []domain.OutlineEntry{{ID: ch1, Chapter: 1, Title: "End", CoreEvent: "old phase ends", Hook: "a cost remains", Scenes: []string{"the phase closes"}}}}},
	}}
	if err := st.Outline.SaveLayeredOutline(current); err != nil {
		t.Fatal(err)
	}
	if err := st.Progress.Init("completed", 1); err != nil {
		t.Fatal(err)
	}
	for _, phase := range []domain.Phase{domain.PhaseOutline, domain.PhaseWriting, domain.PhaseComplete} {
		if err := st.Progress.UpdatePhase(phase); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.Progress.SetCompletionAudit("pass", "completion-before-expansion"); err != nil {
		t.Fatal(err)
	}
	candidate := append(domain.CloneStructureSnapshot(current), domain.VolumeOutline{
		ID: vol2, Index: 2, Title: "Aftermath", Theme: "a separate succession phase",
		Arcs: []domain.ArcOutline{{ID: arc2, Index: 1, Title: "Succession", Goal: "pay the remaining cost", Chapters: []domain.OutlineEntry{{ID: ch2, Chapter: 2, Title: "Claim", CoreEvent: "the claimant acts", Hook: "the realm must choose", Scenes: []string{"the claim divides the court"}}}}},
	})
	budget, err := domain.NewDynamicSoftBudget(2, 3000, 5000)
	if err != nil {
		t.Fatal(err)
	}
	planner := &fakeNormalStructurePlanner{proposal: domain.StructureRevisionProposal{
		Assessment: domain.ContentAdditionAssessment{NeedsAdditionalChapters: true, Reason: "the paid-off first phase cannot contain the succession phase", NewVolume: &domain.DramaticStageEvidence{
			EntryState: "the old conflict is closed", IndependentConflict: "succession", ArcProgression: "claims divide then realign the court",
			Climax: "the realm chooses", IrreversibleOutcome: "a new ruler is bound", CannotFitCurrentVolume: "the first volume already paid its climax and exit",
		}},
		Candidate: candidate, SoftBudget: budget,
	}}
	service := NewNormalRevisionService(st)
	previewed, err := service.Preview(context.Background(), planner, domain.StructureRevisionRequest{
		Operation: domain.StructureRevisionAppendVolume, Intent: "append the succession aftermath", Stage: domain.ManuscriptStageComplete,
		BaseRevision: 1, Current: current, CompletedChapterIDs: []string{ch1}, CurrentSoftBudget: &budget,
	}, "preview")
	if err != nil {
		t.Fatal(err)
	}
	previewChapter := previewed.Preview.Proposal.Candidate[1].Arcs[0].Chapters[0]
	if previewChapter.Title != "" || previewChapter.CoreEvent != "" || len(previewChapter.Scenes) != 0 {
		t.Fatalf("structure preview generated detailed outline before approval: %+v", previewChapter)
	}
	if active, loadErr := st.Revisions.Active(); loadErr != nil || active == nil || active.ID != previewed.Session.ID {
		t.Fatalf("persistent preview session=%+v err=%v", active, loadErr)
	}
	session, err := service.ApproveImpact(previewed.Session.ID, previewed.Session.Revision, "approve-impact")
	if err != nil {
		t.Fatal(err)
	}
	session, err = service.SubmitStructureCandidate(*previewed.Preview, session.ID, session.Revision, "structure-candidate")
	if err != nil {
		t.Fatal(err)
	}
	if _, driftErr := service.SubmitFeedback(session, "different-impact-signature", "move this feedback elsewhere", "drift"); driftErr == nil {
		t.Fatal("target-drift feedback was accepted without a new preview")
	}
	structureAudits := passedNormalAuditExpectations(session)
	if _, err := service.RecordAuditSet(session, structureAudits[:len(structureAudits)-1], "missing-structure-scope"); err == nil {
		t.Fatal("missing exact structure audit scope was accepted")
	}
	forged := append(append([]domain.RevisionAuditEvidence(nil), structureAudits...), domain.RevisionAuditEvidence{
		Scope: "skeleton_book_batch", ScopeID: "invented", ContentSignature: session.CandidateSignature, Passed: true,
	})
	if _, err := service.RecordAuditSet(session, forged, "forged-structure-scope"); err == nil {
		t.Fatal("fictional structure audit scope was accepted")
	}
	substituted := append([]domain.RevisionAuditEvidence(nil), structureAudits...)
	substituted[0].ContentSignature = session.CandidateSignature
	if substituted[0].ContentSignature != structureAudits[0].ContentSignature {
		if _, err := service.RecordAuditSet(session, substituted, "whole-candidate-substitution"); err == nil {
			t.Fatal("whole-candidate signature was accepted for a local scope")
		}
	}
	session, err = service.RecordAuditSet(session, structureAudits, "structure-audits")
	if err != nil {
		t.Fatal(err)
	}
	session, err = service.ApproveStage(session, "approve-structure")
	if err != nil {
		t.Fatal(err)
	}
	session, err = service.SubmitDetailedOutlineCandidate(candidate, session, "outline-candidate")
	if err != nil {
		t.Fatal(err)
	}
	outlineAudits := passedNormalAuditExpectations(session)
	session, err = service.RecordAuditSet(session, outlineAudits, "outline-audits")
	if err != nil {
		t.Fatal(err)
	}
	session, err = service.ApproveStage(session, "approve-outline")
	if err != nil {
		t.Fatal(err)
	}
	if session.Stage == domain.RevisionStageCandidateGenerating {
		session, err = service.SubmitProseReworkCandidate(session, "prose-intents")
		if err != nil {
			t.Fatal(err)
		}
		session, err = service.RecordAuditSet(session, passedNormalAuditExpectations(session), "prose-audits")
		if err != nil {
			t.Fatal(err)
		}
		session, err = service.ApproveStage(session, "approve-prose")
		if err != nil {
			t.Fatal(err)
		}
	}
	alternate, err := service.kernel.Plan(context.Background(), normalSkeletonPlanner{planner: planner, current: current}, domain.StructureRevisionRequest{
		Operation: domain.StructureRevisionAppendVolume, Intent: "different valid preview", Stage: domain.ManuscriptStageComplete,
		BaseRevision: 1, Current: current, CompletedChapterIDs: []string{ch1}, CurrentSoftBudget: &budget,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.PublishStructure(*alternate, session, "substituted-publish"); err == nil {
		t.Fatal("different valid sealed preview was accepted for publication")
	}
	publishInput := storepkg.RevisionMutationInput{SessionID: session.ID, ExpectedRevision: session.Revision, IdempotencyKey: "publish"}
	if _, err := st.Revisions.Publish(domain.NormalRevisionPolicy{}, publishInput); !errors.Is(err, storepkg.ErrRevisionCommandInProgress) {
		t.Fatalf("host-ready normal session accepted plain Publish: %v", err)
	}
	active, err := st.Revisions.Active()
	if err != nil || active == nil || active.ID != session.ID || active.Stage != domain.RevisionStageReadyToPublish {
		t.Fatalf("plain Publish consumed host lifecycle: active=%+v err=%v", active, err)
	}
	formalBeforeOwner, err := st.Outline.LoadLayeredOutline()
	if err != nil || len(formalBeforeOwner) != 1 {
		t.Fatalf("plain Publish changed host formal structure: volumes=%d err=%v", len(formalBeforeOwner), err)
	}
	var pauseErr error
	service.beforeRevisionCommit = func() {
		_, pauseErr = st.Revisions.Pause(domain.NormalRevisionPolicy{}, storepkg.RevisionMutationInput{
			SessionID: session.ID, ExpectedRevision: session.Revision, IdempotencyKey: "pause-during-publish",
		})
		service.beforeRevisionCommit = nil
	}
	published, err := service.PublishStructure(*previewed.Preview, session, publishInput.IdempotencyKey)
	if err != nil || published.Stage != domain.RevisionStageCompleted {
		t.Fatalf("publish=%+v err=%v", published, err)
	}
	if !errors.Is(pauseErr, storepkg.ErrRevisionCommandInProgress) {
		t.Fatalf("successor mutation entered durable publication attempt: %v", pauseErr)
	}
	if _, err := service.PublishStructure(*previewed.Preview, session, "publish"); err == nil {
		t.Fatal("completed normal publication owner was replayable")
	}
	formal, _ := st.Outline.LoadLayeredOutline()
	progress, _ := st.Progress.Load()
	if len(formal) != 2 || progress == nil || progress.Phase != domain.PhaseWriting || progress.TotalChapters != 2 {
		t.Fatalf("formal volumes=%d progress=%+v", len(formal), progress)
	}
}

func passedNormalAuditExpectations(session *domain.RevisionSession) []domain.RevisionAuditEvidence {
	evidence := make([]domain.RevisionAuditEvidence, 0, len(session.AuditExpectations))
	for _, expected := range session.AuditExpectations {
		evidence = append(evidence, domain.RevisionAuditEvidence{
			Scope: expected.Scope, ScopeID: expected.ScopeID, FromChapter: expected.FromChapter,
			ToChapter: expected.ToChapter, ContentSignature: expected.ContentSignature, Passed: true,
		})
	}
	return evidence
}

func TestNormalRevisionServicePersistsPreviewAtEveryManuscriptStage(t *testing.T) {
	for _, stage := range []domain.ManuscriptStage{
		domain.ManuscriptStageProposalComplete,
		domain.ManuscriptStageOutlineComplete,
		domain.ManuscriptStageWriting,
		domain.ManuscriptStageComplete,
	} {
		t.Run(string(stage), func(t *testing.T) {
			st := newPublicationTestStore(t)
			if err := st.Init(); err != nil {
				t.Fatal(err)
			}
			vol := domain.LegacyStructureID("all-stages", domain.StructureKindVolume, "volume")
			arc := domain.LegacyStructureID("all-stages", domain.StructureKindArc, "arc")
			ch1 := domain.LegacyStructureID("all-stages", domain.StructureKindChapter, "chapter-1")
			ch2 := domain.LegacyStructureID("all-stages", domain.StructureKindChapter, "chapter-2")
			current := []domain.VolumeOutline{{ID: vol, Index: 1, Title: "Volume", Theme: "change", Arcs: []domain.ArcOutline{{ID: arc, Index: 1, Title: "Arc", Goal: "choose", Chapters: []domain.OutlineEntry{{ID: ch1, Chapter: 1, Title: "One", CoreEvent: "choice begins", Hook: "a consequence", Scenes: []string{"the choice"}}}}}}}
			candidate := domain.CloneStructureSnapshot(current)
			candidate[0].Arcs[0].Chapters = append(candidate[0].Arcs[0].Chapters, domain.OutlineEntry{ID: ch2, Chapter: 2, Title: "Two", CoreEvent: "consequence lands", Hook: "a new direction", Scenes: []string{"the consequence"}})
			if err := st.Outline.SaveLayeredOutline(current); err != nil {
				t.Fatal(err)
			}
			if err := st.Progress.Init("stage", 1); err != nil {
				t.Fatal(err)
			}
			if err := st.Progress.UpdatePhase(domain.PhaseOutline); err != nil {
				t.Fatal(err)
			}
			switch stage {
			case domain.ManuscriptStageProposalComplete:
				if err := st.RunMeta.SetPlanningReview(&domain.PlanningReview{Status: domain.PlanningReviewStatusPending, Kind: domain.PlanningReviewKindVolumeSplit, Brief: "proposal"}); err != nil {
					t.Fatal(err)
				}
			case domain.ManuscriptStageOutlineComplete:
				if err := st.RunMeta.SetPlanningReview(&domain.PlanningReview{Status: domain.PlanningReviewStatusPending, Kind: domain.PlanningReviewKindChapterOutline, Brief: "outline"}); err != nil {
					t.Fatal(err)
				}
			case domain.ManuscriptStageWriting, domain.ManuscriptStageComplete:
				if err := st.Progress.UpdatePhase(domain.PhaseWriting); err != nil {
					t.Fatal(err)
				}
				if stage == domain.ManuscriptStageComplete {
					if err := st.Progress.UpdatePhase(domain.PhaseComplete); err != nil {
						t.Fatal(err)
					}
				}
			}
			budget, err := domain.NewDynamicSoftBudget(2, 3000, 5000)
			if err != nil {
				t.Fatal(err)
			}
			planner := &fakeNormalStructurePlanner{proposal: domain.StructureRevisionProposal{
				Assessment: domain.ContentAdditionAssessment{NeedsAdditionalChapters: true, Reason: "one causal consequence needs its own chapter"},
				Candidate:  candidate, SoftBudget: budget,
			}}
			previewed, err := NewNormalRevisionService(st).Preview(context.Background(), planner, domain.StructureRevisionRequest{
				Operation: domain.StructureRevisionAppendChapter, Intent: "add the consequence", Stage: stage,
				BaseRevision: 1, Current: current, CurrentSoftBudget: &budget,
			}, "preview-"+string(stage))
			if err != nil {
				t.Fatal(err)
			}
			active, err := st.Revisions.Active()
			if err != nil || active == nil || active.ID != previewed.Session.ID || active.Stage != domain.RevisionStageImpactReviewPending {
				t.Fatalf("persistent %s session=%+v err=%v", stage, active, err)
			}
		})
	}
}

func TestNormalRevisionServiceProposalCompleteAcceptsSkeletonAndCreatesNoDetails(t *testing.T) {
	st := newPublicationTestStore(t)
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	volExisting := domain.LegacyStructureID("proposal-skeleton", domain.StructureKindVolume, "existing")
	arcExisting := domain.LegacyStructureID("proposal-skeleton", domain.StructureKindArc, "existing")
	volNew := domain.LegacyStructureID("proposal-skeleton", domain.StructureKindVolume, "new")
	arcNew := domain.LegacyStructureID("proposal-skeleton", domain.StructureKindArc, "new")
	current := []domain.VolumeOutline{{
		ID: volExisting, Index: 1, Title: "Existing", Theme: "opening",
		Arcs: []domain.ArcOutline{{ID: arcExisting, Index: 1, Title: "Opening", Goal: "begin", EstimatedChapters: 2}},
	}}
	if err := st.Outline.SaveLayeredOutline(current); err != nil {
		t.Fatal(err)
	}
	if err := st.Progress.Init("skeleton", 2); err != nil {
		t.Fatal(err)
	}
	if err := st.Progress.UpdatePhase(domain.PhaseOutline); err != nil {
		t.Fatal(err)
	}
	if err := st.RunMeta.SetPlanningReview(&domain.PlanningReview{Status: domain.PlanningReviewStatusPending, Kind: domain.PlanningReviewKindVolumeSplit, Brief: "proposal"}); err != nil {
		t.Fatal(err)
	}
	candidate := append(domain.CloneStructureSnapshot(current), domain.VolumeOutline{
		ID: volNew, Index: 2, Title: "New", Theme: "new phase",
		Arcs: []domain.ArcOutline{{ID: arcNew, Index: 1, Title: "New arc", Goal: "pay cost", EstimatedChapters: 2}},
	})
	budget, _ := domain.NewDynamicSoftBudget(4, 3000, 5000)
	previewed, err := NewNormalRevisionService(st).Preview(context.Background(), &fakeNormalStructurePlanner{proposal: domain.StructureRevisionProposal{
		Assessment: domain.ContentAdditionAssessment{NeedsAdditionalChapters: true, Reason: "new phase", NewVolume: &domain.DramaticStageEvidence{
			EntryState: "old phase closed", IndependentConflict: "new conflict", ArcProgression: "rises", Climax: "choice",
			IrreversibleOutcome: "new order", CannotFitCurrentVolume: "old phase closed",
		}}, Candidate: candidate, SoftBudget: budget,
	}}, domain.StructureRevisionRequest{
		Operation: domain.StructureRevisionAppendVolume, Intent: "add new phase", Stage: domain.ManuscriptStageProposalComplete,
		BaseRevision: 1, Current: current, CurrentSoftBudget: &budget,
	}, "proposal-skeleton")
	if err != nil {
		t.Fatal(err)
	}
	newArc := previewed.Preview.Proposal.Candidate[1].Arcs[0]
	if len(newArc.Chapters) != 2 {
		t.Fatalf("stable skeleton slots=%d, want 2", len(newArc.Chapters))
	}
	for _, slot := range newArc.Chapters {
		if slot.ID == "" || slot.Chapter <= 0 || slot.Title != "" || slot.CoreEvent != "" || slot.Hook != "" || len(slot.Scenes) != 0 {
			t.Fatalf("proposal skeleton generated details: %+v", slot)
		}
	}
}

func TestNormalRevisionWritingPersistsOnlyExactProseReworkIntents(t *testing.T) {
	st := newPublicationTestStore(t)
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	vol := domain.LegacyStructureID("writing-intent", domain.StructureKindVolume, "vol")
	arc := domain.LegacyStructureID("writing-intent", domain.StructureKindArc, "arc")
	chapterID := domain.LegacyStructureID("writing-intent", domain.StructureKindChapter, "ch-1")
	current := []domain.VolumeOutline{{ID: vol, Index: 1, Arcs: []domain.ArcOutline{{ID: arc, Index: 1, Chapters: []domain.OutlineEntry{{ID: chapterID, Chapter: 1, Title: "Old", CoreEvent: "old event", Hook: "old hook", Scenes: []string{"old"}}}}}}}
	candidate := domain.CloneStructureSnapshot(current)
	candidate[0].Arcs[0].Chapters[0] = domain.OutlineEntry{ID: chapterID, Chapter: 1, Title: "New", CoreEvent: "new event", Hook: "new hook", Scenes: []string{"new"}}
	if err := st.Outline.SaveLayeredOutline(current); err != nil {
		t.Fatal(err)
	}
	if err := st.Progress.Save(&domain.Progress{Phase: domain.PhaseWriting, Layered: true, TotalChapters: 1, CompletedChapters: []int{1}}); err != nil {
		t.Fatal(err)
	}
	budget, _ := domain.NewDynamicSoftBudget(1, 3000, 5000)
	service := NewNormalRevisionService(st)
	previewed, err := service.Preview(context.Background(), &fakeNormalStructurePlanner{proposal: domain.StructureRevisionProposal{
		Assessment: domain.ContentAdditionAssessment{Reason: "rewrite the completed chapter"}, Candidate: candidate, SoftBudget: budget,
	}}, domain.StructureRevisionRequest{
		Operation: domain.StructureRevisionExpandChapter, Intent: "change the reversal", Stage: domain.ManuscriptStageWriting,
		TargetID: chapterID, BaseRevision: 1, Current: current, CompletedChapterIDs: []string{chapterID}, CurrentSoftBudget: &budget,
	}, "writing-preview")
	if err != nil {
		t.Fatal(err)
	}
	session, err := service.ApproveImpact(previewed.Session.ID, previewed.Session.Revision, "writing-impact")
	if err != nil {
		t.Fatal(err)
	}
	session, err = service.SubmitDetailedOutlineCandidate(candidate, session, "writing-details")
	if err != nil {
		t.Fatal(err)
	}
	session, err = service.RecordAuditSet(session, passedNormalAuditExpectations(session), "writing-detail-audit")
	if err != nil {
		t.Fatal(err)
	}
	session, err = service.ApproveStage(session, "writing-detail-approve")
	if err != nil {
		t.Fatal(err)
	}
	session, err = service.SubmitProseReworkCandidate(session, "writing-intents")
	if err != nil {
		t.Fatal(err)
	}
	if len(session.CandidateVersionIDs) != 2 {
		t.Fatalf("prose candidate versions=%v, want one intent plus one queue", session.CandidateVersionIDs)
	}
	for _, versionID := range session.CandidateVersionIDs {
		version, loadErr := st.Revisions.LoadVersion(versionID)
		if loadErr != nil {
			t.Fatal(loadErr)
		}
		if strings.Contains(strings.ToLower(string(version.Payload)), `"body"`) || strings.Contains(strings.ToLower(string(version.Payload)), `"content"`) {
			t.Fatalf("PR-04 generated prose instead of intent/queue: %s", version.Payload)
		}
	}
}

func TestNormalRevisionRejectsUnauditedSiblingDetailInjection(t *testing.T) {
	st := newPublicationTestStore(t)
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	volumeID := domain.LegacyStructureID("detail-injection", domain.StructureKindVolume, "vol")
	arcID := domain.LegacyStructureID("detail-injection", domain.StructureKindArc, "arc")
	affectedID := domain.LegacyStructureID("detail-injection", domain.StructureKindChapter, "affected")
	siblingID := domain.LegacyStructureID("detail-injection", domain.StructureKindChapter, "sibling")
	current := []domain.VolumeOutline{{
		ID: volumeID, Index: 1, Title: "Volume", Theme: "theme",
		Arcs: []domain.ArcOutline{{
			ID: arcID, Index: 1, Title: "Arc", Goal: "goal",
			Chapters: []domain.OutlineEntry{
				{ID: affectedID, Chapter: 1, Title: "Affected old", CoreEvent: "old event", Hook: "old hook", Scenes: []string{"old scene"}},
				{ID: siblingID, Chapter: 2, Title: "Sibling original", CoreEvent: "sibling event", Hook: "sibling hook", Scenes: []string{"sibling scene"}},
			},
		}},
	}}
	if err := st.Outline.SaveLayeredOutline(current); err != nil {
		t.Fatal(err)
	}
	if err := st.Progress.Save(&domain.Progress{Phase: domain.PhaseWriting, Layered: true, TotalChapters: 2, CompletedChapters: []int{1, 2}}); err != nil {
		t.Fatal(err)
	}
	approvedDetails := domain.CloneStructureSnapshot(current)
	approvedDetails[0].Arcs[0].Chapters[0] = domain.OutlineEntry{
		ID: affectedID, Chapter: 1, Title: "Affected revised", CoreEvent: "revised event", Hook: "revised hook", Scenes: []string{"revised scene"},
	}
	budget, err := domain.NewDynamicSoftBudget(2, 3000, 5000)
	if err != nil {
		t.Fatal(err)
	}
	service := NewNormalRevisionService(st)
	previewed, err := service.Preview(context.Background(), &fakeNormalStructurePlanner{proposal: domain.StructureRevisionProposal{
		Assessment: domain.ContentAdditionAssessment{Reason: "revise only the affected chapter"}, Candidate: approvedDetails, SoftBudget: budget,
	}}, domain.StructureRevisionRequest{
		Operation: domain.StructureRevisionExpandChapter, Intent: "revise the affected chapter", Stage: domain.ManuscriptStageWriting,
		TargetID: affectedID, BaseRevision: 1, Current: current, CompletedChapterIDs: []string{affectedID, siblingID}, CurrentSoftBudget: &budget,
	}, "detail-injection-preview")
	if err != nil {
		t.Fatal(err)
	}
	session, err := service.ApproveImpact(previewed.Session.ID, previewed.Session.Revision, "detail-injection-impact")
	if err != nil {
		t.Fatal(err)
	}
	injected := domain.CloneStructureSnapshot(approvedDetails)
	injected[0].Arcs[0].Chapters[1] = domain.OutlineEntry{
		ID: siblingID, Chapter: 2, Title: "Sibling injected", CoreEvent: "unaudited event", Hook: "unaudited hook", Scenes: []string{"unaudited scene"},
	}
	if _, err := service.SubmitDetailedOutlineCandidate(injected, session, "detail-injection-candidate"); err == nil || !strings.Contains(err.Error(), "non-impacted chapter") {
		t.Fatalf("unaudited sibling injection error=%v, want non-impacted rejection", err)
	}
	formal, err := st.Outline.LoadLayeredOutline()
	if err != nil {
		t.Fatal(err)
	}
	if len(formal) != 1 || len(formal[0].Arcs) != 1 || len(formal[0].Arcs[0].Chapters) != 2 ||
		!normalOutlineContentEqual(formal[0].Arcs[0].Chapters[0], current[0].Arcs[0].Chapters[0]) ||
		!normalOutlineContentEqual(formal[0].Arcs[0].Chapters[1], current[0].Arcs[0].Chapters[1]) {
		t.Fatalf("rejected sibling injection changed formal outline: %+v", formal)
	}
}
