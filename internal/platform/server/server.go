package server

import (
	"context"
	"net"
	"sync"
	"time"

	"agentremote/internal/platform/audit"
	"agentremote/internal/platform/auth"
	"agentremote/internal/platform/config"
	"agentremote/internal/platform/device"
	"agentremote/internal/platform/protocol"
	"agentremote/internal/platform/session"
	"agentremote/internal/platform/task"
	"agentremote/internal/platform/transfer"
	"agentremote/internal/platform/transport"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
)

type role int

const (
	roleAnonymous role = iota
	rolePlayer
	roleDevice
)

// 服务器是纯内存的中继平台。它没有数据库，并且故意在重新启动时丢弃所有状态
type Server struct {
	cfg      config.Config
	log      *slog.Logger
	codec    protocol.Codec // JSON WebSocket 消息编解码器
	listener *transport.WebSocketListener

	devices   *device.Manager
	sessions  *session.Manager
	tasks     *task.Manager
	transfers *transfer.Manager // 传输管理器
	uploads   *transfer.Manager // 上传管理器
	auth      *auth.Authenticator
	audit     *audit.Logger //用于记录事件和警告的审计日志记录器

	mu        sync.Mutex
	conns     map[string]*conn // 连接映射，键是连接ID，值是连接对象，关闭时从这里清理连接
	closeOnce sync.Once        //确保关闭逻辑只执行一次
	done      chan struct{}    //通知 Goroutine 退出
	wg        sync.WaitGroup
}

// 新建一个平台服务器并绑定其配置的lintener。
func New(cfg config.Config, log *slog.Logger) (*Server, error) {
	ln, err := transport.ListenWebSocket(cfg.Listen, cfg.TLSCert, cfg.TLSKey, cfg.AllowInsecure, cfg.MaxFrameBytes)
	if err != nil {
		return nil, err
	}
	return NewWithListener(cfg, log, ln), nil
}

// 创建一个新的平台服务器并绑定其配置的侦听器，测试
// 这个用于避免 TLS 证书设置；生产环境应使用 New。
func NewWithListener(cfg config.Config, log *slog.Logger, ln *transport.WebSocketListener) *Server {
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
		uploads:   transfer.NewManager(cfg.MaxTasksPerSession),
		auth:      auth.New(cfg.PlayerPassword, cfg.AuthRatePerMin),
		audit:     audit.New(log),
		conns:     make(map[string]*conn),
		done:      make(chan struct{}),
	}
}

// 返回监听地址，测试用
func (s *Server) Addr() net.Addr { return s.listener.Addr() }

// Run 循环接受连接，直到上下文被取消或监听器失败。所有连接和内存中的状态在返回时都会被丢弃。
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

// 关闭监听，不再接受新连接，并清理所有连接
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

// 处理单个连接的生命周期，包括读取和写入循环。每个连接在单独的 goroutine 中处理。
func (s *Server) serveConn(nc transport.Conn) {
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

// 注册到map中
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

// 清理设备连接
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
		for _, msg := range s.sessionTransferResults(ses.ID, transfer.StatusResultUnknown) {
			s.sendToConnection(ses.OperatorConnectionID, msg)
		}
	}
}

func (s *Server) cleanupOperator(c *conn) {
	interrupted := s.sessions.InterruptByOperator(c.id, "operator_disconnected")
	for _, ses := range interrupted {
		s.devices.RemoveSession(ses.DeviceID, ses.ID)
		finished := s.tasks.FinishSession(ses.ID, task.StatusCancelled)
		for _, msg := range s.sessionTransferResults(ses.ID, transfer.StatusCancelled) {
			s.sendToConnection(ses.OperatorConnectionID, msg)
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
