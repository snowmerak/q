# 구현 스펙: Codex 마일스톤 실행 가이드 및 완료 기준 (DoD)

- **작성일:** 2026-11-28
- **용도:** Codex에 마일스톤 단위로 작업을 위임할 때 사용하는 작업 분할, 준수 문서, 금지 사항, 검증 명령 체크리스트

## 현재 구현 및 검증 현황 (2026-11-28 기준)

| 마일스톤 | 상태 | 핵심 구현 및 검증 요약 |
| --- | --- | --- |
| **M1-A** | **완료** | `pkg/llm`, `pkg/llm/decisions`, `pkg/cdp`, `cmd/llm-provider`, `cmd/cdp-q-driver` 통합 및 역참조 차단(`NO_REVERSE_IMPORTS`) 통과 |
| **M1-B** | **완료** | `pkg/domain`, `pkg/app/config`, SQLite + Bleve/HNSW + LocalFS + Outbox, `cmd/q` 골격 및 저장소 계약 테스트 통과 |
| **M2-A** | **완료** | `pkg/memory`, 1:1 `pkg/runtime`, REST/SSE API, Codex/OpenAI/OpenRouter/DeepInfra(v4.1-flash-1017)/Gemini Hydrogen 라이브 세션 테스트(`Q_LIVE_TEST=1`) 통과 |
| **M2-B** | **완료** | React 19 + TypeScript + Vite + Tailwind v4 + shadcn/ui(테마 전환), `web/dist` `//go:embed`, `Taskfile.yml` 통합 |
| **M3** | **완료** | 7대 내장 도구, 3단계 `ApprovalGate`(`bypass`/`auto-detection`/`request`), Loom(goja `loom_eval`), `pkg/knowledge`, 도구/지식/스킬 UI 구현 및 검증 |
| **M4** | **완료** | 멀티캐릭터 `group` 런타임·그룹 UI, `sqlcommon`/`postgres`/`mysql`/`opensearch`/`s3` 어댑터 및 Docker 기반 6종 조합 매트릭스(`TestStorageSixWayMatrix`)·실DB 계약 테스트 통과 |
| **M5** | **완료** | `desktop/go.mod`(Wails `v3.1.0`), `App.StartAsync`, 트레이/숨김/로그인 자동 시작(`--register-autostart`, 트레이 체크박스), `desktop/packaging/` 매니페스트 및 `Taskfile.yml` 타깃 구성(Windows 환경 빌드·단위 테스트 통과; OS별 최종 인스톨러 파일 생성은 각 OS 패키징 도구 체인에서 실행) |

> [!IMPORTANT]
> **Codex 작업 공통 규칙**
> 1. 각 작업 시작 전 [Blueprint](../blueprint.md), 해당 ADR([`docs/adr/`](../adr/README.md)), 그리고 `docs/specs/`의 계약 문서를 먼저 읽고 인터페이스/스키마를 자의적으로 변형하지 않는다.
> 2. `pkg/llm`과 `pkg/cdp`는 상위 패키지(`pkg/app`, `pkg/runtime`, `pkg/task`, `pkg/memory`, `pkg/knowledge`, `pkg/storage`)를 절대 import하지 않는다.
> 3. 각 마일스톤은 반드시 지정된 검증 명령이 통과한 상태로 끝낸다.

---

## M1-A: `pkg/llm` & `pkg/cdp` 코드 통합 및 독립 CLI

- **선행 조건:** 없음 (현재 `v2` 브랜치 기준 바로 착수 가능)
- **작업 범위:**
  1. `c:\Users\snowm\Projects\snowmerak\llm-provider` → `pkg/llm/` 및 `cmd/llm-provider/` 포팅 (`github.com/snowmerak/q/v2/pkg/llm`)
  2. `c:\Users\snowm\Projects\snowmerak\cdp-q-driver` → `pkg/cdp/` 및 `cmd/cdp-q-driver/` 포팅 (`github.com/snowmerak/q/v2/pkg/cdp`)
  3. `pkg/llm/decisions/`에 Evernight `decision-client.ts` 기반 `github.com/snowmerak/decisions` 연동 클라이언트(또는 어댑터) 추가
  4. 루트 `go.mod` 의존성 동기화 (`go mod tidy`)
