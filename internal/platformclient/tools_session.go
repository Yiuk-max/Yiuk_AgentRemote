package platformclient

import (
	"context"
	"errors"
	"time"

	"agentremote/internal/agenttools"
	"agentremote/internal/platform/protocol"
	"encoding/json"
)

// sessionRequestOptions 聚合请求远程会话所需的参数，供独立请求和一次性命令流程共用。
type sessionRequestOptions struct {
	DeviceID          string
	Purpose           string
	Permissions       []string
	TemporaryPassword string
	WaitForApproval   time.Duration
}

// handleRequestConnection 解析工具参数，并按指定等待时长请求远程会话。
func (c *Client) handleRequestConnection(ctx context.Context, arguments json.RawMessage) (any, error) {
	var input struct {
		DeviceID               string   `json:"device_id"`
		Purpose                string   `json:"purpose"`
		Permissions            []string `json:"permissions"`
		TemporaryPassword      string   `json:"temporary_password"`
		WaitForApprovalSeconds int      `json:"wait_for_approval_seconds"`
	}
	if err := json.Unmarshal(arguments, &input); err != nil {
		return nil, &agenttools.Error{Code: "INVALID_ARGUMENT", Message: "invalid arguments"}
	}
	return c.requestSession(ctx, sessionRequestOptions{
		DeviceID:          input.DeviceID,
		Purpose:           input.Purpose,
		Permissions:       input.Permissions,
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
		Permissions: input.Permissions, TemporaryPassword: input.TemporaryPassword,
	}, func(message protocol.Message) bool {
		return message.Type == protocol.TypeSessionCreated
	})
	if err != nil {
		return nil, err
	}
	result := sessionToolResult(response)
	sessionID := sessionIDFromResult(result)
	status, _ := result["status"].(string)
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

// waitForSessionDecision 轮询会话状态，直到审批结束、上下文取消或等待时限到达。
// 返回 true 表示等待超时或取消，false 仅表示会话不再等待审批，未必已获批准。
func (c *Client) waitForSessionDecision(ctx context.Context, sessionID string, timeout time.Duration) (timedOutOrCancelled bool) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if result, err := c.fetchSessionStatus(ctx, sessionID); err == nil {
			if status, _ := result["status"].(string); status != protocol.StatusPendingApproval && status != protocol.StatusRequested {
				return false
			}
		}
		pollDelay := 1500 * time.Millisecond
		if remaining := time.Until(deadline); remaining < pollDelay {
			pollDelay = remaining
		}
		if pollDelay <= 0 {
			break
		}
		timer := time.NewTimer(pollDelay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return true
		case <-timer.C:
		}
	}
	return true
}

// handleGetSessionStatus 解析工具参数并查询远程会话状态。
func (c *Client) handleGetSessionStatus(ctx context.Context, arguments json.RawMessage) (any, error) {
	var input struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(arguments, &input); err != nil {
		return nil, &agenttools.Error{Code: "INVALID_ARGUMENT", Message: "invalid arguments"}
	}
	return c.fetchSessionStatus(ctx, input.SessionID)
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
