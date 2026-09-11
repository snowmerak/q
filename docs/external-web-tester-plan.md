# External Web Tester 연결 및 Plan executor 구현 계획

작성일: 2026-09-11

상태: 구현 완료(2026-09-11). 이 문서는 `external_web_tester` milestone의 범위,
구현 결정과 검증 기록을 소유한다. 현재 동작의 기준은
`agent-invocation-runtime.md`, `plan-orchestration.md`,
`execution-orchestration.md`다.

대상 독자: Q의 agent connection, `/plan`, ACP 및 실행 checkpoint를 변경하는 구현자와
리뷰어.

갱신 조건: discovery 규칙, executor 전이, checkpoint 형식, 자동 ACP 권한 처리 또는
검증 범위가 달라질 때 이 문서를 먼저 또는 같은 변경에서 갱신한다. 구현 완료 시 실제
동작과 검증 결과를 기록하고 현재 동작 문서와 architecture 산출물을 동기화한다.

## 1. 목표

`q agents`가 관리하는 ACP connection에 새 외부 역할 `external_web_tester`를
할당할 수 있게 한다. 할당된 Web Tester는 다음 세 경로에서 같은 invocation 계약을
사용한다.

1. 일반 채팅의 모델 호출 도구 `external_web_tester`.
2. 사용자가 명시적으로 실행하는 `/agent:web-tester <request>`.
3. 승인된 `/plan` task를 수행하는 Coder와 동등한 executor.

외부 역할은 구성된 capability다. 활성 connection이 없는 `external_*` capability는
실행 시점에 오류를 내는 숨은 기능이 아니라, 애초에 도구·명령·Plan schema에서
발견되지 않아야 한다.

## 2. 확정된 범위

### 포함

- `agents.roles.external_web_tester.agent` 외부 역할과 `q agents` 할당 UI.
- 활성 외부 역할을 판정하는 하나의 공통 discovery 함수.
- 일반 채팅 Default role의 `external_web_tester` 도구.
- TUI와 ACP의 `/agent:web-tester` 명령.
- 연결 상태에 따라 달라지는 slash completion, help, ACP `AvailableCommands`.
- 연결 상태에 따라 달라지는 Planner의 Plan executor schema와 지시.
- Coder와 External Web Tester를 동등한 task executor로 다루는 승인 후 실행 loop.
- Planner review를 통한 재시도, executor 전환, tester 재검증.
- ACP 실행 결과의 Loom capture, progress/trace, durable checkpoint와 실행 기록.
- 기존 Coder-only Plan과 checkpoint의 하위 호환.
- 현재 정적으로 노출되는 `/agent:search` discovery를 같은 규칙으로 수정.

### 포함하지 않음

- Griller, Coder, Scout 또는 Advisor에 `external_web_tester` 도구를 제공하는 작업.
- Planner에게 `external_web_tester`를 일반 tool call로 제공하는 작업. Planner는 Plan
  계약과 review transition을 통해 executor를 선택하며 직접 ACP 도구를 호출하지 않는다.
- 기존 `external_search`의 역할별 도구 노출 정책 변경. Search의 현재 Griller,
  Planner, Advisor 노출은 유지하고 command discovery만 조건부로 바로잡는다.
- Plan task의 병렬 실행 또는 `agents.max_parallel` 의미 변경.
- connection별 권한 설정 UI, 실행 중 permission 질문 또는 승인 단계.
- MCP server나 custom `/subagent` 프로필과의 통합.
- ACP 외의 별도 브라우저 프로토콜 또는 별도 미디어 저장소 도입.

## 3. 현재 상태와 확인된 간극

현재 외부 ACP connection은 `AgentsConfig.Connections`에 저장되고 Search 역할만
`AgentConfig.Agent`로 이를 참조할 수 있다. `q agents`의 External Roles 목록도
Search 하나만 제공한다.

`external_search` tool runtime은 Search 역할이 unassigned, disabled 또는 connection
누락 상태이면 도구를 광고하지 않는다. 반면 `/agent:search`는 다음 위치에서 정적으로
광고되므로 이번 discovery 요구를 완전히 만족하지 않는다.

- TUI의 `localSlashCommands`와 completion.
- ACP session의 `AvailableCommands`.
- ACP `/help` 응답.

