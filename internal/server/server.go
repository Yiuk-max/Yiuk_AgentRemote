package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"yiuk-switchboard/internal/config"
	"yiuk-switchboard/internal/connlimit"
	"yiuk-switchboard/internal/control"
	"yiuk-switchboard/internal/room"
)

type Server struct {
	cfg              config.Config
	listener         net.Listener
	rooms            *room.Manager
	handler          *control.Handler
	connectionBudget *connlimit.Budget
	mu               sync.Mutex
	conns            map[net.Conn]struct{}
	lastLimitLog     time.Time
	closed           bool
	shutdownOnce     sync.Once
	shutdownDone     chan struct{}
}

func New(cfg config.Config) (*Server, error) {
	ln, err := net.Listen("tcp", fmt.Sprintf("0.0.0.0:%d", cfg.ControlPort))
	if err != nil {
		return nil, fmt.Errorf("listen control port: %w", err)
	}
	budget := connlimit.New(cfg.MaxConnections)
	rm := room.NewManager(room.Options{PortMin: cfg.RoomPortMin, PortMax: cfg.RoomPortMax, MaxRooms: cfg.MaxRooms, MaxPlayers: cfg.MaxPlayersPerRoom, MaxPending: cfg.MaxPendingPerRoom, PendingTimeout: time.Duration(cfg.PendingTimeoutSec) * time.Second, ReconnectGrace: time.Duration(cfg.ReconnectGraceSec) * time.Second, SessionIdleTimeout: time.Duration(cfg.SessionIdleTimeoutSec) * time.Second, ConnectionBudget: budget})
	return &Server{cfg: cfg, listener: ln, rooms: rm, handler: control.NewHandler(cfg, rm), connectionBudget: budget,
		conns: make(map[net.Conn]struct{}), shutdownDone: make(chan struct{})}, nil
}

func (s *Server) Run(ctx context.Context) error {
	slog.Info("relay server listening", "control_port", s.cfg.ControlPort, "room_port_min", s.cfg.RoomPortMin, "room_port_max", s.cfg.RoomPortMax)
	var wg sync.WaitGroup
	stopShutdown := context.AfterFunc(ctx, s.shutdown)
	defer stopShutdown()
	defer s.shutdown()
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				s.shutdown()
				wg.Wait()
				return nil
			}
			s.shutdown()
			wg.Wait()
			return err
		}
		if !s.addConn(conn) {
			_ = conn.Close()
			continue
		}
		wg.Add(1) //Add a goroutine to the wait group for each accepted connection. This ensures that the server will wait for all active connections to finish before shutting down.
		go func() {
			defer wg.Done()
			s.handler.ServeConn(conn, func() { s.removeConn(conn) })
		}()
	}
}

func (s *Server) addConn(conn net.Conn) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false
	}
	if !s.connectionBudget.TryAcquire() {
		if time.Since(s.lastLimitLog) >= 10*time.Second {
			slog.Warn("connection rejected: global connection limit reached")
			s.lastLimitLog = time.Now()
		}
		return false
	}
	s.conns[conn] = struct{}{}
	return true
}

func (s *Server) removeConn(conn net.Conn) {
	s.mu.Lock()
	if _, exists := s.conns[conn]; exists {
		delete(s.conns, conn)
		s.connectionBudget.Release()
	}
	s.mu.Unlock()
}

func (s *Server) shutdown() {
	s.shutdownOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		_ = s.listener.Close()
		for conn := range s.conns {
			_ = conn.Close()
		}
		s.mu.Unlock()
		s.rooms.CloseAll()
		close(s.shutdownDone)
	})
	<-s.shutdownDone
}

func RunFromConfig(path string) (runErr error) {
	cfg, err := config.Load(path)
	if err != nil {
		return err
	}
	var logFile *os.File
	var logBuffer *bufferedLogWriter
	if cfg.LogFile != "" {
		logFile, err = os.OpenFile(cfg.LogFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			return fmt.Errorf("open log file: %w", err)
		}
		logBuffer = &bufferedLogWriter{dst: logFile, threshold: 64 << 10}
		slog.SetDefault(slog.New(slog.NewJSONHandler(logBuffer, nil)))
		defer func() {
			slog.Info("relay process stopped; flushing logs")
			if flushErr := logBuffer.Flush(); flushErr != nil {
				runErr = errors.Join(runErr, fmt.Errorf("flush logs: %w", flushErr))
			}
			if syncErr := logFile.Sync(); syncErr != nil {
				runErr = errors.Join(runErr, fmt.Errorf("sync log file: %w", syncErr))
			}
			if closeErr := logFile.Close(); closeErr != nil {
				runErr = errors.Join(runErr, fmt.Errorf("close log file: %w", closeErr))
			}
		}()
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	s, err := New(cfg)
	if err != nil {
		return err
	}
	return s.Run(ctx)
}
