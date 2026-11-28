# MCP 工具

25 个工具。多数组合工具仍可走「`execute_command` + `get_command_result`」脚本路径（`wait` 纯本地）；
抓屏、鼠标与文本定位在设备上报能力时改走原生通道（`execute_native`），旧客户端自动回退脚本。

| 分类 | 工具 |
| --- | --- |
| 设备/会话 | `list_devices`、`get_device_status`、`request_connection`、`get_session_status`、`close_session` |
| 命令 | `run_command`、`execute_command`、`get_command_result`、`cancel_command` |
| 文件 | `download_file`、`upload_file`、`list_dir`、`stat_path` |
| 桌面 | `take_screenshot`、`click`、`move_mouse`、`drag`、`press_keys`、`type_text` |
| 视觉 | `find_on_screen`（UIA 文本定位 + OpenCV 图像模板匹配）、`click_target`（定位+点击一笔事务） |
| 等待 | `wait`、`wait_for` |
| 交互 | `ask_user`、`get_user_reply` |

## 使用约束

- **`drag` 只走原生通道**。`from` / `to` 都是截图坐标，客户端在一笔事务里
  按下 → 沿确定性路径移动 → 抬起；只有确认释放才返回 `released:true`，否则
  `DRAG_NOT_RELEASED`。旧的脚本路径做不了"按住连续移动"，所以旧客户端返回
  `UNSUPPORTED_CLIENT`，不回退。
- **提权窗口收不到合成输入**。任务管理器这类自动提权的窗口（实测 `elevated=1`）
  被 Windows 的 UIPI 挡住：`click` / `drag` 都会"成功返回但什么也没发生"。
  要么让客户端以同等完整性级别运行，要么改用键盘/命令行。
- **输入只发给前台窗口**。`click` / `move_mouse` 按坐标投递不受影响；`press_keys` / `type_text` 落到当时的前台窗口。四个工具都回报 `foreground_title`，输入前先用 `wait_for(window_title=…)` 或 `click` 激活目标。
- **每个工具单次 ≤30 秒**。等待参数（`wait_seconds` / `timeout_seconds` / `wait_for_approval_seconds`）上限统一 25 秒；更久的等待由 Agent 多次调用。
- **坐标基于最近一次截图**。`take_screenshot` 返回 12 位 `screenshot_id`；`click` / `move_mouse` 带上它可避免用过期基准，不匹配返回 `STALE_SCREENSHOT`。
- **设备可以用名字指定**。`device_id` 或 `device_name` 二选一；name 先精确（大小写不敏感）再包含匹配，命中多个返回 `DEVICE_AMBIGUOUS` 和候选。平台是内存态，重启后 id 会变，所以名字更稳。
- **抓屏与鼠标操作优先原生通道**。`take_screenshot` / `click` / `move_mouse` 在设备上报对应能力
  （`screen.capture` / `input.click` / `input.move`）时由客户端执行，不再启动 PowerShell；
  不可用时回退脚本。结果里的 `capture_source` / `input_source` 说明这次走了哪条路径。
- **`take_screenshot` 优先原生通道**。设备上报 `screen.capture` 能力时由客户端抓屏并落盘，
  MCP 用既有的 `download_file` 取图；不可用（旧客户端 / 本地 bridge）才回退 PowerShell 脚本。
  结果里的 `capture_source`（`native` / `script`）说明这次走了哪条路径，返回字段与
  `screenshot_id` 语义不变。图片取走后客户端会用 `screen.release` 删掉临时文件。
- **`find_on_screen` 只走原生通道**。UIA 查询是设备本地能力，脚本做不到，所以旧客户端返回
  `UNSUPPORTED_CLIENT`（带升级提示），不会回退脚本假装"找不到"。返回的 `matches[].center` 就是
  截图坐标，直接把 `screenshot_id` 一起交给 `click` / `drag`。`region` 用 `[left,top,right,bottom]`
  截图坐标缩小范围；`match=exact` 走精确匹配；文字匹配已归一化全半角、大小写与空白
  （OCR 的"收 藏"也能命中）。
- **图像模板匹配用 OpenCV**。`text` / `image_path` / `image_b64` 三选一：`image_path` 是**设备端**
  路径（先用 `upload_file` 传模板），`image_b64` 是 PNG/JPEG（解码后 ≤64 KiB、尺寸 ≤2048x2048）。
  实现是灰度 `TM_CCOEFF_NORMED` + IoU 0.3 去重，`threshold` 默认 0.88；`matches[].source` 为
  `opencv`。客户端未编译 OpenCV 时返回 `UNSUPPORTED_OPERATION`，不伪装成"找不到"。
- **`click_target` 只在客户端内完成，且默认不乱点**。`text` / `image_path` / `image_b64`
  三选一（与 `find_on_screen` 同语义），定位用的那一帧就是点击的坐标基准，全程持设备门，
  中间不会插进别的会话抓屏或抢鼠标。`pick`：`unique`（默认）/ `best` / `first` / `index:N`。
  歧义判据是"第一名与**另一个视觉目标**的最强对手得分差 < 0.03"（同框的 UIA 父子节点、
  NMS 残留不算竞争）：命中多个则返回 `AMBIGUOUS_TARGET`，没命中返回 `NOT_FOUND`，
  **两种情况都不发 down 事件**，错误 `data` 里带 `matches` / `top_score` / `threshold`。
  它没有脚本回退：旧客户端返回 `UNSUPPORTED_CLIENT`，需要升级客户端。
- **错误自带现场**。失败回复带 `hint`（下一步建议）和 `data.devices`（最多 20 台在线设备）。
- **schema 是唯一校验来源**。`validateInput` 直接把 `InputSchema` 交给 JSON Schema 校验器，不再手写一遍约束。
- **分发走注册表**。agenttools 分本地工具（`localTools`）、组合工具（`composedTools`）、后端工具（`BackendTools()`）三层；`TestEveryToolHasExactlyOneHandler` 与 `TestEveryBackendToolHasAHandler` 拦“加了工具忘实现”。

## 待办

| 优先级 | 内容 | 状态 |
| --- | --- | --- |
| P1 | UIA `find_element` / `invoke_element` / `set_element_value` | spike 已做，结论：做。实测 exact 定位 92%、模糊 + 打分 92%，`Invoke` / `Value` 端到端可用；不能全树遍历（1.5~4.2 ms/节点）。复现脚本：[`spike/uia/`](../spike/uia/README.md) |
| P1 | 按键落地验收：`press_keys` / `type_text` 本机测试时前台被子系统通知窗口抢走，需在安静的桌面重跑 | 未验证 |
| P2 | `take_screenshot` 的 `window` / `region` | 待做 |
| P3 | `session_renew` | 待做（审批通过会回传临时密码，可免审批重连） |

`list_dir` / `stat_path` 的符号链接只标注不跟随；`device_name` 多命中目前只有单元测试，没有两台真实设备可测。
