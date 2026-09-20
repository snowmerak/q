# Agent Skill Store 분리 계획

상태: 구현 완료

## 목적

외부 Go 애플리케이션이 전체 Workspace Archive나 Q Library 없이도 Q의
Agent Skill 검색, 자동 힌트, 본문 로드를 사용할 수 있게 한다. Skill
인덱스에 필요한 최소 저장소 계약만 `tools.Runtime`에 주입하며, 기존 Agent
Loop와 Skill 동기화 구현은 복제하지 않는다.

## 현재 결합

- `agentskills.RecordStore`는 이미 `Search`, `Save`, `Delete`만 요구한다.
- `tools.Runtime`은 이 store를 archive의 동적 타입 변환으로만 얻는다.
- `Runtime.SearchSkillHints`와 MCP `search_skills`는 Skill store가 아니라
  `builtin.Archive`를 사용한다.
- 따라서 `tools.NewRuntime`은 검색 도구를 광고하지만 실제 검색과 자동
  힌트는 archive가 없어 실패한다.

## 공개 계약

`tools` 패키지는 기존 `agentskills.RecordStore`를 다음 이름으로 공개한다.

```go
type SkillStore = agentskills.RecordStore

func NewRuntimeWithSkillStore(
    ctx context.Context,
    root string,
    store SkillStore,
) (*Runtime, error)
```

호출자가 store의 lifetime을 소유한다. `Runtime.Close`는 MCP/Loom/LSP
리소스만 닫고 주입된 Skill store는 닫지 않는다.

Skill store의 필수 메서드는 다음 세 개다.

- `Search`: `search_skills`와 host-side 자동 힌트
- `Save`: 발견한 Skill 메타데이터의 최초/변경 투영
- `Delete`: 삭제되거나 shadowing이 바뀐 투영 정리

선택적인 기존 `Prepare` 메서드는 store가 구현할 때만 임베딩 준비에
사용한다. Skill 본문은 계속 `agentskills.Registry`에서 직접 읽으므로
store에 `Get`은 필요하지 않다.

## 내부 변경

1. `agentskills.SearchStore`를 read-only 최소 계약으로 추가하고 기존
   `RecordStore`가 이를 포함하게 한다.
2. builtin Skill 검색이 `Archive` 대신 `SearchStore`를 받게 한다.
3. builtin MCP dependencies에 `SkillStore`를 별도로 전달한다.
4. `tools.Runtime`의 `skillArchive` 결합을 제거하고 `skillStore`만으로
   자동 힌트와 refresh를 수행한다.
5. 기존 archive 기반 생성자는 archive가 `SkillStore`도 구현하면 이전과
   같은 객체를 두 역할에 전달하여 호환성을 유지한다.
6. store나 Library가 없을 때는 동작하지 않는 Skill 도구를 광고하지
   않는다. Library만 있을 때는 global Skill 검색을 허용한다.

## 불변조건

- Agent Loop, Skill hint orchestration, Registry 동기화는 각각 하나만
  유지한다.
- Archive는 대화 기록 검색용이고 Skill store는 Skill 인덱스용이다.
- Q Library가 없으면 Registry가 발견한 global/workspace 로컬 Skill을 모두
  주입된 Skill store에 투영한다.
- Q Library가 있으면 workspace Skill만 로컬 store에 투영하고 global
  Skill은 Library로 라우팅한다.
- `get_skill` 본문은 Loom artifact로 바꾸지 않는다.

## 검증

- 외부 `tools_test` 패키지의 최소 in-memory store로 런타임을 생성한다.
- archive 도구 없이 `search_skills`, `get_skill`, `SearchSkillHints`가
  동작하는지 확인한다.
- 기존 archive+Library 병합, refresh, embedding 준비 테스트를 유지한다.
- store와 Library가 없는 런타임은 Skill 도구를 광고하지 않는지 확인한다.
- `gofmt`, focused tests, `go test ./...`, `go vet ./...`,
  `go run ./scripts/modulecheck`를 통과한다.

## 제외 범위

- 새 Agent Loop 또는 Skill 검색 구현
- Session Store 포맷 변경
- 새로운 인메모리/디스크 Skill 인덱스 구현 제공
- Q Library 및 proposition 기능의 임베딩 앱 자동 시작

## 구현 결과

- `agentskills.SearchStore`와 공개 별칭 `tools.SkillStore`를 추가했다.
- `tools.NewRuntimeWithSkillStore`가 Archive와 독립적으로 Skill 메타데이터를
  동기화하고 `search_skills`, `get_skill`, `SearchSkillHints`를 활성화한다.
- 기존 Archive 생성자는 전달된 객체가 `SkillStore`도 구현할 때 같은
  객체를 재사용하므로 기존 Session Store/embedding 경로를 유지한다.
- store와 Library가 모두 없는 기본 런타임은 실패할 Skill 도구를 더 이상
  광고하지 않는다. Library만 있는 검색은 global 결과와 workspace store
  부재 warning을 함께 반환한다.
- 외부 `tools_test` 패키지에서 세 메서드만 구현한 in-memory store로 위
  계약과 Archive 도구 비노출을 검증했다.

집중 테스트, `go vet ./...`, `go run ./scripts/modulecheck`는 통과했다.
`go test ./...`의 변경 관련 패키지는 모두 통과했으며, 현재 개발 머신에서
별도로 실행 중인 `q.exe`가 `127.0.0.1:17891`과 `:17892`를 점유하여
`cmd/q`의 고정 포트 서비스 테스트만 전체 실행 시 실패했다. 해당 실패
테스트는 포트 간섭이 없을 때 단독 실행으로 통과함을 확인했다.
