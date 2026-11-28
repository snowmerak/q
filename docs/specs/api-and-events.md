# 구현 스펙: HTTP API · SSE 이벤트 · 웹 UI 계약

- **작성일:** 2026-11-28
- **대상 범위:** `pkg/app`, `web/` (M1~M5)
- **관련 문서:** [PRD](../prd.md), [Blueprint](../blueprint.md), [ADR-0002](../adr/0002-chat-bubble-room-and-decisions-protocol.md), [ADR-0003](../adr/0003-tools-approval-gate-and-browser-control.md)

---

## 1. HTTP API 공통 규칙

- **Base Prefix:** `/api/v1`
  - `/api/`로 시작하는 경로가 매칭되지 않을 때 React SPA `index.html`로 fallback하지 않고 JSON `404`를 반환한다.
- **요청 상관관계:** `X-Request-ID` 헤더가 유효한 UUID면 유지하고, 없거나 잘못된 값이면 서버가 새 UUID를 생성해 응답 헤더와 로그에 기록한다.
- **표준 에러 응답 형식:**
  ```json
  {
    "error": {
      "code": "conflict",
      "message": "request_id already exists with different input",
      "retryable": false,
      "details": {}
    }
  }
  ```
- **HTTP 상태 매핑:**
  - `400 Bad Request` (`invalid_input`): JSON 파싱 실패, 스키마·길이 검증 오류
  - `404 Not Found` (`not_found`): 존재하지 않는 리소스
  - `409 Conflict` (`conflict` | `session_busy` | `version_conflict`): 멱등 키 충돌, 룸 실행 중 변경 불가, 낙관적 잠금 충돌
  - `422 Unprocessable Entity` (`model_unavailable` | `values_too_large`): 설정에서 모델 제거됨, 컨텍스트 예산 초과
  - `503 Service Unavailable` (`unavailable`): 저장소·모델 백엔드 연결 불능
- **보안 경계:** 기본 바인딩은 `127.0.0.1`. `/api/v1/config` 조회 시 `*_env` 자체는 유지하되 실제 환경변수 시크릿 값은 절대 노출하지 않는다.

---

## 2. REST 엔드포인트 카탈로그

### 2.1 시스템 · 설정 · 모델
| 메서드 & 경로 | 용도 | 주요 요청 / 응답 |
| --- | --- | --- |
| `GET /api/v1/health` | 프로세스 및 저장소(RDB/Search/Blob) 준비 상태 | `{ status: "ready" \| "degraded", rdb, search, blob }` |
| `GET /api/v1/config` | 현재 유효 설정 조회(시크릿 값 제외) | `ConfigView` |
| `PUT /api/v1/config` | 설정 갱신 및 `config.json` 원자적 저장 | `Config` → `{ applied, restart_required_fields }` |
| `GET /api/v1/models` | `pkg/llm` 모델 목록 + `capabilities` 조회 | `{ models: [...], default_roles }` |
| `POST /api/v1/models/refresh` | 프로바이더 모델 캐시 즉시 갱신 | `{ refreshed_at, count }` |
| `GET /api/v1/decisions/models` | 등록된 Decisions 모델 목록 조회 | `{ models: [...], default_model_id }` |

### 2.2 페르소나 · 캐릭터
| 메서드 & 경로 | 용도 | 주요 요청 / 응답 |
| --- | --- | --- |
| `GET /api/v1/personas` | 페르소나 목록 | `{ items: Persona[] }` |
| `POST /api/v1/personas` | 페르소나 생성 | `PersonaInput` → `201 Persona` |
| `PATCH /api/v1/personas/{id}` | 페르소나 수정 (`expected_version` 필수) | `PersonaPatch` → `Persona` |
| `DELETE /api/v1/personas/{id}` | 페르소나 삭제 (소프트 삭제, 멱등) | `204` |
| `GET /api/v1/characters` | 캐릭터 목록 | `{ items: Character[] }` |
| `POST /api/v1/characters` | 캐릭터 생성 | `CharacterInput` → `201 Character` |
| `GET /api/v1/characters/{id}` | 캐릭터 단건 조회 | `Character` |
| `PATCH /api/v1/characters/{id}` | 캐릭터 수정 (`expected_version` 필수) | `CharacterPatch` → `Character` |
| `DELETE /api/v1/characters/{id}` | 캐릭터 삭제 (소프트 삭제, 멱등) | `204` |

