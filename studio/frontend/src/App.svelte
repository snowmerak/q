<script lang="ts">
  import { Activity, CircleHelp, Files, GitCompareArrows, House, Layers, MessagesSquare, PanelLeftClose, PanelLeftOpen, Settings } from '@lucide/svelte';
  import { onMount } from 'svelte';
  import ChangesView from './ChangesView.svelte';
  import CouncilView from './CouncilView.svelte';
  import FilesView from './FilesView.svelte';
  import HelpView from './HelpView.svelte';
  import OperationsView from './OperationsView.svelte';
  import SessionView from './SessionView.svelte';
  import SettingsView from './SettingsView.svelte';

  type StudioStatus = { version: number; service: string; ready: boolean };
  type ConnectionState =
    | { kind: 'loading' }
    | { kind: 'ready'; status: StudioStatus }
    | { kind: 'error'; message: string };
  type View = 'overview' | 'sessions' | 'councils' | 'files' | 'changes' | 'operations' | 'settings' | 'help';

  const navigation = [
    { label: 'Overview', icon: House, view: 'overview' as View },
    { label: 'Sessions', icon: Layers, view: 'sessions' as View },
    { label: 'Councils', icon: MessagesSquare, view: 'councils' as View },
    { label: 'Files', icon: Files, view: 'files' as View },
    { label: 'Changes', icon: GitCompareArrows, view: 'changes' as View },
    { label: 'Operations', icon: Activity, view: 'operations' as View },
    { label: 'Settings', icon: Settings, view: 'settings' as View },
    { label: 'Help', icon: CircleHelp, view: 'help' as View }
  ];
  let connection: ConnectionState = { kind: 'loading' };
  let activeView: View = 'overview';
  let sidebarCollapsed = false;

  async function loadStatus() {
    connection = { kind: 'loading' };
    try {
      const response = await fetch('/api/v1/status', { headers: { Accept: 'application/json' } });
      if (!response.ok) throw new Error(`Studio returned ${response.status}`);
      const status = (await response.json()) as StudioStatus;
      if (status.service !== 'studio' || !status.ready) throw new Error('Studio is not ready');
      connection = { kind: 'ready', status };
    } catch (error) {
      connection = { kind: 'error', message: error instanceof Error ? error.message : 'Studio is unavailable' };
    }
  }

  function toggleSidebar() {
    sidebarCollapsed = !sidebarCollapsed;
    try {
      localStorage.setItem('q-studio-sidebar-collapsed', String(sidebarCollapsed));
    } catch {
      // Keep the toggle usable when browser storage is unavailable.
    }
  }

  function navigate(view: View) {
    activeView = view;
    const path = view === 'overview' ? '/' : `/${view}`;
    if (window.location.pathname !== path) window.history.pushState({}, '', path);
  }

  function openWorkspaceChanges(workspaceRoot: string) {
    const root = workspaceRoot.trim();
    if (!root) return;
    localStorage.setItem('q-studio-workspace-root', root);
    activeView = 'changes';
    const url = new URL(window.location.href);
    url.pathname = '/changes';
    url.search = '';
    url.searchParams.set('workspace_root', root);
    window.history.pushState({}, '', url.pathname + url.search);
  }

  function viewFromLocation(): View {
    if (window.location.pathname.startsWith('/settings')) return 'settings';
    if (window.location.pathname.startsWith('/sessions')) return 'sessions';
    if (window.location.pathname.startsWith('/councils')) return 'councils';
    if (window.location.pathname.startsWith('/files')) return 'files';
    if (window.location.pathname.startsWith('/changes')) return 'changes';
    if (window.location.pathname.startsWith('/operations')) return 'operations';
    if (window.location.pathname.startsWith('/help')) return 'help';
    return 'overview';
  }

  onMount(() => {
    try {
      sidebarCollapsed = localStorage.getItem('q-studio-sidebar-collapsed') === 'true';
    } catch {
      // Use the expanded default when browser storage is unavailable.
    }
    activeView = viewFromLocation();
    void loadStatus();
    const onPopState = () => {
      activeView = viewFromLocation();
    };
    const onShortcut = (event: KeyboardEvent) => {
      const target = event.target as HTMLElement | null;
      const editing = !!target?.closest('input, textarea, select, [contenteditable="true"]');
      if ((event.ctrlKey || event.metaKey) && event.key.toLowerCase() === 'k') {
        event.preventDefault();
        const active = document.querySelector<HTMLButtonElement>('.sidebar nav button.active');
        (active || document.querySelector<HTMLButtonElement>('.sidebar nav button'))?.focus();
      } else if (!editing && event.key === '?') {
        event.preventDefault();
        navigate('help');
      }
    };
    window.addEventListener('popstate', onPopState);
    window.addEventListener('keydown', onShortcut);
    return () => {
      window.removeEventListener('popstate', onPopState);
      window.removeEventListener('keydown', onShortcut);
    };
  });
</script>

<svelte:head><meta name="description" content="Q Studio local agent control surface" /></svelte:head>

