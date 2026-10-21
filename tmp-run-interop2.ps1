$env:YIUK_PLATFORM_ADDRESS = '103.185.248.64:29564'
$env:YIUK_PLATFORM_PASSWORD = 'Yiuk29564'
$env:YIUK_PLATFORM_TLS = '1'
$env:YIUK_PLATFORM_PIN = '513E9488F55C7DA90EF9349A98909A95E269BCE6B69CF76430C29778524BFC9B'
$dir = 'E:\Yiuk_AgentRemote\release\YiukDeviceAssist-0.1.1-win64'
Push-Location $dir
$out = & (Join-Path $dir 'yiuk-platform-interop.exe') 2>&1 | Out-String
Pop-Location
Write-Output $out
Write-Output ("exit=" + $LASTEXITCODE)
