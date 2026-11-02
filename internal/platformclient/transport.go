package platformclient

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
	"time"

	"agentremote/internal/agenttools"
	"agentremote/internal/platform/protocol"
	"agentremote/internal/platform/transport"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
)

// ensureConnectedLocked 在首次调用时建立认证连接；调用方需持有 callMu。
func (c *Client) ensureConnectedLocked() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return &agenttools.Error{Code: "PLATFORM_UNAVAILABLE", Message: "platform client is closed"}
	}
	if c.connected {
		c.mu.Unlock()
		return nil
	}
	c.mu.Unlock()

	conn, connID, err := c.handshake()
	if err != nil {
		return err
	}
	inbox := make(chan protocol.Message, 256)
	done := make(chan struct{})
	c.mu.Lock()
	c.conn, c.connID, c.inbox, c.connDone, c.connected = conn, connID, inbox, done, true
	c.mu.Unlock()
	go c.readLoop(conn, inbox, done)
	go c.heartbeatLoop(conn, done)
	return nil
}

// heartbeatInterval 指定操作端连接向平台发送心跳的间隔
// 服务端会在连接静默超过 heartbeat_timeout_sec（默认 45 秒）后将其关闭
// 因此操作端必须保持空闲连接活跃，避免会话中断
func (c *Client) heartbeatInterval() time.Duration {
	seconds := c.cfg.HeartbeatIntervalSeconds
	if seconds <= 0 {
		seconds = defaultHeartbeatIntervalSeconds
	}
	if seconds < minHeartbeatIntervalSeconds {
		seconds = minHeartbeatIntervalSeconds
	}
	return time.Duration(seconds) * time.Second
}

// heartbeatLoop 保持空闲连接有效，并检测未关闭套接字但不再响应心跳的对端
func (c *Client) heartbeatLoop(conn transport.Conn, done chan struct{}) {
	ticker := time.NewTicker(c.heartbeatInterval())
	defer ticker.Stop()
	lastPongs := c.pongs.Load()
	misses := 0
	var sequence uint64
	for {
		select {
		case <-done:
			return
		case <-ticker.C:
		}
		pongs := c.pongs.Load()
		if pongs == lastPongs {
			misses++
		} else {
			misses = 0
		}
		lastPongs = pongs
		if misses >= maxMissedHeartbeats {
			// 平台已停止响应；断开连接，使下一次工具调用重新连接，而不是等待 TCP 超时
			c.markDisconnected(done)
			return
		}
		sequence++
		if err := c.writeMessage(conn, protocol.Message{
			Version:      protocol.Version,
			BusinessType: protocol.BusinessAgentAssist,
			Type:         protocol.TypePing,
			Sequence:     sequence,
		}); err != nil {
			c.markDisconnected(done)
			return
		}
	}
}

// writeMessage 串行化单个数据帧，并限制无响应对端阻塞写入的时间
// writeMessage 对单帧写入加锁，并设置有限的写超时，避免对端失效时永久阻塞。
func (c *Client) writeMessage(conn transport.Conn, m protocol.Message) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	data, err := c.codec.Encode(m)
	if err == nil {
		err = conn.WriteMessage(data)
	}
	_ = conn.SetWriteDeadline(time.Time{})
	return err
}

// handshake 建立 WebSocket 连接并完成操作端密码认证。
func (c *Client) handshake() (transport.Conn, string, error) {
	conn, err := c.dial()
	if err != nil {
		// 保留底层错误原因，以便区分主机不可达和证书指纹校验失败
		return nil, "", &agenttools.Error{Code: "PLATFORM_UNAVAILABLE", Message: "could not reach platform: " + err.Error()}
	}
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	auth := protocol.Message{Version: protocol.Version, BusinessType: protocol.BusinessAgentAssist, Type: protocol.TypeAuthenticatePlayer, IdentityType: "player", PlayerPassword: c.cfg.PlayerPassword}
	authData, err := c.codec.Encode(auth)
	if err != nil || conn.WriteMessage(authData) != nil {
		_ = conn.Close()
		return nil, "", &agenttools.Error{Code: "PLATFORM_UNAVAILABLE", Message: "could not authenticate with platform"}
	}
	for {
		data, err := conn.ReadMessage()
		if err != nil {
			_ = conn.Close()
			return nil, "", &agenttools.Error{Code: "PLATFORM_UNAVAILABLE", Message: "platform authentication failed"}
		}
		m, err := c.codec.Decode(data)
		if err != nil {
			_ = conn.Close()
			return nil, "", &agenttools.Error{Code: "PLATFORM_UNAVAILABLE", Message: "platform authentication failed"}
		}
		switch m.Type {
		case protocol.TypeAuthenticated:
			_ = conn.SetReadDeadline(time.Time{})
			return conn, m.ConnectionID, nil
		case protocol.TypeError:
			_ = conn.Close()
			code := m.Code
			if code == "" {
				code = "PLATFORM_AUTH_FAILED"
			}
			return nil, "", &agenttools.Error{Code: code, Message: m.Message}
		}
	}
}

