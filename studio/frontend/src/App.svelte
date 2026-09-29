<script lang="ts">
  import { Activity, Bot, BrainCircuit, Check, ChevronDown, ChevronUp, CircleHelp, Copy, Cpu, GitCompareArrows, House, KeyRound, Layers, Network, Plus, RefreshCw, Server, Settings, SlidersHorizontal, Trash2, Unplug } from '@lucide/svelte';
  import { onMount } from 'svelte';
  import ChangesView from './ChangesView.svelte';
  import IntegrationsView from './IntegrationsView.svelte';
  import HelpView from './HelpView.svelte';
  import OperationsView from './OperationsView.svelte';
  import SessionView from './SessionView.svelte';
  import SubagentsView from './SubagentsView.svelte';
  import WorkspaceModels from './WorkspaceModels.svelte';

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
    custom: boolean;
  };
  type ModelCandidate = { model: string; reasoning_effort: string; timeout: string };
  type ModelGroup = { name: string; candidates: ModelCandidate[] };
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
    api_mode?: string;
    context_override?: number;
  };
  type SystemOneProvider = { id: string; uri: string; api_key_env: string; _original_id?: string };
  type SystemOneModelOption = { id: string; description?: string; release_date?: string };
  type ServiceAPIKey = { id: string; alias: string; created_at?: string; revoked_at?: string; legacy?: boolean };
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
      groups: ModelGroup[];
      roles: RoleModelAssignment[];
    };
    gateway_providers: { config_path: string; items: GatewayProvider[]; api_keys: ServiceAPIKey[] };
    system_one: {
      config_path: string;
      default_model: string;
      agent_skill_model: string;
      providers: SystemOneProvider[];
      api_keys: ServiceAPIKey[];
      active_api_keys: number;
    };
    services: {
      gateway: ListenerSettings;
      system_one: ListenerSettings & { provider_count: number; default_model: string; role_model_count: number };
      library: ListenerSettings;
    };
    integrations: {
      mcp: { config_path: string; items: number; bindings: number };
      lsp: { config_path: string; items: number; bindings: number };
    };
  };
  type View = 'overview' | 'sessions' | 'changes' | 'operations' | 'settings' | 'help';
  type SettingsSection = 'models' | 'providers' | 'system-one' | 'runtime' | 'services' | 'subagents' | 'integrations';
  type SaveState = { kind: 'idle' | 'saving' | 'saved' | 'error'; message?: string };
  type LoomStats = { artifacts: number; blobs: number; bytes: number };
  type LoomGCResult = { artifacts_removed: number; blobs_removed: number; bytes_reclaimed: number; dry_run: boolean };

  const navigation = [
    { label: 'Overview', icon: House, view: 'overview' as View },
    { label: 'Sessions', icon: Layers, view: 'sessions' as View },
    { label: 'Changes', icon: GitCompareArrows, view: 'changes' as View },
    { label: 'Operations', icon: Activity, view: 'operations' as View },
    { label: 'Settings', icon: Settings, view: 'settings' as View },
    { label: 'Help', icon: CircleHelp, view: 'help' as View }
  ];
  const settingsSections = [
    { id: 'models' as const, label: 'Models', description: 'Chat, embedding, and role assignments', icon: Cpu },
    { id: 'providers' as const, label: 'Providers', description: 'Gateway upstream providers', icon: Network },
    { id: 'system-one' as const, label: 'System One', description: 'Decision API and access keys', icon: BrainCircuit },
    { id: 'runtime' as const, label: 'Runtime', description: 'Execution, context, and storage', icon: SlidersHorizontal },
    { id: 'services' as const, label: 'Services', description: 'Gateway and System One', icon: Server },
    { id: 'subagents' as const, label: 'Subagents', description: 'Profiles, delegation, and ACP', icon: Bot },
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
  let systemOneModels: SystemOneModelOption[] = [];
  let systemOneModelsLoading = false;
  let systemOneModelsError = '';
  let systemOneKeyAlias = '';
  let generatedSystemOneKey = '';
  let systemOneKeyCopied = false;
  let gatewayKeyAlias = '';
  let generatedGatewayKey = '';
  let gatewayKeyCopied = false;
  let newModelGroupName = '';
  let newCustomRoleName = '';
  let loomStats: LoomStats | null = null;
  let loomResult: LoomGCResult | null = null;
  let loomLoading = false;
  let loomError = '';

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
      if (activeSection === 'system-one' && systemOneModels.length === 0) void loadSystemOneModels();
      if (activeSection === 'runtime') void loadLoomStatus();
    } catch (error) {
      settingsError = error instanceof Error ? error.message : 'Settings are unavailable';
    }
  }

  function adoptSettings(snapshot: SettingsSnapshot) {
    for (const provider of snapshot.gateway_providers.items) provider._original_id = provider.id;
    for (const provider of snapshot.system_one.providers) provider._original_id = provider.id;
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

  function chooseSettingsSection(section: SettingsSection) {
    activeSection = section;
    const url = new URL(window.location.href);
    url.searchParams.set('section', section);
    window.history.pushState({}, '', url.pathname + url.search);
    if (section === 'models' && modelOptions.length === 0 && !modelsLoading) void loadModels();
    if (section === 'system-one' && systemOneModels.length === 0 && !systemOneModelsLoading) void loadSystemOneModels();
    if (section === 'runtime') void loadLoomStatus();
  }

  function sectionFromLocation(): SettingsSection {
    const section = new URL(window.location.href).searchParams.get('section');
    return settingsSections.some((candidate) => candidate.id === section) ? section as SettingsSection : 'models';
  }

  function navigate(view: View) {
    activeView = view;
    const path = view === 'overview' ? '/' : `/${view}`;
    if (window.location.pathname !== path) window.history.pushState({}, '', path);
    if (view === 'settings' && !settings) void loadSettings();
  }

  function viewFromLocation(): View {
    if (window.location.pathname.startsWith('/settings')) return 'settings';
    if (window.location.pathname.startsWith('/sessions')) return 'sessions';
    if (window.location.pathname.startsWith('/changes')) return 'changes';
    if (window.location.pathname.startsWith('/operations')) return 'operations';
    if (window.location.pathname.startsWith('/help')) return 'help';
    return 'overview';
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

  function saveModelAssignment(target: string, assignment: { model: string; reasoning_effort: string; embedding_dimensions?: number; workspace_root?: string }) {
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
      embedding_dimensions: settings.models.embedding_dimensions,
      workspace_root: localStorage.getItem('q-studio-workspace-root') || ''
    });
  }

  function saveRoleModel(role: RoleModelAssignment) {
    saveModelAssignment(role.role, { model: role.configured_model, reasoning_effort: role.reasoning_effort });
  }

  function saveModelGroup(group: ModelGroup) {
    queueSave(async () => {
      const snapshot = await putSettings('/api/v1/settings/model-groups', JSON.stringify(group));
      await loadModels();
      return snapshot;
    });
  }

  function addModelGroup() {
    const name = newModelGroupName.trim();
    const model = modelOptions.find((option) => !option.group)?.id || '';
    if (!name || !model) return;
    newModelGroupName = '';
    saveModelGroup({ name, candidates: [{ model, reasoning_effort: '', timeout: '' }] });
  }

  function deleteModelGroup(group: ModelGroup) {
    if (!window.confirm(`Delete model group “${group.name}”?`)) return;
    queueSave(async () => {
      const snapshot = await writeSettings('DELETE', `/api/v1/settings/model-groups/${encodeURIComponent(group.name)}`);
      await loadModels();
      return snapshot;
    });
  }

  function addGroupCandidate(group: ModelGroup) {
    const used = new Set(group.candidates.map((candidate) => candidate.model));
    const model = modelOptions.find((option) => !option.group && !used.has(option.id))?.id;
    if (!model) return;
    group.candidates = [...group.candidates, { model, reasoning_effort: '', timeout: '' }];
    saveModelGroup(group);
  }

  function removeGroupCandidate(group: ModelGroup, index: number) {
    if (group.candidates.length <= 1) return;
    group.candidates = group.candidates.filter((_, candidateIndex) => candidateIndex !== index);
    saveModelGroup(group);
  }

  function moveGroupCandidate(group: ModelGroup, index: number, direction: -1 | 1) {
    const target = index + direction;
    if (target < 0 || target >= group.candidates.length) return;
    const candidates = [...group.candidates];
    [candidates[index], candidates[target]] = [candidates[target], candidates[index]];
    group.candidates = candidates;
    saveModelGroup(group);
  }

  function addCustomRole() {
    const role = newCustomRoleName.trim();
    if (!role) return;
    newCustomRoleName = '';
    queueSave(() => writeSettings('POST', '/api/v1/settings/roles', JSON.stringify({ role })));
  }

  function deleteCustomRole(role: RoleModelAssignment) {
    if (!window.confirm(`Delete custom role “${role.role}”?`)) return;
    const root = localStorage.getItem('q-studio-workspace-root') || '';
    const query = root ? `?workspace_root=${encodeURIComponent(root)}` : '';
    queueSave(() => writeSettings('DELETE', `/api/v1/settings/roles/${encodeURIComponent(role.role)}${query}`));
  }

  function saveModelAPIMode(model: ModelOption) {
    queueSave(() => putSettings('/api/v1/settings/model-api-mode', JSON.stringify({ model: model.id, mode: model.api_mode || 'chat_completions' })));
  }

  function saveModelContextOverride(model: ModelOption) {
    queueSave(() => putSettings('/api/v1/settings/gateway/model-metadata', JSON.stringify({ model: model.id, context_window: model.context_override || 0 })));
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

  function saveSystemOneModel(target: 'default' | 'agent-skill-decision', model: string) {
    queueSave(() => putSettings(`/api/v1/settings/system-one/models/${target}`, JSON.stringify({ model })));
  }

  function saveSystemOneProvider(provider: SystemOneProvider) {
    const originalID = provider._original_id || provider.id;
    queueSave(() => putSettings(
      `/api/v1/settings/system-one/providers/${encodeURIComponent(originalID)}`,
      JSON.stringify({ id: provider.id, uri: provider.uri, api_key_env: provider.api_key_env })
    ));
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
    queueSave(() => writeSettings('DELETE', `/api/v1/settings/system-one/providers/${encodeURIComponent(provider._original_id || provider.id)}`));
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

  async function copySystemOneAPIKey() {
    if (!generatedSystemOneKey) return;
    await navigator.clipboard.writeText(generatedSystemOneKey);
    systemOneKeyCopied = true;
  }

  function formatKeyDate(value?: string) {
    if (!value) return 'Imported key';
    return new Intl.DateTimeFormat(undefined, { dateStyle: 'medium', timeStyle: 'short' }).format(new Date(value));
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

  async function loadLoomStatus() {
    const root = localStorage.getItem('q-studio-workspace-root') || '';
    loomError = '';
    loomResult = null;
    if (!root) {
      loomStats = null;
      loomError = 'Open a repository in Sessions to inspect its Loom store.';
      return;
    }
    loomLoading = true;
    try {
      const response = await fetch(`/api/v1/workspaces/loom?workspace_root=${encodeURIComponent(root)}`);
      if (!response.ok) throw new Error(await responseError(response));
      loomStats = ((await response.json()) as { stats: LoomStats }).stats;
    } catch (error) {
      loomError = error instanceof Error ? error.message : 'Could not inspect Loom storage';
    } finally {
      loomLoading = false;
    }
  }

  async function collectLoom(dryRun: boolean) {
    const root = localStorage.getItem('q-studio-workspace-root') || '';
    if (!root || loomLoading) return;
    if (!dryRun && !window.confirm('Run Loom garbage collection for the current repository?')) return;
    loomLoading = true;
    loomError = '';
    try {
      const response = await fetch('/api/v1/workspaces/loom/collect', {
        method: 'POST', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ workspace_root: root, dry_run: dryRun })
      });
      if (!response.ok) throw new Error(await responseError(response));
      const result = (await response.json()) as { stats: LoomStats; result: LoomGCResult };
      loomStats = result.stats;
      loomResult = result.result;
    } catch (error) {
      loomError = error instanceof Error ? error.message : 'Loom garbage collection failed';
    } finally {
      loomLoading = false;
    }
  }

  function formatBytes(value: number) {
    if (value < 1024) return `${value} B`;
    if (value < 1024 * 1024) return `${(value / 1024).toFixed(1)} KiB`;
    return `${(value / (1024 * 1024)).toFixed(1)} MiB`;
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
    activeView = viewFromLocation();
    activeSection = sectionFromLocation();
    void loadStatus();
    if (activeView === 'settings') void loadSettings();
    const onPopState = () => {
      activeView = viewFromLocation();
      activeSection = sectionFromLocation();
      if (activeView === 'settings' && !settings) void loadSettings();
    };
    const onShortcut = (event: KeyboardEvent) => {
      const target = event.target as HTMLElement | null;
      const editing = !!target?.closest('input, textarea, select, [contenteditable="true"]');
      if ((event.ctrlKey || event.metaKey) && event.key.toLowerCase() === 'k') {
        event.preventDefault();
        document.querySelector<HTMLButtonElement>('.sidebar nav button.active, .sidebar nav button')?.focus();
      } else if (!editing && event.key === '?') {
        event.preventDefault();
        navigate('help');
      }
    };
    window.addEventListener('popstate', onPopState);
    window.addEventListener('keydown', onShortcut);
    return () => {
      window.removeEventListener('popstate', onPopState);
      window.removeEventListener('keydown', onShortcut);
    };
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
          class:active={item.view === activeView}
          aria-current={item.view === activeView ? 'page' : undefined}
          onclick={() => item.view && navigate(item.view)}
        ><Icon aria-hidden="true" size={19} strokeWidth={1.7} /><span>{item.label}</span></button>
      {/each}
    </nav>
  </aside>

  <main class:settings-main={activeView === 'settings'} class:sessions-main={activeView === 'sessions'} class:changes-main={activeView === 'changes'}>
    <header class="page-header">
      <div><h1>{activeView === 'settings' ? 'Settings' : activeView === 'sessions' ? 'Sessions' : activeView === 'changes' ? 'Changes' : activeView === 'operations' ? 'Operations' : activeView === 'help' ? 'Help' : 'Studio overview'}</h1>{#if activeView === 'settings'}<p class="page-description">Global and repository configuration shared by Q sessions.</p>{:else if activeView === 'sessions'}<p class="page-description">Navigate registered root sessions and their delegated work.</p>{:else if activeView === 'changes'}<p class="page-description">Inspect bounded diffs and review commits.</p>{:else if activeView === 'operations'}<p class="page-description">Usage, workers, local services, logs, and retention.</p>{:else if activeView === 'help'}<p class="page-description">Studio workflows, shortcuts, and recovery.</p>{/if}</div>
      <div class="connection" aria-live="polite"><span class:online={connection.kind === 'ready'} class="status-dot" aria-hidden="true"></span><span>{connection.kind === 'ready' ? 'Connected' : connection.kind === 'error' ? 'Disconnected' : 'Connecting'}</span></div>
    </header>

    {#if activeView === 'overview'}
      <section class="runtime-panel" aria-labelledby="runtime-heading">
        <h2 id="runtime-heading">Runtime</h2>
        <div class="runtime-body"><dl><div><dt>Local endpoint</dt><dd>{window.location.origin}</dd></div><div><dt>Status</dt><dd class:success={connection.kind === 'ready'}>{connection.kind === 'ready' ? 'Connected' : connection.kind === 'error' ? connection.message : 'Connecting…'}</dd></div></dl>{#if connection.kind === 'error'}<button class="retry" onclick={loadStatus}>Retry connection</button>{/if}</div>
      </section>
      <section class="empty-session" aria-labelledby="empty-heading"><div class="session-outline" aria-hidden="true"><span></span><span></span><span></span></div><h2 id="empty-heading">No session selected</h2><p>Open Sessions to register a root session and continue its conversation.</p><button class="primary-button overview-session-button" onclick={() => navigate('sessions')}>Open sessions</button></section>
    {:else if activeView === 'sessions'}
      <SessionView />
    {:else if activeView === 'changes'}
      <ChangesView />
    {:else if activeView === 'operations'}
      <OperationsView />
    {:else if activeView === 'help'}
      <HelpView />
    {:else if activeView === 'settings'}
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
                    <div class="assignment-role"><span><strong>{roleLabel(role.role)}</strong><small>{role.inherited ? `Inherits ${role.effective_model}` : role.role}</small></span>{#if role.custom}<button class="danger-icon" title="Delete custom role" onclick={() => deleteCustomRole(role)}><Trash2 aria-hidden="true" size={14} /></button>{/if}</div>
                    <label><span>Model</span><select bind:value={role.configured_model} onchange={() => saveRoleModel(role)} disabled={modelsLoading}>
                      <option value="">Inherit</option>
                      {#if role.configured_model && !modelOptions.some((model) => model.id === role.configured_model)}<option value={role.configured_model}>{role.configured_model}</option>{/if}
                      {#each modelOptions as model}<option value={model.id}>{model.id}{model.group ? ' · group' : ''}</option>{/each}
                    </select></label>
                    <label><span>Reasoning</span><select bind:value={role.reasoning_effort} onchange={() => saveRoleModel(role)}><option value="">Provider default</option>{#each reasoningOptions(role.configured_model || role.effective_model, role.reasoning_effort) as effort}<option value={effort}>{effort}</option>{/each}</select></label>
                  </div>
                {/each}
              </div>

              <WorkspaceModels models={modelOptions} />

              <div class="subsection-heading"><div><h3>Custom roles</h3><p>Create a reusable model role for subagents.</p></div></div>
              <div class="settings-card inline-create"><label><span>Role name</span><input placeholder="security-reviewer" bind:value={newCustomRoleName} onkeydown={(event) => event.key === 'Enter' && addCustomRole()} /></label><button class="primary-button" onclick={addCustomRole} disabled={!newCustomRoleName.trim()}><Plus aria-hidden="true" size={15} /> Add role</button></div>

              <div class="subsection-heading"><div><h3>Fallback groups</h3><p>Try ordered candidates with optional reasoning and timeout overrides.</p></div></div>
              <div class="settings-card inline-create"><label><span>New group name</span><input placeholder="reliable-coding" bind:value={newModelGroupName} onkeydown={(event) => event.key === 'Enter' && addModelGroup()} /></label><button class="primary-button" onclick={addModelGroup} disabled={!newModelGroupName.trim() || !modelOptions.some((model) => !model.group)}><Plus aria-hidden="true" size={15} /> Add group</button></div>
              <div class="model-group-list">
                {#each settings.models.groups as group}
                  <article class="settings-card model-group-card">
                    <div class="card-heading"><div><h3>{group.name}</h3><p>{group.candidates.length} ordered candidates</p></div><div class="provider-actions"><button class="primary-button compact-button" onclick={() => addGroupCandidate(group)} disabled={group.candidates.length >= modelOptions.filter((model) => !model.group).length}><Plus aria-hidden="true" size={14} /> Candidate</button><button class="danger-icon" title="Delete model group" onclick={() => deleteModelGroup(group)}><Trash2 aria-hidden="true" size={15} /></button></div></div>
                    <div class="group-candidates">
                      {#each group.candidates as candidate, candidateIndex}
                        <div class="group-candidate">
                          <span class="candidate-order">{candidateIndex + 1}</span>
                          <label><span>Model</span><select bind:value={candidate.model} onchange={() => saveModelGroup(group)}>{#each modelOptions.filter((model) => !model.group) as model}<option value={model.id}>{model.id}</option>{/each}</select></label>
                          <label><span>Reasoning</span><select bind:value={candidate.reasoning_effort} onchange={() => saveModelGroup(group)}><option value="">Provider default</option>{#each reasoningOptions(candidate.model, candidate.reasoning_effort) as effort}<option value={effort}>{effort}</option>{/each}</select></label>
                          <label><span>Timeout</span><input placeholder="60s or empty" bind:value={candidate.timeout} onchange={() => saveModelGroup(group)} /></label>
                          <div class="candidate-actions"><button title="Move up" onclick={() => moveGroupCandidate(group, candidateIndex, -1)} disabled={candidateIndex === 0}><ChevronUp aria-hidden="true" size={14} /></button><button title="Move down" onclick={() => moveGroupCandidate(group, candidateIndex, 1)} disabled={candidateIndex === group.candidates.length - 1}><ChevronDown aria-hidden="true" size={14} /></button><button title="Remove candidate" onclick={() => removeGroupCandidate(group, candidateIndex)} disabled={group.candidates.length === 1}><Trash2 aria-hidden="true" size={13} /></button></div>
                        </div>
                      {/each}
                    </div>
                  </article>
                {/each}
              </div>

              <div class="subsection-heading"><div><h3>Model behavior</h3><p>Select an API mode and override provider context metadata.</p></div></div>
              <div class="settings-card model-behavior-list">
                {#each modelOptions.filter((model) => !model.group) as model}
                  <div class="model-behavior-row"><div><strong>{model.id}</strong><small>{model.context_length ? `Discovered context ${model.context_length.toLocaleString()}` : 'No context metadata'}</small></div><label><span>API mode</span><select bind:value={model.api_mode} onchange={() => saveModelAPIMode(model)}><option value="chat_completions">Chat Completions</option><option value="responses">Responses</option></select></label><label><span>Context override</span><input type="number" min="0" placeholder="Provider metadata" bind:value={model.context_override} onchange={() => saveModelContextOverride(model)} /></label></div>
                {:else}<div class="api-key-empty">Refresh Gateway models to edit their behavior.</div>{/each}
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
          {:else if activeSection === 'system-one'}
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
          {:else if activeSection === 'runtime'}
            <section class="settings-section">
              <div class="section-heading"><div><p class="eyebrow">GLOBAL RUNTIME</p><h2>Execution and storage</h2></div><code>{settings.runtime.config_path}</code></div>
              {#if !settings.runtime.configured}<p class="inline-warning">Choose a Gateway provider and default model in Studio before editing runtime settings.</p>{/if}
              <div class="settings-card"><div class="card-heading"><div><h3>Agent execution</h3><p>Limit concurrent delegated agent work.</p></div></div><label class="field-row"><span><strong>Maximum parallel agents</strong><small>Applies across built-in roles.</small></span><input type="number" min="1" max="64" bind:value={settings.runtime.max_parallel} onchange={saveRuntime} disabled={!settings.runtime.configured} /></label></div>
              <div class="settings-card"><div class="card-heading"><div><h3>Context compaction</h3><p>Control when and how Q compacts long conversations.</p></div></div><div class="field-grid"><label><span>Context window</span><input type="number" min="0" step="1000" bind:value={settings.runtime.context.window} onchange={saveRuntime} disabled={!settings.runtime.configured} /></label><label><span>Trigger ratio</span><input type="number" min="0.01" max="0.99" step="0.01" bind:value={settings.runtime.context.trigger_ratio} onchange={saveRuntime} disabled={!settings.runtime.configured} /></label><label><span>Target ratio</span><input type="number" min="0.01" max="0.98" step="0.01" bind:value={settings.runtime.context.target_ratio} onchange={saveRuntime} disabled={!settings.runtime.configured} /></label><label><span>Recent ratio</span><input type="number" min="0.01" max="0.97" step="0.01" bind:value={settings.runtime.context.recent_ratio} onchange={saveRuntime} disabled={!settings.runtime.configured} /></label></div></div>
              <div class="settings-card">
                <div class="card-heading"><div><h3>Loom storage</h3><p>Bound artifact storage and garbage collection.</p></div><button class="icon-button" title="Refresh Loom stats" onclick={loadLoomStatus} disabled={loomLoading}><RefreshCw aria-hidden="true" size={15} class={loomLoading ? 'spin' : undefined} /></button></div>
                <div class="field-grid"><label><span>Maximum artifact MiB</span><input type="number" min="1" max="1024" bind:value={settings.runtime.loom.maximum_artifact_mib} onchange={saveRuntime} disabled={!settings.runtime.configured} /></label><label><span>Maximum store MiB</span><input type="number" min="1" max="10240" bind:value={settings.runtime.loom.maximum_store_mib} onchange={saveRuntime} disabled={!settings.runtime.configured} /></label><label><span>GC trigger ratio</span><input type="number" min="0.02" max="0.99" step="0.01" bind:value={settings.runtime.loom.gc_trigger_ratio} onchange={saveRuntime} disabled={!settings.runtime.configured} /></label><label><span>GC target ratio</span><input type="number" min="0.01" max="0.98" step="0.01" bind:value={settings.runtime.loom.gc_target_ratio} onchange={saveRuntime} disabled={!settings.runtime.configured} /></label><label><span>GC grace hours</span><input type="number" min="1" max="8760" bind:value={settings.runtime.loom.gc_grace_hours} onchange={saveRuntime} disabled={!settings.runtime.configured} /></label><label class="toggle-field"><span>Garbage collection</span><input type="checkbox" checked={!settings.runtime.loom.gc_disabled} onchange={(event) => { settings!.runtime.loom.gc_disabled = !event.currentTarget.checked; saveRuntime(); }} disabled={!settings.runtime.configured} /><small>{settings.runtime.loom.gc_disabled ? 'Disabled' : 'Enabled'}</small></label></div>
                <div class="loom-operations">
                  {#if loomStats}<div class="loom-stats"><span><strong>{loomStats.artifacts}</strong> artifacts</span><span><strong>{loomStats.blobs}</strong> blobs</span><span><strong>{formatBytes(loomStats.bytes)}</strong> stored</span></div>{/if}
                  {#if loomError}<p class="inline-warning">{loomError}</p>{/if}
                  {#if loomResult}<p class="loom-result">{loomResult.dry_run ? 'Preview' : 'Collected'} · {loomResult.artifacts_removed} artifacts · {loomResult.blobs_removed} blobs · {formatBytes(loomResult.bytes_reclaimed)}</p>{/if}
                  <div><button class="text-button" onclick={() => collectLoom(true)} disabled={loomLoading || !loomStats}>Preview GC</button><button class="primary-button" onclick={() => collectLoom(false)} disabled={loomLoading || !loomStats}>Run GC</button></div>
                </div>
              </div>
            </section>
          {:else if activeSection === 'services'}
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
          {:else if activeSection === 'subagents'}
            <SubagentsView />
          {:else}
            <IntegrationsView />
          {/if}
        </div>
      </div>
    {/if}
  </main>

  <footer class="status-bar"><div><span>Q Studio</span><span class="divider" aria-hidden="true"></span><span class:online={connection.kind === 'ready'} class="status-dot" aria-hidden="true"></span><span>{connection.kind === 'ready' ? 'Ready' : connection.kind === 'error' ? 'Unavailable' : 'Connecting'}</span></div><span>{activeView === 'settings' ? 'Configuration' : activeView === 'sessions' ? 'Repository session' : activeView === 'changes' ? 'Repository changes' : activeView === 'operations' ? 'Runtime operations' : activeView === 'help' ? 'Studio guide' : 'Local'}</span></footer>
</div>
