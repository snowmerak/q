import { test, expect } from '@playwright/test';

for (const change of ['rename', 'type', 'enable']) {
  test(`ChatGPT ${change} waits for the saved Gateway before reading account status`, async ({ page, request }) => {
    const id = `chatgpt-save-${change}`;
    const nextID = change === 'rename' ? `${id}-renamed` : id;
    const added = await request.post('/api/v1/settings/gateway/providers', { data: {
      id, type: change === 'type' ? 'openai-compatible' : 'chatgpt',
      enabled: change !== 'enable', base_url: change === 'type' ? 'http://127.0.0.1:1/v1' : ''
    } });
    expect(added.ok()).toBeTruthy();
    const statusRequests = [];
    let saved = false;
    let saveStarted = false;
    let releaseSave;
    const saveGate = new Promise((resolve) => { releaseSave = resolve; });
    await page.route(`**/api/v1/settings/gateway/providers/${id}`, async (route) => {
      if (route.request().method() !== 'PUT') return route.continue();
      saveStarted = true;
      await saveGate;
      const response = await route.fetch();
      saved = response.ok();
      await route.fulfill({ response });
    });
    await page.route('**/api/v1/settings/gateway/providers/*/chatgpt', async (route) => {
      const requestedID = new URL(route.request().url()).pathname.split('/').at(-2);
      if (requestedID !== id && requestedID !== nextID) return route.continue();
      statusRequests.push(requestedID);
      const available = saved ? requestedID === nextID : change === 'rename' && requestedID === id;
      if (!available) return route.fulfill({ status: 404, json: { error: 'provider does not exist' } });
      await route.fulfill({ json: { active_profile: 'account-a', pending: false, profiles: [
        { id: 'account-a', label: 'Account A', verified: true, connected: true, plan_enabled: true }
      ] } });
    });
    await page.goto('/settings?section=providers');
    const card = page.locator('.provider-card').filter({ hasText: id });
    if (change === 'rename') {
      await expect(card.getByText('ChatGPT plan connected to Q', { exact: true })).toBeVisible();
      await card.getByLabel('Provider ID', { exact: true }).fill(nextID);
      // Editing alone must not query an ID that the Gateway does not have yet.
      await expect(card.getByRole('button', { name: 'Reconnect with ChatGPT', exact: true })).toBeDisabled();
      await card.getByLabel('Model prefix', { exact: true }).click();
    } else if (change === 'type') {
      await card.getByRole('combobox', { name: 'API type', exact: true }).selectOption('chatgpt');
    } else {
      await card.getByRole('checkbox', { name: 'Disabled', exact: true }).check();
    }
    try {
      await expect.poll(() => saveStarted).toBe(true);
      await expect(card.getByRole('button', { name: /^(Continue|Reconnect) with ChatGPT$/ })).toBeDisabled();
      await expect(card.getByRole('alert')).toHaveCount(0);
      expect(statusRequests).toEqual(change === 'rename' ? [id] : []);
    } finally {
      releaseSave();
    }
    await expect(card.getByText('ChatGPT plan connected to Q', { exact: true })).toBeVisible();
    await expect(card.getByRole('button', { name: 'Reconnect with ChatGPT', exact: true })).toBeEnabled();
    await expect.poll(() => statusRequests.at(-1)).toBe(nextID);
    await expect(card.getByRole('alert')).toHaveCount(0);
  });
}

test('ChatGPT provider uses account actions and keeps credentials out of settings', async ({ page, request }) => {
  const errors = [];
  page.on('pageerror', (error) => errors.push(error.message));
  const added = await request.post('/api/v1/settings/gateway/providers', { data: { id: 'chatgpt-ui', type: 'chatgpt', enabled: true } });
  expect(added.ok()).toBeTruthy();
  const initial = await request.get('/api/v1/settings/gateway/providers/chatgpt-ui/chatgpt');
  expect(initial.ok()).toBeTruthy();
  expect(await initial.json()).toMatchObject({ active_profile: '', profiles: [], pending: false });
  expect(await initial.text()).not.toMatch(/access_token|refresh_token|client_id|ext_agent_host_id/);
  let active = 'account-a';
  let connected = true;
  const actions = [];
  await page.route('**/api/v1/settings/gateway/providers/chatgpt-ui/chatgpt**', async (route) => {
    const req = route.request();
    if (req.method() === 'POST') {
      const name = req.url().split('/').pop();
      actions.push({ name, body: req.postDataJSON() });
      if (name === 'select') active = req.postDataJSON().profile;
      if (name === 'disconnect') connected = false;
    }
    await route.fulfill({ json: { active_profile: active, pending: false, profiles: [
      { id: 'account-a', label: 'Account A · first', verified: true, connected, plan_enabled: true },
      { id: 'account-b', label: 'Account B · second', verified: true, connected, plan_enabled: true }
    ] } });
  });
  await page.goto('/settings?section=providers');
  const card = page.locator('.provider-card').filter({ hasText: 'chatgpt-ui' });
  await expect(card.getByText('ChatGPT plan connected to Q', { exact: true })).toBeVisible();
  await expect(card.getByLabel('Base URL', { exact: true })).toHaveCount(0);
  await expect(card.getByLabel('New inline API key', { exact: true })).toHaveCount(0);
  await card.getByLabel('ChatGPT account used by Q').selectOption('account-b');
  await expect(card.getByLabel('ChatGPT account used by Q')).toHaveValue('account-b');
  await card.getByRole('button', { name: 'Disconnect', exact: true }).click();
  await expect(card.getByRole('button', { name: 'Continue with ChatGPT', exact: true })).toBeVisible();
  expect(actions).toEqual([{ name: 'select', body: { profile: 'account-b' } }, { name: 'disconnect', body: {} }]);
  await page.reload();
  await expect(card.getByLabel('ChatGPT account used by Q')).toHaveValue('account-b');
  await expect(card.getByRole('button', { name: 'Continue with ChatGPT', exact: true })).toBeVisible();
  expect(errors).toEqual([]);
});
