# 开发与构建

产品见 [README](../README.md)，模块与协议见[架构与实现](architecture.md)，本机专属路径与已知坑见[本机开发环境](local-dev.md)。

## 1. 环境要求

- Go 1.27.3+（`go.mod` 的 `go` 指令；低于时由 `GOTOOLCHAIN=auto` 自动下载）
- CMake 3.21+、Ninja、Qt 6.4+（`Widgets`、`Network`）
- Windows 10/11 x64

```sh
go version                        # go1.27.3
go install golang.org/dl/go1.27.3@latest && go1.27.3 download
```

依赖版本下限：go-sdk v1.4.1、x/sys v0.44.0、segmentio/encoding v0.5.4。

依赖检查：

```sh
go mod verify
govulncheck ./...                 # go install golang.org/x/vuln/cmd/govulncheck@latest
```

基线输出 `No vulnerabilities found.`。

## 2. 源码结构

```text
cmd/platform/ cmd/relay/ cmd/yiuk-mcp/ cmd/smoke/ cmd/device-sim/

internal/platform/            auth config device session task transfer protocol transport audit
internal/platform/server/     server.go conn.go routing.go janitor.go handler.go
                              handler_session.go handler_command.go handler_download.go
                              errors.go convert.go
internal/platformclient/      client.go config.go transport.go cache.go
                              tools_device.go tools_session.go tools_command.go tools_file.go
internal/agenttools/          工具契约
internal/mcpadapter/          MCP stdio
internal/room/ control/ server/ protocol/   Minecraft relay

configs/                      平台与 relay 配置模板
deploy/                       服务器安装脚本与 systemd 单元
ops/server/                   relay 运维 agent（见 ops/README.md）
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
activity.cpp                活动面板入口：选择当前会话并同步给子进程
activitypanel.{h,cpp}       面板子进程的启动、关闭与 stdin 协议
panel/                      yiuk-activity-panel：原生 Win32 分层窗口（不依赖 Qt）
powershellrunner.{h,cpp}    PowerShell 任务执行
apphelpers glyph titlebar   控件工厂、路径、图标、标题栏
platformclient localbridge securestore   协议客户端、本机 bridge、DPAPI
```

## 3. 构建

```sh
go build -o platform ./cmd/platform
go build -o relay ./cmd/relay
go build -o ops-agent ./ops/server/cmd/ops-agent
```

```sh
cmake -S qt-client -B qt-client/build-qt -G Ninja -DCMAKE_BUILD_TYPE=Release
cmake --build qt-client/build-qt
```

Qt 构建会顺带 `go build ./cmd/yiuk-mcp`，把 `yiuk-mcp.exe` 放到构建目录；`go` 不在 `PATH` 时 CMake 只告警，不生成该文件。

产物：

```text
qt-client/build-qt/yiuk-device-assist.exe    主客户端（Qt）
qt-client/build-qt/yiuk-activity-panel.exe   活动面板（纯 Win32，无 Qt 依赖）
qt-client/build-qt/yiuk-mcp.exe              MCP stdio 适配器（Go）
```

运行时链接 Qt：直接跑构建产物需要把 `Qt6/bin` 加进 `PATH`，或按 §5 打包。

## 4. 测试

```sh
go test ./...
go test -race ./internal/platform/... ./internal/platformclient/...
```

Qt↔Go 互操作（需先启动平台服务）：

```sh
cmake -S qt-client -B qt-client/build-qt -G Ninja -DYIUK_BUILD_TESTS=ON
cmake --build qt-client/build-qt
YIUK_PLATFORM_ADDRESS=127.0.0.1:25664 \
YIUK_PLATFORM_PASSWORD=<player-password> \
./qt-client/build-qt/yiuk-platform-interop
```

覆盖认证、注册、申请、批准、`execute_command`、输出与结果回传、关闭会话。

命令行联调：

```sh
go run ./cmd/smoke -addr host:29564 -password '<player-password>' -ca server.crt -mode approval
go run ./cmd/smoke -addr host:29564 -password '<player-password>' -ca server.crt -mode direct
go run ./cmd/device-sim -addr host:29564 -password '<player-password>' \
  -device-id 111222333 -temporary-password '<32+ chars>' -ca server.crt -mode approval
```

`smoke` 覆盖认证、注册、申请、批准、执行、取结果、关闭，以及跨连接拒绝、错误密码拒绝、已关闭会话拒绝。`device-sim` 自动批准会话并返回固定结果，不执行真实命令；之后用 MCP `run_command` 指向该 `device_id` 与临时密码验证链路。

## 5. 打包发布

只有一个 Windows x64 绿色目录（无安装器），目录里放三个 exe 与 Qt 运行库，再压 zip：

