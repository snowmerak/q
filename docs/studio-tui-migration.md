# Q Studio TUI migration

작성일: 2026-09-29

상태: 실행 중인 이전 명세. 이 문서는 TUI 기능을 Studio로 옮기는 마일스톤,
현재 상태와 완료 증거를 소유한다. 제품 방향과 장기 run/task/Git 구조는
[Q Studio blueprint](studio-blueprint.md)가 소유하고, 정확한 동작 계약은 코드와
각 subsystem 문서가 소유한다.

갱신 조건: 마일스톤의 범위·순서·완료 상태가 바뀌거나, TUI 기능이 Studio의 실제
소유 화면으로 전환될 때 갱신한다. 구현이 없는 항목은 완료로 표시하지 않는다.

## 목표와 원칙

목표는 현재 Q TUI의 일상 기능을 Studio에서 사용할 수 있게 만들고, 기능별 완료
조건을 충족한 뒤 TUI 화면을 축소하거나 제거하는 것이다.

- TUI와 Studio가 같은 application service, validation과 저장 형식을 사용한다.
- browser는 config 파일이나 workspace 파일을 직접 수정하지 않는다.
- secret은 write-only 또는 일회 표시하며 snapshot, event와 log에 평문으로 남기지 않는다.
- workspace와 session은 canonical repository root를 명시적으로 소유한다.
- 실행 중 상태는 HTTP 요청 하나의 수명에만 의존하지 않고 재연결할 수 있어야 한다.
- repository 문서, model output과 tool output은 신뢰하지 않는 content로 렌더링한다.
- Markdown의 raw HTML과 위험한 URL은 정화하고 code block은 언어별 highlighting을
  제공한다.
- 각 세로 단위는 Go API, Studio UI, 저장/실행 side effect와 회귀 검증을 함께 끝낸다.

## 현재 기준선

Studio의 `SessionHost`는 프로세스 안에서 session별 rendererless 실행 상태를 유지한다.
일반 turn 완료는 model, provider conversation ID, 문맥 token 보정이나 MCP/LSP 연결을
폐기하지 않으며, idle 상태에서도 백그라운드 학습 결과를 처리한다. clear/delete와
host 종료는 해당 자원을 닫는다. model/provider, workspace/project나 MCP/LSP
설정이 바뀌면 다음 turn에서 idle 실행 상태를 재생성해 새 설정을 적용한다. learning
toggle은 유지 중인 실행 상태에 즉시 적용하며 embedding 전환은 idle archive lease를 닫는다. session
파일과 Studio run event log는 계속 복구·재연결의 권위 데이터로 사용한다. 토큰 footer는
TUI와 같이 최종 assistant 응답의 사용량만 표시하며 tool 호출에 같은 값이 반복되지 않는다.

Studio에는 repository session lifecycle과 Markdown chat, global model/provider/runtime,
Gateway/System One/Library service, Loom operation, MCP, LSP, Skills, subagent/ACP,
`.qignore`, changes와 commit workflow, durable run과 개입 control이 있다. directory
browser는 Go API가 home에서 시작해 모든 지원 OS에 같은 Web UI를 제공한다.

Sessions는 user-level registry에 등록한 여러 workspace의 root session을 하나의 tree로
표시한다. workspace directory에서 기존 root를 고르거나 새 session을 만들어 등록하고,
기존 nested delegation session은 클릭 가능한 child transcript로 투영한다. registry는
root 위치만 보유하며 transcript와 delegation의 권위는 workspace `.q`에 남는다.

workspace model override, usage, service/worker health와 full help까지 Studio로 이전했다.
bare `q` 대화 TUI와 ACP는 호환 client로 남지만 독립 설정 CLI 명령은 Studio route를
연다.

## 마일스톤

### M0. 이전 계약과 공통 service 경계 — 완료 (2026-09-29)

범위:

- TUI 명령과 화면을 아래 capability matrix에 고정한다.
- 새 Web handler가 Bubble Tea 화면 함수를 호출하지 않게 한다.
- session, settings, changes, commit과 integration 동작을 transport-neutral service로
  옮기고 TUI는 전환 기간에 같은 service를 사용한다.
