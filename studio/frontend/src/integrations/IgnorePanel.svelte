<script lang="ts">
  import { onDestroy } from 'svelte';
  import { requestJSON } from '../api';
  import { requireWorkspace } from './api';
  import type { SettingsOperations } from '../settings/operations';
  import type { IgnoreResponse } from './types';

  export let active = false;
  export let reloadToken = 0;
  export let operations: SettingsOperations;
  export let workspaceRoot = '';
  let ignore: IgnoreResponse | null = null;
  let loadedKey = '';
  let loadGeneration = 0;
  let ignoreTimer: ReturnType<typeof setTimeout> | undefined;
  let pendingDocument: IgnoreResponse | null = null;

  $: if (active && loadedKey !== JSON.stringify([workspaceRoot, reloadToken])) load();
  $: if (!active) flushIgnoreSave();

  function load() {
    flushIgnoreSave();
    loadedKey = JSON.stringify([workspaceRoot, reloadToken]);
    const generation = ++loadGeneration;
    const target = workspaceRoot;
    ignore = null;
    operations.run(async () => {
      const root = requireWorkspace(target);
      const result = await requestJSON<IgnoreResponse>('GET', `/api/v1/workspaces/ignore?workspace_root=${encodeURIComponent(root)}`);
      if (generation === loadGeneration) ignore = result;
    }, '');
  }

  function queueIgnoreSave() {
    if (ignoreTimer) clearTimeout(ignoreTimer);
    pendingDocument = ignore;
    ignoreTimer = setTimeout(flushIgnoreSave, 600);
  }

  function flushIgnoreSave() {
    if (ignoreTimer) clearTimeout(ignoreTimer);
    ignoreTimer = undefined;
    const document = pendingDocument;
    pendingDocument = null;
    if (!document) return;
    const content = document.content;
    operations.run(async () => {
      // Read the latest acknowledged revision when this write reaches the queue.
      const result = await requestJSON<IgnoreResponse>('PUT', '/api/v1/workspaces/ignore', {
        workspace_root: document.workspace_root, content, revision: document.revision
      });
      document.revision = result.revision;
      document.path = result.path;
      if (ignore === document) ignore = document;
    }, '.qignore saved');
  }

  onDestroy(flushIgnoreSave);
</script>

{#if active}
    {#if ignore}
      <div class="subsection-heading"><div><h3>Discovery rules</h3><p>Changes save after a short pause and affect new listings and LSP discovery.</p></div><code>{ignore.path}</code></div>
      <div class="settings-card ignore-editor"><p><code>#</code> comment · <code>!</code> include · <code>/</code> root · trailing <code>/</code> directory · <code>* ** ?</code> wildcards</p><textarea bind:value={ignore.content} oninput={queueIgnoreSave} spellcheck="false" aria-label=".qignore content"></textarea></div>
    {:else}<div class="loading-panel">Loading .qignore…</div>{/if}
{/if}
