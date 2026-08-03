package web

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/voocel/ainovel-cli/assets"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/host"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
)

func TestAdaptationDiscussionEvidenceUsesStableTaskAndFailsClosedOnSourceDrift(t *testing.T) {
	st := storepkg.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	source, err := st.Adaptation.SaveSourceChapter(7, "Source", strings.Repeat("evidence", 2000))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Adaptation.SaveSourceManifest(domain.AdaptationSourceManifest{SourcePath: "source.txt", ChapterCount: 1, Chapters: []domain.AdaptationSource{source}}); err != nil {
		t.Fatal(err)
	}
	if err := st.Adaptation.SavePlan(domain.AdaptationPlan{Granularity: domain.AdaptationGranularityChapter, Brief: "bound", Chapters: []domain.AdaptationChapterPlan{{Chapter: 1, Title: "Target", SourceChapters: []int{7}}}}); err != nil {
		t.Fatal(err)
	}
	plan, err := st.Adaptation.LoadPlan()
	if err != nil || plan == nil || len(plan.Chapters) != 1 || plan.Chapters[0].ID == "" {
		t.Fatalf("confirmed plan=%+v err=%v", plan, err)
	}
	stableID := plan.Chapters[0].ID
	planPayload, _ := json.Marshal(plan)
	manifest, _ := st.Adaptation.LoadSourceManifest()
	manifestPayload, _ := json.Marshal(manifest)
	planSignature, manifestSignature := domain.ContentSignature(planPayload), domain.ContentSignature(manifestPayload)
	evidence, units, err := adaptationDiscussionEvidence(st, stableID, 10000, planSignature, manifestSignature)
	if err != nil || len(evidence) != 1 || units <= 0 || units > 10000 {
		t.Fatalf("evidence=%+v units=%d err=%v", evidence, units, err)
	}
	encodedEvidence, _ := json.Marshal(evidence)
	for _, forbidden := range []string{"content_signature", "manifest_signature", "ledger", stableID} {
		if bytes.Contains(encodedEvidence, []byte(forbidden)) {
			t.Fatalf("visible adaptation evidence leaked %q: %s", forbidden, encodedEvidence)
		}
	}
	if _, _, err := adaptationDiscussionEvidence(st, "ch_ffffffffffffffffffffffffffffffff", 10000, planSignature, manifestSignature); err == nil || !strings.Contains(err.Error(), "stable ID") {
		t.Fatalf("display-order fallback remained available: %v", err)
	}
	if _, err := st.Adaptation.SaveSourceChapter(7, "Source", "drifted bytes"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := adaptationDiscussionEvidence(st, stableID, 10000, planSignature, manifestSignature); err == nil || !strings.Contains(err.Error(), "signature drift") {
		t.Fatalf("source drift was accepted: %v", err)
	}
}

func TestAdaptationDiscussionEvidenceFailsClosedOnZeroCoverageAndExplainsAddedTasks(t *testing.T) {
	st := storepkg.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	source, err := st.Adaptation.SaveSourceChapter(1, "Source", "source prose")
	if err != nil {
		t.Fatal(err)
	}
	manifest := domain.AdaptationSourceManifest{SourcePath: "source.txt", ChapterCount: 1, Chapters: []domain.AdaptationSource{source}}
	if err := st.Adaptation.SaveSourceManifest(manifest); err != nil {
		t.Fatal(err)
	}
	plan := domain.AdaptationPlan{Granularity: domain.AdaptationGranularityChapter, Brief: "bound", Chapters: []domain.AdaptationChapterPlan{{OutlineEntry: domain.OutlineEntry{ID: "ch_0123456789abcdef0123456789abcdef", Chapter: 1}, Chapter: 1, Title: "Target"}}}
	if err := st.Adaptation.SavePlan(plan); err != nil {
		t.Fatal(err)
	}
	loaded, _ := st.Adaptation.LoadPlan()
	manifestLoaded, _ := st.Adaptation.LoadSourceManifest()
	planPayload, _ := json.Marshal(loaded)
	manifestPayload, _ := json.Marshal(manifestLoaded)
	if _, _, err := adaptationDiscussionEvidence(st, loaded.Chapters[0].ID, 10000, domain.ContentSignature(planPayload), domain.ContentSignature(manifestPayload)); err == nil || !strings.Contains(err.Error(), "zero source coverage") {
		t.Fatalf("non-added zero coverage was accepted: %v", err)
	}
	loaded.Chapters[0].IsAdded = true
	loaded.Chapters[0].CoverageNote = "原创过渡章节"
	if err := st.Adaptation.SavePlan(*loaded); err != nil {
		t.Fatal(err)
	}
	loaded, _ = st.Adaptation.LoadPlan()
	planPayload, _ = json.Marshal(loaded)
	evidence, _, err := adaptationDiscussionEvidence(st, loaded.Chapters[0].ID, 10000, domain.ContentSignature(planPayload), domain.ContentSignature(manifestPayload))
	if err != nil || len(evidence) != 1 || evidence[0]["scope"] != "新增目标章节，无原著范围" {
		t.Fatalf("added task explanation=%+v err=%v", evidence, err)
	}
}

func TestRestoreConfirmationRequiresSignedPreview(t *testing.T) {
	request := manuscriptRestoreRequest{RevisionID: "rev_0123456789abcdef0123456789abcdef", ChapterID: "ch_0123456789abcdef0123456789abcdef", ExpectedContentSignature: strings.Repeat("a", 64), IdempotencyKey: "restore-key"}
	if err := validateRestoreRequest(request, true); err == nil || !strings.Contains(err.Error(), "preview_signature") {
		t.Fatalf("unsigned confirmation accepted: %v", err)
	}
	baseline := domain.ManuscriptBaseline{ChapterID: request.ChapterID, CurrentProseSHA256: strings.Repeat("b", 64), ApprovedOutlineSHA256: strings.Repeat("c", 64), StructureSignature: strings.Repeat("d", 64), Mode: domain.RevisionModeNormal}
	candidate := domain.ManuscriptCandidate{Prose: domain.ManuscriptContentRef{SHA256: request.ExpectedContentSignature}}
	first := manuscriptRestorePreviewSignature(request.RevisionID, request.ChapterID, baseline, candidate)
	baseline.CurrentProseSHA256 = strings.Repeat("e", 64)
	second := manuscriptRestorePreviewSignature(request.RevisionID, request.ChapterID, baseline, candidate)
	if first == second {
		t.Fatal("preview signature did not bind current formal prose")
	}
}

type webRestoreContractAuditor struct{}

func (webRestoreContractAuditor) AuditManuscriptCandidate(context.Context, domain.ManuscriptRevisionRuntime, domain.ManuscriptCandidate) (bool, string, error) {
	return true, "passed", nil
}

func (webRestoreContractAuditor) AuditCandidateContract(_ context.Context, task host.ManuscriptContractAuditTask, prose string) (host.ManuscriptContractAuditDecision, error) {
	scenes := task.Outline.Scenes
	contract := domain.NarrativeContract{ChapterID: task.ChapterID, OutlineSHA256: task.OutlineSHA256, Desire: task.Outline.Title, Obstacle: task.Outline.CoreEvent, Choice: scenes[0], Cost: scenes[1], Result: scenes[len(scenes)-1], ExitState: task.Outline.Hook, FutureCommitments: append([]string(nil), scenes...), StateSHA256: task.ProtectedStateSHA256}
	fields := []string{"desire", "obstacle", "choice", "cost", "result", "exit_state", "future_commitments"}
	runes := []rune(prose)
	evidence := make([]host.ManuscriptEvidenceLocation, 0, len(fields))
	for index, field := range fields {
		evidence = append(evidence, host.ManuscriptEvidenceLocation{Field: field, StartRune: index, EndRune: index + 1, Quote: string(runes[index : index+1])})
	}
	return host.ManuscriptContractAuditDecision{Role: "contract_locator", TaskSignature: task.Signature, CandidateSHA256: task.CandidateSHA256, Contract: contract, Evidence: evidence}, nil
}

func (webRestoreContractAuditor) VerifyCandidateContract(_ context.Context, task host.ManuscriptContractVerificationTask, locator host.ManuscriptContractAuditDecision, approved domain.NarrativeContract, _ string) (host.ManuscriptContractVerificationDecision, error) {
	receipts := make([]host.ManuscriptContractVerificationReceipt, 0, len(locator.Evidence))
	for _, location := range locator.Evidence {
		value := webRestoreContractValue(locator.Contract, location.Field)
		receipts = append(receipts, host.ManuscriptContractVerificationReceipt{Field: location.Field, Value: value, ApprovedValue: webRestoreContractValue(approved, location.Field), StartRune: location.StartRune, EndRune: location.EndRune, Quote: location.Quote, Verdict: "entailed", TaskSignature: task.Signature, CandidateSHA256: task.CandidateSHA256})
	}
	return host.ManuscriptContractVerificationDecision{Role: "contract_verifier", TaskSignature: task.Signature, CandidateSHA256: task.CandidateSHA256, Receipts: receipts}, nil
}

func webRestoreContractValue(contract domain.NarrativeContract, field string) string {
	switch field {
	case "desire":
		return contract.Desire
	case "obstacle":
		return contract.Obstacle
	case "choice":
		return contract.Choice
	case "cost":
		return contract.Cost
	case "result":
		return contract.Result
	case "exit_state":
		return contract.ExitState
	case "future_commitments":
		return strings.Join(contract.FutureCommitments, "\n")
	default:
		return ""
	}
}

func webRestoreSidecars() map[string]json.RawMessage {
	return map[string]json.RawMessage{
		"summary": json.RawMessage(`{"summary":"summary"}`), "events": json.RawMessage(`["event"]`),
		"timeline": json.RawMessage(`[{"event":"event"}]`), "cast_state": json.RawMessage(`[{"entity":"hero","field":"status","new_value":"ready"}]`),
		"relationships": json.RawMessage(`[{"character_a":"hero","character_b":"ally","relation":"trusted"}]`),
		"foreshadow":    json.RawMessage(`[{"id":"seed","description":"seed","status":"planted"}]`),
		"world_facts":   json.RawMessage(`[{"category":"other","rule":"rule","boundary":"boundary"}]`),
		"carry_forward": json.RawMessage(`{"character_snapshots":[{"name":"hero","status":"ready","motivation":"act"}]}`),
	}
}

func TestManuscriptRestoreHandlerBindsPreviewAndReplaysIdempotently(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), testTempDir(t))
	defer server.Close()
	manifest, err := server.store.CreateProject("restore handler")
	if err != nil {
		t.Fatal(err)
	}
	st := storepkg.NewStore(manifest.OutputDir)
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	chapterID := "ch_0123456789abcdef0123456789abcdef"
	outline := domain.OutlineEntry{ID: chapterID, Chapter: 1, Title: "goal", CoreEvent: "obstacle", Hook: "exit", Scenes: []string{"choice", "cost"}}
	if err := st.Outline.SaveOutline([]domain.OutlineEntry{outline}); err != nil {
		t.Fatal(err)
	}
	if err := st.Progress.Init("Book", 1); err != nil {
		t.Fatal(err)
	}
	if err := st.Progress.UpdatePhase(domain.PhaseWriting); err != nil {
		t.Fatal(err)
	}
	if err := st.Drafts.SaveFinalChapter(1, "formal prose"); err != nil {
		t.Fatal(err)
	}
	auditor := webRestoreContractAuditor{}
	service := host.NewManuscriptRevisionServiceWithAuditor(st, auditor)
	preview, err := service.Preview(host.ManuscriptPreviewRequest{ChapterID: chapterID, Instruction: "historical"}, "source-preview")
	if err != nil {
		t.Fatal(err)
	}
	source, err := service.SubmitCandidate(preview.Runtime.RevisionID, preview.Runtime.Revision, "source-candidate", host.ManuscriptCandidateInput{ChapterID: chapterID, Prose: "ABCDEFG", Sidecars: webRestoreSidecars()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Cancel(source.RevisionID, source.Revision, "source-cancel"); err != nil {
		t.Fatal(err)
	}
	fake := installFakeSession(t, server, manifest)
	fake.manuscriptService = service
	candidateSignature := source.Candidates[0].Prose.SHA256
	previewBody := []byte(`{"revision_id":"` + source.RevisionID + `","chapter_id":"` + chapterID + `","expected_content_signature":"` + candidateSignature + `"}`)
	response := requestWorkspace(t, server, http.MethodPost, "/api/projects/"+manifest.ID+"/manuscript/workspace/restore/preview", previewBody, "")
	if response.Code != http.StatusOK {
		t.Fatalf("preview status=%d body=%s", response.Code, response.Body.String())
	}
	var decoded struct {
		Preview struct {
			Signature string `json:"preview_signature"`
		} `json:"preview"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &decoded); err != nil || decoded.Preview.Signature == "" {
		t.Fatalf("preview=%s err=%v", response.Body.String(), err)
	}
	confirm := func(signature, key string) *httptest.ResponseRecorder {
		body := []byte(`{"revision_id":"` + source.RevisionID + `","chapter_id":"` + chapterID + `","expected_content_signature":"` + candidateSignature + `","idempotency_key":"` + key + `","preview_signature":"` + signature + `"}`)
		return requestWorkspace(t, server, http.MethodPost, "/api/projects/"+manifest.ID+"/manuscript/workspace/restore", body, "")
	}
	missingPreview := requestWorkspace(t, server, http.MethodPost, "/api/projects/"+manifest.ID+"/manuscript/workspace/restore", []byte(`{"revision_id":"`+source.RevisionID+`","chapter_id":"`+chapterID+`","expected_content_signature":"`+candidateSignature+`","idempotency_key":"missing-preview"}`), "")
	if missingPreview.Code != http.StatusBadRequest {
		t.Fatalf("missing preview status=%d body=%s", missingPreview.Code, missingPreview.Body.String())
	}
	unknownField := requestWorkspace(t, server, http.MethodPost, "/api/projects/"+manifest.ID+"/manuscript/workspace/restore", []byte(`{"revision_id":"`+source.RevisionID+`","chapter_id":"`+chapterID+`","expected_content_signature":"`+candidateSignature+`","idempotency_key":"unknown-field","preview_signature":"`+decoded.Preview.Signature+`","source_chapter":1}`), "")
	if unknownField.Code != http.StatusBadRequest {
		t.Fatalf("unknown confirm field status=%d body=%s", unknownField.Code, unknownField.Body.String())
	}
	invalidStableID := requestWorkspace(t, server, http.MethodPost, "/api/projects/"+manifest.ID+"/manuscript/workspace/restore", []byte(`{"revision_id":"`+source.RevisionID+`","chapter_id":"1","expected_content_signature":"`+candidateSignature+`","idempotency_key":"invalid-id","preview_signature":"`+decoded.Preview.Signature+`"}`), "")
	if invalidStableID.Code != http.StatusBadRequest {
		t.Fatalf("numeric chapter ID status=%d body=%s", invalidStableID.Code, invalidStableID.Body.String())
	}
	forged := confirm("forged", "restore-forged")
	if forged.Code != http.StatusConflict || !bytes.Contains(forged.Body.Bytes(), []byte(`"code":"preview_stale"`)) {
		t.Fatalf("forged status=%d body=%s", forged.Code, forged.Body.String())
	}
	driftedOutline := outline
	driftedOutline.Title = "changed outline"
	if err := st.Outline.SaveOutline([]domain.OutlineEntry{driftedOutline}); err != nil {
		t.Fatal(err)
	}
	staleOutline := confirm(decoded.Preview.Signature, "restore-stale-outline")
	if staleOutline.Code != http.StatusConflict || !bytes.Contains(staleOutline.Body.Bytes(), []byte(`"code":"preview_stale"`)) {
		t.Fatalf("stale outline status=%d body=%s", staleOutline.Code, staleOutline.Body.String())
	}
	if err := st.Outline.SaveOutline([]domain.OutlineEntry{outline}); err != nil {
		t.Fatal(err)
	}
	secondOutline := domain.OutlineEntry{ID: "ch_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", Chapter: 2, Title: "second", CoreEvent: "event", Hook: "hook", Scenes: []string{"choice", "cost"}}
	if err := st.Outline.SaveOutline([]domain.OutlineEntry{outline, secondOutline}); err != nil {
		t.Fatal(err)
	}
	staleStructure := confirm(decoded.Preview.Signature, "restore-stale-structure")
	if staleStructure.Code != http.StatusConflict || !bytes.Contains(staleStructure.Body.Bytes(), []byte(`"code":"preview_stale"`)) {
		t.Fatalf("stale structure status=%d body=%s", staleStructure.Code, staleStructure.Body.String())
	}
	if err := st.Outline.SaveOutline([]domain.OutlineEntry{outline}); err != nil {
		t.Fatal(err)
	}
	modeSource, err := st.Adaptation.SaveSourceChapter(1, "source", "source prose")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Adaptation.SaveSourceManifest(domain.AdaptationSourceManifest{SourcePath: "source.txt", ChapterCount: 1, Chapters: []domain.AdaptationSource{modeSource}}); err != nil {
		t.Fatal(err)
	}
	if err := st.Adaptation.SavePlan(domain.AdaptationPlan{Granularity: domain.AdaptationGranularityChapter, Brief: "mode drift", Chapters: []domain.AdaptationChapterPlan{{OutlineEntry: outline, Chapter: 1, Title: outline.Title, SourceChapters: []int{1}, CoverageNote: "covered"}}}); err != nil {
		t.Fatal(err)
	}
	staleMode := confirm(decoded.Preview.Signature, "restore-stale-mode")
	if staleMode.Code != http.StatusConflict || !bytes.Contains(staleMode.Body.Bytes(), []byte(`"code":"preview_stale"`)) {
		t.Fatalf("stale mode status=%d body=%s", staleMode.Code, staleMode.Body.String())
	}
	if err := os.RemoveAll(filepath.Join(st.Dir(), "meta", "adaptation")); err != nil {
		t.Fatal(err)
	}
	if err := st.Drafts.SaveFinalChapter(1, "formal prose changed after preview"); err != nil {
		t.Fatal(err)
	}
	staleCurrent := confirm(decoded.Preview.Signature, "restore-stale-current")
	if staleCurrent.Code != http.StatusConflict || !bytes.Contains(staleCurrent.Body.Bytes(), []byte(`"code":"preview_stale"`)) {
		t.Fatalf("stale current status=%d body=%s", staleCurrent.Code, staleCurrent.Body.String())
	}
	if err := st.Drafts.SaveFinalChapter(1, "formal prose"); err != nil {
		t.Fatal(err)
	}
	first := confirm(decoded.Preview.Signature, "restore-confirm")
	if first.Code != http.StatusAccepted {
		t.Fatalf("confirm status=%d body=%s", first.Code, first.Body.String())
	}
	var firstRevision struct {
		Revision struct {
			ID string `json:"revision_id"`
		} `json:"revision"`
	}
	if err := json.Unmarshal(first.Body.Bytes(), &firstRevision); err != nil {
		t.Fatal(err)
	}
	second := confirm(decoded.Preview.Signature, "restore-confirm")
	var secondRevision struct {
		Revision struct {
			ID string `json:"revision_id"`
		} `json:"revision"`
	}
	if err := json.Unmarshal(second.Body.Bytes(), &secondRevision); err != nil {
		t.Fatal(err)
	}
	if second.Code != http.StatusAccepted || firstRevision.Revision.ID == "" || secondRevision.Revision.ID != firstRevision.Revision.ID {
		t.Fatalf("replay first=%s second=%s status=%d", first.Body.String(), second.Body.String(), second.Code)
	}
	concurrent := confirm(decoded.Preview.Signature, "restore-concurrent")
	if concurrent.Code != http.StatusConflict || !bytes.Contains(concurrent.Body.Bytes(), []byte(`"code":"active_revision"`)) {
		t.Fatalf("concurrent status=%d body=%s", concurrent.Code, concurrent.Body.String())
	}
	active, err := st.ManuscriptRevisions.Active()
	if err != nil || active == nil || active.Stage != "audit_pending" || len(active.Candidates) != 1 {
		t.Fatalf("active=%+v err=%v", active, err)
	}
}

func TestManuscriptWorkspaceTreeChunkETagAndDiscussionSignature(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), testTempDir(t))
	defer server.Close()
	manifest, err := server.store.CreateProject("workspace reads")
	if err != nil {
		t.Fatal(err)
	}
	st := storepkg.NewStore(manifest.OutputDir)
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	chapterID := domain.LegacyStructureID(st.Dir(), domain.StructureKindChapter, "chapters/0001")
	volumeID := domain.LegacyStructureID(st.Dir(), domain.StructureKindVolume, "volume/1")
	arcID := domain.LegacyStructureID(st.Dir(), domain.StructureKindArc, "volume/1/arc/1")
	if err := st.Outline.SaveLayeredOutline([]domain.VolumeOutline{{ID: volumeID, Index: 1, Title: "Volume", Arcs: []domain.ArcOutline{{ID: arcID, Index: 1, Title: "Arc", Chapters: []domain.OutlineEntry{{ID: chapterID, Chapter: 1, Title: "Chapter", CoreEvent: "choice", Hook: "cost"}}}}}}); err != nil {
		t.Fatal(err)
	}
	if err := st.Progress.Init("Book", 1); err != nil {
		t.Fatal(err)
	}
	if err := st.Progress.UpdatePhase(domain.PhaseWriting); err != nil {
		t.Fatal(err)
	}
	if err := st.Drafts.SaveFinalChapter(1, "first\n\nsecond\n\nthird"); err != nil {
		t.Fatal(err)
	}

	tree := requestWorkspace(t, server, http.MethodGet, "/api/projects/"+manifest.ID+"/manuscript/workspace/tree", nil, "")
	if tree.Code != http.StatusOK || tree.Header().Get("ETag") == "" || !bytes.Contains(tree.Body.Bytes(), []byte(chapterID)) {
		t.Fatalf("tree status=%d body=%s", tree.Code, tree.Body.String())
	}
	treeCached := requestWorkspace(t, server, http.MethodGet, "/api/projects/"+manifest.ID+"/manuscript/workspace/tree", nil, tree.Header().Get("ETag"))
	if treeCached.Code != http.StatusNotModified {
		t.Fatalf("tree cached status=%d", treeCached.Code)
	}

	chunk := requestWorkspace(t, server, http.MethodGet, "/api/projects/"+manifest.ID+"/manuscript/workspace/chapters/"+chapterID+"/content?limit=2", nil, "")
	if chunk.Code != http.StatusOK || !bytes.Contains(chunk.Body.Bytes(), []byte(`"next_cursor":2`)) || bytes.Contains(chunk.Body.Bytes(), []byte("third")) {
		t.Fatalf("chunk status=%d body=%s", chunk.Code, chunk.Body.String())
	}
	for _, path := range []string{
		"/api/projects/" + manifest.ID + "/manuscript/workspace/chapters/" + chapterID + "/content?view=current&version=rev_0123456789abcdef0123456789abcdef",
		"/api/projects/" + manifest.ID + "/manuscript/workspace/chapters/" + chapterID + "/content?view=history&version=rev_0123456789abcdef0123456789abcdef",
		"/api/projects/" + manifest.ID + "/manuscript/workspace/chapters/" + chapterID + "/content?view=garbage",
		"/api/projects/" + manifest.ID + "/manuscript/workspace/chapters/" + chapterID + "/content?view=candidate",
		"/api/projects/" + manifest.ID + "/manuscript/workspace/chapters/1/content?view=current",
	} {
		rejected := requestWorkspace(t, server, http.MethodGet, path, nil, "")
		if rejected.Code != http.StatusBadRequest {
			t.Fatalf("chapter content whitelist accepted %s: status=%d body=%s", path, rejected.Code, rejected.Body.String())
		}
	}
	var body struct {
		Chapter struct {
			Signature string `json:"content_signature"`
		} `json:"chapter"`
	}
	if err := json.Unmarshal(chunk.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	removedDiscussion := requestWorkspace(t, server, http.MethodPost, "/api/projects/"+manifest.ID+"/manuscript/context/discuss", []byte(`{}`), "")
	if removedDiscussion.Code != http.StatusNotFound {
		t.Fatalf("removed discussion route status=%d body=%s", removedDiscussion.Code, removedDiscussion.Body.String())
	}

	outline := requestWorkspace(t, server, http.MethodGet, "/api/projects/"+manifest.ID+"/manuscript/workspace/artifacts/outline/"+chapterID, nil, "")
	if outline.Code != http.StatusOK || outline.Header().Get("ETag") == "" || !bytes.Contains(outline.Body.Bytes(), []byte(`"core_event":"choice"`)) || !bytes.Contains(outline.Body.Bytes(), []byte(`"signature"`)) {
		t.Fatalf("outline artifact status=%d body=%s", outline.Code, outline.Body.String())
	}
	outlineCached := requestWorkspace(t, server, http.MethodGet, "/api/projects/"+manifest.ID+"/manuscript/workspace/artifacts/outline/"+chapterID, nil, outline.Header().Get("ETag"))
	if outlineCached.Code != http.StatusNotModified {
		t.Fatalf("outline cached status=%d", outlineCached.Code)
	}
}

func TestRemovedManuscriptAdaptationDiscussionRoute(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), testTempDir(t))
	defer server.Close()
	manifest, err := server.store.CreateProject("adaptation discussion handler")
	if err != nil {
		t.Fatal(err)
	}
	st := storepkg.NewStore(manifest.OutputDir)
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	chapterID := "ch_abcdefabcdefabcdefabcdefabcdefab"
	entry := domain.OutlineEntry{ID: chapterID, Chapter: 1, Title: "Target", CoreEvent: "event", Hook: "hook", Scenes: []string{"choice", "cost"}}
	if err := st.Outline.SaveOutline([]domain.OutlineEntry{entry}); err != nil {
		t.Fatal(err)
	}
	if err := st.Progress.Init("Book", 1); err != nil {
		t.Fatal(err)
	}
	if err := st.Progress.UpdatePhase(domain.PhaseWriting); err != nil {
		t.Fatal(err)
	}
	if err := st.Drafts.SaveFinalChapter(1, strings.Repeat("target prose ", 700)); err != nil {
		t.Fatal(err)
	}
	sourceText := strings.Repeat("source evidence ", 700)
	source, err := st.Adaptation.SaveSourceChapter(7, "Source", sourceText)
	if err != nil {
		t.Fatal(err)
	}
	manifestContract := domain.AdaptationSourceManifest{SourcePath: "source.txt", ChapterCount: 1, Chapters: []domain.AdaptationSource{source}}
	if err := st.Adaptation.SaveSourceManifest(manifestContract); err != nil {
		t.Fatal(err)
	}
	plan := domain.AdaptationPlan{Granularity: domain.AdaptationGranularityChapter, Brief: "bound", Chapters: []domain.AdaptationChapterPlan{{OutlineEntry: entry, Chapter: 1, Title: "Target", SourceChapters: []int{7}, CoverageNote: "covered"}}}
	if err := st.Adaptation.SavePlan(plan); err != nil {
		t.Fatal(err)
	}
	if err := st.CaptureManuscriptContentProvenance(1, strings.Repeat("target prose ", 700)); err != nil {
		t.Fatal(err)
	}
	service := host.NewManuscriptRevisionService(st)
	fake := installFakeSession(t, server, manifest)
	fake.manuscriptService = service
	baseline, _, err := service.CurrentChapter(chapterID)
	if err != nil {
		t.Fatal(err)
	}
	request := func(stableID string) *httptest.ResponseRecorder {
		payload := []byte(`{"stable_id":"` + stableID + `","artifact_kind":"prose","view":"current","content_signature":"` + baseline.CurrentProseSHA256 + `","intent":"compare the selected scene"}`)
		return requestWorkspace(t, server, http.MethodPost, "/api/projects/"+manifest.ID+"/manuscript/context/discuss", payload, "")
	}

	success := request(chapterID)
	if success.Code != http.StatusNotFound {
		t.Fatalf("removed adaptation discussion status=%d body=%s", success.Code, success.Body.String())
	}
}

func TestManuscriptWorkspaceHistoryIsMetadataOnly(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), testTempDir(t))
	defer server.Close()
	manifest, err := server.store.CreateProject("workspace history")
	if err != nil {
		t.Fatal(err)
	}
	st := storepkg.NewStore(manifest.OutputDir)
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	chapterID := domain.LegacyStructureID(st.Dir(), domain.StructureKindChapter, "chapters/0001")
	if err := st.Outline.SaveOutline([]domain.OutlineEntry{{ID: chapterID, Chapter: 1, Title: "Chapter", CoreEvent: "choice", Hook: "cost"}}); err != nil {
		t.Fatal(err)
	}
	if err := st.Progress.Init("Book", 1); err != nil {
		t.Fatal(err)
	}
	if err := st.Progress.UpdatePhase(domain.PhaseWriting); err != nil {
		t.Fatal(err)
	}
	if err := st.Drafts.SaveFinalChapter(1, "formal prose must not appear in history metadata"); err != nil {
		t.Fatal(err)
	}
	fake := installFakeSession(t, server, manifest)
	fake.manuscriptService = host.NewManuscriptRevisionServiceWithRuntime(st, noChangeManuscriptWriter{}, nil)
	preview := requestWorkspace(t, server, http.MethodPost, "/api/projects/"+manifest.ID+"/manuscript/revision/preview", []byte(`{"chapter_id":"`+chapterID+`","instruction":"polish","kind":"polish","idempotency_key":"history-preview"}`), "")
	if preview.Code != http.StatusAccepted {
		t.Fatalf("preview status=%d body=%s", preview.Code, preview.Body.String())
	}
	history := requestWorkspace(t, server, http.MethodGet, "/api/projects/"+manifest.ID+"/manuscript/workspace/history?chapter_id="+chapterID+"&limit=1", nil, "")
	if history.Code != http.StatusOK || !bytes.Contains(history.Body.Bytes(), []byte(`"items"`)) || bytes.Contains(history.Body.Bytes(), []byte("formal prose must not appear")) || bytes.Contains(history.Body.Bytes(), []byte(`"candidates"`)) {
		t.Fatalf("history status=%d body=%s", history.Code, history.Body.String())
	}
	invalidPath := requestWorkspace(t, server, http.MethodPost, "/api/projects/"+manifest.ID+"/manuscript/workspace/restore/preview", []byte(`{"revision_id":"../outside","chapter_id":"`+chapterID+`","expected_content_signature":"missing"}`), "")
	if invalidPath.Code != http.StatusBadRequest || !bytes.Contains(invalidPath.Body.Bytes(), []byte("stable IDs")) {
		t.Fatalf("restore preview path rejection status=%d body=%s", invalidPath.Code, invalidPath.Body.String())
	}
	gone := requestWorkspace(t, server, http.MethodPost, "/api/projects/"+manifest.ID+"/manuscript/workspace/restore/preview", []byte(`{"revision_id":"rev_00000000000000000000000000000000","chapter_id":"`+chapterID+`","expected_content_signature":"missing"}`), "")
	if gone.Code != http.StatusGone || !bytes.Contains(gone.Body.Bytes(), []byte(`"code":"version_gone"`)) {
		t.Fatalf("restore preview gone status=%d body=%s", gone.Code, gone.Body.String())
	}
	invalidVersion := requestWorkspace(t, server, http.MethodGet, "/api/projects/"+manifest.ID+"/manuscript/workspace/versions/not-a-stable-id?chapter_id="+chapterID, nil, "")
	if invalidVersion.Code != http.StatusBadRequest {
		t.Fatalf("invalid history version status=%d body=%s", invalidVersion.Code, invalidVersion.Body.String())
	}
	goneVersion := requestWorkspace(t, server, http.MethodGet, "/api/projects/"+manifest.ID+"/manuscript/workspace/versions/rev_00000000000000000000000000000000?chapter_id="+chapterID, nil, "")
	if goneVersion.Code != http.StatusGone || !bytes.Contains(goneVersion.Body.Bytes(), []byte(`"code":"version_gone"`)) || !bytes.Contains(goneVersion.Body.Bytes(), []byte(`"action":"reload_history"`)) {
		t.Fatalf("history version gone status=%d body=%s", goneVersion.Code, goneVersion.Body.String())
	}
}

func TestManuscriptRecoveryEndpointReturnsSafeRetryableMetadata(t *testing.T) {
	st := storepkg.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	server := &Server{}
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(method, "/api/projects/p/manuscript/workspace/recovery", nil)
		server.handleManuscriptRecovery(recorder, request, st)
		if recorder.Code != http.StatusOK {
			t.Fatalf("%s status = %d body=%s", method, recorder.Code, recorder.Body.String())
		}
		body := recorder.Body.String()
		if strings.Contains(body, st.Dir()) || strings.Contains(body, "journal") {
			t.Fatalf("%s leaked recovery internals: %s", method, body)
		}
		var response struct {
			Recovered bool                              `json:"recovered"`
			Recovery  storepkg.ManuscriptRecoveryStatus `json:"recovery"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		if !response.Recovered || response.Recovery.Required {
			t.Fatalf("%s response = %+v", method, response)
		}
	}
}

func TestManuscriptTreeAndReviewHistoryDoNotTruncateAtOneHundred(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), testTempDir(t))
	defer server.Close()
	manifest, err := server.store.CreateProject("long revision history")
	if err != nil {
		t.Fatal(err)
	}
	st := storepkg.NewStore(manifest.OutputDir)
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	chapterID := domain.LegacyStructureID(st.Dir(), domain.StructureKindChapter, "chapters/0001")
	otherChapterID := domain.LegacyStructureID(st.Dir(), domain.StructureKindChapter, "chapters/0002")
	volumeID := domain.LegacyStructureID(st.Dir(), domain.StructureKindVolume, "volume/1")
	arcID := domain.LegacyStructureID(st.Dir(), domain.StructureKindArc, "volume/1/arc/1")
	if err := st.Outline.SaveLayeredOutline([]domain.VolumeOutline{{ID: volumeID, Index: 1, Title: "Volume", Arcs: []domain.ArcOutline{{ID: arcID, Index: 1, Title: "Arc", Chapters: []domain.OutlineEntry{{ID: chapterID, Chapter: 1, Title: "Old history"}, {ID: otherChapterID, Chapter: 2, Title: "New history"}}}}}}); err != nil {
		t.Fatal(err)
	}
	if err := st.Progress.Init("Book", 2); err != nil {
		t.Fatal(err)
	}
	if err := st.Progress.UpdatePhase(domain.PhaseWriting); err != nil {
		t.Fatal(err)
	}

	revisions := make(map[string]domain.ManuscriptRevisionRuntime, 127)
	oldID := "rev_00000000000000000000000000000001"
	revisions[oldID] = domain.ManuscriptRevisionRuntime{RevisionID: oldID, Revision: 1, Stage: "completed", UpdatedAt: "2025-01-01T00:00:00Z", Baseline: domain.ManuscriptBaseline{ChapterID: otherChapterID}}
	report, err := st.ManuscriptRevisions.Content().PutMarkdown("101st page audit report")
	if err != nil {
		t.Fatal(err)
	}
	findings, err := st.ManuscriptRevisions.Content().PutJSON([]map[string]string{{"severity": "minor", "summary": "reachable"}})
	if err != nil {
		t.Fatal(err)
	}
	emptyJSON, err := st.ManuscriptRevisions.Content().PutJSON(map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	audit := domain.NewManuscriptAuditArtifact(strings.Repeat("a", 64), emptyJSON, report, findings, emptyJSON, []string{strings.Repeat("b", 64)})
	for index := 2; index <= 127; index++ {
		id := fmt.Sprintf("rev_%032x", index)
		runtime := domain.ManuscriptRevisionRuntime{RevisionID: id, Revision: 1, Stage: "completed", UpdatedAt: time.Date(2026, 1, 1, 0, index, 0, 0, time.UTC).Format(time.RFC3339), Baseline: domain.ManuscriptBaseline{ChapterID: chapterID}}
		if index == 20 {
			runtime.Candidates = []domain.ManuscriptCandidate{{ChapterID: chapterID, DisplayChapter: 1, Prose: domain.ManuscriptContentRef{SHA256: strings.Repeat("a", 64), MediaType: "text/markdown", Size: 1}, AuditArtifact: &audit}}
		}
		revisions[id] = runtime
	}
	payload, err := json.Marshal(map[string]any{"revisions": revisions})
	if err != nil {
		t.Fatal(err)
	}
	indexPath := filepath.Join(manifest.OutputDir, "meta", "revisions", "manuscript", "index.json")
	if err := os.MkdirAll(filepath.Dir(indexPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(indexPath, payload, 0o644); err != nil {
		t.Fatal(err)
	}

	tree := requestWorkspace(t, server, http.MethodGet, "/api/projects/"+manifest.ID+"/manuscript/workspace/tree", nil, "")
	if tree.Code != http.StatusOK || !bytes.Contains(tree.Body.Bytes(), []byte(`"stable_id":"`+chapterID+`"`)) || !bytes.Contains(tree.Body.Bytes(), []byte(`"stable_id":"`+otherChapterID+`"`)) || bytes.Count(tree.Body.Bytes(), []byte(`"has_history":true`)) < 2 {
		t.Fatalf("tree lost history older than 100 revisions: status=%d body=%s", tree.Code, tree.Body.String())
	}
	first := requestWorkspace(t, server, http.MethodGet, "/api/projects/"+manifest.ID+"/manuscript/workspace/artifacts/review/"+chapterID+"?cursor=0&limit=20", nil, "")
	if first.Code != http.StatusOK || !bytes.Contains(first.Body.Bytes(), []byte(`"has_more":true`)) || !bytes.Contains(first.Body.Bytes(), []byte(`"next_cursor":20`)) || bytes.Contains(first.Body.Bytes(), []byte(`"prose"`)) || bytes.Contains(first.Body.Bytes(), []byte(`"candidates"`)) {
		t.Fatalf("first review page contract: status=%d body=%s", first.Code, first.Body.String())
	}
	deep := requestWorkspace(t, server, http.MethodGet, "/api/projects/"+manifest.ID+"/manuscript/workspace/artifacts/review/"+chapterID+"?cursor=100&limit=20", nil, "")
	deepID := "rev_00000000000000000000000000000014"
	if deep.Code != http.StatusOK || !bytes.Contains(deep.Body.Bytes(), []byte(deepID)) || !bytes.Contains(deep.Body.Bytes(), []byte(`"next_cursor":120`)) || !bytes.Contains(deep.Body.Bytes(), []byte(`"has_more":true`)) || bytes.Contains(deep.Body.Bytes(), []byte(`"prose"`)) || bytes.Contains(deep.Body.Bytes(), []byte(`"candidates"`)) {
		t.Fatalf("review cursor did not reach 101+ metadata: status=%d body=%s", deep.Code, deep.Body.String())
	}
	detail := requestWorkspace(t, server, http.MethodGet, "/api/projects/"+manifest.ID+"/manuscript/workspace/artifacts/review/"+chapterID+"/"+deepID, nil, "")
	if detail.Code != http.StatusOK || !bytes.Contains(detail.Body.Bytes(), []byte(`"summary":"reachable"`)) {
		t.Fatalf("deep review detail unavailable: status=%d body=%s", detail.Code, detail.Body.String())
	}
	tombstoned := requestWorkspace(t, server, http.MethodGet, "/api/projects/"+manifest.ID+"/manuscript/workspace/versions/"+deepID+"?chapter_id="+chapterID, nil, "")
	if tombstoned.Code != http.StatusGone || !bytes.Contains(tombstoned.Body.Bytes(), []byte(`"code":"version_gone"`)) {
		t.Fatalf("tombstoned history content status=%d body=%s", tombstoned.Code, tombstoned.Body.String())
	}
}

func requestWorkspace(t *testing.T, server *Server, method, path string, payload []byte, etag string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewReader(payload))
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	return rec
}
