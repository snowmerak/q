# Retrieval routing 비용 실험

실험일: 2026-10-01~02 KST. 기준 코드: `ffcd0e95ce88ea6ff1766e92497f315828d150fe`.
제품 코드와 Q의 전역 설정은 변경하지 않았다. 실험 종료 후 보고서만 보존하기로 하여 실험 코드·설정·원시 로그·임시 snapshot은 제거했다. 이 문서는 실행 당시의 방법, 집계 결과와 한계를 기록한다.

**현재 최소 구현에서는 가설을 지지하지 못했다.** 20개 과거 수정 과제에서 OEV를 사용한 제안 방식은 Sol 호출을 32.5% 줄였지만, gold file recall은 85.8%에서 78.3%, gold evidence recall은 73.4%에서 56.4%로 떨어졌다. 첫 관련 evidence까지의 탐색도 늘었다. 큰 모델 호출 감소는 확인했지만, 동일한 품질을 유지한 비용 절감은 확인하지 못했다.

추가 JEV 비교에서는 file/evidence recall이 84.2%/63.8%로 OEV보다 높았고 Sol 호출은 baseline보다 28.0% 적었다. 그러나 본문이 노출된 파일은 baseline의 9.35개에서 11.65개로 늘었으며, 20개 모두 Sol로 전환했다. JEV도 동일 품질과 적은 탐색이라는 두 조건을 함께 충족했다고 보기는 어렵다. 아래에 별도 실행의 전체 지표와 한계를 기록했다.

OEV가 없는 규칙 라우팅보다 최종 evidence recall은 높았다. 다만 cheap 단계만으로 완료한 과제는 없었고, 추가 4개 과제의 query 생성 비교에서도 Luna의 effort를 `low`에서 `medium`으로 높인 효과를 확인하지 못했다. 다음 우선순위는 후보 본문 확장, query/scope의 구체성, 저비용 종료 판단을 각각 분리해 검증하는 것이다.

## 질문과 비교 범위

목표는 답변 생성이나 코드 수정을 완성하는 것이 아니라, 기존 구현의 위치와 source evidence를 얼마나 적은 탐색과 큰 모델 호출로 얻는지 측정하는 것이다.

- A `baseline`: `codex/gpt-6.1-sol`, reasoning `high`. 큰 모델이 다음 검색·파일 읽기·종료를 직접 결정한다.
- B `proposed`: OEV `google/gemma-4-E4B-it`의 System One `choice`가 전략만 선택하고, `codex/gpt-6-luna` `low`가 typed query를 생성한다. 최대 3 cheap rounds 후 필요한 경우 같은 Sol로 전환한다.
- C `heuristic`: 규칙이 전략을 선택한다. Luna, executor, 종료 조건, Sol fallback은 B와 동일하다. B−C 비교가 OEV routing의 추가 가치를 보는 비교다.
- D `deterministic`: 규칙으로 전략과 query를 모두 생성하며 모델과 escalation을 사용하지 않는다. 값싼 하한선이며, C와 구분한다.

A도 B와 같은 typed executor, 최대 8 retrieval operations와 bounded output을 사용한다. 따라서 이것은 Q의 전체 기본 채팅 프롬프트·모든 도구·무제한 탐색과의 비교가 아니라 **같은 retrieval 도구에서 큰 모델이 직접 탐색하는 통제 비교**다. 모델에 코드 수정 도구는 제공하지 않는다.

## 재사용한 기존 구현

| 기능 | 기존 코드 | 사용 방식 |
|---|---|---|
| Codex 인증·provider 설정 | `providerhost.Store`, llm-provider Gateway | Q의 설정을 읽어 Codex provider만 인메모리 Gateway로 실행 |
| 생성·사용량 | `client.Client.Chat`, native Responses 모드 | 모델과 reasoning effort를 실험 설정에서 선택 |
| closed-set decision | `client/systemone`의 `Request`, `QuestionChoice`, `Answer` | strategy 하나만 선택; query나 파일 선택은 요청하지 않음 |
| OEV endpoint | `systemoneconfig` | 기존 `oev` provider 사용; 저장된 설정은 수정하지 않음 |
| 검색 | 기존 설치된 ripgrep 15.2.0 | shell을 거치지 않고 검증된 인자 배열로 실행 |

실험은 별도 실행기, API 연결, retrieval loop·채점, deterministic executor, dataset 생성·분석 스크립트로 구성했다. 별도 인덱스, DB, planner, multi-agent 계층은 만들지 않았다. 이 임시 구현은 실험 종료 후 제거했다.

## 실행 규칙

전략은 `exact_text`, `filename`, `symbol_definition`, `symbol_references`, `regex_search`, `escalate`다. 큰 모델은 검색에서 이미 발견한 경로에 한해 `read_file`을 사용할 수 있다. 파일명 검색은 경로만 반환한다.

Symbol definition은 Go 선언 패턴 검색이고, references는 단어 경계 검색이다. LSP의 타입 해석·정확한 참조 추적과 같지 않다. 검색은 Go 구현 파일로 제한하며, 테스트·generated·third-party 파일은 snapshot에서 제외했다.

이번 cheap closed set에는 별도 `read_candidate`가 없다. 따라서 검색 snippet보다 넓은 본문이 필요하면 Sol의 `read_file`이나 다음 검색에 의존한다. 이것은 escalation과 evidence recall에 영향을 주는 구현상 제약이며, Luna/OEV의 능력 부족으로 혼동하면 안 된다. 다음 최소 확장 후보는 이미 검색으로 발견한 후보의 제한된 본문을 deterministic하게 확장하는 연산이다.

각 query는 최대 4개 패턴·scope, match limit 30, context 0–3줄이다. 반환 source는 최대 120줄, 각 줄 400자이며 파일 읽기는 100줄 이하다. 반환 JSON은 14,000 bytes로 제한하며, 제한으로 제외한 source line은 파일 탐색량과 recall에도 포함하지 않는다. 검색은 대소문자를 구분하지 않는다. 인자 검증과 경로 제한을 통과한 JSON만 executor로 전달된다.

