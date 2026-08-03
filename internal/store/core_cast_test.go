package store

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
)

func TestCoreCastStoreCASConfirmationAndSemanticInvalidation(t *testing.T) {
	st := NewStore(t.TempDir())
	saved, err := st.CoreCast.SaveCAS(storeCompleteNormalCoreCast(), 0)
	if err != nil {
		t.Fatal(err)
	}
	confirmed, completion, err := st.CoreCast.ConfirmCAS(saved.Revision, saved.ContentSignature, nil, nil, nil)
	if err != nil || !completion.Complete || confirmed.ConfirmedSignature != confirmed.ContentSignature {
		t.Fatalf("confirm = %+v, completion=%+v, err=%v", confirmed, completion, err)
	}

	changed := confirmed
	changed.Members[0].Character.Arc = "a changed arc"
	changed, err = st.CoreCast.SaveCAS(changed, confirmed.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if changed.ConfirmedSignature != "" || changed.ConfirmedAt != "" {
		t.Fatal("semantic update retained confirmation")
	}
	if _, _, err := st.CoreCast.ConfirmCAS(changed.Revision, confirmed.ContentSignature, nil, nil, nil); err == nil {
		t.Fatal("stale signature was accepted")
	} else {
		var conflict *CoreCastSignatureConflictError
		if !errors.As(err, &conflict) || conflict.Actual != changed.ContentSignature {
			t.Fatalf("signature conflict = %T %+v", err, err)
		}
	}
}

func TestCoreCastRepairLegacyUnconfirmedSignaturePreservesDraft(t *testing.T) {
	st := NewStore(t.TempDir())
	saved, err := st.CoreCast.SaveCAS(storeCompleteNormalCoreCast(), 0)
	if err != nil {
		t.Fatal(err)
	}
	legacy := saved
	legacy.DraftRevision++
	legacy.DraftHash = "legacy-normalization-changed"
	if err := st.CoreCast.io.WriteJSON(coreCastContractFile, legacy); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CoreCast.Load(); !errors.Is(err, ErrCoreCastContentSignatureMismatch) {
		t.Fatalf("legacy signature load error = %v", err)
	}
	migrated, err := st.CoreCast.RepairLegacyUnconfirmedSignature()
	if err != nil {
		t.Fatal(err)
	}
	if migrated.Revision != legacy.Revision+1 ||
		migrated.DraftRevision != legacy.DraftRevision ||
		migrated.DraftHash != legacy.DraftHash ||
		len(migrated.Members) != len(legacy.Members) ||
		migrated.ContentSignature == legacy.ContentSignature {
		t.Fatalf("migrated legacy draft = %+v", migrated)
	}
	if reloaded, err := st.CoreCast.Load(); err != nil || reloaded.ContentSignature != migrated.ContentSignature {
		t.Fatalf("reloaded migrated draft = %+v err=%v", reloaded, err)
	}
}

func TestCoreCastLoadWithLegacySignatureRepairPreservesUnconfirmedDraft(t *testing.T) {
	st := NewStore(t.TempDir())
	saved, err := st.CoreCast.SaveCAS(storeCompleteNormalCoreCast(), 0)
	if err != nil {
		t.Fatal(err)
	}
	legacy := saved
	legacy.DraftRevision++
	legacy.DraftHash = "legacy-normalization-changed"
	if err := st.CoreCast.io.WriteJSON(coreCastContractFile, legacy); err != nil {
		t.Fatal(err)
	}

	migrated, err := st.CoreCast.LoadWithLegacySignatureRepair()
	if err != nil {
		t.Fatal(err)
	}
	if migrated == nil ||
		migrated.Revision != legacy.Revision+1 ||
		migrated.DraftRevision != legacy.DraftRevision ||
		migrated.DraftHash != legacy.DraftHash ||
		len(migrated.Members) != len(legacy.Members) ||
		migrated.ContentSignature == legacy.ContentSignature {
		t.Fatalf("migrated legacy draft = %+v", migrated)
	}
}

func TestCoreCastConcurrentSaveAllowsOneRevisionWinner(t *testing.T) {
	dir := t.TempDir()
	first := NewStore(dir)
	saved, err := first.CoreCast.SaveCAS(storeCompleteNormalCoreCast(), 0)
	if err != nil {
		t.Fatal(err)
	}
	stores := []*Store{NewStore(dir), NewStore(dir)}
	errCh := make(chan error, len(stores))
	var wait sync.WaitGroup
	for index, st := range stores {
		wait.Add(1)
		go func(index int, st *Store) {
			defer wait.Done()
			candidate := storeCompleteNormalCoreCast()
			candidate.Members[0].Character.Arc = []string{"first", "second"}[index]
			_, saveErr := st.CoreCast.SaveCAS(candidate, saved.Revision)
			errCh <- saveErr
		}(index, st)
	}
	wait.Wait()
	close(errCh)
	successes, conflicts := 0, 0
	for saveErr := range errCh {
		if saveErr == nil {
			successes++
			continue
		}
		var conflict *CoreCastConflictError
		if errors.As(saveErr, &conflict) {
			conflicts++
		} else {
			t.Fatalf("unexpected save error: %v", saveErr)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("successes=%d conflicts=%d", successes, conflicts)
	}
}

func TestCoreCastPublishIsIdempotentAndDoesNotWriteAdaptationSource(t *testing.T) {
	dir := t.TempDir()
	st := NewStore(dir)
	source := domain.AdaptationSourceFoundation{Premise: "immutable source", Characters: []domain.Character{{ID: "source-lin", Name: "Source Lin"}}}
	if err := st.Adaptation.SaveSourceFoundation(source); err != nil {
		t.Fatal(err)
	}
	sourcePath := filepath.Join(dir, filepath.FromSlash(adaptationSourceFoundationFile))
	sourceBefore, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := st.CoreCast.SaveCAS(storeCompleteNormalCoreCast(), 0)
	if err != nil {
		t.Fatal(err)
	}
	confirmed, _, err := st.CoreCast.ConfirmCAS(saved.Revision, saved.ContentSignature, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	published, err := st.CoreCast.PublishConfirmed(st.Foundation, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	formal, err := st.Foundation.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(formal.Characters) != 1 || formal.Characters[0].ID != "lin" || !formal.RelationshipsReviewed {
		t.Fatalf("foundation = %+v", formal)
	}
	if published.PublishReceipt.ContentSignature != confirmed.ContentSignature || published.PublishReceipt.FoundationRevision != formal.Revision {
		t.Fatalf("receipt = %+v", published.PublishReceipt)
	}
	retried, err := st.CoreCast.PublishConfirmed(st.Foundation, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	afterRetry, err := st.Foundation.Load()
	if err != nil {
		t.Fatal(err)
	}
	if retried.Revision != published.Revision || afterRetry.Revision != formal.Revision {
		t.Fatalf("idempotent retry advanced revisions: contract %d->%d foundation %d->%d", published.Revision, retried.Revision, formal.Revision, afterRetry.Revision)
	}
	_, err = st.BeginOriginalCharacterReview(&domain.PlanningReview{})
	if err != nil {
		t.Fatal(err)
	}
	review, err := st.RunMeta.PlanningReview()
	if err != nil || review == nil {
		t.Fatalf("load active Foundation review: review=%+v err=%v", review, err)
	}
	reviewedCandidate := domain.CloneStoryFoundation(formal)
	reviewedCandidate.RelationshipsReviewed = true
	if _, _, err := st.PublishOriginalCharacterCandidate(
		FoundationGenerationFence{
			Generation:   review.FoundationGeneration,
			BaseRevision: review.FoundationBaseRevision,
		},
		reviewedCandidate,
		review.FoundationBaseRevision,
	); err != nil {
		t.Fatal(err)
	}
	retriedDuringGeneration, err := st.CoreCast.PublishConfirmed(st.Foundation, nil, nil, nil)
	if err != nil {
		t.Fatalf("already-published cast should bypass the active generation mutation guard: %v", err)
	}
	if retriedDuringGeneration.Revision != published.Revision {
		t.Fatalf("generation-time idempotent retry advanced contract revision: %d -> %d", published.Revision, retriedDuringGeneration.Revision)
	}
	sourceAfter, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(sourceBefore, sourceAfter) {
		t.Fatal("core cast publish changed AdaptationSourceFoundation")
	}
}

func TestCoreCastGateBindingAndPublishedFoundationAuthority(t *testing.T) {
	st := NewStore(t.TempDir())
	binding, err := st.CoreCast.SaveGateBinding(CoreCastGateBinding{
		Mode: domain.CoreCastModeNormal, DraftRevision: 1, DraftHash: "draft-hash",
	})
	if err != nil {
		t.Fatal(err)
	}
	saved, err := st.CoreCast.SaveCAS(storeCompleteNormalCoreCast(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.CoreCast.ConfirmCAS(saved.Revision, saved.ContentSignature, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CoreCast.RequireConfirmedGate(binding, nil, nil, nil); err != nil {
		t.Fatalf("current gate blocked: %v", err)
	}
	stale := binding
	stale.DraftHash = "other"
	if _, err := st.CoreCast.RequireConfirmedGate(stale, nil, nil, nil); err == nil {
		t.Fatal("stale semantic draft hash passed the gate")
	}
	if _, err := st.CoreCast.PublishConfirmed(st.Foundation, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := st.Foundation.updateCharacters([]domain.Character{{ID: "other", Name: "Other"}}); err == nil {
		t.Fatal("published core cast characters were overwritten")
	}
	if err := st.Foundation.updatePremise("premise remains architect-owned"); err != nil {
		t.Fatalf("unrelated foundation section was blocked: %v", err)
	}
}

func storeCompleteNormalCoreCast() domain.CoreCastContract {
	return domain.CoreCastContract{
		Version: domain.CoreCastContractVersion, Mode: domain.CoreCastModeNormal, DraftRevision: 1, DraftHash: "draft-hash",
		Members: []domain.CoreCastMember{{
			Character:  domain.Character{ID: "lin", Name: "Lin", Role: "hero", Goal: "save home", Motivation: "duty", Conflict: "fear", Arc: "accept leadership", Traits: []string{"brave"}, Constraints: []string{"will not betray friends"}},
			Importance: domain.CoreCastImportanceProtagonist, Origin: domain.CoreCastOriginOriginal, MainlineFunction: "drives the central conflict", NoCoreRelationships: true,
		}},
	}
}
