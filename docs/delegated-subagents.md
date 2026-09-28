# Delegated subagents

## 목적

q의 일반 대화와 custom subagent가 이름으로 다른 subagent를 호출할 수 있게 한다.
호출은 두 모델 도구로 노출한다.
구현은 기존 Go runtime과 state machine 안에 두며 embedded Python 계층은 추가하지 않는다.

```text
delegate_list() -> []DelegateInfo
delegate(subagent_name, prompt) -> TaskResult | captured ACP result
```

`DelegateInfo.kind`는 `inner` 또는 `external`이다. `inner`는 q의 model runner를
실행하고, `external`은 기존 ACP adapter를 호출한다.

## 일반 루프의 위임

Default role 루프에는 workspace 도구와 `delegate_list`, `delegate`가 함께 노출된다.
루트는 요청에 따라 직접 조사·수정하거나 범위가 명확한 작업을 직업형 subagent에
맡길 수 있다. 별도의 `/mode` 전환, coordinator 전용 프롬프트, 루트 도구 제한은 없다.
하위 에이전트는 각 정의와 역할 설정에 따라 독립된 도구와 delegation 권한을 받는다.

이전 버전이 저장한 `loop_mode` 값은 읽기 호환성만 유지하고 새 세션 저장에는 쓰지
않는다. 기존 transcript와 context의 `q_delegation_mode`, `q_delegation_policy`
메시지는 복원할 때 제거하여 과거 coordinator 제한이 default loop에 남지 않게 한다.

`delegate_list`는 전체 등록 목록이 아니라 현재 호출자가 실제로 호출할 수 있고 현재
runtime에서 실행 가능한 agent만 반환한다. `delegate`는 그 목록에 포함된 canonical
ID만 받는다. 호출 결과는 다른 큰 도구 결과와 마찬가지로 Loom에 저장될 수 있으며,
호출자에게는 bounded receipt가 전달된다.

## 공개 직업형 agent

일반 delegation registry에는 다음 q builtin을 등록한다.

- `builtin/interviewer`: 요구사항의 중요한 불확실성을 확인하고 질문을 상위 agent에 전달한다.
- `builtin/manager`: PM으로서 요구사항, 우선순위, 수용 기준과 계획을 맡는다.
- `builtin/senior-developer`: `reviewer` 모델 role로 기술적 접근을 정하고 junior에게 구현을 맡긴 뒤 변경을 직접 검토한다.
- `builtin/junior-developer`: `coder` 모델 role로 senior developer가 할당한 코드를 읽고 수정하며 검증 및 피드백 반영을 수행한다. 루트에서 직접 호출할 수 없다.
- `builtin/research`: 저장소와 외부 자료를 조사하고 근거 있는 대안을 제시한다.

ACP Search와 External Web Tester는 다음 fixed builtin registry entry로도 등록한다. 실제
ACP connection이 role에 할당되어 있고 enabled일 때만 `delegate_list`에 나타난다.

- `builtin/web-search`: `agents.roles.search`의 ACP connection
- `builtin/web-tester`: `agents.roles.external_web_tester`의 ACP connection

`commit`, `thinker`, `librarian`은 독립 workflow 또는
내부 처리기이므로 일반 delegation registry에 등록하지 않는다.

## 공통 lifecycle

`inner` delegation agent는 동일한 `task_start`와 `task_complete` 계약을 사용한다.
`task_complete`의 schema를 agent별로 교체하지 않는다.

```json
{
  "outcome": "succeeded | blocked",
  "summary": "...",
  "findings": ["..."],
  "artifacts": ["..."],
  "verification": ["..."],
  "blocker": "..."
}
```

`task_start`와 `task_complete`는 host가 자동으로 제공하는 protocol 도구이므로 profile의
`tools`에 저장하지 않는다. Plan의 `submit_brief`, `submit_plan`, `review_task` 같은
도메인 전이 도구는 이 공통 완료 도구로 대체하지 않는다.

`external` agent는 이 lifecycle을 실행하지 않는다. Dispatcher가 `delegate`의 bounded
prompt를 fixed Search/Web Tester adapter 또는 custom external의 generic ACP adapter에
전달하고 Loom capture를 적용한다. Custom external은 저장된 system prompt를 첫 ACP
prompt 앞에 붙이고 textual response를 그대로 반환한다.

