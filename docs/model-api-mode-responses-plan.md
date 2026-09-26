# 모델별 Chat Completions / Responses 선택 설계

상태: Responses 우선 경로 구현, 공급자별 지원 범위 명시

작성일: 2026-09-26

## 목적과 결정 사항

Q는 native Responses가 확인된 Gateway 공급자에서 Responses를 우선 사용한다. Codex App Server, Anthropic 직접 경로, 지원 여부가 불명확한 호환 서버는 Chat Completions를 사용한다. 사용자는 모델별로 어느 API든 명시적으로 지정할 수 있다. 선택한 API에 관계없이 Q의 대화, 이미지 입력, 도구 호출, 스트리밍, 사용량 집계와 모델 전환이 같은 의미로 동작해야 한다.

이 문서는 구현 범위와 통과 조건을 정한다. 아래 설정 키는 이번 구현에 반영되었으며, 실제 공급자별 지원 여부는 별도 검증이 필요하다.

## 기능 동등성 원칙

기존 Chat Completions 경로에서 Q가 제공하는 기능을 Responses 모드의 기준선으로 삼는다. 단순히 `/v1/responses` 호출이 성공하는 것만으로 완료로 보지 않고, Q에서 다음 흐름이 끝까지 같은 결과를 내야 한다.

- 텍스트·이미지 입력, 시스템 지침과 대화 문맥 전달
- 스트리밍의 추론·최종 답변 표시, 완료·오류·취소 처리
- 도구 호출 인자 조립, 실행, 결과 전달, 연속 도구 호출과 다중 턴 이어가기
- 세션 저장·복원, 문맥 압축, 사용량·캐시 토큰 집계
- 기본 대화와 ACP, 하위 에이전트, 역할별 모델 및 모델 그룹 전환

각 항목은 Chat과 Responses에 같은 시나리오를 적용해 회귀 테스트한다. 공급자 고유 기능이나 요청 필드가 API 간에 다르면 해당 모델의 지원 범위를 명시한다. Q의 필수 기능을 Responses에서 구현할 수 없는 모델은 Responses 선택 대상으로 표시하지 않는다. 요청 중 기능을 조용히 생략하거나 Chat으로 자동 전환하지 않는다.

## 변경 경계와 Chat 회귀 방지

Responses 지원은 새 요청·응답 변환기와 이어가기 상태를 중심으로 추가한다. 기존 Chat 요청 생성, 스트림 해석, 도구 호출 처리와 `conversation_id` 동작은 그대로 사용한다. 공통 코드의 변경은 모델별 API 선택을 위한 설정·분기와 두 API가 공유해야 하는 결과 전달에 한정한다. 따라서 코드 변경 위치가 Responses 파일에만 국한되지는 않는다.

API 종류를 명시한 모델은 해당 설정을 따른다. 미설정 모델은 Gateway 공급자가 OpenAI, xAI, OpenRouter의 native Responses 경로로 식별되는 경우 Responses를 사용하고, 그 밖에는 Chat 경로로 들어간다. 이 분기는 요청 시작 시 확정하며, Responses 오류나 타임아웃을 이유로 같은 턴을 Chat으로 재실행하지 않는다.

구현 전후에 기존 Chat 단위·통합 테스트를 통과시키고, Chat 경로의 요청 본문, 스트림 이벤트, 도구 실행, 이미지, 캐시 필드, 사용량과 모델 그룹 전환을 비교한다. 이 검증이 끝나기 전에는 기존 동작이 보존되었다고 단정하지 않는다.

## 구현 전 기준선

