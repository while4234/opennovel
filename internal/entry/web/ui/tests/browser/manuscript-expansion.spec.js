import { expect, test } from '@playwright/test';

test.setTimeout(120000);

async function clickLive(locator) {
  await expect(locator).toBeVisible();
  await locator.evaluate((element) => element.click());
}

async function forceVisibleAuditFailure(page, region, keyPrefix) {
  const active = await page.request.get('/api/projects/browser-project/manuscript/expansion/revision').then((response) => response.json());
  await clickLive(region.getByRole('button', { name: '发起独立审核' }));
	// A real deterministic model candidate contains a continuity violation;
	// no test endpoint mutates the audit task or decision.
  const findings = region.getByRole('alert').filter({ hasText: /continuity|terminal|contradiction|因果|连续/i });
  await expect(findings).toBeVisible();
  await expect(region.getByText('当前状态：candidate_audit_pending')).toBeVisible();
  const blocked = await page.request.post('/api/projects/browser-project/manuscript/expansion/revision/command', {
    data: { action: 'publish', expected_revision: active.revision.revision, idempotency_key: `${keyPrefix}-blocked-publish` },
  });
  expect(blocked.ok()).toBeFalsy();
  await region.getByLabel('审核意见').fill('修复审查发现并重新生成受影响候选');
  await region.getByRole('button', { name: '提交定向反馈' }).click();
  await expect(region.getByText('当前状态：candidate_generating')).toBeVisible();
}

async function finishRevisionThroughUI(page, region) {
  for (let step = 0; step < 20; step += 1) {
    const text = await region.textContent();
    if (text.includes('当前状态：ready_to_publish')) return;
    if (text.includes('当前状态：candidate_audit_pending')) {
      await clickLive(region.getByRole('button', { name: '发起独立审核' }));
      await expect.poll(async () => (await page.request.get('/api/projects/browser-project/manuscript/expansion/revision').then((response) => response.json())).revision.stage).toBe('approval_pending');
      await expect(region.getByText('当前状态：approval_pending')).toBeVisible();
      continue;
    }
    if (text.includes('当前状态：approval_pending')) {
      await clickLive(region.getByRole('button', { name: '人工确认当前阶段' }));
      await expect.poll(() => region.textContent()).not.toContain('当前状态：approval_pending');
      continue;
    }
    if (text.includes('当前状态：candidate_generating')) {
      for (const name of ['提交结构候选', '提交提纲候选', '提交正文返工候选']) {
        const button = region.getByRole('button', { name });
        if (await button.count()) { await clickLive(button); break; }
      }
      await expect(region.getByText('当前状态：candidate_audit_pending')).toBeVisible();
      continue;
    }
    throw new Error(`unexpected visible revision state: ${text}`);
  }
  throw new Error('visible revision flow exceeded bounded steps');
}

test.beforeEach(async ({ page }, testInfo) => {
  const adaptationMode = testInfo.title.includes('production Go adaptation') ? '?mode=adaptation' : '';
  await page.request.post(`/api/test/reset-expansion${adaptationMode}`);
  await page.request.post('/api/test/reset');
  await page.request.post('/api/test/refresh-expansion-metadata');
  await page.goto('/browser-fixture.html');
});

async function planExpansionInManuscript(page, sentence) {
  await page.getByRole('tab', { name: '补剧情 / 扩写' }).click();
  const form = page.locator('.manuscript-action-compose .expansion-form');
  await form.getByLabel('一句话描述').fill(sentence);
  await form.getByRole('button', { name: '提交意见' }).click();
  await page.getByRole('button', { name: '生成签名预览' }).click();
  return page.getByRole('region', { name: '一句话补剧情与扩写' });
}

test('一句话扩写支持极简表单、专业预览、快捷调整、取消和键盘可访问性', async ({ page }, testInfo) => {
  await page.getByRole('tab', { name: '补剧情 / 扩写' }).click();
  const form = page.locator('.manuscript-action-compose .expansion-form');
  await expect(form.getByText('改编模式：会校验原著覆盖与受保护合同。')).toBeVisible();
  await expect(form.locator('details').first()).not.toHaveAttribute('open', '');
  const description = form.getByLabel('一句话描述');
  await description.focus();
  await expect(description).toBeFocused();
  await description.fill('让盟友隐瞒的证据迫使主角公开站队');
  await form.getByRole('button', { name: '提交意见' }).click();
  await page.getByRole('button', { name: '生成签名预览' }).click();
  const region = page.getByRole('region', { name: '一句话补剧情与扩写' });
  await expect(region.getByRole('heading', { name: '插入一章' })).toBeVisible();
  await expect(region.getByText('独立的选择与代价需要结构空间')).toBeVisible();
  await expect(region.getByText(/目标第 2 章/)).toBeVisible();
  await region.getByRole('button', { name: '更充分' }).click();
  await expect(region.getByRole('heading', { name: '增加若干章' })).toBeVisible();
  await region.getByRole('button', { name: '取消预览' }).click();
  await expect(region.getByRole('button', { name: '确认影响并进入固定修订' })).toBeDisabled();
  if (testInfo.project.name === 'mobile') {
    const box = await region.boundingBox();
    expect(box.width).toBeLessThanOrEqual(430);
  }
});

