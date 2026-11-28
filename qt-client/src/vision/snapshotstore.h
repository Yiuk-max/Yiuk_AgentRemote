#pragma once

#include "screengrab.h"

#include <QHash>
#include <QMutex>
#include <QString>
#include <QStringList>
#include <QVector>

namespace yiuk::vision {

// 一张截图的元数据。始终只保存坐标/几何/文件名，不保存像素本身：
// 设备是用户正在用的机器，不能为了"以后可能要用"留一张全屏位图在内存里。
struct SnapshotRef {
    QString id;            // 12 位小写 hex，语义与 MCP 侧 screenshot_id 一致
    ScreenGeometry geometry;
    QString layoutVersion;  // 抓屏时的桌面布局摘要；与当前不一致即视为过期
    QString filePath;       // 落盘的临时截图文件（空 = 只回几何、没有图片）
    qint64 takenAtMs = 0;
};

// SnapshotStore 按会话保存最近若干张截图的元数据（默认 5 张）。
//
// 作用有两个：
//   1. 给 click / move_mouse / drag 校验 screenshot_id 是否仍是坐标基准；
//   2. 管理原生截图临时文件的生命周期（被挤出或会话结束时删除）。
//
// 线程安全：调用方可能是线程池里的多个 worker，所以内部用互斥锁保护。
class SnapshotStore {
public:
    static constexpr int kMaxPerSession = 5;

    // 生成 12 位小写 hex 的截图 id（48 bit 随机），与 Go 侧格式保持一致。
    static QString newId();

    // 登记一张新截图；返回 false 表示参数不合法。超过上限时删除最旧一条的文件。
    bool remember(const QString &sessionId, const SnapshotRef &ref);

    bool latest(const QString &sessionId, SnapshotRef *out) const;

    enum class Validation {
        Ok,             // id 就是最近一张，且布局没变
        Unknown,        // 本会话没有这张 id
        Stale,          // 有更新的截图（或布局变化），坐标基准已过期
    };
    // validate 只把 id 与"最近一张"比较：旧 id 一律过期，不提供"用第 N 张"的语义。
    Validation validate(const QString &sessionId, const QString &id, SnapshotRef *out = nullptr) const;

    // 只删掉这一条的文件（下载完成后回收），保留元数据：
    // screenshot_id 仍然是有效的坐标基准，只是没有图片可再取。
    void forgetFile(const QString &sessionId, const QString &path);

    // 会话结束：删除该会话登记的全部临时截图文件与元数据。
    void forgetSession(const QString &sessionId);

    // 桌面布局变化：所有旧 id 立即失效，并删除已落盘的临时文件。
    void forgetAll();

    int count(const QString &sessionId) const;

    // 测试与诊断用：当前登记的所有临时文件路径。
    QStringList trackedFiles() const;

private:
    mutable QMutex mutex_;
    QHash<QString, QVector<SnapshotRef>> bySession_;
};

}  // namespace yiuk::vision
