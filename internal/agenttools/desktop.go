package agenttools

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// 截屏与点击是组合工具：通过已有的 execute_command / get_command_result / download_file
// 在设备上运行 PowerShell 完成，平台协议与设备端客户端都无需改动。

const (
	defaultScreenshotMaxWidth = 1568
	screenshotTimeoutSeconds  = 60
	clickTimeoutSeconds       = 30
	commandPollInterval       = 500 * time.Millisecond
)

// Image 是工具返回给 Agent 的图片内容，不进入 JSON 结果。
type Image struct {
	MIMEType string
	Data     []byte
}

// screenGeometry 记录某会话最近一次截屏的图像尺寸与对应的物理屏幕区域。
type screenGeometry struct {
	Left         int `json:"left"`
	Top          int `json:"top"`
	ScreenWidth  int `json:"screen_width"`
	ScreenHeight int `json:"screen_height"`
	Width        int `json:"width"`
	Height       int `json:"height"`
}

// screenshotRef 把「一张截图」的 id 与它当时的屏幕几何绑在一起。
// 最近 5 张都保留，仅用于把 STALE_SCREENSHOT 的报错说清楚。
type screenshotRef struct {
	ID       string
	Geometry screenGeometry
}

// sessionGate 让同一会话的截屏串行执行并保持最小间隔，
// 避免 agent 连发截图把低内存设备打满。用信号量而不是互斥锁，
// 排队期间的取消才能立即生效。
type sessionGate struct {
	sem  chan struct{}
	mu   sync.Mutex
	last time.Time
}

type desktopState struct {
	mu       sync.Mutex
	recent   map[string][]screenshotRef
	sequence uint64
	gates    map[string]*sessionGate
}

const (
	maxScreenshotHistory  = 5
	minScreenshotInterval = 500 * time.Millisecond

	// 变化判据：强变化像素占比，或整块平均差。阈值来自真机测量：
	// 点中按钮（标签文字改变）changed_ratio ≈ 0.004~0.02，
	// 点空白处 ≈ 0.000~0.001。
	changedPixelRatioThreshold = 0.002
	changedMeanThreshold       = 0.02
)

func (s *desktopState) gate(sessionID string) *sessionGate {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.gates == nil {
		s.gates = map[string]*sessionGate{}
	}
	gate, ok := s.gates[sessionID]
	if !ok {
		gate = &sessionGate{sem: make(chan struct{}, 1)}
		s.gates[sessionID] = gate
	}
	return gate
}

// newScreenshotID 生成短 id；只用于绑定坐标基准，不承担安全用途。
func (s *desktopState) newScreenshotID(sessionID string) string {
	s.mu.Lock()
	s.sequence++
	sequence := s.sequence
	s.mu.Unlock()
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s:%d:%d", sessionID, sequence, time.Now().UnixNano())))
	return hex.EncodeToString(sum[:])[:12]
}

func (s *desktopState) remember(sessionID string, ref screenshotRef) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.recent == nil {
		s.recent = map[string][]screenshotRef{}
	}
	history := append([]screenshotRef{ref}, s.recent[sessionID]...)
	if len(history) > maxScreenshotHistory {
		history = history[:maxScreenshotHistory]
	}
	s.recent[sessionID] = history
}

func (s *desktopState) latest(sessionID string) (screenshotRef, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	history := s.recent[sessionID]
	if len(history) == 0 {
		return screenshotRef{}, false
	}
	return history[0], true
}

// find 返回指定 id 在历史中的位置（0 = 最新）。
func (s *desktopState) find(sessionID, id string) (screenshotRef, int, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for age, ref := range s.recent[sessionID] {
		if ref.ID == id {
			return ref, age, true
		}
	}
	return screenshotRef{}, 0, false
}

// acquireScreenshotSlot 串行化同一会话的截屏，并保证最小间隔；
// 返回的函数用于释放。排队与等待期间都响应 ctx 取消。
func (c *Core) acquireScreenshotSlot(ctx context.Context, sessionID string) (func(), error) {
	gate := c.desktop.gate(sessionID)
	select {
	case gate.sem <- struct{}{}:
	case <-ctx.Done():
		return nil, &Error{Code: "CANCELLED", Message: "tool call was cancelled while waiting for the screenshot slot"}
	}
	release := func() { <-gate.sem }

	gate.mu.Lock()
	last := gate.last
	gate.mu.Unlock()
	if !last.IsZero() {
		if wait := minScreenshotInterval - time.Since(last); wait > 0 {
			timer := time.NewTimer(wait)
			defer timer.Stop()
			select {
			case <-timer.C:
			case <-ctx.Done():
				release()
				return nil, &Error{Code: "CANCELLED", Message: "tool call was cancelled while waiting for the screenshot slot"}
			}
		}
	}
	gate.mu.Lock()
	gate.last = time.Now()
	gate.mu.Unlock()
	return release, nil
}

