<script lang="ts">
  import { FolderOpen, Folders, GitBranch, MessageSquare, Plus, X } from '@lucide/svelte';
  import { onMount, tick } from 'svelte';
  import DirectoryBrowser from './DirectoryBrowser.svelte';
  import { apiError } from '../api';
  import { formatSessionTime, shortID } from './format';
  import type { RegisteredSessionTree, SessionSummary, StudioProject } from './types';

  export let projects: StudioProject[] = [];
  export let registeredSessions: RegisteredSessionTree[] = [];
  export let busy = false;
  export let onregister: (root: string, sessionID: string, create: boolean) => Promise<void> = async () => {};
  export let onchat: () => Promise<void> = async () => {};
  export let onclose: () => void = () => {};
  export let onerror: (message: string) => void = () => {};

  const tabs = [
    { id: 'chat', label: 'Chat', icon: MessageSquare },
    { id: 'workspace', label: 'Workspace', icon: FolderOpen },
    { id: 'project', label: 'Project', icon: Folders }
  ] as const;
  type Mode = typeof tabs[number]['id'];
  let mode: Mode = 'chat';
  let workspaceInput = '';
  let workspaceSessions: SessionSummary[] = [];
  let workspaceLoadedRoot = '';
  let projectSessions: SessionSummary[] = [];
  let projectLoadedRoot = '';
  let registrationProjectID = '';
  let registrationProjectRoots: string[] = [];
  let registrationProjectRoot = '';
  let workspaceLoading = false;
  let loadError = '';
  let directoryPickerOpen = false;
  let generation = 0;
  let requestController: AbortController | null = null;

  $: selectedRoot = mode === 'project' ? registrationProjectRoot : workspaceInput.trim();
  $: loadedRoot = mode === 'project' ? projectLoadedRoot : workspaceLoadedRoot;
  $: sessions = mode === 'project' ? projectSessions : workspaceSessions;
  $: loaded = !!selectedRoot && selectedRoot === loadedRoot;

  function cancelLookup() {
    generation += 1;
    requestController?.abort();
    requestController = null;
    workspaceLoading = false;
    loadError = '';
  }

  function selectMode(next: Mode) {
    if (mode === next) return;
    cancelLookup();
    mode = next;
    onerror('');
  }

  async function navigateTabs(event: KeyboardEvent) {
    const index = tabs.findIndex(tab => tab.id === mode);
    let next = index;
    if (event.key === 'ArrowRight') next = (index + 1) % tabs.length;
    else if (event.key === 'ArrowLeft') next = (index + tabs.length - 1) % tabs.length;
    else if (event.key === 'Home') next = 0;
    else if (event.key === 'End') next = tabs.length - 1;
    else return;
    event.preventDefault();
    const list = (event.currentTarget as HTMLElement).parentElement;
    selectMode(tabs[next].id);
    await tick();
    list?.querySelectorAll<HTMLButtonElement>('[role=tab]')[next]?.focus();
  }

  async function loadWorkspaceCandidates() {
    const requested = selectedRoot;
    if (!requested || mode === 'chat') return;
    cancelLookup();
    const current = generation;
    const source = mode;
    const controller = new AbortController();
    requestController = controller;
    workspaceLoading = true;
    onerror('');
    try {
      const response = await fetch(`/api/v1/sessions?workspace_root=${encodeURIComponent(requested)}`, { signal: controller.signal });
      if (!response.ok) throw new Error(await apiError(response));
      const result = (await response.json()) as { workspace_root: string; sessions: SessionSummary[] };
      if (current !== generation || selectedRoot !== requested) return;
      if (source === 'workspace') {
        workspaceInput = result.workspace_root;
        workspaceLoadedRoot = result.workspace_root;
        workspaceSessions = result.sessions;
      } else {
        projectLoadedRoot = result.workspace_root;
        projectSessions = result.sessions;
      }
    } catch (cause) {
      if (current === generation && selectedRoot === requested && !controller.signal.aborted) {
        loadError = cause instanceof Error ? cause.message : 'Could not load sessions';
      }
    } finally {
      if (current === generation) {
        workspaceLoading = false;
        requestController = null;
      }
    }
  }

  function selectRegistrationProject(projectID: string) {
    cancelLookup();
    registrationProjectID = projectID;
    registrationProjectRoots = [...(projects.find(project => project.id === projectID)?.workspace_roots || [])];
    registrationProjectRoot = registrationProjectRoots.length === 1 ? registrationProjectRoots[0] : '';
    projectLoadedRoot = '';
    projectSessions = [];
  }

  async function selectDirectory(path: string) {
    workspaceInput = path;
    directoryPickerOpen = false;
    await tick();
    await loadWorkspaceCandidates();
  }

  async function registerSession(sessionID = '', create = false) {
    if (!loaded || workspaceLoading) return;
    await onregister(selectedRoot, sessionID, create);
  }

  onMount(() => {
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape' && !directoryPickerOpen) onclose();
    };
    window.addEventListener('keydown', onKeyDown);
    return () => {
      requestController?.abort();
      window.removeEventListener('keydown', onKeyDown);
    };
  });
</script>

