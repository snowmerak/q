# Q 구조 리팩터링 로드맵

작성일: 2026-09-25

상태: M0~M6을 구현하고 Windows 및 WSL Linux ARM64 로컬 검증을 마쳤다.
macOS 환경 검증과 코드 리뷰는 남아 있다. 현재 동작의 계약은 README, 기능 문서와 코드가 소유한다.

대상 독자: Q의 TUI, ACP, Agent Loop, 설정 저장소와 런타임 생명주기를
변경하는 구현자와 리뷰어.

갱신 조건: 마일스톤의 범위, 패키지 경계, 공개 API 호환성, 검증 기준 또는 구현 순서가
달라질 때 이 문서를 갱신한다. 각 마일스톤을 완료할 때 실제 변경, 검증 결과, 계획과의
차이 및 남은 작업을 기록한다.

문서 관계: [embedded-agent-loop-public-api-plan.md](embedded-agent-loop-public-api-plan.md)는
`app` 패키지에서 Agent Loop를 처음 공개한 완료 기록이다. 그 마일스톤에서 제외했던 새
`agentloop` 패키지 분리를 이 로드맵의 M4가 후속 작업으로 다뤘다. 기존 `app` 경로는 호환
facade로 유지하며, 새 임베딩 경로는 [agent-loop-embedding.md](agent-loop-embedding.md)가 설명한다.

## 1. 목적

현재 Q의 동작과 저장 형식을 유지하면서 다음 구조적 비용을 줄인다.

1. 여러 실행 진입점에 반복된 서비스 시작과 종료 흐름을 한 소유자로 모은다.
2. 운영체제별 파일 교체와 API key 암호화 같은 안정된 공통 의미를 한 구현으로 만든다.
3. 공개 Agent Loop를 TUI와 ACP host 상태로부터 분리해 경량 임베딩 경계를 제공한다.
4. 하나의 `model`에 모인 화면 상태와 이벤트 처리를 화면 또는 기능 단위로 나눈다.
5. 각 단계가 독립적으로 검증되고 되돌려질 수 있도록 변경 크기를 제한한다.

이 작업은 프로세스나 저장소를 새 서비스로 분할하는 프로젝트가 아니다. Q는 하나의
저장소와 배포 단위를 유지하며, 패키지는 책임과 의존 방향을 드러내는 내부 경계로
사용한다.

## 2. 시작 상태와 근거

2026-09-25 기준 정적 조사 결과는 다음과 같다.

| 항목 | 현재 상태 |
| --- | --- |
| `app` 프로덕션 코드 | 58개 파일, 약 23,800줄 |
| `app`의 내부 Q 패키지 의존 | 24개 |
| `model` | 186개 필드, 약 330개 메서드 |
| `model.Update` | 약 880줄 |
| `RunAgentLoop` | 약 350줄이며 `app/model.go`에 위치 |
| 플랫폼별 `replaceFile` | 11개 패키지, 22개 운영체제별 파일 |
| 런타임 시작 경로 | TUI와 ACP 등에 Library, Workspace Memory, Provider 시작·종료 반복 |
| API key 구현 | Gateway와 System One 설정에 생성, hash, 검증, revoke 흐름이 평행 구현 |

최근 150개 commit에서 `app/model.go`는 71회, `app/acp.go`는 45회 변경됐다. 크기만이
아니라 변경 결합도도 높은 영역이다.

기준선으로 실행한 `go test ./...`에서는 다음 두 테스트가 한 번씩 실패했다.

- `TestACPAgentCancelsActiveScoutToolCards`: 테스트 종료 시 Loom 디렉터리 정리 경쟁.
- standalone service smoke test: Library와 Workspace Memory의 고정 포트 충돌.

두 테스트는 각각 단독 실행하면 통과했다. 기능 회귀보다 수명주기 종료와 테스트 격리의
간헐 실패로 판단하며, 대규모 구조 변경 전에 이를 안정화한다.

## 3. 목표 경계와 의존 방향

목표 의존 방향은 다음과 같다.

```text
cmd/q
  └─ app host adapters (TUI, ACP)
       ├─ agentloop
       │    ├─ client
       │    ├─ memory
       │    ├─ subagent
       │    ├─ tools contracts
       │    └─ workspace contracts
       └─ internal/hostruntime
            ├─ providerhost
            ├─ library
            ├─ workspacememory
            └─ usagelog

gatewayconfig   ─┐
systemoneconfig ─┴─ internal/authkey

config, workspace, sessionstore, ... ── internal/fsreplace
```

### `internal/fsreplace`

