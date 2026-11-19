import { test, expect } from './test.mjs';
import { createServer, request as proxyRequest } from 'node:http';

test('legacy browser preferences migrate without replacing profile settings', async ({ page, request }) => {
  await page.addInitScript(() => {
    localStorage.setItem('q-studio-theme', 'night');
    localStorage.setItem('q-studio-text-scale', '145');
  });
  await page.goto('/settings?section=appearance');
  await expect(page.getByRole('radio', { name: /^Night/ })).toBeChecked();
  await expect(page.getByRole('radio', { name: '140%' })).toBeChecked();
  expect((await (await request.get('/api/v1/ui-state')).json()).values['q-studio-theme']).toBe('night');
  await request.patch('/api/v1/ui-state', { data: { values: { 'q-studio-theme': 'light' } } });
  await page.reload();
  await expect(page.getByRole('radio', { name: /^Light/ })).toBeChecked();
});

test('appearance, navigation and drafts survive switching to another port', async ({ page, request, baseURL }) => {
  const chat = await (await request.post('/api/v1/chats', { data: {} })).json();
  const fixture = await (await request.get('/_test/fixture')).json();
  const proxy = createServer((incoming, outgoing) => {
    const target = new URL(incoming.url, baseURL);
    const forwarded = proxyRequest(target, {
      method: incoming.method,
      headers: { ...incoming.headers, host: target.host }
    }, response => {
      outgoing.writeHead(response.statusCode, response.headers);
      response.pipe(outgoing);
    });
    forwarded.on('error', () => { outgoing.writeHead(502); outgoing.end(); });
    incoming.pipe(forwarded);
  });
  await new Promise(resolve => proxy.listen(0, '127.0.0.1', resolve));
  const alternateOrigin = `http://127.0.0.1:${proxy.address().port}`;
  expect(alternateOrigin).not.toBe(baseURL);
  try {
    await page.goto('/settings?section=appearance');
    await page.getByRole('radio', { name: /^Graphite/ }).check();
    await page.getByRole('checkbox', { name: 'Compact session view' }).check();
    await page.getByRole('radio', { name: '140%' }).check();
    await page.getByRole('button', { name: 'Expand sidebar' }).click();
    await request.patch('/api/v1/ui-state', { data: { values: { 'q-studio-workspace-root': fixture.root } } });
    await page.goto(`/sessions/${chat.registration_id}`);
    const input = page.getByRole('textbox', { name: 'Message Q', exact: true });
    await input.fill('Unsent draft across changing ports');
    await expect.poll(async () => Object.values((await (await request.get('/api/v1/ui-state')).json()).values))
      .toContain('Unsent draft across changing ports');
    await page.addInitScript(() => { window.preferencesBeforeLoad = localStorage.getItem('q-studio-theme'); });
    await page.goto(`${alternateOrigin}/sessions/${chat.registration_id}`);
    expect(await page.evaluate(() => window.preferencesBeforeLoad)).toBeNull();
    await expect(page.locator('html')).toHaveAttribute('data-theme', 'graphite');
    await expect(page.locator('html')).toHaveClass(/\bcompact-sessions\b/);
    await expect(page.locator('html')).toHaveCSS('--text-scale', '1.4');
    await expect(page.getByRole('button', { name: 'Collapse sidebar' })).toBeVisible();
    await expect(input).toHaveValue('Unsent draft across changing ports');
    expect(await page.evaluate(() => localStorage.getItem('q-studio-workspace-root'))).toBe(fixture.root);
  } finally {
    await request.delete(`/api/v1/chats/${chat.registration_id}`);
    proxy.closeAllConnections();
    await new Promise(resolve => proxy.close(resolve));
  }
});

