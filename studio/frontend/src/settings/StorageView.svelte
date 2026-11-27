<script lang="ts">
  import { Check, Database, HardDrive, Search, TriangleAlert } from '@lucide/svelte';
  import { onMount } from 'svelte';
  import { apiError as responseError } from '../api';
  import { putSettings } from './persistence';
  import type { QueueSave } from './persistence';
  import type { SettingsSnapshot, StorageSettings } from './types';

  export let settings: SettingsSnapshot;
  export let queueSave: QueueSave;

  type StorageTab = 'local' | 'remote';
  type TestResult = { ok: boolean; target: string; message: string; latency_ms: number };

  let tab: StorageTab = tabFromLocation(settings?.storage?.mode);
  let testingTarget: 'postgres' | 'opensearch' | '' = '';
  let testResults: { postgres?: TestResult; opensearch?: TestResult } = {};

  function tabFromLocation(fallback?: string): StorageTab {
    const param = new URL(window.location.href).searchParams.get('storage_tab');
    if (param === 'local' || param === 'remote') return param;
    return fallback === 'remote' ? 'remote' : 'local';
  }

  function chooseTab(value: StorageTab) {
    tab = value;
    const url = new URL(window.location.href);
    url.searchParams.set('storage_tab', value);
    window.history.pushState({}, '', url.pathname + url.search);
  }

  function tabKeydown(event: KeyboardEvent) {
    if (!['ArrowLeft', 'ArrowRight', 'Home', 'End'].includes(event.key)) return;
    event.preventDefault();
    const next: StorageTab = event.key === 'Home' ? 'local' : event.key === 'End' ? 'remote' : tab === 'local' ? 'remote' : 'local';
    chooseTab(next);
    document.getElementById(`storage-tab-${next}`)?.focus();
  }

  onMount(() => {
    const sync = () => { tab = tabFromLocation(settings?.storage?.mode); };
    window.addEventListener('popstate', sync);
    return () => window.removeEventListener('popstate', sync);
  });

  function buildPayload(
    mode: StorageSettings['mode'],
    opts: { clearPassword?: boolean; clearBearerToken?: boolean; clearDSN?: boolean } = {}
  ) {
    const remote = settings.storage.remote;
    return {
      mode,
      remote: {
        opensearch: {
          enabled: remote.opensearch.enabled,
          endpoint: remote.opensearch.endpoint,
          index_prefix: remote.opensearch.index_prefix,
          refresh: remote.opensearch.refresh,
          username: remote.opensearch.username,
          password_env: remote.opensearch.password_env,
          password: remote.opensearch.password || '',
          clear_password: Boolean(opts.clearPassword),
          bearer_token_env: remote.opensearch.bearer_token_env,
          bearer_token: remote.opensearch.bearer_token || '',
          clear_bearer_token: Boolean(opts.clearBearerToken),
          targets: {
            skills: remote.opensearch.targets.skills,
            knowledge: remote.opensearch.targets.knowledge,
            workspace_memory: remote.opensearch.targets.workspace_memory
          }
        },
        postgres: {
          enabled: remote.postgres.enabled,
          dsn_env: remote.postgres.dsn_env,
          dsn: remote.postgres.dsn || '',
          clear_dsn: Boolean(opts.clearDSN)
        }
      }
    };
  }

  function saveStorage(
    mode: StorageSettings['mode'] = settings.storage.mode,
    opts: { clearPassword?: boolean; clearBearerToken?: boolean; clearDSN?: boolean } = {}
  ) {
    if (!settings?.storage) return;
    const submittedPW = settings.storage.remote.opensearch.password || '';
    const submittedToken = settings.storage.remote.opensearch.bearer_token || '';
    const submittedDSN = settings.storage.remote.postgres.dsn || '';
    const payload = JSON.stringify(buildPayload(mode, opts));
    queueSave(async () => {
      const snapshot = await putSettings('/api/v1/settings/storage', payload);
      if (snapshot.storage) {
        if ((settings.storage.remote.opensearch.password || '') === submittedPW) snapshot.storage.remote.opensearch.password = '';
        if ((settings.storage.remote.opensearch.bearer_token || '') === submittedToken) snapshot.storage.remote.opensearch.bearer_token = '';
        if ((settings.storage.remote.postgres.dsn || '') === submittedDSN) snapshot.storage.remote.postgres.dsn = '';
      }
      return snapshot;
    });
  }

  function activateMode(mode: StorageTab) {
    if (mode === 'remote' && !settings.storage.remote.opensearch.enabled && !settings.storage.remote.postgres.enabled) {
      settings.storage.remote.opensearch.enabled = true;
    }
    saveStorage(mode);
  }

  async function testConnection(target: 'postgres' | 'opensearch') {
    if (!settings?.storage || testingTarget) return;
    testingTarget = target;
    try {
      const payload = buildPayload('remote');
      const response = await fetch('/api/v1/settings/storage/test', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ target, remote: payload.remote })
      });
      if (!response.ok) throw new Error(await responseError(response));
      const result = (await response.json()) as TestResult;
      testResults = { ...testResults, [target]: result };
    } catch (error) {
      testResults = {
        ...testResults,
        [target]: {
          ok: false,
          target,
          message: error instanceof Error ? error.message : 'Connection test failed',
          latency_ms: 0
        }
      };
    } finally {
      testingTarget = '';
    }
  }
