# 一次远程部署的操作记录（2026-10-23）

本文件记录一次通过 Yiuk Switchboard MCP 连接远端设备并部署 `dsh-pet` 插件的完整过程，包含**操作者对本地（operator 机）的全部改动**、**目标机的全部安装动作**、当时的错误决策与修正方式。目的是让后来者能复现、能审计、能回滚。

- 操作者机：仓库所在机器（`E:\Yiuk_AgentRemote`，本机 Unix 名 `34345`）
- 目标机：设备码 **903174411**，Windows 11（`10.0.26100`，PowerShell `5.1.26100.9711`），用户 `41895`，**非管理员**
- 授权方式：设备直连模式（`direct`），会话由 32 位临时密码授权的 `execute_command` 权限
- 结果：目标机 dsh + dsh-pet 0.3.8 安装完成，`dsh web` 正常服务（`127.0.0.1:8080`）

---

## 1. 本地（operator 机）改动清单

### 1.1 唯一留存的文件改动

`C:\Users\34345\.pi\agent\mcp.json` —— **新建**（此前不存在），把 Yiuk MCP server 注册进 pi：

```json
{
  "mcpServers": {
    "yiuk": {
      "command": "E:\\Yiuk_AgentRemote\\release\\YiukDeviceAssist-0.1.4-win64\\yiuk-mcp.exe",
      "exposure": "direct",
      "description": "Yiuk Switchboard 远程设备协作：连接目标电脑执行 PowerShell、传文件"
    }
  }
}
```

注册命令（等价）：

```sh
pi mcp add yiuk --exposure direct \
  --description "Yiuk Switchboard 远程设备协作：连接目标电脑执行 PowerShell、传文件" \
  -- "E:\Yiuk_AgentRemote\release\YiukDeviceAssist-0.1.4-win64\yiuk-mcp.exe"
```

撤销：

```sh
pi mcp remove yiuk
```

### 1.2 创建后已删除的临时文件

| 路径 | 用途 |
| --- | --- |
| `E:\Yiuk_AgentRemote\tmp-mcp-call.mjs` | 手写的 MCP stdio 客户端（单次调用） |
| `E:\Yiuk_AgentRemote\tmp-mcp-run.mjs` | 手写的 MCP stdio 客户端（多步 + 会话占位符 + 结果轮询） |
| `E:\Yiuk_AgentRemote\tmp-step1/2/3.json` | 上述脚本的步骤文件 |
| `C:\msys64\tmp\dsh-pet` | 本地 `git clone` 的 dsh-pet 仓库（仅用于读 README） |

均已 `rm` 删除并复查不存在。

### 1.3 只读，未修改

