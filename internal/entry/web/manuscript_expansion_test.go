package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/voocel/ainovel-cli/assets"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/expansionauditorclient"
	"github.com/voocel/ainovel-cli/internal/host"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
)

func TestExpansionAuditorRuntimeFailuresUseServiceUnavailableEnvelope(t *testing.T) {
	for _, classified := range []error{expansionauditorclient.ErrUnavailable, expansionauditorclient.ErrProcess, expansionauditorclient.ErrDecode} {
		recorder := httptest.NewRecorder()
		writeExpansionError(recorder, errors.Join(classified, errors.New("non-sensitive diagnostic")))
		if recorder.Code != http.StatusServiceUnavailable || !strings.Contains(recorder.Body.String(), `"code":"expansion_auditor_unavailable"`) {
			t.Fatalf("classified=%v status=%d body=%s", classified, recorder.Code, recorder.Body.String())
		}
		if strings.Contains(recorder.Body.String(), "non-sensitive diagnostic") {
			t.Fatalf("classified error leaked its internal diagnostic: %s", recorder.Body.String())
		}
	}
}

func TestUnifiedManuscriptEnvelopeDoesNotExposeUntypedInternals(t *testing.T) {
	recorder := httptest.NewRecorder()
	writeManuscriptOperationError(recorder, errors.New(`open C:\private\novel\chapter.md: secret prose`))
	if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), `"code":"invalid_request"`) {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	for _, forbidden := range []string{"C:\\private", "chapter.md", "secret prose"} {
		if strings.Contains(recorder.Body.String(), forbidden) {
			t.Fatalf("untyped error leaked %q in %s", forbidden, recorder.Body.String())
		}
	}
}

func TestExpansionErrorsUseUnifiedHumanAndBatchEnvelopes(t *testing.T) {
	for _, test := range []struct {
		err    error
		status int
		code   string
	}{
		{&domain.ManuscriptRevisionError{Class: "human_confirmation_required", Err: errors.New("approval is pending")}, http.StatusConflict, "human_confirmation_required"},
		{&domain.ManuscriptRevisionError{Class: "provider_error", Err: errors.New("provider failed")}, http.StatusUnprocessableEntity, "batch_failed"},
	} {
		recorder := httptest.NewRecorder()
		writeExpansionError(recorder, test.err)
		if recorder.Code != test.status || !strings.Contains(recorder.Body.String(), `"code":"`+test.code+`"`) {
			t.Fatalf("err=%v status=%d body=%s", test.err, recorder.Code, recorder.Body.String())
		}
	}
}

type webExpansionRecommender struct {
	recommendation domain.ExpansionRecommendation
}

func (planner webExpansionRecommender) RecommendExpansion(context.Context, host.ExpansionContext, domain.ExpansionRequest) (domain.ExpansionRecommendation, error) {
	return planner.recommendation, nil
}

