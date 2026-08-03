package host

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/internal/bootstrap"
)

func TestAddProviderModelRegistersAndPersists(t *testing.T) {
	withModelConnectivityProbe(t, nil)

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	cfg := bootstrap.Config{
		Provider:  "openai",
		ModelName: "gpt-base",
		Providers: map[string]bootstrap.ProviderConfig{
			"openai": {Type: "openai", APIKey: "old-key", Models: []string{"gpt-base"}},
		},
	}
	cfg.FillDefaults()
	models, err := bootstrap.NewModelSet(cfg)
	if err != nil {
		t.Fatalf("NewModelSet: %v", err)
	}
	host := &Host{
		cfg:    cfg,
		models: models,
		events: make(chan Event, 10),
	}

	err = host.AddProviderModel("writer", "proxy-openai", bootstrap.ProviderConfig{
		Type:    "openai",
		APIKey:  "proxy-key",
		BaseURL: "https://proxy.example/v1",
	}, "proxy-model")
	if err != nil {
		t.Fatalf("AddProviderModel: %v", err)
	}

	provider, model, explicit := host.models.CurrentSelection("writer")
	if !explicit || provider != "proxy-openai" || model != "proxy-model" {
		t.Fatalf("writer selection = %s/%s explicit=%v", provider, model, explicit)
	}
	if got := host.cfg.CandidateModels("proxy-openai"); len(got) != 1 || got[0] != "proxy-model" {
		t.Fatalf("proxy candidates = %#v", got)
	}

	saved, err := bootstrap.LoadConfigFile(filepath.Join(home, ".ainovel", "config.json"))
	if err != nil {
		t.Fatalf("LoadConfigFile: %v", err)
	}
	if saved.Roles["writer"].Provider != "proxy-openai" || saved.Roles["writer"].Model != "proxy-model" {
		t.Fatalf("saved writer role = %#v", saved.Roles["writer"])
	}
	if saved.Providers["proxy-openai"].APIKey != "proxy-key" {
		t.Fatalf("saved provider = %#v", saved.Providers["proxy-openai"])
	}
}

func TestProjectStageModelOverridesAgentAndCanInheritAgain(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	cfg := bootstrap.Config{
		Provider:  "deepseek",
		ModelName: "deepseek-v4-pro",
		Providers: map[string]bootstrap.ProviderConfig{
			"deepseek": {Type: "openai", APIKey: "deepseek-key", Models: []string{"deepseek-v4-pro"}},
			"grok":     {Type: "openai", APIKey: "grok-key", Models: []string{"grok-4.5"}},
		},
		Roles: map[string]bootstrap.RoleConfig{
			"writer": {Provider: "deepseek", Model: "deepseek-v4-pro"},
		},
	}
	cfg.FillDefaults()
	models, err := bootstrap.NewModelSet(cfg)
	if err != nil {
		t.Fatalf("NewModelSet: %v", err)
	}
	host := &Host{cfg: cfg, models: models, events: make(chan Event, 10)}
	stageKey := bootstrap.StageRouteKey(bootstrap.StageWriting)

	provider, model, explicit := host.CurrentModelSelection(stageKey)
	if explicit || provider != "deepseek" || model != "deepseek-v4-pro" {
		t.Fatalf("inherited stage = %s/%s explicit=%v", provider, model, explicit)
	}
	if err := host.SwitchModel(stageKey, "grok", "grok-4.5"); err != nil {
		t.Fatalf("SwitchModel(stage): %v", err)
	}
	provider, model, explicit = host.CurrentModelSelection(stageKey)
	if !explicit || provider != "grok" || model != "grok-4.5" {
		t.Fatalf("overridden stage = %s/%s explicit=%v", provider, model, explicit)
	}
	writerProvider, writerModel, _ := host.CurrentModelSelection("writer")
	if writerProvider != "deepseek" || writerModel != "deepseek-v4-pro" {
		t.Fatalf("writer route changed with stage: %s/%s", writerProvider, writerModel)
	}
	if err := host.ClearModelRoute(stageKey); err != nil {
		t.Fatalf("ClearModelRoute(stage): %v", err)
	}
	provider, model, explicit = host.CurrentModelSelection(stageKey)
	if explicit || provider != "deepseek" || model != "deepseek-v4-pro" {
		t.Fatalf("restored inherited stage = %s/%s explicit=%v", provider, model, explicit)
	}
}

