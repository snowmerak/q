---
locale: ja
title: リモート API
description: 認証付きのフォアグラウンド HTTP サービスで q セッションと単発のエージェント依頼を実行します。
sectionLabel: ガイド
toc:
  - id: 設定と起動
    label: 設定と起動
  - id: ワークスペースの状態を取得
    label: ワークスペースの状態を取得
  - id: エージェントを実行
    label: エージェントを実行
  - id: ストリームイベント
    label: ストリームイベント
  - id: エラーと実行容量
    label: エラーと実行容量
  - id: リモートの境界
    label: リモートの境界
---

## 設定と起動

リスナー、認証スイッチ、Remote 専用 API キーを設定します。

```powershell
q remote config
```

次にフォアグラウンドホストを起動します。

```powershell
q remote
```

既定のリスナーは `127.0.0.1:0` です。Remote は q プロセスがアクセスできる任意のディレクトリを選び、ワークスペース変更ツールを実行できるため、Remote キーは Gateway キーと分離されています。

## ワークスペースの状態を取得

作業ディレクトリのセッションを列挙します。

```powershell
curl.exe -H "Authorization: Bearer $env:Q_REMOTE_KEY" "http://127.0.0.1:8080/v1/sessions?working_directory=C%3A%5Cwork%5Cproject"
```

同じワークスペースで利用できる組み込み・カスタムサブエージェントを列挙します。

```powershell
curl.exe -H "Authorization: Bearer $env:Q_REMOTE_KEY" "http://127.0.0.1:8080/v1/subagents?working_directory=C%3A%5Cwork%5Cproject"
```

## エージェントを実行

`POST /v1/subagent-runs` は JSON を受け取り、`application/x-ndjson` イベントをストリームします。

```powershell
curl.exe -N `
  -H "Authorization: Bearer $env:Q_REMOTE_KEY" `
  -H "Content-Type: application/json" `
  -d '{"working_directory":"C:\\work\\project","prompt":"起動経路を説明して"}' `
  http://127.0.0.1:8080/v1/subagent-runs
```

`working_directory` と `prompt` は必須です。`session_id` を省けば新しいセッションを作成し、指定すると占有されていない既存セッションを再開します。`subagent` を省くか空にするとデフォルトのメインループを実行します。`builtin/scout` のような ID を指定すると直接サブエージェントフローになります。

プロンプトの上限は UTF-8 で 32 KiB、JSON リクエスト本文全体は 256 KiB です。未知の JSON フィールドは拒否されます。レスポンスは `Cache-Control: no-store` を使い、`X-Q-Session-ID` ヘッダーで選択セッションを示します。

## ストリームイベント

最初の NDJSON レコードは必ず `session` です。`working_directory`、`session_id`、`created` から正規化されたワークスペースと新規セッションかどうかが分かります。

| 種別 | 主なフィールド | 意味 |
| --- | --- | --- |
| `status` | `detail` | 起動警告または簡潔なランタイム状態 |
| `activity` | `agent`, `task_id`, `parent_id`, `action`, `detail` | サブエージェントの進行状況 |
| `trace` | `agent`, `kind`, `call_id`, `name`, `content`, `is_error` | 詳細なサブエージェントトレース |
| `tool_call` | `call_id`, `name`, `content` | メインエージェントのツール要求。`content` は引数です。 |
| `message` | `role`, `name`, `call_id`, `content`, `is_error` | セッションへ追加されたモデルまたはツールメッセージ |
| `question` | `question`, `context` | 対話質問の試行。Remote からは回答できません。 |
| `result` | `session_id`, `outcome`, `content` | 成功した最終結果 |
| `cancelled` | — | キャンセル後の最終レコード |
| `error` | `error` | ストリーム開始後の最終エラー |

クライアントは不要なフィールドを無視し、最終レコードまで読み続けます。切断するとリクエストコンテキストとアクティブな実行がキャンセルされます。

## エラーと実行容量

ストリーム開始前の失敗は JSON の `error` オブジェクトと HTTP ステータスで返されます。

| 状態 | コード | 意味 |
| --- | --- | --- |
| `400` | `invalid_request` | JSON、ディレクトリ、セッション ID、Content-Type、リクエスト形式が不正 |
| `401` | `invalid_api_key` | 認証が有効で bearer キーがないか無効 |
| `404` | `session_not_found` または `subagent_not_found` | セッションまたはサブエージェントが存在しない |
| `409` | `session_busy` | 別プロセスが選択セッションを占有中 |
| `413` | `request_too_large` | 本文またはプロンプトが上限超過 |
| `429` | `remote_capacity` | アクティブ実行が `agents.max_parallel` に到達 |
| `503` | `subagent_unavailable` | エージェントはあるがランタイムが利用不可 |

`GET /v1/health` はサービスバージョンと新規実行を受け付けられるかを報告します。このエンドポイントと `/openapi.json` は bearer キーなしでアクセスでき、ワークスペース状態は公開しません。

## リモートの境界

Remote は `ask_to_user` をツール一覧に残しますが、ストリームは一方向です。q は試行した `question` を送った後、対話不可のツールエラーをすぐ返します。モデルは利用可能な情報で続行するか、ブロックされた状態で終了します。

Remote には組み込み TLS、パス許可リスト、バックグラウンドジョブ、再接続、対話回答がありません。認証と信頼できる機密ネットワークまたはリバースプロキシがなければループバック上だけで使ってください。

稼働中のサービスの正確な契約は `/openapi.json` で確認できます。