같은 전략은 cheap loop 안에서 재사용하지 않는다. 패턴 순서·대소문자·동등한 literal/regex query를 정규화해 반복을 거른다. 2회 연속 새 source evidence가 없으면 escalation한다. 명백한 quoted literal/identifier는 fast path로 decision call을 생략할 수 있다. OEV의 상위 두 확률 margin이 0.08 미만이면 escalation한다. 이 임계값은 보정된 정답 확률을 의미하지 않는다.

v2에서는 알려진 identifier가 없으면 symbol 선택지를 제외하고, 최초 2 cheap rounds에는 OEV의 `escalate` 선택지를 제외한다. 실패·낮은 margin에 따른 deterministic escalation은 여전히 가능하다. OEV에는 objective, 검색 수·새 evidence·truncation, 관찰된 identifier만 보낸다. 전체 repository 파일 목록을 주지 않는다.

Cheap 종료는 task 핵심 단어 coverage 0.8 이상, 새 evidence 3줄 이상, 마지막 결과가 잘리지 않았고 source 노출 파일이 12개 이하인 경우에 검토한다. 추가로 한 파일에서 실제 코드 12줄 이상과 control flow, 같은 coverage를 요구한다. **이것은 정답을 보장하는 validator가 아니라 저비용 proxy**다. Gold는 종료 판단에 쓰지 않는다.

## Benchmark와 정답 누출 방지

최근 450개 first-parent 커밋을 순서대로 조사해, 기존 Go 구현 1–4개 파일을 수정한 focused fix 24개를 선택했다. whitespace를 무시한 변경량 6–220줄, 수정 전 evidence가 남아 있는 커밋으로 제한했다. Formatting/lint/dependency/rename, generated file 동반 변경, 정답 구현 파일명을 그대로 노출하는 메시지는 제외했다. 앞 4개는 개발용, 나머지 20개는 held-out 비교용이다.

각 task는 해당 커밋의 parent를 `git archive`로 추출한 독립 snapshot에서 수행했다. `git`, diff, gold, 미래 커밋 정보, 현재 실험 파일은 모델의 도구로 접근할 수 없다. Codex provider는 빈 임시 디렉터리에서 minimal mode로 실행하고 built-in shell·MCP·web·skills를 비활성화했다. 오직 executor가 돌려준 evidence와 커밋 목적만 모델 입력에 포함된다.

별도 read-only 검증으로 24개 snapshot의 파일 목록과 5,481개 source blob을 parent와 대조했다. Windows `git archive`의 CRLF 변환만 정규화했을 때 모든 내용이 일치했고, gold 줄 구간도 모두 parent source 안에 있었다.

Gold file은 parent에도 존재하고 실제 수정된 Go 구현 파일이다. Gold evidence는 `git diff -w --unified=0`의 수정 전 줄 구간이다. 새로 추가된 줄만 있는 hunk는 evidence gold에서 제외했다. AST symbol recall 대신 이 hunk recall을 사용했다.

자동 gold는 불완전하다. 한 줄만 겹쳐도 hunk hit로 계산하며, import·comment 변경이 포함될 수 있다. 반대로 수정하지 않은 caller나 연관 구현을 유용하게 찾았어도 gold로 인정되지 않는다. 여러 task가 같은 subsystem/파일을 공유하므로 20개의 완전히 독립된 문제로 해석할 수 없다. 커밋 subject의 subsystem 단서도 일반 사용자 질문보다 유리할 수 있다.

## Metric 정의

- `gold_file_recall`: gold 파일 중 **본문이 모델에 노출된** 파일 비율. 파일명만 발견한 비율은 별도 `candidate_file_recall`.
- `gold_evidence_recall`: gold hunk 중 반환한 source line이 하나 이상 겹친 구간의 비율. task별 macro 평균.
- `ops/files until first relevant`: 처음 gold 파일 본문을 노출하기까지의 operation 및 고유 source 파일 수. 더 엄격한 기준으로 첫 gold hunk까지의 operation·source 파일·명시적 파일 읽기 수도 별도 집계한다.
- `exposed_files`: 검색 snippet 또는 read로 본문을 본 고유 파일 수. `opened_files`, `open_operations`는 명시적 파일 읽기만 센다. rg가 디스크에서 내부적으로 검사한 모든 파일 수는 아니다.
- `decision/query/large calls`, escalation, API/IR 실패, 0건 검색, 중복 query, recovery, 모델별 input/output/cache/reasoning tokens, wall time을 원시 기록.
- Token 필드가 API에 없으면 `null`. 특히 reasoning token을 input/output 합에서 임의로 추정하지 않는다. 가격·실제 과금액·에너지 소비는 측정하지 않았다.
- First-hit 실패는 `null`로 남긴다. 발견한 task만의 평균과 실패 수를 함께 보고하며 실패를 0회로 계산하지 않는다.
- p90은 정렬한 표본에서 인덱스 `floor((n−1)×0.9)`의 값이다. Recovery는 검색 오류/0건 이후 새 source evidence를 얻은 횟수이며 gold 발견을 뜻하지 않는다.

채점은 loop 종료 이후 수행한다. `annotated.jsonl`의 `relevant_found`, `gold_hunk_found`는 사후 추가된 분석용 라벨이며 모델에 전달되지 않았다.

## 개발용 실험과 개선

초기 버전은 단어 coverage만으로 너무 일찍 멈췄고, OEV는 한 차례 검색 후 바로 escalation하거나 알려진 identifier 없이 symbol 전략을 선택했다. 이후 task별 예외 없이 compact routing state, symbol precondition, 보수적인 종료 조건, OR 패턴·scope를 명시하는 query prompt를 적용했다. 긴 baseline이 초기 발견을 잊지 않도록 공통 state에는 이전 검색을 모두 유지했다.

