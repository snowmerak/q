<script lang="ts">
  import { Download, Upload } from '@lucide/svelte';
  import TransferDialog from './TransferDialog.svelte';
  import type { SettingsBundle } from './transfer';

  export let busy = false;
  export let onimport: (bundle: SettingsBundle) => Promise<string | undefined>;
  let mode: 'import' | 'export' | null = null;
  let message = '';
</script>

<section class="settings-section">
  <div class="section-heading"><div><p class="eyebrow">IMPORT / EXPORT</p><h2>Move your settings</h2><p>Choose settings across tabs and move them together in one JSON file.</p></div></div>
  <div class="transfer-cards">
    <article class="settings-card">
      <Download aria-hidden="true" size={23} />
      <h3>Export settings</h3><p>Select the models, providers, runtime policies, services, subagents, and integrations to include.</p>
      <button class="primary-button" onclick={() => { message = ''; mode = 'export'; }} disabled={busy}>Choose settings to export</button>
    </article>
    <article class="settings-card">
      <Upload aria-hidden="true" size={23} />
      <h3>Import settings</h3><p>Open a Q settings file, choose its items, and review changes before applying them.</p>
      <button class="secondary-button" onclick={() => { message = ''; mode = 'import'; }} disabled={busy}>Choose settings to import</button>
    </article>
  </div>
  <p class="transfer-note">Global settings only. API keys, credential maps, and ACP environment values stay on this machine. Workspace overrides, skills, and ignore rules stay with their repositories.</p>
  {#if message}<p class="transfer-success" role="status">{message}</p>{/if}
</section>

{#if mode}
  <TransferDialog {mode} {onimport} onclose={() => mode = null} oncomplete={(result) => { message = result; mode = null; }} />
{/if}

<style>
  .transfer-cards { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 16px; }
  .transfer-cards article { display: flex; min-height: 228px; align-items: flex-start; flex-direction: column; gap: 14px; padding: 22px; }
  .transfer-cards :global(svg) { color: var(--violet); }
  .transfer-cards h3 { margin: 0; font-size: 16px; }
  .transfer-cards p { margin: 0; }
  .transfer-cards p, .transfer-note { color: var(--muted); font-size: 13px; line-height: 1.7; }
  .transfer-cards button { margin-top: auto; }
  .transfer-note { max-width: 820px; margin-top: 20px; }
  .transfer-success { color: #b8d8ba; font-size: 13px; }
  @media (max-width: 700px) { .transfer-cards { grid-template-columns: 1fr; } }
</style>
