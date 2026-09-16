---
locale: zh-cn
title: ACP 与外部 MCP
description: 将 q 作为 ACP 代理运行，并将外部 MCP 工具服务器连接到选定模型角色。
sectionLabel: 指南
toc:
  - id: 通过acp运行q
    label: 通过 ACP 运行 q
  - id: 控制规划自动化
    label: 控制规划自动化
  - id: 交互与上下文
    label: 交互与上下文
  - id: 连接外部mcp服务器
    label: 连接外部 MCP 服务器
  - id: 传输边界
    label: 传输边界
---

## 通过ACP运行q

通过 stdin/stdout 将 q 启动为 Agent Client Protocol 服务器：

```powershell
q acp --root C:\work\project
```

ACP 模式使用与终端 UI 相同的持久工作区会话、受根目录限制的工具、规划工作流、子代理、学习和提交工作流。它向已连接客户端通告 `/plan`、`/commit`、`/subagents`、`/subagent`、`/learn`、`/clear` 和 `/help` 等命令。

`--root` 默认为当前目录，定义文件、会话、指令、技能和工作区配置的边界。

## 控制规划自动化

进程级标志可以自动处理规划澄清与方案审批，而不更改持久配置：

```powershell
q acp --root C:\work\project --auto-resolve --auto-approve
q acp --root C:\work\project --autonomous
```

`--autonomous` 同时启用两项行为。明确提供的单独标志优先，包括 `--auto-approve=false`。这些标志作用于 `/plan`，不会跳过方案验证、任务执行、审查或提交确认。

斜杠命令 `/auto-resolve`、`/auto-approve`、`/autonomous` 支持 `on`、`off`、`status`。与进程标志不同，`on` 和 `off` 会保存到 `~/.q/config.yaml`。

## 交互与上下文

客户端支持表单式询问时，q 会将其用于规划与提交选择。否则会呈现编号审批选项。规划与代理问题可以把下一条客户端消息作为自由格式回答。

q 会通告 ACP 嵌入式上下文支持，并在回放会话时保留资源 URI、MIME 类型、注释和内容。当 q TUI 连接到另一个 ACP 代理时，可用 `@relative/path` 或 `@"path with spaces"` 附加工作区内文件。远端支持嵌入上下文时会发送文件内容，否则发送资源链接。

## 连接外部MCP服务器

打开 `/mcp` 或运行 `q mcp` 配置外部 MCP 服务器。q 支持本地 stdio 进程和 Streamable HTTP 端点。服务器可以只分配给需要其工具的角色，无需暴露给每个模型请求。

导入的工具名带有命名空间，以避免与 q 内置工具及其他服务器冲突。结果经过与内置工具相同的 Loom 捕获边界，过大的内容会成为有界的工件引用。

凭据配置将子进程环境变量或 HTTP 标头映射到源环境变量名，无需在 `~/.q/mcp.json` 中写入密钥值。

## 传输边界

ACP 客户端提供的 MCP 服务器只属于该 ACP 会话，不会成为 q 的全局配置。支持 stdio 和 Streamable HTTP，不支持 SSE 传输。

MCP 工具使用其自身进程或远程服务的权限。q 可以限制结果内容和可见的模型角色，但不能把任意外部服务器变成操作系统沙箱。