<div class="directory-dialog-backdrop">
  <div class="directory-dialog registration-dialog" role="dialog" aria-modal="true" aria-labelledby="registration-dialog-title">
    <header>
      <div><span>SESSIONS</span><h2 id="registration-dialog-title">New session</h2></div>
      <button class="dialog-close" title="Close" aria-label="Close session registry" onclick={() => onclose()}><X aria-hidden="true" size={18} /></button>
    </header>
    <div class="registration-tabs" role="tablist" aria-label="Session type">
      {#each tabs as tab}
        <button role="tab" id={`registration-tab-${tab.id}`} aria-controls="registration-panel" aria-selected={mode === tab.id} tabindex={mode === tab.id ? 0 : -1} class:active={mode === tab.id} onclick={() => selectMode(tab.id)} onkeydown={navigateTabs} disabled={busy}><tab.icon aria-hidden="true" size={16} />{tab.label}</button>
      {/each}
    </div>
    <div class="registration-panel" id="registration-panel" role="tabpanel" aria-labelledby={`registration-tab-${mode}`} tabindex="0">
      {#if mode === 'chat'}
        <div class="registration-chat">
          <div class="registration-chat-icon"><MessageSquare aria-hidden="true" size={25} /></div>
          <h3>Chat with Q</h3>
          <p>Start a conversation without choosing a project or folder.</p>
          <small>Your conversations stay here until you delete them.</small>
        </div>
      {:else}
        {#if mode === 'workspace'}
          <div class="registration-workspace">
            <label for="registration-workspace"><span>Workspace directory</span><input id="registration-workspace" bind:value={workspaceInput} placeholder="/path/to/workspace" onkeydown={(event) => event.key === 'Enter' && loadWorkspaceCandidates()} /></label>
            <button class="icon-button" title="Choose repository folder" aria-label="Choose repository folder" onclick={() => directoryPickerOpen = true} disabled={workspaceLoading}><FolderOpen aria-hidden="true" size={16} /></button>
            <button class="secondary-button" onclick={loadWorkspaceCandidates} disabled={workspaceLoading || !workspaceInput.trim()}>Load sessions</button>
          </div>
        {:else if projects.length}
          <div class="project-workspace-picker">
            <div>
              <label for="registration-project"><small>Project</small><select id="registration-project" value={registrationProjectID} onchange={(event) => selectRegistrationProject(event.currentTarget.value)}><option value="">Select a project</option>{#each projects as project}<option value={project.id}>{project.name}</option>{/each}</select></label>
              <label for="registration-project-workspace"><small>Workspace</small><select id="registration-project-workspace" bind:value={registrationProjectRoot} disabled={!registrationProjectID}><option value="">Select a workspace</option>{#each registrationProjectRoots as root}<option value={root}>{root}</option>{/each}</select></label>
              <button class="secondary-button" onclick={loadWorkspaceCandidates} disabled={!registrationProjectRoot || workspaceLoading}>Load sessions</button>
            </div>
          </div>
        {/if}
        {#if loadError}<p class="registration-error" role="alert">{loadError}</p>{/if}
        <div class="registration-candidates" aria-busy={workspaceLoading}>
          {#if workspaceLoading}
            <div class="directory-state">Loading sessions…</div>
          {:else if mode === 'project' && !projects.length}
            <div class="registration-empty"><Folders aria-hidden="true" size={24} /><h3>No projects yet</h3><p>Add a project in Sessions to choose one here.</p></div>
          {:else if loaded && sessions.length}
            <button class="new-session-candidate" onclick={() => registerSession('', true)} disabled={busy}><Plus aria-hidden="true" size={16} /><span><strong>Create new session</strong><small>{selectedRoot}</small></span></button>
            {#each sessions as session}
              <button onclick={() => registerSession(session.session_id, false)} disabled={busy || registeredSessions.some(registration => registration.workspace_root === selectedRoot && registration.session.session_id === session.session_id)}>
                <GitBranch aria-hidden="true" size={15} /><span><strong>{session.title || 'New session'}</strong><small>{formatSessionTime(session.updated_at)} · {shortID(session.session_id)}</small></span>
                <em>{registeredSessions.some(registration => registration.workspace_root === selectedRoot && registration.session.session_id === session.session_id) ? 'Registered' : 'Add'}</em>
              </button>
            {/each}
          {:else if loaded}
            <div class="registration-empty"><p>No saved sessions in this workspace yet.</p><button class="primary-button" onclick={() => registerSession('', true)} disabled={busy}><Plus aria-hidden="true" size={15} /> Create new session</button></div>
          {:else}
            <div class="directory-state">{selectedRoot ? 'Load sessions to continue or create a new session.' : mode === 'project' ? 'Choose a project and workspace to find its sessions.' : 'Choose a folder to find its sessions.'}</div>
          {/if}
        </div>
      {/if}
    </div>
    <footer>
      <div><p class="registration-footer-note">{mode === 'chat' ? 'No workspace needed.' : 'Continue an existing session or create a new one.'}</p></div>
      <button class="secondary-button" onclick={() => onclose()}>Cancel</button>
      {#if mode === 'chat'}<button class="primary-button" onclick={onchat} disabled={busy}><Plus aria-hidden="true" size={15} /> New chat</button>{/if}
    </footer>
  </div>
</div>

{#if directoryPickerOpen}
  <DirectoryBrowser onselect={selectDirectory} onclose={() => directoryPickerOpen = false} />
{/if}
