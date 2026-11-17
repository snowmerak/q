import { test as base, expect } from '@playwright/test';

// Preferences now belong to the shared Studio profile. Give each test the
// empty state previously supplied by its isolated browser storage context.
export const test = base.extend({
  uiProfile: [async ({ request }, use) => {
    const response = await request.post('/_test/reset-ui-state');
    expect(response.ok()).toBeTruthy();
    await use();
  }, { auto: true }]
});
export { expect };
