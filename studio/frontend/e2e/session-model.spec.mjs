import { test, expect } from '@playwright/test';

test('session model selection persists, stays local, and locks during execution', async ({ page, request }, testInfo) => {
  const first = await (await request.post('/api/v1/chats', { data: {} })).json();
  const second = await (await request.post('/api/v1/chats', { data: {} })).json();
  const errors = [];
  await page.route('**/api/v1/settings/models', route => route.fulfill({ json: { models: [
    { id: 'default/test-model', reasoning_control: 'effort', reasoning_efforts: ['low', 'high'] },
    { id: `other-provider/${'long-unselected-model-name-'.repeat(5)}` }
  ] } }));
  page.on('pageerror', error => errors.push(error.message));
  page.on('console', message => {
    // A fresh session has no latest run; the existing reconnect flow uses 404.
    const emptyHistory = message.text().includes('404 (Not Found)') && /\/runs\/latest\?/.test(message.location().url);
    if (['error', 'warning'].includes(message.type()) && !emptyHistory) errors.push(message.text());
  });
  const picker = page.getByRole('combobox', { name: 'Session model', exact: true });
  const composer = page.getByRole('textbox', { name: 'Message Q', exact: true });
  const effort = page.getByRole('combobox', { name: 'Reasoning effort', exact: true });
  try {
    await page.goto(`/sessions/${first.registration_id}`);
    await expect(picker).toBeEnabled();
    await expect(picker).toHaveValue('');
    await picker.selectOption('default/test-model');
    await expect(picker).toBeEnabled();
    const endpoint = `/api/v1/sessions/${first.session.session_id}/model?workspace_root=${encodeURIComponent(first.workspace_root)}`;
    await expect(effort).toBeEnabled();
    await effort.selectOption('high');
    await expect(effort).toBeEnabled();
    await expect.poll(async () => (await (await request.get(endpoint)).json()).reasoning_effort).toBe('high');
    await expect.poll(async () => (await (await request.get(endpoint)).json()).model).toBe('default/test-model');
    await page.reload();
    await expect(picker).toHaveValue('default/test-model');
    await expect(effort).toHaveValue('high');
    // Long catalog entries must not push the reasoning control onto another row.
    await expect.poll(async () => {
      const modelBox = await picker.boundingBox();
      const effortBox = await effort.boundingBox();
      return Math.abs(modelBox.y - effortBox.y);
    }).toBeLessThanOrEqual(1);
    await expect(picker).toHaveAttribute('title', 'default/test-model');
    await page.screenshot({ path: testInfo.outputPath('session-model-desktop.png') });
    await page.goto(`/sessions/${second.registration_id}`);
    await expect(picker).toBeEnabled();
    await expect(picker).toHaveValue('');
    await expect(effort).toHaveValue('');
    await page.goto(`/sessions/${first.registration_id}`);
    await expect(picker).toHaveValue('default/test-model');
    await composer.fill('wait for guidance');
    await composer.press('Enter');
    await expect(page.getByRole('heading', { name: 'Which direction?' })).toBeVisible();
    await expect(picker).toBeDisabled();
    await expect(effort).toBeDisabled();
    const rejected = await request.put(`/api/v1/sessions/${first.session.session_id}/model`, { data: { workspace_root: first.workspace_root, model: '', reasoning_effort: 'low' } });
    expect(rejected.status()).toBe(409);
    await page.getByRole('button', { name: 'Blue', exact: true }).click();
    await expect(picker).toBeEnabled();
    await picker.selectOption('');
    await expect(picker).toBeEnabled();
    await page.reload();
    await expect(picker).toHaveValue('');
    await expect(picker.locator('option:checked')).toContainText('default/test-model');
    await expect(effort).toHaveValue('high');
    // Verify the compact control remains usable on a narrow chat viewport.
    await page.setViewportSize({ width: 390, height: 844 });
    await expect(picker).toBeVisible();
    await picker.scrollIntoViewIfNeeded();
    const box = await picker.boundingBox();
    expect(box.x).toBeGreaterThanOrEqual(0);
    expect(box.x + box.width).toBeLessThanOrEqual(390);
    const effortBox = await effort.boundingBox();
    expect(effortBox.x + effortBox.width).toBeLessThanOrEqual(390);
    await page.screenshot({ path: testInfo.outputPath('session-model-mobile.png') });
    await page.getByLabel('More session actions', { exact: true }).click();
    page.once('dialog', dialog => dialog.accept());
    await page.getByRole('button', { name: 'Clear conversation', exact: true }).click();
    await expect(effort).toHaveValue('high');
    await effort.selectOption('');
    await expect(effort).toBeEnabled();
    await page.reload();
    await expect(effort).toHaveValue('');
    expect(errors).toEqual([]);
  } finally {
    await request.delete(`/api/v1/chats/${first.registration_id}`);
    await request.delete(`/api/v1/chats/${second.registration_id}`);
  }
});
