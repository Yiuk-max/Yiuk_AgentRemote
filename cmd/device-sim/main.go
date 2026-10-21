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
	"crypto/tls"
	"crypto/x509"
	"flag"
	"fmt"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"yiuk-switchboard/internal/platform/protocol"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:29564", "platform address")
	password := flag.String("password", "", "platform player password")
	deviceID := flag.String("device-id", "123456789", "device id to register")
	temporary := flag.String("temporary-password", "", "device temporary password (>=32 chars)")
	caFile := flag.String("ca", "", "CA certificate file")
	insecure := flag.Bool("insecure", false, "skip certificate verification")
	mode := flag.String("mode", "approval", "authorization mode: direct or approval")
	flag.Parse()

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
