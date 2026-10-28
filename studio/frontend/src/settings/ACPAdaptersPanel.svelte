<script lang="ts">
  import { Download, RefreshCw } from '@lucide/svelte';
  import { onMount, onDestroy } from 'svelte';
  import { requestJSON } from '../api';

  type Adapter = { preset: string; name: string; package?: string; installed: boolean; can_install: boolean; version?: string; detail: string };
  let adapters: Adapter[] = [];
  let loading = true;
  let installing = '';
  let error = '';
  let disposed = false;
  const endpoint = '/api/v1/settings/acp/adapters';

  async function load() {
    loading = true;
    error = '';
    try {
      const result = await requestJSON<{ adapters: Adapter[] }>('GET', endpoint);
      if (!disposed) adapters = result.adapters;
    } catch (cause) {
      if (!disposed) error = cause instanceof Error ? cause.message : 'Could not check ACP runtimes';
    } finally { if (!disposed) loading = false; }
  }

  async function install(preset: string) {
    installing = preset;
    error = '';
    try {
      const result = await requestJSON<Adapter>('POST', endpoint, { preset });
      if (!disposed) adapters = adapters.map(adapter => adapter.preset === preset ? result : adapter);
    } catch (cause) {
      if (!disposed) error = cause instanceof Error ? cause.message : 'Could not install the ACP adapter';
    } finally { if (!disposed) installing = ''; }
  }

  onMount(() => { void load(); });
  onDestroy(() => { disposed = true; });
</script>

<div class="subsection-heading"><div><h3>ACP runtimes</h3><p>Grok includes ACP. Codex and Claude Code adapters install automatically on first use when npm is available. You can also install them ahead of time.</p></div><button class="icon-button" title="Check ACP runtimes" onclick={load} disabled={loading || !!installing}><RefreshCw size={16} /></button></div>
{#if error}<p class="inline-warning" role="alert">{error}</p>{/if}
{#if loading && !adapters.length}<p>Checking installed runtimes…</p>{/if}
<div class="adapter-grid">
  {#each adapters as adapter}
    <article class="settings-card adapter-card" aria-label={`${adapter.name} ACP runtime`}>
      <div><h4>{adapter.name}</h4><p class:ready={adapter.installed} role="status">{adapter.detail}{adapter.version ? ` · ${adapter.version}` : ''}</p></div>
      {#if adapter.package && !adapter.installed}<button class="text-button" onclick={() => install(adapter.preset)} disabled={!adapter.can_install || !!installing || loading}><Download size={14} />{installing === adapter.preset ? 'Installing…' : 'Install adapter'}</button>{/if}
    </article>
  {/each}
</div>
<p class="context-note">Adapters are installed for Q. Sign in through the agent's own CLI before starting a chat.</p>

<style>
  .adapter-grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(min(220px, 100%), 1fr)); gap: 12px; }
  .adapter-card { display: flex; flex-direction: column; justify-content: space-between; gap: 12px; min-width: 0; margin: 0; padding: 16px; }
  h4 { margin: 0 0 8px; }
  p { margin: 0; color: var(--muted); font-size: 12px; line-height: 1.5; }
  .ready { color: var(--success-text); }
  button { align-self: start; }
  .context-note { margin: 12px 0 24px; }
</style>
