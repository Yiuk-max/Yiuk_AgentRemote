#include "templatematcher.h"

#include <algorithm>

#ifdef YIUK_HAS_OPENCV
#include <opencv2/core.hpp>
#include <opencv2/imgproc.hpp>
#endif

namespace yiuk::vision {

namespace {

QImage asGray(const QImage &image) {
    if (image.isNull()) return image;
    if (image.format() == QImage::Format_Grayscale8) return image;
    return image.convertToFormat(QImage::Format_Grayscale8);
}

}  // namespace

bool templateMatchingAvailable() {
#ifdef YIUK_HAS_OPENCV
    return true;
#else
    return false;
#endif
}

bool matchTemplateImage(const QImage &haystack, const QImage &needle, const TemplateOptions &options,
                        QVector<TemplateHit> *hits, QString *code, QString *message) {
    auto fail = [code, message](const QString &failureCode, const QString &failureMessage) {
        if (code) *code = failureCode;
        if (message) *message = failureMessage;
        return false;
    };
    if (hits == nullptr) return fail(QStringLiteral("INVALID_ARGUMENT"), QStringLiteral("hits 为空"));
    hits->clear();
    if (haystack.isNull() || needle.isNull())
        return fail(QStringLiteral("INVALID_ARGUMENT"), QStringLiteral("屏幕或模板图像为空"));
    if (needle.width() <= 0 || needle.height() <= 0)
        return fail(QStringLiteral("INVALID_ARGUMENT"), QStringLiteral("模板尺寸无效"));

#ifndef YIUK_HAS_OPENCV
    return fail(QStringLiteral("UNSUPPORTED_OPERATION"),
                QStringLiteral("当前客户端未编译 OpenCV，图像模板匹配不可用"));
#else
    const QImage hay = asGray(haystack);
    const QImage tpl = asGray(needle);
    if (hay.isNull() || tpl.isNull())
        return fail(QStringLiteral("INVALID_ARGUMENT"), QStringLiteral("灰度转换失败"));

    QRect region = options.region.isNull()
                       ? QRect(0, 0, hay.width(), hay.height())
                       : options.region.intersected(QRect(0, 0, hay.width(), hay.height()));
    if (region.width() < tpl.width() || region.height() < tpl.height())
        return fail(QStringLiteral("INVALID_ARGUMENT"),
                    QStringLiteral("模板 %1x%2 大于搜索区域 %3x%4")
                        .arg(tpl.width())
                        .arg(tpl.height())
                        .arg(region.width())
                        .arg(region.height()));

    cv::Mat hayMat(region.height(), region.width(), CV_8UC1,
                   const_cast<uchar *>(hay.constBits()) + static_cast<qsizetype>(region.top()) * hay.bytesPerLine() +
                       region.left(),
                   hay.bytesPerLine());
    cv::Mat tplMat(tpl.height(), tpl.width(), CV_8UC1, const_cast<uchar *>(tpl.constBits()), tpl.bytesPerLine());

    cv::Mat result;
    cv::matchTemplate(hayMat, tplMat, result, cv::TM_CCOEFF_NORMED);

    struct Candidate {
        double score;
        int x;
        int y;
    };
    QVector<Candidate> candidates;
    for (int y = 0; y < result.rows; ++y) {
        for (int x = 0; x < result.cols; ++x) {
            const float score = result.at<float>(y, x);
            if (score >= options.threshold) candidates.append(Candidate{score, x, y});
        }
    }
    std::sort(candidates.begin(), candidates.end(),
              [](const Candidate &a, const Candidate &b) { return a.score > b.score; });

    const int limit = std::max(1, options.limit);
    for (const Candidate &candidate : candidates) {
        const QRect rect(region.left() + candidate.x, region.top() + candidate.y, tpl.width(), tpl.height());
        bool overlaps = false;
        for (const TemplateHit &hit : *hits) {
            const QRect inter = rect.intersected(hit.rect);
            if (inter.isEmpty()) continue;
            const double overlap = static_cast<double>(inter.width()) * inter.height() /
                                   std::min(rect.width() * rect.height(), hit.rect.width() * hit.rect.height());
            if (overlap > 0.3) {  // NMS：IoU 0.3（P0 冻结）
                overlaps = true;
                break;
            }
        }
        if (overlaps) continue;
        hits->append(TemplateHit{rect, candidate.score});
        if (hits->size() >= limit) break;
    }
    return true;
#endif
}

}  // namespace yiuk::vision
