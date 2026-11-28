# q v2 프로젝트 청사진 (Blueprint)

- **작성일:** 2026-11-28
- **상태:** 초안 (Draft — 확정 결정 / 제안 설계 / 미결 항목을 구분하여 표기)
- **관련 문서:** [제품 요구사항 정의서 (PRD)](./prd.md)
- **대상 독자:** q v2 백엔드, 저장소, 프런트엔드, 데스크톱 앱 설계 및 구현자
- **갱신 조건:** 시스템 경계, 저장소 권위 및 복구 모델, 주요 실행 흐름, 모듈 구성, 마일스톤 방향이 바뀔 때 갱신한다.

> [!IMPORTANT]
> **현재 상태 안내:** `v2` 브랜치는 기존 v1 코드를 정리한 **문서 선행 단계**이며, 아직 v2 애플리케이션 코드나 테스트는 구현되지 않았습니다. 본 문서에서 **[확정]** 표기는 사용자 합의를 반영한 방향, **[제안]** 표기는 구현 전 검증 및 ADR로 확정이 필요한 설계안, **[미결]** 표기는 추가 논의로 정할 세부 항목입니다.

---

## 1. 목적 및 기존 자산 선별 이식 맵

q v2는 기존 저장소(**Evernight**, **q v1**, **llm-provider**, **cdp-q-driver**)의 검증된 구성요소를 **단일 인스턴스 기반의 캐릭터챗 + 다중 작업 수행 봇**이라는 정체성에 맞게 재조합합니다.

| 출처 저장소 | 가져올 핵심 자산 (선별 이식 / 코드 통합) | 가져오지 않을 자산 (v2 제외 확정) | 어댑팅 포인트 |
| --- | --- | --- | --- |
| **Evernight** (`rileyayla/evernight`) | - 캐릭터·페르소나 스냅샷 모델<br>- 1:1(`SimpleChatLoop`) 및 다중 참여자(`PacedLoop`) 턴 조율 개념<br>- **Decisions**(`github.com/snowmerak/decisions`) 기반 이산 판단 패턴<br>- `observedBy` 기반 인지 격리 및 실시간 끼어들기/중단(`InterceptHub`)<br>- 장기기억 회상 파이프라인(게이트→플래너→검색→선별) 및 시간 감쇄<br>- 캐릭터 가치관/상태/관계 백그라운드 정리 워커<br>- 단계별 체크포인트·멱등 `requestId` 복구 패턴 | - SvelteKit 서버/UI 코드 (Go + React로 전환)<br>- **3인칭 연재 소설(`prose`) 재집필 파이프라인 및 문체 프리셋**<br>- OpenSearch를 원문 권위 저장소로 쓰는 구조 (RDB 권위로 통합 제안)<br>- 다중 서버용 분산 Lease 경합 전제 | 3인칭 산문 집필을 제거하고 **채팅 버블 중심** 대화 루프로 단순화하며, 도구(작업) 호출 및 승인 게이트를 같은 턴 상태 머신 위에서 처리 |
| **q v1** (`snowmerak/q` `main`) | - 도구 실행 루프의 안전 경계(타임아웃, 취소, 호출 상관관계, 중단 복구)<br>- 대용량 도구 출력 불변 아티팩트 분리(Loom 패턴)<br>- MCP 클라이언트 및 **`browser_control` config 패턴**<br>- **Bleve(BM25) + HNSW(벡터)** 임베디드 하이브리드 검색 패턴<br>- 지식 저장소 청킹·리비전·Outbox 재색인 패턴<br>- `storageconfig` 패턴 및 `desktop/` Wails v3 패키징 경험 | - Git worktree 위임 코디네이터, 이슈/티켓 칸반, Commit 에이전트<br>- LSP, TUI(Bubbletea), ACP SDK 포크<br>- **다중 프로세스 동시 실행·리더 선출·테이크오버(`workspace-memory`/`library`)**<br>- 루트 ~40개 패키지 분산 구조 & Svelte Studio UI<br>- 프로젝트 폴더별 `.q/` 분산 저장 | 다중 프로세스 락·프록시 구조를 제거해 **단일 인스턴스** 인프로세스 소유 모델과 **전역 단일 데이터 디렉터리**로 단순화하고, `pkg/` 아래 계층화 |
| **llm-provider** (`snowmerak/llm-provider`) | - 통합 Go LLM 인터페이스, OpenAI 호환 게이트웨이, 모델 목록 캐시/`fsnotify` hot-reload<br>- 모델 메타데이터·능력(`capabilities`: 도구 호출, 추론 강도, `context_length`), 프롬프트 캐시 정규화, 임베딩 | — | **레포 내 `pkg/llm` + `cmd/llm-provider`로 직접 코드 통합.** Q 코어가 라이브러리로 임베드하는 동시에 외부 게이트웨이·독립 CLI·공개 Go 패키지로도 사용 가능하게 유지 |
| **cdp-q-driver** (`snowmerak/cdp-q-driver`) | - Chrome/Chromium CDP 브라우저 제어 라이브러리 및 CLI | — | **레포 내 `pkg/cdp` + `cmd/cdp-q-driver`로 직접 코드 통합.** Q 코어가 직접/CLI로 호출하는 동시에 독립 CLI·공개 Go 패키지로 배포 가능하게 유지 |

