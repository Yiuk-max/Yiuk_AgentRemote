# 标识符与凭据速查

六个 ID、三个凭据。**设备码是公开标识，临时密码才是凭据。**

## 六个 ID

| ID | 谁生成 | 标识什么 | 生命周期 | 泄露后果 |
| --- | --- | --- | --- | --- |
| `device_id` | 客户端提议；密码不匹配时服务端另分配 | 一台设备 | 注册期间（内存）；释放后进墓碑，永不再分配给别人 | 无（客户端主页上就印着 `556 604 822`） |
| `connection_id` | 服务端 `newID()` | 一条 WebSocket 连接 | 连接建立 → 断开 | 无。只在进程内有效 |
| `session_id` | 服务端 `newID()` | 一次授权（设备 × 操作者 × 一段时间） | 默认 600s；审批阶段 120s | 低。还要操作者的 `connection_id` 匹配才能用 |
| `request_id` | 客户端 `randomID()` | 一次命令 / 一次文件传输 | 终态记录按 `terminal_record_retain_sec` 回收 | 无。它是幂等键 |
| `correlation_id` | 客户端 `randomID()` | 一次请求-响应往返 | 那一趟往返 | 无。只为把响应和请求配对 |
| `password_version` | 设备端 | 临时密码第几代 | 每次轮换 +1 | 无 |

`newID()` 与 `randomID()` 都是 16 字节 `crypto/rand` → 32 位小写 hex。

## 三个凭据

| 凭据 | 谁生成 | 证明什么 | 有效期 | 存在哪 | 泄露后果 |
| --- | --- | --- | --- | --- | --- |
| `temporary_password` | 设备端，32 字符 base32（160 bit） | 拥有这个 `device_id` | 默认 600s，可调 60s~24h | 服务端只存 salt + SHA-256；设备端 DPAPI 加密落盘 | 🔴 高：等于拥有这台机器 |
| `player_password` | 部署者写在 `platform.yaml` | 可以使用平台中继资源（不授予任何设备权限） | 永久 | 服务端配置；客户端 `identity.json`（DPAPI） | 🔴 高：能拉全量设备列表并使 owner 弹审批框 |
| `local-api` 的 `token` | Qt 客户端启动时生成 | 调用**本机** Qt bridge 的授权 | 每次启动换 | `local-api.json` | 🟡 中：本机任意进程可调用 Qt 的本地工具 |

## 最容易混的三条

1. **`device_id` 是标识，`temporary_password` 是凭据**。判据是 `(device_id, password)` 这个组合，单靠任何一个都不成立。
2. **`connection_id` 不是身份**：它的生命周期就是一条 socket。网络抖一下 → 新连接 → 旧会话全部 `interrupted`，重新申请新会话（不恢复旧会话）。
3. **`request_id` ≠ `correlation_id`**：`request_id` 跨十几个帧回答“这是哪一次任务”，是平台侧幂等键；`correlation_id` 只在一个请求和它的直接响应之间回答“这趟往返”。

## 查找路径

| 从……查…… | 走哪条路 |
| --- | --- |
| `device_id` → 连接 | `device.Manager.OnlineConnection()` → connID |
| `connection_id` → socket | gateway 的连接表（`internal/platform/gateway/server.go`） |
| `session_id` → 会话 | `session.Manager.Get()` |
| `request_id` → 任务 / 传输 | `task.Manager.Get()` / `transfer.Manager.Get()` |

这几张表都在进程内存里，进程重启全空。

## 附：MCP 名字的两处不同

- `agentremote` = Go module 名 / MCP `serverInfo.name`
- `yiuk` = 宿主配置里 `mcpServers` 的键，决定工具前缀 `mcp__yiuk__*`

改名不影响协议，但要同步改 `~/.pi/agent/mcp.json`、`.pi/mcp.json` 与文档。
