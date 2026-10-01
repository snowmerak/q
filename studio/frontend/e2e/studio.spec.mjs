import { test, expect } from '@playwright/test';
import { tmpdir } from 'node:os';
import { join } from 'node:path';

test.beforeEach(async ({ page }) => {
  const errors = [];
  page.testErrors = errors;
  page.on('pageerror', (error) => errors.push(error.message));
  page.on('console', (message) => {
    if (message.type() === 'error' && !message.text().startsWith('Failed to load resource:')) errors.push(message.text());
  });
  page.on('response', (response) => {
    // A newly registered session has no run yet; the UI handles this 404.
    if (response.status() >= 400 && !(response.status() === 404 && new URL(response.url()).pathname.endsWith('/runs/latest'))) {
      errors.push(`${response.status()} ${response.url()}`);
    }
  });
  await page.goto('/sessions');
  await expect(page.getByRole('heading', { name: 'Sessions', exact: true })).toBeVisible();
});

test.afterEach(async ({ page }) => {
  expect(page.testErrors, 'browser runtime errors').toEqual([]);
});

test('sidebar collapses, keeps navigation usable, and remembers its state', async ({ page }, testInfo) => {
  const sidebar = page.getByRole('complementary', { name: 'Studio navigation' });
  for (const width of [1440, 390]) {
    await page.setViewportSize({ width, height: 900 });
    await page.goto('/sessions');
    const expandedMain = await page.locator('main').boundingBox();
    await sidebar.getByRole('button', { name: 'Collapse sidebar' }).click();
    const expand = sidebar.getByRole('button', { name: 'Expand sidebar' });
    await expect(expand).toHaveAttribute('aria-expanded', 'false');
    await expect(sidebar.getByRole('button', { name: 'Sessions', exact: true })).toHaveAttribute('aria-current', 'page');
    if (width === 1440) {
      expect((await sidebar.boundingBox()).width).toBe(72);
      expect((await page.locator('main').boundingBox()).width).toBeGreaterThan(expandedMain.width);
    }
    await sidebar.getByRole('button', { name: 'Help', exact: true }).click();
    await expect(page.getByRole('heading', { name: 'Help', exact: true })).toBeVisible();
    await page.reload();
    await expect(expand).toHaveAttribute('aria-expanded', 'false');
    await expect(sidebar.getByRole('button', { name: 'Help', exact: true })).toHaveAttribute('aria-current', 'page');
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
    await page.screenshot({ path: testInfo.outputPath(`sidebar-collapsed-${width}.png`) });
    await expand.focus();
    await expand.press('Enter');
    await expect(sidebar.getByRole('button', { name: 'Collapse sidebar' })).toHaveAttribute('aria-expanded', 'true');
    await page.reload();
    await expect(sidebar.getByRole('button', { name: 'Collapse sidebar' })).toBeVisible();
    if (width === 1440) expect((await sidebar.boundingBox()).width).toBe(244);
  }
});

test('help exposes the command reference and scrolls to every section on desktop and mobile', async ({ page }) => {
  for (const viewport of [{ width: 1440, height: 720 }, { width: 900, height: 650 }, { width: 390, height: 844 }]) {
    await page.setViewportSize(viewport);
    await page.goto('/help');
    await expect(page).toHaveURL(/\/help$/);
    await expect(page).toHaveTitle('Q Studio');
    await expect(page.getByRole('heading', { name: 'Help', exact: true })).toBeVisible();
    const guide = page.getByRole('region', { name: 'Studio help guide' });
    const index = page.getByRole('navigation', { name: 'Help sections' });
    await expect(guide.getByRole('heading', { name: 'Sessions, commands, and everyday workflows' })).toBeVisible();
    await expect(guide.locator('#help-cli .reference-list > div')).toHaveCount(21);
    await expect(guide.locator('#help-slash .reference-list > div')).toHaveCount(25);
    expect(await guide.evaluate((element) => element.scrollHeight > element.clientHeight)).toBe(true);
    await guide.hover();
    await page.mouse.wheel(0, 700);
    await expect.poll(() => guide.evaluate((element) => element.scrollTop)).toBeGreaterThan(0);
    await index.getByRole('button', { name: 'CLI commands', exact: true }).click();
    await expect(guide.getByRole('heading', { name: 'CLI commands', exact: true })).toBeInViewport();
    await expect(guide.locator('#help-cli')).toContainText('q commit');
    await expect(guide.locator('#help-cli')).toContainText('interactive commit session in this terminal');
    await index.getByRole('button', { name: 'Slash commands', exact: true }).click();
    await expect(guide.getByRole('heading', { name: 'Slash commands', exact: true })).toBeInViewport();
    await expect(guide.locator('#help-slash')).toContainText('Studio chat currently sends text directly to the default loop');
    await index.getByRole('button', { name: 'Keyboard shortcuts', exact: true }).click();
    await guide.getByText('Terminal commit session', { exact: true }).click();
    await expect(guide.locator('details[open]')).toContainText('Approve and create the proposed commit');
    await index.getByRole('button', { name: 'Commit session', exact: true }).click();
    await expect(guide.getByRole('heading', { name: 'Commit session', exact: true })).toBeInViewport();
    await index.getByRole('button', { name: 'Files', exact: true }).click();
    await expect(guide.getByRole('heading', { name: 'Files: Raw and Diff', exact: true })).toBeInViewport();
    await index.getByRole('button', { name: 'Recovery', exact: true }).click();
    await expect(guide.getByRole('heading', { name: 'Recovery', exact: true })).toBeInViewport();
    await guide.focus();
    await guide.press('Home');
    await expect.poll(() => guide.evaluate((element) => element.scrollTop)).toBe(0);
    await guide.press('End');
    await expect(guide.getByText('Service unavailable', { exact: true })).toBeInViewport();
    await guide.press('Home');
    await expect.poll(() => guide.evaluate((element) => element.scrollTop)).toBe(0);
    await page.evaluate(() => window.scrollTo(0, 0));
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
    await page.screenshot({ path: join(tmpdir(), `q-studio-help-${viewport.width}.png`) });
  }
  await page.goto('/sessions');
  await page.keyboard.press('?');
  await expect(page.getByRole('heading', { name: 'Help', exact: true })).toBeVisible();
  await page.keyboard.press('Control+k');
  await expect(page.getByRole('complementary', { name: 'Studio navigation' }).getByRole('button', { name: 'Help', exact: true })).toBeFocused();
});

test('project selection refreshes workspaces and clears the previous selection', async ({ page, request }) => {
  const fixture = await (await request.get('/_test/fixture')).json();
  await page.getByTitle('Add or create session').click();
  const dialog = page.getByRole('dialog', { name: 'Add a root session' });
  const project = dialog.getByRole('combobox', { name: 'Project', exact: true });
  const workspace = dialog.getByRole('combobox', { name: 'Workspace', exact: true });
  const load = dialog.locator('.project-workspace-picker').getByRole('button', { name: 'Load sessions' });
  await expect(workspace).toBeDisabled();
  await project.selectOption({ label: 'Integration' });
  await expect(workspace.locator('option')).toHaveText(['Select a workspace', fixture.root, fixture.other]);
  await expect(load).toBeDisabled();
  await workspace.selectOption(fixture.root);
  await expect(load).toBeEnabled();
  await project.selectOption({ label: 'Solo' });
  await expect(workspace.locator('option')).toHaveCount(2);
  await expect(load).toBeEnabled();
  await project.selectOption({ label: 'Integration' });
  await expect(workspace).toHaveValue('');
  await expect(load).toBeDisabled();
  await workspace.selectOption(fixture.root);
  await load.click();
  await expect(dialog.getByLabel('Workspace directory')).toHaveValue(fixture.root);
  await expect(dialog.getByRole('button', { name: /Create new session/ })).toBeVisible();
});

test('project dialog creates, cancels edits, updates, and deletes a saved project', async ({ page, request }) => {
  const fixture = await (await request.get('/_test/fixture')).json();
  // An empty registry makes Create project start a new project instead of
  // editing the currently selected session's project.
  const registry = await (await request.get('/api/v1/registered-sessions')).json();
  for (const session of registry.sessions) {
    expect((await request.delete(`/api/v1/registered-sessions/${session.registration_id}`)).ok()).toBe(true);
  }
  await page.reload();
  await page.getByTitle('Create project', { exact: true }).click();
  const create = page.getByRole('dialog', { name: 'Create project', exact: true });
  await create.getByPlaceholder('Project name').fill('UI refactor project');
  await create.getByPlaceholder('/path/to/workspace').fill(fixture.project_root);
  await create.getByRole('button', { name: 'Add', exact: true }).click();
  await create.getByRole('button', { name: 'Choose workspace folder' }).click();
  const browser = page.getByRole('dialog', { name: 'Choose a folder' });
  await browser.getByRole('textbox', { name: 'Directory path' }).fill(fixture.project_other);
  await browser.getByRole('button', { name: 'Go', exact: true }).click();
  await expect(browser.locator('footer code')).toHaveText(fixture.project_other);
  await browser.getByRole('button', { name: 'Choose folder', exact: true }).click();
  await expect(create.getByPlaceholder('/path/to/workspace')).toHaveValue(fixture.project_other);
  await expect(create.locator('.auxiliary-list > div')).toHaveCount(1);
  await create.getByRole('button', { name: 'Add', exact: true }).click();
  await expect(create.locator('.auxiliary-list > div')).toHaveCount(2);
  await create.getByRole('button', { name: 'Save', exact: true }).click();
  await expect(create).toHaveCount(0);

  const edit = page.getByRole('dialog', { name: 'Edit project', exact: true });
  await page.getByRole('button', { name: 'Edit project UI refactor project', exact: true }).click();
  await edit.getByPlaceholder('Project name').fill('Unsaved name');
  await edit.getByRole('button', { name: `Remove ${fixture.project_other}`, exact: true }).click();
  await edit.getByRole('button', { name: 'Cancel', exact: true }).click();
  await page.getByRole('button', { name: 'Edit project UI refactor project', exact: true }).click();
  await expect(edit.getByPlaceholder('Project name')).toHaveValue('UI refactor project');
  await expect(edit.locator('.auxiliary-list > div')).toHaveCount(2);
  await edit.getByPlaceholder('Project name').fill('UI refactor updated');
  await edit.getByRole('button', { name: `Remove ${fixture.project_other}`, exact: true }).click();
  await edit.getByRole('button', { name: 'Save', exact: true }).click();
  await expect(edit).toHaveCount(0);
  await page.reload();
  await page.getByRole('button', { name: 'Edit project UI refactor updated', exact: true }).click();
  await expect(edit.locator('.auxiliary-list > div')).toHaveCount(1);
  const projects = (await (await request.get('/api/v1/projects')).json()).projects;
  expect(projects.find((project) => project.name === 'UI refactor updated').workspace_roots).toEqual([fixture.project_root]);
  page.once('dialog', (dialog) => dialog.accept());
  await edit.getByRole('button', { name: 'Delete', exact: true }).click();
  await expect(edit).toHaveCount(0);
  await expect(page.getByRole('button', { name: 'Edit project UI refactor updated', exact: true })).toHaveCount(0);
});