---

## 2. 시스템 컨텍스트 및 런타임 토폴로지

### 2.1 시스템 컨텍스트 다이어그램 **[확정 + 제안]**

```mermaid
flowchart TB
    subgraph Entrypoints["공유 코어 기반 실행 형태 & 독립 CLI"]
        Desktop["Wails v3 Desktop App<br/>(desktop/go.mod → pkg/app 인프로세스 실행)"]
        ServerCLI["q Server / CLI<br/>(cmd/q → pkg/app 직접 실행)"]
        StandaloneCLIs["독립 배포 CLI<br/>(cmd/llm-provider · cmd/cdp-q-driver)"]
        Browser["Web / Future Mobile Client<br/>(React + shadcn/ui SPA)"]
    end

    subgraph Core["q v2 Shared Core (pkg/* — Single-Instance)"]
        App["pkg/app (HTTP/SSE API + Embedded web/dist + Config)"]
        Runtime["pkg/runtime (1:1 · Multi-Character Bubble · Task Loop)"]
        Task["pkg/task (Approval Gate: bypass/auto-detection/request · MCP · browser_control)"]
        Memory["pkg/memory & pkg/knowledge (Recall · Values · Hybrid Search)"]
        LLM["pkg/llm (Integrated llm-provider) & Decisions Client"]
        StoragePort["pkg/storage (RDB · Search · Blob Ports)"]
    end

    subgraph Storage["단일 전역 데이터 디렉터리 (~/.q 또는 --data-dir) / 원격 DB"]
        RDB[("RDB Authority: PostgreSQL / MySQL / SQLite")]
        Search[("Search Projection: OpenSearch / Bleve + HNSW")]
        Blob[("Artifact & Media Blob (Local FS / S3)")]
    end

    subgraph External["외부 모델 & 확장 도구"]
        UpstreamLLM["Upstream LLM / Embedding Backends"]
        Dec["Decisions API (github.com/snowmerak/decisions)"]
        Tools["MCP Servers · Browser (pkg/cdp 또는 ego-browser)"]
    end

    Desktop -->|"In-process Go import (pkg/app)"| App
    ServerCLI -->|"In-process Go import (pkg/app)"| App
    Desktop -.->|"내장 WebView (HTTP/SSE)"| App
    Browser -->|"HTTP/SSE"| App

    App --> Runtime
    Runtime --> Task
    Runtime --> Memory
    Runtime --> LLM
    Runtime --> StoragePort
    Task --> Tools
    Task --> Dec
    Memory --> LLM
    Memory --> StoragePort

    StoragePort --> RDB
    StoragePort --> Search
    StoragePort --> Blob

    LLM --> UpstreamLLM
    LLM --> Dec
```

### 2.2 실행 모델 및 코어 공유 원칙

1. **단일 인스턴스(Single-instance) 전제 [확정]**:
   - q v2 코어 엔진은 **하나의 프로세스만 떠서 실행되는 것**을 전제합니다.
   - 기존 q v1의 다중 프로세스 간 리더 선출, HTTP 팔로워 프록시, `library.lock` / `workspace-memory.lock` 기반 다중 소유자 테이크오버는 구현하지 않습니다.
   - 동일 전역 데이터 디렉터리에 두 프로세스가 동시에 붙는 사고를 방지하는 **단순 싱글 인스턴스 기동 가드(lock)**만 둡니다.
