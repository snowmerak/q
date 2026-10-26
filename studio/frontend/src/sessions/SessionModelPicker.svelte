<script lang="ts">
  import { onMount } from 'svelte';
  import { requestResponse } from '../api';
  import { reasoningOptions } from '../settings/reasoning';
  import type { ModelOption } from '../settings/types';

  export let workspaceRoot: string;
  export let sessionID: string;
  export let disabled = false;
  export let saving = false;

  type Selection = { model: string; effective_model: string; reasoning_effort: string; effective_reasoning_effort: string };
  let selection: Selection | null = null;
  let models: ModelOption[] = [];
  let loading = true;
  let model = '';
  let effort = '';
  let error = '';
  let catalogError = '';
  let disposed = false;
  const endpoint = `/api/v1/sessions/${encodeURIComponent(sessionID)}/model`;

  onMount(() => {
    void Promise.all([
      requestResponse(`${endpoint}?workspace_root=${encodeURIComponent(workspaceRoot)}`)
        .then(response => response.json())
        .then((value: Selection) => { if (!disposed) { selection = value; model = value.model; effort = value.reasoning_effort || ''; } })
        .catch(cause => { if (!disposed) error = cause instanceof Error ? cause.message : 'Could not load the session model'; }),
      requestResponse('/api/v1/settings/models')
        .then(response => response.json())
        .then((value: { models: ModelOption[] }) => { if (!disposed) models = value.models; })
        .catch(cause => { if (!disposed) catalogError = cause instanceof Error ? cause.message : 'Models are unavailable'; })
    ]).finally(() => { if (!disposed) loading = false; });
    return () => { disposed = true; };
  });

  async function save() {
    if (!selection || saving || disabled) return;
    saving = true;
    error = '';
    try {
      const response = await requestResponse(endpoint, {
        method: 'PUT', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ workspace_root: workspaceRoot, model, reasoning_effort: effort })
      });
      const updated = await response.json() as Selection;
      if (!disposed) selection = updated;
    } catch (cause) {
      if (!disposed) error = cause instanceof Error ? cause.message : 'Could not save the session model';
    } finally {
      if (!disposed) {
        model = selection.model;
        effort = selection.reasoning_effort || '';
        saving = false;
      }
    }
  }
</script>

<div class="session-model-picker">
  <select class="model-select" aria-label="Session model" title={model || selection?.effective_model || 'Default model'} bind:value={model} onchange={save} disabled={disabled || loading || saving || !selection}>
    <option value="">Default{selection && !selection.model ? ` · ${selection.effective_model}` : ''}</option>
    {#if selection?.model && !models.some(model => model.id === selection?.model)}<option value={selection.model}>{selection.model}</option>{/if}
    {#each models as model}<option value={model.id}>{model.id}{model.group ? ' · group' : ''}</option>{/each}
  </select>
  <select class="effort-select" aria-label="Reasoning effort" title="Reasoning effort for this chat" bind:value={effort} onchange={save} disabled={disabled || loading || saving || !selection}>
    <option value="">Reasoning: default{selection?.effective_reasoning_effort && !selection.reasoning_effort ? ` · ${selection.effective_reasoning_effort}` : ''}</option>
    {#each reasoningOptions(model || selection?.effective_model || '', effort || selection?.effective_reasoning_effort || '', models) as option}<option value={option}>{option}</option>{/each}
  </select>
  {#if saving}<span class="model-status" role="status">Saving…</span>{/if}
  {#if error || catalogError}<span class="model-error" role="alert">{error || catalogError}</span>{/if}
</div>

<style>
  .session-model-picker { display: flex; align-items: center; flex-wrap: wrap; gap: 8px; max-width: min(420px, 50%); min-width: 0; font-size: 12px; }
  select { width: auto; min-width: 0; max-width: 100%; padding: 5px 8px; border: 1px solid var(--border); border-radius: 6px; background: var(--surface-raised); color: var(--text); font: inherit; }
  .model-select { width: 220px; }
  .effort-select { max-width: min(160px, 100%); }
  .model-status, .model-error { font-size: 12px; }
  .model-error { color: var(--error); }
</style>
