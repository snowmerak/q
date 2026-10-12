import { test, expect } from '@playwright/test';

test('embedding status reads the API and keeps the page usable on desktop and mobile', async ({ page, request }, testInfo) => {
  const errors = [];
  page.on('pageerror', error => errors.push(error.message));
  const fixture = await (await request.get('/_test/fixture')).json();
  await page.goto('/settings?section=embeddings');
  const panel = page.getByRole('region', { name: 'Embedding status', exact: true });
  await expect(panel.getByRole('heading', { name: 'Embedding status' })).toBeVisible();
  await expect(panel.getByRole('table', { name: 'Embedding coverage by archive' })).toBeVisible();
  await panel.getByLabel('Additional workspace path').fill(fixture.root);
  const refreshed = page.waitForResponse(response => new URL(response.url()).pathname === '/api/v1/embeddings/status' && new URL(response.url()).searchParams.get('workspace_root') === fixture.root);
  await panel.getByRole('button', { name: 'Inspect workspace', exact: true }).click();
  const response = await refreshed;
  expect(response.status()).toBe(200);
  const snapshot = await response.json();
  expect(snapshot.archives.some(archive => archive.workspace_root === fixture.root)).toBe(true);
  const table = panel.getByRole('table');
  await expect(table.locator('tbody > tr:not(.details-row)')).toHaveCount(snapshot.archives.length);
  for (const archive of snapshot.archives) {
    const row = table.locator('tbody > tr:not(.details-row)').filter({ has: page.getByText(archive.workspace_root || 'Global skills and propositions', { exact: true }) });
    await expect(row).toContainText(`${archive.current.toLocaleString()} / ${archive.eligible.toLocaleString()}`);
  }
  await panel.getByText('Global Library details', { exact: true }).click();
  await expect(panel.getByText(/source records ·/).first()).toBeVisible();
  for (const width of [1440, 390]) {
    await page.setViewportSize({ width, height: 900 });
    await expect(panel.getByRole('button', { name: 'Refresh', exact: true })).toBeVisible();
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
    await page.screenshot({ path: testInfo.outputPath(`embeddings-${width}.png`) });
  }
  expect(errors).toEqual([]);
});

test('manual sync repairs an unregistered archive through the real API and survives navigation', async ({ page, request }) => {
  const fixture = await (await request.get('/_test/fixture')).json();
  const configured = await request.put('/api/v1/settings/models/embedding', { data: { model: 'default/test-model', embedding_dimensions: 3 } });
  expect(configured.status()).toBe(200);
  await page.goto('/settings?section=embeddings');
  const panel = page.getByRole('region', { name: 'Embedding status', exact: true });
  const row = panel.getByRole('table').locator('tbody > tr:not(.details-row)').filter({ has: page.getByText(fixture.embedding_root, { exact: true }) });
  await expect(row).toContainText('Needs sync');
  await expect(row).toContainText('Unregistered workspace');
  const accepted = page.waitForResponse(response => new URL(response.url()).pathname === '/api/v1/embeddings/sync' && response.request().method() === 'POST');
  await row.getByRole('button', { name: /^Sync/ }).click();
  const response = await accepted;
  expect(response.status()).toBe(202);
  const job = await response.json();
  expect(job.scope).toBe('workspace');
  expect(job.workspace_root).toBe(fixture.embedding_root);
  await page.goto('/sessions');
  await expect.poll(async () => (await (await request.get(`/api/v1/embeddings/sync/${job.id}`)).json()).state).toBe('succeeded');
  await page.goto('/settings?section=embeddings');
  await expect(panel.getByRole('region', { name: 'Embedding sync progress' })).toContainText('Sync complete');
  await expect(row).toContainText('Up to date');
  await expect(row).toContainText('1 / 1');
  const repeat = await request.post('/api/v1/embeddings/sync', { data: { scope: 'workspace', workspace_root: fixture.embedding_root } });
  expect(repeat.status()).toBe(202);
  const repeated = await repeat.json();
  await expect.poll(async () => (await (await request.get(`/api/v1/embeddings/sync/${repeated.id}`)).json()).state).toBe('succeeded');
  expect((await (await request.get(`/api/v1/embeddings/sync/${repeated.id}`)).json()).embedded).toBe(0);
  for (const scope of ['global', 'all']) {
    const accepted = await request.post('/api/v1/embeddings/sync', { data: { scope } });
    expect(accepted.status()).toBe(202);
    const job = await accepted.json();
    await expect.poll(async () => (await (await request.get(`/api/v1/embeddings/sync/${job.id}`)).json()).state).toBe('succeeded');
  }
  // Restore the shared fixture's default before other specs use it.
  const disabled = await request.put('/api/v1/settings/models/embedding', { data: { model: '', embedding_dimensions: 0 } });
  expect(disabled.status()).toBe(200);
});

