# 开发与构建

## 1. 环境

- Go 1.27.3+（`go.mod` 的 `go` 指令；低于时由 `GOTOOLCHAIN=auto` 自动下载）
- CMake 3.21+、Ninja、Qt 6.4+（`Widgets`、`Network`）
- Windows 10/11 x64

```sh
go mod verify
govulncheck ./...        # go install golang.org/x/vuln/cmd/govulncheck@latest
```

## 2. 源码结构

```text
cmd/dev-redis/               本机假 Redis（miniredis），只为本地起服务
cmd/dev-routes/              打印 Redis 里的路由表（agentremote:*）
cmd/platform-core/           业务进程：只处理业务，不碰客户端 socket
cmd/platform-gateway/        接入进程：只管 socket，把帧原样转给 core
cmd/yiuk-mcp/ cmd/smoke/ cmd/device-sim/ cmd/smoke-identity/

internal/platform/core/       业务：service.go caller.go link.go delivery.go janitor.go
                              handler.go handler_session.go handler_command.go
                              handler_download.go handler_upload.go handler_question.go
                              errors.go convert.go
internal/platform/gateway/    接入：server.go（监听、连接表、投递） conn.go（读写、队列、超时）
                              routes.go（路由登记）
internal/platform/registry/   路由表：网关存活、连接归属、设备路由（Redis，带 TTL）
internal/platform/edge/       core ↔ gateway 的 gRPC 契约与两端适配（edgepb/ 为生成代码）
internal/platform/            auth config device session task transfer jobs protocol
                              transport audit logging question
internal/platformclient/      client.go config.go transport.go cache.go
                              tools_device.go tools_session.go tools_command.go
                              tools_download.go tools_upload.go tools_question.go
internal/agenttools/          工具契约：catalog.go 是目录与校验的唯一事实来源
                              desktop.go 组合工具骨架 + desktop_state.go 截屏状态
                              desktop_screenshot.go desktop_click.go desktop_input.go
                              file_ops.go wait.go scripts.go（内嵌 PowerShell 原文）text.go
internal/mcpadapter/          MCP stdio
internal/localbridge/         本机 Qt bridge 客户端（备用后端）
internal/userconfig/          客户端配置路径

configs/                      平台配置模板
deploy/                       服务器安装脚本与 systemd 单元
```

`core` 不认识 socket，`gateway` 不认识业务，两边只靠一个接口说话：

```go
// core 唯一出口：生产走 gRPC Push，测试里是进程内直连
type Link interface {
    Deliver(connectionID string, frame []byte) error
    Alive(connectionID string) bool
}
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
questions.cpp               询问框接线
activity.cpp                活动面板入口：选择当前会话并同步给子进程
activitypanel.{h,cpp}       面板子进程的启动、关闭与 stdin 协议
panel/                      yiuk-activity-panel：原生 Win32 分层窗口（不依赖 Qt）
vision/                     yiuk-vision 静态库：screengrab（GDI 抓屏/布局/缩放/编码）、
                            snapshotstore（screenshot_id 与临时文件）、sessionlimiter
                            （会话级互斥 + 500ms）、devicegate（进程级抓屏/输入互斥）、
                            visionservice（原生抓屏唯一入口 + 临时文件回收）
input/                      yiuk-input 静态库：SendInput 绝对坐标、确定性 ease-in-out 轨迹、
                            按键安全释放（ButtonGuard）、点击前后的局部变化采样
powershellrunner.{h,cpp}    PowerShell 任务执行
apphelpers glyph titlebar   控件工厂、路径、图标、标题栏
platformclient localbridge securestore   协议客户端、本机 bridge、DPAPI
```

## 3. 构建

```sh
go build -o platform-core ./cmd/platform-core       # 业务进程
go build -o platform-gateway ./cmd/platform-gateway # 接入进程
```

两个进程都要连 Redis，两个 gRPC 端口（`core_grpc_listen` / `gateway_grpc_listen`）只在回环或内网互通。

改过 `internal/platform/edge/edgepb/edge.proto` 之后重新生成（`buf`、`protoc-gen-go`、`protoc-gen-go-grpc` 需在 `PATH`，不需要装 `protoc`）：

