import { expect, test } from '@playwright/test';

test.beforeEach(async ({ page }) => {
  await page.request.post('/api/test/reset');
  await page.goto('/browser-fixture.html?surface=foundation');
  await expect(page.getByRole('heading', { name: '设定中心' })).toBeVisible();
  await expect(page.getByText('项目 A 的目标故事')).toBeVisible();
});

test('原创完整 candidate → 服务端 preview → apply 使用持久化 preview ID 与 idempotency key', async ({ page }) => {
  await page.getByRole('tab', { name: '角色卡' }).click();
  await expect(page.getByLabel('姓名', { exact: true })).toHaveValue('林舟');
  await page.getByRole('tab', { name: '概览' }).click();
  await page.getByRole('tab', { name: '角色卡' }).press('Home');
  await expect(page.getByRole('tab', { name: '概览' })).toBeFocused();
  // Premise editing lives in the target draft through the editor model; add a rule change to dirty the candidate.
  await page.getByRole('tab', { name: '世界规则' }).click();
  await page.getByLabel('规则正文').fill('每次施法都会失去两段记忆');
  const previewRequest = page.waitForRequest((request) => request.url().endsWith('/foundation/preview'));
  await page.getByRole('button', { name: '预览差异与影响' }).click();
  const request = await previewRequest;
  const body = request.postDataJSON();
  expect(body.expected_base_revision).toBe(3);
  expect(body.expected_base_audit_signature).toBe('audit-3');
  expect(body.candidate.characters[0].id).toBe('hero');
  expect(body.candidate.world_rules[0].rule).toContain('两段记忆');
  expect(body).not.toHaveProperty('source_foundation');
  await expect(page.getByText('规范化差异')).toBeVisible();
  const applyRequest = page.waitForRequest((next) => next.url().endsWith('/foundation/apply'));
  await page.getByRole('button', { name: '应用当前 preview ID' }).click();
  const applyBody = (await applyRequest).postDataJSON();
  expect(applyBody.preview_id).toBe('preview-browser');
  expect(applyBody.idempotency_key).toMatch(/^foundation-apply:/);
  expect(Object.keys(applyBody).sort()).toEqual(['idempotency_key', 'preview_id']);
  await expect(page.getByText('打开现有 normal 审核')).toBeVisible();
});

test('改编 SourceFoundation 只读并展示映射、source-fidelity 与 AdaptationPlan 重确认', async ({ page }) => {
  await page.request.post('/api/test/foundation/scenario?value=adaptation');
  await page.reload();
  await expect(page.getByRole('heading', { name: 'SourceFoundation（只读）' })).toBeVisible();
  const sourceCard = page.locator('.source-foundation');
  await expect(sourceCard.getByText('原著世界里旧王归来')).toBeVisible();
  await expect(sourceCard.getByText(/改名保留.*林舟/)).toBeVisible();
  await expect(sourceCard.locator('input, textarea, select')).toHaveCount(0);
  await page.getByRole('tab', { name: '世界规则' }).click();
  await page.getByLabel('规则正文').fill('目标世界能力必须付出代价');
  await page.getByRole('button', { name: '预览差异与影响' }).click();
  await expect(page.getByText('改编影响与 source-fidelity')).toBeVisible();
  await expect(page.getByText('重确认 AdaptationPlan').locator('..')).toContainText('需要');
  await page.getByRole('tab', { name: '角色卡' }).click();
  await page.getByLabel('姓名', { exact: true }).fill('林舟（目标修订）');
  await page.getByRole('button', { name: '预览差异与影响' }).click();
  await expect(page.getByText('影响 CoreCast')).toBeVisible();
  await expect(page.getByText('重确认 CoreCast').locator('..').first()).toContainText('需要');
  await expect(page.getByRole('button', { name: '应用当前 preview ID' })).toBeDisabled();
});

