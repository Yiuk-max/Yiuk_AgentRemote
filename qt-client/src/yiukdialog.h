#pragma once

#include "apphelpers.h"

#include <QDialog>
#include <QLineEdit>

class QHBoxLayout;
class QVBoxLayout;

// 所有次级窗口和对话框共用的无边框弹窗界面
//
// 沿用主窗口的白色底板、共享 QSS 和自绘标题栏（标题在左，右侧复用主窗口的关闭图标）
// 所有弹窗都不会回退到 Windows 原生边框，操作按钮也统一使用共享工厂创建，避免复制样式表
class YiukDialog final : public QDialog {
public:
    enum class ButtonKind { Normal, Primary, Danger };

    YiukDialog(QWidget *parent, const QString &title);

    // 标题栏和按钮行之间的内容区域
    QVBoxLayout *content() const { return bodyLayout; }

    // 在底部按钮行追加一个操作按钮
    // 此处只调整按钮尺寸；样式由 makeButton/makePrimaryButton/makeDangerButton 提供
    QPushButton *addButton(const QString &text, ButtonKind kind = ButtonKind::Normal);

protected:
    void showEvent(QShowEvent *event) override;

private:
    QVBoxLayout *bodyLayout = nullptr;
    QHBoxLayout *buttonRow = nullptr;
    bool centered = false;
};

// QMessageBox/QInputDialog 的替代实现：保留原有信息层级
// 使用客户端自有的白色底板绘制，并去掉边框
void showInfoMessage(QWidget *parent, const QString &title, const QString &text);
void showWarningMessage(QWidget *parent, const QString &title, const QString &text);
bool askConfirm(QWidget *parent, const QString &title, const QString &text,
                const QString &acceptText, const QString &rejectText = QStringLiteral("取消"));
bool askText(QWidget *parent, const QString &title, const QString &label,
             QLineEdit::EchoMode echoMode, const QString &defaultValue,
             QString *outValue, const QString &acceptText = QStringLiteral("确定"));
