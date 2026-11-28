# MCP 视觉定位与鼠标增强：代码级实施计划

> 目标来源：[mcp-vision-mouse-plan.md](mcp-vision-mouse-plan.md)。本文依据当前代码制定实施顺序，不代表功能已实现。
>
> 状态：完成文档与代码勘察；待执行 P0 技术验证。本文中的新增帧、字段、文件名、配额为实施建议，P0 通过后冻结契约。

## 1. 项目认识与当前基线

项目是 Windows 远程协助系统：设备 Qt 客户端与操作者 MCP 主动出站连接 Go 平台；平台 core 管授权与业务，gateway 管 socket，Redis 只管路由。设备码不是凭据，临时密码或所有者审批才授予会话权限。

| 层 | 当前入口与事实 | 本次影响 |
| --- | --- | --- |
| MCP 契约 | `internal/agenttools/catalog.go`，25 个工具（P3 加 `find_on_screen`，P4 加 `drag` / `click_target`），schema 是校验唯一来源 | 新增 schema、注解、错误与注册表覆盖 |
| 桌面组合 | `desktop*.go` + `scripts.go`，PowerShell 抓屏与输入 | 新客户端逐步迁到原生路径；旧客户端保留脚本 |
| 截图状态 | `desktop_state.go`，Go 进程内每会话保存最近 5 个 ID/几何；500ms 节流 | 原生路径必须统一状态与锁，不能平行生成两套 ID |
| 操作者连接 | `internal/platformclient/client.go`，`requestMu` 串行化请求 | 长等待不能阻塞取消；需要可并发的响应分发 |
| 平台协议 | `internal/platform/protocol/protocol.go`，版本 1 的 JSON 信封 | 当前执行帧只支持命令文本，但协议可扩展 |
| 平台执行 | `core/handler_command.go` + `task/manager.go` | 复用会话授权、任务限额、request_id 幂等、取消、终态回收 |
| Qt 执行 | `src/platformclient.cpp` → `platformevents.cpp` → `PowerShellRunner` | 新增明确的原生操作分发，不把 JSON 伪装成脚本 |
| 本机 bridge | `src/localbridge.cpp` + `localtools.cpp` | 已有回环 HTTP + token，但同步处理、5 秒连接定时器、32KiB 请求体不能直接承载新能力 |
| 构建 | `qt-client/CMakeLists.txt`，版本 0.1.8，Qt Widgets/Network | 没有 OpenCV、vcpkg、FetchContent 或第三方许可清单 |
| 工具链 | `docs/local-dev.md`：Qt 6.11.1 + MinGW 13.1 | 不可直接链接 MSVC 版 OpenCV；UIA/WinRT OCR 需验证 |
| CI | `.github/workflows/ci.yml`，只有 Linux Go 检查与产物 | 补 Windows Qt/视觉构建与无输入注入测试 |
| UIA 先验 | `spike/uia/README.md`：三个场景定位 92%，全树遍历昂贵 | 复用查询场景，不把 PowerShell spike 当作 C++ 实现验证 |

勘察时执行 `go test ./...`：通过。没有执行 OpenCV 性能测试、Qt 新模块构建或真机输入验收；默认跳过的桌面测试不算真机通过。

必须保持 [key-architecture.md](key-architecture.md) 的全部不变式，尤其是：已批准会话、绑定操作者连接与设备连接、断线不重放、未知结果不当失败重试、隐私正文不写平台日志。

## 2. 先冻结的设计决策

### 2.1 通道：推荐原生操作帧，不经 PowerShell 或本地 HTTP 绕行

建议新增 `execute_native` 帧，携带 `operation` 与结构化 `arguments`，复用 `command_accepted` / `command_output` / `command_result` / `get_command_result` / `cancel_command` 任务生命周期。

```text
MCP 工具 → agenttools 原生桌面调度 → platformclient
        → core：授权、能力、限额、幂等 → gateway：原样投递
        → Qt NativeOperationDispatcher → VisionService / InputController
        ← 有界 JSON 结果 + 既有任务终态
```

