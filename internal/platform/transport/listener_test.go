package transport

import (
	"net"
	"testing"
)

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
