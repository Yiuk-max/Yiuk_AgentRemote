package agenttools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// nativeBackend 同时实现 Backend 与 NativeInvoker：
// 用来验证 take_screenshot 的原生优先、脚本回退与错误处理。
type nativeBackend struct {
	nativeCalls  int
	commands     int
	downloads    int
	releases     int
	releasedPath string
	releaseErr   error
	imagePath    string
	nativeImpl   func(sessionID, operation string, arguments json.RawMessage) (json.RawMessage, error)
}

func (b *nativeBackend) InvokeNative(_ context.Context, sessionID, operation string, arguments json.RawMessage,
	timeout int) (json.RawMessage, error) {
	b.nativeCalls++
	// 临时截图回收在每条原生抓屏路径之后都会发生：统一在这里拦截，
	// 各用例只需要关心自己的业务操作。
	if operation == nativeScreenshotReleaseOperation {
		var parsed struct {
			Path string `json:"path"`
		}
		_ = json.Unmarshal(arguments, &parsed)
		b.releasedPath = parsed.Path
		if b.releaseErr != nil {
			return nil, b.releaseErr
		}
		b.releases++
		return json.RawMessage(`{"released":true}`), nil
	}
	return b.nativeImpl(sessionID, operation, arguments)
}

func (b *nativeBackend) Invoke(_ context.Context, req Request) (any, error) {
	switch req.Name {
	case ExecuteCommand:
		b.commands++
		return map[string]any{"request_id": "r1", "status": "queued"}, nil
	case GetCommandResult:
		// 脚本路径：抓屏脚本回 JSON 几何，点击脚本回坐标。
		return map[string]any{"status": "succeeded", "chunks": []map[string]any{
			{"stream": "stdout", "data": `{"path":"C:\\temp\\shot.png","left":0,"top":0,"screen_width":2560,"screen_height":1600,"width":1568,"height":980,"x":0,"y":0}` + "\r\n"},
		}}, nil
	case DownloadFile:
		b.downloads++
		// 上一张截图读完就被删掉了；模拟设备每次都提供一个可读的新文件。
		if _, err := os.Stat(b.imagePath); err != nil {
			_ = os.WriteFile(b.imagePath, []byte("png-bytes"), 0o600)
		}
		return map[string]any{"local_path": b.imagePath}, nil
	}
	return nil, nil
}

