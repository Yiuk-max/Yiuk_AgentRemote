package gateway

// 路由登记：告诉别人"这条连接挂在本节点上"。
//
// 只解决寻址，不涉及授权：core 拿到路由后仍然自己判断会话是否有效。
// 所有写入都是尽力而为——Redis 出错只记日志，不阻断连接建立与关闭，
// 因为连接本身还能用（core 查不到路由时会退回配置里的网关地址）。

import (
	"context"
	"time"
)

// routeWriteTimeout 是单次路由写入的上限。路由是旁路信息，不能拖慢收发。
const routeWriteTimeout = 2 * time.Second

// RouteStore 是"谁在哪"的登记处，由 registry.Registry 实现。
// 传 nil 表示不登记（进程内测试、单节点调试）。
type RouteStore interface {
	RegisterGateway(ctx context.Context, gatewayID, instanceID, pushAddr string) error
	PutConn(ctx context.Context, connectionID, gatewayID, instanceID string) error
	DropConn(ctx context.Context, connectionID, gatewayID, instanceID string) error
	PutDevice(ctx context.Context, deviceID, connectionID, gatewayID, instanceID string) error
	DropDevice(ctx context.Context, deviceID, connectionID, gatewayID, instanceID string) error
	RenewConns(ctx context.Context, gatewayID, instanceID string, connectionIDs []string) error
}

// publishGateway 登记本网关的 push 地址与本次启动标识，供 core 寻址。
func (s *Server) publishGateway(ctx context.Context) {
	if s.routes == nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, routeWriteTimeout)
	defer cancel()
	if err := s.routes.RegisterGateway(ctx, s.gatewayID, s.instanceID, s.config.GatewayGRPCAddr); err != nil {
		s.logger.Warn("gateway route registration failed", "gateway", s.gatewayID, "error", err)
	}
}

// publishConnection 登记一条新连接。
func (s *Server) publishConnection(ctx context.Context, connectionID string) {
	if s.routes == nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, routeWriteTimeout)
	defer cancel()
	if err := s.routes.PutConn(ctx, connectionID, s.gatewayID, s.instanceID); err != nil {
		s.logger.Warn("connection route registration failed", "connection", connectionID, "error", err)
	}
}

// forgetConnection 清理一条连接的全部路由。
// 连接断开和进程退出都会走到这里；本节点已经不在时这些键会自己超时。
func (s *Server) forgetConnection(ctx context.Context, clientConn *clientConnection) {
	if s.routes == nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, routeWriteTimeout)
	defer cancel()
	if deviceID := clientConn.publishedDeviceID; deviceID != "" {
		if err := s.routes.DropDevice(ctx, deviceID, clientConn.caller.ConnectionID, s.gatewayID, s.instanceID); err != nil {
			s.logger.Debug("device route cleanup failed", "device", deviceID, "error", err)
		}
	}
	if err := s.routes.DropConn(ctx, clientConn.caller.ConnectionID, s.gatewayID, s.instanceID); err != nil {
		s.logger.Debug("connection route cleanup failed", "connection", clientConn.caller.ConnectionID, "error", err)
	}
}

// syncDeviceRoute 在业务层确认设备身份后登记设备路由。
// 它对比上一次登记的值，只在设备 ID 变化时写一次；
// 设备换连接、或同一连接重新注册成别的设备都能覆盖到。
func (s *Server) syncDeviceRoute(ctx context.Context, clientConn *clientConnection) {
	if s.routes == nil {
		return
	}
	current := clientConn.caller.DeviceID
	if clientConn.caller.Role != "device" {
		current = ""
	}
	if current == clientConn.publishedDeviceID {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, routeWriteTimeout)
	defer cancel()
	if previous := clientConn.publishedDeviceID; previous != "" {
		if err := s.routes.DropDevice(ctx, previous, clientConn.caller.ConnectionID, s.gatewayID, s.instanceID); err != nil {
			s.logger.Debug("device route cleanup failed", "device", previous, "error", err)
		}
	}
	clientConn.publishedDeviceID = current
	if current == "" {
		return
	}
	if err := s.routes.PutDevice(ctx, current, clientConn.caller.ConnectionID, s.gatewayID, s.instanceID); err != nil {
		s.logger.Warn("device route registration failed", "device", current, "error", err)
	}
}

// runRouteKeepalive 续期本网关的存活键和名下连接路由，直到上下文取消。
// 周期取 TTL 的 1/3：两次失败也不会让键提前过期。
func (s *Server) runRouteKeepalive(ctx context.Context) {
	if s.routes == nil {
		return
	}
	interval := s.config.RouteTTL() / 3
	if interval < time.Second {
		interval = time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.publishGateway(ctx)
			connectionIDs := s.connectionIDs()
			if len(connectionIDs) == 0 {
				continue
			}
			renewCtx, cancel := context.WithTimeout(ctx, routeWriteTimeout)
			if err := s.routes.RenewConns(renewCtx, s.gatewayID, s.instanceID, connectionIDs); err != nil {
				s.logger.Warn("connection route renewal failed", "count", len(connectionIDs), "error", err)
			}
			cancel()
		}
	}
}

// connectionIDs 返回本节点当前持有的连接 ID，用于批量续期。
func (s *Server) connectionIDs() []string {
	s.connectionsMu.Lock()
	defer s.connectionsMu.Unlock()
	ids := make([]string, 0, len(s.connections))
	for connectionID := range s.connections {
		ids = append(ids, connectionID)
	}
	return ids
}