</script>

{#if settings?.storage}
<section class="settings-section">
  <div class="section-heading">
    <div>
      <p class="eyebrow">STORAGE BACKEND</p>
      <h2>Local or remote storage</h2>
      <p>Choose between local SQLite + Bleve/HNSW or remote PostgreSQL + OpenSearch. Changes take effect after restarting Library and Workspace Memory.</p>
    </div>
    <span class="count-badge">Active: {settings.storage.mode}</span>
  </div>
  <code class="config-path">{settings.storage.config_path}</code>

  <div class="integration-tabs storage-tabs" role="tablist" aria-label="Storage backend modes">
    <button id="storage-tab-local" role="tab" aria-controls="storage-panel-local" aria-selected={tab === 'local'} tabindex={tab === 'local' ? 0 : -1} class:active={tab === 'local'} onclick={() => chooseTab('local')} onkeydown={tabKeydown}>
      <strong>Local</strong>
      <small>SQLite + Bleve &amp; HNSW {settings.storage.mode === 'local' ? '· Active' : ''}</small>
    </button>
    <button id="storage-tab-remote" role="tab" aria-controls="storage-panel-remote" aria-selected={tab === 'remote'} tabindex={tab === 'remote' ? 0 : -1} class:active={tab === 'remote'} onclick={() => chooseTab('remote')} onkeydown={tabKeydown}>
      <strong>Remote</strong>
      <small>PostgreSQL + OpenSearch {settings.storage.mode === 'remote' ? '· Active' : ''}</small>
    </button>
  </div>

  <div id="storage-panel-local" role="tabpanel" aria-labelledby="storage-tab-local" hidden={tab !== 'local'}>
    <article class="settings-card">
      <div class="card-heading">
        <div>
          <h3><HardDrive aria-hidden="true" size={16} /> Local embedded storage</h3>
          <p>Default zero-dependency storage inside Q's local configuration directory.</p>
        </div>
        {#if settings.storage.mode === 'local'}
          <span class="count-badge active-pill"><Check aria-hidden="true" size={13} /> Active mode</span>
        {:else}
          <button class="primary-button" onclick={() => activateMode('local')}>Use Local Storage</button>
        {/if}
      </div>
      <div class="storage-summary-grid">
        <div class="storage-summary-item"><strong>Work items &amp; tickets</strong><small>Embedded SQLite catalog (<code>workspace-knowledge.sqlite</code>)</small></div>
        <div class="storage-summary-item"><strong>Skills, Knowledge &amp; Memory</strong><small>Local Bleve lexical index + HNSW vector index</small></div>
      </div>
    </article>
  </div>

  <div id="storage-panel-remote" role="tabpanel" aria-labelledby="storage-tab-remote" hidden={tab !== 'remote'}>
    <article class="settings-card">
      <div class="card-heading">
        <div>
          <h3>Remote storage mode</h3>
          <p>Switch Library and Workspace Memory to use external PostgreSQL and/or OpenSearch backends.</p>
        </div>
        {#if settings.storage.mode === 'remote'}
          <span class="count-badge active-pill"><Check aria-hidden="true" size={13} /> Active mode</span>
        {:else}
          <button class="primary-button" onclick={() => activateMode('remote')}>Activate Remote Storage</button>
        {/if}
      </div>
      {#if settings.storage.mode === 'remote' && !settings.storage.remote.opensearch.enabled && !settings.storage.remote.postgres.enabled}
        <p class="inline-warning"><TriangleAlert aria-hidden="true" size={14} /> Remote mode is active, but neither PostgreSQL nor OpenSearch is enabled below. Local fallbacks will remain in use.</p>
      {/if}
    </article>

    <article class="settings-card">
      <div class="card-heading">
        <div>
          <h3><Database aria-hidden="true" size={16} /> PostgreSQL (Work Items &amp; Tickets)</h3>
          <p>Authoritative shared store for workspaces, issues, tickets, coordinator attempts, and outbox receipts.</p>
        </div>
        <div class="provider-actions">
          <label class="compact-toggle">
            <input type="checkbox" bind:checked={settings.storage.remote.postgres.enabled} onchange={() => { if (!settings.storage.remote.postgres.enabled || settings.storage.remote.postgres.dsn_env || settings.storage.remote.postgres.dsn || settings.storage.remote.postgres.has_inline_dsn || settings.storage.mode === 'local') saveStorage(); }} />
            <span>{settings.storage.remote.postgres.enabled ? 'Enabled' : 'Disabled'}</span>
          </label>
          <button class="text-button" onclick={() => testConnection('postgres')} disabled={testingTarget === 'postgres'}>
            {testingTarget === 'postgres' ? 'Testing…' : 'Test connection'}
          </button>
        </div>
      </div>
      <div class="field-grid">
        <label>
          <span>DSN environment variable</span>
          <input placeholder="Q_POSTGRES_DSN" spellcheck={false} bind:value={settings.storage.remote.postgres.dsn_env} onchange={() => saveStorage()} />
        </label>
        <label>
          <span>New inline DSN</span>
          <input type="password" autocomplete="new-password" placeholder={settings.storage.remote.postgres.has_inline_dsn ? 'Stored · enter to replace' : 'postgres://user:pass@host:5432/db'} bind:value={settings.storage.remote.postgres.dsn} onchange={() => saveStorage()} />
        </label>
      </div>
      {#if settings.storage.remote.postgres.has_inline_dsn}
        <button class="text-button danger-text" onclick={() => { if (window.confirm('Clear the stored inline PostgreSQL DSN?')) saveStorage(settings.storage.mode, { clearDSN: true }); }}>Clear stored inline DSN</button>
      {/if}
      {#if testResults.postgres}
        <p class:test-ok={testResults.postgres.ok} class:test-error={!testResults.postgres.ok} class="storage-test-status">
          {testResults.postgres.ok ? `OK (${testResults.postgres.latency_ms}ms) · ${testResults.postgres.message}` : `Failed · ${testResults.postgres.message}`}
        </p>
      {/if}
    </article>

    <article class="settings-card">
      <div class="card-heading">
        <div>
          <h3><Search aria-hidden="true" size={16} /> OpenSearch (Search &amp; Vectors)</h3>
          <p>Remote hybrid BM25 + kNN vector indexes over REST/JSON.</p>
        </div>
        <div class="provider-actions">
          <label class="compact-toggle">
            <input type="checkbox" bind:checked={settings.storage.remote.opensearch.enabled} onchange={() => { if (!settings.storage.remote.opensearch.enabled || settings.storage.remote.opensearch.endpoint || settings.storage.mode === 'local') saveStorage(); }} />
            <span>{settings.storage.remote.opensearch.enabled ? 'Enabled' : 'Disabled'}</span>
          </label>
          <button class="text-button" onclick={() => testConnection('opensearch')} disabled={testingTarget === 'opensearch'}>
            {testingTarget === 'opensearch' ? 'Testing…' : 'Test connection'}
          </button>
        </div>
      </div>
      <div class="field-grid">
        <label class="wide-field">
          <span>Endpoint URL</span>
          <input placeholder="http://127.0.0.1:9200" spellcheck={false} bind:value={settings.storage.remote.opensearch.endpoint} onchange={() => saveStorage()} />
        </label>
        <label>
          <span>Index prefix</span>
          <input placeholder="q" spellcheck={false} bind:value={settings.storage.remote.opensearch.index_prefix} onchange={() => saveStorage()} />
        </label>
        <label>
          <span>Refresh policy</span>
          <select bind:value={settings.storage.remote.opensearch.refresh} onchange={() => saveStorage()}>
            <option value="wait_for">wait_for (default)</option>
            <option value="true">true (immediate)</option>
            <option value="false">false (async)</option>
          </select>
        </label>
        <label>
          <span>Username</span>
          <input placeholder="Optional" spellcheck={false} bind:value={settings.storage.remote.opensearch.username} onchange={() => saveStorage()} />
        </label>
        <label>
          <span>Password environment variable</span>
          <input placeholder="Q_OPENSEARCH_PASSWORD" spellcheck={false} bind:value={settings.storage.remote.opensearch.password_env} onchange={() => saveStorage()} />
        </label>
        <label>
          <span>New inline password</span>
          <input type="password" autocomplete="new-password" placeholder={settings.storage.remote.opensearch.has_inline_password ? 'Stored · enter to replace' : 'Optional'} bind:value={settings.storage.remote.opensearch.password} onchange={() => saveStorage()} />
        </label>
        <label>
          <span>Bearer token environment variable</span>
          <input placeholder="Optional" spellcheck={false} bind:value={settings.storage.remote.opensearch.bearer_token_env} onchange={() => saveStorage()} />
        </label>
        <label>
          <span>New inline bearer token</span>
          <input type="password" autocomplete="new-password" placeholder={settings.storage.remote.opensearch.has_inline_bearer_token ? 'Stored · enter to replace' : 'Optional'} bind:value={settings.storage.remote.opensearch.bearer_token} onchange={() => saveStorage()} />
        </label>
      </div>
      <div class="target-toggles">
        <span class="target-label">OpenSearch targets</span>
        <div class="target-checkboxes">
          <label class="compact-toggle"><input type="checkbox" bind:checked={settings.storage.remote.opensearch.targets.skills} onchange={() => saveStorage()} /><span>Skills catalog</span></label>
          <label class="compact-toggle"><input type="checkbox" bind:checked={settings.storage.remote.opensearch.targets.knowledge} onchange={() => saveStorage()} /><span>Workspace knowledge</span></label>
          <label class="compact-toggle"><input type="checkbox" bind:checked={settings.storage.remote.opensearch.targets.workspace_memory} onchange={() => saveStorage()} /><span>Workspace memory</span></label>
        </div>
      </div>
      <div class="inline-secret-actions">
        {#if settings.storage.remote.opensearch.has_inline_password}
          <button class="text-button danger-text" onclick={() => { if (window.confirm('Clear the stored inline OpenSearch password?')) saveStorage(settings.storage.mode, { clearPassword: true }); }}>Clear stored password</button>
        {/if}
        {#if settings.storage.remote.opensearch.has_inline_bearer_token}
          <button class="text-button danger-text" onclick={() => { if (window.confirm('Clear the stored inline OpenSearch bearer token?')) saveStorage(settings.storage.mode, { clearBearerToken: true }); }}>Clear stored bearer token</button>
        {/if}
      </div>
      {#if testResults.opensearch}
        <p class:test-ok={testResults.opensearch.ok} class:test-error={!testResults.opensearch.ok} class="storage-test-status">
          {testResults.opensearch.ok ? `OK (${testResults.opensearch.latency_ms}ms) · ${testResults.opensearch.message}` : `Failed · ${testResults.opensearch.message}`}
        </p>
      {/if}
    </article>
  </div>
</section>
{/if}

<style>
  .storage-tabs { grid-template-columns: repeat(2, minmax(0, 1fr)); margin-top: 14px; }
  .config-path { display: inline-block; margin-bottom: 12px; color: var(--subtle); font-size: calc(12px * var(--text-scale)); }
  .active-pill { display: inline-flex; align-items: center; gap: 5px; color: var(--accent-text); border-color: var(--accent-border); background: var(--accent-soft); }
  .storage-summary-grid { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 14px; margin-top: 12px; }
  .storage-summary-item { display: grid; gap: 4px; padding: 12px; border: 1px solid var(--border); border-radius: 6px; background: var(--hover-bg); }
  .storage-summary-item small { color: var(--subtle); }
  .target-toggles { display: grid; gap: 8px; margin-top: 14px; }
  .target-label { color: var(--muted); font-size: calc(13px * var(--text-scale)); }
  .target-checkboxes { display: flex; flex-wrap: wrap; gap: 16px; }
  .inline-secret-actions { display: flex; flex-wrap: wrap; gap: 12px; margin-top: 10px; }
  .storage-test-status { margin-top: 12px; padding: 8px 10px; border-radius: 5px; font-size: calc(13px * var(--text-scale)); }
  .storage-test-status.test-ok { background: color-mix(in srgb, var(--accent-soft) 70%, transparent); color: var(--text); border: 1px solid var(--accent-border); }
  .storage-test-status.test-error { color: var(--danger, var(--error)); border: 1px solid color-mix(in srgb, var(--danger, var(--error)) 40%, transparent); }
  @media (max-width: 720px) { .storage-summary-grid { grid-template-columns: 1fr; } }
</style>
