# Yiuk UIA spike driver: make sure each target app has a window, probe it,
# then aggregate the results into one summary.
#
# ASCII only (Windows PowerShell 5.1 reads a BOM-less script as ANSI).

[CmdletBinding()]
param(
    # scenarios.json describing the apps and the elements a real find_element
    # call would have to resolve.
    [string]$ScenarioFile = '',
    # Which scenarios to run; default all.
    [string[]]$Only = @(),
    # Bring the probed window to the foreground before hit testing.
    [ValidateSet('none','foreground','topmost')][string]$ActivateMode = 'foreground',
    # Result directory.
    [string]$OutDir = '',
    [ValidateSet('control', 'raw', 'content')][string]$Walk = 'control',
    [int]$MaxNodes = 20000,
    [int]$MaxSeconds = 60,
    [int]$Repeats = 3,
    # Keep the windows this run opened (default: close only the window we
    # probed, and only when the scenario asks for it).
    [switch]$KeepApps
)

$ErrorActionPreference = 'Stop'
$scriptRoot = Split-Path -Parent $MyInvocation.MyCommand.Path
if (-not $ScenarioFile) { $ScenarioFile = Join-Path $scriptRoot 'scenarios.json' }
if (-not $OutDir) { $OutDir = Join-Path $scriptRoot 'out' }
$probe = Join-Path $scriptRoot 'UiaProbe.ps1'
if (-not (Test-Path -LiteralPath $probe)) { throw "missing $probe" }
if (-not (Test-Path -LiteralPath $OutDir)) { $null = New-Item -ItemType Directory -Force -Path $OutDir }

Add-Type -AssemblyName UIAutomationClient
Add-Type -AssemblyName UIAutomationTypes
$AE = [System.Windows.Automation.AutomationElement]
$SummaryFile = Join-Path $OutDir 'summary.tsv'
if (Test-Path -LiteralPath $SummaryFile) { Remove-Item -LiteralPath $SummaryFile -Force }

function Test-Window($processName, $titleContains) {
    $windowCondition = New-Object System.Windows.Automation.PropertyCondition($AE::ControlTypeProperty, [System.Windows.Automation.ControlType]::Window)
    $windows = $AE::RootElement.FindAll([System.Windows.Automation.TreeScope]::Children, $windowCondition)
    foreach ($window in $windows) {
        try {
            $current = $window.Current
            $process = Get-Process -Id $current.ProcessId -ErrorAction SilentlyContinue
            if ($process -eq $null) { continue }
            if ($process.ProcessName -ne $processName) { continue }
            if ($titleContains -and $current.Name -notlike "*$titleContains*") { continue }
            return $true
        } catch { }
    }
    return $false
}

function Wait-Window($processName, $titleContains, $seconds) {
    $deadline = (Get-Date).AddSeconds($seconds)
    while ((Get-Date) -lt $deadline) {
        if (Test-Window $processName $titleContains) { return $true }
        Start-Sleep -Milliseconds 300
    }
    return $false
}

function Close-TargetWindow($processName, $titleContains) {
    $windowCondition = New-Object System.Windows.Automation.PropertyCondition($AE::ControlTypeProperty, [System.Windows.Automation.ControlType]::Window)
    $windows = $AE::RootElement.FindAll([System.Windows.Automation.TreeScope]::Children, $windowCondition)
    foreach ($window in $windows) {
        try {
            $current = $window.Current
            $process = Get-Process -Id $current.ProcessId -ErrorAction SilentlyContinue
            if ($process -eq $null) { continue }
            if ($process.ProcessName -ne $processName) { continue }
            if ($titleContains -and $current.Name -notlike "*$titleContains*") { continue }
            $pattern = $null
            if ($window.TryGetCurrentPattern([System.Windows.Automation.WindowPattern]::Pattern, [ref]$pattern)) {
                $pattern.Close()
                "closed window: $($current.Name)"
            }
            return
        } catch { }
    }
}

function Start-App($launch, $launchArgs) {
    # {scriptRoot} lets scenarios point at files next to the scripts without
    # hardcoding an absolute path per machine.
    $expanded = @(@($launchArgs) | Where-Object { $_ } | ForEach-Object { ([string]$_).Replace('{scriptRoot}', $scriptRoot) })
    if ($expanded.Count -gt 0) {
        $null = Start-Process -FilePath $launch -ArgumentList $expanded
    } else {
        $null = Start-Process -FilePath $launch
    }
}

$scenarios = Get-Content -LiteralPath $ScenarioFile -Raw -Encoding UTF8 | ConvertFrom-Json

foreach ($scenario in $scenarios) {
    if ($Only.Count -gt 0 -and $Only -notcontains $scenario.name) { continue }

    # A window being alive is enough: the probe only reads it. Never force a
    # restart, because that would close whatever the user has open.
    $started = $false
    $alive = Test-Window $scenario.process ([string]$scenario.titleContains)
    if (-not $alive -and $scenario.launch) {
        "starting: $($scenario.launch)"
        Start-App $scenario.launch $scenario.launchArgs
        $started = $true
    }

    if (-not (Wait-Window $scenario.process ([string]$scenario.titleContains) 25)) {
        "skip: no window for $($scenario.name)"
        continue
    }

    $targetFile = Join-Path $OutDir ("targets-" + $scenario.name + ".json")
    $scenario.targets | ConvertTo-Json -Depth 5 | Set-Content -LiteralPath $targetFile -Encoding UTF8
    $outFile = Join-Path $OutDir ($scenario.name + '.json')

    "probing: $($scenario.name)"
    $arguments = @{
        ProcessName = $scenario.process
        TitleContains = [string]$scenario.titleContains
        Walk = $Walk
        Targets = $targetFile
        MaxNodes = $MaxNodes
        MaxSeconds = $MaxSeconds
        Repeats = $Repeats
        OutFile = $outFile
        ScenarioName = $scenario.name
        SummaryFile = $SummaryFile
    }
    $arguments['ActivateMode'] = $ActivateMode
    if ($scenario.dumpNamed) { $arguments['DumpNamed'] = $true }

    $console = & $probe @arguments
    $console | ForEach-Object { "  $_" }

    # Never kill a user's application and never close a window we did not open:
    # both hold unsaved state. Only clean up after ourselves.
    if ($started -and -not $KeepApps -and $scenario.cleanup -eq 'closeWindow') {
        Close-TargetWindow $scenario.process ([string]$scenario.titleContains)
    }
}

$summaryTable = if (Test-Path -LiteralPath $SummaryFile) { Import-Csv -LiteralPath $SummaryFile -Delimiter "`t" } else { @() }

""
"scenario      view      nodes  named  depth  walkMs  resolves  hits  rate"
foreach ($row in $summaryTable) {
    $rate = if ([int]$row.resolves -gt 0) { [math]::Round(100 * [int]$row.hits / [int]$row.resolves, 1) } else { 0 }
    "{0,-12} {1,-8} {2,6} {3,6} {4,6} {5,7} {6,9} {7,5} {8,5}" -f $row.scenario, $row.view, $row.nodes, $row.named, $row.maxDepth, $row.walkMs, $row.resolves, $row.hits, $rate
}
"written: $SummaryFile (plus one JSON per scenario in $OutDir)"