## 등록과 권한

Custom profile은 scope를 포함한 canonical ID로 등록한다.

```text
global/<profile-name>
workspace/<profile-name>
builtin/web-search
builtin/web-tester
```

기존 `/subagent <name> ...` 명령은 workspace-over-global 이름 해석을 호환 경로로 유지할
수 있다. 그러나 `delegates`와 모델의 `delegate` 호출은 canonical ID만 사용한다.
따라서 workspace profile이 같은 이름의 global agent에 부여된 delegation 권한을
가로챌 수 없다.

Builtin의 delegation 관계는 다음과 같다.

```text
builtin/manager -> [builtin/interviewer, builtin/research, builtin/senior-developer]
builtin/interviewer -> [builtin/research]
builtin/senior-developer -> [builtin/junior-developer, builtin/research, builtin/web-search]
builtin/research -> [builtin/web-search]
```

`builtin/web-search` grant는 ACP Search connection이 unavailable이면 목록에서 자동으로
비활성화된다. 자동 permission을 사용하는 `builtin/web-tester`는 root 또는 명시적으로
선택한 custom profile에서 호출한다.

기존 profile에 저장된 `builtin/scout`, `builtin/griller`, `builtin/planner`,
`builtin/executor`, `builtin/coder`, `builtin/reviewer` grant는 실행 시 무시한다. 이 grant를 새 profile에 저장할 수는 없으며,
기존 profile을 편집할 때도 제거해야 저장할 수 있다. 완료 결과가 저장된 이전 호출은
재시작 후에도 해당 결과를 복구한다.
기존 profile의 `scout`, `griller`, `planner`, `senior-developer`, `junior-developer` 모델 role은 읽을 때 각각
`research`, `interviewer`, `manager`, `reviewer`, `coder`로 변환한다.

Custom profile은 `delegates`로 직접 호출 가능한 agent를 선택한다.

```yaml
version: 1
name: implementer
description: Implement a bounded request and verify it.
kind: inner
role: coder
system_prompt: |
  Work only on the explicit request and report concrete verification.
tools:
  - read_file
  - edit_file
  - run_command
  - wait
delegates:
  - builtin/senior-developer
```

이전 version 1 profile에서 `delegates`가 빠졌으면 빈 목록으로 해석한다. UI로 저장하면
빈 목록도 명시적으로 기록한다. Global profile은 `workspace/*`를 참조할 수 없다.

Dispatcher는 다음 조건을 호출 시점에 다시 검사한다.

- 대상이 등록되어 있고 현재 model과 tool runtime에서 실행 가능한가.
- 대상이 호출자의 직접 `delegates` grant에 포함되는가.
- 현재 호출 stack에 대상이 없어 cycle이 발생하지 않는가.
- 최대 깊이와 전체 호출 수를 넘지 않는가.
- 부모 context의 취소와 deadline이 child에 전달되는가.

`inner` availability는 native role, model, scoped tool을 확인한다. `external`
availability는 enabled ACP role assignment와 Loom capture를 확인하며 native model과
tool scope는 적용하지 않는다. Connection이 꺼지거나 빠지면 저장된 grant는 유지하되
호출은 fail-closed한다.

초기 구현은 동기 호출만 제공한다. 같은 tool turn의 여러 `delegate` 호출은 병렬로
실행하지 않는다. 부모의 도구 호출은 순서대로 저장하고, 각 호출은 순번과 고유
`invocation_id`의 자식 세션을 가진다. 같은 에이전트를 두 번 호출하거나 모델이
`call_id`를 재사용해도 진행 상태와 북마크는 서로 구분된다.

일반 채팅의 `delegate` 실행 중 TUI는 자식의 시작·모델 라운드·도구 호출·완료 상태와
assistant/도구 추적을 표시한다. `ctrl+g`로 추적을 접거나 펼칠 수 있다. ACP는 같은
진행을 thought update로, 자식 도구 호출과 결과를 고유 ACP tool call ID로 전달한다.
중첩 호출은 `task_id`와 `parent_id`로 연결되며, 부모의 대화 문맥에는 기존처럼
자식의 최종 결과만 들어간다. 외부 ACP 자식은 내부 도구 추적을 제공하지 않으므로
시작과 최종 상태를 표시한다.

