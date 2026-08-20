# Role-based subagent architecture notes

## 상태

이 문서는 역할별 모델 할당과 서브에이전트 orchestration의 전체 메모다.
메인 agent의 `task_start`, `ask_to_user`, `task_complete` 제어와 workspace 비변경
`ScoutRunner`가 구현되어 있다. Scout의 `run_command`/`wait`는 정보 수집에만 사용한다.
`/plan`은 Griller가 필요할 때 Scout를 반복 호출하고,
Planner의 조건·계획을 사용자에게 확인받은 뒤 승인된 task를 격리 Coder session에서
실행한다. 각 attempt 뒤 Planner가 `retry` 또는 `next`를 결정하며, 진행 중 실행의
재시작 복구는 아직 구현하지 않는다.

`/plan`의 확정된 상태와 역할 계약은 [plan-orchestration.md](plan-orchestration.md)를
우선한다. 승인 이후 Coder task와 Planner review 계약은
[execution-orchestration.md](execution-orchestration.md)를 우선한다.

## 목표

하나의 채팅 모델이 조사, 계획, 구현, 문제 진단을 모두 수행하는 대신 작업 목적이
명확한 서브에이전트를 구성한다. 각 역할에는 독립적인 모델, reasoning effort,
context와 실행 예산을 할당할 수 있어야 한다.

초기 역할은 다음 일곱 가지다.

| 역할 | 책임 | 기대 출력 |
|---|---|---|
| `griller` | 요청의 모호함, 숨은 제약, 실패 조건과 잘못된 가정을 집요하게 확인 | 질문, 위험 목록, 명시적 가정 |
| `scout` | 저장소 구조, 관련 코드, 기존 패턴과 변경 범위를 빠르게 탐색 | 관련 파일, 심볼, 영향 범위 |
| `research` | 공식 문서, API, 라이브러리와 외부 사실을 조사 | 근거와 출처가 포함된 조사 결과 |
| `planner` | 조사 결과를 실행 가능한 단계와 완료 조건으로 변환 | 사용자가 승인할 수 있는 구현 계획 |
| `coder` | 사용자가 승인한 계획에 따라 코드를 수정하고 적절히 검증 | 패치, 테스트 결과, 변경 설명 |
| `commit` | 현재 작업 트리의 변경을 검토하고 적절한 Git commit을 준비 | 검증된 변경 범위와 commit 메시지 |
| `advisor` | coder가 막히거나 작업이 늘어질 때 문제를 진단하고 대안을 제시 | 실패 원인 가설, 대안, 권장 다음 행동 |

## 기본 orchestration 흐름

```text
사용자 `/plan` 요청
   │
   ▼
 griller ◄──── scout ──── 저장소 조사
   │  ▲          │
   │  └──────────┘ 필요한 만큼 반복
   ├──────── research ─── 외부 조사
   │
   ▼
 planner
                              │
                       사용자 승인
                    ┌─────────┴─────────┐
              수정 요청                 승인
                 │                       │
                 └────── griller         ▼
                                      coder
                                         │
                         ┌───────────────┴───────────────┐
                      작업 완료                    정체·포기·판단 곤란
                         │                              │
                         ▼                              ▼
                       완료                          advisor
                                                        │
                                  coder 재시도·재계획·사용자 질문 중 결정
```

모든 요청이 전체 파이프라인을 거칠 필요는 없다. 예를 들어 단순한 로컬 수정은
`scout → coder`만 사용할 수 있고, 외부 사실이 필요하지 않으면
`research`를 생략한다. root/orchestrator가 요청 특성에 따라 필요한 역할만
선택한다. `planner`가 계획을 작성한 경우 그 계획은 반드시 사용자가 승인해야
`coder`에게 전달한다. 사용자 요청 자체가 충분히 구체적이어서 `planner`를
생략한 작업은 요청에 명시된 범위를 승인된 변경 범위로 취급한다.

`advisor`는 고정 단계가 아니라 필요할 때만 호출하는 보조 역할이다. 호출 조건은
아직 확정하지 않는다. 초기 후보 신호는 coder가 스스로 진행 불가를 보고하거나,
동일한 실패를 반복하거나, root/orchestrator가 실질적인 진전 없이 루프가
늘어진다고 판단한 경우다. advisor의 제안은 자동으로 실행하지 않으며,
root/orchestrator가 coder 재시도, planner를 통한 재계획, 사용자 질문 중 다음
행동을 결정한다.

## 역할별 초기 성향

아래 값은 모델명이 아니라 모델을 고를 때의 기준이다.

