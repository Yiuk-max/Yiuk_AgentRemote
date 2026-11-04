# 系统串讲：服务器上的 pi Agent，一次 MCP 调用，一条落到设备的命令

> 视角：**pi Agent 跑在服务器上**（与平台服务同机或就近），设备是远端 Windows 电脑。
> 目的：把"后端 / Agent 侧 server / 客户端"这三类东西一次说清，然后用一次**真实形状的调用**（在远端设备上装 `dsh` + `dsh-pet` 插件，并写一键启动脚本）把整条链路从头串到尾。
>
> 文中 `session_id` / `request_id` / 时间戳都是**示意值**（32 位十六进制或秒级时间戳），实际每次调用都不同；`dsh` 的版本号是占位。

---

## 0. 一句话结论

| 问题 | 答案 |
| --- | --- |
| 有几个进程？ | 服务器上两个：**平台服务**（Go，常驻）、**yiuk-mcp**（Go，被 pi 按需拉起）；设备上一个：**Qt 客户端** |
| MCP 在哪里？ | 只在 `yiuk-mcp` 进程里，且只占最外一层；往里依次是 `agenttools` → `platformclient` |
| 谁是"客户端"？ | 两个：`internal/platformclient`（操作者侧，跑在服务器的 yiuk-mcp 里）和 `qt-client`（设备侧） |
| 谁说话最多？ | 平台服务：它同时维持 operator 连接和设备连接，所有跨机消息都由它转发 |
| 命令真在哪执行？ | 设备端 Qt 客户端把命令写成临时 `.ps1`（UTF-8 BOM），用 `powershell.exe -File` 跑，再把 stdout/stderr 分帧回传 |
| 服务器上要不要装设备客户端？ | 不要。服务器上只有平台服务 + pi + yiuk-mcp；Qt 客户端只装在**被协助**的设备上 |

---

## 1. 名词表（后面只按这个叫法）

| 叫法 | 指什么 | 代码位置 |
| --- | --- | --- |
| **平台服务（后端）** | 中转 + 认证 + 会话/任务/传输状态机。完全内存化，重启即清空 | `cmd/platform`、`internal/platform/**` |
| **Agent 侧 server** | 面向 pi 的 stdio MCP server，即 `yiuk-mcp` | `cmd/yiuk-mcp`、`internal/mcpadapter`、`internal/agenttools` |
| **操作者客户端** | 上面那个进程里的远端调用者，长连接 + 认证 + 缓存 | `internal/platformclient` |
| **本机桥 backend** | 操作者客户端的替代实现：只连**本机** Qt 客户端的 loopback HTTP。**服务器上没有 Qt 客户端，所以它在这里用不上** | `internal/localbridge` |
| **设备端客户端** | 设备所有者电脑上的 GUI：注册、审批弹窗、执行 PowerShell、文件收发、活动面板 | `qt-client/**` |
| **契约层** | 两端共享的帧结构、类型常量、状态常量 | `internal/platform/protocol`、`internal/platform/transport` |

---

## 2. 全景：谁主动连谁

```text
┌──────────────────────────── 服务器（Linux 或 Windows Server） ────────────────────────────┐
│                                                                                          │
│  pi (MCP Host)                                                                           │
│    │  stdio JSON-RPC（换行分隔）                                                          │
│    ▼                                                                                     │
│  yiuk-mcp                                                                                │
│    ├─ internal/mcpadapter     ← MCP 协议面（tools/list、tools/call）                      │
│    ├─ internal/agenttools     ← 工具目录 + 参数校验 + Backend 接口                        │
│    └─ internal/platformclient ← operator 客户端：WSS 长连接、认证、心跳、缓存              │
│                │                                                                         │
│                │ wss://127.0.0.1:29564  authenticate_player                              │
│                ▼                                                                         │
│  ┌──────────────────────────────────────────────────────────────────────────┐            │
│  │ 平台服务（Go，systemd `yiuk-platform` / docker）                          │            │
│  │ conn.go 读写循环 · routing.go 转发 · handler_*.go 各业务                  │            │
│  │ device / session / task / transfer 管理器 · janitor 过期清理 · audit 审计  │            │
│  └──────────────────────────────────────────────────────────────────────────┘            │
└───────────────────────────────────────┬──────────────────────────────────────────────────┘
                                        │ wss  register_device / device_heartbeat
                                        │ （设备主动外连，服务器不需要能反连设备）
                                        ▼
                        ┌───────────────────────────────────┐
                        │ 设备：yiuk-device-assist.exe (Qt)  │
                        │  ├─ 9 位设备码 + 32 位临时密码     │
                        │  ├─ 审批弹窗 / 会话列表            │
                        │  ├─ PowerShellRunner（真正执行）    │
                        │  ├─ 文件收/发（临时文件+摘要校验）  │
                        │  └─ yiuk-activity-panel（只读面板）│
                        └───────────────────────────────────┘
```

三条铁律：

1. **设备端只外连**，从不监听；平台不需要能反连设备。
2. **operator 与 device 都是"平台的一条连接"**，彼此没有直连通路，一切靠 `ConnectionID` 转发。
3. **一次会话绑定三元组**：`session_id` + operator 连接 + device 连接。连接断开、会话过期、设备被关闭 → 命令一律不执行。

对服务器侧最实际的两条推论：

- pi 与平台同机时，operator 连接是 `127.0.0.1` 上的 WSS，用服务器自己的证书当 CA 验；**同机省略了 80% 的排障面**。
- 服务器上**没有** Qt 客户端，所以 `platform-client.json` 是 yiuk-mcp 唯一的 backend 来源；`localbridge` 回退在服务器上必然失败。

---

## 3. 文件地图：每个目录到底算哪一边

图例：【后端】【Agent 侧】【设备侧】【契约/共享】【测试工具】【部署/配置】

### 3.1 Go 侧

