# Yiuk Switchboard

## 项目状态

本项目的主产品是面向 AI Agent 的远程设备协作与执行平台，正式需求见 [`docs/product-requirements.md`](docs/product-requirements.md)。Windows Qt 客户端当前已具备本机协助界面、本机设备信息、设备码收藏和本地会话管理；跨设备平台连接、设备注册和远程路由仍在实现中。旧 Qt 运维客户端与旧 Minecraft MOD 已移除。

Go 平台后端已完成第一轮改造：`cmd/platform` 提供 TLS 长连接、player 密码认证、设备临时密码、direct/approval 会话状态机、任务状态与有界输出、会话路由和业务类型隔离；状态完全在内存中，重启即失效。Agent MCP 已可通过 `internal/platformclient` 连接平台后端。Qt 设备端已接入平台协议：注册临时密码、接收/批准会话、执行平台下发的 PowerShell 并回传输出；仍需在真实 Windows 上完成 TLS、进程树清理和安装包验收。

Minecraft 联机是必须保留的既有能力，但不要求保留当前后端架构、协议或 MOD 实现。可以重构或重写 Go 后端，并在需要时调整 MOD 以适配新的后端。改造前后都应验证 Minecraft 联机功能仍可用。

平台服务已实现 PRD 所需的设备临时密码、所有者批准、会话授权、任务路由和 MCP 接入。既有 Minecraft relay 继续独立运行；`ops/server` 是 relay 运维代理，不是远程执行 Agent。

## 目录

- `cmd/platform`、`internal/platform/`：平台服务入口、TLS 传输、认证、设备/会话/任务内存管理器、协议与审计。
- `cmd/relay`、`internal/`：Go TCP relay 与房间/会话实现（Minecraft 联机）。
- `internal/platformclient`：Agent MCP 使用的平台 operator 客户端 backend。
- `configs/platform.yaml`：平台服务配置模板；`configs/config.yaml` 为 Minecraft relay 模板。
- `ops/server`：管理 relay 进程的 Go 运维代理，不是 PRD 中的 Windows 远程执行 Agent。
- `deploy/`：部署相关文件。
- `111.md`：PRD 入口；正式产品需求见 `docs/product-requirements.md`。
- `docs/technical-selection.md`：技术选型与待决技术门槛。
- `docs/implementation.md`：模块边界、协议、会话/任务状态和实现顺序。
- `docs/product-requirements.md`：唯一正式产品需求基线。
- `docs/schedule.md`：MVP 工期、人力假设、阶段和风险。
- `docs/server-refactor-plan.md`：完全内存化服务器改造计划、密码授权模型和实施阶段。
- `docs/backend-status.md`：平台后端已实施范围、测试和剩余工作。

## Windows Qt 客户端

Qt 客户端位于 [`qt-client`](qt-client) 目录，使用 Qt 6 Widgets、C++17 和 CMake。当前界面包含：

- `主页`：显示本机设备码、连接密码、设备码输入和协助请求入口。
- 设备码输入行：输入框、添加设备按钮和申请连接按钮保持在同一行；添加设备后保存到本机设备列表。
- `设备列表`：显示本机设备和已添加设备，支持复制本机设备码、刷新列表和滚动查看设备。
- `会话记录`：显示本机会话与任务记录。
- `设置`：控制本机接收协助请求和本地记录，以及平台服务器地址、player 密码、TLS 和授权模式（direct/approval）。

当前客户端的设备列表和会话数据保存在当前 Windows 用户配置目录。设备码用于识别设备，不能单独认证。连接平台后，设备会显示本机 32 位临时密码；对方需输入该密码发起协助请求。临时密码刷新后旧密码和旧会话立即失效。

构建 Qt 客户端：

```sh
cmake -S qt-client -B qt-client/build-qt -G Ninja
cmake --build qt-client/build-qt
```

构建并运行 Qt↔Go 平台互操作测试（需要先启动明文或 TLS 平台服务）：

