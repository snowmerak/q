import { test, expect } from '@playwright/test';
import { createServer } from 'node:http';

const archive = (name, scope = 'global') => ({
  name, scope, ...(scope === 'workspace' ? { workspace_root: '/projects/next' } : {}), registered: false,
  state: 'current', exists: true, records: 1, eligible: 1, current: 1, missing: 0, stale: 0, excluded: 0,
  vectors: 1, current_vectors: 1, invalid_vectors: 0, unreadable: 0, complete: true,
  kinds: [], models: [], issues: []
});
const metadata = () => ({
  model: 'current', dimensions: 3, enabled: true, observed_at: new Date().toISOString(),
  completed_at: '0001-01-01T00:00:00Z', complete: true, discovery_roots: [], warnings: [], archives: []
});

// A real HTTP response stays open between writes. route.fulfill would buffer
// the entire body and could not prove that the first row renders before EOF.
async function openStatusStream(baseURL) {
  let response;
  let closed = false;
  let accept = '';
  const first = Buffer.from(JSON.stringify({ type: 'archive', archive: archive('전역 라이브러리') }) + '\n');
  const split = first.indexOf(Buffer.from('전역')) + 1;
  const server = createServer(async (request, writer) => {
    if (new URL(request.url, baseURL).pathname !== '/api/v1/embeddings/status') {
      try {
        const upstream = await fetch(new URL(request.url, baseURL));
        const headers = Object.fromEntries([...upstream.headers].filter(([name]) => ['content-type', 'content-security-policy', 'cache-control'].includes(name)));
        writer.writeHead(upstream.status, headers);
        writer.end(Buffer.from(await upstream.arrayBuffer()));
      } catch (error) { writer.writeHead(502); writer.end(error.message); }
      return;
    }
    accept = request.headers.accept;
    response = writer;
    writer.on('close', () => { closed = true; });
    writer.writeHead(200, { 'Content-Type': 'application/x-ndjson', 'Cache-Control': 'no-store' });
    writer.write(JSON.stringify({ type: 'started', snapshot: metadata() }) + '\n');
    writer.write(first.subarray(0, split));
  });
  await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
  return {
    url: `http://127.0.0.1:${server.address().port}/settings?section=embeddings`,
    releaseArchive: () => response.write(first.subarray(split)),
    complete: () => response.end(JSON.stringify({ type: 'archive', archive: archive('Next workspace', 'workspace') }) + '\n'
      + JSON.stringify({ type: 'completed', snapshot: { ...metadata(), completed_at: new Date().toISOString() } })),
    interrupt: () => response.end(),
    isClosed: () => closed,
    accept: () => accept,
    close: () => new Promise(resolve => { server.closeAllConnections(); server.close(resolve); })
  };
}

test('embedding archives render as they arrive before the scan completes', async ({ page, baseURL }, testInfo) => {
  const errors = [];
  page.on('pageerror', error => errors.push(error.message));
  const stream = await openStatusStream(baseURL);
  try {
    await page.goto(stream.url);
    const panel = page.getByRole('region', { name: 'Embedding status', exact: true });
    const rows = panel.getByRole('table').locator('tbody > tr:not(.details-row)');
    await expect(panel.getByRole('status')).toContainText('0 archives received');
    await expect(rows).toHaveCount(0);
    stream.releaseArchive();
    await expect(rows).toHaveCount(1);
    await expect(rows.first()).toContainText('전역 라이브러리');
    await expect(panel.getByRole('status')).toContainText('1 archive received');
    await expect(panel.locator('.coverage').first()).toContainText('1 / 1');
    await expect(panel.getByRole('button', { name: 'Checking…', exact: true })).toBeDisabled();
    expect(stream.isClosed()).toBe(false);
    expect(stream.accept()).toContain('application/x-ndjson');
    await page.screenshot({ path: testInfo.outputPath('first-archive.png') });
    stream.complete();
    await expect(rows).toHaveCount(2);
    await expect(panel.locator('.coverage').first()).toContainText('2 / 2');
    await expect(panel.getByRole('button', { name: 'Refresh', exact: true })).toBeEnabled();
    await expect(panel.getByRole('status')).toHaveCount(0);
    await page.screenshot({ path: testInfo.outputPath('completed.png') });
    expect(errors).toEqual([]);
  } finally { await stream.close(); }
});

test('interrupted embedding scans retain received rows and report partial results', async ({ page, baseURL }) => {
  const stream = await openStatusStream(baseURL);
  try {
    await page.goto(stream.url);
    const panel = page.getByRole('region', { name: 'Embedding status', exact: true });
    await expect(panel.getByRole('status')).toContainText('0 archives received');
    stream.releaseArchive();
    await expect(panel.getByRole('table')).toContainText('전역 라이브러리');
    stream.interrupt();
    await expect(panel.getByRole('alert')).toContainText('stream ended before the scan completed');
    await expect(panel.getByRole('alert')).toContainText('archives received before the scan stopped');
    await expect(panel.getByRole('table')).toContainText('전역 라이브러리');
    await expect(panel.locator('.model-summary')).toContainText('Scan interrupted');
    await expect(panel.getByRole('button', { name: 'Refresh', exact: true })).toBeEnabled();
  } finally { await stream.close(); }
});

test('leaving embedding status cancels the unfinished HTTP stream', async ({ page, baseURL }) => {
  const errors = [];
  page.on('pageerror', error => errors.push(error.message));
  const stream = await openStatusStream(baseURL);
  try {
    await page.goto(stream.url);
    const panel = page.getByRole('region', { name: 'Embedding status', exact: true });
    await expect(panel.getByRole('status')).toContainText('0 archives received');
    stream.releaseArchive();
    await expect(panel.getByRole('table')).toContainText('전역 라이브러리');
    await page.getByRole('complementary', { name: 'Studio navigation' }).getByRole('button', { name: 'Sessions', exact: true }).click();
    await expect.poll(stream.isClosed).toBe(true);
    await expect(panel).toHaveCount(0);
    expect(errors).toEqual([]);
  } finally { await stream.close(); }
});
