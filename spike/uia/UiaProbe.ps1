# Yiuk UIA spike: measure the UI Automation tree of one window and the cost
# of resolving elements by name.
#
# ASCII only on purpose: Windows PowerShell 5.1 decodes a script without a BOM
# as ANSI, and non-ASCII comments break parsing (same pitfall as the PowerShell
# scripts embedded in internal/agenttools/desktop.go).
#
# Read-only: never injects input. Results go to -OutFile as UTF-8 JSON.

[CmdletBinding()]
param(
    # Process name of the target app, e.g. notepad, explorer, msedge.
    [Parameter(Mandatory = $true)][string]$ProcessName,
    # Optional substring of the window title to disambiguate several windows.
    [string]$TitleContains = '',
    # control (default), raw or content view.
    [ValidateSet('control', 'raw', 'content')][string]$Walk = 'control',
    # Path to a targets JSON file used for the resolve phase.
    [string]$Targets = '',
    # Hard caps so a pathological tree cannot hang the spike.
    [int]$MaxNodes = 20000,
    [int]$MaxSeconds = 90,
    # How often each resolve query is repeated (measures stability).
    [int]$Repeats = 3,
    # Where to write the result JSON. Required.
    [Parameter(Mandatory = $true)][string]$OutFile,
    # Optional scenario name and TSV summary row (flat, easy to aggregate).
    [string]$ScenarioName = '',
    [string]$SummaryFile = '',
    # Write every named node to the result (bounded by MaxDumpNodes).
    [switch]$DumpNamed,
    [int]$MaxDumpNodes = 1200,
    # Bring the window to the front before the resolve phase, so the FromPoint
    # hit test is not answered by whatever window covers it.
    #   none        do nothing
    #   foreground  AttachThreadInput + SetForegroundWindow
    #   topmost     SetWindowPos(HWND_TOPMOST), foreground not required
    [ValidateSet('none', 'foreground', 'topmost')][string]$ActivateMode = 'none',
    [switch]$Activate
)

$ErrorActionPreference = 'Stop'
Add-Type -AssemblyName UIAutomationClient
Add-Type -AssemblyName UIAutomationTypes
Add-Type -AssemblyName UIAutomationClientsideProviders
# System.Windows.Point lives in WindowsBase and is needed for FromPoint hit tests.
Add-Type -AssemblyName WindowsBase

Add-Type -TypeDefinition @'
using System;
using System.Runtime.InteropServices;
public static class YiukSpike {
    [DllImport("user32.dll")] public static extern bool SetProcessDPIAware();
    [DllImport("user32.dll")] public static extern bool SetProcessDpiAwarenessContext(IntPtr value);
    [DllImport("user32.dll")] public static extern bool SetForegroundWindow(IntPtr hWnd);
    [DllImport("user32.dll")] public static extern bool BringWindowToTop(IntPtr hWnd);
    [DllImport("user32.dll")] public static extern bool ShowWindow(IntPtr hWnd, int cmd);
    [DllImport("user32.dll")] public static extern IntPtr GetForegroundWindow();
    [DllImport("user32.dll")] public static extern uint GetWindowThreadProcessId(IntPtr hWnd, out uint pid);
    [DllImport("user32.dll")] public static extern bool AttachThreadInput(uint attach, uint attachTo, bool attachFlag);
    [DllImport("kernel32.dll")] public static extern uint GetCurrentThreadId();
    [DllImport("user32.dll")] public static extern bool SetWindowPos(IntPtr hWnd, IntPtr after, int x, int y, int cx, int cy, uint flags);
    [DllImport("user32.dll", CharSet = CharSet.Unicode)] public static extern int GetWindowText(IntPtr hWnd, System.Text.StringBuilder text, int count);
    public static string ForegroundTitle() {
        System.Text.StringBuilder text = new System.Text.StringBuilder(512);
        GetWindowText(GetForegroundWindow(), text, text.Capacity);
        return text.ToString();
    }
    // HWND_TOPMOST without moving or resizing. A window that is merely topmost
    // still answers FromPoint even when SetForegroundWindow is refused, which is
    // the case for packaged (UWP/WinUI) and Explorer windows.
    public static bool Topmost(IntPtr hwnd) { return SetWindowPos(hwnd, new IntPtr(-1), 0, 0, 0, 0, 0x0043); }
    public static bool NotTopmost(IntPtr hwnd) { return SetWindowPos(hwnd, new IntPtr(-2), 0, 0, 0, 0, 0x0043); }
    public static void Physical() {
        try { if (SetProcessDpiAwarenessContext(new IntPtr(-4))) return; } catch (EntryPointNotFoundException) {}
        SetProcessDPIAware();
    }
    // SetForegroundWindow alone is refused when the calling process does not
    // own the foreground window; attaching to the foreground thread is the
    // usual way around that restriction.
    public static bool Activate(IntPtr hwnd) {
        ShowWindow(hwnd, 9);
        BringWindowToTop(hwnd);
        uint foregroundPid;
        IntPtr foreground = GetForegroundWindow();
        uint foregroundThread = GetWindowThreadProcessId(foreground, out foregroundPid);
        uint self = GetCurrentThreadId();
        bool attached = foregroundThread != 0 && foregroundThread != self && AttachThreadInput(self, foregroundThread, true);
        bool ok = SetForegroundWindow(hwnd);
        if (attached) { AttachThreadInput(self, foregroundThread, false); }
        return ok;
    }
}
'@
[YiukSpike]::Physical()
if ($Activate) { $ActivateMode = 'foreground' }

