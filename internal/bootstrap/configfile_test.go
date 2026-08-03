package bootstrap

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/voocel/ainovel-cli/internal/errs"
)

const validGlobal = `{
  "provider": "openrouter",
  "model": "google/gemini-2.5-flash",
  "providers": { "openrouter": { "api_key": "sk-test-123456" } }
}`

func TestLoadConfigFileAllowsUTF8BOM(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	data := append([]byte{0xef, 0xbb, 0xbf}, []byte(validGlobal)...)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := LoadConfigFile(path)
	if err != nil {
		t.Fatalf("LoadConfigFile should accept UTF-8 BOM: %v", err)
	}
	if cfg.Provider != "openrouter" || cfg.ModelName != "google/gemini-2.5-flash" {
		t.Fatalf("unexpected config: provider=%q model=%q", cfg.Provider, cfg.ModelName)
	}
}

func TestMergeConfigScheduledResumeFields(t *testing.T) {
	base := Config{ResumeSchedule: ResumeScheduleConfig{DailyTimes: []string{"15:00"}, Timezone: DefaultResumeScheduleTimezone}}
	disabled := false
	overlay := Config{ScheduledResumeEnabled: &disabled}
	merged := MergeConfig(base, overlay)
	if merged.EffectiveScheduledResumeEnabled() {
		t.Fatal("project overlay should explicitly disable scheduled resume")
	}
	if len(merged.ResumeSchedule.DailyTimes) != 1 || merged.ResumeSchedule.DailyTimes[0] != "15:00" {
		t.Fatalf("project enabled overlay replaced global times: %#v", merged.ResumeSchedule.DailyTimes)
	}
}

// writeGlobal 在隔离的 HOME 下写入全局配置，并返回该 HOME。
func writeGlobal(t *testing.T, content string) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	dir := filepath.Join(home, ".ainovel")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if content != "" {
		if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(content), 0o644); err != nil {
			t.Fatalf("write global: %v", err)
		}
	}
	return home
}

// writeProjectConfig 在当前工作目录的 ./.ainovel/ 下写入项目级配置。
// 调用前需先 t.Chdir 到目标目录。
func writeProjectConfig(t *testing.T, content string) {
	t.Helper()
	if err := os.MkdirAll(".ainovel", 0o755); err != nil {
		t.Fatalf("mkdir .ainovel: %v", err)
	}
	if err := os.WriteFile(filepath.Join(".ainovel", "config.json"), []byte(content), 0o644); err != nil {
		t.Fatalf("write project: %v", err)
	}
}

// 根因 3：项目级 ./.ainovel/config.json 存在但是坏 JSON，必须报错，不能静默吞掉退回全局。
func TestLoadConfig_CorruptProjectFailsLoud(t *testing.T) {
	writeGlobal(t, validGlobal)
	proj := t.TempDir()
	t.Chdir(proj)
	// 手抄示例多了个尾逗号——最常见的坏 JSON。
	writeProjectConfig(t, `{ "model": "x", }`)

	if _, err := LoadConfig(""); err == nil {
		t.Fatal("坏的 ./.ainovel/config.json 应当报错，却被静默忽略了")
	}
}

// 全局是最低优先级基底：坏文件不得阻断更高优先级的 --config 覆盖（回归守卫——
// 上一版误把全局也 fail-loud，导致"坏全局 + 有效 --config"的用户被无关文件挡住）。
func TestLoadConfig_CorruptGlobalDoesNotBlockOverride(t *testing.T) {
	writeGlobal(t, `{ not json`)
	proj := t.TempDir()
	t.Chdir(proj)
	good := filepath.Join(proj, "good.json")
	if err := os.WriteFile(good, []byte(validGlobal), 0o644); err != nil {
		t.Fatalf("write override: %v", err)
	}

	cfg, err := LoadConfig(good)
	if err != nil {
		t.Fatalf("坏全局不应阻断有效 --config，得到: %v", err)
	}
	if cfg.Provider != "openrouter" {
		t.Errorf("应使用 --config 的值，得到 provider=%q", cfg.Provider)
	}
}

// 文件不存在是正常情况（便携/首次），不能报错。
func TestLoadConfig_MissingFilesNoError(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home) // ~/.ainovel/config.json 不存在
	t.Setenv("USERPROFILE", home)
	t.Chdir(t.TempDir()) // 也没有 ./.ainovel/config.json

	if _, err := LoadConfig(""); err != nil {
		t.Fatalf("缺失配置文件不应报错，得到: %v", err)
	}
}

