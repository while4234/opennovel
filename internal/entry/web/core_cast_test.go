package web

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/voocel/ainovel-cli/assets"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/entry/startup"
	"github.com/voocel/ainovel-cli/internal/host"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
)

func TestNormalCoCreateRejectsLegacyCoreCastHTTPMutation(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()
	manifest, err := server.store.CreateProject("Core Cast Gate")
	if err != nil {
		t.Fatal(err)
	}
	installFakeSession(t, server, manifest)
	session, _, err := server.sessions.Open(manifest.ID)
	if err != nil {
		t.Fatal(err)
	}
	session.cocreate = readyCoreCastWebSession()
	legacy := completeWebCoreCast()
	session.cocreate.coreCast = &legacy
	if state := session.cocreate.apiState(); state.CoreCast != nil || !state.CanStart {
		t.Fatalf("normal migration exposed stale legacy CoreCast or blocked Character start: %+v", state)
	}

	update := coreCastRequest(t, server, http.MethodPut, manifest.ID, "cocreate/core-cast", map[string]any{
		"expected_revision": 0,
		"core_cast":         completeWebCoreCast(),
	})
	if update.Code == http.StatusOK {
		t.Fatalf("normal co-create accepted legacy CoreCast mutation: %s", update.Body.String())
	}
	stored, err := storepkg.NewStore(manifest.OutputDir).CoreCast.Load()
	if err != nil {
		t.Fatal(err)
	}
	if stored != nil {
		t.Fatalf("rejected normal CoreCast mutation persisted state: %+v", stored)
	}
}

func TestOldNormalCheckpointWithoutCoreCastRestoresForCharacterWorkflow(t *testing.T) {
	checkpoint := webCoCreateCheckpoint{
		Version: webCoCreateCheckpointVersion,
		Kind:    webCoCreateKindNormal,
		Session: startup.CoCreateSnapshot{
			History:     []host.CoCreateMessage{{Role: "user", Content: "idea"}, {Role: "assistant", Content: "<reply>ok</reply><draft>draft</draft><ready>true</ready><suggestions></suggestions>"}},
			DraftPrompt: "draft", DraftHistoryLen: 2, Ready: true,
		},
	}
	state, err := webCoCreateSessionFromCheckpoint(checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	api := state.apiState()
	if !api.CanStart || api.CastConfirmed || !api.CastCompletion.Complete {
		t.Fatalf("normal checkpoint did not migrate to Character-owned cast: %+v", api)
	}
}

func readyCoreCastWebSession() *webCoCreateSession {
	return &webCoCreateSession{
		kind: webCoCreateKindNormal,
		session: startup.NewCoCreateSessionFromSnapshot(startup.CoCreateSnapshot{
			History:     []host.CoCreateMessage{{Role: "user", Content: "idea"}, {Role: "assistant", Content: "<reply>ok</reply><draft>draft</draft><ready>true</ready><suggestions></suggestions>"}},
			DraftPrompt: "draft", DraftHistoryLen: 2, Ready: true,
		}),
	}
}

func completeWebCoreCast() domain.CoreCastContract {
	return domain.CoreCastContract{
		Version: domain.CoreCastContractVersion, Mode: domain.CoreCastModeNormal,
		Members: []domain.CoreCastMember{{
			Character:  domain.Character{ID: "lin", Name: "Lin", Role: "hero", Goal: "save home", Motivation: "duty", Conflict: "fear", Arc: "accept leadership", Traits: []string{"brave"}, Constraints: []string{"will not betray friends"}},
			Importance: domain.CoreCastImportanceProtagonist, Origin: domain.CoreCastOriginOriginal, MainlineFunction: "drives the central conflict", NoCoreRelationships: true,
		}},
	}
}

func coreCastRequest(t *testing.T, server *Server, method, projectID, action string, body any) *httptest.ResponseRecorder {
	t.Helper()
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(method, "/api/projects/"+projectID+"/"+action, bytes.NewReader(payload))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	return rec
}

func decodeCoreCastState(t *testing.T, rec *httptest.ResponseRecorder) webCoCreateState {
	t.Helper()
	var response struct {
		CoCreate webCoCreateState `json:"cocreate"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatalf("decode response: %v body=%s", err, rec.Body.String())
	}
	return response.CoCreate
}
