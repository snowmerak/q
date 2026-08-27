# `/plan` orchestration contract

## 목적

`/plan`은 즉시 단계 목록을 생성하는 명령이 아니다. 사용자의 의도와 저장소의
실제 상태를 충분히 확인한 뒤, 실행 조건과 완료 기준을 포함한 계획을 사용자에게
승인받는 명시적 orchestration mode다.

이 문서는 `/plan`, Griller, Scout, Planner의 책임과 재시도 경계를 정의한다.

## 용어

- **Griller**: 사용자 요청의 빈칸을 찾고 질문, 저장소 조사, 가정 검증을 조율하는
  subagent 역할이다.
- **Grill**: 누적된 사용자 답변, 기존 결정, Loom 자료와 Scout 결과를 조합해 다음
  질문이나 다음 agent의 bounded context를 만드는 내부 과정이다.
- **Scout**: Griller가 위임한 저장소 질문을 workspace 비변경 방식으로 조사하는
  subagent다.
- **Planner**: Grill이 완성한 brief를 조건, 완료 기준, 검증 방법과 실행 단계로
  변환하는 역할이다.

Grill은 Scout보다 먼저 끝나는 별도 전처리 단계가 아니다. Griller는 Grill 도중
저장소 근거가 필요할 때 Scout를 호출하고, 결과를 자기 컨텍스트에 합친 뒤 다시
질문하거나 추가 조사를 위임할 수 있다.

## 상태 머신

```text
/plan
  │
  ▼
GRILLING ──────────────────────────────────────────────┐
  │                                                    │
  ├─ 사용자만 알 수 있는 정보 ── ask_to_user ────────┤
  │                                                    │
  ├─ 저장소에서 확인할 정보 ─── delegate_scout ─┐     │
  │                                             │     │
  │                          Scout report ──────┘     │
  │                                                    │
  ├─ 큰 도구 결과 ── Loom 저장/평가/선별 ────────────┤
  │                                                    │
  └─ critical unknown 없음 + planning brief 완성       │
                    │                                  │
                    ▼                                  │
                 PLANNING                              │
                    │                                  │
       조건 + 가정 + 완료 기준 + 계획 + 검증 방법      │
                    │                                  │
                    ▼                                  │
             AWAITING_CONFIRMATION                     │
              │                  │                     │
           승인                  거절/수정/실패 ────────┘
              │                         이전 컨텍스트와
              ▼                         실패 근거를 보존
           APPROVED
```

`/plan`이 아닌 일반 대화나 직접적인 수정 요청은 이 상태 머신을 자동으로 시작하지
않는다. `/plan`으로 들어온 실행은 사용자 승인 전까지 변경 단계로 넘어가지 않는다.

## Griller 계약

Griller는 다음 정보를 누적한다.

- 사용자의 원래 목표와 이후 답변
- 명시된 범위, 금지 사항과 선호
- 확인된 사실과 아직 검증되지 않은 가정
- 성공 조건, 실패 조건과 검증 요구
- Scout의 구조화된 조사 결과와 근거
- 관련 Loom reference와 그 transform 결과
- 이전 Planner 실패 또는 사용자 수정 요청

Griller는 질문의 답이 저장소에 있을 가능성이 높으면 사용자에게 묻기 전에 Scout를
사용한다. 반대로 제품 의도, 우선순위, 허용할 trade-off처럼 사용자만 결정할 수 있는
사항은 `ask_to_user`로 확인한다.

Grill은 다음 조건이 모두 충족될 때만 Planner로 handoff한다.

1. 계획을 크게 바꿀 수 있는 critical unknown이 남지 않았다.
2. 사실과 가정이 구분되어 있다.
3. 범위와 비범위가 설명 가능하다.
4. 완료 기준과 검증 기대가 준비되어 있다.
5. Planner가 전체 대화나 원시 도구 결과를 다시 읽지 않아도 되는 bounded brief가 있다.

## Scout 계약

Scout는 Griller가 호출한다. Planner를 위해 직접 계획을 쓰거나 사용자에게 질문하지
않는다. 하나의 호출은 하나의 제한된 저장소 조사 질문을 가진다.

입력은 다음을 포함할 수 있다.

