---
locale: ja
title: サブエージェント
description: 隠れた会話状態を継承しない、範囲付きの組み込み・カスタム・外部エージェントを実行します。
sectionLabel: ガイド
toc:
  - id: 利用可能なエージェントを確認
    label: 利用可能なエージェントを確認
  - id: 一つの依頼を実行
    label: 一つの依頼を実行
  - id: チャットから委任
    label: チャットから委任
  - id: 内部エージェントを定義
    label: 内部エージェントを定義
  - id: 外部エージェント
    label: 外部エージェント
---

## 利用可能なエージェントを確認

Studio の **Settings → Subagents** で組み込み定義を確認し、カスタムプロファイルを管理します。一覧には実行種別、モデルロール、スコープ、ツール、委任許可が表示されます。

公開されている組み込み ID は次のとおりです。

- `builtin/interviewer`
- `builtin/manager`
- `builtin/senior-developer`
- `builtin/junior-developer`
- `builtin/research`
- `builtin/web-search`
- `builtin/web-tester`

## 一つの依頼を実行

子エージェントは親の会話を自動継承しないため、必要なコンテキストを依頼にすべて含めます。

```text
/subagent builtin/senior-developer app/model.go のキャンセル処理をレビューして
```

bare q 互換チャットから `/subagent` を明示的に呼ぶ場合は、カスタムプロファイルの短い名前を使います。プロファイルに保存する委任許可には `builtin/senior-developer`、`global/code-reader`、`workspace/browser-check` のような正規 ID を使います。

## チャットから委任

通常のチャットでは、ワークスペースツールと委任を同時に利用できます。メインエージェントは依頼に応じて直接作業するか、範囲の定まったサブエージェントを調整します。manager は PM として要件と計画を担当し、senior developer は自分で変更するか junior developer に実装を任せ、その結果をレビューします。役割を明示する場合は `/subagent <name> <request>` を使います。

Studio は各子をセッションツリーの親の下に表示します。子を選ぶと進行状況、会話、ツール呼び出しが開きます。各呼び出しは親のブックマークと子セッションに保存されます。再起動後は最も深い子から復元し、親を続行します。結果の記録がないツール呼び出しは自動再実行せず、`unknown` として返します。中断された外部 ACP 呼び出しも内部ターンを再開できないため `unknown` を返します。

clean な Git branch に紐づく保存済み session では、変更可能な inner child は local の `q/delegate/<invocation-id>` branch と linked worktree で作業します。成功すると Q が残りの変更を commit し、base/head commit を固定した内部 Change Request を返します。呼び出し元は diff を読み、merge または close します。入れ子の child も同じ流れを使うため、senior developer は junior developer の branch をレビューして merge した後、自分の Change Request を上位へ返せます。Studio の session tree には request の状態と branch が表示されます。この local flow に remote push は不要です。

すべての inner task result には短い `summary` があり、任意の `report` に最終分析、設計根拠、レビューコメント、調査結果を Markdown で最大 256 KiB まで記録できます。大きな結果は Loom に保存され、呼び出し元は reference と制限付き preview を受け取り、必要に応じて完全な report を取得します。

## 内部エージェントを定義

内部プロファイルは q のモデルロール、明示的なツール一覧、直接呼び出せる委任先を選択します。

```yaml
version: 1
name: code-reader
description: 依頼されたコードを説明します。
kind: inner
role: advisor
system_prompt: |
  依頼されたコードを読み、具体的なファイル参照とともに動作を説明してください。
tools:
  - list_directory
  - read_file
delegates: []
```

プロファイルは `~/.q/subagents/` または `<workspace>/.q/subagents/` に置きます。同名のワークスペースプロファイルはグローバルプロファイル全体を置き換えます。


## 外部エージェント

外部プロファイルは有効な ACP 接続へバインドします。システムプロンプトと、リモートエージェントがワークスペースを変更できるかを保存しますが、q のツール、委任先、モデルロールは選びません。

ACP のセッション作成にはシステムメッセージ欄がないため、q は保存したシステムプロンプトを最初の通常 ACP リクエストへ追加します。接続がないか無効な場合、プロファイルを削除せず利用不可になります。