func TestReportAdaptationFailoverPromotesTargetToAllAgents(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	cfg := bootstrap.Config{
		Provider:  "primary",
		ModelName: "model-a",
		Providers: map[string]bootstrap.ProviderConfig{
			"primary":  {Type: "openai", APIKey: "primary-key", Models: []string{"model-a"}},
			"fallback": {Type: "openai", APIKey: "fallback-key", Models: []string{"model-b"}},
		},
		Roles: map[string]bootstrap.RoleConfig{
			"architect":   {Provider: "primary", Model: "model-a"},
			"coordinator": {Provider: "primary", Model: "model-a"},
			"editor":      {Provider: "primary", Model: "model-a"},
			"writer":      {Provider: "primary", Model: "model-a"},
		},
	}
	cfg.FillDefaults()
	models, err := bootstrap.NewModelSet(cfg)
	if err != nil {
		t.Fatalf("NewModelSet: %v", err)
	}
	host := &Host{cfg: cfg, models: models, events: make(chan Event, 10)}

	host.reportAdaptationFailover(bootstrap.FailoverEvent{
		Role:         "architect",
		Reason:       "rate_limit",
		FromProvider: "primary",
		FromModel:    "model-a",
		ToProvider:   "fallback",
		ToModel:      "model-b",
		Err:          errors.New("rate_limit_exceeded"),
	})

	for _, role := range append([]string{"default"}, projectAgentModelRoles...) {
		provider, model, _ := host.models.CurrentSelection(role)
		if provider != "fallback" || model != "model-b" {
			t.Fatalf("%s selection = %s/%s, want fallback/model-b", role, provider, model)
		}
	}
	saved, err := bootstrap.LoadConfigFile(filepath.Join(home, ".ainovel", "config.json"))
	if err != nil {
		t.Fatalf("LoadConfigFile: %v", err)
	}
	if saved.Provider != "fallback" || saved.ModelName != "model-b" {
		t.Fatalf("saved default route = %s/%s, want fallback/model-b", saved.Provider, saved.ModelName)
	}
	for _, role := range projectAgentModelRoles {
		route := saved.Roles[role]
		if route.Provider != "fallback" || route.Model != "model-b" {
			t.Fatalf("saved %s route = %s/%s, want fallback/model-b", role, route.Provider, route.Model)
		}
	}
}

func TestSelectRuntimeFallbackKeepsModelAndDoesNotRewriteRoutes(t *testing.T) {
	withModelConnectivityProbe(t, nil)

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	enabled := true
	cfg := bootstrap.Config{
		Provider:              "primary",
		ModelName:             "model-a",
		PersistPath:           filepath.Join(home, "project-config.json"),
		PersistProjectOverlay: true,
		Providers: map[string]bootstrap.ProviderConfig{
			"primary":  {Type: "openai", APIKey: "primary-key", Models: []string{"model-a"}},
			"fallback": {Type: "openai", APIKey: "fallback-key", Models: []string{"model-b", "model-a"}},
		},
		Roles: map[string]bootstrap.RoleConfig{
			"architect":   {Provider: "primary", Model: "model-a"},
			"coordinator": {Provider: "primary", Model: "model-a"},
			"editor":      {Provider: "primary", Model: "model-a"},
			"writer":      {Provider: "primary", Model: "model-a"},
		},
		ModelAutoSwitch: bootstrap.ModelAutoSwitchConfig{
			Enabled:          &enabled,
			FallbackBackends: []string{"fallback"},
		},
	}
	cfg.FillDefaults()
	models, err := bootstrap.NewModelSet(cfg)
	if err != nil {
		t.Fatalf("NewModelSet: %v", err)
	}
	host := &Host{cfg: cfg, models: models, events: make(chan Event, 10)}

	target, ok := host.selectRuntimeFallback(
		context.Background(),
		bootstrap.ModelRef{Provider: "primary", Model: "model-a"},
		map[string]bool{"primary": true},
		errors.New("rate_limit_exceeded"),
	)
	if !ok {
		t.Fatal("selectRuntimeFallback did not find fallback target")
	}
	if target.Provider != "fallback" || target.Model != "model-a" {
		t.Fatalf("target = %s/%s, want fallback/model-a", target.Provider, target.Model)
	}
	for _, role := range append([]string{"default"}, projectAgentModelRoles...) {
		provider, model, _ := host.models.CurrentSelection(role)
		if provider != "primary" || model != "model-a" {
			t.Fatalf("%s selection changed during candidate selection: %s/%s", role, provider, model)
		}
	}
	if _, err := os.Stat(cfg.PersistPath); !os.IsNotExist(err) {
		t.Fatalf("candidate selection should not persist route changes, stat err=%v", err)
	}
}

