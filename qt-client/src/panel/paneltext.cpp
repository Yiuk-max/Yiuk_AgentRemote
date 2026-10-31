#include "paneltext.h"

#include <cmath>
#include <initializer_list>

namespace yiuk {
namespace {

// Consolas 支持拉丁文、希腊文、西里尔文、制表符和箭头方块
// CJK 部首及其后的字符需要使用中日韩字体
bool IsCjk(wchar_t ch) {
    return ch >= 0x2E80;
}

bool IsSurrogateHigh(wchar_t ch) {
    return ch >= 0xD800 && ch <= 0xDBFF;
}

std::wstring PickFace(std::initializer_list<const wchar_t*> names) {
    for (const wchar_t* name : names) {
        Gdiplus::FontFamily probe(name);
        if (probe.IsAvailable()) return name;
    }
    return names.size() > 0 ? *names.begin() : L"Segoe UI";
}

}  // namespace

TextMetrics::TextMetrics()
    : latinName(PickFace({L"Cascadia Mono", L"Consolas", L"Courier New", L"Tahoma"})),
      cjkName(PickFace({L"Microsoft YaHei UI", L"Microsoft YaHei", L"SimSun", L"Segoe UI"})),
      latinFamily(latinName.c_str()),
      cjkFamily(cjkName.c_str()),
      format(Gdiplus::StringFormat::GenericTypographic()) {
    format.SetFormatFlags(format.GetFormatFlags() | Gdiplus::StringFormatFlagsMeasureTrailingSpaces);
    scratchBitmap = std::make_unique<Gdiplus::Bitmap>(1, 1, PixelFormat32bppPARGB);
    scratchGraphics = std::make_unique<Gdiplus::Graphics>(scratchBitmap.get());
    scratchGraphics->SetTextRenderingHint(Gdiplus::TextRenderingHintAntiAliasGridFit);
}

TextMetrics::~TextMetrics() = default;

void TextMetrics::Init(float size, bool makeBold) {
    if (latinFont && std::abs(size - pixelSize) < 0.01f && makeBold == bold) return;
    pixelSize = size;
    bold = makeBold;
    widthCache.clear();
    const INT style = bold ? Gdiplus::FontStyleBold : Gdiplus::FontStyleRegular;
    latinFont = std::make_unique<Gdiplus::Font>(&latinFamily, pixelSize, style, Gdiplus::UnitPixel);
    cjkFont = std::make_unique<Gdiplus::Font>(&cjkFamily, pixelSize, style, Gdiplus::UnitPixel);
    ascent = AscentFor(latinFont.get());
    height = pixelSize * (latinFamily.GetCellAscent(style) + latinFamily.GetCellDescent(style))
        / latinFamily.GetEmHeight(style);
}

const Gdiplus::Font* TextMetrics::FaceFor(wchar_t ch) const {
    if (cjkFont && (IsCjk(ch) || !latinFont)) return cjkFont.get();
    return latinFont.get();
}

float TextMetrics::AscentFor(const Gdiplus::Font* font) const {
    const Gdiplus::FontFamily* family = font == cjkFont.get() ? &cjkFamily : &latinFamily;
    const INT style = bold ? Gdiplus::FontStyleBold : Gdiplus::FontStyleRegular;
    const INT em = family->GetEmHeight(style);
    if (em <= 0) return pixelSize * 0.8f;
    return pixelSize * family->GetCellAscent(style) / em;
}

int TextMetrics::MeasureRun(const Gdiplus::Font* font, const std::wstring& text, size_t begin,
                            size_t end) const {
    if (!font || begin >= end) return 0;
    Gdiplus::RectF bounds;
    scratchGraphics->MeasureString(text.c_str() + begin, static_cast<INT>(end - begin), font,
                                   Gdiplus::PointF(0.0f, 0.0f), &format, &bounds);
    return static_cast<int>(std::ceil(bounds.Width));
}

int TextMetrics::Advance(const std::wstring& text, size_t index, size_t* units) const {
    if (index >= text.size()) {
        if (units) *units = 0;
        return 0;
    }
    const wchar_t ch = text[index];
    size_t consumed = 1;
    if (IsSurrogateHigh(ch) && index + 1 < text.size()) consumed = 2;
    if (units) *units = consumed;

    // 两种字体中的单个代码单元宽度都稳定（拉丁字体为等宽字体），因此每个字符只需测量一次宽度
    uint32_t key = static_cast<uint32_t>(ch);
    if (consumed == 2) key = 0x10000u | (static_cast<uint32_t>(text[index + 1]) & 0xFFFFu);
    auto cached = widthCache.find(key);
    if (cached != widthCache.end()) return cached->second;
    const int width = MeasureRun(FaceFor(ch), text, index, index + consumed);
    widthCache.emplace(key, width);
    return width;
}

int TextMetrics::Measure(const std::wstring& text) const {
    int total = 0;
    for (size_t i = 0; i < text.size();) {
        size_t units = 1;
        total += Advance(text, i, &units);
        i += units ? units : 1;
    }
    return total;
}

void TextMetrics::Draw(Gdiplus::Graphics& graphics, const std::wstring& text, size_t begin,
                       size_t end, float x, float baselineY, const Gdiplus::Color& color) const {
    if (begin >= end) return;
    Gdiplus::SolidBrush brush(color);
    size_t pos = begin;
    while (pos < end) {
        const Gdiplus::Font* font = FaceFor(text[pos]);
        size_t runEnd = pos;
        while (runEnd < end && FaceFor(text[runEnd]) == font) {
            size_t units = 1;
            if (IsSurrogateHigh(text[runEnd]) && runEnd + 1 < end) units = 2;
            runEnd += units;
        }
        const float y = baselineY - AscentFor(font);
        graphics.DrawString(text.c_str() + pos, static_cast<INT>(runEnd - pos), font,
                            Gdiplus::PointF(x, y), &format, &brush);
        x += static_cast<float>(MeasureRun(font, text, pos, runEnd));
        pos = runEnd;
    }
}

}  // namespace yiuk
