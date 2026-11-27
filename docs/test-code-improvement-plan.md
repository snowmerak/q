# 테스트 코드 개선안

## 배경 및 문제 요약

최근 컨텍스트 사용량 추정 보정(`080a62b`), 이미지 첨부 리사이징(`7988926`), In-Prefix 압축(`c549bb8`) 변경 과정에서 `app`, `subagent`, `agentloop` 단위 테스트의 구조적 취약점이 드러났다. 실패 원인은 크게 두 가지 성격으로 나뉜다.

1. **테스트가 내부 동작을 별도로 재구현하거나 하드코딩해서 깨진 문제**
   - `app/model_test.go` 일부 테스트가 BubbleTea 이벤트 루프에서 소비할 내부 이벤트 개수를 고정 숫자로 가정해, 비가시 내부 이벤트(`toolTokenEstimate`) 1개가 추가되자 전부 한 틱씩 밀렸다.
   - `agentloop`, `subagent`, `app` 테스트가 체크포인트 압축 요청과 종료 도구 응답 요청을 판별하는 문자열 파싱 함수를 각자 중복 작성해 두고 있어, 요청 형태가 바뀔 때마다 테스트 헬퍼가 따로 어긋난다.
   - `subagent`의 Fake LLM이 요청 크기와 무관하게 고정 `PromptTokens: 10`을 반환해, 실제 보고 usage를 신뢰하도록 바뀐 추정 로직과 모순을 일으켰다.
2. **프로덕션 코드가 이중화되어 테스트가 실제 회귀 버그를 잡아낸 문제**
   - `agentloop/context.go`와 `subagent/context.go`가 `memory.Manager` 초기화 및 도구 토큰 예약을 각자 구현해 두고 있어, `ReserveToolTokens` 전환 시 `subagent` 쪽 수정이 누락되었다.

이 문서는 프로덕션 초기화 경로의 중복을 제거하는 선행 조치와 함께, 테스트가 프로덕션 내부 구현을 복제하거나 이벤트 순서·틱 수에 결합되지 않도록 개선하는 방안을 정리한다.

---

## 현재 확인된 구조적 문제

### 1. `app/model_test.go`: BubbleTea 이벤트 루프의 고정 틱 수 하드코딩

