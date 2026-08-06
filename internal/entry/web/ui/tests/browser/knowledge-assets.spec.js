import { mkdir } from 'node:fs/promises';
import path from 'node:path';
import { expect, test } from '@playwright/test';

const screenshotDirectory = path.resolve('test-results/ui-refactor/phase7');
const projects = [
  { id: 'knowledge-a', name: '雾城来信', updated_at: '2026-08-05T12:00:00Z' },
  { id: 'knowledge-b', name: '星港纪事', updated_at: '2026-08-04T10:00:00Z' }
];

const foundation = {
  schema_version: 2,
  revision: 4,
  premise: '失忆守夜人在雾城追查时间裂隙。',
  characters: [{
    id: 'hero', name: '林舟', aliases: ['阿舟'], role: '主角', gender: 'male',
    description: '雾城守夜人', arc: '从独行到信任', traits: ['冷静'], tier: 'core',
    faction: '守夜人', goal: '关闭裂隙', motivation: '保护城市', conflict: '记忆不断消失',
    voice: '短句、克制', constraints: ['不伤害无辜'], notes: ''
  }],
  relationships: [],
  relationships_reviewed: true,
  world_rules: [{ id: 'rule-memory', title: '记忆代价', category: '能力', rule: '每次使用能力都会失去一段记忆', boundary: '代价不可被撤销', strength: 'hard', priority: 10, tags: ['代价'] }]
};

function projectFoundation(projectId) {
  if (projectId !== 'knowledge-b') return foundation;
  return {
    ...foundation,
    premise: '星港领航员追查失控的导航信标。',
    characters: [{
      ...foundation.characters[0],
      id: 'navigator',
      name: '星遥',
      role: '领航员',
      description: '星港领航员'
    }]
  };
}

function foundationResponse(projectId) {
  const selectedFoundation = projectFoundation(projectId);
  return {
    project: projects.find((project) => project.id === projectId),
    foundation: {
      mode: 'normal', target_foundation: selectedFoundation, editable: true,
      base_revision: 4, base_audit_signature: 'audit-foundation-4',
      core_cast: { version: 1, members: [{ character: selectedFoundation.characters[0], importance: 'protagonist' }], planned_relationships: [], content_signature: 'core-cast-signature', confirmed_signature: 'core-cast-signature' },
      core_cast_confirmed: true,
      allowed_operations: ['get', 'preview', 'apply']
    }
  };
}

function characterResponse(projectId) {
  const selectedFoundation = projectFoundation(projectId);
  return {
    project: projects.find((project) => project.id === projectId),
    character_workspace: {
      mode: 'original', base_revision: 4, base_audit_signature: 'audit-foundation-4', current_digest: 'a'.repeat(64),
      current: { revision: 4, digest: 'a'.repeat(64), foundation: selectedFoundation },
      completeness: [{ character_id: selectedFoundation.characters[0].id, tier: 'core', status: 'complete', missing: [] }],
      source_mappings: [], findings: [], allowed_operations: ['analyze', 'review']
    }
  };
}

test.beforeAll(async () => mkdir(screenshotDirectory, { recursive: true }));

