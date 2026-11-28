// Package platformclient 面向远程协助平台实现 agenttools.Backend 接口
// 每个会话保持一个经过认证的操作端连接，避免工具调用之间中断会话；同时在本地缓存流式命令输出，并返回结构化的 agenttools 错误

package platformclient

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"time"

	"agentremote/internal/agenttools"
	"agentremote/internal/platform/protocol"
	"agentremote/internal/platform/transport"
)

// Client 表示与平台保持的持久操作端连接
type Client struct {
	config       Config
	codec        protocol.Codec
	toolHandlers map[string]toolHandler

	// requestMu 串行化请求交互和连接初始化。
	//
	// TODO(薄弱点 #9，见 docs/key-architecture.md §4)：一把锁意味着并发工具调用
	// 会互相阻塞到超时。长任务已经走会话式 execute_command + get_command_result，
	// 但两个并行工具调用仍会排队；应改成 per-request 的 pending map。
	requestMu sync.Mutex

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

// toolHandler 是后端对单个工具的实现：拿原始 JSON 参数，返回结构化结果或业务错误。
type toolHandler func(ctx context.Context, input json.RawMessage) (any, error)

// Invoke 按工具名称分发调用。
//
// 分发走注册表而不是 switch：新增工具时只需要在 toolHandlers 里加一行，
// TestEveryBackendToolHasAHandler 会检查「目录里有、这里没有」的漏项。
func (c *Client) Invoke(ctx context.Context, request agenttools.Request) (any, error) {
	handler, ok := c.toolHandlers[request.Name]
	if !ok {
		return nil, &agenttools.Error{Code: "UNKNOWN_TOOL", Message: "unsupported tool"}
	}
	return handler(ctx, request.Input)
}

// newToolHandlers 把工具名映射到具体实现。两个询问工具共用 handleQuestion，
// 由请求里的 Name 区分 ask_user 与 get_user_reply。
func (c *Client) newToolHandlers() map[string]toolHandler {
	handlers := map[string]toolHandler{
		agenttools.ListDevices: func(ctx context.Context, _ json.RawMessage) (any, error) {
			return c.handleListDevices(ctx)
		},
		agenttools.GetDeviceStatus: func(ctx context.Context, input json.RawMessage) (any, error) {
			return c.handleGetDeviceStatus(ctx, input)
		},
		agenttools.RequestConnection: func(ctx context.Context, input json.RawMessage) (any, error) {
			return c.handleRequestConnection(ctx, input)
		},
		agenttools.RunCommand: func(ctx context.Context, input json.RawMessage) (any, error) {
			return c.handleRunCommand(ctx, input)
		},
		agenttools.GetSessionStatus: func(ctx context.Context, input json.RawMessage) (any, error) {
			return c.handleGetSessionStatus(ctx, input)
		},
		agenttools.ExecuteCommand: func(ctx context.Context, input json.RawMessage) (any, error) {
			return c.handleExecuteCommand(ctx, input)
		},
		agenttools.GetCommandResult: func(ctx context.Context, input json.RawMessage) (any, error) {
			return c.handleGetCommandResult(ctx, input)
		},
		agenttools.DownloadFile: func(ctx context.Context, input json.RawMessage) (any, error) {
			return c.handleDownloadFile(ctx, input)
		},
		agenttools.UploadFile: func(ctx context.Context, input json.RawMessage) (any, error) {
			return c.handleUploadFile(ctx, input)
		},
		agenttools.CancelCommand: func(ctx context.Context, input json.RawMessage) (any, error) {
			return c.handleCancelCommand(ctx, input)
		},
		agenttools.CloseSession: func(ctx context.Context, input json.RawMessage) (any, error) {
			return c.handleCloseSession(ctx, input)
		},
	}
	// 询问类工具要带上自己的名字，让 handleQuestion 知道该发哪种帧。
	for _, name := range []string{agenttools.AskUser, agenttools.GetUserReply} {
		toolName := name
		handlers[toolName] = func(ctx context.Context, input json.RawMessage) (any, error) {
			return c.handleQuestion(ctx, agenttools.Request{Name: toolName, Input: input})
		}
	}
	return handlers
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
