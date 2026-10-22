package platformclient

import (
	"bufio"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"yiuk-switchboard/internal/agenttools"
	"yiuk-switchboard/internal/platform/protocol"
)

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

	conn, reader, connID, err := c.handshake()
	if err != nil {
		return err
	}
	inbox := make(chan protocol.Message, 256)
	done := make(chan struct{})
	c.mu.Lock()
	c.conn, c.connID, c.inbox, c.connDone, c.connected = conn, connID, inbox, done, true
	c.mu.Unlock()
	go c.readLoop(conn, reader, inbox, done)
	return nil
}

func (c *Client) handshake() (net.Conn, *bufio.Reader, string, error) {
	conn, err := c.dial()
	if err != nil {
		// Keep the underlying cause: it distinguishes an unreachable host from
		// a rejected certificate fingerprint.
		return nil, nil, "", &agenttools.Error{Code: "PLATFORM_UNAVAILABLE", Message: "could not reach platform: " + err.Error()}
	}
	reader := bufio.NewReaderSize(conn, c.codec.MaxFrameBytes+4)
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	auth := protocol.Message{Version: protocol.Version, BusinessType: protocol.BusinessAgentAssist, Type: protocol.TypeAuthenticatePlayer, IdentityType: "player", PlayerPassword: c.cfg.PlayerPassword}
	if err := c.codec.Write(conn, auth); err != nil {
		_ = conn.Close()
		return nil, nil, "", &agenttools.Error{Code: "PLATFORM_UNAVAILABLE", Message: "could not authenticate with platform"}
	}
	for {
		m, err := c.codec.Read(reader)
		if err != nil {
			_ = conn.Close()
			return nil, nil, "", &agenttools.Error{Code: "PLATFORM_UNAVAILABLE", Message: "platform authentication failed"}
		}
		switch m.Type {
		case protocol.TypeAuthenticated:
			_ = conn.SetDeadline(time.Time{})
			return conn, reader, m.ConnectionID, nil
		case protocol.TypeError:
			_ = conn.Close()
			code := m.Code
			if code == "" {
				code = "PLATFORM_AUTH_FAILED"
			}
			return nil, nil, "", &agenttools.Error{Code: code, Message: m.Message}
		}
	}
}

// certPinVerifier returns a tls.Config.VerifyPeerCertificate callback that
// accepts exactly the certificate whose DER bytes hash to pin (lowercase hex
// SHA-256). An empty pin is never valid here; callers only install the
// callback when pinning is configured.
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

func (c *Client) dial() (net.Conn, error) {
	address := c.cfg.Address
	address = strings.TrimPrefix(address, "tls://")
	if c.cfg.AllowPlaintext {
		return net.DialTimeout("tcp", address, 5*time.Second)
	}
	cfg := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: hostOnly(address)}
	if c.cfg.InsecureSkipVerify {
		cfg.InsecureSkipVerify = true
	}
	if pin := strings.ToLower(strings.TrimSpace(c.cfg.PinnedCertSHA256)); pin != "" {
		// Verification is replaced by an exact certificate fingerprint check.
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
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	return dialTLSWithFallback(dialer, address, cfg)
}

func dialTLSWithFallback(dialer *net.Dialer, address string, cfg *tls.Config) (net.Conn, error) {
	conn, err := tls.DialWithDialer(dialer, "tcp4", address, cfg)
	if err == nil {
		return conn, nil
	}
	return tls.DialWithDialer(dialer, "tcp", address, cfg)
}

func (c *Client) readLoop(conn net.Conn, reader *bufio.Reader, inbox chan protocol.Message, done chan struct{}) {
	defer c.markDisconnected(done)
	for {
		m, err := c.codec.Read(reader)
		if err != nil {
			return
		}
		select {
		case inbox <- m:
		case <-done:
			return
		}
	}
}

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

func hostOnly(address string) string {
	if host, _, err := net.SplitHostPort(address); err == nil {
		return host
	}
	return address
}

func randomID() string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(raw[:])
}
