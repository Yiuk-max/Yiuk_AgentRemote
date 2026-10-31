package platformclient

import (
	"context"
	"time"

	"agentremote/internal/agenttools"
	"agentremote/internal/platform/protocol"
	"encoding/json"
)

func (c *Client) executeCommand(ctx context.Context, raw json.RawMessage) (any, error) {
	var input struct {
		SessionID        string `json:"session_id"`
		Command          string `json:"command"`
		WorkingDirectory string `json:"working_directory"`
		TimeoutSeconds   int    `json:"timeout_seconds"`
	}
	if err := json.Unmarshal(raw, &input); err != nil {
		return nil, &agenttools.Error{Code: "INVALID_ARGUMENT", Message: "invalid arguments"}
	}
	requestID, status, err := c.executeCommandInternal(ctx, input.SessionID, input.Command, input.WorkingDirectory, input.TimeoutSeconds)
	if err != nil {
		return nil, err
	}
	return map[string]any{"session_id": input.SessionID, "request_id": requestID, "status": status}, nil
}

func (c *Client) executeCommandInternal(ctx context.Context, sessionID, command, workingDirectory string, timeoutSeconds int) (string, string, error) {
	requestID := randomID()
	resp, err := c.call(ctx, protocol.Message{
		Type: protocol.TypeExecuteCommand, SessionID: sessionID, RequestID: requestID,
		Command: command, WorkingDirectory: workingDirectory, TimeoutSeconds: timeoutSeconds,
	}, func(m protocol.Message) bool {
		return m.Type == protocol.TypeCommandAccepted
	})
	if err != nil {
		return "", "", err
	}
	return requestID, resp.Status, nil
}

func (c *Client) getCommandResult(ctx context.Context, raw json.RawMessage) (any, error) {
	var input struct {
		SessionID string `json:"session_id"`
		RequestID string `json:"request_id"`
		Cursor    int    `json:"cursor"`
	}
	if err := json.Unmarshal(raw, &input); err != nil {
		return nil, &agenttools.Error{Code: "INVALID_ARGUMENT", Message: "invalid arguments"}
	}
	return c.getCommandResultInternal(ctx, input.SessionID, input.RequestID, input.Cursor)
}

func (c *Client) getCommandResultInternal(ctx context.Context, sessionID, requestID string, cursor int) (map[string]any, error) {
	if result, ok := c.cachedResult(sessionID, requestID, cursor); ok {
		return result, nil
	}
	resp, err := c.call(ctx, protocol.Message{
		Type: protocol.TypeGetCommandResult, SessionID: sessionID, RequestID: requestID, Cursor: cursor,
	}, func(m protocol.Message) bool {
		return m.Type == protocol.TypeCommandResult
	})
	if err != nil {
		return nil, err
	}
	return resultMap(resp), nil
}

// runCommand 执行一次性操作端流程：请求会话（可选择等待设备所有者批准）、执行命令、等待结果，并在调用方未要求保持会话时关闭会话
func (c *Client) runCommand(ctx context.Context, raw json.RawMessage) (any, error) {
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
	if err := json.Unmarshal(raw, &input); err != nil {
		return nil, &agenttools.Error{Code: "INVALID_ARGUMENT", Message: "invalid arguments"}
	}
	if input.Purpose == "" {
		input.Purpose = "run a remote command"
	}
	if input.TimeoutSeconds <= 0 {
		input.TimeoutSeconds = 60
	}
	budget := time.Duration(input.WaitForApprovalSeconds+input.TimeoutSeconds)*time.Second + 30*time.Second
	runCtx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()

	session, err := c.createSession(runCtx, connectionRequest{
		DeviceID:          input.DeviceID,
		Purpose:           input.Purpose,
		Permissions:       []string{"execute_command"},
		TemporaryPassword: input.TemporaryPassword,
		WaitForApproval:   time.Duration(input.WaitForApprovalSeconds) * time.Second,
	})
	if err != nil {
		return nil, err
	}
	id := sessionIDFromResult(session)
	authorization, _ := session["status"].(string)
	if authorization != protocol.StatusApproved && authorization != protocol.StatusActive {
		return map[string]any{
			"session_id": id, "device_id": input.DeviceID, "authorization": authorization,
			"executed": false, "message": "session was not approved; the command was not executed",
		}, nil
	}

	requestID, commandStatus, err := c.executeCommandInternal(runCtx, id, input.Command, input.WorkingDirectory, input.TimeoutSeconds)
	if err != nil {
		return nil, err
	}
	result, err := c.waitCommandResult(runCtx, id, requestID, time.Duration(input.TimeoutSeconds)*time.Second+15*time.Second)
	if err != nil {
		return nil, err
	}
	if !input.KeepSessionOpen {
		if _, closeErr := c.closeSessionInternal(runCtx, id); closeErr == nil {
			result["session_closed"] = true
		}
	}
	result["session_id"] = id
	result["device_id"] = input.DeviceID
	result["authorization"] = authorization
	result["executed"] = true
	if commandStatus != "" {
		result["initial_status"] = commandStatus
	}
	return result, nil
}

func (c *Client) waitCommandResult(ctx context.Context, sessionID, requestID string, timeout time.Duration) (map[string]any, error) {
	deadline := time.Now().Add(timeout)
	for {
		result, err := c.getCommandResultInternal(ctx, sessionID, requestID, 0)
		if err == nil {
			if status, _ := result["status"].(string); status != protocol.TaskRunning && status != protocol.TaskQueued && status != "" {
				return aggregateResult(result), nil
			}
			if time.Now().After(deadline) {
				result["status"] = protocol.TaskResultUnknown
				result["result_known"] = false
				result["message"] = "timed out waiting for command result"
				return aggregateResult(result), nil
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

func (c *Client) cancelCommand(ctx context.Context, raw json.RawMessage) (any, error) {
	var input struct {
		SessionID string `json:"session_id"`
		RequestID string `json:"request_id"`
	}
	if err := json.Unmarshal(raw, &input); err != nil {
		return nil, &agenttools.Error{Code: "INVALID_ARGUMENT", Message: "invalid arguments"}
	}
	resp, err := c.call(ctx, protocol.Message{
		Type: protocol.TypeCancelCommand, SessionID: input.SessionID, RequestID: input.RequestID,
	}, func(m protocol.Message) bool {
		return m.Type == protocol.TypeCommandCanceled
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"session_id": input.SessionID, "request_id": input.RequestID, "status": resp.Status}, nil
}
