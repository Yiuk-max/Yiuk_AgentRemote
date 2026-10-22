# Yiuk Switchboard 实现方案

状态：MVP 实施基线。关联：[技术选型](technical-selection.md)、[产品需求](product-requirements.md)、[后端实施状态](backend-status.md)、[开发与构建](development.md)。

## 1. 范围

两端安装同一款 Qt 软件：受协助者注册设备并批准会话，协助者电脑上的 Agent 经 MCP 调用统一工具，远端执行单条 PowerShell 命令并返回 stdout、stderr 和退出码。Minecraft 联机能力保持不回归。

## 2. 进程与模块

```text
操作者 Windows                        被协助者 Windows              Go 服务
+----------------------+              +----------------------+      +----------------------+
| Agent (MCP)          |              | Yiuk Qt Client       |      | 认证 / 设备注册      |
|   MCP Adapter        |              | 所有者 UI / 审批     |      | 在线状态             |
|   Tool Core          |              | PowerShellRunner     |      | 会话授权 / 路由      |
| Operator Connection  |<------------------------------------------->| TLS 帧中转           |
+----------------------+              +----------------------+      +----------------------+
```

- **Tool Core**：`internal/agenttools`，设备、会话、任务 API 与结构化错误。
- **MCP Adapter**：`internal/mcpadapter`，stdio MCP server，不含设备端策略。
- **Operator backend**：`internal/platformclient`，长连接 operator 实现。
- **Go Platform**：`internal/platform/*`，认证、设备、会话、任务、传输、审计。
- **Qt Client**：所有者审批、会话管理、PowerShell 执行、文件读取。
- **Minecraft**：`internal/room|control|server|protocol`，独立业务协议。

## 3. 端到端流程

### 3.1 设备注册

1. Qt 首次启动生成设备身份，DPAPI 保护，设备码写入用户配置目录。
2. 客户端主动建立 TLS 连接并注册在线状态，定期心跳。
3. 设备码只用于识别，不是认证凭证。

### 3.2 会话申请与批准

1. Agent 调用 `request_connection(device_id, temporary_password, purpose, permissions, wait_for_approval_seconds)`。
2. 服务端校验临时密码（版本、有效期、撤销），按模式创建会话：`direct` 直接 `approved`，`approval` 进入 `pending_approval`。
3. 目标设备收到 `session_request`，所有者 UI 显示请求者、用途、权限与有效期。
4. 设备批准或拒绝；服务端只接受目标设备连接的批准。
5. 只有绑定的 operator 连接可在 `approved`/`active` 会话上提交任务。

### 3.3 命令任务

1. `execute_command(session_id, command, cwd, timeout)`。
2. 服务端校验会话状态、权限、速率与并发上限，分配 request ID。
3. 设备端校验 session/request 绑定后由 `PowerShellRunner` 启动进程。
4. stdout/stderr 分别异步读取，按有界 chunk 发送：`command_output` 带 request ID、stream、sequence。
5. 结束发送 `command_result`：状态、exit code、截断标记、最终 sequence。
6. 超时或取消终止进程；连接断开时标记 `result_unknown`，不自动重放。

### 3.4 文件下载

1. `download_file(session_id, remote_path, destination_path)`，需会话具备 `read_file`。
2. 设备端按 64 KiB 切块，校验块序号，计算 SHA-256，单文件上限 32 MiB。
3. MCP 端校验大小与校验和，写入目标文件（原子替换）。

### 3.5 结束与撤销

所有者停止、operator 关闭或会话过期时，平台标记终态、拒收新任务并下发取消；设备端同步终止任务。终态收敛到 `closed`、`rejected`、`expired` 或 `interrupted`。

## 4. 状态机

会话：

```text
requested -> pending_approval -> approved -> active -> closed
                    |               |         |
                    +-> rejected    +-> expired/interrupted
```

任务：

```text
queued -> running -> succeeded | failed | timed_out | cancelled | result_unknown
```

终态不可回退；重复 `request_id` 返回已有任务，跨会话复用被拒绝。

## 5. 协议基线

- 客户端主动连接服务端，TCP + TLS 1.2 及以上，校验证书主机名。
- 帧：4 字节大端长度前缀 + UTF-8 JSON envelope，帧上限可配。
- envelope：`version`、`type`、`correlation_id`、`session_id`、`request_id`、`sequence`。
- 读写 deadline、心跳、限流、半开连接检测、有界发送队列、优雅关闭。
- 业务类型显式区分 `agent_assist` 与 `minecraft`，配置与端口独立。

消息集合见[后端实施状态](backend-status.md)。

## 6. 执行要点（Qt）

- `PowerShellRunner`：临时 `.ps1`（UTF-8 BOM + `OutputEncoding` 前置）、`pwsh.exe` 优先 `powershell.exe` fallback、`-NoLogo -NoProfile -NonInteractive -ExecutionPolicy Bypass -File`。
- 每流 1 MiB 输出上限，超限后继续报 `truncated=true` 但不缓存。
- 超时、取消、进程错误统一上报一次终态；脚本与进程由 runner 清理。
- Windows DPAPI 保护本地凭证。
- UI 显示会话状态、远端操作者和断开入口。

源码结构见[开发与构建](development.md#源码结构)。

## 7. MCP 工具契约

所有工具返回 `{ok, code, message, data}`，错误码稳定。

| 工具 | 核心参数 | 结果 |
| --- | --- | --- |
| `list_devices` | — | 设备 ID、名称、在线状态 |
| `get_device_status` | `device_id` | 在线、活动会话摘要 |
| `request_connection` | `device_id`、`temporary_password`、`purpose`、`permissions`、`wait_for_approval_seconds` | `session_id`、状态 |
| `run_command` | `device_id`、`temporary_password`、`command`、`working_directory`、`timeout_seconds`、`wait_for_approval_seconds`、`keep_session_open` | 聚合 stdout/stderr、`exit_code`、状态 |
| `get_session_status` | `session_id` | 状态、权限、到期时间 |
| `execute_command` | `session_id`、`command`、`working_directory`、`timeout_seconds` | `request_id`、状态 |
| `get_command_result` | `session_id`、`request_id`、`cursor` | 输出块与终态 |
| `download_file` | `session_id`、`remote_path`、`destination_path` | 本地路径、大小、SHA-256、状态 |
| `cancel_command` | `session_id`、`request_id` | 任务状态 |
| `close_session` | `session_id` | 关闭状态 |

权限由设备所有者批准，服务端只下发已批准范围。`read_file` 与 `execute_command` 相互独立。

## 8. Minecraft 边界

独立端口、配置、密码和资源限制；远程协助会话不能进入游戏通道。验收按冻结清单在真实客户端执行：登录、开房、房间列表、玩家加入、双向数据流、房主断线恢复。

## 9. 测试范围

- Go 单测：帧边界、鉴权、限流、会话竞态、过期撤销、幂等、路由隔离。
- Go 集成：双端 mock、乱序与重复消息、断线、超时、慢读写、重启。
- Qt：会话与任务状态、输出合并、协议映射。
- Windows 端到端：PowerShell 输出、非 ASCII、长输出截断、超时、取消、进程树清理。
- 安全：未批准执行、过期重放、跨设备与跨会话 IDOR、凭证撤销、TLS 校验、日志泄露。
- Agent 端到端：真实 Agent 经 MCP 完成批准、执行、读结果、二次执行、关闭。
