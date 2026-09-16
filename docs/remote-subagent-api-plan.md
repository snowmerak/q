# q remote 에이전트 실행 API 구현 상태

작성일: 2026-09-16

상태: 1차 구현 완료. 이 문서는 기존 workspace session, 기본 main agent loop와
`/subagent` 실행을 원격 HTTP 요청으로 그대로 구동하는 `q remote`의 현재 계약과
남은 검증 범위를 소유한다.

대상 독자: Q의 CLI, workspace session, agent event loop, subagent runtime과 HTTP
서비스를 변경하는 구현자와 리뷰어.

갱신 조건: `q remote` 명령, remote 설정 파일, REST route나 event schema, session
점유 규칙, `ask_to_user`의 remote 동작, API key lifecycle 또는 검증 범위가 달라질
때 이 문서를 같은 변경에서 갱신한다. 구현이 끝나면 실제 동작과 검증 결과를
기록하고 상태를 구현 완료로 바꾼다.

## 1. 목표

`q remote`를 실행하면 외부 클라이언트가 다음 작업을 수행할 수 있는 foreground
HTTP 서비스를 시작한다.

1. 지정한 working directory의 Q session 목록을 조회한다.
2. 지정한 working directory에서 적용되는 subagent 목록을 조회한다.
3. working directory와 선택적 session을 결합해 기본 main loop 또는 지정한 기존
   `/subagent` 실행을 시작한다.
4. session을 생략하면 새 session을 만들고 그 ID를 반환한다.
5. 실행 중 발생하는 기존 agent event와 최종 결과를 하나의 HTTP response stream으로
   순서대로 중계한다.

Remote 실행은 새로운 agent 의미를 만들지 않는다. `subagent`가 비어 있으면 기존
`submitChat` main loop를, 값이 있으면 기존 `startCustom` `/subagent` 흐름을 headless
host로 구동한다. 같은 working directory, session과 prompt에 대해 기존 TUI가 사용하는
profile 해석, 모델 선택, 도구, delegation, workspace instruction, archive, Loom capture,
session transcript와 결과 렌더링을 그대로 사용한다.

## 2. 확정된 결정

- `q remote`는 저장된 설정으로 HTTP 서버를 실행한다.
- `q remote config`는 bind IP, port, API key 인증 사용 여부와 key 목록을 관리하는
  standalone TUI를 연다.
- 한 remote 요청은 main agent 또는 subagent 실행 하나를 소유한다. 별도 queue, background run,
  polling resource 또는 resume protocol은 추가하지 않는다.
- 제공된 session이 다른 Q process 또는 remote 요청에 점유되어 있으면 기다리지 않고
  `409 session_busy`를 반환한다.
- session을 제공하지 않으면 `workspace.CreateSession`으로 새 session을 생성하고 전체
  실행 동안 그 lock을 유지한다.
- HTTP client disconnect 또는 request cancellation은 실행 context를 취소하고 session
  lock과 요청별 runtime resource를 해제한다.
- 실행 과정은 `application/x-ndjson`으로 중계한다. WebSocket과 양방향 HTTP channel은
  추가하지 않는다.
- `ask_to_user` 도구는 remote라는 이유로 숨기거나 schema에서 제거하지 않는다.
  호출되면 remote host가 즉시 `interaction_unavailable` tool error를 같은 call ID로
  반환한다. Agent는 그 결과를 보고 계속 진행하거나 `task_complete`를 `blocked`로
  끝낼 수 있다.
- Remote API는 실행 중 사용자 응답을 받지 않는다. 사용자는 terminal 결과를 받은
  뒤 같은 session에 새 remote 실행을 보낼 수 있다.
- API key와 master key는 Gateway keyring과 공유하지 않는다. Gateway model access와
  arbitrary working-directory agent execution은 서로 다른 권한이다.

## 3. 범위

### 포함

