package gateway

// 连接在认证前使用握手超时，认证后使用心跳超时。
// 消息由接收协程交给业务层，发送队列与致命错误响应共用 writeMu 串行写入。
import (
	"errors"
	"net"
	"sync"
	"time"

	"agentremote/internal/platform/core"
	"agentremote/internal/platform/transport"
)

// errQueueFull 表示发送队列已满，调用方应断开慢客户端。
var errQueueFull = errors.New("gateway: send queue is full")

// clientConnection 保存一条连接的身份、发送队列和关闭信号。
// 身份字段（core.Caller）会随业务处理更新，例如认证后 role 变成 player。
type clientConnection struct {
	caller         core.Caller //caller 保存连接身份信息，包括 connection_id、role、device_id 等
	transportConn  transport.Conn
	outgoingFrames chan []byte // outgoingFrames 是发送队列，业务层发来的帧先放这里排队，写协程再写回 socket
	closed         chan struct{}
	closeOnce      sync.Once // closeOnce 确保关闭逻辑只执行一次
	writeMu        sync.Mutex
}

// readAndDispatchFrames 读取客户端帧并交给业务层，业务层要求关闭时退出。
// 同一连接一次只有一个帧在处理，所以单连接天然串行化
func (s *Server) readAndDispatchFrames(clientConn *clientConnection) {
	readTimeout := s.config.HandshakeTimeout()
	awaitingAuthentication := true
	for {
		_ = clientConn.transportConn.SetReadDeadline(time.Now().Add(readTimeout))
		frame, err := clientConn.transportConn.ReadMessage() // 读取一帧
		if err != nil {
			if awaitingAuthentication {
				s.logger.Warn("platform WebSocket handshake failed", "remote", clientConn.caller.RemoteAddr, "error", err)
			} else if !errors.Is(err, net.ErrClosed) {
				s.logger.Debug("platform connection read ended", "remote", clientConn.caller.RemoteAddr, "error", err)
			}
			return
		}
		if s.edge == nil {
			return
		}
		// 交给业务层处理，业务层返回 *core.FatalError 时关闭连接
		if err := s.edge.Handle(&clientConn.caller, frame); err != nil {
			var fatalError *core.FatalError
			if errors.As(err, &fatalError) && len(fatalError.Frame) > 0 {
				// 致命错误的响应帧不排队，直接写出去再关连接。
				_ = clientConn.writeFrame(fatalError.Frame)
			}
			return
		}
		if awaitingAuthentication && clientConn.caller.Role != core.RoleAnonymous {
			awaitingAuthentication = false
			readTimeout = s.config.HeartbeatTimeout()
		}
	}
}

// writeQueuedFrames 持续发送排队帧，写入失败或连接关闭时退出。
func (clientConn *clientConnection) writeQueuedFrames() {
	defer clientConn.close()
	for {
		select {
		case frame := <-clientConn.outgoingFrames:
			if err := clientConn.writeFrame(frame); err != nil {
				return
			}
		case <-clientConn.closed:
			return
		}
	}
}

// writeFrame 将 writer 协程与致命错误路径的写操作序列化，避免协议帧交错。
func (clientConn *clientConnection) writeFrame(frame []byte) error {
	clientConn.writeMu.Lock()
	defer clientConn.writeMu.Unlock()
	_ = clientConn.transportConn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	return clientConn.transportConn.WriteMessage(frame)
}

// enqueue 尝试把帧放进发送队列；队列满返回 errQueueFull，连接已关闭返回 core.ErrNoRoute。
func (clientConn *clientConnection) enqueue(frame []byte) error {
	select {
	case <-clientConn.closed:
		return core.ErrNoRoute
	default:
	}
	select {
	case clientConn.outgoingFrames <- frame:
		return nil
	case <-clientConn.closed:
		return core.ErrNoRoute
	default:
		return errQueueFull
	}
}

func (clientConn *clientConnection) close() {
	clientConn.closeOnce.Do(func() {
		close(clientConn.closed)
		_ = clientConn.transportConn.Close()
	})
}
