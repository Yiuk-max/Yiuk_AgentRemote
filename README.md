# Yiuk Switchboard

## 项目状态

本项目的主产品是面向 AI Agent 的远程设备协作与执行平台，正式需求见 [`docs/product-requirements.md`](docs/product-requirements.md)。Windows Qt 设备客户端将重新设计和实现；旧 Qt 运维客户端与旧 Minecraft MOD 已移除。

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

以 `111.md` 为产品范围依据推进主产品。先评估现有 relay、运维代理和 Protobuf 协议哪些值得复用，以及重构或替换它们的成本；技术实现服从产品边界，不为保持旧架构而限制设计。新 Qt 客户端、设备身份/会话协议和 MCP 工具层均尚未实现。Minecraft 联机能力必须保留，但其 Go 后端和 MOD 可以协同调整。
