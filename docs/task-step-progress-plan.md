# 일반 task의 ACP 스텝 진행 표시 구현 계획

작성일: 2026-09-13

상태: 구현 전 제안. 이 문서는 일반 채팅의 `task_start`/`task_complete` lifecycle에
가벼운 스텝 진행 표시를 추가하는 작업의 범위와 구현 순서를 소유한다.

대상 독자: Q의 기본 agent loop, workspace session, ACP plan projection을 변경하는
구현자와 리뷰어.

갱신 조건: orchestration tool 계약, `ActiveTask` 저장 형식, ACP TODO 투영 규칙,
일반 task와 `/plan`의 표시 우선순위 또는 검증 범위가 달라질 때 이 문서를 같은
변경에서 갱신한다. 구현이 끝나면 실제 동작과 검증 결과를 기록하고 상태를
구현 완료로 바꾼다.

## 1. 목표

일반 채팅에서 agent가 하나의 task를 시작할 때 그 task를 구성하는 여러 할 일을
ACP의 Plan/TODO 목록에 표시한다. agent가 현재 할 일을 끝낼 때마다 해당 항목을
`completed`로 바꾸고 다음 항목을 `in_progress`로 표시한다.

예를 들어 agent가 다음 스텝을 선언했다고 가정한다.

```text
1. 원인 확인
2. 코드 수정
3. 테스트 실행
```

ACP에는 처음에 다음 상태가 보인다.

```text
in_progress  원인 확인
pending      코드 수정
pending      테스트 실행
```

첫 checkpoint 뒤에는 전체 목록을 다시 투영한다.

```text
completed    원인 확인
in_progress  코드 수정
pending      테스트 실행
```

이 기능의 checkpoint는 agent가 사용자에게 진행 상황을 보여 주고, 중단된 일반
task를 다시 열었을 때 마지막으로 표시한 위치를 복원하기 위한 진행 표식이다. 임의의
workspace 도구 호출이나 외부 부작용에 대한 트랜잭션 경계는 아니다.

## 2. 범위

### 포함

- `task_start`에서 선택적인 순차 스텝 목록 선언.
- 활성 task의 현재 스텝을 완료하는 non-terminal `task_checkpoint` 도구.
- 한 시점에 최대 하나의 일반 `ActiveTask`만 존재하는 기존 불변식 유지.
- 각 checkpoint에서 완료 수를 단조 증가시키는 순차 상태 전이.
- ACP Plan update를 통한 전체 스텝 목록과 상태 표시.
- workspace session에 스텝과 진행 위치를 저장하고 session 복원 시 재투영.
- 스텝을 선언하지 않는 기존 `task_start` 호출과 저장된 세션의 호환.
- 일반 task와 명시적 `/plan` workflow가 같은 ACP Plan 표면을 사용할 때의 표시
  우선순위.
- tool parsing, 상태 전이, session round-trip, TUI host와 ACP projection 회귀 테스트.

### 포함하지 않음

- 일반 task를 승인된 Plan의 `ExecutionCheckpoint`로 변환하는 작업.
- 스텝별 executor, target resolution, Planner review, retry 또는 attempt budget.
- 스텝의 병렬 실행, 건너뛰기, 되돌리기 또는 완료된 스텝의 재개방.
- 실행 중 스텝 삽입, 삭제, 재정렬 또는 제목 변경.
- checkpoint 시점까지의 workspace 변경을 원자적으로 확정하거나 rollback하는 기능.
- 완료된 일반 task의 별도 실행 기록 파일과 장기 checkpoint archive.
- TUI에 별도의 다중 행 TODO 패널을 추가하는 작업. TUI는 간단한 현재 진행 상태만
  표시하고, 다중 행 목록의 주 소비자는 ACP다.

## 3. 현재 상태

일반 task의 도구 계약은 `app/orchestration.go`에 있고 `task_start`는 단일 objective와
completion criteria만 받는다. `task_complete`는 활성 lifecycle이 있을 때만 성공하고,
성공하면 model/tool loop를 종료한다.

활성 상태는 `workspace.ActiveTask` 포인터 하나로 `session.json`에 저장된다. 현재 저장
필드는 objective, completion criteria, started time뿐이다. TUI와 ACP는 같은
`streamAgentLoop`를 사용하지만 task 상태 이벤트를 각 host에서 받아 저장한다.

ACP는 일반 task를 objective 하나로 된 Plan entry로 표시한다. 반면 `/plan` 실행은
별도 `ExecutionCheckpoint`와 `PlanProposal.Steps`를 사용하며 전체 task 목록을 ACP에
투영한다. 일반 task의 스텝 표시는 이 ACP projection 모양만 재사용하고 Plan의 실행
상태나 저장 형식은 재사용하지 않는다.

