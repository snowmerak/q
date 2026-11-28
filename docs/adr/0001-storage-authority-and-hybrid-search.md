# ADR-0001: RDB 단일 권위 + `Bleve + HNSW` / `OpenSearch` 파생 프로젝션 및 Outbox 재색인 계약

- **상태:** 확정 및 구현·검증 완료 (Accepted & Verified — M1/M4 완료)
- **작성일:** 2026-11-28
- **관련 문서:** [PRD (`REQ-STOR-01`, `REQ-MEM-01`)](../prd.md#42-%EA%B8%B0%EB%8A%A5-%EB%B0%8F-%EB%B9%84%EA%B8%B0%EB%8A%A5-%EC%9A%94%EA%B5%AC%EC%82%AC%ED%95%AD), [Blueprint 4장](../blueprint.md#4-%EC%A1%B0%ED%95%A9-%EA%B0%80%EB%8A%A5%ED%95%9C-%EC%A0%80%EC%9E%A5%EC%86%8C-%EC%95%84%ED%82%A4%ED%85%8D%EC%B2%98-data-authority--recovery-view)
- **참조 근거:** `snowmerak/q` (`main`) `sessionstore/vector.go` · `opensearchindex/index.go`, `rileyayla/evernight` `docs/context-and-memory-design.md`

---

## 1. 맥락 및 동인 (Context & Drivers)

q v2는 단일 인스턴스(Single-instance) 코어 엔진과 단일 전역 데이터 디렉터리(`--data-dir` 오버라이드 지원) 위에서 다음 조건을 동시에 만족해야 합니다.

1. **조합 가능한 저장소:** 운영자/사용자가 **RDB(`PostgreSQL` / `MySQL` / `SQLite`)**와 **검색 엔진(`OpenSearch` / `Bleve + HNSW`)** 6가지 조합을 설정만으로 자유롭게 선택·조합할 수 있어야 합니다.
2. **하이브리드 검색 및 시간 감쇄:** 기억과 지식 문서에 대해 어휘(BM25) + 시맨틱 벡터 검색을 함께 지원해야 하며, 로컬 무설치 환경(`SQLite` + `Bleve`)에서도 벡터 검색을 사용할 수 있어야 합니다 (`Bleve` 단독으로는 벡터 검색을 제공하지 않으므로 `HNSW` 결합 필요).
3. **내구성과 재색인:** 임베딩 모델·차원이 바뀌거나 Search 백엔드를 교체(`Bleve + HNSW` ↔ `OpenSearch`)하더라도 대화·기억·지식 원문이 유실되지 않고 재색인될 수 있어야 합니다.
4. **클린 스타트 & 단일 프로세스:** 기존 Evernight/Q v1 데이터는 이관하지 않으며, 다중 프로세스 간 인덱스 락 경합이나 리더 선출은 고려하지 않습니다.

---

## 2. 검토한 대안 (Considered Options)

### 대안 1: RDB 단일 권위 + Search 파생 프로젝션(Outbox) + Blob Store *(채택)*
- **RDB**가 모든 도메인 원문(캐릭터, 페르소나, 룸, 채팅 버블, 구조화 이벤트, 기억 원문 및 임베딩 메타, 가치관, 지식 문서, 체크포인트, `index_outbox`)의 유일한 권위 저장소입니다.
- **Search(`Bleve + HNSW` 또는 `OpenSearch`)**는 RDB `index_outbox`를 소비해 갱신되는 파생 검색 인덱스입니다.
- **Blob Store**는 대용량 바이너리(이미지, 첨부 파일, Loom 아티팩트 본문)를 불변 저장합니다.

### 대안 2: Evernight 방식 (OpenSearch 원문 + RDB 실행/컨텍스트 상태)
- **기각 사유:** Evernight은 확정 턴과 기억 원문을 OpenSearch `_source`에 두었으나, q v2는 로컬 조합(`Bleve + HNSW`)을 동등한 1급 백엔드로 지원해야 합니다. 로컬 파일 기반 `Bleve + HNSW`를 원문 권위 저장소로 삼으면 트랜잭션 커밋, 백업, `SQLite` 상태와의 원자적 정합성을 유지하기 어렵고 Search 백엔드 교체가 불가능해집니다.

### 대안 3: RDB 단독 검색 (SQLite FTS5 / PG tsvector + pgvector / MySQL FULLTEXT)
- **기각 사유:** 3종 RDB의 전문 검색과 벡터 인덱싱 기능·확장 설치 요구가 서로 크게 달라 일관된 하이브리드 검색 계약을 만들기 어렵고, 명시적으로 요구된 `OpenSearch` 및 `Bleve + HNSW` 조합 요구를 충족하지 못합니다.

---

## 3. 결정 (Decision)

1. **RDB 단일 권위 & Outbox 프로젝션 패턴 채택 [확정]:**
   - 모든 쓰기 트랜잭션은 RDB(`PostgreSQL` / `MySQL` / `SQLite`)에 원문·상태 변경·체크포인트와 함께 `index_outbox` 레코드를 원자적으로 기록합니다.
   - Search 계층(`OpenSearch` / `Bleve + HNSW`)은 파생 프로젝션이므로, 인덱스 파일이나 클러스터 인덱스를 삭제하더라도 RDB 원문으로부터 전체 재색인이 가능해야 합니다.
2. **로컬 하이브리드 검색 (`Bleve + HNSW`) 계약 [확정 + 제안]:**
   - Q v1(`main:sessionstore/vector.go`, `store.go`)에서 검증된 패턴을 계승해 어휘 인덱스는 `bleve/v2`, 벡터 인덱스는 임베디드 HNSW(`goformersearch`)로 구성합니다.
   - **[제안]** 데이터 디렉터리 내 세대별 디렉터리(`index/generations/<gen>/`)에 Bleve, `vectors.hnsw`, `vectors.ids.json`, `state.json`을 관리하고, 임베딩 원문 벡터(또는 재계산 가능한 원문 텍스트·모델·차원 메타)는 RDB에 유지해 세대 교체 시 원자적으로 전환합니다.
   - 코어 엔진은 단일 인스턴스로 실행되므로 `Bleve + HNSW` 파일 핸들에 대한 다중 프로세스 락·리더 선출 로직은 두지 않습니다.
3. **원격 하이브리드 검색 (`OpenSearch`) 계약 [확정 + 제안]:**
   - Q v1(`main:opensearchindex/index.go`)의 alias + backing index 패턴과 Evernight의 시간 감쇄·관찰 범위(`observedBy`) 필터링을 결합합니다.
   - 임베딩 모델·차원·리비전이 다르면 새 backing index를 생성해 재색인한 뒤 alias를 전환합니다.
4. **임베딩 장애 폴백 및 재색인 [확정 + 제안]:**
   - 임베딩 API가 일시 실패해도 RDB 턴 커밋은 실패시키지 않으며, Outbox에 재시도 상태를 남긴 채 우선 키워드(BM25) 색인·검색으로 폴백합니다.
5. **단일 인스턴스 복구·멱등 계약 [확정 + 제안]:**
   - 다중 노드 분산 Lease 대신 전역 데이터 디렉터리 단일 실행 가드(file lock) + 인프로세스 룸/세션 직렬화를 사용합니다.
   - 비정상 종료 후 재시작 복구를 위해 RDB에 단계별 체크포인트와 클라이언트 `requestId`·입력 지문을 저장하며, 동일 `requestId` + 다른 입력은 `409 Conflict`로 거절합니다.

---

## 4. 결과 및 리스크 (Consequences & Risks)

### 장점
- 6가지 저장소 조합(`{PostgreSQL, MySQL, SQLite} × {OpenSearch, Bleve + HNSW}`)이 동일한 상위 의미 계약으로 동작합니다.
- 다중 프로세스 분산 락/리더 선출 코드가 제거되어 RDB 3종 어댑터의 SQL 복잡도와 테스트 부담이 크게 줄어듭니다.
- 임베딩 모델 변경·장애 시에도 원문 손실 없이 재색인으로 복구 가능합니다.

### 비용 및 남은 리스크
- **Bleve와 HNSW의 종료·복구 정합성:** 단일 프로세스라도 강제 종료 시 Bleve 커밋과 HNSW 파일 저장(`vectors.hnsw`) 시점이 어긋날 수 있습니다. 시작 시 RDB `index_outbox` 커서와 HNSW `state.json`을 대조해 누락분을 재적용해야 합니다.
- **RDB 3종 SQL 방언 차이:** 트랜잭션 격리 수준, JSON 필드 처리, 마이그레이션 스크립트를 3종 모두 유지해야 합니다.
- **HNSW 메모리 사용량:** 임베디드 HNSW 그래프가 메모리에 적재되므로 대규모 아카이브 환경에서는 `OpenSearch` 조합을 권장해야 합니다.

### 확인 및 검증 기준
- [x] `pkg/storage` 공통 Contract Test Suite가 6가지 조합(`{SQLite, PostgreSQL, MySQL} × {Bleve+HNSW, OpenSearch}`)에서 모두 통과할 것 (`TestStorageSixWayMatrix` 및 각 어댑터 라이브 계약 테스트 통과).
- [x] Search 인덱스를 완전히 삭제한 뒤 RDB 기준 재색인으로 키워드 및 벡터 검색이 복구될 것.
- [x] 임베딩 서버 차단 상태에서 턴 커밋이 성공하고 키워드 폴백 후 복구 시 Outbox가 벡터를 채울 것 (`TestOutboxWorkerDegradesToLexicalOnEmbeddingFailureAndRecovers`).
