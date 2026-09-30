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
