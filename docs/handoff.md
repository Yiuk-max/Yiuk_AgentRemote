# 交接文档：MCP 视觉定位与鼠标增强

> 写于本轮工作结束时。目的：让下一位接手的人不用读全部历史，就能知道**现在到哪一步、改动在哪、怎么验证、下一步做什么**。
>
> 相关文档：
> - 目标（产品口径）：[`mcp-vision-mouse-plan.md`](mcp-vision-mouse-plan.md)
> - 实施计划（P0–P6 清单与门禁）：[`mcp-vision-mouse-implementation-plan.md`](mcp-vision-mouse-implementation-plan.md)
> - P0 实测数据与工具链坑：[`spike/vision/README.md`](../spike/vision/README.md)

## 1. 一句话状态

**P0 完成、P1 完成约 95%（只差本机 bridge）、P2 完成、P3 进行中（UIA 文本定位已落地）。**
原生操作通道（`execute_native`）已从 MCP 侧协议、Go 平台、Go 客户端一路通到 Qt 客户端，并用真实 Qt ↔ 真实 Go 平台验证过。
P2 实质完成：客户端有了 `yiuk-vision`（抓屏/快照/节流/设备门）与 `yiuk-input`（原生鼠标）两个静态库，
`take_screenshot` / `click` / `move_mouse` 都优先走原生通道，变化检测在设备侧完成，
临时截图由 `screen.release` + 每会话保留 5 张 + 定时清理三重兜底（见 §2.1）。
原生 `click` / `move_mouse` 已用 fixture 窗口做过真机验收（左半区收到左键、右半区收到移入，见 §5.5）。
P3 第一步已完成：`find_on_screen`（UIA 文本定位）是第 23 个工具，返回截图坐标并可直接 click；
图像模板匹配（OpenCV）、OCR 兜底与 `drag`（P4）仍未实现。

## 2. 现在到底有什么

### 2.1 已实现（可运行、有测试）

| 层 | 内容 | 关键文件 |
| --- | --- | --- |
| 协议 | `execute_native` 帧；`operation`/`arguments`/`client_version`/`capabilities` 字段与边界 | `internal/platform/protocol/protocol.go` |
| 配置 | `native_argument_max_bytes`（默认 128 KiB，且 ≤ `max_frame_bytes/2`） | `internal/platform/config/config.go` |
| 设备表 | 能力与版本随注册上报、重连整体覆盖、快照深拷贝 | `internal/platform/device/manager.go` |
| 平台业务 | `handleExecuteNative`：白名单 7 操作、能力校验、已批准会话、任务限额、`request_id` 幂等、`result_unknown` 语义；与命令共用 `submitTask` | `internal/platform/core/handler_native.go`、`handler_command.go` |
| Go 客户端 | `InvokeNative`（暂未注册成 MCP 工具）、会话→设备能力解析、信封解码、错误语义 | `internal/platformclient/tools_native.go` |
| Go 客户端并发 | 单读者 + `correlation_id → 等待通道`，删掉整趟持锁的 `requestMu` | `internal/platformclient/dispatch.go`、`client.go`、`transport.go` |
| Qt 客户端 | `NativeOperationDispatcher`：注册表即能力、线程池执行、取消/超时/超大结果/去重 | `qt-client/src/nativeops.{h,cpp}` |
| Qt 视觉内核 | `yiuk-vision` 静态库：GDI RAII 抓屏（CAPTUREBLT、Per-Monitor V2 物理像素）、布局版本、按宽度缩放、PNG/JPEG 落盘 | `qt-client/src/vision/screengrab.{h,cpp}` |
| Qt 快照与节流 | `SnapshotStore`（每会话 5 份、布局变化失效、临时文件回收）、`SessionLimiter`（会话级互斥 + 500ms 最小间隔、排队可取消）、`VisionService`（唯一入口） | `qt-client/src/vision/{snapshotstore,sessionlimiter,visionservice}.{h,cpp}` |
| Qt 首个操作 | `screen.capture`：支持 `max_width` / `format` / `save`，返回 `screenshot_id` + 几何 + `layout_version` + `path`，仍然不回图片本体 | `qt-client/src/platformevents.cpp` |
| MCP 抓屏 | `take_screenshot` 优先原生通道（能力检查 → `screen.capture` → 既有 `download_file` 取图），不可用时回退脚本；结果带 `capture_source` | `internal/agenttools/native.go`、`desktop_screenshot.go` |
| Qt 原生鼠标 | `input.click` / `input.move`：截图坐标 → 物理坐标（`VisionService::toScreenPoint`）、SendInput、确定性 ease-in-out、`ButtonGuard` 保证抬起、点击前后局部变化采样 | `qt-client/src/input/inputcontroller.{h,cpp}`、`platformevents.cpp` |
| MCP 鼠标 | `click` / `move_mouse` 优先原生通道；只有 `UNSUPPORTED_CLIENT/OPERATION` 回退脚本，`STALE_SCREENSHOT` 等业务错误原样上报；结果带 `input_source` | `internal/agenttools/desktop_click.go`、`desktop_input.go`、`desktop_input_native.go` |
| 设备级门 | `DeviceGate`：进程级互斥，抓屏与输入动作排队，排队可取消 | `qt-client/src/vision/devicegate.{h,cpp}` |
| 临时截图回收 | 新原生操作 `screen.release`（只删截图目录内）；MCP 读完图片后尽力回收；每次抓屏清理 5 分钟前的残留 | `internal/agenttools/desktop_screenshot.go`、`internal/platform/core/handler_native.go`、`platformevents.cpp` |
| Qt 文本定位 | `textlocator`：UIA 控制视图、节点/时间上限、全半角与空白归一化、exact/contains、control_type 与 region 过滤、阅读顺序排序 | `qt-client/src/vision/textlocator.{h,cpp}` |
| 原生 `vision.find` | 参数解析 + `VisionService::searchText`（确保坐标基准 → UIA → 换算成截图坐标 → JSON） | `qt-client/src/platformevents_vision.cpp`、`platformevents.cpp` |
| MCP `find_on_screen` | 第 23 个工具：校验/转发/登记坐标基准；能力缺失返回 `UNSUPPORTED_CLIENT`，**没有脚本回退** | `internal/agenttools/desktop_vision.go`、`catalog.go` |
| Qt 接线 | 注册上报能力与版本；`execute_native` → 分发；结果走 `command_output`+`command_result`；取消/会话结束联动 | `qt-client/src/platformclient.{h,cpp}`、`platformevents.cpp`、`platformlink.cpp`、`mainwindow.h` |

