package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain"
)

type fakeRevisionPolicy struct{}

func (fakeRevisionPolicy) Identity() (string, string) { return "test.revision", "1" }

func (fakeRevisionPolicy) Mode() domain.RevisionMode { return "fake" }

func (fakeRevisionPolicy) ApprovalStages(domain.RevisionImpact) ([]domain.RevisionApprovalStage, error) {
	return []domain.RevisionApprovalStage{
		{ID: "volume", Label: "Volume structure"},
		{ID: "outline", Label: "Detailed outline"},
		{ID: "prose", Label: "Prose"},
	}, nil
}

type reentrantRevisionPolicy struct {
	store     *RevisionStore
	callbacks int
}

func (p *reentrantRevisionPolicy) Identity() (string, string) { return "test.reentrant", "7" }
func (p *reentrantRevisionPolicy) Mode() domain.RevisionMode  { return "fake" }
func (p *reentrantRevisionPolicy) observe() {
	p.callbacks++
	_, _ = p.store.Active()
}
func (p *reentrantRevisionPolicy) ApprovalStages(impact domain.RevisionImpact) ([]domain.RevisionApprovalStage, error) {
	p.observe()
	impact.Items[0].DependencyEvidence[0] = "mutated"
	return []domain.RevisionApprovalStage{{ID: "publish", Label: "Publish"}}, nil
}
func (p *reentrantRevisionPolicy) ValidateImpact(impact domain.RevisionImpact) error {
	p.observe()
	impact.Items[0].Change = "mutated"
	return nil
}
func (p *reentrantRevisionPolicy) ValidateCandidate(session domain.RevisionSession, versions []domain.ArtifactVersion) error {
	p.observe()
	session.Impact.Items[0].Change = "mutated"
	versions[0].Payload[0] = 'x'
	return nil
}
func (p *reentrantRevisionPolicy) Route(session domain.RevisionSession) (*domain.RevisionRoute, error) {
	p.observe()
	session.Impact.Items[0].Change = "mutated"
	return &domain.RevisionRoute{Agent: "revision", Task: "continue"}, nil
}

func (fakeRevisionPolicy) ValidateImpact(impact domain.RevisionImpact) error {
	return impact.Validate()
}

func (fakeRevisionPolicy) ValidateCandidate(session domain.RevisionSession, versions []domain.ArtifactVersion) error {
	if len(versions) == 0 {
		return errors.New("fake policy requires a candidate")
	}
	for _, version := range versions {
		if err := version.Validate(); err != nil {
			return err
		}
	}
	return nil
}

func (fakeRevisionPolicy) Route(session domain.RevisionSession) (*domain.RevisionRoute, error) {
	switch session.Stage {
	case domain.RevisionStageCandidateGenerating:
		return &domain.RevisionRoute{Agent: "revision_writer", Task: "generate isolated candidate", Reason: "fake candidate route"}, nil
	case domain.RevisionStageCandidateAudit:
		return &domain.RevisionRoute{Agent: "revision_auditor", Task: "audit isolated candidate", Reason: "fake audit route"}, nil
	default:
		// Deliberately return an unsafe route so the shared engine test proves
		// manual approval, pause, failure, and terminal stages force it to nil.
		return &domain.RevisionRoute{Agent: "unsafe", Task: "must be ignored"}, nil
	}
}

type panicRevisionPolicy struct{}

func (panicRevisionPolicy) Identity() (string, string) { panic("receipt replay called Identity") }
func (panicRevisionPolicy) Mode() domain.RevisionMode  { panic("receipt replay called Mode") }
func (panicRevisionPolicy) ApprovalStages(domain.RevisionImpact) ([]domain.RevisionApprovalStage, error) {
	panic("receipt replay called ApprovalStages")
}
func (panicRevisionPolicy) ValidateImpact(domain.RevisionImpact) error {
	panic("receipt replay called ValidateImpact")
}
func (panicRevisionPolicy) ValidateCandidate(domain.RevisionSession, []domain.ArtifactVersion) error {
	panic("receipt replay called ValidateCandidate")
}
func (panicRevisionPolicy) Route(domain.RevisionSession) (*domain.RevisionRoute, error) {
	panic("receipt replay called Route")
}

type publishPolicy struct {
	fakeRevisionPolicy
	impactErr error
	stages    []domain.RevisionApprovalStage
}

func (p publishPolicy) ValidateImpact(impact domain.RevisionImpact) error {
	if p.impactErr != nil {
		return p.impactErr
	}
	return p.fakeRevisionPolicy.ValidateImpact(impact)
}

func (p publishPolicy) ApprovalStages(impact domain.RevisionImpact) ([]domain.RevisionApprovalStage, error) {
	if p.stages != nil {
		return append([]domain.RevisionApprovalStage(nil), p.stages...), nil
	}
	return p.fakeRevisionPolicy.ApprovalStages(impact)
}

