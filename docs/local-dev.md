# 本机开发环境（仅限本机）

> **仅限本机开发环境。** 本文记录这台机器上具体的工具链路径、快捷命令、本机数据目录和踩过的坑，用来少走路；通用环境要求与跨机器流程以[开发与构建](development.md)为准。换机器时请从那份文档开始。


## 1. 这台机器上有什么

| 组件 | 位置 | 版本 |
| --- | --- | --- |
| 仓库 | `E:\Yiuk_AgentRemote` | |
| Git | `E:\Git\cmd\git.exe` | |
| Go | 已在 `PATH` | go1.27.3 windows/amd64 |
| CMake | `C:\Program Files\CMake\bin\cmake.exe`（另有 `C:\msys64\ucrt64\bin\cmake.exe`） | 4.3.1 |
| Ninja | `D:\QT\Tools\Ninja\ninja.exe` | |
| Qt | `D:\QT\6.11.1\mingw_64` | Qt 6.11.1 |
| MinGW | `D:\QT\Tools\mingw1310_64\bin\g++.exe` | 13.1.0 posix-seh |
| windeployqt | `D:\QT\6.11.1\mingw_64\bin\windeployqt.exe` | |
| `rg` | `C:\Users\34345\.pi\agent\bin\rg.exe`（pi 自带） | |
| coreutils | 已修：`~/.pi/agent/bin` 的包装脚本转发到 `C:\msys64\usr\bin`（`node ~/.pi/agent/fix-bash-coreutils.mjs` 可重建） | |
| 已有构建目录 | `E:\Yiuk_AgentRemote\qt-client\build-qt`（Ninja + Release，已配置好） | |

主机：Windows 10/11 x64，系统语言中文（代码页 936），显示 2560×1600 @125%。

## 2. 构建（日常一条命令）

```powershell
& "D:\QT\Tools\Ninja\ninja.exe" -C E:\Yiuk_AgentRemote\qt-client\build-qt
```

只构建某个目标（例如只改面板）：

```powershell
& "D:\QT\Tools\Ninja\ninja.exe" -C E:\Yiuk_AgentRemote\qt-client\build-qt yiuk-activity-panel
```

`build-qt` 被删或换机器时重新生成（本机用过的完整参数）：

```powershell
& "C:\Program Files\CMake\bin\cmake.exe" -S E:\Yiuk_AgentRemote\qt-client `
    -B E:\Yiuk_AgentRemote\qt-client\build-qt -G Ninja `
    -DCMAKE_BUILD_TYPE=Release `
    -DCMAKE_PREFIX_PATH=D:/QT/6.11.1/mingw_64 `
    -DCMAKE_CXX_COMPILER=D:/QT/Tools/mingw1310_64/bin/g++.exe `
    -DCMAKE_MAKE_PROGRAM=D:/QT/Tools/Ninja/ninja.exe
```

要跑 Qt↔Go 互操作测试再加 `-DYIUK_BUILD_TESTS=ON`（会额外生成 `yiuk-platform-interop.exe`）。

产物都在 `qt-client\build-qt`：`yiuk-device-assist.exe`、`yiuk-activity-panel.exe`、`yiuk-mcp.exe`。

## 3. 运行与调试

Qt DLL 只装在 `D:\QT\6.11.1\mingw_64\bin`，直接跑构建产物要先补 `PATH`：

```powershell
$env:PATH = "D:\QT\6.11.1\mingw_64\bin;" + $env:PATH
E:\Yiuk_AgentRemote\qt-client\build-qt\yiuk-device-assist.exe
```

- 活动面板是独立进程，主程序「查看活动」或托盘菜单才会拉起；单独调试见 §5.3。
- 想验证「客户端退出后面板自动退出」：结束 `yiuk-device-assist.exe`，`yiuk-activity-panel.exe` 会跟着退出（stdin 关闭）。
- 窗口标题里的版本号来自 `qt-client/CMakeLists.txt` 的 `project(... VERSION ...)`。

## 4. 本机数据、端口与证书

客户端数据目录（绿色版：跟着 exe 走，`data\` 与 `yiuk-device-assist.exe` 同级）：

```text
<exe 目录>\data\
  platform-client.json    MCP 侧平台配置（地址、固定证书指纹）
  platform.json           客户端平台凭证（DPAPI）
  local-api.json          本机 bridge token
  sessions.json           会话与任务历史（删掉可复位「会话记录」页）
  activity-panel.state    活动面板位置尺寸（删掉面板回到屏幕居中默认大小）
  downloads\              远端文件默认落盘目录
  screenshots\            原生抓屏的临时截图（每会话最多 5 张，会话结束自动删除；只有最近一张保留像素）
  templates\              save_template 的模板 PNG + index.json（上限 100 个 / 32 MiB；删掉即清理）
  tmp\                    PowerShell 脚本（每次任务一个 .ps1，执行完删除）
  work\                   命令的默认工作目录（相对路径落点）
  uploads\                远端上传到本机的文件
