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
| `~/.q/config.yaml` | 기본 모델, 역할, 컨텍스트, Loom, LSP 구성 |
| `~/.q/providers.json` | 관리형 Gateway 공급자와 모델 메타데이터 |
| `~/.q/gateway.json` | 독립 Gateway 리스너와 키 메타데이터 |
| `~/.q/gateway.key` | Gateway API 키 검증용 비공개 마스터 키 |
| `~/.q/remote.json` | Remote 리스너, 인증 스위치, 키 메타데이터 |
| `~/.q/remote.key` | Remote API 키 검증용 비공개 마스터 키 |
| `~/.q/library.json` | Global Library 루프백 리스너 설정 |
| `~/.q/workspace-memory.json` | Workspace Memory 루프백 리스너 설정 |
| `~/.q/usage.json` | 토큰 Usage 서비스 루프백 리스너 설정 |
| `~/.q/mcp.json` | 외부 MCP 프로필과 역할 할당 |
| `~/.agents/skills/` | q가 관리하지 않는 휴대 가능한 전역 Agent Skills |
| `~/.q/skills/` | q가 관리하는 전역 Agent Skills |
| `~/.q/subagents/` | 전역 사용자 정의 서브에이전트 프로필 |
| `~/.q/logs/thinker/` | 단기 Thinker 호출 진단 |
| `~/.q/usage/usage.sqlite` | 최근 토큰 이벤트와 일별 집계 |
| `~/.q/usage/archive/` | 오래된 원시 사용량 이벤트의 Parquet 아카이브 |

일반 구성에는 TUI를 사용하세요. 자동화가 필요할 때만 이 파일을 직접 편집하는 것이 좋습니다.

## 워크스페이스 상태

| 경로 | 용도 |
| --- | --- |
| `.q/sessions/<uuid>/session.json` | 대화, 컨텍스트, 제목, 생명주기, 학습 상태 |
| `.q/sessions/<uuid>/plan-execution.json` | 재개 가능한 승인 계획 체크포인트 |
| `.q/plan-executions/` | 완료된 실행 스냅샷 |
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

Loom GC는 구성된 유예 기간에 따라 활성 세션 프로젝션과 계획 체크포인트가 참조하는 아티팩트를 보호합니다. 워크스페이스 `.q` 상태를 삭제하면 q의 영구 기록이 사라지지만 저장소 파일을 되돌리지는 않습니다.
