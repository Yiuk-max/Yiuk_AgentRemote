#pragma once

#include "glyph.h"

#include <QWidget>

class QPushButton;

// 无边框窗口标题栏，包含品牌标签、拖动移动功能和三个窗口按钮
// 与 MainWindow 分离，避免窗口类承担绘制和拖动逻辑
//
// 传入标题后切换为对话框模式：标题显示在左侧，只保留关闭按钮
// 复用主窗口的关闭图标和行为，让所有弹窗都不使用 Windows 原生边框
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
