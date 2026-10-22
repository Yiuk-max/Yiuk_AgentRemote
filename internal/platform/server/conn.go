package server

import (
	"bufio"
	"errors"
	"net"
	"sync"
	"time"

	"yiuk-switchboard/internal/platform/audit"
	"yiuk-switchboard/internal/platform/protocol"
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
		m, err := s.codec.Read(r)
		if err != nil {
			if handshake {
				s.log.Warn("platform TLS/protocol handshake failed", "remote", c.remoteString(), "error", err)
			} else if !errors.Is(err, net.ErrClosed) {
				s.log.Debug("platform connection read ended", "remote", c.remoteString(), "error", err)
			}
			return
		}
		c.lastSeen = time.Now()
		if err := s.handle(c, m); err != nil {
			var pe *protoError
			if errors.As(err, &pe) {
				errMsg := protocol.ErrorMessage(pe.code, pe.message)
				errMsg.CorrelationID = m.CorrelationID
				// Fatal errors are written synchronously so the peer observes the
				// error code before the connection is torn down.
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
		case m := <-c.send:
			if err := c.writeRaw(m); err != nil {
				return
			}
		case <-c.done:
			return
		}
	}
}

// writeRaw serializes writes from the writer goroutine and the fatal-error
// path so protocol frames cannot interleave.
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
