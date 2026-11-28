package agenttools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sync"
	"time"
)

// 本文件：截屏状态机。

// screenGeometry 记录某会话最近一次截屏的图像尺寸与对应的物理屏幕区域。
type screenGeometry struct {
	Left         int `json:"left"`
	Top          int `json:"top"`
	ScreenWidth  int `json:"screen_width"`
	ScreenHeight int `json:"screen_height"`
	Width        int `json:"width"`
	Height       int `json:"height"`
}

// screenshotRef 把「一张截图」的 id 与它当时的屏幕几何绑在一起。
// 最近 5 张都保留，仅用于把 STALE_SCREENSHOT 的报错说清楚。
type screenshotRef struct {
	ID       string
	Geometry screenGeometry
}

// sessionGate 让同一会话的截屏串行执行并保持最小间隔，
// 避免 agent 连发截图把低内存设备打满。用信号量而不是互斥锁，
// 排队期间的取消才能立即生效。
type sessionGate struct {
	sem  chan struct{}
	mu   sync.Mutex
	last time.Time
}

type desktopState struct {
	mu       sync.Mutex
	recent   map[string][]screenshotRef
	sequence uint64
	gates    map[string]*sessionGate
}

const (
	maxScreenshotHistory  = 5
	minScreenshotInterval = 500 * time.Millisecond

	// 变化判据：强变化像素占比，或整块平均差。阈值来自真机测量：
	// 点中按钮（标签文字改变）changed_ratio ≈ 0.004~0.02，
	// 点空白处 ≈ 0.000~0.001。
	changedPixelRatioThreshold = 0.002
	changedMeanThreshold       = 0.02
)

func (s *desktopState) gate(sessionID string) *sessionGate {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.gates == nil {
		s.gates = map[string]*sessionGate{}
	}
	gate, ok := s.gates[sessionID]
	if !ok {
		gate = &sessionGate{sem: make(chan struct{}, 1)}
		s.gates[sessionID] = gate
	}
	return gate
}

// newScreenshotID 生成短 id；只用于绑定坐标基准，不承担安全用途。
func (s *desktopState) newScreenshotID(sessionID string) string {
	s.mu.Lock()
	s.sequence++
	sequence := s.sequence
	s.mu.Unlock()
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s:%d:%d", sessionID, sequence, time.Now().UnixNano())))
	return hex.EncodeToString(sum[:])[:12]
}

// validScreenshotID 校验客户端上报的截图 id 形状：12 位小写 hex。
// 原生抓屏的 id 由设备端生成，MCP 侧只校验形状，不重新生成。
func validScreenshotID(id string) bool {
	if len(id) != 12 {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

func (s *desktopState) remember(sessionID string, ref screenshotRef) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.recent == nil {
		s.recent = map[string][]screenshotRef{}
	}
	history := append([]screenshotRef{ref}, s.recent[sessionID]...)
	if len(history) > maxScreenshotHistory {
		history = history[:maxScreenshotHistory]
	}
	s.recent[sessionID] = history
}

func (s *desktopState) latest(sessionID string) (screenshotRef, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	history := s.recent[sessionID]
	if len(history) == 0 {
		return screenshotRef{}, false
	}
	return history[0], true
}

// countInHistory 返回该会话保留的截图条数（测试与诊断用）。
func (s *desktopState) countInHistory(sessionID string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.recent[sessionID])
}

// find 返回指定 id 在历史中的位置（0 = 最新）。
func (s *desktopState) find(sessionID, id string) (screenshotRef, int, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for age, ref := range s.recent[sessionID] {
		if ref.ID == id {
			return ref, age, true
		}
	}
	return screenshotRef{}, 0, false
}

// acquireScreenshotSlot 串行化同一会话的截屏，并保证最小间隔；
// 返回的函数用于释放。排队与等待期间都响应 ctx 取消。
func (c *Core) acquireScreenshotSlot(ctx context.Context, sessionID string) (func(), error) {
	gate := c.desktop.gate(sessionID)
	select {
	case gate.sem <- struct{}{}:
	case <-ctx.Done():
		return nil, &Error{Code: "CANCELLED", Message: "tool call was cancelled while waiting for the screenshot slot"}
	}
	release := func() { <-gate.sem }

	gate.mu.Lock()
	last := gate.last
	gate.mu.Unlock()
	if !last.IsZero() {
		if wait := minScreenshotInterval - time.Since(last); wait > 0 {
			timer := time.NewTimer(wait)
			defer timer.Stop()
			select {
			case <-timer.C:
			case <-ctx.Done():
				release()
				return nil, &Error{Code: "CANCELLED", Message: "tool call was cancelled while waiting for the screenshot slot"}
			}
		}
	}
	gate.mu.Lock()
	gate.last = time.Now()
	gate.mu.Unlock()
	return release, nil
}
