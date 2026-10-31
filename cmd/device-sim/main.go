// Command device-sim acts as a stand-in Windows device for platform testing.
//
// It authenticates, registers a device id, approves pending sessions, and
// answers execute_command with a canned result. Use it to exercise the
// operator/MCP path when no real Windows client is available.
//
// This is a test harness only. It never runs real shell commands.
//
// Usage:
//
//	device-sim -addr host:port -password <player-password> -device-id 123456789 \
//	           -temporary-password <32+ chars> [-ca ca.pem | -insecure] [-mode approval|direct]
package main

import (
	"bufio"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"flag"
	"fmt"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"agentremote/internal/platform/protocol"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:29564", "platform address")
	password := flag.String("password", "", "platform player password")
	deviceID := flag.String("device-id", "123456789", "device id to register")
	temporary := flag.String("temporary-password", "", "device temporary password (>=32 chars)")
	caFile := flag.String("ca", "", "CA certificate file")
	insecure := flag.Bool("insecure", false, "skip certificate verification")
	mode := flag.String("mode", "approval", "authorization mode: direct or approval")
	uploadDir := flag.String("upload-dir", "", "directory for received uploads (default: <temp>/yiuk-device-sim)")
	flag.Parse()
	if *uploadDir == "" {
		*uploadDir = filepath.Join(os.TempDir(), "yiuk-device-sim")
	}
	if err := os.MkdirAll(*uploadDir, 0o700); err != nil {
		fail("create upload dir: %v", err)
	}
	uploads := &uploadStore{dir: *uploadDir, active: make(map[string]*uploadState)}

	if *password == "" || *temporary == "" {
		fmt.Fprintln(os.Stderr, "device-sim: -password and -temporary-password are required")
		os.Exit(2)
	}
	if len(*temporary) < 32 {
		fmt.Fprintln(os.Stderr, "device-sim: -temporary-password must be at least 32 characters")
		os.Exit(2)
	}

	cfg := &tls.Config{MinVersion: tls.VersionTLS12}
	if *insecure {
		cfg.InsecureSkipVerify = true
	}
	if *caFile != "" {
		pem, err := os.ReadFile(*caFile)
		if err != nil {
			fail("read ca: %v", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			fail("no certificates in %s", *caFile)
		}
		cfg.RootCAs = pool
	}

	conn, err := tls.Dial("tcp", *addr, cfg)
	if err != nil {
		fail("dial: %v", err)
	}
	defer conn.Close()

	codec := protocol.NewCodec(protocol.DefaultMaxFrameBytes)
	reader := bufio.NewReader(conn)
	send := func(m protocol.Message) {
		m.Version = protocol.Version
		if m.BusinessType == "" {
			m.BusinessType = protocol.BusinessAgentAssist
		}
		if err := codec.Write(conn, m); err != nil {
			fail("send %s: %v", m.Type, err)
		}
	}

	send(protocol.Message{Type: protocol.TypeAuthenticatePlayer, IdentityType: "player", PlayerPassword: *password})
	send(protocol.Message{
		Type: protocol.TypeRegisterDevice, DeviceID: *deviceID, DeviceName: "device-sim",
		Platform: "windows", Mode: *mode, TemporaryPassword: *temporary,
	})

	stop := make(chan struct{})
	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
		<-sig
		close(stop)
		_ = conn.Close()
	}()

	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()

	for {
		_ = conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		msg, err := codec.Read(reader)
		if err != nil {
			select {
			case <-stop:
				return
			default:
			}
			fail("read: %v", err)
		}
		switch msg.Type {
		case protocol.TypeAuthenticated:
			logf("authenticated connection=%s", short(msg.ConnectionID))
		case protocol.TypeDeviceRegistered:
			logf("registered device=%s mode=%s version=%d expires=%s",
				msg.DeviceID, msg.Mode, msg.PasswordVersion,
				time.Unix(msg.ExpiresAt, 0).Format(time.RFC3339))
		case protocol.TypeHeartbeatAck:
			logf("heartbeat active_sessions=%d", msg.ActiveSessions)
		case protocol.TypeSessionRequest:
			logf("session request %s purpose=%q operator=%q", short(msg.SessionID),
				sessionField(msg, "purpose"), sessionField(msg, "operator"))
			send(protocol.Message{Type: protocol.TypeApproveSession, SessionID: msg.SessionID})
			logf("approved session %s", short(msg.SessionID))
		case protocol.TypeSessionStatus:
			logf("session %s status=%s", short(msg.SessionID), msg.Status)
		case protocol.TypeExecuteCommand:
			logf("execute %s command=%q", short(msg.RequestID), msg.Command)
			go answer(codec, conn, msg)
		case protocol.TypeUploadFile:
			uploads.start(codec, conn, msg)
		case protocol.TypeFileUploadChunk:
			uploads.appendChunk(codec, conn, msg)
		case protocol.TypeCancelFileUpload:
			uploads.cancel(codec, conn, msg)
		case protocol.TypeCancelCommand:
			logf("cancel requested %s", short(msg.RequestID))
		case protocol.TypeCloseSession:
			logf("close session %s", short(msg.SessionID))
		case protocol.TypeError:
			logf("error %s: %s", msg.Code, msg.Message)
		case protocol.TypePong:
			// ignore
		}
		select {
		case <-heartbeat.C:
			send(protocol.Message{Type: protocol.TypeDeviceHeartbeat, DeviceID: *deviceID})
		default:
		}
	}
}

