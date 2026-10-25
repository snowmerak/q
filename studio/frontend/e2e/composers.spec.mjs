import { test, expect } from '@playwright/test';

test('Composer creates a model conversation, saves history, and continues after reload', async ({ page, request }, testInfo) => {
  const errors = [];
  page.on('pageerror', error => errors.push(error.message));
  await page.route('**/api/v1/settings/models', route => route.fulfill({ json: { models: [
    { id: 'default/test-model' }, { id: 'a-fallback-group', group: true }
  ] } }));
  let id;
  try {
    await page.goto('/');
    await page.getByRole('button', { name: 'Composer', exact: true }).click();
    await expect(page).toHaveTitle('Composer · Q Studio');
    await page.getByRole('button', { name: 'New', exact: true }).click();
    await page.getByLabel('Name', { exact: true }).fill('Reading club idea');
    await expect(page.getByRole('combobox', { name: 'Model', exact: true }).locator('option')).toHaveText(['default/test-model']);
    await page.getByRole('button', { name: 'Create conversation' }).click();
    await expect(page.getByRole('heading', { name: 'Reading club idea', exact: true })).toBeVisible();
    id = new URL(page.url()).pathname.split('/').pop();
    const input = page.getByLabel('Develop your idea');
    await input.fill('I want to make a reading club app');
    await input.press('Enter');
    const history = page.getByRole('region', { name: 'Conversation history' });
    await expect(history.getByText('Who would use your idea first?', { exact: true })).toBeVisible();
    await page.reload();
    await expect(history.getByText('I want to make a reading club app', { exact: true })).toBeVisible();
    await expect(history.getByText('Who would use your idea first?', { exact: true })).toBeVisible();
    await input.fill('The first users are students');
    await input.press('Shift+Enter');
    await expect(input).toHaveValue('The first users are students\n');
    await input.press('Enter');
    await expect(history.getByText("Let's develop the reading club idea for students.", { exact: true })).toBeVisible();
    await page.screenshot({ path: testInfo.outputPath('conversation.png'), fullPage: true });
    const detail = await (await request.get(`/api/v1/composers/${id}`)).json();
    expect(detail.model).toBe('default/test-model');
    expect(detail.turns).toHaveLength(2);
    expect(detail.turns.every(turn => turn.status === 'completed')).toBe(true);
    await page.getByRole('button', { name: 'Sessions', exact: true }).click();
    await page.getByRole('button', { name: 'Composer', exact: true }).click();
    await page.getByRole('button', { name: /Reading club idea default\/test-model/ }).click();
    await expect(history.getByText("Let's develop the reading club idea for students.", { exact: true })).toBeVisible();
    page.once('dialog', dialog => dialog.accept());
    await page.getByRole('button', { name: 'Delete', exact: true }).click();
    await expect(page.getByRole('button', { name: /Reading club idea default\/test-model/ })).toHaveCount(0);
    expect((await request.get(`/api/v1/composers/${id}`)).status()).toBe(404);
    expect(errors).toEqual([]);
  } finally { if (id) await request.delete(`/api/v1/composers/${id}`); }
});

test('Composer survives browser reload during a reply and can stop and continue', async ({ page, request }) => {
  const response = await request.post('/api/v1/composers', { data: { name: 'Explore slowly', model: 'default/test-model' } });
  const conversation = await response.json();
  const id = conversation.id;
  try {
    await page.goto(`/composers/${id}`);
    const input = page.getByLabel('Develop your idea');
    await input.fill('composer wait');
    await input.press('Enter');
    await expect(page.getByRole('button', { name: 'Stop reply' })).toBeVisible();
    await page.reload();
    await expect(page.getByRole('button', { name: 'Stop reply' })).toBeVisible();
    await expect(page.getByRole('button', { name: 'Delete', exact: true })).toBeDisabled();
    await page.getByRole('button', { name: 'Stop reply' }).click();
    await expect(page.getByText('Reply stopped. You can continue the conversation.', { exact: true })).toBeVisible();
    await input.fill('I want a reading club for students');
    await input.press('Enter');
    await expect(page.getByText("Let's develop the reading club idea for students.", { exact: true })).toBeVisible();
  } finally {
    await request.post(`/api/v1/composers/${id}/cancel`, { data: {} });
    await request.delete(`/api/v1/composers/${id}`);
  }
});