| 영역 | 현재 동작 | Responses 전환 시 필요한 작업 |
| --- | --- | --- |
| 설정 | [`config/config.go`](../config/config.go)의 기본 모델과 역할별 모델 설정에는 API 종류가 없다. [`workspace/model.go`](../workspace/model.go)의 작업 공간 설정은 모델 ID만 덮어쓴다. | 실제 선택된 모델 ID와 Gateway 공급자 종류를 기준으로 API 종류를 해석한다. 명시적 모델 설정이 공급자 기본값보다 우선한다. |
| Q 클라이언트 | [`client/client.go`](../client/client.go)에 `CreateResponse`와 `CreateResponseStream`은 있지만 Agent Loop에서 사용하지 않는다. | Responses 요청·이벤트를 Q의 공통 대화 및 도구 이벤트로 변환하고, 다음 턴에 필요한 원본 항목을 보존한다. |
| Agent Loop | [`agentloop/loop.go`](../agentloop/loop.go)는 모든 턴에 `Chat`을 사용하고 `conversation_id`를 전달한다. | 하나의 Agent Loop에서 선택된 모델의 API에 맞는 클라이언트를 호출한다. 턴 종료와 도구 실행의 기존 의미를 유지한다. |
| 모델 그룹 | [`providerhost/model_groups.go`](../providerhost/model_groups.go)의 HTTP 경로는 Chat Completions만 처리한다. Subagent의 모델 후보 변경 시 Chat `conversation_id`를 초기화한다. | 후보 모델마다 API 종류를 다시 선택하고, 이전 후보의 이어가기 상태를 폐기한다. 그룹 HTTP Responses 경로의 지원 범위도 명시한다. |
| Gateway | `llm-provider` Gateway의 Chat 경로에는 공급자별 캐시 최적화가 있다. Responses는 OpenAI 호환 공급자에서 원본 요청을 전달하고, 다른 공급자에서는 Chat의 일부 기능으로 변환한다. | Q가 요구하는 다중 턴 도구 호출과 캐시 계약을 Responses 경로에서도 충족하거나, 충족하지 못하는 공급자를 명시적으로 거절한다. |

Qumi에서 Responses의 프롬프트 접두사 캐시 적중이 관찰되었다. 이는 모델의 자동 캐싱을 확인한 사례이며 Q의 Responses 대화·도구 루프와 Gateway의 공급자별 캐시 최적화가 검증되었다는 뜻은 아니다.

## 외부 설정 계약

모델 ID별 명시적 API 선택을 전역 설정에 둔다. 작업 공간의 기본 모델 변경과 역할별 모델 변경은 기존처럼 모델 ID만 선택한다. 미설정 모델은 Gateway 공급자의 native Responses 지원 정책을 따른다. 따라서 같은 모델 ID는 어디에서 사용하든 같은 API 종류를 가진다.

```yaml
# 정확한 모델 ID별 명시적 설정. 없으면 Gateway 공급자 기본값을 따른다.
model_api_modes:
  openai/gpt-example: responses
  openai/gpt-example-mini: chat_completions
```

- 키는 Gateway가 사용하는 **정확한 모델 ID**다. 모델 이름이나 계열로 지원 여부를 추측하지 않는다.
- 값은 `chat_completions` 또는 `responses`다. 명시한 값은 공급자 기본값보다 우선한다. 알 수 없는 값은 설정 오류로 표시한다.
- 모델 설정 화면에서 현재 선택 모델의 API 종류를 보고 수정할 수 있어야 한다. Responses가 지원되지 않는 모델은 선택할 수 없게 하거나 선택 시 지원 이유가 포함된 오류를 표시한다.
- Responses를 선택한 요청이 실패했다고 Chat Completions로 자동 재시도하지 않는다. 재시도는 같은 API 안에서 안전한 경우에만 수행한다.
- 기존 설정 파일은 수정하지 않는다. 설정에 API 종류가 없는 모델은 확인된 공급자에서 Responses로 전환된다.

Gateway 공급자의 종류, 명시적 `kind`, 공식 API 호스트로 native Responses 경로를 식별한다. 일반 호환 서버는 자동 선택 대상이 아니다. Gateway의 Chat 기반 Responses 어댑터는 원본 출력 항목과 도구 이어가기의 동등성을 보장하지 않으므로 Q가 native Responses를 요구할 때 거절한다. 사용자가 미지원 모델에 Responses를 명시한 경우에도 조용히 Chat으로 전환하지 않고 오류를 표시한다.