- `app/model_test.go`는 실제 `tea.Program` 대신 [`nextAgentMessage`](../app/model_test.go#L3962-L3982)로 `tea.Cmd` / `tea.BatchMsg`에서 `agentEventMsg`를 동기적으로 꺼내 `m.Update()`에 주입하는 미니 디스패처를 사용한다.
- 같은 파일의 [`TestAutomaticSkillHintsUseTaskStartAndUserAnswerSuffixes`](../app/model_test.go#L466-L519) (`for !m.asking`)나 [`TestTaskCompleteIsRejectedWithoutTaskStart`](../app/model_test.go#L3636-L3654) (`for m.waiting`)는 상태 조건 기반으로 동작해 내부 이벤트 수 변화에 영향을 받지 않았다.
- 반면 아래 테스트들은 이벤트 개수를 고정 횟수로 하드코딩해 `7988926`에서 `agentloop/loop.go`가 초기 `toolTokenEstimate` 이벤트를 항상 1개 더 발행하자 즉시 실패하거나 우연히만 통과하는 상태가 되었다.
  - [`TestChatExecutesToolCallsAndContinuesTurn`](../app/model_test.go#L3243-L3300): 1번째 이벤트에서 `"Preparing tool"`, 2번째에서 `"Running tool"`, 3번째에서 도구 결과 식으로 1:1 고정 매핑 (`"Thinking…"` 단계에서 실패).
  - [`TestAskToUserPausesForAnswerAndResumesSameTask`](../app/model_test.go#L3413-L3432): `for range 6`으로 정확히 6틱만 펌핑해 `m.asking == true`를 기대 (`asking false, waiting true`로 실패).
  - [`TestDirectAnswerDoesNotRequireTaskLifecycle`](../app/model_test.go#L3617-L3634): `nextAgentMessage` 딱 1회만 호출 (`waiting true, requests 1`로 실패).
  - [`TestOpenAICompatibleChatCoalescesInitialRuntimeInstructions`](../app/model_test.go#L3213-L3241): `nextAgentMessage` 1회 호출 시점에 `configuredClient.requests`가 1건 들어온다는 타이밍에 우연히 의존.

### 2. 패키지별 테스트에 복제된 요청 분류 로직

Fake `ChatClient`가 `client.Chat` 경계에서 요청 용도(일반 턴 / 압축 체크포인트 / 종료 tool ack)를 구분하기 위해, 프로덕션 내부 문자열과 `request.Tools` 유무를 검사하는 판별기를 테스트마다 따로 작성해 유지하고 있다.

- **체크포인트 요청 판별 복제 (4곳)**:
  - `agentloop/inprefix_compaction_test.go`: `isInPrefixCheckpointRequest`, `isDetachedCheckpointRequest`
  - [`agentloop/preappend_test.go`](../agentloop/preappend_test.go#L98-L100): `isPreAppendCheckpoint` — 여전히 `len(request.Tools) == 0`만 가정해 In-Prefix 요청과 불일치.
  - [`app/agent_context_test.go`](../app/agent_context_test.go#L33-L47): `isCheckpointRequestForTest`, `isDetachedCheckpointRequestForTest`
  - [`subagent/context_test.go`](../subagent/context_test.go#L63-L77): `isContextCheckpointRequest`, `isDetachedContextCheckpointRequest`
- **종료 도구 ack 요청 판별 복제 (2곳)**:
  - [`app/test_planning_helpers_test.go`](../app/test_planning_helpers_test.go#L13-L19): `terminalToolResultRequest` (`"The host has accepted this terminal tool result. The task is finished."` 매칭)
  - [`subagent/terminal_test.go`](../subagent/terminal_test.go#L18-L24): `terminalToolResultRequest` (`"The task is finished. End this turn with a short acknowledgment"` 매칭)

### 3. Fake LLM의 비현실적인 고정 `Usage` 반환

- [`subagent/scout_test.go`](../subagent/scout_test.go#L24-L40)의 `fakeScoutClient.Chat`은 요청 메시지가 몇만 바이트이든 상관없이 항상 `Usage{PromptTokens: 10, CompletionTokens: 2, TotalTokens: 12}`를 반환한다.
- `080a62b` 이후 `memory.Manager.PredictedTokens()`가 프로바이더 보고 `PromptTokens`를 우선 신뢰하게 되면서, 72,000바이트 `loom_read` 결과(`24,000` 토큰)가 추가된 뒤에도 `10 + 24,000 = 24,010 < 27,200`(32,000의 85%)으로 계산되어 [`TestGrillerRetainsActualUserAnswerAfterToolHistoryCompaction`](../subagent/context_test.go#L240-L267)이 실패한다.

### 4. (프로덕션 코드 중복) `memory.Manager` 도구 토큰 초기화 이중화

- [`agentloop/context.go`](../agentloop/context.go#L36-L40)는 `manager.ReserveToolTokens(toolTokens)`를 호출해 `promptUsage.InputTokens`를 오염시키지 않고 도구 스키마 오버헤드만 예약한다.
- 반면 [`subagent/context.go`](../subagent/context.go#L53-L59)의 `NewContextCompactor`는 여전히 `manager.ObserveUsage(local+toolTokens, local)`를 호출하고 있어 생성 직후부터 `promptUsage.InputTokens > 0`이 되며 10% 안전 마진이 꺼진다.
- 이로 인해 [`TestInternalRunnersCompactBetweenToolRoundsAndKeepContracts`](../subagent/context_test.go#L269-L348)에서 `griller`, `scout`, `coder`, `review` 서브테스트가 85% 임계치에 도달하지 못해 실패한다. 이는 테스트가 잘못된 것이 아니라 `subagent` 쪽 초기화 누락 버그를 정상적으로 탐지한 사례다.

---

## 개선 원칙

1. **이벤트 횟수가 아니라 관측 가능한 상태(predicate)로 테스트를 구동한다.**
   - `toolTokenEstimate`, `promptUsage`처럼 UI 표시에 직접 매핑되지 않는 내부 이벤트가 추가·병합되더라도 테스트가 깨지지 않아야 한다.
2. **요청 분류와 초기화 불변식은 프로덕션 단일 출처(Single Source of Truth)를 재사용한다.**
   - 테스트 파일마다 프롬프트 문자열이나 `Tools` 길이를 추측해 복제하지 않고, 프로덕션 패키지가 제공하는 상수·분류 함수 또는 공통 생성자를 사용한다.
3. **Fake `ChatClient`의 `Usage` 기본값은 미보고(`Usage{}`)로 둔다.**
   - 임의의 작은 상수(`PromptTokens: 10`)를 전역 기본값으로 넣지 않고, usage 보고 경로를 검증하는 테스트만 명시적 수치 또는 요청 크기 비례 추정치를 사용한다.
4. **프로덕션 중복(`agentloop` vs `subagent`)은 테스트 우회가 아니라 프로덕션 코드로 먼저 해결한다.**

---

## 세부 개선안

### 개선안 1: `app` TUI 테스트용 상태 기반 펌핑 헬퍼 도입

고정 횟수(`nextAgentMessage` 1회, `for range 6`) 루프를 제거하고, 모델 상태를 검사하는 predicate 기반 헬퍼를 `app/model_test.go`(또는 `app` 테스트 공용 헬퍼 파일)에 둔다.

- **권장 헬퍼 시그니처**:
  - `pumpAgentUntil(t *testing.T, m model, cmd tea.Cmd, maxSteps int, desc string, pred func(model) bool) (model, tea.Cmd)`
  - `pumpAgentToIdle(t *testing.T, m model, cmd tea.Cmd) model`: `!m.waiting`까지 진행.
  - `pumpAgentToStatus(t *testing.T, m model, cmd tea.Cmd, substr string) (model, tea.Cmd)`: `strings.Contains(m.status, substr)`까지 진행.
- **실패 진단 규약**:
  - `maxSteps` 안에 `pred(m)`가 참이 되지 않거나 `cmd == nil`이 되면, `"step count mismatch"` 대신 `desc`, 마지막 `m.status`, `m.waiting`, `m.asking`, 소비한 `agentEventMsg` 요약을 포함해 `t.Fatalf`로 종료한다.
- **기존 고정 틱 테스트 전환 방식**:
  - `TestDirectAnswerDoesNotRequireTaskLifecycle`, `TestOpenAICompatibleChatCoalescesInitialRuntimeInstructions`: `pumpAgentToIdle(t, m, command)`로 턴 완료까지 진행한 뒤 요청 수·메시지·instructions 병합을 검증한다.
  - `TestAskToUserPausesForAnswerAndResumesSameTask`: `pumpAgentUntil(..., "question prompt", func(m model) bool { return m.asking })`로 전환한다.
  - `TestChatExecutesToolCallsAndContinuesTurn`: 중간 단계(`"Preparing tool"` -> `"Running tool" · "start task"` -> `taskStartToolName` tool result -> persisted `ActiveTask` -> `"Running tool" · "write main.go"` -> tool result)를 **고정 1틱**이 아니라 `pumpAgentToStatus` / 상태 전이 순서 관찰로 진행시켜, 중간에 비가시 이벤트가 끼어들어도 전이 순서와 최종 상태 계약만 깨지지 않으면 통과하게 한다.

### 개선안 2: 요청 분류(Compaction / Terminal Ack) 단일화

- **단기 권장안 (최소 침습)**:
  - `memory` 패키지에 압축 요청 판별용 열거형과 분류 함수를 둔다(예: `CheckpointRequestKind` = `None` / `InPrefix` / `Detached`, `ClassifyCheckpointRequest(request client.ChatRequest) CheckpointRequestKind` 또는 `IsCheckpointRequest` / `IsDetachedCheckpointRequest`).
  - 판별 기준 문자열 상수(`session continuation checkpoint` 등)와 In-Prefix/Detached 판단 규칙을 `memory` 한 곳에서 관리하고, `agentloop/inprefix_compaction_test.go`, `agentloop/preappend_test.go`, `subagent/context_test.go`, `app/agent_context_test.go`의 중복 함수 4종을 모두 제거해 해당 함수로 통합한다.
  - `terminalToolResultRequest` 역시 `app`과 `subagent` 각 패키지의 종료 안내 문자열 상수를 직접 참조하는 단일 헬퍼로 통일해 임의 부분 문자열 복제를 없앤다.
- **중기 검토안 (요청 목적 메타데이터)**:
  - `client.ChatRequest`에 JSON 직렬화에서 제외되는(`json:"-"`) 내부 메타데이터 또는 `context` 기반 `RequestPurpose`(`normal`, `compaction_in_prefix`, `compaction_detached`, `terminal_ack`)를 두면 테스트 Fake가 프롬프트 문자열을 파싱하지 않아도 된다.
  - 단, 테스트 편의만을 위해 공개 요청 스키마나 어댑터 계약을 오염시키면 실익보다 유지비가 커지므로, Gateway 사용량 귀속·로깅·트레이싱에서 동일한 요청 목적 구분이 필요해질 때 함께 도입하는 편이 적절하다.

### 개선안 3: Fake `ChatClient`의 `Usage` 계약 표준화

- [`subagent/scout_test.go`](../subagent/scout_test.go#L38)의 `fakeScoutClient.Chat` 등 범용 Fake 클라이언트는 기본적으로 `Usage: client.Usage{}`(미보고)를 반환하도록 바꾼다.
- 프로바이더가 실제 `Usage`를 보고했을 때의 보정·압축·대시보드 집계를 검증하는 테스트는 다음 둘 중 하나를 명시적으로 사용한다.
  1. 테스트 케이스별로 `fakeScoutClient`에 명시적 `usage` 필드를 설정한다.
  2. 요청 메시지·도구 스키마 크기를 반영해 모순 없는 토큰 수를 계산하는 테스트 헬퍼(예: `memory.CountMessages(request.Messages) + memory.CountTools(request.Tools)`)를 사용한다.

### 개선안 4: `memory.Manager` 도구 토큰 초기화 경로 통합 (프로덕션 선행 수정)

- [`subagent/context.go`](../subagent/context.go#L56-L59)의 `ObserveUsage(local+toolTokens, local)` 호출을 `manager.ReserveToolTokens(toolTokens)`로 교체한다.
- 더 나아가 [`memory.NewWithTools(policy, initial, tools)`](../memory/manager.go) 같은 공통 생성자를 두어 `agentloop`와 `subagent`가 도구 토큰 예약 규칙을 중복 구현하지 않게 통합한다.
- 이 수정 이후 `TestInternalRunnersCompactBetweenToolRoundsAndKeepContracts`와 `agentloop` 압축 테스트가 동일한 토큰 예산 규칙으로 동작하는지 함께 검증한다.

---

## 단계별 적용 계획

| 단계 | 범위 | 대상 파일 | 기대 효과 |
|---|---|---|---|
| **Phase 1 (즉시)** | 기존 실패 테스트 및 프로덕션 누락 복구 | `subagent/context.go`, `subagent/scout_test.go`, `app/model_test.go` | `subagent` 2건·`app` 3건 기존 실패 해소 및 이벤트 개수 결합 제거 |
| **Phase 2 (단기)** | 테스트 요청 판별 헬퍼 단일화 | `memory/*`, `agentloop/*_test.go`, `subagent/*_test.go`, `app/*_test.go` | `isCheckpoint*` 4곳·`terminalToolResultRequest` 2곳 중복 제거, `preappend_test.go` 불일치 해소 |
| **Phase 3 (중기)** | 초기화 생성자 통합 및 테스트 가이드 반영 | `memory/manager.go`, `agentloop/context.go`, `subagent/context.go`, [testing.md](testing.md) | `agentloop`/`subagent` 컨텍스트 초기화 단일화 및 신규 테스트 리뷰 기준 정착 |

---

## 검증 기준

1. **전체 단위 테스트 통과**: `go test ./memory ./agentloop ./subagent ./app` 전수 통과.
2. **내부 이벤트 추가 내성**: `agentloop/loop.go`가 모델 호출 전 비가시 상태/추정 `Event`를 1개 더 발행하더라도 `app` TUI 상태 기반 테스트가 모두 통과하는지 확인.
3. **단일 수정 지점 검증**: 체크포인트 프롬프트 문구 또는 `InPrefix`/`Detached` 판별 규칙을 바꿀 때 `memory` 패키지 한 곳만 수정하면 `agentloop`, `subagent`, `app` 테스트가 함께 따라오는지 확인.
