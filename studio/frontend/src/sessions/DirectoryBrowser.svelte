<script lang="ts">
  import { ChevronUp, Folder, HardDrive, Home, X } from '@lucide/svelte';
  import { onMount } from 'svelte';
  import { apiError } from '../api';

  export let kind: 'registration' | 'project' = 'registration';
  export let onselect: (path: string) => void | Promise<void> = () => {};
  export let onclose: () => void = () => {};

  type DirectoryEntry = { name: string; path: string };
  type DirectoryListing = {
    home: string;
    current: string;
    parent?: string;
    roots: string[];
    directories: DirectoryEntry[];
    can_select: boolean;
  };

  let directoryLoading = false;
  let directoryError = '';
  let directoryPathInput = '';
  let directoryListing: DirectoryListing | null = null;

  async function selectBrowsedDirectory() {
    if (directoryListing?.can_select) await onselect(directoryListing.current);
  }

  onMount(() => {
    void browseDirectory();
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key !== 'Escape') return;
      // Escape closes the folder browser before its owning dialog.
      event.stopImmediatePropagation();
      onclose();
    };
    window.addEventListener('keydown', onKeyDown);
    return () => window.removeEventListener('keydown', onKeyDown);
  });

  async function browseDirectory(path = '') {
    directoryLoading = true;
    directoryError = '';
    try {
      const query = path.trim() ? `?path=${encodeURIComponent(path.trim())}` : '';
      const response = await fetch(`/api/v1/directories${query}`, { headers: { Accept: 'application/json' } });
      if (!response.ok) throw new Error(await apiError(response));
      directoryListing = (await response.json()) as DirectoryListing;
      directoryPathInput = directoryListing.current;
    } catch (cause) {
      directoryError = cause instanceof Error ? cause.message : 'Could not browse this directory';
    } finally {
      directoryLoading = false;
    }
  }
</script>

<div class="directory-dialog-backdrop">
  <div class="directory-dialog" role="dialog" aria-modal="true" aria-labelledby="directory-dialog-title">
    <header>
      <div><span>{kind === 'project' ? 'PROJECT WORKSPACE' : 'REPOSITORY'}</span><h2 id="directory-dialog-title">Choose a folder</h2></div>
      <button class="dialog-close" title="Close" aria-label="Close folder browser" onclick={onclose}><X aria-hidden="true" size={18} /></button>
    </header>

    <div class="directory-toolbar">
      <button title="Home" aria-label="Go to home directory" onclick={() => browseDirectory(directoryListing?.home || '')} disabled={directoryLoading}><Home aria-hidden="true" size={16} /></button>
      <button title="Parent directory" aria-label="Go to parent directory" onclick={() => directoryListing?.parent && browseDirectory(directoryListing.parent)} disabled={directoryLoading || !directoryListing?.parent}><ChevronUp aria-hidden="true" size={17} /></button>
      <input aria-label="Directory path" bind:value={directoryPathInput} onkeydown={(event) => event.key === 'Enter' && browseDirectory(directoryPathInput)} />
      <button class="browse-button" onclick={() => browseDirectory(directoryPathInput)} disabled={directoryLoading}>Go</button>
    </div>

    {#if directoryListing && directoryListing.roots.length > 1}
      <div class="directory-roots">
        {#each directoryListing.roots as root}
          <button class:active={directoryListing.current.startsWith(root)} onclick={() => browseDirectory(root)} disabled={directoryLoading}><HardDrive aria-hidden="true" size={13} /> {root}</button>
        {/each}
      </div>
    {/if}

    <div class="directory-list" aria-busy={directoryLoading}>
      {#if directoryLoading && !directoryListing}
        <div class="directory-state">Loading folders…</div>
      {:else if directoryError}
        <div class="directory-state error">{directoryError}</div>
      {:else if directoryListing}
        {#each directoryListing.directories as directory}
          <button onclick={() => browseDirectory(directory.path)} disabled={directoryLoading}>
            <Folder aria-hidden="true" size={17} /><span>{directory.name}</span>
          </button>
        {:else}
          <div class="directory-state">This folder has no subdirectories.</div>
        {/each}
      {/if}
    </div>

    <footer>
      <div>
        <span>SELECTED FOLDER</span>
        <code>{directoryListing?.current || 'Loading…'}</code>
        {#if directoryListing && !directoryListing.can_select}<small>Choose a folder inside the home directory.</small>{/if}
      </div>
      <button class="secondary-button" onclick={onclose}>Cancel</button>
      <button class="primary-button" onclick={selectBrowsedDirectory} disabled={directoryLoading || !directoryListing?.can_select}>Choose folder</button>
    </footer>
  </div>
</div>
