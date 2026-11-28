package protocol

import (
	"errors"
	"testing"
)

func TestCodecRoundTrip(t *testing.T) {
	codec := NewCodec(1024)
	in := Message{
		Version: Version, BusinessType: BusinessAgentAssist, Type: TypeSessionStatus,
		SessionID: "s1", DeviceID: "d1", Status: StatusApproved,
		Session: &SessionInfo{SessionID: "s1", DeviceID: "d1", Status: StatusApproved, Permissions: []string{"execute_command"}},
	}
	data, err := codec.Encode(in)
	if err != nil {
		t.Fatal(err)
	}
	out, err := codec.Decode(data)
	if err != nil {
		t.Fatal(err)
	}
	if out.SessionID != in.SessionID || out.Session == nil || out.Session.Status != StatusApproved {
		t.Fatalf("round trip mismatch: %#v", out)
	}
}

func TestCodecRejectsOversizedMessage(t *testing.T) {
	_, err := NewCodec(16).Encode(Message{Type: TypePing, Message: "this message is too large"})
	if !errors.Is(err, ErrMessageTooLarge) {
		t.Fatalf("got %v, want ErrMessageTooLarge", err)
	}
	_, err = NewCodec(16).Decode(make([]byte, 17))
	if !errors.Is(err, ErrMessageTooLarge) {
		t.Fatalf("got %v, want ErrMessageTooLarge", err)
	}
}

func TestCodecRejectsUnsupportedVersion(t *testing.T) {
	data, err := NewCodec(1024).Encode(Message{Version: 2, Type: TypePing})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewCodec(1024).Decode(data); err == nil {
		t.Fatal("expected unsupported version error")
	}
}

func TestCodecRejectsEmptyMessage(t *testing.T) {
	if _, err := NewCodec(1024).Decode(nil); err == nil {
		t.Fatal("expected empty message error")
	}
}

// 旧客户端（A' 之前）还会在注册帧里带 "mode" 字段。这个字段已经从契约里删掉了，
// 但服务端必须仍然接受这些帧：删字段不能变成一次强制升级。
func TestCodecIgnoresFieldsRemovedFromTheContract(t *testing.T) {
	legacy := []byte(`{"version":1,"business_type":"agent-assist","type":"register_device",
		"device_id":"111222333","device_name":"old-client","platform":"windows",
		"mode":"approval","temporary_password":"0123456789abcdef0123456789abcdef"}`)
	message, err := NewCodec(4096).Decode(legacy)
	if err != nil {
		t.Fatalf("a legacy frame with an unknown field must still decode: %v", err)
	}
	if message.DeviceID != "111222333" || message.Type != TypeRegisterDevice {
		t.Fatalf("unexpected decode result: %#v", message)
	}
}
