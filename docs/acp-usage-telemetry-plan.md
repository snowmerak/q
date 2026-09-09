# ACP `usage_update` 전달 설계 및 구현

- 기준일: 2026-09-09
- 상태: 구현 및 검증 완료
- 결정: ACP v1 stable `session/update`의 `usage_update`만 사용
- Q 기준 커밋: `d21c3a6` (`feat(app): compact context between tool rounds`)
- ACP 조사 기준: upstream `65b2246ba7e395c9d633718d822817f53e61f0e1`

## 결정

Q의 ACP 서버는 각 `session/prompt`가 끝날 때 현재 세션의 컨텍스트 사용량을
`session/update` notification의 `usage_update`로 보낸다.

다음은 이번 범위에서 제외한다.

- `PromptResponse.usage`와 turn별 input/output/total token 합계
- cache read/write token의 별도 전달
- reasoning token과 비용 계산
- 원격 분석 수집, OpenTelemetry exporter, 사용량 영속화
- ACP v2 지원 또는 v2 전환을 위한 선행 추상화
- CLI/config feature flag

ACP v2는 정식 출시 전까지 지원하지 않는다. 이번 구현은 현재 Q가 사용하는
ACP v1과 stable `usage_update` 계약만 대상으로 한다.

`usage_update`에는 cache read/write breakdown이 없다. 캐시된 prompt token도
컨텍스트 윈도를 차지하므로 Q가 보내는 `used`에는 포함되지만, cache hit나
cache write 양을 따로 보여주지는 않는다. 따라서 이번 범위에는
`PromptDetails.CachedTokens` 또는 `CacheWriteTokens` 매핑이 필요 없다.

## 프로토콜 계약

