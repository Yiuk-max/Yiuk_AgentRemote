# Yiuk Switchboard 实现方案

版本：1.0  
状态：MVP 实施基线  
关联文档：[技术选型](technical-selection.md)、[产品需求](product-requirements.md)、[工期估算](schedule.md)、[后端实施状态](backend-status.md)

## 1. 目标与范围

交付一个可在真实两台 Windows 电脑验证的远程协助闭环：两端安装同一款 Qt 软件；受协助者注册设备并批准会话；协助者电脑上的 Agent 经 MCP 调用统一工具；远端 PowerShell 执行单条命令并返回 stdout、stderr 和退出码。MVP 同时验证现有 Minecraft 联机能力未回归。

本文件定义建议实现边界。平台服务端、Agent 平台 backend 和 Qt 客户端平台对接已经实现，具体范围与测试见 [后端实施状态](backend-status.md)；真实 Windows 端到端验收、进程树清理和 Minecraft 联机回归仍在推进。既有 Go relay 继续承载 Minecraft 联机，旧运维代理和 Protobuf schema 为待评估资产。

## 1.1 当前 Qt 客户端界面

当前 Qt 客户端使用固定的桌面工具布局，主要页面如下：

- **主页**：本机设备码、连接密码、复制/显示/刷新密码、目标设备码输入和申请连接。
- **设备码输入行**：输入框右侧为添加设备按钮，随后是申请连接按钮；添加设备会将格式化后的设备码保存到当前 Windows 用户配置。
- **设备列表**：显示本机设备和已添加设备；本机设备支持复制设备码和更换设备码；设备条目按一台设备一行显示，设备数量较多时在列表区域滚动。
- **会话记录**：显示授权会话和命令任务的本地记录。
- **设置**：控制本机接收协助请求、查看本地记录和清除本地记录。

当前页面中的远程设备列表是本地保存的设备码列表，不代表设备已注册或在线。平台未连接时，设备条目的协助操作不可用。

## 2. 进程与模块

```text
操作者 Windows                         被协助者 Windows                Go 服务
+--------------------------+           +----------------------+         +----------------------+
| Agent (Codex/Pi/...)     |           | Yiuk Qt Client       |         | 身份/设备注册       |
|   MCP stdio              |           | 所有者 UI / 授权 UI  |         | 在线状态/连接请求   |
|       |                  |           | PowerShell Executor  |         | 会话授权/路由       |
| MCP Adapter              |           | Device Connection    |<------->| TLS TCP Framed Relay|
| Tool Core                |           +----------------------+         +----------------------+
| Operator Connection      |<------------------------------------------->|
+--------------------------+                                              |
```

逻辑模块：

- **Qt UI**：主页、设备列表、设备码收藏、在线状态、请求、会话和任务显示，以及批准/拒绝/停止入口。当前 UI 已能承载本机流程，远程在线状态仍需平台连接提供。
- **Device Core**：设备凭证、出站连接、心跳、请求展示、授权状态机。
- **Operator Core**：账户/设备查询、申请会话、提交任务、取消/结束。
- **Execution Core**：单次 PowerShell 进程、输出流、超时、取消和退出状态。
- **Tool Core**：稳定的设备、会话、任务 API 与结构化错误。
- **MCP Adapter**：stdio MCP server，映射 Tool Core；不包含设备端安全策略。
- **Go Platform**：认证、设备在线状态、会话状态机、请求路由、流量限制、审计元数据。
- **Minecraft Compatibility**：独立协议适配/业务处理，保持兼容验收清单所要求的游戏流程。

初期 Qt UI/Core 可在一个进程；MCP adapter 可为同一二进制的 `--mcp-stdio` 模式。是否另建执行子进程由原型安全/生命周期测试决定。Go 服务可单进程部署，按 package 隔离领域。

## 3. 端到端流程

### 3.1 注册

当前 Qt 客户端仅生成并持久化本机设备码，设备码保存在当前 Windows 用户配置目录。该身份还没有绑定账户或平台凭证，不能视为正式设备注册。

正式注册流程：

1. Qt 首次启动生成随机设备身份密钥，使用 Windows DPAPI 保护本机私钥/刷新凭证。
2. 用户通过短期配对流程将设备绑定到操作者账户；设备码仅用于识别，不作认证秘密。
3. 客户端主动建立 TLS TCP 连接并注册在线状态，定期心跳。
4. 服务端保存设备元数据和凭证状态，不记录私钥。

配对码有效期、账户身份提供方、密钥轮换/恢复流程是阶段一决策项。不要把硬件指纹作为唯一设备身份。

### 3.2 MCP 工具调用与会话批准

当前客户端提供受限的本机 HTTP bridge，供 `yiuk-mcp` 访问本机 UI/Core；本地请求可创建待批准会话并在 Qt 界面批准。该 bridge 只支持当前设备，不能替代平台服务。

正式远程流程：