// certPinVerifier 返回 tls.Config.VerifyPeerCertificate 回调
// 该回调只接受 DER 字节的 SHA-256（小写十六进制）与 pin 完全匹配的证书
// 此处空 pin 始终无效；调用方仅在配置证书固定时安装该回调
func certPinVerifier(pin string) func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
	return func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
		if len(rawCerts) == 0 {
			return errors.New("server presented no certificate")
		}
		sum := sha256.Sum256(rawCerts[0])
		if got := hex.EncodeToString(sum[:]); got != pin {
			return fmt.Errorf("server certificate fingerprint mismatch: got %s", got)
		}
		return nil
	}
}

// dial 根据明文、CA 信任或证书指纹固定配置创建平台连接。
func (c *Client) dial() (transport.Conn, error) {
	address := c.cfg.Address
	if c.cfg.AllowPlaintext {
		address = "ws://" + strings.TrimPrefix(strings.TrimPrefix(address, "ws://"), "wss://")
		return transport.DialWebSocket(address, nil)
	}
	cfg := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: hostOnly(address)}
	if c.cfg.InsecureSkipVerify {
		cfg.InsecureSkipVerify = true
	}
	if pin := strings.ToLower(strings.TrimSpace(c.cfg.PinnedCertSHA256)); pin != "" {
		// 使用精确的证书指纹检查替代常规验证
		cfg.InsecureSkipVerify = true
		cfg.VerifyPeerCertificate = certPinVerifier(pin)
	}
	if c.cfg.TLSCAFile != "" {
		pool, err := loadCertPool(c.cfg.TLSCAFile)
		if err != nil {
			return nil, err
		}
		cfg.RootCAs = pool
	}
	if strings.HasPrefix(address, "ws://") {
		address = "wss://" + strings.TrimPrefix(address, "ws://")
	}
	return transport.DialWebSocket(address, cfg)
}

// readLoop 持续解码平台消息，将普通事件放入收件箱并单独统计心跳响应。
func (c *Client) readLoop(conn transport.Conn, inbox chan protocol.Message, done chan struct{}) {
	defer c.markDisconnected(done)
	for {
		data, err := conn.ReadMessage()
		if err != nil {
			return
		}
		m, err := c.codec.Decode(data)
		if err != nil {
			return
		}
		if m.Type == protocol.TypePong {
			// Pong 只能证明连接仍有效；心跳 goroutine 会读取计数器，无需将该帧传递给工具调用
			c.pongs.Add(1)
			continue
		}
		select {
		case inbox <- m:
		case <-done:
			return
		}
	}
}

// markDisconnected 仅关闭当前连接对应的状态，避免旧连接协程影响新连接。
func (c *Client) markDisconnected(done chan struct{}) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.connDone != done || !c.connected {
		return
	}
	c.connected = false
	if c.conn != nil {
		_ = c.conn.Close()
		c.conn = nil
	}
	close(done)
}

// loadCertPool 读取 PEM 格式的 CA 文件并构建 TLS 根证书池。
func loadCertPool(path string) (*x509.CertPool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(data) {
		return nil, errors.New("no certificates found in tls_ca file")
	}
	return pool, nil
}

// hostOnly 从连接地址中提取 TLS 校验所需的主机名。
func hostOnly(address string) string {
	address = strings.TrimPrefix(strings.TrimPrefix(address, "wss://"), "ws://")
	address = strings.TrimPrefix(strings.TrimPrefix(address, "tls://"), "tcp://")
	if parsed, err := url.Parse("//" + address); err == nil && parsed.Hostname() != "" {
		return parsed.Hostname()
	}
	if host, _, err := net.SplitHostPort(address); err == nil {
		return host
	}
	return address
}

// randomID 生成请求关联标识；随机源不可用时退回到纳秒时间戳。
func randomID() string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(raw[:])
}
