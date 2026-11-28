package agenttools

import (
	"context"
	"encoding/json"
	"testing"
)

// drag：原生拖动（按住左键 -> 移动 -> 抬起）。

func TestDragUsesNativeChannel(t *testing.T) {
	backend := nativeBackendWithImage(t)
	var dragArguments map[string]any
	backend.nativeImpl = func(_ string, operation string, arguments json.RawMessage) (json.RawMessage, error) {
		switch operation {
		case nativeScreenshotOperation:
			return captureEnvelope(), nil
		case nativeDragOperation:
			if err := json.Unmarshal(arguments, &dragArguments); err != nil {
				t.Fatalf("drag arguments are not JSON: %v", err)
			}
			return json.RawMessage(`{"from":[100,200],"to":[300,400],"screen_from":[163,326],` +
				`"screen_to":[490,653],"duration_ms":800,"hold_ms":120,"released":true}`), nil
		}
		t.Fatalf("unexpected native operation %q", operation)
		return nil, nil
	}
	core := NewCore(backend)

	if shot := core.Call(context.Background(), TakeScreenshot, json.RawMessage(`{"session_id":"s1"}`)); !shot.OK {
		t.Fatalf("screenshot failed: %#v", shot)
	}
	response := core.Call(context.Background(), Drag, json.RawMessage(
		`{"session_id":"s1","from":[100,200],"to":[300,400],"duration_ms":800,"hold_ms":120}`))
	if !response.OK {
		t.Fatalf("drag failed: %#v", response)
	}
	if backend.commands != 0 {
		t.Fatalf("drag ran %d scripts, want 0", backend.commands)
	}
	if dragArguments["screenshot_id"] != "0123456789ab" {
		t.Fatalf("screenshot_id was not forwarded: %#v", dragArguments)
	}
	if duration, _ := dragArguments["duration_ms"].(float64); duration != 800 {
		t.Fatalf("duration_ms was not forwarded: %#v", dragArguments["duration_ms"])
	}
	if hold, _ := dragArguments["hold_ms"].(float64); hold != 120 {
		t.Fatalf("hold_ms was not forwarded: %#v", dragArguments["hold_ms"])
	}

	data, _ := response.Data.(map[string]any)
	if data["released"] != true || data["input_source"] != "native" {
		t.Fatalf("unexpected drag result %#v", data)
	}
	if data["duration_ms"] != 800 || data["hold_ms"] != 120 {
		t.Fatalf("timings missing from the result: %#v", data)
	}
	// 坐标必须回显截图坐标与物理坐标两套，agent 才能自己核对。
	if from, ok := data["from"].([]int); !ok || len(from) != 2 {
		t.Fatalf("from missing: %#v", data["from"])
	}
	if screenFrom, ok := data["screen_from"].([]int); !ok || len(screenFrom) != 2 {
		t.Fatalf("screen_from missing: %#v", data["screen_from"])
	}
}

func TestDragRequiresClientSupport(t *testing.T) {
	backend := nativeBackendWithImage(t)
	backend.nativeImpl = func(_ string, operation string, _ json.RawMessage) (json.RawMessage, error) {
		if operation == nativeScreenshotOperation {
			return captureEnvelope(), nil
		}
		return nil, &Error{Code: "UNSUPPORTED_CLIENT", Message: "device does not advertise " + operation}
	}
	core := NewCore(backend)
	if shot := core.Call(context.Background(), TakeScreenshot, json.RawMessage(`{"session_id":"s1"}`)); !shot.OK {
		t.Fatalf("screenshot failed: %#v", shot)
	}
	commandsBefore := backend.commands
	response := core.Call(context.Background(), Drag, json.RawMessage(
		`{"session_id":"s1","from":[1,2],"to":[3,4]}`))
	if response.OK || response.Code != "UNSUPPORTED_CLIENT" || response.Hint == "" {
		t.Fatalf("got %#v, want UNSUPPORTED_CLIENT with a hint", response)
	}
	if backend.commands != commandsBefore {
		t.Fatal("drag must not fall back to scripts")
	}
}