2. **Server CLI(`cmd/q`)와 Wails v3(`desktop/`)의 동일 코어 공유 [확정]**:
   - `pkg/app`가 코어 엔진 초기화, 단일 인스턴스 가드, HTTP/SSE 서버, 임베드된 React SPA 서빙을 모두 소유합니다.
   - `cmd/q`와 `desktop/`(Wails v3)은 **별도 자식 프로세스 통신 없이 동일한 `pkg/app`을 인프로세스로 직접 구동**합니다.
   - `desktop/`만 별도 `desktop/go.mod`로 분리해 Wails v3 네이티브 의존성(WebView2/WKWebView/GTK4+WebKitGTK)이 루트 Go 모듈과 `cmd/q`, `cmd/llm-provider`, `cmd/cdp-q-driver` 빌드에 유입되지 않도록 격리합니다.
3. **전역 단일 데이터 디렉터리 + `--data-dir` [확정]**:
   - 로컬 실행 시 설정(`config`), `SQLite` DB, `Bleve + HNSW` 인덱스, `blobs` 아티팩트를 전역 단일 데이터 디렉터리(예: `~/.q/` 또는 OS 앱 데이터 경로) 한곳에 보관하며, `--data-dir` 플래그로 경로를 재정의할 수 있습니다.
4. **모바일 확장 대비 [확정]**:
   - 모든 대화 상태, 체크포인트, 기억, 도구 실행과 승인 상태는 `pkg/app` 코어 엔진이 소유하므로, 추후 모바일 웹(PWA)이나 React Native 앱을 추가할 때 HTTP/SSE API 경계만 그대로 연결하면 됩니다.

---

## 3. 핵심 도메인과 공개 패키지 경계 (Building Blocks) **[확정 + 제안]**

외부 Go 프로젝트에서도 import할 수 있도록 루트 디렉터리에 패키지를 나열하지 않고 **`pkg/` 아래 도메인/서브시스템 단위로 계층화**합니다.

| 공개 패키지 | 소유 책임 | 주요 입력 / 출력 | 의존 방향 |
| --- | --- | --- | --- |
| `pkg/domain` | 순수 엔티티·값 객체 (`Character`, `Persona`, `Room`, `MessageBubble`, `Turn`, `Event`, `Memory`, `Value`, `KnowledgeDoc`, `Artifact`, `ApprovalPolicy`) 및 불변식 검증 | 외부 프레임워크·DB에 비종속 | 의존 없음 (최하위 핵심) |
| `pkg/runtime` | 1:1 채팅 버블 루프, 멀티캐릭터 룸 스케줄러(Decisions 연동), `observedBy` 인지 격리, 실시간 인터럽트/취소, Append-only 프롬프트 조립·압축, 턴 체크포인트 | 사용자 입력, 룸 상태 → 턴 이벤트, 캐릭터 채팅 버블 응답, 작업 요청 | `pkg/domain`, `pkg/llm`, `pkg/memory`, `pkg/task`, `pkg/storage` |
| `pkg/task` | 대화 중 도구 호출 루프, **3단계 승인 게이트(`bypass` / `auto-detection` / `request`)**, 타임아웃·취소, Loom 아티팩트 저장, **MCP 및 `browser_control` config 기반 브라우저(`pkg/cdp` / `ego-browser`) 어댑터** | 모델의 tool call → 승인 판정/대기 → 도구 실행 결과 요약 + 아티팩트 ID | `pkg/domain`, `pkg/cdp`, `pkg/storage` |
| `pkg/memory` | 기억 회상(Decisions 게이트 → 쿼리 플래너 → 하이브리드 검색 → Decisions 선별), 개인/룸 기억 생성, 백그라운드 워커(가치관·상태·관계 정리) | 확정 턴 이벤트, 검색 쿼리 → 주입용 기억 세트, 갱신된 가치관/상태 | `pkg/domain`, `pkg/llm`, `pkg/storage` |
| `pkg/knowledge` | 사용자·봇 지식 문서 수집, 청킹, 리비전 관리, 하이브리드 검색 | 문서/노트/파일 → 검색 가능한 지식 청크 | `pkg/domain`, `pkg/llm`, `pkg/storage` |
| `pkg/llm` | **통합된 `llm-provider` 코어**, 역할별 모델 라우팅, **`decisions` 클라이언트 통합**, 모델 `capabilities` 노출, 설정 hot-reload | 역할·메시지·스키마·도구 목록 → 스트림/완성 응답, 임베딩 벡터, Decisions 판정 | `pkg/domain`, `decisions` |
| `pkg/cdp` | **통합된 `cdp-q-driver` 코어** (CDP 세션·페이지·스크립트 제어 및 Q 내장/독립 CLI 공유 진입점) | 브라우저 제어 명령 → DOM/스냅샷/실행 결과 | 외부 Go 표준·CDP 라이브러리만 의존 |
| `pkg/storage` | RDB 포트(`PostgreSQL`/`MySQL`/`SQLite`), Search 포트(`OpenSearch`/`Bleve + HNSW`), Blob 포트(로컬 FS/S3) 및 Outbox 구현 | 도메인 레코드, 색인 문서/벡터, 검색 질의 → 영속화 결과, 하이브리드 랭킹 | `pkg/domain` |
| `pkg/app` | 의존성 조립, 전역 데이터 경로(`--data-dir`), 싱글 인스턴스 가드, HTTP/SSE 서버, Embedded `web/dist` 서빙 (`cmd/q`와 `desktop/`이 공통 import) | 설정/플래그 → 실행 가능한 엔진 인스턴스 | `pkg/*` |