- `q remote`와 `q remote config` CLI parsing, help와 usage.
- remote listener와 authentication 설정의 별도 저장소.
- remote API key 생성, 일회성 secret 표시, 목록과 revoke.
- health, session 목록, subagent 목록과 agent 실행 route.
- working directory 정규화와 session별 exclusive lock.
- 기존 session 복원, 새 session 생성, transcript와 archive 저장.
- 기존 main loop와 inner/custom/external subagent의 직접 실행.
- 기존 agent activity, trace, message, tool call/result와 최종 응답의 NDJSON 투영.
- remote 전용 `ask_to_user` unavailable 응답과 이후 agent loop 지속.
- request body, prompt와 concurrent run의 기본 상한.
- configuration, handler, session lifecycle, authentication, streaming과 기존 TUI/ACP
  동작의 회귀 테스트.
- 구현 완료 시 README, help, current-state runtime 문서와 OpenAPI 계약 갱신.

### 포함하지 않음

- 실행 중 remote 사용자가 질문에 답하는 양방향 interaction.
- WebSocket, callback URL, webhook 또는 별도 event broker.
- background execution, reconnect, replay cursor, retry idempotency와 crash resume.
- session 생성·삭제·이름 변경 또는 일반 chat prompt API.
- request별 model, reasoning effort, system prompt, tool 또는 delegate override.
- API key별 working-directory, subagent 또는 read/write scope.
- working-directory allowlist나 jail. 첫 버전의 인증 주체는 `q remote` process가 접근할
  수 있는 경로 전체에 같은 권한을 가진다.
- built-in TLS termination. Non-loopback 또는 신뢰할 수 없는 network에서는 reverse
  proxy, VPN 같은 외부 confidential transport가 필요하다.
- TUI, ACP와 remote가 같은 session을 동시에 공유하는 협업 모드.

## 4. 현재 상태와 재사용 경계

Workspace는 `.q/sessions/<session-id>` 아래에 session projection을 저장한다.
`workspace.Store.ListSessions`가 목록을 제공하고, `workspace.AcquireSessionLock`과
`workspace.CreateSession`이 session별 exclusive ownership을 제공한다. 이미 점유된
session은 `workspace.ErrLocked`로 구분할 수 있다.

Subagent definition은 builtin, global profile과 workspace profile을 하나의 registry로
결합한다. 현재 `/subagents list`와 `/subagent <name> <request>`는 같은 profile store와
workspace-over-global 해석을 사용한다. External profile은 설정된 ACP connection을,
inner profile은 native model role과 현재 workspace tool catalog를 사용한다.

일반 agent loop는 `agentEvent`를 통해 status, activity, trace, message, tool call,
question, task lifecycle과 final response를 host에 전달한다. TUI와 ACP는 각자의 host
adapter에서 이 event를 표시하고 session을 갱신한다. Remote는 세 번째 host adapter가
되어야 하며 별도의 model/tool loop를 복사해서는 안 된다.

현재 `ask_to_user`는 host에 question과 answer channel을 전달한 뒤 응답을 기다린다.
`answer.Err`는 전체 실행 오류로 끝나므로 remote unavailable을 agent가 처리 가능한
tool result로 되돌리려면 구분 가능한 sentinel과 loop 분기가 필요하다.

## 5. 명령과 설정

### CLI

```text
q remote
q remote config
```

`q remote`는 foreground에서 실행하며 persisted network 설정으로 listener를 연다.
성공하면 실제 listen address와 authentication 상태를 stdout에 한 줄로 출력한다.
OS interrupt를 받으면 새 요청 admission을 멈추고 active request를 취소한 뒤 bounded
shutdown한다.

`q remote config`는 chat, provider runtime, workspace lock 또는 remote listener를
시작하지 않고 설정 화면만 연다. 한 화면에서 다음 설정을 관리하며 Network 편집과
key 생성 화면만 필요할 때 연다.

- `Network`: bind IP와 port.
- `API keys`: authentication enabled, active/revoked key 목록, create와 revoke.

Network 변경은 실행 중인 `q remote`를 재시작해야 적용된다. Authentication toggle,
key 생성과 revoke는 설정 파일 watcher가 유효한 snapshot을 읽어 실행 중인 서버에
원자적으로 반영한다.

