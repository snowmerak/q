<script lang="ts">
  import { GitCommitHorizontal, RefreshCw } from '@lucide/svelte';
  import { onDestroy, onMount } from 'svelte';
  import { requestJSON } from './api';
  import CommitReview from './changes/CommitReview.svelte';
  import FileDiff from './changes/FileDiff.svelte';
  import { statusLabel } from './changes/format';
  import type { ChangedFile, ChangeSnapshot, ChangeDetail } from './changes/types';

  let workspaceInput = '';
  let workspaceRoot = '';
  let snapshot: ChangeSnapshot | null = null;
  let selected: ChangedFile | null = null;
  let detail: ChangeDetail | null = null;
  let loading = false;
  let detailLoading = false;
  let commitLoading = false;
  let reviewing = false;
  let error = '';
  let commitPanel: CommitReview | null = null;
  let loadGeneration = 0;
  let detailGeneration = 0;
  let listController: AbortController | undefined;
  let detailController: AbortController | undefined;

  async function loadChanges(preferred = '', requested = workspaceInput.trim()) {
    if (!requested || (reviewing && requested !== workspaceRoot)) return;
    const generation = ++loadGeneration;
    listController?.abort();
    detailController?.abort();
    detailGeneration += 1;
    const controller = new AbortController();
    listController = controller;
    loading = true;
    detailLoading = false;
    detail = null;
    error = '';
    try {
      const response = await requestJSON<{ workspace_root: string; snapshot: ChangeSnapshot }>('GET',
        `/api/v1/workspaces/changes?workspace_root=${encodeURIComponent(requested)}`, undefined, controller.signal);
      if (generation !== loadGeneration) return;
      workspaceRoot = response.workspace_root;
      workspaceInput = response.workspace_root;
      localStorage.setItem('q-studio-workspace-root', response.workspace_root);
      snapshot = response.snapshot;
      selected = response.snapshot.files.find((file) => file.path === preferred) || response.snapshot.files[0] || null;
      if (selected) await loadDetail(selected);
    } catch (reason) {
      if (controller.signal.aborted || generation !== loadGeneration) return;
      error = reason instanceof Error ? reason.message : 'Could not read repository changes';
      snapshot = null;
      selected = null;
      detail = null;
    } finally {
      if (generation === loadGeneration) loading = false;
    }
  }

  async function loadDetail(file: ChangedFile) {
    const root = workspaceRoot;
    if (!root || (loading && !snapshot?.files.includes(file))) return;
    const generation = ++detailGeneration;
    detailController?.abort();
    const controller = new AbortController();
    detailController = controller;
    selected = file;
    detail = null;
    detailLoading = true;
    error = '';
    try {
      const response = await requestJSON<ChangeDetail>('GET',
        `/api/v1/workspaces/changes/file?workspace_root=${encodeURIComponent(root)}&path=${encodeURIComponent(file.path)}`, undefined, controller.signal);
      if (generation === detailGeneration) detail = response;
    } catch (reason) {
      if (!controller.signal.aborted && generation === detailGeneration) error = reason instanceof Error ? reason.message : 'Could not load the selected diff';
    } finally {
      if (generation === detailGeneration) detailLoading = false;
    }
  }

  onMount(() => {
    workspaceInput = new URL(window.location.href).searchParams.get('workspace_root') || localStorage.getItem('q-studio-workspace-root') || '';
    if (workspaceInput) void loadChanges();
  });
  onDestroy(() => {
    loadGeneration += 1;
    detailGeneration += 1;
    listController?.abort();
    detailController?.abort();
  });
</script>

<div class="changes-layout">
  <aside class="changes-rail">
    <div class="changes-workspace"><label><span>Repository</span><input bind:value={workspaceInput} placeholder="Repository path" /></label><button class="icon-button" title="Load changes" onclick={() => loadChanges()} disabled={loading || commitLoading || (reviewing && workspaceInput.trim() !== workspaceRoot)}><RefreshCw size={15} class={loading ? 'spin' : undefined} /></button></div>
    <div class="changes-heading"><span>{snapshot?.files.length || 0} changed files</span><button class="text-button" onclick={() => loadChanges(selected?.path || '', workspaceRoot)} disabled={loading}>Refresh</button></div>
    <div class="changed-files">{#each snapshot?.files || [] as file}<button class:active={selected?.path === file.path} onclick={() => loadDetail(file)}><span class="git-status">{file.status}</span><span><strong>{file.path}</strong>{#if file.old_path}<small>{file.old_path} →</small>{:else}<small>{statusLabel(file.status)}</small>{/if}</span></button>{:else}<p>{loading ? 'Reading changes…' : 'Working tree is clean.'}</p>{/each}</div>
    <button class="primary-button prepare-commit" onclick={() => commitPanel?.prepareCommit()} disabled={loading || commitLoading || reviewing || !snapshot?.files.length}><GitCommitHorizontal size={16} /> Prepare commit</button>
  </aside>

  <FileDiff {selected} {detail} loading={detailLoading} {error} />
  {#key workspaceRoot}
    <CommitReview bind:this={commitPanel} root={workspaceRoot} preferredPath={selected?.path || ''}
      bind:busy={commitLoading} bind:reviewing onrefresh={(preferred) => loadChanges(preferred, workspaceRoot)} />
  {/key}
</div>
