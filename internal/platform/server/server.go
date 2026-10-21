package server

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log/slog"
	"net"
	"sync"
	"time"

	"yiuk-switchboard/internal/platform/audit"
	"yiuk-switchboard/internal/platform/auth"
	"yiuk-switchboard/internal/platform/config"
	"yiuk-switchboard/internal/platform/device"
	"yiuk-switchboard/internal/platform/protocol"
	"yiuk-switchboard/internal/platform/session"
	"yiuk-switchboard/internal/platform/task"
	"yiuk-switchboard/internal/platform/transport"
)

type role int

const (
	roleAnonymous role = iota
	rolePlayer
	roleDevice
)

// Server is the in-memory platform relay. It has no database and deliberately
// loses all device, session and task state on restart.
type Server struct {
	cfg      config.Config
	log      *slog.Logger
	codec    protocol.Codec
	listener net.Listener

	devices  *device.Manager
	sessions *session.Manager
	tasks    *task.Manager
	auth     *auth.Authenticator
	audit    *audit.Logger

	mu        sync.Mutex
	conns     map[string]*conn
	closeOnce sync.Once
	done      chan struct{}
	wg        sync.WaitGroup
}

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

// New creates a platform server and binds its configured listener.
func New(cfg config.Config, log *slog.Logger) (*Server, error) {
	ln, err := transport.Listen(cfg.Listen, cfg.TLSCert, cfg.TLSKey, cfg.AllowInsecure)
	if err != nil {
		return nil, err
	}
	return NewWithListener(cfg, log, ln), nil
}

// NewWithListener creates a server around an already-bound listener. Tests use
// this to avoid TLS certificate setup; production should use New.
func NewWithListener(cfg config.Config, log *slog.Logger, ln net.Listener) *Server {
	if log == nil {
		log = slog.Default()
	}
	return &Server{
		cfg:      cfg,
		log:      log,
		codec:    protocol.NewCodec(cfg.MaxFrameBytes),
		listener: ln,
		devices:  device.NewManager(cfg.MaxDevices, cfg.TempPasswordTTL()),
		sessions: session.NewManager(cfg.MaxSessions, cfg.MaxSessionsPerDevice, cfg.SessionTimeout(), cfg.ApprovalTimeout()),
		tasks:    task.NewManager(cfg.MaxOutputBytes, cfg.MaxTasksPerSession),
		auth:     auth.New(cfg.PlayerPassword, cfg.AuthRatePerMin),
		audit:    audit.New(log),
		conns:    make(map[string]*conn),
		done:     make(chan struct{}),
	}
}

// Addr returns the listener address, which is useful in tests.
func (s *Server) Addr() net.Addr { return s.listener.Addr() }

// Run accepts connections until the context is cancelled or the listener
// fails. All connections and in-memory state are discarded on return.
func (s *Server) Run(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	runDone := make(chan struct{})
	defer close(runDone)
	go func() {
		select {
		case <-ctx.Done():
			s.Close()
		case <-runDone:
		}
	}()
	go s.janitor(ctx)

	for {
		nc, err := s.listener.Accept()
		if err != nil {
			s.Close()
			s.wg.Wait()
			if ctx.Err() != nil || s.isClosed() {
				return nil
			}
			return err
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.serveConn(nc)
		}()
	}
}

// Close stops the listener and closes every connection.
func (s *Server) Close() {
	s.closeOnce.Do(func() {
		close(s.done)
		_ = s.listener.Close()
		s.mu.Lock()
		conns := make([]*conn, 0, len(s.conns))
		for _, c := range s.conns {
			conns = append(conns, c)
		}
		s.mu.Unlock()
		for _, c := range conns {
			c.close("server_shutdown")
		}
	})
}

func (s *Server) isClosed() bool {
	select {
	case <-s.done:
		return true
	default:
		return false
	}
}

func (s *Server) serveConn(nc net.Conn) {
	if tcp, ok := nc.(*net.TCPConn); ok {
		_ = tcp.SetKeepAlive(true)
	}
	c := &conn{
		id:       newID(),
		nc:       nc,
		remote:   nc.RemoteAddr(),
		srv:      s,
		send:     make(chan protocol.Message, s.cfg.ObserverSlowClientBuf),
		done:     make(chan struct{}),
		lastSeen: time.Now(),
	}
	if !s.registerConn(c) {
		_ = nc.Close()
		return
	}
	defer s.unregisterConn(c)
	defer c.close("connection_closed")
	go c.writeLoop()
	s.readLoop(c)
}

func (s *Server) registerConn(c *conn) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.isClosed() || len(s.conns) >= s.cfg.MaxConnections {
		return false
	}
	s.conns[c.id] = c
	return true
}

func (s *Server) unregisterConn(c *conn) {
	s.mu.Lock()
	delete(s.conns, c.id)
	s.mu.Unlock()
	c.close("unregistered")

	if c.role == roleAnonymous {
		return
	}
	if c.role == roleDevice {
		s.cleanupDevice(c)
	}
	// A registered device connection may also have acted as an operator, so
	// clean up sessions owned by this connection in both roles.
	s.cleanupOperator(c)
}