1. Agent 启动同一软件的 MCP stdio 模式。
2. Adapter 接收 `request_connection(device_id, purpose, permissions)`，校验参数并调用 Tool Core。
3. Go 服务创建限时 `pending_approval` 会话，将申请推送到目标设备。
4. 受协助者 UI 显示协助者可识别身份、用途、权限范围和请求有效期。用户批准或拒绝。
5. Go 服务原子地更新状态；只有该设备、该操作者、该会话绑定的授权令牌可提交任务。
6. MCP 返回明确状态；等待批准应返回 pending/session ID，不占用无限期调用。

### 3.3 命令任务

1. Agent 调用 `execute_command(session_id, command, cwd, timeout)`。
2. Tool Core 生成不可重用的 request ID；服务端验证会话状态、权限、速率和并发上限。
3. 远端客户端再次验证 session/request 绑定后创建 `QProcess`，程序为 PowerShell executable，参数分离传递。
4. stdout/stderr 异步读取并按有界 chunk 发送。每块包含 request ID、stream type、sequence number；接收端校验序号并聚合或流式返回。
5. 任务完成时发送状态、exit code、截断标记和最终 sequence；MCP 返回结构化结果。
6. 超时或取消时结束整个子进程树。连接断开后取消任务；不能确认是否执行完成时标记 `result_unknown`，禁止自动重放非幂等命令。

### 3.4 结束和撤销

设备所有者点击停止、操作者关闭或会话过期时，平台撤销会话令牌、拒收新任务并下发取消；远端本地也立即停止对应任务。状态最终收敛到 `closed` 或 `interrupted`，记录操作者、设备、批准/拒绝、时间和终止原因，不默认保存命令完整输出。

## 4. 会话/任务状态机

会话：

```text
requested -> pending_approval -> approved -> active -> closed
                    |               |         |
                    +-> rejected    +-> expired/interrupted
```

只有 `approved/active` 可提交命令。每次授权绑定 device ID、operator ID、session ID、权限集合和到期时间。批准必须具备幂等 request ID，避免重复点击创建多会话。

任务：

```text
queued -> running -> succeeded | failed | timed_out | cancelled | result_unknown
```

服务端与设备端的任务状态需区分“请求未送达”“设备已启动”“结果部分返回”“结果完整”。重连不自动重复执行。

## 5. 网络协议基线

- 所有客户端主动连接 Go 服务，TCP + TLS；关闭弱密码套件，验证证书主机名。
- 帧：固定宽度网络字节序长度前缀 + Protobuf 或明确定义的版本化二进制消息体；选择一种并统一，不允许沿用旧 schema 却不做语义评审。
- 控制帧与输出 chunk 均有限长；大输出拆块，限定总输出、并发、队列和每设备带宽。
- 公共 envelope 至少有 `protocol_version`、`message_type`、`request_id`、`session_id`、`sequence`。
- 处理读写 deadline、心跳、服务端限流、半开连接、慢客户端、背压和优雅关闭。
- 单一会话只能在已批准的双端连接间路由；设备在线状态不是授权。
- 连接与会话业务类型显式区分 `agent_assist` 和 `minecraft`，配置独立限制/指标。

MVP 可以采用 TLS 端到端到 Go 服务、由服务路由明文应用消息的模式，但须记录中转可见性风险。若威胁模型要求 relay 不可读取命令/输出，需在开发前定义端到端加密、密钥交换、重放防护和授权撤销的相容方案。

## 6. Go 服务拆分建议

```text
cmd/platform/              服务入口
internal/identity/          账户、设备凭证、配对
internal/device/            在线状态、连接维护
internal/session/           请求、批准、撤销、会话状态机
internal/task/              任务状态、幂等与结果元数据
internal/transport/         TLS TCP、帧、连接路由、限流
internal/audit/             最小化审计事件
internal/minecraft/         旧联机协议/房间兼容边界
```

这是目标划分建议，不要求一次性搬完现有目录。先定义领域接口和测试，再迁移。早期状态可存内存，但需明确服务重启会使在线状态和会话失效；若用户需要可靠设备清单/审计，则为持久数据选择 SQLite/PostgreSQL 等存储，阶段一决策。不要把当前 in-memory Minecraft 房间状态误认为可持久恢复。

## 7. Qt/Windows 实现要点

- Qt Network 承载 TLS TCP；socket 操作与界面主线程解耦，使用 QObject worker/线程或事件驱动，不阻塞 UI。
- `QProcess` 的 stdout/stderr 分别异步读取，避免子进程输出填满管道死锁。
- 设置 `CreateProcess` 参数/工作目录与明确的 PowerShell executable；避免 `cmd.exe /c` 字符串拼接。
- 定义 UTF-8 结果编码，测试 BOM/本地代码页/非 ASCII 路径和输出。
- Windows Job Object 管理命令子进程树；程序退出、断线、取消和超时时清理。
- 输出达到限制时停止继续缓存，并在协议中报告 `truncated=true`；避免 GUI/Agent 侧内存无界增长。
- 使用 Windows DPAPI 保护本地凭证；日志脱敏；更新包签名和 TLS 证书校验纳入发布要求。
- UI 始终可见会话状态、远端操作者和立即断开按钮。不能在无提示状态下安装/启用常驻控制。

