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

// connectIfNeeded 复用现有连接，或建立认证连接并启动消息接收和心跳协程。
// 调用方须持有 requestMu，以串行化连接初始化；连接状态由 connectionMu 保护。
func (c *Client) connectIfNeeded() error {
	c.connectionMu.Lock()
	if c.closed {
		c.connectionMu.Unlock()
		return &agenttools.Error{Code: "PLATFORM_UNAVAILABLE", Message: "platform client is closed"}
	}
	if c.connected {
		c.connectionMu.Unlock()
		return nil
	}
	c.connectionMu.Unlock()

	conn, connectionID, err := c.dialAndAuthenticate()
	if err != nil {
		return err
	}
	incomingMessages := make(chan protocol.Message, 256)
	connectionClosed := make(chan struct{})
	c.connectionMu.Lock()
	c.conn, c.connectionID, c.incomingMessages, c.connectionClosed, c.connected = conn, connectionID, incomingMessages, connectionClosed, true
	c.connectionMu.Unlock()
	go c.receiveMessages(conn, incomingMessages, connectionClosed)
	go c.maintainHeartbeat(conn, connectionClosed)
	return nil
}

// heartbeatInterval 指定操作端连接向平台发送心跳的间隔
// 服务端会在连接静默超过 heartbeat_timeout_sec（默认 45 秒）后将其关闭
// 因此操作端必须保持空闲连接活跃，避免会话中断
func (c *Client) heartbeatInterval() time.Duration {
	seconds := c.config.HeartbeatIntervalSeconds
	if seconds <= 0 {
		seconds = defaultHeartbeatIntervalSeconds
	}
	if seconds < minHeartbeatIntervalSeconds {
		seconds = minHeartbeatIntervalSeconds
	}
	return time.Duration(seconds) * time.Second
}

// maintainHeartbeat 定期发送心跳，在对端停止响应或写入失败时关闭连接。
func (c *Client) maintainHeartbeat(conn transport.Conn, connectionClosed chan struct{}) {
	ticker := time.NewTicker(c.heartbeatInterval())
	defer ticker.Stop()
	previousPongCount := c.pongCount.Load()
	missedHeartbeats := 0
	var sequence uint64
	for {
		select {
		case <-connectionClosed:
			return
		case <-ticker.C:
		}
		currentPongCount := c.pongCount.Load()
		if currentPongCount == previousPongCount {
			missedHeartbeats++
		} else {
			missedHeartbeats = 0
		}
		previousPongCount = currentPongCount
		if missedHeartbeats >= maxMissedHeartbeats {
			// 平台已停止响应；断开连接，使下一次工具调用重新连接，而不是等待 TCP 超时
			c.closeConnection(connectionClosed)
			return
		}
		sequence++
		if err := c.writeProtocolMessage(conn, protocol.Message{
			Version:      protocol.Version,
			BusinessType: protocol.BusinessAgentAssist,
			Type:         protocol.TypePing,
			Sequence:     sequence,
		}); err != nil {
			c.closeConnection(connectionClosed)
			return
		}
	}
}

// writeProtocolMessage 编码并串行写入一个协议帧，使用写超时限制阻塞时间。
func (c *Client) writeProtocolMessage(conn transport.Conn, message protocol.Message) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	data, err := c.codec.Encode(message)
	if err == nil {
		err = conn.WriteMessage(data)
	}
	_ = conn.SetWriteDeadline(time.Time{})
	return err
}

// dialAndAuthenticate 建立 WebSocket 连接并完成操作端密码认证。
func (c *Client) dialAndAuthenticate() (transport.Conn, string, error) {
	conn, err := c.dialPlatform()
	if err != nil {
		// 保留底层错误原因，以便区分主机不可达和证书指纹校验失败
		return nil, "", &agenttools.Error{Code: "PLATFORM_UNAVAILABLE", Message: "could not reach platform: " + err.Error()}
	}
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	authRequest := protocol.Message{Version: protocol.Version, BusinessType: protocol.BusinessAgentAssist, Type: protocol.TypeAuthenticatePlayer, IdentityType: "player", PlayerPassword: c.config.PlayerPassword}
	authData, err := c.codec.Encode(authRequest)
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
		response, err := c.codec.Decode(data)
		if err != nil {
			_ = conn.Close()
			return nil, "", &agenttools.Error{Code: "PLATFORM_UNAVAILABLE", Message: "platform authentication failed"}
		}
		switch response.Type {
		case protocol.TypeAuthenticated:
			_ = conn.SetReadDeadline(time.Time{})
			return conn, response.ConnectionID, nil
		case protocol.TypeError:
			_ = conn.Close()
			code := response.Code
			if code == "" {
				code = "PLATFORM_AUTH_FAILED"
			}
			return nil, "", &agenttools.Error{Code: code, Message: response.Message}
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

// dialPlatform 根据明文、CA 信任或证书指纹固定配置创建平台连接。
func (c *Client) dialPlatform() (transport.Conn, error) {
	address := c.config.Address
	if c.config.AllowPlaintext {
		address = "ws://" + strings.TrimPrefix(strings.TrimPrefix(address, "ws://"), "wss://")
		return transport.DialWebSocket(address, nil)
	}
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: hostnameFromAddress(address)}
	if c.config.InsecureSkipVerify {
		tlsConfig.InsecureSkipVerify = true
	}
	if pin := strings.ToLower(strings.TrimSpace(c.config.PinnedCertSHA256)); pin != "" {
		// 使用精确的证书指纹检查替代常规验证
		tlsConfig.InsecureSkipVerify = true
		tlsConfig.VerifyPeerCertificate = certPinVerifier(pin)
	}
	if c.config.TLSCAFile != "" {
		pool, err := loadCertPool(c.config.TLSCAFile)
		if err != nil {
			return nil, err
		}
		tlsConfig.RootCAs = pool
	}
	if strings.HasPrefix(address, "ws://") {
		address = "wss://" + strings.TrimPrefix(address, "ws://")
	}
	return transport.DialWebSocket(address, tlsConfig)
}

// receiveMessages 持续解码平台消息，将普通事件放入队列并单独统计心跳响应。
func (c *Client) receiveMessages(conn transport.Conn, incomingMessages chan protocol.Message, connectionClosed chan struct{}) {
	defer c.closeConnection(connectionClosed)
	for {
		data, err := conn.ReadMessage()
		if err != nil {
			return
		}
		message, err := c.codec.Decode(data)
		if err != nil {
			return
		}
		if message.Type == protocol.TypePong {
			// Pong 只能证明连接仍有效；心跳 goroutine 会读取计数器，无需将该帧传递给工具调用
			c.pongCount.Add(1)
			continue
		}
		select {
		case incomingMessages <- message:
		case <-connectionClosed:
			return
		}
	}
}

// closeConnection 关闭连接、清理连接状态并通知等待方。
// 仅当 connectionClosed 属于当前连接时执行，避免旧连接协程关闭新连接。
func (c *Client) closeConnection(connectionClosed chan struct{}) {
	c.connectionMu.Lock()
	defer c.connectionMu.Unlock()
	if c.connectionClosed != connectionClosed || !c.connected {
		return
	}
	c.connected = false
	if c.conn != nil {
		_ = c.conn.Close()
		c.conn = nil
	}
	close(connectionClosed)
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

// hostnameFromAddress 从连接地址中提取 TLS 校验所需的主机名。
func hostnameFromAddress(address string) string {
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