```sh
cmake -S qt-client -B qt-client/build-qt -G Ninja -DYIUK_BUILD_TESTS=ON
cmake --build qt-client/build-qt
YIUK_PLATFORM_ADDRESS=127.0.0.1:25664 YIUK_PLATFORM_PASSWORD=... ./qt-client/build-qt/yiuk-platform-interop
```

## Agent 工具适配（当前 P0）

统一工具核心位于 `internal/agenttools`，首个 MCP stdio adapter 位于 `internal/mcpadapter`。构建并启动：

```sh
go run ./cmd/yiuk-mcp
```

也可以构建独立程序：

```sh
go build -o yiuk-mcp ./cmd/yiuk-mcp
```

Agent MCP 配置示例（将 `command` 改为本机可执行文件的绝对路径）：

```json
{
  "mcpServers": {
    "yiuk-switchboard": {
      "command": "C:/path/to/yiuk-mcp",
      "args": []
    }
  }
}
```

当前 adapter 注册 8 个统一工具。若存在本机平台客户端配置 `%APPDATA%/Yiuk/DeviceAssist/platform-client.json`，`yiuk-mcp` 会使用远程平台 backend；否则回退到本机 Qt HTTP bridge；两者都没有时返回 `NOT_CONFIGURED`，不会伪造设备在线或命令执行结果。后续 Agent 类型通过增加 adapter 复用 `agenttools` 契约，不修改工具业务语义。

平台客户端配置示例（`platform-client.json`，仅本机用户可读写）：

```json
{
  "address": "switchboard.example.com:25664",
  "player_password": "REPLACE_WITH_THE_PLATFORM_PLAYER_PASSWORD",
  "tls_ca": "C:/certs/platform-ca.pem",
  "timeout_seconds": 30
}
```

本地开发可使用 `"allow_plaintext": true` 连接明文 listener；生产环境必须使用 TLS 并校验证书。平台工具调用返回结构化错误码（如 `DEVICE_OFFLINE`、`BAD_TEMPORARY_PASSWORD`、`INVALID_SESSION_STATE`、`PLATFORM_TIMEOUT`）。

`request_connection` 在平台 backend 下需要设备所有者通过安全渠道提供的 32 字符临时密码（可选字段 `temporary_password`）；临时密码只用于确认对目标设备的访问权，不会成为长期凭证。

## Go 开发

需要 Go 1.26 或更新版本。

```sh
go test ./...
go build ./...
```

构建平台服务：

```sh
go build -o platform ./cmd/platform
./platform -config configs/platform.yaml
```

构建 relay：

```sh
go build -o relay ./cmd/relay
```

构建运维代理：

```sh
go build -o ops-agent ./ops/server/cmd/ops-agent
```

relay 使用 `configs/config.yaml` 作为配置模板。平台服务使用 `configs/platform.yaml`；部署前应复制到受限权限的运行目录，为平台 player 密码、Minecraft host/player 密码分别设置不同的随机强密码，并按部署环境设置监听端口和 TLS 证书。不要将真实密码、TLS 私钥或其他凭证提交到仓库。

平台服务当前内存化运行，重启后设备临时密码、会话和任务全部失效；这是改造计划的有意设计，不要用数据库或磁盘文件"修复"它。

运维代理配置模板位于 `ops/server/ops-agent.yaml`。管理端口应通过防火墙限制为可信运维终端访问。

## 后续方向

平台服务端（P0/P1/P2）和 Agent 平台 backend（P4 的 operator 侧）已实现，并有单元与集成测试覆盖认证、direct/approval、临时密码轮换、跨会话拒绝、输出截断和会话/任务终态。下一步重点是 P3：Qt 设备端接入平台 TLS 连接、注册设备临时密码、展示待批准请求并对每个 session 再校验设备 ID、session ID、权限和过期时间；以及 P4 设备侧的 PowerShell 任务链路、取消和进程树清理。Minecraft 联机能力必须保留，但其 Go 后端和 MOD 可以协同调整，并需按冻结清单做端到端回归。
