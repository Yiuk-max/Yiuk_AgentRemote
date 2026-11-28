# 交接文档

> 写给下一位接手的人：**现在能做什么、代码在哪、怎么验证、哪些坑已经踩过、下一步做什么。**
>
> 相关文档：产品口径 [`mcp-vision-mouse-plan.md`](mcp-vision-mouse-plan.md)、阶段清单 [`mcp-vision-mouse-implementation-plan.md`](mcp-vision-mouse-implementation-plan.md)、
> 不变式与硬上限 [`key-architecture.md`](key-architecture.md)、本机环境与快捷命令 [`local-dev.md`](local-dev.md)、
> 构建/打包/部署 [`development.md`](development.md)、发布包组件与许可 [`third-party.md`](third-party.md)。

## 1. 一句话状态

**P0–P4 完成，P5 只剩多显示器/混合 DPI，P6 只剩 CI 首跑与安装器。** 26 个 MCP 工具、8 个原生操作、两套客户端内核都有真机证据。

- 客户端：`yiuk-vision`（GDI 抓屏 / 快照 / 节流 / 设备门 / UIA / Windows OCR / OpenCV）+ `yiuk-input`（SendInput / 确定性轨迹 / 按键守卫 / 灰度变化检测）
- 视觉工具全部走客户端原生通道；`find_on_screen` / `click_target` / `drag` / `save_template` **没有脚本回退**（旧客户端明确 `UNSUPPORTED_CLIENT`）
- 没有平台时：本机 bridge 的 `POST /native` 走同一个分发器，`find_on_screen` / `click_target` / `drag` 同样可用
- 工作区干净（`git log -1 --oneline` 看当前 HEAD）；最新发布包 **0.1.9**（见 §6）

## 2. 能做什么

### 2.1 MCP 工具（26 个）

| 分类 | 工具 |
| --- | --- |
| 设备/会话 | `list_devices`、`get_device_status`、`request_connection`、`get_session_status`、`close_session` |
| 命令 | `run_command`、`execute_command`、`get_command_result`、`cancel_command` |
| 文件 | `download_file`、`upload_file`、`list_dir`、`stat_path` |
| 桌面 | `take_screenshot`、`click`、`move_mouse`、`drag`、`press_keys`、`type_text` |
| 视觉 | `find_on_screen`、`click_target`、`save_template` |
| 等待 | `wait`、`wait_for` |
| 交互 | `ask_user`、`get_user_reply` |

### 2.2 原生操作（白名单 8 项，全部已实现）

| 操作 | 说明 |
| --- | --- |
| `screen.capture` | `max_width` / `format` / `save`，返回 `screenshot_id` + 几何 + `path`，不回图片本体 |
| `screen.release` | 按路径删临时截图（只允许截图目录内），快照元数据保留 |
| `input.click` | 截图坐标 → 物理坐标、`detect_change` 前后采样 |
| `input.move` | `duration_ms` 确定性 ease-in-out |
| `input.drag` | 按下 → 路径 → 抬起；`released:true` 只在确认释放时返回 |
| `vision.find` | UIA 文本 / Windows OCR / OpenCV 模板匹配 |
| `vision.click_target` | 客户端内 find→pick→click 一笔事务（歧义/未命中不发 down） |
| `vision.save_template` | 从最近一帧保留的像素裁模板（原子写、配额、路径穿越校验） |

结果里的 `capture_source` / `input_source` 说明这次走了 `native` 还是 `script`。

### 2.3 绿色化：所有文件都在程序目录

