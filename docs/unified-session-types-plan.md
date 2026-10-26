# Interaction · Council · Composer 세션 정규화

- 작성일/갱신일: 2026-10-27 (Asia/Seoul)
- 기준 커밋: `8ac7d87776bc2b134110e2c3b1b39d1f3ee1494a`
- 저장 구조 변경 및 후속 UI 통합 분석 기준: `d37d538`.
- 현재 범위: 저장 정규화 완료. 기존 Interaction UI를 보존하며 Sessions에 Council·Composer를 연결했다. 검증 결과는 4.6절에서 관리한다.
- 데이터 정책: 구형 Council·Composer를 이전하거나 가져오지 않는다. 일반 세션은 보존한다.

## 1. 확정된 저장 계약

모든 사용자 세션은 `<workspace-root>/.q/sessions/<session-id>/`에 둔다.
`session.json` 버전 3은 `type: interaction | council | composer`를 명시한다.
기존 버전 1·2의 일반 세션은 `interaction`으로 읽으며, 저장·초기화 시 타입을 보존한다.
알 수 없는 타입을 일반 도구 runtime으로 실행하지 않는다.

프로젝트가 없는 새 Council·Composer는 일반 개인 채팅과 같은
`<config-dir>/chats/chat-*/` 전용 workspace를 기본으로 사용한다.
저장소의 `CreateInRoot`와 생성 API의 선택적 `storage_root`로 저장 workspace를 지정할 수 있다.
Council의 `scope`, `workspace_root`, `project_id`는 읽기 권한을 결정하며 저장 위치와 독립적이다.
타입은 실행기를 구분할 뿐 도구 권한을 부여하지 않는다.

```text
<workspace-root>/.q/
├── sessions/<session-id>/
│   ├── session.json                 # 공통 ID, type, 제목, transcript/context
│   ├── model.json                   # 단일 모델 세션의 모델 선택
│   ├── council/
│   │   ├── config.json              # 구성·읽기 범위
│   │   ├── turns/<turn-id>.json      # 라운드·결과·재시도 checkpoint
│   │   └── participants/<child-id>/
│   │       ├── session.json         # Q 참가자의 native context
│   │       └── council-acp.json     # ACP 참가자의 실행 기록
│   ├── composer/conversation.json   # 화면용 턴·질문·상태 projection
│   ├── docs/diagram/*.d2
│   └── docs/webcomponent/*.html
└── session-locks/                   # 삭제 대상 밖에 두는 native lock
```

사용하지 않는 타입별 디렉터리나 파일을 빈 형태로 생성할 필요는 없다.
일반 세션의 기존 delegation 및 Studio run 파일은 기존 위치와 계약을 유지한다.
workspace archive·Loom을 세션 디렉터리로 이동하지 않는다.

## 2. 이번 구조 변경

- Council ID와 Composer ID는 각각 부모 native session ID와 같다.
- 전역 `studio-sessions.json` catalog에는 `(workspace_root, session_id)` 참조만 둔다.
  타입과 원본 데이터는 해당 세션 파일이 소유한다. catalog 갱신은 프로세스 간 잠금으로 직렬화한다.
- Council 전용 SQLite index와 scope별 전역 데이터 저장 구조를 제거했다.
- Council 조정기는 부모의 질문·최종 답변 projection을 저장한다. 참가자별 Q/ACP 실행 상태는
  `council/participants/` 안에서 관리하며, 참가자는 공통 root 목록에 나타나지 않는다.
- 내부 store는 `ForSession`, 실제 주소 기반 host cache/release, 참가자 잠금에서 경로를 유지한다.
- Composer는 기존 SessionHost와 공통 Agent Loop를 사용한다. native transcript와 context는
  부모 `session.json`이 소유하며, 표시용 이력은 `composer/conversation.json`이 소유한다.
- Composer artifact root는 부모의 `SessionDir()`이다. 허용 파일은 그 아래 D2·HTML이며
  기존 revision 충돌 검사·격리된 미리보기·확대/줌 기능을 유지한다.
- 일반 CLI/ACP 최근 세션 선택과 목록은 `interaction`으로 제한한다.
  일반 메시지·모델 변경·초기화·삭제·compact API로 typed workflow를 수정할 수 없다.
