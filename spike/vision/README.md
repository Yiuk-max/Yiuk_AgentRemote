# P0：视觉定位与鼠标增强的可行性测试

对应目标：[`docs/mcp-vision-mouse-plan.md`](../../docs/mcp-vision-mouse-plan.md) §4 与代码级实施计划 §3 的 P0。
本目录只放**验证代码与测量结果**，不是产品实现；所有结论都能用下面的命令复现。

```text
spike/vision/
  CMakeLists.txt            4 个目标：素材生成、基准、UIA/GDI 探针、WinRT OCR 探针
  src/fixtures.cpp          可复现的测试画面（3 种 DPI × 2 种主题 + 负样本）
  src/ncc_matcher.cpp       手写灰度 NCC（积分图分母 + SSE2 点积 + 粗到精）
  src/opencv_matcher.cpp    OpenCV TM_CCOEFF_NORMED 路线（同参数、同素材）
  src/bench_vision.cpp      两条路线的命中率 / 耗时 / 内存 / 负样本对比
  src/selftest_ncc.cpp      NCC 参考实现 vs SSE2 实现的一致性自检
  src/probe_uia.cpp         UIA COM 直连（不经 PowerShell）的耗时
  src/probe_screen.cpp      GDI 抓屏 + 灰度 + 缩放耗时（不落盘）
  src/probe_winrt_ocr.cpp   Windows.Media.Ocr：语言包、识别耗时、词框
  out/                      生成物（素材、JSON 结果、日志），不进仓库
  third_party/              OpenCV 源码与本地静态构建，不进仓库
```

## 1. 测量环境

| 项 | 值 |
| --- | --- |
| CPU / 内存 | 见 `win32-01`：Windows 10/11 x64，桌面 2560×1600 @125%（DPI 120，单显示器） |
| Qt | 6.11.1，`D:\QT\6.11.1\mingw_64`（**MinGW**，不是 MSVC） |
| 编译器 | MinGW-w64 GCC 13.1.0 `posix-seh` |
| CMake / Ninja | 4.3.1 / 1.12.1 |
| OpenCV | 4.12.0（Apache-2.0，构建产物在 `third_party/install-opencv/etc/licenses`），源码浅克隆后本地静态构建（`core` + `imgproc` + 内部 zlib） |
| Windows SDK | 10.0.26100（只用它查 IID/vtable 顺序，不参与编译） |

复现：

```powershell
# 无 OpenCV 版本（手写 NCC 基线）
cmake -S spike/vision -B spike/vision/build -G Ninja -DCMAKE_BUILD_TYPE=Release `
  -DCMAKE_PREFIX_PATH=D:/QT/6.11.1/mingw_64 `
  -DCMAKE_CXX_COMPILER=D:/QT/Tools/mingw1310_64/bin/g++.exe `
  -DCMAKE_MAKE_PROGRAM=D:/QT/Tools/Ninja/ninja.exe
ninja -C spike/vision/build

# 带 OpenCV 的对比版本（OpenCV_DIR 指向本地静态安装）
cmake -S spike/vision -B spike/vision/build-cv -G Ninja -DCMAKE_BUILD_TYPE=Release `
  -DCMAKE_PREFIX_PATH=D:/QT/6.11.1/mingw_64 `
  -DCMAKE_CXX_COMPILER=D:/QT/Tools/mingw1310_64/bin/g++.exe `
  -DCMAKE_MAKE_PROGRAM=D:/QT/Tools/Ninja/ninja.exe `
  -DOpenCV_DIR=E:/Yiuk_AgentRemote/spike/vision/third_party/install-opencv/x64/mingw/staticlib
ninja -C spike/vision/build-cv vision_bench

