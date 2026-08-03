package store

import (
	"errors"
	"strings"
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
)

func TestCharacterWorkspaceStoreIdempotencyExclusionAndRecovery(t *testing.T) {
	root := t.TempDir()
	st := NewStore(root)
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}

	first := testCharacterWorkspaceRun("run-1", "key-1", strings.Repeat("a", 64))
	created, fresh, err := st.CharacterWorkspace.Create(first)
	if err != nil || !fresh {
		t.Fatalf("Create first: fresh=%t err=%v", fresh, err)
	}
	replayed, fresh, err := st.CharacterWorkspace.Create(first)
	if err != nil || fresh || replayed.RunID != created.RunID {
		t.Fatalf("Create replay: run=%q fresh=%t err=%v", replayed.RunID, fresh, err)
	}

	conflicting := first
	conflicting.RequestFingerprint = strings.Repeat("b", 64)
	if _, _, err := st.CharacterWorkspace.Create(conflicting); err == nil {
		t.Fatal("Create accepted an idempotency key with different input")
	} else {
		var conflict *CharacterWorkspaceConflictError
		if !errors.As(err, &conflict) {
			t.Fatalf("Create conflict type = %T, want CharacterWorkspaceConflictError", err)
		}
	}

	second := testCharacterWorkspaceRun("run-2", "key-2", strings.Repeat("c", 64))
	if _, _, err := st.CharacterWorkspace.Create(second); err == nil {
		t.Fatal("Create accepted a second active run")
	}

	restarted := NewStore(root)
	if err := restarted.Init(); err != nil {
		t.Fatalf("restart Init: %v", err)
	}
	if err := restarted.CharacterWorkspace.RecoverInterrupted(); err != nil {
		t.Fatalf("RecoverInterrupted: %v", err)
	}
	recovered, err := restarted.CharacterWorkspace.Load(first.RunID)
	if err != nil {
		t.Fatalf("Load recovered: %v", err)
	}
	if recovered == nil || recovered.Status != domain.CharacterWorkspaceInterrupted {
		t.Fatalf("recovered status = %#v, want interrupted", recovered)
	}
	if recovered.Error == nil || recovered.Error.Class != "service_interrupted" {
		t.Fatalf("recovered safe error = %#v", recovered.Error)
	}

	createdSecond, fresh, err := restarted.CharacterWorkspace.Create(second)
	if err != nil || !fresh || createdSecond.RunID != second.RunID {
		t.Fatalf("Create after recovery: run=%q fresh=%t err=%v", createdSecond.RunID, fresh, err)
	}
}

func testCharacterWorkspaceRun(
	runID string,
	key string,
	fingerprint string,
) domain.CharacterWorkspaceRun {
	digest := strings.Repeat("d", 64)
	now := domain.RevisionTimestamp()
	return domain.CharacterWorkspaceRun{
		Version:     domain.CharacterWorkspaceRunVersion,
		RunID:       runID,
		Mode:        domain.CharacterWorkspaceAnalyze,
		Status:      domain.CharacterWorkspaceQueued,
		Stage:       "queued",
		ProjectMode: domain.CharacterCardProjectOriginal,
		Base: domain.CharacterCardBinding{
			Candidate: domain.CharacterCardCandidateReference{
				FoundationRevision:       1,
				FoundationAuditSignature: strings.Repeat("e", 64),
				CharacterContentDigest:   digest,
			},
			InputDigest: strings.Repeat("f", 64),
		},
		IdempotencyKey:       key,
		RequestFingerprint:   fingerprint,
		InputCandidateDigest: digest,
		Attempt:              1,
		RetryReceipts:        []domain.CharacterWorkspaceReceipt{},
		CreatedAt:            now,
		UpdatedAt:            now,
	}
}