개발용 중간 버전은 실행기 수정 과정의 진단 자료다. 프롬프트·종료 규칙·history가 함께 바뀌었고 실행이 겹쳤으므로, 버전 간 wall time이나 정확도 차이를 한 개선의 인과 효과로 해석하지 않는다.

평가 도중 `rg --glob '*.go' --glob 'app/*.go'`의 positive glob이 OR로 결합되어 scope가 제한되지 않는 실행기 오류를 발견했다. 초기 개발용 실행과 중단된 `heldout-v2`의 수치는 모두 무효 처리했다. Go 제한을 `--type go`로 바꾸고 다섯 검색 primitive의 scope 회귀 테스트를 추가한 뒤, 같은 20개 task와 고정된 routing/query/종료 정책으로 모든 비교군을 처음부터 재실행했다. 이하 표는 수정된 `heldout-v3`만 사용한다. 오류 조사 과정에서 일부 평가 trace를 이미 보았으므로 분석자가 완전히 blind한 평가라고 주장하지 않는다. 모델에는 계속 gold나 미래 diff를 제공하지 않았다.

## OEV를 사용한 최초 비교 결과

20개 task × 4개 방식, 총 80개 trial을 완료했다. 아래 recall은 task별 비율의 macro 평균이다. 횟수·시간은 별도 표시가 없으면 task당 평균이다.

| 지표 | A Sol 직접 탐색 | B OEV + Luna → Sol | C 규칙 + Luna → Sol | D 규칙만 |
|---|---:|---:|---:|---:|
| Gold file recall | 85.83% | 78.33% | 78.33% | 26.67% |
| Gold evidence/hunk recall | 73.42% | 56.38% | 47.38% | 2.08% |
| 경로만 포함한 candidate file recall | 88.33% | 80.83% | 80.83% | 36.67% |
| Gold 파일 하나 이상 발견한 task | 19/20 | 18/20 | 18/20 | 9/20 |
| Gold 파일 전부 발견한 task | 15/20 | 13/20 | 13/20 | 2/20 |
| Gold hunk 하나 이상 발견한 task | 18/20 | 16/20 | 14/20 | 2/20 |
| 전체 retrieval operations | 7.60 | 7.85 | 7.85 | 2.95 |
| 본문이 노출된 고유 파일 | 9.35 | 9.85 | 10.60 | 4.90 |
| 명시적으로 열어본 고유 파일 | 2.20 | 2.00 | 1.95 | 0 |
| 명시적 파일 읽기 operations | 3.30 | 2.85 | 2.95 | 0 |
| Sol 호출 | 7.85 | 5.30 | 5.10 | 0 |
| OEV 호출 | 0 | 2.70 | 0 | 0 |
| Luna query 호출 | 0 | 2.70 | 2.90 | 0 |
| Escalation task | 해당 없음 | 20/20 | 20/20 | 사용 안 함 |
| Wall time 평균, 초 | 54.27 | 50.76 | 49.47 | 0.111 |
| Wall time 중앙값, 초 | 54.49 | 48.94 | 49.47 | 0.109 |
| Wall time p90, 초 | 60.49 | 59.28 | 56.32 | 0.138 |

First-hit 지표는 **발견에 성공한 task만의 조건부 평균**이다. 실패 수를 함께 보아야 한다. D의 1회가 높은 효율을 뜻하지는 않는다. 대부분의 hunk를 끝내 찾지 못했다.

| 최초 발견까지 | A | B | C | D |
|---|---:|---:|---:|---:|
| Gold 파일: operations | 2.42 | 3.00 | 2.67 | 1.00 |
| Gold 파일: 본문 노출 파일 | 3.68 | 4.22 | 6.11 | 4.44 |
| Gold 파일: 명시적 open 파일 | 0.32 | 0.28 | 0.17 | 0 |
| Gold 파일: 끝내 발견 못한 task | 1 | 2 | 2 | 11 |
| Gold hunk: operations | 3.22 | 4.31 | 4.07 | 1.00 |
| Gold hunk: 본문 노출 파일 | 5.17 | 6.69 | 7.79 | 6.50 |
| Gold hunk: 명시적 open 파일 | 0.72 | 0.75 | 0.64 | 0 |
| Gold hunk: 끝내 발견 못한 task | 2 | 4 | 6 | 18 |

파일 수는 반환 evidence 순서상 첫 hit까지의 누적 고유 파일 수다. 한 operation이 여러 파일을 한꺼번에 반환할 수 있으므로 실제 사람이 순서대로 파일을 읽은 횟수와 같지는 않다.

| 실패·복구, 20 task 합계 | A | B | C | D |
|---|---:|---:|---:|---:|
| API 실패 | 0 | 0 | 0 | 0 |
| Typed IR 제약 위반 | 0 | 1 | 1 | 0 |
| 결과 0건 operations | 3 | 15 | 12 | 15 |
| 실패/0건 뒤 새 evidence를 얻은 recovery | 3 | 11 | 10 | 0 |
| 차단된 동일 query | 0 | 0 | 0 | 0 |
| 전체 operation/call budget 종료 | 15 | 17 | 17 | 해당 없음 |
| 큰 모델이 충분하다고 판단해 종료 | 5 | 3 | 3 | 해당 없음 |

D는 모두 최대 3회인 자체 budget으로 종료했다. B/C의 cheap 종료는 0건이다. 대부분의 방식이 전체 8회 operation 한도에 닿았으므로, 이 결과는 **고정 budget에서 얻은 evidence와 비용**이다. 자연스럽게 충분한 evidence를 확보할 때까지 필요한 전체 비용은 측정하지 않았다.

