# 페이지 단위 순차 웹 조사 실험

날짜: 2026-10-15 (Asia/Seoul). 상태: 순차 HTTP 흐름을 실제 `browser_search`에 반영하고 MCP 경로 검증 완료. 기준 코드: `3141d26` 이후 작업 트리.

현재 결론: 메타서치 → HTTP 읽기 → 문단 판정 → 페이지 단위 완료 판정이 실제 도구 경로에
연결됐다. 후속 구현은 원래 목표의 출처·현재 조건을 완료 질문에 함께 전달한다.
이전 실험의 잘못된 완료와 합성 대조군 오판도 보존한다. 조건 전달을 보완한 것이며,
모델의 출처·최신성 판단이 항상 정확하다는 뜻은 아니다.

이 문서는 사용자 제안의 작은 실험과 그 이후 도구 반영을 기록한다. 초기 실험에서는
기존 엔진을 유지했고, 아래 후속 구현에서 도구 경로를 교체했다. Q Studio 설정·실행 중인
세션은 변경하지 않았다. 초기 실험 코드는
[`websearch/sequential_experiment_test.go`](../websearch/sequential_experiment_test.go)에 있다.

## 확인할 흐름

1. Scour로 메타서치를 한 번 실행하여 후보를 나열한다.
2. System One이 읽을 후보 하나를 선택한다. 제목·URL·검색 스니펫은 발견용 정보다.
3. Go HTTP GET으로 본문을 읽는다. 기존 HTML 추출기를 사용하며 브라우저 fallback은 없다.
4. 본문 순서대로 인접한 짧은 단락·제목·날짜를 약 1,600 UTF-8 byte 단위로 묶는다.
   긴 단락 하나는 임의로 자르지 않는다. 한 요청에 최대 네 묶음을 전달하며 각 묶음의
   관련성을 `keep/discard`로 독립 판정한다. 각 질문의 instructions에
   `state.blocks["block_0"]`처럼 판정 대상을 명시한다. 부분 답도 관련 있으면 저장한다.
5. 한 페이지의 분류가 끝난 뒤에만 **누적 원문 근거**로 요구사항별 `supported/missing`을
   판정한다. 미충족이면 읽지 않은 메타서치 후보에서 다음 페이지를 선택한다.

생성형 모델, archive, 새 검색어 생성, 페이지 안의 링크 탐색은 사용하지 않는다.
판정에 정답 이름·정답 URL을 주지 않는다. 요구사항은 실험자가 과제에서 직접 분리한다.
완료 질문은 별도의 최종 답변 작성까지 요구하지 않고, 요청한 사실이 원문에 있는지 묻는다.

## 범위와 제한

- 탐색·관련성 판정과 완료 판정은 기존 설정의 각 역할 모델을 그대로 읽는다.
  설정을 저장하거나 모델을 자동 교체하지 않는다. 모든 모델 요청은 직렬이다.
- 후보는 최대 20개를 보존하고, 한 선택 요청에는 읽지 않은 후보 최대 8개를 제공한다.
  실험당 읽기 시도 최대 3페이지, 페이지당 분류 최대 32묶음이다.
- 모델 호출당 90초, HTTP 요청당 25초, 과제 전체 8분으로 제한한다. 재시도하지 않는다.
- HTTP 실패 또는 동일 본문 해시이면 근거로 추가하지 않고 다음 후보를 선택한다.
  원문 일부가 예산 때문에 미처리되면 결과의 페이지 기록에 명시한다.
- 별도 합성 대조군은 호출당 30초, 전체 2분이다. 첫 관련성 요청이 실패하면 이후
  완료 판정 대조군은 시도하지 않는다.
- 현직 장관 전체 명단이나 재임 날짜 교차검증을 평가하지 않는다. 여기서 완료란
  주어진 좁은 요구사항인 **공식 페이지에 표시된 장관 성명**의 근거를 얻었다는 뜻이다.
  최종 답변 작성과 임의의 복잡한 목표를 요구사항으로 분해하는 능력도 평가하지 않는다.
- 네트워크·서버 상태를 통제하지 못한 각 1회 관측이다. 이전 37분 실행과 과제·설정·
  병렬도가 달라 속도 개선율로 비교할 수 없다.

