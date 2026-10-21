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
$psi.Environment['YIUK_PLATFORM_PIN'] = '513E9488F55C7DA90EF9349A98909A95E269BCE6B69CF76430C29778524BFC9B'
$psi.Environment['QT_ASSUME_STDERR_HAS_CONSOLE'] = '1'
$psi.Environment['QT_LOGGING_RULES'] = '*.debug=true'
$p = New-Object Diagnostics.Process
$p.StartInfo = $psi
$sw = [Diagnostics.Stopwatch]::StartNew()
[void]$p.Start()
$stdout = $p.StandardOutput.ReadToEnd()
$stderr = $p.StandardError.ReadToEnd()
$p.WaitForExit()
$sw.Stop()
Write-Output ("exit=" + $p.ExitCode + " elapsed_s=" + [math]::Round($sw.Elapsed.TotalSeconds,1))
Write-Output '--- stdout ---'
Write-Output $stdout
Write-Output '--- stderr ---'
Write-Output $stderr
