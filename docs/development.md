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

需要读取远端文件时，先通过 `request_connection` 显式申请 `read_file` 权限并取得已批准会话，再调用 `download_file(session_id, remote_path, destination_path?)`。未提供 `destination_path` 时文件写入用户缓存目录下的 `Yiuk/DeviceAssist/downloads`；显式路径必须是绝对路径且不会覆盖已有文件。

文件按 64 KiB 分块传输，服务端默认 `max_download_bytes: 33554432`（32 MiB），设备端强制同一上限并计算 SHA-256，MCP 端落盘前校验大小和校验和。该权限与 `execute_command` 相互独立，不能由命令执行权限隐含获得。

生产环境必须使用 TLS 并校验证书；`allow_plaintext` 只用于本机开发测试。

### 自签名证书（内部部署）

`deploy/install-platform.sh` 部署的服务默认使用自签名证书。客户端不会静默关闭校验，而是要求显式确认：

1. 在设置页填写服务器地址（部署脚本默认 `:29564`，与 `configs/platform.yaml` 的 `:25664` 不同，以实际部署为准）。
2. 首次连接会弹出证书确认框，显示证书名称与 SHA-256 指纹。
3. 确认是自己的服务器后选择“信任此证书”，客户端会固定该指纹（pinning），后续只接受同一证书。
4. 更换服务器地址会自动清除旧的指纹；证书变更会以“证书已变更”高优先级警告提示。

同一指纹会同步到 MCP 配置的 `pinned_cert_sha256`，因此 Agent 侧同样按指纹校验，不需要 `insecure_skip_verify`。

## 配置与安全约定

- 绝不提交真实密码、TLS 私钥或其他凭证。
- 平台服务当前完全内存化：重启后设备临时密码、会话和任务全部失效，这是有意设计。
- 平台 `player_password` 与 Minecraft `host_password` / `player_password` 必须使用不同的随机强密码。
- 管理端口应通过防火墙限制为可信运维终端访问。
## 服务器部署

Linux 主机可用随仓库提供的脚本一键安装平台服务：

```sh
sudo ./deploy/install-platform.sh /path/to/server.crt /path/to/server.key
```

脚本会：

1. 构建 `cmd/platform` 并安装到 `/opt/yiuk-platform`。
2. 复制证书到 `/opt/yiuk-platform/certs`，私钥权限设为 `600`。
3. 首次运行时生成随机 `player_password` 并打印一次（请立即保存）。
4. 安装并启动 `yiuk-platform.service`（systemd，`Restart=always`）。

已存在 `/opt/yiuk-platform/platform.yaml` 时不会覆盖，避免重新部署导致密码变化。

### player 密码长度

`player_password` 最少 8 个字符，少于 16 个字符时启动日志会输出警告。该密码只用于
证明连接方可以使用平台中转，不会直接授予任何设备访问权；但公开部署仍建议使用
20 个字符以上的随机值，避免被暴力尝试。

如果自行指定短密码，请同时确认 `/etc/ssh`、云控制台等入口已限制来源 IP，并关注
`player_auth_failed` 审计事件。

### 端口说明

平台默认监听 `:29564`。部署前请确认云主机安全组/防火墙已放行该端口；如果服务在
NAT 后面，需要同时确认公网可达性。可以用下面的命令快速验证：

```sh
openssl s_client -connect <host>:29564 -CAfile <server.crt> </dev/null | head
```

### 运维命令

```sh
systemctl status yiuk-platform
journalctl -u yiuk-platform -f
tail -f /opt/yiuk-platform/platform.log
systemctl restart yiuk-platform
```

平台完全内存化：重启会断开所有设备、会话和任务。日志只记录脱敏 ID、状态和错误码，
不记录密码、命令正文或完整输出。

## 联调工具

仓库提供两个测试工具，便于在没有真实 Windows 设备时验证平台。

### `cmd/smoke`：端到端冒烟测试

模拟设备端和操作者两端，完整跑一遍认证、注册、申请、批准、执行、取结果、关闭，
并验证跨连接拒绝、错误密码拒绝和已关闭会话拒绝。

```sh
go run ./cmd/smoke -addr host:29564 -password '<player-password>' -ca server.crt -mode approval
go run ./cmd/smoke -addr host:29564 -password '<player-password>' -ca server.crt -mode direct
```

### `cmd/device-sim`：设备模拟器

注册一个设备 ID，自动批准待批准会话，并对 `execute_command` 返回固定结果。不会执行
任何真实命令，仅用于验证操作者/MCP 路径。

```sh
go run ./cmd/device-sim -addr host:29564 -password '<player-password>' \
  -device-id 111222333 -temporary-password '<32+ chars>' \
  -ca server.crt -mode approval
```

随后可用 MCP `run_command` 指向该 `device_id` 和临时密码验证完整 Agent 链路。

## 提交前检查

以下内容绝不能进入仓库：

- `platform-client.json`、`local-api.json`、`platform.json`、`sessions.json`、`device.json`
  等客户端运行时凭证文件。
- `tmp-*` 临时调试脚本、TLS 握手抓包、证书导出文件。
- 任何包含 `player_password`、`temporary_password`、bridge token 的配置副本。

`.gitignore` 已默认忽略上述模式。提交前请执行一次：

```sh
git status --short
git diff --cached | grep -nE "password|token|secret|PRIVATE KEY" || true
```

如果密码已经在某个提交中泄露，仅删除文件不够：历史记录仍然可被检出。需要轮换密码，
必要时用 `git filter-repo` 重写历史并强制推送。
