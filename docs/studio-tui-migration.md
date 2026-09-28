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

Studio에는 global model/role assignment, Gateway provider, System One, runtime/context,
Loom 보존 설정, Gateway/System One listener와 repository별 session 목록·생성·실행이
있다. directory browser는 Go API가 home에서 시작해 모든 지원 OS에 같은 Web UI를
제공한다.

아직 TUI가 소유하는 주요 기능은 session 삭제와 대화 명령, 질문 응답, durable
interrupt/reconnect, changes/commit, advanced model 설정, Gateway key, Library/Loom
operation, Skills, LSP, MCP, subagent/ACP connection, `.qignore`, usage와 help다.

## 마일스톤

### M0. 이전 계약과 공통 service 경계 — 진행 중

범위:

- TUI 명령과 화면을 아래 capability matrix에 고정한다.
- 새 Web handler가 Bubble Tea 화면 함수를 호출하지 않게 한다.
- session, settings, changes, commit과 integration 동작을 transport-neutral service로
  옮기고 TUI는 전환 기간에 같은 service를 사용한다.
- Studio API error, secret redaction, canonical path와 revision 규칙을 공통화한다.

완료 조건:

- 모든 matrix 행에 권위 service, Studio entry point와 검증이 연결된다.
- 화면별 파일 I/O를 새 Studio handler에 복제하지 않는다.

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
- session 삭제와 in-place clear, workspace learning on/off, rendererless 수동 compaction을
  공용 Go service와 Studio API/UI에 연결했다.
- `go test ./workspace ./studio`, SessionHost/compaction 집중 test, `npm run check`와
  production build를 통과했다. browser 자동화 package는 이 checkout에 설치되어 있지
  않아 자동 screenshot 검증은 수행하지 않았으며, durable 질문·중단·reconnect는 M5가
  소유한다.

### M2. Settings runtime parity

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

### M3. Integration, Skill과 Agent 관리

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

### M4. Changes와 commit workflow

범위:

- staged, unstaged, untracked와 rename 상태, 파일별 diff와 reload.
- binary·large file 제한, syntax-highlighted diff와 line anchor.
- commit 제안, 분할, message 편집, 선택과 실행 결과·실패 복구.
- changes/commit 로직을 TUI와 Studio가 공유하는 service로 추출한다.

완료 조건:

- 임시 Git repository fixture에서 TUI와 Studio가 같은 change snapshot을 만든다.
- 여러 commit 제안을 편집·선택해 실행하고 결과 commit을 확인한다.

### M5. Durable run, 질문과 개입

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

### M6. Operations, help와 TUI retirement gate

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

## Capability matrix

| 기능 | 현재 Studio | 목표 마일스톤 | 상태 |
| --- | --- | --- | --- |
| App shell/status/navigation | 기본 shell과 service ready | M6 operations와 help | 부분 |
| Repository directory browser | Go directory API, home 시작 | M1 유지 | 완료 |
| Session 목록/생성/전환 | 생성·전환·삭제·clear·compact·learning 지원 | M1 완료, M5 실행 복구 | 완료 |
| Chat streaming | 요청 수명 NDJSON | M1 rendering, M5 replay | 부분 |
| Markdown/code rendering | 안전한 Markdown과 언어별 highlighting | M1 | 완료 |
| Tool/reasoning presentation | transcript와 live 접기/요약 | M1 완료, M5 tree 확장 | 완료 |
| Question/interrupt | request abort만 지원 | M5 | 미착수 |
| Changes | 없음 | M4 | 미착수 |
| Commit | 없음 | M4 | 미착수 |
| Global model/role assignment | 기본·embedding·role 지원 | M2 advanced/runtime side effect | 부분 |
| Gateway provider | CRUD와 secret write | M2 hot apply | 부분 |
| Gateway listener/API key | listener만 지원 | M2 | 부분 |
| System One | provider/model/listener/key 지원 | M2 회귀 유지 | 완료 |
| Library/Loom | Loom 설정만 지원 | M2 | 부분 |
| Skills | 없음 | M3 | 미착수 |
| LSP | 개수 요약 | M3 | 미착수 |
| MCP | 개수 요약 | M3 | 미착수 |
| Subagent/ACP connection | 없음 | M3 | 미착수 |
| `.qignore` | 없음 | M3 | 미착수 |
| Usage/help | 최소 status만 지원 | M6 | 미착수 |

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
