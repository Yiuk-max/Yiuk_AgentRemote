// Package platformclient implements the agenttools.Backend contract against
// the remote-assist platform. It keeps one authenticated operator connection
// per session so sessions are not interrupted between tool calls, caches
// streamed command output locally, and returns structured agenttools errors.
package platformclient

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"yiuk-switchboard/internal/agenttools"
	"yiuk-switchboard/internal/platform/protocol"
)

const configFileName = "platform-client.json"

// Config configures an operator connection to the platform.
type Config struct {
	Address            string `json:"address"`
	PlayerPassword     string `json:"player_password"`
	TLSCAFile          string `json:"tls_ca,omitempty"`
	InsecureSkipVerify bool   `json:"insecure_skip_verify,omitempty"`
	AllowPlaintext     bool   `json:"allow_plaintext,omitempty"`
	TimeoutSeconds     int    `json:"timeout_seconds,omitempty"`
}

// ConfigPath returns the per-user platform client configuration path.
func ConfigPath() (string, error) {
	root, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "Yiuk", "DeviceAssist", configFileName), nil
}

// NewFromUserConfig loads the platform client configuration. It returns an
// error when the file is missing or invalid so callers can fall back to the
// local bridge.
func NewFromUserConfig() (*Client, error) {
	path, err := ConfigPath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse platform client config: %w", err)
	}
	return New(cfg)
}

// New validates configuration and returns a lazy client. The connection is
// established on the first tool call.
func New(cfg Config) (*Client, error) {
	cfg.Address = strings.TrimSpace(cfg.Address)
	if cfg.Address == "" {
		return nil, errors.New("platform client address is required")
	}
	if len(cfg.PlayerPassword) < 16 {
		return nil, errors.New("platform client player_password must be at least 16 characters")
	}
	if cfg.TimeoutSeconds <= 0 {
		cfg.TimeoutSeconds = 30
	}
	return &Client{
		cfg:      cfg,
		codec:    protocol.NewCodec(protocol.DefaultMaxFrameBytes),
		sessions: make(map[string]protocol.SessionInfo),
		tasks:    make(map[string]*taskCache),
	}, nil
}

type taskCache struct {
	sessionID   string
	status      string
	exitCode    *int
	truncated   bool
	resultKnown bool
	outputBytes int
	chunks      []protocol.OutputChunk
}

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

	cacheMu  sync.Mutex
	sessions map[string]protocol.SessionInfo
	tasks    map[string]*taskCache
	devices  []protocol.DeviceInfo
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
	case agenttools.CancelCommand:
		return c.cancelCommand(ctx, req.Input)
	case agenttools.CloseSession:
		return c.closeSession(ctx, req.Input)
	default:
		return nil, &agenttools.Error{Code: "UNKNOWN_TOOL", Message: "unsupported tool"}
	}
}

func (c *Client) listDevices(ctx context.Context) (any, error) {
	resp, err := c.call(ctx, protocol.Message{Type: protocol.TypeListDevices}, func(m protocol.Message) bool {
		return m.Type == protocol.TypeDeviceList
	})
	if err != nil {
		return nil, err
	}
	return resp.Devices, nil
}

func (c *Client) getDeviceStatus(ctx context.Context, raw json.RawMessage) (any, error) {
	var input struct {
		DeviceID string `json:"device_id"`
	}
	if err := json.Unmarshal(raw, &input); err != nil {
		return nil, &agenttools.Error{Code: "INVALID_ARGUMENT", Message: "invalid arguments"}
	}
	resp, err := c.call(ctx, protocol.Message{Type: protocol.TypeListDevices}, func(m protocol.Message) bool {
		return m.Type == protocol.TypeDeviceList
	})
	if err != nil {
		return nil, err
	}
	for _, d := range resp.Devices {
		if d.DeviceID == input.DeviceID {
			return d, nil
		}
	}
	return nil, &agenttools.Error{Code: "DEVICE_NOT_FOUND", Message: "device is not registered"}
}

type connectionRequest struct {
	DeviceID          string
	Purpose           string
	Permissions       []string
	TemporaryPassword string
	WaitForApproval   time.Duration
}

