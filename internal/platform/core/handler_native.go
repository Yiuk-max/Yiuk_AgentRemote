package core

import (
	"encoding/json"
	"strings"
	"time"

	"agentremote/internal/platform/audit"
	"agentremote/internal/platform/protocol"
	"agentremote/internal/platform/session"
	"agentremote/internal/platform/task"
)

// 原生操作：与 execute_command 共用授权、任务、幂等与取消，但不接受脚本文本。
//
// 白名单是唯一入口：operation 只能是下面这些名字之一，参数是结构化 JSON。
// 客户端（Qt）对同一份名单再校验一次，平台侧校验不能省：被攻破的设备连接
// 不能靠“我已经校验过”绕过平台。
var nativeOperations = map[string]struct{}{
	"screen.capture":       {}, // 抓屏并存快照，返回截图几何
	"screen.release":       {}, // 删除设备上的临时截图（MCP 取走图片后回收）
	"input.click":          {}, // 坐标点击 + 可选变化检测
	"input.move":           {}, // 移动鼠标（含平滑移动）
	"input.drag":           {}, // 拖动
	"vision.find":          {}, // 文字 / 图像定位
	"vision.click_target":  {}, // 定位 + 点击（客户端内原子执行）
	"vision.save_template": {}, // 从最近快照裁剪模板
}

// 原生操作的超时：默认 10 秒、上限 25 秒，都要小于单次工具调用 30 秒预算。
const (
	defaultNativeTimeoutSeconds = 10
	maxNativeTimeoutSeconds     = 25
)

// supportsOperation 判断设备是否声明了某个原生能力。
//
// 能力是设备自报的，平台只做“是否声明”的判断；真正的授权仍由会话决定，
// 能力缺失只影响可用性，不能用来放宽权限。
func supportsOperation(capabilities []string, operation string) bool {
	for _, capability := range capabilities {
		if capability == operation {
			return true
		}
	}
	return false
}

// sanitizeCapabilities 校验设备自报的能力集合：去空白、去重、限制长度与数量。
//
// 不认识的字符串不报错也不保留语义——平台只把它当作“客户端说它支持”。
// 未知名字忽略掉，避免客户端升级后旧平台因为不认识的条目拒绝注册。
func sanitizeCapabilities(raw []string) ([]string, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	if len(raw) > protocol.MaxCapabilitiesPerDevice {
		return nil, newProtocolError("INVALID_ARGUMENT", "too many capabilities")
	}
	seen := make(map[string]struct{}, len(raw))
	cleaned := make([]string, 0, len(raw))
	for _, value := range raw {
		name := strings.TrimSpace(value)
		if name == "" {
			continue
		}
		if len(name) > protocol.MaxCapabilityLen {
			return nil, newProtocolError("INVALID_ARGUMENT", "capability name is too long")
		}
		if _, duplicate := seen[name]; duplicate {
			continue
		}
		if _, known := nativeOperations[name]; !known {
			continue
		}
		seen[name] = struct{}{}
		cleaned = append(cleaned, name)
	}
	return cleaned, nil
}