// 让进程使用物理像素坐标；否则在缩放的屏幕上 CopyFromScreen 只会截到左上角一部分，点击也会偏移。
// TypeText 用 KEYEVENTF_UNICODE 直接投递字符，不经过键盘布局和输入法；换行按 Shift+Enter，避免聊天框提前发送。
// 脚本必须保持纯 ASCII：Windows PowerShell 5.1 读到无 BOM 的非 ASCII 脚本会按 ANSI 解码。
const desktopPrelude = `$ErrorActionPreference = 'Stop'
Add-Type -TypeDefinition @'
using System;
using System.Runtime.InteropServices;
public static class YiukDesktop {
    [DllImport("user32.dll")] static extern bool SetProcessDpiAwarenessContext(IntPtr value);
    [DllImport("user32.dll")] static extern bool SetProcessDPIAware();
    [DllImport("user32.dll")] public static extern int GetSystemMetrics(int index);
    [DllImport("user32.dll")] public static extern bool SetCursorPos(int x, int y);
    [DllImport("user32.dll")] public static extern void mouse_event(uint flags, uint dx, uint dy, uint data, UIntPtr extra);
    public static void UsePhysicalPixels() {
        try { if (SetProcessDpiAwarenessContext(new IntPtr(-4))) return; } catch (EntryPointNotFoundException) {}
        SetProcessDPIAware();
    }

    [StructLayout(LayoutKind.Sequential)] struct MOUSEINPUT { public int dx; public int dy; public uint mouseData; public uint dwFlags; public uint time; public IntPtr dwExtraInfo; }
    [StructLayout(LayoutKind.Sequential)] struct KEYBDINPUT { public ushort wVk; public ushort wScan; public uint dwFlags; public uint time; public IntPtr dwExtraInfo; }
    [StructLayout(LayoutKind.Explicit)] struct INPUTUNION { [FieldOffset(0)] public MOUSEINPUT mi; [FieldOffset(0)] public KEYBDINPUT ki; }
    [StructLayout(LayoutKind.Sequential)] struct INPUT { public uint type; public INPUTUNION u; }
    [DllImport("user32.dll")] static extern uint SendInput(uint count, INPUT[] inputs, int size);
    static INPUT Key(ushort vk, ushort scan, uint flags) {
        INPUT input = new INPUT();
        input.type = 1;
        input.u.ki.wVk = vk;
        input.u.ki.wScan = scan;
        input.u.ki.dwFlags = flags;
        return input;
    }
    static void Send(params INPUT[] inputs) {
        if (SendInput((uint)inputs.Length, inputs, Marshal.SizeOf(typeof(INPUT))) != inputs.Length)
            throw new InvalidOperationException("SendInput was blocked; the desktop may be locked or a secure desktop is active");
    }
    public static void PressKey(ushort vk) { Send(Key(vk, 0, 0), Key(vk, 0, 2)); }
    public static void Chord(ushort modifier, ushort vk) { Send(Key(modifier, 0, 0), Key(vk, 0, 0), Key(vk, 0, 2), Key(modifier, 0, 2)); }
    public static void TypeText(string text) {
        foreach (char ch in text.Replace("\r\n", "\n").Replace('\r', '\n')) {
            if (ch == '\n') Chord(0x10, 0x0D);
            else Send(Key(0, ch, 4), Key(0, ch, 4 | 2));
            System.Threading.Thread.Sleep(3);
        }
    }
    public static void PressCombo(int[] keys) {
        foreach (int key in keys) Send(Key((ushort)key, 0, 0));
        System.Threading.Thread.Sleep(25);
        for (int i = keys.Length - 1; i >= 0; i--) Send(Key((ushort)keys[i], 0, 2));
    }
    [DllImport("user32.dll")] static extern IntPtr GetForegroundWindow();
    [DllImport("user32.dll", CharSet = CharSet.Unicode)] static extern int GetWindowText(IntPtr hWnd, System.Text.StringBuilder text, int count);
    public static string ForegroundTitle() {
        System.Text.StringBuilder text = new System.Text.StringBuilder(1024);
        GetWindowText(GetForegroundWindow(), text, text.Capacity);
        return text.ToString();
    }
}
'@
[YiukDesktop]::UsePhysicalPixels()
`

