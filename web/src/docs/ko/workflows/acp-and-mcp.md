---
locale: ko
title: ACP와 외부 MCP
description: q를 ACP 에이전트로 실행하고 선택한 모델 역할에 외부 MCP 도구 서버를 연결합니다.
sectionLabel: 가이드
toc:
  - id: acp로-q-실행
    label: ACP로 q 실행
  - id: 계획-자동화-제어
    label: 계획 자동화 제어
  - id: 상호작용과-컨텍스트
    label: 상호작용과 컨텍스트
  - id: 외부-mcp-서버-연결
    label: 외부 MCP 서버 연결
  - id: 전송-경계
    label: 전송 경계
---

## ACP로 q 실행

q를 stdin/stdout 기반 Agent Client Protocol 서버로 시작합니다.

```powershell
q acp --root C:\work\project
```

ACP 모드는 터미널 UI와 같은 영구 워크스페이스 세션, 루트 제한 도구, 계획 워크플로, 서브에이전트, 학습, 커밋 워크플로를 사용합니다. 연결된 클라이언트에 `/plan`, `/commit`, `/subagents`, `/subagent`, `/learn`, `/clear`, `/help` 같은 명령을 알립니다.

`--root`의 기본값은 현재 디렉터리입니다. 파일, 세션, 지침, 스킬, 워크스페이스 구성의 경계를 정의합니다.

## 계획 자동화 제어

프로세스 전용 플래그로 저장된 설정을 바꾸지 않고 계획 질문과 제안 승인을 자동화할 수 있습니다.

```powershell
q acp --root C:\work\project --auto-resolve --auto-approve
q acp --root C:\work\project --autonomous
```

`--autonomous`는 두 동작을 모두 켭니다. `--auto-approve=false`처럼 명시한 개별 플래그가 우선합니다. 이 플래그는 `/plan`에 적용되며 제안 검증, 작업 실행, 검토, 커밋 확인을 건너뛰지 않습니다.

슬래시 명령 `/auto-resolve`, `/auto-approve`, `/autonomous`는 `on`, `off`, `status`를 지원합니다. 프로세스 플래그와 달리 `on`과 `off`는 `~/.q/config.yaml`에 저장됩니다.

## 상호작용과 컨텍스트

클라이언트가 폼 요청을 지원하면 q는 계획과 커밋 선택에 이를 사용합니다. 그렇지 않으면 번호가 있는 승인 동작을 표시합니다. 계획 및 에이전트 질문은 다음 클라이언트 메시지를 자유 형식 답변으로 사용할 수 있습니다.

q는 ACP 임베디드 컨텍스트 지원을 알리고, 세션 재생 시 리소스 URI, MIME 형식, 주석, 내용을 보존합니다. q TUI가 다른 ACP 에이전트에 연결된 경우 `@relative/path` 또는 `@"path with spaces"`로 워크스페이스 파일을 첨부할 수 있습니다. 원격 에이전트가 임베디드 컨텍스트를 지원하면 파일을 포함하고, 아니면 리소스 링크를 보냅니다.

## 외부 MCP 서버 연결

`/mcp`를 열거나 `q mcp`를 실행해 외부 MCP 서버를 구성합니다. q는 로컬 stdio 프로세스와 Streamable HTTP 엔드포인트를 지원합니다. 필요한 역할에만 서버를 할당할 수 있으므로 모든 모델 요청에 도구를 노출할 필요가 없습니다.

가져온 도구 이름은 q 내장 도구 및 다른 서버와 충돌하지 않도록 네임스페이스가 붙습니다. 결과는 내장 도구와 같은 Loom 캡처 경계를 지나며, 너무 큰 페이로드는 제한된 아티팩트 참조가 됩니다.

자격 증명은 자식 환경변수나 HTTP 헤더를 원본 환경변수 이름에 매핑합니다. 비밀 값을 `~/.q/mcp.json`에 직접 기록할 필요가 없습니다.

## 전송 경계

ACP 클라이언트가 제공한 MCP 서버는 해당 ACP 세션에만 적용되며 전역 q 구성이 되지 않습니다. stdio와 Streamable HTTP를 지원하고 SSE 전송은 지원하지 않습니다.

MCP 도구는 자체 프로세스나 원격 서비스의 권한으로 동작합니다. q는 결과 크기와 도구를 볼 수 있는 모델 역할을 제한할 수 있지만 임의의 외부 서버를 운영체제 샌드박스로 바꾸지는 못합니다.