test('仅有来源分析时统一角色卡显示只读来源证据且固定栏不遮挡内容', async ({ page }) => {
  await page.request.post('/api/test/foundation/scenario?value=source-only');
  await page.reload();
  await page.getByRole('tab', { name: '角色卡' }).click();
  await expect(page.getByRole('heading', { name: '原著林舟' })).toBeVisible();
  await expect(page.getByText('这是原著分析得到的只读角色，不是改编目标角色。')).toBeVisible();
  await expect(page.getByRole('heading', { name: '来源映射与证据（只读）' })).toBeVisible();
  const sourceEvidence = page.locator('.source-character-grid article');
  await expect(sourceEvidence.getByText('原著林舟', { exact: true })).toBeVisible();
  await expect(sourceEvidence.getByText('旧王继承人', { exact: true })).toBeVisible();
  await expect(sourceEvidence.getByText('背负流亡王庭的秘密，追查城市灾变。', { exact: true })).toBeVisible();

  const layout = await page.locator('.foundation-center').evaluate((center) => {
    const header = center.querySelector('.foundation-header').getBoundingClientRect();
    const tabs = center.querySelector('.foundation-tabs').getBoundingClientRect();
    const tabButtons = [...center.querySelectorAll('.foundation-tabs button')].map((button) => button.getBoundingClientRect());
    const panel = center.querySelector('.foundation-panel');
    const list = center.querySelector('.character-list-pane');
    const detail = center.querySelector('.character-detail-pane');
    const actions = center.querySelector('.foundation-actions').getBoundingClientRect();
    const panelBox = panel.getBoundingClientRect();
    const listBox = list.getBoundingClientRect();
    const detailBox = detail.getBoundingClientRect();
    const listScrollBefore = list.scrollTop;
    detail.scrollTop = detail.scrollHeight;
    return {
      headerVisible: header.top >= center.getBoundingClientRect().top,
      tabsFullyVisible: tabButtons.every((button) => button.top >= tabs.top && button.bottom <= tabs.bottom),
      panelEndsBeforeActions: panelBox.bottom <= actions.top + 1,
      panesDoNotOverlapActions: listBox.bottom <= actions.top + 1 && detailBox.bottom <= actions.top + 1,
      panelOwnsNoVerticalScroll: getComputedStyle(panel).overflowY === 'hidden',
      detailOwnsVerticalScroll: getComputedStyle(detail).overflowY === 'auto' && detail.scrollTop > 0,
      detailScrollKeepsListStable: list.scrollTop === listScrollBefore
    };
  });
  expect(layout).toEqual({
    headerVisible: true,
    tabsFullyVisible: true,
    panelEndsBeforeActions: true,
    panesDoNotOverlapActions: true,
    panelOwnsNoVerticalScroll: true,
    detailOwnsVerticalScroll: true,
    detailScrollKeepsListStable: true
  });
});

test('角色分析候选按字段接受、审核 finding 定位并在继续编辑后 stale', async ({ page }) => {
  await page.getByRole('tab', { name: '角色卡' }).click();
  await expect(page.getByRole('heading', { name: '角色卡工作台' })).toBeVisible();
  await expect(page.locator('.character-status-badge').filter({ hasText: '完整' }).first()).toBeVisible();
  const analyzeRequest = page.waitForRequest((request) => request.url().endsWith('/foundation/characters/analyze'));
  await page.getByLabel('本轮补充要求（可选）').fill('让语言更可执行');
  await page.getByRole('button', { name: '分析当前角色' }).click();
  const analyzeBody = (await analyzeRequest).postDataJSON();
  expect(analyzeBody.scope.character_ids).toEqual(['hero']);
  expect(analyzeBody.candidate.characters[0].id).toBe('hero');
  expect(analyzeBody.candidate_digest).toMatch(/^[a-f0-9]{64}$/);
  expect(analyzeBody).not.toHaveProperty('source_foundation');
  await expect(page.getByRole('heading', { name: 'Agent 候选比较' })).toBeVisible();
  const voiceCompare = page.locator('.candidate-field-list article').filter({ hasText: '语言风格' });
  await expect(voiceCompare).toContainText('克制、精确');
  const acceptVoice = voiceCompare.getByRole('button', { name: '接受此字段' });
  await page.locator('.character-detail-pane').evaluate((pane) => { pane.scrollTop = pane.scrollHeight; });
  await acceptVoice.click();
  const acceptDialog = page.getByRole('alertdialog', { name: '接受核心角色字段？' });
  await expect(acceptDialog).toBeVisible();
  await acceptDialog.getByRole('button', { name: '接受此字段' }).click();
  await page.getByRole('button', { name: '表演约束' }).click();
  await expect(page.getByLabel('语言风格')).toHaveValue('克制、精确，以短句下判断');

  await page.getByRole('button', { name: '审核全部角色' }).click();
  await expect(page.getByText('语言风格仍不够可执行')).toBeVisible();
  await page.getByRole('button', { name: /警告.*林舟.*语言风格仍不够可执行/s }).click();
  await expect(page.getByLabel('语言风格')).toBeFocused();
  await page.getByLabel('语言风格').fill('短句判断，避免解释性独白');
  await expect(page.getByText(/旧角色审核立即标记为 stale/)).toBeVisible();
});

