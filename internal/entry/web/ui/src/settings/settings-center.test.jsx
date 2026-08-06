// @vitest-environment jsdom

import React from 'react';
import { act } from 'react';
import { createRoot } from 'react-dom/client';
import { createMemoryRouter, MemoryRouter, RouterProvider, useLocation } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { ContextSettings, contextSnapshotView } from './ContextSettings.jsx';
import { GlobalPromptSettings, MAX_GLOBAL_PROMPT_BYTES, globalPromptValidation } from './GlobalPromptSettings.jsx';
import {
  SettingsCenter,
  settingsProjectIdFromSearch,
  settingsSectionFromPath,
  settingsSectionPath
} from './SettingsCenter.jsx';
import { NavigationGuardProvider, useGuardedNavigate, useNavigationGuard } from '../navigation/NavigationGuard.jsx';

const promptFamilies = [
  ['claude', 'Claude', ['claude', 'anthropic', 'opus']],
  ['deepseek', 'DeepSeek', ['deepseek']],
  ['gemini', 'Gemini', ['gemini']],
  ['gpt', 'GPT', ['gpt', 'openai', 'zapi']],
  ['grok', 'Grok', ['grok', 'xai']],
  ['kimi', 'Kimi', ['kimi', 'moonshot']]
].map(([family, label, aliases]) => ({ family, label, aliases, content: `${family} built in`, overridden: family === 'gpt', fallback: family === 'deepseek' }));

function changeTextarea(textarea, value) {
  const setter = Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, 'value').set;
  setter.call(textarea, value);
  textarea.dispatchEvent(new Event('input', { bubbles: true }));
}

describe('settings center', () => {
  it('resolves every independent settings route and renders its secondary navigation', () => {
    for (const section of ['providers', 'models', 'context', 'prompts', 'schedule', 'backend']) {
      expect(settingsSectionFromPath(`/settings/${section}`)).toBe(section);
    }
    expect(settingsSectionFromPath('/settings/unknown')).toBe('providers');
  });

  it('round-trips a project model scope through copyable settings URLs', () => {
    expect(settingsProjectIdFromSearch('?project=project%20one')).toBe('project one');
    expect(settingsProjectIdFromSearch('?other=value')).toBe('');
    expect(settingsSectionPath('models', 'project one')).toBe('/settings/models?project=project+one');
    expect(settingsSectionPath('providers', 'project one')).toBe('/settings/providers?project=project+one');
    expect(settingsSectionPath('prompts', 'project one')).toBe('/settings/prompts');
  });
});

describe('context settings', () => {
  it('maps coordinator and agent context without introducing write controls', () => {
    const view = contextSnapshotView({
      ModelName: 'gpt-5.6', ModelContextWindow: 225000,
      ContextTokens: 45000, ContextWindow: 90000, ContextPercent: 50,
      ContextScope: 'book', ContextStrategy: 'summary',
      ContextActiveMessages: 9, ContextSummaryCount: 2, ContextCompactedCount: 3, ContextKeptCount: 4,
      Agents: [{ Name: 'writer', State: 'working', Context: { Tokens: 12000, ContextWindow: 64000, Percent: 18.75, Scope: 'chapter', Strategy: 'recent', ActiveMessages: 5, SummaryMessages: 1, CompactedCount: 2, KeptCount: 3 } }]
    });
    expect(view.modelName).toBe('gpt-5.6');
    expect(view.coordinator).toMatchObject({ tokens: 45000, window: 90000, active: 9, summary: 2, compacted: 3, kept: 4 });
    expect(view.agents[0]).toMatchObject({ name: 'writer', tokens: 12000, window: 64000, scope: 'chapter' });
  });
});

