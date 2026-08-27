# Q ACP SDK 포크: 구조·동기화·배포

기준일: 2026-08-27. 이전 Q 기준: `615d11f`.
SDK는 태그 대신 Go가 생성하는 커밋 기반 pseudo-version으로 고정한다.

## 결정과 모듈 경계

Q가 ACP 표준을 따라가는 SDK 포크를 직접 유지한다. 별도 GitHub 저장소를
만들지 않고 현재 저장소의 **독립 Go 모듈**로 관리한다. 공식 SDK나 모든 ACP
기능의 완전한 구현을 뜻하지 않는다. 초안 기능은 Unstable API와 capability
협상을 유지하고, 새 기능은 프로토콜 근거·스키마·테스트를 함께 추가한다.

| 용도 | 모듈 | 버전/관리 |
|---|---|---|
| Q | `github.com/snowmerak/q` | 루트 go.mod |
| SDK | `github.com/snowmerak/q/third_party/acp-go-sdk` | 루트 go.mod의 pseudo-version |
| 생성기 | `github.com/snowmerak/q/third_party/acp-go-sdk/cmd/generate` | 개발용 중첩 모듈 |

루트 go.mod는 SDK 포크를 require하며 **replace가 없다.** Q 호출부와 SDK 예제도
포크 경로를 import한다. go.work는 세 모듈을 로컬에서 연결하며 replace가 없다.
워크스페이스 빌드 자체에는 SDK 버전이 필요 없지만, Q의 원격 설치를 위해
루트 go.mod에는 **실제로 공개된 SDK 커밋의 pseudo-version**을 기록한다.

```go
use (
    .
    ./third_party/acp-go-sdk
    ./third_party/acp-go-sdk/cmd/generate
)
```

SDK 변경을 먼저 커밋·푸시한 뒤 Go가 해석한 버전과 checksum을 루트 go.mod와
go.sum에 반영한다. 별도 SDK_VERSION 파일이나 태그는 사용하지 않는다.
SDK의 Go 1.21 기준과 Q의 Go 1.26.5 요구는 별개다.

## 기존 설치 오류와 해결 범위

`2af648e`는 form elicitation 세션 지정을 위해 upstream v0.13.5 복사본을
수정하고 루트 go.mod에 아래 치환을 넣었다.

```go
replace github.com/coder/acp-go-sdk => ./third_party/acp-go-sdk
```

버전 지정 go install은 이 치환을 거부한다. 원본 SDK만 쓰면 app/acp.go와
app/acp_commit.go의 SessionId 필드가 없어 컴파일이 실패한다. 이전 격리
실험에서 두 실패를 모두 확인했다.

