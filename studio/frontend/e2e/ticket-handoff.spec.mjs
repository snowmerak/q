import { test, expect } from './test.mjs';

test('child tickets link to their reviewer and the parent can be handed off', async ({ page, request }) => {
  const project = (await (await request.get('/api/v1/projects')).json()).projects.find(value => value.name === 'Integration');
  const parent = { id: 'parent-fixture', number: 101, title: 'Parent ticket fixture', assigned_member_id: 'senior', execution_state: 'waiting_children' };
  const child = { id: 'child-fixture', number: 102, title: 'Child ticket fixture', parent_ticket_id: parent.id, assigned_member_id: 'junior', execution_state: 'awaiting_review' };
  for (const item of [parent, child]) {
    Object.assign(item, { workspace_id: project.id, kind: 'ticket', execution_mode: 'git', labels: [], state: 'in_progress', revision: 1 });
    await page.route(`**/api/v1/work/items/${item.id}`, route => route.fulfill({ json: item }));
    await page.route(`**/api/v1/work/items/${item.id}/posts?*`, route => route.fulfill({ json: { item, posts: [] } }));
    await page.route(`**/api/v1/work/items/${item.id}/attempts`, route => route.fulfill({ json: { attempts: [] } }));
    await page.route(`**/api/v1/work/items/${item.id}/children`, route => route.fulfill({ json: { items: item.id === parent.id ? [child] : [] } }));
  }
  await page.route('**/api/v1/work/items?*', route => route.fulfill({ json: { items: [parent, child] } }));
  await page.route(`**/api/v1/projects/${project.id}/members`, route => route.fulfill({ json: { members: [
    { id: 'senior', agent_id: 'builtin/senior-developer', execution_root: '/fixture' },
    { id: 'junior', agent_id: 'builtin/junior-developer', execution_root: '/fixture' },
    { id: 'replacement', agent_id: 'workspace/replacement', execution_root: '/fixture' }
  ] } }));
  await page.route(`**/api/v1/work/items/${parent.id}/control`, async route => {
    expect(route.request().postDataJSON()).toMatchObject({ action: 'handoff', revision: 1, member_id: 'replacement' });
    Object.assign(parent, { handoff_member_id: 'replacement', revision: 2 });
    await route.fulfill({ json: parent });
  });
  await page.goto(`/tickets?workspace_id=${project.id}&item_id=${parent.id}`);
  const thread = page.getByRole('region', { name: 'Work item thread' });
  const members = thread.getByLabel('Handoff to');
  await expect(members.getByRole('option', { name: 'builtin/junior-developer' })).toHaveCount(0);
  await members.selectOption('replacement');
  await thread.getByRole('button', { name: 'Hand off parent ticket' }).click();
  await expect(thread).toContainText('Handoff pending · workspace/replacement');
  await thread.getByRole('link', { name: /Child ticket fixture/ }).click();
  await expect(thread.getByRole('link', { name: /Parent ticket fixture/ })).toBeVisible();
  await expect(thread).toContainText('Reviewer · builtin/senior-developer');
});
