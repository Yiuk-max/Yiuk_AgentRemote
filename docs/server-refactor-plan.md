# 服务器改造计划

版本：1.0  
状态：P0–P4 已实施，P5 Minecraft 回归与 P6 发布前检查待完成（见 [backend-status.md](backend-status.md)）  
范围：Go 平台服务器、Minecraft 中转边界、Qt 设备连接协议  
存储基线：完全内存化，不使用数据库或文件持久化业务状态

## 1. 改造目标

本轮改造先不做账户系统、数据库、长期设备注册和复杂部署能力，优先形成一个可在两台 Windows 设备上验证的最小远程协助闭环：

1. 客户端可以使用服务器作为受控中转。
2. 只有持有服务器玩家密码的连接才能使用服务器中转能力。
3. 只有持有目标设备临时密码的操作者，才能访问指定设备或创建该设备的协助请求。
4. 目标设备临时密码支持两种授权方式：
   - **直接远程**：设备允许临时密码直接建立已授权会话。
   - **等待同意**：临时密码只允许创建待批准请求，命令必须等待设备所有者明确批准。
5. 无论采用哪种方式，服务端都必须绑定设备、操作者、会话和权限，禁止跨设备、跨会话复用。
6. 服务端完全内存化，重启后所有在线连接、临时密码、待批准请求、会话和任务状态失效。
7. Minecraft 联机继续保留，但与远程协助使用独立业务类型、认证边界和资源限制。

## 2. 本轮不做

- 不实现账户体系、OAuth、邮箱登录或组织租户。
- 不实现数据库、磁盘设备注册表或跨重启恢复。
- 不把设备码当作密码或认证凭证。
- 不提供无人值守永久授权。
- 不默认提权执行 PowerShell。
- 不把 Minecraft 房间权限转换成远程命令权限。
- 不在同一阶段重写 Minecraft MOD 和远程协助协议。
- 不把“服务器玩家密码”设计成所有设备共享的长期远程控制密码。

## 3. 凭证模型

### 3.1 服务器玩家密码

服务器玩家密码由服务器配置提供，仅用于证明连接方有资格使用服务器中转。它不代表对任何设备拥有控制权。

服务器接收控制连接时，客户端必须先发送：

```text
业务类型：minecraft 或 agent_assist
身份类型：player
服务器玩家密码：...
```

认证成功后，连接才可以申请使用对应业务类型的中转资源。认证失败、缺少密码或密码过期时，服务端拒绝建立中转通道。

约束：

- 服务端只保存进程内配置中的密码摘要或受保护的运行时值，不记录明文日志。
- 使用常量时间比较和按 IP/连接维度限流。
- Minecraft 房主能力继续使用独立的 host 权限或 host 密码，不与 player 权限混用。
- 服务器玩家密码不能直接执行远程命令。
- 服务器重启后重新加载配置；已建立的连接全部断开。

### 3.2 设备临时密码

设备临时密码由目标 Qt 客户端生成并显示给设备所有者，由所有者通过安全渠道交给操作者。它只对应一个设备，具有有限有效期和可主动刷新/撤销能力。

设备临时密码不能由设备码推导，也不能仅凭设备码绕过认证。服务端只在内存中保存临时密码的校验值及以下元数据：

```text
device_id
password_version
issued_at
expires_at
revoked
mode: direct 或 approval
```

默认建议：

- 长度至少 32 字符，显示时可以转换为分组短码，但传输值必须保持足够随机性。
- 默认有效期 10 分钟至 24 小时，由设备端生成策略决定；首个原型使用 10 分钟。
- 设备所有者刷新密码后，旧密码立即失效。
- 客户端退出、设备撤销或本地安全设置变化时，密码立即失效。
- 服务端不得把临时密码写入审计日志、错误消息或普通业务响应。

### 3.3 会话授权

设备临时密码不是无限期会话令牌。校验通过后，服务端创建一个独立的 session：

```text
operator_connection_id
device_id
session_id
permissions
authorization_mode
status
created_at
expires_at
last_activity
```

只有以下条件全部满足，远程命令才能提交：

- 服务器中转连接已通过 player 密码认证，或双方已建立允许的直连通道。
- 目标设备临时密码校验成功，且未过期、未撤销。
- session 属于当前操作者和目标设备。
- session 状态为 `approved` 或 `active`。
- session 权限包含 `execute_command`（命令）；文件下载需要独立的 `read_file` 权限。
- 设备端仍在线，并再次校验 session 和任务绑定。

## 4. 两种连接授权模式

### 4.1 直接远程模式

直接远程模式表示设备所有者通过临时密码授予一次短期访问能力。操作者提供目标设备 ID 和临时密码后，服务端或设备端完成密码校验，成功后建立 `approved` session。

流程：

```text
操作者 -> 目标设备：device_id + temporary_password
目标设备/服务端：校验密码、有效期和 password_version
目标设备/服务端：创建 approved session
操作者 -> 设备：提交 execute_command
设备：再次校验 session、权限和有效期
设备：执行普通用户权限 PowerShell
```

直接远程模式仍必须具备：

- 明确的设备端开关，例如“允许临时密码直接连接”。
- 会话有效期和单设备并发上限。
- UI 中可见的操作者、会话状态和立即停止入口。
- 任一方刷新临时密码或关闭会话后，旧 session 立即失效。

“直接”只表示不等待人工批准，不表示可以跳过设备身份校验、服务器 player 认证或会话权限校验。

### 4.2 等待对方同意模式

