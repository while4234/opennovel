package grokauth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestManualLoginAndRuntimeRefresh(t *testing.T) {
	resetAuthTestState(t)

	var tokenRequests []url.Values
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			writeJSON(t, w, map[string]string{
				"issuer":                 serverIssuer(r),
				"authorization_endpoint": serverIssuer(r) + "/authorize",
				"token_endpoint":         serverIssuer(r) + "/token",
			})
		case "/token":
			if err := r.ParseForm(); err != nil {
				t.Fatalf("ParseForm: %v", err)
			}
			tokenRequests = append(tokenRequests, r.Form)
			switch r.Form.Get("grant_type") {
			case "authorization_code":
				writeJSON(t, w, map[string]any{
					"access_token":  "access-token-1",
					"refresh_token": "refresh-token-1",
					"id_token":      testIDToken(currentLoginNonce(), "user@example.com"),
					"expires_in":    1,
				})
			case "refresh_token":
				writeJSON(t, w, map[string]any{
					"access_token": "access-token-2",
					"expires_in":   3600,
				})
			default:
				t.Fatalf("unexpected grant_type %q", r.Form.Get("grant_type"))
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	activeDiscoveryURL = server.URL + "/.well-known/openid-configuration"

	start, err := StartLogin("work", "Work")
	if err != nil {
		t.Fatalf("StartLogin: %v", err)
	}
	state := authorizeState(t, start.AuthorizeURL)

	status, err := CompleteLogin(context.Background(), "?code=auth-code&state="+url.QueryEscape(state))
	if err != nil {
		t.Fatalf("CompleteLogin: %v", err)
	}
	if !status.LoggedIn || status.Email != "user@example.com" {
		t.Fatalf("status = %#v", status)
	}

	credentials, err := ResolveRuntimeCredentials(context.Background(), "work")
	if err != nil {
		t.Fatalf("ResolveRuntimeCredentials: %v", err)
	}
	if credentials.APIKey != "access-token-2" {
		t.Fatalf("runtime access token = %q, want refreshed token", credentials.APIKey)
	}
	if len(tokenRequests) != 2 {
		t.Fatalf("token request count = %d, want auth+refresh", len(tokenRequests))
	}
	if tokenRequests[0].Get("code_verifier") == "" {
		t.Fatal("authorization_code request should include PKCE code_verifier")
	}

	safeStatus := GetStatus("work")
	encoded, err := json.Marshal(safeStatus)
	if err != nil {
		t.Fatalf("Marshal status: %v", err)
	}
	for _, secret := range []string{"access-token", "refresh-token", "auth-code", "code_verifier"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("status leaked secret %q: %s", secret, encoded)
		}
	}
}

func TestManualLoginRejectsStateMismatch(t *testing.T) {
	resetAuthTestState(t)
	server := newOAuthTestServer(t, 3600)
	defer server.Close()
	activeDiscoveryURL = server.URL + "/.well-known/openid-configuration"

	if _, err := StartLogin("work", "Work"); err != nil {
		t.Fatalf("StartLogin: %v", err)
	}
	_, err := CompleteLogin(context.Background(), "?code=auth-code&state=wrong")
	if err == nil {
		t.Fatal("expected state mismatch error")
	}
	authErr, ok := err.(*AuthError)
	if !ok || authErr.Code != "xai_state_mismatch" {
		t.Fatalf("error = %#v", err)
	}
}

func TestManualLoginRejectsAuthorizeURLWithSpecificError(t *testing.T) {
	resetAuthTestState(t)

	_, err := parseManualCallbackInput("https://auth.x.ai/oauth2/authorize?client_id=x")
	if err == nil {
		t.Fatal("expected authorize URL to be rejected")
	}
	authErr, ok := err.(*AuthError)
	if !ok || authErr.Code != "xai_callback_authorize_url" {
		t.Fatalf("error = %#v", err)
	}
	if !strings.Contains(authErr.Message, "not the callback URL") {
		t.Fatalf("message should explain authorize-vs-callback mismatch: %q", authErr.Message)
	}
}

func TestManualLoginAcceptsBareCodeWhileSessionIsPending(t *testing.T) {
	resetAuthTestState(t)
	server := newOAuthTestServer(t, 3600)
	defer server.Close()
	activeDiscoveryURL = server.URL + "/.well-known/openid-configuration"

	if _, err := StartLogin("work", "Work"); err != nil {
		t.Fatalf("StartLogin: %v", err)
	}
	status, err := CompleteLogin(context.Background(), "hOQrsFbIo1Qy9tT9NVTssjcBGxlG5_DQRYkkWzabbSp3Cb7s2VH_4kgSbVShLpmNhStIesKDuI3kvxZQ2gAjpQ")
	if err != nil {
		t.Fatalf("CompleteLogin: %v", err)
	}
	if !status.LoggedIn {
		t.Fatalf("status = %#v", status)
	}
}

