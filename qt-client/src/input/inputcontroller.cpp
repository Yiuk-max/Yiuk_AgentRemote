#include "inputcontroller.h"

#include <QThread>

#include <windows.h>

#include <dwmapi.h>

#include <cmath>

namespace yiuk::input {

namespace {

constexpr int kSampleWidth = 192;
constexpr int kSampleHeight = 108;
constexpr int kStepMs = 12;  // 8–16ms 之间，兼顾平滑与系统消息队列
constexpr uchar kStrongPixelDelta = 24;

// SendInput 的绝对坐标是 0..65535 的虚拟桌面归一化值。
int normalizeAxis(int value, int origin, int size) {
    if (size <= 1) return 0;
    const double ratio = (value - origin) * 65535.0 / (size - 1);
    if (ratio < 0) return 0;
    if (ratio > 65535) return 65535;
    return static_cast<int>(std::lround(ratio));
}

DWORD buttonDownFlag(const QString &button) {
    if (button.compare(QLatin1String("right"), Qt::CaseInsensitive) == 0) return MOUSEEVENTF_RIGHTDOWN;
    if (button.compare(QLatin1String("middle"), Qt::CaseInsensitive) == 0) return MOUSEEVENTF_MIDDLEDOWN;
    return MOUSEEVENTF_LEFTDOWN;
}

DWORD buttonUpFlag(const QString &button) {
    if (button.compare(QLatin1String("right"), Qt::CaseInsensitive) == 0) return MOUSEEVENTF_RIGHTUP;
    if (button.compare(QLatin1String("middle"), Qt::CaseInsensitive) == 0) return MOUSEEVENTF_MIDDLEUP;
    return MOUSEEVENTF_LEFTUP;
}

bool isCancelled(const std::atomic_bool *cancelled) {
    return cancelled != nullptr && cancelled->load();
}

// 可取消的睡眠：长等待不能吞掉取消。
bool sleepCancellable(int ms, const std::atomic_bool *cancelled) {
    int remaining = ms;
    while (remaining > 0) {
        if (isCancelled(cancelled)) return false;
        const int slice = qMin(remaining, 10);
        QThread::msleep(static_cast<unsigned long>(slice));
        remaining -= slice;
    }
    return !isCancelled(cancelled);
}

bool sendMouse(DWORD flags, int screenX, int screenY) {
    INPUT input{};
    input.type = INPUT_MOUSE;
    input.mi.dwFlags = flags;
    input.mi.dx = normalizeAxis(screenX, GetSystemMetrics(SM_XVIRTUALSCREEN), GetSystemMetrics(SM_CXVIRTUALSCREEN));
    input.mi.dy = normalizeAxis(screenY, GetSystemMetrics(SM_YVIRTUALSCREEN), GetSystemMetrics(SM_CYVIRTUALSCREEN));
    return SendInput(1, &input, sizeof(INPUT)) == 1;
}

bool moveTo(int screenX, int screenY) {
    return sendMouse(MOUSEEVENTF_MOVE | MOUSEEVENTF_ABSOLUTE | MOUSEEVENTF_VIRTUALDESK, screenX, screenY);
}

}  // namespace

QString foregroundTitle() {
    HWND window = GetForegroundWindow();
    if (window == nullptr) return QString();
    wchar_t buffer[512] = {};
    const int length = GetWindowTextW(window, buffer, static_cast<int>(sizeof(buffer) / sizeof(buffer[0])));
    if (length <= 0) return QString();
    return QString::fromWCharArray(buffer, length);
}

ButtonGuard::~ButtonGuard() { release(); }

void ButtonGuard::release() {
    if (!held_) return;
    held_ = false;
    // 无论因为异常、取消还是超时离开作用域，都不能把鼠标键留在按下状态。
    sendMouse(buttonUpFlag(button_), 0, 0);
}

bool click(int screenX, int screenY, const QString &button, int count, const std::atomic_bool *cancelled,
           QString *error) {
    const int presses = qMax(1, count);
    if (!moveTo(screenX, screenY)) {
        if (error) *error = QStringLiteral("无法移动鼠标：桌面可能已锁定或处于安全桌面");
        return false;
    }
    if (!sleepCancellable(50, cancelled)) {
        if (error) *error = QStringLiteral("操作已被取消");
        return false;
    }
    for (int i = 0; i < presses; ++i) {
        if (isCancelled(cancelled)) {
            if (error) *error = QStringLiteral("操作已被取消");
            return false;
        }
        ButtonGuard guard(button);
        if (!sendMouse(buttonDownFlag(button), screenX, screenY)) {
            if (error) *error = QStringLiteral("鼠标按下被系统拒绝：桌面可能已锁定或处于安全桌面");
            return false;
        }
        guard.release();
        if (i + 1 < presses && !sleepCancellable(80, cancelled)) {
            if (error) *error = QStringLiteral("操作已被取消");
            return false;
        }
    }
    return true;
}

QVector<QPoint> buildPath(int fromX, int fromY, int toX, int toY, int durationMs) {
    QVector<QPoint> points;
    points.append(QPoint(fromX, fromY));
    if (durationMs <= 0 || (fromX == toX && fromY == toY)) {
        if (fromX != toX || fromY != toY) points.append(QPoint(toX, toY));
        return points;
    }
    const int steps = qMax(1, durationMs / kStepMs);
    points.reserve(steps + 1);
    for (int i = 1; i <= steps; ++i) {
        const double t = static_cast<double>(i) / steps;
        // ease-in-out（cubic）：起步与收尾都慢，中间快；确定性，无随机。
        const double eased = t < 0.5 ? 4 * t * t * t : 1 - std::pow(-2 * t + 2, 3) / 2;
        const int x = fromX + static_cast<int>(std::lround((toX - fromX) * eased));
        const int y = fromY + static_cast<int>(std::lround((toY - fromY) * eased));
        const QPoint point(x, y);
        if (points.last() != point) points.append(point);
    }
    // 终点必须精确，不能因为取整停在离目标 1px 的地方。
    if (points.last() != QPoint(toX, toY)) points.append(QPoint(toX, toY));
    return points;
}

bool drag(int fromX, int fromY, int toX, int toY, int durationMs, int holdMs, const std::atomic_bool *cancelled,
          QString *error) {
    if (!moveTo(fromX, fromY)) {
        if (error) *error = QStringLiteral("无法移动鼠标到拖动起点：桌面可能已锁定或处于安全桌面");
        return false;
    }
    if (!sleepCancellable(50, cancelled)) {
        if (error) *error = QStringLiteral("操作已被取消");
        return false;
    }
    ButtonGuard guard(QStringLiteral("left"));
    if (!sendMouse(MOUSEEVENTF_LEFTDOWN, fromX, fromY)) {
        if (error) *error = QStringLiteral("鼠标按下被系统拒绝：桌面可能已锁定或处于安全桌面");
        return false;
    }
    if (holdMs > 0 && !sleepCancellable(holdMs, cancelled)) {
        if (error) *error = QStringLiteral("操作已被取消");
        return false;  // guard 在这里抬起按键
    }
    const QVector<QPoint> path = buildPath(fromX, fromY, toX, toY, durationMs);
    for (int i = 1; i < path.size(); ++i) {
        if (isCancelled(cancelled)) {
            if (error) *error = QStringLiteral("操作已被取消");
            return false;  // guard 抬起按键，不会把窗口/滑块留在拖动状态
        }
        const QPoint &point = path.at(i);
        if (!moveTo(point.x(), point.y())) {
            if (error) *error = QStringLiteral("拖动过程中无法移动鼠标");
            return false;
        }
        QThread::msleep(kStepMs);
    }
    guard.release();
    return true;
}

bool move(int screenX, int screenY, int durationMs, const std::atomic_bool *cancelled, QString *error) {
    if (durationMs <= 0) {
        if (!moveTo(screenX, screenY)) {
            if (error) *error = QStringLiteral("无法移动鼠标：桌面可能已锁定或处于安全桌面");
            return false;
        }
        return true;
    }
    POINT current{};
    if (!GetCursorPos(&current)) current = POINT{screenX, screenY};
    const QVector<QPoint> path = buildPath(current.x, current.y, screenX, screenY, durationMs);
    for (int i = 1; i < path.size(); ++i) {
        if (isCancelled(cancelled)) {
            if (error) *error = QStringLiteral("操作已被取消");
            return false;
        }
        const QPoint &point = path.at(i);
        if (!moveTo(point.x(), point.y())) {
            if (error) *error = QStringLiteral("无法移动鼠标：桌面可能已锁定或处于安全桌面");
            return false;
        }
        QThread::msleep(kStepMs);
    }
    return true;
}

bool sampleRegion(int centerX, int centerY, QVector<uchar> *gray, QString *error) {
    if (gray == nullptr) return false;
    // 等 DWM 把最近的绘制合成到桌面：点击后立刻 BitBlt 可能拿到上一帧，
    // 那样"该变的地方"会被误判成没变。锁屏/无合成时 DwmFlush 会失败，忽略即可。
    DwmFlush();
    const int left = GetSystemMetrics(SM_XVIRTUALSCREEN);
    const int top = GetSystemMetrics(SM_YVIRTUALSCREEN);
    const int width = GetSystemMetrics(SM_CXVIRTUALSCREEN);
    const int height = GetSystemMetrics(SM_CYVIRTUALSCREEN);
    if (width < kSampleWidth || height < kSampleHeight) {
        if (error) *error = QStringLiteral("虚拟桌面小于变化检测采样区域");
        return false;
    }
    const int sx = qBound(left, centerX - kSampleWidth / 2, left + width - kSampleWidth);
    const int sy = qBound(top, centerY - kSampleHeight / 2, top + height - kSampleHeight);

    HDC screenDc = GetDC(nullptr);
    if (screenDc == nullptr) {
        if (error) *error = QStringLiteral("无法获取桌面设备上下文");
        return false;
    }
    BITMAPINFO info{};
    info.bmiHeader.biSize = sizeof(BITMAPINFOHEADER);
    info.bmiHeader.biWidth = kSampleWidth;
    info.bmiHeader.biHeight = -kSampleHeight;
    info.bmiHeader.biPlanes = 1;
    info.bmiHeader.biBitCount = 32;
    info.bmiHeader.biCompression = BI_RGB;

    void *bits = nullptr;
    HDC memoryDc = CreateCompatibleDC(screenDc);
    HBITMAP bitmap = CreateDIBSection(screenDc, &info, DIB_RGB_COLORS, &bits, nullptr, 0);
    if (memoryDc == nullptr || bitmap == nullptr || bits == nullptr) {
        if (bitmap) DeleteObject(bitmap);
        if (memoryDc) DeleteDC(memoryDc);
        ReleaseDC(nullptr, screenDc);
        if (error) *error = QStringLiteral("无法创建变化检测位图");
        return false;
    }
    HGDIOBJ previous = SelectObject(memoryDc, bitmap);
    const BOOL copied = BitBlt(memoryDc, 0, 0, kSampleWidth, kSampleHeight, screenDc, sx, sy, SRCCOPY | CAPTUREBLT);
    GdiFlush();
    QVector<uchar> values;
    if (copied) {
        values.resize(kSampleWidth * kSampleHeight);
        const auto *pixels = static_cast<const uchar *>(bits);
        for (int i = 0; i < values.size(); ++i) {
            const uchar *pixel = pixels + i * 4;
            // BGRA -> 灰度（与 PowerShell 的 (R+G+B)/3 同量级）
            values[i] = static_cast<uchar>((pixel[2] + pixel[1] + pixel[0]) / 3);
        }
    }
    SelectObject(memoryDc, previous);
    DeleteObject(bitmap);
    DeleteDC(memoryDc);
    ReleaseDC(nullptr, screenDc);
    if (!copied) {
        if (error) *error = QStringLiteral("采样点击区域失败：桌面可能已锁定或处于安全桌面");
        return false;
    }
    if (gray) *gray = values;
    return true;
}

ChangeMetrics compareSamples(const QVector<uchar> &before, const QVector<uchar> &after) {
    ChangeMetrics metrics;
    if (before.isEmpty() || before.size() != after.size()) return metrics;
    qint64 difference = 0;
    qint64 strongPixels = 0;
    for (int i = 0; i < before.size(); ++i) {
        const int delta = qAbs(static_cast<int>(before.at(i)) - static_cast<int>(after.at(i)));
        difference += delta;
        if (delta >= kStrongPixelDelta) ++strongPixels;
    }
    metrics.diffRatio = std::round(static_cast<double>(difference) / (before.size() * 255.0) * 10000.0) / 10000.0;
    metrics.changedRatio = std::round(static_cast<double>(strongPixels) / before.size() * 10000.0) / 10000.0;
    return metrics;
}

}  // namespace yiuk::input