- **준수 문서:** [ADR-0004](../adr/0004-module-layout-and-independent-distribution.md), [런타임·도구 스펙 4장](./runtime-and-tools.md#4-%EC%9B%90%EB%B3%B8-%EC%A0%80%EC%9E%A5%EC%86%8C-%EC%B0%B8%EC%A1%B0-%EB%A7%A4%ED%95%91%ED%91%9C-codex-%ED%8F%AC%ED%8C%85-%EA%B0%80%EC%9D%B4%EB%93%9C)
- **DoD 검증 명령:**
  ```powershell
  go test ./pkg/llm/... ./pkg/cdp/...
  go build ./cmd/llm-provider
  go build ./cmd/cdp-q-driver
  go list -deps ./pkg/llm ./pkg/cdp | Select-String 'github.com/snowmerak/q/v2/pkg/(app|runtime|task|memory|knowledge|storage)'
  ```
  - 기대 결과: 테스트·빌드 성공, 역참조 검색 출력 없음.

---

## M1-B: `pkg/domain` · `pkg/app/config` · `pkg/storage` (SQLite + Bleve/HNSW + Local Blob) · `cmd/q` 골격

- **선행 조건:** M1-A 완료
- **작업 범위:**
  1. `pkg/domain`: 엔티티, ID/해시, 상태 enum 정의
  2. `pkg/app/config`: `--data-dir` / `Q_DATA_DIR`, `instance.lock` 싱글 인스턴스 가드, `config.json` strict 로더 + `fsnotify` hot-reload
  3. `pkg/storage`: 포트 인터페이스, 안정 에러 센티넬, `sqlite` RDB 어댑터(14개 테이블 마이그레이션), `bleve` + `goformersearch` HNSW 어댑터(세대 디렉터리), `local` Blob 어댑터, `index_outbox` 워커, 공통 Contract Test Suite 작성
  4. `cmd/q`: `q serve` (및 `q cdp` 위임 진입점), `GET /api/v1/health`, `GET/PUT /api/v1/config`, `GET /api/v1/models` 구현
- **준수 문서:** [ADR-0001](../adr/0001-storage-authority-and-hybrid-search.md), [설정·저장소 스펙](./config-and-storage.md), [API·이벤트 스펙](./api-and-events.md)
- **DoD 검증 명령:**
  ```powershell
  go test ./pkg/domain/... ./pkg/storage/... ./pkg/app/... ./cmd/q/...
  go build ./cmd/q
  ```

---

## M2-A: 1:1 캐릭터 봇 런타임 & 기억/가치관 기초 & HTTP/SSE API

- **선행 조건:** M1-B 완료
- **작업 범위:**
  1. `pkg/memory`: `memory_recall_gate` → 경량 플래너 → 하이브리드 검색 → `memory_recall_select`, 개인 기억 생성, `value_jobs` 백그라운드 정리 워커, `state_update` 감정/관계 갱신
  2. `pkg/runtime`: `direct`(1:1) 루프, Append-only 컨텍스트 & 85% 예산 압축, 체크포인트/멱등 `request_id` 재시도, `InterceptHub`
  3. `pkg/app`: Persona/Character/Room/Message/Turn/SSE REST & 스트림 엔드포인트 구현
- **준수 문서:** [ADR-0002](../adr/0002-chat-bubble-room-and-decisions-protocol.md), [API·이벤트 스펙](./api-and-events.md), [런타임·도구 스펙 1~2장](./runtime-and-tools.md)
- **DoD 검증 명령:**
  ```powershell
  go test ./pkg/memory/... ./pkg/runtime/... ./pkg/app/...
  ```
  - 필수 검증 케이스: 1:1 턴 실행 및 SSE 스트리밍, 동일 `request_id` 재시도/`409` 충돌, 85% 압축 및 가치관 잡 실행, Cut-in/Stop 중단.

---

## M2-B: React + shadcn/ui 웹 UI & 빌드 임베드 (`Taskfile.yml`)

- **선행 조건:** M2-A 완료
- **작업 범위:**
  1. `web/`: Vite + React + TypeScript + Tailwind CSS + shadcn/ui 초기화, 라이트/다크/시스템 테마 전환
  2. `web/src/client/`: `api-and-events.md` 기반 HTTP/SSE 클라이언트
  3. `web/src/features/`: 1:1 대화 화면, 캐릭터/페르소나 편집, 모델/설정 화면
  4. `Taskfile.yml`: `web/dist` 빌드 및 `pkg/app` `//go:embed` 연결 (`web/dist`는 `.gitignore` 유지)
- **준수 문서:** [API·이벤트 스펙 4장](./api-and-events.md#4-%ED%94%84%EB%9F%B0%ED%8A%B8%EC%97%94%EB%93%9C-web-%EA%B3%84%EC%95%BD), [Blueprint 6장](../blueprint.md#6-%ED%94%84%EB%9F%B0%ED%8A%B8%EC%97%94%EB%93%9C-%EB%B0%8F-%ED%81%B4%EB%9D%BC%EC%9D%B4%EC%96%B8%ED%8A%B8-%EC%95%84%ED%82%A4%ED%85%8D%EC%B2%98-%ED%99%95%EC%A0%95--%EC%A0%9C%EC%95%88)
- **DoD 검증 명령:**
  ```powershell
  pnpm --dir web install --frozen-lockfile
  pnpm --dir web typecheck
  pnpm --dir web build
  go build ./cmd/q
  ```

---

## M3: 7대 내장 도구 · 3단계 승인 게이트 · `pkg/knowledge`

- **선행 조건:** M2-B 완료
- **작업 범위:**
  1. `pkg/task`: 파일·디렉터리 8종, 셸 3종, 아카이브/지식 2종, Loom 3종(`loom_inspect`/`loom_read`/`loom_eval`), Agent Skills 2종, 외부 MCP + `browser_control`(`pkg/cdp` / `ego-browser`), `ask_to_user` + `delegate_task` 구현
  2. `ApprovalGate`: `bypass` / `auto-detection`(Decisions fail-closed) / `request` 3모드 및 체크포인트 대기/재개 구현
  3. `pkg/knowledge`: 문서 수집, 청킹, 리비전, Outbox 색인, `/api/v1/knowledge*` 엔드포인트 구현
  4. `web/src/features/{tools,knowledge}`: 승인 요청 카드, `ask_to_user` 질문 카드, Loom 드로어, 지식/스킬 UI 추가
- **준수 문서:** [ADR-0003](../adr/0003-tools-approval-gate-and-browser-control.md), [런타임·도구 스펙 3장](./runtime-and-tools.md#3-1%EC%B0%A8-%EB%82%B4%EC%9E%A5-%EB%8F%84%EA%B5%AC-7%EA%B0%9C-%EC%98%81%EC%97%AD-%EC%83%81%EC%84%B8-%EA%B3%84%EC%95%BD-pkgtask)
- **DoD 검증 명령:**
  ```powershell
  go test ./pkg/task/... ./pkg/knowledge/... ./pkg/runtime/...
  pnpm --dir web typecheck && pnpm --dir web build
  ```

---

## M4: 멀티캐릭터 룸 & 확장 저장소 (`PostgreSQL` / `MySQL` / `OpenSearch` / `S3`)

- **선행 조건:** M3 완료
- **작업 범위:**
  1. `pkg/runtime`: `group` 룸 스케줄러(`reserveActor`, `participation`, `user_turn`, `observedBy` 격리, 다중 캐릭터 버블 UI) 완성
  2. `pkg/storage/rdb/{postgres,mysql}` 및 `pkg/storage/search/opensearch`, `pkg/storage/blob/s3` 어댑터 구현
  3. 6가지 저장소 조합(`{SQLite, PostgreSQL, MySQL} × {Bleve+HNSW, OpenSearch}`) Contract Test Suite 실행 검증
- **준수 문서:** [ADR-0001](../adr/0001-storage-authority-and-hybrid-search.md), [ADR-0002](../adr/0002-chat-bubble-room-and-decisions-protocol.md), [설정·저장소 스펙](./config-and-storage.md)
- **DoD 검증 명령:**
  ```powershell
  go test ./pkg/storage/... ./pkg/runtime/...
  # PG/MySQL/OpenSearch 환경변수 설정 시 6종 통합 테스트 포함 실행
  ```

---

## M5: Wails v3 데스크톱 앱 (`desktop/`)

- **선행 조건:** M4 완료 (또는 M3 이후 UI 안정화 시 병행 가능)
- **작업 범위:**
  1. `desktop/go.mod` (Wails `v3.1.0` 고정, `github.com/snowmerak/q/v2` import) 생성
  2. `pkg/app` 인프로세스 구동, 창 제어, 시스템 트레이, 백그라운드 유지, 로그인 시작, `Taskfile.yml` 패키징 타깃 구성
- **준수 문서:** [ADR-0004](../adr/0004-module-layout-and-independent-distribution.md), [Blueprint 6.2절](../blueprint.md#62-%EB%8D%B0%EC%8A%A4%ED%81%AC%ED%86%B1-%EC%95%B1-wails-v3-%EC%A7%91%EC%A4%91-%EB%B0%A9%ED%96%A5)
- **DoD 검증 명령:**
  ```powershell
  pnpm --dir web build
  go -C desktop test ./...
  go -C desktop build ./...
  ```
