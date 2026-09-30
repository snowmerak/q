<script lang="ts">
  import { Copy, RefreshCw } from '@lucide/svelte';
  import { onDestroy } from 'svelte';
  import { requestJSON } from '../api';
  import DiffView from '../DiffView.svelte';
  import CodeView from './CodeView.svelte';
  import { fileLanguage } from './paths';
  import type { FileContent, FileDiff } from './types';
  export let root: string;
  export let path: string;
  export let line = 1;
  export let mode: 'raw' | 'diff' = 'raw';
  export let onmode: (mode: 'raw' | 'diff') => void = () => {};
  export let online: (line: number) => void = () => {};
  let content: FileContent | null = null;
  let diff: FileDiff | null = null;
  let loading = false;
  let error = '';
  let copied = '';
  let generation = 0;
  let controller: AbortController | undefined;
  let loadedKey = '';
  $: key = `${root}\n${path}\n${mode}`;
  $: if (key !== loadedKey) { loadedKey = key; void load(); }
  $: language = fileLanguage(path);
  async function load() {
    const currentGeneration = ++generation;
    controller?.abort();
    content = null; diff = null; error = ''; copied = '';
    if (!root || !path) { loading = false; return; }
    const current = new AbortController();
    controller = current;
    loading = true;
    const selectedMode = mode;
    try {
      const result = await requestJSON<FileContent | FileDiff>('GET', `/api/v1/workspaces/files/${selectedMode === 'raw' ? 'content' : 'diff'}?${new URLSearchParams({ workspace_root: root, path })}`, undefined, current.signal);
      if (generation !== currentGeneration) return;
      if (selectedMode === 'raw') content = result as FileContent; else diff = result as FileDiff;
    } catch (reason) { if (!current.signal.aborted && generation === currentGeneration) error = reason instanceof Error ? reason.message : 'Could not open file'; }
    finally { if (generation === currentGeneration) loading = false; }
  }
  async function copy() {
    try { await navigator.clipboard.writeText(content?.content || ''); copied = content?.truncated ? 'Preview copied' : 'Copied'; }
    catch { copied = 'Copy failed'; }
  }
  function chooseMode(value: 'raw' | 'diff') { mode = value; onmode(value); }
  onDestroy(() => { generation++; controller?.abort(); });
</script>

<section class="file-viewer" aria-label="File viewer">
  <header><div><strong title={path}>{path || 'Select a file'}</strong><small>{root}</small></div><button class="icon-button" aria-label="Refresh file" title="Refresh file" disabled={!path || loading} onclick={() => load()}><RefreshCw size={16} /></button></header>
  <div class="file-mode-bar"><div role="group" aria-label="File view mode"><button class:active={mode === 'raw'} aria-pressed={mode === 'raw'} onclick={() => chooseMode('raw')}>Raw</button><button class:active={mode === 'diff'} aria-pressed={mode === 'diff'} onclick={() => chooseMode('diff')}>Diff</button></div><span>{mode === 'raw' ? language : 'Git changes'}</span>{#if content && !content.binary && !content.missing}<button class="text-button" aria-label={content.truncated ? 'Copy file preview' : 'Copy file content'} onclick={copy}><Copy size={14} />{copied || (content.truncated ? 'Copy preview' : 'Copy')}</button>{/if}</div>
  {#if error}<div class="file-view-state" role="alert">{error}<button class="secondary-button" onclick={() => load()}>Retry file</button></div>
  {:else if loading}<div class="file-view-state">Loading {mode === 'raw' ? 'file' : 'diff'}…</div>
  {:else if !path}<div class="file-view-state">Select a file to inspect its content or Git changes.</div>
  {:else if content}
    <div class="file-preview-meta">{content.size.toLocaleString()} bytes{#if content.truncated}<span>Partial preview · first 256 KiB / 4,000 lines maximum</span>{/if}</div>
    {#if content.missing}<div class="file-view-state">File is absent from the working directory. Use Diff to inspect a deletion.</div>
    {:else if content.binary}<div class="file-view-state">Binary or non-UTF-8 file · content is not displayed.</div>
    {:else if !content.content}<div class="file-view-state">Empty file.</div>
    {:else}<CodeView content={content.content} {language} {line} {online} />{/if}
  {:else if diff}
    {#if !diff.available}<div class="file-view-state">Git diff is unavailable for this directory.<small>{diff.reason}</small></div>
    {:else if !diff.sections.length}<div class="file-view-state">No Git changes for this file.</div>
    {:else}{#key `${root}/${path}`}<DiffView sections={diff.sections} />{/key}{/if}
  {/if}
</section>