실행 환경은 Windows arm64, 논리 CPU 8개, Go 1.27.1, ripgrep 15.2.0이다. 최종 비교는 최대 3개 trial을 동시에 실행했고 task별 arm 순서를 회전했다. Wall time은 이 부하에서 관찰한 task latency이며 단일 대화의 latency로 일반화하지 않는다. Gateway 시작과 모델 연결 probe, snapshot 생성은 측정 구간 밖이다.

### 모델별 실제 사용량

아래 값은 각 방식의 20 task 합계이며 최종 유효 실행만 포함한다. 개발 실험, 연결 probe, 뒤의 query 비교는 제외한다. Cache token은 input token에 포함된 부분집합이다.

| 방식·모델 | Calls | Input tokens | Output tokens | Cached input | API 경과시간 합, 초 |
|---|---:|---:|---:|---:|---:|
| A Sol high | 157 | 1,543,078 | 21,545 | 614,400 | 1,080.68 |
| B Sol high | 106 | 1,057,899 | 16,653 | 391,680 | 763.90 |
| B Luna low | 54 | 267,237 | 2,586 | 189,952 | 210.99 |
| B OEV Gemma | 54 | 18,507 | 0 | 미제공 | 35.30 |
| C Sol high | 102 | 1,081,758 | 15,568 | 417,664 | 764.28 |
| C Luna low | 58 | 299,531 | 2,630 | 217,600 | 220.16 |

D는 모델 사용량이 0이다. OEV의 output 0은 answer token의 logits로 선택하는 API의 실제 usage 값이다. 모든 모델의 별도 reasoning token 수는 API에서 제공되지 않아 `null`로 남겼다. 그러므로 reasoning token 절감량이나 실제 과금액 절감률을 주장하지 않는다.

B는 A보다 Sol을 51회 적게 호출했지만 Luna 54회와 OEV 54회를 추가했다. 전체 모델 호출은 157회에서 214회로 늘었다. 전체 input은 1,343,643으로 약 12.9%, output은 19,239로 약 10.7% 줄었다. Sol API 경과시간 감소 약 316.8초 중 Luna/OEV가 약 246.3초를 사용했다. 관찰된 task wall time 감소는 6.5%에 그쳤다. 호출 수와 모델별 토큰 단가는 같지 않으므로 이 값들을 금전 비용 하나로 합치지 않았다.

### 비용이 줄어든 위치와 품질 손실

| Sol 전환 전 cheap 단계 | B | C | D |
|---|---:|---:|---:|
| Gold file recall | 30.83% | 36.67% | 26.67% |
| Gold hunk recall | 6.83% | 6.46% | 2.08% |
| Gold hunk 하나 이상 발견한 task | 3/20 | 3/20 | 2/20 |
| Gold hunk 전부 발견한 task | 1/20 | 1/20 | 0/20 |

B/C의 최종 hunk 회수율 대부분은 Sol 전환 뒤에 추가됐다. 따라서 이번의 큰 모델 호출 감소는 cheap loop가 문제를 완결해서라기보다, 공통 8회 budget 중 일부 탐색을 Luna로 대체한 효과가 크다. Cheap 단계에서 gold hunk를 전부 본 경우에도 종료하지 않은 사례가 있어 종료 proxy 역시 보수적이다. 다만 자동 gold를 전부 찾았다는 사실만으로 사용자 목적이 충분히 해결됐다고 단정할 수는 없다.

Task를 짝지은 bootstrap 2,000회(seed `20261001`)에서 B−A Sol 호출 차이는 −2.55회, 95% 구간 [−2.85, −2.25]였다. File recall 차이는 −7.50%p [−22.50, +5.00], hunk recall은 −17.04%p [−36.25, +1.00]였다. 둘 다 첫 gold hunk를 찾은 15개 task에서는 B가 평균 1.00 operation 더 필요했다([+0.47, +1.60]). 이는 작은 단일 repository 표본의 task 재표집 구간이며, 반복 모델 실행의 변동성은 포함하지 않는다. Recall 구간이 0을 포함한다고 동등한 품질이나 비열등성이 입증된 것은 아니다.

### Routing, query generation, heuristic을 구분한 해석

**Decision routing:** B는 C와 file recall이 같고 hunk recall이 9.00%p 높았다. 짝지은 task bootstrap 구간은 [+2.75, +16.00]%p였다. 본문 노출 파일도 평균 0.75개 적었다. 이 표본에서는 이후 routing에 유리한 신호가 있다. 그러나 B의 Sol 호출은 0.20회 더 많고, 각 arm을 한 번씩만 실행했으므로 OEV 선택 자체의 순수 인과 효과로 확정할 수 없다. 특히 cheap 단계의 hunk recall 차이는 0.38%p에 불과하다.

**Query와 evidence 확장:** 좁은 query의 빈 결과, 넓은 query의 잘림, declaration만 찾고 구현 본문을 못 보는 패턴이 cheap 단계의 손실 요인이다. Symbol definition 검색은 선언 줄 주변만 반환하고, filename은 본문이 없다. Cheap loop가 이미 얻은 후보를 충분히 읽을 수 없다는 제약을 query generator의 모델 크기 문제와 분리해야 한다.

**Heuristic의 가치:** 첫 OEV 선택은 20/20 모두 `exact_text`로 C의 첫 규칙과 같았다. 이 corpus의 첫 전략 선택은 decision model을 생략할 후보이다. 이후까지 heuristic만으로 충분했다고 하기는 어렵다. C의 최종 hunk recall이 B보다 낮고, query까지 규칙으로 만든 D는 2.08%에 그쳤다. D는 가장 긴 task 단어를 사용하는 단순한 하한선이므로 모든 deterministic 검색 방식에 대한 결론은 아니다.

### OEV trace와 confidence

| 선택 전략 | 호출 | 다음 결과 0건 | 다음 결과 gold 파일 본문 포함 | 다음 결과 gold hunk 포함 |
|---|---:|---:|---:|---:|
| exact_text | 20 | 6 | 5 | 1 |
| symbol_references | 10 | 0 | 6 | 1 |
| symbol_definition | 9 | 2 | 4 | 0 |
| filename | 11 | 0 | 0 | 0 |
| regex_search | 4 | 1 | 2 | 2 |

