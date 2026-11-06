# 关键技术架构：决策、不变式与边界

状态：与当前代码一致（`agentremote` module，Go 1.27）。

本文只讲**为什么这样设计**：每个关键决策解决了什么问题、被什么约束逼出来的、代价是什么。
逐帧时序与代码级细节见 [运行时架构](runtime-architecture.md)；一次真实调用的全流程见 [系统串讲](system-walkthrough.md)；Go 语言层面的入门说明见 [Go 后端技术说明](go-backend-guide.md)。

---

## 1. 一句话与一张图

> 设备**主动出站**连到 Go 平台；Agent 在**设备所有者批准过的会话**里执行 PowerShell 或传文件。
> 平台的职责不是"执行命令"，而是**守住授权边界**，并在失败时**不谎报结果**。

```text
操作者 Windows / 服务器                     设备所有者 Windows
+--------------------------+                +--------------------------+
| pi Agent (MCP)           |                | Yiuk Qt Client           |
|   MCP Adapter (stdio)    |                |   所有者审批 / 会话管理  |
|   Tool Core              |                |   PowerShell 执行        |
|   platformclient (Go)    |                |   文件收发 / 活动面板    |
+------------+-------------+                +------------+-------------+
             |  conn#A operator                          |  conn#B device
             |  TLS 1.2+                                 |  TLS 1.2+
             +------------------+            +------------+
                                v            v
                         +-----------------------+
                         |   Go 平台（中继）     |
                         |   认证 / 设备 / 会话  |
                         |   任务 / 传输 / 路由  |
                         +-----------------------+
```

三条 WS/WSS 长连接全部由客户端**主动出站**；平台**从不 dial 任何设备**。

---

## 2. 关键决策

每条格式：**问题 → 选择 → 理由 → 代价**。

### D1　中继，不做直连、不做反向代理

- **问题**：设备在 NAT/防火墙后面，平台无法主动连它。
- **选择**：所有连接都是客户端出站到平台同一监听端口；"把命令发给设备"在实现上等于"把帧塞进那条已存在的设备连接的发送队列"。
- **理由**：只有出站才不需要 port forwarding、不需要公网 IP、不需要用户改路由器；平台也不需要保存任何"设备可达地址"。
- **代价**：所有流量在平台 hairpin，带宽与内存成本都在中心节点。因此必须配一套有界资源（见 D7），不能无限转发。

### D2　一连接两 goroutine：同步读 + 单写者 + 有界队列

- **问题**：多 goroutine 直接写同一 socket 会字节交错；读侧阻塞会让慢对端拖垮整台服务端。
- **选择**（`internal/platform/server/server.go`、`conn.go`、`routing.go`）：
  - 每条连接固定两个 goroutine：`readAndDispatchMessages`（读 + **同步**分发）与 `writeQueuedMessages`（唯一写者）；
  - `outgoingMessages` 容量 `observer_slow_client_buf`（默认 64）；
  - 队列满**不阻塞调用者、不涨内存**，直接判定对端处理不过来并断开。
- **理由**：读侧同步分发保证**同连接消息严格有序** —— 这是文件分块 `sequence` 连续校验、任务状态单向推进能成立的前提；单写者让帧不会交错。
- **代价**：`writeMu` 仍然必须存在，因为**致命错误路径会绕过队列同步写**（先让对端看到错误码再断开），此时两个 goroutine 并发写同一 socket。另外"慢"等于"被踢"，所以必须靠幂等 + `result_unknown` 兜底（D5、D6）。

### D3　三层身份，且“设备码不是凭据”

| 层 | 凭据 | 授予什么 |
| --- | --- | --- |
| 平台 | `player_password`（`crypto/subtle` 常量时间比较 + 按 IP 限流） | 只是“可以使用平台中继资源”，**不授予任何设备权限，因为我只有一台服务器，不能滥用，我没那么多钱😅** |
| 设备 | 32 字符 base32 临时密码（160 bit，salt + SHA-256，版本轮换 + TTL + 撤销） | 拥有这个设备码 |
| 会话 | `execute_command` / `read_file` / `write_file` **互不继承** | 一次授权、绑设备 + 操作者 + 到期时间 |

- **选择**：`device_id` 与临时密码分工明确：
  - `device_id`（9 位十进制）是**公开标识 + 查询主键**：决定“查哪条设备记录”、把帧路由到哪条连接（`deviceID → connID`）；
  - `temporary_password` 是**所有权凭据**：决定“这条记录归不归你”。
  - 身份是 `(device_id, password)` 这一**组合**，单靠任何一个都不成立。
