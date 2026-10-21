@echo off
cd /d E:\Yiuk_AgentRemote\release\YiukDeviceAssist-0.1.1-win64
set YIUK_PLATFORM_ADDRESS=103.185.248.64:29564
set YIUK_PLATFORM_PASSWORD=Yiuk29564
set YIUK_PLATFORM_TLS=1
set YIUK_PLATFORM_PIN=513E9488F55C7DA90EF9349A98909A95E269BCE6B69CF76430C29778524BFC9B
yiuk-platform-interop.exe > pout.txt 2>&1
echo exit=%errorlevel%
echo --- output ---
type pout.txt