- 内部操作白名单：`screen.capture`、`input.click`、`input.move`、`input.drag`、`vision.find`、`vision.click_target`、`vision.save_template`。前两项用于复用快照与变化检测，`vision.click_target` 用于保证找与点在客户端内原子执行。
- 新的 native backend 接口与公开 MCP 工具目录分开，避免把内部操作错误计入 `BackendTools()` 或对 Agent 暴露内部帧。
- 将 `handler_command.go` 的共同授权/任务创建部分提取为共享逻辑；native 不能绕过它，也不能只依赖 MCP 的能力检查。
- `execute_native` 不接收脚本文本，不支持任意 DLL/函数调用。Qt 同样校验白名单、会话状态、参数与 deadline。
- native 默认 10 秒、最多 25 秒；MCP 调用总预算 30 秒，取消与清理用剩余预算。所有重试、等待锁、OCR、settle 共用同一个 deadline。
- 结果建议沿用输出通道传一份最多 32KiB 的 JSON 业务信封；任务状态表示执行生命周期，信封表示 `NOT_FOUND` 等业务结果。禁止视觉图片、模板内容进入输出或活动日志。平台只转存有界结果，不写审计正文；Qt 活动面板与任务历史仅保存操作名、状态、耗时，不持久化文本查询、OCR/UIA 结果正文或图像参数。
- `image_b64` 的 64KiB 指 PNG 解码后文件字节数，base64 约 88KiB；native 参数建议单独限制为 128KiB，仍受 256KiB 帧预算约束，不放大既有命令 32KiB 上限。使用 Qt 解码 PNG，不引入 OpenCV `imgcodecs`。
- 解码图像另限制宽高/总像素（建议模板不超过 2048×2048）；小压缩文件不能无限解码占用内存。
- gateway/core 的 gRPC 接口目前传原始帧，预计不需改 `edge.proto`，以实现时验证为准。

### 2.2 能力与发布兼容

- 平台认证响应上报原生通道支持；设备注册上报 `client_version`、`capabilities`，平台存入设备快照并在设备列表/状态中返回。
- 能力按操作名声明，仅在对应模块实际可用时发布；OCR 语言支持作为运行时诊断信息，不假定每台 Windows 都有中英语言包。
- MCP 根据会话找到目标设备与当前能力；平台再校验当前连接能力。重连时覆盖能力集合，缺字段按空集合，不复用旧连接缓存。
- 旧 Qt：新工具、非零 `duration_ms` 返回 `UNSUPPORTED_CLIENT`；原 `take_screenshot` / `click` / 默认瞬移继续脚本路径。
- 新 Qt：截图、坐标点击、移动、视觉定位统一原生路径；不能只迁移 `find_on_screen` 而让截图仍在另一套状态里运行。
- 旧平台：没有原生通道能力时明确报升级提示，不向未知平台试发输入操作，也不静默回退执行新能力。
- 滚动升级顺序：平台 → Qt → MCP；“同步发版”不等于要求所有用户同时升级。最低配套版本在发布前确定，不凭空指定。

### 2.3 快照、坐标、锁

- 原生端新增 `ScreenSnapshotStore`：会话 ID、12 位截图 ID、物理虚拟桌面矩形、输出截图尺寸、显示布局版本；最多保留 5 份元数据，不保留 5 张全屏位图。
- `ScreenGrabber` 以 Win32 GDI 虚拟桌面物理像素抓取为统一实现。现有代码是 PowerShell `System.Drawing.Graphics.CopyFromScreen`，不是 Qt/DXGI；因此“复用”实际需要先迁移公共抓屏实现。
- `take_screenshot` 原生分支与视觉定位共用该实现。截图仍可通过既有文件下载通道返回图片；find 不下载图片。
- 有最新截图时，find 的输出坐标使用其图像宽高，不因 ROI 改变原点；没有截图时允许建立默认 1568 宽坐标基准，并返回完整 geometry。find 每次最终采用的新画面生成新 ID 并发布为最新快照，不将新画面的候选标成旧截图 ID；候选 ID 必须能直接用于后续 `click` / `move_mouse` / `drag`。
- `bbox` 固定 `[left,top,right,bottom]`、右下开区间；`region` 同格式。坐标均为截图坐标；原生层集中换算到物理坐标。支持负的虚拟桌面物理原点。
- 布局或分辨率变化立即使旧 ID 失效；旧 ID 的新旧关系不等于屏幕内容没变，不能宣称 ID 校验能识别所有画面变化。
- 新客户端把会话级互斥与 500ms 抓屏间隔放在客户端，Go 旧锁仅保留给 legacy；另设设备级重操作/输入串行门，避免多个 MCP 或多个会话同时抓大图、拖鼠标。
- `click_target` 在同一设备级输入事务内定位、选择、校验、点击及变化采样，不在 Go 端拆成两个独立调用。等待目标出现时，每轮结束释放重操作锁，再等 500ms，不长时间霸占设备。
- 变化检测复用当前 192×108 局部采样与判据：强变化像素比例 ≥ 0.002 或平均差 ≥ 0.02；新旧路径使用同一组测试样本校准。内部 before/after 采样不生成面向 Agent 的新截图 ID。

### 2.4 补齐目标文档的契约歧义

