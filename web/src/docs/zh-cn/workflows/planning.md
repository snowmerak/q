---
locale: zh-cn
title: 规划与执行
description: 对需要澄清、执行和审查的工作使用 q 的审批工作流。
sectionLabel: 指南
toc:
  - id: 开始规划
    label: 开始规划
  - id: 工作流角色
    label: 工作流角色
  - id: 自动化控制
    label: 自动化控制
  - id: 中断后恢复
    label: 中断后恢复
---

## 开始规划

当工作需要澄清、明确审批、拆分为可审查的任务，或需要在中断后恢复时，请使用 `/plan`。

```text
/plan 将当前缓存替换为容量受限的 LRU 实现
```

普通对话也可以直接使用工具，不会自动进入规划工作流。

非交互调用可以运行相同工作流，并仅为该进程启用自动化：

```powershell
q sprint 将当前缓存替换为容量受限的 LRU 实现
```

## 工作流角色

默认流程如下：

1. **Griller** 只询问无法从仓库证据中决定的问题。
2. **Scout** 在需要时进行有范围限制且不修改文件的调查。
3. **Planner** 提出任务、目标、执行者、完成条件与验证方法。
4. **Executor** 通过 Coder 或已配置的外部执行者完成批准的任务。
5. **Planner 审查** 接受结果或提供次数受限的重试反馈。

任务按顺序执行，并共享有上限的尝试次数。执行停止时，已经修改的文件不会自动回滚。

## 自动化控制

`/auto-resolve` 决定 q 是否以工程默认策略回答规划问题，`/auto-approve` 决定是否自动批准有效的 Planner 方案。`/autonomous` 同时更改两项设置。

```text
/auto-resolve on
/auto-approve on
/autonomous status
```

自动化不会跳过方案验证、任务执行或 Planner 审查。

## 中断后恢复

当前执行会在所选会话下保存检查点：

```text
.q/sessions/<uuid>/plan-execution.json
```

重新启动后，q 提供 Resume、Inspect 和 Discard。Discard 只移除检查点，不会撤销执行者已经修改的文件。