test('folder browser closes before registration and registers the selected workspace', async ({ page, request }) => {
  const fixture = await (await request.get('/_test/fixture')).json();
  const home = (await (await request.get('/api/v1/directories')).json()).current;
  await page.getByTitle('Add or create session').click();
  const registry = page.getByRole('dialog', { name: 'Add a root session' });
  await registry.getByRole('button', { name: 'Choose repository folder' }).click();
  const browser = page.getByRole('dialog', { name: 'Choose a folder' });
  await expect(browser.getByRole('textbox', { name: 'Directory path' })).toHaveValue(home);
  await page.keyboard.press('Escape');
  await expect(browser).toHaveCount(0);
  await expect(registry).toBeVisible();
  await registry.getByRole('button', { name: 'Choose repository folder' }).click();
  await expect(browser.getByRole('textbox', { name: 'Directory path' })).toHaveValue(home);
  await browser.getByRole('textbox', { name: 'Directory path' }).fill(fixture.root);
  await browser.getByRole('button', { name: 'Go', exact: true }).click();
  await expect(browser.locator('footer code')).toHaveText(fixture.root);
  await browser.getByRole('button', { name: 'Choose folder', exact: true }).click();
  await expect(browser).toHaveCount(0);
  await expect(registry.getByLabel('Workspace directory')).toHaveValue(fixture.root);
  await registry.getByRole('button', { name: /Create new session/ }).click();
  await expect(registry).toHaveCount(0);
  await expect(page.locator('.composer textarea')).toBeEnabled();
  const selectedID = new URL(page.url()).pathname.split('/').at(-1);
  const registered = (await (await request.get('/api/v1/registered-sessions')).json()).sessions;
  expect(registered.find((session) => session.registration_id === selectedID).workspace_root).toBe(fixture.root);
  await page.getByTitle('Add or create session').click();
  await expect(registry.getByLabel('Workspace directory')).toHaveValue('');
  await page.keyboard.press('Escape');
  await expect(registry).toHaveCount(0);
});

test('nested delegation selection and deletion preserve the root and parent branch', async ({ page, request }) => {
  const fixture = await (await request.get('/_test/fixture')).json();
  const response = await request.post('/api/v1/registered-sessions', { data: {
    workspace_root: fixture.tree_root, session_id: fixture.tree_session_id
  } });
  expect(response.status()).toBe(201);
  const registration = await response.json();
  // Start on another root, then select this tree through a child branch.
  const other = await request.post('/api/v1/registered-sessions', { data: {
    workspace_root: fixture.root, session_id: fixture.session_id
  } });
  expect(other.status()).toBe(201);
  const otherRegistration = await other.json();
  await page.goto('/sessions/' + otherRegistration.registration_id);
  const rail = page.getByRole('complementary', { name: 'Registered session tree' });
  const senior = rail.getByRole('button', { name: 'senior-developer completed', exact: true });
  const junior = rail.getByRole('button', { name: 'junior-developer completed', exact: true });
  await senior.click();
  await expect(page).toHaveURL(new RegExp('/sessions/' + registration.registration_id + '$'));
  await expect(page.locator('.transcript').getByRole('heading', { name: 'Senior fixture', exact: true })).toBeVisible();
  await expect(senior).toHaveClass(/active/);
  await expect(page.locator('.composer textarea')).toHaveCount(0);
  await junior.click();
  await expect(page.locator('.transcript').getByRole('heading', { name: 'Junior fixture', exact: true })).toBeVisible();
  await expect(junior).toHaveClass(/active/);
  expect(await senior.evaluate((el) => el.style.getPropertyValue('--tree-depth'))).toBe('1');
  expect(await junior.evaluate((el) => el.style.getPropertyValue('--tree-depth'))).toBe('2');
  page.once('dialog', (dialog) => dialog.accept());
  const removed = page.waitForResponse((r) => r.request().method() === 'DELETE' && new URL(r.url()).pathname.endsWith('/delegations'));
  await page.getByRole('button', { name: 'Delete completed delegation', exact: true }).click();
  const deletion = await removed;
  expect(deletion.ok()).toBe(true);
  expect(new URL(deletion.url()).searchParams.get('path')).toBe('senior-child/junior-child');
  await expect(junior).toHaveCount(0);
  await expect(senior).toBeVisible();
  await expect(page.locator('.transcript').getByRole('heading', { name: 'Root fixture', exact: true })).toBeVisible();
  await expect(page.locator('.composer textarea')).toBeEnabled();
  await senior.click();
  await expect(page.locator('.transcript').getByRole('heading', { name: 'Senior fixture', exact: true })).toBeVisible();
  await rail.locator('.session-select').filter({ hasText: 'Delegation fixture' }).click();
  await expect(page.locator('.transcript').getByRole('heading', { name: 'Root fixture', exact: true })).toBeVisible();
  const saved = await (await request.get('/api/v1/registered-sessions')).json();
  const tree = saved.sessions.find((item) => item.registration_id === registration.registration_id);
  expect(tree.delegations).toHaveLength(1);
  expect(tree.delegations[0].bookmark.invocation_id).toBe('senior-child');
  expect(tree.delegations[0].children || []).toHaveLength(0);
  expect((await request.delete('/api/v1/registered-sessions/' + registration.registration_id)).ok()).toBe(true);
});

test('guidance redirects a real run without refresh and renders markdown and code', async ({ page, request }, testInfo) => {
  const fixture = await (await request.get('/_test/fixture')).json();
  const registered = await request.post('/api/v1/registered-sessions', { data: { workspace_root: fixture.root, create: true } });
  expect(registered.status()).toBe(201);
  const session = await registered.json();
  await page.goto(`/sessions/${session.registration_id}`);
  const composer = page.locator('.composer textarea');
  await expect(composer).toBeEnabled();
  await composer.fill('wait for guidance');
  await composer.press('Enter');
  await expect(page.getByRole('heading', { name: 'Which direction?' })).toBeVisible();
  await composer.fill('write the requested file');
  await composer.press('Enter');
  await expect(page.getByRole('heading', { name: 'Which direction?' })).toHaveCount(0);
  await expect(composer).toHaveAttribute('placeholder', 'Ask Q to work in this repository…');
  await expect(page.locator('.transcript').getByRole('heading', { name: 'Studio result' })).toBeVisible();
  await expect(page.locator('.transcript .hljs.language-go')).toContainText('package main');
  await expect(page.getByRole('button', { name: 'Copy go code' }).first()).toBeVisible();
  await page.locator('.transcript').getByRole('heading', { name: 'Studio result' }).scrollIntoViewIfNeeded();
  await page.screenshot({ path: testInfo.outputPath('guided-chat.png') });
  await page.reload();
  await expect(page.locator('.transcript').getByRole('heading', { name: 'Studio result' })).toBeVisible();
  // Tool call IDs may repeat in a later turn. They must not collide with the
  // first turn's persisted messages when Svelte renders the transcript.
  await composer.fill('write again');
  await composer.press('Enter');
  await expect(page.locator('.transcript').getByRole('heading', { name: 'Studio result' })).toHaveCount(2);
  await expect(composer).toHaveAttribute('placeholder', 'Ask Q to work in this repository…');
  const detail = await request.get(`/api/v1/sessions/${session.session.session_id}?workspace_root=${encodeURIComponent(fixture.root)}`);
  const transcript = (await detail.json()).transcript;
  expect(transcript.some((message) => message.role === 'user' && message.content.includes('write the requested file'))).toBe(true);
});

