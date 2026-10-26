<script lang="ts">
  import { Activity, CircleHelp, Files, GitCompareArrows, House, Layers, PanelLeftClose, PanelLeftOpen, Settings } from '@lucide/svelte';
  import { onMount } from 'svelte';
  import ChangesView from './ChangesView.svelte';
  import FilesView from './FilesView.svelte';
  import HelpView from './HelpView.svelte';
  import OperationsView from './OperationsView.svelte';
  import OverviewView from './OverviewView.svelte';
  import SessionView from './SessionView.svelte';
  import SettingsView from './SettingsView.svelte';

  type StudioStatus = { version: number; service: string; ready: boolean };
  type ConnectionState =
    | { kind: 'loading' }
    | { kind: 'ready'; status: StudioStatus }
    | { kind: 'error'; message: string };
  type View = 'overview' | 'sessions' | 'councils' | 'composers' | 'files' | 'changes' | 'operations' | 'settings' | 'help';

  const navigation = [
    { label: 'Overview', icon: House, view: 'overview' as View },
    { label: 'Sessions', icon: Layers, view: 'sessions' as View },
    { label: 'Files', icon: Files, view: 'files' as View },
    { label: 'Changes', icon: GitCompareArrows, view: 'changes' as View },
    { label: 'Operations', icon: Activity, view: 'operations' as View },
    { label: 'Settings', icon: Settings, view: 'settings' as View },
    { label: 'Help', icon: CircleHelp, view: 'help' as View }
  ];
  let connection: ConnectionState = { kind: 'loading' };
  let activeView: View = 'overview';
  let sidebarCollapsed = true;
  let sessionView: SessionView | null = null;

  $: activeLabel = navigation.find(item => item.view === activeView)?.label || 'Overview';

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
    if (activeView === view) return;
    if (activeView === 'sessions' && sessionView?.canLeave() === false) return;
    activeView = view;
    const path = view === 'overview' ? '/' : `/${view}`;
    if (window.location.pathname !== path) window.history.pushState({}, '', path);
  }

  function openPath(path: string) {
    if (activeView === 'sessions' && sessionView?.canLeave() === false) return;
    window.history.pushState({}, '', path);
    activeView = viewFromLocation();
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
    if (window.location.pathname.startsWith('/councils')) return 'sessions';
    if (window.location.pathname.startsWith('/composers')) return 'sessions';
    if (window.location.pathname.startsWith('/files')) return 'files';
    if (window.location.pathname.startsWith('/changes')) return 'changes';
    if (window.location.pathname.startsWith('/operations')) return 'operations';
    if (window.location.pathname.startsWith('/help')) return 'help';
    return 'overview';
  }

  onMount(() => {
    try {
      sidebarCollapsed = localStorage.getItem('q-studio-sidebar-collapsed') !== 'false';
    } catch {
      // Use the collapsed default when browser storage is unavailable.
    }
    activeView = viewFromLocation();
    void loadStatus();
    const onPopState = () => {
      const next = viewFromLocation();
      if (activeView === 'sessions' && next !== 'sessions' && sessionView?.canLeave() === false) {
        window.history.pushState({}, '', sessionView.currentPath());
        return;
      }
      activeView = next;
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

<svelte:head><title>{activeLabel} · Q Studio</title><meta name="description" content="Q Studio local agent control surface" /></svelte:head>

<div class="app-shell" class:sidebar-collapsed={sidebarCollapsed}>
  <aside class="sidebar" aria-label="Studio navigation">
    <div class="brand">
      <span class="brand-mark">Q</span><span class="brand-name">Studio</span>
      <button class="sidebar-toggle" aria-label={sidebarCollapsed ? 'Expand sidebar' : 'Collapse sidebar'} title={sidebarCollapsed ? 'Expand sidebar' : 'Collapse sidebar'} aria-expanded={!sidebarCollapsed} aria-controls="studio-navigation" onclick={toggleSidebar}>
        {#if sidebarCollapsed}<PanelLeftOpen aria-hidden="true" size={18} />{:else}<PanelLeftClose aria-hidden="true" size={18} />{/if}
      </button>
    </div>
    <nav id="studio-navigation" aria-label="Main navigation">
      {#each [{ label: 'Workspace', items: navigation.slice(0, 4) }, { label: 'Manage', items: navigation.slice(4) }] as group}
      <div class="navigation-group">
        <span class="navigation-group-label">{group.label}</span>
      {#each group.items as item}
        {@const Icon = item.icon}
        <button
          class:active={item.view === activeView}
          aria-label={item.label}
          title={sidebarCollapsed ? item.label : undefined}
          aria-current={item.view === activeView ? 'page' : undefined}
          onclick={() => item.view && navigate(item.view)}
        ><Icon aria-hidden="true" size={19} strokeWidth={1.7} /><span>{item.label}</span></button>
      {/each}
      </div>
      {/each}
    </nav>
    <div class="navigation-hint"><kbd>Ctrl K</kbd><span>Focus navigation</span></div>
  </aside>

  <main class:settings-main={activeView === 'settings'} class:sessions-main={activeView === 'sessions'} class:composers-main={activeView === 'composers'} class:councils-main={activeView === 'councils'} class:changes-main={activeView === 'changes'} class:files-main={activeView === 'files'} class:help-main={activeView === 'help'}>
    <header class="page-header">
      <div><h1>{activeView === 'settings' ? 'Settings' : activeView === 'sessions' ? 'Sessions' : activeView === 'councils' ? 'Councils' : activeView === 'composers' ? 'Composer' : activeView === 'files' ? 'Files' : activeView === 'changes' ? 'Changes' : activeView === 'operations' ? 'Operations' : activeView === 'help' ? 'Help' : 'Studio overview'}</h1>{#if activeView === 'settings'}<p class="page-description">Studio appearance and global and repository configuration.</p>{:else if activeView === 'sessions'}<p class="page-description">Chat with Q or continue your workspace sessions.</p>{:else if activeView === 'composers'}<p class="page-description">Develop ideas through conversation with your chosen model.</p>{:else if activeView === 'councils'}<p class="page-description">Independent and repository councils with configurable members and chair.</p>{:else if activeView === 'files'}<p class="page-description">Browse workspace files and switch between raw content and Git diffs.</p>{:else if activeView === 'changes'}<p class="page-description">Inspect bounded diffs and review commits.</p>{:else if activeView === 'operations'}<p class="page-description">Usage, workers, local services, logs, and retention.</p>{:else if activeView === 'help'}<p class="page-description">Studio workflows, shortcuts, and recovery.</p>{/if}</div>
    </header>

    {#if activeView === 'overview'}
      {#if connection.kind === 'error'}<div class="overview-connection-error" role="alert"><span>{connection.message}</span><button class="retry" onclick={loadStatus}>Retry connection</button></div>{/if}
      <OverviewView onopen={openPath} />
    {:else if activeView === 'sessions'}
      <SessionView bind:this={sessionView} openChanges={openWorkspaceChanges} />
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

  <footer class="status-bar"><div><span>Q Studio</span><span class="divider" aria-hidden="true"></span><span class:online={connection.kind === 'ready'} class="status-dot" aria-hidden="true"></span><span>{connection.kind === 'ready' ? 'Ready' : connection.kind === 'error' ? 'Unavailable' : 'Connecting'}</span></div><span>{activeView === 'settings' ? 'Configuration' : activeView === 'sessions' ? 'Conversations' : activeView === 'councils' ? 'Model council' : activeView === 'composers' ? 'Idea development' : activeView === 'files' ? 'Workspace files' : activeView === 'changes' ? 'Repository changes' : activeView === 'operations' ? 'Runtime operations' : activeView === 'help' ? 'Studio guide' : 'Local'}</span></footer>
</div>