---

## 4. 조합 가능한 저장소 아키텍처 (Data Authority & Recovery View)

### 4.1 권위 저장소(RDB)와 검색 프로젝션(Search)의 분리 **[제안]**

Evernight은 확정 본문·기억의 원문을 OpenSearch `_source`에 두었으나, q v2는 **PostgreSQL / MySQL / SQLite**와 **OpenSearch / Bleve + HNSW**의 자유로운 조합을 요구합니다. 백엔드 조합과 무관하게 원문 내구성과 트랜잭션 일관성을 동일하게 유지하기 위해 다음과 같이 책임을 분리합니다.

- **단일 진실원 (Authority) = RDB (`PostgreSQL` / `MySQL` / `SQLite`)**
  - 캐릭터, 페르소나, 룸, 컨텍스트, 턴/채팅 버블 원문, 구조화 이벤트(`observedBy` 포함), 기억 원문, 가치관, 지식 문서 메타/원문, 체크포인트, Outbox를 단일 RDB 트랜잭션으로 커밋합니다.
- **파생 프로젝션 (Projection) = Search (`OpenSearch` / `Bleve + HNSW`)**
  - Search 저장소는 어휘 검색(BM25)과 시맨틱 벡터 유사도 검색을 결합한 하이브리드 검색을 담당하는 **파생 인덱스**로 취급합니다.
  - RDB의 `index_outbox` 테이블을 소비해 비동기로 색인하며, Search 백엔드를 교체하거나(`Bleve + HNSW` ↔ `OpenSearch`) 임베딩 모델/차원을 바꿀 때 RDB 원문으로부터 전체 재색인이 가능합니다.
- **대용량 바이너리/출력 = Blob Store**
  - 이미지, 첨부 파일, 장문 도구 출력(Loom 아티팩트) 본문은 전역 데이터 디렉터리 내 로컬 디스크(또는 S3 호환 저장소)에 불변 저장하고, RDB에는 메타데이터와 해시/참조만 기록합니다.

### 4.2 지원 백엔드 조합 및 기술 특성

| 계층 | 백엔드 | 적합 환경 | 단일 인스턴스 내 동작 특성 | 주의사항 및 설계 포인트 |
| --- | --- | --- | --- | --- |
| **RDB (Authority)** | **SQLite** | 단일 데스크톱 앱, 제로 설정 로컬 실행 | WAL 모드 + 단일 프로세스 내 쓰기 직렬화 | 외부 DB 없이 동작. SQL 방언과 JSON 컬럼 처리 추상화 필요 |
| **RDB (Authority)** | **PostgreSQL** | 로컬 컨테이너 또는 기존 PostgreSQL 서버 연동 | 풀 기반 트랜잭션 + 행 단위 상태 업데이트 | 스키마 마이그레이션 시 MySQL/SQLite와 공통 SQL 또는 드라이버별 쿼리셋 유지 필요 |
| **RDB (Authority)** | **MySQL** | 기존 MySQL 인프라 연동 | MySQL 8+ 트랜잭션 + `utf8mb4` JSON 저장 | `json` 정렬/인덱스/UTF-8 규칙을 PostgreSQL과 맞추는 어댑터 테스트 필수 |
| **Search (Projection)** | **Bleve + HNSW** | 외부 검색 서버 없는 로컬 임베디드 하이브리드 검색 | Go 코어 프로세스가 Bleve(BM25)와 HNSW(벡터) 파일 핸들을 단독 소유 (단일 인스턴스 전제이므로 멀티프로세스 락 경합 없음) | 임베딩 모델/차원 변경 시 HNSW 인덱스 세대 교체·재색인 및 정상 종료/체크포인트 영속화 설계 필요 |
| **Search (Projection)** | **OpenSearch** | 대용량 기억·지식, 서버형 하이브리드 검색 | HTTP REST API 기반 BM25 + k-NN 하이브리드 검색 | 외부 OpenSearch 클러스터/컨테이너 필요. 임베딩 차원·리비전별 인덱스 분리 관리 |

