import { test, expect } from '@playwright/test';

test('browser CLI preferences save, reload and fit narrow screens', async ({ page, request }, testInfo) => {
  const errors = [];
  page.on('pageerror', error => errors.push(error.message));
  const before = (await (await request.get('/api/v1/settings')).json()).browser_control.config;
  const saved = async () => (await (await request.get('/api/v1/settings')).json()).browser_control.config;
  try {
    await page.goto('/settings?section=browser-control');
    await expect(page.getByRole('heading', { name: 'Browser control', exact: true })).toBeVisible();
    await page.getByRole('checkbox', { name: 'Use browser CLI preference' }).check();
    await expect.poll(async () => (await saved()).enabled).toBe(true);
    await page.getByRole('combobox', { name: 'Preferred browser CLI' }).selectOption('playwright-cli');
    await expect.poll(async () => (await saved()).preferred).toBe('playwright-cli');
    const command = page.getByRole('textbox', { name: 'playwright-cli executable', exact: true });
    await command.fill('C:\\Program Files\\Browser tools\\playwright-cli.cmd');
    await command.press('Tab');
    await expect.poll(async () => (await saved()).drivers['playwright-cli'].command).toBe('C:\\Program Files\\Browser tools\\playwright-cli.cmd');
    const args = page.getByRole('textbox', { name: 'playwright-cli arguments', exact: true });
    await args.fill('["--profile","with spaces"]');
    await args.press('Tab');
    await expect.poll(async () => (await saved()).drivers['playwright-cli'].args).toEqual(['--profile', 'with spaces']);
    await page.reload();
    await expect(command).toHaveValue('C:\\Program Files\\Browser tools\\playwright-cli.cmd');
    await expect(args).toHaveValue('["--profile","with spaces"]');
    await page.screenshot({ path: testInfo.outputPath('browser-control-desktop.png') });
    await page.setViewportSize({ width: 390, height: 844 });
    await command.scrollIntoViewIfNeeded();
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
    await page.screenshot({ path: testInfo.outputPath('browser-control-mobile.png') });
    await args.fill('not json');
    await args.press('Tab');
    await expect(page.getByRole('alert')).toBeVisible();
    expect((await saved()).drivers['playwright-cli'].args).toEqual(['--profile', 'with spaces']);
    await page.getByRole('checkbox', { name: 'Use browser CLI preference' }).uncheck();
    await expect.poll(async () => (await saved()).enabled).toBe(false);
    await expect(page.locator('pre')).toContainText('disabled');
    expect(errors).toEqual([]);
  } finally {
    expect((await request.put('/api/v1/settings/browser-control', { data: before })).ok()).toBe(true);
  }
});
