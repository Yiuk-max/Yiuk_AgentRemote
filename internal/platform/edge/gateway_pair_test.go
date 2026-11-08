package edge_test

// P3 的核心验收：操作者连 gw-1、设备连 gw-2，两条连接分处不同网关，
// 所有帧都必须经 core 跨网关投递；设备所在网关退出后，会话要靠 Detach 收敛。

import (
	"context"
	"errors"
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
	"agentremote/internal/platform/registry"
	"agentremote/internal/platform/transport"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"google.golang.org/grpc"
)

// startGateway 起一个完整网关：WSS 监听给客户端，Push gRPC 监听给 core。
// 返回网关实例与客户端应连的 ws 地址。
func startGateway(t *testing.T, gatewayID string, routes *registry.Registry, edgeClient *edge.CoreClient) (*gateway.Server, string) {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	pushListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Listen = "127.0.0.1:0"
	cfg.AllowInsecure = true
	cfg.PlayerPassword = testPlayerPassword
	cfg.HandshakeTimeoutSec = 5
	cfg.HeartbeatTimeoutSec = 30
	cfg.MaxOutputBytes = 1 << 20
	cfg.GatewayID = gatewayID
	cfg.RouteTTLSec = 30
	cfg.GatewayGRPCAddr = pushListener.Addr().String()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("gateway %s config: %v", gatewayID, err)
	}

	rawListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	wsListener := transport.ServeWebSocketListener(rawListener, cfg.MaxFrameBytes)
	gw := gateway.NewWithListener(cfg, logger, wsListener, routes)
	gw.Bind(edgeClient)

	grpcServer := grpc.NewServer()
	edgepb.RegisterPushServer(grpcServer, edge.NewPushServer(gw))
	go func() { _ = grpcServer.Serve(pushListener) }()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = gw.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		gw.Close()
		<-done
		grpcServer.Stop()
	})

	return gw, "ws://" + wsListener.Addr().String()
}

func TestCrossGatewayDelivery(t *testing.T) {
	redisServer := miniredis.RunT(t)
	redisClient := redis.NewClient(&redis.Options{Addr: redisServer.Addr()})
	t.Cleanup(func() { _ = redisClient.Close() })
	routes := registry.New(redisClient, 30*time.Second, "")

	// core：只一个，出口不留兜底地址，所以每一帧都必须真的查到路由才送得出去。
	cfg := config.Default()
	cfg.AllowInsecure = true // core 不开客户端监听，TLS 字段对它无意义
	cfg.PlayerPassword = testPlayerPassword
	cfg.HandshakeTimeoutSec = 5
	cfg.HeartbeatTimeoutSec = 30
	cfg.MaxOutputBytes = 1 << 20
	cfg.RouteTTLSec = 30
	if err := cfg.Validate(); err != nil {
		t.Fatalf("core config: %v", err)
	}
	service := core.New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), edge.NewRouterLink(routes, ""))

	coreListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	coreServer := grpc.NewServer()
	edgepb.RegisterEdgeServer(coreServer, edge.NewCoreServer(service))
	go func() { _ = coreServer.Serve(coreListener) }()
	t.Cleanup(coreServer.Stop)

	coreClient, err := edge.NewCoreClient(coreListener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = service.Run(ctx) }()

	_, wsOne := startGateway(t, "gw-1", routes, coreClient)
	gatewayTwo, wsTwo := startGateway(t, "gw-2", routes, coreClient)

	// 关键前提：操作者和设备连在不同的网关上。
	operator, operatorConnID := dialAndAuthenticate(t, wsOne)
	device, deviceConnID := dialAndAuthenticate(t, wsTwo)

	for _, want := range []struct {
		connectionID string
		gatewayID    string
	}{{operatorConnID, "gw-1"}, {deviceConnID, "gw-2"}} {
		route, err := routes.ResolveConn(ctx, want.connectionID)
		if err != nil {
			t.Fatalf("connection route for %s: %v", want.connectionID, err)
		}
		if route.GatewayID != want.gatewayID {
			t.Fatalf("connection %s is on %q, want %q", want.connectionID, route.GatewayID, want.gatewayID)
		}
	}

	// 设备在 gw-2 上注册。
	device.send(protocol.Message{
		Type:              protocol.TypeRegisterDevice,
		DeviceID:          testDeviceID,
		DeviceName:        "cross-gateway-device",
		Platform:          "windows",
		TemporaryPassword: testDevicePassword,
		Mode:              protocol.ModeDirect,
	})
	device.expect(protocol.TypeDeviceRegistered)
	waitFor(t, "device route on gw-2", func() bool {
		route, err := routes.ResolveDevice(ctx, testDeviceID)
		return err == nil && route.ConnectionID == deviceConnID && route.GatewayID == "gw-2"
	})

	// 申请会话：core 要靠路由 + Alive 跨到 gw-2 去问设备连接。
	operator.send(protocol.Message{
		Type:              protocol.TypeRequestSession,
		DeviceID:          testDeviceID,
		TemporaryPassword: testDevicePassword,
		Purpose:           "cross gateway test",
	})
	created := operator.expect(protocol.TypeSessionCreated)
	if created.Status != protocol.StatusApproved {
		t.Fatalf("session status = %q", created.Status)
	}
	device.expect(protocol.TypeSessionStatus)

	// 命令去程：gw-1 → core → gw-2
	operator.send(protocol.Message{
		Type:      protocol.TypeExecuteCommand,
		SessionID: created.SessionID,
		RequestID: "cross-gateway-request",
		Command:   "Get-Location",
	})
	operator.expect(protocol.TypeCommandAccepted)
	forwarded := device.expect(protocol.TypeExecuteCommand)
	if forwarded.Command != "Get-Location" {
		t.Fatalf("forwarded command = %q", forwarded.Command)
	}

	// 结果回程：gw-2 → core → gw-1（含输出帧，顺序不能乱）
	device.send(protocol.Message{Type: protocol.TypeCommandOutput, SessionID: created.SessionID, RequestID: forwarded.RequestID, Stream: "stdout", Data: "C:\\cross\n"})
	device.send(protocol.Message{Type: protocol.TypeCommandResult, SessionID: created.SessionID, RequestID: forwarded.RequestID, Status: protocol.TaskSucceeded, ResultKnown: true})
	if output := operator.expect(protocol.TypeCommandOutput); output.Data != "C:\\cross\n" {
		t.Fatalf("relayed output = %q", output.Data)
	}
	if result := operator.expect(protocol.TypeCommandResult); result.Status != protocol.TaskSucceeded {
		t.Fatalf("command status = %q", result.Status)
	}

	// 设备所在网关退出：连接关闭 → Detach → core 中断会话并通知另一台网关上的操作者。
	gatewayTwo.Close()
	if interrupted := operator.expect(protocol.TypeSessionStatus); interrupted.Status != protocol.StatusInterrupted {
		t.Fatalf("session status after gateway shutdown = %q", interrupted.Status)
	}
	waitFor(t, "device route cleanup", func() bool {
		_, err := routes.ResolveDevice(ctx, testDeviceID)
		return errors.Is(err, registry.ErrNotFound)
	})
	waitFor(t, "device connection route cleanup", func() bool {
		_, err := routes.ResolveConn(ctx, deviceConnID)
		return errors.Is(err, registry.ErrNotFound)
	})

	// 另一台网关上的操作者连接不受影响：仍然可以继续用（比如申请别的设备）。
	operator.send(protocol.Message{Type: protocol.TypeListDevices})
	operator.expect(protocol.TypeDeviceList)
}