func TestSelectRuntimeFallbackRejectsDifferentModel(t *testing.T) {
	withModelConnectivityProbe(t, nil)
	enabled := true
	cfg := bootstrap.Config{
		Provider:  "grok",
		ModelName: "grok-4.5",
		Providers: map[string]bootstrap.ProviderConfig{
			"grok":     {Type: "openai", APIKey: "grok-key", Models: []string{"grok-4.5"}},
			"deepseek": {Type: "openai", APIKey: "deepseek-key", Models: []string{"deepseek-v4-pro"}},
		},
		ModelAutoSwitch: bootstrap.ModelAutoSwitchConfig{
			Enabled:          &enabled,
			FallbackBackends: []string{"deepseek"},
		},
	}
	cfg.FillDefaults()
	models, err := bootstrap.NewModelSet(cfg)
	if err != nil {
		t.Fatalf("NewModelSet: %v", err)
	}
	host := &Host{cfg: cfg, models: models, events: make(chan Event, 10)}

	if target, ok := host.selectRuntimeFallback(
		context.Background(),
		bootstrap.ModelRef{Provider: "grok", Model: "grok-4.5"},
		map[string]bool{"grok": true},
		errors.New("rate_limit_exceeded"),
	); ok {
		t.Fatalf("cross-model fallback selected: %s/%s", target.Provider, target.Model)
	}
}

func TestSelectRuntimeFallbackDoesNotSilentlyPreflightCandidate(t *testing.T) {
	withModelConnectivityProbe(t, errors.New("runtime candidate probe must not run"))
	enabled := true
	cfg := bootstrap.Config{
		Provider:              "deepseek-yuanyu-0",
		ModelName:             "deepseek-v4-pro",
		PersistProjectOverlay: true,
		Providers: map[string]bootstrap.ProviderConfig{
			"deepseek-yuanyu-0": {Type: "openai", APIKey: "primary-key", Models: []string{"deepseek-v4-pro"}},
			"deepseek-suifeng":  {Type: "openai", APIKey: "fallback-key", Models: []string{"deepseek-v4-pro"}},
		},
		ModelAutoSwitch: bootstrap.ModelAutoSwitchConfig{
			Enabled:          &enabled,
			FallbackBackends: []string{"deepseek-yuanyu-0", "deepseek-suifeng"},
		},
	}
	cfg.FillDefaults()
	models, err := bootstrap.NewModelSet(cfg)
	if err != nil {
		t.Fatalf("NewModelSet: %v", err)
	}
	host := &Host{cfg: cfg, models: models, events: make(chan Event, 10)}

	target, ok := host.selectRuntimeFallback(
		context.Background(),
		bootstrap.ModelRef{Provider: "deepseek-yuanyu-0", Model: "deepseek-v4-pro"},
		map[string]bool{"deepseek-yuanyu-0": true},
		errors.New("insufficient_user_quota"),
	)
	if !ok {
		t.Fatal("selectRuntimeFallback silently rejected the configured candidate")
	}
	if target.Provider != "deepseek-suifeng" || target.Model != "deepseek-v4-pro" {
		t.Fatalf("target = %s/%s, want deepseek-suifeng/deepseek-v4-pro", target.Provider, target.Model)
	}
}

