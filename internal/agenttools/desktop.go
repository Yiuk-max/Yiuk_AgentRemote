package agenttools

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
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

type desktopState struct {
	mu      sync.Mutex
	screens map[string]screenGeometry
}

func (s *desktopState) remember(sessionID string, geometry screenGeometry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.screens == nil {
		s.screens = map[string]screenGeometry{}
	}
	s.screens[sessionID] = geometry
}

func (s *desktopState) lookup(sessionID string) (screenGeometry, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	geometry, ok := s.screens[sessionID]
	return geometry, ok
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
` + desktopPrelude + `if (-not [YiukDesktop]::SetCursorPos({{X}}, {{Y}})) { throw 'SetCursorPos failed; the desktop may be locked or a secure desktop is active' }
Start-Sleep -Milliseconds 50
for ($i = 0; $i -lt {{COUNT}}; $i++) {
    [YiukDesktop]::mouse_event({{DOWN}}, 0, 0, 0, [UIntPtr]::Zero)
    [YiukDesktop]::mouse_event({{UP}}, 0, 0, 0, [UIntPtr]::Zero)
    Start-Sleep -Milliseconds 80
}
[pscustomobject]@{ x = {{X}}; y = {{Y}} } | ConvertTo-Json -Compress
`

// 文字以 base64 嵌入脚本，任何内容都不会被当作 PowerShell 代码解析。
// paste 模式在粘贴后等待目标程序读取剪贴板，再恢复原有文本。
const typeTextScript = `# Yiuk: type_text
` + desktopPrelude + `$text = [System.Text.Encoding]::UTF8.GetString([System.Convert]::FromBase64String('{{TEXT_BASE64}}'))
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
[pscustomobject]@{ characters = $text.Length } | ConvertTo-Json -Compress
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

	c.desktop.remember(input.SessionID, shot.screenGeometry)
	mimeType := "image/png"
	if input.Format == "jpeg" {
		mimeType = "image/jpeg"
	}
	return map[string]any{
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
		SessionID string `json:"session_id"`
		X         int    `json:"x"`
		Y         int    `json:"y"`
		Button    string `json:"button"`
		Double    bool   `json:"double"`
	}
	if err := json.Unmarshal(raw, &input); err != nil {
		return nil, &Error{Code: "INVALID_ARGUMENT", Message: "invalid arguments"}
	}
	if input.Button == "" {
		input.Button = "left"
	}
	geometry, ok := c.desktop.lookup(input.SessionID)
	if !ok {
		return nil, &Error{Code: "SCREENSHOT_REQUIRED", Message: "call take_screenshot for this session first; click coordinates are pixels in the latest screenshot"}
	}
	if input.X >= geometry.Width || input.Y >= geometry.Height {
		return nil, &Error{Code: "INVALID_ARGUMENT", Message: fmt.Sprintf("x/y must be inside the latest screenshot (%dx%d)", geometry.Width, geometry.Height)}
	}
	screenX := geometry.Left + int(math.Round(float64(input.X)*float64(geometry.ScreenWidth)/float64(geometry.Width)))
	screenY := geometry.Top + int(math.Round(float64(input.Y)*float64(geometry.ScreenHeight)/float64(geometry.Height)))
	count := 1
	if input.Double {
		count = 2
	}
	flags := mouseButtonFlags[input.Button]
	script := strings.NewReplacer(
		"{{X}}", strconv.Itoa(screenX), "{{Y}}", strconv.Itoa(screenY), "{{COUNT}}", strconv.Itoa(count),
		"{{DOWN}}", strconv.Itoa(flags[0]), "{{UP}}", strconv.Itoa(flags[1]),
	).Replace(clickScript)
	if _, err := c.runDesktopScript(ctx, input.SessionID, script, clickTimeoutSeconds); err != nil {
		return nil, err
	}
	return map[string]any{
		"session_id": input.SessionID,
		"x":          input.X,
		"y":          input.Y,
		"screen_x":   screenX,
		"screen_y":   screenY,
		"button":     input.Button,
		"double":     input.Double,
	}, nil
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
	if _, err := c.runDesktopScript(ctx, input.SessionID, script, typeTextTimeoutSeconds); err != nil {
		return nil, err
	}
	return map[string]any{
		"session_id":  input.SessionID,
		"characters":  utf8.RuneCountInString(input.Text),
		"method":      input.Method,
		"press_enter": input.PressEnter,
	}, nil
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
