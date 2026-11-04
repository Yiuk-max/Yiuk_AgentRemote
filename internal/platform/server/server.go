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
	config   config.Config
	logger   *slog.Logger
	codec    protocol.Codec // JSON WebSocket 消息编解码器
	listener *transport.WebSocketListener

	devices   *device.Manager
	sessions  *session.Manager
	tasks     *task.Manager
	downloads *transfer.Manager // 下载传输状态；不保存文件内容
	uploads   *transfer.Manager // 上传管理器
	auth      *auth.Authenticator
	audit     *audit.Logger //用于记录事件和警告的审计日志记录器

	connectionsMu sync.Mutex
	connections   map[string]*clientConnection // 连接映射，键是连接ID，值是连接对象，关闭时从这里清理连接
	closeOnce     sync.Once                    //确保关闭逻辑只执行一次
	shutdown      chan struct{}                //通知 Goroutine 退出
	connectionWG  sync.WaitGroup
}

// New 创建平台服务器并绑定配置中的监听地址。
func New(serverConfig config.Config, logger *slog.Logger) (*Server, error) {
	listener, err := transport.ListenWebSocket(serverConfig.Listen, serverConfig.TLSCert, serverConfig.TLSKey, serverConfig.AllowInsecure, serverConfig.MaxFrameBytes)
	if err != nil {
		return nil, err
	}
	return NewWithListener(serverConfig, logger, listener), nil
}

