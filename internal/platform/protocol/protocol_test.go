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
	if err != nil { t.Fatal(err) }
	out, err := codec.Decode(data)
	if err != nil { t.Fatal(err) }
	if out.SessionID != in.SessionID || out.Session == nil || out.Session.Status != StatusApproved {
		t.Fatalf("round trip mismatch: %#v", out)
	}
}

func TestCodecRejectsOversizedMessage(t *testing.T) {
	_, err := NewCodec(16).Encode(Message{Type: TypePing, Message: "this message is too large"})
	if !errors.Is(err, ErrMessageTooLarge) { t.Fatalf("got %v, want ErrMessageTooLarge", err) }
	_, err = NewCodec(16).Decode(make([]byte, 17))
	if !errors.Is(err, ErrMessageTooLarge) { t.Fatalf("got %v, want ErrMessageTooLarge", err) }
}

func TestCodecRejectsUnsupportedVersion(t *testing.T) {
	data, err := NewCodec(1024).Encode(Message{Version: 2, Type: TypePing})
	if err != nil { t.Fatal(err) }
	if _, err := NewCodec(1024).Decode(data); err == nil { t.Fatal("expected unsupported version error") }
}

func TestCodecRejectsEmptyMessage(t *testing.T) {
	if _, err := NewCodec(1024).Decode(nil); err == nil { t.Fatal("expected empty message error") }
}
