package auth

import (
	"net"
	"sync"
	"time"
)

type bucket struct {
	started time.Time
	count   int
}

type Limiter struct {
	mu     sync.Mutex
	byIP   map[string]bucket
	limit  int
	window time.Duration
}

func NewLimiter(limit int, window time.Duration) *Limiter {
	return &Limiter{byIP: make(map[string]bucket), limit: limit, window: window}
}

func (l *Limiter) Allow(addr net.Addr) bool {
	ip, _, err := net.SplitHostPort(addr.String())
	if err != nil {
		ip = addr.String()
	}
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	b := l.byIP[ip]
	if now.Sub(b.started) >= l.window {
		b = bucket{started: now}
	}
	if _, exists := l.byIP[ip]; !exists && len(l.byIP) >= 4096 { //if the map is too large, clean up old entries
		for key, old := range l.byIP {
			if now.Sub(old.started) >= l.window {
				delete(l.byIP, key)
			}
		}
		if len(l.byIP) >= 4096 {
			return false
		}
	}
	if b.count >= l.limit {
		l.byIP[ip] = b
		return false
	}
	b.count++
	l.byIP[ip] = b
	return true
}
