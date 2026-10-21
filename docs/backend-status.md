# 平台后端实施状态

状态：项目处于 MVP 开发中；本文档记录已完成范围、测试和剩余工作。

关联文档：[服务器改造计划](server-refactor-plan.md)、[实现方案](implementation.md)、[开发与构建](development.md)。

## 已完成

### P0：凭证和状态语义

- `player_password` 只用于证明连接方可使用平台中转，使用常量时间比较和按 IP 限流。
- 设备临时密码由设备生成（也可由服务端生成后通过 TLS 返回），服务端只保存随机盐 + SHA-256 校验值。
- 临时密码支持版本、签发时间、过期时间、撤销和 `direct` / `approval` 两种模式。
- 冻结了 session 终态（`closed`、`rejected`、`expired`、`interrupted`）和 task 终态（`succeeded`、`failed`、`timed_out`、`cancelled`、`result_unknown`）。
- 服务端重启后所有状态丢失，不伪造成功。

### P1：内存平台核心

代码位置：

```text
internal/platform/device/   设备、临时密码、在线状态
internal/platform/session/  session 状态机与授权绑定
internal/platform/task/     任务、幂等 request ID、有界输出
internal/platform/auth/     player 密码认证
internal/platform/audit/    最小化审计与 ID 脱敏
```

覆盖：

- 设备注册、重新注册、在线连接所有权、心跳、临时密码轮换/撤销。
- direct 模式直接进入 `approved`；approval 模式只能进入 `pending_approval`。
- 只有目标设备连接可以批准/拒绝；只有绑定的 operator 连接可以执行命令。
- 临时密码刷新会撤销该设备的活动 session。
- 重复 `request_id` 返回已有任务，不重复执行；跨 session 复用 request ID 被拒绝。
- stdout/stderr 总字节数受 `max_output_bytes` 限制，超限标记 `truncated=true`。
- 服务端为每个任务保存 deadline；设备无响应时 janitor 将任务标记为 `timed_out` 并向设备下发取消，避免长期占用任务槽位。
- 会话过期、设备断线、operator 断线和所有者关闭会话都会收敛到明确的终态。
- 服务端只记录脱敏 ID、状态和错误码，不记录密码、命令正文或完整输出。

### P2：TLS 中转和双端连接

代码位置：

```text
internal/platform/protocol/   版本化长度前缀帧 + JSON envelope
internal/platform/transport/  TLS listener（可选仅测试用的明文回退）
internal/platform/server/     accept、读写 deadline、心跳、路由、背压
cmd/platform/                 服务入口
```

协议最小集合已实现：

```text
authenticate_player / authenticated
register_device / device_registered
device_heartbeat / heartbeat_ack
update_temporary_password / temporary_password_updated
request_session / session_request / session_created / session_status
approve_session / reject_session
open_session_channel / close_session
execute_command / command_accepted / command_output / command_result
get_command_result / cancel_command / command_cancelled
list_devices / device_list
ping / pong / error
```

传输约束：

- 4 字节大端长度前缀，帧上限 `max_frame_bytes`。
- TLS 最低 1.2；未配置证书且未显式 `allow_insecure` 时拒绝启动。
- 握手 deadline、连接级心跳 deadline、写 deadline。
- 每连接有界发送队列；慢客户端会被断开，避免无界缓存。
- 连接、设备、session、task 都有上限。
- 所有直接响应回显 `correlation_id`；异步事件不回显，便于客户端关联请求。
- 平台 listener 只接受 `agent_assist` 业务；Minecraft 仍由独立 relay 进程处理。

### Agent operator backend

`internal/platformclient` 实现 `agenttools.Backend`：

- 保持一条已认证 operator 长连接，避免 tool 调用之间 session 被断开。
- 支持 8 个统一工具。
- 本地缓存流式 `command_output`，支持 `get_command_result` 游标分页。
- `request_connection` 支持可选 `temporary_password` 字段（平台 backend 必填）。
- 平台返回的结构化错误码直接映射为 `agenttools.Error`。
- `cmd/yiuk-mcp` 优先使用 `%APPDATA%/Yiuk/DeviceAssist/platform-client.json`，否则回退本机 Qt bridge。

