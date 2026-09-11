# 모델 토큰 사용량 트레이스

Q가 소유한 model client 호출은 성공 응답에서 token count만 추출해 user-level Usage
service에 기록한다. 기본 dashboard는 다음 명령으로 연다.

```powershell
q usage
```

`q usage`는 `http://127.0.0.1:17893/`을 기본 endpoint로 사용한다. 실행 중인 compatible
service가 없으면 현재 process가 leader가 되어 Ctrl-C까지 SQLite와 dashboard를
소유하고, 있으면 같은 service에 연결한다.

대상은 main loop, ACP, Q 내부 subagent, Thinker, Librarian, commit agent와 embedding
호출이다. `codex`, `lms`, `ollama`, `grok` 같은 provider를 이름으로 분기하지 않고
OpenAI 호환 응답으로 들어오는 동일한 `usage` 계약을 사용한다. Q 밖에서 실행되는
external agent의 내부 호출은 Q가 provider 응답을 소유하지 않으므로 기록하지 않는다.

## 기록 계약

각 물리 provider 호출은 다음 필드만 가진 event 한 건이 된다.

| 필드 | 의미 |
| --- | --- |
| `event_id` | transport retry에서 재사용하는 random idempotency ID |
| `at` | 응답을 관측한 UTC 시각 |
| `model` | 응답 model, 없으면 요청 model |
| `role` | `main`, `planner`, `griller`, `scout`, `coder`, `thinker`, `librarian`, `commit`, `embedding` 또는 `unknown` |
| `prompt_tokens` | 입력 token 수 |
| `completion_tokens` | 출력 token 수. embedding은 `0` |
| `total_tokens` | 전체 token 수 |
| `cached_tokens` | provider가 보고한 prompt cache read token 수 |
| `cache_write_tokens` | provider가 보고한 prompt cache write token 수 |
| `estimated` | 하나 이상의 기본 token 수를 로컬에서 추정했는지 여부 |
| `cache_estimated` | provider 응답에 cache breakdown이 없었는지 여부 |

prompt/completion 본문, tool argument, endpoint, API key, 비용은 기록하지 않는다. 160K
token 호출도 본문을 복사하지 않으므로 정수 count를 가진 event 한 행이다.

정상 chat 응답과 생성된 뒤 종료되거나 닫힌 stream은 각각 한 번 기록한다. 빈 stream도
prompt 추정값을 남기므로 상위 retry가 버린 호출을 확인할 수 있다. HTTP 단계에서
거절되어 model 응답 자체가 없는 호출은 기록하지 않는다.

## 원격값과 추정값

provider가 반환한 `prompt_tokens`, `completion_tokens`, `total_tokens` 및 cache
breakdown을 우선 사용한다. 기본 token count가 없으면 Q의 보수적인 JSON byte 기반
추정기를 사용하고 `estimated: true`를 남긴다. 이 값은 호출량 추적용이며 provider의
과금 tokenizer와 정확히 일치한다는 보장은 없다.

cache read/write는 응답에 값이 있을 때만 정확히 알 수 있다. breakdown이 없으면 두
값은 `0`이고 `cache_estimated: true`다. 따라서 이 `0`은 "cache를 사용하지 않았다"가
아니라 "provider가 값을 보고하지 않았다"일 수 있다.

## 소유권, 보존과 실패

여러 Q process는 파일 락으로 하나의 Usage service leader를 선출한다. leader만
`~/.q/usage/usage.sqlite`를 열고 다른 process의 Recorder는 bounded loopback HTTP로
proxy한다. event ID의 unique constraint와 같은 transaction의 daily rollup update로
응답 손실 뒤 retry도 한 번만 집계된다.

최근 90일 raw event는 SQLite에 둔다. 더 오래된 complete UTC day는
`~/.q/usage/archive/YYYY/MM/usage-YYYY-MM-DD.parquet`로 옮기고, 전체 기간 daily
rollup은 SQLite에 계속 둔다. 기존 `~/.q/logs/model-usage/usage-*.jsonl`은 startup에서
idempotent하게 import하지만 자동 삭제하지 않는다.

recording에는 최대 250ms deadline이 있고 실패는 model call의 결과를 바꾸지 않는다.
service가 내려갔거나 leader가 교체된 경우 Recorder는 stable event ID로 한 번 다시
연결을 시도한다. 자세한 storage, archive recovery, API와 dashboard 계약은
[`model-usage-dashboard.md`](model-usage-dashboard.md)를 참고한다.