- 조사 objective와 완료 조건
- 현재까지 확인된 context
- 선택적 candidate file 목록
- 선택적 Loom input reference
- 상위 Grill/task 식별자

candidate file과 Loom input은 선택적 lead다. Griller가 관련 파일을 아직 모르는 경우
Scout의 목적 자체가 관련 파일과 심볼을 찾는 것이 될 수 있다.

Scout는 workspace 비변경 조사 도구만 사용한다. 현재 allowlist는 다음과 같다.

- `list_directory`
- `read_file`
- `loom_inspect`, `loom_read`, `loom_eval`
- `search_skills`, `get_skill`
- `run_command`, `wait`

`run_command`는 OS, architecture, shell, tool version, environment와 project metadata처럼
계획에 필요한 사실을 수집할 때만 사용한다. 파일 생성·변경·삭제, dependency
설치·갱신, format/generate, build/test, project script 실행과 Git 상태 변경은 prompt
계약으로 금지한다. 비동기 명령은 `cmd_status` 없이 `wait`로만 완료를 추적한다.
사용자 질문과 재귀 subagent 생성도 허용하지 않는다.
Scout는 반드시 `task_complete`를 단독 호출해 다음 구조를 반환한다.

- `outcome`: `succeeded` 또는 `blocked`
- `summary`
- path/symbol, evidence와 risks가 분리된 findings
- 후속 agent가 읽을 Loom artifacts
- 수행한 verification
- blocked인 경우 구체적인 blocker

Scout 결과는 먼저 Griller에게 돌아간다. Griller는 결과에 따라 사용자 질문, 추가
Scout 호출, Loom transform 또는 Planner handoff 중 다음 행동을 정한다.

Scout의 bounded `summary`, `findings`, `evidence`, `risks`는 `delegate_scout`의
구조화 JSON 결과로 Loom에 capture한 뒤 Griller context에 tool receipt로 전달한다.
작은 결과는 receipt의 `result`에 구조화 보고서 전체와 `loom_ref`가 함께 들어가며,
큰 결과는 bounded preview와 `loom_ref`를 반환한다. 따라서 보고서는 reference
하나로만 치환되지 않으면서도 모든 model-visible subagent 호출 결과가 동일한
capture 경계를 지킨다. 자세한 계약은
[agent-invocation-runtime.md](agent-invocation-runtime.md)를 따른다.

## Planner와 확인 계약

Planner는 완성된 Grill brief를 받아 다음을 하나의 proposal로 만든다.

- 작업 조건과 확정된 결정
- 명시적 가정과 위험
- 범위와 비범위
- 순서가 있는 실행 계획
- 각 task의 Loom/static path 기반 target condition
- 완료 기준과 검증 방법

사용자는 이 조합된 proposal 전체를 승인한다. 승인 전에는 coder나 변경 실행기로
전달하지 않는다.

승인된 Plan의 task 실행과 Planner review 의미는
[execution-orchestration.md](execution-orchestration.md)를 따른다. Target condition은
task의 파일 집합만 고르며 순차 조건이나 결과 조건으로 사용하지 않는다.

### Planner 제출 형식과 오류 수정

Planner의 system prompt에는 실제 validator와 schema 테스트를 통과하는
`succeeded`/`blocked` JSON 예제를 함께 제공한다. 일반적인 task는
`target.any[0].all[0]`에 `kind: paths`와 파일 목록 하나만 사용하면 된다.
아직 생성하지 않은 파일도 workspace-relative 경로로 지정할 수 있다.
Loom selector와 여러 OR/AND group은 필요한 경우에만 사용하며, Loom reference를
임의로 만들지 않는다.

`submit_plan`의 기본 object shape는 두 outcome에서 동일하다.

- `outcome`, `summary`, `conditions`, `steps`, `verification`, `blocker`는 schema의
  required field다. 저장된 이전 proposal에서 사용하지 않는 필드가 생략된 경우는
  parser가 계속 허용한다.
- `succeeded`에서는 conditions, steps, **전체 verification**이 각각 하나 이상
  필요하다. task별 verification은 전체 verification을 대신하지 않는다.
  blocker는 빈 문자열을 보낸다.
- `blocked`에서는 구체적인 blocker가 필요하며 conditions, steps, verification은
  빈 배열을 보낸다. 형식을 채우려고 실행 계획을 지어내지 않는다.