$AE = [System.Windows.Automation.AutomationElement]
$deadline = (Get-Date).AddSeconds($MaxSeconds)

function Get-Walker($name) {
    switch ($name) {
        'raw' { return [System.Windows.Automation.TreeWalker]::RawViewWalker }
        'content' { return [System.Windows.Automation.TreeWalker]::ContentViewWalker }
        default { return [System.Windows.Automation.TreeWalker]::ControlViewWalker }
    }
}

function Safe-Read($element) {
    # Reads the cheap properties of one element; returns $null when the element
    # vanished mid-walk (ElementNotAvailableException is normal on live trees).
    try {
        $current = $element.Current
        $rect = $current.BoundingRectangle
        return [pscustomobject]@{
            name = [string]$current.Name
            type = [string]$current.ControlType.ProgrammaticName
            localized = [string]$current.LocalizedControlType
            automationId = [string]$current.AutomationId
            className = [string]$current.ClassName
            framework = [string]$current.FrameworkId
            offscreen = $current.IsOffscreen
            enabled = $current.IsEnabled
            x = [int]$rect.X
            y = [int]$rect.Y
            w = [int]$rect.Width
            h = [int]$rect.Height
        }
    } catch {
        return $null
    }
}

function Find-TargetWindow {
    $root = $AE::RootElement
    $windowCondition = New-Object System.Windows.Automation.PropertyCondition($AE::ControlTypeProperty, [System.Windows.Automation.ControlType]::Window)
    $scanDeadline = (Get-Date).AddSeconds(20)
    while ((Get-Date) -lt $scanDeadline) {
        $windows = $root.FindAll([System.Windows.Automation.TreeScope]::Children, $windowCondition)
        foreach ($window in $windows) {
            try {
                $current = $window.Current
                $process = Get-Process -Id $current.ProcessId -ErrorAction SilentlyContinue
                if ($process -eq $null) { continue }
                if ($process.ProcessName -ne $ProcessName) { continue }
                if ($TitleContains -ne '' -and $current.Name -notlike "*$TitleContains*") { continue }
                return $window
            } catch { }
        }
        Start-Sleep -Milliseconds 400
    }
    throw "no window of process '$ProcessName' with title like '$TitleContains'"
}

$started = Get-Date
$window = Find-TargetWindow
$windowInfo = Safe-Read $window

# ---- phase 1: walk the tree, collect depth/type statistics ----
$walker = Get-Walker $Walk
$nodeCount = 0
$namedCount = 0
$maxDepth = 0
$failures = 0
$depthHistogram = @{}
$typeHistogram = @{}
$named = New-Object System.Collections.ArrayList
$truncatedByNodes = $false
$truncatedByTime = $false