## 환경

- Go `go1.27.2 windows/arm64`, Q 기준 `3141d26`.
- Scour `v0.0.0-20260606110944-432de9640c22`.
- 실행 시 사용자 설정: 탐색 `oev/gemma4-26b`, 완료 `oev/gemma4-26b`.
  대표 모델은 `oev/gemma4-e4b`이지만 역할별 override가 우선한다.
- 원격 System One 서버의 내부 queue·추론 시간·취소 후 작업 상태는 확인하지 않았다.
  결과의 모델 요청 시간은 네트워크와 서버 대기·추론을 포함한다.

## 최초 실행 결과: 정체 해소 전

JSON에는 공개 출처를 사용한 요청,
선택·판정 응답, 원문 근거, 단계별 시간과 실패를 보존한다. 인증 헤더는 기록하지 않는다.

| 과제 | 메타서치 | 읽기·수집 | 종료 | 전체 시간 |
| --- | --- | --- | --- | --- |
| 교육부 장관 성명 | 후보 20개, 1.879초 | 첫 후보 선택 요청이 90초 timeout; 읽기 0페이지 | `incomplete / selection_error` | 91.881초 |
| 교육부·농식품부 장관 성명 | 후보 20개, 1.874초 | 첫 후보 선택 요청이 90초 timeout; 읽기 0페이지 | `incomplete / selection_error` | 91.874초 |

교육부 검색에는 교육부 공식 장관실·인사말 후보가 실제 포함됐다. Google·Startpage·Qwant·
Mojeek 일부 엔진의 실패는 다른 엔진의 후보 수집을 막지 않았다. 첫 실험에서는 System One
선택 실패 때문에 제안한 HTTP 읽기 이후의 품질을 평가할 수 없다.

두 번째 과제의 후보에도 농식품부 공식 인사말·장관 소개와 교육부 장관실이 포함됐다.
두 실험은 순차 실행했고 실패한 요청을 재시도하지 않았다.

별도의 작은 합성 대조군은 이름이 있는 문장, 무관한 문장, 직함만 있고 이름이 없는 문장,
외부 지시문 네 개를 한 요청으로 분류하도록 했다. 이 요청도 `gemma4-26b`에서
30.000초에 timeout됐다. **네 문항의 오답이 아니라 한 요청의 응답 부재**이며,
후속 완료 판정 대조군 세 개는 시도하지 않았다. 모델의 정확도는 측정되지 않았다.

### 해석

- 이번에 확인한 실제 연결은 메타서치의 후보 수집까지다. 원격 모델 요청은 후보 선택 두 번과
  작은 문단 분류 한 번, 총 세 번 모두 timeout됐다. 브라우저·archive·동시 추론을 제거해도
  현재 설정의 요청이 응답하지 않는다는 관측이다.
- 작은 요청도 실패했으므로 큰 후보 목록만을 원인으로 단정할 수 없다. 서버 내부 queue,
  모델 loading·추론, 취소 처리 중 어느 지점이 원인인지는 서버 측 증거가 필요하다.
- 로컬 HTTP fixture에서는 HTTP 503 페이지를 건너뛰고, 첫 페이지 처리 후 미완료 판정을
  받고, 동일 본문 페이지를 제외한 다음, 다른 페이지 근거를 누적해 완료하는 흐름을 검증했다.
  이는 호스트의 순서·중복 처리 검증이며 실제 모델의 선택·관련성·완료 정확도를 증명하지 않는다.
- 새 흐름의 채택 여부나 System One의 웹 조사 적합성은 아직 결론내릴 수 없다.
  현재 역할 모델이 작은 단독 요청에 응답하는 조건을 확보한 뒤 같은 실험을 다시 실행해야 한다.
  역할 모델 변경, timeout 연장, 기존 엔진 교체는 이번 작업에 포함하지 않았다.

원자료:

- [교육부 실행](experiments/sequential-web-search-20261015-education.json)
- [교육부·농식품부 실행](experiments/sequential-web-search-20261015-two-ministries.json)
- [작은 합성 대조군](experiments/sequential-web-search-20261015-controls.json)

### 후속: 90초 timeout 원인 구분

