# 交接文档

> 写给下一位接手的人：**现在能做什么、代码在哪、怎么验证、哪些坑已经踩过、下一步做什么。**
>
> 相关文档：
> - 产品口径：[`mcp-vision-mouse-plan.md`](mcp-vision-mouse-plan.md)
> - 阶段清单与门禁：[`mcp-vision-mouse-implementation-plan.md`](mcp-vision-mouse-implementation-plan.md)
> - 不能破坏的不变量：[`key-architecture.md`](key-architecture.md)
> - P0 实测数据与工具链坑：[`spike/vision/README.md`](../spike/vision/README.md)

## 1. 一句话状态

**P0–P4 完成（UIA 文本 + Windows OCR 兜底 + OpenCV 图像、`click_target` 含等目标出现）、P1 完成（本机 bridge 原生通道异步化）、P6 进行中（Windows CI + 0.1.9 绿色包已出，CI 待跑绿）。**

客户端已有两套原生内核：`yiuk-vision`（GDI 抓屏 / 快照 / 节流 / 设备门 / UIA / OpenCV）与 `yiuk-input`（SendInput / 确定性轨迹 / 按键守卫 / 灰度变化检测）。`take_screenshot`、`click`、`move_mouse`、`find_on_screen`、`click_target`、`drag` 现在都优先走客户端原生通道，旧客户端自动回退脚本（`find_on_screen` / `click_target` / `drag` 除外，它们必须原生）。

**最新发布包：0.1.9**（`Release\YiukDeviceAssist-0.1.9-win64`，见 §5）。工作区干净（`git log -1 --oneline` 看最新提交）。

## 2. 能做什么

### 2.1 MCP 工具（26 个）

| 分类 | 工具 |
| --- | --- |
| 设备/会话 | `list_devices`、`get_device_status`、`request_connection`、`get_session_status`、`close_session` |
| 命令 | `run_command`、`execute_command`、`get_command_result`、`cancel_command` |
| 文件 | `download_file`、`upload_file`、`list_dir`、`stat_path` |
| 桌面 | `take_screenshot`、`click`、`move_mouse`、`drag`、`press_keys`、`type_text` |
| 视觉 | `find_on_screen`（UIA 文本 / Windows OCR 兜底 / OpenCV 图像模板或 `template_id`，可等目标出现）、`click_target`（定位+选择+点击一笔事务，`source=auto/uia/ocr`，可等目标出现）、`save_template`（从最近一帧裁模板） |
| 等待 | `wait`、`wait_for` |
| 交互 | `ask_user`、`get_user_reply` |

结果里的 `capture_source`（`native`/`script`）与 `input_source`（`native`/`script`）说明这次走了哪条实现路径。

**没有平台时也能用**：本机 bridge 的 `POST /native` 走同一个 `NativeOperationDispatcher`
（同一份注册表/取消/超时/信封上限），只是传输换成 loopback + Bearer token 且**异步回包**；
`localbridge.Client` 实现了 `NativeInvoker`，所以 `find_on_screen` / `click_target` / `drag`
在无平台模式下同样可用。

**等目标出现（两个工具都支持）**：`wait_until_found: true` + `timeout_seconds`。
- `find_on_screen`：1–25 秒（默认 10），轮询在 MCP 侧，每 500ms 一个独立原生调用（每轮自己取/放设备门）；
  到时仍没命中就 **`ok:true` + `timed_out:true` + 空 matches**，不是错误——“没出现”是正常结果。
  返回里带 `attempts`（查了几轮）与 `waited_ms`。
- `click_target`：1–20 秒（默认 10，上限小 5 秒是给“找到后再点+采样”留余量）。
  等待在客户端的一笔事务里：每轮取门 → 抓帧 → 定位 → 选择，**轮间放门**；
  找到的那一轮在同一把门内直接点完（不存在“找到与点击之间被别人改了画面”）。
  超时是 **`NOT_FOUND` + `data.timed_out:true`**，且一次 down 事件都不发。

### 2.2 原生操作（平台白名单，8 项）

