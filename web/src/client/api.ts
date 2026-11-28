import type {
  APIErrorPayload,
  ApprovalMode,
  ArtifactReadResponse,
  Character,
  CharacterInput,
  CharacterPatch,
  ConfigView,
  DecisionModelInfo,
  HealthStatus,
  KnowledgeDoc,
  KnowledgeHit,
  LLMRolesConfig,
  MessageBubble,
  ModelInfo,
  ModelPrefs,
  Persona,
  PersonaInput,
  PersonaPatch,
  RoomDetail,
  RoomMemoriesResponse,
  RoomMode,
  RoomSummary,
  SkillSummary,
  ToolInvocation,
  TurnStatus,
  UserQuestion,
} from "./types";

export class QAPIError extends Error {
  readonly status: number;
  readonly code: string;
  readonly retryable: boolean;
  readonly details?: Record<string, unknown>;

  constructor(
    status: number,
    code: string,
    message: string,
    retryable = false,
    details?: Record<string, unknown>,
  ) {
    super(message);
    this.name = "QAPIError";
    this.status = status;
    this.code = code;
    this.retryable = retryable;
    this.details = details;
  }
}

export interface QClientOptions {
  baseUrl?: string;
  fetchFn?: typeof fetch;
}

export class QClient {
  private readonly baseUrl: string;
  private readonly fetchFn: typeof fetch;

  constructor(options: QClientOptions = {}) {
    this.baseUrl = (options.baseUrl ?? "").replace(/\/+$/, "");
    this.fetchFn = options.fetchFn ?? ((input, init) => fetch(input, init));
  }

  private async request<T>(path: string, init?: RequestInit): Promise<T> {
    const headers = new Headers(init?.headers);
    if (init?.body && !(init.body instanceof FormData) && !headers.has("Content-Type")) {
      headers.set("Content-Type", "application/json");
    }
    const resp = await this.fetchFn(`${this.baseUrl}${path}`, {
      ...init,
      headers,
    });
    if (resp.status === 204) {
      return undefined as T;
    }
    const text = await resp.text();
    let parsed: unknown = undefined;
    if (text.trim()) {
      try {
        parsed = JSON.parse(text);
      } catch {
        if (!resp.ok) {
          throw new QAPIError(resp.status, "http_error", text || resp.statusText, resp.status >= 500);
        }
        return text as T;
      }
    }
    if (!resp.ok) {
      const payload = parsed as Partial<APIErrorPayload> | undefined;
      if (payload?.error) {
        throw new QAPIError(
          resp.status,
          payload.error.code,
          payload.error.message,
          payload.error.retryable,
          payload.error.details,
        );
      }
      throw new QAPIError(resp.status, "http_error", resp.statusText, resp.status >= 500);
    }
    return parsed as T;
  }

  // System, Config & Models
  health() {
    return this.request<HealthStatus>("/api/v1/health");
  }
  getConfig() {
    return this.request<ConfigView>("/api/v1/config");
  }
  putConfig(cfg: ConfigView) {
    return this.request<{ applied: boolean; restart_required_fields: string[] }>("/api/v1/config", {
      method: "PUT",
      body: JSON.stringify(cfg),
    });
  }
  listModels() {
    return this.request<{ models: ModelInfo[]; default_roles: LLMRolesConfig }>("/api/v1/models");
  }
  refreshModels() {
    return this.request<{ refreshed_at: number; count: number }>("/api/v1/models/refresh", {
      method: "POST",
    });
  }
  listDecisionModels() {
    return this.request<{ models: DecisionModelInfo[]; default_model_id: string }>(
      "/api/v1/decisions/models",
    );
  }
  reindexStorage() {
    return this.request<{ job_id: string }>("/api/v1/storage/reindex", { method: "POST" });
  }

  // Personas
  listPersonas() {
    return this.request<{ items: Persona[] }>("/api/v1/personas");
  }
  createPersona(input: PersonaInput) {
    return this.request<Persona>("/api/v1/personas", {
      method: "POST",
      body: JSON.stringify(input),
    });
  }
  patchPersona(id: string, patch: PersonaPatch) {
    return this.request<Persona>(`/api/v1/personas/${encodeURIComponent(id)}`, {
      method: "PATCH",
      body: JSON.stringify(patch),
    });
  }
  deletePersona(id: string) {
    return this.request<void>(`/api/v1/personas/${encodeURIComponent(id)}`, {
      method: "DELETE",
    });
  }

  // Characters
  listCharacters() {
    return this.request<{ items: Character[] }>("/api/v1/characters");
  }
  createCharacter(input: CharacterInput) {
    return this.request<Character>("/api/v1/characters", {
      method: "POST",
      body: JSON.stringify(input),
    });
  }
  getCharacter(id: string) {
    return this.request<Character>(`/api/v1/characters/${encodeURIComponent(id)}`);
  }
  patchCharacter(id: string, patch: CharacterPatch) {
    return this.request<Character>(`/api/v1/characters/${encodeURIComponent(id)}`, {
      method: "PATCH",
      body: JSON.stringify(patch),
    });
  }
  deleteCharacter(id: string) {
    return this.request<void>(`/api/v1/characters/${encodeURIComponent(id)}`, {
      method: "DELETE",
    });
  }