```sh
cd internal/platform/edge/edgepb
buf generate --template buf.gen.yaml .
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

视觉/OCR 的 P0 基线（不注入键鼠、不需要平台）：

```sh
cmake -S spike/vision -B spike/vision/build -G Ninja -DCMAKE_BUILD_TYPE=Release
cmake --build spike/vision/build
spike/vision/build/make_fixtures.exe spike/vision/out/fixtures
spike/vision/build/vision_selftest.exe          # NCC 参考实现一致性
spike/vision/build/vision_bench.exe --manifest spike/vision/out/fixtures/manifest.json --out spike/vision/out/p0.json
spike/vision/build/probe_uia.exe                # UIA COM 耗时
spike/vision/build/probe_screen.exe 20          # GDI 抓屏/灰度/缩放耗时
spike/vision/build/probe_winrt_ocr.exe          # OCR 语言包与识别耗时
```

细节、实测数据与已知工具链坑见 [`spike/vision/README.md`](../spike/vision/README.md)。
MinGW 下调用 WinRT/UIA 需要 `-fno-devirtualize`（见[本机开发环境](local-dev.md) §6）。

Qt 原生操作分发器与视觉内核（不需要平台，控制台运行）：

```sh
qt-client/build-qt/yiuk-nativeops-test.exe      # 能力/信封/取消/超时/超大结果/抓屏几何
qt-client/build-qt/yiuk-vision-test.exe         # 快照回收/坐标基准/节流/缩放/编码 + 一次真实抓屏
qt-client/build-qt/yiuk-input-test.exe          # 轨迹端点/单调性/可复现、变化判据、采样区域
qt-client/build-qt/yiuk-click-target-test.exe   # 真机点击验收（裸 Win32 fixture）：不点/歧义/指定候选/等待超时
qt-client/build-qt/yiuk-input-fixture.exe       # 真机验收目标窗口（左半区点击/右半区悬停）
```

真机输入验收（会真的动鼠标）：fixture 窗口 + 平台 + `YIUK_INTEROP_INPUT=1` 的互操作程序，
fixture 打印 `FIXTURE OK clicks=1 hover=1 ...` 才算通过。加 `YIUK_INTEROP_DETECT_CHANGE=1`
（fixture 用 `--expect-clicks 4`）再验证 `detect_change` 的真机判定。
完整命令见[本机开发环境](local-dev.md) §5.4；`click_target` 的独立验收见 §5.5。

一次跑完全部 Qt 测试（CI 用的就是这条）：

```sh
cd qt-client/build-qt && ctest --output-on-failure
```

GitHub Actions 的 `windows-qt` job 会装 Qt 6.11.1（MinGW 13.1 工具链）、按版本号缓存
OpenCV 4.12.0 的静态构建，然后 `cmake -DYIUK_BUILD_TESTS=ON` + `ctest`。
真机项（`yiuk-click-target-test`）在 runner 上没有前台窗口，会打印 `SKIP` 并返回 2，
CTest 里记为 SKIP 而不是失败。

`take_screenshot` 有两条实现路径：设备客户端上报 `screen.capture` 能力时走原生
（客户端抓屏 + 落盘，MCP 用既有 `download_file` 取图，结果里 `capture_source=native`），
否则回退到 `scripts.go` 里的 PowerShell 抓屏（`capture_source=script`）。原生路径的临时截图
落在设备 `%LOCALAPPDATA%/Yiuk/DeviceAssist/screenshots`：MCP 读完图片后调用原生操作
`screen.release` 删掉这一份（失败只影响磁盘占用），另有"每会话最多 5 张 / 会话结束全删 /
每次抓屏清掉 5 分钟前的残留"三重兜底。

Qt↔Go 互操作（需先启动平台服务）：

```sh
cmake -S qt-client -B qt-client/build-qt -G Ninja -DYIUK_BUILD_TESTS=ON
cmake --build qt-client/build-qt
YIUK_PLATFORM_ADDRESS=127.0.0.1:25664 \
YIUK_PLATFORM_PASSWORD=<player-password> \
./qt-client/build-qt/yiuk-platform-interop
```

命令行联调：

```sh
go run ./cmd/smoke -addr host:29564 -password '<player-password>' -ca server.crt
go run ./cmd/device-sim -addr host:29564 -password '<player-password>' \
  -device-id 111222333 -temporary-password '<32+ chars>' -ca server.crt
