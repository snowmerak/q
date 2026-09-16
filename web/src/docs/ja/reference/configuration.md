---
locale: ja
title: 設定
description: 個人設定、ワークスペース状態、プロファイル、再構築可能なインデックスの場所を確認します。
sectionLabel: リファレンス
toc:
  - id: 個人の状態
    label: 個人の状態
  - id: ワークスペースの状態
    label: ワークスペースの状態
  - id: 正本データ
    label: 正本データ
---

## 個人の状態

| パス | 用途 |
| --- | --- |
| `~/.q/config.yaml` | メインモデル、ロール、コンテキスト、Loom、LSP の設定 |
| `~/.q/providers.json` | 管理対象 Gateway のプロバイダーとモデルメタデータ |
| `~/.q/gateway.json` | 単独 Gateway のリスナーとキーメタデータ |
| `~/.q/gateway.key` | Gateway API キー検証用の非公開マスターキー |
| `~/.q/remote.json` | Remote リスナー、認証スイッチ、キーメタデータ |
| `~/.q/remote.key` | Remote API キー検証用の非公開マスターキー |
| `~/.q/library.json` | Global Library のループバックリスナー設定 |
| `~/.q/workspace-memory.json` | Workspace Memory のループバックリスナー設定 |
| `~/.q/usage.json` | トークン Usage サービスのループバックリスナー設定 |
| `~/.q/mcp.json` | 外部 MCP プロファイルとロール割り当て |
| `~/.agents/skills/` | q が管理しないポータブルなグローバル Agent Skills |
| `~/.q/skills/` | q 管理のグローバル Agent Skills |
| `~/.q/subagents/` | グローバルなカスタムサブエージェントプロファイル |
| `~/.q/logs/thinker/` | 短期間の Thinker 呼び出し診断 |
| `~/.q/usage/usage.sqlite` | 最近のトークンイベントと日次集計 |
| `~/.q/usage/archive/` | 古い生の使用量イベントの Parquet アーカイブ |

通常の設定は TUI から行ってください。自動化が必要な場合にだけ直接編集することを推奨します。

## ワークスペースの状態

| パス | 用途 |
| --- | --- |
| `.q/sessions/<uuid>/session.json` | 会話、コンテキスト、タイトル、ライフサイクル、学習状態 |
| `.q/sessions/<uuid>/plan-execution.json` | 再開可能な承認済み計画チェックポイント |
| `.q/plan-executions/` | 完了した実行スナップショット |
| `.q/model.json` | ワークスペースのモデルロール上書き |
| `.q/learning.json` | ワークスペース学習スイッチ |
| `.q/lsp.json` | ワークスペース LSP ルートと上書き |
| `.q/data/` と `.q/index/` | 永続レコードと派生インデックス |
| `.q/loom/` | 内容アドレス型ツールアーティファクトと GC メタデータ |
| `.agents/skills/` | q が管理しないワークスペース Agent Skills |
| `.q/skills/` | q 管理のワークスペース Agent Skills |
| `.q/subagents/` | ワークスペースのカスタムサブエージェントプロファイル |
| `AGENTS.md` | ワークスペースと入れ子のパス指示 |
| `.qignore` | 探索除外ルール |

## 正本データ

JSON レコードが正本です。Bleve と HNSW インデックスは派生データで再構築できます。

Loom GC は設定された猶予期間に従い、アクティブなセッションプロジェクションと計画チェックポイントから参照されるものを保護します。ワークスペースの `.q` を削除すると q の永続履歴は失われますが、リポジトリファイルは元に戻りません。
