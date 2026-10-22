#pragma once

#include "glyph.h"

#include <QWidget>

class QPushButton;

// Frameless window title bar: brand label, drag-to-move and the three window
// buttons. Kept separate from MainWindow so the window class does not carry
// paint/drag code.
class TitleBar final : public QWidget {
public:
    explicit TitleBar(QWidget *parent = nullptr);

protected:
    void mousePressEvent(QMouseEvent *event) override;
    void mouseDoubleClickEvent(QMouseEvent *event) override;

private:
    QPushButton *windowButton(Glyph glyph, const QString &kind, const QString &tip);
};
