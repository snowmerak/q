<script lang="ts">
  import { Plus, Trash2 } from '@lucide/svelte';
	import ChatGPTConnection from './ChatGPTConnection.svelte';
  import { roleLabel } from './format';
  import type { GatewayProvider } from './types';
  import { putSettings, writeSettings } from './persistence';
  import type { QueueSave } from './persistence';
  import type { SettingsSnapshot } from './types';

  export let settings: SettingsSnapshot;
  export let queueSave: QueueSave;

  const providerIDs = new WeakMap<GatewayProvider, string>();

  function providerKinds(type: string) {
    if (type !== 'openai-compatible') {
      const kind = type === 'xai' ? 'grok' : type === 'codex' ? 'codex' : type;
      return [{ value: '', label: 'Auto' }, { value: kind, label: kind === 'chatgpt' ? 'ChatGPT' : roleLabel(kind) }];
    }
    return [
      { value: '', label: 'Auto' }, { value: 'generic', label: 'Generic' }, { value: 'openai', label: 'OpenAI' },
      { value: 'openrouter', label: 'OpenRouter' }, { value: 'grok', label: 'Grok / xAI' },
      { value: 'anthropic', label: 'Anthropic' }, { value: 'codex', label: 'Codex' }
    ];
  }

  function saveProvider(provider: GatewayProvider, clearAPIKey = false) {
    const originalID = provider._original_id || provider.id;
    const nextID = provider.id;
    const payload = JSON.stringify({
      id: provider.id, type: provider.type, kind: provider.kind, prefix: provider.prefix,
      enabled: provider.enabled, base_url: provider.base_url, api_key_env: provider.api_key_env,
      api_key: provider.api_key || '', clear_api_key: clearAPIKey
    });
    queueSave(async () => {
      const currentID = providerIDs.get(provider) || originalID;
      const snapshot = await putSettings(`/api/v1/settings/gateway/providers/${encodeURIComponent(currentID)}`, payload);
      providerIDs.set(provider, nextID);
      return snapshot;
    });
  }

  function changeProviderType(provider: GatewayProvider) {
    provider.kind = '';
    if (provider.type === 'openai-compatible') {
      if (!provider.base_url) provider.base_url = 'https://api.openai.com/v1';
      if (!provider.api_key_env) provider.api_key_env = 'OPENAI_API_KEY';
    }
	if (provider.type === 'chatgpt') { provider.base_url = ''; provider.api_key_env = ''; provider.api_key = ''; }
    saveProvider(provider);
  }

  function nextProviderID() {
    const used = new Set(settings?.gateway_providers.items.map((provider) => provider.id) || []);
    let index = 1;
    while (used.has(`provider-${index}`)) index++;
    return `provider-${index}`;
  }

  function addProvider() {
    const id = nextProviderID();
    const payload = JSON.stringify({
      id, type: 'openai-compatible', kind: '', prefix: '', enabled: true,
      base_url: 'https://api.openai.com/v1', api_key_env: 'OPENAI_API_KEY'
    });
    queueSave(() => writeSettings('POST', '/api/v1/settings/gateway/providers', payload));
  }

  function deleteProvider(provider: GatewayProvider) {
    if (!window.confirm(`Delete Gateway provider “${provider.id}”?`)) return;
    const originalID = provider._original_id || provider.id;
    queueSave(async () => {
      const snapshot = await writeSettings('DELETE', `/api/v1/settings/gateway/providers/${encodeURIComponent(providerIDs.get(provider) || originalID)}`);
      providerIDs.delete(provider);
      return snapshot;
    });
  }

  function clearProviderKey(provider: GatewayProvider) {
    if (!window.confirm(`Clear the stored inline API key for “${provider.id}”?`)) return;
    saveProvider(provider, true);
  }
</script>

<section class="settings-section">
  <div class="section-heading"><div><p class="eyebrow">GATEWAY</p><h2>Providers</h2><p>Configure upstream APIs exposed through Q's internal Gateway.</p></div><button class="primary-button" onclick={addProvider}><Plus aria-hidden="true" size={16} /> Add provider</button></div>
  <code class="config-path">{settings.gateway_providers.config_path}</code>
  <div class="provider-list">
    {#each settings.gateway_providers.items as provider}
      <article class="settings-card provider-card">
        <div class="card-heading"><div><h3>{provider.id}</h3><p>{provider.model_count ? `${provider.model_count} pinned models` : 'Models discovered from upstream'}</p></div><div class="provider-actions"><label class="compact-toggle"><input type="checkbox" bind:checked={provider.enabled} onchange={() => saveProvider(provider)} /><span>{provider.enabled ? 'Enabled' : 'Disabled'}</span></label><button class="danger-icon" title="Delete provider" onclick={() => deleteProvider(provider)}><Trash2 aria-hidden="true" size={16} /></button></div></div>
        <div class="provider-fields">
          <label><span>Provider ID</span><input bind:value={provider.id} onchange={() => saveProvider(provider)} /></label>
          <label><span>Model prefix</span><input placeholder={provider.id} bind:value={provider.prefix} onchange={() => saveProvider(provider)} /></label>
          <label><span>API type</span><select bind:value={provider.type} onchange={() => changeProviderType(provider)}><option value="openai-compatible">OpenAI compatible</option><option value="openrouter">OpenRouter</option><option value="xai">xAI</option><option value="anthropic">Anthropic</option><option value="codex">Codex App Server</option><option value="chatgpt">ChatGPT plan</option></select></label>
          <label><span>Provider kind</span><select bind:value={provider.kind} onchange={() => saveProvider(provider)}>{#each providerKinds(provider.type) as kind}<option value={kind.value}>{kind.label}</option>{/each}</select></label>
          {#if provider.type !== 'chatgpt'}
          <label class="wide-field"><span>Base URL</span><input placeholder="Provider default" bind:value={provider.base_url} onchange={() => saveProvider(provider)} disabled={provider.type === 'codex'} /></label>
          <label><span>API key environment</span><input placeholder="Optional" bind:value={provider.api_key_env} onchange={() => saveProvider(provider)} disabled={provider.type === 'codex'} /></label>
          <label><span>New inline API key</span><input type="password" autocomplete="new-password" placeholder={provider.has_inline_api_key ? 'Stored · enter to replace' : 'Optional'} bind:value={provider.api_key} onchange={() => saveProvider(provider)} disabled={provider.type === 'codex'} /></label>
          {/if}
        </div>
        {#if provider.type === 'chatgpt'}
          <ChatGPTConnection providerID={provider._original_id || provider.id}
            enabled={provider.enabled && provider._original_enabled === true && provider._original_type === 'chatgpt' && provider.id === provider._original_id} />
        {/if}
        {#if provider.has_inline_api_key}<button class="text-button danger-text" onclick={() => clearProviderKey(provider)}>Clear stored inline key</button>{/if}
      </article>
    {:else}
      <div class="empty-card"><p>No Gateway providers configured.</p><button class="primary-button" onclick={addProvider}><Plus aria-hidden="true" size={16} /> Add provider</button></div>
    {/each}
  </div>
</section>
