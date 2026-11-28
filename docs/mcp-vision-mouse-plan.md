# MCP 视觉定位与鼠标增强计划

承接 [`mcp-tools-improvement-plan.md`](mcp-tools-improvement-plan.md)。上一份解决“键盘 / 等待 / 点击可追溯”，本文解决“**按文字或图像找到目标、点准、拖得动**”。本文只定方案与验收，不含代码改动。

代码勘察与分阶段执行清单见 [`mcp-vision-mouse-implementation-plan.md`](mcp-vision-mouse-implementation-plan.md)。已查明的通道、抓屏与工具链事实回填在 §8、§14；尚需实测的性能、OCR 与 UIA 超时隔离方案不得视为已完成。

已定决策：

- **方案 A**：视觉能力做在 Qt 客户端内部（新增本地视觉模块），不走 PowerShell 组合路径；
- **引入 OpenCV**（只链接 `core` + `imgproc`），先做半天对比测试确认体积与耗时达标。

## 1. 目标与边界

目标：让 agent 说“点那个叫『收藏』的标签”或“点这个图标”，系统自己在当前画面里找到位置并点准，位置变了也能找到；同时补齐 `drag` 和平滑 `move_mouse`，让音量条、进度条、拖拽排序、悬停菜单这类**正常界面操作**稳定可用。

本期做：

- 屏幕内的图像模板匹配、文字匹配；
- `find_on_screen`、`click_target`、`drag`，以及 `move_mouse` 的平滑移动；
- 歧义与失败的明确反馈（宁可报错，也不乱点）；
- 客户端能力协商，旧客户端不受影响。

本期**明确不做**：

- **不做验证码 / 人机验证的绕过**。不实现拟人化随机轨迹、随机抖动、速度曲线伪装、检测规避。轨迹是**确定性**的（同样的参数得到同样的路径）。
- 遇到验证码、滑块验证、风控弹窗时，agent 应识别出来并用 `ask_user` 交给用户处理，用户完成后再继续。工具描述里要写明这一点。
- 不做录屏 / 视频流、跨设备编排、通用 OCR 引擎（不引入 Tesseract）。

## 2. 现状与差距

| 能力 | 现状 | 差距 |
| --- | --- | --- |
| 定位目标 | 只有像素坐标，agent 靠看截图估 | 没有“按文字 / 图像找位置” |
| 悬停 | 第一批计划里的 `move_mouse` 是瞬移 | 依赖中间 `mousemove` 的菜单、dock 触发不稳 |
| 拖动 | 无 | 滑块、进度条、拖拽排序都做不了 |
| 点击验证 | 第二批计划的 `screenshot_id` + `detect_change` | 本文工具要复用同一套机制 |
| 图像处理 | PowerShell + GDI | 做不了稳定的多尺度匹配，必须进客户端 |

## 3. 设计原则

1. **找不到就报错，不乱点**。`click_target` 的返回要么是明确命中，要么是 `NOT_FOUND` / `AMBIGUOUS_TARGET`，并带最高得分与候选，不做“差不多就点”。
2. **不返回图片**。查找结果只回坐标、得分、来源，避免上下文膨胀与隐私外泄。
3. **文字优先走结构化来源**：先 UIA 控件名（精确、零成本），再系统 OCR 兜底，最后才是 agent 自己看图。
4. **坐标语义不变**：仍以最近一次截图为准，所有结果带 `screenshot_id`，沿用上一份计划 §6.1–§6.2 的绑定与 `STALE_SCREENSHOT`。
5. **单次调用 ≤ 30 秒**，等待类参数上限 25 秒，沿用上一份计划的原则 1。
6. **设备资源优先**：视觉查找与截屏共用同一把会话级互斥锁和 500ms 最小间隔（上一份计划 §6.5），内存峰值不高于基线。
7. **旧客户端不坏**：新工具先检查客户端能力，不支持就返回 `UNSUPPORTED_CLIENT` 和升级提示。

## 4. 第 0 步：OpenCV 对比测试（半天）

在写正式代码前，用同一批素材比较两条路线。

