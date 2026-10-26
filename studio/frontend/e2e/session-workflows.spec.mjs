import { test, expect } from '@playwright/test';

test('Sessions preserves the Interaction panel across workflow selection and protects artifact edits', async ({ page, request }, testInfo) => {
  const errors = [];
  page.on('pageerror', error => errors.push(error.message));
  page.on('response', response => {
    if (response.status() >= 400 && new URL(response.url()).pathname.endsWith('/model')) errors.push(`${response.status()} ${response.url()}`);
  });
  const chat = await (await request.post('/api/v1/chats', { data: {} })).json();
  let idea, council, extraChat;
  try {
    await page.goto(`/sessions/${chat.registration_id}`);
    await expect(page).toHaveTitle('Sessions · Q Studio');
    const input = page.getByRole('textbox', { name: 'Message Q', exact: true });
    await expect(page.getByRole('combobox', { name: 'Session model' })).toBeEnabled();
    await input.fill('Keep this ordinary session draft');
    await input.blur();
    const geometry = await page.locator('.chat-panel').boundingBox();
    const before = await page.locator('.chat-panel').screenshot({ caret: 'hide', animations: 'disabled' });
    idea = await (await request.post('/api/v1/composers', { data: { name: 'Unified visual idea', model: 'default/test-model' } })).json();
    council = await (await request.post('/api/v1/councils', { data: { name: 'Unified review', scope: 'independent', members: [{ model: 'default/test-model' }, { model: 'default/test-model-2' }], chair: { model: 'default/test-model' } } })).json();
    await request.put(`/api/v1/composers/${idea.id}/artifacts/source?path=docs/webcomponent/example.html`, { data: { content: '<h1>Original component</h1>', expected_hash: '' } });
    await page.getByRole('button', { name: 'Refresh session tree' }).click();
    const tree = page.getByRole('complementary', { name: 'Registered session tree' });
    const rows = tree.locator('.session-select');
    await expect(rows.filter({ hasText: 'Unified visual idea' })).toBeVisible();
    const filter = tree.getByRole('combobox', { name: 'Filter session type' });
    await filter.selectOption('composer');
    await expect(rows.filter({ hasText: 'Unified review' })).toHaveCount(0);
    // Filtering must keep the currently open ordinary conversation and its draft.
    await expect(input).toHaveValue('Keep this ordinary session draft');
    await filter.selectOption('all');
    await rows.filter({ hasText: 'Unified visual idea' }).click();
    const ideaPath = new URL(page.url()).pathname;
    await expect(page.getByLabel('Develop your idea')).toBeVisible();
    await expect(page.getByRole('button', { name: 'More session actions' })).toHaveCount(0);
    await expect(page.getByRole('button', { name: 'Pause turn' })).toHaveCount(0);
    const artifacts = page.getByRole('region', { name: 'Composer artifacts' });
    await artifacts.getByRole('button', { name: 'Source', exact: true }).click();
    await artifacts.getByLabel('Artifact source').fill('<h1>Unsaved component</h1>');
    await page.getByLabel('Develop your idea').fill('Keep my Composer draft');
    page.once('dialog', dialog => dialog.dismiss());
    await rows.filter({ hasText: 'Unified review' }).click();
    await expect(page).toHaveURL(new RegExp(`${ideaPath}$`));
    await expect(artifacts.getByLabel('Artifact source')).toHaveValue('<h1>Unsaved component</h1>');
    page.once('dialog', dialog => dialog.dismiss());
    await page.getByRole('button', { name: 'Overview', exact: true }).click();
    await expect(artifacts.getByLabel('Artifact source')).toBeVisible();
    await artifacts.getByRole('button', { name: 'Save source' }).click();
    await expect(artifacts.getByText('All changes saved', { exact: true })).toBeVisible();
    await page.screenshot({ path: testInfo.outputPath('unified-composer-desktop.png') });
    await rows.filter({ hasText: 'Unified review' }).click();
    await expect(page.getByRole('heading', { name: 'Unified review', exact: true })).toBeVisible();
    await expect(page.getByLabel('Ask this council')).toBeVisible();
    await expect(page.getByRole('combobox', { name: 'Session model' })).toHaveCount(0);
    await page.getByLabel('Ask this council').fill('Keep my Council draft');
    await page.screenshot({ path: testInfo.outputPath('unified-council-desktop.png') });
    await rows.filter({ hasText: 'Unified visual idea' }).click();
    await expect(page.getByLabel('Develop your idea')).toHaveValue('Keep my Composer draft');
    await tree.locator(`.session-root.active`).getByRole('button', { name: /^Unified visual idea/ }).click();
    await rows.filter({ hasText: chat.session.session_id.slice(0, 8) }).click();
    await expect(input).toHaveValue('Keep this ordinary session draft');
    await expect(page.getByRole('combobox', { name: 'Session model' })).toBeEnabled();
    expect(await page.locator('.chat-panel').boundingBox()).toEqual(geometry);
    const after = await page.locator('.chat-panel').screenshot({ caret: 'hide', animations: 'disabled' });
    expect(after.equals(before), 'ordinary session panel must remain pixel-identical after switching types').toBe(true);
    await page.screenshot({ path: testInfo.outputPath('interaction-preserved.png') });
    await page.goBack();
    await expect(page.getByLabel('Develop your idea')).toHaveValue('Keep my Composer draft');
    await page.goForward();
    await expect(input).toHaveValue('Keep this ordinary session draft');
    await page.setViewportSize({ width: 390, height: 844 });
    await page.getByRole('button', { name: 'Show sessions', exact: true }).click();
    await rows.filter({ hasText: 'Unified review' }).click();
    await expect(tree).toBeHidden();
    await expect(page.getByLabel('Ask this council')).toHaveValue('Keep my Council draft');
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
    await page.screenshot({ path: testInfo.outputPath('unified-council-mobile.png'), fullPage: true });
    await page.getByRole('button', { name: 'Show sessions', exact: true }).click();
    await rows.filter({ hasText: 'Unified visual idea' }).click();
    await expect(page.getByLabel('Develop your idea')).toHaveValue('Keep my Composer draft');
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
    await page.screenshot({ path: testInfo.outputPath('unified-composer-mobile.png'), fullPage: true });
    await page.getByRole('button', { name: 'Show sessions', exact: true }).click();
    await filter.selectOption('composer');
    const created = page.waitForResponse(response => new URL(response.url()).pathname === '/api/v1/chats' && response.request().method() === 'POST');
    await tree.getByRole('button', { name: 'New chat', exact: true }).first().click();
    extraChat = await (await created).json();
    await expect(input).toBeEnabled();
    await expect(tree).toBeHidden();
    await page.getByRole('button', { name: 'Show sessions', exact: true }).click();
    await expect(filter).toHaveValue('all');
    expect(errors).toEqual([]);
  } finally {
    if (idea) await request.delete(`/api/v1/composers/${idea.id}`);
    if (council) await request.delete(`/api/v1/councils/${council.id}`);
    await request.delete(`/api/v1/chats/${chat.registration_id}`);
    if (extraChat) await request.delete(`/api/v1/chats/${extraChat.registration_id}`);
  }
});