- outcome별 non-empty 조건은 field description과 prompt에 명시하고 runtime에서
  검사한다. 최상위 schema에 outcome union을 추가하지 않는다.
- selector는 종류별 schema로 나눈다. `paths`는 비어 있지 않은 paths만,
  `loom`은 code와 하나 이상의 유효한 Loom input만 받는다. 두 종류의 필드를
  혼합하지 않는다. `any`와 `all`은 각각 1~16개다.
- workspace 경로 안전성, UTF-8 byte 기준 code 크기 등 의미적 제약은 설명과
  runtime validation으로 계속 보장한다.

잘 구성된 JSON에서 독립적인 field/step/selector 검증 오류가 여러 개 발견되면
한 번의 tool error에 모아 전달한다. 각 오류에는 0-based 경로
(`steps[1].target.any[0].all[0].paths[0]` 등)를 붙인다. Planner는 모든 오류를
수정한 **전체 proposal**을 다시 제출한다. JSON 문법/type 오류나 알 수 없는
필드는 여전히 strict decoding 단계에서 거절한다.

이 개선은 공통 PlannerRunner를 사용하는 TUI와 ACP 양쪽에 적용되며,
계획/실행 기록의 저장 형식이나 Grill 과정의 저장 정책을 바꾸지 않는다.

사용자가 거절하거나 수정 사항을 주거나 Planner가 유효한 계획을 만들지 못하면
Planner만 반복하지 않는다. 기존 사용자 답변, Scout 결과, Loom reference, proposal과
실패 근거를 보존한 채 `GRILLING`으로 돌아가 다시 Grill한다. 이미 확정된 질문을
무조건 반복하지 않고 새로 생긴 정보 공백만 다룬다.

## 구현 상태

현재 `/plan`은 한 번의 승인 흐름을 실제로 실행할 수 있다. 채팅에서 `/plan`을
입력한 뒤 다음 메시지로 요청을 주거나 `/plan <request>`로 바로 시작한다.

- Griller의 반복 tool loop와 `ask_to_user`
- Griller가 호출하는 `delegate_scout`
- role별 model과 reasoning effort 적용
- Griller가 넘길 bounded `ScoutTask`
- workspace 비변경 조사 tool allowlist와 정보수집 전용 `run_command`/`wait`
- Scout의 독립 model/tool loop와 inline structured report
- 검증된 `task_complete` 결과
- plain-text 종료 reminder
- Session Store lifecycle 기록
- Planner의 `submit_plan` validation
- task별 OR-of-AND target condition validation
- 조건과 계획을 조합한 사용자 confirmation
- 거절 또는 Planner `blocked` 시 이전 brief/proposal/feedback을 보존한 re-grill
- Griller, Scout, Planner의 구조화된 progress event와 채팅 TUI activity panel
- role별 현재 상태와 최근 `started/thinking/tool/delegated/waiting/completed/failed` 로그
- 승인 직후 Loom/static target을 해석하고 Coder/Planner review execution loop 실행
- Coder tool call의 Loom/path evidence를 자동 수집하고 Planner가 선택적으로 검토
- task별 `retry | next`, feedback, facts 병합과 attempt 상한
- Coder/Planner의 상세 trace와 완료 후 접힌 summary
- 승인된 실행의 단계별 durable checkpoint와 재시작 recovery UI

후속 구현 범위는 다음과 같다.

- 승인 이전 Griller/Planner planning state의 재시작 복구
- Griller와 Planner 내부 lifecycle archive
- 전체 activity history를 탐색하는 `/agents` 상세 화면
- Scout 시작 전 workspace baseline/fingerprint를 기록하고 command 실행 뒤 mutation을
  감지하는 harness guard
- Scout mutation이 감지되면 결과를 Griller에 전달하지 않고 비변경 재조사를
  요구하는 nudge/retry 경로
- 기존 사용자 변경과 동시 변경을 보존하면서 Scout 소유 변경만 식별하는 mutation
  journal 및 그 소유권이 확실할 때만 수행하는 안전한 rollback
- workspace 밖의 tool cache, 전역 파일과 외부 부작용은 rollback할 수 없다는 경계의
  명시와 별도 격리 전략
