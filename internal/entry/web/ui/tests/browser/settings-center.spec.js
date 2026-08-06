import { mkdir } from 'node:fs/promises';
import path from 'node:path';
import { expect, test } from '@playwright/test';

const screenshotDirectory = path.resolve('test-results/ui-refactor/phase7');
const projects = [{ id: 'project-settings', name: '雾城来信', updated_at: '2026-08-05T12:00:00Z' }];
const modelConfig = {
  providers: [
    { name: 'deepseek', label: 'DeepSeek', type: 'deepseek', models: ['deepseek-chat'], base_url: 'https://api.deepseek.com', use_proxy: false },
    { name: 'openai', label: 'OpenAI', type: 'openai', models: ['gpt-5.6'], base_url: 'https://api.openai.com/v1', use_proxy: true }
  ],
  roles: [{ role: 'default', provider: 'deepseek', model: 'deepseek-chat', explicit: true }],
  stages: [{ role: 'architect', label: '骨架规划', provider: 'deepseek', model: 'deepseek-chat', explicit: false, reasoning_effort: '' }],
  cocreate_timeout_seconds: 60,
  cocreate_max_tokens: 4096,
  model_auto_switch: { model_call_max_attempts: 7 },
  structure_repair_max_attempts: 2,
  budget_quality_max_attempts: 2,
  adaptation_outline_audit_retry_max_attempts: 2
};
let prompts;
let resumeSchedule;

function resetPrompts() {
  prompts = [
    ['claude', 'Claude', ['claude', 'anthropic', 'opus']],
    ['deepseek', 'DeepSeek', ['deepseek']],
    ['gemini', 'Gemini', ['gemini']],
    ['gpt', 'GPT', ['gpt', 'openai', 'zapi']],
    ['grok', 'Grok', ['grok', 'xai']],
    ['kimi', 'Kimi', ['kimi', 'moonshot']]
  ].map(([family, label, aliases]) => ({ family, label, aliases, content: `# ${label}\nBuilt-in ${family} prompt`, overridden: family === 'gpt', fallback: family === 'deepseek' }));
}

async function waitForStableCount(page, readCount, attempts = 6) {
  for (let attempt = 0; attempt < attempts; attempt += 1) {
    const before = readCount();
    await page.waitForTimeout(200);
    const after = readCount();
    if (after === before) return after;
  }
  throw new Error('snapshot hydration did not reach a stable request count');
}