## 4. 도구 계약

### `task_start`

기존 입력에 선택적인 `steps`를 추가한다.

```json
{
  "objective": "로그인 오류를 수정한다.",
  "completion_criteria": ["관련 테스트가 통과한다."],
  "steps": [
    "오류 재현과 원인 확인",
    "구현 수정",
    "테스트와 결과 확인"
  ]
}
```

규칙은 다음과 같다.

- `objective`는 계속 required다.
- `steps`는 optional이다. 생략하거나 빈 배열이면 기존 단일 objective 표시를 유지한다.
- 각 step은 trim 후 비어 있지 않아야 한다.
- step 순서는 선언 뒤 바뀌지 않는다.
- 항목 수와 문자열 크기에는 bounded schema/runtime 상한을 둔다.
- task 시작 시 완료된 step 수는 0이다.

스텝이 있으면 첫 항목을 `in_progress`, 나머지를 `pending`으로 투영한다. 스텝이
없으면 objective 하나를 `in_progress`로 내보내 현재 ACP 동작을 보존한다.

### `task_checkpoint`

새 도구는 현재 스텝 하나의 완료만 선언한다.

```json
{
  "summary": "재현 로그와 호출 경로를 확인해 세션 갱신 누락을 원인으로 좁혔다."
}
```

현재 index나 step ID를 입력받지 않는다. host가 활성 task의 다음 미완료 스텝을
권위 있게 결정한다. 이 형태는 모델이 잘못된 index를 보내거나 이미 완료한 항목을
다시 완료하는 경우를 없애고 상태 전이를 순차적으로 제한한다.

호출 규칙은 다음과 같다.

- 활성 `task_start` lifecycle이 있어야 한다.
- 활성 task가 하나 이상의 step을 가지고 있어야 한다.
- 아직 완료하지 않은 step이 있어야 한다.
- `summary`는 required이며 현재 step에서 실제로 끝낸 일을 짧게 설명한다.
- `task_checkpoint`는 그 turn의 유일한 tool call이어야 한다. 작업 도구와 같은
  assistant 응답에 미리 섞어 호출하지 않는다.
- 성공하면 `CompletedSteps`를 정확히 1 증가시키고 전체 ACP Plan projection을 다시
  전송한다.
- 마지막 step을 완료해도 agent loop는 종료하지 않는다. agent는 별도
  `task_complete`로 전체 lifecycle을 끝낸다.

### `task_complete`

기존 terminal 의미를 유지한다.

- `outcome: succeeded`는 선언된 step이 모두 checkpoint된 뒤에만 허용한다.
- step이 없는 legacy task는 기존과 같이 즉시 완료할 수 있다.
- `outcome: blocked`는 미완료 step이 있어도 허용하며 task를 terminal 상태로 끝낸다.
- 성공한 완료는 ACP의 모든 step을 `completed`로 마지막 한 번 투영한 뒤
  `ActiveTask`를 제거한다.
- blocked 완료는 완료된 step은 `completed`, 다음 미완료 step은 더 이상 실행 중이
  아님을 표현하도록 ACP projection을 정리한 뒤 lifecycle을 제거한다. ACP가
  `blocked` 상태를 지원하지 않으므로 남은 항목을 `pending`으로 유지하고 최종
  assistant 응답에서 blocker를 설명한다.

## 5. 상태 모델

일반 task 상태는 스텝별 중복 status를 저장하지 않고 목록과 완료 개수만 저장한다.

```go
type ActiveTask struct {
    Objective          string    `json:"objective"`
    CompletionCriteria []string  `json:"completion_criteria,omitempty"`
    Steps              []string  `json:"steps,omitempty"`
    CompletedSteps     int       `json:"completed_steps,omitempty"`
    StartedAt          time.Time `json:"started_at"`
}
```

ACP status는 다음과 같이 계산한다.

- `index < CompletedSteps`: `completed`.
- `index == CompletedSteps`이고 아직 미완료 항목이 있음: `in_progress`.
- `index > CompletedSteps`: `pending`.

이 표현은 `CurrentStep`과 각 step의 status를 동시에 저장해서 생기는 불일치를 피한다.
validator는 `0 <= CompletedSteps <= len(Steps)`를 보장한다. `Steps` slice는
`cloneSession`과 app의 `cloneActiveTask`에서 deep copy한다.

상태 전이는 다음으로 제한한다.

```text
inactive
  └─ task_start
       └─ active(completed=0)
            ├─ task_checkpoint → active(completed=n+1)
            ├─ task_complete succeeded → inactive, 단 모든 step 완료 필요
            └─ task_complete blocked   → inactive
```

