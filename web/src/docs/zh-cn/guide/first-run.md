---
locale: zh-cn
title: 首次运行
description: 启动 Studio、配置模型、创建仓库会话并审查结果。
sectionLabel: 指南
toc:
  - id: 启动-studio
    label: 启动 Studio
  - id: 配置模型
    label: 配置模型
  - id: 创建会话
    label: 创建会话
  - id: 发送第一个请求
    label: 发送第一个请求
  - id: 审查结果
    label: 审查结果
---

## 启动 Studio

启动内置的本地 Web 应用。无论从哪个目录启动，都可以管理任意仓库中的会话。

```powershell
q studio
```

Studio 会输出回环 URL 并在浏览器中打开。使用界面时，请保持该 q 进程运行。

## 配置模型

在 **Settings → Providers** 中添加 Gateway 提供商，并填写 endpoint 与 API 密钥环境变量。然后在 **Settings → Models** 中将发现的模型分配给 **Default**。提供商与模型更改会自动保存。

仅当 Agent Skill 相关性检查等工作需要独立的 typed decision 提供商时，才需配置 **Settings → System One**。

## 创建会话

在 **Sessions** 中选择 **Add session**。通过文件夹浏览器选择仓库目录，然后注册已有根会话或创建新会话。Studio 会记住每个根会话的仓库位置。

每个会话都在注册时指定的准确工作区目录中运行。

## 发送第一个请求

先发送一个具体的仓库问题，以观察 q 如何收集依据：

```text
说明这个项目的启动路径和主要运行时组件。
```

需要专业工作时，可让主代理进行委派，或明确指定职业型角色。子代理显示在会话树的父节点下，选择后可查看其独立对话和工具活动。

## 审查结果

在 **Changes** 中查看已暂存、未暂存、重命名和未跟踪文件，以及高亮 diff。准备完成后启动提交审查，生成 Conventional Commit 或拆分提交提案。在执行所选提案前不会创建提交。
