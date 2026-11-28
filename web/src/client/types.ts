export type ApprovalMode = "bypass" | "auto-detection" | "request";
export type RoomMode = "direct" | "group";
export type TurnStatus =
  | "running"
  | "awaiting_approval"
  | "awaiting_user"
  | "completed"
  | "cancelled"
  | "failed";

export interface APIErrorDetail {
  code: string;
  message: string;
  retryable: boolean;
  details?: Record<string, unknown>;
}

export interface APIErrorPayload {
  error: APIErrorDetail;
}

export interface HealthStatus {
  status: "ready" | "degraded";
  rdb: "ready" | "degraded";
  search: "ready" | "degraded";
  blob: "ready" | "degraded";
  reason?: string;
}

export interface ModelCapabilities {
  tool_calling?: boolean;
  reasoning?: {
    supported: boolean;
    control?: string;
    supported_efforts?: string[];
    default_effort?: string;
    default_enabled?: boolean;
    mandatory?: boolean;
    supports_max_tokens?: boolean;
  };
}

export interface ModelInfo {
  id: string;
  object?: string;
  owned_by?: string;
  context_length?: number;
  max_output_tokens?: number;
  capabilities?: ModelCapabilities;
}

export interface DecisionModelInfo {
  id: string;
  label: string;
  provider_id: string;
  provider_label: string;
  protocol: string;
  model: string;
  context_window?: number;
}

export interface LLMRolesConfig {
  chat_model?: string;
  memory_model?: string;
  recall_planner_model?: string;
  subagent_model?: string;
}

export interface ProviderConfig {
  id: string;
  type: string;
  kind?: string;
  prefix: string;
  enabled: boolean;
  base_url?: string;
  api_key_env?: string;
  models?: string[];
  [key: string]: unknown;
}

export interface ConfigView {
  version: number;
  server: {
    listen: string;
    open_browser: boolean;
    cors_origins?: string[];
  };
  storage: {
    rdb: { driver: string; sqlite_path?: string; dsn_env?: string };
    search: { driver: string; opensearch?: Record<string, unknown> };
    blob: {
      driver: string;
      max_upload_bytes?: number;
      loom_max_artifact_bytes?: number;
      loom_max_store_bytes?: number;
    };
  };
  llm: {
    model_cache_refresh_interval?: string;
    model_cache_refresh_timeout?: string;
    providers: ProviderConfig[];
    roles: LLMRolesConfig;
  };
  decisions: {
    providers: Array<Record<string, unknown>>;
    models: Array<Record<string, unknown>>;
    default_model_id?: string;
  };
  embeddings: {
    enabled: boolean;
    base_url?: string;
    api_key_env?: string;
    model?: string;
    revision?: string;
    dimensions?: number;
    query_prefix?: string;
    document_prefix?: string;
    timeout_ms?: number;
  };
  tools: {
    approval_mode: ApprovalMode;
    tool_overrides?: Record<string, ApprovalMode>;
    allowed_roots?: string[];
    command_timeout_ms?: number;
    max_delegation_depth?: number;
    browser_control: {
      enabled: boolean;
      preferred: "cdp-q-driver" | "ego-browser";
      drivers?: Record<string, { command?: string; args?: string[] }>;
    };
    mcp_servers?: Array<Record<string, unknown>>;
  };
}

export interface Persona {
  id: string;
  name: string;
  description?: string;
  avatar_artifact_id?: string;
  is_default: boolean;
  version: number;
  created_at: number;
  updated_at: number;
}

export interface PersonaInput {
  name: string;
  description?: string;
  avatar_artifact_id?: string;
  is_default?: boolean;
}

export interface PersonaPatch {
  expected_version: number;
  name?: string;
  description?: string;
  avatar_artifact_id?: string;
  is_default?: boolean;
}

export interface ActorAffect {
  valence: number;
  activation: number;
  stressed: boolean;
}

export interface Relationship {
  valence: number;
  activation: number;
  trust: number;
  stressed: boolean;
}

export interface ModelPrefs {
  chat_model?: string;
  memory_model?: string;
  recall_planner_model?: string;
  decision_model?: string;
  reasoning_effort?: string;
  memory_reasoning_effort?: string;
  context_window_override?: number;
}

export interface Character {
  id: string;
  name: string;
  avatar_artifact_id?: string;
  personality?: string;
  background?: string;
  appearance?: string;
  initial_values?: string[];
  initial_state?: {
    self?: ActorAffect;
    relationships?: Record<string, Relationship>;
  };
  model_prefs?: ModelPrefs;
  approval_policy_override?: ApprovalMode;
  version: number;
  created_at: number;
  updated_at: number;
}

export interface CharacterInput {
  name: string;
  avatar_artifact_id?: string;
  personality?: string;
  background?: string;
  appearance?: string;
  initial_values?: string[];
  initial_state?: Character["initial_state"];
  model_prefs?: ModelPrefs;
  approval_policy_override?: ApprovalMode;
}

export interface CharacterPatch extends Partial<CharacterInput> {
  expected_version: number;
}

export interface CharacterValue {
  id: string;
  text: string;
}

export interface ActorState {
  self: ActorAffect;
  state_updated_at?: number;
  relationships?: Record<string, Relationship>;
  relationships_updated_at?: Record<string, number>;
  compressed_memo?: string;
  compacted_through_seq?: number;
  values?: CharacterValue[];
  values_revision: number;
}

export interface Room {
  id: string;
  title: string;
  mode: RoomMode;
  persona_id: string;
  persona_snapshot: Persona;
  approval_mode_override?: ApprovalMode;
  model_overrides?: ModelPrefs;
  max_turns_per_input: number;
  context_version: number;
  version: number;
  created_at: number;
  updated_at: number;
}

export interface RoomMember {
  room_id: string;
  character_id: string;
  character_snapshot: Character;
  base_speed: number;
  actor_state: ActorState;
  active: boolean;
  joined_at: number;
}

export interface Turn {
  id: string;
  room_id: string;
  request_id: string;
  input_hash: string;
  status: TurnStatus;
  error_code?: string;
  error_message?: string;
  created_at: number;
  updated_at: number;
  completed_at?: number;
}

export interface RoomSummary {
  room: Room;
  members: RoomMember[];
}

export interface RoomDetail {
  room: Room;
  members: RoomMember[];
  turns: Turn[];
  pending_approvals: unknown[];
  pending_questions: unknown[];
  active_turn: boolean;
}

export interface MessageBubble {
  id: string;
  room_id: string;
  turn_id: string;
  seq: number;
  role: "user" | "character" | "system";
  author_id: string;
  author_name: string;
  content: string;
  observed_by: string[];
  location_tag?: string;
  metadata_json?: string;
  created_at: number;
}

export interface MemoryRecord {
  id: string;
  room_id: string;
  owner_id: string;
  source_turn_id?: string;
  kind: "episodic" | "fact" | "value_history";
  content: string;
  observed_by: string[];
  occurred_at: number;
  pinned: boolean;
  status: string;
  revision: number;
  created_at: number;
}

export interface RoomMemoriesResponse {
  memories: MemoryRecord[];
  values: Record<string, CharacterValue[]>;
}

export interface SSEEvent {
  id?: string;
  event: string;
  data: unknown;
}
