$ErrorActionPreference = 'Stop'
$dir = 'E:\Yiuk_AgentRemote\release\YiukDeviceAssist-0.1.1-win64'
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
$psi.Environment['YIUK_PLATFORM_INSECURE'] = '1'
$psi.Environment['QT_ASSUME_STDERR_HAS_CONSOLE'] = '1'
$psi.Environment['QT_LOGGING_RULES'] = 'qt.network.ssl.debug=true;qt.network.ssl.warning=true'
$p = New-Object Diagnostics.Process
$p.StartInfo = $psi
[void]$p.Start()
$stdout = $p.StandardOutput.ReadToEnd()
$stderr = $p.StandardError.ReadToEnd()
$p.WaitForExit()
Write-Output ("exit=" + $p.ExitCode)
Write-Output '--- stderr ---'
Write-Output (($stderr -split "`n" | Where-Object { $_ -notmatch '^qt\.core' -and $_ -notmatch '^\s*$' }) -join "`n")
