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
     자동 evidence 수집 + Planner review
          ┌─────┴─────┐
        retry        next
          │            │
   같은 task 재실행    다음 task 또는 전체 완료
```

Task는 배열 순서대로 실행하지만 target condition 자체에는 순차 의미가 없다.
결과 조건식도 없다. Coder 결과의 수용 여부는 Planner의 review가 판단한다.

## Coder evidence와 review 도구

Coder가 호출한 non-Loom 도구 결과는 기존 runtime 경계에서 Loom artifact로 저장된다.
각 Coder attempt는 이 receipt와 도구 호출 인자에서 다음의 제한된 evidence만 자동으로
수집한다.

- 도구 이름
- 결과의 `loom_ref`
- 오류 여부
- 파일 접근 도구에 포함된 workspace-relative path

원시 명령 출력과 전체 Coder transcript는 review 요청에 복제하지 않는다. Coder가
`task_complete` 인자로 evidence를 직접 작성할 수도 없다. Planner는 필요한 근거만
Loom에서 선택적으로 읽는다.

Planner review에는 다음 도구만 제공한다.

- `read_file`
- `loom_inspect`, `loom_read`, `loom_eval`
- 검증 명령을 위한 `run_command`, `wait`
- 활성 Search ACP 연결이 설정된 경우 외부 사실 확인을 위한 `external_search`
- 최종 전이 결정을 위한 `review_task`

Planner는 Git 명령이나 workspace를 변경하는 명령을 실행하지 않도록 지시받으며,
`edit_file`, `write_file`, `cmd_status`와 archive/transcript 조회 도구를 받지 않는다.

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
- Coder tool call의 bounded Loom/path evidence 자동 수집
- `read_file`, Loom 조회와 검증 명령만 허용하는 Planner review tool loop
- 최종 전이를 강제하는 `review_task`
- `decision: retry | next`
- 필수지만 빈 문자열을 허용하는 `feedback`
- review facts를 현재 Plan에 병합하는 처리
- 현재 Plan 전체를 포함하는 Coder system prompt builder
- task 순서, retry, next와 attempt 상한을 집행하는 execution loop
- 승인 직후 execution loop로 진입하는 `/plan` TUI 연결
- workspace MCP 도구와 전용 `task_complete`를 제공하는 격리 Coder session.
  비동기 명령의 busy polling을 막기 위해 `cmd_status`는 제외하고
  `run_command` 뒤에는 `next_offset`을 이어서 `wait`만 사용한다.
- Coder/Planner의 실제 assistant note, tool arguments/result와 review payload를
  스크롤해서 보는 detailed trace (`Ctrl+G`로 compact activity 전환)
- 승인 전 Griller/Scout/Planner의 progress, assistant message, tool arguments/result,
  사용자 질문/답변을 시간순으로 담는 human-readable planning audit
- 승인된 audit은 기존 execution JSON의 `checkpoint.planning`에 최종 brief/proposal과
  함께 들어가며, planning audit 자체는 재시작 복구 상태로 사용하지 않는다.
- Coder attempt와 Planner review의 전체 message/tool lifecycle archive
- 승인된 실행의 `.q/sessions/<uuid>/plan-execution.json` atomic checkpoint
- 완료 실행을 `.q/plan-executions/`의 UTC 타임스탬프 JSON으로 보관
- 시작 시 중단 실행 감지와 Resume / Inspect / Discard recovery UI
- target, Coder pending/running, Planner review, completed 단계별 재시작 복구
- Coder running에서 끊겼을 때 기존 부작용을 먼저 검사하는 새 recovery attempt
- checkpoint가 참조하는 Loom artifact를 live GC root로 유지

checkpoint는 Coder 호출 전에 `coder_running`으로 저장된다. 이 상태에서 프로세스가
끝났다면 기존 호출을 그대로 replay하지 않는다. 재개 시 attempt 번호를 올리고 현재
workspace 변경을 먼저 검사하라는 recovery feedback을 주입한다. 반면 Coder 결과가
이미 저장된 `review_pending` 상태는 Coder를 다시 호출하지 않고 Planner review부터
이어간다. 성공적으로 모든 task가 끝나면 checkpoint를 삭제하지 않고
`.q/plan-executions/plan-execution-<UTC timestamp>-<unique ID>.json`으로 옮긴다.
파일명은 `plan-execution-20260828T033456.123456789Z-...json` 형식이며, 같은
시각의 실행도 고유 ID로 구분한다. 저장된 JSON은 최종 Plan, task 결과와 시도
횟수, session/run ID, `updated_at`과 승인 전 planning audit을 그대로 보존한다.
따라서 승인된 실행은 같은 파일의 `checkpoint.planning.events`에서 Griller/Scout/Planner가
어떤 가시적 메시지를 냈고 어떤 도구를 어떤 인자로 호출해 어떤 결과를 받았는지,
사용자에게 무엇을 물어 어떤 답을 받았는지를 시간순으로 확인할 수 있다. provider가
반환하지 않은 hidden chain-of-thought는 기록하거나 재구성하지 않는다.

승인 전에 취소되거나 실패해 execution checkpoint가 생기지 않은 경우에는 같은
`.q/plan-executions/` 디렉터리에
`plan-planning-<UTC timestamp>-<unique ID>.json`을 새로 쓴다. 이 파일은 최상위
`planning` 필드에 동일한 event schema와 가능한 마지막 brief/proposal을 담는다.
활성 `.q/sessions/<uuid>/plan-execution.json`은 만들지 않으므로 Grill 단계는
복구 가능한 checkpoint가 아니다. 로그가 크면 `truncated`와 `dropped_events`로
표시하고 bounded size로 저장한다.

보관 실패 시 기존 completed checkpoint를 남기므로, 복구 재시도에서는 Coder를
다시 호출하지 않고 보관을 완료할 수 있다. 실패·중단된 실행은 계속 기존 위치의
checkpoint로 복구한다. 보관본은 `/clear`와 session 삭제로 지우지 않으며,
자동 GC나 기간별 삭제는 하지 않는다. 사용자가 오래된 JSON을 직접 정리할 수 있다.
완료 JSON은 Loom GC root가 아니므로, 참조된 원본 artifact에는 기존 GC 정책이
적용된다. 이 변경은 JSON 보관이며 전체 tool-output 원문의 영구 보존은 아니다.

아직 연결하지 않은 부분은 승인 이전 Griller/Planner planning state의 재시작 복구다.