위 hit 수는 신규 gain이 아니라 해당 결과에 gold가 포함됐는지다. `filename`의 본문 hit가 0인 것은 연산의 정의상 당연하다. Declaration은 이후 읽기를 위한 유용한 단서일 수 있다. 전략마다 입력 상태와 실행 순서가 다르므로 이 표만으로 regex가 최선이라고 결정할 수 없다.

Margin ≥0.75인 26개 결정에서도 다음 검색이 0건인 경우가 6개, gold hunk를 포함한 경우는 2개였다. 0.25–0.75 구간은 26개 중 각각 3개와 2개였고, 0.25 미만은 2개뿐이었다. **큰 margin이 downstream evidence 성공을 보장하지 않았다.** 선택지 수·종류가 round마다 바뀌므로 margin을 그대로 비교하는 데도 한계가 있다. 설정한 0.08 미만 조건, literal fast path, 중복 query 차단은 최종 corpus에서 발동하지 않았다. 해당 경로는 기능 테스트 대상이며 성능 효과가 측정됐다는 뜻은 아니다.

### Luna 개선 가능성: 추가 실제 query 실험

개발용 4개 task에서 전략을 `exact_text`로 고정하고 빈 history에서 query 하나만 생성했다. OEV와 Sol fallback을 제거하고 Luna `low`, Luna `medium`, Sol `high`를 비교한 12회의 실제 API 호출이다. 평가용 20개 task의 정책은 바꾸지 않았다.

| Query generator | File recall | Hunk recall | 노출 파일 | Wall 평균, 초 | Input / output tokens 합계 |
|---|---:|---:|---:|---:|---:|
| Luna low | 45.83% | 5.00% | 6.50 | 4.98 | 16,197 / 166 |
| Luna medium | 20.83% | 5.00% | 5.50 | 4.32 | 16,197 / 171 |
| Sol high | 45.83% | 5.83% | 4.00 | 8.81 | 16,197 / 871 |

단순 effort 증가는 개선되지 않았다. 한 task에서는 `medium`이 `scope: ["*studio*"]`를 만들어 디렉터리를 의도대로 포함하지 못했고 결과가 0건이었다. API/JSON 실패는 없었다. Sol도 강제로 한 번의 exact query만 허용하면 hunk recall이 낮았다. 이는 Luna의 reasoning 용량만 키우는 것으로 전체 병목이 해결된다는 근거가 없음을 보여준다. 작은 표본, 고정 호출 순서, cache 차이가 있으므로 `medium`이 본질적으로 더 나쁘거나 빠르다는 결론은 아니다.

Query probe는 개발용 4개 task를 각 모델/effort로 한 번씩 실행했으며, 최종 held-out benchmark의 결과와 합치지 않았다.

## JEV 추가 비교

2026-10-02 KST에 같은 평가용 20개 task로 JEV를 추가 실행했다. 기존 설정과의 차이는 `decision_model: typesafe/jev-latest` 하나다. API의 실제 응답 모델은 `jev-1.13.0`이다. Luna `low`, Sol `high`, 3회 cheap/8회 전체 budget, 프롬프트, scope 처리, 종료 규칙, margin 0.08을 유지했다. Q의 저장된 기본 모델은 바꾸지 않았다.

JEV도 같은 native System One `choice` API를 사용하므로 Go 실행 코드를 수정할 필요가 없었다. 실행 당시 metadata를 대조해 모든 Go source hash와 task manifest hash가 기존 OEV 실행과 일치함을 확인했다. 동시 실행 수는 동일하게 3이다.

기존 OEV·Sol·규칙 결과는 `heldout-v3`를 재사용했다. JEV는 추가 실행이므로 실행 시점, prompt cache, 서비스 부하, Luna/Sol sampling이 통제된 완전한 router ablation은 아니다. OEV는 로컬 네트워크 서비스, JEV는 원격 TypeSafe 서비스라는 차이도 있다. Margin 임계값을 JEV 결과에 맞춰 재조정하지 않았으므로 낮은 confidence에 따른 escalation까지 포함한 동일 정책 비교다.

### JEV 전체 결과

아래 횟수·시간은 task당 평균이다. Recall은 앞과 동일한 task별 macro 평균이다. 최초 80 trial과 추가 JEV 20 trial을 합쳐 총 100 trial이며, 서로 다른 실행 시점의 결과를 비교한다.

| 지표 | Sol 직접 | OEV + Luna → Sol | JEV + Luna → Sol | 규칙 + Luna → Sol |
|---|---:|---:|---:|---:|
| Gold file recall | 85.83% | 78.33% | 84.17% | 78.33% |
| Gold hunk recall | 73.42% | 56.38% | 63.83% | 47.38% |
| Candidate file recall | 88.33% | 80.83% | 88.33% | 80.83% |
| Gold 파일 하나 이상 발견 | 19/20 | 18/20 | 19/20 | 18/20 |
| Gold 파일 전부 발견 | 15/20 | 13/20 | 15/20 | 13/20 |
| Gold hunk 하나 이상 발견 | 18/20 | 16/20 | 17/20 | 14/20 |
| 전체 retrieval operations | 7.60 | 7.85 | 7.75 | 7.85 |
| 본문이 노출된 고유 파일 | 9.35 | 9.85 | 11.65 | 10.60 |
| 명시적으로 열어본 고유 파일 | 2.20 | 2.00 | 1.75 | 1.95 |
| 명시적 파일 읽기 operations | 3.30 | 2.85 | 2.50 | 2.95 |
| Sol 호출 | 7.85 | 5.30 | 5.65 | 5.10 |
| Decision 호출 | 0 | 2.70 | 2.70 | 0 |
| Luna query 호출 | 0 | 2.70 | 2.35 | 2.90 |
| Escalation task | 해당 없음 | 20/20 | 20/20 | 20/20 |
| Wall 평균, 초 | 54.27 | 50.76 | 48.42 | 49.47 |
| Wall 중앙값, 초 | 54.49 | 48.94 | 48.25 | 49.47 |
| Wall p90, 초 | 60.49 | 59.28 | 57.33 | 56.32 |

