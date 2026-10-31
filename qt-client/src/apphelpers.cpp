#include "apphelpers.h"

#include <QDir>
#include <QFileInfo>
#include <QRandomGenerator>
#include <QStandardPaths>


QLabel *makeLabel(const QString &text, const QString &style) {
    auto *label = new QLabel(text);
    label->setStyleSheet(style);
    return label;
}

QPushButton *makeButton(const QString &text, const QString &style) {
    auto *button = new QPushButton(text);
    button->setCursor(Qt::PointingHandCursor);
    button->setStyleSheet(style);
    return button;
}

QPushButton *makePrimaryButton(const QString &text, int height) {
    auto *button = makeButton(text);
    button->setObjectName("primary");
    if (height > 0) {
        // #primary 样式针对 48px 主按钮进行调校，这里只调整按钮尺寸
        button->setStyleSheet("QPushButton#primary { padding: 4px 16px; }");
        button->setFixedHeight(height);
    }
    return button;
}

QPushButton *makeDangerButton(const QString &text, int height) {
    auto *button = makeButton(text,
        "QPushButton { color: #a52b2b; border: 1px solid #e8bcbc; border-radius: 6px; "
        "padding: 7px 12px; background: #ffffff; } QPushButton:hover { background: #fff0f0; }");
    if (height > 0) button->setFixedHeight(height);
    return button;
}

QString yiukConfigRoot() {
    QString root = qEnvironmentVariable("LOCALAPPDATA");
    if (root.isEmpty()) root = qEnvironmentVariable("APPDATA");
    if (root.isEmpty()) root = QStandardPaths::writableLocation(QStandardPaths::AppLocalDataLocation);
    return QDir(root).filePath("Yiuk/DeviceAssist");
}

QString legacyYiukConfigRoot() {
    const QString root = qEnvironmentVariable("APPDATA");
    return root.isEmpty() ? QString() : QDir(root).filePath("Yiuk/DeviceAssist");
}

QString yiukConfigPath(const QString &name) {
    return QDir(yiukConfigRoot()).filePath(name);
}

QString findYiukConfigPath(const QString &name) {
    const QString current = yiukConfigPath(name);
    if (QFileInfo::exists(current)) return current;
    const QString legacyRoot = legacyYiukConfigRoot();
    const QString legacy = legacyRoot.isEmpty() ? QString() : QDir(legacyRoot).filePath(name);
    return !legacy.isEmpty() && QFileInfo::exists(legacy) ? legacy : current;
}

QString defaultCommandWorkingDirectory() {
    const QString home = QDir::homePath();
    return home.isEmpty() ? QDir::currentPath() : home;
}

QString spacedCode(const QString &digits) {
    if (digits.size() != 9) return digits;
    return digits.left(3) + " " + digits.mid(3, 3) + " " + digits.right(3);
}

QString groupedPassword(const QString &password) {
    QStringList groups;
    for (int offset = 0; offset < password.size(); offset += 8)
        groups.append(password.mid(offset, 8));
    return groups.join(" ");
}

QString randomDigits(int count) {
    QString value;
    value.reserve(count);
    for (int i = 0; i < count; ++i)
        value += QString::number(QRandomGenerator::global()->bounded(10));
    return value;
}

// randomPassword 生成由 32 个字符组成的 base32 密码
// 字符表排除了在语音沟通中容易混淆的字符
QString randomPassword(int count) {
    static const char alphabet[] = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789";
    const int alphabetSize = static_cast<int>(sizeof(alphabet)) - 1;
    QString value;
    value.reserve(count);
    for (int i = 0; i < count; ++i)
        value += QLatin1Char(alphabet[QRandomGenerator::global()->bounded(alphabetSize)]);
    return value;
}

QString jsonArrayText(const QJsonArray &array) {
    QStringList values;
    for (const QJsonValue &value : array) values.append(value.toString());
    return values.join(", ");
}
