# AgentRemote Go 后端技术说明与入门指南

> 面向已经会 C++ 后端、epoll、TCP、MySQL、Redis、Asio、WebSocket 和文件分片传输，但刚开始学习 Go 的读者。
>
> 本文以当前仓库为准，重点讲 **Go 平台后端**。MCP 只解释协议定位、启动方式和工具映射，篇幅约占全文的 10%～15%。
>
> 阅读本文后，应该能够回答这些问题：
>
> - 这个项目为什么使用 Go，Go 与 C++ 后端的思维差异是什么？
> - 一个连接从 WebSocket/WSS 到业务处理，经过哪些层？
> - WebSocket 如何提供消息边界，JSON message 如何做大小与版本校验？
> - SHA-256 是什么，它能校验什么，不能防什么？
> - 为什么文件要先写临时文件，再原子改名？
> - 什么是幂等？`request_id` 如何避免重复执行？为什么断线不能自动重放命令？
> - `Dockerfile` 的多阶段构建、非 root、只读文件系统和 Compose 配置分别做什么？

---

## 1. 先建立整体认识

这是一个面向 AI Agent 的远程设备协作平台。它不是把命令直接暴露给公网，而是把几个角色和权限拆开：

```text
AI Agent / MCP Host
        |
        | 启动本机 yiuk-mcp，使用 MCP 调工具
        v
操作者侧 Go platformclient
        |
        | WSS 长连接，发送 execute_command / upload_file ...
        v
                 Go 平台服务
       认证、设备在线状态、会话授权、任务、文件中转
        ^
        | WSS 长连接
        |
设备所有者侧 Qt 客户端
        |  批准会话、读文件、执行 PowerShell、写文件
        v
目标 Windows 设备
```

平台服务的一个重要定位是：**中继和授权中心，而不是文件存储服务，也不是命令执行沙箱**。

- Go 平台服务保存设备、会话、任务和传输的内存状态。
- 命令在设备所有者当前用户权限下执行，不由 Go 服务执行。
- 文件内容经过平台转发，但平台的 transfer manager 不保存完整文件内容，只验证分块并转发。
- 服务重启后，在线连接、临时密码、会话、任务全部失效。
- 文件落盘发生在最终接收方：下载落到操作者本机，上传落到目标设备。

源码入口和主要目录：

```text
cmd/platform/                 Go 平台服务入口
cmd/yiuk-mcp/                 MCP stdio 适配器入口
cmd/smoke/                    命令行链路测试
cmd/device-sim/               设备模拟器

internal/platform/
  auth/                       平台 player_password 认证
  config/                     配置结构和默认限制
  device/                     设备、临时密码、在线连接
  session/                    会话状态机和权限
  task/                       命令任务、输出、游标、幂等
  transfer/                   下载/上传的分块状态和限制
  protocol/                   WebSocket JSON message 的编码、大小与版本校验
  transport/                  WebSocket/WSS 与明确允许的开发用 WS
  server/                     连接生命周期、路由、handler、janitor
  audit/                      脱敏审计日志

internal/platformclient/      操作者侧 Go 长连接客户端
internal/agenttools/          MCP 无关的工具契约和稳定错误码
internal/mcpadapter/          MCP 协议适配
```

和 C++ 的类比：

| C++ 后端概念 | 本项目 Go 对应物 |
| --- | --- |
| `main()` / 服务入口 | `cmd/platform/main.go` |
| `epoll`/事件循环 | Go runtime 调度 goroutine；连接由 `net.Listener` 接收，连接读写由 goroutine 处理 |
| `asio::ip::tcp::socket` | `net.Conn` |
| 自定义 framing | `internal/platform/protocol/protocol.go` 的 `Codec` |
| `std::mutex` | `sync.Mutex` / `sync.RWMutex` |
| 线程池任务 | goroutine + channel，或者直接调用被 mutex 保护的 Manager |
| RAII 清理 | `defer`；例如 `defer file.Close()`、`defer cancel()` |
| `std::error_code` | `error` 接口；用 `errors.Is`、`errors.As` 判断类型 |
| `shared_ptr` | Go 垃圾回收；通常不需要手写引用计数 |
| 编译目标和链接库 | Go module、`go build`、静态链接（本项目 `CGO_ENABLED=0`） |
| 线程安全容器 | 普通 map + 明确的 mutex；Go 原生 map 本身不是并发安全的 |

---

## 2. Go 入门：只学本项目真正用到的部分

### 2.1 包、函数和错误返回

Go 文件属于 package。`package server` 的多个文件可以直接访问同一个包内的类型和函数，但包外只能访问首字母大写的标识符：

```go
package example

import "errors"

var ErrNotFound = errors.New("not found")

func Find(id string) (string, error) {
    if id == "" {
        return "", ErrNotFound
    }
    return id, nil
}
```

Go 不使用异常作为常规控制流。调用者通常显式检查错误：

```go
value, err := Find("device-1")
if err != nil {
    return fmt.Errorf("find device: %w", err)
}
```

`%w` 会包装原错误，因此上层可以继续使用：

```go
if errors.Is(err, ErrNotFound) {
    // 处理“不存在”
}
```

这和 C++ 里通过错误码、`std::expected` 或异常表达失败不同。读本项目时，看到 `if err != nil` 不要认为是样板代码，它就是 Go 后端最主要的错误传播方式。

### 2.2 `:=`、指针和 struct

