package agenttools

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// find_on_screen：设备端 UIA 文本定位的 MCP 侧行为。

func findResultEnvelope() json.RawMessage {
	return json.RawMessage(`{"captured":true,"screenshot_id":"abcdef123456","layout_version":"0123456789abcdef",` +
		`"width":1568,"height":980,"screen_left":0,"screen_top":0,"screen_width":2560,"screen_height":1600,` +
		`"matches":[{"bbox":[100,200,180,240],"center":[140,220],"score":1.0,"source":"uia","text":"收藏",` +
		`"control_type":"Button"},{"bbox":[300,200,380,240],"center":[340,220],"score":0.9,"source":"uia",` +
		`"text":"收藏夹","control_type":"Hyperlink"}],"truncated":false,"visited_nodes":42,"elapsed_ms":31,` +
		`"source":"uia"}`)
}

func TestFindOnScreenUsesNativeResult(t *testing.T) {
	backend := nativeBackendWithImage(t)
	var findArguments map[string]any
	backend.nativeImpl = func(_ string, operation string, arguments json.RawMessage) (json.RawMessage, error) {
		switch operation {
		case nativeFindOperation:
			if err := json.Unmarshal(arguments, &findArguments); err != nil {
				t.Fatalf("find arguments are not JSON: %v", err)
			}
			return findResultEnvelope(), nil
		case nativeClickOperation:
			return json.RawMessage(`{"x":140,"y":220,"screen_x":228,"screen_y":359,"button":"left",` +
				`"foreground_title":"记事本"}`), nil
		}
		t.Fatalf("unexpected native operation %q", operation)
		return nil, nil
	}
	core := NewCore(backend)

	response := core.Call(context.Background(), FindOnScreen, json.RawMessage(
		`{"session_id":"s1","text":"  收藏  ","match":"exact","control_type":"Button",`+
			`"region":[0,0,1200,300],"limit":3}`))
	if !response.OK {
		t.Fatalf("find_on_screen failed: %#v", response)
	}
	// 原生查询不需要任何脚本。
	if backend.commands != 0 {
		t.Fatalf("find_on_screen ran %d scripts, want 0", backend.commands)
	}
	// 参数原样送到设备，空白被去掉。
	if findArguments["text"] != "收藏" || findArguments["match"] != "exact" ||
		findArguments["control_type"] != "Button" {
		t.Fatalf("unexpected find arguments %#v", findArguments)
	}
	if region, ok := findArguments["region"].([]any); !ok || len(region) != 4 {
		t.Fatalf("region was not forwarded: %#v", findArguments["region"])
	}
	if limit, _ := findArguments["limit"].(float64); limit != 3 {
		t.Fatalf("limit was not forwarded: %#v", findArguments["limit"])
	}

	data, _ := response.Data.(map[string]any)
	if data["screenshot_id"] != "abcdef123456" {
		t.Fatalf("screenshot_id = %#v", data["screenshot_id"])
	}
	matches, _ := data["matches"].([]map[string]any)
	if len(matches) != 2 || data["count"] != 2 {
		t.Fatalf("unexpected matches %#v", data["matches"])
	}
	if data["source"] != "uia" || data["visited_nodes"] != 42 || data["elapsed_ms"] != 31 {
		t.Fatalf("unexpected diagnostics %#v", data)
	}
	// 结果里必须带坐标基准的几何，agent 才能自己换算。
	if data["screen_width"] != 2560 || data["width"] != 1568 {
		t.Fatalf("geometry missing from the result: %#v", data)
	}

	// 返回的截图 id 必须能直接用于点击：设备建了新基准，Go 侧要同步登记。
	clicked := core.Call(context.Background(), Click, json.RawMessage(
		`{"session_id":"s1","x":140,"y":220,"screenshot_id":"abcdef123456"}`))
	if !clicked.OK {
		t.Fatalf("click with the find screenshot_id failed: %#v", clicked)
	}
	latest, ok := core.desktop.latest("s1")
	if !ok || latest.ID != "abcdef123456" {
		t.Fatalf("find result was not registered as the coordinate base: %#v", latest)
	}
}