README처럼 설치·설정 방법을 설명하는 정적 문서는 runtime discovery로 보지 않는다.
대신 외부 명령이 connection 할당 후에만 나타난다는 전제와 설정 방법을 명시한다.

Plan은 모든 task를 Coder가 수행한다고 가정한다. `PlanStep`, execution phase,
pending result, Planner review request, 복구 문구와 최종 렌더링이 Coder 타입과 이름에
결합되어 있다. 따라서 Web Tester를 Plan에 연결하려면 ACP handler 추가보다 먼저
executor와 checkpoint 계약을 일반화해야 한다.

## 4. Capability discovery 계약

### 가용성 판정

외부 capability는 현재 configuration snapshot에서 다음 조건을 모두 만족할 때만
available이다.

1. 해당 외부 역할이 `agents.roles`에 존재한다.
2. 역할의 `agent`가 공백이 아닌 connection ID를 가진다.
3. 해당 ID가 `agents.connections`에 존재한다.
4. connection이 disabled가 아니다.

이를 `configuredExternalAgent(config, role)`와 같은 하나의 공통 함수가 판정한다.
Search와 Web Tester가 각자 map을 직접 검사하지 않는다. connection process를 실제로
시작하는 readiness probe는 menu/schema rendering 중 수행하지 않는다. 구성상 available인
process가 시작되지 않으면 invocation에서 명시적인 dependency failure로 처리한다.

### discovery 표면

공통 판정 결과는 다음 모든 표면에 동일하게 적용한다.

| 표면 | Search | External Web Tester |
| --- | --- | --- |
| `q agents` External Roles | 항상 표시 | 항상 표시 |
| 일반 채팅 도구 | 기존 허용 role에서만 조건부 표시 | Default에서만 조건부 표시 |
| TUI slash completion | 조건부 `/agent:search` | 조건부 `/agent:web-tester` |
| TUI help | 조건부 | 조건부 |
| ACP `AvailableCommands` | 조건부 | 조건부 |
| ACP `/help` | 조건부 | 조건부 |
| Planner `submit_plan` executor | 해당 없음 | 조건부 |
| 직접 입력 command handler | available일 때만 전용 명령 처리 | available일 때만 전용 명령 처리 |

`q agents`는 capability를 설정하는 유일한 관리 표면이므로 역할 행을 항상 보여 준다.
그 밖의 표면에서는 unavailable 외부 capability의 이름, usage 또는 schema enum을
광고하지 않는다. 사용자가 unavailable 명령을 직접 입력하면 외부 역할 전용 설정
안내로 존재를 드러내지 않고 일반적인 unknown command 처리를 따른다.

설정 저장은 현재와 같이 다음 turn 또는 다음 planning run의 configuration snapshot부터
반영한다. 진행 중 invocation의 connection을 중간에 교체하지 않는다.

## 5. 역할과 노출 행렬

`external_web_tester`의 모델-visible tool 노출은 Default에만 허용한다.

| 호출 역할 | `external_web_tester` tool |
| --- | --- |
| Default 일반 채팅 | available일 때 제공 |
| Griller | 제공하지 않음 |
| Planner planning | 제공하지 않음 |
| Planner review | 제공하지 않음 |
| Coder | 제공하지 않음 |
| Scout | 제공하지 않음 |
| Advisor | 제공하지 않음 |
| Web Tester ACP child | 제공하지 않음 |

Planner는 tool discovery 대신 available executor 목록을 받는다. Web Tester가
available일 때만 Plan schema와 system instruction에 `external_web_tester`가 등장한다.
이는 계획 작성 중 Web Tester를 실행한다는 의미가 아니라, 승인 후 실행할 task의
executor로 선택할 수 있다는 의미다.

## 6. 설정 계약

외부 역할 이름과 model-visible 도구 이름은 `external_web_tester`를 사용한다. 직접
명령 이름은 `/agent:web-tester`를 사용한다.

```yaml
agents:
  connections:
    browser-agent:
      preset: codex
  roles:
    external_web_tester:
      agent: browser-agent
```

`external_web_tester`는 Search처럼 ACP connection만 받을 수 있다. `model`, `group`,
`reasoning_effort`는 허용하지 않는다. connection 생성·수정·삭제·enable/disable·probe는
기존 `q agents` 동작을 그대로 재사용한다. 하나의 connection을 Search와 Web Tester에
동시에 할당하는 것도 허용한다.