```go
id := "abc"       // 声明并推导类型
count := 3
count++

message := protocol.Message{
    Type:      protocol.TypePing,
    SessionID: sessionID,
}
```

`:=` 只能在函数内部使用，并且左侧至少要有一个新变量。指针存在，但一般不需要手动 `new/delete`：

```go
func FindTask() (*Task, bool) {
    task := &Task{Status: "running"}
    return task, true
}
```

返回的对象由垃圾回收器管理。`*Task` 表示指向 `Task` 的指针，`nil` 表示没有对象。项目中的 `ExitCode *int` 就是为了区分“没有退出码”和“退出码为 0”。

### 2.3 slice、map 和字符串长度

```go
chunks := []string{"a", "b"}
chunks = append(chunks, "c")

sessions := map[string]*Session{}
sessions[sessionID] = session

if value, ok := sessions[sessionID]; !ok {
    // 没找到
} else {
    // 找到了
}
```

- slice 类似可增长的 `std::vector`，底层可能扩容。
- map 类似 `std::unordered_map`，并发读写必须加锁。
- Go 的 `string` 是不可变字节序列，不一定是“字符数组”。`len(s)` 得到的是字节数，不是 Unicode 字符数。协议中的大小限制通常按字节限制是合理的。

### 2.4 `defer`：最接近 RAII 的常用写法

```go
file, err := os.Open(path)
if err != nil {
    return err
}
defer file.Close()
```

`defer` 会在当前函数返回时执行，适合关闭文件、连接和释放锁。注意锁的写法：

```go
m.mu.Lock()
defer m.mu.Unlock()
```

如果临界区很大，`defer` 也可能让锁持有时间变长；在性能敏感路径可以手动解锁，但首先要保证正确性。

### 2.5 goroutine、channel 和 mutex

```go
go c.writeLoop()
```

`go` 会启动一个 goroutine。它比创建一个操作系统线程轻量得多，但并不意味着“无需考虑并发”。本项目同时使用两种并发方式：

1. **连接级 goroutine**：每个连接读取消息，另一个 goroutine 专门写出消息。
2. **Manager + mutex**：`task.Manager`、`transfer.Manager` 等把 map 和状态放在一起，用 `sync.Mutex` 保护状态转换。

channel 是带类型的并发队列：

```go
send := make(chan protocol.Message, 128)
send <- message
message := <-send
```

本项目的连接有有界发送队列。队列满时不能无限制地阻塞内存；慢客户端会被断开或报错。这是后端里很重要的**背压**思想：接收方处理不过来时，系统必须有明确策略。

### 2.6 `context.Context`

`context` 用来传递取消信号、截止时间和请求范围：

```go
ctx, cancel := context.WithTimeout(parent, 5*time.Minute)
defer cancel()
```

服务关闭时取消根 context；连接断开时取消相关工作；超时时让调用链停止。不要把 context 放进长期保存的 struct，也不要把它当普通参数 map 使用。

### 2.7 Go 的测试和 race detector

```sh
go test ./...
go test -race ./internal/platform/... ./internal/platformclient/...
```

`-race` 会检测常见的数据竞争。对于本项目，任务状态、会话状态、连接注册和 janitor 都有并发访问，race test 比只测功能更重要。

---

## 3. 服务启动和连接生命周期

### 3.1 从 `cmd/platform` 到 `server.New`

启动流程可以概括为：

```text
读取 platform.yaml
    -> 解析配置和默认上限
    -> transport.Listen()
        -> TLS listener，或仅在 allow_insecure=true 时使用明文
    -> server.NewWithListener()
        -> 初始化 device/session/task/transfer manager
    -> Server.Run(ctx)
        -> Accept 循环
        -> 每个连接一个 serveConn goroutine
        -> janitor 定期清理过期对象
```

`internal/platform/server/server.go` 中的 `Server` 包含：

```go
type Server struct {
    cfg      config.Config
    listener net.Listener
    devices  *device.Manager
    sessions *session.Manager
    tasks    *task.Manager
    transfers *transfer.Manager
    uploads   *transfer.Manager
    conns    map[string]*conn
    mu       sync.Mutex
}
```

这里没有数据库连接池，也没有 Redis 客户端，因为当前版本是内存状态服务。`transfers` 和 `uploads` 分开，分别管理下载和上传的生命周期。

### 3.2 接收连接：不是一个连接一个线程的 C++ 直译

核心逻辑近似如下：

```go
for {
    nc, err := s.listener.Accept()
    if err != nil {
        // context 取消或 listener 关闭时退出
        return err
    }
    go s.serveConn(nc)
}
```

`serveConn` 会：

1. 给连接生成随机内部 ID。
2. 创建有界 `send` channel。
3. 注册到服务端连接 map。
4. 启动 `writeLoop`。
5. 当前 goroutine 执行 `readLoop`。
6. 读循环结束后注销连接并清理设备/操作者相关会话。

和 epoll 的差异：Go runtime 内部使用网络 poller，不需要业务代码手写 `epoll_wait`。你依然需要处理超时、关闭、半关闭、背压、连接状态和协议 framing，只是不再直接管理 fd 事件。

### 3.3 认证身份和权限是两层

项目有三种连接角色：匿名、player/operator、device。

- `player_password`：证明一个连接可以使用平台中继资源。
- `temporary_password`：证明操作者持有设备所有者发放的临时凭证。
- `approval` 模式：还必须等待设备所有者点击批准。
- `direct` 模式：跳过人工批准，但仍需要设备密码、平台认证、会话权限和会话有效期。

