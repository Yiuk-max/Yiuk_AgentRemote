#pragma once

#include <QImage>

#include <vector>

#include "vision_common.h"

namespace vis {

// 灰度平面：匹配与 OCR 前置都只处理 8 位灰度，避免为视觉链路再分配彩色位图。
struct GrayPlane {
    int w = 0;
    int h = 0;
    std::vector<uint8_t> px;

    bool valid() const { return w > 0 && h > 0 && px.size() == size_t(w) * size_t(h); }
    const uint8_t* row(int y) const { return px.data() + size_t(y) * size_t(w); }
};

GrayPlane toGray(const QImage& image);

// 盒式滤波下采样：pyramid 粗搜与模板缩放共用。factor < 1 时缩小。
GrayPlane resample(const GrayPlane& plane, double factor);

// 灰度积分图：让任意窗口的和/平方和变成 O(1)，是 NCC 分母的主要成本来源。
struct GrayIntegral {
    int w = 0;
    int h = 0;
    std::vector<double> sum;   // (w+1)*(h+1)
    std::vector<double> sq;

    void build(const GrayPlane& plane);
    double windowSum(int x, int y, int w, int h) const;
    double windowSumSq(int x, int y, int w, int h) const;
};

}  // namespace vis
