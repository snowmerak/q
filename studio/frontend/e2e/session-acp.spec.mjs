import { test, expect } from '@playwright/test';
import { tmpdir } from 'node:os';
import { join } from 'node:path';

test('ACP runtimes install npm adapters and show native or unavailable runtimes', async ({ page }) => {
  let installs = 0;
  const codex = { preset: 'codex', name: 'Codex', package: '@agentclientprotocol/codex-acp', installed: false, can_install: true, detail: 'Installs automatically on first use with npm' };
  const adapters = [codex,
    { preset: 'claude', name: 'Claude Code', package: '@agentclientprotocol/claude-agent-acp', installed: false, can_install: false, detail: 'npm is required to install ACP adapters' },
    { preset: 'grok', name: 'Grok', installed: true, can_install: false, detail: 'Native ACP · grok agent stdio' }];
  let complete;
  const gate = new Promise(resolve => { complete = resolve; });
  let started;
  const pending = new Promise(resolve => { started = resolve; });
  await page.route('**/api/v1/settings/acp/adapters', async route => {
    if (route.request().method() === 'GET') return route.fulfill({ json: { adapters } });
    installs++;
    expect(route.request().postDataJSON()).toEqual({ preset: 'codex' });
    started();
    await gate;
    return route.fulfill({ json: { ...codex, installed: true, can_install: false, version: '1.2.3', detail: 'Ready' } });
  });
  await page.goto('/settings?section=providers&provider_tab=acp');
  const codexCard = page.getByRole('article', { name: 'Codex ACP runtime', exact: true });
  const claudeCard = page.getByRole('article', { name: 'Claude Code ACP runtime', exact: true });
  const grokCard = page.getByRole('article', { name: 'Grok ACP runtime', exact: true });
  await expect(codexCard).toContainText('Installs automatically on first use');
  await expect(codexCard.getByRole('button', { name: 'Install adapter', exact: true })).toBeEnabled();
  await expect(claudeCard.getByRole('button', { name: 'Install adapter', exact: true })).toBeDisabled();
  await expect(grokCard).toContainText('Native ACP');
  await expect(grokCard.getByRole('button')).toHaveCount(0);
  expect(installs).toBe(0);
  await codexCard.getByRole('button', { name: 'Install adapter', exact: true }).click();
  await pending;
  await expect(codexCard.getByRole('button', { name: 'Installing…', exact: true })).toBeDisabled();
  complete();
  await expect(codexCard).toContainText('Ready · 1.2.3');
  await expect(codexCard.getByRole('button')).toHaveCount(0);
  expect(installs).toBe(1);
  await page.setViewportSize({ width: 390, height: 844 });
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
  await page.screenshot({ path: join(tmpdir(), 'q-acp-adapter-install.png'), fullPage: true });
});

