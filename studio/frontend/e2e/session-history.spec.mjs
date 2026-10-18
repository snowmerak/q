import { test, expect } from '@playwright/test';

test('session drafts survive switching, navigation, reload, and clear after sending', async ({ page, request }) => {
  const first = await (await request.post('/api/v1/chats', { data: {} })).json();
  const second = await (await request.post('/api/v1/chats', { data: {} })).json();
  const composer = page.getByRole('textbox', { name: 'Message Q', exact: true });
  try {
    await page.goto(`/sessions/${first.registration_id}`);
    await expect(composer).toBeEnabled();
    await composer.fill('first draft');
    // Browser history selects sessions without remounting the view.
    await page.locator('.session-root .session-select').filter({ hasText: second.session.session_id.slice(0, 10) }).click();
    await expect(composer).toHaveValue('');
    await composer.fill('second draft');
    await page.goBack();
    await expect(composer).toHaveValue('first draft');
    await page.getByRole('button', { name: 'Help', exact: true }).click();
    await page.goBack();
    await expect(composer).toHaveValue('first draft');
    await page.reload();
    await expect(composer).toHaveValue('first draft');
    await page.goto(`/sessions/${second.registration_id}`);
    await expect(composer).toHaveValue('second draft');
    await composer.fill('controlled finish');
    await composer.press('Enter');
    await expect(composer).toHaveValue('');
    await expect(page.locator('.chat-heading')).toContainText('Completed');
    await page.reload();
    await expect(composer).toHaveValue('');
    await page.goto(`/sessions/${first.registration_id}`);
    await expect(composer).toHaveValue('first draft');
    await page.route(`**/api/v1/sessions/${first.session.session_id}/messages`, route => route.fulfill({ status: 503, json: { error: 'Temporarily unavailable' } }));
    await composer.press('Enter');
    await expect(page.locator('.chat-heading')).toContainText('Turn failed');
    await expect(composer).toHaveValue('first draft');
    await page.reload();
    await expect(composer).toHaveValue('first draft');
  } finally {
    await request.delete(`/api/v1/chats/${first.registration_id}`);
    await request.delete(`/api/v1/chats/${second.registration_id}`);
  }
});

test('root and nested delegation drafts stay separate', async ({ page, request }) => {
  const fixture = await (await request.get('/_test/fixture')).json();
  const registration = await (await request.post('/api/v1/registered-sessions', { data: {
    workspace_root: fixture.tree_root, session_id: fixture.tree_session_id
  } })).json();
  await page.goto(`/sessions/${registration.registration_id}`);
  const composer = page.getByRole('textbox', { name: 'Message Q', exact: true });
  const tree = page.getByRole('complementary', { name: 'Registered session tree' });
  const senior = tree.getByRole('button', { name: 'senior-developer completed', exact: true });
  const junior = tree.getByRole('button', { name: 'junior-developer completed', exact: true });
  await expect(composer).toBeEnabled();
  await composer.fill('root draft');
  await senior.click();
  await expect(composer).toHaveValue('');
  await composer.fill('senior draft');
  await junior.click();
  await expect(composer).toHaveValue('');
  await composer.fill('junior draft');
  await senior.click();
  await expect(composer).toHaveValue('senior draft');
  await tree.locator('.session-root .session-select').filter({ hasText: registration.session.session_id.slice(0, 10) }).click();
  await expect(composer).toHaveValue('root draft');
  await page.reload();
  await junior.click();
  await expect(composer).toHaveValue('junior draft');
  await request.delete(`/api/v1/registered-sessions/${registration.registration_id}`);
});

