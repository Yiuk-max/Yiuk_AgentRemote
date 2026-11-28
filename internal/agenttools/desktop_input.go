package agenttools

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// 这一批补齐 GUI 自动化的基础原语：组合键、悬停、等待。
// 组合键与悬停仍然走「已有 execute_command + get_command_result」的组合路径，
// 只有 wait 完全在 MCP 本地完成，不占用设备命令通道。

const (
	pressKeysTimeoutSeconds = 30
	moveMouseTimeoutSeconds = 15
	maxWaitMilliseconds     = 25000
	maxWaitForSeconds       = 25
	waitForPollInterval     = 250 * time.Millisecond
)

// 修饰键与命名键只接受白名单，脚本里最终只出现数字 VK 码。
var modifierKeyCodes = map[string]uint16{
	"ctrl":  0x11,
	"shift": 0x10,
	"alt":   0x12,
	"win":   0x5B,
}

var namedKeyCodes = map[string]uint16{
	"enter": 0x0D, "return": 0x0D, "esc": 0x1B, "tab": 0x09, "space": 0x20,
	"backspace": 0x08, "delete": 0x2E, "insert": 0x2D, "home": 0x24, "end": 0x23,
	"pageup": 0x21, "pagedown": 0x22, "printscreen": 0x2C,
	"up": 0x26, "down": 0x28, "left": 0x25, "right": 0x27,
}

func init() {
	for i := 1; i <= 24; i++ {
		namedKeyCodes["f"+strconv.Itoa(i)] = uint16(0x6F + i)
	}
}

// parseKeyChord 把 "ctrl+shift+o" 解析成正序按下的 VK 码。
// 规则：1-4 段；最后一段必须是主键，前面只能是修饰键且不得重复。
func parseKeyChord(spec string) ([]uint16, error) {
	if spec == "" || len(spec) > 64 {
		return nil, fmt.Errorf("keys must contain 1-4 parts")
	}
	parts := strings.Split(spec, "+")
	if len(parts) < 1 || len(parts) > 4 {
		return nil, fmt.Errorf("keys must contain 1-4 parts")
	}
	var modifiers []uint16
	seen := map[string]bool{}
	for _, raw := range parts[:len(parts)-1] {
		name := strings.ToLower(strings.TrimSpace(raw))
		code, ok := modifierKeyCodes[name]
		if !ok {
			return nil, fmt.Errorf("%q is not a modifier (ctrl, shift, alt, win) or is not allowed before the main key", raw)
		}
		if seen[name] {
			return nil, fmt.Errorf("modifier %q is repeated", name)
		}
		seen[name] = true
		modifiers = append(modifiers, code)
	}
	main := strings.ToLower(strings.TrimSpace(parts[len(parts)-1]))
	code, ok := keyCode(main)
	if !ok {
		return nil, fmt.Errorf("%q is not a supported key", parts[len(parts)-1])
	}
	return append(modifiers, code), nil
}

func keyCode(name string) (uint16, bool) {
	if code, ok := namedKeyCodes[name]; ok {
		return code, true
	}
	if len(name) == 1 {
		switch {
		case name >= "a" && name <= "z":
			return uint16(name[0] - 'a' + 'A'), true
		case name >= "0" && name <= "9":
			return uint16(name[0]), true
		}
	}
	return 0, false
}

const pressKeysScript = `# Yiuk: press_keys
` + desktopPrelude + `$foreground = [YiukDesktop]::ForegroundTitle()
$vks = [int[]]@({{VKS}})
for ($i = 0; $i -lt {{REPEAT}}; $i++) {
    [YiukDesktop]::PressCombo($vks)
    if ($i -lt ({{REPEAT}} - 1)) { Start-Sleep -Milliseconds {{INTERVAL}} }
}
[pscustomobject]@{ sent = $true; foreground = $foreground } | ConvertTo-Json -Compress
`

const moveMouseScript = `# Yiuk: move_mouse
` + desktopPrelude + `$foreground = [YiukDesktop]::ForegroundTitle()
if (-not [YiukDesktop]::SetCursorPos({{X}}, {{Y}})) { throw 'SetCursorPos failed; the desktop may be locked or a secure desktop is active' }
if ({{SETTLE}} -gt 0) { Start-Sleep -Milliseconds {{SETTLE}} }
[pscustomobject]@{ x = {{X}}; y = {{Y}}; foreground = $foreground } | ConvertTo-Json -Compress
`

