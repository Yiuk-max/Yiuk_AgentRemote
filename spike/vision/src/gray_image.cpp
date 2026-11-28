#include "gray_image.h"

#include <algorithm>
#include <cmath>

namespace vis {

GrayPlane toGray(const QImage& image) {
    GrayPlane plane;
    if (image.isNull()) return plane;
    QImage gray = image;
    if (gray.format() != QImage::Format_Grayscale8) gray = gray.convertToFormat(QImage::Format_Grayscale8);
    plane.w = gray.width();
    plane.h = gray.height();
    plane.px.resize(size_t(plane.w) * size_t(plane.h));
    for (int y = 0; y < plane.h; ++y) {
        std::copy_n(static_cast<const uint8_t*>(gray.constScanLine(y)), plane.w,
                    plane.px.data() + size_t(y) * size_t(plane.w));
    }
    return plane;
}

GrayPlane resample(const GrayPlane& plane, double factor) {
    GrayPlane out;
    if (!plane.valid() || factor <= 0.0) return out;
    const int outW = std::max(1, int(std::lround(double(plane.w) * factor)));
    const int outH = std::max(1, int(std::lround(double(plane.h) * factor)));
    out.w = outW;
    out.h = outH;
    out.px.assign(size_t(outW) * size_t(outH), 0);
    if (factor == 1.0) {
        out.px = plane.px;
        return out;
    }
    if (factor < 1.0) {
        // 盒式平均：粗搜阶段对细节的损失比双线性小，也不会引入过多插值伪影。
        const int stepX = std::max(1, int(std::lround(1.0 / factor)));
        const int stepY = stepX;
        for (int y = 0; y < outH; ++y) {
            for (int x = 0; x < outW; ++x) {
                const int x0 = std::min(plane.w - 1, x * stepX);
                const int y0 = std::min(plane.h - 1, y * stepY);
                const int x1 = std::min(plane.w, x0 + stepX);
                const int y1 = std::min(plane.h, y0 + stepY);
                double total = 0.0;
                for (int sy = y0; sy < y1; ++sy) {
                    const uint8_t* row = plane.row(sy);
                    for (int sx = x0; sx < x1; ++sx) total += row[sx];
                }
                const int count = std::max(1, (x1 - x0) * (y1 - y0));
                out.px[size_t(y) * size_t(outW) + size_t(x)] = uint8_t(std::lround(total / double(count)));
            }
        }
        return out;
    }
    // 放大：最近邻。模板放大本身会损失质量，这里只用于生成多尺度候选，
    // 命中后的精修仍在原尺度上比对原模板。
    for (int y = 0; y < outH; ++y) {
        const int sy = std::min(plane.h - 1, int(double(y) / factor));
        const uint8_t* srcRow = plane.row(sy);
        uint8_t* dstRow = out.px.data() + size_t(y) * size_t(outW);
        for (int x = 0; x < outW; ++x) {
            const int sx = std::min(plane.w - 1, int(double(x) / factor));
            dstRow[x] = srcRow[sx];
        }
    }
    return out;
}

void GrayIntegral::build(const GrayPlane& plane) {
    w = plane.w;
    h = plane.h;
    sum.assign(size_t(w + 1) * size_t(h + 1), 0.0);
    sq.assign(size_t(w + 1) * size_t(h + 1), 0.0);
    for (int y = 0; y < h; ++y) {
        const uint8_t* row = plane.row(y);
        double rowSum = 0.0;
        double rowSq = 0.0;
        for (int x = 0; x < w; ++x) {
            rowSum += row[x];
            rowSq += double(row[x]) * double(row[x]);
            const size_t index = size_t(y + 1) * size_t(w + 1) + size_t(x + 1);
            sum[index] = sum[size_t(y) * size_t(w + 1) + size_t(x + 1)] + rowSum;
            sq[index] = sq[size_t(y) * size_t(w + 1) + size_t(x + 1)] + rowSq;
        }
    }
}

double GrayIntegral::windowSum(int x, int y, int ww, int hh) const {
    const size_t stride = size_t(w + 1);
    return sum[size_t(y + hh) * stride + size_t(x + ww)] - sum[size_t(y) * stride + size_t(x + ww)] -
           sum[size_t(y + hh) * stride + size_t(x)] + sum[size_t(y) * stride + size_t(x)];
}

double GrayIntegral::windowSumSq(int x, int y, int ww, int hh) const {
    const size_t stride = size_t(w + 1);
    return sq[size_t(y + hh) * stride + size_t(x + ww)] - sq[size_t(y) * stride + size_t(x + ww)] -
           sq[size_t(y + hh) * stride + size_t(x)] + sq[size_t(y) * stride + size_t(x)];
}

}  // namespace vis
