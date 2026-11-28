#include "apphelpers.h"

#include "vision/apppaths.h"

#include <QDir>
#include <QFile>
#include <QFileInfo>
#include <QJsonDocument>
#include <QRandomGenerator>
#include <QStandardPaths>

namespace {

QJsonObject readJsonObject(const QString &path) {
    QFile file(path);
    if (!file.open(QIODevice::ReadOnly)) return QJsonObject{};
    const QJsonDocument document = QJsonDocument::fromJson(file.readAll());
    return document.isObject() ? document.object() : QJsonObject{};
}

// 写 JSON 统一走临时文件 + rename：读取和写入不能共用同一个 QFile
// （文件还打开时 open() 会失败），而重命名单步替换可以避免截断出半个文件。
bool writeJsonObject(const QString &path, const QJsonObject &object) {
    if (!QDir().mkpath(QFileInfo(path).absolutePath())) return false;
    const QString temporaryPath = path + QStringLiteral(".tmp");
    QFile file(temporaryPath);
    if (!file.open(QIODevice::WriteOnly | QIODevice::Truncate)) return false;
    const QByteArray payload = QJsonDocument(object).toJson(QJsonDocument::Compact);
    if (file.write(payload) != payload.size()) {
        file.close();
        QFile::remove(temporaryPath);
        return false;
    }
    file.close();
    QFile::remove(path);
    if (QFile::rename(temporaryPath, path)) return true;
    QFile::remove(temporaryPath);
    return false;
}

// 早期版本把设备码放在 device.json、把平台凭据放在 platform.json。
// 两边的字段合并成 identity.json；DPAPI 保护过的字段原样搬过去，无需解密。
QJsonObject loadLegacyIdentity() {
    QJsonObject identity;
    const QJsonObject device = readJsonObject(existingOrDefaultConfigPath(QStringLiteral("device.json")));
    const QString deviceID = device.value(QStringLiteral("device_id")).toString();
    if (deviceID.size() == 9) identity.insert(QStringLiteral("device_id"), deviceID);
    const QJsonObject platformConfig =
        readJsonObject(existingOrDefaultConfigPath(QStringLiteral("platform.json")));
    for (auto it = platformConfig.begin(); it != platformConfig.end(); ++it) {
        if (it.key() == QLatin1String("enabled") || it.key() == QLatin1String("allow_insecure"))
            continue;
        identity.insert(it.key(), it.value());
    }
    return identity;
}

}  // namespace


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

namespace {

// 首次切到"程序目录 + data"时，把旧版放在 %LOCALAPPDATA%/%APPDATA% 的身份与配置搬过来，
// 否则同一台设备会被平台当成新设备（设备码/临时密码全变）。只搬一次，且不覆盖已有文件。
void migrateLegacyConfigOnce(const QString &target) {
    static bool done = false;
    if (done) return;
    done = true;
    const QStringList legacyRoots = [] {
        QStringList roots;
        if (const QString local = qEnvironmentVariable("LOCALAPPDATA"); !local.isEmpty())
            roots << QDir(local).filePath(QStringLiteral("Yiuk/DeviceAssist"));
        if (const QString roaming = qEnvironmentVariable("APPDATA"); !roaming.isEmpty())
            roots << QDir(roaming).filePath(QStringLiteral("Yiuk/DeviceAssist"));
        return roots;
    }();
    const QStringList names{QStringLiteral("platform.json"), QStringLiteral("platform-client.json"),
                            QStringLiteral("device.json"), QStringLiteral("sessions.json"),
                            QStringLiteral("identity.json"), QStringLiteral("local-api.json"),
                            QStringLiteral("identity.json")};
    for (const QString &name : names) {
        const QString destination = QDir(target).filePath(name);
        if (QFileInfo::exists(destination)) continue;
        for (const QString &root : legacyRoots) {
            const QString source = QDir(root).filePath(name);
            if (!QFileInfo::exists(source)) continue;
            QFile::copy(source, destination);
            break;
        }
    }
    // 旧的活动面板状态也一起搬。
    const QString panel = QDir(target).filePath(QStringLiteral("activity-panel.state"));
    if (!QFileInfo::exists(panel)) {
        for (const QString &root : legacyRoots) {
            const QString source = QDir(root).filePath(QStringLiteral("activity-panel.state"));
            if (!QFileInfo::exists(source)) continue;
            QFile::copy(source, panel);
            break;
        }
    }
}

}  // namespace

