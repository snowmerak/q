import { test, expect } from '@playwright/test';

test('provider model regex filters the catalog, persists and rejects invalid input', async ({ page, request }, testInfo) => {
  const errors = [];
  page.on('pageerror', error => errors.push(error.message));
  const settings = await (await request.get('/api/v1/settings')).json();
  const source = settings.gateway_providers.items.find(provider => provider.base_url);
  const id = 'regex-provider';
  expect((await request.post('/api/v1/settings/gateway/providers', { data: {
    id, type: 'openai-compatible', enabled: true, base_url: source.base_url, prefix: 'regex-prefix'
  } })).ok()).toBe(true);
  const savedProvider = async () => (await (await request.get('/api/v1/settings')).json()).gateway_providers.items.find(provider => provider.id === id);
  const models = async () => (await (await request.get('/api/v1/settings/models')).json()).models.filter(model => model.id.startsWith('regex-prefix/'));
  try {
    const initial = await models();
    expect(initial.length).toBeGreaterThan(0);
    await page.goto('/settings?section=providers');
    const card = page.locator('.provider-card').filter({ has: page.getByRole('heading', { name: id, exact: true }) });
    const filter = card.getByRole('textbox', { name: 'Model filter regex', exact: true });
    const upstreamID = initial[0].id.slice('regex-prefix/'.length);
    const pattern = `^${upstreamID.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')}$`;
    await filter.fill(pattern);
    await filter.press('Tab');
    await expect.poll(async () => (await savedProvider()).model_filter_regex).toBe(pattern);
    await expect.poll(async () => (await models()).map(model => model.id)).toEqual([initial[0].id]);
    await page.reload();
    await expect(filter).toHaveValue(pattern);
    await filter.scrollIntoViewIfNeeded();
    await page.screenshot({ path: testInfo.outputPath('provider-model-filter-desktop.png') });
    await page.setViewportSize({ width: 390, height: 844 });
    await filter.scrollIntoViewIfNeeded();
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
    await page.screenshot({ path: testInfo.outputPath('provider-model-filter-mobile.png') });
    await filter.fill('^$');
    await filter.press('Tab');
    await expect.poll(async () => (await savedProvider()).model_filter_regex).toBe('^$');
    await expect.poll(async () => (await models()).length).toBe(0);
    await filter.fill('[');
    await filter.press('Tab');
    await expect(page.locator('.save-state')).toContainText('provider_model_filters');
    expect((await savedProvider()).model_filter_regex).toBe('^$');
    await expect(filter).toHaveValue('[');
    await filter.fill('');
    await filter.press('Tab');
    await expect.poll(async () => (await savedProvider()).model_filter_regex).toBe('');
    await expect.poll(async () => (await models()).length).toBe(initial.length);
    await expect(page.locator('.save-state')).toHaveText('Saved');
    expect(errors).toEqual([]);
  } finally {
    await request.delete(`/api/v1/settings/gateway/providers/${id}`);
  }
});