| 项 | 路线 1：OpenCV（`core` + `imgproc`） | 路线 2：`QImage` 手写归一化互相关 |
| --- | --- | --- |
| 方案 | `matchTemplate(TM_CCOEFF_NORMED)`，灰度 + 多尺度 | 自写 NCC，必要时加图像金字塔 |
| 需要测的 | 静态链接后体积增量、构建时间、与 Qt6 / 当前 MinGW 工具链的兼容（若采用 MSVC，须单独验证并记录迁移代价） | 实现与维护量、速度 |

测试条件：

- 屏幕 2048×1280，模板 64×64（另测 24×24 的小图标），缩放因子 0.75 / 1.0 / 1.25 / 1.5；
- 指标：单次耗时、内存峰值、命中率（用 §10 的素材集）；
- **达标线**：全屏一次查找 ≤ 300ms（带 ROI 时 ≤ 100ms）、峰值额外内存 ≤ 50MB、安装包体积增量写进发布说明。

**P0 实测结论（2026，见 [`spike/vision/README.md`](../spike/vision/README.md)）**：OpenCV 4.12 只构建
`core`+`imgproc` 静态库时，基准程序 exe 从 200 KB 增至 5.9 MB（+5.7 MB）；全屏 8 尺度中位 266 ms、
p95 653 ms；手写 NCC 同条件下中位 1168 ms、p95 5631 ms，慢 4–12 倍。**采用 OpenCV**；
手写路线不达标，只保留为依赖不可用时的降级/对照。代价：全屏 300 ms 与跨缩放 ≥95% 尚未达标，
转 P3 验收。

决策规则：路线 2 全部达标才考虑手写；否则用 OpenCV。倾向 OpenCV，除非体积增量不可接受。同时确认：

- 通过 vcpkg（或现有依赖管理方式）引入，CI 能重复构建；
- 许可证（Apache-2.0）写进第三方许可清单；
- 不链接 `highgui`、`videoio` 等无关模块。

## 5. 第一批：视觉内核（客户端内）

### 5.1 模块划分

在 `qt-client/` 新增 `vision/` 目录：

- `ScreenGrabber`：抓屏转 `cv::Mat`（灰度优先，需要彩色时再转）。复用现有截屏路径，**不另起一份截屏逻辑**，保证与 `screenGeometry`、`screenshot_id` 一致。
- `TemplateMatcher`：模板匹配、多尺度、结果去重。
- `TextLocator`：UIA 控件名查找 + 系统 OCR 兜底。
- `VisionService`：对外只暴露命令入口，内部负责互斥、节流、超时、取消。

### 5.2 匹配算法要点

- 灰度 + `TM_CCOEFF_NORMED`；对模板和屏幕都先做粗到细：先在 0.5 倍缩小图上粗找，再在候选区域内按原尺度精修，满足 §4 的耗时线。
- 尺度范围默认 0.75–1.5（覆盖 100%–150% DPI 差异与模板来自不同缩放的截图），步长 1.1 倍左右；由 `scale_min` / `scale_max` 可调。
- 阈值默认 0.88；返回按得分排序的候选，非极大值抑制（IoU 0.3）去重。
- 支持 `region`（只在某个矩形内找），显著降低耗时与误匹配。
- 处理 alpha：带透明通道的模板用 mask 参与匹配。
- 明确局限并写进工具描述：模板需与目标**外观基本一致**（主题 / 高亮 / 悬停态会降低得分）；纯色块、重复图标容易歧义。

### 5.3 文字定位

1. **UIA 优先**：按 `Name`（精确 / 包含）、可选 `control_type` 查找，取 `BoundingRectangle`，经 `screenGeometry` 换算成截图坐标，`source: "uia"`。
2. **OCR 兜底**：`Windows.Media.Ocr`，语言取系统已装语言包，可由 `lang` 指定；返回行级 / 词级框，`source: "ocr"`。找不到语言包时返回 `OCR_UNAVAILABLE` 和 `hint`。
3. 匹配模式：`contains`（默认）、`exact`；大小写不敏感；全角半角做归一化。
4. 本步与上一份计划 §8 的 UIA 技术验证合并执行，用记事本、Edge、资源管理器三个目标测命中率；命中率 < 80% 时 UIA 路径只作为可选来源，不作为默认。

### 5.4 `find_on_screen`

```json
{"session_id":"…","text":"收藏","match":"contains","region":[0,0,1200,200],"limit":5}
```

