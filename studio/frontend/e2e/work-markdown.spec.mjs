import { test, expect } from '@playwright/test';

const markdown = '# 읽기 확인\n\n**강조 문장**과 `inline code`\n\n- 첫 항목\n- 둘째 항목\n\n| 이름 | 상태 |\n| --- | --- |\n| Q | Ready |\n\n```go\nvar answer = 42\n```\n\n[Documentation](https://example.com/docs)\n\n<img src=x onerror="window.markdownExecuted=true">';

for (const kind of ['issue', 'ticket']) {
  test(`${kind} Markdown survives content paging, replies and raw-source editing`, async ({ page, request }) => {
    const project = (await (await request.get('/api/v1/projects')).json()).projects.find(p => p.name === 'Integration');
    const source = markdown + '\n\n' + '긴 본문을 페이지로 읽습니다.\n\n'.repeat(600) + '\n## 마지막 페이지\n\n끝까지 **유지**됩니다.';
    const response = await request.post('/api/v1/work/items', { data: {
      workspace_id: project.id, kind, title: `${kind} Markdown`, content: source,
      acceptance: kind === 'ticket' ? ['결과에 **근거**를 포함한다.'] : [], request_id: crypto.randomUUID()
    } });
    expect(response.status()).toBe(201);
    const item = await response.json();
    await page.goto(`/${kind === 'issue' ? 'issues' : 'tickets'}?workspace_id=${project.id}&item_id=${item.id}`);
    const body = page.locator('.post-content').first();
    await expect(body.locator('h1')).toHaveText('읽기 확인');
    await expect(body.locator('strong').first()).toHaveText('강조 문장');
    await expect(body.locator('table td').first()).toHaveText('Q');
    await expect(body.locator('pre code')).toHaveText('var answer = 42\n');
    await expect(body.locator('.code-copy')).toHaveCount(1);
    await expect(body.locator('img')).toHaveCount(0);
    await expect(body).toContainText('<img src=x');
    while (await page.getByRole('button', { name: 'Read more', exact: true }).isVisible()) {
      const more = page.getByRole('button', { name: 'Read more', exact: true });
      const before = await body.textContent();
      const loaded = page.waitForResponse(r => r.url().includes(`/work/posts/${item.body_post_id}?`));
      await more.click();
      await loaded;
      await expect.poll(() => body.textContent()).not.toBe(before);
    }
    await expect(body.locator('h2')).toHaveText('마지막 페이지');
    await expect(body.locator('.code-copy')).toHaveCount(1);
    await page.getByRole('button', { name: 'Edit body', exact: true }).click();
    const editor = page.getByRole('textbox', { name: 'Edit post content', exact: true });
    await expect(editor).toHaveValue(source);
    await editor.fill(source + '\n\n## 수정 내용\n\n원문 보존.');
    await page.getByRole('button', { name: 'Save post', exact: true }).click();
    await page.getByRole('textbox', { name: 'Work item reply' }).fill('## 답변\n\n**검증 완료**');
    await page.getByRole('button', { name: 'Add reply', exact: true }).click();
    await expect(page.locator('.post-content').last().locator('h2')).toHaveText('답변');
    await page.reload();
    await expect(page.locator('.post-content').last().locator('strong')).toHaveText('검증 완료');
    expect(await page.evaluate(() => window.markdownExecuted)).toBeUndefined();
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
  });
}

test('ticket reports and feedback render as Markdown while retaining raw result metadata', async ({ page, request }) => {
  const project = (await (await request.get('/api/v1/projects')).json()).projects.find(p => p.name === 'Integration');
  const response = await request.post('/api/v1/work/items', { data: {
    workspace_id: project.id, kind: 'ticket', title: 'Rendered report', content: 'Review the report.', request_id: crypto.randomUUID()
  } });
  const item = await response.json();
  const result = { outcome: 'succeeded', summary: '**Completed**', report: '# Report\n\nEvidence is available.', findings: ['A **verified** finding'], verification: ['`go test` passed'], artifacts: ['report.md'], evidence: [{ tool: 'read_file' }] };
  await page.route(`**/api/v1/work/items/${item.id}/attempts`, route => route.fulfill({ json: { attempts: [{ id: 'render-attempt', ticket_id: item.id, status: 'awaiting_review', worker_session_id: 'worker', result, feedback: '## Review\n\n**Accepted evidence**' }] } }));
  await page.route('**/api/v1/work/attempts/render-attempt/worker?*', route => route.fulfill({ json: { messages: [
    { index: 0, role: 'assistant', content: '## Worker report\n\n**Verified**' },
    { index: 1, role: 'tool', content: '**literal tool output**' }
  ] } }));
  await page.goto(`/tickets?workspace_id=${project.id}&item_id=${item.id}`);
  const attempt = page.locator('.attempt-result');
  const details = page.locator('.work-thread details').filter({ has: attempt });
  await details.locator(':scope > summary').click();
  await expect(attempt.locator('.markdown-body h1')).toHaveText('Report');
  await expect(page.getByRole('region', { name: 'Integrator feedback' }).locator('strong')).toHaveText('Accepted evidence');
  await attempt.getByText('Raw result', { exact: true }).click();
  expect(JSON.parse(await attempt.locator('pre').textContent())).toEqual(result);
  await page.getByRole('button', { name: 'Read worker conversation', exact: true }).click();
  await expect(page.getByRole('region', { name: 'Worker conversation' }).locator('h2')).toHaveText('Worker report');
  await expect(page.getByRole('region', { name: 'Worker conversation' }).locator('pre')).toHaveText('**literal tool output**');
  await page.waitForResponse(r => r.url().endsWith(`/work/items/${item.id}/attempts`));
  await expect(details).toHaveJSProperty('open', true);
});
