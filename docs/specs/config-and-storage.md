# 구현 스펙: 설정(`config.json`) · RDB 스키마 · `pkg/storage` 계약

- **작성일:** 2026-11-28
- **대상 범위:** `pkg/app/config`, `pkg/domain`, `pkg/storage` (M1~M4)
- **관련 문서:** [PRD](../prd.md), [Blueprint](../blueprint.md), [ADR-0001](../adr/0001-storage-authority-and-hybrid-search.md)

---

## 1. 전역 데이터 디렉터리 (`--data-dir` / `Q_DATA_DIR`)

코어 엔진은 단일 전역 데이터 디렉터리 아래 모든 로컬 상태를 보관한다. 경로 우선순위는 `--data-dir` CLI 플래그 → `Q_DATA_DIR` 환경변수 → OS 기본 사용자 경로(`~/.q`) 순이다.

```text
<data-dir>/
├── instance.lock                 # 단일 인스턴스 기동 가드 (OS 파일 락 + owner PID/시각 메타)
├── config.json                   # 전역 설정 (fsnotify 감시 대상)
├── q.sqlite                      # SQLite 선택 시 기본 DB 파일 (WAL/SHM 포함)
├── index/                        # Bleve + HNSW 선택 시 로컬 인덱스
│   ├── current.json              # 활성 세대 ID, embedding_model, dimensions, prefix 해시
│   └── generations/
│       └── <generation-id>/
│           ├── bleve/            # Bleve BM25 디렉터리
│           ├── vectors.hnsw      # HNSW 벡터 그래프
│           ├── vectors.ids.json  # HNSW 내부 노드 ID <-> record_id 매핑
│           └── state.json        # 마지막 적용 outbox seq / 문서 수 / 리비전
├── blobs/
│   ├── loom/                     # 불변 Loom 아티팩트 본문 (<sha256-32hex>)
│   └── media/                    # 업로드 이미지/첨부 파일
└── logs/
    └── q.jsonl                   # 순환 JSONL 진단 로그
```

---

## 2. `config.json` 스키마 및 리로드 계약

- **포맷:** 단일 JSON 파일 (`strictjson` 검증; 알 수 없는 키 거절).
- **시크릿 원칙:** API 키·DB 비밀번호·S3 시크릿은 JSON에 평문으로 저장하지 않는 것을 기본으로 하고 `*_env` 환경변수 이름만 저장한다(단, 로컬 전용 편의를 위한 직접 필드가 필요할 경우에도 공개 HTTP API `/api/v1/config` 응답에서는 항상 마스킹/생략한다).
- **Hot-Reload (`fsnotify` + 1초 폴백):**
  - 파일 변경 시 디바운스 후 파싱·검증을 수행한다. 유효하지 않은 JSON이면 로그만 남기고 직전 정상 설정을 유지한다.
  - 런타임 즉시 반영 가능: `llm.providers`, `llm.roles`, `decisions`, `embeddings`, `tools.approval_mode`, `tools.tool_overrides`, `tools.browser_control`, `tools.mcp_servers`.
  - 재시작 필요(로그에 경고 후 보류): `server.listen`, `storage.rdb`, `storage.blob` 변경. (`storage.search` 변경은 백그라운드 어댑터 교체 후 필요 시 재색인을 트리거한다.)

### 2.1 `config.json` 전체 스키마 예시

