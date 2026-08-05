import { expect, test } from '@playwright/test';

test.beforeEach(async ({ page }, testInfo) => {
  test.skip(!['mobile', 'mobile-landscape'].includes(testInfo.project.name));
  await page.goto('/browser-fixture.html?surface=mobile-shell');
});

test('mobile shell keeps primary content between its safe navigation bars', async ({ page }, testInfo) => {
  const main = page.locator('.writing-pane');
  if (testInfo.project.name === 'mobile-landscape') {
    const compactTopbar = page.locator('.mobile-workspace-nav');
    await expect(compactTopbar).toBeVisible();
    await expect(page.locator('.mobile-phone-topbar')).toBeHidden();
    await expect(page.locator('.mobile-phone-bottom-nav')).toBeHidden();
    const [topBox, mainBox] = await Promise.all([compactTopbar.boundingBox(), main.boundingBox()]);
    expect(topBox.y + topBox.height).toBeLessThanOrEqual(mainBox.y + 1);
    expect(mainBox.y + mainBox.height).toBeLessThanOrEqual(430);
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
    return;
  }

  const topbar = page.locator('.mobile-phone-topbar');
  const bottomNav = page.locator('.mobile-phone-bottom-nav');
  await expect(topbar).toBeVisible();
  await expect(bottomNav).toBeVisible();

  const [topBox, mainBox, bottomBox] = await Promise.all([
    topbar.boundingBox(),
    main.boundingBox(),
    bottomNav.boundingBox()
  ]);
  expect(topBox.y + topBox.height).toBeLessThanOrEqual(mainBox.y + 1);
  expect(mainBox.y + mainBox.height).toBeLessThanOrEqual(bottomBox.y + 1);
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);

  if (testInfo.project.name === 'mobile') {
    await page.screenshot({ path: testInfo.outputPath('iphone-15-pro-max-writing.png') });
  }
});

test('project drawer, action sheet, and tool task stay mutually exclusive', async ({ page }, testInfo) => {
  test.skip(testInfo.project.name === 'mobile-landscape', 'Landscape uses the existing compact drawer navigation.');
  await page.getByRole('button', { name: '打开项目列表' }).click();
  await expect(page.getByRole('complementary', { name: '项目导航' })).toBeInViewport();
  await expect(page.getByRole('complementary', { name: '创作与高级工具' })).not.toBeInViewport();
  await page.getByRole('button', { name: '关闭侧栏' }).click({ position: { x: 420, y: 400 } });

  await page.getByRole('button', { name: '打开项目操作' }).click();
  await expect(page.getByRole('dialog', { name: '项目操作' })).toBeVisible();
  await page.getByRole('button', { name: '关闭项目操作' }).click();

  await page.getByRole('button', { name: '工具', exact: true }).click();
  await expect(page.getByRole('heading', { name: '工具中心' })).toBeVisible();
  await page.getByRole('button', { name: /改编审计/ }).click();
  await expect(page.locator('.mobile-tool-detail-header').getByText('改编审计', { exact: true })).toBeVisible();
  await page.getByRole('button', { name: '返回工具中心' }).click();
  await expect(page.getByRole('heading', { name: '工具中心' })).toBeVisible();

  if (testInfo.project.name === 'mobile') {
    await page.screenshot({ path: testInfo.outputPath('iphone-15-pro-max-tools.png') });
  }
  await page.getByRole('button', { name: '关闭工具中心' }).click();
  await expect(page.getByRole('heading', { name: '工具中心' })).not.toBeInViewport();
});