func (c *Client) requestConnection(ctx context.Context, raw json.RawMessage) (any, error) {
	var input struct {
		DeviceID               string   `json:"device_id"`
		Purpose                string   `json:"purpose"`
		Permissions            []string `json:"permissions"`
		TemporaryPassword      string   `json:"temporary_password"`
		WaitForApprovalSeconds int      `json:"wait_for_approval_seconds"`
	}
	if err := json.Unmarshal(raw, &input); err != nil {
		return nil, &agenttools.Error{Code: "INVALID_ARGUMENT", Message: "invalid arguments"}
	}
	return c.createSession(ctx, connectionRequest{
		DeviceID:          input.DeviceID,
		Purpose:           input.Purpose,
		Permissions:       input.Permissions,
		TemporaryPassword: input.TemporaryPassword,
		WaitForApproval:   time.Duration(input.WaitForApprovalSeconds) * time.Second,
	})
}

// createSession requests a session and, when requested, waits for the device
// owner to approve it. Direct-mode devices are approved immediately.
func (c *Client) createSession(ctx context.Context, input connectionRequest) (map[string]any, error) {
	if input.TemporaryPassword == "" {
		return nil, &agenttools.Error{Code: "INVALID_ARGUMENT", Message: "temporary_password is required; ask the device owner for the 32-character temporary password"}
	}
	resp, err := c.call(ctx, protocol.Message{
		Type: protocol.TypeRequestSession, DeviceID: input.DeviceID, Purpose: input.Purpose,
		Permissions: input.Permissions, TemporaryPassword: input.TemporaryPassword,
	}, func(m protocol.Message) bool {
		return m.Type == protocol.TypeSessionCreated
	})
	if err != nil {
		return nil, err
	}
	result := sessionResult(resp)
	id := sessionIDFromResult(result)
	status, _ := result["status"].(string)
	if (status == protocol.StatusPendingApproval || status == protocol.StatusRequested) && input.WaitForApproval > 0 {
		if c.waitForApproval(ctx, id, input.WaitForApproval) {
			result["wait_timed_out"] = true
		} else if latest, err := c.getSessionStatusInternal(ctx, id); err == nil {
			for key, value := range latest {
				result[key] = value
			}
		}
	}
	return result, nil
}

func (c *Client) waitForApproval(ctx context.Context, sessionID string, timeout time.Duration) (timedOut bool) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if result, err := c.getSessionStatusInternal(ctx, sessionID); err == nil {
			if status, _ := result["status"].(string); status != protocol.StatusPendingApproval && status != protocol.StatusRequested {
				return false
			}
		}
		sleep := 1500 * time.Millisecond
		if remaining := time.Until(deadline); remaining < sleep {
			sleep = remaining
		}
		if sleep <= 0 {
			break
		}
		timer := time.NewTimer(sleep)
		select {
		case <-ctx.Done():
			timer.Stop()
			return true
		case <-timer.C:
		}
	}
	return true
}

func sessionIDFromResult(result map[string]any) string {
	id, _ := result["session_id"].(string)
	return id
}

func (c *Client) getSessionStatus(ctx context.Context, raw json.RawMessage) (any, error) {
	var input struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(raw, &input); err != nil {
		return nil, &agenttools.Error{Code: "INVALID_ARGUMENT", Message: "invalid arguments"}
	}
	return c.getSessionStatusInternal(ctx, input.SessionID)
}

func (c *Client) getSessionStatusInternal(ctx context.Context, sessionID string) (map[string]any, error) {
	resp, err := c.call(ctx, protocol.Message{Type: protocol.TypeSessionStatus, SessionID: sessionID}, func(m protocol.Message) bool {
		return m.Type == protocol.TypeSessionStatus
	})
	if err != nil {
		return nil, err
	}
	return sessionResult(resp), nil
}

func (c *Client) executeCommand(ctx context.Context, raw json.RawMessage) (any, error) {
	var input struct {
		SessionID        string `json:"session_id"`
		Command          string `json:"command"`
		WorkingDirectory string `json:"working_directory"`
		TimeoutSeconds   int    `json:"timeout_seconds"`
	}
	if err := json.Unmarshal(raw, &input); err != nil {
		return nil, &agenttools.Error{Code: "INVALID_ARGUMENT", Message: "invalid arguments"}
	}
	requestID, status, err := c.executeCommandInternal(ctx, input.SessionID, input.Command, input.WorkingDirectory, input.TimeoutSeconds)
	if err != nil {
		return nil, err
	}
	return map[string]any{"session_id": input.SessionID, "request_id": requestID, "status": status}, nil
}

func (c *Client) executeCommandInternal(ctx context.Context, sessionID, command, workingDirectory string, timeoutSeconds int) (string, string, error) {
	requestID := randomID()
	resp, err := c.call(ctx, protocol.Message{
		Type: protocol.TypeExecuteCommand, SessionID: sessionID, RequestID: requestID,
		Command: command, WorkingDirectory: workingDirectory, TimeoutSeconds: timeoutSeconds,
	}, func(m protocol.Message) bool {
		return m.Type == protocol.TypeCommandAccepted
	})
	if err != nil {
		return "", "", err
	}
	return requestID, resp.Status, nil
}

