package agenttools

import (
	"encoding/json"
)

// 原生输入操作的名称与超时。与 screen.capture 一样，这些是协议层的内部操作，
// 不直接暴露给 Agent；工具层（click / move_mouse）负责参数与回退策略。
const (
	nativeClickOperation = "input.click"
	nativeMoveOperation  = "input.move"

	// 一次点击包含"移动 → 按下 → 抬起 → settle"，给足余量但不超过平台 25 秒硬限制。
	nativeInputTimeoutSeconds = 20
)

// nativeInputResult 是 input.click / input.move 返回的业务数据。
type nativeInputResult struct {
	X            int     `json:"x"`
	Y            int     `json:"y"`
	ScreenX      int     `json:"screen_x"`
	ScreenY      int     `json:"screen_y"`
	Button       string  `json:"button"`
	Double       bool    `json:"double"`
	DurationMS   int     `json:"duration_ms"`
	SettleMS     int     `json:"settle_ms"`
	Foreground   string  `json:"foreground_title"`
	DiffRatio    float64 `json:"diff_ratio"`
	ChangedRatio float64 `json:"changed_ratio"`
}

// nativeClickResult 把原生点击的业务信封转换成与脚本路径一致的响应字段。
// 坐标用 Go 侧校验过的值（screenX/screenY），不信任设备回显。
func (c *Core) nativeClickResult(input clickInput, screenshotID string, screenX, screenY int,
	raw json.RawMessage) (any, error) {
	var clicked nativeInputResult
	if err := json.Unmarshal(raw, &clicked); err != nil {
		return nil, &Error{Code: "DESKTOP_COMMAND_FAILED", Message: "device returned an unexpected native click result"}
	}
	result := map[string]any{
		"session_id":       input.SessionID,
		"x":                input.X,
		"y":                input.Y,
		"screen_x":         screenX,
		"screen_y":         screenY,
		"button":           input.Button,
		"double":           input.Double,
		"screenshot_id":    screenshotID,
		"foreground_title": clicked.Foreground,
		"input_source":     "native",
	}
	if input.DetectChange {
		// 判据与脚本路径、客户端采样保持一致：强变化像素占比，或整体平均差。
		result["changed"] = clicked.ChangedRatio >= changedPixelRatioThreshold || clicked.DiffRatio >= changedMeanThreshold
		result["diff_ratio"] = clicked.DiffRatio
		result["changed_ratio"] = clicked.ChangedRatio
		result["settle_ms"] = input.SettleMS
	}
	return result, nil
}

// nativeMoveResult 同理：移动没有变化检测，只回报坐标、时长与前台窗口。
func (c *Core) nativeMoveResult(input moveMouseInput, screenshotID string, screenX, screenY int,
	raw json.RawMessage) (any, error) {
	var moved nativeInputResult
	if err := json.Unmarshal(raw, &moved); err != nil {
		return nil, &Error{Code: "DESKTOP_COMMAND_FAILED", Message: "device returned an unexpected native move result"}
	}
	return map[string]any{
		"session_id":       input.SessionID,
		"x":                input.X,
		"y":                input.Y,
		"screen_x":         screenX,
		"screen_y":         screenY,
		"duration_ms":      moved.DurationMS,
		"settle_ms":        input.SettleMS,
		"screenshot_id":    screenshotID,
		"foreground_title": moved.Foreground,
		"input_source":     "native",
	}, nil
}
