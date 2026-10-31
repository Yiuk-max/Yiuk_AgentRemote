#pragma once

#include <windows.h>
// GDI+ 需要 PROPID，而 WIN32_LEAN_AND_MEAN 会阻止 <windows.h> 引入该定义
#include <objidl.h>
#include <gdiplus.h>

#include <memory>
#include <string>
#include <vector>

#include "paneltext.h"
#include "paneltypes.h"

namespace yiuk {

// 这是无边框、逐像素透明的窗口，只接收当前远程会话的日志
// 它运行在独立进程中，用户可随时关闭并从客户端重新打开；此处不会主动将窗口置于前台
class PanelWindow {
public:
    explicit PanelWindow(PanelParams params);
    ~PanelWindow();

    PanelWindow(const PanelWindow&) = delete;
    PanelWindow& operator=(const PanelWindow&) = delete;

    bool Create(HINSTANCE instance);
    int RunMessageLoop();

private:
    struct Line {
        int entry = 0;
        int begin = 0;
        int end = 0;
        bool first = false;  // 条目自动换行后的第一行
    };

    struct Metrics {
        int shadow = 18;
        int header = 42;
        int footer = 28;
        int padL = 16;
        int padR = 14;
        int padT = 10;
        int padB = 8;
        int timeW = 52;
        int barW = 2;
        int labelW = 28;
        int gap = 6;
        int textX = 120;
        int lineHeight = 20;
        int entryGap = 6;
        int radius = 14;
        int scrollbar = 4;
        int buttonW = 30;
        int buttonH = 24;
        int buttonGap = 4;
        int padding = 8;  // 滚动条旁的额外留白
    };

    enum class Drag { None, Move, Resize };

    static LRESULT CALLBACK WndProc(HWND window, UINT message, WPARAM wparam, LPARAM lparam);
    LRESULT Handle(UINT message, WPARAM wparam, LPARAM lparam);

    void HandleProtocolLine(const std::wstring& line);
    void AppendEntry(Entry entry);
    void UpdateMetrics();
    void Relayout();
    void WrapEntry(int index, int available);
    int Scale(int value) const;
    int MaxScroll() const;
    int BodyHeight() const;
    int ContentWidth() const;
    Gdiplus::RectF CardRect() const;
    Gdiplus::RectF BodyRect() const;
    bool CardContains(POINT point) const;
    void ButtonRects(Gdiplus::RectF out[3]) const;
    int ButtonAt(POINT point) const;
    int ResizeZone(POINT point) const;
    bool ScrollBarThumb(Gdiplus::RectF* thumb) const;
    void ScrollTo(int value);
    bool Present();
    bool EnsureSurface(int width, int height);
    void Draw(Gdiplus::Graphics& graphics, int width, int height);
    void DrawShadow(Gdiplus::Graphics& graphics, const Gdiplus::RectF& card);
    void DrawCard(Gdiplus::Graphics& graphics, const Gdiplus::RectF& card);
    void DrawHeader(Gdiplus::Graphics& graphics, const Gdiplus::RectF& card,
                    const Gdiplus::GraphicsPath& cardPath);
    void DrawBody(Gdiplus::Graphics& graphics, const Gdiplus::RectF& card,
                  const Gdiplus::GraphicsPath& cardPath);
    void DrawFooter(Gdiplus::Graphics& graphics, const Gdiplus::RectF& card,
                    const Gdiplus::GraphicsPath& cardPath);
    void DrawScrollBar(Gdiplus::Graphics& graphics);
    void DrawButton(Gdiplus::Graphics& graphics, const Gdiplus::RectF& rect, int id, bool hot);
    void ToggleFullscreen();
    void PerformButton(int id);
    void UpdateHover(POINT point);
    void UpdateCursor();
    void LoadState();
    void SaveState();
    void DefaultGeometry(RECT* rect) const;

    HWND window = nullptr;
    PanelParams params;
    std::vector<Entry> entries;
    std::vector<Line> lines;
    Metrics metrics;
    TextMetrics body;
    TextMetrics bodyStrong;
    TextMetrics small;
    int dpi = 96;
    int scroll = 0;
    bool follow = true;
    int contentHeight = 0;
    int hoverButton = -1;
    int pressedButton = -1;
    Drag drag = Drag::None;
    int dragZone = 0;  // bitmask: 1 left, 2 right, 4 top, 8 bottom
    POINT dragOrigin{};
    RECT dragRect{};
    bool draggingScroll = false;
    float scrollGrab = 0.0f;
    bool fullscreen = false;
    RECT restoreRect{};
    RECT initialRect{};
    POINT mouse{-1, -1};
    bool trackingMouse = false;
    HCURSOR cursor = nullptr;

    HDC memoryDC = nullptr;
    HBITMAP dib = nullptr;
    std::unique_ptr<Gdiplus::Bitmap> surface;
    int surfaceWidth = 0;
    int surfaceHeight = 0;
};

}  // namespace yiuk
