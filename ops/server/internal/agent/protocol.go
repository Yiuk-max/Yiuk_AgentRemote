package agent

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"gopkg.in/yaml.v3"
	"yiuk-switchboard/internal/auth"
	relayconfig "yiuk-switchboard/internal/config"
)

const maxFrameBytes = 2 << 20

type wireRequest struct {
	Version  int             `json:"version"`
	ID       uint64          `json:"id"`
	Type     string          `json:"type"`
	Password string          `json:"password,omitempty"`
	Action   string          `json:"action,omitempty"`
	Config   *relayConfigDTO `json:"config,omitempty"`
}
type wireResponse struct {
	Version int    `json:"version"`
	ID      uint64 `json:"id"`
	Type    string `json:"type"`
	OK      bool   `json:"ok"`
	Error   string `json:"error,omitempty"`
	Data    any    `json:"data,omitempty"`
}

func Run(c Config) error {
	if err := c.Validate(); err != nil {
		return err
	}
	certificate, err := tls.LoadX509KeyPair(c.TLSCert, c.TLSKey)
	if err != nil {
		return fmt.Errorf("load TLS certificate: %w", err)
	}
	listener, err := tls.Listen("tcp", c.Listen, &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12})
	if err != nil {
		return err
	}
	a := &api{config: c, auth: newAuthenticator(c.RelayConfig), relay: newRelayProcess(c), loginLimiter: auth.NewLimiter(5, time.Minute)}
	ctx, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()
	var connections sync.Map
	var handlers sync.WaitGroup
	go func() {
		<-ctx.Done()
		_ = listener.Close()
		connections.Range(func(key, value any) bool { _ = value.(net.Conn).Close(); return true })
	}()
	for {
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			if ctx.Err() != nil {
				break
			}
			return acceptErr
		}
		connections.Store(conn, conn)
		handlers.Add(1)
		go func() { defer handlers.Done(); defer connections.Delete(conn); a.serveConn(conn) }()
	}
	handlers.Wait()
	if a.relay.status()["running"].(bool) {
		_ = a.relay.stop()
	}
	return nil
}

type api struct {
	config       Config
	auth         *authenticator
	relay        *relayProcess
	loginLimiter *auth.Limiter
}

type relayConfigDTO struct {
	ControlPort           int    `json:"control_port"`
	RoomPortMin           int    `json:"room_port_min"`
	RoomPortMax           int    `json:"room_port_max"`
	HostPassword          string `json:"host_password"`
	PlayerPassword        string `json:"player_password"`
	MaxRooms              int    `json:"max_rooms"`
	MaxPlayersPerRoom     int    `json:"max_players_per_room"`
	MaxPendingPerRoom     int    `json:"max_pending_per_room"`
	PendingTimeoutSec     int    `json:"pending_timeout_sec"`
	ReconnectGraceSec     int    `json:"reconnect_grace_sec"`
	HeartbeatTimeoutSec   int    `json:"heartbeat_timeout_sec"`
	SessionIdleTimeoutSec int    `json:"session_idle_timeout_sec"`
	MaxConnections        int    `json:"max_connections"`
	AuthRatePerMin        int    `json:"auth_rate_per_min"`
	LogFile               string `json:"log_file"`
}

func configDTO(c relayconfig.Config) relayConfigDTO {
	return relayConfigDTO{c.ControlPort, c.RoomPortMin, c.RoomPortMax, c.HostPassword, c.PlayerPassword, c.MaxRooms, c.MaxPlayersPerRoom, c.MaxPendingPerRoom, c.PendingTimeoutSec, c.ReconnectGraceSec, c.HeartbeatTimeoutSec, c.SessionIdleTimeoutSec, c.MaxConnections, c.AuthRatePerMin, c.LogFile}
}
func (c relayConfigDTO) config() relayconfig.Config {
	return relayconfig.Config{ControlPort: c.ControlPort, RoomPortMin: c.RoomPortMin, RoomPortMax: c.RoomPortMax, HostPassword: c.HostPassword, PlayerPassword: c.PlayerPassword, MaxRooms: c.MaxRooms, MaxPlayersPerRoom: c.MaxPlayersPerRoom, MaxPendingPerRoom: c.MaxPendingPerRoom, PendingTimeoutSec: c.PendingTimeoutSec, ReconnectGraceSec: c.ReconnectGraceSec, HeartbeatTimeoutSec: c.HeartbeatTimeoutSec, SessionIdleTimeoutSec: c.SessionIdleTimeoutSec, MaxConnections: c.MaxConnections, AuthRatePerMin: c.AuthRatePerMin, LogFile: c.LogFile}
}

