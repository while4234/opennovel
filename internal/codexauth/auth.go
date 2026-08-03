package codexauth

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	ProviderID                   = "codex"
	DefaultBaseURL               = "https://chatgpt.com/backend-api/codex"
	DefaultUserAgent             = "codex_cli/0.126.0-alpha.8"
	DefaultOriginator            = "codex_vscode"
	OAuthTokenURL                = "https://auth.openai.com/oauth/token"
	AppServerLoginClientID       = "app_EMoamEEZ73f0CkXaXp7hrann"
	accessTokenRefreshSkew       = 60 * time.Second
	tokenRefreshTimeout          = 30 * time.Second
	maxCodexAuthFileSize   int64 = 1 << 20
)

var (
	httpClientMu sync.Mutex
	httpClient   = http.DefaultClient
	tokenURL     = OAuthTokenURL
	clientID     = AppServerLoginClientID
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
	LoggedIn           bool      `json:"logged_in"`
	Provider           string    `json:"provider,omitempty"`
	AccountID          string    `json:"account_id,omitempty"`
	BaseURL            string    `json:"base_url,omitempty"`
	AuthFileConfigured bool      `json:"auth_file_configured,omitempty"`
	AuthFileExists     bool      `json:"auth_file_exists,omitempty"`
	AuthFileName       string    `json:"auth_file_name,omitempty"`
	ExpiresAt          time.Time `json:"expires_at,omitempty"`
	NeedsReauth        bool      `json:"needs_reauth,omitempty"`
	Message            string    `json:"message,omitempty"`
}

type RuntimeCredentials struct {
	APIKey    string
	BaseURL   string
	AccountID string
}

type authPayload map[string]any

func ResolveRuntimeCredentials(ctx context.Context, authFile string) (RuntimeCredentials, error) {
	var lastErr error
	for _, path := range candidateAuthPaths(authFile) {
		payload, err := loadPayload(path)
		if err != nil {
			lastErr = err
			continue
		}
		payload, err = refreshPayloadIfNeeded(ctx, path, payload)
		if err != nil {
			lastErr = err
			if !allowsAuthPathFallback(err) {
				break
			}
			continue
		}
		tokens, err := tokensFromPayload(payload)
		if err != nil {
			lastErr = err
			continue
		}
		accessToken := tokenString(tokens, "access_token")
		if accessToken == "" {
			lastErr = authError("codex_auth_invalid", true, "Codex auth file is missing an access token.")
			continue
		}
		if expiredWithoutRefresh(tokens) {
			lastErr = authError("codex_auth_expired", true, "Codex access token is expired. Run codex login and retry.")
			continue
		}
		return RuntimeCredentials{
			APIKey:    accessToken,
			BaseURL:   DefaultBaseURL,
			AccountID: accountIDFromTokens(tokens),
		}, nil
	}
	if lastErr != nil {
		return RuntimeCredentials{}, lastErr
	}
	return RuntimeCredentials{}, authError("codex_auth_missing", true, "Codex auth file not found. Run codex login or set CODEX_AUTH_FILE.")
}

func GetStatus(authFile string) AuthStatus {
	paths := candidateAuthPaths(authFile)
	status := AuthStatus{
		BaseURL:            DefaultBaseURL,
		AuthFileConfigured: strings.TrimSpace(authFile) != "" || strings.TrimSpace(os.Getenv("CODEX_AUTH_FILE")) != "",
		NeedsReauth:        true,
	}
	if len(paths) == 0 {
		status.Message = "Codex auth file not found. Run codex login or set CODEX_AUTH_FILE."
		return status
	}
	path := paths[0]
	status.AuthFileExists = true
	status.AuthFileName = filepath.Base(path)

	payload, err := loadPayload(path)
	if err != nil {
		status.Message = safeStatusError(err)
		return status
	}
	tokens, err := tokensFromPayload(payload)
	if err != nil {
		status.Message = safeStatusError(err)
		return status
	}
	status.AccountID = accountIDFromTokens(tokens)
	status.ExpiresAt = expiresAtFromTokens(tokens)
	hasAccessToken := tokenString(tokens, "access_token") != ""
	hasRefreshToken := tokenString(tokens, "refresh_token") != ""
	expired := tokenExpired(tokens)
	status.LoggedIn = hasAccessToken && (!expired || hasRefreshToken)
	status.Provider = boolString(status.LoggedIn, ProviderID)
	status.NeedsReauth = !status.LoggedIn
	switch {
	case status.LoggedIn && expired && hasRefreshToken:
		status.Message = "Codex token will be refreshed on use."
	case status.LoggedIn:
		status.Message = "Codex login is available."
	case hasAccessToken:
		status.Message = "Codex access token is expired. Run codex login and retry."
	default:
		status.Message = "Codex auth file is missing an access token."
	}
	return status
}

