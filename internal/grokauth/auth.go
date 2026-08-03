package grokauth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"
)

const (
	ProviderID             = "xai-oauth"
	DefaultAccountID       = "default"
	DefaultBaseURL         = "https://api.x.ai/v1"
	discoveryURL           = "https://auth.x.ai/.well-known/openid-configuration"
	clientID               = "b1a00492-073a-47ea-816f-4c329264a828"
	scope                  = "openid profile email offline_access grok-cli:access api:access"
	redirectHost           = "127.0.0.1"
	redirectPort           = "56121"
	redirectPath           = "/callback"
	accessTokenRefreshSkew = 120 * time.Second
	callbackServerTimeout  = 10 * time.Minute
	loginStateIdle         = "idle"
	loginStatePending      = "pending"
	loginStateComplete     = "complete"
	loginStateFailed       = "failed"
)

var (
	httpClient             = http.DefaultClient
	activeDiscoveryURL     = discoveryURL
	allowInsecureEndpoints bool
	manualCodePattern      = regexp.MustCompile(`[A-Za-z0-9._-]{16,}`)

	loginMu      sync.Mutex
	activeLogin  *loginSession
	authStoreMu  sync.Mutex
	errNoSession = errors.New("no active Grok login session")
)

type AuthError struct {
	Code            string
	ReloginRequired bool
	Message         string
}

func (e *AuthError) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
}

type AuthStatus struct {
	LoggedIn    bool      `json:"logged_in"`
	Provider    string    `json:"provider,omitempty"`
	AccountID   string    `json:"account_id,omitempty"`
	AccountName string    `json:"account_name,omitempty"`
	Email       string    `json:"email,omitempty"`
	BaseURL     string    `json:"base_url,omitempty"`
	ExpiresAt   time.Time `json:"expires_at,omitempty"`
	NeedsReauth bool      `json:"needs_reauth,omitempty"`
	ActiveLogin string    `json:"active_login,omitempty"`
	Message     string    `json:"message,omitempty"`
}

type LoginStart struct {
	Status               AuthStatus `json:"status"`
	AuthorizeURL         string     `json:"authorize_url"`
	RedirectURI          string     `json:"redirect_uri"`
	ManualPasteSupported bool       `json:"manual_paste_supported"`
	LoopbackListening    bool       `json:"loopback_listening"`
	Message              string     `json:"message,omitempty"`
}

type LoginPoll struct {
	Status  AuthStatus `json:"status"`
	State   string     `json:"state"`
	Done    bool       `json:"done"`
	Message string     `json:"message,omitempty"`
}

type RuntimeCredentials struct {
	APIKey    string
	BaseURL   string
	AccountID string
}

type discoveryDocument struct {
	Issuer                string `json:"issuer"`
	AuthorizationEndpoint string `json:"authorization_endpoint"`
	TokenEndpoint         string `json:"token_endpoint"`
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	IDToken      string `json:"id_token"`
	TokenType    string `json:"token_type"`
	Scope        string `json:"scope"`
	ExpiresIn    int64  `json:"expires_in"`
}

type authStore struct {
	Version         int                     `json:"version"`
	ActiveAccountID string                  `json:"active_account_id,omitempty"`
	Accounts        map[string]accountState `json:"accounts,omitempty"`
}

type accountState struct {
	Provider    string            `json:"provider"`
	AccountID   string            `json:"account_id"`
	AccountName string            `json:"account_name,omitempty"`
	Email       string            `json:"email,omitempty"`
	BaseURL     string            `json:"base_url,omitempty"`
	RedirectURI string            `json:"redirect_uri,omitempty"`
	Discovery   discoveryDocument `json:"discovery,omitempty"`
	Tokens      tokenState        `json:"tokens"`
	CreatedAt   time.Time         `json:"created_at,omitempty"`
	UpdatedAt   time.Time         `json:"updated_at,omitempty"`
	LastRefresh time.Time         `json:"last_refresh,omitempty"`
}

type tokenState struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token,omitempty"`
	IDToken      string    `json:"id_token,omitempty"`
	TokenType    string    `json:"token_type,omitempty"`
	Scope        string    `json:"scope,omitempty"`
	ExpiresAt    time.Time `json:"expires_at,omitempty"`
}