### 4.3 동시성, 복구 및 멱등성 계약 **[제안]**

단일 인스턴스 전제를 적용함으로써 다중 프로세스 분산 락을 제거하고 복구 모델을 단순화합니다.

1. **인프로세스 룸/세션 직렬화 + 비동기 중단**: 단일 코어 엔진 내부에서 세션/룸별 실행 락과 취소(`InterceptHub`)를 관리합니다. **긴 LLM/도구 네트워크 호출이나 승인 대기 동안에는 RDB 트랜잭션이나 행 잠금을 점유하지 않습니다.**
2. **단계별 체크포인트 & 멱등 `requestId`**: 사용자 턴 요청은 클라이언트 `requestId`와 입력 지문을 RDB에 기록합니다. 앱 비정상 종료·재시작 후 같은 `requestId`로 재시도하면 이미 완료된 모델/도구/승인 단계 결과를 체크포인트에서 재사용하며, 다른 입력이 들어오면 `409 Conflict`로 거절합니다.
3. **Outbox 기반 색인 복구**: 턴이나 지식이 RDB에 커밋되면 같은 트랜잭션에 `index_outbox` 레코드를 남깁니다. Search나 임베딩 서버의 일시 장애가 발생해도 대화 커밋을 막지 않으며, 복구 후 Outbox 워커가 지연된 색인을 따라잡습니다.

---

## 5. 주요 실행 흐름 (Major Flows)

### 5.1 턴 실행 및 도구 승인 게이트 흐름 (1:1 · 멀티캐릭터 룸 공통 골격) **[제안]**

```mermaid
sequenceDiagram
    actor User as 사용자 (React / Wails UI)
    participant RT as pkg/runtime
    participant RDB as RDB (Authority)
    participant Search as Search (Bleve+HNSW / OpenSearch)
    participant LLM as pkg/llm
    participant Dec as Decisions API
    participant Tool as MCP / browser_control Runner

    User->>RT: 메시지 전송 (sessionId, requestId, text)
    RT->>RDB: 요청 체크포인트 생성 (또는 재개 조회)
    RT->>Dec: 기억 회상 게이트 확인
    RT->>Search: 기억 · 지식 하이브리드 검색 (observedBy 필터)
    Search-->>RT: 후보 기억 및 지식 청크
    RT->>Dec: 회상 후보 선별

    alt 멀티캐릭터 룸인 경우
        RT->>Dec: 참여 · 발화 순서 조율
    end

    loop 캐릭터 응답 또는 도구(작업) 실행 (실시간 Stop/Cut-in 감시)
        RT->>LLM: Append-only 컨텍스트 + 기억 + 사용 가능한 도구 전달
        alt 모델이 도구 호출 요청
            LLM-->>RT: tool_call
            alt 승인 모드 = bypass
                RT->>Tool: 즉시 실행 (MCP 또는 browser_control 선택 드라이버)
            else 승인 모드 = auto-detection
                RT->>Dec: 실행 위험도/승인 필요 여부 판단
                alt Decisions가 승인 필요로 판정
                    RT-->>User: 승인 요청 카드 표시 및 대기
                    User-->>RT: 승인 또는 거절
                end
            else 승인 모드 = request
                RT-->>User: 승인 요청 카드 표시 및 대기
                User-->>RT: 승인 또는 거절
            end
            Tool-->>RT: 실행 결과 요약 + 대용량 아티팩트 참조
            RT-->>User: SSE 작업 상태 스트리밍
        else 채팅 버블 응답 생성
            LLM-->>RT: 응답 토큰/본문
            RT-->>User: SSE 캐릭터 채팅 버블 스트리밍
        end
    end

    RT->>RDB: 채팅 버블 원문, 이벤트(observedBy), 체크포인트 완료, index_outbox 원자적 커밋
    par 백그라운드 워커 (비동기)
        RDB-->>Search: Outbox 소비 → 기억/지식 색인(BM25 + 벡터) 반영
        RDB-->>LLM: 개인 기억 생성 · 가치관/상태/관계 정리 → RDB 커밋
    end
```

