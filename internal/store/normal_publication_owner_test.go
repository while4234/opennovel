package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain"
)

type oneStageNormalPublicationPolicy struct{}

func (oneStageNormalPublicationPolicy) Mode() domain.RevisionMode { return domain.RevisionModeNormal }

func (oneStageNormalPublicationPolicy) Identity() (string, string) {
	return domain.NormalRevisionPolicyID, domain.NormalRevisionPolicyVersion
}

func (oneStageNormalPublicationPolicy) ApprovalStages(domain.RevisionImpact) ([]domain.RevisionApprovalStage, error) {
	return []domain.RevisionApprovalStage{{ID: "structure", Label: "Structure"}}, nil
}

func (oneStageNormalPublicationPolicy) ValidateImpact(domain.RevisionImpact) error { return nil }

func (oneStageNormalPublicationPolicy) ValidateCandidate(_ domain.RevisionSession, versions []domain.ArtifactVersion) error {
	if len(versions) != 1 || versions[0].ArtifactID != domain.NormalStructureSnapshotID ||
		versions[0].ArtifactKind != domain.NormalArtifactStructureSnapshot {
		return fmt.Errorf("one canonical normal structure snapshot is required")
	}
	var candidate []domain.VolumeOutline
	if err := json.Unmarshal(versions[0].Payload, &candidate); err != nil {
		return err
	}
	return domain.ValidateStructureSnapshot(candidate)
}

func (oneStageNormalPublicationPolicy) Route(domain.RevisionSession) (*domain.RevisionRoute, error) {
	return nil, nil
}

type oneStageAdaptationPublicationPolicy struct {
	oneStageNormalPublicationPolicy
}

func (oneStageAdaptationPublicationPolicy) Mode() domain.RevisionMode {
	return domain.RevisionModeAdaptation
}

func (oneStageAdaptationPublicationPolicy) Identity() (string, string) {
	return domain.AdaptationRevisionPolicyID, domain.AdaptationRevisionPolicyVersion
}

type normalPublicationFixture struct {
	dir       string
	store     *Store
	policy    domain.RevisionPolicy
	baseline  []domain.VolumeOutline
	candidate []domain.VolumeOutline
	progress  *domain.Progress
	session   *domain.RevisionSession
	input     RevisionMutationInput
	owner     *RevisionPublicationOwner
}

func writeAdaptationPublicationPlan(t *testing.T, fixture normalPublicationFixture) {
	t.Helper()
	chapters := fixture.candidate[0].Arcs[0].Chapters
	plan := domain.AdaptationPlan{
		Granularity: domain.AdaptationGranularityChapter,
		Status:      domain.AdaptationPlanStatusConfirmed,
		Volumes: []domain.AdaptationVolumePlan{{
			ID: fixture.candidate[0].ID, Index: 1, Title: fixture.candidate[0].Title, Theme: fixture.candidate[0].Theme,
			TargetFrom: 1, TargetTo: len(chapters), SourceFrom: 1, SourceTo: len(chapters),
		}},
	}
	for index, outline := range chapters {
		plan.Chapters = append(plan.Chapters, domain.AdaptationChapterPlan{
			OutlineEntry: outline, Chapter: index + 1, Title: outline.Title,
			SourceChapters: []int{index + 1}, SourceRange: domain.SourceRange{From: index + 1, To: index + 1},
		})
	}
	if err := newIO(fixture.dir).WriteJSON(adaptationPlanFile, plan); err != nil {
		t.Fatal(err)
	}
}

func newNormalPublicationFixture(t *testing.T, label string) normalPublicationFixture {
	return newPublicationFixture(t, label, oneStageNormalPublicationPolicy{})
}

