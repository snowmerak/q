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
- **Scout**: Griller가 위임한 저장소 질문을 읽기 전용으로 조사하는 subagent다.
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

Scout는 읽기 전용 도구만 사용한다. 초기 allowlist는 다음과 같다.

- `list_directory`
- `read_file`
- `loom_inspect`
- `loom_read`
- `loom_eval`

파일 변경, command 실행, 사용자 질문과 재귀 subagent 생성은 허용하지 않는다.
Scout는 반드시 `task_complete`를 단독 호출해 다음 구조를 반환한다.

- `outcome`: `succeeded` 또는 `blocked`
- `summary`
- path/symbol, evidence와 risks가 분리된 findings
- 후속 agent가 읽을 Loom artifacts
- 수행한 verification
- blocked인 경우 구체적인 blocker

Scout 결과는 먼저 Griller에게 돌아간다. Griller는 결과에 따라 사용자 질문, 추가
Scout 호출, Loom transform 또는 Planner handoff 중 다음 행동을 정한다.

## Planner와 확인 계약

Planner는 완성된 Grill brief를 받아 다음을 하나의 proposal로 만든다.

- 작업 조건과 확정된 결정
- 명시적 가정과 위험
- 범위와 비범위
- 순서가 있는 실행 계획
- 완료 기준과 검증 방법

사용자는 이 조합된 proposal 전체를 승인한다. 승인 전에는 coder나 변경 실행기로
전달하지 않는다.

사용자가 거절하거나 수정 사항을 주거나 Planner가 유효한 계획을 만들지 못하면
Planner만 반복하지 않는다. 기존 사용자 답변, Scout 결과, Loom reference, proposal과
실패 근거를 보존한 채 `GRILLING`으로 돌아가 다시 Grill한다. 이미 확정된 질문을
무조건 반복하지 않고 새로 생긴 정보 공백만 다룬다.

## 구현 상태

현재 `subagent.ScoutRunner`가 첫 실행 코어로 구현되어 있다.

- role별 model과 reasoning effort 적용
- Griller가 넘길 bounded `ScoutTask`
- 읽기 전용 tool allowlist
- 독립 model/tool loop
- 검증된 `task_complete` 결과
- plain-text 종료 reminder
- Session Store lifecycle 기록

아직 구현되지 않은 연결은 다음과 같다.

- `/plan` 명령과 상태 저장
- Griller runner 및 `delegate_scout` tool bridge
- Scout 결과를 Griller context로 되돌리는 반복 loop
- Planner runner와 사용자 confirmation TUI
- 실패 시 accumulated context를 사용한 re-grill
