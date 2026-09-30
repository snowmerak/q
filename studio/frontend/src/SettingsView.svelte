<script lang="ts">
  import { Bot, BrainCircuit, Cpu, Network, Server, SlidersHorizontal, Unplug } from '@lucide/svelte';
  import { onMount } from 'svelte';
  import { requestJSON } from './api';
  import IntegrationsView from './IntegrationsView.svelte';
  import SubagentsView from './SubagentsView.svelte';
  import ModelsPanel from './settings/ModelsPanel.svelte';
  import ProvidersPanel from './settings/ProvidersPanel.svelte';
  import SystemOnePanel from './settings/SystemOnePanel.svelte';
  import RuntimePanel from './settings/RuntimePanel.svelte';
  import ServicesPanel from './settings/ServicesPanel.svelte';
  import type { SaveState, SettingsSection, SettingsSnapshot } from './settings/types';

  // The cache and write queue survive section and page navigation.
  export let active = false;
  const settingsSections = [
    { id: 'models' as const, label: 'Models', description: 'Chat, embedding, and role assignments', icon: Cpu },
    { id: 'providers' as const, label: 'Providers', description: 'Gateway upstream providers', icon: Network },
    { id: 'system-one' as const, label: 'System One', description: 'Decision API and access keys', icon: BrainCircuit },
    { id: 'runtime' as const, label: 'Runtime', description: 'Execution, context, and storage', icon: SlidersHorizontal },
    { id: 'services' as const, label: 'Services', description: 'Gateway and System One', icon: Server },
    { id: 'subagents' as const, label: 'Subagents', description: 'Profiles, delegation, and ACP', icon: Bot },
    { id: 'integrations' as const, label: 'Integrations', description: 'MCP and language servers', icon: Unplug }
  ];


  let activeSection: SettingsSection = sectionFromLocation();
  let settings: SettingsSnapshot | null = null;
  let settingsError = '';
  let settingsLoading = false;
  let saveState: SaveState = { kind: 'idle' };
  let saveQueue = Promise.resolve();
  let savedTimer: ReturnType<typeof setTimeout> | undefined;
  let saveGeneration = 0;
  let pending = 0;
  $: if (active && !settings && !settingsLoading && !settingsError) void loadSettings();

  async function loadSettings() {
    if (settingsLoading) return;
    const generation = saveGeneration;
    settingsLoading = true;
    settingsError = '';
    try {
      const snapshot = await requestJSON<SettingsSnapshot>('GET', '/api/v1/settings');
      if (generation === saveGeneration) adoptSettings(snapshot);
    } catch (reason) {
      settingsError = reason instanceof Error ? reason.message : 'Settings are unavailable';
    } finally {
      settingsLoading = false;
    }
  }

  function adoptSettings(snapshot: SettingsSnapshot) {
    for (const provider of snapshot.gateway_providers.items) provider._original_id = provider.id;
    for (const provider of snapshot.system_one.providers) provider._original_id = provider.id;
    settings = snapshot;
  }

  function sectionFromLocation(): SettingsSection {
    const section = new URL(window.location.href).searchParams.get('section');
    return settingsSections.some((candidate) => candidate.id === section) ? section as SettingsSection : 'models';
  }

  function chooseSettingsSection(section: SettingsSection) {
    activeSection = section;
    const url = new URL(window.location.href);
    url.searchParams.set('section', section);
    window.history.pushState({}, '', url.pathname + url.search);
  }

  function queueSave(request: () => Promise<SettingsSnapshot>) {
    const generation = ++saveGeneration;
    pending += 1;
    if (savedTimer) clearTimeout(savedTimer);
    saveState = { kind: 'saving' };
    saveQueue = saveQueue.then(async () => {
      const snapshot = await request();
      // Older acknowledgements may precede newer unsaved edits in any panel.
      if (generation === saveGeneration) {
        adoptSettings(snapshot);
        saveState = { kind: 'saving' };
      }
    }).catch((reason) => {
      saveState = { kind: 'error', message: reason instanceof Error ? reason.message : 'Could not save settings' };
    }).finally(() => {
      pending -= 1;
      if (pending || saveState.kind === 'error') return;
      saveState = { kind: 'saved' };
      savedTimer = setTimeout(() => (saveState = { kind: 'idle' }), 2400);
    });
  }

  onMount(() => {
    const onPopState = () => { activeSection = sectionFromLocation(); };
    window.addEventListener('popstate', onPopState);
    return () => {
      window.removeEventListener('popstate', onPopState);
      if (savedTimer) clearTimeout(savedTimer);
    };
  });
</script>

  <div class="settings-layout" hidden={!active}>
    <aside class="settings-index" aria-label="Settings sections">
      <div class="scope-label">GLOBAL</div>
      {#each settingsSections as section}
        {@const SectionIcon = section.icon}
        <button class:active={activeSection === section.id} onclick={() => chooseSettingsSection(section.id)}><SectionIcon aria-hidden="true" size={18} strokeWidth={1.7} /><span><strong>{section.label}</strong><small>{section.description}</small></span></button>
      {/each}
    </aside>

    <div class="settings-content">
      <div class="save-state" class:error={saveState.kind === 'error'} aria-live="polite">{saveState.kind === 'saving' ? 'Saving…' : saveState.kind === 'saved' ? 'Saved' : saveState.kind === 'error' ? saveState.message : 'Changes save automatically'}</div>
      {#if settingsError}
        <section class="error-panel"><h2>Settings unavailable</h2><p>{settingsError}</p><button class="retry" onclick={loadSettings}>Retry</button></section>
      {:else if !settings}
        <section class="loading-panel">Loading settings…</section>

      {:else}
        <div hidden={activeSection !== 'models'}><ModelsPanel bind:settings {queueSave} active={active && activeSection === 'models'} /></div>
        <div hidden={activeSection !== 'providers'}><ProvidersPanel bind:settings {queueSave} /></div>
        <div hidden={activeSection !== 'system-one'}><SystemOnePanel bind:settings {queueSave} active={active && activeSection === 'system-one'} /></div>
        <div hidden={activeSection !== 'runtime'}><RuntimePanel bind:settings {queueSave} active={active && activeSection === 'runtime'} /></div>
        <div hidden={activeSection !== 'services'}><ServicesPanel bind:settings {queueSave} /></div>
        {#if active && activeSection === 'subagents'}<SubagentsView />{/if}
        {#if active && activeSection === 'integrations'}<IntegrationsView />{/if}
      {/if}
    </div>
  </div>

<style>
  .settings-layout[hidden] { display: none; }
</style>
