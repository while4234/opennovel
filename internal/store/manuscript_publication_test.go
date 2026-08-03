package store

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
)

func TestManuscriptPublicationRecoversEveryDurableBoundary(t *testing.T) {
	for _, point := range []string{"prepared", "formal_applied", "completed", "runtime_completed"} {
		t.Run(point, func(t *testing.T) {
			root := t.TempDir()
			st, runtime := publicationFixture(t, root)
			st.manuscriptPublicationFailpoint = func(current string) error {
				if current == point {
					return errors.New("stop at " + point)
				}
				return nil
			}
			if _, err := st.PublishManuscriptCandidate(runtime, "publish-"+point); err == nil {
				t.Fatalf("publication unexpectedly crossed %s", point)
			}
			reopened := NewStore(root)
			prose, err := reopened.Drafts.LoadChapterText(1)
			if err != nil {
				t.Fatalf("LoadChapterText: %v", err)
			}
			active, err := reopened.ManuscriptRevisions.Active()
			if err != nil {
				t.Fatalf("Active: %v", err)
			}
			if point == "completed" || point == "runtime_completed" {
				if prose != "new prose" || active != nil {
					t.Fatalf("roll-forward at %s: prose=%q active=%+v", point, prose, active)
				}
			} else {
				if prose != "old prose" || active == nil || active.Stage != "ready_to_publish" {
					t.Fatalf("rollback at %s: prose=%q active=%+v", point, prose, active)
				}
			}
			if journal, err := reopened.loadManuscriptPublicationJournal(); err != nil || journal != nil {
				t.Fatalf("journal after recovery = %+v err=%v", journal, err)
			}
		})
	}
}

func TestManuscriptPublicationFormalAppliedFailureRollsBackBeforeReturning(t *testing.T) {
	st, runtime := publicationFixture(t, t.TempDir())
	st.manuscriptPublicationFailpoint = func(point string) error {
		if point == "formal_applied" {
			return errors.New("stop after formal apply")
		}
		return nil
	}
	if _, err := st.PublishManuscriptCandidate(runtime, "same-process-rollback"); err == nil {
		t.Fatal("expected publication failure")
	}
	if prose, err := st.Drafts.LoadChapterText(1); err != nil || prose != "old prose" {
		t.Fatalf("same-process formal state was not rolled back: prose=%q err=%v", prose, err)
	}
	active, err := st.ManuscriptRevisions.Active()
	if err != nil || active == nil || active.Stage != "ready_to_publish" {
		t.Fatalf("same-process runtime after rollback = %+v err=%v", active, err)
	}
	if journal, err := st.loadManuscriptPublicationJournal(); err != nil || journal != nil {
		t.Fatalf("journal after same-process rollback = %+v err=%v", journal, err)
	}
}

func TestManuscriptOwnershipBlocksLegacyFormalWritesAndNormalFlow(t *testing.T) {
	st, runtime := publicationFixtureBeforeStart(t, t.TempDir())
	started, err := st.ManuscriptRevisions.Start(runtime, "ownership-start")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := st.Outline.SaveOutline([]domain.OutlineEntry{{ID: started.Baseline.ChapterID, Chapter: 1, Title: "forbidden"}}); !errors.Is(err, ErrActiveRevisionBlocksNormalFlow) {
		t.Fatalf("legacy outline write err=%v", err)
	}
	if _, err := st.Revisions.AcquireNormalFlow("continuation"); !errors.Is(err, ErrActiveRevisionBlocksNormalFlow) {
		t.Fatalf("AcquireNormalFlow err=%v", err)
	}
}

func TestLegacyNormalFlowBlocksManuscriptStart(t *testing.T) {
	st, runtime := publicationFixtureBeforeStart(t, t.TempDir())
	lease, err := st.Revisions.AcquireNormalFlow("legacy-writing")
	if err != nil {
		t.Fatalf("AcquireNormalFlow: %v", err)
	}
	defer st.Revisions.ReleaseNormalFlow(lease.Token)
	if _, err := st.ManuscriptRevisions.Start(runtime, "blocked-start"); !errors.Is(err, ErrRevisionCommandInProgress) {
		t.Fatalf("manuscript start err=%v", err)
	}
}

func publicationFixture(t *testing.T, root string) (*Store, *domain.ManuscriptRevisionRuntime) {
	t.Helper()
	st, runtime := publicationFixtureBeforeStart(t, root)
	started, err := st.ManuscriptRevisions.Start(runtime, "fixture-start")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	ready, err := st.ManuscriptRevisions.Mutate(started.RevisionID, started.Revision, "fixture-ready", "fixture_ready", started.RevisionID, func(current *domain.ManuscriptRevisionRuntime) error {
		current.Stage = "ready_to_publish"
		current.Candidates = runtime.Candidates
		return nil
	})
	if err != nil {
		t.Fatalf("Mutate ready: %v", err)
	}
	return st, ready
}

