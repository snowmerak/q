# CDP 기반 Web Search

2026-10-16. 기존 순차 조사 흐름의 페이지 읽기를 Chrome/CDP로 교체했다.
Scour 후보 검색 → System One 페이지 선택 → 렌더링된 본문 분류 → 누적 근거 종료
판정은 그대로다. Raw browser 도구는 등록하지 않으며 내부 CDP 읽기만 사용한다.

## 브라우저와 설정

- 같은 Q 프로세스의 tool runtime들은 Chrome 프로세스 하나를 공유한다.
- 검색마다 incognito context와 페이지를 하나씩 소유한다. 검색 내 페이지 이동은 쿠키를
  유지하고, 다른 검색·세션에는 쿠키가 전달되지 않는다.
- Settings → Web Search의 동시 읽기 한도는 1–8, 기본 2다. 새 검색이 설정을 읽어
  프로세스 공용 한도를 갱신한다. 이미 시작한 읽기는 끝내고 새 읽기에 한도를 적용한다.
- 모델 판정 중에는 읽기 슬롯을 반환한다. 검색 context는 검색 종료까지 유지된다.
  따라서 이 설정은 읽기 동시성이지 상주 context 수나 모델 동시 요청 수가 아니다.
- 페이지 읽기는 슬롯을 얻은 뒤 최대 30초다. DOM 준비와 짧은 안정화를 기다리고,
  일반적인 이미지·미디어·폰트 URL 패턴을 차단한다. 본문은 최대 250,000자로 제한한다.
  HTTP 오류 상태가 제공되면 근거로 채택하지 않고 오류를 반환한다.
- 마지막 tool runtime이 닫히면 Chrome도 종료한다. 검색 context가 모두 닫힌 뒤에는
  2분 유휴 만료도 적용한다. 별도 OS 프로세스는 각각의 pool을 가진다.

탐색/근거 선택과 완료 판정의 System One 모델 UI를 Web Search로 옮겼다. 저장 위치와
역할 키는 기존 `systemone.json`을 유지해 기존 선택값을 그대로 사용한다.
`config.yaml`의 `web_search`에는 `max_concurrent_reads`, `generative_model`을 저장한다.
생성형 모델은 이후 기능을 위한 명시적 선택값이며 **현재 검색에서 호출하지 않는다.**
활성 채팅 모델로 대체하지도 않는다. 선택적 설정 가져오기/내보내기에 함께 포함된다.

## 검증

`go test ./webbrowser -run 'TestReader|TestSharedReader' -count=1`은 실제 설치된
Chrome으로 쿠키를 요구하는 동일 URL 307, JavaScript 이동, 검색 간 쿠키 격리,
호출 취소 후 재사용, 검색 사이 프로세스 재사용, 두 페이지 동시 읽기, 한도 변경과
대기 취소, 마지막 runtime의 pool 정리를 확인한다. Chrome이 없으면 브라우저 검증은 skip이다.

실제 네트워크 검증은 `Q_TEST_CDP_WEB=1 go test ./webbrowser -run
TestReaderPoolOfficialSites -v -count=1`로 별도 실행한다. 이번 실행에서는 다음을 확인했다.

- 외교부의 기존 307 반복 URL은 `www.mofa.go.kr` 열린장관실 본문으로 이동했다.
- 국토부의 기존 이동 페이지는 JavaScript 실행 후 `m_36398/DTL.jsp` 장관소개 본문으로 이동했다.

이는 읽기 경로 검증이며 현직 인명 사실 판정이나 모든 사이트 접근 성공을 보장하지 않는다.
DNS·인증서·사이트 차단과 종료 모델의 오판은 CDP 전환과 별개의 실패 원인으로 남는다.

## 내부 실행 로그

Studio의 `~/.q/logs/studio/*.jsonl`에 기본 INFO 수준으로 검색 내부 진행도 기록한다.
`request_id`, `session_id`, `run_id`, `tool_call_id`를 도구 RPC 너머로 전달하고,
각 검색에는 `search_id`, 배치에는 `search_batch_id`와 `search_task_id`가 붙는다.
모델을 빌려 쓰지 않는 검색도 로그 문맥을 전달한다. 직접 Go 임베더는
`diagnostics.WithLogger`로 명시적으로 제공하지 않으면 기존처럼 조용히 동작한다.