운영체제별 replace 동작과 Windows 재시도 정책을 소유한다. 각 저장소 패키지는 임시 파일
생성, 직렬화, 권한과 오류 문맥을 계속 소유한다. 첫 마일스톤에서는 atomic write 전체를
일반화하지 않는다.

### `internal/hostruntime`

Library, Workspace Memory, Provider Manager와 Usage Recorder의 시작, 부분 초기화 실패
정리, 취소, 대기 및 종료 순서를 소유한다. Workspace session, 화면 상태와 Agent Loop는
소유하지 않는다.

### `internal/authkey`

key 생성, parsing, domain-separated keyed hash와 constant-time 비교 같은 암호화 primitive를
소유한다. Gateway와 System One 패키지는 prefix, domain, 인증 활성화 정책, 설정 파일과 사용자
오류 문맥을 계속 소유한다.

### `agentloop`

Agent Loop 요청, 이벤트, 결과, context 압축, 스트리밍, orchestration, skill hint와 tool
round를 소유한다. Bubble Tea, ACP SDK, Provider Manager, Library 서버와 전역 config에
의존하지 않는다. 기존 `app` 공개 API에는 호환 facade를 둔다.

### `app`

TUI와 ACP host adapter를 소유한다. Agent Loop 이벤트를 각 transport와 화면에
투영하고 session/archive 저장을 조정한다. 화면별 상태는 먼저 `app` 내부 구조체로 묶고,
독립성이 확인된 화면만 후속 패키지 이동 대상으로 삼는다.

## 4. 전역 불변조건

모든 마일스톤은 다음 조건을 지킨다.

- ACP wire message, tool lifecycle과 `write_file`/`edit_file` diff 동작을 유지한다.
- Session Store, workspace session, Gateway, System One 및 MCP 설정 파일 형식을 변경하지 않는다.
- Gateway와 System One key의 prefix, hash domain과 master key 파일을 분리해 유지한다.
- Agent Loop의 실행 구현은 저장소에 하나만 존재한다.
- 공개 Agent Loop 호출자가 새 패키지로 점진적으로 이동할 수 있도록 기존 `app` 경로를
  호환 facade로 유지한다.
- `agentloop` 생성은 Gateway, Library, Workspace Memory 또는 TUI를 암묵적으로 시작하지
  않는다.
- 각 마일스톤은 데이터 migration 없이 이전 commit으로 되돌릴 수 있어야 한다.

## 5. 마일스톤

### M0. 기준선과 테스트 격리

목표: 구조 변경의 실패와 기존 간헐 실패를 구분할 수 있는 검증 기준선을 만든다.

작업:

1. Library와 Workspace Memory를 시작하는 테스트가 임의 포트 또는 테스트별 설정을
   사용하도록 한다.
2. ACP test host가 생성한 background 작업과 Loom 사용자가 종료 전에 모두 완료되도록
   소유권과 대기 지점을 명시한다.
3. 서비스 시작 실패, context 취소와 `Close`의 idempotency를 focused test로 고정한다.
4. 전체 테스트에서 사용하는 실제 사용자 설정과 고정 포트 의존을 제거한다.

완료 조건:

- 위 두 간헐 실패 테스트가 반복 실행에서 안정적으로 통과한다.
- `go test ./... -count=1`이 다른 Q 프로세스나 사용자 설정에 의존하지 않고 통과한다.
- 실패한 부분 초기화가 goroutine, listener, lock 또는 임시 디렉터리를 남기지 않는다.

예상: 1~2일, PR 1개.

### M1. 플랫폼 파일 교체 통합

목표: 11개 패키지의 운영체제별 파일 교체 구현을 한 내부 패키지로 통합한다.

작업:

1. `internal/fsreplace`와 `Replace(source, destination string) error`를 추가한다.
2. Windows `MoveFileExW`, Unix `os.Rename` 및 현재 workspace 재시도 정책을 한곳에 둔다.
3. 기존 저장소 패키지를 차례로 전환하고 패키지별 `replace_*.go`를 제거한다.
4. 파일 mode, temporary file 정리와 오류 wrapping이 기존 계약을 유지하는지 확인한다.

완료 조건:

- 운영체제별 replace 구현이 `internal/fsreplace`에 하나씩만 존재한다.
- destination 존재 여부, Windows transient failure와 영구 오류가 테스트된다.
- 설정 및 session 저장 테스트가 모두 통과한다.

예상: 0.5~1일, PR 1개.

### M2. 공통 host runtime 수명주기

목표: TUI와 ACP가 같은 서비스 수명주기 구현을 사용하게 한다.

작업:

1. `internal/hostruntime`에 options, 시작 결과와 idempotent `Close`를 정의한다.
2. Library, Workspace Memory, Provider Manager와 Usage Recorder의 생성 및 종료 순서를
   옮긴다.
