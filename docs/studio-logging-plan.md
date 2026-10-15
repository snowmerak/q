# Q Studio 진단 로그와 요청 추적

갱신일: 2026-10-16

상태: 구현됨. 이 문서는 Studio 로그의 설정, 요청 연관 관계, 회전·보존과 실패
정책을 소유한다. 대상 독자는 로컬 장애를 조사하는 사용자와 구현을 변경하는
개발자다. 정책·설정·이벤트·수명이 바뀌면 코드와 함께 갱신한다.

`observability-design`, `observability-tooling`, `code-implementation`, `go-tooling`의
관련 지침을 적용했다. 이번 범위는 로컬 진단과 `request_id` 전파다. OTel SDK,
span·metrics·OTLP exporter는 추가하지 않는다. 향후 OTel을 연결해도 `request_id`는
애플리케이션 조회 키이며 표준 `trace_id`·`span_id`와 같은 값으로 해석하지 않는다.

## 출력과 설정

[`diagnostics`](../diagnostics/runtime.go)의 logger를 Studio 실행 경로에서 만들어
주입한다. 전역 logger는 바꾸지 않는다. bare `q`, `q acp`, `q-mcp`와 독립 서비스의
프로토콜 stdout에는 적용하지 않는다. embedded `studio.NewServer`도 logger를
주지 않으면 무출력이다.

| 항목 | 기본값 |
| --- | --- |
| 저장 위치 | `~/.q/logs/studio/` |
| 최소 레벨 | INFO |
| stdout 진단 이벤트 | **끔** |
| 파일 저장 | 켬 |
| 파일당 최대 크기 | **25 MiB = 26,214,400바이트** |
| Studio 로그 합계 | **1 GB = 1,000,000,000바이트** |
| 보존 기간 | **7일** |
| 무기록 상태의 정리 주기 | 60초 |
| 최대 레코드 크기 | 인코딩·개행을 포함해 32 KiB |

파일은 JSONL이다. stdout을 켜면 같은 JSONL 이벤트를 출력한다. 같은 이벤트의
`event_id`는 두 sink에서 동일하다. 외부 collector는 한 출력을 수집하고, 둘 다
수집해야 하면 `event_id`로 중복을 제거한다.

`stdout: false`는 진단 이벤트에 적용한다. Studio/Gateway 접속 주소, CLI 도움말과
오류 안내는 별도 명령 출력이다. sink 장애의 안전한 요약은 stderr에 보고한다.
개인 설정 경로는 [`config.DefaultStore`](../config/config.go)를 따르며 현재
디렉터리나 저장소 `.q` 아래에 진단 파일을 만들지 않는다.

기존 `~/.q/config.yaml`에 다음 선택 필드를 넣는다.

```yaml
logging:
  level: info
  stdout: false
  file_enabled: true
  max_file_mib: 25
  max_total_bytes: 1000000000
  max_age_days: 7
```

[`config.LoggingConfig`](../config/logging.go)가 기본값과 검증을 소유한다.
생략한 필드는 기본값을 적용하고 명시적인 boolean `false`는 보존한다. 레벨은
`debug`, `info`, `warn`, `error`다. 크기·기간의 0은 기본값이며 무제한이 아니다.
음수, 단위 변환 overflow와 파일 크기보다 작은 총량은 거부한다.

시작 시 logging의 레벨·범위가 잘못됐으면 기본 정책으로 진단을 구성하고
`studio.logging.config_fallback`을 남긴다. 설정이 없어도 기본값으로 시작한다.
기존 애플리케이션 설정의 문법·타입·기타 오류 처리 계약은 유지한다. 실행 중
hot reload나 로그용 CLI override·Settings 편집 UI는 추가하지 않았다.

load/save는 logging 값을 보존하고 전역 설정 transfer의 `runtime/logging`으로
export/import한다. 변경한 설정은 다음 Studio 시작부터 적용한다.

## request_id의 수명과 전파

[`diagnostics/context.go`](../diagnostics/context.go)가 요청 ID와 logger를 context에
담는다. 요청 ID는 인증·권한·작업 복구 명령을 의미하지 않는다.