- `web_search.metasearch.*`: 검색 시작/완료, 엔진별 결과 수·실패 분류.
- `web_search.candidate.*`: 후보 URL·순위·엔진, 중복/잘못된 URL 제외, 선택 결과.
- `web_search.page.*`: 읽기 시작, 요청/도착 URL, HTTP 상태, 본문 크기,
  페이지 처리 결과·검사/수집 묶음 수·잘림 여부·소요 시간.
- `web_search.browser.*`: 읽기 슬롯 대기/입장, 현재 읽기 수와 한도, Chrome 재사용 여부.
- `web_search.model.*`: 판정 역할·모델, 슬롯 대기/호출 시작/완료, 입력 byte 수,
  대기·호출 시간, keep/discard 또는 supported/missing 등 선택 개수와 실패 분류.
- `web_search.classification.progress`, `web_search.coverage.completed`:
  본문 분류 진행과 누적 근거 충족 여부. 마지막 `web_search.completed`에 종료 사유를 남긴다.

목표·검색어·후보 스니펫·원문·모델 프롬프트·쿠키/헤더는 기록하지 않는다. 검색어는
식별용 해시와 크기만 남긴다. URL에서도 인증정보, fragment, 세션 경로 매개변수와
일반 쿼리값을 제거하며 숫자 문서 식별자만 유지한다. `url_id`로 삭제 전 URL을 구분하고,
정확한 주소와 원문은 기존 세션/Loom 기록에서 확인할 수 있다. 실패는 원문 오류 대신
DNS, TLS, 리디렉션 한도, 시간초과, 모델 HTTP 상태 등의 제한된 코드로 기록한다.
기존 회전·보관 한도와 비동기 sink를 그대로 사용하며 본문 한 묶음마다 로그를 남기지 않는다.

회귀 테스트는 단계 연결, 병렬 과제의 검색 ID 분리, 민감한 본문/URL 값의 제외,
호출 모델 없이도 MCP를 통해 Studio sink 파일까지 문맥이 전달되는 것을 확인한다.

프런트엔드는 Browser plugin이 없어 기존 Playwright fixture로 검증했다. Web Search의
모델 활성화 조건, 읽기 한도 저장, 생성형 선택값 저장 및 새로고침, System One에서의
검색 설정 UI 제거, 데스크톱·모바일 폭에서의 표시를 확인한다.

검증 결과: 관련 Go 패키지·MCP 설정 경로·실제 Chrome 읽기 테스트, svelte-check,
프로덕션 프런트엔드 빌드, Web Search UI 2건과 설정 전송 UI 4건이 통과했다.
go vet와 golangci-lint(2.14.0)도 통과했다. govulncheck는 설치되어 있지 않아 미실행이다.
Studio 전체 테스트에서는 기존 `TestStudioEmbeddingChangeAppliesAfterAllSessionTurnsFinish`가
Windows의 `config.yaml` 공유 잠금 오류로 실패했다. 마지막 커밋의 소스를 Go overlay로
대조해도 동일하게 재현됐으며, 해당 기존 테스트를 제외한 Studio 테스트는 통과했다.

## 출처 처리 변경

`allowed_domains` 입력과 후보·이동 후 도메인 필터를 제거했다. 출처 평가는 읽기나
근거 수집을 차단하지 않고, 필요하면 `cross_check_suggestions`에 해당 URL의
독립 출처 교차 확인을 제안한다. 제안만으로 완료 판정을 실패시키지 않는다.
사용자가 명시한 공식 자료만의 검증 조건은 최종 완료 판정에서 계속 확인한다.

원문과 CDP 추출 데이터는 변경하지 않는다. 짧은 문단을 1,600바이트 덩어리로
재결합하던 동작을 없애고 각 문단을 별도 keep/discard 질문으로 판정한다.
인접 문단은 최대 400바이트씩 해석 문맥으로만 제공하며 자동으로 근거에 저장하지
않는다. 광고·관련 기사·메뉴의 주제어 일치만으로 채택하지 않도록 판정 지침을
명시한다. 하나의 요청에는 최대 네 문단을 넣되 각 선택과 저장은 독립적이다.
