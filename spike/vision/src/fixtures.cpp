#include "fixtures.h"

#include <QFont>
#include <QPainter>
#include <QPainterPath>

#include <algorithm>
#include <cmath>

namespace vis {
namespace {

struct Palette {
    QColor bg;
    QColor panel;
    QColor border;
    QColor text;
    QColor muted;
    QColor accent;
};

Palette paletteFor(bool dark) {
    if (dark) {
        return Palette{QColor("#10171f"), QColor("#1b2430"), QColor("#2b3746"),
                       QColor("#e8eef7"), QColor("#7d8b9c"), QColor("#4d9dff")};
    }
    return Palette{QColor("#f6f8fb"), QColor("#ffffff"), QColor("#dde3ec"),
                   QColor("#1f2a37"), QColor("#8a95a5"), QColor("#1479ed")};
}

struct DrawItem {
    std::string id;
    Rect logical;
    int icon = -1;
    std::string text;
    bool outline = false;
    int variant = 0;
    bool chip = false;
    bool muted = false;
    bool track = false;
};

void drawIcon(QPainter& p, int icon, const QRectF& area, const QColor& color, bool outline, int variant) {
    const qreal side = std::min(area.width(), area.height());
    const QRectF box(area.center().x() - side / 2.0, area.center().y() - side / 2.0, side, side);
    QPen pen(color, std::max<qreal>(1.1, side * 0.085), Qt::SolidLine, Qt::RoundCap, Qt::RoundJoin);
    p.setBrush(Qt::NoBrush);
    p.setPen(pen);
    const QRectF inner = box.adjusted(side * 0.08, side * 0.08, -side * 0.08, -side * 0.08);
    const auto solidFill = [&] {
        p.setBrush(color);
        p.setPen(Qt::NoPen);
    };
    const auto restorePen = [&] {
        p.setBrush(Qt::NoBrush);
        p.setPen(pen);
    };

    switch (icon) {
        case 0: {  // 首页
            QPainterPath roof;
            roof.moveTo(inner.left(), inner.top() + side * 0.38);
            roof.lineTo(inner.center().x(), inner.top());
            roof.lineTo(inner.right(), inner.top() + side * 0.38);
            p.drawPath(roof);
            const QRectF body(inner.left() + side * 0.16, inner.top() + side * 0.36, side * 0.52, side * 0.5);
            if (outline) {
                p.drawRect(body);
            } else {
                solidFill();
                p.drawRoundedRect(body, side * 0.05, side * 0.05);
                restorePen();
            }
            break;
        }
        case 1: {  // 显示器 / 设备
            QRectF screen = inner.adjusted(0, 0, 0, -side * 0.18);
            if (outline) {
                p.drawRoundedRect(screen, side * 0.06, side * 0.06);
            } else {
                solidFill();
                p.drawRoundedRect(screen, side * 0.06, side * 0.06);
                restorePen();
            }
            p.drawLine(QPointF(screen.center().x(), screen.bottom()), QPointF(screen.center().x(), inner.bottom() - side * 0.06));
            p.drawLine(QPointF(screen.left() + side * 0.16, inner.bottom() - side * 0.06),
                       QPointF(screen.right() - side * 0.16, inner.bottom() - side * 0.06));
            break;
        }
        case 2: {  // 列表
            for (int i = 0; i < 3; ++i) {
                const qreal y = inner.top() + side * (0.16 + 0.28 * i);
                p.setBrush(color);
                p.setPen(Qt::NoPen);
                p.drawEllipse(QPointF(inner.left() + side * 0.06, y), side * 0.05, side * 0.05);
                p.setPen(pen);
                p.drawLine(QPointF(inner.left() + side * 0.2, y), QPointF(inner.right(), y));
            }
            break;
        }
        case 3: {  // 齿轮
            const int teeth = variant == 1 ? 6 : 8;
            QPainterPath gear;
            const QPointF center = inner.center();
            const qreal outer = side * 0.42;
            const qreal innerR = side * 0.28;
            for (int i = 0; i < teeth * 2; ++i) {
                const qreal angle = M_PI * double(i) / double(teeth);
                const qreal radius = (i % 2 == 0) ? outer : innerR;
                const QPointF point(center.x() + radius * std::cos(angle), center.y() + radius * std::sin(angle));
                if (i == 0) gear.moveTo(point); else gear.lineTo(point);
            }
            gear.closeSubpath();
            if (outline) {
                p.drawPath(gear);
            } else {
                solidFill();
                p.drawPath(gear);
                restorePen();
            }
            p.drawEllipse(center, side * 0.11, side * 0.11);
            break;
        }
        case 4: {  // 帮助
            p.drawEllipse(inner.center(), inner.width() / 2.0, inner.height() / 2.0);
            QFont font = p.font();
            font.setPixelSize(int(side * 0.6));
            font.setBold(true);
            p.setFont(font);
            p.drawText(inner, Qt::AlignCenter, QStringLiteral("?"));
            break;
        }
        case 5: {  // 用户
            const QPointF head(inner.center().x(), inner.top() + side * 0.24);
            if (outline) {
                p.drawEllipse(head, side * 0.16, side * 0.16);
            } else {
                solidFill();
                p.drawEllipse(head, side * 0.16, side * 0.16);
                restorePen();
            }
            QPainterPath shoulders;
            shoulders.moveTo(inner.left(), inner.bottom());
            shoulders.quadTo(inner.center().x(), inner.top() + side * 0.34, inner.right(), inner.bottom());
            if (outline) {
                p.drawPath(shoulders);
            } else {
                solidFill();
                p.drawPath(shoulders);
                restorePen();
            }
            break;
        }
        case 6: {  // 星
            QPainterPath star;
            const QPointF center = inner.center();
            for (int i = 0; i < 10; ++i) {
                const qreal radius = (i % 2 == 0) ? side * 0.46 : side * 0.19;
                const qreal angle = -M_PI / 2.0 + M_PI * double(i) / 5.0;
                const QPointF point(center.x() + radius * std::cos(angle), center.y() + radius * std::sin(angle));
                if (i == 0) star.moveTo(point); else star.lineTo(point);
            }
            star.closeSubpath();
            if (outline) p.drawPath(star); else { solidFill(); p.drawPath(star); restorePen(); }
            break;
        }
        case 7: {  // 心
            QPainterPath heart;
            heart.moveTo(inner.center().x(), inner.bottom());
            heart.cubicTo(inner.left() - side * 0.1, inner.top() + side * 0.4, inner.left() + side * 0.18,
                          inner.top() - side * 0.08, inner.center().x(), inner.top() + side * 0.28);
            heart.cubicTo(inner.right() - side * 0.18, inner.top() - side * 0.08, inner.right() + side * 0.1,
                          inner.top() + side * 0.4, inner.center().x(), inner.bottom());
            if (outline) p.drawPath(heart); else { solidFill(); p.drawPath(heart); restorePen(); }
            break;
        }
        case 8: {  // 铃铛（variant 1 带斜杠）
            QPainterPath bell;
            bell.moveTo(inner.left() + side * 0.12, inner.top() + side * 0.7);
            bell.quadTo(inner.center().x(), inner.top() - side * 0.1, inner.right() - side * 0.12,
                        inner.top() + side * 0.7);
            bell.closeSubpath();
            if (outline) p.drawPath(bell); else { solidFill(); p.drawPath(bell); restorePen(); }
            p.drawEllipse(QPointF(inner.center().x(), inner.bottom() - side * 0.02), side * 0.09, side * 0.09);
            if (variant == 1) p.drawLine(inner.topLeft(), inner.bottomRight());
            break;
        }
        case 9: {  // 文件夹（variant 1 开口）
            QPainterPath folder;
            folder.moveTo(inner.left(), inner.top() + side * 0.2);
            folder.lineTo(inner.left() + side * 0.36, inner.top() + side * 0.2);
            folder.lineTo(inner.left() + side * 0.46, inner.top() + side * 0.34);
            folder.lineTo(inner.right(), inner.top() + side * 0.34);
            folder.lineTo(inner.right(), inner.bottom());
            folder.lineTo(inner.left(), inner.bottom());
            folder.closeSubpath();
            if (variant == 1) {
                QPainterPath open;
                open.moveTo(inner.left() + side * 0.1, inner.top() + side * 0.46);
                open.lineTo(inner.right(), inner.top() + side * 0.46);
                open.lineTo(inner.right() - side * 0.12, inner.bottom());
                open.lineTo(inner.left(), inner.bottom());
                open.closeSubpath();
                p.drawPath(open);
            } else if (outline) {
                p.drawPath(folder);
            } else {
                solidFill();
                p.drawPath(folder);
                restorePen();
            }
            break;
        }
        case 10: {  // 放大镜
            const qreal radius = side * 0.30 * (variant == 1 ? 0.7 : 1.0);
            p.drawEllipse(inner.center(), radius, radius);
            p.drawLine(QPointF(inner.center().x() + radius * 0.7, inner.center().y() + radius * 0.7),
                       QPointF(inner.right(), inner.bottom()));
            break;
        }
        case 11: {  // 加号（variant 1 圆圈内）
            if (variant == 1) {
                p.drawEllipse(inner.center(), inner.width() / 2.0, inner.height() / 2.0);
                p.drawLine(QPointF(inner.center().x(), inner.top() + side * 0.26),
                           QPointF(inner.center().x(), inner.bottom() - side * 0.26));
                p.drawLine(QPointF(inner.left() + side * 0.26, inner.center().y()),
                           QPointF(inner.right() - side * 0.26, inner.center().y()));
            } else {
                p.drawLine(QPointF(inner.center().x(), inner.top()), QPointF(inner.center().x(), inner.bottom()));
                p.drawLine(QPointF(inner.left(), inner.center().y()), QPointF(inner.right(), inner.center().y()));
            }
            break;
        }
        case 12: {  // 关闭
            if (variant == 1) p.drawEllipse(inner.center(), inner.width() / 2.0, inner.height() / 2.0);
            const qreal inset = variant == 1 ? side * 0.26 : 0.0;
            p.drawLine(QPointF(inner.left() + inset, inner.top() + inset),
                       QPointF(inner.right() - inset, inner.bottom() - inset));
            p.drawLine(QPointF(inner.right() - inset, inner.top() + inset),
                       QPointF(inner.left() + inset, inner.bottom() - inset));
            break;
        }
        case 13: {  // 播放
            QPainterPath triangle;
            triangle.moveTo(inner.left() + side * 0.16, inner.top());
            triangle.lineTo(inner.right(), inner.center().y());
            triangle.lineTo(inner.left() + side * 0.16, inner.bottom());
            triangle.closeSubpath();
            if (outline) p.drawPath(triangle); else { solidFill(); p.drawPath(triangle); restorePen(); }
            break;
        }
        case 14: {  // 垃圾桶
            p.drawLine(QPointF(inner.left(), inner.top() + side * 0.16),
                       QPointF(inner.right(), inner.top() + side * 0.16));
            const QRectF body(inner.left() + side * 0.14, inner.top() + side * 0.16, side * 0.56, side * 0.62);
            if (outline) p.drawRect(body); else { solidFill(); p.drawRoundedRect(body, side * 0.04, side * 0.04); restorePen(); }
            p.drawLine(QPointF(inner.center().x() - side * 0.1, inner.top()), QPointF(inner.center().x() + side * 0.1, inner.top()));
            break;
        }
        case 15: {  // 下载（variant 1 云）
            if (variant == 1) {
                QPainterPath cloud;
                cloud.addEllipse(QPointF(inner.left() + side * 0.3, inner.top() + side * 0.22), side * 0.2, side * 0.2);
                cloud.addEllipse(QPointF(inner.left() + side * 0.62, inner.top() + side * 0.3), side * 0.16, side * 0.16);
                p.drawPath(cloud);
                return;
            }
            p.drawLine(QPointF(inner.center().x(), inner.top()), QPointF(inner.center().x(), inner.top() + side * 0.56));
            p.drawLine(QPointF(inner.center().x(), inner.top() + side * 0.56),
                       QPointF(inner.center().x() - side * 0.2, inner.top() + side * 0.32));
            p.drawLine(QPointF(inner.center().x(), inner.top() + side * 0.56),
                       QPointF(inner.center().x() + side * 0.2, inner.top() + side * 0.32));
            p.drawLine(QPointF(inner.left(), inner.bottom()), QPointF(inner.right(), inner.bottom()));
            break;
        }
        case 16: {  // 上传（variant 1 画成向下的箭头，作为“相似但不同”的负样本）
            if (variant == 1) {
                p.drawLine(QPointF(inner.center().x(), inner.top()), QPointF(inner.center().x(), inner.bottom() - side * 0.3));
                p.drawLine(QPointF(inner.center().x(), inner.bottom() - side * 0.3),
                           QPointF(inner.center().x() - side * 0.2, inner.bottom() - side * 0.52));
                p.drawLine(QPointF(inner.center().x(), inner.bottom() - side * 0.3),
                           QPointF(inner.center().x() + side * 0.2, inner.bottom() - side * 0.52));
                p.drawLine(QPointF(inner.left(), inner.bottom()), QPointF(inner.right(), inner.bottom()));
                return;
            }
            p.drawLine(QPointF(inner.center().x(), inner.bottom()), QPointF(inner.center().x(), inner.top() + side * 0.3));
            p.drawLine(QPointF(inner.center().x(), inner.top() + side * 0.3),
                       QPointF(inner.center().x() - side * 0.2, inner.top() + side * 0.52));
            p.drawLine(QPointF(inner.center().x(), inner.top() + side * 0.3),
                       QPointF(inner.center().x() + side * 0.2, inner.top() + side * 0.52));
            p.drawLine(QPointF(inner.left(), inner.top()), QPointF(inner.right(), inner.top()));
            break;
        }
        case 17: {  // 分享（variant 1 方框）
            if (variant == 1) {
                p.drawRect(inner.adjusted(side * 0.1, side * 0.1, -side * 0.1, -side * 0.1));
                p.drawLine(inner.topLeft(), inner.topLeft() + QPointF(side * 0.2, side * 0.2));
                return;
            }
            p.drawEllipse(QPointF(inner.right() - side * 0.12, inner.top() + side * 0.18), side * 0.11, side * 0.11);
            p.drawEllipse(QPointF(inner.right() - side * 0.12, inner.bottom() - side * 0.18), side * 0.11, side * 0.11);
            p.drawEllipse(QPointF(inner.left() + side * 0.12, inner.center().y()), side * 0.11, side * 0.11);
            p.drawLine(QPointF(inner.left() + side * 0.22, inner.center().y() - side * 0.06),
                       QPointF(inner.right() - side * 0.22, inner.top() + side * 0.22));
            p.drawLine(QPointF(inner.left() + side * 0.22, inner.center().y() + side * 0.06),
                       QPointF(inner.right() - side * 0.22, inner.bottom() - side * 0.22));
            break;
        }
        case 18: {  // 锁（variant 1 打开）
            const QRectF body(inner.left() + side * 0.12, inner.center().y(), side * 0.62, side * 0.44);
            if (outline) p.drawRoundedRect(body, side * 0.06, side * 0.06);
            else { solidFill(); p.drawRoundedRect(body, side * 0.06, side * 0.06); restorePen(); }
            QPainterPath shackle;
            if (variant == 1) {
                shackle.moveTo(body.left() + side * 0.3, body.top());
                shackle.lineTo(body.left() + side * 0.3, inner.top() + side * 0.14);
                shackle.lineTo(body.right() - side * 0.06, inner.top() + side * 0.14);
            } else {
                shackle.moveTo(body.left() + side * 0.16, body.top());
                shackle.lineTo(body.left() + side * 0.16, inner.top() + side * 0.2);
                shackle.quadTo(inner.center().x(), inner.top() - side * 0.02, body.right() - side * 0.16,
                               inner.top() + side * 0.2);
                shackle.lineTo(body.right() - side * 0.16, body.top());
            }
            p.drawPath(shackle);
            break;
        }
        case 19: {  // 日历（variant 1 带勾）
            p.drawRect(inner.adjusted(0, side * 0.1, 0, 0));
            p.drawLine(QPointF(inner.left(), inner.top() + side * 0.34),
                       QPointF(inner.right(), inner.top() + side * 0.34));
            if (variant == 1) {
                p.drawLine(QPointF(inner.left() + side * 0.24, inner.bottom() - side * 0.26),
                           QPointF(inner.center().x(), inner.bottom() - side * 0.12));
                p.drawLine(QPointF(inner.center().x(), inner.bottom() - side * 0.12),
                           QPointF(inner.right() - side * 0.16, inner.bottom() - side * 0.4));
            } else {
                for (int i = 0; i < 3; ++i) {
                    p.drawPoint(QPointF(inner.left() + side * 0.2 + i * side * 0.22, inner.bottom() - side * 0.24));
                }
            }
            break;
        }
        case 20: {  // 刷新
            QRectF arc = inner.adjusted(0, 0, 0, 0);
            p.drawArc(arc, 40 * 16, 250 * 16);
            const QPointF tip(arc.center().x() + arc.width() * 0.44, arc.center().y() - arc.height() * 0.30);
            QPainterPath arrow;
            arrow.moveTo(tip);
            arrow.lineTo(tip + QPointF(-side * 0.20, -side * 0.05));
            arrow.lineTo(tip + QPointF(-side * 0.02, -side * 0.24));
            arrow.closeSubpath();
            solidFill();
            p.drawPath(arrow);
            restorePen();
            break;
        }
        case 21: {  // 更多
            p.setBrush(color);
            p.setPen(Qt::NoPen);
            for (int i = 0; i < 3; ++i) {
                p.drawEllipse(QPointF(inner.left() + side * (0.2 + 0.3 * i), inner.center().y()), side * 0.09, side * 0.09);
            }
            restorePen();
            break;
        }
        case 22: {  // 圆点（滑块把手）
            solidFill();
            p.drawEllipse(inner.center(), inner.width() / 2.0, inner.height() / 2.0);
            restorePen();
            break;
        }
        case 23: {  // 勾
            p.drawLine(QPointF(inner.left() + side * 0.04, inner.center().y()),
                       QPointF(inner.center().x() - side * 0.08, inner.bottom() - side * 0.14));
            p.drawLine(QPointF(inner.center().x() - side * 0.08, inner.bottom() - side * 0.14),
                       QPointF(inner.right() - side * 0.02, inner.top() + side * 0.16));
            break;
        }
        default: {  // 小圆点
            solidFill();
            p.drawEllipse(inner.center(), inner.width() * 0.3, inner.height() * 0.3);
            restorePen();
            break;
        }
    }
}

std::vector<DrawItem> layoutItems(double width, double height, bool positive) {
    std::vector<DrawItem> items;
    const auto rect = [&](double fx, double fy, double fw, double fh) {
        return Rect{int(std::lround(fx * width)), int(std::lround(fy * height)), int(std::lround(fw * width)),
                    int(std::lround(fh * height))};
    };
    const double icon = 0.030 * height;
    const double navW = 0.145 * width;

    const char* navIds[5] = {"nav_home", "nav_devices", "nav_sessions", "nav_settings", "nav_help"};
    const char* navText[5] = {"首页", "设备", "会话记录", "设置", "帮助"};
    const char* navAlt[5] = {"主页", "装备", "历史", "偏好", "关于"};
    const int navIcon[5] = {0, 1, 2, 3, 4};
    for (int i = 0; i < 5; ++i) {
        DrawItem item;
        item.id = navIds[i];
        item.logical = Rect{int(0.010 * width), int((0.115 + 0.068 * i) * height), int(navW), int(0.052 * height)};
        item.icon = navIcon[i];
        item.text = positive ? navText[i] : navAlt[i];
        item.outline = !positive;
        item.variant = positive ? 0 : 1;
        items.push_back(item);
    }

    DrawItem search;
    search.id = "top_search";
    search.logical = rect(0.185, 0.018, 0.245, 0.042);
    search.chip = true;
    search.icon = 10;
    search.text = positive ? "搜索" : "筛选";
    search.variant = positive ? 0 : 1;
    items.push_back(search);

    const char* topIds[3] = {"top_fav", "top_refresh", "top_more"};
    const char* topText[3] = {"收藏", "刷新", "更多"};
    const char* topAlt[3] = {"收藏夹", "刷新全部", "更多选项"};
    const int topIcon[3] = {6, 20, 21};
    for (int i = 0; i < 3; ++i) {
        DrawItem item;
        item.id = topIds[i];
        item.logical = rect(0.455 + 0.078 * i, 0.018, 0.070, 0.042);
        item.chip = true;
        item.icon = topIcon[i];
        item.text = positive ? topText[i] : topAlt[i];
        item.outline = !positive;
        item.variant = positive ? 0 : 1;
        items.push_back(item);
    }

    DrawItem avatar;
    avatar.id = "top_avatar";
    avatar.logical = rect(0.712, 0.014, 0.031, 0.049);
    avatar.icon = 5;
    avatar.outline = !positive;
    items.push_back(avatar);

    const char* gridIds[12] = {"icon_heart", "icon_bell", "icon_folder", "icon_search", "icon_plus",
                               "icon_close", "icon_play", "icon_trash", "icon_download", "icon_upload",
                               "icon_share", "icon_lock"};
    const int gridIcon[12] = {7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18};
    for (int i = 0; i < 12; ++i) {
        DrawItem item;
        item.id = gridIds[i];
        item.logical = Rect{int((0.795 + 0.042 * (i % 4)) * width), int((0.135 + 0.062 * (i / 4)) * height),
                            int(icon), int(icon)};
        item.icon = gridIcon[i];
        item.outline = !positive;
        item.variant = positive ? 0 : 1;
        items.push_back(item);
    }

    const char* rowIds[3] = {"row_projects", "row_downloads", "row_shared"};
    const char* rowText[3] = {"项目文档", "下载目录", "共享文件夹"};
    const char* rowAlt[3] = {"项目归档", "临时目录", "共享空间"};
    const int rowIcon[3] = {19, 9, 15};
    for (int i = 0; i < 3; ++i) {
        DrawItem item;
        item.id = rowIds[i];
        item.logical = rect(0.185, 0.395 + 0.082 * i, 0.475, 0.064);
        item.chip = true;
        item.icon = rowIcon[i];
        item.text = positive ? rowText[i] : rowAlt[i];
        item.outline = !positive;
        item.variant = positive ? 0 : 1;
        items.push_back(item);
    }

    DrawItem sliderKnob;
    sliderKnob.id = "slider_knob";
    // 负样本里把把手与填充比例一起改掉：滑块没有文字，只改图标不足以构成“相似但不同”
    sliderKnob.logical = rect(positive ? 0.330 : 0.455, 0.735, 0.020, 0.032);
    sliderKnob.icon = 22;
    items.push_back(sliderKnob);

    DrawItem track;
    track.id = "slider_track";
    track.logical = rect(0.185, 0.744, 0.360, 0.014);
    track.track = true;
    items.push_back(track);

    const char* bottomIds[2] = {"bottom_apply", "bottom_cancel"};
    const char* bottomText[2] = {"应用", "取消"};
    const char* bottomAlt[2] = {"确认", "关闭"};
    const int bottomIcon[2] = {23, 12};
    for (int i = 0; i < 2; ++i) {
        DrawItem item;
        item.id = bottomIds[i];
        item.logical = rect(0.185 + 0.098 * i, 0.865, 0.085, 0.042);
        item.chip = true;
        item.icon = bottomIcon[i];
        item.text = positive ? bottomText[i] : bottomAlt[i];
        item.outline = !positive;
        item.variant = positive ? 0 : 1;
        items.push_back(item);
    }

    DrawItem cardGear;
    cardGear.id = "card_gear";
    cardGear.logical = rect(0.735, 0.700, 0.036, 0.056);
    cardGear.icon = 3;
    cardGear.outline = !positive;
    cardGear.variant = positive ? 0 : 1;
    items.push_back(cardGear);

    DrawItem status;
    status.id = "status_text";
    status.logical = rect(0.735, 0.885, 0.170, 0.036);
    status.chip = true;
    status.icon = 24;
    status.text = positive ? "就绪" : "忙碌";
    items.push_back(status);

    return items;
}

QString toQString(const std::string& value) { return QString::fromUtf8(value.c_str()); }

}  // namespace

FixtureScene renderScene(double scale, bool dark, bool positive) {
    const int width = 2048;
    const int height = 1280;
    FixtureScene scene;
    scene.scale = scale;
    scene.dark = dark;
    scene.positive = positive;
    const int dpi = int(std::lround(scale * 100.0));
    scene.name = std::string(positive ? "pos" : "neg") + "-" + std::to_string(dpi) + "-" +
                 (dark ? "dark" : "light");
    scene.image = QImage(width, height, QImage::Format_RGB32);
    const Palette palette = paletteFor(dark);
    scene.image.fill(palette.bg);

    QPainter painter(&scene.image);
    painter.setRenderHint(QPainter::Antialiasing, true);
    painter.setRenderHint(QPainter::TextAntialiasing, true);
    painter.scale(scale, scale);
    const double logicalWidth = double(width) / scale;
    const double logicalHeight = double(height) / scale;

    // 侧栏与顶栏底板：固定布局，不属于可匹配控件
    painter.setPen(Qt::NoPen);
    painter.setBrush(palette.panel);
    painter.drawRect(QRectF(0, 0, 0.165 * logicalWidth, logicalHeight));
    painter.setBrush(palette.bg);
    painter.drawRect(QRectF(0.165 * logicalWidth, 0, logicalWidth, 0.078 * logicalHeight));

    QFont textFont(QStringLiteral("Microsoft YaHei UI"));
    textFont.setPixelSize(int(std::lround(15.0 * 1.0)));
    QFont smallFont(QStringLiteral("Microsoft YaHei UI"));
    smallFont.setPixelSize(int(std::lround(12.0 * 1.0)));

    const int outlinePen = 2;
    Q_UNUSED(outlinePen);

    // 右侧设置卡片底板要在控件之前绘制，否则会盖住卡片内的齿轮图标
    painter.setPen(QPen(palette.border, 1.0));
    painter.setBrush(palette.panel);
    painter.drawRoundedRect(QRectF(0.715 * logicalWidth, 0.675 * logicalHeight, 0.205 * logicalWidth,
                                   0.130 * logicalHeight),
                            8, 8);

    const std::vector<DrawItem> items = layoutItems(logicalWidth, logicalHeight, positive);
    for (const DrawItem& item : items) {
        const QRectF box(item.logical.x, item.logical.y, item.logical.w, item.logical.h);
        if (item.track) {
            painter.setPen(Qt::NoPen);
            painter.setBrush(palette.border);
            painter.drawRoundedRect(box, box.height() / 2.0, box.height() / 2.0);
            painter.setBrush(positive ? palette.accent : palette.muted);
            painter.drawRoundedRect(QRectF(box.x(), box.y(), box.width() * (positive ? 0.62 : 0.30),
                                           box.height()),
                                    box.height() / 2.0, box.height() / 2.0);
            scene.controls.push_back(FixtureControl{item.id,
                                                    Rect{item.logical.x, item.logical.y, item.logical.w, item.logical.h}});
            continue;
        }
        if (item.chip) {
            painter.setPen(QPen(palette.border, std::max(1.0, 1.0)));
            painter.setBrush(palette.panel);
            painter.drawRoundedRect(box, box.height() * 0.22, box.height() * 0.22);
        }
        QColor iconColor = item.muted ? palette.muted : palette.text;
        if (!item.outline && item.chip && !item.text.empty()) iconColor = palette.accent;
        if (!item.text.empty()) {
            painter.setFont(smallFont);
            painter.setPen(iconColor);
            QRectF textBox = box;
            if (item.icon >= 0) {
                const double iconSide = std::min(box.height() * 0.62, box.height() * 0.62);
                const QRectF iconBox(box.x() + box.height() * 0.28, box.center().y() - iconSide / 2.0, iconSide,
                                     iconSide);
                drawIcon(painter, item.icon, iconBox, iconColor, item.outline, item.variant);
                textBox.setLeft(iconBox.right() + box.height() * 0.22);
            }
            painter.drawText(textBox, Qt::AlignVCenter | Qt::AlignLeft, toQString(item.text));
        } else if (item.icon >= 0) {
            drawIcon(painter, item.icon, box, iconColor, item.outline, item.variant);
        }
        scene.controls.push_back(
            FixtureControl{item.id, Rect{item.logical.x, item.logical.y, item.logical.w, item.logical.h}});
    }

    // 右侧设置卡片（底板已先绘制，不作为模板）
    painter.end();

    // 逻辑坐标 → 物理像素矩形
    for (FixtureControl& control : scene.controls) {
        const int left = int(std::lround(double(control.rect.x) * scale));
        const int top = int(std::lround(double(control.rect.y) * scale));
        const int right = int(std::lround(double(control.rect.right()) * scale));
        const int bottom = int(std::lround(double(control.rect.bottom()) * scale));
        control.rect = Rect{left, top, right - left, bottom - top};
    }
    return scene;
}

QImage cropTemplate(const QImage& scene, const Rect& rect, int padding) {
    const Rect padded{rect.x - padding, rect.y - padding, rect.w + 2 * padding, rect.h + 2 * padding};
    const QRect clipped = QRect(padded.x, padded.y, padded.w, padded.h).intersected(scene.rect());
    return scene.copy(clipped);
}

const std::vector<std::string>& positiveControlIds() {
    static const std::vector<std::string> ids = {
        "nav_home", "nav_devices", "nav_sessions", "nav_settings", "nav_help",
        "top_search", "top_fav", "top_refresh", "top_more", "top_avatar",
        "icon_heart", "icon_bell", "icon_folder", "icon_search", "icon_plus", "icon_close", "icon_play",
        "icon_trash", "icon_download", "icon_upload", "icon_share", "icon_lock",
        "row_projects", "row_downloads", "row_shared",
        "slider_knob", "slider_track", "bottom_apply", "bottom_cancel", "card_gear", "status_text"};
    return ids;
}

}  // namespace vis