type loginSession struct {
	accountID   string
	accountName string
	state       string
	nonce       string
	verifier    string
	redirectURI string
	discovery   discoveryDocument
	callbacks   chan callbackValues
	server      *http.Server
	listener    net.Listener
	status      string
	message     string
}

type callbackValues struct {
	code           string
	state          string
	oauthError     string
	description    string
	manualBareCode bool
}

func StartLogin(accountID, accountName string) (LoginStart, error) {
	loginMu.Lock()
	defer loginMu.Unlock()

	stopActiveLoginLocked()

	accountID = normalizeAccountID(firstNonEmpty(accountID, accountName))
	accountName = displayAccountName(accountName, accountID)

	discovery, err := fetchDiscovery(context.Background())
	if err != nil {
		return LoginStart{}, err
	}

	redirectURI := defaultRedirectURI()
	callbacks := make(chan callbackValues, 1)
	server, listener, bindMessage := startCallbackServer(callbacks)
	loopbackListening := server != nil && listener != nil

	verifier, err := randomURLToken(32)
	if err != nil {
		stopServer(server, listener)
		return LoginStart{}, fmt.Errorf("create PKCE verifier: %w", err)
	}
	state, err := randomURLToken(24)
	if err != nil {
		stopServer(server, listener)
		return LoginStart{}, fmt.Errorf("create OAuth state: %w", err)
	}
	nonce, err := randomURLToken(24)
	if err != nil {
		stopServer(server, listener)
		return LoginStart{}, fmt.Errorf("create OAuth nonce: %w", err)
	}

	authorizeURL, err := buildAuthorizeURL(discovery.AuthorizationEndpoint, redirectURI, pkceChallenge(verifier), state, nonce)
	if err != nil {
		stopServer(server, listener)
		return LoginStart{}, err
	}

	activeLogin = &loginSession{
		accountID:   accountID,
		accountName: accountName,
		state:       state,
		nonce:       nonce,
		verifier:    verifier,
		redirectURI: redirectURI,
		discovery:   discovery,
		callbacks:   callbacks,
		server:      server,
		listener:    listener,
		status:      loginStatePending,
		message:     bindMessage,
	}

	return LoginStart{
		Status: AuthStatus{
			AccountID:   accountID,
			AccountName: accountName,
			BaseURL:     DefaultBaseURL,
			ActiveLogin: loginStatePending,
			Message:     bindMessage,
		},
		AuthorizeURL:         authorizeURL,
		RedirectURI:          redirectURI,
		ManualPasteSupported: true,
		LoopbackListening:    loopbackListening,
		Message:              bindMessage,
	}, nil
}

func PollLogin(ctx context.Context) (LoginPoll, error) {
	loginMu.Lock()
	session := activeLogin
	if session == nil {
		status := GetStatus("")
		state := loginStateIdle
		if status.LoggedIn {
			state = loginStateComplete
		}
		loginMu.Unlock()
		return LoginPoll{Status: status, State: state, Done: status.LoggedIn}, nil
	}
	if session.status == loginStateComplete {
		status := GetStatus(session.accountID)
		loginMu.Unlock()
		return LoginPoll{Status: status, State: loginStateComplete, Done: true}, nil
	}
	if session.status == loginStateFailed {
		status := safeSessionStatus(session)
		loginMu.Unlock()
		return LoginPoll{Status: status, State: loginStateFailed, Message: session.message}, nil
	}

	select {
	case callback := <-session.callbacks:
		loginMu.Unlock()
		status, err := completeCallback(ctx, session, callback)
		if err != nil {
			markLoginFailed(session, err)
			return LoginPoll{Status: safeSessionStatus(session), State: loginStateFailed, Message: safeError(err)}, err
		}
		return LoginPoll{Status: status, State: loginStateComplete, Done: true}, nil
	default:
		status := safeSessionStatus(session)
		loginMu.Unlock()
		return LoginPoll{Status: status, State: loginStatePending, Message: session.message}, nil
	}
}

func CompleteLogin(ctx context.Context, callbackInput string) (AuthStatus, error) {
	callback, err := parseManualCallbackInput(callbackInput)
	if err != nil {
		return AuthStatus{}, err
	}

	loginMu.Lock()
	session := activeLogin
	if session == nil {
		loginMu.Unlock()
		return AuthStatus{}, &AuthError{Code: "xai_login_session_missing", ReloginRequired: true, Message: errNoSession.Error()}
	}
	loginMu.Unlock()

	status, err := completeCallback(ctx, session, callback)
	if err != nil {
		markLoginFailed(session, err)
		return AuthStatus{}, err
	}
	return status, nil
}

