#include "screengrab.h"

#include <QCryptographicHash>
#include <QDir>
#include <QFileInfo>
#include <QJsonObject>
#include <QStringList>

#include <windows.h>

namespace yiuk::vision {

namespace {

// GDI 句柄的 RAII：任何提前返回都会按相反顺序释放，
// 长时间运行（每次截图都建 DC 与位图）时不会靠"记得写每一条清理"来避免泄漏。
struct GdiScope {
    HDC screenDc = nullptr;
    HDC memoryDc = nullptr;
    HBITMAP bitmap = nullptr;
    HGDIOBJ previous = nullptr;

    ~GdiScope() {
        if (memoryDc != nullptr && previous != nullptr) SelectObject(memoryDc, previous);
        if (bitmap != nullptr) DeleteObject(bitmap);
        if (memoryDc != nullptr) DeleteDC(memoryDc);
        if (screenDc != nullptr) ReleaseDC(nullptr, screenDc);
    }
};

}  // namespace

bool captureDesktop(ScreenCapture *capture, QString *error) {
    if (capture == nullptr) return false;
    // Per-Monitor V2：与 internal/agenttools/scripts.go 的 SetProcessDpiAwarenessContext 等价，
    // 保证这里拿到的尺寸是物理像素，而不是被系统虚拟化的逻辑像素。
    SetProcessDpiAwarenessContext(DPI_AWARENESS_CONTEXT_PER_MONITOR_AWARE_V2);

    const int left = GetSystemMetrics(SM_XVIRTUALSCREEN);
    const int top = GetSystemMetrics(SM_YVIRTUALSCREEN);
    const int width = GetSystemMetrics(SM_CXVIRTUALSCREEN);
    const int height = GetSystemMetrics(SM_CYVIRTUALSCREEN);
    if (width <= 0 || height <= 0) {
        if (error) *error = QStringLiteral("系统没有报告可用的桌面尺寸（可能处于锁屏或安全桌面）");
        return false;
    }

    GdiScope gdi;
    gdi.screenDc = GetDC(nullptr);
    if (gdi.screenDc == nullptr) {
        if (error) *error = QStringLiteral("无法获取桌面设备上下文");
        return false;
    }

    BITMAPINFO info{};
    info.bmiHeader.biSize = sizeof(BITMAPINFOHEADER);
    info.bmiHeader.biWidth = width;
    info.bmiHeader.biHeight = -height;  // 负高度 = 自上而下，与 QImage 行序一致
    info.bmiHeader.biPlanes = 1;
    info.bmiHeader.biBitCount = 32;
    info.bmiHeader.biCompression = BI_RGB;

    void *bits = nullptr;
    gdi.memoryDc = CreateCompatibleDC(gdi.screenDc);
    gdi.bitmap = CreateDIBSection(gdi.screenDc, &info, DIB_RGB_COLORS, &bits, nullptr, 0);
    if (gdi.memoryDc == nullptr || gdi.bitmap == nullptr || bits == nullptr) {
        if (error) *error = QStringLiteral("无法创建抓屏位图");
        return false;
    }

    gdi.previous = SelectObject(gdi.memoryDc, gdi.bitmap);
    // CAPTUREBLT 让分层窗口（菜单、tooltip、部分自绘控件）也出现在截图里。
    const BOOL copied = BitBlt(gdi.memoryDc, 0, 0, width, height, gdi.screenDc, left, top, SRCCOPY | CAPTUREBLT);
    GdiFlush();
    if (!copied) {
        if (error) *error = QStringLiteral("抓屏失败（桌面可能被锁定或处于安全桌面）");
        return false;
    }

    // DIB 的像素在 BitBlt 之后由我们独占，直接构造再 copy：避免 QImage 引用已释放的内存。
    const QImage view(static_cast<const uchar *>(bits), width, height, width * 4, QImage::Format_ARGB32);
    capture->image = view.copy();

    if (capture->image.isNull()) {
        if (error) *error = QStringLiteral("抓屏结果为空");
        return false;
    }
    capture->geometry.left = left;
    capture->geometry.top = top;
    capture->geometry.screenWidth = width;
    capture->geometry.screenHeight = height;
    capture->geometry.width = capture->image.width();
    capture->geometry.height = capture->image.height();
    return true;
}

QJsonObject geometryToJson(const ScreenGeometry &geometry) {
    return QJsonObject{
        {QStringLiteral("screen_left"), geometry.left},
        {QStringLiteral("screen_top"), geometry.top},
        {QStringLiteral("screen_width"), geometry.screenWidth},
        {QStringLiteral("screen_height"), geometry.screenHeight},
        {QStringLiteral("width"), geometry.width},
        {QStringLiteral("height"), geometry.height},
    };
}

namespace {

QString monitorRectString(HMONITOR monitor) {
    MONITORINFO info{};
    info.cbSize = sizeof(info);
    if (!GetMonitorInfoW(monitor, &info)) return QString();
    const RECT &rect = info.rcMonitor;
    // 物理像素矩形；DPI 单独记录，因为同样的矩形在不同缩放下代表不同的像素密度。
    int dpiX = 96;
    int dpiY = 96;
    // GetDpiForMonitor 需要 shcore.dll；这里用 GetDeviceCaps 避免额外链接依赖。
    if (HDC dc = CreateDCW(L"DISPLAY", nullptr, nullptr, nullptr)) {
        dpiX = GetDeviceCaps(dc, LOGPIXELSX);
        dpiY = GetDeviceCaps(dc, LOGPIXELSY);
        DeleteDC(dc);
    }
    return QStringLiteral("%1,%2,%3,%4@%5x%6")
        .arg(rect.left)
        .arg(rect.top)
        .arg(rect.right)
        .arg(rect.bottom)
        .arg(dpiX)
        .arg(dpiY);
}

BOOL CALLBACK collectMonitor(HMONITOR monitor, HDC, LPRECT, LPARAM data) {
    auto *rects = reinterpret_cast<QStringList *>(data);
    const QString rect = monitorRectString(monitor);
    if (!rect.isEmpty()) rects->append(rect);
    return TRUE;
}

}  // namespace

QString currentLayoutVersion() {
    SetProcessDpiAwarenessContext(DPI_AWARENESS_CONTEXT_PER_MONITOR_AWARE_V2);
    QStringList parts;
    parts << QStringLiteral("virtual:%1,%2,%3,%4")
                 .arg(GetSystemMetrics(SM_XVIRTUALSCREEN))
                 .arg(GetSystemMetrics(SM_YVIRTUALSCREEN))
                 .arg(GetSystemMetrics(SM_CXVIRTUALSCREEN))
                 .arg(GetSystemMetrics(SM_CYVIRTUALSCREEN));
    QStringList monitors;
    EnumDisplayMonitors(nullptr, nullptr, collectMonitor, reinterpret_cast<LPARAM>(&monitors));
    // EnumDisplayMonitors 不保证顺序稳定：排序后再拼，避免同一次布局得到两个版本号。
    monitors.sort();
    parts << monitors;
    const QByteArray digest = QCryptographicHash::hash(parts.join(QLatin1Char('|')).toUtf8(), QCryptographicHash::Sha1).toHex();
    return QString::fromLatin1(digest.left(16));
}

QImage scaleToWidth(const QImage &image, int maxWidth) {
    if (image.isNull() || maxWidth <= 0 || image.width() <= maxWidth) return image;
    const int targetHeight = qMax(1, qRound(static_cast<double>(image.height()) * maxWidth / image.width()));
    return image.scaled(maxWidth, targetHeight, Qt::IgnoreAspectRatio, Qt::SmoothTransformation);
}

bool saveImage(const QImage &image, const QString &path, const QString &format, int quality, QString *error) {
    if (image.isNull()) {
        if (error) *error = QStringLiteral("没有可保存的图像");
        return false;
    }
    const QString normalized = format.compare(QLatin1String("jpeg"), Qt::CaseInsensitive) == 0
                                   || format.compare(QLatin1String("jpg"), Qt::CaseInsensitive) == 0
                               ? QStringLiteral("jpeg")
                               : QStringLiteral("png");
    const QFileInfo target(path);
    if (!QDir().mkpath(target.absolutePath())) {
        if (error) *error = QStringLiteral("无法创建截图目录 %1").arg(target.absolutePath());
        return false;
    }
    const char *encoder = normalized == QLatin1String("jpeg") ? "JPEG" : "PNG";
    const int effectiveQuality = normalized == QLatin1String("jpeg") ? qBound(1, quality, 100) : -1;
    if (!image.save(path, encoder, effectiveQuality)) {
        if (error) *error = QStringLiteral("写入截图文件失败：%1").arg(path);
        return false;
    }
    return true;
}

}  // namespace yiuk::vision