$env:PATH = "D:\QT\6.11.1\mingw_64\bin;" + $env:PATH
cd spike/vision
.\build\make_fixtures.exe out/fixtures
.\build\vision_selftest.exe
.\build\probe_screen.exe 20
.\build\probe_uia.exe
.\build\probe_winrt_ocr.exe
.\build-cv\vision_bench.exe --manifest out/fixtures/manifest.json --out out/p0-bench.json
```

OpenCV 的静态构建（约 1.5 分钟，8 核）：

```powershell
git clone --depth 1 --branch 4.12.0 https://github.com/opencv/opencv.git spike/vision/third_party/opencv-src
cmake -S spike/vision/third_party/opencv-src -B spike/vision/third_party/build-opencv -G Ninja `
  -DCMAKE_BUILD_TYPE=Release `
  -DCMAKE_C_COMPILER=D:/QT/Tools/mingw1310_64/bin/gcc.exe `
  -DCMAKE_CXX_COMPILER=D:/QT/Tools/mingw1310_64/bin/g++.exe `
  -DCMAKE_MAKE_PROGRAM=D:/QT/Tools/Ninja/ninja.exe `
  -DBUILD_LIST=core,imgproc -DBUILD_SHARED_LIBS=OFF -DBUILD_TESTS=OFF -DBUILD_PERF_TESTS=OFF `
  -DBUILD_EXAMPLES=OFF -DWITH_IPP=OFF -DWITH_OPENCL=OFF -DWITH_JPEG=OFF -DWITH_PNG=OFF `
  -DWITH_TIFF=OFF -DWITH_WEBP=OFF -DWITH_PROTOBUF=OFF -DWITH_ADE=OFF -DCPU_DISPATCH=
cmake --build spike/vision/third_party/build-opencv --parallel
cmake --install spike/vision/third_party/build-opencv
```

## 2. 结论（先看这一节）

1. **MinGW 能用 OpenCV**：4.12.0 只构建 `core` + `imgproc`，静态库 6.7 MB + 8.2 MB；基准程序链接后
   exe 从 **200 KB 涨到 5.9 MB（+5.7 MB）**。构建 31 s 配置 + 67 s 编译安装。
   与 MSVC 无关，不需要两套 ABI。体积可以接受，但要写进发布说明。
2. **手写 NCC 与 OpenCV 都能找到目标，但性能差 4–12 倍**：同尺度/同主题下两者都是 100%；
   全屏 8 尺度中位耗时 1168 ms（NCC）对 266 ms（OpenCV），p95 差一个数量级（§5.2）。
3. **MinGW 能直接调 UIA 与 Windows OCR**，不需要 MSVC helper：
   - UIA（C++ COM，**不是** PowerShell）：`CoCreateInstance` 7.0 ms，`ElementFromHandle` 10.6 ms；
     在真实 Edge 窗口（746 个节点）上一次无过滤 `Descendants` **35.5 ms**（0.048 ms/节点），
     带 `ControlType=Button` 过滤 37.0 ms；`ElementFromPoint` 2.7 ms。
   - OCR 引擎可用语言：`en-US`、`zh-Hans-CN`；`MaxImageDimension=10000`；
     900×340 图 **29–31 ms** 出结果，返回行/词与 `BoundingRect`。
4. **工具链有一个必须记住的坑**：MinGW GCC 13.1 在 `-O2/-O3` 下会把这些"纯虚接口"的虚调用
   **反优化成 `__cxa_pure_virtual`**，运行时 `0xC0000005`（栈看起来是坏的，很难查）。
   `-O0` 或加 `-fno-devirtualize` 后完全正常；也可以显式按 vtable 索引调用。
   验证脚本见 §3.4。
5. **抓屏成本低于预期**：GDI 抓 2560×1600 中位 30.4 ms，转灰度 15.2 ms，缩到 1568 宽 12.4 ms，
   整条"抓屏→灰度→缩放"链条约 **58 ms**，单次 find 的 300 ms 预算里还有余量。
6. **OCR 中文文本带空格**：`收 藏 首 页 设 置`。P3 的文本匹配必须先去掉 CJK 字符之间的空白，
   否则"收藏"永远匹配不上。
7. **掩码匹配实测结论与旧印象相反**：OpenCV 4.12 的 `TM_CCOEFF_NORMED` 接受 mask，
   全 255 mask 与不传 mask 结果一致；部分 mask 被接受但数值正确性待 P3 验证（§5.4）。

