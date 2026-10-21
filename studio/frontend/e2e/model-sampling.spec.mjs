import { test, expect } from '@playwright/test';

test('model sampling preset, individual zero, reload, and clear', async ({ page, request }, testInfo) => {
  const errors = [];
  page.on('pageerror', error => errors.push(error.message));
  const catalog = await (await request.get('/api/v1/settings/models')).json();
  const model = catalog.models.find(model => !model.group);
  expect(model).toBeTruthy();
  const endpoint = '/api/v1/settings/model-sampling';
  const saved = model.sampling ?? {};
  const current = async () => {
    const result = await (await request.get('/api/v1/settings/models')).json();
    return result.models.find(option => option.id === model.id).sampling;
  };
  const row = page.locator('.model-behavior-row').filter({ has: page.getByText(model.id, { exact: true }) });
  const sampling = row.locator('details');
  try {
    await page.goto('/settings?section=models&model_tab=behavior');
    await sampling.locator('summary').click();
    await sampling.getByRole('button', { name: 'Greedy / penalties off', exact: true }).click();
    const preset = { temperature: 0, top_p: 1, top_k: 0, min_p: 0, repetition_penalty: 1, presence_penalty: 0, frequency_penalty: 0 };
    await expect.poll(current).toEqual(preset);
    await page.reload();
    await sampling.locator('summary').click();
    for (const [key, value] of Object.entries(preset)) {
      await expect(sampling.getByRole('spinbutton', { name: key, exact: true })).toHaveValue(String(value));
    }
    await page.screenshot({ path: testInfo.outputPath('sampling-desktop.png') });
    const temperature = sampling.getByRole('spinbutton', { name: 'temperature', exact: true });
    await temperature.fill('0.4');
    await temperature.press('Tab');
    await expect.poll(async () => (await current()).temperature).toBe(0.4);
    await temperature.fill('0');
    await temperature.press('Tab');
    await expect.poll(async () => (await current()).temperature).toBe(0);
    const topP = sampling.getByRole('spinbutton', { name: 'top_p', exact: true });
    await topP.fill('');
    await topP.press('Tab');
    await expect.poll(async () => (await current()).top_p).toBeUndefined();
    await page.setViewportSize({ width: 390, height: 844 });
    await temperature.scrollIntoViewIfNeeded();
    const box = await temperature.boundingBox();
    expect(box.x).toBeGreaterThanOrEqual(0);
    expect(box.x + box.width).toBeLessThanOrEqual(390);
    await page.screenshot({ path: testInfo.outputPath('sampling-mobile.png') });
    await sampling.getByRole('button', { name: 'Clear overrides', exact: true }).click();
    await expect.poll(current).toEqual({});
    expect(errors).toEqual([]);
  } finally {
    await request.put(endpoint, { data: { model: model.id, sampling: saved } });
  }
});