### 2.2 测试与验证（当前全绿）

```powershell
go test ./... -count=1                          # 17 个包通过
go test -race ./internal/platform/gateway/ ./internal/platformclient/
gofmt -l .                                      # 必须空输出
go vet ./...

# Qt（需要 QT 在 PATH）
& "D:\QT\Tools\Ninja\ninja.exe" -C qt-client/build-qt
.\qt-client\build-qt\yiuk-nativeops-test.exe    # 18 项断言，含真实抓屏
```

用例数量：平台侧原生通道 10 个（`internal/platform/gateway/native_test.go`）、
Go 客户端 5 个（`internal/platformclient/native_test.go`）、Qt 分发器 18 项
（`qt-client/tests/nativeops_test.cpp`）、Qt 视觉内核 78 项（`qt-client/tests/vision_test.cpp`，含文本归一化与一次真实 UIA 查询）、Qt 原生输入 20 项
（`qt-client/tests/input_test.cpp`）、Go 侧原生抓屏 4 个用例含 3 个畸形信封子用例
（`desktop_screenshot_native_test.go`）、Go 侧原生鼠标 4 个用例（`desktop_input_native_test.go`）、
Go 侧 `find_on_screen` 4 个用例（`desktop_vision_test.go`）。

端到端（真实 Qt ↔ 真实 Go 平台）已验证过一次，命令见 §5.3。平台日志应出现：
`command_submitted` → `command_finished` → `native_submitted operation=screen.capture` →
`command_finished succeeded` → `session_closed`。

### 2.3 尚未实现

- **P1 剩余**：本机 bridge（`qt-client/src/localbridge.cpp` + `localtools.cpp`）的异步完成与有界原生请求体。
  只影响"没有平台"的本地 demo 后端；平台链路不受影响。
- **P2 剩余**：无（原生 `click` / `move_mouse` 已由 `yiuk-input-fixture` 真机验收，见 §5.5）。
- **P3 剩余**：图像模板匹配（需把 OpenCV 接进 Qt 构建）、OCR 兜底、`timeout_seconds` /
  `wait_until_found` 等待语义、固定素材集与命中率验收。
