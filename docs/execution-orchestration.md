# Execution orchestration contract

## 목적

승인된 Plan은 단순한 설명문이나 데이터플로 그래프가 아니다. 전체 목표, 제약,
현재까지 확인된 사실과 순서가 있는 Coder task 목록을 보존하는 실행 문서다.

각 task는 자신이 수정할 파일 집합을 고르는 target condition을 가진다. Coder가 한
번 실행된 뒤에는 Planner가 결과를 검토해 같은 task를 다시 실행할지 다음 task로
넘어갈지만 결정한다.

## Plan과 task

```text
Plan
 ├─ summary / conditions / assumptions / non-goals
 ├─ facts
 ├─ tasks[]
 │    ├─ title / description
 │    ├─ target condition
 │    └─ verification
 ├─ overall verification
 └─ risks
```

`facts`는 승인 시점의 확인된 사실과 실행 중 Planner가 새로 인정한 사실을 함께
가진다. 다음 Coder 호출의 system prompt에는 일부 요약이 아니라 현재 Plan 전체가
포함된다.

## Target condition

Target condition은 한 task가 다룰 파일 집합만 계산한다. 조건 간 실행 순서,
이전 task 결과의 전달, Coder 결과 판정에는 사용하지 않는다.

표현은 OR-of-AND 정규형이다.

```json
{
  "any": [
    {
      "all": [
        {
          "kind": "loom",
          "code": "return [\"app/model.go\"];",
          "inputs": {
            "tree": "loom://0123456789abcdef0123456789abcdef"
          }
        },
        {
          "kind": "paths",
          "paths": ["app/model.go", "app/plan.go"]
        }
      ]
    },
    {
      "all": [
        {
          "kind": "paths",
          "paths": ["README.md"]
        }
      ]
    }
  ]
}
```

- 바깥 `any`: 각 product 결과의 합집합, 즉 OR
- 안쪽 `all`: selector 결과의 교집합, 즉 AND
- `paths`: 명시적인 workspace-relative 파일 집합
- `loom`: 입력 artifact에 JavaScript transform을 적용해 얻는
  workspace-relative 파일 배열

이 정규형은 `(A AND B) OR C`를 순서나 중간 상태 없이 표현한다. Loom selector도
다른 selector와 동일한 하나의 파일 집합일 뿐이다.

## 실행 loop

```text
현재 task의 target condition 평가
                │
                ▼
현재 Plan 전체 + resolved targets + retry feedback
                │
                ▼
             Coder 실행
                │
                ▼
          Planner review_task
          ┌─────┴─────┐
        retry        next
          │            │
   같은 task 재실행    다음 task 또는 전체 완료
```

Task는 배열 순서대로 실행하지만 target condition 자체에는 순차 의미가 없다.
결과 조건식도 없다. Coder 결과의 수용 여부는 Planner의 review가 판단한다.

## Planner review

Planner는 매 Coder attempt 뒤 다음 구조를 반환한다.

```json
{
  "decision": "retry",
  "feedback": "",
  "facts": []
}
```

- `decision`: `retry` 또는 `next`만 허용한다.
- `feedback`: 항상 존재해야 한다. 추가 지시 없는 retry는 빈 문자열을 사용한다.
- `facts`: 이번 attempt에서 새로 확인되어 이후 실행에도 유효한 사실이다.

`retry`는 같은 task와 같은 target condition을 다시 실행한다. `feedback`이 비어 있지
않으면 다음 Coder system prompt에 그대로 추가한다. 별도
`retry_with_feedback` 상태는 없다.

`next`는 현재 task를 인정하고 다음 task로 이동한다. 마지막 task에서 `next`가
나오면 전체 실행이 끝난다.

Planner가 반환한 `facts`는 decision 적용 전에 Plan에 중복 제거하여 추가한다.
따라서 retry와 next 모두 다음 Coder invocation에서 갱신된 Plan을 보게 된다.

## 현재 구현 상태

현재 코드에는 다음 기반이 구현되어 있다.

- Plan task별 OR-of-AND target condition schema와 validation
- 정적 path selector와 실제 `loom_eval`/`loom_read`를 사용하는 Loom transform selector
- product 내부 파일 집합 교집합과 product 간 합집합 evaluator
- `review_task` 전용 Planner runner
- `decision: retry | next`
- 필수지만 빈 문자열을 허용하는 `feedback`
- review facts를 현재 Plan에 병합하는 처리
- 현재 Plan 전체를 포함하는 Coder system prompt builder
- task 순서, retry, next와 attempt 상한을 집행하는 execution loop

아직 연결하지 않은 부분은 다음과 같다.

- Coder model/tool loop와 `task_complete` 결과 수집
- 승인 후 실행 gate
- 중단과 재시작 persistence
- TUI의 task/attempt/review 상태 표시
