$ErrorActionPreference = 'Stop'
$dir = 'E:\Yiuk_AgentRemote\release\YiukDeviceAssist-0.1.1-win64'
$outFile = Join-Path $dir 'pout.txt'
$errFile = Join-Path $dir 'perr.txt'
foreach ($f in @($outFile,$errFile)) { if (Test-Path $f) { Remove-Item -Force $f } }
$psi = New-Object Diagnostics.ProcessStartInfo
$psi.FileName = Join-Path $dir 'yiuk-platform-interop.exe'
$psi.WorkingDirectory = $dir
$psi.UseShellExecute = $false
$psi.CreateNoWindow = $true
$psi.RedirectStandardOutput = $true
$psi.RedirectStandardError = $true
$psi.Environment['YIUK_PLATFORM_ADDRESS'] = '103.185.248.64:29564'
$psi.Environment['YIUK_PLATFORM_PASSWORD'] = 'Yiuk29564'
$psi.Environment['YIUK_PLATFORM_TLS'] = '1'
$psi.Environment['YIUK_PLATFORM_PIN'] = '513E9488F55C7DA90EF9349A98909A95E269BCE6B69CF76430C29778524BFC9B'
$p = New-Object Diagnostics.Process
$p.StartInfo = $psi
[void]$p.Start()
$stdout = $p.StandardOutput.ReadToEnd()
$stderr = $p.StandardError.ReadToEnd()
$p.WaitForExit()
Write-Output ("exit=" + $p.ExitCode)
Write-Output '--- stdout ---'
Write-Output $stdout
Write-Output '--- stderr ---'
Write-Output $stderr
