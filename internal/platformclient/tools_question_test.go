package platformclient_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"agentremote/internal/agenttools"
	"agentremote/internal/platform/protocol"
	"agentremote/internal/platformclient"
)

func TestQuestionToolsRoundTrip(t *testing.T) {
	address := startPlatform(t)
	device := dialFakeDevice(t, address)
	device.register("question-tools")
	backend, err := platformclient.New(platformclient.Config{Address: address, OperatorPassword: playerPassword, AllowPlaintext: true, TimeoutSeconds: 5})
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

func TestWaitSecondsLongPolling(t *testing.T) {
	address := startPlatform(t)
	device := dialFakeDevice(t, address)
	device.register("wait-device")
	backend, err := platformclient.New(platformclient.Config{Address: address, OperatorPassword: playerPassword, AllowPlaintext: true, TimeoutSeconds: 5})
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
			t.Fatalf("%s: %+v", name, response)
		}
		return response.Data.(map[string]any)
	}

	// 审批中的会话：等待 1 秒后应返回 pending + timed_out，而不是报错。
	session := call(agenttools.RequestConnection, map[string]any{"device_id": "wait-device", "purpose": "wait test"})
	sid, _ := session["session_id"].(string)
	if session["next"] != "get_session_status" {
		t.Fatalf("pending request should point at get_session_status: %v", session)
	}
	started := time.Now()
	pending := call(agenttools.GetSessionStatus, map[string]any{"session_id": sid, "wait_seconds": 1})
	if pending["status"] != protocol.StatusPendingApproval || pending["timed_out"] != true {
		t.Fatalf("unexpected pending result: %v", pending)
	}
	if elapsed := time.Since(started); elapsed < 900*time.Millisecond {
		t.Fatalf("long poll returned too early: %v", elapsed)
	}

	// 设备批准后，同一个调用应立刻返回已批准并标记 timed_out=false。
	device.recv(protocol.TypeSessionRequest)
	device.send(protocol.Message{Type: protocol.TypeApproveSession, SessionID: sid, TemporaryPassword: devicePassword})
	approved := call(agenttools.GetSessionStatus, map[string]any{"session_id": sid, "wait_seconds": 5})
	if approved["status"] == protocol.StatusPendingApproval {
		t.Fatalf("approval was not picked up: %v", approved)
	}
	if approved["timed_out"] != false {
		t.Fatalf("approved session must not be marked timed out: %v", approved)
	}

	// ask_user 的回复等待：无人回答时返回 pending + timed_out。
	question := call(agenttools.AskUser, map[string]any{"session_id": sid, "question": "继续吗？", "timeout_seconds": 15})
	rid := question["request_id"].(string)
	device.recv(protocol.TypeAskUser)
	started = time.Now()
	reply := call(agenttools.GetUserReply, map[string]any{"session_id": sid, "request_id": rid, "wait_seconds": 1})
	if reply["status"] != "pending" || reply["timed_out"] != true {
		t.Fatalf("unexpected reply wait result: %v", reply)
	}
	if elapsed := time.Since(started); elapsed < 900*time.Millisecond {
		t.Fatalf("question long poll returned too early: %v", elapsed)
	}
	// 用户真的回答时，同一个 get_user_reply 应立刻拿到结果。
	go func() {
		time.Sleep(300 * time.Millisecond)
		// 这个问题没有选项，因此用自由文本回答。
		device.send(protocol.Message{Type: protocol.TypeUserReply, SessionID: sid, RequestID: rid, Status: "answered", AnswerSource: "text", Answer: "继续"})
	}()
	answered := call(agenttools.GetUserReply, map[string]any{"session_id": sid, "request_id": rid, "wait_seconds": 5})
	if answered["status"] != "answered" || answered["answer"] != "继续" {
		t.Fatalf("long poll did not pick up the answer: %v", answered)
	}
}