func TestAddProviderModelDoesNotOverwriteExistingProvider(t *testing.T) {
	cfg := bootstrap.Config{
		Provider:  "openai",
		ModelName: "gpt-base",
		Providers: map[string]bootstrap.ProviderConfig{
			"openai": {Type: "openai", APIKey: "old-key", Models: []string{"gpt-base"}},
		},
	}
	cfg.FillDefaults()
	models, err := bootstrap.NewModelSet(cfg)
	if err != nil {
		t.Fatalf("NewModelSet: %v", err)
	}
	host := &Host{cfg: cfg, models: models, events: make(chan Event, 10)}

	err = host.AddProviderModel("default", "openai", bootstrap.ProviderConfig{
		Type:   "openai",
		APIKey: "different-key",
	}, "gpt-new")
	if err == nil {
		t.Fatal("expected different existing provider config to be rejected")
	}
	if got := host.cfg.Providers["openai"].APIKey; got != "old-key" {
		t.Fatalf("existing provider was overwritten: %q", got)
	}
}

func TestAddProviderModelRejectsFailedConnectivityProbe(t *testing.T) {
	probeErr := errors.New("probe rejected")
	withModelConnectivityProbe(t, probeErr)

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	cfg := bootstrap.Config{
		Provider:  "openai",
		ModelName: "gpt-base",
		Providers: map[string]bootstrap.ProviderConfig{
			"openai": {Type: "openai", APIKey: "old-key", Models: []string{"gpt-base"}},
		},
	}
	cfg.FillDefaults()
	models, err := bootstrap.NewModelSet(cfg)
	if err != nil {
		t.Fatalf("NewModelSet: %v", err)
	}
	host := &Host{
		cfg:    cfg,
		models: models,
		events: make(chan Event, 10),
	}

	err = host.AddProviderModel("writer", "proxy-openai", bootstrap.ProviderConfig{
		Type:    "openai",
		APIKey:  "proxy-key",
		BaseURL: "https://proxy.example/v1",
	}, "proxy-model")
	if err == nil || !strings.Contains(err.Error(), "probe rejected") {
		t.Fatalf("AddProviderModel error = %v, want probe rejection", err)
	}
	if _, ok := host.cfg.Providers["proxy-openai"]; ok {
		t.Fatal("failed probe should not register provider")
	}
	provider, model, explicit := host.models.CurrentSelection("writer")
	if explicit || provider != "openai" || model != "gpt-base" {
		t.Fatalf("writer selection changed after failed probe: %s/%s explicit=%v", provider, model, explicit)
	}
	if _, err := bootstrap.LoadConfigFile(filepath.Join(home, ".ainovel", "config.json")); err == nil {
		t.Fatal("failed probe should not persist config")
	}
}

func TestSwitchDefaultModelPreservesAgentOverrides(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	cfg := bootstrap.Config{
		Provider:  "openai",
		ModelName: "gpt-base",
		Providers: map[string]bootstrap.ProviderConfig{
			"openai": {Type: "openai", APIKey: "sk-test", Models: []string{"gpt-base", "gpt-next"}},
		},
		Roles: map[string]bootstrap.RoleConfig{
			"writer": {Provider: "openai", Model: "gpt-base"},
		},
	}
	cfg.FillDefaults()
	models, err := bootstrap.NewModelSet(cfg)
	if err != nil {
		t.Fatalf("NewModelSet: %v", err)
	}
	host := &Host{cfg: cfg, models: models, events: make(chan Event, 10)}

	if err := host.SwitchModel("default", "openai", "gpt-next"); err != nil {
		t.Fatalf("SwitchModel: %v", err)
	}
	provider, model, explicit := host.models.CurrentSelection("default")
	if provider != "openai" || model != "gpt-next" || !explicit {
		t.Fatalf("default selection = %s/%s explicit=%v", provider, model, explicit)
	}
	provider, model, explicit = host.models.CurrentSelection("writer")
	if provider != "openai" || model != "gpt-base" || !explicit {
		t.Fatalf("writer selection = %s/%s explicit=%v", provider, model, explicit)
	}
	if rc := host.cfg.Roles["writer"]; rc.Provider != "openai" || rc.Model != "gpt-base" {
		t.Fatalf("cfg writer role = %+v", rc)
	}
	provider, model, explicit = host.models.CurrentSelection("editor")
	if provider != "openai" || model != "gpt-next" || explicit {
		t.Fatalf("editor selection = %s/%s explicit=%v", provider, model, explicit)
	}
}

