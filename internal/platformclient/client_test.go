package platformclient_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"agentremote/internal/agenttools"
	"agentremote/internal/platform/config"
	"agentremote/internal/platform/protocol"
	"agentremote/internal/platform/server"
	"agentremote/internal/platformclient"

	"agentremote/internal/platform/transport"
	"github.com/gorilla/websocket"
)

const playerPassword = "client-test-player-password"
const devicePassword = "0123456789abcdef0123456789abcdef"

type fakeDevice struct {
	t       *testing.T
	conn    *websocket.Conn
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
	wsListener := transport.ServeWebSocketListener(ln, cfg.MaxFrameBytes)
	srv := server.NewWithListener(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), wsListener)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = srv.Run(ctx) }()
	t.Cleanup(func() { cancel(); srv.Close(); <-done })
	return "ws://" + ln.Addr().String()
}

func dialFakeDevice(t *testing.T, address string) *fakeDevice {
	t.Helper()
	conn, _, err := websocket.DefaultDialer.Dial(address, nil)
	if err != nil {
		t.Fatal(err)
	}
	d := &fakeDevice{t: t, conn: conn}
	t.Cleanup(func() { _ = conn.Close() })
	return d
}

func (d *fakeDevice) send(m protocol.Message) {
	d.t.Helper()
	m.Version = protocol.Version
	m.BusinessType = protocol.BusinessAgentAssist
	data, err := protocol.NewCodec(protocol.DefaultMaxMessageBytes).Encode(m)
	if err != nil {
		d.t.Fatalf("encode %s: %v", m.Type, err)
	}
	if err := d.conn.WriteMessage(websocket.BinaryMessage, data); err != nil {
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
		_, data, err := d.conn.ReadMessage()
		var m protocol.Message
		if err == nil {
			m, err = protocol.NewCodec(protocol.DefaultMaxMessageBytes).Decode(data)
		}
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

	// 轮询直到结果可用；首次轮询可能与流式输出通知发生竞争
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

// TestPlatformClientHeartbeatsIdleConnection 验证没有工具调用时操作端连接仍会持续发送心跳
// 如果缺少此检查，平台会在 heartbeat_timeout_sec 后关闭连接，导致长会话中断
func TestPlatformClientHeartbeatsIdleConnection(t *testing.T) {
	rawListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ln := transport.ServeWebSocketListener(rawListener, protocol.DefaultMaxMessageBytes)
	defer ln.Close()
	codec := protocol.NewCodec(protocol.DefaultMaxMessageBytes)
	pings := make(chan uint64, 8)
	go func() {
		conn, acceptErr := ln.Accept()
		if acceptErr != nil {
			return
		}
		defer conn.Close()
		for {
			data, readErr := conn.ReadMessage()
			m, err := codec.Decode(data)
			if readErr != nil {
				return
			}
			if err != nil {
				return
			}
			if readErr != nil {
				return
			}
			switch m.Type {
			case protocol.TypeAuthenticatePlayer:
				response, _ := codec.Encode(protocol.Message{Version: protocol.Version, Type: protocol.TypeAuthenticated, ConnectionID: "conn-heartbeat"})
				_ = conn.WriteMessage(response)
			case protocol.TypePing:
				pings <- m.Sequence
				response, _ := codec.Encode(protocol.Message{Version: protocol.Version, Type: protocol.TypePong, Sequence: m.Sequence})
				_ = conn.WriteMessage(response)
			case protocol.TypeListDevices:
				response, _ := codec.Encode(protocol.Message{Version: protocol.Version, Type: protocol.TypeDeviceList, CorrelationID: m.CorrelationID})
				_ = conn.WriteMessage(response)
			}
		}
	}()

	backend, err := platformclient.New(platformclient.Config{
		Address: "ws://" + ln.Addr().String(), PlayerPassword: playerPassword, AllowPlaintext: true,
		TimeoutSeconds: 5, HeartbeatIntervalSeconds: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()

	// 一次工具调用会建立持久操作端连接
	if _, err := backend.Invoke(context.Background(), agenttools.Request{Name: agenttools.ListDevices, Input: json.RawMessage(`{}`)}); err != nil {
		t.Fatalf("list_devices: %v", err)
	}

	deadline := time.After(6 * time.Second)
	for want := uint64(1); want <= 2; want++ {
		select {
		case seq := <-pings:
			if seq != want {
				t.Fatalf("ping sequence = %d, want %d", seq, want)
			}
		case <-deadline:
			t.Fatalf("received %d heartbeats, want at least 2 keep-alive pings", want-1)
		}
	}
}

// TestPlatformClientCloseSessionIsIdempotent 覆盖会话已过期的情况：平台重启后会清除会话，因此关闭已不存在的会话不应导致工具调用失败
func TestPlatformClientCloseSessionIsIdempotent(t *testing.T) {
	address := startPlatform(t)
	backend, err := platformclient.New(platformclient.Config{
		Address: address, PlayerPassword: playerPassword, AllowPlaintext: true, TimeoutSeconds: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()

	result := invoke(t, backend, agenttools.CloseSession, map[string]any{"session_id": "missing-session"}).(map[string]any)
	if result["status"] != protocol.StatusClosed {
		t.Fatalf("unexpected close result: %#v", result)
	}
	if result["session_id"] != "missing-session" {
		t.Fatalf("session_id = %#v", result["session_id"])
	}
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

func TestPlatformClientDownloadsFileWithReadFilePermission(t *testing.T) {
	address := startPlatform(t)
	device := dialFakeDevice(t, address)
	device.register("device-files")

	backend, err := platformclient.New(platformclient.Config{
		Address: address, PlayerPassword: playerPassword, AllowPlaintext: true, TimeoutSeconds: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()

	session := invoke(t, backend, agenttools.RequestConnection, map[string]any{
		"device_id": "device-files", "purpose": "read a log", "permissions": []string{"read_file"},
		"temporary_password": devicePassword,
	}).(map[string]any)
	sessionID, _ := session["session_id"].(string)
	if sessionID == "" || session["status"] != protocol.StatusApproved {
		t.Fatalf("unexpected session: %#v", session)
	}
	device.recv(protocol.TypeSessionStatus)

	destination := filepath.Join(t.TempDir(), "server.log")
	type outcome struct {
		data any
		err  error
	}
	done := make(chan outcome, 1)
	go func() {
		raw, _ := json.Marshal(map[string]any{
			"session_id": sessionID, "remote_path": `C:\logs\server.log`, "destination_path": destination,
		})
		data, err := backend.Invoke(context.Background(), agenttools.Request{Name: agenttools.DownloadFile, Input: raw})
		done <- outcome{data: data, err: err}
	}()

	forwarded := device.recv(protocol.TypeDownloadFile)
	if forwarded.RequestID == "" || forwarded.RemotePath != `C:\logs\server.log` {
		t.Fatalf("unexpected file request: %#v", forwarded)
	}
	contents := []byte("first line\nsecond line\n")
	sum := sha256.Sum256(contents)
	split := 7
	device.send(protocol.Message{
		Type: protocol.TypeFileChunk, SessionID: sessionID, RequestID: forwarded.RequestID, Sequence: 1,
		Data: base64.StdEncoding.EncodeToString(contents[:split]),
	})
	device.send(protocol.Message{
		Type: protocol.TypeFileChunk, SessionID: sessionID, RequestID: forwarded.RequestID, Sequence: 2,
		Data: base64.StdEncoding.EncodeToString(contents[split:]), EOF: true,
	})
	device.send(protocol.Message{
		Type: protocol.TypeFileDownloadResult, SessionID: sessionID, RequestID: forwarded.RequestID,
		Status: string(protocol.TaskSucceeded), FileName: "server.log", FileSize: int64(len(contents)),
		FileSHA256: hex.EncodeToString(sum[:]), ResultKnown: true,
	})

	result := <-done
	if result.err != nil {
		t.Fatalf("download_file: %v", result.err)
	}
	data := result.data.(map[string]any)
	if data["local_path"] != destination || data["status"] != string(protocol.TaskSucceeded) {
		t.Fatalf("unexpected download result: %#v", data)
	}
	got, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(contents) {
		t.Fatalf("downloaded contents = %q", got)
	}
	if data["sha256"] != hex.EncodeToString(sum[:]) {
		t.Fatalf("sha256 = %v", data["sha256"])
	}
	invoke(t, backend, agenttools.CloseSession, map[string]any{"session_id": sessionID})
	device.recv(protocol.TypeCloseSession)
}

func TestPlatformClientUploadFile(t *testing.T) {
	cases := []struct {
		name     string
		contents []byte
	}{
		{"single chunk", []byte("upload from the operator client\n")},
		{"exact multiple of the chunk size", bytes.Repeat([]byte("a"), 128*1024)},
		{"partial tail chunk", bytes.Repeat([]byte("b"), 100*1024)},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) { runUploadTool(t, tc.contents) })
	}
}

// runUploadTool 执行一次 upload_file 工具调用，并验证设备按顺序收到数据及终止用的 EOF 分块
func runUploadTool(t *testing.T, contents []byte) {
	t.Helper()
	address := startPlatform(t)
	device := dialFakeDevice(t, address)
	device.register("device-upload")

	backend, err := platformclient.New(platformclient.Config{
		Address: address, PlayerPassword: playerPassword, AllowPlaintext: true, TimeoutSeconds: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()

	session := invoke(t, backend, agenttools.RequestConnection, map[string]any{
		"device_id": "device-upload", "purpose": "send a file", "permissions": []string{"write_file"},
		"temporary_password": devicePassword,
	}).(map[string]any)
	sessionID, _ := session["session_id"].(string)
	if sessionID == "" || session["status"] != protocol.StatusApproved {
		t.Fatalf("unexpected session: %#v", session)
	}
	device.recv(protocol.TypeSessionStatus)

	localPath := filepath.Join(t.TempDir(), "upload-source.bin")
	if err := os.WriteFile(localPath, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(contents)
	remotePath := "C:/agent/upload.bin"
	sessionId := sessionID

	type outcome struct {
		data any
		err  error
	}
	done := make(chan outcome, 1)
	go func() {
		raw, _ := json.Marshal(map[string]any{
			"session_id": sessionId, "local_path": localPath,
			"remote_path": remotePath, "overwrite": true,
		})
		data, err := backend.Invoke(context.Background(), agenttools.Request{Name: agenttools.UploadFile, Input: raw})
		done <- outcome{data: data, err: err}
	}()

	forwarded := device.recv(protocol.TypeUploadFile)
	if forwarded.RequestID == "" || forwarded.RemotePath != remotePath ||
		forwarded.FileName == "" || forwarded.FileSize != int64(len(contents)) || !forwarded.Overwrite {
		t.Fatalf("unexpected upload request: %#v", forwarded)
	}
	if forwarded.FileSHA256 != hex.EncodeToString(sum[:]) {
		t.Fatalf("upload request digest = %q", forwarded.FileSHA256)
	}
	var received []byte
	var sequence uint64 = 1
	for {
		chunk := device.recv(protocol.TypeFileUploadChunk)
		if chunk.RequestID != forwarded.RequestID || chunk.Sequence != sequence {
			t.Fatalf("unexpected chunk: %#v", chunk)
		}
		data, decodeErr := base64.StdEncoding.DecodeString(chunk.Data)
		if decodeErr != nil {
			t.Fatalf("chunk is not base64: %v", decodeErr)
		}
		received = append(received, data...)
		if chunk.EOF {
			break
		}
		sequence++
		if sequence > 64 {
			t.Fatal("upload did not terminate")
		}
	}
	if !bytes.Equal(received, contents) {
		t.Fatalf("device received %d bytes, want %d", len(received), len(contents))
	}
	device.send(protocol.Message{
		Type: protocol.TypeFileUploadResult, SessionID: sessionID, RequestID: forwarded.RequestID,
		Status: string(protocol.TaskSucceeded), FileName: forwarded.FileName,
		FileSize: int64(len(contents)), FileSHA256: hex.EncodeToString(sum[:]),
		RemotePath: remotePath, ResultKnown: true,
	})

	result := <-done
	if result.err != nil {
		t.Fatalf("upload_file: %v", result.err)
	}
	data := result.data.(map[string]any)
	if data["remote_path"] != remotePath || data["status"] != string(protocol.TaskSucceeded) {
		t.Fatalf("unexpected upload result: %#v", data)
	}
	if size, _ := data["size"].(int64); size != int64(len(contents)) {
		t.Fatalf("size = %#v", data["size"])
	}
	if data["sha256"] != hex.EncodeToString(sum[:]) {
		t.Fatalf("sha256 = %v", data["sha256"])
	}
	invoke(t, backend, agenttools.CloseSession, map[string]any{"session_id": sessionID})
	device.recv(protocol.TypeCloseSession)
}

// TestPlatformClientUploadFileRejectsInvalidLocalPath 验证无效的 local_path 不会发送到平台
func TestPlatformClientUploadFileRejectsInvalidLocalPath(t *testing.T) {
	address := startPlatform(t)
	backend, err := platformclient.New(platformclient.Config{
		Address: address, PlayerPassword: playerPassword, AllowPlaintext: true, TimeoutSeconds: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()

	for _, localPath := range []string{"relative/file.txt", "C:/definitely/missing/file.bin"} {
		raw, _ := json.Marshal(map[string]any{"session_id": "session-x", "local_path": localPath})
		_, err := backend.Invoke(context.Background(), agenttools.Request{Name: agenttools.UploadFile, Input: raw})
		var toolErr *agenttools.Error
		if err == nil || !errors.As(err, &toolErr) {
			t.Fatalf("%q: got %v, want a tool error", localPath, err)
		}
		if localPath == "relative/file.txt" && toolErr.Code != "INVALID_ARGUMENT" {
			t.Fatalf("relative path code = %s, want INVALID_ARGUMENT", toolErr.Code)
		}
		if localPath != "relative/file.txt" && toolErr.Code != "LOCAL_READ_FAILED" {
			t.Fatalf("missing file code = %s, want LOCAL_READ_FAILED", toolErr.Code)
		}
	}
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
