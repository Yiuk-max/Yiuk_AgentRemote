// 原生输入模块的行为测试：不注入键鼠，只验证可测的纯逻辑与"采样能拿到数据"。
//
// 覆盖：ease-in-out 路径的端点/单调性/可复现性、瞬移退化、灰度变化判据（含阈值边界）、
// 点击区域采样的尺寸与稳定性、前台标题（拿不到时不崩）。

#include "input/inputcontroller.h"

#include <QCoreApplication>
#include <QVector>

#include <algorithm>
#include <cstdio>

namespace {

int failures = 0;

void check(bool condition, const QString &what) {
    if (condition) {
        std::printf("ok   %s\n", qPrintable(what));
        return;
    }
    ++failures;
    std::printf("FAIL %s\n", qPrintable(what));
}

void testPath() {
    const QVector<QPoint> path = yiuk::input::buildPath(0, 0, 100, 50, 400);
    check(path.size() >= 3, QStringLiteral("smooth path has intermediate points"));
    check(path.first() == QPoint(0, 0), QStringLiteral("path starts at the source point"));
    check(path.last() == QPoint(100, 50), QStringLiteral("path ends exactly at the target point"));

    bool monotonicX = true;
    bool monotonicY = true;
    for (int i = 1; i < path.size(); ++i) {
        monotonicX = monotonicX && path.at(i).x() >= path.at(i - 1).x();
        monotonicY = monotonicY && path.at(i).y() >= path.at(i - 1).y();
    }
    check(monotonicX && monotonicY, QStringLiteral("path is monotonic towards the target"));

    const QVector<QPoint> again = yiuk::input::buildPath(0, 0, 100, 50, 400);
    check(path == again, QStringLiteral("path is deterministic for the same arguments"));

    // 反向移动同样单调递减，且不会被"起点在后"搞反。
    const QVector<QPoint> backward = yiuk::input::buildPath(100, 50, 0, 0, 240);
    bool decreasing = true;
    for (int i = 1; i < backward.size(); ++i)
        decreasing = decreasing && backward.at(i).x() <= backward.at(i - 1).x();
    check(decreasing, QStringLiteral("backward path is monotonic"));
    check(backward.last() == QPoint(0, 0), QStringLiteral("backward path ends at the target"));

    // duration 0 = 瞬移：只保留起点与终点。
    const QVector<QPoint> jump = yiuk::input::buildPath(10, 10, 20, 20, 0);
    check(jump.size() == 2 && jump.last() == QPoint(20, 20),
          QStringLiteral("duration 0 degenerates to a single jump"));

    // 起点等于终点时不产生多余点。
    const QVector<QPoint> still = yiuk::input::buildPath(7, 7, 7, 7, 300);
    check(still.size() == 1, QStringLiteral("no path when the pointer is already there"));

    // 缓动：中段位移应明显大于开头一步的位移（ease-in-out 的特征）。
    const QVector<QPoint> eased = yiuk::input::buildPath(0, 0, 1000, 0, 600);
    if (eased.size() > 6) {
        const int firstStep = eased.at(1).x() - eased.at(0).x();
        const int middleStep = eased.at(eased.size() / 2).x() - eased.at(eased.size() / 2 - 1).x();
        check(middleStep > firstStep, QStringLiteral("easing makes the start slower than the middle"));
    } else {
        check(false, QStringLiteral("eased path produced too few points to check"));
    }
}

void testChangeMetrics() {
    QVector<uchar> before(192 * 108, 100);
    QVector<uchar> after = before;
    yiuk::input::ChangeMetrics metrics = yiuk::input::compareSamples(before, after);
    check(metrics.diffRatio == 0 && metrics.changedRatio == 0,
          QStringLiteral("identical samples report no change"));
    check(!(metrics.changedRatio >= yiuk::input::kChangedPixelRatioThreshold ||
            metrics.diffRatio >= yiuk::input::kChangedMeanThreshold),
          QStringLiteral("identical samples are classified as unchanged"));

    // 一小块文字级变化：100 个像素从 100 变成 200（delta 100）。
    for (int i = 0; i < 100; ++i) after[i] = 200;
    metrics = yiuk::input::compareSamples(before, after);
    check(metrics.changedRatio > 0 && metrics.diffRatio > 0,
          QStringLiteral("localized change produces non-zero ratios"));
    check(metrics.changedRatio >= yiuk::input::kChangedPixelRatioThreshold,
          QStringLiteral("localized change crosses the strong-pixel threshold"));

    // 全屏平均变化 10：扩散到全部像素，足以过平均阈值但每个像素都不到"强变化"。
    QVector<uchar> uniform = before;
    for (int i = 0; i < uniform.size(); ++i) uniform[i] = static_cast<uchar>(before.at(i) + 10);
    metrics = yiuk::input::compareSamples(before, uniform);
    check(metrics.changedRatio == 0, QStringLiteral("small uniform deltas are not strong pixels"));
    check(metrics.diffRatio >= yiuk::input::kChangedMeanThreshold,
          QStringLiteral("uniform brightness change crosses the mean threshold"));

    // 尺寸不一致时返回零值，不崩。
    metrics = yiuk::input::compareSamples(before, QVector<uchar>());
    check(metrics.diffRatio == 0 && metrics.changedRatio == 0,
          QStringLiteral("mismatched sample sizes are rejected safely"));
}

void testSamplingAndTitle() {
    QVector<uchar> sample;
    QString error;
    if (yiuk::input::sampleRegion(100, 100, &sample, &error)) {
        check(sample.size() == 192 * 108, QStringLiteral("sample region returns 192x108 gray pixels"));
        QVector<uchar> second;
        check(yiuk::input::sampleRegion(100, 100, &second, &error) && second.size() == sample.size(),
              QStringLiteral("sampling twice keeps the same geometry"));
    } else {
        // 无桌面会话（CI / 服务进程）时不算失败，但必须明确没有验收。
        std::printf("WARN sampleRegion unavailable in this session: %s\n", qPrintable(error));
    }

    // 前台标题拿不到时必须是空字符串而不是崩溃。
    const QString title = yiuk::input::foregroundTitle();
    check(title.size() <= 4096, QStringLiteral("foreground title is bounded"));
}

}  // namespace

int main(int argc, char **argv) {
    QCoreApplication app(argc, argv);
    testPath();
    testChangeMetrics();
    testSamplingAndTitle();
    std::printf(failures == 0 ? "INPUT OK\n" : "INPUT FAILED (%d)\n", failures);
    return failures == 0 ? 0 : 1;
}