- Studio API error, secret redaction, canonical path와 revision 규칙을 공통화한다.

완료 조건:

- 모든 matrix 행에 권위 service, Studio entry point와 검증이 연결된다.
- 화면별 파일 I/O를 새 Studio handler에 복제하지 않는다.

완료 기록:

- session/run은 `app.SessionHost`와 `studio.sessionsService`, model/provider/runtime은 기존
  config store와 `studio.settingsService`, changes/commit은 `changes`/`commitagent`,
  integration은 각 MCP/LSP/Skill/subagent/workspace store를 권위 경계로 사용한다.
- Studio handler는 이 service와 validator를 호출하며 frontend가 config/session/Git 파일을
  직접 읽거나 쓰지 않는다. API path, oversized body, revision, secret redaction과 runtime
  side effect test를 이후 각 milestone에 누적했다.

### M1. 채팅 표현과 기본 session lifecycle — 완료 (2026-09-29)

범위:

- 안전한 Markdown, 표·목록·인용·링크와 언어별 code highlighting.
- user/assistant message, reasoning, tool call과 tool result를 읽기 쉬운 timeline으로 표시.
- 긴 tool body 접기, code와 긴 문장의 overflow, streaming 중 안정적인 layout.
- session 생성·전환·삭제, 현재 session 초기화, 수동 compaction과 learning control.
- 질문 UI와 현재 run을 대상으로 하는 중단 command의 service 경계를 준비한다.

완료 조건:

- 새로 연 session과 복원 transcript가 같은 Markdown 결과를 보인다.
- raw HTML/script와 위험한 link가 실행되지 않는다.
- 일반적인 Go, TypeScript, JSON, shell, diff code block이 강조된다.
- session 삭제, clear, compact와 learning 상태 변경이 TUI와 같은 저장 결과를 만든다.

완료 기록:

- `markdown-it`의 raw HTML 비활성화와 기본 URL 검증 위에 `highlight.js`의 명시적
  언어 목록을 연결했다. 복원 transcript와 streaming 응답이 같은 renderer를 사용한다.
- code copy, 표·목록·인용·링크, 접을 수 있는 tool call/result와 overflow 처리를
  chat timeline에 적용했다.
- TUI와 같은 현재 context 예측치와 context window를 durable run event/snapshot에 기록하고
  Sessions 헤더의 사용률 meter로 표시한다.
- session 삭제와 in-place clear, workspace learning on/off, rendererless 수동 compaction을
  공용 Go service와 Studio API/UI에 연결했다.
- `go test ./workspace ./studio`, SessionHost/compaction 집중 test, `npm run check`와
  production build를 통과했다. browser 자동화 package는 이 checkout에 설치되어 있지
  않아 자동 screenshot 검증은 수행하지 않았으며, durable 질문·중단·reconnect는 M5가
  소유한다.

### M2. Settings runtime parity — 완료 (2026-09-29)

범위:

- embedding 변경 시 workspace archive와 global skill embedding을 재구성하고 진행 상태를
  보여준다.
- model fallback group, custom role, model API mode와 context override를 편집한다.
- Gateway API key 생성·일회 표시·폐기와 active provider hot apply를 제공한다.
- Library listener, Loom usage·GC preview·collect를 제공한다.
- global 설정을 먼저 완료한다. workspace model override는 workspace settings surface를
  도입할 때 연결한다.

완료 조건:

- Studio에서 바꾼 설정과 runtime side effect가 TUI와 일치한다.
- Studio 재시작 없이 provider 변경이 다음 turn에 적용된다.
- embedding 변경 도중 종료되어도 다음 시작에서 필요한 backfill을 재개한다.

완료 기록:

- embedding 저장은 global Library와 Sessions에서 마지막으로 연 repository archive를
  동기화한다. 저장 UI는 재색인이 끝날 때까지 진행 상태를 유지하며, 기존 startup
  backfill도 다음 session 시작에서 미완료 record를 다시 검사한다.
