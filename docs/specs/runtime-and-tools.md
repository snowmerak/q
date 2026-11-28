# 구현 스펙: 런타임 상태 머신 · Decisions · 7대 도구 · 원본 매핑

- **작성일:** 2026-11-28
- **대상 범위:** `pkg/runtime`, `pkg/memory`, `pkg/knowledge`, `pkg/task`, `pkg/llm`, `pkg/cdp` (M1~M4)
- **관련 문서:** [ADR-0002](../adr/0002-chat-bubble-room-and-decisions-protocol.md), [ADR-0003](../adr/0003-tools-approval-gate-and-browser-control.md), [설정·저장소 스펙](./config-and-storage.md), [API·이벤트 스펙](./api-and-events.md)

---

## 1. 턴 상태 머신 (1:1 & 멀티캐릭터 룸)

```text
                POST /turns
                     │
                     ▼
                 [running] ◄─── 승인/질문 응답 제출 후 체크포인트 재개
                  │  │  ▲
     승인 필요 ───┘  │  └─── 질문 응답 대기 (ask_to_user)
          │          │             │
          ▼          ▼             ▼
[awaiting_approval]  │      [awaiting_user]
          │          │             │
          └──────────┴─────────────┴──(Stop/취소)──► [cancelled]
                     │
                     ├──(정상 완료)──► [completed]
                     └──(오류 중단)──► [failed]  ──(POST /retry)──► [running]
```

### 1.1 1:1 모드 (`mode == "direct"`) vs 멀티캐릭터 룸 (`mode == "group"`)
- **공통 사전 단계:**
  1. `room_id + request_id`로 멱등 검사 (완료됨→기존 결과 반환, 다른 `input_hash`→`409`, 미완료→체크포인트 재개, 신규→사용자 메시지 RDB 커밋).
  2. `memory_recall_gate` Decisions → 필요 시 경량 플래너(`minimal`→`low` 추론 강제) 쿼리 생성 → `SearchIndex.Search(ObserverID)` 하이브리드 검색 → `memory_recall_select` Decisions 선별(최대 8개, 컨텍스트 예산 10% 상한).
- **1:1 (`direct`):**
  - 참여자 1명 고정. `participation` / `user_turn` Decisions를 생략하고 즉시 해당 캐릭터 모델(도구 포함)을 호출해 채팅 버블을 생성한다.
- **멀티캐릭터 룸 (`group`):**
  - 호스트 `reserveActor`가 각 활성 캐릭터의 유효 속도를 계산해 게이지를 진행한다(Evernight `paced-state.ts` 공식 계승):
    $$\text{speed} = \text{clamp}\bigl(\text{baseSpeed} \times (1 + \frac{\text{activation}}{200}),\ 1,\ 300\bigr)$$
  - 선택된 캐릭터에 대해 `participation` Decisions(`act` / `wait`) 호출. `act`면 해당 캐릭터가 채팅 버블 생성(필요 시 도구 루프 실행).
  - 캐릭터 발화 후 `user_turn` Decisions 호출: `true`이거나 모든 캐릭터가 연속 `wait`, 또는 `max_turns_per_input`(기본 6)에 도달하면 사용자에게 턴을 넘긴다.

### 1.2 `observedBy` 인지 격리 & 컨텍스트 압축
- 룸 내 생성되는 모든 `messages`와 `memories`에는 `observed_by_json`이 저장된다.
- 캐릭터 프롬프트 조립 및 기억 검색(`SearchQuery.ObserverID`) 시 `observed_by`에 해당 캐릭터 ID가 포함된 항목만 주입한다. `subtext`(선택적 비공개 행간)는 본인 컨텍스트 외 다른 캐릭터에게 노출하지 않는다.
- 각 캐릭터 컨텍스트는 Append-only 프리픽스를 유지하고, 누적 입력이 유효 컨텍스트 예산의 **85%**를 넘으면 (1) 가치관 후보 추출 → `value_jobs` 큐 적재, (2) 오래된 확정 기록 요약 압축(최근 10% 원문 보존, 요약 목표 10%)을 수행한다. `maxOutputTokens`는 입력 한도에 선합산하지 않는다.

### 1.3 `InterceptHub` 실시간 인터럽트
- 실행 중인 턴은 룸별 취소 가능한 추측 컨텍스트(`speculativeCtx`)를 등록한다.
- `POST /intercept` 수신 시(진행 락 대기 없이) 즉시 `speculativeCtx`를 취소하고 미커밋 추측 생성만 폐기한다.
  - `text != ""` (`cut_in`): 새 사용자 메시지를 커밋하고 `speculativeCtx`를 재시작해 이어간다.
  - `text == ""` (`stop`): 상태를 `cancelled`로 정리하고 종료한다.

