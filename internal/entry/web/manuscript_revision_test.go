package web

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/voocel/ainovel-cli/assets"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/host"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
)

func TestManuscriptBatchFailureUsesUnifiedEnvelope(t *testing.T) {
	recorder := httptest.NewRecorder()
	writeManuscriptError(recorder, &domain.ManuscriptRevisionError{Class: "truncated_response", Err: errors.New("provider truncated output")})
	if recorder.Code != http.StatusUnprocessableEntity || !strings.Contains(recorder.Body.String(), `"code":"batch_failed"`) || !strings.Contains(recorder.Body.String(), `"error_class":"truncated_response"`) {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestLegacyChapterReviseCreatesSafePreviewWithoutResume(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), testTempDir(t))
	defer server.Close()
	manifest, err := server.store.CreateProject("safe manuscript revision")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	st := storepkg.NewStore(manifest.OutputDir)
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	chapterID := "ch_abcdef0123456789abcdef0123456789"
	if err := st.Outline.SaveOutline([]domain.OutlineEntry{{ID: chapterID, Chapter: 1, Title: "第一章", CoreEvent: "选择", Hook: "代价"}}); err != nil {
		t.Fatalf("SaveOutline: %v", err)
	}
	if err := st.Progress.Init("书", 1); err != nil {
		t.Fatalf("Progress.Init: %v", err)
	}
	if err := st.Progress.UpdatePhase(domain.PhaseWriting); err != nil {
		t.Fatalf("UpdatePhase: %v", err)
	}
	if err := st.Drafts.SaveFinalChapter(1, "正式正文"); err != nil {
		t.Fatalf("SaveFinalChapter: %v", err)
	}
	fake := installFakeSession(t, server, manifest)
	fake.manuscriptService = host.NewManuscriptRevisionServiceWithRuntime(st, noChangeManuscriptWriter{}, nil)

	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/chapters/revise", bytes.NewBufferString(`{"chapter":1,"instruction":"打磨节奏","mode":"polish"}`))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("legacy revise status=%d body=%s", rec.Code, rec.Body.String())
	}
	var response struct {
		AwaitingConfirmation bool `json:"awaiting_confirmation"`
		Running              bool `json:"running"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !response.AwaitingConfirmation || response.Running {
		t.Fatalf("unsafe legacy response: %+v", response)
	}
	if prose, _ := st.Drafts.LoadChapterText(1); prose != "正式正文" {
		t.Fatalf("legacy preview changed current prose: %q", prose)
	}
	if active, err := st.ManuscriptRevisions.Active(); err != nil || active == nil || active.Baseline.ChapterID != chapterID {
		t.Fatalf("active preview = %+v err=%v", active, err)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/projects/"+manifest.ID+"/manuscript/chapters/"+chapterID, nil)
	rec = httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !bytes.Contains(rec.Body.Bytes(), []byte(`"content":"正式正文"`)) {
		t.Fatalf("stable chapter read status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestProductionManuscriptHandlersEmitMutationOnlyAfterSuccessfulCommands(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), testTempDir(t))
	defer server.Close()
	manifest, err := server.store.CreateProject("handler mutation matrix")
	if err != nil {
		t.Fatal(err)
	}
	st := storepkg.NewStore(manifest.OutputDir)
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	chapterID := "ch_abcdef0123456789abcdef0123456789"
	if err := st.Outline.SaveOutline([]domain.OutlineEntry{{ID: chapterID, Chapter: 1, Title: "Chapter", CoreEvent: "choice", Hook: "cost", Scenes: []string{"scene one", "scene two"}}}); err != nil {
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
	fake := installFakeSession(t, server, manifest)
	fake.manuscriptService = host.NewManuscriptRevisionServiceWithRuntime(st, handlerMatrixManuscriptWorker{}, handlerMatrixManuscriptWorker{})
	projectSession, _, err := server.sessions.Open(manifest.ID)
	if err != nil {
		t.Fatal(err)
	}

	mutationCount := func() int {
		count := 0
		for _, event := range projectSession.EventHistory(0).Events {
			if event.ManuscriptMutation != nil {
				count++
			}
		}
		return count
	}
	preview := requestWorkspace(t, server, http.MethodPost, "/api/projects/"+manifest.ID+"/manuscript/revision/preview", []byte(`{"chapter_id":"`+chapterID+`","instruction":"change story","kind":"rewrite","idempotency_key":"matrix-preview"}`), "")
	if preview.Code != http.StatusAccepted {
		t.Fatalf("preview status=%d body=%s", preview.Code, preview.Body.String())
	}
	var previewBody struct {
		Preview struct {
			Runtime domain.ManuscriptRevisionRuntime `json:"runtime"`
		} `json:"preview"`
	}
	if err := json.Unmarshal(preview.Body.Bytes(), &previewBody); err != nil {
		t.Fatal(err)
	}
	if mutationCount() != 0 {
		t.Fatal("preview emitted a manuscript mutation")
	}

	command := func(action string, runtime domain.ManuscriptRevisionRuntime, key string, expectedStatus int) domain.ManuscriptRevisionRuntime {
		expectedAttempt := 0
		if action == "generate" {
			expectedAttempt = 1
		}
		body, _ := json.Marshal(manuscriptCommandRequest{Action: action, RevisionID: runtime.RevisionID, ExpectedRevision: runtime.Revision, ExpectedAttempt: expectedAttempt, IdempotencyKey: key})
		response := requestWorkspace(t, server, http.MethodPost, "/api/projects/"+manifest.ID+"/manuscript/revision/command", body, "")
		if response.Code != expectedStatus {
			t.Fatalf("%s status=%d body=%s", action, response.Code, response.Body.String())
		}
		if expectedStatus != http.StatusOK {
			return runtime
		}
		var decoded struct {
			Revision domain.ManuscriptRevisionRuntime `json:"revision"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &decoded); err != nil {
			t.Fatal(err)
		}
		return decoded.Revision
	}
	expectMutation := func(action, scope string, runtime domain.ManuscriptRevisionRuntime) domain.ManuscriptRevisionRuntime {
		before := mutationCount()
		next := command(action, runtime, "matrix-"+action, http.StatusOK)
		history := projectSession.EventHistory(0).Events
		if mutationCount() != before+1 {
			t.Fatalf("%s mutation count did not advance", action)
		}
		last := history[len(history)-1].ManuscriptMutation
		if last == nil || last.Scope != scope || last.StableID != chapterID {
			t.Fatalf("%s mutation=%+v", action, last)
		}
		httpHistory := requestWorkspace(t, server, http.MethodGet, "/api/projects/"+manifest.ID+"/events/history?after=0", nil, "")
		if httpHistory.Code != http.StatusOK {
			t.Fatalf("%s HTTP history status=%d body=%s", action, httpHistory.Code, httpHistory.Body.String())
		}
		var replay WebEventHistory
		if err := json.Unmarshal(httpHistory.Body.Bytes(), &replay); err != nil {
			t.Fatalf("%s decode HTTP history: %v", action, err)
		}
		var replayMutation *ManuscriptMutationEvent
		for index := len(replay.Events) - 1; index >= 0; index-- {
			if replay.Events[index].ManuscriptMutation != nil {
				replayMutation = replay.Events[index].ManuscriptMutation
				break
			}
		}
		if replayMutation == nil || replayMutation.Scope != scope || replayMutation.StableID != chapterID {
			t.Fatalf("%s HTTP history mutation=%+v", action, replayMutation)
		}
		return next
	}

	runtime := previewBody.Preview.Runtime
	runtime = expectMutation("confirm_impacts", "structure_publish", runtime)
	runtime = expectMutation("generate", "generation", runtime)
	historicalRevisionID, historicalSignature := runtime.RevisionID, runtime.Candidates[0].Prose.SHA256
	runtime = expectMutation("audit", "audit", runtime)
	runtime = expectMutation("approve", "prose_publish", runtime)
	_ = expectMutation("publish", "prose_publish", runtime)

	secondPreview := requestWorkspace(t, server, http.MethodPost, "/api/projects/"+manifest.ID+"/manuscript/revision/preview", []byte(`{"chapter_id":"`+chapterID+`","instruction":"cancel story","kind":"rewrite","idempotency_key":"matrix-cancel-preview"}`), "")
	var secondBody struct {
		Preview struct {
			Runtime domain.ManuscriptRevisionRuntime `json:"runtime"`
		} `json:"preview"`
	}
	if secondPreview.Code != http.StatusAccepted || json.Unmarshal(secondPreview.Body.Bytes(), &secondBody) != nil {
		t.Fatalf("second preview status=%d body=%s", secondPreview.Code, secondPreview.Body.String())
	}
	_ = expectMutation("cancel", "cancel", secondBody.Preview.Runtime)

	beforeFailure := mutationCount()
	_ = command("generate", secondBody.Preview.Runtime, "matrix-stale-failure", http.StatusConflict)
	unknown := requestWorkspace(t, server, http.MethodPost, "/api/projects/"+manifest.ID+"/manuscript/revision/command", []byte(`{"action":"unknown","revision_id":"`+secondBody.Preview.Runtime.RevisionID+`","expected_revision":1,"idempotency_key":"matrix-unknown"}`), "")
	if unknown.Code != http.StatusBadRequest || mutationCount() != beforeFailure {
		t.Fatalf("failed/unknown commands emitted mutation: unknown=%d count=%d", unknown.Code, mutationCount())
	}

	restorePreview := requestWorkspace(t, server, http.MethodPost, "/api/projects/"+manifest.ID+"/manuscript/workspace/restore/preview", []byte(`{"revision_id":"`+historicalRevisionID+`","chapter_id":"`+chapterID+`","expected_content_signature":"`+historicalSignature+`"}`), "")
	if restorePreview.Code != http.StatusOK {
		t.Fatalf("restore preview status=%d body=%s", restorePreview.Code, restorePreview.Body.String())
	}
	var restoreBody struct {
		Preview struct {
			Signature string `json:"preview_signature"`
		} `json:"preview"`
	}
	if err := json.Unmarshal(restorePreview.Body.Bytes(), &restoreBody); err != nil {
		t.Fatal(err)
	}
	beforeRestore := mutationCount()
	restore := requestWorkspace(t, server, http.MethodPost, "/api/projects/"+manifest.ID+"/manuscript/workspace/restore", []byte(`{"revision_id":"`+historicalRevisionID+`","chapter_id":"`+chapterID+`","expected_content_signature":"`+historicalSignature+`","idempotency_key":"matrix-restore","preview_signature":"`+restoreBody.Preview.Signature+`"}`), "")
	if restore.Code != http.StatusAccepted {
		t.Fatalf("restore status=%d body=%s", restore.Code, restore.Body.String())
	}
	history := projectSession.EventHistory(0).Events
	last := history[len(history)-1].ManuscriptMutation
	if mutationCount() != beforeRestore+1 || last == nil || last.Scope != "generation" || last.StableID != chapterID {
		t.Fatalf("restore mutation=%+v count=%d", last, mutationCount())
	}

	// Replay a real command-produced event through the production SSE handler.
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()
	response, err := http.Get(httpServer.URL + "/api/projects/" + manifest.ID + "/events?after=0")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	reader := bufio.NewReader(response.Body)
	sawRestoreMutation := false
	for lineCount := 0; lineCount < 200 && !sawRestoreMutation; lineCount++ {
		line, readErr := reader.ReadString('\n')
		if readErr != nil {
			t.Fatal(readErr)
		}
		sawRestoreMutation = strings.Contains(line, `"manuscript_mutation":{"scope":"generation","stable_id":"`+chapterID+`"}`)
	}
	if !sawRestoreMutation {
		t.Fatal("production SSE replay did not contain the real restore command mutation")
	}
}

