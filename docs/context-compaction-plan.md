# Context compaction plan

## 목적

대화 요청에 사용될 입력 컨텍스트가 선택한 모델의 context window 중 85%에
도달하면, 오래된 대화를 누적 요약으로 교체해 다음 요청의 입력 컨텍스트를
전체 window의 22% 이하로 줄인다.

이 기능은 다음 원칙을 지킨다.

- 사용자가 보는 전체 transcript는 삭제하지 않는다.
- LLM에 전송하는 context만 압축한다.
- system prompt, tool schema와 아직 끝나지 않은 tool call은 보존한다.
- 오래된 대화는 구조화된 누적 요약으로 만들고 최근 대화는 원문으로 남긴다.
- 압축 후 stateful provider의 `conversation_id`는 초기화한다.
- 모델의 context window를 알 수 없으면 임의의 크기를 추측하지 않는다.

## 구현 상태

현재 다음 핵심 경로가 구현되어 있다.

- 기본 정책 85% trigger, 22% target, 7% recent 원문
- Gateway 모델의 `context_length` 우선 적용, 시작 및 설정 변경 시 cache 갱신
- 전체 transcript와 API request context 분리
- 보수적 token 추정과 실제 `prompt_tokens` 기반 provider overhead 보정
- 오래된 context의 구조화된 rolling summary와 최근 원문 보존
- assistant tool call과 연속된 tool result를 같은 보존 단위로 처리
- 압축 성공 후 `conversation_id` 초기화 및 보류한 사용자 요청 자동 전송
- 압축 실패 시 context를 유지하고 사용자 입력 복구
- TUI header의 예상 context 사용량 표시
- TUI 모델 catalog의 context length 표시 및 Gateway override 편집

수동 `/compact`, `/context`, context-length 오류의 단일 자동 재시도와 모델별
정확한 tokenizer는 후속 단계로 남아 있다.

## 2026-08-02 로컬 API 조사 결과

조사 대상은 `http://localhost:18181/v1`이다. `/models`는 404이고
`/v1/models`가 올바른 endpoint였다. 모델별 검사는 다음과 같은 최소 요청으로
진행했다.

```json
{
  "model": "<model-id>",
  "messages": [{"role": "user", "content": "Reply only: OK"}],
  "max_completion_tokens": 8,
  "stream": false
}
```

47개 모델을 동시 요청 4개로 제한해 검사한 결과는 다음과 같다.

| 모델 그룹 | 모델 수 | Chat 성공 및 usage 반환 | 실패 |
|---|---:|---:|---:|
| `codex/*` | 3 | 3 | 0 |
| `grok/*` | 10 | 5 | 5 |
| `hermes/*` | 10 | 5 | 5 |
| `lmstudio/*` | 6 | 6 | 0 |
| `local/*` | 18 | 13 | 5 |
| 합계 | 47 | 32 | 15 |

`context_length`는 47개 중 37개에서 반환됐으며 10개에는 없었다. 누락된
모델은 Grok/Hermes video 모델 4개와 LM Studio 모델 6개다.

성공한 32개 응답은 모두 `prompt_tokens`, `completion_tokens`, `total_tokens`를
반환했다. 대표 결과는 다음과 같다.

| 모델 | context length | prompt | completion | total | 비고 |
|---|---:|---:|---:|---:|---|
| `codex/gpt-5.6-luna` | 128,000 | 14,630 | 5 | 14,635 | provider 주입 context가 큼 |
| `codex/gpt-5.6-terra` | 258,400 | 16,053 | 5 | 16,058 | provider 주입 context가 큼 |
| `codex/gpt-5.6-sol` | 258,400 | 16,053 | 5 | 16,058 | provider 주입 context가 큼 |
| `grok/grok-4.20-0309-non-reasoning` | 1,000,000 | 188 | 1 | 189 | 일반 usage 관계 |
| `grok/grok-4.20-0309-reasoning` | 1,000,000 | 190 | 1 | 300 | hidden reasoning이 total에 포함된 것으로 보임 |
| `grok/grok-4.3` | 1,000,000 | 196 | 1 | 310 | total이 prompt + completion과 다름 |
| `lmstudio/qwen3-vl-reap-24b-a3b` | 미제공 | 12 | 2 | 14 | 설정 fallback 필요 |
| `local/gemma-4-12B-it-qat-4bit` | 262,144 | 17 | 1 | 18 | 정상 usage |

