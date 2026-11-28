# UIA `find_element` spike 脚本

结论与实现建议记在 [`docs/mcp-tools-improvement-plan.md`](../../docs/mcp-tools-improvement-plan.md) 的 P1 行。

这里的脚本只做一件事：**测出「按名字定位 + 元素级操作」在真实 Windows 应用上的命中率、成本与限制**。它们走的就是 MCP 工具将来会走的通道（`execute_command` 跑 PowerShell + `UIAutomationClient`），不是另一套 API。

## 文件

| 文件 | 作用 |
| --- | --- |
| `UiaProbe.ps1` | 测一个窗口：树规模/深度/遍历成本，并按三种策略解析查询集，记录候选、耗时、`FromPoint` 命中情况、元素支持的 pattern |
| `Run-Spike.ps1` | 驱动：确保目标窗口存在（必要时自己启动）→ 跑 probe → 汇总 `out/summary.tsv` |
| `scenarios.json` | 三个场景与各自带标注的查询集 |
| `Pattern-Smoke.ps1` | 用 `InvokePattern` / `ValuePattern` 真正操作一个控件，并验证效果 |
| `Open-Fixture.ps1` | 打开/关闭 fixture 页面窗口 |
| `fixture.html` | 本地测试页：已知控件名字 + 120 行表格 + 14 层嵌套，模拟真实内容页的树规模 |
| `make-fixture.js` | 重新生成 `fixture.html` |
| `add-bom.js` | 给本目录的 `.ps1` 补 UTF-8 BOM（见下） |

## 跑之前

- Windows 10/11 + Windows PowerShell 5.1（`powershell.exe`），不需要装任何东西；
- 需要一个**交互式桌面会话**：UIA 看不到锁屏、UAC 安全桌面和别的用户会话里的窗口；
- 脚本是只读的，除了 `Pattern-Smoke.ps1`（在 fixture 页上点一个没有副作用的按钮）；
- **不会杀任何用户进程**：只在「窗口本来不存在」时自己启动，且只关闭自己启动的那个窗口。

## 跑

```powershell
# 三个场景（记事本 / 资源管理器 / Edge fixture）
powershell -NoProfile -ExecutionPolicy Bypass -File Run-Spike.ps1 -ActivateMode foreground

# 只跑一个
powershell -NoProfile -ExecutionPolicy Bypass -File Run-Spike.ps1 -Only edge-fixture

# 换个抢前台的策略，比较坐标命中率
powershell -NoProfile -ExecutionPolicy Bypass -File Run-Spike.ps1 -ActivateMode topmost -OutDir out-topmost
```

`-ActivateMode`：

| 值 | 做什么 |
| --- | --- |
| `none`（probe 默认） | 不动窗口，测「窗口可能被挡」的基线 |
| `foreground`（driver 默认） | `AttachThreadInput` + `SetForegroundWindow` |
| `topmost` | `SetWindowPos(HWND_TOPMOST)`，不要求抢到前台 |

## 输出

`out/` 下每个场景一个 JSON（树统计、`named` 节点全量转储、每个查询的多次尝试与候选），加一行 `summary.tsv`。`out/` 不进仓库——它们含这台机器上的窗口标题。

## 两个必须记住的坑

1. **无 BOM 的非 ASCII `.ps1` 会被 PowerShell 5.1 按 ANSI 解码**，中文注释或字符串会直接把脚本语法搞坏（`Unexpected token`、`Add-Type` 静默失效、甚至 `[type]` 解析失败）。改完脚本跑一次 `node add-bom.js`，或者保持纯 ASCII。
2. **`enumerate` 模式必须带 `ControlType` 过滤**：`FindAll(TrueCondition)` 在 568 节点的页面上要 663 ms，带过滤只要 25 ms。

## 现状

三个场景的验收结果：exact 定位 92%、模糊 + 打分 92%，`Invoke` / `Value` 端到端可用；不能全树遍历（1.5~4.2 ms/节点）。结论与工具契约见 [docs/mcp-tools-improvement-plan.md](../../docs/mcp-tools-improvement-plan.md)。