func TestSelectProviderModelInConfigDefaultPreservesAgentRoutes(t *testing.T) {
	cfg := bootstrap.Config{
		Provider:  "openai",
		ModelName: "gpt-base",
		Providers: map[string]bootstrap.ProviderConfig{
			"openai": {Type: "openai", APIKey: "sk-test", Models: []string{"gpt-base", "gpt-next"}},
		},
		Roles: map[string]bootstrap.RoleConfig{
			"writer": {Provider: "openai", Model: "gpt-base"},
		},
	}
	cfg.FillDefaults()

	next, err := SelectProviderModelInConfig(cfg, "default", "openai", "gpt-next")
	if err != nil {
		t.Fatalf("SelectProviderModelInConfig: %v", err)
	}
	if next.Provider != "openai" || next.ModelName != "gpt-next" {
		t.Fatalf("default route = %s/%s", next.Provider, next.ModelName)
	}
	if rc := next.Roles["writer"]; rc.Provider != "openai" || rc.Model != "gpt-base" {
		t.Fatalf("writer role = %+v", rc)
	}
	if _, ok := next.Roles["editor"]; ok {
		t.Fatalf("editor should still inherit default: %+v", next.Roles["editor"])
	}
}

func TestClearModelRouteFallsBackToProjectDefault(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	cfg := bootstrap.Config{
		Provider:              "openai",
		ModelName:             "gpt-base",
		PersistProjectOverlay: true,
		Providers: map[string]bootstrap.ProviderConfig{
			"openai": {Type: "openai", APIKey: "sk-openai", Models: []string{"gpt-base"}},
			"proxy":  {Type: "openai", APIKey: "sk-proxy", Models: []string{"proxy-model"}},
		},
		Roles: map[string]bootstrap.RoleConfig{
			"writer": {Provider: "proxy", Model: "proxy-model", ReasoningEffort: "high"},
		},
	}
	cfg.FillDefaults()
	cfg.PersistProjectConfig = &bootstrap.Config{
		Roles: map[string]bootstrap.RoleConfig{
			"writer": {Provider: "proxy", Model: "proxy-model", ReasoningEffort: "high"},
		},
	}
	models, err := bootstrap.NewModelSet(cfg)
	if err != nil {
		t.Fatalf("NewModelSet: %v", err)
	}
	host := &Host{
		cfg:    cfg,
		models: models,
		events: make(chan Event, 10),
	}

	if err := host.ClearModelRoute("writer"); err != nil {
		t.Fatalf("ClearModelRoute: %v", err)
	}
	provider, model, explicit := host.models.CurrentSelection("writer")
	if explicit || provider != "openai" || model != "gpt-base" {
		t.Fatalf("writer selection = %s/%s explicit=%v", provider, model, explicit)
	}
	if rc := host.cfg.Roles["writer"]; rc.Provider != "" || rc.Model != "" || rc.ReasoningEffort != "high" {
		t.Fatalf("writer role = %+v", rc)
	}
	if rc := host.cfg.PersistProjectConfig.Roles["writer"]; rc.Provider != "" || rc.Model != "" || rc.ReasoningEffort != "high" {
		t.Fatalf("overlay writer role = %+v", rc)
	}
}

