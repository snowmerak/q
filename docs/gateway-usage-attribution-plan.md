# Gateway usage 귀속 및 중복 제거 계획

상태: 구현 완료
대상: Q 유지보수자와 Gateway를 호출하는 외부 클라이언트 작성자
최종 갱신: 2026-09-20

이 문서는 `q gateway start`와 Q가 관리하는 내부 Gateway를 통과한 model 호출을
`q usage`에 포함시키는 작업 명세다. 헤더 이름, validation, event 생성의 권위는
`client`와 `providerhost`의 Go 구현이며, 이 문서는 소유권과 실패 의미를 설명한다.

## 목표와 현재 차이

현재 Q의 model client는 provider 응답을 직접 관측한 뒤 usage를 기록한다. 반면 독립
process인 `q gateway start`는 응답을 proxy할 뿐 Usage service에 기록하지 않는다.
따라서 외부 Go project나 다른 OpenAI-compatible client가 Gateway를 호출하면 실제
provider 사용량이 `q usage`에서 빠진다.

이번 변경의 목표는 다음과 같다.

- managed Gateway와 standalone Gateway가 성공한 chat, Responses, embedding 응답의
  provider token usage를 기록한다.
- Q client가 호출의 bounded `role`과 안정적인 `event_id`를 Gateway에 전달한다.
- Gateway 기록과 기존 Q client 기록이 동시에 일어나도 한 호출만 집계한다.
- usage 기록 장애가 model 응답의 성공·실패를 바꾸지 않는다.
- prompt, completion, tool argument, credential 같은 payload는 기록하지 않는다.

비목표는 비용 계산, 사용자 identity 기반 과금, arbitrary tag 수집, provider payload
보존, Gateway 인증 정책 변경이다.

## 소유권과 호출 흐름

Gateway가 통과시킨 provider 응답 usage의 우선 기록자다. Q client의 기존 Recorder는
Gateway 기록 실패에 대비한 best-effort fallback으로 남는다.

```text
Q client ── role + event_id ──> Q Gateway ──> provider
   │                                │
   │ fallback                       │ provider usage 우선 기록
   └──────── same event_id ─────────┘
                    │
                    v
           Usage service / SQLite
           UNIQUE(event_id)
```

Gateway는 usage가 포함된 성공 응답 조각을 downstream에 쓰기 전에 기록을 시도한다.
Q client도 응답 종료 시 같은 `event_id`로 기록할 수 있지만,
`usage_events.event_id`의 unique constraint와 `ON CONFLICT DO NOTHING` 때문에 daily
rollup까지 정확히 한 번만 증가한다. Gateway가 event를 먼저 commit하면 client 쓰기는
no-op이고, Gateway 기록이 실패하면 client가 같은 event를 채울 수 있다.

외부 client가 metadata를 보내지 않으면 Gateway가 새 event ID를 만들고 role은
`gateway`로 기록한다. 외부 client에는 Q client fallback이 없으므로 Usage service가
일시적으로 기록을 받지 못한 호출은 복구되지 않는다. usage는 진단 projection이며
이 손실 때문에 model 응답을 실패시키지는 않는다.

## Metadata 계약과 신뢰 경계

Q client가 명시적으로 Gateway metadata forwarding을 켠 경우에만 다음 request
header를 보낸다.

| Header | 의미 | 제한 |
| --- | --- | --- |
| `X-Q-Usage-Event-ID` | 한 물리 model 호출의 idempotency ID | 32자 또는 64자 lowercase hexadecimal |
| `X-Q-Usage-Role` | 호출을 만든 Q execution role | 최대 64자, lowercase ASCII `a-z`, `0-9`, `_`, `-` |

이 metadata는 인증, 권한, audit identity가 아니다. Gateway는 값을 검증·정규화한 뒤
provider handler에 전달하기 전에 두 header를 제거한다. 따라서 provider endpoint로
누출되지 않는다. Standalone Gateway caller가 role을 직접 제시할 수 있으므로 role은
운영 분류일 뿐 신뢰 가능한 사용자 identity가 아니다. Cardinality는 문자 집합과 길이
제한으로 bounded하게 유지한다.

