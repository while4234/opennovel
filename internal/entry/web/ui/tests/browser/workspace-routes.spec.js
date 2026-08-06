import { mkdir } from 'node:fs/promises';
import path from 'node:path';
import { expect, test } from '@playwright/test';

const screenshotDirectory = path.resolve('test-results/ui-refactor/phase7');
const pageFailures = new WeakMap();
const project = {
  id: 'project-workspace',
  name: '雾城来信',
  updated_at: '2026-08-05T12:00:00Z',
  word_count: 42600,
  chapter_count: 18
};

const sections = [
  ['write', '创作'],
  ['manuscript', '稿件'],
  ['foundation', '设定'],
  ['simulation', '仿写'],
  ['adaptation', '改编'],
  ['continuation', '续写'],
  ['audit', '审计'],
  ['export', '导出'],
  ['diagnostics', '诊断'],
  ['settings', '项目设置']
];

const sectionAssertions = {
  write: (page) => expectToolView(page, 'status', '运行状态'),
  manuscript: async (page) => {
    await expect(page.locator('.manuscript-workspace-shell')).toBeVisible();
    await expectToolView(page, 'manuscript', '专业稿件');
  },
  foundation: async (page) => {
    await expect(page.locator('.foundation-center')).toBeVisible();
    await expectToolView(page, 'status', '运行状态');
  },
  simulation: (page) => expectToolView(page, 'simulate', '画像'),
  adaptation: (page) => expectToolView(page, 'adapt', '改编'),
  continuation: (page) => expectToolView(page, 'continuation', '续写'),
  audit: (page) => expectToolView(page, 'audit', '改编审计'),
  export: (page) => expectToolView(page, 'export', '导出'),
  diagnostics: (page) => expectToolView(page, 'diag', '运行诊断'),
  settings: (page) => expectToolView(page, 'settings', '项目设定')
};

async function expectToolView(page, view, title) {
  const pane = page.locator('.status-pane');
  await expect(pane).toHaveAttribute('data-tool-view', view);
  await expect(pane.locator('.project-action-rail-header strong')).toHaveText(title);
}

function snapshotResponse() {
  return { project, snapshot: {}, events: [], latest_event_seq: 0 };
}

function manuscriptTreeResponse() {
  return {
    phase: 'writing',
    mode: 'adaptation',
    structure_revision: 1,
    structure_signature: 'phase6-structure-signature',
    active_revision: { revision_id: 'rev_0123456789abcdef0123456789abcdef', stage: 'audit_pending' },
    nodes: [{
      kind: 'volume',
      stable_id: 'vol_0123456789abcdef0123456789abcdef',
      display_label: '第一卷',
      state: 'planned',
      children: [{
        kind: 'arc',
        stable_id: 'arc_0123456789abcdef0123456789abcdef',
        display_label: '雾城来信',
        state: 'planned',
        children: [{
          kind: 'chapter',
          stable_id: 'ch_0123456789abcdef0123456789abcdef',
          display_order: 1,
          display_label: '第一章 来信',
          state: 'planned',
          has_current: false,
          has_candidate: false,
          has_history: false
        }]
      }]
    }]
  };
}

function manuscriptChapterResponse() {
  return {
    project,
    chapter: {
      stable_id: 'ch_0123456789abcdef0123456789abcdef',
      display_chapter: 1,
      view: 'current',
      version_id: '',
      content_signature: 'phase6-current-signature',
      paragraphs: ['雾从城门缓缓漫入，第一封信落在了旧书桌上。'],
      next_cursor: null,
      total_paragraphs: 1
    }
  };
}