```

`smoke` 覆盖认证、注册、两条授权路径（带密码直接批准 / 不带密码转审批）、执行、取结果、关闭，以及跨连接拒绝、错误密码拒绝、已关闭会话拒绝。`device-sim` 自动批准会话并返回固定结果，不执行真实命令；之后用 MCP `run_command` 指向该 `device_id` 与临时密码验证链路。

## 5. 打包发布

只有一个 Windows x64 绿色目录（无安装器），目录里放三个 exe 与 Qt 运行库，再压 zip：

```sh
VERSION=0.1.9
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
- `windeployqt` 会一并带上 `opengl32sw.dll`、`D3Dcompiler_47.dll` 与 MinGW 运行库。
- 版本号来自 `qt-client/CMakeLists.txt` 的 `project(... VERSION ...)`：窗口标题、「关于」、两个 EXE 的文件版本（由 `resources/app-version.rc.in` 生成）都取自这一处；改版本必须重新构建。
- 打包后检查：目录里同时有 `Qt6Core/Gui/Widgets/Network.dll`、`platforms/qwindows.dll` 与三个 exe；在**不把 Qt 加进 `PATH`** 的情况下双击 `yiuk-device-assist.exe` 能启动，窗口标题版本号正确。
- 再验一次面板：启动客户端后从托盘菜单打开活动面板（或直接向 `yiuk-activity-panel.exe` 的 stdin 馈协议行，见[本机开发环境](local-dev.md)）。

压缩与校验（PowerShell）：

```powershell
Compress-Archive -Path $OUT -DestinationPath "release/YiukDeviceAssist-$VERSION-win64.zip" -Force
(Get-FileHash "release/YiukDeviceAssist-$VERSION-win64.zip" -Algorithm SHA256).Hash.ToLower() +
  "  YiukDeviceAssist-$VERSION-win64.zip" | Set-Content "release/YiukDeviceAssist-$VERSION-win64.zip.sha256"
```

可选安装包（绿色 zip 之外），脚本在 `qt-client/installer/YiukDeviceAssist.iss`：

```powershell
# 源目录会自动拼成 Release\YiukDeviceAssist-<MyAppVersion>-win64，
# 产物落在 Release\setup\YiukDeviceAssist-<version>-setup.exe
ISCC.exe qt-client\installer\YiukDeviceAssist.iss
ISCC.exe /O"E:\缓存\setup" qt-client\installer\YiukDeviceAssist.iss   # 换个输出位置
```

- 改版本时同时改 `qt-client/CMakeLists.txt` 的 `project(... VERSION ...)` 和脚本里的 `MyAppVersion`。
- 安装包只在绿色目录已经生成后才能编译；卸载**不删** `%LOCALAPPDATA%\Yiuk\DeviceAssist`，重装沿用同一台设备身份。
- 未做代码签名，下载后 SmartScreen 可能提示。

## 6. 服务器部署

```sh
sudo ./deploy/install-platform.sh /path/to/server.crt /path/to/server.key
```

脚本：构建 `cmd/platform-core` 与 `cmd/platform-gateway` 到 `/opt/yiuk-platform`；证书复制到 `certs`（私钥 `600`）；首次运行生成并打印 `player_password`；安装并启动 `yiuk-platform-core.service` 与 `yiuk-platform-gateway.service`（`Restart=always`）。已存在 `platform.yaml` 时不覆盖。

`player_password` 最少 8 字符，少于 16 字符启动告警。WSS 监听 `:29564`，端口可按配置修改。需放行安全组；验证 TLS：

```sh
openssl s_client -connect <host>:29564 -CAfile <server.crt> </dev/null | head
```

```sh
systemctl status yiuk-platform-core yiuk-platform-gateway
journalctl -u yiuk-platform-core -u yiuk-platform-gateway -f
tail -f /opt/yiuk-platform/platform.log
systemctl restart yiuk-platform-core yiuk-platform-gateway
```