test('Providers groups API and ACP while Sessions chats with an ACP agent', async ({ page, request }) => {
  const fixture = await (await request.get('/_test/fixture')).json();
  const settings = await (await request.get('/api/v1/settings/acp')).json();
  const id = 'session-acp';
  expect((await request.put('/api/v1/settings/acp', { data: {
    connections: { ...settings.connections, [id]: { command: fixture.acp_executable, args: [], env: { Q_STUDIO_ACP_FIXTURE: '1' } } }
  } })).ok()).toBe(true);
  const errors = [];
  page.on('pageerror', error => errors.push(error.message));
  await page.goto('/settings?section=providers');
  const api = page.getByRole('tab', { name: /^API Provider/ });
  const acp = page.getByRole('tab', { name: /^ACP External/ });
  await expect(api).toHaveAttribute('aria-selected', 'true');
  await api.focus();
  await api.press('ArrowRight');
  await expect(acp).toBeFocused();
  await acp.click();
  await expect(acp).toHaveAttribute('aria-selected', 'true');
  await expect(page.getByRole('heading', { name: 'ACP providers', exact: true })).toBeVisible();
  const card = page.locator('.integration-editor').filter({ has: page.getByRole('heading', { name: id, exact: true }) });
  await expect(card.getByLabel('Command', { exact: true })).toHaveValue(fixture.acp_executable);
  await page.reload();
  await expect(acp).toHaveAttribute('aria-selected', 'true');
  await page.screenshot({ path: join(tmpdir(), 'q-acp-providers.png') });
  await page.setViewportSize({ width: 390, height: 844 });
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
  await expect(acp).toBeVisible();
  await page.screenshot({ path: join(tmpdir(), 'q-acp-providers-mobile.png') });
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.goto('/settings?section=subagents');
  await page.getByRole('button', { name: /^External role bindings/ }).click();
  await expect(page.getByLabel('Connection ID', { exact: true })).toHaveCount(0);

  const chat = await (await request.post('/api/v1/chats', { data: {} })).json();
  try {
    await page.goto(`/sessions/${chat.registration_id}`);
    const picker = page.getByRole('combobox', { name: 'Session model', exact: true });
    const effort = page.getByRole('combobox', { name: 'Reasoning effort', exact: true });
    const composer = page.getByRole('textbox', { name: 'Message Q', exact: true });
    await picker.selectOption(`acp:${id}`);
    await expect(picker).toBeEnabled();
    await expect(effort).toBeDisabled();
    await page.getByLabel('More session actions', { exact: true }).click();
    await expect(page.getByRole('button', { name: 'Compact context', exact: true })).toBeDisabled();
    await page.keyboard.press('Escape');
    await expect(effort.locator('option:checked')).toHaveText('Agent default');
    await composer.fill('hello ACP');
    await composer.press('Enter');
    await expect(page.getByText('ACP answer: hello ACP', { exact: true })).toBeVisible();
    await expect(picker).toBeEnabled();
    await composer.fill('permission');
    await composer.press('Enter');
    await expect(page.getByRole('heading', { name: 'Edit fixture file', exact: true })).toBeVisible();
    await expect(picker).toBeDisabled();
    await page.getByRole('button', { name: /^Allow/ }).click();
    await expect(page.getByText('ACP answer: permission', { exact: true })).toBeVisible();
    await expect(picker).toBeEnabled();
    await page.reload();
    await expect(picker).toHaveValue(`acp:${id}`);
    await expect(page.getByText('ACP answer: hello ACP', { exact: true })).toBeVisible();
    await expect(effort).toBeDisabled();
    await page.screenshot({ path: join(tmpdir(), 'q-session-acp.png') });
    await picker.selectOption('');
    await expect(effort).toBeEnabled();
    await expect(picker).toHaveValue('');
    expect(errors).toEqual([]);
  } finally {
    await request.delete(`/api/v1/chats/${chat.registration_id}`);
  }
});

test('ACP connection saves keep the next JSON field being edited', async ({ page, request }) => {
  const id = 'acp-json-draft';
  const fixture = await (await request.get('/_test/fixture')).json();
  const settings = await (await request.get('/api/v1/settings/acp')).json();
  expect((await request.put('/api/v1/settings/acp', { data: {
    connections: { ...settings.connections, [id]: { command: fixture.acp_executable, args: [], env: {}, disabled: true } }
  } })).ok()).toBe(true);
  await page.goto('/settings?section=providers&provider_tab=acp');
  const card = page.locator('.integration-editor').filter({ has: page.getByRole('heading', { name: id, exact: true }) });
  const args = card.getByLabel('Arguments · JSON', { exact: true });
  const env = card.getByLabel('Child environment · JSON', { exact: true });
  let release;
  const gate = new Promise(resolve => { release = resolve; });
  let notify;
  const held = new Promise(resolve => { notify = resolve; });
  let first = true;
  await page.route('**/api/v1/settings/acp', async route => {
    if (route.request().method() !== 'PUT') return route.continue();
    const response = await route.fetch();
    if (first) { first = false; notify(); await gate; }
    await route.fulfill({ response });
  });
  await args.fill('["--first"]');
  await env.click();
  await held;
  await env.fill('{"DRAFT_TOKEN":"test-secret"}');
  release();
  await expect(page.locator('#provider-panel-acp .integration-success')).toContainText('Saved');
  await expect(args).toHaveValue('["--first"]');
  await expect(env).toHaveValue('{"DRAFT_TOKEN":"test-secret"}');
  await env.press('Tab');
  await expect(env).toHaveValue('{"DRAFT_TOKEN":"********"}');
  await page.reload();
  await expect(args).toHaveValue('["--first"]');
  await expect(env).toHaveValue('{"DRAFT_TOKEN":"********"}');
});