## Q 세션의 공통 메시지 형식

작업 공간 세션 v2는 Q 소유의 메시지 형식으로 `transcript`와 `context`를 저장한다. 각 메시지에는 역할, `phase`, 정규화된 텍스트·이미지 블록, 도구 호출 ID·이름·인자와 도구 결과를 둔다. 알 수 없는 기존 콘텐츠 블록은 `raw` 확장 블록으로 보존해 마이그레이션에서 누락하지 않는다. 저장할 때 Chat 또는 Responses 요청 본문을 세션 형식으로 사용하지 않는다.

세션 v1의 Chat 메시지는 로드할 때 공통 형식으로 변환하고 다음 일반 저장 시 v2로 기록한다. 기존 세션 파일을 일괄 수정하지 않는다. Responses의 암호화된 추론·원본 출력 항목은 `response_replay`에 따로 저장하고, 같은 모델의 Responses 이어가기에서만 재사용한다. 완료된 Responses 턴의 캐시 친화성 키는 `response_affinity`에 모델과 함께 저장해 같은 모델·API로 재시작할 때 복원한다. API나 모델을 바꾸면 공통 기록에서 요청을 다시 만들며 공급자 내부 이어가기 상태는 전달하지 않는다.

세션 파일은 임시 파일을 동기화한 뒤 교체한다. TUI와 ACP 모두 사용자 메시지를 모델 요청 전에 저장하고, 도구 호출·결과와 최종 답변도 기록될 때 저장한다. 재시작 시 결과가 없는 도구 호출은 실행 결과를 알 수 없는 중단으로 기록해 다음 요청의 도구 대화 순서를 복구한다. 스트리밍 중 아직 확정되지 않은 출력은 복원하지 않으며, 도구의 외부 부작용을 자동으로 재실행하거나 정확히 한 번 실행되었다고 간주하지 않는다.

Gateway는 기존대로 Chat Completions와 Responses HTTP 경로를 각각 받는다. Q가 선택한 API 경로를 호출하고, 세션 형식 변환은 Q에서 수행한다. 단일 후보 모델 그룹의 Gateway Responses 경로는 해당 후보가 `model_api_modes`에 **명시적으로** Responses로 설정된 경우에 사용한다. 공급자 기본 선호만으로는 그룹 경로를 자동 전환하지 않는다.

## 대화 및 도구 호출 계약

기존 [`agentloop`](../agentloop)은 대화 순서, 도구 실행, 압축, 취소의 책임을 계속 가진다. API 차이는 클라이언트 경계에서 처리한다.

1. Agent Loop가 실제 요청에 사용할 모델을 결정한 뒤, 해당 모델의 API 종류를 해석한다. 모델 그룹의 다음 후보로 이동할 때도 다시 해석한다.
2. Chat 모드는 기존 메시지와 `conversation_id` 흐름을 유지한다.
3. Responses 모드는 Q가 대화 상태를 관리하는 흐름을 기본으로 하고 `store: false`를 전송한다. 현재 OpenAI API는 이 흐름에서 암호화된 추론 항목을 기본 반환한다. 다음 턴에는 이전 응답의 원본 출력 항목을 순서대로 재전송하고, 도구 결과를 동일한 `call_id`의 `function_call_output`으로 추가한다. 공급자별로 필요한 필드와 재전송 가능 항목을 확인하며, native Responses가 아닌 Gateway 어댑터는 명시적으로 거절한다.
4. Q 화면과 도구 실행에는 응답을 공통 형태로 변환해 전달한다. `output_text`와 추론 이벤트의 구분, 도구 이름·인자·호출 ID, 완료 이벤트와 사용량을 유지한다. 스트림의 최종 완료 전에는 도구 호출을 확정하지 않는다.
5. 암호화된 추론 등 공급자 원본 출력 항목은 이어가기에 필요한 비공개 세션 상태로 보관한다. 이를 사용자 표시용 대화, 아카이브 검색 텍스트, 사용량 로그에 섞지 않는다. 세션 저장·복원 시에는 이 상태를 함께 복원하거나 복원 불가를 명시하고 안전하게 새 대화를 시작한다.
6. 대화 압축, API 종류 변경, 모델·공급자 변경 때 공급자 이어가기 상태를 무효화한다. Q가 보존한 가시적 대화로 새 요청을 구성하며, 이전 응답 ID 또는 이전 모델의 암호화된 항목을 새 모델의 이어가기 정보로 사용하지 않는다. 도구 실행 도중에는 상태를 바꾸지 않는다.

