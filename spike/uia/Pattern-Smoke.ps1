# Yiuk UIA spike: can a control be operated without moving the mouse?
#
# Resolves one element by name (exact, then scored substring) and operates it
# through its UIA pattern - Invoke for buttons, Value for edits - then reports
# what the window title became as proof that the operation really ran.
#
# ASCII only (Windows PowerShell 5.1 reads a BOM-less script as ANSI).

[CmdletBinding()]
param(
    [string]$ProcessName = 'msedge',
    [string]$TitleContains = 'YiukSpikeFixture',
    # Button label to invoke through InvokePattern.
    [string]$ButtonName = '收藏夹',
    # Edit label whose value is set through ValuePattern.
    [string]$EditName = '在页面中搜索',
    [string]$Value = 'uia-spike',
    # window: search the whole window (browser chrome included).
    # document: search only the page/document subtree, which is how a real
    # find_element avoids returning a browser button instead of a page button.
    [ValidateSet('window', 'document')][string]$Scope = 'document',
    [Parameter(Mandatory = $true)][string]$OutFile
)

$ErrorActionPreference = 'Stop'
Add-Type -AssemblyName UIAutomationClient
Add-Type -AssemblyName UIAutomationTypes
$AE = [System.Windows.Automation.AutomationElement]

function Find-Window {
    $condition = New-Object System.Windows.Automation.PropertyCondition($AE::ControlTypeProperty, [System.Windows.Automation.ControlType]::Window)
    $windows = $AE::RootElement.FindAll([System.Windows.Automation.TreeScope]::Children, $condition)
    foreach ($window in $windows) {
        try {
            $current = $window.Current
            $process = Get-Process -Id $current.ProcessId -ErrorAction SilentlyContinue
            if ($process -eq $null) { continue }
            if ($process.ProcessName -ne $ProcessName) { continue }
            if ($current.Name -notlike "*$TitleContains*") { continue }
            return $window
        } catch { }
    }
    throw "no window of $ProcessName containing '$TitleContains'"
}

$window = Find-Window
$titleBefore = $window.Current.Name

$searchRoot = $window
if ($Scope -eq 'document') {
    $documentCondition = New-Object System.Windows.Automation.PropertyCondition($AE::ControlTypeProperty, [System.Windows.Automation.ControlType]::Document)
    $documents = $window.FindAll([System.Windows.Automation.TreeScope]::Descendants, $documentCondition)
    $largest = $null
    $largestArea = -1
    foreach ($document in $documents) {
        try {
            $rect = $document.Current.BoundingRectangle
            $area = [int]$rect.Width * [int]$rect.Height
            if ($area -gt $largestArea) { $largestArea = $area; $largest = $document }
        } catch { }
    }
    if ($largest -ne $null) { $searchRoot = $largest }
}

# Same scoring rule as UiaProbe.ps1: exact > prefix > substring, then visible,
# then enabled, then the innermost (smallest) element.
function Find-Element($name, $controlTypeName) {
    $condition = New-Object System.Windows.Automation.PropertyCondition($AE::ControlTypeProperty, [System.Windows.Automation.ControlType]::$controlTypeName)
    $all = $searchRoot.FindAll([System.Windows.Automation.TreeScope]::Descendants, $condition)
    $best = $null
    $bestScore = -1
    $seen = 0
    foreach ($candidate in $all) {
        try { $current = $candidate.Current } catch { continue }
        if ($current.Name -notlike "*$name*") { continue }
        $seen++
        $rank = 2
        if ($current.Name -like $name) { $rank = 4 } elseif ($current.Name -like "$name*") { $rank = 3 }
        $rect = $current.BoundingRectangle
        $area = [math]::Min(99999, [math]::Abs([int]$rect.Width * [int]$rect.Height))
        $score = ($rank * 1000000) + $(if ($current.IsOffscreen) { 0 } else { 100000 }) + $(if ($current.IsEnabled) { 10000 } else { 0 }) + (10000 - $area) + (99 - [math]::Min(99, $current.Name.Length))
        if ($score -gt $bestScore) { $bestScore = $score; $best = $candidate }
    }
    return @{ element = $best; candidates = $seen }
}

$result = [pscustomobject]@{
    timestamp = (Get-Date).ToString('o')
    scope = $Scope
    window = $titleBefore
    titleBefore = $titleBefore
    titleAfterInvoke = ''
    invoke = $null
    value = $null
}

$button = Find-Element $ButtonName 'Button'
$result | Add-Member -NotePropertyName invokeCandidates -NotePropertyValue $button.candidates
if ($button.element -ne $null) {
    $info = $button.element.Current
    $patterns = @($button.element.GetSupportedPatterns() | ForEach-Object { $_.ProgrammaticName })
    $pattern = $null
    $invoked = $false
    $errorText = ''
    if ($button.element.TryGetCurrentPattern([System.Windows.Automation.InvokePattern]::Pattern, [ref]$pattern)) {
        try { $pattern.Invoke(); $invoked = $true } catch { $errorText = $_.Exception.Message }
    } else {
        $errorText = 'no InvokePattern'
    }
    Start-Sleep -Milliseconds 600
    $result.invoke = [pscustomobject]@{
        resolvedName = $info.Name
        automationId = $info.AutomationId
        className = $info.ClassName
        patterns = $patterns
        invoked = $invoked
        error = $errorText
    }
    try { $result.titleAfterInvoke = (Find-Window).Current.Name } catch { $result.titleAfterInvoke = '' }
}

$edit = Find-Element $EditName 'Edit'
$result | Add-Member -NotePropertyName editCandidates -NotePropertyValue $edit.candidates
if ($edit.element -ne $null) {
    $info = $edit.element.Current
    $pattern = $null
    $set = $false
    $readBack = ''
    $errorText = ''
    if ($edit.element.TryGetCurrentPattern([System.Windows.Automation.ValuePattern]::Pattern, [ref]$pattern)) {
        try { $pattern.SetValue($Value); $set = $true; $readBack = $pattern.Current.Value } catch { $errorText = $_.Exception.Message }
    } else {
        $errorText = 'no ValuePattern'
    }
    $result.value = [pscustomobject]@{
        resolvedName = $info.Name
        automationId = $info.AutomationId
        className = $info.ClassName
        setValue = $set
        readBack = $readBack
        error = $errorText
    }
}

"window: $($result.window)"
if ($result.invoke) {
    "invoke '$ButtonName' -> resolved '$($result.invoke.resolvedName)' ($($result.invoke.className)) invoked=$($result.invoke.invoked)"
    "title after invoke: $($result.titleAfterInvoke)"
}
if ($result.value) {
    "value  '$EditName' -> resolved '$($result.value.resolvedName)' setValue=$($result.value.setValue) readBack='$($result.value.readBack)'"
}

$json = $result | ConvertTo-Json -Depth 6
[System.IO.File]::WriteAllText($OutFile, $json, (New-Object System.Text.UTF8Encoding($false)))
"written: $OutFile"
