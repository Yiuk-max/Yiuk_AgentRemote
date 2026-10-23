# 架构与实现

状态：远程协助链路已实现并可在本机联调；真实双机验收、发布前安全检查待完成（见 §12）。

关联：[产品需求](product-requirements.md)、[开发与构建](development.md)、[本机开发环境](local-dev.md)。

## 1. 选型基线

| 领域 | 选型 | 说明 |
| --- | --- | --- |
| 桌面应用 | Qt 6 Widgets + C++17 + CMake | 同一安装包同时提供设备所有者与协助者能力 |
| 中转服务 | Go（单进程、模块分层） | 内存状态，暂无数据库 |
| Agent 集成 | 独立 MCP stdio adapter（`yiuk-mcp`） | Agent 以本机子进程方式启动，只做协议映射 |
| 客户端传输 | TCP + TLS 1.2+，4 字节长度前缀帧 + JSON | 全部由客户端主动出站，适配 NAT |
| 设备执行 | `PowerShellRunner`（`pwsh` 优先，`powershell` 回退） | 独立参数传递、临时脚本文件、超时/取消/输出上限 |
| 身份与授权 | 设备码 + 设备临时密码 + 会话授权 | 设备码只作查找标识；临时密码永不出现在服务端日志 |
| 保活 | 设备与操作者双向 ping 心跳 | 操作者连接空闲时仍需保活，避免长会话被误判断线 |
| 发布 | Windows x64 绿色目录 + zip | 见[打包发布](development.md#打包发布) |

被否掉的方案：Web UI（用户明确偏好 Qt）、HTTP/WebSocket（长会话与分块流需额外设计，非标准端口在当前部署可用）、复用旧 Protobuf（字段语义已过时）、把 MCP 直接写进 GUI 主程序（GUI 生命周期不适合 stdio 子进程）。

## 2. 进程与模块

```text
操作者 Windows                          设备所有者 Windows
+--------------------------+            +--------------------------+
| Agent (MCP Host)         |            | Yiuk Qt Client           |
|   yiuk-mcp               |            |   所有者审批 / 会话管理  |
|   internal/agenttools    |            |   PowerShellRunner       |
|   internal/platformclient|            |   yiuk-activity-panel    |
+------------+-------------+            +------------+-------------+
             |  TLS 长连接                            |  TLS 长连接
             +------------------+--------------------+
                                |
                         +------v-------+
                         | Go 平台服务  |
                         | 认证 / 设备  |
                         | 会话 / 任务  |
                         | TLS 中转路由 |
                         +--------------+
```

| 位置 | 职责 |
| --- | --- |
| `internal/platform/{auth,device,session,task,transfer,protocol,transport,audit}` | 平台领域逻辑 |
| `internal/platform/server` | 连接、路由、janitor、各 handler、错误映射 |
| `internal/platformclient` | Agent 侧 operator 长连接 backend |
| `internal/agenttools` | 语言无关的工具契约与稳定错误码 |
| `internal/mcpadapter`、`cmd/yiuk-mcp` | MCP stdio 适配 |
| `qt-client/src` | 设备侧客户端：平台协议、审批 UI、命令执行、活动面板 |

## 3. 授权模型

设备端生成 32 位临时密码（版本 + 有效期 + 撤销），服务端只保存随机盐 + SHA-256 校验值。两种模式：

- **`approval`（逐次批准）**：临时密码只证明操作者持有设备所有者发放的信息。会话进入 `pending_approval`，设备所有者看到请求者、用途、权限、有效期后批准或拒绝。
- **`direct`（直接远程）**：服务端直接创建 `approved` 会话，不等待人工批准。

`direct` 只表示跳过人工批准，**不能**跳过设备身份校验、player 认证与会话权限校验；且仍必须具备：设备端可见的开关、会话有效期与单设备并发上限、UI 中可见的操作者/状态/立即停止入口、任一方刷新临时密码或关闭会话后旧会话立即失效。

不变量：

- 只有目标设备的连接可以批准/拒绝；只有绑定的 operator 连接可以提交任务。
- `pending_approval`、`rejected`、`expired`、`closed` 状态下服务端一律拒绝任务。
- `read_file`、`write_file` 与 `execute_command` 权限相互独立。
- 刷新临时密码会撤销该设备的活动会话。

## 4. 端到端流程

### 4.1 设备注册

1. 客户端首次启动生成 9 位设备码，写入用户配置目录；设备凭证用 Windows DPAPI 保护。
2. 客户端主动建立 TLS 连接并注册在线状态，之后按心跳维持。
3. 设备码只用于识别与查找，不是认证凭证。

### 4.2 申请与批准

1. Agent 调用 `request_connection(device_id, temporary_password, purpose, permissions, wait_for_approval_seconds)`；UI 侧输入 32 位临时密码与用途后发 `request_session`。
2. 服务端校验 player 认证与临时密码（版本/有效期/撤销），按模式创建会话。
3. 目标设备收到 `session_request`，弹出无系统标题栏的授权页；批准/拒绝经 `approve_session` / `reject_session` 回传。
4. 会话绑定设备、操作者、权限集合与到期时间。

### 4.3 命令任务

1. Agent 调 `execute_command(session_id, command, working_directory, timeout_seconds)`。
2. 服务端校验状态、权限、速率与并发上限，分配 `request_id`；重复 `request_id` 返回已有任务，不重复执行。
3. 设备端 `PowerShellRunner` 启动进程，stdout/stderr 分别异步读取，以 `command_output`（带 stream 与 sequence）流式回传。
4. 结束回 `command_result`：状态、exit code、`truncated`、最终 sequence。
5. 超时/取消终止进程；连接断开把无法确认的终态标记为 `result_unknown`，不自动重放。

### 4.4 文件下载

1. `download_file(session_id, remote_path, destination_path)`，会话须具备 `read_file`。
2. 设备端按 64 KiB 切块、校验序号、计算 SHA-256，单文件上限 32 MiB。
3. MCP 端校验大小与校验和，写入目标文件（不覆盖已有文件）。
4. 服务端只转发 Base64 分块，校验权限、分块序号、累计字节数与设备归属；超 `max_download_bytes` 或 5 分钟未完成的传输会被终止。

### 4.5 文件上传

1. `upload_file(session_id, local_path, remote_path, overwrite)`，会话须具备 `write_file`。
2. MCP 端校验本地文件（绝对路径、普通文件、≤32 MiB），先算 SHA-256，再按 64 KiB 切块、Base64 编码逐帧推送。
3. 服务端校验权限、分块序号、累计字节数并转发给设备；超 `max_upload_bytes` 或 5 分钟未完成即终止并下发取消。
4. 设备端解析目标路径（为空时落到 `%LOCALAPPDATA%\Yiuk\DeviceAssist\uploads`；已有目录则拼接文件名），先写同目录临时文件，收齐后校验大小与 SHA-256，再改名到目标；默认拒绝覆盖已有文件。
5. 设备回 `file_upload_result`（状态、`remote_path`、大小、SHA-256），MCP 端比对摘要后返回最终远端路径。

### 4.6 结束与撤销

所有者停止、operator 关闭或过期时，平台置终态、拒收新任务并下发取消；设备端同步终止任务。

## 5. 状态机

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

终态不可回退。服务端重启后在线连接、临时密码、请求与会话全部失效，不伪造成功。

## 6. 协议基线

- 帧：4 字节大端长度前缀 + UTF-8 JSON envelope，帧上限 `max_frame_bytes`（默认 256 KiB）。
- envelope 字段：`version`、`type`、`correlation_id`、`session_id`、`request_id`、`sequence`；直接响应回显 `correlation_id`，异步事件不回显。
- 传输约束：TLS 最低 1.2；未配置证书且未显式 `allow_insecure` 时拒绝启动；握手/心跳/写均有 deadline；每连接有界发送队列，慢客户端直接断开；连接、设备、会话、任务均有上限。
- 保活：设备连接每 15 秒发 `device_heartbeat`；操作者连接（`internal/platformclient`）空闲时每 15 秒发 `ping`，连续 3 个周期没有 `pong` 就主动断开，下次工具调用重连。`ping` 间隔可用 `heartbeat_interval_seconds` 覆盖，下限 1 秒。
- 消息集合：

```text
authenticate_player / authenticated
register_device / device_registered
device_heartbeat / heartbeat_ack
update_temporary_password / temporary_password_updated
request_session / session_request / session_created / session_status
approve_session / reject_session
open_session_channel / close_session
execute_command / command_accepted / command_output / command_result
download_file / cancel_file_download / file_chunk / file_download_result
upload_file / cancel_file_upload / file_upload_chunk / file_upload_result
get_command_result / cancel_command / command_cancelled
list_devices / device_list
ping / pong / error
```

- 平台 listener 只接受 `agent_assist` 业务。

## 7. 平台服务实现

内存状态（无数据库）：

```text
ServerState
  server_auth          player 密码（常量时间比较 + 按 IP 限流）
  devices             临时密码哈希/版本/有效期/撤销、模式、在线连接、心跳
  connections         已认证连接与有界发送队列
  sessions            状态机与授权绑定
  tasks               request_id 幂等、有界输出、deadline
```

- janitor 定期收敛：会话过期、设备/操作者断线、任务 deadline 超时（标记 `timed_out` 并向设备下发取消）。
- 审计只记录脱敏 ID、状态与错误码，不记录密码、命令正文或完整输出。
- 设备临时密码可轮换、撤销；轮换后旧会话立即失效。

## 8. 设备端执行要点（Qt）

- `PowerShellRunner`：临时 `.ps1`（UTF-8 BOM + 显式 `OutputEncoding`），`pwsh.exe` 优先、`powershell.exe` 回退，参数 `-NoLogo -NoProfile -NonInteractive -ExecutionPolicy Bypass -File`，不经过 `cmd.exe` 二次解析。
- 每流 1 MiB 输出上限，超限继续报 `truncated=true` 但不缓存。
- 超时、取消、进程错误只上报一次终态；脚本与进程由 runner 清理。
- 命令以当前登录用户权限运行，默认不提权；普通权限不是沙箱。
- 本地凭证（平台 player 密码、桥 token）用 DPAPI；`platform-client.json` 保存在 `%LOCALAPPDATA%\Yiuk\DeviceAssist`。

## 9. 活动面板（子进程）

远程协助活动窗口是**只读接收面板**，由独立进程 `yiuk-activity-panel.exe` 承载，不依赖 Qt：

- `ActivityPanel`（客户端内）用 `QProcess` 拉起子进程，只通过 stdin 推送协议行。用户关闭面板（`×`、Alt+F4、结束进程）不影响会话；下次点「查看活动」或托盘菜单会重新拉起并回放历史。
- 新记录只更新已打开面板的内容，**绝不**抢焦点或前置窗口；只有用户主动打开时才下发 `A|`。
- 窗口：`CreateWindowExW(WS_EX_LAYERED)` + `UpdateLayeredWindow` 逐像素透明，GDI+ 自绘圆角卡片、柔和阴影、标题栏与最小化/全屏/关闭三键。支持标题栏拖动、边缘缩放、滚轮滚动、贴底自动跟随，并记住位置与尺寸（`%LOCALAPPDATA%\Yiuk\DeviceAssist\activity-panel.state`）。
- 只显示当前会话（活跃优先、其次最新），没有会话切换器。

协议（UTF-8，一行一条；`\` 与换行转义）：

| 行 | 含义 |
| --- | --- |
| `H\|标题\|会话标签\|state\|状态文案` | 标题、顶部会话标签与状态 |
| `R\|` | 清空日志（切换会话时） |
| `E\|kind\|HH:MM:SS\|文本` | 一条记录，`kind` ∈ `info/session/exec/out/err/read/file/upload/ok` |
| `S\|state\|状态文案` | 只更新状态 |
| `A\|` | 用户主动打开时前置窗口 |

`state` 取 `active` / `pending` / `closed` / `none`，颜色由面板决定。配色沿用 pi 深色主题的分类（当前只做文字颜色，背景块留待后续）：会话=紫、执行命令=绿、读取=蓝、下载=兰紫、上传=琥珀、输出=灰、错误=红、完成=绿。

## 10. MCP 工具契约

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
| `upload_file` | `session_id`、`local_path`、`remote_path`、`overwrite` | 远端路径、大小、SHA-256、状态 |
| `cancel_command` | `session_id`、`request_id` | 任务状态 |
| `close_session` | `session_id` | 关闭状态 |

MCP 工具声明 `outputSchema` 与只读/破坏性/幂等提示，供 pi 等 Host 做结构化消费与权限提示。`cmd/yiuk-mcp` 优先读取 `%LOCALAPPDATA%\Yiuk\DeviceAssist\platform-client.json`，否则回退本机 Qt bridge（bridge 只支持本机设备，不伪造远端结果）。

## 11. 测试范围

- Go 单测：帧边界、鉴权限流、临时密码版本/过期/撤销、会话竞态与绑定、任务幂等与终态不可变、输出截断、游标分页。
- Go 集成：双端 mock、乱序与重复消息、断线、超时、慢读写、重启、跨 operator 拒绝。
- Qt：`yiuk-platform-interop` 以两个 `PlatformClient` 分别充当设备与操作者，覆盖认证、注册、请求、批准、`execute_command`、输出/结果回传、关闭会话。
- 端到端：`cmd/smoke` 与 `cmd/device-sim` 覆盖命令行链路；Windows 侧仍需验证非 ASCII 输出、长输出截断、超时、取消与进程树清理。
- 安全：未批准执行、过期重放、跨设备/跨会话 IDOR、凭证撤销、TLS 校验、日志泄露。

## 12. 未完成

- **进程树清理**：Qt 目前用 `QProcess::kill()`，尚未用 Windows Job Object 清理整棵子进程树；非 ASCII/代码页输出与取消边界仍需真机验证。
- **真实双机验收**：TLS 证书、安装包、UI 端到端流程。
- **发布前安全检查**：真实证书、日志脱敏复核、慢客户端与资源泄漏压测、安装包验收。