| 路径 | 归属 | 职责 |
| --- | --- | --- |
| `cmd/platform/main.go` | 【后端】 | 进程入口：读 YAML → 建 slog → `server.New` → `Run`，处理 SIGINT/SIGTERM |
| `cmd/yiuk-mcp/main.go` | 【Agent 侧】 | 进程入口：先试 `platformclient`，失败回落 `localbridge`，都失败就报 NOT_CONFIGURED，然后 `mcpadapter.Run` |
| `cmd/smoke/main.go` | 【测试工具】 | 全链路自测：认证→注册→申请→批准→执行→取结果→关闭，外加错误密码/跨连接等反例 |
| `cmd/device-sim/main.go` | 【测试工具】 | 假设备：自动批准、返回固定结果，**不执行真实命令**，用来验证链路 |
| `internal/mcpadapter/server.go` | 【Agent 侧】 | 把 `agenttools` 的工具目录注册成 MCP `tools/list`，把 `tools/call` 转成 `core.Call`，结果塞 `content` + `structuredContent` |
| `internal/agenttools/tools.go` | 【契约】 | 11 个工具的名字/JSON Schema/注解、`validateInput` 严格校验、`Backend` 接口、`Response{ok,code,message,data}` 信封 |
| `internal/platformclient/*` | 【Agent 侧】 | operator 长连接：`transport.go` 拨号/握手/心跳/读循环，`client.go` 请求-响应关联，`cache.go` 输出与下载缓存，`tools_*.go` 各工具实现，`config.go` 读 `platform-client.json` |
| `internal/localbridge/client.go` | 【Agent 侧】 | 同一 `Backend` 接口的另一实现：HTTP + token 打本机 Qt 的 bridge，配置为 `local-api.json`（服务器场景用不到） |
| `internal/userconfig/path.go` | 【共享】 | 统一解析 per-user 配置目录：Windows 优先 `%LOCALAPPDATA%`，Linux 用 `$XDG_CONFIG_HOME`/`~/.config`，再拼 `Yiuk/DeviceAssist` |
| `internal/platform/protocol/protocol.go` | 【契约】 | `Message` 结构、`Type*`/`Status*`/`Task*` 常量、JSON codec 与 256 KiB 帧上限 |
| `internal/platform/transport/*` | 【共享】 | 后端监听（TLS + WebSocket，路径 `/`）与 `DialWebSocket`（每次连接一方复用） |
| `internal/platform/server/conn.go` | 【后端】 | 每连接读写循环、握手期 10 s / 认证后 45 s 读超时、有界发送队列（慢客户端直接断开） |
| `internal/platform/server/routing.go` | 【后端】 | `enqueueMessage` / `sendToConnection` / `sendToDevice` / `notifySessionStatus`：所有跨机转发都在这 |
| `internal/platform/server/handler.go` | 【后端】 | 帧类型分发 + 认证 + 设备注册 + 心跳 |
| `internal/platform/server/handler_session.go` | 【后端】 | 申请/批准/拒绝/查询/关闭会话 |
| `internal/platform/server/handler_command.go` | 【后端】 | 提交命令→校验授权→起 task→转发设备；命令输出累加并回灌 operator；取结果分页 |
| `internal/platform/server/handler_download.go` / `handler_upload.go` | 【后端】 | 文件读/写两组传输状态机（分块、摘要、终态） |
| `internal/platform/server/janitor.go` | 【后端】 | 周期清理过期会话、离线设备、僵死任务 |
| `internal/platform/server/errors.go` | 【后端】 | 领域错误 → 稳定错误码（`DEVICE_OFFLINE`、`SESSION_EXPIRED`…） |
| `internal/platform/server/convert.go` | 【后端】 | 领域对象 → `protocol.DeviceInfo` / `SessionInfo` |
| `internal/platform/device|session|task|transfer|auth|audit|config` | 【后端】 | 各领域管理器：在线表/密码哈希、会话状态机、任务输出累加、传输分块、限流、审计、YAML 配置与校验 |

### 3.2 Qt 设备侧（`qt-client/src`，只装在设备上）

| 文件 | 职责 |
| --- | --- |
| `main.cpp` | 入口：单实例锁、单实例 QLocalServer、样式 |
| `mainwindow.{h,cpp}` | 窗口骨架、托盘、页面切换、**`loadOrCreateDeviceCode()`（9 位设备码生成/持久化）**、会话与任务持久化 |
| `pages_home.cpp` | 主页：显示设备码/临时密码、发起连接、审批入口 |
| `pages_devices.cpp` | 我的设备 / 会话记录列表 |
| `pages_settings.cpp` | 设置：平台地址、player 密码、TLS、授权模式、Agent 集成 |
| `platformlink.cpp` | 启本机 bridge、读写 `platform.json`、**写 `platform-client.json`**、`mcpAgentConfigText()` |
| `platformevents.cpp` | 平台事件落地：会话请求弹窗、`execute_command` 执行、命令输出回传、文件收发 |
| `sessionflow.cpp` | 会话过期/待批准/批准/活动状态机 |
| `platformclient.{h,cpp}` | 设备侧协议客户端：WebSocket + 证书指纹固定、注册、心跳、重连 |
| `powershellrunner.{h,cpp}` | 真正执行：写 `%TEMP%\yiuk-<requestId>.ps1`（UTF-8 BOM）→ `powershell.exe -NoLogo -NoProfile -NonInteractive -ExecutionPolicy Bypass -File`，超时/取消/截断 |
| `localbridge.{h,cpp}` + `localtools.cpp` | 本机 HTTP bridge：让没配平台的 MCP 也能操作**本机**（`device_id` 必须等于自己） |
| `securestore.cpp` | 用 Windows DPAPI 保护 `platform.json` 里的密码与临时密码 |
| `activitypanel.cpp`、`panel/**` | `yiuk-activity-panel.exe`：只读悬浮面板（纯 Win32，stdin 逐行协议） |

### 3.3 配置与部署

