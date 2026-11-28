# 关键技术约束

改代码前只需确认三件事：**哪些不变式不能破坏**、**哪些上限是硬的**、**哪些已知问题还没修**。

## 1. 一句话与一张图

> 设备**主动出站**连到 Go 平台；Agent 在**设备所有者批准过的会话**里执行 PowerShell 或传文件。

```text
操作者 Windows / 服务器                     设备所有者 Windows
+--------------------------+                +--------------------------+
| pi Agent (MCP)           |                | Yiuk Qt Client           |
|   MCP Adapter (stdio)    |                |   所有者审批 / 会话管理  |
|   Tool Core              |                |   PowerShell 执行        |
|   platformclient (Go)    |                |   文件收发 / 活动面板    |
+------------+-------------+                +------------+-------------+
             | operator (WSS)                            | device (WSS)
             +------------------+          +-------------+
                                v          v
                         +-----------------------+
                         |   Go 平台（中继）     |
                         |   认证 / 设备 / 会话  |
                         |   任务 / 传输 / 路由  |
                         +-----------------------+
```

三条长连接全部由客户端主动出站，平台从不 dial 设备。平台拆成 `core`（业务）与 `gateway`（socket）两个进程，彼此只传原始帧。

## 2. 必须保持的不变式

1. **设备码不是凭据**：码只用于寻址与查询（`deviceID → connID`），所有权由临时密码判定；密码不匹配时一律分配新码，不报错、不抢占。
2. **密码校验先于在线冲突判定**：因此收到 `DEVICE_ALREADY_ONLINE` 就一定意味着密码也匹配。
3. **`revoked` 永不可 reclaim**，且 `revoked` 判定必须在密码校验与分配**之前**。
4. **命令只能绑定已批准的会话**：`requested` / `pending_approval` / 终态会话一律不执行。
5. **批准即全权**：会话创建后即拥有执行 / 读 / 写三项能力；新增的“会话级断言”必须是**收紧**，不能依赖调用方声明。
6. **`request_id` 不可跨会话复用**；同一会话内重复提交返回原结果，不重复执行。幂等只在终态记录保留期内有效。
7. **断线不自动重放**；不确定就 `result_unknown`（与 `failed` 必须区分：前者可能已生效）。
8. **单连接消息严格有序**（gateway 对同一连接串行调用 core）。
9. **临时密码、命令正文、完整输出不写服务端日志**。
10. **连接表只有一份，在 gateway**。core 需要时调 `Alive`，投递失败就当设备不可用。
11. **路由不构成授权**。Redis 里的 `route:*` 只解决寻址；查不到就退回配置里的网关地址，且不得因此跳过任何授权检查或谎报成功。
12. **路由清理必须比较后删除**：值仍属于自己才删，否则旧连接的迟到清理会删掉新连接的路由。

**授权的唯一规则**：带对临时密码 = 直接批准；不带 = 转审批，由所有者在客户端点批准。没有第二个开关。

## 3. 资源上限

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
| `temp_password_ttl_sec` | 600 | 临时密码有效期（设备端可调 60s ~ 24h） |
| `heartbeat_timeout_sec` / `handshake_timeout_sec` | 45 / 10 | 认证后 / 认证前读 deadline |
| `max_output_bytes` | 2 MiB | 单任务 stdout + stderr 总预算 |
| `max_download_bytes` / `max_upload_bytes` | 32 MiB | 单文件上限 |
| `max_frame_bytes` | 256 KiB | 单帧上限 |
| `auth_rate_per_min` / `register_rate_per_min` | 30 | 每 IP 每分钟认证 / 注册尝试 |
| `observer_slow_client_buf` | 64 | 每连接发送队列长度 |
| `command_max_len` / `working_directory_max_len` / `identifier_max_len` | 32 KiB / 4 KiB / 128 | 输入边界 |
| `unused_device_release_sec` / `offline_device_keep_sec` | 600 / 24h | 设备槽释放 / 离线保留 |
| `terminal_record_retain_sec` / `terminal_output_retain_bytes` | 900 / 64 MiB | 终态记录与输出保留 |
| `transfer.MaxChunkBytes` / `MaxChunks` | 96 KiB（解码后）/ 4096 | 单块与块数 |

超限时**优先拒绝而不是排队**，错误码点名是哪个上限（`FILE_TOO_LARGE` / `TASK_LIMIT` / `TRANSFER_LIMIT` / `OUTPUT_TRUNCATED`）。

心跳：设备 15s 一个 `device_heartbeat`；操作者空闲 15s 一个 `ping`，连续 3 个周期没 `pong` 主动断开。心跳只证明链路活着，不触发任何业务重放。

## 4. 已知薄弱点（未修）

代码里对应位置有 `// TODO(薄弱点 #N)`。

| # | 风险 | 位置 | 症状 |
| --- | --- | --- | --- |
| 3 | 结果分页没有字节预算 | `task.Manager.ResultFromCursor` 返回 cursor 之后**全部**分块；`max_output_bytes`(2 MiB) ≫ `max_frame_bytes`(256 KiB) | 输出超约 256 KiB 时 `get_command_result` 编码失败 → 写循环退出 → 操作者连接被关 → 会话中断 |
| 4 | 会话无滑动续期 | `session.Manager.Authorize` 只写不读 `LastActivity` | `timeout_seconds` 最大 3600，但 600s 就被 janitor 杀掉，长命令必然中断 |
| 5 | 上传无流控 | `platformclient.handleUploadFile` 一次性发完全部分块 | 32 MiB = 512 帧灌进深度 64 的队列，设备稍慢被判 `slow_client` 断开，重连后分块静默丢失 |
| 7 | 设备连接可当 operator 发起会话 | `core.handleRequestSession` 只挡了“连自己” | 被攻破的设备客户端可以连别的设备 |
| 8 | 输出截断后重复发最后一帧 | `core.handleCommandOutput` | 靠客户端 `sequence` 去重才没出错；第三方客户端会重复渲染 |
| 9 | 操作者客户端单飞 | `platformclient.Client` 整个流式请求持 `requestMu` | 并发工具调用互相阻塞到超时；长任务应改用 `execute_command` + `get_command_result` |
| 11 | 分流没有健康检查 | 部署层（DNAT 轮询） | 网关挂掉后新连接仍可能被分过去，只能靠客户端重连；多网关故障恢复慢一拍 |

## 5. 关联文档

| 文档 | 内容 |
| --- | --- |
| [标识符与凭据](identifiers.md) | 6 个 ID + 3 个凭据：谁生成、什么生命周期、泄露后果 |
| [MCP 工具](mcp-tools-improvement-plan.md) | 22 个工具清单、使用约束、待办 |
| [Agent 询问设备用户](user-questions.md) | `ask_user` / `get_user_reply` 的状态语义 |
| [开发与构建](development.md) | 构建、测试、打包、部署 |
| [本机开发环境](local-dev.md) | 本机路径、快捷命令、已知坑 |
| [README](../README.md) | 产品定位与能力清单 |
