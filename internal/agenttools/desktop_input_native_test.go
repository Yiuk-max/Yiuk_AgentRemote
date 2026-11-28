package agenttools

import (
	"context"
	"encoding/json"
	"testing"
)

// 原生点击 / 移动的优先与回退。fake 后端同时实现 Backend 与 NativeInvoker，
// 所以这里的断言能直接看到"到底走了哪条路径"。

func captureEnvelope() json.RawMessage {
	return json.RawMessage(`{"screenshot_id":"0123456789ab","path":"C:\\temp\\shot.png","format":"png",` +
		`"width":1568,"height":980,"screen_left":-1920,"screen_top":0,"screen_width":3840,"screen_height":1600}`)
}

func TestClickPrefersNativeInput(t *testing.T) {
	backend := nativeBackendWithImage(t)
	var clickArguments map[string]any
	backend.nativeImpl = func(sessionID, operation string, arguments json.RawMessage) (json.RawMessage, error) {
		switch operation {
		case nativeScreenshotOperation:
			return captureEnvelope(), nil
		case nativeClickOperation:
			if err := json.Unmarshal(arguments, &clickArguments); err != nil {
				t.Fatalf("click arguments are not JSON: %v", err)
			}
			return json.RawMessage(`{"x":100,"y":200,"screen_x":-1675,"screen_y":326,` +
				`"button":"left","double":false,"foreground_title":"未命名 - 记事本",` +
				`"diff_ratio":0.0041,"changed_ratio":0.0089}`), nil
		}
		t.Fatalf("unexpected native operation %q", operation)
		return nil, nil
	}
	core := NewCore(backend)

	if shot := core.Call(context.Background(), TakeScreenshot, json.RawMessage(`{"session_id":"s1"}`)); !shot.OK {
		t.Fatalf("screenshot failed: %#v", shot)
	}
	response := core.Call(context.Background(), Click, json.RawMessage(
		`{"session_id":"s1","x":100,"y":200,"detect_change":true,"screenshot_id":"0123456789ab"}`))
	if !response.OK {
		t.Fatalf("native click failed: %#v", response)
	}
	// 原生路径不能再启动 PowerShell，也不能走脚本的截图节流。
	if backend.commands != 0 {
		t.Fatalf("native click ran %d scripts, want 0", backend.commands)
	}
	// 坐标、幂等键与变化检测参数必须原样送到设备。
	if clickArguments["screenshot_id"] != "0123456789ab" || clickArguments["detect_change"] != true {
		t.Fatalf("unexpected native click arguments %#v", clickArguments)
	}
	if settle, _ := clickArguments["settle_ms"].(float64); settle != 250 {
		t.Fatalf("detect_change without settle_ms should default to 250, got %#v", clickArguments["settle_ms"])
	}

	data, _ := response.Data.(map[string]any)
	if data["input_source"] != "native" {
		t.Fatalf("input_source = %#v, want native", data["input_source"])
	}
	if data["changed"] != true {
		t.Fatalf("changed = %#v, want true for diff 0.0041 / strong 0.0089", data["changed"])
	}
	if data["foreground_title"] != "未命名 - 记事本" {
		t.Fatalf("foreground_title = %#v", data["foreground_title"])
	}
	// 屏幕坐标以 Go 侧校验过的换算为准（截图 1568 宽 -> 3840 物理宽，原点 -1920）。
	if data["screen_x"] != -1675 || data["screen_y"] != 327 {
		t.Fatalf("screen coordinates = (%#v,%#v), want (-1675,327)", data["screen_x"], data["screen_y"])
	}
}

