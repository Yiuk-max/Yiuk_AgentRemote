// Command smoke drives one full remote-assist flow against a live platform
// server over TLS, acting as both the device and the operator.
//
// Usage:
//
//	smoke -addr host:port -password <player-password> -ca <ca.pem> [-insecure]
package main

import (
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"agentremote/internal/platform/protocol"
	"agentremote/internal/platform/transport"
)

type client struct {
	name  string
	conn  transport.Conn
	codec protocol.Codec
}

func main() {
	addr := flag.String("addr", "127.0.0.1:25664", "platform address")
	password := flag.String("password", "", "platform player password")
	caFile := flag.String("ca", "", "CA certificate file")
	insecure := flag.Bool("insecure", false, "skip certificate verification")
	mode := flag.String("mode", "approval", "device authorization mode: direct or approval")
	flag.Parse()
	if *password == "" {
		fail("missing -password")
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
	conn, err := transport.DialWebSocket(*addr, cfg)
	if err != nil {
		fail("dial: %v", err)
	}
	defer conn.Close()

	// Use a random device id so repeated smoke runs do not collide with a
	// device that a previous run already registered (re-registration requires
	// the current temporary password).
	deviceID := "9" + randomHex(4)
	devicePassword := randomHex(20) // 40 chars, >= 32 required
	operatorID := "200000002"
	operatorPassword := randomHex(20)

	device := &client{name: "device", conn: conn, codec: protocol.NewCodec(protocol.DefaultMaxMessageBytes)}
	operatorConn, err := transport.DialWebSocket(*addr, cfg)
	if err != nil {
		fail("dial operator: %v", err)
	}
	defer operatorConn.Close()
	operator := &client{name: "operator", conn: operatorConn, codec: protocol.NewCodec(protocol.DefaultMaxMessageBytes)}

	// 1. player authentication (both sides)
	device.auth(*password)
	operator.auth(*password)

	// 2. device registration
	device.send(protocol.Message{
		Type: protocol.TypeRegisterDevice, DeviceID: deviceID, DeviceName: "smoke-device",
		Platform: "windows", Mode: *mode, TemporaryPassword: devicePassword,
	})
	registered := device.recv(protocol.TypeDeviceRegistered)
	step("device registered", "device_id", registered.DeviceID, "mode", registered.Mode, "version", registered.PasswordVersion)

	// 3. operator lists devices
	operator.send(protocol.Message{Type: protocol.TypeListDevices})
	list := operator.recv(protocol.TypeDeviceList)
	step("device listed", "count", len(list.Devices), "online", list.Devices[0].Online)

	// 4. operator requests a session with the temporary password
	operator.send(protocol.Message{
		Type: protocol.TypeRequestSession, DeviceID: deviceID, TemporaryPassword: devicePassword,
		Purpose: "smoke test", Permissions: []string{"execute_command"},
	})
	created := operator.recv(protocol.TypeSessionCreated)
	sessionID := created.SessionID
	step("session created", "session", short(sessionID), "status", created.Status)

	// 5. approval mode: device approves
	if *mode == "approval" {
		request := device.recv(protocol.TypeSessionRequest)
		device.send(protocol.Message{Type: protocol.TypeApproveSession, SessionID: request.SessionID})
		operator.recv(protocol.TypeSessionStatus)
		device.recv(protocol.TypeSessionStatus)
		step("session approved", "session", short(sessionID))
	}

	// 6. operator executes a command
	requestID := randomHex(8)
	operator.send(protocol.Message{
		Type: protocol.TypeExecuteCommand, SessionID: sessionID, RequestID: requestID,
		Command: "Get-Location", TimeoutSeconds: 30,
	})
	operator.recv(protocol.TypeCommandAccepted)
	forwarded := device.recv(protocol.TypeExecuteCommand)
	step("command forwarded", "request", short(forwarded.RequestID), "command", forwarded.Command)

	// 7. device streams output and reports the result
	device.send(protocol.Message{
		Type: protocol.TypeCommandOutput, SessionID: sessionID, RequestID: requestID,
		Stream: "stdout", Data: "C:\\smoke-test\n", Sequence: 1,
	})
	output := operator.recv(protocol.TypeCommandOutput)
	exit := 0
	device.send(protocol.Message{
		Type: protocol.TypeCommandResult, SessionID: sessionID, RequestID: requestID,
		Status: string(protocol.TaskSucceeded), ExitCode: &exit, ResultKnown: true,
	})
	operator.recv(protocol.TypeCommandResult)
	step("output relayed", "data", trim(output.Data))

	// 8. operator reads the paged result
	operator.send(protocol.Message{Type: protocol.TypeGetCommandResult, SessionID: sessionID, RequestID: requestID})
	paged := operator.recv(protocol.TypeCommandResult)
	step("result readable", "chunks", len(paged.Chunks), "status", paged.Status)

	// 9. unapproved/other-connection access must be rejected
	intruderConn, err := transport.DialWebSocket(*addr, cfg)
	if err != nil {
		fail("dial intruder: %v", err)
	}
	defer intruderConn.Close()
	intruder := &client{name: "intruder", conn: intruderConn, codec: protocol.NewCodec(protocol.DefaultMaxMessageBytes)}
	intruder.auth(*password)
	intruder.send(protocol.Message{
		Type: protocol.TypeExecuteCommand, SessionID: sessionID, RequestID: randomHex(8), Command: "whoami",
	})
	denied := intruder.recvError()
	if denied != "PERMISSION_DENIED" {
		fail("cross-connection access was not denied: %s", denied)
	}
	step("cross-connection rejected", "code", denied)

	// 10. wrong player password must fail
	badConn, err := transport.DialWebSocket(*addr, cfg)
	if err != nil {
		fail("dial bad-auth: %v", err)
	}
	defer badConn.Close()
	bad := &client{name: "bad-auth", conn: badConn, codec: protocol.NewCodec(protocol.DefaultMaxMessageBytes)}
	bad.send(protocol.Message{Type: protocol.TypeAuthenticatePlayer, IdentityType: "player", PlayerPassword: "wrong-password-value"})
	if code := bad.recvError(); code != "BAD_PASSWORD" {
		fail("wrong password accepted: %s", code)
	}
	step("wrong player password rejected", "code", "BAD_PASSWORD")

	// 11. close the session
	operator.send(protocol.Message{Type: protocol.TypeCloseSession, SessionID: sessionID})
	closed := operator.recv(protocol.TypeSessionStatus)
	device.recv(protocol.TypeCloseSession)
	step("session closed", "status", closed.Status)

	// 12. session is no longer usable
	operator.send(protocol.Message{
		Type: protocol.TypeExecuteCommand, SessionID: sessionID, RequestID: randomHex(8), Command: "whoami",
	})
	if code := operator.recvError(); code != "INVALID_SESSION_STATE" {
		fail("closed session still usable: %s", code)
	}
	step("closed session rejected", "code", "INVALID_SESSION_STATE")

	_ = operatorID
	_ = operatorPassword
	fmt.Println("\nALL CHECKS PASSED")
}

func (c *client) auth(password string) {
	c.send(protocol.Message{Type: protocol.TypeAuthenticatePlayer, IdentityType: "player", PlayerPassword: password})
	c.recv(protocol.TypeAuthenticated)
}

func (c *client) send(m protocol.Message) {
	m.Version = protocol.Version
	if m.BusinessType == "" {
		m.BusinessType = protocol.BusinessAgentAssist
	}
	data, err := c.codec.Encode(m)
	if err != nil {
		fail("%s encode %s: %v", c.name, m.Type, err)
	}
	if err := c.conn.WriteMessage(data); err != nil {
		fail("%s send %s: %v", c.name, m.Type, err)
	}
}

func (c *client) recv(types ...string) protocol.Message {
	want := make(map[string]bool, len(types))
	for _, t := range types {
		want[t] = true
	}
	for {
		_ = c.conn.SetReadDeadline(time.Now().Add(10 * time.Second))
		data, err := c.conn.ReadMessage()
		if err != nil {
			fail("%s receive %v: %v", c.name, types, err)
		}
		m, err := c.codec.Decode(data)
		if err != nil {
			fail("%s decode %v: %v", c.name, types, err)
		}
		if len(want) == 0 || want[m.Type] {
			if m.Type == protocol.TypeError {
				fail("%s got error %s: %s", c.name, m.Code, m.Message)
			}
			return m
		}
	}
}

// recvError reads the next error frame and returns its code.
func (c *client) recvError() string {
	for {
		_ = c.conn.SetReadDeadline(time.Now().Add(10 * time.Second))
		data, err := c.conn.ReadMessage()
		if err != nil {
			fail("%s receive error: %v", c.name, err)
		}
		m, err := c.codec.Decode(data)
		if err != nil {
			fail("%s decode error: %v", c.name, err)
		}
		if m.Type == protocol.TypeError {
			return m.Code
		}
	}
}

func step(label string, kv ...any) {
	fmt.Printf("  ok  %-34s", label)
	for i := 0; i+1 < len(kv); i += 2 {
		value := kv[i+1]
		if raw, err := json.Marshal(value); err == nil {
			fmt.Printf(" %v=%s", kv[i], string(raw))
		}
	}
	fmt.Println()
}

func randomHex(n int) string {
	raw := make([]byte, n)
	if _, err := rand.Read(raw); err != nil {
		fail("rand: %v", err)
	}
	return hex.EncodeToString(raw)
}

func short(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

func trim(s string) string {
	if len(s) > 40 {
		return s[:40] + "..."
	}
	return s
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "FAIL: "+format+"\n", args...)
	os.Exit(1)
}
