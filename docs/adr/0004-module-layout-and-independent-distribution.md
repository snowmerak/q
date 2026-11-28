# ADR-0004: 공개 `pkg/` 계층, 공유 코어 및 `llm`/`cdp` 독립 배포 모듈 경계

- **상태:** 부분 확정 / 모듈 분리 방식 제안 (Accepted Layout & Shared Core / Proposed Submodule Strategy — M1 착수 시 최종 확정)
- **작성일:** 2026-11-28
- **관련 문서:** [PRD (`REQ-LLM-01`, `REQ-UI-01`)](../prd.md#42-%EA%B8%B0%EB%8A%A5-%EB%B0%8F-%EB%B9%84%EA%B8%B0%EB%8A%A5-%EC%9A%94%EA%B5%AC%EC%82%AC%ED%95%AD), [Blueprint 2장·6장·7장](../blueprint.md#7-%ED%94%84%EB%A1%9C%EC%A0%9D%ED%8A%B8-%EA%B5%AC%EC%84%B1-%ED%99%95%EC%A0%95--%EC%84%B8%EB%B6%80-%ED%8C%8C%EC%9D%BC-%EB%B0%B0%EC%B9%98%EC%99%80-%EB%A9%80%ED%8B%B0%EB%AA%A8%EB%93%88-%EC%97%AC%EB%B6%80%EB%8A%94-m1%EC%97%90%EC%84%9C-%EB%AF%B8%EC%84%B8-%EC%A1%B0%EC%A0%95)
- **참조 근거:** `snowmerak/llm-provider/go.mod` (직접 의존성 4개), `snowmerak/cdp-q-driver/go.mod` (직접 의존성 2개), `snowmerak/q` (`main`) `go.mod` · `docs/studio-desktop.md`

---

## 1. 맥락 및 동인 (Context & Drivers)

q v2 프로젝트 구성에 대해 다음 사항이 확정되었습니다.

1. **공개 라이브러리 중심 `pkg/` 계층화:** 외부 Go 프로젝트에서 import할 수 있도록 공개 패키지로 설계하되, Q v1처럼 루트 디렉터리에 ~40개 패키지가 나열되지 않도록 `pkg/` 아래 계층화합니다.
2. **코어 로직 공유 (`cmd/q` + `desktop/`):** `cmd/q`(Server/CLI)와 `desktop/`(Wails v3)이 동일한 공개 코어 패키지(`pkg/app`)를 인프로세스로 직접 공유합니다. 단, `desktop/`은 별도 `desktop/go.mod`로 분리해 WebView2/WKWebView/GTK4/WebKitGTK 네이티브 의존성이 코어 모듈에 유입되지 않게 합니다.
3. **`web/dist` 비커밋:** 프런트엔드 빌드 산출물은 Git에 넣지 않고 빌드/릴리스 시 생성해 `pkg/app`에 임베드합니다.
4. **`llm-provider` 및 `cdp-q-driver` 레포 내 통합 + 독립 배포:** 두 프로젝트의 코드를 `snowmerak/q` (`v2`) 저장소 안으로 통합해 Q 코어가 직접 사용하면서도, 외부에서 독립 배포 가능한 공개 라이브러리(`pkg/llm`, `pkg/cdp`)와 별도 CLI(`cmd/llm-provider`, `cmd/cdp-q-driver`)를 제공합니다.

남은 핵심 설계 선택은 **`pkg/llm`과 `pkg/cdp`를 루트 `go.mod` 하나에 둘지, 독립 하위 `go.mod` 멀티모듈로 분리할지**입니다.

---

## 2. 확정된 구조 규칙 (Accepted Structural Rules)

1. **디렉터리 경계:**
   - `cmd/{q, llm-provider, cdp-q-driver}`: 실행 바이너리 진입점
   - `pkg/{app, domain, runtime, task, memory, knowledge, storage, llm, cdp}`: 공개 라이브러리 계층
   - `internal/`: 외부에 노출하지 않는 최소 내부 헬퍼
   - `web/`: React + TypeScript + Vite + Tailwind + shadcn/ui (`dist/`는 `.gitignore`)
   - `desktop/`: `desktop/go.mod`를 가진 Wails v3 셸(`pkg/app` 직접 import)
2. **단방향 의존성(역참조 금지) 규칙:**
   - `pkg/llm`과 `pkg/cdp`는 상위 Q 코어 패키지(`pkg/app`, `pkg/runtime`, `pkg/task`, `pkg/memory`, `pkg/knowledge`, `pkg/storage`)를 **절대 import하지 않습니다.**
   - `pkg/domain`은 외부 DB/웹 프레임워크를 import하지 않습니다.

---

## 3. 모듈 분리 방식 비교 (M1 착수 시 최종 확정 대상)

### 옵션 A: 하이브리드 멀티모듈 (`go.work` + `pkg/llm/go.mod`, `pkg/cdp/go.mod`, `desktop/go.mod`) *(제안 · 권장)*
- **근거:** 실제 `go.mod` 확인 기준 `llm-provider`는 직접 의존성이 4개(`go-oidc/v3`, `fsnotify`, `rueidis`, `x/sys`), `cdp-q-driver`는 2개(`chromedp/cdproto`, `chromedp/chromedp`)뿐인 매우 가벼운 모듈입니다. 반면 루트 `q` 코어는 RDB 드라이버 3종(SQLite/PG/MySQL), `bleve/v2`, HNSW, Goja, MCP SDK 등을 포함합니다.
- **장점:**
  - 외부 Go 프로젝트가 `pkg/llm`이나 `pkg/cdp`만 import할 때 `go.mod`에 DB·검색 엔진 의존성이 전혀 유입되지 않습니다.
  - `pkg/llm/v...`, `pkg/cdp/v...` 형태로 독립 버저닝·릴리스(`go install .../cmd/llm-provider@latest`)가 깔끔합니다.
- **단점/비용:**
  - 루트에 `go.work`를 두고, CI에서 `GOWORK=off`로 각 모듈(`.`, `./pkg/llm`, `./pkg/cdp`, `./desktop`)이 독립 빌드되는지 검증해야 하며 릴리스 태그 관리가 1개에서 3개로 늘어납니다.

### 옵션 B: 단일 루트 `go.mod` + `desktop/go.mod`만 분리
- **장점:**
  - 버전 태그(`v2.x.y`)와 `go.mod` 관리가 단순하며, `cmd/llm-provider`와 `cmd/cdp-q-driver` 바이너리는 Go 링커가 미사용 패키지를 제거하므로 바이너리 수준에서는 여전히 독립 배포됩니다.
- **단점/비용:**
  - 외부 Go 라이브러리가 `github.com/snowmerak/q/v2/pkg/llm`을 import하면 루트 `go.mod`의 전체 MVS(Minimum Version Selection) 의존 그래프(SQLite, PG, MySQL, Bleve 등)가 소비자의 `go.mod`/`go.sum`에 간접 의존성으로 끌려옵니다.

---

## 4. 결과 및 확인 기준 (Consequences & Verification)

- **현재 상태:** 디렉터리 배치, `pkg/app` 공유, `desktop/go.mod` 분리, `web/dist` 비커밋, 단방향 의존성 규칙은 **확정**되었습니다.
- **M1 착수 시 결정:** 외부 Go 라이브러리 소비자 관점의 의존성 경량화를 우선해 **옵션 A(하위 모듈 분리)**를 기본 제안으로 두되, M1 스캐폴딩 시작 시 태깅/릴리스 복잡도를 검토해 옵션 A 또는 B를 최종 확정하고 본 ADR 상태를 `확정(Accepted)`으로 갱신합니다.

### 확인 기준
- [ ] `go list -deps ./pkg/llm` 및 `go list -deps ./pkg/cdp` 결과에 `github.com/snowmerak/q/v2/pkg/{app,runtime,task,memory,knowledge,storage}`가 포함되지 않을 것.
- [ ] `cmd/q`, `cmd/llm-provider`, `cmd/cdp-q-driver`가 각각 독립 바이너리로 빌드될 것.
- [ ] `desktop/`이 `pkg/app`을 직접 import해 Wails v3 바이너리를 빌드하되, 루트 모듈 `go.mod`에는 Wails 의존성이 추가되지 않을 것.
