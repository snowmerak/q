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

프런트엔드는 Browser plugin이 없어 기존 Playwright fixture로 검증했다. Web Search의
모델 활성화 조건, 읽기 한도 저장, 생성형 선택값 저장 및 새로고침, System One에서의
검색 설정 UI 제거, 데스크톱·모바일 폭에서의 표시를 확인한다.

검증 결과: 관련 Go 패키지·MCP 설정 경로·실제 Chrome 읽기 테스트, svelte-check,
프로덕션 프런트엔드 빌드, Web Search UI 2건과 설정 전송 UI 4건이 통과했다.
go vet와 golangci-lint(2.14.0)도 통과했다. govulncheck는 설치되어 있지 않아 미실행이다.
Studio 전체 테스트에서는 기존 `TestStudioEmbeddingChangeAppliesAfterAllSessionTurnsFinish`가
Windows의 `config.yaml` 공유 잠금 오류로 실패했다. 마지막 커밋의 소스를 Go overlay로
대조해도 동일하게 재현됐으며, 해당 기존 테스트를 제외한 Studio 테스트는 통과했다.
