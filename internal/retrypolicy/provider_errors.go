package retrypolicy

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/voocel/litellm"
)

func IsProviderGatewayError(err error) bool {
	if err == nil {
		return false
	}
	var llmErr *litellm.LiteLLMError
	if errors.As(err, &llmErr) && isGatewayStatusCode(llmErr.StatusCode) {
		return true
	}
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "bad gateway") ||
		strings.Contains(msg, "service unavailable") ||
		strings.Contains(msg, "gateway timeout") {
		return true
	}
	for _, status := range []string{"500", "502", "503", "504"} {
		if strings.Contains(msg, "http "+status) ||
			strings.Contains(msg, "status "+status) ||
			strings.Contains(msg, "status="+status) ||
			strings.Contains(msg, "status_code "+status) ||
			strings.Contains(msg, "status_code="+status) {
			return true
		}
		if strings.Contains(msg, "<html") && strings.Contains(msg, status) {
			return true
		}
	}
	return false
}

func SanitizeProviderError(err error) string {
	if err == nil {
		return ""
	}
	var llmErr *litellm.LiteLLMError
	if errors.As(err, &llmErr) && isGatewayStatusCode(llmErr.StatusCode) {
		return fmt.Sprintf("provider gateway error: %d %s", llmErr.StatusCode, http.StatusText(llmErr.StatusCode))
	}
	return SanitizeProviderErrorMessage(err.Error())
}

func SanitizeProviderErrorMessage(message string) string {
	msg := strings.TrimSpace(message)
	if msg == "" {
		return ""
	}
	lower := strings.ToLower(msg)
	if strings.Contains(lower, "bad gateway") || strings.Contains(lower, "502") {
		return "provider gateway error: 502 Bad Gateway"
	}
	if strings.Contains(lower, "service unavailable") || strings.Contains(lower, "503") {
		return "provider gateway error: 503 Service Unavailable"
	}
	if strings.Contains(lower, "gateway timeout") || strings.Contains(lower, "504") {
		return "provider gateway error: 504 Gateway Timeout"
	}
	if strings.Contains(lower, "<html") {
		return "provider returned an HTML error page"
	}
	return msg
}

func isGatewayStatusCode(status int) bool {
	switch status {
	case http.StatusInternalServerError,
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
		http.StatusGatewayTimeout:
		return true
	default:
		return false
	}
}
