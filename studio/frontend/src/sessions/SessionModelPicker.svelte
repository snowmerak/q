<script lang="ts">
  import { onMount } from 'svelte';
  import { requestResponse } from '../api';
  import { reasoningOptions } from '../settings/reasoning';
  import type { ModelOption } from '../settings/types';
  import type { AgentResponse } from '../subagents/types';

  export let workspaceRoot: string;
  export let sessionID: string;
  export let disabled = false;
  export let saving = false;
  export let agent = '';

  type Selection = { model: string; agent?: string; effective_model: string; reasoning_effort: string; effective_reasoning_effort: string };
  let selection: Selection | null = null;
  let models: ModelOption[] = [];
  let agents: string[] = [];
  let loading = true;
  let model = '';
  let effort = '';
  let error = '';
  let catalogError = '';
  let agentError = '';
  let disposed = false;
  const endpoint = `/api/v1/sessions/${encodeURIComponent(sessionID)}/model`;

  onMount(() => {
    void Promise.all([
      requestResponse(`${endpoint}?workspace_root=${encodeURIComponent(workspaceRoot)}`)
        .then(response => response.json())
        .then((value: Selection) => { if (!disposed) { selection = value; agent = value.agent || ''; model = value.agent ? `acp:${value.agent}` : value.model; effort = value.reasoning_effort || ''; } })
        .catch(cause => { if (!disposed) error = cause instanceof Error ? cause.message : 'Could not load the session model'; }),
      requestResponse('/api/v1/settings/models')
        .then(response => response.json())
        .then((value: { models: ModelOption[] }) => { if (!disposed) models = value.models; })
        .catch(cause => { if (!disposed) catalogError = cause instanceof Error ? cause.message : 'Models are unavailable'; }),
      requestResponse('/api/v1/settings/acp')
        .then(response => response.json())
        .then((value: AgentResponse) => { if (!disposed) agents = Object.entries(value.connections || {}).filter(([, connection]) => !connection.disabled).map(([id]) => id).sort(); })
        .catch(cause => { if (!disposed) agentError = cause instanceof Error ? cause.message : 'ACP providers are unavailable'; })
    ]).finally(() => { if (!disposed) loading = false; });
    return () => { disposed = true; };
  });

  async function save() {
    if (!selection || saving || disabled) return;
    saving = true;
    error = '';
    const selectedAgent = model.startsWith('acp:') ? model.slice(4) : '';
    try {
      const response = await requestResponse(endpoint, {
        method: 'PUT', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ workspace_root: workspaceRoot, model: selectedAgent ? '' : model, agent: selectedAgent, reasoning_effort: selectedAgent ? '' : effort })
      });
      const updated = await response.json() as Selection;
      if (!disposed) selection = updated;
    } catch (cause) {
      if (!disposed) error = cause instanceof Error ? cause.message : 'Could not save the session model';
    } finally {
      if (!disposed) {
        model = selection.agent ? `acp:${selection.agent}` : selection.model;
        agent = selection.agent || '';
        effort = selection.reasoning_effort || '';
        saving = false;
      }
    }
  }
</script>

<div class="session-model-picker">
  <select class="model-select" aria-label="Session model" title={model || selection?.effective_model || 'Default model'} bind:value={model} onchange={save} disabled={disabled || loading || saving || !selection}>
    <option value="">Default{selection && !selection.model && !selection.agent ? ` · ${selection.effective_model}` : ''}</option>
    {#if selection?.model && !models.some(model => model.id === selection?.model)}<option value={selection.model}>{selection.model}</option>{/if}
    <optgroup label="API Provider">{#each models as model}<option value={model.id}>{model.id}{model.group ? ' · group' : ''}</option>{/each}</optgroup>
    <optgroup label="ACP">
      {#if selection?.agent && !agents.includes(selection.agent)}<option value={`acp:${selection.agent}`}>ACP · {selection.agent} · unavailable</option>{/if}
      {#each agents as agent}<option value={`acp:${agent}`}>ACP · {agent}</option>{/each}
    </optgroup>
  </select>
  <select class="effort-select" aria-label="Reasoning effort" title="Reasoning effort for this chat" bind:value={effort} onchange={save} disabled={disabled || loading || saving || !selection || model.startsWith('acp:')}>
    <option value="">{model.startsWith('acp:') ? 'Agent default' : 'Reasoning: default'}{selection?.effective_reasoning_effort && !selection.reasoning_effort ? ` · ${selection.effective_reasoning_effort}` : ''}</option>
    {#each reasoningOptions(model || selection?.effective_model || '', effort || selection?.effective_reasoning_effort || '', models) as option}<option value={option}>{option}</option>{/each}
  </select>
  {#if saving}<span class="model-status" role="status">Saving…</span>{/if}
  {#if error || (catalogError && !agents.length) || agentError}<span class="model-error" role="alert">{error || (agents.length ? '' : catalogError) || agentError}</span>{/if}
</div>

<style>
  .session-model-picker { display: flex; align-items: center; flex-wrap: wrap; gap: 8px; max-width: min(420px, 50%); min-width: 0; font-size: 12px; }
  select { width: auto; min-width: 0; max-width: 100%; padding: 5px 8px; border: 1px solid var(--border); border-radius: 6px; background: var(--surface-raised); color: var(--text); font: inherit; }
  .model-select { width: 220px; }
  .effort-select { max-width: min(160px, 100%); }
  .model-status, .model-error { font-size: 12px; }
  .model-error { color: var(--error); }
</style>
