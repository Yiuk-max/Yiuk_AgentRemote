#pragma once

#include "glyph.h"

#include <QWidget>

class QPushButton;

// Frameless window title bar: brand label, drag-to-move and the three window
// buttons. Kept separate from MainWindow so the window class does not carry
// paint/drag code.
//
// Passing a title switches to dialog mode: the title is drawn on the left and
// only the close button is kept, reusing the same glyph/behaviour as the main
// window so every popup drops the native Windows chrome.
class TitleBar final : public QWidget {
public:
    explicit TitleBar(QWidget *parent = nullptr, const QString &title = QString());

protected:
    void mousePressEvent(QMouseEvent *event) override;
    void mouseDoubleClickEvent(QMouseEvent *event) override;

private:
    QPushButton *windowButton(Glyph glyph, const QString &kind, const QString &tip);

    bool dialogMode = false;
};