func GetStatus(accountID string) AuthStatus {
	authStoreMu.Lock()
	store, err := loadStore()
	authStoreMu.Unlock()
	if err != nil {
		return AuthStatus{NeedsReauth: true, BaseURL: DefaultBaseURL}
	}

	accountID = normalizeAccountID(accountID)
	if accountID == DefaultAccountID && store.ActiveAccountID != "" {
		accountID = store.ActiveAccountID
	}
	account, ok := store.Accounts[accountID]
	if !ok {
		return AuthStatus{
			AccountID:   accountID,
			AccountName: displayAccountName("", accountID),
			BaseURL:     DefaultBaseURL,
			NeedsReauth: true,
		}
	}

	loggedIn := account.Tokens.AccessToken != "" && account.Tokens.RefreshToken != ""
	return AuthStatus{
		LoggedIn:    loggedIn,
		Provider:    boolString(loggedIn, ProviderID),
		AccountID:   account.AccountID,
		AccountName: account.AccountName,
		Email:       account.Email,
		BaseURL:     validatedBaseURL(account.BaseURL),
		ExpiresAt:   tokenExpiry(account.Tokens),
		NeedsReauth: !loggedIn,
	}
}

func ResolveRuntimeCredentials(ctx context.Context, accountID string) (RuntimeCredentials, error) {
	authStoreMu.Lock()
	store, err := loadStore()
	if err != nil {
		authStoreMu.Unlock()
		return RuntimeCredentials{}, &AuthError{Code: "xai_auth_missing", ReloginRequired: true, Message: "Grok account is not logged in."}
	}
	accountID = normalizeAccountID(accountID)
	if accountID == DefaultAccountID && store.ActiveAccountID != "" {
		accountID = store.ActiveAccountID
	}
	account, ok := store.Accounts[accountID]
	if !ok || account.Tokens.AccessToken == "" {
		authStoreMu.Unlock()
		return RuntimeCredentials{}, &AuthError{Code: "xai_auth_missing", ReloginRequired: true, Message: "Grok account is not logged in."}
	}
	if tokenExpiring(account.Tokens) {
		refreshed, err := refreshTokens(ctx, account)
		if err != nil {
			authStoreMu.Unlock()
			return RuntimeCredentials{}, err
		}
		account = updateAccountTokens(account, refreshed)
		store.Accounts[accountID] = account
		store.ActiveAccountID = accountID
		if err := saveStore(store); err != nil {
			authStoreMu.Unlock()
			return RuntimeCredentials{}, fmt.Errorf("save Grok auth store: %w", err)
		}
	}
	authStoreMu.Unlock()

	return RuntimeCredentials{
		APIKey:    account.Tokens.AccessToken,
		BaseURL:   validatedBaseURL(account.BaseURL),
		AccountID: accountID,
	}, nil
}

