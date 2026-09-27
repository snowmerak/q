# 수동 `/compact` 안정성 리뷰

2026-09-27 기준. 대상은 TUI와 ACP의 수동 `/compact` 명령, 공용 체크포인트 계산, 세션 저장 경계다. 실제 모델 제공자와 외부 ACP 클라이언트를 연결한 종단 간 검증은 이 리뷰에 포함하지 않는다.

## 동작 및 상태 경계

| 항목 | 검토 결과 |
|---|---|
| 명령 발견·실행 | TUI의 slash catalog, 도움말, 자동완성과 ACP의 AvailableCommands, `/help`, 명령 실행 경로에 등록되어 있다. 진행 중인 TUI turn은 입력을 받지 않고 ACP는 세션별 prompt 잠금으로 직렬화한다. |
| 수동 실행 | `memory.Manager.Plan`을 직접 호출하므로 자동 압축 임계치 이전에도 실행된다. 압축할 이전 대화가 없으면 모델을 호출하지 않고 안내한다. 명령 문구는 transcript에 추가되지 않는다. |
| 체크포인트 | 기존 정규화 및 보존 규칙을 사용한다. 모델 응답은 별도 `memory.Manager` 사본에 적용하며, 대화 원문은 그대로 둔다. |
| 세션 저장 | 압축된 사본과 초기화된 `conversationID`로 세션을 먼저 저장한다. 저장이 성공한 뒤에만 활성 context와 `conversationID`를 교체한다. 저장 실패 시 기존 context, 보정 통계, 압축 횟수, provider 대화 ID를 유지한다. |
| 완료 | 수동 TUI 명령은 후속 채팅 요청을 보내지 않고 압축 목표 토큰 값을 지운다. 자동 압축은 보류 중인 요청의 응답까지 목표값을 유지한다. ACP는 명령 응답과 사용량 갱신을 보낸다. |
| 취소·오류 | TUI는 turn ID가 달라진 늦은 결과를 무시한다. ACP는 모델 호출 뒤와 저장 직전에 취소를 확인한다. 체크포인트 생성·검증·세션 저장 오류에서는 활성 context를 바꾸지 않는다. |
| archive | 세션 저장과 활성 상태 반영 뒤 기록한다. archive 기록 실패는 이미 완료된 압축을 되돌리지 않으며, 명령 성공과 함께 경고로 표시한다. |

주요 구현은 `app/chat_controller.go`, `app/chat_events.go`, `app/acp.go`, `memory/manager.go`, `workspace/session.go`에 있다. 세션 저장은 임시 파일을 만든 뒤 교체하는 방식이다.

## 확인한 회귀 시나리오

`app/compact_command_test.go`와 `memory/manager_test.go`의 테스트로 다음을 확인했다.

- TUI 및 ACP가 자동 임계치 전에 `/compact`를 실행하며, transcript를 유지하고 체크포인트를 저장한다.
- 빈 대화는 모델 호출 없이 안내하고, 모델 오류는 context를 유지한다.
- TUI에서 취소된 결과는 무시한다. ACP에서 취소를 무시한 모델 응답도 적용하지 않는다.
- TUI 및 ACP의 세션 저장 실패는 활성 context와 provider 대화 ID를 유지한다.
- 수동 TUI 완료 뒤 압축 목표 토큰 값이 남지 않는다.
- `CheckpointCopy`는 원본의 메시지와 압축 통계를 바꾸지 않고 보정된 provider overhead를 사본에 유지한다.

확인 명령: `go test ./app ./memory -run 'Test(TUICompactCommand|ACPCompactCommand|CheckpointCopy)' -count=1`, `go test -p 1 ./...`, `go vet ./...`, `go build ./...`.

## 남은 검증 경계

- 테스트의 모델과 ACP 연결은 가짜 구현이다. 실제 제공자의 체크포인트 JSON 품질, 토큰 사용량, 외부 ACP 클라이언트 화면 표시와 취소 타이밍은 별도 통합 검증이 필요하다.
- 취소가 세션 파일 교체 중에 발생하면 저장이 완료될 수 있다. 저장 뒤 ACP 알림 전달이 실패해도 압축된 세션은 유지된다. 이때 재연결한 클라이언트는 저장된 세션 상태를 읽어야 한다.
- 압축 목표 토큰 수는 추정치다. 요약 결과가 목표를 넘더라도 checkpoint를 거부하지 않는 기존 정책을 따른다.
- `go mod tidy -diff`는 이 변경과 무관하게 `go.sum`의 이전 `llm-provider` 버전 체크섬 두 줄을 제거하라고 보고한다. 이번 수정에는 모듈 파일을 변경하지 않았다.
- `golangci-lint 2.14.0 run --no-config ./...`는 변경 파일 밖의 기존 6건을 보고했다: `workspace/delegation.go`의 `errcheck` 4건, `app/session_restart_test.go`와 `subagent/delegation.go`의 `staticcheck` 각 1건. `/compact` 변경 파일에서 보고된 항목은 없다.
- `govulncheck` 실행 파일과 `go.mod`의 도구 등록이 없어 취약점 스캔은 실행하지 못했다.
- 기본 병렬 `go test ./...`는 `cmd/q`의 `TestRunGatewayWithStoreServesConfiguredProviders`가 취소 뒤 5초 안에 종료되지 않아 한 차례 실패했다. 해당 테스트를 단독으로 3회 실행한 결과와 `go test -p 1 ./...`는 통과했다. 이 테스트의 시간 민감성은 `/compact` 변경 범위 밖이다.
