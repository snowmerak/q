<script lang="ts">
  import { Check, Copy, KeyRound, Plus, RefreshCw, Trash2 } from '@lucide/svelte';
  import { formatKeyDate } from './format';
  import type { ServiceAPIKey, SystemOneModelOption, SystemOneProvider } from './types';
  import { apiError as responseError } from '../api';
  import { putSettings, writeSettings } from './persistence';
  import type { QueueSave } from './persistence';
  import type { SettingsSnapshot } from './types';

  export let active = false;
  export let settings: SettingsSnapshot;
  export let queueSave: QueueSave;

  let started = false;
  let systemOneModels: SystemOneModelOption[] = [];
  let systemOneModelsLoading = false;
  let systemOneModelsError = '';
  let systemOneKeyAlias = '';
  let generatedSystemOneKey = '';
  let systemOneKeyCopied = false;
  const providerIDs = new WeakMap<SystemOneProvider, string>();
  $: if (active && !started) { started = true; void loadSystemOneModels(); }

  async function loadSystemOneModels() {
    systemOneModelsLoading = true;
    systemOneModelsError = '';
    try {
      const response = await fetch('/api/v1/settings/system-one/models', { headers: { Accept: 'application/json' } });
      if (!response.ok) throw new Error(await responseError(response));
      const catalog = (await response.json()) as { models: SystemOneModelOption[] };
      systemOneModels = catalog.models;
    } catch (error) {
      systemOneModelsError = error instanceof Error ? error.message : 'System One models are unavailable';
    } finally {
      systemOneModelsLoading = false;
    }
  }

  function saveSystemOneModel(target: 'default' | 'agent-skill-decision' | 'archive-decision', model: string) {
    queueSave(() => putSettings(`/api/v1/settings/system-one/models/${target}`, JSON.stringify({ model })));
  }

  function saveSystemOneProvider(provider: SystemOneProvider) {
    const originalID = provider._original_id || provider.id;
    const nextID = provider.id;
    const payload = JSON.stringify({ id: provider.id, uri: provider.uri, api_key_env: provider.api_key_env });
    queueSave(async () => {
      const currentID = providerIDs.get(provider) || originalID;
      const snapshot = await putSettings(`/api/v1/settings/system-one/providers/${encodeURIComponent(currentID)}`, payload);
      providerIDs.set(provider, nextID);
      return snapshot;
    });
  }

  function nextSystemOneProviderID() {
    const used = new Set(settings?.system_one.providers.map((provider) => provider.id) || []);
    if (!used.has('typesafe')) return 'typesafe';
    let index = 1;
    while (used.has(`provider-${index}`)) index++;
    return `provider-${index}`;
  }

  function addSystemOneProvider() {
    const id = nextSystemOneProviderID();
    queueSave(() => writeSettings('POST', '/api/v1/settings/system-one/providers', JSON.stringify({
      id,
      uri: 'https://api.typesafe.ai/v1/systemone',
      api_key_env: id === 'typesafe' ? 'TYPESAFE_API_KEY' : ''
    })));
  }

  function deleteSystemOneProvider(provider: SystemOneProvider) {
    if (!window.confirm(`Delete System One provider “${provider.id}”?`)) return;
    const originalID = provider._original_id || provider.id;
    queueSave(async () => {
      const snapshot = await writeSettings('DELETE', `/api/v1/settings/system-one/providers/${encodeURIComponent(providerIDs.get(provider) || originalID)}`);
      providerIDs.delete(provider);
      return snapshot;
    });
  }

  function createSystemOneAPIKey() {
    const alias = systemOneKeyAlias.trim();
    if (!alias) return;
    queueSave(async () => {
      const response = await fetch('/api/v1/settings/system-one/api-keys', {
        method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ alias })
      });
      if (!response.ok) throw new Error(await responseError(response));
      const result = (await response.json()) as { settings: SettingsSnapshot; secret: string };
      generatedSystemOneKey = result.secret;
      systemOneKeyAlias = '';
      systemOneKeyCopied = false;
      return result.settings;
    });
  }

  function revokeSystemOneAPIKey(key: ServiceAPIKey) {
    if (!window.confirm(`Revoke System One API key “${key.alias}”?`)) return;
    queueSave(() => writeSettings('DELETE', `/api/v1/settings/system-one/api-keys/${encodeURIComponent(key.id)}`));
  }

  async function copySystemOneAPIKey() {
    if (!generatedSystemOneKey) return;
    await navigator.clipboard.writeText(generatedSystemOneKey);
    systemOneKeyCopied = true;
  }
</script>

