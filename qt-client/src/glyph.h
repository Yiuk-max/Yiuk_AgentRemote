#pragma once

#include <QColor>
#include <QIcon>
#include <QPixmap>

class QPainter;

// 手绘矢量图标，让客户端不依赖图标字体或平台主题
enum class Glyph {
    Home, Devices, History, Settings, Copy, Refresh, Star, StarFilled, Eye, EyeOff, Minimize, Maximize, Restore, Close
};

void paintGlyph(QPainter &painter, Glyph glyph, const QColor &color);
// 使用统一的 18x18 设计网格绘制；通过 `size` 绘制大图标，避免放大小位图导致模糊
QPixmap glyphPixmap(Glyph glyph, const QColor &color, int size = 18);
QIcon glyphIcon(Glyph glyph);

// 收藏切换图标：设备未收藏时显示灰色描边，已收藏时显示琥珀色实心图标
QIcon starIcon(bool filled);