$walkStart = Get-Date
$stack = New-Object System.Collections.Stack
$stack.Push([pscustomobject]@{ element = $window; depth = 0 })
while ($stack.Count -gt 0) {
    if ($nodeCount -ge $MaxNodes) { $truncatedByNodes = $true; break }
    if ((Get-Date) -gt $deadline) { $truncatedByTime = $true; break }

    $item = $stack.Pop()
    $element = $item.element
    $depth = $item.depth
    $info = Safe-Read $element
    if ($info -eq $null) { $failures++; continue }
    $nodeCount++
    if ($depth -gt $maxDepth) { $maxDepth = $depth }
    $key = [string]$depth
    if ($depthHistogram.ContainsKey($key)) { $depthHistogram[$key]++ } else { $depthHistogram[$key] = 1 }
    $typeKey = [string]$info.type
    if ($typeHistogram.ContainsKey($typeKey)) { $typeHistogram[$typeKey]++ } else { $typeHistogram[$typeKey] = 1 }
    if ($info.name -ne '' -and $DumpNamed -and $named.Count -lt $MaxDumpNodes) {
        $namedCount++
        $null = $named.Add([pscustomobject]@{
            depth = $depth
            name = $info.name
            type = $info.type
            automationId = $info.automationId
            className = $info.className
            offscreen = $info.offscreen
            x = $info.x; y = $info.y; w = $info.w; h = $info.h
        })
    }

    try {
        $child = $walker.GetFirstChild($element)
        while ($child -ne $null) {
            $stack.Push([pscustomobject]@{ element = $child; depth = $depth + 1 })
            $child = $walker.GetNextSibling($child)
        }
    } catch {
        $failures++
    }
}
$walkMs = [int]((Get-Date) - $walkStart).TotalMilliseconds

# ---- phase 2: resolve targets by name ----
# UIA has no "name contains" condition, so three strategies are measured:
#   exact       FindFirst(Descendants, name == value [AND controlType == type])
#   enumerate   FindAll(Descendants, controlType or TrueCondition) + local filter
#   walk        TreeWalker traversal + local filter

$foregroundOk = $null
$topmostOk = $null
$foregroundBefore = ''
$foregroundAfter = ''
if ($ActivateMode -ne 'none') {
    # A click is routed to whatever window is on top, so a hit test only means
    # anything once the target window is actually visible where we say it is.
    $handle = [IntPtr]$window.Current.NativeWindowHandle
    $foregroundBefore = [YiukSpike]::ForegroundTitle()
    if ($ActivateMode -eq 'foreground') {
        $foregroundOk = [YiukSpike]::Activate($handle)
    } else {
        $topmostOk = [YiukSpike]::Topmost($handle)
    }
    Start-Sleep -Milliseconds 350
    $foregroundAfter = [YiukSpike]::ForegroundTitle()
}
function Get-MatchScore($info, $value) {
    # Ranking model a real find_element would need, because FindAll returns
    # document order, which is frequently the wrong element (a "star this page"
    # button sorts before the actual toolbar button it is named after).
    # rank: exact name > prefix > substring; then on screen; then enabled;
    # then the smallest area (innermost control, not its container).
    $rank = 0
    if ($info.name -like $value) { $rank = 4 }
    elseif ($info.name -like "$value*") { $rank = 3 }
    elseif ($info.name -like "*$value*") { $rank = 2 }
    $visible = if ($info.offscreen) { 0 } else { 1 }
    $enabled = if ($info.enabled) { 1 } else { 0 }
    $area = [math]::Min(99999, [math]::Abs($info.w * $info.h))
    $lengthPenalty = [math]::Min(99, $info.name.Length)
    return ($rank * 1000000000) + ($visible * 1000000) + ($enabled * 100000) + (100000 - $area) + (99 - $lengthPenalty)
}

function Get-PatternNames($element) {
    # Invoke / Value / SelectionItem make it possible to operate a control
    # without moving the mouse and without the window being on top, which is
    # the alternative to clicking at a rectangle center.
    $names = @()
    if ($element -eq $null) { return $names }
    try {
        foreach ($pattern in $element.GetSupportedPatterns()) {
            $names += $pattern.ProgrammaticName.Replace('PatternIdentifiers.Pattern', '')
        }
    } catch { }
    return $names
}