Gateway가 만든 합성 `resp_` ID는 공급자의 `previous_response_id`가 아니다. `store: false` 흐름에서 이 값을 서버 측 대화 이어가기에 사용하지 않는다. 원본 출력 항목 없이 화면에 보이는 텍스트만 재전송하면 추론 또는 도구 호출의 문맥이 빠질 수 있으므로 성공으로 간주하지 않는다.

### 모델 그룹과 하위 에이전트

Q 내부의 모델 후보 선택은 각 **구체 모델**의 API 종류를 따른다. 후보가 바뀌면 이전 후보의 `conversation_id`, Responses 원본 출력 항목, 캐시 친화성 식별자를 폐기하고 Q의 공통 대화 기록에서 요청을 재구성한다. 하위 에이전트도 동일한 규칙을 사용한다.

Gateway의 모델 그룹 HTTP 엔드포인트는 후보가 하나이고 그 모델이 Responses로 명시적으로 설정된 경우에만 `/v1/responses`를 제공한다. 여러 후보가 있는 그룹은 원본 추론 항목을 어느 모델에 다시 보낼지 확정할 수 없으므로 명시적으로 거절한다. Chat 그룹의 후보 전환은 기존대로 동작한다.

## 프롬프트 캐시 계약

Responses를 선택했다는 사실만으로 캐시 최적화가 완성되지는 않는다. 공급자의 자동 접두사 캐시와 같은 대화가 같은 캐시 경로에 배치되도록 돕는 설정을 구분한다.

- Q는 대화와 실제 모델에 묶인 안정적인 캐시 친화성 식별자를 생성·보존한다. Gateway가 이를 공급자에 맞게 매핑한다. OpenAI·xAI는 `prompt_cache_key`, OpenRouter는 `session_id` 같은 공급자별 필드를 사용한다. 이 식별자는 응답 ID나 Chat의 `conversation_id`와 의미상 별개다.
- 사용자가 요청 또는 공급자 설정에 명시한 캐시 값이 있으면 자동 생성값보다 우선한다. API·모델·공급자 전환 또는 대화 압축으로 공통 접두사가 달라질 때 자동 식별자를 갱신한다.
- 공급자별 지원 여부를 확인하고 Chat 경로의 기존 최적화와 동등한 규칙을 Responses 경로에도 적용한다. 공급자에게 전달되지 않아야 하는 Gateway 전용 식별자는 제거한다.
- 안정적인 접두사 경계를 알 수 없는 요청에 캐시 breakpoint를 일괄 삽입하지 않는다. 공급자가 요구하는 명시적 캐시 표시가 필요한 경우에는 해당 공급자와 요청 형태에 맞는 경계를 정의한다.
- 캐시 검증은 동일한 긴 접두사를 두 번 전송하고 공급자가 보고한 `cached_tokens`, 캐시 작성 토큰과 지연 시간을 확인한다. 짧은 프롬프트에서 적중 토큰이 0인 결과만으로 결함을 단정하지 않는다.

## 구현 순서

