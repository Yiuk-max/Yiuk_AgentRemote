package edge

// 路由寻址：core 需要知道"这条连接挂在哪个网关"，再去那个网关投递。
//
// Redis 里查不到路由时退回配置里的网关地址，这样单网关部署即使 Redis 抖动
// 也不会把正在进行的协助打断；多网关时这条退路自然失效（连接不在那个网关上，
// 投递会返回 ErrNoRoute，结果仍然是"设备不可用"，不会错投）。

import (
	"context"
	"errors"
	"sync"
	"time"

	"agentremote/internal/platform/core"
	"agentremote/internal/platform/edge/edgepb"
	"agentremote/internal/platform/registry"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// lookupTimeout 是查一次路由的上限。路由查不到就当作不可投递，不无限等。
const lookupTimeout = 2 * time.Second

// deliverTimeout 是单次投递的上限。
//
// 投递发生在“处理来源连接这一帧”的路径上：多网关时它要跨到另一台网关，
// 如果那里卡住，超时时间就是来源连接被堵住的时长（文件传 512 块会累加）。
// 所以这里要比 rpcTimeout 短：宁可报“没送到”，也不能堵住对面。
const deliverTimeout = 2 * time.Second

// RouteResolver 查"连接在哪、网关在哪"，由 registry.Registry 实现。
type RouteResolver interface {
	ResolveConn(ctx context.Context, connectionID string) (registry.ConnRoute, error)
	ResolveGateway(ctx context.Context, gatewayID string) (registry.GatewayRoute, error)
}

// pushClient 是一个网关 Push 服务的连接。
type pushClient struct {
	conn   *grpc.ClientConn
	client edgepb.PushClient
}

func dialPush(address string) (*pushClient, error) {
	conn, err := grpc.NewClient(address, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, err
	}
	return &pushClient{conn: conn, client: edgepb.NewPushClient(conn)}, nil
}

func (c *pushClient) deliver(ctx context.Context, connectionID string, frame []byte) error {
	reply, err := c.client.Deliver(ctx, &edgepb.DeliverRequest{ConnectionId: connectionID, Frame: frame})
	if err != nil {
		return err
	}
	if !reply.GetDelivered() {
		return core.ErrNoRoute
	}
	return nil
}

func (c *pushClient) alive(ctx context.Context, connectionID string) (bool, error) {
	reply, err := c.client.Alive(ctx, &edgepb.AliveRequest{ConnectionId: connectionID})
	if err != nil {
		return false, err
	}
	return reply.GetAlive(), nil
}

// gatewayClient 缓存"某个网关某次启动"的连接，网关重启（instanceID 变了）就换掉。
type gatewayClient struct {
	instanceID string
	addr       string
	client     *pushClient
}

// routerLink 实现 core.Link：把core.Service的出站帧push到 gateway 的 Push 服务。
// 先查路由选出是哪个网关，查不到（或 Redis 不可用）才用兜底地址。
type routerLink struct {
	resolver     RouteResolver // nil 表示不做路由查询，只用 fallbackAddr
	fallbackAddr string        // 路由查不到时使用的网关地址

	clientsMu sync.Mutex
	clients   map[string]*gatewayClient // gateway_id → 连接
	fallback  *pushClient

	// 同一条连接的帧必须按顺序到达。core 的主要出站路径本身是串行的，
	// 只有过期清理会并发推送，所以这里按连接分片加锁就够了。
	shards [64]sync.Mutex //shards 是分片锁，按 connection_id 哈希分片，保证同一连接的帧按顺序投递
}

// NewRouterLink 返回 core 的出站出口：按 Redis 路由投递，
// 路由查不到（或 Redis 不可用）时退回 fallbackAddr。
func NewRouterLink(resolver RouteResolver, fallbackAddr string) core.Link {
	return &routerLink{
		resolver:     resolver,
		fallbackAddr: fallbackAddr,
		clients:      make(map[string]*gatewayClient),
	}
}

// NewPushClient 连上 gateway 的 Push 服务，返回 core 需要的出口。
// 这是只知道一个固定网关地址的简化版，测试与单网关调试用。
func NewPushClient(address string) (core.Link, error) {
	if address == "" {
		return nil, errors.New("edge: gateway address is required")
	}
	return NewRouterLink(nil, address), nil
}

// 给对应连接的分片锁上锁，保证同一连接的帧按顺序投递，这个只把锁返回，调用方要自己加锁和解锁
func (l *routerLink) lock(connectionID string) *sync.Mutex {
	hash := uint32(0)
	for i := 0; i < len(connectionID); i++ {
		hash = hash*31 + uint32(connectionID[i])
	}
	return &l.shards[hash%uint32(len(l.shards))]
}

// clientFor 找出这条连接应该投给哪个网关。
func (l *routerLink) clientFor(connectionID string) (*pushClient, error) {
	if l.resolver != nil {
		ctx, cancel := context.WithTimeout(context.Background(), lookupTimeout)
		route, routeErr := l.resolver.ResolveConn(ctx, connectionID)
		if routeErr == nil {
			gatewayRoute, gatewayErr := l.resolver.ResolveGateway(ctx, route.GatewayID)
			cancel()
			if gatewayErr != nil {
				// 路由还在但网关的存活键已经过期：那个网关已经死了。
				return nil, core.ErrNoRoute
			}
			return l.clientForGateway(route.GatewayID, gatewayRoute)
		}
		cancel()
		if !errors.Is(routeErr, registry.ErrNotFound) {
			// Redis 本身不可用：退回配置地址，别把正在进行的协助卡死。
			// 连接 ID 是随机生成的，投错网关也不会投到别的连接上。
			return l.fallbackClient()
		}
		// 路由不存在：连接可能刚建立还没登记，同样退回配置地址。
	}
	return l.fallbackClient()
}

// clientForGateway 取（或换掉）某个网关的连接。
func (l *routerLink) clientForGateway(gatewayID string, route registry.GatewayRoute) (*pushClient, error) {
	l.clientsMu.Lock()
	defer l.clientsMu.Unlock()
	if cached := l.clients[gatewayID]; cached != nil &&
		cached.instanceID == route.InstanceID && cached.addr == route.PushAddr {
		return cached.client, nil
	}
	client, err := dialPush(route.PushAddr)
	if err != nil {
		return nil, err
	}
	if previous := l.clients[gatewayID]; previous != nil {
		_ = previous.client.conn.Close()
	}
	l.clients[gatewayID] = &gatewayClient{instanceID: route.InstanceID, addr: route.PushAddr, client: client}
	return client, nil
}

// fallbackClient 取配置里那个网关的连接。
func (l *routerLink) fallbackClient() (*pushClient, error) {
	if l.fallbackAddr == "" {
		return nil, core.ErrNoRoute
	}
	l.clientsMu.Lock()
	defer l.clientsMu.Unlock()
	if l.fallback != nil {
		return l.fallback, nil
	}
	client, err := dialPush(l.fallbackAddr)
	if err != nil {
		return nil, err
	}
	l.fallback = client
	return client, nil
}

// Deliver 投递一帧；目标连接不在 gateway 上时返回 core.ErrNoRoute。
func (l *routerLink) Deliver(connectionID string, frame []byte) error {
	client, err := l.clientFor(connectionID)
	if err != nil {
		return err
	}
	mu := l.lock(connectionID) // 给对应连接的分片锁上锁，保证同一连接的帧按顺序投递
	mu.Lock()
	defer mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), deliverTimeout)
	defer cancel()
	return client.deliver(ctx, connectionID, frame)
}

// Alive 问 gateway 这条连接是否还挂着；问不到就当作不在。
func (l *routerLink) Alive(connectionID string) bool {
	client, err := l.clientFor(connectionID)
	if err != nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), rpcTimeout)
	defer cancel()
	alive, err := client.alive(ctx, connectionID)
	if err != nil {
		return false
	}
	return alive
}
