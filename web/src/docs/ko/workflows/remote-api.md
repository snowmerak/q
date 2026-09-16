---
locale: ko
title: 원격 API
description: 인증된 포그라운드 HTTP 서비스에서 q 세션과 단발 에이전트 요청을 실행합니다.
sectionLabel: 가이드
toc:
  - id: 구성과-시작
    label: 구성과 시작
  - id: 워크스페이스-상태-조회
    label: 워크스페이스 상태 조회
  - id: 에이전트-실행
    label: 에이전트 실행
  - id: 스트림-이벤트
    label: 스트림 이벤트
  - id: 오류와-실행-용량
    label: 오류와 실행 용량
  - id: 원격-경계
    label: 원격 경계
---

## 구성과 시작

리스너, 인증 스위치, Remote 전용 API 키를 구성합니다.

```powershell
q remote config
```

그다음 포그라운드 호스트를 실행합니다.

```powershell
q remote
```

기본 리스너는 `127.0.0.1:0`입니다. Remote 키는 q 프로세스가 접근할 수 있는 어떤 작업 디렉터리든 선택하고 워크스페이스 변경 도구를 실행할 수 있으므로 Gateway 키와 분리되어 있습니다.

## 워크스페이스 상태 조회

작업 디렉터리의 세션을 나열합니다.

```powershell
curl.exe -H "Authorization: Bearer $env:Q_REMOTE_KEY" "http://127.0.0.1:8080/v1/sessions?working_directory=C%3A%5Cwork%5Cproject"
```

같은 워크스페이스에 적용되는 내장 및 사용자 정의 서브에이전트를 나열합니다.

```powershell
curl.exe -H "Authorization: Bearer $env:Q_REMOTE_KEY" "http://127.0.0.1:8080/v1/subagents?working_directory=C%3A%5Cwork%5Cproject"
```

## 에이전트 실행

`POST /v1/subagent-runs`는 JSON을 받고 `application/x-ndjson` 이벤트를 스트리밍합니다.

```powershell
curl.exe -N `
  -H "Authorization: Bearer $env:Q_REMOTE_KEY" `
  -H "Content-Type: application/json" `
  -d '{"working_directory":"C:\\work\\project","prompt":"시작 경로를 설명해줘"}' `
  http://127.0.0.1:8080/v1/subagent-runs
```

`working_directory`와 `prompt`는 필수입니다. `session_id`를 생략하면 새 세션을 만들고, 점유되지 않은 기존 세션을 재개하려면 값을 보냅니다. `subagent`를 생략하거나 빈 값으로 보내면 기본 메인 루프를 실행합니다. `builtin/scout` 같은 ID를 보내면 직접 서브에이전트 흐름을 사용합니다.

프롬프트는 UTF-8 32 KiB, 전체 JSON 요청 본문은 256 KiB로 제한됩니다. 알 수 없는 JSON 필드는 거부됩니다. 응답은 `Cache-Control: no-store`를 사용하며 `X-Q-Session-ID` 헤더에 선택된 세션을 제공합니다.

## 스트림 이벤트

첫 NDJSON 레코드는 항상 `session`입니다. `working_directory`, `session_id`, `created` 필드로 정규화된 워크스페이스와 새 세션 생성 여부를 알 수 있습니다.

| 종류 | 주요 필드 | 의미 |
| --- | --- | --- |
| `status` | `detail` | 시작 경고 또는 간결한 런타임 상태 |
| `activity` | `agent`, `task_id`, `parent_id`, `action`, `detail` | 서브에이전트 생명주기 진행 상황 |
| `trace` | `agent`, `kind`, `call_id`, `name`, `content`, `is_error` | 상세 서브에이전트 추적 항목 |
| `tool_call` | `call_id`, `name`, `content` | 메인 에이전트 도구 요청. `content`는 인자를 포함합니다. |
| `message` | `role`, `name`, `call_id`, `content`, `is_error` | 세션에 추가된 모델 또는 도구 메시지 |
| `question` | `question`, `context` | 대화형 질문 시도. Remote에서는 답할 수 없습니다. |
| `result` | `session_id`, `outcome`, `content` | 성공한 종료 결과 |
| `cancelled` | — | 취소 후 종료 레코드 |
| `error` | `error` | 스트리밍 시작 후의 종료 실패 |

클라이언트는 필요하지 않은 필드를 무시하고 종료 레코드까지 읽어야 합니다. 연결을 끊으면 요청 컨텍스트와 활성 실행이 취소됩니다.

## 오류와 실행 용량

스트리밍 시작 전 실패는 JSON `error` 객체와 HTTP 상태로 반환됩니다.

| 상태 | 코드 | 의미 |
| --- | --- | --- |
| `400` | `invalid_request` | JSON, 디렉터리, 세션 ID, Content-Type, 요청 형식이 잘못됨 |
| `401` | `invalid_api_key` | 인증이 켜져 있고 bearer 키가 없거나 유효하지 않음 |
| `404` | `session_not_found` 또는 `subagent_not_found` | 선택한 세션이나 서브에이전트가 없음 |
| `409` | `session_busy` | 다른 프로세스가 선택한 세션을 점유 중 |
| `413` | `request_too_large` | 본문 또는 프롬프트가 제한을 초과함 |
| `429` | `remote_capacity` | 활성 실행이 `agents.max_parallel`에 도달함 |
| `503` | `subagent_unavailable` | 에이전트는 있지만 구성된 런타임을 사용할 수 없음 |

`GET /v1/health`는 서비스 버전과 새 실행 수락 가능 여부를 보고합니다. 이 엔드포인트와 `/openapi.json`은 bearer 키 없이 접근할 수 있으며 워크스페이스 상태를 노출하지 않습니다.

## 원격 경계

Remote는 도구 카탈로그에 `ask_to_user`를 유지하지만 스트림은 단방향입니다. q는 시도한 `question`을 내보낸 뒤 즉시 상호작용 불가 도구 오류를 반환합니다. 모델은 가진 정보로 계속하거나 막힌 상태로 끝낼 수 있습니다.

Remote에는 내장 TLS, 경로 허용 목록, 백그라운드 작업, 재연결, 대화형 답변이 없습니다. 인증과 신뢰할 수 있는 기밀 네트워크 또는 리버스 프록시가 없다면 루프백에서만 사용하세요.

실행 중인 서비스의 정확한 계약은 `/openapi.json`에서 볼 수 있습니다.