// 正常路径：全局 + 项目级合并生效。
func TestLoadConfig_ValidMergeWorks(t *testing.T) {
	writeGlobal(t, validGlobal)
	proj := t.TempDir()
	t.Chdir(proj)
	writeProjectConfig(t, `{
  "model": "google/gemini-2.5-pro",
  "reasoning_effort": "high",
  "roles": {
    "writer": {
      "provider": "openrouter",
      "model": "google/gemini-2.5-flash",
      "reasoning_effort": "low"
    }
  }
}`)

	cfg, err := LoadConfig("")
	if err != nil {
		t.Fatalf("有效配置不应报错: %v", err)
	}
	if cfg.Provider != "openrouter" {
		t.Errorf("provider 应保留全局值 openrouter，得到 %q", cfg.Provider)
	}
	if cfg.ModelName != "google/gemini-2.5-pro" {
		t.Errorf("model 应被项目级覆盖，得到 %q", cfg.ModelName)
	}
	if cfg.ReasoningEffort != "high" {
		t.Errorf("reasoning_effort 应被项目级覆盖，得到 %q", cfg.ReasoningEffort)
	}
	if got := cfg.Roles["writer"].ReasoningEffort; got != "low" {
		t.Errorf("roles.writer.reasoning_effort 应被项目级覆盖，得到 %q", got)
	}
}

func TestMergeConfigSimulationModeOverride(t *testing.T) {
	base := Config{SimulationMode: SimulationModeNormal}
	overlay := Config{SimulationMode: SimulationModeReinforced}

	merged := MergeConfig(base, overlay)
	if merged.SimulationMode != SimulationModeReinforced {
		t.Fatalf("merged simulation_mode = %q, want %q", merged.SimulationMode, SimulationModeReinforced)
	}

	merged = MergeConfig(Config{SimulationMode: SimulationModeReinforced}, Config{})
	if merged.SimulationMode != SimulationModeReinforced {
		t.Fatalf("empty overlay should not clear simulation_mode, got %q", merged.SimulationMode)
	}
}

func TestMergeConfigAdaptationOutlineAuditRetryOverride(t *testing.T) {
	base := Config{AdaptationOutlineAuditRetryMaxAttempts: 2}
	overlay := Config{AdaptationOutlineAuditRetryMaxAttempts: 5}

	merged := MergeConfig(base, overlay)
	if merged.AdaptationOutlineAuditRetryMaxAttempts != 5 {
		t.Fatalf("merged adaptation_outline_audit_retry_max_attempts = %d, want 5", merged.AdaptationOutlineAuditRetryMaxAttempts)
	}
}

func TestMergeConfig_ProviderExtraFields(t *testing.T) {
	baseUseProxy := true
	overlayUseProxy := false
	base := Config{
		Provider:  "openrouter",
		ModelName: "google/gemini-2.5-flash",
		Proxy:     "http://127.0.0.1:7890",
		Providers: map[string]ProviderConfig{
			"openrouter": {
				Label:                 "OpenRouter",
				TemplateProvider:      "openrouter",
				UseProxy:              &baseUseProxy,
				RequestTimeoutSeconds: 60,
				API:                   "chat",
				APIKey:                "sk-test-123456",
				ExtraBody: map[string]any{
					"temperature": 0.8,
				},
				Extra: map[string]any{
					"user_agent": "base-client/1.0",
				},
			},
		},
	}
	overlay := Config{
		Proxy: "127.0.0.1:7897",
		Providers: map[string]ProviderConfig{
			"openrouter": {
				Label:                      "OpenRouter Proxy",
				TemplateProvider:           "codex",
				UseProxy:                   &overlayUseProxy,
				RequestTimeoutSeconds:      120,
				ConnectivityTimeoutSeconds: 12,
				API:                        "responses",
				BaseURL:                    "https://proxy.example.com/v1",
				ExtraBody: map[string]any{
					"min_p": 0.05,
				},
				Extra: map[string]any{
					"user_agent": "override-client/1.0",
					"headers": map[string]any{
						"X-Custom-Client": "ainovel",
					},
				},
			},
		},
	}

	cfg := mergeConfig(base, overlay)
	if cfg.Proxy != "127.0.0.1:7897" {
		t.Fatalf("Proxy = %q, want overlay proxy", cfg.Proxy)
	}
	pc := cfg.Providers["openrouter"]
	if pc.Label != "OpenRouter Proxy" || pc.TemplateProvider != "codex" {
		t.Fatalf("provider metadata = label %q template %q", pc.Label, pc.TemplateProvider)
	}
	if pc.UseProxy == nil || *pc.UseProxy {
		t.Fatalf("UseProxy = %#v, want explicit false", pc.UseProxy)
	}
	if pc.RequestTimeoutSeconds != 120 || pc.ConnectivityTimeoutSeconds != 12 {
		t.Fatalf("timeouts = %d/%d, want 120/12", pc.RequestTimeoutSeconds, pc.ConnectivityTimeoutSeconds)
	}
	if pc.APIKey != "sk-test-123456" {
		t.Fatalf("APIKey = %q, want inherited key", pc.APIKey)
	}
	if pc.API != "responses" {
		t.Fatalf("API = %q, want responses", pc.API)
	}
	if pc.BaseURL != "https://proxy.example.com/v1" {
		t.Fatalf("BaseURL = %q, want overlay URL", pc.BaseURL)
	}
	if _, ok := pc.ExtraBody["temperature"]; ok {
		t.Fatalf("ExtraBody should be replaced by overlay, got %#v", pc.ExtraBody)
	}
	if got := pc.ExtraBody["min_p"]; got != 0.05 {
		t.Fatalf("ExtraBody[min_p] = %#v, want 0.05", got)
	}
	if got := pc.Extra["user_agent"]; got != "override-client/1.0" {
		t.Fatalf("Extra[user_agent] = %#v, want override-client/1.0", got)
	}
	headers, ok := pc.Extra["headers"].(map[string]any)
	if !ok {
		t.Fatalf("Extra[headers] missing or invalid: %#v", pc.Extra["headers"])
	}
	if got := headers["X-Custom-Client"]; got != "ainovel" {
		t.Fatalf("Extra.headers[X-Custom-Client] = %#v, want ainovel", got)
	}
}

