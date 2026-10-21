# 开发与构建

本文档面向需要构建、运行和测试 Yiuk Switchboard 的开发者。产品介绍见
[README](../README.md)，实现边界见[实现方案](implementation.md)，后端实施范围见
[后端实施状态](backend-status.md)。

## 环境要求

- Go 1.26 或更新版本。
- CMake 3.21+、Ninja。
- Qt 6.4 或更新版本（Windows 推荐 Qt 6.8），组件 `Widgets`、`Network`。
- 目标平台：Windows 10/11 x64。

## 仓库结构（开发视角）

```text
cmd/platform/            平台服务入口
cmd/relay/               Minecraft relay 入口
cmd/yiuk-mcp/            Agent MCP stdio 入口
internal/platform/       平台认证、设备、会话、任务、协议、传输、审计
internal/platformclient/ Agent 使用的平台 operator backend
internal/agenttools/     统一工具契约
internal/mcpadapter/     MCP stdio adapter
internal/room/           既有 Minecraft relay 房间实现
internal/control/        既有 Minecraft relay 控制协议
ops/server/              relay 运维代理
qt-client/               Windows Qt 客户端
configs/platform.yaml    平台服务配置模板
configs/config.yaml      Minecraft relay 配置模板
docs/                    需求、方案、计划和实施状态
```

## 构建与运行

### 平台服务

```sh
go build -o platform ./cmd/platform
./platform -config configs/platform.yaml
```

`configs/platform.yaml` 中必须替换 `player_password` 并提供 `tls_cert` / `tls_key`。
仅本地开发可设置 `allow_insecure: true` 使用明文监听。

### Minecraft relay

```sh
go build -o relay ./cmd/relay
./relay -config configs/config.yaml
```

relay 与平台服务使用独立配置和端口，互不共享密码或会话。

### 运维代理

```sh
go build -o ops-agent ./ops/server/cmd/ops-agent
./ops-agent -config ops/server/ops-agent.yaml
```

### Qt 客户端

```sh
cmake -S qt-client -B qt-client/build-qt -G Ninja
cmake --build qt-client/build-qt
```

## 测试

```sh
go test ./...
go test -race ./internal/platform/... ./internal/platformclient/...
```

### Qt↔Go 互操作测试

需要先启动明文或 TLS 平台服务：

```sh
cmake -S qt-client -B qt-client/build-qt -G Ninja -DYIUK_BUILD_TESTS=ON
cmake --build qt-client/build-qt
YIUK_PLATFORM_ADDRESS=127.0.0.1:25664 \
YIUK_PLATFORM_PASSWORD=your-platform-player-password \
./qt-client/build-qt/yiuk-platform-interop
```

测试会启动一个 Qt 设备端和一个 Qt operator 端，完成认证、注册、请求、批准、
`execute_command`、输出/结果回传和关闭会话。

## Agent MCP 配置

Qt 客户端发布目录会同时包含 `yiuk-device-assist.exe` 和 `yiuk-mcp.exe`，不会把 MCP 文件复制到用户目录，也不会创建额外的安装目录。

在 Qt 客户端的 **设置 → Agent 集成** 中点击 **复制 Agent 配置**，客户端会自动使用当前发布目录中的 `yiuk-mcp.exe`，并把适用于 Agent 的 MCP 配置复制到剪贴板。

用户只需要把剪贴板内容粘贴到所使用 Agent 的 MCP 设置中。通用配置形态如下：

```json
{
  "mcpServers": {
    "yiuk-switchboard": {
      "command": "C:/path/to/YiukDeviceAssist/yiuk-mcp.exe",
      "args": []
    }
  }
}
```

MCP 平台配置保存在当前用户的 `%LOCALAPPDATA%/Yiuk/DeviceAssist/platform-client.json`，由 Qt 客户端自动生成。旧版本的 `%APPDATA%/Yiuk/DeviceAssist` 配置会兼容读取。

生产环境必须使用 TLS 并校验证书；`allow_plaintext` 只用于本机开发测试。

## 配置与安全约定

- 绝不提交真实密码、TLS 私钥或其他凭证。
- 平台服务当前完全内存化：重启后设备临时密码、会话和任务全部失效，这是有意设计。
- 平台 `player_password` 与 Minecraft `host_password` / `player_password` 必须使用不同的随机强密码。
- 管理端口应通过防火墙限制为可信运维终端访问。