---
locale: zh-cn
title: 子代理
description: 运行不继承隐藏对话状态的内置、自定义及外部代理。
sectionLabel: 指南
toc:
  - id: 查看可用代理
    label: 查看可用代理
  - id: 执行单次请求
    label: 执行单次请求
  - id: 在聊天中委派
    label: 在聊天中委派
  - id: 定义内部代理
    label: 定义内部代理
  - id: 外部代理
    label: 外部代理
---

## 查看可用代理

打开 `/subagents` 查看内置定义并管理自定义配置。列表会显示每个配置的执行类型、模型角色、范围、工具和委派许可。

公开的内置 ID 包括：

- `builtin/scout`
- `builtin/griller`
- `builtin/planner`
- `builtin/executor`
- `builtin/reviewer`
- `builtin/coder`
- `builtin/web-search`
- `builtin/web-tester`

## 执行单次请求

请在请求中提供全部必要上下文。子代理不会自动继承父对话。

```text
/subagent builtin/scout 解释 app/model.go 中的取消处理
```

在 TUI 中，自定义配置使用简短名称。配置中存储的委派许可则使用 `builtin/scout`、`global/code-reader` 或 `workspace/browser-check` 等规范 ID。

## 在聊天中委派

普通聊天默认使用 `default` 模式，主代理可直接调用工具。要将工作交给有明确范围的子代理，请在当前会话输入 `/mode delegation`。输入 `/mode default` 可恢复直接使用工具的循环。模式会保存在会话中，独立于需要提案批准和执行的 `/plan`。

对话中会显示子代理进度和工具调用；按 `Ctrl+G` 展开或收起详细记录。每次调用都会保存父会话书签和子会话。重启后，q 先恢复最深层的子会话，再继续父会话。没有记录结果的工具调用会返回 `unknown`，不会自动重试。中断的外部 ACP 调用也会返回 `unknown`，因为其内部轮次无法恢复。

## 定义内部代理

内部配置会选择 q 模型角色、明确的工具列表以及可直接调用的委派对象。

```yaml
version: 1
name: code-reader
description: 解释所请求的代码。
kind: inner
role: scout
system_prompt: |
  阅读所请求的代码，并通过具体文件位置解释其行为。
tools:
  - list_directory
  - read_file
delegates:
  - builtin/scout
```

配置文件存放在 `~/.q/subagents/` 或 `<workspace>/.q/subagents/`。同名的工作区配置会整体替代全局配置。

## 外部代理

外部配置绑定到已启用的 ACP 连接。它保存系统提示以及远程代理能否修改工作区，但不选择 q 工具、委派对象或 q 模型角色。

ACP 创建会话的协议没有系统消息字段，因此 q 会把保存的系统提示附加在首次普通 ACP 请求之前。连接缺失或被禁用时，配置会暂时不可用，而无需删除它。