func (c *Client) getCommandResult(ctx context.Context, raw json.RawMessage) (any, error) {
	var input struct {
		SessionID string `json:"session_id"`
		RequestID string `json:"request_id"`
		Cursor    int    `json:"cursor"`
	}
	if err := json.Unmarshal(raw, &input); err != nil {
		return nil, &agenttools.Error{Code: "INVALID_ARGUMENT", Message: "invalid arguments"}
	}
	return c.getCommandResultInternal(ctx, input.SessionID, input.RequestID, input.Cursor)
}

func (c *Client) getCommandResultInternal(ctx context.Context, sessionID, requestID string, cursor int) (map[string]any, error) {
	if result, ok := c.cachedResult(sessionID, requestID, cursor); ok {
		return result, nil
	}
	resp, err := c.call(ctx, protocol.Message{
		Type: protocol.TypeGetCommandResult, SessionID: sessionID, RequestID: requestID, Cursor: cursor,
	}, func(m protocol.Message) bool {
		return m.Type == protocol.TypeCommandResult
	})
	if err != nil {
		return nil, err
	}
	return resultMap(resp), nil
}

// runCommand is the one-shot operator flow: request a session (optionally
// waiting for owner approval), execute one command, wait for its result, and
// close the session unless the caller asks to keep it open.
func (c *Client) runCommand(ctx context.Context, raw json.RawMessage) (any, error) {
	var input struct {
		DeviceID               string `json:"device_id"`
		TemporaryPassword      string `json:"temporary_password"`
		Command                string `json:"command"`
		Purpose                string `json:"purpose"`
		WorkingDirectory       string `json:"working_directory"`
		TimeoutSeconds         int    `json:"timeout_seconds"`
		WaitForApprovalSeconds int    `json:"wait_for_approval_seconds"`
		KeepSessionOpen        bool   `json:"keep_session_open"`
	}
	if err := json.Unmarshal(raw, &input); err != nil {
		return nil, &agenttools.Error{Code: "INVALID_ARGUMENT", Message: "invalid arguments"}
	}
	if input.Purpose == "" {
		input.Purpose = "run a remote command"
	}
	if input.TimeoutSeconds <= 0 {
		input.TimeoutSeconds = 60
	}
	budget := time.Duration(input.WaitForApprovalSeconds+input.TimeoutSeconds)*time.Second + 30*time.Second
	runCtx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()

	session, err := c.createSession(runCtx, connectionRequest{
		DeviceID:          input.DeviceID,
		Purpose:           input.Purpose,
		Permissions:       []string{"execute_command"},
		TemporaryPassword: input.TemporaryPassword,
		WaitForApproval:   time.Duration(input.WaitForApprovalSeconds) * time.Second,
	})
	if err != nil {
		return nil, err
	}
	id := sessionIDFromResult(session)
	authorization, _ := session["status"].(string)
	if authorization != protocol.StatusApproved && authorization != protocol.StatusActive {
		return map[string]any{
			"session_id": id, "device_id": input.DeviceID, "authorization": authorization,
			"executed": false, "message": "session was not approved; the command was not executed",
		}, nil
	}

	requestID, commandStatus, err := c.executeCommandInternal(runCtx, id, input.Command, input.WorkingDirectory, input.TimeoutSeconds)
	if err != nil {
		return nil, err
	}
	result, err := c.waitCommandResult(runCtx, id, requestID, time.Duration(input.TimeoutSeconds)*time.Second+15*time.Second)
	if err != nil {
		return nil, err
	}
	if !input.KeepSessionOpen {
		if _, closeErr := c.closeSessionInternal(runCtx, id); closeErr == nil {
			result["session_closed"] = true
		}
	}
	result["session_id"] = id
	result["device_id"] = input.DeviceID
	result["authorization"] = authorization
	result["executed"] = true
	if commandStatus != "" {
		result["initial_status"] = commandStatus
	}
	return result, nil
}

