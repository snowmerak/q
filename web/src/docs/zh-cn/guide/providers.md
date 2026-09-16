---
locale: zh-cn
title: 选择提供商
description: 连接模型提供商，并为 q 的原生角色分配模型。
sectionLabel: 指南
toc:
  - id: 支持的提供商
    label: 支持的提供商
  - id: 配置gateway
    label: 配置 Gateway
  - id: 分配模型角色
    label: 分配模型角色
  - id: 配置嵌入模型
    label: 配置嵌入模型
  - id: 回退组
    label: 回退组
---

## 支持的提供商

q 管理的 Gateway 支持：

- 包括本地服务器在内的 OpenAI 兼容 HTTP API
- OpenRouter
- xAI 和 Grok
- Anthropic 原生 Messages API
- 使用当前 Codex 登录状态的本地 Codex App Server

## 配置Gateway

在 q 中打开 `/gateway` 添加提供商和监听设置。普通 q 会话会在临时回环端口上管理专属的 Gateway 子进程。

提供商修改会先启动替代进程，再切换到新设置。如果替代进程无法启动，q 会继续使用正在运行的配置。

尽可能通过环境变量引用凭据。全局提供商定义存放于 `~/.q/providers.json`。

## 分配模型角色

打开 `/model`，为主对话和 `griller`、`scout`、`planner`、`executor`、`coder`、`commit`、`thinker`、`librarian` 等专门角色分配模型。

在分配表中按 `a` 可以创建可复用的自定义角色。自定义子代理可以选用该角色，同时保留自身的工具与委派许可。

工作区覆盖设置存于 `.q/model.json`，全局分配存于 `~/.q/config.yaml`。

## 配置嵌入模型

模型分配界面也包含全局 `embedding` 目标。选择支持嵌入的模型，并输入该模型要求的向量维度。q 接受 1 至 4096 的维度。

Workspace Memory 和 Library 维护共享的派生索引，因此嵌入配置属于全局设置，不能按工作区覆盖。分配或更换模型会重新配置可重建的 HNSW 投影，并使用新模型为当前 Agent Skills 补建向量。清除该目标后，技能和历史检索只使用 BM25。

所选提供商必须支持嵌入请求，维度也必须匹配模型。嵌入设置失败不会导致底层 JSON 记录不可用。

## 回退组

角色可以引用有序模型组。候选模型超时或返回暂时性的 HTTP 5xx 时，q 会尝试下一个候选。

用户取消、工具失败和验证错误不会触发回退。此时应修改请求、工具状态或配置，而不是更换模型端点。
