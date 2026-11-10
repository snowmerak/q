import { test, expect } from '@playwright/test';
import { mkdtemp, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';

test('ticket polling refreshes all loaded pages and preserves further pagination', async ({ page, request }) => {
  const root = await mkdtemp(join(tmpdir(), 'q-ticket-pagination-'));
  let project;
  try {
    const created = await request.post('/api/v1/projects', { data: { name: 'Ticket pagination', workspace_roots: [root] } });
    expect(created.status()).toBe(201);
    project = await created.json();
    const tickets = [];
    for (let index = 0; index < 205; index++) {
      const response = await request.post('/api/v1/work/items', { data: {
        workspace_id: project.id, kind: 'ticket', title: `Paging ticket ${index + 1}`,
        content: 'Paused work for pagination regression.', schedule: { paused: true }
      } });
      expect(response.status()).toBe(201);
      tickets.push(await response.json());
    }
    await page.clock.install();
    await page.goto(`/tickets?workspace_id=${project.id}`);
    const cards = page.locator('.ticket-board .work-card');
    await expect(cards).toHaveCount(100);
    await page.getByRole('button', { name: 'More tickets', exact: true }).click();
    await expect(cards).toHaveCount(200);

    const changed = tickets[149];
    expect((await request.put(`/api/v1/work/items/${changed.id}`, { data: {
      revision: changed.revision, title: 'Updated on the second page', state: changed.state, labels: changed.labels
    } })).ok()).toBe(true);
    const refreshed = page.waitForResponse(response => {
      const url = new URL(response.url());
      return url.pathname === '/api/v1/work/items' && !!url.searchParams.get('cursor');
    });
    await page.clock.fastForward(5000);
    await refreshed;
    await expect(cards).toHaveCount(200);
    await expect(cards.filter({ hasText: 'Updated on the second page' })).toHaveCount(1);

    await page.getByRole('button', { name: 'More tickets', exact: true }).click();
    await expect(cards).toHaveCount(205);
    await expect(page.getByRole('button', { name: 'More tickets', exact: true })).toHaveCount(0);
    const last = tickets[204];
    expect((await request.put(`/api/v1/work/items/${last.id}`, { data: {
      revision: last.revision, title: 'Updated on the third page', state: last.state, labels: last.labels
    } })).ok()).toBe(true);
    await page.clock.fastForward(5000);
    await expect(cards.filter({ hasText: 'Updated on the third page' })).toHaveCount(1);
    await expect(cards).toHaveCount(205);
  } finally {
    if (project) expect((await request.delete(`/api/v1/projects/${project.id}`)).ok()).toBe(true);
    await rm(root, { recursive: true, force: true });
  }
});