func TestConfigureProviderModelRenamesAndPreservesBlankAPIKeyWithoutSelecting(t *testing.T) {
	withModelConnectivityProbe(t, nil)
	enabled := true
	cfg := bootstrap.Config{
		Provider:  "custom-openai",
		ModelName: "old-model",
		Providers: map[string]bootstrap.ProviderConfig{
			"custom-openai": {
				Label:   "Wrong",
				Type:    "openai",
				API:     "chat",
				APIKey:  "sk-old",
				BaseURL: "https://old.example/v1",
				Models:  []string{"old-model"},
			},
		},
		Roles: map[string]bootstrap.RoleConfig{
			"writer": {Provider: "custom-openai", Model: "old-model"},
			"editor": {
				Provider:  "custom-openai",
				Model:     "old-model",
				Fallbacks: []bootstrap.ModelRef{{Provider: "custom-openai", Model: "old-model"}},
			},
		},
		ModelAutoSwitch: bootstrap.ModelAutoSwitchConfig{
			Enabled:            &enabled,
			FallbackBackends:   []string{"custom-openai"},
			NetworkMaxAttempts: 3,
		},
	}
	cfg.FillDefaults()

	next, err := ConfigureProviderModelInConfig(context.Background(), cfg, ProviderModelUpdate{
		Role:             "default",
		OriginalProvider: "custom-openai",
		Provider:         "fixed-openai",
		Model:            "new-model",
		ProviderConfig: bootstrap.ProviderConfig{
			Label:   "Fixed",
			Type:    "openai",
			API:     "responses",
			BaseURL: "https://new.example/v1",
		},
		NetworkMaxAttempts:      4,
		AutoSwitchCandidatePool: true,
	})
	if err != nil {
		t.Fatalf("ConfigureProviderModelInConfig: %v", err)
	}
	if _, ok := next.Providers["custom-openai"]; ok {
		t.Fatal("old provider key still exists")
	}
	pc := next.Providers["fixed-openai"]
	if pc.APIKey != "sk-old" || pc.Label != "Fixed" || pc.API != "responses" || pc.BaseURL != "https://new.example/v1" {
		t.Fatalf("renamed provider config = %+v", pc)
	}
	if !reflect.DeepEqual(next.ModelAutoSwitch.FallbackBackends, []string{"fixed-openai"}) || next.ModelAutoSwitch.EffectiveNetworkMaxAttempts() != 4 {
		t.Fatalf("auto switch = %+v", next.ModelAutoSwitch)
	}
	if fallback := next.Roles["editor"].Fallbacks[0]; fallback.Provider != "fixed-openai" || fallback.Model != "old-model" {
		t.Fatalf("editor fallback = %+v", fallback)
	}
	if next.Provider != "fixed-openai" || next.ModelName != "old-model" {
		t.Fatalf("default route = %s/%s, want fixed-openai/old-model", next.Provider, next.ModelName)
	}
	if rc := next.Roles["writer"]; rc.Provider != "fixed-openai" || rc.Model != "old-model" {
		t.Fatalf("writer route changed: %+v", rc)
	}
}

func TestConfigureProviderModelExplicitSelectUpdatesDefaultRoute(t *testing.T) {
	withModelConnectivityProbe(t, nil)
	selectAfterSave := true
	cfg := bootstrap.Config{
		Provider:  "custom-openai",
		ModelName: "old-model",
		Providers: map[string]bootstrap.ProviderConfig{
			"custom-openai": {
				Type:    "openai",
				API:     "chat",
				APIKey:  "sk-old",
				BaseURL: "https://old.example/v1",
				Models:  []string{"old-model"},
			},
		},
		Roles: map[string]bootstrap.RoleConfig{
			"writer": {Provider: "custom-openai", Model: "old-model"},
		},
	}
	cfg.FillDefaults()

	next, err := ConfigureProviderModelInConfig(context.Background(), cfg, ProviderModelUpdate{
		Role:             "default",
		OriginalProvider: "custom-openai",
		Provider:         "fixed-openai",
		Model:            "new-model",
		ProviderConfig: bootstrap.ProviderConfig{
			Type:    "openai",
			API:     "responses",
			BaseURL: "https://new.example/v1",
		},
		SelectAfterSave: &selectAfterSave,
	})
	if err != nil {
		t.Fatalf("ConfigureProviderModelInConfig with select: %v", err)
	}
	if next.Provider != "fixed-openai" || next.ModelName != "new-model" {
		t.Fatalf("default route = %s/%s", next.Provider, next.ModelName)
	}
	if rc := next.Roles["writer"]; rc.Provider != "fixed-openai" || rc.Model != "old-model" {
		t.Fatalf("writer route changed: %+v", rc)
	}
}