### 저장 파일

```text
~/.q/remote.json
~/.q/remote.key
```

`remote.json`은 listener와 key metadata/hash만 저장한다. `remote.key`는 key hash를
검증하는 master key이며 별도 private file로 저장한다. 발급된 bearer secret은 생성
직후 한 번만 표시하고 파일, log 또는 session archive에 저장하지 않는다.

초기 설정 모양은 다음과 같다.

```json
{
  "version": 1,
  "server": {
    "host": "127.0.0.1",
    "port": 0
  },
  "authentication": {
    "enabled": false
  },
  "api_keys": []
}
```

기본값은 `127.0.0.1:0`과 authentication disabled다. Host는 IP literal이어야 하고
port는 `0..65535`다. Authentication을 활성화하려면 active key가 하나 이상 있어야
한다. 마지막 active key를 revoke하려면 먼저 authentication을 비활성화해야 한다.

Remote key는 별도 prefix와 hashing domain을 사용한다. 예를 들어 secret은 `qrk_`
prefix를 사용하고 Gateway `qk_` key를 remote endpoint에서 받아들이지 않는다.

## 6. REST 계약

구현 시 `remoteapi/openapi.json`을 유지되는 wire 계약의 authority로 추가하고
`GET /openapi.json`으로 제공한다. Go request/response type과 route는 이 계약을
구현하며 handler test가 method, content type, required field와 대표 response의
일치를 검증한다.

### Health

```http
GET /v1/health
```

Health는 service name, protocol version과 새 실행을 받을 수 있는지를 반환한다.
Credential이나 workspace/session 정보를 포함하지 않으며 authentication 설정과
관계없이 접근할 수 있다.

### Session 목록

```http
GET /v1/sessions?working_directory=<absolute-or-relative-path>
```

서버는 자신의 process working directory를 기준으로 상대 경로를 절대 경로로 만들고
`filepath.Clean`/`filepath.Abs`와 실제 directory 확인을 적용한다. 응답은 canonical
working directory와 newest-first session 목록을 반환한다.

```json
{
  "working_directory": "C:\\work\\project",
  "sessions": [
    {
      "session_id": "2cf6...",
      "run_id": "run-2cf6...",
      "title": "Inspect authentication",
      "updated_at": "2026-09-16T06:10:00Z"
    }
  ]
}
```

목록 조회는 session lock을 획득하지 않고 점유 여부를 보장하지 않는다. 실행 요청의
lock 획득만 authoritative하다.

### Subagent 목록

```http
GET /v1/subagents?working_directory=<absolute-or-relative-path>
```

Working directory는 workspace profile root를 결정하므로 필수다. 응답은 기존
`/subagents list`와 같은 builtin/global/workspace definition과 loading issue를
구조화한다.

```json
{
  "working_directory": "C:\\work\\project",
  "subagents": [
    {
      "name": "builtin/scout",
      "description": "Investigate repository evidence and report bounded findings.",
      "source": "builtin",
      "kind": "inner",
      "role": "scout",
      "mutates_workspace": false,
      "available": true
    }
  ],
  "issues": []
}
```

목록은 canonical ID를 반환한다. 실행 요청은 기존 `/subagent` 호환 규칙과 동일하게
canonical ID와 bare custom profile name을 모두 해석하되 response와 기록에는 resolved
canonical ID를 사용한다.

### Agent 실행

```http
POST /v1/subagent-runs
Content-Type: application/json
Accept: application/x-ndjson
```

```json
{
  "working_directory": "C:\\work\\project",
  "session_id": "optional-session-id",
  "subagent": "builtin/scout",
  "prompt": "인증 흐름을 조사하고 관련 파일과 위험을 정리해줘"
}
```

- `working_directory`, `prompt`는 필수다.
- `subagent`는 선택이다. 비어 있거나 생략하면 기본 main agent loop를 실행하고, 값이
  있으면 기존 `/subagent <name> <request>` 해석을 사용한다.
