package gateway_test

import (
	"testing"
	"time"

	"agentremote/internal/platform/protocol"
)

func TestUserQuestionFlowAndOwnership(t *testing.T) {
	address, _ := startServer(t, nil)
	device := dialClient(t, address)
	device.auth(testOperatorPassword)
	device.register("question-device", testDevicePassword)
	operator := dialClient(t, address)
	operator.auth(testOperatorPassword)
	operator.send(protocol.Message{Type: protocol.TypeRequestSession, DeviceID: "question-device", TemporaryPassword: testDevicePassword, Purpose: "question integration"})
	sessionID := operator.recv(protocol.TypeSessionCreated).SessionID
	device.recv(protocol.TypeSessionStatus)
	attacker := dialClient(t, address)
	attacker.auth(testOperatorPassword)
	ask := protocol.Message{Type: protocol.TypeAskUser, SessionID: sessionID, RequestID: "question-1", Question: "选择安装位置？", Options: []string{"默认", "D盘"}, TimeoutSeconds: 15}
	attacker.send(ask)
	if got := attacker.recv(protocol.TypeError); got.Code == "" {
		t.Fatal(got)
	}
	operator.send(ask)
	accepted := operator.recv(protocol.TypeUserQuestionAccepted)
	if accepted.Status != "pending" || accepted.ExpiresAt <= time.Now().Unix() {
		t.Fatal(accepted)
	}
	forwarded := device.recv(protocol.TypeAskUser)
	if forwarded.Question != ask.Question || len(forwarded.Options) != 2 {
		t.Fatal(forwarded)
	}
	// Identical retransmission is acknowledged, not forwarded again.
	operator.send(ask)
	operator.recv(protocol.TypeUserQuestionAccepted)
	changed := ask
	changed.Question = "changed"
	operator.send(changed)
	if got := operator.recv(protocol.TypeError); got.Code != "REQUEST_CONFLICT" {
		t.Fatal(got)
	}
	busy := ask
	busy.RequestID = "busy"
	operator.send(busy)
	if got := operator.recv(protocol.TypeError); got.Code != "QUESTION_BUSY" {
		t.Fatal(got)
	}
	attacker.send(protocol.Message{Type: protocol.TypeGetUserReply, SessionID: sessionID, RequestID: ask.RequestID})
	if got := attacker.recv(protocol.TypeError); got.Code != "SESSION_NOT_FOUND" {
		t.Fatal(got)
	}
	index := 1
	attacker.send(protocol.Message{Type: protocol.TypeUserReply, SessionID: sessionID, RequestID: ask.RequestID, Status: "answered", AnswerSource: "option", SelectedIndex: &index})
	attacker.recv(protocol.TypeError)
	bad := 9
	device.send(protocol.Message{Type: protocol.TypeUserReply, SessionID: sessionID, RequestID: ask.RequestID, Status: "answered", AnswerSource: "option", SelectedIndex: &bad})
	device.recv(protocol.TypeError)
	device.send(protocol.Message{Type: protocol.TypeUserReply, SessionID: sessionID, RequestID: ask.RequestID, Status: "answered", AnswerSource: "option", SelectedIndex: &index, Answer: "spoofed"})
	answer := operator.recv(protocol.TypeUserReply)
	if answer.Answer != "D盘" || answer.AnswerSource != "option" {
		t.Fatal(answer)
	}
	operator.send(protocol.Message{Type: protocol.TypeGetUserReply, SessionID: sessionID, RequestID: ask.RequestID, CorrelationID: "get1"})
	queried := operator.recvWhere(protocol.TypeUserReply, func(m protocol.Message) bool { return m.CorrelationID == "get1" })
	if queried.Answer != "D盘" {
		t.Fatal(queried)
	}
	ask.RequestID = "text"
	ask.Options = nil
	operator.send(ask)
	operator.recv(protocol.TypeUserQuestionAccepted)
	device.recv(protocol.TypeAskUser)
	device.send(protocol.Message{Type: protocol.TypeUserReply, SessionID: sessionID, RequestID: "text", Status: "answered", AnswerSource: "text", Answer: "自定义 😀"})
	if answer = operator.recv(protocol.TypeUserReply); answer.Answer != "自定义 😀" || answer.SelectedIndex != nil {
		t.Fatal(answer)
	}
	ask.RequestID = "cancel"
	operator.send(ask)
	operator.recv(protocol.TypeUserQuestionAccepted)
	device.recv(protocol.TypeAskUser)
	device.send(protocol.Message{Type: protocol.TypeUserReply, SessionID: sessionID, RequestID: "cancel", Status: "cancelled"})
	if answer = operator.recv(protocol.TypeUserReply); answer.Status != "cancelled" {
		t.Fatal(answer)
	}
	ask.RequestID = "close"
	operator.send(ask)
	operator.recv(protocol.TypeUserQuestionAccepted)
	device.recv(protocol.TypeAskUser)
	operator.send(protocol.Message{Type: protocol.TypeCloseSession, SessionID: sessionID})
	operator.recv(protocol.TypeSessionStatus)
	if got := device.recv(protocol.TypeCancelUserQuestion); got.RequestID != "close" || got.Status != "cancelled" {
		t.Fatal(got)
	}
	operator.send(protocol.Message{Type: protocol.TypeGetUserReply, SessionID: sessionID, RequestID: "close", CorrelationID: "after-close"})
	if got := operator.recvWhere(protocol.TypeUserReply, func(m protocol.Message) bool { return m.CorrelationID == "after-close" }); got.Status != "cancelled" {
		t.Fatal(got)
	}
	operator.send(ask)
	operator.recv(protocol.TypeError)
}