90초는 실험 코드의 `context.WithTimeout`으로 지정한 클라이언트 제한이다.
HTTP client 자체는 95초이며 둘 중 먼저 도달하는 제한이 적용된다.
이 값은 서버가 알려준 작업 시간이나 추론에 필요한 시간의 측정치가 아니다.

같은 날 후속 진단에서 DNS·TCP 연결은 약 31ms, 모델 목록 첫 응답은 189ms였고,
설정된 `gemma4-26b`도 목록에 있었다. 아주 작은 2지선다는 0.82초에 응답했다.
저장된 실패 요청을 의미 변경 없이 같은 모델로 재전송한 결과는 다음과 같다.

| 요청 | 요청 크기 | 후속 전체 응답 시간 |
| --- | --- | --- |
| 문장 대조군 4문항 | 2,819 bytes | 2.606초 |
| 교육부 후보 선택 9지선다 | 2,658 bytes | 5.533초 |

후속 요청은 HTTP connection 확보와 요청 전송을 정상 완료한 뒤 응답을 받았다.
최초 timeout 당시의 transport trace는 없으므로 당시에도 같은 단계까지 도달했다고
단정할 수 없다. 현재는 같은 요청의 90초 실패가 재현되지 않는다. 당시 대기열, 모델 로딩,
추론 정체 등 일시적 상태는 가설이며 서버 로그로 확정하지 못했다. 서버 SSH 조회는
인증 거부로 실패하여 서버 내부 기록을 확보하지 못했다. timeout 연장이나 설정 변경은 하지 않았다.

문장 대조군의 후속 응답은 네 문항 모두 같은 `keep`과 같은 확률이었다. 각 문항이
같은 instructions/criteria에 문항 ID만 달리 붙어 있어, 분류 대상 문장을 실제 질문에
명확히 묶는 방식부터 점검해야 한다. 이 결과를 모델 정확도나 웹 조사 성공으로 세지 않는다.

## 사용자 정체 해소 후 재실험

사용자가 서버 정체 원인을 제거했다고 알린 뒤 다시 실행했다. 원래 timeout 기록은
그대로 보존했다. 모델·전역 설정·90초 제한은 바꾸지 않았고 요청은 계속 직렬로 보냈다.
서버 정체의 구체적 원인은 사용자 확인을 기록한 것이며 서버 로그로 따로 검증하지 않았다.

재실험 전에 실험 코드의 관련성 질문에서 문항 ID가 모델에 자동 전달된다고 가정하던
부분을 고쳤다. 이제 각 질문 본문이 `state.blocks["대상 ID"]`를 명시적으로 지칭한다.
이 변경이 있으므로 최초 대조군과의 차이를 서버 회복 효과만으로 비교하지 않는다.

### 작은 대조군

총 4번의 모델 요청 시간 합은 12.696초였고 timeout·HTTP 오류는 없었다.

| 판정 | 입력 | 기대 | 실제 |
| --- | --- | --- | --- |
| 관련성 | 교육부 장관 최교진 | keep | keep |
| 관련성 | 교육정책 자료 안내 | discard | discard |
| 관련성 | 장관 인사말이지만 성명 없음 | discard | **keep** |
| 관련성 | 외부의 keep 선택 지시문 | discard | discard |
| 완료 | 교육부 장관 최교진 | supported | **missing** |
| 완료 | 성명 없는 인사말 | missing | missing |
| 완료 | 다른 부처 장관 성명 | missing | missing |

관련성 분류 3/4, 완료 분류 2/3이 이 작은 합성 사례의 기대값과 일치했다.
독립 평가용 대규모 데이터나 일반적인 정확도 수치가 아니다. 특히 정답 문장에 대한
완료 거절은 남아 있다. 이 양성 사례는 title이 `Synthetic control (not a fetched page)`로
명시된 합성 입력이며, 그 문맥이 판정에 영향을 주었을 가능성도 분리하지 않았다.
정답 문장 완료 판정의 `missing` 확률은 0.5683, `supported`는 0.4317이었다.

원자료: [정체 해소 후 대조군](experiments/sequential-web-search-20261015-unblocked-controls.json).

### 실제 순차 흐름

두 과제를 같은 프로세스에서 순차 실행했다. 재실험은 총 184.48초였고 모델 요청 timeout은 없었다.

