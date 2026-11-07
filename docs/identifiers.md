# 标识符与凭据速查

项目里出现过 6 个 ID + 3 个凭据，容易混。这份文档只回答三件事：**谁生成的、标识什么、泄露了会怎样**。

设计理由见 [关键技术架构](key-architecture.md)，运行时用法见 [运行时架构](runtime-architecture.md)。

---

## 1. 先看全局：它们分属四个圈

```text
① 设备圈                   ② 连接圈                 ③ 授权圈              ④ 请求圈
device_id                  connection_id            session_id            request_id
temporary_password                                  （存的是          correlation_id
password_version                                     ①②的 ID）
   │                          │                        │                      │
   └── 一台机器               └── 一条 socket           └── 一次授权           └── 一次任务/传输
```

**最容易懵的一点**：`session_id` 本身不存 `device_id`，它存的是 **`connection_id`**（`DeviceConnectionID` / `OperatorConnectionID`）。所以"连接断了 = 会话用不了了"，这也正是演进计划 P0 要改的地方。

---

## 2. 六个 ID

| ID | 谁生成 | 标识什么 | 作用域 | 生命周期 | 泄露后果 |
| --- | --- | --- | --- | --- | --- |
| `device_id` | 客户端提议；密码不匹配时服务端另分配 | **一台设备** | 平台全局 | 注册期间（内存）；释放后进墓碑，**永不再分配给别人** | **无**。它是公开标识——客户端主页上就印着 `556 604 822` |
| `connection_id` | 服务端 `newID()`（`server.go`） | **一条 WebSocket 连接** | 单条连接 | 连接建立 → 断开 | **无**。只在进程内有效，不可复用 |
| `session_id` | 服务端 `newID()`（`handler_session.go`） | **一次授权**（设备 × 操作者 × 一段时间） | 一个设备一个操作者 | 默认 600s；审批阶段 120s | 低。还要操作者的 `connection_id` 匹配才能用 |
| `request_id` | **客户端** `randomID()` | **一次命令 / 一次文件传输** | 平台全局唯一 | 进程生命周期（**目前终态也不回收** ← 已知问题） | 无。它是幂等键 |
| `correlation_id` | **客户端** `randomID()` | **一次请求-响应往返** | 单次往返 | 那一趟往返 | 无。只为把响应和请求配对 |
| `password_version` | 设备端 | 临时密码第几代 | 一台设备 | 每次轮换 +1（当前实例是 78） | 无 |

`newID()` 与 `randomID()` 都是 16 字节 `crypto/rand` → 32 位小写 hex（`password_version` 除外，它是递增整数）。

---

## 3. 三个凭据（不是 ID，别混）

| 凭据 | 谁生成 | 证明什么 | 有效期 | 存在哪 | 泄露后果 |
| --- | --- | --- | --- | --- | --- |
| `temporary_password` | 设备端，32 字符 base32（160 bit） | **拥有这个 device_id** | 默认 600s，可调 60s~24h | 服务端只存 **salt + SHA-256**；设备端 DPAPI 加密落盘 | 🔴 **高**：等于拥有这台机器 |
| `player_password` | 部署者写在 `platform.yaml` | **可以使用平台中继资源**（不授予任何设备权限） | 永久 | 服务端配置；客户端 `identity.json`（DPAPI） | 🔴 高：能拉全量设备列表并使 owner 弹审批框 |
| `local-api` 的 `token` | Qt 客户端启动时生成 | 调用**本机** Qt bridge 的授权 | 每次启动换 | `local-api.json` | 🟡 中：本机任意进程可调用 Qt 的本地工具 |

---

## 4. 一次调用的 ID 时间线