func ResolveAuthPath(authFile string) string {
	if value := strings.TrimSpace(authFile); value != "" {
		return expandHome(value)
	}
	if envPath := strings.TrimSpace(os.Getenv("CODEX_AUTH_FILE")); envPath != "" {
		return expandHome(envPath)
	}
	return DefaultAuthPath()
}

// InstallAuthFile validates and atomically stores an uploaded Codex login.
// Callers should persist only the target path, never the credential payload.
func InstallAuthFile(data []byte, target string) (AuthStatus, error) {
	if len(data) == 0 {
		return AuthStatus{}, authError("codex_auth_invalid", true, "Codex auth file is empty.")
	}
	if int64(len(data)) > maxCodexAuthFileSize {
		return AuthStatus{}, authError("codex_auth_invalid", true, "Codex auth file exceeds the 1 MiB limit.")
	}
	data = bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF})
	var payload authPayload
	if err := json.Unmarshal(data, &payload); err != nil {
		return AuthStatus{}, authError("codex_auth_invalid", true, "Codex auth file is not valid JSON.")
	}
	if payload == nil {
		return AuthStatus{}, authError("codex_auth_invalid", true, "Codex auth file must contain a JSON object.")
	}
	tokens, err := tokensFromPayload(payload)
	if err != nil {
		return AuthStatus{}, err
	}
	if tokenString(tokens, "access_token") == "" {
		return AuthStatus{}, authError("codex_auth_invalid", true, "Codex auth file is missing an access token.")
	}
	if expiredWithoutRefresh(tokens) {
		return AuthStatus{}, authError("codex_auth_expired", true, "Codex access token is expired. Run codex login and retry.")
	}
	target = strings.TrimSpace(target)
	if target == "" {
		return AuthStatus{}, fmt.Errorf("Codex auth target path is required")
	}
	if err := savePayload(target, payload); err != nil {
		return AuthStatus{}, fmt.Errorf("save uploaded Codex auth file: %w", err)
	}
	return GetStatus(target), nil
}

func DefaultAuthPath() string {
	if codexHome := strings.TrimSpace(os.Getenv("CODEX_HOME")); codexHome != "" {
		return filepath.Join(expandHome(codexHome), "auth.json")
	}
	projectPath := filepath.Join(mustGetwd(), ".codex", "auth.json")
	if _, err := os.Stat(projectPath); err == nil {
		return projectPath
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".codex", "auth.json")
	}
	return filepath.Join(home, ".codex", "auth.json")
}

func candidateAuthPaths(authFile string) []string {
	paths := []string{ResolveAuthPath(authFile)}
	paths = append(paths, DefaultAuthPath())
	paths = append(paths, filepath.Join(mustGetwd(), ".codex", "auth.json"))
	if home, err := os.UserHomeDir(); err == nil {
		paths = append(paths, filepath.Join(home, ".codex", "auth.json"))
	}
	out := make([]string, 0, len(paths))
	seen := make(map[string]bool, len(paths))
	for _, raw := range paths {
		path := expandHome(raw)
		if path == "" {
			continue
		}
		if _, err := os.Stat(path); err != nil {
			continue
		}
		key := canonicalPathKey(path)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, path)
	}
	return out
}

func loadPayload(path string) (authPayload, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, authError("codex_auth_missing", true, "Codex auth file not found. Run codex login or set CODEX_AUTH_FILE.")
	}
	defer file.Close()

	data, err := io.ReadAll(io.LimitReader(file, maxCodexAuthFileSize))
	if err != nil {
		return nil, fmt.Errorf("read Codex auth file: %w", err)
	}
	data = bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF})
	var payload authPayload
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, authError("codex_auth_invalid", true, "Codex auth file is not valid JSON.")
	}
	if payload == nil {
		return nil, authError("codex_auth_invalid", true, "Codex auth file must contain a JSON object.")
	}
	return payload, nil
}

func tokensFromPayload(payload authPayload) (map[string]any, error) {
	raw, ok := payload["tokens"]
	if !ok {
		return nil, authError("codex_auth_invalid", true, "Codex auth file is missing the tokens object.")
	}
	tokens, ok := raw.(map[string]any)
	if !ok {
		return nil, authError("codex_auth_invalid", true, "Codex auth file tokens field must be an object.")
	}
	return cloneAnyMap(tokens), nil
}

