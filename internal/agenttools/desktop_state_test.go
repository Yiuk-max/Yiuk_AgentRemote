package agenttools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestScreenshotIDIsBoundToLatestShot(t *testing.T) {
	core := NewCore(nil)
	geometry := screenGeometry{Left: 0, Top: 0, ScreenWidth: 2000, ScreenHeight: 1000, Width: 1000, Height: 500}
	first := core.desktop.newScreenshotID("s")
	core.desktop.remember("s", screenshotRef{ID: first, Geometry: geometry})

	if err := core.requireFreshScreenshot("s", first); err != nil {
		t.Fatalf("latest id rejected: %v", err)
	}
	if err := core.requireFreshScreenshot("s", ""); err != nil {
		t.Fatalf("omitted id must fall back to the latest shot: %v", err)
	}

	second := core.desktop.newScreenshotID("s")
	core.desktop.remember("s", screenshotRef{ID: second, Geometry: geometry})
	err := core.requireFreshScreenshot("s", first)
	toolErr, ok := err.(*Error)
	if !ok || toolErr.Code != "STALE_SCREENSHOT" {
		t.Fatalf("expected STALE_SCREENSHOT, got %v", err)
	}
	if !strings.Contains(toolErr.Message, first) || !strings.Contains(toolErr.Message, second) {
		t.Fatalf("message should name both ids: %q", toolErr.Message)
	}

	if err := core.requireFreshScreenshot("s", "deadbeefcafe"); err == nil {
		t.Fatal("unknown id must be rejected")
	}
	// 省略 id 表示“沿用最新”；没有截图时由 screenPoint 报 SCREENSHOT_REQUIRED。
	if err := core.requireFreshScreenshot("other-session", ""); err != nil {
		t.Fatalf("omitted id must not be rejected on its own: %v", err)
	}
	if _, _, _, err := core.screenPoint("other-session", 0, 0); err == nil {
		t.Fatal("screenPoint must require a screenshot")
	}
}

func TestScreenshotHistoryKeepsOnlyRecent(t *testing.T) {
	core := NewCore(nil)
	geometry := screenGeometry{Width: 10, Height: 10, ScreenWidth: 10, ScreenHeight: 10}
	var ids []string
	for i := 0; i < maxScreenshotHistory+2; i++ {
		id := core.desktop.newScreenshotID("s")
		ids = append(ids, id)
		core.desktop.remember("s", screenshotRef{ID: id, Geometry: geometry})
	}
	if _, age, ok := core.desktop.find("s", ids[0]); ok {
		t.Fatalf("oldest id should be evicted, found at age %d", age)
	}
	if _, age, ok := core.desktop.find("s", ids[len(ids)-1]); !ok || age != 0 {
		t.Fatalf("newest id should be at age 0, got %d/%v", age, ok)
	}
}

func TestScreenshotIDsAreUnique(t *testing.T) {
	core := NewCore(nil)
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		id := core.desktop.newScreenshotID("s")
		if len(id) != 12 {
			t.Fatalf("unexpected id length %q", id)
		}
		if seen[id] {
			t.Fatalf("duplicate id %q", id)
		}
		seen[id] = true
	}
}

func TestScreenshotSlotSerializesAndThrottles(t *testing.T) {
	core := NewCore(nil)
	ctx := context.Background()
	var started []time.Time
	for i := 0; i < 3; i++ {
		release, err := core.acquireScreenshotSlot(ctx, "s")
		if err != nil {
			t.Fatal(err)
		}
		started = append(started, time.Now())
		release()
	}
	for i := 1; i < len(started); i++ {
		if gap := started[i].Sub(started[i-1]); gap < minScreenshotInterval-50*time.Millisecond {
			t.Fatalf("screenshot %d started only %v after the previous one", i, gap)
		}
	}

	// 取消等待时必须立刻返回，而不是占着锁。
	release, err := core.acquireScreenshotSlot(ctx, "s2")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	cancelled, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := core.acquireScreenshotSlot(cancelled, "s2")
		done <- err
	}()
	select {
	case err := <-done:
		toolErr, ok := err.(*Error)
		if !ok || toolErr.Code != "CANCELLED" {
			t.Fatalf("expected CANCELLED, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancelled screenshot slot did not return")
	}
}

func TestBatchTwoValidation(t *testing.T) {
	bad := []struct{ tool, input string }{
		{Click, `{"session_id":"s","x":1,"y":1,"settle_ms":2001}`},
		{Click, `{"session_id":"s","x":1,"y":1,"screenshot_id":""}`},
		{MoveMouse, `{"session_id":"s","x":1,"y":1,"screenshot_id":""}`},
		{GetSessionStatus, `{"session_id":"s","wait_seconds":26}`},
		{GetSessionStatus, `{"session_id":"s","wait_seconds":-1}`},
		{GetUserReply, `{"session_id":"s","request_id":"r","wait_seconds":26}`},
		{RequestConnection, `{"device_id":"d","purpose":"p","wait_for_approval_seconds":26}`},
		{RunCommand, `{"device_id":"d","temporary_password":"` + strings.Repeat("a", 32) + `","command":"c","wait_for_approval_seconds":26}`},
	}
	for _, tc := range bad {
		if err := validateInput(json.RawMessage(tc.input), tc.tool); err == nil {
			t.Fatalf("%s accepted %s", tc.tool, tc.input)
		}
	}
	good := []struct{ tool, input string }{
		{Click, `{"session_id":"s","x":1,"y":1,"screenshot_id":"a1b2c3d4e5f6","detect_change":true,"settle_ms":2000}`},
		{MoveMouse, `{"session_id":"s","x":1,"y":1,"screenshot_id":"a1b2c3d4e5f6"}`},
		{GetSessionStatus, `{"session_id":"s","wait_seconds":25}`},
		{GetUserReply, `{"session_id":"s","request_id":"r","wait_seconds":25}`},
		{RequestConnection, `{"device_id":"d","purpose":"p","wait_for_approval_seconds":25}`},
	}
	for _, tc := range good {
		if err := validateInput(json.RawMessage(tc.input), tc.tool); err != nil {
			t.Fatalf("%s rejected %s: %v", tc.tool, tc.input, err)
		}
	}
}