### 2.1 门禁对照（`docs/mcp-vision-mouse-plan.md` §11）

| 门禁 | 结果 | 说明 |
| --- | --- | --- |
| 同 DPI/同主题素材命中率 ≥ 95% | ✅ **100%** | 124/124（OpenCV） |
| 跨缩放（100% 模板 → 150% 界面） | ❌ **60.1%** | 文字控件为主，得分 0.72–0.93 |
| 负样本零命中 | ❌ **12/496** | 云下载图标 0.96–0.98、近义文本 0.88–0.90 |
| 全屏单次 ≤ 300 ms | ⚠️ **中位 266 ms / p95 653 ms** | 8 尺度 FFT 成本；ROI 与自适应尺度待 P3 |
| 带 `region` ≤ 100 ms | ⏳ 未测 | 本次基准只跑全屏 |
| 额外峰值内存 ≤ 50 MB | ✅ **+7.0 MB** | 全量 744 次后 |
| 单次调用 ≤ 30 s | ✅ | 最慢 0.85 s（OpenCV）/ 6.5 s（手写 NCC） |
| 旧客户端行为不变 | ⏳ 未涉及 | 本阶段没有改协议与客户端 |

## 3. 关键实现说明

### 3.1 测试素材是生成的，不是截图

`make_fixtures` 用 Qt 画一张 2048×1280 的"应用界面"：侧栏 5 项、顶栏搜索与 3 个按钮、12 个图标、
3 行列表、滑块、底部按钮、卡片齿轮、状态条，共 31 个控件，全部带固定 ID 与像素框。

- 3 种 DPI（100% / 125% / 150%）× 2 种主题（浅 / 深）= 6 张正样本；
- 1.0 与 1.5 DPI × 2 主题 = 4 张**负样本**：同样的版式，但图标改成描边/变体、文字换成近义词；
- 模板从 100%/150% 场景裁剪（含 2–5 px padding），共 124 个。

这样做的原因是**可复现**：素材由代码生成，任何人重跑都得到同一批像素与标注，不把开发机的桌面
内容带进仓库。真实桌面由 `probe_screen` 单独测耗时，不落盘。

### 3.2 手写 NCC 的两个层次

- `Exact`：每个尺度在目标区域全量扫描。分母用积分图 O(1)，分子用 SSE2 `_mm_madd_epi16` 点积。
- `Pyramid`：屏幕只缩一次（默认 1/2），每个尺度把模板缩到同比例粗搜，取最高分的前 16 个峰，
  再在原尺度 ±6 px 窗口内精修。命中后仍以原尺度模板为准。

`vision_selftest` 用**双精度标量参考实现**校验 SSE2 与积分图结果：真值位置得分必须是 1.0，
`Exact` 必须命中同一像素，`Pyramid` 允许 2 px 漂移，纯色屏幕不得命中。

实现过程中被自检抓到的两个真实缺陷（已修，留在提交历史里就是为了说明为什么需要自检）：

1. 金字塔精修窗口把"模板左上角可行范围"当成了"区域"，宽度少算一个模板宽度，
   ROI 直接变空，**所有金字塔查找都返回 0 命中**；
2. 同一处窗口宽度用的是 `2*margin+1` 而非 `模板尺寸 + 2*margin`。

### 3.3 OpenCV 路线对齐了同一组参数

`opencv_matcher.cpp` 复用同一份 `SearchParams`：同样的尺度梯、阈值、`limit`、NMS 的 IoU 0.3，
粗搜也做 1/2 缩放 + 精修。否则两张表不可比。

### 3.4 MinGW 的虚调用坑（重要）

`probe_winrt_ocr` 在 `-O3` 下的 `get_MaxImageDimension` 会被编译成 `call __cxa_pure_virtual`：

```text
g++ ... -O3 -S src/probe_winrt_ocr.cpp | rg pure_virtual   # 命中
调用 → 0xC0000005，gdb 报告的 pc 落在一个模块基址上，栈看起来是坏的
```

