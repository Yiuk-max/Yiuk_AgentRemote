package protocol

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"testing"
)

func TestCodecRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	codec := NewCodec(1024)
	in := Message{
		Version: Version, BusinessType: BusinessAgentAssist, Type: TypeSessionStatus,
		SessionID: "s1", DeviceID: "d1", Status: StatusApproved,
		Session: &SessionInfo{SessionID: "s1", DeviceID: "d1", Status: StatusApproved, Permissions: []string{"execute_command"}},
	}
	if err := codec.Write(&buf, in); err != nil {
		t.Fatal(err)
	}
	out, err := codec.Read(bufio.NewReader(&buf))
	if err != nil {
		t.Fatal(err)
	}
	if out.SessionID != in.SessionID || out.Session == nil || out.Session.Status != StatusApproved {
		t.Fatalf("round trip mismatch: %#v", out)
	}
}

func TestCodecRejectsOversizedFrame(t *testing.T) {
	var buf bytes.Buffer
	header := make([]byte, 4)
	binary.BigEndian.PutUint32(header, 2048)
	buf.Write(header)
	buf.Write(make([]byte, 2048))
	_, err := NewCodec(1024).Read(bufio.NewReader(&buf))
	if !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("got %v, want ErrFrameTooLarge", err)
	}
}

func TestCodecRejectsUnsupportedVersion(t *testing.T) {
	var buf bytes.Buffer
	codec := NewCodec(1024)
	if err := codec.Write(&buf, Message{Version: 2, Type: TypePing}); err != nil {
		t.Fatal(err)
	}
	_, err := codec.Read(bufio.NewReader(&buf))
	if err == nil {
		t.Fatal("expected unsupported version error")
	}
}

func TestCodecRejectsEmptyFrame(t *testing.T) {
	var buf bytes.Buffer
	buf.Write([]byte{0, 0, 0, 0})
	_, err := NewCodec(1024).Read(bufio.NewReader(&buf))
	if err == nil {
		t.Fatal("expected empty frame error")
	}
}
