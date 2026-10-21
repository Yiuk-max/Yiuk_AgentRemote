# 服务器改造计划

版本：1.0  
状态：P0/P1/P2、Agent operator backend 与 Qt 设备端协议对接已实施；真实 Windows 验证、Job Object 进程树清理、P5 Minecraft 回归和 P6 发布前检查待完成（实施状态见 [backend-status.md](backend-status.md)）  
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

### 5.1 服务器中转

服务器中转用于两端无法直接连通或需要统一路由的场景。中转连接必须完成服务器 player 密码认证。

```text
操作者 Qt/Agent --TLS--> Go 服务 <--TLS-- 目标 Qt
                         |
                         +-- player password：允许使用中转
                         +-- temporary password：确认目标设备访问权
                         +-- session authorization：允许具体任务
```

服务端只转发已绑定到同一 session 的消息。未认证连接、不同设备连接、不同 session 连接和过期连接不得互相转发。

服务器中转至少分配独立的业务标识：

```text
business_type = agent_assist
business_type = minecraft
```

Minecraft 连接只能进入 Minecraft relay 房间；远程协助连接只能进入 agent session 路由。两类连接不得共享 session ID、权限或任务通道。

### 5.2 设备直连

设备直连用于双方网络可达的场景。直连不使用服务器玩家密码，也不占用服务器中转资源；直连仍然必须使用目标设备临时密码完成设备侧握手。如果设备选择等待同意模式，则直连只建立待批准连接，不能绕过批准。

如果直连失败并回退到服务器中转，客户端必须重新提供服务器玩家密码；不能把一次直连成功或临时密码校验结果当作中转资格。

建议首个版本先完成服务器中转闭环，再增加直连优化。直连协议和中转协议使用相同的设备、session 和权限校验字段，避免直连成为权限校验旁路。

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

## 8. 服务端模块计划

建议新增平台服务入口，不直接把所有新逻辑塞进现有 `internal/control`：

```text
cmd/platform/
internal/platform/
  auth/             player password、限流、连接认证
  device/           设备临时密码和在线状态
  approval/         请求、批准、拒绝、过期
  session/          session 授权和撤销
  task/             任务状态、结果块、取消、幂等
  transport/        TLS、帧、心跳、读写 deadline
  router/           操作者/设备的 session 路由
  audit/            最小化状态审计
internal/minecraft/ 现有 relay 兼容边界或适配器
```

现有目录的处理建议：

- `internal/room`、`internal/session` 的 TCP 双向复制能力优先保留并增加明确的 Minecraft 边界。
- 现有 `internal/control` 不直接承担设备授权；必要时仅作为 Minecraft 兼容协议适配层。
- `internal/protocol` 不继续扩展旧 Minecraft 字段承载远程协助语义；新平台定义独立、版本化消息。
- `internal/config` 拆出平台配置和 Minecraft relay 配置，避免新旧密码和端口混用。
- `internal/agenttools` 保持稳定，新增真实平台 backend；MCP adapter 不直接实现授权逻辑。

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

## 10. 配置计划

首个版本可继续使用 YAML，但将配置分组，避免把设备临时密码写入服务端配置：

```yaml
platform:
  listen: ":25664"
  tls_cert: "server.crt"
  tls_key: "server.key"
  player_password: "REPLACE_WITH_A_LONG_RANDOM_PLAYER_SECRET"
  max_connections: 1024
  max_devices: 1024
  max_sessions: 2048
  max_tasks_per_session: 2
  session_timeout_sec: 600
  approval_timeout_sec: 120
  max_output_bytes: 2097152

minecraft:
  control_port: 25564
  room_port_min: 25570
  room_port_max: 25599
  host_password: "REPLACE_WITH_A_LONG_RANDOM_HOST_SECRET"
  player_password: "REPLACE_WITH_A_LONG_RANDOM_MINECRAFT_PLAYER_SECRET"
```

如果服务器中转的 Minecraft player 密码和平台 player 密码暂时必须共用，也要在代码中保持业务认证对象和权限检查分离，并在日志、配置文档和后续迁移计划中标记为过渡方案。推荐最终使用不同密码。

## 11. 分阶段实施

### P0：冻结凭证和状态语义

- 冻结服务器玩家密码、设备临时密码和 session 授权的职责。
- 冻结 direct/approval 两种模式。
- 冻结所有终态、错误码、超时和重启语义。
- 确定首个版本是否只做服务器中转，直连作为后续阶段。
- 为 Minecraft 建立黑盒兼容清单。

退出条件：未认证中转、错误临时密码、未批准 session 和跨设备 session 的拒绝规则都有测试用例。

### P1：内存平台核心

- 新增 device、session、task 的内存管理器。
- 实现临时密码生成、哈希存储、过期和刷新。
- 实现 direct/approval 状态机。
- 实现 session 所有者和设备绑定。
- 实现内存资源上限和定时清理。

