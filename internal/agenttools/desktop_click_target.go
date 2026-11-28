package agenttools

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
)

// click_target：「找到就点」的一笔事务（P4）。
//
// 与 find_on_screen + click 的组合相比，差别不是"少调一次工具"，而是：
// 定位用的那一帧就是点击的坐标基准，且**全程持有设备门**——中间不会插进别的
// 会话抓屏或抢鼠标。因此不存在"找到的是旧画面"这种错点。
//
// 安全口径（规格 §6.3）：
//   - 默认 pick=unique：候选之间存在不同位置的近似并列结果时返回 AMBIGUOUS_TARGET，
//     **一个 down 事件都不发**；
//   - 没有命中返回 NOT_FOUND（带最高得分与阈值），同样不点；
//   - 两类错误都把候选现场放在错误 data 里，让 Agent 自己换参数，而不是"差不多就点"。

const (
	nativeClickTargetOperation = "vision.click_target"
	// 抓屏 + UIA 300ms / OpenCV 匹配 + 点击 + settle 全在一笔事务里，
	// 但平台单次原生操作硬上限是 25 秒。
	nativeClickTargetTimeoutSeconds = 25
	// 等目标出现时客户端每 500ms 一轮、轮间放设备门；预算比原生任务上限少 5 秒，
	// 给"找到后再点一下 + 采样"留出余量，不然得等到的瞬间先被平台超时杀掉。
	maxClickTargetWaitSeconds = 20
)

// clickTargetWaitTimeoutSeconds 把等待预算换成原生任务超时：
// 等待本身在客户端的一笔事务里完成（定位到就点，中间不放门），
// 所以这里只需要把平台的 25 秒上限切好。
func clickTargetWaitTimeoutSeconds(timeoutSeconds int) int {
	if timeoutSeconds <= 0 {
		return nativeClickTargetTimeoutSeconds
	}
	timeout := timeoutSeconds + 5
	if timeout > nativeClickTargetTimeoutSeconds {
		timeout = nativeClickTargetTimeoutSeconds
	}
	return timeout
}

