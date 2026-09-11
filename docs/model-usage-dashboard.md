# Model Usage 저장소와 대시보드 작업 명세

작성일: 2026-09-11

상태: 구현 완료. 이 문서는 model usage의 전역 소유권, SQLite/Parquet 보존,
loopback API와 웹 대시보드 계약을 소유한다. 호출별 token 추출 규칙은
`model-usage-tracing.md`가 소유한다.

대상 독자: Q의 model client, 전역 서비스, CLI, storage와 dashboard를 변경하는
구현자와 리뷰어.

갱신 조건: service ownership, schema, retention, archive recovery, API 또는 dashboard
동작이 바뀔 때 이 문서를 먼저 또는 같은 변경에서 갱신한다.

## 1. 목표와 범위

Q가 관측하는 각 물리 model API 호출에 대해 token 수만 수집한다. 여러 Q 프로세스가
동시에 실행되어도 하나의 user-level Usage service가 저장소를 소유한다. 최근 raw
이벤트와 전체 기간 집계는 SQLite에서 조회하고, 오래된 raw 이벤트는 날짜별 Parquet로
보존한다. `q usage`는 같은 서비스가 제공하는 read-only 웹 대시보드를 연다.

포함 범위:

- chat, stream, embedding의 provider 보고 token 및 bounded 로컬 추정값.
- model과 실행 role별 호출 수 및 token 집계.
- 파일 락과 고정 loopback endpoint를 이용한 단일 service leader와 HTTP proxy.
- SQLite hot store, 전체 기간 일별 summary, 90일이 지난 complete UTC day의 Parquet archive.
- 기존 process-local JSONL 로그의 idempotent one-time import.
- dashboard HTML과 versioned JSON API, OpenAPI 문서, `q usage` CLI.
- 저장·프록시 장애가 model call 결과를 바꾸지 않는 best-effort 격리.

제외 범위:

- prompt, completion, tool arguments, endpoint, API key, 비용의 저장.
- provider 청구 금액 계산과 환율·가격표 관리.
- Q가 응답을 소유하지 않는 external agent 내부 호출.
- 원격 접근, 계정 인증, multi-user dashboard 또는 외부 telemetry 전송.
- dashboard에서 raw prompt나 개별 conversation을 탐색하는 기능.

## 2. 규모 가정과 선택

관측한 실제 표본은 10분 46초 동안 187 호출, 6,312,119 cumulative tokens,
37,645 byte JSONL이었다. 현재 schema의 재현 가능한 benchmark는 Windows ARM64에서
187,000행을 행당 약 177 byte(약 31.6 MiB)에 저장했다. 동일 행을 36시간에 집중시킨
worst-burst 표본에서 최근 구간의 정확한 hot query는 warm 3회 평균 약 445 ms/op,
전체 기간 daily-rollup query는 약 0.19 ms/op였다. 따라서 저장량은 160K 같은 token 수가
아니라 API 호출 수에 비례하며, Q의 단일 사용자 workload에는 SQLite가 충분하다.

다만 raw event를 무기한 한 파일에 쌓지 않는다.

- 최근 90일 raw event: SQLite `usage_events`.
- 전체 기간 dashboard: SQLite `usage_daily` rollup.
- 90일보다 오래된 complete UTC day raw event: immutable daily Parquet.

dashboard의 장기 조회는 Parquet를 매 요청마다 scan하지 않는다. Parquet는 장기 원본과
외부 분석용이고, 빠른 화면은 보존되는 daily rollup을 사용한다. 최근 90일의 7d/30d/90d
조회는 시작·종료 partial UTC day만 raw event로 읽고 그 사이 complete day는 rollup으로
합쳐 timestamp 경계의 정확성과 응답 속도를 함께 유지한다.

## 3. 소유권과 lifecycle

기본 endpoint는 `http://127.0.0.1:17893`이다. 설정과 데이터는 user configuration
directory 아래에 둔다.

```text
q processes (TUI / ACP / commands / subagents)
               |
               | bounded loopback HTTP
               v
       one Usage service leader
               |
               +-- usage/usage.sqlite       hot events + all-time rollups
               +-- usage/archive/YYYY/MM/   daily Parquet
               `-- logs/model-usage/        legacy JSONL import source
