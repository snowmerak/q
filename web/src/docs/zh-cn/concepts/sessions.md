---
locale: zh-cn
title: 会话与记忆
description: 了解 q 如何跨工作区运行保存、压缩、学习和恢复状态。
sectionLabel: 概念
toc:
  - id: 持久会话
    label: 持久会话
  - id: 记录与上下文
    label: 记录与上下文
  - id: 委派恢复
    label: 委派恢复
  - id: workspace-memory
    label: Workspace Memory
  - id: 学习
    label: 学习
---

## 持久会话

每个工作区都可以在 `.q/sessions/` 下保存多个基于 UUID 的会话。启动时的选择器会显示会话标题与近期活动。

一个进程占用一个选定会话。另一个 q 进程可以在同一工作区打开不同会话，但会话锁不会串行化对仓库文件的编辑。

## 记录与上下文

q 将用户可见的完整记录与发给模型的压缩请求上下文分别保存。如果具备上下文元数据，q 可以缩短模型上下文，同时保留完整的用户可见记录。

主要会话记录为：

```text
.q/sessions/<uuid>/session.json
```

获批计划的执行还会在旁边写入可恢复的 `plan-execution.json`。

会话 v2 使用 Chat Completions 和 Responses 共用的消息格式。现有 v1 Chat Completions 会话在读取时转换，并在下次保存时写入新格式。选定的聊天循环模式和适用时的 Responses 重放状态也会保存。

## 委派恢复

委派模式下，每个子调用的书签保存在 `delegations.json`，子会话保存在 `delegates/<invocation-id>/`。子代理也可以继续委派。重启后，q 从最深层的子会话开始恢复，再将保存的结果返回给父调用。

没有记录结果的普通工具调用会以 `unknown` 返回给代理，不会自动重试。中断的外部 ACP 子代理也会返回 `unknown`，因为其内部轮次无法恢复。`/plan` 仍使用独立的执行检查点。

## Workspace Memory

Workspace Memory 保存持久消息、工具活动、失败记录、生命周期事件与搜索索引。检索历史时，无须在单一聊天视图中承载每一条事件。

大型工具结果作为不可变 Loom 产物保存。提示中只放入有长度限制的收据，模型之后可以仅检查相关片段。

## 学习

成功完成轮次后，Thinker 可以提取可复用的命题：已确立的工作流、持久的项目事实、可复用的解决办法，以及可能影响未来工作的有依据发现。

Librarian 决定在全局 Library 中创建、合并还是丢弃命题。`/learn off` 会停止收集和处理队列，但不会删除已入队的数据。

某次运行特有的构建、测试、格式化和审计快照保留在任务历史中，不会变成持久项目事实。
