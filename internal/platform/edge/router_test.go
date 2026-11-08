package edge_test

// P2 的路由行为：
//   - 网关把"连接挂在本节点"写进 Redis，core 靠路由找到它再投递
//   - 路由查不到且没有兜底地址时，投递必须失败（不能假装送到了）
//   - 连接关闭后，连接与设备路由都要被清掉

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
	"github.com/gorilla/websocket"
	"github.com/redis/go-redis/v9"
	"google.golang.org/grpc"
)

// 不需要 Redis 的场景下，路由查不到就必须报错，而不是当成投递成功。
type missingResolver struct{}

func (missingResolver) ResolveConn(ctx context.Context, connectionID string) (registry.ConnRoute, error) {
	return registry.ConnRoute{}, registry.ErrNotFound
}

func (missingResolver) ResolveGateway(ctx context.Context, gatewayID string) (registry.GatewayRoute, error) {
	return registry.GatewayRoute{}, registry.ErrNotFound
}

func TestRouterLinkWithoutRoute(t *testing.T) {
	// 没有兜底地址：路由不存在时不能投递。
	link := edge.NewRouterLink(missingResolver{}, "")
	if err := link.Deliver("conn-missing", []byte(`{"type":"ping"}`)); !errors.Is(err, core.ErrNoRoute) {
		t.Fatalf("deliver without route = %v, want ErrNoRoute", err)
	}
	if link.Alive("conn-missing") {
		t.Fatal("alive without route must be false")
	}

	// 有兜底地址时不是 ErrNoRoute：它会真的去试那个地址（这里地址是空的，
	// 所以只断言“不是路由缺失”，不断言具体错误）。
	fallbackLink := edge.NewRouterLink(missingResolver{}, "127.0.0.1:1")
	if err := fallbackLink.Deliver("conn-missing", []byte(`{"type":"ping"}`)); errors.Is(err, core.ErrNoRoute) {
		t.Fatal("with a fallback address the delivery must be attempted, not rejected as unroutable")
	}
}