test('switching sessions aborts polling and rejects an old run response', async ({ page, request }) => {
  const fixture = await (await request.get('/_test/fixture')).json();
  const first = await (await request.post('/api/v1/registered-sessions', { data: { workspace_root: fixture.root, create: true } })).json();
  const second = await (await request.post('/api/v1/registered-sessions', { data: { workspace_root: fixture.other, create: true } })).json();
  let release;
  const gate = new Promise((resolve) => { release = resolve; });
  let notifyHeld;
  const held = new Promise((resolve) => { notifyHeld = resolve; });
  let notifyDelivered;
  const delivered = new Promise((resolve) => { notifyDelivered = resolve; });
  const matchesRun = (url) => new URL(url).pathname.startsWith(`/api/v1/sessions/${first.session.session_id}/runs/`) && new URL(url).pathname.endsWith('/events');
  let intercepted = false;
  await page.route('**/events?**', async (route) => {
    if (!matchesRun(route.request().url()) || intercepted) return route.continue();
    intercepted = true;
    const response = await route.fetch();
    const body = await response.json();
    body.run.status = 'running';
    body.events = [{ cursor: 1, at: new Date().toISOString(), event: { type: 'stream', kind: 'response', start: true, content: 'STALE RUN CONTENT' } }];
    notifyHeld();
    await gate;
    await route.fulfill({ response, json: body });
    notifyDelivered();
  });
  try {
    await page.goto(`/sessions/${first.registration_id}`);
    const composer = page.locator('.composer textarea');
    await expect(composer).toBeEnabled();
    await composer.fill('wait for guidance');
    await composer.press('Enter');
    await held;
    const aborted = page.waitForEvent('requestfailed', { predicate: (request) => matchesRun(request.url()) });
    await page.locator('.session-root').filter({ hasText: second.session.session_id.slice(0, 10) }).getByRole('button', { name: /New session/ }).click();
    await aborted;
    await expect(page).toHaveURL(new RegExp(`/sessions/${second.registration_id}$`));
    await expect(composer).toBeEnabled();
    release();
    await delivered;
    await expect(composer).toHaveAttribute('placeholder', 'Ask Q to work in this repository…');
    await expect(page.locator('.chat-heading p')).toHaveText(fixture.other);
    await expect(page.locator('.transcript')).not.toContainText('STALE RUN CONTENT');
    await expect(page.getByRole('heading', { name: 'Which direction?' })).toHaveCount(0);
    // The previous run remains recoverable in its own session after observation stops.
    const latest = await request.get(`/api/v1/sessions/${first.session.session_id}/runs/latest?workspace_root=${encodeURIComponent(fixture.root)}`);
    expect(latest.ok()).toBe(true);
    const run = await latest.json();
    expect(['queued', 'running', 'waiting', 'paused']).toContain(run.status);
    const cancelled = await request.post(`/api/v1/sessions/${first.session.session_id}/runs/${run.id}/commands`, { data: { workspace_root: fixture.root, action: 'cancel' } });
    expect(cancelled.ok()).toBe(true);
  } finally {
    release();
  }
});

test('a delayed session detail cannot replace a newer selection', async ({ page, request }) => {
  const fixture = await (await request.get('/_test/fixture')).json();
  const first = await (await request.post('/api/v1/registered-sessions', { data: { workspace_root: fixture.project_root, create: true } })).json();
  const second = await (await request.post('/api/v1/registered-sessions', { data: { workspace_root: fixture.project_other, create: true } })).json();
  await page.goto(`/sessions/${second.registration_id}`);
  await expect(page.locator('.composer textarea')).toBeEnabled();
  let release;
  const gate = new Promise((resolve) => { release = resolve; });
  let notifyHeld;
  const held = new Promise((resolve) => { notifyHeld = resolve; });
  await page.route(`**/api/v1/sessions/${first.session.session_id}?**`, async (route) => {
    const response = await route.fetch();
    notifyHeld();
    await gate;
    await route.fulfill({ response });
  });
  const rootButton = (session) => page.locator('.session-root').filter({ hasText: session.session.session_id.slice(0, 10) }).getByRole('button', { name: /New session/ });
  try {
    await rootButton(first).click();
    await held;
    await expect(page.locator('.composer textarea')).toBeDisabled();
    await rootButton(second).click();
    await expect(page.locator('.composer textarea')).toBeEnabled();
    const oldResponse = page.waitForResponse((response) => new URL(response.url()).pathname === `/api/v1/sessions/${first.session.session_id}`);
    release();
    await oldResponse;
    // A subsequent round trip gives rendering and the old response handler time to finish.
    await page.getByRole('button', { name: 'Configure session project' }).click();
    await expect(page.getByRole('dialog').locator('.auxiliary-list code')).toHaveText(fixture.project_other);
    await page.keyboard.press('Escape');
    await expect(page).toHaveURL(new RegExp(`/sessions/${second.registration_id}$`));
    await expect(page.locator('.chat-heading p')).toHaveText(fixture.project_other);
  } finally {
    release();
  }
});

test('runtime checkbox saves automatically and survives reload', async ({ page, request }) => {
  await page.goto('/settings?section=runtime');
  const checkbox = page.getByRole('checkbox', { name: 'Garbage collection' });
  await expect(checkbox).toBeEnabled();
  const previous = await checkbox.isChecked();
  const saved = page.waitForResponse((response) => response.url().endsWith('/api/v1/settings/runtime') && response.request().method() === 'PUT');
  await checkbox.setChecked(!previous);
  expect((await saved).ok()).toBe(true);
  const settings = await (await request.get('/api/v1/settings')).json();
  expect(settings.runtime.loom.gc_disabled).toBe(previous);
  await page.reload();
  await expect(checkbox).toBeChecked({ checked: !previous });
});

test('settings retain their save queue and selected section across navigation', async ({ page }) => {
  let snapshots = 0;
  page.on('request', (request) => {
    if (request.method() === 'GET' && new URL(request.url()).pathname === '/api/v1/settings') snapshots++;
  });
  await page.goto('/settings?section=runtime');
  const checkbox = page.getByRole('checkbox', { name: 'Garbage collection' });
  await expect(checkbox).toBeEnabled();
  const previous = await checkbox.isChecked();
  const sections = page.getByRole('complementary', { name: 'Settings sections' });
  await sections.getByRole('button', { name: /^Models/ }).click();
  await expect(page.getByRole('heading', { name: 'Model assignments' })).toBeVisible();
  await page.goBack();
  await expect(checkbox).toBeEnabled();

  let releaseSave;
  const saveGate = new Promise((resolve) => { releaseSave = resolve; });
  let notifySaved;
  const saved = new Promise((resolve) => { notifySaved = resolve; });
  await page.route('**/api/v1/settings/runtime', async (route) => {
    const response = await route.fetch();
    notifySaved();
    await saveGate;
    await route.fulfill({ response });
  });
  try {
    await checkbox.setChecked(!previous);
    await saved;
    const navigation = page.getByRole('complementary', { name: 'Studio navigation' });
    await navigation.getByRole('button', { name: 'Help', exact: true }).click();
    await expect(page.getByRole('heading', { name: 'Help', exact: true })).toBeVisible();
    const saveResponse = page.waitForResponse((response) => response.url().endsWith('/api/v1/settings/runtime') && response.request().method() === 'PUT');
    releaseSave();
    expect((await saveResponse).ok()).toBe(true);
    await navigation.getByRole('button', { name: 'Settings', exact: true }).click();
    await expect(checkbox).toBeChecked({ checked: !previous });
    await expect(page.locator('.save-state')).not.toContainText('Saving');
    expect(snapshots, 'cached settings remain owned by the mounted settings component').toBe(1);
  } finally {
    releaseSave();
  }
});



test('global subagent profiles save roles, permissions, prompts, and deletion', async ({ page, request }) => {
  await page.goto('/settings?section=subagents');
  const context = page.getByRole('textbox', { name: 'Repository context · optional', exact: true });
  await context.fill('');
  await page.getByRole('button', { name: 'Use global only', exact: true }).click();
  await expect(page.getByRole('heading', { name: 'Custom profiles', exact: true })).toBeVisible();
  await page.getByRole('textbox', { name: 'Profile name', exact: true }).fill('ui-inspector');
  const save = (method) => page.waitForResponse((r) => new URL(r.url()).pathname.startsWith('/api/v1/settings/subagents/profiles') && r.request().method() === method);
  let saved = save('POST');
  await page.getByRole('button', { name: 'Add profile', exact: true }).click();
  expect((await saved).ok()).toBe(true);
  const card = page.locator('.subagent-profile').filter({ has: page.getByRole('heading', { name: 'ui-inspector', exact: true }) });
  saved = save('PUT');
  await card.getByRole('combobox', { name: 'Model role', exact: true }).selectOption('reviewer');
  expect((await saved).ok()).toBe(true);
  saved = save('PUT');
  await card.getByRole('textbox', { name: 'System prompt', exact: true }).fill('Review the code and report evidence.');
  await card.getByRole('textbox', { name: 'System prompt', exact: true }).press('Tab');
  expect((await saved).ok()).toBe(true);
  await card.getByPlaceholder('Filter tools').fill('read_file');
  saved = save('PUT');
  await card.getByRole('checkbox', { name: 'read_file', exact: true }).check();
  expect((await saved).ok()).toBe(true);
  await card.getByPlaceholder('Filter delegates').fill('builtin/research');
  saved = save('PUT');
  await card.getByRole('checkbox', { name: /^builtin\/research / }).check();
  expect((await saved).ok()).toBe(true);
  await page.reload();
  await expect(card.getByRole('combobox', { name: 'Model role', exact: true })).toHaveValue('reviewer');
  await expect(card.getByRole('textbox', { name: 'System prompt', exact: true })).toHaveValue('Review the code and report evidence.');
  await expect(card.getByRole('checkbox', { name: 'read_file', exact: true })).toBeChecked();
  await expect(card.getByRole('checkbox', { name: /^builtin\/research / })).toBeChecked();
  const snapshot = await (await request.get('/api/v1/settings/subagents')).json();
  const profile = snapshot.profiles.find((entry) => entry.profile.name === 'ui-inspector').profile;
  expect(profile.tools).toContain('read_file');
  expect(profile.delegates).toContain('builtin/research');
  page.once('dialog', (dialog) => dialog.accept());
  saved = save('DELETE');
  await card.getByTitle('Delete profile', { exact: true }).click();
  expect((await saved).ok()).toBe(true);
  await expect(card).toHaveCount(0);
});

