// 原生输入的真机验收目标窗口（纯 Win32，不依赖 Qt）。
//
// 左半区期望"被左键按下一次"，右半区期望"收到一次鼠标移入"。
// 两个条件都满足就打印 FIXTURE OK 并退出 0；15 秒内没等到就打印 FIXTURE FAIL 并退出 1。
//
// 坐标不在这里算：调用方（yiuk-platform-interop）用 FindWindowW 找到这个窗口，
// 读 GetWindowRect 自己换算，窗口只负责如实记录收到的消息。

#define WIN32_LEAN_AND_MEAN
#include <windows.h>
#include <windowsx.h>

#include <cstdio>

namespace {

constexpr wchar_t kWindowClass[] = L"YiukInputFixtureWindow";
constexpr wchar_t kWindowTitle[] = L"Yiuk 原生输入验收（左半区=点击目标，右半区=悬停目标）";
constexpr UINT_PTR kPollTimer = 1;
constexpr UINT kPollIntervalMs = 100;
constexpr UINT kTimeoutMs = 15000;

bool hoverSeen = false;
bool hoverInLeftHalf = false;
bool clickSeen = false;
bool clickInRightHalf = false;
ULONGLONG startedAt = 0;

void paint(HWND hwnd) {
    PAINTSTRUCT ps{};
    HDC dc = BeginPaint(hwnd, &ps);
    RECT client{};
    GetClientRect(hwnd, &client);
    RECT left = client;
    left.right = client.right / 2;
    RECT right = client;
    right.left = client.right / 2;

    HBRUSH leftBrush = CreateSolidBrush(hoverSeen && !hoverInLeftHalf ? RGB(198, 232, 205) : RGB(232, 236, 244));
    HBRUSH rightBrush = CreateSolidBrush(hoverSeen ? RGB(198, 232, 205) : RGB(232, 236, 244));
    FillRect(dc, &left, leftBrush);
    FillRect(dc, &right, rightBrush);
    DeleteObject(leftBrush);
    DeleteObject(rightBrush);

    SetBkMode(dc, TRANSPARENT);
    SetTextColor(dc, RGB(40, 44, 52));
    wchar_t text[256];
    wsprintfW(text, L"点击目标（左）\n%s", clickSeen ? L"已收到左键按下 ✓" : L"等待左键按下…");
    DrawTextW(dc, text, -1, &left, DT_CENTER | DT_VCENTER | DT_WORDBREAK);
    wsprintfW(text, L"悬停目标（右）\n%s", hoverSeen ? L"已收到鼠标移入 ✓" : L"等待鼠标移入…");
    DrawTextW(dc, text, -1, &right, DT_CENTER | DT_VCENTER | DT_WORDBREAK);

    EndPaint(hwnd, &ps);
}

void finish(bool ok) {
    if (ok) {
        std::printf("FIXTURE OK click=%d hover=%d hover_in_left=%d click_in_right=%d\n", clickSeen ? 1 : 0,
                    hoverSeen ? 1 : 0, hoverInLeftHalf ? 1 : 0, clickInRightHalf ? 1 : 0);
    } else {
        std::printf("FIXTURE FAIL click=%d hover=%d hover_in_left=%d click_in_right=%d\n", clickSeen ? 1 : 0,
                    hoverSeen ? 1 : 0, hoverInLeftHalf ? 1 : 0, clickInRightHalf ? 1 : 0);
    }
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
                if (!hoverSeen) InvalidateRect(hwnd, nullptr, TRUE);
                hoverSeen = true;
            } else {
                // 点击前鼠标也会移到左半区：记录下来，用于区分"悬停"和"点击前的移动"。
                hoverInLeftHalf = true;
            }
            return 0;
        }
        case WM_LBUTTONDOWN: {
            RECT client{};
            GetClientRect(hwnd, &client);
            const int x = GET_X_LPARAM(lparam);
            if (x < client.right / 2) {
                if (!clickSeen) InvalidateRect(hwnd, nullptr, TRUE);
                clickSeen = true;
            } else {
                clickInRightHalf = true;
            }
            return 0;
        }
        case WM_TIMER:
            if (wparam == kPollTimer) {
                if (clickSeen && hoverSeen) {
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

int main() {
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

    // 左上有意留出空间：坐标由调用方读 GetWindowRect，不写死。
    HWND hwnd = CreateWindowExW(WS_EX_TOPMOST | WS_EX_TOOLWINDOW, kWindowClass, kWindowTitle,
                                WS_OVERLAPPEDWINDOW | WS_VISIBLE, 160, 160, 640, 360, nullptr, nullptr,
                                GetModuleHandleW(nullptr), nullptr);
    if (hwnd == nullptr) {
        std::printf("FIXTURE FAIL create_window\n");
        return 1;
    }
    ShowWindow(hwnd, SW_SHOWNOACTIVATE);
    UpdateWindow(hwnd);
    std::printf("FIXTURE READY\n");
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
