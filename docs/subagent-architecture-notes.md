# Role-based subagent architecture notes

## 상태

이 문서는 역할별 모델 할당과 서브에이전트 orchestration을 위한 아이디어
초안이다. 실제 인터페이스와 구현은 추후 통합할 라이브러리를 확인한 뒤
확정한다. 현재 코드에는 이 설계를 구현하지 않는다.

## 목표

하나의 채팅 모델이 조사, 계획, 구현, 검토를 모두 수행하는 대신 작업 목적이
명확한 서브에이전트를 구성한다. 각 역할에는 독립적인 모델, reasoning effort,
도구 권한, context와 실행 예산을 할당할 수 있어야 한다.

초기 역할은 다음 여섯 가지다.

| 역할 | 책임 | 기대 출력 |
|---|---|---|
| `griller` | 요청의 모호함, 숨은 제약, 실패 조건과 잘못된 가정을 집요하게 확인 | 질문, 위험 목록, 명시적 가정 |
| `scout` | 저장소 구조, 관련 코드, 기존 패턴과 변경 범위를 빠르게 탐색 | 관련 파일, 심볼, 영향 범위 |
| `research` | 공식 문서, API, 라이브러리와 외부 사실을 조사 | 근거와 출처가 포함된 조사 결과 |
| `planner` | 조사 결과를 실행 가능한 단계와 완료 조건으로 변환 | 의존성이 표시된 구현 계획 |
| `coder` | 승인된 계획에 따라 코드를 수정하고 적절히 검증 | 패치, 테스트 결과, 변경 설명 |
| `reviewer` | 구현의 정확성, 회귀, 보안, 누락된 테스트를 독립적으로 검토 | 중요도별 지적과 승인/재작업 판단 |

## 기본 orchestration 흐름

```text
사용자 요청
   │
   ├─ griller ── 모호함·위험·가정 확인
   │
   ├─ scout ───── 저장소 조사 ─┐
   └─ research ── 외부 조사 ──┤  필요할 때 병렬 실행
                              ▼
                           planner
                              │
                              ▼
                            coder
                              │
                              ▼
                           reviewer
                              │
                  승인 ───────┴────── 재작업
                   │                    │
                   ▼                    └─ coder로 제한적 피드백
                 완료
```

모든 요청이 전체 파이프라인을 거칠 필요는 없다. 예를 들어 단순한 로컬 수정은
`scout → coder → reviewer`만 사용할 수 있고, 외부 사실이 필요하지 않으면
`research`를 생략한다. root/orchestrator가 요청 특성에 따라 필요한 역할만
선택한다.

## 역할별 초기 성향

아래 값은 모델명이 아니라 모델을 고를 때의 기준이다.

| 역할 | 속도/비용 | reasoning 기본값 후보 | context 성향 | 도구 권한 |
|---|---|---|---|---|
| `griller` | 중간 | `high` | 사용자 요청과 기존 결정 중심 | 읽기 전용, 질문 생성 |
| `scout` | 빠름 | `low` 또는 `medium` | 저장소 탐색 결과 중심 | 파일/검색 읽기 전용 |
| `research` | 중간 | `medium` 또는 `high` | 큰 context와 출처 보존 | 웹/문서 읽기 전용 |
| `planner` | 중간 | `high` | scout/research 결과 중심 | 원칙적으로 쓰기 금지 |
| `coder` | 품질 우선 | `high` | 계획, 관련 코드, 테스트 중심 | 파일 수정과 로컬 명령 허용 |
| `reviewer` | 품질 우선 | `high` | diff와 검증 결과 중심 | 읽기 및 비파괴 검증 |

정확한 모델과 effort는 `http://localhost:18181/v1/models`의 실제 capability와
요청 실험을 거쳐 결정한다. 모델 ID만으로 reasoning effort 지원 여부를
단정하지 않는다.

## 설정 구조 초안

```yaml
agents:
  max_parallel: 3
  roles:
    griller:
      model: ""
      reasoning_effort: high
      enabled: true
    scout:
      model: ""
      reasoning_effort: medium
      enabled: true
    research:
      model: ""
      reasoning_effort: high
      enabled: true
    planner:
      model: ""
      reasoning_effort: high
      enabled: true
    coder:
      model: ""
      reasoning_effort: high
      enabled: true
    reviewer:
      model: ""
      reasoning_effort: high
      enabled: true
```

모델을 비워두면 현재 채팅 모델 또는 역할별 기본 모델을 상속하는 방식을
고려한다. `reasoning_effort`가 provider에서 지원되지 않을 때 무시할지,
오류로 처리할지는 라이브러리의 capability 표현을 확인한 뒤 결정한다.

## 에이전트 실행 단위

각 서브에이전트 실행은 최소한 다음 값을 가진다.