func TestMergeConfigRuntimeRoot(t *testing.T) {
	base := Config{RuntimeRoot: "base-root"}
	overlay := Config{RuntimeRoot: "web-root"}

	cfg := mergeConfig(base, overlay)
	if cfg.RuntimeRoot != "web-root" {
		t.Fatalf("RuntimeRoot = %q, want web-root", cfg.RuntimeRoot)
	}
}

// 根因 2（issue #37 核心复现）：项目级覆盖 provider 但没声明对应 providers 凭证，
// ValidateBase 必须报 config 错误（而非放行后在更深处崩溃）。
func TestValidateBase_ProviderOverrideWithoutCredentials(t *testing.T) {
	cfg := Config{
		Provider:  "mimo",
		ModelName: "mimo-v2.5-pro",
		Providers: map[string]ProviderConfig{
			"openrouter": {APIKey: "sk-test-123456"},
		},
	}
	cfg.FillDefaults()
	err := cfg.ValidateBase()
	if err == nil {
		t.Fatal("provider 缺凭证应报错")
	}
	if !errors.Is(err, errs.ErrConfig) {
		t.Errorf("应包装 errs.ErrConfig，得到: %v", err)
	}
}

func TestValidateBaseRejectsInvalidProviderAPI(t *testing.T) {
	cfg := Config{
		Provider:  "openai",
		ModelName: "gpt-5.1",
		Providers: map[string]ProviderConfig{
			"openai": {APIKey: "sk-test-123456", API: "legacy"},
		},
	}
	cfg.FillDefaults()
	err := cfg.ValidateBase()
	if err == nil {
		t.Fatal("provider api 非法应报错")
	}
	if !errors.Is(err, errs.ErrConfig) {
		t.Errorf("应包装 errs.ErrConfig，得到: %v", err)
	}
}

func TestValidateBaseRejectsProviderAPIOnNonOpenAIProvider(t *testing.T) {
	cfg := Config{
		Provider:  "anthropic",
		ModelName: "claude-sonnet-4",
		Providers: map[string]ProviderConfig{
			"anthropic": {APIKey: "sk-test-123456", API: "responses"},
		},
	}
	cfg.FillDefaults()
	err := cfg.ValidateBase()
	if err == nil {
		t.Fatal("非 OpenAI provider 配置 api 应报错")
	}
	if !errors.Is(err, errs.ErrConfig) {
		t.Errorf("应包装 errs.ErrConfig，得到: %v", err)
	}
}

// 示例配置必须自洽：去注释后是合法 JSON、
// 顶层 provider 指针不悬空、且点破了“指针”心智——它是用户照抄的样板，自己坏了就坑人。
func TestExampleConfigIsValidAndSelfConsistent(t *testing.T) {
	if exampleConfig == "" {
		t.Fatal("go:embed 未生效，exampleConfig 为空")
	}
	rootExample, err := os.ReadFile(filepath.Join("..", "..", "config.example.jsonc"))
	if err != nil {
		t.Fatalf("读取根目录 config.example.jsonc: %v", err)
	}
	if string(rootExample) != exampleConfig {
		t.Fatal("根目录 config.example.jsonc 与 internal/bootstrap/config.example.jsonc 不一致")
	}
	var cfg Config
	if err := json.Unmarshal(stripJSONComments([]byte(exampleConfig)), &cfg); err != nil {
		t.Fatalf("内置示例去注释后不是合法 JSON（用户照抄即坑）: %v", err)
	}
	if cfg.Provider == "" || cfg.ModelName == "" {
		t.Fatal("示例应给出默认 provider/model")
	}
	if _, ok := cfg.Providers[cfg.Provider]; !ok {
		t.Errorf("示例顶层 provider %q 未指向 providers 中的条目——指针正面样板自己悬空了", cfg.Provider)
	}
	if !contains(exampleConfig, "指针") {
		t.Error("示例应点破“provider 是指针”——别让 #37 的认知陷阱回潮")
	}
}

func TestWriteStartupError(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	path := WriteStartupError("boom: provider not configured")
	if path == "" {
		t.Fatal("应返回落盘路径")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 last-error.log: %v", err)
	}
	if want := "boom: provider not configured"; !contains(string(data), want) {
		t.Errorf("日志应包含 %q，实际: %s", want, data)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
