package platformclient

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"agentremote/internal/agenttools"
	"agentremote/internal/platform/protocol"
)

// sessionRequestOptions 聚合请求远程会话所需的参数，供独立请求和一次性命令流程共用。
// 不再携带 permissions：会话批准后即拥有全部三项能力。
type sessionRequestOptions struct {
	DeviceID          string
	Purpose           string
	TemporaryPassword string
	WaitForApproval   time.Duration
}

// handleRequestConnection 解析工具参数，并按指定等待时长请求远程会话。
func (c *Client) handleRequestConnection(ctx context.Context, arguments json.RawMessage) (any, error) {
	var input struct {
		DeviceID               string `json:"device_id"`
		DeviceName             string `json:"device_name"`
		Purpose                string `json:"purpose"`
		TemporaryPassword      string `json:"temporary_password"`
		WaitForApprovalSeconds int    `json:"wait_for_approval_seconds"`
	}
	if err := json.Unmarshal(arguments, &input); err != nil {
		return nil, &agenttools.Error{Code: "INVALID_ARGUMENT", Message: "invalid arguments"}
	}
	device, err := c.resolveDevice(ctx, input.DeviceID, input.DeviceName)
	if err != nil {
		return nil, err
	}
	return c.requestSession(ctx, sessionRequestOptions{
		DeviceID:          device.DeviceID,
		Purpose:           input.Purpose,
		TemporaryPassword: input.TemporaryPassword,
		WaitForApproval:   time.Duration(input.WaitForApprovalSeconds) * time.Second,
	})
}

// requestSession 向平台请求创建会话，并在需要时等待设备所有者审批。
// 直连模式下的设备会立即批准会话。
func (c *Client) requestSession(ctx context.Context, input sessionRequestOptions) (map[string]any, error) {
	// 不提供临时密码时平台会创建审批请求（status=pending_approval），由设备所有者批准；
	// 提供当前临时密码则直接进入 direct 会话。两种流程都由服务端决定，这里不再本地拦截。
	response, err := c.sendRequest(ctx, protocol.Message{
		Type: protocol.TypeRequestSession, DeviceID: input.DeviceID, Purpose: input.Purpose,
		TemporaryPassword: input.TemporaryPassword,
	}, func(message protocol.Message) bool {
		return message.Type == protocol.TypeSessionCreated
	})
	if err != nil {
		return nil, err
	}
	result := sessionToolResult(response)
	sessionID := sessionIDFromResult(result)
	status, _ := result["status"].(string)
	if status == protocol.StatusPendingApproval || status == protocol.StatusRequested {
		// 默认立即返回；不要在 request_connection 里等审批（宿主超时会先到）。
		result["next"] = "get_session_status"
	}
	if (status == protocol.StatusPendingApproval || status == protocol.StatusRequested) && input.WaitForApproval > 0 {
		if c.waitForSessionDecision(ctx, sessionID, input.WaitForApproval) {
			result["wait_timed_out"] = true
		} else if latest, err := c.fetchSessionStatus(ctx, sessionID); err == nil {
			for key, value := range latest {
				result[key] = value
			}
		}
	}
	return result, nil
}

// waitForSessionDecision 等审批落定；返回 true 表示等待超时或取消。
// 查询失败按“仍在等待”处理，避免一次网络抖动就把审批判成超时。
func (c *Client) waitForSessionDecision(ctx context.Context, sessionID string, timeout time.Duration) bool {
	_, settled, _ := pollUntil(ctx, 1500*time.Millisecond, timeout,
		func() (map[string]any, error) {
			result, err := c.fetchSessionStatus(ctx, sessionID)
			if err != nil {
				return map[string]any{"status": protocol.StatusPendingApproval}, nil
			}
			return result, nil
		},
		func(status map[string]any) bool {
			value, _ := status["status"].(string)
			return !isPendingApproval(value)
		})
	return !settled
}