3. 시작 중간 단계의 실패가 이미 생성된 리소스를 역순으로 정리하게 한다.
4. `app.Run`, `openACPHost`와 관련 standalone 경로를 전환한다.
5. host별로 필요한 client, model 목록과 서비스 endpoint만 명시적으로 노출한다.

완료 조건:

- Library와 Workspace Memory를 직접 시작하는 application 경로가 한 lifecycle 구현을
  통한다.
- 각 host의 취소와 종료가 bounded하며 `Close`를 여러 번 호출해도 안전하다.
- 부분 초기화 실패와 정상 종료 모두 listener, goroutine, client와 recorder를 남기지 않는다.
- TUI, ACP focused test와 전체 테스트가 통과한다.

예상: 2~4일, PR 1~2개.

### M3. API key 공통 primitive

목표: Gateway와 System One의 인증 키 암호화와 lifecycle 중복을 제거한다.

작업:

1. `internal/authkey`에 key policy, record, generate, parse, hash, verify와 revoke primitive를
   정의한다.
2. Gateway와 System One 설정 타입은 JSON 호환을 유지하며 공통 record를 alias 또는 명시적
   변환으로 사용한다.
3. 각 서비스의 prefix와 domain separator를 policy로 고정한다.
4. master key 생성과 private file 저장의 공통 부분을 추출한다.
5. authenticator의 서비스별 활성화 및 HTTP 오류 정책은 기존 패키지에 둔다.

완료 조건:

- key 생성, hash와 constant-time 검증 구현이 한곳에 존재한다.
- Gateway key와 System One key는 서로의 서비스에서 인증되지 않는다.
- 기존 설정 파일을 그대로 읽고 쓸 수 있다.
- secret은 생성 반환값 이외의 파일, log와 오류에 나타나지 않는다.

예상: 1~2일, PR 1개.

M1과 M3는 M0 이후 서로 독립적으로 진행할 수 있다.

### M4. 공개 Agent Loop 패키지 분리

목표: 임베딩 사용자가 Bubble Tea와 ACP host 구현을 함께 의존하지 않도록 한다.

작업:

1. `agentloop` 패키지에 공개 request, result, event와 최소 client/tool 계약을 추가한다.
2. 현재 `RunAgentLoop` 본문과 context, stream, orchestration, skill hint 관련 구현을 이동한다.
3. TUI, ACP, Search/Web Tester 호출자를 새 패키지로 전환한다.
4. `app.RunAgentLoop`, 관련 타입과 helper는 alias 또는 forwarding facade로 유지한다.
5. 외부 `agentloop_test` 패키지에서 workspace 준비, tool round, 질문, compaction과 streaming을
   공개 API만으로 검증한다.

완료 조건:

- `agentloop`는 `app`, Bubble Tea, ACP SDK와 provider process lifecycle을 import하지 않는다.
- TUI와 ACP가 동일한 Agent Loop 구현을 사용한다.
- 기존 `app` 공개 호출자와 새 `agentloop` 호출자가 모두 컴파일되고 같은 결과를 얻는다.
- [embedded-agent-loop-public-api-plan.md](embedded-agent-loop-public-api-plan.md)의 기존 공개
  계약과 제한을 유지한다.

예상: 3~6일, PR 2개.

### M5. TUI model 상태와 reducer 분해

목표: `model`의 평면 상태와 880줄 `Update`를 공유 상태, 화면 상태와 chat event reducer로
분해한다.

작업:

1. session, chat, provider/model 설정, Gateway, Skills, LSP, MCP와 custom subagent 상태를
   명명된 하위 상태 구조체로 묶는다.
2. 화면별 key update와 view 함수를 같은 기능 파일의 controller 또는 reducer로 옮긴다.
3. Agent Loop event 처리를 화면 입력 처리에서 분리한다.
4. 최상위 `Update`는 전역 lifecycle event와 현재 화면 dispatch를 담당하게 한다.
5. Bubble Tea의 value copy 의미를 유지하도록 mutable pointer, channel과 cancel function의
   소유권을 테스트한다.

완료 조건:

- 최상위 `model`에는 공유 의존성과 명명된 하위 상태만 남으며 화면별 primitive 필드가
  평면으로 늘어나지 않는다.
- 최상위 `Update`는 화면 구현 세부사항 대신 명시적인 dispatch를 수행한다.
- 화면 이동, 입력 focus, resize, 취소, 질문 응답, session 저장과 chat 완료 회귀 테스트가
  통과한다.
- 기능별 후속 변경이 대부분 해당 상태와 controller 파일 안에서 끝난다.

예상: 4~8일, 화면군별 PR 3~5개.

### M6. 문서와 경계 정리

