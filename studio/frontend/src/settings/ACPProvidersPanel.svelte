<script lang="ts">
  import { CheckCircle2, RefreshCw } from '@lucide/svelte';
  import { onMount, onDestroy } from 'svelte';
  import { requestJSON } from '../api';
  import { prepareAgents } from '../subagents/catalog';
  import ACPAdaptersPanel from './ACPAdaptersPanel.svelte';
  import ConnectionsPanel from '../subagents/ConnectionsPanel.svelte';
  import { createSettingsOperations } from './operations';
  import type { AgentResponse } from '../subagents/types';

  let agents: AgentResponse | null = null;
  let workspaceRoot = '';
  let generation = 0;
  const operations = createSettingsOperations();
  function load() {
    const root = workspaceRoot.trim();
    const current = ++generation;
    operations.run(async () => {
      const value = prepareAgents(await requestJSON<AgentResponse>('GET', `/api/v1/settings/acp${root ? `?workspace_root=${encodeURIComponent(root)}` : ''}`));
      if (current === generation) agents = value;
    }, '');
  }
  onMount(() => { workspaceRoot = localStorage.getItem('q-studio-workspace-root') || ''; load(); });
  onDestroy(() => { generation++; });
</script>

<section class="settings-section">
  <div class="section-heading"><div><p class="eyebrow">ACP PROVIDERS</p><h2>ACP providers</h2><p>Connect external agents for Sessions, Councils, and subagents.</p></div><button class="icon-button" title="Reload ACP providers" onclick={load} disabled={$operations.busy}><RefreshCw size={16} /></button></div>
  <ACPAdaptersPanel />
  <div class="workspace-context settings-card"><label><span>Test workspace · optional</span><input bind:value={workspaceRoot} placeholder="Workspace directory for ACP connection tests" onkeydown={event => event.key === 'Enter' && load()} /></label><button class="text-button" onclick={load}>Load workspace</button></div>
  {#if $operations.error}<p class="inline-warning">{$operations.error}</p>{:else if $operations.message}<p class="integration-success"><CheckCircle2 size={14} /> {$operations.message}</p>{/if}
  {#if agents}<ConnectionsPanel bind:agents {operations} showBindings={false} endpoint="/api/v1/settings/acp" />{:else}<p>Loading ACP providers…</p>{/if}
</section>
