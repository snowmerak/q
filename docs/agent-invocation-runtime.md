# Agent invocation과 Loom capture 계약

## 목적

모델에 노출되는 subagent 위임은 tool call이다. ACP 또는 q native subagent의 응답을
별도 callback 경로로 상위 모델에 직접 반환하지 않는다. 모든 결과는 하나의 agent
invocation runtime을 통과해 불변 Loom artifact로 capture되고, 정상적인
`assistant tool_call -> tool result` 메시지 쌍으로 반환된다.

최초로 이 경계를 사용하는 invocation tool은 다음과 같다.

- `external_search`: `agents.roles.search`에 할당된 ACP connection을 호출한다.
- `delegate_scout`: Griller를 위해 범위가 제한된 native Scout 조사를 한 번 실행한다.

Griller에서 Planner로, Coder에서 Planner Review로 넘어가는 것처럼 타입이 정해진
workflow transition은 모델에 노출되는 tool call이 아니다. 검증된 값과 durable
checkpoint는 기존 plan execution state machine이 관리하며 이 capture 경계 밖에 둔다.

## Runtime 경계

Agent invocation runtime은 role-scoped workspace tool runtime을 감싸며 다음 네 가지
책임을 가진다.

1. 호출 역할에 허용된 invocation tool만 광고한다.
2. 광고한 invocation을 정확히 하나의 handler로 전달한다.
3. tool-level error를 포함한 모든 handler 결과를 Loom에 capture한다.
4. 호출 모델에는 크기가 제한된 Loom receipt만 반환한다.

알 수 없는 tool은 감싼 runtime으로 위임한다. Loom capture를 사용할 수 없거나
capture가 실패했을 때 invocation 결과를 capture되지 않은 inline 값으로 대신
반환해서는 안 된다. Tool 이름 충돌은 묵시적으로 덮어쓰지 않고 구성 또는 생성
오류로 처리한다.

Capture metadata는 결과를 생성한 transport를 구분한다.

- MCP tool은 protocol `mcp`, artifact kind `mcp-result`를 사용한다.
- ACP agent tool은 protocol `acp`, artifact kind `agent-result`를 사용한다.
- q native subagent는 protocol `q-subagent`, artifact kind `agent-result`를 사용한다.

기존 Loom receipt 정책을 그대로 적용한다. Inline 임계값 이하 결과는 저장하는 동시에
receipt의 `result`에 포함한다. 더 큰 결과는 제한된 `preview`와 `loom_ref`만 반환하고,
전체 결과는 `loom_inspect`, `loom_read`, `loom_eval`로 읽을 수 있도록 Loom에 보존한다.
Agent handler는 capture 전에 별도의 임의 크기 제한을 적용하지 않는다.

## ACP invocation lifecycle

ACP invocation은 현재 turn의 configuration snapshot에서 connection을 결정하고 다음의
격리된 lifecycle 하나를 소유한다.

1. 설정된 command와 authentication method를 결정한다.
2. ACP process를 시작하고 initialize한다.
3. 현재 workspace를 root로 하는 session 하나를 생성한다.
4. Invocation에 맞는 permission policy를 적용한다.
5. 범위가 제한된 task prompt를 보내고 textual response를 수집한다.
6. 지원하면 session을 delete하고, 그렇지 않으면 close한다.
7. Child process를 종료한다.

Search는 read-only permission policy를 사용한다. Search가 자신을 재귀 호출하지 않도록
`external_search`는 Default, Griller, Planner에만 노출하고 Search agent의 격리 ACP
session에는 노출하지 않는다.

## 상위 agent로의 반환

Default, Griller, Planner의 자율 호출은 각자의 기존 model/tool loop를 사용한다. 상위
모델은 tool call을 생성하고 같은 call ID의 `role: tool` 메시지로 Loom receipt를 받은
뒤 다음 model round를 계속한다.

`/agent:search`는 상위 모델에게 tool 선택을 맡기지 않고 동일한 lifecycle을 강제로
실행한다. q가 synthetic assistant tool call을 생성하고 기록한 뒤 같은 runtime으로
`external_search`를 호출하고, matching tool result를 기록한 다음 기존 Default loop를
재개한다. 이때 원래 사용자 메시지를 synthetic evidence prompt로 교체하지 않는다.

TUI와 ACP server surface는 같은 tool lifecycle을 사용한다. 양쪽 모두 assistant tool
call, capture된 tool result, 최종 상위 agent 응답을 workspace session과 archive에
저장한다.

## 구성과 갱신

Invocation runtime은 현재 turn 또는 planning run을 시작할 때 만든 configuration
snapshot을 사용한다. `/agents` 설정을 저장하면 workspace runtime을 재시작하지 않아도
다음 invocation부터 반영된다. Search connection이 없거나 disabled 또는 unassigned이면
`external_search`를 광고하지 않는다. 명시적 `/agent:search`는 turn을 시작하지 않고
필요한 설정을 안내한다.

## 필수 검증

- 성공한 모든 invocation receipt에 유효한 `loom_ref`가 있어야 한다.
- Loom에는 잘리지 않은 전체 structured result가 있어야 한다.
- 큰 결과는 표준 preview 정책을 사용해야 한다.
- Default가 자율적으로 `external_search`를 호출할 수 있어야 한다.
- `/agent:search`가 TUI와 ACP 양쪽에서 matching tool call/result 쌍을 만들어야 한다.
- Griller와 Planner가 다음 round에서 capture된 Search 결과를 받아야 한다.
- Griller가 다음 round에서 capture된 Scout 결과를 받아야 한다.
- 성공, 실패, cancel 뒤 Search session을 delete하거나 close해야 한다.
- disabled 또는 unassigned role에는 invocation tool을 광고하지 않아야 한다.
- 실제 Codex ACP 통합이 capture된 상위 agent handoff까지 완료되어야 한다.
