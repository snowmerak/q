<script lang="ts">
  import { RefreshCw } from '@lucide/svelte';
  import { formatBytes } from './format';
  import type { LoomStats, LoomGCResult } from './types';
  import { apiError as responseError } from '../api';
  import { putSettings } from './persistence';
  import type { QueueSave } from './persistence';
  import type { SettingsSnapshot } from './types';

  export let active = false;
  export let settings: SettingsSnapshot;
  export let queueSave: QueueSave;

  let loomStats: LoomStats | null = null;
  let loomResult: LoomGCResult | null = null;
  let loomLoading = false;
  let loomError = '';
  let loomRoot = '';
  let loomGeneration = 0;
  $: if (active) void loadLoomStatus();

  function saveRuntime() {
    if (!settings) return;
    const payload = JSON.stringify({
      max_parallel: settings.runtime.max_parallel,
      context: settings.runtime.context,
      loom: settings.runtime.loom
    });
    queueSave(() => putSettings('/api/v1/settings/runtime', payload));
  }

  async function loadLoomStatus() {
    const generation = ++loomGeneration;
    const root = localStorage.getItem('q-studio-workspace-root') || '';
    loomError = '';
    loomResult = null;
    if (!root) {
      loomStats = null;
      loomRoot = '';
      loomLoading = false;
      loomError = 'Open a repository in Sessions to inspect its Loom store.';
      return;
    }
    loomLoading = true;
    try {
      const response = await fetch(`/api/v1/workspaces/loom?workspace_root=${encodeURIComponent(root)}`);
      if (!response.ok) throw new Error(await responseError(response));
      const result = (await response.json()) as { stats: LoomStats };
      if (generation !== loomGeneration) return;
      loomStats = result.stats;
      loomRoot = root;
    } catch (error) {
      if (generation === loomGeneration) loomError = error instanceof Error ? error.message : 'Could not inspect Loom storage';
    } finally {
      if (generation === loomGeneration) loomLoading = false;
    }
  }

  async function collectLoom(dryRun: boolean) {
    const root = loomRoot;
    if (!root || loomLoading) return;
    if (!dryRun && !window.confirm('Run Loom garbage collection for the current repository?')) return;
    const generation = ++loomGeneration;
    loomLoading = true;
    loomError = '';
    try {
      const response = await fetch('/api/v1/workspaces/loom/collect', {
        method: 'POST', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ workspace_root: root, dry_run: dryRun })
      });
      if (!response.ok) throw new Error(await responseError(response));
      const result = (await response.json()) as { stats: LoomStats; result: LoomGCResult };
      if (generation !== loomGeneration) return;
      loomStats = result.stats;
      loomResult = result.result;
    } catch (error) {
      if (generation === loomGeneration) loomError = error instanceof Error ? error.message : 'Loom garbage collection failed';
    } finally {
      if (generation === loomGeneration) loomLoading = false;
    }
  }
</script>

<section class="settings-section">
  <div class="section-heading"><div><p class="eyebrow">GLOBAL RUNTIME</p><h2>Execution and storage</h2></div><code>{settings.runtime.config_path}</code></div>
  {#if !settings.runtime.configured}<p class="inline-warning">Choose a Gateway provider and default model in Studio before editing runtime settings.</p>{/if}
  <div class="settings-card"><div class="card-heading"><div><h3>Agent execution</h3><p>Limit concurrent delegated agent work.</p></div></div><label class="field-row"><span><strong>Maximum parallel agents</strong><small>Applies across built-in roles.</small></span><input type="number" min="1" max="64" bind:value={settings.runtime.max_parallel} onchange={saveRuntime} disabled={!settings.runtime.configured} /></label></div>
  <div class="settings-card"><div class="card-heading"><div><h3>Context compaction</h3><p>Control when and how Q compacts long conversations.</p></div></div><div class="field-grid"><label><span>Context window</span><input type="number" min="0" step="1000" bind:value={settings.runtime.context.window} onchange={saveRuntime} disabled={!settings.runtime.configured} /></label><label><span>Trigger ratio</span><input type="number" min="0.01" max="0.99" step="0.01" bind:value={settings.runtime.context.trigger_ratio} onchange={saveRuntime} disabled={!settings.runtime.configured} /></label><label><span>Target ratio</span><input type="number" min="0.01" max="0.98" step="0.01" bind:value={settings.runtime.context.target_ratio} onchange={saveRuntime} disabled={!settings.runtime.configured} /></label><label><span>Recent ratio</span><input type="number" min="0.01" max="0.97" step="0.01" bind:value={settings.runtime.context.recent_ratio} onchange={saveRuntime} disabled={!settings.runtime.configured} /></label></div></div>
  <div class="settings-card">
    <div class="card-heading"><div><h3>Loom storage</h3><p>Bound artifact storage and garbage collection.</p></div><button class="icon-button" title="Refresh Loom stats" onclick={loadLoomStatus} disabled={loomLoading}><RefreshCw aria-hidden="true" size={15} class={loomLoading ? 'spin' : undefined} /></button></div>
    <div class="field-grid"><label><span>Maximum artifact MiB</span><input type="number" min="1" max="1024" bind:value={settings.runtime.loom.maximum_artifact_mib} onchange={saveRuntime} disabled={!settings.runtime.configured} /></label><label><span>Maximum store MiB</span><input type="number" min="1" max="10240" bind:value={settings.runtime.loom.maximum_store_mib} onchange={saveRuntime} disabled={!settings.runtime.configured} /></label><label><span>GC trigger ratio</span><input type="number" min="0.02" max="0.99" step="0.01" bind:value={settings.runtime.loom.gc_trigger_ratio} onchange={saveRuntime} disabled={!settings.runtime.configured} /></label><label><span>GC target ratio</span><input type="number" min="0.01" max="0.98" step="0.01" bind:value={settings.runtime.loom.gc_target_ratio} onchange={saveRuntime} disabled={!settings.runtime.configured} /></label><label><span>GC grace hours</span><input type="number" min="1" max="8760" bind:value={settings.runtime.loom.gc_grace_hours} onchange={saveRuntime} disabled={!settings.runtime.configured} /></label><label class="toggle-field"><span>Garbage collection</span><input type="checkbox" checked={!settings.runtime.loom.gc_disabled} onchange={(event) => { settings!.runtime.loom.gc_disabled = !event.currentTarget.checked; saveRuntime(); }} disabled={!settings.runtime.configured} /><small>{settings.runtime.loom.gc_disabled ? 'Disabled' : 'Enabled'}</small></label></div>
    <div class="loom-operations">
      {#if loomStats}<div class="loom-stats"><span><strong>{loomStats.artifacts}</strong> artifacts</span><span><strong>{loomStats.blobs}</strong> blobs</span><span><strong>{formatBytes(loomStats.bytes)}</strong> stored</span></div>{/if}
      {#if loomError}<p class="inline-warning">{loomError}</p>{/if}
      {#if loomResult}<p class="loom-result">{loomResult.dry_run ? 'Preview' : 'Collected'} · {loomResult.artifacts_removed} artifacts · {loomResult.blobs_removed} blobs · {formatBytes(loomResult.bytes_reclaimed)}</p>{/if}
      <div><button class="text-button" onclick={() => collectLoom(true)} disabled={loomLoading || !loomStats}>Preview GC</button><button class="primary-button" onclick={() => collectLoom(false)} disabled={loomLoading || !loomStats}>Run GC</button></div>
    </div>
  </div>
</section>
