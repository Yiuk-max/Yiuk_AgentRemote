# Yiuk Switchboard

## 项目状态

本项目的主产品是面向 AI Agent 的远程设备协作与执行平台，正式需求见 [`docs/product-requirements.md`](docs/product-requirements.md)。Windows Qt 客户端当前已具备本机协助界面、本机设备信息、设备码收藏和本地会话管理；跨设备平台连接、设备注册和远程路由仍在实现中。旧 Qt 运维客户端与旧 Minecraft MOD 已移除。

Minecraft 联机是必须保留的既有能力，但不要求保留当前后端架构、协议或 MOD 实现。可以重构或重写 Go 后端，并在需要时调整 MOD 以适配新的后端。改造前后都应验证 Minecraft 联机功能仍可用。

当前仓库保留了既有 Go TCP relay 和运维代理源码。它们尚未实现 PRD 所需的通用设备注册、所有者授权、远程命令执行或 MCP 接入；`ops/server` 是 relay 运维代理，不是远程执行 Agent。

## 目录

- `cmd/relay`、`internal/`：Go TCP relay 与房间/会话实现。
- `ops/server`：管理 relay 进程的 Go 运维代理，不是 PRD 中的 Windows 远程执行 Agent。
- `configs/`：relay 配置模板。
- `deploy/`：部署相关文件。
- `111.md`：PRD 入口；正式产品需求见 `docs/product-requirements.md`。
- `docs/technical-selection.md`：技术选型与待决技术门槛。
- `docs/implementation.md`：模块边界、协议、会话/任务状态和实现顺序。
- `docs/product-requirements.md`：唯一正式产品需求基线。
- `docs/schedule.md`：MVP 工期、人力假设、阶段和风险。
- `docs/server-refactor-plan.md`：完全内存化服务器改造计划、密码授权模型和实施阶段。

## Windows Qt 客户端

Qt 客户端位于 [`qt-client`](qt-client) 目录，使用 Qt 6 Widgets、C++17 和 CMake。当前界面包含：

- `主页`：显示本机设备码、连接密码、设备码输入和协助请求入口。
- 设备码输入行：输入框、添加设备按钮和申请连接按钮保持在同一行；添加设备后保存到本机设备列表。
- `设备列表`：显示本机设备和已添加设备，支持复制本机设备码、刷新列表和滚动查看设备。
- `会话记录`：显示本机会话与任务记录。
- `设置`：控制本机接收协助请求和本地记录。

当前客户端的设备列表和会话数据保存在当前 Windows 用户配置目录。设备码用于识别设备，不能单独认证。平台未连接时，添加的远程设备不能发起真实连接。

构建 Qt 客户端：

```sh
cmake -S qt-client -B qt-client/build-qt -G Ninja
cmake --build qt-client/build-qt
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

当前 adapter 注册 8 个统一工具，但远程平台后端尚未实现。工具调用会返回 `NOT_CONFIGURED`，不会伪造设备在线或命令执行结果。后续 Agent 类型通过增加 adapter 复用 `agenttools` 契约，不修改工具业务语义。

## Go 开发

需要 Go 1.26 或更新版本。

```sh
go test ./...
go build ./...
```

构建 relay：

```sh
go build -o relay ./cmd/relay
```

构建运维代理：

```sh
go build -o ops-agent ./ops/server/cmd/ops-agent
```

relay 使用 `configs/config.yaml` 作为配置模板。部署前应复制到受限权限的运行目录，为 host 和 player 配置不同的随机强密码，并按部署环境设置监听端口。不要将真实密码、TLS 私钥或其他凭证提交到仓库。

运维代理配置模板位于 `ops/server/ops-agent.yaml`。管理端口应通过防火墙限制为可信运维终端访问。

## 后续方向

以 `111.md` 为产品范围依据推进主产品。下一步重点是设备注册、凭证保护、平台在线连接、跨设备请求/批准、会话撤销和远程任务路由。现有 Qt 客户端已经提供本机 UI 和本地 MCP 桥接，但不能替代真实平台服务。Minecraft 联机能力必须保留，但其 Go 后端和 MOD 可以协同调整。
