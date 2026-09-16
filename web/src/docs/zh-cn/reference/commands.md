---
locale: zh-cn
title: 命令
description: 查找用于控制 q 主要工作流的交互命令和独立命令。
sectionLabel: 参考
toc:
  - id: 聊天命令
    label: 聊天命令
  - id: 独立命令
    label: 独立命令
  - id: 常用按键
    label: 常用按键
---

## 聊天命令

| 命令 | 用途 |
| --- | --- |
| `/plan [request]` | 明确、提议、批准、执行和审查计划。 |
| `/auto-resolve [on\|off\|status]` | 控制计划澄清问题的工程默认回答。 |
| `/auto-approve [on\|off\|status]` | 控制有效计划提案的自动批准。 |
| `/autonomous [on\|off\|status]` | 同时控制上述两项计划自动化设置。 |
| `/changes` | 浏览已暂存、未暂存和未跟踪的更改。 |
| `/commit` | 生成并审查提交提案。 |
| `/sessions` | 打开另一个已保存的工作区会话。 |
| `/new` | 创建并切换到新会话。 |
| `/clear` | 清除当前会话视图和计划检查点。 |
| `/learn [on\|off\|status]` | 检查点或控制此工作区的持久学习。 |
| `/model` | 分配模型并配置回退组。 |
| `/gateway` | 配置提供商和 Gateway 监听设置。 |
| `/library` | 配置全局 Library 监听设置。 |
| `/loom` | 查看 Loom 存储并配置垃圾回收。 |
| `/ignore` | 编辑 `.qignore` 中的工作区发现规则。 |
| `/skills` | 管理全局和工作区 Agent Skills。 |
| `/subagents` | 管理内置、自定义和外部代理。 |
| `/subagent <name> <request>` | 运行一次范围明确的子代理请求。 |
| `/mcp` | 配置外部 MCP 服务器。 |
| `/lsp` | 配置语言服务器配置文件和根目录。 |
| `/help` | 打开完整的命令和按键指南。 |

## 独立命令

| 命令 | 用途 |
| --- | --- |
| `q sprint <request...>` | 运行一次自主的计划工作流。 |
| `q remote` | 启动前台 Remote REST 服务。 |
| `q remote config` | 配置 Remote 监听地址和 API 密钥。 |
| `q gateway` | 配置独立 Gateway。 |
| `q gateway start` | 启动兼容 OpenAI 的 Gateway。 |
| `q library` | 配置全局 Library 监听设置。 |
| `q library start` | 让全局 Library 作为前台服务持续运行。 |
| `q memory` | 让 Workspace Memory 独立运行。 |
| `q usage` | 打开本地 Token 使用量仪表盘。 |
| `q commit` | 打开独立的提交流程。 |
| `q model` | 配置模型和角色分配。 |
| `q subagents` | 管理子代理配置文件与 ACP 绑定。 |
| `q skills` | 管理 Agent Skills。 |
| `q mcp` | 配置外部 MCP 服务器。 |
| `q lsp` | 配置语言服务器与工作区根目录。 |
| `q ignore` | 编辑 `.qignore`。 |
| `q help` | 不启动聊天服务，直接打开命令和按键指南。 |
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
