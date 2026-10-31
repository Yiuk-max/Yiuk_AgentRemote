package transport

import (
	"net"
	"testing"

	"agentremote/internal/platform/protocol"
	"github.com/gorilla/websocket"
)

func TestWebSocketMessagesPreserveMessageBoundaries(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ws := ServeWebSocketListener(ln, protocol.DefaultMaxMessageBytes)
	defer ws.Close()
	client, _, err := websocket.DefaultDialer.Dial("ws://"+ln.Addr().String(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	serverConn, err := ws.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer serverConn.Close()

	codec := protocol.NewCodec(1024)
	want := []protocol.Message{{Type: protocol.TypePing, Sequence: 42}, {Type: protocol.TypePong, Sequence: 43}}
	for _, message := range want {
		data, err := codec.Encode(message)
		if err != nil {
			t.Fatal(err)
		}
		if err := client.WriteMessage(websocket.BinaryMessage, data); err != nil {
			t.Fatal(err)
		}
		gotData, err := serverConn.ReadMessage()
		if err != nil {
			t.Fatal(err)
		}
		got, err := codec.Decode(gotData)
		if err != nil {
			t.Fatal(err)
		}
		if got.Type != message.Type || got.Sequence != message.Sequence {
			t.Fatalf("got %#v, want %#v", got, message)
		}
	}
	response, err := codec.Encode(want[0])
	if err != nil {
		t.Fatal(err)
	}
	if err := serverConn.WriteMessage(response); err != nil {
		t.Fatal(err)
	}
	kind, data, err := client.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	if kind != websocket.BinaryMessage {
		t.Fatalf("got WebSocket message kind %d", kind)
	}
	got, err := codec.Decode(data)
	if err != nil {
		t.Fatal(err)
	}
	if got.Type != want[0].Type || got.Sequence != want[0].Sequence {
		t.Fatalf("got %#v, want %#v", got, want[0])
	}
}

func TestListenRequiresTLSByDefault(t *testing.T) {
	if _, err := Listen("127.0.0.1:0", "", "", false); err == nil {
		t.Fatal("expected TLS requirement error")
	}
}

func TestListenAllowsPlaintextOnlyWhenExplicit(t *testing.T) {
	ln, err := Listen("127.0.0.1:0", "", "", true)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	if _, ok := ln.(*net.TCPListener); !ok {
		t.Fatalf("got listener type %T, want *net.TCPListener", ln)
	}
}

func TestListenRejectsPartialTLSConfig(t *testing.T) {
	if _, err := Listen("127.0.0.1:0", "cert.pem", "", true); err == nil {
		t.Fatal("expected partial TLS config error")
	}
}