const screenshotScript = `# Yiuk: take_screenshot
` + desktopPrelude + `Add-Type -AssemblyName System.Drawing
$maxWidth = {{MAX_WIDTH}}
$format = '{{FORMAT}}'
$left = [YiukDesktop]::GetSystemMetrics(76)
$top = [YiukDesktop]::GetSystemMetrics(77)
$screenWidth = [YiukDesktop]::GetSystemMetrics(78)
$screenHeight = [YiukDesktop]::GetSystemMetrics(79)
$dir = Join-Path $env:TEMP 'yiuk-screenshots'
New-Item -ItemType Directory -Force -Path $dir | Out-Null
Get-ChildItem -Path $dir -File | Remove-Item -Force -ErrorAction SilentlyContinue
$bitmap = New-Object System.Drawing.Bitmap $screenWidth, $screenHeight
$graphics = [System.Drawing.Graphics]::FromImage($bitmap)
try { $graphics.CopyFromScreen($left, $top, 0, 0, $bitmap.Size) } finally { $graphics.Dispose() }
$width = $screenWidth
$height = $screenHeight
if ($screenWidth -gt $maxWidth) {
    $width = $maxWidth
    $height = [int][Math]::Round($screenHeight * $maxWidth / $screenWidth)
    $scaled = New-Object System.Drawing.Bitmap $width, $height
    $graphics = [System.Drawing.Graphics]::FromImage($scaled)
    $graphics.InterpolationMode = [System.Drawing.Drawing2D.InterpolationMode]::HighQualityBicubic
    $graphics.DrawImage($bitmap, 0, 0, $width, $height)
    $graphics.Dispose()
    $bitmap.Dispose()
    $bitmap = $scaled
}
if ($format -eq 'jpeg') {
    $path = Join-Path $dir ('yiuk-screenshot-' + [guid]::NewGuid().ToString('N') + '.jpg')
    $codec = [System.Drawing.Imaging.ImageCodecInfo]::GetImageEncoders() | Where-Object { $_.MimeType -eq 'image/jpeg' }
    $encoderParams = New-Object System.Drawing.Imaging.EncoderParameters 1
    $encoderParams.Param[0] = New-Object System.Drawing.Imaging.EncoderParameter -ArgumentList ([System.Drawing.Imaging.Encoder]::Quality), ([long]80)
    $bitmap.Save($path, $codec, $encoderParams)
} else {
    $path = Join-Path $dir ('yiuk-screenshot-' + [guid]::NewGuid().ToString('N') + '.png')
    $bitmap.Save($path, [System.Drawing.Imaging.ImageFormat]::Png)
}
$bitmap.Dispose()
[pscustomobject]@{ path = $path; left = $left; top = $top; screen_width = $screenWidth; screen_height = $screenHeight; width = $width; height = $height } | ConvertTo-Json -Compress
`

const clickScript = `# Yiuk: click
` + desktopPrelude + `$foreground = [YiukDesktop]::ForegroundTitle()
if (-not [YiukDesktop]::SetCursorPos({{X}}, {{Y}})) { throw 'SetCursorPos failed; the desktop may be locked or a secure desktop is active' }
Start-Sleep -Milliseconds 50
for ($i = 0; $i -lt {{COUNT}}; $i++) {
    [YiukDesktop]::mouse_event({{DOWN}}, 0, 0, 0, [UIntPtr]::Zero)
    [YiukDesktop]::mouse_event({{UP}}, 0, 0, 0, [UIntPtr]::Zero)
    Start-Sleep -Milliseconds 80
}
[pscustomobject]@{ x = {{X}}; y = {{Y}}; foreground = $foreground } | ConvertTo-Json -Compress
`