```json
{
  "version": 2,
  "server": {
    "listen": "127.0.0.1:17070",
    "open_browser": false,
    "cors_origins": []
  },
  "storage": {
    "rdb": {
      "driver": "sqlite",
      "sqlite_path": "q.sqlite",
      "dsn_env": "Q_RDB_DSN"
    },
    "search": {
      "driver": "bleve",
      "opensearch": {
        "endpoint": "http://127.0.0.1:19200",
        "index_prefix": "q",
        "username": "",
        "password_env": "Q_OPENSEARCH_PASSWORD",
        "bearer_token_env": "Q_OPENSEARCH_TOKEN",
        "timeout_ms": 15000
      }
    },
    "blob": {
      "driver": "local",
      "max_upload_bytes": 20971520,
      "loom_max_artifact_bytes": 67108864,
      "loom_max_store_bytes": 268435456,
      "s3": {
        "endpoint": "",
        "bucket": "",
        "region": "",
        "access_key_env": "Q_S3_ACCESS_KEY",
        "secret_key_env": "Q_S3_SECRET_KEY"
      }
    }
  },
  "llm": {
    "model_cache_refresh_interval": "15m",
    "model_cache_refresh_timeout": "10s",
    "providers": [
      {
        "id": "local",
        "type": "openai-compatible",
        "prefix": "local",
        "enabled": true,
        "base_url": "http://127.0.0.1:11888/v1",
        "api_key_env": ""
      }
    ],
    "roles": {
      "chat_model": "local/default",
      "memory_model": "local/default",
      "recall_planner_model": "local/default",
      "subagent_model": "local/default"
    }
  },
  "decisions": {
    "providers": [
      {
        "id": "default",
        "protocol": "openai",
        "base_url": "http://127.0.0.1:18181/v1",
        "api_key_env": "",
        "timeout_ms": 120000
      }
    ],
    "models": [
      {
        "id": "default-decision",
        "provider_id": "default",
        "model": "default-decision",
        "context_window": 32768
      }
    ],
    "default_model_id": "default-decision"
  },
  "embeddings": {
    "enabled": true,
    "base_url": "http://127.0.0.1:1234/v1",
    "api_key_env": "",
    "model": "text-embedding-embeddinggemma-2",
    "revision": "embeddinggemma-2-v1",
    "dimensions": 768,
    "query_prefix": "task: search result | query: ",
    "document_prefix": "title: none | text: ",
    "timeout_ms": 15000
  },
  "tools": {
    "approval_mode": "auto-detection",
    "tool_overrides": {
      "remove_path": "request",
      "run_command": "auto-detection",
      "read_file": "bypass"
    },
    "allowed_roots": [],
    "command_timeout_ms": 120000,
    "max_delegation_depth": 2,
    "browser_control": {
      "enabled": true,
      "preferred": "cdp-q-driver",
      "drivers": {
        "cdp-q-driver": {
          "command": "",
          "args": ["cdp"]
        },
        "ego-browser": {
          "command": "ego-browser",
          "args": []
        }
      }
    },
    "mcp_servers": []
  }
}
```

> [!NOTE]
> `storage.rdb.driver`는 `"sqlite" | "postgres" | "mysql"` 중 하나이며, `postgres`/`mysql` 선택 시 `dsn_env`에 지정된 환경변수에서 DSN을 읽는다. `tools.browser_control.drivers.cdp-q-driver.command`가 빈 문자열이면 현재 실행 파일(`q`)의 `cdp` 서브커맨드(`pkg/cdp`)를 사용한다.

---

## 3. RDB 핵심 테이블 논리 스키마 (SQLite / PostgreSQL / MySQL 공통)

### 3.1 공통 타입·규칙
- **PK/FK ID:** UUID/ULID 문자열(`TEXT` / `VARCHAR(64)`).
- **타임스탬프:** UTC Unix milliseconds (`BIGINT`).
- **JSON 컬럼:** 도메인 구조체/메타데이터는 Go 측에서 정규화 직렬화해 저장(SQLite `TEXT`, PostgreSQL `JSONB`, MySQL `JSON`).
- **낙관적 동시성:** 주요 가변 엔티티(`personas`, `characters`, `rooms`)는 `version BIGINT NOT NULL DEFAULT 1`을 가지며, 수정 시 `WHERE id = ? AND version = ?`로 검사한다.
- **마이그레이션:** `schema_migrations (version INT PRIMARY KEY, applied_at BIGINT NOT NULL)` 테이블로 관리.

### 3.2 테이블 목록 (14개)

1. **`personas`**
   - `id` (PK), `name`, `description`, `avatar_artifact_id`, `is_default` (`BOOLEAN`), `version`, `created_at`, `updated_at`, `deleted_at`
2. **`characters`**
   - `id` (PK), `name`, `avatar_artifact_id`, `personality`, `background`, `appearance`, `initial_values_json` (string[]), `initial_state_json`, `model_prefs_json` (primary/memory/recall/reasoning), `approval_policy_override`, `version`, `created_at`, `updated_at`, `deleted_at`
3. **`rooms`**
   - `id` (PK), `title`, `mode` (`"direct"` | `"group"`), `persona_id` (FK→`personas`), `persona_snapshot_json`, `approval_mode_override`, `model_overrides_json`, `max_turns_per_input` (`INT`), `context_version` (`BIGINT`), `version`, `created_at`, `updated_at`, `deleted_at`