- **P3 起未做**：UIA/OCR 文本定位、`find_on_screen`/`click_target`/`drag`、
  模板管理、混合 DPI、ROI 缓存、发布。
- `screen.capture` 现在有 MCP 入口了，但只是 `take_screenshot` 的实现细节：
  没有独立的原生工具，`InvokeNative` 也不直接暴露给 Agent（工具契约留到 P3 冻结）。

## 3. 未提交的改动（重要）

工作区有 **19 个已修改文件 + 31 个未跟踪条目**，本轮全部未提交。`git status --short` 现状：

- 修改：`docs/{README,development,key-architecture,local-dev}.md`、
  `internal/platform/{config,protocol}`、`internal/platform/core/{convert,handler,handler_command}`、
  `internal/platform/device/manager.go`、`internal/platformclient/{client,transport}`、
  `qt-client/CMakeLists.txt`、`qt-client/src/{mainwindow.h,platformclient.h,platformclient.cpp,platformevents.cpp,platformlink.cpp}`、
  `qt-client/tests/platform_interop.cpp`
- 新增（源码）：`internal/platform/core/handler_native.go`、`internal/platformclient/{dispatch.go,tools_native.go}`、
  `internal/platform/gateway/native_test.go`、`internal/platformclient/native_test.go`、
  `internal/agenttools/native.go`、`internal/agenttools/desktop_screenshot_native_test.go`、
  `internal/agenttools/desktop_input_native.go`、`internal/agenttools/desktop_input_native_test.go`、
  `qt-client/src/nativeops.{h,cpp}`、`qt-client/src/vision/{screengrab,snapshotstore,sessionlimiter,devicegate,visionservice}.{h,cpp}`、
  `qt-client/src/input/inputcontroller.{h,cpp}`、
  `qt-client/tests/nativeops_test.cpp`、`qt-client/tests/vision_test.cpp`、`qt-client/tests/input_test.cpp`、
  `docs/mcp-vision-mouse-plan.md`、`docs/mcp-vision-mouse-implementation-plan.md`、
  `spike/vision/`（P0 素材/基准/探针源码）

**提交前必读**：

1. 不要 `git add -A`。`spike/vision/` 里只有源码该入库；`build*/`、`out/`、`third_party/`
   已由 `spike/vision/.gitignore` 排除，`git status -uall spike/vision` 应只列出 19 个源码文件。
2. 仓库强制 LF（`.gitattributes` 的 `* text=auto eol=lf`）。用 PowerShell 的 `Set-Content`/`Out-File`
   改文件会引入 CRLF，提交前跑一次转换（本轮已用 Python 统一过）。
3. 提交建议拆成四个可回归的提交：
   ① P0 spike + 三份文档；② 平台侧原生通道（protocol/config/device/core + gateway 测试）；
   ③ Go 客户端并发与 `InvokeNative`；④ Qt 分发器 + `screen.capture` + interop。

## 4. 关键设计与不变量（改动前必须知道）

1. **能力不是授权**。`capabilities` 只决定"这个操作能不能用"，授权仍然只由已批准会话决定；
   平台与客户端都各自校验一次白名单，任何一层都不能省。
2. **原生操作与命令共用任务机制**：同一 `task.Manager`（`max_tasks_per_session=2`）、同一
   `request_id` 幂等域、同一取消语义（`cancel_command`）、同一终态回收。不要为 native 新开一套。
3. **业务失败 ≠ 任务失败**。设备回 `command_result succeeded` + stdout 信封
   `{"ok":false,"code":"NOT_FOUND",...}`；断线/超时必须是 `result_unknown`（可能已生效，禁止直接重试）。
4. **不返回图片**。`screen.capture` 只回几何；图片仍走既有的截图/下载通路（P2 统一）。
5. **原生操作串行与设备资源**：Qt 侧线程池上限 2；P2 起所有抓屏/视觉必须共用同一把会话级互斥 +
   500ms 最小间隔（计划 §2.3），否则低内存设备会被拖垮。
6. **旧客户端不受影响**：平台只在设备自报能力时才下发 `execute_native`；未知能力名在注册时被静默丢弃。

## 5. 怎么验证（复制即用）

### 5.1 Go

