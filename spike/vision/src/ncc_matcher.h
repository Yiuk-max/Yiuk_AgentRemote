#pragma once

#include <vector>

#include "gray_image.h"

namespace vis {

enum class SearchMode {
    Exact,    // 每个尺度在目标区域内全量扫描（慢但无漏检）
    Pyramid,  // 1/4 粗搜 → 原尺度精修
};

struct SearchParams {
    double scaleMin = 0.75;
    double scaleMax = 1.5;
    double scaleStep = 1.1;
    double threshold = 0.88;
    int limit = 5;
    Rect roi;
    bool hasRoi = false;
    int pyramidDiv = 2;
    int coarsePeaks = 16;
    double coarseThreshold = 0.2;
};

struct SearchResult {
    std::vector<Match> matches;   // 已 NMS、按得分降序、截断到 limit
    double bestScore = 0.0;       // 阈值以下的最高分也保留，供诊断
    double elapsedMs = 0.0;
    long long positionsEvaluated = 0;
    // OpenCV 路线专用：TM_CCOEFF_NORMED 产出的 NaN/Inf 数量（手写路线恒为 0）。
    long long nanPatched = 0;
};

std::vector<double> scaleLadder(const SearchParams& params);

SearchResult nccFind(const GrayPlane& screen, const GrayPlane& tmpl, const SearchParams& params, SearchMode mode);

}  // namespace vis