function Test-PointRelation($element, $info) {
    # Would a click at the center of this element hit it? FromPoint is the
    # cheapest objective check: it asks the same question Windows asks when
    # routing a real click.
    if ($info.w -le 0 -or $info.h -le 0) { return 'empty-rect' }
    $point = New-Object System.Windows.Point([double]($info.x + $info.w / 2), [double]($info.y + $info.h / 2))
    $atPoint = $AE::FromPoint($point)
    if ($atPoint -eq $null) { return 'nothing-at-point' }
    if ([System.Windows.Automation.Automation]::Compare($atPoint, $element)) { return 'self' }
    $pointInfo = Safe-Read $atPoint
    $walker = [System.Windows.Automation.TreeWalker]::RawViewWalker
    $cursor = $walker.GetParent($atPoint)
    $distance = 1
    while ($cursor -ne $null -and $distance -le 8) {
        if ([System.Windows.Automation.Automation]::Compare($cursor, $element)) { return 'inside' }
        $cursor = $walker.GetParent($cursor)
        $distance++
    }
    $script:lastPointInfo = $pointInfo
    return 'other'
}

$results = @()
$script:lastPointInfo = $null
if ($Targets -ne '' -and (Test-Path -LiteralPath $Targets)) {
    $targetList = Get-Content -LiteralPath $Targets -Raw -Encoding UTF8 | ConvertFrom-Json
    foreach ($target in $targetList) {
        $mode = if ($target.mode) { $target.mode } else { 'exact' }
        $controlTypeName = [string]$target.controlType
        $value = [string]$target.value
        $expectName = [string]$target.expectName
        if ($ActivateMode -ne 'none') {
            # Re-assert per target: another window may steal the foreground or
            # the topmost slot between targets, silently invalidating hit tests.
            $handle = [IntPtr]$window.Current.NativeWindowHandle
            if ($ActivateMode -eq 'foreground') {
                $null = [YiukSpike]::Activate($handle)
            } else {
                $null = [YiukSpike]::Topmost($handle)
            }
            Start-Sleep -Milliseconds 120
        }
        $attempts = @()
        for ($i = 1; $i -le $Repeats; $i++) {
            $sw = [System.Diagnostics.Stopwatch]::StartNew()
            $matched = $null
            $best = $null
            $bestScore = -1
            $candidateList = New-Object System.Collections.ArrayList
            $errorText = ''
            try {
                switch ($mode) {
                    'exact' {
                        $conditions = @()
                        $conditions += New-Object System.Windows.Automation.PropertyCondition($AE::NameProperty, $value)
                        if ($controlTypeName) {
                            $conditions += New-Object System.Windows.Automation.PropertyCondition($AE::ControlTypeProperty, [System.Windows.Automation.ControlType]::$controlTypeName)
                        }
                        $condition = if ($conditions.Count -eq 1) { $conditions[0] } else { New-Object System.Windows.Automation.AndCondition($conditions) }
                        $matched = $window.FindFirst([System.Windows.Automation.TreeScope]::Descendants, $condition)
                        $best = $matched
                    }
                    'enumerate' {
                        $condition = if ($controlTypeName) {
                            New-Object System.Windows.Automation.PropertyCondition($AE::ControlTypeProperty, [System.Windows.Automation.ControlType]::$controlTypeName)
                        } else {
                            [System.Windows.Automation.Condition]::TrueCondition
                        }
                        $all = $window.FindAll([System.Windows.Automation.TreeScope]::Descendants, $condition)
                        foreach ($candidate in $all) {
                            $info = Safe-Read $candidate
                            if ($info -eq $null) { continue }
                            if ($info.type -ne '' -and $controlTypeName -and $info.type -ne "ControlType.$controlTypeName") { continue }
                            if ($info.name -notlike "*$value*") { continue }
                            if ($matched -eq $null) { $matched = $candidate }
                            $score = Get-MatchScore $info $value
                            if ($candidateList.Count -lt 20) { $null = $candidateList.Add($info) }
                            if ($score -gt $bestScore) { $bestScore = $score; $best = $candidate }
                        }
                    }
                    'walk' {
                        $w = Get-Walker $Walk
                        $local = New-Object System.Collections.Stack
                        $local.Push($window)
                        $visited = 0
                        while ($local.Count -gt 0 -and $visited -lt $MaxNodes) {
                            $current = $local.Pop()
                            $visited++
                            $info = Safe-Read $current
                            if ($info -ne $null -and $info.name -like "*$value*") {
                                if ($controlTypeName -eq '' -or $info.type -eq "ControlType.$controlTypeName") {
                                    if ($matched -eq $null) { $matched = $current }
                                    $score = Get-MatchScore $info $value
                                    if ($candidateList.Count -lt 20) { $null = $candidateList.Add($info) }
                                    if ($score -gt $bestScore) { $bestScore = $score; $best = $current }
                                }
                            }
                            $childNode = $w.GetFirstChild($current)
                            while ($childNode -ne $null) { $local.Push($childNode); $childNode = $w.GetNextSibling($childNode) }
                        }
                    }
                    default { throw "unknown mode '$mode'" }
                }
            } catch {
                $errorText = $_.Exception.Message
            }
            $sw.Stop()

            $firstInfo = if ($matched) { Safe-Read $matched } else { $null }
            $bestInfo = if ($best) { Safe-Read $best } else { $null }
            $relation = if ($matched) { Test-PointRelation $matched $firstInfo } else { '' }
            $pointInfo = $script:lastPointInfo
            $script:lastPointInfo = $null
            $script:firstPatterns = Get-PatternNames $matched
            $script:bestPatterns = Get-PatternNames $best

            $attempts += [pscustomobject]@{
                ms = [int]$sw.ElapsedMilliseconds
                hit = ($matched -ne $null)
                candidates = $candidateList.Count
                error = $errorText
                name = if ($firstInfo) { $firstInfo.name } else { '' }
                type = if ($firstInfo) { $firstInfo.type } else { '' }
                automationId = if ($firstInfo) { $firstInfo.automationId } else { '' }
                className = if ($firstInfo) { $firstInfo.className } else { '' }
                offscreen = if ($firstInfo) { $firstInfo.offscreen } else { $null }
                enabled = if ($firstInfo) { $firstInfo.enabled } else { $null }
                centerX = if ($firstInfo) { [int]($firstInfo.x + $firstInfo.w / 2) } else { -1 }
                centerY = if ($firstInfo) { [int]($firstInfo.y + $firstInfo.h / 2) } else { -1 }
                w = if ($firstInfo) { $firstInfo.w } else { 0 }
                h = if ($firstInfo) { $firstInfo.h } else { 0 }
                bestName = if ($bestInfo) { $bestInfo.name } else { '' }
                bestType = if ($bestInfo) { $bestInfo.type } else { '' }
                bestAutomationId = if ($bestInfo) { $bestInfo.automationId } else { '' }
                bestClassName = if ($bestInfo) { $bestInfo.className } else { '' }
                bestCenterX = if ($bestInfo) { [int]($bestInfo.x + $bestInfo.w / 2) } else { -1 }
                bestCenterY = if ($bestInfo) { [int]($bestInfo.y + $bestInfo.h / 2) } else { -1 }
                pointRelation = $relation
                pointName = if ($pointInfo) { $pointInfo.name } else { '' }
                pointType = if ($pointInfo) { $pointInfo.type } else { '' }
                candidatesDetail = @($candidateList)
            }
            if ($i -eq 1 -and $candidateList.Count -gt 0) {
                $distinct = @($candidateList | ForEach-Object { $_.name } | Select-Object -Unique)
                if ($mode -ne 'exact') {
                    "  candidates for '$value': " + ($distinct -join ' | ')
                }
            }
        }
        $first = $attempts[0]
        $median = (@($attempts | ForEach-Object { $_.ms }) | Sort-Object)[[int]([math]::Floor($attempts.Count / 2))]
        $results += [pscustomobject]@{
            label = [string]$target.label
            mode = $mode
            value = $value
            controlType = $controlTypeName
            expect = [string]$target.expect
            expectName = $expectName
            hit = $first.hit
            hitCount = @($attempts | Where-Object { $_.hit }).Count
            attempts = $attempts
            candidates = $first.candidates
            msMedian = $median
            msMax = (@($attempts | ForEach-Object { $_.ms }) | Sort-Object)[-1]
            resolvedName = $first.name
            resolvedType = $first.type
            resolvedAutomationId = $first.automationId
            resolvedClassName = $first.className
            centerX = $first.centerX
            centerY = $first.centerY
            resolvedW = $first.w
            resolvedH = $first.h
            bestName = $first.bestName
            bestType = $first.bestType
            bestAutomationId = $first.bestAutomationId
            bestClassName = $first.bestClassName
            bestCenterX = $first.bestCenterX
            bestCenterY = $first.bestCenterY
            pointRelation = $first.pointRelation
            pointName = $first.pointName
            pointType = $first.pointType
            firstPatterns = @($script:firstPatterns)
            bestPatterns = @($script:bestPatterns)
            # Strict accuracy: did the strategy return the element the query
            # actually meant? Empty expectName means "not labelled, judge by eye".
            firstCorrect = if ($expectName) { $first.name -like $expectName } else { $null }
            bestCorrect = if ($expectName) { $first.bestName -like $expectName } else { $null }
            error = $first.error
        }
    }
}