목표: 구현된 패키지 경계와 실제 검증 결과를 유지되는 문서에 반영한다.

작업:

1. 이 문서에 마일스톤별 구현 결과, 차이와 검증 evidence를 기록한다.
2. `docs/architecture.drawio`와 `docs/architecture.svg`에 `agentloop`와 host runtime 경계를
   반영한다.
3. Agent Loop 임베딩 문서의 import 경로와 호환 facade를 갱신한다.
4. 사용되지 않는 compatibility helper는 공개 호환 기간을 확인한 뒤 별도 변경으로
   정리한다.

완료 조건:

- 코드, README의 링크, Agent Loop 문서와 architecture diagram이 같은 경계를 설명한다.
- 계획 상태가 실제 완료 상태와 남은 제한을 구분한다.
- 전체 검증 결과와 실행하지 못한 외부 검증이 기록된다.

예상: 1일, PR 1개 또는 M4/M5의 마지막 PR에 포함.

## 6. 순서와 전달 단위

```text
M0 기준선
 ├─ M1 fsreplace ───────────────────────────────────────────────┐
 ├─ M2 hostruntime ─ M4 agentloop ─ M5 TUI model ──────────────┼─ M6 문서
 └─ M3 authkey ─────────────────────────────────────────────────┘
```

실제 merge 순서는 `M0 → M1 → M2 → M3 → M4 → M5 → M6`을 기본으로 한다. M1과 M3는
의존성이 없으므로 구현 인력과 review 상황에 따라 순서를 바꿀 수 있다. M5는 M4 이후에
진행해 Agent Loop 코드가 빠진 상태에서 UI 상태만 다룬다.

전체 예상 규모는 12~24 작업일, 9~13개 PR이었다. 실제 구현은 사용자의 연속 작업 요청에
따라 한 작업 트리에 진행했다. 리뷰할 때는 파일 교체, host runtime, API key, Agent Loop,
TUI model 순서로 변경을 확인한다.

## 7. 검증 전략

각 마일스톤의 focused test에 더해 merge 전 다음 검증을 수행한다.

```text
gofmt
go test ./...
go vet ./...
go run ./scripts/modulecheck
git diff --check
```

플랫폼 파일 처리 변경은 Windows와 Linux/macOS 환경에서 각각 검증한다. ACP 및 Agent Loop 변경은
fake client/runtime 기반 결정론적 테스트로 tool call, tool result, diff, question,
compaction, cancellation과 terminal result를 검증한다. 실제 provider 연결은 구조
리팩터링의 완료 조건에 포함하지 않는다.

전체 테스트 실패 시 먼저 focused test를 통해 변경 회귀와 환경 간섭을 구분한다. 단독
실행만 통과하는 테스트는 완료 evidence로 인정하지 않고 격리 원인을 제거한다.

## 8. 주요 위험과 대응

| 위험 | 대응 |
| --- | --- |
| 종료 순서 변경으로 goroutine 또는 데이터 flush 누락 | M0에서 lifecycle test를 고정하고 M2에서 한 소유자가 역순 종료 |
| Windows replace 의미 변화 | 기존 재시도 테스트를 공통 패키지로 이동하고 실제 호출 패키지 회귀 테스트 유지 |
| Gateway와 System One key domain 혼동 | policy fixture와 상호 인증 거부 테스트를 필수화 |
| 공개 Agent Loop import 경로 변경 | `app` facade와 type alias 유지, 제거는 별도 호환성 결정으로 처리 |
| 패키지 이동 중 두 Agent Loop 구현 생성 | 이동 PR에서 원본을 forwarding 또는 삭제하고 단일 구현 조건 검사 |
| Bubble Tea value model의 copy 의미 손상 | 하위 상태의 값/포인터 소유권을 명시하고 focus, cancel, channel test 유지 |
| 대형 PR로 회귀 원인 불명확 | 마일스톤을 소유권 단위 PR로 나누고 각 PR에서 전체 테스트 실행 |

## 9. 제외 범위

- 새로운 network service, daemon 또는 별도 repository 도입.
- Session Store, Loom, workspace 및 config 파일 schema 변경.
- Agent orchestration, planning, skill hint와 compaction 정책 재설계.
- TUI 화면 디자인 또는 명령 체계 변경.
- 일반적인 `common` 패키지에 짧은 helper를 모두 모으는 작업.
- 기존 외부 MCP, provider와 ACP protocol의 기능 확장.

5~14줄 수준의 `writeJSON`, 문자열 clone, strict JSON decode 같은 작은 중복은 의미와 오류
문맥이 각 패키지에 남아 있어 이번 로드맵의 우선 대상으로 삼지 않는다.

## 10. 상태 추적