test.beforeAll(async () => mkdir(screenshotDirectory, { recursive: true }));
test.beforeEach(async ({ page }) => {
  resetPrompts();
  resumeSchedule = { daily_times: ['15:00'], timezone: 'Asia/Shanghai' };
  const browserErrors = [];
  page.on('console', (message) => { if (message.type() === 'error') browserErrors.push(message.text()); });
  page.on('pageerror', (error) => browserErrors.push(error.message));
  page.__settingsBrowserErrors = browserErrors;
  page.__settingsRequests = [];
  page.__settingsBackendShouldFail = false;
  await page.route('**/api/**', async (route) => {
    const request = route.request();
    const url = new URL(request.url());
    const pathname = url.pathname;
    page.__settingsRequests.push({ method: request.method(), pathname, body: request.postDataJSON?.() });
    if (pathname === '/api/setup') return route.fulfill({ json: { setup_required: false, providers: [] } });
    if (pathname === '/api/runtime') return route.fulfill({ json: { runtime_root: 'D:/OpenNovel', config: { provider: 'deepseek', model: 'deepseek-chat', style: 'default' } } });
    if (pathname === '/api/projects' && request.method() === 'GET') return route.fulfill({ json: { projects } });
    if (pathname === '/api/models' && request.method() === 'GET') return route.fulfill({ json: { models: modelConfig } });
    if (pathname === '/api/projects/project-settings/models' && request.method() === 'GET') return route.fulfill({ json: { models: modelConfig } });
    if (pathname === '/api/models/default' && request.method() === 'POST') return route.fulfill({ json: { models: modelConfig, runtime: { config: { provider: request.postDataJSON().provider, model: request.postDataJSON().model } } } });
    if (pathname === '/api/models/discover' && request.method() === 'POST') return route.fulfill({ json: { discovery: { models: ['deepseek-chat', 'deepseek-reasoner'] } } });
    if (/^\/api\/projects\/[^/]+\/events$/.test(pathname)) return route.fulfill({ contentType: 'text/event-stream', body: ': connected\n\n' });
    if (pathname === '/api/models/global-prompts' && request.method() === 'GET') return route.fulfill({ json: { prompts } });
    const promptMatch = pathname.match(/^\/api\/models\/global-prompts\/([^/]+)$/);
    if (promptMatch && request.method() === 'PUT') {
      const family = decodeURIComponent(promptMatch[1]);
      prompts = prompts.map((prompt) => prompt.family === family ? { ...prompt, content: request.postDataJSON().content, overridden: true } : prompt);
      return route.fulfill({ json: { prompts } });
    }
    if (promptMatch && request.method() === 'DELETE') {
      const family = decodeURIComponent(promptMatch[1]);
      prompts = prompts.map((prompt) => prompt.family === family ? { ...prompt, content: `# ${prompt.label}\nBuilt-in ${family} prompt`, overridden: false } : prompt);
      return route.fulfill({ json: { prompts } });
    }
    const snapshotMatch = pathname.match(/^\/api\/projects\/([^/]+)\/snapshot$/);
    if (snapshotMatch) return route.fulfill({ json: { project: projects.find((project) => project.id === snapshotMatch[1]), events: [], latest_event_seq: 0, snapshot: {
      ModelName: 'gpt-5.6', ModelContextWindow: 225000,
      ContextTokens: 45000, ContextWindow: 90000, ContextPercent: 50,
      ContextScope: 'book', ContextStrategy: 'summary', ContextActiveMessages: 12,
      ContextSummaryCount: 3, ContextCompactedCount: 5, ContextKeptCount: 7,
      Agents: [{ Name: 'writer', State: 'working', Context: { Tokens: 12000, ContextWindow: 64000, Percent: 18.75, Scope: 'chapter', Strategy: 'recent', ActiveMessages: 4, SummaryMessages: 1, CompactedCount: 2, KeptCount: 3 } }]
    } } });
    if (pathname === '/api/resume-schedule' && request.method() === 'GET') return route.fulfill({ json: { schedule: resumeSchedule } });
    if (pathname === '/api/resume-schedule' && request.method() === 'PUT') {
      resumeSchedule = { ...resumeSchedule, daily_times: request.postDataJSON().daily_times };
      return route.fulfill({ json: { schedule: resumeSchedule } });
    }
    if (pathname === '/api/projects/project-settings/backend/status' && request.method() === 'GET') return route.fulfill({ json: { backend: { status: 'ready', provider: 'deepseek', model: 'deepseek-chat', runtime_state: 'idle', recent_calls: [] } } });
    if (pathname === '/api/projects/project-settings/backend/test' && request.method() === 'POST') {
      if (page.__settingsBackendShouldFail) return route.fulfill({ status: 503, json: { error: { code: 'backend_unavailable', message: '后端测试暂不可用' } } });
      return route.fulfill({ json: { backend: { status: 'ready', provider: 'deepseek', model: 'deepseek-chat', runtime_state: 'idle', recent_calls: [], manual_test: { message: '连接测试成功' } } } });
    }
    if (pathname === '/api/styles') return route.fulfill({ json: { styles: [{ id: 'default', label: '默认' }], default_style: 'default' } });
    return route.fulfill({ json: {} });
  });
});

test.afterEach(async ({ page }) => {
  expect(page.__settingsBrowserErrors, 'settings pages should not emit browser errors').toEqual([]);
});

test('provider and model settings retain their real request mappings', async ({ page }, testInfo) => {
  test.skip(testInfo.project.name !== 'desktop');
  await page.goto('/settings/providers');
  const providersPage = page.locator('.settings-section-providers');
  await expect(page.getByRole('heading', { name: '提供商', exact: true })).toBeVisible();
  await expect(providersPage.getByText('配置模型', { exact: true })).toBeVisible();
  await providersPage.getByRole('button', { name: '测试并发现模型' }).click();
  await expect.poll(() => page.__settingsRequests.some((item) => item.method === 'POST' && item.pathname === '/api/models/discover' && item.body.provider === 'deepseek')).toBe(true);
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
  await page.screenshot({ path: path.join(screenshotDirectory, 'providers.png'), fullPage: true });

  await page.goto('/settings/models');
  const modelsPage = page.locator('.settings-section-models');
  await expect(page.getByRole('heading', { name: '模型', exact: true })).toBeVisible();
  await expect(modelsPage.getByText('当前默认模型', { exact: true })).toBeVisible();
  await modelsPage.locator('.default-model-controls select').first().selectOption('openai');
  await expect.poll(() => page.__settingsRequests.some((item) => item.method === 'POST' && item.pathname === '/api/models/default' && item.body.provider === 'openai' && item.body.model === 'gpt-5.6')).toBe(true);
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
  await page.screenshot({ path: path.join(screenshotDirectory, 'models.png'), fullPage: true });
});