$result = [pscustomobject]@{
    scriptVersion = 2
    timestamp = (Get-Date).ToString('o')
    machine = [pscustomobject]@{
        os = [System.Environment]::OSVersion.VersionString
        ps = $PSVersionTable.PSVersion.ToString()
    }
    target = [pscustomobject]@{
        process = $ProcessName
        titleFilter = $TitleContains
        windowName = $windowInfo.name
        className = $windowInfo.className
        pid = $window.Current.ProcessId
        x = $windowInfo.x; y = $windowInfo.y; w = $windowInfo.w; h = $windowInfo.h
    }
    activation = [pscustomobject]@{
        mode = $ActivateMode
        setForegroundWindow = $foregroundOk
        topmost = $topmostOk
        foregroundBefore = $foregroundBefore
        foregroundAfter = $foregroundAfter
    }
    walk = [pscustomobject]@{
        view = $Walk
        nodeCount = $nodeCount
        namedNodes = $namedCount
        maxDepth = $maxDepth
        failures = $failures
        elapsedMs = $walkMs
        msPerNode = if ($nodeCount -gt 0) { [math]::Round($walkMs / $nodeCount, 3) } else { 0 }
        truncatedByNodes = $truncatedByNodes
        truncatedByTime = $truncatedByTime
        depthHistogram = $depthHistogram
        typeHistogram = $typeHistogram
    }
    named = @($named)
    resolves = @($results)
    totalMs = [int]((Get-Date) - $started).TotalMilliseconds
}