async function expectMobileGeometry(page, projectName) {
  const geometry = await page.evaluate(() => {
    const rectOf = (element) => {
      const rect = element.getBoundingClientRect();
      return { left: rect.left, right: rect.right, top: rect.top, bottom: rect.bottom, width: rect.width, height: rect.height };
    };
    const nav = document.querySelector('.mobile-phone-bottom-nav');
    const composer = document.querySelector('.composer');
    const submit = composer?.querySelector('button[type="submit"]');
    const navRect = rectOf(nav);
    const composerRect = rectOf(composer);
    const submitRect = rectOf(submit);
    const overlapWidth = Math.max(0, Math.min(navRect.right, composerRect.right) - Math.max(navRect.left, composerRect.left));
    const overlapHeight = Math.max(0, Math.min(navRect.bottom, composerRect.bottom) - Math.max(navRect.top, composerRect.top));
    const hit = document.elementFromPoint((submitRect.left + submitRect.right) / 2, (submitRect.top + submitRect.bottom) / 2);
    return {
      viewportWidth: innerWidth,
      nav: navRect,
      composer: composerRect,
      submit: submitRect,
      navButtonWidths: Array.from(nav.querySelectorAll('button'), (button) => button.getBoundingClientRect().width),
      overlapArea: overlapWidth * overlapHeight,
      submitCenterHit: submit.contains(hit)
    };
  });
  console.log('MOBILE_GEOMETRY', projectName, geometry);
  expect(Math.abs(geometry.nav.left)).toBeLessThanOrEqual(1);
  expect(Math.abs(geometry.nav.right - geometry.viewportWidth)).toBeLessThanOrEqual(1);
  expect(Math.abs(geometry.nav.width - geometry.viewportWidth)).toBeLessThanOrEqual(1);
  expect(Math.max(...geometry.navButtonWidths) - Math.min(...geometry.navButtonWidths)).toBeLessThanOrEqual(1);
  expect(geometry.composer.left).toBeGreaterThanOrEqual(0);
  expect(geometry.composer.right).toBeLessThanOrEqual(geometry.viewportWidth + 1);
  expect(geometry.overlapArea).toBe(0);
  expect(geometry.submitCenterHit).toBe(true);
}

test.beforeAll(async () => {
  await mkdir(screenshotDirectory, { recursive: true });
});

test.beforeEach(async ({ page }) => {
  const failures = [];
  pageFailures.set(page, failures);
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
  await page.route('**/api/**', async (route) => {
    const request = route.request();
    const pathname = new URL(request.url()).pathname;
    if (pathname === '/api/setup') return route.fulfill({ json: { setup_required: false, providers: [] } });
    if (pathname === '/api/runtime') return route.fulfill({ json: { version: 'test', context_window: 225000 } });
    if (pathname === '/api/projects' && request.method() === 'GET') return route.fulfill({ json: { projects: [project] } });
    if (pathname === '/api/models') return route.fulfill({ json: { models: { providers: [], routes: {} } } });
    if (pathname === '/api/trash/projects') return route.fulfill({ json: { projects: [] } });
    if (pathname === `/api/projects/${project.id}/snapshot`) return route.fulfill({ json: snapshotResponse() });
    if (pathname === `/api/projects/${project.id}/events`) {
      return route.fulfill({
        status: 200,
        contentType: 'text/event-stream',
        body: 'event: heartbeat\ndata: {}\n\n'
      });
    }
    if (pathname === `/api/projects/${project.id}/manuscript/workspace/tree`) {
      return route.fulfill({ json: manuscriptTreeResponse(), headers: { etag: '"phase6-tree"' } });
    }
    if (pathname.startsWith(`/api/projects/${project.id}/manuscript/workspace/chapters/`) && pathname.endsWith('/content')) {
      return route.fulfill({ json: manuscriptChapterResponse() });
    }
    if (pathname === `/api/projects/${project.id}/foundation`) {
      return route.fulfill({
        json: {
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
            base_audit_signature: 'phase6-audit-signature',
            allowed_operations: ['get', 'preview', 'apply']
          }
        }
      });
    }
    return route.fulfill({ json: {} });
  });
});

test.afterEach(async ({ page }) => {
  expect(pageFailures.get(page) || []).toEqual([]);
});

test('ten workspace deep links activate real project sections', async ({ page }, testInfo) => {
  test.skip(testInfo.project.name !== 'desktop');
  const requestedSnapshots = [];
  page.on('request', (request) => {
    if (new URL(request.url()).pathname === `/api/projects/${project.id}/snapshot`) {
      requestedSnapshots.push(request.url());
    }
  });

  for (const [section, label] of sections) {
    await page.goto(`/projects/${project.id}/${section}`);
    await expect(page.locator('.app-shell')).toHaveAttribute('data-workspace-section', section);
    await expect(page.locator('.project-section-navigation a[aria-current="page"]')).toContainText(label);
    await expect(page.getByRole('heading', { name: project.name, exact: true })).toBeVisible();
    await sectionAssertions[section](page);
  }

  expect(requestedSnapshots.length).toBeGreaterThanOrEqual(1);
});