```json
{"session_id":"…","image_path":"C:\\Users\\…\\tpl\\slider.png","threshold":0.9,"limit":3}
```

- `text` 与 `image_path` / `image_b64`（≤ 64 KB 的 PNG）**二选一**；同时给或都不给返回 `INVALID_ARGUMENT`。图像也可通过现有 `upload_file` 先传到设备，再用 `image_path` 引用。
- `limit`：1–20，默认 5；`timeout_seconds`：1–25，默认 10（超时返回已有候选加 `timed_out: true`）。
- 可选 `wait_until_found: true` 加 `timeout_seconds`：在超时内每 500ms 重新查找，直到出现（等价于“等按钮出现”），仍遵守截屏节流。
- 返回：

```json
{
  "ok": true,
  "screenshot_id": "a1b2c3d4e5f6",
  "matches": [
    {"bbox":[412,84,468,108], "center":[440,96], "score":0.94, "source":"uia"}
  ],
  "elapsed_ms": 120
}
```

- 注解：`ReadOnly: true, Idempotent: true`。

### 5.5 第一批的验收

- 单元测试：多尺度命中、NMS 去重、`region` 越界、alpha mask、文字归一化（全 / 半角、大小写）、参数二选一校验、超时返回。
- 负样本测试：相似但不对的图标不得高于阈值命中（见 §10）。
- 性能：达到 §4 的耗时线；连续 20 次查找内存不增长。

## 6. 第二批：鼠标动作

### 6.1 `move_mouse` 增加 `duration_ms`

- `duration_ms`：0–2000，默认 0（保持第一批的瞬移行为，向后兼容）。
- 非 0 时按**确定性的缓动曲线**（ease-in-out）分步 `SendInput` 移动，步长约 8–16ms；同参数同路径，**不加随机**。
- 用途仅限：触发依赖中间 `mousemove` 的悬停菜单、dock、tooltip 展开。

### 6.2 `drag`

```json
{"session_id":"…","from":[412,300],"to":[820,300],"duration_ms":400,"hold_ms":50,"screenshot_id":"a1b2c3d4e5f6"}
```

- 流程：移动到起点 → 左键按下 → 等 `hold_ms` → 按确定性缓动曲线移动到终点 → 抬起。
- `duration_ms`：50–5000，默认 400；`hold_ms`：0–1000，默认 50；起终点距离上限为屏幕尺寸；`screenshot_id` 校验同 `click`。
- **安全兜底**：无论发生什么（异常、取消、超时），脚本 / 客户端必须在 `finally` 中确保**抬起按键**，避免鼠标键卡住。
- 返回 `{session_id, from, to, screen_from, screen_to, released: true}`。
- 注解：`Destructive: true`（可拖动、移动、删除对象）。
- 典型用法（写进工具描述）：音量条、进度条、窗口调整、拖拽排序。

### 6.3 `click_target`

```json
{"session_id":"…","text":"收藏","pick":"best","detect_change":true,"settle_ms":250}
```

- 等于 `find_on_screen` + `click`，参数继承两者；点击坐标取命中框中心。
- `pick`：`unique`（默认，命中多个返回 `AMBIGUOUS_TARGET`）、`best`、`first`、`index:N`。
- **歧义规则**：默认 `unique`，且当第一名与第二名得分差 < 0.03 且位置不同，即视为歧义。`AMBIGUOUS_TARGET` 返回候选列表，由 agent 改用 `pick` 或缩小 `region`。
- 没找到：`NOT_FOUND`，带最高得分与阈值，供 agent 调整；不点击。
- `detect_change` 复用上一份计划 §6.2 的机制，返回 `{changed, diff_ratio}`。
- 注解：`Destructive: true`。

### 6.4 第二批的验收

- 单元测试：缓动曲线端点与单调性、`drag` 的 `finally` 抬起（模拟中途取消）、`pick` 各分支、歧义判定边界。
- 真机：见 §10.3。

## 7. 第三批：易用性与健壮性

