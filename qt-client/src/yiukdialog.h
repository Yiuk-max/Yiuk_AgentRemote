#pragma once

#include "apphelpers.h"

#include <QDialog>
#include <QLineEdit>

class QHBoxLayout;
class QVBoxLayout;

// Frameless popup surface shared by every secondary window and dialog.
//
// It keeps the main window's white sheet, reused QSS and self-drawn title bar
// (title on the left, the main window's close glyph on the right), so no popup
// falls back to the native Windows chrome, and every action button comes from
// the shared button factories instead of a copied stylesheet.
class YiukDialog final : public QDialog {
public:
    enum class ButtonKind { Normal, Primary, Danger };

    YiukDialog(QWidget *parent, const QString &title);

    // Content area between the title bar and the button row.
    QVBoxLayout *content() const { return bodyLayout; }

    // Appends one action button to the bottom row. Only the box is resized here;
    // the look comes from makeButton/makePrimaryButton/makeDangerButton.
    QPushButton *addButton(const QString &text, ButtonKind kind = ButtonKind::Normal);

protected:
    void showEvent(QShowEvent *event) override;

private:
    QVBoxLayout *bodyLayout = nullptr;
    QHBoxLayout *buttonRow = nullptr;
    bool centered = false;
};

// QMessageBox/QInputDialog replacements: same information hierarchy as before,
// but frameless and drawn with the client's own white sheet.
void showInfoMessage(QWidget *parent, const QString &title, const QString &text);
void showWarningMessage(QWidget *parent, const QString &title, const QString &text);
bool askConfirm(QWidget *parent, const QString &title, const QString &text,
                const QString &acceptText, const QString &rejectText = QStringLiteral("取消"));
bool askText(QWidget *parent, const QString &title, const QString &label,
             QLineEdit::EchoMode echoMode, const QString &defaultValue,
             QString *outValue, const QString &acceptText = QStringLiteral("确定"));
