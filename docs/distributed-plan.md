# 分布式演进计划：网关多实例 · RabbitMQ · 用户微服务

状态：**计划，未实施**。当前仍是单进程、全内存实现（见 [关键技术架构](key-architecture.md) D9）。

终态愿景见 `docs/agentremote_final_architecture_rabbitmq.svg`；**本文档是可达路径**，且明确写清"现在不做什么"。

---

## 0. 边界：这次做什么、不做什么

| | 内容 |
| --- | --- |
| ✅ 做 | 网关可多开（session/task/device **仍在一个进程里**）；RabbitMQ 承担**跨网关送帧**；新增**用户登录系统**作为**独立微服务** |
| ❌ 不做 | 拆 session-svc / task-svc / device-svc；平台本体持久化；会话跨重启恢复 |
| 触发条件 | 只有当**单个网关撑不住**，或"网关重启会杀掉所有会话"成为真实痛点时才开工 |

**为什么先不拆核心业务**：`session` / `task` / `device` 三个模块的每次状态变更都与连接生命周期强绑定（断线要 `InterruptByDevice`、过期要发 `close_session`），拆出去后每个状态变更都要跨网络，代价远大于收益。而**"用户"是全新领域**，没有历史包袱——它才是最适合先长成微服务的边界。

---

## 1. 目标形态

```text
                      ┌── Nginx stream（L4 透传，不终止 TLS）
  Agent / 设备 ──TLS─►│        ├──► gateway-1 ─┐
                      │        └──► gateway-2 ─┤
                      └────────────────────────┤
                                               ├── Redis      状态 + 路由表
                                               ├── RabbitMQ   跨网关送帧 + 审计事件
                                               └── user-svc ──► Postgres（仅用户数据）
```

三个关键事实：

1. **gateway 仍然是有状态的整体**——它内含 session/task/device 三块逻辑，只是这些数据搬到了 Redis。
2. **socket 搬不走**。所以多网关必须解决"这台设备的连接在哪个网关"，靠 Redis 里的路由表 + RabbitMQ 转发。
3. **user-svc 是唯一的新服务**，也是唯一碰 Postgres 的组件。

---

## 2. 分阶段路线

每个阶段都能独立上线、独立回滚，且都有可验收的收益。

### P0　会话可恢复（不引入任何新组件）

**问题**：现在会话绑 `OperatorConnectionID`，操作者一断线，`InterruptByOperator` 就把该连接名下所有会话标成 `interrupted`。多网关之后，一次滚动发布 = 该节点上所有会话全死，**可用性反而比单进程更差**。

**做法**：

```go
// session.Session 增加两个字段
OperatorPrincipal string  // "user:<id>"（P4 之后）或 "player:<connID>"（兼容期）
ResumeToken       string  // 重连时凭它续期，不打断任务
```

- `Authorize()` 从"比对 connectionID"改成"比对 principal"；
- 操作者重连后带 `resume_session{session_id, resume_token}`，把会话的 `OperatorConnectionID` 改成新连接；
- 设备断线仍然中断（设备侧进程真的断了，无法确认结果）。

**验收**：拔网线 3 秒再插回，会话仍是 `active`，正在跑的命令继续、结果正常回传。

**这一步收益最大、成本最低，且不依赖 P2/P3。**

---

### P1　审计出口抽象（只留缝，不接线）

**现状**：31 种审计事件只进 `slog`，进程重启即丢。现阶段**确实没什么用**——查不到、留不住、也没人消费。

**做法**：只做接口抽象，**不部署 RabbitMQ**。

```go
// internal/platform/audit
type Publisher interface {
    Publish(ctx context.Context, event string, attrs map[string]any) error
}
// 现在：SlogPublisher（默认，行为与今天完全一致）
// 以后：AMQPPublisher（P3/P5 才启用）
```

**不接线的原因**：MQ 没有消费者时，事件会在 broker 里堆积，只会白白吃内存。**等有 Postgres 消费者了再打开**（见 §5）。