| 路径 | 归属 | 说明 |
| --- | --- | --- |
| `configs/platform.yaml` | 【后端配置模板】 | 监听、证书、`player_password`、各类上限、`session_timeout_sec`（600）、`heartbeat_timeout_sec`（45）、`max_output_bytes`（2 MiB）、`max_download_bytes`/`max_upload_bytes`（32 MiB） |
| `deploy/install-platform.sh` | 【部署】 | 装在 `/opt/yiuk-platform`（`yiuk-platform` 二进制 + `platform.yaml` + `certs/` + `platform.log`），建 systemd 服务，首次生成 `player_password` |
| `deploy/systemd/`、`deploy/docker-entrypoint.sh`、`docker-compose.yml`、`Dockerfile` | 【部署】 | 同上，systemd / 容器两种形态（容器默认 `:29564`） |
| `.pi/mcp.json` | 【服务器 MCP 配置】 | 把 `yiuk-mcp` 注册给 pi |
| `~/.config/Yiuk/DeviceAssist/platform-client.json`（Linux）<br>`%LOCALAPPDATA%\Yiuk\DeviceAssist\platform-client.json`（Windows） | 【服务器运行时】 | pi 所在用户的 MCP backend 配置：地址、player 密码、CA 或证书指纹 |
| 设备上的 `%LOCALAPPDATA%\Yiuk\DeviceAssist\` | 【设备运行时】 | `device.json`(设备码) `platform.json`(DPAPI 凭证) `sessions.json` `downloads/` |

---

## 4. 把 MCP 这一侧彻底说清

### 4.1 三个容易混的东西

| 你可能以为 | 实际 |
| --- | --- |
| `internal/platformclient` 是"后端的一部分" | 不是。它**运行在服务器上的 yiuk-mcp 进程里**，是个客户端，只是和后端在同一个 Go module 里 |
| `internal/mcpadapter` 会自己发网络请求 | 不会。它只做 `MCP ⇄ agenttools` 的协议翻译，一个网络调用都没有 |
| `internal/agenttools` 是"工具实现" | 不是。它只有**目录 + 参数校验 + 一个 `Backend` 接口**。真正的实现在 `platformclient`（或本机场景下的 `localbridge`） |

依赖方向（单向，不回流）：

```
cmd/yiuk-mcp
   ├─► internal/mcpadapter ──► internal/agenttools (Backend 接口)
   ├─► internal/platformclient ──► internal/platform/protocol + transport
   └─► internal/localbridge ───► internal/agenttools
```

`agenttools.Backend` 只有一个方法，这也是"换 backend 不改工具层"的原因：

```go
// internal/agenttools/tools.go:55
type Backend interface {
	Invoke(context.Context, Request) (any, error)
}
```

### 4.2 一次 `tools/call` 的原始形态

pi 通过 stdio 与 `yiuk-mcp` 交换**换行分隔**的 JSON-RPC：

```jsonc
// pi → yiuk-mcp
{"jsonrpc":"2.0","id":1,"method":"initialize",
 "params":{"protocolVersion":"2025-06-18","capabilities":{},
           "clientInfo":{"name":"pi","version":"0.x"}}}

// yiuk-mcp → pi（serverInfo 来自 internal/mcpadapter/server.go）
{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-06-18",
 "capabilities":{"tools":{}},"serverInfo":{"name":"agentremote","version":"0.1.0"}}}

// pi → yiuk-mcp（通知，无 id）
{"jsonrpc":"2.0","method":"notifications/initialized"}

// pi → yiuk-mcp
{"jsonrpc":"2.0","id":2,"method":"tools/call",
 "params":{"name":"list_devices","arguments":{}}}
```

`tools/call` 的返回是**两层信封**：MCP 的 `content`（给模型看的文本）里再套一层本项目自己的 `Response`：

```jsonc
{"jsonrpc":"2.0","id":2,"result":{
  "content":[{"type":"text","text":"{\"ok\":true,\"code\":\"OK\",\"data\":[...]}"}],
  "structuredContent":{"ok":true,"code":"OK","data":[...]},
  "isError":false}}