// 条件在 Go 侧校验，值以 base64 传入，避免引号和通配符影响 PowerShell 解析。
const waitForScript = `# Yiuk: wait_for
` + desktopPrelude + `$condition = '{{CONDITION}}'
$value = [System.Text.Encoding]::UTF8.GetString([System.Convert]::FromBase64String('{{VALUE_BASE64}}'))
$started = Get-Date
$deadline = $started.AddSeconds({{TIMEOUT}})
$matched = $false
$evidence = ''
while (-not $matched -and ((Get-Date) -lt $deadline)) {
    if ($condition -eq 'window_title') {
        $title = [YiukDesktop]::ForegroundTitle()
        if ($title.IndexOf($value, [System.StringComparison]::OrdinalIgnoreCase) -ge 0) { $matched = $true; $evidence = $title }
    } elseif ($condition -eq 'process') {
        $name = [System.IO.Path]::GetFileNameWithoutExtension($value)
        $proc = Get-Process -Name $name -ErrorAction SilentlyContinue | Select-Object -First 1
        if ($null -ne $proc) { $matched = $true; $evidence = ($proc.ProcessName + '#' + $proc.Id) }
    } else {
        if (Test-Path -LiteralPath $value) { $matched = $true; $evidence = $value }
    }
    if (-not $matched) { Start-Sleep -Milliseconds 250 }
}
$elapsed = [int]((Get-Date) - $started).TotalMilliseconds
[pscustomobject]@{ matched = $matched; timed_out = (-not $matched); elapsed_ms = $elapsed; evidence = $evidence } | ConvertTo-Json -Compress
`

// screenPoint 把「最近一次截图」的像素坐标换算成物理屏幕坐标，click 与 move_mouse 共用。
func (c *Core) screenPoint(sessionID string, x, y int) (int, int, screenGeometry, error) {
	ref, ok := c.desktop.latest(sessionID)
	if !ok {
		return 0, 0, screenGeometry{}, &Error{Code: "SCREENSHOT_REQUIRED", Message: "call take_screenshot for this session first; coordinates are pixels in the latest screenshot"}
	}
	geometry := ref.Geometry
	if x < 0 || y < 0 || x >= geometry.Width || y >= geometry.Height {
		return 0, 0, screenGeometry{}, &Error{Code: "INVALID_ARGUMENT", Message: fmt.Sprintf("x/y must be inside the latest screenshot (%dx%d)", geometry.Width, geometry.Height)}
	}
	screenX := geometry.Left + int(math.Round(float64(x)*float64(geometry.ScreenWidth)/float64(geometry.Width)))
	screenY := geometry.Top + int(math.Round(float64(y)*float64(geometry.ScreenHeight)/float64(geometry.Height)))
	return screenX, screenY, geometry, nil
}

func (c *Core) pressKeys(ctx context.Context, raw json.RawMessage) (any, error) {
	var input struct {
		SessionID  string `json:"session_id"`
		Keys       string `json:"keys"`
		Repeat     int    `json:"repeat"`
		IntervalMS int    `json:"interval_ms"`
	}
	if err := json.Unmarshal(raw, &input); err != nil {
		return nil, &Error{Code: "INVALID_ARGUMENT", Message: "invalid arguments"}
	}
	if input.Repeat == 0 {
		input.Repeat = 1
	}
	if input.IntervalMS == 0 {
		input.IntervalMS = 60
	}
	codes, err := parseKeyChord(input.Keys)
	if err != nil {
		return nil, &Error{Code: "INVALID_ARGUMENT", Message: err.Error()}
	}
	parts := make([]string, len(codes))
	for i, code := range codes {
		parts[i] = fmt.Sprintf("0x%02X", code)
	}
	script := strings.NewReplacer(
		"{{VKS}}", strings.Join(parts, ","),
		"{{REPEAT}}", strconv.Itoa(input.Repeat),
		"{{INTERVAL}}", strconv.Itoa(input.IntervalMS),
	).Replace(pressKeysScript)
	var pressed struct {
		Foreground string `json:"foreground"`
	}
	if err := c.runDesktopJSON(ctx, input.SessionID, script, pressKeysTimeoutSeconds, &pressed); err != nil {
		return nil, err
	}
	return map[string]any{
		"session_id":       input.SessionID,
		"keys":             input.Keys,
		"repeat":           input.Repeat,
		"sent":             true,
		"foreground_title": pressed.Foreground,
	}, nil
}

