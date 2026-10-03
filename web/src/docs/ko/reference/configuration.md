---
locale: ko
title: 구성
description: q의 개인 설정, 워크스페이스 상태, 프로필, 재생성 가능한 인덱스 위치를 확인합니다.
sectionLabel: 참조
toc:
  - id: 개인-상태
    label: 개인 상태
  - id: 워크스페이스-상태
    label: 워크스페이스 상태
  - id: 원본-데이터
    label: 원본 데이터
---

## 개인 상태

| 경로 | 용도 |
| --- | --- |
| `~/.q/config.yaml` | 기본 모델, 역할, 모델별 API 모드, 컨텍스트, Loom, LSP 구성 |
| `~/.q/providers.json` | 관리형 Gateway 공급자와 모델 메타데이터 |
| `~/.q/gateway.json` | 독립 Gateway 리스너와 키 메타데이터 |
| `~/.q/gateway.key` | Gateway API 키 검증용 비공개 마스터 키 |
| `~/.q/systemone.json` | System One 공급자, 모델 라우팅, 리스너와 API 키 메타데이터 |
| `~/.q/systemone.key` | System One API 키 검증용 비공개 마스터 키 |
| `~/.q/library.json` | Global Library 루프백 리스너 설정 |
| `~/.q/workspace-memory.json` | Workspace Memory 루프백 리스너 설정 |
| `~/.q/usage.json` | 토큰 Usage 서비스 루프백 리스너 설정 |
| `~/.q/mcp.json` | 외부 MCP 프로필과 역할 할당 |
| `~/.agents/skills/` | q가 관리하지 않는 휴대 가능한 전역 Agent Skills |
| `~/.q/skills/` | q가 관리하는 전역 Agent Skills |
| `~/.q/subagents/` | 전역 사용자 정의 서브에이전트 프로필 |
| `~/.q/studio-sessions.json` | Studio에 등록한 루트 세션과 워크스페이스 위치 |
| `~/.q/logs/thinker/` | 단기 Thinker 호출 진단 |
| `~/.q/usage/usage.sqlite` | 최근 토큰 이벤트와 일별 집계 |
| `~/.q/usage/archive/` | 오래된 원시 사용량 이벤트의 Parquet 아카이브 |

일반 구성에는 Studio를 사용하세요. 자동화가 필요할 때만 이 파일을 직접 편집하는 것이 좋습니다.

Studio의 **Settings → Import / Export**에서 전역 설정을 옮길 수 있습니다. 팝업의 Models, Providers, System One, Runtime, Services, Subagents, Integrations 탭에서 항목을 선택하세요. 탭을 바꾸어도 선택은 유지되며, 선택한 항목을 하나의 JSON 파일로 내보냅니다.

가져오기는 파일 선택 → 항목 선택 → 변경 검토 → 적용 순서입니다. 같은 ID의 항목은 덮어쓰고, 선택하지 않은 항목은 유지합니다. 모델 그룹, ACP 연결, MCP 서버 등 필요한 참조가 없으면 적용 전에 오류를 표시하므로 관련 항목을 함께 선택하세요. 리스너 주소와 포트 변경은 해당 서비스가 재시작될 때 적용됩니다.

파일 형식은 `format: "q-settings"`, `version: 1`, `scope: "global"`, `sections`입니다. `sections`는 설정 영역별로 항목 ID와 데이터를 담습니다. 파일은 최대 4 MiB이며, API 키, 공급자의 헤더·본문·환경변수 맵과 ACP 환경변수 값은 제외됩니다. 기존 연결에 가져올 때 해당 비밀값은 유지됩니다. 워크스페이스 설정, Skills, `.qignore`는 이 전역 설정 파일에 포함되지 않습니다.

## 워크스페이스 상태

| 경로 | 용도 |
| --- | --- |
| `.q/sessions/<uuid>/session.json` | 대화, 컨텍스트, 제목, 생명주기, 학습 상태 |
| `.q/sessions/<uuid>/delegations.json` | 위임된 자식 호출의 북마크 |
| `.q/sessions/<uuid>/delegates/<invocation-id>/` | 자식 세션, 실행 상태, 중첩 위임 트리 |
| `.q/model.json` | 워크스페이스 모델 역할 오버라이드 |
| `.q/learning.json` | 워크스페이스 학습 스위치 |
| `.q/lsp.json` | 워크스페이스 LSP 루트와 오버라이드 |
| `.q/data/` 및 `.q/index/` | 영구 레코드와 파생 인덱스 |
| `.q/loom/` | 내용 주소 기반 도구 아티팩트와 GC 메타데이터 |
| `.agents/skills/` | q가 관리하지 않는 휴대 가능한 워크스페이스 Agent Skills |
| `.q/skills/` | q가 관리하는 워크스페이스 Agent Skills |
| `.q/subagents/` | 워크스페이스 사용자 정의 서브에이전트 프로필 |
| `AGENTS.md` | 워크스페이스 및 중첩 경로 지침 |
| `.qignore` | 검색 제외 규칙 |

## 원본 데이터

JSON 레코드가 원본입니다. Bleve와 HNSW 인덱스는 파생 데이터이며 다시 만들 수 있습니다.

Loom GC는 구성된 유예 기간에 따라 활성 세션 프로젝션과 저장된 레코드가 참조하는 아티팩트를 보호합니다. 워크스페이스 `.q` 상태를 삭제하면 q의 영구 기록이 사라지지만 저장소 파일을 되돌리지는 않습니다.
