#include "yiukdialog.h"

#include "titlebar.h"

#include <QGraphicsDropShadowEffect>
#include <QHBoxLayout>
#include <QPushButton>
#include <QShowEvent>
#include <QVBoxLayout>

namespace {

// 白色卡片周围留出空白，让投影自然淡出
// 投影只需将对话框与后方白色页面区分开，因此保持浅色，避免形成黑色边带
constexpr int kShadowMargin = 18;
constexpr int kShadowBlur = 22;
constexpr int kShadowAlpha = 64;
constexpr int kDialogButtonHeight = 34;
constexpr int kDialogButtonMinWidth = 92;

// 主体文本自动换行；下方的琥珀色标记承载原生消息框图标的警告提示
QLabel *bodyLabel(const QString &text, const QString &style = "font-size: 13px; color: #263446;") {
    auto *label = makeLabel(text, style);
    label->setTextFormat(Qt::PlainText);
    label->setWordWrap(true);
    label->setTextInteractionFlags(Qt::TextSelectableByMouse);
    return label;
}

QLabel *noticeBadge() {
    return makeLabel(QStringLiteral("注意"),
                     "font-size: 12px; font-weight: 600; color: #8a5b14; background: #fff6e5; "
                     "border: 1px solid #f3dfb0; border-radius: 10px; padding: 2px 10px;");
}

void centerOnParent(QWidget *popup) {
    QWidget *owner = popup->parentWidget();
    if (!owner) return;
    const QRect host = owner->window()->frameGeometry();
    popup->move(host.center() - QPoint(popup->width() / 2, popup->height() / 2));
}

}  // namespace

YiukDialog::YiukDialog(QWidget *parent, const QString &title) : QDialog(parent) {
    setWindowTitle(title);
    // 移除原生标题栏：对话框沿用主窗口的绘制方式，并使用共享的关闭按钮替代系统按钮
    setWindowFlags(Qt::Dialog | Qt::FramelessWindowHint);
    setModal(true);
    setSizeGripEnabled(false);
    // 窗口本身保持透明，使卡片投影可以渐隐到边距中
    // 白色表面和细边框由卡片绘制
    setAttribute(Qt::WA_TranslucentBackground, true);

    auto *outer = new QVBoxLayout(this);
    outer->setContentsMargins(kShadowMargin, kShadowMargin, kShadowMargin, kShadowMargin);
    outer->setSpacing(0);

    auto *card = new QWidget(this);
    card->setObjectName(QStringLiteral("dialogCard"));
    card->setAttribute(Qt::WA_StyledBackground, true);
    auto *shadow = new QGraphicsDropShadowEffect(card);
    shadow->setBlurRadius(kShadowBlur);
    shadow->setOffset(0, 4);
    shadow->setColor(QColor(24, 34, 48, kShadowAlpha));
    card->setGraphicsEffect(shadow);

    auto *cardLayout = new QVBoxLayout(card);
    cardLayout->setContentsMargins(1, 1, 1, 1);
    cardLayout->setSpacing(0);
    cardLayout->addWidget(new TitleBar(card, title));
    outer->addWidget(card);

    auto *body = new QWidget(card);
    bodyLayout = new QVBoxLayout(body);
    bodyLayout->setContentsMargins(20, 14, 20, 4);
    bodyLayout->setSpacing(10);
    cardLayout->addWidget(body, 1);

    // 按钮行位于独立的底部区域，因此调用方可以按任意顺序填充 content()，按钮仍会位于右下方
    auto *footer = new QWidget(card);
    auto *footerLayout = new QHBoxLayout(footer);
    footerLayout->setContentsMargins(20, 6, 20, 16);
    footerLayout->setSpacing(8);
    footerLayout->addStretch();
    buttonRow = footerLayout;
    cardLayout->addWidget(footer);
}

