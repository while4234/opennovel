package web

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/voocel/ainovel-cli/assets"
	"github.com/voocel/ainovel-cli/internal/domain"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
)

func establishWebApprovedNormalFoundationFixture(t *testing.T, st *storepkg.Store) {
	t.Helper()
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	binding, err := st.CoreCast.SaveGateBinding(storepkg.CoreCastGateBinding{
		Mode: domain.CoreCastModeNormal, DraftRevision: 1, DraftHash: "web-normal-draft",
	})
	if err != nil {
		t.Fatal(err)
	}
	contract := domain.CoreCastContract{
		Version: domain.CoreCastContractVersion, Mode: domain.CoreCastModeNormal,
		DraftRevision: binding.DraftRevision, DraftHash: binding.DraftHash,
		Members: []domain.CoreCastMember{{
			Character: domain.Character{
				ID: "web-hero", Name: "Web Hero", Role: "protagonist", Goal: "reveal the truth",
				Motivation: "protect the city", Conflict: "the archive is sealed", Arc: "accepts responsibility",
				Traits: []string{"persistent"}, Constraints: []string{"cannot erase evidence"},
			},
			Importance: domain.CoreCastImportanceProtagonist, Origin: domain.CoreCastOriginOriginal,
			MainlineFunction: "drives the Web fixture", NoCoreRelationships: true,
		}},
	}
	saved, err := st.CoreCast.SaveCAS(contract, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.CoreCast.ConfirmCAS(saved.Revision, saved.ContentSignature, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CoreCast.PublishConfirmed(st.Foundation, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	review := &domain.PlanningReview{Brief: "approved Web fixture", StartPrompt: "start"}
	if _, err := st.BeginFoundationReview(review); err != nil {
		t.Fatal(err)
	}
	fence := &storepkg.FoundationGenerationFence{Generation: review.FoundationGeneration, BaseRevision: review.FoundationBaseRevision}
	foundation, err := st.Foundation.Load()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.SaveFoundationPremise(fence, "A complete Web fixture premise"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SaveFoundationCharacters(fence, foundation.Characters); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SaveFoundationRelationships(fence, foundation.Relationships); err != nil {
		t.Fatal(err)
	}
	review, err = st.SaveFoundationWorldRules(fence, []domain.WorldRule{{ID: "web-rule", Rule: "Consequences persist", Strength: domain.WorldRuleStrengthHard}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.ConfirmFoundation(review.FoundationRevision, review.FoundationAuditSignature); err != nil {
		t.Fatal(err)
	}
}

func setWebPlanningReviewPreservingFoundation(
	t *testing.T,
	st *storepkg.Store,
	configure func(*domain.PlanningReview),
) *domain.PlanningReview {
	t.Helper()
	review, err := st.RunMeta.PlanningReview()
	if err != nil {
		t.Fatal(err)
	}
	if review == nil {
		review = &domain.PlanningReview{}
	}
	configure(review)
	if err := st.RunMeta.SetPlanningReview(review); err != nil {
		t.Fatal(err)
	}
	return review
}

func TestFoundationReviewHTTPConfirmAndReviseUseCurrentProjectState(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), t.TempDir())
	defer server.Close()

	confirmProject, err := server.store.CreateProject("Foundation HTTP Confirm")
	if err != nil {
		t.Fatal(err)
	}
	installFakeSession(t, server, confirmProject)
	pending := pendingFoundationReviewForWebTest(t, confirmProject.OutputDir)
	body, _ := json.Marshal(webFoundationConfirmRequest{
		ExpectedRevision: pending.FoundationRevision,
		AuditSignature:   pending.FoundationAuditSignature,
	})
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/projects/"+confirmProject.ID+"/cocreate/foundation/confirm", bytes.NewReader(body))
	server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("confirm status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	confirmed, err := storepkg.NewStore(confirmProject.OutputDir).RunMeta.PlanningReview()
	if err != nil || confirmed == nil || confirmed.FoundationStatus != domain.FoundationReviewStatusApproved || confirmed.Kind != domain.PlanningReviewKindBlueprint {
		t.Fatalf("confirmed review=%+v err=%v", confirmed, err)
	}

	reviseProject, err := server.store.CreateProject("Foundation HTTP Revise")
	if err != nil {
		t.Fatal(err)
	}
	installFakeSession(t, server, reviseProject)
	revisePending := pendingFoundationReviewForWebTest(t, reviseProject.OutputDir)
	staleBody, _ := json.Marshal(webFoundationConfirmRequest{
		ExpectedRevision: revisePending.FoundationRevision - 1,
		AuditSignature:   revisePending.FoundationAuditSignature,
	})
	recorder = httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodPost, "/api/projects/"+reviseProject.ID+"/cocreate/foundation/confirm", bytes.NewReader(staleBody))
	server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusConflict {
		t.Fatalf("stale confirm status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var staleResponse struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &staleResponse); err != nil {
		t.Fatal(err)
	}
	if staleResponse.Error.Code != storepkg.FoundationReviewErrorStale {
		t.Fatalf("stale code=%q", staleResponse.Error.Code)
	}

	reviseBody, _ := json.Marshal(webFoundationReviseRequest{Feedback: "raise the cost of exposing the archive"})
	recorder = httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodPost, "/api/projects/"+reviseProject.ID+"/cocreate/foundation/revise", bytes.NewReader(reviseBody))
	server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("revise status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	revised, err := storepkg.NewStore(reviseProject.OutputDir).RunMeta.PlanningReview()
	if err != nil || revised == nil || revised.FoundationStatus != domain.FoundationReviewStatusCollecting ||
		revised.FoundationGeneration != revisePending.FoundationGeneration+1 || revised.FoundationFeedback != "raise the cost of exposing the archive" {
		t.Fatalf("revised review=%+v err=%v", revised, err)
	}
}

func TestWriteFoundationReviewErrorReturnsStableCodeAndLatestReview(t *testing.T) {
	review := &domain.PlanningReview{
		Kind:               domain.PlanningReviewKindFoundation,
		Status:             domain.PlanningReviewStatusPending,
		FoundationStatus:   domain.FoundationReviewStatusPending,
		FoundationRevision: 9,
	}
	recorder := httptest.NewRecorder()
	writeFoundationReviewError(recorder, &storepkg.FoundationReviewError{
		Code:   storepkg.FoundationReviewErrorStale,
		Err:    errors.New("stale foundation"),
		Review: review,
	})
	if recorder.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", recorder.Code)
	}
	var response struct {
		Error struct {
			Code   string                 `json:"code"`
			Latest *domain.PlanningReview `json:"latest"`
		} `json:"error"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Error.Code != storepkg.FoundationReviewErrorStale || response.Error.Latest == nil || response.Error.Latest.FoundationRevision != 9 {
		t.Fatalf("response = %+v", response)
	}
}

func TestFoundationReviewHTTPRejectsSourceFoundationMutation(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), t.TempDir())
	defer server.Close()
	project, err := server.store.CreateProject("Readonly Source Foundation")
	if err != nil {
		t.Fatal(err)
	}
	installFakeSession(t, server, project)
	for _, action := range []string{"confirm", "revise"} {
		body := []byte(`{"feedback":"tamper","source_foundation":{"premise":"tampered"}}`)
		if action == "confirm" {
			body = []byte(`{"expected_revision":1,"audit_signature":"forged","source_foundation":{"premise":"tampered"}}`)
		}
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/api/projects/"+project.ID+"/cocreate/foundation/"+action, bytes.NewReader(body))
		server.Handler().ServeHTTP(recorder, request)
		if recorder.Code != http.StatusUnprocessableEntity || !strings.Contains(recorder.Body.String(), "read-only") {
			t.Fatalf("%s status=%d body=%s", action, recorder.Code, recorder.Body.String())
		}
	}
}

func TestWriteFoundationReviewErrorClassifiesSessionBusy(t *testing.T) {
	recorder := httptest.NewRecorder()
	writeFoundationReviewError(recorder, ErrSessionActionInProgress)
	if recorder.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", recorder.Code)
	}
	var response struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Error.Code != storepkg.FoundationReviewErrorBusy {
		t.Fatalf("code = %q, want %q", response.Error.Code, storepkg.FoundationReviewErrorBusy)
	}
}
