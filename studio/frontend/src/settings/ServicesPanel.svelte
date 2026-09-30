<script lang="ts">
  import { Check, Copy, KeyRound } from '@lucide/svelte';
  import { formatKeyDate } from './format';
  import type { ServiceAPIKey } from './types';
  import { apiError as responseError } from '../api';
  import { putSettings, writeSettings } from './persistence';
  import type { QueueSave } from './persistence';
  import type { SettingsSnapshot } from './types';

  export let settings: SettingsSnapshot;
  export let queueSave: QueueSave;

  let gatewayKeyAlias = '';
  let generatedGatewayKey = '';
  let gatewayKeyCopied = false;

  function createGatewayAPIKey() {
    const alias = gatewayKeyAlias.trim();
    if (!alias) return;
    queueSave(async () => {
      const response = await fetch('/api/v1/settings/gateway/api-keys', {
        method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ alias })
      });
      if (!response.ok) throw new Error(await responseError(response));
      const result = (await response.json()) as { settings: SettingsSnapshot; secret: string };
      generatedGatewayKey = result.secret;
      gatewayKeyAlias = '';
      gatewayKeyCopied = false;
      return result.settings;
    });
  }

  function revokeGatewayAPIKey(key: ServiceAPIKey) {
    if (!window.confirm(`Revoke Gateway API key “${key.alias}”?`)) return;
    queueSave(() => writeSettings('DELETE', `/api/v1/settings/gateway/api-keys/${encodeURIComponent(key.id)}`));
  }

  async function copyGatewayAPIKey() {
    if (!generatedGatewayKey) return;
    await navigator.clipboard.writeText(generatedGatewayKey);
    gatewayKeyCopied = true;
  }

  function saveService(name: 'gateway' | 'system-one' | 'library') {
    if (!settings) return;
    const service = name === 'system-one' ? settings.services.system_one : settings.services[name];
    const payload = JSON.stringify({
      host: service.host,
      port: service.port
    });
    queueSave(() => putSettings(`/api/v1/settings/services/${name}`, payload));
  }
</script>

<section class="settings-section">
  <div class="section-heading"><div><p class="eyebrow">SERVICES</p><h2>Listener configuration</h2><p>Network changes apply when the corresponding service restarts.</p></div></div>
  <div class="service-grid">
    <article class="settings-card service-card">
      <div class="card-heading"><div><h3>Gateway</h3><p>OpenAI-compatible model gateway.</p></div><span class="count-badge">{settings.services.gateway.active_api_keys} keys</span></div>
      <label><span>Host</span><input bind:value={settings.services.gateway.host} onchange={() => saveService('gateway')} /></label><label><span>Port</span><input type="number" min="0" max="65535" bind:value={settings.services.gateway.port} onchange={() => saveService('gateway')} /></label><code>{settings.services.gateway.config_path}</code>
      <div class="service-key-manager">
        {#if generatedGatewayKey}
          <div class="generated-key compact-generated-key"><div><KeyRound aria-hidden="true" size={16} /><span><strong>Copy this key now</strong><small>It is shown once.</small></span></div><div class="generated-key-value"><code>{generatedGatewayKey}</code><button class="icon-button" title="Copy API key" onclick={copyGatewayAPIKey}>{#if gatewayKeyCopied}<Check aria-hidden="true" size={16} />{:else}<Copy aria-hidden="true" size={16} />{/if}</button></div><button class="text-button" onclick={() => generatedGatewayKey = ''}>Dismiss</button></div>
        {:else}
          <div class="api-key-create"><label><span>New key alias</span><input placeholder="Studio client" bind:value={gatewayKeyAlias} onkeydown={(event) => event.key === 'Enter' && createGatewayAPIKey()} /></label><button class="primary-button" onclick={createGatewayAPIKey} disabled={!gatewayKeyAlias.trim()}><KeyRound aria-hidden="true" size={15} /> Generate</button></div>
        {/if}
        <div class="api-key-list">
          {#each settings.gateway_providers.api_keys as key}
            <div class="api-key-row"><div><strong>{key.alias}</strong><small>{key.id.slice(0, 8)} · {formatKeyDate(key.created_at)}</small></div><span class:revoked={key.revoked_at}>{key.revoked_at ? 'Revoked' : 'Active'}</span>{#if !key.revoked_at}<button class="text-button danger-text" onclick={() => revokeGatewayAPIKey(key)}>Revoke</button>{/if}</div>
          {:else}<div class="api-key-empty">No keys configured. Authentication is disabled.</div>{/each}
        </div>
      </div>
    </article>
    <article class="settings-card service-card">
      <div class="card-heading"><div><h3>System One</h3><p>{settings.services.system_one.provider_count} providers · {settings.services.system_one.default_model}</p></div><span class="count-badge">{settings.services.system_one.active_api_keys} keys</span></div>
      <label><span>Host</span><input bind:value={settings.services.system_one.host} onchange={() => saveService('system-one')} /></label><label><span>Port</span><input type="number" min="0" max="65535" bind:value={settings.services.system_one.port} onchange={() => saveService('system-one')} /></label><code>{settings.services.system_one.config_path}</code>
    </article>
    <article class="settings-card service-card">
      <div class="card-heading"><div><h3>Library</h3><p>Global Agent Skill and proposition service.</p></div><span class="count-badge">local</span></div>
      <label><span>Host</span><input bind:value={settings.services.library.host} onchange={() => saveService('library')} /></label><label><span>Port</span><input type="number" min="1" max="65535" bind:value={settings.services.library.port} onchange={() => saveService('library')} /></label><code>{settings.services.library.config_path}</code>
    </article>
  </div>
</section>
