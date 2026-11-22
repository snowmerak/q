<script lang="ts">
  import { onDestroy } from 'svelte';
  import { requestJSON } from '../api';
  import type { ACPModelSettings } from '../acpModelSettings';
  import type { ACPExecutionTarget } from './types';

  export let target: ACPExecutionTarget;
  export let workspaceRoot: string;
  export let disabled = false;
  export let selectTarget: (target: ACPExecutionTarget) => void;

  let settings: ACPModelSettings | null = null;
  let loading = false;
  let error = '';
  let disposed = false;
  let probe: AbortController | null = null;
  onDestroy(() => { disposed = true; probe?.abort(); });

  $: model = target.agent_model || '';
  $: effort = target.agent_reasoning_effort || '';
  $: modelAvailable = !model || settings?.current_model === model;
  $: effortOptions = modelAvailable ? settings?.reasoning_efforts || [] : [];
  $: currentModelName = settings?.models.find(option => option.id === settings?.current_model)?.name || settings?.current_model;
  $: currentEffortName = settings?.reasoning_efforts.find(option => option.id === settings?.current_reasoning_effort)?.name || settings?.current_reasoning_effort;

  async function load(model: string, changeModel = false) {
    if (disabled || loading) return;
    const controller = new AbortController();
    probe = controller;
    loading = true;
    error = '';
    try {
      const result = await requestJSON<ACPModelSettings>('POST', '/api/v1/settings/subagents/probe', {
        workspace_root: workspaceRoot, connection_id: target.agent, include_model_settings: true, model
      }, controller.signal);
      if (disposed) return;
      settings = result;
      if (changeModel) {
        if (model && result.current_model !== model) {
          error = 'This ACP agent no longer offers the selected model.';
          return;
        }
        selectTarget({
          agent: target.agent,
          agent_model: model || undefined,
          agent_reasoning_effort: result.reasoning_efforts.some(option => option.id === effort) ? effort : undefined
        });
      }
    } catch (cause) {
      if (!disposed) error = cause instanceof Error ? cause.message : 'Could not load ACP options';
    } finally {
      probe = null;
      if (!disposed) loading = false;
    }
  }

  function selectModel(event: Event) {
    const select = event.currentTarget as HTMLSelectElement;
    const next = select.value;
    select.value = model;
    void load(next, true);
  }

  function selectEffort(event: Event) {
    const select = event.currentTarget as HTMLSelectElement;
    const next = select.value;
    select.value = effort;
    selectTarget({ ...target, agent_reasoning_effort: next || undefined });
  }
</script>

<label>
  <span>ACP model</span>
  <select aria-label="ACP model" value={model} onchange={selectModel} disabled={disabled || loading || !settings?.supported && !model}>
    <option value="">ACP default{!model && currentModelName ? ` · ${currentModelName}` : ''}</option>
    {#if model && !settings?.models.some(option => option.id === model)}
      <option value={model}>{model}{settings && !modelAvailable ? ' · unavailable' : ''}</option>
    {/if}
    {#each settings?.models || [] as option}
      <option value={option.id}>{option.name}{option.group ? ` · ${option.group}` : ''}</option>
    {/each}
  </select>
</label>
<label>
  <span>Reasoning effort</span>
  <select aria-label="ACP reasoning effort" value={effort} onchange={selectEffort}
    disabled={disabled || loading || !modelAvailable || !settings?.reasoning_supported && !effort}>
    <option value="">ACP default{modelAvailable && !effort && currentEffortName ? ` · ${currentEffortName}` : ''}</option>
    {#if effort && !effortOptions.some(option => option.id === effort)}
      <option value={effort}>{effort}{settings && modelAvailable ? ' · unavailable' : ''}</option>
    {/if}
    {#each effortOptions as option}
      <option value={option.id}>{option.name}{option.group ? ` · ${option.group}` : ''}</option>
    {/each}
  </select>
</label>
<div class="actions">
  <button type="button" class="secondary-button" onclick={() => load(model)} disabled={disabled || loading}>
    {loading ? 'Connecting to ACP…' : settings ? 'Refresh ACP options' : 'Load ACP options'}
  </button>
</div>
{#if error}<small class="option-error" role="alert">{error}</small>{/if}

<style>
  .option-error { color: var(--error); overflow-wrap: anywhere; }
</style>
