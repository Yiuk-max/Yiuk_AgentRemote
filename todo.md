协助时平台那里已经断联，客户端任然显示协助ing，显示仍然连接着平台

但 Go 侧operator/MCP 客户端从不发
心跳(internal/platformclient 里
没有任何 ping)。所以会话闲置超过45秒→连接被服务端判死→transport 自动重连（新连接ID)→会话仍绑在旧连接上→再调 close_session 就not
found / connection does not own the session。这次我把挂起压到25秒，关闭就正常了。
两个建议的修复（都改 GO，不改Qt)：

1. operator 客户端加心跳（比如 15 秒一次 ping)，长会话就不会莫名断；
2.DEVICE ALREADY_ONLINE 从"永久放弃重连“改成退避重试（那是 Qt侧，需重新编译客户端）。
