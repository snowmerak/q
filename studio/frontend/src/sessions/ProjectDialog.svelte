<script lang="ts">
  import { Folder, FolderOpen, X } from '@lucide/svelte';
  import { onMount } from 'svelte';
  import DirectoryBrowser from './DirectoryBrowser.svelte';
  import { apiError } from '../api';
  import type { StudioProject } from './types';

  export let project: StudioProject | undefined = undefined;
  export let workspaceRoot = '';
  export let onsaved: (status: string) => Promise<void> = async () => {};
  export let onclose: () => void = () => {};

  let editingProjectID = project?.id || '';
  let projectName = project?.name || '';
  let projectRoots = project ? [...project.workspace_roots] : (workspaceRoot ? [workspaceRoot] : []);
  let projectRootInput = '';
  let projectSaving = false;
  let projectDialogError = '';
  let directoryPickerOpen = false;

  function chooseProjectWorkspace() {
    directoryPickerOpen = true;
  }

  function selectDirectory(path: string) {
    projectRootInput = path;
    directoryPickerOpen = false;
  }

  function addProjectWorkspace() {
    const value = projectRootInput.trim();
    if (!value) return;
    if (!projectRoots.includes(value)) {
      projectRoots = [...projectRoots, value];
    }
    projectRootInput = '';
  }

  function removeProjectWorkspace(index: number) {
    projectRoots = projectRoots.filter((_, candidate) => candidate !== index);
  }

  async function saveProject() {
    if (projectSaving) return;
    addProjectWorkspace();
    if (!projectName.trim() || !projectRoots.length) return;
    projectSaving = true;
    projectDialogError = '';
    try {
      const target = editingProjectID ? `/api/v1/projects/${encodeURIComponent(editingProjectID)}` : '/api/v1/projects';
      const response = await fetch(target, {
        method: editingProjectID ? 'PUT' : 'POST', headers: { 'Content-Type': 'application/json', Accept: 'application/json' },
        body: JSON.stringify({ name: projectName.trim(), workspace_roots: projectRoots })
      });
      if (!response.ok) throw new Error(await apiError(response));
      await onsaved(editingProjectID ? 'Project updated' : 'Project created');
    } catch (cause) {
      projectDialogError = cause instanceof Error ? cause.message : 'Could not save the project';
    } finally {
      projectSaving = false;
    }
  }

  async function deleteProject() {
    if (!editingProjectID || projectSaving) return;
    if (!window.confirm(`Delete project ${projectName}? Its sessions and workspace data will remain available as independents.`)) return;
    projectSaving = true;
    projectDialogError = '';
    try {
      const response = await fetch(`/api/v1/projects/${encodeURIComponent(editingProjectID)}`, { method: 'DELETE' });
      if (!response.ok) throw new Error(await apiError(response));
      await onsaved('Project deleted');
    } catch (cause) {
      projectDialogError = cause instanceof Error ? cause.message : 'Could not delete the project';
    } finally {
      projectSaving = false;
    }
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
  <div class="directory-dialog workspace-dialog" role="dialog" aria-modal="true" aria-labelledby="project-dialog-title">
    <header>
      <div><span>STUDIO PROJECT</span><h2 id="project-dialog-title">{editingProjectID ? 'Edit project' : 'Create project'}</h2></div>
      <button class="dialog-close" title="Close" aria-label="Close project settings" onclick={() => onclose()}><X aria-hidden="true" size={18} /></button>
    </header>
    <div class="workspace-dialog-body">
      {#if projectDialogError}<div class="workspace-dialog-error" role="alert">{projectDialogError}</div>{/if}
      <label><span>PROJECT NAME</span><input bind:value={projectName} placeholder="Project name" /></label>
      <p>Registered sessions whose primary workspace matches one of these directories appear under this project.</p>
      <div class="auxiliary-heading"><div><span>WORKSPACE DIRECTORIES</span><p>Each session keeps its own primary directory and receives the other project directories as auxiliary workspaces.</p></div><em>{projectRoots.length}/{32}</em></div>
      <div class="auxiliary-create">
        <input bind:value={projectRootInput} placeholder="/path/to/workspace" onkeydown={(event) => event.key === 'Enter' && addProjectWorkspace()} />
        <button class="icon-button" title="Choose workspace folder" aria-label="Choose workspace folder" onclick={chooseProjectWorkspace}><FolderOpen aria-hidden="true" size={16} /></button>
        <button class="secondary-button" onclick={addProjectWorkspace} disabled={!projectRootInput.trim()}>Add</button>
      </div>
      <div class="auxiliary-list">
        {#each projectRoots as root, index}
          <div><Folder aria-hidden="true" size={15} /><code title={root}>{root}</code><button title="Remove workspace" aria-label={`Remove ${root}`} onclick={() => removeProjectWorkspace(index)}><X aria-hidden="true" size={14} /></button></div>
        {:else}
          <div class="auxiliary-empty">Add at least one workspace directory.</div>
        {/each}
      </div>
    </div>
    <footer class="project-dialog-footer"><div><span>PROJECT CONTEXT</span><code>Workspace changes apply to project sessions on their next turn.</code></div>{#if editingProjectID}<button class="danger-button" onclick={deleteProject} disabled={projectSaving}>Delete</button>{/if}<button class="secondary-button" onclick={() => onclose()}>Cancel</button><button class="primary-button" onclick={saveProject} disabled={projectSaving || !projectName.trim() || (!projectRoots.length && !projectRootInput.trim()) || projectRoots.length > 32}>{projectSaving ? 'Saving…' : 'Save'}</button></footer>
  </div>
</div>

{#if directoryPickerOpen}
  <DirectoryBrowser kind="project" onselect={selectDirectory} onclose={() => directoryPickerOpen = false} />
{/if}