func TestDragRejectsBadArgumentsAndMissingRelease(t *testing.T) {
	backend := nativeBackendWithImage(t)
	backend.nativeImpl = func(_ string, operation string, _ json.RawMessage) (json.RawMessage, error) {
		if operation == nativeScreenshotOperation {
			return captureEnvelope(), nil
		}
		// 设备说拖动跑完了，但没确认抬起：不能报成功。
		return json.RawMessage(`{"from":[1,2],"to":[3,4],"duration_ms":400,"hold_ms":50,"released":false}`), nil
	}
	core := NewCore(backend)
	if shot := core.Call(context.Background(), TakeScreenshot, json.RawMessage(`{"session_id":"s1"}`)); !shot.OK {
		t.Fatalf("screenshot failed: %#v", shot)
	}

	same := core.Call(context.Background(), Drag, json.RawMessage(`{"session_id":"s1","from":[5,5],"to":[5,5]}`))
	if same.OK || same.Code != "INVALID_ARGUMENT" {
		t.Fatalf("got %#v, want INVALID_ARGUMENT for a zero-length drag", same)
	}
	outOfBounds := core.Call(context.Background(), Drag, json.RawMessage(
		`{"session_id":"s1","from":[1,2],"to":[99999,4]}`))
	if outOfBounds.OK || outOfBounds.Code != "INVALID_ARGUMENT" {
		t.Fatalf("got %#v, want INVALID_ARGUMENT for out-of-screenshot coordinates", outOfBounds)
	}
	notReleased := core.Call(context.Background(), Drag, json.RawMessage(
		`{"session_id":"s1","from":[1,2],"to":[30,40]}`))
	if notReleased.OK || notReleased.Code != "DRAG_NOT_RELEASED" {
		t.Fatalf("got %#v, want DRAG_NOT_RELEASED", notReleased)
	}

	// 没有截图就不能拖：坐标基准必须先建立。
	fresh := NewCore(backend)
	missing := fresh.Call(context.Background(), Drag, json.RawMessage(`{"session_id":"s2","from":[1,2],"to":[3,4]}`))
	if missing.OK || missing.Code != "SCREENSHOT_REQUIRED" {
		t.Fatalf("got %#v, want SCREENSHOT_REQUIRED", missing)
	}
}

func TestMoveMouseForwardsDuration(t *testing.T) {
	backend := nativeBackendWithImage(t)
	var moveArguments map[string]any
	backend.nativeImpl = func(_ string, operation string, arguments json.RawMessage) (json.RawMessage, error) {
		switch operation {
		case nativeScreenshotOperation:
			return captureEnvelope(), nil
		case nativeMoveOperation:
			_ = json.Unmarshal(arguments, &moveArguments)
			return json.RawMessage(`{"x":4,"y":5,"screen_x":10,"screen_y":8,"duration_ms":700,` +
				`"foreground_title":"桌面"}`), nil
		}
		t.Fatalf("unexpected native operation %q", operation)
		return nil, nil
	}
	core := NewCore(backend)
	if shot := core.Call(context.Background(), TakeScreenshot, json.RawMessage(`{"session_id":"s1"}`)); !shot.OK {
		t.Fatalf("screenshot failed: %#v", shot)
	}
	response := core.Call(context.Background(), MoveMouse, json.RawMessage(
		`{"session_id":"s1","x":4,"y":5,"duration_ms":700}`))
	if !response.OK {
		t.Fatalf("move failed: %#v", response)
	}
	if duration, _ := moveArguments["duration_ms"].(float64); duration != 700 {
		t.Fatalf("duration_ms was not forwarded: %#v", moveArguments["duration_ms"])
	}
	data, _ := response.Data.(map[string]any)
	if data["input_source"] != "native" {
		t.Fatalf("input_source = %#v", data["input_source"])
	}
}