- 타입별 전체 삭제는 자신의 세션 디렉터리만 제거한다. 일반 개인 채팅을 삭제할 때도
  같은 workspace에 추가한 다른 세션이 있으면 workspace를 보존한다.

구성·checkpoint·projection을 파일 하나에 억지로 합치지 않는다.
Council 부모는 조정기가 쓰고, native 참가자와 Composer context는 기존 host가 쓴다.
새 모델/tool loop나 화면 이력에서 실행 context를 재조립하는 경로를 추가하지 않는다.

## 3. 기존 데이터 처리

사용자 결정에 따라 migration·ID alias·구형 데이터 importer를 구현하지 않는다.
구형 전용 `<config-dir>/council/`, `<config-dir>/composer/`는 새 목록이나 실행에서 읽지 않는다.
기존 일반 `.q/sessions/`, 개인 chat, 프로젝트와 설정 파일은 삭제 대상이 아니다.

로컬 `C:\Users\snowm\.q\council`과 `C:\Users\snowm\.q\composer` 삭제를 시도했으나
실행 정책에서 거부했다(`blocked by policy`). 두 디렉터리는 실제로 남아 있으므로 삭제 완료로
간주하지 않는다. 자동 이전이나 앱 시작 시 삭제로 이 제한을 우회하지 않는다.

## 4. 후속 Sessions 화면 통합 계획

아래 4.1~4.5절은 `d37d538` 당시 작성한 통합 분석과 제안이다.
이후 사용자가 기존 Sessions UI 보존을 최우선 조건으로 구현을 요청했다.
현재 구현은 4.6절에 기록하며, 공통 durable monitor/API 통합은 후속 범위로 유지한다.

1. 공통 목록 응답에 타입·capability를 연결하고 전체/Interaction/Council/Composer 필터를 제공한다.
2. 기존 개인 chat·프로젝트 구분, 검색, delegation 탐색을 유지한다.
3. 하나의 상세 틀에서 일반 transcript, Council 라운드/구성, Composer artifact 패널을 표시한다.
4. 타입별 실행·취소·질문·재시도 상태를 공통 monitor에 연결한다.
5. 기존 `/councils/<id>`, `/composers/<id>` 경로를 새 상세 화면에 연결한다.
   Sessions URL의 registration ID와 native session ID를 혼동하지 않는다.
6. 모바일, 질문 응답, 재접속, 입력칸 하단 배치, D2 줌·확대와 HTML 편집을 회귀 검증한다.

실행 중 타입 변경, 타입 간 자동 전환, 세션 대화나 산출물 병합은 별도 요구사항이다.

### 4.1 코드 조사 결과

아래 표는 `d37d538` 시점의 구현 전 조사 결과다. 이후 구현 상태는 4.6절이 소유한다.

| 지점 | 현재 코드의 동작 | 통합에 필요한 변경 |
| --- | --- | --- |
| `studio/session_registry.go` | catalog에 세 타입이 있지만 응답에서 Interaction 이외를 제외 | 공통 목록에 세 타입 노출, 타입별 capability 제공 |
| `studio/sessions.go` | backend summary에 이미 `type` 존재. workspace 목록은 Interaction만 조회 | UI 타입 정의 연결, 통합 조회 범위 결정. CLI/ACP 필터는 유지 |
| `sessions/types.ts` | frontend SessionSummary에 `type` 없음 | SessionType과 capability 계약 추가 |
| `SessionView.svelte` | 선택 시 일반 transcript, learning, latest run, delegation을 모두 조회 | 목록/선택 틀과 일반 Interaction 상세를 분리하고 타입별 상세 연결 |
| `SessionTree.svelte` | 프로젝트/개인 chat 그룹, 검색, delegation 트리 | 타입 필터·표시 추가, 기존 그룹과 검색 유지 |
| `CouncilView.svelte` | 목록·URL·생성·구성·라운드·polling을 한 컴포넌트가 소유 | 목록/URL을 공통 틀로 이동하고 구성 폼·상세 패널 추출 |
| `ComposerView.svelte` | 목록·URL·생성·대화·질문·polling을 한 컴포넌트가 소유 | 대화 패널·생성 폼 추출, 공통 선택/초안/이탈 처리 연결 |
| `ComposerArtifacts.svelte` | composer ID로 source/preview를 읽는 독립 패널, dirty 상태 전달 | 기존 패널 재사용, 세션 변경 시 미저장 편집 보호 |
| `App.svelte`, `OverviewView.svelte` | 별도 최상위 메뉴와 각 상세 URL, 일반 세션 최근 목록 | Sessions로 진입점 통합, 기존 링크와 최근 목록 처리 |

