<script lang="ts">
  import { RefreshCw } from '@lucide/svelte';
  import { onMount } from 'svelte';
  import { requestJSON } from './api';
  import Dashboard from './operations/Dashboard.svelte';
  import type { Snapshot } from './operations/types';

  let days = 30;
  let loadedDays = 30;
  let model = '';
  let role = '';
  let models: string[] = [];
  let roles: string[] = [];
  let optionsDays: number | undefined;
  let snapshot: Snapshot | null = null;
  let loading = true;
  let error = '';
  let generation = 0;
  let controller: AbortController | undefined;

  function updateOptions(result: Snapshot, period: number) {
    models = [...new Set([
      ...(result.usage.models || []).map(entry => entry.name),
      ...(result.usage.performance || []).map(entry => entry.name),
      ...(model ? [model] : []),
    ])].sort();
    roles = [...new Set([
      ...(result.usage.roles || []).map(entry => entry.name),
      ...(role ? [role] : []),
    ])].sort();
    optionsDays = period;
  }

  async function load() {
    const requestedDays = days;
    const query = new URLSearchParams({ days: String(requestedDays) });
    const filtered = Boolean(model || role);
    if (model) query.set('model', model);
    if (role) query.set('role', role);
    const requestGeneration = ++generation;
    controller?.abort();
    const requestController = new AbortController();
    controller = requestController;
    loading = true;
    error = '';
    try {
      const result = await requestJSON<Snapshot>('GET', `/api/v1/operations?${query}`, undefined, requestController.signal);
      if (requestGeneration !== generation) return;
      snapshot = { ...result, services: result.services || [], logs: result.logs || [] };
      loadedDays = requestedDays;
      if (!filtered && !result.usage_error) {
        updateOptions(result, requestedDays);
      } else if (filtered && requestedDays !== optionsDays) {
        const choices = await requestJSON<Snapshot>('GET', `/api/v1/operations?days=${requestedDays}`, undefined, requestController.signal).catch(() => null);
        if (requestGeneration === generation && choices && !choices.usage_error) updateOptions(choices, requestedDays);
      }
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
    <label><span>Model</span><select bind:value={model} onchange={load}><option value="">All models</option>{#each models as option}<option value={option}>{option}</option>{/each}</select></label>
    <label><span>Role</span><select bind:value={role} onchange={load}><option value="">All roles</option>{#each roles as option}<option value={option}>{option}</option>{/each}</select></label>
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
  .operations-toolbar { display: flex; flex-wrap: wrap; align-items: center; justify-content: flex-end; gap: 9px 16px; }
  .operations-toolbar label { display: flex; align-items: center; gap: 9px; min-width: 0; max-width: 100%; color: var(--subtle); font-size: var(--type-caption); }
  .operations-toolbar label span { white-space: nowrap; }
  .operations-toolbar select { min-width: 130px; }
  .operations-toolbar label:nth-child(2) select { width: 16em; max-width: 65vw; text-overflow: ellipsis; }
</style>
