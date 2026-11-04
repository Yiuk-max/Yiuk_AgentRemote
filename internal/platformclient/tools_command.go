package platformclient

import (
	"context"
	"time"

	"agentremote/internal/agenttools"
	"agentremote/internal/platform/protocol"
	"encoding/json"
)

// handleExecuteCommand 解析工具参数并提交已授权会话中的 PowerShell 命令。
func (c *Client) handleExecuteCommand(ctx context.Context, arguments json.RawMessage) (any, error) {
	var input struct {
		SessionID        string `json:"session_id"`
		Command          string `json:"command"`
		WorkingDirectory string `json:"working_directory"`
		TimeoutSeconds   int    `json:"timeout_seconds"`
	}
	if err := json.Unmarshal(arguments, &input); err != nil {
		return nil, &agenttools.Error{Code: "INVALID_ARGUMENT", Message: "invalid arguments"}
	}
	requestID, status, err := c.submitCommand(ctx, input.SessionID, input.Command, input.WorkingDirectory, input.TimeoutSeconds)
	if err != nil {
		return nil, err
	}
	return map[string]any{"session_id": input.SessionID, "request_id": requestID, "status": status}, nil
}

// submitCommand 分配请求标识并提交命令，等待平台确认接收后返回，不等待执行结束。
func (c *Client) submitCommand(ctx context.Context, sessionID, command, workingDirectory string, timeoutSeconds int) (string, string, error) {
	requestID := randomID()
	response, err := c.sendRequest(ctx, protocol.Message{
		Type: protocol.TypeExecuteCommand, SessionID: sessionID, RequestID: requestID,
		Command: command, WorkingDirectory: workingDirectory, TimeoutSeconds: timeoutSeconds,
	}, func(message protocol.Message) bool {
		return message.Type == protocol.TypeCommandAccepted
	})
	if err != nil {
		return "", "", err
	}
	return requestID, response.Status, nil
}

// handleGetCommandResult 解析工具分页参数，并查询指定命令的输出与状态。
func (c *Client) handleGetCommandResult(ctx context.Context, arguments json.RawMessage) (any, error) {
	var input struct {
		SessionID string `json:"session_id"`
		RequestID string `json:"request_id"`
		Cursor    int    `json:"cursor"`
	}
	if err := json.Unmarshal(arguments, &input); err != nil {
		return nil, &agenttools.Error{Code: "INVALID_ARGUMENT", Message: "invalid arguments"}
	}
	return c.fetchCommandResult(ctx, input.SessionID, input.RequestID, input.Cursor)
}

// fetchCommandResult 优先返回终态缓存，否则向平台请求最新结果。
func (c *Client) fetchCommandResult(ctx context.Context, sessionID, requestID string, cursor int) (map[string]any, error) {
	if result, ok := c.cachedFinalCommandResult(sessionID, requestID, cursor); ok {
		return result, nil
	}
	response, err := c.sendRequest(ctx, protocol.Message{
		Type: protocol.TypeGetCommandResult, SessionID: sessionID, RequestID: requestID, Cursor: cursor,
	}, func(message protocol.Message) bool {
		return message.Type == protocol.TypeCommandResult
	})
	if err != nil {
		return nil, err
	}
	return commandToolResult(response), nil
}