- Remote `prompt`는 항상 model-visible user message다. `/new`, `/gateway` 같은 TUI
  control slash command를 API로 실행하지 않는다.
- `session_id`가 있으면 해당 session을 lock하고 복원한다.
- `session_id`가 없으면 새 session을 생성한다.
- Prompt 크기는 기존 `MaximumDelegatePromptBytes`를 재사용한다.
- Unknown JSON field와 trailing JSON value는 거부한다.
- Request body 전체에도 prompt보다 약간 큰 고정 상한을 둔다.

Session lock과 실행 준비가 완료된 뒤 `200 OK`와 NDJSON stream을 시작한다. 새 session
ID는 response header `X-Q-Session-ID`와 첫 `session` event에 모두 포함한다.

```json
{"type":"session","working_directory":"C:\\work\\project","session_id":"2cf6...","created":true}
{"type":"activity","agent":"builtin/scout","task_id":"builtin-scout-...","action":"started","detail":"..."}
{"type":"trace","agent":"builtin/scout","kind":"tool_call","call_id":"call-1","name":"read_file","content":"{...}"}
{"type":"trace","agent":"builtin/scout","kind":"tool_result","call_id":"call-1","name":"read_file","content":"...","is_error":false}
{"type":"result","session_id":"2cf6...","outcome":"succeeded","content":"조사 결과 ..."}
```

Event는 한 writer에서 발생 순서대로 쓰고 각 JSON line 뒤에 flush한다. Provider가
명시적으로 반환한 assistant content와 tool traffic은 전달하지만 provider가 반환하지
않은 hidden chain-of-thought를 만들거나 노출하지 않는다. Loom receipt가 적용된 큰 tool
result는 기존 bounded receipt를 전달하고 full artifact를 HTTP body에 다시 펼치지 않는다.

Terminal event는 정확히 하나의 `result`, `cancelled` 또는 `error`다. Runner가
구조화된 outcome을 제공하면 `result.outcome`에 `succeeded` 또는 `blocked`를 함께
싣고, textual external agent처럼 outcome이 없는 기존 경로에서는 이 필드를 생략한다.
Stream을 열기 전 실패는 일반 HTTP status와 JSON error로 반환한다. Stream을 연 뒤
실패는 terminal NDJSON event로 반환하고 연결을 닫는다.

### 오류

오류 body는 안정적인 code와 안전한 message를 가진다.

```json
{
  "error": {
    "code": "session_busy",
    "message": "the selected session is already in use"
  }
}
```

초기 code와 HTTP mapping은 다음과 같다.

| Status | Code | 의미 |
| --- | --- | --- |
| `400` | `invalid_request` | JSON, path, session ID 또는 prompt가 유효하지 않음 |
| `401` | `invalid_api_key` | 인증이 켜졌고 bearer key가 없거나 유효하지 않음 |
| `404` | `session_not_found` | 제공한 session이 working directory에 없음 |
| `404` | `subagent_not_found` | 적용 registry에서 subagent를 찾을 수 없음 |
| `409` | `session_busy` | session lock을 다른 process/request가 소유함 |
| `413` | `request_too_large` | body 또는 prompt 상한 초과 |
| `429` | `remote_capacity` | Remote run admission 상한 초과 |
| `503` | `subagent_unavailable` | model, tool 또는 external ACP binding이 현재 실행 불가 |
| `500` | `internal_error` | 안전하게 분류할 수 없는 server failure |

내부 provider error, credential, raw filesystem failure와 다른 session 정보는 public
message에 포함하지 않고 server log에 credential 없는 진단만 남긴다.

## 7. Session ownership과 실행 lifecycle

실행 준비 순서는 다음과 같다.

1. Request를 bounded strict JSON으로 해석한다.
2. Working directory를 canonicalize하고 directory인지 확인한다.
3. `subagent`가 있으면 현재 global config와 해당 workspace profile을 읽어 resolve한다.
4. 기존 session이면 full runtime 초기화 전에 non-blocking session lock을 획득하고
   projection을 읽는다. 따라서 이미 점유된 session은 불필요한 model/tool startup 없이
   conflict를 반환한다.
