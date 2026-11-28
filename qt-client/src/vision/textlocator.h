#pragma once

#include <QHash>
#include <QRect>
#include <QString>
#include <QVector>

namespace yiuk::vision {

// 一条文本命中。rect 是**物理屏幕坐标**（Per-Monitor V2），
// 由 VisionService 换算成截图坐标后才会发给操作者。
struct TextMatch {
    QRect rect;
    QString name;
    QString controlType;
    QString source = QStringLiteral("uia");
    double score = 0;
};

struct TextQuery {
    QString text;
    // contains（默认）| exact；大小写与全半角差异都会被归一化掉。
    QString match = QStringLiteral("contains");
    // 可选：按控件类型过滤（"Button" / "Edit" / ...，大小写不敏感）。
    QString controlType;
    // 可选：只在这个矩形内查找（物理屏幕坐标，空 = 全屏）。
    QRect region;
    int limit = 5;
    // 硬上限：不允许无界全树遍历（P0 实测 Edge 746 节点 35ms，5000 节点够用且可控）。
    int maxNodes = 5000;
    // 遍历时间预算：超限就带着已有结果返回，并标记 truncated。
    int timeBudgetMs = 300;
};

struct TextSearchOutcome {
    QVector<TextMatch> matches;
    bool truncated = false;
    int visitedNodes = 0;
    int elapsedMs = 0;
};

// 归一化：全角转半角、去掉所有空白（OCR 的中文结果字间带空格）、统一小写。
// contains / exact 都在归一化后的文本上比较，避免"收藏"与"收 藏"、"Save"与"save"失配。
QString normalizeText(const QString &value);

// 控件类型 id -> 名称（只覆盖常见类型，未知返回空）。
QString controlTypeName(int controlTypeId);
// 名称 -> 控件类型 id；未知返回 0。
int controlTypeId(const QString &name);

// 在前台窗口的控件树里按文本查找（UIA，控制视图）。
// 失败写 code：INVALID_ARGUMENT / UIA_UNAVAILABLE（锁屏、无前台窗口、COM 不可用）。
bool findText(const TextQuery &query, TextSearchOutcome *outcome, QString *code, QString *message);

}  // namespace yiuk::vision
