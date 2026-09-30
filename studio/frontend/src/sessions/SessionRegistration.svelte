<script lang="ts">
  import { FolderOpen, GitBranch, Plus, X } from '@lucide/svelte';
  import { onMount } from 'svelte';
  import DirectoryBrowser from './DirectoryBrowser.svelte';
  import { apiError } from '../api';
  import { formatSessionTime, shortID } from './format';
  import type { RegisteredSessionTree, SessionSummary, StudioProject } from './types';

  export let projects: StudioProject[] = [];
  export let registeredSessions: RegisteredSessionTree[] = [];
  export let busy = false;
  export let onregister: (root: string, sessionID: string, create: boolean) => Promise<void> = async () => {};
  export let onclose: () => void = () => {};
  export let onerror: (message: string) => void = () => {};

  let workspaceInput = '';
  let workspaceLoading = false;
  let sessions: SessionSummary[] = [];
  let registrationProjectID = '';
  let registrationProjectRoots: string[] = [];
  let registrationProjectRoot = '';
  let directoryPickerOpen = false;

  async function loadWorkspaceCandidates() {
    const requested = workspaceInput.trim();
    if (!requested) return;
    workspaceLoading = true;
    onerror('');
    try {
      const response = await fetch(`/api/v1/sessions?workspace_root=${encodeURIComponent(requested)}`);
      if (!response.ok) throw new Error(await apiError(response));
      const result = (await response.json()) as { workspace_root: string; sessions: SessionSummary[] };
      workspaceInput = result.workspace_root;
      sessions = result.sessions;
    } catch (cause) {
      onerror(cause instanceof Error ? cause.message : 'Could not inspect the repository');
    } finally {
      workspaceLoading = false;
    }
  }

  function selectRegistrationProject(projectID: string) {
    registrationProjectID = projectID;
    registrationProjectRoots = [...(projects.find((project) => project.id === projectID)?.workspace_roots || [])];
    registrationProjectRoot = registrationProjectRoots.length === 1 ? registrationProjectRoots[0] : '';
  }

  async function loadRegistrationProjectWorkspace() {
    if (!registrationProjectRoot) return;
    workspaceInput = registrationProjectRoot;
    await loadWorkspaceCandidates();
  }

  function chooseWorkspace() {
    directoryPickerOpen = true;
  }

  async function selectDirectory(path: string) {
    workspaceInput = path;
    directoryPickerOpen = false;
    await loadWorkspaceCandidates();
  }

  async function registerSession(sessionID = '', create = false) {
    await onregister(workspaceInput.trim(), sessionID, create);
  }

  onMount(() => {
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape' && !directoryPickerOpen) onclose();
    };
    window.addEventListener('keydown', onKeyDown);
    return () => window.removeEventListener('keydown', onKeyDown);
  });
</script>

<div class="directory-dialog-backdrop">
  <div class="directory-dialog registration-dialog" role="dialog" aria-modal="true" aria-labelledby="registration-dialog-title">
    <header>
      <div><span>SESSION REGISTRY</span><h2 id="registration-dialog-title">Add a root session</h2></div>
      <button class="dialog-close" title="Close" aria-label="Close session registry" onclick={() => onclose()}><X aria-hidden="true" size={18} /></button>
    </header>
    <div class="registration-workspace">
      <label for="registration-workspace"><span>Workspace directory</span><input id="registration-workspace" bind:value={workspaceInput} placeholder="/path/to/repository" onkeydown={(event) => event.key === 'Enter' && loadWorkspaceCandidates()} /></label>
      <button class="icon-button" title="Choose repository folder" aria-label="Choose repository folder" onclick={chooseWorkspace} disabled={workspaceLoading}><FolderOpen aria-hidden="true" size={16} /></button>
      <button class="secondary-button" onclick={loadWorkspaceCandidates} disabled={workspaceLoading || !workspaceInput.trim()}>Load sessions</button>
    </div>
    {#if projects.length}
      <div class="project-workspace-picker">
        <span>PROJECT WORKSPACES</span>
        <div>
          <label for="registration-project"><small>Project</small><select id="registration-project" value={registrationProjectID} onchange={(event) => selectRegistrationProject(event.currentTarget.value)}><option value="">Select a project</option>{#each projects as project}<option value={project.id}>{project.name}</option>{/each}</select></label>
          <label for="registration-project-workspace"><small>Workspace</small><select id="registration-project-workspace" bind:value={registrationProjectRoot} disabled={!registrationProjectID}><option value="">Select a workspace</option>{#each registrationProjectRoots as root}<option value={root}>{root}</option>{/each}</select></label>
          <button class="secondary-button" onclick={loadRegistrationProjectWorkspace} disabled={!registrationProjectRoot || workspaceLoading}>Load sessions</button>
        </div>
      </div>
    {/if}
    <div class="registration-candidates">
      {#if workspaceLoading}
        <div class="directory-state">Loading sessions…</div>
      {:else if workspaceInput && sessions.length}
        <button class="new-session-candidate" onclick={() => registerSession('', true)} disabled={busy}><Plus aria-hidden="true" size={16} /><span><strong>Create new session</strong><small>{workspaceInput}</small></span></button>
        {#each sessions as session}
          <button onclick={() => registerSession(session.session_id, false)} disabled={busy || registeredSessions.some((registration) => registration.workspace_root === workspaceInput && registration.session.session_id === session.session_id)}>
            <GitBranch aria-hidden="true" size={15} /><span><strong>{session.title || 'New session'}</strong><small>{formatSessionTime(session.updated_at)} · {shortID(session.session_id)}</small></span>
            <em>{registeredSessions.some((registration) => registration.workspace_root === workspaceInput && registration.session.session_id === session.session_id) ? 'Registered' : 'Add'}</em>
          </button>
        {/each}
      {:else if workspaceInput}
        <div class="registration-empty"><p>No saved sessions found in this workspace.</p><button class="primary-button" onclick={() => registerSession('', true)} disabled={busy}><Plus aria-hidden="true" size={15} /> Create new session</button></div>
      {:else}
        <div class="directory-state">Choose a workspace directory, then select an existing session or create a new one.</div>
      {/if}
    </div>
    <footer><div><span>REGISTRATION</span><code>Workspace data stays in the repository. Studio stores only the root session reference.</code></div><button class="secondary-button" onclick={() => onclose()}>Cancel</button></footer>
  </div>
</div>

{#if directoryPickerOpen}
  <DirectoryBrowser onselect={selectDirectory} onclose={() => directoryPickerOpen = false} />
{/if}