func TestConfigureProviderModelRejectsFailedProbeWithoutMutating(t *testing.T) {
	withModelConnectivityProbe(t, errors.New("probe rejected"))
	cfg := bootstrap.Config{
		Provider:  "custom-openai",
		ModelName: "old-model",
		Providers: map[string]bootstrap.ProviderConfig{
			"custom-openai": {Type: "openai", APIKey: "sk-old", Models: []string{"old-model"}},
		},
	}
	cfg.FillDefaults()

	_, err := ConfigureProviderModelInConfig(context.Background(), cfg, ProviderModelUpdate{
		Role:             "default",
		OriginalProvider: "custom-openai",
		Provider:         "fixed-openai",
		Model:            "new-model",
		ProviderConfig:   bootstrap.ProviderConfig{Type: "openai"},
	})
	if err == nil || !strings.Contains(err.Error(), "probe rejected") {
		t.Fatalf("ConfigureProviderModelInConfig error = %v, want probe rejection", err)
	}
	if _, ok := cfg.Providers["fixed-openai"]; ok {
		t.Fatal("input config mutated after failed probe")
	}
	if cfg.Providers["custom-openai"].APIKey != "sk-old" {
		t.Fatalf("input provider changed: %+v", cfg.Providers["custom-openai"])
	}
}

func TestConfigureProviderModelLocalEditSkipsConnectivityProbe(t *testing.T) {
	withModelConnectivityProbe(t, errors.New("probe should not run for local edit"))
	cfg := bootstrap.Config{
		Provider:  "custom-openai",
		ModelName: "deepseek-v4-pro",
		Providers: map[string]bootstrap.ProviderConfig{
			"custom-openai": {
				Label:   "DeepSeek",
				Type:    "openai",
				API:     "chat",
				APIKey:  "sk-old",
				BaseURL: "https://yuanyuicloud.cn/v1",
				Models:  []string{"deepseek-v4-pro"},
			},
		},
	}
	cfg.FillDefaults()

	next, err := ConfigureProviderModelInConfig(context.Background(), cfg, ProviderModelUpdate{
		Role:             "default",
		OriginalProvider: "custom-openai",
		Provider:         "deepseek_yuanyu_0",
		Model:            "deepseek-v4-pro",
		ProviderConfig: bootstrap.ProviderConfig{
			Label:   "deepseek_yuanyu_0",
			Type:    "openai",
			API:     "chat",
			BaseURL: "https://yuanyuicloud.cn/v1",
		},
	})
	if err != nil {
		t.Fatalf("ConfigureProviderModelInConfig local edit: %v", err)
	}
	if _, ok := next.Providers["custom-openai"]; ok {
		t.Fatal("old provider key still exists")
	}
	pc := next.Providers["deepseek_yuanyu_0"]
	if pc.APIKey != "sk-old" || pc.Label != "deepseek_yuanyu_0" {
		t.Fatalf("edited provider config = %+v", pc)
	}
	if next.Provider != "deepseek_yuanyu_0" || next.ModelName != "deepseek-v4-pro" {
		t.Fatalf("default route after rename = %s/%s", next.Provider, next.ModelName)
	}
}

func TestConfigureProviderModelCanSaveWithoutSelectingRoute(t *testing.T) {
	withModelConnectivityProbe(t, nil)
	selectAfterSave := false
	cfg := bootstrap.Config{
		Provider:  "openai",
		ModelName: "gpt-base",
		Providers: map[string]bootstrap.ProviderConfig{
			"openai": {Type: "openai", APIKey: "sk-openai", Models: []string{"gpt-base"}},
		},
		Roles: map[string]bootstrap.RoleConfig{
			"writer": {Provider: "openai", Model: "gpt-base"},
		},
	}
	cfg.FillDefaults()

	next, err := ConfigureProviderModelInConfig(context.Background(), cfg, ProviderModelUpdate{
		Role:     "writer",
		Provider: "deepseek2",
		Model:    "deepseek-v4-pro",
		ProviderConfig: bootstrap.ProviderConfig{
			Label:   "DeepSeek Relay",
			Type:    "openai",
			API:     "chat",
			APIKey:  "sk-deepseek",
			BaseURL: "https://api.example/v1",
		},
		SelectAfterSave: &selectAfterSave,
	})
	if err != nil {
		t.Fatalf("ConfigureProviderModelInConfig without select: %v", err)
	}
	if _, ok := next.Providers["deepseek2"]; !ok {
		t.Fatal("new provider was not saved")
	}
	if next.Provider != "openai" || next.ModelName != "gpt-base" {
		t.Fatalf("default route changed to %s/%s", next.Provider, next.ModelName)
	}
	if rc := next.Roles["writer"]; rc.Provider != "openai" || rc.Model != "gpt-base" {
		t.Fatalf("writer route changed: %+v", rc)
	}
}