test('生产 Go 确认拒绝客户端自签审核并完成独立审核、人工确认和原子发布', async ({ page }) => {
  const region = await planExpansionInManuscript(page, '让盟友隐瞒的证据迫使主角公开站队');
  await expect(region.getByRole('heading', { name: '插入一章' })).toBeVisible();
  await clickLive(region.getByRole('button', { name: '确认影响并进入固定修订' }));
  await expect(region.getByRole('heading', { name: '修订进度' })).toBeVisible();
  await expect(region.getByText('当前状态：candidate_audit_pending')).toBeVisible();
	const active = await page.request.get('/api/projects/browser-project/manuscript/expansion/revision').then((response) => response.json());
	const forged = await page.request.post('/api/projects/browser-project/manuscript/expansion/revision/command', { data: { action: 'audit', expected_revision: active.revision.revision, idempotency_key: 'forged-browser-audit' } });
	expect(forged.status()).toBe(400);
	await expect(region.getByText('当前状态：candidate_audit_pending')).toBeVisible();
  await forceVisibleAuditFailure(page, region, 'normal-visible-failure');
  await finishRevisionThroughUI(page, region);
  await expect(region.getByText('当前状态：ready_to_publish')).toBeVisible();
  await region.getByRole('button', { name: '后处理并原子发布' }).click();
  await expect(region.getByText('当前状态：completed')).toBeVisible();
});

test('安全 API 合同覆盖 restart、two-tab、stale 与 expiry', async ({ page }) => {
	const metadata = await page.request.get('/api/test/expansion-metadata').then((response) => response.json());
  const plan = (key) => page.request.post('/api/projects/browser-project/manuscript/expansion/plan', { data: { location: 'after', reference_ids: ['ch_0123456789abcdef0123456789abcdef'], sentence: '安全恢复', adjustment: 'default', expected_structure_revision: metadata.structure_revision, expected_structure_signature: metadata.structure_signature, idempotency_key: key } });
  const planned = await plan('browser-restart-plan');
  expect(planned.ok()).toBeTruthy();
  const preview = (await planned.json()).preview;
  await page.request.post('/api/test/restart-expansion');
  const first = await page.request.post('/api/projects/browser-project/manuscript/expansion/confirm', { data: { preview_id: preview.preview_id, expected_revision: metadata.structure_revision, idempotency_key: 'tab-a' } });
  const second = await page.request.post('/api/projects/browser-project/manuscript/expansion/confirm', { data: { preview_id: preview.preview_id, expected_revision: metadata.structure_revision, idempotency_key: 'tab-b' } });
  expect(first.ok()).toBeTruthy(); expect(second.ok()).toBeTruthy();
  expect((await first.json()).confirmation.revision.revision_id).toBe((await second.json()).confirmation.revision.revision_id);
	const stale = await page.request.post('/api/projects/browser-project/manuscript/expansion/confirm', { data: { preview_id: preview.preview_id, expected_revision: metadata.structure_revision + 1, idempotency_key: 'tab-stale-new-key' } });
	expect(stale.status()).toBe(409);
  await page.request.post('/api/test/reset-expansion');
  const expiring = await plan('browser-expiry-plan');
  const expiringPreview = (await expiring.json()).preview;
  await page.request.post('/api/test/expire-expansion');
  const expired = await page.request.post('/api/projects/browser-project/manuscript/expansion/confirm', { data: { preview_id: expiringPreview.preview_id, expected_revision: metadata.structure_revision, idempotency_key: 'expired' } });
  expect(expired.status()).toBe(409);
  expect((await expired.json()).error.code).toBe('preview_stale');
});

test('完本项目提供继续扩写入口', async ({ page }) => {
  await page.request.post('/api/test/phase-complete');
  await page.reload();
  await expect(page.getByRole('tab', { name: '补剧情 / 扩写' })).toBeVisible();
});

test('章节间入口会带入稳定位置且不会暴露签名、事件账本或 source ID', async ({ page }) => {
  await page.getByRole('button', { name: '打开完整目录' }).click();
  const surface = page.getByRole('dialog', { name: '稿件目录抽屉' });
  await surface.getByRole('button', { name: /在第 1 章后补充剧情/ }).click();
  await surface.getByRole('button', { name: '关闭目录' }).click();
  const form = page.locator('.manuscript-action-compose .expansion-form');
  await expect(form.getByLabel('插入位置')).toHaveValue('after');
  await expect(form).not.toContainText('browser-structure-signature');
  await expect(form).not.toContainText('event ledger');
  await expect(form).not.toContainText('source_id');
});

test('production Go adaptation project preserves source contracts through audit, human gates, and publish', async ({ page }) => {
  await page.getByRole('tab', { name: '补剧情 / 扩写' }).click();
  await expect(page.locator('.manuscript-action-compose .expansion-form').getByText('改编模式：会校验原著覆盖与受保护合同。')).toBeVisible();
  const region = await planExpansionInManuscript(page, 'add an original bridge without replacing protected source events');
  await expect(region.getByText('目标第 3 章 · 无原著章映射 · 新增剧情', { exact: true })).toBeVisible();
  await clickLive(region.getByRole('button', { name: '确认影响并进入固定修订' }));
  await expect(region.getByText('当前状态：candidate_audit_pending')).toBeVisible();
  await forceVisibleAuditFailure(page, region, 'adaptation-visible-failure');
  await finishRevisionThroughUI(page, region);
  await region.getByRole('button', { name: '后处理并原子发布' }).click();
  await expect(region.getByText('当前状态：completed')).toBeVisible();
  const contract = await page.request.get('/api/test/adaptation-contract').then((response) => response.json());
  expect(contract.mode).toBe('adaptation');
  expect(contract.source_chapter_count).toBe(2);
  expect(contract.target_chapter_count).toBe(3);
  expect(contract.last_target_display).toBe(3);
  expect(contract.last_is_added).toBe(true);
	expect(contract.last_source_chapters ?? []).toEqual([]);
});