**验收**：`go test ./...` 全绿，行为零变化。

---

### P2　Redis：状态外置 + 路由表

这一步是 P3 的**前置条件**，不能跳过。

**搬什么**

| 数据 | 现状 | 目标 |
| --- | --- | --- |
| `devices`（设备码 / 密码哈希 / 墓碑） | `device.Manager` 进程内存 | Redis |
| `sessions`（状态机 / 权限 / 到期） | `session.Manager` 进程内存 | Redis |
| `tasks`（状态 / 退出码） | `task.Manager` 进程内存 | Redis |
| **任务的 2 MiB 输出块** | `task.Task.Chunks` | ❌ **不进 Redis**，留在网关内存，或落对象存储 |
| `connections`（socket） | `Server.connections` | ❌ 搬不走，永远留在网关 |

**新增：路由表**

```text
route:device:<device_id>   →  {"node":"gw-1","conn":"a3f…","ver":7}   TTL 60s
route:session:<session_id> →  {"node":"gw-1"}                          TTL 60s
```

- 设备心跳（15s 一次）续期，TTL 60s ≈ 允许连丢 4 次心跳。
- `ver` 用于防止"旧连接重建覆盖新连接"。

**顺手必须修的两件事**（见 [关键技术架构](key-architecture.md) §5）：

1. `session.Manager.Create` 用 `len(m.sessions)` 判断上限，而终态会话**永不回收** → 改造时改成活跃计数。
2. 每网关都会跑 `janitor` 的 1 秒 ticker → 多网关后会**重复处理同一个过期会话**，需要 Redis 锁选主（或把 janitor 收敛到单一实例）。

**验收**：单网关时行为与今天 100% 一致（回归测试通过），且杀掉进程重启后 `route:*` 由心跳自愈。

---

### P3　多网关 + Nginx L4 + RabbitMQ 跨网关送帧

见 §3 详细设计。

**验收**：operator 连 gateway-1、设备连 gateway-2，跑通一条命令，端到端时延增加 < 20ms，`sequence` 严格连续。

---

### P4　用户登录系统（唯一的新微服务）

见 §4。

---

## 3. RabbitMQ 的两条用途

前面那句口诀：**要立刻拿到答案的走 gRPC；不需要立刻拿答案的走 RabbitMQ。**

### 3.1 跨网关送帧（核心用途）

```text
exchange:  agentremote.gw      type=direct
routing key: <目标网关节点名>   例：gateway-2
queue:     gw.gateway-1        （每个网关一个，自己独占消费）
queue:     gw.gateway-2
```

gateway-1 要把帧送给挂在 gateway-2 上的设备：

```go
ch.Publish("agentremote.gw", "gateway-2", false, false, amqp.Publishing{
    DeliveryMode: 1,          // ① 不落盘
    ContentType:  "application/json",
    Body:         frame,
})
```

### 三个必须做对的设置

| 设置 | 值 | 为什么 |
| --- | --- | --- |
| **队列与消费者** | 每个网关**一个队列、一个消费者**，且以 `exclusive=true, autoDelete=true` 声明 | ① MQ 只在**单队列单消费者**下保证顺序，而你的 `sequence` 校验、任务状态单向推进都依赖顺序；② 网关下线时队列自动删除，否则离线网关的队列会一直堆帧 |
| **确认模式** | `autoAck = true` | **至多一次**。手动 ack 的默认语义是"消费者崩了就把消息塞回去重投"，那会重放一条可能已经执行过的命令，直接违反不变式 #7 |
| **投递模式** | `DeliveryMode: 1`（不持久化） | 不落盘。broker 重启即丢，语义和现在的"全内存"一致，不引入新的持久化承诺 |

消费侧：

```go
msgs, _ := ch.Consume("gw.gateway-2", "gateway-2", true /*autoAck*/, true /*exclusive*/, false, false, nil)
for msg := range msgs {
    deliverToLocalConnection(msg.Body)   // 只查本进程的 connections
}
```

### 丢了怎么办

