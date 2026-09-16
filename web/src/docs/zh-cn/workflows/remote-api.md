---
locale: zh-cn
title: 远程 API
description: 通过经认证的前台 HTTP 服务运行 q 会话和一次性代理请求。
sectionLabel: 指南
toc:
  - id: 配置并启动
    label: 配置并启动
  - id: 查询工作区状态
    label: 查询工作区状态
  - id: 运行代理
    label: 运行代理
  - id: 流事件
    label: 流事件
  - id: 错误与容量
    label: 错误与容量
  - id: 远程边界
    label: 远程边界
---

## 配置并启动

配置监听地址、认证开关和 Remote 专用 API 密钥：

```powershell
q remote config
```

然后运行前台服务：

```powershell
q remote
```

默认监听地址为 `127.0.0.1:0`。Remote 密钥与 Gateway 密钥分开，因为 Remote 可以选择 q 进程有权访问的任何工作目录，并执行可修改工作区的工具。

## 查询工作区状态

列出指定工作目录中的会话：

```powershell
curl.exe -H "Authorization: Bearer $env:Q_REMOTE_KEY" "http://127.0.0.1:8080/v1/sessions?working_directory=C%3A%5Cwork%5Cproject"
```

列出同一工作区中有效的内置和自定义子代理：

```powershell
curl.exe -H "Authorization: Bearer $env:Q_REMOTE_KEY" "http://127.0.0.1:8080/v1/subagents?working_directory=C%3A%5Cwork%5Cproject"
```

## 运行代理

`POST /v1/subagent-runs` 接受 JSON，并流式返回 `application/x-ndjson` 事件：

```powershell
curl.exe -N `
  -H "Authorization: Bearer $env:Q_REMOTE_KEY" `
  -H "Content-Type: application/json" `
  -d '{"working_directory":"C:\\work\\project","prompt":"解释启动路径"}' `
  http://127.0.0.1:8080/v1/subagent-runs
```

必须提供 `working_directory` 和 `prompt`。省略 `session_id` 会创建新会话；提供它会恢复未被占用的现有会话。省略 `subagent` 或发送空值会运行默认主循环。设置为 `builtin/scout` 等 ID 则使用直接子代理流程。

提示上限为 32 KiB UTF-8 数据，完整 JSON 请求正文上限为 256 KiB。未知 JSON 字段会被拒绝。响应使用 `Cache-Control: no-store`，`X-Q-Session-ID` 标头给出所选会话。

## 流事件

第一个 NDJSON 记录始终是 `session`。其中 `working_directory`、`session_id` 和 `created` 表示规范化的工作区以及是否创建了新会话。

| 类型 | 主要字段 | 含义 |
| --- | --- | --- |
| `status` | `detail` | 启动警告或简短运行状态 |
| `activity` | `agent`, `task_id`, `parent_id`, `action`, `detail` | 子代理生命周期进度 |
| `trace` | `agent`, `kind`, `call_id`, `name`, `content`, `is_error` | 详细子代理跟踪项 |
| `tool_call` | `call_id`, `name`, `content` | 主代理工具请求；`content` 包含参数 |
| `message` | `role`, `name`, `call_id`, `content`, `is_error` | 加入会话的模型或工具消息 |
| `question` | `question`, `context` | 尝试交互提问；Remote 无法接受回答 |
| `result` | `session_id`, `outcome`, `content` | 成功的最终结果 |
| `cancelled` | — | 取消后的终止记录 |
| `error` | `error` | 开始流式响应后的最终错误 |

客户端应忽略不需要的字段，并持续读取直到终止记录。断开连接会取消请求上下文和当前运行。

## 错误与容量

开始流式响应前的失败使用 JSON `error` 对象和 HTTP 状态：

| 状态 | 代码 | 含义 |
| --- | --- | --- |
| `400` | `invalid_request` | JSON、目录、会话 ID、Content-Type 或请求形状无效 |
| `401` | `invalid_api_key` | 已开启认证，但 bearer 密钥缺失或无效 |
| `404` | `session_not_found` 或 `subagent_not_found` | 所选会话或子代理不存在 |
| `409` | `session_busy` | 另一进程已占用所选会话 |
| `413` | `request_too_large` | 正文或提示超出限制 |
| `429` | `remote_capacity` | 活跃运行达到 `agents.max_parallel` |
| `503` | `subagent_unavailable` | 代理存在，但配置的运行时不可用 |

`GET /v1/health` 报告服务版本及当前是否接受新运行。它与 `/openapi.json` 都允许无 bearer 密钥访问，但不暴露工作区状态。

## 远程边界

Remote 保留工具列表中的 `ask_to_user`，但流是单向的。q 会发出尝试的 `question`，随后立即返回“无法交互”的工具错误。模型可以根据现有信息继续，或报告被阻塞。

Remote 不提供内置 TLS、路径允许列表、后台任务、重新连接或交互回答。除非具备认证及可信的保密网络或反向代理，否则请仅在回环地址使用。

运行中的服务通过 `/openapi.json` 提供准确协议。
