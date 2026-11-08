// core 进程这一侧（core_for_gateway_use）：跑在 core 进程里的两半适配。
//
//   - CoreServer：把 core.Service 暴露成 Edge 服务，给 gateway 调用
//   - pushLink / NewPushClient：core 连 gateway 的 Push 服务，实现 core.Link
//     （P2 起按路由选网关，实现放在 core_router.go）
//
// 只被 cmd/platform-core 使用；进程内直连的两条路径（测试、本地调试）不走 gRPC。
package edge

import (
	"context"
	"errors"
	"time"

	"agentremote/internal/platform/core"
	"agentremote/internal/platform/edge/edgepb"
)

// rpcTimeout 是 core 与 gateway 之间单次调用的上限。
// 超过就按“送不到”处理，不无限等待。
const rpcTimeout = 5 * time.Second

// CoreServer 实现 edgepb.EdgeServer，把 gRPC 请求交给 core.Service。
type CoreServer struct {
	edgepb.UnimplementedEdgeServer
	service *core.Service
}

// NewCoreServer 包装一个业务层。
func NewCoreServer(service *core.Service) *CoreServer {
	return &CoreServer{service: service}
}

// Handle 处理一条连接上的一帧。
// 业务层的致命错误通过 reply 返回，不当作 gRPC 错误，避免 gateway 重试。
func (s *CoreServer) Handle(ctx context.Context, request *edgepb.HandleRequest) (*edgepb.HandleReply, error) {
	caller := CallerFromProto(request.GetCaller()) // 还原连接身份
	reply := &edgepb.HandleReply{}                 // 回复
	// 交给业务层core handle 处理
	if err := s.service.Handle(caller, request.GetFrame()); err != nil {
		var fatalError *core.FatalError
		if errors.As(err, &fatalError) {
			reply.Fatal = true
			reply.FatalFrame = fatalError.Frame
			reply.FatalError = fatalError.Error()
		} else {
			reply.Fatal = true
			reply.FatalError = err.Error()
		}
	}
	// Handle 可能更新了身份（认证后 role、注册后 device_id）。
	reply.Caller = CallerToProto(caller)
	return reply, nil
}

// Detach 在连接关闭后收敛该连接名下的会话。
func (s *CoreServer) Detach(ctx context.Context, request *edgepb.DetachRequest) (*edgepb.DetachReply, error) {
	s.service.Detach(CallerFromProto(request.GetCaller())) // 交给业务层收敛
	return &edgepb.DetachReply{}, nil
}