| 마일스톤 | 상태 | 구현 PR/commit | 검증 기록 | 남은 작업 |
| --- | --- | --- | --- | --- |
| M0 기준선과 테스트 격리 | 완료 | 본 변경 | service smoke 20회, ACP 취소 50회, 전체 테스트 통과 | 없음 |
| M1 플랫폼 파일 교체 | 구현 완료 | 본 변경 | Windows 재시도 test와 WSL Linux ARM64 전체 테스트 통과 | macOS 환경 확인 |
| M2 host runtime | 완료 | 본 변경 | 부분 시작 실패 정리, `Close` 10회 반복, `app` 및 전체 테스트 통과 | 없음 |
| M3 API key primitive | 완료 | 본 변경 | Gateway/System One key test와 전체 테스트 통과 | 없음 |
| M4 Agent Loop 패키지 | 구현 완료 | 본 변경 | 외부 `agentloop_test`의 workspace/tool/question/compaction/stream test, 기존 `app` test, 직접 import 경계 검사 통과 | 호스트 호출자는 호환 facade를 통해 단일 실행 본문 사용 |
| M5 TUI model 분해 | 구현 완료 | 본 변경 | `app` 및 전체 테스트 통과; 화면 입력, 질문, 세션, 취소 회귀 포함 | 화면별 view와 controller는 기능별 파일군으로 유지 |
| M6 문서와 경계 정리 | 구현 완료 | 본 변경 | Draw.io/XML 및 SVG 갱신, SVG 렌더 확인, Windows와 WSL Linux 전체 테스트, `go vet ./...`, modulecheck 통과 | macOS 환경 확인과 코드 리뷰 |

## 11. 구현 및 검증 기록

- M4: `agentloop`가 요청, 이벤트, 결과, context 압축, stream 복구, orchestration,
  skill hint, 역할별 tool scope와 단일 `RunAgentLoop` 본문을 소유한다. `app`는 기존
  공개 타입과 이벤트 채널을 투영하는 facade를 유지한다. TUI, ACP 및 내부
  Search/Web Tester 호출자는 이 facade를 통해 같은 실행 본문에 도달한다.
  새 임베딩 호출자는 `agentloop`를 직접 사용한다. `agentloop`의 직접 import에는 `app`,
  Bubble Tea, ACP SDK 또는 `providerhost`가 없다. `tools`와 `workspace` 계약은
  그대로 사용하므로 이들의 전이 의존성은 남지만 서비스 프로세스를 시작하지 않는다.
- M5: `model` 필드를 13개의 명명된 값 상태로 나눴다. Agent Loop 이벤트와 chat 결과
  reducer를 `chat_events.go`로, 화면 메시지 dispatch를 `screen_dispatch.go`로 옮겼다.
  provider/model/chat 입력은 각각의 기능별 controller 파일로 나눴고, 기존 view 파일군을
  유지했다. 최상위 `Update`는 전역 메시지와 화면 dispatch를 담당한다. 하위 상태를
  값으로 embed해 기존 Bubble Tea 모델 복사 의미를 유지한다.
- M6: README, 임베딩 가이드, 최초 공개 API 기록과 Draw.io/SVG 아키텍처 그림을 새
  소유권에 맞췄다. 호환 helper의 제거는 공개 API 사용자에게 영향을 줄 수 있으므로
  이번 작업에서 하지 않았다.
- 검증: Windows에서 `go test ./... -count=1`, `go vet ./...`,
  `go run ./scripts/modulecheck`와 `git diff --check`를 통과했다. 첫 전체 테스트를
  modulecheck와 동시에 실행했을 때 Library 임베딩 테스트 한 건이 HTTP 응답 시간
  초과로 실패했다. 해당 테스트를 단독 5회 재실행하고 전체 테스트를 순차 재실행해
  모두 통과했다. WSL Debian 13 Linux ARM64에서 Go 1.26.5와 linuxbrew Go 1.27.1로
  `go test ./... -count=1`을 각각 통과했다. 첫 WSL 실행에서 발견한 두 이식성 문제는
  `agentskills` 테스트의 `HOME` 격리와 `subagent`의 역슬래시 경로 정규화로 수정했다.
  macOS 환경 검증은 실행하지 않았다.

## 12. Studio 설정 책임 분리 (2026-10-01)

최근 Studio 확장으로 화면 전환과 설정 저장이 `App.svelte`에, 여러 설정 기능의
HTTP 처리가 `studio/settings.go`에 함께 모였다. 이 변경은 해당 영역의 책임을
나누며 기존 API, 설정 파일 형식, mutex와 런타임 적용 순서를 유지한다.

