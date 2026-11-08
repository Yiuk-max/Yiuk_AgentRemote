package auth

import (
	"errors"
	"testing"
)

func TestAuthenticate(t *testing.T) {
	a := New("correct-player-password", 10)
	remote := "127.0.0.1:1234"
	if err := a.Authenticate(remote, "correct-player-password"); err != nil {
		t.Fatalf("correct password rejected: %v", err)
	}
	if err := a.Authenticate(remote, "wrong"); !errors.Is(err, ErrBadPassword) {
		t.Fatalf("got %v, want ErrBadPassword", err)
	}
}

func TestRateLimit(t *testing.T) {
	a := New("correct-player-password", 1)
	remote := "127.0.0.1:1234"
	if err := a.Authenticate(remote, "wrong"); !errors.Is(err, ErrBadPassword) {
		t.Fatalf("got %v, want ErrBadPassword", err)
	}
	if err := a.Authenticate(remote, "correct-player-password"); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("got %v, want ErrRateLimited", err)
	}
}
