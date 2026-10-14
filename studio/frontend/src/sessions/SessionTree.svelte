<script lang="ts">
  import { Folders, GitBranch, MessageSquarePlus, Plus, RefreshCw, X } from '@lucide/svelte';
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

  $: chats = registeredSessions.filter((registration) => registration.kind === 'chat');
  $: independents = registeredSessions.filter((registration) => !registration.project_id && registration.kind !== 'chat');

  function projectSessions(sessions: RegisteredSessionTree[], projectID: string) {
    return sessions.filter((registration) => registration.project_id === projectID);
  }
</script>

  <aside class="session-rail" aria-label="Registered session tree">
    <div class="session-list-heading"><span>SESSION TREE</span><div><button title="New chat" onclick={onchat} disabled={sessionLoading}><MessageSquarePlus aria-hidden="true" size={15} /></button><button title="Refresh session tree" onclick={onrefresh} disabled={workspaceLoading}><RefreshCw aria-hidden="true" size={14} /></button><button title="Create project" onclick={() => onproject()} disabled={sessionLoading}><Folders aria-hidden="true" size={15} /></button><button title="Add or create session" onclick={onregistration} disabled={sessionLoading}><Plus aria-hidden="true" size={15} /></button></div></div>
    <div class="session-list session-tree">
      {#snippet sessionBranch(registration: RegisteredSessionTree)}
        <div class="session-list-item session-root" class:active={selectedRegistrationID === registration.registration_id && !selectedDelegationPath} class:broken={!!registration.issue}>
          <button class="session-select" onclick={() => !registration.issue && onselect(registration)} disabled={!!registration.issue}>
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
      {#if projects.length || registeredSessions.length}
        {#if chats.length}
          <div class="session-project-heading"><span><MessageSquarePlus aria-hidden="true" size={13} /><strong>Chats</strong><small>{chats.length}</small></span></div>
          {#each chats as registration}{@render sessionBranch(registration)}{/each}
        {/if}
        {#each projects as project}
          <div class="session-project-heading"><span><Folders aria-hidden="true" size={13} /><strong>{project.name}</strong><small>{project.workspace_roots.length}</small></span><button title={`Edit ${project.name}`} aria-label={`Edit project ${project.name}`} onclick={() => onproject(project)}>Edit</button></div>
          {#each projectSessions(registeredSessions, project.id) as registration}
            {@render sessionBranch(registration)}
          {:else}
            <div class="session-group-empty">No registered sessions</div>
          {/each}
        {/each}
        <div class="session-project-heading independent-heading"><span><GitBranch aria-hidden="true" size={13} /><strong>Independents</strong><small>{independents.length}</small></span></div>
        {#each independents as registration}
          {@render sessionBranch(registration)}
        {:else}
          <div class="session-group-empty">No independent sessions</div>
        {/each}
      {:else}
        <div class="session-list-empty"><p>Start a chat or add a workspace session.</p><button class="primary-button" onclick={onchat} disabled={sessionLoading}><MessageSquarePlus aria-hidden="true" size={15} /> New chat</button><button class="secondary-button" onclick={onregistration}>Add session</button></div>
      {/if}
    </div>
  </aside>