QPushButton *YiukDialog::addButton(const QString &text, ButtonKind kind) {
    QPushButton *button = nullptr;
    switch (kind) {
    case ButtonKind::Primary:
        button = makePrimaryButton(text, kDialogButtonHeight);
        break;
    case ButtonKind::Danger:
        button = makeDangerButton(text, kDialogButtonHeight);
        break;
    case ButtonKind::Normal:
        button = makeButton(text);
        button->setFixedHeight(kDialogButtonHeight);
        break;
    }
    button->setMinimumWidth(kDialogButtonMinWidth);
    buttonRow->addWidget(button);
    return button;
}

void YiukDialog::showEvent(QShowEvent *event) {
    QDialog::showEvent(event);
    if (centered) return;
    centered = true;
    centerOnParent(this);
}

void showInfoMessage(QWidget *parent, const QString &title, const QString &text) {
    YiukDialog dialog(parent, title);
    dialog.setFixedWidth(440);
    dialog.content()->addWidget(bodyLabel(text));
    dialog.content()->addStretch();
    auto *ok = dialog.addButton(QStringLiteral("知道了"), YiukDialog::ButtonKind::Primary);
    ok->setDefault(true);
    QObject::connect(ok, &QPushButton::clicked, &dialog, &QDialog::accept);
    dialog.adjustSize();
    dialog.exec();
}

void showWarningMessage(QWidget *parent, const QString &title, const QString &text) {
    YiukDialog dialog(parent, title);
    dialog.setFixedWidth(440);
    auto *badgeRow = new QHBoxLayout;
    badgeRow->addWidget(noticeBadge());
    badgeRow->addStretch();
    dialog.content()->addLayout(badgeRow);
    dialog.content()->addWidget(bodyLabel(text));
    dialog.content()->addStretch();
    auto *ok = dialog.addButton(QStringLiteral("知道了"), YiukDialog::ButtonKind::Primary);
    ok->setDefault(true);
    QObject::connect(ok, &QPushButton::clicked, &dialog, &QDialog::accept);
    dialog.adjustSize();
    dialog.exec();
}

bool askConfirm(QWidget *parent, const QString &title, const QString &text,
                const QString &acceptText, const QString &rejectText) {
    YiukDialog dialog(parent, title);
    dialog.setFixedWidth(440);
    dialog.content()->addWidget(bodyLabel(text));
    dialog.content()->addStretch();
    auto *reject = dialog.addButton(rejectText);
    auto *accept = dialog.addButton(acceptText, YiukDialog::ButtonKind::Primary);
    // 按 Enter 键不能触发危险操作确认
    reject->setDefault(true);
    accept->setAutoDefault(false);
    QObject::connect(reject, &QPushButton::clicked, &dialog, &QDialog::reject);
    QObject::connect(accept, &QPushButton::clicked, &dialog, &QDialog::accept);
    dialog.adjustSize();
    return dialog.exec() == QDialog::Accepted;
}

bool askText(QWidget *parent, const QString &title, const QString &label,
             QLineEdit::EchoMode echoMode, const QString &defaultValue,
             QString *outValue, const QString &acceptText) {
    YiukDialog dialog(parent, title);
    dialog.setFixedWidth(460);
    dialog.content()->addWidget(bodyLabel(label, "font-size: 13px; color: #5f6d80;"));
    auto *edit = new QLineEdit(defaultValue);
    // 复用设置页输入框样式，使提示输入框与主窗口一致
    // 避免使用主页无边框输入框的样式
    edit->setObjectName(QStringLiteral("settingsField"));
    edit->setEchoMode(echoMode);
    edit->setMinimumHeight(34);
    dialog.content()->addWidget(edit);
    dialog.content()->addStretch();
    auto *cancel = dialog.addButton(QStringLiteral("取消"));
    auto *accept = dialog.addButton(acceptText, YiukDialog::ButtonKind::Primary);
    accept->setDefault(true);
    QObject::connect(cancel, &QPushButton::clicked, &dialog, &QDialog::reject);
    QObject::connect(accept, &QPushButton::clicked, &dialog, &QDialog::accept);
    QObject::connect(edit, &QLineEdit::returnPressed, &dialog, &QDialog::accept);
    dialog.adjustSize();
    edit->setFocus();
    edit->selectAll();
    if (dialog.exec() != QDialog::Accepted) return false;
    if (outValue) *outValue = edit->text().trimmed();
    return true;
}