- `App.svelte`: navigation, 연결 상태, 전역 단축키와 sidebar만 소유한다.
- `SettingsView.svelte`: 설정 화면, catalog cache와 직렬 저장 queue를 소유한다.
  화면 전환 중에도 component를 유지하고 표시만 바꿔, 진행 중인 저장과 기존
  설정 cache를 보존한다. 설정 API 타입은 `settings/types.ts`에 모았다.
- Go 설정 처리: service와 snapshot은 `settings.go`, JSON 계약은
  `settings_types.go`, 기능 처리는 `settings_models.go`,
  `settings_workspace_models.go`, `settings_providers.go`,
  `settings_systemone.go`, `settings_runtime.go`로 나눈다. 공통 HTTP 응답과
  strict settings request decode는 `api.go`에 둔다.
- Go 선언 89개가 분리 전후 동일하게 유지되는지 확인했다. 기존 Studio API
  회귀·통합 테스트와 브라우저 테스트에 더해, 설정 저장 응답이 다른 화면에서
  도착해도 queue와 선택 section이 유지되는 브라우저 회귀를 검증한다.

검증: Windows에서 `task test`(전체 Go, 중첩 ACP 모듈, 생성 코드 검사,
Svelte 검사), `task studio:test`(브라우저 7개), `go vet ./studio`와
`git diff --check`를 통과했다. 프런트엔드 배포 bundle도 재빌드했다.

이후 정리 후보는 `SessionView.svelte`의 registry/project dialog와 실행·채팅
controller, `studio/integrations.go`의 MCP/LSP/Skills/agent별 경계다. dialog 분리의
후속 구현은 아래 기록을 따른다.

## 13. Studio 세션 대화상자 분리 (2026-10-01)

- 세션 등록, 프로젝트 편집, 폴더 탐색의 입력·조회·오류·닫기 상태를 각각
  `sessions/SessionRegistration.svelte`, `sessions/ProjectDialog.svelte`,
  `sessions/DirectoryBrowser.svelte`로 옮겼다. 폴더 탐색은 등록과 프로젝트
  편집에서 같은 component를 사용하며, Escape는 가장 위의 폴더 창부터 닫는다.
- `SessionView.svelte`는 선택한 root/child session, 실제 session 등록 요청,
  registry 새로고침과 실행·스트리밍 controller를 계속 소유한다. 대화상자가
  선택한 명시적 workspace path와 완료 callback으로 연결한다.
- session/run/registry JSON 타입과 표시 helper는 `sessions/types.ts`와
  `sessions/format.ts`, 관련 HTTP 오류 parsing은 `api.ts`에 모았다. API와
  backend 저장 형식은 변경하지 않았다.
- browser fixture의 임시 directory를 사용해 프로젝트 생성·수정·삭제, 취소 후
  편집값 복원, folder selection 후 실제 session 등록, home부터 재탐색 및
  중첩 dialog의 Escape 순서를 추가 검증한다. 실제 사용자 프로젝트는 사용하지 않는다.

검증: `task studio:test`(브라우저 9개), Svelte 검사와 Studio Go 회귀·통합
테스트를 통과했다. 배포 bundle도 재빌드했다. 이후 후보는 세션의 실행·채팅
controller와 `studio/integrations.go`의 기능별 분리다.

## 14. Studio 실행 조회 수명 분리 (2026-10-01)

- `sessions/run-monitor.ts`가 이벤트 long polling, cursor, guidance redirect,
  재연결 대기와 요청 취소를 소유한다. workspace/session 경로를 시작할 때
  고정하고, 선택 변경·대화 지우기·화면 종료 시 요청과 retry timer를 취소한다.
- `SessionView.svelte`는 메시지 입력, run command와 표시 상태를 소유한다.
  세션 detail, latest run, transcript, delegation 및 learning 응답은 자신을
  시작한 선택이 여전히 유효할 때만 화면에 반영한다. 세션 조회 중에는 입력을
  잠시 막아 이전 session ID와 새로운 workspace path가 섞이지 않게 한다.
- 지연된 이전 run 응답과 세션 detail 응답을 브라우저에서 의도적으로 보류한 뒤
  선택을 바꿔, 요청 취소와 새 선택 보존을 검증한다. 기존 guidance redirect와
  새로고침 후 replay 흐름도 유지한다. 실행 중인 backend task는 관찰 종료와
  별개로 유지되며 기존 세션에서 다시 연결할 수 있다.

검증: Svelte 검사와 `task studio:test`(브라우저 11개)를 통과했다.
배포 bundle을 재빌드했다. 다음 단계는 Go integration 처리의 기능별 분리다.

## 15. Studio integration 처리 분리 (2026-10-01)