func (c *Client) waitCommandResult(ctx context.Context, sessionID, requestID string, timeout time.Duration) (map[string]any, error) {
	deadline := time.Now().Add(timeout)
	for {
		result, err := c.getCommandResultInternal(ctx, sessionID, requestID, 0)
		if err == nil {
			if status, _ := result["status"].(string); status != protocol.TaskRunning && status != protocol.TaskQueued && status != "" {
				return aggregateResult(result), nil
			}
			if time.Now().After(deadline) {
				result["status"] = protocol.TaskResultUnknown
				result["result_known"] = false
				result["message"] = "timed out waiting for command result"
				return aggregateResult(result), nil
			}
		} else if time.Now().After(deadline) {
			return nil, err
		}
		timer := time.NewTimer(750 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, &agenttools.Error{Code: "PLATFORM_TIMEOUT", Message: "command wait was canceled"}
		case <-timer.C:
		}
	}
}

// aggregateResult turns streamed chunks into single stdout/stderr strings for
// callers that do not want to page through output themselves.
func aggregateResult(result map[string]any) map[string]any {
	var stdout, stderr strings.Builder
	switch chunks := result["chunks"].(type) {
	case []protocol.OutputChunk:
		for _, chunk := range chunks {
			if chunk.Stream == "stderr" {
				stderr.WriteString(chunk.Data)
			} else {
				stdout.WriteString(chunk.Data)
			}
		}
	case []any:
		for _, raw := range chunks {
			entry, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			data, _ := entry["data"].(string)
			if stream, _ := entry["stream"].(string); stream == "stderr" {
				stderr.WriteString(data)
			} else {
				stdout.WriteString(data)
			}
		}
	}
	exitCode := result["exit_code"]
	if pointer, ok := exitCode.(*int); ok && pointer != nil {
		exitCode = *pointer
	}
	return map[string]any{
		"session_id":   result["session_id"],
		"request_id":   result["request_id"],
		"status":       result["status"],
		"exit_code":    exitCode,
		"stdout":       stdout.String(),
		"stderr":       stderr.String(),
		"truncated":    result["truncated"],
		"result_known": result["result_known"],
		"output_bytes": result["output_bytes"],
	}
}

