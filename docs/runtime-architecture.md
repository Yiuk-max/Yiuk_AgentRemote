# 运行时架构：消息转发、MCP 工具与客户端

状态：与当前代码一致（`agentremote` module）。本文只讲**后端运行时、协议转发、MCP 工具契约和客户端非界面逻辑**，不涉及 Qt 界面实现。

关联文档：[架构与实现](architecture.md)（选型与授权模型）、[Go 后端技术说明](go-backend-guide.md)（Go 入门与幂等/SHA-256 详解）、[开发与构建](development.md)。

目录：

1. [连接拓扑](#1-连接拓扑)
2. [连接生命周期：读循环与写循环](#2-连接生命周期读循环与写循环)
3. [身份、注册与 deviceID→connID 映射](#3-身份注册与-deviceidconnid-映射)
4. [会话：把两条连接绑在一起](#4-会话把两条连接绑在一起)
5. [去程：命令怎么到目标设备](#5-去程命令怎么到目标设备)
6. [回程：执行结果怎么回来](#6-回程执行结果怎么回来)
7. [一次完整调用](#7-一次完整调用)
8. [失败与收敛](#8-失败与收敛)
9. [资源上限](#9-资源上限)
10. [MCP：定位与 stdio 适配](#10-mcp定位与-stdio-适配)
11. [MCP 工具清单](#11-mcp-工具清单)
12. [MCP 结果形状与稳定错误码](#12-mcp-结果形状与稳定错误码)
13. [两种使用模式与幂等语义](#13-两种使用模式与幂等语义)
14. [MCP backend 选择与宿主配置](#14-mcp-backend-选择与宿主配置)
15. [操作者客户端（Go platformclient）](#15-操作者客户端go-platformclient)
16. [设备客户端（Qt，非界面部分）](#16-设备客户端qt非界面部分)
17. [消息方向速查](#17-消息方向速查)
18. [测试与联调](#18-测试与联调)
19. [已知缺口](#19-已知缺口)
20. [关键代码索引](#20-关键代码索引)

---

## 1. 连接拓扑

平台是**中继（relay）**，不是反向代理，也不做设备间直连。三条 WebSocket（WS/WSS）长连接全部由客户端**主动出站**连到平台的同一个监听端口；端口沿用部署配置，不要求使用 443：

```text
操作者机                                         设备所有者机
platformclient / yiuk-mcp                        Qt 客户端
   │ conn#A (operator)                              │ conn#B (device)
   │ TLS 1.2+                                       │ TLS 1.2+
   └──────────────►  平台 :29564  ◄─────────────────┘
                     Server.conns map
                     ├─ A: role=rolePlayer
                     └─ B: role=roleDevice, deviceID=111222333
```

这一点决定了三件事：

- 设备在 NAT/防火墙后面也能被访问，因为**它自己连出来**；
- 平台**从不主动 dial** 任何设备，没有“下发连接”的概念；
- “把命令发给设备”在实现上等于“把帧塞进那条已存在的设备连接的发送队列”。

平台维护的索引只有两层：

| 映射 | 位置 | 用途 |
| --- | --- | --- |
| `connID → *conn` | `Server.conns` + `sync.Mutex` | 找到连接对象、写入它的发送队列 |
| `deviceID → connID` | `device.Manager` | 把“发给某台设备”翻译成“发给哪条连接” |
| `sessionID → *Session` | `session.Manager` | 授权校验与状态机 |

---

## 2. 连接生命周期：读循环与写循环

### 2.1 accept：每连接一个 goroutine

`internal/platform/server/server.go`：

```go
for {
    nc, err := s.listener.Accept()
    if err != nil { s.Close(); s.wg.Wait(); ... }
    s.wg.Add(1)
    go func() {
        defer s.wg.Done()
        s.serveConn(nc)
    }()
}
```

`serveConn` 建连接对象、注册进 `conns`，然后：

```go
go c.writeLoop()   // 唯一的写者
s.readLoop(c)      // 当前 goroutine：读 + 处理
```

所以**一条连接固定两个 goroutine**，没有线程池、没有 worker 池、没有任务队列。

### 2.2 读循环：同步处理，严格串行

`internal/platform/server/conn.go`：

```go
func (s *Server) readLoop(c *conn) {
    deadline := s.cfg.HandshakeTimeout()   // 默认 10s
    handshake := true
    for {
        _ = c.nc.SetReadDeadline(time.Now().Add(deadline))
        data, err := c.nc.ReadMessage()
        if err != nil { ... return }
        m, err := s.codec.Decode(data)
        if err != nil { ... return }
        c.lastSeen = time.Now()
        if err := s.handle(c, m); err != nil { ... }   // 同步，不是 go s.handle
        if handshake && c.role != roleAnonymous {
            handshake = false
            deadline = s.cfg.HeartbeatTimeout()        // 默认 45s
        }
    }
}
```

两个要点：

1. **`s.handle` 是普通函数调用**，同一连接的消息严格按到达顺序处理。这是文件分块 `sequence` 校验、任务状态单向推进能成立的前提。
2. **读超时会变**：认证前是 `handshake_timeout_sec`（10s），认证后放宽到 `heartbeat_timeout_sec`（45s），因为长连接靠心跳而不是业务流量保活。

### 2.3 写循环：有界队列 + 单一写者

```go
func (c *conn) writeLoop() {
    defer c.close("write_loop")
    for {
        select {
        case m := <-c.send:
            if err := c.writeRaw(m); err != nil { return }
        case <-c.done:
            return
        }
    }
}
```

`c.send` 是容量为 `observer_slow_client_buf`（默认 64）的 channel。**为什么要单独的写 goroutine**：

- 读取侧不能因为对端 socket 缓冲满而卡住，否则一个慢设备会阻塞所有想给它发消息的处理 goroutine；
- 所有业务帧有唯一写者，天然不会出现多线程写 socket 的**字节交错**。

### 2.4 `writeMu` 为什么仍然存在

```go
func (c *conn) writeRaw(m protocol.Message) error {
    c.writeMu.Lock()
    defer c.writeMu.Unlock()
    _ = c.nc.SetWriteDeadline(time.Now().Add(10 * time.Second))
    data, err := c.srv.codec.Encode(m)
    if err != nil { return err }
    return c.nc.WriteMessage(data)
}
```

因为**致命错误路径会绕过队列同步写**：`readLoop` 遇到 `fatal` 协议错误时直接 `c.writeRaw(errMsg)`，保证对端先看到错误码再被断开。这时是两个 goroutine 并发写同一 socket，必须串行化。`SetWriteDeadline(10s)` 防止对端不读导致永久阻塞。

### 2.5 背压策略：慢客户端直接断开

`internal/platform/server/routing.go`：

```go
select {
case c.send <- m:
    return true
case <-c.done:
    return false
default:                                  // 队列满
    s.audit.Warn("slow_client_disconnected", "connection", audit.RedactID(c.id))
    c.close("slow_client")
    return false
}
```

队列满不阻塞调用者，也不无限堆内存，而是判定对端处理不过来并断开。这是“有界队列 + 明确拒绝”的背压模型。

---

## 3. 身份、注册与 deviceID→connID 映射

### 3.1 三种角色

```go
const (
    roleAnonymous role = iota   // 已连接未认证
    rolePlayer                  // 平台认证通过
    roleDevice                  // 已注册设备
)
```

### 3.2 平台认证（两条连接都要做）

`authenticate_player` → `handleAuthenticate`：

- 业务类型必须是 `agent_assist`，其他业务一律拒绝；
- `identity_type` 必须是 `player`；
- 密码用 `crypto/subtle.ConstantTimeCompare` 比对，并按来源 IP 限流（默认 30 次/分钟）；
- 通过后 `c.role = rolePlayer`，回 `authenticated` 并带 `connection_id`。

**player 密码只证明“可以使用平台中继资源”，不授予任何设备权限。**

### 3.3 设备注册

设备连接在同一套认证之后再发 `register_device`：

```json
{"type":"register_device","device_id":"111222333","temporary_password":"<32 位>",
 "mode":"approval","password_version":2,"expires_at":1760000000}
```

`handleRegisterDevice` 关键动作：

```go
info, generated, err := s.devices.Register(device.Registration{...})  // 校验临时密码盐+SHA256、版本、TTL、撤销
if _, err := s.devices.SetOnline(info.ID, c.id); err != nil { ... }   // ★ deviceID -> connID
c.role   = roleDevice
c.deviceID = info.ID
```

`device.Manager.Register` 的安全语义：

- **重新注册不是“知道公开设备码就能抢设备”**，必须提供当前有效的临时密码（比对 `hashPassword(salt, candidate)`）；
- 已在线设备不允许被另一条连接注册（`ErrDeviceOnline`）；
- 已撤销的临时密码拒绝注册（`ErrPasswordRevoke`）；
- 临时密码为空或已过期时，服务端生成新的 32 位密码并在 `device_registered` 里返回。

另有一条自愈逻辑：如果设备码标记在线但对应连接已不在 `conns` 里（进程崩过），先 `ForceOffline` 再允许注册。

---

## 4. 会话：把两条连接绑在一起

`request_session` 到达时（`handler_session.go`）：

```go
onlineID, ok := s.devices.OnlineConnection(m.DeviceID)   // deviceID -> 当前在线 connID
if !ok { return perr("DEVICE_OFFLINE", ...) }

s.sessions.Create(id, session.CreateRequest{
    DeviceID:             m.DeviceID,
    OperatorConnectionID: c.id,       // ★ A 的 connID（谁来操作）
    DeviceConnectionID:   onlineID,   // ★ B 的 connID（建会话那一刻锁定）
    Permissions:          m.Permissions,
    Mode:                 device.Mode(m.Mode),
    ...
})
```

`Session` 就是一张把“谁在请求”和“谁被请求”钉在一起的授权记录：

```text
OperatorConnectionID = A
DeviceConnectionID   = B
Permissions          = {execute_command}
Status               = pending_approval | approved
ExpiresAt            = ...
```

`approval` 模式下：`session_request` 推给 B，所有者批准后回 `approve_session`，`session.Approve` 先做归属校验：

```go
if s.DeviceConnectionID != deviceConnectionID {
    return nil, ErrNotOwner        // 只有目标设备那条连接能批准
}
```

`direct` 模式下创建即 `approved`，但仍需临时密码校验、会话有效期和权限集，只是跳过人工批准。

**重要推论：设备重连后拿到的 connID 变了，旧会话不会自动迁移到新连接。** 后续所有操作都会因 `onlineID != ses.DeviceConnectionID` 而被拒绝，必须重新走一次申请/批准。

---

## 5. 去程：命令怎么到目标设备

以 `execute_command` 为例，一次调用跨两次进程边界、经过两次队列：

```text
operator goroutine                 平台                            device goroutine
─────────────────                  ────                            ────────────────
callStream()
  writeMessage(message)
        │ TLS
        ▼
                    readLoop(connA)        ← 每连接一个读 goroutine
                      codec.Decode()       ← 完整 WebSocket binary message 中的 JSON
                      s.handle(c, m)
                        handleExecuteCommand()
                          Authorization              ← 会话绑定 + 权限
                          devices.OnlineConnection() ← 设备还在线吗
                          tasks.Start(request_id)    ← 幂等去重
                          sendToConnection(B, forward)
                                   │
                                   ▼ c.send <- msg   （有界队列，容量 64）
                             writeLoop(connB)
                               writeRaw()             ← writeMu + 写 deadline
                                   │ TLS
                                   ▼
                                                       readLoop(connB)
                                                         s.handle()
                                                           → PowerShellRunner
```

### 5.1 handler 的校验顺序

`internal/platform/server/handler_command.go`：

```go
1. validID("session_id"), validID("request_id")
2. command 非空且 <= command_max_len（默认 32 KiB）
3. working_directory <= 4096
4. timeout 缺省 60s，范围 1..3600
5. sessions.Authorize(sessionID, c.id, PermissionExecuteCommand)
      → 会话存在 / 未终态 / 未过期 / c.id == OperatorConnectionID / 有该权限
6. devices.OnlineConnection(deviceID) == ses.DeviceConnectionID 且连接仍存在
7. tasks.Start(sessionID, requestID, timeout)   ← 幂等入口
8. sessions.Activate(...)                        ← approved -> active
9. sendToConnection(ses.DeviceConnectionID, forward)
```

第 5、6 步是防越权的核心：**不能只拿 session_id 就操作**，必须同时满足“这条连接是会话的 operator”和“设备仍是会话绑定的那条在线连接”。

### 5.2 幂等入口

```go
t, created, err := s.tasks.Start(ses.ID, m.RequestID, timeout)
if !created {
    // 同一 request_id 重复到达：返回已有任务状态，绝不向设备再发一条
    return s.sendOK(c, m, protocol.Message{Type: TypeCommandAccepted, RequestID: t.RequestID, Status: string(t.Status)})
}
```

`task.Manager.Start` 的规则：

| 情况 | 结果 |
| --- | --- |
| request_id 不存在 | 新建任务，`created=true` |
| 已存在且同一 session | 返回已有任务，`created=false` |
| 已存在但另一 session | `ErrRequestConflict` → `REQUEST_ID_CONFLICT` |
| 该 session 活跃任务已达上限 | `ErrTaskLimit` → `TASK_LIMIT` |

### 5.3 转发

```go
forward := protocol.Message{
    Type: TypeExecuteCommand, SessionID: ses.ID, RequestID: t.RequestID,
    Command: m.Command, WorkingDirectory: m.WorkingDirectory, TimeoutSeconds: timeout,
}
if !s.sendToConnection(ses.DeviceConnectionID, forward) {
    s.tasks.Finish(ses.ID, m.RequestID, task.StatusResultUnknown, nil, false)
    return perr("DEVICE_UNAVAILABLE", "could not deliver command to device")
}
```

转发失败（连接已关闭/队列已死）时任务直接标记 `result_unknown`：命令可能到了也可能没到，服务端不猜测。

### 5.4 两个 ID 的分工

| ID | 谁生成 | 语义 | 出现位置 |
| --- | --- | --- | --- |
| `correlation_id` | 调用方，**每次请求新生成** | 这条直接响应回应的是哪次请求 | 直接响应回显；**异步事件为空** |
| `request_id` | 调用方，**代表一次业务操作** | 这是哪个任务/传输，用于幂等与查询 | 命令与文件传输的所有帧 |

`sendOK` 统一负责回显：

```go
func (s *Server) sendOK(c *conn, req, resp protocol.Message) error {
    resp.CorrelationID = req.CorrelationID
    s.send(c, resp)
    return nil
}
```

异步事件（`command_output`、`command_result`、`file_chunk`、`session_request`…）**不带** `correlation_id`，靠 `session_id` + `request_id` 关联。

---

## 6. 回程：执行结果怎么回来

设备发两类消息：流式 `command_output` 与终态 `command_result`。

### 6.1 输出

```go
func (s *Server) handleCommandOutput(c *conn, m protocol.Message) error {
    if c.role != roleDevice {
        return perr("NOT_DEVICE", "only the target device can send command output")
    }
    ses, ok := s.sessions.Get(m.SessionID)
    // 三重绑定：设备码对 + 连接对 + 会话对
    if !ok || ses.DeviceID != c.deviceID || ses.DeviceConnectionID != c.id {
        return perr("SESSION_NOT_FOUND", "session not found")
    }
    t, err := s.tasks.AppendOutput(ses.ID, m.RequestID, m.Stream, m.Data)  // 有总字节预算
    ...
    s.sendToConnection(ses.OperatorConnectionID, protocol.Message{
        Type: TypeCommandOutput, SessionID: ses.ID, RequestID: m.RequestID,
        Stream: m.Stream, Data: data, Sequence: sequence,
        Truncated: t.Truncated, OutputBytes: t.OutputBytes,
    })
}
```

`AppendOutput` 的行为：

- 每任务 stdout+stderr 合计预算 `max_output_bytes`（默认 2 MiB）；
- 超出预算的数据**丢弃**，只置 `truncated=true`，继续允许任务正常终止；
- 每个块分配递增 `sequence`，用于客户端去重和 cursor 分页。

### 6.2 终态

```go
status, err := terminalTaskStatus(m.Status)                         // 只接受终态
resultKnown := m.ResultKnown || status != task.StatusResultUnknown
t, err := s.tasks.Finish(ses.ID, m.RequestID, status, m.ExitCode, resultKnown)
s.sendToConnection(ses.OperatorConnectionID, taskResultMessage(t))
```

`Finish` 的幂等保护：任务已终态时直接返回原记录，**后到的结果不会覆盖先到的终态**。

终态集合：`succeeded | failed | timed_out | cancelled | result_unknown`。

### 6.3 操作者侧如何接收

```go
// platformclient/client.go
case m := <-inbox:
    c.observe(m)                       // ★ 先把异步事件写进本地缓存
    if m.CorrelationID != correlationID {
        continue                       // 不是本次调用的响应，继续等
    }
    if match(m) { return m, nil }
```

因此：

- `execute_command` 的直接响应 `command_accepted` 立刻返回，工具调用**不阻塞到命令跑完**；
- 后续 `command_output` / `command_result` 因为没有 `correlation_id` 会被 `continue` 跳过，但 `observe` 已经把内容写进 `taskCache` / `downloadCache`；
- `get_command_result` 再按 `cursor` 取值（`cursor` 是**块索引**，不是字节偏移），服务端还支持 `next_cursor`。

---

## 7. 一次完整调用

```text
Agent           platformclient        Go 平台                 Qt 设备端
  │                  │                   │                       │
  │ run_command      │                   │                       │
  ├─────────────────►│                   │                       │
  │                  │ authenticate_player                      │
  │                  ├──────────────────►│ (connA role=rolePlayer)
  │                  │◄──authenticated───┤                       │
  │                  │                   │◄──register_device─────┤
  │                  │                   │   devices.SetOnline(B) │
  │                  │ request_session   │                       │
  │                  ├──────────────────►│ sessions.Create(op=A,dev=B)
  │                  │                   ├──session_request─────►│
  │                  │                   │◄──approve_session─────┤ 所有者批准
  │                  │◄─session_created──┤ Status=approved        │
  │                  │                   │                       │
  │                  │ execute_command   │                       │
  │                  ├──────────────────►│ Authorize / tasks.Start│
  │                  │◄command_accepted──┤ （回显 correlation_id）│
  │                  │                   ├──execute_command─────►│ PowerShell 启动
  │                  │                   │◄──command_output──────┤ stdout
  │                  │◄──command_output──┤ AppendOutput（限额）   │
  │                  │                   │◄──command_output──────┤ stderr
  │                  │◄──command_output──┤                       │
  │                  │                   │◄──command_result──────┤ 终态 + exit_code
  │                  │◄──command_result──┤ Finish（幂等）        │
  │                  │                   │                       │
  │                  │ get_command_result│                       │
  │                  ├──────────────────►│ tasks.Result(cursor)  │
  │                  │◄──chunks+next─────┤                       │
  │◄──stdout/stderr──┤                   │                       │
  │                  │ close_session     │                       │
  │                  ├──────────────────►│ (keep_session_open=false 时自动)
```

`run_command` 是“一次性”封装：内部 `request_session`（可选等待批准）→ `execute_command` → 轮询 `get_command_result` 直到终态 → 关闭会话 → 把 chunks 聚合成 `stdout` / `stderr` 两个字符串。

---

## 8. 失败与收敛

### 8.1 提交阶段就拦住

| 情况 | 错误码 |
| --- | --- |
| 会话不存在/不属于本连接/无该权限/已过期 | `SESSION_NOT_FOUND`、`PERMISSION_DENIED`、`SESSION_EXPIRED`、`INVALID_SESSION_STATE` |
| 设备离线或已换连接 | `DEVICE_OFFLINE` |
| 同一个 request_id 属于别的会话 | `REQUEST_ID_CONFLICT` |
| 会话活跃任务/传输达上限 | `TASK_LIMIT` / `TRANSFER_LIMIT` |

### 8.2 转发失败

`sendToConnection` 返回 false → 任务/传输直接进 `result_unknown`，返回 `DEVICE_UNAVAILABLE`。**不重试**。

### 8.3 对端断线

设备断线（`server.go cleanupDevice`）：

```go
_, owned := s.devices.SetOffline(c.deviceID, c.id)
interrupted := s.sessions.InterruptByDevice(c.deviceID, "device_disconnected")
for _, ses := range interrupted {
    finished := s.tasks.FinishSession(ses.ID, task.StatusResultUnknown)   // 结果不可知
    for _, t := range finished { s.sendToConnection(ses.OperatorConnectionID, taskResultMessage(t)) }
    // 传输同样标记 result_unknown 并通知操作者
}
```

操作者断线（`cleanupOperator`）：

```go
interrupted := s.sessions.InterruptByOperator(c.id, "operator_disconnected")
finished := s.tasks.FinishSession(ses.ID, task.StatusCancelled)          // 本地可确定
s.sendToConnection(ses.DeviceConnectionID, {Type: close_session, Code: "OPERATOR_DISCONNECTED"})
```

两者刻意不同：设备断线时命令是否执行无法确认（`result_unknown`），操作者断线时平台知道应终止（`cancelled`）并让设备停止 PowerShell。

### 8.4 超时与 janitor

`janitor` 每 **1 秒** sweep 一次（`internal/platform/server/janitor.go`）：

| 对象 | 触发条件 | 动作 |
| --- | --- | --- |
| 会话 | `approval_timeout_sec`(120s) 或 `session_timeout_sec`(600s) 到期 | 置 `expired`，向设备发 `close_session(SESSION_EXPIRED)`，任务标 `cancelled`，通知操作者 |
| 命令任务 | 任务 deadline 到期 | 置 `timed_out`，向设备发 `cancel_command`，通知操作者 |
| 上传/下载 | 5 分钟未完成 | 置 `result_unknown`，向设备发对应 cancel |
| 离线设备 | 超过 `offline_device_keep_sec`(24h) | 清理记录 |

服务端超时和设备端超时是两套：服务端负责回收槽位与可观测性，设备端 `PowerShellRunner` 才是真正 kill 进程的一方。

### 8.5 心跳

| 侧 | 频率 | 失效判定 |
| --- | --- | --- |
| 设备（Qt） | 15s 一个 `device_heartbeat` | 服务端读 deadline 45s，超时断开 |
| 操作者（Go） | 空闲 15s 一个 `ping`（`heartbeat_interval_seconds` 可覆盖，下限 1s） | 连续 3 个周期没 `pong` 主动断开，下次工具调用重连 |

心跳只证明链路活着，**不会触发任何业务重放**。

---

## 9. 资源上限

`internal/platform/config/config.go` 的硬编码默认值：

| 配置 | 默认 | 作用 |
| --- | --- | --- |
| `max_connections` | 1024 | 连接总数 |
| `max_devices` | 1024 | 设备总数 |
| `max_sessions` | 2048 | 会话总数 |
| `max_sessions_per_device` | 8 | 单设备并发会话 |
| `max_tasks_per_session` | 2 | 单会话并发任务（下载/上传也用它） |
| `session_timeout_sec` | 600 | 批准后的会话有效期 |
| `approval_timeout_sec` | 120 | 等待批准时限 |
| `temp_password_ttl_sec` | 600 | 临时密码有效期 |
| `heartbeat_timeout_sec` | 45 | 认证后读 deadline |
| `handshake_timeout_sec` | 10 | 认证前读 deadline |
| `max_output_bytes` | 2 MiB | 单任务输出总预算 |
| `max_download_bytes` / `max_upload_bytes` | 32 MiB | 单文件上限 |
| `max_frame_bytes` | 256 KiB | 单帧上限 |
| `auth_rate_per_min` | 30 | 每 IP 每分钟认证尝试 |
| `observer_slow_client_buf` | 64 | 每连接发送队列长度 |
| `command_max_len` / `working_dir_max_len` / `identifier_max_len` | 32 KiB / 4 KiB / 128 | 输入边界 |

单块上限由 `transfer.MaxChunkBytes = 96 KiB`（解码后）和 `MaxChunks = 4096` 约束；客户端按 64 KiB 切块，Base64 后约 87 KiB，仍在 256 KiB 帧内。

---

## 10. MCP：定位与 stdio 适配

MCP（Model Context Protocol）是让 AI Host 以标准方式发现和调用外部工具的协议，可以理解为“AI 工具 RPC”。本项目的形态：

```text
AI Host（例如 pi）
    │ MCP over stdio（JSON-RPC）
    v
本机子进程 yiuk-mcp
    │ Go 内部调用
    v
platformclient  ──TLS──►  Go 平台  ──TLS──►  Qt 设备端
```

为什么单独进程：stdio 传输要求 stdout **只输出协议消息**，任何调试输出混进去都会破坏协议；把 MCP 放进 GUI 主进程会让生命周期和标准输入输出都变复杂。

适配层 `internal/mcpadapter/server.go` 做的事很少——只做协议映射：

```go
func NewServer(core *agenttools.Core) *mcp.Server {
    server := mcp.NewServer(&mcp.Implementation{Name: "agentremote", Version: "0.1.0"}, nil)
    for _, spec := range core.Tools() {
        server.AddTool(&mcp.Tool{
            Name: spec.Name, Description: spec.Description,
            InputSchema: spec.InputSchema, OutputSchema: spec.OutputSchema,
            Annotations: &mcp.ToolAnnotations{
                ReadOnlyHint:    spec.Annotations.ReadOnly,
                DestructiveHint: spec.Annotations.Destructive,
                IdempotentHint:  spec.Annotations.Idempotent,
                OpenWorldHint:   spec.Annotations.OpenWorld,
            },
        }, newHandler(spec.Name))
    }
    return server
}
```

工具调用结果的形状：

```go
response := core.Call(ctx, toolName, req.Params.Arguments)
content, _ := json.Marshal(response)
return &mcp.CallToolResult{
    Content:           []mcp.Content{&mcp.TextContent{Text: string(content)}},
    StructuredContent: response,      // 结构化消费
    IsError:           !response.OK,  // 让 Host 感知失败
}, nil
```

`agenttools.Core.Call` 在调用后端之前先做**输入校验**（`validateInput`）：

- 参数必须是 JSON 对象；
- 必填字段必须存在；
- 所有字符串字段类型必须正确，非 `working_directory` 不得为空；
- **未知字段直接拒绝**（`unknown field "x"`），避免模型传错参数被静默忽略；
- 长度/范围边界（identifier ≤128、purpose ≤512、command ≤32768、timeout 1..3600、cursor ≥0、wait ≤300）。

---

## 11. MCP 工具清单

所有工具定义在 `internal/agenttools/tools.go`。

| 工具 | 必填参数 | 可选参数 | 需要权限 | 注解 |
| --- | --- | --- | --- | --- |
| `list_devices` | — | — | 仅 player 认证 | readOnly, idempotent |
| `get_device_status` | `device_id` | — | 仅 player 认证 | readOnly, idempotent |
| `request_connection` | `device_id`、`purpose`、`permissions`、`temporary_password` | `wait_for_approval_seconds` | 临时密码 | destructive=false |
| `run_command` | `device_id`、`temporary_password`、`command` | `purpose`、`working_directory`、`timeout_seconds`、`wait_for_approval_seconds`、`keep_session_open` | 内部申请 `execute_command` | destructive=true |
| `get_session_status` | `session_id` | — | 会话归属 | readOnly, idempotent |
| `execute_command` | `session_id`、`command` | `working_directory`、`timeout_seconds` | `execute_command` | destructive=true |
| `get_command_result` | `session_id`、`request_id` | `cursor` | 会话归属 | readOnly, idempotent |
| `download_file` | `session_id`、`remote_path` | `destination_path` | `read_file` | destructive=false |
| `upload_file` | `session_id`、`local_path` | `remote_path`、`overwrite` | `write_file` | destructive=true |
| `cancel_command` | `session_id`、`request_id` | — | 会话归属 | destructive=true |
| `close_session` | `session_id` | — | 会话归属 | destructive=true, idempotent |

注解只是**给 Host 做确认策略提示**（例如“这个工具可能改动远端状态，先问人”），**不是安全边界**——真正的校验永远在平台服务里。

### 11.1 参数语义要点

- `temporary_password`：32～256 字符，由设备所有者给出；**平台只存盐 + SHA-256**，不回显。
- `permissions`：`execute_command` / `read_file` / `write_file` 三者独立，不允许重复，至少一个。下载不会自动获得命令执行权。
- `wait_for_approval_seconds`：0～300，仅影响客户端是否轮询等待，不改变服务端 `approval_timeout_sec`。
- `keep_session_open`：`run_command` 默认执行完就关会话；置 true 可复用会话继续操作。
- `cursor`：`get_command_result` 的**块索引**，返回 `next_cursor`。
- `overwrite`：`upload_file` 默认 false，设备端拒绝替换已有文件。
- `destination_path`：`download_file` 指定时必须绝对路径且**必须不存在**。

---

## 12. MCP 结果形状与稳定错误码

统一响应：

```json
{ "ok": true, "code": "OK", "message": "", "data": { } }
```

失败时 `ok=false`，`code` 是稳定错误码，`message` 是给人/模型看的短说明。**调用方应基于 `code` 分支，而不是解析 `message`。**

### 12.1 客户端/工具层错误码

| code | 触发点 |
| --- | --- |
| `UNKNOWN_TOOL` | 工具名不在白名单 |
| `INVALID_ARGUMENT` | 参数校验失败（缺字段、类型错、越界、未知字段） |
| `NOT_CONFIGURED` | 既没有平台配置也没有本地 bridge 配置 |
| `INTERNAL` | 后端返回了非结构化错误 |
| `PLATFORM_UNAVAILABLE` | 连不上平台 / 连接中途断开 |
| `PLATFORM_TIMEOUT` | 平台在超时内没有响应 |
| `CANCELLED` | 调用被 context 取消 |
| `LOCAL_CLIENT_UNAVAILABLE` | 本机 Qt bridge 不可用 |
| `LOCAL_BRIDGE_ERROR` | bridge 返回非法响应 |
| `LOCAL_PATH_EXISTS` | 下载目标已存在，拒绝覆盖 |
| `LOCAL_READ_FAILED` / `LOCAL_WRITE_FAILED` | 本地文件读写失败 |
| `FILE_TOO_LARGE` | 超过 32 MiB |
| `FILE_INTEGRITY_FAILED` | 大小或 SHA-256 不匹配 |
| `FILE_UPLOAD_FAILED` / `FILE_DOWNLOAD_FAILED` | 传输终态非成功 |

### 12.2 平台侧错误码（`internal/platform/server/errors.go`）

| 分组 | code |
| --- | --- |
| 认证 | `BAD_PASSWORD`、`RATE_LIMITED`、`AUTH_REQUIRED`、`ALREADY_AUTHENTICATED`、`BAD_IDENTITY`、`BUSINESS_TYPE_NOT_SUPPORTED`、`BAD_VERSION` |
| 设备 | `DEVICE_NOT_FOUND`、`DEVICE_LIMIT`、`DEVICE_ALREADY_ONLINE`、`DEVICE_OFFLINE`、`DEVICE_UNAVAILABLE`、`BAD_TEMPORARY_PASSWORD`、`TEMPORARY_PASSWORD_EXPIRED`、`TEMPORARY_PASSWORD_REVOKED`、`STALE_PASSWORD_VERSION`、`WEAK_TEMPORARY_PASSWORD` |
| 会话 | `SESSION_NOT_FOUND`、`SESSION_LIMIT`、`SESSION_EXPIRED`、`INVALID_SESSION_STATE`、`PERMISSION_DENIED` |
| 任务 | `TASK_NOT_FOUND`、`TASK_LIMIT`、`TASK_ALREADY_FINISHED`、`REQUEST_ID_CONFLICT` |
| 传输 | `TRANSFER_NOT_FOUND`、`TRANSFER_LIMIT`、`TRANSFER_IN_PROGRESS`、`TRANSFER_ALREADY_FINISHED`、`INVALID_CHUNK_SEQUENCE`、`CHUNK_TOO_LARGE`、`CHUNK_LIMIT`、`FILE_TOO_LARGE` |
| 角色 | `NOT_DEVICE`、`UNKNOWN_MESSAGE` |

错误码同时写进脱敏审计日志；日志里**不记录密码、命令正文和完整输出**。

---

## 13. 两种使用模式与幂等语义

### 13.1 会话式（推荐给长任务）

```text
request_connection → session_id
execute_command    → request_id + command_accepted（立即返回）
get_command_result → 轮询 cursor 拿输出和终态
close_session
```

优点：命令不受单次 MCP 调用超时限制，输出可分批拉取，可以被 `cancel_command` 打断。

### 13.2 一次性

```text
run_command → 内部完成申请、执行、等待、关闭，返回聚合 stdout/stderr
```

优点：一次调用得到结果；缺点：整条链路受客户端总预算约束（`wait_for_approval + timeout + 30s`）。

### 13.3 MCP 的幂等提示 ≠ 幂等保证

- `idempotentHint` 只描述“这个工具重复调用是否通常安全”，供 Host 决定是否允许自动重试；
- `execute_command` 标记为 `destructive=true` 且**没有** `idempotent` 注解，因为命令正文可能删文件、重启服务；
- 真正的去重发生在服务端：`request_id` 相同则不重复执行；
- **断线不自动重放**：连接断开后任务/传输转 `result_unknown`，是否重试由人或上层显式决定；
- `close_session` 标记 `idempotent=true`，并且客户端对 `SESSION_NOT_FOUND` 做了收敛处理（返回“已关闭”，因为平台重启本身就会丢会话）。

---

## 14. MCP backend 选择与宿主配置

`cmd/yiuk-mcp/main.go` 的选择顺序：

```go
platform, platformErr := platformclient.NewFromUserConfig()
if platformErr == nil {
    backend = platform                        // 远程平台
} else if local, err := localbridge.NewFromUserConfig(); err == nil {
    backend = local                           // 本机 Qt bridge
} else {
    slog.Warn("MCP backend is not configured; tools will return NOT_CONFIGURED", ...)
}
```

配置文件位置（Windows，`internal/userconfig/path.go`）：

```text
%LOCALAPPDATA%\Yiuk\DeviceAssist\platform-client.json    平台 backend
%LOCALAPPDATA%\Yiuk\DeviceAssist\local-api.json          本机 bridge
```

`platform-client.json` 字段：

| 字段 | 说明 |
| --- | --- |
| `address` | `host:port`，可带 `tls://` 前缀 |
| `player_password` | 平台 player 密码（≥8 字符） |
| `tls_ca` | 自建 CA 的 PEM 路径 |
| `pinned_cert_sha256` | 服务端证书 DER 的小写 hex SHA-256，设置后只接受该证书 |
| `insecure_skip_verify` | 仅测试；与 pin 互斥使用 |
| `allow_plaintext` | 仅本机测试 |
| `timeout_seconds` | 单次调用超时，默认 30 |
| `heartbeat_interval_seconds` | 空闲 ping 间隔，默认 15，下限 1 |

宿主侧配置（由 Qt 设置页「复制 Agent 配置」生成）：

```json
{
  "mcpServers": {
    "agentremote": {
      "command": "C:/path/to/AgentRemote/yiuk-mcp.exe",
      "args": []
    }
  }
}
```

---

## 15. 操作者客户端（Go platformclient）

`internal/platformclient` 是 operator 侧长连接实现，也是 MCP 的默认 backend。

### 15.1 懒连接与串行化

```go
type Client struct {
    callMu sync.Mutex   // 串行化工具调用与连接建立
    writeMu sync.Mutex  // 防止心跳与工具调用写出交错字节
    conn      net.Conn
    inbox     chan protocol.Message   // 容量 256
    connDone  chan struct{}
    pongs     atomic.Uint64
    sessions  map[string]protocol.SessionInfo
    tasks     map[string]*taskCache
    downloads map[string]*downloadCache
}
```

**一个 Client 同时只允许一笔工具调用在飞**（`callMu`）。文件传输的“边发请求边推分块”也必须在这个互斥里完成，否则分块会和别的调用的帧交错。

`ensureConnectedLocked` 在第一次调用时才真正建连（懒连接），断线后下一次调用自动重连。

### 15.2 握手与 TLS

```go
conn = dial()                      // tls.DialWithDialer 或明文
写 authenticate_player
读 authenticated → 保存 connection_id
```

证书策略三选一：

- `tls_ca`：标准 CA 校验；
- `pinned_cert_sha256`：`VerifyPeerCertificate` 里比对 DER 的 SHA-256，只接受这一张证书（自签部署常用）；
- `insecure_skip_verify`：仅测试。

另外 `dialTLSWithFallback` 先试 `tcp4` 再试 `tcp`，规避部分 TUN/VPN 只有 IPv4 路由的情况。

### 15.3 心跳与断线

```go
func (c *Client) heartbeatLoop(conn net.Conn, done chan struct{}) {
    // 每 heartbeatInterval 发一个 ping；连续 maxMissedHeartbeats(3) 个周期没 pong 就 markDisconnected
}
```

`pong` 帧在 `readLoop` 里被单独消费（只 `pongs.Add(1)`），不会进入 `inbox` 干扰工具调用。断线后**没有任何自动重放**：下次工具调用会重连，但上一次的命令/传输已经由平台记为 `result_unknown`。

### 15.4 输出缓存

`observe(m)` 把异步事件写进本地缓存：

| 消息 | 缓存 |
| --- | --- |
| `session_created` / `session_status` / `session_request` | `sessions[sessionID]` |
| `command_output` | `tasks[requestID].chunks`（按 `sequence` 去重） |
| `command_result` | `tasks[requestID]` 终态 |
| `file_chunk` | `downloads[requestID].chunks`（Base64 解码后） |
| `file_download_result` | `downloads[requestID]` 终态 |
| `device_list` | `devices` |

`get_command_result` 优先用缓存（`cachedResult`），必要时再向平台拉取。

### 15.5 文件上传（操作者 → 设备）

`tools_upload.go`：

```text
1. local_path 必须绝对路径；必须是普通文件；≤ 32 MiB
2. 计算 SHA-256，Seek(0) 回到开头
3. upload_file 元数据：file_name / file_size / file_sha256 / overwrite
4. 64 KiB 分块，Base64，sequence 从 1 递增
5. 「预读一块」保证最后一块带 eof（文件大小是块大小整数倍时也能正确结束）
6. 空文件不发分块，由元数据生成
7. 收到 file_upload_result 后校验：status=succeeded、result_known、大小一致、SHA-256 一致
```

第 5 步的写法值得注意：

```go
pending, _ := readUploadChunk(file)
for {
    next, nextErr := readUploadChunk(file)
    eof := nextErr != nil
    write(frame{pending, sequence, eof})
    if eof { break }
    pending = next
}
```

### 15.6 文件下载（设备 → 操作者）

`tools_file.go`：

```text
1. destination_path 给定时必须绝对路径，且 Lstat 必须 ErrNotExist（拒绝覆盖）
2. 未给定时写到默认下载目录，文件名 = 纳秒时间戳 + 清洗后的远端文件名
3. 发 download_file，等 file_download_result
4. 逐块写入同目录临时文件 .yiuk-download-*
5. tmp.Sync() → Close → os.Rename(临时文件, 目标)
6. 用写入过程算的 SHA-256 与设备回的摘要比对，不一致就删除并报 FILE_INTEGRITY_FAILED
```

文件名的清洗在 `safeRemoteBase`：去掉路径分隔符、`<>:"/\|?*`、控制字符、首尾点，长度截断到 160，空则 `download.bin`。

传输超时下限 5 分钟（`fileTransferTimeout`）。

---

## 16. 设备客户端（Qt，非界面部分）

设备侧负责“批准、执行、落盘”，全部逻辑在 `qt-client/src`，本节只讲与平台协议和本地资源相关的部分。

### 16.1 PlatformClient：协议客户端与连接状态机

`platformclient.{h,cpp}` 实现与 Go 平台完全对称的客户端：

```text
disconnected → connecting → authenticating → registering → connected
                                     ↘(重连退避)→ connecting
```

要点：

- **消息格式与 Go 一致**：每条 WebSocket 二进制消息直接包含 JSON，单条上限 256 KiB，`kProtocolVersion = 1`；
- **心跳**：`kHeartbeatIntervalMs = 15000`，定时发 `device_heartbeat`；
- **重连**：指数退避 `min(30s, 1s << attempts)`；
- **代理回退**：先走系统代理（适配 VPN），TLS 握手失败再直连一次；
- **TLS**：`forceTls12` 可把连接钉在 TLS 1.2（个别 Windows Schannel 组合对 TLS 1.3 不友好）；`pinnedFingerprint` 设置后按证书 SHA-256 校验，失败发 `certificateUntrusted` 信号要求人工信任；
- **凭据轮换**：首次注册成功后自动轮换临时密码并通过 `update_temporary_password` 上报新版本；轮换会让该设备的活动会话失效，这是有意的安全行为。

`handleMessage` 只是把帧翻译成信号，业务判断在 `platformevents.cpp`：

```text
authenticated        → 发 register_device
device_registered    → 记录 password_version，必要时轮换密码，启动心跳
session_request      → sessionRequested（交给审批流程）
session_status       → sessionUpdated（终态时清理本地任务）
execute_command      → commandRequested（交给 PowerShellRunner）
download_file        → fileDownloadRequested（开文件、算摘要、推分块）
upload_file          → fileUploadRequested（建临时文件）
file_upload_chunk    → fileUploadChunkReceived（校验序号/大小/摘要，落盘）
cancel_command       → cancelRequested（终止本地进程）
close_session        → closeRequested（终止任务、清理传输）
```

**防御性重复校验**：设备端在执行任何操作前，会重新检查本地缓存的会话状态和权限（`sessionAllowsFileRead` / `sessionAllowsFileWrite` 要求 `status ∈ {approved, active}` 且权限集包含对应项）。平台已经校验过，设备端再校验一次，属于纵深防御。

### 16.2 PowerShellRunner：唯一的命令执行实现

`powershellrunner.{h,cpp}` 同时服务平台链路和本机 bridge，保证“命令执行只有一份实现”。

```text
1. 命令写入临时 .ps1（UTF-8 BOM + 显式 OutputEncoding）
2. 解释器：pwsh.exe 优先，powershell.exe 回退
3. 参数：-NoLogo -NoProfile -NonInteractive -ExecutionPolicy Bypass -File <script>
4. QProcess::SeparateChannels，stdout/stderr 分别异步读
5. 每流上限 1 MiB，超出截断并置 truncated，继续报终态
6. 超时定时器 → finish("timed_out")；cancel() → finish("cancelled")
7. finish 只执行一次：先 disconnect 进程再 kill，避免回调重入
8. 清理：kill + waitForFinished(1000) + 删除临时脚本
```

为什么带 BOM：Windows PowerShell 5.1 不带 BOM 时按 ANSI 解码 `.ps1`，非 ASCII 命令会被破坏。为什么不用 `cmd.exe /c`：避免二次解析带来的引号和转义问题。为什么不提权：命令以当前登录用户权限运行，**普通权限不是沙箱**。

### 16.3 接收上传：临时文件 + 校验 + 原子改名

`platformevents.cpp` 的 `onPlatformFileUploadRequested` / `onPlatformFileUploadChunk`：

```text
1. 会话必须有 write_file 权限，否则回 failed
2. file_size ≤ 32 MiB，sha256 长度 ≤ 128
3. 目标路径解析：
   - remote_path 为空 → <配置目录>/uploads/<时间戳>-<文件名>
   - remote_path 是目录 → 目录 + 清洗后的文件名
   - 否则必须是绝对路径
4. 目标已存在且 overwrite=false → 回 failed（默认拒绝覆盖）
5. 在同目录创建临时文件 <target>.yiuk-upload-<requestId8>.part
6. 每块：序号必须连续 → Base64 解码 → 累计大小不得超过声明值 → 写入 → 更新 hash
7. eof：flush/close → 比较 received == expected → 比较 SHA-256
8. overwrite=true 时先删除目标，再 QFile::rename(temp, target)
9. 回 file_upload_result：status / remote_path / file_size / file_sha256
10. 任何失败路径都删除临时文件；会话结束也会 abort 并清理
```

注意第 5 步：临时文件和目标**同目录**，rename 才可能保持原子性。

### 16.4 发送下载：分块推送

`pumpFileDownload`：

```text
1. 每次泵动前重新校验 read_file 权限，会话失效立即 cancelled
2. 64 KiB 一块，边读边更新 SHA-256
3. 累计超过 32 MiB 立即 failed
4. eof 时校验已发送字节数 == 文件大小，再发 file_download_result
5. 用 QTimer::singleShot(0, ...) 异步续泵，避免长时间占用事件循环
```

### 16.5 凭据存储：DPAPI

`securestore.cpp`：

```text
protect(plain)   → "dpapi:" + Base64(CryptProtectData(utf8))
unprotect(x)     → 以 "dpapi:" 前缀走 CryptUnprotectData
                 → "plain:" 前缀为无 DPAPI 环境的降级
                 → 无前缀按明文处理（向后兼容）
```

DPAPI 的密钥绑定当前 Windows 用户：把配置文件拷到另一台机器/另一个用户下**无法解密**，这是设计意图。

### 16.6 本机 bridge（LocalBridge）

当操作者和设备是同一台机器、且不想走平台时，Qt 客户端内起一个只监听回环的 HTTP 服务：

```text
监听：127.0.0.1:<随机端口>
鉴权：Authorization: Bearer <64 hex token>（进程启动时用 CSPRNG 生成）
接口：POST /tool/<toolName>，body 为工具 input JSON
限制：请求头 64 KiB、body 32 KiB、5 秒无响应断开
响应：{"ok":bool,"code":...,"message":...,"data":...}
```

token 和 endpoint 写到 `%LOCALAPPDATA%\Yiuk\DeviceAssist\local-api.json`，Go 侧 `localbridge.NewFromUserConfig` 读它并强校验 `http://127.0.0.1:...` 前缀与 token 长度。

bridge **只支持本机设备**，不会伪造远端结果；远端能力必须通过平台 backend。

---

## 17. 消息方向速查

| 消息 | 方向 | 是否直接响应 |
| --- | --- | --- |
| `authenticate_player` / `authenticated` | C→S / S→C | 是（回显 correlation_id） |
| `register_device` / `device_registered` | 设备→S / S→设备 | 是 |
| `device_heartbeat` / `heartbeat_ack` | 设备→S / S→设备 | 否 |
| `update_temporary_password` / `temporary_password_updated` | 设备→S / S→设备 | 是 |
| `request_session` / `session_created` | 操作者→S / S→操作者 | 是 |
| `session_request` | S→设备 | 否（异步通知，需批准） |
| `approve_session` / `reject_session` | 设备→S | 否（结果走 `session_status`） |
| `session_status` | S→双方 | 否 |
| `execute_command` / `command_accepted` | 操作者→S / S→操作者 | 是 |
| `execute_command` | S→设备 | 否（异步执行） |
| `command_output` | 设备→S→操作者 | 否 |
| `command_result` | 设备→S→操作者 | 否（异步终态推送） |
| `get_command_result` / `command_result` | 操作者→S / S→操作者 | 是 |
| `cancel_command` / `command_cancelled` | 操作者→S / S→操作者 | 是（同时下发设备） |
| `download_file` | 操作者→S→设备 | 否（立即回报只表示已下发；终态走 `file_download_result`） |
| `file_chunk` | 设备→S→操作者 | 否 |
| `file_download_result` | 设备→S→操作者 | 否（但作为 `download_file` 的**延迟响应**携带原 correlation_id，`callStream` 以此匹配） |
| `upload_file` | 操作者→S→设备 | 否 |
| `file_upload_chunk` | 操作者→S→设备 | 否 |
| `file_upload_result` | 设备→S→操作者 | 否（同上，作为 `upload_file` 的延迟响应携带原 correlation_id） |
| `list_devices` / `device_list` | C→S / S→C | 是 |
| `ping` / `pong` | C→S / S→C | 是 |
| `error` | S→C | 回显 correlation_id；`fatal` 时先写后断 |

---

## 18. 测试与联调

```sh
go test ./...                                        # 单元 + 集成
go test -race ./internal/platform/... ./internal/platformclient/...
go build ./cmd/platform && go build ./cmd/yiuk-mcp
```

命令行链路（需先起平台）：

```sh
go run ./cmd/smoke -addr host:29564 -password '<player>' -ca server.crt -mode approval
go run ./cmd/device-sim -addr host:29564 -password '<player>' \
  -device-id 111222333 -temporary-password '<32+ chars>' -ca server.crt -mode approval
```

- `cmd/smoke`：认证、注册、申请、批准、执行、取结果、关闭，以及跨连接拒绝、错误密码拒绝、已关闭会话拒绝；
- `cmd/device-sim`：自动批准并返回固定结果，不执行真实命令，便于验证 MCP 链路；
- `qt-client/tests/platform_interop.cpp`：两个 `PlatformClient` 分别充当设备与操作者，验证 Qt↔Go 互操作。

Go 单测重点覆盖：帧边界、鉴权限流、临时密码版本/过期/撤销、会话竞态与绑定、任务幂等与终态不可变、输出截断、游标分页、传输序号与上限。

---

## 19. 已知缺口

| 项 | 说明 |
| --- | --- |
| 进程树清理 | Qt 目前用 `QProcess::kill()`，未使用 Windows Job Object 清理整棵子进程树；子进程可能残留。 |
| 真机验收 | TLS 证书、安装包、非 ASCII 输出、长输出截断、取消边界仍需双机验证。 |
| 客户端单飞限制 | Go operator 客户端一次只允许一笔调用在飞；长任务应改用会话式 `execute_command` + `get_command_result`，否则会阻塞其他工具调用。 |
| 命名遗留 | Go module 与 MCP 名已改为 `agentremote`；`YIUK_*` 环境变量、`yiuk-platform` 二进制/服务/数据目录仍保留旧名，改动需配套迁移。 |
| 无持久化 | 平台完全内存化，重启即丢失所有在线状态、临时密码、会话与任务；不做“重启后恢复”。 |

---

## 20. 关键代码索引

| 主题 | 文件 |
| --- | --- |
| accept / 连接注册 / 断线清理 | `internal/platform/server/server.go` |
| 读写循环 / deadline / writeMu | `internal/platform/server/conn.go` |
| 发送队列 / 慢客户端 / sendOK | `internal/platform/server/routing.go` |
| 消息分发 | `internal/platform/server/handler.go` |
| 会话请求 / 批准 / 关闭 | `internal/platform/server/handler_session.go` |
| 命令执行与结果 | `internal/platform/server/handler_command.go` |
| 下载与上传转发 | `internal/platform/server/handler_download.go`、`handler_upload.go` |
| 超时收敛 | `internal/platform/server/janitor.go` |
| 稳定错误码 | `internal/platform/server/errors.go` |
| 帧编解码与消息结构 | `internal/platform/protocol/protocol.go` |
| 设备与临时密码 | `internal/platform/device/manager.go` |
| 会话状态机 | `internal/platform/session/manager.go` |
| 任务与输出预算 | `internal/platform/task/manager.go` |
| 传输分块与上限 | `internal/platform/transfer/manager.go` |
| TLS 监听 | `internal/platform/transport/listener.go` |
| 工具契约与校验 | `internal/agenttools/tools.go` |
| MCP 适配 | `internal/mcpadapter/server.go`、`cmd/yiuk-mcp/main.go` |
| operator 客户端 | `internal/platformclient/{client,transport,config,cache}.go`、`tools_*.go` |
| 本机 bridge 客户端 | `internal/localbridge/client.go` |
| 设备协议客户端 | `qt-client/src/platformclient.{h,cpp}` |
| 平台事件处理（非界面） | `qt-client/src/platformevents.cpp` |
| 命令执行 | `qt-client/src/powershellrunner.{h,cpp}` |
| 凭据保护 | `qt-client/src/securestore.cpp` |
| 回环 bridge 服务 | `qt-client/src/localbridge.{h,cpp}` |
