package platformclient

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"agentremote/internal/agenttools"
	"agentremote/internal/platform/protocol"
)

// 原生操作（视觉定位、鼠标动作）的客户端入口。
//
// 这里刻意不注册成 MCP 工具：工具契约（find_on_screen / click_target / drag）
// 在 P3 才冻结，先只把"提交 → 能力校验 → 结果解码"这条内部通路做好并测通。
// 将来工具 handler 只需要解析参数后调用 InvokeNative。
//
// 协议上它与 execute_command 共用任务、幂等与取消：设备回传的 stdout 是业务信封
// {"ok":true,"data":...} 或 {"ok":false,"code":...}，任务状态只表示执行生命周期。

const (
	nativeDefaultTimeoutSeconds = 10
	nativeMaxTimeoutSeconds     = 25
)

// InvokeNative 提交一条白名单内的原生操作，等待终态并解码业务信封。
func (c *Client) InvokeNative(ctx context.Context, sessionID, operation string, arguments json.RawMessage, timeoutSeconds int) (json.RawMessage, error) {
	sessionID = strings.TrimSpace(sessionID)
	operation = strings.TrimSpace(operation)
	if sessionID == "" || operation == "" {
		return nil, &agenttools.Error{Code: "INVALID_ARGUMENT", Message: "session_id and operation are required"}
	}
	if len(arguments) == 0 {
		arguments = json.RawMessage(`{}`)
	}
	if !json.Valid(arguments) {
		return nil, &agenttools.Error{Code: "INVALID_ARGUMENT", Message: "arguments must be valid JSON"}
	}
	timeout := timeoutSeconds
	if timeout <= 0 {
		timeout = nativeDefaultTimeoutSeconds
	}
	if timeout > nativeMaxTimeoutSeconds {
		timeout = nativeMaxTimeoutSeconds
	}

	// 先看设备自报的能力：缺失就不发请求，并把"该升级客户端"说清楚。
	// 平台侧还会再校验一次，两条检查互不替代。
	device, err := c.sessionDevice(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if !deviceSupports(device.Capabilities, operation) {
		// 客户端可能持有旧快照（设备在我们上次拉列表之后才升级/降级）。
		// 能力缺失时刷新一次再判断，避免把"刚升级完"误报成 UNSUPPORTED_CLIENT。
		if refreshed, refreshErr := c.sessionDeviceFresh(ctx, sessionID); refreshErr == nil {
			device = refreshed
		}
	}
	if !deviceSupports(device.Capabilities, operation) {
		return nil, &agenttools.Error{
			Code:    "UNSUPPORTED_CLIENT",
			Message: "device does not advertise " + operation,
			Hint:    "该设备的客户端版本不支持这个操作；请设备所有者升级 Qt 客户端后重试（旧工具不受影响）",
			Data:    map[string]any{"device_id": device.DeviceID, "client_version": device.ClientVersion},
		}
	}

	requestID := randomID()
	response, err := c.sendRequest(ctx, protocol.Message{
		Type:           protocol.TypeExecuteNative,
		SessionID:      sessionID,
		RequestID:      requestID,
		Operation:      operation,
		Arguments:      arguments,
		TimeoutSeconds: timeout,
	}, func(message protocol.Message) bool {
		return message.Type == protocol.TypeCommandAccepted
	})
	if err != nil {
		return nil, err
	}
	if isTerminalTaskStatus(response.Status) {
		// 平台在重复 request_id 时会直接返回终态，此时没有必要再轮询。
		return nil, &agenttools.Error{
			Code:    "REQUEST_ALREADY_FINISHED",
			Message: "operation with this request id already finished: " + response.Status,
		}
	}

	// 设备侧超时 + 传输余量；任务超时由平台自己收敛，这里只兜住"没有回音"。
	wait := time.Duration(timeout)*time.Second + 20*time.Second
	result, err := c.waitForCommandResult(ctx, sessionID, requestID, wait)
	if err != nil {
		return nil, err
	}
	return decodeNativeResult(operation, result)
}

// decodeNativeResult 把任务终态 + stdout 信封转换成工具层可用的结果或结构化错误。
func decodeNativeResult(operation string, result map[string]any) (json.RawMessage, error) {
	status, _ := result["status"].(string)
	stdout, _ := result["stdout"].(string)
	resultKnown, _ := result["result_known"].(bool)

	// 断线/超时的任务是"可能已生效"，必须与 failed 区分开。
	if status == protocol.TaskResultUnknown && !resultKnown {
		return nil, &agenttools.Error{
			Code:    "RESULT_UNKNOWN",
			Message: operation + " 的结果未知（设备可能已执行），不要直接重试",
			Hint:    "先 take_screenshot 核对设备当前画面，再决定是否重新发起",
			Data:    map[string]any{"status": status, "stderr": result["stderr"]},
		}
	}
	if status != protocol.TaskSucceeded {
		stderr, _ := result["stderr"].(string)
		return nil, &agenttools.Error{
			Code:    "NATIVE_OPERATION_FAILED",
			Message: "device reported " + status + ": " + truncateForError(stderr),
			Hint:    "确认会话仍在、设备未锁屏；必要时重新截图后再试",
		}
	}

	var envelope struct {
		OK      bool            `json:"ok"`
		Code    string          `json:"code"`
		Message string          `json:"message"`
		Hint    string          `json:"hint"`
		Data    json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal([]byte(stdout), &envelope); err != nil {
		return nil, &agenttools.Error{
			Code:    "NATIVE_RESULT_INVALID",
			Message: "device returned a malformed native result envelope",
			Hint:    "客户端与 MCP 的版本可能不匹配；升级到同一发布批次后重试",
		}
	}
	if !envelope.OK {
		code := envelope.Code
		if code == "" {
			code = "NATIVE_OPERATION_FAILED"
		}
		return nil, &agenttools.Error{Code: code, Message: envelope.Message, Hint: envelope.Hint}
	}
	if len(envelope.Data) == 0 {
		return json.RawMessage(`{}`), nil
	}
	return envelope.Data, nil
}

// sessionDevice 解析会话所属设备：先读缓存，缺失时向平台查一次会话状态与设备列表。
//
// 缓存本身就是"最近一次看到的设备快照"，设备重连会刷新能力；
// 这里额外刷新一次列表，保证升级后的客户端不会被旧能力挡住。
func (c *Client) sessionDevice(ctx context.Context, sessionID string) (protocol.DeviceInfo, error) {
	deviceID := c.cachedSessionDeviceID(sessionID)
	if deviceID == "" {
		response, err := c.sendRequest(ctx, protocol.Message{
			Type: protocol.TypeSessionStatus, SessionID: sessionID,
		}, func(message protocol.Message) bool {
			return message.Type == protocol.TypeSessionStatus
		})
		if err != nil {
			return protocol.DeviceInfo{}, err
		}
		if response.Session != nil {
			deviceID = response.Session.DeviceID
		}
	}
	if deviceID == "" {
		return protocol.DeviceInfo{}, &agenttools.Error{
			Code: "SESSION_NOT_FOUND", Message: "session has no device binding",
			Hint: "先调用 get_session_status 或 request_connection",
		}
	}
	if device, ok := c.cachedDeviceByID(deviceID); ok {
		return device, nil
	}
	// 缓存里没有（例如刚连上平台）：拉一次设备列表再查。
	if _, err := c.handleListDevices(ctx); err != nil {
		return protocol.DeviceInfo{}, err
	}
	if device, ok := c.cachedDeviceByID(deviceID); ok {
		return device, nil
	}
	return protocol.DeviceInfo{}, &agenttools.Error{
		Code: "DEVICE_NOT_FOUND", Message: "device of this session is no longer listed",
		Hint: "平台是内存态，设备可能已离线；调用 list_devices 确认",
	}
}

// sessionDeviceFresh 强制刷新设备列表后再解析会话所属设备。
func (c *Client) sessionDeviceFresh(ctx context.Context, sessionID string) (protocol.DeviceInfo, error) {
	if _, err := c.handleListDevices(ctx); err != nil {
		return protocol.DeviceInfo{}, err
	}
	deviceID := c.cachedSessionDeviceID(sessionID)
	if deviceID == "" {
		return protocol.DeviceInfo{}, &agenttools.Error{Code: "SESSION_NOT_FOUND", Message: "session has no device binding"}
	}
	if device, ok := c.cachedDeviceByID(deviceID); ok {
		return device, nil
	}
	return protocol.DeviceInfo{}, &agenttools.Error{Code: "DEVICE_NOT_FOUND", Message: "device of this session is no longer listed"}
}

func (c *Client) cachedSessionDeviceID(sessionID string) string {
	c.cacheMu.Lock()
	defer c.cacheMu.Unlock()
	if session, ok := c.cachedSessions[sessionID]; ok {
		return session.DeviceID
	}
	return ""
}

func (c *Client) cachedDeviceByID(deviceID string) (protocol.DeviceInfo, bool) {
	c.cacheMu.Lock()
	defer c.cacheMu.Unlock()
	for _, device := range c.cachedDevices {
		if device.DeviceID == deviceID {
			return device, true
		}
	}
	return protocol.DeviceInfo{}, false
}

func deviceSupports(capabilities []string, operation string) bool {
	for _, capability := range capabilities {
		if capability == operation {
			return true
		}
	}
	return false
}

func truncateForError(value string) string {
	value = strings.TrimSpace(value)
	if len(value) <= 200 {
		return value
	}
	return value[:200] + "…"
}
