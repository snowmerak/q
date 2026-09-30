<script lang="ts">
  import { ChevronDown, ChevronRight, File, Folder, Link } from '@lucide/svelte';
  import { onDestroy } from 'svelte';
  import { requestJSON } from '../api';
  import type { FileEntry, FileListing } from './types';
  export let root: string;
  export let path = '.';
  export let selected = '';
  export let statuses: Record<string, string> = {};
  export let onselect: (path: string) => void;
  export let depth = 0;
  let entries: FileEntry[] = [];
  let expanded = new Set<string>();
  let nextOffset = -1;
  let loading = false;
  let error = '';
  let started = false;
  let controller: AbortController | undefined;
  $: if (!started) { started = true; void load(); }

  async function load(offset = 0) {
    controller?.abort();
    const current = new AbortController();
    controller = current;
    loading = true;
    error = '';
    try {
      const listing = await requestJSON<FileListing>('GET', `/api/v1/workspaces/files?${new URLSearchParams({ workspace_root: root, path, offset: String(offset) })}`, undefined, current.signal);
      if (current.signal.aborted) return;
      entries = offset ? [...entries, ...listing.entries] : listing.entries;
      nextOffset = listing.next_offset;
    } catch (reason) {
      if (!current.signal.aborted) error = reason instanceof Error ? reason.message : 'Could not read directory';
    } finally { if (!current.signal.aborted) loading = false; }
  }
  function toggle(path: string) {
    const next = new Set(expanded);
    if (next.has(path)) next.delete(path); else next.add(path);
    expanded = next;
  }
  onDestroy(() => controller?.abort());
</script>

<div class="file-tree-branch">
  {#each entries as entry (entry.path)}
    <button class="file-tree-entry" class:active={selected === entry.path} style={`padding-left: ${12 + depth * 16}px`} aria-label={`${entry.directory ? 'Directory' : 'File'} ${entry.path}`} aria-expanded={entry.directory ? expanded.has(entry.path) : undefined} title={entry.unavailable ? 'Link target is unavailable or outside this workspace' : entry.path} disabled={entry.unavailable} onclick={() => entry.directory ? toggle(entry.path) : onselect(entry.path)}>
      {#if entry.directory}{#if expanded.has(entry.path)}<ChevronDown size={13} />{:else}<ChevronRight size={13} />{/if}<Folder size={15} />{:else}<span class="file-tree-spacer"></span><File size={15} />{/if}
      <span>{entry.name}</span>{#if entry.symlink}<Link size={12} />{/if}{#if statuses[entry.path]}<small>{statuses[entry.path]}</small>{/if}
    </button>
    {#if entry.directory && expanded.has(entry.path)}<svelte:self {root} path={entry.path} {selected} {statuses} {onselect} depth={depth + 1} />{/if}
  {/each}
  {#if error}<p class="file-tree-message" role="alert">{error}</p><button class="text-button" onclick={() => load()}>Retry directory</button>{/if}
  {#if loading}<p class="file-tree-message">Loading directory…</p>{:else if !entries.length && !error}<p class="file-tree-message">Empty directory</p>{/if}
  {#if nextOffset >= 0}<button class="text-button file-tree-more" disabled={loading} onclick={() => load(nextOffset)}>Load more entries</button>{/if}
</div>