### Minecraft 边界

- 新增 `cmd/relay` 入口运行既有 relay，平台服务不读取 Minecraft 配置。
- 平台协议使用独立业务类型和独立 listener；远程协助消息不能进入 Minecraft room。
- 既有 `internal/room`、`internal/control`、`internal/protocol` 未改语义。

## 测试

```sh
go test ./...
go test -race ./internal/platform/... ./internal/platformclient/...
```

覆盖内容：

- 设备临时密码正确/错误/过期/撤销/版本回退/设备上限/在线所有权。
- session direct/approval、批准/拒绝、过期、撤销、operator/device 绑定、并发上限。
- task 幂等、并发上限、输出截断、游标分页、终态不可变。
- 协议帧往返、超长帧、空帧、版本拒绝。
- 平台集成：未认证拒绝、错误 player 密码、approval 未批准不能执行、批准后执行、跨 operator 拒绝、临时密码轮换撤销 session、输出截断、session 关闭。
- Agent backend 集成：list/get status/request/execute/result/close 全链路。

## Qt 客户端对接（P3/P4 设备侧）

代码位置：

```text
qt-client/src/platformclient.{h,cpp}  Qt 平台协议客户端
qt-client/src/securestore.{h,cpp}     Windows DPAPI 凭证保护
qt-client/tests/platform_interop.cpp  Qt↔Go 互操作测试
```

已完成：

- 设置页新增平台服务配置：服务器地址、player 密码、TLS、自签名证书开发选项、`direct` / `approval` 授权模式、连接/断开。
- player 密码使用 Windows DPAPI 保护后写入 `%APPDATA%/Yiuk/DeviceAssist/platform.json`；非 Windows 构建显式使用明文标记，仅用于开发。
- 设备临时密码改为本地生成 32 字符 base32 密码；刷新密码会提升版本并通过 `update_temporary_password` 同步到平台，旧会话由服务端立即撤销。
- Qt 通过 `QSslSocket` 连接平台，完成 `authenticate_player`、`register_device`、心跳和自动重连（指数退避）。
- 平台 `session_request` 会进入现有审批 UI，批准/拒绝通过 `approve_session` / `reject_session` 回传。
- 平台 `execute_command` 会创建有界 QProcess 任务，按 `command_output` 流式回传 stdout/stderr，并发送 `command_result`（退出码、截断、结果未知）。
- 平台 `cancel_command` / `close_session` 会终止对应 QProcess；超时会回传 `timed_out`。
- operator 侧支持通过 UI 申请会话：输入 32 位临时密码和用途后发送 `request_session`。

Qt↔Go 互操作测试已通过：两个 Qt `PlatformClient` 分别作为设备和操作者，完成认证、注册、请求、批准、`execute_command`、输出/结果回传和关闭会话。

## 尚未完成

- **P4 进程树清理**：Qt 目前使用 `QProcess::kill()`，尚未用 Windows Job Object 清理整棵子进程树；非 ASCII/代码页输出和取消边界仍需在真实 Windows 上验证。
- **P3 真实 Windows 验证**：Qt 代码已在本地编译并通过 Go 平台互操作，但未在两台真实 Windows 设备上做 TLS 证书、安装包和 UI 端到端验收。
- **P5 Minecraft 回归**：Minecraft relay 已暂停，需要按冻结清单在两台真实客户端上做开房、加入、双向游戏流和房主恢复回归。
- **P6 发布前安全检查**：真实 TLS 证书、日志脱敏复核、慢客户端/资源泄漏压测和 Windows 安装包验收。

## 运行

```sh
go build -o platform ./cmd/platform
./platform -config configs/platform.yaml
```

`configs/platform.yaml` 中必须替换 `player_password` 并提供 `tls_cert` / `tls_key`。仅本地测试可设置 `allow_insecure: true`。

Agent 侧配置示例：

```json
{
  "address": "switchboard.example.com:25664",
  "player_password": "REPLACE_WITH_THE_PLATFORM_PLAYER_PASSWORD",
  "tls_ca": "C:/certs/platform-ca.pem",
  "timeout_seconds": 30
}
```