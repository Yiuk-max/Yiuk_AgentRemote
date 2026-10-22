// Package platformclient implements the agenttools.Backend contract against
// the remote-assist platform. It keeps one authenticated operator connection
// per session so sessions are not interrupted between tool calls, caches
// streamed command output locally, and returns structured agenttools errors.

package platformclient

import (
	"context"
	"net"
	"sync"
	"time"

	"yiuk-switchboard/internal/agenttools"
	"yiuk-switchboard/internal/platform/protocol"
)

// Client is a persistent operator connection to the platform.
type Client struct {
	cfg   Config
	codec protocol.Codec

	callMu sync.Mutex // serializes tool calls and connection setup

	mu        sync.Mutex
	conn      net.Conn
	connID    string
	inbox     chan protocol.Message
	connDone  chan struct{}
	connected bool
	closed    bool

	cacheMu   sync.Mutex
	sessions  map[string]protocol.SessionInfo
	tasks     map[string]*taskCache
	downloads map[string]*downloadCache
	devices   []protocol.DeviceInfo
}

// Close terminates the operator connection.
func (c *Client) Close() error {
	c.callMu.Lock()
	defer c.callMu.Unlock()
	c.mu.Lock()
	c.closed = true
	conn, done := c.conn, c.connDone
	c.mu.Unlock()
	if conn != nil {
		_ = conn.Close()
	}
	if done != nil {
		c.markDisconnected(done)
	}
	return nil
}

// Invoke implements agenttools.Backend.
func (c *Client) Invoke(ctx context.Context, req agenttools.Request) (any, error) {
	switch req.Name {
	case agenttools.ListDevices:
		return c.listDevices(ctx)
	case agenttools.GetDeviceStatus:
		return c.getDeviceStatus(ctx, req.Input)
	case agenttools.RequestConnection:
		return c.requestConnection(ctx, req.Input)
	case agenttools.RunCommand:
		return c.runCommand(ctx, req.Input)
	case agenttools.GetSessionStatus:
		return c.getSessionStatus(ctx, req.Input)
	case agenttools.ExecuteCommand:
		return c.executeCommand(ctx, req.Input)
	case agenttools.GetCommandResult:
		return c.getCommandResult(ctx, req.Input)
	case agenttools.DownloadFile:
		return c.downloadFile(ctx, req.Input)
	case agenttools.CancelCommand:
		return c.cancelCommand(ctx, req.Input)
	case agenttools.CloseSession:
		return c.closeSession(ctx, req.Input)
	default:
		return nil, &agenttools.Error{Code: "UNKNOWN_TOOL", Message: "unsupported tool"}
	}
}

func (c *Client) call(ctx context.Context, req protocol.Message, match func(protocol.Message) bool) (protocol.Message, error) {
	return c.callTimeout(ctx, req, match, time.Duration(c.cfg.TimeoutSeconds)*time.Second)
}

func (c *Client) callTimeout(ctx context.Context, req protocol.Message, match func(protocol.Message) bool, timeout time.Duration) (protocol.Message, error) {
	c.callMu.Lock()
	defer c.callMu.Unlock()

	if err := c.ensureConnectedLocked(); err != nil {
		return protocol.Message{}, err
	}
	c.mu.Lock()
	conn, inbox, done := c.conn, c.inbox, c.connDone
	c.mu.Unlock()

	// Process any events that arrived between calls so cached output is up to
	// date before a new request is correlated.
	c.drainInbox(inbox)

	correlationID := randomID()
	req.Version = protocol.Version
	req.BusinessType = protocol.BusinessAgentAssist
	req.CorrelationID = correlationID
	_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	if err := c.codec.Write(conn, req); err != nil {
		c.markDisconnected(done)
		return protocol.Message{}, &agenttools.Error{Code: "PLATFORM_UNAVAILABLE", Message: "could not send request to platform"}
	}
	_ = conn.SetWriteDeadline(time.Time{})

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return protocol.Message{}, &agenttools.Error{Code: "CANCELLED", Message: "tool call was cancelled"}
		case <-timer.C:
			return protocol.Message{}, &agenttools.Error{Code: "PLATFORM_TIMEOUT", Message: "platform did not respond in time"}
		case <-done:
			return protocol.Message{}, &agenttools.Error{Code: "PLATFORM_UNAVAILABLE", Message: "platform connection is unavailable"}
		case m := <-inbox:
			c.observe(m)
			if m.CorrelationID != correlationID {
				continue
			}
			if m.Type == protocol.TypeError {
				code := m.Code
				if code == "" {
					code = "PLATFORM_ERROR"
				}
				return m, &agenttools.Error{Code: code, Message: m.Message}
			}
			if match(m) {
				return m, nil
			}
		}
	}
}

func (c *Client) drainInbox(inbox chan protocol.Message) {
	for {
		select {
		case m := <-inbox:
			c.observe(m)
		default:
			return
		}
	}
}