test('核心角色在统一工作区可编辑并由高风险 preview 门控；离开时保护草稿', async ({ page }) => {
  await page.getByRole('tab', { name: '角色卡' }).click();
  await page.getByLabel('姓名', { exact: true }).fill('林舟（新身份）');
  await page.getByRole('button', { name: '返回创作' }).click();
  await expect(page.getByRole('alertdialog', { name: '离开并保留未发布草稿？' })).toBeVisible();
  await page.getByRole('button', { name: '继续编辑' }).click();
  await expect(page.getByLabel('姓名', { exact: true })).toHaveValue('林舟（新身份）');
  await page.getByRole('button', { name: '预览差异与影响' }).click();
  await expect(page.getByText('影响 CoreCast')).toBeVisible();
  await expect(page.getByRole('button', { name: '应用当前 preview ID' })).toBeDisabled();
});

test('Character Agent 失败可用同一草稿安全重试且不会重复提交候选', async ({ page }) => {
  await page.request.post('/api/test/foundation/scenario?value=character-error');
  await page.reload();
  await page.getByRole('tab', { name: '角色卡' }).click();
  await page.getByRole('button', { name: '分析并补全全部角色' }).click();
  await expect(page.getByText('安全的 Character Agent 测试失败')).toBeVisible();
  const retryRequest = page.waitForRequest((request) => request.url().endsWith('/foundation/characters/retry'));
  await page.getByRole('button', { name: '安全重试' }).click();
  const body = (await retryRequest).postDataJSON();
  expect(body.run_id).toBe('character-analyze-browser');
  expect(body.candidate_digest).toMatch(/^[a-f0-9]{64}$/);
  expect(body).not.toHaveProperty('candidate');
  await expect(page.getByText('角色分析 · 已完成')).toBeVisible();
});

test('stale 409 保留草稿并以服务器新 revision 重新对比', async ({ page }) => {
  await page.request.post('/api/test/foundation/scenario?value=stale');
  await page.reload();
  await page.getByRole('tab', { name: '世界规则' }).click();
  const rule = page.getByLabel('规则正文');
  await rule.fill('用户未保存的 stale 草稿');
  await page.getByRole('button', { name: '预览差异与影响' }).click();
  await expect(page.getByText('服务器基线已变化，草稿仍完整保留。')).toBeVisible();
  await expect(rule).toHaveValue('用户未保存的 stale 草稿');
  await page.getByRole('button', { name: '以最新基线重新对比' }).click();
  await expect(rule).toHaveValue('用户未保存的 stale 草稿');
  await page.getByRole('button', { name: '预览差异与影响' }).click();
  await expect(page.getByText('preview ID：preview-browser')).toBeVisible();
});

test('failed 只调用 retry；active 与正文后 readonly 锁定编辑', async ({ page }) => {
  await page.request.post('/api/test/foundation/scenario?value=failed');
  await page.reload();
  await page.getByRole('tab', { name: '修订状态' }).click();
  const retryRequest = page.waitForRequest((request) => request.url().endsWith('/foundation/retry'));
  await page.getByRole('button', { name: '从持久化安全边界重试' }).click();
  expect((await retryRequest).postDataJSON()).toEqual(expect.objectContaining({ idempotency_key: expect.stringMatching(/^foundation-retry:/) }));
  const lastRequestResponse = await page.request.get('/api/test/foundation/last-request');
  const lastRequest = await lastRequestResponse.json();
  expect(Object.keys(lastRequest.body)).toEqual(['idempotency_key']);

  await page.request.post('/api/test/foundation/scenario?value=active');
  await page.reload();
  await expect(page.getByText('正在重新生成')).toBeVisible();
  await page.getByRole('tab', { name: '世界规则' }).click();
  await expect(page.getByLabel('规则正文')).toBeDisabled();

  await page.request.post('/api/test/foundation/scenario?value=readonly');
  await page.reload();
  await expect(page.getByText('只读原因：body_files_started')).toBeVisible();
  await page.getByRole('tab', { name: '世界规则' }).click();
  await expect(page.getByLabel('规则正文')).toBeDisabled();
});

