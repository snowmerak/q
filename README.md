# q v2

> **상태:** `v2` 브랜치 초기 문서 선행 단계 (구현 전). 기존 v1 코드와 문서는 `main` 브랜치 및 `v0.5.0` 태그에 보존되어 있습니다.

**q v2**는 기존의 코딩 에이전트 및 워크스페이스 제어면(v1)을 리셋하고, **캐릭터챗 감각의 자연스러운 대화 상호작용을 우선하면서 여러 작업을 수행할 수 있는 봇**으로 재설계하는 프로젝트입니다.

## 핵심 방향

- **모듈 & 실행 전제:** `github.com/snowmerak/q/v2` (Go 모듈 v2). 코어 엔진은 **단일 인스턴스(Single-instance)** 프로세스 실행을 전제로 합니다.
- **상호작용 중심 (Evernight 선별 이식):**
  - 1:1 캐릭터 봇 대화와 다수 캐릭터가 참여하는 멀티캐릭터 룸(Room)을 모두 1차 범위로 지원하며, 산문(소설) 재집필 없이 **채팅 버블 중심의 직접 대화**로 진행합니다.
  - 캐릭터 성격·페르소나·장기기억·가치관·상태/관계 유지, `observedBy` 기반 인지 격리, 실시간 끼어들기/중단, **Decisions**(`github.com/snowmerak/decisions`) 기반 이산 판단, 체크포인트 복구 개념을 계승합니다.
- **작업 수행 & 승인 게이트 (Q v1 · Decisions 결합):**
  - 1차 작업 도구로 **기본 MCP 연동**과 **브라우저 제어(`cdp-q-driver` 또는 `ego lite`)**를 포함하며, 대용량 출력은 아티팩트(Loom 패턴)로 분리합니다.
  - 도구 실행 승인 게이트는 **`bypass`**(무조건 실행), **`auto-detection`**(Decisions 판정 후 필요한 경우만 승인 요청), **`request`**(전부 승인 요청) 3가지 모드를 제공합니다.
- **지식 저장소 & LLM 기반 (Q v1 · `llm-provider` 선별 이식):**
  - `github.com/snowmerak/llm-provider`를 통해 멀티 LLM 프로바이더, 모델 메타데이터/능력(`capabilities`, 추론 강도, 도구 호출, 임베딩) 라우팅을 연동합니다.
  - 조합 가능한 저장소: RDB(`PostgreSQL` / `MySQL` / `SQLite`)와 검색 엔진(`OpenSearch` / `Bleve + HNSW`)을 환경에 맞게 자유롭게 조합해 구성합니다.
- **클라이언트 & UI:** **React + TypeScript + shadcn/ui** 기반의 라이트/다크 모드 UI를 제공하며, 데스크톱은 **Wails v3** 앱을 1차 대상으로 하고 모바일은 추후 대응을 검토합니다.

## 문서

- [제품 요구사항 정의서 (PRD)](./docs/prd.md) — 제품 목표, 1차 범위(In-Scope / Out-of-Scope), `REQ-*` 요구사항 및 수용 기준, 미결 질문
- [프로젝트 청사진 (Blueprint)](./docs/blueprint.md) — 자산 재사용 맵, 단일 인스턴스 시스템 컨텍스트, 도메인 경계, 조합 가능한 저장소 구조(`RDB × OpenSearch/Bleve+HNSW`), 실행 흐름, 임시 디렉터리 구성안, 마일스톤 및 후속 ADR 목록
