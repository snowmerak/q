# Bleve + HNSW session store notes

## 상태

이 문서는 workspace의 `.q` 아래에 채팅과 agent 실행 데이터를 계속 축적하고,
Bleve를 이용해 검색하는 세션 저장소의 설계 메모다.

`sessionstore` 패키지에 1차 저장소 기반을 구현했다. 공통 record의 atomic JSON
저장, 명시적인 Bleve mapping, text/구조/시간 검색, 최신성 재정렬, 순수 Go HNSW
vector 검색과 전체 index rebuild를 지원한다. 아직 앱 및 subagent runner에는
연결하지 않았으며 append-only run event, blob과 embedding 생성 pipeline은 후속
범위다.

## 목표

- 현재 채팅뿐 아니라 완료된 run, task, agent 결과와 중요한 tool output을 보존한다.
- 앱 재시작 후 진행 중이던 orchestration을 복구할 수 있게 한다.
- 과거 사용자 요청, 결정, 조사 결과와 구현 결과를 text 및 vector로 검색한다.
- 시간, 역할, 상태와 run 같은 구조화된 조건으로 결과를 제한할 수 있게 한다.
- 검색 인덱스가 손상되거나 형식이 바뀌어도 원본 데이터에서 다시 만들 수 있게 한다.

이 저장소는 단순한 임시 디렉터리가 아니라 workspace-local 실행 아카이브다.
자동으로 완료 데이터를 삭제하지 않으며 명시적인 정리 정책이나 명령은 추후
추가한다.

## 저장 구조 초안

```text
.q/
├─ session.json                 # 현재 채팅을 빠르게 복구하기 위한 projection
├─ data/
│  ├─ records/                  # 1차 구현의 공통 record 원본 JSON
│  ├─ runs/
│  │  └─ <run-id>/
│  │     ├─ manifest.json
│  │     ├─ events.jsonl
│  │     └─ tasks/
│  │        └─ <task-id>.json
│  └─ blobs/
│     └─ <sha256>
└─ index/
   ├─ bleve/                      # text, metadata, datetime
   ├─ vectors.hnsw                # 파생 HNSW graph
   ├─ vectors.ids.json            # graph numeric ID -> record ID
   └─ state.json
```

`data`가 source of truth다. Bleve와 HNSW는 파생 데이터이며 언제든 삭제하고 다시
만들 수 있어야 한다. `session.json`은 현재 UI용 projection으로 유지하되 장기
이력의 유일한 원본으로 사용하지 않는다.

1차 구현은 record ID의 SHA-256 값을 파일명으로 사용해 path traversal과 파일명
충돌을 피한다. run별 `events.jsonl`과 projection이 추가되기 전까지 모든 공통
record를 `data/records`에 둔다.

큰 context, tool output과 결과 본문은 content-addressed blob으로 분리하는 방식을
고려한다. run과 task record에는 blob hash와 논리적인 artifact 참조를 저장한다.
동일한 내용을 여러 task가 사용하는 경우 중복 저장을 피할 수 있다.

## 공통 record envelope

검색과 lifecycle 복구에 필요한 공통 필드를 먼저 정의하고, record 종류별 payload를
추가한다.

```go
type Record struct {
    ID        string
    Kind      string
    Version   int
    RunID     string
    TaskID    string
    ParentID  string
    Role      string
    Model     string
    Effort    string
    Status    string
    CreatedAt time.Time
    UpdatedAt time.Time
    Summary   string
    Content   string
    Refs      []string
    Tags      []string
}
```

초기 `Kind` 후보는 다음과 같다.

- `message`: root 또는 agent 대화 메시지
- `task`: agent task의 현재 projection
- `event`: lifecycle과 tool 실행 이벤트
- `result`: `task_complete`로 반환한 구조화 결과
- `question`: `ask_to_user` 질문과 사용자 응답
- `artifact`: 파일, symbol, URL과 blob 참조
- `summary`: context compaction 또는 run 요약

ID와 시간은 생성 후 바꾸지 않는다. record schema와 Bleve mapping은 각각 version을
가져야 한다.

## 저장 및 복구 원칙

1. lifecycle event를 `events.jsonl`에 append한다.
2. task와 run의 현재 projection은 임시 파일을 쓴 뒤 atomic replace한다.
3. 큰 본문은 blob을 먼저 안전하게 기록한 뒤 record가 참조하게 한다.
4. 원본 기록이 성공한 다음 Bleve 색인을 요청한다.
5. 색인 실패가 원본 저장 실패로 전파되어서는 안 된다.
6. `index/state.json`에 마지막으로 반영한 event sequence와 mapping version을 둔다.
7. 시작 시 누락 event를 다시 색인하거나 전체 rebuild를 수행할 수 있어야 한다.

