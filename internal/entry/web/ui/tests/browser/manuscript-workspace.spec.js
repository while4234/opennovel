import { expect, test } from '@playwright/test';

test.beforeEach(async ({ page }) => {
  await page.request.post('/api/test/reset');
  await page.goto('/browser-fixture.html');
  await expect(page.getByText('当前：第1段', { exact: false })).toBeVisible();
});

async function openTree(page) {
  await page.getByRole('button', { name: '打开完整目录' }).click();
  return page.getByRole('dialog', { name: '稿件目录抽屉' });
}

test('真实 API 支持分层树、键盘 tabs、candidate、懒审核和十万字窗口', async ({ page }) => {
  const treeSurface = await openTree(page);
  await expect(treeSurface.getByRole('tree')).toHaveAttribute('aria-label', '卷、故事弧与章节');
  await expect(page.getByRole('treeitem', { name: /第一卷/ })).toHaveAttribute('aria-expanded', 'true');
  await expect(page.getByRole('treeitem', { name: /第一故事弧/ })).toHaveAttribute('aria-expanded', 'true');
  await expect(page.getByText('目标第 1 章')).toBeVisible();
  await expect(page.getByText('原著第 7–8 章')).toBeVisible();
  await expect(page.getByText('候选：第1段', { exact: false })).toHaveCount(0);
  const firstTreeItem = treeSurface.locator('[data-tree-index="0"]');
  await firstTreeItem.press('End');
  await expect(treeSurface.locator('[data-tree-index="131"]')).toBeFocused();
  await page.keyboard.press('Home');
  await expect(firstTreeItem).toBeFocused();
  await page.getByRole('button', { name: '关闭目录' }).click();
  await page.getByRole('button', { name: '候选稿' }).click();
  await expect(page.getByText('候选：第1段', { exact: false })).toBeVisible();
  await page.getByRole('button', { name: '正式稿' }).click();

  const proseTab = page.getByRole('tab', { name: '正文' });
  await proseTab.press('ArrowRight');
  await expect(page.getByRole('tab', { name: '章节提纲' })).toBeFocused();
  await expect(page.getByText('完成真实验收')).toBeVisible();
  await page.getByRole('tab', { name: '审核' }).click();
	await expect(page.getByText('修订记录：20')).toBeVisible();
	for (let pageIndex = 0; pageIndex < 6; pageIndex += 1) await page.getByRole('button', { name: '加载更多审核记录' }).click();
	await expect(page.getByText('修订记录：126')).toBeVisible();
  await expect(page.getByText('真实延迟加载审核报告')).toHaveCount(0);
	await page.getByRole('button', { name: '加载审核报告与发现' }).last().click();
	await expect(page.getByText('第 101+ 条真实审核详情')).toBeVisible();

  await page.getByRole('tab', { name: '正文' }).click();
  await expect(page.getByText('已加载 240 / 240 段').first()).toBeVisible();
  expect(await page.locator('.manuscript-reader').first().locator('p').count()).toBeLessThanOrEqual(122);
  const totalRunes = await page.evaluate(async () => {
    let cursor = 0, total = 0;
    do {
      const response = await fetch(`/api/projects/browser-project/manuscript/workspace/chapters/ch_0123456789abcdef0123456789abcdef/content?view=current&cursor=${cursor}&limit=100`);
      const body = await response.json(); total += body.chapter.paragraphs.join('').length; cursor = body.chapter.next_cursor;
    } while (cursor != null);
    return total;
  });
  expect(totalRunes).toBeGreaterThan(100000);
});

