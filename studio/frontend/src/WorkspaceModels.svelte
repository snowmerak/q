<script lang="ts">
  import { RefreshCw } from '@lucide/svelte';
  import { onMount } from 'svelte';
  import { requestResponse } from './api';

  type ModelOption = { id: string; group?: boolean };
  type Assignment = { role: string; configured_model: string; effective_model: string; inherited: boolean };
  type Snapshot = { workspace_root: string; config_path: string; assignments: Assignment[] };

  export let models: ModelOption[] = [];

  let workspaceRoot = '';
  let snapshot: Snapshot | null = null;
  let loading = false;
  let error = '';
  let saved = '';

  async function load() {
    const root = workspaceRoot.trim();
    if (!root) {
      snapshot = null;
      error = '';
      return;
    }
    loading = true;
    error = '';
    saved = '';
    try {
      const response = await requestResponse(`/api/v1/workspaces/models?workspace_root=${encodeURIComponent(root)}`, { headers: { Accept: 'application/json' } });
      snapshot = await response.json() as Snapshot;
      workspaceRoot = snapshot.workspace_root;
      localStorage.setItem('q-studio-workspace-root', workspaceRoot);
    } catch (reason) {
      error = reason instanceof Error ? reason.message : 'Workspace model settings are unavailable';
    } finally {
      loading = false;
    }
  }

  async function save(assignment: Assignment) {
    if (!snapshot) return;
    loading = true;
    error = '';
    saved = '';
    try {
      const method = assignment.configured_model ? 'PUT' : 'DELETE';
      const response = await requestResponse(`/api/v1/workspaces/models/${encodeURIComponent(assignment.role)}`, {
        method,
        headers: { Accept: 'application/json', 'Content-Type': 'application/json' },
        body: JSON.stringify({ workspace_root: snapshot.workspace_root, model: assignment.configured_model })
      });
      snapshot = await response.json() as Snapshot;
      saved = 'Saved';
      window.setTimeout(() => { saved = ''; }, 1400);
    } catch (reason) {
      const message = reason instanceof Error ? reason.message : 'Workspace model could not be saved';
      await load();
      error = message;
      saved = '';
    } finally {
      loading = false;
    }
  }

  function label(role: string) {
    return role === 'default' ? 'Default' : role.split('-').map((part) => part.charAt(0).toUpperCase() + part.slice(1)).join(' ');
  }

  onMount(() => {
    workspaceRoot = new URL(window.location.href).searchParams.get('workspace_root') || localStorage.getItem('q-studio-workspace-root') || '';
    if (workspaceRoot) void load();
  });
</script>

<div class="workspace-model-heading">
  <div><h3>Workspace overrides</h3><p>Override the default and occupational role models for one repository.</p></div>
  <span class:error={!!error}>{loading ? 'Loading…' : error || saved}</span>
</div>
<div class="settings-card workspace-model-card">
  <div class="workspace-root-row">
    <label><span>Repository path</span><input bind:value={workspaceRoot} onkeydown={(event) => event.key === 'Enter' && load()} placeholder="Choose a repository in Sessions" /></label>
    <button class="icon-button" title="Load workspace models" onclick={load} disabled={loading || !workspaceRoot.trim()}><RefreshCw aria-hidden="true" size={15} class={loading ? 'spin' : undefined} /></button>
  </div>
  {#if snapshot}
    <code>{snapshot.config_path}</code>
    <div class="workspace-assignments">
      {#each snapshot.assignments as assignment}
        <div class="workspace-assignment">
          <div><strong>{label(assignment.role)}</strong><small>{assignment.inherited ? `Inherits ${assignment.effective_model}` : assignment.effective_model}</small></div>
          <label><span>Model</span><select bind:value={assignment.configured_model} onchange={() => save(assignment)} disabled={loading}>
            <option value="">Inherit global</option>
            {#if assignment.configured_model && !models.some((model) => model.id === assignment.configured_model)}<option value={assignment.configured_model}>{assignment.configured_model}</option>{/if}
            {#each models as model}<option value={model.id}>{model.id}{model.group ? ' · group' : ''}</option>{/each}
          </select></label>
        </div>
      {/each}
    </div>
  {:else}
    <p class="workspace-empty">Open a repository in Sessions or enter its path to edit `.q/model.json`.</p>
  {/if}
</div>

<style>
  .workspace-model-heading { display: flex; align-items: flex-end; justify-content: space-between; gap: 18px; margin: 28px 0 10px; }
  .workspace-model-heading h3 { margin: 0; color: var(--text); font-size: calc(14px * var(--text-scale)); }
  .workspace-model-heading p { margin: 5px 0 0; color: #7d869b; font-size: calc(11px * var(--text-scale)); }
  .workspace-model-heading > span { min-height: 16px; color: #69d9aa; font: calc(10px * var(--text-scale)) ui-monospace, SFMono-Regular, Consolas, monospace; }
  .workspace-model-heading > span.error { max-width: 48%; color: #f299a4; text-align: right; }
  .workspace-model-card { overflow: hidden; }
  .workspace-root-row { display: grid; grid-template-columns: minmax(0, 1fr) auto; align-items: end; gap: 10px; padding: 15px 18px; border-bottom: 1px solid var(--border); }
  .workspace-root-row label { display: grid; gap: 6px; }
  .workspace-root-row label span, .workspace-assignment label span { color: var(--subtle); font-size: calc(10px * var(--text-scale)); }
  .workspace-root-row input, .workspace-assignment select { width: 100%; min-width: 0; }
  .workspace-model-card > code { display: block; overflow: hidden; padding: 10px 18px; color: #68738c; border-bottom: 1px solid var(--border); text-overflow: ellipsis; white-space: nowrap; }
  .workspace-assignment { display: grid; grid-template-columns: minmax(170px, 0.55fr) minmax(260px, 1fr); align-items: center; gap: 22px; padding: 13px 18px; border-top: 1px solid var(--border); }
  .workspace-assignment:first-child { border-top: 0; }
  .workspace-assignment > div { display: grid; gap: 4px; min-width: 0; }
  .workspace-assignment strong { color: var(--text); font-size: calc(12px * var(--text-scale)); }
  .workspace-assignment small { overflow: hidden; color: #758099; font: calc(9px * var(--text-scale)) ui-monospace, SFMono-Regular, Consolas, monospace; text-overflow: ellipsis; white-space: nowrap; }
  .workspace-assignment label { display: grid; gap: 5px; }
  .workspace-empty { margin: 0; padding: 18px; color: var(--subtle); font-size: calc(11px * var(--text-scale)); }
  @media (max-width: 760px) { .workspace-assignment { grid-template-columns: 1fr; gap: 10px; } .workspace-model-heading { align-items: flex-start; flex-direction: column; } }
</style>