test('a late Council response cannot replace the newly selected ordinary session', async ({ page, request }) => {
  const chat = await (await request.post('/api/v1/chats', { data: {} })).json();
  const council = await (await request.post('/api/v1/councils', { data: { name: 'Delayed review', scope: 'independent', members: [{ model: 'default/test-model' }, { model: 'default/test-model-2' }], chair: { model: 'default/test-model' } } })).json();
  let release;
  const gate = new Promise(resolve => { release = resolve; });
  try {
    await page.route(`**/api/v1/councils/${council.id}`, async route => {
      await gate;
      await route.fulfill({ json: { council, turns: [] } }).catch(() => {});
    });
    await page.goto(`/sessions/${chat.registration_id}`);
    const pending = page.waitForRequest(request => new URL(request.url()).pathname === `/api/v1/councils/${council.id}`);
    await page.locator('.session-select').filter({ hasText: 'Delayed review' }).click();
    await pending;
    await page.locator('.session-select').filter({ hasText: chat.session.session_id.slice(0, 8) }).click();
    release();
    await expect(page).toHaveURL(new RegExp(`/sessions/${chat.registration_id}$`));
    await expect(page.getByRole('textbox', { name: 'Message Q', exact: true })).toBeEnabled();
    await expect(page.locator('.chat-panel')).toBeVisible();
    await expect(page.getByLabel('Ask this council')).toHaveCount(0);
  } finally {
    release();
    await request.delete(`/api/v1/councils/${council.id}`);
    await request.delete(`/api/v1/chats/${chat.registration_id}`);
  }
});