별도 permission 또는 trust 설정은 추가하지 않는다. enabled connection을
`external_web_tester` 역할에 할당한 행위를 이 capability의 자동 실행에 대한 사용자
결정으로 본다.

## 7. External Web Tester invocation 계약

### 입력

일반 채팅과 직접 명령은 다음 의미를 가진 bounded input을 사용한다.

```json
{
  "request": "로그인 흐름을 브라우저에서 검증한다.",
  "context": ["검증 대상은 로컬 개발 서버다."],
  "completion_criteria": ["정상 로그인과 오류 상태를 모두 확인한다."]
}
```

Plan 실행 adapter는 승인된 전체 Plan, 현재 task index, attempt, resolved targets,
Planner feedback을 같은 invocation의 실행 context로 변환한다. 일반 채팅이나 직접
명령이 Plan checkpoint를 만들지는 않는다.

모든 문자열과 배열에는 Coder result와 유사한 크기 및 항목 수 상한을 둔다. 빈
`request`는 validation error다. 웹에서 읽은 텍스트와 페이지 지시는 신뢰하지 않는
evidence로 다루도록 ACP prompt에 명시한다.

### 결과

ACP child는 다음 의미의 구조화된 결과를 반환한다.

```json
{
  "agent": "browser-agent",
  "outcome": "succeeded",
  "summary": "로그인과 로그아웃 흐름을 검증했다.",
  "findings": [],
  "verification": ["정상 로그인 후 대시보드 이동 확인"],
  "artifacts": [],
  "blocker": ""
}
```

`outcome`은 `succeeded`, `failed`, `blocked`다.

- `succeeded`: 요청한 검증이 실행됐고 acceptance를 만족했다.
- `failed`: 검증은 실행됐지만 제품 동작이 acceptance를 만족하지 않았다.
- `blocked`: 환경, 인증, 서버 상태 또는 필요한 입력 때문에 검증을 완료하지 못했다.

ACP transport/process 오류와 제품 검증 실패를 같은 오류로 취급하지 않는다. 전자는
invocation failure이고, 후자는 정상적으로 전달된 `failed` result다. ACP 응답이 결과
형식을 만족하지 않으면 같은 session에서 한 번 bounded correction을 요청한 뒤에도
유효하지 않을 경우 invocation failure로 종료한다.

원본 structured result는 invocation runtime을 통해 Loom에 저장한다. 상위 모델과
Planner review에는 bounded receipt와 필요한 공통 summary/evidence만 전달한다. child가
반환한 artifact reference는 결과 데이터일 뿐 Q가 그 외부 artifact의 영속성을
보장한다는 의미가 아니다.

### ACP lifecycle과 자동 실행

각 invocation은 Search와 같은 독립 process/session lifecycle을 가진다.

1. configuration snapshot에서 command와 auth method를 결정한다.
2. ACP process를 시작하고 initialize/authenticate한다.
3. 현재 workspace root로 임시 session을 생성한다.
4. Web Tester 전용 prompt를 실행한다.
5. ACP permission request에 사용자 질문 없이 `allow_once`를 우선 선택하고, 해당
   option이 없으면 제공된 `allow_always`를 선택한다. 허용 option이 없으면
   invocation failure로 종료한다.
6. 결과를 검증하고 Loom에 capture한다.
7. 지원하면 session을 delete하고, 아니면 close한 뒤 child process를 종료한다.

별도 permission UI나 connection 설정은 없다. 자동 승인 동작은 Web Tester invocation에만
적용하고 Search의 read-only policy와 일반 ACP 대화의 interactive policy는 변경하지
않는다. child process는 사용자가 선택한 외부 executable이며, role assignment가 그
process에 대한 신뢰 경계다. 환경 변수의 값, 인증 정보, 브라우저 cookie 또는 page
content를 progress, trace, 오류 메시지나 checkpoint에 복제하지 않는다.

각 Web Tester invocation에는 parent context cancellation과 별도의 고정된 15분
deadline을 함께 적용한다. 설정 항목은 추가하지 않는다. deadline을 넘기면 ACP
session과 child process를 정리하고 timeout failure를 반환한다.

## 8. 일반 채팅과 직접 명령

### 일반 채팅

