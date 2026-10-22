// yiuk-activity-panel: the receive-only log window of the Qt client.
//
// It runs as its own process because the user owns its lifetime: closing it must
// not stop the remote session, and no new activity may pull it back to the front.
// The parent client streams protocol lines to our stdin; everything below turns
// them into one native, per-pixel translucent Win32 window.
#include <windows.h>
#include <shellapi.h>

#include <memory>
#include <string>

#include "panelwindow.h"

namespace {

#ifndef DPI_AWARENESS_CONTEXT_PER_MONITOR_AWARE_V2
#define DPI_AWARENESS_CONTEXT_PER_MONITOR_AWARE_V2 ((DPI_AWARENESS_CONTEXT)-4)
#endif

void EnableDpiAwareness() {
    using SetContextFn = BOOL(WINAPI*)(DPI_AWARENESS_CONTEXT);
    if (auto setContext =
            reinterpret_cast<SetContextFn>(GetProcAddress(GetModuleHandleW(L"user32.dll"),
                                                          "SetProcessDpiAwarenessContext"))) {
        if (setContext(DPI_AWARENESS_CONTEXT_PER_MONITOR_AWARE_V2)) return;
    }
    SetProcessDPIAware();
}

std::wstring Decode(const wchar_t* text) {
    return text ? std::wstring(text) : std::wstring();
}

yiuk::SessionState ParseState(const std::wstring& token) {
    if (token == L"active") return yiuk::SessionState::Active;
    if (token == L"pending") return yiuk::SessionState::Pending;
    if (token == L"closed") return yiuk::SessionState::Closed;
    return yiuk::SessionState::None;
}

yiuk::PanelParams ParseArguments() {
    yiuk::PanelParams params;
    int count = 0;
    LPWSTR* argv = CommandLineToArgvW(GetCommandLineW(), &count);
    if (!argv) return params;
    for (int i = 1; i + 1 < count; i += 2) {
        const std::wstring flag = argv[i];
        const std::wstring value = Decode(argv[i + 1]);
        if (flag == L"--title") params.title = value;
        else if (flag == L"--label") params.label = value;
        else if (flag == L"--status") params.status = value;
        else if (flag == L"--state") params.state = ParseState(value);
    }
    LocalFree(argv);
    return params;
}

}  // namespace

int WINAPI WinMain(HINSTANCE instance, HINSTANCE, LPSTR, int) {
    EnableDpiAwareness();

    Gdiplus::GdiplusStartupInput input;
    ULONG_PTR token = 0;
    if (Gdiplus::GdiplusStartup(&token, &input, nullptr) != Gdiplus::Ok) return 1;

    int exitCode = 0;
    {
        // Kept in a scope so every GDI+ object (fonts, surface) is released
        // before the runtime shuts down.
        auto panel = std::make_unique<yiuk::PanelWindow>(ParseArguments());
        if (!panel->Create(instance)) {
            Gdiplus::GdiplusShutdown(token);
            return 1;
        }
        exitCode = panel->RunMessageLoop();
    }
    Gdiplus::GdiplusShutdown(token);
    return exitCode;
}
