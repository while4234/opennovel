import { describe, expect, it } from 'vitest';
import {
  buildExistingModelActionPayload,
  buildModelAddPayload,
  canSubmitModelAdd,
  createNewModelDraft,
  modelDiscoveryMessage,
  modelAddValidationMessage,
  modelAddModeDefaults,
  modelAddSaveTarget,
  modelOptionsForProvider,
  reasoningLevelsForModel
} from './App.jsx';

describe('model add helpers', () => {
  it('offers xhigh reasoning for Grok 4.5', () => {
    expect(reasoningLevelsForModel('grok-4.5', { type: 'grok', auth: 'grok_oauth' }))
      .toEqual(['', 'low', 'medium', 'high', 'xhigh']);
  });

  it('builds a Grok OAuth provider config without API key fields', () => {
    const state = {
      mode: 'grok_oauth',
      role: 'writer',
      provider: 'grok-oauth',
      model: 'grok-4.3-latest',
      account_id: 'work',
      api: 'chat',
      api_key: 'should-not-be-sent',
      base_url: 'https://example.invalid',
      grok_status: { logged_in: true }
    };

    expect(buildModelAddPayload(state, null)).toEqual({
      select_after_save: false,
      provider: 'grok-oauth',
      model: 'grok-4.3-latest',
      label: 'Grok',
      template_provider: 'grok',
      use_proxy: true,
      request_timeout_seconds: 0,
      connectivity_timeout_seconds: 0,
      network_disconnect_max_attempts: 0,
      auto_switch_candidate_pool: false,
      type: 'grok',
      auth: 'grok_oauth',
      account_id: 'work'
    });
    expect(canSubmitModelAdd(state, null)).toBe(true);
  });

  it('builds a Codex login provider config without API key fields', () => {
    const state = {
      mode: 'codex_auth',
      role: 'writer',
      provider: 'codex-login',
      model: 'gpt-5.5',
      auth_file: 'D:/codex/auth.json',
      api_key: 'should-not-be-sent',
      codex_status: { logged_in: true }
    };

    expect(buildModelAddPayload(state, null)).toEqual({
      select_after_save: false,
      provider: 'codex-login',
      model: 'gpt-5.5',
      label: 'Codex',
      template_provider: 'codex',
      use_proxy: true,
      request_timeout_seconds: 0,
      connectivity_timeout_seconds: 0,
      network_disconnect_max_attempts: 0,
      auto_switch_candidate_pool: false,
      type: 'openai',
      auth: 'codex',
      api: 'responses',
      base_url: 'https://chatgpt.com/backend-api/codex',
      auth_file: 'D:/codex/auth.json'
    });
    expect(buildModelAddPayload(state, null)).not.toHaveProperty('api_key');
    expect(canSubmitModelAdd(state, null)).toBe(true);
  });

  it('builds new custom provider payloads without assigning an agent route', () => {
    const payload = buildModelAddPayload({
      mode: 'custom',
      role: 'writer',
      provider: 'deepseek2',
      label: 'DeepSeek Relay',
      template_provider: 'custom',
      type: 'openai',
      api: 'chat',
      model: 'deepseek-v4-pro',
      api_key: 'sk-test',
      base_url: 'https://api.example/v1'
    }, null);

    expect(payload).toMatchObject({
      select_after_save: false,
      provider: 'deepseek2',
      model: 'deepseek-v4-pro',
      type: 'openai',
      api: 'chat',
      base_url: 'https://api.example/v1'
    });
    expect(payload).not.toHaveProperty('role');
  });

  it('persists model add/edit through the global registry even inside a project', () => {
    expect(modelAddSaveTarget({ id: 'project-1' }, { select_after_save: false })).toEqual({
      persistScope: 'global',
      projectId: 'project-1',
      refreshProjectModels: true,
      selectProjectAfterSave: false
    });
    expect(modelAddSaveTarget({ id: 'project-1' }, { select_after_save: true })).toEqual({
      persistScope: 'global',
      projectId: 'project-1',
      refreshProjectModels: true,
      selectProjectAfterSave: false
    });
    expect(modelAddSaveTarget(null, { select_after_save: true })).toEqual({
      persistScope: 'global',
      projectId: '',
      refreshProjectModels: false,
      selectProjectAfterSave: false
    });
  });

  it('requires a confirmed Grok login before adding the provider', () => {
    const state = {
      mode: 'grok_oauth',
      provider: 'grok-oauth',
      model: 'grok-4.3-latest',
      account_id: 'default',
      grok_status: { needs_reauth: true }
    };

    expect(canSubmitModelAdd(state, null)).toBe(false);
  });

  it('requires a confirmed Codex login before adding the provider', () => {
    const state = {
      mode: 'codex_auth',
      provider: 'codex-login',
      model: 'gpt-5.5',
      codex_status: { needs_reauth: true }
    };

    expect(canSubmitModelAdd(state, null)).toBe(false);
  });

  it('uses stable defaults when switching into Grok OAuth mode', () => {
    const state = modelAddModeDefaults({
      mode: 'grok_oauth',
      role: 'default',
      provider: 'openrouter',
      model: ''
    });

    expect(state.provider).toBe('grok-oauth');
    expect(state.type).toBe('grok');
    expect(state.auth).toBe('grok_oauth');
    expect(state.account_id).toBe('default');
    expect(state.model).toBe('grok-4.3-latest');
    expect(state.api).toBe('');
    expect(state.use_proxy).toBe(true);
  });

  it('uses stable defaults when switching into Codex login mode', () => {
    const state = modelAddModeDefaults({
      mode: 'codex_auth',
      role: 'default',
      provider: 'openrouter',
      model: ''
    });

    expect(state.provider).toBe('codex-login');
    expect(state.type).toBe('openai');
    expect(state.auth).toBe('codex');
    expect(state.model).toBe('gpt-5.5');
    expect(state.api).toBe('responses');
    expect(state.api_key).toBe('');
    expect(state.use_proxy).toBe(true);
  });

  it('omits OpenAI endpoint when editing a Grok OAuth provider', () => {
    const modelConfig = {
      providers: [{
        name: 'grok-oauth',
        label: 'Grok',
        template_provider: 'grok',
        type: 'grok',
        auth: 'grok_oauth',
        api: 'chat',
        models: ['grok-4.3-latest']
      }]
    };
    const state = modelAddModeDefaults({
      mode: 'existing',
      original_provider: 'grok-oauth',
      provider: 'grok-oauth',
      model: 'grok-4.3-latest',
      api: 'chat'
    }, modelConfig.providers);

    expect(state.api).toBe('');
    expect(buildModelAddPayload(state, modelConfig)).toMatchObject({
      select_after_save: false,
      original_provider: 'grok-oauth',
      provider: 'grok-oauth',
      model: 'grok-4.3-latest',
      type: 'grok',
      auth: 'grok_oauth',
      api: ''
    });
  });

  it('defaults Codex to proxy and DeepSeek to direct access', () => {
    const codex = modelAddModeDefaults({
      mode: 'preset',
      preset: 'codex',
      request_timeout_seconds: '120',
      connectivity_timeout_seconds: '12'
    });
    const deepseek = modelAddModeDefaults({
      mode: 'preset',
      preset: 'deepseek',
      request_timeout_seconds: '120',
      connectivity_timeout_seconds: '12'
    });

    expect(buildModelAddPayload(codex, null)).toMatchObject({
      provider: 'codex',
      template_provider: 'codex',
      api: 'responses',
      use_proxy: true,
      request_timeout_seconds: 120,
      connectivity_timeout_seconds: 12
    });
    expect(buildModelAddPayload(deepseek, null)).toMatchObject({
      provider: 'deepseek',
      template_provider: 'deepseek',
      use_proxy: false
    });
  });

  it('hydrates existing provider configs for editing', () => {
    const state = modelAddModeDefaults({
      mode: 'existing',
      role: 'default'
    }, [
      {
        name: 'openai',
        label: 'OpenAI',
        template_provider: 'openai',
        type: 'openai',
        api: 'responses',
        base_url: 'https://api.openai.com/v1',
        use_proxy: true,
        request_timeout_seconds: 90,
        connectivity_timeout_seconds: 8,
        network_disconnect_max_attempts: 4,
        auto_switch_candidate_pool: true,
        models: ['gpt-5.1']
      }
    ]);

    expect(state).toMatchObject({
      mode: 'existing',
      original_provider: 'openai',
      provider: 'openai',
      label: 'OpenAI',
      template_provider: 'openai',
      type: 'openai',
      api: 'responses',
      base_url: 'https://api.openai.com/v1',
      model: 'gpt-5.1',
      use_proxy: true,
      request_timeout_seconds: '90',
      connectivity_timeout_seconds: '8',
      network_disconnect_max_attempts: '4',
      auto_switch_candidate_pool: true,
      api_key: ''
    });
  });

  it('starts a clean one-column new custom draft from an existing provider', () => {
    const state = createNewModelDraft({
      mode: 'existing',
      role: 'writer',
      original_provider: 'custom-openai',
      provider: 'custom-openai',
      label: 'DeepSeek',
      model: 'deepseek-v4-pro',
      api_key: 'sk-old',
      base_url: 'https://api.sfkey.cn/v1',
      request_timeout_seconds: '',
      connectivity_timeout_seconds: ''
    }, [
      { name: 'custom-openai', models: ['deepseek-v4-pro'] }
    ], 'custom');

    expect(state).toMatchObject({
      mode: 'custom',
      role: 'writer',
      original_provider: '',
      provider: 'custom-openai-2',
      label: 'Custom',
      model: '',
      api_key: '',
      base_url: '',
      request_timeout_seconds: '120',
      connectivity_timeout_seconds: '12',
      network_disconnect_max_attempts: '7'
    });
  });

  it('creates a unique provider key when a preset provider already exists', () => {
    const state = modelAddModeDefaults({
      mode: 'preset',
      preset: 'deepseek',
      role: 'default'
    }, [
      { name: 'deepseek', models: ['deepseek-chat'] }
    ], 'existing');

    expect(state.provider).toBe('deepseek-2');
    expect(state.request_timeout_seconds).toBe('120');
    expect(state.connectivity_timeout_seconds).toBe('12');
    expect(state.network_disconnect_max_attempts).toBe('7');
  });

  it('builds editable existing provider payloads without empty API keys', () => {
    const payload = buildModelAddPayload({
      mode: 'existing',
      role: 'writer',
      original_provider: 'openai',
      provider: 'openai-proxy',
      label: 'OpenAI Proxy',
      template_provider: 'openai',
      type: 'openai',
      api: 'responses',
      auth: 'api_key',
      model: 'gpt-5.1',
      base_url: 'https://proxy.example/v1',
      api_key: '',
      use_proxy: true,
      request_timeout_seconds: '90',
      connectivity_timeout_seconds: '8',
      network_disconnect_max_attempts: '4',
      auto_switch_candidate_pool: true
    }, null);

    expect(payload).toEqual({
      role: 'writer',
      select_after_save: false,
      original_provider: 'openai',
      provider: 'openai-proxy',
      model: 'gpt-5.1',
      label: 'OpenAI Proxy',
      template_provider: 'openai',
      use_proxy: true,
      request_timeout_seconds: 90,
      connectivity_timeout_seconds: 8,
      network_disconnect_max_attempts: 4,
      auto_switch_candidate_pool: true,
      type: 'openai',
      auth: 'api_key',
      api: 'responses',
      base_url: 'https://proxy.example/v1'
    });
    expect(payload).not.toHaveProperty('api_key');
    expect(canSubmitModelAdd({ ...payload, mode: 'existing' }, null)).toBe(true);
  });

  it('rejects duplicate provider keys only in new-provider mode', () => {
    const modelConfig = {
      providers: [{ name: 'deepseek', models: ['deepseek-v4-pro'] }]
    };
    const duplicate = {
      mode: 'custom',
      role: 'default',
      provider: 'deepseek',
      model: 'deepseek-v4-pro',
      type: 'openai',
      api: 'chat',
      base_url: 'https://api.sfkey.cn/v1'
    };
    const existing = {
      ...duplicate,
      mode: 'existing',
      original_provider: 'deepseek'
    };

    expect(modelAddValidationMessage(duplicate, modelConfig)).toContain('已存在');
    expect(canSubmitModelAdd(duplicate, modelConfig)).toBe(false);
    expect(modelAddValidationMessage(existing, modelConfig)).toBe('');
    expect(canSubmitModelAdd(existing, modelConfig)).toBe(true);
  });

  it('sends an existing provider API key only when the user enters one', () => {
    expect(buildModelAddPayload({
      mode: 'existing',
      original_provider: 'openai',
      provider: 'openai',
      model: 'gpt-5.1',
      api_key: 'sk-new'
    }, null)).toMatchObject({
      original_provider: 'openai',
      provider: 'openai',
      model: 'gpt-5.1',
      api_key: 'sk-new'
    });
  });

  it('keeps the current default model visible even when it is not listed', () => {
    expect(modelOptionsForProvider([
      { name: 'openai', models: ['gpt-a', 'gpt-b'] }
    ], 'openai', 'gpt-custom')).toEqual(['gpt-custom', 'gpt-a', 'gpt-b']);
  });

  it('summarizes discovered models for the model name dropdown', () => {
    expect(modelDiscoveryMessage({ status: 'ok', supported: true }, [
      'grok-4.3',
      'grok-4.5',
      'grok-4.20'
    ])).toBe('测试完成，发现 3 个支持模型，请在“模型名称”下拉列表中选择');
    expect(modelDiscoveryMessage({ status: 'fallback', supported: false }, [
      'grok-4.3-latest'
    ])).toBe('服务不支持在线探测，已加载 1 个已配置模型');
    expect(modelDiscoveryMessage({ status: 'error', message: 'unauthorized' }, [])).toBe('unauthorized');
  });

  it('builds a minimal payload for testing configured models', () => {
    expect(buildExistingModelActionPayload('writer', 'grok-oauth', 'grok-4.3-latest')).toEqual({
      role: 'writer',
      provider: 'grok-oauth',
      model: 'grok-4.3-latest'
    });
  });
});
