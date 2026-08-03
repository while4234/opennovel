package flow

import (
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
)

type fakeFlowRevisionPolicy struct{}

func (fakeFlowRevisionPolicy) Identity() (string, string) { return "test.flow-revision", "1" }

func (fakeFlowRevisionPolicy) Mode() domain.RevisionMode { return "fake-flow" }

func (fakeFlowRevisionPolicy) ApprovalStages(domain.RevisionImpact) ([]domain.RevisionApprovalStage, error) {
	return []domain.RevisionApprovalStage{{ID: "prose", Label: "Prose"}}, nil
}

func (fakeFlowRevisionPolicy) ValidateImpact(domain.RevisionImpact) error { return nil }

func (fakeFlowRevisionPolicy) ValidateCandidate(domain.RevisionSession, []domain.ArtifactVersion) error {
	return nil
}

func (fakeFlowRevisionPolicy) Route(session domain.RevisionSession) (*domain.RevisionRoute, error) {
	if session.Stage == domain.RevisionStageCandidateGenerating {
		return &domain.RevisionRoute{Agent: "revision_writer", Task: "generate revision candidate", Reason: "active revision"}, nil
	}
	return nil, nil
}

func TestRouteActiveRevisionIsExclusive(t *testing.T) {
	progress := &domain.Progress{Phase: domain.PhaseWriting}
	if got := Route(State{RevisionActive: true, Progress: progress}); got != nil {
		t.Fatalf("manual revision boundary routed ordinary work: %+v", got)
	}
	route := &domain.RevisionRoute{Agent: "revision_writer", Task: "generate revision candidate", Reason: "active revision"}
	got := Route(State{RevisionActive: true, RevisionRoute: route, Progress: progress})
	if got == nil || got.Agent != route.Agent || got.Task != route.Task || got.Reason != route.Reason {
		t.Fatalf("revision route = %+v", got)
	}
	if got.Fence.Generation != route.Generation || got.Fence.SessionID != route.SessionID || got.Fence.Revision != route.Revision {
		t.Fatalf("revision route dropped its write fence: %+v", got.Fence)
	}
}

func TestLoadStatePersistsRevisionRouteAndBlocksMissingProgress(t *testing.T) {
	st := storepkg.NewStore(t.TempDir())
	impact, err := domain.NewRevisionImpact("test impact", []domain.RevisionImpactItem{{
		ArtifactID: "chapter-1", ArtifactKind: "prose", Change: "rewrite",
	}})
	if err != nil {
		t.Fatal(err)
	}
	policy := fakeFlowRevisionPolicy{}
	session, err := st.Revisions.Start(policy, storepkg.StartRevisionInput{Intent: "test", Impact: impact, IdempotencyKey: "start"})
	if err != nil {
		t.Fatal(err)
	}
	session, err = st.Revisions.ApproveImpact(policy, storepkg.RevisionMutationInput{
		SessionID: session.ID, ExpectedRevision: session.Revision, IdempotencyKey: "approve-impact",
	})
	if err != nil {
		t.Fatal(err)
	}
	state := LoadState(storepkg.NewStore(st.Dir()))
	if !state.RevisionActive || state.RevisionRoute == nil || state.RevisionRoute.Agent != "revision_writer" {
		t.Fatalf("loaded revision state = %+v", state)
	}
	got := Route(state)
	if got == nil || got.Agent != "revision_writer" {
		t.Fatalf("route from persisted revision = %+v", got)
	}
}