일반 provider를 직접 호출하는 `client.Client`는 opt-in이 없으면 이 header를 보내지
않는다. Q가 관리하는 Gateway를 대상으로 생성한 client만 forwarding을 켠다.

## 응답 처리와 실패 의미

기록 대상은 HTTP 2xx의 다음 응답이다.

- `/v1/chat/completions`: JSON `usage` 또는 SSE final usage chunk
- `/v1/responses`: JSON `usage` 또는 `response.completed` event의 usage
- `/v1/embeddings`: JSON `usage`

Gateway middleware는 response bytes를 downstream에 그대로 전달하면서 model과 token
count만 읽는다. streaming flush를 보존하며 전체 response payload를 Usage storage에
보관하지 않는다. provider가 usage를 전혀 제공하지 않으면 Gateway는 임의의 과금값을
만들지 않는다. Q client는 자신이 소유한 chat/embedding 호출에 대해서만 기존의 bounded
추정 fallback을 사용할 수 있다.

Recorder에는 기존 250ms deadline이 적용된다. 기록 실패는 response status/body를
바꾸지 않는다. HTTP rejection과 usage가 없는 error response도 기록하지 않는다.

## 구현 순서

1. `client`에 Gateway metadata header, validation, event context와 opt-in forwarding을
   추가한다.
2. forwarded event ID를 client-side `UsageRecord`에도 재사용한다.
3. `providerhost`에 streaming-safe usage response middleware를 추가한다.
4. managed child와 `q gateway start` 모두 같은 middleware와 user-level Recorder를
   구성한다.
5. managed Q client에서만 metadata forwarding을 활성화한다.
6. README와 model usage 문서를 실제 동작에 맞게 갱신한다.

## 수용 조건과 검증

- Q client가 보낸 role/event ID와 client fallback record의 event ID가 같다.
- opt-in하지 않은 direct provider client는 Q metadata header를 보내지 않는다.
- Gateway는 metadata header를 downstream provider에 전달하지 않는다.
- metadata가 없는 external call은 `gateway` role과 유효한 새 event ID로 기록된다.
- JSON chat/embedding/Responses와 SSE chat/Responses usage가 올바른 token field로
  변환된다.
- 같은 event ID의 Gateway/client 기록은 SQLite에서 한 호출로 집계된다.
- Recorder 실패와 invalid metadata가 model response를 손상시키지 않는다.
- focused test, `go test ./...`, `go vet ./...`, distribution module check가 통과한다.

## 완료 기록

2026-09-20에 위 순서대로 구현했다. managed child와 `q gateway start`는 같은
middleware를 사용하고, Q의 managed client만 metadata forwarding을 활성화한다.
standalone 통합 테스트는 임시 OpenAI-compatible provider, 실제 Gateway HTTP server,
실제 Usage service와 SQLite query를 연결해 `planner` role 한 호출이 한 번 집계되는 것을
확인한다.

실행한 검증은 다음과 같다.

- `go test ./...`
- `go vet ./...`
- `go run ./scripts/modulecheck`
- `git diff --check`

모두 통과했다. module check는 현재 checkout을 file module proxy로 묶고 격리된 cache에서
versioned `q`와 `q-mcp`를 설치했으며 외부 publish는 수행하지 않았다.

남은 제한은 의도한 failure contract와 같다. provider가 usage를 보내지 않으면 Gateway는
임의 추정치를 만들지 않는다. Q client fallback은 기존에 계측하던 chat, chat stream,
embedding 호출에 적용되며 raw Responses API 호출은 Gateway 기록에 의존한다. 외부
client-only 호출은 Gateway Recorder가 250ms 안에 commit하지 못하면 diagnostic event를
잃을 수 있지만 model 응답은 계속 전달된다.