---

## 2. Decisions 질문 계약 (6종)

`pkg/llm` 내부 Decisions 클라이언트는 `openai`(`/decisions`), `jev`(`/systemone`), `gemini`(`/decisions`) 프로토콜을 지원한다.

| 질문 키 | 유형 (`predicate` / `choice` / `score`) | 입력 컨텍스트 요약 | 출력 사용 방식 |
| --- | --- | --- | --- |
| `memory_recall_gate` | `predicate` (`needs_recall`) | 최근 대화 창 + 현재 사용자 입력 + 미주입 기억 존재 여부 | `>= 0.5`일 때만 회상 플래너 + 검색 실행 |
| `memory_recall_select` | 후보 ID별 `predicate` | 검색 후보(최대 16개) + 현재 입력 | 확률순 상위 최대 8개(예산 10% 내) 주입 |
| `participation` | `choice` (`act` / `wait`) | 선택된 캐릭터 관점 컨텍스트(`observedBy` 필터 적용) | `act`면 발화, `wait`면 다음 게이지 주체로 이동 |
| `user_turn` | `predicate` (`handoff`) | 직전 생성된 채팅 버블 + 참여자 목록 | `>= 0.5`면 룸 루프 종료 후 사용자 턴 전환 |
| `state_update` | `self` + 상대별 `change`(`predicate`), `valence`/`activation`/`trust`(`score` 5단계: `-20,-10,0,+10,+20`), `stress`(`predicate`) | 확정된 턴 경험 (`observedBy` 내 참여자만 대상) | Evernight `character-experience.ts`와 동일하게 `[-100, 100]` 클램프 갱신 |
| `approval_auto_detection` | `predicate` (`requires_approval`) | 도구명, 분류(`read`/`write`/`exec`/`external`), 인자 JSON 요약, 최근 사용자 요청 | `>= 0.5` 또는 **Decisions 호출 실패 시(fail-closed)** `awaiting_approval`로 전환 |

---

## 3. 1차 내장 도구 7개 영역 상세 계약 (`pkg/task`)

모든 도구 호출은 모델 응답에서 수신되면 **승인 게이트(`bypass` / `auto-detection` / `request`)**를 거친 뒤 실행된다.

| 영역 | 도구 이름 | 핵심 계약 및 안전 경계 |
| --- | --- | --- |
| **1. 파일·디렉터리** | `read_file`, `edit_file`, `write_file`, `list_directory`, `create_directory`, `move_path`, `copy_path`, `remove_path` | - `tools.allowed_roots`에 등록된 경로 내부만 접근 허용(미설정 시 읽기 전용 또는 명시적 승인 필수 정책 적용 권장; 어휘적 `..` 및 심링크 탈출 거절).<br>- 전역 `<data-dir>` 내부 DB/인덱스(`q.sqlite`, `index/`, `blobs/`, `instance.lock`) 직접 수정 차단.<br>- `read_file`은 `LINE#HASH:` 앵커를 반환하고 `edit_file`은 최신 앵커 일치 시에만 원자적 적용. |
| **2. 셸·커맨드** | `run_command`, `cmd_status`, `wait` | - `run_command`는 비동기 `command_id` 반환. `cmd_status`/`wait`(최대 60초)로 증분 출력 조회.<br>- 출력 버퍼 상한 4 MiB, 청크 최대 256 KiB. `command_timeout_ms` 초과 또는 사용자 Stop 시 프로세스 트리 종료. |
| **3. 아카이브·지식** | `search_archive`, `get_archive_record` | - 현재 룸의 기억/메시지(`ObserverID` 강제) 및 허용 스코프의 `knowledge_chunks`를 `SearchIndex.Search`로 조회. |
| **4. Loom 아티팩트** | `loom_inspect`, `loom_read`, `loom_eval` | - 임계값(예: 16 KiB)을 넘는 도구/MCP/브라우저 출력은 `blobs/loom/`에 `loom://<32hex>` 불변 아티팩트로 저장하고 모델에는 요약+참조만 반환.<br>- `loom_read` 기본 64 KiB / 최대 1 MiB. `loom_eval`은 Goja JS 샌드박스로 타임아웃/메모리 제한 하에 필터·변환 실행. |
| **5. Agent Skills** | `search_skills`, `get_skill` | - `<data-dir>/skills/` 또는 설정된 스킬 디렉터리에서 스킬 메타 검색 및 본문/참고 리소스 읽기. |
| **6. 외부 MCP & 브라우저** | 동적 `mcp_<server>_<tool>` 및 `browser_*` | - `mcp_servers` 설정의 stdio/HTTP MCP 서버 연결 및 도구 목록 동적 등록.<br>- `browser_control` config(`enabled`, `preferred`: `"cdp-q-driver"` \| `"ego-browser"`, `drivers.<name>.command/args`)에 따라 드라이버 실행. `cdp-q-driver`는 내장 `pkg/cdp`(`q cdp`) 또는 독립 CLI 사용. |
| **7. 오케스트레이션** | `ask_to_user`, `task_start`, `task_complete`, `delegate_task` | - `ask_to_user` 호출 시 `user_questions`에 저장 후 `awaiting_user` 상태로 대기(DB 락 미점유).<br>- `delegate_task`는 최대 깊이 `tools.max_delegation_depth`(기본 2) 내에서 하위 서브에이전트 루프를 실행하며 부모의 승인 게이트 정책을 상속. |