test('queued profile edits preserve revisions, scope migration, and the original repository', async ({ page, request }) => {
  const fixture = await (await request.get('/_test/fixture')).json();
  await page.goto('/settings?section=subagents&workspace_root=' + encodeURIComponent(fixture.project_root));
  await expect(page.getByRole('heading', { name: 'Custom profiles', exact: true })).toBeVisible();
  await page.getByRole('textbox', { name: 'Profile name', exact: true }).fill('ui-queued');
  await page.getByRole('button', { name: 'Add profile', exact: true }).click();
  const card = page.locator('.subagent-profile').filter({ has: page.getByRole('heading', { name: 'ui-queued', exact: true }) });
  await expect(card).toBeVisible();
  let release;
  const gate = new Promise((resolve) => { release = resolve; });
  let notifyHeld;
  const held = new Promise((resolve) => { notifyHeld = resolve; });
  const writes = [];
  await page.route('**/api/v1/settings/subagents/profiles', async (route) => {
    if (route.request().method() !== 'PUT') return route.continue();
    writes.push(route.request().postDataJSON());
    const response = await route.fetch();
    if (writes.length === 1) { notifyHeld(); await gate; }
    await route.fulfill({ response });
  });
  try {
    await card.getByRole('textbox', { name: 'Description', exact: true }).fill('First draft');
    await card.getByRole('textbox', { name: 'Description', exact: true }).press('Tab');
    await held;
    await card.getByRole('combobox', { name: 'Model role', exact: true }).selectOption('reviewer');
    await card.getByRole('combobox', { name: 'Scope', exact: true }).selectOption('workspace');
    await card.getByRole('textbox', { name: 'Description', exact: true }).fill('Final draft');
    await card.getByRole('textbox', { name: 'Description', exact: true }).press('Tab');
    await page.getByRole('textbox', { name: 'Repository context · optional', exact: true }).fill(fixture.project_other);
    await page.getByRole('button', { name: 'Load repository', exact: true }).click();
    release();
    await expect(page.getByTitle('Reload subagent settings', { exact: true })).toBeEnabled();
    expect(writes).toHaveLength(4);
    expect(writes.map((write) => write.workspace_root)).toEqual(Array(4).fill(fixture.project_root));
    expect(writes[3].original_scope).toBe('workspace');
    expect(writes[1].revision).not.toBe(writes[0].revision);
    expect(writes[2].revision).not.toBe(writes[1].revision);
    // Moving an unchanged profile changes its scope, while its content hash stays the same.
    expect(writes[3].revision).toBe(writes[2].revision);
    const snapshot = await (await request.get('/api/v1/settings/subagents?workspace_root=' + encodeURIComponent(fixture.project_root))).json();
    const entry = snapshot.profiles.find((entry) => entry.profile.name === 'ui-queued');
    expect(entry.scope).toBe('workspace');
    expect(entry.profile.description).toBe('Final draft');
    expect(entry.profile.role).toBe('reviewer');
    await expect(card).toHaveCount(0);
  } finally { release(); }
});

test('ACP connections preserve secret mappings, enabled state, and role bindings', async ({ page, request }) => {
  await page.goto('/settings?section=subagents');
  await expect(page.getByRole('heading', { name: 'Custom profiles', exact: true })).toBeVisible();
  await page.getByRole('button', { name: /^ACP connections/ }).click();
  await page.getByRole('textbox', { name: 'Connection ID', exact: true }).fill('ui-acp');
  const save = () => page.waitForResponse((r) => new URL(r.url()).pathname === '/api/v1/settings/subagents' && r.request().method() === 'PUT');
  let saved = save();
  await page.getByRole('button', { name: 'Add connection', exact: true }).click();
  expect((await saved).ok()).toBe(true);
  const card = page.locator('.integration-editor').filter({ has: page.getByRole('heading', { name: 'ui-acp', exact: true }) });
  const env = card.getByRole('textbox', { name: 'Child environment · JSON', exact: true });
  saved = save();
  await env.fill('{"Q_STUDIO_TEST_TOKEN":"fixture-secret"}');
  await env.press('Tab');
  expect((await saved).ok()).toBe(true);
  await expect(env).toHaveValue('{"Q_STUDIO_TEST_TOKEN":"********"}');
  saved = save();
  await card.getByRole('checkbox', { name: /^Connection state/ }).uncheck();
  expect((await saved).ok()).toBe(true);
  await expect(card.getByText('Disabled', { exact: true })).toBeVisible();
  saved = save();
  await card.getByRole('checkbox', { name: /^Connection state/ }).check();
  expect((await saved).ok()).toBe(true);
  const binding = page.locator('.binding-grid select').first();
  saved = save();
  await binding.selectOption('ui-acp');
  expect((await saved).ok()).toBe(true);
  await page.reload();
  await page.getByRole('button', { name: /^ACP connections/ }).click();
  await expect(binding).toHaveValue('ui-acp');
  await expect(env).toHaveValue('{"Q_STUDIO_TEST_TOKEN":"********"}');
  await expect(card.getByRole('checkbox', { name: /^Connection state/ })).toBeChecked();
  const snapshot = await (await request.get('/api/v1/settings/subagents')).json();
  expect(snapshot.connections['ui-acp'].env.Q_STUDIO_TEST_TOKEN).toBe('********');
  page.once('dialog', (dialog) => dialog.accept());
  saved = save();
  await card.getByTitle('Delete ACP connection', { exact: true }).click();
  expect((await saved).ok()).toBe(true);
  await expect(card).toHaveCount(0);
});

test('MCP edits persist transport, environment grants, and deletion', async ({ page, request }) => {
  await page.goto('/settings?section=integrations');
  await expect(page.getByRole('heading', { name: 'MCP servers', exact: true })).toBeVisible();
  await page.getByRole('textbox', { name: 'Server ID', exact: true }).fill('ui-docs');
  const save = () => page.waitForResponse((r) => new URL(r.url()).pathname === '/api/v1/integrations/mcp' && r.request().method() === 'PUT');
  let saved = save();
  await page.getByRole('button', { name: 'Add server', exact: true }).click();
  expect((await saved).ok()).toBe(true);
  const card = page.locator('.integration-editor').filter({ has: page.getByRole('heading', { name: 'ui-docs', exact: true }) });
  saved = save();
  await card.getByRole('combobox', { name: 'Transport', exact: true }).selectOption('streamable-http');
  expect((await saved).ok()).toBe(true);
  const url = card.getByRole('textbox', { name: 'URL', exact: true });
  saved = save();
  await url.fill('https://example.test/mcp');
  await url.press('Tab');
  expect((await saved).ok()).toBe(true);
  const headers = card.getByRole('textbox', { name: 'Header to env mapping · JSON', exact: true });
  await headers.fill('{');
  await headers.press('Tab');
  await expect(page.locator('.integration-message')).toContainText('headers:');
  saved = save();
  await headers.fill('{"Authorization":"DOCS_TOKEN"}');
  await headers.press('Tab');
  expect((await saved).ok()).toBe(true);
  saved = save();
  await card.getByRole('checkbox', { name: 'reviewer', exact: true }).check();
  expect((await saved).ok()).toBe(true);
  const config = (await (await request.get('/api/v1/integrations/mcp')).json()).config;
  expect(config.servers['ui-docs']).toEqual({ transport: 'streamable-http', url: 'https://example.test/mcp', headers: { Authorization: 'DOCS_TOKEN' } });
  expect(config.roles.reviewer).toContain('ui-docs');
  await page.reload();
  await expect(url).toHaveValue('https://example.test/mcp');
  await expect(headers).toHaveValue('{"Authorization":"DOCS_TOKEN"}');
  await expect(card.getByRole('checkbox', { name: 'reviewer', exact: true })).toBeChecked();
  page.once('dialog', (dialog) => dialog.accept());
  saved = save();
  await card.getByTitle('Delete MCP server', { exact: true }).click();
  expect((await saved).ok()).toBe(true);
  await expect(card).toHaveCount(0);
  const removed = (await (await request.get('/api/v1/integrations/mcp')).json()).config;
  expect(removed.servers?.['ui-docs']).toBeUndefined();
  expect(removed.roles?.reviewer || []).not.toContain('ui-docs');
});

