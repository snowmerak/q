# 기존 Agent Loop 공개 API 계획

상태: 구현 완료

후속 변경: 이 문서는 최초 `app` 공개 API 작업의 기록이다. 2026-09-25의
[구조 리팩터링 로드맵](refactoring-roadmap.md) M4에서 실행 본문을 단일
`agentloop` 패키지로 이동했고, `app` API는 호환 facade로 유지한다. 현재
임베딩 방법은 [Agent Loop 가이드](agent-loop-embedding.md)를 따른다.

## 목적

외부 Go 애플리케이션이 Q의 실제 Agent Loop를 워크스페이스 단위로
호출할 수 있게 한다. 외부 호스트는 모델, 워크스페이스, 이벤트 소비와
선택적인 세션 저장을 소유하고, Q는 현재 TUI와 ACP에서 사용하는 모델/툴
반복 실행, orchestration, Agent Skill 힌트, 동적 워크스페이스 지침과
컨텍스트 압축을 그대로 수행한다.

이 변경의 최우선 불변조건은 **Agent Loop 구현을 하나만 유지하는 것**이다.
별도 `agent` 또는 `agentloop` 패키지에 기존 코드를 복사하거나, 두 번째
워크스페이스 상태 머신을 만들지 않는다.

## 현재 기준선

- 실제 반복 실행은 `app/model.go`의 private `streamAgentLoop`에 있다.
- TUI, ACP, 내장 Search/Web Tester 경로가 같은 private 이벤트 타입을 쓴다.
- 압축은 `app/agent_context.go`, orchestration은 `app/orchestration.go`, 자동
  Skill 힌트는 `app/skill_hints.go`의 현재 구현이 권위다.
- 워크스페이스 초기 지침은 `model.appendRuntimeMessages`에서 구성한다.
- `client.Client`, `tools.Runtime`, `workspace.Store`는 이미 공개되어 있다.

## 결정

### 구현 위치

Agent Loop는 `app` 패키지에 그대로 둔다. 기존 함수 본문을
`RunAgentLoop`로 공개하고 TUI와 ACP도 이 공개 함수만 호출한다. 패키지
분리는 이번 범위에 포함하지 않는다.

### 공개 계약

```go
type AgentLoopRequest struct {
	Client               ChatClient
	Tools                AgentToolRuntime
	Model                 string
	ReasoningEffort       string
	Messages              []client.Message
	ConversationID        string
	WorkingDirectory      string
	ActiveTask            *workspace.ActiveTask
	Stream                bool
	CoalesceInstructions  bool
	ContextPolicy         memory.Policy
}

func RunAgentLoop(
	ctx context.Context,
	request AgentLoopRequest,
	events chan<- AgentEvent,
)
```

`RunAgentLoop`는 동기 함수이며 호출자가 보통 goroutine에서 실행한다. 함수가
반환되기 전에 `events`를 닫는다. 입력 메시지 슬라이스는 복사하며, 주입된
클라이언트나 툴 런타임은 닫지 않는다. 취소와 deadline은 `ctx`가 소유한다.

`AgentEvent`는 현재 내부 이벤트 그 자체다. 별도 이벤트 처리 파이프라인을
만들지 않고, 외부 호출자는 공개 accessor로 다음 값을 읽는다.

- 상태 문자열
- assistant/tool 메시지와 툴 오류 여부
- 툴 호출
- 스트리밍 thinking/response delta
- `ask_to_user` 질문과 답변 채널
- context replacement와 compaction
- task 시작/완료
- 최종 응답, outcome, 요청 토큰 추정치와 툴 호출 수
- 오류

질문을 처리할 수 없는 호스트는 `ErrInteractionUnavailable`을 담은 답변을
보낸다. 그러면 현재 동작처럼 모델이 사용할 수 있는 툴 오류로 변환된다.

### 워크스페이스 초기 메시지

현재 `model.appendRuntimeMessages` 본문을 `PrepareWorkspaceMessages`로
리팩터링한다. 복제본은 두지 않는다.

```go
type WorkspaceMessageOptions struct {
	Root             string
	Tools            AgentToolRuntime
	ArchiveAvailable bool
}

func PrepareWorkspaceMessages(
	messages []client.Message,
	options WorkspaceMessageOptions,
) []client.Message
```

이 함수는 입력 슬라이스를 변경하지 않고 다음 메시지를 추가한다.

- 워크스페이스 루트 `AGENTS.md`
- OS, architecture, shell, root와 `.qignore` 안내
- 실제 툴 목록에 따른 Agent Skills와 Library proposition 안내
- archive가 있을 때만 archive 검색 안내
- Q의 `task_start`, `ask_to_user`, `task_complete` 계약

TUI도 같은 함수를 사용한다.

### 역할별 툴

현재 private `scopeTools`를 `ScopeTools`로 공개한다. 반환값은 광고된 role
카탈로그에 포함된 툴만 호출할 수 있으며, 새 role runtime 구현은 만들지
않는다.

## 최소 실행 구성과 선택 기능