- `%LOCALAPPDATA%\Yiuk\DeviceAssist\`：`platform-client.json`、`platform.json`、`local-api.json`、`activity-panel.state`、`sessions.json`
  - 证据：这些文件的 mtime 均为 `19:03–19:05`，早于本次会话（`21:2x`），未被写入。
- `E:\Yiuk_AgentRemote` 的 Go / Qt 源码（读 `cmd/yiuk-mcp`、`internal/{mcpadapter,agenttools,platformclient,userconfig}` 以确认 MCP 协议与配置路径）。
- 项目内 `.pi/mcp.json`（`2025-10-20` 已存在，条目为 `yiuk-switchboard` → `go run ./cmd/yiuk-mcp`），不是本次新增。

### 1.4 进程层面

- 多次短命启动 `yiuk-mcp.exe`（作为 stdio MCP server），进程均已退出，无残留。
- 多次 `pi -p` 非交互子会话，均带 `--no-session`，不写会话文件。
- 本机**未安装、未下载**任何软件；`%LOCALAPPDATA%\Yiuk\DeviceAssist\downloads` 未变化。

---

## 2. 目标机（903174411）改动清单

目标机现状（部署前）：**无 DSH、无 Node.js、无 npm**，有 `winget`（用户级 shim）、可访问外网、C 盘剩余 338 GB、当前进程非管理员。

按顺序执行的动作（每条都是一个 `execute_command` 任务，会话结束即关闭）：

### 2.1 安装 Node.js（免管理员）

管理员不可用，故放弃 `winget`/MSI，改用官方便携 zip 解压到用户目录：

```powershell
$idx = Invoke-RestMethod 'https://nodejs.org/dist/index.json' -TimeoutSec 60
$ver = ($idx | Where-Object { $_.lts } | Select-Object -First 1).version   # v24.22.1
$zip = Join-Path $env:TEMP ('node-' + $ver + '-win-x64.zip')
Invoke-WebRequest -UseBasicParsing -Uri ('https://nodejs.org/dist/' + $ver + '/node-' + $ver + '-win-x64.zip') -OutFile $zip
Add-Type -AssemblyName System.IO.Compression.FileSystem
$dest = Join-Path $env:LOCALAPPDATA 'Programs'
[System.IO.Compression.ZipFile]::ExtractToDirectory($zip, (Join-Path $dest 'nodejs-tmp'))
Move-Item (Join-Path $dest ('nodejs-tmp\node-' + $ver + '-win-x64')) (Join-Path $dest 'nodejs')
```

- 结果：`C:\Users\41895\AppData\Local\Programs\nodejs`，`node v24.22.1`，`npm 11.19.1`
- 并写入用户级 PATH：`[Environment]::SetEnvironmentVariable('Path', ..., 'User')`

### 2.2 安装 dsh CLI 与 pnpm —— 注意 `allow-scripts`

第一次 `npm install -g @deepseek-ai/dsh pnpm` 虽然成功，但 npm 11 默认**拦截了安装脚本**：

```
npm warn install-scripts  pnpm (preinstall/postinstall)
npm warn install-scripts  @deepseek-ai/dsh-subprocess-local (postinstall)
npm warn install-scripts  koffi, node-pty, @google/genai, protobufjs
```

`pnpm` 的二进制、`node-pty`/`koffi` 的原生预编译都靠这些脚本，被拦截会让 dsh 不可用。修正：

```powershell
npm config set allow-scripts=pnpm,@deepseek-ai/dsh-subprocess-local,koffi,node-pty,@google/genai,protobufjs --location=user
npm install -g --force --allow-scripts=pnpm,@deepseek-ai/dsh-subprocess-local,koffi,node-pty,@google/genai,protobufjs @deepseek-ai/dsh pnpm
```

验证：

```
dsh  --version → 0.2.1-rc.2          # 与 dsh-pet 官方测试版本一致
pnpm --version → 12.15.0
```

shim 位置：`%LOCALAPPDATA%\Programs\nodejs\dsh.cmd|dsh.ps1|pnpm.cmd`

> 经验：npm 11 全局安装带原生依赖的包，必须显式 `--allow-scripts`，否则"安装成功但运行失败"。

### 2.3 安装插件

```powershell
dsh plugin --profile web add dsh-pet@0.3.8
```

输出要点：

```
dsh: initialized profile web at C:\Users\41895\.dsh\profiles\web
Downloading dsh-pet@0.3.8: 62.78 MB
+ dsh-pet 0.3.8        Done in 3.5s using pnpm v12.15.0
```

验证：

```
dsh plugin --profile web list
dsh-profile-web C:\Users\41895\.dsh\profiles\web (PRIVATE)
└── dsh-pet@0.3.8        1 package
```

### 2.4 启动（关键：插件装了不等于在跑）

目标机原本没有 DSH 进程，只装插件不会有任何可见效果。启动 web profile：

```powershell
Start-Process -FilePath "$env:LOCALAPPDATA\Programs\nodejs\dsh.cmd" `
  -ArgumentList '--profile','web','--port','8080' `
  -WindowStyle Hidden `
  -RedirectStandardOutput "$env:USERPROFILE\.dsh\web.out.log" `
  -RedirectStandardError  "$env:USERPROFILE\.dsh\web.err.log" -PassThru