필터만 제거하면 `selectRegisteredSession()`이 Council/Composer에도 일반 run·delegation
조회와 일반 실행 컨트롤을 연결한다. 서버가 이러한 변경을 거절하도록 이미 보호되어 있으므로
목록 노출과 상세 dispatch를 같은 변경에서 완성해야 한다.

### 4.2 권장 화면 경계

```text
Sessions 화면
├── 공통 목록: 검색 / 전체·Interaction·Council·Composer / 프로젝트·개인 분류
├── 공통 선택·URL·새 세션 진입점
└── 선택한 세션의 상세 패널
    ├── Interaction: 기존 transcript·도구·delegation·파일·실행 제어
    ├── Council: 질문·라운드·의견·검토·최종 답변·구성·재시도
    └── Composer: 대화·질문 응답·하단 입력·D2/HTML 소스와 미리보기
```

가칭 `SessionWorkbench`가 catalog/선택/라우팅/생성을 소유하고,
`InteractionSessionPanel`, `CouncilSessionPanel`, `ComposerSessionPanel`을 연결한다.
각 패널은 목록이나 전역 URL을 다시 소유하지 않는다. 기존 화면 전체를 그대로 중첩하면
좌측 목록과 생성 진입점이 중복되므로 상세와 폼을 추출한다.

상세 패널은 registration ID를 key로 마운트하여 세션별 상태를 분리한다.
입력 영역은 세 타입 모두 상세 하단을 사용하되, 실행 중 입력의 의미는 유지한다.
Interaction은 guidance로 이어질 수 있지만 Council/Composer에 같은 의미를 임의로 부여하지 않는다.
전송 버튼·질문 카드 같은 작은 UI는 필요한 범위에서 공통화한다.

### 4.3 실행 연결과 capability

첫 UI 통합에서는 기존 API와 실행 서비스를 유지하고 frontend에서 타입별 어댑터로 연결한다.
목록·선택·생성 후 갱신·삭제 후 갱신은 공통 틀이 소유하며,
각 패널/어댑터는 기존 타입별 조회·전송·취소·질문·재시도를 호출한다.
새 Agent Loop나 실행 이력 저장 형식을 추가하지 않는다.

| 동작 | Interaction | Council | Composer |
| --- | --- | --- | --- |
| 전송 / 중단 | 기존 messages / run commands | turns / 해당 run cancel | messages / cancel |
| 진행 관찰 | durable cursor 기반 RunMonitor | 라운드 snapshot polling | 대화·질문 snapshot polling |
| 질문 응답 | pending question에 답변 | 현재 별도 사용자 질문 API 없음 | 기존 answer API |
| pause / resume / guidance | 기존 지원 | 라운드 재시도와 구분, 공통 컨트롤 제공 안 함 | 현재 공통 컨트롤 제공 안 함 |
| 실패 턴 재시도 | 기존 실행 복구 의미 유지 | resume / rerun 구분 유지 | 다음 대화로 계속, Council 재시도 UI 제공 안 함 |
| 모델/구성 | 기존 SessionModelPicker | 멤버·의장·라운드 구성 | 생성 시 모델 선택, 기존 세션 모델 수정 API는 현재 없음 |
| compact / clear | 기존 지원 | 현재 공통 컨트롤 제공 안 함 | 현재 공통 컨트롤 제공 안 함 |
| 산출물 | 기존 파일 탐색 | 라운드 결과·export | D2/HTML artifact 패널 |

서버 응답의 capability는 해당 타입의 현재 지원 동작만 설명한다. 실행 중 여부·잠금·질문
call ID는 실제 서비스가 계속 검사한다. frontend capability 표시를 권한 검증으로 사용하지 않는다.
기존 `RequireInteraction`을 제거해서 일반 endpoint로 typed workflow를 실행하지 않는다.