test('live tools and output appear inline and follow only from at least 95 percent scroll', async ({ page, request }, testInfo) => {
  const chat = await (await request.post('/api/v1/chats', { data: {} })).json();
  const errors = [];
  page.on('pageerror', error => errors.push(error.message));
  const run = { id: 'inline-fixture', session_id: chat.session.session_id, status: 'running', cursor: 0 };
  const history = [
    { role: 'user', content: 'Earlier work' },
    ...Array.from({ length: 35 }, (_, index) => ({ role: 'assistant', content: `Earlier response ${index}\n\n${'History paragraph. '.repeat(20)}` })),
    { role: 'user', content: 'Current task' }
  ];
  let release;
  let cursor = 0;
  let replay = [
    { type: 'stream', kind: 'thinking', start: true, content: 'Planning the next step.' },
    { type: 'stream', kind: 'response', start: true, content: 'I will read the file first.' },
    { type: 'message', role: 'assistant', content: 'I will read the file first.' },
    { type: 'tool_call', call_id: 'read-1', name: 'read_file', content: '{"path":"README.md"}' },
    { type: 'message', role: 'tool', call_id: 'read-1', name: 'read_file', content: 'File contents are available during execution.' },
    { type: 'stream', kind: 'response', start: true, content: 'Working on the next step.' }
  ];
  const base = `/api/v1/sessions/${chat.session.session_id}`;
  await page.route(`**${base}?*`, async route => {
    const response = await route.fetch();
    const detail = await response.json();
    await route.fulfill({ response, json: { ...detail, transcript: history } });
  });
  await page.route(`**${base}/runs/latest?*`, route => route.fulfill({ json: run }));
  await page.route(`**${base}/runs/inline-fixture/events?*`, async route => {
    const batch = replay ?? await new Promise(resolve => { release = resolve; });
    replay = null;
    const events = batch.map(event => ({ cursor: ++cursor, at: new Date().toISOString(), event }));
    await route.fulfill({ json: { run, events, next_cursor: cursor } });
  });
  async function emit(events) {
    await expect.poll(() => typeof release).toBe('function');
    const send = release;
    release = undefined;
    send(events);
  }
  try {
    await page.goto(`/sessions/${chat.registration_id}`);
    const transcript = page.locator('.transcript');
    await expect(transcript.locator('.tool-card')).toHaveCount(1);
    await expect(transcript.locator('.transcript-tool-result')).toContainText('File contents are available during execution.');
    await expect(transcript).toContainText('Working on the next step.');
    await expect(transcript.locator('.chat-message').filter({ hasText: 'I will read the file first.' })).toHaveCount(1);
    await expect(page.getByRole('complementary', { name: 'Current turn activity' })).toHaveCount(0);
    const at = async percent => transcript.evaluate((element, value) => {
      element.scrollTop = (element.scrollHeight - element.clientHeight) * value;
      return element.scrollTop;
    }, percent);
    const position = await at(0.94);
    await emit([{ type: 'stream', kind: 'response', content: '\n\n' + 'More live output.\n\n'.repeat(100) }]);
    await expect(transcript).toContainText('More live output.');
    expect(Math.abs(await transcript.evaluate(element => element.scrollTop) - position)).toBeLessThanOrEqual(1);
    await at(0.96);
    await emit([{ type: 'stream', kind: 'response', content: '\n\nFollowing the latest output.' }]);
    await expect(transcript).toContainText('Following the latest output.');
    await expect.poll(() => transcript.evaluate(element => element.scrollHeight - element.clientHeight - element.scrollTop)).toBeLessThanOrEqual(1);
    run.status = 'completed';
    await emit([{ type: 'result', outcome: 'succeeded', content: 'Finished the current task.' }]);
    await expect(transcript).toContainText('Finished the current task.');
    await expect(transcript.locator('.tool-card')).toHaveCount(1);
    await expect(transcript).toContainText('I will read the file first.');
    await page.screenshot({ path: testInfo.outputPath('inline-history-desktop.png') });
    await page.setViewportSize({ width: 390, height: 844 });
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
    await page.locator('.session-actions-menu > summary').click();
    await expect(page.getByRole('button', { name: 'Compact context', exact: true })).toBeVisible();
    await page.keyboard.press('Escape');
    await expect(page.locator('.session-actions-menu')).not.toHaveAttribute('open');
    await page.screenshot({ path: testInfo.outputPath('inline-history-mobile.png') });
    expect(errors).toEqual([]);
  } finally {
    release?.([]);
    await request.delete(`/api/v1/chats/${chat.registration_id}`);
  }
});

test('Models tabs isolate editors, support keyboard navigation, and restore through history and reload', async ({ page }, testInfo) => {
  await page.goto('/settings?section=models');
  const tabs = page.getByRole('tablist', { name: 'Model settings' });
  await expect(tabs.getByRole('tab', { name: 'Assignments', exact: true })).toHaveAttribute('aria-selected', 'true');
  await expect(page.locator('#model-panel-assignments .assignment-list')).toBeVisible();
  await expect(page.getByRole('textbox', { name: 'New group name', exact: true })).toBeHidden();
  await tabs.getByRole('tab', { name: 'Fallback groups', exact: true }).click();
  await expect(page.locator('#model-panel-assignments .assignment-list')).toBeHidden();
  await expect(page.getByRole('textbox', { name: 'New group name', exact: true })).toBeVisible();
  await tabs.getByRole('tab', { name: 'Fallback groups', exact: true }).press('ArrowRight');
  const behavior = tabs.getByRole('tab', { name: 'Model behavior', exact: true });
  await expect(behavior).toBeFocused();
  await expect(page.locator('.model-behavior-list')).toBeVisible();
  await page.reload();
  await expect(behavior).toHaveAttribute('aria-selected', 'true');
  await page.goBack();
  await expect(tabs.getByRole('tab', { name: 'Fallback groups', exact: true })).toHaveAttribute('aria-selected', 'true');
  await page.screenshot({ path: testInfo.outputPath('models-tabs-desktop.png') });
  await page.setViewportSize({ width: 390, height: 844 });
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
  await page.screenshot({ path: testInfo.outputPath('models-tabs-mobile.png') });
});
