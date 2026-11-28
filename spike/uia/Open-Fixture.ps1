# Opens the UIA fixture in its own Edge window (title contains YiukSpikeFixture)
# so Pattern-Smoke.ps1 has something deterministic to operate on.
# ASCII only.
param(
    [string]$Fixture = (Join-Path (Split-Path -Parent $MyInvocation.MyCommand.Path) 'fixture.html'),
    [int]$WaitSeconds = 6,
    # Close every fixture window instead of opening one.
    [switch]$Close
)
$url = 'file:///' + ($Fixture -replace '\\', '/')

if ($Close) {
    Add-Type -AssemblyName UIAutomationClient
    Add-Type -AssemblyName UIAutomationTypes
    $AE = [System.Windows.Automation.AutomationElement]
    $condition = New-Object System.Windows.Automation.PropertyCondition($AE::ControlTypeProperty, [System.Windows.Automation.ControlType]::Window)
    $windows = $AE::RootElement.FindAll([System.Windows.Automation.TreeScope]::Children, $condition)
    foreach ($window in $windows) {
        try {
            $current = $window.Current
            if ($current.Name -notlike '*YiukSpikeFixture*') { continue }
            $pattern = $null
            if ($window.TryGetCurrentPattern([System.Windows.Automation.WindowPattern]::Pattern, [ref]$pattern)) {
                $pattern.Close()
                'closed: ' + $current.Name
            }
        } catch { }
    }
    return
}

$null = Start-Process -FilePath 'msedge' -ArgumentList '--new-window', $url
$deadline = (Get-Date).AddSeconds($WaitSeconds)
while ((Get-Date) -lt $deadline) {
    $window = Get-Process -Name msedge -ErrorAction SilentlyContinue |
        Where-Object { $_.MainWindowTitle -like '*YiukSpikeFixture*' } |
        Select-Object -First 1
    if ($window) { "opened: " + $window.MainWindowTitle; return }
    Start-Sleep -Milliseconds 300
}
"opened (title not confirmed): $url"
