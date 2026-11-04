; Yiuk Device Assist —— Windows x64 安装包脚本（Inno Setup 6.x / 7.x）
;
; 前置：先按 docs/development.md 第 5 节生成绿色目录
;       Release\YiukDeviceAssist-<version>-win64
;
; 编译：ISCC.exe qt-client\installer\YiukDeviceAssist.iss
;       自定义输出目录：ISCC.exe /O"E:\缓存\setup" qt-client\installer\YiukDeviceAssist.iss
;
; 注意：MyAppVersion 必须与 qt-client/CMakeLists.txt 里 project(... VERSION ...) 一致，
; 否则安装包显示的版本和客户端窗口标题（Yiuk Device Assist <version>）会对不上。
;
; 客户端配置不放在安装目录：卸载**不会**删除 %LOCALAPPDATA%\Yiuk\DeviceAssist，
; 因为里面的 identity.json 保存了设备码与 DPAPI 保护的临时密码，保留后重装能沿用
; 同一台设备身份。需要彻底清除时由用户手动删除该目录。

#define MyAppName "Yiuk Device Assist"
#define MyAppVersion "0.1.7"
#define MyAppPublisher "Yiuk"
#define MyAppExeName "yiuk-device-assist.exe"

; 源目录与输出目录都相对本脚本计算，换机器 / CI 不用改绝对路径
#define RepoRoot AddBackslash(SourcePath) + "..\.."
#define SourceDir AddBackslash(RepoRoot) + "Release\YiukDeviceAssist-" + MyAppVersion + "-win64"
#define OutputDir AddBackslash(RepoRoot) + "Release\setup"

[Setup]
; AppId 唯一标识本应用，跨版本必须保持不变，否则升级会变成并排安装。
; {{ 是 Inno 对字面量 { 的转义，向导生成的就是这个形式，末尾不需要再补 }。
AppId={{59006D49-8B0C-44EE-8512-836A6B16DC21}
AppName={#MyAppName}
AppVersion={#MyAppVersion}
AppPublisher={#MyAppPublisher}
DefaultDirName={autopf}\{#MyAppName}
UninstallDisplayIcon={app}\{#MyAppExeName}
; 必须是四段数字，否则 setup.exe 的“文件版本”留空
VersionInfoVersion={#MyAppVersion}.0
VersionInfoProductName={#MyAppName}
VersionInfoCompany={#MyAppPublisher}
; x64compatible：x64 与 Windows 11 on Arm（Arm 下由系统模拟 x64）
ArchitecturesAllowed=x64compatible
ArchitecturesInstallIn64BitMode=x64compatible
; 只支持 Windows 10/11，老系统在安装开始前就被挡住
MinVersion=10.0
DisableProgramGroupPage=yes
PrivilegesRequiredOverridesAllowed=commandline
; 客户端是托盘常驻的：覆盖安装前必须把它关掉，否则 exe 被占用。Restart Manager
; 会发会话结束消息，客户端会正常退出（已实测）。注意静默安装结束后不会自动拉起。
CloseApplications=yes
OutputDir={#OutputDir}
OutputBaseFilename=YiukDeviceAssist-{#MyAppVersion}-setup
SolidCompression=yes
WizardStyle=modern dynamic
; 未做代码签名（下载后 SmartScreen 可能提示）。以后要签名就在这里加 SignTool 与
; SignedUninstaller，并对 Release 目录里的 exe 一并签名。

[Languages]
; 默认只带英文：Inno 自带的 Languages 目录里没有简体中文，需要先从
; https://jrsoftware.org/files/istrans/ 下载 ChineseSimplified.isl 放进 Inno 的
; Languages 目录，再启用下面一行。
;Name: "chinesesimplified"; MessagesFile: "compiler:Languages\ChineseSimplified.isl"
Name: "english"; MessagesFile: "compiler:Default.isl"

[Tasks]
Name: "desktopicon"; Description: "{cm:CreateDesktopIcon}"; GroupDescription: "{cm:AdditionalIcons}"; Flags: unchecked

[Files]
; 一条通配即可覆盖三个 exe、Qt 运行库与 plugins 子目录（platforms/tls/styles/...）
Source: "{#SourceDir}\*"; DestDir: "{app}"; Flags: ignoreversion recursesubdirs createallsubdirs
; 不要对共享系统 DLL 使用 ignoreversion

[Icons]
Name: "{autoprograms}\{#MyAppName}"; Filename: "{app}\{#MyAppExeName}"
Name: "{autodesktop}\{#MyAppName}"; Filename: "{app}\{#MyAppExeName}"; Tasks: desktopicon

[Run]
; runasoriginaluser：否则提权安装后客户端会以管理员身份启动，远程命令在提权上下文里
; 执行；若用户用另一个管理员账户提权，%LOCALAPPDATA% 变化还会凭空多出一台“新设备”。
Filename: "{app}\{#MyAppExeName}"; Description: "{cm:LaunchProgram,{#StringChange(MyAppName, '&', '&&')}}"; Flags: nowait postinstall skipifsilent runasoriginaluser
