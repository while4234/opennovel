package bootstrap

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const defaultProviderConnectivityTimeout = 15 * time.Second

var directProxyDefaultTemplates = map[string]struct{}{
	"deepseek": {},
	"doubao":   {},
}

func ProviderUsesProxy(providerName, model string, pc ProviderConfig) bool {
	if pc.UseProxy != nil {
		return *pc.UseProxy
	}
	if providerDefaultsToDirectProxy(providerName, model, pc) {
		return false
	}
	return true
}

func ProviderRequestTimeout(pc ProviderConfig) time.Duration {
	if pc.RequestTimeoutSeconds <= 0 {
		return 0
	}
	return time.Duration(pc.RequestTimeoutSeconds) * time.Second
}

func ProviderConnectivityTimeout(pc ProviderConfig) time.Duration {
	if pc.ConnectivityTimeoutSeconds <= 0 {
		return defaultProviderConnectivityTimeout
	}
	return time.Duration(pc.ConnectivityTimeoutSeconds) * time.Second
}

func ProviderTransport(cfg Config, providerName, model string, pc ProviderConfig) (http.RoundTripper, bool, error) {
	useProxy := ProviderUsesProxy(providerName, model, pc)
	if !useProxy {
		transport := cloneDefaultHTTPTransport()
		transport.Proxy = nil
		return transport, false, nil
	}

	proxyURL, err := normalizeProxyURL(cfg.Proxy)
	if err != nil {
		return nil, true, err
	}
	if proxyURL == nil {
		return nil, true, nil
	}

	transport := cloneDefaultHTTPTransport()
	transport.Proxy = http.ProxyURL(proxyURL)
	return transport, true, nil
}

func normalizeProxyURL(raw string) (*url.URL, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return nil, nil
	}
	if !strings.Contains(value, "://") {
		value = "http://" + value
	}
	parsed, err := url.Parse(value)
	if err != nil {
		return nil, fmt.Errorf("proxy url: %w", err)
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https":
	default:
		return nil, fmt.Errorf("proxy url scheme must be http or https")
	}
	if parsed.Host == "" {
		return nil, fmt.Errorf("proxy url host is required")
	}
	return parsed, nil
}

func cloneDefaultHTTPTransport() *http.Transport {
	if base, ok := http.DefaultTransport.(*http.Transport); ok {
		return base.Clone()
	}
	return &http.Transport{Proxy: http.ProxyFromEnvironment}
}

func providerDefaultsToDirectProxy(providerName, model string, pc ProviderConfig) bool {
	template := strings.ToLower(strings.TrimSpace(pc.TemplateProvider))
	if _, ok := directProxyDefaultTemplates[template]; ok {
		return true
	}
	name := strings.ToLower(strings.TrimSpace(providerName))
	if _, ok := directProxyDefaultTemplates[name]; ok {
		return true
	}
	model = strings.ToLower(strings.TrimSpace(model))
	for prefix := range directProxyDefaultTemplates {
		if strings.HasPrefix(model, prefix) {
			return true
		}
	}
	baseURL := strings.ToLower(strings.TrimSpace(pc.BaseURL))
	return strings.Contains(baseURL, "api.deepseek.com")
}
