# 开发与构建

产品见 [README](../README.md)，模块边界见[实现方案](implementation.md)，实施范围见[后端实施状态](backend-status.md)。

## 环境要求

- Go 1.27.3+（`go.mod` 的 `go` 指令）
- CMake 3.21+、Ninja、Qt 6.4+（`Widgets`、`Network`）
- Windows 10/11 x64

## 工具链

```sh
go version                        # go1.27.3
go install golang.org/dl/go1.27.3@latest && go1.27.3 download
```

基础安装低于 1.27.3 时由 `GOTOOLCHAIN=auto` 自动下载。`go install` 的二进制位于 `%USERPROFILE%\go\bin`，需在 `PATH` 中。

依赖版本下限：go-sdk v1.4.1、x/sys v0.44.0、segmentio/encoding v0.5.4。

## 依赖检查

```sh
go mod verify
govulncheck ./...                 # go install golang.org/x/vuln/cmd/govulncheck@latest
```

基线输出 `No vulnerabilities found.`。

## 源码结构

```text
cmd/platform/ cmd/relay/ cmd/yiuk-mcp/ cmd/smoke/ cmd/device-sim/

internal/platform/            auth config device session task transfer protocol transport audit
internal/platform/server/     server.go conn.go routing.go janitor.go handler.go
                              handler_session.go handler_command.go handler_download.go
                              errors.go convert.go
internal/platformclient/      client.go config.go transport.go cache.go
                              tools_device.go tools_session.go tools_command.go tools_file.go
internal/agenttools/         工具契约
internal/mcpadapter/         MCP stdio
internal/room/ control/ server/ protocol/   Minecraft relay
ops/server/                  relay 运维代理
configs/                     平台与 relay 配置模板
```

Qt 客户端（`qt-client/src`）：

```text
main.cpp                    入口
mainwindow.{h,cpp}          窗口、托盘、侧边栏、页面切换、会话持久化
pages_home.cpp              主页
pages_devices.cpp           设备列表、会话记录
pages_settings.cpp          设置
platformlink.cpp            本地桥、平台配置、MCP 配置同步
platformevents.cpp          平台会话事件、文件下载、命令任务
sessionflow.cpp             会话过期、待批准、批准、活动会话
localtools.cpp              本机工具实现
activity.cpp                远程协助活动窗口
powershellrunner.{h,cpp}    PowerShell 任务执行
apphelpers glyph titlebar   控件工厂、路径、图标、标题栏
platformclient localbridge securestore   协议客户端、本机 bridge、DPAPI
```

## 构建与运行

```sh
go build -o platform ./cmd/platform  && ./platform -config configs/platform.yaml
go build -o relay ./cmd/relay        && ./relay -config configs/config.yaml
go build -o ops-agent ./ops/server/cmd/ops-agent && ./ops-agent -config ops/server/ops-agent.yaml
```

`configs/platform.yaml` 必须替换 `player_password` 并提供 `tls_cert` / `tls_key`；本机开发可设 `allow_insecure: true`。

```sh
cmake -S qt-client -B qt-client/build-qt -G Ninja
cmake --build qt-client/build-qt
```

## 测试

```sh
go test ./...
go test -race ./internal/platform/... ./internal/platformclient/...
```

Qt↔Go 互操作测试（需先启动平台服务）：

```sh
cmake -S qt-client -B qt-client/build-qt -G Ninja -DYIUK_BUILD_TESTS=ON
cmake --build qt-client/build-qt
YIUK_PLATFORM_ADDRESS=127.0.0.1:25664 \
YIUK_PLATFORM_PASSWORD=<player-password> \
./qt-client/build-qt/yiuk-platform-interop
```

覆盖认证、注册、申请、批准、`execute_command`、输出与结果回传、关闭会话。

## Agent MCP 配置

发布目录含 `yiuk-device-assist.exe` 与 `yiuk-mcp.exe`。**设置 → Agent 集成 → 复制 Agent 配置** 生成并复制配置：

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

平台配置：`%LOCALAPPDATA%/Yiuk/DeviceAssist/platform-client.json`，兼容读取 `%APPDATA%/Yiuk/DeviceAssist`。

远端文件：先取得带 `read_file` 权限的已批准会话，再 `download_file(session_id, remote_path, destination_path?)`。未指定路径时写入 `Yiuk/DeviceAssist/downloads`；显式路径须为绝对路径且不覆盖已有文件。分块 64 KiB，上限 32 MiB，SHA-256 校验。`read_file` 与 `execute_command` 权限独立。

生产环境使用 TLS 并校验证书，`allow_plaintext` 仅本机测试。

### 自签名证书

1. 设置页填写服务器地址（部署脚本默认 `:29564`，`configs/platform.yaml` 为 `:25664`）。
2. 首次连接显示证书名称与 SHA-256 指纹。
3. 选择“信任此证书”后固定指纹。
4. 更换地址清除旧指纹；证书变更显示“证书已变更”警告。

指纹同步到 MCP 配置的 `pinned_cert_sha256`。

## 配置与安全约定

- 不提交真实密码、TLS 私钥或其他凭证。
- 平台服务完全内存化，重启后临时密码、会话、任务全部失效。
- 平台 `player_password` 与 Minecraft `host_password` / `player_password` 使用不同随机强密码。
- 管理端口由防火墙限制为可信运维终端。

## 服务器部署

```sh
sudo ./deploy/install-platform.sh /path/to/server.crt /path/to/server.key
```

脚本：构建 `cmd/platform` 到 `/opt/yiuk-platform`；证书复制到 `certs`（私钥 `600`）；首次运行生成并打印 `player_password`；安装启动 `yiuk-platform.service`（`Restart=always`）。已存在 `platform.yaml` 时不覆盖。

`player_password` 最少 8 字符，少于 16 字符启动告警。监听 `:29564`，需放行安全组；验证：

```sh
openssl s_client -connect <host>:29564 -CAfile <server.crt> </dev/null | head
```

```sh
systemctl status yiuk-platform
journalctl -u yiuk-platform -f
tail -f /opt/yiuk-platform/platform.log
systemctl restart yiuk-platform
```

日志只记录脱敏 ID、状态与错误码。

## 联调工具

```sh
go run ./cmd/smoke -addr host:29564 -password '<player-password>' -ca server.crt -mode approval
go run ./cmd/smoke -addr host:29564 -password '<player-password>' -ca server.crt -mode direct
go run ./cmd/device-sim -addr host:29564 -password '<player-password>' \
  -device-id 111222333 -temporary-password '<32+ chars>' -ca server.crt -mode approval
```

`smoke` 覆盖认证、注册、申请、批准、执行、取结果、关闭，以及跨连接拒绝、错误密码拒绝、已关闭会话拒绝。`device-sim` 自动批准会话并返回固定结果，不执行真实命令；之后用 MCP `run_command` 指向该 `device_id` 与临时密码验证链路。

## 提交前检查

不入库：`platform-client.json`、`local-api.json`、`platform.json`、`sessions.json`、`device.json`、`tmp-*` 调试文件、TLS 抓包、证书导出，以及含 `player_password` / `temporary_password` / bridge token 的配置副本。

```sh
git status --short
git diff --cached | grep -nE "password|token|secret|PRIVATE KEY" || true
```

密码已进入提交时，轮换密码并用 `git filter-repo` 重写历史。