| 최초 발견까지, 성공한 task 조건부 평균 | Sol 직접 | OEV | JEV |
|---|---:|---:|---:|
| Gold 파일: operations / 본문 노출 파일 | 2.42 / 3.68 | 3.00 / 4.22 | 3.37 / 6.42 |
| Gold 파일: 명시적 open 파일 | 0.32 | 0.28 | 0.21 |
| Gold 파일: 끝내 발견 못한 task | 1 | 2 | 1 |
| Gold hunk: operations / 본문 노출 파일 | 3.22 / 5.17 | 4.31 / 6.69 | 3.94 / 6.88 |
| Gold hunk: 명시적 open 파일 | 0.72 | 0.75 | 0.47 |
| Gold hunk: 끝내 발견 못한 task | 2 | 4 | 3 |

JEV의 실패 집계는 API 실패 0, 빈 symbol을 만든 query의 IR 오류 3, 중복 query 차단 1로 총 4회다. 결과 0건 operations는 13회, 이후 새 evidence를 얻은 recovery는 11회다. 16개 task는 operation/call budget으로, 4개는 Sol의 `done`으로 종료했다. Cheap 단계만으로 종료한 task는 없다.

### JEV 토큰·호출·시간

| 모델, 20 task 합계 | Calls | Input tokens | Output tokens | Cached input | API 경과시간 합, 초 |
|---|---:|---:|---:|---:|---:|
| Sol high | 113 | 1,135,654 | 17,108 | 371,584 | 749.51 |
| Luna low | 47 | 237,012 | 2,173 | 161,024 | 197.33 |
| JEV 1.13.0 | 54 | 30,298 | 2,401 | 미제공 | 14.28 |

총 호출은 214회로 OEV 방식과 같고, Sol 호출 7회가 늘고 Luna 호출 7회가 줄었다. 총 input/output은 1,402,964/21,682이다. JEV의 output token은 API가 반환한 값이며, OEV가 output 0을 보고한 것과 임의로 같은 집계라고 가정하지 않았다. Reasoning token은 모두 미제공이다.

JEV decision의 관찰된 평균 API latency는 약 264ms, OEV는 약 654ms였다. 전체 task latency도 JEV가 평균 2.33초 짧았다. 그러나 Sol 호출이 더 많은데도 Sol API 경과시간 합은 작아졌으므로, end-to-end 시간 차이를 전부 router 속도의 효과로 설명할 수 없다. 실행 시점·cache·서비스 변동이 포함된 값이다. 실제 과금액은 비교하지 않았다.

### JEV가 바꾼 부분

첫 선택은 `regex_search` 12회, `exact_text` 8회였다. OEV의 첫 선택은 `exact_text` 20회였다. 전체 54개 선택은 regex 16, exact 15, references 13, definition 7, escalate 3이었고 filename은 없었다. 낮은 margin으로 4회 escalation했으며 이 중 2회는 첫 query를 만들기 전이었다. 2회 연속 새 source evidence가 없어 중단한 경우는 3회다. 결정 결과의 `escalate` 3회와 deterministic guard 사유를 구분해 로그에 남겼다.

| Sol 전환 전 cheap 단계 | OEV | JEV | 규칙 + Luna |
|---|---:|---:|---:|
| Gold file recall | 30.83% | 39.17% | 36.67% |
| Gold hunk recall | 6.83% | 18.96% | 6.46% |
| Gold hunk 하나 이상 발견 | 3/20 | 6/20 | 3/20 |
| Gold hunk 전부 발견 | 1/20 | 3/20 | 1/20 |

이번 실행에서는 JEV가 cheap 단계에서도 더 많은 gold evidence를 확보했다. 단순히 Sol을 더 호출한 뒤 최종 점수가 오른 것만은 아니다. 하지만 첫 gold 파일까지 노출한 파일 수와 전체 노출 파일 수는 늘었고, cheap 단계에서 gold hunk를 전부 찾은 3개 task도 종료하지 못했다. 검색 폭·본문 확장·종료 proxy의 trade-off가 남아 있다.

JEV−OEV file recall 차이는 +5.83%p, hunk recall은 +7.46%p였다. 짝지은 task bootstrap 95% 구간은 각각 [−8.33, +20.83], [−12.79, +28.00]%p로 넓다. 둘 다 첫 hunk를 찾은 공통 14개 task에서는 first-hit operation 차이가 평균 0회였다. 단독 조건부 평균 3.94 대 4.31만 보고 JEV의 first-hit 효율이 개선됐다고 결론 내리면 안 된다.

JEV는 baseline보다 Sol 호출이 28.0% 적지만 hunk recall은 9.58%p 낮았고, 노출 파일은 평균 2.30개 많았다. **JEV를 사용하면 OEV보다 나아질 가능성은 보였지만, routing 자체의 확실한 우위나 원래 가설의 충족을 입증한 결과는 아니다.** 정책을 고정한 이번 비교 이후에는, 본문 확장과 종료 판단을 먼저 고친 뒤 새로운 과제/반복 실행에서 두 router를 비교하는 것이 타당하다.

JEV에서 실제 cheap query로 이어진 decision의 margin별 hunk hit는 `<0.25` 2/14, `0.25–0.75` 4/23, `≥0.75` 2/9였다. 낮은 margin으로 실행하지 않은 결정은 이 분모에서 제외되므로 해당 구간의 실패율이나 guard의 이득을 직접 추정할 수 없다. Margin 0.08이 JEV에 최적인지는 검증하지 않았다.

