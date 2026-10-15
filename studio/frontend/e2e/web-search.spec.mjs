import { test, expect } from '@playwright/test';

test('web search owns browser capacity and model choices', async ({ page, request }) => {
  const errors = [];
  page.on('pageerror', error => errors.push(error.message));
  const before = await (await request.get('/api/v1/settings')).json();
  await page.goto('/settings?section=web-search');
  await expect(page.getByRole('heading', { name: 'Web Search', exact: true })).toBeVisible();
  await page.getByLabel('Maximum concurrent page reads').selectOption('3');
  await expect.poll(async () => (await (await request.get('/api/v1/settings')).json()).web_search.max_concurrent_reads).toBe(3);
  await page.getByLabel('Source and evidence model').selectOption('typesafe/decision-test');
  await page.getByLabel('Completion model', { exact: true }).selectOption('typesafe/decision-test');
  await expect(page.getByRole('status').filter({ hasText: 'Web search tools enabled.' })).toBeVisible();
  await page.getByLabel('Generative search model').selectOption('default/test-model');
  await expect.poll(async () => (await (await request.get('/api/v1/settings')).json()).web_search.generative_model).toBe('default/test-model');
  await page.reload();
  await expect(page.getByLabel('Maximum concurrent page reads')).toHaveValue('3');
  await expect(page.getByLabel('Source and evidence model')).toHaveValue('typesafe/decision-test');
  await expect(page.getByLabel('Generative search model')).toHaveValue('default/test-model');
  await page.goto('/settings?section=system-one');
  await expect(page.getByRole('heading', { name: 'Decision API', exact: true })).toBeVisible();
  await expect(page.getByLabel('Source and evidence model')).not.toBeVisible();
  await request.put('/api/v1/settings/web-search', { data: { max_concurrent_reads: before.web_search.max_concurrent_reads, generative_model: before.web_search.generative_model } });
  await request.put('/api/v1/settings/system-one/models/web-search', { data: { model: before.web_search.navigation_model } });
  await request.put('/api/v1/settings/system-one/models/web-search-finish', { data: { model: before.web_search.completion_model } });
  const after = await (await request.get('/api/v1/settings')).json();
  expect(after.models.default_model).toBe(before.models.default_model);
  expect(errors).toEqual([]);
});