test('项目切换 fence 丢弃项目 A 的迟到响应，移动端按钮可达', async ({ page }, testInfo) => {
  await page.request.post('/api/test/foundation/delay-project-a');
  await page.reload({ waitUntil: 'domcontentloaded' });
  await page.getByRole('button', { name: '项目 B' }).click();
  await expect(page.getByText('项目 B 的目标故事')).toBeVisible();
  await page.waitForTimeout(550);
  await expect(page.getByText('项目 A 的目标故事')).toHaveCount(0);
  if (testInfo.project.name === 'mobile') {
    await page.getByRole('tab', { name: '世界规则' }).click();
    await expect(page.getByRole('button', { name: '添加规则' })).toBeInViewport();
    expect(await page.locator('body').evaluate((body) => body.scrollWidth <= body.clientWidth + 1)).toBe(true);
  }
});

test('计划关系图谱使用 StoryFoundation 稳定 ID、筛选/布局隔离并让 connect 只进入 preview 草稿', async ({ page }, testInfo) => {
  await page.request.post('/api/test/foundation/scenario?value=graph');
  await page.reload();
  await page.getByRole('tab', { name: '计划关系' }).click();
  if (testInfo.project.name === 'mobile') {
    await expect(page.getByRole('button', { name: '关系列表' })).toHaveAttribute('aria-pressed', 'true');
    await page.getByRole('button', { name: '关系图谱' }).click();
  }
  const canvas = page.getByLabel('StoryFoundation 计划关系图谱');
  await expect(canvas).toHaveAttribute('data-source', 'StoryFoundation.relationships');
  await expect(canvas.locator('.react-flow__node')).toHaveCount(3);
  await expect(canvas).toContainText('引路 · 单向 · 生效');
  await expect(canvas).toContainText('rival · 无向 · 计划');
  await expect(page.getByLabel('图例')).toContainText('边标签同时显示类型、方向和状态');

  const factionFilter = page.locator('.foundation-graph-filters label').filter({ hasText: '阵营' }).locator('select');
  await factionFilter.selectOption('旧王庭');
  await expect(canvas.locator('.react-flow__node')).toHaveCount(1);
  await factionFilter.selectOption('');
  const heroNode = canvas.locator('.react-flow__node[data-id="hero"]');
  await heroNode.click();
  await page.getByRole('button', { name: '聚焦所选角色一跳邻居' }).click();
  await expect(canvas.locator('.react-flow__node')).toHaveCount(2);
  await page.getByRole('button', { name: '取消一跳聚焦' }).click();

  if (testInfo.project.name === 'desktop') {
    await expect(canvas.locator('.react-flow__minimap')).toBeVisible();
    const box = await heroNode.boundingBox();
    await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2);
    await page.mouse.down();
    await page.mouse.move(box.x + box.width / 2 + 35, box.y + box.height / 2 + 20, { steps: 4 });
    await page.mouse.up();
    const stored = await page.evaluate(() => {
      const key = Object.keys(localStorage).find((item) => item.startsWith('foundation-graph-layout:foundation-project-a:'));
      return { key, value: key ? localStorage.getItem(key) : null };
    });
    expect(stored.value).toContain('node_coordinates');
    expect(stored.key).not.toContain('audit-3');
    expect(stored.value).not.toContain('audit-3');
    expect(stored.value).not.toContain('项目 A 的目标故事');
    expect(stored.value).not.toContain('林舟');
  } else {
    await expect(page.getByRole('button', { name: '返回关系列表' })).toBeInViewport();
    expect(await page.locator('body').evaluate((body) => body.scrollWidth <= body.clientWidth + 1)).toBe(true);
    return;
  }

  const allySource = canvas.locator('.react-flow__node[data-id="ally"] .react-flow__handle.source');
  const heroTarget = canvas.locator('.react-flow__node[data-id="hero"] .react-flow__handle.target');
  const sourceBox = await allySource.boundingBox();
  const targetBox = await heroTarget.boundingBox();
  await page.mouse.move(sourceBox.x + sourceBox.width / 2, sourceBox.y + sourceBox.height / 2);
  await page.mouse.down();
  await page.mouse.move(targetBox.x + targetBox.width / 2, targetBox.y + targetBox.height / 2, { steps: 8 });
  await page.mouse.up();
  await expect(page.getByText('有修改', { exact: true })).toBeVisible();
  const previewRequest = page.waitForRequest((request) => request.url().endsWith('/foundation/preview'));
  await page.getByRole('button', { name: '预览差异与影响' }).click();
  const previewBody = (await previewRequest).postDataJSON();
  expect(previewBody.candidate.relationships).toHaveLength(3);
  expect(previewBody.candidate.relationships.at(-1)).toEqual(expect.objectContaining({ source_character_id: 'ally', target_character_id: 'hero', direction: 'directed' }));
  expect(previewBody).not.toHaveProperty('relationship_state');
});