실패 15개의 분류는 다음과 같다.

- multi-agent 모델 2개: Chat Completions를 지원하지 않음
- image/video 모델 8개: Chat Completions에서 모델을 찾을 수 없음
- TTS/ASR 모델 2개: 전용 audio endpoint가 필요함
- 로컬 모델 2개: 메모리 ceiling 초과
- 번역 모델 1개: 요청 content 형식이 전용 chat template과 맞지 않음

일부 `lmstudio/text-embedding-*` 모델은 이름과 달리 이번 Chat 요청을 받아
usage를 반환했다. 따라서 이름만으로 chat capability를 판정하지 않고 실제
요청 가능 여부 또는 향후 provider capability metadata를 사용해야 한다.

### 조사로 확정된 설계 판단

1. 압축 임계치는 `total_tokens`가 아니라 `prompt_tokens`를 기준으로 한다.
   reasoning token과 생성 token은 다음 요청의 입력 context와 같은 값이 아니다.
2. 로컬 tokenizer 추정만 사용하면 안 된다. Codex처럼 provider가 약 1.5만
   token을 추가할 수 있으므로 마지막 실제 `prompt_tokens`로 추정치를 보정한다.
3. `/models`의 `context_length`를 우선 사용하되 설정의 명시적 fallback이
   반드시 필요하다.
4. 모델 변경 시 context window와 token 보정값을 함께 초기화해야 한다.

## 데이터 구조

현재 `app.model.messages`가 transcript와 API context 역할을 동시에 수행한다.
이를 다음 두 계층으로 분리한다.

```go
type model struct {
    transcript []client.Message // UI에 표시하는 전체 원문 기록
    memory     *memory.Manager   // API 요청에 넣을 압축 가능한 context
}
```

새 `memory` 패키지는 Bubble Tea에 의존하지 않는다.

```go
type Manager struct {
    ContextWindow   int
    TriggerRatio    float64
    TargetRatio     float64
    ContextMessages []client.Message

    LastPromptTokens int
    LastLocalEstimate int
    ProviderOverhead  int
}

type Plan struct {
    NeedsCompaction bool
    ProjectedTokens int
    TriggerTokens   int
    TargetTokens    int
    SummarySource   []client.Message
    RecentTail      []client.Message
}
```

`Manager`의 주요 API 초안은 다음과 같다.

```go
func (m *Manager) Plan(next client.Message, immutable []client.Message) Plan
func (m *Manager) ObserveUsage(actualPromptTokens, localEstimate int)
func (m *Manager) Apply(summary client.Message, recent []client.Message)
func (m *Manager) Messages(immutable []client.Message, next client.Message) []client.Message
```

## context window 결정

우선순위는 다음과 같다.

1. Gateway `/models`에서 선택한 모델의 `context_length`
   (`model_metadata` override 포함)
2. 사용자가 파일에 설정한 fallback `context.window`
3. 알 수 없음

Gateway metadata를 실제 routing 제한의 권위 있는 값으로 취급한다. 모델
catalog의 `Ctrl+E` 편집은 Gateway `model_metadata`를 변경하고 Gateway 교체와
`/models` 재조회를 거쳐 현재 모델의 cache를 즉시 갱신한다. `context.window`는
TUI에 노출하지 않으며 metadata가 없는 provider를 위한 file-only fallback이다.
두 값이 모두 없거나 0이면 자동 압축을 비활성화하고 UI에
`context unknown`을 표시한다.

설정 예시는 다음과 같다.

```yaml
context:
  window: 258400
  trigger_ratio: 0.85
  target_ratio: 0.22
  recent_ratio: 0.07
```

기본값은 trigger 0.85, target 0.22, recent 0.07이다. `window`는 선택 모델의
metadata가 존재하면 생략할 수 있다.

## token 사용량 계산

응답을 받은 뒤에는 실제 값을 기록한다.