// 文字以 base64 嵌入脚本，任何内容都不会被当作 PowerShell 代码解析。
// paste 模式在粘贴后等待目标程序读取剪贴板，再恢复原有文本。
// 点击后变化检测：只采样点击位置周围 192x108 区域并缩到 96x54 灰度，
// 不抓整屏，避免为了“看一眼有没有变”再产生一次大位图。
const clickWithChangeScript = `# Yiuk: click (change detection)
` + desktopPrelude + `Add-Type -AssemblyName System.Drawing
$screenLeft = [YiukDesktop]::GetSystemMetrics(76)
$screenTop = [YiukDesktop]::GetSystemMetrics(77)
$screenWidth = [YiukDesktop]::GetSystemMetrics(78)
$screenHeight = [YiukDesktop]::GetSystemMetrics(79)
$foreground = [YiukDesktop]::ForegroundTitle()
# 取点击点周围 192x108 的原分辨率灰度；小图缩放会把“一处文字变了”抹平。
function Sample([int]$cx, [int]$cy) {
    $w = 192; $h = 108
    $sx = [Math]::Max($screenLeft, [Math]::Min($screenLeft + $screenWidth - $w, $cx - [int]($w / 2)))
    $sy = [Math]::Max($screenTop, [Math]::Min($screenTop + $screenHeight - $h, $cy - [int]($h / 2)))
    $bitmap = New-Object System.Drawing.Bitmap $w, $h
    $graphics = [System.Drawing.Graphics]::FromImage($bitmap)
    try { $graphics.CopyFromScreen($sx, $sy, 0, 0, $bitmap.Size) } finally { $graphics.Dispose() }
    $bytes = New-Object byte[] ($w * $h)
    for ($y = 0; $y -lt $h; $y++) {
        for ($x = 0; $x -lt $w; $x++) {
            $pixel = $bitmap.GetPixel($x, $y)
            $bytes[$y * $w + $x] = [byte](($pixel.R + $pixel.G + $pixel.B) / 3)
        }
    }
    $bitmap.Dispose()
    return ,$bytes
}
$before = Sample {{X}} {{Y}}
if (-not [YiukDesktop]::SetCursorPos({{X}}, {{Y}})) { throw 'SetCursorPos failed; the desktop may be locked or a secure desktop is active' }
Start-Sleep -Milliseconds 50
for ($i = 0; $i -lt {{COUNT}}; $i++) {
    [YiukDesktop]::mouse_event({{DOWN}}, 0, 0, 0, [UIntPtr]::Zero)
    [YiukDesktop]::mouse_event({{UP}}, 0, 0, 0, [UIntPtr]::Zero)
    Start-Sleep -Milliseconds 80
}
Start-Sleep -Milliseconds {{SETTLE}}
$after = Sample {{X}} {{Y}}
$difference = 0
$strongPixels = 0
for ($i = 0; $i -lt $before.Length; $i++) {
    $delta = [Math]::Abs([int]$before[$i] - [int]$after[$i])
    $difference += $delta
    if ($delta -ge 24) { $strongPixels++ }
}
$diffRatio = [Math]::Round($difference / ($before.Length * 255.0), 4)
$changedRatio = [Math]::Round($strongPixels / [double]$before.Length, 4)
[pscustomobject]@{ x = {{X}}; y = {{Y}}; foreground = $foreground; diff_ratio = $diffRatio; changed_ratio = $changedRatio } | ConvertTo-Json -Compress
`

const typeTextScript = `# Yiuk: type_text
` + desktopPrelude + `$foreground = [YiukDesktop]::ForegroundTitle()
$text = [System.Text.Encoding]::UTF8.GetString([System.Convert]::FromBase64String('{{TEXT_BASE64}}'))
if ('{{METHOD}}' -eq 'paste') {
    $previous = $null
    try { $previous = Get-Clipboard -Raw -ErrorAction Stop } catch {}
    Set-Clipboard -Value ($text -replace "\r?\n", "` + "`r`n" + `")
    [YiukDesktop]::Chord(0x11, 0x56)
    Start-Sleep -Milliseconds 500
    if ($null -ne $previous) { Set-Clipboard -Value $previous }
} else {
    [YiukDesktop]::TypeText($text)
}
if (${{PRESS_ENTER}}) {
    Start-Sleep -Milliseconds 150
    [YiukDesktop]::PressKey(0x0D)
}
[pscustomobject]@{ characters = $text.Length; foreground = $foreground } | ConvertTo-Json -Compress
`

const (
	maxTypeTextRunes       = 4000
	typeTextTimeoutSeconds = 120
)

var mouseButtonFlags = map[string][2]int{
	"left":   {0x0002, 0x0004},
	"right":  {0x0008, 0x0010},
	"middle": {0x0020, 0x0040},
}

