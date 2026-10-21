package platformclient_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"

	"yiuk-switchboard/internal/agenttools"
	"yiuk-switchboard/internal/platform/config"
	"yiuk-switchboard/internal/platform/protocol"
	"yiuk-switchboard/internal/platform/server"
	"yiuk-switchboard/internal/platformclient"
)

const playerPassword = "client-test-player-password"
const devicePassword = "0123456789abcdef0123456789abcdef"

type fakeDevice struct {
	t       *testing.T
	conn    net.Conn
	reader  *bufio.Reader
	codec   protocol.Codec
	pending []protocol.Message
}

func startPlatform(t *testing.T) string {
	t.Helper()
	cfg := config.Default()
	cfg.Listen = "127.0.0.1:0"
	cfg.AllowInsecure = true
	cfg.PlayerPassword = playerPassword
	cfg.MaxOutputBytes = 1 << 20
	cfg.HeartbeatTimeoutSec = 30
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := server.NewWithListener(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), ln)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = srv.Run(ctx) }()
	t.Cleanup(func() { cancel(); srv.Close(); <-done })
	return ln.Addr().String()
}

func dialFakeDevice(t *testing.T, address string) *fakeDevice {
	t.Helper()
	conn, err := net.DialTimeout("tcp", address, 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	d := &fakeDevice{t: t, conn: conn, reader: bufio.NewReader(conn), codec: protocol.NewCodec(protocol.DefaultMaxFrameBytes)}
	t.Cleanup(func() { _ = conn.Close() })
	return d
}

func (d *fakeDevice) send(m protocol.Message) {
	d.t.Helper()
	m.Version = protocol.Version
	m.BusinessType = protocol.BusinessAgentAssist
	if err := d.codec.Write(d.conn, m); err != nil {
		d.t.Fatalf("send %s: %v", m.Type, err)
	}
}

func (d *fakeDevice) recv(want ...string) protocol.Message {
	d.t.Helper()
	for i, m := range d.pending {
		if matches(m.Type, want) {
			d.pending = append(d.pending[:i], d.pending[i+1:]...)
			return m
		}
	}
	for {
		_ = d.conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		m, err := d.codec.Read(d.reader)
		if err != nil {
			d.t.Fatalf("receive %v: %v", want, err)
		}
		if matches(m.Type, want) {
			return m
		}
		d.pending = append(d.pending, m)
	}
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

func (d *fakeDevice) register(id string) {
	d.registerWithMode(id, protocol.ModeDirect)
}

func (d *fakeDevice) registerWithMode(id, mode string) {
	d.t.Helper()
	d.send(protocol.Message{Type: protocol.TypeAuthenticatePlayer, IdentityType: "player", PlayerPassword: playerPassword})
	d.recv(protocol.TypeAuthenticated)
	d.send(protocol.Message{
		Type: protocol.TypeRegisterDevice, DeviceID: id, DeviceName: "fake", Platform: "windows",
		Mode: mode, TemporaryPassword: devicePassword,
	})
	d.recv(protocol.TypeDeviceRegistered)
}

func invoke(t *testing.T, backend *platformclient.Client, name string, input any) any {
	t.Helper()
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	data, err := backend.Invoke(context.Background(), agenttools.Request{Name: name, Input: raw})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return data
}

func TestPlatformClientDirectSessionFlow(t *testing.T) {
	address := startPlatform(t)
	device := dialFakeDevice(t, address)
	device.register("device-1")

	backend, err := platformclient.New(platformclient.Config{
		Address: address, PlayerPassword: playerPassword, AllowPlaintext: true, TimeoutSeconds: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()

	devices := invoke(t, backend, agenttools.ListDevices, map[string]any{}).([]protocol.DeviceInfo)
	if len(devices) != 1 || devices[0].DeviceID != "device-1" || !devices[0].Online {
		t.Fatalf("unexpected devices: %#v", devices)
	}

	status := invoke(t, backend, agenttools.GetDeviceStatus, map[string]any{"device_id": "device-1"}).(protocol.DeviceInfo)
	if status.DeviceID != "device-1" {
		t.Fatalf("unexpected status: %#v", status)
	}

	session := invoke(t, backend, agenttools.RequestConnection, map[string]any{
		"device_id": "device-1", "purpose": "fix", "permissions": []string{"execute_command"},
		"temporary_password": devicePassword,
	}).(map[string]any)
	sessionID, _ := session["session_id"].(string)
	if sessionID == "" || session["status"] != protocol.StatusApproved {
		t.Fatalf("unexpected session: %#v", session)
	}
	device.recv(protocol.TypeSessionStatus)

	command := invoke(t, backend, agenttools.ExecuteCommand, map[string]any{
		"session_id": sessionID, "command": "whoami", "timeout_seconds": 30,
	}).(map[string]any)
	requestID, _ := command["request_id"].(string)
	if requestID == "" || command["status"] != protocol.TaskRunning {
		t.Fatalf("unexpected command: %#v", command)
	}
	forwarded := device.recv(protocol.TypeExecuteCommand)
	if forwarded.RequestID != requestID || forwarded.Command != "whoami" {
		t.Fatalf("unexpected forwarded command: %#v", forwarded)
	}

	device.send(protocol.Message{
		Type: protocol.TypeCommandOutput, SessionID: sessionID, RequestID: requestID, Stream: "stdout", Data: "ok\n",
	})
	exit := 0
	device.send(protocol.Message{
		Type: protocol.TypeCommandResult, SessionID: sessionID, RequestID: requestID,
		Status: string(protocol.TaskSucceeded), ExitCode: &exit, ResultKnown: true,
	})

	// Poll until the result is available; the first poll may race with the
	// streamed output notification.
	var result map[string]any
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		got := invoke(t, backend, agenttools.GetCommandResult, map[string]any{
			"session_id": sessionID, "request_id": requestID, "cursor": 0,
		}).(map[string]any)
		if got["status"] == string(protocol.TaskSucceeded) {
			result = got
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if result == nil {
		t.Fatal("command result was not available")
	}
	if result["status"] != string(protocol.TaskSucceeded) {
		t.Fatalf("unexpected result: %#v", result)
	}

	invoke(t, backend, agenttools.CloseSession, map[string]any{"session_id": sessionID})
	device.recv(protocol.TypeCloseSession)
	device.recv(protocol.TypeSessionStatus)
}

func TestPlatformClientRejectsMissingTemporaryPassword(t *testing.T) {
	address := startPlatform(t)
	device := dialFakeDevice(t, address)
	device.register("device-2")

	backend, err := platformclient.New(platformclient.Config{
		Address: address, PlayerPassword: playerPassword, AllowPlaintext: true, TimeoutSeconds: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()

	raw := json.RawMessage(`{"device_id":"device-2","purpose":"x","permissions":["execute_command"]}`)
	_, err = backend.Invoke(context.Background(), agenttools.Request{Name: agenttools.RequestConnection, Input: raw})
	var toolErr *agenttools.Error
	if err == nil || !errors.As(err, &toolErr) || toolErr.Code != "INVALID_ARGUMENT" {
		t.Fatalf("got %v, want INVALID_ARGUMENT", err)
	}
}

func TestPlatformClientRunCommand(t *testing.T) {
	address := startPlatform(t)
	device := dialFakeDevice(t, address)
	device.register("device-run")

	backend, err := platformclient.New(platformclient.Config{
		Address: address, PlayerPassword: playerPassword, AllowPlaintext: true, TimeoutSeconds: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()

	type outcome struct {
		data any
		err  error
	}
	done := make(chan outcome, 1)
	go func() {
		raw, _ := json.Marshal(map[string]any{
			"device_id": "device-run", "temporary_password": devicePassword,
			"command": "hostname", "timeout_seconds": 20, "wait_for_approval_seconds": 0,
		})
		data, err := backend.Invoke(context.Background(), agenttools.Request{Name: agenttools.RunCommand, Input: raw})
		done <- outcome{data: data, err: err}
	}()

	forwarded := device.recv(protocol.TypeExecuteCommand)
	if forwarded.RequestID == "" || forwarded.Command != "hostname" {
		t.Fatalf("unexpected forwarded command: %#v", forwarded)
	}
	device.send(protocol.Message{
		Type: protocol.TypeCommandOutput, SessionID: forwarded.SessionID, RequestID: forwarded.RequestID,
		Stream: "stdout", Data: "server-01\n", Sequence: 1,
	})
	exit := 0
	device.send(protocol.Message{
		Type: protocol.TypeCommandResult, SessionID: forwarded.SessionID, RequestID: forwarded.RequestID,
		Status: string(protocol.TaskSucceeded), ExitCode: &exit, ResultKnown: true,
	})

	result := <-done
	if result.err != nil {
		t.Fatalf("run_command: %v", result.err)
	}
	data, ok := result.data.(map[string]any)
	if !ok {
		t.Fatalf("unexpected result type %T", result.data)
	}
	if data["status"] != string(protocol.TaskSucceeded) || data["executed"] != true {
		t.Fatalf("unexpected run result: %#v", data)
	}
	if stdout, _ := data["stdout"].(string); stdout != "server-01\n" {
		t.Fatalf("stdout = %q", stdout)
	}
	if code, ok := data["exit_code"].(int); !ok || code != 0 {
		t.Fatalf("exit_code = %#v", data["exit_code"])
	}
	if data["session_closed"] != true {
		t.Fatalf("session should be closed: %#v", data)
	}
	device.recv(protocol.TypeCloseSession)
}

func TestPlatformClientRunCommandApprovalMode(t *testing.T) {
	address := startPlatform(t)
	device := dialFakeDevice(t, address)
	device.registerWithMode("device-approval", protocol.ModeApproval)

	backend, err := platformclient.New(platformclient.Config{
		Address: address, PlayerPassword: playerPassword, AllowPlaintext: true, TimeoutSeconds: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()

	type outcome struct {
		data any
		err  error
	}
	done := make(chan outcome, 1)
	go func() {
		raw, _ := json.Marshal(map[string]any{
			"device_id": "device-approval", "temporary_password": devicePassword,
			"command": "ipconfig", "timeout_seconds": 20, "wait_for_approval_seconds": 5,
		})
		data, err := backend.Invoke(context.Background(), agenttools.Request{Name: agenttools.RunCommand, Input: raw})
		done <- outcome{data: data, err: err}
	}()

	request := device.recv(protocol.TypeSessionRequest)
	device.send(protocol.Message{Type: protocol.TypeApproveSession, SessionID: request.SessionID})
	forwarded := device.recv(protocol.TypeExecuteCommand)
	if forwarded.Command != "ipconfig" {
		t.Fatalf("unexpected command: %#v", forwarded)
	}
	device.send(protocol.Message{
		Type: protocol.TypeCommandOutput, SessionID: forwarded.SessionID, RequestID: forwarded.RequestID,
		Stream: "stdout", Data: "ok\n", Sequence: 1,
	})
	exit := 0
	device.send(protocol.Message{
		Type: protocol.TypeCommandResult, SessionID: forwarded.SessionID, RequestID: forwarded.RequestID,
		Status: string(protocol.TaskSucceeded), ExitCode: &exit, ResultKnown: true,
	})

	result := <-done
	if result.err != nil {
		t.Fatalf("run_command: %v", result.err)
	}
	data := result.data.(map[string]any)
	if data["authorization"] != protocol.StatusApproved || data["status"] != string(protocol.TaskSucceeded) {
		t.Fatalf("unexpected approval-mode result: %#v", data)
	}
	if stdout, _ := data["stdout"].(string); stdout != "ok\n" {
		t.Fatalf("stdout = %q", stdout)
	}
	device.recv(protocol.TypeCloseSession)
}
