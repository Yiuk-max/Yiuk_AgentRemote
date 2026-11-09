// Package registry 用 Redis 记录"谁在哪"：网关存活、连接归属、设备路由。
//
// 它只解决寻址，不授予任何操作权：会话是否有效、命令是否允许，仍由 core 判断。
// 所有键都带 TTL：
//
//	agentremote:gateway:<gateway_id>      → "<instance_id> <push_addr>"
//	agentremote:route:conn:<conn_id>      → "<gateway_id> <instance_id>"
//	agentremote:route:device:<device_id>  → "<conn_id> <gateway_id> <instance_id>"
//
// 网关每 TTL/3 续期一次自己名下的键；进程被强杀时键自然过期，
// 谁也不会把残留键当成"这条连接还活着"。
package registry

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// ErrNotFound 表示这条路由不存在或已经过期。
var ErrNotFound = errors.New("registry: route not found")

// defaultPrefix 是键前缀，便于和同一个 Redis 里的其它数据分开。
const defaultPrefix = "agentremote:"

// compareAndDelete 只在值仍然属于自己时才删，避免旧连接的迟到清理删掉新路由。
var compareAndDelete = redis.NewScript(`
if redis.call('GET', KEYS[1]) == ARGV[1] then
	return redis.call('DEL', KEYS[1])
end
return 0
`)

// Registry 是 Redis 路由表的读写入口。零值不可用，请用 Open / New。
type Registry struct {
	client redis.UniversalClient
	ttl    time.Duration
	prefix string
}

// Options 描述如何连 Redis。
type Options struct {
	Addr     string
	Password string
	DB       int
	TTL      time.Duration
	Prefix   string
}

// Open 连接 Redis。它不会在启动时拨号：连不上时后续调用返回错误，
// 不会让进程起不来（运维可以先起服务再起 Redis）。
func Open(options Options) *Registry {
	if options.TTL <= 0 {
		options.TTL = 30 * time.Second
	}
	if options.Prefix == "" {
		options.Prefix = defaultPrefix
	}
	return &Registry{
		client: redis.NewClient(&redis.Options{
			Addr:     options.Addr,
			Password: options.Password,
			DB:       options.DB,
		}),
		ttl:    options.TTL,
		prefix: options.Prefix,
	}
}

// New 用已有客户端构造，供测试注入。
func New(client redis.UniversalClient, ttl time.Duration, prefix string) *Registry {
	if ttl <= 0 {
		ttl = 30 * time.Second
	}
	if prefix == "" {
		prefix = defaultPrefix
	}
	return &Registry{client: client, ttl: ttl, prefix: prefix}
}

// Close 关闭底层连接。
func (r *Registry) Close() error {
	if r == nil || r.client == nil {
		return nil
	}
	return r.client.Close()
}

// Ping 检查 Redis 是否可用。
func (r *Registry) Ping(ctx context.Context) error {
	if r == nil || r.client == nil {
		return errors.New("registry: not configured")
	}
	return r.client.Ping(ctx).Err()
}

func (r *Registry) gatewayKey(gatewayID string) string {
	return r.prefix + "gateway:" + gatewayID
}

func (r *Registry) connKey(connectionID string) string {
	return r.prefix + "route:conn:" + connectionID
}

func (r *Registry) deviceKey(deviceID string) string {
	return r.prefix + "route:device:" + deviceID
}

// RegisterGateway 登记本网关的 push 地址与本次启动标识，并续期。
func (r *Registry) RegisterGateway(ctx context.Context, gatewayID, instanceID, pushAddr string) error {
	if r == nil || r.client == nil {
		return errors.New("registry: not configured")
	}
	return r.client.Set(ctx, r.gatewayKey(gatewayID), instanceID+" "+pushAddr, r.ttl).Err()
}

// RenewGateway 只续期网关存活键，不改内容。
func (r *Registry) RenewGateway(ctx context.Context, gatewayID, instanceID, pushAddr string) error {
	return r.RegisterGateway(ctx, gatewayID, instanceID, pushAddr)
}

// PutConn 登记一条连接属于哪个网关。
func (r *Registry) PutConn(ctx context.Context, connectionID, gatewayID, instanceID string) error {
	if r == nil || r.client == nil {
		return errors.New("registry: not configured")
	}
	return r.client.Set(ctx, r.connKey(connectionID), gatewayID+" "+instanceID, r.ttl).Err()
}

// DropConn 删除连接归属，只在本网关仍是这条路由的主人时才删。
func (r *Registry) DropConn(ctx context.Context, connectionID, gatewayID, instanceID string) error {
	if r == nil || r.client == nil {
		return nil
	}
	return compareAndDelete.Run(ctx, r.client, []string{r.connKey(connectionID)}, gatewayID+" "+instanceID).Err()
}

// PutDevice 登记设备路由。<conn_id> 是设备当前连接，供跨网关寻址。
func (r *Registry) PutDevice(ctx context.Context, deviceID, connectionID, gatewayID, instanceID string) error {
	if r == nil || r.client == nil {
		return errors.New("registry: not configured")
	}
	return r.client.Set(ctx, r.deviceKey(deviceID), connectionID+" "+gatewayID+" "+instanceID, r.ttl).Err()
}

