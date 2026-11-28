#include "opencv_matcher.h"

#include <opencv2/core.hpp>
#include <opencv2/imgproc.hpp>

#include <algorithm>
#include <cmath>
#include <queue>

namespace vis {
namespace {

cv::Mat wrap(const GrayPlane& plane) {
    return cv::Mat(plane.h, plane.w, CV_8U, const_cast<uint8_t*>(plane.px.data()), size_t(plane.w));
}

GrayPlane unwrap(const cv::Mat& mat) {
    GrayPlane plane;
    plane.w = mat.cols;
    plane.h = mat.rows;
    plane.px.assign(mat.data, mat.data + mat.total());
    return plane;
}

struct Peak {
    double score = 0.0;
    int x = 0;
    int y = 0;
};

struct PeakMinHeap {
    bool operator()(const Peak& a, const Peak& b) const { return a.score > b.score; }
};

// patchNaNs 的返回值是修补数量：TM_CCOEFF_NORMED 在纯色区域会产出 NaN/Inf，
// 这一项直接回答“OpenCV 是否需要额外清理”。
std::vector<Peak> collectPeaks(cv::Mat& response, int templateW, int templateH, double floorScore, int keep,
                               double* bestScore, long long* nanPatched) {
    // 4.12 的 patchNaNs 返回 void；用 -2.0 作哨兵，在收集峰值时顺便统计 NaN 数量。
    cv::patchNaNs(response, -2.0);
    cv::max(response, -1.0, response);
    cv::min(response, 1.0, response);

    double best = -1.0;
    std::priority_queue<Peak, std::vector<Peak>, PeakMinHeap> top;
    for (int y = 0; y < response.rows; ++y) {
        const float* row = response.ptr<float>(y);
        for (int x = 0; x < response.cols; ++x) {
            const double score = double(row[x]);
            if (row[x] == -2.0f) {
                if (nanPatched) ++(*nanPatched);
                continue;
            }
            if (!std::isfinite(score)) continue;
            if (score > best) best = score;
            if (score < floorScore) continue;
            if (int(top.size()) < keep) {
                top.push(Peak{score, x, y});
            } else if (score > top.top().score) {
                top.pop();
                top.push(Peak{score, x, y});
            }
        }
    }
    if (bestScore && best > *bestScore) *bestScore = best;
    std::vector<Peak> peaks;
    while (!top.empty()) {
        peaks.push_back(top.top());
        top.pop();
    }
    return peaks;
}

std::vector<Match> suppressDuplicates(std::vector<Match> matches, int limit) {
    std::sort(matches.begin(), matches.end(), [](const Match& a, const Match& b) { return a.score > b.score; });
    std::vector<Match> kept;
    for (const Match& candidate : matches) {
        bool overlaps = false;
        for (const Match& existing : kept) {
            if (iou(candidate.box, existing.box) > 0.3) {
                overlaps = true;
                break;
            }
        }
        if (!overlaps) kept.push_back(candidate);
        if (int(kept.size()) >= limit) break;
    }
    return kept;
}

cv::Mat resized(const cv::Mat& source, double factor) {
    cv::Mat out;
    if (std::abs(factor - 1.0) < 1e-6) return source;
    const int w = std::max(1, int(std::lround(double(source.cols) * factor)));
    const int h = std::max(1, int(std::lround(double(source.rows) * factor)));
    cv::resize(source, out, cv::Size(w, h), 0, 0, factor < 1.0 ? cv::INTER_AREA : cv::INTER_LINEAR);
    return out;
}

Rect clampTopLeft(const GrayPlane& screen, int x, int y, int w, int h) {
    return Rect{std::max(0, std::min(screen.w - w, x)), std::max(0, std::min(screen.h - h, y)), w, h};
}

}  // namespace

SearchResult cvFind(const GrayPlane& screen, const GrayPlane& tmpl, const SearchParams& params, SearchMode mode) {
    SearchResult result;
    const double start = nowMs();
    if (!screen.valid() || !tmpl.valid()) {
        result.elapsedMs = nowMs() - start;
        return result;
    }
    const cv::Mat screenMat = wrap(screen);
    const cv::Mat tmplMat = wrap(tmpl);
    const std::vector<double> scales = scaleLadder(params);
    std::vector<Match> candidates;
    double bestScore = -1.0;

    const Rect searchArea = params.hasRoi
                                ? params.roi.intersect(Rect{0, 0, screen.w, screen.h})
                                : Rect{0, 0, screen.w, screen.h};

    if (mode == SearchMode::Exact) {
        for (double scale : scales) {
            const cv::Mat scaled = resized(tmplMat, scale);
            if (scaled.cols < 4 || scaled.rows < 4 || scaled.cols > screen.w || scaled.rows > screen.h) continue;
            const cv::Rect region(std::max(0, searchArea.x), std::max(0, searchArea.y),
                                  std::min(searchArea.w, screen.w - std::max(0, searchArea.x)),
                                  std::min(searchArea.h, screen.h - std::max(0, searchArea.y)));
            if (region.width < scaled.cols || region.height < scaled.rows) continue;
            const cv::Mat sub = screenMat(region);
            cv::Mat response;
            cv::matchTemplate(sub, scaled, response, cv::TM_CCOEFF_NORMED);
            const std::vector<Peak> peaks = collectPeaks(response, scaled.cols, scaled.rows, params.threshold, 24,
                                                        &bestScore, &result.nanPatched);
            for (const Peak& peak : peaks) {
                candidates.push_back(Match{Rect{region.x + peak.x, region.y + peak.y, scaled.cols, scaled.rows},
                                           peak.score, scale});
            }
        }
    } else {
        const int smallest = std::min(tmpl.w, tmpl.h);
        const int div = smallest >= 32 ? std::max(2, params.pyramidDiv) : 2;
        const double coarseFactor = 1.0 / double(div);
        const GrayPlane coarsePlane = unwrap(resized(screenMat, coarseFactor));
        const cv::Mat coarseScreen = wrap(coarsePlane);
        for (double scale : scales) {
            const cv::Mat scaled = resized(tmplMat, scale);
            if (scaled.cols < 6 || scaled.rows < 6 || scaled.cols > screen.w || scaled.rows > screen.h) continue;
            const cv::Mat coarseTemplate = resized(scaled, coarseFactor);
            if (coarseTemplate.cols < 4 || coarseTemplate.rows < 4 || coarseTemplate.cols > coarseScreen.cols ||
                coarseTemplate.rows > coarseScreen.rows) {
                continue;
            }
            const cv::Rect coarseRegion(std::max(0, searchArea.x), std::max(0, searchArea.y),
                                        std::min(searchArea.w, coarseScreen.cols - std::max(0, searchArea.x)),
                                        std::min(searchArea.h, coarseScreen.rows - std::max(0, searchArea.y)));
            if (coarseRegion.width < coarseTemplate.cols || coarseRegion.height < coarseTemplate.rows) continue;
            cv::Mat coarseResponse;
            cv::matchTemplate(coarseScreen(coarseRegion), coarseTemplate, coarseResponse, cv::TM_CCOEFF_NORMED);
            const std::vector<Peak> coarsePeaks = collectPeaks(coarseResponse, coarseTemplate.cols,
                                                               coarseTemplate.rows, params.coarseThreshold,
                                                               params.coarsePeaks, &bestScore, &result.nanPatched);
            for (const Peak& coarse : coarsePeaks) {
                const int margin = div + 2;
                const Rect window = clampTopLeft(screen, coarseRegion.x + coarse.x * div - margin,
                                                 coarseRegion.y + coarse.y * div - margin, scaled.cols + 2 * margin,
                                                 scaled.rows + 2 * margin);
                const cv::Mat crop = screenMat(cv::Rect(window.x, window.y, window.w, window.h));
                cv::Mat response;
                cv::matchTemplate(crop, scaled, response, cv::TM_CCOEFF_NORMED);
                const std::vector<Peak> refined = collectPeaks(response, scaled.cols, scaled.rows, -2.0, 1,
                                                               &bestScore, &result.nanPatched);
                for (const Peak& peak : refined) {
                    candidates.push_back(Match{Rect{window.x + peak.x, window.y + peak.y, scaled.cols, scaled.rows},
                                               peak.score, scale});
                }
            }
        }
    }

    result.bestScore = bestScore;
    std::vector<Match> above;
    for (const Match& candidate : candidates) {
        if (candidate.score >= params.threshold) above.push_back(candidate);
    }
    result.matches = suppressDuplicates(above, std::max(1, params.limit));
    result.elapsedMs = nowMs() - start;
    return result;
}

bool cvMaskedCcoeffFullAndPartial(std::string* reason, bool* partialAccepted) {
    if (partialAccepted) *partialAccepted = false;
    cv::Mat screen(64, 64, CV_8U, cv::Scalar(0));
    for (int y = 0; y < 64; ++y) {
        for (int x = 0; x < 64; ++x) screen.at<uint8_t>(y, x) = uint8_t((x * 3 + y * 5) % 251);
    }
    cv::Mat tmpl = screen(cv::Rect(16, 16, 16, 16)).clone();
    cv::Mat mask(16, 16, CV_8U, cv::Scalar(255));
    cv::Mat response;
    cv::Mat unmasked;
    try {
        cv::matchTemplate(screen, tmpl, unmasked, cv::TM_CCOEFF_NORMED);
        cv::matchTemplate(screen, tmpl, response, cv::TM_CCOEFF_NORMED, mask);
        // 全 255 的 mask 等价于不传 mask；如果结果不一致，说明“接受 mask”只是没报错，
        // 实际计算是坏的，比直接抛异常更危险。
        if (response.size() != unmasked.size()) {
            if (reason) *reason = "masked response size differs from unmasked";
            return false;
        }
        double maxDiff = 0.0;
        for (int y = 0; y < response.rows; ++y) {
            for (int x = 0; x < response.cols; ++x) {
                const double a = double(response.at<float>(y, x));
                const double b = double(unmasked.at<float>(y, x));
                if (std::isfinite(a) && std::isfinite(b)) maxDiff = std::max(maxDiff, std::fabs(a - b));
            }
        }
        if (maxDiff > 1e-4) {
            if (reason) *reason = "masked/unmasked responses differ by " + std::to_string(maxDiff);
            return false;
        }
    } catch (const cv::Exception& error) {
        if (reason) *reason = error.what();
        return false;
    } catch (const std::exception& error) {
        if (reason) *reason = error.what();
        return false;
    }
    // 部分 mask（一半像素为 0）：只验证“是否被接受”，数值正确性留到 P3 用素材验证。
    if (partialAccepted) {
        cv::Mat partialMask(16, 16, CV_8U, cv::Scalar(255));
        for (int y = 0; y < 16; ++y) {
            for (int x = 0; x < 8; ++x) partialMask.at<uint8_t>(y, x) = 0;
        }
        try {
            cv::Mat partialResponse;
            cv::matchTemplate(screen, tmpl, partialResponse, cv::TM_CCOEFF_NORMED, partialMask);
            *partialAccepted = true;
        } catch (const cv::Exception&) {
            *partialAccepted = false;
        }
    }
    if (reason) reason->clear();
    return true;
}

}  // namespace vis
