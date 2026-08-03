package store

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/voocel/ainovel-cli/internal/adaptaudit"
	"github.com/voocel/ainovel-cli/internal/domain"
)

func TestAdaptationRevisionPolicySessionResumesAfterStoreRestart(t *testing.T) {
	dir := t.TempDir()
	policy := domain.AdaptationRevisionPolicy{Stage: domain.ManuscriptStageWriting}
	impact, err := domain.NewRevisionImpact("adaptation writing insertion", []domain.RevisionImpactItem{
		{
			ArtifactID: "target-2", ArtifactKind: domain.StructureKindChapter, Change: "insert target chapter",
			Requirement: domain.StructureImpactRequired, Cause: domain.StructureImpactContentDependency,
			DependencyEvidence: []string{"stable target insertion"},
		},
		{
			ArtifactID: domain.AdaptationRevisionBatchPlanID, ArtifactKind: domain.AdaptationRevisionArtifactBatchPlan,
			Change: "bounded source-local work", Requirement: domain.StructureImpactRequired, Cause: domain.StructureImpactContentDependency,
			DependencyEvidence: []string{"BatchPlan boundary"},
		},
		{
			ArtifactID: domain.AdaptationRevisionPlanSnapshotID, ArtifactKind: domain.AdaptationRevisionArtifactPlanSnapshot,
			Change: "bind source contract", Requirement: domain.StructureImpactRequired, Cause: domain.StructureImpactContentDependency,
			DependencyEvidence: []string{"immutable source signature"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	started, err := NewRevisionStore(dir).Start(policy, StartRevisionInput{
		Intent: "insert chapter during writing", Impact: impact, IdempotencyKey: "adaptation-start",
	})
	if err != nil {
		t.Fatal(err)
	}
	approved, err := NewRevisionStore(dir).ApproveImpact(policy, RevisionMutationInput{
		SessionID: started.ID, ExpectedRevision: started.Revision, IdempotencyKey: "adaptation-impact",
	})
	if err != nil {
		t.Fatal(err)
	}
	paused, err := NewRevisionStore(dir).Pause(policy, RevisionMutationInput{
		SessionID: approved.ID, ExpectedRevision: approved.Revision, IdempotencyKey: "adaptation-pause",
	})
	if err != nil {
		t.Fatal(err)
	}
	resumed, err := NewRevisionStore(dir).Resume(policy, RevisionMutationInput{
		SessionID: paused.ID, ExpectedRevision: paused.Revision, IdempotencyKey: "adaptation-resume",
	})
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Stage != domain.RevisionStageCandidateGenerating || resumed.ResumeStage != "" || resumed.Generation <= started.Generation {
		t.Fatalf("resumed adaptation revision lost durable position: %+v", resumed)
	}
}

func TestAdaptationRevisionStoreDestructivePathsFenceBeforeFirstWrite(t *testing.T) {
	st := setupLayered(t, []domain.VolumeOutline{{
		Index: 1, Title: "volume", Theme: "theme",
		Arcs: []domain.ArcOutline{{Index: 1, Title: "arc", Chapters: []domain.OutlineEntry{{Chapter: 1, Title: "one", CoreEvent: "event", Hook: "hook"}}}},
	}})
	plan := domain.AdaptationPlan{
		Granularity: domain.AdaptationGranularityArc, Status: domain.AdaptationPlanStatusConfirmed,
		RewritePolicy: domain.AdaptationRewriteFullRewrite,
		Volumes:       []domain.AdaptationVolumePlan{{Index: 1, Title: "volume", TargetFrom: 1, TargetTo: 1, SourceFrom: 1, SourceTo: 1}},
		Chapters: []domain.AdaptationChapterPlan{{
			OutlineEntry: domain.OutlineEntry{Chapter: 1, Title: "one", CoreEvent: "event", Hook: "hook"},
			Chapter:      1, Title: "one", SourceChapters: []int{1}, SourceRange: domain.SourceRange{From: 1, To: 1},
			TargetRunes: 1000, TargetMinRunes: 800, TargetMaxRunes: 1200,
		}},
	}
	if err := st.Adaptation.SavePlan(plan); err != nil {
		t.Fatal(err)
	}
	report := adaptaudit.Audit(adaptaudit.Input{Mode: adaptaudit.ModeFree, Scope: adaptaudit.Scope{TargetFrom: 1, TargetTo: 1}})
	if err := st.Adaptation.SaveAuditReport(report); err != nil {
		t.Fatal(err)
	}
	before, err := st.CaptureAdaptationFormalSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	session := startAdaptationStoreRevision(t, st)

	for name, mutate := range map[string]func() error{
		"reset":           st.Adaptation.Reset,
		"reset generated": st.Adaptation.ResetGenerated,
		"rollback migration": func() error {
			_, err := st.Rollback(domain.RollbackRequest{Confirm: true})
			return err
		},
		"repair outline": func() error {
			return st.RepairArcOutline(1, 1, []domain.OutlineEntry{{Chapter: 1, Title: "changed", CoreEvent: "changed", Hook: "changed"}})
		},
		"repair recovery": func() error {
			_, err := st.FindDuplicateOutlineRepairBatch(&domain.Progress{Layered: true, TotalChapters: 1})
			return err
		},
		"audit migration": func() error {
			_, err := st.Adaptation.ListAuditRuns()
			return err
		},
		"expand arc": func() error {
			return st.ExpandArc(1, 1, []domain.OutlineEntry{{Chapter: 1, Title: "changed", CoreEvent: "changed", Hook: "changed", Scenes: []string{"changed"}}})
		},
		"append volume": func() error {
			return st.AppendVolume(domain.VolumeOutline{
				Index: 2, Title: "two", Theme: "two",
				Arcs: []domain.ArcOutline{{
					Index: 1, Title: "arc",
					Chapters: []domain.OutlineEntry{{Title: "two", CoreEvent: "two", Hook: "two", Scenes: []string{"two"}}},
				}},
			})
		},
		"append skeleton volume": func() error {
			return st.AppendSkeletonVolume(domain.VolumeOutline{Index: 2, Title: "two", Theme: "two", Arcs: []domain.ArcOutline{{Index: 1, Title: "arc"}}})
		},
		"revise chapter outline": func() error {
			return st.ReviseChapterOutline(1, domain.OutlineEntry{Title: "changed", CoreEvent: "changed", Hook: "changed", Scenes: []string{"changed"}})
		},
		"audit clear without owner":    func() error { return st.ClearAdaptationRevisionAudits(st.Revisions, "wrong-session") },
		"formal restore without owner": func() error { return st.RestoreAdaptationFormalSnapshot(st.Revisions, before, "wrong-session") },
	} {
		t.Run(name, func(t *testing.T) {
			if err := mutate(); err == nil || !errors.Is(err, ErrRevisionCommandInProgress) &&
				!strings.Contains(err.Error(), "active revision") && !strings.Contains(err.Error(), "does not own") {
				t.Fatalf("destructive path was not owner-fenced: %v", err)
			}
			after, captureErr := st.CaptureAdaptationFormalSnapshot()
			if captureErr != nil {
				t.Fatal(captureErr)
			}
			if !reflect.DeepEqual(before, after) {
				t.Fatal("rejected destructive path changed the formal snapshot")
			}
		})
	}

	if err := st.WithPreparedAdaptationRevisionCommand("clear-audits", "publish", "clear-audits-fingerprint", func(owner *RevisionStore) error {
		return st.ClearAdaptationRevisionAudits(owner, session.ID)
	}); err != nil {
		t.Fatalf("active owner could not clear revision audits: %v", err)
	}
}

func TestLegacyMutationSerializesWithRevisionStart(t *testing.T) {
	st := NewStore(t.TempDir())
	entered := make(chan struct{})
	release := make(chan struct{})
	mutationDone := make(chan error, 1)
	go func() {
		mutationDone <- st.Revisions.withLegacyMutation("race probe", func() error {
			close(entered)
			<-release
			return nil
		})
	}()
	<-entered
	startDone := make(chan error, 1)
	go func() {
		_, err := st.Revisions.Start(domain.AdaptationRevisionPolicy{Stage: domain.ManuscriptStageWriting}, StartRevisionInput{
			Intent: "race", Impact: adaptationStoreFenceImpact(t), IdempotencyKey: "race-start",
		})
		startDone <- err
	}()
	select {
	case err := <-startDone:
		t.Fatalf("revision start bypassed the legacy write transaction: %v", err)
	default:
	}
	close(release)
	if err := <-mutationDone; err != nil {
		t.Fatal(err)
	}
	if err := <-startDone; err != nil {
		t.Fatal(err)
	}
}

func TestPreparedRevisionCommandRequiresFullOwnerCapability(t *testing.T) {
	dir := t.TempDir()
	st := NewStore(dir)
	revisions := st.Revisions
	owner, err := revisions.claimCommandFence("shared-key", "preview", "preview-fingerprint")
	if err != nil {
		t.Fatal(err)
	}
	policy := domain.AdaptationRevisionPolicy{Stage: domain.ManuscriptStageWriting}
	start := StartRevisionInput{Intent: "owned preview", Impact: adaptationStoreFenceImpact(t), IdempotencyKey: "owned-start"}

	if _, err := NewRevisionStore(dir).Start(policy, StartRevisionInput{
		Intent: "forged same-key preview", Impact: start.Impact, IdempotencyKey: "shared-key",
	}); !errors.Is(err, ErrRevisionCommandInProgress) {
		t.Fatalf("ordinary same-key start entered prepared command: %v", err)
	}
	started, err := owner.Start(policy, start)
	if err != nil {
		t.Fatalf("owned nested start was blocked: %v", err)
	}
	if replay, err := owner.Start(policy, start); err != nil || !reflect.DeepEqual(replay, started) {
		t.Fatalf("owned exact replay failed: replay=%+v err=%v", replay, err)
	}
	if _, err := NewRevisionStore(dir).Start(policy, start); !errors.Is(err, ErrRevisionCommandInProgress) {
		t.Fatalf("ordinary exact receipt replay bypassed prepared ownership: %v", err)
	}
	if _, err := NewRevisionStore(dir).Pause(policy, RevisionMutationInput{
		SessionID: started.ID, ExpectedRevision: started.Revision, IdempotencyKey: "shared-key",
	}); !errors.Is(err, ErrRevisionCommandInProgress) {
		t.Fatalf("ordinary different mutation forged prepared key: %v", err)
	}
	paused, err := owner.Pause(policy, RevisionMutationInput{
		SessionID: started.ID, ExpectedRevision: started.Revision, IdempotencyKey: "owned-pause",
	})
	if err != nil {
		t.Fatalf("owned nested mutation was blocked: %v", err)
	}
	if _, err := revisions.claimCommandFence("shared-key", "publish", "preview-fingerprint"); !errors.Is(err, ErrRevisionCommandInProgress) {
		t.Fatalf("operation metadata did not participate in ownership: %v", err)
	}
	if _, err := revisions.claimCommandFence("shared-key", "preview", "different-fingerprint"); !errors.Is(err, ErrRevisionCommandInProgress) {
		t.Fatalf("fingerprint metadata did not participate in ownership: %v", err)
	}
	if err := st.PrepareAdaptationRevisionCommand(owner, "shared-key", "cancel", "preview-fingerprint"); !errors.Is(err, ErrRevisionCommandInProgress) {
		t.Fatalf("scoped owner API accepted different operation metadata: %v", err)
	}
	if err := NewStore(t.TempDir()).PrepareAdaptationRevisionCommand(owner, "shared-key", "preview", "preview-fingerprint"); !errors.Is(err, ErrRevisionCommandInProgress) {
		t.Fatalf("scoped owner API accepted a capability from another project: %v", err)
	}
	if err := owner.releaseCommandFence(); err != nil {
		t.Fatal(err)
	}
	if _, err := NewRevisionStore(dir).Resume(policy, RevisionMutationInput{
		SessionID: paused.ID, ExpectedRevision: paused.Revision, IdempotencyKey: "successor-resume",
	}); err != nil {
		t.Fatalf("successor mutation remained fenced after owner cleanup: %v", err)
	}
}

func TestPreparedRevisionCommandOwnsServiceReceiptAndRuntimeWrites(t *testing.T) {
	dir := t.TempDir()
	st := NewStore(dir)
	session := startAdaptationStoreRevision(t, st)
	runtime := adaptationStoreRuntime(session.ID, false)
	if err := st.SaveAdaptationRevisionRuntime(st.Revisions, runtime); err != nil {
		t.Fatal(err)
	}
	competitor := NewStore(dir)
	otherProject := NewStore(t.TempDir())
	var staleOwner *RevisionStore

	err := st.WithPreparedAdaptationRevisionCommand("owned-durable-write", "pause", "pause-fingerprint", func(owner *RevisionStore) error {
		if token, decodeErr := hex.DecodeString(owner.commandOwner.Token); decodeErr != nil || len(token) != 32 {
			t.Fatalf("prepared owner capability is not 256 bits: token_bytes=%d err=%v", len(token), decodeErr)
		}
		if err := st.PrepareAdaptationRevisionCommand(owner, "owned-durable-write", "pause", "pause-fingerprint"); err != nil {
			return err
		}
		for name, forged := range map[string]error{
			"matching receipt": competitor.SaveAdaptationRevisionServiceReceipt(competitor.Revisions, "owned-durable-write", "pause", "pause-fingerprint", session),
			"different key":    st.SaveAdaptationRevisionServiceReceipt(owner, "different-key", "pause", "pause-fingerprint", session),
			"different operation": st.SaveAdaptationRevisionServiceReceipt(
				owner, "owned-durable-write", "publish", "pause-fingerprint", session,
			),
			"different fingerprint": st.SaveAdaptationRevisionServiceReceipt(
				owner, "owned-durable-write", "pause", "different-fingerprint", session,
			),
			"runtime save":  competitor.SaveAdaptationRevisionRuntime(competitor.Revisions, adaptationStoreRuntime(session.ID, true)),
			"runtime clear": competitor.ClearAdaptationRevisionRuntime(competitor.Revisions, session.ID),
			"cross project": st.SaveAdaptationRevisionServiceReceipt(otherProject.Revisions, "owned-durable-write", "pause", "pause-fingerprint", session),
		} {
			if !errors.Is(forged, ErrRevisionCommandInProgress) {
				t.Fatalf("%s bypassed prepared ownership: %v", name, forged)
			}
		}
		staleOwner = owner.withCommandOwner(&revisionCommandFence{
			Key: owner.commandOwner.Key, Operation: owner.commandOwner.Operation,
			Fingerprint: owner.commandOwner.Fingerprint, OwnerToken: strings.Repeat("0", 64),
		})
		if err := st.SaveAdaptationRevisionRuntime(staleOwner, adaptationStoreRuntime(session.ID, true)); !errors.Is(err, ErrRevisionCommandInProgress) {
			t.Fatalf("stale owner token changed runtime: %v", err)
		}
		ownedRuntime := adaptationStoreRuntime(session.ID, true)
		if err := st.SaveAdaptationRevisionRuntime(owner, ownedRuntime); err != nil {
			return err
		}
		if err := st.ClearAdaptationRevisionRuntime(owner, session.ID); err != nil {
			return err
		}
		if err := st.SaveAdaptationRevisionRuntime(owner, ownedRuntime); err != nil {
			return err
		}
		paused, err := owner.Pause(domain.AdaptationRevisionPolicy{Stage: domain.ManuscriptStageWriting}, RevisionMutationInput{
			SessionID: session.ID, ExpectedRevision: session.Revision, IdempotencyKey: "owned-durable-write",
		})
		if err != nil {
			return err
		}
		if err := st.SaveAdaptationRevisionServiceReceipt(owner, "owned-durable-write", "pause", "pause-fingerprint", paused); err != nil {
			return err
		}
		return st.CompleteAdaptationRevisionCommand(owner, "owned-durable-write", "pause", "pause-fingerprint")
	})
	if err != nil {
		t.Fatal(err)
	}
	persisted, err := st.Adaptation.LoadRevisionRuntime()
	if err != nil || persisted == nil || !persisted.Paused {
		t.Fatalf("owner runtime was not durable: runtime=%+v err=%v", persisted, err)
	}
	if err := st.SaveAdaptationRevisionRuntime(st.Revisions, adaptationStoreRuntime(session.ID, false)); err != nil {
		t.Fatalf("successor remained fenced after terminal receipt cleanup: %v", err)
	}
	if err := st.SaveAdaptationRevisionServiceReceipt(staleOwner, "owned-durable-write", "pause", "pause-fingerprint", session); !errors.Is(err, ErrRevisionCommandInProgress) {
		t.Fatalf("released owner token remained usable: %v", err)
	}
	if err := st.SaveAdaptationRevisionRuntime(staleOwner, adaptationStoreRuntime(session.ID, false)); !errors.Is(err, ErrRevisionCommandInProgress) {
		t.Fatalf("released owner token changed runtime: %v", err)
	}
}

func TestPreparedRevisionCommandOwnsEveryFormalWriteAndRollback(t *testing.T) {
	st := setupLayered(t, []domain.VolumeOutline{{
		Index: 1, Title: "volume", Theme: "theme",
		Arcs: []domain.ArcOutline{{Index: 1, Title: "arc", Chapters: []domain.OutlineEntry{{Chapter: 1, Title: "one", CoreEvent: "event", Hook: "hook"}}}},
	}})
	base := domain.AdaptationPlan{
		Granularity: domain.AdaptationGranularityArc, Status: domain.AdaptationPlanStatusConfirmed,
		RewritePolicy: domain.AdaptationRewriteFullRewrite,
		Volumes:       []domain.AdaptationVolumePlan{{Index: 1, Title: "volume", TargetFrom: 1, TargetTo: 1, SourceFrom: 1, SourceTo: 1}},
		Chapters: []domain.AdaptationChapterPlan{{
			OutlineEntry: domain.OutlineEntry{Chapter: 1, Title: "one", CoreEvent: "event", Hook: "hook"},
			Chapter:      1, Title: "one", SourceChapters: []int{1}, SourceRange: domain.SourceRange{From: 1, To: 1},
			TargetRunes: 1000, TargetMinRunes: 800, TargetMaxRunes: 1200,
		}},
	}
	if err := st.Adaptation.SavePlan(base); err != nil {
		t.Fatal(err)
	}
	report := adaptaudit.Audit(adaptaudit.Input{Mode: adaptaudit.ModeArc, Scope: adaptaudit.Scope{TargetFrom: 1, TargetTo: 1}})
	if err := st.Adaptation.SaveAuditReport(report); err != nil {
		t.Fatal(err)
	}
	if err := st.Progress.Save(&domain.Progress{Phase: domain.PhaseWriting, Layered: true, TotalChapters: 1}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := st.CaptureAdaptationFormalSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	session := startAdaptationStoreRevision(t, st)
	competitor := NewStore(st.Dir())
	otherProject := NewStore(t.TempDir())
	var staleOwner *RevisionStore
	if err := st.WithPreparedAdaptationRevisionCommand("stale-formal", "publish", "stale-formal-fingerprint", func(owner *RevisionStore) error {
		staleOwner = owner
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	changed := base
	changed.Chapters = append([]domain.AdaptationChapterPlan(nil), base.Chapters...)
	changed.Chapters[0].Title = "owner change"
	changed.Chapters[0].OutlineEntry.Title = "owner change"
	progress := &domain.Progress{Phase: domain.PhaseWriting, Layered: true, TotalChapters: 2}
	layered, err := st.Outline.LoadLayeredOutline()
	if err != nil {
		t.Fatal(err)
	}
	before := formalWriteProjectBytes(t, st.Dir())
	var currentOwner *RevisionStore
	err = st.WithPreparedAdaptationRevisionCommand("owned-formal", "formal", "owned-formal-fingerprint", func(owner *RevisionStore) error {
		currentOwner = owner
		if err := st.PrepareAdaptationRevisionCommand(owner, "owned-formal", "formal", "owned-formal-fingerprint"); err != nil {
			return err
		}
		forgedPublicationOwner := &RevisionPublicationOwner{
			revisions: competitor.Revisions, policy: domain.NormalRevisionPolicy{}, sessionID: session.ID,
			expectedRevision: session.Revision, mode: domain.RevisionModeNormal, policyID: domain.NormalRevisionPolicyID,
			policyVersion: domain.NormalRevisionPolicyVersion,
		}
		attempts := []struct {
			name string
			call func() error
		}{
			{"same-session plan save", func() error {
				return competitor.SaveAdaptationPlanForRevision(competitor.Revisions, changed, session.ID)
			}},
			{"same-session plan restore", func() error {
				return competitor.RestoreAdaptationPlanForRevision(competitor.Revisions, base, session.ID)
			}},
			{"same-session snapshot restore", func() error {
				return competitor.RestoreAdaptationFormalSnapshot(competitor.Revisions, snapshot, session.ID)
			}},
			{"same-session audit cleanup", func() error { return competitor.ClearAdaptationRevisionAudits(competitor.Revisions, session.ID) }},
			{"same-session progress write", func() error {
				return competitor.SaveAdaptationRevisionProgress(competitor.Revisions, progress, session.ID)
			}},
			{"cross-project plan save", func() error { return st.SaveAdaptationPlanForRevision(otherProject.Revisions, changed, session.ID) }},
			{"stale plan save", func() error { return st.SaveAdaptationPlanForRevision(staleOwner, changed, session.ID) }},
			{"layered publish", func() error {
				return st.PublishLayeredStructureForRevision(forgedPublicationOwner, layered, "forged-layered-publish")
			}},
			{"layered restore", func() error { return st.RestoreLayeredStructureForRevision(forgedPublicationOwner, layered, progress) }},
		}
		errs := make([]error, len(attempts))
		var wg sync.WaitGroup
		for index := range attempts {
			wg.Add(1)
			go func(index int) {
				defer wg.Done()
				errs[index] = attempts[index].call()
			}(index)
		}
		wg.Wait()
		for index, attempt := range attempts {
			if !errors.Is(errs[index], ErrRevisionCommandInProgress) {
				t.Fatalf("%s bypassed exact prepared ownership: %v", attempt.name, errs[index])
			}
		}
		if after := formalWriteProjectBytes(t, st.Dir()); !reflect.DeepEqual(before, after) {
			t.Fatal("rejected prepared-command races changed formal bytes")
		}
		if err := st.SaveAdaptationPlanForRevision(owner, changed, session.ID); err != nil {
			return err
		}
		if err := st.SaveAdaptationRevisionProgress(owner, progress, session.ID); err != nil {
			return err
		}
		if err := st.ClearAdaptationRevisionAudits(owner, session.ID); err != nil {
			return err
		}
		if err := st.RestoreAdaptationPlanForRevision(owner, base, session.ID); err != nil {
			return err
		}
		if err := st.RestoreAdaptationFormalSnapshot(owner, snapshot, session.ID); err != nil {
			return err
		}
		return st.RollbackAdaptationRevisionCommand(owner)
	})
	if err != nil {
		t.Fatal(err)
	}
	if after := formalWriteProjectBytes(t, st.Dir()); !reflect.DeepEqual(before, after) {
		t.Fatal("genuine owner rollback did not restore byte-identical formal state")
	}
	if err := st.SaveAdaptationPlanForRevision(currentOwner, changed, session.ID); !errors.Is(err, ErrRevisionCommandInProgress) {
		t.Fatalf("released formal owner remained usable: %v", err)
	}
	if after := formalWriteProjectBytes(t, st.Dir()); !reflect.DeepEqual(before, after) {
		t.Fatal("stale owner changed formal bytes after release")
	}
}

func TestPreparedRevisionRuntimeCrashRecoveryRestoresExactCheckpoint(t *testing.T) {
	dir := t.TempDir()
	st := NewStore(dir)
	session := startAdaptationStoreRevision(t, st)
	before := adaptationStoreRuntime(session.ID, false)
	if err := st.SaveAdaptationRevisionRuntime(st.Revisions, before); err != nil {
		t.Fatal(err)
	}

	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("prepared command did not simulate a process crash")
			}
		}()
		_ = st.WithPreparedAdaptationRevisionCommand("runtime-crash", "pause", "pause-fingerprint", func(owner *RevisionStore) error {
			if err := st.PrepareAdaptationRevisionCommand(owner, "runtime-crash", "pause", "pause-fingerprint"); err != nil {
				return err
			}
			if err := st.SaveAdaptationRevisionRuntime(owner, adaptationStoreRuntime(session.ID, true)); err != nil {
				return err
			}
			panic("simulated crash after runtime write")
		})
	}()

	restarted := NewStore(dir)
	if restarted.commandRecoveryErr != nil {
		t.Fatalf("restart recovery failed: %v", restarted.commandRecoveryErr)
	}
	after, err := restarted.Adaptation.LoadRevisionRuntime()
	if err != nil || !reflect.DeepEqual(after, &before) {
		t.Fatalf("restart did not restore exact runtime: got=%+v want=%+v err=%v", after, before, err)
	}
	if found, err := restarted.Adaptation.HasRevisionServiceReceipt("runtime-crash", "pause", "pause-fingerprint"); err != nil || found {
		t.Fatalf("crash recovery retained a forged terminal marker: found=%v err=%v", found, err)
	}
	if err := restarted.SaveAdaptationRevisionRuntime(restarted.Revisions, adaptationStoreRuntime(session.ID, true)); err != nil {
		t.Fatalf("restart recovery did not release successor writes: %v", err)
	}
}

func TestAdaptationRevisionV1PreparedJournalUpgradeRollbackAndReceiptCleanup(t *testing.T) {
	for _, operation := range []string{"preview", "save", "complete", "cancel"} {
		t.Run(operation+" rollback", func(t *testing.T) {
			dir := t.TempDir()
			st := NewStore(dir)
			session := startAdaptationStoreRevision(t, st)
			before := adaptationStoreRuntime(session.ID, false)
			if err := st.SaveAdaptationRevisionRuntime(st.Revisions, before); err != nil {
				t.Fatal(err)
			}
			crashPreparedAdaptationCommand(t, st, "v1-"+operation, operation, operation+"-fingerprint", func(owner *RevisionStore) error {
				if err := st.SaveAdaptationRevisionRuntime(owner, adaptationStoreRuntime(session.ID, true)); err != nil {
					return err
				}
				return rewriteAdaptationCommandJournalAsV1(st.dir)
			})
			restarted := NewStore(dir)
			if restarted.commandRecoveryErr != nil {
				t.Fatalf("v1 %s startup rollback failed: %v", operation, restarted.commandRecoveryErr)
			}
			after, err := restarted.Adaptation.LoadRevisionRuntime()
			if err != nil || !reflect.DeepEqual(after, &before) {
				t.Fatalf("v1 %s rollback runtime=%+v want=%+v err=%v", operation, after, before, err)
			}
		})
	}

	t.Run("pause receipt cleanup", func(t *testing.T) {
		dir := t.TempDir()
		st := NewStore(dir)
		session := startAdaptationStoreRevision(t, st)
		key, operation, fingerprint := "v1-receipt-pause", "pause", "pause-fingerprint"
		crashPreparedAdaptationCommand(t, st, key, operation, fingerprint, func(owner *RevisionStore) error {
			paused, err := owner.Pause(domain.AdaptationRevisionPolicy{Stage: domain.ManuscriptStageWriting}, RevisionMutationInput{
				SessionID: session.ID, ExpectedRevision: session.Revision, IdempotencyKey: key,
			})
			if err != nil {
				return err
			}
			if err := st.SaveAdaptationRevisionServiceReceipt(owner, key, operation, fingerprint, paused); err != nil {
				return err
			}
			return rewriteAdaptationCommandJournalAsV1(st.dir)
		})
		restarted := NewStore(dir)
		if restarted.commandRecoveryErr != nil {
			t.Fatalf("v1 pause receipt cleanup failed: %v", restarted.commandRecoveryErr)
		}
		if pending, err := restarted.adaptationRevisionCommandPending(); err != nil || pending {
			t.Fatalf("v1 pause receipt cleanup retained prepared evidence: pending=%v err=%v", pending, err)
		}
	})
}

func TestAdaptationRevisionNonPublishReceiptStrictRecoveryMatrix(t *testing.T) {
	mutations := []struct {
		name   string
		mutate func([]byte) []byte
	}{
		{name: "empty object", mutate: func(data []byte) []byte { return replaceAdaptationServiceResultForTest(t, data, map[string]any{}) }},
		{name: "scalar", mutate: func(data []byte) []byte { return replaceAdaptationServiceResultForTest(t, data, true) }},
		{name: "partial", mutate: func(data []byte) []byte {
			return mutateAdaptationServiceResultForTest(t, data, func(result map[string]any) { delete(result, "policy_id") })
		}},
		{name: "wrong type", mutate: func(data []byte) []byte {
			return mutateAdaptationServiceResultForTest(t, data, func(result map[string]any) { result["revision"] = "two" })
		}},
		{name: "unknown", mutate: func(data []byte) []byte {
			return mutateAdaptationServiceResultForTest(t, data, func(result map[string]any) { result["unknown"] = true })
		}},
		{name: "cross session", mutate: func(data []byte) []byte {
			return mutateAdaptationServiceResultForTest(t, data, func(result map[string]any) { result["id"] = "rev-cross-session" })
		}},
		{name: "duplicate", mutate: func(data []byte) []byte {
			return []byte(strings.Replace(string(data), `"id":`, `"id":"duplicate","id":`, 1))
		}},
		{name: "multiple", mutate: func(data []byte) []byte { return append(append([]byte(nil), data...), []byte("{}\n")...) }},
	}
	for _, version := range []int{1, 2} {
		for _, mutation := range mutations {
			t.Run(fmt.Sprintf("v%d/%s", version, mutation.name), func(t *testing.T) {
				dir := t.TempDir()
				st := NewStore(dir)
				session := startAdaptationStoreRevision(t, st)
				key, operation, fingerprint := "strict-pause", "pause", strings.Repeat("a", 64)
				crashPreparedAdaptationCommand(t, st, key, operation, fingerprint, func(owner *RevisionStore) error {
					paused, err := owner.Pause(domain.AdaptationRevisionPolicy{Stage: domain.ManuscriptStageWriting}, RevisionMutationInput{
						SessionID: session.ID, ExpectedRevision: session.Revision, IdempotencyKey: key,
					})
					if err != nil {
						return err
					}
					if err := st.SaveAdaptationRevisionServiceReceipt(owner, key, operation, fingerprint, paused); err != nil {
						return err
					}
					if version == 1 {
						return rewriteAdaptationCommandJournalAsV1(st.dir)
					}
					return nil
				})
				receiptPath := newIO(dir).path(adaptationRevisionServiceReceiptsFile)
				validReceipt, err := os.ReadFile(receiptPath)
				if err != nil {
					t.Fatal(err)
				}
				committedBefore, err := st.Revisions.LoadSession(session.ID)
				if err != nil {
					t.Fatal(err)
				}
				formalBefore := formalWriteProjectBytes(t, dir)
				authorityBefore, err := capturePublicationAuthoritySnapshot(newIO(dir))
				if err != nil {
					t.Fatal(err)
				}
				tampered := mutation.mutate(validReceipt)
				if err := os.WriteFile(receiptPath, tampered, 0o644); err != nil {
					t.Fatal(err)
				}
				restarted := NewStore(dir)
				if restarted.commandRecoveryErr == nil {
					t.Fatal("malformed non-publish receipt was treated as committed")
				}
				if pending, err := restarted.adaptationRevisionCommandFilesPending(); err != nil || !pending {
					t.Fatalf("malformed receipt lost prepared evidence: pending=%v err=%v", pending, err)
				}
				committedAfter, err := restarted.Revisions.LoadSession(session.ID)
				if err != nil || !reflect.DeepEqual(committedBefore, committedAfter) {
					t.Fatalf("malformed receipt changed revision facts: before=%+v after=%+v err=%v", committedBefore, committedAfter, err)
				}
				if formalAfter := formalWriteProjectBytes(t, dir); !reflect.DeepEqual(formalBefore, formalAfter) {
					t.Fatal("malformed receipt changed formal facts")
				}
				authorityAfter, err := capturePublicationAuthoritySnapshot(newIO(dir))
				if err != nil || !reflect.DeepEqual(authorityBefore, authorityAfter) {
					t.Fatalf("malformed receipt changed authority facts: err=%v", err)
				}
				if err := os.WriteFile(receiptPath, validReceipt, 0o644); err != nil {
					t.Fatal(err)
				}
				repaired := NewStore(dir)
				if repaired.commandRecoveryErr != nil {
					t.Fatalf("repaired receipt could not retry recovery: %v", repaired.commandRecoveryErr)
				}
				if pending, err := repaired.adaptationRevisionCommandFilesPending(); err != nil || pending {
					t.Fatalf("repaired receipt retained prepared evidence: pending=%v err=%v", pending, err)
				}
			})
		}
	}
}

func TestAdaptationRevisionNonPublishInternalIdentityTamperFailsClosed(t *testing.T) {
	mutations := []struct {
		name   string
		mutate func(state, service []byte, internalFingerprint, serviceFingerprint string) ([]byte, []byte)
	}{
		{name: "internal fingerprint", mutate: func(state, service []byte, internalFingerprint, _ string) ([]byte, []byte) {
			return []byte(strings.Replace(string(state), internalFingerprint, strings.Repeat("0", 64), 1)), service
		}},
		{name: "paired internal and outer fingerprint", mutate: func(state, service []byte, internalFingerprint, serviceFingerprint string) ([]byte, []byte) {
			state = []byte(strings.Replace(string(state), internalFingerprint, strings.Repeat("1", 64), 1))
			service = []byte(strings.Replace(string(service), serviceFingerprint, strings.Repeat("2", 64), 1))
			return state, service
		}},
		{name: "internal service operation", mutate: func(state, service []byte, _, _ string) ([]byte, []byte) {
			return []byte(strings.Replace(string(state), `"service_operation": "pause"`, `"service_operation": "resume"`, 1)), service
		}},
		{name: "internal service fingerprint", mutate: func(state, service []byte, _, serviceFingerprint string) ([]byte, []byte) {
			needle := fmt.Sprintf(`"service_fingerprint": %q`, serviceFingerprint)
			replacement := fmt.Sprintf(`"service_fingerprint": %q`, strings.Repeat("3", 64))
			return []byte(strings.Replace(string(state), needle, replacement, 1)), service
		}},
		{name: "paired service and outer fingerprint", mutate: func(state, service []byte, _, serviceFingerprint string) ([]byte, []byte) {
			replacement := strings.Repeat("4", 64)
			stateNeedle := fmt.Sprintf(`"service_fingerprint": %q`, serviceFingerprint)
			stateReplacement := fmt.Sprintf(`"service_fingerprint": %q`, replacement)
			return []byte(strings.Replace(string(state), stateNeedle, stateReplacement, 1)),
				[]byte(strings.Replace(string(service), serviceFingerprint, replacement, 1))
		}},
	}
	for _, version := range []int{1, 2} {
		for _, mutation := range mutations {
			t.Run(fmt.Sprintf("v%d/%s", version, mutation.name), func(t *testing.T) {
				dir := t.TempDir()
				st := NewStore(dir)
				session := startAdaptationStoreRevision(t, st)
				key, operation, serviceFingerprint := "identity-pause", "pause", strings.Repeat("a", 64)
				crashPreparedAdaptationCommand(t, st, key, operation, serviceFingerprint, func(owner *RevisionStore) error {
					paused, err := owner.Pause(domain.AdaptationRevisionPolicy{Stage: domain.ManuscriptStageWriting}, RevisionMutationInput{
						SessionID: session.ID, ExpectedRevision: session.Revision, IdempotencyKey: key,
					})
					if err != nil {
						return err
					}
					if err := st.SaveAdaptationRevisionServiceReceipt(owner, key, operation, serviceFingerprint, paused); err != nil {
						return err
					}
					if version == 1 {
						return rewriteAdaptationCommandJournalAsV1(st.dir)
					}
					return nil
				})

				statePath := newIO(dir).path(revisionStateFile)
				servicePath := newIO(dir).path(adaptationRevisionServiceReceiptsFile)
				validState, err := os.ReadFile(statePath)
				if err != nil {
					t.Fatal(err)
				}
				validService, err := os.ReadFile(servicePath)
				if err != nil {
					t.Fatal(err)
				}
				var decoded revisionState
				if err := decodeStrictUniqueJSON(validState, &decoded); err != nil {
					t.Fatal(err)
				}
				internalFingerprint := decoded.Receipts[key].Fingerprint
				tamperedState, tamperedService := mutation.mutate(validState, validService, internalFingerprint, serviceFingerprint)
				if reflect.DeepEqual(tamperedState, validState) && reflect.DeepEqual(tamperedService, validService) {
					t.Fatal("identity mutation did not change durable evidence")
				}
				if err := os.WriteFile(statePath, tamperedState, 0o644); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(servicePath, tamperedService, 0o644); err != nil {
					t.Fatal(err)
				}
				restarted := NewStore(dir)
				if restarted.commandRecoveryErr == nil {
					t.Fatal("tampered internal/service identity was accepted")
				}
				if pending, err := restarted.adaptationRevisionCommandFilesPending(); err != nil || !pending {
					t.Fatalf("identity tamper lost prepared evidence: pending=%v err=%v", pending, err)
				}
				if err := os.WriteFile(statePath, validState, 0o644); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(servicePath, validService, 0o644); err != nil {
					t.Fatal(err)
				}
				if repaired := NewStore(dir); repaired.commandRecoveryErr != nil {
					t.Fatalf("repaired identity evidence could not retry: %v", repaired.commandRecoveryErr)
				}
			})
		}
	}
}

func TestAdaptationRevisionPreviewReceiptStrictRecoveryMatrix(t *testing.T) {
	mutations := []struct {
		name   string
		mutate func([]byte) []byte
	}{
		{name: "empty object", mutate: func(data []byte) []byte { return replaceAdaptationServiceResultForTest(t, data, map[string]any{}) }},
		{name: "scalar", mutate: func(data []byte) []byte { return replaceAdaptationServiceResultForTest(t, data, true) }},
		{name: "partial", mutate: func(data []byte) []byte {
			return mutateAdaptationServiceResultForTest(t, data, func(result map[string]any) { delete(result, "preview") })
		}},
		{name: "missing candidate", mutate: func(data []byte) []byte {
			return mutateAdaptationServiceResultForTest(t, data, func(result map[string]any) { delete(result["preview"].(map[string]any), "candidate") })
		}},
		{name: "null candidate", mutate: func(data []byte) []byte {
			return mutateAdaptationServiceResultForTest(t, data, func(result map[string]any) { result["preview"].(map[string]any)["candidate"] = nil })
		}},
		{name: "zero candidate", mutate: func(data []byte) []byte {
			return mutateAdaptationServiceResultForTest(t, data, func(result map[string]any) { result["preview"].(map[string]any)["candidate"] = map[string]any{} })
		}},
		{name: "missing impact", mutate: func(data []byte) []byte {
			return mutateAdaptationServiceResultForTest(t, data, func(result map[string]any) { delete(result["preview"].(map[string]any), "impact") })
		}},
		{name: "null impact", mutate: func(data []byte) []byte {
			return mutateAdaptationServiceResultForTest(t, data, func(result map[string]any) { result["preview"].(map[string]any)["impact"] = nil })
		}},
		{name: "invalid granularity", mutate: func(data []byte) []byte {
			return mutateAdaptationPreviewCandidateForTest(t, data, func(candidate *domain.AdaptationPlan) { candidate.Granularity = "invalid" })
		}},
		{name: "invalid budget", mutate: func(data []byte) []byte {
			return mutateAdaptationPreviewCandidateForTest(t, data, func(candidate *domain.AdaptationPlan) { candidate.TargetTotalRunes = 0 })
		}},
		{name: "invalid source coverage", mutate: func(data []byte) []byte {
			return mutateAdaptationPreviewCandidateForTest(t, data, func(candidate *domain.AdaptationPlan) {
				candidate.Chapters[0].SourceChapters = nil
				candidate.Chapters[0].SourceRange = domain.SourceRange{}
			})
		}},
		{name: "invalid stable target", mutate: func(data []byte) []byte {
			return mutateAdaptationPreviewCandidateForTest(t, data, func(candidate *domain.AdaptationPlan) { candidate.Chapters[0].ID = "" })
		}},
		{name: "invalid event contract", mutate: func(data []byte) []byte {
			return mutateAdaptationPreviewCandidateForTest(t, data, func(candidate *domain.AdaptationPlan) { candidate.SourceEvents[0].ID = "" })
		}},
		{name: "invalid relationship contract", mutate: func(data []byte) []byte {
			return mutateAdaptationPreviewCandidateForTest(t, data, func(candidate *domain.AdaptationPlan) {
				candidate.TargetRelationshipStates = map[string]string{"": "invalid"}
			})
		}},
		{name: "invalid setting contract", mutate: func(data []byte) []byte {
			return mutateAdaptationPreviewCandidateForTest(t, data, func(candidate *domain.AdaptationPlan) {
				candidate.TargetSettingLocks = []domain.AdaptationSettingLock{{Key: "", Value: "invalid"}}
			})
		}},
		{name: "wrong durable base signature", mutate: func(data []byte) []byte {
			return mutateAdaptationPreviewForTest(t, data, func(preview *adaptationStructureRevisionPreviewReceipt) {
				preview.BasePlanSignature = strings.Repeat("c", 64)
			})
		}},
		{name: "wrong durable source manifest signature", mutate: func(data []byte) []byte {
			return mutateAdaptationPreviewForTest(t, data, func(preview *adaptationStructureRevisionPreviewReceipt) {
				preview.SourceManifestSignature = strings.Repeat("d", 64)
			})
		}},
		{name: "missing preserve contract", mutate: func(data []byte) []byte {
			return mutateAdaptationPreviewCandidateForTest(t, data, func(candidate *domain.AdaptationPlan) {
				candidate.Chapters[0].PreserveEvents = nil
			})
		}},
		{name: "missing required changes contract", mutate: func(data []byte) []byte {
			return mutateAdaptationPreviewCandidateForTest(t, data, func(candidate *domain.AdaptationPlan) {
				candidate.Chapters[0].RequiredChanges = nil
			})
		}},
		{name: "missing forbidden moves contract", mutate: func(data []byte) []byte {
			return mutateAdaptationPreviewCandidateForTest(t, data, func(candidate *domain.AdaptationPlan) {
				candidate.Chapters[0].ForbiddenMoves = nil
			})
		}},
		{name: "protected base rules drift", mutate: func(data []byte) []byte {
			return mutateAdaptationPreviewCandidateForTest(t, data, func(candidate *domain.AdaptationPlan) {
				candidate.Brief = "replace durable source contract"
				candidate.Rules = domain.CompileAdaptationRules(candidate.Brief, candidate.Granularity)
				candidate.Chapters[0].RuleIDs = domain.AdaptationRuleIDs(domain.ApplicableAdaptationRules(candidate.Rules, candidate.Granularity, 1))
			})
		}},
		{name: "unknown", mutate: func(data []byte) []byte {
			return mutateAdaptationServiceResultForTest(t, data, func(result map[string]any) { result["unknown"] = true })
		}},
		{name: "wrong type", mutate: func(data []byte) []byte {
			return mutateAdaptationServiceResultForTest(t, data, func(result map[string]any) { result["preview"] = "invalid" })
		}},
		{name: "cross session", mutate: func(data []byte) []byte {
			return mutateAdaptationServiceResultForTest(t, data, func(result map[string]any) {
				result["session"].(map[string]any)["id"] = "rev-cross-session"
			})
		}},
		{name: "duplicate", mutate: func(data []byte) []byte {
			return []byte(strings.Replace(string(data), `"preview":`, `"preview":null,"preview":`, 1))
		}},
		{name: "multiple", mutate: func(data []byte) []byte { return append(append([]byte(nil), data...), []byte("{}\n")...) }},
	}
	for _, version := range []int{1, 2} {
		for _, mutation := range mutations {
			t.Run(fmt.Sprintf("v%d/%s", version, mutation.name), func(t *testing.T) {
				dir := t.TempDir()
				st := NewStore(dir)
				base := adaptationStorePreviewCandidate()
				manifest := adaptationStorePreviewManifest()
				if err := st.Adaptation.SaveProposal(base); err != nil {
					t.Fatal(err)
				}
				if err := st.Adaptation.SaveSourceManifest(manifest); err != nil {
					t.Fatal(err)
				}
				durableBase, err := st.Adaptation.LoadProposal()
				if err != nil {
					t.Fatal(err)
				}
				durableManifest, err := st.Adaptation.LoadSourceManifest()
				if err != nil {
					t.Fatal(err)
				}
				base, manifest = *durableBase, *durableManifest
				key, operation, fingerprint := "strict-preview", "preview", strings.Repeat("b", 64)
				crashPreparedAdaptationCommand(t, st, key, operation, fingerprint, func(owner *RevisionStore) error {
					impact := adaptationStoreFenceImpact(t)
					candidate := base
					basePayload, _ := json.Marshal(base)
					preview := &adaptationStructureRevisionPreviewReceipt{
						Stage: domain.ManuscriptStageWriting, BasePlanSignature: domain.JSONContentSignature(basePayload),
						SourceManifestSignature: domain.AdaptationSourceManifestContractSignature(manifest), Candidate: &candidate, Impact: &impact,
					}
					copy := *preview
					payload, _ := json.Marshal(copy)
					preview.Signature = domain.JSONContentSignature(payload)
					session, err := owner.Start(domain.AdaptationRevisionPolicy{Stage: domain.ManuscriptStageWriting}, StartRevisionInput{
						Intent: "strict preview", Impact: impact, PreviewSignature: preview.Signature, IdempotencyKey: key,
					})
					if err != nil {
						return err
					}
					result := adaptationRevisionPreviewReceipt{Preview: preview, Session: session}
					if err := st.SaveAdaptationRevisionServiceReceipt(owner, key, operation, fingerprint, &result); err != nil {
						return err
					}
					if version == 1 {
						return rewriteAdaptationCommandJournalAsV1(st.dir)
					}
					return nil
				})
				receiptPath := newIO(dir).path(adaptationRevisionServiceReceiptsFile)
				validReceipt, err := os.ReadFile(receiptPath)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(receiptPath, mutation.mutate(validReceipt), 0o644); err != nil {
					t.Fatal(err)
				}
				restarted := NewStore(dir)
				if restarted.commandRecoveryErr == nil {
					t.Fatal("malformed preview receipt was treated as committed")
				}
				if pending, err := restarted.adaptationRevisionCommandFilesPending(); err != nil || !pending {
					t.Fatalf("malformed preview receipt lost prepared evidence: pending=%v err=%v", pending, err)
				}
				if err := os.WriteFile(receiptPath, validReceipt, 0o644); err != nil {
					t.Fatal(err)
				}
				repaired := NewStore(dir)
				if repaired.commandRecoveryErr != nil {
					t.Fatalf("repaired preview receipt could not retry recovery: %v", repaired.commandRecoveryErr)
				}
			})
		}
	}
}

func TestAdaptationRevisionPreviewDurableFactsValidation(t *testing.T) {
	st := NewStore(t.TempDir())
	base := adaptationStorePreviewCandidate()
	manifest := adaptationStorePreviewManifest()
	if err := st.Adaptation.SaveProposal(base); err != nil {
		t.Fatal(err)
	}
	if err := st.Adaptation.SaveSourceManifest(manifest); err != nil {
		t.Fatal(err)
	}
	durableBase, err := st.Adaptation.LoadProposal()
	if err != nil || durableBase == nil {
		t.Fatalf("load durable preview base: base=%+v err=%v", durableBase, err)
	}
	durableManifest, err := st.Adaptation.LoadSourceManifest()
	if err != nil || durableManifest == nil {
		t.Fatalf("load durable preview manifest: manifest=%+v err=%v", durableManifest, err)
	}
	impact := adaptationStoreFenceImpact(t)

	newResult := func() *adaptationRevisionPreviewReceipt {
		candidate := adaptationStoreClonePlan(t, *durableBase)
		basePayload, err := json.Marshal(durableBase)
		if err != nil {
			t.Fatal(err)
		}
		return &adaptationRevisionPreviewReceipt{Preview: &adaptationStructureRevisionPreviewReceipt{
			Stage: domain.ManuscriptStageWriting, BasePlanSignature: domain.JSONContentSignature(basePayload),
			SourceManifestSignature: domain.AdaptationSourceManifestContractSignature(*durableManifest),
			Candidate:               &candidate, Impact: &impact, Signature: strings.Repeat("a", 64),
		}}
	}
	if err := st.verifyAdaptationRevisionPreviewDurableFacts("preview", newResult()); err != nil {
		t.Fatalf("valid durable preview facts were rejected: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*adaptationStructureRevisionPreviewReceipt)
	}{
		{name: "wrong base signature", mutate: func(preview *adaptationStructureRevisionPreviewReceipt) {
			preview.BasePlanSignature = strings.Repeat("b", 64)
		}},
		{name: "wrong manifest signature", mutate: func(preview *adaptationStructureRevisionPreviewReceipt) {
			preview.SourceManifestSignature = strings.Repeat("c", 64)
		}},
		{name: "missing preserve events", mutate: func(preview *adaptationStructureRevisionPreviewReceipt) {
			preview.Candidate.Chapters[0].PreserveEvents = nil
		}},
		{name: "missing required changes", mutate: func(preview *adaptationStructureRevisionPreviewReceipt) {
			preview.Candidate.Chapters[0].RequiredChanges = nil
		}},
		{name: "missing forbidden moves", mutate: func(preview *adaptationStructureRevisionPreviewReceipt) {
			preview.Candidate.Chapters[0].ForbiddenMoves = nil
		}},
		{name: "protected rules drift", mutate: func(preview *adaptationStructureRevisionPreviewReceipt) {
			preview.Candidate.Rules = domain.CompileAdaptationRules("replace protected rules", preview.Candidate.Granularity)
		}},
		{name: "protected relationship goals drift", mutate: func(preview *adaptationStructureRevisionPreviewReceipt) {
			preview.Candidate.RelationshipGoals = []string{"replace protected relationship goal"}
		}},
		{name: "protected relationship states drift", mutate: func(preview *adaptationStructureRevisionPreviewReceipt) {
			preview.Candidate.TargetRelationshipStates = map[string]string{"pair": "changed"}
		}},
		{name: "protected setting locks drift", mutate: func(preview *adaptationStructureRevisionPreviewReceipt) {
			preview.Candidate.TargetSettingLocks = []domain.AdaptationSettingLock{{Key: "city", Value: "changed"}}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := newResult()
			test.mutate(result.Preview)
			if err := st.verifyAdaptationRevisionPreviewDurableFacts("preview", result); err == nil {
				t.Fatal("tampered durable preview facts were accepted")
			}
		})
	}
}

func replaceAdaptationServiceResultForTest(t *testing.T, data []byte, replacement any) []byte {
	t.Helper()
	var state map[string]any
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatal(err)
	}
	for _, value := range state["receipts"].(map[string]any) {
		value.(map[string]any)["result"] = replacement
	}
	encoded, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return append(encoded, '\n')
}

func mutateAdaptationServiceResultForTest(t *testing.T, data []byte, mutate func(map[string]any)) []byte {
	t.Helper()
	var state map[string]any
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatal(err)
	}
	for _, value := range state["receipts"].(map[string]any) {
		mutate(value.(map[string]any)["result"].(map[string]any))
	}
	encoded, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return append(encoded, '\n')
}

func mutateAdaptationPreviewCandidateForTest(t *testing.T, data []byte, mutate func(*domain.AdaptationPlan)) []byte {
	t.Helper()
	return mutateAdaptationPreviewForTest(t, data, func(preview *adaptationStructureRevisionPreviewReceipt) {
		mutate(preview.Candidate)
	})
}

func mutateAdaptationPreviewForTest(t *testing.T, data []byte, mutate func(*adaptationStructureRevisionPreviewReceipt)) []byte {
	t.Helper()
	state, err := decodeAdaptationRevisionServiceReceiptState(data)
	if err != nil {
		t.Fatal(err)
	}
	for key, receipt := range state.Receipts {
		var result adaptationRevisionPreviewReceipt
		if err := decodeStrictUniqueJSON(receipt.Result, &result); err != nil {
			t.Fatal(err)
		}
		mutate(result.Preview)
		copy := *result.Preview
		copy.Signature = ""
		payload, err := json.Marshal(copy)
		if err != nil {
			t.Fatal(err)
		}
		result.Preview.Signature = domain.JSONContentSignature(payload)
		result.Session.PreviewSignature = result.Preview.Signature
		receipt.Result, err = json.Marshal(&result)
		if err != nil {
			t.Fatal(err)
		}
		state.Receipts[key] = receipt
	}
	encoded, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return append(encoded, '\n')
}

func TestAdaptationRevisionV1RollbackPreservesUntrackedAuthorityFacts(t *testing.T) {
	authorityRoot := t.TempDir()
	restoreAuthority, err := ConfigureExpansionAuthorityForTestProcess(authorityRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer restoreAuthority()

	st := NewStore(t.TempDir())
	session := startAdaptationStoreRevision(t, st)
	trustBytes := []byte("new-trust-that-v1-never-tracked")
	receiptBytes := []byte("new-receipt-that-v1-never-tracked")
	instanceID := strings.Repeat("a", 64)
	recordPath, err := authorityProjectRecordPath(instanceID)
	if err != nil {
		t.Fatal(err)
	}
	recordBytes := []byte("new-authority-record-that-v1-never-tracked")

	err = st.WithPreparedAdaptationRevisionCommand("v1-preserve-authority", "cancel", "cancel-fingerprint", func(owner *RevisionStore) error {
		if err := st.PrepareAdaptationRevisionCommand(owner, "v1-preserve-authority", "cancel", "cancel-fingerprint"); err != nil {
			return err
		}
		if err := rewriteAdaptationCommandJournalAsV1(st.dir); err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(recordPath), 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(newIO(st.dir).path(expansionPublicationTrustFile), trustBytes, 0o600); err != nil {
			return err
		}
		if err := os.WriteFile(newIO(st.dir).path(expansionPublicationReceiptFile), receiptBytes, 0o600); err != nil {
			return err
		}
		if err := os.WriteFile(recordPath, recordBytes, 0o600); err != nil {
			return err
		}
		if err := st.SaveAdaptationRevisionRuntime(owner, adaptationStoreRuntime(session.ID, true)); err != nil {
			return err
		}
		return st.RollbackAdaptationRevisionCommand(owner)
	})
	if err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string][]byte{
		newIO(st.dir).path(expansionPublicationTrustFile):   trustBytes,
		newIO(st.dir).path(expansionPublicationReceiptFile): receiptBytes,
		recordPath: recordBytes,
	} {
		got, err := os.ReadFile(path)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("v1 rollback changed untracked authority fact %s: got=%q want=%q err=%v", filepath.Base(path), got, want, err)
		}
	}
}

func TestAdaptationRevisionV1PreparedJournalTamperFailsClosed(t *testing.T) {
	for _, test := range []struct {
		name   string
		tamper func(string) error
	}{
		{name: "unknown field", tamper: func(path string) error {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			return os.WriteFile(path, append(data[:len(data)-2], []byte(",\n  \"unknown\": true\n}\n")...), 0o644)
		}},
		{name: "duplicate field", tamper: func(path string) error {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			return os.WriteFile(path, []byte(strings.Replace(string(data), "\"version\": 1,", "\"version\": 1, \"version\": 1,", 1)), 0o644)
		}},
		{name: "path escape", tamper: func(path string) error {
			var journal adaptationRevisionCommandJournalV1
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if err := json.Unmarshal(data, &journal); err != nil {
				return err
			}
			journal.Files = []string{"../escaped"}
			return newIO(filepath.Dir(filepath.Dir(filepath.Dir(path)))).WriteJSON(adaptationRevisionCommandJournalFile, journal)
		}},
		{name: "missing snapshot", tamper: func(path string) error {
			return os.Remove(filepath.Join(filepath.Dir(path), "adaptation-command-snapshot", filepath.FromSlash(revisionStateFile)))
		}},
		{name: "identity mismatch", tamper: func(path string) error {
			var journal adaptationRevisionCommandJournalV1
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if err := json.Unmarshal(data, &journal); err != nil {
				return err
			}
			journal.Key = "different-key"
			encoded, err := json.MarshalIndent(journal, "", "  ")
			if err != nil {
				return err
			}
			return os.WriteFile(path, append(encoded, '\n'), 0o644)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			st := NewStore(dir)
			startAdaptationStoreRevision(t, st)
			crashPreparedAdaptationCommand(t, st, "v1-tamper", "cancel", "cancel-fingerprint", func(*RevisionStore) error {
				return rewriteAdaptationCommandJournalAsV1(st.dir)
			})
			formalBefore := formalWriteProjectBytes(t, dir)
			journalPath := newIO(dir).path(adaptationRevisionCommandJournalFile)
			if err := test.tamper(journalPath); err != nil {
				t.Fatal(err)
			}
			restarted := NewStore(dir)
			if restarted.commandRecoveryErr == nil {
				t.Fatal("tampered v1 prepared journal did not fail closed")
			}
			if _, err := os.Stat(journalPath); err != nil {
				t.Fatalf("tampered v1 diagnostic journal was removed: %v", err)
			}
			if formalAfter := formalWriteProjectBytes(t, dir); !reflect.DeepEqual(formalBefore, formalAfter) {
				t.Fatal("tampered v1 recovery changed formal files before failing closed")
			}
		})
	}
}

func crashPreparedAdaptationCommand(
	t *testing.T,
	st *Store,
	key, operation, fingerprint string,
	mutate func(*RevisionStore) error,
) {
	t.Helper()
	var commandErr error
	func() {
		defer func() {
			if recover() == nil {
				if commandErr != nil {
					t.Fatalf("prepared command setup failed before simulated crash: %v", commandErr)
				}
				t.Fatal("prepared command did not simulate a process crash")
			}
		}()
		commandErr = st.WithPreparedAdaptationRevisionCommand(key, operation, fingerprint, func(owner *RevisionStore) error {
			if err := st.PrepareAdaptationRevisionCommand(owner, key, operation, fingerprint); err != nil {
				return err
			}
			if err := mutate(owner); err != nil {
				return err
			}
			panic("simulated v1 process crash")
		})
	}()
}

func rewriteAdaptationCommandJournalAsV1(dir string) error {
	io := newIO(dir)
	journal, err := loadAdaptationRevisionCommandJournal(io)
	if err != nil {
		return err
	}
	legacyFiles := make([]string, 0, len(journal.Files))
	allowed := make(map[string]struct{})
	for _, rel := range adaptationRevisionCommandTrackedFilesForVersion(1) {
		allowed[rel] = struct{}{}
	}
	for _, rel := range journal.Files {
		_, tracked := allowed[rel]
		if tracked || strings.HasPrefix(rel, structureRootDir+"/") {
			legacyFiles = append(legacyFiles, rel)
		}
	}
	return io.WriteJSON(adaptationRevisionCommandJournalFile, adaptationRevisionCommandJournalV1{
		Version: 1, Key: journal.Key, Operation: journal.Operation, Fingerprint: journal.Fingerprint, Files: legacyFiles,
	})
}

func adaptationStoreRuntime(sessionID string, paused bool) domain.AdaptationRevisionRuntime {
	return domain.AdaptationRevisionRuntime{
		Version: domain.AdaptationRevisionRuntimeVersion, SessionID: sessionID,
		Stage: domain.ManuscriptStageWriting, BasePlanSignature: "base",
		SourceManifestSignature: "source", PreviewSignature: "preview", Paused: paused,
		BatchPlan: domain.BatchPlan{Batches: []domain.BatchWork{{ID: "batch-1"}}},
	}
}

func formalWriteProjectBytes(t *testing.T, dir string) map[string][]byte {
	t.Helper()
	result := make(map[string][]byte)
	err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if strings.HasSuffix(rel, ".lock") || strings.HasPrefix(rel, "meta/revisions/") ||
			strings.Contains(rel, "adaptation-command-journal") || strings.Contains(rel, "adaptation-command-snapshot") ||
			rel == adaptationRevisionServiceReceiptsFile || rel == adaptationRevisionRuntimeFile {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		result[rel] = data
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestPendingMigrationRecoveryIsFencedOnReopenAndCommand(t *testing.T) {
	st := setupLayered(t, []domain.VolumeOutline{{
		Index: 1, Title: "one", Theme: "one",
		Arcs: []domain.ArcOutline{{Index: 1, Title: "arc", Chapters: []domain.OutlineEntry{{Chapter: 1, Title: "one", CoreEvent: "one", Hook: "one"}}}},
	}})
	st.Outline.migration.failpoint = func(stage string) error {
		if stage == migrationFailAfterWrite {
			return errors.New("leave pending migration")
		}
		return nil
	}
	err := st.AppendSkeletonVolume(domain.VolumeOutline{Index: 2, Title: "two", Theme: "two", Arcs: []domain.ArcOutline{{Index: 1, Title: "arc"}}})
	if err == nil {
		t.Fatal("expected pending migration failpoint")
	}
	st.Outline.migration.failpoint = nil
	before, err := st.CaptureAdaptationFormalSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	startAdaptationStoreRevision(t, st)
	reopened := NewStore(st.Dir())
	if reopened.recoveryErr == nil || !strings.Contains(reopened.recoveryErr.Error(), "active revision") {
		t.Fatalf("startup recovered a pending migration through the active fence: %v", reopened.recoveryErr)
	}
	if err := reopened.RecoverStructureMigration(); err == nil || !strings.Contains(err.Error(), "active revision") {
		t.Fatalf("explicit recovery bypassed the active fence: %v", err)
	}
	if _, err := reopened.Adaptation.LoadPlan(); err == nil || !strings.Contains(err.Error(), "active revision") {
		t.Fatalf("LoadPlan recovered a pending migration through the active fence: %v", err)
	}
	if _, err := reopened.Outline.LoadLayeredOutline(); err == nil || !strings.Contains(err.Error(), "active revision") {
		t.Fatalf("index-backed outline read recovered a pending migration through the active fence: %v", err)
	}
	after, err := st.CaptureAdaptationFormalSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatal("rejected recovery changed the formal snapshot")
	}
}

func startAdaptationStoreRevision(t *testing.T, st *Store) *domain.RevisionSession {
	t.Helper()
	policy := domain.AdaptationRevisionPolicy{Stage: domain.ManuscriptStageWriting}
	impact := adaptationStoreFenceImpact(t)
	session, err := st.Revisions.Start(policy, StartRevisionInput{Intent: "fence direct paths", Impact: impact, PreviewSignature: "preview", IdempotencyKey: "store-fence"})
	if err != nil {
		t.Fatal(err)
	}
	return session
}

func adaptationStoreFenceImpact(t *testing.T) domain.RevisionImpact {
	t.Helper()
	impact, err := domain.NewRevisionImpact("adaptation store fence", []domain.RevisionImpactItem{
		{ArtifactID: adaptationStorePreviewChapterID(), ArtifactKind: domain.StructureKindChapter, Change: "revise target", Requirement: domain.StructureImpactRequired, Cause: domain.StructureImpactContentDependency, DependencyEvidence: []string{"stable target"}},
		{ArtifactID: domain.AdaptationRevisionBatchPlanID, ArtifactKind: domain.AdaptationRevisionArtifactBatchPlan, Change: "bounded work", Requirement: domain.StructureImpactRequired, Cause: domain.StructureImpactContentDependency, DependencyEvidence: []string{"batch"}},
		{ArtifactID: domain.AdaptationRevisionPlanSnapshotID, ArtifactKind: domain.AdaptationRevisionArtifactPlanSnapshot, Change: "bind source", Requirement: domain.StructureImpactRequired, Cause: domain.StructureImpactContentDependency, DependencyEvidence: []string{"source"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return impact
}

func adaptationStorePreviewCandidate() domain.AdaptationPlan {
	granularity := domain.AdaptationGranularityArc
	plan := domain.AdaptationPlan{
		Granularity: granularity, ModePolicy: domain.AdaptationModePolicyForGranularity(granularity),
		Status: domain.AdaptationPlanStatusConfirmed, RewritePolicy: domain.AdaptationRewritePolicyForGranularity(granularity),
		Brief: "preserve source", WordTolerance: 0.15, SourceTotalRunes: 1000,
		TargetTotalRunes: 4000, TargetMinRunes: 3000, TargetMaxRunes: 5000,
		RelationshipGoals:  []string{"preserve the source relationship"},
		TargetSettingLocks: []domain.AdaptationSettingLock{{Key: "city", Value: "source city"}},
		SourceEvents: []domain.AdaptationEvent{{
			ID: "source-event-1", Description: "source event", Origin: domain.AdaptationEventOriginSource,
			Importance: domain.AdaptationEventMainline, SourceChapter: 1, Required: true,
		}},
		Volumes: []domain.AdaptationVolumePlan{{
			ID: adaptationStorePreviewVolumeID(), Index: 1, Title: "volume", TargetFrom: 1, TargetTo: 1,
			SourceFrom: 1, SourceTo: 1, MainlineEventIDs: []string{"source-event-1"},
		}},
		Chapters: []domain.AdaptationChapterPlan{{
			OutlineEntry: domain.OutlineEntry{
				ID: adaptationStorePreviewChapterID(), Chapter: 1, Title: "source",
				CoreEvent: "source event", Hook: "source consequence", Scenes: []string{"source scene"},
			}, Chapter: 1, Title: "source",
			SourceChapters: []int{1}, SourceRange: domain.SourceRange{From: 1, To: 1}, SourceRunes: 1000,
			EventIDs: []string{"source-event-1"}, CoverageNote: "preserve source coverage",
			TargetRunes: 4000, TargetMinRunes: 3000, TargetMaxRunes: 5000,
			PreserveEvents: []string{"source event"}, RequiredChanges: []string{"revise target"},
			ForbiddenMoves: []string{"preserve source event"},
		}},
	}
	plan.Rules = domain.CompileAdaptationRules(plan.Brief, granularity)
	plan.Chapters[0].RuleIDs = domain.AdaptationRuleIDs(domain.ApplicableAdaptationRules(plan.Rules, granularity, 1))
	return plan
}

func adaptationStoreClonePlan(t *testing.T, plan domain.AdaptationPlan) domain.AdaptationPlan {
	t.Helper()
	payload, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	var clone domain.AdaptationPlan
	if err := json.Unmarshal(payload, &clone); err != nil {
		t.Fatal(err)
	}
	return clone
}

func adaptationStorePreviewVolumeID() string {
	return domain.LegacyStructureID("adaptation-store-preview", domain.StructureKindVolume, "volume/1")
}

func adaptationStorePreviewChapterID() string {
	return domain.LegacyStructureID("adaptation-store-preview", domain.StructureKindChapter, "chapter/1")
}

func adaptationStorePreviewManifest() domain.AdaptationSourceManifest {
	return domain.AdaptationSourceManifest{
		SourcePath:   "source",
		ChapterCount: 1,
		Chapters: []domain.AdaptationSource{{
			Chapter: 1, Title: "source", SHA256: strings.Repeat("a", 64), Path: "chapter-1.txt", Runes: 1000,
		}},
	}
}