设备码不是密码，只是查找和标识设备。不能因为知道 `device_id` 就获得设备权限。

平台密码检查使用 `crypto/subtle.ConstantTimeCompare`，并按来源地址限流。服务端不会把密码、命令正文、完整输出写入审计日志。

---

## 4. 自定义协议：WebSocket 承载 JSON 消息

### 4.1 WebSocket message 边界

平台客户端使用 WebSocket 长连接；生产环境通过 WSS（TLS 1.2+），开发测试可显式使用 WS。每条 WebSocket binary message 直接包含一个 UTF-8 JSON envelope。WebSocket 实现负责底层 TCP 字节流的分片与重组，应用层不再额外添加长度头。

Go 使用 `ReadMessage` / `WriteMessage`，Qt 客户端按 WebSocket 帧协议解析分片并在完整 message 到达后交给 JSON parser。协议 codec 对完整 JSON 字节执行大小限制、JSON 解析、版本与必填类型校验；服务端默认限制单条 message 为 256 KiB。
- 先判断长度再分配，防止对端发一个很大的长度造成内存消耗。
- 当前默认单帧上限是 256 KiB。
- 文件内容不能一次塞进一帧，而是 64 KiB 左右的分块后 Base64 编码。

Base64 会增加约三分之一体积，所以“64 KiB 原始数据”经过编码后仍要低于帧上限。

### 4.3 JSON envelope

消息有统一 envelope：

```json
{
  "version": 1,
  "business_type": "agent_assist",
  "type": "execute_command",
  "request_id": "随机请求 ID",
  "correlation_id": "调用方关联 ID",
  "session_id": "会话 ID",
  "command": "Get-Date",
  "timeout_seconds": 60
}
```

常用字段：

- `version`：协议版本。
- `type`：消息类型，例如 `request_session`、`command_output`。
- `correlation_id`：直接响应对应哪个请求。
- `request_id`：任务或文件传输的业务身份，用于幂等和查询。
- `session_id`：权限边界。
- `sequence`：输出块或文件块的顺序。

当前用 JSON 是为了让 Qt 和 Go 共享清晰的显式 schema，不需要生成 Protobuf 代码。未来可以替换二进制 codec，但业务层不应该依赖具体编码格式。

---

## 5. 会话、任务和文件传输的状态机

### 5.1 会话状态

```text
requested -> pending_approval -> approved -> active -> closed
                    |               |         |
                    +-> rejected    +-> expired/interrupted
```

含义：

- `pending_approval`：等待设备所有者决定。
- `approved`：授权已成立，但还没有真正提交任务。
- `active`：已经有命令或文件操作在使用。
- `interrupted`：连接或设备断开，无法继续可靠完成。
- 终态不能回退。

每次任务提交都要经过：

1. session ID 合法性检查。
2. 当前连接是否属于这个 session。
3. session 是否仍有效。
4. session 是否拥有对应权限，例如 `execute_command`、`read_file`、`write_file`。
5. session 绑定的设备是否仍是当前在线连接。

这一步是防止 IDOR（只改一个 ID 就访问别人的资源）的核心。

### 5.2 命令任务状态

```text
queued -> running -> succeeded
                  -> failed
                  -> timed_out
                  -> cancelled
                  -> result_unknown
```

Go 的 `task.Manager` 保存：

- request ID 和 session ID。
- deadline。
- stdout/stderr 分块。
- 输出总字节数和 `truncated`。
- 可选退出码。
- `ResultKnown`。
- 是否请求取消。

输出设置总预算，超过预算后继续允许任务终止，但不再无限缓存输出，最终 `truncated=true`。这防止远端命令打印几 GB 内容把服务内存吃光。

`get_command_result` 支持 cursor：cursor 是**分块索引**，不是字节偏移。客户端拿到 `next_cursor` 后继续请求，可以避免重复拉取已经消费过的输出。

### 5.3 文件传输状态

文件传输也有 running、succeeded、failed、cancelled、result_unknown。`transfer.Manager` 验证：

- Base64 是否有效。
- 解码后分块是否为空。
- 单块是否超过上限。
- `sequence` 是否从 1 开始连续递增。
- 总字节数是否超过文件/配置上限。
- 分块数量是否超过上限。
- 一个 session 的并发传输数。

平台只保存计数和状态，转发原始分块，不把整个文件缓存到内存。

---

## 6. 幂等：先把这个概念真正搞懂

### 6.1 幂等的数学直觉

一个操作如果执行一次和执行多次，最终效果相同，就称为幂等。形式上：

```text
f(f(x)) = f(x)
```

常见例子：

- `GET /users/1`：读操作通常幂等。
- `PUT /users/1` 设置为同一个 JSON：通常幂等。
- `DELETE /users/1`：第一次删除，后续仍然是不存在，通常设计成幂等。
- `POST /orders` 创建订单：通常不幂等，重复 POST 可能创建两个订单。
- `mkdir -p /tmp/a`：目录已存在时仍能得到同样结果，可以设计为幂等。
- `rm file`：第一次成功，第二次可能报不存在；业务层可以把“已经不存在”视作同一结果，也可以不这么做。

**网络请求的“重试安全”不等于操作天然幂等。** 例如 TCP 连接断开后，客户端不知道命令到底有没有在设备上执行：

