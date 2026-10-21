package server_test

import (
	"bufio"
	"context"
	"io"
	"log/slog"
	"net"
	"strings"
	"testing"
	"time"

	"yiuk-switchboard/internal/platform/config"
	"yiuk-switchboard/internal/platform/protocol"
	"yiuk-switchboard/internal/platform/server"
)

const testPlayerPassword = "test-player-password-1234567890"
const testDevicePassword = "0123456789abcdef0123456789abcdef"

type testClient struct {
	t       *testing.T
	conn    net.Conn
	reader  *bufio.Reader
	codec   protocol.Codec
	pending []protocol.Message
}

func startServer(t *testing.T, mutate func(*config.Config)) (string, *server.Server) {
	t.Helper()
	cfg := config.Default()
	cfg.Listen = "127.0.0.1:0"
	cfg.AllowInsecure = true
	cfg.PlayerPassword = testPlayerPassword
	cfg.MaxOutputBytes = 1024
	cfg.HeartbeatTimeoutSec = 30
	cfg.HandshakeTimeoutSec = 5
	if mutate != nil {
		mutate(&cfg)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("test config: %v", err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := server.NewWithListener(cfg, logger, ln)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = srv.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		srv.Close()
		<-done
	})
	return ln.Addr().String(), srv
}