type noChangeManuscriptWriter struct{}

func (noChangeManuscriptWriter) PlanManuscriptRevision(context.Context, domain.ManuscriptBaseline, string, domain.ManuscriptInstructionKind) (host.ManuscriptPlan, error) {
	return host.ManuscriptPlan{}, nil
}
func (noChangeManuscriptWriter) GenerateManuscriptSegment(context.Context, domain.ManuscriptRevisionRuntime, domain.ManuscriptReworkItem, host.ManuscriptGenerationContext, int, int, string) (host.ManuscriptGeneratedSegment, error) {
	return host.ManuscriptGeneratedSegment{}, nil
}

type handlerMatrixManuscriptWorker struct{}

func (handlerMatrixManuscriptWorker) PlanManuscriptRevision(_ context.Context, baseline domain.ManuscriptBaseline, _ string, _ domain.ManuscriptInstructionKind) (host.ManuscriptPlan, error) {
	contract := baseline.NarrativeContract
	contract.Result = "handler matrix changed result"
	return host.ManuscriptPlan{
		StoryChanged: true,
		Outline:      domain.OutlineEntry{ID: baseline.ChapterID, Chapter: baseline.DisplayChapter, Title: contract.Desire, CoreEvent: contract.Obstacle, Hook: contract.ExitState, Scenes: append([]string(nil), contract.FutureCommitments...)},
		Contract:     contract,
	}, nil
}