- 909줄의 `studio/integrations.go`에서 MCP, LSP, Skills, agent connection,
  subagent profile과 ignore 처리를 각각 `integrations_mcp.go`,
  `integrations_lsp.go`, `integrations_skills.go`, `integrations_agents.go`,
  `integrations_profiles.go`, `integrations_ignore.go`로 옮겼다. 각 기능의
  request/response 타입은 그 기능과 함께 둔다.
- `integrations.go`는 service 생성, 공유 mutex, 선택적 workspace 경로 처리,
  revision과 strict request decode만 소유하며 66줄로 줄었다. profile store와
  delegation graph 검증은 profile 처리와, connection 참조 검증과 secret
  마스킹은 agent connection 처리와 함께 유지한다.
- 분리 전후 선언 47개의 본문을 문자열 내부까지 동일하게 비교했다. HTTP 경로,
  JSON 계약, 요청 크기 제한, 공유 lock, revision conflict와 runtime 적용 순서는
  변경하지 않았다.

검증: `go test ./studio -count=1 -timeout=3m`, `go vet ./studio`,
`task studio:test`(브라우저 11개), `git diff --check`를 통과했다. MCP/LSP 설정,
Skills 목록, connection/profile 저장과 ignore revision 충돌을 기존 회귀 테스트로
검증했다. 다음 후보는 frontend integration 화면의 기능별 상태와 세션 transcript/
activity 표시 책임을 분리하는 것이다.

## 16. Studio integration 화면의 상태 분리 (2026-10-01)

- `IntegrationsView.svelte`는 panel 선택, repository 경로 입력과 작업 상태만
  소유한다. MCP, LSP, Skills와 `.qignore`의 조회·입력·저장은
  `integrations/MCPPanel.svelte`, `LSPPanel.svelte`, `SkillsPanel.svelte`,
  `IgnorePanel.svelte`가 각각 소유한다. 타입과 JSON 처리는 같은 directory에 둔다.
- `integrations/operations.ts`의 직렬 queue로 저장과 후속 조회 순서를 유지하고,
  대기 중인 모든 작업이 끝날 때까지 busy 상태를 유지한다. 저장 payload와
  workspace path는 입력 당시 고정한다. 오래된 저장/조회 응답은 새로운 편집이나
  repository 선택을 덮지 않는다.
- `.qignore`는 문서별 revision을 저장 승인 후 갱신한다. panel 변경, repository
  변경과 component 종료 시 debounce 중인 입력을 해당 문서의 경로로 저장한다.
  서버의 revision 충돌 검사는 유지하며 browser 전체 종료의 저장 완료는 보장하지 않는다.
- 브라우저 회귀 4개를 추가했다. MCP transport/env grant/삭제와 JSON 오류 복구,
  지연된 LSP 저장 중 repository 변경, `.qignore` revision 연쇄와 화면 전환 저장,
  portable Skills 표시와 실제 local service 재인덱싱을 확인한다. fixture에만 임시
  portable skill을 추가하며 외부 Git repository나 실제 사용자 설정을 사용하지 않는다.

검증: Svelte 검사, Studio Go 테스트, `task studio:test`(브라우저 15개),
`git diff --check`를 통과했다. 기존 Playwright fixture로 실제 browser 렌더링을
검증했고 배포 bundle을 재빌드했다. 다음 단계는 session transcript/activity 표시다.

## 17. Studio 대화·활동 표시 책임 분리 (2026-10-01)

- `sessions/Transcript.svelte`가 user/assistant 메시지, tool call/result,
  thinking과 response draft, loading 표시와 transcript DOM을 소유한다.
  `SessionView.svelte`는 메시지와 실행 상태를 전달하고, 렌더링 완료 후
  component의 scroll 동작을 호출한다.
- `sessions/RunActivity.svelte`가 active task, delegation tree, change request와
  tool activity 표시를 소유한다. 표시 helper는 기존 `sessions/format.ts`에 모았다.
- 부모 화면은 session 선택, 입력, 질문 답변, 실행 command와 run monitor를 계속
  소유한다. 반복 tool ID를 위한 message/call index key와 기존 DOM 구조를 유지해
  새로고침 replay, tool 결과 펼치기와 화면 폭에 따른 정렬을 보존한다.

검증: Svelte 검사와 `task studio:test`(브라우저 15개)를 통과했다. 실제 default
loop의 guidance 전환, 응답 갱신, replay와 Markdown/code 표시, 900·1440·2560·
3440px에서 tool call/result 양쪽 정렬을 검증했고, 1440px 대화 screenshot도
확인했다. 배포 bundle을 재빌드했다. 이 단계에서는 기존 표시의 책임만 나눴다.
다음 정리 후보는 subagent 설정의 profile/connection 상태와 session tree 표시다.