日志只记录脱敏 ID、状态与错误码。

### 6.1 两进程（core + gateway）

两个进程读**同一份** `platform.yaml`，各自只取自己那一侧：

```yaml
platform:
  listen: ":29564"              # 客户端连这里（仅 gateway）
  tls_cert / tls_key            # 仅 gateway
  player_password: "..."        # 仅 core 使用
  core_grpc_listen: "127.0.0.1:25670"     # core 监听，提供 Edge 服务
  gateway_grpc_listen: "127.0.0.1:25671"  # gateway 监听，提供 Push 服务
  core_grpc_addr: "127.0.0.1:25670"       # gateway 连 core
  gateway_grpc_addr: "127.0.0.1:25671"    # core 连 gateway
```

```sh
./platform-core    -config platform.yaml   # 只有业务，没有对外端口
./platform-gateway -config platform.yaml   # 对外 WSS，把帧原样转给 core
```

- 两个 gRPC 地址都是内网明文，只监听回环或容器内网，不要对外暴露。
- 连接表只在 gateway；core 判断“设备连接还在不在”时通过 gRPC 问 gateway（`Alive`）。
- gateway 重启会中断它名下的连接；正常退出时会给 core 发 `Detach`，core 按规则收敛会话。
- gateway 被强杀（收不到 `Detach`）时，它名下的会话靠 core 的过期清理收敛，最长一个 `session_timeout_sec`。
- 连接归属写在 Redis，gateway 每 `route_ttl_sec / 3` 续期一次。

### 6.2 多网关（同一台机器，DNAT 分流）

两个网关各用一份配置，只差四项：

| 配置项 | gw-1 | gw-2 |
| --- | --- | --- |
| `gateway_id` | `gw-1` | `gw-2` |
| `listen`（客户端 WSS） | `127.0.0.1:29565` | `127.0.0.1:29566` |
| `gateway_grpc_listen` | `127.0.0.1:25671` | `127.0.0.1:25672` |
| `gateway_grpc_addr`（core 用它投递） | `127.0.0.1:25671` | `127.0.0.1:25672` |

`core`、Redis 不变，`core_grpc_addr` 仍指向同一个 core。

```sh
./platform-gateway -config gateway-1.yaml
./platform-gateway -config gateway-2.yaml
```

分流用内核 DNAT 轮询，不用 Nginx、不做健康检查：

```sh
iptables -t nat -A PREROUTING -p tcp --dport 29564     -m statistic --mode nth --every 2 --packet 0 -j DNAT --to-destination 127.0.0.1:29565
iptables -t nat -A PREROUTING -p tcp --dport 29564 -j DNAT --to-destination 127.0.0.1:29566
iptables -t nat -A POSTROUTING -p tcp -s 127.0.0.1 --sport 29565:29566 -j MASQUERADE
```

- 源 IP 天然保留：按 IP 限流、审计 `remote`、会话里的“操作者设备”都不受影响。
- 只影响新连接；已建立的长连接一直留在原网关。
- 网关挂了，新连接仍可能被分过去；客户端连不上就重连（指数退避，最长 30 秒），不自动摘除节点。
- 看每条连接落在哪个网关：`go run ./cmd/dev-routes`。

验证跨网关：`device-sim` 连 `29566`，另一个客户端连 `29565`，命令与文件双向跑通；停掉设备所在网关，操作者应在一次往返内收到会话中断。

### 6.3 容器（Docker，不用 compose）

一个镜像两个角色，用 `YIUK_BINARY` 选：`yiuk-platform-gateway`（默认，对外 WSS）与 `yiuk-platform-core`（只有业务）。两者都要能读到同一个 Redis，gateway 要能连到 core 的 gRPC 地址。