// handleExecuteNative 处理一条原生操作请求。
//
// 与 handleExecuteCommand 的差别只在“载荷是白名单操作 + JSON”：
// 会话授权、设备在线、任务限额、request_id 幂等、取消与终态语义全部复用。
func (s *Service) handleExecuteNative(caller *Caller, message protocol.Message) error {
	if err := s.validateID("session_id", message.SessionID); err != nil {
		return newProtocolError("INVALID_ARGUMENT", err.Error())
	}
	if err := s.validateID("request_id", message.RequestID); err != nil {
		return newProtocolError("INVALID_ARGUMENT", err.Error())
	}
	operation := strings.TrimSpace(message.Operation)
	if operation == "" || len(operation) > protocol.MaxNativeOperationLen {
		return newProtocolError("INVALID_ARGUMENT", "operation is required and bounded")
	}
	if _, known := nativeOperations[operation]; !known {
		return newProtocolError("UNSUPPORTED_OPERATION", "operation is not in the native whitelist")
	}
	arguments := message.Arguments
	if len(arguments) == 0 {
		arguments = json.RawMessage(`{}`)
	}
	if len(arguments) > s.config.NativeArgumentMaxBytes {
		return newProtocolError("INVALID_ARGUMENT", "arguments exceed native_argument_max_bytes")
	}
	if !json.Valid(arguments) {
		return newProtocolError("INVALID_ARGUMENT", "arguments must be valid JSON")
	}
	timeoutSeconds := message.TimeoutSeconds
	if timeoutSeconds <= 0 {
		timeoutSeconds = defaultNativeTimeoutSeconds
	}
	if timeoutSeconds < 1 || timeoutSeconds > maxNativeTimeoutSeconds {
		return newProtocolError("INVALID_ARGUMENT", "timeout_seconds must be between 1 and 25")
	}

	// 授权与命令完全一致：已批准/活跃会话、同一操作者连接、会话仍有余期。
	sessionRecord, err := s.sessions.Authorize(message.SessionID, caller.ConnectionID, session.PermissionExecuteCommand)
	if err != nil {
		return s.sessionProtocolError(err)
	}
	deviceConnectionID, ok := s.devices.OnlineConnection(sessionRecord.DeviceID)
	if !ok || deviceConnectionID != sessionRecord.DeviceConnectionID {
		return newProtocolError("DEVICE_OFFLINE", "device is no longer online")
	}
	deviceInfo, ok := s.devices.Get(sessionRecord.DeviceID)
	if !ok {
		return newProtocolError("DEVICE_OFFLINE", "device is no longer online")
	}
	if !supportsOperation(deviceInfo.Capabilities, operation) {
		// 明确区分“客户端太旧”与“操作不存在”，让 Agent 知道该升级还是该改参数。
		return newProtocolError("UNSUPPORTED_CLIENT",
			"device does not advertise capability "+operation+"; ask the owner to upgrade the client")
	}

	return s.submitTask(caller, taskSubmission{
		message: message,
		session: sessionRecord,
		frame: func(requestID string) protocol.Message {
			return protocol.Message{
				Type:           protocol.TypeExecuteNative,
				SessionID:      sessionRecord.ID,
				RequestID:      requestID,
				Operation:      operation,
				Arguments:      arguments,
				TimeoutSeconds: timeoutSeconds,
			}
		},
		timeout:     time.Duration(timeoutSeconds) * time.Second,
		auditEvent:  "native_submitted",
		auditDetail: []any{"operation", operation},
		deliverErr:  "could not deliver operation to device",
	})
}

// taskSubmission 描述一次“提交给设备执行的任务”。
//
// 命令与原生操作共用同一套：任务限额与幂等 → 会话激活 → 投递 → 回执。
// 差异只在帧内容、超时上限、审计事件与投递失败文案。
type taskSubmission struct {
	message     protocol.Message // 原始请求帧：用于回执的 correlation_id 与 request_id
	session     *session.Session
	frame       func(requestID string) protocol.Message
	timeout     time.Duration
	auditEvent  string
	auditDetail []any
	deliverErr  string
}

// submitTask 是命令与原生操作唯一的提交入口。
//
// 幂等语义与原来的 handleExecuteCommand 完全一致：
// 同一会话内重复的 request_id 返回原任务状态而不重复执行；
// 投递失败一律把任务收敛成 result_unknown（可能已生效），不当作失败。
func (s *Service) submitTask(caller *Caller, submission taskSubmission) error {
	taskRecord, created, err := s.tasks.Start(submission.session.ID, submission.message.RequestID, submission.timeout)
	if err != nil {
		return s.taskProtocolError(err)
	}
	if !created {
		return s.sendResponse(caller, submission.message, protocol.Message{
			Type:      protocol.TypeCommandAccepted,
			SessionID: submission.session.ID,
			RequestID: taskRecord.RequestID,
			Status:    string(taskRecord.Status),
		})
	}
	if _, err := s.sessions.Activate(submission.session.ID, caller.ConnectionID); err != nil {
		s.tasks.Finish(submission.session.ID, submission.message.RequestID, task.StatusResultUnknown, nil, false)
		return s.sessionProtocolError(err)
	}
	if !s.send(submission.session.DeviceConnectionID, submission.frame(taskRecord.RequestID)) {
		s.tasks.Finish(submission.session.ID, submission.message.RequestID, task.StatusResultUnknown, nil, false)
		return newProtocolError("DEVICE_UNAVAILABLE", submission.deliverErr)
	}
	detail := append([]any{"session", audit.RedactID(submission.session.ID),
		"request", audit.RedactID(taskRecord.RequestID),
		"device", audit.RedactID(submission.session.DeviceID)}, submission.auditDetail...)
	s.audit.Event(submission.auditEvent, detail...)
	return s.sendResponse(caller, submission.message, protocol.Message{
		Type:      protocol.TypeCommandAccepted,
		SessionID: submission.session.ID,
		RequestID: taskRecord.RequestID,
		Status:    string(taskRecord.Status),
	})
}
