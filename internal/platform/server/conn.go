package server

// conn 结构 + readLoop（WebSocket/协议握手期用 handshake_timeout，
//认证后切 heartbeat_timeout 作读 deadline）+ writeLoop + writeMu 保证帧不交错
import (
	"errors"
	"net"
	"sync"
	"time"

	"agentremote/internal/platform/audit"
	"agentremote/internal/platform/protocol"
	"agentremote/internal/platform/transport"
)

type conn struct {
	id        string
	nc        transport.Conn
	remote    net.Addr
	srv       *Server
	send      chan protocol.Message
	done      chan struct{}
	closeOnce sync.Once
	writeMu   sync.Mutex

	role     role
	business string
	deviceID string
	label    string
	lastSeen time.Time
}

func (s *Server) readLoop(c *conn) {
	deadline := s.cfg.HandshakeTimeout()
	handshake := true
	for {
		_ = c.nc.SetReadDeadline(time.Now().Add(deadline))
		data, err := c.nc.ReadMessage()
		if err != nil {
			if handshake {
				s.log.Warn("platform WebSocket/protocol handshake failed", "remote", c.remoteString(), "error", err)
			} else if !errors.Is(err, net.ErrClosed) {
				s.log.Debug("platform connection read ended", "remote", c.remoteString(), "error", err)
			}
			return
		}

		m, err := s.codec.Decode(data)
		if err != nil {
			s.log.Debug("platform message decode failed", "connection", audit.RedactID(c.id), "error", err)
			return
		}
		c.lastSeen = time.Now()

		if err := s.handle(c, m); err != nil {
			var pe *protoError
			if errors.As(err, &pe) {
				errMsg := protocol.ErrorMessage(pe.code, pe.message)
				errMsg.CorrelationID = m.CorrelationID
				if pe.fatal {
					_ = c.writeRaw(errMsg)
					return
				}
				s.send(c, errMsg)
			} else {
				s.log.Error("platform request failed", "connection", audit.RedactID(c.id), "error", err)
				errMsg := protocol.ErrorMessage("INTERNAL", "request failed")
				errMsg.CorrelationID = m.CorrelationID
				_ = c.writeRaw(errMsg)
				return
			}
		}
		if handshake && c.role != roleAnonymous {
			handshake = false
			deadline = s.cfg.HeartbeatTimeout()
		}
	}
}

func (c *conn) writeLoop() {
	defer c.close("write_loop")
	for {
		select {
		case m := <-c.send: //有消息可发送，取出消息
			if err := c.writeRaw(m); err != nil {
				return
			}
		case <-c.done:
			return
		}
	}
}

// writeRaw 会将来自 writer 协程和致命错误路径的写操作序列化，这样协议帧就不会交错
func (c *conn) writeRaw(m protocol.Message) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	_ = c.nc.SetWriteDeadline(time.Now().Add(10 * time.Second))
	data, err := c.srv.codec.Encode(m)
	if err != nil {
		return err
	}
	return c.nc.WriteMessage(data)
}

func (c *conn) close(reason string) {
	c.closeOnce.Do(func() {
		close(c.done)
		_ = c.nc.Close()
	})
}

func (c *conn) remoteString() string {
	if c.remote == nil {
		return ""
	}
	return c.remote.String()
}
