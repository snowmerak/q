---
locale: zh-cn
title: 安装 q
description: 使用 Go 安装 q，或从源码构建，然后启动本地 Studio。
sectionLabel: 指南
toc:
  - id: 要求
    label: 要求
  - id: 使用go安装
    label: 使用 Go 安装
  - id: 从源码安装
    label: 从源码安装
  - id: 无需安装直接运行
    label: 无需安装直接运行
  - id: 启动-studio
    label: 启动 Studio
---

## 要求

安装 q 前，请确保具备以下条件：

- Go 1.26.5 或更高版本
- `PATH` 中可用的 Git
- 至少一个已配置的模型提供商
- 用于 Studio 的现代浏览器

[Task](https://taskfile.dev/) 是可选的。所有必要的构建和测试命令都能直接通过 Go 运行。

## 使用Go安装

从 Go 模块直接安装 q：

```powershell
go install github.com/snowmerak/q/cmd/q@latest
```

可选的 `q-mcp` 伴随程序也可以同样安装：

```powershell
go install github.com/snowmerak/q/cmd/q-mcp@latest
```

Go 将二进制文件写入 `GOBIN`；未设置时写入 `GOPATH/bin`。请确保该目录位于 `PATH` 中。

## 从源码安装

如果要构建当前源码或参与 q 开发，请克隆仓库：

```powershell
git clone https://github.com/snowmerak/q.git
cd q
go install ./cmd/q ./cmd/q-mcp
```

这会从已检出的源码安装两个命令，而不是通过 Go 模块代理解析 `@latest`。

## 无需安装直接运行

在源码检出目录中直接启动 q：

```powershell
go run ./cmd/q studio
```

仓库还包含常用开发任务的 Task 目标：

```powershell
task run
task build
task test
```

## 启动 Studio

从任意目录启动 Studio，然后在 **Sessions** 中注册仓库：

```powershell
q studio
```

在 **Settings → Providers** 中添加提供商，并在 **Settings → Models** 中分配默认模型。bare `q` 仍可作为终端兼容客户端使用。
