package codexauth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestResolveRuntimeCredentialsUsesCodexAuthFile(t *testing.T) {
	authPath := filepath.Join(t.TempDir(), "auth.json")
	writeTestAuthFile(t, authPath, map[string]any{
		"access_token": testJWT(t, map[string]any{
			"exp":        time.Now().Add(time.Hour).Unix(),
			"account_id": "acct-test",
		}),
	})
	t.Setenv("CODEX_AUTH_FILE", authPath)

	credentials, err := ResolveRuntimeCredentials(context.Background(), "")
	if err != nil {
		t.Fatalf("ResolveRuntimeCredentials: %v", err)
	}
	if credentials.APIKey == "" || credentials.AccountID != "acct-test" || credentials.BaseURL != DefaultBaseURL {
		t.Fatalf("credentials = %+v", credentials)
	}

	status := GetStatus("")
	if !status.LoggedIn || status.AccountID != "acct-test" || status.AuthFileName != "auth.json" {
		t.Fatalf("status = %+v", status)
	}
	if strings.Contains(status.Message, authPath) {
		t.Fatalf("status message leaked auth path: %q", status.Message)
	}
}

func TestResolveAuthPathPrefersExplicitFileOverEnvironment(t *testing.T) {
	envPath := filepath.Join(t.TempDir(), "environment-auth.json")
	explicitPath := filepath.Join(t.TempDir(), "uploaded-auth.json")
	t.Setenv("CODEX_AUTH_FILE", envPath)

	if got := ResolveAuthPath(explicitPath); got != explicitPath {
		t.Fatalf("ResolveAuthPath() = %q, want explicit path %q", got, explicitPath)
	}
}

func TestInstallAuthFileValidatesAndStoresCredential(t *testing.T) {
	target := filepath.Join(t.TempDir(), "managed", "auth.json")
	payload, err := json.Marshal(map[string]any{
		"tokens": map[string]any{
			"access_token": testJWT(t, map[string]any{
				"exp":        time.Now().Add(time.Hour).Unix(),
				"account_id": "acct-uploaded",
			}),
		},
	})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	status, err := InstallAuthFile(payload, target)
	if err != nil {
		t.Fatalf("InstallAuthFile: %v", err)
	}
	if !status.LoggedIn || status.AccountID != "acct-uploaded" || status.AuthFileName != "auth.json" {
		t.Fatalf("status = %+v", status)
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("stored auth file: %v", err)
	}
}

func TestInstallAuthFileRejectsInvalidJSON(t *testing.T) {
	target := filepath.Join(t.TempDir(), "auth.json")
	if _, err := InstallAuthFile([]byte(`{"tokens":`), target); err == nil {
		t.Fatal("InstallAuthFile accepted invalid JSON")
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("invalid upload should not be stored: %v", err)
	}
}

func TestResolveRuntimeCredentialsRefreshesExpiredToken(t *testing.T) {
	authPath := filepath.Join(t.TempDir(), "auth.json")
	writeTestAuthFile(t, authPath, map[string]any{
		"access_token":  testJWT(t, map[string]any{"exp": time.Now().Add(-time.Hour).Unix()}),
		"refresh_token": "refresh-test",
	})
	t.Setenv("CODEX_AUTH_FILE", authPath)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method = %s", r.Method)
		}
		if err := r.ParseForm(); err != nil {
			t.Fatalf("ParseForm: %v", err)
		}
		if r.Form.Get("refresh_token") != "refresh-test" {
			t.Fatalf("refresh token = %q", r.Form.Get("refresh_token"))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": testJWT(t, map[string]any{
				"exp":        time.Now().Add(time.Hour).Unix(),
				"account_id": "acct-refreshed",
			}),
			"expires_in": 3600,
		})
	}))
	defer server.Close()

	previousTokenURL := tokenURL
	previousHTTPClient := httpClient
	tokenURL = server.URL
	httpClient = server.Client()
	t.Cleanup(func() {
		tokenURL = previousTokenURL
		httpClient = previousHTTPClient
	})

	credentials, err := ResolveRuntimeCredentials(context.Background(), "")
	if err != nil {
		t.Fatalf("ResolveRuntimeCredentials refresh: %v", err)
	}
	if credentials.AccountID != "acct-refreshed" {
		t.Fatalf("credentials = %+v", credentials)
	}

	data, err := os.ReadFile(authPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !strings.Contains(string(data), "acct-refreshed") {
		t.Fatalf("refreshed auth file = %s", string(data))
	}
}

func writeTestAuthFile(t *testing.T, path string, tokens map[string]any) {
	t.Helper()
	payload := map[string]any{"tokens": tokens}
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
}

func testJWT(t *testing.T, claims map[string]any) string {
	t.Helper()
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatalf("Marshal claims: %v", err)
	}
	return header + "." + base64.RawURLEncoding.EncodeToString(payload) + "."
}
