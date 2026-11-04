import { test, expect } from '@playwright/test';

test('provider API mode initializes once and model Chat override survives reload', async ({ page, request }, testInfo) => {
  const errors = [];
  page.on('pageerror', error => errors.push(error.message));
  page.on('console', message => {
    // Studio has no favicon; its browser-generated 404 is unrelated to settings.
    if (message.type() === 'error' && !message.location().url.endsWith('/favicon.ico')) errors.push(message.text());
  });
  const settings = await (await request.get('/api/v1/settings')).json();
  const source = settings.gateway_providers.items.find(provider => provider.base_url);
  const id = 'api-mode-provider';
  const savedProvider = async () => (await (await request.get('/api/v1/settings')).json()).gateway_providers.items.find(provider => provider.id === id);
  expect((await request.post('/api/v1/settings/gateway/providers', { data: { id, type: 'openai-compatible', kind: '', enabled: true, base_url: source.base_url } })).ok()).toBe(true);
  try {
    await page.goto('/settings?section=providers');
    await expect(page).toHaveTitle(/Q Studio/);
    const card = page.locator('.provider-card').filter({ has: page.getByRole('heading', { name: id, exact: true }) });
    const mode = card.getByRole('combobox', { name: 'API mode', exact: true });
    await expect(mode).toHaveValue('chat_completions');
    await card.getByRole('combobox', { name: 'Provider kind', exact: true }).selectOption('grok');
    await expect.poll(async () => (await savedProvider()).api_mode).toBe('responses');
    await expect(mode).toHaveValue('responses');
    await mode.selectOption('chat_completions');
    await expect.poll(async () => (await savedProvider()).api_mode).toBe('chat_completions');
    await card.getByRole('textbox', { name: 'Model prefix', exact: true }).fill('api-mode-prefix');
    await card.getByRole('textbox', { name: 'Model prefix', exact: true }).press('Tab');
    await expect.poll(async () => (await savedProvider()).prefix).toBe('api-mode-prefix');
    await page.reload();
    await expect(mode).toHaveValue('chat_completions');
    await mode.scrollIntoViewIfNeeded();
    await page.screenshot({ path: testInfo.outputPath('provider-api-mode-desktop.png') });
    await page.setViewportSize({ width: 390, height: 844 });
    await mode.scrollIntoViewIfNeeded();
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
    await page.screenshot({ path: testInfo.outputPath('provider-api-mode-mobile.png') });
    await page.setViewportSize({ width: 1440, height: 900 });
    await mode.selectOption('responses');
    await expect.poll(async () => (await savedProvider()).api_mode).toBe('responses');
    const catalog = await (await request.get('/api/v1/settings/models')).json();
    const model = catalog.models.find(model => model.id.startsWith('api-mode-prefix/'));
    expect(model?.api_mode).toBe('responses');
    const currentModel = async () => (await (await request.get('/api/v1/settings/models')).json()).models.find(option => option.id === model.id);
    await page.goto('/settings?section=models&model_tab=behavior');
    const row = page.locator('.model-behavior-row').filter({ has: page.getByText(model.id, { exact: true }) });
    const modelMode = row.getByRole('combobox', { name: 'API mode', exact: true });
    await expect(modelMode).toHaveValue('');
    await modelMode.selectOption('chat_completions');
    await expect.poll(async () => (await currentModel()).api_mode).toBe('chat_completions');
    await page.reload();
    await expect(modelMode).toHaveValue('chat_completions');
    expect((await currentModel()).api_mode_override).toBe('chat_completions');
    await modelMode.selectOption('');
    await expect.poll(async () => (await currentModel()).api_mode).toBe('responses');
    await expect(modelMode).toHaveValue('');
    expect(errors).toEqual([]);
  } finally {
    await request.delete(`/api/v1/settings/gateway/providers/${id}`);
  }
});