test.beforeEach(async ({ page }) => {
  page.__knowledgeRequests = [];
  page.__knowledgeErrors = [];
  page.on('console', (message) => { if (message.type() === 'error') page.__knowledgeErrors.push(message.text()); });
  page.on('pageerror', (error) => page.__knowledgeErrors.push(error.message));

  await page.route('**/api/**', async (route) => {
    const request = route.request();
    const url = new URL(request.url());
    const pathname = url.pathname;
    const method = request.method();
    const contentType = request.headers()['content-type'] || '';
    const rawBody = request.postData() || '';
    let body = null;
    if (contentType.includes('application/json') && rawBody) body = JSON.parse(rawBody);
    page.__knowledgeRequests.push({ method, pathname, search: url.search, contentType, rawBody, body });

    if (pathname === '/api/setup') return route.fulfill({ json: { setup_required: false, providers: [] } });
    if (pathname === '/api/runtime') return route.fulfill({ json: { version: 'phase5', context_window: 225000 } });
    if (pathname === '/api/projects' && method === 'GET') return route.fulfill({ json: { projects } });
    if (pathname === '/api/models') return route.fulfill({ json: { models: { providers: [], routes: {} } } });
    if (pathname === '/api/styles') return route.fulfill({ json: { styles: [{ id: 'default', label: '默认' }], default_style: 'default' } });
    if (pathname === '/api/resume-schedule') return route.fulfill({ json: { schedule: { daily_times: [], timezone: 'Asia/Shanghai' } } });
    if (pathname === '/api/trash/projects') return route.fulfill({ json: { projects: [] } });
    if (pathname === '/api/libraries/novels' && method === 'GET') return route.fulfill({ json: { items: [{ name: '旧城夜行', chapter_count: 28, size: 98304, updated_at: '2026-08-02T08:00:00Z' }] } });
    if (pathname === '/api/libraries/simulation' && method === 'GET') return route.fulfill({ json: { items: [{ name: '冷峻悬疑', source_count: 6, size: 4096, health_state: 'healthy', updated_at: '2026-08-01T08:00:00Z' }] } });
    if (pathname === '/api/libraries/simulation/upload' && method === 'POST') return route.fulfill({ json: { items: [], message: '画像上传成功' } });
    if (pathname === '/api/observability/usage') return route.fulfill({ json: { groups: [{ key: 'openai/gpt-5.6', calls: 12, input_tokens: 24000, output_tokens: 4200, cache_read_tokens: 8000, cost_usd: 0.42, coverage: 1, failure_rate: 0, retry_rate: 0.08, cache_capable: true }], trend: [{ date: '2026-08-04', input_tokens: 9000, cost_usd: 0.16 }, { date: '2026-08-05', input_tokens: 15000, cost_usd: 0.26 }] } });
    if (pathname === '/api/observability/recommendations') return route.fulfill({ json: { recommendations: [{ id: 'cache', model: 'gpt-5.6', evidence: '长上下文重复率较高', action: '保留稳定前缀以提高缓存命中' }] } });

    const foundationMatch = pathname.match(/^\/api\/projects\/([^/]+)\/foundation$/);
    if (foundationMatch && method === 'GET') {
      if (page.__delayKnowledgeA && foundationMatch[1] === 'knowledge-a') {
        await new Promise((resolve) => setTimeout(resolve, 350));
      }
      return route.fulfill({ json: foundationResponse(foundationMatch[1]) });
    }
    const charactersMatch = pathname.match(/^\/api\/projects\/([^/]+)\/foundation\/characters(?:\/(analyze|review|retry|discard))?$/);
    if (charactersMatch) return route.fulfill({ status: charactersMatch[2] ? 202 : 200, json: characterResponse(charactersMatch[1]) });
    const previewMatch = pathname.match(/^\/api\/projects\/([^/]+)\/foundation\/preview$/);
    if (previewMatch && method === 'POST') return route.fulfill({ json: { preview: { version: 1, id: 'phase5-preview', base_revision: 4, candidate: body.candidate, candidate_signature: 'candidate-signature', diff: { changes: [{ entity_type: 'world_rule', entity_id: 'rule-memory', kind: 'modified', changed_fields: ['rule'], high_risk: true }] }, impact: { evidence_level: 'structured', full_book: true, reasons: [{ code: 'hard_rule_changed', required: true, entity_ids: ['rule-memory'] }], required_audits: [{ scope: 'book', scope_id: 'all', required: true }], requires_foundation_confirmation: true }, validation: { valid: true, errors: [], warnings: [] }, can_apply: true } } });
    const applyMatch = pathname.match(/^\/api\/projects\/([^/]+)\/foundation\/apply$/);
    if (applyMatch && method === 'POST') return route.fulfill({ json: { revision: { revision_id: 'foundation-revision-5', preview_id: body.preview_id, stage: 'auditing', generation: 5 } } });
    if (/^\/api\/projects\/[^/]+\/adapt\/library\/load$/.test(pathname)) return route.fulfill({ json: { message: '小说已加载', source_file: { name: '旧城夜行.txt', relative_path: 'adapt/旧城夜行.txt' }, adaptation: { analysis_status: 'done' }, analyzed: true } });
    if (/^\/api\/projects\/[^/]+\/adapt\/source$/.test(pathname)) return route.fulfill({ json: { message: '来源已上传', source_file: { name: 'source.txt', relative_path: 'adapt/source.txt' } } });
    if (/^\/api\/projects\/[^/]+\/import$/.test(pathname)) return route.fulfill({ json: { message: '小说已导入' } });
    if (/^\/api\/projects\/[^/]+\/simulate\/library\/load$/.test(pathname)) return route.fulfill({ json: { message: '画像已加载', accepted: true, running: true } });
    if (/^\/api\/projects\/[^/]+\/simulate\/library\/save$/.test(pathname)) return route.fulfill({ json: { message: '画像已保存' } });
    if (/^\/api\/projects\/[^/]+\/simulate\/import$/.test(pathname)) return route.fulfill({ json: { message: '画像已导入' } });
    return route.fulfill({ json: {} });
  });
});

