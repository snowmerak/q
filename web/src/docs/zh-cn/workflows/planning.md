---
locale: zh-cn
title: 通过委派规划与实施
description: 使用职业角色子代理处理需求、计划、实施和审查。
sectionLabel: 指南
toc:
  - id: 委派任务
    label: 委派任务
  - id: 角色
    label: 角色
---

## 委派任务

在工作区会话中输入 `/mode delegation`，然后描述期望的结果。主代理可以委派 manager 处理需求和计划，并委派 senior developer 指导实施。`q sprint <request...>` 可在没有交互式 UI 的情况下运行一项委派任务。

## 角色

manager 是负责需求、优先级和验收标准的 PM 职位。interviewer 整理需要用户决定的问题，researcher 调查具体问题。senior developer 使用 `reviewer` 模型角色，将有明确范围的实现任务交给 junior developer，直接审查实际修改和验证结果，并在需要时要求修正。junior developer 使用 `coder` 模型角色。