1. Studio HTTP 경계에서 `X-Request-ID`가 정규 UUID 형식이면 채택하고 소문자
   표준형으로 정규화한다. header가 없으면 context의 기존 요청 ID를 유지하고,
   둘 다 없으면 임의 UUID를 생성한다. 잘못된 header 값은 새 UUID로 교체하고
   원본 값은 기록하지 않는다. 선택한 ID는 응답 `X-Request-ID`로 반환한다.
2. 메시지 제출 시 실행 스냅샷에 요청 ID를 저장한다. 브라우저 context의 취소
   수명은 가져오지 않고 서버 context에 진단 값만 복사한다.
3. 실행 logger는 `request_id`, `session_id`, `run_id`를 갖는다. 도구·모델·중첩
   서브에이전트는 같은 context 계보를 사용한다. 자식은 `task_id`, `parent_id`,
   `agent`, `call_id`로 구분한다.
4. 저장된 자식 위임 상태와 실행 이벤트 NDJSON에도 요청 ID를 남긴다. 현재
   요청 ID가 없는 자식 복구 경로는 저장된 ID를 사용할 수 있다. 명시적인 새
   요청이 있으면 현재 ID를 우선하며, 기존 ID 없는 기록도 계속 읽는다.
5. 독립적인 자식 후속 요청인 `StartDelegation`도 진단 값을 서버 소유 context에
   복사한다. 방향 안내로 이어진 같은 루트 작업은 기존 요청 ID를 유지한다.
6. 상주 세션의 다음 턴은 새 요청 ID를 사용한다. 이전 턴 ID가 다음 턴으로
   흘러가지 않도록 요청 단위 context를 사용한다.
7. 카운슬도 HTTP 요청의 진단 값을 서버 소유 context에 복사한다. 의견 수집,
   라운드별 상호 검토, 의장 종합과 참여자 도구 실행에 같은 ID를 전파한다.
   HTTP 밖에서 직접 실행하면 context와 저장된 턴의 요청 ID를 순서대로 확인하고,
   요청 ID가 둘 다 없으면 UUID를 생성한다. `Turn.request_id`에도 저장한다.
8. 카운슬 재시도는 재시도 요청의 ID를 사용한다. resume은 같은 `turn_id`의
   최신 `request_id`를 갱신하고, rerun은 새 턴을 만들고 `rerun_of`를 보존한다.
   재시도 로그의 `previous_request_id`, `source_turn_id`로 이전 시도를 연결한다.
   취소 로그에는 취소 요청의 ID와 대상 실행의 `run_request_id`를 함께 남긴다.

외부 ACP 에이전트는 호스트 invocation의 시작·종료 로그를 연결한다. 외부
프로그램 자체 로그를 수집하거나 표준 프로토콜 밖으로 요청 ID를 주입하지 않는다.
진단 파일을 삭제해도 세션·실행·위임 데이터는 삭제하지 않는다.

## 이벤트와 민감정보

각 이벤트에는 UTC `time`, `level`, `msg`, 안정적인 `event`, `schema_version: 1`,
프로세스별 `instance_id`, 개별 `event_id`가 있다. 요청 ID는 요청을 원인으로 하는
실행에만 붙인다.

| 경로 | 기록 |
| --- | --- |
| 서버 | `studio.started/ready/stopped`: INFO. 시작 시 적용한 한도와 stdout 여부도 포함한다. |
| HTTP | `studio.request.completed`: 정상·예상된 4xx는 DEBUG, 서버 5xx는 ERROR. 메서드·route template·상태·`duration_ms`를 기록한다. |
| 루트 실행 | `studio.run.started/finished`: INFO, 실행 실패는 ERROR. 사용자 취소는 정상 상태다. |
| 도구·제어 | 성공 도구 호출·완료는 DEBUG, 도구 오류는 WARN. 질문·일시 정지·재개는 INFO. 본문은 제외한다. |
| 서브에이전트 | `studio.subagent.started/finished`, `studio.invocation.started/finished`: 요청·작업·호출 식별자와 소요 시간·결과를 기록한다. |
| 카운슬 턴 | `council.turn.queued/retry_queued/started/finished/cancel_requested`: INFO, 실행 실패는 ERROR. `council_id`, `turn_id`, 상태·소요 시간과 재시도 연결 정보를 기록한다. |
| 카운슬 단계·참여자 | `council.stage.started/finished`, `council.seat.started/finished`: INFO, 실패는 ERROR, 사용자 취소는 INFO. `stage`, `round`, 참여자 `seat`, `role`, `session_id`, `operation_id`, `model`·`agent`를 기록한다. 완료 작업을 재사용하는 `council.seat.skipped`는 DEBUG다. |
| 카운슬 도구·저장 | `council.tool.started/finished`: DEBUG, 도구 오류는 WARN. `council.persistence.failed`, `council.lock.release_failed`: ERROR. `council.session.release_failed`: WARN. 본문·원본 오류 문자열은 제외한다. |
| 저장 | `studio.persistence.failed`: ERROR. 이벤트·종료 저장 실패를 안전한 속성으로 구분한다. |
| Gateway | 시작·정상 중단·설정 변경 재시작은 INFO, 사용 불가는 WARN. 기존 명령 출력은 별도다. |