세 타입의 상태 표시는 공통 표현으로 맞출 수 있지만, snapshot polling에서 임의의 durable
event cursor를 만들어서는 안 된다. `sessionSummary.run_id`도 현재 활성 Studio run ID로
간주하지 않는다. 공통 durable monitor/API까지 통합하는 작업은 UI 통합 뒤 별도 단계로 둔다.

### 4.4 ID·그룹·수명주기에서 보존할 계약

- **URL과 실행 주소:** `/sessions/<registration-id>`를 유지한다. 실행에는 registration의
  `workspace_root`와 `session.session_id`를 사용한다. native ID만으로 모든 루트를 식별하지 않는다.
- **기존 링크:** `/councils/<session-id>`, `/composers/<session-id>`는 catalog의 타입/ID를
  조회해 Sessions 상세로 연결한다. 여러 후보가 있으면 임의 선택하지 않는다. 데이터 이전은 없다.
- **그룹과 타입:** 현재 `kind: chat`은 관리되는 개인 workspace를 뜻한다. 이를 실행 타입으로
  사용하지 않는다. private workspace의 Council도 Council 타입이며 개인 분류에 들어갈 수 있다.
  Council의 project 읽기 scope만으로 저장 세션의 프로젝트 소유권을 바꾸지 않는다.
- **등록 해제와 삭제:** typed store는 catalog를 통해 세션을 찾는다. 일반 세션의 단순 unregister를
  노출하면 typed 데이터가 디스크에 남은 채 발견되지 않을 수 있다. 첫 통합에서는 타입별 전체
  삭제 endpoint를 사용하고, 독립적인 숨김/등록 해제는 별도 검색 계약이 생기기 전 제공하지 않는다.
- **초안:** 일반 Sessions의 `sessions/drafts.ts`를 재사용하고 registration ID로 구분한다.
  Composer의 현재 컴포넌트 내부 Map과 Council의 단일 prompt 상태를 공통 초안 저장에 연결한다.
- **미저장 source:** 목록 선택, 새 세션, 타입 필터로 인한 선택 변경, 뒤로/앞으로 이동 전에
  artifact dirty 상태를 확인한다. 편집 취소 시 기존 선택과 URL이 서로 달라지지 않게 한다.
- **늦은 응답:** 타입/세션 전환 시 polling과 fetch를 중지하고 세대 번호를 검증한다.
  Council은 현재 timer 정리만으로 진행 중 fetch를 취소하지 않으므로 이 부분을 보강한다.
- **목록 최신성:** 생성·삭제·완료 후 공통 catalog를 갱신한다. 일부 endpoint 응답은 회복 처리로
  상태가 바뀔 수 있으므로 UI가 표시용 상태를 원본 `session.json`에 다시 저장하지 않는다.

### 4.5 구현 순서와 첫 통합의 완료 기준

1. **상세 패널 추출:** 기존 세 화면에서 목록/라우팅을 분리하고, 기존 URL에서 기능을 보존한다.
   타입 정의와 생성 폼도 분리한다. Composer artifact·Council 결과 표시를 재작성하지 않는다.
2. **공통 화면 연결:** catalog 응답과 frontend 타입을 연결하고 목록에 세 타입을 노출한다.
   선택은 타입별 패널로 dispatch하며, 지원하지 않는 일반 컨트롤을 노출하지 않는다.
3. **생성·관리 연결:** 공통 New 진입점에서 타입을 고르고 기존 생성 서비스를 호출한다.
   독립 세션의 private workspace 기본값과 선택적 `storage_root` 계약을 유지한다.
   생성 응답 후 catalog를 다시 조회해 해당 registration을 선택한다.
4. **메뉴·링크 정리:** Sessions만 최상위 진입점으로 두고 기존 typed URL, Overview 최근 목록,
   뒤로/앞으로 이동과 새로고침을 연결한다. 기존 `new=chat/workspace` 링크도 보존한다.
5. **회귀 검증:** 한 Sessions 화면에서 세 타입의 생성·검색·선택·재접속·삭제까지 확인한다.
   Council 구성/라운드/ACP·재시도, Composer 질문/편집/확대, 일반 delegation/파일/제어를 유지한다.

첫 통합 완료는 **하나의 Sessions 진입점과 목록 안에서 세 타입을 각각 사용할 수 있는 상태**다.
세 타입의 모든 HTTP 주소·run event 형식까지 동시에 합치는 것을 완료 조건으로 삼지 않는다.
그 후 공통 실행 monitor가 필요하면 현재 타입별 의미를 보존하는 서버 어댑터를 추가한다.