test('desktop tools use canonical URLs and browser history restores the active tool', async ({ page }, testInfo) => {
  test.skip(testInfo.project.name !== 'desktop');
  await page.goto(`/projects/${project.id}/write`);
  await expect(page.locator('.writing-pane')).toBeVisible();

  await page.locator('.project-section-navigation').getByRole('link', { name: '仿写' }).click();
  await expect(page).toHaveURL(new RegExp(`/projects/${project.id}/simulation$`));
  await expect(page.locator('.app-shell')).toHaveAttribute('data-workspace-section', 'simulation');

  await page.locator('.project-section-navigation').getByRole('link', { name: '改编', exact: true }).click();
  await expect(page).toHaveURL(new RegExp(`/projects/${project.id}/adaptation$`));
  await page.goBack();
  await expect(page).toHaveURL(new RegExp(`/projects/${project.id}/simulation$`));
  await expect(page.locator('.project-section-navigation a[aria-current="page"]')).toContainText('仿写');
  await page.goForward();
  await expect(page).toHaveURL(new RegExp(`/projects/${project.id}/adaptation$`));
  await expect(page.locator('.project-section-navigation a[aria-current="page"]')).toContainText('改编');
});

test('desktop workspace keeps the main canvas and action rail visible', async ({ page }, testInfo) => {
  test.skip(testInfo.project.name !== 'desktop');
  const screenshotSections = ['write', 'manuscript', 'foundation', 'simulation', 'adaptation'];
  for (const section of screenshotSections) {
    const manuscriptTreeLoaded = section === 'manuscript'
      ? page.waitForResponse((response) => response.url().includes('/manuscript/workspace/tree') && response.status() === 200)
      : null;
    await page.goto(`/projects/${project.id}/${section}`);
    if (manuscriptTreeLoaded) await manuscriptTreeLoaded;
    await expect(page.locator('.app-shell')).toHaveAttribute('data-workspace-section', section);
    await expect(page.locator('.status-pane')).toBeVisible();
    await expect(page.getByText('invalid manuscript tree response')).toHaveCount(0);
    await expect(page.getByText('manuscript chapter identity mismatch')).toHaveCount(0);
    if (section === 'manuscript') {
      await expect(page.getByText('雾从城门缓缓漫入，第一封信落在了旧书桌上。')).toBeVisible();
    }
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
    await page.screenshot({ path: path.join(screenshotDirectory, `workspace-${section}.png`), fullPage: true });
  }
  await page.goto(`/projects/${project.id}/write`);
  await page.locator('.status-pane').screenshot({ path: path.join(screenshotDirectory, 'right-rail.png') });
});

test('mobile write, manuscript, and tools routes are mutually exclusive without overflow', async ({ page }, testInfo) => {
  test.skip(!['mobile', 'mobile-landscape'].includes(testInfo.project.name));
  await page.goto(`/projects/${project.id}/write`);
  await expect(page.locator('.app-shell')).toHaveAttribute('data-workspace-section', 'write');
  await expect(page.locator('.writing-pane')).toBeVisible();
  await expect(page.locator('.status-pane')).not.toHaveClass(/mobile-open/);
  await expect(page.locator('.global-mobile-header')).toContainText(project.name);
  await expect(page.locator('.global-mobile-header').getByRole('status', { name: /连接状态/ })).toBeVisible();
  await expectMobileGeometry(page, testInfo.project.name);
  await page.locator('.global-mobile-header').getByRole('button', { name: '打开项目操作' }).click();
  await expect(page.getByRole('dialog', { name: '项目操作' })).toBeVisible();
  await page.getByRole('button', { name: '关闭项目操作' }).click();
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);

  await page.goto(`/projects/${project.id}/manuscript`);
  await expect(page.locator('.app-shell')).toHaveAttribute('data-workspace-section', 'manuscript');
  await expect(page.locator('.manuscript-workspace-shell')).toBeVisible();
  await expect(page.locator('.status-pane')).not.toHaveClass(/mobile-open/);
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);

  await page.goto(`/projects/${project.id}/simulation`);
  await expect(page.locator('.app-shell')).toHaveAttribute('data-workspace-section', 'simulation');
  await expect(page.locator('.status-pane')).toHaveClass(/mobile-open/);
  await expect(page.locator('.status-pane')).toBeVisible();
  expect(await page.evaluate(() => {
    const pane = document.querySelector('.status-pane');
    return pane?.contains(document.elementFromPoint(innerWidth / 2, innerHeight / 2)) === true;
  })).toBe(true);
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);

  if (testInfo.project.name === 'mobile-landscape') {
    await page.screenshot({ path: path.join(screenshotDirectory, 'landscape.png'), fullPage: true });
  } else {
    await page.screenshot({ path: path.join(screenshotDirectory, 'mobile-tools.png'), fullPage: true });
    await page.goto(`/projects/${project.id}/write`);
    await page.screenshot({ path: path.join(screenshotDirectory, 'mobile-write.png'), fullPage: true });
  }
});