```powershell
go test ./... -count=1
go test -race ./internal/platform/gateway/ ./internal/platformclient/ -count=1
go test ./internal/platform/gateway/ -run TestNative -v
go test ./internal/platformclient/ -run TestInvokeNative -v
```

### 5.2 Qt（无平台）

```powershell
$env:PATH = "D:\QT\6.11.1\mingw_64\bin;" + $env:PATH
cmake -S qt-client -B qt-client/build-qt -G Ninja -DCMAKE_BUILD_TYPE=Release `
  -DCMAKE_PREFIX_PATH=D:/QT/6.11.1/mingw_64 `
  -DCMAKE_CXX_COMPILER=D:/QT/Tools/mingw1310_64/bin/g++.exe `
  -DCMAKE_MAKE_PROGRAM=D:/QT/Tools/Ninja/ninja.exe -DYIUK_BUILD_TESTS=ON
& "D:\QT\Tools\Ninja\ninja.exe" -C qt-client/build-qt
.\qt-client\build-qt\yiuk-nativeops-test.exe      # 原生分发器
.\qt-client\build-qt\yiuk-vision-test.exe         # 快照/节流/缩放/编码 + 一次真实抓屏
.\qt-client\build-qt\yiuk-input-test.exe          # 轨迹/变化判据/采样区域
```

### 5.3 端到端（真实 Qt ↔ 真实 Go 平台）

平台三条命令（本机明文，端口 25664/25670/25671/6379），配置要点：
`allow_insecure: true`、`player_password: "interop-player-password-123456"`、`log_file` 用**当前目录**的相对路径
（写成 `tmp-interop/core.log` 会启动失败）。

```powershell
cd E:\Yiuk_AgentRemote
go build -o tmp-interop/dev-redis.exe ./cmd/dev-redis
go build -o tmp-interop/platform-core.exe ./cmd/platform-core
go build -o tmp-interop/platform-gateway.exe ./cmd/platform-gateway
# 依次后台启动 dev-redis / platform-core / platform-gateway（工作目录 tmp-interop）

