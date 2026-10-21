$ErrorActionPreference = 'Stop'
$target = '127.0.0.1:29574'
$cert = New-Object Security.Cryptography.X509Certificates.X509Certificate2 'E:\Yiuk_AgentRemote\tmp-tls\server.crt'
$pin = $cert.GetCertHashString([Security.Cryptography.HashAlgorithmName]::SHA256)
Write-Output ("local platform cert sha256 = " + $pin)

$psi = New-Object Diagnostics.ProcessStartInfo
$psi.FileName = 'E:\Yiuk_AgentRemote\release\YiukDeviceAssist-0.1.1-win64\yiuk-platform-interop.exe'
$psi.WorkingDirectory = 'E:\Yiuk_AgentRemote\release\YiukDeviceAssist-0.1.1-win64'
$psi.UseShellExecute = $false
$psi.CreateNoWindow = $true
$psi.RedirectStandardOutput = $true
$psi.RedirectStandardError = $true
$psi.Environment['YIUK_PLATFORM_ADDRESS'] = $target
$psi.Environment['YIUK_PLATFORM_PASSWORD'] = 'local-tls-password-123456'
$psi.Environment['YIUK_PLATFORM_TLS'] = '1'
$psi.Environment['YIUK_PLATFORM_PIN'] = $pin
$p = New-Object Diagnostics.Process
$p.StartInfo = $psi
[void]$p.Start()
$stdout = $p.StandardOutput.ReadToEnd()
$stderr = $p.StandardError.ReadToEnd()
$p.WaitForExit()
Write-Output ("exit=" + $p.ExitCode)
Write-Output '--- stdout ---'
Write-Output $stdout
Write-Output '--- stderr (qt.* noise filtered) ---'
Write-Output (($stderr -split "`n" | Where-Object { $_ -notmatch '^qt\.' }) -join "`n")
