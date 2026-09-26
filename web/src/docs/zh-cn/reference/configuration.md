---
locale: zh-cn
title: 配置
description: 查找 q 的个人设置、工作区状态、配置文件与可重建索引。
sectionLabel: 参考
toc:
  - id: 个人状态
    label: 个人状态
  - id: 工作区状态
    label: 工作区状态
  - id: 事实来源
    label: 事实来源
---

## 个人状态

| 路径 | 用途 |
| --- | --- |
| `~/.q/config.yaml` | 主要模型、角色、各模型的 API 模式、上下文、Loom 与 LSP 配置。 |
| `~/.q/providers.json` | 受管理 Gateway 的提供商与模型元数据。 |
| `~/.q/gateway.json` | 独立 Gateway 监听地址与密钥元数据。 |
| `~/.q/gateway.key` | 用于验证 Gateway API 密钥的私有主密钥。 |
| `~/.q/remote.json` | Remote 监听地址、认证开关与密钥元数据。 |
| `~/.q/remote.key` | 用于验证 Remote API 密钥的私有主密钥。 |
| `~/.q/library.json` | 全局 Library 回环监听设置。 |
| `~/.q/workspace-memory.json` | Workspace Memory 回环监听设置。 |
| `~/.q/usage.json` | Token Usage 服务回环监听设置。 |
| `~/.q/mcp.json` | 外部 MCP 配置文件与角色分配。 |
| `~/.agents/skills/` | 可被发现但不由 q 管理的便携式全局 Agent Skills。 |
| `~/.q/skills/` | 由 q 管理的全局 Agent Skills。 |
| `~/.q/subagents/` | 全局自定义子代理配置文件。 |
| `~/.q/logs/thinker/` | 短期保存的 Thinker 调用诊断。 |
| `~/.q/usage/usage.sqlite` | 近期 Token 事件与每日汇总。 |
| `~/.q/usage/archive/` | 旧原始使用事件的 Parquet 归档。 |

普通配置请使用 TUI。仅在自动化确有需要时直接编辑这些文件。

## 工作区状态

| 路径 | 用途 |
| --- | --- |
| `.q/sessions/<uuid>/session.json` | 会话记录、上下文、标题、生命周期与学习状态。 |
| `.q/sessions/<uuid>/delegations.json` | 子代理调用的书签。 |
| `.q/sessions/<uuid>/delegates/<invocation-id>/` | 子会话、执行状态和嵌套委派树。 |
| `.q/sessions/<uuid>/plan-execution.json` | 可恢复的获批计划检查点。 |
| `.q/plan-executions/` | 已完成的执行快照。 |
| `.q/model.json` | 工作区模型角色覆盖设置。 |
| `.q/learning.json` | 工作区学习开关。 |
| `.q/lsp.json` | 工作区 LSP 根目录与覆盖设置。 |
| `.q/data/` 与 `.q/index/` | 持久记录与派生索引。 |
| `.q/loom/` | 按内容寻址的工具产物与 GC 元数据。 |
| `.agents/skills/` | 可被发现但不由 q 管理的便携式工作区 Agent Skills。 |
| `.q/skills/` | 由 q 管理的工作区 Agent Skills。 |
| `.q/subagents/` | 工作区自定义子代理配置文件。 |
| `AGENTS.md` | 工作区及嵌套路径指令。 |
| `.qignore` | 文件发现排除规则。 |

## 事实来源

JSON 记录是事实来源。Bleve 与 HNSW 索引是派生数据，可以重建。

Loom 垃圾回收会在配置的宽限期内保护活跃会话视图和计划检查点所引用的内容。删除工作区的 `.q` 状态会移除该工作区中 q 的持久历史，但不会还原仓库文件。