- fallback group 후보 순서·reasoning·timeout, custom role, concrete model API mode와
  Gateway context metadata override를 Models 화면에 추가했다.
- provider 수정은 실행 중인 `SessionHost`의 Gateway child를 교체한 뒤 저장하므로 다음
  turn부터 재시작 없이 적용된다. Gateway server key는 생성 때만 secret을 반환한다.
- Library listener와 repository Loom stats, GC preview/collect를 Services와 Runtime에
  연결했다.
- `go test ./app ./studio ./workspace ./gatewayconfig ./library ./loom`, `npm run check`,
  production build를 통과했다.

### M3. Integration, Skill과 Agent 관리 — 완료 (2026-09-29)

범위:

- MCP server CRUD, stdio/Streamable HTTP, env/header mapping과 role assignment.
- LSP profile/root CRUD, enable/default, discovery와 validation error.
- global/workspace Skill 목록, Git install/update/remove와 index reconciliation.
- builtin/custom subagent profile, delegation grant, ACP binding과 connection probe.
- repository `.qignore` 편집, validation, revision conflict 처리.

완료 조건:

- TUI에서 만들 수 있는 동일한 config를 Studio에서 만들고 다시 열어 손실 없이 편집한다.
- connection probe와 discovery 결과가 대상 항목에 귀속되어 표시된다.
- credential value는 Studio read API로 돌아오지 않는다.

완료 기록:

- Integrations 화면은 MCP, Language servers, Skills, `.qignore`를 관리한다. Subagents는
  별도 Settings 화면에서 custom profile, builtin definition, ACP connection과 외부 role
  binding을 관리한다. global profile과 connection은 repository 없이 열 수 있고,
  repository 경로를 지정하면 workspace profile과 ACP probe가 추가된다.
- MCP의 두 transport, environment/header reference와 role grant, LSP global profile,
  language default, repository root, PATH discovery를 기존 config validator에 연결했다.
- portable/global/workspace Skill을 함께 표시하고 Q-managed Git checkout의 clone,
  pull, delete와 global/workspace index reconciliation을 한 operation으로 묶었다.
- builtin occupational agent를 읽기 전용으로 표시하고 custom inner/external profile,
  model role, 검색 가능한 tool와 delegation 선택, ACP connection/binding/probe를 편집하게 했다.
  ACP child environment value는 read API에서 redacted되고 unchanged marker는 저장 시
  원래 secret을 보존한다.
- `.qignore`는 입력 정지 뒤 자동 저장하며 SHA-256 revision mismatch를 `409`로
  반환한다. custom profile도 원본 revision을 검사하며 삭제 전 delegation reference를
  확인한다.
- `go test ./studio ./app ./lsp ./mcpconfig ./agentskills ./subagent ./workspace`,
  `npm run check`, production build를 통과했다.

### M4. Changes와 commit workflow — 완료 (2026-09-29)

범위:

- staged, unstaged, untracked와 rename 상태, 파일별 diff와 reload.
- binary·large file 제한, syntax-highlighted diff와 line anchor.
- commit 제안, 분할, message 편집, 선택과 실행 결과·실패 복구.
- changes/commit 로직을 TUI와 Studio가 공유하는 service로 추출한다.

완료 조건:

- 임시 Git repository fixture에서 TUI와 Studio가 같은 change snapshot을 만든다.
- 여러 commit 제안을 편집·선택해 실행하고 결과 commit을 확인한다.

완료 기록:

- 기존 `changes` package를 Studio API에서 그대로 호출해 staged, unstaged, untracked,
  rename 상태와 bounded patch를 제공한다. detail 요청은 매번 현재 snapshot에 실제로
  포함된 경로만 받아 임의 repository 파일 읽기를 막는다.
- Changes 화면에 repository별 파일 목록, reload, binary/large preview 상태,
  `highlight.js` diff 강조와 staged/unstaged section까지 구분되는 행 permalink를
  추가했다.
- 기존 `commitagent` headless session을 review API로 감싸 split proposal, 선택한
  proposal message 수정, regenerate, index snapshot 검증, commit과 optional push를
  제공한다. 처음 index가 비어 자동 stage한 상태와 push 실패도 별도로 표시한다.