同一个程序加 `-fno-devirtualize` 即正常；`-O2` 同样会崩。产品侧有三种应对，
P1 需要在 CMake 里二选一并写测试：

| 方案 | 代价 |
| --- | --- |
| 给用到 WinRT/UIA 的目标加 `-fno-devirtualize` | 一行 CMake，影响该目标的代码质量（可测） |
| 所有 WinRT/UIA 调用都按 vtable 索引显式调用 | 需要一层封装，但完全不依赖编译器行为 |
| 视觉/OCR 放 MSVC helper 进程 | 多一套构建与运行库，发布包复杂度上升 |

本次探针选择 `-fno-devirtualize`（`probe_ocr` 目标已配置）。

## 4. 各探针的原始输出

### 4.1 GDI 抓屏（`probe_screen.exe 20`）

```json
{"virtual_desktop":{"left":0,"top":0,"width":2560,"height":1600},"dpi":120,"monitors":1,
 "grab_ms":{"median_ms":30.4,"p95_ms":34.9,"max_ms":35.2},
 "gray_ms":{"median_ms":15.2},
 "downscale_1568_ms":{"median_ms":12.4},
 "gray_bytes":4096000,"scaled":{"width":1568,"height":980},"working_set_mb":35.5}
```

结论：抓屏+灰度+缩放 ≈ 58 ms，可以被 500 ms 的抓屏节流容纳；灰度只占 4 MB。

### 4.2 UIA（`probe_uia.exe`，前台窗口为 Edge，746 个节点）

```json
{"create_ms":6.98,"from_handle_ms":10.63,
 "app_children_true":{"count":2,"ms":11.75},
 "descendants_buttons":{"count":33,"ms":36.99},
 "descendants_true":{"count":746,"ms":35.51},
 "per_node_ms":0.048,"worst_single_query_ms":36.99,"from_point_ms":2.74}
```

**这与 `spike/uia/` 的 PowerShell 结论差两个数量级**：PowerShell 走 UIA 是 1.5–4.2 ms/节点
（568 节点的页面要秒级），而 C++ COM 直连是 **0.048 ms/节点**。原因主要是 PowerShell 的对象封送，
不是 UIA 本身。结论随之修正：

- C++ 客户端里做**有上限的** UIA 遍历是可行的（Edge 746 节点 35 ms），不必为此引入 helper 进程；
- 仍要设节点上限（建议 ≤ 5000）与单次时间上限（建议 ≤ 300 ms），超限返回
  `UIA_BUDGET_EXCEEDED` 并降级到 OCR，而不是无限遍历；
- 查询要优先限定 `control_type` / 窗口；无过滤的 `FindAll(TrueCondition)` 只允许在节点上限内使用。

### 4.3 Windows OCR（`probe_winrt_ocr.exe`）

```json
{"ok":true,"languages":["en-US","zh-Hans-CN"],"language_count":2,"engine_language":"zh-Hans-CN",
 "max_dimension":10000,"image":{"width":900,"height":340},"recognize_ms":29.3,"lines":3,
 "words_first_line":6,"first_word_bbox":[45.0,37.0,38.0,39.0],
 "first_line":"收 藏 首 页 设 置",
 "text_preview":"收 藏 首 页 设 置 Settings 2026 设 备 已 连 接 确 定"}
```

要点：

- 引擎只能吃 `SoftwareBitmap`。从 `IBuffer` 造它有两个必踩的点：
  `IBufferFactory::Create` 只设 `Capacity`，**必须再写 `Length`**，否则 `CreateCopyFromBuffer`
  返回 `0xC00D7170`；
- 语言包缺失时 `TryCreateFromUserProfileLanguages` 返回空，要回 `OCR_UNAVAILABLE`；
- 中文结果每个字之间带空格，文本归一化要处理；
- 词级 `BoundingRect` 可用，坐标直接是图像像素，能直接换算到截图坐标。

### 4.4 NCC 自检（`vision_selftest.exe`）

