import { mkdir } from 'node:fs/promises';
import { resolve } from 'node:path';
import { expect, test } from '@playwright/test';

const acceptanceUrl = process.env.OPENNOVEL_ACCEPTANCE_URL || 'http://127.0.0.1:9999';
const screenshotRoot = resolve('test-results/ui-refactor/phase7');

test.beforeAll(async () => {
  await mkdir(screenshotRoot, { recursive: true });
});

test.beforeEach(async ({ page }) => {
  await page.goto(`${acceptanceUrl}/projects`);
  await expect(page.getByRole('heading', { name: '项目', exact: true })).toBeVisible();
});

test('desktop project center supports a persistent collapsed sidebar', async ({ page }, testInfo) => {
  test.skip(testInfo.project.name !== 'desktop');
  await expect(page.getByRole('complementary', { name: '主导航' })).toBeVisible();
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
  await page.screenshot({ path: resolve(screenshotRoot, 'projects-expanded.png'), fullPage: true });

  await page.getByTitle('收起侧栏').click();
  await expect(page.locator('.global-shell')).toHaveClass(/sidebar-collapsed/);
  await expect.poll(() => page.evaluate(() => localStorage.getItem('opennovel.sidebar.collapsed'))).toBe('true');
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
  await page.screenshot({ path: resolve(screenshotRoot, 'projects-collapsed.png'), fullPage: true });
});

test('mobile project center uses a navigation drawer without horizontal overflow', async ({ page }, testInfo) => {
  test.skip(testInfo.project.name !== 'mobile');
  await page.getByRole('button', { name: '打开主导航' }).click();
  await expect(page.getByRole('complementary', { name: '主导航' })).toBeVisible();
  await expect(page.getByRole('button', { name: '关闭主导航' }).first()).toBeVisible();
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
  await page.screenshot({ path: resolve(screenshotRoot, 'projects-mobile-430x932.png'), fullPage: true });
});

test('desktop opens a real current project workspace without browser errors', async ({ page }, testInfo) => {
  test.skip(testInfo.project.name !== 'desktop');
  const failures = [];
  page.on('console', (message) => {
    if (message.type() === 'error') failures.push(`console: ${message.text()}`);
  });
  page.on('pageerror', (error) => failures.push(`page: ${error.message}`));
  page.on('requestfailed', (request) => {
    const failure = request.failure()?.errorText || 'unknown failure';
    if (failure !== 'net::ERR_ABORTED') failures.push(`request: ${request.url()} (${failure})`);
  });
  page.on('response', (response) => {
    if (response.status() >= 400) failures.push(`response: ${response.url()} (${response.status()})`);
  });

  const response = await page.request.get(`${acceptanceUrl}/api/projects`);
  expect(response.ok()).toBe(true);
  const projects = (await response.json()).projects || [];
  expect(projects.length).toBeGreaterThan(0);
  const project = projects[0];
  await page.goto(`${acceptanceUrl}/projects/${encodeURIComponent(project.id)}/write`);
  await expect(page.getByRole('heading', { name: project.name, exact: true })).toBeVisible();
  await expect(page.locator('.writing-pane')).toBeVisible();
  await expect(page.locator('.status-pane')).toBeVisible();
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
  await page.screenshot({ path: resolve(screenshotRoot, 'workspace-real-current.png'), fullPage: true });
  expect(failures).toEqual([]);
});
