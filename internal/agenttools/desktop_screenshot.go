package agenttools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
)

func (c *Core) takeScreenshot(ctx context.Context, raw json.RawMessage) (any, []Image, error) {
	var input struct {
		SessionID string `json:"session_id"`
		MaxWidth  int    `json:"max_width"`
		Format    string `json:"format"`
	}
	if err := json.Unmarshal(raw, &input); err != nil {
		return nil, nil, &Error{Code: "INVALID_ARGUMENT", Message: "invalid arguments"}
	}
	if input.MaxWidth == 0 {
		input.MaxWidth = defaultScreenshotMaxWidth
	}
	if input.Format == "" {
		input.Format = "png"
	}
	release, err := c.acquireScreenshotSlot(ctx, input.SessionID)
	if err != nil {
		return nil, nil, err
	}
	defer release()
	script := strings.NewReplacer("{{MAX_WIDTH}}", strconv.Itoa(input.MaxWidth), "{{FORMAT}}", input.Format).Replace(screenshotScript)
	output, err := c.runDesktopScript(ctx, input.SessionID, script, screenshotTimeoutSeconds)
	if err != nil {
		return nil, nil, err
	}
	var shot struct {
		Path string `json:"path"`
		screenGeometry
	}
	if err := json.Unmarshal([]byte(output), &shot); err != nil || shot.Path == "" || shot.Width <= 0 || shot.Height <= 0 || shot.ScreenWidth <= 0 || shot.ScreenHeight <= 0 {
		return nil, nil, &Error{Code: "SCREENSHOT_FAILED", Message: "device returned an unexpected screenshot result"}
	}

	downloaded, err := c.backend.Invoke(ctx, Request{Name: DownloadFile, Input: mustJSON(map[string]any{"session_id": input.SessionID, "remote_path": shot.Path})})
	if err != nil {
		return nil, nil, err
	}
	var file struct {
		LocalPath string `json:"local_path"`
	}
	if err := decodeResult(downloaded, &file); err != nil || file.LocalPath == "" {
		return nil, nil, &Error{Code: "SCREENSHOT_FAILED", Message: "screenshot download returned no local file"}
	}
	imageData, err := os.ReadFile(file.LocalPath)
	_ = os.Remove(file.LocalPath)
	if err != nil {
		return nil, nil, &Error{Code: "SCREENSHOT_FAILED", Message: "could not read the downloaded screenshot"}
	}

	screenshotID := c.desktop.newScreenshotID(input.SessionID)
	c.desktop.remember(input.SessionID, screenshotRef{ID: screenshotID, Geometry: shot.screenGeometry})
	mimeType := "image/png"
	if input.Format == "jpeg" {
		mimeType = "image/jpeg"
	}
	return map[string]any{
		"screenshot_id": screenshotID,
		"session_id":    input.SessionID,
		"width":         shot.Width,
		"height":        shot.Height,
		"screen_left":   shot.Left,
		"screen_top":    shot.Top,
		"screen_width":  shot.ScreenWidth,
		"screen_height": shot.ScreenHeight,
		"format":        input.Format,
		"size_bytes":    len(imageData),
	}, []Image{{MIMEType: mimeType, Data: imageData}}, nil
}

// requireFreshScreenshot 校验调用方声明的坐标基准；省略 id 时沿用最新一张。
func (c *Core) requireFreshScreenshot(sessionID, id string) error {
	if id == "" {
		return nil
	}
	latest, ok := c.desktop.latest(sessionID)
	if !ok {
		return &Error{Code: "SCREENSHOT_REQUIRED", Message: "call take_screenshot for this session first; coordinates are pixels in the latest screenshot"}
	}
	if latest.ID == id {
		return nil
	}
	if _, age, known := c.desktop.find(sessionID, id); known {
		return &Error{Code: "STALE_SCREENSHOT", Message: fmt.Sprintf(
			"screenshot_id %s is stale: %d newer screenshot(s) exist and the latest is %s. Re-run take_screenshot and use the new coordinates.",
			id, age, latest.ID)}
	}
	return &Error{Code: "STALE_SCREENSHOT", Message: fmt.Sprintf(
		"screenshot_id %s is unknown; the latest screenshot is %s. Re-run take_screenshot.", id, latest.ID)}
}