```go
providerOverhead = max(0, usage.PromptTokens-lastLocalEstimate)
```

다음 요청 전에는 다음 식으로 입력 token을 예측한다.

```go
projected = tokenCounter.Count(nextPayload) + providerOverhead
trigger   = int(float64(contextWindow) * 0.85)
target    = int(float64(contextWindow) * 0.22)
```

provider overhead는 급격히 줄어드는 방향으로 즉시 보정하지 않고 최근 관측값의
최댓값 또는 EWMA에 안전 여유 10%를 적용한다. 모델 또는 provider를 변경하면
overhead를 0으로 초기화한다.

OpenAI-compatible 모델의 tokenizer를 항상 알 수는 없으므로 `TokenCounter`는
교체 가능한 인터페이스로 둔다.

```go
type TokenCounter interface {
    CountMessages([]client.Message) int
}
```

초기 구현은 보수적인 UTF-8 기반 추정기를 사용하고, 추후 알려진 OpenAI 모델은
정확한 tokenizer 구현으로 교체한다. 실제 usage가 한 번이라도 반환되면 그 값을
우선해 보정한다.

## 압축 알고리즘

압축 결과의 목표 형태는 다음과 같다.

```text
immutable system/tool context
+ structured cumulative summary
+ recent raw turns
<= context window의 22%
```

1. system/developer prompt와 tool schema를 immutable 영역으로 분리한다.
2. 최근 원문은 최대 context window의 7%만 남긴다.
3. 나머지 오래된 메시지와 기존 누적 요약을 summary source로 선택한다.
4. assistant tool call과 대응하는 tool result는 하나의 atomic turn으로 취급한다.
5. summary source가 너무 크면 70% 이하의 chunk로 나눠 rolling summary를 만든다.
6. summary output budget은 `22% - immutable - recent`로 제한한다.
7. 결과를 다시 계산해 22%를 넘으면 더 작은 예산으로 한 번 재압축한다.
8. 그래도 넘으면 recent tail부터 오래된 turn 단위로 줄이되 tool 묶음은 깨지 않는다.

immutable 영역만 이미 22%를 넘으면 목표 달성은 불가능하다. 이 경우 목표를
`immutable + 최소 summary/recent budget`으로 올리고 UI에 경고한다.

### 요약 형식

자유로운 산문 대신 다음 섹션을 가진 구조화된 텍스트를 사용한다.

```text
## User goals
## Confirmed decisions
## Current state
## Important files, identifiers, and exact values
## Constraints
## Unresolved work
## Recent outcomes and errors
```

요약 요청에는 사실을 만들지 말 것, 완료와 미완료를 구분할 것, 경로·명령·오류·
식별자를 정확히 보존할 것, 지정 token budget을 넘지 말 것을 명시한다.

## 전송 상태 흐름

```text
Idle
  └─ Enter
      ├─ projected < 85% → Sending
      └─ projected >= 85%
           └─ Compacting
                ├─ 성공 → conversation_id 초기화 → Sending
                └─ 실패 → 원문 보존 → 사용자 입력 복구 → Idle
```

사용자 입력은 `pendingMessage`로 보관하고 transcript에는 즉시 표시한다. 압축이
성공하면 원래 입력을 자동 전송한다. 같은 context-length 오류에 대해서만 강제
압축 후 한 번 재시도하며 무한 재시도는 금지한다.

stateful provider의 `conversation_id`를 유지하면 서버가 압축 전 context를 계속
보유할 수 있으므로 압축 적용 직후 반드시 비운다.

## TUI 변경

하단 상태에 다음 정보를 추가한다.

```text
context 72% · 186k/258k
compacting context…
context compacted · 221k → 55k
context size unknown
```

수동 명령도 제공한다.

- `/compact`: 임계치와 관계없이 현재 context 압축
- `/context`: window, 실제/예상 token, provider overhead와 압축 횟수 표시

압축 중에는 중복 전송을 막고 `Ctrl+C`만 허용한다.

## 오류 정책

