package store

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain"
)

func TestFoundationLoadsSchemaV1AndV2WithoutRevisionOrFileMutation(t *testing.T) {
	for _, schema := range []int{1, 2} {
		t.Run(string(rune('0'+schema)), func(t *testing.T) {
			dir := t.TempDir()
			io := newIO(dir)
			fixture := legacyCharacterCardFoundation(schema)
			files, _, err := foundationTransactionPayloads(fixture)
			if err != nil {
				t.Fatal(err)
			}
			for rel, data := range files {
				if err := io.WriteFileUnlocked(rel, data); err != nil {
					t.Fatal(err)
				}
			}
			// Restore the requested legacy schema after the helper generated
			// canonical v3-compatible projections and manifest signatures.
			var raw map[string]any
			if err := json.Unmarshal(files[foundationCanonicalFile], &raw); err != nil {
				t.Fatal(err)
			}
			raw["schema_version"] = schema
			canonical, err := json.MarshalIndent(raw, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(io.path(foundationCanonicalFile), canonical, 0o644); err != nil {
				t.Fatal(err)
			}
			var manifest foundationProjectionManifest
			if err := io.ReadJSON(foundationManifestFile, &manifest); err != nil {
				t.Fatal(err)
			}
			manifest.Files[foundationCanonicalFile] = fileDigest(canonical)
			if err := io.WriteJSON(foundationManifestFile, manifest); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(io.path(foundationCanonicalFile))
			if err != nil {
				t.Fatal(err)
			}

			store := newFoundationStore(io)
			loaded, err := store.Load()
			if err != nil {
				t.Fatal(err)
			}
			if loaded.SchemaVersion != domain.StoryFoundationSchemaVersion || loaded.Revision != fixture.Revision {
				t.Fatalf("loaded identity = schema %d revision %d", loaded.SchemaVersion, loaded.Revision)
			}
			after, err := os.ReadFile(io.path(foundationCanonicalFile))
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, after) {
				t.Fatal("read-only legacy load rewrote the canonical fixture")
			}
			migrated, err := store.SaveCAS(loaded, loaded.Revision)
			if err != nil {
				t.Fatal(err)
			}
			if migrated.Revision != loaded.Revision {
				t.Fatalf("schema-only migration changed revision: %d -> %d", loaded.Revision, migrated.Revision)
			}
			repeated, err := store.SaveCAS(migrated, migrated.Revision)
			if err != nil {
				t.Fatal(err)
			}
			if repeated.Revision != migrated.Revision {
				t.Fatalf("repeated migration changed revision: %d -> %d", migrated.Revision, repeated.Revision)
			}
		})
	}
}

