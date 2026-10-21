$ErrorActionPreference = 'Stop'
$dir = 'E:\Yiuk_AgentRemote\release\YiukDeviceAssist-0.1.1-win64'
$cert = New-Object Security.Cryptography.X509Certificates.X509Certificate2 'E:\Yiuk_AgentRemote\tmp-tls\server.crt'
$pin = $cert.GetCertHashString([Security.Cryptography.HashAlgorithmName]::SHA256)
$psi = New-Object Diagnostics.ProcessStartInfo
$psi.FileName = Join-Path $dir 'yiuk-platform-interop.exe'
$psi.WorkingDirectory = $dir
$psi.UseShellExecute = $false
$psi.CreateNoWindow = $true
$psi.RedirectStandardOutput = $true
$psi.RedirectStandardError = $true
$psi.Environment['YIUK_PLATFORM_ADDRESS'] = '127.0.0.1:29574'
$psi.Environment['YIUK_PLATFORM_PASSWORD'] = 'local-tls-password-123456'
$psi.Environment['YIUK_PLATFORM_TLS'] = '1'
$psi.Environment['YIUK_PLATFORM_PIN'] = $pin
$psi.Environment['QT_ASSUME_STDERR_HAS_CONSOLE'] = '1'
$p = New-Object Diagnostics.Process
$p.StartInfo = $psi
[void]$p.Start()
$stdout = $p.StandardOutput.ReadToEnd()
$stderr = $p.StandardError.ReadToEnd()
$p.WaitForExit()
Write-Output ("exit=" + $p.ExitCode)
Write-Output '--- stdout ---'
Write-Output $stdout
Write-Output '--- stderr (noise filtered) ---'
Write-Output (($stderr -split "`n" | Where-Object { $_ -notmatch '^qt\.' -and $_ -notmatch '^\s*$' }) -join "`n")