- **判据**（`device.Manager.Register`）：以请求里的 `device_id` 查 `devices` / `tombstones`，再用密码哈希比对——
  - 查到且密码匹配 → 同一个身份，刷新条目；
  - 查到但密码不匹配 → **分配新码**，原条目不动（不报错、不抢占）；
  - 墓碑里存在且密码匹配 → 收回原码（reclaim），版本 +1；
  - 两边都没有 → 采纳客户端提议的码（adopt）。
  - 且**密码校验先于在线冲突判定**，所以收到 `DEVICE_ALREADY_ONLINE` 一定意味着密码也匹配。
- **理由**：设备码会出现在屏幕、截图、聊天记录里，而且 `list_devices` 会把全量设备码返回给任何已认证的连接——它**本来就不保密**，所以不能当秘密；反过来，“密码不匹配就抢走别人的码”会让远程会话串台到另一台机器，所以撞码只能另发新码。
- **“码不是凭证”不等于“码不参与授权”**：`request_session` 不带密码也能发起，转 `pending_approval` 由所有者批准。码是**发起授权请求的地址**，授权来源只有两个：密码（`direct`）或所有者手动批准（`approval`）。坦白说，这种模式参考了todesk、uu远程这类远控软件，毕竟本项目也属于远控.
- **接受的残余风险**：任何持 `player_password` 的人知道某台设备的码就能向它弹审批框。靠：电脑owner是否批准 + `purpose` 必填（UI 必须展示）+ `max_sessions_per_device`(8) 限制并发刷框 + `register_rate_per_min`(30) 限制刷设备槽
- **代价**：服务端要维护 `devices` + `tombstones` 两份内存状态，且墓碑需要按 `10 × max_devices` 裁剪

### D4　会话 = 权限集合 + TTL + 单向状态机

`requested → pending_approval → approved → active → closed`，终态还有 `rejected / expired / interrupted`。

- **选择**：会话绑定设备、操作者连接、权限集合、到期时间；每次操作前 `Authorize(sessionID, operatorConnectionID, permission)` 三重校验（归属 / 状态 / 未过期 / 有该权限）。
- **理由**：批准的是"**这一次、这几项**能力"，不是"这个人"。所以下载不继承执行权限、上传不继承读取权限。
- **代价**：Agent 需要显式带 `session_id` + `permissions`，`run_command` 这类一站式工具也要在内部先建会话；不能"一次批准永久可用"。

### D5　稳定错误码 + 请求幂等 + 断线不自动重放