test.afterEach(async ({ page }) => {
  expect(page.__knowledgeErrors, 'knowledge pages should not emit browser errors').toEqual([]);
});

test('characters use an independent project scope and the real agent contract', async ({ page }, testInfo) => {
  test.skip(testInfo.project.name !== 'desktop');
  await page.goto('/characters');
  await expect(page.getByRole('heading', { name: '角色卡', exact: true })).toBeVisible();
  await expect(page.getByRole('heading', { name: '林舟', exact: true })).toBeVisible();
  const agentToggle = page.getByRole('button', { name: /Character Agent/ });
  if (await agentToggle.getAttribute('aria-expanded') !== 'true') await agentToggle.click();
  await page.getByLabel('本轮补充要求（可选）').fill('强化角色的记忆代价');
  await page.getByRole('button', { name: /分析并补全全部角色/ }).click();
  await expect.poll(() => page.__knowledgeRequests.find((item) => item.method === 'POST' && item.pathname === '/api/projects/knowledge-a/foundation/characters/analyze')).toBeTruthy();
  const request = page.__knowledgeRequests.find((item) => item.pathname.endsWith('/foundation/characters/analyze'));
  expect(request.body).toMatchObject({ expected_base_revision: 4, expected_base_audit_signature: 'audit-foundation-4', instruction: '强化角色的记忆代价', scope: { character_ids: [] } });
  expect(request.body.idempotency_key).toMatch(/^foundation-character-analyze:/);
  await expect(page.locator('.compatibility-workspace')).toBeHidden();
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
  await page.screenshot({ path: path.join(screenshotDirectory, 'characters.png'), fullPage: true });
});

test('switching knowledge scope clears the old project and fences a late response', async ({ page }, testInfo) => {
  test.skip(testInfo.project.name !== 'desktop');
  page.__delayKnowledgeA = true;
  await page.goto('/characters');
  const selector = page.getByLabel('目标项目');
  await expect(selector).toBeVisible();
  await selector.selectOption('knowledge-b');
  await expect(page.getByRole('heading', { name: '星遥', exact: true })).toBeVisible();
  await expect(page.getByRole('heading', { name: '林舟', exact: true })).toHaveCount(0);
  await page.waitForTimeout(450);
  await expect(page.getByRole('heading', { name: '星遥', exact: true })).toBeVisible();
  await expect(page.getByRole('heading', { name: '林舟', exact: true })).toHaveCount(0);
  await page.screenshot({ path: path.join(screenshotDirectory, 'characters-project-switch.png'), fullPage: true });
});