- review session은 repository lock을 소유하고 cancel, execute, server shutdown 또는
  30분 inactivity 때 닫힌다. 임시 Git repository integration test와
  `go test ./studio ./commitagent ./changes ./app`, `npm run check`, production build를
  통과했다.

### M5. Durable run, 질문과 개입 — 완료 (2026-09-29)

범위:

- append-only run event와 snapshot + cursor replay.
- browser disconnect와 무관한 run lifecycle, session별 동시 실행과 reconnect.
- 정확한 run/tool call에 대한 `ask_to_user` 응답.
- guidance, pause/resume/cancel과 late-command 결과.
- delegation 호출 tree, child timeline, recovery state와 상세 tool event.

완료 조건:

- 실행 중 새로고침 후 중복이나 누락 없이 timeline을 이어 본다.
- 부모→자식→손자 호출과 질문/중단 대상이 재시작 뒤에도 유지된다.
- browser 요청 취소만으로 worker가 사라지지 않는다.

완료 기록:

- POST request와 turn lifetime을 분리하고 session별 background run으로 실행한다. 같은
  session에는 한 turn만 허용하지만 서로 다른 repository/session은 동시에 실행할 수
  있다.
- 각 run은 session 아래 append-only NDJSON event log와 atomic snapshot을 가진다.
  cursor long polling은 중복 없이 reconnect하며, snapshot보다 앞선 log와 crash 중
  부분 기록된 마지막 행도 시작 시 안전하게 reconcile한다.
- `SessionRunControl`을 rendererless default loop에 추가해 정확한 `ask_to_user` call ID의
  choice/freeform answer, pause/resume/cancel과 late command rejection을 제공한다. 실행 중
  composer guidance는 현재 turn을 안전하게 interrupt한 뒤 같은 session에서 새 run으로
  이어지고 redirect event로 UI가 자동 전환된다.
- Sessions 화면은 browser disconnect와 무관하게 run을 계속하며 새로고침 시 latest run과
  cursor를 복원한다. 질문 card, pause/resume/stop, guidance composer와 persisted
  parent-child delegation tree를 함께 표시한다.
- server restart에서 active snapshot은 `interrupted`로 표시하고 기존 transcript,
  active task와 child delegation checkpoint를 다음 turn의 공용 recovery 경로가 복원한다.
- request cancellation, disk replay, cursor tail, session별 동시 실행, stale command,
  질문과 control, delegation tree fixture를 포함해
  `go test ./studio ./app ./workspace ./agentloop`, `go vet ./studio ./app`,
  `npm run check`와 production build를 통과했다.

### M6. Operations, help와 TUI retirement gate — 완료 (2026-09-29)

범위:

- token usage, service/worker health, logs와 보존 상태를 Studio에 연결한다.
- Studio navigation과 기능에 맞춘 help, shortcut와 오류 복구 안내를 제공한다.
- `/new`, `/clear`, `/compact`, `/learn`, `/model`, `/gateway`, `/systemone`, `/library`,
  `/loom`, `/ignore`, `/skills`, `/lsp`, `/mcp`, `/subagents`, `/subagent`, `/changes`,
  `/commit`, `/sessions`, `/help`의 Web 대응 경로를 검증한다.
- parity가 확인된 TUI 화면부터 deprecation과 제거를 별도 change로 수행한다.

완료 조건:

- 아래 matrix의 모든 TUI parity 행이 완료되고 browser end-to-end 검증을 통과한다.
- README의 기본 사용 경로가 Studio를 가리키며 남은 TUI 호환 범위가 명확하다.

완료 기록:

- 기존 `.q/model.json` store를 사용하는 workspace model API와 Models 화면을 추가했다.
  default와 직업형 role은 repository별 override를 저장하고 Thinker/Librarian 같은 shared
  role은 global로 유지한다. main config가 없는 첫 실행도 Studio에서 provider를 만든 뒤
  default model을 선택해 초기화할 수 있다.
