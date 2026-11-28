# 交接文档

> 写给下一位接手的人：**现在能做什么、代码在哪、怎么验证、哪些坑已经踩过、下一步做什么。**
>
> 相关文档：
> - 产品口径：[`mcp-vision-mouse-plan.md`](mcp-vision-mouse-plan.md)
> - 阶段清单与门禁：[`mcp-vision-mouse-implementation-plan.md`](mcp-vision-mouse-implementation-plan.md)
> - 不能破坏的不变量：[`key-architecture.md`](key-architecture.md)
> - P0 实测数据与工具链坑：[`spike/vision/README.md`](../spike/vision/README.md)

## 1. 一句话状态

**P0 完成、P1 95%（只差本机 bridge 异步化）、P2 完成、P3 文本与图像定位完成（OCR/等待语义未做）、P4 完成 `move_mouse` 平滑移动与 `drag`（`click_target` 未做）。**

客户端已有两套原生内核：`yiuk-vision`（GDI 抓屏 / 快照 / 节流 / 设备门 / UIA / OpenCV）与 `yiuk-input`（SendInput / 确定性轨迹 / 按键守卫 / 灰度变化检测）。`take_screenshot`、`click`、`move_mouse`、`find_on_screen`、`drag` 现在都优先走客户端原生通道，旧客户端自动回退脚本（`find_on_screen` / `drag` 除外，它们必须原生）。

工作区干净，最新提交 `ce1e5e7`。

## 2. 能做什么

### 2.1 MCP 工具（24 个）

| 分类 | 工具 |
| --- | --- |
| 设备/会话 | `list_devices`、`get_device_status`、`request_connection`、`get_session_status`、`close_session` |
| 命令 | `run_command`、`execute_command`、`get_command_result`、`cancel_command` |
| 文件 | `download_file`、`upload_file`、`list_dir`、`stat_path` |
| 桌面 | `take_screenshot`、`click`、`move_mouse`、`drag`、`press_keys`、`type_text` |
| 视觉 | `find_on_screen`（UIA 文本 / OpenCV 图像模板） |
| 等待 | `wait`、`wait_for` |
| 交互 | `ask_user`、`get_user_reply` |

结果里的 `capture_source`（`native`/`script`）与 `input_source`（`native`/`script`）说明这次走了哪条实现路径。

### 2.2 原生操作（平台白名单，8 项）

| 操作 | 状态 | 说明 |
| --- | --- | --- |
| `screen.capture` | 已实现 | `max_width` / `format` / `save`，返回 `screenshot_id` + 几何 + `path`，不回图片本体 |
| `screen.release` | 已实现 | 按路径删除临时截图，只允许截图目录内 |
| `input.click` | 已实现 | 截图坐标 → 物理坐标、`detect_change` 前后采样 |
| `input.move` | 已实现 | `duration_ms` 确定性 ease-in-out |
| `input.drag` | 已实现 | 按下 → 路径 → 抬起，`released:true` 只在确认释放时返回 |
| `vision.find` | 已实现 | UIA 文本 + OpenCV 灰度模板匹配 |
| `vision.click_target` | 未实现 | 客户端内 find/pick/click 事务 |
| `vision.save_template` | 未实现 | 从快照裁剪模板 |

### 2.3 绿色化：所有文件都在程序目录

