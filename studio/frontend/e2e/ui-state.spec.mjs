import { test, expect } from './test.mjs';
import { createServer, request as proxyRequest } from 'node:http';

test('legacy browser preferences migrate without replacing profile settings', async ({ page, request }) => {
  await page.addInitScript(() => {
    localStorage.setItem('q-studio-theme', 'night');
    localStorage.setItem('q-studio-text-scale', '145');
  });
  await page.goto('/settings?section=appearance');
  await expect(page.getByRole('radio', { name: /^Night/ })).toBeChecked();
  await expect(page.getByRole('radio', { name: '145%' })).toBeChecked();
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
    await page.getByRole('radio', { name: /^Night/ }).check();
    await page.getByRole('radio', { name: '145%' }).check();
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
    await expect(page.locator('html')).toHaveAttribute('data-theme', 'night');
    await expect(page.locator('html')).toHaveCSS('--text-scale', '1.45');
    await expect(page.getByRole('button', { name: 'Collapse sidebar' })).toBeVisible();
    await expect(input).toHaveValue('Unsent draft across changing ports');
    expect(await page.evaluate(() => localStorage.getItem('q-studio-workspace-root'))).toBe(fixture.root);
  } finally {
    await request.delete(`/api/v1/chats/${chat.registration_id}`);
    proxy.closeAllConnections();
    await new Promise(resolve => proxy.close(resolve));
  }
});

test('profile persistence works when browser storage is blocked', async ({ page, request }) => {
  await request.patch('/api/v1/ui-state', { data: { values: { 'q-studio-theme': 'light', 'q-studio-text-scale': '115' } } });
  await page.addInitScript(() => {
    Object.defineProperty(window, 'localStorage', { get() { throw new DOMException('Storage blocked', 'SecurityError'); } });
  });
  await page.goto('/settings?section=appearance');
  await expect(page.getByRole('radio', { name: /^Light/ })).toBeChecked();
  await expect(page.getByRole('radio', { name: '115%' })).toBeChecked();
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