| 역할 | 속도/비용 | reasoning 기본값 후보 | context 성향 |
|---|---|---|---|
| `griller` | 중간 | `high` | 사용자 요청과 기존 결정 중심 |
| `scout` | 빠름 | `low` 또는 `medium` | 저장소 탐색 결과 중심 |
| `research` | 중간 | `medium` 또는 `high` | 큰 context와 출처 보존 |
| `planner` | 중간 | `high` | scout/research 결과와 사용자 피드백 중심 |
| `coder` | 품질 우선 | `high` | 승인된 계획, 관련 코드, 테스트 중심 |
| `commit` | 빠름 | `low` 또는 `medium` | 작업 트리 diff와 검증 결과 중심 |
| `advisor` | 품질 우선 | `high` | coder의 시도, 실패 결과와 미해결 문제 중심 |

정확한 모델과 effort는 `http://localhost:18181/v1/models`의 실제 capability와
요청 실험을 거쳐 결정한다. 모델 ID만으로 reasoning effort 지원 여부를
단정하지 않는다.

## 설정 구조 초안

```yaml
model_groups:
  heavy:
    candidates:
      - model: codex/gpt-5.6-sol
        reasoning_effort: high
        timeout: 60s
      - model: grok/grok-4.5
        reasoning_effort: high
agents:
  max_parallel: 3
  roles:
    griller:
      model: ""
      reasoning_effort: ""
    scout:
      model: ""
      reasoning_effort: ""
    research:
      model: ""
      reasoning_effort: ""
    planner:
      group: heavy
    coder:
      model: ""
      reasoning_effort: ""
    commit:
      model: ""
      reasoning_effort: ""
    advisor:
      model: ""
      reasoning_effort: ""
```

`model`과 `group`을 모두 비워두면 현재 채팅 모델 또는 역할별 기본 모델을
상속한다. 둘은 동시에 지정할 수 없다. 그룹은 후보 순서대로 timeout 또는 HTTP
5xx 장애에 fallback하며, 후보의 `timeout`을 생략하면 별도 제한을 걸지 않는다.
`/model`의 최상위 화면에서 `g`를 누르면 그룹 편집 화면으로 들어간다. Gateway는
그룹을 `group/<name>` 모델로 노출하며 context length는 모든 후보 중 가장 작은
값을 사용한다.
`reasoning_effort`의 기본값은 빈 문자열이다. 값이 비어 있거나 공백만
있으면 요청 필드 자체를 전송하지 않고 provider의 모델 기본값을 사용한다.
명시한 값은 `/v1/models`의 `capabilities.reasoning`이 `effort` control을 광고할
때만 허용하며, `supported_efforts`가 제공되면 그 목록에 포함된 값인지 검증한다.
선택한 모델이 catalog에 없거나 effort를 지원하지 않으면 agent 실행 전에 오류로
처리한다.

## 에이전트 실행 단위

각 서브에이전트 실행은 최소한 다음 값을 가진다.

```go
type AgentSpec struct {
    Role            string
    Model           string
    ReasoningEffort string
    Instructions    string
    TokenBudget     int
}

type AgentTask struct {
    ID           string
    ParentID     string
    Objective    string
    Context      []Message
    Dependencies []string
}

type AgentResult struct {
    TaskID       string
    Status       string
    Outcome      string
    Summary      string
    Findings     []Finding
    Artifacts    []Artifact
    Verification []Verification
    Blocker      string
    Usage        Usage
}
```

`Status`는 runner 관점의 완료, 취소, 오류 같은 lifecycle 상태이고, `Outcome`은
에이전트가 맡은 목적을 달성했는지 또는 막혔는지를 나타낸다.

구체적인 타입은 통합 라이브러리의 task, message, tool, usage 타입을 우선해
재사용한다.

## orchestration builtin tools

현재 메인 agent loop에는 아래 세 도구의 스키마와 실행 제어가 구현되어 있다.
`task_start`가 명시적인 task lifecycle을 시작하고, 시작된 task만
`task_complete`를 필수로 요구한다. 시작하지 않은 turn은 일반 assistant 단답으로
종료할 수 있다. `ask_to_user`는 TUI 입력을 기다렸다가 같은 tool loop를 재개하고,
`task_complete`는 구조화된 결과를 최종 assistant 응답으로 변환하며 실행을
종료한다. 향후 subagent runner에도 같은 제어기를 연결해야 한다.

root와 서브에이전트를 포함한 모든 agent 실행 모드가 사용자와의 상호작용
경계와 자신의 실행 종료를 명시적으로 표현할 수 있도록 다음 builtin tool이
필요하다. 세 도구는 역할이나 모드별 allowlist 대상이 아니며 모든 agent run에
기본으로 등록한다.

### `task_start`

도구 사용이나 여러 실행 단계가 필요한 작업의 lifecycle을 명시적으로 시작한다.
단순 질문과 짧은 답변에는 호출하지 않아도 된다. 성공적으로 호출된 뒤에는 같은
turn이 반드시 `task_complete`로 끝나야 한다.