func TestCharacterCardStoreSaveReloadAndStaleRegression(t *testing.T) {
	dir := t.TempDir()
	foundationStore := newFoundationStore(newIO(dir))
	foundation := legacyCharacterCardFoundation(domain.StoryFoundationSchemaVersion)
	foundation.Characters[0].Tier = "core"
	foundation.Characters[0].Description = "lead"
	foundation.Characters[0].Arc = "opens up"
	foundation.Characters[0].Goal = "win"
	foundation.Characters[0].Motivation = "protect family"
	foundation.Characters[0].Conflict = "duty versus family"
	foundation.Characters[0].Traits = []string{"calm"}
	foundation.Characters[0].Constraints = []string{"keeps promises"}
	foundation.Characters[0].InitialState = &domain.CharacterInitialState{Situation: "under watch"}
	savedFoundation, err := foundationStore.SaveCAS(foundation, 0)
	if err != nil {
		t.Fatal(err)
	}
	binding, err := domain.CharacterCardBindingFromFoundation(
		savedFoundation,
		domain.CharacterCardInputSignatures{CreativeBrief: "brief", CoreCast: "cast"},
	)
	if err != nil {
		t.Fatal(err)
	}
	lifecycle := domain.CharacterCardLifecycle{
		Version:   domain.CharacterCardLifecycleVersion,
		Mode:      domain.CharacterCardProjectOriginal,
		Candidate: binding.Candidate, Inputs: binding.Inputs, InputDigest: binding.InputDigest,
		AnalysisStatus: domain.CharacterCardAnalysisCandidateReady,
		ReviewStatus:   domain.CharacterCardReviewPassed, ReviewedCandidate: binding.Candidate,
		ReviewedInputDigest: binding.InputDigest, Findings: []domain.CharacterCardReviewFinding{},
		ConfirmationStatus: domain.CharacterCardConfirmed, RunID: "run", IdempotencyKey: "key",
	}
	store := newCharacterCardStore(newIO(dir))
	saved, err := store.SaveCAS(lifecycle, 0, binding)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Revision != 1 {
		t.Fatalf("lifecycle revision = %d", saved.Revision)
	}
	reloaded, err := store.Load(binding)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded == nil || reloaded.ReviewStatus != domain.CharacterCardReviewPassed {
		t.Fatalf("reloaded lifecycle = %+v", reloaded)
	}

	changed := domain.CloneStoryFoundation(savedFoundation)
	changed.Characters[0].InitialState.Situation = "imprisoned"
	changedFoundation, err := foundationStore.SaveCAS(changed, savedFoundation.Revision)
	if err != nil {
		t.Fatal(err)
	}
	changedBinding, err := domain.CharacterCardBindingFromFoundation(changedFoundation, binding.Inputs)
	if err != nil {
		t.Fatal(err)
	}
	stale, err := store.Load(changedBinding)
	if err != nil {
		t.Fatal(err)
	}
	if stale.AnalysisStatus != domain.CharacterCardAnalysisStale ||
		stale.ReviewStatus != domain.CharacterCardReviewStale ||
		stale.ConfirmationStatus != domain.CharacterCardConfirmationStale {
		t.Fatalf("stale lifecycle = %+v", stale)
	}
}

func TestApplyCoreCastPreservesNonCoreFoundationContent(t *testing.T) {
	foundation := legacyCharacterCardFoundation(domain.StoryFoundationSchemaVersion)
	foundation.Characters = append(foundation.Characters, domain.Character{ID: "support", Name: "Support", Role: "guide"})
	foundation.Relationships = []domain.CharacterRelationship{{
		ID: "mixed", SourceCharacterID: "core", TargetCharacterID: "support",
		Type: domain.RelationshipTypeProfessional, Direction: domain.RelationshipDirectionDirected,
		Status: domain.RelationshipStatusPlanned,
	}}
	contract := domain.CoreCastContract{
		Members:              []domain.CoreCastMember{{Character: domain.Character{ID: "core", Name: "Core", Role: "lead"}}},
		PlannedRelationships: []domain.CharacterRelationship{},
	}
	result := domain.ApplyCoreCastToFoundation(foundation, contract)
	if len(result.Characters) != 2 || len(result.Relationships) != 1 {
		t.Fatalf("core cast publish dropped supporting content: %+v", result)
	}
}

