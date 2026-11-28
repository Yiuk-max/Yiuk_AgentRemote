package platformclient

import (
	"context"
	"encoding/json"
	"time"

	"agentremote/internal/agenttools"
	"agentremote/internal/platform/protocol"
)

func (c *Client) handleQuestion(ctx context.Context, req agenttools.Request) (any, error) {
	var msg protocol.Message
	var input struct {
		WaitSeconds int `json:"wait_seconds"`
	}
	if err := json.Unmarshal(req.Input, &msg); err != nil {
		return nil, &agenttools.Error{Code: "INVALID_ARGUMENT", Message: "invalid question arguments"}
	}
	_ = json.Unmarshal(req.Input, &input)
	want := protocol.TypeUserReply
	if req.Name == agenttools.AskUser {
		msg.Type = protocol.TypeAskUser
		msg.RequestID = randomID()
		want = protocol.TypeUserQuestionAccepted
	} else {
		msg.Type = protocol.TypeGetUserReply
	}
	result, err := c.sendRequest(ctx, msg, func(m protocol.Message) bool { return m.Type == want })
	if err != nil {
		return nil, err
	}
	data := questionResult(result)
	if req.Name == agenttools.GetUserReply && input.WaitSeconds > 0 && result.Status == "pending" {
		return c.pollQuestionResult(ctx, msg.SessionID, msg.RequestID, time.Duration(input.WaitSeconds)*time.Second)
	}
	return data, nil
}

// fetchQuestion 查询一次询问结果。
func (c *Client) fetchQuestion(ctx context.Context, sessionID, requestID string) (map[string]any, error) {
	response, err := c.sendRequest(ctx, protocol.Message{
		Type: protocol.TypeGetUserReply, SessionID: sessionID, RequestID: requestID,
	}, func(m protocol.Message) bool { return m.Type == protocol.TypeUserReply })
	if err != nil {
		return nil, err
	}
	return questionResult(response), nil
}

// pollQuestionResult 在 MCP 内等待用户回复；超时同样不是错误。
func (c *Client) pollQuestionResult(ctx context.Context, sessionID, requestID string, wait time.Duration) (any, error) {
	result, settled, err := pollUntil(ctx, 750*time.Millisecond, wait,
		func() (map[string]any, error) { return c.fetchQuestion(ctx, sessionID, requestID) },
		func(status map[string]any) bool { value, _ := status["status"].(string); return value != "pending" })
	if err != nil {
		return nil, err
	}
	result["timed_out"] = !settled
	return result, nil
}

// questionResult 把平台消息转换成工具结果；只有 answered 才带答案。
func questionResult(result protocol.Message) map[string]any {
	data := map[string]any{
		"session_id": result.SessionID, "request_id": result.RequestID,
		"status": result.Status, "expires_at": result.ExpiresAt,
	}
	if result.Status == "answered" {
		data["answer_source"] = result.AnswerSource
		data["answer"] = result.Answer
		if result.SelectedIndex != nil {
			data["selected_index"] = *result.SelectedIndex
		}
	}
	if result.Status == "timed_out" {
		data["message"] = "No reply received. The user may not have answered, or the client may not support questions."
	}
	return data
}
