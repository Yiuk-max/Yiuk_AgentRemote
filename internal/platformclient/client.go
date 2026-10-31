// Package platformclient 面向远程协助平台实现 agenttools.Backend 接口
// 每个会话保持一个经过认证的操作端连接，避免工具调用之间中断会话；同时在本地缓存流式命令输出，并返回结构化的 agenttools 错误

package platformclient

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"agentremote/internal/agenttools"
	"agentremote/internal/platform/protocol"
	"agentremote/internal/platform/transport"
)

// Client 表示与平台保持的持久操作端连接
type Client struct {
	cfg   Config
	codec protocol.Codec

	callMu sync.Mutex // 串行化工具调用和连接初始化

	// writeMu 串行化帧写入，避免心跳 goroutine 与执行中的工具调用交错写入数据
	writeMu sync.Mutex

	mu        sync.Mutex
	conn      transport.Conn
	connID    string
	inbox     chan protocol.Message
	connDone  chan struct{}
	connected bool
	closed    bool

	// pongs 统计当前连接收到的 pong 帧数；心跳 goroutine 根据计数是否停滞判断对端是否失效
	pongs atomic.Uint64

	cacheMu   sync.Mutex
	sessions  map[string]protocol.SessionInfo
	tasks     map[string]*taskCache
	downloads map[string]*downloadCache
	devices   []protocol.DeviceInfo
}

// Close 关闭操作端连接
func (c *Client) Close() error {
	c.callMu.Lock()
	defer c.callMu.Unlock()
	c.mu.Lock()
	c.closed = true
	conn, done := c.conn, c.connDone
	c.mu.Unlock()
	if conn != nil {
		_ = conn.Close()
	}
	if done != nil {
		c.markDisconnected(done)
	}
	return nil
}

// Invoke 实现 agenttools.Backend
func (c *Client) Invoke(ctx context.Context, req agenttools.Request) (any, error) {
	switch req.Name {
	case agenttools.ListDevices:
		return c.listDevices(ctx)
	case agenttools.GetDeviceStatus:
		return c.getDeviceStatus(ctx, req.Input)
	case agenttools.RequestConnection:
		return c.requestConnection(ctx, req.Input)
	case agenttools.RunCommand:
		return c.runCommand(ctx, req.Input)
	case agenttools.GetSessionStatus:
		return c.getSessionStatus(ctx, req.Input)
	case agenttools.ExecuteCommand:
		return c.executeCommand(ctx, req.Input)
	case agenttools.GetCommandResult:
		return c.getCommandResult(ctx, req.Input)
	case agenttools.DownloadFile:
		return c.downloadFile(ctx, req.Input)
	case agenttools.UploadFile:
		return c.uploadFile(ctx, req.Input)
	case agenttools.CancelCommand:
		return c.cancelCommand(ctx, req.Input)
	case agenttools.CloseSession:
		return c.closeSession(ctx, req.Input)
	default:
		return nil, &agenttools.Error{Code: "UNKNOWN_TOOL", Message: "unsupported tool"}
	}
}

func (c *Client) call(ctx context.Context, req protocol.Message, match func(protocol.Message) bool) (protocol.Message, error) {
	return c.callTimeout(ctx, req, match, time.Duration(c.cfg.TimeoutSeconds)*time.Second)
}

func (c *Client) callTimeout(ctx context.Context, req protocol.Message, match func(protocol.Message) bool, timeout time.Duration) (protocol.Message, error) {
	return c.callStream(ctx, req, nil, match, timeout)
}

// callStream 发送一个请求，可选地在同一连接上继续发送额外数据帧（如文件分块），然后等待对应的响应
// 整个交互期间都会持有调用互斥锁，避免其他工具调用将数据帧插入流中
func (c *Client) callStream(ctx context.Context, req protocol.Message, frames func(write func(protocol.Message) error) error, match func(protocol.Message) bool, timeout time.Duration) (protocol.Message, error) {
	c.callMu.Lock()
	defer c.callMu.Unlock()

	if err := c.ensureConnectedLocked(); err != nil {
		return protocol.Message{}, err
	}
	c.mu.Lock()
	conn, inbox, done := c.conn, c.inbox, c.connDone
	c.mu.Unlock()

	// 处理两次调用之间到达的事件，确保将新请求与响应关联前缓存的输出是最新的
	c.drainInbox(inbox)

	correlationID := randomID()
	req.Version = protocol.Version
	req.BusinessType = protocol.BusinessAgentAssist
	req.CorrelationID = correlationID
	if err := c.writeMessage(conn, req); err != nil {
		c.markDisconnected(done)
		return protocol.Message{}, &agenttools.Error{Code: "PLATFORM_UNAVAILABLE", Message: "could not send request to platform"}
	}
	if frames != nil {
		write := func(m protocol.Message) error {
			m.Version = protocol.Version
			m.BusinessType = protocol.BusinessAgentAssist
			return c.writeMessage(conn, m)
		}
		if err := frames(write); err != nil {
			c.markDisconnected(done)
			return protocol.Message{}, &agenttools.Error{Code: "PLATFORM_UNAVAILABLE", Message: "could not stream data to platform: " + err.Error()}
		}
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return protocol.Message{}, &agenttools.Error{Code: "CANCELLED", Message: "tool call was cancelled"}
		case <-timer.C:
			return protocol.Message{}, &agenttools.Error{Code: "PLATFORM_TIMEOUT", Message: "platform did not respond in time"}
		case <-done:
			return protocol.Message{}, &agenttools.Error{Code: "PLATFORM_UNAVAILABLE", Message: "platform connection is unavailable"}
		case m := <-inbox:
			c.observe(m)
			if m.CorrelationID != correlationID {
				continue
			}
			if m.Type == protocol.TypeError {
				code := m.Code
				if code == "" {
					code = "PLATFORM_ERROR"
				}
				return m, &agenttools.Error{Code: code, Message: m.Message}
			}
			if match(m) {
				return m, nil
			}
		}
	}
}

func (c *Client) drainInbox(inbox chan protocol.Message) {
	for {
		select {
		case m := <-inbox:
			c.observe(m)
		default:
			return
		}
	}
}
