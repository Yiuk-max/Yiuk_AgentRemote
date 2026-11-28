package agenttools

import (
	"context"
	"encoding/json"
	"testing"
)

// click_target：客户端内 find -> pick -> click 一笔事务的 MCP 侧行为。

func clickTargetEnvelope() json.RawMessage {
	return json.RawMessage(`{"captured":true,"screenshot_id":"aabbccddee11","layout_version":"0123456789abcdef",` +
		`"width":2560,"height":1600,"screen_left":0,"screen_top":0,"screen_width":2560,"screen_height":1600,` +
		`"matches":[{"bbox":[100,200,180,240],"center":[140,220],"score":1.0,"source":"uia","text":"收藏",` +
		`"control_type":"Button"},{"bbox":[300,200,380,240],"center":[340,220],"score":0.9,"source":"uia",` +
		`"text":"收藏夹","control_type":"Hyperlink"}],` +
		`"picked":{"index":1,"bbox":[100,200,180,240],"center":[140,220],"score":1.0,"source":"uia",` +
		`"text":"收藏","control_type":"Button"},` +
		`"candidate_count":2,"x":140,"y":220,"screen_x":140,"screen_y":220,"button":"left","double":false,` +
		`"foreground_title":"记事本","diff_ratio":0.5,"changed_ratio":0.2,"detect_change":true,` +
		`"elapsed_ms":320,"source":"uia"}`)
}

func TestClickTargetForwardsPickAndReportsPicked(t *testing.T) {
	backend := nativeBackendWithImage(t)
	var forwarded map[string]any
	backend.nativeImpl = func(_ string, operation string, arguments json.RawMessage) (json.RawMessage, error) {
		if operation != nativeClickTargetOperation {
			t.Fatalf("unexpected native operation %q", operation)
		}
		if err := json.Unmarshal(arguments, &forwarded); err != nil {
			t.Fatalf("arguments are not JSON: %v", err)
		}
		return clickTargetEnvelope(), nil
	}
	core := NewCore(backend)

	response := core.Call(context.Background(), ClickTarget, json.RawMessage(
		`{"session_id":"s1","text":"  收藏  ","match":"exact","control_type":"Button","region":[0,0,1200,300],`+
			`"limit":3,"pick":"best","button":"right","double":true}`))
	if !response.OK {
		t.Fatalf("click_target failed: %#v", response)
	}
	if backend.commands != 0 {
		t.Fatalf("click_target ran %d scripts, want 0", backend.commands)
	}
	if forwarded["text"] != "收藏" || forwarded["match"] != "exact" || forwarded["control_type"] != "Button" {
		t.Fatalf("unexpected forwarded arguments %#v", forwarded)
	}
	if forwarded["pick"] != "best" || forwarded["button"] != "right" || forwarded["double"] != true {
		t.Fatalf("pick/button/double were not forwarded: %#v", forwarded)
	}
	// 没给 detect_change / settle_ms 时不要伪造：由客户端取默认值（true / 250）。
	if _, sent := forwarded["detect_change"]; sent {
		t.Fatalf("detect_change must not be invented when omitted: %#v", forwarded)
	}
	if _, sent := forwarded["settle_ms"]; sent {
		t.Fatalf("settle_ms must not be invented when omitted: %#v", forwarded)
	}

	data, _ := response.Data.(map[string]any)
	if data["clicked"] != true || data["candidate_count"] != 2 {
		t.Fatalf("unexpected result %#v", data)
	}
	if data["x"] != 140 || data["y"] != 220 || data["screen_x"] != 140 || data["screen_y"] != 220 {
		t.Fatalf("click coordinates missing: %#v", data)
	}
	picked, _ := data["picked"].(map[string]any)
	if picked["index"] != 1 || picked["text"] != "收藏" {
		t.Fatalf("which candidate was clicked must be reported: %#v", picked)
	}
	// 变化判据与 click 一致：changed_ratio 0.2 明显超过强变化占比阈值。
	if data["changed"] != true || data["diff_ratio"] != 0.5 {
		t.Fatalf("detect_change fields missing: %#v", data)
	}
	if data["screenshot_id"] != "aabbccddee11" || data["width"] != 2560 {
		t.Fatalf("coordinate base missing from the result: %#v", data)
	}

	// 这一帧就是点击的坐标基准：Go 侧必须同步登记，后续 click 带同一个 id 不算过期。
	if _, _, known := core.desktop.find("s1", "aabbccddee11"); !known {
		t.Fatalf("click_target did not register the screenshot it clicked")
	}
}