func TestTwoProcessFlowViaRedisRoutes(t *testing.T) {
	redisServer := miniredis.RunT(t)
	redisClient := redis.NewClient(&redis.Options{Addr: redisServer.Addr()})
	t.Cleanup(func() { _ = redisClient.Close() })

	cfg := config.Default()
	cfg.Listen = "127.0.0.1:0"
	cfg.AllowInsecure = true
	cfg.PlayerPassword = testPlayerPassword
	cfg.HandshakeTimeoutSec = 5
	cfg.HeartbeatTimeoutSec = 30
	cfg.MaxOutputBytes = 1 << 20
	cfg.GatewayID = "gw-test"
	cfg.RouteTTLSec = 30
	if err := cfg.Validate(); err != nil {
		t.Fatalf("test config: %v", err)
	}
	routes := registry.New(redisClient, cfg.RouteTTL(), "")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	// 网关把自己的 Push 地址登记进 Redis，core 靠它来投递；
	// 所以要先拿到实际端口，再创建网关。
	pushListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	cfg.GatewayGRPCAddr = pushListener.Addr().String()

	rawListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	wsListener := transport.ServeWebSocketListener(rawListener, cfg.MaxFrameBytes)
	gw := gateway.NewWithListener(cfg, logger, wsListener, routes)

	pushServer := grpc.NewServer()
	edgepb.RegisterPushServer(pushServer, edge.NewPushServer(gw))
	go func() { _ = pushServer.Serve(pushListener) }()

	// 不给兜底地址：所有投递都必须真的经过 Redis 路由。
	service := core.New(cfg, logger, edge.NewRouterLink(routes, ""))

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
	operator, operatorID := dialAndAuthenticate(t, address)
	device, deviceConnID := dialAndAuthenticate(t, address)

	// 网关登记了存活键。
	if _, err := routes.ResolveGateway(ctx, cfg.GatewayID); err != nil {
		t.Fatalf("gateway route missing after start: %v", err)
	}
	// 两条连接都登记了归属。
	for _, connectionID := range []string{operatorID, deviceConnID} {
		if _, err := routes.ResolveConn(ctx, connectionID); err != nil {
			t.Fatalf("connection route missing for %s: %v", connectionID, err)
		}
	}

	device.send(protocol.Message{
		Type:              protocol.TypeRegisterDevice,
		DeviceID:          testDeviceID,
		DeviceName:        "redis-route-device",
		Platform:          "windows",
		TemporaryPassword: testDevicePassword,
		Mode:              protocol.ModeDirect,
	})
	registered := device.expect(protocol.TypeDeviceRegistered)
	if registered.DeviceID != testDeviceID {
		t.Fatalf("device_id = %q, want %q", registered.DeviceID, testDeviceID)
	}
	// 设备路由指向这条设备连接。网关是在把回应写回客户端之后才登记设备路由的，
	// 所以这里要轮询等一下，不能假设“收到回应 == 路由已就绪”。
	var deviceRoute registry.DeviceRoute
	waitFor(t, "device route registration", func() bool {
		route, err := routes.ResolveDevice(ctx, testDeviceID)
		if err != nil {
			return false
		}
		deviceRoute = route
		return true
	})
	if deviceRoute.ConnectionID != deviceConnID || deviceRoute.GatewayID != cfg.GatewayID {
		t.Fatalf("device route = %+v", deviceRoute)
	}

	// 直接模式申请会话：core 必须靠路由 + Alive 确认设备连接还在。
	operator.send(protocol.Message{
		Type:              protocol.TypeRequestSession,
		DeviceID:          testDeviceID,
		TemporaryPassword: testDevicePassword,
		Purpose:           "redis route test",
	})
	created := operator.expect(protocol.TypeSessionCreated)
	if created.Status != protocol.StatusApproved {
		t.Fatalf("session status = %q", created.Status)
	}
	device.expect(protocol.TypeSessionStatus)

	// 命令往返都要经过路由投递。
	operator.send(protocol.Message{
		Type:      protocol.TypeExecuteCommand,
		SessionID: created.SessionID,
		RequestID: "redis-route-request",
		Command:   "Get-Location",
	})
	operator.expect(protocol.TypeCommandAccepted)
	forwarded := device.expect(protocol.TypeExecuteCommand)
	device.send(protocol.Message{Type: protocol.TypeCommandResult, SessionID: created.SessionID, RequestID: forwarded.RequestID, Status: protocol.TaskSucceeded, ResultKnown: true})
	if result := operator.expect(protocol.TypeCommandResult); result.Status != protocol.TaskSucceeded {
		t.Fatalf("command status = %q", result.Status)
	}

	// 设备断线：连接路由和设备路由都要被清掉，会话要中断。
	_ = device.conn.Close()
	if interrupted := operator.expect(protocol.TypeSessionStatus); interrupted.Status != protocol.StatusInterrupted {
		t.Fatalf("session status after device disconnect = %q", interrupted.Status)
	}
	waitFor(t, "device route cleanup", func() bool {
		_, err := routes.ResolveDevice(ctx, testDeviceID)
		return errors.Is(err, registry.ErrNotFound)
	})
	waitFor(t, "connection route cleanup", func() bool {
		_, err := routes.ResolveConn(ctx, deviceConnID)
		return errors.Is(err, registry.ErrNotFound)
	})
}

// dialAndAuthenticate 连上网关并完成 player 认证，返回客户端与它的连接 ID。
func dialAndAuthenticate(t *testing.T, address string) (*testClient, string) {
	t.Helper()
	conn, _, err := websocket.DefaultDialer.Dial(address, nil)
	if err != nil {
		t.Fatalf("dial %s: %v", address, err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	client := &testClient{t: t, conn: conn}
	client.send(protocol.Message{Type: protocol.TypeAuthenticatePlayer, IdentityType: "player", PlayerPassword: testPlayerPassword})
	authenticated := client.expect(protocol.TypeAuthenticated)
	if authenticated.ConnectionID == "" {
		t.Fatal("authenticated reply has no connection_id")
	}
	return client, authenticated.ConnectionID
}

// waitFor 轮询等待一个条件成立，最多 2 秒。
func waitFor(t *testing.T, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}
