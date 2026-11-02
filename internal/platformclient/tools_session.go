package platformclient

import (
	"context"
	"errors"
	"time"

	"agentremote/internal/agenttools"
	"agentremote/internal/platform/protocol"
	"encoding/json"
)

// connectionRequest 聚合创建远程会话所需的参数，供独立请求和一次性命令流程共用。
type connectionRequest struct {
	DeviceID          string
	Purpose           string
	Permissions       []string
	TemporaryPassword string
	WaitForApproval   time.Duration
}

// requestConnection 解析工具参数，并按指定等待时长请求远程会话。
func (c *Client) requestConnection(ctx context.Context, raw json.RawMessage) (any, error) {
	var input struct {
		DeviceID               string   `json:"device_id"`
		Purpose                string   `json:"purpose"`
		Permissions            []string `json:"permissions"`
		TemporaryPassword      string   `json:"temporary_password"`
		WaitForApprovalSeconds int      `json:"wait_for_approval_seconds"`
	}
	if err := json.Unmarshal(raw, &input); err != nil {
		return nil, &agenttools.Error{Code: "INVALID_ARGUMENT", Message: "invalid arguments"}
	}
	return c.createSession(ctx, connectionRequest{
		DeviceID:          input.DeviceID,
		Purpose:           input.Purpose,
		Permissions:       input.Permissions,
		TemporaryPassword: input.TemporaryPassword,
		WaitForApproval:   time.Duration(input.WaitForApprovalSeconds) * time.Second,
	})
}

// createSession 请求会话，并在需要时等待设备所有者批准
// 直连模式下的设备会立即批准会话
func (c *Client) createSession(ctx context.Context, input connectionRequest) (map[string]any, error) {
	if input.TemporaryPassword == "" {
		return nil, &agenttools.Error{Code: "INVALID_ARGUMENT", Message: "temporary_password is required; ask the device owner for the 32-character temporary password"}
	}
	resp, err := c.call(ctx, protocol.Message{
		Type: protocol.TypeRequestSession, DeviceID: input.DeviceID, Purpose: input.Purpose,
		Permissions: input.Permissions, TemporaryPassword: input.TemporaryPassword,
	}, func(m protocol.Message) bool {
		return m.Type == protocol.TypeSessionCreated
	})
	if err != nil {
		return nil, err
	}
	result := sessionResult(resp)
	id := sessionIDFromResult(result)
	status, _ := result["status"].(string)
	if (status == protocol.StatusPendingApproval || status == protocol.StatusRequested) && input.WaitForApproval > 0 {
		if c.waitForApproval(ctx, id, input.WaitForApproval) {
			result["wait_timed_out"] = true
		} else if latest, err := c.getSessionStatusInternal(ctx, id); err == nil {
			for key, value := range latest {
				result[key] = value
			}
		}
	}
	return result, nil
}

// waitForApproval 轮询会话状态直到审批完成、上下文取消或等待时限到达。
func (c *Client) waitForApproval(ctx context.Context, sessionID string, timeout time.Duration) (timedOut bool) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if result, err := c.getSessionStatusInternal(ctx, sessionID); err == nil {
			if status, _ := result["status"].(string); status != protocol.StatusPendingApproval && status != protocol.StatusRequested {
				return false
			}
		}
		sleep := 1500 * time.Millisecond
		if remaining := time.Until(deadline); remaining < sleep {
			sleep = remaining
		}
		if sleep <= 0 {
			break
		}
		timer := time.NewTimer(sleep)
		select {
		case <-ctx.Done():
			timer.Stop()
			return true
		case <-timer.C:
		}
	}
	return true
}

// getSessionStatus 解析参数并查询远程会话状态。
func (c *Client) getSessionStatus(ctx context.Context, raw json.RawMessage) (any, error) {
	var input struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(raw, &input); err != nil {
		return nil, &agenttools.Error{Code: "INVALID_ARGUMENT", Message: "invalid arguments"}
	}
	return c.getSessionStatusInternal(ctx, input.SessionID)
}

// getSessionStatusInternal 查询指定会话，并转换为工具结果结构。
func (c *Client) getSessionStatusInternal(ctx context.Context, sessionID string) (map[string]any, error) {
	resp, err := c.call(ctx, protocol.Message{Type: protocol.TypeSessionStatus, SessionID: sessionID}, func(m protocol.Message) bool {
		return m.Type == protocol.TypeSessionStatus
	})
	if err != nil {
		return nil, err
	}
	return sessionResult(resp), nil
}

// closeSession 解析参数并执行关闭会话操作。
func (c *Client) closeSession(ctx context.Context, raw json.RawMessage) (any, error) {
	var input struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(raw, &input); err != nil {
		return nil, &agenttools.Error{Code: "INVALID_ARGUMENT", Message: "invalid arguments"}
	}
	return c.closeSessionInternal(ctx, input.SessionID)
}

// closeSessionInternal 关闭会话；对已不存在的会话按幂等成功处理。
func (c *Client) closeSessionInternal(ctx context.Context, sessionID string) (map[string]any, error) {
	resp, err := c.call(ctx, protocol.Message{Type: protocol.TypeCloseSession, SessionID: sessionID}, func(m protocol.Message) bool {
		return m.Type == protocol.TypeSessionStatus && isTerminalSession(m.Status)
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
	return sessionResult(resp), nil
}