test('worldbook previews and applies the exact Foundation revision contract', async ({ page }, testInfo) => {
  test.skip(testInfo.project.name !== 'desktop');
  await page.goto('/worldbook');
  await expect(page.getByRole('heading', { name: '世界书', exact: true })).toBeVisible();
  const rule = page.getByLabel('规则正文');
  await rule.fill('每次使用能力都会永久失去一段记忆');
  await page.getByRole('button', { name: '预览差异与影响' }).click();
  await expect(page.getByRole('heading', { name: '差异与影响预览' })).toBeVisible();
  const preview = page.__knowledgeRequests.find((item) => item.pathname.endsWith('/foundation/preview'));
  expect(preview.body.expected_base_revision).toBe(4);
  expect(preview.body.expected_base_audit_signature).toBe('audit-foundation-4');
  expect(preview.body.candidate.world_rules[0].rule).toContain('永久失去');
  await page.getByRole('button', { name: '应用当前 preview ID' }).click();
  await expect.poll(() => page.__knowledgeRequests.find((item) => item.pathname.endsWith('/foundation/apply'))).toBeTruthy();
  const apply = page.__knowledgeRequests.find((item) => item.pathname.endsWith('/foundation/apply'));
  expect(apply.body.preview_id).toBe('phase5-preview');
  expect(apply.body.idempotency_key).toMatch(/^foundation-apply:/);
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
  await page.screenshot({ path: path.join(screenshotDirectory, 'worldbook.png'), fullPage: true });
});

test('novel and profile libraries retain load and multipart contracts', async ({ page }, testInfo) => {
  test.skip(testInfo.project.name !== 'desktop');
  await page.goto('/libraries/novels');
  await expect(page.getByRole('heading', { name: '小说仓库', exact: true })).toBeVisible();
  await page.getByRole('button', { name: '加载到项目' }).click();
  await expect.poll(() => page.__knowledgeRequests.some((item) => item.pathname === '/api/projects/knowledge-a/adapt/library/load' && item.body?.name === '旧城夜行')).toBe(true);
  await page.getByTitle('上传来源文件到所选项目').locator('input').setInputFiles({ name: 'source.txt', mimeType: 'text/plain', buffer: Buffer.from('novel') });
  await expect.poll(() => page.__knowledgeRequests.some((item) => item.pathname === '/api/projects/knowledge-a/adapt/source' && item.contentType.includes('multipart/form-data') && item.rawBody.includes('name="source"'))).toBe(true);
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
  await page.screenshot({ path: path.join(screenshotDirectory, 'novel-library.png'), fullPage: true });

  await page.goto('/libraries/profiles');
  await expect(page.getByRole('heading', { name: '仿写画像库', exact: true })).toBeVisible();
  await page.getByTitle('上传可移植画像到全局库').locator('input').setInputFiles({ name: 'voice.json', mimeType: 'application/json', buffer: Buffer.from('{}') });
  await expect.poll(() => page.__knowledgeRequests.some((item) => item.pathname === '/api/libraries/simulation/upload' && item.rawBody.includes('name="files"'))).toBe(true);
  await page.getByLabel('画像名称').fill('当前画像');
  await page.getByRole('button', { name: '保存画像' }).click();
  await expect.poll(() => page.__knowledgeRequests.some((item) => item.pathname === '/api/projects/knowledge-a/simulate/library/save' && item.body?.name === '当前画像')).toBe(true);
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
  await page.screenshot({ path: path.join(screenshotDirectory, 'profile-library.png'), fullPage: true });
});

test('dashboard uses both observability GET endpoints and renders backend trend data', async ({ page }, testInfo) => {
  test.skip(testInfo.project.name !== 'desktop');
  await page.goto('/dashboard');
  await expect(page.getByRole('heading', { name: '仪表盘', exact: true })).toBeVisible();
  await expect(page.getByText('openai/gpt-5.6')).toBeVisible();
  await expect(page.getByText('长上下文重复率较高')).toBeVisible();
  expect(page.__knowledgeRequests.some((item) => item.method === 'GET' && item.pathname === '/api/observability/usage' && item.search.includes('group_by=model'))).toBe(true);
  expect(page.__knowledgeRequests.some((item) => item.method === 'GET' && item.pathname === '/api/observability/recommendations')).toBe(true);
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
  await page.screenshot({ path: path.join(screenshotDirectory, 'dashboard.png'), fullPage: true });
});

test('mobile characters collapse to one column without horizontal overflow', async ({ page }, testInfo) => {
  test.skip(testInfo.project.name !== 'mobile');
  await page.goto('/characters');
  await expect(page.getByRole('heading', { name: '角色卡', exact: true })).toBeVisible();
  await expect(page.getByRole('heading', { name: '林舟', exact: true })).toBeVisible();
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
  await page.screenshot({ path: path.join(screenshotDirectory, 'mobile-characters.png'), fullPage: true });
});
