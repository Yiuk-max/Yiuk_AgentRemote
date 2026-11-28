#pragma once

#include <QImage>
#include <QRect>
#include <QString>
#include <QVector>

namespace yiuk::vision {

// 一次模板匹配的命中。rect 是 haystack（截图）里的坐标，右下开区间。
struct TemplateHit {
    QRect rect;
    double score = 0;
};

// 一次匹配的现场（即使零命中也有）：给失败 hint 用，避免一律建议“降低阈值”。
struct TemplateStats {
    // 搜索区域内的最高归一化得分（可能低于阈值）。
    double bestScore = 0;
    // 搜索区域几乎是纯色（灰度标准差很小）：模板匹配没有可用纹理。
    bool uniformArea = false;
    double areaStdDev = 0;
};

struct TemplateOptions {
    // TM_CCOEFF_NORMED 的阈值：P0 冻结 0.88。
    double threshold = 0.88;
    int limit = 5;
    // 在 haystack 里限定搜索区域（截图坐标；空 = 全图）。
    QRect region;
};

// OpenCV 是否编译进本客户端。没有时 matchTemplateImage 返回 UNSUPPORTED_OPERATION，
// 而不是伪装成"找不到"。
bool templateMatchingAvailable();

// 灰度 TM_CCOEFF_NORMED 模板匹配 + IoU 去重，结果按得分降序。
// 失败写 code：INVALID_ARGUMENT（图片为空/模板大于搜索区域）/
// UNSUPPORTED_OPERATION（未编译 OpenCV）。
bool matchTemplateImage(const QImage &haystack, const QImage &needle, const TemplateOptions &options,
                        QVector<TemplateHit> *hits, TemplateStats *stats, QString *code, QString *message);

}  // namespace yiuk::vision