| 问题 | 建议冻结语义 |
| --- | --- |
| `pick=unique` 与得分差冲突 | 默认只允许一个合格候选；两个即歧义。`best` 允许选最高分，但前两名差 < 0.03 仍拒点；`first` / `index:N` 是明确选择，N 从 0 开始 |
| `limit` 会隐藏歧义 | 先判歧义再截断返回；`limit=1` 不能使两个目标变成 unique。截断结果标注 `truncated`，索引只针对返回的稳定排序列表 |
| 排序 | 图像按 score 降序再按 top/left；文字 score 表示文本匹配质量，不当作图像置信度比较。`first` 按 top/left 的阅读顺序 |
| 没找到与超时 | find 正常无结果返回成功与空数组；等待超时带 `timed_out:true` 和已有候选；click_target 无合格候选返回 `NOT_FOUND`，不执行输入 |
| 最高分 | 图像保留阈值以下的最高有限得分供诊断；文字没有可比较得分时返回 null，不伪造 |
| `detect_change` 默认值 | click_target 默认 true，省略 settle 默认 250ms；显式 0 不得被当成未传。原 click 默认不变 |
| 文本/UIA 范围 | UIA 只接受当前桌面内可见、有非空矩形且与 region 相交的候选；过滤 offscreen，并验证命中点未被其他窗口遮挡。优先限定窗口与 control_type，禁止无界全树扫描 |
| UIA → OCR | UIA 无合格候选才 OCR；UIA 多候选不得靠 OCR 静默消歧。建议提供 `source:auto/uia/ocr`，显式 uia 不强制依赖 OCR |
| 模板引用 | text、image_path、image_b64、第三批 template_id 四选一；path 是设备端绝对路径，不是操作者本机路径 |
| 工具数量 | 当前 22；第一批 find 后 23；第二批 click_target/drag 后 25；第三批 save_template 后 **26** |
| 内存指标 | 同时记录操作端、设备端、平台。视觉增量 ≤50MB 与整条任务链峰值不超过旧链路基线是两项指标；达不到不得把“无泄漏”写成通过 |
| 调用时长 | 当前 `desktop.go` 截屏为 60s、脚本轮询额外加 15s，`type_text` 为 120s，不能以文档宣称已有统一 30s 硬限制；本期为新增/迁移原生桌面调用建立总预算，不擅自缩短通用命令接口 |

## 3. 分阶段执行清单

### P0：可行性与契约冻结（已完成，见 `spike/vision/README.md`）

- [x] 新建 `spike/vision/`：素材由代码生成（31 控件 × 3 DPI × 2 主题 + 4 张负样本），基准不注入输入。
- [x] OpenCV 4.12.0（`core`+`imgproc`，静态）与手写 NCC 用同一素材、同一参数对比；
      构建 31s 配置 + 67s 编译安装，exe 增量 **+5.7MB**（200KB → 5.9MB）。
- [x] 用本机同 ABI 的 MinGW 13.1 构建 OpenCV，禁用无关模块；未使用 MSVC 预编译库。
- [x] 实测 mask：OpenCV 4.12 的 `TM_CCOEFF_NORMED` **接受** mask；全 255 mask 与不传 mask
      结果一致（差 < 1e-4），部分 mask 被接受但数值正确性留到 P3 用素材验证。
      `patchNaNs` 在 4.12 返回 void，NaN 需自行统计；本次 1240 次调用修补数为 0。
- [x] MinGW 直连 UIA（`CoCreateInstance` 7.0ms；Edge 746 节点整棵树 35.5ms）与
      Windows OCR（语言 `en-US`/`zh-Hans-CN`，900×340 识别 29–41ms，词框可用），无需 MSVC helper。
- [x] 结论修正：OCR 与 UIA 都不需要 helper 进程。C++ COM 直连 UIA 在 Edge（746 节点）上是
      0.048 ms/节点、整棵树 35.5 ms，比 PowerShell 路线快两个数量级；但仍要设节点上限（≤5000）
      与时间上限（≤300 ms），超限降级到 OCR。
- [x] 已确认单显示器 125% 下的 GDI 抓屏链路 ≈58ms（抓 30.4ms + 灰度 15.2ms + 缩放 12.4ms）。
- [x] 因缺少可复现的旧任务链基线，回归调用数/误点数目标改为 P6 用真实链路重建，不沿用旧文档数字。
- [x] `spike/vision/README.md` 记录环境、复现命令、原始输出与结论。

**P0 新增的硬结论**：

1. **工具链阻塞项**：MinGW GCC 13.1 在 `-O2/-O3` 下把纯虚接口的 WinRT/UIA 虚调用反优化成
   `__cxa_pure_virtual`，运行期 `0xC0000005`。必须在 P1 定案：给相关目标加
   `-fno-devirtualize`，或所有 WinRT/UIA 调用改为显式 vtable 索引；两条都要进 Windows CI。
2. **正式路线定为 OpenCV**；手写 NCC 只作降级/对照。
3. **OCR 输入细节**：`IBufferFactory::Create` 只设 `Capacity`，必须补写 `Length`，
   否则 `CreateCopyFromBuffer` 返回 `0xC00D7170`。
4. **OCR 文本归一化**：中文结果字间带空格（`收 藏 首 页 设 置`），P3 的文本匹配必须先去掉 CJK 间空白。
5. 已冻结常量：阈值 0.88、尺度 0.75–1.5 步长 1.1、NMS IoU 0.3、金字塔 1/2 + 精修窗口
   = 模板尺寸 + 12px、抓屏节流 500ms、单次 find 300ms/100ms（ROI）。