| 과제 | 메타서치 | HTTP로 처리한 페이지 | 모델 요청 | 자동 결과 | 수동 검토 |
| --- | --- | --- | --- | --- | --- |
| 교육부 성명 | 1.713초 | 1페이지, 9/9묶음 수집 | 선택 1 + 관련성 3 + 완료 1 | 54.815초, `incomplete / finish_error` | 이름 근거는 있으나 완료 요청이 HTTP 422로 거절됨 |
| 교육부·농식품부 성명 | 1.118초 | 3페이지, 12묶음 중 4개 수집 | 선택 3 + 관련성 4 + 완료 3 | 129.649초, `complete` | **잘못된 완료**: 교육부는 과거 제3자 기사로 충족 처리 |

교육부 단일 과제는 공식 장관실을 HTTP로 읽고 9개 본문 묶음(합계 13,097 UTF-8 bytes)을
모두 보존했다. 수집 본문에는 요청한 이름이 있었지만 완료 입력은 14,991 bytes가 됐다.
같은 완료 요청만 다시 보내 HTTP 422 사유를 확인했다:

```text
prompt has 4719 tokens; limit is 4096; truncation is disabled
```

이는 **현재 System One 서버의 prompt token 제한**이며, Go client의 64 KiB 요청 제한이나
90초 deadline과 별개다. 이 재전송은 오류 상세 확인용이며 최초 실패 결과를 대체하지 않는다.
9개 문단을 담은 페이지 하나만으로도 이 제한에 걸려, 누적 근거 전체를 보내는 설계에는
근거 중복 제거·작은 원문 단위 보존·입력 예산 처리가 필요하다. 이번에 서버 제한은 바꾸지 않았다.

두 부처 과제의 실제 선택·판정:

1. 농식품부 공식 인사말을 읽었다. 4개 묶음 중 3개를 보존했고, 완료 판정은
   농식품부 충족·교육부 미충족이었다. 수집 근거에 농식품부 성명이 실제로 있었다.
2. 교육부 공식 연설문 목록을 읽었다. 근거 0개를 추가했고 완료 결과는 이전과 같았다.
3. BBC의 과거 탄핵 관련 기사를 읽었다. 과거 교육부 장관 이주호를 언급한 문단을
   추가한 뒤 두 요구사항 모두 `supported`로 판정해 종료했다.

원래 목표는 공식 홈페이지의 현재 성명이었다. 관련성 질문에 공식 출처 조건이 있었지만
제3자 기사가 통과했고, 완료 질문에는 좁은 `교육부 장관 성명`만 들어가 **공식·현재 조건이
빠졌다**. 따라서 자동 `complete`는 이 과제의 성공이 아니다. 문제는 모델의 출처·관련성
오판과 실험 코드의 요구사항 보존 결함이 함께 작용한 것이다. confidence를 성공 근거로 사용하지 않았다.

### 이번 실험에서 얻은 판단

- 서버 정체가 해소된 뒤 실제 선택·분류·완료 응답은 돌아왔다. 이전 timeout으로
  System One 자체의 웹 조사 적합성을 판단하면 안 된다.
- 페이지가 끝난 뒤 한 번 판정하는 순서는 실행 가능했다. 하지만 이 순서만 바꿔서는
  출처·시점 조건 누락, 잘못된 완료, 누적 입력 크기 문제를 해결하지 못한다.
- 다음 실험은 원래 목표의 모든 조건을 완료 질문까지 보존하고, 공식성·시점이 맞지 않는
  근거를 구분하며, 서버 입력 예산 안에서 필요한 원문을 보존하는 처리가 필요하다.
  이번에는 결과를 기록하는 데서 멈췄고 기존 엔진·모델 설정은 수정하지 않았다.
- 독립 사례는 실제 웹 과제 두 개와 합성 대조군 일곱 개뿐이다. 전체 장관 명단, 다른 모델,
  서버 동시성, 다양한 도메인에서의 성능과 정확도를 검증한 결과가 아니다.

원자료:

- [정체 해소 후 두 과제](experiments/sequential-web-search-20261015-unblocked-flow.json)
- [교육부 완료 요청 422 사유 재현](experiments/sequential-web-search-20261015-unblocked-422.json)

