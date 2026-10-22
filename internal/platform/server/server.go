package server

import (
	"context"
	"net"
	"sync"
	"time"

	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"yiuk-switchboard/internal/platform/audit"
	"yiuk-switchboard/internal/platform/auth"
	"yiuk-switchboard/internal/platform/config"
	"yiuk-switchboard/internal/platform/device"
	"yiuk-switchboard/internal/platform/protocol"
	"yiuk-switchboard/internal/platform/session"
	"yiuk-switchboard/internal/platform/task"
	"yiuk-switchboard/internal/platform/transfer"
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

	devices   *device.Manager
	sessions  *session.Manager
	tasks     *task.Manager
	transfers *transfer.Manager
	auth      *auth.Authenticator
	audit     *audit.Logger

	mu        sync.Mutex
	conns     map[string]*conn
	closeOnce sync.Once
	done      chan struct{}
	wg        sync.WaitGroup
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
		cfg:       cfg,
		log:       log,
		codec:     protocol.NewCodec(cfg.MaxFrameBytes),
		listener:  ln,
		devices:   device.NewManager(cfg.MaxDevices, cfg.TempPasswordTTL()),
		sessions:  session.NewManager(cfg.MaxSessions, cfg.MaxSessionsPerDevice, cfg.SessionTimeout(), cfg.ApprovalTimeout()),
		tasks:     task.NewManager(cfg.MaxOutputBytes, cfg.MaxTasksPerSession),
		transfers: transfer.NewManager(cfg.MaxTasksPerSession),
		auth:      auth.New(cfg.PlayerPassword, cfg.AuthRatePerMin),
		audit:     audit.New(log),
		conns:     make(map[string]*conn),
		done:      make(chan struct{}),
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
		// Useful when a client fails before authenticating (often a TLS
		// handshake or wrong-port issue rather than a password problem).
		s.audit.Warn("connection_closed_unauthenticated", "remote", c.remoteString())
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
		for _, t := range s.transfers.FinishSession(ses.ID, transfer.StatusResultUnknown) {
			s.sendToConnection(ses.OperatorConnectionID, transferResultMessage(t))
		}
	}
}

func (s *Server) cleanupOperator(c *conn) {
	interrupted := s.sessions.InterruptByOperator(c.id, "operator_disconnected")
	for _, ses := range interrupted {
		s.devices.RemoveSession(ses.DeviceID, ses.ID)
		finished := s.tasks.FinishSession(ses.ID, task.StatusCancelled)
		for _, t := range s.transfers.FinishSession(ses.ID, transfer.StatusCancelled) {
			s.sendToConnection(ses.OperatorConnectionID, transferResultMessage(t))
		}
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
			s.sendToConnection(ses.OperatorConnectionID, taskResultMessage(t))
		}
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