기본 INFO에서는 폴링·정적 asset마다 성공 로그를 쌓지 않는다. DEBUG로 켜면
해당 요청도 요청 ID·route 필드로 확인한다.

enqueue 전에 두 sink에 공통 정규화를 적용한다. 요청·응답 body, query,
인증 header, 대화, 숨은 추론, 도구 인자·결과, 파일 내용과 설정 전체를 속성에
넣지 않는다. 매칭되지 않은 HTTP 경로는 `unmatched`로 기록한다. 원본 오류
문자열은 저장하지 않고 `error_type`으로 바꾸며 원본 `detail`은 제외한다.
net/http의 원본 panic·연결 오류 dump도 안전한 이벤트로 치환한다. 요청 panic은
요청 ID를 기록한 뒤 기존 panic 처리를 이어간다.

문자열 속성은 최대 2 KiB다. 초과 시 UTF-8 경계를 지켜 줄이고 해당 속성에
`truncated`, `omitted_bytes`를 표시한다. 인코딩 후 32 KiB를 넘으면 JSON을
잘라 저장하지 않고 drop 집계에 포함한다. 각 경로도 안전한 속성만 전달해야
하며 공통 필터가 임의의 secret을 모두 찾아낸다고 가정하지 않는다.

## 회전과 정리

[`diagnostics/files.go`](../diagnostics/files.go)가 다음 파일만 관리한다.

```text
~/.q/logs/studio/
  .retention.lock
  studio-<UTC 시각>-<instance_id: 32자리 hex>-<sequence>.jsonl
```

매 시작 새로운 instance ID를 사용하고 이전 실행의 파일에 이어 쓰지 않는다.
다음 완전한 레코드가 파일 상한을 넘거나 UTC 날짜가 바뀌면 새 파일을 만든다.
기록이 없는데 빈 파일을 만들지 않는다.

시작·기록·60초 주기에 정리한다. batch 단위로 기존
[`worklock.AcquireFile`](../worklock/lock.go)의 OS lock을 잡고, 경합 재시도는 총
100ms로 제한한다. 파일 handle은 lock 해제 전에 닫는다. 다른 프로세스가 자신의
이전 파일을 지웠으면 다음 기록에서 새 파일을 시작한다.

1. 생성 규칙에 맞는 일반 파일만 열거한다. symlink·하위 디렉터리·다른 이름은 제외한다.
2. `mtime`이 현재 시각에서 보존 기간을 뺀 값보다 **이전**이면 지운다. 정확한
   기간 경계는 보존한다. 낮춘 파일 크기 상한을 초과하는 기존 파일도 삭제한다.
3. 다음 레코드를 합쳐 총량을 넘으면 오래된 파일부터 지운다. `mtime` 동률은
   이름 순서로 결정하고 이번에 이어 쓸 파일은 보존한다.
4. 예산 확보 후 append한다. 부분 쓰기 오류는 원래 길이로 복구를 시도하고 해당
   파일에는 계속 덧붙이지 않는다.

각 프로세스는 시작 시 읽은 정책을 사용한다. 동시에 다른 정책으로 실행하면
작은 용량·짧은 기간을 적용하는 프로세스가 로그를 먼저 지울 수 있다.