`<exe 目录>\data\` 下：`platform.json`、`platform-client.json`、`identity.json`、`local-api.json`、`device.json`、`sessions.json`、`activity-panel.state`、`screenshots\`、`tmp\`（PowerShell 脚本）、`work\`（默认工作目录）、`uploads\`。
`data\` 不可写时回退 `%LOCALAPPDATA%\Yiuk\DeviceAssist`；首次切换会把旧目录的身份与配置**复制**过来（设备码不变），旧目录此后只读不写。

## 3. 代码在哪

### 3.1 客户端（Qt）

| 模块 | 内容 |
| --- | --- |
| `src/nativeops.{h,cpp}` | 原生操作分发器：注册表即能力、线程池（2 并发）、取消/超时/超大信封/`request_id` 去重；`Handler(sessionId, arguments, cancelled)` |
| `src/vision/apppaths.{h,cpp}` | `programDataRoot()`：`<exe>\data` 优先，回退用户目录 |
| `src/vision/screengrab.{h,cpp}` | GDI RAII 抓屏（CAPTUREBLT、Per-Monitor V2）、布局版本、缩放、PNG/JPEG 编码 |
| `src/vision/snapshotstore.{h,cpp}` | 每会话 5 张 `screenshot_id` + 几何 + 布局版本，布局变化即失效，临时文件回收 |
| `src/vision/sessionlimiter.{h,cpp}` | 会话级互斥 + 500ms 最小间隔，排队可取消 |
| `src/vision/devicegate.{h,cpp}` | 进程级互斥：抓屏与输入不能并发 |
| `src/vision/visionservice.{h,cpp}` | 唯一入口：`capture` / `clickWithOptions` / `searchText` / `matchImage` / `toScreenPoint` / `screenRectToScreenshot` / `releaseScreenshotFile` / `pruneOrphanScreenshots` |
| `src/vision/textlocator.{h,cpp}` | UIA 控制视图查询（节点 5000 / 时间 300ms 上限）、全半角与空白归一化、control_type 与 region 过滤、阅读顺序排序 |
| `src/vision/templatematcher.{h,cpp}` | OpenCV 灰度 `TM_CCOEFF_NORMED`、阈值、region、IoU 0.3 去重；未编译 OpenCV 时返回 `UNSUPPORTED_OPERATION` |
| `src/input/inputcontroller.{h,cpp}` | `SendInput` 绝对坐标、`buildPath` 确定性缓动、`ButtonGuard`、`drag`、灰度采样与变化判据、前台标题 |
| `src/platformevents.cpp` | 注册 `screen.capture` / `input.click` / `input.move` / `input.drag` / `screen.release`；会话结束清理 |
| `src/platformevents_vision.cpp` | `vision.find`：参数校验 + 调用 `searchText` / `matchImage` |
| `src/apphelpers.cpp` | 配置根 = `programDataPath()`（含旧配置一次性迁移）、默认命令工作目录 = `data\work` |

### 3.2 MCP / 平台（Go）

| 模块 | 内容 |
| --- | --- |
| `internal/agenttools/native.go` | 可选 `NativeInvoker` 接口（本地 bridge 不实现 → 自动走脚本） |
| `internal/agenttools/desktop_screenshot.go` | 原生优先抓屏 + `screen.release` 回收 + `capture_source` |
| `internal/agenttools/desktop_click.go`、`desktop_input.go`、`desktop_input_native.go` | 原生点击/移动与结果映射；只有 `UNSUPPORTED_CLIENT/OPERATION` 才回退 |
| `internal/agenttools/desktop_vision.go` | `find_on_screen`：text / image_path / image_b64 三选一 |
| `internal/agenttools/desktop_drag.go` | `drag`：坐标校验、`released` 必须为真 |
| `internal/platform/core/handler_native.go` | 白名单、能力校验、`submitTask` 共用、`result_unknown` 语义 |
| `internal/platform/protocol/protocol.go` | `execute_native` 帧与字段边界 |
| `internal/userconfig/path.go` | `ProgramDataRoot()`：`<exe>/data` 优先，旧路径回退读取 |

## 4. 怎么验证

### 4.1 Go

```powershell
gofmt -l .                      # 必须空
go vet ./...
go test ./... -count=1
go test -race ./internal/agenttools/ ./internal/platformclient/ ./internal/platform/gateway/ -count=1
```

### 4.2 Qt（不需要平台）

```powershell
& "D:\QT\Tools\Ninja\ninja.exe" -C qt-client\build-qt
.\qt-client\build-qt\yiuk-vision-test.exe       # 抓屏/快照/节流/设备门/回收/UIA/OpenCV
.\qt-client\build-qt\yiuk-input-test.exe        # 轨迹/变化判据/采样
.\qt-client\build-qt\yiuk-nativeops-test.exe    # 分发器能力/取消/超时/去重
```

### 4.3 真实 Qt ↔ 真实 Go 平台（互操作）

平台配置要点：`allow_insecure: true`、`player_password: "interop-player-password-123456"`、`log_file` 用当前目录的相对路径。
**平台是内存态：每次重跑前重启 core+gateway**，否则第二次注册会因为密码已轮换而拿不到同一个设备码。

```powershell
$env:YIUK_PLATFORM_ADDRESS  = "127.0.0.1:25664"
$env:YIUK_PLATFORM_PASSWORD = "interop-player-password-123456"
$env:YIUK_PLATFORM_INSECURE = "1"
$env:YIUK_INTEROP_NATIVE    = "1"
.\qt-client\build-qt\yiuk-platform-interop.exe
```

依次应出现：`screen.capture` → `screen.capture(save)` → `screen.release` → `vision.find`。

可选段（都会真的动鼠标/窗口，默认关闭）：

| 环境变量 | 作用 | 需要的 fixture |
| --- | --- | --- |
| `YIUK_INTEROP_INPUT=1` | `input.move` + `input.click` | `yiuk-input-fixture.exe --expect-clicks 1` |
| `YIUK_INTEROP_DETECT_CHANGE=1` | 左半区换亮度应 `changed=true`，右半区应 `false` | `yiuk-input-fixture.exe --expect-clicks 4` |
| `YIUK_INTEROP_WINDOW_DRAG=<窗口类名>` | 拖目标窗口标题栏绕屏一圈并校验回到起点 | 任意普通权限窗口；`TaskManagerWindow` 需客户端提权 |
| `YIUK_INTEROP_DRAG_SMOKE=1` | 只在窗口附近走 300px 闭环，不碰屏幕边缘 | 同上 |

### 4.4 用真实客户端验收（推荐路径）

```powershell
# 1) 起平台：dev-redis + platform-core + platform-gateway（见 local-dev.md）
# 2) 以管理员身份启动客户端（要操作提权窗口时必须提权）
Start-Process cmd.exe -Verb RunAs -ArgumentList '/c','set "PATH=D:\QT\6.11.1\mingw_64\bin;%PATH%" && start "" "E:\Yiuk_AgentRemote\qt-client\build-qt\yiuk-device-assist.exe"'
# 3) MCP 侧：list_devices → request_connection（可直接带临时密码，跳过审批）
# 4) find_on_screen(image_path=...) → drag(from=..., to=..., screenshot_id=...)
```

## 5. 真机验收记录（已做）

| 项目 | 结果 |
| --- | --- |
| 悬停 + 点击（fixture 窗口） | `FIXTURE OK clicks=1 hover=1 hover_in_left=1 click_in_right=0` |
| `detect_change` | 换亮度区 `changed_ratio 1 / diff_ratio 0.80 / changed=true`；静态区 `0 / 0 / false`，连续两次一致 |
| `drag`（fixture 窗口） | 全屏 5 段 `reached true`、回到起点；冒烟 4 段同样通过 |
| OpenCV 定位 | 任务管理器标题栏 `160x30 @758,341` 在全屏 2560×1600 中匹配回同一点，`score 1.0000` |
| 任务管理器拖动（真实客户端 + 真实平台） | 起点 `(752,337)` → 右 `+592` → 下 `+323` → 左 `−1344` → 上 `−323` → 右 `+752`，最终 `rect = 752,337`，**闭环** |
| 绿色化 | `data\` 内出现配置/身份/`screenshots\`/`tmp\`；`data\tmp` 可见正在执行的 `.ps1`；C 盘旧目录不再写入 |

## 6. 踩过的坑（改输入/视觉前必读）

1. **UIPI**：任务管理器等自动提权窗口，非提权客户端发的合成点击/拖动会被 Windows **静默丢弃**（`SendInput` 仍返回成功，窗口纹丝不动）。要操作提权窗口，客户端必须同样提权。
2. **坐标空间必须与 `screenshot_id` 对应**：`take_screenshot(max_width=1568)` 之后截图坐标 ≠ 物理像素（比例 1.633）。混用会让点击落到别处——所有结果都带 `screenshot_id`，用同一张图的坐标系。
3. **按下点不能被挡住**：活动面板、终端等浮层会盖住目标窗口，点击落到浮层上。验收时把其它窗口最小化，或选一个不被覆盖的标题栏位置。
4. **激活点击与拖动之间要跳过双击时间**：先点一下标题栏激活，若 500ms 内在同一点再次按下，Windows 会当成"双击标题栏"把窗口最大化（位置 `-9,-9`）。等 700ms，并在 `IsZoomed()` 为真时先 `SW_RESTORE`。
5. **灰度不是亮度**：`RGB(255,158,96)` 与 `RGB(96,158,255)` 的灰度都是 169，`detect_change` 看不出来。换色要换亮度。
6. **采样前要等合成**：点击后立刻 `BitBlt` 可能拿到上一帧，`sampleRegion` 里先 `DwmFlush()`。
7. **模板要在同一状态截取**：标题栏激活/未激活像素不同；窗口被销毁的瞬间会被判成"巨大变化"（换窗口/退出前不要采样）。
8. **窗口移动有延迟**：拖动后立刻按"新位置"再按，可能还没移到位而点到客户区。每段之前用 `GetWindowRect` 确认，或先 OpenCV 重新定位。
9. **屏幕边缘有第三方浮层**：这台机器常驻 `MyDockFinder`，会抢边缘点击；验收里用"中部 300px 闭环"的冒烟模式。
10. **Git Bash 没有 coreutils**：`ls/grep/head/sed` 都不存在，用 `rg`、PowerShell 或 Go；中文控制台是 CP936，看文件用编辑器。

## 7. 已知限制与未验证

| 项 | 状态 |
| --- | --- |
| `click_target` / `vision.click_target` | 未实现（P4 剩余） |
| OCR 兜底 | 未实现（UIA 已可用；`OCR_UNAVAILABLE` 语义待补） |
| `find_on_screen` 的 `timeout_seconds` / `wait_until_found` | 未实现 |
| 多显示器 / 混合 DPI / 负原点 | 未验证（P5） |
| 模板管理（`vision.save_template`）、ROI 缓存 | 未实现（P5） |
| Windows CI + OpenCV 静态构建缓存 | 未做（P6）；本机 exe 因 OpenCV 增加 ~5.7MB |
| 本机 bridge 异步化 | 未做（P1 剩余，仅影响无平台 demo 后端） |
| UIA 命中率/耗时三场景实测 | 未做（探针只测了遍历耗时；空文档 0 节点约 320ms） |
| 平台已知薄弱点 #3/#4/#5/#7/#8/#11 | 未修，见 `key-architecture.md` §4 |

## 8. 下一步（按顺序）

1. **`click_target`**：客户端内 find → pick → click 一笔事务（默认 `unique` + `detect_change`，歧义/未命中时**不按下**）。可直接复用 `VisionService::searchText/matchImage` 与 `clickWithOptions`，注意把设备门持有到点击结束。
2. **`find_on_screen` 的等待语义**：`timeout_seconds` + `wait_until_found`（每 500ms 一轮、轮间释放设备门、可取消）。
3. **OCR 兜底**：`Windows.Media.Ocr`（P0 已验 C++ 直连可用），缺语言包返回 `OCR_UNAVAILABLE` + hint。
4. **P5**：`vision.save_template`（从快照裁剪，配额与路径校验）、多显示器/混合 DPI、ROI 缓存。
5. **P6**：Windows CI（Qt + OpenCV 固定版本缓存）、绿色包/安装器体积与启动验证、`Release\` 重新打包。

## 9. 交接检查清单

- [ ] `gofmt -l .` 空、`go vet ./...` 干净、`go test ./...` 全绿
- [ ] `yiuk-vision-test` / `yiuk-input-test` / `yiuk-nativeops-test` 三个都输出 `OK`
- [ ] 互操作程序 `INTEROP EXIT=0`，日志有四段原生操作
- [ ] 客户端启动后 `data\` 里有配置与身份，`%LOCALAPPDATA%` 旧目录不再被写
- [ ] 没有残留的 `platform-core` / `platform-gateway` / `dev-redis` / `yiuk-input-fixture` 进程
- [ ] 没有把 `platform.json` / `local-api.json` / `data\` 提交进仓库（`.gitignore` 已覆盖常见项，`data` 目录在 `build-qt` 内不会入库）