// NewWithListener 使用已有监听器创建平台服务器，供测试注入监听器。
// 生产环境应使用 New。
func NewWithListener(serverConfig config.Config, logger *slog.Logger, listener *transport.WebSocketListener) *Server {
	if logger == nil {
		logger = slog.Default()
	}
	return &Server{
		config:      serverConfig,
		logger:      logger,
		codec:       protocol.NewCodec(serverConfig.MaxFrameBytes),
		listener:    listener,
		devices:     device.NewManager(serverConfig.MaxDevices, serverConfig.TempPasswordTTL()),
		sessions:    session.NewManager(serverConfig.MaxSessions, serverConfig.MaxSessionsPerDevice, serverConfig.SessionTimeout(), serverConfig.ApprovalTimeout()),
		tasks:       task.NewManager(serverConfig.MaxOutputBytes, serverConfig.MaxTasksPerSession),
		downloads:   transfer.NewManager(serverConfig.MaxTasksPerSession),
		uploads:     transfer.NewManager(serverConfig.MaxTasksPerSession),
		auth:        auth.New(serverConfig.PlayerPassword, serverConfig.AuthRatePerMin),
		audit:       audit.New(logger),
		connections: make(map[string]*clientConnection),
		shutdown:    make(chan struct{}),
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
	go s.runExpirationCleanup(ctx)

	for {
		transportConn, err := s.listener.Accept()
		if err != nil {
			s.Close()
			s.connectionWG.Wait()
			if ctx.Err() != nil || s.isClosed() {
				return nil
			}
			return err
		}
		s.connectionWG.Add(1)
		go func() {
			defer s.connectionWG.Done()
			s.serveConnection(transportConn)
		}()
	}
}

// 关闭监听，不再接受新连接，并清理所有连接
func (s *Server) Close() {
	s.closeOnce.Do(func() {
		close(s.shutdown)
		_ = s.listener.Close()
		s.connectionsMu.Lock()
		openConnections := make([]*clientConnection, 0, len(s.connections))
		for _, clientConn := range s.connections {
			openConnections = append(openConnections, clientConn)
		}
		s.connectionsMu.Unlock()
		for _, clientConn := range openConnections {
			clientConn.close("server_shutdown")
		}
	})
}

func (s *Server) isClosed() bool {
	select {
	case <-s.shutdown:
		return true
	default:
		return false
	}
}

// 处理单个连接的生命周期，包括读取和写入循环。每个连接在单独的 goroutine 中处理。
func (s *Server) serveConnection(transportConn transport.Conn) {
	clientConn := &clientConnection{
		id:               newID(),
		transportConn:    transportConn,
		remoteAddr:       transportConn.RemoteAddr(),
		server:           s,
		outgoingMessages: make(chan protocol.Message, s.config.ObserverSlowClientBuf),
		closed:           make(chan struct{}),
		lastSeen:         time.Now(),
	}
	if !s.addConnection(clientConn) {
		_ = transportConn.Close()
		return
	}
	defer s.removeConnectionAndEndSessions(clientConn)
	defer clientConn.close("connection_closed")
	go clientConn.writeQueuedMessages()
	s.readAndDispatchMessages(clientConn)
}

// addConnection 注册连接；服务器已关闭或连接数达到上限时返回 false。
func (s *Server) addConnection(clientConn *clientConnection) bool {
	s.connectionsMu.Lock()
	defer s.connectionsMu.Unlock()
	if s.isClosed() || len(s.connections) >= s.config.MaxConnections {
		return false
	}
	s.connections[clientConn.id] = clientConn
	return true
}

// removeConnectionAndEndSessions 移除并关闭连接，收敛其设备与操作者角色关联的会话。
func (s *Server) removeConnectionAndEndSessions(clientConn *clientConnection) {
	s.connectionsMu.Lock()
	delete(s.connections, clientConn.id)
	s.connectionsMu.Unlock()
	clientConn.close("unregistered")

	if clientConn.role == roleAnonymous {
		// Useful when a client fails before authenticating (often a TLS
		// handshake or wrong-port issue rather than a password problem).
		s.audit.Warn("connection_closed_unauthenticated", "remote", clientConn.remoteAddress())
		return
	}
	if clientConn.role == roleDevice {
		s.interruptDeviceSessionsOnDisconnect(clientConn)
	}
	// A registered device connection may also have acted as an operator, so
	// clean up sessions owned by this connection in both roles.
	s.interruptOperatorSessionsOnDisconnect(clientConn)
}

// interruptDeviceSessionsOnDisconnect 将设备标记为离线，中断其会话并上报未知结果。
func (s *Server) interruptDeviceSessionsOnDisconnect(clientConn *clientConnection) {
	if clientConn.deviceID == "" {
		return
	}
	_, wasOnlineConnectionOwner := s.devices.SetOffline(clientConn.deviceID, clientConn.id)
	if !wasOnlineConnectionOwner {
		return
	}
	s.audit.Event("device_disconnected", "device", audit.RedactID(clientConn.deviceID), "connection", audit.RedactID(clientConn.id))
	interruptedSessions := s.sessions.InterruptByDevice(clientConn.deviceID, "device_disconnected")
	for _, sessionRecord := range interruptedSessions {
		s.devices.RemoveSession(sessionRecord.DeviceID, sessionRecord.ID)
		finishedTasks := s.tasks.FinishSession(sessionRecord.ID, task.StatusResultUnknown)
		s.notifySessionStatus(sessionRecord)
		for _, commandTask := range finishedTasks {
			s.sendToConnection(sessionRecord.OperatorConnectionID, commandResultMessage(commandTask))
		}
		for _, resultMessage := range s.finishSessionTransfers(sessionRecord.ID, transfer.StatusResultUnknown) {
			s.sendToConnection(sessionRecord.OperatorConnectionID, resultMessage)
		}
	}
}

// interruptOperatorSessionsOnDisconnect 中断该操作者的会话，取消任务并通知设备停止执行。
func (s *Server) interruptOperatorSessionsOnDisconnect(clientConn *clientConnection) {
	interruptedSessions := s.sessions.InterruptByOperator(clientConn.id, "operator_disconnected")
	for _, sessionRecord := range interruptedSessions {
		s.devices.RemoveSession(sessionRecord.DeviceID, sessionRecord.ID)
		finishedTasks := s.tasks.FinishSession(sessionRecord.ID, task.StatusCancelled)
		for _, resultMessage := range s.finishSessionTransfers(sessionRecord.ID, transfer.StatusCancelled) {
			s.sendToConnection(sessionRecord.OperatorConnectionID, resultMessage)
		}
		s.notifySessionStatus(sessionRecord)
		if sessionRecord.DeviceConnectionID != "" {
			s.sendToConnection(sessionRecord.DeviceConnectionID, protocol.Message{
				Type:      protocol.TypeCloseSession,
				SessionID: sessionRecord.ID,
				DeviceID:  sessionRecord.DeviceID,
				Code:      "OPERATOR_DISCONNECTED",
				Message:   "operator disconnected",
			})
		}
		for _, commandTask := range finishedTasks {
			s.sendToConnection(sessionRecord.OperatorConnectionID, commandResultMessage(commandTask))
		}
	}
}

func newID() string {
	var randomBytes [16]byte
	if _, err := rand.Read(randomBytes[:]); err != nil {
		// crypto/rand failure is unrecoverable for identity generation.
		panic("platform: crypto/rand unavailable: " + err.Error())
	}
	return hex.EncodeToString(randomBytes[:])
}