| 操作 | 状态 | 说明 |
| --- | --- | --- |
| `screen.capture` | 已实现 | `max_width` / `format` / `save`，返回 `screenshot_id` + 几何 + `path`，不回图片本体 |
| `screen.release` | 已实现 | 按路径删除临时截图，只允许截图目录内 |
| `input.click` | 已实现 | 截图坐标 → 物理坐标、`detect_change` 前后采样 |
| `input.move` | 已实现 | `duration_ms` 确定性 ease-in-out |
| `input.drag` | 已实现 | 按下 → 路径 → 抬起，`released:true` 只在确认释放时返回 |
| `vision.find` | 已实现 | UIA 文本 + OpenCV 灰度模板匹配 |
| `vision.click_target` | 已实现 | 客户端内 find→pick→click 事务（每轮持设备门，歧义/未命中不按下；可等目标出现） |
| `vision.save_template` | 已实现 | 从最近一帧保留的像素裁模板（原子写、配额、路径穿越校验） |

### 2.3 绿色化：所有文件都在程序目录

`<exe 目录>\data\` 下：`platform.json`、`platform-client.json`、`identity.json`、`local-api.json`、`device.json`、`sessions.json`、`activity-panel.state`、`screenshots\`、`templates\`（`save_template` 的模板 PNG + `index.json`）、`tmp\`（PowerShell 脚本）、`work\`（默认工作目录）、`uploads\`。
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
| `src/vision/visionservice.{h,cpp}` | 唯一入口：`capture` / `clickWithOptions` / `clickTarget` / `searchText` / `matchImage` / `saveTemplate` / `loadTemplate` / `toScreenPoint` / `screenRectToScreenshot` / `releaseScreenshotFile` / `pruneOrphanScreenshots`；`pickTarget`、`screenshotRectToScreen` 与歧义判据是纯函数 |
| `src/vision/requestparser.{h,cpp}` | 原生操作参数 → `VisionService` 入参的**唯一**解析处（三选一、`source=auto/uia/ocr`、region、阈值/limit 钳制、click_target 默认值与等待预算）；客户端 handler 与互操作程序共用，纯函数可测 |
| `src/vision/textlocator.{h,cpp}` | UIA 控制视图查询（节点 5000 / 时间 300ms 上限）、全半角与空白归一化、control_type 与 region 过滤、阅读顺序排序 |
| `src/vision/roicache.{h,cpp}` | 图像模板的 ROI 缓存：键 = 像素摘要+阈值+region+布局版本，TTL 10 s / 32 条；只给 `limit==1` 的只读查找，`click_target` 不用 |
| `src/vision/ocrlocator.{h,cpp}` | Windows.Media.Ocr 兜底：手写 WinRT ABI（无 winrt 头/C++/WinRT）、按查询文本挑语言、词窗口匹配（去字间空格）、词框并集、按面积去重；**必须 `-fno-devirtualize` 编译** |
| `src/vision/templatematcher.{h,cpp}` | OpenCV 灰度 `TM_CCOEFF_NORMED`、阈值、region、IoU 0.3 去重；未编译 OpenCV 时返回 `UNSUPPORTED_OPERATION` |
| `src/input/inputcontroller.{h,cpp}` | `SendInput` 绝对坐标、`buildPath` 确定性缓动、`ButtonGuard`、`drag`、灰度采样与变化判据、前台标题 |
| `src/platformevents.cpp` | 注册 `screen.capture` / `input.click` / `input.move` / `input.drag` / `screen.release`；会话结束清理 |
| `src/platformevents_vision.cpp` | `vision.find` / `vision.click_target`：解析（`requestparser`）+ 调用 `searchText` / `matchImage` / `clickTarget`，失败信封带 hint 与现场 |
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

### 4.2 Qt（不需要平台，CI 也跑这一套）

```powershell
& "D:\QT\Tools\Ninja\ninja.exe" -C qt-client\build-qt
.\qt-client\build-qt\yiuk-vision-test.exe       # 抓屏/快照/节流/设备门/回收/UIA/OpenCV/pickTarget
.\qt-client\build-qt\yiuk-input-test.exe        # 轨迹/变化判据/采样
.\qt-client\build-qt\yiuk-nativeops-test.exe    # 分发器能力/取消/超时/去重
.\qt-client\build-qt\yiuk-click-target-test.exe # 真机：真窗口 + 真 UIA + 真 SendInput（会动鼠标）
.\qt-client\build-qt\yiuk-localbridge-test.exe # 本机 MCP 桥：token / 两个通道 / 请求体上限 / 异步回包

