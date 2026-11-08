// Package gateway 负责网络接入：WSS 监听、连接表、协议帧透传、心跳与超时。
//
// 它不理解任何业务语义：从客户端读到的字节原样交给业务层，
// 业务层要发的字节原样写回 socket。连接身份（connection_id / role / device_id）
// 保存在这里的连接对象上，每次交给业务层时带上、返回时取回更新后的值。
package gateway

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net"
	"sync"

	"agentremote/internal/platform/audit"
	"agentremote/internal/platform/config"
	"agentremote/internal/platform/core"
	"agentremote/internal/platform/transport"
)

// Edge 是网关依赖的业务层。单进程时由 core.Service 直接实现，
// 两个进程时由 gRPC 客户端实现，网关代码不需要区分。
type Edge interface {
	// Handle 处理一帧，返回 *core.FatalError 表示这一帧之后要关闭连接。
	Handle(caller *core.Caller, frame []byte) error
	// Detach 在连接关闭后通知业务层收敛它名下的会话。
	Detach(caller *core.Caller)
}

// Server 持有本节点的连接表，是“这条连接还活着吗”的唯一依据。
type Server struct {
	config   config.Config
	logger   *slog.Logger                 // 日志记录器
	listener *transport.WebSocketListener // 监听器，接受新连接
	edge     Edge                         //接口，需要实现的两个核心函数

	connectionsMu sync.Mutex
	connections   map[string]*clientConnection // 键是连接 ID，关闭时从这里清理
	closeOnce     sync.Once
	shutdown      chan struct{} // 通知 goroutine 退出
	connectionWG  sync.WaitGroup
}

// New 创建网关并绑定配置中的监听地址。
func New(serverConfig config.Config, logger *slog.Logger) (*Server, error) {
	listener, err := transport.ListenWebSocket(serverConfig.Listen, serverConfig.TLSCert, serverConfig.TLSKey, serverConfig.AllowInsecure, serverConfig.MaxFrameBytes)
	if err != nil {
		return nil, err
	}
	return NewWithListener(serverConfig, logger, listener), nil
}

// NewWithListener 使用已有监听器创建网关，供单进程加载与测试注入监听器。
func NewWithListener(serverConfig config.Config, logger *slog.Logger, listener *transport.WebSocketListener) *Server {
	if logger == nil {
		logger = slog.Default()
	}
	return &Server{
		config:      serverConfig,
		logger:      logger,
		listener:    listener,
		connections: make(map[string]*clientConnection),
		shutdown:    make(chan struct{}),
	}
}

// Bind 绑定业务层。必须在 Run 之前调用。
func (s *Server) Bind(edge Edge) { s.edge = edge }

// Addr 返回监听地址，测试用。
func (s *Server) Addr() net.Addr { return s.listener.Addr() }

// Run 循环接受连接，直到上下文被取消或监听器失败。
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

// Close 关闭监听，不再接受新连接，并清理所有连接。
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
			clientConn.close()
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

// serveConnection 处理单个连接的生命周期，包括读取和写入循环。
func (s *Server) serveConnection(transportConn transport.Conn) {
	clientConn := &clientConnection{
		caller: core.Caller{
			ConnectionID: newID(),
			RemoteAddr:   transportConn.RemoteAddr().String(),
		},
		transportConn:  transportConn,
		outgoingFrames: make(chan []byte, s.config.ObserverSlowClientBuf),
		closed:         make(chan struct{}),
	}
	if !s.addConnection(clientConn) { // 把新连接注册到连接表，注册失败说明服务器已关闭或连接数达到上限
		_ = transportConn.Close()
		return
	}
	defer s.removeConnection(clientConn)
	go clientConn.writeQueuedFrames()   // 启动写循环，负责把业务层发来的帧写回 socket
	s.readAndDispatchFrames(clientConn) // 启动读循环。复杂把网关接受的消息发给业务层处理，业务层返回 *core.FatalError 时关闭连接
}

// addConnection 注册连接；服务器已关闭或连接数达到上限时返回 false。
func (s *Server) addConnection(clientConn *clientConnection) bool {
	s.connectionsMu.Lock()
	defer s.connectionsMu.Unlock()
	if s.isClosed() || len(s.connections) >= s.config.MaxConnections {
		return false
	}
	s.connections[clientConn.caller.ConnectionID] = clientConn
	return true
}

// removeConnection 移除并关闭连接，然后通知业务层收敛该连接名下的会话。
func (s *Server) removeConnection(clientConn *clientConnection) {
	s.connectionsMu.Lock()
	delete(s.connections, clientConn.caller.ConnectionID)
	s.connectionsMu.Unlock()
	clientConn.close()
	if s.edge != nil {
		s.edge.Detach(&clientConn.caller)
	}
}

// connectionByID 返回连接表中指定 ID 的连接，找不到返回 nil。
func (s *Server) connectionByID(connectionID string) *clientConnection {
	s.connectionsMu.Lock()
	defer s.connectionsMu.Unlock()
	return s.connections[connectionID]
}

// Deliver 把业务层编好码的一帧写进目标连接的发送队列。
// 目标连接不在本节点时返回 core.ErrNoRoute，队列满时断开该连接。
// core通过gRPC调用gateway的Deliver方法，把出站帧投递到目标连接的发送队列，保证同一连接的帧按顺序投递
func (s *Server) Deliver(connectionID string, frame []byte) error {
	if connectionID == "" {
		return core.ErrNoRoute
	}
	clientConn := s.connectionByID(connectionID) //找到目标连接
	if clientConn == nil {
		return core.ErrNoRoute
	}
	switch err := clientConn.enqueue(frame); err { //把帧写进目标连接的发送队列，队列满时断开该连接
	case nil:
		return nil
	case errQueueFull:
		s.logger.Warn("slow_client_disconnected", "connection", audit.RedactID(connectionID))
		clientConn.close()
		return err
	default:
		return err
	}
}

// Alive 报告连接当前是否还挂在本节点。
func (s *Server) Alive(connectionID string) bool {
	return s.connectionByID(connectionID) != nil
}

func newID() string {
	var randomBytes [16]byte
	if _, err := rand.Read(randomBytes[:]); err != nil {
		// crypto/rand failure is unrecoverable for identity generation.
		panic("gateway: crypto/rand unavailable: " + err.Error())
	}
	return hex.EncodeToString(randomBytes[:])
}