총량은 관리 대상 JSONL 파일의 논리 길이 합이다. lock 파일·filesystem 할당
단위·메모리 큐는 제외한다. 기존 Thinker의 3일 정책, legacy model-usage, 사용량
DB와 세션 NDJSON은 집계·삭제하지 않는다. **1 GB는 `~/.q`나 `logs` 전체 한도가 아니다.**

삭제 실패로 예산을 확보하지 못하면 새 기록을 drop한다. 권한·외부 쓰기 때문에
남은 기존 초과분까지 항상 지울 수 있다고 보장하지 않는다. Studio가 꺼져 있을
때는 별도 정리 daemon이 없고 다음 시작 때 정리한다. POSIX에서는 디렉터리
`0700`·파일 `0600`을 사용한다. Windows ACL은 개인 설정 디렉터리 정책을 따른다.

## 장애 격리와 종료

파일과 선택적 stdout은 각각 256개 레코드의 독립 유한 큐와 worker를 갖는다.
큐마다 payload 기준 최대 8 MiB이며 객체 등의 부가 메모리는 별도다. enqueue는
비차단이고 가득 차면 새 레코드를 drop한다. 파일은 대기 레코드를 최대 64개씩
묶어 처리한다.

파일 실패가 요청·에이전트 작업을 실패시키지 않는다. 실패한 batch의 손실을
보수적으로 집계하고 다음 batch와 주기 정리에서 재시도한다. 느린 stdout은
파일 worker를 막지 않는다. 장애 요약은 재귀 logger 호출 없이 stderr에 최대
분당 1회 기록하고 원본 filesystem 오류·민감한 경로를 출력하지 않는다.

HTTP·실행·Gateway를 정리하고 종료 이벤트를 넣은 뒤 큐를 닫는다. drain은 최대
2초다. timeout·drop·sink 실패는 안전한 종료 요약으로 보고하며 stdout은 닫지
않는다. crash·강제 종료·disk-full에서는 큐의 이벤트와 마지막 행이 유실될 수
있다. 매 이벤트 fsync나 무손실 감사 보장은 없으며 작업 복구는 기존 저장소를 쓴다.

## 검증

- 파일 크기 경계, 전체 초과의 삭제 순서와 낮춘 한도 적용.
- 보존 기간 초과·정확한 경계와 다른 로그 디렉터리 보호.
- 실제 두 프로세스가 같은 디렉터리의 파일 회전·총량 예산을 공유하는 검증.
- 파일 오류의 stdout 격리, 막힌 stdout·큐 초과·종료 deadline 검증.
- stdout 기본 끔·opt-in의 동일 event ID와 민감한 오류·본문 제외.
- HTTP 취소 후 ID 유지, 실행 스냅샷·NDJSON 재조회, 중첩 자식의 모델 context·
  로그·위임 상태에서 같은 ID 확인과 상주 세션 다음 턴의 ID 교체.
- UUID header 채택·정규화, 누락·잘못된 값의 UUID 생성, context ID 유지.
- 카운슬 HTTP·직접 실행의 ID 생성·전파·저장, 브라우저 취소 후 실행 유지,
  모든 단계·참여자·도구 로그의 식별자와 본문 제외, 실패·취소·resume·rerun 연결.
- 설정 기본값·범위·load/save·transfer 왕복과 임시 HOME의 Studio 기동·종료 파일 확인.

완료 검증은 Windows ARM64, Go 1.27.2의 현재 작업 트리 기준이다.

- `go test ./diagnostics ./config ./council ./studio ./cmd/q ./app ./subagent ./workspace`: 통과.
- `go build ./...`, 관련 8개 package의 `go vet`, `go mod tidy -diff`: 통과.
- golangci-lint 2.14.0의 기본 검사(`errcheck`, `govet`, `ineffassign`, `staticcheck`,
  `unused`)에서 변경분 경고 0건. 전체 검사에는 기존 `cmd/q/studio_test.go`의
  응답 body Close 반환값 경고 2건이 남아 있다.
- 문서의 로컬 링크와 `git diff --check`: 통과.
- race detector는 `windows/arm64`에서 지원되지 않아 실행하지 못했다.
  `govulncheck`는 설치되어 있지 않아 실행하지 않았다.