```

모든 프로세스는 같은 `Ensure` 경로를 사용한다. endpoint health가 없으면
`usage-service.lock` 획득자가 listener와 SQLite를 열어 leader가 되고, 나머지는
compatible protocol을 확인한 follower client가 된다. SQLite는 leader 한 프로세스만
연다. `q usage`는 leader가 없으면 직접 leader가 되어 Ctrl-C까지 유지하며, 이미 leader가
있으면 dashboard를 열고 service를 계속 감시하다가 takeover할 수 있다.

일반 model call의 recording 경로는 service startup 실패, timeout 또는 HTTP/SQLite
오류를 호출자에게 전파하지 않는다. 각 기록에는 client가 만든 stable event ID가 있어
응답 손실 뒤 재시도해도 중복 삽입되지 않는다. 기록 요청 deadline은 짧고 bounded하다.

## 4. 이벤트와 role 계약

`usage_events`의 논리 필드는 다음과 같다.

| 필드 | 의미 |
| --- | --- |
| `event_id` | retry-safe한 128-bit random ID |
| `occurred_at_ms` | 응답을 관측한 UTC Unix millisecond |
| `model` | 응답 model, 없으면 요청 model |
| `role` | 호출 context의 Q role, 없으면 `unknown` |
| `prompt_tokens` | 입력 token |
| `completion_tokens` | 출력 token |
| `total_tokens` | 전체 token |
| `cached_tokens` | provider가 보고한 cache read token |
| `cache_write_tokens` | provider가 보고한 cache write token |
| `estimated` | 기본 token count 일부를 로컬 추정했는지 여부 |
| `cache_estimated` | cache breakdown이 없었는지 여부 |

role은 model client를 복제하지 않고 request context에 붙인다. 기본값은 `unknown`이며,
공통 orchestration 경계에서 `main`, `planner`, `griller`, `scout`, `coder`, `thinker`,
`librarian`, `commit`, `embedding`처럼 bounded label을 지정한다. 임의 사용자 입력을 role로
저장하지 않는다.

`usage_daily`는 UTC day, model, role을 key로 calls와 각 token 합계,
estimated/cache-estimated 호출 수를 유지한다. event 삽입과 rollup upsert는 같은 SQLite
transaction에서 수행한다.

## 5. SQLite durability와 migration

SQLite는 WAL, foreign keys, busy timeout을 켜고 process 내부 connection 수를 1로
제한한다. schema version은 `PRAGMA user_version`으로 관리한다. database와 archive
directory는 user-only 권한으로 생성한다.

기존 `~/.q/logs/model-usage/usage-*.jsonl`은 startup에서 transaction 단위로 가져온다.
legacy row에는 event ID와 role이 없으므로 source file identity, line number와 row
payload의 SHA-256으로 deterministic ID를 만들고 role은 `unknown`으로 기록한다.
`usage_imports` manifest가 source file metadata와 imported row count를 보존하므로 재시작
또는 여러 binary가 같은 파일을 만나도 중복되지 않는다. 첫 milestone에서는 import가
성공해도 legacy 파일을 자동 삭제하지 않는다.

## 6. Parquet archive와 crash recovery

archive cutoff보다 오래된 complete UTC day를 한 파일로 쓴다.

```text
usage/archive/2026/06/usage-2026-06-12.parquet
```

절차:

1. SQLite snapshot에서 해당 day의 raw rows를 event ID 순으로 읽는다.
2. 같은 directory의 temporary file에 Parquet를 쓰고 close/sync한다.
3. Parquet를 다시 읽어 row count와 token 합계를 검증한다.
4. final path로 atomic replace한다.
5. SQLite transaction에서 `usage_archives` manifest를 upsert하고 해당 raw rows를 지운다.

final file 작성 후 DB commit 전에 종료되면 다음 run이 기존 file을 검증하고 5단계를
완료한다. DB commit 후에는 manifest가 해당 day의 checksum, row count와 token 합계를
소유한다. 이미 다른 manifest와 충돌하는 파일은 덮어쓰거나 raw data를 삭제하지 않고
오류로 남긴다. rollup은 archive 후에도 삭제하지 않는다.

archive는 startup과 6시간 주기의 maintenance pass에서 시도한다. 실패는 기록과 dashboard를
막지 않으며 다음 maintenance pass에서 다시 시도한다.

## 7. HTTP와 dashboard 계약

서비스는 loopback IP에만 bind하고 request `Host`를 실제 listener host/port로 제한한다.
원격 listener, wildcard bind와 cross-origin browser mutation은 허용하지 않는다. URL,
HTML, log에 secret을 넣지 않는다. append body에는 작은 고정 상한과 strict JSON decode를
적용한다.

주요 route:

- `GET /v1/health`: service/protocol compatibility.
- `POST /v1/events`: 내부 token event의 idempotent append.
- `GET /api/v1/usage`: range, model, role filter가 적용된 summary/series/table.
- `GET /openapi.json`: 위 API의 OpenAPI 3 계약.
- `GET /`: embedded dashboard.

dashboard는 24h, 7d, 30d, 90d range와 model/role filter를 제공하고 다음을 표시한다.

- total tokens, API calls, cache-read 비율, estimated 호출 비율.
- prompt/completion/cached token time series.
- role별 token strip.
- model별 calls와 token breakdown table.

화면이 보이는 동안 dashboard는 5초마다 현재 filter로 usage를 다시 조회한다. 진행 중인
요청은 다음 조회 전에 취소해 느린 응답이 쌓이거나 최신 filter 결과를 덮어쓰지 않게 한다.
숨겨졌던 화면이 다시 보이면 즉시 한 번 갱신한다.

UI는 Q binary 안에 embedded된 `html/template`, CSS와 dependency-free JavaScript로
구성한다. React build chain이나 외부 CDN은 추가하지 않는다. read-only 화면이며 HTML
escape, CSP, `nosniff`, `no-store` header를 적용한다.

## 8. 검증과 acceptance

필수 검증:

- token normalization, stable event ID와 context role propagation unit test.
- SQLite initialize/reopen, concurrent append, idempotent retry, rollup transaction test.
- legacy JSONL partial/corrupt/import replay test.
- Parquet write/read 검증, archive replay와 file/DB crash-window recovery test.
- leader/follower election, incompatible port, shutdown/takeover lifecycle test.
- HTTP strict decode, body limit, Host/Origin boundary와 query validation test.
- dashboard empty/data/filter/mobile interaction browser test.
- built binary `q usage` health/dashboard smoke.
- reproducible SQLite storage/query benchmark and full repository regression.

Acceptance criteria:

1. 하나의 user-level leader만 SQLite를 열고 다른 Q process는 같은 API로 기록한다.
2. recording failure나 250 ms 이내 timeout이 model call 결과를 바꾸지 않는다.
3. 같은 event를 retry해도 raw row와 daily rollup이 한 번만 증가한다.
4. 90일 이전 complete day는 검증된 Parquet가 생긴 뒤에만 SQLite raw에서 제거된다.
5. archive 후에도 all-time dashboard aggregate가 변하지 않는다.
6. 기존 JSONL은 삭제 없이 idempotent하게 import된다.
7. dashboard와 OpenAPI는 prompt, endpoint, credential 또는 비용을 노출하지 않는다.
8. 160K-token 호출도 token integer를 가진 event 한 행으로 저장되며 payload 크기가
   token 수에 비례하지 않는다.

## 9. 구현 검증 기록

2026-09-11 구현에서는 별도 code generation이 필요한 `templ` 대신 Go 표준
`html/template`를 선택했다. 정적 asset과 함께 binary에 embed할 수 있고 현재 화면에는
반복되는 server-side component가 없어 추가 build tool의 이득이 작기 때문이다.

- desktop 1440×1000과 mobile 390×844에서 실제 embedded dashboard를 렌더링했다.
- 24h/7d 전환, model filter, role 조합 filter와 mobile table 가로 scroll을 조작했다.
- mobile document 자체의 가로 overflow는 없고 table wrapper만 의도대로 scroll된다.
- browser console warning/error는 0건이었다.
- 생성한 visual concept의 dark graphite hierarchy, metric band, stacked traffic chart,
  role strip와 model ledger를 유지했다. 구현에서는 privacy 설명과 `All` range를 추가하고,
  장식적 search/pagination은 실제 데이터 규모에 필요하지 않아 제외했다. refresh는 새
  model 호출을 열린 화면에 반영하기 위해 5초 자동 주기로 제공한다.
- `BenchmarkSQLiteUsageAggregate`가 187,000행의 DB byte/row와 aggregate 시간을 함께
  출력하며, 160K-token single-call test는 한 event만 생기는 것을 검증한다.

## 10. Rollback

writer를 다시 process-local JSONL로 되돌려도 기존 SQLite와 Parquet는 삭제하지 않는다.
새 binary가 기록한 usage database는 diagnostic projection이므로 core chat/session
recovery를 막지 않는다. rollback 이후 재도입 시 stable schema/version과 archive
manifest에서 계속할 수 있어야 한다.