JEV 결과는 기존 OEV 원시 결과를 덮어쓰지 않고 별도로 수집해 과제별로 비교했다. 보고서만 남기는 정리 과정에서 양쪽 원시 로그와 비교 코드는 제거했다.

## 개선 리서치

Query rewriting 연구는 질문과 검색 표현 간 차이를 별도 작은 rewriter로 처리하고 retrieval feedback으로 개선하는 접근을 제시한다. 이 실험에 적용할 다음 단계는 더 큰 planner가 아니라, 실패한 typed query와 검색 결과를 이용해 query prompt 또는 소형 rewriter를 개선하는 것이다. 해당 연구는 QA/web retrieval 실험이며, Q의 코드 검색에서 효과가 입증됐다는 뜻은 아니다. [Ma et al., EMNLP 2023](https://aclanthology.org/2023.emnlp-main.322/)

RouteLLM은 더 강한 모델의 상대적인 필요성을 데이터로 학습하고, 품질–호출 비용 trade-off와 router 자체 overhead를 함께 평가한다. Q에도 `choice` confidence만으로 성공을 가정하기보다 실제 downstream evidence gain과 추가 비용으로 routing 가치를 검증하는 접근이 맞다. 해당 논문은 모델 선택 routing이며, 여기의 검색 전략 선택과는 과제가 다르다. [Ong et al., RouteLLM](https://arxiv.org/abs/2406.18665)

OEV의 확률은 지정한 answer token 사이에서 정규화한 분포다. 로컬 OEV 소스의 choice confidence 식은 `(max(p) - 1/n) / (1 - 1/n)`이다. 높은 margin도 선택지가 부적절하면 잘못된 결정을 강하게 지지할 수 있다. Contextual calibration 연구는 내용 없는 입력으로 answer prior를 추정하고 prompt/label 편향을 줄이는 방식을 제시한다. 이를 OEV에 적용하려면 선택지 순서 교환·내용 없는 입력·held-out downstream 성공을 함께 확인해야 한다. 이번 실험에서는 학습이나 calibration까지 구현하지 않았다. [Zhao et al., ICML 2021](https://proceedings.mlr.press/v139/zhao21c.html)

위 연구와 이번 trace를 근거로 다음 실험은 아래 순서가 적절하다. **아래 효과는 아직 검증하지 않은 제안**이다.

1. **검색 후보의 본문 확장부터 분리 검증한다.** 검색 hit나 filename 후보에서 이미 관찰한 경로·줄 주변을 최대 60–100줄로 확장하는 `read_candidate`를 cheap executor에 추가한다. 전체 파일 목록에서 모델이 경로를 고르도록 하지 않는다. A/B/C 모두 같은 operation·byte budget을 적용하고, 자동 확장도 operation과 source 노출량에 센다. Declaration을 찾고도 Sol로 넘어가는 병목을 직접 겨냥한다.
2. **Scope와 query의 오류를 값싸게 복구한다.** 존재하지 않는 scope를 `rg --files`로 검사하고, 넓은 단어/잘린 결과에는 관찰한 identifier와 component를 이용해 좁힌다. Scope 검사도 retrieval 비용으로 기록한다. 구체적인 경로는 검색 결과에서 얻고, task별 정답 규칙을 넣지 않는다. 소형 모델을 더 호출하기 전에 validator·검색 상태만으로 고칠 수 있는 경우를 구분한다.
3. **첫 routing call 생략을 별도 ablation으로 측정한다.** Literal/identifier fast path를 유지하면서, 넓은 자연어 목적에 대한 첫 literal 검색 기본값이 다른 corpus에서도 충분한지 확인한다. 후속 전략에는 OEV를 남기고, 동일한 query 상태를 고정해 router 변경의 영향과 query sampling을 분리한다. 이번 corpus의 20/20 일치를 모든 문제의 최적 전략으로 일반화하지 않는다.
4. **종료 기준은 evidence 확장과 분리해 평가한다.** Keyword coverage만 완화하면 잘못된 조기 종료가 늘 수 있다. 개발용 gold로 proxy의 false-stop/불필요한 escalation을 분석하되 runtime에는 gold를 넣지 않는다. `선언 → 구현 본문`, 오류 조건·상태 전이 등 관찰 가능한 구조 신호가 단어 coverage보다 나은지 확인한다.
5. **그 뒤에 rewriter와 OEV calibration을 검토한다.** 실패한 query와 다음 성공 query를 개발용 예제로 만들고 task/subsystem을 분리한 평가에서 검증한다. OEV는 선택지 순서·answer prior에 대한 민감도부터 측정한다. 비용 목표를 `작은 모델 정확도` 하나가 아니라 `추가 gold evidence / operation·token`으로 둔다. Luna medium은 이번 비교에서 이득이 없었으므로 기본 해결책으로 채택하지 않는다.

품질 유지 여부는 새로운 평가 과제와 반복 실행에서 먼저 정한 recall 허용 손실·비용 기준으로 판단해야 한다. 이번 20개 결과를 보고 임계값을 조정한 뒤 같은 과제만 재평가하면 held-out 검증이 되지 않는다. 더 큰 인덱스나 범용 planner를 추가하기 전에 위 작은 ablation으로 병목을 확인할 수 있다.

## 기록 보존 범위 및 검증

보고서만 남기는 결정에 따라 실험 실행기·설정·분석 코드, 원시 JSONL과 집계 JSON, source 사본, 개발용·평가용 snapshot을 제거했다. 이 문서의 표와 해석은 삭제 전에 집계·검증한 값이다. 원시 로그가 남아 있지 않으므로 독립적인 재집계나 동일 실행기의 즉시 재실행은 불가능하다. 다시 실험하려면 위 방법과 아래 과제 목록을 바탕으로 실행기를 재구성해야 한다.

실행 당시 검색 primitive의 scope, 경로 제한, evidence byte budget, 정답 채점, query 중복 정규화, routing precondition, 종료 proxy에 대한 Go 테스트를 통과했다. 실제 API 연결, 최초 80개 비교 trial, JEV 20개 추가 trial, query probe 12개 호출을 완료했다. Snapshot 24개/5,481개 blob 검증도 통과했다. 제품 실행 경로는 바꾸지 않았으며 이를 제품 전체의 회귀 테스트 결과로 표현하지 않는다.

초기 `dev-v1`, `dev-v2`, 중단한 `heldout-v2`는 scope 오류로 무효 처리했다. 최초 비교표는 수정된 `heldout-v3`, JEV 비교는 같은 실행 코드와 정책의 `heldout-jev-v1`에 해당한다. 이 이름들은 실행 당시의 식별자이며 현재 디렉터리 경로가 아니다.

재구성 시 공통 설정은 Sol `high`, Luna `low`, cheap rounds 3, 전체 retrieval operations 8, 큰 모델 호출 상한 9, 생성 API timeout 120초, margin 0.08, coverage 0.8, worker 3이다. OEV/JEV의 decision API에는 기존 System One client의 30초 HTTP timeout을 사용했다. Hosted model 변동, 모델 출력의 확률성, prompt cache, 서비스 부하 때문에 같은 조건이라도 동일한 수치를 보장하지 않는다.

## Benchmark 과제 목록

아래 commit의 parent 상태에서 해당 subject/body가 설명하는 기존 구현과 control flow를 찾도록 요청했다. 공통 objective 문장은 다음과 같고, 그 뒤에 해당 commit 메시지를 붙였다. 모델에 gold 파일 목록이나 diff는 제공하지 않았다.

> Locate the existing implementation responsible for this requested behavior fix, and retrieve source evidence for the relevant control flow. Do not implement it.

앞 4개는 개발용/query probe용, 나머지 20개는 모든 비교군이 공유한 평가용이다. Gold evidence 줄 구간은 앞서 설명한 수정 전 diff hunk 규칙으로 정의했다.

| Task | 분할 | Commit | Subject | Gold 구현 파일 |
|---|---|---|---|---|
| task-01 | 개발 | `8fadb78c3ebe` | fix(workspace): tolerate Windows session file replacement | `internal/fsreplace/replace_windows.go`, `workspace/session.go` |
| task-02 | 개발 | `10ebb2aee777` | fix(studio): keep streaming across guidance redirects | `studio/runs.go` |
| task-03 | 개발 | `ec5371f5164e` | fix(systemone): removed inline provider API key storage | `app/model_state.go`, `app/systemone.go`, `systemoneconfig/config.go` |
| task-04 | 개발 | `f524970e7df2` | fix(app): persist manual compaction before activating context | `app/acp.go`, `app/chat_events.go` |
| task-05 | 평가 | `89ee38cf7ee5` | fix(app): kept configured tools available in delegation mode | `app/delegation.go`, `app/loop_mode.go` |
| task-06 | 평가 | `6d4660423894` | fix(app): reserved skill tools for delegation coordination | `app/loop_mode.go` |
| task-07 | 평가 | `3beb38748777` | fix: preserve active task lifecycle through compaction | `agentloop/context.go` |
| task-08 | 평가 | `017b5991da8a` | fix: retry service health after listener collision | `library/runtime.go`, `workspacememory/runtime.go` |
| task-09 | 평가 | `88358757c447` | fix(sessionstore): preserve archive indexes across embedding changes | `sessionstore/store.go`, `workspacememory/client.go` |
| task-10 | 평가 | `4b32c8e5a91b` | fix(context): resume compacted tool loops with user message | `app/acp.go`, `app/agent_context.go` |
| task-11 | 평가 | `0e5eff3e0870` | fix(skills): prefer confident semantic matches | `library/skills.go`, `sessionstore/search.go`, `tools/builtin/skills.go` |
| task-12 | 평가 | `c01a110fcf7f` | fix(skills): refresh workspace discovery | `agentskills/registry.go`, `tools/runtime.go` |
| task-13 | 평가 | `1204b58dfab6` | fix: make subagent details scrollable | `app/custom_subagent_ui.go`, `app/custom_subagent_view.go` |
| task-14 | 평가 | `f02c7e138d94` | fix(plan): preserve selected user choices | `app/planning_log.go`, `subagent/planning.go` |
| task-15 | 평가 | `162d5d4e4f41` | fix(chat): recover from empty provider responses | `app/agent_context.go`, `app/model.go` |
| task-16 | 평가 | `6d937a8ff65a` | fix(session): omitted Loom-backed tool results from archives | `subagent/lifecycle.go` |
| task-17 | 평가 | `20e24f83e446` | fix(thinker): omit absolute paths from knowledge | `thinker/context.go`, `thinker/runner.go` |
| task-18 | 평가 | `1fb42ac3f35a` | fix(thinker): retain future-actionable knowledge | `thinker/context.go`, `thinker/runner.go` |
| task-19 | 평가 | `50d2cae4fc5e` | fix(thinker): learn from completed research | `thinker/context.go`, `thinker/runner.go` |
| task-20 | 평가 | `874df4e71e54` | fix(workspace): hardened session persistence on Windows | `app/acp.go`, `app/model.go` |
| task-21 | 평가 | `f6cb4df9a697` | fix(memory): applied compacted summaries above the target | `memory/manager.go` |
| task-22 | 평가 | `05c416e4ccba` | fix(memory): allowed compacted context up to twenty percent | `memory/manager.go` |
| task-23 | 평가 | `79fa72405749` | fix(app): accepted Ctrl-J as an alternate newline shortcut | `app/model.go` |
| task-24 | 평가 | `ba3ec08e636a` | fix(tools): hardened file anchors and directory creation | `tools/builtin/edit.go`, `tools/builtin/files.go`, `tools/builtin/register.go` |