```go
type AgentSpec struct {
    Role            string
    Model           string
    ReasoningEffort string
    Instructions    string
    ToolPolicy      ToolPolicy
    TokenBudget     int
}

type AgentTask struct {
    ID           string
    ParentID     string
    Objective    string
    Context      []Message
    Dependencies []string
}

type AgentResult struct {
    TaskID       string
    Status       string
    Summary      string
    Findings     []Finding
    Artifacts    []Artifact
    Usage        Usage
}
```

구체적인 타입은 통합 라이브러리의 task, message, tool, usage 타입을 우선해
재사용한다.

## context와 handoff

서브에이전트마다 독립 context를 사용한다. 전체 root 대화를 그대로 복제하지
않고 역할 수행에 필요한 최소 정보만 전달한다.

권장 handoff envelope는 다음 내용을 포함한다.

- 구체적인 objective와 완료 조건
- 관련 사용자 요청과 확정된 결정
- 필요한 파일, 심볼, 문서 위치
- 선행 에이전트의 구조화된 결과
- 허용된 도구와 변경 범위
- 금지 사항과 미해결 질문

각 서브에이전트도 기존 78% trigger, 22% target context compaction 정책을
독립적으로 적용할 수 있어야 한다. 다만 짧은 일회성 작업은 압축 없이 종료하는
것이 일반적이다.

## 공유 workspace 정책

서브에이전트가 같은 workspace를 사용할 경우 충돌 방지가 필요하다.

- `scout`, `research`, `planner`, `reviewer`는 기본적으로 읽기 전용이다.
- 동시에 파일을 수정할 수 있는 `coder`는 기본 한 개로 제한한다.
- 여러 coder를 허용하려면 파일 ownership 또는 별도 worktree 전략이 필요하다.
- 사용자 변경과 다른 에이전트의 변경을 임의로 되돌리지 않는다.
- destructive command는 root/orchestrator의 정책을 그대로 따른다.
- reviewer는 coder의 설명이 아니라 실제 diff와 테스트 결과를 확인한다.

## tool 권한

현재 MCP 및 builtin file tools는 역할별 allowlist를 둘 수 있어야 한다.

```text
griller  : 대화 context 읽기
scout    : list/read/search
research : web/docs read
planner  : 결과 읽기, plan 작성
coder    : read/search/edit/write/test
reviewer : read/search/diff/test
```

서브에이전트가 다른 서브에이전트를 무제한 생성하는 recursive spawn은 초기
버전에서는 허용하지 않는다. spawn 권한은 root/orchestrator에만 두고 최대
깊이와 동시 실행 수를 명시한다.

## 결과 병합 원칙

root/orchestrator는 결과를 단순 연결하지 않고 다음 규칙으로 병합한다.

1. 사실과 추론을 구분한다.
2. 서로 충돌하는 결과는 숨기지 않고 근거와 함께 표시한다.
3. planner는 scout/research 결과를 입력으로 받되 원본 결과를 참조할 수 있다.
4. coder에게는 승인된 계획과 변경 범위만 전달한다.
5. reviewer 지적은 중요도와 재현 근거가 있어야 한다.
6. 완료 판단과 최종 사용자 응답은 root/orchestrator가 담당한다.

## TUI 아이디어

메인 transcript를 서브에이전트 로그로 채우지 않고 요약된 상태만 표시한다.

```text
agents · scout ✓ · research … · planner waiting
reviewer: 2 findings · coder retry 1/2
```

상세 실행 로그는 별도 panel 또는 `/agents` 명령으로 여는 방식을 고려한다.
취소는 전체 작업과 개별 agent 모두 지원해야 한다.

## 통합 라이브러리 확인 후 결정할 사항

- 라이브러리가 제공하는 agent/task lifecycle과 cancellation 방식
- subagent 생성이 callback, runner, graph 중 어떤 추상화인지
- 메시지와 tool call/result 타입을 그대로 재사용할 수 있는지
- 모델별 `reasoning_effort` 전달 필드와 허용 값
- usage, context window와 hidden reasoning token의 보고 방식
- streaming event와 에이전트 상태 이벤트 구조
- 병렬 실행 제한과 공유 client의 concurrency 안전성
- retry, timeout, budget과 오류 전파 정책
- 결과용 structured output/schema 지원 여부
- checkpoint 또는 session persistence 지원 여부

## 초기 구현 단계 후보

라이브러리를 받은 뒤 다음 순서로 다시 검토한다.

1. 라이브러리 타입을 현재 `client`, `memory`, MCP 도구 구조와 대응시킨다.
2. 단일 `scout` 서브에이전트의 생성, 실행, 취소, 결과 반환을 연결한다.
3. 역할별 config와 모델/reasoning effort 선택을 추가한다.
4. 읽기 전용 역할의 병렬 실행과 결과 handoff를 구현한다.
5. 단일 writer `coder`와 독립 `reviewer` 반복을 구현한다.
6. TUI 상태와 사용량 표시를 추가한다.
7. 충돌, 취소, timeout, context compaction 통합 테스트를 작성한다.
