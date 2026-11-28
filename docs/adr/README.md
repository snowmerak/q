# q v2 아키텍처 결정 기록 (ADR)

이 디렉터리는 `q v2`(`github.com/snowmerak/q/v2`)의 핵심 아키텍처 결정과 대안 비교, 근거 및 결과를 기록합니다.

## 번호 및 상태 규칙

- **번호 규칙:** `0001`부터 4자리 순번으로 부여하며, 기존 번호는 변경하거나 덮어쓰지 않습니다.
- **상태 표기:**
  - `확정 (Accepted)`: 사용자와 합의된 결정 사항.
  - `부분 확정 / 세부 제안`: 큰 구조와 방향은 확정되었으나, 세부 구현 방식(예: 멀티모듈 분리 여부, 세부 테이블/가드)을 제안 상태로 남긴 항목.
  - `제안 (Proposed)`: 아직 최종 확정되지 않은 설계안.
  - `대체됨 (Superseded)`: 후속 ADR로 대체된 결정(양방향 링크 유지).

## ADR 목록

| 번호 | 제목 | 상태 | 작성일 |
| --- | --- | --- | --- |
| [ADR-0001](./0001-storage-authority-and-hybrid-search.md) | RDB 단일 권위 + `Bleve + HNSW` / `OpenSearch` 파생 프로젝션 및 Outbox 재색인 계약 | 확정 (세부 스키마 제안) | 2026-11-28 |
| [ADR-0002](./0002-chat-bubble-room-and-decisions-protocol.md) | 채팅 버블 기반 멀티캐릭터 룸 + Decisions 조율 및 `observedBy` 격리 프로토콜 | 확정 (세부 파라미터 제안) | 2026-11-28 |
| [ADR-0003](./0003-tools-approval-gate-and-browser-control.md) | 1차 내장 도구 7개 영역, 3단계 승인 게이트 및 `browser_control` 계약 | 확정 (세부 가드 제안) | 2026-11-28 |
| [ADR-0004](./0004-module-layout-and-independent-distribution.md) | 공개 `pkg/` 계층, 공유 코어 및 `llm`/`cdp` 독립 배포 모듈 경계 | 확정 (단일 루트 `go.mod` + `desktop/go.mod`) | 2026-11-28 |

## 관련 문서

- [제품 요구사항 정의서 (PRD)](../prd.md)
- [프로젝트 청사진 (Blueprint)](../blueprint.md)
