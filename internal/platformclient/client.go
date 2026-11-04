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
	config Config
	codec  protocol.Codec

	requestMu sync.Mutex // 串行化请求交互和连接初始化

	// writeMu 串行化帧写入，避免心跳 goroutine 与执行中的工具调用交错写入数据
	writeMu sync.Mutex

	connectionMu     sync.Mutex // 保护连接状态；不负责串行化请求或帧写入
	conn             transport.Conn
	connectionID     string
	incomingMessages chan protocol.Message
	connectionClosed chan struct{}
	connected        bool
	closed           bool

	// pongCount 统计收到的 pong 帧数；心跳 goroutine 根据计数是否停滞判断对端是否失效
	pongCount atomic.Uint64

	cacheMu         sync.Mutex
	cachedSessions  map[string]protocol.SessionInfo
	cachedCommands  map[string]*commandResultCache
	cachedDownloads map[string]*downloadCache
	cachedDevices   []protocol.DeviceInfo
}

// Close 关闭操作端连接
func (c *Client) Close() error {
	c.requestMu.Lock()
	defer c.requestMu.Unlock()
	c.connectionMu.Lock()
	c.closed = true
	conn, connectionClosed := c.conn, c.connectionClosed
	c.connectionMu.Unlock()
	if conn != nil {
		_ = conn.Close()
	}
	if connectionClosed != nil {
		c.closeConnection(connectionClosed)
	}
	return nil
}

// Invoke 按工具名称分发调用，并将原始 JSON 参数交给对应的平台操作。
func (c *Client) Invoke(ctx context.Context, request agenttools.Request) (any, error) {
	switch request.Name {
	case agenttools.ListDevices:
		return c.handleListDevices(ctx)
	case agenttools.GetDeviceStatus:
		return c.handleGetDeviceStatus(ctx, request.Input)
	case agenttools.RequestConnection:
		return c.handleRequestConnection(ctx, request.Input)
	case agenttools.RunCommand:
		return c.handleRunCommand(ctx, request.Input)
	case agenttools.GetSessionStatus:
		return c.handleGetSessionStatus(ctx, request.Input)
	case agenttools.ExecuteCommand:
		return c.handleExecuteCommand(ctx, request.Input)
	case agenttools.GetCommandResult:
		return c.handleGetCommandResult(ctx, request.Input)
	case agenttools.DownloadFile:
		return c.handleDownloadFile(ctx, request.Input)
	case agenttools.UploadFile:
		return c.handleUploadFile(ctx, request.Input)
	case agenttools.CancelCommand:
		return c.handleCancelCommand(ctx, request.Input)
	case agenttools.CloseSession:
		return c.handleCloseSession(ctx, request.Input)
	default:
		return nil, &agenttools.Error{Code: "UNKNOWN_TOOL", Message: "unsupported tool"}
	}
}

// sendRequest 使用客户端配置的默认超时发送请求，并等待匹配的响应。
func (c *Client) sendRequest(ctx context.Context, request protocol.Message, matchesResponse func(protocol.Message) bool) (protocol.Message, error) {
	return c.sendRequestWithTimeout(ctx, request, matchesResponse, time.Duration(c.config.TimeoutSeconds)*time.Second)
}

// sendRequestWithTimeout 使用指定超时发送请求，并等待匹配的响应。
func (c *Client) sendRequestWithTimeout(ctx context.Context, request protocol.Message, matchesResponse func(protocol.Message) bool, timeout time.Duration) (protocol.Message, error) {
	return c.sendStreamingRequest(ctx, request, nil, matchesResponse, timeout)
}

// sendStreamingRequest 发送请求及可选的后续数据帧，然后等待同一 correlation_id 下匹配的响应。
// 整个交互期间持有 requestMu，避免其他请求将数据帧插入流中。
func (c *Client) sendStreamingRequest(ctx context.Context, request protocol.Message, sendFrames func(sendFrame func(protocol.Message) error) error, matchesResponse func(protocol.Message) bool, timeout time.Duration) (protocol.Message, error) {
	c.requestMu.Lock()
	defer c.requestMu.Unlock()

	if err := c.connectIfNeeded(); err != nil {
		return protocol.Message{}, err
	}
	c.connectionMu.Lock()
	conn, incomingMessages, connectionClosed := c.conn, c.incomingMessages, c.connectionClosed
	c.connectionMu.Unlock()

	// 处理两次调用之间到达的事件，确保将新请求与响应关联前缓存的输出是最新的
	c.cachePendingMessages(incomingMessages)

	correlationID := randomID()
	request.Version = protocol.Version
	request.BusinessType = protocol.BusinessAgentAssist
	request.CorrelationID = correlationID
	if err := c.writeProtocolMessage(conn, request); err != nil {
		c.closeConnection(connectionClosed)
		return protocol.Message{}, &agenttools.Error{Code: "PLATFORM_UNAVAILABLE", Message: "could not send request to platform"}
	}
	if sendFrames != nil {
		sendFrame := func(message protocol.Message) error {
			message.Version = protocol.Version
			message.BusinessType = protocol.BusinessAgentAssist
			return c.writeProtocolMessage(conn, message)
		}
		if err := sendFrames(sendFrame); err != nil {
			c.closeConnection(connectionClosed)
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
		case <-connectionClosed:
			return protocol.Message{}, &agenttools.Error{Code: "PLATFORM_UNAVAILABLE", Message: "platform connection is unavailable"}
		case message := <-incomingMessages:
			c.cacheMessage(message)
			if message.CorrelationID != correlationID {
				continue
			}
			if message.Type == protocol.TypeError {
				code := message.Code
				if code == "" {
					code = "PLATFORM_ERROR"
				}
				return message, &agenttools.Error{Code: code, Message: message.Message}
			}
			if matchesResponse(message) {
				return message, nil
			}
		}
	}
}

// cachePendingMessages 将当前已排队的消息写入缓存，供下一次请求使用。
func (c *Client) cachePendingMessages(incomingMessages chan protocol.Message) {
	for {
		select {
		case message := <-incomingMessages:
			c.cacheMessage(message)
		default:
			return
		}
	}
}