```

- `isError` 由 `!response.OK` 决定；
- 业务错误也是 `result`（不是 JSON-RPC error）：`{"ok":false,"code":"DEVICE_OFFLINE","message":"..."}`；
- 只有"编码失败"这类框架问题才走 JSON-RPC 层错误。

### 4.3 工具目录与它绑定到哪一层

| 工具 | 需要的会话状态 | 权限 | 落到设备上的动作 |
| --- | --- | --- | --- |
| `list_devices` | 无 | 无（只读） | 无（平台侧查在线表） |
| `get_device_status` | 无 | 无（只读） | 无（平台侧查在线表） |
| `request_connection` | 创建会话 | 申请 `permissions` | 设备弹审批框 |
| `get_session_status` | 任意 | 无 | 无 |
| `execute_command` | `approved`/`active` | `execute_command` | `PowerShellRunner` 起 `powershell.exe` |
| `get_command_result` | 同上 | `execute_command` | 无（读平台/本地缓存） |
| `cancel_command` | 同上 | `execute_command` | 杀 PowerShell 进程树 |
| `download_file` | `approved`/`active` | `read_file` | 设备读文件→分块→`file_download_result` |
| `upload_file` | `approved`/`active` | `write_file` | 设备写临时文件→校验大小+SHA256→原子改名 |
| `run_command` | 以上四步的封装 | `execute_command` | 请求→等批→执行→等结果→关会话 |
| `close_session` | 任意 | 无 | 设备结束本地任务、清会话 |

两个给服务器常驻 Agent 的实用点：

- **`run_command`** 是为"服务器托管的 Agent"设计的一次性封装（工具描述原文就是 *one-shot remote task for a server-hosted agent*）：内部就是 `requestSession → submitCommand → waitForCommandResult → requestSessionClose`，并对整段设了 `wait_for_approval + timeout + 30s` 预算。单条命令用它最省事。
- **长流程**（本任务要执行多次、还要传文件）必须用长版本：`request_connection` 拿 `session_id`，之后 `execute_command` / `upload_file` / `get_command_result` 复用同一会话，最后 `close_session`。

### 4.4 参数校验分三层（都不可省）

1. **`agenttools/tools.go`**：JSON Schema + `validateInput`（未知字段直接拒、长度上限、`permissions` 枚举与去重、`timeout_seconds ∈ [1,3600]`）。
2. **`platformclient`**：只看必要语义（如 `temporary_password` 为空就直接 `INVALID_ARGUMENT`）。
3. **`internal/platform/server`**：自己的上限（`CommandMaxLen`、`IdentifierMaxLen`、密码长度 `WEAK_TEMPORARY_PASSWORD`）+ 状态机校验（会话必须 approved/active，设备必须仍在线且连接一致）。

> 客户端校验只是"别浪费一次往返"，**服务端从不相信客户端**。

### 4.5 四个 ID 的分工（最容易读叉的地方）

| ID | 谁生成 | 生成频率 | 作用域 | 干什么用 | 能不能复用 |
| --- | --- | --- | --- | --- | --- |
| `connection_id` | 服务端 `newID()`（`server.go:150`，随 `authenticated` 下发） | 每条连接一次 | 一条 WSS 连接 | 平台内部路由：`connections` 表、`sendToConnection`、会话“绑定的是哪条管道” | 不能。重连 = 新值，旧会话因 `DeviceConnectionID` 不匹配而作废 |
| `session_id` | 服务端 `newID()`（`handler_session.go:41`） | 每次 `request_connection` 成功 | 一次授权 | 权限/状态机/审计的主体；后面每条命令、每个文件操作都要带它 | **复用**：一次任务流程内多条命令共享同一个 |
| `request_id` | **客户端** `randomID()`（`tools_command.go:32`、`tools_file.go:58`、`tools_upload.go:52`） | **每次** `execute_command` / `download_file` / `upload_file` 各一个新值 | 一次具体动作 | ① 幂等键 `tasks.Start(session, request)`；② 结果寻址 `get_command_result` / `cancel_command`；③ 设备端临时脚本名 `yiuk-<requestId>.ps1`、输出序号表、上传/下载状态机键 | **绝不复用**。同会话重复 = 返回已有任务（不重跑）；跨会话重复 = `REQUEST_ID_CONFLICT` |
| `correlation_id` | **客户端** `randomID()`，在 `sendStreamingRequest` 里每次调用现生成（`client.go:118`） | 每次请求-响应对 | 一次往返（一个请求帧 + 它的直接回复） | 只做“回执配对”：服务端 `sendResponse` 原样回显（`routing.go:92`），客户端 `sendStreamingRequest` 用它丢弃无关事件 | 一次性、无状态；异步事件（`command_output`、`session_status` 推送）**不带它** |

一句话区分：

- `connection_id` = **哪条管道**（路由用）
- `session_id` = **哪次授权**（谁、多久、能干什么）
- `request_id` = **哪个动作**（幂等 + 结果寻址 + 设备端资源命名）
- `correlation_id` = **哪次问答**（活在一次往返里，用完即忘）

同一条命令里它们怎么出现：

```text
execute_command
  request_id     = 7b41d9e0…   ← 客户端新生成（这次动作的身份，后面一直用它）
  correlation_id = a1b2c3d4…   ← 客户端新生成（只给这一次问答用）
  {type:execute_command, session_id:5f0c…, request_id:7b41…, correlation_id:a1b2…}
        └─► 服务端 Authorize(session_id, conn.id=connection_id)，转发设备时**丢掉 correlation_id**
            {type:execute_command, session_id:5f0c…, request_id:7b41…}
  ◄── command_accepted  {correlation_id:a1b2…}   ← sendResponse 回显，配对完成
  ◄── command_output(seq=1) {session_id, request_id}   ← 异步事件，无 correlation

get_command_result
  request_id     = 7b41…        ← **沿用刚才那个，不是新生成**
  correlation_id = c9d8e7f6…    ← 这条查询自己新生成
```

所以：**`request_id` 是“动作”，每次 `execute_command` 都是新的；`correlation_id` 是“回合”，每次工具调用（包括 `get_command_result` 这种查询）都是新的；`session_id` 跨越整个会话不变。**

两个实现细节：

- `get_command_result` / `cancel_command` 的 `request_id` 来自**工具参数**（人/模型传入），客户端不会替你生成——传错就是真的查不到（`TASK_NOT_FOUND`）。
- `request_id` 会变成设备上的文件名，而服务端只校验“非空 + ≤128 字节”（`errors.go:14`）；客户端现用 32 位十六进制所以安全。若想更严，设备端应加 `^[A-Za-z0-9_-]{1,128}$` 白名单。

---

## 5. 帧级时序（一条命令的完整来回）

```text
服务器: yiuk-mcp(operator)          平台服务                    设备: Qt 客户端
      │                              │                              │
      │ request_session ────────────►│ 校验临时密码 → 建会话         │
      │  (device_id,purpose,perm,    │ status=pending_approval       │
      │   temporary_password)        │ session_request ────────────►│ 弹窗
      │◄──── session_created ────────│                              │
      │                              │◄──── approve_session ────────│ 所有者点同意
      │◄──── session_status(approved)│──── session_status(approved) ►│
      │                              │                              │
      │ execute_command ────────────►│ Authorize(session,perm)       │
      │  (session_id,request_id,     │ 设备在线且连接一致？           │
      │   command,timeout)           │ tasks.Start(...)              │
      │◄──── command_accepted ───────│ execute_command ─────────────►│ 写 .ps1 → 跑
      │                              │                              │
      │                              │◄──── command_output(seq=1) ──│ stdout 分块
      │◄──── command_output(seq=1) ──│ （同时累加到 task 输出缓冲）    │
      │                              │◄──── command_output(seq=2) ──│ stderr 分块
      │◄──── command_output(seq=2) ──│                              │
      │                              │◄──── command_result ─────────│ 退出码/状态
      │                              │  task 终态 + finish           │
      │ get_command_result(cursor=0)►│                              │
      │◄──── command_result(chunks) ─│                              │
      │                              │                              │
      │ close_session ──────────────►│ 会话 closed + 任务 cancelled  │
      │                              │──── close_session ──────────►│ 收尾
