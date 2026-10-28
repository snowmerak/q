<script lang="ts">
  import { RefreshCw } from '@lucide/svelte';
  import { onMount, onDestroy } from 'svelte';
  import { requestJSON } from '../api';

  export let workspaceRoot: string;
  export let sessionID: string;
  export let disabled = false;
  export let saving = false;

  type Option = { id: string; name: string; group?: string };
  type State = { supported: boolean; current_model: string; models: Option[]; reasoning_supported: boolean; current_reasoning_effort: string; reasoning_efforts: Option[] };
  let state: State | null = null;
  let model = '';
  let effort = '';
  let error = '';
  let disposed = false;
  let mounted = false;
  const endpoint = `/api/v1/sessions/${encodeURIComponent(sessionID)}/acp/model`;
  $: if (mounted && !disabled && !state && !saving && !error) void load();

  function adopt(result: State) {
    state = result;
    model = result.current_model;
    effort = result.current_reasoning_effort;
  }

  async function load() {
    if (saving || disabled) return;
    saving = true;
    error = '';
    try {
      const result = await requestJSON<State>('POST', endpoint, { workspace_root: workspaceRoot });
      if (!disposed) adopt(result);
    } catch (cause) {
      if (!disposed) error = cause instanceof Error ? cause.message : 'Could not load agent models';
    } finally { if (!disposed) saving = false; }
  }

  async function save(field: 'model' | 'reasoning_effort') {
    if (!state || saving || disabled) return;
    saving = true;
    error = '';
    try {
      const result = await requestJSON<State>('PUT', endpoint, { workspace_root: workspaceRoot, [field]: field === 'model' ? model : effort });
      if (!disposed) adopt(result);
    } catch (cause) {
      if (!disposed) error = cause instanceof Error ? cause.message : 'Could not change the agent model';
    } finally {
      if (!disposed) { adopt(state); saving = false; }
    }
  }

  onMount(() => { mounted = true; });
  onDestroy(() => { disposed = true; });
</script>

<select aria-label="ACP model" title="Model offered by this ACP agent" bind:value={model} onchange={() => save('model')} disabled={disabled || saving || !state?.supported}>
  {#if !state}<option value="">{saving ? 'Connecting to agent…' : 'Agent models unavailable'}</option>
  {:else if !state.supported}<option value={state.current_model}>{state.current_model || 'Agent default'} · model selection unavailable</option>
  {:else}
    {#if !state.models.some(option => option.id === model)}<option value={model}>{model || 'Agent default'}</option>{/if}
    {#each state.models as option}<option value={option.id}>{option.name}{option.group ? ` · ${option.group}` : ''}</option>{/each}
  {/if}
</select>
<select class="effort-select" aria-label="Reasoning effort" title={state?.reasoning_supported ? 'Reasoning effort offered by this ACP agent' : 'This agent does not expose reasoning controls'} bind:value={effort} onchange={() => save('reasoning_effort')} disabled={disabled || saving || !state?.reasoning_supported}>
  {#if !state?.reasoning_supported}<option value={effort}>{saving && !state ? 'Reasoning: loading…' : 'Reasoning: not exposed'}</option>
  {:else}
    {#if !state.reasoning_efforts.some(option => option.id === effort)}<option value={effort}>Reasoning: {effort || 'unspecified'}</option>{/if}
    {#each state.reasoning_efforts as option}<option value={option.id}>{option.name}</option>{/each}
  {/if}
</select>
<button class="icon-button" title="Reload agent models" onclick={load} disabled={disabled || saving}><RefreshCw size={13} /></button>
{#if error}<span class="model-error" role="alert">{error}</span>{/if}

<style>
  select { width: auto; min-width: 0; max-width: 220px; padding: 5px 8px; border: 1px solid var(--border); border-radius: 6px; background: var(--surface-raised); color: var(--text); font: inherit; }
  .effort-select { max-width: 180px; }
  button { width: 26px; height: 26px; }
  .model-error { color: var(--error); font-size: 12px; min-width: 0; overflow-wrap: anywhere; }
</style>