func (handlerMatrixManuscriptWorker) GenerateManuscriptSegment(_ context.Context, runtime domain.ManuscriptRevisionRuntime, item domain.ManuscriptReworkItem, _ host.ManuscriptGenerationContext, attempt, segment int, dependency string) (host.ManuscriptGeneratedSegment, error) {
	contract := runtime.Baseline.NarrativeContract
	if runtime.OutlinePreview != nil {
		contract = runtime.OutlinePreview.Contract
	}
	return host.ManuscriptGeneratedSegment{
		ChapterID: item.ChapterID, Attempt: attempt, Segment: segment,
		Prose: handlerMatrixContractProse(contract), Sidecars: handlerMatrixSidecars(),
		Complete: true, DependencyEvidence: dependency,
	}, nil
}

func (handlerMatrixManuscriptWorker) AuditManuscriptCandidate(_ context.Context, _ domain.ManuscriptRevisionRuntime, _ domain.ManuscriptCandidate) (bool, string, error) {
	return true, "independent audit passed", nil
}

func (handlerMatrixManuscriptWorker) AuditCandidateContract(_ context.Context, task host.ManuscriptContractAuditTask, prose string) (host.ManuscriptContractAuditDecision, error) {
	fields := []struct{ name, prefix string }{
		{"desire", "Desire is "}, {"obstacle", "Obstacle is "}, {"choice", "The choice is "},
		{"cost", "The cost is "}, {"result", "The result is "}, {"exit_state", "The exit state is "},
		{"future_commitments", "Future commitments are "},
	}
	contract := domain.NarrativeContract{ChapterID: task.ChapterID, OutlineSHA256: task.OutlineSHA256, StateSHA256: task.ProtectedStateSHA256}
	evidence := make([]host.ManuscriptEvidenceLocation, 0, len(fields))
	for _, field := range fields {
		start := strings.Index(prose, field.prefix)
		if start < 0 {
			continue
		}
		valueStart := start + len(field.prefix)
		end := strings.Index(prose[valueStart:], ".")
		if end < 0 {
			continue
		}
		end += valueStart
		value := prose[valueStart:end]
		switch field.name {
		case "desire":
			contract.Desire = value
		case "obstacle":
			contract.Obstacle = value
		case "choice":
			contract.Choice = value
		case "cost":
			contract.Cost = value
		case "result":
			contract.Result = value
		case "exit_state":
			contract.ExitState = value
		case "future_commitments":
			if value != "" {
				contract.FutureCommitments = strings.Split(value, "\n")
			}
		}
		evidence = append(evidence, host.ManuscriptEvidenceLocation{Field: field.name, StartRune: len([]rune(prose[:valueStart])), EndRune: len([]rune(prose[:end])), Quote: value})
	}
	return host.ManuscriptContractAuditDecision{Role: "contract_locator", TaskSignature: task.Signature, CandidateSHA256: task.CandidateSHA256, Contract: contract, Evidence: evidence}, nil
}