### 5.2 Outbox 기반 검색 프로젝션 및 재색인 흐름 **[제안]**

```mermaid
flowchart LR
    Commit["RDB 트랜잭션 커밋<br/>(채팅 버블 · 기억 · 지식 원문 + index_outbox)"] --> Worker["Outbox Indexer Worker"]
    Worker -->|"임베딩 설정 활성 시"| Emb["Embeddings API"]
    Emb -->|"성공"| Adapter["Search Adapter<br/>(Bleve+HNSW 또는 OpenSearch)"]
    Emb -.->|"일시 실패 시 키워드만 색인 + 재시도 예약"| Adapter
    Reindex["임베딩 모델 변경 / Search 백엔드 교체"] -->|"RDB 원문 스캔"| Worker
```

---

## 6. 프런트엔드 및 클라이언트 아키텍처 **[확정 + 제안]**

### 6.1 웹 UI 스택 및 빌드/임베드 정책

- **기술 스택 [확정]:** **React**, **TypeScript**, **Vite**, **Tailwind CSS**, **shadcn/ui** 기반 컴포넌트 라이브러리.
- **테마 시스템 [확정]:** shadcn/ui의 시맨틱 CSS 변수 토큰(`background`, `foreground`, `card`, `muted`, `border`, `primary` 등)을 기준으로 **라이트 모드 / 다크 모드 / 시스템 설정 따름**을 초기부터 내장합니다.
- **빌드 산출물(`web/dist`) 정책 [확정]:** `web/dist`는 `.gitignore`에 두어 Git 이력에 커밋하지 않습니다. `task build` 및 릴리스 빌드 과정에서 React 앱을 빌드한 뒤 Go `embed`로 `pkg/app`에 내장합니다(개발 중에는 Vite dev server가 `/api`를 코어 엔진으로 프록시).
- **클라이언트 계층 분리 [제안]:**
  - `web/src/client/`: 프레임워크 비종속 TypeScript HTTP/SSE 클라이언트, 요청 `requestId` 생성기, 스트림/승인 이벤트 파서, 공유 타입.
  - `web/src/features/`: 대화(1:1 및 룸), 도구 승인/아티팩트 뷰, 캐릭터/페르소나 편집기, 지식 저장소 브라우저, 모델·저장소·`browser_control`·승인 정책 설정.

### 6.2 데스크톱 앱 (Wails v3) 및 모바일 방향

- **데스크톱 [확정]:** 별도 자식 엔진 프로세스 없이 **`desktop/`(Wails v3)이 `pkg/app`을 인프로세스로 직접 실행**합니다. Windows(NSIS + WebView2), macOS(DMG), Linux(DEB/RPM, GTK4 + WebKitGTK 6.0) 패키징과 창 닫기 시 트레이 상주, 로그인 시작을 지원합니다. 도입 시점에 Wails v3의 구체 버전을 `desktop/go.mod`에 고정합니다.
- **모바일 [미결 — 추후 검토]:** 1차 구현에서는 모바일 전용 빌드를 만들지 않되, (1) 반응형 레이아웃으로 작은 뷰포트 대응을 열어두고, (2) `pkg/app` HTTP/SSE 경계를 통해 추후 모바일 클라이언트를 연결할 수 있도록 유지합니다.

---

## 7. 프로젝트 구성 **[확정 — 세부 파일 배치와 멀티모듈 여부는 M1에서 미세 조정]**

공개 라이브러리 중심(`pkg/`) 계층화, `llm-provider`/`cdp-q-driver` 코드 통합 및 독립 CLI 제공, `cmd/q`와 `desktop/`의 `pkg/app` 공유 원칙을 반영한 확정 구조입니다.

