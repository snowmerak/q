export type ContextSettings = { window: number; trigger_ratio: number; target_ratio: number; recent_ratio: number };
export type LoomSettings = {
  maximum_artifact_mib: number;
  maximum_store_mib: number;
  gc_disabled: boolean;
  gc_trigger_ratio: number;
  gc_target_ratio: number;
  gc_grace_hours: number;
};
export type ListenerSettings = { config_path: string; host: string; port: number; active_api_keys: number };
export type RoleModelAssignment = {
  role: string;
  configured_model: string;
  effective_model: string;
  reasoning_effort: string;
  inherited: boolean;
  custom: boolean;
};
export type ModelCandidate = { model: string; reasoning_effort: string; timeout: string };
export type ModelGroup = { name: string; candidates: ModelCandidate[] };
export type GatewayProvider = {
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
  _original_type?: string;
  _original_enabled?: boolean;
};
export type ModelOption = {
  id: string;
  context_length?: number;
  reasoning_control?: string;
  reasoning_efforts?: string[];
  default_effort?: string;
  group?: boolean;
  api_mode?: string;
  context_override?: number;
};
export type SystemOneProvider = { id: string; uri: string; api_key_env: string; _original_id?: string };
export type SystemOneModelOption = { id: string; description?: string; release_date?: string };
export type ServiceAPIKey = { id: string; alias: string; created_at?: string; revoked_at?: string; legacy?: boolean };
export type SettingsSnapshot = {
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
    archive_model: string;
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
export type SettingsSection = 'appearance' | 'models' | 'providers' | 'system-one' | 'runtime' | 'services' | 'subagents' | 'integrations' | 'import-export';
export type SaveState = { kind: 'idle' | 'saving' | 'saved' | 'error'; message?: string };
export type LoomStats = { artifacts: number; blobs: number; bytes: number };
export type LoomGCResult = { artifacts_removed: number; blobs_removed: number; bytes_reclaimed: number; dry_run: boolean };
