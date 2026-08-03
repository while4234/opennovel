package store

import (
	"errors"
	"sync"
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
)

func TestManuscriptRevisionCrossStoreSingleOwnerAndIdempotentRetry(t *testing.T) {
	root := t.TempDir()
	first := NewStore(root)
	second := NewStore(root)
	runtimeA := testManuscriptRuntime("msr_a", "instruction a")
	runtimeB := testManuscriptRuntime("msr_b", "instruction b")

	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for _, call := range []func() error{
		func() error { _, err := first.ManuscriptRevisions.Start(runtimeA, "start-a"); return err },
		func() error { _, err := second.ManuscriptRevisions.Start(runtimeB, "start-b"); return err },
	} {
		wg.Add(1)
		go func(call func() error) {
			defer wg.Done()
			<-start
			errs <- call()
		}(call)
	}
	close(start)
	wg.Wait()
	close(errs)
	successes := 0
	conflicts := 0
	for err := range errs {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, ErrRevisionCommandInProgress):
			conflicts++
		default:
			t.Fatalf("Start error = %v", err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("successes=%d conflicts=%d", successes, conflicts)
	}
	active, err := NewStore(root).ManuscriptRevisions.Active()
	if err != nil || active == nil {
		t.Fatalf("Active = %+v err=%v", active, err)
	}
	key := "start-a"
	runtime := runtimeA
	if active.RevisionID == runtimeB.RevisionID {
		key, runtime = "start-b", runtimeB
	}
	retry, err := NewStore(root).ManuscriptRevisions.Start(runtime, key)
	if err != nil || retry.RevisionID != active.RevisionID {
		t.Fatalf("idempotent retry = %+v err=%v", retry, err)
	}
}

func TestManuscriptRevisionStartCapturesSnapshotInsideOwnershipTransaction(t *testing.T) {
	root := t.TempDir()
	first := NewStore(root)
	second := NewStore(root)
	entered := make(chan struct{})
	release := make(chan struct{})
	firstResult := make(chan error, 1)
	go func() {
		_, err := first.ManuscriptRevisions.StartAtomic(testManuscriptRuntime("msr_atomic", "atomic"), "atomic-start", func(runtime *domain.ManuscriptRevisionRuntime) error {
			close(entered)
			<-release
			runtime.Baseline.StructureSignature = "captured-inside-owner"
			return nil
		})
		firstResult <- err
	}()
	<-entered
	secondResult := make(chan error, 1)
	go func() {
		_, err := second.ManuscriptRevisions.Start(testManuscriptRuntime("msr_racer", "racer"), "racer-start")
		secondResult <- err
	}()
	select {
	case err := <-secondResult:
		t.Fatalf("competing owner returned before atomic snapshot completed: %v", err)
	default:
	}
	close(release)
	if err := <-firstResult; err != nil {
		t.Fatalf("StartAtomic: %v", err)
	}
	if err := <-secondResult; !errors.Is(err, ErrRevisionCommandInProgress) {
		t.Fatalf("competing Start error = %v", err)
	}
	active, err := NewStore(root).ManuscriptRevisions.Active()
	if err != nil || active.Baseline.StructureSignature != "captured-inside-owner" {
		t.Fatalf("atomic captured runtime = %+v err=%v", active, err)
	}
}

func testManuscriptRuntime(id, instruction string) domain.ManuscriptRevisionRuntime {
	contract := domain.NarrativeContract{
		ChapterID: "ch_0123456789abcdef0123456789abcdef", OutlineSHA256: "outline", Desire: "desire", Obstacle: "obstacle",
		Choice: "choice", Cost: "cost", Result: "result", ExitState: "exit", StateSHA256: domain.ContentSignature([]byte("state")),
	}
	baseline := domain.ManuscriptBaseline{
		ChapterID: contract.ChapterID, DisplayChapter: 1, CurrentProseSHA256: domain.ContentSignature([]byte("prose")), ApprovedOutlineSHA256: domain.ContentSignature([]byte("outline")),
		StructureSignature: "structure", NarrativeContract: contract, Mode: domain.RevisionModeNormal,
	}
	baseline.ContractArtifact = domain.NewNarrativeContractArtifact(contract, baseline.CurrentProseSHA256, baseline.ApprovedOutlineSHA256)
	return domain.ManuscriptRevisionRuntime{
		Version: 1, RevisionID: id, Revision: 1, Mode: domain.RevisionModeNormal,
		PolicyID: domain.NormalManuscriptRevisionPolicyID, PolicyVersion: domain.ManuscriptRevisionPolicyVersion,
		Instruction: instruction, InstructionKind: domain.ManuscriptInstructionRewrite, Stage: "approval_pending", Baseline: baseline,
		Queue:             []domain.ManuscriptReworkItem{{ChapterID: baseline.ChapterID, DisplayChapter: 1, Requirement: domain.StructureImpactRequired, ExpectedSignatures: []string{"prose"}, Status: "pending", IdempotencyKey: "item"}},
		PublicationStatus: domain.ManuscriptPublicationNone, UpdatedAt: "now",
	}
}