두 번째 `task_start` 거절, active task 없는 `task_complete` 거절과 plain assistant
응답을 통한 암묵적 종료 금지는 기존 규칙을 유지한다.

## 6. 상태 저장과 복원

`Steps`와 `CompletedSteps`는 기존 `session.json`의 `active_task` 안에 저장한다. 별도
checkpoint 파일을 만들지 않는다. 일반 task는 현재 대화 lifecycle에 속하고,
`session.json`이 이미 활성 task의 source of truth이기 때문이다.

기존 세션에는 새 필드가 없으므로 새 binary에서는 빈 steps와 완료 수 0으로 읽는다.
현재 session decoder가 unknown field를 거절하므로 새 세션을 구 binary로 여는
downgrade 호환성은 제공되지 않는다. 이 milestone에서는 별도 migration 파일을
도입하지 않고 additive forward compatibility만 제공한다.

session 복원 prompt에는 다음 내용을 포함한다.

- 전체 objective와 completion criteria.
- 전체 step 목록.
- 완료된 step 수.
- 현재 실행할 step 또는 모든 step이 완료됐다는 상태.
- 이미 완료된 step을 반복하지 말고 현재 step부터 계속하라는 지시.

checkpoint 저장은 기존 `ActiveTask` 이벤트와 workspace session 저장 경계를
따른다. 이 기능은 표시용 진행 상태이므로 Plan executor의 pre-effect durability나
ambiguous side-effect recovery를 약속하지 않는다. 저장 오류의 처리는 기존 task
lifecycle 오류 정책을 유지하며, 더 강한 acknowledgment 계약은 별도 milestone로
분리한다.

## 7. ACP projection

일반 task에서 ACP로 보내는 projection builder를 하나 둔다. 시작, checkpoint,
완료, session replay가 모두 이 builder를 사용한다.

```text
ActiveTask
  → taskPlanUpdate(active | succeeded | blocked)
  → agentPlanUpdate
  → ACP UpdatePlan(entries...)
```

ACP `UpdatePlan`은 항목 하나의 patch가 아니라 현재 전체 목록을 다시 보낸다. 따라서
checkpoint마다 모든 step과 계산된 status를 내보낸다. 기존 `/plan`의
`planUpdateFromCheckpoint`와 wire projection 타입은 공유할 수 있지만,
`PlanProposal`이나 `ExecutionCheckpoint`를 일반 task의 authority로 사용하지 않는다.

스텝이 없는 task는 objective 하나를 현재 방식대로 표시한다. 이 fallback은 기존
모델 호출, 저장된 active task와 작은 직접 작업의 UI를 보존한다.

TUI는 다중 행 Plan을 새로 렌더링하지 않는다. checkpoint 이벤트를 받을 때
`Task step 2/3 · 구현 수정`처럼 현재 진행을 status line에 표시하는 정도만 포함한다.

## 8. `/plan` 및 다른 ACP workflow와의 우선순위

ACP에는 한 시점의 Plan projection이 하나뿐이지만 일반 active task와 사용자가
명시적으로 시작한 `/plan`, `/debug`, `/review` 같은 workflow는 현재 같은 session에서
연속해서 실행될 수 있다.

표시 우선순위는 다음으로 정한다.

1. 실행 중인 명시적 workflow가 자신의 Plan projection을 소유한다.
2. workflow가 끝났을 때 일반 `ActiveTask`가 남아 있으면 그 스텝 projection을 다시
   내보낸다.
3. 다음 `task_checkpoint` 또는 session replay도 항상 저장된 일반 task projection
   전체를 다시 내보낸다.

이 정책은 일반 task 때문에 `/plan` 진입을 새로 금지하지 않으면서도 workflow가
끝난 뒤 ACP 화면에 오래된 Plan 목록이 남는 것을 방지한다.

## 9. 구현 단계

### 단계 1: 계약과 순수 상태 전이

- `task_start.steps`와 `task_checkpoint` 입력/schema/parser를 추가한다.
- step 정규화, 상한, 빈 항목과 unknown field 검증을 추가한다.
- `ActiveTask`에 `Steps`와 `CompletedSteps`를 추가한다.
- 현재 step 완료와 ACP status 계산을 순수 helper로 구현한다.

### 단계 2: 기본 agent loop 연결

- orchestration tool 목록에 `task_checkpoint`를 등록한다.
- 활성 task, 남은 step, 단독 tool call 조건을 검증한다.
- 성공 시 local `activeTask`를 갱신하고 host에 progress event를 보낸다.
- `task_complete succeeded`에서 모든 step 완료 조건을 검사한다.
- completion reminder와 resume instruction에 step 진행 상태를 반영한다.

