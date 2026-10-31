#include "panelwindow.h"

#include <windowsx.h>
#include <shlobj.h>

#include <algorithm>
#include <cmath>
#include <cstdio>
#include <cwchar>

#include "panelpipe.h"

namespace yiuk {
namespace {

const wchar_t* kWindowClass = L"YiukActivityPanel";

// 深色且略带冷色调的底板，确保面板在任何桌面壁纸上都清晰可读
// 文字颜色沿用 pi 的深色主题标记：紫色表示会话生命周期，绿色表示命令，蓝色表示读取，兰紫色表示下载，弱化色表示输出，红色表示错误
const Gdiplus::Color kCardFill(233, 20, 23, 30);
const Gdiplus::Color kCardEdge(30, 255, 255, 255);
const Gdiplus::Color kRule(18, 255, 255, 255);
const Gdiplus::Color kTitleInk(235, 238, 245);
const Gdiplus::Color kMutedInk(151, 161, 178);
const Gdiplus::Color kDimInk(107, 117, 134);
const Gdiplus::Color kIconInk(205, 204, 212, 226);

Gdiplus::Color KindColor(Kind kind, BYTE alpha) {
    switch (kind) {
        case Kind::Session: return Gdiplus::Color(alpha, 196, 138, 222);
        case Kind::Execute: return Gdiplus::Color(alpha, 111, 211, 168);
        case Kind::Read: return Gdiplus::Color(alpha, 143, 167, 240);
        case Kind::Download: return Gdiplus::Color(alpha, 214, 152, 226);
        case Kind::Upload: return Gdiplus::Color(alpha, 226, 176, 106);
        case Kind::Error: return Gdiplus::Color(alpha, 239, 142, 114);
        case Kind::Success: return Gdiplus::Color(alpha, 111, 211, 168);
        case Kind::Output: return Gdiplus::Color(alpha, 152, 161, 178);
        case Kind::Info:
        default: return Gdiplus::Color(alpha, 138, 148, 164);
    }
}

// 正文颜色接近类型对应的色相，但会调亮至接近白色，确保充满命令和路径的屏幕仍易于阅读
Gdiplus::Color KindInk(Kind kind) {
    switch (kind) {
        case Kind::Session: return Gdiplus::Color(255, 231, 214, 243);
        case Kind::Execute: return Gdiplus::Color(255, 198, 237, 217);
        case Kind::Read: return Gdiplus::Color(255, 201, 214, 248);
        case Kind::Download: return Gdiplus::Color(255, 239, 210, 246);
        case Kind::Upload: return Gdiplus::Color(255, 252, 238, 214);
        case Kind::Error: return Gdiplus::Color(255, 246, 190, 176);
        case Kind::Success: return Gdiplus::Color(255, 191, 236, 214);
        case Kind::Output: return Gdiplus::Color(255, 157, 167, 184);
        case Kind::Info:
        default: return Gdiplus::Color(255, 146, 156, 173);
    }
}

std::wstring KindLabel(Kind kind) {
    switch (kind) {
        case Kind::Session: return L"会话";
        case Kind::Execute: return L"执行";
        case Kind::Read: return L"读取";
        case Kind::Download: return L"下载";
        case Kind::Upload: return L"上传";
        case Kind::Error: return L"错误";
        case Kind::Success: return L"完成";
        case Kind::Output: return L"输出";
        case Kind::Info:
        default: return L"信息";
    }
}

Gdiplus::Color StateColor(SessionState state, BYTE alpha) {
    switch (state) {
        case SessionState::Active: return Gdiplus::Color(alpha, 111, 211, 168);
        case SessionState::Pending: return Gdiplus::Color(alpha, 217, 200, 90);
        case SessionState::Closed: return Gdiplus::Color(alpha, 138, 148, 164);
        case SessionState::None:
        default: return Gdiplus::Color(alpha, 110, 120, 137);
    }
}

Gdiplus::Color StateInk(SessionState state) {
    switch (state) {
        case SessionState::Active: return Gdiplus::Color(228, 143, 214, 178);
        case SessionState::Pending: return Gdiplus::Color(228, 226, 212, 130);
        case SessionState::Closed: return Gdiplus::Color(224, 150, 160, 176);
        case SessionState::None:
        default: return kDimInk;
    }
}

void AddRoundRect(Gdiplus::GraphicsPath& path, const Gdiplus::RectF& rect, float radius) {
    const float limit = std::min(rect.Width, rect.Height) * 0.5f;
    const float r = std::max(0.0f, std::min(radius, limit));
    if (r <= 0.01f) {
        path.AddRectangle(rect);
        return;
    }
    const float d = r * 2.0f;
    path.AddArc(rect.X, rect.Y, d, d, 180.0f, 90.0f);
    path.AddArc(rect.X + rect.Width - d, rect.Y, d, d, 270.0f, 90.0f);
    path.AddArc(rect.X + rect.Width - d, rect.Y + rect.Height - d, d, d, 0.0f, 90.0f);
    path.AddArc(rect.X, rect.Y + rect.Height - d, d, d, 90.0f, 90.0f);
    path.CloseFigure();
}

void FillRoundRect(Gdiplus::Graphics& graphics, const Gdiplus::RectF& rect, float radius,
                   const Gdiplus::Color& color) {
    Gdiplus::GraphicsPath path;
    AddRoundRect(path, rect, radius);
    Gdiplus::SolidBrush brush(color);
    graphics.FillPath(&brush, &path);
}

std::wstring Unescape(const std::wstring& text) {
    std::wstring out;
    out.reserve(text.size());
    for (size_t i = 0; i < text.size(); ++i) {
        if (text[i] == L'\\' && i + 1 < text.size()) {
            const wchar_t next = text[++i];
            if (next == L'n') out.push_back(L'\n');
            else if (next == L't') out.push_back(L' ');
            else out.push_back(next);
        } else {
            out.push_back(text[i]);
        }
    }
    return out;
}

std::vector<std::wstring> SplitFields(const std::wstring& line, size_t limit) {
    std::vector<std::wstring> fields;
    size_t start = 0;
    while (fields.size() + 1 < limit) {
        const size_t bar = line.find(L'|', start);
        if (bar == std::wstring::npos) break;
        fields.push_back(line.substr(start, bar - start));
        start = bar + 1;
    }
    fields.push_back(line.substr(start));
    return fields;
}

// 缩短标题栏标签，避免较长的操作端名称延伸到窗口按钮下方
std::wstring Elide(const TextMetrics& metrics, const std::wstring& text, int maxWidth) {
    if (maxWidth <= 0) return std::wstring();
    if (metrics.Measure(text) <= maxWidth) return text;
    const std::wstring ellipsis = L"…";
    const int ellipsisWidth = metrics.Measure(ellipsis);
    int width = 0;
    size_t cut = 0;
    while (cut < text.size()) {
        size_t units = 1;
        const int advance = metrics.Advance(text, cut, &units);
        if (width + advance + ellipsisWidth > maxWidth) break;
        width += advance;
        cut += units;
    }
    return text.substr(0, cut) + ellipsis;
}

Kind ParseKind(const std::wstring& token) {
    if (token == L"session") return Kind::Session;
    if (token == L"exec") return Kind::Execute;
    if (token == L"out") return Kind::Output;
    if (token == L"err") return Kind::Error;
    if (token == L"read") return Kind::Read;
    if (token == L"file") return Kind::Download;
    if (token == L"upload") return Kind::Upload;
    if (token == L"ok") return Kind::Success;
    return Kind::Info;
}

SessionState ParseState(const std::wstring& token) {
    if (token == L"active") return SessionState::Active;
    if (token == L"pending") return SessionState::Pending;
    if (token == L"closed") return SessionState::Closed;
    return SessionState::None;
}

UINT QueryDpi(HWND window) {
    using GetDpiForWindowFn = UINT(WINAPI*)(HWND);
    static auto getDpiForWindow =
        reinterpret_cast<GetDpiForWindowFn>(GetProcAddress(GetModuleHandleW(L"user32.dll"), "GetDpiForWindow"));
    if (getDpiForWindow && window) return getDpiForWindow(window);
    HDC dc = GetDC(nullptr);
    const UINT dpi = dc ? static_cast<UINT>(GetDeviceCaps(dc, LOGPIXELSX)) : 96u;
    if (dc) ReleaseDC(nullptr, dc);
    return dpi ? dpi : 96u;
}

std::wstring StatePath() {
    wchar_t folder[MAX_PATH] = {0};
    if (FAILED(SHGetFolderPathW(nullptr, CSIDL_LOCAL_APPDATA | CSIDL_FLAG_CREATE, nullptr,
                                SHGFP_TYPE_CURRENT, folder)))
        return std::wstring();
    std::wstring directory = folder;
    directory += L"\\Yiuk";
    CreateDirectoryW(directory.c_str(), nullptr);
    directory += L"\\DeviceAssist";
    CreateDirectoryW(directory.c_str(), nullptr);
    directory += L"\\activity-panel.state";
    return directory;
}

// 日志中不显示制表符字形，因此条目中出现制表符时会将其扩展为空格
std::wstring ExpandTabs(const std::wstring& text) {
    if (text.find(L'\t') == std::wstring::npos) return text;
    std::wstring out;
    out.reserve(text.size() + 8);
    for (const wchar_t ch : text) {
        if (ch == L'\t') out.append(L"    ");
        else out.push_back(ch);
    }
    return out;
}

}  // namespace

PanelWindow::PanelWindow(PanelParams panelParams) : params(std::move(panelParams)) {}

PanelWindow::~PanelWindow() {
    surface.reset();
    // DIB 仍选入内存 DC，因此先释放 DC
    if (memoryDC) DeleteDC(memoryDC);
    if (dib) DeleteObject(dib);
}

bool PanelWindow::Create(HINSTANCE instance) {
    dpi = static_cast<int>(QueryDpi(nullptr));
    LoadState();

    WNDCLASSEXW cls{};
    cls.cbSize = sizeof cls;
    cls.style = CS_DBLCLKS | CS_HREDRAW | CS_VREDRAW;
    cls.lpfnWndProc = &PanelWindow::WndProc;
    cls.hInstance = instance;
    cls.hCursor = LoadCursorW(nullptr, IDC_ARROW);
    cls.lpszClassName = kWindowClass;
    if (!RegisterClassExW(&cls) && GetLastError() != ERROR_CLASS_ALREADY_EXISTS) return false;

    const int width = initialRect.right - initialRect.left;
    const int height = initialRect.bottom - initialRect.top;
    window = CreateWindowExW(WS_EX_LAYERED, kWindowClass, params.title.c_str(), WS_POPUP,
                             initialRect.left, initialRect.top, width, height, nullptr, nullptr, instance,
                             this);
    if (!window) return false;

    dpi = static_cast<int>(QueryDpi(window));
    GetWindowRect(window, &restoreRect);
    UpdateMetrics();
    Relayout();
    Present();
    ShowWindow(window, SW_SHOW);
    StartStdinReader(window);
    return true;
}

int PanelWindow::RunMessageLoop() {
    MSG message{};
    int result = 0;
    while (true) {
        const BOOL status = GetMessageW(&message, nullptr, 0, 0);
        if (status <= 0) {
            result = static_cast<int>(message.wParam);
            break;
        }
        TranslateMessage(&message);
        DispatchMessageW(&message);
    }
    return result;
}

LRESULT CALLBACK PanelWindow::WndProc(HWND window, UINT message, WPARAM wparam, LPARAM lparam) {
    PanelWindow* self = nullptr;
    if (message == WM_NCCREATE) {
        auto* create = reinterpret_cast<CREATESTRUCTW*>(lparam);
        self = static_cast<PanelWindow*>(create->lpCreateParams);
        SetWindowLongPtrW(window, GWLP_USERDATA, reinterpret_cast<LONG_PTR>(self));
        self->window = window;
    } else {
        self = reinterpret_cast<PanelWindow*>(GetWindowLongPtrW(window, GWLP_USERDATA));
    }
    if (!self) return DefWindowProcW(window, message, wparam, lparam);
    return self->Handle(message, wparam, lparam);
}

LRESULT PanelWindow::Handle(UINT message, WPARAM wparam, LPARAM lparam) {
    switch (message) {
        case WM_NCHITTEST: {
            POINT point{GET_X_LPARAM(lparam), GET_Y_LPARAM(lparam)};
            ScreenToClient(window, &point);
            // 柔和投影绘制在分层位图中；投影区域的点击应传递给下方窗口
            return CardContains(point) ? HTCLIENT : HTTRANSPARENT;
        }
        case WM_ERASEBKGND:
            return 1;
        case WM_PAINT: {
            PAINTSTRUCT paint{};
            BeginPaint(window, &paint);
            EndPaint(window, &paint);
            Present();
            return 0;
        }
        case WM_SIZE:
        case WM_MOVE: {
            if (!fullscreen && window) GetWindowRect(window, &restoreRect);
            if (message == WM_SIZE) {
                UpdateMetrics();
                Relayout();
                InvalidateRect(window, nullptr, FALSE);
            }
            return 0;
        }
        case WM_MOUSEMOVE: {
            const POINT point{GET_X_LPARAM(lparam), GET_Y_LPARAM(lparam)};
            mouse = point;
            if (!trackingMouse) {
                TRACKMOUSEEVENT track{sizeof track, TME_LEAVE, window, 0};
                trackingMouse = TrackMouseEvent(&track) != FALSE;
            }
            if (drag == Drag::Move) {
                POINT now{};
                GetCursorPos(&now);
                SetWindowPos(window, nullptr, dragRect.left + (now.x - dragOrigin.x),
                             dragRect.top + (now.y - dragOrigin.y), 0, 0,
                             SWP_NOSIZE | SWP_NOZORDER | SWP_NOACTIVATE);
                return 0;
            }
            if (drag == Drag::Resize) {
                POINT now{};
                GetCursorPos(&now);
                const int dx = now.x - dragOrigin.x;
                const int dy = now.y - dragOrigin.y;
                RECT rect = dragRect;
                const int minWidth = Scale(420);
                const int minHeight = Scale(260);
                if (dragZone & 1) rect.left = std::min(rect.left + dx, rect.right - minWidth);
                if (dragZone & 2) rect.right = std::max(rect.right + dx, rect.left + minWidth);
                if (dragZone & 4) rect.top = std::min(rect.top + dy, rect.bottom - minHeight);
                if (dragZone & 8) rect.bottom = std::max(rect.bottom + dy, rect.top + minHeight);
                SetWindowPos(window, nullptr, rect.left, rect.top, rect.right - rect.left,
                             rect.bottom - rect.top, SWP_NOZORDER | SWP_NOACTIVATE);
                return 0;
            }
            if (draggingScroll) {
                Gdiplus::RectF thumb;
                if (ScrollBarThumb(&thumb)) {
                    const Gdiplus::RectF bodyRect = BodyRect();
                    const float trackTop = bodyRect.Y + metrics.padT;
                    const float trackHeight = static_cast<float>(BodyHeight());
                    const float span = trackHeight - thumb.Height;
                    if (span > 1.0f) {
                        const float offset = static_cast<float>(point.y) - trackTop - scrollGrab;
                        const float ratio = std::max(0.0f, std::min(1.0f, offset / span));
                        follow = ratio >= 0.999f;
                        ScrollTo(static_cast<int>(std::lround(ratio * MaxScroll())));
                    }
                }
                return 0;
            }
            const int button = ButtonAt(point);
            if (button != hoverButton) {
                hoverButton = button;
                InvalidateRect(window, nullptr, FALSE);
            }
            UpdateCursor();
            return 0;
        }
        case WM_MOUSELEAVE:
            trackingMouse = false;
            mouse = POINT{-1, -1};
            if (hoverButton != -1) {
                hoverButton = -1;
                InvalidateRect(window, nullptr, FALSE);
            }
            SetCursor(LoadCursorW(nullptr, IDC_ARROW));
            return 0;
        case WM_LBUTTONDOWN: {
            SetFocus(window);
            const POINT point{GET_X_LPARAM(lparam), GET_Y_LPARAM(lparam)};
            mouse = point;
            const int button = ButtonAt(point);
            if (button >= 0) {
                pressedButton = button;
                SetCapture(window);
                InvalidateRect(window, nullptr, FALSE);
                return 0;
            }
            Gdiplus::RectF thumb;
            if (ScrollBarThumb(&thumb) && thumb.Contains(static_cast<Gdiplus::REAL>(point.x),
                                                         static_cast<Gdiplus::REAL>(point.y))) {
                draggingScroll = true;
                scrollGrab = static_cast<float>(point.y) - thumb.Y;
                SetCapture(window);
                return 0;
            }
            const int zone = ResizeZone(point);
            if (zone) {
                drag = Drag::Resize;
                dragZone = zone;
            } else if (point.y <= metrics.shadow + metrics.header) {
                drag = Drag::Move;
                dragZone = 0;
            } else {
                return 0;
            }
            GetCursorPos(&dragOrigin);
            GetWindowRect(window, &dragRect);
            SetCapture(window);
            UpdateCursor();
            return 0;
        }
        case WM_LBUTTONUP: {
            const POINT point{GET_X_LPARAM(lparam), GET_Y_LPARAM(lparam)};
            if (pressedButton >= 0) {
                const int button = pressedButton;
                pressedButton = -1;
                ReleaseCapture();
                if (ButtonAt(point) == button) PerformButton(button);
                InvalidateRect(window, nullptr, FALSE);
                return 0;
            }
            if (drag != Drag::None || draggingScroll) {
                const bool moved = drag == Drag::Move || drag == Drag::Resize;
                drag = Drag::None;
                dragZone = 0;
                draggingScroll = false;
                ReleaseCapture();
                if (moved) SaveState();
                return 0;
            }
            return 0;
        }
        case WM_LBUTTONDBLCLK: {
            const POINT point{GET_X_LPARAM(lparam), GET_Y_LPARAM(lparam)};
            if (ButtonAt(point) < 0 && point.y <= metrics.shadow + metrics.header) ToggleFullscreen();
            return 0;
        }
        case WM_MOUSEWHEEL: {
            const int delta = GET_WHEEL_DELTA_WPARAM(wparam);
            const int step = metrics.lineHeight * 3;
            ScrollTo(scroll - (delta / WHEEL_DELTA) * step);
            InvalidateRect(window, nullptr, FALSE);
            return 0;
        }
        case WM_SETCURSOR:
            if (LOWORD(lparam) == HTCLIENT) UpdateCursor();
            return TRUE;
        case WM_KEYDOWN:
            if (wparam == VK_ESCAPE && fullscreen) ToggleFullscreen();
            return 0;
        case WM_DPICHANGED: {
            dpi = static_cast<int>(HIWORD(wparam));
            const auto* suggested = reinterpret_cast<const RECT*>(lparam);
            SetWindowPos(window, nullptr, suggested->left, suggested->top,
                         suggested->right - suggested->left, suggested->bottom - suggested->top,
                         SWP_NOZORDER | SWP_NOACTIVATE);
            UpdateMetrics();
            Relayout();
            InvalidateRect(window, nullptr, FALSE);
            return 0;
        }
        case WM_CAPTURECHANGED:
            drag = Drag::None;
            dragZone = 0;
            draggingScroll = false;
            pressedButton = -1;
            return 0;
        case kMessageLine: {
            auto* payload = reinterpret_cast<std::wstring*>(wparam);
            if (payload) {
                HandleProtocolLine(*payload);
                delete payload;
            }
            return 0;
        }
        case kMessageClosed:
            // 为面板提供数据的客户端已退出，面板没有可继续显示的内容，因此一并关闭
            PostMessageW(window, WM_CLOSE, 0, 0);
            return 0;
        case WM_CLOSE:
            SaveState();
            DestroyWindow(window);
            return 0;
        case WM_DESTROY:
            PostQuitMessage(0);
            return 0;
        default:
            break;
    }
    return DefWindowProcW(window, message, wparam, lparam);
}

void PanelWindow::HandleProtocolLine(const std::wstring& line) {
    if (line.empty()) return;
    const std::vector<std::wstring> fields = SplitFields(line, 5);
    switch (line[0]) {
        case L'H': {
            if (fields.size() >= 5) {
                if (!fields[1].empty()) params.title = fields[1];
                params.label = fields[2];
                params.state = ParseState(fields[3]);
                params.status = fields[4];
            }
            InvalidateRect(window, nullptr, FALSE);
            return;
        }
        case L'S': {
            if (fields.size() >= 3) {
                params.state = ParseState(fields[1]);
                params.status = fields[2];
                InvalidateRect(window, nullptr, FALSE);
            }
            return;
        }
        case L'R': {
            entries.clear();
            lines.clear();
            contentHeight = 0;
            scroll = 0;
            follow = true;
            Relayout();
            InvalidateRect(window, nullptr, FALSE);
            return;
        }
        case L'E': {
            if (fields.size() >= 4) {
                Entry entry;
                entry.kind = ParseKind(fields[1]);
                entry.time = fields[2];
                entry.text = ExpandTabs(Unescape(fields[3]));
                AppendEntry(std::move(entry));
            }
            return;
        }
        case L'A': {
            if (IsIconic(window)) ShowWindow(window, SW_RESTORE);
            SetForegroundWindow(window);
            return;
        }
        default:
            return;
    }
}

void PanelWindow::AppendEntry(Entry entry) {
    entries.push_back(std::move(entry));
    while (entries.size() > 600) entries.erase(entries.begin());
    UpdateMetrics();
    Relayout();
    InvalidateRect(window, nullptr, FALSE);
}

int PanelWindow::Scale(int value) const {
    return MulDiv(value, dpi, 96);
}

void PanelWindow::UpdateMetrics() {
    Metrics next;
    next.shadow = Scale(18);
    next.header = Scale(42);
    next.footer = Scale(28);
    next.padL = Scale(16);
    next.padR = Scale(14);
    next.padT = Scale(10);
    next.padB = Scale(8);
    next.timeW = Scale(52);
    next.barW = Scale(2);
    next.labelW = Scale(28);
    next.gap = Scale(6);
    next.lineHeight = Scale(20);
    next.entryGap = Scale(6);
    next.radius = Scale(14);
    next.scrollbar = Scale(4);
    next.buttonW = Scale(30);
    next.buttonH = Scale(24);
    next.buttonGap = Scale(4);
    next.padding = Scale(8);
    next.textX = next.padL + next.timeW + next.gap + next.barW + Scale(8) + next.labelW + Scale(10);
    metrics = next;

    body.Init(static_cast<float>(Scale(13)), false);
    bodyStrong.Init(static_cast<float>(Scale(13)), true);
    small.Init(static_cast<float>(Scale(11)), false);
}

Gdiplus::RectF PanelWindow::CardRect() const {
    RECT client{0, 0, 0, 0};
    if (window) GetClientRect(window, &client);
    const int shadow = metrics.shadow;
    const float width = static_cast<float>(std::max(0, static_cast<int>(client.right) - 2 * shadow));
    const float height = static_cast<float>(std::max(0, static_cast<int>(client.bottom) - 2 * shadow));
    return Gdiplus::RectF(static_cast<Gdiplus::REAL>(shadow), static_cast<Gdiplus::REAL>(shadow), width,
                          height);
}

Gdiplus::RectF PanelWindow::BodyRect() const {
    const Gdiplus::RectF card = CardRect();
    const float top = card.Y + metrics.header;
    const float height = std::max(0.0f, card.Height - metrics.header - metrics.footer);
    return Gdiplus::RectF(card.X, top, card.Width, height);
}

int PanelWindow::BodyHeight() const {
    const Gdiplus::RectF bodyRect = BodyRect();
    return std::max(0, static_cast<int>(bodyRect.Height) - metrics.padT - metrics.padB);
}

int PanelWindow::ContentWidth() const {
    const Gdiplus::RectF card = CardRect();
    const int right = static_cast<int>(card.X + card.Width) - metrics.padR - metrics.scrollbar - metrics.padding;
    const int left = static_cast<int>(card.X) + metrics.textX;
    return std::max(Scale(80), right - left);
}

int PanelWindow::MaxScroll() const {
    return std::max(0, contentHeight - BodyHeight());
}

void PanelWindow::Relayout() {
    lines.clear();
    contentHeight = 0;
    const int available = ContentWidth();
    for (int i = 0; i < static_cast<int>(entries.size()); ++i) WrapEntry(i, available);
    for (size_t i = 0; i < lines.size(); ++i) {
        if (lines[i].first && i > 0) contentHeight += metrics.entryGap;
        contentHeight += metrics.lineHeight;
    }
    if (follow) scroll = MaxScroll();
    scroll = std::max(0, std::min(scroll, MaxScroll()));
}

void PanelWindow::WrapEntry(int index, int available) {
    const std::wstring& text = entries[index].text;
    const int width = std::max(available, Scale(40));
    bool first = true;
    size_t pos = 0;
    for (;;) {
        size_t eol = text.find(L'\n', pos);
        if (eol == std::wstring::npos) eol = text.size();
        size_t begin = pos;
        if (begin >= eol) {
            lines.push_back({index, static_cast<int>(begin), static_cast<int>(begin), first});
            first = false;
        }
        while (begin < eol) {
            int used = 0;
            size_t cursor = begin;
            size_t lastSpace = std::wstring::npos;
            while (cursor < eol) {
                size_t units = 1;
                const int advance = body.Advance(text, cursor, &units);
                if (used + advance > width && cursor > begin) break;
                used += advance;
                if (text[cursor] == L' ') lastSpace = cursor;
                cursor += units;
                if (used >= width) break;
            }
            size_t end = cursor;
            if (cursor < eol && lastSpace != std::wstring::npos && lastSpace > begin) end = lastSpace;
            if (end <= begin) end = begin + 1;
            lines.push_back({index, static_cast<int>(begin), static_cast<int>(end), first});
            first = false;
            begin = end;
            while (begin < eol && text[begin] == L' ') ++begin;
        }
        if (eol >= text.size()) break;
        pos = eol + 1;
    }
}

void PanelWindow::ScrollTo(int value) {
    const int clamped = std::max(0, std::min(value, MaxScroll()));
    follow = clamped >= MaxScroll();
    if (clamped == scroll) return;
    scroll = clamped;
    InvalidateRect(window, nullptr, FALSE);
}

bool PanelWindow::CardContains(POINT point) const {
    const Gdiplus::RectF card = CardRect();
    return point.x >= card.X && point.y >= card.Y && point.x <= card.X + card.Width &&
        point.y <= card.Y + card.Height;
}

void PanelWindow::ButtonRects(Gdiplus::RectF out[3]) const {
    const Gdiplus::RectF card = CardRect();
    const float top = card.Y + (metrics.header - metrics.buttonH) * 0.5f;
    float x = card.X + card.Width - Scale(10);
    for (int i = 2; i >= 0; --i) {
        x -= metrics.buttonW;
        out[i] = Gdiplus::RectF(x, top, static_cast<Gdiplus::REAL>(metrics.buttonW),
                                static_cast<Gdiplus::REAL>(metrics.buttonH));
        x -= metrics.buttonGap;
    }
}

int PanelWindow::ButtonAt(POINT point) const {
    Gdiplus::RectF buttons[3];
    ButtonRects(buttons);
    for (int i = 0; i < 3; ++i) {
        if (buttons[i].Contains(static_cast<Gdiplus::REAL>(point.x), static_cast<Gdiplus::REAL>(point.y)))
            return i;
    }
    return -1;
}

int PanelWindow::ResizeZone(POINT point) const {
    const Gdiplus::RectF card = CardRect();
    const int edge = Scale(7);
    int zone = 0;
    if (std::abs(point.x - static_cast<int>(card.X)) <= edge) zone |= 1;
    if (std::abs(point.x - static_cast<int>(card.X + card.Width)) <= edge) zone |= 2;
    if (std::abs(point.y - static_cast<int>(card.Y)) <= edge) zone |= 4;
    if (std::abs(point.y - static_cast<int>(card.Y + card.Height)) <= edge) zone |= 8;
    return zone;
}

bool PanelWindow::ScrollBarThumb(Gdiplus::RectF* thumb) const {
    const int visible = BodyHeight();
    if (contentHeight <= visible || visible <= 0) return false;
    const Gdiplus::RectF bodyRect = BodyRect();
    const Gdiplus::RectF card = CardRect();
    const float trackTop = bodyRect.Y + metrics.padT;
    const float trackHeight = static_cast<float>(visible);
    const float height = std::max(static_cast<float>(Scale(28)),
                                  trackHeight * static_cast<float>(visible) / static_cast<float>(contentHeight));
    const int maxScroll = MaxScroll();
    const float ratio = maxScroll > 0 ? static_cast<float>(scroll) / static_cast<float>(maxScroll) : 0.0f;
    if (thumb) {
        *thumb = Gdiplus::RectF(card.X + card.Width - metrics.padR - metrics.scrollbar,
                                trackTop + (trackHeight - height) * ratio,
                                static_cast<Gdiplus::REAL>(metrics.scrollbar), height);
    }
    return true;
}

void PanelWindow::UpdateHover(POINT point) {
    const int button = ButtonAt(point);
    if (button != hoverButton) {
        hoverButton = button;
        InvalidateRect(window, nullptr, FALSE);
    }
}

void PanelWindow::UpdateCursor() {
    HCURSOR wanted = LoadCursorW(nullptr, IDC_ARROW);
    if (drag == Drag::Resize) {
        switch (dragZone) {
            case 1: case 2: wanted = LoadCursorW(nullptr, IDC_SIZEWE); break;
            case 4: case 8: wanted = LoadCursorW(nullptr, IDC_SIZENS); break;
            case 5: case 10: wanted = LoadCursorW(nullptr, IDC_SIZENWSE); break;
            case 6: case 9: wanted = LoadCursorW(nullptr, IDC_SIZENESW); break;
            default: break;
        }
    } else if (drag == Drag::Move) {
        wanted = LoadCursorW(nullptr, IDC_SIZEALL);
    } else {
        const int zone = ResizeZone(mouse);
        switch (zone) {
            case 1: case 2: wanted = LoadCursorW(nullptr, IDC_SIZEWE); break;
            case 4: case 8: wanted = LoadCursorW(nullptr, IDC_SIZENS); break;
            case 5: case 10: wanted = LoadCursorW(nullptr, IDC_SIZENWSE); break;
            case 6: case 9: wanted = LoadCursorW(nullptr, IDC_SIZENESW); break;
            default: break;
        }
        if (ButtonAt(mouse) >= 0) wanted = LoadCursorW(nullptr, IDC_ARROW);
    }
    if (wanted != cursor) {
        cursor = wanted;
        SetCursor(cursor);
    }
}

void PanelWindow::PerformButton(int id) {
    switch (id) {
        case 0: ShowWindow(window, SW_MINIMIZE); break;
        case 1: ToggleFullscreen(); break;
        case 2: PostMessageW(window, WM_CLOSE, 0, 0); break;
        default: break;
    }
}

void PanelWindow::ToggleFullscreen() {
    if (!fullscreen) {
        GetWindowRect(window, &restoreRect);
        // 调整大小前先切换标志，避免 WM_SIZE/WM_MOVE 覆盖退出全屏后要恢复的矩形区域
        fullscreen = true;
        MONITORINFO info{};
        info.cbSize = sizeof info;
        if (GetMonitorInfoW(MonitorFromWindow(window, MONITOR_DEFAULTTONEAREST), &info)) {
            SetWindowPos(window, nullptr, info.rcWork.left, info.rcWork.top,
                         info.rcWork.right - info.rcWork.left, info.rcWork.bottom - info.rcWork.top,
                         SWP_NOZORDER | SWP_NOACTIVATE);
        }
    } else {
        fullscreen = false;
        SetWindowPos(window, nullptr, restoreRect.left, restoreRect.top,
                     restoreRect.right - restoreRect.left, restoreRect.bottom - restoreRect.top,
                     SWP_NOZORDER | SWP_NOACTIVATE);
        SaveState();
    }
    InvalidateRect(window, nullptr, FALSE);
}

bool PanelWindow::EnsureSurface(int width, int height) {
    if (dib && width == surfaceWidth && height == surfaceHeight && surface) return true;
    surface.reset();
    // 同时重新创建内存 DC：删除仍选入 DC 的位图会静默失败，导致每次调整大小都泄漏一个 DIB
    if (memoryDC) {
        DeleteDC(memoryDC);
        memoryDC = nullptr;
    }
    if (dib) {
        DeleteObject(dib);
        dib = nullptr;
    }
    memoryDC = CreateCompatibleDC(nullptr);
    if (!memoryDC) return false;
    BITMAPINFO info{};
    info.bmiHeader.biSize = sizeof(BITMAPINFOHEADER);
    info.bmiHeader.biWidth = width;
    info.bmiHeader.biHeight = -height;
    info.bmiHeader.biPlanes = 1;
    info.bmiHeader.biBitCount = 32;
    info.bmiHeader.biCompression = BI_RGB;
    void* pixels = nullptr;
    dib = CreateDIBSection(memoryDC, &info, DIB_RGB_COLORS, &pixels, nullptr, 0);
    if (!dib || !pixels) return false;
    SelectObject(memoryDC, dib);
    surface = std::make_unique<Gdiplus::Bitmap>(width, height, width * 4, PixelFormat32bppPARGB,
                                               static_cast<BYTE*>(pixels));
    if (surface->GetLastStatus() != Gdiplus::Ok) {
        surface.reset();
        return false;
    }
    surfaceWidth = width;
    surfaceHeight = height;
    return true;
}

bool PanelWindow::Present() {
    if (!window) return false;
    RECT client{};
    GetClientRect(window, &client);
    const int width = client.right - client.left;
    const int height = client.bottom - client.top;
    if (width <= 0 || height <= 0) return false;
    if (!EnsureSurface(width, height)) return false;
    {
        Gdiplus::Graphics graphics(surface.get());
        graphics.SetCompositingMode(Gdiplus::CompositingModeSourceOver);
        graphics.Clear(Gdiplus::Color(0, 0, 0, 0));
        Draw(graphics, width, height);
        graphics.Flush(Gdiplus::FlushIntentionSync);
    }
    HDC screen = GetDC(nullptr);
    if (!screen) return false;
    SIZE size{width, height};
    POINT source{0, 0};
    BLENDFUNCTION blend{AC_SRC_OVER, 0, 255, AC_SRC_ALPHA};
    const BOOL ok = UpdateLayeredWindow(window, screen, nullptr, &size, memoryDC, &source, 0, &blend, ULW_ALPHA);
    ReleaseDC(nullptr, screen);
    return ok != FALSE;
}

void PanelWindow::Draw(Gdiplus::Graphics& graphics, int width, int height) {
    (void)width;
    (void)height;
    graphics.SetSmoothingMode(Gdiplus::SmoothingModeAntiAlias);
    graphics.SetPixelOffsetMode(Gdiplus::PixelOffsetModeHalf);
    graphics.SetCompositingQuality(Gdiplus::CompositingQualityHighQuality);
    graphics.SetTextRenderingHint(Gdiplus::TextRenderingHintAntiAliasGridFit);

    const Gdiplus::RectF card = CardRect();
    Gdiplus::GraphicsPath cardPath;
    AddRoundRect(cardPath, card, static_cast<float>(metrics.radius));

    DrawShadow(graphics, card);
    DrawCard(graphics, card);
    DrawHeader(graphics, card, cardPath);
    DrawBody(graphics, card, cardPath);
    DrawFooter(graphics, card, cardPath);
    DrawScrollBar(graphics);
}

void PanelWindow::DrawShadow(Gdiplus::Graphics& graphics, const Gdiplus::RectF& card) {
    const int depth = std::max(4, metrics.shadow);
    for (int i = depth; i >= 1; --i) {
        const float inset = static_cast<float>(i) - 0.5f;
        Gdiplus::RectF ring(card.X - inset, card.Y - inset + 3.0f, card.Width + inset * 2.0f,
                            card.Height + inset * 2.0f);
        const float distance = 1.0f - static_cast<float>(i) / static_cast<float>(depth);
        const BYTE alpha = static_cast<BYTE>(3.0f + 24.0f * distance * distance);
        Gdiplus::Pen pen(Gdiplus::Color(alpha, 0, 0, 0), 1.8f);
        Gdiplus::GraphicsPath path;
        AddRoundRect(path, ring, metrics.radius + inset);
        graphics.DrawPath(&pen, &path);
    }
}

void PanelWindow::DrawCard(Gdiplus::Graphics& graphics, const Gdiplus::RectF& card) {
    Gdiplus::GraphicsPath path;
    AddRoundRect(path, card, static_cast<float>(metrics.radius));
    Gdiplus::SolidBrush fill(kCardFill);
    graphics.FillPath(&fill, &path);

    // 在上方三分之一处添加淡淡的光泽，为半透明底板增加层次感
    const float sheenHeight = std::min(card.Height * 0.45f, static_cast<float>(Scale(180)));
    Gdiplus::LinearGradientBrush sheen(
        Gdiplus::RectF(card.X, card.Y, card.Width, sheenHeight),
        Gdiplus::Color(20, 255, 255, 255), Gdiplus::Color(0, 255, 255, 255),
        Gdiplus::LinearGradientModeVertical);
    graphics.SetClip(&path);
    graphics.FillRectangle(&sheen, Gdiplus::RectF(card.X, card.Y, card.Width, sheenHeight));
    graphics.ResetClip();

    Gdiplus::Pen edge(kCardEdge, 1.0f);
    graphics.DrawPath(&edge, &path);
}

void PanelWindow::DrawHeader(Gdiplus::Graphics& graphics, const Gdiplus::RectF& card,
                             const Gdiplus::GraphicsPath& cardPath) {
    graphics.SetClip(&cardPath);
    Gdiplus::Pen rule(kRule, 1.0f);
    const float ruleInset = metrics.radius * 0.5f;
    graphics.DrawLine(&rule, card.X + ruleInset, card.Y + metrics.header,
                      card.X + card.Width - ruleInset, card.Y + metrics.header);

    const float centerY = card.Y + metrics.header * 0.5f;
    const float dotX = card.X + metrics.padL + Scale(2);
    const float radius = static_cast<float>(Scale(4));
    const float halo = radius + Scale(3);
    Gdiplus::SolidBrush glow(StateColor(params.state, 40));
    graphics.FillEllipse(&glow, dotX - halo, centerY - halo, halo * 2.0f, halo * 2.0f);
    Gdiplus::SolidBrush dot(StateColor(params.state, 240));
    graphics.FillEllipse(&dot, dotX - radius, centerY - radius, radius * 2.0f, radius * 2.0f);

    float x = dotX + radius + Scale(10);
    const float titleBaseline = centerY - bodyStrong.Height() * 0.5f + bodyStrong.Ascent();
    bodyStrong.Draw(graphics, params.title, 0, params.title.size(), x, titleBaseline, kTitleInk);
    x += static_cast<float>(bodyStrong.Measure(params.title));

    Gdiplus::RectF buttons[3];
    ButtonRects(buttons);
    const int labelRoom = static_cast<int>(buttons[0].X - x) - Scale(22);
    if (!params.label.empty() && labelRoom > Scale(24)) {
        x += Scale(10);
        Gdiplus::Pen separator(Gdiplus::Color(34, 255, 255, 255), 1.0f);
        graphics.DrawLine(&separator, x, centerY - Scale(6), x, centerY + Scale(6));
        x += Scale(10);
        const std::wstring label = Elide(small, params.label, labelRoom - Scale(12));
        const float baseline = centerY - small.Height() * 0.5f + small.Ascent();
        small.Draw(graphics, label, 0, label.size(), x, baseline, kMutedInk);
    }

    for (int i = 0; i < 3; ++i) DrawButton(graphics, buttons[i], i, hoverButton == i);
    graphics.ResetClip();
}

void PanelWindow::DrawButton(Gdiplus::Graphics& graphics, const Gdiplus::RectF& rect, int id, bool hot) {
    const float radius = static_cast<float>(Scale(6));
    if (hot) {
        const Gdiplus::Color fill = id == 2 ? Gdiplus::Color(210, 229, 72, 77) : Gdiplus::Color(22, 255, 255, 255);
        FillRoundRect(graphics, rect, radius, fill);
    }
    const Gdiplus::Color ink = (id == 2 && hot) ? Gdiplus::Color(255, 255, 255, 255) : kIconInk;
    Gdiplus::Pen pen(ink, static_cast<Gdiplus::REAL>(Scale(1)) + 0.15f);
    pen.SetStartCap(Gdiplus::LineCapRound);
    pen.SetEndCap(Gdiplus::LineCapRound);
    const float cx = rect.X + rect.Width * 0.5f;
    const float cy = rect.Y + rect.Height * 0.5f;
    const float s = static_cast<float>(Scale(4.5f));
    switch (id) {
        case 0:
            graphics.DrawLine(&pen, cx - s, cy, cx + s, cy);
            break;
        case 1:
            if (fullscreen) {
                graphics.DrawLine(&pen, cx - s, cy - s + s * 0.5f, cx + s - s * 0.5f, cy - s + s * 0.5f);
                graphics.DrawLine(&pen, cx - s, cy - s + s * 0.5f, cx - s, cy + s);
                graphics.DrawLine(&pen, cx - s, cy + s, cx + s, cy + s);
                graphics.DrawLine(&pen, cx + s, cy + s, cx + s, cy - s + s * 0.5f);
                graphics.DrawLine(&pen, cx - s + s * 0.5f, cy + s - s * 0.5f, cx - s + s * 0.5f, cy - s);
                graphics.DrawLine(&pen, cx - s + s * 0.5f, cy - s, cx + s, cy - s);
            } else {
                graphics.DrawLine(&pen, cx - s, cy - s, cx + s, cy - s);
                graphics.DrawLine(&pen, cx + s, cy - s, cx + s, cy + s);
                graphics.DrawLine(&pen, cx + s, cy + s, cx - s, cy + s);
                graphics.DrawLine(&pen, cx - s, cy + s, cx - s, cy - s);
            }
            break;
        case 2:
            graphics.DrawLine(&pen, cx - s, cy - s, cx + s, cy + s);
            graphics.DrawLine(&pen, cx + s, cy - s, cx - s, cy + s);
            break;
        default:
            break;
    }
}

void PanelWindow::DrawBody(Gdiplus::Graphics& graphics, const Gdiplus::RectF& card,
                           const Gdiplus::GraphicsPath& cardPath) {
    graphics.SetClip(&cardPath);
    const Gdiplus::RectF bodyRect = BodyRect();
    const float textLeft = card.X + metrics.textX;

    if (entries.empty()) {
        // Agent 尚未发送任何内容时会话也可能已经存在，因此空状态需要区分这两种情况
        const bool hasSession = params.state != SessionState::None;
        const std::wstring title = hasSession ? L"本会话还没有记录" : L"当前没有远程协助会话";
        const std::wstring hint = hasSession
            ? L"Agent 的读取、执行与下载操作会实时显示在这里。"
            : L"会话开始后，这里会显示 Agent 的读取、执行与下载操作。";
        const float centerY = bodyRect.Y + bodyRect.Height * 0.5f;
        const float titleX = card.X + (card.Width - body.Measure(title)) * 0.5f;
        const float hintX = card.X + (card.Width - small.Measure(hint)) * 0.5f;
        body.Draw(graphics, title, 0, title.size(), titleX,
                  centerY - metrics.lineHeight * 0.5f, Gdiplus::Color(210, 186, 195, 212));
        small.Draw(graphics, hint, 0, hint.size(), hintX, centerY + metrics.lineHeight * 0.9f, kDimInk);
        graphics.ResetClip();
        return;
    }

    float y = bodyRect.Y + metrics.padT - scroll;
    const float bottom = bodyRect.Y + metrics.padT + BodyHeight();
    const float barX = card.X + metrics.padL + metrics.timeW + metrics.gap;
    for (size_t i = 0; i < lines.size(); ++i) {
        const Line& line = lines[i];
        if (line.first && i > 0) y += metrics.entryGap;
        const float lineBottom = y + metrics.lineHeight;
        if (lineBottom >= bodyRect.Y && y <= bottom) {
            const Entry& entry = entries[line.entry];
            const float centerY = y + metrics.lineHeight * 0.5f;
            const float baseline = centerY - body.Height() * 0.5f + body.Ascent();
            if (line.first) {
                FillRoundRect(graphics,
                              Gdiplus::RectF(barX, y + Scale(3), static_cast<Gdiplus::REAL>(metrics.barW),
                                             static_cast<Gdiplus::REAL>(metrics.lineHeight - Scale(6))),
                              metrics.barW * 0.5f, KindColor(entry.kind, 215));
                if (!entry.time.empty()) {
                    const float timeX = card.X + metrics.padL + metrics.timeW - small.Measure(entry.time);
                    const float timeBaseline = centerY - small.Height() * 0.5f + small.Ascent();
                    small.Draw(graphics, entry.time, 0, entry.time.size(), timeX, timeBaseline, kDimInk);
                }
                const std::wstring label = KindLabel(entry.kind);
                body.Draw(graphics, label, 0, label.size(), barX + metrics.barW + Scale(8), baseline,
                          KindColor(entry.kind, 238));
            }
            if (line.begin < line.end) {
                body.Draw(graphics, entry.text, static_cast<size_t>(line.begin),
                          static_cast<size_t>(line.end), textLeft, baseline, KindInk(entry.kind));
            }
        }
        y += metrics.lineHeight;
    }
    graphics.ResetClip();
}

void PanelWindow::DrawFooter(Gdiplus::Graphics& graphics, const Gdiplus::RectF& card,
                             const Gdiplus::GraphicsPath& cardPath) {
    graphics.SetClip(&cardPath);
    const float top = card.Y + card.Height - metrics.footer;
    Gdiplus::Pen rule(kRule, 1.0f);
    const float ruleInset = metrics.radius * 0.5f;
    graphics.DrawLine(&rule, card.X + ruleInset, top, card.X + card.Width - ruleInset, top);

    const float centerY = top + metrics.footer * 0.5f;
    const float dotX = card.X + metrics.padL + Scale(3);
    const float radius = static_cast<float>(Scale(2.5f));
    Gdiplus::SolidBrush dot(StateColor(params.state, 220));
    graphics.FillEllipse(&dot, dotX - radius, centerY - radius, radius * 2.0f, radius * 2.0f);

    const float baseline = centerY - small.Height() * 0.5f + small.Ascent();
    small.Draw(graphics, params.status, 0, params.status.size(), dotX + radius + Scale(8), baseline,
               StateInk(params.state));

    wchar_t buffer[64];
    swprintf(buffer, 64, L"只读 · %d 条记录", static_cast<int>(entries.size()));
    const std::wstring count = buffer;
    const float countX = card.X + card.Width - metrics.padR - small.Measure(count);
    small.Draw(graphics, count, 0, count.size(), countX, baseline, kDimInk);
    graphics.ResetClip();
}

void PanelWindow::DrawScrollBar(Gdiplus::Graphics& graphics) {
    Gdiplus::RectF thumb;
    if (!ScrollBarThumb(&thumb)) return;
    const bool hot = draggingScroll;
    FillRoundRect(graphics, thumb, thumb.Width * 0.5f,
                  hot ? Gdiplus::Color(140, 226, 232, 244) : Gdiplus::Color(80, 208, 215, 230));
}

void PanelWindow::DefaultGeometry(RECT* rect) const {
    MONITORINFO info{};
    info.cbSize = sizeof info;
    GetMonitorInfoW(MonitorFromPoint(POINT{0, 0}, MONITOR_DEFAULTTOPRIMARY), &info);
    const RECT work = info.rcWork;
    const int workWidth = work.right - work.left;
    const int workHeight = work.bottom - work.top;
    const int width = std::min(Scale(820), workWidth - Scale(120));
    const int height = std::min(Scale(600), workHeight - Scale(120));
    rect->left = work.left + (workWidth - width) / 2;
    rect->top = work.top + (workHeight - height) / 2;
    rect->right = rect->left + width;
    rect->bottom = rect->top + height;
}

void PanelWindow::LoadState() {
    DefaultGeometry(&initialRect);
    const std::wstring path = StatePath();
    if (path.empty()) return;
    HANDLE file = CreateFileW(path.c_str(), GENERIC_READ, FILE_SHARE_READ, nullptr, OPEN_EXISTING,
                              FILE_ATTRIBUTE_NORMAL, nullptr);
    if (file == INVALID_HANDLE_VALUE) return;
    char buffer[128] = {0};
    DWORD read = 0;
    ReadFile(file, buffer, sizeof buffer - 1, &read, nullptr);
    CloseHandle(file);
    int x = 0, y = 0, width = 0, height = 0;
    if (sscanf(buffer, "%d %d %d %d", &x, &y, &width, &height) != 4) return;
    if (width < Scale(320) || height < Scale(220)) return;
    RECT saved{x, y, x + width, y + height};
    if (!MonitorFromRect(&saved, MONITOR_DEFAULTTONULL)) return;
    initialRect = saved;
}

void PanelWindow::SaveState() {
    if (!window) return;
    RECT rect = restoreRect;
    if (!fullscreen) GetWindowRect(window, &rect);
    if (rect.right - rect.left < Scale(320) || rect.bottom - rect.top < Scale(220)) return;
    const std::wstring path = StatePath();
    if (path.empty()) return;
    HANDLE file = CreateFileW(path.c_str(), GENERIC_WRITE, 0, nullptr, CREATE_ALWAYS,
                              FILE_ATTRIBUTE_NORMAL, nullptr);
    if (file == INVALID_HANDLE_VALUE) return;
    char buffer[128] = {0};
    const int length = snprintf(buffer, sizeof buffer, "%ld %ld %ld %ld\n", rect.left, rect.top,
                                rect.right - rect.left, rect.bottom - rect.top);
    DWORD written = 0;
    if (length > 0) WriteFile(file, buffer, static_cast<DWORD>(length), &written, nullptr);
    CloseHandle(file);
}

}  // namespace yiuk