Default tool runtime은 Web Tester가 available일 때만 `external_web_tester` invocation을
추가한다. 기존 invocation runtime의 이름 충돌 검증과 mandatory Loom capture를
그대로 적용한다. 결과는 정상적인 assistant tool call과 matching tool result로
대화, session archive 및 Loom에 남는다.

### `/agent:web-tester`

직접 명령은 `/agent:search`와 같은 흐름을 재사용한다.

1. q가 synthetic assistant tool call을 만든다.
2. 동일한 `external_web_tester` invocation runtime을 호출한다.
3. matching tool result를 기록한다.
4. 기존 Default parent loop가 receipt를 근거로 최종 사용자 응답을 작성한다.

TUI와 ACP server는 하나의 service 함수와 입력 변환을 공유한다. 두 표면이 별도의
결과 형식이나 archive 의미를 갖지 않는다.

## 9. Plan schema

`PlanStep`에 executor를 추가한다.

```json
{
  "title": "로그인 흐름 브라우저 검증",
  "description": "실제 UI에서 정상 로그인, 오류 상태와 로그아웃을 검증한다.",
  "executor": "external_web_tester",
  "target": {
    "any": [{
      "all": [{
        "kind": "paths",
        "paths": ["web/login.tsx", "web/session.ts"]
      }]
    }]
  },
  "verification": ["정상 로그인 후 대시보드가 표시된다."]
}
```

`executor`는 task의 최초 및 acceptance executor다.

- 항상 허용되는 값: `coder`.
- Web Tester가 available일 때만 추가되는 값: `external_web_tester`.
- 새 Planner tool schema에서는 `executor`를 required로 광고한다.
- legacy proposal과 persisted checkpoint에서 필드가 없으면 `coder`로 정규화한다.
- 알려지지 않았거나 현재 planning run에 available하지 않은 executor는 proposal
  validation error다.

Target condition은 두 executor 모두 유지한다. Coder에게는 변경 범위이고 Web
Tester에게는 task와 관련된 workspace context다. Web Tester가 target 파일을 변경할
권한을 의미하지 않는다. 별도 URL 필드는 이번 milestone에 추가하지 않고 task
description, context와 verification에 검증 대상을 명시한다.

Plan 렌더링과 승인 화면에는 각 task의 executor를 표시한다. 사용자가 승인 전에 어떤
외부 agent가 실행될지 확인할 수 있어야 한다.

## 10. 실행과 Planner review

### 공통 executor 경계

현재 `ExecutionLoop.Coder`를 executor ID로 dispatch하는 좁은 함수 계약으로 바꾼다.

```text
approved PlanStep
       │
       ▼
TaskAttempt + active executor
       │
       ├── coder adapter ───────────────┐
       │                                │
       └── external_web_tester adapter ─┤
                                        ▼
                              bounded TaskResult
                                        │
                                        ▼
                                Planner review
```

공통 attempt에는 Plan, task index, attempt number, resolved targets, feedback와 active
executor가 포함된다. 공통 result는 executor, outcome, summary, findings, artifacts,
verification, blocker와 bounded Loom evidence를 보존한다. Coder의 기존 completion과
evidence 수집은 Coder adapter 내부에서 공통 result로 변환한다.

등록되지 않은 executor나 현재 unavailable인 외부 executor를 Coder로 자동 대체하지
않는다. 실행 전 또는 resume 시 actionable unavailable error를 반환하고 checkpoint를
유지한다.

### review transition

Planner는 매 attempt 결과를 검토해 `next` 또는 `retry`를 반환한다. `retry`에는 다음
attempt를 수행할 executor를 선택하는 `next_executor`를 추가한다.

```json
{
  "decision": "retry",
  "next_executor": "coder",
  "feedback": "로그인 제출 뒤 세션 cookie가 저장되지 않는다. tester evidence를 확인하고 수정한다.",
  "fact_changes": []
}
```

- `next`: 현재 task를 승인하고 다음 task로 이동한다.
- `retry`: 같은 task와 target을 유지하고 `next_executor`로 새 attempt를 실행한다.
- `next_executor`가 없으면 현재 executor를 반복한다.
- `next_executor`는 현재 planning/execution capability set에 존재해야 한다.
- Web Tester task의 `next`는 active executor가 `external_web_tester`이고 결과가
  `succeeded`일 때만 유효하다.