func TestFindOnScreenKeepsKnownCoordinateBase(t *testing.T) {
	backend := nativeBackendWithImage(t)
	// 先建立一张已知快照，再让 find 复用同一个 id。
	backend.nativeImpl = func(_ string, operation string, _ json.RawMessage) (json.RawMessage, error) {
		switch operation {
		case nativeScreenshotOperation:
			return captureEnvelope(), nil
		case nativeFindOperation:
			return json.RawMessage(`{"captured":true,"screenshot_id":"0123456789ab","width":1568,"height":980,` +
				`"screen_left":-1920,"screen_top":0,"screen_width":3840,"screen_height":1600,"matches":[]}`), nil
		}
		t.Fatalf("unexpected native operation %q", operation)
		return nil, nil
	}
	core := NewCore(backend)
	if shot := core.Call(context.Background(), TakeScreenshot, json.RawMessage(`{"session_id":"s1"}`)); !shot.OK {
		t.Fatalf("screenshot failed: %#v", shot)
	}
	if found := core.Call(context.Background(), FindOnScreen,
		json.RawMessage(`{"session_id":"s1","text":"收藏"}`)); !found.OK {
		t.Fatalf("find failed: %#v", found)
	}
	// 复用同一个 id 不能把快照历史挤成两条。
	if count := core.desktop.countInHistory("s1"); count != 1 {
		t.Fatalf("find duplicated the snapshot history: %d entries", count)
	}
}

func TestFindOnScreenRequiresClientSupport(t *testing.T) {
	backend := nativeBackendWithImage(t)
	backend.nativeImpl = func(_ string, operation string, _ json.RawMessage) (json.RawMessage, error) {
		return nil, &Error{Code: "UNSUPPORTED_CLIENT", Message: "device does not advertise " + operation}
	}
	core := NewCore(backend)
	commandsBefore := backend.commands
	response := core.Call(context.Background(), FindOnScreen, json.RawMessage(`{"session_id":"s1","text":"收藏"}`))
	if response.OK || response.Code != "UNSUPPORTED_CLIENT" {
		t.Fatalf("got %#v, want UNSUPPORTED_CLIENT", response)
	}
	if response.Hint == "" {
		t.Fatal("UNSUPPORTED_CLIENT must tell the operator to upgrade the client")
	}
	// 没有脚本回退：脚本也不可能做 UIA 查询，回退只会静默给出"找不到"。
	if backend.commands != commandsBefore {
		t.Fatal("find_on_screen must not fall back to scripts")
	}
}

func TestFindOnScreenValidatesArguments(t *testing.T) {
	backend := nativeBackendWithImage(t)
	backend.nativeImpl = func(_ string, operation string, _ json.RawMessage) (json.RawMessage, error) {
		t.Fatalf("validation should stop before any native call (%s)", operation)
		return nil, nil
	}
	core := NewCore(backend)

	blank := core.Call(context.Background(), FindOnScreen, json.RawMessage(`{"session_id":"s1","text":"   "}`))
	if blank.OK || blank.Code != "INVALID_ARGUMENT" {
		t.Fatalf("got %#v, want INVALID_ARGUMENT for blank text", blank)
	}
	badRegion := core.Call(context.Background(), FindOnScreen,
		json.RawMessage(`{"session_id":"s1","text":"收藏","region":[100,100,50,50]}`))
	if badRegion.OK || badRegion.Code != "INVALID_ARGUMENT" {
		t.Fatalf("got %#v, want INVALID_ARGUMENT for an inverted region", badRegion)
	}
	if backend.nativeCalls != 0 {
		t.Fatalf("validation must not reach the device: %d native calls", backend.nativeCalls)
	}
}

