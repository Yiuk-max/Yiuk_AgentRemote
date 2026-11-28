package agenttools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// nativeScreenshotOperation 是设备端注册的原生抓屏操作名。
// 与 MCP 侧 23 个工具名无关：它是协议层的内部操作，不直接暴露给 Agent。
const nativeScreenshotOperation = "screen.capture"

// nativeScreenshotTimeoutSeconds 覆盖一次抓屏（含设备排队与 PNG 编码），
// 上限受平台侧原生操作 25 秒的硬限制约束。
const nativeScreenshotTimeoutSeconds = 20

// nativeScreenshotReleaseOperation 让设备删除一张已经取走的临时截图。
// 这是纯清理动作：失败只影响磁盘占用，绝不影响工具结果。
const nativeScreenshotReleaseOperation = "screen.release"

const nativeReleaseTimeoutSeconds = 5

// nativeCapture 是 screen.capture 返回的业务数据。
// 几何字段最终映射到 screenGeometry，两条路径（原生 / 脚本）共用后续逻辑。
type nativeCapture struct {
	ScreenshotID string
	Path         string
	Format       string
	Geometry     screenGeometry
}

// UnmarshalJSON 兼容两种坐标键名：客户端原生信封用 screen_left / screen_top，
// 旧脚本路径用 left / top；两者语义相同，都必须落到同一个 screenGeometry，
// 否则负的虚拟桌面原点会在原生路径上被静默丢掉。
func (n *nativeCapture) UnmarshalJSON(data []byte) error {
	var wire struct {
		ScreenshotID string `json:"screenshot_id"`
		Path         string `json:"path"`
		Format       string `json:"format"`
		ScreenLeft   *int   `json:"screen_left"`
		Left         *int   `json:"left"`
		ScreenTop    *int   `json:"screen_top"`
		Top          *int   `json:"top"`
		ScreenWidth  int    `json:"screen_width"`
		ScreenHeight int    `json:"screen_height"`
		Width        int    `json:"width"`
		Height       int    `json:"height"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	n.ScreenshotID = wire.ScreenshotID
	n.Path = wire.Path
	n.Format = wire.Format
	n.Geometry = screenGeometry{
		ScreenWidth:  wire.ScreenWidth,
		ScreenHeight: wire.ScreenHeight,
		Width:        wire.Width,
		Height:       wire.Height,
	}
	if wire.ScreenLeft != nil {
		n.Geometry.Left = *wire.ScreenLeft
	} else if wire.Left != nil {
		n.Geometry.Left = *wire.Left
	}
	if wire.ScreenTop != nil {
		n.Geometry.Top = *wire.ScreenTop
	} else if wire.Top != nil {
		n.Geometry.Top = *wire.Top
	}
	return nil
}

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

	// 新客户端优先走原生通道：抓屏、缩放、写盘都在客户端内部完成，
	// 会话级互斥与 500ms 最小间隔也由客户端负责，不再启动 PowerShell。
	if shot, ok, err := c.tryNativeScreenshot(ctx, input.SessionID, input.MaxWidth, input.Format); err != nil {
		return nil, nil, err
	} else if ok {
		return c.finishScreenshot(ctx, input.SessionID, shot.Path, shot.Geometry, shot.ScreenshotID,
			shot.Format, "native")
	}

	// 旧客户端 / 本地后端：沿用脚本抓屏（Path 由设备上的 PowerShell 写出）。
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
	return c.finishScreenshot(ctx, input.SessionID, shot.Path, shot.screenGeometry, "", input.Format, "script")
}

// tryNativeScreenshot 尝试原生抓屏。
//
// 返回 (shot, true, nil) 表示成功；(_, false, nil) 表示应回退脚本路径（后端或设备没有原生通道）；
// 其它情况是真实失败，必须原样上报——尤其不能在 result_unknown 之后偷偷重跑脚本。
func (c *Core) tryNativeScreenshot(ctx context.Context, sessionID string, maxWidth int, format string) (nativeCapture, bool, error) {
	arguments := map[string]any{
		"max_width": maxWidth,
		"format":    format,
		// 图片必须落盘：MCP 侧通过既有的 download_file 通路取文件，原生信封里不带像素。
		"save": true,
	}
	raw, err := c.invokeNative(ctx, sessionID, nativeScreenshotOperation, arguments, nativeScreenshotTimeoutSeconds)
	if err != nil {
		if isUnsupportedNative(err) {
			return nativeCapture{}, false, nil
		}
		return nativeCapture{}, false, err
	}
	var shot nativeCapture
	if err := json.Unmarshal(raw, &shot); err != nil {
		return nativeCapture{}, false, &Error{Code: "SCREENSHOT_FAILED", Message: "device returned an unreadable native screenshot result"}
	}
	// id 由客户端生成，MCP 侧只做形状校验：绝不静默替换成自己的 id，
	// 否则后续 click 携带的 screenshot_id 会和设备侧记录分叉。
	if !validScreenshotID(shot.ScreenshotID) {
		return nativeCapture{}, false, &Error{
			Code:    "SCREENSHOT_FAILED",
			Message: "device returned an invalid screenshot_id",
			Hint:    "客户端与 MCP 的版本可能不匹配；升级到同一发布批次后重试",
		}
	}
	if shot.Path == "" || shot.Geometry.Width <= 0 || shot.Geometry.Height <= 0 ||
		shot.Geometry.ScreenWidth <= 0 || shot.Geometry.ScreenHeight <= 0 {
		return nativeCapture{}, false, &Error{Code: "SCREENSHOT_FAILED", Message: "device returned an unexpected native screenshot result"}
	}
	return shot, true, nil
}

// finishScreenshot 下载设备上的临时截图、读入内存、登记坐标基准并返回工具结果。
//
// 两条路径（原生 / 脚本）只有"谁生成 id、谁管临时文件"不同：
// 原生路径的临时文件由客户端按会话回收（最多保留 5 张，会话结束全删）。
func (c *Core) finishScreenshot(ctx context.Context, sessionID, devicePath string, geometry screenGeometry,
	providedID, format, source string) (any, []Image, error) {
	if source == "native" && devicePath != "" {
		// 图片要么已经读进内存、要么下载失败；两种情况设备都不需要再留着这份临时文件。
		// 放在 defer 里保证失败路径也会清理，且不影响工具结果。
		defer c.releaseNativeScreenshot(ctx, sessionID, devicePath)
	}
	downloaded, err := c.backend.Invoke(ctx, Request{Name: DownloadFile, Input: mustJSON(map[string]any{
		"session_id": sessionID, "remote_path": devicePath,
	})})
	if err != nil {
		return nil, nil, err
	}
	var file struct {
		LocalPath string `json:"local_path"`
	}
	if err := decodeResult(downloaded, &file); err != nil || file.LocalPath == "" {
		return nil, nil, &Error{Code: "SCREENSHOT_FAILED", Message: "screenshot download returned no local file"}
	}
	imageData, readErr := os.ReadFile(file.LocalPath)
	_ = os.Remove(file.LocalPath)
	if readErr != nil {
		return nil, nil, &Error{Code: "SCREENSHOT_FAILED", Message: "could not read the downloaded screenshot"}
	}

	screenshotID := providedID
	if screenshotID == "" {
		screenshotID = c.desktop.newScreenshotID(sessionID)
	}
	c.desktop.remember(sessionID, screenshotRef{ID: screenshotID, Geometry: geometry})

	mimeType := "image/png"
	if strings.EqualFold(format, "jpeg") || strings.EqualFold(format, "jpg") {
		mimeType = "image/jpeg"
	}
	return map[string]any{
		"screenshot_id": screenshotID,
		"session_id":    sessionID,
		"width":         geometry.Width,
		"height":        geometry.Height,
		"screen_left":   geometry.Left,
		"screen_top":    geometry.Top,
		"screen_width":  geometry.ScreenWidth,
		"screen_height": geometry.ScreenHeight,
		"format":        format,
		"size_bytes":    len(imageData),
		// capture_source 说明这一张是设备原生抓屏还是旧脚本抓屏，便于排查版本混用。
		"capture_source": source,
	}, []Image{{MIMEType: mimeType, Data: imageData}}, nil
}

// releaseNativeScreenshot 尽力让设备删除临时截图。
//
// 用去掉取消的 context：工具本身被取消时，清理仍然应该发生（但只等几秒）。
// 旧客户端没有 screen.release 能力时这里会拿到 UNSUPPORTED_CLIENT，静默忽略即可。
func (c *Core) releaseNativeScreenshot(ctx context.Context, sessionID, path string) {
	if path == "" {
		return
	}
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), nativeReleaseTimeoutSeconds+time.Second)
	defer cancel()
	_, _ = c.invokeNative(cleanupCtx, sessionID, nativeScreenshotReleaseOperation,
		map[string]any{"path": path}, nativeReleaseTimeoutSeconds)
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