```text
reference_score_at_truth=1.000000
exact_best score=1.000000 box=(74,52,48,36) expected=(74,52,48,36)
pyramid_best score=1.000000 box=(74,52,44,33) scale=0.908
coarse_reference_at_truth=1.000000 coarse_best=1.000000 at (37,26)
flat_screen_matches=0 best=0.000000
SELFTEST OK
```

## 5. 基准：手写 NCC vs OpenCV

素材：6 张正样本 × 124 个模板 = 744 次查找；4 张负样本 × 124 个模板 = 496 次查找。
参数：阈值 0.88、尺度 0.75–1.5（步长 1.1）、`limit=3`、NMS IoU 0.3、金字塔 1/2 粗搜 + 精修。

### 5.1 命中率

| 组合 | 样本 | OpenCV 命中率 | 手写 NCC 命中率 |
| --- | --- | --- | --- |
| 同尺度 / 同主题（模板与场景同 DPI、同主题） | 124 | **100%** | 100%（见下注） |
| 跨尺度 / 同主题（100% 模板 → 125%/150% 场景） | 248 | **60.1%** | 未单独测 |
| 跨主题（浅色模板 → 深色场景，反之亦然） | 372 | **0%** | 0% |
| 合计（6 正样本 × 124 模板） | 744 | 36.7% | — |

- 手写 NCC 只在 `pos-100-light` 场景上跑了 124 次（`out/p0-bench-ncc-subset.json`），
  同尺度同主题的 31 个模板全部命中；其余 93 个是跨尺度/跨主题样本，按设计会失败。
- **跨主题 0% 是预期**：灰度 NCC 对反色/换主题不做归一化。验收口径应当是
  "同一主题内、每个 DPI 自己的模板"，而不是"浅色模板必须命中深色界面"。
  客户端要按当前主题选择模板（`save_template` 记录主题/DPI）。
- 跨尺度 60.1% 的失败样本得分集中在 **0.72–0.93**（阈值 0.88），
  失败的是**文字为主的控件**（`nav_*`、`top_*`、`row_*`、`bottom_*`、`status_text`），
  图标类大多命中。原因：文字在不同 DPI 下重新栅格化 + 尺度梯只有 1.1 倍步长
  （1.208 / 1.329 包夹 1.25，1.462 接近 1.5）。结论：文字必须优先走 UIA/OCR，
  像素匹配只作为图标与图像的兜底。

### 5.2 耗时（单次 find，8 个尺度，2048×1280 全屏）

| 路线 | 中位 | p95 | 最大 |
| --- | --- | --- | --- |
| OpenCV（金字塔，正样本 744 次） | **265.8 ms** | 652.5 ms | 853.5 ms |
| OpenCV（金字塔，单场景 124 次） | 236.9 ms | 594.2 ms | 725.5 ms |
| 手写 NCC（金字塔，124 次） | **1168 ms** | 5631 ms | 6492 ms |
| OpenCV（Exact，单次全量 8 尺度） | 877–1051 ms | — | — |
| 手写 NCC（Exact，单次全量 8 尺度） | 10563–12327 ms | — | — |

- 手写 NCC 比 OpenCV 慢 **4–12 倍**，p95 差一个数量级；不值得继续优化。
- OpenCV 的 p95 超过 300 ms 门禁：8 个尺度 × FFT 匹配是主要成本。
  达到 300 ms 的可行手段（P3 做，不在 P0 下结论）：
  1) `region`/ROI（预计 ≤100 ms，本次未测）；
  2) 按模板尺寸自适应尺度数（小图标只需 3–4 个尺度）；
  3) 粗搜放到 1/4（本次 1/2）；
  4) 命中后缓存 ROI，连续查找同一目标时不再全屏。

### 5.3 负样本与误点

`out/p0-neg-cv.json`：**12 / 496 次误报**（4 张负样本 × 124 模板），全部集中在两类：