1. **模板管理**：`save_template`（从最近一次截图的 `region` 裁剪并保存到设备模板目录，返回 `template_id`），`find_on_screen` 可用 `template_id` 引用，省掉每次上传；模板目录有数量与体积上限，仅限当前用户目录。
2. **多显示器与 DPI**：`find_on_screen` / `drag` 在多显示器、混合 DPI 下的坐标换算专项测试；`region` 支持指定显示器。
3. **ROI 缓存**：同一会话内连续查找同一目标时，优先在上次命中位置附近的 ROI 内找，未命中再回退全屏。
4. **失败现场**：`NOT_FOUND` 附带 `hint`（如“尝试降低 threshold 到 0.8”或“目标可能被遮挡，先 `take_screenshot`”）。

## 8. 通道与协议（方案 A 的代价）

上一份计划的原则 4（“不改 Qt 客户端、全部走组合路径”）**对本文工具不再成立**，需要修订：

- 客户端新增内置命令：`vision.find`、`input.drag`、`input.move`（含 `duration_ms`）、`vision.save_template`。
- **代码勘察结论**：现有 `execute_command` 确实只下发 PowerShell 文本，但平台使用可扩展的 JSON 消息，core 按 `Type` 分发，gateway 转发原始帧。推荐新增结构化原生操作帧，复用既有任务授权、幂等、限额、取消与结果通道，不经脚本绕行本地 HTTP。具体契约在代码级实施计划 P0 冻结；客户端内部操作还需包括公共抓屏、坐标点击与原子 `click_target`，不能只实现上述四项。
- **能力协商**：客户端在设备状态里上报 `capabilities`（如 `["vision.find","input.drag"]`）；MCP 在调用前检查，缺失返回 `UNSUPPORTED_CLIENT` 加 `hint`（“请升级 Qt 客户端到 ≥ x.y.z”）。
- 版本：MCP 与 Qt 客户端需要**同步发版**；MCP 新工具在旧客户端上只返回明确错误，旧工具不受影响。

## 9. 兼容性与发布

- 新增工具：`find_on_screen`、`click_target`、`drag`（`move_mouse` 只增参数），前两批从当前 22 个增至 25 个；第三批加入 `save_template` 后为 26 个。同步更新实际存在的 `catalog_test.go` / `registry_test.go` / `schema_test.go`、backend 注册检查、`server_test.go`、`README.md`、`mcpSetupPromptText()`、`docs/development.md`。
- 所有新参数向后兼容；不改既有错误码含义。
- 工具描述每个控制在 2 句内，并统一加一句：“遇到验证码或人机验证请使用 `ask_user` 交给用户处理。”
- 第三方许可清单加入 OpenCV；发布说明写明安装包体积变化。
- 版本按批次发，MCP 与 Qt 客户端各自升小版本，并在说明里写清最低配套版本。

## 10. 测试计划

1. **素材集（固定、入库）**：约 30 个常见控件 / 图标，在 100% / 125% / 150% DPI、浅色 / 深色主题下各截一份；另含 15 个**负样本**（与目标相似但不同的图标、同一图标的禁用态）。
2. **单元**：匹配、NMS、文字归一化、参数校验、缓动曲线、`finally` 抬起、`pick` 与歧义规则。
3. **真机（本地平台 + 真实 Qt 客户端）**：
   - 沿用上一份计划的桌面互操作测试程序思路，新增一个带按钮、图标、**普通滑块**（音量条 / 进度条）、可拖拽列表和悬停菜单的测试窗口，由窗口自己校验收到的事件；
   - 依次测 `find_on_screen` → `click_target` → `drag` → `move_mouse(duration_ms)`；
   - 先断言测试窗口为前台窗口，否则跳过而不抢焦点；不默认纳入 `go test ./...`，用环境变量显式开启。
4. **回归**：重跑 Edge → 抖音收藏夹任务，与上一份计划 §4 的基线对比误点次数、调用次数、内存峰值。

## 11. 验收标准

- 命中率：三种 DPI、两种主题下，素材集命中率 ≥ 95%；
- 误点：负样本零命中；找不到或歧义时一律不点击；
- 耗时：全屏单次 ≤ 300ms，带 `region` ≤ 100ms；单次工具调用 ≤ 30 秒；
- 内存：连续 20 次查找无增长，峰值不高于基线；
- `drag` 任何异常路径都已抬起按键（测试覆盖取消与超时）；
- 旧客户端调用新工具返回 `UNSUPPORTED_CLIENT`，旧工具行为不变；
- Edge → 抖音收藏夹回归任务误点归零，调用次数达到上一份计划的目标。