```sh
docker network create yiuk || true
docker build -t yiuk-platform:local .

# Redis：只放路由，丢了只是路由失效，不丢授权。不挂卷。
docker run -d --name yiuk-redis --network yiuk --restart unless-stopped redis:7-alpine redis-server --save "" --appendonly no --maxmemory 64mb --maxmemory-policy allkeys-lru

# core：没有对外端口
docker run -d --name yiuk-core --network yiuk --restart unless-stopped -e YIUK_BINARY=yiuk-platform-core -e YIUK_PLAYER_PASSWORD='<16 位以上随机串>' -e YIUK_REDIS_ADDR=yiuk-redis:6379 -e YIUK_CORE_GRPC_LISTEN=:25670 -e YIUK_GATEWAY_GRPC_ADDR=yiuk-gateway:25671 yiuk-platform:local

# gateway：对外 29564，需要证书
docker run -d --name yiuk-gateway --network yiuk --restart unless-stopped -e YIUK_BINARY=yiuk-platform-gateway -e YIUK_PLAYER_PASSWORD='<同一个密码>' -e YIUK_REDIS_ADDR=yiuk-redis:6379 -e YIUK_GATEWAY_ID=gw-1 -e YIUK_CORE_GRPC_ADDR=yiuk-core:25670 -e YIUK_GATEWAY_GRPC_LISTEN=:25671 -e YIUK_GATEWAY_GRPC_ADDR=yiuk-gateway:25671 -e YIUK_TLS_SELF_SIGNED=true -p 29564:29564 -v yiuk-platform-data:/var/lib/yiuk-platform yiuk-platform:local
```

- 两个 gRPC 端口（25670 / 25671）只在 `yiuk` 网络内互通，不要 `-p` 暴露。
- `YIUK_GATEWAY_GRPC_ADDR` 是 core 用来投递的地址，必须是 gateway 在容器网络里的名字。
- 多网关时每个 gateway 用不同的 `YIUK_GATEWAY_ID` 和 `YIUK_GATEWAY_GRPC_ADDR`。
- core 不监听客户端，入口脚本会跳过证书生成；给它挂载证书没有意义。

配置有两种来源：

1. **环境变量**（默认）：启动时把 `platform.yaml` 渲染到 `/var/lib/yiuk-platform/platform.yaml`，每次重启按当前变量重写。只写基础字段，其余限制走内置默认值。
2. **挂载配置**（`-e YIUK_CONFIG=/etc/yiuk/platform.yaml -v <file>:/etc/yiuk/platform.yaml:ro`）：文件原样使用，入口脚本不写盘；路径不存在直接报错退出。要改 `max_*`、各类 TTL 等默认值就走这条路，模板见 `configs/platform.yaml`。

| 变量 | 默认 | 说明 |
| --- | --- | --- |
| `YIUK_BINARY` | `yiuk-platform-gateway` | 跑哪个角色 |
| `YIUK_PLAYER_PASSWORD` | 无 | 玩家密码；缺失且无配置文件时拒绝启动 |
| `YIUK_PLAYER_PASSWORD_FILE` | 无 | 从文件读密码（docker secret），优先于上一项 |
| `YIUK_LISTEN` | `:29564` | gateway 的 WSS 监听地址 |
| `YIUK_TLS_CERT` / `YIUK_TLS_KEY` | `/var/lib/yiuk-platform/certs/server.crt` / `.key` | 证书与私钥（只 gateway 用） |
| `YIUK_ALLOW_INSECURE` | `false` | 明文监听，仅限本地测试 |
| `YIUK_TLS_SELF_SIGNED` | `false` | 证书缺失时生成一次性自签证书 |
| `YIUK_TLS_HOST` | `yiuk-platform` | 自签证书的 CN 与 SAN（另含 `127.0.0.1`） |
| `YIUK_TLS_SELF_SIGNED_DAYS` | `825` | 自签有效期（天） |
| `YIUK_LOG_FILE` | `/var/lib/yiuk-platform/platform.log` | 日志文件 |
| `YIUK_CORE_GRPC_LISTEN` / `YIUK_GATEWAY_GRPC_LISTEN` | `:25670` / `:25671` | 两个进程各自的内网 gRPC 监听 |
| `YIUK_CORE_GRPC_ADDR` / `YIUK_GATEWAY_GRPC_ADDR` | `127.0.0.1:25670` / `127.0.0.1:25671` | 对方地址 |
| `YIUK_GATEWAY_ID` | `gw-1` | 网关身份，写进 Redis 路由 |
| `YIUK_REDIS_ADDR` | `127.0.0.1:6379` | Redis 地址 |
| `YIUK_REDIS_PASSWORD` / `YIUK_REDIS_DB` | 空 / `0` | Redis 认证与库号 |
| `YIUK_ROUTE_TTL_SEC` | `30` | 路由键 TTL，网关每 1/3 周期续期 |
| `YIUK_HEALTHCHECK_PORT` | `29564` | 镜像内置 `HEALTHCHECK` 探测端口；core 容器设成 `25670` |