test('settings model scope does not replace or reset the active writing project', async ({ page }, testInfo) => {
  test.skip(testInfo.project.name !== 'desktop');
  await page.addInitScript(() => {
    class StableEventSource {
      static CONNECTING = 0;
      static OPEN = 1;
      static CLOSED = 2;
      static instances = 0;

      constructor(url) {
        this.url = url;
        this.readyState = StableEventSource.OPEN;
        StableEventSource.instances += 1;
        queueMicrotask(() => this.onopen?.(new Event('open')));
        if (StableEventSource.instances === 1) {
          setTimeout(() => this.onerror?.(new Event('error')), 50);
        }
      }

      addEventListener() {
        // This test exercises project/session state, not event payload handling.
      }

      close() {
        this.readyState = StableEventSource.CLOSED;
      }
    }
    Object.defineProperty(window, 'EventSource', { configurable: true, value: StableEventSource });
  });
  await page.goto('/projects/project-settings/write');
  const snapshotCount = () => page.__settingsRequests.filter((item) => item.pathname === '/api/projects/project-settings/snapshot').length;
  await expect(page.locator('.writing-pane')).toBeVisible();
  await expect.poll(() => page.__settingsRequests.some((item) => item.pathname === '/api/projects/project-settings/resume-schedule')).toBe(true);
  await expect.poll(snapshotCount).toBeGreaterThanOrEqual(2);
  const initialSnapshotCount = await waitForStableCount(page, snapshotCount);
  await expect(page.getByRole('link', { name: '雾城来信', exact: true })).toBeVisible();

  await page.getByRole('link', { name: '设置', exact: true }).click();
  const providersPage = page.locator('.settings-section-providers');
  await providersPage.getByRole('button', { name: '新建', exact: true }).click();
  const settingsDraft = providersPage.getByLabel('显示名称');
  await settingsDraft.fill('未保存的设置草稿');
  const draftValue = await settingsDraft.inputValue();
  await page.getByLabel('配置作用域').selectOption('project-settings');
  await expect.poll(() => page.__settingsRequests.some((item) => item.method === 'GET' && item.pathname === '/api/projects/project-settings/models')).toBe(true);
  await expect(settingsDraft).toHaveValue(draftValue);
  await expect(page.getByRole('link', { name: '雾城来信', exact: true })).toBeVisible();
  await page.waitForTimeout(200);
  expect(snapshotCount()).toBe(initialSnapshotCount);

  await page.getByRole('link', { name: '雾城来信', exact: true }).click();
  await expect(page.locator('.writing-pane')).toBeVisible();
  expect(snapshotCount()).toBe(initialSnapshotCount);
});

test('workspace model shortcut selects project scope while prompts stay inside settings', async ({ page }, testInfo) => {
  test.skip(testInfo.project.name !== 'desktop');
  await page.goto('/projects/project-settings/write');
  const primaryNavigation = page.getByRole('complementary', { name: '主导航' });
  await expect(primaryNavigation.getByRole('link', { name: '提示词', exact: true })).toHaveCount(0);

  await page.getByRole('link', { name: '配置本项目模型', exact: true }).click();
  await expect(page).toHaveURL(/\/settings\/models\?project=project-settings$/);
  await expect(page.getByLabel('配置作用域')).toHaveValue('project-settings');
  await expect.poll(() => page.__settingsRequests.some((item) => item.method === 'GET' && item.pathname === '/api/projects/project-settings/models')).toBe(true);
  await expect(page.locator('.settings-center-sidebar').getByRole('link', { name: /提示词/ })).toBeVisible();
});

test('context settings are project-selectable and read only', async ({ page }, testInfo) => {
  test.skip(testInfo.project.name !== 'desktop');
  await page.goto('/settings/context');
  await expect(page.getByRole('heading', { name: '上下文', exact: true })).toBeVisible();
  await expect(page.getByText('gpt-5.6', { exact: true })).toBeVisible();
  await expect(page.getByText('225,000', { exact: true })).toBeVisible();
  await expect(page.getByText('writer', { exact: true })).toBeVisible();
  await expect(page.getByRole('button', { name: /保存/ })).toHaveCount(0);
  expect(page.__settingsRequests.some((item) => item.method === 'GET' && item.pathname === '/api/projects/project-settings/snapshot')).toBe(true);
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
  await page.screenshot({ path: path.join(screenshotDirectory, 'context.png'), fullPage: true });
});