// DropDevice 删除设备路由，同样只在仍属于自己时才删。
func (r *Registry) DropDevice(ctx context.Context, deviceID, connectionID, gatewayID, instanceID string) error {
	if r == nil || r.client == nil {
		return nil
	}
	return compareAndDelete.Run(ctx, r.client, []string{r.deviceKey(deviceID)}, connectionID+" "+gatewayID+" "+instanceID).Err()
}

// RenewConns 一次性续期本节点名下的连接路由。
func (r *Registry) RenewConns(ctx context.Context, gatewayID, instanceID string, connectionIDs []string) error {
	if r == nil || r.client == nil || len(connectionIDs) == 0 {
		return nil
	}
	value := gatewayID + " " + instanceID
	pipe := r.client.Pipeline()
	for _, connectionID := range connectionIDs {
		pipe.Set(ctx, r.connKey(connectionID), value, r.ttl)
	}
	_, err := pipe.Exec(ctx)
	return err
}

// RenewDevices 一次性续期本节点名下的设备路由（route:device:<device_id>）。
//
// 设备路由和连接路由一样带 TTL，必须跟着连接一起续期：只续 route:conn 的话，
// 设备一直在线、TTL 一到 route:device 就消失，多网关部署下 core 按设备找网关
// 会查不到（单网关侥幸被配置里的兜底地址掩住）。
// connectionByDevice 是「设备 ID → 连接 ID」。
func (r *Registry) RenewDevices(ctx context.Context, gatewayID, instanceID string, connectionByDevice map[string]string) error {
	if r == nil || r.client == nil || len(connectionByDevice) == 0 {
		return nil
	}
	prefix := gatewayID + " " + instanceID
	pipe := r.client.Pipeline()
	for deviceID, connectionID := range connectionByDevice {
		if deviceID == "" || connectionID == "" {
			continue
		}
		pipe.Set(ctx, r.deviceKey(deviceID), connectionID+" "+prefix, r.ttl)
	}
	_, err := pipe.Exec(ctx)
	return err
}

// ConnRoute 是一条连接的路由结果。
type ConnRoute struct {
	GatewayID  string
	InstanceID string
}

// ResolveConn 查一条连接属于哪个网关。路由不存在时返回 ErrNotFound。
func (r *Registry) ResolveConn(ctx context.Context, connectionID string) (ConnRoute, error) {
	if r == nil || r.client == nil {
		return ConnRoute{}, errors.New("registry: not configured")
	}
	value, err := r.client.Get(ctx, r.connKey(connectionID)).Result()
	if errors.Is(err, redis.Nil) {
		return ConnRoute{}, ErrNotFound
	}
	if err != nil {
		return ConnRoute{}, err
	}
	fields := strings.Fields(value)
	if len(fields) != 2 {
		return ConnRoute{}, fmt.Errorf("registry: malformed conn route for %q", connectionID)
	}
	return ConnRoute{GatewayID: fields[0], InstanceID: fields[1]}, nil
}

// GatewayRoute 是一个网关的存活信息。
type GatewayRoute struct {
	InstanceID string
	PushAddr   string
}

// ResolveGateway 查网关当前这次启动的 push 地址。网关已经退出时返回 ErrNotFound。
func (r *Registry) ResolveGateway(ctx context.Context, gatewayID string) (GatewayRoute, error) {
	if r == nil || r.client == nil {
		return GatewayRoute{}, errors.New("registry: not configured")
	}
	value, err := r.client.Get(ctx, r.gatewayKey(gatewayID)).Result()
	if errors.Is(err, redis.Nil) {
		return GatewayRoute{}, ErrNotFound
	}
	if err != nil {
		return GatewayRoute{}, err
	}
	fields := strings.Fields(value)
	if len(fields) != 2 {
		return GatewayRoute{}, fmt.Errorf("registry: malformed gateway route for %q", gatewayID)
	}
	return GatewayRoute{InstanceID: fields[0], PushAddr: fields[1]}, nil
}

// DeviceRoute 是一个设备的路由结果。
type DeviceRoute struct {
	ConnectionID string
	GatewayID    string
	InstanceID   string
}

// ResolveDevice 查一个设备当前连在哪个连接、哪个网关上（P3 跨网关寻址用）。
func (r *Registry) ResolveDevice(ctx context.Context, deviceID string) (DeviceRoute, error) {
	if r == nil || r.client == nil {
		return DeviceRoute{}, errors.New("registry: not configured")
	}
	value, err := r.client.Get(ctx, r.deviceKey(deviceID)).Result()
	if errors.Is(err, redis.Nil) {
		return DeviceRoute{}, ErrNotFound
	}
	if err != nil {
		return DeviceRoute{}, err
	}
	fields := strings.Fields(value)
	if len(fields) != 3 {
		return DeviceRoute{}, fmt.Errorf("registry: malformed device route for %q", deviceID)
	}
	return DeviceRoute{ConnectionID: fields[0], GatewayID: fields[1], InstanceID: fields[2]}, nil
}