$env:PATH = "D:\QT\6.11.1\mingw_64\bin;" + $env:PATH
$env:YIUK_PLATFORM_ADDRESS   = "127.0.0.1:25664"
$env:YIUK_PLATFORM_PASSWORD  = "interop-player-password-123456"
$env:YIUK_INTEROP_NATIVE     = "1"
$env:YIUK_DEVICE_ID          = "123456789"
.\qt-client\build-qt\yiuk-platform-interop.exe
```

注意：平台是内存态，**每次重跑前重启 core+gateway**，否则第二次注册会因为密码已轮换而拿到新设备码、
互操作测试失败（这是既有设计，不是 bug）。

`YIUK_INTEROP_NATIVE=1` 时这套程序现在连跑三段原生操作，平台日志里应依次出现：

```text
native_submitted operation=screen.capture          # 只回几何
native_submitted operation=screen.capture          # max_width=800 + save=true，校验 PNG 真的落盘
native_submitted operation=screen.release          # 回收临时截图，校验文件已删除
native_submitted operation=vision.find             # UIA 文本定位，校验返回截图坐标与 screenshot_id
```

进程退出码 0 才算通过；跑完 `%LOCALAPPDATA%\Yiuk\DeviceAssist\screenshots` 里不应留下文件。

### 5.5 原生输入真机验收（会真的动鼠标）

`yiuk-input-fixture.exe` 打开一个置顶窗口：**左半区等一次左键按下，右半区等一次鼠标移入**，
两个条件都满足就打印 `FIXTURE OK` 并退出。互操作程序在 `YIUK_INTEROP_INPUT=1` 时按
`GetWindowRect` 找到这个窗口，把物理坐标换成截图坐标，先 `input.move`（300ms 平滑移动）再
`input.click`，平台日志会多出 `native_submitted operation=input.move/input.click`。

```powershell
cd E:\Yiuk_AgentRemote
.\qt-client\build-qt\yiuk-input-fixture.exe          # 另开一个窗口跑，看到 FIXTURE READY
# 平台三条命令仍然照 §5.3 启动，然后：
$env:YIUK_INTEROP_INPUT = "1"
.\qt-client\build-qt\yiuk-platform-interop.exe
```

通过判据：fixture 输出 `FIXTURE OK clicks=1 hover=1 ...`，互操作程序退出码 0。
测试结束后 fixture 自己退出；如果它打印 `FIXTURE FAIL`，说明输入没落到窗口上
（锁屏、窗口被遮挡、坐标换算错误）。

### 5.6 `detect_change` 真机验收（会真的点两下）

`yiuk-input-fixture` 的左半区**每次点击都换亮度**（深/浅两组灰度），右半区点击**不改任何像素**。
互操作程序在 `YIUK_INTEROP_DETECT_CHANGE=1` 时各点一次，并按与 Go 侧相同的判据
（强变化像素占比 ≥ 0.002 或平均差 ≥ 0.02）检查结果：

```powershell
.\qt-client\build-qt\yiuk-input-fixture.exe --expect-clicks 4
# 平台照 §5.3 启动后：
$env:YIUK_INTEROP_DETECT_CHANGE = "1"
.\qt-client\build-qt\yiuk-platform-interop.exe
```

实测（连续两次）：

```text
"interop-native-change" changed_ratio 1 diff_ratio 0.8039 changed true  expect true
"interop-native-static" changed_ratio 0 diff_ratio 0     changed false expect false
INTEROP EXIT=0
FIXTURE OK clicks=4 hover=1 hover_in_left=1 click_in_right=1
```

两个坑值得记住：

1. **颜色必须换亮度**。采样是灰度（`(R+G+B)/3`），`255,158,96` 与 `96,158,255` 的灰度都是 169，
   fixture 早期版本就是这样"换了色却测不出变化"。
2. **判定前不能让目标窗口消失**。fixture 原来在第 3 次点击后立即退出，右半区的"点击后"采样
   拍到的是它消失后的桌面，`diff_ratio` 高达 0.88 —— 看起来像误报，其实是目标没了。
   现在多补一次点击（`--expect-clicks 4`），保证最后两次采样时窗口还在。

`sampleRegion` 在采样前会 `DwmFlush()`，避免点击后立刻 BitBlt 拿到上一帧。

### 5.4 P0 基线（视觉相关，重跑约 5–15 分钟）

```powershell
cd spike\vision
.\build\make_fixtures.exe out/fixtures
.\build\vision_selftest.exe
.\build-cv\vision_bench.exe --manifest out/fixtures/manifest.json --out out/p0-bench.json
```

OpenCV 静态构建的完整命令在 `spike/vision/README.md` §1。

## 6. 环境与工具链坑（这一台机器）

1. **没有 coreutils**：Git Bash 里没有 `ls`/`grep`/`head`/`sed`/`rm`/`cat`。用 `rg`、PowerShell 或 Go。
   命令输出别用 `| powershell -Command` 套两层，会静默吞掉结果。
2. **中文控制台乱码**：CP936；看 UTF-8 文件请用编辑器或 `read` 工具，别据此判断文件损坏。
3. **MinGW GCC 13.1 的虚调用坑**：`-O2/-O3` 会把纯虚接口（WinRT/UIA）的虚调用编译成
   `__cxa_pure_virtual`，运行期 `0xC0000005`。现有缓解：`probe_winrt_ocr` 目标加 `-fno-devirtualize`。
   **P3 引入 UIA/OCR 到 Qt 时必须显式定案**（`-fno-devirtualize` 或所有调用改显式 vtable 索引），否则
   Release 版必崩。详见 `spike/vision/README.md` §3.4。
4. **Windows SDK 的 `winrt/*.h` 在 MinGW 下不能编译**（`Windows.Foundation.h` 与 `windows.foundation.h`
   大小写冲突）。OCR 走自包含 ABI 声明（`spike/vision/src/probe_winrt_ocr.cpp`），可直接照抄。
5. **Qt 构建目录的编译器会粘在 cache 里**：`build-qt` 一旦用错编译器（例如 PATH 里有 msys64 的 c++），
   重新 `cmake -DCMAKE_CXX_COMPILER=...` 也不会改。症状是 activity-panel 目标用 `C:\msys64\...\c++.exe`。
   处理：删掉 `build-qt` 重新按 §5.2 生成（本轮已做过一次）。
6. **`Out-File`/`Set-Content` 会写 CRLF**：本仓库要求 LF，见 §3。
7. **OCR 输入细节**（P3 会用到）：`IBufferFactory::Create` 只设 `Capacity`，必须再写 `Length`，
   否则 `CreateCopyFromBuffer` 返回 `0xC00D7170`；中文 OCR 结果字间带空格（`收 藏 首 页 设 置`）。

## 7. 下一步（按顺序）

### 7.1 收尾 P1（半天）

1. 本机 bridge：`LocalBridge` 目前同步处理并在 5s 后断连，需要支持"异步完成 + 有界原生请求体"。
   注意它只服务本地 demo 后端，不要为它开新的未鉴权端口；仍然是 loopback + token + 会话校验。
2. 顺便确认 `localtools.cpp` 的工具表是否要把 `screen.capture` 暴露给本地 backend（可选，不阻塞）。

### 7.2 P2：统一抓屏与快照（第一批视觉的前置）

抓屏、快照、节流、原生鼠标都已落地（见 §2.1）。剩下：

1. 真机验收：`click` / `move_mouse` 的原生分支只在单元测试里验证过，需要在真实 GUI 会话里
   手动点一次、悬停一次（`yiuk-platform-interop` 只跑 `screen.capture`，不注入键鼠）。
2. 验收：混用 `take_screenshot` → 后续坐标点击时 ID 与坐标始终一致；旧客户端脚本路径不退化。
3. 之后进入 P3（UIA/OCR 文本定位 + `find_on_screen`），见实施计划 P3 清单。

### 7.3 P3 起的提醒

- 文本定位优先 UIA（C++ COM，实测 0.048ms/节点，Edge 746 节点 35ms），OCR 兜底；
  两者都要设节点/时间上限，超限降级而不是无限遍历。
- 跨缩放命中率目前只有 60.1%（文字控件失败于 0.72–0.93 分）；P3 要么用 UIA/OCR 处理文字，
  要么做尺度精修。**不要**靠降低阈值掩盖。
- 负样本 12/496 误报的两类原因（带 padding 的图标模板、近义文本）已写在 P0 README §5.3。

## 8. 已知风险 / 未验证

| 项 | 状态 |
| --- | --- |
| 全屏单次 ≤300ms | 未达标：OpenCV 中位 266ms / p95 653ms（8 尺度全屏），ROI 未测 |
| 跨缩放 ≥95% | 未达标：60.1% |
| 负样本零命中 | 未达标：12/496 |
| 真机长稳（20 次抓屏/视觉） | 未测，P2 补 |
| `-fno-devirtualize` 是否影响性能 | 未测（只在探针里有 16–31ms 的 OCR 数据） |
| 端到端时延（MCP→平台→设备→回传） | 未测 |
| 多显示器 / 混合 DPI / 负坐标 | 未测（P5） |
| Qt 原生通道在真机 GUI 里的人工验收 | 只用互操作程序验证过协议往返，未在真实 GUI 会话里手动点过 |
| MCP `take_screenshot` 原生全链路 | 未验：互操作里已跑通 `screen.capture`，但"原生抓屏 → `download_file` 取图 → Agent 收到图片"只在单元测试里验证过 |
| `find_on_screen` 命中率 | 未测：UIA 链路已跑通（0 节点时约 320ms），但记事本/Edge/资源管理器三场景的命中率与 300ms 预算未实测 |
| `detect_change` 真机行为 | 已验证：换亮度的区域 `changed=true`（changed_ratio 1 / diff_ratio 0.80），静态区域 `changed=false`（0 / 0），连续两次一致 |
| 原生截图临时文件的长期增长 | 机制已齐（release + 5 张上限 + 定时清理），但未长跑确认 |
| 原生 `click` / `move_mouse` 真机行为 | 已验证：`yiuk-input-fixture` 收到左半区左键与右半区移入（2 次运行均 OK）；仍未覆盖多显示器/混合 DPI |

## 9. 交接检查清单

- [ ] `go test ./...`、`gofmt -l .`、`go vet ./...` 全绿
- [ ] `yiuk-nativeops-test.exe` / `yiuk-vision-test.exe` / `yiuk-input-test.exe` 通过
- [ ] 端到端互操作通过（§5.3），并确认平台日志有 `native_submitted`
- [ ] `git status -uall spike/vision` 只有源码，没有 `build*/out/third_party`
- [ ] 没有残留的 `platform-core`/`platform-gateway`/`dev-redis` 进程
- [ ] 没有 `tmp-*` 目录、没有把 `platform.conf`/`local-api.json` 等凭据文件带进提交
- [ ] 提交信息里写清"P0/P1 完成内容 + 未达标的门禁项"，不要写成视觉功能已完成