```text
q/                              # Go module: github.com/snowmerak/q/v2
├── go.mod
├── .gitignore                  # web/dist, bin, .q 등 제외
├── Taskfile.yml                # 빌드·테스트·프런트엔드·독립 CLI·데스크톱 패키징
├── cmd/
│   ├── q/                      # 메인 서버/CLI (pkg/app 인프로세스 실행)
│   ├── llm-provider/           # 독립 배포용 LLM Gateway CLI (pkg/llm 기반)
│   └── cdp-q-driver/           # 독립 배포용 CDP 브라우저 CLI (pkg/cdp 기반)
├── pkg/                        # 외부 import 가능한 공개 Go 패키지 계층
│   ├── app/                    # 코어 엔진 조립, 전역 --data-dir, 싱글 인스턴스 가드, HTTP/SSE + Embedded web/dist
│   ├── domain/                 # Character, Persona, Room, MessageBubble, Memory, Value, Knowledge
│   ├── runtime/                # 1:1 · 멀티캐릭터 버블 루프, Decisions 조율, observedBy, 인터럽트
│   ├── task/                   # 도구 루프, 승인 게이트(bypass/auto-detection/request), MCP, browser_control, Loom
│   ├── memory/                 # 기억 회상, 기억/가치관/상태·관계 백그라운드 워커
│   ├── knowledge/              # 지식 문서 수집, 청킹, 하이브리드 검색
│   ├── storage/                # RDB / Search / Blob 포트 인터페이스 및 계약 테스트
│   │   ├── rdb/                # sqlite, postgres, mysql 어댑터
│   │   ├── search/             # bleve+hnsw(임베디드), opensearch 어댑터
│   │   └── blob/               # localfs, s3 어댑터
│   ├── llm/                    # 통합된 llm-provider 공개 라이브러리 + Decisions 클라이언트
│   └── cdp/                    # 통합된 cdp-q-driver 공개 라이브러리
├── internal/                   # 외부 비공개 최소 내부 유틸리티
├── web/                        # React + TypeScript + Vite + Tailwind + shadcn/ui (dist/는 미커밋)
├── desktop/                    # 별도 Go 모듈 (desktop/go.mod) — Wails v3 셸 (pkg/app 직접 import)
└── docs/
    ├── prd.md                  # 제품 요구사항 정의서
    ├── blueprint.md            # 본 프로젝트 청사진
    └── adr/                    # 아키텍처 결정 기록 (예정)
```

> [!NOTE]
> **Go 모듈 경계 세부 결정 노트 (M1 착수 시 확정):** `pkg/llm`과 `pkg/cdp`를 루트 `go.mod`(`github.com/snowmerak/q/v2`) 단일 모듈 안에 둘지, 외부 소비자가 RDB/Bleve 의존성 없이 `llm-provider` 또는 `cdp-q-driver`만 가볍게 가져갈 수 있도록 하위 `go.mod` 멀티모듈(또는 `go.work`)로 분리할지는 M1 착수 시 `ADR-0004`에서 최종 결정합니다.

---

## 8. 단계별 마일스톤, 리스크 및 후속 ADR **[제안]**

### 8.1 제안 마일스톤

| 마일스톤 | 목표 범위 | 완료 기준(초안) |
| --- | --- | --- |
| **M0. 기반 정렬** | `v2` 브랜치 정리, PRD/Blueprint 확정, 핵심 ADR 작성 | 모듈 경계(`ADR-0004`) 및 저장소/브라우저/룸 상세 ADR 합의 |
| **M1. 코어 골격 & 통합 패키지 & 최소 저장소** | `pkg/llm` + `cmd/llm-provider`, `pkg/cdp` + `cmd/cdp-q-driver` 코드 이식, `pkg/domain`, `pkg/storage` 포트 + `SQLite` / `Bleve + HNSW`, `pkg/app` + `cmd/q` 골격 | 독립 CLI 2종 빌드, 저장소 계약 테스트 통과, 단일 인스턴스로 LLM/Decisions/하이브리드 검색 동작 |
| **M2. 1:1 캐릭터 봇 + 기본 UI** | 캐릭터/페르소나 CRUD, 1:1 대화 루프, 기억 회상/가치관 기초, React + shadcn/ui(라이트/다크) 채팅 버블 화면, `web/dist` 빌드 임베드 | 1:1 대화, 테마 전환, 실패 요청 재시도, `web/dist` 임베드 빌드 검증 |
| **M3. 작업 실행 & 승인 게이트 & 지식** | `pkg/task` 도구 루프 + MCP + `browser_control`(`cdp-q-driver`/`ego-browser`) + 3단계 승인 게이트 + Loom 아티팩트, `pkg/knowledge` 수집·검색 | 승인 게이트 3모드(`bypass`/`auto-detection`/`request`), 브라우저 드라이버 전환, 지식 검색 검증 |
| **M4. 멀티캐릭터 룸 & 확장 저장소** | Decisions 기반 멀티캐릭터 채팅 버블 루프·`observedBy`·실시간 인터럽트, `PostgreSQL` / `MySQL` / `OpenSearch` 어댑터 완성 | 6종 저장소 조합 계약 테스트 및 룸 시나리오 검증 |
| **M5. Wails v3 데스크톱 & 다음 단계** | `desktop/` Wails v3 셸(`pkg/app` 인프로세스 공유)·트레이·패키징, 모바일 확장 방안 재검토 | 데스크톱 빌드 및 패키징 스모크 통과 |