```

要点：

- **输出两条路**：`command_output` 是流式增量（operator 侧缓存，`cache.go:cacheCommandOutput`）；`command_result` 是终态（含完整 `chunks`、`exit_code`、`truncated`）。`get_command_result` 先看本地缓存，已是终态就直接分页返回，不再打平台（`cachedFinalCommandResult`）。
- **截断是全局的**：`max_output_bytes`（默认 2 MiB）在 task 管理器里控，超出后 `truncated=true`，后续输出被丢弃而不是把内存撑爆。
- **`request_id` 不可复用**（重复提交同一 `(session,request)` 只会返回已存在的任务，不会重跑）；断线**不自动重放**。
- **命令正文与输出不落服务器日志**：审计只记 `session/request/device` 的脱敏 ID。

---

## 6. 真实调用模拟：服务器上的 pi 装 `dsh` + `dsh-pet`，并写一键启动脚本

### 6.0 场景

| 项 | 值 |
| --- | --- |
| 发起方 | **服务器上的 pi**（MCP Host），经 `yiuk-mcp` → `platformclient` 发起 |
| 目标设备 | `903174411`（Windows 11，普通用户，非管理员） |
| 授权模式 | `approval`（每次需要设备所有者点同意） |
| 临时密码 | 设备端生成的 32 位（base32 字符集，去掉易混字符），所有者当面/电话告知 |
| 要申请的权限 | `execute_command` + `read_file` + `write_file` |
| 任务 | ① 装 `dsh`；② 装 `dsh-pet` 插件；③ 在设备上放一个一键启动脚本；④ 冒烟验证 |

> **`dsh` 的安装方式必须按实际情况替换**：Python 包就是 `pip install dsh`，Node 包就是 `npm i -g dsh`，独立 exe 就下载解压。下面按 Python 包写，Node 变体见注。

### 6.1 第 0 跳：服务器侧配置先到位

pi 所在用户读 `~/.config/Yiuk/DeviceAssist/platform-client.json`（Windows 服务器上是 `%LOCALAPPDATA%\Yiuk\DeviceAssist\platform-client.json`）：

```json
{"address":"127.0.0.1:29564","player_password":"<48 位>",
 "timeout_seconds":30,"tls_ca":"/opt/yiuk-platform/certs/server.crt"}
```

- 同机就用 `tls_ca` 指向服务器自己的证书；跨机、没有 CA 时改用 `pinned_cert_sha256`（服务器证书 DER 的 SHA-256 小写十六进制）。
- 该用户必须能读 `certs/server.crt`，并能写自己的 `~/.config`。
- **别用 `yiukplatform` 跑 pi**：那是 `nologin`、无 home 的服务账号，`os.UserConfigDir()` 拿不到可用目录。
- 没有这个文件 → `NewFromUserConfig` 失败 → 回退 `localbridge`（服务器上无 Qt 客户端，必然失败）→ 所有工具返回 `NOT_CONFIGURED`。**这一步不通，后面全是 `NOT_CONFIGURED`，别去查设备。**

### 6.2 第 1 跳：`list_devices`（确认设备真的在线）

```json
// tools/call（pi 发出的第一件事：先看设备在不在）
{"name":"list_devices","arguments":{}}

// 返回 data（[]DeviceInfo）
[{"device_id":"903174411","name":"DESKTOP-8F3K2Q","platform":"windows",
  "mode":"approval","online":true,"password_version":3,
  "password_expires_at":1790000000,"last_heartbeat":1789999972,"active_sessions":0}]
```

- 只有 `online:true` 且 `active_sessions:0` 才继续；`online:false` 说明设备进程没连上平台（或心跳超 45 s 被判离线）。
- 平台侧只查自己的内存在线表，**不会**去碰设备。

### 6.3 第 2 跳：`request_connection`（拿授权，关键的一步）

```json
{"name":"request_connection","arguments":{
  "device_id":"903174411",
  "purpose":"install dsh and dsh-pet plugin, write one-click start script",
  "permissions":["execute_command","read_file","write_file"],
  "temporary_password":"K7QM3XZP9RTV2W8YHJ4NLB6CD5FG2SQA",
  "wait_for_approval_seconds":120}}
```

平台内部（`handler_session.go:handleRequestSession`）：

1. `devices.VerifyPassword(...)` 比对 `hashPassword(salt, candidate)`；错/过期/已撤销 → `BAD_TEMPORARY_PASSWORD` / `TEMPORARY_PASSWORD_EXPIRED` / `TEMPORARY_PASSWORD_REVOKED`；
2. 查在线连接；不在线 → `DEVICE_OFFLINE`；
3. `sessions.Create(...)`；`approval` 模式 → `pending_approval`（`direct` 模式直接 `approved`）；
4. 给设备连接发 `session_request`，设备端弹审批框。

返回：

```json
{"ok":true,"code":"OK","data":{
  "session_id":"5f0c2a9b6d1e47c8a3b15e9d0c4f7a21",
  "device_id":"903174411","status":"pending_approval","mode":"approval",
  "purpose":"install dsh and dsh-pet plugin, write one-click start script",
  "permissions":["execute_command","read_file","write_file"],
  "expires_at":1790000600,"created_at":1790000000,"operator":"a1b2c3d4e5f60718"}}
```

因为传了 `wait_for_approval_seconds:120`，`platformclient` 会每 1.5 s 轮询 `get_session_status`。**人在设备上点"批准"**之后：

```json
{"ok":true,"code":"OK","data":{"session_id":"5f0c…7a21","status":"approved","mode":"approval",
  "permissions":["execute_command","read_file","write_file"],"approved_at":1790000042}}