func TestUserQuestionTimeoutAndDisconnect(t *testing.T) {
	for _, mode := range []string{"timeout", "disconnect"} {
		t.Run(mode, func(t *testing.T) {
			address, _ := startServer(t, nil)
			device := dialClient(t, address)
			device.auth(testOperatorPassword)
			device.register("timeout-device", testDevicePassword)
			operator := dialClient(t, address)
			operator.auth(testOperatorPassword)
			operator.send(protocol.Message{Type: protocol.TypeRequestSession, DeviceID: "timeout-device", TemporaryPassword: testDevicePassword, Purpose: "test"})
			sid := operator.recv(protocol.TypeSessionCreated).SessionID
			operator.send(protocol.Message{Type: protocol.TypeAskUser, SessionID: sid, RequestID: "q", Question: "继续？", TimeoutSeconds: 15})
			operator.recv(protocol.TypeUserQuestionAccepted)
			device.recv(protocol.TypeAskUser)
			if mode == "disconnect" {
				device.conn.Close()
				if r := operator.recv(protocol.TypeUserReply); r.Status != "result_unknown" {
					t.Fatal(r)
				}
				return
			}
			// Poll immediately to prove the request path is not held by the pending question.
			operator.send(protocol.Message{Type: protocol.TypeGetUserReply, SessionID: sid, RequestID: "q"})
			if r := operator.recv(protocol.TypeUserReply); r.Status != "pending" {
				t.Fatal(r)
			}
			time.Sleep(16 * time.Second)
			operator.send(protocol.Message{Type: protocol.TypeGetUserReply, SessionID: sid, RequestID: "q", CorrelationID: "expired"})
			if r := operator.recvWhere(protocol.TypeUserReply, func(m protocol.Message) bool { return m.CorrelationID == "expired" }); r.Status != "timed_out" {
				t.Fatal(r)
			}
			if r := device.recv(protocol.TypeCancelUserQuestion); r.Status != "timed_out" {
				t.Fatal(r)
			}
			device.send(protocol.Message{Type: protocol.TypeUserReply, SessionID: sid, RequestID: "q", Status: "answered", AnswerSource: "text", Answer: "late"})
			if r := operator.recv(protocol.TypeUserReply); r.Status != "timed_out" {
				t.Fatal(r)
			}
		})
	}
}
