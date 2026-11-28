package agenttools

import (
	"context"
	"encoding/json"
)

// drag：在设备上按住左键从 from 拖到 to。
//
// 与 click 一样走原生通道：脚本路径做不了"按住并连续移动"，
// 旧客户端没有 input.drag 能力时明确返回 UNSUPPORTED_CLIENT，不回退。
// 客户端保证任何路径（成功/取消/系统拒绝）都会抬起按键，并在结果里回 released=true。

const (
	nativeDragOperation = "input.drag"
	// 一次拖动最长 5 秒 + settle，留足余量但仍受平台 25 秒上限约束。
	nativeDragTimeoutSeconds = 25
)

func (c *Core) dragOnScreen(ctx context.Context, raw json.RawMessage) (any, error) {
	var input struct {
		SessionID    string `json:"session_id"`
		From         []int  `json:"from"`
		To           []int  `json:"to"`
		DurationMS   int    `json:"duration_ms"`
		HoldMS       int    `json:"hold_ms"`
		ScreenshotID string `json:"screenshot_id"`
	}
	if err := json.Unmarshal(raw, &input); err != nil {
		return nil, &Error{Code: "INVALID_ARGUMENT", Message: "invalid arguments"}
	}
	if len(input.From) != 2 || len(input.To) != 2 {
		return nil, &Error{Code: "INVALID_ARGUMENT", Message: "from and to must both be [x,y]"}
	}
	if input.From[0] < 0 || input.From[1] < 0 || input.To[0] < 0 || input.To[1] < 0 {
		return nil, &Error{Code: "INVALID_ARGUMENT", Message: "from/to must not be negative"}
	}
	if input.From[0] == input.To[0] && input.From[1] == input.To[1] {
		return nil, &Error{Code: "INVALID_ARGUMENT", Message: "from and to are the same point"}
	}

	// 坐标基准：与 click 相同的校验，避免拿过期截图去拖。
	if err := c.requireFreshScreenshot(input.SessionID, input.ScreenshotID); err != nil {
		return nil, err
	}
	latest, ok := c.desktop.latest(input.SessionID)
	if !ok {
		return nil, &Error{Code: "SCREENSHOT_REQUIRED",
			Message: "call take_screenshot for this session first; coordinates are pixels in the latest screenshot"}
	}
	// Go 侧先做一次范围检查，把"越界"在发起前就挡住（客户端还会再校验一次）。
	if _, _, _, err := c.screenPoint(input.SessionID, input.From[0], input.From[1]); err != nil {
		return nil, err
	}
	if _, _, _, err := c.screenPoint(input.SessionID, input.To[0], input.To[1]); err != nil {
		return nil, err
	}

	arguments := map[string]any{
		"from":          input.From,
		"to":            input.To,
		"duration_ms":   input.DurationMS,
		"hold_ms":       input.HoldMS,
		"screenshot_id": latest.ID,
	}
	rawResult, err := c.invokeNative(ctx, input.SessionID, nativeDragOperation, arguments, nativeDragTimeoutSeconds)
	if err != nil {
		if isUnsupportedNative(err) {
			return nil, &Error{
				Code:    "UNSUPPORTED_CLIENT",
				Message: "device does not support input.drag",
				Hint:    "该设备的客户端还不支持拖动；请设备所有者升级 Qt 客户端（其他工具不受影响）",
			}
		}
		return nil, err
	}

	var result struct {
		From       []int `json:"from"`
		To         []int `json:"to"`
		ScreenFrom []int `json:"screen_from"`
		ScreenTo   []int `json:"screen_to"`
		DurationMS int   `json:"duration_ms"`
		HoldMS     int   `json:"hold_ms"`
		Released   bool  `json:"released"`
	}
	if err := json.Unmarshal(rawResult, &result); err != nil {
		return nil, &Error{Code: "NATIVE_RESULT_INVALID",
			Message: "device returned a malformed drag result",
			Hint:    "客户端与 MCP 的版本可能不匹配；升级到同一发布批次后重试"}
	}
	if !result.Released {
		// 没确认抬起就不能说成功：鼠标键可能还按着，后续操作会全部失准。
		return nil, &Error{Code: "DRAG_NOT_RELEASED",
			Message: "device did not confirm the button release",
			Hint:    "先 take_screenshot 核对状态；必要时用 click 在同一位置补一次抬起"}
	}

	return map[string]any{
		"session_id":    input.SessionID,
		"from":          result.From,
		"to":            result.To,
		"screen_from":   result.ScreenFrom,
		"screen_to":     result.ScreenTo,
		"duration_ms":   result.DurationMS,
		"hold_ms":       result.HoldMS,
		"screenshot_id": latest.ID,
		"released":      true,
		"input_source":  "native",
	}, nil
}
