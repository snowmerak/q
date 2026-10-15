<script lang="ts">
  import { RefreshCw } from '@lucide/svelte';
  import { requestJSON } from '../api';
  import { putSettings } from './persistence';
  import type { QueueSave } from './persistence';
  import type { ModelOption, SettingsSnapshot, SystemOneModelOption } from './types';

  export let active = false;
  export let settings: SettingsSnapshot;
  export let queueSave: QueueSave;

  let started = false;
  let loading = false;
  let decisionModels: SystemOneModelOption[] = [];
  let generativeModels: ModelOption[] = [];
  let decisionError = '';
  let generativeError = '';
  $: if (active && !started) { started = true; void loadModels(); }

  async function loadModels() {
    loading = true;
    decisionError = '';
    generativeError = '';
    const results = await Promise.allSettled([
      requestJSON<{ models: SystemOneModelOption[] }>('GET', '/api/v1/settings/system-one/models'),
      requestJSON<{ models: ModelOption[] }>('GET', '/api/v1/settings/models')
    ]);
    if (results[0].status === 'fulfilled') decisionModels = results[0].value.models;
    else decisionError = String(results[0].reason);
    if (results[1].status === 'fulfilled') generativeModels = results[1].value.models.filter((model) => !model.group);
    else generativeError = String(results[1].reason);
    loading = false;
  }

  function saveBrowser() {
    const payload = JSON.stringify({ max_concurrent_reads: settings.web_search.max_concurrent_reads, generative_model: settings.web_search.generative_model });
    queueSave(() => putSettings('/api/v1/settings/web-search', payload));
  }

  function saveDecision(target: string, model: string) {
    const payload = JSON.stringify({ model });
    queueSave(() => putSettings(`/api/v1/settings/system-one/models/${target}`, payload));
  }
</script>

<section class="settings-section">
  <div class="section-heading"><div><h2>Web Search</h2><p>Browser capacity and models used for research.</p></div><button class="icon-button" title="Refresh web search models" onclick={loadModels} disabled={loading}><RefreshCw aria-hidden="true" size={16} class={loading ? 'spin' : undefined} /></button></div>
  <div class="settings-card assignment-list">
    <div class="assignment-row"><div><strong>Concurrent page reads</strong><small>Shared across searches in this Q process. Each search keeps its own cookies.</small></div><label><span>Maximum concurrent page reads</span><select aria-label="Maximum concurrent page reads" bind:value={settings.web_search.max_concurrent_reads} onchange={saveBrowser}>{#each [1, 2, 3, 4, 5, 6, 7, 8] as count}<option value={count}>{count}{count === 2 ? ' · default' : ''}</option>{/each}</select></label></div>
  </div>
  <div class="subsection-heading"><div><h3>Research models</h3><p>Decision models use your System One providers.</p></div></div>
  {#if decisionError}<p class="inline-warning">{decisionError}</p>{/if}
  <div class="settings-card assignment-list system-one-assignments">
    <div class="assignment-row system-one-assignment"><div><strong>Source and evidence selection</strong><small>Selects pages, checks publishers and identifies useful source passages.</small></div><label><span>Source and evidence model</span><select aria-label="Source and evidence model" bind:value={settings.web_search.navigation_model} onchange={() => saveDecision('web-search', settings.web_search.navigation_model)} disabled={loading}><option value="">Not configured</option>{#if settings.web_search.navigation_model && !decisionModels.some((model) => model.id === settings.web_search.navigation_model)}<option value={settings.web_search.navigation_model}>{settings.web_search.navigation_model}</option>{/if}{#each decisionModels as model}<option value={model.id}>{model.id}</option>{/each}</select></label></div>
    <div class="assignment-row system-one-assignment"><div><strong>Completion judgment</strong><small>Checks whether accumulated evidence answers the requested information.</small></div><label><span>Completion model</span><select aria-label="Completion model" bind:value={settings.web_search.completion_model} onchange={() => saveDecision('web-search-finish', settings.web_search.completion_model)} disabled={loading}><option value="">Not configured</option>{#if settings.web_search.completion_model && !decisionModels.some((model) => model.id === settings.web_search.completion_model)}<option value={settings.web_search.completion_model}>{settings.web_search.completion_model}</option>{/if}{#each decisionModels as model}<option value={model.id}>{model.id}</option>{/each}</select></label></div>
  </div>
  <p class="helper-copy" role="status">{settings.web_search.enabled ? 'Web search tools enabled.' : 'Select both decision models to enable web search tools.'}</p>
  <div class="subsection-heading"><div><h3>Generative model</h3><p>Choose a fixed model for future search planning. This stage is not active yet.</p></div></div>
  {#if generativeError}<p class="inline-warning">{generativeError}</p>{/if}
  <div class="settings-card assignment-list"><div class="assignment-row"><div><strong>Search planning model</strong><small>Saved independently of the active chat model. Selecting a model does not enable generative search.</small></div><label><span>Generative search model</span><select aria-label="Generative search model" bind:value={settings.web_search.generative_model} onchange={saveBrowser} disabled={loading}><option value="">Not configured</option>{#if settings.web_search.generative_model && !generativeModels.some((model) => model.id === settings.web_search.generative_model)}<option value={settings.web_search.generative_model}>{settings.web_search.generative_model}</option>{/if}{#each generativeModels as model}<option value={model.id}>{model.id}</option>{/each}</select></label></div></div>
</section>

<style>
  .assignment-row small {
    white-space: normal;
    overflow: visible;
    text-overflow: clip;
    line-height: 1.5;
  }
</style>
