package server

//conn 结构 + readLoop（握手期用 handshake_timeout，
//认证后切 heartbeat_timeout 作读 deadline）+ writeLoop + writeMu 保证帧不交错
import (
	"bufio"
	"errors"
	"net"
	"sync"
	"time"

	"agentremote/internal/platform/audit"
	"agentremote/internal/platform/protocol"
)

type conn struct {
	id        string
	nc        net.Conn
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
	r := bufio.NewReaderSize(c.nc, s.cfg.MaxFrameBytes+4)
	deadline := s.cfg.HandshakeTimeout()
	handshake := true
	for {
		_ = c.nc.SetReadDeadline(time.Now().Add(deadline))
		m, err := s.codec.Read(r) //读一帧
		if err != nil {
			if handshake {
				s.log.Warn("platform TLS/protocol handshake failed", "remote", c.remoteString(), "error", err)
			} else if !errors.Is(err, net.ErrClosed) {
				s.log.Debug("platform connection read ended", "remote", c.remoteString(), "error", err)
			}
			return
		}
		c.lastSeen = time.Now() //更新上次活跃时间点
		//handle开始处理业务逻辑
		if err := s.handle(c, m); err != nil {
			// 出问题，返回报错信息
			var pe *protoError
			if errors.As(err, &pe) {
				errMsg := protocol.ErrorMessage(pe.code, pe.message)
				errMsg.CorrelationID = m.CorrelationID
				// 致命错误是同步写入的，因此对等方在连接被拆除之前可以观察到错误代码
				if pe.fatal {
					_ = c.writeRaw(errMsg)
					return
				}
				s.send(c, errMsg)
			} else { //没成功开始处理消息，直接返回报错
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
	return c.srv.codec.Write(c.nc, m)
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