等待同意模式表示临时密码只能证明操作者持有设备所有者发放的临时访问信息，不能直接执行命令。服务端创建 `pending_approval` session，并向目标 Qt 客户端推送申请。

流程：

```text
操作者 -> 服务端：device_id + temporary_password + purpose + permissions
服务端：校验 player 认证和临时密码
服务端：创建 pending_approval session
服务端 -> 目标设备：推送操作者、用途、权限、过期时间
目标所有者：批准或拒绝
服务端：原子更新 session 状态
approved -> 允许已批准的 execute_command / read_file
rejected/expired/closed -> 拒绝所有操作
```

目标设备 UI 至少显示：

- 操作者标识或连接标识。
- 请求用途。
- 请求权限。
- 创建时间和过期时间。
- 批准、拒绝和立即结束入口。

在 `pending_approval`、`rejected`、`expired` 或 `closed` 状态下，服务端必须拒绝任务，不得因为操作者已经提供临时密码而自动放行。

## 5. 连接路径

客户端只主动出站。服务器中转与设备直连使用相同的设备、session 与权限校验字段，直连不构成授权旁路。


## 6. 服务端内存状态

首个版本使用进程内结构，不引入数据库。建议按领域拆分以下状态：

```text
ServerState
  server_auth
  devices map[DeviceID]*Device
  connections map[ConnectionID]*Connection
  sessions map[SessionID]*Session
  tasks map[RequestID]*Task
  minecraft rooms map[RoomID]*Room
```

### Device

```text
ID
Name
Platform
TemporaryPasswordHash
PasswordVersion
PasswordIssuedAt
PasswordExpiresAt
PasswordRevoked
Mode
OnlineConnectionID
LastHeartbeat
ActiveSessionIDs
```

### Session

```text
ID
DeviceID
OperatorConnectionID
DeviceConnectionID
Purpose
Permissions
Mode
Status
CreatedAt
ExpiresAt
ApprovedAt
ClosedAt
CloseReason
```

### Task

```text
RequestID
SessionID
CommandState
StartedAt
FinishedAt
ExitCode
StdoutChunks
StderrChunks
OutputBytes
Truncated
ResultKnown
CancelRequested
```

输出块必须有上限。服务端不能因为操作者不读取结果而无限缓存 stdout/stderr；达到上限后标记 `truncated=true`，并继续保留终态信息。

## 7. 会话和任务状态机

### 7.1 会话

```text
requested
  -> pending_approval
  -> approved
  -> active
  -> closed

pending_approval -> rejected
pending_approval -> expired
approved -> expired
active -> interrupted
active -> closed
```

状态转换规则：

- `requested` 只在服务端内部短暂存在，失败时不能留下半初始化 session。
- `pending_approval` 只有目标设备对应的 owner connection 可以批准或拒绝。
- `approved` 只允许绑定的操作者和目标设备使用。
- `active` 表示至少有一个有效任务或数据通道。
- `closed`、`rejected`、`expired`、`interrupted` 是终态，不能恢复或复用。
- 设备断线时，未完成 session 进入 `interrupted`；不得自动重放任务。
- 服务端重启时所有 session 直接丢失，客户端显示为断线/未知，不伪造成功。

### 7.2 任务

```text
queued -> running -> succeeded
                  -> failed
                  -> timed_out
                  -> cancelled
                  -> result_unknown
```

任务必须使用不可重用的 request ID。服务端对重复 request ID 返回已有状态，不再次启动命令；连接断开后无法确认是否启动的任务进入 `result_unknown`，不能自动重试。

## 8. 服务端模块

见[开发与构建](development.md#源码结构)。


## 9. 新平台协议最小集合

首个版本使用 TLS 长连接和长度限制的版本化帧。控制消息至少包含：

```text
version
business_type
message_type
request_id
connection_id
device_id
session_id
sequence
```

消息类型建议包括：

```text
authenticate_player
register_device
device_heartbeat
update_temporary_password
request_session
approve_session
reject_session
session_status
open_session_channel
execute_command
command_output
command_result
cancel_command
close_session
ping
pong
error
```

密码字段只允许出现在必要的认证消息中。服务端日志只记录错误码、设备 ID、session ID 和请求 ID 的脱敏形式，不记录密码、命令正文或完整输出。

所有消息都需要：

- 最大帧长度。
- 读写 deadline。
- 连接级心跳。
- 慢客户端背压。
- 无效状态转换拒绝。
- 连接关闭后的资源清理。
- 业务类型和 session 绑定校验。

## 10. 配置

平台配置项见 `configs/platform.yaml`。Minecraft relay 由独立进程与配置承载，平台服务不读取。


## 11. 验收标准

本轮服务器改造完成必须同时满足：

1. 完全没有业务数据库或持久化 session 存储。
2. 服务器重启后旧设备连接、临时密码、请求和 session 全部不可用。
3. 没有服务器玩家密码，任何服务器中转连接都无法建立。
4. 只有设备临时密码可以访问指定设备的连接入口。
5. direct 模式不等待批准即可建立短期已授权 session，但仍受设备开关、期限和权限限制。
6. approval 模式必须由对方设备明确批准；临时密码不能直接绕过批准。
7. 未批准、被拒绝、过期、撤销或关闭的 session 不能执行命令。
8. 设备所有者可以立即结束 session，并终止正在运行的任务。
9. 服务端不记录密码、完整命令或完整输出。
10. Minecraft 既有冻结兼容清单通过真实端到端验证。
11. 远程协助和 Minecraft 的认证、session、端口、限额和日志边界明确分离。