```go
type TaskStartInput struct {
    Objective          string
    CompletionCriteria []string
}
```

### `ask_to_user`

현재 실행을 일시 정지하고 root/orchestrator를 통해 사용자에게 질문한다.
사용자 응답이 도착하면 그 응답을 호출한 에이전트의 context에 추가하고 실행을
계속한다. 특정 역할이나 승인 흐름에 종속되지 않는 범용 상호작용 도구로
구성한다. 호출하는 LLM이 질문마다 적절한 선택지를 생성하고, UI는 그 선택지와
자유 입력란을 함께 제공한다.

```go
type AskToUserChoice struct {
    ID          string
    Label       string
    Description string
}

type AskToUserInput struct {
    Question string
    Context  string
    Choices  []AskToUserChoice
}

type AskToUserOutput struct {
    SelectedChoiceID string
    Freeform         string
}
```

선택지는 시스템에 미리 고정하지 않는다. LLM은 질문의 맥락에 맞는 짧은 선택지를
일반적으로 함께 제공하고, 사용자는 하나를 선택하거나 자유 입력으로 다른 답을
줄 수 있다. 현재 TUI는 `↑/↓` 또는 `Tab/Shift+Tab`으로 선택지를 이동하고 빈 입력에서
Enter를 누르면 `selected_choice_id`를 반환한다. 텍스트를 입력하면 선택 대신
`freeform`으로 전달한다. planner의 계획 승인/수정 요청은 이 일반 형식의 한 사용
사례다.
질문을 기다리는 동안 전체 orchestration을 멈출지 해당 의존성 branch만 멈출지는
통합 라이브러리의 pause/resume 방식 확인 후 결정한다.

### `task_complete`

현재 agent task를 끝내고 구조화된 결과를 caller에게 반환한다. 서브에이전트가
호출한 경우에는 전체 사용자 요청의 완료 선언이 아니며, 최종 완료 판단은 계속
root/orchestrator가 담당한다. root가 호출한 경우에는 전체 orchestration의 완료
신호가 된다. 정상 완료뿐 아니라 coder가 더 진행할 수 없는 상태도 terminal
outcome으로 전달해 advisor 호출 판단에 사용할 수 있어야 한다.

```go
type TaskCompleteInput struct {
    Outcome      string
    Summary      string
    Findings     []Finding
    Artifacts    []Artifact
    Verification []Verification
    Blocker      string
}
```

`task_complete`는 현재 세션에 완료되지 않은 `task_start`가 있는 경우에만 허용한다.
active task는 `.q/session.json`에 저장되므로 앱 재시작이나 다음 turn 이후에도
복원된다.
`Outcome`의 초기 후보는 `succeeded`와 `blocked`다. 시스템 오류나 취소는 모델이
선언하는 outcome과 구분해 runner lifecycle에서 처리한다. `task_complete`가
호출되면 해당 에이전트의 model/tool loop를 종료하고 입력을 `AgentResult`로
변환한다.

세 도구는 workspace 파일 도구를 제공하는 builtin MCP server와 성격이 다르다.
TUI 상호작용과 agent lifecycle을 제어해야 하므로 공통 agent runner가 항상
등록하거나 tool call을 가로채 처리하는 orchestration 전용 builtin으로 둔다.

## context와 handoff

서브에이전트마다 독립 context를 사용한다. 전체 root 대화를 그대로 복제하지
않고 역할 수행에 필요한 최소 정보만 전달한다.

권장 handoff envelope는 다음 내용을 포함한다.

- 구체적인 objective와 완료 조건
- 관련 사용자 요청과 확정된 결정
- 필요한 파일, 심볼, 문서 위치
- 선행 에이전트의 구조화된 결과
- 허용된 변경 범위
- 금지 사항과 미해결 질문

각 서브에이전트도 기존 78% trigger, 22% target context compaction 정책을
독립적으로 적용할 수 있어야 한다. 다만 짧은 일회성 작업은 압축 없이 종료하는
것이 일반적이다.

## 공유 workspace 정책

서브에이전트가 같은 workspace를 사용할 경우 충돌 방지가 필요하다.

- 동시에 파일을 수정할 수 있는 `coder`는 기본 한 개로 제한한다.
- 여러 coder를 허용하려면 파일 ownership 또는 별도 worktree 전략이 필요하다.
- 사용자 변경과 다른 에이전트의 변경을 임의로 되돌리지 않는다.
- destructive command는 root/orchestrator의 정책을 그대로 따른다.

서브에이전트가 다른 서브에이전트를 무제한 생성하는 recursive spawn은 초기
버전에서는 허용하지 않는다. spawn 권한은 root/orchestrator에만 두고 최대
깊이와 동시 실행 수를 명시한다.

