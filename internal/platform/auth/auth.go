// Package auth verifies the server player password. The player password only
// proves that a connection may use platform relay resources; it never grants
// access to a device. Device access always requires a separate temporary
// password and, in approval mode, an explicit owner decision.
package auth

import (
	"crypto/subtle"
	"errors"
	"net"
	"time"

	baseauth "yiuk-switchboard/internal/auth"
)

var (
	ErrBadPassword = errors.New("player password is incorrect")
	ErrRateLimited = errors.New("too many authentication attempts")
)

// Authenticator performs constant-time password checks with per-IP rate
// limiting. It stores only the configured secret in process memory.
type Authenticator struct {
	password []byte
	limiter  *baseauth.Limiter
}

// New returns an authenticator for the given player password.
func New(password string, attemptsPerMinute int) *Authenticator {
	return &Authenticator{
		password: []byte(password),
		limiter:  baseauth.NewLimiter(attemptsPerMinute, time.Minute),
	}
}

// Authenticate verifies a candidate password. The rate limiter is consulted
// before the comparison so repeated failures cannot be used to probe the
// secret cheaply.
func (a *Authenticator) Authenticate(remote net.Addr, candidate string) error {
	if a == nil {
		return ErrBadPassword
	}
	if remote != nil && a.limiter != nil && !a.limiter.Allow(remote) {
		return ErrRateLimited
	}
	if subtle.ConstantTimeCompare([]byte(candidate), a.password) != 1 {
		return ErrBadPassword
	}
	return nil
}