```

没批/超时（服务端 `approval_timeout_sec` 默认 120 s）→ `expired`；主动拒 → `rejected`。此时**绝不能**继续发命令：只会拿到 `INVALID_SESSION_STATE`。

> 想无人值守跑通整条链，只能在实验环境把设备切成 `direct` 模式（唯一区别就是自动批准）。生产语义就是"有人必须点那一下"。

### 6.4 第 3 跳：`execute_command`（装 dsh + dsh-pet）

```json
{"name":"execute_command","arguments":{
  "session_id":"5f0c2a9b6d1e47c8a3b15e9d0c4f7a21",
  "command":"$ErrorActionPreference='Stop'\n$py=(Get-Command python -ErrorAction Stop).Source\n& $py -m pip install --upgrade dsh dsh-pet\n& $py -m dsh --version\n& $py -m dsh plugin list",
  "working_directory":"C:\\Users\\41895",
  "timeout_seconds":600}}
```

> Node 变体：把后三行换成 `& npm.cmd install -g dsh dsh-pet` / `& dsh.cmd --version`。
> `$ErrorActionPreference='Stop'` 是必须的：否则 `pip` 失败后脚本会继续往下跑，给你一个 `exit_code:0` 的假成功。

返回（只是"已受理"，**不含输出**）：

```json
{"ok":true,"code":"OK","data":{"session_id":"5f0c…7a21",
  "request_id":"c81d4f27a95b43e6b0d72a1c9e5f3018","status":"running"}}
```

设备上真正发生的事（`platformevents.cpp:startPlatformCommand`）：

1. 会话状态必须是 `active`/`approved`，否则直接回 `cancelled` 结果；
2. `PowerShellRunner` 把命令写成 `%TEMP%\yiuk-<requestId>.ps1`（**带 UTF-8 BOM**），并预置 `[Console]::OutputEncoding = UTF8`（避免 936 代码页把中文输出搞乱）；
3. 优先 `pwsh.exe`，没有则 `powershell.exe`，参数 `-NoLogo -NoProfile -NonInteractive -ExecutionPolicy Bypass -File <脚本>`；
4. 边跑边 `command_output` 回传（递增 `sequence`），同时写进活动面板与 `sessions.json`；
5. 结束发 `command_result`：`status` = `succeeded`/`failed`/`timed_out`/`cancelled`，附 `exit_code`、`truncated`。

#### 6.4.1 执行用的临时脚本为什么按 request 命名，重复执行会不会撞名

不会撞名，也不存在"重命名"：

- **名字**：`%TEMP%\yiuk-<requestId>.ps1`（`powershellrunner.cpp:54`）。`requestId` 由 operator 端 `randomID()` 现生成——16 字节 `crypto/rand` → 32 位十六进制（`platformclient/transport.go:randomID`、`tools_command.go:submitCommand`），**每次 `execute_command` 都是新值**。所以"跑第 N 次"= 第 N 个不同的文件名，不是覆盖同一个名字。
- **生命周期**：任务结束（成功/失败/超时/取消）→ `finishAndReport()` → `stopProcessAndRemoveScript()` → `QFile::remove(scriptPath_)` + 清空路径（`powershellrunner.cpp:124-138`）。正常路径下 `%TEMP%` 里不会留东西。即使同名重开，`QIODevice::WriteOnly` 也是**截断**写，不会追加旧内容。
- **重复提交有三层挡着**：
  1. 服务端 `tasks.Start(sessionID, requestID)` 幂等——同会话重复提交直接返回已存在任务、**不再转发设备**（`handler_command.go:56-62`）；不同会话复用同一 `requestId` → `REQUEST_ID_CONFLICT`；
  2. 设备端 `commandRunners.contains(requestId)` 把在跑的重复帧直接丢弃（`platformevents.cpp:572`）；
  3. 客户端断线**不自动重放**。
- **唯一残留**：设备进程被强杀/断电 → `stopProcessAndRemoveScript()` 没跑 → `%TEMP%` 留几个 `yiuk-*.ps1`（每个几 KB，纯垃圾、无安全含义，因为 `requestId` 一次性）。要彻底干净可以在客户端启动时顺手删掉 24 小时前的 `yiuk-*.ps1`。

> 别和**任务产物**混：`C:\dsh\start-dsh.ps1` 是**固定文件名**，重复 `upload_file` 需要 `overwrite:true`，否则设备直接拒绝（`目标文件已存在；如需覆盖请设置 overwrite=true`，`platformevents.cpp:449`）。临时脚本按 request 命名、产物用固定名 + 显式覆盖，两套语义不要混。

### 6.5 第 4 跳：`get_command_result`（取输出，按 cursor 翻页）

```json
{"name":"get_command_result","arguments":{
  "session_id":"5f0c…7a21","request_id":"c81d…3018","cursor":0}}
```

```json
{"ok":true,"code":"OK","data":{
  "session_id":"5f0c…7a21","request_id":"c81d…3018","status":"succeeded",
  "exit_code":0,"truncated":false,"result_known":true,"output_bytes":1834,
  "next_cursor":3,
  "chunks":[
    {"stream":"stdout","sequence":1,"data":"Collecting dsh\n  Downloading dsh-2.4.1-py3-none-any.whl (312 kB)\n"},
    {"stream":"stdout","sequence":2,"data":"Successfully installed dsh-2.4.1 dsh-pet-0.9.0\n"},
    {"stream":"stdout","sequence":3,"data":"dsh 2.4.1\n[dsh-pet] loaded\n"}]}}
```

- `status` 还是 `running`/`queued` → 隔一会儿用 `cursor=next_cursor` 再取，别重复读已消费的块；
- `result_known:false` + `status:"result_unknown"` = 结果丢了（设备掉线/平台重启），**不要重试同一命令**（可能有副作用），先查状态再决定；
- `truncated:true` = 超过 `max_output_bytes`（2 MiB），后续输出被丢弃，不是命令失败。

### 6.6 第 5 跳：`upload_file`（把一键启动脚本送上去）

为什么不直接在命令里 `Set-Content` 写脚本？两个理由：① 脚本里有 `$`、引号、反引号，嵌进一行命令会被反复转义；② `upload_file` 有 **SHA-256 校验 + 临时文件 + 原子改名**，且默认**拒绝覆盖**已存在文件。

```json
{"name":"upload_file","arguments":{
  "session_id":"5f0c…7a21",
  "local_path":"/srv/yiuk/artifacts/start-dsh.ps1",
  "remote_path":"C:\\dsh\\start-dsh.ps1",
  "overwrite":true}}
