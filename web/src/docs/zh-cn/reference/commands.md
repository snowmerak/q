---
locale: zh-cn
title: 命令
description: 查找用于控制 q 主要工作流的聊天命令和 CLI 命令。
sectionLabel: 参考
toc:
  - id: 聊天命令
    label: 聊天命令
  - id: cli-命令
    label: CLI 命令
  - id: 常用按键
    label: 常用按键
---

## 聊天命令

| 命令 | 用途 |
| --- | --- |
| `/changes` | 浏览已暂存、未暂存和未跟踪的更改。 |
| `/commit` | 生成并审查提交提案。 |
| `/sessions` | 打开另一个已保存的工作区会话。 |
| `/new` | 创建并切换到新会话。 |
| `/clear` | 清除当前会话视图。 |
| `/learn [on\|off\|status]` | 检查点或控制此工作区的持久学习。 |
| `/model` | 分配模型并配置回退组。 |
| `/gateway` | 配置提供商和 Gateway 监听设置。 |
| `/systemone` | 配置 System One 提供商、decision 模型、密钥和监听设置。 |
| `/library` | 配置全局 Library 监听设置。 |
| `/loom` | 查看 Loom 存储并配置垃圾回收。 |
| `/ignore` | 编辑 `.qignore` 中的工作区发现规则。 |
| `/skills` | 管理全局和工作区 Agent Skills。 |
| `/subagents` | 管理内置、自定义和外部代理。 |
| `/subagent <name> <request>` | 运行一次范围明确的子代理请求。 |
| `/mcp` | 配置外部 MCP 服务器。 |
| `/lsp` | 配置语言服务器配置文件和根目录。 |
| `/help` | 打开完整的命令和按键指南。 |


## CLI 命令

| 命令 | 用途 |
| --- | --- |
| `q studio [--host <ip>] [--port <port>] [--no-open]` | 启动内置 Studio Web 界面。默认使用 `127.0.0.1` 和随机端口。 |
| `q gateway` | 在 Studio 中打开 Gateway 提供商和监听设置。 |
| `q gateway start` | 启动兼容 OpenAI 的 Gateway。 |
| `q systemone` | 在 Studio 中打开 System One 提供商、decision 模型和密钥。 |
| `q systemone start [--host <ip>] [--port <port>]` | 启动 System One decision API。 |
| `q library` | 在 Studio 中打开全局 Library 监听设置。 |
| `q library start` | 让全局 Library 作为前台服务持续运行。 |
| `q memory` | 让 Workspace Memory 独立运行。 |
| `q usage` | 打开 Studio 的 Operations。 |
| `q commit` | 在 Studio Changes 和提交审查中打开当前仓库。 |
| `q model` | 在 Studio 中打开全局和工作区模型分配。 |
| `q subagents` | 在 Studio 中打开子代理配置和 ACP 绑定。 |
| `q skills` | 在 Studio 中打开 Agent Skills。 |
| `q mcp` | 在 Studio 中打开 MCP 配置。 |
| `q lsp` | 在 Studio 中打开语言服务器和工作区根目录。 |
| `q ignore` | 在 Studio 中打开当前仓库的 `.qignore` 编辑器。 |
| `q help` | 打开 Studio Help。 |
| `q acp [flags]` | 通过 stdin/stdout 将 q 作为 ACP 服务器运行。 |

`q agents` 是 `q subagents` 的兼容别名。

## 常用按键

| 按键 | 动作 |
| --- | --- |
| `Enter` / `Ctrl+S` | 发送当前消息。 |
| `Shift+Enter` | 插入换行。 |
| `Ctrl+O` | 折叠或展开工具结果正文。 |
| `Ctrl+G` | 折叠或展开子代理跟踪。 |
| `Ctrl+H` | 打开或关闭帮助。 |
| `Ctrl+C` | 中断当前轮次；空闲时退出。 |
| `Esc` | 离开当前界面或退出聊天。 |
