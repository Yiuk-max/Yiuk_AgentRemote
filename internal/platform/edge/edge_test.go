package edge_test

// 这个测试按拆分后的真实形态跑：core 与 gateway 各是一个 gRPC 端点，
// 中间只传原始帧。它固定三件在纯进程内测试里看不到的事：
//   - Alive：core 需要问 gateway 才知道某条连接还在不在
//   - Deliver：core 的异步/回调帧通过 gateway 才能写到 socket
//   - Detach：连接关闭后 core 收敛会话

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"

	"agentremote/internal/platform/config"
	"agentremote/internal/platform/core"
	"agentremote/internal/platform/edge"
	"agentremote/internal/platform/edge/edgepb"
	"agentremote/internal/platform/gateway"
	"agentremote/internal/platform/protocol"
	"agentremote/internal/platform/transport"

	"github.com/gorilla/websocket"
	"google.golang.org/grpc"
)

const (
	testPlayerPassword = "test-player-password-1234567890"
	testDevicePassword = "0123456789abcdef0123456789abcdef"
	testDeviceID       = "123456789"
	readTimeout        = 5 * time.Second
)

// testClient 是一个最小的协议客户端，够用来驱动一次完整协助流程。
type testClient struct {
	t    *testing.T
	conn *websocket.Conn
}

func (c *testClient) send(message protocol.Message) {
	c.t.Helper()
	message.Version = protocol.Version
	message.BusinessType = protocol.BusinessAgentAssist
	data, err := json.Marshal(message)
	if err != nil {
		c.t.Fatalf("encode %s: %v", message.Type, err)
	}
	if err := c.conn.WriteMessage(websocket.BinaryMessage, data); err != nil {
		c.t.Fatalf("write %s: %v", message.Type, err)
	}
}

// expect 读消息直到遇到指定类型，返回它；读不到就失败。
func (c *testClient) expect(messageType string) protocol.Message {
	c.t.Helper()
	deadline := time.Now().Add(readTimeout)
	for {
		if err := c.conn.SetReadDeadline(deadline); err != nil {
			c.t.Fatalf("set read deadline: %v", err)
		}
		_, data, err := c.conn.ReadMessage()
		if err != nil {
			c.t.Fatalf("waiting for %s: %v", messageType, err)
		}
		var message protocol.Message
		if err := json.Unmarshal(data, &message); err != nil {
			c.t.Fatalf("decode %q: %v", string(data), err)
		}
		if message.Type == messageType {
			return message
		}
		if message.Type == protocol.TypeError && messageType != protocol.TypeError {
			c.t.Fatalf("waiting for %s, got error %s/%s", messageType, message.Code, message.Message)
		}
	}
}