func (c *Client) cancelCommand(ctx context.Context, raw json.RawMessage) (any, error) {
	var input struct {
		SessionID string `json:"session_id"`
		RequestID string `json:"request_id"`
	}
	if err := json.Unmarshal(raw, &input); err != nil {
		return nil, &agenttools.Error{Code: "INVALID_ARGUMENT", Message: "invalid arguments"}
	}
	resp, err := c.call(ctx, protocol.Message{
		Type: protocol.TypeCancelCommand, SessionID: input.SessionID, RequestID: input.RequestID,
	}, func(m protocol.Message) bool {
		return m.Type == protocol.TypeCommandCanceled
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"session_id": input.SessionID, "request_id": input.RequestID, "status": resp.Status}, nil
}

func (c *Client) closeSession(ctx context.Context, raw json.RawMessage) (any, error) {
	var input struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(raw, &input); err != nil {
		return nil, &agenttools.Error{Code: "INVALID_ARGUMENT", Message: "invalid arguments"}
	}
	return c.closeSessionInternal(ctx, input.SessionID)
}

func (c *Client) closeSessionInternal(ctx context.Context, sessionID string) (map[string]any, error) {
	resp, err := c.call(ctx, protocol.Message{Type: protocol.TypeCloseSession, SessionID: sessionID}, func(m protocol.Message) bool {
		return m.Type == protocol.TypeSessionStatus && isTerminalSession(m.Status)
	})
	if err != nil {
		return nil, err
	}
	return sessionResult(resp), nil
}

func (c *Client) call(ctx context.Context, req protocol.Message, match func(protocol.Message) bool) (protocol.Message, error) {
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

	timeout := time.Duration(c.cfg.TimeoutSeconds) * time.Second
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
		return nil, nil, "", &agenttools.Error{Code: "PLATFORM_UNAVAILABLE", Message: "could not reach platform"}
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
	if c.cfg.TLSCAFile != "" {
		pool, err := loadCertPool(c.cfg.TLSCAFile)
		if err != nil {
			return nil, err
		}
		cfg.RootCAs = pool
	}
	dialer := &net.Dialer{Timeout: 5 * time.Second}
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

func (c *Client) observe(m protocol.Message) {
	switch m.Type {
	case protocol.TypeSessionCreated, protocol.TypeSessionStatus, protocol.TypeSessionRequest:
		if m.Session != nil {
			c.cacheMu.Lock()
			c.sessions[m.Session.SessionID] = *m.Session
			c.cacheMu.Unlock()
		}
	case protocol.TypeCommandOutput:
		c.observeOutput(m)
	case protocol.TypeCommandResult:
		c.observeResult(m)
	case protocol.TypeDeviceList:
		c.cacheMu.Lock()
		c.devices = append([]protocol.DeviceInfo(nil), m.Devices...)
		c.cacheMu.Unlock()
	}
}

func (c *Client) observeOutput(m protocol.Message) {
	c.cacheMu.Lock()
	defer c.cacheMu.Unlock()
	t := c.task(m.SessionID, m.RequestID)
	if m.Data != "" {
		last := uint64(0)
		if len(t.chunks) > 0 {
			last = t.chunks[len(t.chunks)-1].Sequence
		}
		if m.Sequence > last {
			t.chunks = append(t.chunks, protocol.OutputChunk{Stream: m.Stream, Sequence: m.Sequence, Data: m.Data})
		}
	}
	t.truncated = t.truncated || m.Truncated
	if m.OutputBytes > 0 {
		t.outputBytes = m.OutputBytes
	} else if m.Data != "" {
		t.outputBytes += len(m.Data)
	}
}

func (c *Client) observeResult(m protocol.Message) {
	c.cacheMu.Lock()
	defer c.cacheMu.Unlock()
	t := c.task(m.SessionID, m.RequestID)
	last := uint64(0)
	if len(t.chunks) > 0 {
		last = t.chunks[len(t.chunks)-1].Sequence
	}
	for _, ch := range m.Chunks {
		if ch.Sequence > last {
			t.chunks = append(t.chunks, ch)
			last = ch.Sequence
		}
	}
	t.status = m.Status
	t.exitCode = m.ExitCode
	t.truncated = t.truncated || m.Truncated
	t.resultKnown = m.ResultKnown
	if m.OutputBytes > t.outputBytes {
		t.outputBytes = m.OutputBytes
	}
}

// task must be called with cacheMu held.
func (c *Client) task(sessionID, requestID string) *taskCache {
	t, ok := c.tasks[requestID]
	if !ok || t.sessionID != sessionID {
		t = &taskCache{sessionID: sessionID, status: string(protocol.TaskRunning)}
		c.tasks[requestID] = t
	}
	return t
}

func (c *Client) cachedResult(sessionID, requestID string, cursor int) (map[string]any, bool) {
	c.cacheMu.Lock()
	defer c.cacheMu.Unlock()
	t, ok := c.tasks[requestID]
	if !ok || t.sessionID != sessionID || !isTerminalTaskStatus(t.status) {
		return nil, false
	}
	if cursor < 0 {
		cursor = 0
	}
	if cursor > len(t.chunks) {
		cursor = len(t.chunks)
	}
	chunks := append([]protocol.OutputChunk(nil), t.chunks[cursor:]...)
	return map[string]any{
		"session_id":   sessionID,
		"request_id":   requestID,
		"status":       t.status,
		"exit_code":    t.exitCode,
		"truncated":    t.truncated,
		"result_known": t.resultKnown,
		"output_bytes": t.outputBytes,
		"chunks":       chunks,
		"next_cursor":  len(t.chunks),
	}, true
}

func sessionResult(m protocol.Message) map[string]any {
	if m.Session != nil {
		return map[string]any{
			"session_id":      m.Session.SessionID,
			"device_id":       m.Session.DeviceID,
			"status":          m.Session.Status,
			"mode":            m.Session.Mode,
			"purpose":         m.Session.Purpose,
			"permissions":     m.Session.Permissions,
			"expires_at":      m.Session.ExpiresAt,
			"created_at":      m.Session.CreatedAt,
			"close_reason":    m.Session.CloseReason,
			"operator":        m.Session.Operator,
			"operator_device": m.Session.OperatorDevice,
		}
	}
	return map[string]any{"session_id": m.SessionID, "device_id": m.DeviceID, "status": m.Status}
}

func resultMap(m protocol.Message) map[string]any {
	return map[string]any{
		"session_id":   m.SessionID,
		"request_id":   m.RequestID,
		"status":       m.Status,
		"exit_code":    m.ExitCode,
		"truncated":    m.Truncated,
		"result_known": m.ResultKnown,
		"output_bytes": m.OutputBytes,
		"chunks":       m.Chunks,
		"next_cursor":  m.NextCursor,
	}
}

func isTerminalTaskStatus(status string) bool {
	switch status {
	case string(protocol.TaskSucceeded), string(protocol.TaskFailed), string(protocol.TaskTimedOut), string(protocol.TaskCancelled), string(protocol.TaskResultUnknown):
		return true
	default:
		return false
	}
}

func isTerminalSession(status string) bool {
	switch status {
	case protocol.StatusClosed, protocol.StatusRejected, protocol.StatusExpired, protocol.StatusInterrupted:
		return true
	default:
		return false
	}
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