// uploadStore writes operator uploads into a local directory so the upload
// path can be exercised end to end without the Qt device.
type uploadStore struct {
	mu     sync.Mutex
	dir    string
	active map[string]*uploadState
}

type uploadState struct {
	sessionID  string
	targetPath string
	tempPath   string
	fileName   string
	expected   int64
	sha256     string
	overwrite  bool
	nextSeq    uint64
	received   int64
	hash       []byte
	chunks     []byte
}

func (u *uploadStore) start(codec protocol.Codec, conn net.Conn, msg protocol.Message) {
	u.mu.Lock()
	defer u.mu.Unlock()
	name := strings.TrimSpace(msg.FileName)
	if name == "" {
		name = "upload.bin"
	} else {
		name = filepath.Base(name)
	}
	target := strings.TrimSpace(msg.RemotePath)
	if target == "" {
		target = filepath.Join(u.dir, fmt.Sprintf("%d-%s", time.Now().UnixNano(), name))
	} else if !filepath.IsAbs(target) {
		u.finish(codec, conn, msg, "failed", "", 0, name, "remote_path must be absolute")
		return
	}
	if _, err := os.Stat(target); err == nil && !msg.Overwrite {
		u.finish(codec, conn, msg, "failed", "", 0, name, "target file already exists; overwrite is not enabled")
		return
	}
	state := &uploadState{
		sessionID:  msg.SessionID,
		targetPath: target,
		fileName:   name,
		expected:   msg.FileSize,
		sha256:     strings.ToLower(strings.TrimSpace(msg.FileSHA256)),
		overwrite:  msg.Overwrite,
		nextSeq:    1,
		hash:       make([]byte, 0, 0),
	}
	state.tempPath = target + ".part"
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		u.finish(codec, conn, msg, "failed", "", 0, name, "cannot create target directory")
		return
	}
	if err := os.WriteFile(state.tempPath, nil, 0o600); err != nil {
		u.finish(codec, conn, msg, "failed", "", 0, name, "cannot create target file")
		return
	}
	u.active[msg.RequestID] = state
	logf("upload %s start name=%q size=%d target=%q", short(msg.RequestID), name, msg.FileSize, target)
	if msg.FileSize == 0 {
		u.complete(codec, conn, msg, state)
	}
}

func (u *uploadStore) appendChunk(codec protocol.Codec, conn net.Conn, msg protocol.Message) {
	u.mu.Lock()
	defer u.mu.Unlock()
	state, ok := u.active[msg.RequestID]
	if !ok || state.sessionID != msg.SessionID {
		return
	}
	data, err := base64.StdEncoding.DecodeString(msg.Data)
	if err != nil {
		u.fail(codec, conn, msg, state, "invalid base64 chunk")
		return
	}
	if msg.Sequence != state.nextSeq {
		u.fail(codec, conn, msg, state, "chunk sequence is not contiguous")
		return
	}
	if state.received+int64(len(data)) > state.expected {
		u.fail(codec, conn, msg, state, "more bytes than declared")
		return
	}
	file, err := os.OpenFile(state.tempPath, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		u.fail(codec, conn, msg, state, "cannot open target file")
		return
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		u.fail(codec, conn, msg, state, "cannot write target file")
		return
	}
	if err := file.Close(); err != nil {
		u.fail(codec, conn, msg, state, "cannot flush target file")
		return
	}
	state.chunks = append(state.chunks, data...)
	state.received += int64(len(data))
	state.nextSeq++
	if !msg.EOF {
		return
	}
	u.complete(codec, conn, msg, state)
}

