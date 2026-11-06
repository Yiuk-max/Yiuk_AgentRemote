1 ✅ 设备码可发起申请连接；不提供临时密码时进入申请/批准流程。提供正确的临时密码时直接连接，不需要在设备设置中切换授权模式。

2 ✅ 移除主页手动发起连接区域；收藏设备的添加与管理移至设备页面。

3 ✅ 会话建立或批准后自动打开活动面板。

4 ✅ 活动面板要优化,更优雅些，自定义玻璃化

5 ✅ 制作setup 安装包

7     github 仓库、主页、readme更新精修

8 ✅ readme邀请合作开发,寻求有mac的开发者开发出 mac 版本客户端

9 ✅设备码生成机制

10 危险命令拦截

11 MCP 工具扩展:截屏、模拟点击

12 ✅客户端没有“换一个 设备码ID 重试”的逻辑

13 申请、询问窗口,问用户需要什么

14 ✅客户端发送阶段有没有写超时兜底

15 ✅ 部分函数命名和实际功能不符,代码阅读困难

16 给客户端设计图标

17 已经启动时，再启动，不新开进程，打开已有进程主页面

18 终态会话永不回收；进程跑够 2048 次会话创建后，**所有新会话永久 `SESSION_LIMIT`**，直到重启

19  | 任务/传输终态记录永不回收 | `internal/platform/task/manager.go`、`transfer/manager.go` | 每条大输出命令最多留 2 MiB 常驻；长时间运行会 OOM

20 Host 是可以并发发多个 tools/call 的（比如同时问两个设备状态）。但 platformclient 是单飞的：

 ```go
   // internal/platformclient/client.go
   func (c *Client) sendStreamingRequest(...) {
       c.requestMu.Lock()          // ← 整个请求期间独占
       defer c.requestMu.Unlock()
 ```

 → 第二个并发调用会在 requestMu 上阻塞到第一个超时，模型看到的是 PLATFORM_TIMEOUT，而它完全不知道自己只是排在后面。
 这是 docs/key-architecture.md 第 5 节第 9 条登记的薄弱点，改法是 per-request pending map（correlation_id → chan）

-21 大改造：分布式+微服务
   1.先引入redis，把设备码从内部维持的map放到redis
   2.Nginx 反向代理和gateway引入，先把网关和业务拆分为两部分
   3.Rabbit引入，实现不同网关间消息传递、转发
   4.后面把业务拆成微服务
22 新增用户系统（计划微服务）与数据库持久化，尝试引入邮件验证码

23 客户端版本更新，数据库存储setup安装包，客户端拉取并完成更新
