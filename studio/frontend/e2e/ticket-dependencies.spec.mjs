import { test, expect } from '@playwright/test';

test('ticket prerequisites survive creation and editing, expose cancellation waits, and reject cycles', async ({ page, request }, testInfo) => {
  const errors = [];
  page.on('pageerror', error => errors.push(error.message));
  const project = (await (await request.get('/api/v1/projects')).json()).projects.find(value => value.name === 'Integration');
  const response = await request.post('/api/v1/work/items', { data: {
    workspace_id: project.id, kind: 'ticket', title: 'Dependency plan', content: 'Write the plan.',
    schedule: { not_before: new Date(Date.now() + 86_400_000).toISOString(), paused: true }
  } });
  expect(response.status()).toBe(201);
  const prerequisite = await response.json();
  await page.goto(`/tickets?workspace_id=${project.id}`);
  await page.getByRole('button', { name: 'New ticket', exact: true }).click();
  const create = page.locator('.work-create');
  await create.getByRole('textbox', { name: 'Work item title', exact: true }).fill('Dependent implementation');
  await create.getByRole('textbox', { name: 'Work item body', exact: true }).fill('Implement the approved plan.');
  await create.getByRole('searchbox', { name: 'Find prerequisite tickets', exact: true }).fill('Dependency plan');
  await create.getByRole('checkbox', { name: /Dependency plan/ }).check();
  const created = page.waitForResponse(value => value.url().endsWith('/api/v1/work/items') && value.request().method() === 'POST');
  await create.getByRole('button', { name: 'Create ticket', exact: true }).click();
  const createdResponse = await created;
  expect(createdResponse.status()).toBe(201);
  const dependent = await createdResponse.json();
  expect(dependent.depends_on).toEqual([prerequisite.id]);
  const thread = page.getByRole('region', { name: 'Work item thread' });
  await expect(thread.getByRole('link', { name: /Dependency plan/ })).toHaveAttribute('href', `/tickets?workspace_id=${project.id}&item_id=${prerequisite.id}`);
  await expect(thread).toContainText('Waiting for prerequisite tickets');
  await expect(thread).toContainText('Waiting for completion');
  expect((await (await request.get(`/api/v1/work/items/${dependent.id}/attempts`)).json()).attempts).toEqual([]);

  const cancelled = await request.post(`/api/v1/work/items/${prerequisite.id}/control`, { data: { action: 'cancel', revision: prerequisite.revision } });
  expect(cancelled.ok()).toBe(true);
  await page.reload();
  await expect(thread).toContainText('Waiting for completion');
  expect((await (await request.get(`/api/v1/work/items/${dependent.id}`)).json()).waiting_on).toEqual([prerequisite.id]);
  await page.getByRole('button', { name: 'Edit details', exact: true }).click();
  const edit = thread.locator('form').filter({ has: page.getByLabel('Edit work item title', { exact: true }) });
  await expect(edit.getByRole('checkbox', { name: /Dependency plan/ })).toBeChecked();
  await edit.getByLabel('Edit work item title', { exact: true }).fill('Dependent implementation revised');
  await page.getByRole('button', { name: 'Save details', exact: true }).click();
  await expect(thread.getByRole('heading', { name: /Dependent implementation revised/ })).toBeVisible();
  await expect(thread).toContainText('Waiting for completion');

  await thread.getByRole('link', { name: /Dependency plan/ }).click();
  await page.getByRole('button', { name: 'Edit details', exact: true }).click();
  await edit.getByRole('searchbox', { name: 'Find prerequisite tickets', exact: true }).fill('Dependent implementation revised');
  await edit.getByRole('checkbox', { name: /Dependent implementation revised/ }).check();
  await page.getByRole('button', { name: 'Save details', exact: true }).click();
  await expect(page.locator('.work-error')).toContainText('cycle');
  expect((await (await request.get(`/api/v1/work/items/${prerequisite.id}`)).json()).depends_on || []).toEqual([]);
  await page.setViewportSize({ width: 390, height: 844 });
  await edit.getByRole('group', { name: 'Prerequisite tickets', exact: true }).scrollIntoViewIfNeeded();
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
  await page.screenshot({ path: testInfo.outputPath('ticket-dependencies-mobile.png') });
  expect(errors).toEqual([]);
});