1. **llm-provider 계약:** 모델의 native Responses 지원 신호와 미지원 오류를 정한다. Responses 요청·스트림에서 원본 출력 항목과 `function_call_output`의 전달, 캐시 친화성 매핑, 사용량 반환을 검증한다. 어댑터가 지원하지 않는 필드를 조용히 버리지 않게 한다.
2. **Q 클라이언트:** Chat과 Responses의 요청·결과를 Agent Loop용 공통 형태로 변환한다. Responses 원본 항목의 비공개 이어가기 상태와 스트림 완료·취소 처리를 추가한다.
3. **Q Agent Loop와 설정:** 구체 모델별 API 선택을 기본 대화, ACP, 하위 에이전트, 모델 후보 전환에 적용한다. 설정 검증과 모델 설정 화면을 연결한다.
4. **Gateway 모델 그룹:** `/v1/responses` 그룹 요청을 지원 계약에 맞게 구현하거나 명시적 미지원 오류를 제공한다.
5. **회귀 및 실제 호출:** 아래 기준을 자동 테스트로 확인한 후, opt-in 실제 Gateway 호출로 캐시 및 다중 턴을 확인한다.

## 완료 기준

| 시나리오 | 확인할 결과 |
| --- | --- |
| 설정 호환성 | 미설정 모델은 확인된 native 공급자에서 Responses, 그 외에는 Chat Completions로 동작한다. 모델별 명시적 설정이 우선하고 잘못된 API 값은 설정 오류가 된다. |
| Responses 기본 대화 | 두 턴 이상의 텍스트 대화와 이미지 입력이 원본 요청에 보존된다. 스트림과 비스트림의 화면 출력 의미가 같다. |
| 도구 이어가기 | `function_call`의 `call_id`에 맞는 결과가 다음 Responses 요청의 `function_call_output`으로 들어간다. 암호화된 추론·원본 출력 항목의 순서가 보존된다. |
| 완료·취소·오류 | 완료 이벤트 전에는 도구가 실행되지 않는다. 중단·재시도로 같은 도구의 부작용이 중복 실행되지 않는다. 미지원 공급자는 명확한 오류를 낸다. |
| 모델 전환 | 작업 공간 기본 모델, 역할별 모델, 하위 에이전트, 모델 그룹 후보마다 실제 모델의 API 종류가 적용된다. 후보 전환 시 이전 공급자의 이어가기 상태가 새 요청에 섞이지 않는다. |
| 캐시 | 같은 모델·대화의 연속 턴에 안정적인 친화성 값이 전달된다. 명시적 값이 우선하며 모델·API·압축 전환 시 자동 값이 갱신된다. 공급자 사용량에서 캐시 토큰을 확인할 수 있다. |
| 보존과 복원 | 세션 복원과 압축 후에도 대화가 이어진다. 공급자 원본 항목이 사용자 대화 기록과 검색 텍스트에 노출되지 않는다. |
| Chat 회귀 | 기존 Chat Completions의 도구 호출, 이미지, 캐시 최적화, `conversation_id`, 사용량 집계가 유지된다. |

자동 테스트에는 가짜 upstream을 사용해 전송 본문, 헤더, 스트림 이벤트, 도구 호출 ID와 실패 처리를 검사한다. 실제 공급자 테스트는 별도로 표시한다. Qumi에서 관찰한 캐시 적중을 Q의 테스트 결과로 대신 기록하지 않는다.

## 이번 구현의 검증 상태

