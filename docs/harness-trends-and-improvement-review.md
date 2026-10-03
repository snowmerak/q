# 에이전트 하네스 개발 동향과 Q 개선 제안

작성·조사일: 2026-10-03 KST

코드 기준: `5694174d48da0eb811e377957e934ada296d9b4a` — 2026-10-02, `docs: record retrieval routing experiment results`

상태: 코드 조사와 공개 자료에 근거한 **개선 제안**. 승인된 구현 계획이나 새 기능의 완료 기록이 아니다.
대상: Q의 런타임, 도구, 세션, 모델 연결, Studio를 개발하는 유지보수자.

## 1. 판단과 우선순위

**Q의 다음 투자처는 기존 실행 기반 위에서 작업 성공을 측정하고, 완료 근거와 실행 비용을 통제하는 기능이다.** Q에는 이미 임베딩 가능한 Agent Loop, 역할별 도구 제한, 위임 세션 복구, Git worktree 변경 검토, Loom 결과 저장, 컨텍스트 압축과 메모리 도구가 있다. 이들을 새로 도입하는 로드맵은 현재 상태에 맞지 않는다.

최근 공개 사례의 공통점은 모델과 하네스를 함께 평가하고, 실행 기록에서 실패 원인을 찾으며, 세션·컨텍스트·실행 환경의 수명을 분리한다는 것이다. Q에 적용할 때는 다음 순서를 권한다. P0는 다음 개발 주기의 우선 투자라는 뜻이며, 전부 현재의 결함이나 출시 차단 문제라는 뜻은 아니다.

| 순위 | 개선안 | Q에서 바뀌어야 할 결과 | 규모·의존성 |
|---|---|---|---|
| P0 / A | 작업 단위 trace와 지속 가능한 eval | 프롬프트·도구·모델 변경의 품질/비용 효과를 같은 작업에서 비교 | 중간. 다른 개선의 측정 기반 |
| P0 / B | 완료 선언과 검증 근거 연결 | `succeeded`와 실제 확인된 결과를 구별 | 중간. A의 최소 식별자·artifact 연결 활용 |
| P0 / C | 실행 트리 전체 예산과 정체 감지 | 부모·자식·재시도가 공동 한도 안에서 실행 | 중간~큼. 사용량 연결과 복구 계약 필요 |
| P1 / D | 점진적 작업 상태를 활용하는 압축 | 제약과 미완료 작업을 보존하면서 압축 비용·재탐색 감소 | 중간~큼. 기존 memory 연구의 후속 |
| P1 / E | 실행 권한과 실행 환경의 명시적 계약 | 로컬 신뢰 실행과 제한된 실행의 보장 수준을 구분 | 큼. 비신뢰 저장소·원격 운영을 지원한다면 P0 |
| P1 / F | 캐시를 보존하는 도구 정의의 선택적 로딩 | 큰 카탈로그의 입력 부담을 줄이면서 기존 prefix 재사용 | 조건부. provider·gateway 지원과 총비용 검증 필요 |
| P2 / G | 라우팅·위임을 품질 기준으로 최적화 | 저비용 모델과 위임의 이득이 입증된 작업에만 확대 | 실험 중심. A 선행 |
| P2 / H | 현재 계약과 과거 문서의 구분 | 에이전트가 제거된 workflow를 현재 기능으로 오인하지 않음 | 작음. 독립적으로 진행 가능 |

규모는 일정 약속이 아니라 변경 범위의 상대적 추정이다. 팀 규모와 배포 목표는 조사 범위에 없으므로 주 단위 일정은 제시하지 않는다.

## 2. 조사 범위와 해석 방법

여기서 **하네스**는 모델 주변의 요청 구성, 도구 호출, 실행 상태, 권한, 복구, 검증, 관찰 기능을 뜻한다. 평가를 실행하는 *evaluation harness*와는 구분한다.

- 외부 동향은 직접 열어 확인한 공식 기술 글·제품 발표를 사용했다. 검토 자료는 2025-11~2026-09 발표를 포함하며, 최신 자료는 2026-09-10 OpenAI Agents API 발표다. 시장 전체를 빠짐없이 조사했다는 의미는 아니다.
- Q는 `README`, 현행 기능 문서와 주요 실행 경로를 대조했다. 문서의 계획·역사적 상태보다 현재 코드를 우선했다.
- 코드에서 확인한 사실, 기존 실험의 결과, 이 보고서의 제안을 구분한다. 상용 서비스 발표는 방향을 보여주는 근거이며 Q의 성능을 증명하지 않는다.
- 새 live-model 실험이나 경쟁 제품 벤치마크는 실행하지 않았다. 따라서 Q의 성공률, 평균 비용, 경쟁 제품 대비 순위는 주장하지 않는다.
- 기존 [구조 리팩터링 로드맵](refactoring-roadmap.md)은 완료된 패키지 분리의 기록이다. 여기서 `agentloop` 분리나 host lifetime 통합을 다시 미구현 과제로 잡지 않는다.

## 3. 현재 동향과 Q에 대한 의미

### 3.1. 하네스 변경도 실험으로 평가한다