```

升级时会自动把旧的 `%LOCALAPPDATA%\Yiuk\DeviceAssist\`（以及更旧的 `%APPDATA%\`）里的身份与配置
**复制**到 `data\`，设备码不变；旧目录此后只读不写，确认没问题可以删掉。

端口与配置：

| 用途 | 端口 |
| --- | --- |
| gateway：客户端 WSS（本地开发） | `:25664`（`configs\platform.yaml`） |
| gateway：客户端 WSS（服务器默认） | `:29564`（`deploy\install-platform.sh`） |
| core：gRPC（gateway 连它） | `:25670` |
| gateway：gRPC（core 投递用） | `:25671` |
| Redis | `:6379` |

本机已有一份 TLS 联调用的自签证书：`tmp-tls\server.crt`、`tmp-tls\server.key`（`tmp-*` 与证书都在 `.gitignore` 里，不会进仓库）。

起本地平台服务要三个东西：Redis + core + gateway。注意 `platform.yaml` 里的证书是相对路径，
**gateway 必须在证书目录（`tmp-tls`）里运行**，否则报 `open server.crt: The system cannot find the file specified`
（`allow_insecure: true` 也绕不过：它只表示允许不校验证书，TLS 监听仍然要证书文件）。

```powershell
cd E:\Yiuk_AgentRemote

# 1) Redis：本机没有 Redis 时用仓库自带的假 Redis（miniredis，不持久化）
go run ./cmd/dev-redis              # 挂在 127.0.0.1:6379；有真 Redis 就不用跑

# 2) 同一份配置，两个进程各取自己那一侧（先把 player_password 换掉）
Copy-Item configs\platform.yaml tmp-tls\platform.yaml
# gateway 会读 server.crt/server.key（即使 allow_insecure: true 也要文件存在），必须从 tmp-tls 里跑：
openssl req -x509 -newkey rsa:2048 -nodes -keyout tmp-tls\server.key -out tmp-tls\server.crt -days 2 -subj "//CN=127.0.0.1"
go run ./cmd/platform-core    -config tmp-tls\platform.yaml
cd tmp-tls; go build -o platform-gateway.exe ..\cmd\platform-gateway; .\platform-gateway.exe -config platform.yaml; cd ..

# 3) 看路由表：网关存活键 + 每条连接的归属 + 设备路由
go run ./cmd/dev-routes
```

Redis 里应该能看到 `agentremote:gateway:gw-1`；客户端连上后会多出 `agentremote:route:conn:*`，设备注册后再多出 `agentremote:route:device:*`。

## 5. 快速自测（单机即可）

### 5.1 Qt↔Go 互操作

```powershell
$env:YIUK_PLATFORM_ADDRESS = "127.0.0.1:25664"
$env:YIUK_PLATFORM_PASSWORD = "<platform.yaml 的 player_password>"
$env:YIUK_PLATFORM_TLS = "1"
$env:YIUK_PLATFORM_CA = "E:\Yiuk_AgentRemote\tmp-tls\server.crt"
E:\Yiuk_AgentRemote\qt-client\build-qt\yiuk-platform-interop.exe
```

注意：`allow_insecure: true` 时网关**仍然只服务 TLS**（客户端发明文会得到
`client sent an HTTP request to an HTTPS server`），所以上面是 TLS + CA，不是明文。
它是 GUI 子系统的 exe，控制台看不到输出，**只看退出码**。

覆盖认证、注册、申请、批准、执行、结果回传、关闭会话。需要 `-DYIUK_BUILD_TESTS=ON` 的构建。

### 5.2 命令行链路

```powershell
go run ./cmd/smoke -addr 127.0.0.1:25664 -password '<player-password>' -ca tmp-tls/server.crt
go run ./cmd/device-sim -addr 127.0.0.1:25664 -password '<player-password>' `
  -device-id 111222333 -temporary-password '<32+ chars>' -ca tmp-tls/server.crt
```

