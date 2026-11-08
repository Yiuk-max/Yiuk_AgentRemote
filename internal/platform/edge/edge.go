// Package edge 是 gateway 与 core 之间的 gRPC 契约与两端适配。
//
// 契约只有两个服务：
//   - Edge：core 提供，gateway 调用（Handle / Detach）
//   - Push：gateway 提供，core 调用（Deliver / Alive）
//
// 传的都是客户端原始 JSON 字节，这一层不做任何业务解释。
// 内网明文，不用TLS太麻烦
package edge

import (
	"agentremote/internal/platform/core"
	"agentremote/internal/platform/edge/edgepb"
)

// CallerToProto 把 core 的连接身份转成契约类型。
func CallerToProto(caller *core.Caller) *edgepb.Caller {
	if caller == nil {
		return nil
	}
	return &edgepb.Caller{
		ConnectionId: caller.ConnectionID,
		RemoteAddr:   caller.RemoteAddr,
		Role:         string(caller.Role),
		DeviceId:     caller.DeviceID,
		DisplayName:  caller.DisplayName,
		BusinessType: caller.BusinessType,
	}
}

// CallerFromProto 每次返回新对象：调用方可能就地修改它。
func CallerFromProto(pb *edgepb.Caller) *core.Caller {
	if pb == nil {
		return &core.Caller{}
	}
	return &core.Caller{
		ConnectionID: pb.GetConnectionId(),
		RemoteAddr:   pb.GetRemoteAddr(),
		Role:         core.Role(pb.GetRole()),
		DeviceID:     pb.GetDeviceId(),
		DisplayName:  pb.GetDisplayName(),
		BusinessType: pb.GetBusinessType(),
	}
}
