package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/voocel/ainovel-cli/assets"
	"github.com/voocel/ainovel-cli/internal/bootstrap"
)

func TestSetupStatusDoesNotExposeCredentials(t *testing.T) {
	cfg := bootstrap.Config{
		Provider: "openai",
		Providers: map[string]bootstrap.ProviderConfig{
			"openai": {APIKey: "secret-value"},
		},
	}
	server := NewServer(cfg, assets.Bundle{}, t.TempDir())
	defer server.Close()
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/setup", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload["setup_required"] != true {
		t.Fatalf("setup_required = %#v", payload["setup_required"])
	}
	if body := recorder.Body.String(); body == "" || containsSecret(body, "secret-value") {
		t.Fatalf("setup response leaked credentials: %s", body)
	}
}

func containsSecret(body, secret string) bool {
	return strings.Contains(body, secret)
}