- 요약 API 실패: context와 transcript를 변경하지 않고 입력을 복구한다.
- usage 미제공: 추정치를 계속 사용하되 UI에 `estimated`를 표시한다.
- context window 미제공: 설정 fallback이 없으면 자동 압축하지 않는다.
- 빈 요약 또는 잘못된 응답: 적용하지 않는다.
- context-length 오류: 강제 압축 후 요청을 한 번만 재시도한다.
- 모델 변경: context window 재조회, token 보정 초기화, conversation ID 초기화.
- provider 변경: 기존 정책대로 대화를 초기화하고 새 memory manager를 생성한다.

## 구현 순서

1. config에 context 정책과 validation을 추가한다.
2. 모델 선택 시 `context_length`를 runtime/config에 보존한다.
3. 독립적인 `memory` 패키지와 보수적 `TokenCounter`를 구현한다.
4. transcript와 request context를 분리한다.
5. 응답 usage 관측 및 provider overhead 보정을 추가한다.
6. compaction request/result 상태와 자동 재전송을 Bubble Tea에 연결한다.
7. `/compact`, `/context`와 footer 상태를 추가한다.
8. context-length 오류의 단일 재시도를 추가한다.

## 테스트 계획과 완료 조건

단위 테스트:

- 77.9%에서는 압축하지 않고 85% 이상에서 압축한다.
- 압축 후 immutable + summary + recent가 22% 이하가 된다.
- system prompt와 tool call/result 묶음이 보존된다.
- 기존 summary가 다음 summary에 병합된다.
- 실제 prompt usage가 estimator overhead를 보정한다.
- reasoning 모델의 `total_tokens`가 임계치 계산에 사용되지 않는다.
- context window가 없을 때 자동 압축이 비활성화된다.
- 압축 실패 시 원문과 pending input이 보존된다.
- 압축 성공 시 conversation ID가 초기화된다.
- 모델 변경 시 context metadata와 보정값이 갱신된다.

통합 테스트:

- 가짜 client로 `chat → 85% 감지 → compact → 원 요청 재전송` 순서를 검증한다.
- 로컬 API에서 usage가 반환되는 chat 모델 하나와 반환되지 않는 fake provider를
  각각 검증한다.
- `go test ./...`, `go vet ./...`, `go build ./...`를 통과한다.

완료 기준은 자동 압축 이후 전송된 실제 prompt usage가 목표 22% 이하이거나,
provider overhead/immutable context 때문에 달성할 수 없다는 경고가 명확히
표시되는 것이다.

## 부록: 모델별 최소 요청 결과

`P/C/T`는 각각 prompt, completion, total token이다. `length`는 의도적으로
출력을 8 token으로 제한해 발생한 정상적인 finish reason이다.