func TestRevisionStoreCandidateIsolationFeedbackApprovalsAndIdempotency(t *testing.T) {
	dir := t.TempDir()
	canonical := filepath.Join(dir, "chapters", "01.md")
	if err := os.MkdirAll(filepath.Dir(canonical), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(canonical, []byte("canonical draft"), 0o644); err != nil {
		t.Fatal(err)
	}
	policy := fakeRevisionPolicy{}
	store := NewRevisionStore(dir)
	impact := mustRevisionImpact(t)

	session, err := store.Start(policy, StartRevisionInput{Intent: "revise chapter", Impact: impact, IdempotencyKey: "start-1"})
	if err != nil {
		t.Fatal(err)
	}
	if session.Stage != domain.RevisionStageImpactReviewPending || session.Revision != 1 || session.Route != nil {
		t.Fatalf("unexpected initial session: %+v", session)
	}
	if _, err := store.Start(policy, StartRevisionInput{Intent: "another", Impact: impact, IdempotencyKey: "start-2"}); !errors.Is(err, ErrActiveRevisionExists) {
		t.Fatalf("second active revision error = %v", err)
	}

	session = mustApproveImpact(t, store, policy, session, "impact-1")
	if session.Stage != domain.RevisionStageCandidateGenerating || session.Route == nil || session.Route.Agent != "revision_writer" {
		t.Fatalf("impact approval did not enter candidate generation: %+v", session)
	}
	if _, err := store.ApproveImpact(policy, RevisionMutationInput{SessionID: session.ID, ExpectedRevision: 1, IdempotencyKey: "stale-impact"}); !IsRevisionConflict(err) {
		t.Fatalf("stale expected_revision error = %v", err)
	}

	first := mustSubmitCandidate(t, store, policy, session, "candidate-1", "first candidate")
	firstVersionID := first.CandidateVersionIDs[0]
	if current, err := store.CurrentVersion("chapter-1"); err != nil || current != nil {
		t.Fatalf("candidate leaked into current version: current=%+v err=%v", current, err)
	}
	assertCanonicalDraft(t, canonical)

	failedAudit, err := store.RecordAudit(policy, RevisionAuditInput{
		RevisionMutationInput: RevisionMutationInput{SessionID: first.ID, ExpectedRevision: first.Revision, IdempotencyKey: "audit-1"},
		CandidateSignature:    first.CandidateSignature, Passed: false, Report: "needs another round",
	})
	if err != nil {
		t.Fatal(err)
	}
	if failedAudit.Stage != domain.RevisionStageCandidateGenerating || failedAudit.Round != 2 || len(failedAudit.CandidateVersionIDs) != 0 {
		t.Fatalf("failed audit did not preserve a recoverable next round: %+v", failedAudit)
	}
	if _, err := store.LoadVersion(firstVersionID); err != nil {
		t.Fatalf("rejected candidate history was lost: %v", err)
	}

	second := mustSubmitCandidate(t, store, policy, failedAudit, "candidate-2", "second candidate")
	audited, err := store.RecordAudit(policy, RevisionAuditInput{
		RevisionMutationInput: RevisionMutationInput{SessionID: second.ID, ExpectedRevision: second.Revision, IdempotencyKey: "audit-2"},
		CandidateSignature:    second.CandidateSignature, Passed: true, Report: "pass",
	})
	if err != nil {
		t.Fatal(err)
	}
	if audited.Route != nil {
		t.Fatalf("manual approval boundary retained a policy route: %+v", audited.Route)
	}
	feedbackRound, err := store.SubmitFeedback(policy, RevisionFeedbackInput{
		RevisionMutationInput: RevisionMutationInput{SessionID: audited.ID, ExpectedRevision: audited.Revision, IdempotencyKey: "feedback-2"},
		StageID:               "volume", Message: "strengthen the volume consequence",
	})
	if err != nil {
		t.Fatal(err)
	}
	if feedbackRound.Stage != domain.RevisionStageCandidateGenerating || feedbackRound.Round != 3 || len(feedbackRound.Feedback) != 1 {
		t.Fatalf("multi-round feedback state = %+v", feedbackRound)
	}
	third := mustSubmitCandidate(t, store, policy, feedbackRound, "candidate-3", "third candidate")
	audited, err = store.RecordAudit(policy, RevisionAuditInput{
		RevisionMutationInput: RevisionMutationInput{SessionID: third.ID, ExpectedRevision: third.Revision, IdempotencyKey: "audit-3"},
		CandidateSignature:    third.CandidateSignature, Passed: true, Report: "pass after feedback",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Publish(policy, RevisionMutationInput{SessionID: audited.ID, ExpectedRevision: audited.Revision, IdempotencyKey: "publish-too-early"}); err == nil {
		t.Fatal("publish crossed staged human approvals")
	}
	if _, err := store.ApproveStage(policy, RevisionApprovalInput{
		RevisionMutationInput: RevisionMutationInput{SessionID: audited.ID, ExpectedRevision: audited.Revision, IdempotencyKey: "wrong-stage"},
		StageID:               "outline",
	}); err == nil {
		t.Fatal("out-of-order stage approval succeeded")
	}

	approved := audited
	for _, stage := range []string{"volume", "outline", "prose"} {
		approved, err = store.ApproveStage(policy, RevisionApprovalInput{
			RevisionMutationInput: RevisionMutationInput{SessionID: approved.ID, ExpectedRevision: approved.Revision, IdempotencyKey: "approve-" + stage},
			StageID:               stage,
		})
		if err != nil {
			t.Fatalf("approve %s: %v", stage, err)
		}
		assertCanonicalDraft(t, canonical)
	}
	if approved.Stage != domain.RevisionStageReadyToPublish {
		t.Fatalf("stage after approvals = %q", approved.Stage)
	}
	published, err := store.Publish(policy, RevisionMutationInput{SessionID: approved.ID, ExpectedRevision: approved.Revision, IdempotencyKey: "publish-1"})
	if err != nil {
		t.Fatal(err)
	}
	if published.Stage != domain.RevisionStageCompleted {
		t.Fatalf("publish stage = %q", published.Stage)
	}
	current, err := store.CurrentVersion("chapter-1")
	if err != nil || current == nil || string(current.Payload) != `"third candidate"` {
		t.Fatalf("published current version = %+v err=%v", current, err)
	}
	assertCanonicalDraft(t, canonical)

	// A delayed exact retry returns the original receipt even after the session
	// has advanced, while reusing the key for another payload is rejected.
	replayed, err := store.Start(policy, StartRevisionInput{Intent: "revise chapter", Impact: impact, IdempotencyKey: "start-1"})
	if err != nil || replayed.Revision != 1 || replayed.ID != published.ID {
		t.Fatalf("delayed idempotent start = %+v err=%v", replayed, err)
	}
	if _, err := store.Start(policy, StartRevisionInput{Intent: "changed payload", Impact: impact, IdempotencyKey: "start-1"}); !errors.Is(err, ErrRevisionIdempotencyConflict) {
		t.Fatalf("idempotency conflict error = %v", err)
	}
	versions, err := store.ListVersions("chapter-1")
	if err != nil || len(versions) != 3 {
		t.Fatalf("version history len=%d err=%v", len(versions), err)
	}
}

func TestRevisionStorePauseFailureRecoveryCancellationAndHistoricalRestore(t *testing.T) {
	dir := t.TempDir()
	policy := fakeRevisionPolicy{}
	store := NewRevisionStore(dir)
	first, err := store.Start(policy, StartRevisionInput{Intent: "first", Impact: mustRevisionImpact(t), IdempotencyKey: "start-first"})
	if err != nil {
		t.Fatal(err)
	}
	first = mustApproveImpact(t, store, policy, first, "approve-first-impact")
	first = mustSubmitCandidate(t, store, policy, first, "first-candidate", "historical content")
	first = mustPassAndApproveAll(t, store, policy, first, "first")
	first, err = store.Publish(policy, RevisionMutationInput{SessionID: first.ID, ExpectedRevision: first.Revision, IdempotencyKey: "publish-first"})
	if err != nil {
		t.Fatal(err)
	}
	historicalID := first.CandidateVersionIDs[0]

	second, err := store.Start(policy, StartRevisionInput{Intent: "recoverable", Impact: mustRevisionImpact(t), IdempotencyKey: "start-second"})
	if err != nil {
		t.Fatal(err)
	}
	second = mustApproveImpact(t, store, policy, second, "approve-second-impact")
	failed, err := store.Fail(policy, RevisionFailureInput{
		RevisionMutationInput: RevisionMutationInput{SessionID: second.ID, ExpectedRevision: second.Revision, IdempotencyKey: "fail-second"},
		Error:                 "fake model interruption",
	})
	if err != nil {
		t.Fatal(err)
	}
	if failed.Stage != domain.RevisionStageFailed || failed.ResumeStage != domain.RevisionStageCandidateGenerating {
		t.Fatalf("failed state = %+v", failed)
	}

	// A new store instance simulates process restart; durable state remains the
	// only source of truth and resumes at the exact failed stage.
	restarted := NewRevisionStore(dir)
	active, err := restarted.Active()
	if err != nil || active == nil || active.ID != second.ID || active.Stage != domain.RevisionStageFailed {
		t.Fatalf("active after restart = %+v err=%v", active, err)
	}
	resumed, err := restarted.Resume(policy, RevisionMutationInput{SessionID: active.ID, ExpectedRevision: active.Revision, IdempotencyKey: "resume-second"})
	if err != nil || resumed.Stage != domain.RevisionStageCandidateGenerating || resumed.Route == nil {
		t.Fatalf("resumed = %+v err=%v", resumed, err)
	}
	paused, err := restarted.Pause(policy, RevisionMutationInput{SessionID: resumed.ID, ExpectedRevision: resumed.Revision, IdempotencyKey: "pause-second"})
	if err != nil || paused.Stage != domain.RevisionStagePaused {
		t.Fatalf("paused = %+v err=%v", paused, err)
	}
	cancelled, err := restarted.Cancel(policy, RevisionMutationInput{SessionID: paused.ID, ExpectedRevision: paused.Revision, IdempotencyKey: "cancel-second"})
	if err != nil || cancelled.Stage != domain.RevisionStageCancelled {
		t.Fatalf("cancelled = %+v err=%v", cancelled, err)
	}

	restored, err := restarted.RestoreVersion(policy, RestoreArtifactVersionInput{
		VersionID: historicalID, Intent: "restore old version", IdempotencyKey: "restore-historical",
	})
	if err != nil {
		t.Fatal(err)
	}
	if restored.ID == first.ID || restored.Stage != domain.RevisionStageImpactReviewPending || restored.RestoresVersionID != historicalID {
		t.Fatalf("historical restore did not create a new revision: %+v", restored)
	}
	if len(restored.CandidateVersionIDs) != 1 || restored.CandidateVersionIDs[0] == historicalID {
		t.Fatalf("restore reused historical version instead of isolating a candidate: %+v", restored.CandidateVersionIDs)
	}
	current, err := restarted.CurrentVersion("chapter-1")
	if err != nil || current == nil || current.ID != historicalID {
		t.Fatalf("restore changed current artifact before review: %+v err=%v", current, err)
	}
	restored, err = restarted.ApproveImpact(policy, RevisionMutationInput{SessionID: restored.ID, ExpectedRevision: restored.Revision, IdempotencyKey: "approve-restore-impact"})
	if err != nil || restored.Stage != domain.RevisionStageCandidateAudit {
		t.Fatalf("restore impact approval stage = %+v err=%v", restored, err)
	}
}

func TestRevisionStoreSerializesSingleActiveRevisionAcrossInstances(t *testing.T) {
	dir := t.TempDir()
	policy := fakeRevisionPolicy{}
	stores := []*RevisionStore{NewRevisionStore(dir), NewRevisionStore(dir)}
	impact := mustRevisionImpact(t)
	var wg sync.WaitGroup
	errs := make(chan error, len(stores))
	for index, store := range stores {
		wg.Add(1)
		go func(index int, store *RevisionStore) {
			defer wg.Done()
			_, err := store.Start(policy, StartRevisionInput{
				Intent: "concurrent", Impact: impact, IdempotencyKey: string(rune('a' + index)),
			})
			errs <- err
		}(index, store)
	}
	wg.Wait()
	close(errs)
	succeeded, blocked := 0, 0
	for err := range errs {
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, ErrActiveRevisionExists):
			blocked++
		default:
			t.Fatalf("unexpected concurrent start error: %v", err)
		}
	}
	if succeeded != 1 || blocked != 1 {
		t.Fatalf("concurrent starts: succeeded=%d blocked=%d", succeeded, blocked)
	}
}

func TestRevisionStoreMultiProcessCanonicalTransaction(t *testing.T) {
	if dir := os.Getenv("AINOVEL_REVISION_HELPER_DIR"); dir != "" {
		gate := os.Getenv("AINOVEL_REVISION_HELPER_GATE")
		deadline := time.Now().Add(10 * time.Second)
		for {
			if _, err := os.Stat(gate); err == nil {
				break
			}
			if time.Now().After(deadline) {
				fmt.Println("ERR gate timeout")
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
		impact, _ := domain.NewRevisionImpact("process race", []domain.RevisionImpactItem{{
			ArtifactID: "chapter-1", ArtifactKind: "prose", Change: "rewrite",
		}})
		_, err := NewRevisionStore(dir).Start(fakeRevisionPolicy{}, StartRevisionInput{
			Intent: "race", Impact: impact, IdempotencyKey: os.Getenv("AINOVEL_REVISION_HELPER_KEY"),
		})
		switch {
		case err == nil:
			fmt.Println("RESULT OK")
		case errors.Is(err, ErrActiveRevisionExists):
			fmt.Println("RESULT ACTIVE")
		default:
			fmt.Println("RESULT ERR", err)
		}
		return
	}
	dir := t.TempDir()
	gate := filepath.Join(dir, "start.gate")
	run := func(path, key string) <-chan string {
		result := make(chan string, 1)
		go func() {
			cmd := exec.Command(os.Args[0], "-test.run=^TestRevisionStoreMultiProcessCanonicalTransaction$")
			cmd.Env = append(os.Environ(),
				"AINOVEL_REVISION_HELPER_DIR="+path,
				"AINOVEL_REVISION_HELPER_GATE="+gate,
				"AINOVEL_REVISION_HELPER_KEY="+key,
			)
			output, err := cmd.CombinedOutput()
			if err != nil {
				result <- "command error: " + err.Error() + ": " + string(output)
				return
			}
			result <- string(output)
		}()
		return result
	}
	first := run(dir, "process-a")
	second := run(filepath.Join(dir, "."), "process-b")
	time.Sleep(100 * time.Millisecond)
	if err := os.WriteFile(gate, []byte("go"), 0o644); err != nil {
		t.Fatal(err)
	}
	outputs := []string{<-first, <-second}
	ok, active := 0, 0
	for _, output := range outputs {
		if strings.Contains(output, "RESULT OK") {
			ok++
		}
		if strings.Contains(output, "RESULT ACTIVE") {
			active++
		}
		if strings.Contains(output, "RESULT ERR") || strings.Contains(output, "command error") {
			t.Fatalf("helper failed: %s", output)
		}
	}
	if ok != 1 || active != 1 {
		t.Fatalf("multi-process results = %#v, want one OK and one ACTIVE", outputs)
	}
	session, err := NewRevisionStore(dir).Active()
	if err != nil || session == nil || session.ID != "rev-000001" {
		t.Fatalf("durable active session = %#v, err=%v", session, err)
	}
}

func TestRevisionPolicyCallbacksAreReentrantDeepCopiedAndReceiptFirst(t *testing.T) {
	store := NewRevisionStore(t.TempDir())
	policy := &reentrantRevisionPolicy{store: store}
	impact := mustRevisionImpact(t)
	done := make(chan struct{})
	var session *domain.RevisionSession
	var startErr error
	go func() {
		session, startErr = store.Start(policy, StartRevisionInput{Intent: "safe policy", Impact: impact, IdempotencyKey: "reentrant-start"})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("policy callback deadlocked while re-entering RevisionStore")
	}
	if startErr != nil {
		t.Fatal(startErr)
	}
	callbacks := policy.callbacks
	replayed, err := store.Start(policy, StartRevisionInput{Intent: "safe policy", Impact: impact, IdempotencyKey: "reentrant-start"})
	if err != nil || replayed.ID != session.ID {
		t.Fatalf("replay = %#v, err=%v", replayed, err)
	}
	if policy.callbacks != callbacks {
		t.Fatalf("idempotent receipt invoked policy callbacks: before=%d after=%d", callbacks, policy.callbacks)
	}
	if session.Impact.Items[0].Change != "rewrite chapter" || session.Impact.Items[0].DependencyEvidence[0] != "outline chapter-1" {
		t.Fatalf("policy mutated persisted impact: %+v", session.Impact)
	}
	approved, err := store.ApproveImpact(policy, RevisionMutationInput{SessionID: session.ID, ExpectedRevision: session.Revision, IdempotencyKey: "reentrant-approve"})
	if err != nil {
		t.Fatal(err)
	}
	payload := json.RawMessage(`"candidate"`)
	candidate, err := store.SubmitCandidate(policy, SubmitRevisionCandidateInput{
		SessionID: approved.ID, ExpectedRevision: approved.Revision, IdempotencyKey: "reentrant-candidate",
		Artifacts: []CandidateArtifactInput{{ArtifactID: "chapter-1", ArtifactKind: "prose", Payload: payload}},
	})
	if err != nil {
		t.Fatal(err)
	}
	version, err := store.LoadVersion(candidate.CandidateVersionIDs[0])
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(version.Payload) || string(version.Payload) != string(payload) {
		t.Fatalf("policy mutated persisted payload: %q", version.Payload)
	}
	if candidate.PolicyID != "test.reentrant" || candidate.PolicyVersion != "7" {
		t.Fatalf("policy identity was not persisted: %+v", candidate)
	}
}

func TestExactReceiptReplayCallsNoPolicyMethodIncludingModeAndIdentity(t *testing.T) {
	store := NewRevisionStore(t.TempDir())
	impact := mustRevisionImpact(t)
	started, err := store.Start(fakeRevisionPolicy{}, StartRevisionInput{
		Intent: "receipt first", Impact: impact, IdempotencyKey: "receipt-start",
	})
	if err != nil {
		t.Fatal(err)
	}
	if replayed, err := store.Start(panicRevisionPolicy{}, StartRevisionInput{
		Intent: "receipt first", Impact: impact, IdempotencyKey: "receipt-start",
	}); err != nil || replayed.ID != started.ID {
		t.Fatalf("start receipt replay = %+v, err=%v", replayed, err)
	}
	approved, err := store.ApproveImpact(fakeRevisionPolicy{}, RevisionMutationInput{
		SessionID: started.ID, ExpectedRevision: started.Revision, IdempotencyKey: "receipt-mutation",
	})
	if err != nil {
		t.Fatal(err)
	}
	if replayed, err := store.ApproveImpact(panicRevisionPolicy{}, RevisionMutationInput{
		SessionID: started.ID, ExpectedRevision: started.Revision, IdempotencyKey: "receipt-mutation",
	}); err != nil || replayed.Revision != approved.Revision {
		t.Fatalf("mutation receipt replay = %+v, err=%v", replayed, err)
	}
}

func TestRevisionTransactionDoesNotStealLiveOwnerFromOldMtime(t *testing.T) {
	dir := t.TempDir()
	first := NewRevisionStore(dir)
	second := &RevisionStore{io: newIO(dir), mu: &sync.Mutex{}}
	entered := make(chan struct{})
	release := make(chan struct{})
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- first.withRevisionTransaction(func() error {
			close(entered)
			<-release
			return nil
		})
	}()
	<-entered
	lockPath := filepath.Join(dir, filepath.FromSlash(revisionLockFile))
	old := time.Now().Add(-24 * time.Hour)
	if err := os.Chtimes(lockPath, old, old); err != nil {
		t.Fatal(err)
	}
	secondEntered := make(chan struct{})
	secondDone := make(chan error, 1)
	go func() {
		secondDone <- second.withRevisionTransaction(func() error {
			close(secondEntered)
			return nil
		})
	}()
	select {
	case <-secondEntered:
		t.Fatal("old mtime allowed a live revision transaction to be stolen")
	case <-time.After(150 * time.Millisecond):
	}
	close(release)
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
	select {
	case <-secondEntered:
	case <-time.After(3 * time.Second):
		t.Fatal("successor did not acquire the OS lock after owner release")
	}
	if err := <-secondDone; err != nil {
		t.Fatal(err)
	}
}

func TestRevisionRejectsEmptyStagesAndPublishRevalidatesCurrentPolicy(t *testing.T) {
	dir := t.TempDir()
	store := NewRevisionStore(dir)
	empty := publishPolicy{stages: []domain.RevisionApprovalStage{}}
	if _, err := store.Start(empty, StartRevisionInput{
		Intent: "empty stages", Impact: mustRevisionImpact(t), IdempotencyKey: "empty-stage-start",
	}); err == nil || !strings.Contains(err.Error(), "at least one approval stage") {
		t.Fatalf("empty approval stages were accepted: %v", err)
	}

	session, err := store.Start(fakeRevisionPolicy{}, StartRevisionInput{
		Intent: "publish policy", Impact: mustRevisionImpact(t), IdempotencyKey: "publish-policy-start",
	})
	if err != nil {
		t.Fatal(err)
	}
	session = mustApproveImpact(t, store, fakeRevisionPolicy{}, session, "publish-policy-impact")
	session = mustSubmitCandidate(t, store, fakeRevisionPolicy{}, session, "publish-policy-candidate", "candidate")
	session = mustPassAndApproveAll(t, store, fakeRevisionPolicy{}, session, "publish-policy")
	invalid := publishPolicy{impactErr: errors.New("impact no longer allowed")}
	if _, err := store.Publish(invalid, RevisionMutationInput{
		SessionID: session.ID, ExpectedRevision: session.Revision, IdempotencyKey: "publish-policy-invalid",
	}); err == nil || !strings.Contains(err.Error(), "impact no longer allowed") {
		t.Fatalf("publish accepted policy-invalid persisted impact: %v", err)
	}
	changedStages := publishPolicy{stages: []domain.RevisionApprovalStage{{ID: "replacement", Label: "Replacement"}}}
	if _, err := store.Publish(changedStages, RevisionMutationInput{
		SessionID: session.ID, ExpectedRevision: session.Revision, IdempotencyKey: "publish-policy-stages",
	}); err == nil || !strings.Contains(err.Error(), "approval stages changed") {
		t.Fatalf("publish accepted changed policy stages: %v", err)
	}
}

func TestRevisionRestartValidationRejectsStateJump(t *testing.T) {
	dir := t.TempDir()
	state := newRevisionState()
	state.Generation = 9
	impact := mustRevisionImpact(t)
	state.Sessions["rev-000001"] = domain.RevisionSession{
		Version: domain.RevisionSchemaVersion, ID: "rev-000001", Mode: "fake",
		Stage: domain.RevisionStageReadyToPublish, Revision: 4, Generation: 9,
		PolicyID: "test.revision", PolicyVersion: "1", Intent: "crafted", Impact: impact,
		ApprovalStages: []domain.RevisionApprovalStage{{ID: "publish", Label: "Publish"}},
		Round:          2, CreatedAt: domain.RevisionTimestamp(), UpdatedAt: domain.RevisionTimestamp(),
	}
	state.ActiveSessionID = "rev-000001"
	if err := newIO(dir).WriteJSON(revisionStateFile, state); err != nil {
		t.Fatal(err)
	}
	if _, err := NewRevisionStore(dir).Active(); err == nil || !strings.Contains(err.Error(), "non-empty candidate") {
		t.Fatalf("crafted ready_to_publish state was accepted: %v", err)
	}
}

func TestRevisionRestartValidationRejectsEmptyApprovalStages(t *testing.T) {
	dir := t.TempDir()
	state := newRevisionState()
	state.Generation = 3
	state.Sessions["rev-000001"] = domain.RevisionSession{
		Version: domain.RevisionSchemaVersion, ID: "rev-000001", Mode: "fake",
		Stage: domain.RevisionStageImpactReviewPending, Revision: 1, Generation: 3,
		PolicyID: "test.revision", PolicyVersion: "1", Intent: "crafted", Impact: mustRevisionImpact(t),
		ApprovalStages: nil, Round: 1, CreatedAt: domain.RevisionTimestamp(), UpdatedAt: domain.RevisionTimestamp(),
	}
	state.ActiveSessionID = "rev-000001"
	if err := newIO(dir).WriteJSON(revisionStateFile, state); err != nil {
		t.Fatal(err)
	}
	if _, err := NewRevisionStore(dir).Active(); err == nil || !strings.Contains(err.Error(), "must not be empty") {
		t.Fatalf("persisted empty approval stages were accepted: %v", err)
	}
}

func TestNormalFlowLeaseFencesRevisionStartAndStaleSideEffects(t *testing.T) {
	dir := t.TempDir()
	first := NewRevisionStore(dir)
	alias := NewRevisionStore(filepath.Join(dir, "."))
	lease, err := first.AcquireNormalFlow("host-test")
	if err != nil {
		t.Fatal(err)
	}
	fence, err := first.FenceForNormalFlow(lease.Token)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := alias.Start(fakeRevisionPolicy{}, StartRevisionInput{
		Intent: "must wait", Impact: mustRevisionImpact(t), IdempotencyKey: "lease-blocked-start",
	}); !errors.Is(err, ErrActiveRevisionExists) {
		t.Fatalf("revision start while normal work is leased = %v", err)
	}
	if err := first.ReleaseNormalFlow(lease.Token); err != nil {
		t.Fatal(err)
	}
	called := false
	if err := first.WithFence(fence, func() error { called = true; return nil }); err == nil {
		t.Fatal("stale normal-flow fence was accepted")
	}
	if called {
		t.Fatal("stale fenced side effect ran")
	}
	if _, err := alias.Start(fakeRevisionPolicy{}, StartRevisionInput{
		Intent: "after stop", Impact: mustRevisionImpact(t), IdempotencyKey: "lease-released-start",
	}); err != nil {
		t.Fatalf("revision start after normal work stopped: %v", err)
	}
}

func TestRevisionCancelPermanentlyFencesQueuedRouteBeforeReleasingOwnership(t *testing.T) {
	store := NewRevisionStore(t.TempDir())
	policy := fakeRevisionPolicy{}
	session, err := store.Start(policy, StartRevisionInput{
		Intent: "cancel queued route", Impact: mustRevisionImpact(t), IdempotencyKey: "cancel-route-start",
	})
	if err != nil {
		t.Fatal(err)
	}
	session = mustApproveImpact(t, store, policy, session, "cancel-route-approve")
	if session.Route == nil {
		t.Fatal("candidate generation route is missing")
	}
	fence := RevisionFence{
		Generation: session.Route.Generation,
		SessionID:  session.Route.SessionID,
		Revision:   session.Route.Revision,
	}
	if _, err := store.Cancel(policy, RevisionMutationInput{
		SessionID: session.ID, ExpectedRevision: session.Revision, IdempotencyKey: "cancel-route",
	}); err != nil {
		t.Fatal(err)
	}
	called := false
	if err := store.WithFence(fence, func() error { called = true; return nil }); err == nil {
		t.Fatal("cancelled revision route retained write authority")
	}
	if called {
		t.Fatal("queued route side effect ran after cancellation")
	}
	if active, err := store.Active(); err != nil || active != nil {
		t.Fatalf("cancel did not release active ownership: active=%+v err=%v", active, err)
	}
}

func TestRevisionAcceptsRepeatedFeedbackWhileCandidateIsGenerating(t *testing.T) {
	store := NewRevisionStore(t.TempDir())
	policy := fakeRevisionPolicy{}
	session, err := store.Start(policy, StartRevisionInput{
		Intent: "multi-round candidate", Impact: mustRevisionImpact(t), IdempotencyKey: "feedback-start",
	})
	if err != nil {
		t.Fatal(err)
	}
	session = mustApproveImpact(t, store, policy, session, "feedback-impact")
	for index, message := range []string{"make the reversal earlier", "keep the final choice irreversible"} {
		session, err = store.SubmitFeedback(policy, RevisionFeedbackInput{
			RevisionMutationInput: RevisionMutationInput{
				SessionID: session.ID, ExpectedRevision: session.Revision,
				IdempotencyKey: fmt.Sprintf("feedback-%d", index+1),
			},
			Message: message,
		})
		if err != nil {
			t.Fatalf("feedback %d: %v", index+1, err)
		}
		if session.Stage != domain.RevisionStageCandidateGenerating || session.Route == nil {
			t.Fatalf("feedback %d locked the candidate: %+v", index+1, session)
		}
	}
	if session.Round != 3 || len(session.Feedback) != 2 {
		t.Fatalf("multi-round feedback state = %+v", session)
	}
}

func TestNormalRevisionStagesStructureBeforeOutlineAndMinimalProse(t *testing.T) {
	store := NewRevisionStore(t.TempDir())
	policy := domain.NormalRevisionPolicy{}
	volumeID := domain.LegacyStructureID("store-normal-stage", domain.StructureKindVolume, "final")
	arcID := domain.LegacyStructureID("store-normal-stage", domain.StructureKindArc, "final")
	chapterID := domain.LegacyStructureID("store-normal-stage", domain.StructureKindChapter, "affected")
	impact, err := domain.NewRevisionImpact("insert a final volume", []domain.RevisionImpactItem{
		{
			ArtifactID: volumeID, ArtifactKind: domain.StructureKindVolume, Change: "insert final volume",
			Requirement: domain.StructureImpactRequired, Cause: domain.StructureImpactStructureChange,
			DependencyEvidence: []string{"the existing ending opens a separate final conflict"},
		},
		{
			ArtifactID: chapterID, ArtifactKind: domain.StructureKindChapter, Change: "coordinate and rewrite one affected chapter",
			Requirement: domain.StructureImpactRequired, Cause: domain.StructureImpactContentDependency, RequiresBodyRewrite: true,
			DependencyEvidence: []string{"the inserted volume changes this chapter's irreversible exit"},
		},
		{
			ArtifactID: "batch-plan", ArtifactKind: domain.NormalArtifactBatchPlan, Change: "bound generation and review scope",
			Requirement: domain.StructureImpactRequired, Cause: domain.StructureImpactContentDependency,
			DependencyEvidence: []string{"the affected chapter must be generated in a bounded batch"},
		},
		{ArtifactID: domain.NormalStructureSnapshotID, ArtifactKind: domain.NormalArtifactStructureSnapshot, Change: "bind snapshot", Requirement: domain.StructureImpactRequired, Cause: domain.StructureImpactContentDependency, DependencyEvidence: []string{"accepted version"}},
		{ArtifactID: "rework:" + chapterID, ArtifactKind: domain.NormalArtifactProseReworkIntent, Change: "queue exact rework", Requirement: domain.StructureImpactRequired, Cause: domain.StructureImpactContentDependency, DependencyEvidence: []string{"stable target"}},
		{ArtifactID: domain.NormalProseReworkQueueID, ArtifactKind: domain.NormalArtifactProseReworkQueue, Change: "queue slots", Requirement: domain.StructureImpactRequired, Cause: domain.StructureImpactContentDependency, DependencyEvidence: []string{"PR-06 boundary"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	session, err := store.Start(policy, StartRevisionInput{Intent: "completed-book expansion", Impact: impact, IdempotencyKey: "normal-stage-start"})
	if err != nil {
		t.Fatal(err)
	}
	session, err = store.ApproveImpact(policy, RevisionMutationInput{SessionID: session.ID, ExpectedRevision: session.Revision, IdempotencyKey: "normal-stage-impact"})
	if err != nil {
		t.Fatal(err)
	}

	snapshot := fmt.Sprintf(`[{"id":%q,"index":1,"title":"Final","arcs":[{"id":%q,"index":1,"title":"Arc","chapters":[{"id":%q,"chapter":1,"title":"Coordinated","core_event":"choice","hook":"cost","scenes":["choose"]}]}]}]`, volumeID, arcID, chapterID)
	plan := fmt.Sprintf(`{"batches":[{"id":"batch-001","index":1,"chapter_ids":[%q],"volume_id":%q,"arc_id":%q,"estimated_output_words":3000,"status":"pending"}],"volume_reviews":[{"scope_id":%q,"status":"pending"}],"whole_book_review":{"scope_id":"whole-book","status":"pending"}}`, chapterID, volumeID, arcID, volumeID)
	submitStage := func(prefix, stage string) {
		t.Helper()
		var artifacts []CandidateArtifactInput
		switch stage {
		case domain.NormalApprovalStructure:
			artifacts = []CandidateArtifactInput{
				{ArtifactID: volumeID, ArtifactKind: domain.StructureKindVolume, Payload: json.RawMessage(`{"entry_state":"old ending","independent_conflict":"succession","arc_progression":"alliances fracture","climax":"capital falls","irreversible_outcome":"realm divides","cannot_fit_current_volume":"prior phase paid off","soft_budget":{"estimated_chapters":1,"chapter_min_words":3000,"chapter_max_words":5000,"target_total_words":4000,"total_min_words":3000,"total_max_words":5000}}`)},
				{ArtifactID: domain.NormalStructureSnapshotID, ArtifactKind: domain.NormalArtifactStructureSnapshot, Payload: json.RawMessage(snapshot)},
			}
		case domain.NormalApprovalOutline:
			detail := fmt.Sprintf(`{"chapter_id":%q,"current_number":1,"volume_id":%q,"arc_id":%q,"outline":{"id":%q,"chapter":1,"title":"Coordinated","core_event":"choice","hook":"cost","scenes":["choose"]}}`, chapterID, volumeID, arcID, chapterID)
			artifacts = []CandidateArtifactInput{
				{ArtifactID: chapterID, ArtifactKind: domain.StructureKindChapter, Payload: json.RawMessage(detail)},
				{ArtifactID: "batch-plan", ArtifactKind: domain.NormalArtifactBatchPlan, Payload: json.RawMessage(plan)},
				{ArtifactID: domain.NormalStructureSnapshotID, ArtifactKind: domain.NormalArtifactStructureSnapshot, Payload: json.RawMessage(snapshot)},
			}
		case domain.NormalApprovalProse:
			intent := fmt.Sprintf(`{"chapter_id":%q,"current_number":1,"volume_id":%q,"arc_id":%q,"reason":"stable dependency"}`, chapterID, volumeID, arcID)
			queue := fmt.Sprintf(`{"chapter_ids":[%q]}`, chapterID)
			artifacts = []CandidateArtifactInput{
				{ArtifactID: "rework:" + chapterID, ArtifactKind: domain.NormalArtifactProseReworkIntent, Payload: json.RawMessage(intent)},
				{ArtifactID: domain.NormalProseReworkQueueID, ArtifactKind: domain.NormalArtifactProseReworkQueue, Payload: json.RawMessage(queue)},
			}
		}
		candidate, submitErr := store.SubmitCandidate(policy, SubmitRevisionCandidateInput{
			SessionID: session.ID, ExpectedRevision: session.Revision, IdempotencyKey: prefix + "-candidate",
			Artifacts: artifacts,
		})
		if submitErr != nil {
			t.Fatalf("%s candidate: %v", stage, submitErr)
		}
		evidence := make([]domain.RevisionAuditEvidence, 0, len(candidate.AuditExpectations))
		for _, expected := range candidate.AuditExpectations {
			evidence = append(evidence, domain.RevisionAuditEvidence{Scope: expected.Scope, ScopeID: expected.ScopeID, FromChapter: expected.FromChapter, ToChapter: expected.ToChapter, ContentSignature: expected.ContentSignature, Passed: true})
		}
		session, submitErr = store.RecordAudit(policy, RevisionAuditInput{
			RevisionMutationInput: RevisionMutationInput{SessionID: candidate.ID, ExpectedRevision: candidate.Revision, IdempotencyKey: prefix + "-audit"},
			CandidateSignature:    candidate.CandidateSignature, Evidence: evidence, Report: "current signatures pass",
		})
		if submitErr != nil {
			t.Fatalf("%s audit: %v", stage, submitErr)
		}
		session, submitErr = store.ApproveStage(policy, RevisionApprovalInput{
			RevisionMutationInput: RevisionMutationInput{SessionID: session.ID, ExpectedRevision: session.Revision, IdempotencyKey: prefix + "-approve"},
			StageID:               stage,
		})
		if submitErr != nil {
			t.Fatalf("%s approval: %v", stage, submitErr)
		}
	}

	submitStage("normal-structure", domain.NormalApprovalStructure)
	if session.Stage != domain.RevisionStageCandidateGenerating || len(session.AcceptedVersionIDs) != 2 || len(session.Approvals) != 1 {
		t.Fatalf("structure approval did not open outline round: %+v", session)
	}
	submitStage("normal-outline", domain.NormalApprovalOutline)
	if session.Stage != domain.RevisionStageCandidateGenerating || len(session.Approvals) != 2 {
		t.Fatalf("outline approval did not open prose round: %+v", session)
	}
	submitStage("normal-prose", domain.NormalApprovalProse)
	if session.Stage != domain.RevisionStageReadyToPublish {
		t.Fatalf("normal revision not ready after ordered stages: %+v", session)
	}
	stateBefore, err := os.ReadFile(filepath.Join(store.io.dir, filepath.FromSlash(revisionStateFile)))
	if err != nil {
		t.Fatal(err)
	}
	input := RevisionMutationInput{SessionID: session.ID, ExpectedRevision: session.Revision, IdempotencyKey: "normal-publish"}
	if _, err := store.Publish(policy, input); !errors.Is(err, ErrRevisionCommandInProgress) {
		t.Fatalf("plain Publish completed staged normal revision: %v", err)
	}
	stateAfter, err := os.ReadFile(filepath.Join(store.io.dir, filepath.FromSlash(revisionStateFile)))
	if err != nil {
		t.Fatal(err)
	}
	if string(stateAfter) != string(stateBefore) {
		t.Fatal("plain normal Publish changed revision state")
	}
	active, err := store.Active()
	if err != nil || active == nil || active.Stage != domain.RevisionStageReadyToPublish {
		t.Fatalf("plain normal Publish consumed lifecycle: active=%+v err=%v", active, err)
	}
	versions, owner, err := store.ValidatePublishWithOwner(policy, input)
	if err != nil || owner == nil || len(versions) == 0 {
		t.Fatalf("owner-bound validation: versions=%d owner=%+v err=%v", len(versions), owner, err)
	}
	for _, id := range []string{volumeID, chapterID, "rework:" + chapterID, domain.NormalProseReworkQueueID} {
		if current, loadErr := store.CurrentVersion(id); loadErr != nil || current != nil {
			t.Fatalf("plain Publish consumed artifact %s: current=%+v err=%v", id, current, loadErr)
		}
	}
}

func TestRevisionStoreRecoversCrashedNormalFlowLease(t *testing.T) {
	dir := t.TempDir()
	state := newRevisionState()
	state.Generation = 4
	state.NormalLease = &NormalFlowLease{
		Token: "crashed", Generation: 4, Owner: "dead-host", PID: 1 << 30, AcquiredAt: domain.RevisionTimestamp(),
	}
	if err := newIO(dir).WriteJSON(revisionStateFile, state); err != nil {
		t.Fatal(err)
	}
	store := NewRevisionStore(dir)
	fence, err := store.SnapshotFence()
	if err != nil {
		t.Fatal(err)
	}
	if fence.LeaseToken != "" || fence.Generation != 5 {
		t.Fatalf("stale lease recovery fence = %+v", fence)
	}
	if _, err := store.Start(fakeRevisionPolicy{}, StartRevisionInput{
		Intent: "after crash", Impact: mustRevisionImpact(t), IdempotencyKey: "after-crash",
	}); err != nil {
		t.Fatalf("start after crashed normal flow: %v", err)
	}
}

func mustRevisionImpact(t *testing.T) domain.RevisionImpact {
	t.Helper()
	impact, err := domain.NewRevisionImpact("chapter impact", []domain.RevisionImpactItem{{
		ArtifactID: "chapter-1", ArtifactKind: "prose", Change: "rewrite chapter",
		DependencyEvidence: []string{"outline chapter-1"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	return impact
}

func mustApproveImpact(t *testing.T, store *RevisionStore, policy fakeRevisionPolicy, session *domain.RevisionSession, key string) *domain.RevisionSession {
	t.Helper()
	next, err := store.ApproveImpact(policy, RevisionMutationInput{SessionID: session.ID, ExpectedRevision: session.Revision, IdempotencyKey: key})
	if err != nil {
		t.Fatal(err)
	}
	return next
}

func mustSubmitCandidate(t *testing.T, store *RevisionStore, policy fakeRevisionPolicy, session *domain.RevisionSession, key, content string) *domain.RevisionSession {
	t.Helper()
	payload, _ := json.Marshal(content)
	next, err := store.SubmitCandidate(policy, SubmitRevisionCandidateInput{
		SessionID: session.ID, ExpectedRevision: session.Revision, IdempotencyKey: key,
		Artifacts: []CandidateArtifactInput{{ArtifactID: "chapter-1", ArtifactKind: "prose", Payload: payload}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return next
}

func mustPassAndApproveAll(t *testing.T, store *RevisionStore, policy fakeRevisionPolicy, session *domain.RevisionSession, prefix string) *domain.RevisionSession {
	t.Helper()
	next, err := store.RecordAudit(policy, RevisionAuditInput{
		RevisionMutationInput: RevisionMutationInput{SessionID: session.ID, ExpectedRevision: session.Revision, IdempotencyKey: prefix + "-audit"},
		CandidateSignature:    session.CandidateSignature, Passed: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, stage := range []string{"volume", "outline", "prose"} {
		next, err = store.ApproveStage(policy, RevisionApprovalInput{
			RevisionMutationInput: RevisionMutationInput{SessionID: next.ID, ExpectedRevision: next.Revision, IdempotencyKey: prefix + "-approve-" + stage},
			StageID:               stage,
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return next
}

func assertCanonicalDraft(t *testing.T, path string) {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "canonical draft" {
		t.Fatalf("canonical draft was overwritten: %q", content)
	}
}
