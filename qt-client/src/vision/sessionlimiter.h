#pragma once

#include <QHash>
#include <QString>

#include <atomic>
#include <condition_variable>
#include <memory>
#include <mutex>

namespace yiuk::vision {

// SessionLimiter 给"重操作"（抓屏 / 视觉查找）加两道门：
//
//   1. 同一会话内串行：避免 Agent 连发截图把低内存设备打满；
//   2. 最小间隔：两次重操作之间至少 500ms，给桌面和 CPU 留出恢复时间。
//
// 用条件变量而不是计数信号量，是为了让"排队期间的取消"能立刻生效：
// 平台取消一个请求时，排在后面的调用不能继续等满 500ms 才退出。
//
// 锁是会话级的，不是设备级的：多会话共用一台设备时由上层的设备级门（P2 后续）
// 继续收紧；这里先保证单个会话不会自相残杀。
class SessionLimiter {
public:
    static constexpr qint64 kDefaultMinIntervalMs = 500;

    explicit SessionLimiter(qint64 minIntervalMs = kDefaultMinIntervalMs);
    ~SessionLimiter() = default;

    // 取得一次重操作许可；失败（被取消）时返回 false 并写入 error。
    // 调用方必须在所有路径上调用 release()。
    bool acquire(const QString &sessionId, const std::atomic_bool *cancelled, QString *error);

    void release(const QString &sessionId);

    // 测试用：把最小间隔调小以便快速跑用例；生产路径不要调用。
    void setMinIntervalMs(qint64 minIntervalMs);
    qint64 minIntervalMs() const;

private:
    struct Gate {
        bool busy = false;
        qint64 lastMs = 0;
    };

    mutable std::mutex mutex_;
    std::condition_variable cv_;
    // shared_ptr 保证在 unlock 重新等待期间指针不会被 QHash 重哈希搬走。
    QHash<QString, std::shared_ptr<Gate>> gates_;
    qint64 minIntervalMs_;
};

}  // namespace yiuk::vision
