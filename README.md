# Yiuk Switchboard

面向 AI Agent 的远程设备协作与执行平台。Agent 在设备所有者授权后连接并操作远端 Windows 设备。

状态：MVP 开发中。

## 核心能力

- **设备与在线状态**：设备码标识设备，在线状态由平台维护，设备码不是认证凭证。
- **临时密码**：设备端生成 32 位临时密码，支持有效期、版本轮换与撤销。
- **两种授权模式**：逐次批准（`approval`）与直接远程（`direct`）。
- **会话授权**：会话绑定设备、操作者、权限集合与到期时间，可即时结束并终止任务。
- **命令任务**：单次 PowerShell 执行，返回 stdout、stderr、退出码、状态与截断标记；支持工作目录、超时、取消、输出上限。
- **远端文件读取**：独立 `read_file` 权限，64 KiB 分块，单文件上限 32 MiB，SHA-256 校验后落盘。
- **Agent 工具**：`list_devices`、`get_device_status`、`request_connection`、`run_command`、`get_session_status`、`execute_command`、`get_command_result`、`download_file`、`cancel_command`、`close_session`，附 MCP stdio 适配器。
- **TLS 中转**：远程协助与 Minecraft 联机使用独立业务类型、端口、密码与资源限制。
- **错误与幂等**：稳定错误码、不可复用请求 ID、断线不自动重放。

## 安全边界

- 命令只能绑定设备所有者批准的会话；拒绝、过期、关闭的会话不执行。
- 命令以当前登录用户权限运行，不提升权限；普通权限不是沙箱。
- 临时密码、命令正文与完整输出不写入服务端日志；设备端凭证使用 Windows DPAPI。
- 服务端完全内存化，重启后在线连接、临时密码、请求与会话全部失效。

## 架构

```text
操作者 Windows                          设备所有者 Windows
+--------------------------+            +--------------------------+
| Agent (MCP)              |            | Yiuk Qt Client           |
|   MCP Adapter            |            |   所有者审批 / 会话管理  |
|   Tool Core              |            |   PowerShell 执行        |
+------------+-------------+            +------------+-------------+
             |                                       |
             +------------------+--------------------+
                                |
                         +------v-------+
                         | Go 平台服务  |
                         | 认证 / 设备  |
                         | 会话 / 任务  |
                         | TLS 中转路由 |
                         +--------------+
```

## 项目现状

| 能力 | 状态 |
| --- | --- |
| Go 平台服务（认证、设备、会话、任务、TLS 中转、路由） | 已完成 |
| Agent 工具核心与 MCP 适配器 | 已完成 |
| Agent 平台 backend | 已完成 |
| Windows Qt 客户端平台对接 | 已完成 |
| Qt↔Go 互操作测试 | 已通过 |
| 真实 Windows 安装包与 TLS 端到端验收 | 进行中 |
| Windows 进程树清理、安装升级、发布前安全检查 | 进行中 |
| Minecraft 联机回归与冻结清单验证 | 待执行 |

## 游戏联机

保留既有的 Minecraft 局域网联机中转，与远程协助并列且互不互通。

## 文档

- [产品需求](docs/product-requirements.md)：范围、功能与验收标准。
- [技术选型](docs/technical-selection.md)：技术基线与待决项。
- [实现方案](docs/implementation.md)：模块边界、协议与状态机。
- [开发与构建](docs/development.md)：环境、构建、测试与部署。
- [后端实施状态](docs/backend-status.md)：已完成范围、测试与剩余工作。
- [服务器改造计划](docs/server-refactor-plan.md)：内存化服务器与密码授权模型。