진행 중인 task는 최소한 `queued`, `running`, `waiting_user`, `succeeded`,
`blocked`, `failed`, `cancelled` 상태를 구분한다. `ask_to_user`를 호출하면 질문과
대기 상태를 저장하고, 응답을 기록한 뒤 실행을 재개한다. `task_complete`는 caller
task의 terminal outcome과 결과를 저장한다.

`/clear`는 현재 채팅 projection을 비우되 `.q/data`의 장기 archive는 삭제하지
않는 방향으로 한다. archive 삭제와 index rebuild는 별도 명령으로 제공한다.

## Bleve와 HNSW의 역할

Bleve는 다음 기능을 담당한다.

- message, summary, result와 artifact의 full-text 검색
- `run_id`, `task_id`, `kind`, `role`, `status`, `tags` 조건 검색
- `created_at`과 `updated_at` 범위 필터 및 정렬
- HNSW는 embedding이 준비된 record의 approximate cosine 검색
- 애플리케이션 계층은 text와 vector 결과의 RRF hybrid fusion

Bleve v2의 document type별 mapping을 사용하고 동적 mapping에만 의존하지 않는다.
필드의 초기 mapping 후보는 다음과 같다.

| 필드 | mapping | 용도 |
|---|---|---|
| `id`, `run_id`, `task_id`, `parent_id` | keyword | 정확 일치와 join 참조 |
| `kind`, `role`, `status`, `model`, `effort`, `tags` | keyword | filter와 facet |
| `summary`, `content` | text | BM25 full-text 검색 |
| `created_at`, `updated_at` | datetime + doc values | 범위, 정렬, 시간 가중치 |
| `embedding` | record JSON 원본 | HNSW rebuild와 semantic search |
| `refs` | keyword 또는 별도 artifact document | 역참조 검색 |

Bleve mapping과 index format이 바뀌면 `.q/index/bleve`를 새로 만드는 방식을
기본 migration으로 사용한다. 원본 record를 인덱스 내부 stored field에만 두지
않는다.

참고 문서:

