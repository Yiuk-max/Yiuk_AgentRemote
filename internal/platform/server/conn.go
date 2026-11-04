package server

// 连接在认证前使用握手超时，认证后使用心跳超时。
// 消息由接收协程分发，发送队列与致命错误响应共用 writeMu 串行写入。
import (
	"errors"
	"net"
	"sync"
	"time"

	"agentremote/internal/platform/audit"
	"agentremote/internal/platform/protocol"
	"agentremote/internal/platform/transport"
)

// clientConnection 保存一条客户端连接的身份、发送队列和关闭信号。
type clientConnection struct {
	id               string
	transportConn    transport.Conn
	remoteAddr       net.Addr
	server           *Server
	outgoingMessages chan protocol.Message
	closed           chan struct{}
	closeOnce        sync.Once
	writeMu          sync.Mutex

	role         role
	businessType string
	deviceID     string
	displayName  string
	lastSeen     time.Time
}

// readAndDispatchMessages 读取并分发协议消息，必要时回复错误或关闭连接。
func (s *Server) readAndDispatchMessages(clientConn *clientConnection) {
	readTimeout := s.config.HandshakeTimeout()
	awaitingAuthentication := true
	for {
		_ = clientConn.transportConn.SetReadDeadline(time.Now().Add(readTimeout))
		data, err := clientConn.transportConn.ReadMessage()
		if err != nil {
			if awaitingAuthentication {
				s.logger.Warn("platform WebSocket/protocol handshake failed", "remote", clientConn.remoteAddress(), "error", err)
			} else if !errors.Is(err, net.ErrClosed) {
				s.logger.Debug("platform connection read ended", "remote", clientConn.remoteAddress(), "error", err)
			}
			return
		}

		message, err := s.codec.Decode(data)
		if err != nil {
			s.logger.Debug("platform message decode failed", "connection", audit.RedactID(clientConn.id), "error", err)
			return
		}
		clientConn.lastSeen = time.Now()

		if err := s.dispatchMessage(clientConn, message); err != nil {
			var protocolErr *protocolError
			if errors.As(err, &protocolErr) {
				errorMessage := protocol.ErrorMessage(protocolErr.code, protocolErr.message)
				errorMessage.CorrelationID = message.CorrelationID
				if protocolErr.fatal {
					_ = clientConn.writeProtocolMessage(errorMessage)
					return
				}
				s.enqueueMessage(clientConn, errorMessage)
			} else {
				s.logger.Error("platform request failed", "connection", audit.RedactID(clientConn.id), "error", err)
				errorMessage := protocol.ErrorMessage("INTERNAL", "request failed")
				errorMessage.CorrelationID = message.CorrelationID
				_ = clientConn.writeProtocolMessage(errorMessage)
				return
			}
		}
		if awaitingAuthentication && clientConn.role != roleAnonymous {
			awaitingAuthentication = false
			readTimeout = s.config.HeartbeatTimeout()
		}
	}
}

// writeQueuedMessages 持续发送排队消息，写入失败或连接关闭时退出。
func (clientConn *clientConnection) writeQueuedMessages() {
	defer clientConn.close("write_loop")
	for {
		select {
		case message := <-clientConn.outgoingMessages: //有消息可发送，取出消息
			if err := clientConn.writeProtocolMessage(message); err != nil {
				return
			}
		case <-clientConn.closed:
			return
		}
	}
}

// writeProtocolMessage 会将来自 writer 协程和致命错误路径的写操作序列化，这样协议帧就不会交错
func (clientConn *clientConnection) writeProtocolMessage(message protocol.Message) error {
	clientConn.writeMu.Lock()
	defer clientConn.writeMu.Unlock()
	_ = clientConn.transportConn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	data, err := clientConn.server.codec.Encode(message)
	if err != nil {
		return err
	}
	return clientConn.transportConn.WriteMessage(data)
}

func (clientConn *clientConnection) close(reason string) {
	clientConn.closeOnce.Do(func() {
		close(clientConn.closed)
		_ = clientConn.transportConn.Close()
	})
}

func (clientConn *clientConnection) remoteAddress() string {
	if clientConn.remoteAddr == nil {
		return ""
	}
	return clientConn.remoteAddr.String()
}