따라서 Web Tester가 제품 결함을 찾았을 때 다음 흐름을 표현할 수 있다.

```text
external_web_tester failed
          │ retry → coder
          ▼
       Coder repair
          │ retry → external_web_tester
          ▼
       Web retest succeeded
          │ next
          ▼
       다음 Plan task
```

Planner review는 `external_web_tester` tool을 직접 받지 않는다. coordinator가 review의
검증된 transition을 checkpoint에 기록한 뒤 다음 executor를 호출한다.

### attempt 상한

기존 task attempt 상한은 executor가 바뀌어도 같은 task 전체에 적용한다. executor
전환으로 attempt budget을 초기화하지 않는다. configuration, schema 또는 unavailable
오류를 같은 attempt에서 무한 재시도하지 않는다.

## 11. Checkpoint, 복구와 호환성

실행 phase는 Coder 이름 대신 executor 의미를 갖도록 일반화한다.

```text
target
  → executor_pending
  → executor_running
  → review_pending
  → completed
```

checkpoint에는 현재 executor와 pending common result를 저장한다. 외부 Web Tester
호출 전에 `executor_running`을 durable하게 기록하고, 성공 결과를 받은 뒤
`review_pending`과 Loom receipt를 저장한다. `review_pending`에서 재개할 때 Web Tester를
다시 호출하지 않는다.

`executor_running` 중 프로세스가 중단되면 외부 사이트에 어떤 동작이 적용됐는지 Q가
확정할 수 없다. 기존 Coder와 마찬가지로 호출을 성공으로 추정하지 않는다. resume은
attempt를 증가시키고 이전 attempt가 부분 실행됐을 수 있다는 feedback을 주어 새
invocation을 시작한다. Web Tester prompt는 상태를 먼저 재확인하고 destructive setup을
반복하지 않도록 지시한다.

persisted execution file은 명시적인 새 version으로 쓰고 기존 version을 읽는 migration을
추가한다.

- 누락된 PlanStep executor → `coder`.
- `coder_pending` → `executor_pending`, active executor `coder`.
- `coder_running` → `executor_running`, active executor `coder`.
- 기존 `CoderResult` → executor가 `coder`인 common TaskResult.
- 기존 completed execution history는 다시 쓰지 않고 기존 reader 의미를 유지한다.

새 binary가 기존 active checkpoint를 resume하는 compatibility test를 추가한다. 구현
도중 rollback이 필요한 경우 새 checkpoint를 구 binary가 읽을 수 있다고 약속하지
않으며, migration 적용 전후의 지원 범위를 README 또는 release note에 명시한다.

## 12. 관측성과 결과 보존

Web Tester attempt는 Coder와 같은 execution ID 아래 별도 task ID를 가진다. progress와
trace에는 최소한 다음 전이가 보여야 한다.

- queued / started / thinking 또는 tool activity / completed / failed.
- executor ID, Plan task index, attempt number와 parent execution ID.
- ACP session 시작 실패, invalid result, cancellation과 cleanup failure.
- Planner review가 선택한 `decision`과 `next_executor`.

웹 페이지 본문, cookie, token, authorization header, connection environment 값은 trace와
checkpoint에 원문으로 남기지 않는다. 상세 결과는 Loom receipt로 연결하고 execution
checkpoint에는 bounded summary와 reference만 둔다. 완료된 실행은 기존
`.q/plan-executions/` archive lifecycle을 따른다.

## 13. 구현 단계

### 단계 1: 외부 역할과 공통 discovery

- `AgentRoleExternalWebTester`와 외부 역할 판별/검증 helper 추가.
- Search 전용 config validation을 공통 외부 역할 validation으로 변경.
- `q agents` External Roles에 `external_web_tester` 추가.
- TUI/ACP command catalog와 help를 configuration-aware builder 하나로 통합.
- `/agent:search`가 unavailable일 때 completion/help/ACP discovery에서 사라지도록 수정.

완료 조건: connection의 absent, unassigned, missing, disabled, enabled 조합에 대해 모든
discovery 표면이 같은 결과를 낸다.

### 단계 2: Web Tester invocation과 사용자 진입점

