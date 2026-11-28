# ADR-0003: 1차 내장 도구 7개 영역, 3단계 승인 게이트 및 `browser_control` 계약

- **상태:** 확정 (Accepted — 7대 도구 범위·3단계 승인 게이트·`browser_control` config 확정 / 경로·셸 세부 가드와 fail-closed 기본값은 M3 구현 시 제안안 기준 확정)
- **작성일:** 2026-11-28
- **관련 문서:** [PRD (`REQ-TASK-01`)](../prd.md#42-%EA%B8%B0%EB%8A%A5-%EB%B0%8F-%EB%B9%84%EA%B8%B0%EB%8A%A5-%EC%9A%94%EA%B5%AC%EC%82%AC%ED%95%AD), [Blueprint 3장·5장](../blueprint.md#51-%ED%84%B4-%EC%8B%A4%ED%96%89-%EB%B0%8F-%EB%8F%84%EA%B5%AC-%EC%8A%B9%EC%9D%B8-%EA%B2%8C%EC%9D%B4%ED%8A%B8-%ED%9D%90%EB%A6%84-11-%EB%A9%80%ED%8B%B0%EC%BA%90%EB%A6%AD%ED%84%B0-%EB%A3%B8-%EA%B3%B5%ED%86%B5-%EA%B3%A8%EA%B2%A9-%EC%A0%9C%EC%95%88)
- **참조 근거:** `snowmerak/q` (`main`) `tools/README.md` · `tools/builtin/register.go` · `docs/browser-control.md` · `loom/`

---

## 1. 맥락 및 동인 (Context & Drivers)

q v2는 캐릭터챗 봇이면서도 사용자의 실질적인 로컬·웹·지식 작업을 수행해야 합니다. 이를 위해 Q v1(`main`)에서 검증된 도구군 중 IDE/코딩 리포지토리 전용 도구를 제외한 **7개 도구 영역**을 1차 범위로 채택했습니다.

동시에, 파일 수정이나 셸 명령·브라우저 제어 같은 부수효과가 있는 도구를 캐릭터 대화 흐름 안에서 안전하게 실행하기 위해 사용자 제어 가능한 승인 경계와 대용량 출력 보호 장치가 필요합니다.

---

## 2. 검토한 대안 (Considered Options)

### 대안 1: 단일 `pkg/task` 실행기 + Decisions 연동 3단계 승인 게이트 + `browser_control` config *(채택)*
- 모든 도구(내장 7개 영역 + 외부 MCP)를 `pkg/task`의 공통 인터페이스·승인 게이트·Loom 파이프라인으로 통과시킵니다.
- 승인 모드는 `bypass`, `auto-detection`(Decisions 판정), `request` 3가지를 제공합니다.

### 대안 2: 정적 화이트리스트/정규식 규칙 기반 승인 게이트
- **기각 사유:** 셸 명령 인자(`run_command`), MCP 외부 서버 도구, 브라우저 동작처럼 컨텍스트에 따라 위험도가 달라지는 호출은 정적 이름/패턴만으로는 과차단 또는 우회가 발생합니다.

### 대안 3: 고정 단일 정책 (전부 자동 또는 전부 수동 승인)
- **기각 사유:** 전부 자동은 파괴적 로컬 작업 시 위험하고, 전부 수동은 `read_file`·`search_archive` 같은 무해한 조회마다 대화가 끊겨 봇 UX를 크게 해칩니다.

---

## 3. 결정 (Decision)

### 3.1 1차 내장 도구 카탈로그 [확정]
1. **파일·디렉터리 작업:** `read_file`, `edit_file`, `write_file`, `list_directory`, `create_directory`, `move_path`, `copy_path`, `remove_path`
2. **셸·커맨드 실행:** `run_command`, `cmd_status`, `wait`
3. **아카이브·기억/지식 조회:** `search_archive`, `get_archive_record`
4. **Loom 아티팩트:** `loom_inspect`, `loom_read`, `loom_eval`
5. **Agent Skills:** `search_skills`, `get_skill`
6. **외부 MCP & 브라우저 제어:** 외부 MCP 서버 동적 도구 + `browser_control` config 기반 드라이버(`cdp-q-driver` / `ego-browser`)
7. **오케스트레이션·상호작용:** `ask_to_user` + 서브에이전트/작업 위임(`task_start`, `task_complete`, `delegate_*` 등)

> **제외 확정:** LSP 7종(`lsp_*`), Git worktree/PR 리뷰(`change_request_read`, `review_task`), 이슈/티켓(`ticket_question`) 등 IDE·저장소 제어면 전용 도구는 포함하지 않습니다.

### 3.2 3단계 승인 게이트 (`ApprovalGate`) [확정 + 제안]
모든 도구 호출은 실행 전 아래 설정 모드(전역 기본값 또는 세션/봇/도구별 오버라이드)에 따라 게이트를 거칩니다.

| 모드 | 동작 | 비고 |
| --- | --- | --- |
| `bypass` | 사용자 승인 없이 즉시 실행 | 신뢰된 샌드박스나 자동화 세션용 |
| `auto-detection` | **Decisions** API에 도구명, 분류(`read`/`write`/`exec`/`external`), 인자 요약을 질의해 승인이 필요하다고 판정된 호출만 사용자 승인 대기 | **[제안]** Decisions 호출 실패 시 안전을 위해 fail-closed(사용자 승인 요청으로 전환). 읽기 전용 조회(`read_file`, `search_archive`, `loom_read`, `search_skills` 등)는 메타데이터상 저위험으로 분류 |
| `request` | 모든 도구 호출에 대해 사용자 승인 요청 카드 표시 및 대기 | 최대 통제 모드 |

- 승인 대기 또는 `ask_to_user` 대기 중에는 **RDB 트랜잭션/행 락을 점유하지 않고** 대기 상태를 체크포인트에 저장합니다. 사용자가 UI에서 승인/거절/응답을 제출하면 체크포인트에서 턴을 재개합니다.

### 3.3 `browser_control` 설정 계약 [확정]
Q v1(`main:docs/browser-control.md`)과 동일한 설정 구조를 채택합니다.
- 설정에서 `enabled`, `preferred`(`cdp-q-driver` | `ego-browser`), 드라이버별 `command`와 `args` 문자열 배열을 분리해 지정합니다.
- `cdp-q-driver` 선택 시 별도 `command`가 비어 있으면 통합된 `pkg/cdp` 또는 `cmd/cdp-q-driver` 진입점을 사용하고, `ego-browser` 선택 시 설정된 외부 실행 파일/인자를 사용합니다.

### 3.4 안전 경계 및 Loom 아티팩트 처리 [제안]
- **파일 도구 경계:** Q v1처럼 설정된 작업 루트(또는 허용 디렉터리 목록) 기준으로 어휘적 `..` 탈출과 심링크 탈출을 차단합니다. 전역 데이터 디렉터리(`~/.q/` 등) 내부 DB/아티팩트 원본 파일에 대한 직접 쓰기는 보호합니다.
- **셸 실행 경계:** 비동기 `run_command` → `cmd_status`/`wait` 모델을 유지하고, 출력 버퍼 상한(예: Q v1 기준 4 MiB 보존, 청크당 최대 256 KiB)과 타임아웃·사용자 `Stop` 취소 시 프로세스 트리 종료를 보장합니다.
- **Loom 대용량 출력 분리:** `run_command`, `read_file`, MCP, 브라우저 스냅샷 등의 대용량 출력은 Blob Store에 불변 아티팩트로 저장하고, 모델 프롬프트에는 요약·미리보기와 아티팩트 ID만 주입해 컨텍스트 오염을 방지합니다. 후속 분석은 `loom_inspect`/`loom_read`/`loom_eval`로 수행합니다.
- **서브에이전트 위임 상한:** 무한 재귀 위임을 막기 위해 최대 위임 깊이와 턴당 호출 상한을 둡니다.

---

## 4. 결과 및 리스크 (Consequences & Risks)

### 장점
- 봇이 일상 대화와 실질적 작업(로컬 파일, 셸, 웹 탐색, MCP, 지식 조회)을 일관된 단일 루프에서 수행할 수 있습니다.
- `auto-detection` 모드를 통해 읽기/조회 작업은 끊김 없이 진행하고 위험 작업만 사용자 확인을 받는 균형을 달성할 수 있습니다.

### 비용 및 남은 리스크
- **`auto-detection` 오판(과차단/미탐):** Decisions 모델이나 프롬프트 구성에 따라 안전 작업이 막히거나 위험 인자가 통과될 수 있습니다. 고위험 도구에 대해서는 설정에서 강제 `request` 고정을 허용해야 합니다.
- **로컬 셸/파일 부수효과:** Git worktree 격리가 v2에서 제외되었으므로 파일 수정·삭제(`edit_file`/`write_file`/`remove_path`)는 실제 로컬 경로에 바로 적용됩니다. 따라서 기본 승인 모드를 `bypass`가 아닌 `auto-detection` 또는 `request`로 두는 것이 안전합니다.

### 확인 및 검증 기준
- [ ] 3가지 승인 모드(`bypass` / `auto-detection` / `request`) 각각에 대해 허용/거절/타임아웃 통합 테스트 통과.
- [ ] `browser_control` config 전환 시 `cdp-q-driver`와 `ego-browser` 간 드라이버 선택이 런타임에 반영됨을 검증할 것.
- [ ] 대용량 도구 출력이 프롬프트에 통째로 들어가지 않고 Loom 아티팩트 참조로 치환되며 `loom_read`/`loom_eval`로 조회 가능함을 검증할 것.
