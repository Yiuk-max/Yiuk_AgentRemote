#include "titlebar.h"

#include "apphelpers.h"

#include <QHBoxLayout>
#include <QMouseEvent>
#include <QPushButton>
#include <QWindow>

TitleBar::TitleBar(QWidget *parent) : QWidget(parent) {
    setFixedHeight(42);
    setStyleSheet("background: #ffffff;");
    auto *layout = new QHBoxLayout(this);
    layout->setContentsMargins(20, 0, 6, 0);
    layout->setSpacing(2);
    layout->addWidget(makeLabel("YIUK", "font-size: 15px; font-weight: 800; color: #1479ed;"));
    layout->addSpacing(10);
    layout->addWidget(makeLabel("设备协助", "font-size: 13px; font-weight: 600; color: #435168;"));
    layout->addStretch();
    auto *minimize = windowButton(Glyph::Minimize, "minimize", "最小化");
    auto *maximize = windowButton(Glyph::Maximize, "maximize", "最大化");
    auto *close = windowButton(Glyph::Close, "close", "关闭");
    layout->addWidget(minimize);
    layout->addWidget(maximize);
    layout->addWidget(close);
    connect(minimize, &QPushButton::clicked, this, [this] { window()->showMinimized(); });
    connect(maximize, &QPushButton::clicked, this, [this] {
        window()->isMaximized() ? window()->showNormal() : window()->showMaximized();
    });
    connect(close, &QPushButton::clicked, this, [this] { window()->close(); });
}

void TitleBar::mousePressEvent(QMouseEvent *event) {
    if (event->button() == Qt::LeftButton && window()->windowHandle())
        window()->windowHandle()->startSystemMove();
    QWidget::mousePressEvent(event);
}

void TitleBar::mouseDoubleClickEvent(QMouseEvent *event) {
    if (event->button() == Qt::LeftButton)
        window()->isMaximized() ? window()->showNormal() : window()->showMaximized();
    QWidget::mouseDoubleClickEvent(event);
}

QPushButton *TitleBar::windowButton(Glyph glyph, const QString &kind, const QString &tip) {
    auto *button = new QPushButton;
    button->setObjectName("window" + kind);
    button->setIcon(glyphIcon(glyph));
    button->setIconSize(QSize(18, 18));
    button->setToolTip(tip);
    button->setCursor(Qt::PointingHandCursor);
    button->setFixedSize(38, 30);
    button->setStyleSheet(kind == "close"
        ? "QPushButton { border: 0; background: transparent; padding: 0; } QPushButton:hover { background: #e81123; border-radius: 4px; }"
        : "QPushButton { border: 0; background: transparent; padding: 0; } QPushButton:hover { background: #edf1f5; border-radius: 4px; }");
    return button;
}
