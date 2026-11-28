# Agent 询问设备用户

已批准会话中可调用 `ask_user`，在设备端弹出询问框。用户可单选或自由输入。

```json
{
  "session_id": "已批准的会话 ID",
  "question": "你希望把软件安装在哪里？",
  "options": ["默认位置", "D 盘", "只下载，暂不安装"],
  "timeout_seconds": 120
}
```

`ask_user` 立即返回 `request_id`、`status: pending`、`expires_at`，**只表示平台已转发，不表示用户看到**。之后用 `get_user_reply` 查询（支持 `wait_seconds` 长轮询，不要忙轮询）。

返回字段：`session_id`、`request_id`、`status`、`answer_source`（`option` / `text`）、`selected_index`（从 0 开始，仅选项）、`answer`（最多 1000 字）、`expires_at`。

## 边界

- 问题 1–1000 字符；选项可省略，最多 6 项，每项 1–100 字，去空白后不得为空或重复；等待默认 120 秒，可设 15–300 秒，且受会话到期时间限制。
- 每设备最多一个待回复问题（超出 `QUESTION_BUSY`）；每会话保留记录最多 32 条（超出 `QUESTION_LIMIT_REACHED`）。
- 状态：`pending` 等待中；`answered` 用户明确提交（**只有它带有效答案**）；`cancelled` 用户点了暂不回答 / 关闭 / Esc，或会话正常结束；`timed_out` 截止前没有回复（也可能是旧客户端不支持询问，不能理解为拒绝）；`result_unknown` 设备或操作端断线、客户端无法显示请求，不能确认回复。
- 超时、取消、断线**都不能当作同意**，也不能自动重新弹窗。回复不授予额外权限、不自动执行选项内容。
- 禁止通过此工具索取密码、验证码、私钥等敏感凭据。问题与答案不写平台日志，也不落客户端持久化文件；活动面板只记生命周期摘要。
- 相同请求 ID 的协议重传不会重复弹窗；工具层每次 `ask_user` 都是新问题，所以不要用重复调用重试一个仍在等待的问题。平台重启不恢复询问，终态记录默认保留 15 分钟，之后查询返回 `QUESTION_NOT_FOUND`。

## 验证

`go test ./...` 覆盖选项/自由输入、越权、幂等、忙碌、迟到回复、原子终态、超时与断线。真机端到端需要一个已批准的会话与真实 Qt 客户端（`yiuk-question-interop.exe`）。