**门禁状态（实测，详见 `spike/vision/README.md` §2.1 与 §5）**：

| 门禁 | 结果 | 后续动作 |
| --- | --- | --- |
| 同 DPI/同主题命中率 ≥95% | ✅ 100%（124/124） | 保持 |
| 跨缩放（100% 模板 → 150%） | ❌ 60.1%，失败集中在文字控件 | P3：文字走 UIA/OCR；尺度梯自适应 + 命中后局部尺度精修 |
| 负样本零命中 | ❌ 12/496（云下载图标、近义文本） | P3：图标模板去 padding / 用 mask；阈值不是唯一手段 |
| 全屏 ≤300ms | ⚠️ 中位 266ms / p95 653ms | P3：优先 ROI 缓存与自适应尺度数；450ms 以上必须有解释 |
| 带 region ≤100ms | ⏳ 未测 | P3 补测 |
| 额外内存 ≤50MB | ✅ +7.0MB | P6 用真实链路复测 |
| 手写 NCC 替代 OpenCV | ❌ 慢 4–12 倍，p95 差一个数量级 | 放弃，只留降级/对照 |

门禁结论：**OpenCV 路线保留，但"全屏 300ms"与"跨缩放 ≥95%"没有达标**，
这两项转成 P3 的验收项，不允许在 P3 完成前宣称本期目标达成。

### P1：原生任务通道、能力与取消（基础设施）

- [x] `protocol`：`execute_native` 帧、`operation`/`arguments`/`client_version`/`capabilities` 字段与边界；
      `device.Manager`、`core/convert.go` 复制并透传能力集合（`snapshot` 与 `protocolDeviceInfo` 都做深拷贝）。
- [x] `core`：抽出 `submitTask`（命令与原生操作共用提交路径）；native 白名单、能力检查、已批准会话、
      设备连接绑定、任务限额与幂等；`UNSUPPORTED_OPERATION` / `UNSUPPORTED_CLIENT` 与 result_unknown 语义明确。
      测试：`internal/platform/gateway/native_test.go`（9 个用例，含越权、幂等、跨会话复用、断线、能力覆盖与清理）。
- [x] `platformclient`：`InvokeNative`（内部接口，暂不暴露为 MCP 工具）、会话→设备能力解析（缺失时刷新一次）、
      stdout 业务信封解码、`UNSUPPORTED_CLIENT` / `RESULT_UNKNOWN` / `NATIVE_RESULT_INVALID` 语义。
      测试：`internal/platformclient/native_test.go`（4 个用例）。
- [x] 将 `requestMu` 整次等待改为 `correlation_id → 等待通道`（`internal/platformclient/dispatch.go`）：
      单读者 `dispatchMessages` 先 `cacheMessage` 再分发；没有等待者的帧只进缓存；
      连接初始化仍由 `connectMu` 单飞、写帧仍由 `writeMu` 串行；连接关闭时先把队列排干再退出，
      避免终态响应刚好排在关闭信号之后被丢掉。测试：并发两个原生请求、设备反序回复
      （`TestInvokeNativeConcurrentRequestsRouteByCorrelation`，旧实现下会超时），
      以及 `-race` 下全量 existing 测试。
- [x] Qt 新增 `NativeOperationDispatcher`（`qt-client/src/nativeops.{h,cpp}`）：操作注册表即能力来源，
      handler 跑在独立 `QThreadPool`（最多 2 并发），结果经 `QueuedConnection` 回主线程；
      主线程只做参数/会话校验与发帧，抓屏等重活不阻塞 GUI 与心跳。
      覆盖未知操作、取消（原子标记）、超时（先结算、丢弃迟到结果）、超大信封（32 KiB 上限）、
      request_id 去重；测试 `qt-client/tests/nativeops_test.cpp`（18 项断言，含真实抓屏 2560×1600）。
- [x] 第一个真实原生操作 `screen.capture`：`qt-client/src/vision/screengrab.{h,cpp}`（GDI + CAPTUREBLT，
      Per-Monitor V2 物理像素），只回几何、不回图片；平台端到端验证见下一条。
- [x] 端到端验证（真实 Qt ↔ 真实 Go 平台）：`yiuk-platform-interop` 以 `YIUK_INTEROP_NATIVE=1` 运行，
      设备上报 `capabilities:["screen.capture"]`，平台 `native_submitted` → 客户端分发器真实抓屏 →
      `command_finished succeeded`，会话正常关闭（平台日志三帧齐全）。
- [x] 把 native 纳入取消、会话关闭/过期、断线、退出清理。
      取消现在**等处理器收尾**（最多 `kCancelGraceMs = 2000`）再回报：处理器先完成就照实说
      `succeeded`，超过宽限才报 `cancelled`，不再“先喊已取消、鼠标键还按着”。
      会话关闭/过期 → `cancelSession`；平台断线 → `cancelAll`（不重放，避免一次点击变两次）；退出 → 析构里 `cancelAll` + 有界等待。
      测试：`yiuk-nativeops-test` 新增“完成早于宽限报 succeeded / 超过宽限报 cancelled / 宽限就是上限”三条。
