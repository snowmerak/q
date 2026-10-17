<script lang="ts">
  import { ChevronDown, Folders, GitBranch, MessageSquarePlus, Plus, RefreshCw, Search, X } from '@lucide/svelte';
  import { formatSessionTime, shortID } from './format';
  import { flattenDelegations } from './tree';
  import type { FlatDelegation, RegisteredSessionTree, StudioProject } from './types';

  export let projects: StudioProject[];
  export let registeredSessions: RegisteredSessionTree[];
  export let selectedRegistrationID: string;
  export let selectedDelegationPath: string;
  export let workspaceLoading: boolean;
  export let sessionLoading: boolean;
  export let sending: boolean;
  export let onrefresh: () => void;
  export let onproject: (project?: StudioProject) => void;
  export let onregistration: () => void;
  export let onchat: () => void;
  export let onselect: (registration: RegisteredSessionTree) => void;
  export let onunregister: (registration: RegisteredSessionTree) => void;
  export let ondelegate: (registration: RegisteredSessionTree, node: FlatDelegation) => void;

  let search = '';
  let searchInput: HTMLInputElement;
  let collapsedGroups = new Set<string>();
  $: query = search.trim().toLocaleLowerCase();
  $: filteredSessions = registeredSessions.filter(registration => {
    const project = projects.find(project => project.id === registration.project_id);
    const title = registration.session.title || (registration.kind === 'chat' ? 'New chat' : 'New session');
    const terms = [title, registration.kind === 'chat' ? 'Personal chat' : '', registration.session.session_id, registration.workspace_root, project?.name,
      ...flattenDelegations(registration.delegations || [], 1).map(node => node.bookmark.agent)];
    return !query || terms.some(term => term?.toLocaleLowerCase().includes(query));
  });
  $: chats = filteredSessions.filter((registration) => registration.kind === 'chat');
  $: independents = filteredSessions.filter((registration) => !registration.project_id && registration.kind !== 'chat');
  $: visibleProjects = projects.filter(project => !query || project.name.toLocaleLowerCase().includes(query)
    || filteredSessions.some(session => session.project_id === project.id));

  function projectSessions(sessions: RegisteredSessionTree[], projectID: string) {
    return sessions.filter((registration) => registration.project_id === projectID);
  }

  function toggleGroup(id: string) {
    const next = new Set(collapsedGroups);
    if (next.has(id)) next.delete(id);
    else next.add(id);
    collapsedGroups = next;
  }

  function clearSearch() {
    search = '';
    searchInput?.focus();
  }
