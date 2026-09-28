---
locale: zh-cn
title: 委派工作
description: 通过职业角色子代理协调需求、调研、实施和审查。
sectionLabel: 指南
toc:
  - id: 开始任务
    label: 开始任务
  - id: 角色与模型
    label: 角色与模型
  - id: 旧版-plan-模式
    label: 旧版 plan 模式
---

## 开始任务

在工作区会话中描述期望的结果。默认循环可以直接使用工作区工具，也可以委派 manager 处理需求和计划、research 调查具体问题、senior developer 处理技术工作或审查。代理的执行顺序并不固定。

如只需 PM 工作，可在聊天中使用 `/subagent builtin/manager <request>`。

## 角色与模型

manager 是负责需求、优先级、验收标准及必要工作计划的 PM 职位。interviewer 整理需要用户决定的问题，research 调查具体问题。每个角色都能直接读取相关工作区证据。

senior developer 使用 `reviewer` 模型角色以及编辑和命令工具。它可以直接修改代码，也可以将有明确范围的实现任务交给 junior developer，然后审查实际修改和验证结果，并在需要时要求修正。junior developer 也使用 `coder` 模型角色以及编辑和命令工具。独立的 `builtin/reviewer` 子代理已移除；技术审查请交给 `builtin/senior-developer`。

## 旧版 plan 模式

`/plan`、`/auto-approve`、`/auto-resolve` 和 `/autonomous` 不再启动任务。现在可在普通委派中请 manager 制定计划。现有 plan 检查点仅作为历史数据保留，不会作为新 plan 任务恢复。
