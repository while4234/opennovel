package startup

import (
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/host"
)

func TestCoCreateDraftBindingUsesMonotonicRevisionAndNormalizedHash(t *testing.T) {
	session := NewCoCreateSession("seed")
	session.ApplyReply(host.CoCreateReply{Prompt: "draft  \r\nline", Raw: "assistant"})
	firstRevision, firstHash := session.DraftRevision(), session.DraftHash()
	if firstRevision <= 0 || firstHash == "" {
		t.Fatalf("first binding = revision %d hash %q", firstRevision, firstHash)
	}
	session.AppendUser("refine")
	if session.DraftRevision() != firstRevision || session.DraftHash() != firstHash {
		t.Fatal("user history length changed the semantic draft binding")
	}
	session.ApplyReply(host.CoCreateReply{Prompt: "draft\nline", Raw: "assistant"})
	if session.DraftRevision() <= firstRevision {
		t.Fatal("accepted draft revision did not advance monotonically")
	}
	if session.DraftHash() != firstHash {
		t.Fatal("line ending and trailing whitespace normalization changed the draft hash")
	}
	restored := NewCoCreateSessionFromSnapshot(session.Snapshot())
	if restored.DraftRevision() != session.DraftRevision() || restored.DraftHash() != session.DraftHash() {
		t.Fatalf("restored binding = %d/%q, want %d/%q", restored.DraftRevision(), restored.DraftHash(), session.DraftRevision(), session.DraftHash())
	}
}

func TestCoCreateSnapshotMigratesLegacyCastAsUnreviewedSeed(t *testing.T) {
	raw := `<reply>ok</reply><draft>brief</draft><cast>{"version":1,"mode":"normal","draft_revision":1,"members":[],"planned_relationships":[],"source_dispositions":[]}</cast><ready>true</ready><suggestions></suggestions>`
	restored := NewCoCreateSessionFromSnapshot(CoCreateSnapshot{
		History: []host.CoCreateMessage{{Role: "assistant", Content: raw}},
	})
	seed := restored.LegacyCoreCast()
	if seed == nil || seed.Mode != domain.CoreCastModeNormal || seed.ConfirmedSignature != "" {
		t.Fatalf("legacy CoreCast seed = %+v", seed)
	}
	again := NewCoCreateSessionFromSnapshot(restored.Snapshot()).LegacyCoreCast()
	if again == nil || again.Mode != domain.CoreCastModeNormal || again.ConfirmedSignature != "" {
		t.Fatalf("persisted legacy CoreCast seed = %+v", again)
	}
}