// callDesktopTool 处理截屏与点击；ok 为 false 表示 name 不是组合工具。
func (c *Core) callDesktopTool(ctx context.Context, name string, input json.RawMessage) (data any, images []Image, ok bool, err error) {
	switch name {
	case TakeScreenshot:
		data, images, err = c.takeScreenshot(ctx, input)
		return data, images, true, err
	case Click:
		data, err = c.click(ctx, input)
		return data, nil, true, err
	case TypeText:
		data, err = c.typeText(ctx, input)
		return data, nil, true, err
	case PressKeys:
		data, err = c.pressKeys(ctx, input)
		return data, nil, true, err
	case MoveMouse:
		data, err = c.moveMouse(ctx, input)
		return data, nil, true, err
	case WaitFor:
		data, err = c.waitFor(ctx, input)
		return data, nil, true, err
	case ListDir:
		data, err = c.listDir(ctx, input)
		return data, nil, true, err
	case StatPath:
		data, err = c.statPath(ctx, input)
		return data, nil, true, err
	}
	return nil, nil, false, nil
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

func (c *Core) click(ctx context.Context, raw json.RawMessage) (any, error) {
	var input struct {
		SessionID    string `json:"session_id"`
		X            int    `json:"x"`
		Y            int    `json:"y"`
		Button       string `json:"button"`
		Double       bool   `json:"double"`
		ScreenshotID string `json:"screenshot_id"`
		DetectChange bool   `json:"detect_change"`
		SettleMS     int    `json:"settle_ms"`
	}
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
	latest, _ := c.desktop.latest(input.SessionID)
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

// runDesktopJSON 运行组合脚本，并把 stdout 最后一行解析到 out。
// 所有输入类工具共用，避免每个 handler 重复“解析失败就报 DESKTOP_COMMAND_FAILED”。
func (c *Core) runDesktopJSON(ctx context.Context, sessionID, script string, timeoutSeconds int, out any) error {
	output, err := c.runDesktopScript(ctx, sessionID, script, timeoutSeconds)
	if err != nil {
		return err
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal([]byte(output), out); err != nil {
		return &Error{Code: "DESKTOP_COMMAND_FAILED", Message: "device returned an unexpected script result"}
	}
	return nil
}

// runDesktopScript 提交脚本并等待结束，返回 stdout 最后一个非空行。
func (c *Core) runDesktopScript(ctx context.Context, sessionID, script string, timeoutSeconds int) (string, error) {
	submitted, err := c.backend.Invoke(ctx, Request{Name: ExecuteCommand, Input: mustJSON(map[string]any{
		"session_id": sessionID, "command": script, "timeout_seconds": timeoutSeconds,
	})})
	if err != nil {
		return "", err
	}
	var task struct {
		RequestID string `json:"request_id"`
	}
	if err := decodeResult(submitted, &task); err != nil || task.RequestID == "" {
		return "", &Error{Code: "DESKTOP_COMMAND_FAILED", Message: "command submission returned no request_id"}
	}

	deadline := time.Now().Add(time.Duration(timeoutSeconds)*time.Second + 15*time.Second)
	query := mustJSON(map[string]any{"session_id": sessionID, "request_id": task.RequestID})
	for {
		raw, err := c.backend.Invoke(ctx, Request{Name: GetCommandResult, Input: query})
		if err != nil {
			return "", err
		}
		var result struct {
			Status string `json:"status"`
			Stdout string `json:"stdout"`
			Stderr string `json:"stderr"`
			Chunks []struct {
				Stream string `json:"stream"`
				Data   string `json:"data"`
			} `json:"chunks"`
		}
		if err := decodeResult(raw, &result); err != nil {
			return "", &Error{Code: "DESKTOP_COMMAND_FAILED", Message: "unexpected command result"}
		}
		for _, chunk := range result.Chunks {
			if chunk.Stream == "stderr" {
				result.Stderr += chunk.Data
			} else {
				result.Stdout += chunk.Data
			}
		}
		switch result.Status {
		case "", "queued", "running":
		case "succeeded":
			return lastNonEmptyLine(result.Stdout), nil
		default:
			return "", &Error{Code: "DESKTOP_COMMAND_FAILED", Message: result.Status + ": " + truncateText(strings.TrimSpace(result.Stderr), 800)}
		}
		if time.Now().After(deadline) {
			return "", &Error{Code: "DESKTOP_COMMAND_TIMEOUT", Message: "timed out waiting for the device"}
		}
		timer := time.NewTimer(commandPollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return "", &Error{Code: "CANCELLED", Message: "tool call was cancelled"}
		case <-timer.C:
		}
	}
}

func mustJSON(value any) json.RawMessage {
	data, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return data
}

// decodeResult 将后端返回的任意结构转换为目标类型；两种后端的结果字段名一致，但具体 Go 类型不同。
func decodeResult(value any, out any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, out)
}

func lastNonEmptyLine(text string) string {
	lines := strings.Split(strings.TrimSpace(text), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

func truncateText(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	return text[:limit] + "..."
}