test('queued LSP edits keep their repository when the workspace changes', async ({ page, request }) => {
  const fixture = await (await request.get('/_test/fixture')).json();
  expect((await request.put('/api/v1/workspaces/lsp', { data: {
    workspace_root: fixture.project_root,
    global: { servers: { 'ui-gopls': { languages: ['go'], command: 'gopls', args: [] } }, languages: { go: 'ui-gopls' } },
    workspace: { version: 1, roots: [] }
  } })).ok()).toBe(true);
  await page.goto('/settings?section=integrations&panel=lsp&workspace_root=' + encodeURIComponent(fixture.project_root));
  await expect(page.getByRole('heading', { name: 'Repository roots', exact: true })).toBeVisible();
  let release;
  const gate = new Promise((resolve) => { release = resolve; });
  let notifyHeld;
  const held = new Promise((resolve) => { notifyHeld = resolve; });
  const writes = [];
  await page.route('**/api/v1/workspaces/lsp', async (route) => {
    if (route.request().method() !== 'PUT') return route.continue();
    writes.push(route.request().postDataJSON());
    const response = await route.fetch();
    if (writes.length === 1) { notifyHeld(); await gate; }
    await route.fulfill({ response });
  });
  try {
    await page.getByRole('button', { name: 'Add root', exact: true }).click();
    await held;
    const root = page.getByRole('textbox', { name: 'Root path', exact: true });
    await root.fill('src');
    await root.press('Tab');
    await page.getByRole('textbox', { name: 'Repository path', exact: true }).fill(fixture.project_other);
    await page.getByRole('button', { name: 'Load repository', exact: true }).click();
    release();
    await expect(page.getByRole('heading', { name: 'Repository roots', exact: true })).toBeVisible();
    await expect(page.getByRole('textbox', { name: 'Root path', exact: true })).toHaveCount(0);
    await expect(page.getByTitle('Reload current panel', { exact: true })).toBeEnabled();
    expect(writes).toHaveLength(2);
    expect(writes.map((write) => write.workspace_root)).toEqual([fixture.project_root, fixture.project_root]);
    const read = async (root) => (await (await request.get('/api/v1/workspaces/lsp?workspace_root=' + encodeURIComponent(root))).json()).workspace.roots || [];
    expect((await read(fixture.project_root)).map((root) => root.path)).toEqual(['src']);
    expect(await read(fixture.project_other)).toEqual([]);
  } finally { release(); }
});

test('ignore debounce preserves revisions and flushes to the owning repository on navigation', async ({ page, request }) => {
  const fixture = await (await request.get('/_test/fixture')).json();
  await page.goto('/settings?section=integrations&panel=ignore&workspace_root=' + encodeURIComponent(fixture.project_root));
  const editor = page.getByRole('textbox', { name: '.qignore content', exact: true });
  await expect(editor).toBeVisible();
  let release;
  const gate = new Promise((resolve) => { release = resolve; });
  let notifyHeld;
  const held = new Promise((resolve) => { notifyHeld = resolve; });
  const writes = [];
  await page.route('**/api/v1/workspaces/ignore', async (route) => {
    if (route.request().method() !== 'PUT') return route.continue();
    writes.push(route.request().postDataJSON());
    const response = await route.fetch();
    if (writes.length === 1) { notifyHeld(); await gate; }
    await route.fulfill({ response });
  });
  try {
    await editor.fill('build/\n');
    await held;
    await editor.fill('build/\nvendor/\n');
    await page.getByRole('textbox', { name: 'Repository path', exact: true }).fill(fixture.project_other);
    await page.getByRole('button', { name: 'Load repository', exact: true }).click();
    release();
    await expect(editor).toBeVisible();
    await expect(editor).toHaveValue('');
    await expect(page.getByTitle('Reload current panel', { exact: true })).toBeEnabled();
    expect(writes.map((write) => write.workspace_root)).toEqual([fixture.project_root, fixture.project_root]);
    expect(writes[1].revision).not.toBe(writes[0].revision);
    const read = async (root) => (await (await request.get('/api/v1/workspaces/ignore?workspace_root=' + encodeURIComponent(root))).json()).content;
    expect(await read(fixture.project_root)).toBe('build/\nvendor/\n');
    expect(await read(fixture.project_other)).toBe('');
    const saved = page.waitForResponse((r) => new URL(r.url()).pathname === '/api/v1/workspaces/ignore' && r.request().method() === 'PUT');
    await editor.fill('cache/\n');
    await page.getByRole('complementary', { name: 'Studio navigation' }).getByRole('button', { name: 'Help', exact: true }).click();
    expect((await saved).ok()).toBe(true);
    await expect(page.getByRole('heading', { name: 'Help', exact: true })).toBeVisible();
    expect(await read(fixture.project_other)).toBe('cache/\n');
    expect(writes[2].workspace_root).toBe(fixture.project_other);
  } finally { release(); }
});

test('skills show global entries without a repository and switch scopes when one is loaded or cleared', async ({ page, request }) => {
  const fixture = await (await request.get('/_test/fixture')).json();
  await page.evaluate(() => localStorage.removeItem('q-studio-workspace-root'));
  await page.goto('/settings?section=integrations&panel=skills');
  const globalSkill = page.getByRole('heading', { name: 'studio-global-review', exact: true });
  const repositorySkills = page.getByRole('heading', { name: 'Repository skills', exact: true });
  const repositorySkill = page.getByRole('heading', { name: 'studio-review', exact: true });
  const reindex = page.getByRole('button', { name: 'Reindex all', exact: true });
  await expect(page.getByRole('heading', { name: 'Global skills', exact: true })).toBeVisible();
  await expect(globalSkill).toBeVisible();
  await expect(repositorySkills).toHaveCount(0);
  await expect(repositorySkill).toHaveCount(0);
  await expect(reindex).toBeDisabled();
  await expect(page.getByRole('button', { name: 'Clone and index', exact: true })).toHaveCount(0);
  await page.getByRole('textbox', { name: 'Repository path', exact: true }).fill(fixture.root);
  await page.getByRole('button', { name: 'Load repository', exact: true }).click();
  await expect(globalSkill).toBeVisible();
  await expect(repositorySkills).toBeVisible();
  await expect(repositorySkill).toBeVisible();
  await expect(reindex).toBeEnabled();
  await page.getByRole('textbox', { name: 'Repository path', exact: true }).fill('');
  await page.getByRole('button', { name: 'Load repository', exact: true }).click();
  await expect(globalSkill).toBeVisible();
  await expect(repositorySkills).toHaveCount(0);
  await expect(repositorySkill).toHaveCount(0);
  await expect(reindex).toBeDisabled();
  await page.reload();
  await expect(globalSkill).toBeVisible();
  await expect(page.getByRole('textbox', { name: 'Repository path', exact: true })).toHaveValue('');
  await expect(repositorySkills).toHaveCount(0);
  await expect(page.locator('.integration-message')).toHaveCount(0);
});

test('portable skills render read-only and reindex through the real local services', async ({ page, request }) => {
  const fixture = await (await request.get('/_test/fixture')).json();
  await page.goto('/settings?section=integrations&panel=skills&workspace_root=' + encodeURIComponent(fixture.root));
  const card = page.locator('.skill-card').filter({ has: page.getByRole('heading', { name: 'studio-review', exact: true }) });
  await expect(card).toContainText('Review the requested Go package.');
  await expect(card).toContainText('Read only');
  await expect(card.getByRole('button')).toHaveCount(0);
  const saved = page.waitForResponse((r) => new URL(r.url()).pathname === '/api/v1/workspaces/skills/reindex');
  await page.getByRole('button', { name: 'Reindex all', exact: true }).click();
  expect((await saved).ok()).toBe(true);
  await expect(page.locator('.integration-message')).toContainText('Skill indexes rebuilt');
  await expect(card).toBeVisible();
});

test('tool results align with tool calls across wide and narrow viewports', async ({ page, request }, testInfo) => {
  const fixture = await (await request.get('/_test/fixture')).json();
  const registered = await request.post('/api/v1/registered-sessions', { data: { workspace_root: fixture.root, create: true } });
  expect(registered.status()).toBe(201);
  const session = await registered.json();
  await page.goto(`/sessions/${session.registration_id}`);
  const composer = page.locator('.composer textarea');
  await expect(composer).toBeEnabled();
  await composer.fill('write the requested file');
  await composer.press('Enter');
  await expect(page.locator('.transcript').getByRole('heading', { name: 'Studio result' })).toBeVisible();
  await expect(composer).toHaveAttribute('placeholder', 'Ask Q to work in this repository…');

  const transcript = page.locator('.transcript');
  const results = transcript.locator('.transcript-tool-result');
  expect(await results.count()).toBeGreaterThan(0);
  for (const width of [2560, 3440, 1440, 900]) {
    await page.setViewportSize({ width, height: 1000 });
    for (const expanded of [false, true]) {
      // Compare every persisted result with the calls' content column, including
      // both edges: wide screens must keep the same centered transcript measure.
      for (const result of await results.all()) {
        const isExpanded = (await result.getAttribute('open')) !== null;
        if (isExpanded !== expanded) {
          await result.locator('summary').click();
        }
      }
      const geometry = await transcript.evaluate((element) => {
        const call = element.querySelector('.tool-card').getBoundingClientRect();
        return [...element.querySelectorAll('.transcript-tool-result')].map((result) => {
          const bounds = result.getBoundingClientRect();
          return { left: Math.abs(bounds.left - call.left), right: Math.abs(bounds.right - call.right) };
        });
      });
      for (const delta of geometry) {
        expect(delta.left, `left edge at ${width}px, expanded=${expanded}`).toBeLessThanOrEqual(1);
        expect(delta.right, `right edge at ${width}px, expanded=${expanded}`).toBeLessThanOrEqual(1);
      }
      expect(await transcript.evaluate((element) => element.scrollWidth <= element.clientWidth)).toBe(true);
    }
    if (width === 3440) {
      await results.first().scrollIntoViewIfNeeded();
      await page.screenshot({ path: testInfo.outputPath('wide-tool-results.png') });
    }
  }
});