## 서버 입력 한도 확장 후 재실험

사용자가 입력 한도를 확장했다고 알린 뒤, 처음 확인한 서버는 여전히 4,096으로
응답했고 같은 요청도 422였다. 이후 잠시 연결 거부가 발생한 다음 서버가 다시 열렸다.
새 `/v1/models`에서 `gemma4-26b`의 `max_input_tokens=32768`,
`context_window=131072`를 확인한 뒤 재실험했다. Q 설정이나 실험 코드는 이 단계에서
변경하지 않았다. 모델 역할은 여전히 탐색·완료 모두 `oev/gemma4-26b`다.

### 기존 422 요청 확인

이전의 14,991-byte 완료 요청을 그대로 전송했다. 4,719 input tokens로 처리됐고
38.871초에 HTTP 200, `requirement_0=supported`를 받았다. 입력 제한으로 거절되던
요청이 이제 실제 판정까지 진행된다는 점을 확인했다.

### 같은 두 과제의 새 메타서치부터 실행

| 과제 | 시간 | 읽은 페이지 | 문단 묶음 | 모델 요청 | 검토 결과 |
| --- | --- | --- | --- | --- | --- |
| 교육부 성명 | 15.511초 | 공식 인사말 1개 | 1개 중 1개 수집 | 선택 1 + 관련성 1 + 완료 1 | 자동 complete, 공식 본문의 성명 확인 |
| 교육부·농식품부 성명 | 151.350초 | 농식품부 인사말 + 교육부 메인, 2개 | 17개 중 6개 수집 | 선택 2 + 관련성 5 + 완료 2 | 자동 complete, 양쪽 공식 본문의 성명 확인 |

두 과제 순차 실행은 총 166.87초였다. 메타서치는 각각 1.860초와 1.150초였고,
모델 요청 timeout·HTTP 422는 없었다.

직접 검토한 근거:

- 교육부 단일 과제는 `https://moe.go.kr/mnstrInsa.do`의
  `교육부 장관 최교진입니다.`와 서명 부분을 수집했다.
- 두 부처 과제는 `https://mafra.go.kr/home/5301/subview.do`의
  `제67대 농림축산식품부 장관 송미령입니다.`를 수집했다.
- 이어 `https://moe.go.kr/main.do?s=moe`에서 `최교진 교육부 장관`을 명시한 동정과
  2026-10-15·2026-10-13 등의 날짜가 있는 본문을 수집했다.
- 두 부처 과제는 첫 페이지 뒤에 농식품부 충족·교육부 미충족으로 판정했고, 두 번째
  페이지 처리 뒤에 두 요구사항을 모두 충족으로 판정했다. 제3자 기사는 수집하지 않았다.

이 검토의 통과 범위는 **공식 홈페이지에 표시된 성명을 원문 근거로 수집**하는 것이다.
전체 부처, 임명·재임 날짜의 독립 교차검증, 다른 날짜·검색 결과에서도 잘못된 완료를
막는지는 검증하지 않았다. 이전 BBC 오완료를 막을 조건 보존 로직도 추가하지 않았다.
새 검색에서는 이전과 다른 페이지가 선택됐으므로 시간 차이를 입력 한도 변경 효과만으로
해석하면 안 된다. 같은 과제의 반복 실행이며 독립 사례 수가 늘어난 것도 아니다.

원자료:

- [재시작 직전 4,096 제한 응답](experiments/sequential-web-search-20261015-pre-restart-422.json)
- [한도 확장 후 기존 요청 통과 및 모델 한도](experiments/sequential-web-search-20261015-limit-raised-probe.json)
- [한도 확장 후 두 과제 전체 기록](experiments/sequential-web-search-20261015-limit-raised-flow.json)

## 실제 browser_search 반영

[`websearch/pages.go`](../websearch/pages.go)가 configured `browser_search`, 호환 이름
`web_search`, `web_search_batch`에 사용된다. 입력 필드와 근거 출력은 유지하고 페이지별
`pages` 기록을 추가했다. `max_steps`/`steps`는 이 경로에서 읽기를 시도한 페이지 수다.
후속 변경으로 `decision_timeout_seconds` 입력과 개별 탐색·출처·문단 판정 타임아웃을
제거했다. 판정 HTTP 클라이언트도 별도 시간제한 없이 검색/호출자 컨텍스트를 따른다.
검색 전체 35분·완료 판정 30분·페이지 HTTP GET 25초 제한은 유지한다.