## 8. MCP 工具 schema 建议

所有工具返回 `{ok, code, message, data}` 结构，错误 code 稳定且不依赖自然语言。输入使用严格 schema，限制字段长度和权限集合。

| 工具 | 核心参数 | 结果 |
| --- | --- | --- |
| `list_devices` | 可访问范围/分页 | 设备 ID、名称、在线状态 |
| `get_device_status` | device_id | 在线、客户端版本、活动会话摘要 |
| `request_connection` | device_id、temporary_password、purpose、requested_permissions、wait_for_approval_seconds | session_id、pending/approved/rejected/expired |
| `run_command` | device_id、temporary_password、command、cwd、timeout、wait_for_approval_seconds、keep_session_open | 聚合 stdout/stderr、exit_code、状态、是否关闭会话 |
| `get_session_status` | session_id | 状态、授权范围、错误 |
| `execute_command` | session_id、command、cwd、timeout | request_id、状态、stdout/stderr、exit_code、truncated |
| `get_command_result` | session_id、request_id、cursor | 新输出块和终态 |
| `download_file` | session_id、remote_path、destination_path | 本地路径、文件大小、SHA-256、传输状态 |
| `cancel_command` | session_id、request_id | 是否接受取消及任务状态 |
| `close_session` | session_id | 关闭状态 |

注意：产品级权限不应由 Agent 任意请求并自动授予；设备所有者 UI 展示并批准请求权限，服务端只下发已批准范围。`read_file` 与 `execute_command` 是独立权限，文件下载不能通过命令执行权限隐含获得。

## 9. Minecraft 兼容策略

阶段一先定义黑盒验收：原版模组登录/认证、创建/关闭房间、房间列表、房主断线恢复（若仍需保留）、玩家加入、实际双向游戏 TCP 流、现有目标 MC/loader 组合。明确版本与测试环境。

有三种迁移方案，阶段一选定：

1. **共享服务、分业务协议**：保留游戏端口/旧协议适配器，新设备/会话服务独立。
2. **新旧 relay 并行**：新平台服务单独运行，旧 Minecraft relay 独立部署；适合降低回归风险，但部署成本上升。
3. **单后端重写并迁移 MOD**：统一传输/账户底层，MOD 适配新游戏业务 API；耦合更少但回归成本较高。

不以“编译通过”作为 Minecraft 保留验收；必须按冻结清单运行游戏客户端端到端测试。每个会影响公共协议或端口的变更，都应先有旧版本兼容或明确的配套发布策略。

## 10. 测试与交付

- Go 单测：协议帧边界、鉴权、限流、会话竞态、过期/撤销、幂等、路由隔离。
- Go 集成测试：两类客户端 mock、乱序/重复消息、断线、超时、慢读写、重启。
- Qt 单测：会话/任务状态、schema 映射、输出合并、UI model。
- Windows 集成测试：PowerShell stdout/stderr/exit code、非 ASCII、长输出截断、超时、取消、进程树清理。
- 安全测试：未批准执行、过期/重放 token、跨设备/跨会话 IDOR、凭证撤销、TLS 验证、日志泄露。
- Agent E2E：至少一个真实目标 Agent 通过 MCP 完成批准、执行、读结果、二次执行和关闭。
- Minecraft 回归：两台客户端/指定版本完成开房、加入和实际双向数据。

所有协议消息应有 golden test；桌面和服务端可观测 request/session ID，但日志不得包含密码、凭证或默认完整命令输出。

## 11. 分阶段实现顺序

### P0：统一工具与 MCP 适配

当前阶段只实现 Agent 无关的工具契约、参数校验、结构化结果/错误、MCP stdio adapter、可配置 backend 接口和 fake backend 测试。明确不包含 Qt UI、远程设备连接、真实命令执行和设备注册。P0 完成标准：目标 Agent 能启动 MCP 子进程、发现全部工具、调用 fake backend 并读取稳定结构化响应；未配置后端返回 `NOT_CONFIGURED`。

### P1 及以后

1. 冻结用户流程、双端同款软件形态和 Minecraft 保留验收。
2. 做 TCP/TLS、MCP stdio、QProcess 与 Minecraft 兼容 spike，比较后端复用/重写方案。
3. 定义协议和状态机，写跨语言 golden test。
4. 做两台电脑的手工授权远程命令原型，不先做完整安装 UX。
5. 加入设备注册、批准 UI、撤销、限额和审计。
6. 提供 MCP adapter 和真实 Agent E2E。
7. 跑安全、稳定性、Minecraft 兼容和安装升级验收后发布 MVP。