package web

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/voocel/ainovel-cli/assets"
	"github.com/voocel/ainovel-cli/internal/adaptaudit"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/host"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
)

type webPreparedFencePolicy struct{}

func (webPreparedFencePolicy) Identity() (string, string) { return "test.web-prepared-fence", "1" }
func (webPreparedFencePolicy) Mode() domain.RevisionMode  { return "web-prepared-fence" }
func (webPreparedFencePolicy) ApprovalStages(domain.RevisionImpact) ([]domain.RevisionApprovalStage, error) {
	return []domain.RevisionApprovalStage{{ID: "prose", Label: "Prose"}}, nil
}
func (webPreparedFencePolicy) ValidateImpact(domain.RevisionImpact) error { return nil }
func (webPreparedFencePolicy) ValidateCandidate(domain.RevisionSession, []domain.ArtifactVersion) error {
	return nil
}
func (webPreparedFencePolicy) Route(domain.RevisionSession) (*domain.RevisionRoute, error) {
	return nil, nil
}

func TestProjectAdaptationRevisionPreviewUsesProductionService(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()
	manifest, err := server.store.CreateProject("Adaptation Revision")
	if err != nil {
		t.Fatal(err)
	}
	st := storepkg.NewStore(manifest.OutputDir)
	base, source := webAdaptationRevisionFixture(manifest.ID)
	if err := st.Adaptation.SaveSourceManifest(source); err != nil {
		t.Fatal(err)
	}
	if err := st.Adaptation.SavePlan(base); err != nil {
		t.Fatal(err)
	}
	report := adaptaudit.Audit(adaptaudit.Input{Mode: adaptaudit.ModeArc, Scope: adaptaudit.Scope{TargetFrom: 1, TargetTo: 2}})
	if err := st.Adaptation.SaveAuditReport(report); err != nil {
		t.Fatal(err)
	}
	if err := st.Progress.Save(&domain.Progress{NovelName: "adaptation", Phase: domain.PhaseWriting, Flow: domain.FlowWriting, TotalChapters: 2}); err != nil {
		t.Fatal(err)
	}
	formal, _ := st.Adaptation.LoadPlan()
	candidate := *formal
	candidate.TargetEventLedger = append(candidate.TargetEventLedger, domain.AdaptationEvent{ID: "added-event", Description: "bridge", Origin: domain.AdaptationEventOriginAdded, Required: true, DependsOn: []string{"source-event-2"}})
	addedID := domain.LegacyStructureID(manifest.ID, domain.StructureKindChapter, "chapter/added")
	candidate.Chapters = append(candidate.Chapters, domain.AdaptationChapterPlan{OutlineEntry: domain.OutlineEntry{ID: addedID, Chapter: 3, Title: "Bridge", CoreEvent: "bridge", Hook: "hook", Scenes: []string{"scene"}}, Chapter: 3, Title: "Bridge", IsAdded: true, AddedEventIDs: []string{"added-event"}, CoverageNote: "new story only", TargetRunes: 3500, TargetMinRunes: 2500, TargetMaxRunes: 4500, RequiredChanges: []string{"bridge"}, ForbiddenMoves: []string{"keep source"}})
	candidate.Volumes[0].TargetTo = 3
	candidate.TargetTotalRunes += 3500
	candidate.TargetMaxRunes += 4500
	payload, _ := json.Marshal(webAdaptationRevisionPreviewRequest{Intent: "append bridge", Candidate: candidate, IdempotencyKey: "web-preview"})
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/adapt/revision/preview", bytes.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("preview status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var response struct {
		Revision domain.RevisionSession `json:"revision"`
		Preview  struct {
			Stage domain.ManuscriptStage `json:"stage"`
		} `json:"preview"`
	}
	if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response.Revision.Mode != domain.RevisionModeAdaptation || response.Preview.Stage != domain.ManuscriptStageWriting {
		t.Fatalf("production response=%+v", response)
	}
	formalBefore, err := st.Adaptation.LoadPlan()
	if err != nil {
		t.Fatal(err)
	}
	legacyRecorder := httptest.NewRecorder()
	legacyRequest := httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/adapt/confirm", bytes.NewReader([]byte(`{}`)))
	legacyRequest.Header.Set("Content-Type", "application/json")
	server.Handler().ServeHTTP(legacyRecorder, legacyRequest)
	if legacyRecorder.Code < http.StatusBadRequest || !bytes.Contains(legacyRecorder.Body.Bytes(), []byte("blocked by an active revision")) {
		t.Fatalf("real Web legacy adaptation confirmation bypassed active revision: status=%d body=%s", legacyRecorder.Code, legacyRecorder.Body.String())
	}
	active, err := st.Revisions.Active()
	if err != nil || active == nil || active.ID != response.Revision.ID || active.Revision != response.Revision.Revision {
		t.Fatalf("legacy Web bypass changed active revision: active=%+v err=%v", active, err)
	}
	formalAfterLegacy, err := st.Adaptation.LoadPlan()
	if err != nil || !reflect.DeepEqual(formalBefore, formalAfterLegacy) {
		t.Fatalf("legacy Web adaptation confirmation changed formal plan: plan=%+v err=%v", formalAfterLegacy, err)
	}
	for _, bypass := range []struct {
		name string
		path string
		body string
	}{
		{name: "rollback", path: "/rollback", body: `{"confirm":true}`},
		{name: "audit", path: "/adapt/audit", body: `{}`},
	} {
		t.Run("real Web "+bypass.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+bypass.path, bytes.NewBufferString(bypass.body))
			req.Header.Set("Content-Type", "application/json")
			server.Handler().ServeHTTP(rec, req)
			if rec.Code < http.StatusBadRequest || !bytes.Contains(rec.Body.Bytes(), []byte("active revision")) {
				t.Fatalf("real Web %s bypassed active revision: status=%d body=%s", bypass.name, rec.Code, rec.Body.String())
			}
			after, loadErr := st.Adaptation.LoadPlan()
			if loadErr != nil || !reflect.DeepEqual(formalBefore, after) {
				t.Fatalf("rejected real Web %s changed formal plan: plan=%+v err=%v", bypass.name, after, loadErr)
			}
		})
	}
	_ = os.Remove(filepath.Join(manifest.OutputDir, "meta", "adaptation", "audits", "index.json"))
	historyRecorder := httptest.NewRecorder()
	historyRequest := httptest.NewRequest(http.MethodGet, "/api/projects/"+manifest.ID+"/adapt/audits", nil)
	server.Handler().ServeHTTP(historyRecorder, historyRequest)
	if historyRecorder.Code < http.StatusBadRequest || !bytes.Contains(historyRecorder.Body.Bytes(), []byte("active revision")) {
		t.Fatalf("audit-history GET rebuilt a missing index through the active fence: status=%d body=%s", historyRecorder.Code, historyRecorder.Body.String())
	}
	formalAfterHistory, err := st.Adaptation.LoadPlan()
	if err != nil || !reflect.DeepEqual(formalBefore, formalAfterHistory) {
		t.Fatalf("rejected audit-history GET changed formal plan: plan=%+v err=%v", formalAfterHistory, err)
	}
}