test('compact sessions reclaim space without shrinking messages', async ({ page, request }, testInfo) => {
  const chat = await (await request.post('/api/v1/chats', { data: {} })).json();
  const sessionURL = `/sessions/${chat.registration_id}`;
  const settingsURL = '/settings?section=appearance';
  const compactView = page.getByRole('checkbox', { name: 'Compact session view' });
  const pageHeader = page.locator('.sessions-main .page-header');
  const chatHeader = page.locator('.chat-heading');
  const selectedRow = page.locator('.session-select[aria-current="true"]');
  const message = page.locator('.chat-message .markdown-body').first();
  const height = locator => locator.evaluate(element => element.getBoundingClientRect().height);

  try {
    await page.goto(sessionURL);
    const input = page.getByRole('textbox', { name: 'Message Q', exact: true });
    await input.fill('Review the project documentation.');
    await input.press('Enter');
    await expect(chatHeader).toContainText('Completed');
    const originalHeights = await Promise.all([pageHeader, chatHeader, selectedRow].map(height));
    const messageSize = await message.evaluate(element => getComputedStyle(element).fontSize);
    await page.screenshot({ path: testInfo.outputPath('standard-session.png') });

    await page.goto(settingsURL);
    await expect(compactView).not.toBeChecked();
    await compactView.check();
    await expect(page.locator('html')).toHaveClass(/\bcompact-sessions\b/);
    await page.getByRole('radio', { name: /^Graphite/ }).check();
    await expect(compactView).toBeChecked();
    await expect.poll(async () => (await (await request.get('/api/v1/ui-state')).json()).values)
      .toMatchObject({ 'q-studio-compact-sessions': 'true', 'q-studio-theme': 'graphite' });

    await page.goto(sessionURL);
    await expect(page.locator('html')).toHaveCSS('--background', '#171717');
    await expect(message).toHaveCSS('font-size', messageSize);
    const compactHeights = await Promise.all([pageHeader, chatHeader, selectedRow].map(height));
    for (const [index, compactHeight] of compactHeights.entries()) {
      expect(compactHeight).toBeLessThan(originalHeights[index]);
    }
    await page.screenshot({ path: testInfo.outputPath('graphite-compact-session.png') });

    await page.setViewportSize({ width: 390, height: 844 });
    await page.getByRole('button', { name: 'Show sessions', exact: true }).click();
    await expect(selectedRow).toBeVisible();
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);

    await page.goto(settingsURL);
    await expect(compactView).toBeVisible();
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
    await compactView.uncheck();
    await expect(page.getByRole('radio', { name: /^Graphite/ })).toBeChecked();
    await expect.poll(async () => (await (await request.get('/api/v1/ui-state')).json()).values['q-studio-compact-sessions']).toBe('false');
    await page.setViewportSize({ width: 1440, height: 900 });
    await page.goto(sessionURL);
    expect(await Promise.all([pageHeader, chatHeader, selectedRow].map(height))).toEqual(originalHeights);
  } finally {
    await request.delete(`/api/v1/chats/${chat.registration_id}`);
  }
});

test('profile persistence works when browser storage is blocked', async ({ page, request }) => {
  await request.patch('/api/v1/ui-state', { data: { values: { 'q-studio-theme': 'light', 'q-studio-text-scale': '115' } } });
  await page.addInitScript(() => {
    Object.defineProperty(window, 'localStorage', { get() { throw new DOMException('Storage blocked', 'SecurityError'); } });
  });
  await page.goto('/settings?section=appearance');
  await expect(page.getByRole('radio', { name: /^Light/ })).toBeChecked();
  await expect(page.getByRole('radio', { name: '120%' })).toBeChecked();
  await page.getByRole('radio', { name: /^Night/ }).check();
  await expect.poll(async () => (await (await request.get('/api/v1/ui-state')).json()).values['q-studio-theme']).toBe('night');
  await page.reload();
  await expect(page.getByRole('radio', { name: /^Night/ })).toBeChecked();
  await expect(page.getByRole('alert')).toHaveCount(0);
});

test('failed saves are visible and newer edits win during retry', async ({ page, request }) => {
  await page.goto('/settings?section=appearance');
  let reject = true;
  await page.route('**/api/v1/ui-state', route => {
    if (route.request().method() === 'PATCH' && reject) return route.fulfill({ status: 503, json: { error: 'Unavailable' } });
    return route.continue();
  });
  await page.getByRole('radio', { name: /^Night/ }).check();
  await expect(page.getByRole('alert')).toContainText('could not be saved');
  await page.getByRole('radio', { name: /^Light/ }).check();
  await page.getByRole('radio', { name: '130%' }).check();
  reject = false;
  await expect.poll(async () => (await (await request.get('/api/v1/ui-state')).json()).values['q-studio-theme']).toBe('light');
  await expect.poll(async () => (await (await request.get('/api/v1/ui-state')).json()).values['q-studio-text-scale']).toBe('130');
  await expect(page.getByRole('alert')).toHaveCount(0);
});
