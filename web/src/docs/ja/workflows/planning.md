---
locale: ja
title: 委任による計画と実装
description: 職務別サブエージェントで要件、計画、実装、レビューを進めます。
sectionLabel: ガイド
toc:
  - id: 作業を委任する
    label: 作業を委任する
  - id: 役割
    label: 役割
---

## 作業を委任する

ワークスペースのセッションで `/mode delegation` を入力し、求める結果を伝えます。メインエージェントは manager に要件と計画を、senior developer に実装の指導を委任できます。`q sprint <request...>` は対話型 UI なしで一件の委任作業を実行します。

## 役割

manager は PM として要件、優先順位、受け入れ基準を担当します。interviewer はユーザーの判断が必要な質問を整理し、researcher は特定の問題を調査します。senior developer は `reviewer` モデルロールを使い、junior developer に範囲を決めた実装を任せ、実際の変更と検証結果を直接レビューして必要なら修正を依頼します。junior developer は `coder` モデルロールを使います。
