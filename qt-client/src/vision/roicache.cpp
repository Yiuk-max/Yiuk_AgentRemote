#include "roicache.h"

#include <QMutexLocker>

namespace yiuk::vision {

namespace {

// 模板像素的摘要：按行读，避开 QImage 每行的对齐填充（不能直接 hash bits()）。
QByteArray needleDigest(const QImage &needle) {
    QCryptographicHash hash(QCryptographicHash::Sha1);
    hash.addData(QByteArray::number(needle.width()));
hash.addData(QByteArrayLiteral("x"));
hash.addData(QByteArray::number(needle.height()));
    const int rowBytes = needle.width() * (needle.depth() / 8);
    for (int y = 0; y < needle.height(); ++y)
        hash.addData(reinterpret_cast<const char *>(needle.constScanLine(y)), rowBytes);
    return hash.result();
}
}  // namespace

QString RoiCache::imageKey(const QImage &needle, const TemplateOptions &options, const QString &layoutVersion) {
    const QImage gray = needle.isNull() ? needle : needle.convertToFormat(QImage::Format_Grayscale8);
    QCryptographicHash hash(QCryptographicHash::Sha1);
    hash.addData("roi-image-v1");
    hash.addData(layoutVersion.toUtf8());
    hash.addData(QByteArray::number(qRound64(options.threshold * 1000)));
    hash.addData(QByteArray::number(options.region.x()) + ',' + QByteArray::number(options.region.y()) + ',' +
                 QByteArray::number(options.region.width()) + ',' + QByteArray::number(options.region.height()));
    // 模板像素真的参与摘要：换了一张长得不一样的模板不能命中旧位置。
    hash.addData(needleDigest(gray));
    return QString::fromLatin1(hash.result().toHex());
}

QRect RoiCache::expand(const QRect &hit, int marginPx, const QRect &searchArea) {
    if (hit.isNull()) return QRect();
    const QRect expanded = hit.adjusted(-marginPx, -marginPx, marginPx, marginPx);
    if (searchArea.isNull()) return expanded;
    const QRect clamped = expanded.intersected(searchArea);
    return clamped.isNull() ? QRect() : clamped;
}

QRect RoiCache::lookup(const QString &key, qint64 nowMs) const {
    QMutexLocker locker(&mutex_);
    const auto it = entries_.constFind(key);
    if (it == entries_.constEnd()) return QRect();
    if (it->expiresAtMs <= nowMs) return QRect();
    return it->rect;
}

void RoiCache::store(const QString &key, const QRect &rect, qint64 nowMs) {
    if (key.isEmpty() || rect.isNull()) return;
    QMutexLocker locker(&mutex_);
    // 条目有界：满了先清理过期项，仍满就丢最早写入的那条（sequence 最小）。
    if (entries_.size() >= kMaxEntries) {
        for (auto it = entries_.begin(); it != entries_.end();) {
            if (it->expiresAtMs <= nowMs)
                it = entries_.erase(it);
            else
                ++it;
        }
        while (entries_.size() >= kMaxEntries) {
            auto oldest = entries_.begin();
            for (auto it = entries_.begin(); it != entries_.end(); ++it) {
                if (it->sequence < oldest->sequence) oldest = it;
            }
            entries_.erase(oldest);
        }
    }
    Entry entry;
    entry.rect = rect;
    entry.expiresAtMs = nowMs + kTtlMs;
    entry.sequence = ++sequence_;
    entries_.insert(key, entry);
}

void RoiCache::clear() {
    QMutexLocker locker(&mutex_);
    entries_.clear();
}

int RoiCache::size() {
    QMutexLocker locker(&mutex_);
    return entries_.size();
}

}  // namespace yiuk::vision
