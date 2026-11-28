#include "ncc_matcher.h"

#include <algorithm>
#include <cmath>
#include <cstdio>
#include <cstdlib>
#include <queue>

#ifdef __SSE2__
#include <emmintrin.h>
#endif

namespace vis {
namespace {

struct TemplatePrep {
    GrayPlane plane;
    double mean = 0.0;
    double variance = 0.0;  // sum((t-mean)^2)
};

TemplatePrep prepareTemplate(const GrayPlane& plane) {
    TemplatePrep prep;
    prep.plane = plane;
    const double count = double(plane.w) * double(plane.h);
    double total = 0.0;
    for (int y = 0; y < plane.h; ++y) {
        const uint8_t* row = plane.row(y);
        for (int x = 0; x < plane.w; ++x) total += row[x];
    }
    prep.mean = total / count;
    double variance = 0.0;
    for (int y = 0; y < plane.h; ++y) {
        const uint8_t* row = plane.row(y);
        for (int x = 0; x < plane.w; ++x) {
            const double d = double(row[x]) - prep.mean;
            variance += d * d;
        }
    }
    prep.variance = variance;
    return prep;
}

// Σ screen(x,y) * template(x,y)。两个输入都是 u8，零扩展到 i16 后 madd，
// 累加不会溢出 int32（最大 255*255*4096 ≈ 2.7 亿）。
inline long long dotRows(const uint8_t* screen, const uint8_t* tmpl, int count) {
#ifdef __SSE2__
    const __m128i zero = _mm_setzero_si128();
    __m128i acc = zero;
    int i = 0;
    for (; i + 16 <= count; i += 16) {
        const __m128i s8 = _mm_loadu_si128(reinterpret_cast<const __m128i*>(screen + i));
        const __m128i t8 = _mm_loadu_si128(reinterpret_cast<const __m128i*>(tmpl + i));
        const __m128i sLo = _mm_unpacklo_epi8(s8, zero);
        const __m128i sHi = _mm_unpackhi_epi8(s8, zero);
        const __m128i tLo = _mm_unpacklo_epi8(t8, zero);
        const __m128i tHi = _mm_unpackhi_epi8(t8, zero);
        acc = _mm_add_epi32(acc, _mm_madd_epi16(sLo, tLo));
        acc = _mm_add_epi32(acc, _mm_madd_epi16(sHi, tHi));
    }
    __m128i high = _mm_shuffle_epi32(acc, _MM_SHUFFLE(1, 0, 3, 2));
    acc = _mm_add_epi32(acc, high);
    high = _mm_shuffle_epi32(acc, _MM_SHUFFLE(2, 3, 0, 1));
    acc = _mm_add_epi32(acc, high);
    long long total = _mm_cvtsi128_si32(acc);
    for (; i < count; ++i) total += int(screen[i]) * int(tmpl[i]);
    return total;
#else
    long long total = 0;
    for (int i = 0; i < count; ++i) total += int(screen[i]) * int(tmpl[i]);
    return total;
#endif
}

struct Peak {
    double score = 0.0;
    int x = 0;
    int y = 0;
};

struct PeakMinHeap {
    bool operator()(const Peak& a, const Peak& b) const { return a.score > b.score; }
};

// 在 [x0,x1] × [y0,y1] 的左上角允许范围内扫描，保留得分最高的 keep 个位置。
double scanArea(const GrayPlane& screen, const GrayIntegral& integral, const TemplatePrep& prep, int x0, int y0,
                int x1, int y1, double floorScore, int keep, std::vector<Peak>& peaks,
                long long& positionsEvaluated) {
    const GrayPlane& t = prep.plane;
    if (t.w <= 0 || t.h <= 0 || x1 < x0 || y1 < y0) return 0.0;
    const double count = double(t.w) * double(t.h);
    const double templateNorm = std::sqrt(std::max(0.0, prep.variance));
    double best = 0.0; int bestPosX = -1; int bestPosY = -1;
    std::priority_queue<Peak, std::vector<Peak>, PeakMinHeap> top;
    if (templateNorm < 1e-6) return 0.0;

    for (int y = y0; y <= y1; ++y) {
        for (int x = x0; x <= x1; ++x) {
            const double sum = integral.windowSum(x, y, t.w, t.h);
            const double sumSq = integral.windowSumSq(x, y, t.w, t.h);
            const double varScreen = sumSq - sum * sum / count;
            // 纯色区域没有相关性可言；也避免除以接近 0 的分母得到 NaN。
            if (varScreen <= 1e-3) continue;
            long long dot = 0;
            for (int ty = 0; ty < t.h; ++ty) {
                dot += dotRows(screen.row(y + ty) + x, t.row(ty), t.w);
            }
            ++positionsEvaluated;
            const double numerator = double(dot) - sum * prep.mean;
            const double score = numerator / (std::sqrt(varScreen) * templateNorm);
            if (!std::isfinite(score)) continue;
            if (score > best) { best = score; bestPosX = x; bestPosY = y; }
            if (score >= floorScore) {
                if (int(top.size()) < keep) {
                    top.push(Peak{score, x, y});
                } else if (score > top.top().score) {
                    top.pop();
                    top.push(Peak{score, x, y});
                }
            }
        }
    }
    if (getenv("YIUK_NCC_DEBUG")) { std::fprintf(stderr, "scan best=%.4f at (%d,%d) t=%dx%d x0..x1=%d..%d y0..y1=%d..%d\n", best, bestPosX, bestPosY, t.w, t.h, x0, x1, y0, y1); }
    while (!top.empty()) {
        peaks.push_back(top.top());
        top.pop();
    }
    return best;
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

Rect searchBounds(const GrayPlane& screen, const Rect& roi, bool hasRoi, int templateW, int templateH) {
    Rect bounds{0, 0, screen.w - templateW, screen.h - templateH};
    if (bounds.w < 0 || bounds.h < 0) return Rect{0, 0, -1, -1};
    if (hasRoi) {
        bounds.x = std::max(bounds.x, roi.x);
        bounds.y = std::max(bounds.y, roi.y);
        const int right = std::min(bounds.right(), roi.right() - templateW);
        const int bottom = std::min(bounds.bottom(), roi.bottom() - templateH);
        bounds.w = right - bounds.x;
        bounds.h = bottom - bounds.y;
    }
    return bounds;
}

}  // namespace

std::vector<double> scaleLadder(const SearchParams& params) {
    std::vector<double> scales;
    if (params.scaleStep <= 1.0) {
        scales.push_back(params.scaleMin);
        return scales;
    }
    for (double scale = params.scaleMin; scale <= params.scaleMax * 1.0001; scale *= params.scaleStep) {
        scales.push_back(scale);
    }
    return scales;
}

SearchResult nccFind(const GrayPlane& screen, const GrayPlane& tmpl, const SearchParams& params, SearchMode mode) {
    SearchResult result;
    const double start = nowMs();
    if (!screen.valid() || !tmpl.valid()) {
        result.elapsedMs = nowMs() - start;
        return result;
    }
    GrayIntegral integral;
    integral.build(screen);
    const std::vector<double> scales = scaleLadder(params);
    std::vector<Match> candidates;
    double bestScore = 0.0;

    if (mode == SearchMode::Exact) {
        for (double scale : scales) {
            const GrayPlane scaled = resample(tmpl, scale);
            if (scaled.w < 6 || scaled.h < 6 || scaled.w > screen.w || scaled.h > screen.h) continue;
            const Rect bounds = searchBounds(screen, params.roi, params.hasRoi, scaled.w, scaled.h);
            if (bounds.empty()) continue;
            const TemplatePrep prep = prepareTemplate(scaled);
            std::vector<Peak> peaks;
            const double best = scanArea(screen, integral, prep, bounds.x, bounds.y, bounds.right(), bounds.bottom(),
                                          params.threshold, 24, peaks, result.positionsEvaluated);
            bestScore = std::max(bestScore, best);
            for (const Peak& peak : peaks) {
                candidates.push_back(Match{Rect{peak.x, peak.y, scaled.w, scaled.h}, peak.score, scale});
            }
        }
    } else {
        // 粗搜：屏幕只缩放一次，各尺度只缩放模板。
        const int smallest = std::min(tmpl.w, tmpl.h);
        const int div = smallest >= 32 ? std::max(2, params.pyramidDiv) : 2;
        const double coarseFactor = 1.0 / double(div);
        const GrayPlane coarseScreen = resample(screen, coarseFactor);
        GrayIntegral coarseIntegral;
        coarseIntegral.build(coarseScreen);
        for (double scale : scales) {
            const GrayPlane scaled = resample(tmpl, scale);
            if (scaled.w < 8 || scaled.h < 8 || scaled.w > screen.w || scaled.h > screen.h) continue;
            const GrayPlane coarseTemplate = resample(scaled, coarseFactor);
            if (coarseTemplate.w < 4 || coarseTemplate.h < 4) continue;
            const Rect coarseBounds = searchBounds(coarseScreen, params.roi, params.hasRoi, coarseTemplate.w,
                                                  coarseTemplate.h);
            if (coarseBounds.empty()) continue;
            const TemplatePrep coarsePrep = prepareTemplate(coarseTemplate);
            std::vector<Peak> coarsePeaks;
            const double coarseBest = scanArea(coarseScreen, coarseIntegral, coarsePrep, coarseBounds.x,
                                               coarseBounds.y, coarseBounds.right(), coarseBounds.bottom(),
                                               params.coarseThreshold, params.coarsePeaks, coarsePeaks,
                                               result.positionsEvaluated);
            bestScore = std::max(bestScore, coarseBest);
            if (getenv("YIUK_NCC_DEBUG")) { std::fprintf(stderr, "[pyr] scale=%.4f scaled=%dx%d coarse=%dx%d coarseBest=%.4f peaks=%zu keep=%d floor=%.3f\n", scale, scaled.w, scaled.h, coarseTemplate.w, coarseTemplate.h, coarseBest, coarsePeaks.size(), params.coarsePeaks, params.coarseThreshold); }
            if (coarsePeaks.empty()) {
                bestScore = std::max(bestScore, coarseBest);
                continue;
            }
            const TemplatePrep prep = prepareTemplate(scaled);
            for (const Peak& coarse : coarsePeaks) {
                const int centerX = coarse.x * div;
                const int centerY = coarse.y * div;
                const int margin = 2 * div + 2;
                // window 描述的是"模板可能落在这里"的区域，必须包含整个模板尺寸，
                // 不能再被 searchBounds 当成模板左上角的可行范围。
                Rect window{centerX - margin, centerY - margin, scaled.w + 2 * margin, scaled.h + 2 * margin};
                const Rect bounds = searchBounds(screen, window, true, scaled.w, scaled.h);
                if (bounds.empty()) continue;
                std::vector<Peak> refined;
                const double best = scanArea(screen, integral, prep, bounds.x, bounds.y, bounds.right(),
                                             bounds.bottom(), -1.0, 1, refined, result.positionsEvaluated);
                bestScore = std::max(bestScore, best);
                for (const Peak& peak : refined) {
                    candidates.push_back(Match{Rect{peak.x, peak.y, scaled.w, scaled.h}, peak.score, scale});
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

}  // namespace vis
