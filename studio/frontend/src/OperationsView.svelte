<script lang="ts">
  import { RefreshCw } from '@lucide/svelte';
  import { onMount } from 'svelte';
  import { requestJSON } from './api';
  import Dashboard from './operations/Dashboard.svelte';
  import type { Snapshot } from './operations/types';

  let days = 30;
  let loadedDays = 30;
  let snapshot: Snapshot | null = null;
  let loading = true;
  let error = '';
  let generation = 0;
  let controller: AbortController | undefined;

  async function load() {
    const requestedDays = days;
    const requestGeneration = ++generation;
    controller?.abort();
    const requestController = new AbortController();
    controller = requestController;
    loading = true;
    error = '';
    try {
      const result = await requestJSON<Snapshot>('GET', `/api/v1/operations?days=${requestedDays}`, undefined, requestController.signal);
      if (requestGeneration !== generation) return;
      snapshot = { ...result, services: result.services || [], logs: result.logs || [] };
      loadedDays = requestedDays;
    } catch (reason) {
      if (!requestController.signal.aborted && requestGeneration === generation) error = reason instanceof Error ? reason.message : 'Operations are unavailable';
    } finally {
      if (requestGeneration === generation) loading = false;
    }
  }

  onMount(() => {
    void load();
    // Avoid replacing a slow request every time the refresh interval elapses.
    const timer = setInterval(() => { if (!loading) void load(); }, 15000);
    return () => {
      clearInterval(timer);
      generation += 1;
      controller?.abort();
    };
  });
</script>

<section class="operations-view">
  <div class="operations-toolbar">
    <label><span>Usage period</span><select bind:value={days} onchange={load}><option value={1}>24 hours</option><option value={7}>7 days</option><option value={30}>30 days</option><option value={90}>90 days</option></select></label>
    <button class="icon-button" title="Refresh operations" onclick={load} disabled={loading}><RefreshCw aria-hidden="true" size={16} class={loading ? 'spin' : undefined} /></button>
  </div>

  {#if error}
    <div class="error-panel"><h2>Operations unavailable</h2><p>{error}</p><button class="retry" onclick={load}>Retry</button></div>
  {:else if !snapshot}
    <div class="loading-panel">Loading runtime operations…</div>
  {:else}
    <Dashboard {snapshot} days={loadedDays} />
  {/if}
</section>

<style>
  .operations-view { display: grid; gap: 18px; min-height: 0; padding: 0 0 26px; }
  .operations-toolbar { display: flex; justify-content: flex-end; gap: 9px; }
  .operations-toolbar label { display: flex; align-items: center; gap: 9px; color: var(--subtle); font-size: calc(10px * var(--text-scale)); }
  .operations-toolbar select { min-width: 130px; }
</style>