func TestConfigureProviderModelNormalizesGrokOAuthOpenAIFields(t *testing.T) {
	withModelConnectivityProbe(t, nil)
	selectAfterSave := false
	cfg := bootstrap.Config{
		Provider:  "openai",
		ModelName: "gpt-base",
		Providers: map[string]bootstrap.ProviderConfig{
			"openai": {Type: "openai", APIKey: "sk-openai", Models: []string{"gpt-base"}},
		},
	}
	cfg.FillDefaults()

	next, err := ConfigureProviderModelInConfig(context.Background(), cfg, ProviderModelUpdate{
		Role:            "default",
		Provider:        "grok-oauth",
		Model:           "grok-4.3-latest",
		SelectAfterSave: &selectAfterSave,
		ProviderConfig: bootstrap.ProviderConfig{
			Label:     "Grok",
			Type:      "grok",
			Auth:      bootstrap.ProviderAuthGrokOAuth,
			AccountID: "default",
			API:       "chat",
			APIKey:    "should-clear",
		},
	})
	if err != nil {
		t.Fatalf("ConfigureProviderModelInConfig grok oauth: %v", err)
	}
	pc := next.Providers["grok-oauth"]
	if pc.Type != "grok" || pc.Auth != bootstrap.ProviderAuthGrokOAuth || pc.AccountID != "default" {
		t.Fatalf("grok provider config = %+v", pc)
	}
	if pc.API != "" || pc.APIKey != "" {
		t.Fatalf("grok_oauth should clear OpenAI api fields: %+v", pc)
	}
	if next.Provider != "openai" || next.ModelName != "gpt-base" {
		t.Fatalf("default route changed to %s/%s", next.Provider, next.ModelName)
	}
}

func TestConfiguredProviderModelProbeAllowsExistingProviderEdit(t *testing.T) {
	withModelConnectivityProbe(t, nil)
	cfg := bootstrap.Config{
		Provider:  "deepseek",
		ModelName: "deepseek-v4-pro",
		Providers: map[string]bootstrap.ProviderConfig{
			"deepseek": {
				Label:   "DeepSeek",
				Type:    "openai",
				API:     "chat",
				APIKey:  "sk-old",
				BaseURL: "https://api.sfkey.cn/v1",
				Models:  []string{"deepseek-v4-pro"},
			},
		},
	}
	cfg.FillDefaults()

	result, err := TestConfiguredProviderModelInConfig(context.Background(), cfg, ProviderModelUpdate{
		Role:             "default",
		OriginalProvider: "deepseek",
		Provider:         "deepseek",
		Model:            "deepseek-v4-pro",
		ProviderConfig: bootstrap.ProviderConfig{
			Label:   "DeepSeek",
			Type:    "openai",
			API:     "chat",
			BaseURL: "https://api.sfkey.cn/v1",
		},
	})
	if err != nil {
		t.Fatalf("TestConfiguredProviderModelInConfig: %v", err)
	}
	if result.Status != "ok" || result.Provider != "deepseek" || result.Model != "deepseek-v4-pro" {
		t.Fatalf("configured model test = %+v", result)
	}
	if cfg.Providers["deepseek"].APIKey != "sk-old" {
		t.Fatalf("input provider changed: %+v", cfg.Providers["deepseek"])
	}
}

func withModelConnectivityProbe(t *testing.T, err error) {
	t.Helper()
	restore := SetAddedModelConnectivityProbeForTest(func(context.Context, agentcore.ChatModel) error {
		return err
	})
	t.Cleanup(restore)
}
