// 原生输入/变化检测的真机验收目标窗口（纯 Win32，不依赖 Qt）。
//
// 行为：
//   * 左半区：每次左键按下都把左半区换色（并计数）——用来验证 detect_change 的"确实变了"；
//   * 右半区：鼠标移入会被记录；左键按下只计数，**不改任何像素**——用来验证"没变时不能误报"。
//
// 参数：--expect-clicks N（默认 1）
//   看到右半区移入、且左键按下次数达到 N 时打印 FIXTURE OK 并退出 0；
//   30 秒内没达到就打印 FIXTURE FAIL 并退出 1。
//
// 坐标不在这里算：调用方（yiuk-platform-interop）用 FindWindowW 找到窗口、读 GetWindowRect 自己换算。

#define WIN32_LEAN_AND_MEAN
#include <windows.h>
#include <windowsx.h>

#include <cstdio>
#include <cstdlib>
#include <cstring>

namespace {

constexpr wchar_t kWindowClass[] = L"YiukInputFixtureWindow";
constexpr wchar_t kWindowTitle[] = L"Yiuk 原生输入/变化检测验收（左半区点击会变色，右半区点击不变）";
constexpr UINT_PTR kPollTimer = 1;
constexpr UINT kPollIntervalMs = 100;
constexpr UINT kTimeoutMs = 30000;

bool hoverSeen = false;
bool hoverInLeftHalf = false;
bool clickInRightHalf = false;
bool leftToggled = false;
int clicks = 0;
int expectedClicks = 1;
ULONGLONG startedAt = 0;

void paint(HWND hwnd) {
    // 验收用：把每一次重绘都记下来，便于对照"点击/采样"的先后顺序。
    std::printf("EVENT paint toggled=%d\n", leftToggled ? 1 : 0);
    std::fflush(stdout);
    PAINTSTRUCT ps{};
    HDC dc = BeginPaint(hwnd, &ps);
    RECT client{};
    GetClientRect(hwnd, &client);
    RECT left = client;
    left.right = client.right / 2;
    RECT right = client;
    right.left = client.right / 2;

    // 左半区两种"亮度"相差很大的底色：detect_change 采样的是灰度，
    // 只换色相不换亮度（例如 255,158,96 与 96,158,255 的灰度都是 169）是看不出来的。
    HBRUSH leftBrush = CreateSolidBrush(leftToggled ? RGB(30, 34, 44) : RGB(230, 240, 255));
    HBRUSH rightBrush = CreateSolidBrush(RGB(232, 236, 244));
    FillRect(dc, &left, leftBrush);
    FillRect(dc, &right, rightBrush);
    DeleteObject(leftBrush);
    DeleteObject(rightBrush);

    SetBkMode(dc, TRANSPARENT);
    SetTextColor(dc, RGB(24, 26, 32));
    wchar_t text[256];
    wsprintfW(text, L"变化目标（左）\n点击 %d 次\n%s", clicks, leftToggled ? L"已换色" : L"原始色");
    DrawTextW(dc, text, -1, &left, DT_CENTER | DT_VCENTER | DT_WORDBREAK);
    wsprintfW(text, L"静态目标（右）\n%s\n点击不改任何像素", hoverSeen ? L"已收到鼠标移入" : L"等待鼠标移入");
    DrawTextW(dc, text, -1, &right, DT_CENTER | DT_VCENTER | DT_WORDBREAK);

    EndPaint(hwnd, &ps);
}

void finish(bool ok) {
    std::printf("%s clicks=%d hover=%d hover_in_left=%d click_in_right=%d\n", ok ? "FIXTURE OK" : "FIXTURE FAIL",
                clicks, hoverSeen ? 1 : 0, hoverInLeftHalf ? 1 : 0, clickInRightHalf ? 1 : 0);
    std::fflush(stdout);
    PostQuitMessage(ok ? 0 : 1);
}

LRESULT CALLBACK windowProc(HWND hwnd, UINT message, WPARAM wparam, LPARAM lparam) {
    switch (message) {
        case WM_MOUSEMOVE: {
            RECT client{};
            GetClientRect(hwnd, &client);
            const int x = GET_X_LPARAM(lparam);
            if (x >= client.right / 2) {
                if (!hoverSeen) {
                    hoverSeen = true;
                    std::printf("EVENT hover_right\n");
                    std::fflush(stdout);
                    InvalidateRect(hwnd, nullptr, TRUE);
                }
            } else {
                hoverInLeftHalf = true;
            }
            return 0;
        }
        case WM_LBUTTONDOWN: {
            RECT client{};
            GetClientRect(hwnd, &client);
            const int x = GET_X_LPARAM(lparam);
            if (x < client.right / 2) {
                ++clicks;
                leftToggled = !leftToggled;
                std::printf("EVENT click_left toggled=%d\n", leftToggled ? 1 : 0);
                // 立刻重绘，让"点击后"的采样能看到新颜色，而不是等下一次 WM_PAINT。
                RedrawWindow(hwnd, nullptr, nullptr, RDW_INVALIDATE | RDW_UPDATENOW);
            } else {
                clickInRightHalf = true;
                ++clicks;
                std::printf("EVENT click_right (no visual change)\n");
            }
            std::fflush(stdout);
            return 0;
        }
        case WM_TIMER:
            if (wparam == kPollTimer) {
                if (hoverSeen && clicks >= expectedClicks) {
                    finish(true);
                } else if (GetTickCount64() - startedAt > kTimeoutMs) {
                    finish(false);
                }
                return 0;
            }
            break;
        case WM_PAINT:
            paint(hwnd);
            return 0;
        case WM_DESTROY:
            PostQuitMessage(0);
            return 0;
        default:
            break;
    }
    return DefWindowProcW(hwnd, message, wparam, lparam);
}

}  // namespace

