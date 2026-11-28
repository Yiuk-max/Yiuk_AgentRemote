# MCP 工具现状与后续计划

依据 [`mcp-tool-suggestions.md`](mcp-tool-suggestions.md) 的实测记录整理。本文只保留**当前状态、验证结论和待办**，不再重复实现过程。

## 1. 现状

22 个工具，全部走「`execute_command` + `get_command_result`」组合路径，平台协议与 Qt 客户端不需要跟着改。

| 分类 | 工具 |
| --- | --- |
| 设备/会话 | `list_devices`、`get_device_status`、`request_connection`、`get_session_status`、`close_session` |
| 命令 | `run_command`、`execute_command`、`get_command_result`、`cancel_command` |
| 文件 | `download_file`、`upload_file`、`list_dir`、`stat_path` |
| 桌面 | `take_screenshot`、`click`、`move_mouse`、`press_keys`、`type_text` |
| 等待 | `wait`、`wait_for` |
| 交互 | `ask_user`、`get_user_reply` |

关键约束，也是踩过的坑：

- **输入只发给前台窗口**。`click` / `move_mouse` 按坐标投递，不受影响；`press_keys` / `type_text` 落到当时的前台窗口。四个工具都会回报 `foreground_title`，Agent 输入前应先用 `wait_for(window_title=…)` 或先 `click` 激活目标。
- **每个工具单次 ≤30 秒**。等待参数（`wait_seconds` / `timeout_seconds` / `wait_for_approval_seconds`）上限统一 25 秒；更久的等待由 Agent 多次调用。
- **坐标基于最近一次截图**。`take_screenshot` 返回 12 位 `screenshot_id`；`click` / `move_mouse` 带上它可避免用过期基准，不匹配返回 `STALE_SCREENSHOT`。
- **设备可以用名字指定**。`device_id` 或 `device_name` 二选一；name 先精确（大小写不敏感）再包含匹配，命中多个返回 `DEVICE_AMBIGUOUS` 和候选。平台是内存态，重启后 id 会变，所以名字更稳。
- **错误自带现场**。失败回复带 `hint`（下一步建议）和 `data.devices`（最多 20 台在线设备）。

## 2. 已验证（真机：本地平台 + 真实 Qt 客户端）

| 能力 | 结论 |
| --- | --- |
| 截图与坐标换算 | 截图坐标 → 屏幕坐标误差 ≤2px |
| `move_mouse` | 系统光标移动到 `(1024,658)`，与预期一致 |
| `click` | 点中测试窗口按钮，窗口回报 `clicked` |
| `detect_change` | 点按钮 `changed=true`，点空白 `changed=false`（见下） |
| 截图节流 | 连续两次截图 id 不同、间隔 ≥500ms |
| `STALE_SCREENSHOT` | 用旧 id 点击被拒，报错点名新旧 id |
| `wait_for` | `process=explorer` 命中；不存在的进程超时返回 `matched:false` |
| `list_dir` / `stat_path` | 真实目录列举、`limit` 截断、对文件提示改用 `stat_path`、缺失路径带 hint |

`detect_change` 的判据来自真机校准：第一版把采样缩到 96×54，点中按钮只有 `diff_ratio 0.0084`，低于阈值被判成“没变化”。现在改为**原分辨率 192×108 采样**，两条判据任命中即可——`changed_ratio >= 0.002` 或 `diff_ratio >= 0.02`，两个值都返回给调用方。校准数据：

| 场景 | changed | diff_ratio | changed_ratio |
| --- | --- | --- | --- |
| 点中按钮（标签文字改变） | true | 0.0035–0.0073 | 0.0084–0.0285 |
| 点窗口空白 | false | 0.0026–0.0037 | 0 |

## 3. 未验证

- **`press_keys` / `type_text` 的按键落地**：本机测试时前台反复被通知窗口（`Windows.UI.Core.CoreWindow`，标题“新通知”）抢走，键盘输入因此没有落到测试窗口。需要在一台桌面安静的机器上补跑：

```powershell
$env:QT_PLUGIN_PATH="<Qt>\6.11.1\mingw_64\plugins"
$env:YIUK_DESKTOP_QT_EXE="<repo>\qt-client\build-qt\yiuk-question-interop.exe"
go test ./internal/platformclient -run TestDesktopPrimitivesAgainstRealClient -v -count=1
```

成功时应看到测试窗口依次收到 `b`、清空、`xyz`，且不出现 “synthetic input appears blocked”。

- **符号链接**：只在单元测试覆盖。本机创建符号链接需要额外权限，没造出真实 reparse point。
- **`device_name` 多命中**：只有单元测试，没有两台真实设备可测。
- **`wait_for` 的 `window_title` / `path_exists`**：`process` 已真机验证，另两种只有单元/脚本级覆盖。

## 4. 后续（未做）

| 优先级 | 内容 | 备注 |
| --- | --- | --- |
| P1 | UIA `find_element` / `click_element` | 先做一天 spike：记事本、Edge、资源管理器各测命中率与树深度，<80% 就不做 |
| P2 | `take_screenshot` 的 `window` / `region` | 图更小、读得更准；要多显示器 + DPI 实测，并走现成的截屏节流 |
| P3 | `session_renew` | 先只写文档：审批通过会回传临时密码，可免审批重连 |

先补完第 3 节的按键验收，再谈这些。