- [Bleve index mapping](https://blevesearch.com/docs/Index-Mapping/)
- [Bleve query types](https://blevesearch.com/docs/Query/)
- [Bleve sorting](https://blevesearch.com/docs/Sorting/)
- [Bleve v2.6.0 release](https://github.com/blevesearch/bleve/releases/tag/v2.6.0)

## 시간 기반 검색

`created_at`은 문자열의 동적 mapping이 아니라 명시적인 datetime field로
정의한다. 이를 통해 다음 세 방식을 지원한다.

### 범위 필터

`DateRangeQuery`를 text query와 conjunction으로 결합해 특정 시점 이후나 기간
내의 record만 검색한다. 이 필터는 hard constraint로 취급한다.

### 정렬

- `created_at` 내림차순: 최신 record 우선
- `_score` 내림차순 후 `created_at` 내림차순: relevance 우선, 동점이면 최신 우선
- `created_at` 내림차순 후 `_score` 내림차순: freshness 우선

정렬에 사용하는 datetime field는 색인되어 있어야 한다.

### recency 가중치

Bleve 2.6의 `custom_score` query는 doc values를 callback에 불러와 hit의 점수를
변경할 수 있다. `created_at`에 half-life 기반 decay를 적용하는 방식을 고려한다.

```text
recency = exp(-ln(2) * age / half_life)
score   = relevance * (1 + weight * recency)
```

기본 `weight`는 낮게 두어 오래된 정확한 결과가 최신의 관련 없는 결과보다 항상
밀리지 않게 한다. half-life와 weight는 record kind별로 다르게 둘 수 있다.
예를 들어 사용자 결정은 천천히 감쇠시키고 일시적인 tool 결과는 빠르게
감쇠시킨다.

`custom_score`는 candidate마다 callback을 실행하므로 넓은 query에서는 비용을
측정해야 한다. 명시적인 datetime mapping과 doc values가 필요하다.

- [Bleve custom filter/custom score](https://github.com/blevesearch/bleve/pull/2289)

## vector와 hybrid 검색

text와 vector 검색은 서로 다른 강점을 가진다.

- BM25: 정확한 identifier, 파일명, symbol과 오류 메시지
- vector: 표현이 달라진 과거 결정, 유사한 문제와 요약
- structured filter: 현재 workspace, role, kind, status와 시간 범위

초기 hybrid 흐름은 다음과 같이 한다.

```text
BM25 candidates ─┐
                 ├─ RRF 또는 RSF ── recency rerank ── top K
vector candidates┘
          ▲
     structured filters
```

현재 구현은 Bleve와 HNSW에서 각각 후보를 얻고 애플리케이션 계층에서 RRF로
합친 뒤 recency reranking을 적용한다. text query 내부의 `custom_score`만 사용하면
vector와 fusion하는 과정에서 시간 가중치가 희석될 수 있기 때문이다. 실제
candidate 수, half-life와 fusion 파라미터는 corpus를 축적한 뒤 조정한다.

embedding 생성에 실패하거나 embedding model이 설정되지 않은 record도 text
검색은 가능해야 한다. embedding model ID, vector dimension과 생성 시각을 함께
보존하고 모델이 바뀌면 background re-embedding을 지원한다.

embedding model과 dimension은 개인 config에 명시적으로 저장한다.

```yaml
embedding:
  model: openai/text-embedding-3-small
  dimensions: 1536
```

`/model`의 `embedding` target에서 `/v1/models` 카탈로그의 모델을 고른 뒤
1~4096 범위의 dimension을 입력한다. 현재 공통 model metadata는 embedding
capability와 기본 dimension을 광고하지 않으므로 자동 추론하지 않는다. 이 값은
embedding request와 HNSW `VectorConfig`의 `model`, `dimensions`에 동일하게
사용해야 한다. 어느 한쪽이 바뀌면 vector state 불일치로 보고 graph를 다시 만든다.
새 모델의 embedding이 없는 기존 record는 text 검색에는 계속 포함되며 background
re-embedding 대상이 된다.

HNSW backend는 `goformersearch`의 순수 Go 구현을 사용한다. graph는
`.q/index/vectors.hnsw`, numeric ID mapping은 `.q/index/vectors.ids.json`에 atomic
replace로 저장한다. 현재 backend는 delete/update를 직접 지원하지 않으므로 record의
embedding이 변경되거나 제거되면 원본 record에서 vector graph만 다시 만든다.
신규 record는 graph에 증분 추가한다. 이 선택으로 `vectors` build tag, CGO와
`libfaiss_c`가 필요하지 않다.

## 동시성과 운영

- 원본 store write는 run 단위 sequence를 부여한다.
- Bleve update와 HNSW graph 저장은 원본 write와 하나의 transaction으로 간주하지 않는다.
- 하나의 index writer 또는 직렬화된 indexing queue로 동시 update를 제어한다.
- 검색은 index update와 병행할 수 있어야 한다.
- 앱 비정상 종료 후 마지막 불완전 JSONL record를 감지할 수 있어야 한다.
- index open 실패, mapping version 불일치와 sequence gap은 rebuild 대상으로 본다.
- tool output에 credential이 포함될 수 있으므로 저장과 색인 전 redaction 정책이
  필요하다.

## 초기 구현 단계 후보

현재 완료한 범위:

- 공통 record envelope와 종류별 payload 저장
- `.q/data/records`의 atomic source write
- 명시적인 Bleve text, keyword, datetime mapping
- text, structured filter, inclusive created-at range와 정렬 검색
- half-life 기반 애플리케이션 레벨 recency reranking
- 색인 실패 후 source 보존, 시작 시 catch-up과 전체 rebuild
- Record embedding 원본, HNSW vector index와 BM25/vector RRF fusion

후속 구현 순서:

1. run, task, event와 blob 참조 schema를 구체화한다.
2. append-only event store와 run/task projection을 구현한다.
3. 진행 중 run load와 `waiting_user` 복구 테스트를 작성한다.
4. 현재 chat과 subagent runner의 lifecycle을 저장소에 연결한다.
5. 현재 chat과 agent record의 embedding 생성 pipeline을 추가한다.
6. model 변경 시 background re-embedding을 통합 테스트한다.
7. TUI 검색, archive 관리와 index rebuild 명령을 추가한다.
8. 여러 process의 file lock, 보존 기간과 redaction 정책을 구현한다.

## 결정이 필요한 사항

- event sequence를 run-local 또는 workspace-global로 둘지
- JSONL record의 framing과 마지막 불완전 write 복구 방식
- blob 압축, 최대 크기와 inline threshold
- raw tool output의 기본 보존 범위와 secret redaction
- 검색 analyzer와 한국어/코드 tokenization 정책
- embedding provider, model, dimension과 batch 정책
- RRF와 RSF 중 기본 fusion 방식
- record kind별 recency half-life와 weight
- archive 정리, export와 삭제 명령
- 여러 q process가 같은 workspace를 열 때의 file/index lock 정책
