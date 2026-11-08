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

// Allow 按来源地址计数。remote 是接入层看到对端地址字符串
// （host:port 或纯 host），只用于分桶，不参与鉴权。
func (l *Limiter) Allow(remote string) bool {
	ip, _, err := net.SplitHostPort(remote)
	if err != nil {
		ip = remote
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