test('embedding status distinguishes old, missing, and unreadable data and refreshes', async ({ page }) => {
  let calls = 0;
  const archive = (name, changes = {}) => ({
    name, scope: 'workspace', workspace_root: `/projects/${name}`, registered: false,
    state: 'pending', exists: true, records: 3, eligible: 3, current: 1, missing: 1, stale: 1, excluded: 0,
    vectors: 2, current_vectors: 1, invalid_vectors: 0, unreadable: 0, complete: true,
    kinds: [], models: [{ model: 'previous', dimensions: 1024, vectors: 1 }],
    index: { model: 'previous', dimensions: 1024, vectors: 1, indexed_at: '2026-10-12T00:00:00Z', configuration_matches: false }, issues: [], ...changes
  });
  await page.route('**/api/v1/embeddings/status**', route => {
    calls++;
    return route.fulfill({ json: {
      model: 'current', dimensions: 768, enabled: true, observed_at: '2026-10-12T00:00:00Z', completed_at: '2026-10-12T00:00:01Z',
      complete: calls > 1, discovery_roots: ['/projects'], warnings: [], archives: calls > 1
        ? [archive('fixed', { state: 'current', current: 3, missing: 0, stale: 0, models: [] })]
        : [archive('old'), archive('broken', { state: 'error', complete: false, unreadable: 1, issues: ['Cannot read record'] })]
    } });
  });
  await page.goto('/settings?section=embeddings');
  const panel = page.getByRole('region', { name: 'Embedding status', exact: true });
  await expect(panel.getByText('Needs sync', { exact: true })).toBeVisible();
  await expect(panel.getByText('Incomplete scan', { exact: true })).toBeVisible();
  await expect(panel.getByRole('status')).toContainText('Scan incomplete');
  await panel.getByText('broken details', { exact: true }).click();
  await expect(panel.getByText('Cannot read record', { exact: true })).toBeVisible();
  await panel.getByRole('button', { name: 'Refresh', exact: true }).click();
  await expect(panel.getByText('Up to date', { exact: true })).toBeVisible();
  await expect(panel.getByText('Scan incomplete.', { exact: false })).toHaveCount(0);
});

test('manual sync shows a failure and supports retry with Sync all', async ({ page }) => {
  let job;
  let attempts = 0;
  const archive = { name: 'old', scope: 'workspace', workspace_root: '/projects/old', registered: false,
    state: 'pending', exists: true, records: 1, eligible: 1, current: 0, missing: 1, stale: 0, excluded: 0,
    vectors: 0, current_vectors: 0, invalid_vectors: 0, unreadable: 0, complete: true, kinds: [], models: [], issues: [] };
  await page.route('**/api/v1/embeddings/status**', route => route.fulfill({ json: {
    model: 'current', dimensions: 768, enabled: true, observed_at: '2026-10-12T00:00:00Z', completed_at: '2026-10-12T00:00:01Z',
    complete: true, discovery_roots: ['/projects'], warnings: [], archives: [archive], sync_job: job
  } }));
  await page.route('**/api/v1/embeddings/sync', route => {
    expect(route.request().postDataJSON().scope).toBe('all');
    attempts++;
    job = { id: `job-${attempts}`, scope: 'all', state: attempts === 1 ? 'failed' : 'succeeded', model: 'current', dimensions: 768,
      started_at: '2026-10-12T00:00:00Z', completed_at: '2026-10-12T00:00:01Z', total: 1, completed: 1,
      failed: attempts === 1 ? 1 : 0, embedded: attempts === 1 ? 0 : 1, error: attempts === 1 ? 'Provider unavailable; retry' : undefined,
      targets: [] };
    return route.fulfill({ status: 202, json: job });
  });
  await page.goto('/settings?section=embeddings');
  const panel = page.getByRole('region', { name: 'Embedding status', exact: true });
  const sync = panel.getByRole('button', { name: 'Sync all', exact: true });
  await expect(sync).toBeEnabled();
  await sync.click();
  const progress = panel.getByRole('region', { name: 'Embedding sync progress' });
  await expect(progress).toContainText('Sync failed');
  await expect(progress).toContainText('Provider unavailable; retry');
  await expect(sync).toBeEnabled();
  await sync.click();
  await expect(progress).toContainText('Sync complete');
  await expect(progress).toContainText('1 records updated');
  expect(attempts).toBe(2);
});

test('sync completion stays available when archive inspection fails', async ({ page }) => {
  let started = false;
  const job = { id: 'fallback-job', scope: 'all', state: 'queued', model: 'current', dimensions: 768,
    started_at: '2026-10-12T00:00:00Z', total: 1, completed: 0, failed: 0, embedded: 0, targets: [] };
  await page.route('**/api/v1/embeddings/status**', route => started
    ? route.fulfill({ status: 500, json: { error: 'Selected workspace disappeared' } })
    : route.fulfill({ json: { model: 'current', dimensions: 768, enabled: true, observed_at: '2026-10-12T00:00:00Z', completed_at: '2026-10-12T00:00:01Z',
      complete: true, discovery_roots: [], warnings: [], archives: [] } }));
  await page.route('**/api/v1/embeddings/sync', route => {
    started = true;
    return route.fulfill({ status: 202, json: job });
  });
  await page.route('**/api/v1/embeddings/sync/fallback-job', route => route.fulfill({ json: { ...job, state: 'succeeded', completed: 1, embedded: 2 } }));
  await page.goto('/settings?section=embeddings');
  const panel = page.getByRole('region', { name: 'Embedding status', exact: true });
  const sync = panel.getByRole('button', { name: 'Sync all', exact: true });
  await expect(sync).toBeEnabled();
  await sync.click();
  await expect(panel.getByRole('alert')).toContainText('Selected workspace disappeared');
  await expect(panel.getByRole('region', { name: 'Embedding sync progress' })).toContainText('Sync complete');
  await expect(sync).toBeEnabled();
});
