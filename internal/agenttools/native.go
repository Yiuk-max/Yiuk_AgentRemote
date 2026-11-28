package agenttools

import (
	"context"
	"encoding/json"
	"errors"
)

// 本文件：组合工具走"设备原生通道"的公共入口。
//
// 原生通道（协议帧 execute_native）与 execute_command 共用会话授权、任务限额、
// request_id 幂等与取消；区别只在设备侧执行的是注册过的结构化操作，不是脚本文本。
//
// 这里把"后端是否支持原生通道"做成可选接口，而不是给 Backend 加必选方法：
// 本地 bridge（没有平台时的 demo 后端）不实现它，组合工具就继续走 PowerShell 脚本，
// 旧客户端的行为不受影响。

// NativeInvoker 由能下发 execute_native 的后端实现（目前是平台客户端）。
type NativeInvoker interface {
	InvokeNative(ctx context.Context, sessionID, operation string, arguments json.RawMessage, timeoutSeconds int) (json.RawMessage, error)
}

// errNativeUnsupported 表示后端根本没有原生通道。它与"设备不支持某个操作"
// （UNSUPPORTED_CLIENT / UNSUPPORTED_OPERATION）在调用方看来是同一类：
// 都应当回退到脚本路径。
var errNativeUnsupported = &Error{Code: "UNSUPPORTED_CLIENT", Message: "backend has no native operation channel"}

// invokeNative 提交一条原生操作并解码业务信封。
// arguments 可以是 json.RawMessage（原样透传）或任意可序列化结构。
func (c *Core) invokeNative(ctx context.Context, sessionID, operation string, arguments any, timeoutSeconds int) (json.RawMessage, error) {
	invoker, ok := c.backend.(NativeInvoker)
	if !ok {
		return nil, errNativeUnsupported
	}
	payload, err := marshalNativeArguments(arguments)
	if err != nil {
		return nil, err
	}
	return invoker.InvokeNative(ctx, sessionID, operation, payload, timeoutSeconds)
}

func marshalNativeArguments(arguments any) (json.RawMessage, error) {
	if arguments == nil {
		return json.RawMessage(`{}`), nil
	}
	if raw, ok := arguments.(json.RawMessage); ok {
		return raw, nil
	}
	encoded, err := json.Marshal(arguments)
	if err != nil {
		return nil, &Error{Code: "INVALID_ARGUMENT", Message: "could not encode native operation arguments"}
	}
	return encoded, nil
}

// isUnsupportedNative 判断错误是否表示"这个后端或设备没有该原生能力"，
// 这类错误允许调用方退回脚本路径；其余错误（超时、断线、业务失败）必须原样上报。
func isUnsupportedNative(err error) bool {
	var toolErr *Error
	if !errors.As(err, &toolErr) {
		return false
	}
	return toolErr.Code == "UNSUPPORTED_CLIENT" || toolErr.Code == "UNSUPPORTED_OPERATION"
}