  // Rooms, Messages & Turns
  listRooms() {
    return this.request<{ items: RoomSummary[] }>("/api/v1/rooms");
  }
  createRoom(input: {
    title: string;
    mode?: RoomMode;
    persona_id: string;
    character_ids: string[];
    approval_mode_override?: string;
    max_turns_per_input?: number;
  }) {
    return this.request<RoomDetail>("/api/v1/rooms", {
      method: "POST",
      body: JSON.stringify(input),
    });
  }
  getRoom(id: string) {
    return this.request<RoomDetail>(`/api/v1/rooms/${encodeURIComponent(id)}`);
  }
  patchRoom(
    id: string,
    patch: {
      expected_version: number;
      title?: string;
      character_ids?: string[];
      approval_mode_override?: ApprovalMode;
      model_overrides?: ModelPrefs;
      max_turns_per_input?: number;
    },
  ) {
    return this.request<RoomDetail>(`/api/v1/rooms/${encodeURIComponent(id)}`, {
      method: "PATCH",
      body: JSON.stringify(patch),
    });
  }
  deleteRoom(id: string) {
    return this.request<void>(`/api/v1/rooms/${encodeURIComponent(id)}`, {
      method: "DELETE",
    });
  }
  listMessages(roomId: string, params?: { before_seq?: number; limit?: number }) {
    const qs = new URLSearchParams();
    if (params?.before_seq) qs.set("before_seq", String(params.before_seq));
    if (params?.limit) qs.set("limit", String(params.limit));
    const suffix = qs.toString() ? `?${qs.toString()}` : "";
    return this.request<{ items: MessageBubble[]; next_before_seq?: number }>(
      `/api/v1/rooms/${encodeURIComponent(roomId)}/messages${suffix}`,
    );
  }
  submitTurn(roomId: string, input: { request_id: string; text: string; location_tag?: string }) {
    return this.request<{ turn_id: string; status: TurnStatus; user_message_id: string }>(
      `/api/v1/rooms/${encodeURIComponent(roomId)}/turns`,
      {
        method: "POST",
        body: JSON.stringify(input),
      },
    );
  }
  retryTurn(roomId: string, turnId: string) {
    return this.request<{ turn_id: string; status: TurnStatus }>(
      `/api/v1/rooms/${encodeURIComponent(roomId)}/turns/${encodeURIComponent(turnId)}/retry`,
      { method: "POST" },
    );
  }
  interceptTurn(roomId: string, text: string) {
    return this.request<{ accepted: boolean }>(
      `/api/v1/rooms/${encodeURIComponent(roomId)}/intercept`,
      {
        method: "POST",
        body: JSON.stringify({ text }),
      },
    );
  }
  getRoomMemories(roomId: string) {
    return this.request<RoomMemoriesResponse>(
      `/api/v1/rooms/${encodeURIComponent(roomId)}/memories`,
    );
  }
  async uploadMedia(file: File) {
    const form = new FormData();
    form.append("file", file);
    return this.request<{ artifact_id: string; url: string }>("/api/v1/media", {
      method: "POST",
      body: form,
    });
  }

  // Approvals, Questions, Artifacts, Knowledge & Skills
  resolveApproval(roomId: string, invocationId: string, decision: "approve" | "reject", reason = "") {
    return this.request<ToolInvocation>(
      `/api/v1/rooms/${encodeURIComponent(roomId)}/approvals/${encodeURIComponent(invocationId)}`,
      {
        method: "POST",
        body: JSON.stringify({ decision, reason }),
      },
    );
  }
  resolveQuestion(roomId: string, questionId: string, answerText: string) {
    return this.request<UserQuestion>(
      `/api/v1/rooms/${encodeURIComponent(roomId)}/questions/${encodeURIComponent(questionId)}`,
      {
        method: "POST",
        body: JSON.stringify({ answer_text: answerText }),
      },
    );
  }
  getArtifact(id: string, offset = 0, limit = 65536) {
    return this.request<ArtifactReadResponse>(
      `/api/v1/artifacts/${encodeURIComponent(id)}?offset=${offset}&limit=${limit}`,
    );
  }
  listKnowledge(scope = "") {
    const qs = scope ? `?scope=${encodeURIComponent(scope)}` : "";
    return this.request<{ items: KnowledgeDoc[] }>(`/api/v1/knowledge${qs}`);
  }
  createKnowledge(input: { scope: string; title: string; content: string; media_type?: string }) {
    return this.request<KnowledgeDoc>("/api/v1/knowledge", {
      method: "POST",
      body: JSON.stringify(input),
    });
  }
  patchKnowledge(id: string, input: { expected_revision: number; title?: string; scope?: string; content?: string }) {
    return this.request<KnowledgeDoc>(`/api/v1/knowledge/${encodeURIComponent(id)}`, {
      method: "PATCH",
      body: JSON.stringify(input),
    });
  }
  deleteKnowledge(id: string) {
    return this.request<void>(`/api/v1/knowledge/${encodeURIComponent(id)}`, { method: "DELETE" });
  }
  searchKnowledge(input: { query: string; mode?: "hybrid" | "lexical" | "semantic"; scopes?: string[]; limit?: number }) {
    return this.request<{ hits: KnowledgeHit[] }>("/api/v1/knowledge/search", {
      method: "POST",
      body: JSON.stringify(input),
    });
  }
  listSkills() {
    return this.request<{ items: SkillSummary[] }>("/api/v1/skills");
  }
}