func TestCharacterCardStoreDiscardCandidateCASFencesRevisionAndDigest(t *testing.T) {
	dir := t.TempDir()
	foundation := legacyCharacterCardFoundation(domain.StoryFoundationSchemaVersion)
	foundation.Revision = 1
	binding, err := domain.CharacterCardBindingFromFoundation(
		foundation,
		domain.CharacterCardInputSignatures{CreativeBrief: "brief"},
	)
	if err != nil {
		t.Fatal(err)
	}
	store := newCharacterCardStore(newIO(dir))
	saved, err := store.SaveCandidateCAS(domain.CharacterCardCandidate{
		Version:    domain.CharacterCardCandidateVersion,
		Base:       binding,
		Foundation: foundation,
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := domain.CharacterCardContentDigest(saved.Foundation)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.DiscardCandidateCAS(saved.Revision+1, digest); err == nil {
		t.Fatal("stale revision unexpectedly discarded the candidate")
	}
	if current, err := store.LoadCandidate(); err != nil || current == nil {
		t.Fatalf("candidate missing after rejected discard: candidate=%+v err=%v", current, err)
	}
	if err := store.DiscardCandidateCAS(saved.Revision, digest); err != nil {
		t.Fatal(err)
	}
	if current, err := store.LoadCandidate(); err != nil || current != nil {
		t.Fatalf("candidate remains after valid discard: candidate=%+v err=%v", current, err)
	}
}

func TestCharacterCardStoresShareProjectLockAcrossStoreInstances(t *testing.T) {
	dir := t.TempDir()
	first := NewStore(dir).CharacterCards
	second := NewStore(dir).CharacterCards

	first.mu.Lock()
	loadDone := make(chan error, 1)
	go func() {
		_, err := second.LoadCandidate()
		loadDone <- err
	}()

	select {
	case err := <-loadDone:
		first.mu.Unlock()
		t.Fatalf("second store bypassed the shared project lock: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	first.mu.Unlock()

	select {
	case err := <-loadDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("second store remained blocked after the shared project lock was released")
	}
}

func TestCharacterCardDiscardCannotDeleteConcurrentCandidateRevision(t *testing.T) {
	dir := t.TempDir()
	first := NewStore(dir).CharacterCards
	second := NewStore(dir).CharacterCards
	foundation := legacyCharacterCardFoundation(domain.StoryFoundationSchemaVersion)
	foundation.Revision = 1
	binding, err := domain.CharacterCardBindingFromFoundation(
		foundation,
		domain.CharacterCardInputSignatures{CreativeBrief: "brief"},
	)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := first.SaveCandidateCAS(domain.CharacterCardCandidate{
		Version:    domain.CharacterCardCandidateVersion,
		Base:       binding,
		Foundation: foundation,
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	oldDigest, err := domain.CharacterCardContentDigest(saved.Foundation)
	if err != nil {
		t.Fatal(err)
	}

	writerEntered := make(chan struct{})
	releaseWriter := make(chan struct{})
	defer func() {
		select {
		case <-releaseWriter:
		default:
			close(releaseWriter)
		}
	}()
	second.io.writeFault = func(rel, stage string) error {
		if rel == characterCardCandidateFile && stage == "after_temp_sync" {
			close(writerEntered)
			<-releaseWriter
		}
		return nil
	}
	next := saved
	next.Foundation.Premise = "concurrently updated candidate"
	saveDone := make(chan error, 1)
	go func() {
		_, saveErr := second.SaveCandidateCAS(next, saved.Revision)
		saveDone <- saveErr
	}()
	select {
	case <-writerEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("candidate writer did not reach the deterministic barrier")
	}

	discardDone := make(chan error, 1)
	go func() {
		discardDone <- first.DiscardCandidateCAS(saved.Revision, oldDigest)
	}()
	select {
	case discardErr := <-discardDone:
		t.Fatalf("discard bypassed the in-flight candidate write: %v", discardErr)
	case <-time.After(100 * time.Millisecond):
	}
	close(releaseWriter)

	select {
	case saveErr := <-saveDone:
		if saveErr != nil {
			t.Fatal(saveErr)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("candidate writer did not finish")
	}
	select {
	case discardErr := <-discardDone:
		var conflict *CharacterCardLifecycleConflictError
		if !errors.As(discardErr, &conflict) || conflict.Expected != saved.Revision || conflict.Actual != saved.Revision+1 {
			t.Fatalf("stale discard error = %v, want revision conflict %d -> %d", discardErr, saved.Revision, saved.Revision+1)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("stale discard did not finish after the writer released the shared lock")
	}
	current, err := first.LoadCandidate()
	if err != nil {
		t.Fatal(err)
	}
	if current == nil || current.Revision != saved.Revision+1 || current.Foundation.Premise != next.Foundation.Premise {
		t.Fatalf("concurrent candidate revision was deleted or replaced: %+v", current)
	}
}

func legacyCharacterCardFoundation(schema int) domain.StoryFoundation {
	return domain.StoryFoundation{
		SchemaVersion: schema,
		Revision:      4,
		Premise:       "legacy",
		Characters: []domain.Character{{
			ID: "core", Name: "Core", Role: "lead", Description: "legacy lead", Arc: "changes",
			Traits: []string{"steady"},
		}},
		WorldRules: []domain.WorldRule{{
			ID: "rule", Category: "other", Rule: "Legacy rule", Boundary: "fixed",
			Strength: domain.WorldRuleStrengthHard,
		}},
	}
}
