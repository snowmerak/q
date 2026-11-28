# ADR-0002: 채팅 버블 기반 멀티캐릭터 룸 + Decisions 조율 및 `observedBy` 격리 프로토콜

- **상태:** 확정 및 구현·검증 완료 (Accepted & Verified — M2/M4 완료)
- **작성일:** 2026-11-28
- **관련 문서:** [PRD (`REQ-CHAT-01`, `REQ-ROOM-01`, `REQ-MEM-01`)](../prd.md#42-%EA%B8%B0%EB%8A%A5-%EB%B0%8F-%EB%B9%84%EA%B8%B0%EB%8A%A5-%EC%9A%94%EA%B5%AC%EC%82%AC%ED%95%AD), [Blueprint 3장·5장](../blueprint.md#5-%EC%A3%BC%EC%9A%94-%EC%8B%A4%ED%96%89-%ED%9D%90%EB%A6%84-major-flows)
- **참조 근거:** `rileyayla/evernight` `docs/story-progression-architecture.md` · `docs/simple-chat.md` · `docs/character-values.md`

---

## 1. 맥락 및 동인 (Context & Drivers)

q v2는 사용자와의 캐릭터챗 상호작용을 최우선으로 두며, **1:1 캐릭터 봇**과 **다수 캐릭터가 함께 참여하는 멀티캐릭터 룸**을 모두 1차 범위에 포함합니다.

Evernight은 `PacedLoop`(다중 캐릭터 + 월드 3인칭 소설 집필)와 `SimpleChatLoop`(1:1 직접 대화)를 통해 캐릭터 주체성, `observedBy` 기반 인지 격리, 기억·가치관 갱신, 실시간 끼어들기를 검증했습니다. 그러나 q v2는 소설 연재 서비스가 아니라 **대화·작업 중심 봇**이므로 다음 변화가 필요합니다.

1. 3인칭 과거형 소설 본문(`prose`)으로 재집필하지 않고 **캐릭터별 채팅 버블**로 직접 대화해야 합니다.
2. 이산 판단(참여 여부, 사용자 턴 전환, 기억 회상 게이트/선별, 상태·관계 갱신, 도구 승인 자동 판정)은 `github.com/snowmerak/decisions`를 일관되게 사용합니다.
3. 캐릭터가 대화 중 도구(파일·셸·브라우저·MCP 등)를 호출할 수 있어야 하며, 도중에 사용자가 끼어들거나 멈출 수 있어야 합니다.

---

## 2. 검토한 대안 (Considered Options)

### 대안 1: 채팅 버블 중심 통합 턴 루프 + Decisions 조율 (`SimpleChatLoop` + 산문 제거형 `PacedLoop`) *(채택)*
- 1:1과 멀티캐릭터 룸 모두 캐릭터가 직접 채팅 버블로 응답하되, 룸에서는 호스트 스케줄러와 `decisions`가 참여·발화 순서·턴 전환을 조율합니다.
- 3인칭 `prose` 집필, 시점 검수(`pov_review`), 이름 공개 서술 검수(`name_review`), 문체 프리셋은 제거합니다.

### 대안 2: Evernight 원형 유지 (`raw` 버퍼 → 월드 3인칭 `prose` 재집필)
- **기각 사유:** 봇과의 채팅 버블 대화라는 v2 정체성과 맞지 않으며, 매 턴 월드 집필·시점 검수·이름 검수용 LLM/Decisions 추가 호출이 발생해 봇 응답 지연과 토큰 비용이 과도하게 증가합니다.

### 대안 3: 고정 라운드 로빈 또는 일반 LLM 프롬프트 라우팅
- **기각 사유:** 고정 순서는 불필요한 빈 맞장구를 유발해 캐릭터 주체성을 떨어뜨리고, 일반 LLM 텍스트 라우팅 대신 `decisions` API를 사용하기로 한 결정과도 맞지 않습니다.

---

## 3. 결정 (Decision)

1. **채팅 버블 중심 응답 포맷 [확정]:**
   - 1:1 대화와 멀티캐릭터 룸 모두 별도 소설 재집필 없이 캐릭터 모델이 생성한 메시지(대사·행동 표현 포함 가능한 단일 챗 버블 단위)를 그대로 스트리밍 및 영속화합니다.
   - **[제안]** 룸 내 도구 호출 및 실행 결과 카드는 해당 도구를 호출한 캐릭터의 버블 맥락에 연결해 UI에 표시합니다.
2. **멀티캐릭터 룸 조율 (`reserveActor` + `Decisions`) [확정 + 제안]:**
   - 호스트가 캐릭터별 속도(`baseSpeed`, `activation`) 기반 게이지를 계산해 다음 기회 대상 캐릭터를 예약합니다(월드 서술자 주체는 제외).
   - `decisions`의 `participation`(`act` / `wait`) 판단을 거쳐 `act`인 캐릭터만 발화/도구 루프를 실행합니다. 모든 참여 캐릭터가 연속 `wait`하거나, 발화 후 `decisions`의 `user_turn` 판단이 `true`가 되면 사용자에게 턴을 넘깁니다.
   - **[제안]** 무한 루프·비용 폭주를 막기 위해 사용자 입력 1회당 최대 연속 캐릭터 발화 횟수 상한(룸 설정 또는 기본 상한)을 둡니다.
3. **인지 격리(`observedBy`) 및 프롬프트 캐시 친화성 [확정 + 제안]:**
   - 각 메시지/사건/기억에는 `observedBy` 참여자 집합과 선택적 위치/공간 스코프를 기록합니다. 룸 내 다른 공간·비공개 대화를 관찰하지 못한 캐릭터의 컨텍스트와 하이브리드 기억 검색에는 해당 항목을 절대 주입하지 않습니다.
   - 캐릭터별 `ActorContext`는 Append-only 프리픽스(시스템 지침 + 초기/현재 가치관 + 압축 메모 + 누적 대화)를 유지하고, 회상된 기억·지식과 현재 작업 입력을 뒤에 붙여 프롬프트 캐시 적중률을 높입니다. 누적 입력이 유효 예산의 기본 85%를 넘으면 해당 캐릭터의 오래된 확정 기록을 요약 압축합니다.
4. **실시간 끼어들기 및 중단 (`InterceptHub`) [확정]:**
   - 턴 실행 중 추측성 단계(스케줄링, Decisions 참여 판단, 생성 스트리밍, 승인 대기 전 단계)는 `InterceptHub`의 취소 컨텍스트로 감쌉니다.
   - 사용자가 멈추기(Stop) 또는 끼어들기(Cut-in)를 요청하면 미확정 추측 단계를 즉시 중단·롤백하고 사용자 입력을 반영해 재개하거나 턴을 종료합니다. 이미 확정 커밋된 버블·도구 결과는 롤백하지 않습니다.
5. **확정 후 비동기 후처리 (기억 · 가치관 · 상태/관계) [확정 + 제안]:**
   - 턴이 RDB에 확정 커밋된 뒤 백그라운드에서 (a) 개인 기억 추출 및 `index_outbox` 발행, (b) `decisions` 기반 자기 상태(`valence`/`activation`/`stressed`) 및 사용자·타 캐릭터 관계 갱신, (c) 압축 경계에서 추출된 가치관 후보 병합/정리를 수행합니다.
   - 후처리 실패가 이미 확정된 채팅 버블이나 턴 원문을 훼손하지 않습니다.

---

## 4. 결과 및 리스크 (Consequences & Risks)

### 장점
- Evernight의 강점(캐릭터 주체성, `observedBy` 격리, 기억·가치관 진화, 실시간 Cut-in/Stop)을 유지하면서도 3인칭 집필·검수 단계를 제거해 응답 지연을 크게 줄입니다.
- 1:1 대화는 룸 프로토콜에서 참여 캐릭터 수를 1명으로 고정하고 `participation`/`user_turn` 조율을 생략한 특수 케이스로 통합 관리할 수 있습니다.

### 비용 및 남은 리스크
- **도구 실행 중 캐릭터 간 맥락 혼선:** 캐릭터 A가 도구 실행 결과를 받아 장황한 데이터를 처리하는 동안 다른 캐릭터가 어떻게 인식할지 경계가 필요합니다. 기본 원칙으로 도구 호출의 최종 요약과 공개 결과 버블만 `observedBy`에 공유하고, 중간 원시 tool-call 추적은 호출한 캐릭터 컨텍스트(또는 Loom 참조)에 제한하는 것을 권장합니다.
- **멀티캐릭터 호출 비용:** 참여 캐릭터가 많을수록 `participation`, `memory_recall_gate`, `state_update` 등 `decisions` 호출이 늘어납니다. 1턴 내 최대 발화 횟수 상한과 경량 판단 모델 분리가 필요합니다.

### 확인 및 검증 기준
- [x] 멀티캐릭터 룸에서 비공개/다른 위치 메시지가 `observedBy`에 없는 캐릭터의 LLM 요청 프롬프트에 포함되지 않음을 검증할 것 (`TestGroupTurnParticipationWaitHandoffAndObservedByIsolation`).
- [x] 실행 도중 Stop/Cut-in 요청 시 미완료 AI 생성만 취소되고 확정 기록이 보존됨을 검증할 것 (`TestInterceptStopAndCutIn`).
- [x] 전원 `wait` 또는 `user_turn == true` 조건에서 정상적으로 사용자 턴으로 종료됨을 검증할 것 (`TestGroupTurnParticipationWaitHandoffAndObservedByIsolation`, `TestGroupTurnMaxTurnsPerInputCap`).
