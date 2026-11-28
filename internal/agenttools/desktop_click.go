package agenttools

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strconv"
	"strings"
	"unicode/utf8"
)

const typeTextTimeoutSeconds = 120

var mouseButtonFlags = map[string][2]int{
	"left":   {0x0002, 0x0004},
	"right":  {0x0008, 0x0010},
	"middle": {0x0020, 0x0040},
}

// clickInput 是 click 的入参；单独命名是因为原生分支需要把它整份传给结果构造函数。
type clickInput struct {
	SessionID    string `json:"session_id"`
	X            int    `json:"x"`
	Y            int    `json:"y"`
	Button       string `json:"button"`
	Double       bool   `json:"double"`
	ScreenshotID string `json:"screenshot_id"`
	DetectChange bool   `json:"detect_change"`
	SettleMS     int    `json:"settle_ms"`
}

func (c *Core) click(ctx context.Context, raw json.RawMessage) (any, error) {
	var input clickInput
	if err := json.Unmarshal(raw, &input); err != nil {
		return nil, &Error{Code: "INVALID_ARGUMENT", Message: "invalid arguments"}
	}
	if input.Button == "" {
		input.Button = "left"
	}
	if input.DetectChange && input.SettleMS == 0 {
		input.SettleMS = 250
	}
	if !input.DetectChange {
		input.SettleMS = 0
	}
	if err := c.requireFreshScreenshot(input.SessionID, input.ScreenshotID); err != nil {
		return nil, err
	}
	screenX, screenY, _, err := c.screenPoint(input.SessionID, input.X, input.Y)
	if err != nil {
		return nil, err
	}
	latest, _ := c.desktop.latest(input.SessionID)

	// 新客户端优先走原生点击：坐标换算、SendInput、点击后的局部变化采样都在设备内部完成，
	// 不再为一次点击启动 PowerShell。设备没有该能力时才退回脚本路径。
	arguments := map[string]any{
		"x":             input.X,
		"y":             input.Y,
		"screenshot_id": latest.ID,
		"button":        input.Button,
		"double":        input.Double,
		"detect_change": input.DetectChange,
		"settle_ms":     input.SettleMS,
	}
	if nativeRaw, nativeErr := c.invokeNative(ctx, input.SessionID, nativeClickOperation, arguments,
		nativeInputTimeoutSeconds); nativeErr == nil {
		return c.nativeClickResult(input, latest.ID, screenX, screenY, nativeRaw)
	} else if !isUnsupportedNative(nativeErr) {
		return nil, nativeErr
	}

	count := 1
	if input.Double {
		count = 2
	}
	flags := mouseButtonFlags[input.Button]
	template := clickScript
	if input.DetectChange {
		template = clickWithChangeScript
		release, err := c.acquireScreenshotSlot(ctx, input.SessionID)
		if err != nil {
			return nil, err
		}
		defer release()
	}
	script := strings.NewReplacer(
		"{{X}}", strconv.Itoa(screenX), "{{Y}}", strconv.Itoa(screenY), "{{COUNT}}", strconv.Itoa(count),
		"{{DOWN}}", strconv.Itoa(flags[0]), "{{UP}}", strconv.Itoa(flags[1]),
		"{{SETTLE}}", strconv.Itoa(input.SettleMS),
	).Replace(template)
	var clicked struct {
		Foreground   string  `json:"foreground"`
		DiffRatio    float64 `json:"diff_ratio"`
		ChangedRatio float64 `json:"changed_ratio"`
	}
	if err := c.runDesktopJSON(ctx, input.SessionID, script, clickTimeoutSeconds, &clicked); err != nil {
		return nil, err
	}
	result := map[string]any{
		"session_id":       input.SessionID,
		"x":                input.X,
		"y":                input.Y,
		"screen_x":         screenX,
		"screen_y":         screenY,
		"button":           input.Button,
		"double":           input.Double,
		"screenshot_id":    latest.ID,
		"foreground_title": clicked.Foreground,
		"input_source":     "script",
	}
	if input.DetectChange {
		// 判据两条：明显变色的像素占比，或整体平均差。文字/按钮状态变化
		// 只占画面很小一块，所以必须看“强变化像素”，而不是平均亮度。
		result["changed"] = clicked.ChangedRatio >= changedPixelRatioThreshold || clicked.DiffRatio >= changedMeanThreshold
		result["diff_ratio"] = clicked.DiffRatio
		result["changed_ratio"] = clicked.ChangedRatio
		result["settle_ms"] = input.SettleMS
	}
	return result, nil
}

func (c *Core) typeText(ctx context.Context, raw json.RawMessage) (any, error) {
	var input struct {
		SessionID  string `json:"session_id"`
		Text       string `json:"text"`
		Method     string `json:"method"`
		PressEnter bool   `json:"press_enter"`
	}
	if err := json.Unmarshal(raw, &input); err != nil {
		return nil, &Error{Code: "INVALID_ARGUMENT", Message: "invalid arguments"}
	}
	if input.Method == "" {
		input.Method = "type"
	}
	script := strings.NewReplacer(
		"{{TEXT_BASE64}}", base64.StdEncoding.EncodeToString([]byte(input.Text)),
		"{{METHOD}}", input.Method,
		"{{PRESS_ENTER}}", strconv.FormatBool(input.PressEnter),
	).Replace(typeTextScript)
	var typed struct {
		Foreground string `json:"foreground"`
	}
	if err := c.runDesktopJSON(ctx, input.SessionID, script, typeTextTimeoutSeconds, &typed); err != nil {
		return nil, err
	}
	return map[string]any{
		"session_id":       input.SessionID,
		"characters":       utf8.RuneCountInString(input.Text),
		"method":           input.Method,
		"press_enter":      input.PressEnter,
		"foreground_title": typed.Foreground,
	}, nil
}