### 8.2 주요 기술 리스크

| 리스크 | 영향 | 완화 방향 |
| --- | --- | --- |
| **공개 패키지의 전이 의존성 비대화** | 외부 사용자가 `pkg/llm`이나 `pkg/cdp`만 import하려 할 때 루트 모듈의 DB/검색 의존성이 함께 딸려올 수 있음 | `pkg/llm`, `pkg/cdp`가 상위 코어(`pkg/app`, `pkg/storage`)를 역참조하지 않도록 엄격히 분리하고, 필요 시 하위 `go.mod` 멀티모듈 분리 검토(`ADR-0004`) |
| **HNSW 영속화·재색인 및 임베딩 차원 변경 관리** | 로컬 `Bleve + HNSW` 조합에서 비정상 종료 시 Bleve와 HNSW 간 상태 불일치나 임베딩 모델 변경 시 차원 충돌 위험 | `index_outbox` 커서와 HNSW 세대 디렉터리 분리, 시작 시 정합성 검증 및 RDB 원문 기반 재색인 제공 |
| **승인 게이트 `auto-detection` 판정 오류** | Decisions가 위험 도구를 통과(미탐)시키거나 무해한 도구를 매번 차단(과차단)해 UX 저하 | 도구 메타데이터(읽기/쓰기/외부 부수효과 분류)를 Decisions 입력에 명시하고, 고위험 도구는 강제 `request` 정책 지정 가능하게 설계 |
| **RDB 3종(`SQLite`/`PostgreSQL`/`MySQL`) 동시 지원 비용** | SQL 방언, JSON 연산, 마이그레이션 차이로 유지보수 비용 증가 | 단일 인스턴스 전제로 분산 락 SQL을 제거하고, 공통 저장소 계약 테스트(Contract Test Suite)와 단순한 쿼리 계층 유지 |
| **캐릭터 페르소나 유지 vs 도구 호출 정합성** | 강한 역할극 프롬프트가 모델의 정확한 tool-call JSON/인자 생성을 방해하거나, 반대로 작업 응답 시 캐릭터 톤이 깨질 수 있음 | 내부 작업/판단 지침과 캐릭터 페르소나 영역 분리, 모델 `capabilities.tool_calling` 검증 |
| **멀티캐릭터 룸의 지연·비용·인터럽트 경합** | 캐릭터 수 증가 시 Decisions·생성·기억 호출이 증가하고 사용자 끼어들기 시 상태 오염 위험 | 룸당 활성 참여자·턴 예산 상한, 경량 판단 모델 분리, 인프로세스 락 + 체크포인트 + `InterceptHub` 원자적 중단 |

### 8.3 후속 작성할 ADR 후보 (`docs/adr/`)

1. **`ADR-0001`: RDB 단일 권위 + `Bleve + HNSW` / `OpenSearch` 파생 프로젝션 및 Outbox 재색인 계약**
2. **`ADR-0002`: 채팅 버블 기반 멀티캐릭터 룸 + Decisions 조율 및 `observedBy` 격리 프로토콜**
3. **`ADR-0003`: 도구 승인 게이트 3모드(`bypass` / `auto-detection` / `request`), MCP 및 `browser_control`(`cdp-q-driver` / `ego-browser`) 경계**
4. **`ADR-0004`: 단일 `go.mod` vs `llm`/`cdp` 하위 모듈 분리 및 공개 패키지 API 경계**