5. Provider, Workspace Memory, Library, archive, Loom, MCP와 workspace tool runtime을
   기존 startup 계약으로 초기화한다.
6. Session을 생략했다면 runtime prerequisite가 준비된 뒤 새 session과 lock을 만든다.
7. `subagent`가 없으면 기존 `submitChat`, 있으면 기존
   `/subagent <resolved-name> <prompt>` session 실행을 시작한다.
8. Event를 NDJSON으로 투영하는 동시에 기존 규칙으로 transcript, task state, archive와
   usage를 갱신한다.
9. Success, blocked, failure, cancellation 또는 disconnect 후 요청별 resource와 lock을
   정확히 한 owner가 닫는다.

Runtime 초기화나 subagent resolution 실패만으로 빈 새 session을 남기지 않도록 새
session 생성은 실행 prerequisites가 검증된 뒤 수행한다. Session을 만든 뒤 실행이
실패하면 session과 실패/cancellation 기록은 보존한다. Remote가 생성한 session도 TUI와
ACP session picker에서 일반 session과 동일하게 열 수 있어야 한다.

Remote는 session lock을 기다리거나 기존 owner를 중단하지 않는다. `ErrLocked`는 즉시
`409 session_busy`로 변환한다. 같은 workspace의 서로 다른 session은 기존 session-lock
정책대로 독립 실행할 수 있지만 remote process의 active run 수는
`config.EffectiveAgents().MaxParallel`로 제한하고 queue 대신 `429`를 반환한다.

## 8. 기존 동작 보존과 `ask_to_user`

Remote를 위해 별도의 simplified runner를 만들지 않는다. 구현은 기존 private `model`을
headless host로 감싸고 `submitChat`, `startCustom`, `model.Update`를 직접 재사용한다.
따라서 TUI/ACP 코드를 넓게 이동시키지 않으면서 다음 항목을 공통으로 유지한다.

- `resolvePublicAgent`와 workspace-over-global profile resolution.
- Inner/custom role의 model, effort, tool와 delegation 선택.
- External ACP process와 permission policy.
- Root/nested workspace instruction loading.
- Agent Skill hint, archive search, Loom capture와 usage recording.
- `task_start`, `ask_to_user`, `task_complete` schema와 model-visible tool semantics.
- User command, assistant/tool message와 final response의 session/archive 저장 순서.

Remote host는 question event를 받으면 기다리지 않고 다음 sentinel을 answer channel로
보낸다.

```text
ErrInteractionUnavailable
```

공통 agent loop는 이 sentinel만 특별 처리한다.

1. 같은 `ask_to_user` call ID에 error tool result를 만든다.
2. 내용은 remote mode에서 interactive input을 받을 수 없으므로 현재 정보로 계속하거나
   `task_complete(outcome=blocked)`를 호출하라는 안정적인 문구다.
3. Tool result를 history, session/archive와 remote event stream에 기록한다.
4. Agent loop를 종료하지 않고 다음 model round를 계속한다.

다른 `answer.Err`는 기존처럼 host/execution failure로 처리한다. TUI는 계속 실제 사용자
응답을 기다리고 ACP의 기존 question/answer 흐름도 바뀌지 않는다. Remote unavailable은
`ask_to_user` 도구를 제거하거나 prompt에 autonomous mode를 강제하는 방식으로 구현하지
않는다.

## 9. Authentication과 network 경계

`GET /v1/health`와 `GET /openapi.json`을 제외한 route는 authentication enabled일 때
`Authorization: Bearer <key>`를 요구한다. Authentication disabled이면 middleware가
credential을 요구하지 않는다.

Active key는 모두 동일한 full remote 권한을 가진다. 즉 authenticated caller는 Q
process 계정이 접근할 수 있는 working directory를 선택하고 mutating subagent를 실행할
수 있다. 첫 버전에는 key scope나 path allowlist가 없으므로 UI와 README에 이 권한을
명시한다.