func (u *uploadStore) complete(codec protocol.Codec, conn net.Conn, msg protocol.Message, state *uploadState) {
	sum := sha256.Sum256(state.chunks)
	actual := hex.EncodeToString(sum[:])
	if state.received != state.expected {
		u.fail(codec, conn, msg, state, "received size does not match declaration")
		return
	}
	if state.sha256 != "" && actual != state.sha256 {
		u.fail(codec, conn, msg, state, "checksum does not match")
		return
	}
	if state.overwrite {
		_ = os.Remove(state.targetPath)
	}
	if err := os.Rename(state.tempPath, state.targetPath); err != nil {
		u.fail(codec, conn, msg, state, "cannot move the received file into place")
		return
	}
	delete(u.active, msg.RequestID)
	logf("upload %s complete %d bytes -> %s", short(msg.RequestID), state.received, state.targetPath)
	u.finish(codec, conn, msg, "succeeded", state.targetPath, state.received, state.fileName, "")
}

func (u *uploadStore) cancel(codec protocol.Codec, conn net.Conn, msg protocol.Message) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if state, ok := u.active[msg.RequestID]; ok {
		_ = os.Remove(state.tempPath)
		delete(u.active, msg.RequestID)
		logf("upload %s cancelled", short(msg.RequestID))
	}
	u.finish(codec, conn, msg, "cancelled", "", 0, msg.FileName, "")
}

func (u *uploadStore) fail(codec protocol.Codec, conn net.Conn, msg protocol.Message, state *uploadState, reason string) {
	_ = os.Remove(state.tempPath)
	delete(u.active, msg.RequestID)
	logf("upload %s failed: %s", short(msg.RequestID), reason)
	u.finish(codec, conn, msg, "failed", "", 0, state.fileName, reason)
}

func (u *uploadStore) finish(codec protocol.Codec, conn net.Conn, msg protocol.Message, status, remotePath string, size int64, fileName, detail string) {
	if detail != "" {
		logf("upload %s %s: %s", short(msg.RequestID), status, detail)
	}
	result := protocol.Message{
		Version:      protocol.Version,
		BusinessType: protocol.BusinessAgentAssist,
		Type:         protocol.TypeFileUploadResult,
		SessionID:    msg.SessionID,
		RequestID:    msg.RequestID,
		Status:       status,
		RemotePath:   remotePath,
		FileName:     fileName,
		FileSize:     size,
		ResultKnown:  true,
	}
	_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	_ = codec.Write(conn, result)
}

// answer reports a canned successful result for a simulated command.
func answer(codec protocol.Codec, conn net.Conn, msg protocol.Message) {
	time.Sleep(150 * time.Millisecond)
	output := fmt.Sprintf("device-sim: would run: %s\n", msg.Command)
	write := func(m protocol.Message) {
		m.Version = protocol.Version
		m.BusinessType = protocol.BusinessAgentAssist
		_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
		_ = codec.Write(conn, m)
	}
	write(protocol.Message{
		Type: protocol.TypeCommandOutput, SessionID: msg.SessionID, RequestID: msg.RequestID,
		Stream: "stdout", Data: output, Sequence: 1,
	})
	exit := 0
	write(protocol.Message{
		Type: protocol.TypeCommandResult, SessionID: msg.SessionID, RequestID: msg.RequestID,
		Status: string(protocol.TaskSucceeded), ExitCode: &exit, ResultKnown: true,
	})
}

func sessionField(msg protocol.Message, field string) string {
	if msg.Session == nil {
		return ""
	}
	switch field {
	case "purpose":
		return msg.Session.Purpose
	case "operator":
		return msg.Session.Operator
	default:
		return ""
	}
}

func short(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

func logf(format string, args ...any) {
	fmt.Printf("[device-sim] %s %s\n", time.Now().Format("15:04:05"), strings.TrimSpace(fmt.Sprintf(format, args...)))
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "[device-sim] FAIL: "+format+"\n", args...)
	os.Exit(1)
}
