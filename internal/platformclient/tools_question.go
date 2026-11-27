package platformclient

import (
	"agentremote/internal/agenttools"
	"agentremote/internal/platform/protocol"
	"context"
	"encoding/json"
)

func (c *Client) handleQuestion(ctx context.Context, req agenttools.Request) (any, error) {
	var msg protocol.Message
	if err := json.Unmarshal(req.Input, &msg); err != nil {
		return nil, &agenttools.Error{Code: "INVALID_ARGUMENT", Message: "invalid question arguments"}
	}
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
	data := map[string]any{"session_id": result.SessionID, "request_id": result.RequestID, "status": result.Status, "expires_at": result.ExpiresAt}
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
	return data, nil
}