func TestManualLoginExtractsBareCodeFromWhitespaceAndPageText(t *testing.T) {
	resetAuthTestState(t)

	raw := "输入此代码以完成登录\r\n  hOQrsFbIo1Qy9tT9NVTssjcBGxlG5_DQRYkkWzabbSp3Cb7s2VH_4kgSbVShLpmNhStIesKDuI3kvxZQ2gAjpQ  "
	callback, err := parseManualCallbackInput(raw)
	if err != nil {
		t.Fatalf("parseManualCallbackInput: %v", err)
	}
	if !callback.manualBareCode || callback.code == "" {
		t.Fatalf("callback = %#v", callback)
	}
}

func TestManualLoginAcceptsQueryWithoutQuestionMark(t *testing.T) {
	resetAuthTestState(t)

	callback, err := parseManualCallbackInput("code=auth-code&state=state-value")
	if err != nil {
		t.Fatalf("parseManualCallbackInput: %v", err)
	}
	if callback.code != "auth-code" || callback.state != "state-value" {
		t.Fatalf("callback = %#v", callback)
	}
}

func TestManualLoginAcceptsFullCallbackURL(t *testing.T) {
	resetAuthTestState(t)
	server := newOAuthTestServer(t, 3600)
	defer server.Close()
	activeDiscoveryURL = server.URL + "/.well-known/openid-configuration"

	start, err := StartLogin("work", "Work")
	if err != nil {
		t.Fatalf("StartLogin: %v", err)
	}
	state := authorizeState(t, start.AuthorizeURL)
	callback := "http://127.0.0.1:56121/callback?code=auth-code&state=" + url.QueryEscape(state)

	status, err := CompleteLogin(context.Background(), callback)
	if err != nil {
		t.Fatalf("CompleteLogin: %v", err)
	}
	if !status.LoggedIn {
		t.Fatalf("status = %#v", status)
	}
}

func resetAuthTestState(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	oldClient := httpClient
	oldDiscovery := activeDiscoveryURL
	oldAllow := allowInsecureEndpoints
	httpClient = http.DefaultClient
	allowInsecureEndpoints = true
	loginMu.Lock()
	stopActiveLoginLocked()
	loginMu.Unlock()
	t.Cleanup(func() {
		loginMu.Lock()
		stopActiveLoginLocked()
		loginMu.Unlock()
		httpClient = oldClient
		activeDiscoveryURL = oldDiscovery
		allowInsecureEndpoints = oldAllow
	})
}

func newOAuthTestServer(t *testing.T, expiresIn int64) *httptest.Server {
	t.Helper()
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			writeJSON(t, w, map[string]string{
				"issuer":                 server.URL,
				"authorization_endpoint": server.URL + "/authorize",
				"token_endpoint":         server.URL + "/token",
			})
		case "/token":
			writeJSON(t, w, map[string]any{
				"access_token":  "access-token",
				"refresh_token": "refresh-token",
				"id_token":      testIDToken(currentLoginNonce(), "user@example.com"),
				"expires_in":    expiresIn,
			})
		default:
			http.NotFound(w, r)
		}
	}))
	return server
}

func currentLoginNonce() string {
	loginMu.Lock()
	defer loginMu.Unlock()
	if activeLogin == nil {
		return ""
	}
	return activeLogin.nonce
}

func authorizeState(t *testing.T, raw string) string {
	t.Helper()
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse authorize URL: %v", err)
	}
	state := parsed.Query().Get("state")
	if state == "" {
		t.Fatal("authorize URL missing state")
	}
	return state
}

func testIDToken(nonce, email string) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
	payload, _ := json.Marshal(map[string]any{
		"nonce": nonce,
		"email": email,
		"exp":   time.Now().Add(time.Hour).Unix(),
	})
	return header + "." + base64.RawURLEncoding.EncodeToString(payload) + "."
}

func writeJSON(t *testing.T, w http.ResponseWriter, value any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(value); err != nil {
		t.Fatalf("Encode: %v", err)
	}
}

func serverIssuer(r *http.Request) string {
	return "http://" + r.Host
}
