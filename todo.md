协助时平台那里已经断联，客户端任然显示协助ing，显示仍然连接着平台

但 Go 侧operator/MCP 客户端从不发
心跳(internal/platformclient 里
没有任何 ping)。所以会话闲置超过45秒→连接被服务端判死→transport 自动重连（新连接ID)→会话仍绑在旧连接上→再调 close_session 就not
found / connection does not own the session。这次我把挂起压到25秒，关闭就正常了。
两个建议的修复（都改 GO，不改Qt)：

1. operator 客户端加心跳（比如 15 秒一次 ping)，长会话就不会莫名断；
2.DEVICE ALREADY_ONLINE 从"永久放弃重连“改成退避重试（那是 Qt侧，需重新编译客户端）。

---

## 处理结果（已完成）

1. **operator 心跳（Go）**：`internal/platformclient` 在连接建立后启动保活协程，空闲时每
   15 秒发 `ping`（`heartbeat_interval_seconds` 可覆盖，下限 1 秒），连续 3 个周期收不到
   `pong` 判定对端已死并主动断开，下次工具调用重连。写帧统一走 `writeMessage` 加锁 +
   写 deadline，心跳与工具请求不会交错误帧。
2. **DEVICE_ALREADY_ONLINE（Qt）**：不再永久放弃重连，改为断链后按指数退避（1s→30s）
   重试注册，服务端旧连接过期后自动恢复在线；其余凭证类错误仍是终止重试、等待用户处理。
3. **追加修复**：
   - 服务端把「会话不存在 / 不属于该连接」统一映射为 `SESSION_NOT_FOUND`（原先 11 处
     返回不一致的 `NOT_FOUND`）。
   - `close_session` 对 `SESSION_NOT_FOUND` 幂等成功，重连后关闭旧会话不再报错。
   - 客户端平台连接断开时，本机把所有 platform 会话标记为 `interrupted` 并停掉任务，
     UI 不再在断联后继续显示「远程协助进行中」；`interrupted` 有了中文状态文案。
4. **回归测试**：`internal/platformclient` 新增 `TestPlatformClientHeartbeatsIdleConnection`
   与 `TestPlatformClientCloseSessionIsIdempotent`；`go test ./...` 全绿，Qt 客户端重新编译通过。

## Minecraft 联机已彻底移除

删除：`cmd/relay`、`internal/{room,session,protocol,control,server,config,connlimit}`、
`proto/switchboard.proto`、`configs/config.yaml`、`ops/`（relay 运维 agent），以及平台
侧 `minecraft` 业务类型/配置节和全部文档中的 Minecraft/游戏联机内容。