退出条件：不依赖网络即可通过单元测试覆盖全部状态转换、过期、撤销、重复请求和并发访问。

### P2：TLS 中转和双端连接

- 新增平台 TLS listener。
- 实现 player password 认证和连接限流。
- 实现设备连接、操作者连接和心跳。
- 实现 session 路由和双端断线清理。
- 实现最大帧、deadline、背压和优雅关闭。

退出条件：两个 mock 客户端可以经服务器完成认证、请求、批准、建立 session 和关闭 session；没有 player 密码的连接无法占用中转资源。

### P3：Qt 设备端接入

- Qt 启动时生成设备临时密码并显示有效期。
- Qt 通过 TLS 主动连接平台服务。
- Qt 支持 direct/approval 模式开关。
- Qt 显示待批准请求和活动会话。
- Qt 对每个 session 再校验设备 ID、session ID、权限和过期时间。

退出条件：两台 Windows 设备完成真实请求、批准、拒绝、过期和立即结束流程。

### P4：PowerShell 任务链路

- 将现有本机执行逻辑抽出为任务执行模块。
- 接入任务 ID、stdout/stderr 分块、退出码和截断标记。
- 实现超时、取消、进程树清理和断线处理。
- 将 MCP backend 从 local bridge 切换为平台 session backend。

退出条件：一个真实 MCP Agent 能在批准后的 session 中连续执行两条命令、读取结果、取消任务和关闭 session；未批准任务始终失败。

### P5：Minecraft 隔离和兼容回归

- 明确 Minecraft relay 与 platform listener 的端口、配置和资源边界。
- 对现有开房、列房、加入、双向流和房主恢复做端到端回归。
- 增加业务类型隔离测试，证明远程 session 不能访问 Minecraft 房间。
- 根据结果选择单进程分域或新旧 relay 并行部署。

退出条件：Minecraft 冻结清单真实通过，且新平台认证改造没有改变原有联机路径。

### P6：清理和发布前安全检查

- 检查服务重启后的状态清理。
- 检查密码、命令、输出和 TLS 私钥不进入日志。
- 检查临时密码刷新是否立即撤销旧 session。
- 检查 IDOR、重放、跨设备、跨 session 和重复任务。
- 检查慢客户端、输出上限、连接上限和资源泄漏。

## 12. 测试计划

### 单元测试

- player 密码正确、错误、缺失、限流和常量时间比较。
- 临时密码正确、错误、过期、刷新、撤销和版本变化。
- direct 模式直接进入 `approved`。
- approval 模式只能进入 `pending_approval`。
- 未批准、拒绝、过期、撤销和关闭状态拒绝命令。
- session 只能由绑定的操作者和目标设备访问。
- 重复 request ID 不重复创建任务。
- 服务重启或 manager 重建后旧 ID 和旧密码全部失效。

### 集成测试

- 无 player 密码的连接无法创建中转通道。
- 有 player 密码但无临时密码只能得到设备访问拒绝。
- 有临时密码但 approval 未批准时不能执行命令。
- owner 批准后，只有指定 operator 可以执行命令。
- 设备断线后 session 和任务进入正确终态。
- 设备刷新临时密码后旧密码、旧 session 和旧任务均不能继续创建新任务。
- 慢读操作者不会导致服务端无限缓存输出。
- 远程协助消息不能进入 Minecraft room。

### Windows 端到端测试

- 临时密码显示、复制、刷新和过期。
- direct 模式建立会话并执行 PowerShell。
- approval 模式显示用途、权限、批准/拒绝按钮。
- 所有者立即停止会话后正在执行的任务被取消。
- stdout、stderr、退出码、非 ASCII 输出、超时和输出截断正确。
- Qt 重启后旧 session 显示 interrupted/result_unknown，不显示成功。

### Minecraft 回归

- host/player 认证。
- 创建、列出、关闭房间。
- 玩家加入和实际双向游戏 TCP 流。
- 房主断线恢复。
- 达到房间、连接和待处理请求上限时正确拒绝。

## 13. 验收标准

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

## 14. 需要在实施前确认的问题

以下问题不影响先做 P0/P1，但必须在 P2 前确认：

1. 临时密码的默认有效期是 10 分钟、1 小时还是 24 小时。
2. direct 模式是否默认关闭，还是由设备所有者显式开启。
3. 直连失败时是否自动回退服务器中转；若回退，是否仍必须提供服务器玩家密码。
4. 服务器平台 player 密码与 Minecraft player 密码是否分开。
5. Qt 退出后是否立即使设备离线并撤销临时密码。
6. 服务重启后客户端是否自动重新生成临时密码，还是要求所有者手动刷新。
7. 是否允许一个设备同时存在多个 approval 请求。
8. 设备端是否允许多个 active session，以及每个 session 的最大任务并发数。
9. 首个版本是否只支持 Windows 设备和 PowerShell。

在这些问题确认前，不应把临时密码当作长期设备凭证，也不应开放无批准的永久远程控制。
