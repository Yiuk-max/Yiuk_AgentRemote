#pragma once

#include <windows.h>
// GDI+ 需要 PROPID，而 WIN32_LEAN_AND_MEAN 会阻止 <windows.h> 引入该定义
#include <objidl.h>
#include <gdiplus.h>

#include <memory>
#include <string>
#include <unordered_map>

namespace yiuk {

// 文本使用两种字体排版：命令和输出使用等宽拉丁字体，中文内容使用中日韩字体
// GDI+ 不会自动混合两种字体，因此每段文本都由包含对应字形的字体测量和绘制
class TextMetrics {
public:
    TextMetrics();
    ~TextMetrics();

    TextMetrics(const TextMetrics&) = delete;
    TextMetrics& operator=(const TextMetrics&) = delete;

    // 尺寸单位为设备像素；窗口会按显示器 DPI 缩放
    void Init(float pixelSize, bool bold = false);

    float PixelSize() const { return pixelSize; }
    float Ascent() const { return ascent; }
    float Height() const { return height; }

    // 单个字形单位的前进距离；`units` 返回 1 或 2（代理对）
    int Advance(const std::wstring& text, size_t index, size_t* units = nullptr) const;
    int Measure(const std::wstring& text) const;

    // 使用 `baselineY` 作为共用基线绘制 [begin, end)，确保不同字号的文本仍位于同一视觉行
    void Draw(Gdiplus::Graphics& graphics, const std::wstring& text, size_t begin, size_t end,
              float x, float baselineY, const Gdiplus::Color& color) const;

private:
    const Gdiplus::Font* FaceFor(wchar_t ch) const;
    float AscentFor(const Gdiplus::Font* font) const;
    int MeasureRun(const Gdiplus::Font* font, const std::wstring& text, size_t begin, size_t end) const;

    std::wstring latinName;
    std::wstring cjkName;
    Gdiplus::FontFamily latinFamily;
    Gdiplus::FontFamily cjkFamily;
    Gdiplus::StringFormat format;

    std::unique_ptr<Gdiplus::Font> latinFont;
    std::unique_ptr<Gdiplus::Font> cjkFont;
    std::unique_ptr<Gdiplus::Bitmap> scratchBitmap;
    std::unique_ptr<Gdiplus::Graphics> scratchGraphics;

    mutable std::unordered_map<uint32_t, int> widthCache;
    float pixelSize = 13.0f;
    float ascent = 10.0f;
    float height = 16.0f;
    bool bold = false;
};

}  // namespace yiuk
