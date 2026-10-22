#pragma once

#include <QColor>
#include <QIcon>
#include <QPixmap>

class QPainter;

// Hand-drawn vector glyphs so the client does not depend on an icon font or a
// platform theme.
enum class Glyph {
    Home, Devices, History, Settings, Copy, Refresh, Star, StarFilled, Eye, EyeOff, Minimize, Maximize, Restore, Close
};

void paintGlyph(QPainter &painter, Glyph glyph, const QColor &color);
// Rendered from the same 18x18 design grid; `size` keeps big icons crisp instead
// of upscaling one small pixmap.
QPixmap glyphPixmap(Glyph glyph, const QColor &color, int size = 18);
QIcon glyphIcon(Glyph glyph);

// The favourite toggle: outlined grey when the device is not saved, filled amber
// when it already is.
QIcon starIcon(bool filled);
