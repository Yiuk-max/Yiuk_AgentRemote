# Agent 询问设备用户

在已有批准/活动会话中，Agent 可调用 `ask_user`，让客户端显示与现有窗口风格一致的询问框。用户可单选或输入自己的回答。不需要额外服务或数据库。

## 界面与交互

```text
远程协助询问                                        ×
接下来你希望我先做什么？

[ 检查并修复软件问题        ]   ← 点击即回答
[ 安装或配置软件            ]
[ 整理文件，先给出方案      ]

[ 也可以直接输入你的回答…            ]
                        [暂不回答] [发送]
```

- **点击某一项选项就是回答**，不需要再点确认；选项本身就是 Agent 认为可接受的回答。
- 想自由输入时直接在输入框写，点“发送”或按 Enter。输入为空或超过 1000 字时“发送”不可用，Enter 也不会提交。
- “暂不回答”、标题栏关闭、Esc 三者等价，都返回 `cancelled`，不会自动选择任何选项。
- 空输入时按 Enter 不会误选第一项：选项按钮和标题栏按钮都不是对话框的默认按钮。
- 窗口不阻塞主窗口，所有者可以同时去主窗口“立即停止会话”。
- 客户端常在托盘后台运行，因此询问窗口置顶显示并请求前台，同时在主窗口隐藏时发出一条托盘提醒，避免被其他应用遮住而无人发现。
- 长问题或六个长选项超出约 620px 时，只有问题与选项区域滚动，输入框和按钮始终可见。
- 显示位置跟随所有者窗口（可见时）或鼠标所在屏幕居中，并保证不越出可用屏幕。

## 使用

```json
{
  "session_id": "已批准的会话 ID",
  "question": "你希望把软件安装在哪里？",
  "options": ["默认位置", "D 盘", "只下载，暂不安装"],
  "timeout_seconds": 120
}
```

`ask_user` 立即返回 `request_id`、`status: pending`、`expires_at`，仅表示平台接收并转发，不表示用户已看到。

- 问题：1–1000 个 Unicode 字符。
- 选项：可省略，最多 6 项，每项 1–100 字，去除首尾空白后不得为空或重复。不提供选项时为纯输入询问。
- 等待：默认 120 秒，可设置 15–300 秒；受会话到期时间限制。
- 每设备最多一个待回复问题；每会话保留记录最多 32 条。超出分别返回 `QUESTION_BUSY` / `QUESTION_LIMIT_REACHED`。

稍后使用 `get_user_reply` 查询，不忙轮询：

```json
{"session_id":"…","request_id":"…"}
```

工具统一响应的 `data`：

```json
{
  "session_id": "…",
  "request_id": "…",
  "status": "answered",
  "answer_source": "option",
  "selected_index": 1,
  "answer": "D 盘",
  "expires_at": 1770000000
}
```

下标从 0 开始。自由输入时 `answer_source: text`，不返回 `selected_index`，`answer` 最多 1000 字。

## 状态及边界

- `pending`：等待回复。
- `answered`：用户明确提交；只有这个状态有有效答案。
- `cancelled`：用户点击暂不回答、关闭、Esc，或会话正常结束。
- `timed_out`：在截止时间前没有收到回复。也可能是旧客户端不支持询问，不能理解成用户拒绝。
- `result_unknown`：设备/操作端断线，或客户端无法显示/接受请求，不能确认回复。

回复不授予额外权限，不自动执行选项内容；Agent 必须自行判断下一步。禁止通过此工具索取密码、验证码、私钥等敏感凭据。问题、答案不写进平台日志或客户端持久化文件；活动面板只记录生命周期摘要。

相同请求 ID 的协议重传不会重复弹窗；工具层每次 `ask_user` 都是新问题，所以不要用重复调用重试一个仍在等待的问题。超时、取消、断线不能当作默认同意，更不能自动重新弹窗。平台重启不恢复询问，终态记录按现有配置保留（默认 15 分钟），之后查询返回 `QUESTION_NOT_FOUND`。

原操作者在会话关闭后仍可查询自己的终态结果，但不能用已关闭会话发起新询问。平台和 Qt 客户端需共同升级。无需新增权限配置：首版复用 `execute_command` 的会话授权。

## 验证

- 单元与集成：`go test ./...` 覆盖选项/自由输入、越权、幂等、忙碌、迟到回复、原子终态、超时与断线。
- 真机：本地真实平台 + 真实 Qt `MainWindow`，端到端测试自动操作实际控件（选项、输入+Enter、纯输入、长文本、Esc、关闭、暂不回答、超时、关闭会话、设备断线）。
- 交互修复（都有回归覆盖）：空输入按 Enter 不会误选第一项；标题栏按钮不再是对话框的隐式默认按钮；长问题只滚动正文；窗口置顶并在托盘隐藏时发提醒。

```sh
go test ./...
$env:QT_PLUGIN_PATH="<Qt>\6.11.1\mingw_64\plugins"
$env:YIUK_QUESTION_QT_EXE="<repo>\qt-client\build-qt\yiuk-question-interop.exe"
$env:YIUK_QUESTION_MCP_EXE="<repo>\qt-client\build-qt\yiuk-mcp.exe"
go test ./internal/platformclient -run 'Test(MCPQuestionQt|LocalBridgeQuestionQt)EndToEnd' -v -count=1
```
