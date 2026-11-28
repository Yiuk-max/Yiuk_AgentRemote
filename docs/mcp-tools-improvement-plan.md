# MCP 工具

22 个工具，全部走「`execute_command` + `get_command_result`」组合路径（除 `wait` 纯本地）。加工具不需要改平台协议与 Qt 客户端。

| 分类 | 工具 |
| --- | --- |
| 设备/会话 | `list_devices`、`get_device_status`、`request_connection`、`get_session_status`、`close_session` |
| 命令 | `run_command`、`execute_command`、`get_command_result`、`cancel_command` |
| 文件 | `download_file`、`upload_file`、`list_dir`、`stat_path` |
| 桌面 | `take_screenshot`、`click`、`move_mouse`、`press_keys`、`type_text` |
| 等待 | `wait`、`wait_for` |
| 交互 | `ask_user`、`get_user_reply` |

## 使用约束

- **输入只发给前台窗口**。`click` / `move_mouse` 按坐标投递不受影响；`press_keys` / `type_text` 落到当时的前台窗口。四个工具都回报 `foreground_title`，输入前先用 `wait_for(window_title=…)` 或 `click` 激活目标。
- **每个工具单次 ≤30 秒**。等待参数（`wait_seconds` / `timeout_seconds` / `wait_for_approval_seconds`）上限统一 25 秒；更久的等待由 Agent 多次调用。
- **坐标基于最近一次截图**。`take_screenshot` 返回 12 位 `screenshot_id`；`click` / `move_mouse` 带上它可避免用过期基准，不匹配返回 `STALE_SCREENSHOT`。
- **设备可以用名字指定**。`device_id` 或 `device_name` 二选一；name 先精确（大小写不敏感）再包含匹配，命中多个返回 `DEVICE_AMBIGUOUS` 和候选。平台是内存态，重启后 id 会变，所以名字更稳。
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