- Operations는 1/7/30/90일 usage series와 model/role 합계, active/resident run,
  Gateway·Library·Workspace Memory·Usage health, hot database와 archive 크기, bounded service
  log를 한 snapshot으로 제공한다. Usage 조회와 health는 기존 user-level service 계약을
  통과한다.
- Help에 Studio shortcut, browser/server restart 복구와 모든 local slash command의 Web
  대응표를 기록했다. `q model`, `q gateway`, `q systemone`, `q library`, `q usage`,
  integration/help 명령은 대응 Studio route를 열며 `start` service command와
  bare `q`/ACP 호환 client는 유지한다. `q commit`은 터미널의 대화형 커밋 세션을 실행한다.
- README의 기본 실행 경로를 `q studio`로 바꾸고 blueprint의 현재 상태와 navigation을
  실제 구현에 맞췄다. Go API test, runtime service smoke, Svelte type check와 production
  build로 검증했다. embedded server의 Operations, Help와 긴 Models 화면을 headless Edge로
  렌더링해 navigation, 내부 scroll과 workspace override 배치를 확인했다.

## Capability matrix

| 기능 | 현재 Studio | 목표 마일스톤 | 상태 |
| --- | --- | --- | --- |
| App shell/status/navigation | Overview, Sessions, Changes, Operations, Settings, Help | M6 operations와 help | 완료 |
| Repository directory browser | Go directory API, home 시작 | M1 유지 | 완료 |
| Session tree/등록/전환 | 전역 root registry, workspace에서 기존 root 선택·새 root 생성, child transcript, 삭제·clear·compact·learning·run reconnect | M1, M5 | 완료 |
| Chat streaming | durable event log, cursor replay와 background run | M1, M5 | 완료 |
| Session context usage | 예측 token/context window와 사용률 meter | M1 | 완료 |
| Markdown/code rendering | 안전한 Markdown과 언어별 highlighting | M1 | 완료 |
| Tool/reasoning presentation | transcript와 live 접기/요약 | M1 완료, M5 tree 확장 | 완료 |
| Question/interrupt | exact question answer, pause/resume/cancel, guidance redirect | M5 | 완료 |
| Changes | repository change 목록·bounded highlighted diff·행 anchor | M4 | 완료 |
| Commit | proposal review·수정·재생성·split commit·optional push | M4 | 완료 |
| Global model/role assignment | assignment·group·custom role·API mode·metadata·reindex | M2 | 완료 |
| Gateway provider | CRUD와 실행 중 hot apply | M2 | 완료 |
| Gateway listener/API key | listener와 일회 표시 key 관리 | M2 | 완료 |
| System One | provider/model/listener/key 지원 | M2 회귀 유지 | 완료 |
| Library/Loom | Library listener, Loom 설정·stats·GC | M2 | 완료 |
| Skills | global/repository list·Git lifecycle·reindex | M3 | 완료 |
| LSP | profile/default/root CRUD와 discovery | M3 | 완료 |
| MCP | server CRUD, transport, env/header ref, role grant | M3 | 완료 |
| Subagent/ACP connection | builtin/custom profile, delegation, binding, probe | M3 | 완료 |
| `.qignore` | revision-aware 자동 저장 editor | M3 | 완료 |
| Usage/help | usage·health·worker·log·retention과 Studio help | M6 | 완료 |

`완료`는 해당 행의 현재 범위가 observable acceptance를 통과했다는 뜻이다. 뒤
마일스톤에서 durability나 operations가 확장될 수 있다.

## 검증과 완료 기록

각 마일스톤은 다음 중 영향을 받는 증거만 실행한다.

- Go service/API unit 및 integration test
- Svelte type check와 production build, committed `dist` drift 확인
- browser에서 keyboard, scroll, reconnect와 responsive layout 확인
- 실제 임시 repository/session/config를 이용한 end-to-end smoke
- secret, unsafe HTML, path, oversized output와 concurrent update 경계 검사
- Windows와 POSIX path/build test

마일스톤 완료 시 이 문서에 완료일, 구현 요약, 검증 명령과 남은 deviation을 기록한다.
부분 구현은 완료로 올리지 않는다.