- [x] 本机 bridge：新增 `POST /native` **异步**通道（`LocalBridge::setNativeHandler` + 一次性订阅
      `NativeOperationDispatcher::finished`），抓屏 30ms / OCR 300ms / 等目标 20s 都不再按住 GUI 线程；
      请求体上限按通道分开（普通工具 32 KiB，原生操作 128 KiB + 4 KiB 余量，与平台
      `native_argument_max_bytes` 对齐），超限先把体读掉再回 413（否则客户端只看到连接被重置）；
      仍是 loopback + Bearer token，并**再校验会话**（存在 → 已批准 → 能力存在）。
      Go 侧 `localbridge.Client` 实现 `NativeInvoker`，没有平台时 find/click_target/drag 也能用。
      测试：`yiuk-localbridge-test`（token/404/两个上限/异步回包 15 条）+ `internal/localbridge` 3 个 Go 用例。

**门禁**：模拟设备覆盖批准/拒绝/关闭/过期、越权、重复请求、跨会话 ID、限额、能力缺失、取消/断线/迟到结果；原有命令、上传下载与询问测试不退化。

### P2：统一快照与抓屏（视觉前置）

进行中。已完成的部分可直接运行并有测试（`qt-client/tests/vision_test.cpp`）：

- [x] `qt-client/src/vision/` 建成可复用静态库 `yiuk-vision`（`screengrab`、`snapshotstore`、
      `sessionlimiter`、`visionservice`），设备端、两个互操作程序与测试程序共用同一份实现，
      不再把新代码堆进 MainWindow。
- [x] GDI RAII（`GdiScope`）、物理桌面坐标、布局版本（虚拟桌面 + 每显示器矩形 + DPI 摘要）、
      按宽度平滑缩放、PNG/JPEG 落盘；抓屏实现从 `platformevents.cpp` 迁到 `VisionService`。
- [x] 会话级互斥 + 500ms 最小间隔放在客户端（`SessionLimiter`，条件变量、排队可取消），
      与服务端 legacy 脚本路径的 `desktopState` 互不替代。
- [x] 快照表 `SnapshotStore`：会话 → 12 位 `screenshot_id` + 几何 + 布局版本，最多 5 份，
      布局变化立即失效；临时截图落 `%LOCALAPPDATA%/Yiuk/DeviceAssist/screenshots`，
      唯一文件名，被挤出或会话结束时删除。
- [x] `screen.capture` 原生操作接受 `max_width` / `format` / `save`，返回
      `screenshot_id`、几何、`layout_version`、`path`、`size_bytes`；**仍然不回图片本体**。
- [x] `take_screenshot` 接原生分支：MCP 先查能力，原生可用就走原生（抓屏 + 落盘 + 既有
      `download_file` 取图），否则回退脚本；结果多一个 `capture_source` 字段说明走了哪条路径。
      旧客户端的返回字段、`screenshot_id` 语义与 `STALE_SCREENSHOT` 判定不变。
- [x] 局部变化采样在原生路径上已迁到客户端：`input.click` 在设备侧采样点击点周围 192×108 灰度，
      用与脚本相同的判据（≥24 灰阶的像素占比 ≥0.002 或平均差 ≥0.02）算 `diff_ratio` / `changed_ratio`；
      旧客户端仍走 PowerShell 脚本。
- [x] `click` / `move_mouse` 接原生分支（`input.click` / `input.move`）：MCP 先查能力，
      原生可用就下发结构化操作（坐标、`screenshot_id`、按键、双击、`detect_change`、`settle_ms`），
      否则回退脚本；结果多一个 `input_source`（`native` / `script`）。`input.move` 已支持
      `duration_ms`（确定性 ease-in-out），但工具 schema 到 P4 才暴露该参数。
- [x] 设备级资源门：`vision/devicegate.{h,cpp}` 是进程级互斥，抓屏（`VisionService::capture`）与
      输入动作（`input.click` / `input.move`）都先过同一道门；排队期间可取消。
      会话级 `SessionLimiter` 继续负责"同一会话 500ms 最小间隔"，两层语义互不替代。
- [x] 临时截图主动回收：新增原生操作 `screen.release`（按路径删除，只允许截图目录内），
      MCP 在图片读完后（成功或失败）尽力回收；`VisionService::pruneOrphanScreenshots` 每次抓屏
      顺手清掉 5 分钟前不再被任何快照引用的残留文件，兜住崩溃/断电。
- [ ] `drag`（P4）；`input::ButtonGuard` 已能保证任何路径抬起按键，等 `input.drag` 落地后复用。
- [ ] 真机验收原生 `click` / `move_mouse`：单元测试覆盖了参数、坐标换算、回退与错误分类，
      `yiuk-platform-interop` 已通过平台验证 `screen.capture` / `screen.release` 全链路，
      但还没有在真实 GUI 会话里手动点过一次（互操作程序不注入键鼠）。

