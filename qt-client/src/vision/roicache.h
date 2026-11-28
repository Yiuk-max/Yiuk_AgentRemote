#pragma once

#include "templatematcher.h"

#include <QCryptographicHash>
#include <QHash>
#include <QMutex>
#include <QPoint>
#include <QRect>
#include <QString>

#include <algorithm>

namespace yiuk::vision {

// ROI 缓存：记住"上一次这个目标出现在哪"，下一次先在它附近找。
//
// 键 = 目标内容摘要 + 匹配参数 + 桌面布局版本。任何一项变了都是新键，旧条目自然失效，
// 所以不会出现"换了桌面还按旧坐标找"的情况。
//
// 三条纪律（P5 门禁）：
//   1. **只用于"只要一个命中"的只读查找**（`limit == 1`）。要多个候选时不许用，
//      否则会悄悄少报候选；
//   2. **click_target 一律不用**：`unique` / `best` 必须验证全域有没有竞争候选，
//      只看旧位置会漏掉歧义、点错目标——宁可慢，不可点错；
//   3. 命中框是**在当前帧上重新验证过的**，不是直接拿旧坐标当结果；TTL 与条目数都有界。
class RoiCache {
public:
    static constexpr int kMaxEntries = 32;
    static constexpr qint64 kTtlMs = 10000;
    // 在旧位置周围扩一圈再找：目标可能因为布局微调移动了几像素。
    static constexpr int kMarginPx = 64;

    // 内容摘要取灰度模板像素 + 阈值 + 来源缩放/region：同图不同参数不共享缓存。
    static QString imageKey(const QImage &needle, const TemplateOptions &options, const QString &layoutVersion);

    // 把旧的命中框扩成 ROI，并夹到搜索区域（含原有 region 约束）内。
    static QRect expand(const QRect &hit, int marginPx, const QRect &searchArea);

    // 返回旧位置（空 QRect 表示没有可用条目：过期或从未记录）。
    QRect lookup(const QString &key, qint64 nowMs) const;
    void store(const QString &key, const QRect &rect, qint64 nowMs);
    void clear();
    int size();

private:
    struct Entry {
        QRect rect;
        qint64 expiresAtMs = 0;
        quint64 sequence = 0;
    };
    mutable QMutex mutex_;
    QHash<QString, Entry> entries_;
    quint64 sequence_ = 0;
};

}  // namespace yiuk::vision