- strict input/result 계약과 ACP prompt 추가.
- Search와 공유하는 ACP process/session 생성 및 cleanup helper 추출.
- Web Tester 자동 permission mode 추가.
- Default runtime에만 조건부 invocation 등록.
- TUI/ACP `/agent:web-tester`를 synthetic tool call 경로에 연결.
- Loom capture와 parent response handoff 연결.

완료 조건: 일반 채팅과 직접 명령이 같은 invocation, receipt, archive lifecycle을
사용하고 다른 역할에는 도구가 광고되지 않는다.

### 단계 3: 동적 Plan 계약

- `PlanStep.executor`와 legacy default 추가.
- available executor를 받는 Planner prompt/tool schema/parser 추가.
- 승인 화면과 Plan rendering에 executor 표시.
- unavailable executor proposal을 승인 전에 거부.

완료 조건: Web Tester connection 유무에 따라 Planner가 받는 schema와 생성 가능한
Plan이 달라지고, legacy Plan은 Coder Plan으로 유지된다.

### 단계 4: executor 실행 loop와 review routing

- Coder-only attempt/result/dispatch를 공통 executor 계약으로 일반화.
- Coder adapter와 External Web Tester adapter 등록.
- `review_task.next_executor` 및 tester → coder → tester 전이 검증.
- attempt budget, progress, trace와 최종 결과 rendering 갱신.

완료 조건: 승인된 혼합 Plan이 순서대로 실행되고, Web Tester failure가 Coder repair와
Web retest로 이어진 뒤에만 tester task를 완료할 수 있다.

### 단계 5: durable migration과 복구

- execution checkpoint 새 version과 v1 migration 추가.
- executor running/review pending interruption test 추가.
- resume 시 현재 configuration에서 executor availability 재검증.
- 완료 archive와 Loom GC root 동작 유지.

완료 조건: 기존 Coder checkpoint를 복구할 수 있고, Web Tester 결과가 저장된
`review_pending`은 외부 호출을 반복하지 않는다.

### 단계 6: 현재 동작 문서와 architecture 동기화

- `README.md`의 `/agent:*`, `q agents`, `/plan` 설명 갱신.
- `agent-invocation-runtime.md`의 conditional discovery와 Web Tester lifecycle 갱신.
- `plan-orchestration.md`, `execution-orchestration.md`의 executor/review/recovery 계약 갱신.
- `architecture.drawio`와 `architecture.svg`를 함께 갱신하고 source/render 일치를 확인.
- 이 문서에 실제 구현 상태, 실행한 검증과 남은 외부 검증을 기록.

## 14. 예상 코드 변경 지점

| 영역 | 주요 위치 | 계획된 변경 |
| --- | --- | --- |
| Config | `config/config.go` | 외부 역할 상수, 공통 validation/discovery 기반 |
| Agents UI | `app/agents.go` | Web Tester 역할 할당 |
| Command discovery | `app/slash_commands.go`, `app/acp.go` | 조건부 catalog/help/handler |
| Invocation runtime wiring | `app/agent_invocation.go` | Default-only Web Tester 등록 |
| ACP adapter | `app/acp_search.go`, 신규 `app/acp_web_tester.go`, `app/acp_client.go` | 공통 lifecycle, 자동 permission, result 변환 |
| Invocation contract | 신규 `subagent/external_web_tester.go` | tool input/result/schema/validation |
| Plan contract | `subagent/planning.go`, `subagent/planning_contract.go` | executor schema, parser, prompt, rendering |
| Execution contract | `subagent/execution.go`, `subagent/execute_loop.go`, `subagent/coder.go` | common executor/result/review routing |
| Plan composition | `app/plan.go` | capability snapshot과 executor adapter 구성 |
| Persistence | `workspace/execution.go`, `workspace/execution_history.go` | version migration과 archive 호환 |
| TUI/ACP presentation | `app/plan.go`, `app/acp_trace.go` 및 관련 renderer | executor progress와 recovery 문구 |

구현 중 실제 책임이 더 좁은 파일로 이동할 수 있지만, 표의 사용자-visible 계약과
durability 경계는 유지한다.

## 15. 검증 계획

### Unit 및 contract

- 외부 capability availability truth table.
- Search/Web Tester 외부 역할 config validation과 persistence.
- Web Tester input/result의 strict parsing, 크기 제한과 outcome validation.
- Plan executor의 dynamic schema, parser와 legacy default.
- executor dispatch, retry routing, attempt 상한과 invalid transition.
- checkpoint v1 migration과 phase/result validation.
- 자동 permission이 `allow_once`, fallback `allow_always`, 허용 option 없음의 각
  결과를 결정하고 사용자 질문을 만들지 않는지 검증.

