import { test, expect } from '@playwright/test';

test('overview starts a chat once, resumes it, and opens workspace and provider setup', async ({ page, request }) => {
  await page.goto('/');
  await expect(page).toHaveTitle('Overview · Q Studio');
  const created = page.waitForResponse(response => new URL(response.url()).pathname === '/api/v1/chats' && response.request().method() === 'POST');
  await page.getByRole('button', { name: 'New chat', exact: true }).click();
  const chat = await (await created).json();
  try {
    await expect(page).toHaveURL(new RegExp(`/sessions/${chat.registration_id}$`));
    await expect(page.getByRole('textbox', { name: 'Message Q', exact: true })).toBeEnabled();
    const count = (await (await request.get('/api/v1/registered-sessions')).json()).sessions.length;
    await page.reload();
    await expect(page.getByRole('textbox', { name: 'Message Q', exact: true })).toBeEnabled();
    expect((await (await request.get('/api/v1/registered-sessions')).json()).sessions).toHaveLength(count);
    await page.getByRole('button', { name: 'Overview', exact: true }).click();
    const recent = page.getByRole('region', { name: 'Recent sessions' });
    await recent.getByRole('button', { name: /New chat Personal chat/ }).first().click();
    await expect(page).toHaveURL(new RegExp(`/sessions/${chat.registration_id}$`));
    await page.getByRole('button', { name: 'Overview', exact: true }).click();
    await page.getByRole('button', { name: 'Open a workspace', exact: true }).click();
    const dialog = page.getByRole('dialog', { name: 'New session', exact: true });
    await expect(dialog.getByRole('tab', { name: 'Workspace', exact: true })).toHaveAttribute('aria-selected', 'true');
    await page.keyboard.press('Escape');
    await page.getByRole('button', { name: 'Overview', exact: true }).click();
    await page.getByRole('button', { name: /^Set up your models/ }).click();
    await expect(page.locator('.settings-index button.active')).toContainText('Providers');
    await expect(page).toHaveTitle('Settings · Q Studio');
  } finally {
    await request.delete(`/api/v1/chats/${chat.registration_id}`);
  }
});

test('session search matches titles, project names, and paths and expands collapsed matches', async ({ page, request }) => {
  const fixture = await (await request.get('/_test/fixture')).json();
  const registration = await (await request.post('/api/v1/registered-sessions', { data: { workspace_root: fixture.root, create: true } })).json();
  try {
    await page.goto(`/sessions/${registration.registration_id}`);
    const tree = page.getByRole('complementary', { name: 'Registered session tree' });
    const search = tree.getByRole('textbox', { name: 'Search sessions' });
    const selected = tree.locator('.session-root.active');
    await expect(selected).toBeVisible();
    const project = tree.getByRole('button', { name: /^Integration \d/ });
    await project.click();
    await expect(selected).toBeHidden();
    await search.fill('INTEGRATION');
    await expect(selected).toBeVisible();
    await search.fill(fixture.root.toUpperCase());
    await expect(selected).toBeVisible();
    await search.fill(registration.session.title || 'New session');
    await expect(selected).toBeVisible();
    await search.fill('unmatched-session-ux-query');
    await expect(tree.getByText('No sessions found', { exact: true })).toBeVisible();
    await expect(tree.getByRole('status')).toHaveText('0 sessions found');
    // Filtering the list must never replace the open conversation.
    await expect(page).toHaveURL(new RegExp(`/sessions/${registration.registration_id}$`));
    await search.press('Escape');
    await expect(search).toHaveValue('');
    await expect(search).toBeFocused();
    await expect(selected).toBeHidden();
    await project.click();
    await expect(selected).toBeVisible();
  } finally {
    await request.delete(`/api/v1/registered-sessions/${registration.registration_id}`);
  }
});

test('mobile session picker reveals the list and closes after selection without horizontal overflow', async ({ page, request }) => {
  const chat = await (await request.post('/api/v1/chats', { data: {} })).json();
  try {
    await page.setViewportSize({ width: 390, height: 844 });
    await page.goto(`/sessions/${chat.registration_id}`);
    const tree = page.getByRole('complementary', { name: 'Registered session tree' });
    await expect(page.getByRole('textbox', { name: 'Message Q', exact: true })).toBeEnabled();
    await expect(tree).toBeHidden();
    await page.getByRole('button', { name: 'Show sessions', exact: true }).click();
    await expect(tree).toBeVisible();
    await expect(tree.getByRole('textbox', { name: 'Search sessions' })).toBeVisible();
    await tree.locator('.session-root.active .session-select').click();
    await expect(tree).toBeHidden();
    await expect(page.getByRole('button', { name: 'Show sessions', exact: true })).toHaveAttribute('aria-expanded', 'false');
    for (const width of [320, 390, 900, 1200, 1440]) {
      await page.setViewportSize({ width, height: 844 });
      expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
      const composer = await page.getByRole('textbox', { name: 'Message Q', exact: true }).boundingBox();
      expect(composer.x + composer.width).toBeLessThanOrEqual(width);
      expect((await page.locator('.chat-heading h2').boundingBox()).width).toBeGreaterThan(80);
    }
    await page.goto('/settings?section=appearance');
    await page.getByRole('radio', { name: '145%', exact: true }).check();
    await page.goto(`/sessions/${chat.registration_id}`);
    for (const width of [320, 390, 900]) {
      await page.setViewportSize({ width, height: 844 });
      expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
    }
  } finally {
    await request.delete(`/api/v1/chats/${chat.registration_id}`);
  }
});

test('IME composition Enter keeps the draft and ordinary Enter sends it', async ({ page, request }) => {
  const chat = await (await request.post('/api/v1/chats', { data: {} })).json();
  try {
    await page.goto(`/sessions/${chat.registration_id}`);
    const composer = page.getByRole('textbox', { name: 'Message Q', exact: true });
    await expect(composer).toBeEnabled();
    await composer.fill('한글 입력 확인');
    await composer.dispatchEvent('keydown', { key: 'Enter', code: 'Enter', isComposing: true });
    await expect(composer).toHaveValue('한글 입력 확인');
    await expect(page.locator('.user-message')).toHaveCount(0);
    await composer.press('Enter');
    await expect(page.locator('.user-message').last()).toContainText('한글 입력 확인');
    await expect(composer).toHaveValue('');
  } finally {
    await request.delete(`/api/v1/chats/${chat.registration_id}`);
  }
});