func publicationFixtureBeforeStart(t *testing.T, root string) (*Store, domain.ManuscriptRevisionRuntime) {
	t.Helper()
	st := NewStore(root)
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	chapterID := "ch_abcdefabcdefabcdefabcdefabcdefab"
	entry := domain.OutlineEntry{ID: chapterID, Chapter: 1, Title: "one", CoreEvent: "event", Hook: "hook"}
	if err := st.Outline.SaveOutline([]domain.OutlineEntry{entry}); err != nil {
		t.Fatalf("SaveOutline: %v", err)
	}
	if err := st.Progress.Init("book", 1); err != nil {
		t.Fatalf("Progress.Init: %v", err)
	}
	if err := st.Progress.UpdatePhase(domain.PhaseWriting); err != nil {
		t.Fatalf("UpdatePhase: %v", err)
	}
	if err := st.Drafts.SaveFinalChapter(1, "old prose"); err != nil {
		t.Fatalf("SaveFinalChapter: %v", err)
	}
	if err := st.Summaries.SaveSummary(domain.ChapterSummary{Chapter: 1, Summary: "old summary"}); err != nil {
		t.Fatalf("SaveSummary: %v", err)
	}
	prose, _ := st.ManuscriptRevisions.Content().PutMarkdown("new prose")
	summary, _ := st.ManuscriptRevisions.Content().PutJSON(domain.ChapterSummary{Chapter: 1, Summary: "new summary"})
	events, _ := st.ManuscriptRevisions.Content().PutJSON([]string{"event"})
	timeline, _ := st.ManuscriptRevisions.Content().PutJSON([]domain.TimelineEvent{{Event: "event"}})
	states, _ := st.ManuscriptRevisions.Content().PutJSON([]domain.StateChange{{Entity: "hero", Field: "status", NewValue: "changed"}})
	relationships, _ := st.ManuscriptRevisions.Content().PutJSON([]domain.RelationshipEntry{{CharacterA: "hero", CharacterB: "ally", Relation: "trusted"}})
	foreshadow, _ := st.ManuscriptRevisions.Content().PutJSON([]domain.ForeshadowEntry{{ID: "seed", Description: "seed", Status: "planted"}})
	world, _ := st.ManuscriptRevisions.Content().PutJSON([]domain.WorldRule{{Category: "other", Rule: "rule", Boundary: "boundary"}})
	carry, _ := st.ManuscriptRevisions.Content().PutJSON(manuscriptCarryForward{CharacterSnapshots: []domain.CharacterSnapshot{{Name: "hero", Status: "ready", Motivation: "act"}}})
	contractEvidence, _ := st.ManuscriptRevisions.Content().PutJSON(map[string]any{"version": 1, "source": "server_fixture"})
	contract := domain.NarrativeContract{ChapterID: chapterID, OutlineSHA256: domain.ContentSignature([]byte("outline")), Desire: "d", Obstacle: "o", Choice: "c", Cost: "cost", Result: "r", ExitState: "e", StateSHA256: domain.ContentSignature([]byte("state"))}
	baseline := domain.ManuscriptBaseline{ChapterID: chapterID, DisplayChapter: 1, CurrentProseSHA256: domain.ContentSignature([]byte("old prose")), ApprovedOutlineSHA256: domain.ContentSignature([]byte("outline")), StructureSignature: domain.ContentSignature([]byte("structure")), NarrativeContract: contract, Mode: domain.RevisionModeNormal}
	baseline.ContractArtifact = domain.NewNarrativeContractArtifact(contract, baseline.CurrentProseSHA256, baseline.ApprovedOutlineSHA256)
	candidate := domain.ManuscriptCandidate{ChapterID: chapterID, DisplayChapter: 1, Prose: prose, Sidecar: domain.ManuscriptSidecar{Summary: summary, Events: events, Timeline: timeline, CastState: states, Relationships: relationships, Foreshadow: foreshadow, WorldFacts: world, CarryForward: carry}, BaselineSignature: "baseline", ContractSignature: "contract", ContractEvidence: contractEvidence, OutlineSignature: baseline.ApprovedOutlineSHA256, ModeSignature: "mode"}
	candidate.ContractArtifact = domain.NewNarrativeContractArtifact(contract, prose.SHA256, candidate.OutlineSignature)
	candidatePayload, _ := json.Marshal(candidate)
	candidateSignature := domain.ContentSignature(candidatePayload)
	auditTask, _ := st.ManuscriptRevisions.Content().PutJSON(map[string]string{"candidate_signature": candidateSignature})
	auditReport, _ := st.ManuscriptRevisions.Content().PutMarkdown("passed")
	auditFindings, _ := st.ManuscriptRevisions.Content().PutJSON([]string{"passed"})
	auditReceipt, _ := st.ManuscriptRevisions.Content().PutJSON(map[string]any{"passed": true})
	auditArtifact := domain.NewManuscriptAuditArtifact(candidateSignature, auditTask, auditReport, auditFindings, auditReceipt, []string{candidate.Prose.SHA256})
	candidate.AuditArtifact = &auditArtifact
	candidate.AuditSignature = candidateSignature
	runtime := domain.ManuscriptRevisionRuntime{Version: 1, RevisionID: "msr_publication", Revision: 1, Mode: domain.RevisionModeNormal, PolicyID: domain.NormalManuscriptRevisionPolicyID, PolicyVersion: domain.ManuscriptRevisionPolicyVersion, Instruction: "rewrite", InstructionKind: domain.ManuscriptInstructionRewrite, Stage: "approval_pending", Baseline: baseline, Queue: []domain.ManuscriptReworkItem{{ChapterID: chapterID, DisplayChapter: 1, Requirement: domain.StructureImpactRequired, ExpectedSignatures: []string{baseline.CurrentProseSHA256}, Status: "generated", IdempotencyKey: "item"}}, Candidates: []domain.ManuscriptCandidate{candidate}, PublicationStatus: domain.ManuscriptPublicationNone, UpdatedAt: "now"}
	return st, runtime
}
