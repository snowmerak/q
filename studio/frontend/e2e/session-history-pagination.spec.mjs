import { test, expect } from '@playwright/test';

test('completed sessions open at the latest history and prepend earlier turns without moving the viewport', async ({ page, request }, testInfo) => {
  const chat = await (await request.post('/api/v1/chats', { data: {} })).json();
  const base = `/api/v1/sessions/${chat.session.session_id}`;
  const history = Array.from({ length: 120 }, (_, turn) => [
    { role: 'user', content: `Request ${turn}` },
    { role: 'assistant', tool_calls: [{ id: `tool-${turn}`, function: { name: 'read_file', arguments: '{}' } }] },
    { role: 'tool', tool_call_id: `tool-${turn}`, name: 'read_file', content: `Result ${turn}` },
    { role: 'assistant', content: `Answer ${turn}\n\n${'History paragraph. '.repeat(40)}` }
  ]).flat();
  const cursors = [];
  let eventRequests = 0;
  const errors = [];
  page.on('pageerror', error => errors.push(error.message));
  await page.route(`**${base}?*`, async route => {
    const url = new URL(route.request().url());
    const end = Number(url.searchParams.get('before') ?? history.length);
    const start = Math.max(0, end - Number(url.searchParams.get('limit') ?? history.length));
    cursors.push({ start, end });
    await route.fulfill({ json: { workspace_root: chat.workspace_root, session: chat.session,
      transcript: history.slice(start, end), history: { start, end, total: history.length },
      transcript_thoughts: [{ before: 3, content: `Thought for ${start / 4}` }] } });
  });
  await page.route(`**${base}/runs/latest?*`, route => route.fulfill({ json: { id: 'completed-history', session_id: chat.session.session_id, status: 'completed', cursor: 285 } }));
  await page.route(`**${base}/runs/completed-history/events?*`, route => {
    eventRequests++;
    return route.fulfill({ json: {
      run: { id: 'completed-history', status: 'completed', cursor: 1 },
      events: [{ cursor: 1, at: new Date().toISOString(), event: { type: 'result', content: 'Answer 120' } }], next_cursor: 1
    } });
  });
  await page.route(`**${base}/messages`, route => {
    history.push({ role: 'user', content: 'Request 120' }, { role: 'assistant', content: 'Answer 120' });
    return route.fulfill({ json: { id: 'completed-history', status: 'completed', cursor: 1 } });
  });
  try {
    await page.goto(`/sessions/${chat.registration_id}`);
    const transcript = page.locator('.transcript');
    await expect(transcript).toContainText('Answer 119');
    await expect(transcript).not.toContainText('Request 0');
    expect(cursors[0]).toEqual({ start: 400, end: 480 });
    await expect(page.getByRole('textbox', { name: 'Message Q', exact: true })).toBeEnabled();
    expect(eventRequests).toBe(0);
    const anchor = transcript.locator('.chat-message').filter({ hasText: 'Request 100' });
    await transcript.evaluate(element => { element.scrollTop = 120; });
    const previousY = (await anchor.boundingBox()).y;
    // Keep this button outside the viewport to exercise prepend anchoring without
    // Playwright's click scrolling changing the initial position.
    await transcript.getByRole('button', { name: 'Load earlier messages', exact: true }).evaluate(element => element.click());
    await expect(transcript).toContainText('Request 80');
    await expect.poll(async () => Math.abs((await anchor.boundingBox()).y - previousY)).toBeLessThanOrEqual(2);
    expect(cursors[1]).toEqual({ start: 320, end: 400 });
    await transcript.evaluate(element => { element.scrollTop = 0; });
    await expect(transcript).toContainText('Request 60');
    expect(cursors[2]).toEqual({ start: 240, end: 320 });
    await page.screenshot({ path: testInfo.outputPath('history-desktop.png') });
    await page.setViewportSize({ width: 390, height: 844 });
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
    await page.screenshot({ path: testInfo.outputPath('history-mobile.png') });
    const composer = page.getByRole('textbox', { name: 'Message Q', exact: true });
    await composer.fill('Request 120');
    await composer.press('Enter');
    await expect(transcript).toContainText('Answer 120');
    await expect.poll(() => cursors.some(cursor => cursor.end === history.length)).toBe(true);
    // Refreshing the tail on completion must retain pages the user loaded.
    await expect(transcript).toContainText('Request 60');
    expect(eventRequests).toBe(1);
    await page.reload();
    await expect(transcript).toContainText('Answer 120');
    await expect(transcript).not.toContainText('Request 60');
    expect(eventRequests).toBe(1);
    expect(errors).toEqual([]);
  } finally {
    await request.delete(`/api/v1/chats/${chat.registration_id}`);
  }
});

test('a running session drains all terminal event pages, including a tool result on the next page', async ({ page, request }) => {
  const chat = await (await request.post('/api/v1/chats', { data: {} })).json();
  const base = `/api/v1/sessions/${chat.session.session_id}`;
  const run = { id: 'paged-run', session_id: chat.session.session_id, status: 'running', cursor: 285 };
  const cursors = [];
  let completed = false;
  const events = [
    ...Array.from({ length: 254 }, (_, index) => ({ type: 'stream', kind: 'thinking', start: index === 0, content: 'Progress. ' })),
    { type: 'message', role: 'assistant', content: 'Checking the file.' },
    { type: 'tool_call', call_id: 'paired-tool', name: 'read_file', content: '{}' },
    { type: 'message', role: 'tool', call_id: 'paired-tool', name: 'read_file', content: 'The paired result is present.' },
    ...Array.from({ length: 27 }, () => ({ type: 'stream', kind: 'thinking', content: 'Finishing. ' })),
    { type: 'result', outcome: 'succeeded', content: 'The complete final answer.' }
  ];
  await page.route(`**${base}?*`, route => route.fulfill({ json: {
    workspace_root: chat.workspace_root, session: chat.session,
    transcript: [{ role: 'user', content: 'Read the file.' }, ...(completed ? [{ role: 'assistant', content: 'The complete final answer.' }] : [])]
  } }));
  await page.route(`**${base}/runs/latest?*`, route => route.fulfill({ json: run }));
  await page.route(`**${base}/runs/paged-run/events?*`, route => {
    const after = Number(new URL(route.request().url()).searchParams.get('after'));
    cursors.push(after);
    const batch = events.slice(after, after + 256).map((event, index) => ({ cursor: after + index + 1, at: new Date().toISOString(), event }));
    completed = after > 0;
    return route.fulfill({ json: { run: { ...run, status: 'completed', outcome: 'succeeded' }, events: batch, next_cursor: after + batch.length } });
  });
  try {
    await page.goto(`/sessions/${chat.registration_id}`);
    const transcript = page.locator('.transcript');
    await expect(transcript).toContainText('The complete final answer.');
    await expect(transcript.locator('.tool-card')).toHaveCount(1);
    await expect(transcript.locator('.transcript-tool-result')).toContainText('The paired result is present.');
    expect(cursors).toEqual([0, 256]);
  } finally {
    await request.delete(`/api/v1/chats/${chat.registration_id}`);
  }
});