```text
客户端 -> 平台 -> 设备：执行删除/扣款/重启
设备已经执行
返回结果还没到客户端，连接断开
客户端重试
```

如果重试会再次执行命令，就可能产生灾难性副作用。

### 6.2 本项目如何使用 `request_id`

调用方为一次命令生成一个不可复用的 `request_id`。服务端收到后：

```go
t, created, err := s.tasks.Start(sessionID, requestID, timeout)
if err != nil {
    return err
}
if !created {
    // 已经存在：返回已有任务状态，不再向设备发送第二个命令
    return existingTaskResponse(t)
}
// 只有 created=true 才向设备转发 execute_command
```

`task.Manager.Start` 的规则：

1. request ID 不存在：创建任务，返回 `created=true`。
2. request ID 已存在且属于同一 session：返回已有任务，`created=false`。
3. request ID 已存在但属于另一个 session：返回冲突，不能跨会话复用。
4. 任务已经终态：重复 Finish 返回原终态，不覆盖第一次结果。

这样，同一个请求在网络层被重复送达时，不会再次启动 PowerShell。

文件传输也用 request ID，但上传分块并不是“随便重复发都安全”：分块必须按严格 sequence 到达，服务端拒绝乱序和重复块。终态 `Finish` 是幂等的，已经完成的传输不会被后来的结果覆盖。

### 6.3 哪些操作不能认为幂等

不要因为有 request ID 就把所有接口都当成幂等：

- `execute_command` 的命令正文可能是 `Remove-Item`、数据库写入或服务重启，本质上可能不幂等。
- `upload_file` 如果目标存在且明确设置 `overwrite=true`，重复执行会再次替换文件；最终内容可能相同，但文件修改时间等副作用可能不同。
- 临时密码轮换、关闭会话有状态副作用，需要由服务端根据状态机决定重复请求的结果。
- “查询任务结果”幂等，因为只是读取已有状态。

因此 request ID 的作用是：**把一次逻辑请求的身份固定下来，并让服务端对重复送达去重；它不是把命令本身变成安全的数学幂等操作。**

### 6.4 为什么断线不自动重放

本项目明确规定：**连接断线时不自动重放命令或文件传输。**

对于命令：

- 设备断线时，服务端把未确认任务标记为 `result_unknown`。
- 操作者断线时，会话被中断，任务不会偷偷再次发给设备。
- 进程可能已经执行、可能正在执行、也可能没执行，服务端不能伪造成功或失败。

对于文件：

- 传输中断时标记 `result_unknown`，设备端临时文件应清理。
- 客户端如果确实希望再试，应由上层明确发起一个新的逻辑操作，重新确认目标状态。

这是“可靠性”和“安全性”的取舍。自动重放可以提高成功率，但在不知道副作用是否已经发生时，可能造成重复命令。更高级的系统可以使用幂等键、设备端查询、事务日志或可恢复上传协议，但本项目当前选择保守地“不自动重放”。

---

## 7. SHA-256：第一次接触时应该理解到什么程度

### 7.1 它是什么

SHA-256 是一种密码学哈希函数：输入任意长度的字节，输出固定 256 bit，也就是 32 字节，通常显示成 64 个十六进制字符。

```text
输入：hello
输出：2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824
```

它具有：

- 相同输入得到相同输出。
- 输入改一个 bit，摘要通常大幅变化。
- 从摘要反推出原文在计算上不可行。
- 找到两个不同输入得到同一个摘要在设计目标下非常困难。

### 7.2 它在文件上传中解决什么问题

上传流程：

```text
操作者本地文件
    -> 读取文件，计算 local SHA-256
    -> 把文件大小和摘要写入 upload_file 元数据
    -> 按 64 KiB 分块发送
    -> 设备写临时文件
    -> 设备对临时文件计算 SHA-256
    -> 比较大小和摘要
    -> 通过后原子改名为目标文件
    -> 回报 remote SHA-256
    -> Go 客户端再次比较
```

Go 侧的简化代码：

```go
func fileSHA256(file *os.File) (string, error) {
    hash := sha256.New()
    if _, err := io.Copy(hash, file); err != nil {
        return "", err
    }
    return hex.EncodeToString(hash.Sum(nil)), nil
}
```

实际 `internal/platformclient/tools_upload.go` 会先检查普通文件和 32 MiB 上限，计算摘要，再 `Seek(0, io.SeekStart)` 回到文件开头，保证“声明的摘要”和“实际发送的字节”来自同一份读取。

下载时，操作者侧把收到的分块重新写入临时文件，同时更新 hash，最终比较：

```go
hash := sha256.New()
for _, chunk := range chunks {
    if _, err := tmp.Write(chunk); err != nil {
        return err
    }
    if _, err := hash.Write(chunk); err != nil {
        return err
    }
}
actual := hex.EncodeToString(hash.Sum(nil))
```

### 7.3 SHA-256 不能解决什么

SHA-256 只能说明“算摘要的两边看到的字节是否一致”，不能：

- 证明文件来源可信。
- 防止攻击者同时替换文件和摘要。
- 代替 TLS 加密。
- 代替身份认证和授权。
- 防止恶意文件本身执行危险内容。

因此本项目同时使用 TLS、会话权限、设备所有者批准和 SHA-256。TLS 保护传输和身份，SHA-256 保护文件内容完整性，授权系统决定谁能读写。

### 7.4 摘要、加密、编码的区别