func refreshPayloadIfNeeded(ctx context.Context, path string, payload authPayload) (authPayload, error) {
	tokens, err := tokensFromPayload(payload)
	if err != nil {
		return nil, err
	}
	if !tokensNeedRefresh(tokens) {
		return payload, nil
	}
	latest, err := loadPayload(path)
	if err != nil {
		return nil, err
	}
	latestTokens, err := tokensFromPayload(latest)
	if err != nil {
		return nil, err
	}
	if !tokensNeedRefresh(latestTokens) {
		return latest, nil
	}
	refreshToken := tokenString(latestTokens, "refresh_token")
	if refreshToken == "" {
		return nil, authError("codex_auth_not_refreshable", true, "Codex access token is expired and no refresh token is available. Run codex login.")
	}
	refreshed, err := refreshTokens(ctx, refreshToken)
	if err != nil {
		if recovered, ok := freshPayloadFromConcurrentRefresh(path); ok {
			return recovered, nil
		}
		return nil, err
	}
	updated := clonePayload(latest)
	updated["tokens"] = mergeRefreshedTokens(latestTokens, refreshed)
	updated["last_refresh"] = time.Now().UTC().Format(time.RFC3339)
	if err := savePayload(path, updated); err != nil {
		return nil, fmt.Errorf("save Codex auth file: %w", err)
	}
	return updated, nil
}

func refreshTokens(ctx context.Context, refreshToken string) (map[string]any, error) {
	ctx, cancel := context.WithTimeout(ctx, tokenRefreshTimeout)
	defer cancel()

	activeClientID := strings.TrimSpace(os.Getenv("CODEX_OAUTH_CLIENT_ID"))
	if activeClientID == "" {
		activeClientID = clientID
	}
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("client_id", activeClientID)
	form.Set("refresh_token", refreshToken)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	httpClientMu.Lock()
	client := httpClient
	httpClientMu.Unlock()
	resp, err := client.Do(req)
	if err != nil {
		return nil, authError("codex_auth_refresh_failed", false, "Codex token refresh request failed.")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, authError("codex_auth_refresh_failed", resp.StatusCode == http.StatusUnauthorized, refreshErrorMessage(resp))
	}
	var payload map[string]any
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxCodexAuthFileSize)).Decode(&payload); err != nil {
		return nil, authError("codex_auth_refresh_failed", false, "Codex token refresh returned invalid JSON.")
	}
	if tokenString(payload, "access_token") == "" {
		return nil, authError("codex_auth_refresh_failed", false, "Codex token refresh returned no access token.")
	}
	return payload, nil
}

func mergeRefreshedTokens(current, refreshed map[string]any) map[string]any {
	merged := cloneAnyMap(current)
	for _, key := range []string{"access_token", "id_token", "refresh_token", "token_type", "scope"} {
		value := tokenString(refreshed, key)
		if value != "" {
			merged[key] = value
		}
	}
	if expiresAt := refreshedExpiresAt(refreshed, merged); !expiresAt.IsZero() {
		merged["expires_at"] = expiresAt.Unix()
	}
	if accountID := accountIDFromTokens(merged); accountID != "" {
		merged["account_id"] = accountID
	}
	return merged
}

func refreshedExpiresAt(refreshed, tokens map[string]any) time.Time {
	if expiresAt := expiresAtFromTokens(refreshed); !expiresAt.IsZero() {
		return expiresAt
	}
	if expiresIn, ok := numberValue(refreshed["expires_in"]); ok && expiresIn > 0 {
		return time.Now().UTC().Add(time.Duration(expiresIn) * time.Second)
	}
	return expiresAtFromTokens(tokens)
}

func freshPayloadFromConcurrentRefresh(path string) (authPayload, bool) {
	payload, err := loadPayload(path)
	if err != nil {
		return nil, false
	}
	tokens, err := tokensFromPayload(payload)
	if err != nil || tokensNeedRefresh(tokens) {
		return nil, false
	}
	return payload, true
}

func savePayload(path string, payload authPayload) error {
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".codex-auth-*.tmp")
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

func tokensNeedRefresh(tokens map[string]any) bool {
	expiresAt := expiresAtFromTokens(tokens)
	if expiresAt.IsZero() {
		return false
	}
	if !time.Now().Before(expiresAt) {
		return true
	}
	return tokenString(tokens, "refresh_token") != "" && time.Until(expiresAt) <= accessTokenRefreshSkew
}

func expiredWithoutRefresh(tokens map[string]any) bool {
	return tokenExpired(tokens) && tokenString(tokens, "refresh_token") == ""
}

func tokenExpired(tokens map[string]any) bool {
	expiresAt := expiresAtFromTokens(tokens)
	return !expiresAt.IsZero() && !time.Now().Before(expiresAt)
}

func expiresAtFromTokens(tokens map[string]any) time.Time {
	for _, key := range []string{"expires_at", "expiresAt"} {
		if value, ok := unixTimeValue(tokens[key]); ok {
			return value
		}
	}
	for _, key := range []string{"access_token", "id_token"} {
		claims := jwtPayload(tokenString(tokens, key))
		if value, ok := unixTimeValue(claims["exp"]); ok {
			return value
		}
	}
	return time.Time{}
}