func nativeBackendWithImage(t *testing.T) *nativeBackend {
	t.Helper()
	imagePath := filepath.Join(t.TempDir(), "shot.png")
	if err := os.WriteFile(imagePath, []byte("png-bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	return &nativeBackend{imagePath: imagePath}
}

func TestTakeScreenshotPrefersNativeChannel(t *testing.T) {
	backend := nativeBackendWithImage(t)
	backend.nativeImpl = func(sessionID, operation string, arguments json.RawMessage) (json.RawMessage, error) {
		if operation == nativeClickOperation {
			// 后续的 click 也会走原生通道：这里只回一个最小信封。
			return json.RawMessage(`{"x":100,"y":200,"screen_x":100,"screen_y":200,"foreground_title":"Notepad"}`), nil
		}
		if operation != nativeScreenshotOperation {
			t.Fatalf("got operation %q, want %q", operation, nativeScreenshotOperation)
		}
		var parsed struct {
			MaxWidth int    `json:"max_width"`
			Format   string `json:"format"`
			Save     bool   `json:"save"`
		}
		if err := json.Unmarshal(arguments, &parsed); err != nil {
			t.Fatalf("native arguments are not JSON: %v", err)
		}
		if parsed.MaxWidth != defaultScreenshotMaxWidth || parsed.Format != "png" || !parsed.Save {
			t.Fatalf("unexpected native arguments %#v", parsed)
		}
		return json.RawMessage(`{"screenshot_id":"0123456789ab","path":"C:\\temp\\shot.png","format":"png",` +
			`"width":1568,"height":980,"screen_left":-1920,"screen_top":0,"screen_width":3840,"screen_height":1600}`), nil
	}
	core := NewCore(backend)

	response := core.Call(context.Background(), TakeScreenshot, json.RawMessage(`{"session_id":"s1"}`))
	if !response.OK {
		t.Fatalf("native screenshot failed: %#v", response)
	}
	if len(response.Images) != 1 || string(response.Images[0].Data) != "png-bytes" {
		t.Fatalf("unexpected images %#v", response.Images)
	}
	// 原生路径不能再去跑 PowerShell。
	if backend.commands != 0 {
		t.Fatalf("native screenshot ran %d scripts, want 0", backend.commands)
	}
	if backend.downloads != 1 {
		t.Fatalf("expected exactly one download, got %d", backend.downloads)
	}

	data, ok := response.Data.(map[string]any)
	if !ok {
		t.Fatalf("unexpected data %#v", response.Data)
	}
	if data["screenshot_id"] != "0123456789ab" {
		t.Fatalf("device screenshot id was not preserved: %#v", data["screenshot_id"])
	}
	if data["capture_source"] != "native" {
		t.Fatalf("capture_source = %#v, want native", data["capture_source"])
	}
	if data["screen_left"] != -1920 || data["screen_width"] != 3840 {
		t.Fatalf("negative virtual-screen origin was lost: %#v", data)
	}
	// 图片取走后设备上的临时文件必须被回收，否则长期运行会一直堆在设备磁盘上。
	if backend.releases != 1 || backend.releasedPath != `C:\temp\shot.png` {
		t.Fatalf("temp screenshot was not released: releases=%d path=%q", backend.releases, backend.releasedPath)
	}

	// 同一个 id 必须立刻能用于坐标操作：原生抓屏登记进 Go 侧快照。
	clicked := core.Call(context.Background(), Click, json.RawMessage(
		`{"session_id":"s1","x":100,"y":200,"screenshot_id":"0123456789ab"}`))
	if !clicked.OK {
		t.Fatalf("click with the native screenshot id failed: %#v", clicked)
	}
	// 再抓一张（换 id）后，旧 id 必须过期。
	backend.nativeImpl = func(string, string, json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage(`{"screenshot_id":"ffffffffffff","path":"C:\\temp\\shot.png","format":"png",` +
			`"width":1568,"height":980,"screen_left":0,"screen_top":0,"screen_width":2560,"screen_height":1600}`), nil
	}
	if second := core.Call(context.Background(), TakeScreenshot, json.RawMessage(`{"session_id":"s1"}`)); !second.OK {
		t.Fatalf("second native screenshot failed: %#v", second)
	}
	stale := core.Call(context.Background(), Click, json.RawMessage(
		`{"session_id":"s1","x":100,"y":200,"screenshot_id":"0123456789ab"}`))
	if stale.OK || stale.Code != "STALE_SCREENSHOT" {
		t.Fatalf("got %#v, want STALE_SCREENSHOT for the superseded id", stale)
	}
}

func TestTakeScreenshotFallsBackWhenNativeUnsupported(t *testing.T) {
	backend := nativeBackendWithImage(t)
	backend.nativeImpl = func(string, string, json.RawMessage) (json.RawMessage, error) {
		return nil, &Error{Code: "UNSUPPORTED_CLIENT", Message: "device does not advertise screen.capture"}
	}
	core := NewCore(backend)

	response := core.Call(context.Background(), TakeScreenshot, json.RawMessage(`{"session_id":"s1"}`))
	if !response.OK {
		t.Fatalf("script fallback failed: %#v", response)
	}
	if backend.nativeCalls != 1 {
		t.Fatalf("expected one native attempt, got %d", backend.nativeCalls)
	}
	if backend.commands == 0 {
		t.Fatal("fallback did not run the screenshot script")
	}
	data, _ := response.Data.(map[string]any)
	if data["capture_source"] != "script" {
		t.Fatalf("capture_source = %#v, want script", data["capture_source"])
	}
	if id, _ := data["screenshot_id"].(string); !validScreenshotID(id) {
		t.Fatalf("script path id %q is not a 12-char hex id", id)
	}
}

func TestTakeScreenshotSucceedsWhenReleaseFails(t *testing.T) {
	// 回收失败只能影响磁盘占用：截图本身已经读进内存，工具必须成功。
	backend := nativeBackendWithImage(t)
	backend.releaseErr = &Error{Code: "NATIVE_OPERATION_FAILED", Message: "device busy"}
	backend.nativeImpl = func(string, string, json.RawMessage) (json.RawMessage, error) {
		return captureEnvelope(), nil
	}
	core := NewCore(backend)
	response := core.Call(context.Background(), TakeScreenshot, json.RawMessage(`{"session_id":"s1"}`))
	if !response.OK {
		t.Fatalf("release failure broke the screenshot: %#v", response)
	}
	if backend.releases != 0 {
		t.Fatalf("expected the release to fail, got %d successful releases", backend.releases)
	}
}

func TestTakeScreenshotRejectsMalformedNativeResult(t *testing.T) {
	cases := map[string]string{
		"invalid id":       `{"screenshot_id":"not-hex","path":"C:\\t\\s.png","format":"png","width":10,"height":10,"screen_width":10,"screen_height":10}`,
		"missing path":     `{"screenshot_id":"0123456789ab","format":"png","width":10,"height":10,"screen_width":10,"screen_height":10}`,
		"missing geometry": `{"screenshot_id":"0123456789ab","path":"C:\\t\\s.png","format":"png","width":0,"height":0,"screen_width":0,"screen_height":0}`,
	}
	for name, payload := range cases {
		t.Run(name, func(t *testing.T) {
			backend := nativeBackendWithImage(t)
			backend.nativeImpl = func(string, string, json.RawMessage) (json.RawMessage, error) {
				return json.RawMessage(payload), nil
			}
			core := NewCore(backend)
			response := core.Call(context.Background(), TakeScreenshot, json.RawMessage(`{"session_id":"s1"}`))
			if response.OK || response.Code != "SCREENSHOT_FAILED" {
				t.Fatalf("got %#v, want SCREENSHOT_FAILED", response)
			}
			// 设备已经执行过抓屏：不能偷偷再跑一遍脚本。
			if backend.commands != 0 || backend.downloads != 0 {
				t.Fatalf("malformed native result triggered fallback (commands=%d downloads=%d)",
					backend.commands, backend.downloads)
			}
		})
	}
}

func TestValidScreenshotID(t *testing.T) {
	for _, id := range []string{"0123456789ab", "ffffffffffff", "000000000000"} {
		if !validScreenshotID(id) {
			t.Fatalf("%q should be a valid screenshot id", id)
		}
	}
	for _, id := range []string{"", "0123456789a", "0123456789abc", "0123456789AB", "zzzzzzzzzzzz", "0123456789ag"} {
		if validScreenshotID(id) {
			t.Fatalf("%q should be rejected", id)
		}
	}
}