func completeCallback(ctx context.Context, session *loginSession, callback callbackValues) (AuthStatus, error) {
	if session == nil {
		return AuthStatus{}, &AuthError{Code: "xai_login_session_missing", ReloginRequired: true, Message: errNoSession.Error()}
	}
	if callback.oauthError != "" {
		return AuthStatus{}, &AuthError{Code: "xai_authorization_failed", Message: "xAI authorization failed: " + callback.oauthError}
	}
	if callback.manualBareCode && callback.state == "" {
		if session.status != loginStatePending || session.verifier == "" {
			return AuthStatus{}, &AuthError{Code: "xai_login_session_missing", ReloginRequired: true, Message: "No active Grok login session. Start login again."}
		}
		callback.state = session.state
	}
	if callback.state == "" {
		return AuthStatus{}, &AuthError{Code: "xai_state_mismatch", Message: "xAI authorization failed: state mismatch."}
	}
	if callback.state != session.state {
		return AuthStatus{}, &AuthError{Code: "xai_state_mismatch", Message: "xAI authorization failed: state mismatch."}
	}
	if callback.code == "" {
		return AuthStatus{}, &AuthError{Code: "xai_code_missing", Message: "xAI authorization callback was missing a code."}
	}

	tokens, err := exchangeCode(ctx, session, callback.code)
	if err != nil {
		return AuthStatus{}, err
	}
	if tokens.IDToken != "" {
		if err := validateIDTokenNonce(tokens.IDToken, session.nonce); err != nil {
			return AuthStatus{}, err
		}
	}

	account := accountState{
		Provider:    ProviderID,
		AccountID:   session.accountID,
		AccountName: session.accountName,
		BaseURL:     DefaultBaseURL,
		RedirectURI: session.redirectURI,
		Discovery:   session.discovery,
		Tokens:      tokenStateFromResponse(tokens, tokenState{}),
		CreatedAt:   time.Now().UTC(),
		UpdatedAt:   time.Now().UTC(),
	}
	account.Email = profileEmail(account.Tokens.IDToken)

	authStoreMu.Lock()
	store, err := loadStore()
	if err != nil {
		store = authStore{Version: 1, Accounts: make(map[string]accountState)}
	}
	if store.Accounts == nil {
		store.Accounts = make(map[string]accountState)
	}
	if previous, ok := store.Accounts[session.accountID]; ok && !previous.CreatedAt.IsZero() {
		account.CreatedAt = previous.CreatedAt
	}
	store.Accounts[session.accountID] = account
	store.ActiveAccountID = session.accountID
	if err := saveStore(store); err != nil {
		authStoreMu.Unlock()
		return AuthStatus{}, fmt.Errorf("save Grok auth store: %w", err)
	}
	authStoreMu.Unlock()

	loginMu.Lock()
	if activeLogin == session {
		session.status = loginStateComplete
		stopActiveLoginLocked()
	}
	loginMu.Unlock()

	return GetStatus(session.accountID), nil
}

func fetchDiscovery(ctx context.Context) (discoveryDocument, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, activeDiscoveryURL, nil)
	if err != nil {
		return discoveryDocument{}, err
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return discoveryDocument{}, fmt.Errorf("Grok OIDC discovery failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return discoveryDocument{}, fmt.Errorf("Grok OIDC discovery failed: HTTP %d", resp.StatusCode)
	}
	var doc discoveryDocument
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&doc); err != nil {
		return discoveryDocument{}, fmt.Errorf("decode Grok OIDC discovery: %w", err)
	}
	if err := validateAuthEndpoint(doc.AuthorizationEndpoint); err != nil {
		return discoveryDocument{}, err
	}
	if err := validateAuthEndpoint(doc.TokenEndpoint); err != nil {
		return discoveryDocument{}, err
	}
	return doc, nil
}

func exchangeCode(ctx context.Context, session *loginSession, code string) (tokenResponse, error) {
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("client_id", clientID)
	form.Set("code", code)
	form.Set("code_verifier", session.verifier)
	form.Set("redirect_uri", session.redirectURI)
	return tokenRequest(ctx, session.discovery.TokenEndpoint, form)
}

func refreshTokens(ctx context.Context, account accountState) (tokenResponse, error) {
	refreshToken := strings.TrimSpace(account.Tokens.RefreshToken)
	if refreshToken == "" {
		return tokenResponse{}, &AuthError{Code: "xai_auth_missing_refresh_token", ReloginRequired: true, Message: "Grok OAuth refresh token is missing. Please log in again."}
	}
	tokenEndpoint := account.Discovery.TokenEndpoint
	if tokenEndpoint == "" {
		discovery, err := fetchDiscovery(ctx)
		if err != nil {
			return tokenResponse{}, err
		}
		tokenEndpoint = discovery.TokenEndpoint
		account.Discovery = discovery
	}
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("client_id", clientID)
	form.Set("refresh_token", refreshToken)
	return tokenRequest(ctx, tokenEndpoint, form)
}