워크스페이스 임베딩의 필수 입력은 root, `ChatClient`, `AgentToolRuntime`,
model ID다. 일반적인 최소 런타임은 `tools.NewRuntime(ctx, root)`로 만든다.

- 스트리밍 클라이언트가 없으면 일반 Chat으로 폴백한다.
- Skill 검색 capability가 없으면 자동 힌트만 생략한다.
- archive, Library, LSP와 외부 MCP는 자동으로 시작하지 않는다.
- context window가 0이면 현재처럼 압축하지 않는다.
- 세션 저장은 자동 수행하지 않는다. 필요하면 기존 `workspace.Session`과
  `workspace.Store`를 사용한다.
- Gateway, 전역 config, provider manager, TUI, ACP, Thinker/Learning과 plan
  automation은 공개 Agent Loop의 생성 책임에 포함하지 않는다.

## 구현 작업

1. `ChatClient`와 `AgentToolRuntime`을 현재 private 인터페이스의 공개
   선언으로 바꾸고 내부 이름은 alias로 유지한다.
2. `AgentLoopRequest`와 `AgentEvent` accessor를 추가한다.
3. 현재 `streamAgentLoop`를 `RunAgentLoop`로 바꾸고 모든 내부 호출자를
   새 함수로 전환한다.
4. 질문, 답변, stream delta, compaction과 context replacement 중 외부
   이벤트에 필요한 현재 타입만 공개 이름으로 승격한다.
5. `appendRuntimeMessages` 구현을 `PrepareWorkspaceMessages` 하나로
   리팩터링한다.
6. `scopeTools`를 `ScopeTools`로 공개하고 기존 호출자가 사용하게 한다.
7. 외부 테스트 패키지에서 공개 API만 사용해 실제 툴 라운드를 실행한다.
8. README에 최소 임베딩 예제와 이 문서 링크를 추가한다.

## 제외 범위

- 새 `agent`/`agentloop` 패키지
- 기존 루프, 압축, orchestration 또는 Skill 힌트의 복사
- 별도 `Workspace`나 session 상태 머신
- 자동 Gateway/config/Library/workspace-memory 시작
- Session Store 포맷 또는 마이그레이션 변경
- 서브에이전트 delegation과 plan executor의 공개 구성 API

## 검증과 완료 조건

- 외부 `app_test` 패키지가 `RunAgentLoop`로 직접 응답과 실제 툴 호출을
  완료한다.
- 외부 테스트가 `PrepareWorkspaceMessages`로 루트 `AGENTS.md`와 Q
  워크스페이스 지침을 받는다.
- TUI와 ACP를 포함한 모든 기존 호출자가 `RunAgentLoop`를 사용한다.
- 저장소의 Agent Loop 라운드 구현은 하나뿐이다.
- 기존 압축, orchestration, Skill 힌트 구현은 이동하거나 복제하지 않는다.
- `gofmt`, focused tests, `go test ./...`, `go vet ./...`와
  `go run ./scripts/modulecheck`가 통과한다.

## 구현 기록

계획대로 새 패키지나 두 번째 루프를 만들지 않고 기존 구현을 공개했다.

- `app.RunAgentLoop`가 기존 반복 실행 본문이며 TUI, ACP, 내장 Search/Web
  Tester도 이 함수만 호출한다.
- `app.AgentLoopRequest`, `app.AgentEvent`와 이벤트 payload/accessor,
  `app.ChatClient`, `app.AgentToolRuntime`을 공개했다.
- `app.PrepareWorkspaceMessages`가 루트 `AGENTS.md`, 런타임 환경, 선택적인
  archive/Skill/proposition 안내와 orchestration 계약을 한 곳에서 만든다.
  TUI의 기존 초기화도 이 함수를 사용한다.
- `app.ScopeTools`가 기존 role 기반 툴 필터를 그대로 공개한다.
- `app_test` 외부 패키지 테스트가 공개 API만 사용해 워크스페이스 지침을
  준비하고 모델의 툴 호출을 런타임에 전달한 뒤 최종 응답을 받는다.

검증 결과:

- `gofmt` 완료
- `go test ./app` 통과
- `go test ./...` 통과
- `go vet ./...` 통과
- `go run ./scripts/modulecheck` 통과
- `git diff --check` 통과

남은 의도적인 제한은 다음과 같다.

- 툴을 포함한 Agent Loop 실행에는 `AgentToolRuntime`이 필요하다. 표준 최소
  구현은 `tools.NewRuntime(ctx, root)`다.
- 호출자는 반환될 때까지 이벤트를 소비해야 하며, 질문 이벤트에 답하고
  필요한 transcript/compaction/session 영속화를 직접 수행한다.
- 공개 API는 최소 변경을 위해 `app` 패키지에 있으므로 이 패키지의 기존
  TUI 관련 전이 의존성도 함께 빌드된다. 별도 경량 패키지로의 이동은 이번
  범위가 아니며, 이동 시에도 기존 루프를 복제하지 않아야 한다.
