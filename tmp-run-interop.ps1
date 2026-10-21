$env:YIUK_PLATFORM_ADDRESS = '103.185.248.64:29564'
$env:YIUK_PLATFORM_PASSWORD = 'Yiuk29564'
$env:YIUK_PLATFORM_TLS = '1'
$env:YIUK_PLATFORM_PIN = '513E9488F55C7DA90EF9349A98909A95E269BCE6B69CF76430C29778524BFC9B'
$env:PATH = 'D:\QT\6.11.1\mingw_64\bin;D:\QT\Tools\mingw1310_64\bin;' + $env:PATH
$exe = 'E:\Yiuk_AgentRemote\qt-client\build\Qt-6.11.1-MinGW-Interop\yiuk-platform-interop.exe'
$out = & $exe 2>&1 | Out-String
Write-Output $out
Write-Output ("exit=" + $LASTEXITCODE)