func (c *Core) clickTarget(ctx context.Context, raw json.RawMessage) (any, error) {
	var input struct {
		SessionID      string  `json:"session_id"`
		Text           string  `json:"text"`
		ImagePath      string  `json:"image_path"`
		ImageB64       string  `json:"image_b64"`
		Threshold      float64 `json:"threshold"`
		Match          string  `json:"match"`
		ControlType    string  `json:"control_type"`
		Region         []int   `json:"region"`
		Limit          int     `json:"limit"`
		Pick           string  `json:"pick"`
		Button         string  `json:"button"`
		Double         bool    `json:"double"`
		DetectChange   *bool   `json:"detect_change"`
		SettleMS       *int    `json:"settle_ms"`
		WaitUntilFound bool    `json:"wait_until_found"`
		TimeoutSeconds int     `json:"timeout_seconds"`
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
	switch {
	case imagePath != "":
		arguments["image_path"] = imagePath
	case imageB64 != "":
		arguments["image_b64"] = imageB64
	default:
		arguments["text"] = text
		if input.Match != "" {
			arguments["match"] = input.Match
		}
		if input.ControlType != "" {
			arguments["control_type"] = input.ControlType
		}
	}
	if imagePath != "" || imageB64 != "" {
		if input.Threshold > 0 {
			arguments["threshold"] = input.Threshold
		}
	}
	if len(input.Region) > 0 {
		if len(input.Region) != 4 || input.Region[2] <= input.Region[0] || input.Region[3] <= input.Region[1] {
			return nil, &Error{Code: "INVALID_ARGUMENT",
				Message: "region must be [left,top,right,bottom] with a positive area"}
		}
		arguments["region"] = input.Region
	}
	if input.Limit > 0 {
		arguments["limit"] = input.Limit
	}
	if pick := strings.TrimSpace(input.Pick); pick != "" {
		arguments["pick"] = pick
	}
	if input.Button != "" {
		arguments["button"] = input.Button
	}
	if input.Double {
		arguments["double"] = true
	}
	// detect_change / settle_ms 用指针区分"没给"与"给了 false/0"：
	// 客户端默认 detect_change=true、settle_ms=250，但显式 0 不能被当成未传。
	if input.DetectChange != nil {
		arguments["detect_change"] = *input.DetectChange
	}
	if input.SettleMS != nil {
		arguments["settle_ms"] = *input.SettleMS
	}
	// 等目标出现：默认不等（click_target 的默认行为是"现在就叫点"）。
	if input.WaitUntilFound {
		seconds := input.TimeoutSeconds
		if seconds == 0 {
			seconds = 10
		}
		if seconds < 1 || seconds > maxClickTargetWaitSeconds {
			return nil, &Error{Code: "INVALID_ARGUMENT",
				Message: fmt.Sprintf("timeout_seconds must be between 1 and %d when wait_until_found is set",
					maxClickTargetWaitSeconds)}
		}
		arguments["wait_until_found"] = true
		arguments["timeout_seconds"] = seconds
		input.TimeoutSeconds = seconds
	}

	rawResult, err := c.invokeNative(ctx, input.SessionID, nativeClickTargetOperation, arguments,
		clickTargetWaitTimeoutSeconds(input.TimeoutSeconds))
	if err != nil {
		if isUnsupportedNative(err) {
			return nil, &Error{
				Code:    "UNSUPPORTED_CLIENT",
				Message: "device does not support vision.click_target",
				Hint:    "该设备的客户端还没有 click_target；请设备所有者升级 Qt 客户端，或先用 find_on_screen + click 组合",
			}
		}
		return nil, err
	}

	var result struct {
		ScreenshotID string `json:"screenshot_id"`
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
		Picked struct {
			Index       int     `json:"index"`
			BBox        []int   `json:"bbox"`
			Center      []int   `json:"center"`
			Score       float64 `json:"score"`
			Source      string  `json:"source"`
			Text        string  `json:"text"`
			ControlType string  `json:"control_type"`
		} `json:"picked"`
		CandidateCount int     `json:"candidate_count"`
		X              int     `json:"x"`
		Y              int     `json:"y"`
		ScreenX        int     `json:"screen_x"`
		ScreenY        int     `json:"screen_y"`
		Button         string  `json:"button"`
		Double         bool    `json:"double"`
		Foreground     string  `json:"foreground_title"`
		DiffRatio      float64 `json:"diff_ratio"`
		ChangedRatio   float64 `json:"changed_ratio"`
		DetectChange   bool    `json:"detect_change"`
		Attempts       int     `json:"attempts"`
		WaitedMS       int     `json:"waited_ms"`
		ElapsedMS      int     `json:"elapsed_ms"`
		Source         string  `json:"source"`
	}
	if err := json.Unmarshal(rawResult, &result); err != nil {
		return nil, &Error{Code: "NATIVE_RESULT_INVALID",
			Message: "device returned a malformed click_target result",
			Hint:    "客户端与 MCP 的版本可能不匹配；升级到同一发布批次后重试"}
	}
	if result.Width <= 0 || result.Height <= 0 || result.ScreenWidth <= 0 || result.ScreenHeight <= 0 {
		return nil, &Error{Code: "NATIVE_RESULT_INVALID",
			Message: "vision.click_target did not return a usable coordinate base"}
	}
	if result.Picked.Index < 1 || len(result.Picked.Center) != 2 {
		return nil, &Error{Code: "NATIVE_RESULT_INVALID",
			Message: "vision.click_target did not report which candidate it clicked"}
	}

	// 点击用的是新抓的这一帧：把它的坐标基准登记到 Go 侧快照表，
	// 这样后续 click / drag 带同一个 screenshot_id 也不会被判过期。
	if validScreenshotID(result.ScreenshotID) {
		if _, _, known := c.desktop.find(input.SessionID, result.ScreenshotID); !known {
			c.desktop.remember(input.SessionID, screenshotRef{ID: result.ScreenshotID, Geometry: screenGeometry{
				Left:         result.ScreenLeft,
				Top:          result.ScreenTop,
				ScreenWidth:  result.ScreenWidth,
				ScreenHeight: result.ScreenHeight,
				Width:        result.Width,
				Height:       result.Height,
			}})
		}
	}

	matches := make([]map[string]any, 0, len(result.Matches))
	for _, match := range result.Matches {
		entry := map[string]any{
			"text":         match.Text,
			"source":       match.Source,
			"control_type": match.ControlType,
			"score":        roundScore(match.Score),
		}
		if len(match.BBox) == 4 {
			entry["bbox"] = match.BBox
		}
		if len(match.Center) == 2 {
			entry["center"] = match.Center
		}
		matches = append(matches, entry)
	}

	// 坐标用设备回显的截图坐标（x/y）与物理坐标（screen_x/y）成对返回：
	// 前者用于继续跟 screenshot_id 配合，后者用于核对真实落点。
	data := map[string]any{
		"session_id":      input.SessionID,
		"screenshot_id":   result.ScreenshotID,
		"clicked":         true,
		"x":               result.X,
		"y":               result.Y,
		"screen_x":        result.ScreenX,
		"screen_y":        result.ScreenY,
		"button":          input.Button,
		"double":          result.Double,
		"picked":          pickedJSON(result.Picked.Index, result.Picked.BBox, result.Picked.Center, result.Picked.Score, result.Picked.Source, result.Picked.Text, result.Picked.ControlType),
		"matches":         matches,
		"candidate_count": result.CandidateCount,
		"source":          firstNonEmpty(result.Source, "uia"),
		"elapsed_ms":      result.ElapsedMS,
		// 等待语义的现场：默认不等时 attempts=1，timed_out 恒 false（超时是失败）。
		"attempts":         result.Attempts,
		"waited_ms":        result.WaitedMS,
		"timed_out":        false,
		"foreground_title": result.Foreground,
		"width":            result.Width,
		"height":           result.Height,
		"screen_left":      result.ScreenLeft,
		"screen_top":       result.ScreenTop,
		"screen_width":     result.ScreenWidth,
		"screen_height":    result.ScreenHeight,
		"input_source":     "native",
	}
	// 变化检测的判据与 click / 脚本路径完全一致，避免"设备说变了、MCP 说没变"。
	if result.DetectChange {
		data["changed"] = result.ChangedRatio >= changedPixelRatioThreshold || result.DiffRatio >= changedMeanThreshold
		data["diff_ratio"] = result.DiffRatio
		data["changed_ratio"] = result.ChangedRatio
	}
	return data, nil
}

func pickedJSON(index int, bbox, center []int, score float64, source, text, controlType string) map[string]any {
	entry := map[string]any{
		"index":        index,
		"score":        roundScore(score),
		"source":       source,
		"text":         text,
		"control_type": controlType,
	}
	if len(bbox) == 4 {
		entry["bbox"] = bbox
	}
	if len(center) == 2 {
		entry["center"] = center
	}
	return entry
}

// roundScore 保留两位小数：给 Agent 看的是决策依据，不需要完整双精度尾巴。
func roundScore(score float64) float64 { return math.Round(score*100) / 100 }
