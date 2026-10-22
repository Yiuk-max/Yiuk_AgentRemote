#pragma once

#include <windows.h>
// GDI+ needs PROPID, which WIN32_LEAN_AND_MEAN keeps out of <windows.h>.
#include <objidl.h>
#include <gdiplus.h>

#include <memory>
#include <string>
#include <unordered_map>

namespace yiuk {

// Text is laid out with two typefaces: a monospaced Latin face for commands and
// output, and a CJK face for the Chinese copy. GDI+ does not font-link between
// them, so every run is measured and drawn with the face that owns its glyphs.
class TextMetrics {
public:
    TextMetrics();
    ~TextMetrics();

    TextMetrics(const TextMetrics&) = delete;
    TextMetrics& operator=(const TextMetrics&) = delete;

    // Sizes are device pixels: the window scales them by the monitor DPI.
    void Init(float pixelSize, bool bold = false);

    float PixelSize() const { return pixelSize; }
    float Ascent() const { return ascent; }
    float Height() const { return height; }

    // Advance of one glyph unit; `units` reports 1 or 2 (surrogate pair).
    int Advance(const std::wstring& text, size_t index, size_t* units = nullptr) const;
    int Measure(const std::wstring& text) const;

    // Draws [begin, end) with `baselineY` as the shared baseline, so text of
    // different sizes still sits on one visual line.
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
