---
locale: ko
title: 위임 작업
description: 직업형 서브에이전트로 요구사항, 조사, 구현과 검토를 조율합니다.
sectionLabel: 가이드
toc:
  - id: 작업-시작
    label: 작업 시작
  - id: 역할과-모델
    label: 역할과 모델
---

## 작업 시작

워크스페이스 세션에서 원하는 결과를 설명하세요. default loop는 workspace 도구를 직접 사용하면서 manager에게 요구사항과 계획을, research에게 조사를, senior developer에게 기술 작업이나 검토를 맡길 수 있습니다. 에이전트를 거치는 순서는 고정되어 있지 않습니다.

PM 업무만 요청하려면 채팅에서 `/subagent builtin/manager <request>`를 사용하세요.

## 역할과 모델

manager는 PM 직책으로 요구사항, 우선순위, 수용 기준과 필요한 작업 계획을 맡습니다. interviewer는 사용자 결정이 필요한 질문을 가려내고 research는 특정 문제를 조사합니다. 각 역할은 필요한 워크스페이스 근거를 직접 읽을 수 있습니다.

senior developer는 `reviewer` 모델 역할과 수정·명령 도구를 사용합니다. 직접 코드를 수정하거나 junior developer에게 범위가 정해진 구현을 맡길 수 있으며, 실제 변경과 검증 결과를 검토하고 필요하면 수정을 요청합니다. junior developer도 `coder` 모델 역할과 수정·명령 도구를 사용합니다.