test('真实 history 分页、restore preview/确认可用且讨论跳转已移除', async ({ page }) => {
  await page.getByRole('tab', { name: '修订历史' }).click();
  await page.getByRole('button', { name: '加载更多历史' }).click();
  await expect(page.getByText(/2026-07-15/)).toBeVisible();
  await page.getByRole('button', { name: /2026-07-16/ }).click();
  await expect(page.getByText(/^历史正式正文1：/)).toBeVisible();
  for (let index = 0; index < 5; index += 1) await page.getByRole('button', { name: '继续加载' }).click();
  await expect(page.getByText('已加载 240 / 240 段', { exact: false })).toBeVisible();
  await page.getByRole('button', { name: '下一窗口' }).click();
  await expect(page.getByText(/^历史正式正文240：/)).toBeVisible();
  expect(await page.locator('.manuscript-reader').last().locator('p').count()).toBeLessThanOrEqual(122);
  await page.getByRole('button', { name: '预览恢复影响' }).click();
  await expect(page.getByText('创建新的 audit_pending 修订；不覆盖当前正式稿')).toBeVisible();
  await page.getByRole('button', { name: '确认并新建修订' }).click();
  await expect(page.getByText('已从历史版本创建新的修订；当前正式稿未被覆盖，仍需独立审核与确认。')).toBeVisible();

  await expect(page.getByRole('button', { name: '带当前上下文去讨论' })).toHaveCount(0);
  await expect(page.getByRole('tab', { name: '润色' })).toBeVisible();
});

test('桌面正文约 920px，章节组合框支持三种章号格式并在稿件区原地追问', async ({ page }, testInfo) => {
  test.skip(testInfo.project.name !== 'desktop');
  const width = await page.locator('.manuscript-reader').first().evaluate((element) => element.getBoundingClientRect().width);
  expect(width).toBeGreaterThanOrEqual(900);
  expect(width).toBeLessThanOrEqual(925);

  const combobox = page.getByRole('combobox', { name: '选择章节' });
  for (const value of ['2', '第2章', '第二章']) {
    await combobox.click();
    const search = page.getByRole('searchbox', { name: '搜索章节' });
    await search.fill(value);
    await expect(page.getByRole('option', { name: /第 2 章/ })).toBeVisible();
    await search.press('Enter');
    await expect(page.getByRole('heading', { name: /第 2 章/ })).toBeVisible();
  }
  await combobox.click();
  const search = page.getByRole('searchbox', { name: '搜索章节' });
  await search.fill('999');
  await expect(page.getByText('未找到匹配章节')).toBeVisible();
  await search.press('Enter');
  await expect(page.getByRole('heading', { name: /第 2 章/ })).toBeVisible();

  await page.getByRole('tab', { name: '润色' }).click();
  await page.getByLabel('修改意见').fill('加强冲突，但范围不明确');
  await page.getByRole('button', { name: '提交意见' }).click();
  const clarification = page.getByLabel('只修改冲突场景，还是整章？');
  await expect(clarification).toBeVisible();
  await clarification.fill('只修改冲突场景');
  await page.getByRole('button', { name: '回答' }).click();
  await expect(page.getByText('意见已明确，可以生成安全预览')).toBeVisible();
  await expect(page.locator('.manuscript-workspace-shell')).toBeVisible();
});

test('历史正文被清理时保留当前稿并提供重新加载动作', async ({ page }) => {
	await page.getByRole('tab', { name: '修订历史' }).click();
	await page.getByRole('button', { name: /2026-07-16/ }).click();
	await expect(page.getByText(/^历史正式正文1：/)).toBeVisible();
	await page.request.post('/api/test/tombstone-history');
	await page.getByRole('button', { name: '继续加载' }).click();
	await expect(page.getByText('历史版本已被清理', { exact: false })).toBeVisible();
	await expect(page.getByRole('button', { name: '重新加载历史' })).toBeVisible();
	await expect(page.getByText('历史版本正文')).toHaveCount(0);
	await page.getByRole('tab', { name: '正文' }).click();
	await expect(page.getByText('当前：第1段', { exact: false })).toBeVisible();
});