test('model settings scroll inside the content pane at desktop and narrow widths', async ({ page }) => {
  for (const width of [1440, 900]) {
    await page.setViewportSize({ width, height: 650 });
    await page.goto('/settings?section=models');
    const pane = page.locator('.settings-content');
    await expect(pane.getByRole('heading', { name: 'Model assignments' })).toBeVisible();
    await expect.poll(() => pane.evaluate((element) => element.scrollHeight > element.clientHeight)).toBe(true);
    await pane.hover();
    await page.mouse.wheel(0, 700);
    await expect.poll(() => pane.evaluate((element) => element.scrollTop)).toBeGreaterThan(0);
  }
});


test('changes reject a late file diff after repository selection', async ({ page, request }) => {
  const fixture = await (await request.get('/_test/fixture')).json();
  await page.goto('/changes?workspace_root=' + encodeURIComponent(fixture.git_root));
  await expect(page.locator('.diff-view')).toContainText('+var answer  = 42');
  let release;
  const gate = new Promise((resolve) => { release = resolve; });
  let notify;
  const held = new Promise((resolve) => { notify = resolve; });
  let aborted = false;
  page.on('requestfailed', (r) => { if (r.url().includes('/changes/file')) aborted = true; });
  await page.route('**/api/v1/workspaces/changes/file?**', async (route) => {
    const url = new URL(route.request().url());
    if (url.searchParams.get('workspace_root') !== fixture.git_root || url.searchParams.get('path') !== 'secondary.go') return route.continue();
    const response = await route.fetch();
    notify();
    await gate;
    await route.fulfill({ response }).catch(() => {});
  });
  await page.locator('.changed-files button').filter({ hasText: 'secondary.go' }).click();
  await held;
  await page.getByRole('textbox', { name: 'Repository', exact: true }).fill(fixture.git_other);
  await page.getByTitle('Load changes', { exact: true }).click();
  await expect(page.locator('.diff-view')).toContainText('+var answer  = 84');
  await expect.poll(() => aborted).toBe(true);
  release();
  await expect(page.locator('.change-detail h2')).toHaveText('main.go');
  await expect(page.locator('.diff-view')).not.toContainText('count');
  await page.getByRole('button', { name: /Link to diff line 1 in/ }).first().click();
  await expect(page.locator('.diff-line.selected')).toHaveCount(1);
  await page.locator('.changed-files button').filter({ hasText: 'secondary.go' }).click();
  await expect(page.locator('.diff-view')).toContainText('+var count  = 1');
  await expect(page.locator('.diff-line.selected')).toHaveCount(0);
});

test('commit review queues message edits and executes only its repository', async ({ page, request }) => {
  const fixture = await (await request.get('/_test/fixture')).json();
  await page.goto('/changes?workspace_root=' + encodeURIComponent(fixture.git_root));
  await expect(page.locator('.diff-view')).toContainText('+var answer  = 42');
  await page.getByRole('button', { name: 'Prepare commit', exact: true }).click();
  const proposal = page.locator('.proposal-list textarea').first();
  await expect(proposal).toBeVisible();
  let release;
  const gate = new Promise((resolve) => { release = resolve; });
  let notify;
  const held = new Promise((resolve) => { notify = resolve; });
  const writes = [];
  await page.route('**/api/v1/workspaces/commits/*/proposals/*', async (route) => {
    writes.push(route.request().postDataJSON());
    const response = await route.fetch();
    if (writes.length === 1) { notify(); await gate; }
    await route.fulfill({ response });
  });
  await proposal.fill('style: first message');
  await proposal.press('Tab');
  await held;
  await proposal.fill('style: final fixture message');
  await proposal.press('Tab');
  await page.getByRole('textbox', { name: 'Repository', exact: true }).fill(fixture.git_other);
  await expect(page.getByTitle('Load changes', { exact: true })).toBeDisabled();
  release();
  await expect(page.getByRole('button', { name: 'Commit', exact: true })).toBeEnabled();
  await expect(proposal).toHaveValue('style: final fixture message');
  expect(writes.map((item) => item.message)).toEqual(['style: first message', 'style: final fixture message']);
  page.once('dialog', (dialog) => dialog.accept());
  await page.getByRole('button', { name: 'Commit', exact: true }).click();
  await expect(page.locator('.commit-result')).toContainText('Committed');
  await expect(page.locator('.commit-result')).toContainText('style: final fixture message');
  const original = await (await request.get('/api/v1/workspaces/changes?workspace_root=' + encodeURIComponent(fixture.git_root))).json();
  const other = await (await request.get('/api/v1/workspaces/changes?workspace_root=' + encodeURIComponent(fixture.git_other))).json();
  expect(original.snapshot.files || []).toHaveLength(0);
  expect(other.snapshot.files).toHaveLength(2);
  await expect(page.getByRole('textbox', { name: 'Repository', exact: true })).toHaveValue(fixture.git_root);
});


test('initial folder lookup preserves a directory typed while loading', async ({ page, request }) => {
  const fixture = await (await request.get('/_test/fixture')).json();
  let release;
  const gate = new Promise((resolve) => { release = resolve; });
  let notify;
  const held = new Promise((resolve) => { notify = resolve; });
  await page.route('**/api/v1/directories', async (route) => {
    const response = await route.fetch();
    notify();
    await gate;
    await route.fulfill({ response });
  });
  await page.getByTitle('Add or create session').click();
  await page.getByRole('button', { name: 'Choose repository folder' }).click();
  const browser = page.getByRole('dialog', { name: 'Choose a folder' });
  await held;
  const path = browser.getByRole('textbox', { name: 'Directory path' });
  await path.fill(fixture.project_other);
  release();
  await expect(browser.getByRole('button', { name: 'Go', exact: true })).toBeEnabled();
  await expect(path).toHaveValue(fixture.project_other);
  await browser.getByRole('button', { name: 'Go', exact: true }).click();
  await expect(browser.locator('footer code')).toHaveText(fixture.project_other);
});


test('operations keep the selected period and stop polling on navigation', async ({ page }) => {
  await page.clock.install();
  let release;
  const gate = new Promise((resolve) => { release = resolve; });
  let notify;
  const held = new Promise((resolve) => { notify = resolve; });
  let reads = 0;
  let aborted = false;
  page.on('requestfailed', (r) => { if (r.url().includes('/operations?days=30')) aborted = true; });
  await page.route('**/api/v1/operations?**', async (route) => {
    reads++;
    const response = await route.fetch();
    if (new URL(route.request().url()).searchParams.get('days') === '30') {
      notify(); await gate;
    }
    await route.fulfill({ response }).catch(() => {});
  });
  await page.goto('/operations');
  await held;
  await page.getByRole('combobox', { name: 'Usage period', exact: true }).selectOption('7');
  await expect(page.locator('.usage-card')).toContainText('7 day window');
  await expect(page.getByRole('heading', { name: 'Services', exact: true })).toBeVisible();
  await expect(page.getByRole('heading', { name: 'Retention', exact: true })).toBeVisible();
  await expect(page.getByRole('heading', { name: 'Runtime logs', exact: true })).toBeVisible();
  await expect.poll(() => aborted).toBe(true);
  release();
  await expect(page.locator('.usage-card')).not.toContainText('30 day window');
  const before = reads;
  await page.clock.fastForward('00:00:16');
  await expect.poll(() => reads).toBeGreaterThan(before);
  await expect(page.getByTitle('Refresh operations', { exact: true })).toBeEnabled();
  await page.getByRole('complementary', { name: 'Studio navigation' }).getByRole('button', { name: 'Help', exact: true }).click();
  const stopped = reads;
  await page.clock.fastForward('00:00:31');
  expect(reads).toBe(stopped);
});


test('global model editors preserve queued drafts across settings panels', async ({ page, request }) => {
  await page.goto('/settings?section=models');
  await expect(page.getByTitle('Refresh Gateway models', { exact: true })).toBeEnabled();
  await page.getByRole('textbox', { name: 'Role name', exact: true }).fill('ui-model-role');
  await page.getByRole('button', { name: 'Add role', exact: true }).click();
  const role = page.locator('.assignment-row').filter({ has: page.getByText('Ui Model Role', { exact: true }) });
  await expect(role).toBeVisible();
  const model = page.locator('.primary-assignment').first().getByRole('combobox', { name: 'Model', exact: true });
  const selectedModel = await model.inputValue();
  await role.getByRole('combobox', { name: 'Model', exact: true }).selectOption(selectedModel);
  await expect(page.locator('.save-state')).toHaveText('Saved');
  await page.getByRole('textbox', { name: 'New group name', exact: true }).fill('ui-fallback');
  await page.getByRole('button', { name: 'Add group', exact: true }).click();
  const group = page.locator('.model-group-card').filter({ has: page.getByRole('heading', { name: 'ui-fallback', exact: true }) });
  const timeout = group.getByRole('textbox', { name: 'Timeout', exact: true });
  await expect(timeout).toBeVisible();
  let release;
  const gate = new Promise((resolve) => { release = resolve; });
  let notify;
  const held = new Promise((resolve) => { notify = resolve; });
  const writes = [];
  await page.route('**/api/v1/settings/model-groups', async (route) => {
    if (route.request().method() !== 'PUT') return route.continue();
    writes.push(route.request().postDataJSON());
    const response = await route.fetch();
    if (writes.length === 1) { notify(); await gate; }
    await route.fulfill({ response });
  });
  await timeout.fill('45s'); await timeout.press('Tab'); await held;
  await timeout.fill('90s'); await timeout.press('Tab');
  await page.getByRole('complementary', { name: 'Settings sections' }).getByRole('button', { name: /^Runtime/ }).click();
  const parallel = page.getByRole('spinbutton', { name: /Maximum parallel agents/ });
  await parallel.fill('5'); await parallel.press('Tab');
  release();
  await expect(page.locator('.save-state')).toHaveText('Saved');
  await expect(parallel).toHaveValue('5');
  const saved = await (await request.get('/api/v1/settings')).json();
  expect(saved.runtime.max_parallel).toBe(5);
  expect(saved.models.groups.find((item) => item.name === 'ui-fallback').candidates[0].timeout).toBe('1m30s');
  expect(writes.map((item) => item.candidates[0].timeout)).toEqual(['45s', '90s']);
  await page.getByRole('complementary', { name: 'Settings sections' }).getByRole('button', { name: /^Models/ }).click();
  await expect(timeout).toHaveValue('1m30s');
  await expect(role.getByRole('combobox', { name: 'Model', exact: true })).toHaveValue(selectedModel);
  page.once('dialog', (dialog) => dialog.accept());
  await group.getByTitle('Delete model group', { exact: true }).click();
  await expect(group).toHaveCount(0);
  page.once('dialog', (dialog) => dialog.accept());
  await role.getByTitle('Delete custom role', { exact: true }).click();
  await expect(role).toHaveCount(0);
});