## 18. Studio subagent 설정 상태 분리 (2026-10-01)

- `SubagentsView.svelte`는 repository context, tab 선택과 catalog 조회를 소유한다.
  profile 편집·scope 이동은 `subagents/ProfilesPanel.svelte`, ACP connection과
  role binding은 `ConnectionsPanel.svelte`, 기본 제공 profile 표시는
  `BuiltinProfiles.svelte`로 옮겼다. tab을 바꿔도 편집 상태와 저장 수명을 유지한다.
- 직렬 저장 queue는 `settings/operations.ts`, JSON 입력 처리는 `settings/json.ts`,
  공통 HTTP 처리는 `api.ts`로 모아 integration과 subagent 설정이 같이 사용한다.
  각 저장은 입력 당시의 payload와 조회한 repository 경로를 유지한다.
- profile 저장은 승인된 revision과 scope를 다음 저장에 전달한다. scope만 옮기면
  content hash가 같을 수 있으므로 scope도 별도로 추적한다. 오래된 응답은 최신
  입력을 덮지 않으며 profile/connection 저장은 각자의 catalog 영역만 갱신한다.
- browser 회귀 3개로 profile role/prompt/tool/delegation grant와 삭제, 연속 편집과
  scope 이동 중 repository 변경, ACP secret 마스킹·활성화·role binding을 검증한다.
  이 과정에서 활성화 후 role 선택 목록이 갱신되지 않는 문제도 수정했다.
  실제 외부 ACP process 실행은 이번 browser 검증에 포함하지 않는다.

검증: Svelte 검사와 `task studio:test`(브라우저 18개)를 통과했다.
배포 bundle을 재빌드했다. 다음 단계는 session tree 표시 책임 분리다.

## 19. Studio 세션 트리 표시 책임 분리 (2026-10-01)

- `sessions/SessionTree.svelte`는 project/Independents 그룹, root와 중첩 delegation
  표시, 선택 강조와 tree depth를 소유한다. 선택·등록·삭제·새로고침·project 편집은
  callback으로 부모에 전달하며 API 요청과 실행 controller는 계속
  `SessionView.svelte`가 소유한다. 기존 DOM과 CSS 구조는 유지했다.
- 공통 `sessions/tree.ts`가 invocation path를 누적하며 delegation tree를 펼친다.
  rail과 run activity에 전달하는 목록이 같은 경로 계산을 사용하므로 중첩 delegation
  선택과 삭제가 같은 child를 가리킨다.
- opt-in browser fixture에 workspace API로 임시 root → senior → junior 기록을
  생성했다. 다른 root에서 child를 선택하는 흐름, 깊이·선택 강조·transcript 전환,
  junior 삭제 요청의 전체 path와 senior/root 보존, root 입력 복원을 검증한다.
  저장된 tree와 실제 Studio API를 검증하며 모델의 delegation 실행을 재현하는 테스트는 아니다.

검증: Svelte 검사, Studio Go 회귀·통합 테스트와 `task studio:test`(브라우저 19개)를
통과했다. 배포 bundle을 재빌드했다. 다음 정리 후보는 Changes 화면의 repository/
change request 상태와 diff 표시, 그리고 Operations 화면의 조회·표시 책임이다.

## 20. Studio 변경 조회와 커밋 리뷰 분리 (2026-10-01)

- `ChangesView.svelte`는 repository snapshot과 파일 선택을, `changes/FileDiff.svelte`는
  diff 표시를, `changes/CommitReview.svelte`는 proposal 편집과 commit 수명을 소유한다.
  공통 JSON 계약과 표시 helper는 같은 directory에 둔다.
- repository/file 조회를 취소하고 generation을 검사해 늦은 응답이 새 선택을 덮지
  않는다. 파일 변경 시 diff line 선택도 초기화한다. 리뷰 중 다른 repository로 이동은
  취소 후 가능하며 저장된 리뷰의 root로만 commit/refresh한다. 편집은 직렬로 저장하고
  화면 종료 시 대기 작업 이후 남아 있는 리뷰를 해제한다.
- 실제 임시 Git 저장소에서 diff 교체와 리뷰 메시지 연속 저장·commit을 검증했다.
  전체 검증 중 발견한 폴더 초기 조회의 입력 덮어쓰기도 수정하고 지연 응답 회귀를 추가했다.

검증: Svelte 검사와 `task studio:test`(브라우저 22개)를 통과했다. 배포 bundle을
재빌드했다. 남은 정리 범위는 Operations 조회/표시 분리, 전역 Settings 편집 기능
분리와 전체 검증이다. 이 세 단계가 끝나면 이번 Studio 정리를 완료로 기록한다.
