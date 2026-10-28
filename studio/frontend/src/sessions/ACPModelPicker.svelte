<script lang="ts">
  import { RefreshCw } from '@lucide/svelte';
  import { onMount, onDestroy } from 'svelte';
  import { requestJSON } from '../api';

  export let workspaceRoot: string;
  export let sessionID: string;
  export let disabled = false;
  export let saving = false;

  type State = { supported: boolean; current_model: string; models: { id: string; name: string; group?: string }[] };
  let state: State | null = null;
  let model = '';
  let error = '';
  let disposed = false;
  let mounted = false;
  const endpoint = `/api/v1/sessions/${encodeURIComponent(sessionID)}/acp/model`;
  $: if (mounted && !disabled && !state && !saving && !error) void load();

  async function load() {
    if (saving || disabled) return;
    saving = true;
    error = '';
    try {
      const result = await requestJSON<State>('POST', endpoint, { workspace_root: workspaceRoot });
      if (!disposed) { state = result; model = result.current_model; }
    } catch (cause) {
      if (!disposed) error = cause instanceof Error ? cause.message : 'Could not load agent models';
    } finally { if (!disposed) saving = false; }
  }

  async function save() {
    if (!state || saving || disabled) return;
    saving = true;
    error = '';
    try {
      const result = await requestJSON<State>('PUT', endpoint, { workspace_root: workspaceRoot, model });
      if (!disposed) state = result;
    } catch (cause) {
      if (!disposed) error = cause instanceof Error ? cause.message : 'Could not change the agent model';
    } finally {
      if (!disposed) { model = state.current_model; saving = false; }
    }
  }

  onMount(() => { mounted = true; });
  onDestroy(() => { disposed = true; });
</script>

<select aria-label="ACP model" title="Model offered by this ACP agent" bind:value={model} onchange={save} disabled={disabled || saving || !state?.supported}>
  {#if !state}<option value="">{saving ? 'Connecting to agent…' : 'Agent models unavailable'}</option>
  {:else if !state.supported}<option value={state.current_model}>{state.current_model || 'Agent default'} · model selection unavailable</option>
  {:else}
    {#if !state.models.some(option => option.id === model)}<option value={model}>{model || 'Agent default'}</option>{/if}
    {#each state.models as option}<option value={option.id}>{option.name}{option.group ? ` · ${option.group}` : ''}</option>{/each}
  {/if}
</select>
<button class="icon-button" title="Reload agent models" onclick={load} disabled={disabled || saving}><RefreshCw size={13} /></button>
{#if error}<span class="model-error" role="alert">{error}</span>{/if}

<style>
  select { width: auto; min-width: 0; max-width: 220px; padding: 5px 8px; border: 1px solid var(--border); border-radius: 6px; background: var(--surface-raised); color: var(--text); font: inherit; }
  button { width: 26px; height: 26px; }
  .model-error { color: var(--error); font-size: 12px; }
</style>