`<exe 目录>\data\`：`platform.json`、`platform-client.json`、`identity.json`、`local-api.json`、`device.json`、`sessions.json`、`activity-panel.state`、`screenshots\`、`templates\`（+`index.json`）、`tmp\`、`work\`、`uploads\`。
`data\` 不可写时回退 `%LOCALAPPDATA%\Yiuk\DeviceAssist`；首次切换把旧目录的身份与配置**复制**过来（设备码不变），旧目录此后只读不写。

## 3. 必须记住的行为契约

这些是"看起来能用、但会用错"的地方，alter 之前先读。

1. **坐标基准 = 最近一张截图**。截图坐标只在"这张图"里有意义；`take_screenshot(max_width=1568)` 之后截图坐标 ≠ 物理像素（比例 1.633）。所有返回都带 `screenshot_id`，跨图混用坐标会点歪。
2. **只有最近一张快照保留像素**（≈16 MB 预算）。`save_template` 只认这一帧：旧 `screenshot_id` 或已换帧 → `STALE_SCREENSHOT`，**绝不重抓一张冒充旧图**。
3. **等待语义**：`wait_until_found` + `timeout_seconds`。`find_on_screen` 1–25 s（默认 10），轮询在 MCP 侧、每 500 ms 一个独立原生调用（每轮自己取/放设备门），超时是 `ok:true` + `timed_out:true` + 空 matches（**不是错误**）。`click_target` 1–20 s（默认 10），等待在客户端一笔事务里：每轮取门→抓帧→定位→选择、**轮间放门**，找到的那一轮在同一把门内点完；超时是 `NOT_FOUND` + `data.timed_out:true`，一次 down 都不发。
4. **歧义判据**：`pick=unique`（默认）时，第一名与**另一个视觉目标**的最强对手得分差 < 0.03 即 `AMBIGUOUS_TARGET`。同一目标的重复命中（UIA 父子节点、NMS 残留：中心重合或一个框 ≥60% 面积被另一个盖住）不算竞争，否则一个按钮永远点不成。
5. **定位来源**：`source = auto|uia|ocr`。`auto` 先 UIA，失败或没命中再对新抓的一帧做 OCR；`source=ocr` 时缺语言包是**真错误**（`OCR_UNAVAILABLE`），`auto` 只把原因写进 `ocr_note`。
6. **模板**：`template_id` 与 `text` / `image_path` / `image_b64` 四选一；配额 100 个 / 32 MiB / 单个 4 MiB / 2048×2048，超了 `TEMPLATE_QUOTA_EXCEEDED`（不静默驱逐）。清理 = 删设备上的 `data\templates`。
7. **ROI 缓存**只加速图像模板，且**只在 `limit == 1`** 时启用；命中必须在当前帧的 ROI 上重新匹配。**`click_target` 一律不用缓存**——`unique`/`best` 必须验证全域竞争候选。结果带 `roi_cached` / `searched_area` / `search_area`。
8. **取消会等设备收尾**：最多 2 s（`kCancelGraceMs`）。处理器先完成就照实报 `succeeded`，超过宽限才报 `cancelled`；会话关闭 → `cancelSession`，平台断线 → `cancelAll`（**不重放**，否则一次点击会变成两次）。
9. **失败 hint 按现场生成**（`yiuk::vision::failureHint`）：纯色区域说"没有可用纹理"、普通未命中报"最高分 vs 阈值"、歧义报候选数、缺语言包/模板配额各指具体动作。从不自动降阈值后点击。

## 4. 代码在哪

### 4.1 客户端（Qt，`qt-client/src`）

| 模块 | 内容 |
| --- | --- |
| `nativeops.{h,cpp}` | 原生操作分发器：注册表即能力、线程池（2 并发）、取消/超时/超大信封（32 KiB）/`request_id` 去重；`Handler(sessionId, arguments, cancelled)` |
| `vision/apppaths.{h,cpp}` | `programDataRoot()`：`<exe>\data` 优先，回退用户目录 |
| `vision/screengrab.{h,cpp}` | GDI RAII 抓屏（CAPTUREBLT、Per-Monitor V2）、布局版本、缩放、PNG/JPEG、`screenshotRectToScreen` |
| `vision/snapshotstore.{h,cpp}` | 每会话 5 张 `screenshot_id` + 几何 + 布局版本；**只有最近一张留像素**；临时文件回收 |
| `vision/sessionlimiter.{h,cpp}` | 会话级互斥 + 500 ms 最小间隔，排队可取消 |
| `vision/devicegate.{h,cpp}` | 进程级互斥：抓屏与输入不能并发；`DeviceLock::release()` 供轮间放门 |
| `vision/visionservice.{h,cpp}` | 唯一入口：`capture` / `clickWithOptions` / `clickTarget` / `searchText` / `matchImage` / `saveTemplate` / `loadTemplate` / `toScreenPoint` / `screenRectToScreenshot` / `releaseScreenshotFile` / `pruneOrphanScreenshots`；`pickTarget` 与歧义判据是纯函数 |
| `vision/textlocator.{h,cpp}` | UIA 控制视图查询（节点 5000 / 300 ms 上限）、全半角与空白归一化、control_type/region 过滤、阅读顺序 |
| `vision/ocrlocator.{h,cpp}` | Windows.Media.Ocr：手写 WinRT ABI（SDK 头在 MinGW 下不能编译）、按查询挑语言、词窗口匹配（去 CJK 字间空格）、词框并集 + 面积去重；**必须 `-fno-devirtualize`** |
| `vision/templatematcher.{h,cpp}` | OpenCV 灰度 `TM_CCOEFF_NORMED`、阈值、region、IoU 0.3 去重、`TemplateStats`（最高分 / 纯色判定） |
| `vision/roicache.{h,cpp}` | 图像模板 ROI 缓存：键 = 像素摘要+阈值+region+布局版本，TTL 10 s / 32 条 |
| `vision/requestparser.{h,cpp}` | 原生操作参数 → `VisionService` 入参的**唯一**解析处（四选一、source、region、阈值/limit 钳制、click_target 默认值与等待预算）+ `failureHint()`；客户端 handler 与互操作程序共用，纯函数可测 |
| `input/inputcontroller.{h,cpp}` | `SendInput` 绝对坐标、`buildPath` 确定性缓动、`ButtonGuard`、`drag`、灰度采样与判据、前台标题 |
| `platformevents.cpp` | 注册 8 个原生操作；会话结束/关闭/断线/退出的取消与清理 |
| `platformevents_vision.cpp` | `vision.find` / `vision.click_target` / `vision.save_template`：解析 → 调用 → 信封（失败带 hint 与现场） |
| `localbridge.{h,cpp}` + `localtools.cpp` | 本机 bridge：`POST /tool/<name>` 同步、`POST /native` **异步**；loopback + Bearer token，再校验会话 |
| `platformclient.{h,cpp}` / `platformlink.cpp` | 平台 WSS 客户端与设备侧交互、配置与状态 |

### 4.2 MCP / 平台（Go）

| 模块 | 内容 |
| --- | --- |
| `internal/agenttools/catalog.go` | 26 个工具的 schema/注解，**schema 是校验唯一来源** |
| `internal/agenttools/desktop_*.go` | 各桌面工具：原生优先 + 脚本回退（`find_on_screen`/`click_target`/`drag`/`save_template` 必须原生） |
| `internal/agenttools/native.go` | 可选 `NativeInvoker` 接口：本地 bridge 与平台客户端各自实现 |
| `internal/platform/core/handler_native.go` | 白名单、能力校验、`submitTask` 共用、`result_unknown` 语义 |
| `internal/platform/protocol/protocol.go` | `execute_native` 帧与字段边界 |
| `internal/platformclient/` | 平台客户端：`correlation_id → 等待通道` 的并发分发、`InvokeNative`、错误信封（含 `data`）透传 |
| `internal/localbridge/client.go` | 本机 bridge 客户端；**实现 `NativeInvoker`**，无平台时视觉工具可用 |
| `internal/userconfig/path.go` | `ProgramDataRoot()`：`<exe>/data` 优先，旧路径回退读取 |

## 5. 怎么验证

### 5.1 Go

```powershell
gofmt -l .                      # 必须空
go vet ./...
go test ./... -count=1
go test -race ./internal/agenttools/ ./internal/platformclient/ ./internal/localbridge/ ./internal/platform/gateway/ -count=1
```

### 5.2 Qt（不需要平台，CI 也跑这一套）

```powershell
& "D:\QT\Tools\Ninja\ninja.exe" -C qt-client\build-qt
cd qt-client\build-qt; ctest --output-on-failure      # 5 个用例
```

| 用例 | 断言数 | 覆盖 |
| --- | --- | --- |
| `yiuk-vision-test` | 189 | 快照/节流/设备门/UIA/OpenCV/参数解析/裁模板/OCR/ROI 缓存/失败 hint |
| `yiuk-nativeops-test` | 23 | 能力/信封/取消宽限/超时/超大结果/真实抓屏 |
| `yiuk-input-test` | 20 | 轨迹端点与单调性/变化判据/采样 |
| `yiuk-localbridge-test` | 15 | token/两个通道/两个请求体上限/异步回包 |
| `yiuk-click-target-test` | 26 | **真机**：真窗口 + 真 UIA/OCR + 真 SendInput（会动鼠标） |

### 5.3 真实 Qt ↔ 真实 Go 平台（互操作）

平台是**内存态**：每次重跑前重启 core+gateway，否则第二次注册会因为密码已轮换拿不到同一个设备码。
`platform.yaml` 里证书是相对路径，**gateway 必须从证书目录启动**；`allow_insecure: true` 也绕不过（它只表示允许不校验证书，TLS 监听仍要证书文件）。

```powershell
# tmp-tls 一次性准备（自签证书 + 配置拷贝，player_password 用下面的值）
openssl req -x509 -newkey rsa:2048 -nodes -keyout server.key -out server.crt -days 2 -subj "//CN=127.0.0.1"