test('prompt settings save and reset a fixed family through PUT and DELETE', async ({ page }, testInfo) => {
  test.skip(testInfo.project.name !== 'desktop');
  await page.goto('/settings/prompts');
  await expect(page.getByRole('heading', { name: '提示词', exact: true })).toBeVisible();
  await expect(page.locator('.prompt-family-list button')).toHaveCount(6);
  await page.getByRole('button', { name: /GPT/ }).click();
  await page.getByLabel('提示词内容').fill('# Custom GPT\nNew prompt');
  await page.getByRole('button', { name: '保存', exact: true }).click();
  await expect(page.getByRole('status')).toContainText('已保存');
  expect(page.__settingsRequests.some((item) => item.method === 'PUT' && item.pathname === '/api/models/global-prompts/gpt' && item.body.content === '# Custom GPT\nNew prompt')).toBe(true);
  page.once('dialog', (dialog) => dialog.accept());
  await page.getByRole('button', { name: '恢复内置' }).click();
  await expect(page.getByRole('status')).toContainText('已恢复内置提示词');
  expect(page.__settingsRequests.some((item) => item.method === 'DELETE' && item.pathname === '/api/models/global-prompts/gpt')).toBe(true);
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
  await page.screenshot({ path: path.join(screenshotDirectory, 'prompts.png'), fullPage: true });
});

test('dirty prompts guard browser back and forward history navigation', async ({ page }, testInfo) => {
  test.skip(testInfo.project.name !== 'desktop');
  await page.goto('/settings/providers');
  await page.locator('.settings-center-sidebar').getByRole('link', { name: /提示词/ }).click();
  await page.getByLabel('提示词内容').fill('dirty back content');
  page.once('dialog', (dialog) => dialog.dismiss());
  await page.goBack({ waitUntil: 'commit' }).catch(() => {});
  await expect(page).toHaveURL(/\/settings\/prompts$/);
  page.once('dialog', (dialog) => dialog.accept());
  await page.goBack({ waitUntil: 'commit' });
  await expect(page).toHaveURL(/\/settings\/providers$/);

  await page.locator('.settings-center-sidebar').getByRole('link', { name: /提示词/ }).click();
  await page.locator('.settings-center-sidebar').getByRole('link', { name: '模型 路由与重试', exact: true }).click();
  await page.goBack({ waitUntil: 'commit' });
  await page.getByLabel('提示词内容').fill('dirty forward content');
  page.once('dialog', (dialog) => dialog.dismiss());
  await page.goForward({ waitUntil: 'commit' }).catch(() => {});
  await expect(page).toHaveURL(/\/settings\/prompts$/);
  page.once('dialog', (dialog) => dialog.accept());
  await page.goForward({ waitUntil: 'commit' });
  await expect(page).toHaveURL(/\/settings\/models$/);
});

test('schedule and backend settings use their real API contracts and surface errors', async ({ page }, testInfo) => {
  test.skip(testInfo.project.name !== 'desktop');
  await page.goto('/settings/schedule');
  const schedulePage = page.locator('.settings-section-schedule');
  await expect.poll(() => page.__settingsRequests.some((item) => item.method === 'GET' && item.pathname === '/api/resume-schedule')).toBe(true);
  await schedulePage.getByLabel('每日启动时间', { exact: true }).fill('18:30');
  await schedulePage.getByRole('button', { name: '添加', exact: true }).click();
  await schedulePage.getByRole('button', { name: '统一保存', exact: true }).click();
  await expect.poll(() => page.__settingsRequests.some((item) => item.method === 'PUT' && item.pathname === '/api/resume-schedule' && item.body.daily_times.includes('18:30'))).toBe(true);

  await page.locator('.settings-center-sidebar').getByRole('link', { name: /后端/ }).click();
  const backendPage = page.locator('.settings-section-backend');
  await expect.poll(() => page.__settingsRequests.some((item) => item.method === 'GET' && item.pathname === '/api/projects/project-settings/backend/status')).toBe(true);
  await backendPage.getByRole('button', { name: 'Test', exact: true }).click();
  await expect(backendPage.getByText('连接测试成功')).toBeVisible();
  expect(page.__settingsRequests.some((item) => item.method === 'POST' && item.pathname === '/api/projects/project-settings/backend/test' && JSON.stringify(item.body) === '{}')).toBe(true);

  page.__settingsBackendShouldFail = true;
  await backendPage.getByRole('button', { name: 'Test', exact: true }).click();
  await expect(backendPage.getByRole('alert')).toContainText('后端测试暂不可用');
  page.__settingsBrowserErrors = page.__settingsBrowserErrors.filter((message) => !message.includes('status of 503'));
});

test('mobile settings keep secondary navigation and editor within the viewport', async ({ page }, testInfo) => {
  test.skip(testInfo.project.name !== 'mobile');
  await page.goto('/settings/prompts');
  await expect(page.getByRole('heading', { name: '提示词', exact: true })).toBeVisible();
  await expect(page.getByLabel('配置中心导航')).toBeVisible();
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
  await page.screenshot({ path: path.join(screenshotDirectory, 'mobile-settings.png'), fullPage: true });
});