// handleGetSessionStatus 解析工具参数并查询远程会话状态。
// wait_seconds > 0 时在 MCP 内轮询，避免 agent 自己空转调用。
func (c *Client) handleGetSessionStatus(ctx context.Context, arguments json.RawMessage) (any, error) {
	var input struct {
		SessionID   string `json:"session_id"`
		WaitSeconds int    `json:"wait_seconds"`
	}
	if err := json.Unmarshal(arguments, &input); err != nil {
		return nil, &agenttools.Error{Code: "INVALID_ARGUMENT", Message: "invalid arguments"}
	}
	if input.WaitSeconds <= 0 {
		return c.fetchSessionStatus(ctx, input.SessionID)
	}
	return c.fetchSessionStatusWaiting(ctx, input.SessionID, time.Duration(input.WaitSeconds)*time.Second)
}

// pollUntil 按固定间隔拉取状态，直到 done 命中、等待超时或调用被取消。
// 超时/取消不是错误：返回最后一次结果，由调用方决定怎么标注。
func pollUntil(ctx context.Context, interval, wait time.Duration, fetch func() (map[string]any, error), done func(map[string]any) bool) (map[string]any, bool, error) {
	deadline := time.Now().Add(wait)
	for {
		last, err := fetch()
		if err != nil {
			return nil, false, err
		}
		if done(last) {
			return last, true, nil
		}
		delay := interval
		if remaining := time.Until(deadline); remaining < delay {
			delay = remaining
		}
		if delay <= 0 {
			return last, false, nil
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return last, false, nil
		case <-timer.C:
		}
	}
}

func isPendingApproval(status string) bool {
	return status == protocol.StatusPendingApproval || status == protocol.StatusRequested
}

// fetchSessionStatusWaiting 轮询到会话离开审批态、调用被取消或等待超时。
func (c *Client) fetchSessionStatusWaiting(ctx context.Context, sessionID string, wait time.Duration) (map[string]any, error) {
	result, settled, err := pollUntil(ctx, 1500*time.Millisecond, wait,
		func() (map[string]any, error) { return c.fetchSessionStatus(ctx, sessionID) },
		func(status map[string]any) bool {
			value, _ := status["status"].(string)
			return !isPendingApproval(value)
		})
	if err != nil {
		return nil, err
	}
	result["timed_out"] = !settled
	return result, nil
}

// fetchSessionStatus 查询指定会话，并转换为工具结果结构。
func (c *Client) fetchSessionStatus(ctx context.Context, sessionID string) (map[string]any, error) {
	response, err := c.sendRequest(ctx, protocol.Message{Type: protocol.TypeSessionStatus, SessionID: sessionID}, func(message protocol.Message) bool {
		return message.Type == protocol.TypeSessionStatus
	})
	if err != nil {
		return nil, err
	}
	return sessionToolResult(response), nil
}

// handleCloseSession 解析工具参数并请求关闭会话。
func (c *Client) handleCloseSession(ctx context.Context, arguments json.RawMessage) (any, error) {
	var input struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(arguments, &input); err != nil {
		return nil, &agenttools.Error{Code: "INVALID_ARGUMENT", Message: "invalid arguments"}
	}
	return c.requestSessionClose(ctx, input.SessionID)
}

// requestSessionClose 请求平台关闭会话；对已不存在的会话按幂等成功处理。
func (c *Client) requestSessionClose(ctx context.Context, sessionID string) (map[string]any, error) {
	response, err := c.sendRequest(ctx, protocol.Message{Type: protocol.TypeCloseSession, SessionID: sessionID}, func(message protocol.Message) bool {
		return message.Type == protocol.TypeSessionStatus && isTerminalSessionStatus(message.Status)
	})
	if err != nil {
		// 关闭操作是幂等的
		// 平台重启（或替换操作端连接）时会清除会话，因此会话已不存在时也无需再次关闭
		var toolErr *agenttools.Error
		if errors.As(err, &toolErr) && toolErr.Code == "SESSION_NOT_FOUND" {
			return map[string]any{
				"session_id":   sessionID,
				"status":       protocol.StatusClosed,
				"close_reason": "session_not_found",
				"idempotent":   true,
			}, nil
		}
		return nil, err
	}
	return sessionToolResult(response), nil
}