go run ./cmd/dev-redis                                   # 127.0.0.1:6379
go run ./cmd/platform-core -config tmp-tls\platform.yaml  # :25670
cd tmp-tls; .\platform-gateway.exe -config platform.yaml  # :25664 / :25671

$env:YIUK_PLATFORM_ADDRESS  = "127.0.0.1:25664"
$env:YIUK_PLATFORM_PASSWORD = "interop-player-password-123456"
$env:YIUK_PLATFORM_TLS      = "1"
$env:YIUK_PLATFORM_CA       = "E:\Yiuk_AgentRemote\tmp-tls\server.crt"
$env:YIUK_INTEROP_NATIVE    = "1"
.\qt-client\build-qt\yiuk-platform-interop.exe           # 只看退出码：0 = 通过
```

应出现 6 条 `native_submitted`：`screen.capture` ×2、`screen.release`、`vision.find`、`vision.click_target` ×2（第二段带 `wait_until_found` + 1 s 预算，期望 `NOT_FOUND` + `timed_out`；两段都不碰鼠标）。
可选段（都会真动鼠标/窗口，默认关闭）：`YIUK_INTEROP_INPUT=1` 配 `yiuk-input-fixture.exe --expect-clicks 1`；`YIUK_INTEROP_DETECT_CHANGE=1` 配 `--expect-clicks 4`；`YIUK_INTEROP_WINDOW_DRAG=<窗口类名>`。

### 5.4 用真实客户端验收

```powershell
# 平台起来之后，以管理员身份启动客户端（要操作提权窗口时必须提权）
Start-Process cmd.exe -Verb RunAs -ArgumentList '/c','set "PATH=D:\QT\6.11.1\mingw_64\bin;%PATH%" && start "" "E:\Yiuk_AgentRemote\qt-client\build-qt\yiuk-device-assist.exe"'
# MCP 侧：list_devices → request_connection（可带临时密码跳过审批）→ take_screenshot
#        → find_on_screen / click_target → save_template → 再用 template_id 复跑
```

## 6. 真机 / 互操作验收记录（已做）

| 项目 | 结果 |
| --- | --- |
| 悬停 + 点击（fixture） | `FIXTURE OK clicks=1 hover=1 hover_in_left=1 click_in_right=0` |
| `detect_change` | 换亮度区 `changed_ratio 1 / diff_ratio 0.80 / changed=true`；静态区 `0/0/false`，连续两次一致 |
| `drag`（fixture） | 全屏 5 段 `reached true`、回到起点；冒烟 4 段同样通过 |
| 任务管理器拖动（真实客户端 + 平台） | 起点 `(752,337)` → 右 +592 → 下 +323 → 左 −1344 → 上 −323 → 右 +752，`rect = 752,337`，**闭环** |
| `click_target`（裸 Win32 按钮 fixture） | 未命中 `NOT_FOUND` 且 0 次 down；两个同标签按钮 `AMBIGUOUS_TARGET` 且 0 次 down；`pick=index:2` 只点第二个（`clicks[2]==1`）；`pick=best` 点唯一按钮；落点 `(969,348)` 在命中框内 |
| `click_target` 等待 | 预算 1.2 s：`timed_out:true`、`attempts>=2`、0 次 down；目标已在时 `attempts==1` |
| OCR（GDI 画字，不读桌面） | `zh-Hans-CN`、2 行、**29 ms**；归一化后命中「收 藏」与 `Settings`，词框 `(32,49) 78×39` |
| OCR 端到端（`source=ocr` + region） | 中文按钮「收藏 首页 设置」被读到并点中：`source=ocr`、只有该按钮 `+1` |
| 模板（真实抓屏） | 160×80 裁剪 → `template_id` 12 位 hex、读回同尺寸；旧 id → `STALE_SCREENSHOT`；越界/超大 → `INVALID_ARGUMENT`/`TEMPLATE_TOO_LARGE`；`../../windows/win.ini` → `INVALID_ARGUMENT`；损坏文件 → `TEMPLATE_CORRUPTED`；索引写满 → `TEMPLATE_QUOTA_EXCEEDED` |
| ROI 缓存 | 同模板连查两次：第二次 `roi_cached=true`、搜索面积 < 整屏 ½、落点与全屏命中一致 `(877,549)` |
| 互操作 | `INTEROP EXIT=0`，平台审计 6 条 `native_submitted` |
| 发布包 0.1.9 | 目录 82 MB / 24 个文件；zip 34.1 MB，sha256 `fdecd723…073a`；在**不含 Qt/MinGW 的 PATH** 下启动成功（标题 0.1.9），`data\` 就地创建 |
| 绿色化 | `data\` 内出现配置/身份/`screenshots\`/`templates\`/`tmp\`；C 盘旧目录不再写入 |

## 7. 踩过的坑（改输入/视觉前必读）

1. **UIPI**：任务管理器等自动提权窗口，非提权客户端发的合成点击/拖动会被 Windows **静默丢弃**（`SendInput` 仍返回成功，窗口纹丝不动）。要操作提权窗口，客户端必须同样提权。
2. **坐标空间必须与 `screenshot_id` 对应**，详见 §3.1。
3. **按下点不能被挡住**：活动面板、终端等浮层会盖住目标窗口。验收时最小化其它窗口，或选不被覆盖的标题栏位置。
4. **激活点击与拖动之间要跳过双击时间**：500 ms 内在同一点再按下会被当成双击标题栏（窗口最大化到 `-9,-9`）。等 700 ms，并在 `IsZoomed()` 为真时先 `SW_RESTORE`。
5. **灰度不是亮度**：`RGB(255,158,96)` 与 `RGB(96,158,255)` 灰度都是 169，`detect_change` 看不出来。换色要换亮度。
6. **采样前要等合成**：点击后立刻 `BitBlt` 可能拿到上一帧，`sampleRegion` 里先 `DwmFlush()`。
7. **模板要在同一状态截取**：标题栏激活/未激活像素不同；窗口销毁的瞬间会被判成"巨大变化"。
8. **窗口移动有延迟**：拖动后立刻按"新位置"再按可能还没移到位。每段之前用 `GetWindowRect` 确认，或先 OpenCV 重新定位。
9. **屏幕边缘有第三方浮层**：这台机器常驻 `MyDockFinder`，会抢边缘点击；验收用"中部 300 px 闭环"的冒烟模式。
10. **MinGW + WinRT/UIA 的虚调用坑**：GCC 13.1 在 `-O2/-O3` 会把"声明为纯虚的 WinRT/UIA 接口调用"反优化成 `__cxa_pure_virtual`（运行期 `0xC0000005`，栈看起来是坏的）。`ocrlocator.cpp` 用 `-fno-devirtualize` 编译；另外 Windows SDK 的 `winrt/*.h` **不能**在 MinGW 下编译（大小写不敏感的文件系统上互相覆盖），OCR 的接口是按 ABI 手写的。
11. **UIA 的 COM apartment 坑（已修）**：`findText` 以前把 `RPC_E_CHANGED_MODE` 当"COM 不可用"，导致在已经初始化过 COM 的线程（Qt 主线程被 `OleInitialize` 拉成 STA）里直接降级 `UIA_UNAVAILABLE`。现在只在自己初始化成功时才配对 `CoUninitialize`。
12. **Qt 窗口的 UIA 包围盒是逻辑像素**：Qt 无障碍桥按 DIP 上报 `BoundingRectangle`，125% 下比物理像素小一圈（实测按钮 `784,477` vs UIA `980,596`）。原生 Win32 窗口没这个问题——所以真机 fixture 用裸 Win32 BUTTON。对真实 Qt 目标做 `text` 定位仍会偏，待修。
13. **本机 Git Bash 没有 coreutils（已修）**：pi 的 `shellPath` 指向 MSYS2 的 bash，非登录启动不读 `/etc/profile`。修法见 [`local-dev.md`](local-dev.md) §6.1（`node ~/.pi/agent/fix-bash-coreutils.mjs`，**不要跨 MSYS 根**）。
14. **本地平台的 TLS 坑**：`allow_insecure: true` 下网关仍只服务 TLS；客户端要 `YIUK_PLATFORM_TLS=1 + YIUK_PLATFORM_CA`，且 gateway 必须从证书目录启动。`yiuk-platform-interop` 是 GUI 子系统 exe，控制台看不到输出，**只看退出码**。
15. **中文控制台是 CP936**：UTF-8 内容在 `cmd`/Git Bash 里显示成乱码，看文件用编辑器。

## 8. 未做与已知限制

| 项 | 状态 |
| --- | --- |
| 多显示器 / 混合 DPI（100/150%、负原点、热插拔）+ `monitor_id` | 未做；本机单屏 2560×1600@125%，**无法在本机验证** |
| OCR 的 ROI 裁剪 | 未做；现在每次整帧识别（整屏耗时**未测**；900×340 实测 29 ms）。ROI 缓存模块是通用的，扩展时要处理裁剪偏移 |
| 非原生 Win32 目标（Qt/WPF）的 UIA 坐标 | 已验证 Qt 上报逻辑像素（§7.12），未做修正 |
| Windows CI | 已加 `windows-qt` job（Qt 6.11.1 + MinGW 13.1 + OpenCV 4.12 缓存 + ctest），**还没在 GitHub 上跑绿过** |
| 安装器（ISCC）重打 / 安装卸载回归 | 未做（绿色 zip 已重打） |
| Edge → 抖音收藏夹回归 | 未做；需设备所有者授权，遇人机验证用 `ask_user` 请本人完成 |
| UIA 命中率三场景实测 | 未做（只测了遍历耗时；空文档 0 节点约 320 ms） |
| 平台已知薄弱点 #3/#4/#5/#7/#8/#11 | 未修，见 [`key-architecture.md`](key-architecture.md) §4 |

## 9. 下一步（按顺序）

1. **P6 收尾**：`windows-qt` CI 第一次跑绿（失败先看 Qt/工具链缓存的那两步）；`ISCC` 安装器重打 + 安装/卸载回归。
2. **多显示器 / 混合 DPI**：`monitor_id` 限定搜索范围 + 与 `region` 的互斥规则；在有多屏的机器上按 100/125/150% 与负原点各测一遍。
3. **OCR ROI 裁剪**：把 ROI 缓存扩展到 OCR（裁剪 + 偏移回写），目标是把 400 ms 降到 60 ms 级。
4. 平台薄弱点 #3/#4/#5/#7/#8/#11。

## 10. 交接检查清单

- [ ] `gofmt -l .` 空、`go vet ./...` 干净、`go test ./...` 全绿（含 `-race` 关键包）
- [ ] `cd qt-client\build-qt; ctest --output-on-failure` 5/5 通过（真机项拿不到前台窗口时算 SKIP）
- [ ] 互操作 `INTEROP EXIT=0`，平台审计 6 条 `native_submitted`
- [ ] 客户端启动后 `data\` 里有配置与身份，`%LOCALAPPDATA%` 旧目录不再被写
- [ ] 没有残留的 `platform-core` / `platform-gateway` / `dev-redis` / `yiuk-input-fixture` 进程
- [ ] 没有把 `platform.json` / `local-api.json` / `data\` / `tmp-tls\` 提交进仓库（`.gitignore` 已覆盖）
- [ ] `Release\` 里的包与当前 HEAD 一致（版本号来自 `qt-client/CMakeLists.txt`，改版本必须重新构建）
