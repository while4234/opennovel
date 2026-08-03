package retrypolicy

import (
	"errors"
	"strings"
	"testing"

	"github.com/voocel/litellm"
)

func TestProviderGatewayErrorDetectsHTTPStatus(t *testing.T) {
	err := litellm.NewHTTPError("deepseek", 502, "<html><body>502 Bad Gateway</body></html>")

	if !IsProviderGatewayError(err) {
		t.Fatal("IsProviderGatewayError = false, want true")
	}
	got := SanitizeProviderError(err)
	if got != "provider gateway error: 502 Bad Gateway" {
		t.Fatalf("sanitized error = %q", got)
	}
}

func TestProviderGatewayErrorDetectsHTMLMessage(t *testing.T) {
	err := errors.New("<html><body>503 Service Unavailable</body></html>")

	if !IsProviderGatewayError(err) {
		t.Fatal("IsProviderGatewayError = false, want true")
	}
	got := SanitizeProviderError(err)
	if strings.Contains(strings.ToLower(got), "<html") {
		t.Fatalf("sanitized error leaked html: %q", got)
	}
}
