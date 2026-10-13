# 웹 검색 루프: Qwen3.8 27B와 Gemma4 E4B 실험

2026-10-12, OEV `http://macstudio:12821/v1` 실제 호출.

**System One에 검색 결과 선택부터 근거 수집·재탐색·종료까지 맡기는 것은 가능했다.** Python 과제는 출처 확인과 질문별 완료 검증을 별도로 넣었을 때 생성형 호출 **0회**, System One **13회**, 약 **72초**에 모든 근거를 모으고 종료했다. 다만 단일 행동 선택 질문에 전부 맡기면 근거가 전혀 없는 상태에서도 자신 있게 종료했다.

이번 결과는 세 과제를 보면서 전처리와 정책을 개선한 탐색적 실험이다. 독립 평가셋의 성공률이나 일반 웹 탐색 성능으로 해석하면 안 된다.

## 실행 조건

- 루프/검증 생성 모델: 실제 OEV 모델 ID `qwen3.8-27b`.
- System One: `gemma4-e4b`, native `/systemone`, `choice` 질문.
- Qwen: temperature 0, reasoning_effort `none`, max_tokens 400. 모델 호출 제한 60초, 동시 실행 1, 애플리케이션 재시도 없음.
- Windows amd64, Go 1.27.2, 현재 Git HEAD `532ff5b` 위의 미커밋 실험 코드.
- 서버의 GPU·RAM·전력 설정·다른 작업·prefix cache 상태는 확인하지 않았다. 반복 측정은 하지 않았다. 캐시와 서버 상태에 따라 호출 시간이 크게 달랐다.
- 일반 모델 역할 설정, provider 설정, System One 설정은 변경하지 않았다. 환경의 OpenAI API key도 OEV로 전달하지 않는다.
- Q의 Chat client, native System One client, Rod runtime을 사용하는 별도 Go 실험 루프다. Q 메인 AgentLoop/ACP를 통한 제품 전체 E2E 시험은 아니다.

검색은 실제 DuckDuckGo HTML 검색 페이지를 Rod로 읽었다. 정적 문서는 Go HTTP와 HTML parser로 전체 본문을 읽었다. 같은 URL의 내용은 캡처하여 비교군에 재사용했고 요청·응답·원문·에러를 보존했다.

## 과제와 평가

| 과제 | 찾아야 하는 근거 |
| --- | --- |
| Go | CancelFunc 호출이 필요한 이유, 호출이 작업 종료를 기다리는지 |
| SQLite | WAL의 네트워크 파일시스템 제약, 읽기 전용으로 여는 버전과 세 가지 조건 |
| Python | asyncio.timeout의 예외 종류, catch 위치, deadline 재설정 |

평가는 생성된 답변 대신 **수집한 공식 문서 인용에 필요한 내용이 명시적으로 있는지** 확인했다. Go 2개, SQLite 5개, Python 3개의 사후 rubric marker를 사용했고 본문도 검토했다. 정답 URL·정답 문장·이 평가 rubric은 탐색 모델에 전달하지 않았다.

SQLite의 세부 rubric은 네트워크 제약, 3.22.0 이후 지원, sidecar 파일의 존재와 읽기 가능 여부, 디렉터리 쓰기 권한에 따른 파일 생성, immutable 대안이다. 대략적인 조건 요약만 있고 읽기 가능 여부나 디렉터리 권한이 빠진 경우 완전한 근거로 세지 않았다.