func tokenRequest(ctx context.Context, endpoint string, form url.Values) (tokenResponse, error) {
	if err := validateAuthEndpoint(endpoint); err != nil {
		return tokenResponse{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return tokenResponse{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return tokenResponse{}, fmt.Errorf("Grok OAuth token request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return tokenResponse{}, fmt.Errorf("Grok OAuth token request failed: HTTP %d", resp.StatusCode)
	}
	var tokens tokenResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&tokens); err != nil {
		return tokenResponse{}, fmt.Errorf("decode Grok OAuth token response: %w", err)
	}
	if strings.TrimSpace(tokens.AccessToken) == "" {
		return tokenResponse{}, &AuthError{Code: "xai_token_invalid", Message: "Grok OAuth token response was missing access_token."}
	}
	return tokens, nil
}

func startCallbackServer(callbacks chan<- callbackValues) (*http.Server, net.Listener, string) {
	addr := net.JoinHostPort(redirectHost, redirectPort)
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, nil, "Loopback callback port is unavailable; paste the callback URL manually after login."
	}

	mux := http.NewServeMux()
	server := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	mux.HandleFunc(redirectPath, func(w http.ResponseWriter, r *http.Request) {
		values := r.URL.Query()
		callback := callbackValues{
			code:        values.Get("code"),
			state:       values.Get("state"),
			oauthError:  values.Get("error"),
			description: values.Get("error_description"),
		}
		select {
		case callbacks <- callback:
		default:
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, "<html><body>Grok login callback received. You can return to AINovel.</body></html>")
	})

	go func() {
		_ = server.Serve(listener)
	}()
	go func() {
		time.Sleep(callbackServerTimeout)
		_ = server.Close()
	}()
	return server, listener, ""
}

func parseManualCallbackInput(raw string) (callbackValues, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return callbackValues{}, &AuthError{Code: "xai_callback_empty", Message: "Paste the Grok callback URL or query string."}
	}
	var query url.Values
	if strings.HasPrefix(raw, "?") {
		var err error
		query, err = url.ParseQuery(strings.TrimPrefix(raw, "?"))
		if err != nil {
			return callbackValues{}, &AuthError{Code: "xai_callback_invalid", Message: "Invalid callback query string."}
		}
	} else if parsed, ok := parseAbsoluteURL(raw); ok {
		if isAuthorizeURL(parsed) {
			return callbackValues{}, &AuthError{Code: "xai_callback_authorize_url", Message: "You pasted the Grok login URL, not the callback URL. Finish browser login first, then paste the address that starts with http://127.0.0.1:56121/callback?code=..., or paste the one-time code shown by xAI."}
		}
		if err := validateLoopbackCallbackURL(parsed); err != nil {
			return callbackValues{}, err
		}
		query = parsed.Query()
	} else if looksLikeCallbackQuery(raw) {
		var err error
		query, err = url.ParseQuery(raw)
		if err != nil {
			return callbackValues{}, &AuthError{Code: "xai_callback_invalid", Message: "Invalid callback query string."}
		}
	} else if code, ok := extractBareManualCode(raw); ok {
		return callbackValues{code: code, manualBareCode: true}, nil
	} else {
		return callbackValues{}, &AuthError{Code: "xai_callback_invalid", Message: "Paste the callback URL after browser login, the one-time code shown by xAI, or a query string beginning with ?code=."}
	}
	callback := callbackValues{
		code:        query.Get("code"),
		state:       query.Get("state"),
		oauthError:  query.Get("error"),
		description: query.Get("error_description"),
	}
	if callback.oauthError == "" && callback.code != "" && callback.state == "" {
		return callbackValues{}, &AuthError{Code: "xai_callback_state_missing", Message: "Callback is missing state. Please paste the full callback URL, or paste ?code=...&state=... from the browser address bar."}
	}
	return callback, nil
}

func parseAbsoluteURL(raw string) (*url.URL, bool) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, false
	}
	return parsed, true
}

func isAuthorizeURL(parsed *url.URL) bool {
	path := strings.ToLower(parsed.Path)
	return strings.EqualFold(parsed.Hostname(), "auth.x.ai") && strings.Contains(path, "authorize")
}

func looksLikeCallbackQuery(raw string) bool {
	lowered := strings.ToLower(raw)
	if !strings.Contains(raw, "=") {
		return false
	}
	return strings.Contains(lowered, "code=") || strings.Contains(lowered, "state=") || strings.Contains(lowered, "error=")
}

