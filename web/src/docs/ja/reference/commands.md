---
locale: ja
title: コマンド
description: q の主要ワークフローを制御するチャットコマンドと CLI コマンドを確認します。
sectionLabel: リファレンス
toc:
  - id: チャットコマンド
    label: チャットコマンド
  - id: cli-コマンド
    label: CLI コマンド
  - id: 基本キー
    label: 基本キー
---

## チャットコマンド

| コマンド | 用途 |
| --- | --- |
| `/changes` | ステージ済み、未ステージ、未追跡の変更を確認します。 |
| `/commit` | コミット案を生成してレビューします。 |
| `/sessions` | 保存された別のワークスペースセッションを開きます。 |
| `/new` | 新しいセッションを作成して切り替えます。 |
| `/clear` | 現在の会話プロジェクションをクリアします。 |
| `/learn [on\|off\|status]` | 永続学習をチェックポイントまたは制御します。 |
| `/model` | モデルを割り当て、フォールバックグループを設定します。 |
| `/gateway` | プロバイダーと Gateway リスナーを設定します。 |
| `/systemone` | System One のプロバイダー、decision モデル、キー、リスナーを設定します。 |
| `/library` | グローバル Library リスナーを設定します。 |
| `/loom` | Loom ストレージと GC を確認・設定します。 |
| `/ignore` | `.qignore` の探索ルールを編集します。 |
| `/skills` | グローバルおよびワークスペースの Agent Skills を管理します。 |
| `/subagents` | 組み込み・カスタム・外部エージェントを管理します。 |
| `/subagent <name> <request>` | 範囲を限定したサブエージェント依頼を実行します。 |
| `/mcp` | 外部 MCP サーバーを設定します。 |
| `/lsp` | 言語サーバーのプロファイルとルートを設定します。 |
| `/help` | コマンドとキーのガイドを開きます。 |


## CLI コマンド

| コマンド | 用途 |
| --- | --- |
| `q studio [--port <port>] [--no-open]` | ループバックで組み込み Studio Web インターフェースを起動します。 |
| `q gateway` | Studio で Gateway のプロバイダーとリスナー設定を開きます。 |
| `q gateway start` | OpenAI 互換 Gateway を起動します。 |
| `q systemone` | Studio で System One のプロバイダー、decision モデル、キーを開きます。 |
| `q systemone start [--host <ip>] [--port <port>]` | System One decision API を起動します。 |
| `q library` | Studio でグローバル Library リスナー設定を開きます。 |
| `q library start` | グローバル Library をフォアグラウンドで稼働させます。 |
| `q memory` | Workspace Memory を独立して稼働させます。 |
| `q usage` | Studio の Operations を開きます。 |
| `q commit` | 現在のリポジトリを Studio の Changes とコミットレビューで開きます。 |
| `q model` | Studio でグローバルとワークスペースのモデル割り当てを開きます。 |
| `q subagents` | Studio でサブエージェントと ACP バインディングを開きます。 |
| `q skills` | Studio で Agent Skills を開きます。 |
| `q mcp` | Studio で MCP 設定を開きます。 |
| `q lsp` | Studio で言語サーバーとワークスペースルートを開きます。 |
| `q ignore` | Studio で現在のリポジトリの `.qignore` を開きます。 |
| `q help` | Studio Help を開きます。 |
| `q acp [flags]` | q を stdin/stdout の ACP サーバーとして実行します。 |

`q agents` は `q subagents` の互換エイリアスです。

## 基本キー

| キー | 動作 |
| --- | --- |
| `Enter` / `Ctrl+S` | 現在のメッセージを送信します。 |
| `Shift+Enter` | 改行を挿入します。 |
| `Ctrl+O` | ツール結果の本文を折りたたみ／展開します。 |
| `Ctrl+G` | サブエージェントトレースを折りたたみ／展開します。 |
| `Ctrl+H` | ヘルプを開閉します。 |
| `Ctrl+C` | 実行中のターンを中断し、待機中は終了します。 |
| `Esc` | 現在の画面を離れるかチャットを終了します。 |