## 결과 병합 원칙

root/orchestrator는 결과를 단순 연결하지 않고 다음 규칙으로 병합한다.

1. 사실과 추론을 구분한다.
2. 서로 충돌하는 결과는 숨기지 않고 근거와 함께 표시한다.
3. planner는 scout/research 결과를 입력으로 받되 원본 결과를 참조할 수 있다.
4. planner가 작성한 계획은 사용자 승인 전까지 coder에게 실행 지시로 전달하지 않는다.
5. coder에게는 사용자가 승인한 계획과 변경 범위만 전달한다.
6. advisor의 진단과 제안은 권고로 취급하며 root/orchestrator가 다음 행동을 결정한다.
7. 완료 판단과 최종 사용자 응답은 root/orchestrator가 담당한다.

## TUI activity projection

메인 transcript를 서브에이전트 로그로 채우지 않고 별도 scrollable trace panel에
role별 상태와 실제 model/tool transcript를 표시한다.

```text
agents · griller ✓ · scout ✓ · planner … · detailed trace
› coder · tool call · read_file

coder · assistant
I will inspect the existing benchmark conventions before editing.

coder · tool call · read_file
{
  "path": "hash_bench_test.go"
}

coder · tool result · read_file
{"content":"...","loom_ref":"..."}
```

runner는 모델이 실제로 반환한 assistant text, tool name과 raw arguments, tool
result/error를 `agent`, `task_id`, `parent_id`와 함께 trace event로 보낸다. JSON은
읽기 좋게 펼쳐 표시한다. `PgUp/PgDn`, `Ctrl+U/Ctrl+D`, `Home/End`로 스크롤하고
`Ctrl+G`로 상세 trace와 compact activity를 전환한다. 새 이벤트가 들어올 때 사용자가
과거 로그를 읽는 중이면 위치를 유지하고, bottom에 있을 때만 자동으로 따라간다.
성공적으로 workflow가 끝나면 상세 panel은 자동으로 접히고
`SUBAGENTS COMPLETE · ctrl+g inspect trace` 한 줄만 남긴다. 따라서 최종 응답과
입력칸이 즉시 보이며, 보존된 trace는 `Ctrl+G`로 다시 펼칠 수 있다. 실패 시에는
원인 확인을 위해 상세 panel을 펼친 상태로 유지한다.

provider가 반환하지 않은 hidden chain-of-thought는 표시하거나 추정하지 않는다.
대신 각 agent prompt는 tool call과 함께 짧은 user-visible progress note를 생성하도록
요구하며, 반환된 note만 assistant trace로 표시한다. 화면 보관은 최근 400 events,
event당 16 KiB로 제한하고, 전체 message/tool lifecycle은 Session Store archive에
유지한다. 이 구조는 병렬 Scout도 task ID별로 구분할 수 있다. 전체 history 탐색용
`/agents` 화면과 개별 agent 취소는 후속 범위다.

## 통합 라이브러리 확인 후 결정할 사항

- 라이브러리가 제공하는 agent/task lifecycle과 cancellation 방식
- agent 실행 중 `ask_to_user`의 pause/resume과 사용자 응답 전달 방식
- `task_complete` 호출로 model/tool loop를 정상 종료하는 방식
- subagent 생성이 callback, runner, graph 중 어떤 추상화인지
- 메시지와 tool call/result 타입을 그대로 재사용할 수 있는지
- 모델별 `reasoning_effort` 전달 필드와 허용 값
- usage, context window와 hidden reasoning token의 보고 방식
- streaming event와 에이전트 상태 이벤트 구조
- 병렬 실행 제한과 공유 client의 concurrency 안전성
- retry, timeout, budget과 오류 전파 정책
- 결과용 structured output/schema 지원 여부
- checkpoint 또는 session persistence 지원 여부

## 초기 구현 단계 후보

라이브러리를 받은 뒤 다음 순서로 다시 검토한다.

1. 라이브러리 타입을 현재 `client`, `memory`, MCP 도구 구조와 대응시킨다.
2. orchestration 전용 `task_complete`를 연결하고 단일 `scout`의 생성, 실행,
   취소, 결과 반환을 검증한다.
3. `ask_to_user`의 TUI pause/resume과 planner 계획 승인 흐름을 연결한다.
4. 역할별 config와 모델/reasoning effort 선택을 추가한다.
5. `scout`와 `research`의 병렬 실행과 결과 handoff를 구현한다.
6. coder의 `blocked` outcome, 정체 신호와 선택적 `advisor` 호출을 구현한다.
7. TUI 상태와 사용량 표시를 추가한다.
8. 충돌, 취소, timeout, context compaction 통합 테스트를 작성한다.