// handleRunCommand 执行一次性工具流程：请求会话、提交命令、等待结果，并按参数关闭会话。
func (c *Client) handleRunCommand(ctx context.Context, arguments json.RawMessage) (any, error) {
	var input struct {
		DeviceID               string `json:"device_id"`
		TemporaryPassword      string `json:"temporary_password"`
		Command                string `json:"command"`
		Purpose                string `json:"purpose"`
		WorkingDirectory       string `json:"working_directory"`
		TimeoutSeconds         int    `json:"timeout_seconds"`
		WaitForApprovalSeconds int    `json:"wait_for_approval_seconds"`
		KeepSessionOpen        bool   `json:"keep_session_open"`
	}
	if err := json.Unmarshal(arguments, &input); err != nil {
		return nil, &agenttools.Error{Code: "INVALID_ARGUMENT", Message: "invalid arguments"}
	}
	if input.Purpose == "" {
		input.Purpose = "run a remote command"
	}
	if input.TimeoutSeconds <= 0 {
		input.TimeoutSeconds = 60
	}
	operationTimeout := time.Duration(input.WaitForApprovalSeconds+input.TimeoutSeconds)*time.Second + 30*time.Second
	runCtx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()

	session, err := c.requestSession(runCtx, sessionRequestOptions{
		DeviceID:          input.DeviceID,
		Purpose:           input.Purpose,
		Permissions:       []string{"execute_command"},
		TemporaryPassword: input.TemporaryPassword,
		WaitForApproval:   time.Duration(input.WaitForApprovalSeconds) * time.Second,
	})
	if err != nil {
		return nil, err
	}
	sessionID := sessionIDFromResult(session)
	authorizationStatus, _ := session["status"].(string)
	if authorizationStatus != protocol.StatusApproved && authorizationStatus != protocol.StatusActive {
		return map[string]any{
			"session_id": sessionID, "device_id": input.DeviceID, "authorization": authorizationStatus,
			"executed": false, "message": "session was not approved; the command was not executed",
		}, nil
	}

	requestID, initialCommandStatus, err := c.submitCommand(runCtx, sessionID, input.Command, input.WorkingDirectory, input.TimeoutSeconds)
	if err != nil {
		return nil, err
	}
	result, err := c.waitForCommandResult(runCtx, sessionID, requestID, time.Duration(input.TimeoutSeconds)*time.Second+15*time.Second)
	if err != nil {
		return nil, err
	}
	if !input.KeepSessionOpen {
		if _, closeErr := c.requestSessionClose(runCtx, sessionID); closeErr == nil {
			result["session_closed"] = true
		}
	}
	result["session_id"] = sessionID
	result["device_id"] = input.DeviceID
	result["authorization"] = authorizationStatus
	result["executed"] = true
	if initialCommandStatus != "" {
		result["initial_status"] = initialCommandStatus
	}
	return result, nil
}

// waitForCommandResult 定期查询命令结果，直到任务结束或达到等待时限。
func (c *Client) waitForCommandResult(ctx context.Context, sessionID, requestID string, timeout time.Duration) (map[string]any, error) {
	deadline := time.Now().Add(timeout)
	for {
		result, err := c.fetchCommandResult(ctx, sessionID, requestID, 0)
		if err == nil {
			if status, _ := result["status"].(string); status != protocol.TaskRunning && status != protocol.TaskQueued && status != "" {
				return aggregateCommandOutput(result), nil
			}
			if time.Now().After(deadline) {
				result["status"] = protocol.TaskResultUnknown
				result["result_known"] = false
				result["message"] = "timed out waiting for command result"
				return aggregateCommandOutput(result), nil
			}
		} else if time.Now().After(deadline) {
			return nil, err
		}
		timer := time.NewTimer(750 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, &agenttools.Error{Code: "PLATFORM_TIMEOUT", Message: "command wait was canceled"}
		case <-timer.C:
		}
	}
}

// handleCancelCommand 解析工具参数并请求平台取消指定会话中的命令。
func (c *Client) handleCancelCommand(ctx context.Context, arguments json.RawMessage) (any, error) {
	var input struct {
		SessionID string `json:"session_id"`
		RequestID string `json:"request_id"`
	}
	if err := json.Unmarshal(arguments, &input); err != nil {
		return nil, &agenttools.Error{Code: "INVALID_ARGUMENT", Message: "invalid arguments"}
	}
	response, err := c.sendRequest(ctx, protocol.Message{
		Type: protocol.TypeCancelCommand, SessionID: input.SessionID, RequestID: input.RequestID,
	}, func(message protocol.Message) bool {
		return message.Type == protocol.TypeCommandCanceled
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"session_id": input.SessionID, "request_id": input.RequestID, "status": response.Status}, nil
}