```sh
VERSION=0.1.3
BUILD=qt-client/build-qt
OUT=release/YiukDeviceAssist-$VERSION-win64

cmake --build $BUILD                       # 三个 exe 都在 $BUILD 里
mkdir -p $OUT                              # PowerShell: New-Item -ItemType Directory -Force $OUT
cp $BUILD/yiuk-device-assist.exe $OUT
cp $BUILD/yiuk-activity-panel.exe $OUT
cp $BUILD/yiuk-mcp.exe $OUT
windeployqt --release --no-translations $OUT/yiuk-device-assist.exe
```

- 只对主客户端跑 `windeployqt`：`yiuk-activity-panel.exe` 只依赖系统 DLL，`yiuk-mcp.exe` 是纯 Go 静态产物。
- `windeployqt` 会一并带上 `opengl32sw.dll`、`D3Dcompiler_47.dll` 与 MinGW 运行库（Qt 的软件 OpenGL 回退，在不装显卡驱动的机器上更稳）。0.1.3 包为 24 个文件、解压约 77 MB、zip 约 32 MB。
- 版本号来自 `qt-client/CMakeLists.txt` 的 `project(... VERSION ...)`，会显示在窗口标题与「关于」信息里；改版本必须重新构建。
- 打包后检查：目录里同时有 `Qt6Core/Gui/Widgets/Network.dll`、`platforms/qwindows.dll` 与三个 exe；在**不把 Qt 加进 `PATH`** 的情况下双击 `yiuk-device-assist.exe` 能启动，并确认窗口标题里的版本号正确。
- 再验一次面板：启动客户端后从托盘菜单打开活动面板（或直接向 `yiuk-activity-panel.exe` 的 stdin 馈协议行，见[本机开发环境](local-dev.md)）；缺少面板时会提示找不到 `yiuk-activity-panel.exe`。

压缩与校验（PowerShell）：

```powershell
Compress-Archive -Path $OUT -DestinationPath "release/YiukDeviceAssist-$VERSION-win64.zip" -Force
(Get-FileHash "release/YiukDeviceAssist-$VERSION-win64.zip" -Algorithm SHA256).Hash.ToLower() +
  "  YiukDeviceAssist-$VERSION-win64.zip" | Set-Content "release/YiukDeviceAssist-$VERSION-win64.zip.sha256"
```

发布说明写在 `docs/release-notes-<version>.txt`，随包说明启动步骤、服务端地址与密码填写位置、自签名证书与 Agent 配置的入口。

## 6. 服务器部署

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

## 7. 客户端配置与证书

平台配置：`%LOCALAPPDATA%/Yiuk/DeviceAssist/platform-client.json`，兼容读取 `%APPDATA%/Yiuk/DeviceAssist`。

自签名证书：

1. 设置页填写服务器地址（部署脚本默认 `:29564`，`configs/platform.yaml` 为 `:25664`）。
2. 首次连接显示证书名称与 SHA-256 指纹。
3. 选择「信任此证书」后固定指纹。
4. 更换地址清除旧指纹；证书变更显示「证书已变更」警告。

指纹同步到 MCP 配置的 `pinned_cert_sha256`。

## 8. Agent MCP 配置

发布目录含 `yiuk-device-assist.exe`、`yiuk-activity-panel.exe`、`yiuk-mcp.exe`。**设置 → Agent 集成 → 复制 Agent 配置** 生成并复制配置：

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

远端文件：先取得带 `read_file` 权限的已批准会话，再 `download_file(session_id, remote_path, destination_path?)`。未指定路径时写入 `Yiuk/DeviceAssist/downloads`；显式路径须为绝对路径且不覆盖已有文件。分块 64 KiB，上限 32 MiB，SHA-256 校验。

## 9. 配置与安全约定

- 不提交真实密码、TLS 私钥或其他凭证。
- 平台服务完全内存化，重启后临时密码、会话、任务全部失效。
- 平台 `player_password` 与 Minecraft `host_password` / `player_password` 使用不同随机强密码。
- 管理端口由防火墙限制为可信运维终端。
- 生产环境使用 TLS 并校验证书，`allow_plaintext` / `allow_insecure` 仅本机测试。

## 10. 提交前检查

不入库：`platform-client.json`、`local-api.json`、`platform.json`、`sessions.json`、`device.json`、`tmp-*` 调试文件、TLS 抓包、证书导出，以及含 `player_password` / `temporary_password` / bridge token 的配置副本。

```sh
git status --short
git diff --cached | grep -nE "password|token|secret|PRIVATE KEY" || true
```

密码已进入提交时，轮换密码并用 `git filter-repo` 重写历史。
