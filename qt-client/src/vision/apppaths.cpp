#include "apppaths.h"

#include <QCoreApplication>
#include <QDir>
#include <QFile>
#include <QStandardPaths>

namespace yiuk::vision {

namespace {

bool directoryIsWritable(const QString &path) {
    if (!QDir().mkpath(path)) return false;
    // 真正写一次：只判断权限位在 Windows 上不可靠（ACL、只读介质）。
    QFile probe(QDir(path).filePath(QStringLiteral(".yiuk-write-test")));
    if (!probe.open(QIODevice::WriteOnly)) return false;
    probe.close();
    probe.remove();
    return true;
}

struct Roots {
    QString portable;
    QString fallback;
    bool portableWritable = false;
};

const Roots &roots() {
    static const Roots value = [] {
        Roots result;
        result.portable = QDir(QCoreApplication::applicationDirPath()).filePath(QStringLiteral("data"));
        result.portableWritable = directoryIsWritable(result.portable);
        QString root = qEnvironmentVariable("LOCALAPPDATA");
        if (root.isEmpty()) root = qEnvironmentVariable("APPDATA");
        if (root.isEmpty()) root = QStandardPaths::writableLocation(QStandardPaths::AppLocalDataLocation);
        result.fallback = QDir(root).filePath(QStringLiteral("Yiuk/DeviceAssist"));
        return result;
    }();
    return value;
}

}  // namespace

QString programDataRoot() {
    const Roots &value = roots();
    return value.portableWritable ? value.portable : value.fallback;
}

bool programDataIsPortable() { return roots().portableWritable; }

QString programDataPath(const QString &relative) {
    const QString root = programDataRoot();
    if (relative.isEmpty()) return root;
    const QString path = QDir(root).filePath(relative);
    QDir().mkpath(path);
    return path;
}

}  // namespace yiuk::vision
