<script lang="ts">
  import { CheckCircle2, RefreshCw } from '@lucide/svelte';
  import { onMount } from 'svelte';
  import MCPPanel from './integrations/MCPPanel.svelte';
  import LSPPanel from './integrations/LSPPanel.svelte';
  import SkillsPanel from './integrations/SkillsPanel.svelte';
  import IgnorePanel from './integrations/IgnorePanel.svelte';
  import { createIntegrationOperations } from './integrations/operations';

  type Panel = 'mcp' | 'lsp' | 'skills' | 'ignore';
  const panels: { id: Panel; label: string; detail: string }[] = [
    { id: 'mcp', label: 'MCP', detail: 'External tool servers and role grants' },
    { id: 'lsp', label: 'Language servers', detail: 'Profiles, defaults, and project roots' },
    { id: 'skills', label: 'Skills', detail: 'Global and repository skill indexes' },
    { id: 'ignore', label: '.qignore', detail: 'Repository discovery exclusions' }
  ];


  let panel: Panel = 'mcp';
  let workspaceRoot = '';
  let repositoryRoot = '';
  let ready = false;
  let reloadToken = 0;
  const operations = createIntegrationOperations();

  function loadPanel() {
    repositoryRoot = workspaceRoot.trim();
    if (repositoryRoot) localStorage.setItem('q-studio-workspace-root', repositoryRoot);
    reloadToken += 1;
  }

  function choosePanel(next: Panel) {
    panel = next;
    loadPanel();
  }

  onMount(() => {
    const location = new URL(window.location.href);
    workspaceRoot = location.searchParams.get('workspace_root') || localStorage.getItem('q-studio-workspace-root') || '';
    repositoryRoot = workspaceRoot.trim();
    const requestedPanel = location.searchParams.get('panel') as Panel | null;
    if (requestedPanel && panels.some((candidate) => candidate.id === requestedPanel)) panel = requestedPanel;
    ready = true;
  });
</script>

<section class="settings-section integration-manager">
  <div class="section-heading">
    <div><p class="eyebrow">INTEGRATIONS</p><h2>Runtime connections</h2><p>Configure tool, language, skill, and discovery controls.</p></div>
    <button class="icon-button" title="Reload current panel" onclick={loadPanel} disabled={$operations.busy}><RefreshCw size={16} class={$operations.busy ? 'spin' : undefined} /></button>
  </div>
  <div class="workspace-context settings-card">
    <label><span>Repository path</span><input bind:value={workspaceRoot} placeholder="Open a repository in Sessions" /></label>
    <button class="text-button" onclick={loadPanel}>Load repository</button>
  </div>
  <div class="integration-tabs" role="tablist">
    {#each panels as item}<button class:active={panel === item.id} onclick={() => choosePanel(item.id)}><strong>{item.label}</strong><small>{item.detail}</small></button>{/each}
  </div>
  {#if $operations.error}<p class="inline-warning integration-message">{$operations.error}</p>{:else if $operations.message}<p class="integration-success integration-message"><CheckCircle2 size={14} /> {$operations.message}</p>{/if}

  <MCPPanel active={ready && panel === 'mcp'} {reloadToken} {operations} />
  <LSPPanel active={ready && panel === 'lsp'} workspaceRoot={repositoryRoot} {reloadToken} {operations} />
  <SkillsPanel active={ready && panel === 'skills'} workspaceRoot={repositoryRoot} {reloadToken} {operations} />
  <IgnorePanel active={ready && panel === 'ignore'} workspaceRoot={repositoryRoot} {reloadToken} {operations} />
</section>
