<script lang="ts">
  import type { ModelOption } from '../settings/types';
  import { reasoningOptions } from '../settings/reasoning';
  import type { ExecutionTarget } from './types';

  export let target: ExecutionTarget | null | undefined = undefined;
  export let models: ModelOption[];
  export let connections: string[];
  export let externalOnly = false;
  export let defaultModel = '';
  export let disabled = false;
  export let label = 'Run with';
  export let selectTarget: (target: ExecutionTarget | null) => void;

  $: value = target?.agent ? `acp:${target.agent}` : target?.model ? `model:${target.model}` : '';
  $: effort = target?.reasoning_effort || '';
  $: efforts = reasoningOptions(target?.model || defaultModel, effort, models);

  function change(event: Event) {
    const select = event.currentTarget as HTMLSelectElement;
    const next = select.value;
    select.value = value;
    if (next.startsWith('acp:')) selectTarget({ agent: next.slice(4) });
    else if (next.startsWith('model:')) selectTarget({ model: next.slice(6) });
    else selectTarget(externalOnly ? null : { default: true });
  }

  function changeEffort(event: Event) {
    const select = event.currentTarget as HTMLSelectElement;
    const reasoning_effort = select.value || undefined;
    select.value = effort;
    const model = target?.model;
    selectTarget(model ? { model, reasoning_effort } : { default: true, reasoning_effort });
  }
</script>

<div class="execution-fields">
<label>
  <span>{label}</span>
  <select {value} aria-label={label} onchange={change} {disabled}>
    <option value="">{externalOnly ? 'Unassigned' : 'Use default model'}</option>
    {#if !externalOnly}
      <optgroup label="API Provider">
        {#if target?.model && !models.some(model => model.id === target?.model)}
          <option value={`model:${target.model}`}>{target.model} · unavailable</option>
        {/if}
        {#each models as model}<option value={`model:${model.id}`}>{model.id}{model.group ? ' · group' : ''}</option>{/each}
      </optgroup>
    {/if}
    <optgroup label="ACP">
      {#if target?.agent && !connections.includes(target.agent)}
        <option value={`acp:${target.agent}`}>ACP · {target.agent} · unavailable</option>
      {/if}
      {#each connections as connection}<option value={`acp:${connection}`}>ACP · {connection}</option>{/each}
    </optgroup>
  </select>
</label>
{#if !externalOnly && !target?.agent && efforts.length}
  <label>
    <span>Reasoning</span>
    <select value={effort} aria-label="Reasoning" onchange={changeEffort} {disabled}>
      <option value="">Provider default</option>
      {#each efforts as option}<option value={option}>{option}</option>{/each}
    </select>
  </label>
{/if}
</div>

<style>
  .execution-fields { display: grid; min-width: 0; gap: 8px; }
</style>
