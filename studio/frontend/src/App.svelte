<script lang="ts">
  import { Cpu, House, Layers, Network, Plus, RefreshCw, Server, Settings, SlidersHorizontal, Trash2, Unplug } from '@lucide/svelte';
  import { onMount } from 'svelte';

  type StudioStatus = { version: number; service: string; ready: boolean };
  type ConnectionState =
    | { kind: 'loading' }
    | { kind: 'ready'; status: StudioStatus }
    | { kind: 'error'; message: string };
  type ContextSettings = { window: number; trigger_ratio: number; target_ratio: number; recent_ratio: number };
  type LoomSettings = {
    maximum_artifact_mib: number;
    maximum_store_mib: number;
    gc_disabled: boolean;
    gc_trigger_ratio: number;
    gc_target_ratio: number;
    gc_grace_hours: number;
  };
  type ListenerSettings = { config_path: string; host: string; port: number; active_api_keys: number };
  type RoleModelAssignment = {
    role: string;
    configured_model: string;
    effective_model: string;
    reasoning_effort: string;
    inherited: boolean;
  };
  type GatewayProvider = {
    id: string;
    type: string;
    kind: string;
    prefix: string;
    enabled: boolean;
    base_url: string;
    api_key_env: string;
    has_inline_api_key: boolean;
    model_count: number;
    api_key?: string;
    _original_id?: string;
  };
  type ModelOption = {
    id: string;
    context_length?: number;
    reasoning_control?: string;
    reasoning_efforts?: string[];
    default_effort?: string;
    group?: boolean;
  };
  type SettingsSnapshot = {
    version: number;
    scope: 'global';
    runtime: {
      configured: boolean;
      config_path: string;
      max_parallel: number;
      context: ContextSettings;
      loom: LoomSettings;
    };
    models: {
      config_path: string;
      default_model: string;
      default_reasoning_effort: string;
      embedding_model: string;
      embedding_dimensions: number;
      group_count: number;
      roles: RoleModelAssignment[];
    };
    gateway_providers: { config_path: string; items: GatewayProvider[] };
    services: {
      gateway: ListenerSettings;
      system_one: ListenerSettings & { provider_count: number; default_model: string; role_model_count: number };
      remote: ListenerSettings & { authentication_enabled: boolean };
    };
    integrations: {
      mcp: { config_path: string; items: number; bindings: number };
      lsp: { config_path: string; items: number; bindings: number };
    };
  };
  type View = 'overview' | 'settings';
  type SettingsSection = 'models' | 'providers' | 'runtime' | 'services' | 'integrations';
  type SaveState = { kind: 'idle' | 'saving' | 'saved' | 'error'; message?: string };

  const navigation = [
    { label: 'Overview', icon: House, view: 'overview' as View },
    { label: 'Sessions', icon: Layers, disabled: true },
    { label: 'Settings', icon: Settings, view: 'settings' as View }
  ];
  const settingsSections = [
    { id: 'models' as const, label: 'Models', description: 'Chat, embedding, and role assignments', icon: Cpu },
    { id: 'providers' as const, label: 'Providers', description: 'Gateway upstream providers', icon: Network },
    { id: 'runtime' as const, label: 'Runtime', description: 'Execution, context, and storage', icon: SlidersHorizontal },
    { id: 'services' as const, label: 'Services', description: 'Gateway, System One, and Remote', icon: Server },
    { id: 'integrations' as const, label: 'Integrations', description: 'MCP and language servers', icon: Unplug }
  ];

  let connection: ConnectionState = { kind: 'loading' };
  let activeView: View = 'overview';
  let activeSection: SettingsSection = 'models';
  let settings: SettingsSnapshot | null = null;
  let settingsError = '';
  let saveState: SaveState = { kind: 'idle' };
  let saveQueue = Promise.resolve();
  let savedTimer: ReturnType<typeof setTimeout> | undefined;
  let modelOptions: ModelOption[] = [];
  let modelsLoading = false;
  let modelsError = '';

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

  async function loadSettings() {
    settingsError = '';
    try {
      const response = await fetch('/api/v1/settings', { headers: { Accept: 'application/json' } });
      if (!response.ok) throw new Error(await responseError(response));
      adoptSettings((await response.json()) as SettingsSnapshot);
      if (activeSection === 'models' && modelOptions.length === 0) void loadModels();
    } catch (error) {
      settingsError = error instanceof Error ? error.message : 'Settings are unavailable';
    }
  }

  function adoptSettings(snapshot: SettingsSnapshot) {
    for (const provider of snapshot.gateway_providers.items) provider._original_id = provider.id;
    settings = snapshot;
  }

  async function loadModels() {
    modelsLoading = true;
    modelsError = '';
    try {
      const response = await fetch('/api/v1/settings/models', { headers: { Accept: 'application/json' } });
      if (!response.ok) throw new Error(await responseError(response));
      const catalog = (await response.json()) as { models: ModelOption[] };
      modelOptions = catalog.models;
    } catch (error) {
      modelsError = error instanceof Error ? error.message : 'Models are unavailable';
    } finally {
      modelsLoading = false;
    }
  }

  function chooseSettingsSection(section: SettingsSection) {
    activeSection = section;
    const url = new URL(window.location.href);
    url.searchParams.set('section', section);
    window.history.pushState({}, '', url.pathname + url.search);
    if (section === 'models' && modelOptions.length === 0 && !modelsLoading) void loadModels();
  }

  function sectionFromLocation(): SettingsSection {
    const section = new URL(window.location.href).searchParams.get('section');
    return settingsSections.some((candidate) => candidate.id === section) ? section as SettingsSection : 'models';
  }

  function navigate(view: View) {
    activeView = view;
    const path = view === 'settings' ? '/settings' : '/';
    if (window.location.pathname !== path) window.history.pushState({}, '', path);
    if (view === 'settings' && !settings) void loadSettings();
  }

  function queueSave(request: () => Promise<SettingsSnapshot>) {
    if (savedTimer) clearTimeout(savedTimer);
    saveState = { kind: 'saving' };
    saveQueue = saveQueue
      .then(async () => {
        adoptSettings(await request());
        saveState = { kind: 'saved' };
        savedTimer = setTimeout(() => (saveState = { kind: 'idle' }), 2400);
      })
      .catch((error) => {
        saveState = { kind: 'error', message: error instanceof Error ? error.message : 'Could not save settings' };
      });
  }

  function saveModelAssignment(target: string, assignment: { model: string; reasoning_effort: string; embedding_dimensions?: number }) {
    queueSave(() => putSettings(`/api/v1/settings/models/${encodeURIComponent(target)}`, JSON.stringify(assignment)));
  }

  function saveDefaultModel() {
    if (!settings) return;
    saveModelAssignment('default', {
      model: settings.models.default_model,
      reasoning_effort: settings.models.default_reasoning_effort
    });
  }

  function saveEmbeddingModel() {
    if (!settings) return;
    saveModelAssignment('embedding', {
      model: settings.models.embedding_model,
      reasoning_effort: '',
      embedding_dimensions: settings.models.embedding_dimensions
    });
  }

  function saveRoleModel(role: RoleModelAssignment) {
    saveModelAssignment(role.role, { model: role.configured_model, reasoning_effort: role.reasoning_effort });
  }

  function reasoningOptions(model: string, current: string) {
    const discovered = modelOptions.find((candidate) => candidate.id === model);
    const common = ['minimal', 'low', 'medium', 'high', 'xhigh', 'max', 'ultra'];
    const values = discovered?.reasoning_control === 'effort' && discovered.reasoning_efforts?.length
      ? [...discovered.reasoning_efforts]
      : model.startsWith('group/') || current ? common : [];
    if (current && !values.includes(current)) values.push(current);
    return [...new Set(values)];
  }

  function roleLabel(role: string) {
    return role.split('-').map((part) => part.charAt(0).toUpperCase() + part.slice(1)).join(' ');
  }

  function providerKinds(type: string) {
    if (type !== 'openai-compatible') {
      const kind = type === 'xai' ? 'grok' : type === 'codex' ? 'codex' : type;
      return [{ value: '', label: 'Auto' }, { value: kind, label: roleLabel(kind) }];
    }
    return [
      { value: '', label: 'Auto' }, { value: 'generic', label: 'Generic' }, { value: 'openai', label: 'OpenAI' },
      { value: 'openrouter', label: 'OpenRouter' }, { value: 'grok', label: 'Grok / xAI' },
      { value: 'anthropic', label: 'Anthropic' }, { value: 'codex', label: 'Codex' }
    ];
  }

  function saveProvider(provider: GatewayProvider, clearAPIKey = false) {
    const originalID = provider._original_id || provider.id;
    const payload = JSON.stringify({
      id: provider.id, type: provider.type, kind: provider.kind, prefix: provider.prefix,
      enabled: provider.enabled, base_url: provider.base_url, api_key_env: provider.api_key_env,
      api_key: provider.api_key || '', clear_api_key: clearAPIKey
    });
    queueSave(() => putSettings(`/api/v1/settings/gateway/providers/${encodeURIComponent(originalID)}`, payload));
  }

  function changeProviderType(provider: GatewayProvider) {
    provider.kind = '';
    if (provider.type === 'openai-compatible') {
      if (!provider.base_url) provider.base_url = 'https://api.openai.com/v1';
      if (!provider.api_key_env) provider.api_key_env = 'OPENAI_API_KEY';
    }
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
    queueSave(() => writeSettings('DELETE', `/api/v1/settings/gateway/providers/${encodeURIComponent(provider._original_id || provider.id)}`));
  }

  function clearProviderKey(provider: GatewayProvider) {
    if (!window.confirm(`Clear the stored inline API key for “${provider.id}”?`)) return;
    saveProvider(provider, true);
  }

  function saveRuntime() {
    if (!settings) return;
    const payload = JSON.stringify({
      max_parallel: settings.runtime.max_parallel,
      context: settings.runtime.context,
      loom: settings.runtime.loom
    });
    queueSave(() => putSettings('/api/v1/settings/runtime', payload));
  }

  function saveService(name: 'gateway' | 'system-one' | 'remote') {
    if (!settings) return;
    const service = name === 'system-one' ? settings.services.system_one : settings.services[name];
    const payload = JSON.stringify({
      host: service.host,
      port: service.port,
      authentication_enabled: name === 'remote' ? settings.services.remote.authentication_enabled : false
    });
    queueSave(() => putSettings(`/api/v1/settings/services/${name}`, payload));
  }

  async function putSettings(url: string, body: string): Promise<SettingsSnapshot> {
    return writeSettings('PUT', url, body);
  }

  async function writeSettings(method: 'POST' | 'PUT' | 'DELETE', url: string, body?: string): Promise<SettingsSnapshot> {
    const response = await fetch(url, { method, headers: body ? { 'Content-Type': 'application/json' } : undefined, body });
    if (!response.ok) throw new Error(await responseError(response));
    return (await response.json()) as SettingsSnapshot;
  }

  async function responseError(response: Response) {
    try {
      const body = (await response.json()) as { error?: string };
      return body.error || `Studio returned ${response.status}`;
    } catch {
      return `Studio returned ${response.status}`;
    }
  }

  onMount(() => {
    activeView = window.location.pathname.startsWith('/settings') ? 'settings' : 'overview';
    activeSection = sectionFromLocation();
    void loadStatus();
    if (activeView === 'settings') void loadSettings();
    const onPopState = () => {
      activeView = window.location.pathname.startsWith('/settings') ? 'settings' : 'overview';
      activeSection = sectionFromLocation();
      if (activeView === 'settings' && !settings) void loadSettings();
    };
    window.addEventListener('popstate', onPopState);
    return () => window.removeEventListener('popstate', onPopState);
  });