**门禁**：混用 take_screenshot → find → click/move/drag 时 ID 与坐标始终一致；旧客户端桌面回归通过；
连续抓屏无 GDI 对象/内存增长（`yiuk-vision-test` 覆盖快照/节流/设备门/回收/编码，GDI 长稳仍待 P6 实测）；
原生 click / move 的真机验收仍是 P6 的必过项。

### P3：视觉内核与 find_on_screen（第一批可发布）

- [x] `TemplateMatcher`（基础版）：`qt-client/src/vision/templatematcher.{h,cpp}` —— 灰度
      `TM_CCOEFF_NORMED`、阈值、`region` 限定、IoU 0.3 去重、按分数降序、`limit` 截断。
      OpenCV 4.12 已接入 `yiuk-vision`（复用 `spike/vision` 的 MinGW 静态构建，exe 增量 +5.7MB）；
      找不到 OpenCV 时编译期关闭，运行时返回 `UNSUPPORTED_OPERATION`。
      未做：粗到细的金字塔、多尺度（0.75–1.5）、alpha mask —— 当前假设模板与目标同缩放。
- [x] `vision.find` 的图像分支：`image_path` / `image_b64`（PNG/JPEG，Qt 解码，≤64 KiB、≤2048x2048），
      抓一帧不落盘的画面直接匹配，返回截图坐标。
- [x] `TextLocator`（基础版）：`qt-client/src/vision/textlocator.{h,cpp}` —— UIA 控制视图 `FindAll`，
      限制 `maxNodes`（5000）与 `timeBudgetMs`（300），超限带已有结果返回并标记 truncated；
      归一化全角→半角、去所有空白（OCR 的"收 藏"）、casefold；`exact` / `contains`；
      `control_type` 名称过滤；`region` 相交过滤；过滤 offscreen 与零尺寸矩形；按阅读顺序排序。
      未做：UIA 结果缓存、三种 DPI/两主题下的命中率重测（仍按计划低于 80% 则降级为显式来源）。
- [x] OCR：`Windows.Media.Ocr` 直连（手写 WinRT ABI，MinGW 需 `-fno-devirtualize`）；
      系统语言枚举 + 按查询文本挑引擎（CJK 优先中文）、`MaxImageDimension` 校验；
      行/词窗口匹配（归一化去掉 CJK 字间空格）+ 词框并集 + ≥60% 重叠去重；
      缺语言包返回 `OCR_UNAVAILABLE` + hint，`source=auto` 时只记 `ocr_note` 不影响 UIA/图像结果。
      未做：按 region 裁剪后再识别（当前整帧识别）、竖排/旋转文本。
- [x] `find_on_screen`（文本路径）：`limit` 1–20、`region`、`control_type`、`match`，返回截图坐标 bbox/center
      与 `screenshot_id`，不返回图片；没有截图时先抓一张不落盘的建立基准。
      等待语义：`wait_until_found` + `timeout_seconds`（1–25，默认 10），MCP 侧每 500ms 一个独立原生调用，
      轮间不占设备门；超时返回 `timed_out:true` + 已有候选（不当作错误）。
- [x] `agenttools` schema/注册表/注解（ReadOnly、Idempotent）、原生 backend 接线、平台链路互操作
      （`yiuk-platform-interop` 已跑通 `vision.find`）。本地 bridge 仍返回 `UNSUPPORTED_CLIENT`。
- [ ] 固定素材入库：约 30 类控件在三 DPI/两主题下的截图与至少 15 个负样本，使用自建界面或许可清楚的素材，不含用户真实桌面隐私。

**门禁**：素材命中率 ≥95%，负样本零合格命中；region/alpha/纯色/超大解码/取消/超时均覆盖；性能达 P0 口径、20 次稳态无持续增长。没有真机 OCR 环境时明确未验收，不用 mock 替代结论。

### P4：平滑移动、drag、click_target（第二批可发布）

- [x] 在 `qt-client/input/` 建立 `inputcontroller`：纯函数 `buildPath` 生成确定性 ease-in-out 路径，
      12ms 步长、终点精确取整、不加随机、不做规避检测；真实客户端与互操作程序共用。
- [x] move：`duration_ms` 0–2000，默认 0（瞬移）；老客户端仍可用原参数。工具 schema 已暴露。
- [x] drag：`duration_ms` 50–5000、`hold_ms` 0–1000，两端都先过 `toScreenPoint` 范围校验；
      `ButtonGuard` 保证任何可处理路径抬起按键，结果用 `released:true` 表示已确认释放，
      没确认就返回 `DRAG_NOT_RELEASED` 而不是报成功。