func (handlerMatrixManuscriptWorker) VerifyCandidateContract(_ context.Context, task host.ManuscriptContractVerificationTask, locator host.ManuscriptContractAuditDecision, approved domain.NarrativeContract, _ string) (host.ManuscriptContractVerificationDecision, error) {
	receipts := make([]host.ManuscriptContractVerificationReceipt, 0, len(locator.Evidence))
	for _, location := range locator.Evidence {
		value := handlerMatrixContractValue(locator.Contract, location.Field)
		receipts = append(receipts, host.ManuscriptContractVerificationReceipt{Field: location.Field, Value: value, ApprovedValue: handlerMatrixContractValue(approved, location.Field), StartRune: location.StartRune, EndRune: location.EndRune, Quote: location.Quote, Verdict: "entailed", TaskSignature: task.Signature, CandidateSHA256: task.CandidateSHA256})
	}
	return host.ManuscriptContractVerificationDecision{Role: "contract_verifier", TaskSignature: task.Signature, CandidateSHA256: task.CandidateSHA256, Receipts: receipts}, nil
}

func handlerMatrixContractValue(contract domain.NarrativeContract, field string) string {
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

func handlerMatrixContractProse(contract domain.NarrativeContract) string {
	return strings.Join([]string{
		"Desire is " + contract.Desire + ".", "Obstacle is " + contract.Obstacle + ".",
		"The choice is " + contract.Choice + ".", "The cost is " + contract.Cost + ".",
		"The result is " + contract.Result + ".", "The exit state is " + contract.ExitState + ".",
		"Future commitments are " + strings.Join(contract.FutureCommitments, "\n") + ".",
	}, "\n")
}

func handlerMatrixSidecars() map[string]json.RawMessage {
	return map[string]json.RawMessage{
		"summary": json.RawMessage(`{"chapter":1,"summary":"new summary","characters":[],"key_events":[]}`),
		"events":  json.RawMessage(`["event"]`), "timeline": json.RawMessage(`[{"event":"event"}]`),
		"cast_state":    json.RawMessage(`[{"entity":"hero","field":"status","new_value":"changed"}]`),
		"relationships": json.RawMessage(`[{"character_a":"hero","character_b":"ally","relation":"trusted"}]`),
		"foreshadow":    json.RawMessage(`[{"id":"seed","description":"seed","status":"planted"}]`),
		"world_facts":   json.RawMessage(`[{"category":"other","rule":"rule","boundary":"boundary"}]`),
		"carry_forward": json.RawMessage(`{"character_snapshots":[{"name":"hero","status":"ready","motivation":"act"}]}`),
	}
}
