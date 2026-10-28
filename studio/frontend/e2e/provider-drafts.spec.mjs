import { test, expect } from '@playwright/test';
import { tmpdir } from 'node:os';
import { join } from 'node:path';

test.beforeEach(async ({ page }) => {
  page.providerErrors = [];
  page.on('pageerror', error => page.providerErrors.push(error.message));
  page.on('console', message => {
    if (message.type() === 'error' && !message.location().url.endsWith('/favicon.ico')) {
      page.providerErrors.push(message.text());
    }
  });
});

test.afterEach(async ({ page }) => {
  expect(page.providerErrors).toEqual([]);
});

test('provider save acknowledgements preserve the next field before blur', async ({ page, request }) => {
  const id = 'draft-ack-provider';
  expect((await request.post('/api/v1/settings/gateway/providers', { data: {
    id, type: 'openai-compatible', enabled: false, base_url: 'http://127.0.0.1:1/v1'
  } })).ok()).toBe(true);
  await page.goto('/settings?section=providers');
  const card = page.locator('.provider-card').filter({ has: page.getByRole('heading', { name: id, exact: true }) });
  const prefix = card.getByRole('textbox', { name: 'Model prefix', exact: true });
  const baseURL = card.getByRole('textbox', { name: 'Base URL', exact: true });
  let release;
  const gate = new Promise(resolve => { release = resolve; });
  let notify;
  const held = new Promise(resolve => { notify = resolve; });
  let first = true;
  await page.route(`**/api/v1/settings/gateway/providers/${id}`, async route => {
    const response = await route.fetch();
    if (first) { first = false; notify(); await gate; }
    await route.fulfill({ response });
  });
  await prefix.fill('draft-prefix');
  await baseURL.click();
  await held;
  await baseURL.fill('http://127.0.0.1:2/v1');
  release();
  await expect(page.locator('.save-state')).toHaveText('Saved');
  await expect(baseURL).toHaveValue('http://127.0.0.1:2/v1');
  await baseURL.press('Tab');
  await expect.poll(async () => {
    const snapshot = await (await request.get('/api/v1/settings')).json();
    return snapshot.gateway_providers.items.find(provider => provider.id === id)?.base_url;
  }).toBe('http://127.0.0.1:2/v1');
  await page.reload();
  await expect(baseURL).toHaveValue('http://127.0.0.1:2/v1');
  const key = card.getByLabel('New inline API key', { exact: true });
  await key.fill('test-provider-draft-key');
  await key.press('Tab');
  await expect(key).toHaveValue('');
  await expect(key).toHaveAttribute('placeholder', 'Stored · enter to replace');
});

test('System One provider acknowledgements preserve the next field before blur', async ({ page, request }) => {
  const id = 'draft-system-one';
  const fixture = await (await request.get('/_test/fixture')).json();
  expect((await request.post('/api/v1/settings/system-one/providers', { data: {
    id, uri: fixture.decision_uri, api_key_env: ''
  } })).ok()).toBe(true);
  await page.goto('/settings?section=system-one');
  const card = page.locator('.system-one-provider-card').filter({ hasText: id });
  let release;
  const gate = new Promise(resolve => { release = resolve; });
  let notify;
  const held = new Promise(resolve => { notify = resolve; });
  let first = true;
  await page.route('**/api/v1/settings/system-one/providers/*', async route => {
    if (route.request().method() !== 'PUT') return route.continue();
    const response = await route.fetch();
    if (first) { first = false; notify(); await gate; }
    await route.fulfill({ response });
  });
  const providerID = card.getByRole('textbox', { name: 'Provider ID', exact: true });
  const environment = card.getByRole('textbox', { name: 'API key environment', exact: true });
  await providerID.fill(id + '-renamed');
  await environment.click();
  await held;
  await environment.fill('Q_STUDIO_DECISION_KEY');
  release();
  await expect(page.locator('.save-state')).toHaveText('Saved');
  await expect(environment).toHaveValue('Q_STUDIO_DECISION_KEY');
  await environment.press('Tab');
  await expect.poll(async () => {
    const snapshot = await (await request.get('/api/v1/settings')).json();
    return snapshot.system_one.providers.find(provider => provider.id === id + '-renamed')?.api_key_env;
  }).toBe('Q_STUDIO_DECISION_KEY');
});

test('embedding polling preserves an unsubmitted provider rename and URL', async ({ page, request }) => {
  const id = 'draft-poll-provider';
  expect((await request.post('/api/v1/settings/gateway/providers', { data: {
    id, type: 'openai-compatible', enabled: false, base_url: 'http://127.0.0.1:1/v1'
  } })).ok()).toBe(true);
  let reads = 0;
  const withPendingEmbedding = snapshot => {
    snapshot.models.embedding_change = {
      id: 'draft-poll', model: 'default/test-model', dimensions: 3,
      state: 'pending', requested_at: new Date().toISOString()
    };
    return snapshot;
  };
  await page.route('**/api/v1/settings', async route => {
    const response = await route.fetch();
    reads += 1;
    await route.fulfill({ json: withPendingEmbedding(await response.json()) });
  });
  await page.route(`**/api/v1/settings/gateway/providers/${id}`, async route => {
    const response = await route.fetch();
    await route.fulfill({ json: withPendingEmbedding(await response.json()) });
  });
  await page.goto('/settings?section=providers');
  const card = page.locator('.provider-card').filter({ has: page.getByRole('heading', { name: id, exact: true }) });
  const baseURL = card.getByRole('textbox', { name: 'Base URL', exact: true });
  await baseURL.fill('http://127.0.0.1:3/v1');
  const before = reads;
  await expect.poll(() => reads).toBeGreaterThan(before);
  await expect(baseURL).toHaveValue('http://127.0.0.1:3/v1');
  await baseURL.press('Tab');
  await expect(page.locator('.save-state')).toHaveText('Saved');
  const providerID = card.getByRole('textbox', { name: 'Provider ID', exact: true });
  await providerID.fill(id + '-renamed');
  const renamed = page.locator('.provider-card').filter({ has: page.getByRole('heading', { name: id + '-renamed', exact: true }) });
  const beforeRename = reads;
  await expect.poll(() => reads).toBeGreaterThan(beforeRename);
  await expect(renamed.getByRole('textbox', { name: 'Provider ID', exact: true })).toHaveValue(id + '-renamed');
  await page.screenshot({ path: join(tmpdir(), 'q-provider-draft-preserved.png') });
  await renamed.getByRole('textbox', { name: 'Provider ID', exact: true }).press('Tab');
  await expect.poll(async () => {
    const snapshot = await (await request.get('/api/v1/settings')).json();
    return snapshot.gateway_providers.items.some(provider => provider.id === id + '-renamed');
  }).toBe(true);
});