**不重试。** 帧丢了 → 调用方等超时 → 任务标 `result_unknown`、`result_known=false` → Agent 自己决定要不要重来。

这正是这个项目特别适合"至多一次"的原因：**"不确定"早就被当成一等公民了**，不需要为了"不丢"引入复杂的 ack/去重机制。

### 3.2 设备上下线广播（fanout）

设备在 gateway-1 掉线时，其他网关的路由缓存要失效：

```text
exchange: agentremote.presence   type=fanout
queue:    presence.gateway-1 / presence.gateway-2   （同样是 exclusive + autoDelete）
payload:  {"device_id":"123456789","online":false,"node":"gw-1","ver":8}
```

各网关收到后删本地缓存 / 改 Redis 里的 `route:device:*`。

### 3.3 明确不放进 MQ 的东西

| 不放 | 原因 |
| --- | --- |
| 文件分块（64 KiB × 512） | 大消息会阻塞队列；现有 `sequence` + 累计字节 + SHA-256 的分块协议更合适 |
| MCP 工具调用 | 本身是一问一答，插队列只会让模型等更久 |
| 会话权限校验 | 要立刻答复，必须 gRPC |
| 限流计数 / 在线状态 | 这是查表，Redis 的活 |

---

## 4. 用户登录系统（新微服务）

### 4.1 为什么它是"最适合先长成微服务"的边界

- 它是**全新领域**，没有历史包袱，不需要改造存量代码；
- 它和"设备 / 会话 / 命令 / 文件"**零耦合**，只回答"这个请求是谁发的"；
- 它是**唯一需要强持久化**的东西（用户数据不能因为平台重启就没了），因此也是独立数据库的天然候选。

### 4.2 它顺手解决了 P0 的身份问题

现在"操作者身份"就是一条连接（`OperatorConnectionID`），连接断了身份就没了。引入用户系统后：

```text
operator 身份：user:42（稳定）  ← 由 user-svc 签发
                 ↓
session.OperatorPrincipal = "user:42"   → 重连后凭 token 恢复会话
```

**用户系统不是"额外加个登录功能"，它是会话可恢复的使能条件。** 这一步做完，P0 的 `OperatorPrincipal` 才有真正的值可填。

### 4.3 职责边界（越界就是设计失败）

| 做 | 不做 |
| --- | --- |
| 注册 / 登录 / 登出、密码哈希、token 签发与校验 | ❌ 不碰设备、不碰会话、不碰命令 |
| 回答"这个 token 对应谁、有哪些权限" | ❌ 不做授权决策（那是 `session.Manager.Authorize` 的事） |
| 用户表、登录审计 | ❌ 不读平台的其他 Redis key |

一句话：**user-svc 只回答"你是谁"，不回答"你能干什么"。**

### 4.4 接口草案（gRPC）

```proto
service UserService {
  rpc Login     (LoginRequest)      returns (LoginReply);      // 用户名/密码 → token
  rpc Introspect(IntrospectRequest) returns (Identity);        // token → 身份 + 角色
  rpc Logout    (LogoutRequest)     returns (LogoutReply);
}

message Identity {
  string user_id   = 1;
  string display   = 2;
  repeated string roles = 3;
  int64  expires_at = 4;
}
```

- **选 gRPC 而不是 MQ**：登录校验必须立刻拿到答案，不能"扔进队列等回信"。
- **网关侧只在两处调用**：`authenticate_player`（换身份）和 `resume_session`（校验续期）。
- **缓存**：`Introspect` 结果在网关内存缓存 30~60 秒，避免每条消息都打一次 user-svc。

### 4.5 与现有 `player_password` 的兼容

`authenticate_player` 现在只认 `player_password`。改造后**同时接受两种**：

```text
identity_type = "player"  → 旧路径：比对 platform.yaml 里的 player_password（保留，供服务器上的 agent 使用）
identity_type = "user"    → 新路径：token → user-svc.Introspect → 得到 user_id
```