</script>

<svelte:head><meta name="description" content="Q Studio local agent control surface" /></svelte:head>

<div class="app-shell">
  <aside class="sidebar" aria-label="Studio navigation">
    <div class="brand"><span class="brand-mark">Q</span><span>Studio</span></div>
    <nav>
      {#each navigation as item}
        {@const Icon = item.icon}
        <button
          class:active={!item.disabled && item.view === activeView}
          disabled={item.disabled}
          aria-current={!item.disabled && item.view === activeView ? 'page' : undefined}
          title={item.disabled ? 'Available with session management' : undefined}
          onclick={() => item.view && navigate(item.view)}
        ><Icon aria-hidden="true" size={19} strokeWidth={1.7} /><span>{item.label}</span></button>
      {/each}
    </nav>
  </aside>

  <main class:settings-main={activeView === 'settings'}>
    <header class="page-header">
      <div><h1>{activeView === 'settings' ? 'Settings' : 'Studio overview'}</h1>{#if activeView === 'settings'}<p class="page-description">Global configuration shared by Q sessions.</p>{/if}</div>
      <div class="connection" aria-live="polite"><span class:online={connection.kind === 'ready'} class="status-dot" aria-hidden="true"></span><span>{connection.kind === 'ready' ? 'Connected' : connection.kind === 'error' ? 'Disconnected' : 'Connecting'}</span></div>
    </header>

    {#if activeView === 'overview'}
      <section class="runtime-panel" aria-labelledby="runtime-heading">
        <h2 id="runtime-heading">Runtime</h2>
        <div class="runtime-body"><dl><div><dt>Local endpoint</dt><dd>{window.location.origin}</dd></div><div><dt>Status</dt><dd class:success={connection.kind === 'ready'}>{connection.kind === 'ready' ? 'Connected' : connection.kind === 'error' ? connection.message : 'Connecting…'}</dd></div></dl>{#if connection.kind === 'error'}<button class="retry" onclick={loadStatus}>Retry connection</button>{/if}</div>
      </section>
      <section class="empty-session" aria-labelledby="empty-heading"><div class="session-outline" aria-hidden="true"><span></span><span></span><span></span></div><h2 id="empty-heading">No session selected</h2><p>Choose a session to establish workspace context.</p></section>
    {:else}
      <div class="settings-layout">
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
          {:else if activeSection === 'models'}
            <section class="settings-section">
              <div class="section-heading">
                <div><p class="eyebrow">GLOBAL MODELS</p><h2>Model assignments</h2><p>Choose Gateway models for chat, embedding, and agent roles.</p></div>
                <button class="icon-button" title="Refresh Gateway models" onclick={loadModels} disabled={modelsLoading}><RefreshCw aria-hidden="true" size={16} class={modelsLoading ? 'spin' : undefined} /></button>
              </div>
              <code class="config-path">{settings.models.config_path}</code>
              {#if modelsError}<p class="inline-warning">{modelsError}</p>{/if}
              <div class="settings-card assignment-list">
                <div class="assignment-row primary-assignment">
                  <div><strong>Default</strong><small>Primary chat model and role fallback</small></div>
                  <label><span>Model</span><select bind:value={settings.models.default_model} onchange={saveDefaultModel} disabled={modelsLoading}>
                    {#if settings.models.default_model && !modelOptions.some((model) => model.id === settings?.models.default_model)}<option value={settings.models.default_model}>{settings.models.default_model}</option>{/if}
                    {#each modelOptions as model}<option value={model.id}>{model.id}{model.group ? ' · group' : ''}</option>{/each}
                  </select></label>
                  <label><span>Reasoning</span><select bind:value={settings.models.default_reasoning_effort} onchange={saveDefaultModel}><option value="">Provider default</option>{#each reasoningOptions(settings.models.default_model, settings.models.default_reasoning_effort) as effort}<option value={effort}>{effort}</option>{/each}</select></label>
                </div>
                <div class="assignment-row primary-assignment">
                  <div><strong>Embedding</strong><small>Semantic indexes and skill search</small></div>
                  <label><span>Model</span><select bind:value={settings.models.embedding_model} onchange={saveEmbeddingModel} disabled={modelsLoading}>
                    <option value="">Disabled</option>
                    {#if settings.models.embedding_model && !modelOptions.some((model) => model.id === settings?.models.embedding_model)}<option value={settings.models.embedding_model}>{settings.models.embedding_model}</option>{/if}
                    {#each modelOptions.filter((model) => !model.group) as model}<option value={model.id}>{model.id}</option>{/each}
                  </select></label>
                  <label><span>Dimensions</span><input type="number" min="1" max="4096" bind:value={settings.models.embedding_dimensions} onchange={saveEmbeddingModel} disabled={!settings.models.embedding_model} /></label>
                </div>
                {#each settings.models.roles as role}
                  <div class="assignment-row">
                    <div><strong>{roleLabel(role.role)}</strong><small>{role.inherited ? `Inherits ${role.effective_model}` : role.role}</small></div>
                    <label><span>Model</span><select bind:value={role.configured_model} onchange={() => saveRoleModel(role)} disabled={modelsLoading}>
                      <option value="">Inherit</option>
                      {#if role.configured_model && !modelOptions.some((model) => model.id === role.configured_model)}<option value={role.configured_model}>{role.configured_model}</option>{/if}
                      {#each modelOptions as model}<option value={model.id}>{model.id}{model.group ? ' · group' : ''}</option>{/each}
                    </select></label>
                    <label><span>Reasoning</span><select bind:value={role.reasoning_effort} onchange={() => saveRoleModel(role)}><option value="">Provider default</option>{#each reasoningOptions(role.configured_model || role.effective_model, role.reasoning_effort) as effort}<option value={effort}>{effort}</option>{/each}</select></label>
                  </div>
                {/each}
              </div>
            </section>
          {:else if activeSection === 'providers'}
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
                      <label><span>API type</span><select bind:value={provider.type} onchange={() => changeProviderType(provider)}><option value="openai-compatible">OpenAI compatible</option><option value="openrouter">OpenRouter</option><option value="xai">xAI</option><option value="anthropic">Anthropic</option><option value="codex">Codex App Server</option></select></label>
                      <label><span>Provider kind</span><select bind:value={provider.kind} onchange={() => saveProvider(provider)}>{#each providerKinds(provider.type) as kind}<option value={kind.value}>{kind.label}</option>{/each}</select></label>
                      <label class="wide-field"><span>Base URL</span><input placeholder="Provider default" bind:value={provider.base_url} onchange={() => saveProvider(provider)} disabled={provider.type === 'codex'} /></label>
                      <label><span>API key environment</span><input placeholder="Optional" bind:value={provider.api_key_env} onchange={() => saveProvider(provider)} disabled={provider.type === 'codex'} /></label>
                      <label><span>New inline API key</span><input type="password" autocomplete="new-password" placeholder={provider.has_inline_api_key ? 'Stored · enter to replace' : 'Optional'} bind:value={provider.api_key} onchange={() => saveProvider(provider)} disabled={provider.type === 'codex'} /></label>
                    </div>
                    {#if provider.has_inline_api_key}<button class="text-button danger-text" onclick={() => clearProviderKey(provider)}>Clear stored inline key</button>{/if}
                  </article>
                {:else}
                  <div class="empty-card"><p>No Gateway providers configured.</p><button class="primary-button" onclick={addProvider}><Plus aria-hidden="true" size={16} /> Add provider</button></div>
                {/each}
              </div>
            </section>
          {:else if activeSection === 'runtime'}
            <section class="settings-section">
              <div class="section-heading"><div><p class="eyebrow">GLOBAL RUNTIME</p><h2>Execution and storage</h2></div><code>{settings.runtime.config_path}</code></div>
              {#if !settings.runtime.configured}<p class="inline-warning">Complete the initial model setup in Q before editing runtime settings.</p>{/if}
              <div class="settings-card"><div class="card-heading"><div><h3>Agent execution</h3><p>Limit concurrent delegated agent work.</p></div></div><label class="field-row"><span><strong>Maximum parallel agents</strong><small>Applies across built-in roles.</small></span><input type="number" min="1" max="64" bind:value={settings.runtime.max_parallel} onchange={saveRuntime} disabled={!settings.runtime.configured} /></label></div>
              <div class="settings-card"><div class="card-heading"><div><h3>Context compaction</h3><p>Control when and how Q compacts long conversations.</p></div></div><div class="field-grid"><label><span>Context window</span><input type="number" min="0" step="1000" bind:value={settings.runtime.context.window} onchange={saveRuntime} disabled={!settings.runtime.configured} /></label><label><span>Trigger ratio</span><input type="number" min="0.01" max="0.99" step="0.01" bind:value={settings.runtime.context.trigger_ratio} onchange={saveRuntime} disabled={!settings.runtime.configured} /></label><label><span>Target ratio</span><input type="number" min="0.01" max="0.98" step="0.01" bind:value={settings.runtime.context.target_ratio} onchange={saveRuntime} disabled={!settings.runtime.configured} /></label><label><span>Recent ratio</span><input type="number" min="0.01" max="0.97" step="0.01" bind:value={settings.runtime.context.recent_ratio} onchange={saveRuntime} disabled={!settings.runtime.configured} /></label></div></div>
              <div class="settings-card"><div class="card-heading"><div><h3>Loom storage</h3><p>Bound artifact storage and garbage collection.</p></div></div><div class="field-grid"><label><span>Maximum artifact MiB</span><input type="number" min="1" max="1024" bind:value={settings.runtime.loom.maximum_artifact_mib} onchange={saveRuntime} disabled={!settings.runtime.configured} /></label><label><span>Maximum store MiB</span><input type="number" min="1" max="10240" bind:value={settings.runtime.loom.maximum_store_mib} onchange={saveRuntime} disabled={!settings.runtime.configured} /></label><label><span>GC trigger ratio</span><input type="number" min="0.02" max="0.99" step="0.01" bind:value={settings.runtime.loom.gc_trigger_ratio} onchange={saveRuntime} disabled={!settings.runtime.configured} /></label><label><span>GC target ratio</span><input type="number" min="0.01" max="0.98" step="0.01" bind:value={settings.runtime.loom.gc_target_ratio} onchange={saveRuntime} disabled={!settings.runtime.configured} /></label><label><span>GC grace hours</span><input type="number" min="1" max="8760" bind:value={settings.runtime.loom.gc_grace_hours} onchange={saveRuntime} disabled={!settings.runtime.configured} /></label><label class="toggle-field"><span>Disable garbage collection</span><input type="checkbox" bind:checked={settings.runtime.loom.gc_disabled} onchange={saveRuntime} disabled={!settings.runtime.configured} /><small>{settings.runtime.loom.gc_disabled ? 'Disabled' : 'Enabled'}</small></label></div></div>
            </section>
          {:else if activeSection === 'services'}
            <section class="settings-section">
              <div class="section-heading"><div><p class="eyebrow">SERVICES</p><h2>Listener configuration</h2><p>Network changes apply when the corresponding service restarts.</p></div></div>
              <div class="service-grid">
                <article class="settings-card service-card">
                  <div class="card-heading"><div><h3>Gateway</h3><p>OpenAI-compatible model gateway.</p></div><span class="count-badge">{settings.services.gateway.active_api_keys} keys</span></div>
                  <label><span>Host</span><input bind:value={settings.services.gateway.host} onchange={() => saveService('gateway')} /></label><label><span>Port</span><input type="number" min="0" max="65535" bind:value={settings.services.gateway.port} onchange={() => saveService('gateway')} /></label><code>{settings.services.gateway.config_path}</code>
                </article>
                <article class="settings-card service-card">
                  <div class="card-heading"><div><h3>System One</h3><p>{settings.services.system_one.provider_count} providers · {settings.services.system_one.default_model}</p></div><span class="count-badge">{settings.services.system_one.active_api_keys} keys</span></div>
                  <label><span>Host</span><input bind:value={settings.services.system_one.host} onchange={() => saveService('system-one')} /></label><label><span>Port</span><input type="number" min="0" max="65535" bind:value={settings.services.system_one.port} onchange={() => saveService('system-one')} /></label><code>{settings.services.system_one.config_path}</code>
                </article>
                <article class="settings-card service-card">
                  <div class="card-heading"><div><h3>Remote</h3><p>Workspace session and agent execution API.</p></div><span class="count-badge">{settings.services.remote.active_api_keys} keys</span></div>
                  <label><span>Host</span><input bind:value={settings.services.remote.host} onchange={() => saveService('remote')} /></label><label><span>Port</span><input type="number" min="0" max="65535" bind:value={settings.services.remote.port} onchange={() => saveService('remote')} /></label><label class="toggle-field"><span>Require authentication</span><input type="checkbox" bind:checked={settings.services.remote.authentication_enabled} onchange={() => saveService('remote')} /><small>{settings.services.remote.authentication_enabled ? 'Enabled' : 'Disabled'}</small></label><code>{settings.services.remote.config_path}</code>
                </article>
              </div>
            </section>
          {:else}
            <section class="settings-section">
              <div class="section-heading"><div><p class="eyebrow">INTEGRATIONS</p><h2>Tool connections</h2><p>Current global configuration discovered from Q.</p></div></div>
              <div class="integration-grid">
                <article class="settings-card integration-card"><div><h3>MCP servers</h3><p>External tools and role grants.</p></div><div class="integration-stats"><strong>{settings.integrations.mcp.items}</strong><span>servers</span><strong>{settings.integrations.mcp.bindings}</strong><span>role bindings</span></div><code>{settings.integrations.mcp.config_path}</code></article>
                <article class="settings-card integration-card"><div><h3>Language servers</h3><p>Trusted executables and language mappings.</p></div><div class="integration-stats"><strong>{settings.integrations.lsp.items}</strong><span>servers</span><strong>{settings.integrations.lsp.bindings}</strong><span>language bindings</span></div><code>{settings.integrations.lsp.config_path}</code></article>
              </div>
            </section>
          {/if}
        </div>
      </div>
    {/if}
  </main>

  <footer class="status-bar"><div><span>Q Studio</span><span class="divider" aria-hidden="true"></span><span class:online={connection.kind === 'ready'} class="status-dot" aria-hidden="true"></span><span>{connection.kind === 'ready' ? 'Ready' : connection.kind === 'error' ? 'Unavailable' : 'Connecting'}</span></div><span>{activeView === 'settings' ? 'Global settings' : 'Local'}</span></footer>
</div>
