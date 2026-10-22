#include "glyph.h"

#include <QPainter>
#include <QPainterPath>
#include <QtMath>

namespace {

QPainterPath starPath() {
    QPainterPath path;
    for (int i = 0; i < 10; ++i) {
        const qreal angle = -3.14159265358979323846 / 2 + i * 3.14159265358979323846 / 5;
        const qreal radius = i % 2 == 0 ? 7.2 : 3.2;
        const QPointF point(9 + qCos(angle) * radius, 9 + qSin(angle) * radius);
        if (i == 0) path.moveTo(point); else path.lineTo(point);
    }
    path.closeSubpath();
    return path;
}

// A single-colour icon rendered at the sizes the UI asks for, so a 27px star is
// drawn instead of a stretched 18px pixmap.
static QIcon stateIcon(Glyph glyph, const QColor &normal, const QColor &active, const QColor &disabled) {
    static const int sizes[] = {16, 20, 24, 28, 32, 40, 48};
    QIcon icon;
    for (const int size : sizes) {
        icon.addPixmap(glyphPixmap(glyph, normal, size), QIcon::Normal, QIcon::Off);
        icon.addPixmap(glyphPixmap(glyph, active, size), QIcon::Active, QIcon::Off);
        icon.addPixmap(glyphPixmap(glyph, active, size), QIcon::Normal, QIcon::On);
        icon.addPixmap(glyphPixmap(glyph, disabled, size), QIcon::Disabled, QIcon::Off);
    }
    return icon;
}

}  // namespace



void paintGlyph(QPainter &painter, Glyph glyph, const QColor &color) {
    painter.setRenderHint(QPainter::Antialiasing, true);
    painter.setPen(QPen(color, 1.65, Qt::SolidLine, Qt::RoundCap, Qt::RoundJoin));
    painter.setBrush(Qt::NoBrush);
    switch (glyph) {
    case Glyph::Home: {
        QPainterPath path; path.moveTo(2, 8); path.lineTo(9, 2); path.lineTo(16, 8);
        path.moveTo(4, 7); path.lineTo(4, 16); path.lineTo(14, 16); path.lineTo(14, 7);
        path.moveTo(8, 16); path.lineTo(8, 11); path.lineTo(11, 11); path.lineTo(11, 16); painter.drawPath(path); break;
    }
    case Glyph::Devices:
        painter.drawRoundedRect(QRectF(2, 3, 11, 9), 1.5, 1.5);
        painter.drawLine(QPointF(5, 15), QPointF(10, 15)); painter.drawLine(QPointF(7.5, 12), QPointF(7.5, 15));
        painter.drawRoundedRect(QRectF(12, 8, 4, 8), 1, 1); break;
    case Glyph::History:
        painter.drawEllipse(QRectF(2.5, 2.5, 13, 13));
        painter.drawLine(QPointF(9, 5), QPointF(9, 9)); painter.drawLine(QPointF(9, 9), QPointF(12, 10.5)); break;
    case Glyph::Settings:
        painter.drawEllipse(QRectF(6, 6, 6, 6));
        for (int a = 0; a < 360; a += 45) {
            const qreal r1 = 6.7, r2 = 8.4, rad = a * 3.14159265358979323846 / 180.0;
            painter.drawLine(QPointF(9 + qCos(rad) * r1, 9 + qSin(rad) * r1), QPointF(9 + qCos(rad) * r2, 9 + qSin(rad) * r2));
        }
        break;
    case Glyph::Copy:
        painter.drawRoundedRect(QRectF(6, 2, 9, 11), 1.4, 1.4);
        painter.drawLine(QPointF(4, 5), QPointF(3, 5)); painter.drawLine(QPointF(3, 5), QPointF(3, 15));
        painter.drawLine(QPointF(3, 15), QPointF(12, 15)); painter.drawLine(QPointF(12, 15), QPointF(12, 14)); break;
    case Glyph::Refresh:
        painter.drawArc(QRectF(3, 3, 12, 12), 40 * 16, 245 * 16);
        painter.drawLine(QPointF(13, 3), QPointF(16, 3)); painter.drawLine(QPointF(16, 3), QPointF(16, 6));
        painter.drawLine(QPointF(5, 15), QPointF(2, 15)); painter.drawLine(QPointF(2, 15), QPointF(2, 12)); break;
    case Glyph::Star:
    case Glyph::StarFilled: {
        const QPainterPath path = starPath();
        if (glyph == Glyph::StarFilled) {
            // Solid star for the "already saved" state.
            painter.setPen(QPen(color, 1.2, Qt::SolidLine, Qt::RoundCap, Qt::RoundJoin));
            painter.setBrush(color);
        }
        painter.drawPath(path);
        painter.setBrush(Qt::NoBrush);
        break;
    }
    case Glyph::Eye:
    case Glyph::EyeOff: {
        QPainterPath path; path.moveTo(1.5, 9); path.cubicTo(5, 3, 13, 3, 16.5, 9); path.cubicTo(13, 15, 5, 15, 1.5, 9); painter.drawPath(path);
        painter.drawEllipse(QRectF(7, 7, 4, 4));
        if (glyph == Glyph::EyeOff) painter.drawLine(QPointF(3, 3), QPointF(15, 15));
        break;
    }
    case Glyph::Minimize: painter.drawLine(QPointF(3, 12), QPointF(15, 12)); break;
    case Glyph::Maximize: painter.drawRect(QRectF(3.5, 3.5, 11, 11)); break;
    case Glyph::Restore:
        painter.drawRect(QRectF(3.5, 5.5, 9, 9));
        painter.drawLine(QPointF(6, 3.5), QPointF(14.5, 3.5)); painter.drawLine(QPointF(14.5, 3.5), QPointF(14.5, 12)); break;
    case Glyph::Close:
        painter.drawLine(QPointF(4, 4), QPointF(14, 14)); painter.drawLine(QPointF(14, 4), QPointF(4, 14)); break;
    }
}

QPixmap glyphPixmap(Glyph glyph, const QColor &color, int size) {
    QPixmap pixmap(size, size);
    pixmap.fill(Qt::transparent);
    QPainter painter(&pixmap);
    painter.setRenderHint(QPainter::Antialiasing, true);
    // Same 18x18 grid and 2px inset as before, scaled to the requested size.
    const qreal factor = size / 18.0;
    painter.translate(2.0 * factor, 2.0 * factor);
    painter.scale(0.78 * factor, 0.78 * factor);
    paintGlyph(painter, glyph, color);
    return pixmap;
}

QIcon glyphIcon(Glyph glyph) {
    QIcon icon;
    icon.addPixmap(glyphPixmap(glyph, QColor("#66758a")), QIcon::Normal, QIcon::Off);
    icon.addPixmap(glyphPixmap(glyph, QColor("#1479ed")), QIcon::Active, QIcon::Off);
    icon.addPixmap(glyphPixmap(glyph, QColor("#1479ed")), QIcon::Normal, QIcon::On);
    icon.addPixmap(glyphPixmap(glyph, QColor("#aab4c1")), QIcon::Disabled, QIcon::Off);
    return icon;
}

QIcon starIcon(bool filled) {
    if (!filled) return stateIcon(Glyph::Star, QColor("#66758a"), QColor("#1479ed"), QColor("#aab4c1"));
    // Amber from the same family the warning banners use, so the filled state
    // reads as "saved" without introducing a new colour.
    return stateIcon(Glyph::StarFilled, QColor("#f0ad2a"), QColor("#d99314"), QColor("#e4d4ab"));
}

