package agenttools

import (
	"context"
	"encoding/json"
	"testing"
)

// save_template：从最近一帧裁模板，返回 template_id 供 find/click_target 复用。
func TestSaveTemplateForwardsRegionAndReturnsID(t *testing.T) {
	backend := nativeBackendWithImage(t)
	var forwarded map[string]any
	backend.nativeImpl = func(_ string, operation string, arguments json.RawMessage) (json.RawMessage, error) {
		if operation != nativeSaveTemplateOperation {
			t.Fatalf("unexpected native operation %q", operation)
		}
		_ = json.Unmarshal(arguments, &forwarded)
		return json.RawMessage(`{"template_id":"aabbccddeeff","width":160,"height":80,"size_bytes":2048,` +
			`"region":[40,40,200,120]}`), nil
	}
	core := NewCore(backend)

	response := core.Call(context.Background(), SaveTemplate, json.RawMessage(
		`{"session_id":"s1","region":[40,40,200,120],"screenshot_id":"aabbccddee11"}`))
	if !response.OK {
		t.Fatalf("save_template failed: %#v", response)
	}
	if backend.commands != 0 {
		t.Fatalf("save_template ran %d scripts, want 0", backend.commands)
	}
	if region, ok := forwarded["region"].([]any); !ok || len(region) != 4 {
		t.Fatalf("region was not forwarded: %#v", forwarded["region"])
	}
	if forwarded["screenshot_id"] != "aabbccddee11" {
		t.Fatalf("screenshot_id was not forwarded: %#v", forwarded)
	}
	data, _ := response.Data.(map[string]any)
	if data["template_id"] != "aabbccddeeff" || data["width"] != 160 || data["height"] != 80 {
		t.Fatalf("unexpected result %#v", data)
	}
	if hint, _ := data["hint"].(string); hint == "" {
		t.Fatalf("result should tell the agent how to use the template: %#v", data)
	}

	// 没有 screenshot_id 时不要凭空塞一个空字段：客户端会按"最近一张"处理。
	forwarded = nil
	if response := core.Call(context.Background(), SaveTemplate,
		json.RawMessage(`{"session_id":"s1","region":[0,0,10,10]}`)); !response.OK {
		t.Fatalf("save_template without screenshot_id failed: %#v", response)
	}
	if _, sent := forwarded["screenshot_id"]; sent {
		t.Fatalf("screenshot_id must not be invented: %#v", forwarded)
	}
}

func TestSaveTemplateValidatesArgumentsAndResult(t *testing.T) {
	core := NewCore(nativeBackendWithImage(t))

	// schema：region 必须是 4 个非负整数。
	for _, bad := range []string{
		`{"session_id":"s1","region":[0,0,10]}`,
		`{"session_id":"s1","region":[-1,0,10,10]}`,
	} {
		response := core.Call(context.Background(), SaveTemplate, json.RawMessage(bad))
		if response.OK || response.Code != "INVALID_ARGUMENT" {
			t.Fatalf("%s must be rejected: %#v", bad, response)
		}
	}
	// 空面积：schema 表达不了，handler 挡。
	empty := core.Call(context.Background(), SaveTemplate, json.RawMessage(
		`{"session_id":"s1","region":[10,10,10,40]}`))
	if empty.OK || empty.Code != "INVALID_ARGUMENT" {
		t.Fatalf("a degenerate region must be rejected: %#v", empty)
	}

	// 设备返回的 template_id 格式不对时不能往外交（它会被 find 引用）。
	backend := nativeBackendWithImage(t)
	backend.nativeImpl = func(_ string, _ string, _ json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage(`{"template_id":"../../etc","width":1,"height":1,"size_bytes":1}`), nil
	}
	broken := NewCore(backend).Call(context.Background(), SaveTemplate,
		json.RawMessage(`{"session_id":"s1","region":[0,0,10,10]}`))
	if broken.OK || broken.Code != "NATIVE_RESULT_INVALID" {
		t.Fatalf("a malformed template id must be rejected: %#v", broken)
	}
}

// find / click_target 的第四种来源：template_id，且与其它来源互斥。
func TestLocateToolsForwardTemplateID(t *testing.T) {
	backend := nativeBackendWithImage(t)
	var findArgs, clickArgs map[string]any
	backend.nativeImpl = func(_ string, operation string, arguments json.RawMessage) (json.RawMessage, error) {
		switch operation {
		case nativeFindOperation:
			_ = json.Unmarshal(arguments, &findArgs)
			return emptyFindEnvelope(), nil
		case nativeClickTargetOperation:
			_ = json.Unmarshal(arguments, &clickArgs)
			return clickTargetEnvelope(), nil
		}
		t.Fatalf("unexpected native operation %q", operation)
		return nil, nil
	}
	core := NewCore(backend)

	if response := core.Call(context.Background(), FindOnScreen, json.RawMessage(
		`{"session_id":"s1","template_id":"aabbccddeeff","threshold":0.93}`)); !response.OK {
		t.Fatalf("find with template_id failed: %#v", response)
	}
	if findArgs["template_id"] != "aabbccddeeff" || findArgs["threshold"] != 0.93 {
		t.Fatalf("template_id was not forwarded: %#v", findArgs)
	}
	if response := core.Call(context.Background(), ClickTarget, json.RawMessage(
		`{"session_id":"s1","template_id":"aabbccddeeff","pick":"best"}`)); !response.OK {
		t.Fatalf("click_target with template_id failed: %#v", response)
	}
	if clickArgs["template_id"] != "aabbccddeeff" {
		t.Fatalf("template_id was not forwarded to click_target: %#v", clickArgs)
	}

	// 两个来源同时给：明确拒绝，不猜。
	for _, tool := range []string{FindOnScreen, ClickTarget} {
		response := core.Call(context.Background(), tool, json.RawMessage(
			`{"session_id":"s1","text":"收藏","template_id":"aabbccddeeff"}`))
		if response.OK || response.Code != "INVALID_ARGUMENT" {
			t.Fatalf("%s: text + template_id must be rejected: %#v", tool, response)
		}
	}
}
