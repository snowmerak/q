<script lang="ts">
  import { uiStorage } from './uiStorage';
  import { CheckCircle2, RefreshCw } from '@lucide/svelte';
  import { onDestroy, onMount } from 'svelte';
  import { requestJSON } from './api';
  import { prepareAgents } from './subagents/catalog';
  import { createSettingsOperations } from './settings/operations';
  import ProfilesPanel from './subagents/ProfilesPanel.svelte';
  import BuiltinProfiles from './subagents/BuiltinProfiles.svelte';
  import type { ModelOption } from './settings/types';
  import type { AgentResponse } from './subagents/types';

  type Tab = 'profiles' | 'builtins';

  const tabs: { id: Tab; label: string; detail: string }[] = [
    { id: 'profiles', label: 'Custom profiles', detail: 'Roles, tools, and delegation grants' },
    { id: 'builtins', label: 'Built in', detail: 'Occupational roles shipped with Q' }
  ];

  let tab: Tab = 'profiles';
  let workspaceRoot = '';
  let agents: AgentResponse | null = null;
  let models: ModelOption[] = [];
  let modelError = '';
  let destroyed = false;
  let loadGeneration = 0;
  const operations = createSettingsOperations();

  function rootValue() {
    return workspaceRoot.trim();
  }

  function rememberWorkspace() {
    const root = rootValue();
    if (root) uiStorage.setItem('q-studio-workspace-root', root);
    else uiStorage.removeItem('q-studio-workspace-root');
    return root;
  }

  function loadAgents() {
    const root = rememberWorkspace();
    const generation = ++loadGeneration;
    const query = root ? `?workspace_root=${encodeURIComponent(root)}` : '';
    agents = null;
    operations.run(async () => {
      const result = prepareAgents(await requestJSON<AgentResponse>('GET', `/api/v1/settings/subagents${query}`));
      if (generation === loadGeneration) agents = result;
    }, '');
  }

  function loadModels() {
    void requestJSON<{ models: ModelOption[] }>('GET', '/api/v1/settings/models')
      .then(result => { if (!destroyed) { models = result.models; modelError = ''; } })
      .catch(() => { if (!destroyed) modelError = 'API models are unavailable. Check Providers → API Provider.'; });
  }

  onDestroy(() => { loadGeneration += 1; destroyed = true; });

  onMount(() => {
    const location = new URL(window.location.href);
    workspaceRoot = location.searchParams.get('workspace_root') || uiStorage.getItem('q-studio-workspace-root') || '';
    void loadAgents();
    loadModels();
  });
</script>

<section class="settings-section subagent-manager">
  <div class="section-heading">
    <div><p class="eyebrow">SUBAGENTS</p><h2>Delegation profiles</h2><p>Choose models or ACP connections and configure custom profile tools.</p></div>
    <button class="icon-button" title="Reload subagent settings" onclick={() => { loadAgents(); loadModels(); }} disabled={$operations.busy}><RefreshCw size={16} class={$operations.busy ? 'spin' : undefined} /></button>
  </div>
  <code class="config-path">{agents?.config_path || '~/.q/config.yaml'}</code>

  <div class="workspace-context settings-card">
    <label><span>Repository context · optional</span><input bind:value={workspaceRoot} placeholder="Load repository profiles" onkeydown={(event) => event.key === 'Enter' && loadAgents()} /></label>
    <button class="text-button" onclick={loadAgents}>{rootValue() ? 'Load repository' : 'Use global only'}</button>
  </div>
  <p class="context-note">Global profiles are always available. A repository path adds its <code>.q/subagents</code> profiles. Manage ACP connections in Providers → ACP.</p>

  <div class="integration-tabs subagent-tabs" role="tablist">
    {#each tabs as item}<button class:active={tab === item.id} onclick={() => tab = item.id}><strong>{item.label}</strong><small>{item.detail}</small></button>{/each}
  </div>
  {#if $operations.error}<p class="inline-warning integration-message">{$operations.error}</p>{:else if $operations.message}<p class="integration-success integration-message"><CheckCircle2 size={14} /> {$operations.message}</p>{/if}
  {#if modelError}<p class="inline-warning integration-message">{modelError}</p>{/if}

  {#if !agents}
    <div class="loading-panel">Loading subagent settings…</div>
  {:else}
    <div hidden={tab !== 'profiles'}><ProfilesPanel bind:agents {models} {operations} /></div>
    <div hidden={tab !== 'builtins'}><BuiltinProfiles bind:agents {models} {operations} /></div>
  {/if}
</section>
