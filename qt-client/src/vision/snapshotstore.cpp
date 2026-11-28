#include "snapshotstore.h"

#include <QDateTime>
#include <QFile>
#include <QFileInfo>
#include <QRandomGenerator>

namespace yiuk::vision {

QString SnapshotStore::newId() {
    // 12 位小写 hex = 48 bit 随机；只用于绑定坐标基准，不承担安全用途。
    QString id;
    id.reserve(12);
    while (id.size() < 12) id += QString::number(QRandomGenerator::global()->generate(), 16).rightJustified(8, QLatin1Char('0'));
    return id.left(12);
}

bool SnapshotStore::remember(const QString &sessionId, const SnapshotRef &ref) {
    if (sessionId.isEmpty() || ref.id.isEmpty()) return false;
    QString evictedFile;
    {
        QMutexLocker locker(&mutex_);
        QVector<SnapshotRef> &history = bySession_[sessionId];
        history.prepend(ref);
        // 像素只留最近一张：save_template 只对"当前这张"有意义，旧图留着只会吃内存。
        for (int i = 1; i < history.size(); ++i) history[i].image = QImage();
        while (history.size() > kMaxPerSession) {
            const SnapshotRef dropped = history.takeLast();
            // 只删除我们自己写下来的临时文件：调用方没给路径就不动。
            if (!dropped.filePath.isEmpty()) evictedFile = dropped.filePath;
        }
    }
    // 删文件放在锁外：QFile::remove 可能被安全软件拖慢，不能占着锁。
    if (!evictedFile.isEmpty()) QFile::remove(evictedFile);
    return true;
}

bool SnapshotStore::latest(const QString &sessionId, SnapshotRef *out) const {
    QMutexLocker locker(&mutex_);
    const QVector<SnapshotRef> history = bySession_.value(sessionId);
    if (history.isEmpty()) return false;
    if (out) *out = history.first();
    return true;
}

SnapshotStore::Validation SnapshotStore::validate(const QString &sessionId, const QString &id,
                                                  SnapshotRef *out) const {
    QMutexLocker locker(&mutex_);
    const QVector<SnapshotRef> history = bySession_.value(sessionId);
    if (history.isEmpty()) return Validation::Unknown;
    for (const SnapshotRef &ref : history) {
        if (ref.id != id) continue;
        if (out) *out = ref;
        // 只有"最近一张 + 布局没变"才算有效基准。
        if (ref.id != history.first().id) return Validation::Stale;
        if (ref.layoutVersion != currentLayoutVersion()) return Validation::Stale;
        return Validation::Ok;
    }
    return Validation::Unknown;
}

void SnapshotStore::forgetFile(const QString &sessionId, const QString &path) {
    if (path.isEmpty()) return;
    QMutexLocker locker(&mutex_);
    QVector<SnapshotRef> &history = bySession_[sessionId];
    for (SnapshotRef &ref : history) {
        if (QFileInfo(ref.filePath) == QFileInfo(path)) ref.filePath.clear();
    }
    // 文件本身由调用方删除：这里只负责不再引用它（避免被 prune 当成"仍在用"）。
}

void SnapshotStore::forgetSession(const QString &sessionId) {
    QStringList files;
    {
        QMutexLocker locker(&mutex_);
        const QVector<SnapshotRef> history = bySession_.take(sessionId);
        for (const SnapshotRef &ref : history) {
            if (!ref.filePath.isEmpty()) files << ref.filePath;
        }
    }
    for (const QString &file : files) QFile::remove(file);
}

void SnapshotStore::forgetAll() {
    QHash<QString, QVector<SnapshotRef>> dropped;
    {
        QMutexLocker locker(&mutex_);
        dropped.swap(bySession_);
    }
    for (const QVector<SnapshotRef> &history : dropped) {
        for (const SnapshotRef &ref : history) {
            if (!ref.filePath.isEmpty()) QFile::remove(ref.filePath);
        }
    }
}

int SnapshotStore::count(const QString &sessionId) const {
    QMutexLocker locker(&mutex_);
    return bySession_.value(sessionId).size();
}

QStringList SnapshotStore::trackedFiles() const {
    QMutexLocker locker(&mutex_);
    QStringList files;
    for (const QVector<SnapshotRef> &history : bySession_) {
        for (const SnapshotRef &ref : history) {
            if (!ref.filePath.isEmpty()) files << ref.filePath;
        }
    }
    return files;
}

}  // namespace yiuk::vision