func TestProjectAdaptationRevisionHTTPPreparedPreviewRejectsSameKeyDirectStart(t *testing.T) {
	server, manifest, st, candidate := seedWebAdaptationRevisionServer(t)
	defer server.Close()
	projectSession, _, err := server.sessions.Open(manifest.ID)
	if err != nil {
		t.Fatal(err)
	}
	service := projectSession.AdaptationRevisionService()
	competitor := storepkg.NewStore(st.Dir())
	key := "http-prepared-same-key"
	impact, err := domain.NewRevisionImpact("HTTP same-key competitor", []domain.RevisionImpactItem{{
		ArtifactID: "chapter-1", ArtifactKind: "outline", Change: "compete",
	}})
	if err != nil {
		t.Fatal(err)
	}
	var competingErr error
	service.SetCommandPreparedHookForTesting(func() {
		_, competingErr = competitor.Revisions.Start(webPreparedFencePolicy{}, storepkg.StartRevisionInput{
			Intent: "forge HTTP preview ownership", Impact: impact, IdempotencyKey: key,
		})
	})
	payload, _ := json.Marshal(webAdaptationRevisionPreviewRequest{Intent: "HTTP owner capability", Candidate: candidate, IdempotencyKey: key})
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/adapt/revision/preview", bytes.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("prepared HTTP preview status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if !errors.Is(competingErr, storepkg.ErrRevisionCommandInProgress) {
		t.Fatalf("same-key direct start entered prepared HTTP preview: %v", competingErr)
	}
	service.SetCommandPreparedHookForTesting(nil)
	active, err := st.Revisions.Active()
	if err != nil || active == nil {
		t.Fatalf("prepared HTTP preview lost its acknowledged revision: active=%+v err=%v", active, err)
	}
	if _, err := service.Cancel(active, "http-prepared-cleanup"); err != nil {
		t.Fatal(err)
	}
}