기존 API 보호 테스트는 유지한다. 추가 테스트는 공통 목록의 타입/ID, 정상 및 손상된 등록 항목,
타입별 동작 노출, 기존 링크, 늦은 응답 격리, 미저장 source·초안 보존에 집중한다.
조사·문서 갱신 단계에서는 제품 코드 변경이나 새 테스트 실행을 하지 않았다.

### 4.6 Sessions 통합 구현과 UI 보존

이 절의 보존 비교는 `81a3bfa`까지의 Sessions 통합을 기준으로 한다.
이후 사용자가 요청한 Chat reasoning 선택과 Composer 스타일 정렬은 4.7절에서 다룬다.

사용자 조건: 기존 Sessions UI를 해치지 않는 것을 최우선으로 한다.
이에 따라 Interaction 상세 전체를 다른 컴포넌트로 옮기는 대규모 추출은 진행하지 않았다.
기존 `SessionView.svelte`의 Interaction markup, 공통 `app.css`, 입력창·transcript·파일 패널
배치를 유지하고, 같은 목록의 선택 타입이 Council/Composer일 때만 상세 패널을 연결한다.
기존 typed 컴포넌트는 embedded 모드에서 자신의 목록·전역 URL 처리를 사용하지 않는다.

- catalog는 세 타입을 함께 반환한다. typed root에는 일반 delegation tree를 붙이지 않는다.
- 기존 New chat와 Workspace/Project 생성 흐름은 유지한다. 사용자 요청에 따라 개인 생성
  탭 이름을 Temporary로 바꾸고, 하단에 New chat·New Council·New Composer를 같은 수준의
  생성 버튼으로 배치한다. 모바일에서는 버튼을 줄바꿈하며 기존 창 높이 기준을 유지한다.
- 타입 필터는 typed 세션이 있거나 해당 필터를 선택한 경우에만 나타난다.
  필터 변경은 열려 있는 대화를 교체하지 않으며 새 세션 생성 시 전체 필터로 복귀한다.
- 최상위 진입점은 Sessions다. 기존 `/councils`, `/composers` URL은 catalog를 통해
  registration을 찾아 같은 Sessions 화면으로 연결하며 실행에는 native session ID를 쓴다.
- 일반 대화의 실행·모델·압축·학습·파일·delegation 컨트롤은 유지한다.
  typed 상세는 기존 타입별 API를 사용한다. 별도 capability 응답과 durable monitor 통합은
  이번에 추가하지 않았으며 서버의 일반 endpoint 보호를 유지한다.
- 세션별 초안은 공통 저장 helper를 사용하되 Interaction 입력과 typed 입력의 작성 주체를
  분리한다. 미저장 source는 선택·메뉴 이동·브라우저 이탈 시 보호한다.
- 전환 중 fetch/poll은 취소하고 늦은 응답을 무시한다. 모델 선택기는 선택된 root와 ID가
  일치한 상세를 받은 뒤 연결하여 이전 ID와 새 root를 섞어 조회하지 않는다.

검증 경계는 일반 채팅 영역의 시각적 보존, 기존 Sessions 전체 사용자 흐름,
Council/Composer 기능 유지, 타입 간 초안·편집·비동기 상태의 격리다.
검증 결과(2026-10-27):

- 변경 전 기존 Sessions 브라우저 테스트 10개를 실행해 기준 화면을 확보했다.
- 변경 후 기존 브라우저 회귀 86개가 통과했다. 추가한 workflow 통합 테스트 2개도
  최종 재실행에서 통과했다. 추가 테스트는 모바일 목록이 접힌 후의 조회 선택자를 수정해 재검증했다.
- 기준/최종 캡처의 일반 대화 영역 비교에서 1440×900과 390×900 모두 변경 픽셀 0개였다.
  의도적으로 통합한 최상위 메뉴는 비교 대상에서 제외했다. 별도로 타입 전환 전후의
  Interaction 상세는 전체 패널 screenshot buffer와 bounding box가 동일함을 검증했다.
- Chromium에서 일반 모델 선택·생성 창·검색·IME·파일·delegation·guidance·재접속,
  Council 구성/ACP/라운드/재시도, Composer 질문/편집/격리 미리보기/확대를 검증했다.