```text
设备端启动
  device_id = 556604822                     ← 从 identity.json 读出来（或首次由服务端分配）
  temporary_password = K7QM…                ← 设备端生成，告诉操作者

操作者连接
  connection_id = "a3f9…"（operator）        ← 服务端在 accept 时生成

request_session{purpose}
  session_id = "77c1e0aa…"                  ← 服务端生成，返回给 operator
  （会话里记 OperatorConnectionID = "a3f9…"，DeviceConnectionID = "b71c…"）

execute_command{session_id}
  request_id = "5d2e…"                      ← 客户端生成，用作幂等键
  correlation_id = "9f41…"                  ← 客户端生成，只为这一趟配对
      ↑ 平台回 command_accepted 时原样带回 correlation_id，客户端据此认出"这是我的响应"

get_command_result{session_id, request_id}
  用同一个 request_id 反复查 → 拿输出和终态
```

**注意 `request_id` 在 `execute_command` 之后一直被用**：`get_command_result` / `cancel_command` 都靠它定位那个任务。所以那个 ID 是**客户端起的名字**，不是服务端给的。

---

## 5. 五个最容易混的地方

### ① `device_id` 是标识，`temporary_password` 是凭据
设备码公开（屏幕上印着、`list_devices` 也会返回），密码保密。判据是 `(device_id, password)` 这个**组合**。

### ② `connection_id` 被当成"身份"用了
`Session.OperatorConnectionID` 存的是连接 ID，而连接 ID **生命周期 = 一条 socket**。所以：
- 网络抖一下 → 新连接 → 新 connID → 旧会话全部 `interrupted`；
- 这也是多网关改造的第一个障碍（socket 不可迁移）。

演进计划 P0 就是把这里换成稳定的 `OperatorPrincipal` + `resume_token`。

### ③ `request_id` ≠ `correlation_id`
| | `request_id` | `correlation_id` |
| --- | --- | --- |
| 回答什么 | "这是哪一次任务"（跨多帧） | "这是哪一趟往返"（一帧） |
| 跨几个消息 | 从 `execute_command` 到 `get_command_result`，**十几个帧** | 只在一个请求和它的直接响应之间 |
| 谁用 | 平台（幂等 + 定位任务） | 客户端（把我的响应从别人的响应里挑出来） |

### ④ `session_id` ≠ `request_id`
- 一个 `session_id` 下可以有很多 `request_id`（连跑 10 条命令 = 1 会话 + 10 请求）；
- 但 `max_tasks_per_session = 2` 限制同时**未完成**的任务数；
- 跨会话复用同一个 `request_id` 会被拒（`REQUEST_ID_CONFLICT`）。

### ⑤ `request_id` 是**全局**唯一键，不是会话内唯一
`task.Manager.tasks` 是一个以 `request_id` 为键的全局 map。好处是实现简单、天然防重；代价是——**它永不回收**（`docs/key-architecture.md` §5 第 2 条），长期运行会累积内存。

---

## 6. 一张"谁认识谁"的表

| 从……查…… | 走哪条路 | 代码位置 |
| --- | --- | --- |
| `device_id` → 连接 | `device.Manager.OnlineConnection()` → `connID` | `device/manager.go` |
| `connection_id` → socket | `Server.connections[connID]` | `server/routing.go` |
| `session_id` → 会话 | `session.Manager.Get()` | `session/manager.go` |
| `request_id` → 任务 | `task.Manager.tasks[requestID]` | `task/manager.go` |
| `request_id` → 传输 | `transfer.Manager.transfers[requestID]` | `transfer/manager.go` |

**三张 map 都以 ID 为键，都在进程内存里，进程一重启全空** —— 这是 [D9「服务端全内存」](key-architecture.md#d9服务端全内存且明确不持久化) 的直接结果。

---

## 7. 命名上的一处历史包袱

MCP server 名叫 `agentremote`（`NewServer` 里），但 pi 配置里注册的名字是 `yiuk`，工具名因此是 `mcp__yiuk__*`。这两者不是同一个东西：

- `agentremote` = Go module 名 / MCP `serverInfo.name`
- `yiuk` = pi 配置里 `mcpServers` 的键，决定工具的**调用前缀**

改名不影响协议，但要同步改 `~/.pi/agent/mcp.json`、`.pi/mcp.json` 与文档。