### 2.3 룸 · 메시지 · 턴 제어
| 메서드 & 경로 | 용도 | 주요 요청 / 응답 |
| --- | --- | --- |
| `GET /api/v1/rooms` | 대화방 목록 (`direct` 1:1 및 `group` 룸) | `{ items: RoomSummary[] }` |
| `POST /api/v1/rooms` | 1:1 또는 멀티캐릭터 룸 생성 (캐릭터·페르소나 스냅샷 복사) | `{ title, mode, persona_id, character_ids, approval_mode_override? }` → `201 RoomDetail` |
| `GET /api/v1/rooms/{id}` | 룸 상태, 참여 멤버 상태(`actor_state`), 진행 중 턴/대기 승인·질문 조회 | `RoomDetail` |
| `PATCH /api/v1/rooms/{id}` | 제목, 참여자, 모델/승인 모드 오버라이드 변경 (실행 중 턴 있으면 `409`) | `RoomPatch` → `RoomDetail` |
| `DELETE /api/v1/rooms/{id}` | 룸 삭제 (멱등) | `204` |
| `GET /api/v1/rooms/{id}/messages` | 채팅 버블 이력 커서 페이지네이션 (`before_seq`, `limit`) | `{ items: MessageBubble[], next_before_seq? }` |
| `POST /api/v1/rooms/{id}/turns` | 새 사용자 턴 시작 (비동기 실행 시작, SSE로 진행 전달) | `{ request_id, text, location_tag? }` → `202 { turn_id, status }` (동일 `request_id` 완료 시 `200`, 실행 중이면 `202`, 다른 입력이면 `409`) |
| `POST /api/v1/rooms/{id}/turns/{turnId}/retry` | 실패/중단된 턴 체크포인트에서 재개 | `202 { turn_id, status }` |
| `POST /api/v1/rooms/{id}/intercept` | 실행 중 **끼어들기(Cut-in)** (`text != ""`) 또는 **멈추기(Stop)** (`text == ""`) | `{ text }` → `202 { accepted: true }` |
| `GET /api/v1/rooms/{id}/stream` | 룸 실시간 이벤트 SSE 스트림 (`Last-Event-ID` 지원) | `text/event-stream` |

### 2.4 승인 게이트 · 사용자 질문(`ask_to_user`)
| 메서드 & 경로 | 용도 | 주요 요청 / 응답 |
| --- | --- | --- |
| `POST /api/v1/rooms/{id}/approvals/{invocationId}` | 대기 중인 도구 호출 승인 또는 거절 | `{ decision: "approve" \| "reject", reason? }` → `200 ToolInvocation` |
| `POST /api/v1/rooms/{id}/questions/{questionId}` | `ask_to_user` 질문 답변 제출 | `{ answer_text }` → `200 UserQuestion` |

### 2.5 지식 · 기억 · 스킬 · 아티팩트 · 유지보수
| 메서드 & 경로 | 용도 | 주요 요청 / 응답 |
| --- | --- | --- |
| `GET /api/v1/knowledge` | 지식 문서 목록 (`scope` 필터) | `{ items: KnowledgeDocSummary[] }` |
| `POST /api/v1/knowledge` | 지식 문서 등록 및 청킹·Outbox 색인 | `{ scope, title, content, media_type }` → `201 KnowledgeDoc` |
| `PATCH /api/v1/knowledge/{id}` / `DELETE /api/v1/knowledge/{id}` | 지식 문서 수정(리비전 증가) / 삭제 | `KnowledgeDoc` / `204` |
| `POST /api/v1/knowledge/search` | 하이브리드/키워드/시맨틱 검색 테스트·조회 | `{ query, mode, scopes, limit }` → `{ hits: [...] }` |
| `GET /api/v1/rooms/{id}/memories` | 룸/캐릭터 기억 및 가치관 이력 조회 | `{ memories, values }` |
| `GET /api/v1/skills` | 등록된 Agent Skills 목록 조회 | `{ items: SkillSummary[] }` |
| `GET /api/v1/artifacts/{id}` | Loom 아티팩트 메타/본문 범위 조회 (`offset`, `limit`) | `{ artifact, content, truncated }` |
| `POST /api/v1/media` / `GET /api/v1/media/{id}` | 이미지 업로드 (`multipart/form-data`, ≤20 MiB) / 조회 | `{ artifact_id, url }` / binary |
| `POST /api/v1/storage/reindex` | RDB 권위 원문 기준 Search 인덱스 재색인 시작 | `202 { job_id }` |

---

## 3. SSE 스트림 계약 (`GET /api/v1/rooms/{id}/stream`)