func accountIDFromTokens(tokens map[string]any) string {
	for _, key := range []string{"account_id", "accountId"} {
		if value := tokenString(tokens, key); value != "" {
			return value
		}
	}
	for _, tokenKey := range []string{"id_token", "access_token"} {
		claims := jwtPayload(tokenString(tokens, tokenKey))
		for _, key := range []string{"account_id", "accountId", "https://api.openai.com/auth/account_id"} {
			if value := tokenString(claims, key); value != "" {
				return value
			}
		}
		if nested, ok := claims["https://api.openai.com/auth"].(map[string]any); ok {
			for _, key := range []string{"chatgpt_account_id", "account_id", "accountId"} {
				if value := tokenString(nested, key); value != "" {
					return value
				}
			}
		}
	}
	return ""
}

func jwtPayload(token string) map[string]any {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return nil
	}
	payload := parts[1]
	for len(payload)%4 != 0 {
		payload += "="
	}
	data, err := base64.URLEncoding.DecodeString(payload)
	if err != nil {
		data, err = base64.RawURLEncoding.DecodeString(parts[1])
		if err != nil {
			return nil
		}
	}
	var claims map[string]any
	if err := json.Unmarshal(data, &claims); err != nil {
		return nil
	}
	return claims
}

func tokenString(values map[string]any, key string) string {
	if values == nil {
		return ""
	}
	value, ok := values[key]
	if !ok || value == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(value))
}

func unixTimeValue(value any) (time.Time, bool) {
	if value == nil {
		return time.Time{}, false
	}
	seconds, ok := numberValue(value)
	if !ok || seconds <= 0 {
		if text := strings.TrimSpace(fmt.Sprint(value)); text != "" {
			if parsed, err := time.Parse(time.RFC3339, text); err == nil {
				return parsed.UTC(), true
			}
		}
		return time.Time{}, false
	}
	return time.Unix(int64(seconds), 0).UTC(), true
}

func numberValue(value any) (float64, bool) {
	switch typed := value.(type) {
	case float64:
		return typed, true
	case float32:
		return float64(typed), true
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case json.Number:
		parsed, err := typed.Float64()
		return parsed, err == nil
	case string:
		parsed, err := strconv.ParseFloat(strings.TrimSpace(typed), 64)
		return parsed, err == nil
	default:
		return 0, false
	}
}

func allowsAuthPathFallback(err error) bool {
	var authErr *AuthError
	if errors.As(err, &authErr) {
		switch authErr.Code {
		case "codex_auth_missing", "codex_auth_expired", "codex_auth_not_refreshable", "codex_auth_refresh_failed", "codex_auth_invalid":
			return true
		}
	}
	return false
}

func refreshErrorMessage(resp *http.Response) string {
	var payload map[string]any
	if err := json.NewDecoder(io.LimitReader(resp.Body, 32<<10)).Decode(&payload); err == nil {
		code := strings.TrimSpace(fmt.Sprint(payload["error"]))
		description := strings.TrimSpace(firstNonEmpty(fmt.Sprint(payload["error_description"]), fmt.Sprint(payload["message"])))
		if detail := strings.Trim(strings.Join([]string{code, description}, ": "), ": "); detail != "" {
			return fmt.Sprintf("Codex token refresh failed: HTTP %d: %s", resp.StatusCode, detail)
		}
	}
	return fmt.Sprintf("Codex token refresh failed: HTTP %d", resp.StatusCode)
}

func authError(code string, reauth bool, message string) *AuthError {
	return &AuthError{Code: code, ReloginRequired: reauth, Message: message}
}

func safeStatusError(err error) string {
	if err == nil {
		return ""
	}
	var authErr *AuthError
	if errors.As(err, &authErr) {
		return authErr.Message
	}
	return strings.ReplaceAll(err.Error(), "\n", " ")
}

func canonicalPathKey(path string) string {
	absolute, err := filepath.Abs(path)
	if err != nil {
		absolute = path
	}
	if runtime.GOOS == "windows" {
		return strings.ToLower(absolute)
	}
	return absolute
}

func expandHome(path string) string {
	path = strings.TrimSpace(path)
	if path == "" || path == "~" || !strings.HasPrefix(path, "~"+string(filepath.Separator)) {
		if path == "~" {
			home, err := os.UserHomeDir()
			if err == nil {
				return home
			}
		}
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	return filepath.Join(home, strings.TrimPrefix(path, "~"+string(filepath.Separator)))
}

func mustGetwd() string {
	wd, err := os.Getwd()
	if err != nil {
		return "."
	}
	return wd
}

func clonePayload(payload authPayload) authPayload {
	out := make(authPayload, len(payload))
	for key, value := range payload {
		out[key] = value
	}
	return out
}

func cloneAnyMap(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" && value != "<nil>" {
			return value
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