func TestClickFallsBackToScriptWithoutNativeInput(t *testing.T) {
	backend := nativeBackendWithImage(t)
	backend.nativeImpl = func(_ string, operation string, _ json.RawMessage) (json.RawMessage, error) {
		return nil, &Error{Code: "UNSUPPORTED_CLIENT", Message: "device does not advertise " + operation}
	}
	core := NewCore(backend)

	if shot := core.Call(context.Background(), TakeScreenshot, json.RawMessage(`{"session_id":"s1"}`)); !shot.OK {
		t.Fatalf("script screenshot failed: %#v", shot)
	}
	response := core.Call(context.Background(), Click, json.RawMessage(`{"session_id":"s1","x":10,"y":10}`))
	if !response.OK {
		t.Fatalf("script click failed: %#v", response)
	}
	if backend.commands == 0 {
		t.Fatal("fallback did not run the click script")
	}
	data, _ := response.Data.(map[string]any)
	if data["input_source"] != "script" {
		t.Fatalf("input_source = %#v, want script", data["input_source"])
	}
}

func TestMoveMousePrefersNativeInput(t *testing.T) {
	backend := nativeBackendWithImage(t)
	var moveArguments map[string]any
	backend.nativeImpl = func(_ string, operation string, arguments json.RawMessage) (json.RawMessage, error) {
		switch operation {
		case nativeScreenshotOperation:
			return captureEnvelope(), nil
		case nativeMoveOperation:
			if err := json.Unmarshal(arguments, &moveArguments); err != nil {
				t.Fatalf("move arguments are not JSON: %v", err)
			}
			return json.RawMessage(`{"x":4,"y":5,"screen_x":-1910,"screen_y":8,"duration_ms":0,"settle_ms":120,` +
				`"foreground_title":"桌面"}`), nil
		}
		t.Fatalf("unexpected native operation %q", operation)
		return nil, nil
	}
	core := NewCore(backend)

	if shot := core.Call(context.Background(), TakeScreenshot, json.RawMessage(`{"session_id":"s1"}`)); !shot.OK {
		t.Fatalf("screenshot failed: %#v", shot)
	}
	response := core.Call(context.Background(), MoveMouse, json.RawMessage(`{"session_id":"s1","x":4,"y":5,"settle_ms":120}`))
	if !response.OK {
		t.Fatalf("native move failed: %#v", response)
	}
	if backend.commands != 0 {
		t.Fatalf("native move ran %d scripts, want 0", backend.commands)
	}
	if duration, _ := moveArguments["duration_ms"].(float64); duration != 0 {
		t.Fatalf("duration_ms should be 0 until P4 exposes it, got %#v", moveArguments["duration_ms"])
	}
	data, _ := response.Data.(map[string]any)
	if data["input_source"] != "native" || data["settle_ms"] != 120 {
		t.Fatalf("unexpected move result %#v", data)
	}
	if data["foreground_title"] != "桌面" {
		t.Fatalf("foreground_title = %#v", data["foreground_title"])
	}
}

func TestNativeInputErrorsAreNotRetriedWithScripts(t *testing.T) {
	backend := nativeBackendWithImage(t)
	backend.nativeImpl = func(_ string, operation string, _ json.RawMessage) (json.RawMessage, error) {
		if operation == nativeScreenshotOperation {
			return captureEnvelope(), nil
		}
		// 设备明确说这个 screenshot_id 过期了：这是业务失败，不能退回脚本再点一次。
		return nil, &Error{Code: "STALE_SCREENSHOT", Message: "screenshot_id is stale"}
	}
	core := NewCore(backend)
	if shot := core.Call(context.Background(), TakeScreenshot, json.RawMessage(`{"session_id":"s1"}`)); !shot.OK {
		t.Fatalf("screenshot failed: %#v", shot)
	}

	commandsBefore := backend.commands
	response := core.Call(context.Background(), Click, json.RawMessage(`{"session_id":"s1","x":1,"y":1}`))
	if response.OK || response.Code != "STALE_SCREENSHOT" {
		t.Fatalf("got %#v, want STALE_SCREENSHOT", response)
	}
	if backend.commands != commandsBefore {
		t.Fatal("native business error must not trigger the script path")
	}
}