- 모델별 `model_api_modes` 설정과 모델 화면의 `ctrl+r` 전환을 추가했다. 미설정 모델은 확인된 native 공급자에서 Responses, 그 외에는 Chat 경로로 간다.
- Q 클라이언트에서 native Responses 요청·스트림을 공통 대화 형식으로 변환한다. 응답 원본 항목과 생성 모델은 메시지의 비공개 필드와 작업 공간 세션의 `response_replay`에 보존하고 검색용 아카이브 메시지에는 직렬화하지 않는다. 다른 모델로 전환한 요청에는 가시적 대화만 재구성한다. `commentary` 메시지와 최종 답변을 `phase`로 분리하고 원본 출력 항목의 phase를 이어가기에 보존한다.
- 세션 v2의 Q 공통 메시지 형식과 v1 Chat 세션 로드 변환을 구현했다. 공통 세션을 복원한 다음 Chat와 Responses 양쪽 요청으로 이미지·도구 호출·최종 답변 phase를 구성하는 테스트를 추가했다.
- Gateway는 Q가 native Responses를 요구할 때 Chat 어댑터를 거절하고, Responses의 `cache_affinity_id`를 공급자별 캐시 필드로 옮긴 뒤 upstream 전송 전에 제거한다. 모델 그룹은 단일 후보가 Responses로 설정된 경우에만 Responses 경로를 허용한다.
- 가짜 upstream을 사용한 Q→Gateway→Responses 다중 턴 테스트에서 도구 결과·원본 추론 항목·캐시 친화성 전송을 확인했다. 이미지 변환, 스트리밍 완료 조건, 그룹 전환, 세션 원본 항목 복원은 각각의 자동 테스트로 확인했다.
- 실제 공급자 호출 결과는 아래 표에 기록한다. 테스트는 Q 클라이언트와 현재 `llm-provider` 의존성으로 임시 Gateway를 만들고 실행한다. 로컬에 이미 실행 중인 Gateway 프로세스에는 영향을 주지 않는다.
- `go test -p 1 ./...`, `go build ./...`, `go vet ./...`, `golangci-lint run --no-config ./...`가 통과했다. 실제 공급자 테스트는 `Q_RESPONSES_INTEGRATION_MODEL=openai/gpt-5-nano`와 `Q_RESPONSES_CACHE_PROBE=1`을 지정해 별도로 실행했다.

현재 Responses 변환기는 Q의 Chat 전용 `stop` 요청을 명시적 오류로 거절한다. Q의 현재 실행 경로에서는 `stop`을 사용하지 않는다. `output_schema`는 Responses의 `text.format` JSON Schema로 옮기며 실제 OpenAI·xAI·OpenRouter Claude 호출에서 검증했다. 여러 후보를 가진 Gateway HTTP 모델 그룹도 Responses 요청을 거절한다. ACP와 하위 에이전트는 공통 클라이언트 경로를 사용하지만, 실제 공급자 호출 검증은 위 기본 대화·도구·이미지·스트리밍 시나리오까지 수행했다.

### 실제 공급자 검증 (2026-09-26)

| 경로와 모델 | 두 턴·도구 이어가기·스트리밍·이미지 | 긴 접두사 캐시 읽기 |
| --- | --- | --- |
| OpenAI `openai/gpt-5-nano` | 통과 | 두 번째 요청 2,560토큰 적중. 첫 시도는 0, 재시도에서 적중 |
| xAI `xai/grok-4.5` | 통과 | 두 번째 요청 3,200토큰 적중 |
| OpenRouter `openrouter/openai/gpt-5-nano` | 통과 | 두 번째 요청 2,560토큰 적중 |
| OpenRouter Claude `openrouter/anthropic/claude-haiku-4.5` | 통과 | 새 접두사로 첫 요청 5,269토큰 작성, 두 번째 요청 5,269토큰 읽기. Gateway가 최상위 `cache_control`을 자동 설정 |
| Anthropic 직접 `claude/claude-haiku-4-5-20251001` | native Responses 미지원 오류 확인. 같은 모델의 Chat Completions 텍스트 요청은 통과 | Responses 캐시 검증 대상 아님 |

OpenRouter의 Claude Responses 캐시는 [OpenRouter의 `cache_control` 계약](https://openrouter.ai/docs/guides/best-practices/prompt-caching)에 따라 최상위 자동 캐시 설정을 넣는다. 요청이나 공급자 설정이 명시한 값은 덮어쓰지 않는다. OpenAI의 `prompt_cache_key`는 캐시 경로 선택에 영향을 주지만 적중을 보장하지 않으므로 [OpenAI의 프롬프트 캐시 안내](https://developers.openai.com/api/docs/guides/prompt-caching)에 따라 단일 캐시 미스를 전송 실패로 해석하지 않는다.

## 참고 자료

- [OpenAI Prompt Caching](https://developers.openai.com/api/docs/guides/prompt-caching)
- [OpenRouter Responses API](https://openrouter.ai/docs/api/api-reference/responses/create-responses)
- [xAI Prompt Caching](https://docs.x.ai/developers/advanced-api-usage/prompt-caching/maximizing-cache-hits)