test('gateway provider edits follow an acknowledged rename in the save queue', async ({ page, request }) => {
  expect((await request.post('/api/v1/settings/gateway/providers', { data: {
    id: 'ui-gateway', type: 'openai-compatible', enabled: false, base_url: 'http://127.0.0.1:1/v1', api_key_env: ''
  } })).ok()).toBe(true);
  await page.goto('/settings?section=providers');
  const card = page.locator('.provider-card').filter({ hasText: 'ui-gateway' });
  await expect(card).toBeVisible();
  let release;
  const gate = new Promise((resolve) => { release = resolve; });
  let notify;
  const held = new Promise((resolve) => { notify = resolve; });
  const writes = [];
  await page.route('**/api/v1/settings/gateway/providers/*', async (route) => {
    if (route.request().method() !== 'PUT') return route.continue();
    writes.push({ url: route.request().url(), body: route.request().postDataJSON() });
    const response = await route.fetch();
    if (writes.length === 1) { notify(); await gate; }
    await route.fulfill({ response });
  });
  const id = card.getByRole('textbox', { name: 'Provider ID', exact: true });
  await id.fill('ui-gateway-renamed'); await id.press('Tab'); await held;
  await card.getByRole('textbox', { name: 'Model prefix', exact: true }).fill('studio');
  await card.getByRole('textbox', { name: 'Model prefix', exact: true }).press('Tab');
  release();
  await expect(page.locator('.save-state')).toHaveText('Saved');
  expect(new URL(writes[1].url).pathname).toBe('/api/v1/settings/gateway/providers/ui-gateway-renamed');
  expect(writes[1].body.prefix).toBe('studio');
  await page.reload();
  await expect(id).toHaveValue('ui-gateway-renamed');
  await expect(card.getByRole('textbox', { name: 'Model prefix', exact: true })).toHaveValue('studio');
  page.once('dialog', (dialog) => dialog.accept());
  await card.getByTitle('Delete provider', { exact: true }).click();
  await expect(card).toHaveCount(0);
});

test('system one routing and listener key management persist through native settings panels', async ({ page, request }) => {
  const fixture = await (await request.get('/_test/fixture')).json();
  expect((await request.post('/api/v1/settings/system-one/providers', { data: {
    id: 'ui-decision', uri: fixture.decision_uri, api_key_env: ''
  } })).ok()).toBe(true);
  await page.goto('/settings?section=system-one');
  await expect(page.getByTitle('Refresh System One models', { exact: true })).toBeEnabled();
  const role = page.locator('.system-one-assignment').filter({ hasText: 'Agent Skill Decision' });
  await expect(role.getByRole('combobox', { name: 'Model', exact: true }).locator('option[value="ui-decision/decision-test"]')).toHaveCount(1);
  await role.getByRole('combobox', { name: 'Model', exact: true }).selectOption('ui-decision/decision-test');
  await expect(page.locator('.save-state')).toHaveText('Saved');
  const card = page.locator('.system-one-provider-card').filter({ hasText: 'ui-decision' });
  let release;
  const gate = new Promise((resolve) => { release = resolve; });
  let notify;
  const held = new Promise((resolve) => { notify = resolve; });
  const writes = [];
  await page.route('**/api/v1/settings/system-one/providers/*', async (route) => {
    if (route.request().method() !== 'PUT') return route.continue();
    writes.push(route.request().url());
    const response = await route.fetch();
    if (writes.length === 1) { notify(); await gate; }
    await route.fulfill({ response });
  });
  const id = card.getByRole('textbox', { name: 'Provider ID', exact: true });
  await id.fill('ui-decision-renamed'); await id.press('Tab'); await held;
  await card.getByRole('textbox', { name: 'API key environment', exact: true }).fill('Q_STUDIO_DECISION_KEY');
  await card.getByRole('textbox', { name: 'API key environment', exact: true }).press('Tab');
  release();
  await expect(page.locator('.save-state')).toHaveText('Saved');
  expect(new URL(writes[1]).pathname).toBe('/api/v1/settings/system-one/providers/ui-decision-renamed');
  await expect(role.getByRole('combobox', { name: 'Model', exact: true })).toHaveValue('ui-decision-renamed/decision-test');
  const alias = page.getByRole('textbox', { name: 'New key alias', exact: true });
  await alias.fill('ui-decision-key');
  await page.getByRole('button', { name: 'Generate key', exact: true }).click();
  await expect(page.locator('.generated-key:visible code')).not.toBeEmpty();
  await page.getByRole('button', { name: 'Dismiss', exact: true }).click();
  const keyRow = page.locator('.api-key-row').filter({ hasText: 'ui-decision-key' });
  page.once('dialog', (dialog) => dialog.accept());
  await keyRow.getByRole('button', { name: 'Revoke', exact: true }).click();
  await expect(keyRow).toContainText('Revoked');
  await role.getByRole('combobox', { name: 'Model', exact: true }).selectOption('');
  await expect(page.locator('.save-state')).toHaveText('Saved');
  page.once('dialog', (dialog) => dialog.accept());
  await card.getByTitle('Delete provider', { exact: true }).click();
  await expect(card).toHaveCount(0);

  await page.getByRole('complementary', { name: 'Settings sections' }).getByRole('button', { name: /^Services/ }).click();
  const library = page.locator('.service-card').filter({ has: page.getByRole('heading', { name: 'Library', exact: true }) });
  await library.getByRole('textbox', { name: 'Host', exact: true }).fill('127.0.0.2');
  await library.getByRole('textbox', { name: 'Host', exact: true }).press('Tab');
  await expect(page.locator('.save-state')).toHaveText('Saved');
  const gateway = page.locator('.service-card').filter({ has: page.getByRole('heading', { name: 'Gateway', exact: true }) });
  await gateway.getByRole('textbox', { name: 'New key alias', exact: true }).fill('ui-gateway-key');
  await gateway.getByRole('button', { name: 'Generate', exact: true }).click();
  await expect(gateway.locator('.generated-key code')).not.toBeEmpty();
  await gateway.getByRole('button', { name: 'Dismiss', exact: true }).click();
  const gatewayKey = gateway.locator('.api-key-row').filter({ hasText: 'ui-gateway-key' });
  page.once('dialog', (dialog) => dialog.accept());
  await gatewayKey.getByRole('button', { name: 'Revoke', exact: true }).click();
  await expect(gatewayKey).toContainText('Revoked');
  await page.reload();
  await expect(library.getByRole('textbox', { name: 'Host', exact: true })).toHaveValue('127.0.0.2');
  await expect(gatewayKey).toContainText('Revoked');
});

