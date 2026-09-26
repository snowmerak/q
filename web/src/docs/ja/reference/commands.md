---
locale: ja
title: コマンド
description: q の主要ワークフローを制御する対話コマンドと単独コマンドを確認します。
sectionLabel: リファレンス
toc:
  - id: チャットコマンド
    label: チャットコマンド
  - id: 単独コマンド
    label: 単独コマンド
  - id: 基本キー
    label: 基本キー
---

## チャットコマンド

| コマンド | 用途 |
| --- | --- |
| `/plan [request]` | 計画を明確化、提案、承認、実行、レビューします。 |
| `/mode [default\|delegation]` | 現在のチャットループモードを確認または変更します。 |
| `/auto-resolve [on\|off\|status]` | 計画上の質問への既定回答を制御します。 |
| `/auto-approve [on\|off\|status]` | 有効な計画案の自動承認を制御します。 |
| `/autonomous [on\|off\|status]` | 両方の計画自動化設定をまとめて制御します。 |
| `/changes` | ステージ済み、未ステージ、未追跡の変更を確認します。 |
| `/commit` | コミット案を生成してレビューします。 |
| `/sessions` | 保存された別のワークスペースセッションを開きます。 |
| `/new` | 新しいセッションを作成して切り替えます。 |
| `/clear` | 現在の会話プロジェクションと計画チェックポイントをクリアします。 |
| `/learn [on\|off\|status]` | 永続学習をチェックポイントまたは制御します。 |
| `/model` | モデルを割り当て、フォールバックグループを設定します。 |
| `/gateway` | プロバイダーと Gateway リスナーを設定します。 |
| `/library` | グローバル Library リスナーを設定します。 |
| `/loom` | Loom ストレージと GC を確認・設定します。 |
| `/ignore` | `.qignore` の探索ルールを編集します。 |
| `/skills` | グローバルおよびワークスペースの Agent Skills を管理します。 |
| `/subagents` | 組み込み・カスタム・外部エージェントを管理します。 |
| `/subagent <name> <request>` | 範囲を限定したサブエージェント依頼を実行します。 |
| `/mcp` | 外部 MCP サーバーを設定します。 |
| `/lsp` | 言語サーバーのプロファイルとルートを設定します。 |
| `/help` | コマンドとキーのガイドを開きます。 |

## 単独コマンド

| コマンド | 用途 |
| --- | --- |
| `q sprint <request...>` | 自律的な計画ワークフローを一回実行します。 |
| `q remote` | フォアグラウンドの Remote REST ホストを起動します。 |
| `q remote config` | Remote リスナーと API キーを設定します。 |
| `q gateway` | 単独 Gateway を設定します。 |
| `q gateway start` | OpenAI 互換 Gateway を起動します。 |
| `q library` | グローバル Library リスナーを設定します。 |
| `q library start` | グローバル Library をフォアグラウンドで稼働させます。 |
| `q memory` | Workspace Memory を独立して稼働させます。 |
| `q usage` | ローカルのトークン使用量ダッシュボードを開きます。 |
| `q commit` | 単独コミットワークフローを開きます。 |
| `q model` | モデルとロール割り当てを設定します。 |
| `q subagents` | プロファイルと ACP バインドを管理します。 |
| `q skills` | Agent Skills を管理します。 |
| `q mcp` | 外部 MCP サーバーを設定します。 |
| `q lsp` | 言語サーバーとワークスペースルートを設定します。 |
| `q ignore` | `.qignore` を編集します。 |
| `q help` | チャットサービスなしでコマンドとキーのガイドを開きます。 |
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
