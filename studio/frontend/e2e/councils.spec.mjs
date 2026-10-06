import { test, expect } from '@playwright/test';
import { readFile } from 'node:fs/promises';

test('council list appears while model discovery is still pending', async ({ page }) => {
  const council = { id: '55555555-5555-4555-8555-555555555555', name: 'Available council', scope: 'independent', members: [{ model: 'test/one' }, { model: 'test/two' }], chair: { model: 'test/one' } };
  let releaseModels;
  const modelsReady = new Promise((resolve) => { releaseModels = resolve; });
  await page.route('**/api/v1/councils', (route) => route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ councils: [council] }) }));
  await page.route('**/api/v1/settings/models', async (route) => {
    await modelsReady;
    await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ models: [] }) });
  });
  await page.goto('/councils');
  await expect(page.getByRole('button', { name: /Available council/ })).toBeVisible();
  releaseModels();
});

test('independent council is configured in Studio and reopens without a workspace', async ({ page, request }) => {
  const errors = [];
  page.on('pageerror', (error) => errors.push(error.message));
  await page.route('**/api/v1/settings/models', async (route) => {
    await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ models: [
      { id: 'default/test-model', reasoning_control: 'effort', reasoning_efforts: ['low', 'high'] },
      { id: 'default/test-model-2', reasoning_control: 'effort', reasoning_efforts: ['medium', 'high'] },
      { id: 'default/test-chair', reasoning_control: 'effort', reasoning_efforts: ['high', 'xhigh'] }
    ] }) });
  });
  await page.goto('/councils');
  await expect(page.getByRole('heading', { name: 'Councils', exact: true, level: 1 })).toBeVisible();
  await page.getByRole('button', { name: 'New', exact: true }).click();
  await page.getByLabel('Name', { exact: true }).fill('Independent review');
  await expect(page.getByLabel('Scope')).toHaveValue('independent');
  await expect(page.getByLabel('Workspace directory')).toHaveCount(0);
  await expect(page.getByLabel('Reasoning for member 1').locator('option')).toHaveText(['Provider default', 'low', 'high']);
  await page.getByLabel('Reasoning for member 1').selectOption('high');
  await page.getByLabel('Chair model').selectOption('default/test-chair');
  await expect(page.getByLabel('Chair reasoning').locator('option')).toHaveText(['Provider default', 'high', 'xhigh']);
  await page.getByLabel('Chair reasoning').selectOption('xhigh');
  await expect(page.getByLabel('Rounds')).toHaveValue('2');
  await page.getByLabel('Rounds').selectOption('3');
  await page.getByRole('button', { name: 'Create council' }).click();
  await expect(page.getByRole('heading', { name: 'Independent review' })).toBeVisible();
  const id = new URL(page.url()).pathname.split('/').pop();
  const response = await request.get(`/api/v1/councils/${id}`);
  expect(response.ok()).toBe(true);
  const detail = await response.json();
  expect(detail.council.scope).toBe('independent');
  expect(detail.council.workspace_root).toBeUndefined();
  expect(detail.council.project_id).toBeUndefined();
  expect(detail.council.chair.model).toBe('default/test-chair');
  expect(detail.council.members[0].reasoning_effort).toBe('high');
  expect(detail.council.chair.reasoning_effort).toBe('xhigh');
  expect(detail.council.rounds).toBe(3);
  await page.reload();
  await expect(page.getByRole('heading', { name: 'Independent review' })).toBeVisible();
  await page.getByRole('button', { name: 'Configure' }).click();
  await expect(page.getByLabel('Chair model')).toHaveValue('default/test-chair');
  await expect(page.getByLabel('Reasoning for member 1')).toHaveValue('high');
  await expect(page.getByLabel('Chair reasoning')).toHaveValue('xhigh');
  await expect(page.getByLabel('Rounds')).toHaveValue('3');
  await page.getByText('Export history ▾').click();
  const markdownDownload = page.waitForEvent('download');
  await page.getByRole('link', { name: 'Markdown' }).click();
  expect((await markdownDownload).suggestedFilename()).toBe(`council-${id}.md`);
  const jsonDownload = page.waitForEvent('download');
  await page.getByRole('link', { name: 'JSON' }).click();
  const savedJSON = await jsonDownload;
  expect(savedJSON.suggestedFilename()).toBe(`council-${id}.json`);
  const exported = JSON.parse(await readFile(await savedJSON.path(), 'utf8'));
  expect(exported.council.id).toBe(id);
  expect(errors).toEqual([]);
});

