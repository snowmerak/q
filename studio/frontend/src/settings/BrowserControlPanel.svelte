<script lang="ts">
  import { RefreshCw } from '@lucide/svelte';
  import type { SettingsSnapshot } from './types';
  import type { QueueSave } from './persistence';
  import { putSettings } from './persistence';

  export let settings: SettingsSnapshot;
  export let queueSave: QueueSave;
  let error = '';
  const names: Record<string, string> = { 'cdp-q-driver': 'CDP Q Driver · built into Q', 'ego-browser': 'Ego Browser', 'playwright-cli': 'Playwright CLI' };

  function save() {
    error = '';
    const payload = JSON.stringify(settings.browser_control.config);
    queueSave(() => putSettings('/api/v1/settings/browser-control', payload));
  }
  function saveArgs(id: string, text: string) {
    try {
      const args = JSON.parse(text);
      if (!Array.isArray(args) || !args.every(arg => typeof arg === 'string')) throw new Error('Arguments must be a JSON array of strings.');
      settings.browser_control.config.drivers[id].args = args;
      save();
    } catch (reason) { error = reason instanceof Error ? reason.message : 'Invalid arguments'; }
  }
</script>

<section class="settings-section">
  <div class="section-heading"><div><p class="eyebrow">GLOBAL TOOLS</p><h2>Browser control</h2><p>Tell Q which browser CLI you prefer. Q finds relevant skills when needed.</p></div></div>
  <div class="settings-card">
    <label class="field-row"><span><strong>Use browser CLI preference</strong><small>Applies on the next turn, including existing conversations.</small></span><input aria-label="Use browser CLI preference" type="checkbox" bind:checked={settings.browser_control.config.enabled} onchange={save} /></label>
    <label class="field-row"><span><strong>Preferred CLI</strong></span><select aria-label="Preferred browser CLI" bind:value={settings.browser_control.config.preferred} onchange={save}>{#each settings.browser_control.drivers as driver}<option value={driver.id}>{names[driver.id] || driver.id}</option>{/each}</select></label>
  </div>
  {#each settings.browser_control.drivers as driver (driver.id)}
    <div class="settings-card">
      <div class="card-heading"><div><h3>{names[driver.id] || driver.id}</h3><p>{driver.available ? 'Executable found' : 'Executable not found'}</p></div><button class="icon-button" title="Check browser CLI paths" aria-label={`Check ${driver.id} executable`} onclick={save}><RefreshCw size={16} /></button></div>
      <div class="field-grid">
        <label><span>Executable</span><input aria-label={`${driver.id} executable`} bind:value={settings.browser_control.config.drivers[driver.id].command} onchange={save} placeholder={driver.id === 'cdp-q-driver' ? 'Built in · current Q executable' : driver.id} /><small>A name uses PATH. A full path selects that file. Enter the executable only, without shell quotes.</small></label>
        <label><span>Arguments</span><input aria-label={`${driver.id} arguments`} value={JSON.stringify(settings.browser_control.config.drivers[driver.id].args || [])} onchange={event => saveArgs(driver.id, event.currentTarget.value)} spellcheck="false" /><small>JSON array, for example ["cdp"].</small></label>
      </div>
      {#if driver.resolved_command}<code class="resolved-path">{driver.resolved_command}</code>{/if}
    </div>
  {/each}
  {#if error}<p class="inline-warning" role="alert">{error}</p>{/if}
  <div class="settings-card"><div class="card-heading"><div><h3>Prompt preview</h3><p>No fixed skill binding is required. Executable checks do not start a browser.</p></div></div><pre>{settings.browser_control.prompt || 'Browser CLI preference is disabled.'}</pre></div>
</section>

<style>
  .field-grid { display: grid; grid-template-columns: minmax(0, 2fr) minmax(0, 1fr); gap: 16px; }
  .field-grid label { display: flex; flex-direction: column; gap: 8px; min-width: 0; }
  input:not([type="checkbox"]) { width: 100%; min-width: 0; }
  .field-row > span { flex-shrink: 0; }
  select { width: min(100%, 360px); min-width: 0; }
  small { color: var(--muted); }
  pre { white-space: pre-wrap; overflow-wrap: anywhere; font: inherit; font-size: .85rem; margin: 0; padding: 17px 20px; }
  .resolved-path { display: block; overflow-wrap: anywhere; margin: 0 20px 18px; color: var(--muted); }
  @media(max-width: 720px) { .field-grid { grid-template-columns: minmax(0, 1fr); } }
</style>