## 12. 风险

| 风险 | 处理 |
| --- | --- |
| 被用于绕过验证码 / 人机验证或批量刷量 | 本期明确不做拟人化随机轨迹与规避检测；轨迹确定性；工具描述要求验证码交给 `ask_user`；`drag`、`click_target` 标注 `Destructive: true`。 |
| 点错目标（相似图标、主题变化） | 默认 `unique`，歧义不点；负样本校准阈值；`click_target` 默认带 `detect_change`。 |
| OpenCV 体积与构建复杂度 | §4 先测；只链接 `core` + `imgproc`；静态链接；体积写进发布说明。 |
| 低内存宿主（2 GB 无 swap）被截屏 / 视觉拖垮 | 与截屏共用互斥与最小间隔；灰度处理；`Mat` 用完即释放；基线与回归记录内存峰值。 |
| 鼠标键卡住 | `drag` 在 `finally` 保证抬起；取消与超时测试覆盖。 |
| 屏幕内容被工具读取的隐私风险 | 不返回图片；模板目录仅限当前用户目录并设上限；日志不记录图像内容。 |
| 锁屏 / UAC 安全桌面输入被丢弃 | 沿用已有显式错误，并在工具描述里说明不可用场景。 |
| 客户端与 MCP 版本不匹配 | 能力协商加 `UNSUPPORTED_CLIENT`，发布说明写最低配套版本。 |
| OCR 语言包缺失 | 返回 `OCR_UNAVAILABLE` 加 `hint`，UIA 与图像路径不受影响。 |

## 13. 排期（相对工作量，非日历）

| 阶段 | 内容 | 相对工作量 |
| --- | --- | --- |
| 第 0 步 | OpenCV 对比测试、依赖接入验证、通道方式确认 | 小 |
| 第一批 | 视觉内核、`find_on_screen`、UIA / OCR 文字定位、素材集与测试 | 大 |
| 第二批 | `move_mouse` 平滑移动、`drag`、`click_target` | 中 |
| 第三批 | 模板管理、多显示器 / DPI、ROI 缓存、失败提示 | 中 |

## 14. 开工前确认：代码结论与实测项

1. **已查明通道**：`internal/platform/protocol/protocol.go` + `core/handler.go` 使用按类型分发的 JSON 协议，可新增原生操作帧；现有命令执行本身只有 PowerShell。推荐方案见 §8，协议边界与兼容矩阵仍须 P0 冻结。
2. **已查明抓屏**：`internal/agenttools/scripts.go` 的 PowerShell 用 `System.Drawing.Graphics.CopyFromScreen`（GDI）；Qt 暂无公共抓屏模块。`screenshot_id`、几何与 500ms 节流在 Go `desktop_state.go`，新客户端必须先统一迁移公共抓屏与快照管理，不能另建互不相通的坐标基准。
3. **已查明构建**：CMake 直接 `find_package(Qt6)`，未接入 OpenCV/依赖管理器；本机是 Qt 6.11.1 + MinGW 13.1，当前 CI 只有 Linux Go。OpenCV 同 ABI 构建、固定版本依赖与 Windows CI 路线须先验证，不能直接使用 MSVC 预编译库。
4. **已实测（P0，见 [`spike/vision/README.md`](../spike/vision/README.md)）**：MinGW 13.1 可直连
   `Windows.Media.Ocr`（语言 `en-US` + `zh-Hans-CN`，900×340 识别 29–31ms）与 UIA COM，不需要 MSVC helper；
   OpenCV 4.12 `core`+`imgproc` 静态构建可链接，exe 增量约 +5.7MB。
   附带两个硬约束：MinGW 在 `-O2/-O3` 会把纯虚接口的 WinRT/UIA 虚调用反优化成 `__cxa_pure_virtual`
   （必须加 `-fno-devirtualize` 或改显式 vtable 调用）；OCR 中文结果字间带空格，文本匹配要先归一化。
   仍待办：UIA 阻塞调用的硬超时隔离方案在 P1 定案。
5. **待冻结语义**：`pick` 与 0.03 歧义规则、坐标与快照生命周期、模板像素保留、全链路 30 秒预算、内存基线及回归调用数。具体建议与门禁见代码级实施计划 §2–§5。