- **选择**：错误统一走 `error` 帧的稳定 `code`；`request_id` 是**全局唯一键**（同 session 重复提交返回原任务，跨 session 复用返回 `REQUEST_ID_CONFLICT`）；
- 网络断线**不会自动重放**。**理由**：Agent 会重试。让重试**安全**（幂等）比让重试**看起来成功**(自动重放）便宜得多；后者在"删文件 / 改注册表"这类命令上会直接造成二次破坏

### D6　`result_unknown`：不确定就是不确定，交给AI自行想办法验证

这是本项目最核心的语义选择。

- **理由**：`failed` 意味着"确定没生效，可以重试"；`result_unknown` 意味着"可能已经生效，不要盲目重试"。二者混同可能让 Agent误重复操作，造成破坏
- **代价**：调用方必须处理第三种状态，工具描述里必须写明"不要盲重试"。

失败收敛规则（`server.go`、`janitor.go`）：

| 触发 | 会话 | 任务 | 设备侧动作 |
| --- | --- | --- | --- |
| 设备断线 | `interrupted` | `result_unknown` | 无 |
| 操作者断线 | `interrupted` | `cancelled`（平台**确定**该终止） | `close_session(OPERATOR_DISCONNECTED)` |
| 会话 TTL 到期 | `expired` | `cancelled` | `close_session(SESSION_EXPIRED)` |
| 命令 deadline 到期 | 不变 | `timed_out` | `cancel_command` |
| 传输 5 分钟未完成 | 不变 | `result_unknown` | 对应 `cancel_*` |
| 转发失败（队列满/连接没了） | 不变 | `result_unknown` | 无 |

设备断线与操作者断线**刻意不同**：设备断开后命令是否执行无法确认；操作者断开则平台知道应当终止，并且必须让设备的 PowerShell 停下来。

### D7　所有资源都有明确上限

单帧、单块、单文件、单任务输出、每连接队列、每会话并发、会话总数、设备总数、连接总数、认证速率都有上限（见第 4 节）。

- **理由**：平台是中心节点 + 全内存态，任何一处无界都是可被单个客户端触发的 OOM 或拒绝服务。
- **代价**：超限时**优先拒绝而不是排队**，所以会出现"合法但过大的请求被拒"，必须在错误码里说清是哪个上限（`FILE_TOO_LARGE` / `TASK_LIMIT` / `TRANSFER_LIMIT` / `OUTPUT_TRUNCATED`）。

### D8　文件传输的正确性是"校验出来的"，不是"传完就算"

- 分块 `sequence` 严格连续，跳号即 `FILE_SEQUENCE_INVALID`；
- 累计字节 ≤ 声明大小；块数 ≤ `MaxChunks`（4096，防止 1 字节块撑爆记账）；
- 设备端**先写临时文件，校验大小与 SHA-256 通过后才原子改名**；默认**拒绝覆盖**已有文件（需显式 `overwrite`）；
- 传输"成功"但字节数与声明不符 → 降级为 `result_unknown`（`internal/platform/transfer/manager.go`）。

- **理由**：跨三次进程边界（操作者 → 平台 → 设备）+ Base64 编码，任何一层都可能截断；只报"传完了"等于把数据损坏的责任推给用户。
- **代价**：多一轮读文件算哈希；大文件要读两遍。

### D9　服务端全内存，且明确不持久化

- **选择**：在线连接、临时密码、会话、任务、传输**全部内存态**；重启即全部失效，**不做"重启后恢复"**。
- **理由**：一旦持久化就必须回答"重启后哪些还活着、状态机断点在哪、旧连接的帧算不算数"——这些是真正的一致性难题。宁可整体失效并让设备自动重连注册，也不要半个系统在中间态里运行。也没什么值得存的数据，甚至连用户系统都没做，现阶段不如直接纯内存，部署轻快
- **代价**：单点、不能水平扩容、重启即服务中断、跨重启无法审计完整链路。要把平台做成多实例，必须先把"设备粘性"（同一 device 必须落到同一节点）解决掉。

---

## 3. 服务端必须保证的不变式

任何改动都不得破坏这些性质；它们比"某个功能能用"更重要。

1. **设备码不是凭据**：码只用于寻址与查询（`deviceID → connID`），所有权由密码判定。只有密码匹配才算拥有设备码；不匹配时一律分配新码，不报错、不抢占。
2. **密码校验先于在线冲突判定**：因此收到 `DEVICE_ALREADY_ONLINE` 就一定意味着密码也匹配。
3. **`revoked` 永不可 reclaim**，且 `revoked` 判定必须在密码校验与分配**之前**，否则撤销会被过期或重启自动解除。
4. **命令只能绑定已批准的会话**：`requested` / `pending_approval` / 终态会话一律不执行。
5. **权限不继承**：三个权限互相独立，任何"顺手复用已有权限"的捷径都是漏洞。
6. **`request_id` 不可跨会话复用**；同一会话内重复提交返回原结果，不重复执行。
7. **断线不自动重放**；不确定就 `result_unknown`。
8. **单连接消息严格有序**（依赖 D2 的同步分发）。
9. **临时密码、命令正文、完整输出不写服务端日志**（`internal/platform/audit` 只记 `RedactID` 摘要）。

---

## 4. 资源上限速查

来源：`internal/platform/config/config.go` 的硬编码默认值。

| 配置 | 默认 | 作用 |
| --- | --- | --- |
| `max_connections` | 1024 | 连接总数 |
| `max_devices` | 1024 | 设备总数 |
| `max_sessions` | 2048 | 会话总数 |
| `max_sessions_per_device` | 8 | 单设备并发会话 |
| `max_tasks_per_session` | 2 | 单会话并发任务（上传/下载也用它） |
| `session_timeout_sec` | 600 | 批准后的会话有效期 |
| `approval_timeout_sec` | 120 | 等待批准时限 |
| `temp_password_ttl_sec` | 600 | 临时密码有效期（可被设备端 TTL 覆盖，60s ~ 24h） |
| `heartbeat_timeout_sec` | 45 | 认证后读 deadline |
| `handshake_timeout_sec` | 10 | 认证前读 deadline |
| `max_output_bytes` | 2 MiB | 单任务 stdout + stderr 总预算 |
| `max_download_bytes` / `max_upload_bytes` | 32 MiB | 单文件上限 |
| `max_frame_bytes` | 256 KiB | 单帧上限 |
| `auth_rate_per_min` | 30 | 每 IP 每分钟认证尝试 |
| `register_rate_per_min` | 30 | 每 IP 每分钟设备注册 |
| `observer_slow_client_buf` | 64 | 每连接发送队列长度 |
| `command_max_len` / `working_dir_max_len` / `identifier_max_len` | 32 KiB / 4 KiB / 128 | 输入边界 |
| `unused_device_release_sec` / `offline_device_keep_sec` | 600 / 24h | 设备槽释放 / 离线保留 |
| `transfer.MaxChunkBytes` / `MaxChunks` | 96 KiB（解码后）/ 4096 | 单块与块数 |
| 客户端上传分块 | 64 KiB（Base64 后约 87 KiB） | 保证在 256 KiB 帧内 |

心跳：设备 15s 一个 `device_heartbeat`；操作者空闲 15s 一个 `ping`，连续 3 个周期没 `pong` 主动断开并在下次调用时重连。**心跳只证明链路活着，不触发任何业务重放。**

---

## 5. 已知薄弱点（尚未修复，按风险排序）

写在文档里而不是留在脑子里。每条都标了位置，便于后续接手。

| # | 风险 | 位置 | 症状 | 方向 |
| --- | --- | --- | --- | --- |
| 1 | `max_sessions` 变成**终身配额** | `internal/platform/session/manager.go`（`Create` 里 `len(m.sessions) >= maxSessions`，且全文件无删除） | 终态会话永不回收；进程跑够 2048 次会话创建后，**所有新会话永久 `SESSION_LIMIT`**，直到重启 | 改成活跃计数，或终态会话延迟回收 |
| 2 | 任务/传输终态记录永不回收 | `internal/platform/task/manager.go`、`transfer/manager.go` | 每条大输出命令最多留 2 MiB 常驻；长时间运行会 OOM | 终态记录 TTL / LRU 回收 |
| 3 | 结果分页没有字节预算 | `task.Manager.ResultFromCursor` 返回 cursor 之后**全部**分块；`max_output_bytes`(2 MiB) ≫ `max_frame_bytes`(256 KiB) | 一条输出超过约 256 KiB 的命令，`get_command_result` 编码失败 → 写循环退出 → **操作者连接被关 → 会话中断** | 按字节切页 + `Encode` 失败降级为错误帧而不是退出写循环 |
| 4 | 会话无滑动续期，`LastActivity` 是死字段 | `session/manager.go`（`Activate`/`Authorize` 只写不读） | `timeout_seconds` 最大 3600 但会话 600s 就被 janitor 杀掉，长命令必然中断 | 有活动即续期，或区分 idle / max lifetime |
| 5 | 上传无流控 | `internal/platformclient/tools_upload.go` 一次性发完所有分块；`internal/platform/server/handler_upload.go` 忽略 `sendToConnection` 返回值 | 32 MiB = 512 帧灌进深度 64 的队列；设备稍慢就被判 `slow_client` 断开；设备重连后分块静默丢弃 | 窗口/ack 流控，队列满时反压操作者而不是断设备 |
| 6 | 设备的 `mode` 字段不参与授权 | `internal/platform/server/handler_session.go`（`handleRequestSession` 只看"有没有带临时密码"） | 所有者设 `approval` 想每次弹窗，但持有密码者直接进 `direct`，弹窗不出现 | 明确这是产品决定还是缺陷；若要生效，`direct` 需设备侧显式开启 |
| 7 | 设备连接可当 operator 发起会话 | `handleRequestSession` 只挡了"连自己" | 被攻破的设备客户端可以连别的设备 | 视产品意图决定是否要求 `role == rolePlayer` |
| 8 | 输出截断后会重复发最后一帧 | `internal/platform/server/handler_command.go`（`handleCommandOutput`） | 靠客户端 `sequence` 去重才没出错；第三方客户端会重复渲染 | 截断后不再下发 data |
| 9 | 操作者客户端单飞 | `internal/platformclient/client.go`（整个流式请求持 `requestMu`） | 并发工具调用互相阻塞到超时；长任务应改用会话式 `execute_command` + `get_command_result` | 改为 per-request pending map |
| 10 | 无 CI | 仓库无 `.github/` | 只能靠本地 `go vet ./...` + `go test ./...` | 补流水线：vet + test + race |

---

## 6. 关联文档

| 文档 | 内容 |
| --- | --- |
| [运行时架构](runtime-architecture.md) | 连接拓扑、读写循环、帧级时序、MCP 工具清单与错误码 |
| [系统串讲](system-walkthrough.md) | 一次真实调用全流程、文件归属地图、服务器侧部署与排错对照 |
| [Go 后端技术说明](go-backend-guide.md) | 面向 C++ 开发者的 Go 说明、幂等、SHA-256、原子落盘 |
| [开发与构建](development.md) | 环境、构建、测试、打包发布、部署（systemd / Docker） |
| [本机开发环境](local-dev.md) | 仅限本机的工具链路径、快捷命令与已知坑 |
| [设备码身份流程（设计稿）](tmp-device-identity-flows.md) | 设备码 / 临时密码 / 墓碑的完整场景推演 |
| [README](../README.md) | 能力清单、安全边界、项目现状 |
