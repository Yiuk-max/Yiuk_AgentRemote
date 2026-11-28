# MCP 工具实测与改进建议

> 起因：2026-11-27 用 `yiuk-mcp` 完整跑了一遍「打开浏览器 → 抖音 → 收藏夹 → 询问用户 → 打开指定收藏夹」的真实任务，以及「唤出 dock → 打开 Kazumi」的桌面操作。
> 本文记录**实测踩到的坑**和**按收益排序的改进建议**，供后续迭代 `cmd/yiuk-mcp` 参考。

---

> 实施计划见 [`mcp-tools-improvement-plan.md`](mcp-tools-improvement-plan.md)（分批、参数、验收标准）。

## 1. 现状

当前 MCP 暴露 16 个工具：

| 分类 | 工具 |
| --- | --- |
| 设备/会话 | `list_devices`、`get_device_status`、`request_connection`、`get_session_status`、`close_session` |
| 命令 | `run_command`、`execute_command`、`get_command_result`、`cancel_command` |
| 文件 | `download_file`、`upload_file` |
| 桌面操作 | `take_screenshot`、`click`、`type_text` |
| 交互 | `ask_user`、`get_user_reply` |

实测结论：**「命令类」很强，「桌面操作类」还停留在"像素级遥控"**，缺少让 agent 高效、稳定操作 GUI 的原语。

---

## 2. 实测踩到的 5 个瓶颈

### 2.1 `request_connection` 是阻塞式，和 agent 侧超时不匹配

```text
实测：wait_for_approval_seconds=120 时，MCP 调用在 60 秒被上层掐断；
      但会话其实已经建成（平台日志有 session_created），
      用户侧还多看到一个弹窗。
```

**代价**：一次白等 + 用户困惑"怎么又弹一次"。

**建议**：改为**非阻塞**，立即返回 `{session_id, status: "pending_approval"}`，由 agent 用 `get_session_status` 轮询。阻塞等待可以作为可选参数保留。

### 2.2 只认 `device_id`，平台重启后必然踩坑

平台是纯内存的，重启后设备重新注册、`device_id` 会变。实测中拿着旧 id 直接得到：

```json
{"ok": false, "code": "DEVICE_NOT_FOUND", "message": "device not found"}
```

**代价**：两次无意义往返。

**建议**：
- 接受设备名（`device_id` 或 `device_name` 二选一，模糊匹配 `-Yiuk-`）；
- 报错自带现场信息：`DEVICE_NOT_FOUND` 时附上当前在线设备列表，或至少提示"请先 list_devices"。

### 2.3 只有像素坐标点击，没有元素级定位

实测"抖音收藏 tab"连续点错 3 次（点到「我的预约」→「观看历史」→ 最后改用 `?showTab=favorite_collection` 直达）。原因：

- 横向标签栏间距只有 20~30 px；
- 截图被缩放到 `max_width`（2048×1280 → 1200 宽），换算系数要靠 agent 自己推；
- 点击后没有"是否命中"的反馈。

**建议**：
1. **坐标归一化**：`click({x: 0.36, y: 0.42})`，消除分辨率/缩放换算；
2. **`click` 绑定 `screenshot_id`**：声明坐标基于哪张截图，服务端校验是否最新；
3. 返回**点击后画面是否变化**（一次内部截图 diff），省掉 agent 再截一次；
4. 长期：`find_element(name)` 走 Windows UI Automation，按控件名/角色直接点。

### 2.4 没有 hover（只移动不点击）

想读 dock 图标名字只能"悬停→截图→读 tooltip"。实测中被迫绕道：

```powershell
Add-Type ... SetCursorPos(1024, 1279)   # 手动移动鼠标
take_screenshot                          # 再截图看 tooltip
```

**建议**：加 `move_mouse({x, y})`。hover 是读取 UI 元信息的唯一手段（tooltip、展开菜单、触发自动隐藏的 dock）。

### 2.5 没有 `wait`，所有"等它加载"都变成轮询

实测每条命令后面都跟着 2~10 次 `get_command_result` 空转；agent 沙箱里甚至连 `setTimeout` 都没有。

**建议**：`wait({ms})`、`wait_for({window_title, path_exists, process})`，或给 `get_command_result` 加长轮询参数 `wait_seconds`（0~25）。

---