$json = $result | ConvertTo-Json -Depth 8
$directory = Split-Path -Parent $OutFile
if ($directory -and -not (Test-Path -LiteralPath $directory)) {
    $null = New-Item -ItemType Directory -Force -Path $directory
}
[System.IO.File]::WriteAllText($OutFile, $json, (New-Object System.Text.UTF8Encoding($false)))

if ($SummaryFile) {
    # Flat TSV row: Windows PowerShell 5.1 ConvertFrom-Json chokes on some of
    # the large nested results, so the aggregate lives in plain text.
    $row = [pscustomobject]@{
        scenario = $(if ($ScenarioName) { $ScenarioName } else { $ProcessName })
        process = $ProcessName
        view = $Walk
        window = $windowInfo.name
        nodes = $nodeCount
        named = $namedCount
        maxDepth = $maxDepth
        walkMs = $walkMs
        resolves = $results.Count
        hits = @($results | Where-Object { $_.hit }).Count
    }
    $new = -not (Test-Path -LiteralPath $SummaryFile)
    if ($new) {
        $row | Export-Csv -LiteralPath $SummaryFile -Delimiter "`t" -NoTypeInformation -Encoding UTF8
    } else {
        $row | Export-Csv -LiteralPath $SummaryFile -Delimiter "`t" -NoTypeInformation -Encoding UTF8 -Append
    }
}

# ASCII-only console summary so the GBK console cannot garble it.
"process=$ProcessName nodes=$nodeCount maxDepth=$maxDepth walk=$walkMs ms named=$namedCount"
"resolves=" + $results.Count + " hits=" + (@($results | Where-Object { $_.hit }).Count)
foreach ($r in $results) {
    $status = if ($r.hit) { 'HIT ' } else { 'MISS' }
    $correct = if ($r.expectName) { if ($r.firstCorrect) { 'correct' } else { 'WRONG' } } else { 'unlabelled' }
    "  $status $($r.label) [$($r.mode)] value='$($r.value)' ms=$($r.msMedian) cand=$($r.candidates) point=$($r.pointRelation) $correct"
    "       first='$($r.resolvedName)' ($($r.resolvedType))"
    if ($r.mode -ne 'exact') { "       best ='$($r.bestName)' ($($r.bestType))" }
}
"written: $OutFile"
