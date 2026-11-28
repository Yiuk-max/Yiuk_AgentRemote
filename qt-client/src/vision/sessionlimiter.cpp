#include "sessionlimiter.h"

#include <QDateTime>
#include <QThread>

#include <chrono>

namespace yiuk::vision {

namespace {

bool isCancelled(const std::atomic_bool *cancelled) {
    return cancelled != nullptr && cancelled->load();
}

qint64 nowMs() {
    return QDateTime::currentMSecsSinceEpoch();
}

}  // namespace

SessionLimiter::SessionLimiter(qint64 minIntervalMs) : minIntervalMs_(minIntervalMs) {}

void SessionLimiter::setMinIntervalMs(qint64 minIntervalMs) {
    std::lock_guard<std::mutex> lock(mutex_);
    minIntervalMs_ = minIntervalMs;
}

qint64 SessionLimiter::minIntervalMs() const {
    std::lock_guard<std::mutex> lock(mutex_);
    return minIntervalMs_;
}

bool SessionLimiter::acquire(const QString &sessionId, const std::atomic_bool *cancelled, QString *error) {
    if (sessionId.isEmpty()) {
        if (error) *error = QStringLiteral("会话 ID 为空");
        return false;
    }
    std::unique_lock<std::mutex> lock(mutex_);
    auto &slot = gates_[sessionId];
    if (!slot) slot = std::make_shared<Gate>();

    // 等占位：同一会话只允许一个重操作在跑。
    while (slot->busy) {
        if (isCancelled(cancelled)) {
            if (error) *error = QStringLiteral("操作已被取消");
            return false;
        }
        cv_.wait_for(lock, std::chrono::milliseconds(10));
    }
    slot->busy = true;

    // 最小间隔：从上一轮结束（释放）起算。等待期间仍响应取消。
    for (;;) {
        const qint64 waitMs = slot->lastMs + minIntervalMs_ - nowMs();
        if (waitMs <= 0) break;
        if (isCancelled(cancelled)) {
            slot->busy = false;
            cv_.notify_all();
            if (error) *error = QStringLiteral("操作已被取消");
            return false;
        }
        // 分段睡眠：既要响应取消，也要让其它会话的 acquire 能拿到锁。
        lock.unlock();
        QThread::msleep(static_cast<unsigned long>(qMin<qint64>(waitMs, 10)));
        lock.lock();
    }
    return true;
}

void SessionLimiter::release(const QString &sessionId) {
    std::lock_guard<std::mutex> lock(mutex_);
    const auto slot = gates_.value(sessionId);
    if (!slot) return;
    slot->lastMs = nowMs();
    slot->busy = false;
    cv_.notify_all();
}

}  // namespace yiuk::vision
