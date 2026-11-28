#pragma once

#include <QPoint>
#include <QString>
#include <QVector>

#include <atomic>

namespace yiuk::input {

// 原生鼠标输入。与 scripts.go 里的 PowerShell 路径行为一致：
//   * 坐标已经是物理屏幕坐标（截图坐标由 VisionService::toScreenPoint 换算）；
//   * 鼠标键必须在任何路径上抬起（P4 的 drag 会用同一套 RAII）；
//   * 移动轨迹是**确定性**的：同样的参数得到同样的点序列，不加随机抖动。

// 当前前台窗口标题；拿不到时返回空字符串。
QString foregroundTitle();

// 一次按下/抬起的安全保证：析构时如果还按着，就补发抬起。
class ButtonGuard {
public:
    explicit ButtonGuard(const QString &button) : button_(button) {}
    ~ButtonGuard();
    ButtonGuard(const ButtonGuard &) = delete;
    ButtonGuard &operator=(const ButtonGuard &) = delete;
    void release();
    bool held() const { return held_; }

private:
    QString button_;
    bool held_ = true;
};

// 绝对定位并点击（含双击）；返回 false 表示 SendInput 被系统拒绝（锁屏/安全桌面）。
bool click(int screenX, int screenY, const QString &button, int count, const std::atomic_bool *cancelled,
           QString *error);

// 移动到目标点。durationMs <= 0 时瞬移；否则按确定性 ease-in-out 分步移动，
// 用于触发依赖中间 mousemove 的悬停菜单 / dock / tooltip。
bool move(int screenX, int screenY, int durationMs, const std::atomic_bool *cancelled, QString *error);

// 把直线路径拆成 ease-in-out 的中间点（含起点与终点）。
// 纯函数：不碰系统 API，供测试验证端点、单调性与可复现性。
QVector<QPoint> buildPath(int fromX, int fromY, int toX, int toY, int durationMs);

// 采样点击点周围 192x108 的灰度（与原 PowerShell 判据同一块区域、同一判据）。
bool sampleRegion(int centerX, int centerY, QVector<uchar> *gray, QString *error);

struct ChangeMetrics {
    double diffRatio = 0;     // 平均差 / 255
    double changedRatio = 0;  // 变化 >= 24 灰阶的像素占比
};
ChangeMetrics compareSamples(const QVector<uchar> &before, const QVector<uchar> &after);

// 变化判据（与 Go 侧 desktop_state.go 保持一致）。
constexpr double kChangedPixelRatioThreshold = 0.002;
constexpr double kChangedMeanThreshold = 0.02;

}  // namespace yiuk::input
