# 本机开发环境（仅限本机）

> **仅限本机开发环境。** 本文记录这台机器上具体的工具链路径、快捷命令、本机数据目录和踩过的坑，用来少走路；通用环境要求与跨机器流程以[开发与构建](development.md)为准。换机器时请从那份文档开始。

最后核对：2026-10-22，工具链 Qt 6.11.1 / MinGW 13.1.0 / Go 1.27.3 / CMake 4.3.1。

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
| `rg` | `C:\Users\34345\.pi\agent\bin\rg.exe`（pi 自带，不在 Git Bash 的 coreutils 里） | |
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

客户端数据目录（新版）：

```text
%LOCALAPPDATA%\Yiuk\DeviceAssist\
  platform-client.json    MCP 侧平台配置（地址、固定证书指纹）
  platform.json           客户端平台凭证（DPAPI）
  local-api.json          本机 bridge token
  sessions.json           会话与任务历史（删掉可复位「会话记录」页）
  activity-panel.state    活动面板位置尺寸（删掉面板回到屏幕居中默认大小）
  downloads\              远端文件默认落盘目录
```

旧的 `%APPDATA%\Yiuk\DeviceAssist\` 仍会被读取，本机 `device.json`（设备码）还在那边：`{"device_id":"556604822"}`。

端口与配置：

| 用途 | 位置 | 端口 |
| --- | --- | --- |
| 平台服务（本地开发） | `configs\platform.yaml` | `:25664` |
| Minecraft relay | `configs\config.yaml` | 控制 `25564`，房间 `25570-25599` |
| 服务器部署脚本默认 | `deploy\install-platform.sh` | `:29564` |

本机已有一份 TLS 联调用的自签证书与构建好的服务端：`tmp-tls\server.crt`、`tmp-tls\server.key`、`tmp-tls\platform.exe`（`tmp-*` 与证书都在 `.gitignore` 里，不会进仓库）。

起本地平台服务（注意 `platform.yaml` 里证书是相对路径，要在证书目录运行）：

```powershell
cd E:\Yiuk_AgentRemote
go build -o tmp-tls\platform.exe ./cmd/platform
Copy-Item configs\platform.yaml tmp-tls\platform.yaml
.\tmp-tls\platform.exe -config tmp-tls\platform.yaml      # 先把 player_password 换掉
```

## 5. 快速自测（单机即可）

### 5.1 Qt↔Go 互操作

```powershell
$env:YIUK_PLATFORM_ADDRESS = "127.0.0.1:25664"
$env:YIUK_PLATFORM_PASSWORD = "<platform.yaml 的 player_password>"
E:\Yiuk_AgentRemote\qt-client\build-qt\yiuk-platform-interop.exe
```

覆盖认证、注册、申请、批准、执行、结果回传、关闭会话。需要 `-DYIUK_BUILD_TESTS=ON` 的构建。

### 5.2 命令行链路

```powershell
go run ./cmd/smoke -addr 127.0.0.1:25664 -password '<player-password>' -ca tmp-tls/server.crt -mode approval
go run ./cmd/device-sim -addr 127.0.0.1:25664 -password '<player-password>' `
  -device-id 111222333 -temporary-password '<32+ chars>' -ca tmp-tls/server.crt -mode approval
```

### 5.3 活动面板（不连平台也能看效果）

面板只读 stdin，一行一条协议（格式见[架构与实现](architecture.md#9-活动面板子进程)）。feed 用 UTF-8 保存，例如 `feed.log`：

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

## 6. 本机已知坑

1. **这个 Git Bash 没有 coreutils**：`ls`、`grep`、`head`、`sed`、`mkdir` 都不存在，`cat` 也不可靠。脚本里用 PowerShell 或 `rg`（路径见 §1）。
2. **中文控制台乱码**：系统代码页 936，UTF-8 内容在 `cmd` / Git Bash 里会显示成乱码（文件本身没问题）。要看清先 `[Console]::OutputEncoding = [Text.Encoding]::UTF8`，或把输出重定向到文件再用编辑器打开。
3. **PowerShell 5.1 按 ANSI 读无 BOM 的 .ps1**：脚本里出现中文会变成乱码字符串，进而把参数传给子进程也一起坏。临时脚本要么只写 ASCII，要么存成 UTF-8 with BOM；给面板传中文参数建议走上面 §5.3 的写法。
4. **125% 缩放**：客户端与活动面板都是 Per-Monitor V2 DPI 感知，按物理像素工作；从 PowerShell（DPI 未感知）取到的窗口矩形是虚拟化坐标，`CopyFromScreen` 抓到的是 2048×1280 的重采样桌面，像素级核对要按 `×1.25` 换算回物理坐标。
5. **`*.exe` / `*.dll` / `*.log` / `*.ps1` / `tmp-*` / `/release/` 都在 `.gitignore` 里**：临时脚本别丢在仓库根目录，否则会被静默忽略。
6. **Qt 构建依赖 `go`**：`go` 不在 `PATH` 时 CMake 只告警，`yiuk-mcp.exe` 不会生成，发布包里就会缺文件。

## 7. 复位与清理

```powershell
# 复位活动面板窗口位置/大小
Remove-Item "$env:LOCALAPPDATA\Yiuk\DeviceAssist\activity-panel.state"

# 复位会话记录与连接状态
Remove-Item "$env:LOCALAPPDATA\Yiuk\DeviceAssist\sessions.json"

# 结束残留进程
Get-Process yiuk-device-assist,yiuk-activity-panel,yiuk-mcp -ErrorAction SilentlyContinue | Stop-Process -Force
```

发布产物在 `release\`（不进仓库，见[打包发布](development.md#5-打包发布)）。