| 模板 | 负样本内容 | 得分 | 结论 |
| --- | --- | --- | --- |
| `icon_download` | 同一位置画成"云下载"变体 | 0.96–0.98 | 带 padding 的图标模板大部分是背景，平滑 NCC 无法区分字形 |
| `row_projects` | 文本"项目归档"（正样本是"项目文档"） | 0.88–0.90 | 近义文本接近阈值，单靠阈值挡不住 |

对应产品结论（已写进 P3 待办）：

- 图标模板应裁到**紧贴内容**（padding 0–1 px），或用 mask 屏蔽背景；
- 阈值不能只看绝对分数，`pick=unique` + "前两名得分差 < 0.03 即歧义"仍然必要，
  并且 `click_target` 必须默认带 `detect_change`；
- 近义文本的正解是 UIA 精确名匹配 / OCR 文本匹配，不是调像素阈值。

### 5.4 mask 与 NaN

- `TM_CCOEFF_NORMED` + **全 255 mask** 与不传 mask 的结果完全一致（差 < 1e-4）；
  **部分 mask 也被接受**（未验证数值正确性，P3 用素材验证）。
  这修正了"OpenCV 只支持 CCORR/SQDIFF 带 mask"的旧印象（4.12 实测）。
- 本次 744 + 496 次调用中 `patchNaNs` 修补数量为 **0**，纯色区域被提前跳过；
  但 4.12 的 `patchNaNs` 返回 `void`，需要自己统计，仍要保留 NaN/Inf 防护。

### 5.5 内存与稳定性

| 指标 | 结果 |
| --- | --- |
| OpenCV 路线工作集峰值增量 | **+7.0 MB**（744 次）/ +3.6 MB（124 次） |
| 手写 NCC 连续 20 次同目标 | 工作集变化 +0.6 MB（无持续增长） |
| OpenCV 连续调用 | 无崩溃；`patchNaNs` 0 次 |

额外内存远低于 50 MB 门禁；真正的峰值要在 P6 用真实链路（MCP + 平台 + 客户端）复测。

## 6. 对实施计划的影响

1. **路线选择固化**：OpenCV（`core`+`imgproc`，静态）为正式路线，手写 NCC 只保留为
   "依赖不可用时的降级"或对照实现。理由：包体 +5.7 MB 可接受，命中率与耗时见 §5；
   手写实现的维护成本（自检抓到的两个窗口 bug 就是例子）不值得。
2. **首次冻结的常量**：
   - `threshold = 0.88`、`scale 0.75–1.5 step 1.1`、`NMS IoU 0.3`、`limit ≤ 20`；
   - 金字塔粗搜 1/2、精修窗口 = 模板尺寸 + 12 px；
   - 抓屏节流 500 ms、单次 find 预算 300 ms（全屏）/100 ms（带 region）。
3. **能力协商新增一条硬事实**：客户端上报 `capabilities` 时，`vision.find` 只有在
   视觉模块编译进来、且 OCR 语言可用（若声明 `text` 能力）时才发布；
   OCR 语言集合必须运行时探测，不能按"Windows 自带"假定。
4. **工具链风险升级为 P1 阻塞项**：`-fno-devirtualize` 或显式 vtable 调用必须二选一定案，
   并进 Windows CI；否则第一次在 Release 里调 UIA/WinRT 就会崩。
5. **P3 的文本匹配要加一节**：CJK 空格归一化、全半角、大小写；UIA 优先、OCR 兜底；
   UIA 全树遍历必须有上限。

## 7. 未验证 / 仍是风险

- 真实应用（记事本 / Edge / 资源管理器）的 UIA 命中率没有在本次 P0 重测；
  `spike/uia/` 的历史结果是 exact 92%、模糊 92%，P3 需要在新实现上复测并写进验收。
- 混合 DPI、多显示器、负坐标原点：本次只有单显示器 125%，P5 必须补真机矩阵。
- 端到端时延（MCP → 平台 → 设备 → 回传）没有在本次测量；本文件只测设备侧操作耗时。
- 内存峰值：基准记录了工作集峰值，但"整条任务链峰值不超过基线"仍要在 P6 用真实链路复测。
