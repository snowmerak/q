<script lang="ts">
  import { FolderOpen, RefreshCw, X } from '@lucide/svelte';
  import { onDestroy } from 'svelte';
  import { requestJSON } from '../api';
  import DirectoryBrowser from '../sessions/DirectoryBrowser.svelte';
  import DirectoryTree from './DirectoryTree.svelte';
  import FileViewer from './FileViewer.svelte';
  import { resolveFileLink } from './paths';
  import type { FileChanges } from './types';
  export let initialRoot = '';
  export let initialPath = '';
  export let initialLine = 1;
  export let initialMode: 'raw' | 'diff' = 'raw';
  export let roots: string[] = [];
  export let compact = false;
  export let onclose: (() => void) | undefined = undefined;
  export let onlocation: (root: string, path: string, mode: 'raw' | 'diff', line: number) => void = () => {};
  let root = '';
  let rootInput = initialRoot;
  let path = '';
  let pathInput = '';
  let line = initialLine;
  let mode = initialMode;
  let pickerOpen = false;
  let changes: FileChanges | null = null;
  let error = '';
  let loading = false;
  let treeVersion = 0;
  let viewerVersion = 0;
  let generation = 0;
  let controller: AbortController | undefined;
  let initialKey = '';
  $: requestedKey = `${initialRoot}\n${initialPath}\n${initialLine}`;
  $: if (requestedKey !== initialKey) {
    initialKey = requestedKey;
    void loadRoot(initialRoot, initialPath, initialLine);
  }
  $: options = [...new Set([initialRoot, ...roots, root].filter(Boolean))];
  $: statuses = Object.fromEntries((changes?.files || []).map((file) => [file.path, file.status]));
  export function openFile(workspace: string, file: string, fileLine = 1) {
    void loadRoot(workspace, file, fileLine);
  }
  async function loadRoot(requested = rootInput.trim(), preferred = '', requestedLine = 1) {
    const currentGeneration = ++generation;
    controller?.abort();
    changes = null; error = ''; path = ''; pathInput = ''; root = '';
    if (!requested) { loading = false; return; }
    const current = new AbortController();
    controller = current;
    loading = true;
    try {
      // Validate and canonicalize the root before mounting either reader.
      const listing = await requestJSON<{ workspace_root: string }>('GET', `/api/v1/workspaces/files?${new URLSearchParams({ workspace_root: requested })}`, undefined, current.signal);
      if (generation !== currentGeneration) return;
      root = listing.workspace_root;
      rootInput = root;
      path = preferred; pathInput = preferred; line = requestedLine;
      treeVersion++; viewerVersion++;
      onlocation(root, path, mode, line);
      const result = await requestJSON<FileChanges>('GET', `/api/v1/workspaces/files/changes?${new URLSearchParams({ workspace_root: root })}`, undefined, current.signal);
      if (generation === currentGeneration) changes = result;
    } catch (reason) { if (!current.signal.aborted && generation === currentGeneration) error = reason instanceof Error ? reason.message : 'Could not open workspace'; }
    finally { if (generation === currentGeneration) loading = false; }
  }
  function selectFile(value: string, requestedLine = 1) {
    path = value; pathInput = value; line = requestedLine;
    onlocation(root, path, mode, line);
  }
  function openPath() {
    const target = resolveFileLink(pathInput, [root, ...options]);
    if (!target || !target.path) { error = 'Choose a file inside one of the available workspaces.'; return; }
    error = '';
    if (target.root !== root) void loadRoot(target.root, target.path, target.line);
    else selectFile(target.path, target.line);
  }
  function chooseMode(value: 'raw' | 'diff') { mode = value; onlocation(root, path, mode, line); }
  function chooseLine(value: number) { line = value; onlocation(root, path, mode, line); }
  onDestroy(() => { generation++; controller?.abort(); });
</script>

<div class="file-explorer" class:compact aria-label="Workspace files">
  <header class="files-workspace-controls">
    <div class="files-workspace-heading"><strong>{compact ? 'Session files' : 'Workspace files'}</strong>{#if onclose}<button class="icon-button" aria-label="Close file browser" onclick={onclose}><X size={16} /></button>{/if}</div>
    {#if options.length}<label><span>Workspace</span><select aria-label="File workspace" value={root || initialRoot} onchange={(event) => loadRoot(event.currentTarget.value)}><option value="">Select a workspace</option>{#each options as option}<option value={option}>{option}</option>{/each}</select></label>{/if}
    {#if !compact}<div class="files-root-input"><input aria-label="File workspace directory" bind:value={rootInput} placeholder="Workspace directory" onkeydown={(event) => event.key === 'Enter' && loadRoot()} /><button class="icon-button" aria-label="Choose file workspace folder" title="Choose folder" onclick={() => pickerOpen = true}><FolderOpen size={16} /></button><button class="secondary-button" disabled={!rootInput.trim()} onclick={() => loadRoot()}>Open</button></div>{/if}
    <div class="files-path-input"><input aria-label="File path" bind:value={pathInput} placeholder="Relative file path" onkeydown={(event) => event.key === 'Enter' && openPath()} disabled={!root} /><button class="secondary-button" disabled={!root || !pathInput.trim()} onclick={openPath}>Open file</button><button class="icon-button" aria-label="Refresh files" title="Refresh files" disabled={!root || loading} onclick={() => loadRoot(root, path, line)}><RefreshCw size={16} /></button></div>
  </header>
  {#if error}<div class="files-error" role="alert">{error}</div>{/if}
  <div class="files-body">
    <aside class="files-tree" aria-label="Directory and file tree">
      {#if root}{#key `${root}/${treeVersion}`}<DirectoryTree {root} selected={path} {statuses} onselect={selectFile} />{/key}{:else}<p class="file-tree-message">{loading ? 'Opening workspace…' : 'Choose a workspace to browse its files.'}</p>{/if}
      {#if changes?.files.length}<div class="files-changed-heading">GIT CHANGES · {changes.files.length}</div>{#each changes.files as file}<button class="file-tree-entry" class:active={path === file.path} title={file.old_path ? `${file.old_path} → ${file.path}` : file.path} aria-label={`Changed file ${file.path}`} onclick={() => selectFile(file.path)}><small>{file.status}</small><span>{file.path}</span></button>{/each}{/if}
    </aside>
    {#key viewerVersion}<FileViewer {root} {path} {line} {mode} onmode={chooseMode} online={chooseLine} />{/key}
  </div>
</div>
{#if pickerOpen}<DirectoryBrowser onselect={(value) => { pickerOpen = false; void loadRoot(value); }} onclose={() => pickerOpen = false} />{/if}