- 生产用真实证书：把 `server.crt` / `server.key` 挂进卷的 `certs/`，并设 `YIUK_TLS_SELF_SIGNED=false`；私钥在容器内为 `600`、归属 `10001`。
- 卷用命名卷：bind mount 的主机目录属主必须是 `10001`。
- 平台状态全在内存：core 重启即清空在线设备、临时密码、会话与任务，客户端需重连。
- 自签证书要在客户端「设置」页按 SHA-256 指纹信任；换证书后客户端会提示「证书已变更」。

## 7. 客户端配置与证书

平台配置：**exe 同级的 `data/platform-client.json`**（绿色版，跟着程序目录走）；`data/` 不可写时才回退 `%LOCALAPPDATA%/Yiuk/DeviceAssist`。升级时会从旧位置复制身份与配置一次，之后旧目录只读不写。MCP（`yiuk-mcp.exe`）用同一套规则：`<exe>/data/` 优先，旧路径作为回退读取。

自签名证书：设置页填服务器地址（部署脚本默认 `:29564`，`configs/platform.yaml` 为 `:25664`）→ 首次连接显示证书名称与 SHA-256 指纹 → 「信任此证书」后固定指纹。更换地址清除旧指纹；证书变更显示警告。指纹同步到 MCP 配置的 `pinned_cert_sha256`。

## 8. Agent MCP 配置

发布目录含 `yiuk-device-assist.exe`、`yiuk-activity-panel.exe`、`yiuk-mcp.exe`。**设置 → Agent 集成 → 复制 Agent 配置** 生成并复制：

```json
{
  "mcpServers": {
    "agentremote": {
      "command": "C:/path/to/YiukDeviceAssist/yiuk-mcp.exe",
      "args": []
    }
  }
}
```

远端文件：先取得一个已批准的会话，再 `download_file(session_id, remote_path, destination_path?)`。未指定路径时写入 `Yiuk/DeviceAssist/downloads`；显式路径须为绝对路径且不覆盖已有文件。分块 64 KiB，上限 32 MiB，SHA-256 校验。

## 9. 配置与安全约定

- 不提交真实密码、TLS 私钥或其他凭证。
- 平台服务完全内存化，重启后临时密码、会话、任务全部失效。
- 原生截图临时文件由客户端回收（每会话最多 5 张、会话结束全删），服务端不记录截图路径。
- 终态的任务与传输记录按 `terminal_record_retain_sec`（默认 900 秒）回收，保留输出总量受 `terminal_output_retain_bytes`（默认 64 MiB）限制；回收后 `get_command_result` 对老任务返回 `TASK_NOT_FOUND`。会话记录不回收，关闭后仍可查状态。
- 每套部署使用独立的随机 `player_password`，不要与其他服务复用。
- 管理端口由防火墙限制为可信运维终端。
- 生产环境使用 TLS 并校验证书，明文模式仅本机测试。

## 10. 提交前检查

不入库：`platform-client.json`、`local-api.json`、`platform.json`、`sessions.json`、`device.json`、`tmp-*` 调试文件、TLS 抓包、证书导出，以及含 `player_password` / `temporary_password` / bridge token 的配置副本。

```sh
git status --short
git diff --cached | grep -nE "password|token|secret|PRIVATE KEY" || true

gofmt -l .            # 必须零输出；行尾由 .gitattributes 统一为 LF
go vet ./...
go test ./...
```

密码已进入提交时，轮换密码并用 `git filter-repo` 重写历史。

CI（`.github/workflows/ci.yml`）会再跑一遍 build / vet / test / gofmt，并产出 linux/amd64 二进制——不要在 2C2G 的服务器上编译。
