---
locale: ko
title: q 설치
description: Go로 q를 설치하거나 소스에서 빌드한 뒤 원하는 워크스페이스에서 시작합니다.
sectionLabel: 가이드
toc:
  - id: 요구사항
    label: 요구사항
  - id: go로-설치
    label: Go로 설치
  - id: 소스에서-설치
    label: 소스에서 설치
  - id: 설치하지-않고-실행
    label: 설치하지 않고 실행
  - id: 워크스페이스에서-시작
    label: 워크스페이스에서 시작
---

## 요구사항

q를 설치하기 전에 다음 항목을 준비하세요.

- Go 1.26.5 이상
- `PATH`에서 실행 가능한 Git
- 구성된 모델 공급자 하나 이상
- ANSI 색상을 지원하는 터미널

[Task](https://taskfile.dev/)는 선택 사항입니다. 필요한 모든 빌드와 테스트 명령은 Go만으로 실행할 수 있습니다.

## Go로 설치

Go 모듈에서 q를 직접 설치합니다.

```powershell
go install github.com/snowmerak/q/cmd/q@latest
```

선택 사항인 `q-mcp` 동반 도구도 같은 방식으로 설치할 수 있습니다.

```powershell
go install github.com/snowmerak/q/cmd/q-mcp@latest
```

Go는 바이너리를 `GOBIN`에 기록하며, `GOBIN`이 없으면 `GOPATH/bin`을 사용합니다. 해당 디렉터리가 `PATH`에 포함되어 있어야 합니다.

## 소스에서 설치

현재 소스 트리를 빌드하거나 q에 기여하려면 저장소를 복제하세요.

```powershell
git clone https://github.com/snowmerak/q.git
cd q
go install ./cmd/q ./cmd/q-mcp
```

이 명령은 Go 모듈 프록시에서 `@latest`를 해석하지 않고 체크아웃된 소스에서 두 명령을 설치합니다.

## 설치하지 않고 실행

소스 체크아웃에서 q를 바로 시작할 수 있습니다.

```powershell
go run ./cmd/q
```

저장소에는 일반적인 개발 작업용 Task 대상도 있습니다.

```powershell
task run
task build
task test
```

## 워크스페이스에서 시작

q가 워크스페이스로 사용할 저장소나 디렉터리로 이동한 뒤 실행하세요.

```powershell
cd C:\path\to\project
q
```

첫 실행에서는 공급자 설정이 열립니다. 모델을 할당한 뒤 평소처럼 요청을 입력하거나 `/`를 입력해 명령을 찾아보세요.