진행 중인 일반 `delegate` 호출은 부모·자식 세션 트리와 북마크로 복구한다.
저장 형식과 재시작 순서는 [중첩 delegate 세션과 재귀 복구](delegation-session-recovery.md)에 정리했다.

## 도구와 변경 권한

모든 inner subagent는 정의나 custom profile의 도구 목록과 관계없이
`task_start`, `search_skills`, `get_skill`을 고정으로 받는다. `task_start`
결과에 관련 스킬 후보가 들어오며, 전문이 필요하면 `get_skill`로 읽는다.
`builtin/senior-developer`는 `read_file`, `list_directory`, Loom 조회 도구를 직접 사용하지만
파일 변경과 `run_command` 도구를 받지 않는다. 변경 작업은 junior developer에 위임할 수 있다. 파일 변경이 필요한 custom profile은
도구를 명시적으로 선택한다. Registry의 mutation 표시는 직접 변경 도구뿐 아니라
변경 가능한 delegate까지 전파한다.

## TUI와 명령

`/subagents` 화면은 builtin과 custom agent를 같은 목록에서 보여 주고 실행 방식은
`kind: inner|external`로 표시한다. Builtin inner definition은 고정이며 access label은
직접 도구와 delegate 권한을 함께 반영한다. Builtin external은
system prompt를 definition에 포함하며 화면에서는 연결된 ACP만 바꿀 수 있다.
`c`에서 shared ACP connection을 등록·편집·검사·활성화·삭제한다. 별도 `/agents` 화면은 없다.
`builtin/web-tester`에는 workspace
mutation 가능성 표시를 붙인다.

새 custom external profile은 Scope 아래에서 Kind를 external로 선택한 뒤 ACP connection,
System Prompt, workspace access를 저장한다. q model Role, Tools, Delegates는 비활성화된다.
ACP protocol의 NewSession에는 system-message 필드가 없으므로 이 System Prompt는 첫
ordinary ACP prompt 앞에 붙는다.

```yaml
version: 1
name: browser-check
kind: external
agent: browser
system_prompt: Verify the requested browser behavior and report observed evidence.
mutates_workspace: true
tools: []
delegates: []
```

Delegates picker는 registry에서 현재 선택 가능한 canonical ID와 설명을 읽는다. 자기
자신, scope상 참조할 수 없는 대상, 호출 불가능한 내부 workflow는 표시하지 않는다.
저장 시 이름을 정렬하고 중복, 자기 참조, cycle, scope 위반을 검증한다. 저장 오류는
현재 폼의 동작처럼 draft를 보존한다.

`/subagents list`와 `/subagents show`는 kind, source, model role 또는 ACP connection,
변경 가능 여부, 도구 및 delegation 정보를 표시한다. `/subagent`는 공개 builtin의
canonical ID와 기존 custom profile 이름을 실행할 수 있다. Builtin external의 직접
실행은 `/subagent builtin/web-search <query>` 또는
`/subagent builtin/web-tester <request>`로 정규화하며, 기존 전용 실행 lifecycle을
내부적으로 재사용한다.

## 제거하는 전용 workflow

다음 진입점은 일반 subagent delegation으로 대체하고 제거한다.

- TUI와 ACP의 `/debug`, `/review`
- CLI의 `q diagnose`, `q review`
- 전용 Debug와 Advisor Review runner 및 새 실행 기록 작성 경로

기존 `.q/debug-executions`와 `.q/review-executions` 파일은 사용자 데이터이므로 삭제하지
않는다. 기존 plan 실행 checkpoint는 데이터 호환성을 위해 읽을 수 있지만 새 plan 작업은 시작하지 않는다.

## 구현 순서

1. Registry, dispatcher, 공통 lifecycle 및 delegate 도구를 추가한다.
2. 직업형 builtin과 일반 대화를 연결한다.
3. Profile schema와 `/subagents` 편집 UI에 Delegates를 추가한다.
4. Senior developer와 junior developer를 포함한 직업형 역할을 등록한다.
5. `/debug`, `/review`, `q diagnose`, `q review`와 전용 runner를 제거한다.
6. Registry 권한, cycle, lifecycle, TUI 저장, TUI·ACP 호출을 검증한다.