describe('global prompt settings', () => {
  let container;
  let root;

  beforeEach(() => {
    globalThis.IS_REACT_ACT_ENVIRONMENT = true;
    container = document.createElement('div');
    document.body.append(container);
    root = createRoot(container);
  });

  afterEach(async () => {
    await act(async () => root.unmount());
    container.remove();
    vi.restoreAllMocks();
  });

  async function renderPrompts(api = {}) {
    const resolvedAPI = {
      get: vi.fn().mockResolvedValue({ prompts: promptFamilies }),
      update: vi.fn().mockImplementation(async (family, content) => ({ prompts: promptFamilies.map((prompt) => prompt.family === family ? { ...prompt, content, overridden: true } : prompt) })),
      reset: vi.fn().mockImplementation(async (family) => ({ prompts: promptFamilies.map((prompt) => prompt.family === family ? { ...prompt, content: `${family} built in`, overridden: false } : prompt) })),
      ...api
    };
    await act(async () => root.render(<GlobalPromptSettings api={resolvedAPI} confirmAction={() => true} />));
    await act(async () => Promise.resolve());
    return resolvedAPI;
  }

  it('enforces the backend-compatible content validation', () => {
    expect(globalPromptValidation('  ').message).toContain('不能为空');
    expect(globalPromptValidation('before\0after').message).toContain('NUL');
    expect(globalPromptValidation('界'.repeat(MAX_GLOBAL_PROMPT_BYTES)).message).toContain('64 KiB');
    expect(globalPromptValidation('valid prompt')).toMatchObject({ valid: true, bytes: 12 });
  });

  it('loads all six families, saves the selected content, and resets an override', async () => {
    const api = await renderPrompts();
    expect(container.querySelectorAll('.prompt-family-list button')).toHaveLength(6);
    expect(container.textContent).toContain('未知模型默认');

    const textarea = container.querySelector('textarea');
    await act(async () => {
      changeTextarea(textarea, 'updated Claude prompt');
    });
    const saveButton = Array.from(container.querySelectorAll('button')).find((button) => button.textContent === '保存');
    await act(async () => saveButton.click());
    expect(api.update).toHaveBeenCalledWith('claude', 'updated Claude prompt');

    await act(async () => Array.from(container.querySelectorAll('.prompt-family-list button')).find((button) => button.textContent.includes('GPT')).click());
    const resetButton = Array.from(container.querySelectorAll('button')).find((button) => button.textContent.includes('恢复内置'));
    await act(async () => resetButton.click());
    expect(api.reset).toHaveBeenCalledWith('gpt');
  });

  it('keeps backend errors visible and prevents invalid saves', async () => {
    const api = await renderPrompts({ update: vi.fn().mockRejectedValue(new Error('disk unavailable')) });
    const textarea = container.querySelector('textarea');
    await act(async () => {
      changeTextarea(textarea, 'changed');
    });
    const saveButton = Array.from(container.querySelectorAll('button')).find((button) => button.textContent === '保存');
    await act(async () => saveButton.click());
    expect(container.textContent).toContain('disk unavailable');
    expect(api.update).toHaveBeenCalledOnce();

    await act(async () => {
      changeTextarea(textarea, '   ');
    });
    expect(container.textContent).toContain('trim 后不能为空');
    expect(saveButton.disabled).toBe(true);
  });

  it('warns before a dirty route link leaves the editor', async () => {
    const confirm = vi.fn().mockReturnValue(false);
    const router = createMemoryRouter([{ path: '*', element: <NavigationGuardProvider><SettingsCenter section="prompts"><GlobalPromptSettings confirmAction={confirm} api={{ get: vi.fn().mockResolvedValue({ prompts: promptFamilies }), update: vi.fn(), reset: vi.fn() }} /></SettingsCenter></NavigationGuardProvider> }], { initialEntries: ['/settings/prompts'] });
    await act(async () => root.render(<RouterProvider router={router} />));
    await act(async () => Promise.resolve());
    const textarea = container.querySelector('textarea');
    await act(async () => {
      changeTextarea(textarea, 'dirty content');
    });
    const link = container.querySelector('a[href="/settings/providers"]');
    const event = new MouseEvent('click', { bubbles: true, cancelable: true });
    await act(async () => link.dispatchEvent(event));
    expect(confirm).toHaveBeenCalled();
    expect(event.defaultPrevented).toBe(true);
    expect(router.state.location.pathname).toBe('/settings/prompts');
  });
});

function GuardedNavigationHarness({ confirmAction }) {
  const location = useLocation();
  const navigate = useGuardedNavigate();
  useNavigationGuard(true, confirmAction);
  return <><span data-path>{location.pathname}</span><button onClick={() => navigate('/settings/models')} type="button">programmatic</button></>;
}

describe('navigation guard', () => {
  let container;
  let root;
  beforeEach(() => { globalThis.IS_REACT_ACT_ENVIRONMENT = true; container = document.createElement('div'); document.body.append(container); root = createRoot(container); });
  afterEach(async () => { await act(async () => root.unmount()); container.remove(); });

  it('blocks programmatic SPA navigation through the same confirmation contract', async () => {
    const confirmAction = vi.fn().mockReturnValue(false);
    const router = createMemoryRouter([{ path: '*', element: <NavigationGuardProvider><GuardedNavigationHarness confirmAction={confirmAction} /></NavigationGuardProvider> }], { initialEntries: ['/settings/prompts'] });
    await act(async () => root.render(<RouterProvider router={router} />));
    await act(async () => container.querySelector('button').click());
    expect(confirmAction).toHaveBeenCalledWith('navigation');
    expect(container.querySelector('[data-path]').textContent).toBe('/settings/prompts');
  });
});

describe('context settings rendering', () => {
  let container;
  let root;
  beforeEach(() => { globalThis.IS_REACT_ACT_ENVIRONMENT = true; container = document.createElement('div'); document.body.append(container); root = createRoot(container); });
  afterEach(async () => { await act(async () => root.unmount()); container.remove(); });

  it('loads the selected project snapshot and exposes no save control', async () => {
    const loadSnapshot = vi.fn().mockResolvedValue({ snapshot: { ModelName: 'gpt-5.6', ModelContextWindow: 225000, ContextTokens: 10, ContextWindow: 100, ContextPercent: 10 } });
    await act(async () => root.render(<ContextSettings projects={[{ id: 'p1', name: '项目一' }]} loadSnapshot={loadSnapshot} />));
    await act(async () => Promise.resolve());
    expect(loadSnapshot).toHaveBeenCalledWith('p1');
    expect(container.textContent).toContain('gpt-5.6');
    expect(container.textContent).toContain('225,000');
    expect(Array.from(container.querySelectorAll('button')).some((button) => button.textContent.includes('保存'))).toBe(false);
  });
});
