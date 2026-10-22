#include "titlebar.h"

#include "apphelpers.h"

#include <QHBoxLayout>
#include <QMouseEvent>
#include <QPushButton>
#include <QWindow>

TitleBar::TitleBar(QWidget *parent, const QString &title)
    : QWidget(parent), dialogMode(!title.isEmpty()) {
    setFixedHeight(dialogMode ? 40 : 42);
    setStyleSheet("background: #ffffff;");
    auto *layout = new QHBoxLayout(this);
    layout->setContentsMargins(dialogMode ? 16 : 12, 0, 6, 0);
    layout->setSpacing(dialogMode ? 6 : 2);
    if (dialogMode) {
        // Same type ramp as the page headings, only smaller than the window
        // brand label; the close button is the main window's own glyph.
        layout->addWidget(makeLabel(title, "font-size: 13px; font-weight: 600; color: #263446;"));
        layout->addStretch();
    } else {
        layout->addStretch();
    }
    auto *minimize = windowButton(Glyph::Minimize, "minimize", "最小化");
    auto *maximize = windowButton(Glyph::Maximize, "maximize", "最大化");
    auto *close = windowButton(Glyph::Close, "close", "关闭");
    layout->addWidget(minimize);
    layout->addWidget(maximize);
    layout->addWidget(close);
    minimize->setVisible(!dialogMode);
    maximize->setVisible(!dialogMode);
    connect(minimize, &QPushButton::clicked, this, [this] { window()->hide(); });
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
    // Dialogs are not maximizable; only the main window keeps the double-click
    // toggle.
    if (event->button() == Qt::LeftButton && !dialogMode)
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
    // Title bar buttons never take keyboard focus, so a dialog still focuses its
    // first real input instead of the close glyph.
    button->setFocusPolicy(Qt::NoFocus);
    button->setFixedSize(38, 30);
    button->setStyleSheet(kind == "close"
        ? "QPushButton { border: 0; background: transparent; padding: 0; min-width: 0; } QPushButton:hover { background: #e81123; border-radius: 4px; }"
        : "QPushButton { border: 0; background: transparent; padding: 0; min-width: 0; } QPushButton:hover { background: #edf1f5; border-radius: 4px; }");
    return button;
}