기준은 [Go context 문서](https://pkg.go.dev/context), [SQLite WAL 문서](https://www.sqlite.org/wal.html), [Python asyncio task 문서](https://docs.python.org/3/library/asyncio-task.html)다.

## 주요 결과

아래 시간은 해당 실행에서 관찰한 값이며 성능 보장이나 통계적 비교가 아니다. 캡처된 페이지를 재사용한 실행은 웹 fetch 비용이 대부분 제외되어 있다.

| 구성 | Go | SQLite | Python |
| --- | --- | --- | --- |
| 코드만: 첫 결과 + 키워드 상위 4구간 | 2/2 | 5/5 | 0/3 |
| System One 행동 선택만 | 2/2, 28초, 4회 | 3/5, 35초, 5회 | **0/3인데 종료**, 21초, 3회 |
| System One + Qwen 종료 검증 | 2/2, 47초, 생성 1회 | **3/5인데 검증 통과**, 58초, 생성 1회 | 1/3, 69초, 생성 2회, 미완료로 보고 |

Python 추가 비교:

| 구성 | 근거 | 결과 | 생성형 호출 | System One 호출 | 시간 |
| --- | --- | --- | --- | --- | --- |
| Qwen 매 단계 | 3/3 | 근거 수집 후 종료 호출 timeout | 3 | 0 | 75초 |
| System One + 질문별 완료 검증 | 0/3 공식 근거 | 외부 튜토리얼로 이동, 단계 제한 도달 | 0 | 13 | 54초 |
| **System One + 질문별 완료 검증 + 출처 확인** | **3/3** | **완료** | **0** | **13** | **72초** |

마지막 실행의 입력 토큰 합계는 19,429, 출력 토큰 합계는 0이다. 출력 생성이 없다는 뜻이지 모델 추론이나 입력 비용이 없다는 뜻은 아니다. 추가 검증으로 호출 수가 늘어 시간이 반드시 줄어드는 것도 아니다.

## 실제로 무엇이 달라졌나

처음에는 `open / collect / search / finish`를 한 `choice`에 넣었다. Gemma는 Python 개요 페이지를 열고 일반적인 asyncio 설명 한 구간만 수집한 뒤 `finish`를 골랐다. 종료 confidence는 약 **0.961**이었다. 예외 종류·catch 위치·재설정 근거는 전부 없었다.

같은 근거를 놓고 질문 세 항목 각각에 `supported / missing`을 물으면 **세 항목 모두 missing**으로 판단했다. 그래서 다음과 같이 분리했다.

1. System One이 다음 행동을 선택한다.
2. 링크를 열기 전, System One이 공식 출처인지 별도 판단한다.
3. `finish`를 고르면 질문별 근거 충족 여부를 별도로 판단한다.
4. 누락되면 Go 코드가 종료를 거부하고 기존 검색 결과의 다른 후보로 돌아간다.

이 구성은 Python 개요의 조기 종료를 거부했고, 다음에 선택한 runebook.dev는 외부 출처로 거부했다. 이후 공식 `asyncio-task.html`로 이동해 필요한 구간을 수집하고 종료했다. **새 검색어 생성도, 생성형 단계 결정도 필요하지 않았다.** 최초 검색어는 과제 문장 그대로였다.

단, 검증에 쓴 2/2/3개의 질문 항목은 실험자가 과제 문장을 분리해 제공했다. 임의의 복잡한 사용자 요청을 System One이 스스로 이 구조로 분해하는 능력은 검증하지 않았다. 출처 판단도 모델의 분류이며 도메인 소유권을 증명하는 보안 장치는 아니다.

SQLite는 다른 한계를 보여준다. 행동 선택, 별도 System One 완료 검증, Qwen 완료 검증 모두 대략적인 읽기 전용 조건 요약을 충분한 근거로 받아들였다. 정확한 읽기 권한·디렉터리 권한을 담은 구간은 후보에 있었지만 선택하지 않았다. **생성 모델을 한 번 더 호출하는 것만으로 세부 조건 누락이 해결되지는 않았다.**

## 생성형 모델을 제거할 수 있는 범위

| 작업 | 이번에 확인한 처리 방식 | 범위 |
| --- | --- | --- |
| HTTP fetch, HTML 정리, DOM 관측, URL 처리 | Go 코드 | 모델 불필요 |
| 본문 분할, 키워드 후보 추리기, 캐시, 중복·방문 기록 | Go 코드 | 모델 불필요. 지나친 후보 축소는 정답을 제거함 |
| 후보 링크와 근거 구간 선택 | System One | 생성형 호출 없이 수행 가능 |
| 공식 출처 분류 | 별도 System One 질문 | Python에서 공식 문서와 외부 튜토리얼 구분 성공 |
| 질문별 완료 판단과 재탐색 | 별도 System One 질문 + Go 상태 전이 | Python 전체 탐색을 생성형 호출 0회로 완료 |
| 새로운 검색어 작성 | Qwen 또는 코드가 만든 후보 중 선택 | 자유로운 새 문자열 작성은 현재 choice API의 역할 밖. 이번 성공 과제는 기존 검색 결과만으로 해결 |
| 임의 요청의 질문 분해, 상충하는 세부 조건 해석 | 별도 과제 필요 | Qwen을 남길 후보 영역이지만 이번 실험에서 신뢰성을 입증하지 못함 |
| 자연어 최종 답변·여러 출처 종합 | 별도 생성 단계 후보 | 이번 실험은 원문 근거 반환까지만 평가. 최종 답변 품질은 미측정 |

코드만의 기준군은 쉬운 첫 검색 결과에서는 충분했지만 Python처럼 상세 문서를 찾아야 하는 경우 실패했다. 반대로 판단이 필요한 부분을 모두 큰 생성 모델에 맡길 필요도 없었다. 후보가 잘 준비되고 요구사항이 구조화되어 있으면 System One이 전체 반복을 담당할 수 있다.

Q의 기본 설계 후보는 **Go 실행기 + System One 탐색 루프**로 두고, 후보가 소진되거나 새로운 검색어·요구사항 분해·출처 간 해석이 필요할 때 Qwen에 넘기는 형태다. 최종 출력은 우선 `status`, `evidence[{url, passage}]`, `unresolved_requirements`로 반환할 수 있다. `confidence` 하나로 종료를 허용하지 않는다.

## 검색 엔진과 브라우저의 별도 문제

- Google Rod pilot은 페이지 이동 중 `Inspected target navigated or closed`로 실패했다. 모델 판단 실패와 구분했다.
- DuckDuckGo의 Rod 캡처에서는 실제 검색 결과를 얻었다.
- 이후 Go HTTP만으로 같은 세 검색 문장을 요청한 시험은 모두 HTTP 202로 처리되어 실패했다. 그 시점의 HTML body는 캡처하지 않았으므로 CAPTCHA나 영구 차단이라고 단정하지 않는다.
- 정적 공식 문서의 Go HTTP fetch는 성공했다. 따라서 **본문 읽기는 HTTP 우선**, 검색 페이지나 JS가 필요한 페이지는 **브라우저 fallback**이 실용적인 후보다. 검색 엔진의 장기 안정성은 이번에 입증하지 않았다.

현재 원문 구간 선택은 1,400 rune window, 300 rune overlap, 키워드 중복 개수 상위 4구간이다. compact 모드의 링크 후보는 최대 5개다. 정적 본문은 최대 250,000 rune까지 읽는다. 표·단락·섹션 구조를 보존하는 추출은 아직 없다. 파일 권한 같은 조건이 경계에서 잘리는 문제가 남는다.

초기 pilot에는 큰 입력의 Qwen timeout, System One timeout, 작은 확인 요청 timeout도 있었다. 이후 서비스는 다시 응답했다. 서버 부하·queue·취소 동작을 확인하지 못했으므로 이를 특정 모델의 한계나 입력 크기 하나의 원인으로 단정하지 않는다.

## 재현과 남긴 자료

실행기는 `scripts/websearchbench/`에 있다. 정상 Go 테스트는 외부 모델을 호출하지 않는다. 아래 `go run` 명령은 실제 모델과 공개 사이트를 호출한다.

```powershell
# 전체 생성 모델, query 생성 + System One, query도 생성하지 않는 구성
go run ./scripts/websearchbench -compact -steps 8 -out docs/experiments/rerun.json

# 요구사항별 완료 확인과 출처 확인을 포함한 생성형 호출 0회 Python 루프
go run ./scripts/websearchbench -compact -no-plan -source-check -task python -arms systemone-gated -steps 12 -out docs/experiments/rerun-python.json

# 이미 캡처한 페이지로 재실행: 없는 URL만 live fetch
go run ./scripts/websearchbench -compact -no-plan -source-check -task python -arms systemone-gated -steps 12 -replay docs/experiments/websearch-python-source-checked.json -out docs/experiments/replay-python.json

# 네트워크 없이 사후 근거 coverage 평가
go run ./scripts/websearchbench -evaluate docs/experiments/websearch-compact-v2.json,docs/experiments/websearch-python-source-checked.json

# 모델·브라우저 없는 HTTP 검색 시도
go run ./scripts/websearchbench -compact -http-search -no-plan -arms deterministic -out docs/experiments/http-rerun.json
```

`websearch-compact-v2.json`은 기본 System One과 코드 기준군, `websearch-hybrid.json`은 Qwen 종료 검증, `websearch-python-final.json`은 출처 검증 전의 System One 루프 및 Qwen 기준군, `websearch-python-source-checked.json`은 최종 생성형 호출 0회 실행이다. `websearch-gates.json`에는 질문별 검증 probe가 있고, `websearch-http-only.json`에는 HTTP 202 실패가 있다. 앞선 pilot도 삭제하지 않았다. `websearch-scores.json`은 사후 rubric 결과, `websearch-metadata.json`은 모델 catalog와 실행 조건이다.

Qwen은 JSON 객체 앞에 설명을 붙이는 응답도 했다. 최초 엄격 parser의 실패를 보존했고 이후 허용한 단일 JSON 객체 추출은 `protocol_note`에 기록했다. 실제 제품 연결에서는 정식 tool-call 경로를 사용해야 한다.

검증: 실험 실행기의 단위 테스트·go vet·golangci-lint. 테스트는 redirect 이후 상대 URL 해석, script/navigation 제거, Unicode 근거 보존, 검색 redirect 해석과 중복 제거, chunk 경계 근거 보존, 평가 rubric의 비공식 출처 배제를 확인한다.

로그인·CAPTCHA 해결·iframe·shadow DOM·복잡한 폼·한국어 과제·멀티소스 충돌·적대적 페이지·독립 평가셋·최종 자연어 답변의 품질은 이번 범위 밖이다.