### 단계 3: session persistence와 TUI host

- workspace session round-trip과 deep clone을 확장한다.
- task progress event를 받을 때 `m.activeTask`를 교체하고 session을 저장한다.
- TUI status line에 현재 step과 전체 개수를 표시한다.
- interrupt, `/clear`, `/new`, session 전환의 기존 정리 의미를 유지한다.

### 단계 4: ACP projection

- 일반 task의 전체 ACP Plan projection builder를 추가한다.
- task start, checkpoint, terminal completion과 session replay에 연결한다.
- 명시적 workflow 종료 후 남아 있는 일반 task projection을 복원한다.
- 기존 plan execution의 `planUpdateFromCheckpoint`와 상태 전이를 변경하지 않는다.

### 단계 5: 문서와 회귀 검증

- 구현된 tool 계약을 orchestration 문서와 필요 시 README에 반영한다.
- 아래 단위/통합 테스트를 추가하고 전체 Go 검증을 수행한다.
- 실제 구현이 이 계획과 달라졌다면 이 문서의 상태, 결정과 남은 제한을 갱신한다.

## 10. 테스트 계획

### 도구와 상태 단위 테스트

- `task_start`가 유효한 ordered steps를 파싱한다.
- omitted/empty steps가 기존 단일 objective 동작을 보존한다.
- 빈 step, 상한 초과와 unknown field를 거절한다.
- active task 없는 `task_checkpoint`를 거절한다.
- step 없는 task와 모든 step이 이미 완료된 task의 checkpoint를 거절한다.
- checkpoint가 현재 step만 완료하고 완료 수를 정확히 1 증가시킨다.
- checkpoint와 다른 tool call을 같은 turn에 보낸 경우 상태를 변경하지 않는다.
- 미완료 step이 있는 `task_complete succeeded`를 거절한다.
- `task_complete blocked`는 미완료 step이 있어도 terminal 상태가 된다.

### session 테스트

- steps와 완료 수가 `session.json` round-trip에서 유지된다.
- 기존 steps 없는 session을 읽을 수 있다.
- clone 뒤 원본과 복사본의 steps slice가 서로 영향을 주지 않는다.
- 복원된 task의 developer instruction이 현재 step을 정확히 가리킨다.

### ACP 및 host 통합 테스트

- 세 step 시작 시 `in_progress, pending, pending` 목록을 받는다.
- 첫 checkpoint 뒤 `completed, in_progress, pending` 목록을 받는다.
- 마지막 checkpoint 뒤 모든 항목이 `completed`다.
- session replay가 저장된 완료 수와 동일한 목록을 다시 보낸다.
- 스텝 없는 task는 objective 한 항목으로 표시된다.
- `/plan` workflow가 끝난 뒤 남아 있는 일반 task 목록이 복원된다.
- TUI와 ACP가 checkpoint 이후 같은 `ActiveTask` 상태를 저장한다.

### 전체 검증

```text
go test ./app ./workspace ./subagent
go test ./...
go vet ./...
go build ./...
```

외부 ACP process는 필요하지 않다. 기존 fake ACP connection으로 Plan update의 전체
entry 목록과 status 전이를 검증한다.

## 11. 완료 기준

- 일반 agent가 하나의 `task_start`에서 여러 step을 선언할 수 있다.
- ACP가 선언 직후 전체 step 목록과 정확한 초기 status를 표시한다.
- 각 성공한 `task_checkpoint` 뒤 정확히 한 step만 완료되고 다음 step이 실행 중으로
  표시된다.
- session을 다시 열어도 저장된 진행 위치가 동일하게 복원된다.
- 모든 step 완료 전에는 succeeded `task_complete`가 허용되지 않는다.
- blocked completion, steps 없는 legacy task와 기존 단일 active task 불변식이
  유지된다.
- 일반 task는 Plan executor, Planner review, retry 또는 별도 plan checkpoint 파일에
  의존하지 않는다.
- 기존 `/plan` 실행과 복구 테스트가 변경 없이 통과한다.

## 12. 예상 변경 범위

핵심 구현은 다음 파일에 집중한다.

- `app/orchestration.go`, `app/orchestration_test.go`
- `app/model.go`, `app/model_test.go`, `app/terminal_turn_test.go`
- `app/acp.go`, `app/acp_test.go`
- `workspace/session.go`, `workspace/session_test.go`
- 관련 README 및 orchestration 문서

예상 규모는 테스트와 문서 갱신을 포함해 약 7~10개 구현/테스트 파일,
200~400줄이다. `subagent/execute_loop.go`, `workspace/execution.go`와 plan checkpoint
저장 형식은 변경하지 않는 것을 기본 조건으로 한다.