- 兼容期两条都开；`player_password` 永不删除（服务器上的 pi 靠它，删了会断掉现网部署）。
- 审计里必须记清是哪种身份进来的（`identity=player|user`），否则事后分不清谁做了什么。

### 4.6 数据

只有 user-svc 连 **Postgres**：`users`、`sessions_token`、`login_audit` 三张表。
平台本体（gateway）**仍然不持久化**——保持 D9 的取舍不变。

---

## 5. 审计：现在留缝，以后再接线

**现状很诚实**：31 种事件只进 `slog`，重启即丢，查不到三个月前谁在设备上执行了什么。

**所以这一路分两步：**

| 阶段 | 做法 | 说明 |
| --- | --- | --- |
| 现在（P1） | 只抽象 `audit.Publisher` 接口，实现仍是 slog | 不装 MQ、不装 Postgres，零成本 |
| 以后（有消费者时） | `AMQPPublisher` → exchange `agentremote.audit` → `audit.persist` 队列 → 写 Postgres | 这时才真正部署 |

**接线时的两个注意点：**

1. **必须有消费者才开**。没有消费者时事件会在 broker 里堆积；即使如此也要设 `x-max-length` 兜底（比如 10 万条 + `x-overflow: drop-head`），避免 broker 被撑爆。
2. **审计用 at-least-once**（`autoAck=false` + `DeliveryMode=2`）：重复写一行日志无害，**丢**才要命。这和 §3.1 的"跨网关送帧用至多一次"正好相反——**同一套 MQ，两条通道用两套语义**，不要混。

---

## 6. 必须守住的不变式

分布式之后，[关键技术架构](key-architecture.md) §3 的九条不变式仍然全部有效，其中四条最容易在改造中被破坏：

| 不变式 | 最容易被谁破坏 |
| --- | --- |
| #1 设备码不是凭据，密码匹配才算拥有 | 把设备码搬 Redis 后，"查 + 比 + 分配新码"不再原子 → 必须用 Lua/CAS 重写 |
| #6 `request_id` 不可跨会话复用 | 多网关共享 task 存储后，键空间变成全局，要重新确认冲突语义 |
| #7 断线不自动重放 | 用了手动 ack / 持久化队列就会被破坏（见 §3.1 三个设置） |
| #8 单连接消息严格有序 | 多消费者消费同一队列、或网关间走两条不同路径，都会乱序 |

---

## 7. 验收标准汇总

| 阶段 | 验收 |
| --- | --- |
| P0 | 拔网线 3 秒重连，会话仍 `active`，命令继续并正常回传 |
| P1 | `go test ./...` 全绿，行为零变化（纯抽象） |
| P2 | 单网关时行为与今天一致；进程重启后设备码/会话由心跳自愈；`max_sessions` 不再变终身配额 |
| P3 | 跨网关跑通命令，时延 +<20ms；`sequence` 严格连续；拔掉 gateway-2 后其队列自动删除 |
| P4 | 手机/服务器两种身份都能认证；token 过期后 `SESSION_NOT_FOUND`；用户表重启不丢 |

---

## 8. 明确不做的事

- ❌ **不拆** session / task / device 三个模块成独立服务。
- ❌ **不做** 平台本体持久化（重启仍丢连接、会话、临时密码）。
- ❌ **不把** 2 MiB 命令输出放进 Redis 或 MQ。
- ❌ **不在** Nginx 做 TLS 终止（会废掉客户端的证书指纹固定）。
- ❌ **不用** MQ 承载文件分块或 MCP 工具调用。

---

## 9. 关联文档

| 文档 | 内容 |
| --- | --- |
| [关键技术架构](key-architecture.md) | 九个关键决策、九条不变式、资源上限、已知薄弱点 |
| [运行时架构](runtime-architecture.md) | 连接拓扑、读写循环、失败收敛、MCP 工具契约 |
| [系统串讲](system-walkthrough.md) | 一次真实调用的全流程与排错 |
| `docs/agentremote_final_architecture_rabbitmq.svg` | 终态愿景图（仅供参考，非本文档的实施范围） |
