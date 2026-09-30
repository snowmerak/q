<script lang="ts">
  import { onMount } from 'svelte';
  import { requestJSON } from './api';
  import FileExplorer from './files/FileExplorer.svelte';
  import type { RegisteredSessionTree, StudioProject } from './sessions/types';
  const query = new URL(window.location.href).searchParams;
  let initialRoot = query.get('workspace_root') || localStorage.getItem('q-studio-workspace-root') || '';
  let initialPath = query.get('path') || '';
  let initialLine = Number(query.get('line')) || 1;
  let initialMode: 'raw' | 'diff' = query.get('mode') === 'diff' ? 'diff' : 'raw';
  let roots: string[] = [];
  let error = '';
  function location(root: string, path: string, mode: 'raw' | 'diff', line: number) {
    localStorage.setItem('q-studio-workspace-root', root);
    const url = new URL(window.location.href);
    url.search = new URLSearchParams({ workspace_root: root, ...(path ? { path } : {}), mode, line: String(line) }).toString();
    window.history.replaceState({}, '', url.pathname + url.search);
  }
  onMount(() => {
    let disposed = false;
    void Promise.all([
      requestJSON<{ projects: StudioProject[] }>('GET', '/api/v1/projects'),
      requestJSON<{ sessions: RegisteredSessionTree[] }>('GET', '/api/v1/registered-sessions')
    ]).then(([projects, sessions]) => {
      if (!disposed) roots = [...new Set([...projects.projects.flatMap((project) => project.workspace_roots), ...sessions.sessions.map((session) => session.workspace_root)])];
    }).catch((reason) => { if (!disposed) error = reason instanceof Error ? reason.message : 'Could not load available workspaces'; });
    return () => { disposed = true; };
  });
</script>

{#if error}<div class="files-error" role="alert">{error}</div>{/if}
<FileExplorer {initialRoot} {initialPath} {initialLine} {initialMode} {roots} onlocation={location} />