func (c *Core) moveMouse(ctx context.Context, raw json.RawMessage) (any, error) {
	var input struct {
		SessionID    string `json:"session_id"`
		X            int    `json:"x"`
		Y            int    `json:"y"`
		SettleMS     int    `json:"settle_ms"`
		ScreenshotID string `json:"screenshot_id"`
	}
	if err := json.Unmarshal(raw, &input); err != nil {
		return nil, &Error{Code: "INVALID_ARGUMENT", Message: "invalid arguments"}
	}
	if err := c.requireFreshScreenshot(input.SessionID, input.ScreenshotID); err != nil {
		return nil, err
	}
	screenX, screenY, _, err := c.screenPoint(input.SessionID, input.X, input.Y)
	if err != nil {
		return nil, err
	}
	script := strings.NewReplacer(
		"{{X}}", strconv.Itoa(screenX), "{{Y}}", strconv.Itoa(screenY),
		"{{SETTLE}}", strconv.Itoa(input.SettleMS),
	).Replace(moveMouseScript)
	var moved struct {
		Foreground string `json:"foreground"`
	}
	if err := c.runDesktopJSON(ctx, input.SessionID, script, moveMouseTimeoutSeconds, &moved); err != nil {
		return nil, err
	}
	latest, _ := c.desktop.latest(input.SessionID)
	return map[string]any{
		"session_id":       input.SessionID,
		"x":                input.X,
		"y":                input.Y,
		"screen_x":         screenX,
		"screen_y":         screenY,
		"settle_ms":        input.SettleMS,
		"screenshot_id":    latest.ID,
		"foreground_title": moved.Foreground,
	}, nil
}

func (c *Core) waitFor(ctx context.Context, raw json.RawMessage) (any, error) {
	var input struct {
		SessionID string `json:"session_id"`
		Condition string `json:"condition"`
		Value     string `json:"value"`
		Timeout   int    `json:"timeout_seconds"`
	}
	if err := json.Unmarshal(raw, &input); err != nil {
		return nil, &Error{Code: "INVALID_ARGUMENT", Message: "invalid arguments"}
	}
	if input.Timeout == 0 {
		input.Timeout = 10
	}
	script := strings.NewReplacer(
		"{{CONDITION}}", input.Condition,
		"{{VALUE_BASE64}}", base64.StdEncoding.EncodeToString([]byte(input.Value)),
		"{{TIMEOUT}}", strconv.Itoa(input.Timeout),
	).Replace(waitForScript)
	// 设备侧最多轮询 timeout 秒，命令超时留 10 秒余量给提交与取回结果。
	var result struct {
		Matched   bool   `json:"matched"`
		TimedOut  bool   `json:"timed_out"`
		ElapsedMS int    `json:"elapsed_ms"`
		Evidence  string `json:"evidence"`
	}
	if err := c.runDesktopJSON(ctx, input.SessionID, script, input.Timeout+10, &result); err != nil {
		return nil, err
	}
	return map[string]any{
		"session_id": input.SessionID,
		"condition":  input.Condition,
		"value":      input.Value,
		"matched":    result.Matched,
		"timed_out":  result.TimedOut,
		"elapsed_ms": result.ElapsedMS,
		"evidence":   result.Evidence,
	}, nil
}

// wait 在 MCP 本地睡眠，不接触设备。被取消时返回实际等待时长，由调用方自行判断。
func (c *Core) wait(ctx context.Context, raw json.RawMessage) (any, error) {
	var input struct {
		MS int `json:"ms"`
	}
	if err := json.Unmarshal(raw, &input); err != nil {
		return nil, &Error{Code: "INVALID_ARGUMENT", Message: "invalid arguments"}
	}
	started := time.Now()
	if input.MS > 0 {
		timer := time.NewTimer(time.Duration(input.MS) * time.Millisecond)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
			return map[string]any{"waited_ms": int(time.Since(started).Milliseconds()), "cancelled": true}, nil
		}
	}
	return map[string]any{"waited_ms": int(time.Since(started).Milliseconds())}, nil
}