// 所有配置/会话/截图/脚本都放在程序目录下的 data\，跟随绿色目录一起走；
// 目录不可写时才回退到用户目录。
QString yiukConfigRoot() {
    const QString root = yiuk::vision::programDataPath(QString());
    migrateLegacyConfigOnce(root);
    return root;
}

QString legacyYiukConfigRoot() {
    const QString root = qEnvironmentVariable("APPDATA");
    return root.isEmpty() ? QString() : QDir(root).filePath("Yiuk/DeviceAssist");
}

QString yiukConfigPath(const QString &name) {
    return QDir(yiukConfigRoot()).filePath(name);
}

QString existingOrDefaultConfigPath(const QString &name) {
    const QString current = yiukConfigPath(name);
    if (QFileInfo::exists(current)) return current;
    const QString legacyRoot = legacyYiukConfigRoot();
    const QString legacy = legacyRoot.isEmpty() ? QString() : QDir(legacyRoot).filePath(name);
    return !legacy.isEmpty() && QFileInfo::exists(legacy) ? legacy : current;
}

QString defaultCommandWorkingDirectory() {
    // 命令的相对路径落点也留在程序目录里（data\work），不写到用户目录。
    return yiuk::vision::programDataPath(QStringLiteral("work"));
}

QString formatDeviceCode(const QString &digits) {
    if (digits.size() != 9) return digits;
    return digits.left(3) + " " + digits.mid(3, 3) + " " + digits.right(3);
}

QString formatTemporaryPassword(const QString &password) {
    QStringList groups;
    for (int offset = 0; offset < password.size(); offset += 8)
        groups.append(password.mid(offset, 8));
    return groups.join(" ");
}

QString generateRandomDigits(int count) {
    QString value;
    value.reserve(count);
    for (int i = 0; i < count; ++i)
        value += QString::number(QRandomGenerator::global()->bounded(10));
    return value;
}

// generateTemporaryPassword 按指定长度生成密码，字符表排除语音沟通中易混淆的字符。
QString generateTemporaryPassword(int count) {
    static const char alphabet[] = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789";
    const int alphabetSize = static_cast<int>(sizeof(alphabet)) - 1;
    QString value;
    value.reserve(count);
    for (int i = 0; i < count; ++i)
        value += QLatin1Char(alphabet[QRandomGenerator::global()->bounded(alphabetSize)]);
    return value;
}

QString joinJsonStringArray(const QJsonArray &array) {
    QStringList values;
    for (const QJsonValue &value : array) values.append(value.toString());
    return values.join(", ");
}

QJsonObject loadYiukIdentity() {
    QJsonObject identity = readJsonObject(existingOrDefaultConfigPath(QStringLiteral("identity.json")));
    if (!identity.isEmpty()) return identity;
    identity = loadLegacyIdentity();
    if (identity.isEmpty()) return identity;
    // 迁移写到新根目录，旧文件保留一版以便回滚。写失败不致命：
    // saveYiukIdentity 下次仍会从旧文件重建再写。
    writeJsonObject(yiukConfigPath(QStringLiteral("identity.json")), identity);
    return identity;
}

bool saveYiukIdentity(const QJsonObject &patch) {
    const QString path = existingOrDefaultConfigPath(QStringLiteral("identity.json"));
    QJsonObject identity = readJsonObject(path);
    if (identity.isEmpty()) identity = loadLegacyIdentity();
    for (auto it = patch.begin(); it != patch.end(); ++it) identity.insert(it.key(), it.value());
    return writeJsonObject(path, identity);
}