## 3. 建议清单（按优先级）

| 优先级 | 建议 | 解决什么 | 改动量 |
| --- | --- | --- | --- |
| **P0** | `press_keys`（组合键：`ctrl+l`、`ctrl+shift+o`、`win+r`、`alt+f4`） | GUI 自动化的主干道；今天"打开收藏夹"原本一个组合键搞定 | 小（SendInput 已有实现，扩展修饰键即可） |
| **P0** | `wait` / `wait_for` | 消除空转轮询，稳定"等加载完" | 小 |
| **P0** | `move_mouse` | hover 读 tooltip / 触发 dock | 极小 |
| **P1** | `click` 归一化坐标 + `screenshot_id` + 变化反馈 | 点得准、点得可验证 | 中 |
| **P1** | `request_connection` 非阻塞 | 消除超时错位 | 小 |
| **P1** | `find_element(name)` / `click_element(name)`（UIA） | 彻底摆脱像素坐标 | 中大 |
| **P2** | `list_dir` / `stat_path` | 看远端文件不用开 PowerShell 执行权限 | 小 |
| **P2** | 设备名解析 + 报错自带在线设备列表 | 减少往返 | 小 |
| **P2** | `run_command` 的 `temporary_password` 改为可选（与 `request_connection` 一致） | 语义一致 | 极小 |
| **P3** | `take_screenshot` 支持 `window` / `region` | 图小 → agent 读得准、省上下文 | 中 |
| **P3** | `session_renew`，或把"审批通过后返回临时密码可免审批重连"写进描述 | 600 秒会话到期后的连续性 | 小/文档 |
| **P3** | `ask_user` 的回答推送或在 `get_user_reply` 加长轮询 | 减少轮询 | 小 |

---

## 4. Agent 侧使用纪律（无需改代码）

1. **先 `list_devices` 并缓存 `device_id`**；平台重启会让旧 id 失效。
2. **能用命令就不点**：
   - 启动程序：`Get-StartApps` 拿 `AppID` → `Start-Process "shell:AppsFolder\<AppID>"`（实测 Kazumi 一次成功）；
   - 开网页：`Start-Process msedge "URL"`；
   - 查文件：PowerShell `Get-ChildItem` / `Test-Path`。
3. **一次截图，规划所有点击**；只在"状态真的变了"时才重新截图。
4. **长文本用 `type_text(method="paste")`**，逐字输入慢且易丢字符。
5. **会话用完即 `close_session`**；需要时用返回的 `temporary_password` 免审批重连。
6. **重操作之间看一眼宿主内存**：截屏循环是内存大户（实测连续 8 次截屏 + 十几条 PowerShell 后，2 GB 无 swap 的机器直接硬 hang）。

---

## 5. 附：实测记录

| 时间 | 动作 | 结果 |
| --- | --- | --- |
| 15:32 | `request_connection`（旧 device_id） | `DEVICE_NOT_FOUND` |
| 15:33 | `list_devices` → `556604822` | `mode: direct`，在线 |
| 15:33 | `request_connection` | approved，返回 `temporary_password` |
| 15:34 | `take_screenshot` | 2048×1280 屏，Edge 为默认浏览器 |
| 15:35 | `Start-Process msedge` + `click` 地址栏 + `type_text` | 抖音首页加载 |
| 15:36~15:39 | `click` 收藏、我的预约、观看历史 | 连点错 3 次 |
| 15:40 | URL 直达 `?showTab=favorite_collection` | 成功，读到 7 个收藏夹 |
| 15:41 | `ask_user`（6 选项）→ `get_user_reply` | 回答 `travel` |
| 15:42 | `click` travel 夹 | 打开 22 个作品 |
| 15:45~15:50 | dock 实验：`SetCursorPos` 贴底 + 悬停读 tooltip | dock 弹出，但"启动台"图标未定位成功 |
| 15:51 | `Get-StartApps` → `shell:AppsFolder\com.flutter.kazumi_...` | Kazumi 启动成功 |
| 15:51 | （服务器侧）整机在连续截屏后立即硬 hang，16:01 重启 | 已加 2 GB swap 兜底 |

**结论**：把 P0 的三个工具（`press_keys`、`wait`、`move_mouse`）补齐后，同一条任务的工具调用次数预计能从 ~25 次降到 8 次以内。
