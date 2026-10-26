# Interaction · Council · Composer 세션 정규화

- 작성일/갱신일: 2026-10-27 (Asia/Seoul)
- 기준 커밋: `8ac7d87776bc2b134110e2c3b1b39d1f3ee1494a`
- 현재 범위: Council·Composer 저장 및 실행 구조 변경. Sessions 화면 통합은 후속 작업.
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

현재 Council·Composer 화면과 API는 새 저장소를 사용하지만 독립 메뉴를 유지한다.
일반 Sessions 화면은 아직 `interaction`만 표시한다. 아래 작업은 이번 범위에 포함하지 않는다.

1. 공통 목록 응답에 타입·capability를 연결하고 전체/Interaction/Council/Composer 필터를 제공한다.
2. 기존 개인 chat·프로젝트 구분, 검색, delegation 탐색을 유지한다.
3. 하나의 상세 틀에서 일반 transcript, Council 라운드/구성, Composer artifact 패널을 표시한다.
4. 타입별 실행·취소·질문·재시도 상태를 공통 monitor에 연결한다.
5. 기존 `/councils/<id>`, `/composers/<id>` 경로를 새 상세 화면에 연결한다.
   Sessions URL의 registration ID와 native session ID를 혼동하지 않는다.
6. 모바일, 질문 응답, 재접속, 입력칸 하단 배치, D2 줌·확대와 HTML 편집을 회귀 검증한다.

실행 중 타입 변경, 타입 간 자동 전환, 세션 대화나 산출물 병합은 별도 요구사항이다.

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