```

运行状态：

```
LISTEN=127.0.0.1:8080  pid=34052 (node)
配置树：dsh --profile web --dump-config → HAS_PET=True / id: dsh-pet
[dsh-pet] Electron not found, downloading v43.3.0 (win32-x64) ... 137.7MB
[dsh-pet] ready in 22.2s: C:\Users\41895\.dsh\electron\electron.exe
dsh web: http://127.0.0.1:8080/?token=<TOKEN>
```

- `dsh-pet` 会自己下载 Electron 用于「脱离浏览器的桌面透明小窗」。
- stderr 一条无害提示：`《main-config.jsonc》的 pets 缺失或为空，已取默认宠物列表`（用内置宠物，正常）。
- 无 token 访问返回 **401**，属预期。

### 2.5 目标机最终产物一览

| 项 | 位置 / 版本 |
| --- | --- |
| Node.js | `%LOCALAPPDATA%\Programs\nodejs`，`v24.22.1` |
| npm | `11.19.1`（用户级 `allow-scripts` 已配置） |
| dsh CLI | `@deepseek-ai/dsh` `0.2.1-rc.2`（全局） |
| pnpm | `12.15.0`（全局） |
| profile | `C:\Users\41895\.dsh\profiles\web` |
| 插件 | `dsh-pet@0.3.8` |
| Electron | `C:\Users\41895\.dsh\electron\electron.exe`（43.3.0） |
| 运行中 | `dsh web`，`127.0.0.1:8080`，pid 34052 |
| 日志 | `C:\Users\41895\.dsh\web.out.log` / `web.err.log` |

---

## 3. 时间线

| 时间 (本地) | 事件 |
| --- | --- |
| 21:25 | 手写 `tmp-mcp-call.mjs`，用 bash+node 手工驱动 `yiuk-mcp.exe`，`list_devices` 确认设备在线 |
| 21:25 | 首次 `execute_command` 返回 `PERMISSION_DENIED: connection does not own the session`（见 §4.2） |
| 21:26 | 改为单进程多步脚本，探测目标机环境（无 git/node） |
| 21:28 | `pi mcp add yiuk` 注册 MCP（**正确做法，来得太晚**）；删除全部临时脚本与本地 clone |
| 21:29 | 用 `pi -p --tools 'mcp__yiuk__*'` 走 pi 自带 MCP 支持继续 |
| 21:30 | 目标机安装 Node.js（便携 zip） |
| 21:31 | 安装 dsh + pnpm；发现 `allow-scripts` 拦截并重装放行 |
| 21:32 | `dsh plugin --profile web add dsh-pet@0.3.8` 成功 |
| 21:32 | `dsh web` 启动，Electron 自动下载，插件 ready |

---

## 4. 过程中的两个坑（含教训）

### 4.1 核心教训：MCP 没配就该先配 MCP，不要自己造客户端

现象：pi 工具表里只有 `read/bash/edit/write`，没有 `mcp__*`。

原因链：

1. `~/.pi/agent/mcp.json` **不存在**（全局未注册任何 MCP server）。
2. 项目内 `.pi/mcp.json` 的 `yiuk-switchboard` 只在「项目信任 + 会话目录在项目内」时才加载，当前会话目录在 `release/YiukDeviceAssist-0.1.4-win64`，且未授权信任。
3. 于是 MCP 工具从未被声明给模型。

**错误处理**：用 bash 起 `node`，手写 JSON-RPC stdio 客户端直接跟 `yiuk-mcp.exe` 对话。
技术上可行（MCP 是标准协议，任何客户端都能驱动），但这是把「配置缺失」误判成「需要自造工具」。

**正确姿势**：

```sh
pi mcp add <name> --exposure direct -- <server-exe>   # 或写 ~/.pi/agent/mcp.json
pi mcp list                                          # 验证 connected + tools
# 然后在会话里 /reload（会话启动早于注册，工具表需重载）
```

补充约束：**注册后当前会话仍看不到新工具**。pi 只在会话启动时声明工具，必须 `/reload`。在无法 reload 的情况下，可用 pi 自带的非交互入口，仍然不写代码：

```sh
pi -p --no-builtin-tools --tools 'mcp__yiuk__*' --no-session "<指令>"
```

### 4.2 会话（session）归属于「一次 operator 连接」，不是密码

`yiuk-mcp.exe` 每次启动都是一条新的 operator 连接（`operator` 字段为随机 id）。因此：

```
第一次进程：request_connection → session A（approved）
第二次进程：execute_command(session A) → PERMISSION_DENIED: connection does not own the session
```

**规则**：`request_connection` 与其后续 `execute_command`/`get_command_result`/`close_session` 必须在**同一个 MCP server 进程**内完成。
推荐用 `run_command`（一次性：请求 → 等待审批 → 执行 → 轮询结果 → 关闭会话），天然规避该问题，本次部署后半程都用的它。

---

## 5. 回滚与清理

### 本机

```sh
pi mcp remove yiuk      # 删除本次新增的 MCP 注册
```

### 目标机（903174411）

```powershell
# 1) 停止 web 服务
Get-NetTCPConnection -State Listen | Where-Object LocalPort -eq 8080 | ForEach-Object { Stop-Process -Id $_.OwningProcess -Force }

# 2) 移除插件
dsh plugin --profile web remove dsh-pet

# 3) 可选：彻底移除 DSH 相关
npm uninstall -g @deepseek-ai/dsh pnpm
Remove-Item -Recurse -Force "$env:USERPROFILE\.dsh"        # profile + electron + 日志
Remove-Item -Recurse -Force "$env:LOCALAPPDATA\pnpm"        # pnpm store

# 4) 可选：移除 Node.js（含用户 PATH 中那一项）
Remove-Item -Recurse -Force "$env:LOCALAPPDATA\Programs\nodejs"
```

---

## 6. 待办 / 未验证项

- [ ] 目标机浏览器里宠物是否正常渲染（只能在目标机屏幕确认；服务端 401 校验已通过，说明 UI 在跑）
- [ ] `dsh web` 目前绑 `127.0.0.1:8080`，仅目标机本机可访问。要跨机查看需要 `--host 192.168.1.101 --trusted-host 192.168.1.101:8080`（目标机网卡：`192.168.1.101` / `172.30.217.119` / `10.4.175.223`）
- [ ] 进程为手动 `Start-Process` 启动，**未做开机自启**，重启后需再执行 §2.4
- [ ] 目标机 Node/dsh/插件均装在用户目录，未做系统级安装与升级策略
