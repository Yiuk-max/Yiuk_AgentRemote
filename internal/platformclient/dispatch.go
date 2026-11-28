package platformclient

import (
	"agentremote/internal/platform/protocol"
)

// 本文件：把操作端连接的入站消息分发给等待它的请求。
//
// 重构前的做法是"谁在等就由谁读 incomingMessages"，并用一把 requestMu 把整趟
// 请求-响应串起来（TODO(薄弱点 #9)）。那样第二个工具调用必须等第一个超时。
// 现在连接只有一个读者（dispatchMessages），按 correlation_id 把响应投递到各自
// 的等待通道；没有等待者的帧只进缓存——异步事件本来就不属于某个请求。
//
// 顺序保证：cacheMessage 一定在投递之前执行，因此请求拿到响应时，
// 相关缓存（会话状态、命令输出、下载分块）已经是最新的，行为与重构前一致。

const pendingResponseBuffer = 1

// registerPending 为一次往返登记等待通道。必须在写出请求之前调用，
// 否则响应可能先到而被丢弃。
func (c *Client) registerPending(correlationID string) chan protocol.Message {
	responses := make(chan protocol.Message, pendingResponseBuffer)
	c.pendingMu.Lock()
	if c.pending == nil {
		c.pending = make(map[string]chan protocol.Message)
	}
	c.pending[correlationID] = responses
	c.pendingMu.Unlock()
	return responses
}

// unregisterPending 在请求结束时摘掉等待通道；只删自己那一个，
// 避免旧请求的清理误删复用了同一 ID 的新请求（ID 是随机的，这里只是防御）。
func (c *Client) unregisterPending(correlationID string, responses chan protocol.Message) {
	c.pendingMu.Lock()
	if current, ok := c.pending[correlationID]; ok && current == responses {
		delete(c.pending, correlationID)
	}
	c.pendingMu.Unlock()
}

// deliver 把一帧响应交给对应的等待者。没有等待者时直接丢弃：
// 该帧若是有用的事件，已经在 cacheMessage 里留下副本。
func (c *Client) deliver(message protocol.Message) {
	if message.CorrelationID == "" {
		return
	}
	c.pendingMu.Lock()
	responses, ok := c.pending[message.CorrelationID]
	c.pendingMu.Unlock()
	if !ok {
		return
	}
	// 每个 correlation_id 只应有一个终局响应；缓冲区满说明已经投递过，
	// 直接丢弃而不是阻塞分发协程（否则一个慢读者会拖住整条连接）。
	select {
	case responses <- message:
	default:
	}
}

// dispatchMessages 是连接唯一的读者：缓存所有帧，再按 correlation_id 分发。
//
// 连接关闭时先把队列里剩下的帧处理完再退出：终态响应（例如 command_result）
// 可能刚好排在关闭信号之后，丢掉它会让调用方误判为 PLATFORM_UNAVAILABLE。
func (c *Client) dispatchMessages(incoming <-chan protocol.Message, connectionClosed <-chan struct{}) {
	drain := func() {
		for {
			select {
			case message := <-incoming:
				c.cacheMessage(message)
				c.deliver(message)
			default:
				return
			}
		}
	}
	for {
		select {
		case message := <-incoming:
			c.cacheMessage(message)
			c.deliver(message)
		case <-connectionClosed:
			drain()
			return
		}
	}
}
