#pragma once

#include <QColor>
#include <QIcon>
#include <QPixmap>

class QPainter;

// Hand-drawn vector glyphs so the client does not depend on an icon font or a
// platform theme.
enum class Glyph {
    Home, Devices, History, Settings, Copy, Refresh, Star, Eye, EyeOff, Minimize, Maximize, Restore, Close
};

void paintGlyph(QPainter &painter, Glyph glyph, const QColor &color);
QPixmap glyphPixmap(Glyph glyph, const QColor &color);
QIcon glyphIcon(Glyph glyph);
