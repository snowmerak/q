# ACP `usage_update` 전달 설계 및 구현

- 기준일: 2026-09-09
- 상태: 구현 및 검증 완료
- 결정: ACP v1 stable `session/update`의 `usage_update`만 사용
- Q 구현 기준 커밋: `646c907` (`feat(orchestration): allow up to nine user choices`)
- 메인루프 변경 기준: `d21c3a6` (`feat(app): compact context between tool rounds`)
- ACP 조사 기준: upstream `65b2246ba7e395c9d633718d822817f53e61f0e1`

## 결정

Q의 ACP 서버는 `session/prompt` 중 컨텍스트의 주요 변경이 완료될 때와 prompt가
끝날 때 현재 세션 사용량을 `session/update` notification의 `usage_update`로
보낸다. 전체 message와 compaction 단위의 증감은 보여주되, token이나 stream
delta마다 보내지는 않는다.

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
| `acpAgent.prompt`의 `promptMu` 구간 | 일반 prompt와 slash/workflow가 직렬화되는 세션 경계 | publisher lifecycle과 unlock 전 final flush의 소유 지점 |

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

`acpAgent.prompt`가 `promptMu`를 획득하면 prompt 범위의 usage publisher를 시작하고
종료 시 final flush를 defer한다. 주요 checkpoint는 다음과 같다.

- user message가 session memory와 workspace projection에 반영된 뒤
- tool loop의 완성된 assistant/tool message가 반영된 뒤
- prompt 시작 전 또는 tool loop 중 compaction이 적용된 직후
- `/clear`로 conversation memory가 재설정된 직후
- plan/debug/review/custom/search/commit/question 경로의 context message 반영 뒤
- 최종 assistant와 provider prompt usage 보정이 반영된 뒤

streaming 중인 부분 문자열은 아직 다음 model request의 session memory가 아니므로
`agent_message_chunk`마다 추정치를 올리지 않는다.

이 지점이 적합한 이유는 다음과 같다.

- 긴 model/tool 작업 중에도 user와 tool message 증가를 client가 볼 수 있다.
- compaction과 `/clear`의 감소가 최종 응답까지 기다리지 않고 게시된다.
- 같은 세션의 다음 prompt가 memory를 바꾸기 전에 최종 snapshot을 flush한다.
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
`usage_update`는 첫 prompt의 첫 context checkpoint부터 도착한다는 제한을 수용한다.

publisher는 prompt당 goroutine 하나와 capacity 1 channel 하나만 소유한다. 전송이
진행 중일 때 변화가 연속되면 대기 중인 값을 최신 snapshot으로 교체한다. 같은
`used/size`를 성공적으로 보낸 경우에는 중복 전송하지 않는다. 따라서 빠른 tool
event가 transport보다 많아져도 queue가 무한히 자라거나 오래된 모든 값을 따라잡느라
prompt가 지연되지 않는다. 이는 모든 중간 값을 보존하는 accounting stream이 아니라
최신 상태를 빠르게 보여주는 projection이다.

### 실패 처리

usage 전달은 best effort projection이다.

1. memory 변경과 workspace 저장은 usage delivery와 무관한 source of truth다.
2. 중간 update는 session lifetime context의 background publisher가 보내므로 model과
   tool 진행을 막지 않는다.
3. 각 전송에는 1초 timeout을 적용하고, pending update는 하나로 제한한다.
4. prompt 종료 시 최종 snapshot을 publisher에 전달하고 `promptMu` 안에서 최대
   1초 동안 flush한 뒤 publisher를 취소한다.
5. 전송 실패는 prompt response/error를 바꾸지 않으며, prompt당 첫 실패만 session
   ID와 error로 구조화 로그에 남긴다. prompt/response 본문은 기록하지 않는다.
6. 영속 queue나 오래된 snapshot 재전송은 두지 않는다. 다음 checkpoint의 최신
   snapshot이 유실된 projection을 대체한다.