func newPublicationFixture(t *testing.T, label string, policy domain.RevisionPolicy) normalPublicationFixture {
	t.Helper()
	useExpansionAuthorityRootForTest(t, filepath.Join(t.TempDir(), "normal-publication-authority"))
	dir := t.TempDir()
	writeStoreTestProjectManifest(t, dir, "normal-publication-"+label)
	st := NewStore(dir)
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	volumeID := domain.LegacyStructureID(label, domain.StructureKindVolume, "volume")
	arcID := domain.LegacyStructureID(label, domain.StructureKindArc, "arc")
	chapterID := domain.LegacyStructureID(label, domain.StructureKindChapter, "chapter-1")
	secondID := domain.LegacyStructureID(label, domain.StructureKindChapter, "chapter-2")
	baseline := []domain.VolumeOutline{{
		ID: volumeID, Index: 1, Title: "Volume", Theme: "theme",
		Arcs: []domain.ArcOutline{{
			ID: arcID, Index: 1, Title: "Arc", Goal: "goal",
			Chapters: []domain.OutlineEntry{{ID: chapterID, Chapter: 1, Title: "One", CoreEvent: "begin", Hook: "cost", Scenes: []string{"begin"}}},
		}},
	}}
	candidate := domain.CloneStructureSnapshot(baseline)
	candidate[0].Arcs[0].Chapters = append(candidate[0].Arcs[0].Chapters, domain.OutlineEntry{
		ID: secondID, Chapter: 2, Title: "Two", CoreEvent: "cost lands", Hook: "choice", Scenes: []string{"pay"},
	})
	progress := &domain.Progress{
		NovelName: "publication", Phase: domain.PhaseComplete, Flow: domain.FlowWriting,
		Layered: true, TotalChapters: 1, CompletedChapters: []int{1},
		CompletionAuditStatus: "pass", CompletionAuditReportDigest: "prepublish-audit",
	}
	if err := st.Outline.SaveLayeredOutline(baseline); err != nil {
		t.Fatal(err)
	}
	if err := st.Progress.Save(progress); err != nil {
		t.Fatal(err)
	}
	impact, err := domain.NewRevisionImpact("publish exact structure", []domain.RevisionImpactItem{{
		ArtifactID: domain.NormalStructureSnapshotID, ArtifactKind: domain.NormalArtifactStructureSnapshot,
		Change: "replace structure", Requirement: domain.StructureImpactRequired, Cause: domain.StructureImpactContentDependency,
	}})
	if err != nil {
		t.Fatal(err)
	}
	session, err := st.Revisions.Start(policy, StartRevisionInput{Intent: "publish", Impact: impact, IdempotencyKey: label + "-start"})
	if err != nil {
		t.Fatal(err)
	}
	session, err = st.Revisions.ApproveImpact(policy, RevisionMutationInput{SessionID: session.ID, ExpectedRevision: session.Revision, IdempotencyKey: label + "-impact"})
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(candidate)
	session, err = st.Revisions.SubmitCandidate(policy, SubmitRevisionCandidateInput{
		SessionID: session.ID, ExpectedRevision: session.Revision, IdempotencyKey: label + "-candidate",
		Artifacts: []CandidateArtifactInput{{ArtifactID: domain.NormalStructureSnapshotID, ArtifactKind: domain.NormalArtifactStructureSnapshot, Payload: payload}},
	})
	if err != nil {
		t.Fatal(err)
	}
	session, err = st.Revisions.RecordAudit(policy, RevisionAuditInput{
		RevisionMutationInput: RevisionMutationInput{SessionID: session.ID, ExpectedRevision: session.Revision, IdempotencyKey: label + "-audit"},
		CandidateSignature:    session.CandidateSignature, Passed: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	session, err = st.Revisions.ApproveStage(policy, RevisionApprovalInput{
		RevisionMutationInput: RevisionMutationInput{SessionID: session.ID, ExpectedRevision: session.Revision, IdempotencyKey: label + "-approve"},
		StageID:               "structure",
	})
	if err != nil {
		t.Fatal(err)
	}
	input := RevisionMutationInput{SessionID: session.ID, ExpectedRevision: session.Revision, IdempotencyKey: label + "-publish"}
	_, owner, err := st.Revisions.ValidatePublishWithOwner(policy, input)
	if err != nil {
		t.Fatal(err)
	}
	return normalPublicationFixture{
		dir: dir, store: st, policy: policy, baseline: baseline, candidate: candidate,
		progress: progress, session: session, input: input, owner: owner,
	}
}

func TestChildFormalWritersRejectPreparedOwnerBypassByteIdentically(t *testing.T) {
	fixture := newNormalPublicationFixture(t, "child-writer-fence")
	// End the ready normal revision so the prepared command is the sole owner.
	if _, err := fixture.store.Revisions.Cancel(fixture.policy, RevisionMutationInput{
		SessionID: fixture.session.ID, ExpectedRevision: fixture.session.Revision, IdempotencyKey: "cancel-before-command",
	}); err != nil {
		t.Fatal(err)
	}
	before := snapshotFormalWriterFiles(t, fixture.dir)
	err := fixture.store.WithPreparedAdaptationRevisionCommand("child-writer", "direct-formal-writes", "fingerprint", func(*RevisionStore) error {
		progressWriters := []struct {
			name string
			call func() error
		}{
			{"save", func() error { return fixture.store.Progress.Save(&domain.Progress{NovelName: "changed"}) }},
			{"init", func() error { return fixture.store.Progress.Init("changed", 99) }},
			{"set total", func() error { return fixture.store.Progress.SetTotalChapters(99) }},
			{"set name", func() error { return fixture.store.Progress.SetNovelName("changed") }},
			{"update phase", func() error { return fixture.store.Progress.UpdatePhase(domain.PhaseWriting) }},
			{"start chapter", func() error { return fixture.store.Progress.StartChapter(1) }},
			{"complete chapter", func() error { return fixture.store.Progress.MarkChapterComplete(1, 100, "hook", "strand") }},
			{"complete book", fixture.store.Progress.MarkComplete},
			{"completion audit", func() error { return fixture.store.Progress.SetCompletionAudit("fail", "changed") }},
			{"reopen", func() error { return fixture.store.Progress.Reopen([]int{1}, "changed") }},
			{"reopen flow", func() error { return fixture.store.Progress.ReopenWithFlow([]int{1}, "changed", domain.FlowRewriting) }},
			{"queue rewrite", func() error {
				return fixture.store.Progress.QueuePendingRewrites([]int{1}, "changed", domain.FlowRewriting)
			}},
			{"clear in progress", fixture.store.Progress.ClearInProgress},
			{"volume arc", func() error { return fixture.store.Progress.UpdateVolumeArc(9, 9) }},
			{"layered", func() error { return fixture.store.Progress.SetLayered(false) }},
			{"flow", func() error { return fixture.store.Progress.SetFlow(domain.FlowRewriting) }},
			{"pending rewrites", func() error { return fixture.store.Progress.SetPendingRewrites([]int{1}, "changed") }},
			{"complete rewrite", func() error { return fixture.store.Progress.CompleteRewrite(1) }},
			{"clear rewrites", fixture.store.Progress.ClearPendingRewrites},
		}
		for _, writer := range progressWriters {
			if err := writer.call(); !errors.Is(err, ErrRevisionCommandInProgress) {
				return fmt.Errorf("progress %s bypass error=%v", writer.name, err)
			}
		}
		outlineWriters := []struct {
			name string
			call func() error
		}{
			{"flat", func() error { return fixture.store.Outline.SaveOutline(domain.FlattenOutline(fixture.candidate)) }},
			{"layered", func() error { return fixture.store.Outline.SaveLayeredOutline(fixture.candidate) }},
			{"clear layered", fixture.store.Outline.ClearLayeredOutline},
		}
		for _, writer := range outlineWriters {
			if err := writer.call(); !errors.Is(err, ErrRevisionCommandInProgress) {
				return fmt.Errorf("outline %s bypass error=%v", writer.name, err)
			}
		}
		if _, err := fixture.store.ReconcilePendingRewriteProgress(); !errors.Is(err, ErrRevisionCommandInProgress) {
			return fmt.Errorf("reconcile progress bypass error=%v", err)
		}
		if err := fixture.store.ClearHandledSteer(); !errors.Is(err, ErrRevisionCommandInProgress) {
			return fmt.Errorf("clear steering progress bypass error=%v", err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	after := snapshotFormalWriterFiles(t, fixture.dir)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("rejected child writers changed formal bytes\nbefore=%v\nafter=%v", before, after)
	}
}

func TestNormalPublicationRequiresExactAppliedOwner(t *testing.T) {
	fixture := newNormalPublicationFixture(t, "plain-publish-rejection")
	seedNormalPublicationSentinels(t, fixture.dir)

	before := snapshotNormalPublicationProjectFiles(t, fixture.dir)
	if _, err := fixture.store.Revisions.Publish(fixture.policy, fixture.input); !errors.Is(err, ErrRevisionCommandInProgress) {
		t.Fatalf("plain normal Publish error=%v", err)
	}
	if after := snapshotNormalPublicationProjectFiles(t, fixture.dir); !reflect.DeepEqual(before, after) {
		t.Fatal("plain normal Publish changed revision, formal, runtime, receipt, audit, or journal bytes")
	}
	active, err := fixture.store.Revisions.Active()
	if err != nil || active == nil || active.ID != fixture.session.ID || active.Stage != domain.RevisionStageReadyToPublish {
		t.Fatalf("plain normal Publish consumed the active lifecycle: active=%+v err=%v", active, err)
	}
	if current, err := fixture.store.Revisions.CurrentVersion(domain.NormalStructureSnapshotID); err != nil || current != nil {
		t.Fatalf("plain normal Publish consumed candidate artifacts: current=%+v err=%v", current, err)
	}

	before = snapshotNormalPublicationProjectFiles(t, fixture.dir)
	if err := fixture.store.PublishLayeredStructureForRevision(fixture.owner, fixture.baseline, fixture.input.IdempotencyKey); err == nil {
		t.Fatal("valid but unaccepted structure substituted for the bound candidate")
	}
	if after := snapshotNormalPublicationProjectFiles(t, fixture.dir); !reflect.DeepEqual(before, after) {
		t.Fatal("candidate substitution changed project bytes")
	}

	if err := fixture.store.PublishLayeredStructureForRevision(fixture.owner, fixture.candidate, fixture.input.IdempotencyKey); err != nil {
		t.Fatal(err)
	}
	before = snapshotNormalPublicationProjectFiles(t, fixture.dir)
	if _, err := fixture.store.Revisions.Publish(fixture.policy, fixture.input); !errors.Is(err, ErrRevisionCommandInProgress) {
		t.Fatalf("plain Publish entered a formal_applied attempt: %v", err)
	}
	if after := snapshotNormalPublicationProjectFiles(t, fixture.dir); !reflect.DeepEqual(before, after) {
		t.Fatal("plain Publish consumed the durable formal_applied attempt")
	}
	wrongInput := fixture.input
	wrongInput.IdempotencyKey += "-substituted"
	if _, err := fixture.store.Revisions.PublishWithOwner(fixture.policy, wrongInput, fixture.owner); !errors.Is(err, ErrRevisionCommandInProgress) {
		t.Fatalf("owner accepted a substituted publish identity: %v", err)
	}
	if after := snapshotNormalPublicationProjectFiles(t, fixture.dir); !reflect.DeepEqual(before, after) {
		t.Fatal("publish identity substitution changed project bytes")
	}

	published, err := fixture.store.Revisions.PublishWithOwner(fixture.policy, fixture.input, fixture.owner)
	if err != nil || published == nil || published.Stage != domain.RevisionStageCompleted {
		t.Fatalf("exact owner publication=%+v err=%v", published, err)
	}
	assertFormalStructure(t, fixture.store, fixture.candidate, 2)
	if active, err := fixture.store.Revisions.Active(); err != nil || active != nil {
		t.Fatalf("exact owner did not complete the lifecycle: active=%+v err=%v", active, err)
	}
	if current, err := fixture.store.Revisions.CurrentVersion(domain.NormalStructureSnapshotID); err != nil || current == nil {
		t.Fatalf("exact owner did not publish bound artifacts: current=%+v err=%v", current, err)
	}
}

func TestNormalPublicationOwnerBindsCandidateAndOneShotAttempt(t *testing.T) {
	t.Run("candidate substitution and rollback mismatch", func(t *testing.T) {
		fixture := newNormalPublicationFixture(t, "candidate-binding")
		before := snapshotFormalWriterFiles(t, fixture.dir)
		if err := fixture.store.PublishLayeredStructureForRevision(fixture.owner, fixture.baseline, fixture.input.IdempotencyKey); err == nil {
			t.Fatal("valid but unaccepted candidate substituted for the canonical candidate")
		}
		if after := snapshotFormalWriterFiles(t, fixture.dir); !reflect.DeepEqual(before, after) {
			t.Fatal("candidate substitution changed formal bytes")
		}
		if err := fixture.store.PublishLayeredStructureForRevision(fixture.owner, fixture.candidate, fixture.input.IdempotencyKey); err != nil {
			t.Fatal(err)
		}
		if err := fixture.store.RestoreLayeredStructureForRevision(fixture.owner, fixture.candidate, fixture.progress); err == nil {
			t.Fatal("rollback accepted a snapshot that was not the bound prepublish state")
		}
		assertFormalStructure(t, fixture.store, fixture.candidate, 2)
		if err := fixture.store.RollbackLayeredStructureForRevision(fixture.owner); err != nil {
			t.Fatal(err)
		}
		assertFormalStructure(t, fixture.store, fixture.baseline, 1)
		if err := fixture.store.RollbackLayeredStructureForRevision(fixture.owner); err == nil {
			t.Fatal("rollback owner replay succeeded")
		}
	})

	t.Run("failed formal write rolls back once", func(t *testing.T) {
		fixture := newNormalPublicationFixture(t, "failure-rollback")
		injected := true
		fixture.store.Outline.migration.failpoint = func(point string) error {
			if injected && point == migrationFailAfterWrite {
				injected = false
				return fmt.Errorf("injected publication failure")
			}
			return nil
		}
		if err := fixture.store.PublishLayeredStructureForRevision(fixture.owner, fixture.candidate, fixture.input.IdempotencyKey); err == nil {
			t.Fatal("injected formal publication failure did not fire")
		}
		fixture.store.Outline.migration.failpoint = nil
		before := snapshotNormalPublicationProjectFiles(t, fixture.dir)
		if _, err := fixture.store.Revisions.PublishWithOwner(fixture.policy, fixture.input, fixture.owner); !errors.Is(err, ErrRevisionCommandInProgress) {
			t.Fatalf("prepared-only publication attempt finalized: %v", err)
		}
		if after := snapshotNormalPublicationProjectFiles(t, fixture.dir); !reflect.DeepEqual(before, after) {
			t.Fatal("rejected prepared-only finalization changed project bytes")
		}
		if err := fixture.store.RollbackLayeredStructureForRevision(fixture.owner); err != nil {
			t.Fatal(err)
		}
		assertFormalStructure(t, fixture.store, fixture.baseline, 1)
	})

	t.Run("formal structure substitution blocks finalization", func(t *testing.T) {
		fixture := newNormalPublicationFixture(t, "formal-structure-binding")
		if err := fixture.store.PublishLayeredStructureForRevision(fixture.owner, fixture.candidate, fixture.input.IdempotencyKey); err != nil {
			t.Fatal(err)
		}
		payload, err := json.MarshalIndent(fixture.baseline, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(fixture.dir, "layered_outline.json"), payload, 0o644); err != nil {
			t.Fatal(err)
		}
		before := snapshotNormalPublicationProjectFiles(t, fixture.dir)
		if _, err := fixture.store.Revisions.PublishWithOwner(fixture.policy, fixture.input, fixture.owner); err == nil || !strings.Contains(err.Error(), "formal structure changed") {
			t.Fatalf("substituted formal structure finalization error=%v", err)
		}
		if after := snapshotNormalPublicationProjectFiles(t, fixture.dir); !reflect.DeepEqual(before, after) {
			t.Fatal("formal structure mismatch rejection changed project bytes")
		}
		if err := fixture.store.RollbackLayeredStructureForRevision(fixture.owner); err != nil {
			t.Fatal(err)
		}
		assertFormalStructure(t, fixture.store, fixture.baseline, 1)
	})

	t.Run("success invalidates owner and blocks successor", func(t *testing.T) {
		fixture := newNormalPublicationFixture(t, "success-one-shot")
		if err := fixture.store.PublishLayeredStructureForRevision(fixture.owner, fixture.candidate, fixture.input.IdempotencyKey); err != nil {
			t.Fatal(err)
		}
		published, err := fixture.store.Revisions.PublishWithOwner(fixture.policy, fixture.input, fixture.owner)
		if err != nil || published.Stage != domain.RevisionStageCompleted {
			t.Fatalf("published=%+v err=%v", published, err)
		}
		if _, err := fixture.store.Revisions.PublishWithOwner(fixture.policy, fixture.input, fixture.owner); err == nil {
			t.Fatal("successful owner replay returned its old receipt")
		}
		if err := fixture.store.RollbackLayeredStructureForRevision(fixture.owner); err == nil {
			t.Fatal("successful publication owner rolled back committed formal state")
		}
		assertFormalStructure(t, fixture.store, fixture.candidate, 2)
		progress, progressErr := fixture.store.Progress.Load()
		if progressErr != nil || progress == nil || progress.Phase != domain.PhaseWriting || progress.CompletionRevalidation == nil || progress.CompletionRevalidation.Status != "pending" || progress.CompletionRevalidation.AcceptedRevisionID != fixture.owner.sessionID || progress.CompletionRevalidation.CurrentStructureSignature != domain.StructureSignature(fixture.candidate) {
			t.Fatalf("durable completion checkpoint=%+v err=%v", progress, progressErr)
		}
	})

	t.Run("restart recovers bound snapshot", func(t *testing.T) {
		fixture := newNormalPublicationFixture(t, "restart-rollback")
		if err := fixture.store.PublishLayeredStructureForRevision(fixture.owner, fixture.candidate, fixture.input.IdempotencyKey); err != nil {
			t.Fatal(err)
		}
		reopened := NewStore(fixture.dir)
		if err := reopened.RecoverStructureMigration(); err != nil {
			t.Fatal(err)
		}
		assertFormalStructure(t, reopened, fixture.baseline, 1)
		if err := reopened.RollbackLayeredStructureForRevision(fixture.owner); err == nil {
			t.Fatal("recovered publication owner remained reusable")
		}
	})

	t.Run("active lease successor and cross project reject stale owner", func(t *testing.T) {
		fixture := newNormalPublicationFixture(t, "stale-owner")
		if err := fixture.store.PublishLayeredStructureForRevision(fixture.owner, fixture.candidate, fixture.input.IdempotencyKey); err != nil {
			t.Fatal(err)
		}
		if err := fixture.store.RollbackLayeredStructureForRevision(fixture.owner); err != nil {
			t.Fatal(err)
		}
		cancelled, err := fixture.store.Revisions.Cancel(fixture.policy, RevisionMutationInput{
			SessionID: fixture.session.ID, ExpectedRevision: fixture.session.Revision, IdempotencyKey: "cancel-after-rollback",
		})
		if err != nil || cancelled.Stage != domain.RevisionStageCancelled {
			t.Fatalf("cancelled=%+v err=%v", cancelled, err)
		}
		lease, err := fixture.store.Revisions.AcquireNormalFlow("successor-normal-flow")
		if err != nil {
			t.Fatal(err)
		}
		if err := fixture.store.RollbackLayeredStructureForRevision(fixture.owner); err == nil {
			t.Fatal("stale owner overwrote an active normal-flow lease")
		}
		if err := fixture.store.Revisions.ReleaseNormalFlow(lease.Token); err != nil {
			t.Fatal(err)
		}
		successorImpact, _ := domain.NewRevisionImpact("successor", []domain.RevisionImpactItem{{ArtifactID: domain.NormalStructureSnapshotID, ArtifactKind: domain.NormalArtifactStructureSnapshot, Change: "successor"}})
		if _, err := fixture.store.Revisions.Start(fixture.policy, StartRevisionInput{Intent: "successor", Impact: successorImpact, IdempotencyKey: "successor-start"}); err != nil {
			t.Fatal(err)
		}
		if err := fixture.store.RollbackLayeredStructureForRevision(fixture.owner); err == nil {
			t.Fatal("stale owner overwrote a successor revision")
		}
		other := newNormalPublicationFixture(t, "cross-project")
		if err := other.store.PublishLayeredStructureForRevision(fixture.owner, other.candidate, fixture.input.IdempotencyKey); err == nil {
			t.Fatal("cross-project publication owner was accepted")
		}
	})
}

func TestNormalPublicationAuthorityRollsBackWriteCommitAndCrashFailures(t *testing.T) {
	for _, test := range []struct {
		name      string
		faultFile string
	}{
		{"receipt write", expansionPublicationReceiptFile},
		{"trust write", expansionPublicationTrustFile},
		{"revision state commit", revisionStateFile},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newNormalPublicationFixture(t, "authority-"+strings.ReplaceAll(test.name, " ", "-"))
			if err := fixture.store.PublishLayeredStructureForRevision(fixture.owner, fixture.candidate, fixture.input.IdempotencyKey); err != nil {
				t.Fatal(err)
			}
			before, err := capturePublicationAuthoritySnapshot(newIO(fixture.dir))
			if err != nil {
				t.Fatal(err)
			}
			registryBefore := snapshotExpansionAuthorityRegistry(t)
			restoreFault := fixture.store.SetExpansionWriteFaultForTesting(func(rel, stage string) error {
				if rel == test.faultFile && stage == "after_temp_sync" {
					return errors.New("injected authority transaction failure")
				}
				return nil
			})
			if _, err := fixture.store.Revisions.Publish(fixture.policy, fixture.input); err == nil {
				t.Fatal("injected publication failure did not fire")
			}
			restoreFault()
			after, err := capturePublicationAuthoritySnapshot(newIO(fixture.dir))
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before, after) {
				t.Fatalf("authority changed across failed %s\nbefore=%+v\nafter=%+v", test.name, before, after)
			}
			if registryAfter := snapshotExpansionAuthorityRegistry(t); !reflect.DeepEqual(registryBefore, registryAfter) {
				t.Fatalf("external authority registry changed across failed %s\nbefore=%+v\nafter=%+v", test.name, registryBefore, registryAfter)
			}
			if err := fixture.store.RollbackLayeredStructureForRevision(fixture.owner); err != nil {
				t.Fatal(err)
			}
		})
	}

	t.Run("external private write", func(t *testing.T) {
		fixture := newNormalPublicationFixture(t, "authority-private-write")
		if err := fixture.store.PublishLayeredStructureForRevision(fixture.owner, fixture.candidate, fixture.input.IdempotencyKey); err != nil {
			t.Fatal(err)
		}
		before, _ := capturePublicationAuthoritySnapshot(newIO(fixture.dir))
		expansionAuthorityWriteFault = func(path, stage string) error {
			if strings.Contains(filepath.ToSlash(path), "/projects/") && stage == "after_sync" {
				return errors.New("injected protected private-key write failure")
			}
			return nil
		}
		defer func() { expansionAuthorityWriteFault = nil }()
		if _, err := fixture.store.Revisions.PublishWithOwner(fixture.policy, fixture.input, fixture.owner); err == nil {
			t.Fatal("private-key write failure did not fire")
		}
		expansionAuthorityWriteFault = nil
		after, _ := capturePublicationAuthoritySnapshot(newIO(fixture.dir))
		if !reflect.DeepEqual(before, after) {
			t.Fatalf("authority changed across private-key failure\nbefore=%+v\nafter=%+v", before, after)
		}
		if err := fixture.store.RollbackLayeredStructureForRevision(fixture.owner); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("startup crash recovery", func(t *testing.T) {
		fixture := newNormalPublicationFixture(t, "authority-crash")
		if err := fixture.store.PublishLayeredStructureForRevision(fixture.owner, fixture.candidate, fixture.input.IdempotencyKey); err != nil {
			t.Fatal(err)
		}
		beforeState, err := fixture.store.Revisions.loadUnlocked()
		if err != nil || beforeState.Publication == nil {
			t.Fatalf("durable publication attempt missing: %v", err)
		}
		identity, err := capturePublicationProjectIdentity(fixture.dir)
		if err != nil {
			t.Fatal(err)
		}
		trust, _, err := createExpansionProjectAuthority(identity.Manifest.ID, fixture.dir)
		if err != nil {
			t.Fatal(err)
		}
		if err := newIO(fixture.dir).WriteJSON(expansionPublicationTrustFile, trust); err != nil {
			t.Fatal(err)
		}
		if err := newIO(fixture.dir).WriteJSON(expansionPublicationReceiptFile, ExpansionPublicationReceipt{Version: 1, ProjectID: trust.ProjectID, ProjectInstance: trust.ProjectInstance}); err != nil {
			t.Fatal(err)
		}
		reopened := NewStore(fixture.dir)
		if err := reopened.RecoverStructureMigration(); err != nil {
			t.Fatal(err)
		}
		after, err := capturePublicationAuthoritySnapshot(newIO(fixture.dir))
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(beforeState.Publication.AuthoritySnapshot, after) {
			t.Fatalf("startup recovery did not restore authority snapshot\nwant=%+v\ngot=%+v", beforeState.Publication.AuthoritySnapshot, after)
		}
	})

	t.Run("repeated failure retry and committed replay keep one exact generation", func(t *testing.T) {
		fixture := newNormalPublicationFixture(t, "authority-retry-generation")
		if err := fixture.store.PublishLayeredStructureForRevision(fixture.owner, fixture.candidate, fixture.input.IdempotencyKey); err != nil {
			t.Fatal(err)
		}
		before := snapshotExpansionAuthorityRegistry(t)
		restoreFault := fixture.store.SetExpansionWriteFaultForTesting(func(rel, stage string) error {
			if rel == expansionPublicationReceiptFile && stage == "after_temp_sync" {
				return errors.New("repeatable receipt failure")
			}
			return nil
		})
		for attempt := 0; attempt < 2; attempt++ {
			if _, err := fixture.store.Revisions.PublishWithOwner(fixture.policy, fixture.input, fixture.owner); err == nil {
				t.Fatal("repeated injected publication failure did not fire")
			}
			if got := snapshotExpansionAuthorityRegistry(t); !reflect.DeepEqual(before, got) {
				t.Fatalf("failed retry %d left an authority generation\nbefore=%+v\nafter=%+v", attempt+1, before, got)
			}
		}
		restoreFault()
		committed, err := fixture.store.Revisions.PublishWithOwner(fixture.policy, fixture.input, fixture.owner)
		if err != nil {
			t.Fatal(err)
		}
		afterCommit := snapshotExpansionAuthorityRegistry(t)
		if len(afterCommit) != len(before)+1 {
			t.Fatalf("successful first publication did not add exactly one authority record: before=%d after=%d", len(before), len(afterCommit))
		}
		replayed, err := fixture.store.Revisions.lookupRevisionReceipt(fixture.input.IdempotencyKey, "publish", fixture.owner.publishFingerprint)
		if err != nil || !reflect.DeepEqual(committed, replayed) {
			t.Fatalf("committed publication replay=%+v err=%v", replayed, err)
		}
		if got := snapshotExpansionAuthorityRegistry(t); !reflect.DeepEqual(afterCommit, got) {
			t.Fatalf("idempotent replay changed external authority bytes\ncommitted=%+v\nreplay=%+v", afterCommit, got)
		}
	})
}

func TestExpansionPublicationReceiptAndRevisionStateShareOneTransaction(t *testing.T) {
	fixture := newNormalPublicationFixture(t, "receipt-state-single-transaction")
	if err := fixture.store.PublishLayeredStructureForRevision(fixture.owner, fixture.candidate, fixture.input.IdempotencyKey); err != nil {
		t.Fatal(err)
	}

	receiptWritten := make(chan struct{})
	releaseCommit := make(chan struct{})
	restoreFault := fixture.store.SetExpansionWriteFaultForTesting(func(rel, stage string) error {
		if rel == expansionPublicationReceiptFile && stage == "after_replace" {
			close(receiptWritten)
			<-releaseCommit
		}
		return nil
	})
	defer restoreFault()

	publishResult := make(chan error, 1)
	go func() {
		_, err := fixture.store.Revisions.PublishWithOwner(fixture.policy, fixture.input, fixture.owner)
		publishResult <- err
	}()
	select {
	case <-receiptWritten:
	case err := <-publishResult:
		t.Fatalf("publication failed before the signed receipt boundary: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("publication did not reach the signed receipt boundary")
	}

	competingWriter := make(chan error, 1)
	go func() {
		competingWriter <- fixture.store.SaveExpansionAuditorTrust(ExpansionAuditorTrust{PublicKeyHex: strings.Repeat("a", 64)})
	}()
	select {
	case err := <-competingWriter:
		t.Fatalf("formal writer entered between receipt and revision state commit: %v", err)
	case <-time.After(200 * time.Millisecond):
	}
	competingStartup := make(chan *Store, 1)
	go func() { competingStartup <- NewStore(fixture.dir) }()
	select {
	case <-competingStartup:
		t.Fatal("NewStore entered while the live publisher owned the project transaction")
	case <-time.After(200 * time.Millisecond):
	}
	close(releaseCommit)
	if err := <-publishResult; err != nil {
		t.Fatal(err)
	}
	select {
	case reopened := <-competingStartup:
		if err := reopened.RecoverStructureMigration(); err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("NewStore deadlocked after the live publisher committed")
	}
	if err := <-competingWriter; err != nil {
		t.Fatal(err)
	}

	state, err := fixture.store.Revisions.load()
	if err != nil {
		t.Fatal(err)
	}
	committed, ok := state.Receipts[fixture.input.IdempotencyKey]
	if !ok || committed.Result.Stage != domain.RevisionStageCompleted || committed.Result.Generation != state.Generation {
		t.Fatalf("revision receipt/state generation was not committed atomically: state=%+v receipt=%+v", state, committed)
	}
	var publication ExpansionPublicationReceipt
	if err := newIO(fixture.dir).ReadJSON(expansionPublicationReceiptFile, &publication); err != nil {
		t.Fatal(err)
	}
	if publication.SessionID != committed.Result.ID || publication.SessionRevision != committed.Result.Revision || publication.PublicationGeneration != committed.Result.Generation {
		t.Fatalf("signed publication receipt does not bind the committed revision generation: publication=%+v revision=%+v", publication, committed.Result)
	}
}

func TestCommittedPublicationFinalizeFaultRetryAndUnavailableGC(t *testing.T) {
	previousRetention := authorityOrphanRetention
	authorityOrphanRetention = 0
	defer func() { authorityOrphanRetention = previousRetention }()
	t.Run("acceptance write failure retries the committed receipt", func(t *testing.T) {
		fixture := newNormalPublicationFixture(t, "acceptance-finalize-retry")
		if err := fixture.store.PublishLayeredStructureForRevision(fixture.owner, fixture.candidate, fixture.input.IdempotencyKey); err != nil {
			t.Fatal(err)
		}
		expansionAuthorityWriteFault = func(path, stage string) error {
			if strings.Contains(filepath.ToSlash(path), "/acceptances/") && stage == "after_sync" {
				return errors.New("injected acceptance finalize failure")
			}
			return nil
		}
		if _, err := fixture.store.Revisions.PublishWithOwner(fixture.policy, fixture.input, fixture.owner); err == nil {
			t.Fatal("acceptance finalize failure did not fire")
		}
		expansionAuthorityWriteFault = nil
		state, err := fixture.store.Revisions.loadUnlocked()
		if err != nil || state.Receipts[fixture.input.IdempotencyKey].Result.Stage != domain.RevisionStageCompleted {
			t.Fatalf("formal state was not committed before finalize failure: %v", err)
		}
		if _, err := fixture.store.Revisions.PublishWithOwner(fixture.policy, fixture.input, fixture.owner); err != nil {
			t.Fatalf("idempotent retry did not finish finalize: %v", err)
		}
		rootHash, _ := expansionPublicationRootHash(fixture.dir)
		journalPath, _ := authorityCreationJournalPath(rootHash)
		if _, err := os.Stat(journalPath); !os.IsNotExist(err) {
			t.Fatalf("finalized journal remains after retry: %v", err)
		}
	})

	for _, unavailable := range []string{"moved", "deleted"} {
		t.Run("accepted evidence protects "+unavailable+" committed project", func(t *testing.T) {
			fixture := newNormalPublicationFixture(t, "accepted-evidence-"+unavailable)
			if err := fixture.store.PublishLayeredStructureForRevision(fixture.owner, fixture.candidate, fixture.input.IdempotencyKey); err != nil {
				t.Fatal(err)
			}
			publicationJournalWrites := 0
			expansionAuthorityWriteFault = func(path, stage string) error {
				if strings.Contains(filepath.ToSlash(path), "/publications/") && stage == "after_sync" {
					publicationJournalWrites++
					if publicationJournalWrites == 2 {
						return errors.New("injected accepted journal transition failure")
					}
				}
				return nil
			}
			if _, err := fixture.store.Revisions.PublishWithOwner(fixture.policy, fixture.input, fixture.owner); err == nil {
				t.Fatal("accepted journal transition failure did not fire")
			}
			expansionAuthorityWriteFault = nil
			rootHash, err := expansionPublicationRootHash(fixture.dir)
			if err != nil {
				t.Fatal(err)
			}
			journalPath, _ := authorityCreationJournalPath(rootHash)
			var journal expansionAuthorityCreationJournal
			if err := readProtectedAuthorityJSONStrict(journalPath, &journal); err != nil || journal.State != authorityCreationPending {
				t.Fatalf("pending committed journal missing: state=%q err=%v", journal.State, err)
			}
			recordPath, _ := authorityProjectRecordPath(journal.ProjectInstance)
			if unavailable == "moved" {
				if err := os.Rename(fixture.dir, fixture.dir+"-moved"); err != nil {
					t.Fatal(err)
				}
			} else if err := os.RemoveAll(fixture.dir); err != nil {
				t.Fatal(err)
			}
			report, err := ReconcileExpansionAuthorityOrphans()
			if err != nil || report.Finalized != 1 {
				t.Fatalf("committed unavailable reconciliation report=%+v err=%v", report, err)
			}
			if _, err := os.Stat(recordPath); err != nil {
				t.Fatalf("committed private record was deleted: %v", err)
			}
			if _, err := os.Stat(journalPath); !os.IsNotExist(err) {
				t.Fatalf("committed journal was not finalized: %v", err)
			}
		})
	}
}

func TestAdaptationCommittedPublicationFinalizeFaultRetryAndUnavailableGC(t *testing.T) {
	previousRetention := authorityOrphanRetention
	authorityOrphanRetention = 0
	defer func() { authorityOrphanRetention = previousRetention }()

	for _, fault := range []struct {
		name      string
		pathPart  string
		stage     string
		writeCall int
	}{
		{name: "before acceptance replace", pathPart: "/acceptances/", stage: "after_sync", writeCall: 1},
		{name: "after acceptance replace", pathPart: "/acceptances/", stage: "after_replace", writeCall: 1},
		{name: "before accepted journal replace", pathPart: "/publications/", stage: "after_sync", writeCall: 2},
		{name: "after accepted journal replace", pathPart: "/publications/", stage: "after_replace", writeCall: 2},
	} {
		t.Run(fault.name, func(t *testing.T) {
			defer func() { expansionAuthorityWriteFault = nil }()
			label := "adaptation-" + strings.ReplaceAll(fault.name, " ", "-")
			fixture := newPublicationFixture(t, label, oneStageAdaptationPublicationPolicy{})
			writeAdaptationPublicationPlan(t, fixture)
			if _, _, err := fixture.store.Revisions.loadOrCreateExpansionPublicationAuthority("normal-publication-" + label); err != nil {
				t.Fatal(err)
			}
			calls := 0
			expansionAuthorityWriteFault = func(path, stage string) error {
				if strings.Contains(filepath.ToSlash(path), fault.pathPart) && stage == fault.stage {
					calls++
					if calls == fault.writeCall {
						return errors.New("injected adaptation finalize fault")
					}
				}
				return nil
			}
			if _, err := fixture.store.Revisions.PublishWithOwner(fixture.policy, fixture.input, fixture.owner); err == nil {
				t.Fatal("adaptation finalize fault did not fire")
			}
			expansionAuthorityWriteFault = nil
			result, err := fixture.store.Revisions.Publish(fixture.policy, fixture.input)
			if err != nil || result.Stage != domain.RevisionStageCompleted {
				t.Fatalf("exact adaptation retry did not finish finalize: result=%+v err=%v", result, err)
			}
			restarted := NewStore(fixture.dir).Revisions
			if replay, err := restarted.Publish(fixture.policy, fixture.input); err != nil || replay.Stage != domain.RevisionStageCompleted {
				t.Fatalf("adaptation restart exact replay failed: result=%+v err=%v", replay, err)
			}
			conflict := fixture.input
			conflict.ExpectedRevision++
			if _, err := restarted.Publish(fixture.policy, conflict); err == nil {
				t.Fatal("same-key different adaptation fingerprint bypassed exact replay")
			}
		})
	}

	for _, unavailable := range []string{"moved", "deleted"} {
		t.Run("committed adaptation survives "+unavailable+" output GC", func(t *testing.T) {
			defer func() { expansionAuthorityWriteFault = nil }()
			label := "adaptation-unavailable-" + unavailable
			fixture := newPublicationFixture(t, label, oneStageAdaptationPublicationPolicy{})
			writeAdaptationPublicationPlan(t, fixture)
			if _, _, err := fixture.store.Revisions.loadOrCreateExpansionPublicationAuthority("normal-publication-" + label); err != nil {
				t.Fatal(err)
			}
			expansionAuthorityWriteFault = func(path, stage string) error {
				if strings.Contains(filepath.ToSlash(path), "/acceptances/") && stage == "after_replace" {
					return errors.New("injected adaptation acceptance post-replace failure")
				}
				return nil
			}
			if _, err := fixture.store.Revisions.Publish(fixture.policy, fixture.input); err == nil {
				t.Fatal("adaptation accepted transition fault did not fire")
			}
			expansionAuthorityWriteFault = nil
			rootHash, _ := expansionPublicationRootHash(fixture.dir)
			journalPath, _ := authorityCreationJournalPath(rootHash)
			var journal expansionAuthorityCreationJournal
			if err := readProtectedAuthorityJSONStrict(journalPath, &journal); err != nil {
				t.Fatal(err)
			}
			recordPath, _ := authorityProjectRecordPath(journal.ProjectInstance)
			if unavailable == "moved" {
				if err := os.Rename(fixture.dir, fixture.dir+"-moved"); err != nil {
					t.Fatal(err)
				}
			} else if err := os.RemoveAll(fixture.dir); err != nil {
				t.Fatal(err)
			}
			report, err := ReconcileExpansionAuthorityOrphans()
			if err != nil || report.Finalized != 1 {
				t.Fatalf("adaptation reconciliation report=%+v err=%v", report, err)
			}
			if _, err := os.Stat(recordPath); err != nil {
				t.Fatalf("adaptation owner capability was removed: %v", err)
			}
		})
	}
}

func snapshotFormalWriterFiles(t *testing.T, root string) map[string]string {
	t.Helper()
	result := make(map[string]string)
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if rel != "meta/progress.json" && rel != "outline.json" && rel != "outline.md" &&
			rel != "layered_outline.json" && rel != "layered_outline.md" && !strings.HasPrefix(rel, structureRootDir+"/") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		result[rel] = string(data)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

type authorityRegistryFileSnapshot struct {
	Data   string
	Mode   fs.FileMode
	Digest string
}

func snapshotExpansionAuthorityRegistry(t *testing.T) map[string]authorityRegistryFileSnapshot {
	t.Helper()
	root, err := expansionAuthorityRootDir()
	if err != nil {
		t.Fatal(err)
	}
	projects := filepath.Join(root, "projects")
	result := make(map[string]authorityRegistryFileSnapshot)
	err = filepath.WalkDir(projects, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if os.IsNotExist(walkErr) {
				return nil
			}
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		result[filepath.Base(path)] = authorityRegistryFileSnapshot{Data: string(data), Mode: info.Mode().Perm(), Digest: domain.ContentSignature(data)}
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return result
}

func seedNormalPublicationSentinels(t *testing.T, root string) {
	t.Helper()
	for rel, content := range map[string]string{
		adaptationAuditReportFile:             `{"status":"sentinel-audit"}`,
		adaptationRevisionRuntimeFile:         `{"status":"sentinel-runtime"}`,
		adaptationRevisionServiceReceiptsFile: `{"version":1,"receipts":{"sentinel":{}}}`,
		continuationCommitJournalFile:         `{"stage":"sentinel-journal"}`,
	} {
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func snapshotNormalPublicationProjectFiles(t *testing.T, root string) map[string]string {
	t.Helper()
	result := make(map[string]string)
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if strings.HasSuffix(rel, ".lock") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		result[rel] = string(data)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func assertFormalStructure(t *testing.T, st *Store, want []domain.VolumeOutline, total int) {
	t.Helper()
	got, err := st.Outline.LoadLayeredOutline()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("formal structure mismatch\ngot=%+v\nwant=%+v", got, want)
	}
	progress, err := st.Progress.Load()
	if err != nil {
		t.Fatal(err)
	}
	if progress == nil || progress.TotalChapters != total {
		t.Fatalf("formal progress=%+v want total=%d", progress, total)
	}
}
