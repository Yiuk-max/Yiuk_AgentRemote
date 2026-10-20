package control

import (
	"bufio"
	"crypto/subtle"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"sync"
	"time"

	"yiuk-switchboard/internal/auth"
	"yiuk-switchboard/internal/config"
	"yiuk-switchboard/internal/protocol"
	"yiuk-switchboard/internal/room"
)

type Handler struct {
	config  config.Config
	rooms   *room.Manager
	limiter *auth.Limiter
}

func NewHandler(c config.Config, rooms *room.Manager) *Handler {
	return &Handler{config: c, rooms: rooms, limiter: auth.NewLimiter(c.AuthRatePerMin, time.Minute)}
}

func (h *Handler) ServeConn(conn net.Conn, onClose func()) { //message handler for control connection
	closeConn := true
	releaseOnReturn := true
	defer func() {
		if closeConn {
			_ = conn.Close()
		}
		if releaseOnReturn && onClose != nil {
			onClose()
		}
	}()
	if tcp, ok := conn.(*net.TCPConn); ok {
		_ = tcp.SetKeepAlive(true)
	}
	r := bufio.NewReaderSize(conn, protocol.MaxMessageBytes+8)
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	first, err := protocol.ReadFrame(r)
	if err != nil {
		return
	}
	if first.T == "data" {
		if first.SID == "" || !h.rooms.Pair(first.SID, conn, r, onClose) {
			slog.Warn("rejected data connection", "remote", conn.RemoteAddr().String())
			return
		}
		// The paired session now owns both socket cleanup and the global connection slot.
		closeConn = false
		releaseOnReturn = false
		// The initial handshake has a short deadline to prevent idle sockets from
		// hanging around. Once this socket becomes a paired byte tunnel, remove that
		// deadline; leaving it in place drops every relayed game session after 10s.
		if err := conn.SetReadDeadline(time.Time{}); err != nil {
			slog.Warn("could not clear data connection read deadline", "remote", conn.RemoteAddr().String(), "error", err)
			_ = conn.Close()
			return
		}
		return
	}
	if first.T != "auth" {
		_ = protocol.Write(conn, protocol.Message{T: "err", Code: "BAD_REQUEST", Msg: "first message must be auth or data"})
		return
	}
	if !h.limiter.Allow(conn.RemoteAddr()) {
		_ = protocol.Write(conn, protocol.Message{T: "err", Code: "RATE_LIMITED", Msg: "too many login attempts"})
		return
	}
	if first.Ver != 2 {
		_ = protocol.Write(conn, protocol.Message{T: "err", Code: "BAD_VERSION", Msg: "unsupported protocol version"})
		return
	}
	hostPassword := subtle.ConstantTimeCompare([]byte(first.PW), []byte(h.config.HostPassword)) == 1
	playerPassword := subtle.ConstantTimeCompare([]byte(first.PW), []byte(h.config.PlayerPassword)) == 1
	if !hostPassword && !playerPassword {
		slog.Warn("authentication failed", "remote", conn.RemoteAddr().String())
		_ = protocol.Write(conn, protocol.Message{T: "err", Code: "BAD_PASSWORD", Msg: "password incorrect"})
		return
	}
	if first.Name == "" {
		_ = protocol.Write(conn, protocol.Message{T: "err", Code: "BAD_REQUEST", Msg: "nickname is required"})
		return
	}
	clientID := conn.RemoteAddr().String() + "/" + strconv.FormatInt(time.Now().UnixNano(), 10)
	client := &client{conn: conn}
	if err := client.send(protocol.Message{T: "ok", Msg: "登录成功"}); err != nil {
		return
	}
	slog.Info("client authenticated", "remote", conn.RemoteAddr().String(), "name", first.Name)
	defer h.rooms.OwnerDisconnected(clientID)
	_ = conn.SetReadDeadline(time.Now().Add(time.Duration(h.config.HeartbeatTimeoutSec) * time.Second))
	for {
		msg, err := protocol.ReadFrame(r)
		if err != nil {
			if !errors.Is(err, net.ErrClosed) {
				slog.Debug("control connection ended", "remote", conn.RemoteAddr().String(), "error", err)
			}
			return
		}
		_ = conn.SetReadDeadline(time.Now().Add(time.Duration(h.config.HeartbeatTimeoutSec) * time.Second))
		if err := h.handle(client, clientID, first.Name, hostPassword, conn.RemoteAddr(), msg); err != nil {
			return
		}
	}
}

func (h *Handler) handle(c *client, id, name string, canHost bool, remote net.Addr, msg protocol.Message) error {
	switch msg.T {
	case "ping":
		return c.send(protocol.Message{T: "pong"})
	case "list":
		return c.send(protocol.Message{T: "rooms", List: h.rooms.List()})
	case "create":
		if !canHost {
			return c.send(protocol.Message{T: "err", Code: "HOST_PERMISSION_REQUIRED", Msg: "当前登录权限不能创建房间，请使用服主密码"})
		}
		var hostIP net.IP
		if tcp, ok := remote.(*net.TCPAddr); ok {
			hostIP = append(net.IP(nil), tcp.IP...)
		}
		created, err := h.rooms.Create(msg.Name, name, msg.MC, msg.Loader, msg.Max, msg.Mods, msg.LocalPort, hostIP, id, c.send)
		if err != nil {
			code := "BAD_REQUEST"
			if errors.Is(err, room.ErrRoomLimit) {
				code = "ROOM_LIMIT"
			} else if errors.Is(err, room.ErrPortsExhausted) {
				code = "PORT_EXHAUSTED"
			}
			return c.send(protocol.Message{T: "err", Code: code, Msg: err.Error()})
		}
		return c.send(created)
	case "room_mods":
		mods, ok := h.rooms.Mods(msg.ID)
		if !ok {
			return c.send(protocol.Message{T: "err", Code: "NOT_FOUND", Msg: "room not found"})
		}
		if mods == nil {
			mods = make([]protocol.Mod, 0)
		}
		return c.send(protocol.Message{T: "mods", ID: msg.ID, Mods: mods})
	case "close":
		if !h.rooms.Close(msg.ID, id) {
			return c.send(protocol.Message{T: "err", Code: "NOT_OWNER", Msg: "room not found or caller is not owner"})
		}
		return c.send(protocol.Message{T: "ok", ID: msg.ID})
	case "resume":
		if !canHost {
			return c.send(protocol.Message{T: "err", Code: "HOST_PERMISSION_REQUIRED", Msg: "当前登录权限不能恢复服主房间，请使用服主密码"})
		}
		if !h.rooms.Resume(msg.ID, msg.Token, id, c.send) {
			return c.send(protocol.Message{T: "err", Code: "BAD_TOKEN", Msg: "room could not be resumed"})
		}
		return c.send(protocol.Message{T: "ok", ID: msg.ID})
	default:
		return c.send(protocol.Message{T: "err", Code: "BAD_REQUEST", Msg: fmt.Sprintf("unknown message type %q", msg.T)})
	}
}

type client struct {
	conn net.Conn
	mu   sync.Mutex
}

func (c *client) send(msg protocol.Message) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	_ = c.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	return protocol.Write(c.conn, msg)
}
