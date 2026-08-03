package host

import (
	"testing"

	"github.com/voocel/ainovel-cli/internal/bootstrap"
)

func TestRestoreConfiguredModelRoutesResetsRuntimeStageFailover(t *testing.T) {
	cfg := bootstrap.Config{
		Provider:  "deepseek-suifeng",
		ModelName: "deepseek-v4-pro",
		Providers: map[string]bootstrap.ProviderConfig{
			"deepseek-suifeng":  {Type: "openai", APIKey: "primary-key", Models: []string{"deepseek-v4-pro"}},
			"deepseek-yuanyu-0": {Type: "openai", APIKey: "fallback-key", Models: []string{"deepseek-v4-pro"}},
		},
	}
	cfg.FillDefaults()
	models, err := bootstrap.NewModelSet(cfg)
	if err != nil {
		t.Fatalf("NewModelSet: %v", err)
	}
	host := &Host{cfg: cloneHostRuntimeConfig(cfg), models: models}
	writingRoute := bootstrap.StageRouteKey(bootstrap.StageWriting)

	if err := models.Swap(writingRoute, "deepseek-yuanyu-0", "deepseek-v4-pro"); err != nil {
		t.Fatalf("simulate runtime failover: %v", err)
	}
	provider, _, _ := host.CurrentModelSelection(writingRoute)
	if provider != "deepseek-yuanyu-0" {
		t.Fatalf("runtime provider before restore = %q, want fallback", provider)
	}

	if err := host.restoreConfiguredModelRoutes(); err != nil {
		t.Fatalf("restoreConfiguredModelRoutes: %v", err)
	}
	provider, model, explicit := host.CurrentModelSelection(writingRoute)
	if provider != "deepseek-suifeng" || model != "deepseek-v4-pro" || explicit {
		t.Fatalf("restored writing route = %s/%s explicit=%v, want inherited configured primary", provider, model, explicit)
	}
}
