// gateway 进程这一侧（gateway_for_core_use）：跑在 gateway 进程里的两半适配。
//
//   - PushServer：把本节点连接表暴露成 Push 服务，给 core 调用
//   - CoreClient：gateway 连 core 的 Edge 服务，实现 gateway.Edge
//
// 只被 cmd/platform-gateway 使用；单进程模式（cmd/platform）两条路径都是函数直调，
// 不经过这里。
package edge

import (
	"context"
	"errors"

	"agentremote/internal/platform/core"
	"agentremote/internal/platform/edge/edgepb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// ConnectionSet 是 Push 服务需要的连接表能力，由 gateway.Server 实现。
type ConnectionSet interface {
	Deliver(connectionID string, frame []byte) error
	Alive(connectionID string) bool
}

// PushServer 实现 edgepb.PushServer，把 core 的出站帧写进本节点连接表。
type PushServer struct {
	edgepb.UnimplementedPushServer
	connections ConnectionSet
}

// NewPushServer 包装一个连接表。
func NewPushServer(connections ConnectionSet) *PushServer {
	return &PushServer{connections: connections}
}

// Deliver 投递一帧。送不到返回 delivered=false，core 据此按“设备不可用”处理。
func (s *PushServer) Deliver(ctx context.Context, request *edgepb.DeliverRequest) (*edgepb.DeliverReply, error) {
	err := s.connections.Deliver(request.GetConnectionId(), request.GetFrame())
	return &edgepb.DeliverReply{Delivered: err == nil}, nil
}

// Alive 回答连接是否还挂在本节点。
func (s *PushServer) Alive(ctx context.Context, request *edgepb.AliveRequest) (*edgepb.AliveReply, error) {
	return &edgepb.AliveReply{Alive: s.connections.Alive(request.GetConnectionId())}, nil
}

// CoreClient 实现 gateway.Edge：把一帧转给 core 处理。
type CoreClient struct {
	client edgepb.EdgeClient
}

// NewCoreClient 连上 core 的 Edge 服务。
func NewCoreClient(address string) (*CoreClient, error) {
	conn, err := grpc.NewClient(address, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, err
	}
	return &CoreClient{client: edgepb.NewEdgeClient(conn)}, nil
}

// Handle 把一帧交给 core，并把 core 更新后的身份写回 caller。
// core 不可用时返回致命错误：这条连接没法继续服务，应当关闭。
func (c *CoreClient) Handle(caller *core.Caller, frame []byte) error {
	ctx, cancel := context.WithTimeout(context.Background(), rpcTimeout)
	defer cancel()
	reply, err := c.client.Handle(ctx, &edgepb.HandleRequest{Caller: CallerToProto(caller), Frame: frame})
	if err != nil {
		return &core.FatalError{Err: err}
	}
	if updated := reply.GetCaller(); updated != nil {
		*caller = *CallerFromProto(updated)
	}
	if reply.GetFatal() {
		return &core.FatalError{Frame: reply.GetFatalFrame(), Err: errors.New(reply.GetFatalError())}
	}
	return nil
}

// Detach 在连接关闭后通知 core。失败只影响收敛，不影响本地清理。
func (c *CoreClient) Detach(caller *core.Caller) {
	ctx, cancel := context.WithTimeout(context.Background(), rpcTimeout)
	defer cancel()
	if _, err := c.client.Detach(ctx, &edgepb.DetachRequest{Caller: CallerToProto(caller)}); err != nil {
		_ = err
	}
}