func extractBareManualCode(raw string) (string, bool) {
	raw = strings.TrimSpace(strings.Trim(raw, `"'`))
	if raw == "" || strings.Contains(raw, "://") || strings.Contains(raw, "=") {
		return "", false
	}
	collapsed := removeWhitespace(raw)
	if isManualCodeToken(collapsed) {
		return collapsed, true
	}
	matches := manualCodePattern.FindAllString(raw, -1)
	longest := ""
	for _, match := range matches {
		if len(match) > len(longest) {
			longest = match
		}
	}
	if isManualCodeToken(longest) {
		return longest, true
	}
	return "", false
}

func removeWhitespace(raw string) string {
	var b strings.Builder
	for _, r := range raw {
		if r == ' ' || r == '\t' || r == '\r' || r == '\n' {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func isManualCodeToken(value string) bool {
	if len(value) < 16 {
		return false
	}
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '-' || r == '_' || r == '.':
		default:
			return false
		}
	}
	return true
}

func validateLoopbackCallbackURL(parsed *url.URL) error {
	if parsed.Scheme != "http" {
		return &AuthError{Code: "xai_callback_invalid", Message: "Grok callback URL must use http on 127.0.0.1."}
	}
	if parsed.Hostname() != redirectHost || parsed.Port() != redirectPort || parsed.Path != redirectPath {
		return &AuthError{Code: "xai_callback_invalid", Message: "Grok callback URL host, port, or path does not match the expected loopback redirect."}
	}
	return nil
}

func buildAuthorizeURL(endpoint, redirectURI, challenge, state, nonce string) (string, error) {
	if err := validateAuthEndpoint(endpoint); err != nil {
		return "", err
	}
	u, err := url.Parse(endpoint)
	if err != nil {
		return "", err
	}
	q := u.Query()
	q.Set("response_type", "code")
	q.Set("client_id", clientID)
	q.Set("redirect_uri", redirectURI)
	q.Set("scope", scope)
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", "S256")
	q.Set("state", state)
	q.Set("nonce", nonce)
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func validateAuthEndpoint(raw string) error {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return fmt.Errorf("invalid Grok OAuth endpoint")
	}
	if allowInsecureEndpoints {
		return nil
	}
	if parsed.Scheme != "https" || !strings.EqualFold(parsed.Hostname(), "auth.x.ai") {
		return fmt.Errorf("Grok OAuth endpoint must be on https://auth.x.ai")
	}
	return nil
}

func validateIDTokenNonce(idToken, expectedNonce string) error {
	payload := jwtPayload(idToken)
	nonce, _ := payload["nonce"].(string)
	if nonce != "" && nonce != expectedNonce {
		return &AuthError{Code: "xai_nonce_mismatch", Message: "xAI authorization failed: nonce mismatch."}
	}
	return nil
}

func profileEmail(idToken string) string {
	payload := jwtPayload(idToken)
	email, _ := payload["email"].(string)
	return strings.TrimSpace(email)
}

func jwtPayload(token string) map[string]any {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return nil
	}
	decoded, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil
	}
	var payload map[string]any
	if err := json.Unmarshal(decoded, &payload); err != nil {
		return nil
	}
	return payload
}

func tokenStateFromResponse(resp tokenResponse, previous tokenState) tokenState {
	state := tokenState{
		AccessToken:  strings.TrimSpace(resp.AccessToken),
		RefreshToken: strings.TrimSpace(resp.RefreshToken),
		IDToken:      strings.TrimSpace(resp.IDToken),
		TokenType:    strings.TrimSpace(resp.TokenType),
		Scope:        strings.TrimSpace(resp.Scope),
	}
	if state.RefreshToken == "" {
		state.RefreshToken = previous.RefreshToken
	}
	if state.IDToken == "" {
		state.IDToken = previous.IDToken
	}
	if state.TokenType == "" {
		state.TokenType = previous.TokenType
	}
	if state.Scope == "" {
		state.Scope = previous.Scope
	}
	if resp.ExpiresIn > 0 {
		state.ExpiresAt = time.Now().UTC().Add(time.Duration(resp.ExpiresIn) * time.Second)
	} else {
		state.ExpiresAt = expiryFromJWT(state.AccessToken)
	}
	if state.ExpiresAt.IsZero() {
		state.ExpiresAt = previous.ExpiresAt
	}
	return state
}