Key 검증은 constant-time hash comparison을 사용한다. Secret이나 Authorization header를
request log, error, trace, session, archive에 남기지 않는다. Key creation과 revoke는 alias,
key ID, time과 결과만 기록할 수 있다.

Non-loopback bind와 authentication disabled 조합은 허용하되 startup과 config UI에 강한
경고를 표시한다. Authentication enabled여도 plain HTTP bearer key는 confidentiality를
제공하지 않으므로 신뢰할 수 없는 network에는 직접 노출하지 않는다.

## 10. Runtime과 configuration lifecycle

`q remote` process는 provider manager, usage recorder, Library와 Workspace Memory를 한 번
초기화한다. Provider child는 첫 실행 요청에서 existing-session lock을 확인한 뒤 한 번만
lazy start하고 이후 concurrent run이 공유한다. Session run은 working-directory-bound
tool, MCP, archive와 Loom resource를 소유하고 종료 시 닫는다. 목록 query는 provider나
전체 model/tool runtime을 시작하지 않는다.

Server startup은 다음을 만족해야 ready다.

- `remote.json`을 읽고 검증했다.
- Authentication enabled이면 master key와 active key를 읽었다.
- Listener bind가 성공했다.
- Process-wide host가 실행 요청을 받을 준비가 되었다. `/v1/health`의
  `accepting_runs`는 admission capacity만 나타내며 provider 가용성을 보장하지 않는다.

Listener bind 변경은 restart 후 적용한다. Authentication과 keyring watcher는 마지막으로
검증된 snapshot을 유지하며 malformed 또는 읽기 실패한 새 파일로 현재 keyring을
교체하지 않는다.

Shutdown 시 새 request를 거부하고 active request context를 취소한다. 각 run은 session
failure/cancellation 기록을 가능한 범위에서 flush하고 ACP child, tool runtime, archive와
session lock을 닫는다. Shutdown deadline 뒤에는 process 종료를 허용하되 다음 실행에서
session lock과 archive가 정상 복구 가능한 기존 contract를 유지한다.

## 11. 구현 구조

현재 package와 책임은 다음과 같다.

| 위치 | 책임 |
| --- | --- |
| `remoteconfig` | Listener/auth 설정, remote key 생성·검증·저장 |
| `remoteapi` | OpenAPI, HTTP handler, strict decode, error와 NDJSON event projection |
| `app/remote*.go` | `q remote config` UI, process-wide `RemoteHost`, 기존 model을 구동하는 headless event host |
| `cmd/q/remote.go` | CLI parsing, listener, authentication reload, server shutdown |
| `workspace` | 기존 session list/create/lock 계약 재사용; 필요한 read-only projection helper만 추가 |
| `subagent` | 기존 registry/runner/result 계약 유지; remote 전용 policy를 넣지 않음 |

가장 중요한 구현 경계는 `app.RemoteHost`와 headless `remoteExecutionModel`이다. HTTP
handler는 private model state를 복제하지 않고, `RemoteHost`가 기존 model entry point와
event update를 호출한다. 이 방식은 공통 service로 TUI/ACP 전체를 이동시키는 큰 refactor
대신 기존 flow를 그대로 재사용하는 최소 변경을 택한 것이다.

API authentication 구현은 Gateway와 source-level utility를 공유할 수 있지만 설정 파일,
master key, secret prefix, hashing domain과 active key set은 분리한다. Gateway key를 remote
key로 자동 migration하거나 양쪽에서 동시에 인정하지 않는다.

## 12. 구현 순서

### 단계 1: Remote configuration — 완료

- `remoteconfig` schema, default, strict load/save와 validation을 추가한다.
- Remote 전용 master key, generate/verify/revoke와 authenticator를 추가한다.
- `q remote config`의 Network/API keys 화면과 저장 실패 처리를 구현한다.
- `q remote`/`q remote config` command parsing과 help를 추가한다.

### 단계 2: 기존 session execution의 headless host — 완료