test('running council keeps its answers unobscured', async ({ page }) => {
  const id = '11111111-1111-4111-8111-111111111111';
  const turn = {
    id: '22222222-2222-4222-8222-222222222222', council_id: id,
    prompt: 'Compare the options', status: 'running', stage: 'reviews',
    members: [{ model: 'test/one' }, { model: 'test/two' }], chair: { model: 'test/one' },
    responses: [{ label: 'A', model: 'test/one', text: 'First independent answer' }], reviews: [],
    created_at: '2026-10-06T12:00:00Z'
  };
  const council = { id, name: 'Running review', scope: 'independent', members: turn.members, chair: turn.chair };
  await page.route('**/api/v1/settings/models', (route) => route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ models: [{ id: 'test/one' }, { id: 'test/two' }] }) }));
  await page.route('**/api/v1/councils', (route) => route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ councils: [council] }) }));
  await page.route(`**/api/v1/councils/${id}`, (route) => route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ council, turns: [turn] }) }));
  await page.route(`**/api/v1/councils/${id}/runs/${turn.id}`, (route) => route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(turn) }));
  await page.goto(`/councils/${id}`);
  await expect(page.getByText('First independent answer')).toBeVisible();
  await expect(page.getByRole('tab', { name: /B test\/two/ })).toBeVisible();
  await page.getByRole('tab', { name: /B test\/two/ }).click();
  await expect(page.getByText("Waiting for this model's answer…")).toBeVisible();
  await page.getByRole('tab', { name: /A test\/one/ }).click();
  await expect(page.getByLabel('Ask this council')).toHaveCount(0);
  await expect(page.getByRole('button', { name: 'Cancel turn' })).toBeVisible();
  const layout = await page.locator('.running-controls').evaluate((controls) => {
    const answer = document.querySelector('.council-turn');
    return { position: getComputedStyle(controls).position, controlsBottom: controls.getBoundingClientRect().bottom, answerTop: answer.getBoundingClientRect().top };
  });
  expect(layout.position).toBe('static');
  expect(layout.controlsBottom).toBeLessThanOrEqual(layout.answerTop);

  Object.assign(turn, {
    status: 'completed', stage: 'complete', final: 'Chair conclusion', total_rounds: 3, current_round: 3,
    ranking: [{ label: 'A', average_rank: 1, reviews: 1 }],
    reviews: [{ model: 'test/two', text: 'Answer A was more precise', ranking: ['A'] }],
    rounds: [
      { number: 1, responses: turn.responses },
      { number: 2, reviews: [{ model: 'test/two', text: 'Answer A was more precise', ranking: ['A'] }], ranking: [{ label: 'A', average_rank: 1, reviews: 1 }] },
      { number: 3, responses: [{ label: 'A', model: 'test/one', text: 'Revised answer after review' }], reviews: [{ model: 'test/two', text: JSON.stringify({ analysis: 'The revision is clearer', revised_answer: 'Hidden duplicate' }), ranking: ['A'] }] }
    ]
  });
  await page.reload();
  await expect(page.getByText('Chair conclusion')).toBeVisible();
  await expect(page.getByText('Average rank 1.00 · 1 reviews')).toHaveCount(0);
  await expect(page.getByText('03 · INTEGRATED REVIEW')).toBeVisible();
  await expect(page.getByText('Revised answer after review')).toBeVisible();
  await page.locator('.review-list summary').first().click();
  await expect(page.getByText('Answer A was more precise')).toBeVisible();
  await page.locator('.review-list summary').last().click();
  await expect(page.getByText('The revision is clearer')).toBeVisible();
  await expect(page.getByText('Hidden duplicate')).toHaveCount(0);
});

test('completed council switches between every independent answer', async ({ page }) => {
  const id = '33333333-3333-4333-8333-333333333333';
  const members = [{ model: 'test/one' }, { model: 'test/two' }, { model: 'test/three' }];
  const council = { id, name: 'Answer tabs', scope: 'independent', members, chair: members[0] };
  const turn = {
    id: '44444444-4444-4444-8444-444444444444', council_id: id,
    prompt: 'Compare the options', status: 'completed', stage: 'complete',
    members, chair: members[0],
    responses: members.map((seat, index) => ({ label: String.fromCharCode(65 + index), model: seat.model, text: `Independent answer ${index + 1}` })),
    reviews: [], final: 'Chair conclusion', created_at: '2026-10-06T12:00:00Z'
  };
  await page.route('**/api/v1/settings/models', (route) => route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ models: members.map((seat) => ({ id: seat.model })) }) }));
  await page.route('**/api/v1/councils', (route) => route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ councils: [council] }) }));
  await page.route(`**/api/v1/councils/${id}`, (route) => route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ council, turns: [turn] }) }));
  await page.goto(`/councils/${id}`);
  const panel = page.getByRole('tabpanel');
  await expect(panel).toContainText('Independent answer 1');
  await page.getByRole('tab', { name: /B test\/two/ }).click();
  await expect(panel).toContainText('Independent answer 2');
  await page.getByRole('tab', { name: /C test\/three/ }).click();
  await expect(panel).toContainText('Independent answer 3');
  await page.getByRole('tab', { name: /A test\/one/ }).click();
  await expect(panel).toContainText('Independent answer 1');
});