func updateAccountTokens(account accountState, resp tokenResponse) accountState {
	account.Tokens = tokenStateFromResponse(resp, account.Tokens)
	account.Email = firstNonEmpty(profileEmail(account.Tokens.IDToken), account.Email)
	account.BaseURL = validatedBaseURL(account.BaseURL)
	account.UpdatedAt = time.Now().UTC()
	account.LastRefresh = account.UpdatedAt
	return account
}

func tokenExpiry(tokens tokenState) time.Time {
	if !tokens.ExpiresAt.IsZero() {
		return tokens.ExpiresAt
	}
	return expiryFromJWT(tokens.AccessToken)
}

func tokenExpiring(tokens tokenState) bool {
	expiry := tokenExpiry(tokens)
	if expiry.IsZero() {
		return false
	}
	return time.Until(expiry) <= accessTokenRefreshSkew
}

func expiryFromJWT(token string) time.Time {
	payload := jwtPayload(token)
	exp, ok := payload["exp"].(float64)
	if !ok || exp <= 0 {
		return time.Time{}
	}
	return time.Unix(int64(exp), 0).UTC()
}

func loadStore() (authStore, error) {
	path := storePath()
	data, err := os.ReadFile(path)
	if err != nil {
		return authStore{}, err
	}
	var store authStore
	if err := json.Unmarshal(data, &store); err != nil {
		return authStore{}, err
	}
	if store.Accounts == nil {
		store.Accounts = make(map[string]accountState)
	}
	return store, nil
}

func saveStore(store authStore) error {
	if store.Version == 0 {
		store.Version = 1
	}
	if store.Accounts == nil {
		store.Accounts = make(map[string]accountState)
	}
	data, err := json.MarshalIndent(store, "", "  ")
	if err != nil {
		return err
	}
	path := storePath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "grok-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil && runtime.GOOS != "windows" {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		if runtime.GOOS != "windows" {
			return err
		}
		_ = os.Remove(path)
		if err := os.Rename(tmpName, path); err != nil {
			return err
		}
	}
	return nil
}

func storePath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".ainovel", "auth", "grok.json")
	}
	return filepath.Join(home, ".ainovel", "auth", "grok.json")
}

func defaultRedirectURI() string {
	return "http://" + net.JoinHostPort(redirectHost, redirectPort) + redirectPath
}

func randomURLToken(bytes int) (string, error) {
	buf := make([]byte, bytes)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func pkceChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func normalizeAccountID(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return DefaultAccountID
	}
	var b strings.Builder
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == '_':
			b.WriteRune(r)
		case r == ' ' || r == '.':
			b.WriteRune('-')
		}
	}
	out := strings.Trim(b.String(), "-_")
	if out == "" {
		return DefaultAccountID
	}
	return out
}

func displayAccountName(value, accountID string) string {
	value = strings.TrimSpace(value)
	if value != "" {
		return value
	}
	if accountID == "" || accountID == DefaultAccountID {
		return "Default"
	}
	return accountID
}

func validatedBaseURL(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return DefaultBaseURL
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return DefaultBaseURL
	}
	return strings.TrimRight(value, "/")
}

func safeSessionStatus(session *loginSession) AuthStatus {
	if session == nil {
		return AuthStatus{BaseURL: DefaultBaseURL}
	}
	return AuthStatus{
		AccountID:   session.accountID,
		AccountName: session.accountName,
		BaseURL:     DefaultBaseURL,
		ActiveLogin: session.status,
		Message:     session.message,
	}
}

func markLoginFailed(session *loginSession, err error) {
	loginMu.Lock()
	defer loginMu.Unlock()
	if session == nil {
		return
	}
	session.status = loginStateFailed
	session.message = safeError(err)
	if activeLogin == session {
		stopActiveLoginLocked()
	}
}

func stopActiveLoginLocked() {
	if activeLogin == nil {
		return
	}
	stopServer(activeLogin.server, activeLogin.listener)
	activeLogin = nil
}

func stopServer(server *http.Server, listener net.Listener) {
	if server != nil {
		_ = server.Close()
	}
	if listener != nil {
		_ = listener.Close()
	}
}

func safeError(err error) string {
	if err == nil {
		return ""
	}
	var authErr *AuthError
	if errors.As(err, &authErr) {
		return authErr.Message
	}
	return strings.ReplaceAll(err.Error(), "\n", " ")
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func boolString(ok bool, value string) string {
	if ok {
		return value
	}
	return ""
}
