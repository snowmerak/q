import { test, expect } from '@playwright/test';

test('session type tabs keep separate selections and fit desktop and mobile', async ({ page, request }, testInfo) => {
  const errors = [];
  page.on('pageerror', error => errors.push(error.message));
  page.on('console', message => {
    if (message.type() === 'error' && !message.text().startsWith('Failed to load resource:')) errors.push(message.text());
  });
  const fixture = await (await request.get('/_test/fixture')).json();
  await page.goto('/sessions');
  await expect(page).toHaveTitle(/Q Studio/);
  await page.getByRole('button', { name: 'Add or create session', exact: true }).click();
  const dialog = page.getByRole('dialog', { name: 'New session', exact: true });
  const chat = dialog.getByRole('tab', { name: 'Temporary', exact: true });
  const workspace = dialog.getByRole('tab', { name: 'Workspace', exact: true });
  const project = dialog.getByRole('tab', { name: 'Project', exact: true });
  await expect(chat).toHaveAttribute('aria-selected', 'true');
  await expect(dialog.getByRole('textbox')).toHaveCount(0);
  await expect(dialog.getByRole('combobox')).toHaveCount(0);
  await workspace.click();
  await dialog.getByLabel('Workspace directory').fill(fixture.root);
  await dialog.getByRole('button', { name: 'Load sessions', exact: true }).click();
  await expect(dialog.getByRole('button', { name: /Create new session/ })).toBeVisible();
  await project.click();
  await expect(dialog.getByLabel('Workspace directory')).toHaveCount(0);
  await expect(dialog.getByRole('button', { name: /Create new session/ })).toHaveCount(0);
  await dialog.getByRole('combobox', { name: 'Project', exact: true }).selectOption({ label: 'Integration' });
  await dialog.getByRole('combobox', { name: 'Workspace', exact: true }).selectOption(fixture.other);
  await dialog.getByRole('button', { name: 'Load sessions', exact: true }).click();
  await expect(dialog.getByRole('button', { name: /Create new session/ })).toBeVisible();
  await workspace.click();
  await expect(dialog.getByLabel('Workspace directory')).toHaveValue(fixture.root);
  await expect(dialog.getByRole('button', { name: /Create new session/ })).toBeVisible();
  await workspace.press('ArrowRight');
  await expect(project).toBeFocused();
  await expect(project).toHaveAttribute('aria-selected', 'true');
  await expect(dialog.getByRole('combobox', { name: 'Workspace', exact: true })).toHaveValue(fixture.other);
  await project.press('Home');
  await expect(chat).toBeFocused();
  for (const width of [1440, 390]) {
    await page.setViewportSize({ width, height: 900 });
    for (const tab of [chat, workspace, project]) {
      await tab.click();
      await expect(tab).toHaveAttribute('aria-selected', 'true');
      await expect(dialog.getByRole('tabpanel')).toBeVisible();
      await expect(dialog.getByRole('button', { name: 'Cancel', exact: true })).toBeVisible();
      expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
      const bounds = await dialog.boundingBox();
      expect(bounds.height).toBeLessThan(650);
      await dialog.screenshot({ path: testInfo.outputPath(`registration-${await tab.innerText()}-${width}.png`) });
    }
  }
  expect(errors).toEqual([]);
});

test('switching type cancels a lookup and rejects its delayed session list', async ({ page, request }) => {
  const fixture = await (await request.get('/_test/fixture')).json();
  let release;
  let delivered;
  const gate = new Promise(resolve => release = resolve);
  const finished = new Promise(resolve => delivered = resolve);
  await page.route('**/api/v1/sessions?*', async route => {
    if (new URL(route.request().url()).searchParams.get('workspace_root') !== fixture.root) return route.continue();
    await gate;
    try {
      await route.fulfill({ json: { workspace_root: fixture.root, sessions: [{ session_id: 'late-session', title: 'Stale workspace session', updated_at: new Date().toISOString() }] } });
    } catch { /* The tab switch aborts the original request. */ }
    delivered();
  });
  await page.goto('/sessions');
  await page.getByRole('button', { name: 'Add or create session', exact: true }).click();
  const dialog = page.getByRole('dialog', { name: 'New session', exact: true });
  await dialog.getByRole('tab', { name: 'Workspace', exact: true }).click();
  await dialog.getByLabel('Workspace directory').fill(fixture.root);
  const requested = page.waitForRequest(request => new URL(request.url()).pathname === '/api/v1/sessions');
  await dialog.getByRole('button', { name: 'Load sessions', exact: true }).click();
  await requested;
  await dialog.getByRole('tab', { name: 'Project', exact: true }).click();
  await dialog.getByRole('combobox', { name: 'Project', exact: true }).selectOption({ label: 'Solo' });
  await dialog.getByRole('button', { name: 'Load sessions', exact: true }).click();
  await expect(dialog.getByRole('button', { name: /Create new session/ })).toBeVisible();
  release();
  await finished;
  await expect(dialog.getByText('Stale workspace session', { exact: true })).toHaveCount(0);
  await dialog.getByRole('tab', { name: 'Workspace', exact: true }).click();
  await expect(dialog.getByLabel('Workspace directory')).toHaveValue(fixture.root);
  await expect(dialog.getByRole('button', { name: /Create new session/ })).toHaveCount(0);
});

test('project tab explains an empty project list', async ({ page, request }) => {
  const registry = await (await request.get('/api/v1/registered-sessions')).json();
  await page.route('**/api/v1/registered-sessions', route => route.fulfill({ json: { ...registry, projects: [] } }));
  await page.goto('/sessions');
  await page.getByRole('button', { name: 'Add or create session', exact: true }).click();
  const dialog = page.getByRole('dialog', { name: 'New session', exact: true });
  await dialog.getByRole('tab', { name: 'Project', exact: true }).click();
  await expect(dialog.getByRole('heading', { name: 'No projects yet', exact: true })).toBeVisible();
  await expect(dialog.getByRole('combobox')).toHaveCount(0);
  await dialog.getByRole('tab', { name: 'Temporary', exact: true }).click();
  await expect(dialog.getByRole('button', { name: 'New chat', exact: true })).toBeEnabled();
});
