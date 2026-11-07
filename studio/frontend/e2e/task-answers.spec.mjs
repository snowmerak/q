import { test, expect } from '@playwright/test';

for (const placement of ['before', 'alongside', 'after']) {
  test(`task answer ${placement} completion stays visible once after reconnect`, async ({ page, request }, testInfo) => {
    const errors = [];
    page.on('pageerror', error => errors.push(error.message));
    const chat = await (await request.post('/api/v1/chats', { data: {} })).json();
    try {
      await page.goto(`/sessions/${chat.registration_id}`);
      const composer = page.getByRole('textbox', { name: 'Message Q', exact: true });
      await composer.fill(`task answer ${placement}`);
      await page.getByRole('button', { name: 'Send message', exact: true }).click();
      const transcript = page.locator('.transcript');
      const answer = transcript.getByRole('heading', { name: 'Research results', exact: true });
      await expect(answer).toHaveCount(1);
      await expect(transcript).toContainText('The verified answer is 42.');
      await expect(composer).toBeEnabled();
      await expect.poll(async () => {
        const url = `/api/v1/sessions/${chat.session.session_id}/runs/latest?workspace_root=${encodeURIComponent(chat.workspace_root)}`;
        const run = await (await request.get(url)).json();
        return run.status;
      }).toBe('completed');
      await expect(answer).toHaveCount(1);
      await expect(transcript).not.toContainText('Internal completion acknowledgment.');
      await expect(transcript).not.toContainText('still needs task_complete');
      await page.reload();
      await expect(answer).toHaveCount(1);
      await expect(transcript).toContainText('The verified answer is 42.');
      await expect(transcript.getByRole('link', { name: 'https://example.com/evidence', exact: true })).toBeVisible();
      await page.setViewportSize({ width: 390, height: 844 });
      expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
      await page.screenshot({ path: testInfo.outputPath(`task-answer-${placement}-mobile.png`) });
      expect(errors).toEqual([]);
    } finally {
      await request.delete(`/api/v1/chats/${chat.registration_id}`);
    }
  });
}