| 技术 | 是否可还原 | 主要用途 |
| --- | --- | --- |
| SHA-256 | 不可逆（设计目标） | 完整性、内容指纹、密码校验值 |
| AES/TLS | 可用密钥解密 | 保密性 |
| Base64 | 可逆 | 把二进制放进 JSON/文本，不提供安全性 |

文件分块在协议中使用 Base64，是因为消息载荷是 JSON；它不是加密。真正的保密性来自 TLS。

---

## 8. 临时文件、原子改名和默认拒绝覆盖

### 8.1 为什么不能直接写目标文件

假设下载目标是 `result.zip`，直接写：

```text
打开 result.zip
写入一半
进程崩溃/磁盘满/网络中断
```

用户会得到一个存在但损坏的 `result.zip`。下一次程序看到文件存在，可能误以为下载完成。

正确做法：

```text
创建同目录临时文件 .yiuk-download-xxxx
按块写临时文件
计算 SHA-256
flush / fsync（本项目调用 tmp.Sync）
关闭临时文件
rename(临时文件, 目标文件)
```

项目的 `writeDownload` 在 `internal/platformclient/tools_file.go` 中使用 `os.CreateTemp`，写完后 `Sync`、`Close`，最后 `os.Rename`。发生错误时关闭并删除临时文件。

### 8.2 “原子”是什么意思

同一个文件系统内的 rename 通常是一个原子命名空间切换：其他进程不会看到“半个新文件”，而是看到旧文件或完整的新文件。

注意边界：

- 临时文件和目标文件必须在同一目录/同一文件系统，否则 rename 可能不是原子操作或直接失败。
- `Sync` 保证数据更接近落到磁盘，但不同操作系统和文件系统仍有更复杂的持久化语义。
- 原子改名不等于内容一定正确，内容正确仍要靠大小和 SHA-256 校验。
- Windows 上如果目标文件被另一个进程打开，rename 可能失败。

### 8.3 默认不覆盖的原因

项目的下载目标默认必须不存在：

```go
if _, err := os.Lstat(destination); err == nil {
    return LOCAL_PATH_EXISTS
}
```

上传到设备时，设备端默认也拒绝替换已有目标文件；只有调用者显式设置 `overwrite=true` 才允许覆盖。

这是一个安全默认值：

- 防止路径写错覆盖用户文件。
- 防止重试把已有的正确文件破坏掉。
- 防止“断线后不确定是否成功”时盲目再次写入。
- 让工具的破坏性行为在 MCP schema 和参数中显式可见。

“检查不存在，再 rename”存在经典 TOCTOU（检查和使用之间被其他进程抢先创建）窗口。因此设备端还应该在最终落盘操作处采用不覆盖语义，而不能只依赖事先的 `Stat`。当前项目把最终的写临时文件、校验、改名集中在设备端处理，并将覆盖意图作为显式参数。

### 8.4 上传的完整流程

```text
1. Go 操作者端检查 local_path 是绝对路径、普通文件、<= 32 MiB
2. 计算本地 SHA-256
3. upload_file 元数据发送：文件名、大小、摘要、overwrite
4. 文件按 64 KiB 分块，Base64 编码，sequence 从 1 开始
5. 平台检查权限、session、sequence、Base64、累计大小后转发
6. Qt 设备端在目标目录创建临时文件
7. 收齐分块后检查大小和 SHA-256
8. overwrite=false 且目标存在：失败，不覆盖
9. 校验成功：同目录原子改名到目标
10. 设备回报最终路径、大小、SHA-256
11. Go 再次比较结果，向 MCP 返回成功或 FILE_INTEGRITY_FAILED
```

空文件是特殊情况：它没有有效的非空分块，设备根据元数据创建并校验大小为 0 的文件。

---

## 9. 命令执行和断线处理

### 9.1 命令不在 Go 服务执行

`execute_command` 到达 Go 服务后，服务做授权、限流、任务创建和转发；实际 PowerShell 由目标 Windows 客户端执行。这样设计的原因：

- 命令需要访问目标机器的文件和环境。
- 服务端不应该拥有设备本地权限。
- 设备所有者需要在 UI 看到和批准操作。

Qt 侧 runner 使用临时 `.ps1` 文件、`pwsh` 优先、`powershell` 回退，stdout/stderr 分别读取，设置超时、取消和输出上限。普通用户权限运行，不自动提权；普通权限也不等于沙箱。

### 9.2 Go 服务的 handler 逻辑

`internal/platform/server/handler_command.go` 的主要顺序：

```text
校验 session_id/request_id
校验 command 非空、长度有界
校验 working_directory 长度
设置 timeout，限制在 1～3600 秒
sessions.Authorize(..., PermissionExecuteCommand)
确认设备仍是绑定的在线连接
 tasks.Start(...)
如果 created=false：返回已有任务，不重复转发
sessions.Activate(...)
sendToConnection(device, execute_command)
返回 command_accepted
```

设备回传输出时，服务端再次检查“这个连接是不是这个 session 绑定的目标设备”，然后进入 `AppendOutput`，保存有限的输出并转发给操作者。

### 9.3 为什么有 `result_unknown`

`failed` 表示系统知道操作失败；`cancelled` 表示系统知道取消；`result_unknown` 表示结果无法可靠判断。

例如：

```text
设备收到命令并开始执行
设备发出了部分 stdout
设备网卡断开
```

此时不能简单说“失败”，因为命令可能已经完成；也不能说“成功”，因为没有收到终态。`result_unknown` 是分布式系统中比猜测更诚实的状态。

