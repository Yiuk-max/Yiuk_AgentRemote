package agenttools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// save_template：把最近一帧截图里的一块像素存成模板，之后用 template_id 复用。
//
// 关键约束（P5 冻结）：只能从**最近一张截图保留的像素**裁剪。像素已被回收时明确
// STALE_SCREENSHOT，绝不为了这次调用重新抓屏——那会让模板与坐标基准错位，
// 也会让"我看到的是旧画面"这种判断变成谎言。

const (
	nativeSaveTemplateOperation = "vision.save_template"
	// 裁剪 + PNG 编码 + 原子写盘都在设备本地，秒级足够。
	nativeSaveTemplateTimeoutSeconds = 15
)

func (c *Core) saveTemplate(ctx context.Context, raw json.RawMessage) (any, error) {
	var input struct {
		SessionID    string `json:"session_id"`
		ScreenshotID string `json:"screenshot_id"`
		Region       []int  `json:"region"`
	}
	if err := json.Unmarshal(raw, &input); err != nil {
		return nil, &Error{Code: "INVALID_ARGUMENT", Message: "invalid arguments"}
	}
	if len(input.Region) != 4 {
		return nil, &Error{Code: "INVALID_ARGUMENT", Message: "region must be [left,top,right,bottom]"}
	}
	if input.Region[2] <= input.Region[0] || input.Region[3] <= input.Region[1] {
		return nil, &Error{Code: "INVALID_ARGUMENT", Message: "region must have a positive area"}
	}
	if input.Region[0] < 0 || input.Region[1] < 0 {
		return nil, &Error{Code: "INVALID_ARGUMENT", Message: "region must not be negative"}
	}

	arguments := map[string]any{"region": input.Region}
	if screenshotID := strings.TrimSpace(input.ScreenshotID); screenshotID != "" {
		arguments["screenshot_id"] = screenshotID
	}

	rawResult, err := c.invokeNative(ctx, input.SessionID, nativeSaveTemplateOperation, arguments,
		nativeSaveTemplateTimeoutSeconds)
	if err != nil {
		if isUnsupportedNative(err) {
			return nil, &Error{
				Code:    "UNSUPPORTED_CLIENT",
				Message: "device does not support vision.save_template",
				Hint:    "该设备的客户端还不能保存模板；请设备所有者升级 Qt 客户端，或继续每次上传 image_b64",
			}
		}
		return nil, err
	}

	var result struct {
		TemplateID string `json:"template_id"`
		Width      int    `json:"width"`
		Height     int    `json:"height"`
		SizeBytes  int    `json:"size_bytes"`
		Region     []int  `json:"region"`
	}
	if err := json.Unmarshal(rawResult, &result); err != nil {
		return nil, &Error{Code: "NATIVE_RESULT_INVALID",
			Message: "device returned a malformed save_template result",
			Hint:    "客户端与 MCP 的版本可能不匹配；升级到同一发布批次后重试"}
	}
	// template_id 是 find / click_target 的引用键：格式不对就不能往外交。
	if !validTemplateID(result.TemplateID) {
		return nil, &Error{Code: "NATIVE_RESULT_INVALID",
			Message: "device returned an invalid template id"}
	}

	return map[string]any{
		"session_id":  input.SessionID,
		"template_id": result.TemplateID,
		"width":       result.Width,
		"height":      result.Height,
		"size_bytes":  result.SizeBytes,
		"region":      result.Region,
		"hint":        fmt.Sprintf("find_on_screen / click_target with template_id=%s", result.TemplateID),
	}, nil
}

// validTemplateID 与客户端的格式一致：12 位小写十六进制（48 bit 随机）。
func validTemplateID(id string) bool {
	if len(id) != 12 {
		return false
	}
	for _, ch := range id {
		if (ch < '0' || ch > '9') && (ch < 'a' || ch > 'f') {
			return false
		}
	}
	return true
}