### 5.3 活动面板（不连平台也能看效果）

面板只读 stdin，一行一条协议（格式见本节下方示例）。feed 用 UTF-8 保存，例如 `feed.log`：

```text
H|远程协助活动|Agent · 453 313 764|active|远程协助进行中
E|session|11:20:01|本机用户已批准 Agent 会话
E|exec|11:20:02|Agent 开始执行任务 77c1e0aa\nPowerShell> Get-ChildItem C:\Users
E|out|11:20:03|    Directory: C:\Users
E|file|11:20:11|Agent 请求下载文件 setup-notes.md（2048 字节）
E|ok|11:20:12|文件下载 77c1e0aa 完成：2048 字节
E|err|11:20:20|stderr: Get-Process : 找不到名为 "notepad" 的进程。
```

喂给面板（要保持 stdin 打开，面板才会停留）：

```powershell
$psi = New-Object Diagnostics.ProcessStartInfo
$psi.FileName = "E:\Yiuk_AgentRemote\qt-client\build-qt\yiuk-activity-panel.exe"
$psi.Arguments = '--title "远程协助活动" --label "Agent · 453 313 764" --state active --status "远程协助进行中"'
$psi.RedirectStandardInput = $true
$psi.UseShellExecute = $false
$p = [Diagnostics.Process]::Start($psi)
$w = New-Object IO.StreamWriter($p.StandardInput.BaseStream, (New-Object Text.UTF8Encoding $false))
$w.NewLine = "`n"
Get-Content feed.log -Encoding UTF8 | ForEach-Object { $w.WriteLine($_) }
$w.Flush()          # 不 Dispose：面板继续显示。收尾时 $w.Dispose() 或 Stop-Process -Name yiuk-activity-panel
```

截图核对（面板是普通层级的窗口，先置顶再抓屏）：

```powershell
Add-Type -Name W -Namespace N -MemberDefinition '[DllImport("user32.dll")] public static extern bool SetWindowPos(IntPtr h, IntPtr a, int x, int y, int cx, int cy, uint f);'
$h = (Get-Process yiuk-activity-panel).MainWindowHandle
[N.W]::SetWindowPos($h, [IntPtr]::new(-1), 0, 0, 0, 0, 0x0013) | Out-Null
```

### 5.4 原生输入真机验收（会真的动鼠标）

```powershell
E:\Yiuk_AgentRemote\qt-client\build-qt\yiuk-input-fixture.exe   # 置顶窗口：左半区等点击、右半区等悬停
# 按 §4 起 Redis + core + gateway 之后：
$env:YIUK_PLATFORM_ADDRESS  = "127.0.0.1:25664"
$env:YIUK_PLATFORM_PASSWORD = "<player-password>"
$env:YIUK_PLATFORM_TLS      = "1"
$env:YIUK_PLATFORM_CA       = "E:\Yiuk_AgentRemote\tmp-tls\server.crt"
$env:YIUK_INTEROP_NATIVE    = "1"
$env:YIUK_INTEROP_INPUT     = "1"
E:\Yiuk_AgentRemote\qt-client\build-qt\yiuk-platform-interop.exe
```

通过判据：fixture 打印 `FIXTURE OK clicks=1 hover=1 ...`，互操作程序退出码 0。
fixture 30 秒内没等到事件会打印 `FIXTURE FAIL` 并退出 1。

变化检测（`detect_change`）再加一个开关：fixture 用 `--expect-clicks 4` 启动，
并设置 `$env:YIUK_INTEROP_DETECT_CHANGE = "1"`。左半区会深/浅换色（应 `changed=true`），
右半区点击不改像素（应 `changed=false`）。

### 5.5 `click_target` 真机验收（会真的动鼠标）

```powershell
E:\Yiuk_AgentRemote\qt-client\build-qt\yiuk-click-target-test.exe   # 退出码 0 + CLICKTARGET OK
```

自己建一个裸 Win32 窗口（5 个按钮：1 个唯一标签、2 个同标签、2 个占位），
依次验收：未命中 `NOT_FOUND` 不点、同标签 `AMBIGUOUS_TARGET` 不点、`pick=index:2` 只点第二个、
`pick=best` 点唯一那个、落点在命中框内。不需要平台，也不需要客户端。
**要看得到桌面**：拿不到前台窗口时它打印 `SKIP ...` 并返回 2（不当失败）。

## 6. 本机已知坑

1. **Git Bash 没有 coreutils（已修）**：根因是 pi 的 `shellPath` 用 MSYS2 的 `bash.exe` 非登录启动，
   拿不到 `C:\msys64\usr\bin`。现在 `~/.pi/agent/bin` 里有一层包装脚本转发到同一个 MSYS 根，
   重建命令 `node ~/.pi/agent/fix-bash-coreutils.mjs`。**不要**把包装脚本指向别的 MSYS 根
   （`E:\Git\usr\bin` 会让带引号的通配符被提前展开）。仍缺 `xxd/patch/zip/tree/strings`。
2. **中文控制台乱码**：系统代码页 936，UTF-8 内容在 `cmd` / Git Bash 里会显示成乱码（文件本身没问题）。要看清先 `[Console]::OutputEncoding = [Text.Encoding]::UTF8`，或把输出重定向到文件再用编辑器打开。
3. **PowerShell 5.1 按 ANSI 读无 BOM 的 .ps1**：脚本里出现中文会变成乱码字符串，进而把参数传给子进程也一起坏。临时脚本要么只写 ASCII，要么存成 UTF-8 with BOM；给面板传中文参数建议走上面 §5.3 的写法。
4. **125% 缩放**：客户端与活动面板都是 Per-Monitor V2 DPI 感知，按物理像素工作；从 PowerShell（DPI 未感知）取到的窗口矩形是虚拟化坐标，`CopyFromScreen` 抓到的是 2048×1280 的重采样桌面，像素级核对要按 `×1.25` 换算回物理坐标。
5. **`*.exe` / `*.dll` / `*.log` / `*.ps1` / `tmp-*` / `/release/` 都在 `.gitignore` 里**：临时脚本别丢在仓库根目录，否则会被静默忽略。
6. **MinGW + WinRT/UIA 的虚调用坑**：GCC 13.1 在 `-O2/-O3` 会把“声明为纯虚的 WinRT/UIA 接口调用”
   反优化成 `__cxa_pure_virtual`，运行时报 `0xC0000005` 且栈看起来是坏的（gdb 只给一个模块基址）。
   给相关目标加 `-fno-devirtualize`，或按 vtable 索引显式调用；`spike/vision/src/probe_winrt_ocr.cpp`
   是可复现的例子。另外 Windows SDK 的 `winrt/*.h` **不能**直接在 MinGW 下编译
   （`Windows.Foundation.h` 与 `windows.foundation.h` 在大小写不敏感的文件系统上互相覆盖），
   需要自己按 ABI 声明接口，IID 与 vtable 顺序从 SDK 头里抄。
7. **视觉 P0 的复现目录**：`spike/vision/`（`out/`、`third_party/` 都不进仓库）。
   OpenCV 静态构建产物在 `spike/vision/third_party/install-opencv/x64/mingw/staticlib`，
   `-DOpenCV_DIR` 指向它即可；构建约 1.5 分钟。
8. **Qt 构建依赖 `go`**：`go` 不在 `PATH` 时 CMake 只告警，`yiuk-mcp.exe` 不会生成，发布包里就会缺文件。

## 7. 复位与清理

```powershell
# 复位活动面板窗口位置/大小
Remove-Item "$env:LOCALAPPDATA\Yiuk\DeviceAssist\activity-panel.state"

# 复位会话记录与连接状态
Remove-Item "$env:LOCALAPPDATA\Yiuk\DeviceAssist\sessions.json"

# 清掉原生抓屏残留的临时截图
Remove-Item "$env:LOCALAPPDATA\Yiuk\DeviceAssist\screenshots" -Recurse -Force -ErrorAction SilentlyContinue

# 结束残留进程
Get-Process yiuk-device-assist,yiuk-activity-panel,yiuk-mcp -ErrorAction SilentlyContinue | Stop-Process -Force
```

发布产物在 `release\`（不进仓库，见[打包发布](development.md#5-打包发布)）。