func dialClient(t *testing.T, address string) *testClient {
	t.Helper()
	conn, err := net.DialTimeout("tcp", address, 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	c := &testClient{t: t, conn: conn, reader: bufio.NewReader(conn), codec: protocol.NewCodec(protocol.DefaultMaxFrameBytes)}
	t.Cleanup(func() { _ = conn.Close() })
	return c
}

func (c *testClient) send(m protocol.Message) {
	c.t.Helper()
	if m.Version == 0 {
		m.Version = protocol.Version
	}
	if m.BusinessType == "" {
		m.BusinessType = protocol.BusinessAgentAssist
	}
	if err := c.codec.Write(c.conn, m); err != nil {
		c.t.Fatalf("send %s: %v", m.Type, err)
	}
}

func (c *testClient) recv(want ...string) protocol.Message {
	c.t.Helper()
	for i, m := range c.pending {
		if matches(m.Type, want) {
			c.pending = append(c.pending[:i], c.pending[i+1:]...)
			return m
		}
	}
	for {
		_ = c.conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		m, err := c.codec.Read(c.reader)
		if err != nil {
			c.t.Fatalf("receive %v: %v", want, err)
		}
		if matches(m.Type, want) {
			return m
		}
		c.pending = append(c.pending, m)
	}
}

func (c *testClient) recvWhere(want string, ok func(protocol.Message) bool) protocol.Message {
	c.t.Helper()
	for {
		m := c.recv(want)
		if ok(m) {
			return m
		}
	}
}

func (c *testClient) expectNone(d time.Duration) {
	c.t.Helper()
	for _, m := range c.pending {
		if m.Type != protocol.TypePong {
			c.t.Fatalf("unexpected pending message %s", m.Type)
		}
	}
	_ = c.conn.SetReadDeadline(time.Now().Add(d))
	m, err := c.codec.Read(c.reader)
	if err == nil {
		c.t.Fatalf("unexpected message %s", m.Type)
	}
}

func (c *testClient) auth(password string) {
	c.t.Helper()
	c.send(protocol.Message{Type: protocol.TypeAuthenticatePlayer, IdentityType: "player", PlayerPassword: password})
	c.recv(protocol.TypeAuthenticated)
}

func (c *testClient) register(deviceID, password, mode string) protocol.Message {
	c.t.Helper()
	c.send(protocol.Message{
		Type: protocol.TypeRegisterDevice, DeviceID: deviceID, DeviceName: "test-device",
		Platform: "windows", Mode: mode, TemporaryPassword: password,
	})
	return c.recv(protocol.TypeDeviceRegistered)
}

func matches(got string, want []string) bool {
	if len(want) == 0 {
		return true
	}
	for _, w := range want {
		if got == w {
			return true
		}
	}
	return false
}

func TestApprovalSessionCommandFlow(t *testing.T) {
	address, _ := startServer(t, nil)
	device := dialClient(t, address)
	device.auth(testPlayerPassword)
	registered := device.register("device-1", testDevicePassword, protocol.ModeApproval)
	if registered.Status != "" {
		t.Fatalf("unexpected registration status: %#v", registered)
	}
	device.send(protocol.Message{Type: protocol.TypeDeviceHeartbeat})
	ack := device.recv(protocol.TypeHeartbeatAck)
	if ack.ActiveSessions != 0 {
		t.Fatalf("active sessions = %d, want 0", ack.ActiveSessions)
	}

	operator := dialClient(t, address)
	operator.auth(testPlayerPassword)
	operator.send(protocol.Message{Type: protocol.TypeListDevices})
	list := operator.recv(protocol.TypeDeviceList)
	if len(list.Devices) != 1 || list.Devices[0].DeviceID != "device-1" || !list.Devices[0].Online {
		t.Fatalf("unexpected device list: %#v", list.Devices)
	}

	operator.send(protocol.Message{
		Type: protocol.TypeRequestSession, DeviceID: "device-1", TemporaryPassword: testDevicePassword,
		Purpose: "fix the printer", Permissions: []string{"execute_command"},
	})
	created := operator.recv(protocol.TypeSessionCreated)
	if created.Status != protocol.StatusPendingApproval || created.SessionID == "" {
		t.Fatalf("unexpected created session: %#v", created)
	}
	sessionID := created.SessionID
	request := device.recv(protocol.TypeSessionRequest)
	if request.Session == nil || request.Session.Purpose != "fix the printer" {
		t.Fatalf("unexpected approval request: %#v", request)
	}

	// A pending session must not execute commands.
	operator.send(protocol.Message{
		Type: protocol.TypeExecuteCommand, SessionID: sessionID, RequestID: "req-before-approval", Command: "whoami",
	})
	errMsg := operator.recv(protocol.TypeError)
	if errMsg.Code != "INVALID_SESSION_STATE" {
		t.Fatalf("pending execute code = %s, want INVALID_SESSION_STATE", errMsg.Code)
	}

	device.send(protocol.Message{Type: protocol.TypeApproveSession, SessionID: sessionID})
	approved := operator.recv(protocol.TypeSessionStatus)
	if approved.Status != protocol.StatusApproved {
		t.Fatalf("operator status = %s, want approved", approved.Status)
	}
	device.recv(protocol.TypeSessionStatus)

	operator.send(protocol.Message{
		Type: protocol.TypeExecuteCommand, SessionID: sessionID, RequestID: "req-1",
		Command: "Get-ChildItem", WorkingDirectory: "C:\\", TimeoutSeconds: 30,
	})
	accepted := operator.recv(protocol.TypeCommandAccepted)
	if accepted.RequestID != "req-1" || accepted.Status != protocol.TaskRunning {
		t.Fatalf("unexpected accepted message: %#v", accepted)
	}
	forwarded := device.recv(protocol.TypeExecuteCommand)
	if forwarded.RequestID != "req-1" || forwarded.Command != "Get-ChildItem" || forwarded.TimeoutSeconds != 30 {
		t.Fatalf("unexpected forwarded command: %#v", forwarded)
	}

	device.send(protocol.Message{
		Type: protocol.TypeCommandOutput, SessionID: sessionID, RequestID: "req-1", Stream: "stdout", Data: "hello\n",
	})
	output := operator.recv(protocol.TypeCommandOutput)
	if output.Data != "hello\n" || output.Sequence == 0 {
		t.Fatalf("unexpected output: %#v", output)
	}

	exit := 0
	device.send(protocol.Message{
		Type: protocol.TypeCommandResult, SessionID: sessionID, RequestID: "req-1",
		Status: string(protocol.TaskSucceeded), ExitCode: &exit, ResultKnown: true,
	})
	result := operator.recv(protocol.TypeCommandResult)
	if result.Status != string(protocol.TaskSucceeded) || result.ExitCode == nil || *result.ExitCode != 0 {
		t.Fatalf("unexpected result: %#v", result)
	}

	operator.send(protocol.Message{Type: protocol.TypeGetCommandResult, SessionID: sessionID, RequestID: "req-1", Cursor: 0})
	paged := operator.recvWhere(protocol.TypeCommandResult, func(m protocol.Message) bool { return len(m.Chunks) > 0 })
	if len(paged.Chunks) != 1 || paged.Chunks[0].Data != "hello\n" || paged.NextCursor != 1 {
		t.Fatalf("unexpected paged result: %#v", paged)
	}

	operator.send(protocol.Message{Type: protocol.TypeCloseSession, SessionID: sessionID})
	closed := operator.recv(protocol.TypeSessionStatus)
	if closed.Status != protocol.StatusClosed {
		t.Fatalf("closed status = %s, want closed", closed.Status)
	}
	device.recv(protocol.TypeCloseSession)
}

func TestDirectModeAndOperatorBinding(t *testing.T) {
	address, _ := startServer(t, nil)
	device := dialClient(t, address)
	device.auth(testPlayerPassword)
	device.register("device-direct", testDevicePassword, protocol.ModeDirect)

	operator1 := dialClient(t, address)
	operator1.auth(testPlayerPassword)
	operator1.send(protocol.Message{
		Type: protocol.TypeRequestSession, DeviceID: "device-direct", TemporaryPassword: testDevicePassword,
		Purpose: "direct help", Permissions: []string{"execute_command"},
	})
	created := operator1.recv(protocol.TypeSessionCreated)
	if created.Status != protocol.StatusApproved {
		t.Fatalf("direct status = %s, want approved", created.Status)
	}
	sessionID := created.SessionID
	device.recv(protocol.TypeSessionStatus)

	operator2 := dialClient(t, address)
	operator2.auth(testPlayerPassword)
	operator2.send(protocol.Message{
		Type: protocol.TypeExecuteCommand, SessionID: sessionID, RequestID: "stolen", Command: "whoami",
	})
	errMsg := operator2.recv(protocol.TypeError)
	if errMsg.Code != "PERMISSION_DENIED" {
		t.Fatalf("cross-operator code = %s, want PERMISSION_DENIED", errMsg.Code)
	}
	device.expectNone(150 * time.Millisecond)

	operator1.send(protocol.Message{
		Type: protocol.TypeExecuteCommand, SessionID: sessionID, RequestID: "direct-1", Command: "whoami",
	})
	operator1.recv(protocol.TypeCommandAccepted)
	device.recv(protocol.TypeExecuteCommand)
}

func TestExpiredDevicePasswordCanRecoverWithSameDeviceID(t *testing.T) {
	address, _ := startServer(t, func(cfg *config.Config) {
		cfg.TempPasswordTTLSec = 1
		cfg.TemporaryPasswordMin = 1
		cfg.TemporaryPasswordMax = 60
	})

	first := dialClient(t, address)
	first.auth(testPlayerPassword)
	registered := first.register("device-expired", testDevicePassword, protocol.ModeApproval)
	if registered.PasswordVersion != 1 {
		t.Fatalf("initial password version = %d, want 1", registered.PasswordVersion)
	}
	_ = first.conn.Close()
	time.Sleep(1100 * time.Millisecond)

	reconnect := dialClient(t, address)
	reconnect.auth(testPlayerPassword)
	recovered := reconnect.register("device-expired", testDevicePassword, protocol.ModeApproval)
	if recovered.TemporaryPassword == "" {
		t.Fatal("server did not issue a replacement password")
	}
	if recovered.PasswordVersion != 2 {
		t.Fatalf("recovered password version = %d, want 2", recovered.PasswordVersion)
	}

	operator := dialClient(t, address)
	operator.auth(testPlayerPassword)
	operator.send(protocol.Message{Type: protocol.TypeListDevices})
	devices := operator.recv(protocol.TypeDeviceList)
	if len(devices.Devices) != 1 || devices.Devices[0].DeviceID != "device-expired" || !devices.Devices[0].Online {
		t.Fatalf("recovered device not online: %#v", devices.Devices)
	}
}

func TestWrongPlayerPasswordAndAuthRequired(t *testing.T) {
	address, _ := startServer(t, nil)

	anonymous := dialClient(t, address)
	anonymous.send(protocol.Message{Type: protocol.TypeRegisterDevice, DeviceID: "d", TemporaryPassword: testDevicePassword})
	errMsg := anonymous.recv(protocol.TypeError)
	if errMsg.Code != "AUTH_REQUIRED" {
		t.Fatalf("anonymous code = %s, want AUTH_REQUIRED", errMsg.Code)
	}

	bad := dialClient(t, address)
	bad.send(protocol.Message{Type: protocol.TypeAuthenticatePlayer, IdentityType: "player", PlayerPassword: "wrong"})
	errMsg = bad.recv(protocol.TypeError)
	if errMsg.Code != "BAD_PASSWORD" {
		t.Fatalf("bad password code = %s, want BAD_PASSWORD", errMsg.Code)
	}
}

func TestTemporaryPasswordRotationRevokesSessions(t *testing.T) {
	address, _ := startServer(t, nil)
	device := dialClient(t, address)
	device.auth(testPlayerPassword)
	device.register("device-rotate", testDevicePassword, protocol.ModeApproval)

	operator := dialClient(t, address)
	operator.auth(testPlayerPassword)
	operator.send(protocol.Message{
		Type: protocol.TypeRequestSession, DeviceID: "device-rotate", TemporaryPassword: testDevicePassword,
		Purpose: "rotate", Permissions: []string{"execute_command"},
	})
	created := operator.recv(protocol.TypeSessionCreated)
	device.recv(protocol.TypeSessionRequest)
	device.send(protocol.Message{Type: protocol.TypeApproveSession, SessionID: created.SessionID})
	operator.recv(protocol.TypeSessionStatus)
	device.recv(protocol.TypeSessionStatus)

	newPassword := strings.Repeat("n", 32)
	device.send(protocol.Message{
		Type: protocol.TypeUpdateTemporaryPassword, TemporaryPassword: newPassword, PasswordVersion: 2, Mode: protocol.ModeApproval,
	})
	updated := device.recv(protocol.TypeTemporaryPasswordUpdated)
	if updated.PasswordVersion != 2 {
		t.Fatalf("password version = %d, want 2", updated.PasswordVersion)
	}
	revoked := operator.recv(protocol.TypeSessionStatus)
	if revoked.Status != protocol.StatusInterrupted {
		t.Fatalf("revoked status = %s, want interrupted", revoked.Status)
	}

	operator.send(protocol.Message{
		Type: protocol.TypeExecuteCommand, SessionID: created.SessionID, RequestID: "after-rotate", Command: "whoami",
	})
	errMsg := operator.recv(protocol.TypeError)
	if errMsg.Code != "INVALID_SESSION_STATE" {
		t.Fatalf("after rotation code = %s, want INVALID_SESSION_STATE", errMsg.Code)
	}

	// The old password no longer works; the new one does.
	operator2 := dialClient(t, address)
	operator2.auth(testPlayerPassword)
	operator2.send(protocol.Message{
		Type: protocol.TypeRequestSession, DeviceID: "device-rotate", TemporaryPassword: testDevicePassword,
		Purpose: "old", Permissions: []string{"execute_command"},
	})
	if errMsg := operator2.recv(protocol.TypeError); errMsg.Code != "BAD_TEMPORARY_PASSWORD" {
		t.Fatalf("old password code = %s, want BAD_TEMPORARY_PASSWORD", errMsg.Code)
	}
	operator2.send(protocol.Message{
		Type: protocol.TypeRequestSession, DeviceID: "device-rotate", TemporaryPassword: newPassword,
		Purpose: "new", Permissions: []string{"execute_command"},
	})
	if created := operator2.recv(protocol.TypeSessionCreated); created.Status != protocol.StatusPendingApproval {
		t.Fatalf("new password status = %s, want pending_approval", created.Status)
	}
}

func TestFileDownloadRequiresReadFilePermissionAndStreamsChunks(t *testing.T) {
	address, _ := startServer(t, func(cfg *config.Config) { cfg.MaxDownloadBytes = 1024 })
	device := dialClient(t, address)
	device.auth(testPlayerPassword)
	device.register("device-files", testDevicePassword, protocol.ModeDirect)

	operator := dialClient(t, address)
	operator.auth(testPlayerPassword)
	operator.send(protocol.Message{
		Type: protocol.TypeRequestSession, DeviceID: "device-files", TemporaryPassword: testDevicePassword,
		Purpose: "commands only", Permissions: []string{"execute_command"},
	})
	created := operator.recv(protocol.TypeSessionCreated)
	device.recv(protocol.TypeSessionStatus)
	operator.send(protocol.Message{Type: protocol.TypeDownloadFile, SessionID: created.SessionID, RequestID: "file-denied", RemotePath: `C:\secret.txt`})
	if errMsg := operator.recv(protocol.TypeError); errMsg.Code != "PERMISSION_DENIED" {
		t.Fatalf("download without read_file = %s, want PERMISSION_DENIED", errMsg.Code)
	}
	device.expectNone(100 * time.Millisecond)

	operator.send(protocol.Message{
		Type: protocol.TypeRequestSession, DeviceID: "device-files", TemporaryPassword: testDevicePassword,
		Purpose: "read logs", Permissions: []string{"read_file"},
	})
	readSession := operator.recv(protocol.TypeSessionCreated)
	device.recv(protocol.TypeSessionStatus)
	operator.send(protocol.Message{Type: protocol.TypeDownloadFile, SessionID: readSession.SessionID, RequestID: "file-1", RemotePath: `C:\logs\app.log`})
	forwarded := device.recv(protocol.TypeDownloadFile)
	if forwarded.RequestID != "file-1" || forwarded.RemotePath != `C:\logs\app.log` {
		t.Fatalf("unexpected forwarded file request: %#v", forwarded)
	}

	device.send(protocol.Message{Type: protocol.TypeFileChunk, SessionID: readSession.SessionID, RequestID: "file-1", Sequence: 2, Data: "bG9n"})
	if errMsg := device.recv(protocol.TypeError); errMsg.Code != "INVALID_CHUNK_SEQUENCE" {
		t.Fatalf("out-of-order chunk = %s, want INVALID_CHUNK_SEQUENCE", errMsg.Code)
	}
	device.send(protocol.Message{Type: protocol.TypeFileChunk, SessionID: readSession.SessionID, RequestID: "file-1", Sequence: 1, Data: "aGVsbG8="})
	chunk := operator.recvWhere(protocol.TypeFileChunk, func(m protocol.Message) bool { return m.RequestID == "file-1" })
	if chunk.Data != "aGVsbG8=" || chunk.Sequence != 1 {
		t.Fatalf("unexpected forwarded chunk: %#v", chunk)
	}
	device.send(protocol.Message{
		Type: protocol.TypeFileDownloadResult, SessionID: readSession.SessionID, RequestID: "file-1",
		Status: string(protocol.TaskSucceeded), FileName: "app.log", FileSize: 5, FileSHA256: "checksum", ResultKnown: true,
	})
	result := operator.recvWhere(protocol.TypeFileDownloadResult, func(m protocol.Message) bool { return m.RequestID == "file-1" })
	if result.Status != string(protocol.TaskSucceeded) || result.FileSize != 5 || result.FileSHA256 != "checksum" {
		t.Fatalf("unexpected file result: %#v", result)
	}
}

func TestOutputTruncation(t *testing.T) {
	address, _ := startServer(t, func(c *config.Config) { c.MaxOutputBytes = 1024 })
	device := dialClient(t, address)
	device.auth(testPlayerPassword)
	device.register("device-output", testDevicePassword, protocol.ModeDirect)
	operator := dialClient(t, address)
	operator.auth(testPlayerPassword)
	operator.send(protocol.Message{
		Type: protocol.TypeRequestSession, DeviceID: "device-output", TemporaryPassword: testDevicePassword,
		Purpose: "output", Permissions: []string{"execute_command"},
	})
	created := operator.recv(protocol.TypeSessionCreated)
	device.recv(protocol.TypeSessionStatus)
	operator.send(protocol.Message{
		Type: protocol.TypeExecuteCommand, SessionID: created.SessionID, RequestID: "big", Command: "dir",
	})
	operator.recv(protocol.TypeCommandAccepted)
	device.recv(protocol.TypeExecuteCommand)
	device.send(protocol.Message{
		Type: protocol.TypeCommandOutput, SessionID: created.SessionID, RequestID: "big", Stream: "stdout",
		Data: strings.Repeat("x", 2048),
	})
	output := operator.recv(protocol.TypeCommandOutput)
	if !output.Truncated || output.OutputBytes != 1024 {
		t.Fatalf("unexpected truncated output: %#v", output)
	}
	operator.send(protocol.Message{Type: protocol.TypeGetCommandResult, SessionID: created.SessionID, RequestID: "big"})
	paged := operator.recvWhere(protocol.TypeCommandResult, func(m protocol.Message) bool { return len(m.Chunks) > 0 })
	if len(paged.Chunks) != 1 || len(paged.Chunks[0].Data) != 1024 || !paged.Truncated {
		t.Fatalf("unexpected paged output: %#v", paged)
	}
}
