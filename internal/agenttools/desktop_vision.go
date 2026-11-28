package agenttools

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
)

// find_on_screen：设备端 UIA 文本定位（P3）。
//
// 与抓屏/点击一样走原生通道：文本定位本质上是设备本地的 UIA 查询，
// 旧客户端没有这个能力，因此**没有脚本回退**——回退只会得到"找不到"，
// 那是在说谎。能力缺失时明确返回 UNSUPPORTED_CLIENT。

const (
	nativeFindOperation = "vision.find"
	// UIA 查询本身有 300ms 预算，加上设备门与一次可能的坐标基准抓屏，留足余量。
	nativeFindTimeoutSeconds = 20
)

func (c *Core) findOnScreen(ctx context.Context, raw json.RawMessage) (any, error) {
	var input struct {
		SessionID   string  `json:"session_id"`
		Text        string  `json:"text"`
		ImagePath   string  `json:"image_path"`
		ImageB64    string  `json:"image_b64"`
		Threshold   float64 `json:"threshold"`
		Match       string  `json:"match"`
		ControlType string  `json:"control_type"`
		Region      []int   `json:"region"`
		Limit       int     `json:"limit"`
	}
	if err := json.Unmarshal(raw, &input); err != nil {
		return nil, &Error{Code: "INVALID_ARGUMENT", Message: "invalid arguments"}
	}
	text := strings.TrimSpace(input.Text)
	imagePath := strings.TrimSpace(input.ImagePath)
	imageB64 := strings.TrimSpace(input.ImageB64)
	given := 0
	for _, value := range []string{text, imagePath, imageB64} {
		if value != "" {
			given++
		}
	}
	if given != 1 {
		return nil, &Error{Code: "INVALID_ARGUMENT",
			Message: "give exactly one of text, image_path or image_b64"}
	}
	arguments := map[string]any{}
	if imagePath != "" {
		arguments["image_path"] = imagePath
		if input.Threshold > 0 {
			arguments["threshold"] = input.Threshold
		}
	} else if imageB64 != "" {
		arguments["image_b64"] = imageB64
		if input.Threshold > 0 {
			arguments["threshold"] = input.Threshold
		}
	} else {
		arguments["text"] = text
		if input.Match != "" {
			arguments["match"] = input.Match
		}
		if input.ControlType != "" {
			arguments["control_type"] = input.ControlType
		}
	}
	if len(input.Region) > 0 {
		if len(input.Region) != 4 || input.Region[2] <= input.Region[0] || input.Region[3] <= input.Region[1] {
			return nil, &Error{Code: "INVALID_ARGUMENT", Message: "region must be [left,top,right,bottom] with a positive area"}
		}
		arguments["region"] = input.Region
	}
	if input.Limit > 0 {
		arguments["limit"] = input.Limit
	}

	rawResult, err := c.invokeNative(ctx, input.SessionID, nativeFindOperation, arguments, nativeFindTimeoutSeconds)
	if err != nil {
		if isUnsupportedNative(err) {
			return nil, &Error{
				Code:    "UNSUPPORTED_CLIENT",
				Message: "device does not support vision.find",
				Hint:    "该设备的客户端还没有文本定位能力；请设备所有者升级 Qt 客户端（旧工具不受影响）",
			}
		}
		return nil, err
	}

	var result struct {
		ScreenshotID string `json:"screenshot_id"`
		Format       string `json:"format"`
		ScreenLeft   int    `json:"screen_left"`
		ScreenTop    int    `json:"screen_top"`
		ScreenWidth  int    `json:"screen_width"`
		ScreenHeight int    `json:"screen_height"`
		Width        int    `json:"width"`
		Height       int    `json:"height"`
		Matches      []struct {
			BBox        []int   `json:"bbox"`
			Center      []int   `json:"center"`
			Score       float64 `json:"score"`
			Source      string  `json:"source"`
			Text        string  `json:"text"`
			ControlType string  `json:"control_type"`
		} `json:"matches"`
		Truncated    bool   `json:"truncated"`
		VisitedNodes int    `json:"visited_nodes"`
		ElapsedMS    int    `json:"elapsed_ms"`
		Source       string `json:"source"`
	}
	if err := json.Unmarshal(rawResult, &result); err != nil {
		return nil, &Error{Code: "NATIVE_RESULT_INVALID",
			Message: "device returned a malformed vision.find result",
			Hint:    "客户端与 MCP 的版本可能不匹配；升级到同一发布批次后重试"}
	}
	if result.Width <= 0 || result.Height <= 0 || result.ScreenWidth <= 0 || result.ScreenHeight <= 0 {
		return nil, &Error{Code: "NATIVE_RESULT_INVALID",
			Message: "vision.find did not return a usable coordinate base"}
	}

	// 设备侧的坐标基准必须和 Go 侧快照表一致：未知就登记，已知就不动
	// （find 通常复用最近一张截图，重复登记会把历史挤掉）。
	geometry := screenGeometry{
		Left:         result.ScreenLeft,
		Top:          result.ScreenTop,
		ScreenWidth:  result.ScreenWidth,
		ScreenHeight: result.ScreenHeight,
		Width:        result.Width,
		Height:       result.Height,
	}
	if validScreenshotID(result.ScreenshotID) {
		if _, _, known := c.desktop.find(input.SessionID, result.ScreenshotID); !known {
			c.desktop.remember(input.SessionID, screenshotRef{ID: result.ScreenshotID, Geometry: geometry})
		}
	}

	matches := make([]map[string]any, 0, len(result.Matches))
	for _, match := range result.Matches {
		entry := map[string]any{
			"text":         match.Text,
			"source":       match.Source,
			"control_type": match.ControlType,
		}
		if len(match.BBox) == 4 {
			entry["bbox"] = match.BBox
		}
		if len(match.Center) == 2 {
			entry["center"] = match.Center
		}
		// 文本匹配质量：完全一致是 1.0，包含是 0.9；不要伪装成图像置信度。
		entry["score"] = math.Round(match.Score*100) / 100
		matches = append(matches, entry)
	}

	return map[string]any{
		"session_id":    input.SessionID,
		"screenshot_id": result.ScreenshotID,
		"matches":       matches,
		"count":         len(matches),
		"truncated":     result.Truncated,
		"source":        firstNonEmpty(result.Source, "uia"),
		"visited_nodes": result.VisitedNodes,
		"elapsed_ms":    result.ElapsedMS,
		"width":         result.Width,
		"height":        result.Height,
		"screen_left":   result.ScreenLeft,
		"screen_top":    result.ScreenTop,
		"screen_width":  result.ScreenWidth,
		"screen_height": result.ScreenHeight,
		// 明确的下一步：把 center 直接喂给 click，坐标基准就是 screenshot_id。
		"hint": fmt.Sprintf("click or drag with screenshot_id=%s and the center of the chosen match", result.ScreenshotID),
	}, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