연결 자체가 끊겼다면 prompt response도 client에 전달되지 않을 수 있지만,
usage notification이 Q의 세션 저장 성공 여부를 바꾸지는 않는다.

## 구현 결과

### production 변경

[`app/acp_usage.go`](../app/acp_usage.go)에 다음을 구현했다.

- `memory.Stats`를 stable `usage_update` variant로 바꾸는 순수 mapper
- unknown context와 nil memory 생략
- 음수 `used`의 0 보정, `used > size` 보존, `cost` 생략
- capacity 1 최신값 coalescing publisher와 session lifetime context
- 중간 비차단 전송, 종료 시 1초 final flush, 동일값 dedupe
- prompt당 첫 실패에 session ID와 error만 남기는 best-effort 로그

[`app/acp.go`](../app/acp.go)의 `promptMu` 구간이 publisher의 lifecycle을 소유하고,
ACP의 일반 chat, compaction, plan/debug/review, commit/question, agent search와 custom
subagent 경로가 semantic checkpoint를 게시한다. final flush는 unlock 전에 끝나므로
같은 세션의 다음 prompt와 snapshot이 섞이지 않는다.

모델 메인루프, subagent result, `client.Usage`, memory manager, SDK 생성물, workspace
저장 형식, CLI/config는 변경하지 않았다. 메인루프가 compact된 상태를 이미
`a.state.memory`에 동기화하므로 turn usage accumulator나 workflow별 propagation도
추가하지 않았다.

### 검증 결과

[`app/acp_usage_test.go`](../app/acp_usage_test.go)에 다음 경계를 검증했다.

| 경계 | 결과 |
|---|---|
| known/unknown context, 음수, window 초과, nil memory | mapper와 생략 규칙 통과 |
| JSON marshal | `sessionUpdate: "usage_update"`, `used`, `size`, cost 생략 확인 |
| 일반 ACP prompt | model이 완료되기 전에 user message 증가를 보내고, 종료 시 assistant를 포함한 최종 값 flush |
| tool-loop compaction | 큰 tool result 증가 뒤, 메인루프 compaction을 적용한 감소를 다음 model round 전에 전송 |
| `session/new` | handler 안에서는 usage를 보내지 않음 |
| 연속 checkpoint | capacity 1 pending slot이 최신 snapshot만 유지하고 성공한 동일값은 dedupe |
| 전송 오류 | prompt 성공 결과 보존, prompt당 한 번의 본문 없는 구조화 로그 확인 |
| 읽지 않는 client | 중간 전송이 model/tool 진행을 막지 않고 final flush는 1초 timeout으로 종료 |
| JSON-RPC pipe | 모든 usage notification이 올바른 discriminator/session/value로 prompt response보다 먼저 도착 |

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

각 payload는 정수 두 개뿐이고 pending slot은 하나로 고정되며 model/transport
속도가 지배적이므로 별도 benchmark는 두지 않았다.

## 롤백과 완료 기준

디스크 상태나 설정을 바꾸지 않으므로 롤백은 `acpAgent.prompt`의 publisher
lifecycle, 각 checkpoint와 관련 helper/test를 되돌리는 것으로 끝난다. migration이나
저장 데이터 정리는 없다.

구현 결과가 만족하는 조건은 다음과 같다.

- context window를 알고 `promptMu`를 획득한 ACP prompt는 주요 message/compaction
  checkpoint와 종료 시 v1 stable `usage_update`를 보낸다.
- `used`는 prompt 종료 후 Q가 다음 요청에 사용할 context 추정치다.
- compaction과 `/clear` 뒤 감소가 게시되고, 빠른 연속 변화는 최신 값으로 합쳐진다.
- cache token은 `used`에 포함되지만 cache read/write breakdown은 보내지 않는다.
- `cost`는 생략한다.
- context window를 모르면 update를 보내지 않는다.
- notification 실패는 prompt 결과와 저장 상태를 바꾸지 않는다.
- unit, ACP integration, JSON-RPC pipe smoke와 전체 회귀가 통과한다.
- ACP v2와 `PromptResponse.usage`에는 변경이 없다.