<section class="settings-section">
  <div class="section-heading">
    <div><p class="eyebrow">GLOBAL SYSTEM ONE</p><h2>Decision API</h2><p>Configure decision providers, model routing, and access to <code>q systemone start</code>.</p></div>
    <button class="icon-button" title="Refresh System One models" onclick={loadSystemOneModels} disabled={systemOneModelsLoading}><RefreshCw aria-hidden="true" size={16} class={systemOneModelsLoading ? 'spin' : undefined} /></button>
  </div>
  <code class="config-path">{settings.system_one.config_path}</code>
  {#if systemOneModelsError}<p class="inline-warning">{systemOneModelsError}</p>{/if}

  <div class="settings-card assignment-list system-one-assignments">
    <div class="assignment-row system-one-assignment">
      <div><strong>Default</strong><small>Representative decision model</small></div>
      <label><span>Model</span><select bind:value={settings.system_one.default_model} onchange={() => saveSystemOneModel('default', settings!.system_one.default_model)} disabled={systemOneModelsLoading}>
        {#if settings.system_one.default_model && !systemOneModels.some((model) => model.id === settings?.system_one.default_model)}<option value={settings.system_one.default_model}>{settings.system_one.default_model}</option>{/if}
        {#each systemOneModels as model}<option value={model.id}>{model.id}</option>{/each}
      </select></label>
    </div>
    <div class="assignment-row system-one-assignment">
      <div><strong>Agent Skill Decision</strong><small>{settings.system_one.agent_skill_model ? 'Dedicated role model' : `Uses ${settings.system_one.default_model}`}</small></div>
      <label><span>Model</span><select bind:value={settings.system_one.agent_skill_model} onchange={() => saveSystemOneModel('agent-skill-decision', settings!.system_one.agent_skill_model)} disabled={systemOneModelsLoading}>
        <option value="">Use default · {settings.system_one.default_model}</option>
        {#if settings.system_one.agent_skill_model && !systemOneModels.some((model) => model.id === settings?.system_one.agent_skill_model)}<option value={settings.system_one.agent_skill_model}>{settings.system_one.agent_skill_model}</option>{/if}
        {#each systemOneModels as model}<option value={model.id}>{model.id}</option>{/each}
      </select></label>
    </div>
    <div class="assignment-row system-one-assignment">
      <div><strong>Archive Decision</strong><small>{settings.system_one.archive_model ? 'Dedicated role model' : `Uses ${settings.system_one.default_model}`}</small></div>
      <label><span>Model</span><select bind:value={settings.system_one.archive_model} onchange={() => saveSystemOneModel('archive-decision', settings!.system_one.archive_model)} disabled={systemOneModelsLoading}>
        <option value="">Use default · {settings.system_one.default_model}</option>
        {#if settings.system_one.archive_model && !systemOneModels.some((model) => model.id === settings?.system_one.archive_model)}<option value={settings.system_one.archive_model}>{settings.system_one.archive_model}</option>{/if}
        {#each systemOneModels as model}<option value={model.id}>{model.id}</option>{/each}
      </select></label>
    </div>
  </div>

  <div class="subsection-heading"><div><h3>Providers</h3><p>Each model is addressed as provider ID and model name.</p></div><button class="primary-button" onclick={addSystemOneProvider}><Plus aria-hidden="true" size={16} /> Add provider</button></div>
  <div class="provider-list">
    {#each settings.system_one.providers as provider}
      <article class="settings-card provider-card system-one-provider-card">
        <div class="card-heading"><div><h3>{provider.id}</h3><p>Native System One endpoint</p></div><button class="danger-icon" title="Delete provider" onclick={() => deleteSystemOneProvider(provider)}><Trash2 aria-hidden="true" size={16} /></button></div>
        <div class="provider-fields system-one-provider-fields">
          <label><span>Provider ID</span><input bind:value={provider.id} onchange={() => saveSystemOneProvider(provider)} /></label>
          <label class="wide-field"><span>Endpoint URI</span><input placeholder="https://example.com/v1/systemone" bind:value={provider.uri} onchange={() => saveSystemOneProvider(provider)} /></label>
          <label><span>API key environment</span><input placeholder="Optional · unauthenticated when empty" bind:value={provider.api_key_env} onchange={() => saveSystemOneProvider(provider)} /></label>
        </div>
      </article>
    {/each}
  </div>

  <div class="subsection-heading"><div><h3>Server API keys</h3><p>Authenticate clients calling <code>q systemone start</code>. With no active keys, the server accepts unauthenticated requests.</p></div><span class="count-badge">{settings.system_one.active_api_keys} active</span></div>
  <div class="settings-card api-key-card">
    {#if generatedSystemOneKey}
      <div class="generated-key"><div><KeyRound aria-hidden="true" size={18} /><span><strong>Copy this key now</strong><small>It will not be shown again after this page is dismissed.</small></span></div><div class="generated-key-value"><code>{generatedSystemOneKey}</code><button class="icon-button" title="Copy API key" onclick={copySystemOneAPIKey}>{#if systemOneKeyCopied}<Check aria-hidden="true" size={16} />{:else}<Copy aria-hidden="true" size={16} />{/if}</button></div><button class="text-button" onclick={() => generatedSystemOneKey = ''}>Dismiss</button></div>
    {:else}
      <div class="api-key-create"><label><span>New key alias</span><input placeholder="Studio client" bind:value={systemOneKeyAlias} onkeydown={(event) => event.key === 'Enter' && createSystemOneAPIKey()} /></label><button class="primary-button" onclick={createSystemOneAPIKey} disabled={!systemOneKeyAlias.trim()}><KeyRound aria-hidden="true" size={16} /> Generate key</button></div>
    {/if}
    <div class="api-key-list">
      {#each settings.system_one.api_keys as key}
        <div class="api-key-row"><div><strong>{key.alias}</strong><small>{key.id === 'legacy' ? 'legacy' : key.id.slice(0, 8)} · {formatKeyDate(key.created_at)}</small></div><span class:revoked={key.revoked_at}>{key.revoked_at ? 'Revoked' : 'Active'}</span>{#if !key.revoked_at}<button class="text-button danger-text" onclick={() => revokeSystemOneAPIKey(key)}>Revoke</button>{/if}</div>
      {:else}
        <div class="api-key-empty">No server API keys configured. Authentication is disabled.</div>
      {/each}
    </div>
  </div>
</section>
