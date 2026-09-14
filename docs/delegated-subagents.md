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

`delegate_list`는 전체 등록 목록이 아니라 현재 호출자가 실제로 호출할 수 있고 현재
runtime에서 실행 가능한 agent만 반환한다. `delegate`는 그 목록에 포함된 canonical
ID만 받는다. 호출 결과는 다른 큰 도구 결과와 마찬가지로 Loom에 저장될 수 있으며,
호출자에게는 bounded receipt가 전달된다.

## 공개 agent와 plan 내부 agent

일반 delegation registry에는 다음 q builtin을 등록한다.

- `builtin/scout`: 저장소와 제공된 자료를 조사한다.
- `builtin/griller`: 요청의 모호함, 빠진 조건, 위험을 찾는다.
- `builtin/planner`: 요청을 실행 가능한 접근법과 단계로 정리한다.
- `builtin/reviewer`: 요청한 코드나 결과를 수정하지 않고 검토한다.
- `builtin/coder`: 요청 범위에서 workspace를 수정하고 검증한다.

`/plan`과 `q sprint`의 Griller, Planner, Coder, Planner review는 승인된 계획과
구조화된 중간 결과를 사용하는 별도 Go workflow다. 특히 plan Coder와 plan reviewer는
일반 registry에 공개하지 않으며 `delegate_list`에도 나타나지 않는다.

ACP Search와 External Web Tester는 다음 fixed builtin registry entry로도 등록한다. 실제
ACP connection이 role에 할당되어 있고 enabled일 때만 `delegate_list`에 나타난다.

- `builtin/web-search`: `agents.roles.search`의 ACP connection
- `builtin/web-tester`: `agents.roles.external_web_tester`의 ACP connection

`research`는 실행 runner가 없고, `commit`, `thinker`, `librarian`은 독립 workflow 또는
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

Builtin의 초기 delegation 관계는 다음과 같다.

```text
builtin/scout    -> []
builtin/griller  -> [builtin/scout, builtin/web-search]
builtin/planner  -> [builtin/scout, builtin/web-search]
builtin/reviewer -> [builtin/scout, builtin/web-search]
builtin/coder    -> [builtin/scout, builtin/reviewer]
```

`builtin/web-search` grant는 ACP Search connection이 unavailable이면 목록에서 자동으로
비활성화된다. 자동 permission을 사용하는 `builtin/web-tester`는 builtin에 기본 grant
하지 않고 root 또는 명시적으로 선택한 custom profile에서만 호출한다.

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
  - builtin/scout
  - builtin/reviewer
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
실행하지 않는다.

## 도구와 변경 권한

`builtin/scout`, `builtin/griller`, `builtin/planner`, `builtin/reviewer`는 파일 변경
도구를 받지 않는다. `run_command`는 파일을 변경할 수 있으므로 이 네 builtin의 기본
도구에서 제외한다. `builtin/coder`만 파일 변경과 command 도구를 받는다.

`builtin/griller`, `builtin/planner`, `builtin/reviewer`는 `read_file`과
`list_directory`도 받지 않는다. 저장소의 파일이나 디렉터리 원문이 필요하면 직접
읽지 않고 `builtin/scout`에 위임한다. `builtin/scout`와 `builtin/coder`는 두 읽기
도구를 유지한다.

`builtin/coder`는 명시적인 delegation grant가 있을 때만 발견되고 호출된다.
Reviewer는 Coder를 호출할 수 없으므로 검토가 자동 수정으로 바뀌지 않는다.

## TUI와 명령

`/subagents` 화면은 builtin과 custom agent를 같은 목록에서 보여 주고 실행 방식은
`kind: inner|external`로 표시한다. Builtin inner는 read-only다. Builtin external은
system prompt를 definition에 포함하며 화면에서는 연결된 ACP만 바꿀 수 있다.
`c`에서 shared ACP connection을 등록·편집·검사·활성화·삭제한다. 별도 `/agents` 화면은 없다.
`builtin/coder`와 `builtin/web-tester`에는 workspace mutation 가능성 표시를 붙인다.

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
않는다. `/plan`, `q sprint`, plan 실행 checkpoint와 Planner review는 유지한다.

## 구현 순서

1. Registry, dispatcher, 공통 lifecycle 및 delegate 도구를 추가한다.
2. Scout, Griller, Planner builtin과 일반 대화를 연결한다.
3. Profile schema와 `/subagents` 편집 UI에 Delegates를 추가한다.
4. 일반 Reviewer와 Coder를 등록한다.
5. `/debug`, `/review`, `q diagnose`, `q review`와 전용 runner를 제거한다.
6. Registry 권한, cycle, lifecycle, TUI 저장, TUI·ACP 호출 및 plan 회귀를 검증한다.