- [x] 取消/超时/关闭会话都会走到 guard；但进程被强杀时 RAII 不会运行，不宣称绝对保证（已在文档写明）。
- [x] `find_on_screen` 的 `timeout_seconds` / `wait_until_found`：MCP 侧每 500ms 一个独立原生调用
      （每轮自己取/放设备门），超时返回 `ok:true` + `timed_out:true` + 已有候选/空结果。
- [x] `click_target` 的等待在客户端一笔事务里：每轮取门→抓帧→定位→选择，轮间放门，
      找到的那一轮在同一把门内点完；超时 `NOT_FOUND` + `timed_out:true` 且不发 down。
- [x] `click_target`：客户端内事务完成 find/pick/click；默认 unique 与 detect_change，歧义/不命中/过期/取消时没有任何 down 事件。
      实现：`VisionService::clickTarget`（每轮持门：抓一帧 → 定位 → `pickTarget` → `clickPointLocked`；
      等目标出现时轮间放门，找到的那一轮在同一把门内点完），
      歧义判据写在纯函数 `pickTarget` 里（第一名 vs **另一个视觉目标**的最强对手，差 < 0.03；
      中心重合或一個框有 ≥60% 面积被另一个盖住的 UIA 父子节点 / NMS 残留不算竞争）。
      失败信封带候选现场（需要 `envelopeError(..., data)` + Go 侧 `decodeNativeResult` 透传）。
- [x] 结果保留 session/截图坐标/物理坐标/foreground_title；只在确认释放时返回 `released:true`。
- [x] 工具描述写明验证码用 `ask_user` 交给用户；输入类工具另外注明提权窗口的限制。

**门禁**：路径端点/单调性/可复现、每个 pick 分支与 0.03 边界、limit 截断、所有 down 之后的故障注入；
真机普通滑块、排序列表、悬停菜单由窗口确认事件与最终状态，不只检查 SendInput 成功。

**真机验收（已做）**：`yiuk-input-fixture` 的窗口被 `input.drag` 按"右 → 右下 → 左下 → 左 → 回起点"
分 5 段拖满整个屏幕，5 段全部到达目标、最终回到起点，`INTEROP EXIT=0`。

**已知限制（实测）**：任务管理器（`TaskManagerWindow`）拖不动。实测该进程 `elevated=1`，
Windows 的 UIPI 会静默丢弃非提权进程投递的合成输入——这与 `click` 早就写在工具描述里的
"提权窗口点击被静默丢弃"是同一条限制，不是拖动实现的问题。
如果要用拖动方式操作提权窗口，MCP/客户端必须以同等完整性级别运行。

### P5：模板管理、混合 DPI 与 ROI 缓存（第三批）

- [x] `save_template`：从**最近一张快照保留的像素**裁剪；像素已被回收（旧 id / 已换帧）就返回
      `STALE_SCREENSHOT` + 提示重新截图，绝不重抓一张冒充旧图。内存预算固定为一张全屏帧
      （2560×1600 ARGB32 ≈ 16 MB，新帧入表时旧帧像素释放）。
- [x] 模板只写 `<程序目录>data\templates`：12 位 hex 随机 id（既是格式校验也是路径穿越防线）、
      `QSaveFile` 原子写入、索引 `index.json` 同事务；上限冻结为 **100 个 / 总 32 MiB / 单个 4 MiB / 2048×2048**，
      超了明确 `TEMPLATE_QUOTA_EXCEEDED`（不静默驱逐）；删除策略 = 手工删该目录（文档已写），
      索引写失败会把刚写的 PNG 删掉，不留孤儿。
- [x] `find_on_screen` / `click_target` 支持 `template_id`（与 text/image_path/image_b64 四选一）；
      索引里存宽高与**来源缩放**（截图宽/物理宽），活动日志只记 operation 名，不含图片与正文。
- [ ] 多显示器与混合 DPI：100/125/150%、左/上负原点、插拔、分辨率变化；增加独立 monitor_id 参数与互斥规则，不悄悄改变既有 region 数组语义。
- [x] ROI 缓存（`qt-client/src/vision/roicache.{h,cpp}`）：键 = 模板像素摘要 + 阈值 + region + **布局版本**，
      TTL 10 s、上限 32 条（满了先清过期再丢最早）。只对**图像模板**且在 `limit == 1` 时启用——
      要多个候选却只查旧位置会悄悄少报；命中必须在**当前帧**的 ROI 上重新匹配，不是直接信旧坐标；
      ROI 失配就回全屏并刷新缓存。**click_target 一律不用**：`unique`/`best` 必须验证全域竞争候选。
      结果带 `roi_cached` / `searched_area` / `search_area`，Agent 能看出这次是不是走的短路。
- [x] hint 根据现场生成（`yiuk::vision::failureHint(code, data)`，纯函数）：
      纯色搜索区（灰度标准差 < 2）说"没有可用纹理"，普通未命中报"最高得分 X < 阈值 Y"，
      两个定位器都空时报 OCR 的原因，歧义报候选数与 `pick` 用法，缺语言包/模板配额/模板不存在各有具体动作。
      从未自动降阈值后点击：阈值是调用方的参数，hint 只描述现场。