func TestClickTargetForwardsExplicitDetectChangeZeroSettle(t *testing.T) {
	backend := nativeBackendWithImage(t)
	var forwarded map[string]any
	backend.nativeImpl = func(_ string, _ string, arguments json.RawMessage) (json.RawMessage, error) {
		_ = json.Unmarshal(arguments, &forwarded)
		return clickTargetEnvelope(), nil
	}
	core := NewCore(backend)

	// 显式 0 / false 必须原样送到设备：省略与显式给了 0 是两件事。
	response := core.Call(context.Background(), ClickTarget, json.RawMessage(
		`{"session_id":"s1","text":"收藏","detect_change":false,"settle_ms":0,"pick":"index:2"}`))
	if !response.OK {
		t.Fatalf("click_target failed: %#v", response)
	}
	if forwarded["detect_change"] != false {
		t.Fatalf("detect_change=false was dropped: %#v", forwarded)
	}
	if value, ok := forwarded["settle_ms"]; !ok || value != float64(0) {
		t.Fatalf("settle_ms=0 was treated as omitted: %#v", forwarded)
	}
	if forwarded["pick"] != "index:2" {
		t.Fatalf("pick=index:N was not forwarded: %#v", forwarded)
	}
}

func TestClickTargetKeepsDeviceFindingsOnFailure(t *testing.T) {
	backend := nativeBackendWithImage(t)
	backend.nativeImpl = func(_ string, _ string, _ json.RawMessage) (json.RawMessage, error) {
		// 设备侧判歧义：一个 down 都没发，但必须把候选带回来。
		return nil, &Error{
			Code:    "AMBIGUOUS_TARGET",
			Message: "命中 2 个候选，第一与第二名得分差小于 0.03，不点击",
			Hint:    "use pick=best or pick=index:N",
			Data: map[string]any{
				"candidate_count":  2,
				"top_score":        0.90,
				"competitor_score": 0.89,
			},
		}
	}
	core := NewCore(backend)

	response := core.Call(context.Background(), ClickTarget, json.RawMessage(`{"session_id":"s1","text":"收藏"}`))
	if response.OK || response.Code != "AMBIGUOUS_TARGET" {
		t.Fatalf("ambiguity must fail the call: %#v", response)
	}
	data, _ := response.Data.(map[string]any)
	if data["candidate_count"] != 2 {
		t.Fatalf("候选现场没有传给 Agent: %#v", response.Data)
	}
	// 失败也不能登记坐标基准：这一帧从来没有被点过，但它是设备的真实画面，
	// 登记与否不影响正确性；这里只确认失败路径不产生 clicked=true 之类的谎话。
	if _, has := data["clicked"]; has {
		t.Fatalf("failure must not claim a click: %#v", data)
	}
}

func TestClickTargetValidatesArguments(t *testing.T) {
	core := NewCore(nativeBackendWithImage(t))

	// text 与 image_path 同时给：设备会拒绝，但在 MCP 边界就挡住更便宜。
	both := core.Call(context.Background(), ClickTarget, json.RawMessage(
		`{"session_id":"s1","text":"收藏","image_path":"C:\\tpl\\a.png"}`))
	if both.OK || both.Code != "INVALID_ARGUMENT" {
		t.Fatalf("text+image must be rejected: %#v", both)
	}
	// 什么都没给：schema 的 anyOf 挡住。
	empty := core.Call(context.Background(), ClickTarget, json.RawMessage(`{"session_id":"s1"}`))
	if empty.OK || empty.Code != "INVALID_ARGUMENT" {
		t.Fatalf("missing target must be rejected: %#v", empty)
	}
	// region 必须是有正面积的四个整数。
	badRegion := core.Call(context.Background(), ClickTarget, json.RawMessage(
		`{"session_id":"s1","text":"收藏","region":[100,100,100,200]}`))
	if badRegion.OK || badRegion.Code != "INVALID_ARGUMENT" {
		t.Fatalf("degenerate region must be rejected: %#v", badRegion)
	}
	// pick 只认唯一的几种写法（不做"差不多"的模糊接受）。
	for _, pick := range []string{`"nearest"`, `"index:0"`, `"index:"`, `"unique "`} {
		response := core.Call(context.Background(), ClickTarget, json.RawMessage(
			`{"session_id":"s1","text":"收藏","pick":`+pick+`}`))
		if response.OK || response.Code != "INVALID_ARGUMENT" {
			t.Fatalf("pick=%s must be rejected: %#v", pick, response)
		}
	}
}