### Integration

- fake ACP connection으로 initialize, prompt, permission, delete/close 전체 lifecycle 검증.
- Web Tester result가 Loom에 저장되고 bounded receipt가 parent/reviewer에 전달되는지 검증.
- TUI command catalog와 ACP `AvailableCommands`가 같은 availability 결과를 사용하는지 검증.
- Default에는 tool이 있고 Griller/Planner/Coder/Scout/Advisor에는 없는 role matrix 검증.
- 실행 중 connection unavailable, cancellation, malformed response와 cleanup failure 검증.
- active checkpoint를 저장하고 store를 다시 열어 resume하는 검증.

### End-to-end와 smoke

- built Q에서 `q agents`로 Web Tester 역할을 할당한 뒤 slash completion에
  `/agent:web-tester`가 나타나는 smoke.
- 직접 명령 한 번이 실제 ACP child, Loom receipt와 최종 parent 응답까지 완료되는 smoke.
- `/plan`에서 Coder task 뒤 Web Tester task가 실행되고 Planner review를 통과하는 smoke.
- 설정을 제거하거나 disable한 새 session에서 도구, command와 Planner executor가 모두
  사라지는 end-to-end 검증.
- 실제 browser-capable ACP agent 검증은 명시적인 환경 변수와 synthetic test site가 있을
  때만 실행하고, 없으면 prerequisite와 검증되지 않은 경계를 명확히 표시하며 skip한다.

기본 회귀 명령은 최소 다음 package를 포함한다.

```text
go test ./config ./subagent ./app ./workspace
```

구현 완료 전 전체 repository test와 실제 CLI smoke를 추가로 실행한다. 외부 ACP/browser
통합이 skip됐다면 fake ACP 테스트 통과를 실제 브라우저 interoperability의 증거로
표현하지 않는다.

## 16. Acceptance criteria

1. `q agents`에서 `external_web_tester`에 enabled ACP connection을 할당할 수 있다.
2. unassigned, missing 또는 disabled connection이면 `external_web_tester`가 일반 채팅
   tools, slash completion/help, ACP commands 및 Planner schema 어디에도 나타나지 않는다.
3. 기존 `/agent:search`도 동일한 conditional command discovery를 따른다.
4. available 상태에서는 일반 채팅과 `/agent:web-tester`가 동일한 자동 ACP invocation을
   실행하고 결과를 Loom receipt로 반환한다.
5. Web Tester 도구는 Griller, Planner, Coder, Scout, Advisor 또는 Web Tester child에
   노출되지 않는다.
6. Planner는 available일 때만 `external_web_tester` executor task를 제안할 수 있고,
   승인 화면은 executor를 표시한다.
7. 승인된 mixed-executor Plan이 task 순서를 보존하며 Coder와 Web Tester를 dispatch한다.
8. Web Tester failure는 Planner가 Coder repair로 전환하고 Web Tester를 다시 실행할 수
   있으며, 성공한 Web Tester attempt 없이 tester task를 완료할 수 없다.
9. 실행 중단과 resume이 unknown external outcome을 성공으로 추정하거나
   `review_pending` invocation을 중복 실행하지 않는다.
10. 기존 executor 필드 없는 Plan과 Coder checkpoint를 새 binary가 Coder 실행으로
    복구한다.
11. 실행 중 permission 질문이나 새 permission 설정 없이 Web Tester가 자동으로
    동작한다.
12. 현재 동작 문서, README와 architecture source/render가 구현 결과와 일치한다.

## 17. 주요 위험과 대응

- **정적 command catalog의 drift**: TUI completion, help와 ACP command 목록을 같은
  configuration-aware builder에서 파생한다.
- **tester failure 후 무의미한 재검사 반복**: Planner review가 `next_executor`로
  Coder repair를 선택하고 다시 tester로 돌아올 수 있게 한다.
- **외부 호출 중단의 불명확한 결과**: invocation 전 running checkpoint를 저장하고
  resume에서 상태 재확인을 요구하는 새 attempt를 만든다.