---

## 10. 心跳、janitor 和资源上限

### 10.1 心跳不是业务请求

设备连接和操作者连接都需要保活。空闲连接如果只依赖业务流量，可能被 NAT、防火墙或负载均衡设备认为失效。

项目使用 ping/pong：

- 约每 15 秒发送心跳。
- 连续多个周期没有回应就关闭连接。
- 关闭后，下次工具调用重新连接。
- 重新连接不会重放上一次命令。

### 10.2 janitor

服务中有定期清理任务，负责收敛无法靠业务消息自然结束的状态：

- 过期的临时密码。
- 到期的 pending/active 会话。
- 超过 deadline 的命令任务。
- 超时的文件传输。
- 失联连接相关资源。

过期不是简单地从 map 删除。需要先转到明确终态，并通知相关连接，必要时向设备发送 cancel。这样调用方可以知道任务是 timeout、cancelled 还是 result_unknown。

### 10.3 为什么所有东西都要有上限

网络服务的安全边界不仅是认证，还包括资源：

- 最大连接数。
- 最大设备数和每设备会话数。
- 每 session 最大并发任务/传输数。
- 单帧最大字节数。
- 单文件最大 32 MiB。
- 单块大小和最大块数量。
- 单任务 stdout/stderr 总输出预算。
- 命令、路径、purpose 的最大长度。
- 认证失败按 IP 限流。

没有这些边界，一个合法身份也能通过超大命令、超多连接或无限输出消耗服务。

---

## 11. TLS 和认证边界

`internal/platform/transport/listener.go` 的规则很直接：

```go
if certFile != "" || keyFile != "" {
    // 必须成对加载，TLS 最低 1.2
    return tls.Listen("tcp", address, tlsConfig)
}
if !allowInsecure {
    return error("TLS is required")
}
return net.Listen("tcp", address)
```

生产环境应使用真实证书。自签证书适合测试，但客户端应固定（pin）证书 SHA-256 指纹，并在证书变化时提示用户，而不是无条件接受任何新证书。

TLS 解决的是：

- 传输保密。
- 传输完整性。
- 证书身份验证。

TLS 不会自动解决：

- 操作者有没有设备权限。
- session 是否过期。
- 命令是不是危险的。
- 重复请求是否会重复执行。

所以项目仍然需要 player password、temporary password、owner approval、session permission 和 request ID。

---

## 12. Dockerfile 逐段理解

根目录 `Dockerfile` 使用多阶段构建：

```dockerfile
ARG GO_VERSION=1.27.3
FROM golang:${GO_VERSION}-alpine AS build
WORKDIR /src
ENV CGO_ENABLED=0 GOOS=linux
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN go build -trimpath -ldflags='-s -w' -o /out/yiuk-platform ./cmd/platform
```

逐行解释：

- `ARG GO_VERSION`：构建参数，可以通过 `--build-arg` 改 Go 版本。
- `golang:...-alpine`：带 Go 编译器的构建镜像。
- `CGO_ENABLED=0`：关闭 cgo，本项目纯 Go，便于产生静态二进制。
- 先复制 `go.mod/go.sum` 再 `go mod download`：利用 Docker layer cache；源码没变时不必重新下载依赖。
- `-trimpath`：减少构建路径信息。
- `-ldflags='-s -w'`：去掉符号和调试信息，减小二进制；开发调试不一定要用。

运行阶段：

```dockerfile
FROM alpine:3.22
RUN apk add --no-cache ca-certificates openssl \
    && addgroup -S -g 10001 yiuk \
    && adduser -S -u 10001 -G yiuk ...
COPY --from=build /out/yiuk-platform /usr/local/bin/yiuk-platform
USER yiuk
WORKDIR /var/lib/yiuk-platform
VOLUME ["/var/lib/yiuk-platform"]
EXPOSE 29564
ENTRYPOINT ["/usr/local/bin/docker-entrypoint.sh"]
```

关键点：

- 最终镜像不包含 Go 编译器和源码，体积更小、攻击面更小。
- `ca-certificates` 供 TLS 使用，`openssl` 用于测试场景生成自签证书。
- 使用 uid/gid `10001` 的非 root 用户。
- `/var/lib/yiuk-platform` 保存配置、证书和日志；业务状态仍在内存。
- `EXPOSE` 只是镜像元数据，不等于宿主机已经开放端口；Compose 的 `ports` 才负责映射。
- `STOPSIGNAL SIGTERM` 让服务有机会优雅关闭。
- `HEALTHCHECK` 用 `nc` 检查监听端口。

### 12.1 为什么 Dockerfile 要多阶段

如果把编译和运行写在一个镜像里，运行镜像会带着 Go、源码和构建缓存。多阶段只从 build 阶段复制一个二进制：

```text
build image: Go + 源码 + module cache
runtime image: Alpine + 二进制 + entrypoint + 必要工具
```

这是生产镜像的常见做法。

### 12.2 Compose 的含义

`docker-compose.yml` 主要配置：

```yaml
services:
  platform:
    build: .
    ports:
      - "29564:29564"
    environment:
      YIUK_PLAYER_PASSWORD: "..."
    volumes:
      - yiuk-platform-data:/var/lib/yiuk-platform
    read_only: true
    tmpfs:
      - /tmp
    security_opt:
      - no-new-privileges:true
```