- 메타서치 후보 중 읽지 않은 페이지를 선택한다. publisher 검사와 문단 분류는 탐색 역할,
  페이지 처리 후 완료 검사는 완료 역할을 사용한다. 생성형 planner 호출은 0회다.
- 문단별 질문에 대상 ID를 명시한다. 단일 문단이 전체 목표를 해결해야 한다고 요구하지 않는다.
- 누적 원문·요구사항과 함께 `original_goal`, `allowed_domains`, `observed_at`을 완료 입력에
  넣는다. 개별 요구사항 외에 원래 목표 전체를 별도 질문으로 검사한다.
- `allowed_domains`는 후보 및 최종 redirect URL에 코드로 적용한다. 공식성·시점 조건은
  각 모델 질문에도 보존하지만, 모델 판단을 출처 소유권·재임 사실의 증명으로 취급하지 않는다.
- HTTP 실패·허용되지 않은 publisher·동일 본문은 다음 후보로 넘어간다. HTTP-only 경로가
  자동으로 Chrome이나 browser fallback을 실행하지 않는다. 후보 소진은 부분 결과를 반환한다.
- 페이지당 최대 64묶음, 전체 근거 최대 64묶음이다. 실제 완료 요청을 직렬화하여 64 KiB
  계약 안에 들어가는지 수집 전에 확인한다. 분류 묶음은 필요하면 줄이고, 단일 초대형 문단이나
  추가 근거가 안 들어가는 경우 잘림·누락을 페이지 기록에 명시한다. tokenizer 한도는 별개다.
- API 422 등에서 provider의 `detail` 문자열을 제한된 길이로 보존하여 입력 토큰 초과 사유가
  일반적인 `HTTP 422` 뒤에 숨지 않게 했다.
- `Discovery`를 주입하지 않는 기존 Go 임베더의 브라우저/planner 경로는 호환 목적으로 유지한다.
  configured 도구는 항상 Scour를 주입하므로 새 경로를 사용한다.

현재 계약과 재현은 [tools README](../tools/README.md)가 담당한다. 관련 회귀 검증은
[`pages_test.go`](../websearch/pages_test.go), 배치 상태 분리 테스트, 별도 native 모델 라우팅 및
MCP 도구 테스트에 있다. 브라우저 조작에 대한 기존 테스트는 임베더 호환 경로를 검증한다.

### 실제 도구 호출 검증

`go run ./scripts/websearchsmoke -tool browser_search ...`로 Q Runtime → MCP → configured
서비스 → 실제 Scour/HTTP/System One 경계를 호출했다. 임시 workspace와 Loom은 호출 후 삭제했고,
전체 payload를 `.artifact.json`으로 먼저 내보냈다. 모델 요청은 직렬이며 설정을 바꾸지 않았다.

| 사례 | 결과 | 페이지/근거 | native 호출 | 완료 검사 | native 요청 시간 합 |
| --- | --- | --- | --- | --- | --- |
| 교육부 공식 자료 검색 | complete | 1 / 1 | 4 | 1 | 18.260초 |
| 교육부·농식품부 공식 자료 | complete | 2 / 5 | 11 | 2 | 158.733초 |
| 과거 BBC 기사만 제공한 대조군 | incomplete, publisher 거부 | 1 / 0 | 1 | 0 | 2.281초 |

위 시간은 native 요청 시간의 합이며 전체 CLI 실행·검색·HTTP 시간을 포함한 wall time이 아니다.
검색 결과와 페이지가 매번 달라 이전 실험의 시간과 직접 비교하지 않는다.

두 부처 사례는 농식품부 공식 인사말의 송미령 표시, 교육부 메인 본문의 최교진 표시를
수집했다. BBC 대조군은 이전 실패의 기사 URL을 허용 도메인 제한 없이 직접 제공했지만
publisher 검사에서 거부했고 그 원문을 근거에 넣지 않았다. 교육부 단일 사례가 선택한
페이지는 2026-09-30 공식 동정 자료다. 공식 본문 성명 수집은 확인했으나 이것만으로
조회일 재임을 독립적으로 검증했다고 보지 않는다. 출처·현재 조건의 전달과 회귀를
확인한 것이며 모든 시점·도메인에 대한 오완료 방지를 증명한 결과는 아니다.