- `RemoteHost`가 process-wide provider, Library, Workspace Memory와 usage lifecycle을
  소유하고 request별 workspace runtime을 기존 startup contract로 연다.
- 기존 session resume와 새 session 생성은 `workspace`의 lock/create 계약을 그대로 쓴다.
- Main agent는 `submitChat`, subagent는 `startCustom`, event 반영은 `model.Update`를
  headless adapter에서 직접 호출한다.
- `RemoteEventSink`를 추가하되 TUI와 ACP의 observable 순서와 저장 결과는 바꾸지 않는다.
- `errRemoteInteractionUnavailable` sentinel과 error tool-result 전이를 추가했다.

### 단계 3: REST handler와 stream — 완료

- Health, session 목록, subagent 목록과 run handler를 구현한다.
- OpenAPI authority와 handler conformance test를 추가한다.
- Authentication middleware, request bounds, stable error mapping을 적용한다.
- NDJSON event union, flush, terminal event와 disconnect cancellation을 구현한다.

### 단계 4: Service lifecycle — 완료

- `q remote` listener, keyring watcher와 readiness output을 구현한다.
- Global dependency startup, active run admission과 graceful shutdown을 연결한다.
- Network 변경 restart 안내와 unsafe bind/auth 조합 경고를 추가한다.

### 단계 5: 문서와 최종 검증 — 자동 검증 완료, 실제 provider 실행은 별도

- README의 standalone command와 personal-state 표에 remote를 추가한다.
- Current-state remote API와 운영 제약을 별도 문서 또는 이 문서의 완료 상태로 정리한다.
- 이 문서와 README에서 external client → q remote → workspace session/agent 흐름과
  session lock ownership을 현재 동작으로 갱신한다.
- 구현 결과, 실제 test command와 smoke evidence를 이 문서에 기록한다.

## 13. 검증 계획

### Configuration과 authentication

- Default가 `127.0.0.1:0`, authentication disabled인지 검증한다.
- Invalid IP/port, enabled-without-active-key와 duplicate alias/ID를 거부한다.
- Key secret이 생성 시 한 번만 노출되고 저장 파일에는 hash만 남는지 검증한다.
- Valid, missing, malformed, revoked와 Gateway key를 각각 허용/거부한다.
- Authentication toggle과 keyring reload가 새 요청에 원자적으로 반영된다.
- Invalid reload가 마지막 valid keyring을 손상하지 않는다.

### Discovery

- Working directory별 session 목록이 newest-first이며 다른 workspace와 섞이지 않는다.
- Builtin/global/workspace subagent와 profile issue가 기존 `/subagents list` 해석과 같다.
- Workspace profile shadowing과 bare-name compatibility가 기존 direct command와 같다.

### Session 실행

- 제공한 inactive session을 lock, restore, 실행, 저장하고 unlock한다.
- 점유된 session은 실행을 시작하지 않고 즉시 `409 session_busy`를 반환한다.
- Session을 생략하면 새 session을 만들고 header/첫 event/final record에 같은 ID를 쓴다.
- Invalid 또는 없는 session은 새 session으로 묵시적으로 대체하지 않는다.
- Success, blocked, model/tool failure, request cancellation과 disconnect 뒤 lock과 resource가
  해제된다.
- Remote 생성 session을 TUI와 ACP가 이후 정상적으로 열 수 있다.

### 실행 동등성

- 같은 fixture에서 TUI/ACP direct `/subagent`와 remote가 같은 resolved agent, model role,
  tool catalog, final content와 archive shape를 만든다.
- Inner/custom/external agent가 기존 permission과 capture 규칙을 유지한다.
- Nested instruction, Skill hint, delegation과 Loom receipt가 remote에서도 누락되지 않는다.
- Remote refactor 뒤 기존 TUI question answer와 ACP interaction regression test가 통과한다.

### `ask_to_user`

- Remote에서도 기존 조건대로 `ask_to_user`가 model-visible하다.
- 호출 즉시 같은 call ID의 `interaction_unavailable` error tool result가 history와 stream에
  추가된다.
