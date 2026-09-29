---
locale: ko
title: 서브에이전트
description: 숨겨진 대화 상태를 상속하지 않는 제한된 내장·사용자 정의·외부 에이전트를 실행합니다.
sectionLabel: 가이드
toc:
  - id: 사용-가능한-에이전트-확인
    label: 사용 가능한 에이전트 확인
  - id: 단일-요청-실행
    label: 단일 요청 실행
  - id: 채팅에서-위임
    label: 채팅에서 위임
  - id: 내부-에이전트-정의
    label: 내부 에이전트 정의
  - id: 외부-에이전트
    label: 외부 에이전트
---

## 사용 가능한 에이전트 확인

Studio의 **Settings → Subagents**에서 내장 정의를 확인하고 사용자 정의 프로필을 관리하세요. 목록에는 각 프로필의 실행 종류, 모델 역할, 범위, 도구, 위임 권한이 표시됩니다.

공개 내장 ID는 다음과 같습니다.

- `builtin/interviewer`
- `builtin/manager`
- `builtin/senior-developer`
- `builtin/junior-developer`
- `builtin/research`
- `builtin/web-search`
- `builtin/web-tester`

## 단일 요청 실행

자식은 부모 대화를 자동으로 상속하지 않으므로 요청에 필요한 작업 컨텍스트를 모두 포함하세요.

```text
/subagent builtin/senior-developer app/model.go의 취소 경로를 검토해줘
```

bare q 호환 채팅에서 `/subagent`를 직접 호출할 때는 사용자 정의 프로필의 짧은 이름을 사용합니다. 프로필에 저장하는 위임 권한은 `builtin/senior-developer`, `global/code-reader`, `workspace/browser-check` 같은 정규 ID를 사용합니다.

## 채팅에서 위임

일반 채팅에는 직접 workspace 도구와 delegation이 함께 제공됩니다. 메인 에이전트는 요청에 따라 직접 작업하거나 범위가 정해진 서브에이전트를 조율할 수 있습니다. manager는 PM으로서 요구사항과 계획을 맡고, senior developer는 직접 수정하거나 junior developer에게 구현을 맡긴 뒤 결과를 검토합니다. 역할을 직접 지정하려면 `/subagent <name> <request>`를 사용하세요.

Studio는 각 자식을 세션 트리의 부모 아래에 표시합니다. 자식을 선택하면 진행 상황, 대화와 도구 호출이 열립니다. 각 호출은 부모의 북마크와 자식 세션으로 저장됩니다. 재시작하면 가장 깊은 자식부터 복구한 뒤 부모를 이어갑니다. 결과가 기록되지 않은 도구 호출은 자동 재실행하지 않고 `unknown`으로 전달합니다. 중단된 외부 ACP 호출도 내부 턴을 재개할 수 없어 `unknown`으로 반환합니다.

clean Git branch를 사용하는 저장된 session에서 변경 가능한 inner child는 local `q/delegate/<invocation-id>` branch와 linked worktree에서 작업합니다. 성공하면 Q가 남은 변경을 commit하고 base/head commit이 고정된 내부 Change Request를 반환합니다. 호출한 agent는 diff를 읽은 뒤 merge하거나 close합니다. 중첩 child도 같은 흐름을 사용하므로 senior developer가 junior developer의 branch를 검토·병합한 뒤 자신의 Change Request를 상위에 반환할 수 있습니다. Studio session tree에는 요청 상태와 branch가 표시됩니다. 이 local 흐름에는 remote push가 필요하지 않습니다.

모든 inner 작업 결과는 짧은 `summary`와 선택적인 `report`를 가집니다. `report`에는 최종 분석, 설계 근거, 리뷰 의견이나 조사 종합을 Markdown으로 최대 256 KiB까지 담을 수 있습니다. 큰 결과는 Loom에 저장되며 호출자는 reference와 제한된 preview를 받은 뒤 필요할 때 전체 보고서를 조회합니다.

## 내부 에이전트 정의

내부 프로필은 q 모델 역할, 명시적인 도구 목록, 직접 호출 가능한 위임 대상을 선택합니다.

```yaml
version: 1
name: code-reader
description: 요청된 코드를 설명합니다.
kind: inner
role: advisor
system_prompt: |
  요청된 코드를 읽고 구체적인 파일 위치와 함께 동작을 설명하세요.
tools:
  - list_directory
  - read_file
delegates: []
```

프로필은 `~/.q/subagents/` 또는 `<workspace>/.q/subagents/`에 있습니다. 같은 이름의 워크스페이스 프로필은 전역 프로필 전체를 대체합니다.


## 외부 에이전트

외부 프로필은 활성 ACP 연결에 바인딩됩니다. 시스템 프롬프트와 원격 에이전트의 워크스페이스 변경 허용 여부를 저장하지만 q 도구, 위임 대상, q 모델 역할은 선택하지 않습니다.

ACP 세션 생성에는 시스템 메시지 필드가 없으므로 q는 저장된 시스템 프롬프트를 첫 일반 ACP 요청 앞에 붙입니다. 연결이 없거나 비활성화되어 있으면 프로필을 삭제하지 않고도 사용할 수 없는 상태가 됩니다.