- `build`：本地构建镜像。
- `ports`：宿主机端口映射到容器。
- `environment`：注入配置；不要把真实密码提交进 git。
- `volumes`：保存配置、证书和日志。它不会保存 Go 内存状态。
- `read_only: true`：容器根文件系统只读；允许写的主要是 volume 和 `/tmp`。
- `tmpfs`：内存中的临时文件系统，容器删除后消失。
- `no-new-privileges`：防止进程通过某些方式获得更高权限。
- `restart: unless-stopped`：异常退出时自动重启，但重启会清空平台内存状态。

常用命令：

```sh
cp .env.example .env
# 在 .env 中填 YIUK_PLAYER_PASSWORD

docker compose up -d --build
docker compose ps
docker compose logs -f
docker compose down
```

### 12.3 entrypoint 为什么先写 `.tmp` 再 `mv`

`deploy/docker-entrypoint.sh` 会根据环境变量生成 `platform.yaml`。它不是直接覆盖目标，而是：

```sh
... > "$CONFIG.tmp"
chmod 600 "$CONFIG.tmp"
mv "$CONFIG.tmp" "$CONFIG"
```

这和下载文件的临时文件 + 原子改名是同一类思想：即使生成配置时出错，也不应该把原配置截断成半份。配置文件中也会处理 YAML 字符串转义，避免密码里的 `: # " \` 破坏 YAML。

---

## 13. MCP：只需要掌握的最小知识

### 13.1 MCP 是什么

MCP（Model Context Protocol）是让 AI Host 以标准方式发现和调用外部工具的协议。可以把它理解成一层“AI 工具 RPC”：

```text
AI Host（例如 pi）
    | JSON-RPC / MCP
    v
本机启动的 yiuk-mcp 子进程
    | Go 内部调用
    v
platformclient -> TLS -> Go 平台服务
```

本项目不把 MCP 直接塞进 Qt GUI，而是运行独立的 `yiuk-mcp`，因为 MCP 通常使用 stdio，独立子进程更适合管理生命周期和标准输入输出。

### 13.2 stdio 模式

Host 启动 `yiuk-mcp.exe`，通过 stdin/stdout 交换 MCP 消息：

- stdout 只能输出协议消息，不能混入普通调试日志。
- 日志应写 stderr。
- MCP adapter 负责把工具调用转换到 `agenttools`。
- `agenttools` 再调用远端 `platformclient` 或本机 bridge。

`cmd/yiuk-mcp/main.go` 的选择顺序是：

1. 尝试读取用户配置，创建远程 `platformclient`。
2. 远程配置不可用时，回退本机 Qt bridge。
3. 两者都不可用时，工具返回 `NOT_CONFIGURED`，不伪造远程结果。

### 13.3 工具和结果

工具例如：

```text
list_devices
get_device_status
request_connection
run_command
execute_command
get_command_result
download_file
upload_file
cancel_command
close_session
```

统一结果形状大致是：

```json
{
  "ok": true,
  "code": "OK",
  "message": "",
  "data": {}
}
```

MCP 只是调用入口，真正的安全检查仍在 Go 平台服务中。不能因为某个 Agent 调用了 MCP，就跳过 session、权限和设备绑定校验。

### 13.4 MCP 和幂等的关系

MCP Host 可能因为模型、用户或连接原因再次调用工具，所以工具设计要把以下信息交给上层：

- `request_id`。
- 明确的状态，例如 `running`、`result_unknown`。
- 稳定错误码，而不是只返回一段人类可读文本。
- 上传是否 `overwrite`。
- 工具是否只读、是否有破坏性、是否幂等。

MCP 本身不会替你处理命令重放。后端的 request ID 和断线不自动重放策略仍然是最终安全边界。

---

## 14. 推荐的源码阅读顺序

不要一上来从 Qt 或全部 handler 读起，建议按下面顺序：

### 第一步：先跑通最短路径

```sh
go test ./...
go build ./cmd/platform
go build ./cmd/yiuk-mcp
```

然后看：

```text
cmd/platform/main.go
internal/platform/server/server.go
internal/platform/protocol/protocol.go
```

目标是理解“配置 -> listener -> accept -> read/write loop”。

### 第二步：读协议和测试

阅读：

```text
internal/platform/protocol/protocol.go
internal/platform/protocol/protocol_test.go
```

重点回答：

- WebSocket binary message 是如何限制大小和校验版本的？
- 最大帧是多少？
- `ReadFull` 如何处理半包？
- JSON version 如何校验？

### 第三步：读三个 Manager

顺序建议：

```text
internal/platform/task/manager.go
internal/platform/transfer/manager.go
internal/platform/session/manager.go
```

重点看每个方法的锁范围、状态转换、clone、上限和终态处理。`clone()` 很重要：Manager 不应该把内部可变指针直接暴露给调用方。

### 第四步：读 handler

```text
internal/platform/server/handler_session.go
internal/platform/server/handler_command.go
internal/platform/server/handler_download.go
internal/platform/server/handler_upload.go
```

用固定模板读每个 handler：

```text
参数校验
 -> 身份/连接角色校验
 -> session 权限校验
 -> 设备在线和绑定校验
 -> Manager 创建或查找状态
 -> 转发消息
 -> 记录脱敏审计
 -> 返回直接响应
