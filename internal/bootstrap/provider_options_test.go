package bootstrap

import "testing"

func TestProviderUsesProxyDefaultsAndExplicitOverrides(t *testing.T) {
	trueValue := true
	falseValue := false
	cases := []struct {
		name     string
		provider string
		model    string
		config   ProviderConfig
		want     bool
	}{
		{name: "codex default proxy", provider: "codex", model: "gpt-5.1-codex", want: true},
		{name: "grok default proxy", provider: "grok", model: "grok-4.3-latest", want: true},
		{name: "deepseek default direct", provider: "deepseek", model: "deepseek-chat", want: false},
		{name: "deepseek template direct", provider: "custom-deepseek", model: "deepseek-chat", config: ProviderConfig{TemplateProvider: "deepseek"}, want: false},
		{name: "explicit false wins", provider: "codex", model: "gpt-5.1-codex", config: ProviderConfig{UseProxy: &falseValue}, want: false},
		{name: "explicit true wins", provider: "deepseek", model: "deepseek-chat", config: ProviderConfig{UseProxy: &trueValue}, want: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ProviderUsesProxy(tc.provider, tc.model, tc.config); got != tc.want {
				t.Fatalf("ProviderUsesProxy() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestProviderTransportNormalizesConfiguredProxy(t *testing.T) {
	cfg := Config{Proxy: "127.0.0.1:7897"}
	transport, handled, err := ProviderTransport(cfg, "codex", "gpt-5.1-codex", ProviderConfig{})
	if err != nil {
		t.Fatalf("ProviderTransport: %v", err)
	}
	if !handled || transport == nil {
		t.Fatalf("ProviderTransport handled=%v transport=%v, want configured transport", handled, transport)
	}
}