4. **`room_members`**
   - `room_id` (PK part, FK→`rooms`), `character_id` (PK part, FK→`characters`), `character_snapshot_json`, `base_speed` (`INT`), `actor_state_json` (현재 `valence`/`activation`/`stressed`, `relationships`, 압축 메모, `actor_values_json`, `values_revision`), `active` (`BOOLEAN`), `joined_at`
5. **`turns`**
   - `id` (PK), `room_id` (FK→`rooms`), `request_id` (`VARCHAR(128)`), `input_hash` (`VARCHAR(64)`), `status` (`"running"` | `"awaiting_approval"` | `"awaiting_user"` | `"completed"` | `"cancelled"` | `"failed"`), `checkpoint_json`, `error_code`, `error_message`, `created_at`, `updated_at`, `completed_at`
   - **인덱스:** `UNIQUE (room_id, request_id)`
6. **`messages`**
   - `id` (PK), `room_id` (FK→`rooms`), `turn_id` (FK→`turns`), `seq` (`BIGINT`), `role` (`"user"` | `"character"` | `"system"`), `author_id` (`persona_id` or `character_id`), `author_name`, `content`, `subtext` (비공개 행간, UI/타 캐릭터 비노출), `observed_by_json` (`string[]`), `location_tag`, `metadata_json`, `created_at`
   - **인덱스:** `UNIQUE (room_id, seq)`
7. **`tool_invocations`**
   - `id` (PK), `turn_id` (FK→`turns`), `room_id`, `actor_id`, `call_id`, `tool_name`, `tool_source` (`"builtin"` | `"mcp"` | `"browser"` | `"subagent"`), `args_json`, `gate_mode` (`"bypass"` | `"auto-detection"` | `"request"`), `decision_reason`, `status` (`"pending_approval"` | `"approved"` | `"rejected"` | `"running"` | `"completed"` | `"failed"` | `"cancelled"`), `result_summary`, `artifact_id` (FK→`artifacts`, nullable), `error_message`, `created_at`, `updated_at`
8. **`user_questions`**
   - `id` (PK), `turn_id` (FK→`turns`), `room_id`, `actor_id`, `prompt`, `choices_json`, `allow_freeform` (`BOOLEAN`), `status` (`"pending"` | `"answered"` | `"cancelled"`), `answer_text`, `created_at`, `answered_at`
9. **`memories`**
   - `id` (PK), `room_id` (FK→`rooms`), `owner_id` (`character_id` or `"room"`), `source_turn_id`, `kind` (`"episodic"` | `"fact"` | `"value_history"`), `content`, `observed_by_json`, `occurred_at` (`BIGINT`), `pinned` (`BOOLEAN`), `status` (`"active"` | `"superseded"` | `"deleted"`), `revision` (`BIGINT`), `embedding_model`, `embedding_dim` (`INT`), `embedding_blob` (`BLOB`/`BYTEA`, 재색인용 원문 벡터 캐시), `created_at`
10. **`value_jobs`**
    - `id` (PK), `room_id`, `character_id`, `source_turn_id`, `base_values_revision` (`BIGINT`), `candidates_json`, `status` (`"queued"` | `"running"` | `"completed"` | `"failed"`), `attempts` (`INT`), `last_error`, `next_run_at`, `created_at`, `updated_at`
11. **`knowledge_docs`**
    - `id` (PK), `scope` (`"global"` | `"room:<id>"` | `"character:<id>"`), `title`, `source_uri`, `media_type`, `content`, `content_sha256`, `revision` (`BIGINT`), `status` (`"active"` | `"deleted"`), `created_at`, `updated_at`
12. **`knowledge_chunks`**
    - `id` (PK), `doc_id` (FK→`knowledge_docs`), `doc_revision` (`BIGINT`), `chunk_ordinal` (`INT`), `char_offset` (`INT`), `text`, `embedding_model`, `embedding_dim` (`INT`), `embedding_blob` (`BLOB`/`BYTEA`)
13. **`index_outbox`**
    - `seq` (`BIGINT` 자동 증가 PK), `entity_type` (`"message"` | `"memory"` | `"knowledge_chunk"`), `entity_id`, `op` (`"upsert"` | `"delete"`), `revision` (`BIGINT`), `payload_json`, `status` (`"pending"` | `"embedding_retry"` | `"done"` | `"dead"`), `attempts` (`INT`), `next_attempt_at`, `last_error`, `created_at`
