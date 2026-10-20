package room

import (
	"io"
	"net"
	"testing"
	"time"

	"yiuk-switchboard/internal/protocol"
)

func TestLocalHostFastPath(t *testing.T) {
	backend, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()
	go func() {
		conn, err := backend.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = io.Copy(conn, conn)
	}()

	players, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer players.Close()
	notified := make(chan struct{}, 1)
	r := &room{id: "local", localPort: backend.Addr().(*net.TCPAddr).Port,
		hostIP: net.ParseIP("127.0.0.1"), max: 1, owner: func(protocol.Message) error { notified <- struct{}{}; return nil },
		active: make(map[net.Conn]struct{})}
	m := NewManager(Options{SessionIdleTimeout: time.Second})
	finished := make(chan struct{})
	go func() {
		conn, err := players.Accept()
		if err == nil {
			m.player(r, conn)
		}
		close(finished)
	}()
	client, err := net.Dial("tcp", players.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	_ = client.SetDeadline(time.Now().Add(2 * time.Second))
	payload := []byte("minecraft-packet")
	if _, err := client.Write(payload); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(client, got); err != nil {
		t.Fatal(err)
	}
	if string(got) != string(payload) {
		t.Fatalf("echo = %q, want %q", got, payload)
	}
	select {
	case <-notified:
		t.Fatal("fast path unexpectedly requested Java host forwarding")
	default:
	}
	_ = client.Close()
	select {
	case <-finished:
	case <-time.After(2 * time.Second):
		t.Fatal("fast-path session did not stop")
	}
}
