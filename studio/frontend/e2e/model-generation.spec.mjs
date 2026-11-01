import { test, expect } from '@playwright/test';

 test('model output limit and recovery settings persist, clear, and fit mobile', async ({ page, request }, testInfo) => {
  const errors=[];
  page.on('pageerror', error=>errors.push(error.message));
  const catalog=await (await request.get('/api/v1/settings/models')).json();
  const model=catalog.models.find(model=>!model.group && !model.output_limit_unsupported);
  const snapshot=await (await request.get('/api/v1/settings')).json();
  const current=async()=> (await (await request.get('/api/v1/settings/models')).json()).models.find(option=>option.id===model.id);
  try {
   await page.goto('/settings?section=models&model_tab=behavior');
   await expect(page).toHaveTitle(/Q Studio/);
   const limit=page.getByRole('spinbutton',{name:`Maximum output tokens for ${model.id}`,exact:true});
   await expect(limit).toBeVisible();
   await limit.fill('128');await limit.press('Tab');
   await expect.poll(async()=> (await current()).output_limit).toBe(128);
   await page.getByRole('combobox',{name:`Chat token parameter for ${model.id}`,exact:true}).selectOption('max_tokens');
   await expect.poll(async()=> (await current()).output_token_field).toBe('max_tokens');
   const compatibility=page.getByRole('checkbox',{name:`Allow missing finish reason for ${model.id}`,exact:true});
   await compatibility.check();
   await expect.poll(async()=> (await current()).allow_missing_finish_reason).toBe(true);
   const retries=page.getByRole('spinbutton',{name:'Additional recovery attempts',exact:true});
   await retries.fill('0');await retries.press('Tab');
   await expect.poll(async()=> (await (await request.get('/api/v1/settings')).json()).models.recovery_attempts).toBe(0);
   await page.reload();
   await expect(limit).toHaveValue('128');await expect(compatibility).toBeChecked();await expect(retries).toHaveValue('0');
   await page.screenshot({path:testInfo.outputPath('output-limits-desktop.png')});
   await page.setViewportSize({width:390,height:844});
   await limit.scrollIntoViewIfNeeded();
   expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
   await page.screenshot({path:testInfo.outputPath('output-limits-mobile.png')});
   await limit.fill('');await limit.press('Tab');
   await expect.poll(async()=> (await current()).output_limit).toBeUndefined();
   await retries.fill('');await retries.press('Tab');
   await expect.poll(async()=> (await (await request.get('/api/v1/settings')).json()).models.recovery_attempts).toBe(2);
   expect(errors).toEqual([]);
  } finally {
   await request.put('/api/v1/settings/model-output-limit',{data:{model:model.id,output_limit:model.output_limit??null,output_token_field:model.output_token_field||'',allow_missing_finish_reason:model.allow_missing_finish_reason??false}});
   await request.put('/api/v1/settings/generation-recovery',{data:snapshot.models.generation_recovery??{}});
  }
 });

 test('cutoff text recovers in one turn and replays after reload', async ({ page, request }, testInfo) => {
  const errors=[];page.on('pageerror',error=>errors.push(error.message));
  const catalog=await (await request.get('/api/v1/settings/models')).json();
  const model=catalog.models.find(model=>model.id==='default/test-model');
  const saved=await (await request.get('/api/v1/settings')).json();
  const chat=await (await request.post('/api/v1/chats',{data:{}})).json();
  try {
   expect((await request.put('/api/v1/settings/model-output-limit',{data:{model:model.id,output_limit:128}})).ok()).toBe(true);
   expect((await request.put('/api/v1/settings/generation-recovery',{data:{max_attempts:2}})).ok()).toBe(true);
   await page.goto(`/sessions/${chat.registration_id}`);
   const composer=page.getByRole('textbox',{name:'Message Q',exact:true});await expect(composer).toBeEnabled();
   await composer.fill('browser output recovery');await composer.press('Enter');
   await expect(page.locator('.chat-heading')).toContainText('Completed');
   const answer=page.locator('.transcript').getByText('The answer is 42.',{exact:true});
   await expect(answer).toHaveCount(1);
   await page.reload();await expect(answer).toHaveCount(1);
   await page.screenshot({path:testInfo.outputPath('generation-recovery-replay.png')});
   expect(errors).toEqual([]);
  } finally {
   await request.delete(`/api/v1/chats/${chat.registration_id}`);
   await request.put('/api/v1/settings/model-output-limit',{data:{model:model.id,output_limit:model.output_limit??null,output_token_field:model.output_token_field||'',allow_missing_finish_reason:model.allow_missing_finish_reason??false}});
   await request.put('/api/v1/settings/generation-recovery',{data:saved.models.generation_recovery??{}});
  }
 });
