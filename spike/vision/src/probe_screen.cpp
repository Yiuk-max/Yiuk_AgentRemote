// GDI 抓屏探针：测量真实桌面抓取耗时、虚拟桌面几何与灰度转换成本。
// 不落盘、不保存任何像素内容，只输出尺寸/耗时/校验和，避免把开发机桌面带进仓库。

#include <QGuiApplication>
#include <QImage>

#include <windows.h>

#include <algorithm>
#include <chrono>
#include <cstdio>
#include <vector>

#include "gray_image.h"
#include "vision_common.h"

namespace {

struct Capture {
    std::vector<uint8_t> bgra;
    int width = 0;
    int height = 0;
    int left = 0;
    int top = 0;
    bool ok = false;
};

// 与 internal/agenttools/scripts.go 的 CopyFromScreen 等价：整块虚拟桌面、物理像素。
Capture captureVirtualDesktop(HDC screenDc, int left, int top, int width, int height) {
    Capture capture;
    BITMAPINFO info{};
    info.bmiHeader.biSize = sizeof(BITMAPINFOHEADER);
    info.bmiHeader.biWidth = width;
    info.bmiHeader.biHeight = -height;  // 自上而下
    info.bmiHeader.biPlanes = 1;
    info.bmiHeader.biBitCount = 32;
    info.bmiHeader.biCompression = BI_RGB;
    void* bits = nullptr;
    HDC memoryDc = CreateCompatibleDC(screenDc);
    HBITMAP bitmap = CreateDIBSection(screenDc, &info, DIB_RGB_COLORS, &bits, nullptr, 0);
    if (!memoryDc || !bitmap || !bits) {
        if (bitmap) DeleteObject(bitmap);
        if (memoryDc) DeleteDC(memoryDc);
        return capture;
    }
    HGDIOBJ previous = SelectObject(memoryDc, bitmap);
    const BOOL copied = BitBlt(memoryDc, 0, 0, width, height, screenDc, left, top, SRCCOPY | CAPTUREBLT);
    GdiFlush();
    if (copied) {
        capture.bgra.assign(static_cast<uint8_t*>(bits), static_cast<uint8_t*>(bits) + size_t(width) * size_t(height) * 4);
        capture.width = width;
        capture.height = height;
        capture.left = left;
        capture.top = top;
        capture.ok = true;
    }
    SelectObject(memoryDc, previous);
    DeleteObject(bitmap);
    DeleteDC(memoryDc);
    return capture;
}

QImage toImage(const Capture& capture) {
    QImage image(capture.bgra.data(), capture.width, capture.height, capture.width * 4, QImage::Format_ARGB32);
    return image.copy();
}

}  // namespace

int main(int argc, char** argv) {
    QGuiApplication app(argc, argv);
    const int iterations = argc > 1 ? std::max(1, atoi(argv[1])) : 20;

    const int left = GetSystemMetrics(SM_XVIRTUALSCREEN);
    const int top = GetSystemMetrics(SM_YVIRTUALSCREEN);
    const int width = GetSystemMetrics(SM_CXVIRTUALSCREEN);
    const int height = GetSystemMetrics(SM_CYVIRTUALSCREEN);
    const HDC screenDc = GetDC(nullptr);
    if (!screenDc || width <= 0 || height <= 0) {
        std::printf("{\"ok\":false,\"error\":\"no screen dc\"}\n");
        return 1;
    }

    std::printf("{\"ok\":true,\"virtual_desktop\":{\"left\":%d,\"top\":%d,\"width\":%d,\"height\":%d},"
                "\"dpi\":%d,\"monitors\":%d,",
                left, top, width, height, GetDeviceCaps(screenDc, LOGPIXELSX), GetSystemMetrics(SM_CMONITORS));

    std::vector<double> grabTimes;
    std::vector<double> grayTimes;
    std::vector<double> scaleTimes;
    uint64_t checksum = 0;
    size_t grayBytes = 0;
    int scaledWidth = 0;
    int scaledHeight = 0;
    for (int i = 0; i < iterations; ++i) {
        const double grabStart = vis::nowMs();
        const Capture capture = captureVirtualDesktop(screenDc, left, top, width, height);
        grabTimes.push_back(vis::nowMs() - grabStart);
        if (!capture.ok) continue;
        const QImage image = toImage(capture);
        checksum += uint64_t(image.constScanLine(0)[0]) + uint64_t(image.width()) + uint64_t(image.height());

        const double grayStart = vis::nowMs();
        const vis::GrayPlane gray = vis::toGray(image);
        grayTimes.push_back(vis::nowMs() - grayStart);
        grayBytes = gray.px.size();

        const double scaleStart = vis::nowMs();
        const vis::GrayPlane scaled = vis::resample(gray, std::min(1.0, 1568.0 / double(gray.w)));
        scaleTimes.push_back(vis::nowMs() - scaleStart);
        scaledWidth = scaled.w;
        scaledHeight = scaled.h;
    }
    ReleaseDC(nullptr, screenDc);

    const vis::MemSample memory = vis::sampleMemory();
    std::printf("\"grab_ms\":%s,\"gray_ms\":%s,\"downscale_1568_ms\":%s,\"gray_bytes\":%zu,"
                "\"scaled\":{\"width\":%d,\"height\":%d},\"working_set_mb\":%.1f,\"checksum\":%llu}\n",
                vis::timingSummary(grabTimes).c_str(), vis::timingSummary(grayTimes).c_str(),
                vis::timingSummary(scaleTimes).c_str(), grayBytes, scaledWidth, scaledHeight, memory.workingSetMB,
                static_cast<unsigned long long>(checksum));
    return 0;
}