// 等待目标出现：每 500ms 一轮的循环在 Go 侧，轮间不占设备（每轮是一个独立原生调用）。
// 测试把间隔调小，只验证轮次、总预算与 timed_out 语义。
func TestFindOnScreenWaitsUntilFound(t *testing.T) {
	old := findPollInterval
	findPollInterval = 2 * time.Millisecond
	defer func() { findPollInterval = old }()

	backend := nativeBackendWithImage(t)
	calls := 0
	backend.nativeImpl = func(_ string, operation string, _ json.RawMessage) (json.RawMessage, error) {
		if operation != nativeFindOperation {
			t.Fatalf("unexpected native operation %q", operation)
		}
		calls++
		if calls < 3 {
			return emptyFindEnvelope(), nil
		}
		return findResultEnvelope(), nil
	}
	core := NewCore(backend)

	response := core.Call(context.Background(), FindOnScreen, json.RawMessage(
		`{"session_id":"s1","text":"收藏","wait_until_found":true,"timeout_seconds":5}`))
	if !response.OK {
		t.Fatalf("waiting find failed: %#v", response)
	}
	if calls != 3 {
		t.Fatalf("device was queried %d times, want 3", calls)
	}
	data, _ := response.Data.(map[string]any)
	if data["attempts"] != 3 || data["timed_out"] != false || data["count"] != 2 {
		t.Fatalf("unexpected waiting result: %#v", data)
	}
	if waited, _ := data["waited_ms"].(int); waited < 4 {
		t.Fatalf("waited_ms should cover the two poll intervals, got %#v", data["waited_ms"])
	}
}

func TestFindOnScreenTimesOutWithExistingCandidates(t *testing.T) {
	old := findPollInterval
	findPollInterval = 2 * time.Millisecond
	defer func() { findPollInterval = old }()

	backend := nativeBackendWithImage(t)
	calls := 0
	backend.nativeImpl = func(_ string, _ string, _ json.RawMessage) (json.RawMessage, error) {
		calls++
		return emptyFindEnvelope(), nil
	}
	core := NewCore(backend)

	// 1 秒预算 + 2ms 间隔：会查很多轮，但必须在预算内返回，不能拖成宿主超时。
	started := time.Now()
	response := core.Call(context.Background(), FindOnScreen, json.RawMessage(
		`{"session_id":"s1","text":"永远不存在的按钮","wait_until_found":true,"timeout_seconds":1}`))
	elapsed := time.Since(started)
	if !response.OK {
		t.Fatalf("a wait timeout is a normal result, not an error: %#v", response)
	}
	data, _ := response.Data.(map[string]any)
	if data["timed_out"] != true {
		t.Fatalf("timed_out should be true: %#v", data)
	}
	if data["count"] != 0 {
		t.Fatalf("no match expected: %#v", data)
	}
	if elapsed > 3*time.Second {
		t.Fatalf("wait budget was not respected: %v", elapsed)
	}
	if calls < 2 {
		t.Fatalf("expected repeated searches, got %d", calls)
	}
}

// 没有 wait_until_found 时 timeout_seconds 不生效：一次查完就返回，不睡。
func TestFindOnScreenWithoutWaitDoesNotSleep(t *testing.T) {
	old := findPollInterval
	findPollInterval = 200 * time.Millisecond
	defer func() { findPollInterval = old }()

	backend := nativeBackendWithImage(t)
	calls := 0
	backend.nativeImpl = func(_ string, _ string, _ json.RawMessage) (json.RawMessage, error) {
		calls++
		return emptyFindEnvelope(), nil
	}
	core := NewCore(backend)

	started := time.Now()
	response := core.Call(context.Background(), FindOnScreen, json.RawMessage(
		`{"session_id":"s1","text":"不存在","timeout_seconds":5}`))
	if !response.OK {
		t.Fatalf("find failed: %#v", response)
	}
	if calls != 1 {
		t.Fatalf("without wait_until_found the device must be queried once, got %d", calls)
	}
	if elapsed := time.Since(started); elapsed > 150*time.Millisecond {
		t.Fatalf("without wait_until_found there must be no sleep, took %v", elapsed)
	}
	data, _ := response.Data.(map[string]any)
	if data["timed_out"] != false || data["attempts"] != 1 {
		t.Fatalf("unexpected result: %#v", data)
	}
}

// 空结果的信封：与设备真实返回同形，只是 matches 为空。
func emptyFindEnvelope() json.RawMessage {
	return json.RawMessage(`{"captured":true,"screenshot_id":"aabbccddee11","layout_version":"0123456789abcdef",` +
		`"width":2560,"height":1600,"screen_left":0,"screen_top":0,"screen_width":2560,"screen_height":1600,` +
		`"matches":[],"truncated":false,"visited_nodes":12,"elapsed_ms":9,"source":"uia"}`)
}