- 세 타입 초안, 미저장 source 이탈 취소, 뒤로/앞으로 이동, 늦은 Council 응답 격리,
  모바일 선택기와 새 채팅 생성 시 타입 필터 복귀를 추가 검증했다.
- `go test -p 1 ./... -timeout=10m`, 최종 frontend asset 기준 Studio 전체 테스트,
  frontend check/build, `go vet`, `go build`, module tidy 차이 검사, golangci-lint가 통과했다.
  govulncheck에서 코드가 호출하는 취약점은 발견되지 않았다.

Browser 플러그인이 제공되지 않아 저장소의 Playwright와 격리된 Studio/model fixture를 사용했다.
스크린샷·trace 등 실행 산출물은 저장소 밖의 임시 디렉터리에 저장했다.

### 4.7 Chat reasoning과 Composer 표시 일관성

- Chat 하단 모델 선택기 옆에 모델 discovery의 지원 단계에 따른 reasoning 선택을 추가했다.
  세션의 `model.json`에 저장하며, 빈 값은 전역 Chat reasoning을 상속한다. 모델만 변경하는
  기존 API 요청은 reasoning 선택을 보존한다. 실행 중 변경은 기존 세션 잠금으로 거부한다.
- 일반 SessionHost 턴에서 선택값을 적용한다. 명시적인 `RunWithOptions` 정책은 우선하므로
  Council·Composer 등 임베딩 실행기의 reasoning 정책을 바꾸지 않는다.
- Composer 대화는 일반 Chat의 Transcript와 헤더·질문·입력창 스타일을 재사용한다.
  표시용 턴/질문/응답 상태만 렌더러에 투영하며 native 모델 context를 다시 조립하지 않는다.
  전송은 입력창 안의 화살표, 중단과 삭제는 헤더 아이콘으로 제공한다.
- 산출물 경계·편집·격리 미리보기·확대, 초안과 미저장 source 보호는 기존 계약을 유지한다.
- 저장·상속·명시적 실행 정책 우선순위는 실제 모델 요청을 검사하는 Go 테스트로 확인한다.
  브라우저에서는 저장/재시작/초기화/실행 중 잠금과 Composer의 기존 전체 흐름을 검증한다.

세부 저장·임베딩 계약은 [Agent Loop 임베딩 문서](agent-loop-embedding.md)가 소유한다.

검증 결과: Go 전체 테스트와 실제 요청의 reasoning/상속/명시적 정책 우선순위 테스트,
frontend check/build, vet/build/tidy 차이 검사, lint, govulncheck, modulecheck가 통과했다.
브라우저는 79개 통과 후 공유 테스트 서버의 5분 제한이 만료되어 남은 9개를 재실행했고
모두 통과했다. Playwright용 Go fixture 제한을 10분으로 늘렸으며 제품 실행 제한은 바꾸지 않았다.

## 5. 수정 규모와 검증

초기 전체 통합 추정은 제품 소스 약 30~45개, 테스트 약 15~25개를 포함한
45~75개 파일, 개발자 1명 기준 7~12인일이었다. 이 수치는 데이터 이전과 UI까지 포함한
구현 전 추정이다. 이번 결정으로 이전 작업은 제외했고 UI는 후속 범위로 남았다.
공수 추정은 실제 소요 시간이나 자동화 실행 시간을 보장하지 않는다.

구조 변경에서 확인할 계약:

- 구형 일반 session 읽기, 타입 직렬화·보존, 알 수 없는 타입 거부.
- Council 라운드·취소·재시도·native/ACP 참가자 내부 경로와 잠금.
- Composer 공통 loop의 도구 실행·질문·재개·context 보존 및 부모 docs 안의 artifact CRUD.
- 일반 API의 typed workflow 수정 거부, 한 세션 삭제 시 다른 세션 보존.
- 서비스 재시작 후 shared catalog 조회, 구형 전용 데이터 미수입.
- Go 테스트·vet·build·lint·취약점 검사와 공개 API 배포 확인(modulecheck).
- 실제 Studio fixture에서 Council·Composer 브라우저 회귀 테스트.

현재 저장·실행 계약은 [Agent Loop 임베딩 문서](agent-loop-embedding.md)에도 반영한다.
