package web

import "github.com/voocel/ainovel-cli/internal/bootstrap"

func cloneWebConfig(cfg bootstrap.Config) bootstrap.Config {
	out := cfg
	out.ResumeSchedule.DailyTimes = append([]string(nil), cfg.ResumeSchedule.DailyTimes...)
	if cfg.ScheduledResumeEnabled != nil {
		enabled := *cfg.ScheduledResumeEnabled
		out.ScheduledResumeEnabled = &enabled
	}
	out.ModelAutoSwitch = cloneWebModelAutoSwitchConfig(cfg.ModelAutoSwitch)
	out.Providers = cloneWebProviderConfigs(cfg.Providers)
	out.Roles = cloneWebRoleConfigs(cfg.Roles)
	out.ProjectOwnedProviders = cloneWebBoolMap(cfg.ProjectOwnedProviders)
	if cfg.PersistProviders != nil {
		out.PersistProviders = cloneWebBoolMap(cfg.PersistProviders)
	}
	if cfg.PersistProjectConfig != nil {
		project := cloneWebConfig(*cfg.PersistProjectConfig)
		out.PersistProjectConfig = &project
	}
	return out
}

func cloneWebBoolMap(values map[string]bool) map[string]bool {
	if len(values) == 0 {
		return nil
	}
	out := make(map[string]bool, len(values))
	for key, value := range values {
		out[key] = value
	}
	return out
}

func cloneWebModelAutoSwitchConfig(cfg bootstrap.ModelAutoSwitchConfig) bootstrap.ModelAutoSwitchConfig {
	out := cfg
	out.FallbackBackends = append([]string(nil), cfg.FallbackBackends...)
	if cfg.Enabled != nil {
		enabled := *cfg.Enabled
		out.Enabled = &enabled
	}
	return out
}

func cloneWebProviderConfigs(providers map[string]bootstrap.ProviderConfig) map[string]bootstrap.ProviderConfig {
	if len(providers) == 0 {
		return nil
	}
	out := make(map[string]bootstrap.ProviderConfig, len(providers))
	for name, provider := range providers {
		out[name] = cloneWebProviderConfig(provider)
	}
	return out
}

func cloneWebProviderConfig(provider bootstrap.ProviderConfig) bootstrap.ProviderConfig {
	out := provider
	out.Models = append([]string(nil), provider.Models...)
	out.ModelReasoningEfforts = cloneWebStringMap(provider.ModelReasoningEfforts)
	out.ExtraBody = cloneWebAnyMap(provider.ExtraBody)
	out.Extra = cloneWebAnyMap(provider.Extra)
	return out
}

func cloneWebStringMap(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	out := make(map[string]string, len(values))
	for key, value := range values {
		out[key] = value
	}
	return out
}

func cloneWebRoleConfigs(roles map[string]bootstrap.RoleConfig) map[string]bootstrap.RoleConfig {
	if len(roles) == 0 {
		return nil
	}
	out := make(map[string]bootstrap.RoleConfig, len(roles))
	for name, role := range roles {
		copyRole := role
		copyRole.Fallbacks = append([]bootstrap.ModelRef(nil), role.Fallbacks...)
		out[name] = copyRole
	}
	return out
}

func cloneWebAnyMap(values map[string]any) map[string]any {
	if len(values) == 0 {
		return nil
	}
	out := make(map[string]any, len(values))
	for key, value := range values {
		out[key] = value
	}
	return out
}