func (a *api) serveConn(conn net.Conn) {
	defer conn.Close()
	remote := conn.RemoteAddr().String()
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
	if tlsConn, ok := conn.(*tls.Conn); ok {
		if err := tlsConn.Handshake(); err != nil {
			log.Printf("TLS handshake failed from %s: %v", remote, err)
			return
		}
	}
	_ = conn.SetDeadline(time.Time{})
	r := bufio.NewReaderSize(conn, 64<<10)
	authenticated := false
	for {
		request, err := readRequest(r)
		if err != nil {
			if err != io.EOF && err != net.ErrClosed {
				log.Printf("read request from %s failed: %v", remote, err)
			}
			return
		}
		response := a.dispatch(request, &authenticated, conn.RemoteAddr())
		if !response.OK {
			log.Printf("request from %s rejected (type=%s): %s", remote, request.Type, response.Error)
		}
		if err = writeResponse(conn, response); err != nil {
			log.Printf("write response to %s failed (type=%s): %v", remote, request.Type, err)
			return
		}
	}
}

func readRequest(r *bufio.Reader) (wireRequest, error) {
	var req wireRequest
	var prefix [4]byte
	if _, err := io.ReadFull(r, prefix[:]); err != nil {
		return req, err
	}
	size := binary.BigEndian.Uint32(prefix[:])
	if size == 0 || size > maxFrameBytes {
		return req, fmt.Errorf("invalid frame size")
	}
	b := make([]byte, size)
	if _, err := io.ReadFull(r, b); err != nil {
		return req, err
	}
	if err := json.Unmarshal(b, &req); err != nil {
		return req, err
	}
	return req, nil
}
func writeResponse(w io.Writer, response wireResponse) error {
	b, err := json.Marshal(response)
	if err != nil {
		return err
	}
	if len(b) > maxFrameBytes {
		return fmt.Errorf("response exceeds maximum frame size")
	}
	frame := make([]byte, 4+len(b))
	binary.BigEndian.PutUint32(frame[:4], uint32(len(b)))
	copy(frame[4:], b)
	for len(frame) > 0 {
		n, e := w.Write(frame)
		if e != nil {
			return e
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		frame = frame[n:]
	}
	return nil
}

func (a *api) dispatch(req wireRequest, authenticated *bool, remote net.Addr) wireResponse {
	response := wireResponse{Version: 1, ID: req.ID, Type: req.Type, OK: true}
	fail := func(code string) wireResponse { response.OK = false; response.Error = code; return response }
	if req.Version != 1 {
		return fail("unsupported operations protocol version")
	}
	switch req.Type {
	case "authenticate":
		if *authenticated {
			return fail("connection already authenticated")
		}
		if !a.loginLimiter.Allow(remote) {
			return fail("too many login attempts; try again later")
		}
		if !a.auth.verifyHostPassword(req.Password) {
			return fail("invalid host password")
		}
		*authenticated = true
		return response
	}
	if !*authenticated {
		return fail("host-password authentication required")
	}
	switch req.Type {
	case "status":
		response.Data = a.relay.status()
	case "metrics":
		response.Data = systemMetrics()
	case "config_get":
		b, err := os.ReadFile(a.config.RelayConfig)
		if err != nil {
			return fail("could not read relay config")
		}
		var c relayconfig.Config
		if err := yaml.Unmarshal(b, &c); err != nil {
			return fail("relay config is not valid YAML")
		}
		response.Data = configDTO(c)
	case "config_set":
		if req.Config == nil {
			return fail("configuration is required")
		}
		if err := a.saveConfig(*req.Config); err != nil {
			return fail(err.Error())
		}
		response.Data = *req.Config
	case "process":
		var err error
		switch req.Action {
		case "start":
			err = a.relay.start()
		case "stop":
			err = a.relay.stop()
		case "restart":
			err = a.relay.restart()
		default:
			return fail("unsupported process action")
		}
		if err != nil {
			return fail(err.Error())
		}
		response.Data = a.relay.status()
	default:
		return fail("unknown operation")
	}
	return response
}

func (a *api) saveConfig(dto relayConfigDTO) error {
	parsed := dto.config()
	if err := parsed.Validate(); err != nil {
		return fmt.Errorf("configuration rejected: %w", err)
	}
	b, err := yaml.Marshal(parsed)
	if err != nil {
		return fmt.Errorf("could not encode config")
	}
	f, err := os.CreateTemp(filepath.Dir(a.config.RelayConfig), ".relay-config-*.yaml")
	if err != nil {
		return fmt.Errorf("could not stage config")
	}
	temp := f.Name()
	defer os.Remove(temp)
	if _, err = f.Write(b); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return fmt.Errorf("could not write config")
	}
	if err = os.Rename(temp, a.config.RelayConfig); err != nil {
		return fmt.Errorf("could not replace config")
	}
	return nil
}