func (s *Server) cleanupDevice(c *conn) {
	if c.deviceID == "" {
		return
	}
	_, owned := s.devices.SetOffline(c.deviceID, c.id)
	if !owned {
		return
	}
	s.audit.Event("device_disconnected", "device", audit.RedactID(c.deviceID), "connection", audit.RedactID(c.id))
	interrupted := s.sessions.InterruptByDevice(c.deviceID, "device_disconnected")
	for _, ses := range interrupted {
		s.devices.RemoveSession(ses.DeviceID, ses.ID)
		finished := s.tasks.FinishSession(ses.ID, task.StatusResultUnknown)
		s.notifySession(ses)
		for _, t := range finished {
			s.sendToConnection(ses.OperatorConnectionID, taskResultMessage(t))
		}
	}
}

func (s *Server) cleanupOperator(c *conn) {
	interrupted := s.sessions.InterruptByOperator(c.id, "operator_disconnected")
	for _, ses := range interrupted {
		s.devices.RemoveSession(ses.DeviceID, ses.ID)
		finished := s.tasks.FinishSession(ses.ID, task.StatusCancelled)
		s.notifySession(ses)
		if ses.DeviceConnectionID != "" {
			s.sendToConnection(ses.DeviceConnectionID, protocol.Message{
				Type:      protocol.TypeCloseSession,
				SessionID: ses.ID,
				DeviceID:  ses.DeviceID,
				Code:      "OPERATOR_DISCONNECTED",
				Message:   "operator disconnected",
			})
		}
		for _, t := range finished {
			s.sendToConnection(ses.DeviceConnectionID, taskResultMessage(t))
		}
	}
}

func (s *Server) readLoop(c *conn) {
	r := bufio.NewReaderSize(c.nc, s.cfg.MaxFrameBytes+4)
	deadline := s.cfg.HandshakeTimeout()
	handshake := true
	for {
		_ = c.nc.SetReadDeadline(time.Now().Add(deadline))
		m, err := s.codec.Read(r)
		if err != nil {
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

func (s *Server) send(c *conn, m protocol.Message) bool {
	if c == nil {
		return false
	}
	select {
	case <-c.done:
		return false
	default:
	}
	if m.Version == 0 {
		m.Version = protocol.Version
	}
	if m.BusinessType == "" {
		m.BusinessType = protocol.BusinessAgentAssist
	}
	select {
	case c.send <- m:
		return true
	case <-c.done:
		return false
	default:
		s.audit.Warn("slow_client_disconnected", "connection", audit.RedactID(c.id))
		c.close("slow_client")
		return false
	}
}

func (s *Server) lookupConn(id string) *conn {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.conns[id]
}

func (s *Server) sendToConnection(id string, m protocol.Message) bool {
	if id == "" {
		return false
	}
	return s.send(s.lookupConn(id), m)
}

func (s *Server) sendToDevice(deviceID string, m protocol.Message) bool {
	connID, ok := s.devices.OnlineConnection(deviceID)
	if !ok {
		return false
	}
	return s.sendToConnection(connID, m)
}

func (s *Server) notifySession(ses *session.Session) {
	if ses == nil {
		return
	}
	info := sessionInfo(ses)
	msg := protocol.Message{
		Type:      protocol.TypeSessionStatus,
		SessionID: ses.ID,
		DeviceID:  ses.DeviceID,
		Status:    string(ses.Status),
		Session:   &info,
	}
	seen := make(map[string]struct{}, 2)
	for _, id := range []string{ses.OperatorConnectionID, ses.DeviceConnectionID} {
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		s.sendToConnection(id, msg)
	}
}

func (s *Server) janitor(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.sweep()
		}
	}
}

func (s *Server) sweep() {
	expired := s.sessions.Expire()
	for _, ses := range expired {
		s.devices.RemoveSession(ses.DeviceID, ses.ID)
		finished := s.tasks.FinishSession(ses.ID, task.StatusCancelled)
		s.notifySession(ses)
		for _, t := range finished {
			s.sendToConnection(ses.OperatorConnectionID, taskResultMessage(t))
		}
	}
	for _, t := range s.tasks.Expire() {
		ses, ok := s.sessions.Get(t.SessionID)
		if !ok {
			continue
		}
		s.sendToConnection(ses.OperatorConnectionID, taskResultMessage(t))
		s.sendToConnection(ses.DeviceConnectionID, protocol.Message{
			Type:      protocol.TypeCancelCommand,
			SessionID: t.SessionID,
			RequestID: t.RequestID,
		})
		s.audit.Event("command_timed_out", "session", audit.RedactID(t.SessionID), "request", audit.RedactID(t.RequestID))
	}
	if removed := s.devices.Cleanup(time.Duration(s.cfg.OfflineDeviceKeepSec) * time.Second); removed > 0 {
		s.audit.Event("offline_devices_cleaned", "count", removed)
	}
}

func newID() string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		// crypto/rand failure is unrecoverable for identity generation.
		panic("platform: crypto/rand unavailable: " + err.Error())
	}
	return hex.EncodeToString(raw[:])
}

// protoError is a client-visible protocol error. Fatal errors close the
// connection after the error frame is written.
type protoError struct {
	code    string
	message string
	fatal   bool
}

func (e *protoError) Error() string { return e.code + ": " + e.message }

func perr(code, message string) error {
	return &protoError{code: code, message: message}
}

func ferr(code, message string) error {
	return &protoError{code: code, message: message, fatal: true}
}

// sendOK sends a direct response and echoes the caller's correlation id.
// Asynchronous events do not pass through this helper.
func (s *Server) sendOK(c *conn, req, resp protocol.Message) error {
	resp.CorrelationID = req.CorrelationID
	s.send(c, resp)
	return nil
}
