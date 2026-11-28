import type {
  APIErrorPayload,
  Character,
  CharacterInput,
  CharacterPatch,
  ConfigView,
  DecisionModelInfo,
  HealthStatus,
  LLMRolesConfig,
  MessageBubble,
  ModelInfo,
  Persona,
  PersonaInput,
  PersonaPatch,
  RoomDetail,
  RoomMemoriesResponse,
  RoomMode,
  RoomSummary,
  TurnStatus,
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
  }) {
    return this.request<RoomDetail>("/api/v1/rooms", {
      method: "POST",
      body: JSON.stringify(input),
    });
  }
  getRoom(id: string) {
    return this.request<RoomDetail>(`/api/v1/rooms/${encodeURIComponent(id)}`);
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
}