**门禁**：26 工具完整接线、模板越权/配额/损坏/旧快照覆盖、缓存失效与重复目标不误点、混合 DPI 真机通过。

### P6：完整回归与发布

- [x] 新增 Qt 侧测试：`yiuk-vision-test`（快照/节流/设备门/UIA/OpenCV/参数解析/OCR）、
      `yiuk-click-target-test`（真窗口 + 真 UIA + 真 SendInput + OCR）、`yiuk-localbridge-test`。
      **未做**独立的 `internal/platformclient/vision_qt_test.go`：全链路改由 `yiuk-platform-interop`
      覆盖（真实 Qt ↔ 真实 Go 平台，见 §4.5），Go 侧单测用假设备。
- [x] 真机测试必须显式运行（`yiuk-click-target-test` 单独一个可执行文件，`ctest` 里配了 `SKIP_RETURN_CODE 2`）：
      拿不到前台窗口时打印 `SKIP …` 并返回 2，**不**去激活别的应用；`go test ./...` 全程不注入键鼠。
- [x] `go test ./...` / `go vet ./...` / 相关包 `-race`；Windows `ctest`（5 个用例，含真机项按 SKIP）；
      Go↔Qt 互操作；CI 新增 `windows-qt` job：Qt 6.11.1（MinGW 13.1 工具链，`install-qt-action` 缓存）
      + OpenCV 4.12.0 静态构建按版本号缓存 + `ctest`。**注意**：这条 job 还没有在 GitHub 上跑绿过，
      首次 push 后要看结果再修（本机等价命令：`cmake -DYIUK_BUILD_TESTS=ON` + `ctest`）。
- [ ] Edge → 抖音收藏夹回归须经设备所有者授权，遇人机验证用 ask_user 请本人在设备上完成。对照 P0 记录误点、调用次数、耗时、三端内存，不依赖外站任务替代 fixture 自动验收。
- [ ] 更新 `README.md`、`docs/README.md`、工具说明、`docs/development.md`、`mcpSetupPromptText()`（`src/platformlink.cpp`）；更新实际存在的 `catalog_test.go` / `registry_test.go` / `schema_test.go`、`server_test.go` 及 backend 注册检查，不寻找不存在的 tools_test.go。
- [x] 新建 [`third-party.md`](third-party.md)：发布包里带什么、许可证、版本钉在哪儿；
      同步 CMake / 安装脚本版本到 **0.1.9**。
- [x] 绿色包 0.1.9 已重打：`ReleaseYiukDeviceAssist-0.1.9-win64`（82 MB）+ zip（35.7 MB）+ sha256；
      在**不含 Qt/MinGW 的 PATH** 下启动成功（标题 `Yiuk Device Assist 0.1.9`、`data` 就地创建）。
      体积变化：0.1.8 → 0.1.9 目录 +6 MB（OpenCV 静态库 + OCR 代码）。
      **未做**：安装器（ISCC）重打与真机安装/卸载回归。

## 4. 依赖顺序与提交边界

```text
P0 技术验证 + 契约冻结
 ├─ P1 协议/能力/任务/并发取消 ── P2 统一快照 ── P3 find/文字/图像
 └─ 纯算法与素材、InputSink 测试可并行 ────────────┘
                                  P4 鼠标与原子点击
                                  P5 模板/DPI/缓存
                                  P6 全链路/打包/发布
```

建议按可回归提交拆分：①benchmark/契约；②协议与设备能力；③并发响应分发；④Qt native 生命周期+bridge；⑤统一截图/坐标；⑥图像匹配；⑦UIA/OCR；⑧find 接线；⑨move/drag；⑩click_target；⑪模板/DPI/cache；⑫CI/回归/发布。

P1–P2 作为第一批视觉的前置，不独立宣称视觉完成；P3/P4/P5 可分别发布。每次提交先补失败测试，再实现，再回归；不捆绑用户系统、数据库、自动更新或通用 UIA invoke/set_value 等无关工作。

## 5. 完成定义与尚需实测的决策

只有以下全部完成，才标记目标计划完成：

1. P0–P6 清单与测试报告齐全，所有阻塞项关闭；完整工具数 26。
2. 找不到/歧义绝不点击，取消/异常收尾不留可处理的持键状态；不把未知结果说成未生效。
3. 命中率、负样本、全屏/ROI 耗时、30 秒调用预算、三端峰值与稳态内存均有可复现证据。
4. 新旧平台/客户端兼容矩阵验证，权限不变式与现有 25 个工具回归通过。
5. Windows 可复现构建、第三方许可、绿色包/安装器、版本与升级顺序完整。

仍需 P0 实测，而不是继续阅读代码就能回答：OpenCV 静态包体与速度、MinGW 的 Windows OCR 接入方式、UIA 硬超时隔离方案、目标机器语言包覆盖、完整回归调用数与内存基线。
