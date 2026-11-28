package gateway_test

import (
	"testing"

	"agentremote/internal/platform/protocol"
)

func TestUserQuestionRequiresApprovalAndDeviceBinding(t *testing.T) {
	address, _ := startServer(t, nil)
	device := dialClient(t, address)
	device.auth(testOperatorPassword)
	device.register("approval-question", testDevicePassword)
	op := dialClient(t, address)
	op.auth(testOperatorPassword)
	op.send(protocol.Message{Type: protocol.TypeRequestSession, DeviceID: "approval-question", Purpose: "approval needed"})
	sid := op.recv(protocol.TypeSessionCreated).SessionID
	device.recv(protocol.TypeSessionRequest)
	ask := protocol.Message{Type: protocol.TypeAskUser, SessionID: sid, RequestID: "q", Question: "question"}
	op.send(ask)
	op.recv(protocol.TypeError)
	device.send(protocol.Message{Type: protocol.TypeApproveSession, SessionID: sid, TemporaryPassword: testDevicePassword})
	op.recv(protocol.TypeSessionStatus)
	op.send(ask)
	op.recv(protocol.TypeUserQuestionAccepted)
	device.recv(protocol.TypeAskUser)
	otherDevice := dialClient(t, address)
	otherDevice.auth(testOperatorPassword)
	otherDevice.register("unrelated-question-device", testDevicePassword)
	otherDevice.send(protocol.Message{Type: protocol.TypeUserReply, SessionID: sid, RequestID: "q", Status: "answered", AnswerSource: "text", Answer: "spoof"})
	otherDevice.recv(protocol.TypeError)
	op.send(protocol.Message{Type: protocol.TypeGetUserReply, SessionID: sid, RequestID: "q"})
	if got := op.recv(protocol.TypeUserReply); got.Status != "pending" {
		t.Fatal(got)
	}
	device.send(protocol.Message{Type: protocol.TypeUserQuestionFailed, SessionID: sid, RequestID: "q", Code: "INVALID_ARGUMENT"})
	if got := op.recv(protocol.TypeUserReply); got.Status != "result_unknown" {
		t.Fatal(got)
	}
	device.recv(protocol.TypeCancelUserQuestion)
	// An operator disconnect also dismisses the question on the device.
	ask.RequestID = "operator-disconnect"
	op.send(ask)
	op.recv(protocol.TypeUserQuestionAccepted)
	device.recv(protocol.TypeAskUser)
	op.conn.Close()
	if got := device.recv(protocol.TypeCancelUserQuestion); got.RequestID != ask.RequestID || got.Status != "result_unknown" {
		t.Fatal(got)
	}
}