입력과 원자료:

- [교육부 입력](experiments/browser-search-pages-education-input.json), [결과](experiments/browser-search-pages-education-result.json), [전체 artifact](experiments/browser-search-pages-education-result.json.artifact.json)
- [두 부처 입력](experiments/browser-search-pages-two-input.json), [결과](experiments/browser-search-pages-two-result.json), [전체 artifact](experiments/browser-search-pages-two-result.json.artifact.json)
- [과거 기사 입력](experiments/browser-search-pages-history-input.json), [결과](experiments/browser-search-pages-history-result.json), [전체 artifact](experiments/browser-search-pages-history-result.json.artifact.json)

검증: 관련 Go 패키지(`websearch`, `tools`, `tools/builtin`, `agentloop`, `subagent`,
`client/systemone`, `webbrowser`, `scripts/websearchsmoke`) 테스트, `go vet`,
golangci-lint 2.14.0의 변경 범위 검사(0 issues), `go mod tidy`, `go build ./...`,
`bin/q.exe`와 `bin/q-mcp.exe` 빌드가 통과했다. PowerShell 비교 스크립트 문법도 검사했다.
`govulncheck`는 설치돼 있지 않아 실행하지 않았다. 실행 중인 Studio는 재시작하지 않았다.

검증: `go test ./websearch -count=1`, `go vet ./websearch`,
`golangci-lint run --no-config ./websearch/...` 통과(2.14.0, 기본 standard checks,
errcheck/govet/ineffassign/staticcheck/unused). `govulncheck`는 설치돼 있지 않아 실행하지 않았다.
외부 실험은 실행을 마쳤다. 입력 한도 확장 후 두 과제는 좁은 성명 수집 범위의 원문 검토를
통과했다. 이전 미완료·잘못된 완료 결과와 대조군 오판은 별도 기록으로 유지한다.

## 재현

저장소 루트의 PowerShell에서 실행한다. 외부 검색·공개 웹페이지·설정된 System One
서버를 실제 호출하므로 아래 opt-in 변수가 필요하다. 기본 테스트에서는 live 호출을 건너뛴다.
JSON의 `incomplete` 또는 대조군 기대값 불일치는 측정 결과이며, 테스트 실행 자체의
`PASS`를 조사 성공이나 모델 정확도 통과로 해석하면 안 된다.

```powershell
# 로컬 HTTP fixture: 실패 페이지 건너뛰기, 동일 본문 제외, 페이지 처리 후 판정/다음 후보
go test ./websearch -run '^TestSequentialExperimentPageBoundary$' -count=1

$env:Q_TEST_SEQUENTIAL_WEB = '1'
$env:Q_SEQUENTIAL_CASE = 'education' # 또는 two-ministries; 생략하면 두 과제를 순차 실행
$env:Q_SEQUENTIAL_RESULT = "$PWD/docs/experiments/sequential-web-search-20261015-education.json"
go test ./websearch -run '^TestLiveSequentialExperiment$' -count=1 -v -timeout=20m

# 합성 정답/무관/이름 없음/웹 지시문 대조군과 완료 판정 대조군
$env:Q_SEQUENTIAL_RESULT = "$PWD/docs/experiments/sequential-web-search-20261015-controls.json"
go test ./websearch -run '^TestLiveSequentialControls$' -count=1 -v -timeout=3m

Remove-Item Env:Q_TEST_SEQUENTIAL_WEB, Env:Q_SEQUENTIAL_CASE, Env:Q_SEQUENTIAL_RESULT -ErrorAction SilentlyContinue
```

재실행 시 기존 기록을 보존하려면 `Q_SEQUENTIAL_RESULT`에 다른 파일명을 지정한다.

후속 변경: `allowed_domains` 입력·도메인 필터는 제거했다. 출처 판정은 수집 거부 대신
`cross_check_suggestions`로 교차 확인을 제안한다. 현재 계약은 `web-search-cdp.md` 참고.
