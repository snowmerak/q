import { test, expect } from '@playwright/test';

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