```

```json
{"ok":true,"code":"OK","data":{
  "session_id":"5f0c…7a21","request_id":"7b41d9e05c2a48f6b93e17d0a4c85e62",
  "local_path":"/srv/yiuk/artifacts/start-dsh.ps1",
  "remote_path":"C:\\dsh\\start-dsh.ps1","file_name":"start-dsh.ps1",
  "size":3187,"sha256":"b6f1…9c2d","chunks":1,"overwrite":true,"status":"succeeded"}}
```

- `local_path` 是**服务器上**的路径（pi 所在进程能读到的文件），所以脚本要么先在服务器上生成，要么从仓库 `scripts/` 里取。
- 设备侧落盘：64 KiB 分块 → 写 `*.part` → 校验总字节数与 SHA-256 → 原子改名；`remote_path` 留空则落到设备 `uploads\`。单文件上限 32 MiB。

### 6.7 第 6 跳：冒烟（`-DryRun`）

```json
{"name":"execute_command","arguments":{
  "session_id":"5f0c…7a21",
  "command":"& 'C:\\dsh\\start-dsh.ps1' -DryRun",
  "timeout_seconds":60}}
```

期望 `exit_code:0`，输出里能看到解析到的 `dsh` 启动方式、插件目录、PID/日志路径——**只解析不启动**。装错位置会以非 0 退出并给出可操作原因（`dsh executable not found; pass -DshExe`）。

### 6.8 第 7 跳：`close_session`

```json
{"name":"close_session","arguments":{"session_id":"5f0c…7a21"}}
→ {"ok":true,"code":"OK","data":{"session_id":"5f0c…7a21","device_id":"903174411","status":"closed"}}
```

平台侧：会话 `closed`（reason `closed_by_operator`）、该会话剩余任务 `cancelled`、未完成传输 `cancelled`，并向设备发 `close_session`（设备停掉本地进程）——**授权即刻失效，不等超时**。

### 6.9 失败面速查（每个错误码对应哪一跳）

| 错误码 | 出现位置 | 说明 / 处理 |
| --- | --- | --- |
| `NOT_CONFIGURED` | 第 0 跳 | 服务器上 `platform-client.json` 缺失/无效（`localbridge` 在服务器上不可用） |
| `PLATFORM_UNAVAILABLE` | 任一跳 | 连不上/握手失败/CA 或指纹不匹配；看 `platformclient` 的错误串 |
| `PLATFORM_TIMEOUT` | 任一跳 | 工具调用超时（默认 30 s；文件传输另算） |
| `DEVICE_NOT_FOUND` | 第 2 跳 | 设备码从未注册（或已被过期清理） |
| `DEVICE_OFFLINE` | 第 2、6.4 跳 | 设备进程不在线 / 心跳断了 / 提交命令时连接已换 |
| `BAD_TEMPORARY_PASSWORD` | 第 2 跳 | 密码错；**设备码本身不是凭证** |
| `TEMPORARY_PASSWORD_EXPIRED` / `_REVOKED` | 第 2 跳 | 让所有者刷新或重新生成 |
| `INVALID_SESSION_STATE` | 第 6.4 跳 | 会话还是 `pending_approval` / 已 `closed` / `expired` |
| `SESSION_EXPIRED` | 第 6.4~6.8 跳 | 超过 `session_timeout_sec`（默认 600 s） |
| `PERMISSION_DENIED` | 第 6.4/6.6 跳 | 申请时没带这个权限（比如没申请 `write_file` 就 `upload_file`） |
| `SESSION_LIMIT` | 第 2 跳 | 该设备并发会话上限（`max_sessions_per_device`，默认 8） |
| `TASK_LIMIT` / `REQUEST_ID_CONFLICT` | 第 6.4 跳 | 同会话任务上限（默认 2）/ 复用了 `request_id` |
| `FILE_TOO_LARGE`、`CHUNK_TOO_LARGE`、`INVALID_CHUNK_SEQUENCE` | 第 6.6 跳 | 32 MiB 上限、64 KiB 分块、序号必须连续 |
| `RATE_LIMITED` | 认证期 | `auth_rate_per_min` 限流 |

---

## 7. 一键启动脚本与服务器侧部署

这里只写"脚本要满足什么"，不给可运行源码（脚本内容按现场环境生成即可）。

### 7.1 任务产物：设备侧一键启动脚本（契约）

| 项 | 约定 |
| --- | --- |
| 产物 | `C:\dsh\start-dsh.ps1` + `C:\dsh\start-dsh.cmd`（cmd 只是 `powershell -File` 的 ASCII 包装，给双击用） |
| 送达方式 | `upload_file`（`write_file` 权限）；**不用**命令里写文件 |
| 参数 | `-Root`、`-DshExe`、`-PluginDir`、`-Install`、`-DryRun`、`-Stop`、`-Tail`、`-NoPet` |
| 行为 | 解析 dsh 启动方式（`dsh.exe` → `python -m dsh` → npm shim）→ 定位 `dsh-pet` 插件目录 → 写 PID 与日志 → 先停旧实例再启动 |
| 验收 | `-DryRun` 只解析不启动、退出码 0；失败时给人能照做的下一步（提示 `-Install` 或 `-DshExe`） |
| 约束 | 纯 ASCII（PowerShell 5.1 按 ANSI 读无 BOM 的 `.ps1`，有中文必乱码）；日志超 5 MB 轮转；停进程用 `taskkill /T` 连带子进程 |
| 重复投放 | 固定文件名，重传必须带 `overwrite:true`；这与执行期临时脚本 `%TEMP%\yiuk-<requestId>.ps1`（每个 request 一个新文件名、结束即删）是两套不同语义（见 §6.4.1） |

设备上的使用方式（人手动或 MCP 命令都行）：

```powershell
& C:\dsh\start-dsh.ps1 -DryRun      # 自检
& C:\dsh\start-dsh.ps1 -Install     # 装依赖并启动
& C:\dsh\start-dsh.ps1 -Stop        # 停止
```

`-DryRun` 是给 MCP 用的"可自动验收"开关：有它，第 6.7 跳才能在不真的拉起进程的前提下判断脚本正确。

### 7.2 服务器侧：只有部署，没有"一键脚本"

**平台服务**（三选一，都已存在）：

- `sudo ./deploy/install-platform.sh <cert> <key>`：装到 `/opt/yiuk-platform`，建 systemd 服务 `yiuk-platform`，首次生成并打印 `player_password`（之后不再打印，重跑不会覆盖 `platform.yaml`）；
- `deploy/systemd/` 里已有单元文件，手工部署时直接抄；
- `docker compose up -d`（`YIUK_TLS_*`、`YIUK_PLAYER_PASSWORD` 走环境变量，容器内 `:29564`）。

**Agent 侧**（pi + yiuk-mcp）：

1. 构建：`go build ./cmd/platform ./cmd/yiuk-mcp`（`yiuk-mcp` 是纯 Go 静态产物，服务器上不需要 Qt）；
2. 在 `.pi/mcp.json` 注册：

```json
{"mcpServers":{"yiuk":{
  "command":"/opt/yiuk/yiuk-mcp",
  "args":[],
  "exposure":"direct"}}}
