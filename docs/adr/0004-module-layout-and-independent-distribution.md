# ADR-0004: 공개 `pkg/` 계층, 공유 코어 및 `llm`/`cdp` 독립 배포 모듈 경계

- **상태:** 확정 및 구현·검증 완료 (Accepted & Verified — 단일 루트 `go.mod` + `desktop/go.mod` 분리)
- **작성일:** 2026-11-28
- **관련 문서:** [PRD (`REQ-LLM-01`, `REQ-UI-01`)](../prd.md#42-%EA%B8%B0%EB%8A%A5-%EB%B0%8F-%EB%B9%84%EA%B8%B0%EB%8A%A5-%EC%9A%94%EA%B5%AC%EC%82%AC%ED%95%AD), [Blueprint 2장·6장·7장](../blueprint.md#7-%ED%94%84%EB%A1%9C%EC%A0%9D%ED%8A%B8-%EA%B5%AC%EC%84%B1-%ED%99%95%EC%A0%95--%EC%84%B8%EB%B6%80-%ED%8C%8C%EC%9D%BC-%EB%B0%B0%EC%B9%98%EB%8A%94-%EA%B5%AC%ED%98%84-%EC%8B%9C-%EB%AF%B8%EC%84%B8-%EC%A1%B0%EC%A0%95)
- **참조 근거:** `snowmerak/llm-provider/go.mod` (직접 의존성 4개), `snowmerak/cdp-q-driver/go.mod` (직접 의존성 2개), `snowmerak/q` (`main`) `go.mod` · `docs/studio-desktop.md`

---

## 1. 맥락 및 동인 (Context & Drivers)

q v2 프로젝트 구성에 대해 다음 방향이 요구되었습니다.

1. **공개 라이브러리 중심 `pkg/` 계층화:** 외부 Go 프로젝트에서 import할 수 있도록 공개 패키지로 설계하되, Q v1처럼 루트 디렉터리에 ~40개 패키지가 나열되지 않도록 `pkg/` 아래 계층화합니다.
2. **코어 로직 공유 (`cmd/q` + `desktop/`):** `cmd/q`(Server/CLI)와 `desktop/`(Wails v3)이 동일한 공개 코어 패키지(`pkg/app`)를 인프로세스로 직접 공유합니다. 단, `desktop/`은 별도 `desktop/go.mod`로 분리해 WebView2/WKWebView/GTK4/WebKitGTK 네이티브 의존성이 코어 모듈에 유입되지 않게 합니다.
3. **`web/dist` 비커밋:** 프런트엔드 빌드 산출물은 Git에 넣지 않고 빌드/릴리스 시 생성해 `pkg/app`에 임베드합니다.
4. **`llm-provider` 및 `cdp-q-driver` 레포 내 통합 + 독립 배포:** 두 프로젝트의 코드를 `snowmerak/q` (`v2`) 저장소 안으로 통합해 Q 코어가 직접 사용하면서도, 외부에서 독립 배포 가능한 공개 라이브러리(`pkg/llm`, `pkg/cdp`)와 별도 CLI(`cmd/llm-provider`, `cmd/cdp-q-driver`)를 제공합니다.

---

## 2. 검토한 모듈 분리 대안 (Considered Options)

### 옵션 B: 단일 루트 `go.mod` (`github.com/snowmerak/q/v2`) + `desktop/go.mod`만 분리 *(채택)*
- **결정:** 루트 모듈 `github.com/snowmerak/q/v2` 하나에 `cmd/{q, llm-provider, cdp-q-driver}`와 `pkg/*`를 모두 포함하고, Wails v3 네이티브 GUI 의존성을 가진 `desktop/`만 별도 `desktop/go.mod`로 분리합니다.
- **채택 사유:**
  - 단일 버전 태그(`v2.x.y`)와 단일 `go.mod` 체계로 개발·테스트·릴리스 워크플로를 단순하게 유지할 수 있습니다.
  - `cmd/llm-provider`와 `cmd/cdp-q-driver`는 Go 컴파일러·링커가 진입점에서 도달 가능한 패키지만 링크하므로, 독립 CLI 바이너리(`go install github.com/snowmerak/q/v2/cmd/llm-provider@latest` 등)에는 DB·검색 엔진 바이너리가 포함되지 않고 독립 배포됩니다.
- **감수하는 비용:** 외부 Go 프로젝트가 `github.com/snowmerak/q/v2/pkg/llm`이나 `pkg/cdp`를 라이브러리로 import하면, 소비자의 `go.mod` MVS(Minimum Version Selection) 그래프에는 루트 `go.mod`의 전체 의존성(DB·검색·MCP 등)이 간접 의존성으로 노출됩니다(단, 미사용 패키지는 최종 바이너리에 컴파일·링크되지 않음).

### 옵션 A: 하이브리드 멀티모듈 (`go.work` + `pkg/llm/go.mod`, `pkg/cdp/go.mod`, `desktop/go.mod`) *(기각)*
- **장점:** `llm-provider`(직접 의존성 4개)와 `cdp-q-driver`(직접 의존성 2개)를 별도 하위 Go 모듈로 두면 외부 Go 소비자의 `go.mod`에 루트 코어 의존성이 전혀 유입되지 않습니다.
- **기각 사유:** 1인/단일 레포 중심 개발 단계에서 `pkg/llm/v...`, `pkg/cdp/v...`, 루트 `v2.x.y` 등 다중 모듈 태그 관리와 `go.work` / `GOWORK=off` 동기화 비용에 비해 실익이 크지 않아 단순한 단일 루트 `go.mod`(옵션 B)를 선택했습니다. 추후 외부 라이브러리 소비자의 `go.mod` 경량화가 실제로 중요해지면 별도 ADR로 전환할 수 있습니다.

---

## 3. 확정된 구조 규칙 (Accepted Structural Rules)

1. **모듈 경계:**
   - 루트 모듈: `github.com/snowmerak/q/v2` (`go.mod`) — `cmd/*`, `pkg/*`, `internal/*` 포함
   - 데스크톱 셸 모듈: `desktop/go.mod` — Wails v3 의존성 격리, `pkg/app` 직접 import (로컬 개발/빌드 시 `replace` 또는 `go.work` 활용)
2. **디렉터리 경계:**
   - `cmd/{q, llm-provider, cdp-q-driver}`: 실행 바이너리 진입점
   - `pkg/{app, domain, runtime, task, memory, knowledge, storage, llm, cdp}`: 공개 라이브러리 계층
   - `internal/`: 외부에 노출하지 않는 최소 내부 헬퍼
   - `web/`: React + TypeScript + Vite + Tailwind + shadcn/ui (`dist/`는 `.gitignore`)
   - `desktop/`: Wails v3 셸
3. **단방향 의존성(역참조 금지) 규칙:**
   - `pkg/llm`과 `pkg/cdp`는 상위 Q 코어 패키지(`pkg/app`, `pkg/runtime`, `pkg/task`, `pkg/memory`, `pkg/knowledge`, `pkg/storage`)를 **절대 import하지 않습니다.** (독립 CLI 빌드 경량화 및 패키지 간 결합 차단 보장)
   - `pkg/domain`은 외부 DB/웹 프레임워크를 import하지 않습니다.

---

## 4. 결과 및 확인 기준 (Consequences & Verification)

- 단일 루트 모듈과 `desktop/go.mod` 분리 전략이 확정되었습니다.
- `pkg/llm`과 `pkg/cdp`의 역참조 금지를 정적 검사로 강제하면, 추후 하위 모듈 분리가 필요해지더라도 코드 이동 없이 `go.mod`만 추가해 전환할 수 있습니다.

### 확인 기준
- [x] `go list -deps ./pkg/llm` 및 `go list -deps ./pkg/cdp` 결과에 `github.com/snowmerak/q/v2/pkg/{app,runtime,task,memory,knowledge,storage}`가 포함되지 않을 것.
- [x] `cmd/q`, `cmd/llm-provider`, `cmd/cdp-q-driver`가 각각 독립 바이너리로 빌드될 것.
- [x] `desktop/`이 `pkg/app`을 직접 import해 Wails v3 바이너리를 빌드하되, 루트 `go.mod`에는 Wails 의존성이 추가되지 않을 것.