LangChain은 같은 모델을 유지하고 하네스를 수정한 실험에서 Terminal Bench 2.0 점수가 52.8에서 66.5로 올랐다고 보고했다. 핵심 방법은 trace 분석, 종료 전 검증 유도, 반복 실패 감지와 추론 예산 조정이었다. 이는 해당 환경의 결과이며 Q에 같은 상승 폭을 기대할 근거는 아니다. Q에는 **변경 전후를 같은 조건에서 비교하는 개선 루프**가 유용하다. [LangChain, 2026-02-17](https://www.langchain.com/blog/improving-deep-agents-with-harness-engineering)

Anthropic은 에이전트가 말한 성공과 실제 환경의 최종 상태를 구분하고, 반복 trial 및 코드·모델·사람 기반 채점을 목적에 맞게 조합한다. Q도 런타임 회귀 테스트와 실제 모델의 작업 수행 평가를 따로 유지해야 한다. [Anthropic evals, 2026-01-09](https://www.anthropic.com/engineering/demystifying-evals-for-ai-agents)

### 3.2. 장기 실행은 이어받을 상태와 검증 기준을 설계하는 문제다

Anthropic의 장기 실행 사례는 작은 작업 단위, 진행 기록, 다음 실행이 복원할 수 있는 환경, 사용자 관점의 검증을 강조한다. 후속 사례는 generator와 evaluator를 분리하고 평가 기준을 구체화했다. Q의 복구와 역할 위임은 이 방향에 부합한다. 추가 과제는 **복구한 실행이 어떤 요구사항을 충족했고 무엇을 검증했는지**를 구조적으로 연결하는 것이다. [장기 실행, 2025-11-26](https://www.anthropic.com/engineering/effective-harnesses-for-long-running-agents), [애플리케이션 개발 하네스, 2026-03-24](https://www.anthropic.com/engineering/harness-design-long-running-apps)

### 3.3. 세션, 모델 컨텍스트, 실행 환경의 경계가 중요해졌다

Anthropic Managed Agents는 세션 로그, 모델·도구 루프, sandbox를 분리한다. OpenAI의 9월 Agents API 발표도 관리형 하네스와 선택 가능한 실행 환경을 구분한다. 이는 Q를 클라우드 서비스로 전환해야 한다는 뜻이 아니다. Q의 기존 임베딩 경계를 유지하며 **실행 환경을 교체해도 세션과 정책 계약이 유지되도록** 만드는 근거다. [Anthropic, 2026-04-08](https://www.anthropic.com/engineering/managed-agents), [OpenAI, 2026-09-10](https://openai.com/index/introducing-the-agents-api/)

### 3.4. 도구와 지식을 필요할 때 가져온다

Anthropic은 tool search와 programmatic tool calling을 통해 도구 정의와 중간 결과의 컨텍스트 부담을 줄이는 방식을 설명한다. OpenAI의 최근 하네스 발표에도 이 두 기능이 포함됐다. Q에는 이미 결과 외부화와 `loom_eval`이 있으므로, 다음 후보는 **도구 정의의 로딩 비용**과 **결과를 다시 읽는 방식**이다. [Anthropic, 2025-11-24](https://www.anthropic.com/engineering/advanced-tool-use), [OpenAI Agents API](https://openai.com/index/introducing-the-agents-api/)

### 3.5. 저장소와 실행 결과를 에이전트가 직접 이해할 수 있게 만든다

OpenAI의 harness engineering 사례는 짧은 저장소 안내, 구조화된 문서, 도구로 확인할 수 있는 UI·로그·지표, 기계적으로 검사하는 경계를 강조한다. Q에 적용하면 안내 문서를 늘리는 것보다 **실행한 명령·관찰한 결과·수정한 revision을 연결해서 읽을 수 있게 하는 것**이 우선이다. [OpenAI, 2026-02-11](https://openai.com/index/harness-engineering/)

### 3.6. 모델이 개선되면 하네스의 보조 규칙도 재검토한다

Anthropic은 이전 모델의 조기 종료를 보완하던 context reset이 후속 모델에서는 불필요해진 사례를 설명한다. Q도 반복 경고, 역할별 프롬프트, 강제 검토 단계를 모델에 무관한 영구 규칙으로 누적하기보다 켜고 끌 수 있게 하고 평가해야 한다. [Anthropic Managed Agents](https://www.anthropic.com/engineering/managed-agents)

## 4. Q의 실제 기반과 남아 있는 간극

| 영역 | 확인한 현재 상태 | 개선 판단의 경계 | 근거 |
|---|---|---|---|
| 실행 코어 | host가 client·tools·persistence의 수명을 소유하는 공개 `agentloop` API | 새로운 프레임워크나 서비스 분할이 필수는 아님 | [types.go](../agentloop/types.go), [embedding 계약](agent-loop-embedding.md) |
| 완료 처리 | `task_start`의 완료 조건과 `task_complete`의 결과·검증 문자열을 지원 | 메인 완료 경로는 검증 문자열을 실제 실행 결과와 대조하지 않음 | [orchestration.go](../agentloop/orchestration.go), [loop.go](../agentloop/loop.go) |
| 도구 권한 | `ScopeTools`는 역할에 없는 도구 호출을 거부; MCP 역할별 목록 지원 | 이름 기반 역할 제한과 명령 프로세스 격리는 서로 다른 보장 | [tool_scope.go](../agentloop/tool_scope.go), [runtime.go](../tools/runtime.go) |
| 결과 처리 | 일반 도구 결과를 Loom에 저장하고 bounded receipt 제공; `loom_eval`로 artifact를 처리 | 결과 외부화·코드 기반 결과 처리는 이미 있음 | [runtime.go](../tools/runtime.go), [loom.go](../tools/builtin/loom.go), [eval.go](../loom/eval.go) |
| 컨텍스트 | 원본 이력과 모델용 컨텍스트 분리; 작업·사실 메모리 갱신과 replay 지원 | 압축 요청은 여전히 `Plan.Source`를 직렬화해 모델에 전달 | [manager.go](../memory/manager.go), [tools.go](../memory/tools.go) |
| 복구 | 중첩 위임 복구, 저장된 결과 재사용, 불명확한 실행은 `unknown` 처리 | 무조건 재실행하지 않는 계약을 보존해야 함 | [복구 계약](delegation-session-recovery.md), [복구 구현](../app/delegation_recovery.go) |
| 변경 격리 | 위임용 worktree와 base/head commit을 고정한 change request; 부모 상태 검사 후 merge | Git 검토 기반은 있음. 실행 증거를 해당 revision에 연결할 여지가 있음 | [manager.go](../gitwork/manager.go) |
| 추적·사용량 | Studio의 cursor·timestamp 포함 실행 이벤트, 위임 trace, 모델·역할별 token usage | `UsageRecord`에는 session/run 연결 필드가 없어 작업별 품질·비용 분석에 추가 상관관계 필요 | [runs.go](../studio/runs.go), [trace.go](../subagent/trace.go), [usage.go](../client/usage.go) |
| 테스트 | 로컬 모델 fixture 기반 런타임·브라우저 회귀 테스트 및 검색 실험 기록 | 현재 기본 테스트가 live-model 작업 품질을 측정하는 것은 아님 | [testing.md](testing.md), [검색 실험](retrieval-routing-experiment.md) |

## 5. 개선안별 설계와 완료 기준

### A. 작업별 trace와 eval을 지속 가능한 개발 자산으로 만든다 — P0

**근거.** `client.UsageRecord`는 물리적인 provider 호출의 event ID, 모델, 역할, 토큰과 추정 여부를 기록한다. `app.SessionEvent`에는 run/session/task/call 식별자가 있고 Studio가 시간과 cursor를 부여한다. 두 기록을 연결하면 작업별 분석이 가능하지만, 현재의 usage 구조 자체에는 이 연결이 없다. [사용량 계약](../client/usage.go), [세션 이벤트](../app/session_host.go), [Studio 저장](../studio/runs.go)

**최소 변경.** 기존 로그를 대체하는 대형 관측 플랫폼보다 다음 연결부터 추가한다.

- 논리 작업의 root ID, session/run ID, invocation ID와 물리 provider usage event ID의 매핑.
- tool call은 `call_id` 단독 대신 session·run·호출 순번을 포함한 식별자로 연결. 재사용된 call ID와 provider 재시도를 구분한다.
- 실행 manifest에 Q commit, 모델 ID, API mode, reasoning 설정, prompt/tool catalog 버전, workspace revision과 OS를 기록한다. 서버 측 모델 revision을 알 수 없으면 그 한계를 남긴다.
- model/tool/compaction/delegation의 시작·종료·결과를 기록하고 Loom artifact와 연결한다. 기존 사용량 로그의 token-only 성격은 유지하고, 원문은 별도의 접근 범위를 가진 trace에서 다룬다.
- 한 작업의 실패한 시도까지 포함하는 token·latency·verification 결과를 내보내는 최소 실행기를 만든다. 예컨대 `evals/`와 `q eval`은 **제안 이름**이며 현재 명령이 아니다.

**초기 평가 세트 제안.** 20~30개 정도의 작고 대표적인 과제에서 시작한다. 개수 자체를 성숙도 기준으로 삼지 않는다.

| 유형 | 판정 대상 | 우선 채점 방식 |
|---|---|---|
| 기존 버그 수정 | 수정 전 실패·수정 후 성공, 기존 동작 보존 | 고정된 테스트와 최종 diff |
| 저장소 조사 | 근거 위치·관련 코드 회수, 답변의 근거성 | source 범위 검사 + 일부 사람 검토 |
| 긴 작업·사용자 정정 | 압축 후 최신 제약 준수, 완료/미완료 구분 | 결정적 fixture + 별도 live trial |
| 중단·복구 | 중복 부작용 방지, unknown 보존, 재개 일관성 | 장애 주입·저장 상태 검사 |
| 위임·merge | 검토 대상과 결과 revision의 일치 | 실제 임시 Git 저장소 |
| 도구·권한 | 허용 범위 준수, 정의 발견·호출 성공 | 결정적 정책 검사 |

기존 검색 실험은 20개 held-out 과제를 평가했지만 실행기와 원시 로그는 종료 후 제거됐다고 명시한다. 결과를 무효화할 이유는 없으나, 지속적인 재평가에는 별도 재현 가능한 fixture·manifest·채점기를 새로 유지해야 한다. 예전 임시 파일이 남아 있다고 전제하지 않는다. [실험 범위와 보존 상태](retrieval-routing-experiment.md)

**완료 기준.** 같은 데이터셋·환경·budget으로 baseline과 후보를 재실행할 수 있고, 실패까지 포함한 성공률/토큰/latency를 비교할 수 있어야 한다. 개발용과 평가용 과제를 분리하고, 모델 trial을 반복한다. 초기 소표본의 차이는 task별 결과와 불확실성을 함께 제시한다. live 호출은 명시적으로 선택하는 별도 suite로 두고 일반 회귀 테스트를 네트워크·과금에 의존시키지 않는다.

### B. 완료 선언을 검증 evidence와 연결한다 — P0

**근거.** `parseTaskComplete`는 outcome·summary와 blocked일 때의 blocker를 검증한다. `verification`은 문자열 배열이고, 메인 루프는 그것을 실행된 명령·결과·파일 상태와 연결해서 검사하지 않는다. 즉 구조화된 완료 응답은 있지만, 그 응답 자체가 검증 증명은 아니다. [파서](../agentloop/orchestration.go), [완료 분기](../agentloop/loop.go)

**발생 조건과 영향.** 모델이 테스트를 실행하지 않았거나 테스트 후 코드를 다시 바꿨는데도 `succeeded`를 내면, 수신자가 완료 문자열만으로 확인 수준을 구분하기 어렵다. 실제 오류 사례를 재현했다는 주장이 아니라 현재 계약이 허용하는 상태다.

**제안.** 완료 응답과 별도로 host가 관리하는 verification receipt를 둔다. 작업 결과와 검증 상태는 독립적으로 기록한다. 기존 `succeeded`/`blocked` wire enum을 즉시 확장하기보다 호환 가능한 메타데이터부터 추가한다.

| 제안 필드 | 의미 |
|---|---|
| `criterion_id` | 어떤 완료 조건을 확인했는가 |
| `verifier` / `tool_call_ref` | 명령, 테스트, 브라우저 검사, 파일 확인의 실제 실행 위치 |
| `status` | `passed`, `failed`, `not_run`, `not_applicable`, `unknown` |
| `workspace_revision` | Git commit 또는 변경 파일 manifest의 digest |
| `evidence_ref` | 출력, diff, screenshot 등의 Loom 참조 |
| `reason` | 실행하지 못했거나 해당하지 않는 이유 |

host가 관찰한 exit code와 결과를 receipt의 근거로 사용한다. 임의의 `echo success`를 실행한 사실만으로 완료 조건이 검증됐다고 인정하지 않는다. verifier 선택은 사용자 요구나 저장소 계약에 연결한다. 테스트 후 대상 파일이 바뀌면 이전 receipt는 과거 증거로 유지하고 현재 변경의 검증 상태는 갱신한다.

문서 작성·조사에는 실행 테스트가 항상 필요하지 않다. 파일 존재, 링크, 출처, 요구 범위 확인 등 작업 유형에 맞는 verifier를 사용하고, 의미적 정확성을 완전히 자동 판정한다고 주장하지 않는다.

**수정 위치.** `agentloop`의 완료 이벤트와 host의 결과 저장, `workspace`의 task metadata, `gitwork`의 change request, Studio의 완료 표시. 임베딩 host가 선택할 수 있는 정책으로 제공하고 기본 API 사용자가 별도 서비스에 의존하지 않게 한다.

**완료 기준.** 검증 미실행, 실패, 실행 결과 유실, 검증 후 수정의 네 경우가 통과 상태로 표시되지 않아야 한다. 한 완료 조건에서 실제 receipt와 당시 revision으로 이동할 수 있어야 한다. 검증할 수 없는 결과는 그 범위를 명시해서 종료할 수 있어야 한다.

### C. 실행 트리 전체의 budget과 정체 감지를 도입한다 — P0

**근거.** 메인 `RunAgentLoop`는 종료 조건을 만날 때까지 반복하며 공개 `Request`에는 별도 budget이 없다. 일반 위임 runner에는 `MaxRounds`와 기본 320회 제한이 있고, 앱의 위임 dispatcher에도 깊이 4와 호출 32회 제한이 있다. 따라서 Q에 제한이 전혀 없다는 진단은 틀리지만, 이 한도들은 **전체 작업의 공통 토큰·시간 예산**을 표현하지는 않는다. [메인 루프](../agentloop/loop.go), [Request](../agentloop/types.go), [GeneralRunner](../subagent/delegation.go), [위임 dispatcher](../app/delegation.go)

**제안.** host가 소유하는 선택적 budget controller를 루트 작업에 연결한다. 모든 자식, provider retry/fallback, 압축 호출이 같은 ledger에 귀속되도록 한다.

- 처음에는 model-call 수, 누적 token, 경과 시간과 동시 작업 수를 지원한다. 금액 상한은 가격 버전·사용량 누락 처리까지 정의한 뒤 추가한다.
- 실행 전에 상한을 예약하고 완료 뒤 실제 usage로 정산한다. 보고되지 않은 usage는 0으로 간주하지 않고 추정·미확인 상태를 구분한다.
- hard limit과 사용자에게 알리는 soft limit을 구분한다. 상한 도달 시 신규 작업을 막고 진행 상태를 저장한다. 이미 시작한 부작용을 성공·실패로 임의 확정하지 않는다.
- 재시작 후에도 소진량과 예약 상태를 복원한다. 사용자 응답 대기 시간을 wall-time에 포함할지는 정책으로 명시한다.

정체 감지는 동일한 도구 이름의 반복만으로 구현하지 않는다. `wait`는 정상 반복이고 파일이 바뀌었다면 같은 `read_file`도 새 증거다. 정규화된 입력, 파일 revision/결과 digest, 새 evidence 유무, 같은 오류의 반복을 함께 본다. 처음에는 재계획을 유도하는 관찰 모드로 평가하고, 중단 정책은 정상 작업의 오탐률을 확인한 뒤 적용한다.

**완료 기준.** 자식 위임·fallback·압축·재시작으로 예산을 우회할 수 없어야 한다. 한도에서 추가 호출이 시작되지 않고, 남은 상태와 중단 이유를 복구할 수 있어야 한다. 재탐색 감소는 실제 개선 지표이고, 경고를 많이 냈다는 사실은 성과가 아니다.

### D. 점진적 메모리를 압축 경로까지 연결한다 — P1

**구현 업데이트 (2026-10-03).** `memory_checkpoint`의 명시적 확인과 revision 검증을
추가했다. 유효한 기준 상태 이후의 미반영 이력만 보충 요약하고, 모두 반영됐으면
요약 모델 호출을 생략한다. 사용자 원문·정정·첨부 참조는 별도 앵커로 보존하며
TUI/ACP와 내부 에이전트가 같은 경로를 사용한다. 결정적 회귀 테스트를 추가했고,
아래 live trial 완료 기준은 아직 검증하지 않았다.
[현재 동작과 제한](context-compaction-plan.md#점진적-task-memory-압축-2026-10-03)

**검토 당시 근거.** `memory_set_active_work`, `memory_complete_work`, `memory_record_fact`와 결과 replay는 이미 구현돼 있었다. 압축에는 유지한 task memory를 overlay하지만 `Plan.RequestMessages()`는 오래된 `Source` 전체를 직렬화했다. 기존 [압축 품질 연구](context-compaction-quality-research.md)의 후속 구현 지점이었다. [manager.go](../memory/manager.go), [tools.go](../memory/tools.go)

**제안.** 압축을 즉시 없애기보다 단계적으로 변경한다.

1. 사용자 원문·후속 정정, 작업 상태, 사실의 출처를 구분하는 projection을 강화한다. 기존 task anchor와 원문 보존을 재사용한다.
2. 메모리 갱신에 event identity와 revision을 연결해 중복 적용과 오래된 갱신을 구분한다. 모델의 사실 요약은 사용자 지시나 실제 실행 상태의 권위를 대신하지 않는다.
3. 메모리 갱신 이후 누락된 구간만 보충 요약하는 후보 경로를 만들고 기존 full-source 압축과 비교한다. 상태가 충분하지 않으면 기존 경로로 돌아간다.
4. 압축 후 필요한 원문은 session/archive 또는 Loom에서 다시 가져올 수 있게 한다. 다음에 필요한 정보가 요약에 반드시 남는다고 가정하지 않는다.

**평가.** 사용자 제약 보존, 완료 작업의 재실행, 필요한 source 재회수, 반복 읽기, 압축 token·latency를 함께 측정한다. “압축률이 높다”만으로 성공이라고 하지 않는다. 반복 압축, 뒤늦은 사용자 정정, 도구 결과 유실을 포함한다.

**완료 기준.** 결정적 fixture에서 최신 정정과 미완료 상태가 보존되고, live trial에서는 기준선보다 작업 성공률을 떨어뜨리지 않으면서 압축 비용 또는 불필요한 재탐색 감소가 관찰돼야 한다. 이 효과는 아직 측정하지 않았다.

### E. 권한 정책과 실행 환경을 교체 가능한 경계로 만든다 — P1

**근거.** 파일 도구의 루트 제한과 역할별 호출 제한은 이미 있다. 반면 `run_command`는 작업 디렉터리를 확인한 뒤 플랫폼 shell을 실행한다. Q의 보안 문서도 이를 OS sandbox가 아니라고 설명한다. 외부 Web Tester에는 자동 permission 정책이 적용된다. 이는 현재의 신뢰 실행 계약이며, 별도의 격리 보장을 뜻하지 않는다. [commands.go](../tools/builtin/commands.go), [보안 경계](../web/src/docs/ko/concepts/security.md), [Web Tester](../app/acp_web_tester.go), [ACP permission](../app/acp_client.go)

**제안.** 로컬 신뢰 모드를 유지하면서, 실행 프로필이 실제로 보장하는 capability를 host가 확인하고 전달한다.

- 파일 read/write 범위, 프로세스 실행, 네트워크, 자격 증명 전달, 외부 변경의 범위를 별도 정책으로 표현한다.
- 정책 판정은 광고한 도구 목록뿐 아니라 실행 직전 경계에도 적용한다. 동적으로 발견된 도구와 ACP 위임도 상위 grant를 확대하지 못하게 한다.
- `local trusted`와 제한된 executor를 구분하고, 제한된 모드가 OS·컨테이너 등에 의해 보장되지 않으면 해당 모드의 시작을 거부한다. worktree나 cwd를 sandbox로 표시하지 않는다.
- 제한된 executor에는 필요한 환경변수만 전달하고 provider/MCP 자격 증명은 가능한 한 실행 프로세스 밖의 연결 계층에서 사용한다.
- 기존 사용자 선택에 따른 자동 실행을 유지하되, 외부 agent가 가진 권한과 Q가 직접 강제할 수 있는 범위를 표시한다. 모든 호출에 확인창을 추가하는 방식은 피한다.

**완료 기준.** 제한 모드에서 허용 밖 경로·네트워크·자격 증명 접근을 실제 executor 대상으로 검증한다. Windows와 POSIX의 지원 수준을 별도로 기록한다. UI의 정책 표시와 실제 강제 여부가 일치해야 한다.

이 방향은 세션과 실행 환경을 분리한 공개 사례에서 참고할 수 있다. Q에서는 하나의 Go 배포 단위를 유지한 채 인터페이스와 선택적 executor부터 도입할 수 있다. [Anthropic Managed Agents](https://www.anthropic.com/engineering/managed-agents)

### F. 캐시를 보존하는 도구 로딩과 Loom 재사용을 검토한다 — P1, 조건부

**근거.** `ToolsForRole`은 역할에 연결된 MCP 서버의 도구 정의를 모은다. 메인 루프는 시작 시 `availableTools`를 구성하고 그 목록을 각 요청에 전달한다. 역할별 제한은 있지만, 해당 루프의 계약은 요청 중 검색해서 schema를 추가하는 구조는 아니다. [카탈로그](../tools/runtime.go), [요청 구성](../agentloop/loop.go)

**캐시와의 상충 관계.** 일반 `tools` 목록을 실행 중 추가·삭제·재정렬하면 모델 입력 앞부분이 바뀌어 이후 컨텍스트의 캐시 재사용을 잃을 수 있다. Anthropic은 일반 도구 정의 변경이 tools→system→messages 캐시를 무효화한다고 명시하고, OpenAI도 정의·순서·schema를 안정적으로 유지하도록 안내한다. 정의 token을 줄여도 긴 대화의 cache miss와 재작성 비용이 더 크면 전체 비용과 지연은 증가한다. [Anthropic tool caching](https://platform.claude.com/docs/en/agents-and-tools/tool-use/tool-use-with-prompt-caching), [OpenAI prompt caching](https://developers.openai.com/api/docs/guides/prompt-caching)

일반 배열 변경과 provider가 지원하는 지연 로딩은 구분해야 한다. Anthropic의 deferred tool은 발견 시 `tool_reference`로 대화 이력에 추가되고, OpenAI Tool Search도 정의를 컨텍스트 끝에 추가해 이전 prefix를 보존한다. 다만 Q의 현재 [Responses 요청 변환](../client/responses.go)은 typed `Tools`에서 function 외 타입을 거부하며 function의 일부 필드를 변환한다. 공식 기능이 있다는 사실만으로 Q의 provider·gateway 경로가 이를 이미 지원한다고 볼 수 없다. [Anthropic deferred tools](https://platform.claude.com/docs/en/agents-and-tools/tool-use/tool-use-with-prompt-caching), [OpenAI Tool Search](https://developers.openai.com/api/docs/guides/tools-tool-search)

**제안.** 기본값은 세션에서 안정적인 도구 정의와 순서를 유지하는 것이다. 역할·작업별 선별은 가능하면 세션 시작 때 하고, 작은 카탈로그는 eager 로딩을 유지한다. 큰 카탈로그에서만 schema token, 선택 빈도와 cache read/write를 먼저 측정한다. 이후 provider·gateway가 캐시를 보존하는 native tool search 또는 append-only 로딩을 지원하고 그 이력이 replay·복구되는 경우에 한해 선택적 로딩을 실험한다. 미지원 경로에서 일반 `tools` 배열을 매 round 바꾸는 방식으로 조용히 대체하지 않는다.

이때 discovery 결과와 실행 권한을 구분한다. 검색으로 도구를 발견해도 grant가 생기지 않는다. schema 버전, namespace, 실행 경로와 호출 허용 여부를 함께 관리하고, 압축·재개 뒤에도 필요한 정의를 복원한다. 검색 실패 시 허용된 카탈로그를 탐색할 수 있는 fallback이 필요하다.

Q에는 `loom_eval`이 있으므로 범용 코드 실행기를 먼저 추가할 필요는 없다. 기존 artifact를 선별·집계하는 예제와 receipt 안내를 개선하고, 도구 round-trip이 실제 병목으로 확인된 경우에만 권한 검사를 거치는 programmatic call 또는 독립 read 호출의 제한적 병렬 실행을 검토한다. 쓰기 순서, 출력 순서, 취소, replay 의미는 별도 계약이 필요하다.

**완료 기준.** 고정 카탈로그와 후보 구현을 같은 작업으로 비교하고, cold start와 cache가 유지된 긴 대화를 각각 평가한다. Schema token뿐 아니라 비캐시 입력, cache read/write, 검색 추가 호출, 전체 비용과 latency를 포함해야 한다. 작은 카탈로그의 지연을 불필요하게 늘리지 않고 도구 선택 성공률을 유지해야 하며, 권한 밖 호출·정의 변경 후 stale 호출·압축 후 재로딩을 확인한다. 캐시 손실을 포함한 이득이 확인되기 전에는 기본 동작으로 채택하지 않는다.

### G. 저비용 라우팅과 위임은 품질이 유지되는 범위에서 확대한다 — P2

**현재의 가장 강한 Q 내부 근거는 최근 검색 실험이다.** 해당 실험에서 OEV+Luna→Sol 경로는 Sol 호출을 32.5% 줄였지만 gold evidence recall은 73.4%에서 56.4%로 낮아졌다. JEV 비교도 63.8%로 baseline에 못 미쳤다. 최초 OEV 비교는 모든 과제가 큰 모델로 전환됐다. 이는 제한된 retrieval 실험의 결과이며 코드 수정 전체의 성공률이나 일반적인 모델 능력 평가가 아니다. [실험 결과와 한계](retrieval-routing-experiment.md)

따라서 다음 순서가 타당하다.

1. 이미 발견한 후보의 제한된 본문 확장을 추가하는 실험부터 한다. 후보를 읽을 수 없는 도구 계약의 제약과 routing model의 판단을 분리한다.
2. query 생성, scope 선택, 종료 판단을 한 번에 바꾸지 않고 각각 비교한다.
3. 정답 위치를 판단하는 retrieval과 실제 수정·검증까지 수행하는 end-to-end 평가를 분리한다.
4. 동일 모델의 단일 에이전트, 현재 위임 구조, 필요할 때만 evaluator를 부르는 구조를 같은 예산으로 비교한다. senior/junior 역할 수를 늘리는 것을 자체 목표로 삼지 않는다.

**채택 기준.** 비용 절감은 큰 모델 호출 수뿐 아니라 모든 모델의 token, 실패·재시도, latency를 포함해 계산한다. 품질 허용 차이를 실험 전에 정하고 반복 평가한다. 동등한 품질을 입증하지 못하면 저비용 경로는 선택적 실험 기능으로 둔다. 현재 자료만으로 저비용 라우팅을 기본값으로 바꿀 근거는 없다.

### H. 현재 계약과 과거 설계의 구분을 정리한다 — P2

**구체적 사례.** README는 `/plan`과 이전 승인 workflow의 제거를 설명한다. 반면 [agent-invocation-runtime.md](agent-invocation-runtime.md)는 `/plan`의 executor 노출을 설명하고, 필수 검증에는 제거된 `/agent:*` 표현도 남아 있다. 기능이 퇴행했다는 뜻은 아니지만, 문서를 읽는 에이전트가 구현할 대상을 잘못 이해할 수 있다. [현재 사용자 진입점](../README.md)

**제안.** 구현 계약을 소유하는 문서와 완료된 설계 기록을 지정한다. 과거 설계에는 기준 revision과 대체 문서를 연결하고, 사용법 문서에는 현행 명령만 남긴다. 문서의 문장을 고정하는 테스트 대신 링크·경로와 명령 catalog의 일치 여부를 검사한다.

**완료 기준.** 현재 사용자 workflow로 설명된 명령은 실제 catalog에서 찾을 수 있고, 폐기된 workflow는 역사적 설명임을 바로 알 수 있어야 한다. 이 보고서는 드리프트를 지적하며 해당 문서들을 함께 수정하지는 않는다.

## 6. 권장 구현 순서

| 단계 | 구체적 산출물 | 다음 단계로 넘어갈 근거 |
|---|---|---|
| 1. 기준선 확보 | A의 최소 trace 연결, 고정 과제·manifest·grader, H의 현재 문서 정리 | 같은 작업을 같은 설정으로 재평가 가능 |
| 2. 완료·중단 계약 | B의 verification receipt와 C의 공통 budget | 미검증/실패/unknown 구분, 재시작 후 budget 보존 |
| 3. 장기 실행 효율 | D의 부분 요약 후보, 지원 경로에 한정한 F의 캐시 보존 로딩 실험 | 품질 유지와 cache read/write를 포함한 비용·latency 변화 확인 |
| 4. 실행 환경 확장 | E의 capability 계약과 필요한 executor 한 종류 | 실제 격리 검사 통과, 지원 범위 문서화 |
| 5. 최적화 확대 | G의 routing·delegation 비교 | 고정된 품질 기준을 만족한 비용 개선 |

비신뢰 저장소 실행 또는 외부 사용자의 원격 접속이 가까운 목표라면 4단계를 앞당긴다. 반대로 신뢰된 개인 workspace 도구로 유지한다면 executor 확장을 급히 일반화할 필요는 없다.

첫 변경 묶음은 **식별자 연결 → 소수 과제의 반복 평가 → 완료 evidence 연결**로 작게 잡는 것이 좋다. 평가 프레임워크, 모든 OS sandbox, 전체 memory migration을 한 번에 도입하지 않는다.

## 7. 개선을 판단할 지표

다음은 새 평가에서 수집할 지표이며, 현재 Q의 수치가 아니다.

| 지표 | 계산·해석 |
|---|---|
| 작업 성공률 | 최종 상태를 grader로 확인한 성공 trial / 전체 trial. 실행 오류도 분모에 포함 |
| 잘못된 성공 선언율 | `succeeded`였으나 완료 조건 검증이 실패한 trial / 성공 선언 trial |
| 성공당 token 비용 | 실패·재시도를 포함한 전체 사용량 / 검증된 성공 수. 성공 0이면 계산 불가 |
| p50/p95 latency | 모델·도구·압축·위임 대기의 분해와 함께 보고. 실패·timeout을 별도 표시 |
| 제약 보존 | 압축·복구 후 사용자 정정을 포함한 명시 조건의 위반 여부 |
| 재탐색 | 같은 revision에서 새 evidence 없이 반복된 검색·읽기의 비율 |
| 복구 무결성 | 중복 부작용 수, unknown의 임의 성공 처리 수, 잘못된 merge 수 |
| cache 효율 | provider가 보고한 cached/input tokens. 미보고와 0을 구분 |
| 개입 부담 | 질문·권한 요청·수동 복구 수. 필요한 개입과 불필요한 중단을 구분 |

초기 목표치를 근거 없이 “성공률 95%”, “비용 50% 절감”으로 정하지 않는다. 먼저 기준선을 만들고, 변경별 기대 효과와 허용 가능한 품질 차이를 사전에 정한다. 결정적 무결성 테스트와 변동성이 있는 모델 품질 평가의 합격 기준은 구분한다.

## 8. 유지할 설계와 지금 보류할 확장

- **Loom과 원본 이력 보존:** 압축된 모델 입력과 실제 증거를 분리하는 기반이다. 요약으로 원본을 대체하지 않는다.
- **`unknown`의 보수적인 복구:** 부작용 가능 호출을 무조건 재실행하지 않는다. 후속으로 도구별 idempotency/reconciliation을 추가하더라도 확인 가능한 도구부터 적용한다.
- **commit이 고정된 change request:** base/head 검사와 부모 변경 거부를 유지한다. 자동 merge 확대보다 검증 evidence 연결이 먼저다.
- **host 소유의 임베딩 계약:** 새로운 정책·추적은 선택적 interface/decorator로 시작한다. loop 생성이 서비스나 sandbox를 자동 시작하게 만들지 않는다.
- **단일 배포 단위:** 위 개선을 위해 별도 분산 workflow 엔진이나 서비스 분할이 곧바로 필요한 것은 아니다.
- **실험 가능한 보조 규칙:** 무제한 반복, 상시 다중 에이전트 검토, 자동 prompt self-modification은 초기 기본값으로 넣지 않는다. 모델별 효과를 평가한 뒤 채택한다.

Q의 차별화 방향에 대한 제안은 **로컬 workspace에서 실행 근거를 추적할 수 있고, 중단 후 복구 가능하며, 여러 provider와 host에서 사용할 수 있는 Go 하네스**다. 이는 시장 점유율이나 경쟁 우위를 측정한 결론이 아니라 현재 구현 자산에 근거한 제품 방향 제안이다.

## 9. 이번 조사에서 수행한 검증과 한계

- 조사 시작 시 `git status --short`는 비어 있었다. 이 작업은 보고서만 추가하며 제품 코드를 변경하지 않는다.
- Windows arm64, Go `1.27.1`에서 다음 명령을 실행했고 세 패키지 모두 통과했다. 네트워크 다운로드를 막기 위해 해당 실행 프로세스에 `GOPROXY=off`, `GOSUMDB=off`, `GOTOOLCHAIN=local`을 설정했다.

  ```powershell
  go test ./agentloop ./memory ./subagent -count=1 -timeout=90s
  ```

- 위 결과는 해당 패키지의 테스트 통과를 뜻한다. 전체 `task test`, Studio 브라우저 suite, 실제 provider/ACP 상호운용, sandbox 보안 검사, 장시간 모델 품질 평가를 이번에 실행했다는 뜻은 아니다.
- 현행 코드와 기존 문서를 대조한 정적 분석이다. 제안의 성능 효과는 A의 평가 기반에서 별도로 검증해야 한다.
- 외부 자료의 게시일과 링크를 본문에 함께 남겼다. 저장소의 실행 계약이나 공급자 기능이 바뀌면 이 보고서의 비교를 갱신하되, 실제 현재 동작의 권위는 코드와 해당 기능의 소유 문서에 둔다.