<div class="app-shell" class:sidebar-collapsed={sidebarCollapsed}>
  <aside class="sidebar" aria-label="Studio navigation">
    <div class="brand">
      <span class="brand-mark">Q</span><span class="brand-name">Studio</span>
      <button class="sidebar-toggle" aria-label={sidebarCollapsed ? 'Expand sidebar' : 'Collapse sidebar'} title={sidebarCollapsed ? 'Expand sidebar' : 'Collapse sidebar'} aria-expanded={!sidebarCollapsed} aria-controls="studio-navigation" onclick={toggleSidebar}>
        {#if sidebarCollapsed}<PanelLeftOpen aria-hidden="true" size={18} />{:else}<PanelLeftClose aria-hidden="true" size={18} />{/if}
      </button>
    </div>
    <nav id="studio-navigation">
      {#each navigation as item}
        {@const Icon = item.icon}
        <button
          class:active={item.view === activeView}
          aria-label={item.label}
          title={sidebarCollapsed ? item.label : undefined}
          aria-current={item.view === activeView ? 'page' : undefined}
          onclick={() => item.view && navigate(item.view)}
        ><Icon aria-hidden="true" size={19} strokeWidth={1.7} /><span>{item.label}</span></button>
      {/each}
    </nav>
  </aside>

  <main class:settings-main={activeView === 'settings'} class:sessions-main={activeView === 'sessions'} class:councils-main={activeView === 'councils'} class:changes-main={activeView === 'changes'} class:files-main={activeView === 'files'} class:help-main={activeView === 'help'}>
    <header class="page-header">
      <div><h1>{activeView === 'settings' ? 'Settings' : activeView === 'sessions' ? 'Sessions' : activeView === 'councils' ? 'Councils' : activeView === 'files' ? 'Files' : activeView === 'changes' ? 'Changes' : activeView === 'operations' ? 'Operations' : activeView === 'help' ? 'Help' : 'Studio overview'}</h1>{#if activeView === 'settings'}<p class="page-description">Global and repository configuration shared by Q sessions.</p>{:else if activeView === 'sessions'}<p class="page-description">Navigate registered root sessions and their delegated work.</p>{:else if activeView === 'councils'}<p class="page-description">Independent and repository councils with configurable members and chair.</p>{:else if activeView === 'files'}<p class="page-description">Browse workspace files and switch between raw content and Git diffs.</p>{:else if activeView === 'changes'}<p class="page-description">Inspect bounded diffs and review commits.</p>{:else if activeView === 'operations'}<p class="page-description">Usage, workers, local services, logs, and retention.</p>{:else if activeView === 'help'}<p class="page-description">Studio workflows, shortcuts, and recovery.</p>{/if}</div>
      <div class="connection" aria-live="polite"><span class:online={connection.kind === 'ready'} class="status-dot" aria-hidden="true"></span><span>{connection.kind === 'ready' ? 'Connected' : connection.kind === 'error' ? 'Disconnected' : 'Connecting'}</span></div>
    </header>

    {#if activeView === 'overview'}
      <section class="runtime-panel" aria-labelledby="runtime-heading">
        <h2 id="runtime-heading">Runtime</h2>
        <div class="runtime-body"><dl><div><dt>Local endpoint</dt><dd>{window.location.origin}</dd></div><div><dt>Status</dt><dd class:success={connection.kind === 'ready'}>{connection.kind === 'ready' ? 'Connected' : connection.kind === 'error' ? connection.message : 'Connecting…'}</dd></div></dl>{#if connection.kind === 'error'}<button class="retry" onclick={loadStatus}>Retry connection</button>{/if}</div>
      </section>
      <section class="empty-session" aria-labelledby="empty-heading"><div class="session-outline" aria-hidden="true"><span></span><span></span><span></span></div><h2 id="empty-heading">No session selected</h2><p>Open Sessions to register a root session and continue its conversation.</p><button class="primary-button overview-session-button" onclick={() => navigate('sessions')}>Open sessions</button></section>
    {:else if activeView === 'sessions'}
      <SessionView openChanges={openWorkspaceChanges} />
    {:else if activeView === 'councils'}
      <CouncilView />
    {:else if activeView === 'changes'}
      <ChangesView />
    {:else if activeView === 'files'}
      <FilesView />
    {:else if activeView === 'operations'}
      <OperationsView />
    {:else if activeView === 'help'}
      <HelpView />
    {/if}
    <SettingsView active={activeView === 'settings'} />
  </main>

  <footer class="status-bar"><div><span>Q Studio</span><span class="divider" aria-hidden="true"></span><span class:online={connection.kind === 'ready'} class="status-dot" aria-hidden="true"></span><span>{connection.kind === 'ready' ? 'Ready' : connection.kind === 'error' ? 'Unavailable' : 'Connecting'}</span></div><span>{activeView === 'settings' ? 'Configuration' : activeView === 'sessions' ? 'Repository session' : activeView === 'councils' ? 'Model council' : activeView === 'files' ? 'Workspace files' : activeView === 'changes' ? 'Repository changes' : activeView === 'operations' ? 'Runtime operations' : activeView === 'help' ? 'Studio guide' : 'Local'}</span></footer>
</div>
