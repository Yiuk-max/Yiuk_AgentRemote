$ErrorActionPreference = 'Stop'
$tmp = 'E:\Yiuk_AgentRemote\tmp-verify-appdata'
$root = Join-Path $tmp 'Yiuk\DeviceAssist'
if (Test-Path $tmp) { Remove-Item -Recurse -Force $tmp }
New-Item -ItemType Directory -Force -Path $root | Out-Null
Set-Content -Encoding ASCII -Path (Join-Path $root 'device.json') -Value '{"device_id":"555000111"}'

$player = [Convert]::ToBase64String([Text.Encoding]::UTF8.GetBytes('Yiuk29564'))
$temp = [Convert]::ToBase64String([Text.Encoding]::UTF8.GetBytes('0123456789abcdef0123456789abcdef'))
$cfg = @{
  enabled = $true
  address = '103.185.248.64:29564'
  tls = $true
  allow_insecure = $false
  mode = 'direct'
  password_version = 1
  certificate_fingerprint = '513E9488F55C7DA90EF9349A98909A95E269BCE6B69CF76430C29778524BFC9B'
  player_password = ('plain:' + $player)
  temporary_password = ('plain:' + $temp)
} | ConvertTo-Json -Compress
Set-Content -Encoding ASCII -Path (Join-Path $root 'platform.json') -Value $cfg

$env:APPDATA = $tmp
$env:LOCALAPPDATA = $tmp
$env:QT_QPA_PLATFORM = 'offscreen'
$qt = Start-Process -FilePath 'E:\Yiuk_AgentRemote\qt-client\build\Qt-6.11.1-MinGW-Release\yiuk-device-assist.exe' -PassThru
Start-Sleep -Seconds 10

# Ask the platform (through the pinned MCP backend) whether that device is online.
$mcpRoot = Join-Path $tmp 'Yiuk\DeviceAssist'
$mcpCfg = @{ address='103.185.248.64:29564'; player_password='Yiuk29564'; timeout_seconds=30;
             pinned_cert_sha256='513E9488F55C7DA90EF9349A98909A95E269BCE6B69CF76430C29778524BFC9B' } | ConvertTo-Json -Compress
Set-Content -Encoding ASCII -Path (Join-Path $mcpRoot 'platform-client.json') -Value $mcpCfg

$psi = New-Object Diagnostics.ProcessStartInfo
$psi.FileName = 'E:\Yiuk_AgentRemote\qt-client\build\Qt-6.11.1-MinGW-Release\yiuk-mcp.exe'
$psi.WorkingDirectory = 'E:\Yiuk_AgentRemote'
$psi.UseShellExecute = $false
$psi.RedirectStandardInput = $true
$psi.RedirectStandardOutput = $true
$psi.RedirectStandardError = $true
$psi.CreateNoWindow = $true
$psi.Environment['LOCALAPPDATA'] = $tmp
$psi.Environment['APPDATA'] = $tmp
$mcp = New-Object Diagnostics.Process
$mcp.StartInfo = $psi
[void]$mcp.Start()
function SendMcp($obj) {
  $mcp.StandardInput.WriteLine(($obj | ConvertTo-Json -Compress -Depth 20))
  $mcp.StandardInput.Flush()
  while ($true) {
    $line = $mcp.StandardOutput.ReadLine()
    if ($null -eq $line) { return $null }
    if ($line.Trim().Length -eq 0) { continue }
    $parsed = $line | ConvertFrom-Json
    if ($parsed.id -eq $obj.id) { return $parsed }
  }
}
[void](SendMcp @{ jsonrpc='2.0'; id=1; method='initialize'; params=@{ protocolVersion='2025-06-18'; capabilities=@{}; clientInfo=@{ name='verify'; version='1' } } })
$mcp.StandardInput.WriteLine('{"jsonrpc":"2.0","method":"notifications/initialized","params":{}}')
$mcp.StandardInput.Flush()
$list = SendMcp @{ jsonrpc='2.0'; id=2; method='tools/call'; params=@{ name='list_devices'; arguments=@{} } }
$mcp.StandardInput.Close()
$mcp.WaitForExit()

$qtAlive = -not $qt.HasExited
Stop-Process -Id $qt.Id -Force -ErrorAction SilentlyContinue

$devices = $list.result.structuredContent.data
$target = $devices | Where-Object { $_.device_id -eq '555000111' }
Write-Output ("qt_process_alive = {0}" -f $qtAlive)
if ($null -eq $target) {
  Write-Output "device 555000111 NOT FOUND on platform"
} else {
  Write-Output ("device 555000111 online = {0}, mode = {1}" -f $target.online, $target.mode)
}