`usage_update`는 2026-06-05 ACP v1 stable로 승격됐다
([공식 발표](https://github.com/agentclientprotocol/agent-client-protocol/blob/65b2246ba7e395c9d633718d822817f53e61f0e1/docs/announcements/session-usage-stabilized.mdx),
[RFD](https://github.com/agentclientprotocol/agent-client-protocol/blob/65b2246ba7e395c9d633718d822817f53e61f0e1/docs/rfds/session-usage.mdx)).
권위 있는 wire 정의는 현재 ACP v1 stable schema다
([schema](https://github.com/agentclientprotocol/agent-client-protocol/blob/65b2246ba7e395c9d633718d822817f53e61f0e1/schema/v1/schema.json)).

```json
{
  "jsonrpc": "2.0",
  "method": "session/update",
  "params": {
    "sessionId": "q-session-id",
    "update": {
      "sessionUpdate": "usage_update",
      "used": 53000,
      "size": 200000
    }
  }
}
```

| 필드 | ACP 의미 | Q 값 |
|---|---|---|
| `sessionId` | 상태가 속한 세션 | 해당 `acpAgent` runtime의 `sessionID` |
| `used` | 현재 컨텍스트에 들어 있는 token | `max(0, memory.Stats().PredictedTokens)` |
| `size` | 현재 유효한 전체 context window | `memory.Stats().ContextWindow` |
| `cost` | 선택적 세션 누적 비용 | 생략 |

`used`와 `size`는 ACP schema의 non-negative integer 계약을 지킨다. 의미 있는
context window를 알 수 없어 `size <= 0`이면 notification 전체를 생략한다.
schema는 `used <= size`를 요구하지 않으므로 Q의 보수적 추정치가 window를
초과하면 상한으로 숨기지 않고 그대로 보낸다.

이 notification은 연결된 ACP client로 보내는 현재 상태 projection이다. 원격
수집이나 durable accounting이 아니며 응답/ack도 없다. 전달에 실패해도 세션
메시지와 workspace 저장 상태가 source of truth로 남는다.

## 현재 Q 재확인

메인루프 변경 `d21c3a6`을 포함해 구현 전 코드를 확인한 결과, 당시 Q는
`usage_update`를 보내지 않았지만 구현에 필요한 상태는 이미 한 곳에 모여 있었다.

| 현재 경로 | 동작 | 구현에 주는 영향 |
|---|---|---|
| [`streamAgentLoop`](../app/model.go) | tool round마다 loop-local memory에 메시지를 추가하고 공급자 prompt usage로 추정치를 보정 | 모델 호출별 usage를 따로 합산할 필요 없음 |
| [`agentLoopContext`](../app/agent_context.go) | tool loop 중 context를 compact하고 compaction plan/summary를 event로 전달 | compact된 현재 context를 주 세션에 반영할 수 있음 |
| `continueACPAgentTurn` | assistant/tool message와 compaction을 `a.state.memory`에 적용 | prompt 종료 시 주 세션 memory가 최종 상태를 보유 |
| `finishPrompt` | 최종 assistant를 추가하고 마지막 prompt usage로 `ObserveUsage`를 호출 | notification 직전의 context 추정치가 최신 상태 |
| [`memory.Manager.Stats`](../memory/manager.go) | `ContextWindow`, `PredictedTokens`, 최근 공급자 보정값 제공 | `used/size`의 단일 원천으로 사용 가능 |
| `acpAgent.prompt`의 `promptMu` 구간 | 일반 prompt와 slash/workflow가 직렬화되는 세션 경계 | unlock 전에 notification을 한 번 연결할 지점 |

`PredictedTokens`는 정확한 provider tokenizer snapshot이 아니라, 현재 메시지의
로컬 추정치에 공급자 prompt token에서 관측한 overhead와 안전 여유를 합친
보수적 값이다. 직전 `PromptTokens`만 보내면 방금 추가된 assistant/tool
메시지를 놓치므로 현재 Q의 context 관리와 같은 `PredictedTokens`를 쓰는 편이
일관된다.

Q의 SDK 포크는 오래된 stable schema를 기반으로 해 생성 주석에는
`SessionUsageUpdate`가 unstable로 남아 있지만, 필요한
`acp.SessionUpdate.UsageUpdate`와 `acp.SessionUsageUpdate` 타입 및 JSON
discriminator 처리는 이미 생성돼 있다
([SDK 포크 문서](./acp-go-sdk-patch.md)). 현재 공식 stable wire와 같은 형태를
낼 수 있으므로 SDK schema나 `types_gen.go`는 이번에 변경하지 않는다.

## 전송 시점과 실패 처리

### 전송 시점

`acpAgent.prompt`가 `promptMu`를 획득한 직후 best-effort emitter를 defer한다.
함수 본문이 성공·취소·오류로 끝나면 emitter가 현재 memory snapshot을 한 번
보낸 다음 mutex를 푼다.

이 지점이 적합한 이유는 다음과 같다.

- 일반 chat, tool loop, `/clear`, plan/debug/review/custom/commit 등 직렬화된 ACP
  prompt 경로를 한 곳에서 다룬다.
- 같은 세션의 다음 prompt가 memory를 바꾸기 전에 최종 snapshot을 읽는다.
- tool-loop compaction event와 최종 assistant 반영이 이미 끝난 상태다.
- 취소나 오류 중 일부 메시지가 반영됐어도 실제 남은 context를 알릴 수 있다.
- turn별 provider usage 소유권을 workflow result에 추가할 필요가 없다.

`acpPromptMessage` 변환이나 session 검증처럼 mutex를 잡기 전에 실패하는 요청과,
mutex 밖에서 처리되는 workspace learning control command는 update를 보내지 않는다.
mutex를 잡은 뒤 취소되거나 실패한 prompt는 일부 상태가 이미 반영됐을 수 있으므로
종료 시점의 snapshot을 보낸다.

`NewSession`이나 `LoadSession` handler 안에서도 보내지 않는다. 현재 SDK는
handler가 반환된 뒤 JSON-RPC response를 쓰므로 그 안에서 notification을 보내면
세션 생성/복원 response보다 먼저 wire에 나갈 수 있다. 이런 초기 update가
client에서 유실될 수 있다는 사례가 있다
([Zed issue #60199](https://github.com/zed-industries/zed/issues/60199)). 첫
`usage_update`는 첫 prompt가 끝날 때 도착한다는 제한을 수용한다.

prompt 안에서 여러 번 compact되더라도 중간 update는 보내지 않고 최종 snapshot
하나만 보낸다. notification은 작고 prompt당 한 번으로 제한되므로 별도 dedupe
상태, queue, rate limit은 추가하지 않는다.

### 실패 처리

usage 전달은 best effort projection이다.

1. deferred helper는 prompt 함수의 반환값을 읽거나 변경하지 않아 원래 response와
   error를 그대로 보존한다.
2. request context는 취소됐을 수 있으므로 session lifetime context에서 1초짜리
   child context를 만들고 기존 `runtime.updateContext(...)`로 `usage_update`를
   시도한다.
3. deferred emitter가 실패하면 session ID와 error만 구조화 로그에 남긴다.
   prompt/response 본문은 기록하지 않는다.
4. usage 전송 실패로 성공한 prompt를 실패시키거나 원래 prompt error를
   덮어쓰지 않는다.
5. 재시도와 영속 queue는 두지 않는다. 다음 prompt가 최신 snapshot을 다시
   보내므로 오래된 update를 복구할 필요가 없다.

전송 중에는 snapshot 순서를 지키기 위해 `promptMu`를 유지하지만 최대 1초 뒤
취소한다. 따라서 읽지 않는 client가 한 세션의 다음 prompt를 무한히 막지
못한다.

연결 자체가 끊겼다면 prompt response도 client에 전달되지 않을 수 있지만,
usage notification이 Q의 세션 저장 성공 여부를 바꾸지는 않는다.

## 구현 결과

### production 변경

[`app/acp_usage.go`](../app/acp_usage.go)에 다음을 구현했다.

- `memory.Stats`를 stable `usage_update` variant로 바꾸는 순수 mapper
- unknown context와 nil memory 생략
- 음수 `used`의 0 보정, `used > size` 보존, `cost` 생략
- session lifetime context를 부모로 한 1초 timeout 전송
- 실패 시 session ID와 error만 남기는 best-effort 로그

[`app/acp.go`](../app/acp.go)의 `promptMu` 구간에는 defer 한 줄만 연결했다. Go의
defer LIFO 순서 때문에 usage 전송이 unlock보다 먼저 실행되어 같은 세션의 다음
prompt와 snapshot이 섞이지 않는다. emitter는 반환값을 변경하지 않는다.

production 변경은 신규 helper 45줄과 prompt 연결 3줄이다. 모델 메인루프,
subagent result, `client.Usage`, memory manager, SDK 생성물, workspace 저장 형식,
CLI/config는 변경하지 않았다. 메인루프가 compact된 상태를 이미
`a.state.memory`에 동기화하므로 turn usage accumulator나 workflow별 propagation도
추가하지 않았다.

### 검증 결과

[`app/acp_usage_test.go`](../app/acp_usage_test.go)에 다음 경계를 검증했다.

| 경계 | 결과 |
|---|---|
| known/unknown context, 음수, window 초과, nil memory | mapper와 생략 규칙 통과 |
| JSON marshal | `sessionUpdate: "usage_update"`, `used`, `size`, cost 생략 확인 |
| 일반 ACP prompt | 종료 뒤 최종 session memory 값으로 정확히 한 번 전송 |
| tool-loop compaction | 메인루프 compaction을 session memory에 적용한 뒤 그 최종 값 전송 |
| `session/new` | handler 안에서는 usage를 보내지 않음 |
| 전송 오류 | prompt 성공 결과 보존, 본문 없는 구조화 로그 확인 |
| 읽지 않는 client | 1초 timeout으로 전송을 중단하고 prompt 성공 결과 보존 |
| JSON-RPC pipe | notification discriminator/session/value를 wire에서 확인하고 prompt response보다 먼저 도착 |

실행한 회귀 검증은 다음과 같다.

```sh
go test ./app -count=1
task test
task dist:check
```

모두 통과했다. `task test`에는 전체 module test, 중첩 ACP SDK test와
`acp:check` 생성물 drift 검사가 포함된다. race detector도 시도했으나 현재 Windows
Go 환경이 `CGO_ENABLED=0`이라 `-race requires cgo`로 실행되지 않았다. 대신 세션
잠금 안에서의 전송 순서와 blocking client timeout을 결정론적 테스트로 검증했다.

정수 두 개의 snapshot과 작은 JSON notification 하나를 prompt당 한 번만
추가하므로 별도 benchmark는 두지 않았다.

## 롤백과 완료 기준

디스크 상태나 설정을 바꾸지 않으므로 롤백은 `acpAgent.prompt`의 emitter defer와
관련 helper/test를 되돌리는 것으로 끝난다. migration이나 저장 데이터 정리는
없다.

구현 결과가 만족하는 조건은 다음과 같다.

- context window를 알고 `promptMu`를 획득한 ACP prompt가 종료될 때 v1 stable
  `usage_update`가 한 번 전송된다.
- `used`는 prompt 종료 후 Q가 다음 요청에 사용할 context 추정치다.
- cache token은 `used`에 포함되지만 cache read/write breakdown은 보내지 않는다.
- `cost`는 생략한다.
- context window를 모르면 update를 보내지 않는다.
- notification 실패는 prompt 결과와 저장 상태를 바꾸지 않는다.
- unit, ACP integration, JSON-RPC pipe smoke와 전체 회귀가 통과한다.
- ACP v2와 `PromptResponse.usage`에는 변경이 없다.
