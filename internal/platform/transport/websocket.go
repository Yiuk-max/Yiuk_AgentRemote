package transport

import (
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"agentremote/internal/platform/protocol"
	"github.com/gorilla/websocket"
)

var ErrListenerClosed = errors.New("websocket listener closed")

type Conn interface {
	ReadMessage() ([]byte, error)
	WriteMessage([]byte) error
	SetReadDeadline(time.Time) error
	SetWriteDeadline(time.Time) error
	LocalAddr() net.Addr
	RemoteAddr() net.Addr
	Close() error
}

type WebSocketListener struct {
	listener net.Listener
	server   *http.Server
	conns    chan Conn
	done     chan struct{}
	once     sync.Once
	maxBytes int
}

// DialWebSocket 主动连接 WebSocket 服务端。
// address 支持 ws://、wss://、tls://、tcp:// 以及 host:port 格式。
// tlsConfig 用于配置 TLS，例如证书校验、客户端证书等。
func DialWebSocket(address string, tlsConfig *tls.Config) (Conn, error) {
	scheme := "ws"
	if tlsConfig != nil || strings.HasPrefix(address, "tls://") || strings.HasPrefix(address, "tcp://") || strings.HasPrefix(address, "wss://") {
		scheme = "wss" //有配置加密，走加密
	}
	var u url.URL
	// 如果地址已经包含 ws:// 或 wss://，直接解析 URL。
	if strings.HasPrefix(address, "ws://") || strings.HasPrefix(address, "wss://") {
		parsed, err := url.Parse(address)
		if err != nil {
			return nil, err
		}
		u = *parsed
		if tlsConfig != nil {
			u.Scheme = "wss"
		}
	} else { // 去掉自定义地址前缀，提取实际的 host:port
		address = strings.TrimPrefix(strings.TrimPrefix(address, "tls://"), "tcp://")
		u = url.URL{Scheme: scheme, Host: address, Path: "/"}
	}
	// 创建 WebSocket 拨号器
	// HandshakeTimeout：限制 WebSocket 握手的最长等待时间
	// TLSClientConfig：配置底层 TLS 连接
	dialer := websocket.Dialer{HandshakeTimeout: 5 * time.Second, TLSClientConfig: tlsConfig}
	conn, response, err := dialer.Dial(u.String(), nil)
	if response != nil && response.Body != nil {
		_ = response.Body.Close()
	}
	if err != nil {
		return nil, err
	}
	conn.SetReadLimit(int64(protocol.DefaultMaxMessageBytes))
	return &wsConn{Conn: conn}, nil
}

func ListenWebSocket(address, certFile, keyFile string, allowInsecure bool, maxBytes int) (*WebSocketListener, error) {
	ln, err := Listen(address, certFile, keyFile, allowInsecure)
	if err != nil {
		return nil, err
	}
	return ServeWebSocketListener(ln, maxBytes), nil
}

// ServeWebSocketListener 将一个普通 TCP Listener 包装成 WebSocket Listener。
// 外部通过 Accept 获取已经完成 WebSocket 握手的连接。
// ln：底层 TCP Listener。
// maxBytes：单条 WebSocket 消息允许的最大字节数。
func ServeWebSocketListener(ln net.Listener, maxBytes int) *WebSocketListener {
	if maxBytes <= 0 {
		maxBytes = protocol.DefaultMaxMessageBytes
	}
	ws := &WebSocketListener{listener: ln, conns: make(chan Conn), done: make(chan struct{}), maxBytes: maxBytes}
	upgrader := websocket.Upgrader{ReadBufferSize: 4096, WriteBufferSize: 4096}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || !websocket.IsWebSocketUpgrade(r) {
			http.Error(w, "WebSocket upgrade required", http.StatusUpgradeRequired)
			return
		}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		conn.SetReadLimit(int64(ws.maxBytes))
		select {
		case ws.conns <- &wsConn{Conn: conn}:
		case <-ws.done:
			_ = conn.Close()
		}
	})
	ws.server = &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		_ = ws.server.Serve(ln)
		ws.Close()
	}()
	return ws
}

func (l *WebSocketListener) Accept() (Conn, error) {
	select {
	case conn := <-l.conns:
		return conn, nil
	case <-l.done:
		return nil, ErrListenerClosed
	}
}

func (l *WebSocketListener) Close() error {
	var err error
	l.once.Do(func() {
		close(l.done)
		err = l.server.Close()
		_ = l.listener.Close()
	})
	return err
}

func (l *WebSocketListener) Addr() net.Addr { return l.listener.Addr() }

type wsConn struct{ *websocket.Conn }

func (c *wsConn) ReadMessage() ([]byte, error) {
	kind, data, err := c.Conn.ReadMessage()
	if err != nil {
		return nil, err
	}
	if kind != websocket.BinaryMessage {
		return nil, errors.New("expected binary WebSocket message")
	}
	return data, nil
}

func (c *wsConn) WriteMessage(data []byte) error {
	return c.Conn.WriteMessage(websocket.BinaryMessage, data)
}

func (c *wsConn) LocalAddr() net.Addr                { return c.Conn.LocalAddr() }
func (c *wsConn) RemoteAddr() net.Addr               { return c.Conn.RemoteAddr() }
func (c *wsConn) SetReadDeadline(t time.Time) error  { return c.Conn.SetReadDeadline(t) }
func (c *wsConn) SetWriteDeadline(t time.Time) error { return c.Conn.SetWriteDeadline(t) }