test('延迟的旧章节 history 响应不会覆盖快速切换后的选择', async ({ page }, testInfo) => {
	await page.request.post('/api/test/delay-next-history');
	await page.getByRole('tab', { name: '修订历史' }).click();
	const treeSurface = await openTree(page);
	await treeSurface.locator('[data-tree-index="3"]').click();
	await page.waitForTimeout(500);
	await expect(page.locator('[data-tree-index="3"]')).toHaveAttribute('aria-selected', 'true');
	await expect(page.getByText(/2026-07-16/)).toHaveCount(0);
});

for (const [sourceKind, failTree] of [['local', false], ['local', true], ['sse', false], ['sse', true]]) {
  test(`延迟 ${sourceKind} mutation tree ${failTree ? '失败' : '成功'}不会重选旧章或覆盖新章`, async ({ page }) => {
    await page.request.post(`/api/test/delay-next-tree?fail=${failTree ? 1 : 0}`);
    await page.request.post('/api/test/delay-next-chapter');
    const refreshStarted = page.waitForRequest((request) => request.url().includes('/manuscript/workspace/tree'));
    if (sourceKind === 'sse') {
      await page.request.post('/api/test/emit-mutation');
    } else {
      await page.evaluate(() => window.dispatchEvent(new CustomEvent('ainovel:manuscript-mutated', { detail: { path: '/api/projects/browser-project/manuscript/revision/command' } })));
    }
    await refreshStarted;
    const treeSurface = await openTree(page);
    await treeSurface.locator('[data-tree-index="3"]').click();
    await page.waitForTimeout(450);
    await expect(page.locator('[data-tree-index="3"]')).toHaveAttribute('aria-selected', 'true');
    await expect(page.getByText('STALE_TREE_ERROR')).toHaveCount(0);
    await expect(page.getByText(/B章节：/, { exact: false }).first()).toBeVisible({ timeout: 3000 });
    await expect(page.getByText(/^当前：第1段/, { exact: false })).toHaveCount(0);
    await expect(page.locator('.manuscript-reader').first()).toHaveAttribute('aria-busy', 'false');
    await expect(page.locator('[data-tree-index="3"]')).toHaveAttribute('aria-selected', 'true');
  });
}

test('真实网络失败可重试且 SSE 发布会刷新可见正文', async ({ page }) => {
  await expect(page.getByText('已加载 240 / 240 段').first()).toBeVisible();
  await page.evaluate(() => fetch('/api/test/fail-next-chapter', { method: 'POST' }));
  const treeSurface = await openTree(page);
  await treeSurface.getByRole('treeitem', { name: /真实后端长章/ }).click();
  await expect(page.getByText('网络异常，保留上次成功正文。', { exact: false })).toBeVisible();
  await expect(page.getByText('当前：第1段', { exact: false })).toBeVisible();
  await page.getByRole('button', { name: '重试' }).click();
  await expect(page.getByText('当前：第1段', { exact: false })).toBeVisible();
  await page.evaluate(() => fetch('/api/projects/browser-project/manuscript/revision/command', { method: 'POST', headers: { 'content-type': 'application/json' }, body: JSON.stringify({ action: 'publish', chapter_id: 'ch_0123456789abcdef0123456789abcdef' }) }));
  await expect(page.getByText('发布后：第1段', { exact: false })).toBeVisible({ timeout: 10000 });
});

test('移动端目录是可关闭并返回焦点的 dialog drawer', async ({ page }, testInfo) => {
  test.skip(testInfo.project.name !== 'mobile');
  const opener = page.getByRole('button', { name: '打开完整目录' });
  await opener.click();
  const drawer = page.getByRole('dialog', { name: '稿件目录抽屉' });
  await expect(drawer).toBeVisible();
  await expect(page.getByRole('button', { name: '关闭目录' })).toBeFocused();
  await page.keyboard.press('Tab');
  await expect(drawer.locator(':focus')).toHaveCount(1);
  await page.keyboard.press('Escape');
  await expect(drawer).toBeHidden();
  await expect(opener).toBeFocused();
});