| 모델 | context | P/C/T | 결과 |
|---|---:|---:|---|
| `codex/gpt-5.6-luna` | 128,000 | 14,630/5/14,635 | 성공 |
| `codex/gpt-5.6-sol` | 258,400 | 16,053/5/16,058 | 성공 |
| `codex/gpt-5.6-terra` | 258,400 | 16,053/5/16,058 | 성공 |
| `grok/grok-4.20-0309-non-reasoning` | 1,000,000 | 188/1/189 | 성공 |
| `grok/grok-4.20-0309-reasoning` | 1,000,000 | 190/1/300 | 성공 |
| `grok/grok-4.20-multi-agent-0309` | 1,000,000 | - | Chat Completions 미지원 |
| `grok/grok-4.3` | 1,000,000 | 196/1/310 | 성공 |
| `grok/grok-4.5` | 200,000 | 210/1/229 | 성공 |
| `grok/grok-build-0.1` | 256,000 | 190/1/368 | 성공 |
| `grok/grok-imagine-image` | 8,000 | - | Chat endpoint에서 모델을 찾지 못함 |
| `grok/grok-imagine-image-quality` | 8,000 | - | Chat endpoint에서 모델을 찾지 못함 |
| `grok/grok-imagine-video` | - | - | Chat endpoint에서 모델을 찾지 못함 |
| `grok/grok-imagine-video-1.5` | - | - | Chat endpoint에서 모델을 찾지 못함 |
| `hermes/grok-4.20-0309-non-reasoning` | 1,000,000 | 188/1/189 | 성공 |
| `hermes/grok-4.20-0309-reasoning` | 1,000,000 | 190/1/355 | 성공 |
| `hermes/grok-4.20-multi-agent-0309` | 1,000,000 | - | Chat Completions 미지원 |
| `hermes/grok-4.3` | 1,000,000 | 196/1/372 | 성공 |
| `hermes/grok-4.5` | 200,000 | 210/1/229 | 성공 |
| `hermes/grok-build-0.1` | 256,000 | 190/1/340 | 성공 |
| `hermes/grok-imagine-image` | 8,000 | - | Chat endpoint에서 모델을 찾지 못함 |
| `hermes/grok-imagine-image-quality` | 8,000 | - | Chat endpoint에서 모델을 찾지 못함 |
| `hermes/grok-imagine-video` | - | - | Chat endpoint에서 모델을 찾지 못함 |
| `hermes/grok-imagine-video-1.5` | - | - | Chat endpoint에서 모델을 찾지 못함 |
| `lmstudio/agents-a1-4b` | - | 255/8/263 | 성공 (`length`) |
| `lmstudio/qwen3-vl-reap-24b-a3b` | - | 12/2/14 | 성공 |
| `lmstudio/qwen3.5-4b-mtp` | - | 14/8/22 | 성공 (`length`) |
| `lmstudio/text-embedding-embeddinggemma-300m` | - | 14/8/22 | 성공 (`length`) |
| `lmstudio/text-embedding-nomic-embed-text-v1.5` | - | 14/8/22 | 성공 (`length`) |
| `lmstudio/text-embedding-qwen3-embedding-0.6b` | - | 14/8/22 | 성공 (`length`) |
| `local/Agents-A1-4bit` | 262,144 | 14/8/22 | 성공 (`length`) |
| `local/gemma-4-12B-it-qat-4bit` | 262,144 | 17/1/18 | 성공 |
| `local/gemma-4-26B-A4B-it-QAT-MLX-4bit` | 262,144 | 14/8/22 | 성공 (`length`) |
| `local/gemma-4-26B-A4B-it-uncensored-abliterix-MLX-4bit-mixed_4_6` | 262,144 | 56/8/64 | 성공 (`length`) |
| `local/gemma-4-31B-it-qat-4bit` | 262,144 | 14/8/22 | 성공 (`length`) |
| `local/gemma-4-31B-it-uncensored-abliterix-MLX-4bit-mixed_4_6` | 262,144 | 56/8/64 | 성공 (`length`) |
| `local/Hy-MT2-1.8B` | 262,144 | 7/1/8 | 성공 |
| `local/Hy-MT2-30B-A3B-MLX-4bit` | 262,144 | 19/1/20 | 성공 |
| `local/Hy-MT2-7B-bf16` | 262,144 | 6/1/7 | 성공 |
| `local/mlx-community--Qwen3-TTS-12Hz-0.6B-Base-8bit` | 131,072 | - | TTS endpoint 필요 |
| `local/mlx-community--whisper-large-v3-turbo-asr-fp16` | 131,072 | - | transcription endpoint 필요 |
| `local/Qwen3-Coder-Next-MLX-4bit` | 262,144 | - | 로컬 메모리 ceiling 초과 |
| `local/Qwen3.5-9B-MLX-4bit` | 262,144 | 14/8/22 | 성공 (`length`) |
| `local/Qwen3.6-27B-uncensored-heretic-v2-Native-MTP-Preserved-oQ4-MLX` | 262,144 | 14/8/22 | 성공 (`length`) |
| `local/Qwen3.6-35B-A3B-UD-MLX-4bit` | 262,144 | 14/8/22 | 성공 (`length`) |
| `local/Qwen3.6-35B-A3B-uncensored-abliterix-v2-MLX-4bit-mixed_4_6` | 262,144 | - | 로컬 메모리 ceiling 초과 |
| `local/translategemma-12b-it-8bit` | 131,072 | - | 전용 content template 필요 |
| `local/translategemma-4b-it-4bit` | 131,072 | 14/8/22 | 성공 (`length`) |
