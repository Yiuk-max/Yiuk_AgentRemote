package registry_test

// 路由表的行为测试：登记、查询、过期、以及"旧连接清理不能删掉新路由"。

import (
	"context"
	"errors"
	"testing"
	"time"

	"agentremote/internal/platform/registry"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func newRegistry(t *testing.T, ttl time.Duration) (*registry.Registry, *miniredis.Miniredis) {
	t.Helper()
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	return registry.New(client, ttl, ""), server
}

func TestGatewayRegistration(t *testing.T) {
	routes, server := newRegistry(t, 30*time.Second)
	ctx := context.Background()

	if err := routes.RegisterGateway(ctx, "gw-1", "boot-a", "10.0.0.1:25671"); err != nil {
		t.Fatalf("register gateway: %v", err)
	}
	route, err := routes.ResolveGateway(ctx, "gw-1")
	if err != nil {
		t.Fatalf("resolve gateway: %v", err)
	}
	if route.InstanceID != "boot-a" || route.PushAddr != "10.0.0.1:25671" {
		t.Fatalf("gateway route = %+v", route)
	}

	// 网关重启后同名键指向新的启动标识；旧实例的信息必须被顶掉。
	if err := routes.RegisterGateway(ctx, "gw-1", "boot-b", "10.0.0.2:25671"); err != nil {
		t.Fatalf("re-register gateway: %v", err)
	}
	route, err = routes.ResolveGateway(ctx, "gw-1")
	if err != nil {
		t.Fatalf("resolve gateway after restart: %v", err)
	}
	if route.InstanceID != "boot-b" || route.PushAddr != "10.0.0.2:25671" {
		t.Fatalf("gateway route after restart = %+v", route)
	}

	// 没有续期时，存活键会在 TTL 后消失：core 据此判定网关已死。
	server.FastForward(31 * time.Second)
	if _, err := routes.ResolveGateway(ctx, "gw-1"); !errors.Is(err, registry.ErrNotFound) {
		t.Fatalf("expired gateway route error = %v, want ErrNotFound", err)
	}
}

func TestConnRouteLastWriterWins(t *testing.T) {
	routes, _ := newRegistry(t, 30*time.Second)
	ctx := context.Background()

	// 同一台网关重启前后用了同一个 gateway_id，但启动标识不同。
	if err := routes.PutConn(ctx, "conn-1", "gw-1", "boot-b"); err != nil {
		t.Fatalf("put conn: %v", err)
	}
	// 迟到的旧实例清理不能删掉新路由。
	if err := routes.DropConn(ctx, "conn-1", "gw-1", "boot-a"); err != nil {
		t.Fatalf("drop stale conn: %v", err)
	}
	route, err := routes.ResolveConn(ctx, "conn-1")
	if err != nil {
		t.Fatalf("connection route must survive a stale cleanup: %v", err)
	}
	if route.GatewayID != "gw-1" || route.InstanceID != "boot-b" {
		t.Fatalf("connection route = %+v", route)
	}

	// 本人清理必须生效。
	if err := routes.DropConn(ctx, "conn-1", "gw-1", "boot-b"); err != nil {
		t.Fatalf("drop own conn: %v", err)
	}
	if _, err := routes.ResolveConn(ctx, "conn-1"); !errors.Is(err, registry.ErrNotFound) {
		t.Fatalf("dropped connection route error = %v, want ErrNotFound", err)
	}
}

func TestDeviceRoute(t *testing.T) {
	routes, _ := newRegistry(t, 30*time.Second)
	ctx := context.Background()

	if err := routes.PutDevice(ctx, "123456789", "conn-1", "gw-1", "boot-a"); err != nil {
		t.Fatalf("put device: %v", err)
	}
	route, err := routes.ResolveDevice(ctx, "123456789")
	if err != nil {
		t.Fatalf("resolve device: %v", err)
	}
	if route.ConnectionID != "conn-1" || route.GatewayID != "gw-1" || route.InstanceID != "boot-a" {
		t.Fatalf("device route = %+v", route)
	}

	// 设备换连接：新值的清理键值对不上时不能删。
	if err := routes.PutDevice(ctx, "123456789", "conn-2", "gw-1", "boot-a"); err != nil {
		t.Fatalf("put device on new connection: %v", err)
	}
	if err := routes.DropDevice(ctx, "123456789", "conn-1", "gw-1", "boot-a"); err != nil {
		t.Fatalf("drop stale device route: %v", err)
	}
	route, err = routes.ResolveDevice(ctx, "123456789")
	if err != nil {
		t.Fatalf("device route must survive a stale cleanup: %v", err)
	}
	if route.ConnectionID != "conn-2" {
		t.Fatalf("device route connection = %q, want conn-2", route.ConnectionID)
	}
}

func TestRenewDevices(t *testing.T) {
	routes, server := newRegistry(t, 30*time.Second)
	ctx := context.Background()

	if err := routes.PutConn(ctx, "conn-1", "gw-1", "boot-a"); err != nil {
		t.Fatalf("put conn: %v", err)
	}
	if err := routes.PutDevice(ctx, "123456789", "conn-1", "gw-1", "boot-a"); err != nil {
		t.Fatalf("put device: %v", err)
	}
	// 设备路由必须和连接路由一起续期：只续连接的话设备键会先过期。
	server.FastForward(25 * time.Second)
	if err := routes.RenewConns(ctx, "gw-1", "boot-a", []string{"conn-1"}); err != nil {
		t.Fatalf("renew conns: %v", err)
	}
	if err := routes.RenewDevices(ctx, "gw-1", "boot-a", map[string]string{"123456789": "conn-1"}); err != nil {
		t.Fatalf("renew devices: %v", err)
	}
	server.FastForward(25 * time.Second)
	route, err := routes.ResolveDevice(ctx, "123456789")
	if err != nil {
		t.Fatalf("renewed device route expired early: %v", err)
	}
	if route.ConnectionID != "conn-1" || route.GatewayID != "gw-1" || route.InstanceID != "boot-a" {
		t.Fatalf("renewed device route = %+v", route)
	}
	// 停止续期后，设备路由应当在 TTL 后自然过期。
	server.FastForward(31 * time.Second)
	if _, err := routes.ResolveDevice(ctx, "123456789"); !errors.Is(err, registry.ErrNotFound) {
		t.Fatalf("device route error after TTL = %v, want ErrNotFound", err)
	}
}

func TestRenewConns(t *testing.T) {
	routes, server := newRegistry(t, 30*time.Second)
	ctx := context.Background()

	if err := routes.PutConn(ctx, "conn-1", "gw-1", "boot-a"); err != nil {
		t.Fatalf("put conn: %v", err)
	}
	// 续期会把 TTL 重置，所以推进 25 秒后再续期，键还能活过原来的过期点。
	server.FastForward(25 * time.Second)
	if err := routes.RenewConns(ctx, "gw-1", "boot-a", []string{"conn-1"}); err != nil {
		t.Fatalf("renew conns: %v", err)
	}
	server.FastForward(25 * time.Second)
	if _, err := routes.ResolveConn(ctx, "conn-1"); err != nil {
		t.Fatalf("renewed connection route expired early: %v", err)
	}
	server.FastForward(31 * time.Second)
	if _, err := routes.ResolveConn(ctx, "conn-1"); !errors.Is(err, registry.ErrNotFound) {
		t.Fatalf("connection route error after TTL = %v, want ErrNotFound", err)
	}
}

func TestNotConfigured(t *testing.T) {
	var routes *registry.Registry
	if err := routes.Ping(context.Background()); err == nil {
		t.Fatal("nil registry must report an error")
	}
	if _, err := routes.ResolveConn(context.Background(), "conn-1"); err == nil {
		t.Fatal("nil registry must report an error")
	}
}