- Agent가 오류 뒤 계속 작업해 succeeded 또는 blocked로 끝낼 수 있다.
- Unavailable sentinel 이외의 host error는 기존처럼 실행을 실패시킨다.

### HTTP와 lifecycle

- Strict JSON, unknown field, trailing value, body/prompt 상한과 content type을 검증한다.
- NDJSON event 순서, line 단위 JSON 유효성, flush와 terminal event exactly-once를 검증한다.
- Stream 전 오류는 HTTP JSON error, stream 후 오류는 terminal event인지 검증한다.
- Capacity 초과는 queue 없이 `429`를 반환한다.
- Interrupt가 admission을 중단하고 active run을 취소·정리한다.
- `go test ./... -count=1`과 실제 `q remote` start/health/list/run/interrupt smoke를 수행한다.

## 14. 구현 결과와 검증

2026-09-16 기준 1차 구현은 다음과 같다.

- `remoteconfig`가 `remote.json`, `remote.key`, `qrk_` key 생성·검증·revoke와
  authentication snapshot reload를 소유한다. Gateway keyring과 hashing domain을
  공유하지 않는다.
- `app.RemoteHost`가 기존 startup과 session lock을 사용하고, headless
  `remoteExecutionModel`이 기존 `submitChat`, `startCustom`, `model.Update`를 구동한다.
- `remoteapi`가 health, OpenAPI, session/subagent discovery, strict run request,
  authentication, admission limit와 NDJSON stream을 구현한다.
- `cmd/q`가 `q remote`, graceful shutdown, settings watcher와 `q remote config` entry를
  제공한다.
- README가 현재 command, 저장 파일, optional `subagent`, 권한과 network 제약을 설명한다.

수행한 자동 검증:

- `go test ./remoteconfig ./remoteapi ./app ./cmd/q -count=1`: 통과.
- `go test ./... -count=1`: 통과.
- `go vet ./...`: 통과.
- Focused `go test -race`는 현재 실행 호스트가 `windows/arm64`라 Go toolchain이 race
  detector를 지원하지 않아 실행되지 않았다.
- Foreground service smoke가 임시 personal store로 실제 listener를 열고
  `GET /v1/health`를 호출한 뒤 context cancellation로 정상 종료함을 검증했다.
- Remote key의 별도 prefix/domain, enabled-without-key 거부, plaintext 비저장, final-key
  revoke 거부, protected route 인증, strict JSON, session busy mapping, 빈 `subagent`의
  main-loop 선택, terminal exactly-once와 `ask_to_user` unavailable 후 계속 실행을
  focused test로 검증했다.

실제 hosted provider를 사용한 main/custom/external agent HTTP end-to-end 실행은 이번
자동 검증에 포함하지 않았다. Provider별 상호운용성은 기존 model/ACP test와 공통 실행
경로 재사용에 기대며, 별도 실제-provider smoke evidence가 추가되기 전까지는 그 경계를
검증 완료로 주장하지 않는다.

## 15. 완료 조건

다음 조건이 모두 충족되면 이 milestone을 완료로 본다.

- 사용자가 `q remote config`에서 listener와 remote 전용 API key를 관리할 수 있다.
- `q remote`가 설정대로 실행되고 authentication policy를 적용한다.
- Client가 working directory별 session과 subagent를 조회할 수 있다.
- Client가 기존 session 또는 자동 생성된 새 session에서 main agent나 subagent를
  실행하고 과정과 terminal 결과를 NDJSON으로 받을 수 있다.
- 기존 owner가 있는 session은 중단하거나 기다리지 않고 명시적인 conflict를 반환한다.
- Remote의 `ask_to_user` 호출은 즉시 model-visible unavailable 결과가 되고 전체 실행을
  강제로 종료하지 않는다.
- TUI와 ACP의 기존 직접 실행, interaction, session/archive 동작이 바뀌지 않는다.
- 인증, session lock, event stream, cancellation과 대표 inner/external 실행에 대한
  자동 검증과 foreground service smoke evidence가 기록된다.
