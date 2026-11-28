package localbridge

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"agentremote/internal/agenttools"
)

const testToken = "0123456789abcdef0123456789abcdef"

// 本机桥的原生通道：loopback + token、请求体形状、信封成功/失败都要与平台链路同形。
func TestInvokeNativePostsTheEnvelope(t *testing.T) {
	var path, authorization, contentType string
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		authorization = r.Header.Get("Authorization")
		contentType = r.Header.Get("Content-Type")
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Errorf("request body is not JSON: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"ok":true,"code":"OK","data":{"clicked":true,"x":10,"y":20}}`)
	}))
	defer server.Close()

	client := New(Config{URL: server.URL, Token: testToken})
	data, err := client.InvokeNative(context.Background(), "session-1", "vision.click_target",
		json.RawMessage(`{"text":"收藏","pick":"best"}`), 15)
	if err != nil {
		t.Fatalf("InvokeNative: %v", err)
	}
	if path != "/native" {
		t.Fatalf("posted to %q, want /native", path)
	}
	if authorization != "Bearer "+testToken {
		t.Fatalf("authorization header = %q", authorization)
	}
	if contentType != "application/json" {
		t.Fatalf("content-type = %q", contentType)
	}
	if body["session_id"] != "session-1" || body["operation"] != "vision.click_target" {
		t.Fatalf("unexpected request body: %#v", body)
	}
	if body["timeout_seconds"] != float64(15) {
		t.Fatalf("timeout was not forwarded: %#v", body["timeout_seconds"])
	}
	if requestID, _ := body["request_id"].(string); len(requestID) != 32 {
		t.Fatalf("request_id should be 32 hex chars, got %#v", body["request_id"])
	}
	if arguments, ok := body["arguments"].(map[string]any); !ok || arguments["text"] != "收藏" {
		t.Fatalf("arguments were not nested as an object: %#v", body["arguments"])
	}

	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil || decoded["clicked"] != true {
		t.Fatalf("unexpected data %q (%v)", string(data), err)
	}
}

func TestInvokeNativeCarriesFailureFindings(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		io.WriteString(w, `{"ok":false,"code":"AMBIGUOUS_TARGET","message":"two candidates",`+
			`"hint":"use pick=best","data":{"candidate_count":2}}`)
	}))
	defer server.Close()

	client := New(Config{URL: server.URL, Token: testToken})
	_, err := client.InvokeNative(context.Background(), "session-1", "vision.click_target",
		json.RawMessage(`{"text":"收藏"}`), 0)
	if err == nil {
		t.Fatal("a failed envelope must be an error")
	}
	toolErr, ok := err.(*agenttools.Error)
	if !ok {
		t.Fatalf("unexpected error type %T", err)
	}
	if toolErr.Code != "AMBIGUOUS_TARGET" || toolErr.Hint == "" {
		t.Fatalf("unexpected error: %#v", toolErr)
	}
	data, ok := toolErr.Data.(map[string]any)
	if !ok || data["candidate_count"] != float64(2) {
		t.Fatalf("failure findings were dropped: %#v", toolErr.Data)
	}
}

// 本地桥现在实现了 NativeInvoker：组合工具（find_on_screen / click_target）不再退化成
// UNSUPPORTED_CLIENT，而是真的下发原生操作。
func TestLocalBridgeBacksNativeTools(t *testing.T) {
	var operations []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body struct {
			Operation string `json:"operation"`
		}
		_ = json.Unmarshal(raw, &body)
		operations = append(operations, body.Operation)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"ok":true,"code":"OK","data":{"captured":true,"screenshot_id":"aabbccddee11",`+
			`"width":2560,"height":1600,"screen_left":0,"screen_top":0,"screen_width":2560,"screen_height":1600,`+
			`"matches":[],"truncated":false,"visited_nodes":3,"elapsed_ms":5,"source":"uia"}}`)
	}))
	defer server.Close()

	core := agenttools.NewCore(New(Config{URL: server.URL, Token: testToken}))
	response := core.Call(context.Background(), agenttools.FindOnScreen,
		json.RawMessage(`{"session_id":"session-1","text":"收藏"}`))
	if !response.OK {
		t.Fatalf("find_on_screen over the local bridge failed: %#v", response)
	}
	if len(operations) != 1 || !strings.HasPrefix(operations[0], "vision.") {
		t.Fatalf("unexpected native operations: %#v", operations)
	}
}