---

## 4. 원본 저장소 참조 매핑표 (Codex 포팅 가이드)

Codex가 각 마일스톤을 구현할 때 참고해야 할 **원본 파일 경로**와 **Q v2 이식 규칙**이다.

| 원본 저장소 & 경로 | Q v2 대상 패키지 | 이식 시 변경·주의 규칙 |
| --- | --- | --- |
| `c:\Users\snowm\Projects\snowmerak\llm-provider\*` (`capabilities.go`, `client.go`, `types.go`, `gateway/`, `providers/`, `cmd/llm-provider/`) | `pkg/llm/*`, `cmd/llm-provider/` | Go import 경로를 `github.com/snowmerak/llm-provider` → `github.com/snowmerak/q/v2/pkg/llm`으로 치환. 상위 Q 패키지 역참조 절대 금지. 기존 단위 테스트 모두 함께 이식. |
| `c:\Users\snowm\Projects\snowmerak\cdp-q-driver\*` (`browser.go`, `page.go`, `actions.go`, `cli/`, `cmd/cdp-q-driver/`, `internal/cli/`) | `pkg/cdp/*`, `cmd/cdp-q-driver/` | `github.com/snowmerak/cdp-q-driver` → `github.com/snowmerak/q/v2/pkg/cdp`로 치환 (`internal/cli`는 `pkg/cdp/internal/cli`로 이동해 `cmd/q cdp`와 `cmd/cdp-q-driver`가 공유). 상위 Q 패키지 역참조 금지. |
| `snowmerak/q` (`main` 브랜치): `sessionstore/{store.go,vector.go,index.go,record.go}`, `opensearchindex/*`, `storageconfig/*` | `pkg/storage/{rdb,search}/`, `pkg/app/config/` | 다중 프로세스 락·리더 선출 제거. `bleve/v2` + `goformersearch` HNSW 및 OpenSearch alias/backing-index 패턴만 추출. |
| `snowmerak/q` (`main` 브랜치): `loom/*`, `tools/builtin/{register,files,edit,hashline,commands,loom,skills}.go`, `tools/{external,runtime}.go`, `agentloop/orchestration.go` | `pkg/task/` | `.q` 워크스페이스 락·Git worktree 의존성 제거. `allowed_roots` + 승인 게이트(`ApprovalGate`) 래퍼 적용. `lsp.go`와 `workitems.go`는 제외. |
| `snowmerak/q` (`main` 브랜치): `desktop/*`, `scripts/desktopbuild/*` | `desktop/` | 동반 `q-engine` 프로세스 실행 코드를 제거하고 `pkg/app` 직접 import/인프로세스 구동으로 변경. Wails `v3.1.0` 유지. |
| `rileyayla/evernight`: `src/lib/server/ai/{simple-chat.ts,paced-loop.ts,paced-state.ts,character-experience.ts,memory-recall.ts,values.ts,values-worker.ts,intercepts.ts,decision-client.ts}` | `pkg/runtime/`, `pkg/memory/`, `pkg/llm/decisions/` | TypeScript → Go로 포팅. `narration.ts`, `name-discovery.ts`, `pov_review`, `prose` 재집필 파이프라인은 **이식하지 않음**. |
