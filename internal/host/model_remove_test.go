package host

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/voocel/ainovel-cli/internal/bootstrap"
)

func TestRemoveProviderModelClearsRoleAndPersists(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	cfg := bootstrap.Config{
		Provider:  "openai",
		ModelName: "gpt-base",
		Providers: map[string]bootstrap.ProviderConfig{
			"openai": {Type: "openai", APIKey: "base-key", Models: []string{"gpt-base"}},
			"proxy":  {Type: "openai", APIKey: "proxy-key", Models: []string{"proxy-model"}},
		},
		Roles: map[string]bootstrap.RoleConfig{
			"writer": {Provider: "proxy", Model: "proxy-model"},
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

	if err := host.RemoveProviderModel("proxy", "proxy-model"); err != nil {
		t.Fatalf("RemoveProviderModel: %v", err)
	}
	provider, model, explicit := host.models.CurrentSelection("writer")
	if explicit || provider != "openai" || model != "gpt-base" {
		t.Fatalf("writer selection = %s/%s explicit=%v", provider, model, explicit)
	}
	if _, ok := host.cfg.Providers["proxy"]; ok {
		t.Fatalf("proxy provider still exists: %+v", host.cfg.Providers["proxy"])
	}
	if _, ok := host.cfg.Roles["writer"]; ok {
		t.Fatalf("writer role still exists: %+v", host.cfg.Roles["writer"])
	}

	saved, err := bootstrap.LoadConfigFile(filepath.Join(home, ".ainovel", "config.json"))
	if err != nil {
		t.Fatalf("LoadConfigFile: %v", err)
	}
	if _, ok := saved.Providers["proxy"]; ok {
		t.Fatalf("saved proxy provider still exists: %+v", saved.Providers["proxy"])
	}
	if _, ok := saved.Roles["writer"]; ok {
		t.Fatalf("saved writer role still exists: %+v", saved.Roles["writer"])
	}
}

func TestRemoveProviderModelRejectsCurrentDefault(t *testing.T) {
	cfg := bootstrap.Config{
		Provider:  "openai",
		ModelName: "gpt-base",
		Providers: map[string]bootstrap.ProviderConfig{
			"openai": {Type: "openai", APIKey: "base-key", Models: []string{"gpt-base"}},
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

	err = host.RemoveProviderModel("openai", "gpt-base")
	if err == nil || !strings.Contains(err.Error(), "current default") {
		t.Fatalf("RemoveProviderModel error = %v, want current default rejection", err)
	}
	provider, model, explicit := host.models.CurrentSelection("default")
	if !explicit || provider != "openai" || model != "gpt-base" {
		t.Fatalf("default selection changed: %s/%s explicit=%v", provider, model, explicit)
	}
}