```

### 第五步：读客户端文件处理

```text
internal/platformclient/tools_upload.go
internal/platformclient/tools_file.go
```

这里能看到 SHA-256、Base64、64 KiB 分块、临时文件、Sync、Rename、默认不覆盖的完整实现。

### 第六步：最后读 MCP 和 Docker

```text
internal/mcpadapter/
internal/agenttools/
cmd/yiuk-mcp/main.go
Dockerfile
docker-compose.yml
deploy/docker-entrypoint.sh
```

此时你已经知道 MCP 调用的后端是什么，也能理解容器只是打包和运行方式，不是业务架构本身。

---

## 15. 从 C++ 迁移到 Go 时最容易踩的坑

### 15.1 把 goroutine 当作“永远不会阻塞的线程”

goroutine 仍然可能死循环、等待 channel、持有锁太久或泄漏。每个长期 goroutine 都应该有退出条件，例如 context、done channel 或连接关闭。

### 15.2 以为 map 并发安全

普通 map 并发写会 panic，读写竞争会被 race detector 报告。状态 Manager 的 mutex 是必需的，不是装饰。

### 15.3 忽略 slice 的共享底层数组

返回内部 slice 后，调用者可能修改它或看到并发变化。所以 task/transfer Manager 返回 clone，输出 chunks 也复制。

### 15.4 用 `len(string)` 当字符数

它是字节数。协议限制、文件大小、帧大小按字节通常正确；如果未来要限制用户看到的 Unicode 字符数，需要额外使用 rune 计数并明确语义。

### 15.5 误以为 `Write` 对应一次 `Read`

TCP framing 永远要存在，Go 的 `net.Conn` 也不会替你保留消息边界。

### 15.6 只判断错误文本，不使用 `errors.Is`

错误可能被 `%w` 包装。应使用稳定的 sentinel error 或自定义类型，不要依赖字符串比较。

### 15.7 把 `defer` 放在超大循环里

循环内大量 defer 会延迟到外层函数返回。文件传输时应明确资源生命周期，必要时在每轮或辅助函数中释放。

### 15.8 误把 context 当超时万能药

context 只能传播取消意图。底层 socket、进程、文件写入仍需要正确响应取消；设备端 PowerShell 进程树清理也必须真实实现。

### 15.9 只做功能测试，不做竞态和断线测试

本项目最危险的 bug 往往发生在：

- 设备恰好断线。
- 同一个 request ID 重复到达。
- cancel 和 result 同时到达。
- session 到期和新命令同时到达。
- 输出超过上限。
- 慢客户端堵满发送队列。

这些需要单测、集成测试、`go test -race` 和真实双机测试共同覆盖。

---

## 16. 一份适合当前项目的学习路线

### 阶段 A：Go 语法和标准库（1～2 天）

学习：

- package、struct、interface、slice、map。
- error、`errors.Is`、`fmt.Errorf("%w")`。
- pointer、method、constructor 风格。
- goroutine、channel、mutex。
- `io.Reader`、`io.Writer`、`os.File`、`net.Conn`。
- `context` 和 `encoding/json`。

练习：写一个带大小限制和版本校验的 WebSocket JSON echo server/client。

### 阶段 B：复刻本项目的三个小功能（2～3 天）

1. 写 `sha256sum <file>`，输出十六进制摘要。
2. 写“临时文件写入 + Sync + Rename”，失败时清理临时文件。
3. 写一个带 mutex 的 task manager，支持 request ID 去重和终态不可变。

### 阶段 C：读项目后端（2～4 天）

按第 14 节顺序读协议、server、manager、handler、client。每读一个模块都画一张状态图，不要只看函数名。

### 阶段 D：网络和异常路径（持续）

重点练习：

- 半包、粘包、超大帧。
- TLS 握手失败和证书变化。
- 设备断线、操作者断线。
- 超时与取消竞态。
- 重复 request ID、乱序 chunk。
- 文件校验失败、磁盘满、目标已存在。

### 阶段 E：Docker（半天～1 天）

掌握：

```text
Dockerfile 的 layer/cache
多阶段构建
非 root 用户
端口映射
volume 和 tmpfs
环境变量与 secret
healthcheck
read_only 文件系统
容器重启时哪些状态会丢失
```

当前项目最值得记住的一句话是：**volume 只保存磁盘文件；Go 平台的设备、会话、任务状态仍然在内存，容器重启后全部失效。**

---

## 17. 总结：把这个项目压缩成十句话

1. Go 用 goroutine、channel、WebSocket message 和少量 mutex 写出了一个并发 WSS 后端。
2. 每条 WebSocket binary message 直接承载 JSON，codec 对消息大小、版本和类型做校验。
3. Server 只做认证、授权、状态、转发和限制，命令由目标 Windows 客户端执行。
4. player password 只允许使用平台；temporary password 和 owner approval 才决定设备访问。
5. session 是权限边界，task 和 transfer 是可追踪的异步状态机。
6. SHA-256 用来验证文件字节一致，不负责加密、认证或安全执行文件。
7. 文件先写同目录临时文件，校验并 Sync 后原子 Rename，避免半文件暴露。
8. 下载和设备上传默认拒绝覆盖，覆盖必须由调用者显式选择。
9. 幂等是重复送达不重复产生副作用；request ID 去重，但不能让任意命令天然安全。
10. 断线不自动重放，无法确认的任务标记 `result_unknown`，由人或上层明确决定下一步。

如果先掌握这十句话，再回头看代码，整个项目会从“很多 Go 文件”变成“协议、状态机、授权、传输完整性和资源边界”这几条清晰主线。
