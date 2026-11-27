package platformclient_test

import (
	"agentremote/internal/agenttools"
	"agentremote/internal/platform/protocol"
	"agentremote/internal/platformclient"
	"context"
	"encoding/json"
	"testing"
)

func TestQuestionToolsRoundTrip(t *testing.T) {
	address := startPlatform(t)
	device := dialFakeDevice(t, address)
	device.register("question-tools")
	backend, err := platformclient.New(platformclient.Config{Address: address, PlayerPassword: playerPassword, AllowPlaintext: true, TimeoutSeconds: 5})
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()
	core := agenttools.NewCore(backend)
	call := func(name string, input any) map[string]any {
		t.Helper()
		raw, _ := json.Marshal(input)
		response := core.Call(context.Background(), name, raw)
		if !response.OK {
			t.Fatal(response)
		}
		return response.Data.(map[string]any)
	}
	session := call(agenttools.RequestConnection, map[string]any{"device_id": "question-tools", "purpose": "questions", "temporary_password": devicePassword})
	sid := session["session_id"].(string)
	pending := call(agenttools.AskUser, map[string]any{"session_id": sid, "question": "你想要什么？", "options": []string{"A", "B"}, "timeout_seconds": 15})
	rid := pending["request_id"].(string)
	forwarded := device.recv(protocol.TypeAskUser)
	if forwarded.RequestID != rid {
		t.Fatal(forwarded)
	}
	if r := call(agenttools.GetUserReply, map[string]any{"session_id": sid, "request_id": rid}); r["status"] != "pending" {
		t.Fatal(r)
	}
	index := 0
	device.send(protocol.Message{Type: protocol.TypeUserReply, SessionID: sid, RequestID: rid, Status: "answered", AnswerSource: "option", SelectedIndex: &index})
	// Query and device response may race; fetch until the response has arrived.
	var answer map[string]any
	for i := 0; i < 20; i++ {
		answer = call(agenttools.GetUserReply, map[string]any{"session_id": sid, "request_id": rid})
		if answer["status"] == "answered" {
			break
		}
	}
	if answer["answer"] != "A" || answer["selected_index"] != 0 {
		t.Fatal(answer)
	}
	pending = call(agenttools.AskUser, map[string]any{"session_id": sid, "question": "保持等待"})
	rid = pending["request_id"].(string)
	device.recv(protocol.TypeAskUser)
	call(agenttools.CloseSession, map[string]any{"session_id": sid})
	if r := call(agenttools.GetUserReply, map[string]any{"session_id": sid, "request_id": rid}); r["status"] != "cancelled" {
		t.Fatal(r)
	}
}