</script>

  <aside id="session-tree" class="session-rail" aria-label="Registered session tree">
    <div class="session-list-heading"><span>YOUR SESSIONS</span><div><button title="Refresh session tree" aria-label="Refresh session tree" onclick={onrefresh} disabled={workspaceLoading}><RefreshCw aria-hidden="true" size={14} /></button><button title="Create project" aria-label="Create project" onclick={() => onproject()} disabled={sessionLoading}><Folders aria-hidden="true" size={15} /></button></div></div>
    <div class="session-create-actions"><button class="primary-button" title="New chat" onclick={onchat} disabled={sessionLoading}><MessageSquarePlus aria-hidden="true" size={15} />New chat</button><button class="secondary-button" title="Add or create session" aria-label="Add or create session" onclick={onregistration} disabled={sessionLoading}><Plus aria-hidden="true" size={15} />New session</button></div>
    <div class="session-search"><Search aria-hidden="true" size={15} /><input bind:this={searchInput} bind:value={search} aria-label="Search sessions" placeholder="Search sessions…" onkeydown={(event) => { if (event.key === 'Escape') clearSearch(); }} />{#if search}<button aria-label="Clear session search" onclick={clearSearch}><X aria-hidden="true" size={14} /></button>{/if}</div>
    {#if query}<p class="session-search-count" role="status">{filteredSessions.length} {filteredSessions.length === 1 ? 'session' : 'sessions'} found</p>{/if}
    <div class="session-list session-tree" aria-busy={workspaceLoading}>
      {#snippet groupHeading(id: string, label: string, count: number)}
        <button class="session-group-toggle" aria-expanded={!!query || !collapsedGroups.has(id)} aria-controls={`session-group-${id}`} disabled={!!query} title={query ? 'Clear search to collapse groups' : undefined} onclick={() => toggleGroup(id)}>
          <ChevronDown class={query || !collapsedGroups.has(id) ? '' : 'group-chevron-collapsed'} aria-hidden="true" size={13} />
          {#if id === 'chats'}<MessageSquarePlus aria-hidden="true" size={13} />{:else if id === 'independent'}<GitBranch aria-hidden="true" size={13} />{:else}<Folders aria-hidden="true" size={13} />{/if}
          <strong>{label}</strong><small>{count}</small>
        </button>
      {/snippet}
      {#snippet sessionBranch(registration: RegisteredSessionTree)}
        <div class="session-list-item session-root" class:active={selectedRegistrationID === registration.registration_id && !selectedDelegationPath} class:broken={!!registration.issue}>
          <button class="session-select" aria-current={selectedRegistrationID === registration.registration_id && !selectedDelegationPath ? 'true' : undefined} onclick={() => !registration.issue && onselect(registration)} disabled={!!registration.issue}>
            <strong>{registration.session.title || (registration.kind === 'chat' ? 'New chat' : 'New session')}</strong>
            <small title={registration.kind === 'chat' ? undefined : registration.workspace_root}>{registration.kind === 'chat' ? 'Personal chat' : registration.workspace_root}</small>
            <code>{registration.issue || `${formatSessionTime(registration.session.updated_at)} · ${shortID(registration.session.session_id)}`}</code>
          </button>
          <button class="session-delete" title={registration.kind === 'chat' ? 'Delete chat' : 'Remove from Studio'} aria-label={registration.kind === 'chat' ? `Delete chat ${registration.session.title || ''}`.trim() : `Remove ${registration.session.title || 'session'} from Studio`} onclick={() => onunregister(registration)} disabled={sending || sessionLoading}><X aria-hidden="true" size={13} /></button>
        </div>
        {#each flattenDelegations(registration.delegations || [], 1) as node}
          <button class="session-child" class:active={selectedRegistrationID === registration.registration_id && selectedDelegationPath === node.path} style={`--tree-depth: ${node.depth}`} onclick={() => ondelegate(registration, node)} disabled={!!registration.issue}>
            <span class="tree-branch" aria-hidden="true"></span>
            <span><strong>{node.bookmark.agent.replace(/^builtin\//, '')}</strong><small>{node.state?.change_request ? `${node.state.status} · CR ${node.state.change_request.status}` : node.state?.status || 'recorded'}</small></span>
          </button>
        {/each}
      {/snippet}
      {#if workspaceLoading && !projects.length && !registeredSessions.length}
        <div class="session-list-empty" role="status"><p>Loading your sessions…</p></div>
      {:else if query && !filteredSessions.length && !visibleProjects.length}
        <div class="session-list-empty"><Search aria-hidden="true" size={23} /><strong>No sessions found</strong><p>Try a session title, project name, or workspace path.</p><button class="text-button" onclick={clearSearch}>Clear search</button></div>
      {:else if projects.length || registeredSessions.length}
        {#if chats.length}
          <div class="session-project-heading">{@render groupHeading('chats', 'Chats', chats.length)}</div>
          <div id="session-group-chats" hidden={!query && collapsedGroups.has('chats')}>
          {#each chats as registration}{@render sessionBranch(registration)}{/each}
          </div>
        {/if}
        {#each visibleProjects as project}
          {@const sessions = projectSessions(filteredSessions, project.id)}
          <div class="session-project-heading">{@render groupHeading(project.id, project.name, sessions.length)}<button title={`Edit ${project.name}`} aria-label={`Edit project ${project.name}`} onclick={() => onproject(project)}>Edit</button></div>
          <div id={`session-group-${project.id}`} hidden={!query && collapsedGroups.has(project.id)}>
          {#each sessions as registration}
            {@render sessionBranch(registration)}
          {:else}
            <div class="session-group-empty">No registered sessions</div>
          {/each}
          </div>
        {/each}
        {#if independents.length || !query}
        <div class="session-project-heading independent-heading">{@render groupHeading('independent', 'Independents', independents.length)}</div>
        <div id="session-group-independent" hidden={!query && collapsedGroups.has('independent')}>
        {#each independents as registration}
          {@render sessionBranch(registration)}
        {:else}
          <div class="session-group-empty">No independent sessions</div>
        {/each}
        </div>
        {/if}
      {:else}
        <div class="session-list-empty"><p>Start a chat or add a workspace session.</p><button class="primary-button" onclick={onchat} disabled={sessionLoading}><MessageSquarePlus aria-hidden="true" size={15} /> New chat</button><button class="secondary-button" onclick={onregistration}>Add session</button></div>
      {/if}
    </div>
  </aside>
