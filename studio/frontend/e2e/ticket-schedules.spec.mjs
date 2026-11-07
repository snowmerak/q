import { test, expect } from '@playwright/test';

test.use({ timezoneId: 'Asia/Seoul' });

test('ticket schedules preserve local start time, support repeats and pause, and reject invalid intervals', async ({ page, request }, testInfo) => {
  const errors = [];
  page.on('pageerror', error => errors.push(error.message));
  const project = (await (await request.get('/api/v1/projects')).json()).projects.find(p => p.name === 'Integration');
  await page.goto(`/tickets?workspace_id=${project.id}`);
  await page.getByRole('button', { name: 'New ticket', exact: true }).click();
  const create = page.locator('.work-create');
  await create.getByRole('textbox', { name: 'Work item title', exact: true }).fill('Scheduled news collection');
  await create.getByRole('textbox', { name: 'Work item body', exact: true }).fill('Collect verified news and attach source links.');
  await create.getByRole('checkbox', { name: 'Schedule execution', exact: true }).check();
  const localStart = await page.evaluate(() => {
    const date = new Date(Date.now() + 86_400_000);
    const pad = value => String(value).padStart(2, '0');
    return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}T09:30:07`;
  });
  await create.getByLabel('Available from', { exact: true }).fill(localStart);
  const created = page.waitForResponse(r => r.url().endsWith('/api/v1/work/items') && r.request().method() === 'POST');
  await create.getByRole('button', { name: 'Create ticket', exact: true }).click();
  const response = await created;
  expect(response.status()).toBe(201);
  const item = await response.json();
  const expectedStart = new Date(`${localStart}+09:00`).toISOString();
  expect(new Date(item.schedule.not_before).toISOString()).toBe(expectedStart);
  expect(item.schedule.every || '').toBe('');
  const current = async () => (await request.get(`/api/v1/work/items/${item.id}`)).json();
  await expect(page.getByRole('region', { name: 'Ticket schedule', exact: true })).toContainText('Scheduled execution');
  await page.reload();
  await page.getByRole('button', { name: 'Edit details', exact: true }).click();
  const detail = page.locator('.work-thread form').filter({ has: page.getByLabel('Edit work item title', { exact: true }) });
  await expect(detail.getByLabel('Available from', { exact: true })).toHaveValue(localStart);
  await detail.getByRole('textbox', { name: 'Repeat every', exact: true }).fill('30m');
  await detail.getByRole('checkbox', { name: 'Pause schedule', exact: true }).check();
  await page.getByRole('button', { name: 'Save details', exact: true }).click();
  await expect.poll(async () => (await current()).schedule.every).toBe('30m0s');
  expect((await current()).schedule.paused).toBe(true);
  await expect(page.getByRole('region', { name: 'Ticket schedule', exact: true })).toContainText('Paused');
  await page.getByRole('button', { name: 'Edit details', exact: true }).click();
  await expect(detail.getByLabel('Available from', { exact: true })).toHaveValue(localStart);
  await detail.getByRole('textbox', { name: 'Repeat every', exact: true }).fill('5s');
  await page.getByRole('button', { name: 'Save details', exact: true }).click();
  await expect(page.getByRole('alert')).toContainText('at least 1m');
  expect((await current()).schedule.every).toBe('30m0s');
  await expect(detail.getByRole('textbox', { name: 'Repeat every', exact: true })).toHaveValue('5s');
  await detail.getByRole('textbox', { name: 'Repeat every', exact: true }).fill('2h');
  await detail.getByRole('checkbox', { name: 'Pause schedule', exact: true }).uncheck();
  await detail.locator('.ticket-schedule').scrollIntoViewIfNeeded();
  await page.screenshot({ path: testInfo.outputPath('ticket-schedule-desktop.png') });
  await page.setViewportSize({ width: 390, height: 844 });
  await detail.getByRole('textbox', { name: 'Repeat every', exact: true }).scrollIntoViewIfNeeded();
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
  await page.screenshot({ path: testInfo.outputPath('ticket-schedule-mobile.png') });
  await page.getByRole('button', { name: 'Save details', exact: true }).click();
  await expect.poll(async () => (await current()).schedule.every).toBe('2h0m0s');
  expect(new Date((await current()).schedule.not_before).toISOString()).toBe(expectedStart);
  await page.reload();
  await expect(page.getByRole('region', { name: 'Ticket schedule', exact: true })).toContainText('2h0m0s');
  expect(errors).toEqual([]);
});

test('the coordinator issues a separate run ticket and the UI links it to its schedule', async ({ page, request }) => {
  const project = (await (await request.get('/api/v1/projects')).json()).projects.find(p => p.name === 'Integration');
  const response = await request.post('/api/v1/work/items', { data: {
    workspace_id: project.id, kind: 'ticket', title: 'Periodic source check', content: 'Read sources and record the evidence.',
    execution_mode: 'report', schedule: { every: '1h' }
  } });
  expect(response.status()).toBe(201);
  const parent = await response.json();
  let run;
  await expect.poll(async () => {
    const list = await (await request.get(`/api/v1/work/items?workspace_id=${project.id}&kind=ticket&limit=100`)).json();
    const runs = list.items.filter(item => item.schedule_id === parent.id);
    run = runs[0];
    return runs.length;
  }).toBe(1);
  expect(run.id).not.toBe(parent.id);
  expect(run.body_post_id).not.toBe(parent.body_post_id);
  expect(run.schedule).toBeUndefined();
  await page.goto(`/tickets?workspace_id=${project.id}&item_id=${run.id}`);
  await expect(page.getByRole('link', { name: 'Open schedule', exact: true })).toBeVisible();
  await page.getByRole('link', { name: 'Open schedule', exact: true }).click();
  await expect(page).toHaveURL(new RegExp(`item_id=${parent.id}`));
  await expect(page.getByRole('region', { name: 'Ticket schedule', exact: true })).toContainText('Recurring schedule');
  // Leave the isolated fixture's schedule paused after verifying automatic issuance.
  const current = await (await request.get(`/api/v1/work/items/${parent.id}`)).json();
  expect((await request.put(`/api/v1/work/items/${parent.id}`, { data: {
    revision: current.revision, title: current.title, state: current.state, labels: current.labels,
    schedule: { ...current.schedule, paused: true }
  } })).ok()).toBe(true);
});
