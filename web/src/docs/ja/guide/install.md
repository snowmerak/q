---
locale: ja
title: q のインストール
description: Go で q をインストールするかソースからビルドし、対象のワークスペースで起動します。
sectionLabel: ガイド
toc:
  - id: 要件
    label: 要件
  - id: goでインストール
    label: Go でインストール
  - id: ソースからインストール
    label: ソースからインストール
  - id: インストールせずに実行
    label: インストールせずに実行
  - id: ワークスペースで起動
    label: ワークスペースで起動
---

## 要件

q をインストールする前に、次のものを用意してください。

- Go 1.26.5 以降
- `PATH` から実行できる Git
- 設定済みのモデルプロバイダーが一つ以上
- ANSI カラー対応のターミナル

[Task](https://taskfile.dev/) は任意です。必要なビルドとテストはすべて Go だけで実行できます。

## Goでインストール

Go モジュールから q を直接インストールします。

```powershell
go install github.com/snowmerak/q/cmd/q@latest
```

任意の `q-mcp` コンパニオンも同じ方法でインストールできます。

```powershell
go install github.com/snowmerak/q/cmd/q-mcp@latest
```

Go はバイナリを `GOBIN` に配置し、未設定なら `GOPATH/bin` を使います。そのディレクトリを `PATH` に追加してください。

## ソースからインストール

現在のソースをビルドする場合や q にコントリビュートする場合は、リポジトリをクローンします。

```powershell
git clone https://github.com/snowmerak/q.git
cd q
go install ./cmd/q ./cmd/q-mcp
```

これは Go モジュールプロキシから `@latest` を解決せず、チェックアウトしたソースから両方のコマンドをインストールします。

## インストールせずに実行

ソースチェックアウトから q を直接起動できます。

```powershell
go run ./cmd/q
```

一般的な開発作業向けの Task ターゲットもあります。

```powershell
task run
task build
task test
```

## ワークスペースで起動

q がワークスペースとして扱うリポジトリまたはディレクトリへ移動して起動します。

```powershell
cd C:\path\to\project
q
```

初回起動ではプロバイダー設定が開きます。モデルを割り当てたら通常どおり依頼を入力するか、`/` でコマンドを検索してください。
