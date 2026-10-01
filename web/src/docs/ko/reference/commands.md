---
locale: ko
title: 명령어
description: q의 주요 워크플로를 제어하는 채팅 및 CLI 명령을 확인합니다.
sectionLabel: 참조
toc:
  - id: 채팅-명령
    label: 채팅 명령
  - id: cli-명령
    label: CLI 명령
  - id: 필수-키
    label: 필수 키
---

## 채팅 명령

| 명령 | 용도 |
| --- | --- |
| `/changes` | 스테이징, 비스테이징, 미추적 변경을 살펴봅니다. |
| `/commit` | 커밋 제안을 생성하고 검토합니다. |
| `/sessions` | 다른 저장된 워크스페이스 세션을 엽니다. |
| `/new` | 새 세션을 만들어 전환합니다. |
| `/clear` | 현재 대화 프로젝션을 지웁니다. |
| `/learn [on\|off\|status]` | 워크스페이스 영구 학습을 체크포인트하거나 제어합니다. |
| `/model` | 모델을 할당하고 폴백 그룹을 구성합니다. |
| `/gateway` | 공급자와 Gateway 리스너를 구성합니다. |
| `/systemone` | System One 공급자, decision 모델, 키와 리스너를 구성합니다. |
| `/library` | 전역 Library 리스너를 구성합니다. |
| `/loom` | Loom 저장소를 확인하고 GC를 구성합니다. |
| `/ignore` | `.qignore` 워크스페이스 검색 규칙을 편집합니다. |
| `/skills` | 전역 및 워크스페이스 Agent Skills를 관리합니다. |
| `/subagents` | 내장·사용자 정의·외부 에이전트를 관리합니다. |
| `/subagent <name> <request>` | 범위가 제한된 서브에이전트 요청 하나를 실행합니다. |
| `/mcp` | 외부 MCP 서버를 구성합니다. |
| `/lsp` | 언어 서버 프로필과 루트를 구성합니다. |
| `/help` | 전체 명령 및 키 도움말을 엽니다. |


## CLI 명령

| 명령 | 용도 |
| --- | --- |
| `q studio [--host <ip>] [--port <port>] [--no-open]` | 내장 Studio 웹 인터페이스를 시작합니다. 기본값은 `127.0.0.1`과 임의 포트입니다. |
| `q gateway` | Studio에서 Gateway 공급자와 리스너 설정을 엽니다. |
| `q gateway start` | OpenAI 호환 Gateway를 시작합니다. |
| `q systemone` | Studio에서 System One 공급자, decision 모델과 키를 엽니다. |
| `q systemone start [--host <ip>] [--port <port>]` | System One decision API를 시작합니다. |
| `q library` | Studio에서 전역 Library 리스너 설정을 엽니다. |
| `q library start` | 전역 Library를 포그라운드 서비스로 유지합니다. |
| `q memory` | Workspace Memory를 독립적으로 실행합니다. |
| `q usage` | Studio의 Operations를 엽니다. |
| `q commit` | 현재 저장소의 커밋 제안을 생성·검토·수정하고 승인하는 터미널 커밋 세션을 실행합니다. |
| `q model` | Studio에서 전역 및 워크스페이스 모델 할당을 엽니다. |
| `q subagents` | Studio에서 서브에이전트 프로필과 ACP 바인딩을 엽니다. |
| `q skills` | Studio에서 Agent Skills를 엽니다. |
| `q mcp` | Studio에서 MCP 구성을 엽니다. |
| `q lsp` | Studio에서 언어 서버와 워크스페이스 루트를 엽니다. |
| `q ignore` | Studio에서 현재 저장소의 `.qignore` 편집기를 엽니다. |
| `q help` | Studio Help를 엽니다. |
| `q acp [flags]` | q를 stdin/stdout ACP 서버로 실행합니다. |

`q agents`는 `q subagents`의 호환 별칭입니다.

## 필수 키

| 키 | 동작 |
| --- | --- |
| `Enter` / `Ctrl+S` | 현재 메시지를 보냅니다. |
| `Shift+Enter` | 새 줄을 삽입합니다. |
| `Ctrl+O` | 도구 결과 본문을 접거나 펼칩니다. |
| `Ctrl+G` | 서브에이전트 추적을 접거나 펼칩니다. |
| `Ctrl+H` | 도움말을 열거나 닫습니다. |
| `Ctrl+C` | 활성 턴을 중단하고, 유휴 상태에서는 종료합니다. |
| `Esc` | 현재 화면을 나가거나 채팅을 종료합니다. |