14. **`artifacts`**
    - `id` (PK, 32-hex), `kind` (`"loom"` | `"media"`), `media_type`, `byte_size` (`BIGINT`), `sha256`, `blob_key`, `source_meta_json`, `pinned` (`BOOLEAN`), `created_at`, `last_accessed_at`

---

## 4. `pkg/storage` Go 포트 인터페이스 계약

### 4.1 안정 오류 센티넬 (`pkg/storage`)
어댑터 구현(`sqlite`, `postgres`, `mysql`, `bleve`, `opensearch`, `localfs`, `s3`)은 드라이버 내부 오류를 그대로 노출하지 않고 아래 센티넬 에러를 wrap해 반환해야 한다.

- `ErrNotFound` — 대상 리소스 없음 (`HTTP 404`)
- `ErrAlreadyExists` — 중복 키 또는 이미 존재하는 리소스 (`HTTP 409`)
- `ErrConflict` — `version` 불일치, `request_id` 입력 해시 불일치 (`HTTP 409`)
- `ErrInvalidInput` — 잘못된 스코프, 차원 불일치, 빈 ID (`HTTP 400`)
- `ErrUnavailable` — Search/임베딩/외부 저장소 일시 불능 (`HTTP 503`)

### 4.2 핵심 Go 인터페이스 시그니처 (기준안)

```go
package storage

type RDBStore interface {
	Tx(ctx context.Context, fn func(tx RDBTx) error) error
	Personas() PersonaRepo
	Characters() CharacterRepo
	Rooms() RoomRepo
	Turns() TurnRepo
	Messages() MessageRepo
	Tools() ToolInvocationRepo
	Memories() MemoryRepo
	Knowledge() KnowledgeRepo
	Outbox() OutboxRepo
	Artifacts() ArtifactRepo
	Ping(ctx context.Context) error
	Close() error
}

type SearchDocument struct {
	ID         string
	EntityType string            // "message" | "memory" | "knowledge_chunk"
	Scope      string            // e.g. "room:<id>", "global"
	OwnerID    string
	ObservedBy []string
	Text       string
	OccurredAt int64             // UTC Unix ms (시간 감쇄용)
	Revision   int64
	Vector     []float32         // nil이면 BM25만 색인
}

type SearchQuery struct {
	Text          string
	Vector        []float32        // nil이면 lexical-only
	Mode          string           // "hybrid" | "lexical" | "semantic"
	EntityTypes   []string
	Scopes        []string
	ObserverID    string           // 비어있지 않으면 observedBy에 해당 ID 포함 문서만 반환
	Limit         int
	KeywordWeight float64          // 기본 0.5
	HalfLifeTurns int              // 시간 감쇄 기준
	MinDecay      float64          // 기본 0.2
}

type SearchHit struct {
	ID         string
	EntityType string
	Score      float64
	Revision   int64
}

type SearchIndex interface {
	Upsert(ctx context.Context, doc SearchDocument) error
	UpsertBatch(ctx context.Context, docs []SearchDocument) error
	Delete(ctx context.Context, entityType, id string) error
	Search(ctx context.Context, q SearchQuery) ([]SearchHit, error)
	ConfigureVector(ctx context.Context, model string, dim int) error
	ResetAndRebuild(ctx context.Context, docs <-chan SearchDocument) error
	Counts(ctx context.Context) (docs uint64, vectors int, err error)
	Close() error
}

type BlobStore interface {
	Put(ctx context.Context, key string, r io.Reader, mediaType string) (size int64, sha256Hex string, err error)
	Open(ctx context.Context, key string) (io.ReadCloser, error)
	Delete(ctx context.Context, key string) error
	Close() error
}
```

### 4.3 `SearchIndex` 필수 불변식
1. `SearchQuery.ObserverID != ""`가 주어지면 `Bleve + HNSW`와 `OpenSearch` 모두 **후보 검색 단계(pre-filter) 또는 정확 필터링**에서 `ObservedBy`에 `ObserverID`가 포함된 문서만 반환해야 한다.
2. `Mode == "hybrid"`이고 `Vector == nil`이거나 벡터 인덱스가 아직 준비되지 않았으면 어휘(BM25) 결과로 폴백한다. 반면 `Mode == "semantic"`일 때 벡터가 없으면 `ErrUnavailable`을 반환한다.
3. `ResetAndRebuild`는 `Bleve + HNSW`의 경우 새 세대 디렉터리를 완성한 뒤 `current.json`을 원자적으로 교체하고, `OpenSearch`의 경우 새 backing index에 적재한 뒤 alias를 전환한다.