- **전송 규칙:**
  - 각 이벤트는 `id: <monotonic-int>`, `event: <event-name>`, `data: <json>` 형식을 따른다.
  - 15초마다 주석 heartbeat(`: keepalive\n\n`)를 전송한다.
  - SSE는 실시간 전달 경로일 뿐 권위 저장소가 아니다. 클라이언트는 재연결 시 `GET /api/v1/rooms/{id}` 및 `GET /api/v1/rooms/{id}/messages`로 누락 상태를 동기화한다.

### 3.1 이벤트 타입 목록

| `event` 이름 | `data` 주요 필드 | 발생 시점 |
| --- | --- | --- |
| `turn.started` | `{ turn_id, request_id, user_message_id }` | 사용자 턴 수락 및 사용자 메시지 RDB 커밋 직후 |
| `actor.status` | `{ turn_id, actor_id, actor_name, phase }` (`"recalling"` \| `"deciding"` \| `"thinking"` \| `"acting"`) | 캐릭터 상태 단계 전환 시 |
| `bubble.delta` | `{ turn_id, message_id, actor_id, delta }` | 캐릭터 채팅 버블 스트리밍 토큰 |
| `bubble.completed` | `{ turn_id, message: MessageBubble }` | 캐릭터 채팅 버블 RDB 커밋 완료 |
| `tool.started` | `{ turn_id, invocation_id, actor_id, tool_name, gate_mode }` | 도구 호출 시작 시 |
| `tool.progress` | `{ turn_id, invocation_id, message }` | 장시간 도구(`run_command` 등) 진행 알림 |
| `tool.completed` | `{ turn_id, invocation: ToolInvocation }` | 도구 실행 완료 또는 실패 시 |
| `approval.required` | `{ turn_id, invocation: ToolInvocation }` | `request` 또는 `auto-detection` 판정으로 승인 대기 진입 시 |
| `approval.resolved` | `{ turn_id, invocation_id, decision }` | 사용자 승인/거절 반영 시 |
| `question.required` | `{ turn_id, question: UserQuestion }` | `ask_to_user` 호출로 사용자 입력 대기 진입 시 |
| `question.resolved` | `{ turn_id, question_id, answer_text }` | 사용자 답변 제출 시 |
| `turn.intercepted` | `{ turn_id, kind: "cut_in" \| "stop", text? }` | `InterceptHub` 중단/끼어들기 처리 시 |
| `turn.completed` | `{ turn_id, status: "completed" \| "cancelled" }` | 턴 종료 및 RDB 최종 커밋 후 |
| `turn.failed` | `{ turn_id, error: { code, message, retryable } }` | 턴 오류 종료 시 (재시도 가능 여부 포함) |

---

## 4. 프런트엔드 (`web/`) 계약

- **디렉터리 분리:**
  - `web/src/client/` — React에 의존하지 않는 순수 TypeScript HTTP 클라이언트(`fetch` + `EventSource`/`fetchEventSource` 래퍼) 및 위 DTO/SSE 타입 정의.
  - `web/src/components/ui/` — shadcn/ui 컴포넌트 (Tailwind CSS 변수 토큰 기반).
  - `web/src/components/theme-provider.tsx` — `light` / `dark` / `system` 테마 상태 관리 (`localStorage` + `prefers-color-scheme`).
  - `web/src/features/`:
    1. `rooms/`: 1:1 및 멀티캐릭터 채팅 버블 타임라인, 스트리밍 표시, `Stop` / `Cut-in` 입력창, 실패 턴 `Retry` 버튼
    2. `tools/`: 인라인 도구 실행 상태 카드, **승인 요청 카드(승인/거절)**, **`ask_to_user` 질문 카드(선택지 + 자유 입력)**, **Loom 아티팩트 드로어**
    3. `characters/`: 캐릭터(성격/배경/외형/초기 가치관/초기 상태/모델 설정) 및 사용자 페르소나 편집기
    4. `knowledge/`: 지식 문서 등록/편집/삭제, 하이브리드 검색, 룸 기억/가치관 조회 뷰, Skills 목록
    5. `settings/`: LLM 프로바이더·역할별 모델, Decisions 모델, 저장소 상태, `tools.approval_mode` / `browser_control` / `mcp_servers` 편집기
- **빌드 & 임베드 규칙:** `web/dist`는 Git에 커밋하지 않고 빌드 시 생성해 `pkg/app`의 `//go:embed`로 내장한다.
