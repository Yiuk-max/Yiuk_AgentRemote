#pragma once

#include <QImage>
#include <QRect>
#include <QJsonObject>
#include <QString>

namespace yiuk::vision {

// 一次抓屏的几何信息。
//
// 全部是**物理像素**：Windows 上开启了 Per-Monitor V2 DPI 感知后，
// 截到的位图与鼠标坐标用的是同一套坐标，和 MCP 侧 screenshot_id 的语义一致。
struct ScreenGeometry {
    int left = 0;           // 虚拟桌面左上角（可能为负，多显示器向左/上扩展）
    int top = 0;
    int screenWidth = 0;    // 虚拟桌面物理像素宽高
    int screenHeight = 0;
    int width = 0;          // 本次输出图像的宽高（P2 会按 max_width 缩放，本阶段等于物理尺寸）
    int height = 0;
};

struct ScreenCapture {
    QImage image;  // Format_ARGB32，虚拟桌面整块
    ScreenGeometry geometry;
};

// captureDesktop 抓取整块虚拟桌面。失败时返回 false 并写入 error（锁屏 / 安全桌面 / 会话不可见）。
bool captureDesktop(ScreenCapture *capture, QString *error);

QJsonObject geometryToJson(const ScreenGeometry &geometry);

// 截图坐标 -> 物理屏幕坐标（与 VisionService::screenRectToScreenshot 互逆）。
// OCR 在图像像素上出框，需要这一份换算回屏幕坐标。
QRect screenshotRectToScreen(const ScreenGeometry &geometry, const QRect &rect);

// 当前桌面布局的摘要（虚拟桌面矩形 + 每个显示器的物理矩形 + DPI）。
// 插拔显示器、改分辨率或改缩放都会让摘要变化，用于让旧的 screenshot_id 立即失效。
// 只描述布局，不包含屏幕内容。
QString currentLayoutVersion();

// 按最大宽度等比缩放；maxWidth <= 0 或图像本来就更窄时返回原图。
// 缩放使用平滑过滤：坐标要按输出图像尺寸换算，不能让像素格边界漂移。
QImage scaleToWidth(const QImage &image, int maxWidth);

// 把图像编码写入指定路径；format 只接受 "png" / "jpeg"。
// 写盘前按格式补充扩展名，失败时返回 false 并写入 error。
bool saveImage(const QImage &image, const QString &path, const QString &format, int quality, QString *error);

}  // namespace yiuk::vision