- **구성 변경에 따른 묵시적 fallback**: unavailable executor를 Coder로 대체하지 않고
  checkpoint를 보존한 actionable failure로 처리한다.
- **자동 ACP 실행의 신뢰 범위**: Web Tester 역할 할당을 명시적 trust decision으로
  취급하고 다른 ACP 경로의 permission policy는 변경하지 않는다.
- **대형 또는 민감한 웹 결과**: bounded result와 Loom reference를 사용하고 credential,
  cookie, 환경 변수와 원문 page payload를 일반 trace/checkpoint에서 제외한다.
- **지속 형식 변경**: 새 execution version과 실제 v1 fixture migration test를 먼저
  제공한다.

## 18. 완료 기록

2026-09-11에 milestone을 구현했다.

- `config.Config.ExternalAgentConnection`을 Search와 Web Tester의 공통 availability
  판정으로 추가했다. `q agents`의 외부 역할 행은 항상 보이지만 enabled connection이
  없는 역할은 model tool, slash completion/help, ACP command와 Planner executor
  schema에서 제거된다. 같은 이름의 base tool도 이 경계를 우회하지 못한다.
- Web Tester는 Default 일반 채팅과 `/agent:web-tester`에만 model-visible invocation으로
  연결했다. Griller, Scout, Planner, Coder, Advisor 또는 Web Tester child에는 도구를
  제공하지 않는다. Planner는 `/plan` proposal/review schema에서 executor ID만 조건부로
  받는다.
- ACP invocation은 15분 deadline, `allow_once` 우선·`allow_always` fallback의 자동
  permission, fail-closed no-allow 처리, strict bounded JSON과 한 번의 형식 교정을
  적용한다. 결과 원문은 Loom에 capture한다.
- execution loop를 공통 executor/result/attempt 계약으로 일반화했다. planned executor를
  acceptance executor로 유지하고, tester failed → coder repair → tester retest와 shared
  attempt limit을 지원한다. unavailable executor는 fallback하지 않고 durable checkpoint를
  유지한다.
- execution checkpoint를 version 2로 올리고 v1의 executor 없는 plan,
  `coder_pending`/`coder_running`, pending/completed Coder result를 `coder` executor로
  migration한다.
- README, agent invocation/Plan/execution 문서와 `architecture.drawio` 및 렌더된
  `architecture.svg`를 실제 동작에 맞췄다. 계획의 “공통 discovery builder”는 하나의
  availability predicate를 모든 표면이 공유하는 형태로 구현했고, TUI와 ACP의 표현용
  catalog 자체는 각 protocol renderer에 남겼다.

검증 결과:

- `go test ./config ./subagent ./workspace ./app -count=1`: 통과.
- `go test ./... -count=1`: 전체 repository 통과.
- `go vet ./...`: 통과.
- 실제 `cmd/q`를 임시 `q.exe`로 build하고 `q acp --help`를 실행: 통과. 임시 binary와
  directory는 검증 후 제거했다.
- fake ACP lifecycle 테스트에서 prompt, 자동 permission, 한 번의 correction,
  session delete/close와 Loom receipt 전달을 검증했다.
- `architecture.drawio`와 `architecture.svg`를 XML로 parse하고 핵심 label 일치를
  확인했으며 SVG 렌더를 시각 검수했다.
- `go test -race ./config ./subagent ./workspace ./app -count=1`은 이 실행 호스트인
  Windows/ARM64에서 Go race detector가 지원되지 않아 실행할 수 없었다.
- 실제 browser-capable ACP 통합은 `Q_TEST_ACP_WEB_TESTER`와 `Q_TEST_ACP_PRESET`이
  설정되지 않아 opt-in 테스트에서 skip됐다. 따라서 fake ACP 통과를 실제 브라우저
  interoperability의 증거로 간주하지 않는다.

rollback은 외부 역할 상수/invocation과 executor adapter를 제거하고 checkpoint writer를
v1로 되돌리는 것만으로는 안전하지 않다. 이미 생성된 v2 checkpoint를 구 binary가 읽지
못하므로, 배포 rollback 전 active v2 실행을 완료·보관하거나 별도 down-migration해야
한다. 후속 검증은 browser-capable ACP가 준비된 환경에서 opt-in integration과 실제
`/agent:web-tester`, mixed `/plan` smoke를 실행하는 것이다.