func TestManuscriptExpansionAPIPlansSanitizedPreviewAndConfirmsOneRevision(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), testTempDir(t))
	defer server.Close()
	manifest, err := server.store.CreateProject("expansion api")
	if err != nil {
		t.Fatal(err)
	}
	st := storepkg.NewStore(manifest.OutputDir)
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	current := webExpansionStructure()
	if err := st.Outline.SaveLayeredOutline(current); err != nil {
		t.Fatal(err)
	}
	if err := st.Progress.Save(&domain.Progress{Phase: domain.PhaseWriting, Layered: true, TotalChapters: 1, CurrentChapter: 1}); err != nil {
		t.Fatal(err)
	}
	installFakeSession(t, server, manifest)
	candidate := domain.CloneStructureSnapshot(current)
	facts := domain.ExpansionDramaticFactSet{SchemaVersion: domain.ExpansionDramaticFactsSchemaV1, GoalState: "pursued", ConflictState: "active", ChoiceState: "committed", CostState: "paid", ResultState: "achieved", CharacterBefore: "passive", CharacterAfter: "active", ClimaxState: "occurred", ExitState: "irreversible", ImpactState: "required"}
	candidate[0].Arcs[0].Chapters = append(candidate[0].Arcs[0].Chapters, domain.OutlineEntry{ID: "ch_22222222222222222222222222222222", Title: "代价", CoreEvent: "公开站队", Hook: "失去盟友", Scenes: []string{"证据公开"}, DramaticFacts: &facts})
	proposalBudget, _ := domain.NewDynamicSoftBudget(2, 2200, 3600)
	deltaBudget, _ := domain.NewDynamicSoftBudget(1, 2200, 3600)
	proposal := domain.StructureRevisionProposal{Assessment: domain.ContentAdditionAssessment{NeedsAdditionalChapters: true, Reason: "需要独立选择与代价"}, Candidate: candidate, SoftBudget: proposalBudget}
	recommendation := domain.ExpansionRecommendation{Form: domain.ExpansionFormInsertOne, Reason: "专业戏剧判断", Location: domain.ExpansionAfter, ChapterCount: 1, ChapterMinWords: 2200, ChapterMaxWords: 3600, TotalMinWords: 2200, TotalMaxWords: 3600, OldSummary: "隐瞒", NewSummary: "公开", Assessment: domain.ExpansionDramaticAssessment{Goal: "取证", Conflict: "信任破裂", Choice: "公开", Cost: "失去盟友", Result: "新联盟", CharacterStageChange: "主动承担", CharacterBeforeStage: "被动", CharacterAfterStage: "主动", IndependentClimax: "公开证据", IrreversibleExit: "旧盟友离开", CurrentFit: "独立单元", VolumePacingEffect: "中点转折", TypedClaims: &facts}, AuditChain: []string{"结构", "提纲"}, ModeConstraints: []string{"normal source firewall"}, OrderedOperations: []domain.ExpansionOperation{{Operation: domain.StructureRevisionAppendChapter, Intent: "追加代价章", Proposal: proposal}}, SoftBudgetDelta: deltaBudget}
	auditorPath := filepath.Join(t.TempDir(), "expansion-auditor.exe")
	build := exec.Command(filepath.Join(runtime.GOROOT(), "bin", "go.exe"), "build", "-o", auditorPath, "./cmd/expansion-auditor")
	build.Dir = filepath.Clean(filepath.Join("..", "..", ".."))
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build independent auditor: %v\n%s", err, output)
	}
	t.Setenv("AINOVEL_EXPANSION_AUDITOR", auditorPath)
	auditor, err := expansionauditorclient.New()
	if err != nil {
		t.Fatal(err)
	}
	if err := auditor.Init(context.Background(), manifest.OutputDir); err != nil {
		t.Fatal(err)
	}
	server.sessions.Project(manifest.ID).expansionPlanner = host.NewExpansionPlanner(st, webExpansionRecommender{recommendation})

	structureRevision := domain.StructureRevision(current)
	planBody := map[string]any{"location": "after", "reference_ids": []string{current[0].Arcs[0].Chapters[0].ID}, "sentence": "让证据迫使主角公开站队", "adjustment": "default", "expected_structure_revision": structureRevision, "expected_structure_signature": domain.StructureSignature(current), "idempotency_key": "plan-1"}
	recorder := requestExpansion(t, server, http.MethodPost, "/api/projects/"+manifest.ID+"/manuscript/expansion/plan", planBody)
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("plan status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if body := recorder.Body.String(); strings.Contains(body, "candidate_signature") || strings.Contains(body, "base_structure_signature") || strings.Contains(body, "target_id") || strings.Contains(body, "ch_2222") {
		t.Fatalf("public preview leaked internal identity or signature: %s", body)
	}
	var response struct {
		Preview struct {
			ID   string `json:"preview_id"`
			Form string `json:"form"`
		} `json:"preview"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Preview.ID == "" || response.Preview.Form != "insert_one" {
		t.Fatalf("preview=%+v", response.Preview)
	}

	confirm := requestExpansion(t, server, http.MethodPost, "/api/projects/"+manifest.ID+"/manuscript/expansion/confirm", map[string]any{"preview_id": response.Preview.ID, "expected_revision": structureRevision, "idempotency_key": "confirm-1"})
	if confirm.Code != http.StatusOK {
		t.Fatalf("confirm status=%d body=%s", confirm.Code, confirm.Body.String())
	}
	replay := requestExpansion(t, server, http.MethodPost, "/api/projects/"+manifest.ID+"/manuscript/expansion/confirm", map[string]any{"preview_id": response.Preview.ID, "expected_revision": structureRevision, "idempotency_key": "confirm-2"})
	if replay.Code != http.StatusOK || !strings.Contains(replay.Body.String(), `"replay":true`) {
		t.Fatalf("two-tab replay status=%d body=%s", replay.Code, replay.Body.String())
	}
	active, err := st.Revisions.Active()
	if err != nil || active == nil || active.Mode != domain.RevisionModeNormal {
		t.Fatalf("active revision=%+v err=%v", active, err)
	}
	queued := requestExpansion(t, server, http.MethodPost, "/api/projects/"+manifest.ID+"/manuscript/expansion/revision/command", map[string]any{"action": "request_audit", "expected_revision": active.Revision, "idempotency_key": "queue-before-auditor-stop"})
	if queued.Code != http.StatusOK {
		t.Fatalf("queue audit status=%d body=%s", queued.Code, queued.Body.String())
	}
	t.Setenv("AINOVEL_EXPANSION_AUDITOR", filepath.Join(t.TempDir(), "stopped-expansion-auditor.exe"))
	processed := requestExpansion(t, server, http.MethodPost, "/api/projects/"+manifest.ID+"/manuscript/expansion/revision/auditor/process", map[string]any{})
	if processed.Code != http.StatusServiceUnavailable || !strings.Contains(processed.Body.String(), `"code":"expansion_auditor_unavailable"`) {
		t.Fatalf("stopped auditor status=%d body=%s", processed.Code, processed.Body.String())
	}
	pending, err := st.Revisions.Active()
	if err != nil || pending == nil || pending.Stage != domain.RevisionStageCandidateAudit || pending.Revision != active.Revision {
		t.Fatalf("stopped auditor did not preserve pending revision: %+v err=%v", pending, err)
	}
}

func TestManuscriptExpansionPublicAuditRejectsSignedTypedSemanticMismatch(t *testing.T) {
	auditorPath := filepath.Join(t.TempDir(), "expansion-auditor.exe")
	build := exec.Command(filepath.Join(runtime.GOROOT(), "bin", "go.exe"), "build", "-o", auditorPath, "./cmd/expansion-auditor")
	build.Dir = filepath.Clean(filepath.Join("..", "..", ".."))
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build independent auditor: %v\n%s", err, output)
	}
	t.Setenv("AINOVEL_EXPANSION_AUDITOR", auditorPath)
	tests := []struct {
		name   string
		mutate func(*domain.ExpansionDramaticFactSet)
	}{
		{"causality", func(value *domain.ExpansionDramaticFactSet) { value.ResultState = "failed" }},
		{"character", func(value *domain.ExpansionDramaticFactSet) { value.CharacterAfter = "independent" }},
		{"climax", func(value *domain.ExpansionDramaticFactSet) { value.ClimaxState = "absent" }},
		{"exit", func(value *domain.ExpansionDramaticFactSet) { value.ExitState = "reversible" }},
		{"impact", func(value *domain.ExpansionDramaticFactSet) { value.ImpactState = "recommended" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := NewServer(testWebConfig(t), assets.Load("default"), testTempDir(t))
			defer server.Close()
			manifest, err := server.store.CreateProject("typed semantic " + test.name)
			if err != nil {
				t.Fatal(err)
			}
			st := storepkg.NewStore(manifest.OutputDir)
			if err := st.Init(); err != nil {
				t.Fatal(err)
			}
			current := webExpansionStructure()
			if err := st.Outline.SaveLayeredOutline(current); err != nil {
				t.Fatal(err)
			}
			if err := st.Progress.Save(&domain.Progress{Phase: domain.PhaseWriting, Layered: true, TotalChapters: 1, CurrentChapter: 1}); err != nil {
				t.Fatal(err)
			}
			installFakeSession(t, server, manifest)
			auditor, err := expansionauditorclient.New()
			if err != nil {
				t.Fatal(err)
			}
			if err := auditor.Init(context.Background(), manifest.OutputDir); err != nil {
				t.Fatal(err)
			}
			facts := domain.ExpansionDramaticFactSet{SchemaVersion: domain.ExpansionDramaticFactsSchemaV1, GoalState: "pursued", ConflictState: "active", ChoiceState: "committed", CostState: "paid", ResultState: "achieved", CharacterBefore: "passive", CharacterAfter: "active", ClimaxState: "occurred", ExitState: "irreversible", ImpactState: "required"}
			claims := facts
			test.mutate(&claims)
			candidate := domain.CloneStructureSnapshot(current)
			candidate[0].Arcs[0].Chapters = append(candidate[0].Arcs[0].Chapters, domain.OutlineEntry{ID: "ch_22222222222222222222222222222222", Chapter: 2, Title: "代价", CoreEvent: "公开站队", Hook: "失去盟友", Scenes: []string{"证据公开"}, DramaticFacts: &facts})
			proposalBudget, _ := domain.NewDynamicSoftBudget(2, 2200, 3600)
			deltaBudget, _ := domain.NewDynamicSoftBudget(1, 2200, 3600)
			recommendation := domain.ExpansionRecommendation{Form: domain.ExpansionFormInsertOne, Reason: "typed semantic boundary", Location: domain.ExpansionAfter, ChapterCount: 1, ChapterMinWords: 2200, ChapterMaxWords: 3600, TotalMinWords: 2200, TotalMaxWords: 3600, OldSummary: "隐瞒", NewSummary: "公开", Assessment: domain.ExpansionDramaticAssessment{Goal: "取证", Conflict: "信任破裂", Choice: "公开", Cost: "失去盟友", Result: "新联盟", CharacterStageChange: "被动到主动", CharacterBeforeStage: "被动", CharacterAfterStage: "主动", IndependentClimax: "公开证据", IrreversibleExit: "旧盟友离开", CurrentFit: "独立单元", VolumePacingEffect: "中点转折", TypedClaims: &claims}, AuditChain: []string{"结构", "提纲"}, ModeConstraints: []string{"normal source firewall"}, OrderedOperations: []domain.ExpansionOperation{{Operation: domain.StructureRevisionAppendChapter, Intent: "追加代价章", Proposal: domain.StructureRevisionProposal{Assessment: domain.ContentAdditionAssessment{NeedsAdditionalChapters: true, Reason: "需要独立选择与代价"}, Candidate: candidate, SoftBudget: proposalBudget}}}, SoftBudgetDelta: deltaBudget}
			server.sessions.Project(manifest.ID).expansionPlanner = host.NewExpansionPlanner(st, webExpansionRecommender{recommendation})
			structureRevision := domain.StructureRevision(current)
			planned := requestExpansion(t, server, http.MethodPost, "/api/projects/"+manifest.ID+"/manuscript/expansion/plan", map[string]any{"location": "after", "reference_ids": []string{current[0].Arcs[0].Chapters[0].ID}, "sentence": "增加不可逆选择", "adjustment": "default", "expected_structure_revision": structureRevision, "expected_structure_signature": domain.StructureSignature(current), "idempotency_key": "plan-" + test.name})
			if planned.Code != http.StatusAccepted {
				t.Fatalf("plan status=%d body=%s", planned.Code, planned.Body.String())
			}
			var planResponse struct {
				Preview struct {
					ID string `json:"preview_id"`
				} `json:"preview"`
			}
			if err := json.Unmarshal(planned.Body.Bytes(), &planResponse); err != nil {
				t.Fatal(err)
			}
			confirmed := requestExpansion(t, server, http.MethodPost, "/api/projects/"+manifest.ID+"/manuscript/expansion/confirm", map[string]any{"preview_id": planResponse.Preview.ID, "expected_revision": structureRevision, "idempotency_key": "confirm-" + test.name})
			if confirmed.Code != http.StatusOK {
				t.Fatalf("confirm status=%d body=%s", confirmed.Code, confirmed.Body.String())
			}
			active, err := st.Revisions.Active()
			if err != nil || active == nil {
				t.Fatalf("active=%+v err=%v", active, err)
			}
			queued := requestExpansion(t, server, http.MethodPost, "/api/projects/"+manifest.ID+"/manuscript/expansion/revision/command", map[string]any{"action": "request_audit", "expected_revision": active.Revision, "idempotency_key": "audit-" + test.name})
			if queued.Code != http.StatusOK {
				t.Fatalf("queue status=%d body=%s", queued.Code, queued.Body.String())
			}
			before, err := st.LoadExpansionRuntime()
			if err != nil {
				t.Fatal(err)
			}
			taskID := active.ID + ":" + fmt.Sprint(active.Revision)
			beforeTask := append([]byte(nil), before.PendingAudits[taskID]...)
			artifact, err := auditor.ReviewRevision(context.Background(), manifest.OutputDir, active.ID)
			if err != nil || artifact.Decision != "needs_fix" || !strings.Contains(strings.Join(artifact.Findings, " "), "typed candidate fact") {
				t.Fatalf("signed typed mismatch decision=%s findings=%v err=%v", artifact.Decision, artifact.Findings, err)
			}
			afterBinary, err := st.LoadExpansionRuntime()
			if err != nil || !bytes.Equal(beforeTask, afterBinary.PendingAudits[taskID]) {
				t.Fatalf("independent binary mutated pending revision task err=%v", err)
			}
			processed := requestExpansion(t, server, http.MethodPost, "/api/projects/"+manifest.ID+"/manuscript/expansion/revision/auditor/process", map[string]any{})
			if processed.Code != http.StatusOK || !strings.Contains(processed.Body.String(), `"audit_decision":"needs_fix"`) {
				t.Fatalf("process status=%d body=%s", processed.Code, processed.Body.String())
			}
		})
	}
}

func TestPublicExpansionPreviewNeverSerializesKernelOperationsOrAdaptationCandidate(t *testing.T) {
	preview := &domain.ExpansionPreview{ID: "exp", Mode: domain.RevisionModeAdaptation, Recommendation: domain.ExpansionRecommendation{Form: domain.ExpansionFormNewArc, OrderedOperations: []domain.ExpansionOperation{{Operation: domain.StructureRevisionAppendArc, TargetID: "internal"}}, AdaptationCandidate: &domain.AdaptationPlan{}}}
	payload, err := json.Marshal(publicExpansionPreview(preview))
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"ordered_operations", "adaptation_candidate", "internal", "signature"} {
		if bytes.Contains(payload, []byte(forbidden)) {
			t.Fatalf("public DTO leaked %q: %s", forbidden, payload)
		}
	}
}

func requestExpansion(t *testing.T, server *Server, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	payload, _ := json.Marshal(body)
	request := httptest.NewRequest(method, path, bytes.NewReader(payload))
	request.Header.Set("content-type", "application/json")
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	return recorder
}

func webExpansionStructure() []domain.VolumeOutline {
	return []domain.VolumeOutline{{ID: "vol_11111111111111111111111111111111", Index: 1, Title: "第一卷", Theme: "信任", Arcs: []domain.ArcOutline{{ID: "arc_11111111111111111111111111111111", Index: 1, Title: "第一弧", Goal: "取证", Chapters: []domain.OutlineEntry{{ID: "ch_11111111111111111111111111111111", Chapter: 1, Title: "隐瞒", CoreEvent: "发现证据", Hook: "是否公开", Scenes: []string{"秘密"}}}}}}}
}