```

3. 写 `~/.config/Yiuk/DeviceAssist/platform-client.json`（见 §6.1）——这是**唯一**让 MCP 找到平台的东西。

**为什么服务器上不需要别的"启动脚本"**：平台有 systemd/docker 托管生命周期；`yiuk-mcp` 由 pi 按 MCP stdio 约定拉起和回收（不要自己常驻，常驻进程 stdin 一断就会被 SDK 退出）。所以"一键"这件事在服务器侧只等于：`systemctl restart yiuk-platform` + 重启 pi。

### 7.3 调用是怎么被发起的

正常形态下，pi 不写代码、不写脚本驱动：人在 pi 里说一句

> "设备码 903 174 411，临时密码 K7QM…，帮我装 dsh 和 dsh-pet，再放个一键启动脚本，最后自检一下。"

pi 依据工具描述自己编排成 §6 的那串调用（先 `list_devices` 确认在线 → `request_connection` 申请三项权限 → 等批准 → `execute_command` 装 → `get_command_result` 读输出 → `upload_file` 上传脚本 → `execute_command -DryRun` 冒烟 → `close_session`）。人只需要在设备上点一次"批准"。

两种例外：

- **单条命令**：用 `run_command`（内部封装了申请→等批→执行→等结果→关会话），一次工具调用搞定，代价是拿不到中途分页。
- **要留活口**：`run_command` 传 `keep_session_open: true`，之后继续用 `execute_command` / `upload_file` 复用同一会话。

---

## 8. 自检清单（出问题时按顺序掐）

| # | 检查 | 通过标准 |
| --- | --- | --- |
| 1 | 服务器上 `~/.config/Yiuk/DeviceAssist/platform-client.json` 存在，`address`/`player_password` 非空，`tls_ca` 可读 | 否则 `NOT_CONFIGURED` |
| 2 | `systemctl status yiuk-platform`；从服务器 `curl -k https://127.0.0.1:29564/` 或 `Test-NetConnection` | 服务在、端口通；不通看 `/opt/yiuk-platform/platform.log` |
| 3 | `tools/call list_devices` | 目标 `online:true`；否则看设备端"平台：已连接"状态 |
| 4 | `request_connection` 返回 `pending_approval` | 说明密码对、设备在线；卡多半是**密码错**或**没人点批准** |
| 5 | 设备端弹出审批框，活动面板出现"Agent 开始执行任务" | 帧已落到设备 |
| 6 | `execute_command` → `running` → `get_command_result` → `succeeded` | 命令真的跑了 |
| 7 | `exit_code != 0` | 是 PowerShell 的错而不是链路问题：把 `stderr` 块拼起来看 |
| 8 | `result_known:false` | 结果是**不可知**，不要盲重试 |
| 9 | `close_session` 后用同一 `session_id` 再执行 | 必须 `INVALID_SESSION_STATE` / `SESSION_NOT_FOUND` |

---

## 9. 现状与坑

- **README 里有三个悬空链接**：`docs/architecture.md`、`docs/product-requirements.md`、`docs/remote-deploy-session-2026-10-23.md` 在当前工作区已被删除（`git status` 为 ` D`）但未提交，README 仍指向它们。运行时细节以 `docs/runtime-architecture.md`、`docs/go-backend-guide.md` 和本文档为准。
- **平台服务完全内存化**：重启后设备密码、会话、任务全部失效，设备需重新注册（客户端会自动重连注册）。
- **两个同名的"客户端"**：`internal/platformclient`（Go，operator，跑在服务器）与 `qt-client/src/platformclient.{h,cpp}`（C++，device，跑在设备）。同名不同物。
- **服务器上别指望 `localbridge`**：它是给"本机设备"用的，服务器上没有 Qt 客户端，`platform-client.json` 是唯一 backend 来源。
- **跑 pi 的用户决定配置目录**：Linux 是 `~/.config/Yiuk/DeviceAssist`，Windows 是 `%LOCALAPPDATA%\Yiuk\DeviceAssist`。`yiukplatform` 服务账号无 home，不能拿来跑 pi。
- **936 代码页**：Windows 控制台里看 UTF-8 中文会乱码（文件本身没问题）；`.ps1` 含中文必须存成 UTF-8 **with BOM**，否则 PowerShell 5.1 按 ANSI 解码。
- **`.gitignore` 含 `*.ps1`、`tmp-*`、`*.exe`、`*.crt`、`*.key`**：如果最终要把设备侧脚本入库，放在 `scripts/` 并加 `!scripts/*.ps1` 例外，否则会被静默忽略。