func TestProjectAdaptationRevisionHTTPPreparedOwnerGuardsDurableWrites(t *testing.T) {
	server, manifest, st, candidate := seedWebAdaptationRevisionServer(t)
	defer server.Close()
	projectSession, _, err := server.sessions.Open(manifest.ID)
	if err != nil {
		t.Fatal(err)
	}
	service := projectSession.AdaptationRevisionService()
	competitor := storepkg.NewStore(st.Dir())
	otherProject := storepkg.NewStore(t.TempDir())
	previewKey := "http-owner-durable-preview"
	previewRequest := host.AdaptationRevisionPreviewRequest{Intent: "HTTP durable owner", Candidate: candidate}
	previewIdentity, err := json.Marshal(previewRequest)
	if err != nil {
		t.Fatal(err)
	}
	var previewReceiptErr error
	service.SetCommandPreparedHookForTesting(func() {
		previewReceiptErr = competitor.SaveAdaptationRevisionServiceReceipt(
			competitor.Revisions, previewKey, "preview", domain.ContentSignature(previewIdentity), map[string]string{"forged": "receipt"},
		)
	})
	payload, _ := json.Marshal(webAdaptationRevisionPreviewRequest{Intent: previewRequest.Intent, Candidate: candidate, IdempotencyKey: previewKey})
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/adapt/revision/preview", bytes.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("prepared HTTP preview status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if !errors.Is(previewReceiptErr, storepkg.ErrRevisionCommandInProgress) {
		t.Fatalf("matching HTTP preview receipt bypassed prepared owner: %v", previewReceiptErr)
	}
	active, err := st.Revisions.Active()
	if err != nil || active == nil {
		t.Fatalf("prepared HTTP preview lost its active revision: active=%+v err=%v", active, err)
	}
	runtime, err := st.Adaptation.LoadRevisionRuntime()
	if err != nil || runtime == nil {
		t.Fatalf("prepared HTTP preview lost runtime: runtime=%+v err=%v", runtime, err)
	}
	base, err := st.Adaptation.LoadPlan()
	if err != nil || base == nil {
		t.Fatalf("load HTTP formal plan: plan=%+v err=%v", base, err)
	}
	formalSnapshot, err := st.CaptureAdaptationFormalSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	progress, err := st.Progress.Load()
	if err != nil || progress == nil {
		t.Fatalf("load HTTP progress: progress=%+v err=%v", progress, err)
	}
	layered := webPreparedOwnerLayeredFixture("http-formal-owner")
	var staleOwner *storepkg.RevisionStore
	if err := st.WithPreparedAdaptationRevisionCommand("http-stale-formal", "publish", "http-stale-formal-fingerprint", func(owner *storepkg.RevisionStore) error {
		staleOwner = owner
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	cancelKey := "http-owner-durable-cancel"
	cancelIdentity, err := json.Marshal(struct{ ExpectedRevision int }{active.Revision})
	if err != nil {
		t.Fatal(err)
	}
	var cancelReceiptErr, runtimeSaveErr, runtimeClearErr error
	var formalErrors []error
	var formalBytesBefore, formalBytesAfter map[string][]byte
	service.SetCommandPreparedHookForTesting(func() {
		formalBytesBefore = webAdaptationProjectBytes(t, st.Dir())
		cancelReceiptErr = competitor.SaveAdaptationRevisionServiceReceipt(
			competitor.Revisions, cancelKey, "cancel", domain.ContentSignature(cancelIdentity), active,
		)
		corrupted := *runtime
		corrupted.Paused = true
		runtimeSaveErr = competitor.SaveAdaptationRevisionRuntime(competitor.Revisions, corrupted)
		runtimeClearErr = competitor.ClearAdaptationRevisionRuntime(competitor.Revisions, active.ID)
		attempts := []func() error{
			func() error {
				return competitor.SaveAdaptationPlanForRevision(competitor.Revisions, candidate, active.ID)
			},
			func() error {
				return competitor.RestoreAdaptationPlanForRevision(competitor.Revisions, *base, active.ID)
			},
			func() error {
				return competitor.RestoreAdaptationFormalSnapshot(competitor.Revisions, formalSnapshot, active.ID)
			},
			func() error { return competitor.ClearAdaptationRevisionAudits(competitor.Revisions, active.ID) },
			func() error {
				return competitor.SaveAdaptationRevisionProgress(competitor.Revisions, progress, active.ID)
			},
			func() error { return st.SaveAdaptationPlanForRevision(otherProject.Revisions, candidate, active.ID) },
			func() error { return st.SaveAdaptationPlanForRevision(staleOwner, candidate, active.ID) },
			func() error {
				return st.PublishLayeredStructureForRevision(nil, layered, "http-forged-layered-publish")
			},
			func() error { return st.RestoreLayeredStructureForRevision(nil, layered, progress) },
		}
		formalErrors = make([]error, len(attempts))
		var wg sync.WaitGroup
		for index := range attempts {
			wg.Add(1)
			go func(index int) {
				defer wg.Done()
				formalErrors[index] = attempts[index]()
			}(index)
		}
		wg.Wait()
		formalBytesAfter = webAdaptationProjectBytes(t, st.Dir())
	})
	cancelBody, _ := json.Marshal(webAdaptationRevisionCommandRequest{Action: "cancel", ExpectedRevision: active.Revision, IdempotencyKey: cancelKey})
	cancelled := postWebAdaptationRevisionCommand(t, server, manifest.ID, cancelBody)
	for name, forged := range map[string]error{
		"matching cancel receipt": cancelReceiptErr,
		"runtime save":            runtimeSaveErr,
		"runtime clear":           runtimeClearErr,
	} {
		if !errors.Is(forged, storepkg.ErrRevisionCommandInProgress) {
			t.Fatalf("forged HTTP %s bypassed prepared owner: %v", name, forged)
		}
	}
	for index, forged := range formalErrors {
		if !errors.Is(forged, storepkg.ErrRevisionCommandInProgress) {
			t.Fatalf("forged HTTP formal write %d bypassed prepared owner: %v", index, forged)
		}
	}
	if !reflect.DeepEqual(formalBytesBefore, formalBytesAfter) {
		t.Fatal("rejected HTTP same-session/cross-project/stale races changed project bytes")
	}
	if active, err := st.Revisions.Active(); err != nil || active != nil {
		t.Fatalf("HTTP terminal owner did not clear session: active=%+v err=%v", active, err)
	}
	if persisted, err := st.Adaptation.LoadRevisionRuntime(); err != nil || persisted != nil {
		t.Fatalf("HTTP terminal owner did not clear runtime: runtime=%+v err=%v", persisted, err)
	}
	lease, err := competitor.Revisions.AcquireNormalFlow("http-owner-successor")
	if err != nil {
		t.Fatalf("HTTP terminal cleanup did not release successor: %v", err)
	}
	if err := competitor.Revisions.ReleaseNormalFlow(lease.Token); err != nil {
		t.Fatal(err)
	}
	service.SetCommandPreparedHookForTesting(nil)
	if replayed := postWebAdaptationRevisionCommand(t, server, manifest.ID, cancelBody); !reflect.DeepEqual(replayed, cancelled) {
		t.Fatalf("HTTP terminal receipt replay drifted: replay=%+v want=%+v", replayed, cancelled)
	}
}

func TestProjectAdaptationRevisionHTTPReplaysStableReceiptsBeforeMutablePreconditions(t *testing.T) {
	t.Run("structure detail and cancel", func(t *testing.T) {
		server, manifest, st, candidate := seedWebAdaptationRevisionServer(t)
		defer server.Close()
		projectSession, _, err := server.sessions.Open(manifest.ID)
		if err != nil {
			t.Fatal(err)
		}
		service := projectSession.AdaptationRevisionService()
		previewed, err := service.Preview(host.AdaptationRevisionPreviewRequest{Intent: "HTTP stable receipt", Candidate: candidate}, "http-replay-preview")
		if err != nil {
			t.Fatal(err)
		}
		session, err := service.ApproveImpact(previewed.Session.ID, previewed.Session.Revision, "http-replay-impact")
		if err != nil {
			t.Fatal(err)
		}
		session, err = service.SubmitStructureCandidate(*previewed.Preview, session, "http-replay-structure")
		if err != nil {
			t.Fatal(err)
		}
		completeWebAdaptationRuntime(t, service, st, session.ID)
		audited, err := service.RecordAuditSet(session, webAdaptationPassingEvidence(session), "http-replay-structure-audit")
		if err != nil {
			t.Fatal(err)
		}

		approveBody, _ := json.Marshal(webAdaptationRevisionCommandRequest{Action: "approve_stage", ExpectedRevision: audited.Revision, IdempotencyKey: "http-structure-approval"})
		approved := postWebAdaptationRevisionCommand(t, server, manifest.ID, approveBody)
		if _, err := service.RunBatchCommand(approved.ID, domain.AdaptationRevisionBatchStart, "adaptation-batch-001", ""); err != nil {
			t.Fatal(err)
		}
		if replay := postWebAdaptationRevisionCommand(t, server, manifest.ID, approveBody); !reflect.DeepEqual(replay, approved) {
			t.Fatalf("structure approval HTTP replay drifted after runtime progress: got=%+v want=%+v", replay, approved)
		}

		detailBody, _ := json.Marshal(webAdaptationRevisionCommandRequest{Action: "submit_details", ExpectedRevision: approved.Revision, IdempotencyKey: "http-details", Candidate: candidate})
		detailed := postWebAdaptationRevisionCommand(t, server, manifest.ID, detailBody)
		if _, err := service.RunBatchCommand(detailed.ID, domain.AdaptationRevisionBatchStart, "adaptation-batch-001", ""); err != nil {
			t.Fatal(err)
		}
		if replay := postWebAdaptationRevisionCommand(t, server, manifest.ID, detailBody); !reflect.DeepEqual(replay, detailed) {
			t.Fatalf("detail HTTP replay drifted after runtime progress: got=%+v want=%+v", replay, detailed)
		}

		cancelBody, _ := json.Marshal(webAdaptationRevisionCommandRequest{Action: "cancel", ExpectedRevision: detailed.Revision, IdempotencyKey: "http-cancel"})
		cancelled := postWebAdaptationRevisionCommand(t, server, manifest.ID, cancelBody)
		if active, err := st.Revisions.Active(); err != nil || active != nil {
			t.Fatalf("cancel did not remove the active revision: active=%+v err=%v", active, err)
		}
		lease, err := storepkg.NewStore(st.Dir()).Revisions.AcquireNormalFlow("http-cancel-successor")
		if err != nil {
			t.Fatalf("HTTP cancel returned before receipt ownership cleanup: %v", err)
		}
		if err := st.Revisions.ReleaseNormalFlow(lease.Token); err != nil {
			t.Fatal(err)
		}
		if replay := postWebAdaptationRevisionCommand(t, server, manifest.ID, cancelBody); !reflect.DeepEqual(replay, cancelled) {
			t.Fatalf("cancel HTTP replay drifted after terminal cleanup: got=%+v want=%+v", replay, cancelled)
		}
	})

	t.Run("publish", func(t *testing.T) {
		server, manifest, st, candidate := seedWebAdaptationRevisionServer(t)
		defer server.Close()
		projectSession, _, err := server.sessions.Open(manifest.ID)
		if err != nil {
			t.Fatal(err)
		}
		service := projectSession.AdaptationRevisionService()
		previewed, err := service.Preview(host.AdaptationRevisionPreviewRequest{Intent: "HTTP publish receipt", Candidate: candidate}, "http-publish-preview")
		if err != nil {
			t.Fatal(err)
		}
		session, err := service.ApproveImpact(previewed.Session.ID, previewed.Session.Revision, "http-publish-impact")
		if err != nil {
			t.Fatal(err)
		}
		session, err = service.SubmitStructureCandidate(*previewed.Preview, session, "http-publish-structure")
		if err != nil {
			t.Fatal(err)
		}
		completeWebAdaptationRuntime(t, service, st, session.ID)
		session, err = service.RecordAuditSet(session, webAdaptationPassingEvidence(session), "http-publish-structure-audit")
		if err != nil {
			t.Fatal(err)
		}
		session, err = service.ApproveStage(session, "http-publish-structure-approve")
		if err != nil {
			t.Fatal(err)
		}
		session, err = service.SubmitDetailedOutlineCandidate(candidate, session, "http-publish-details")
		if err != nil {
			t.Fatal(err)
		}
		completeWebAdaptationRuntime(t, service, st, session.ID)
		session, err = service.RecordAuditSet(session, webAdaptationPassingEvidence(session), "http-publish-details-audit")
		if err != nil {
			t.Fatal(err)
		}
		session, err = service.ApproveStage(session, "http-publish-details-approve")
		if err != nil {
			t.Fatal(err)
		}

		publishBody, _ := json.Marshal(webAdaptationRevisionCommandRequest{Action: "publish", ExpectedRevision: session.Revision, IdempotencyKey: "http-publish", Preview: previewed.Preview})
		published := postWebAdaptationRevisionCommand(t, server, manifest.ID, publishBody)
		if active, err := st.Revisions.Active(); err != nil || active != nil {
			t.Fatalf("publish did not remove the active revision: active=%+v err=%v", active, err)
		}
		lease, err := storepkg.NewStore(st.Dir()).Revisions.AcquireNormalFlow("http-publish-successor")
		if err != nil {
			t.Fatalf("HTTP publish returned before receipt ownership cleanup: %v", err)
		}
		if err := st.Revisions.ReleaseNormalFlow(lease.Token); err != nil {
			t.Fatal(err)
		}
		if replay := postWebAdaptationRevisionCommand(t, server, manifest.ID, publishBody); !reflect.DeepEqual(replay, published) {
			t.Fatalf("publish HTTP replay drifted after terminal cleanup: got=%+v want=%+v", replay, published)
		}
	})
}

func TestProjectAdaptationRevisionHTTPReadsCannotRecoverPendingMigration(t *testing.T) {
	server, manifest, st, candidate := seedWebAdaptationRevisionServer(t)
	defer server.Close()
	projectSession, _, err := server.sessions.Open(manifest.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := projectSession.AdaptationRevisionService().Preview(host.AdaptationRevisionPreviewRequest{Intent: "fence HTTP reads", Candidate: candidate}, "http-read-preview"); err != nil {
		t.Fatal(err)
	}
	migrationLog := filepath.Join(st.Dir(), "meta", "structure", "migration.json")
	if err := os.MkdirAll(filepath.Dir(migrationLog), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(migrationLog, []byte(`{"version":1,"stage":"planned"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	before := webAdaptationProjectBytes(t, st.Dir())
	for _, path := range []string{
		"/api/projects/" + manifest.ID + "/adapt/audits",
	} {
		recorder := httptest.NewRecorder()
		server.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		if recorder.Code < http.StatusBadRequest || !bytes.Contains(recorder.Body.Bytes(), []byte("active revision")) {
			t.Fatalf("HTTP read recovered a pending migration: path=%s status=%d body=%s", path, recorder.Code, recorder.Body.String())
		}
		if after := webAdaptationProjectBytes(t, st.Dir()); !reflect.DeepEqual(before, after) {
			t.Fatalf("rejected HTTP read changed the pending formal/derived snapshot: %s", path)
		}
	}
}

func seedWebAdaptationRevisionServer(t *testing.T) (*Server, ProjectManifest, *storepkg.Store, domain.AdaptationPlan) {
	t.Helper()
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	manifest, err := server.store.CreateProject("Adaptation HTTP Receipt")
	if err != nil {
		server.Close()
		t.Fatal(err)
	}
	st := storepkg.NewStore(manifest.OutputDir)
	base, source := webAdaptationRevisionFixture(manifest.ID)
	if err := st.Adaptation.SaveSourceManifest(source); err != nil {
		server.Close()
		t.Fatal(err)
	}
	if err := st.Adaptation.SavePlan(base); err != nil {
		server.Close()
		t.Fatal(err)
	}
	report := adaptaudit.Audit(adaptaudit.Input{Mode: adaptaudit.ModeArc, Scope: adaptaudit.Scope{TargetFrom: 1, TargetTo: 2}})
	if err := st.Adaptation.SaveAuditReport(report); err != nil {
		server.Close()
		t.Fatal(err)
	}
	if err := st.Progress.Save(&domain.Progress{NovelName: "adaptation", Phase: domain.PhaseWriting, Flow: domain.FlowWriting, TotalChapters: 2}); err != nil {
		server.Close()
		t.Fatal(err)
	}
	formal, err := st.Adaptation.LoadPlan()
	if err != nil || formal == nil {
		server.Close()
		t.Fatalf("load seeded adaptation plan: plan=%+v err=%v", formal, err)
	}
	candidate := *formal
	candidate.TargetEventLedger = append(candidate.TargetEventLedger, domain.AdaptationEvent{ID: "added-event", Description: "bridge", Origin: domain.AdaptationEventOriginAdded, Required: true, DependsOn: []string{"source-event-2"}})
	addedID := domain.LegacyStructureID(manifest.ID, domain.StructureKindChapter, "chapter/added")
	candidate.Chapters = append(candidate.Chapters, domain.AdaptationChapterPlan{OutlineEntry: domain.OutlineEntry{ID: addedID, Chapter: 3, Title: "Bridge", CoreEvent: "bridge", Hook: "hook", Scenes: []string{"scene"}}, Chapter: 3, Title: "Bridge", IsAdded: true, AddedEventIDs: []string{"added-event"}, CoverageNote: "new story only", TargetRunes: 3500, TargetMinRunes: 2500, TargetMaxRunes: 4500, RequiredChanges: []string{"bridge"}, ForbiddenMoves: []string{"keep source"}})
	candidate.Volumes[0].TargetTo = 3
	candidate.TargetTotalRunes += 3500
	candidate.TargetMaxRunes += 4500
	return server, manifest, st, candidate
}

func postWebAdaptationRevisionCommand(t *testing.T, server *Server, projectID string, body []byte) *domain.RevisionSession {
	t.Helper()
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/adapt/revision/command", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("adaptation revision command status=%d body=%s request=%s", recorder.Code, recorder.Body.String(), body)
	}
	var response struct {
		Revision *domain.RevisionSession `json:"revision"`
	}
	if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response.Revision == nil {
		t.Fatalf("adaptation revision command returned no revision: %s", recorder.Body.String())
	}
	return response.Revision
}

func completeWebAdaptationRuntime(t *testing.T, service *host.AdaptationRevisionService, st *storepkg.Store, sessionID string) {
	t.Helper()
	runtime, err := st.Adaptation.LoadRevisionRuntime()
	if err != nil || runtime == nil {
		t.Fatalf("load adaptation runtime: runtime=%+v err=%v", runtime, err)
	}
	for _, batch := range runtime.BatchPlan.Batches {
		for _, command := range []domain.AdaptationRevisionBatchCommand{domain.AdaptationRevisionBatchStart, domain.AdaptationRevisionBatchGenerated, domain.AdaptationRevisionBatchAuditPass} {
			if _, err := service.RunBatchCommand(sessionID, command, batch.ID, "passed"); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, review := range runtime.BatchPlan.VolumeReviews {
		if _, err := service.RunBatchCommand(sessionID, domain.AdaptationRevisionVolumeReviewStart, review.ScopeID, ""); err != nil {
			t.Fatal(err)
		}
		if _, err := service.RunBatchCommand(sessionID, domain.AdaptationRevisionVolumeReviewPass, review.ScopeID, "passed"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := service.RunBatchCommand(sessionID, domain.AdaptationRevisionGlobalReviewStart, "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := service.RunBatchCommand(sessionID, domain.AdaptationRevisionGlobalReviewPass, "", "passed"); err != nil {
		t.Fatal(err)
	}
}

func webAdaptationPassingEvidence(session *domain.RevisionSession) []domain.RevisionAuditEvidence {
	evidence := make([]domain.RevisionAuditEvidence, 0, len(session.AuditExpectations))
	for _, expected := range session.AuditExpectations {
		evidence = append(evidence, domain.RevisionAuditEvidence{Scope: expected.Scope, ScopeID: expected.ScopeID, FromChapter: expected.FromChapter, ToChapter: expected.ToChapter, ContentSignature: expected.ContentSignature, Passed: true})
	}
	return evidence
}

func webPreparedOwnerLayeredFixture(project string) []domain.VolumeOutline {
	volumeID := domain.LegacyStructureID(project, domain.StructureKindVolume, "volume-1")
	arcID := domain.LegacyStructureID(project, domain.StructureKindArc, "volume-1/arc-1")
	chapterID := domain.LegacyStructureID(project, domain.StructureKindChapter, "volume-1/arc-1/chapter-1")
	return []domain.VolumeOutline{{
		ID: volumeID, Index: 1, Title: "Volume", Theme: "theme",
		Arcs: []domain.ArcOutline{{
			ID: arcID, Index: 1, Title: "Arc", Goal: "goal",
			Chapters: []domain.OutlineEntry{{ID: chapterID, Chapter: 1, Title: "One", CoreEvent: "event", Hook: "hook", Scenes: []string{"scene"}}},
		}},
	}}
}

func webAdaptationProjectBytes(t *testing.T, dir string) map[string][]byte {
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
		if strings.HasSuffix(rel, ".lock") {
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

func webAdaptationRevisionFixture(projectID string) (domain.AdaptationPlan, domain.AdaptationSourceManifest) {
	volumeID := domain.LegacyStructureID(projectID, domain.StructureKindVolume, "volume/1")
	chapters := []domain.AdaptationChapterPlan{
		{OutlineEntry: domain.OutlineEntry{ID: domain.LegacyStructureID(projectID, domain.StructureKindChapter, "chapter/1"), Chapter: 1, Title: "One", CoreEvent: "meeting", Hook: "clue", Scenes: []string{"meet"}}, Chapter: 1, Title: "One", SourceChapters: []int{1}, SourceRange: domain.SourceRange{From: 1, To: 1}, SourceRunes: 1000, EventIDs: []string{"source-event-1"}, TargetRunes: 4000, TargetMinRunes: 3000, TargetMaxRunes: 5000, PreserveEvents: []string{"meeting"}, RequiredChanges: []string{"adapt"}, ForbiddenMoves: []string{"keep meeting"}},
		{OutlineEntry: domain.OutlineEntry{ID: domain.LegacyStructureID(projectID, domain.StructureKindChapter, "chapter/2"), Chapter: 2, Title: "Two", CoreEvent: "answer", Hook: "ending", Scenes: []string{"answer"}}, Chapter: 2, Title: "Two", SourceChapters: []int{2}, SourceRange: domain.SourceRange{From: 2, To: 2}, SourceRunes: 1000, EventIDs: []string{"source-event-2"}, DependsOnEventIDs: []string{"source-event-1"}, TargetRunes: 4000, TargetMinRunes: 3000, TargetMaxRunes: 5000, PreserveEvents: []string{"answer"}, RequiredChanges: []string{"adapt"}, ForbiddenMoves: []string{"keep answer"}},
	}
	plan := domain.AdaptationPlan{Granularity: domain.AdaptationGranularityArc, ModePolicy: domain.AdaptationModePolicyForGranularity(domain.AdaptationGranularityArc), Status: domain.AdaptationPlanStatusConfirmed, RewritePolicy: domain.AdaptationRewriteFullRewrite, Brief: "preserve source", TargetTotalRunes: 8000, TargetMinRunes: 6000, TargetMaxRunes: 10000, SourceEvents: []domain.AdaptationEvent{{ID: "source-event-1", Description: "meeting", Origin: domain.AdaptationEventOriginSource, Importance: domain.AdaptationEventMainline, SourceChapter: 1, Required: true}, {ID: "source-event-2", Description: "answer", Origin: domain.AdaptationEventOriginSource, SourceChapter: 2, Required: true, DependsOn: []string{"source-event-1"}}}, Volumes: []domain.AdaptationVolumePlan{{ID: volumeID, Index: 1, Title: "Source", TargetFrom: 1, TargetTo: 2, SourceFrom: 1, SourceTo: 2, MainlineEventIDs: []string{"source-event-1"}}}, Chapters: chapters}
	manifest := domain.AdaptationSourceManifest{ChapterCount: 2, Chapters: []domain.AdaptationSource{{Chapter: 1, Title: "One", SHA256: "one", Runes: 1000}, {Chapter: 2, Title: "Two", SHA256: "two", Runes: 1000}}}
	return plan, manifest
}