test('file browser switches raw and diff and handles unchanged, deleted, binary and large files', async ({ page, request }) => {
  const fixture = await (await request.get('/_test/fixture')).json();
  await page.goto('/files?' + new URLSearchParams({ workspace_root: fixture.files_root }));
  await expect(page).toHaveTitle(/Q Studio/);
  await expect(page.getByRole('heading', { name: 'Files', exact: true })).toBeVisible();
  await page.getByRole('button', { name: 'File main.go', exact: true }).click();
  const viewer = page.getByRole('region', { name: 'File viewer' });
  await expect(viewer.locator('.language-go .hljs-keyword').first()).toBeVisible();
  await expect(viewer.locator('.file-code-grid code')).toContainText('answer  = 128');
  await expect(viewer.getByRole('button', { name: 'File line 3', exact: true })).toBeVisible();
  await viewer.getByRole('button', { name: 'File line 3', exact: true }).click();
  expect(new URL(page.url()).searchParams.get('line')).toBe('3');
  await page.context().grantPermissions(['clipboard-read', 'clipboard-write']);
  await viewer.getByRole('button', { name: 'Copy file content', exact: true }).click();
  await expect(viewer.getByRole('button', { name: 'Copy file content', exact: true })).toContainText('Copied');
  expect(await page.evaluate(() => navigator.clipboard.readText())).toContain('answer  = 128');
  await viewer.getByRole('button', { name: 'Diff', exact: true }).click();
  await expect(viewer).toContainText('HEAD → working tree');
  await expect(viewer.locator('.inline-file-diff-row.added code')).toContainText('var answer  = 128');
  await page.screenshot({ path: join(tmpdir(), 'q-studio-files-diff.png') });
  await page.reload();
  await expect(viewer.getByRole('button', { name: 'Diff', exact: true })).toHaveAttribute('aria-pressed', 'true');
  await expect(viewer.locator('.inline-file-diff-row.added code')).toContainText('var answer  = 128');
  await viewer.getByRole('button', { name: 'Raw', exact: true }).click();
  await expect(viewer.getByRole('button', { name: 'File line 3', exact: true })).toHaveClass(/selected/);
  await viewer.getByRole('button', { name: 'Diff', exact: true }).click();
  await page.getByRole('button', { name: 'File clean.txt', exact: true }).click();
  await expect(viewer).toContainText('No Git changes for this file.');
  await page.getByRole('button', { name: 'Changed file deleted.txt', exact: true }).click();
  await expect(viewer.locator('.inline-file-diff-row.removed code')).toContainText('deleted content');
  await viewer.getByRole('button', { name: 'Raw', exact: true }).click();
  await expect(viewer).toContainText('File is absent from the working directory.');
  await page.getByRole('button', { name: 'File binary.bin', exact: true }).click();
  await expect(viewer).toContainText('Binary or non-UTF-8 file');
  await page.getByRole('button', { name: 'File large.txt', exact: true }).click();
  await expect(viewer).toContainText('Partial preview');
  expect(await viewer.locator('.file-line-numbers button').count()).toBeLessThanOrEqual(4001);
  await page.getByRole('button', { name: 'Directory nested', exact: true }).click();
  await page.getByRole('button', { name: 'File nested/config.json', exact: true }).click();
  await expect(viewer.locator('.language-json .hljs-attr')).toContainText('"enabled"');
  for (const width of [1440, 390]) {
    await page.setViewportSize({ width, height: 900 });
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
    await viewer.scrollIntoViewIfNeeded();
    await page.screenshot({ path: join(tmpdir(), `q-studio-files-raw-${width}.png`) });
  }
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.getByRole('textbox', { name: 'File workspace directory', exact: true }).fill(fixture.other);
  await page.locator('.files-root-input').getByRole('button', { name: 'Open', exact: true }).click();
  await expect(viewer.locator('header small')).toHaveText(fixture.other);
  await page.getByRole('textbox', { name: 'File path', exact: true }).fill('missing.txt');
  await page.getByRole('button', { name: 'Open file', exact: true }).click();
  await viewer.getByRole('button', { name: 'Diff', exact: true }).click();
  await expect(viewer).toContainText('Git diff is unavailable for this directory.');
});

test('file diff keeps the entire file, correct line numbers, gutter markers and multiline syntax', async ({ page, request }) => {
  const fixture = await (await request.get('/_test/fixture')).json();
  await page.goto('/files?' + new URLSearchParams({ workspace_root: fixture.files_root, path: 'full.go', mode: 'diff' }));
  const viewer = page.getByRole('region', { name: 'File viewer' });
  const rows = viewer.locator('.inline-file-diff-row');
  await expect(rows).toHaveCount(124);
  await expect(rows.first().locator('code')).toHaveText('package main');
  await expect(rows.last().locator('code')).toHaveText('var tail = 7');
  await expect(rows.last()).toHaveAttribute('data-new-line', '122');
  await expect(rows.last()).toHaveAttribute('data-old-line', '122');
  const added = viewer.locator('.inline-file-diff-row.added').filter({ hasText: 'added comment line' });
  await expect(added.locator('.inline-diff-marker')).toHaveText('+');
  await expect(added).toHaveAttribute('data-new-line', '86');
  await expect(added.locator('code .hljs-comment')).toHaveText('added comment line');
  const removed = viewer.locator('.inline-file-diff-row.removed').filter({ hasText: 'comment line 56' });
  await expect(removed.locator('.inline-diff-marker')).toHaveText('−');
  await expect(removed).toHaveAttribute('data-old-line', '56');
  await expect(removed).not.toHaveAttribute('data-new-line');
  await expect(viewer.locator('.inline-file-diff-grid')).not.toContainText('@@');
  await expect(viewer.locator('.inline-file-diff-grid')).not.toContainText('diff --git');
  await added.getByRole('button', { name: 'Diff line 86', exact: true }).click();
  expect(new URL(page.url()).searchParams.get('line')).toBe('86');
  await page.screenshot({ path: join(tmpdir(), 'q-studio-files-inline-diff.png') });
  await viewer.getByRole('combobox', { name: 'Diff comparison', exact: true }).selectOption('staged');
  await expect(viewer).toContainText('HEAD → index');
  await expect(rows).toHaveCount(122);
  await expect(viewer.locator('.inline-file-diff-row.added')).toHaveCount(0);
  await expect(rows.last().locator('code')).toHaveText('var tail = 7');
  await viewer.getByRole('combobox', { name: 'Diff comparison', exact: true }).selectOption('unstaged');
  await expect(rows).toHaveCount(124);
  await expect(added).toBeVisible();
  await viewer.getByRole('button', { name: 'Raw', exact: true }).click();
  await expect(viewer.locator('.file-code-grid code')).toContainText('var tail = 7');
  await expect(viewer.locator('.inline-file-diff-row')).toHaveCount(0);
});

test('file browser ignores a late raw read when the file and mode change', async ({ page, request }) => {
  const fixture = await (await request.get('/_test/fixture')).json();
  let held, release;
  const captured = new Promise((resolve) => held = resolve);
  const gate = new Promise((resolve) => release = resolve);
  await page.route('**/api/v1/workspaces/files/content?*', async (route) => {
    if (new URL(route.request().url()).searchParams.get('path') !== 'main.go') return route.continue();
    const response = await route.fetch();
    held(); await gate;
    await route.fulfill({ response }).catch(() => {});
  });
  await page.goto('/files?' + new URLSearchParams({ workspace_root: fixture.files_root }));
  await page.getByRole('button', { name: 'File main.go', exact: true }).click();
  await captured;
  await page.getByRole('button', { name: 'File secondary.go', exact: true }).click();
  const viewer = page.getByRole('region', { name: 'File viewer' });
  await viewer.getByRole('button', { name: 'Diff', exact: true }).click();
  await expect(viewer.locator('.inline-file-diff-row.added code')).toContainText('var count  = 1');
  release();
  await expect(viewer.locator(':scope > header strong')).toHaveText('secondary.go');
  await expect(viewer.locator('.inline-file-diff-grid')).not.toContainText('answer');
  await viewer.getByRole('button', { name: 'Raw', exact: true }).click();
  await expect(viewer.locator('.file-code-grid code')).toContainText('count  = 1');
});

test('session file links open the selected delegation worktree and project workspaces', async ({ page, request }) => {
  const fixture = await (await request.get('/_test/fixture')).json();
  const project = await request.post('/api/v1/projects', { data: { name: 'Viewer project', workspace_roots: [fixture.files_root, fixture.project_other] } });
  expect(project.ok()).toBe(true);
  const registered = await request.post('/api/v1/registered-sessions', { data: { workspace_root: fixture.files_root, session_id: fixture.files_session_id } });
  expect(registered.status()).toBe(201);
  const session = await registered.json();
  await page.goto('/sessions/' + session.registration_id);
  await page.getByRole('link', { name: 'Inspect main', exact: true }).click();
  const panel = page.getByRole('complementary', { name: 'Session file browser' });
  const viewer = panel.getByRole('region', { name: 'File viewer' });
  await expect(viewer.locator('.file-code-grid code')).toContainText('answer  = 128');
  await expect(viewer.getByRole('button', { name: 'File line 3', exact: true })).toHaveClass(/selected/);
  await expect(panel.getByRole('combobox', { name: 'File workspace', exact: true }).locator('option').filter({ hasText: fixture.project_other })).toHaveCount(1);
  await page.getByRole('complementary', { name: 'Registered session tree' }).getByRole('button', { name: /^junior-developer completed.*CR working$/ }).click();
  await page.getByRole('link', { name: 'Inspect child main', exact: true }).click();
  await expect(viewer.locator('header small')).toHaveText(fixture.files_worktree);
  await expect(viewer.locator('.file-code-grid code')).toContainText('answer = 999');
  await viewer.getByRole('button', { name: 'Diff', exact: true }).click();
  await expect(viewer.locator('.inline-file-diff-row.added code')).toContainText('var answer = 999');
  await expect(page.locator('.transcript')).toContainText('Files child');
  await page.screenshot({ path: join(tmpdir(), 'q-studio-session-files.png') });
  await panel.getByRole('combobox', { name: 'File workspace', exact: true }).selectOption(fixture.project_other);
  await expect(viewer.locator('header small')).toHaveText(fixture.project_other);
  await page.getByRole('link', { name: 'Inspect child main', exact: true }).click();
  await expect(viewer.locator('header small')).toHaveText(fixture.files_worktree);
  await expect(viewer.locator('.inline-file-diff-row.added code')).toContainText('var answer = 999');
  for (const width of [900, 390]) {
    await page.setViewportSize({ width, height: 900 });
    await expect(panel).toBeVisible();
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
    await page.screenshot({ path: join(tmpdir(), `q-studio-session-files-${width}.png`) });
  }
  await panel.getByRole('button', { name: 'Close file browser', exact: true }).click();
  await expect(panel).toHaveCount(0);
  await page.setViewportSize({ width: 1440, height: 900 });
  await expect(page.getByRole('complementary', { name: 'Current turn activity' })).toBeVisible();
  await request.delete('/api/v1/projects/' + (await project.json()).id);
});