int main(int argc, char **argv) {
    for (int i = 1; i + 1 < argc; ++i) {
        if (std::strcmp(argv[i], "--expect-clicks") == 0) {
            expectedClicks = std::atoi(argv[i + 1]);
            if (expectedClicks < 1) expectedClicks = 1;
        }
    }

    // 与客户端一致：物理像素坐标，避免 125% 缩放下把点击投到别处。
    SetProcessDpiAwarenessContext(DPI_AWARENESS_CONTEXT_PER_MONITOR_AWARE_V2);

    WNDCLASSEXW windowClass{};
    windowClass.cbSize = sizeof(windowClass);
    windowClass.lpfnWndProc = windowProc;
    windowClass.hInstance = GetModuleHandleW(nullptr);
    windowClass.hCursor = LoadCursorW(nullptr, MAKEINTRESOURCEW(32512));  // IDC_ARROW
    windowClass.hbrBackground = reinterpret_cast<HBRUSH>(COLOR_WINDOW + 1);
    windowClass.lpszClassName = kWindowClass;
    windowClass.style = CS_HREDRAW | CS_VREDRAW;
    if (RegisterClassExW(&windowClass) == 0) {
        std::printf("FIXTURE FAIL register_class\n");
        return 1;
    }

    HWND hwnd = CreateWindowExW(WS_EX_TOPMOST | WS_EX_TOOLWINDOW, kWindowClass, kWindowTitle,
                                WS_OVERLAPPEDWINDOW | WS_VISIBLE, 160, 160, 640, 360, nullptr, nullptr,
                                GetModuleHandleW(nullptr), nullptr);
    if (hwnd == nullptr) {
        std::printf("FIXTURE FAIL create_window\n");
        return 1;
    }
    ShowWindow(hwnd, SW_SHOWNOACTIVATE);
    UpdateWindow(hwnd);
    std::printf("FIXTURE READY expect_clicks=%d\n", expectedClicks);
    std::fflush(stdout);

    startedAt = GetTickCount64();
    SetTimer(hwnd, kPollTimer, kPollIntervalMs, nullptr);

    MSG message{};
    BOOL result = 0;
    while ((result = GetMessageW(&message, nullptr, 0, 0)) > 0) {
        TranslateMessage(&message);
        DispatchMessageW(&message);
    }
    return result == 0 ? static_cast<int>(message.wParam) : 0;
}