go.work만 추가하고 원래 모듈 이름을 유지하면 배포는 해결되지 않는다.
버전 설치는 workspace를 무시하며 중첩 모듈은 부모의 module zip에서 제외된다.
이번에는 **자체 모듈 경로와 배포 가능한 버전**도 함께 분리했다.
[Go 설치 규칙](https://go.dev/ref/mod#go-install),
[워크스페이스](https://go.dev/ref/mod#workspaces),
[하위 모듈 버전](https://go.dev/ref/mod#vcs-dir).

## 유지하는 프로토콜 차이

업스트림: coder/acp-go-sdk v0.13.5,
커밋 `0845a3bb9eddda5bfc22a94dd3598c90cb842451`.
SDK의 UPSTREAM.json에 출처와 프로토콜 근거를 기록한다.

기존 수작업 패치는 UnstableCreateElicitationForm.SessionId 한 필드였다.
이제 schema/q-overrides.json에서 누락된 scope를 보충하고 생성기에 적용한다.
기준은 [ACP elicitation RFD](https://github.com/agentclientprotocol/agent-client-protocol/blob/main/docs/rfds/elicitation.mdx)이며,
정식 안정 규격으로 승격됐다고 가정하지 않는다.

- form과 URL 요청 모두 sessionId 또는 requestId로 scope를 지정한다.
- 세션 범위에는 선택적으로 toolCallId가 붙는다.
- RequestId는 optional union pointer라서 없으면 JSON에서 빠지고 숫자 0과
  문자열 ID는 보존된다.
- Q는 기존처럼 세션 범위 form과 capability 협상을 사용한다. URL elicitation
  지원이나 새 권한 정책을 광고하지 않는다.
- scope의 상호 배타성은 호출자 계약이다. SDK의 기존 Validate는 완전한
  JSON Schema 검증기가 아니다.
- 기존 CancelNotification.SessionId의 근거 불명 omitempty 변경은 제거했다.
  원본 스키마대로 필수 필드를 생성한다.

원본 스키마 파일은 그대로 두고 오버레이 적용 → stable/unstable 병합 → Go 생성
순으로 처리한다. 타깃이 사라지거나 동일 필드가 upstream에서 호환되지 않게
바뀌면 생성 실패로 검토를 요구한다. 모든 의미적 규격 변경을 탐지하지는 않는다.

생성기는 같은 upstream 커밋에서 복원했다. 이전 module zip에는 별도 모듈인
cmd/generate가 없어서 생성 코드만 수작업으로 수정된 상태였다.
Apache-2.0 LICENSE와 출처를 유지한다.

## 개발·검증

저장소 루트에서:

```sh
task acp:generate
task acp:check
task test
task dist:check
```

Task 없이 실행하려면:

```sh
go -C third_party/acp-go-sdk/cmd/generate run .
go -C third_party/acp-go-sdk/cmd/generate run . -check
go -C third_party/acp-go-sdk/cmd/generate test ./...
go -C third_party/acp-go-sdk test ./...
go test ./...
go run ./scripts/modulecheck
```

루트 go test ./...만으로는 중첩 SDK와 생성기 테스트가 실행되지 않는다.
SDK 단독 검증은 그 디렉터리에서 GOWORK=off로 실행한다.
PowerShell에서는 `$env:GOWORK = 'off'`를 설정한다.

modulecheck는 현재 체크아웃의 추적 파일과 ignore되지 않은 신규 파일로 Q와
SDK의 별도 module zip을 만들고 임시 파일 프록시를 통해 **GOWORK=off 버전 지정
설치**를 실행한다. 테스트 아카이브 안에서만 SDK 의존성을 테스트용 버전으로
바꾸고 실제 SDK checksum을 제외한다. GOMODCACHE·GOBIN도 격리하며 실제 소스의
go.mod/go.sum과 공유 캐시는 바꾸지 않는다. 실제 GitHub 공개 여부나 Zed 호환성을
검증하는 것은 아니다.

주요 회귀 항목:

- 폼·URL의 세션/도구 scope, 숫자·문자열·0 요청 ID JSON 왕복.
- 없는 scope 필드의 생략과 취소 알림의 필수 session ID 보존.
- 오버레이 재적용, 잘못된 패치, upstream 필드 충돌·필수화 탐지.
- Q의 Griller 질문, plan 승인, commit 승인·취소·재생성, 다중 세션 테스트.
- 생성 후 재생성 일치, go vet, 바이너리 빌드.
- 수동 확인: Zed에서 질문·plan·commit 폼이 올바른 세션에 표시되는지.

## 첫 배포에 필요한 일

태그는 필수가 아니다. 로컬 워크스페이스 참조, 원격 모듈 배포, Git 태그를
구분한다. 원격에서 받을 수 있는 SDK 커밋이 필요하며 버전은 Go가 생성한다.

1. SDK 변경을 먼저 커밋하고 Q 저장소의 main에 푸시한다.
2. 루트의 미공개 SDK require를 제거하고 GOWORK=off로 go mod tidy를 실행한다.
   Go가 공개된 SDK의 최신 버전과 checksum을 실제 go.mod/go.sum에 기록한다.
   이미 유효한 SDK 의존성이 있다면 go get으로 @latest를 요청해 갱신한다.
3. Q의 의존성과 checksum을 별도 커밋으로 공개한다. SDK 커밋은 자기 자신의
   미래 해시를 포함할 수 없으므로 첫 배포는 SDK 공개 → Q 의존성 반영 순서다.
4. 체크아웃 밖의 임시 GOBIN에서 공개 Q 커밋과 @latest 설치를 확인한다.

구체적인 명령은 [SDK RELEASING.md](../third_party/acp-go-sdk/RELEASING.md)에 있다.
해시나 시각을 직접 조합하지 않고 Go의 버전 해석 결과를 사용한다.
나중에 태그를 도입한다면 SDK 태그에는 third_party/acp-go-sdk/ 접두사가 필요하다.
커밋·푸시는 명시적인 요청을 받아 수행한다.

첫 SDK 공개 커밋은 `155b26051836518f3db3ad1a31ee9110aabe325a`이고, Go가 실제로
선택한 버전은 `v0.0.0-20260827012000-155b26051836`이다. 프록시의 latest 캐시가
이전 커밋을 반환하면 GOPROXY=direct로 저장소를 직접 조회해 확인한다.
이 설정은 해당 명령에만 적용하며 checksum 검증을 비활성화하지 않는다.

## 다음 업스트림 업데이트

SDK 디렉터리는 Git submodule이 아니다. 여기서 git pull하면 **Q가 갱신된다.**
원본 전체 트리를 새 버전으로 덮어쓰지 않는다.

1. UPSTREAM.json의 기준 커밋과 새 태그를 별도 upstream checkout에서 확보한다.
   module zip만 받으면 generator가 누락되므로 전체 Git 소스를 사용한다.
2. 기준 upstream → 새 upstream, 기준 upstream → Q 포크 diff를 비교해 3-way
   병합한다. Git 객체를 Q에 fetch했다면 git apply --3way도 가능하지만 독립
   저장소의 브랜치를 Q main에 그대로 merge하지 않는다.
3. 포크 모듈/import 경로, UPSTREAM.json, Q 문서·배포 절차를
   유지하고 새 파일의 원본 import도 포크 경로로 수정한다.
4. 새 프로토콜을 채택하면 schema/version을 갱신하고 make version 또는
   make update-schema VERSION=...으로 스키마를 받아 생성한다. 불필요해진
   오버레이 필드는 제거한다. 생성 파일을 손으로 병합한 채로 끝내지 않는다.
5. UPSTREAM.json과 Q_PATCH.md에 새 기준·차이를 기록하고 전체 테스트, 생성물
   검사, 배포 검사 및 Zed 수동 확인을 한다.
6. SDK 변경을 먼저 커밋·푸시하고 Go가 선택한 새 pseudo-version으로 Q 의존성과
   checksum을 갱신·커밋·푸시한다.

텍스트 충돌 해결만으로 끝나지는 않는다. API 이름·필드 선택성·wire JSON·
capability 의미가 바뀌면 호출부도 맞춰야 한다. upstream에 흡수된 패치는
줄이고, Q 고유 기능은 표준 필드를 임의 변경하지 않고 ACP 확장으로 분리한다.