// TestTwoProcessFlow 覆盖 core 与 gateway 分处两个 gRPC 端点时的完整流程。
func TestTwoProcessFlow(t *testing.T) {
	cfg := config.Default()
	cfg.Listen = "127.0.0.1:0"
	cfg.AllowInsecure = true
	cfg.PlayerPassword = testPlayerPassword
	cfg.HandshakeTimeoutSec = 5
	cfg.HeartbeatTimeoutSec = 30
	cfg.MaxOutputBytes = 1 << 20
	if err := cfg.Validate(); err != nil {
		t.Fatalf("test config: %v", err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	// gateway 与它的 Push 服务（core → gateway）
	rawListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	wsListener := transport.ServeWebSocketListener(rawListener, cfg.MaxFrameBytes)
	gw := gateway.NewWithListener(cfg, logger, wsListener)

	pushListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	pushServer := grpc.NewServer()
	edgepb.RegisterPushServer(pushServer, edge.NewPushServer(gw))
	go func() { _ = pushServer.Serve(pushListener) }()

	// core 与它的 Edge 服务（gateway → core），出口连 gateway 的 Push
	link, err := edge.NewPushClient(pushListener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	service := core.New(cfg, logger, link)

	coreListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	coreServer := grpc.NewServer()
	edgepb.RegisterEdgeServer(coreServer, edge.NewCoreServer(service))
	go func() { _ = coreServer.Serve(coreListener) }()

	coreClient, err := edge.NewCoreClient(coreListener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	gw.Bind(coreClient)

	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan struct{})
	go func() { defer close(runDone); _ = gw.Run(ctx) }()
	go func() { _ = service.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		gw.Close()
		<-runDone
		coreServer.Stop()
		pushServer.Stop()
	})

	address := "ws://" + wsListener.Addr().String()
	dial := func() *testClient {
		t.Helper()
		conn, _, err := websocket.DefaultDialer.Dial(address, nil)
		if err != nil {
			t.Fatalf("dial %s: %v", address, err)
		}
		t.Cleanup(func() { _ = conn.Close() })
		return &testClient{t: t, conn: conn}
	}
	authenticate := func(client *testClient) {
		t.Helper()
		client.send(protocol.Message{Type: protocol.TypeAuthenticatePlayer, IdentityType: "player", PlayerPassword: testPlayerPassword})
		client.expect(protocol.TypeAuthenticated)
	}

	operator := dial()
	authenticate(operator)

	device := dial()
	authenticate(device)
	device.send(protocol.Message{
		Type:              protocol.TypeRegisterDevice,
		DeviceID:          testDeviceID,
		DeviceName:        "edge-test-device",
		Platform:          "windows",
		TemporaryPassword: testDevicePassword,
		Mode:              protocol.ModeDirect,
	})
	registered := device.expect(protocol.TypeDeviceRegistered)
	if registered.DeviceID != testDeviceID {
		t.Fatalf("device_id = %q, want %q", registered.DeviceID, testDeviceID)
	}

	// 直接模式申请会话：core 在这里必须问 gateway “设备连接还活着吗”（Alive）。
	operator.send(protocol.Message{
		Type:              protocol.TypeRequestSession,
		DeviceID:          testDeviceID,
		TemporaryPassword: testDevicePassword,
		Purpose:           "edge integration test",
	})
	created := operator.expect(protocol.TypeSessionCreated)
	if created.Status != protocol.StatusApproved {
		t.Fatalf("session status = %q, want %q", created.Status, protocol.StatusApproved)
	}
	sessionID := created.SessionID

	// 设备也应该收到会话建立的通知（core 通过 Deliver 推到设备连接）。
	device.expect(protocol.TypeSessionStatus)

	// 命令去程：operator → core → gateway → device
	operator.send(protocol.Message{
		Type:      protocol.TypeExecuteCommand,
		SessionID: sessionID,
		RequestID: "edge-test-request",
		Command:   "Get-Location",
	})
	operator.expect(protocol.TypeCommandAccepted)
	forwarded := device.expect(protocol.TypeExecuteCommand)
	if forwarded.Command != "Get-Location" {
		t.Fatalf("forwarded command = %q", forwarded.Command)
	}

	// 结果回程：device → core → gateway → operator
	device.send(protocol.Message{Type: protocol.TypeCommandOutput, SessionID: sessionID, RequestID: forwarded.RequestID, Stream: "stdout", Data: "C:\\edge\n"})
	device.send(protocol.Message{Type: protocol.TypeCommandResult, SessionID: sessionID, RequestID: forwarded.RequestID, Status: protocol.TaskSucceeded, ResultKnown: true})
	result := operator.expect(protocol.TypeCommandResult)
	if result.Status != protocol.TaskSucceeded {
		t.Fatalf("command status = %q, want %q", result.Status, protocol.TaskSucceeded)
	}

	// 设备掉线：gateway 关连接后必须调 Detach，core 中断会话并通知操作者。
	_ = device.conn.Close()
	interrupted := operator.expect(protocol.TypeSessionStatus)
	if interrupted.Status != protocol.StatusInterrupted {
		t.Fatalf("session status after device disconnect = %q, want %q", interrupted.Status, protocol.StatusInterrupted)
	}

	// 设备已经不在连接表里：core 再申请会话必须靠 Alive 得到“不在”。
	operator.send(protocol.Message{
		Type:              protocol.TypeRequestSession,
		DeviceID:          testDeviceID,
		TemporaryPassword: testDevicePassword,
		Purpose:           "after device disconnect",
	})
	offline := operator.expect(protocol.TypeError)
	if offline.Code != "DEVICE_OFFLINE" {
		t.Fatalf("error code = %q, want DEVICE_OFFLINE", offline.Code)
	}
}
