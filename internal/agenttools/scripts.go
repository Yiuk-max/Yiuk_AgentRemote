package agenttools

// 本文件：所有内嵌 PowerShell 原文。

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
$root = $env:YIUK_DATA_DIR
if ([string]::IsNullOrEmpty($root)) { $root = $env:TEMP }
$dir = Join-Path $root 'screenshots'
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

const listDirScript = `# Yiuk: list_dir
$ErrorActionPreference = 'Stop'
$path = [System.Text.Encoding]::UTF8.GetString([System.Convert]::FromBase64String('{{PATH_BASE64}}'))
$limit = {{LIMIT}}
$item = Get-Item -LiteralPath $path -Force -ErrorAction Stop
if (-not $item.PSIsContainer) {
    [pscustomobject]@{ kind = 'not_directory' } | ConvertTo-Json -Compress
    exit 0
}
if ($item.LinkType) {
    [pscustomobject]@{ kind = 'symlink_root'; target = $item.Target } | ConvertTo-Json -Compress
    exit 0
}
$entries = New-Object System.Collections.ArrayList
$truncated = $false
foreach ($child in Get-ChildItem -LiteralPath $path -Force -ErrorAction SilentlyContinue) {
    if ($entries.Count -ge $limit) { $truncated = $true; break }
    $type = 'file'
    if ($child.PSIsContainer) { $type = 'dir' }
    if ($child.LinkType) { $type = 'symlink' }
    $size = $null
    if (-not $child.PSIsContainer) { $size = [long]$child.Length }
    $entry = [pscustomobject]@{
        name = $child.Name
        type = $type
        size = $size
        modified = $child.LastWriteTimeUtc.ToString('o')
        target = $child.Target
    }
    [void]$entries.Add($entry)
}
[pscustomobject]@{ kind = 'ok'; entries = $entries; truncated = $truncated; returned = $entries.Count } | ConvertTo-Json -Depth 4 -Compress
`

const statPathScript = `# Yiuk: stat_path
$ErrorActionPreference = 'Stop'
$path = [System.Text.Encoding]::UTF8.GetString([System.Convert]::FromBase64String('{{PATH_BASE64}}'))
$item = Get-Item -LiteralPath $path -Force -ErrorAction SilentlyContinue
if ($null -eq $item) {
    [pscustomobject]@{ kind = 'missing' } | ConvertTo-Json -Compress
    exit 0
}
$type = 'file'
if ($item.PSIsContainer) { $type = 'dir' }
if ($item.LinkType) { $type = 'symlink' }
$size = $null
if (-not $item.PSIsContainer) { $size = [long]$item.Length }
[pscustomobject]@{
    kind = 'ok'
    type = $type
    size = $size
    modified = $item.LastWriteTimeUtc.ToString('o')
    target = $item.Target
} | ConvertTo-Json -Compress
`