# 一次跑完（CI 用的就是这条；真机项拿不到前台窗口时 ctest 计为 SKIP）：
cd qt-client\build-qt; ctest --output-on-failure
```

### 4.3 真实 Qt ↔ 真实 Go 平台（互操作）

平台配置要点：`allow_insecure: true`、`player_password: "interop-player-password-123456"`、`log_file` 用当前目录的相对路径。
**平台是内存态：每次重跑前重启 core+gateway**，否则第二次注册会因为密码已轮换而拿不到同一个设备码。
证书是相对路径，**gateway 必须从证书目录启动**（`cd tmp-tls && ./platform-gateway.exe -config platform.yaml`）；
并且 `allow_insecure: true` 时网关仍然只服务 TLS，所以客户端要带 CA：
`allow_insecure` 只意味着“允许不校验证书”，不是“可以用明文”。

```powershell
# tmp-tls 下一次性的准备（自签证书 + 配置拷贝）
openssl req -x509 -newkey rsa:2048 -nodes -keyout server.key -out server.crt -days 2 -subj "//CN=127.0.0.1"
$env:YIUK_PLATFORM_ADDRESS  = "127.0.0.1:25664"
$env:YIUK_PLATFORM_PASSWORD = "interop-player-password-123456"
$env:YIUK_PLATFORM_TLS      = "1"
$env:YIUK_PLATFORM_CA       = "E:\Yiuk_AgentRemote\tmp-tls\server.crt"
$env:YIUK_INTEROP_NATIVE    = "1"
.\qt-client\build-qt\yiuk-platform-interop.exe   # 看退出码：INTEROP=0
```

依次应出现：`screen.capture` → `screen.capture(save)` → `screen.release` → `vision.find` →
`vision.click_target` ×2（故意用不会出现的文本：第一次期望 `NOT_FOUND` + 候选现场；
第二次带 `wait_until_found` + 1 秒预算，期望 `NOT_FOUND` + `timed_out:true` + `attempts>=2`。两段都不碰鼠标。）

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
| `click_target` 真机（`yiuk-click-target-test`，裸 Win32 按钮 fixture） | 未命中 `NOT_FOUND` 且 0 次 down；两个同标签按钮 `AMBIGUOUS_TARGET` 且 0 次 down；`pick=index:2` 只点到第二个（`clicks[2]==1`，其余 0）；`pick=best` 点到唯一按钮；落点 `(969,348)` 在命中框 `[799,322,1139,374]` 内 |
| 互操作（真实 Qt ↔ 真实 Go 平台） | `INTEROP EXIT=0`；平台审计 6 条 `native_submitted`：`screen.capture` ×2、`screen.release`、`vision.find`、`vision.click_target` ×2（含等待超时段） |
| 模板（`yiuk-vision-test`，真实抓屏） | 从最近一帧裁 160×80：`template_id` 12 位 hex、`loadTemplate` 读回同尺寸；旧 `screenshot_id` → `STALE_SCREENSHOT`；越界/超大 region → `INVALID_ARGUMENT`/`TEMPLATE_TOO_LARGE`；`../../windows/win.ini` → `INVALID_ARGUMENT`；损坏文件 → `TEMPLATE_CORRUPTED`；索引写满 100 条 → `TEMPLATE_QUOTA_EXCEEDED` |
| OCR（`yiuk-vision-test`，GDI 画字，不读桌面） | `zh-Hans-CN`、2 行、**29 ms**；归一化后命中「收 藏」与 `Settings`，词框并集落在画字位置 `(32,49) 78×39` |
| OCR 端到端（`yiuk-click-target-test`，`source=ocr` + `region`） | 中文按钮「收藏 首页 设置」被 OCR 读到并点中：`source=ocr`、`clicks[5]==1`，其余按钮 0 次 |
| 发布包 0.1.9 | `Release/YiukDeviceAssist-0.1.9-win64`（82 MB / 24 个文件）+ zip（34 MB）+ sha256 `6ce3df6d…8787`；在**不含 Qt/MinGW 的 PATH** 下启动成功（含 `save_template`/OCR/hint），`data` 就地创建 |
| 失败 hint（`yiuk-vision-test`） | 纯色区域说"没有可用纹理"（不提阈值）、普通未命中带"最高分 0.71 < 阈值 0.88"、歧义带候选数与 `pick` 用法、缺语言包/模板配额/模板不存在各指向具体动作、未知错误码不编造 |
| ROI 缓存（`yiuk-vision-test`，真实抓屏） | 同一个模板连查两次：第二次 `roi_cached=true`、搜索面积为整屏 <½、落点与全屏命中一致 `(877,549)`；`limit>1` 时不走短路；换模板/换布局/换阈值/过期都命中不到旧条目 |
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
10. **UIA 的逗号坑（已修）**：`findText` 把 `CoInitializeEx` 返回的 `RPC_E_CHANGED_MODE` 当"COM 不可用"，
    导致在**已经初始化过 COM 的线程**里（典型是 Qt 主线程，被 OleInitialize 拉成 STA）
    `find_on_screen` / `click_target` 直接降级成 `UIA_UNAVAILABLE`。现在把该返回值当"COM 可用、只是不归我管"，
    只在自己初始化成功时才配对 `CoUninitialize`。客户端 handler 本来跑在线程池里，所以日常路径碰不到；
    但写测试/工具直接在主线程调用时会中。
11. **Qt 窗口的 UIA 包围盒是逻辑像素**：Qt 的无障碍桥按 DIP 上报 `BoundingRectangle`，
    在 125% 缩放下比物理像素小一圈（实测按钮 `784,477` vs UIA `980,596`）。原生 Win32 窗口没这个问题。
    所以验收 fixture 用裸 Win32 BUTTON 而不是 Qt 控件；对真实 Qt 目标做 `text` 定位时，
    命中的中心会落在真实位置左上方向，需要靠 `image` 模板或 `region` 修正（待评估）。
10. **Git Bash 的 coreutils（已修）**：pi 的 `shellPath` 指向 `C:\msys64\usr\bin\bash.exe`，以 `bash -c`
    非登录方式启动，不读 `/etc/profile`，而 Windows PATH 里没有 `C:\msys64\usr\bin`，所以
    `ls/grep/head/sed/find` 曾经全部 `command not found`。修法：在 `~/.pi/agent/bin`（pi 已把它放在
    子进程 PATH 首位）放一层包装脚本，转发到**同一个 MSYS 根**的 exe，重建命令
    `node ~/.pi/agent/fix-bash-coreutils.mjs`（幂等）。两条纪律：① **不要跨 MSYS 根**
    （指向 `E:\Git\usr\bin\*` 会让 `'*.md'` 这类带引号的通配符被提前展开，`/tmp` 也会解析到另一个根）；
    ② `settings.shellPath` 别改成 Git 的 `bash.exe`，同样原因。仍缺 `xxd/patch/zip/tree/strings`
    （`pacman -S` 可装；`diff/cmp/unzip` 已补）。中文控制台仍是 CP936，看文件用编辑器。

## 7. 已知限制与未验证

| 项 | 状态 |
| --- | --- |
| 非原生 Win32 目标（Qt/WPF）的 UIA 坐标 | 已验证 Qt 目标上报逻辑像素（见 §6.11），未做修正 |
| 多显示器 / 混合 DPI / 负原点 | 未验证（P5） |
| Windows CI | 已加 `windows-qt` job（Qt 6.11.1 + MinGW + OpenCV 4.12 缓存 + ctest），**还没在 GitHub 上跑绿过** |
| 安装器（ISCC）重打 / 安装卸载回归 | 未做（绿色 zip 已重打，见 §5） |
| UIA 命中率/耗时三场景实测 | 未做（探针只测了遍历耗时；空文档 0 节点约 320ms） |
| 平台已知薄弱点 #3/#4/#5/#7/#8/#11 | 未修，见 `key-architecture.md` §4 |

## 8. 下一步（按顺序）

1. **P6 收尾**：把 `windows-qt` CI 第一次跑绿（安装器可选）、Edge→抖音回归需设备所有者授权。
2. **P5 剩余**：多显示器/混合 DPI + `monitor_id`（本机单屏无法验证，需要有多屏的机器）；
   OCR 目前每次全帧识别（2560×1600 约 200–400ms），可以把 ROI 缓存扩展到 OCR 裁剪。

## 9. 交接检查清单

- [ ] `gofmt -l .` 空、`go vet ./...` 干净、`go test ./...` 全绿
- [ ] `ctest --output-on-failure` 在 `qt-client\build-qt` 里 5/5 通过（真机项拿不到前台窗口时算 SKIP）
- [ ] 互操作程序 `INTEROP EXIT=0`；平台审计有 6 条 `native_submitted`（`screen.capture` ×2、`screen.release`、`vision.find`、`vision.click_target` ×2）
- [ ] 客户端启动后 `data\` 里有配置与身份，`%LOCALAPPDATA%` 旧目录不再被写
- [ ] 没有残留的 `platform-core` / `platform-gateway` / `dev-redis` / `yiuk-input-fixture` 进程
- [ ] 没有把 `platform.json` / `local-api.json` / `data\` 提交进仓库（`.gitignore` 已覆盖常见项，`data` 目录在 `build-qt` 内不会入库）
