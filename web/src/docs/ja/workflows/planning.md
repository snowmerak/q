---
locale: ja
title: 委任作業
description: 職務別サブエージェントで要件、調査、実装、レビューを調整します。
sectionLabel: ガイド
toc:
  - id: 作業を始める
    label: 作業を始める
  - id: 役割とモデル
    label: 役割とモデル
  - id: 以前の-plan-モード
    label: 以前の plan モード
---

## 作業を始める

ワークスペースのセッションで求める結果を伝えます。デフォルトループはワークスペースツールを直接使いながら、manager に要件と計画を、research に調査を、senior developer に技術作業やレビューを委任できます。エージェントの順序は固定されていません。

PM の作業だけを依頼する場合は、チャットで `/subagent builtin/manager <request>` を使います。

## 役割とモデル

manager は PM として要件、優先順位、受け入れ基準、必要な作業計画を担当します。interviewer はユーザーの判断が必要な質問を整理し、research は特定の問題を調査します。各ロールは関連するワークスペースの根拠を直接読めます。

senior developer は `reviewer` モデルロールと編集・コマンドツールを使います。自分でコードを変更するか、junior developer に範囲を決めた実装を任せ、実際の変更と検証結果をレビューして必要なら修正を依頼できます。junior developer も `coder` モデルロールと編集・コマンドツールを使います。独立した `builtin/reviewer` サブエージェントは廃止されたため、技術レビューは `builtin/senior-developer` に依頼します。

## 以前の plan モード

`/plan` と `/auto-approve`、`/auto-resolve`、`/autonomous` は作業を開始しません。計画は通常の委任で manager に依頼します。既存の plan チェックポイントは過去のデータとして残りますが、新しい plan 実行として再開されません。
