import { mkdir } from 'node:fs/promises';
import path from 'node:path';
import { expect, test } from '@playwright/test';

const screenshotDirectory = path.resolve('test-results/ui-refactor/phase7');
const projects = [
  { id: 'project-recent', name: '雾城来信', last_accessed_at: '2026-08-05T12:00:00Z', word_count: 42600, chapter_count: 18 },
  { id: 'project-older', name: '星港纪事', updated_at: '2026-07-22T09:30:00Z', word_count: 18900, chapter_count: 7 }
];

function snapshotResponse(project) {
  return { project, snapshot: {}, events: [], latest_event_seq: 0 };
}

function foundationResponse(project) {
  return {
    project,
    foundation: {
      mode: 'normal',
      target_foundation: {
        schema_version: 2,
        revision: 1,
        premise: '测试故事',
        characters: [],
        relationships: [],
        relationships_reviewed: false,
        world_rules: []
      },
      editable: true,
      base_revision: 1,
      base_audit_signature: 'test-audit-signature',
      allowed_operations: ['get', 'preview', 'apply']
    }
  };
}

test.beforeAll(async () => {
  await mkdir(screenshotDirectory, { recursive: true });
});

test.beforeEach(async ({ page }) => {
  await page.route('**/api/**', async (route) => {
    const request = route.request();
    const pathname = new URL(request.url()).pathname;
    if (pathname === '/api/setup') return route.fulfill({ json: { setup_required: false, providers: [] } });
    if (pathname === '/api/runtime') return route.fulfill({ json: { version: 'test', context_window: 225000 } });
    if (pathname === '/api/projects' && request.method() === 'GET') return route.fulfill({ json: { projects } });
    if (pathname === '/api/models') return route.fulfill({ json: { models: { providers: [], routes: {} } } });
    if (pathname === '/api/trash/projects') return route.fulfill({ json: { projects: [] } });
    const snapshotMatch = pathname.match(/^\/api\/projects\/([^/]+)\/snapshot$/);
    if (snapshotMatch) {
      const project = projects.find((item) => item.id === snapshotMatch[1]) || { id: snapshotMatch[1], name: snapshotMatch[1] };
      return route.fulfill({ json: snapshotResponse(project) });
    }
    const foundationMatch = pathname.match(/^\/api\/projects\/([^/]+)\/foundation$/);
    if (foundationMatch) {
      const project = projects.find((item) => item.id === foundationMatch[1]) || { id: foundationMatch[1], name: foundationMatch[1] };
      return route.fulfill({ json: foundationResponse(project) });
    }
    return route.fulfill({ json: {} });
  });
});

test('project center supports the expanded and collapsed desktop sidebar', async ({ page }, testInfo) => {
  test.skip(testInfo.project.name !== 'desktop');
  await page.goto('/projects');
  await expect(page.getByRole('heading', { name: '项目', exact: true })).toBeVisible();
  const collection = page.locator('.project-collection');
  await expect(collection.getByText('雾城来信', { exact: true })).toBeVisible();
  await expect(collection.getByText('星港纪事', { exact: true })).toBeVisible();
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);

  await page.screenshot({ path: path.join(screenshotDirectory, 'project-center-expanded.png'), fullPage: true });
  await page.getByTitle('收起侧栏').click();
  await expect(page.locator('.global-shell')).toHaveClass(/sidebar-collapsed/);
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
  await page.screenshot({ path: path.join(screenshotDirectory, 'project-center-collapsed.png'), fullPage: true });
});

test('project center uses a mobile navigation drawer without horizontal overflow', async ({ page }, testInfo) => {
  test.skip(testInfo.project.name !== 'mobile');
  await page.goto('/projects');
  await expect(page.getByRole('heading', { name: '项目', exact: true })).toBeVisible();
  await page.getByRole('button', { name: '打开主导航' }).click();
  await expect(page.getByRole('complementary', { name: '主导航' })).toHaveClass(/mobile-open/);
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
  await page.screenshot({ path: path.join(screenshotDirectory, 'project-center-mobile-430x932.png'), fullPage: true });
});

test('knowledge deep links use an independent project scope while missing workspace routes recover', async ({ page }, testInfo) => {
  test.skip(testInfo.project.name !== 'desktop');
  await page.goto('/characters');
  await expect(page.getByRole('heading', { name: '角色卡', exact: true })).toBeVisible();
  await expect(page.getByLabel('目标项目')).toHaveValue('project-recent');
  await expect(page.locator('.foundation-center')).toBeVisible();
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
  await page.screenshot({ path: path.join(screenshotDirectory, 'project-route-chooser.png'), fullPage: true });

  await page.goto('/projects/missing-project/write');
  await expect(page.getByRole('heading', { name: '项目不存在或无法加载' })).toBeVisible();
  await expect(page.getByRole('button', { name: '重新加载' })).toBeVisible();
  await expect(page.getByRole('button', { name: '返回项目中心' })).toBeVisible();

  await page.getByRole('button', { name: /星港纪事/ }).click();
  await expect(page).toHaveURL(/\/projects\/project-older\/write$/);
  await expect(page.getByRole('heading', { name: '项目不存在或无法加载' })).toBeHidden();
  await expect(page.locator('.writing-pane')).toBeVisible();
});

test('retrying a failed project open calls the project snapshot again and recovers', async ({ page }, testInfo) => {
  test.skip(testInfo.project.name !== 'desktop');
  let snapshotRequests = 0;
  await page.route('**/api/projects/project-recent/snapshot**', async (route) => {
    snapshotRequests += 1;
    if (snapshotRequests === 1) {
      return route.fulfill({ status: 503, json: { error: { code: 'temporary_failure', message: '临时加载失败' } } });
    }
    return route.fulfill({ json: snapshotResponse(projects[0]) });
  });

  await page.goto('/projects/project-recent/write');
  await expect(page.getByRole('heading', { name: '项目不存在或无法加载' })).toBeVisible();
  await expect(page.getByRole('alert')).toContainText('临时加载失败');
  expect(snapshotRequests).toBe(1);

  await page.getByRole('button', { name: '重新加载' }).click();
  await expect.poll(() => snapshotRequests).toBe(2);
  await expect(page.getByRole('heading', { name: '项目不存在或无法加载' })).toBeHidden();
  await expect(page.locator('.writing-pane')).toBeVisible();
});
