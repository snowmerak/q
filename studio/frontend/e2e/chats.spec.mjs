import { test, expect } from '@playwright/test';

test('session registration can start a chat with an empty workspace field', async ({ page, request }) => {
  await page.goto('/sessions');
  await page.getByRole('button', { name: 'Add or create session', exact: true }).click();
  const dialog = page.getByRole('dialog', { name: 'New session', exact: true });
  await expect(dialog.getByRole('tab', { name: 'Chat', exact: true })).toHaveAttribute('aria-selected', 'true');
  await expect(dialog.getByLabel('Workspace directory')).toHaveCount(0);
  const created = page.waitForResponse(response => new URL(response.url()).pathname === '/api/v1/chats' && response.request().method() === 'POST');
  await dialog.getByRole('button', { name: 'New chat', exact: true }).click();
  const response = await created;
  expect(response.status()).toBe(201);
  const chat = await response.json();
  await expect(dialog).toHaveCount(0);
  await expect(page.getByPlaceholder('Message Q…')).toBeVisible();
  expect((await request.delete(`/api/v1/chats/${chat.registration_id}`)).status()).toBe(204);
});

test('create a personal chat without selecting a workspace, reload it, and delete it', async ({ page }, testInfo) => {
  const errors = [];
  page.on('pageerror', error => errors.push(error.message));
  await page.goto('/sessions');
  const tree = page.getByRole('complementary', { name: 'Registered session tree' });
  const created = page.waitForResponse(response => new URL(response.url()).pathname === '/api/v1/chats' && response.request().method() === 'POST');
  await tree.getByRole('button', { name: 'New chat', exact: true }).first().click();
  const response = await created;
  expect(response.status()).toBe(201);
  const chat = await response.json();
  expect(chat.kind).toBe('chat');
  expect(chat.project_id).toBe('');
  expect(chat.workspace_root.replaceAll('\\', '/')).toContain('/.q/chats/chat-');
  await expect(page).toHaveURL(new RegExp(`/sessions/${chat.registration_id}$`));
  await expect(page.getByRole('dialog')).toHaveCount(0);
  const composer = page.getByPlaceholder('Message Q…');
  await composer.fill('controlled finish');
  await page.getByRole('button', { name: 'Send message', exact: true }).click();
  await expect(page.getByPlaceholder('Message Q…')).toBeVisible();
  await expect(page.locator('.session-root.active')).toContainText('Personal chat');
  await page.reload();
  await expect(page.locator('.session-root.active')).toContainText('Personal chat');
  await expect(page.locator('.chat-panel')).toContainText('controlled finish');
  for (const width of [1440, 390]) {
    await page.setViewportSize({ width, height: 900 });
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
    await page.screenshot({ path: testInfo.outputPath(`chat-${width}.png`) });
  }
  await page.getByRole('button', { name: 'Show sessions', exact: true }).click();
  page.once('dialog', dialog => dialog.accept());
  const deleted = page.waitForResponse(response => new URL(response.url()).pathname === `/api/v1/chats/${chat.registration_id}` && response.request().method() === 'DELETE');
  await page.locator('.session-root.active').getByRole('button', { name: /Delete chat/ }).click();
  expect((await deleted).status()).toBe(204);
  await expect(page).not.toHaveURL(new RegExp(`/sessions/${chat.registration_id}$`));
  expect(errors).toEqual([]);
});
