// NCC 自检：先用参考实现（双精度、标量、逐像素）算出真值位置，
// 再和 nccFind / SSE2 点积的结果比对。任何一项不一致都说明基准不可信。

#include <QGuiApplication>
#include <QImage>

#include <cmath>
#include <cstdio>
#include <random>
#include <vector>

#include "gray_image.h"
#include "ncc_matcher.h"

namespace {

using vis::GrayPlane;

GrayPlane makeScreen(int width, int height, unsigned seed) {
    GrayPlane plane;
    plane.w = width;
    plane.h = height;
    plane.px.resize(size_t(width) * size_t(height));
    std::mt19937 rng(seed);
    std::uniform_int_distribution<int> noise(0, 40);
    for (int y = 0; y < height; ++y) {
        for (int x = 0; x < width; ++x) {
            int value = 128 + int(40.0 * std::sin(double(x) / 23.0)) + int(30.0 * std::cos(double(y) / 17.0));
            // 画一个“控件”：实心矩形 + 斜线，让模板有明确的高频结构
            if (x > 60 && x < 160 && y > 40 && y < 110) {
                value = 220;
                if (((x - 60) / 6 + (y - 40) / 5) % 3 == 0) value = 30;
            }
            plane.px[size_t(y) * size_t(width) + size_t(x)] = uint8_t(std::clamp(value + noise(rng), 0, 255));
        }
    }
    return plane;
}

GrayPlane crop(const GrayPlane& source, int x, int y, int w, int h) {
    GrayPlane plane;
    plane.w = w;
    plane.h = h;
    plane.px.resize(size_t(w) * size_t(h));
    for (int row = 0; row < h; ++row) {
        for (int col = 0; col < w; ++col) {
            plane.px[size_t(row) * size_t(w) + size_t(col)] =
                source.row(y + row)[x + col];
        }
    }
    return plane;
}

// 参考实现：双精度、无 SIMD、无积分图。
double referenceNcc(const GrayPlane& screen, int x, int y, const GrayPlane& tmpl) {
    const double count = double(tmpl.w) * double(tmpl.h);
    double sumS = 0, sumT = 0;
    for (int row = 0; row < tmpl.h; ++row) {
        for (int col = 0; col < tmpl.w; ++col) {
            sumS += screen.row(y + row)[x + col];
            sumT += tmpl.row(row)[col];
        }
    }
    const double meanS = sumS / count;
    const double meanT = sumT / count;
    double cov = 0, varS = 0, varT = 0;
    for (int row = 0; row < tmpl.h; ++row) {
        for (int col = 0; col < tmpl.w; ++col) {
            const double ds = double(screen.row(y + row)[x + col]) - meanS;
            const double dt = double(tmpl.row(row)[col]) - meanT;
            cov += ds * dt;
            varS += ds * ds;
            varT += dt * dt;
        }
    }
    if (varS < 1e-6 || varT < 1e-6) return 0.0;
    return cov / std::sqrt(varS * varT);
}

}  // namespace

int main(int argc, char** argv) {
    QGuiApplication app(argc, argv);
    int failures = 0;

    const GrayPlane screen = makeScreen(320, 240, 12345);
    const GrayPlane tmpl = crop(screen, 74, 52, 48, 36);  // 与控件部分重叠，位置已知

    // 1) 参考实现：真值位置应当是 (74,52)，得分接近 1。
    const double reference = referenceNcc(screen, 74, 52, tmpl);
    std::printf("reference_score_at_truth=%.6f\n", reference);
    if (reference < 0.999) {
        std::printf("FAIL: reference NCC at truth is not ~1\n");
        ++failures;
    }

    // 2) nccFind 精确模式（单尺度 = 1.0）必须命中同一位置。
    vis::SearchParams params;
    params.scaleMin = 1.0;
    params.scaleMax = 1.0;
    params.scaleStep = 1.0;
    params.threshold = 0.88;
    params.limit = 3;
    const vis::SearchResult exact = vis::nccFind(screen, tmpl, params, vis::SearchMode::Exact);
    if (exact.matches.empty()) {
        std::printf("FAIL: nccFind exact returned no match (best=%.6f)\n", exact.bestScore);
        ++failures;
    } else {
        const vis::Match& best = exact.matches.front();
        std::printf("exact_best score=%.6f box=(%d,%d,%d,%d) expected=(74,52,%d,%d)\n", best.score, best.box.x,
                    best.box.y, best.box.w, best.box.h, tmpl.w, tmpl.h);
        if (best.box.x != 74 || best.box.y != 52) {
            std::printf("FAIL: exact best position is wrong\n");
            ++failures;
        }
        if (std::fabs(best.score - reference) > 1e-3) {
            std::printf("FAIL: SSE2 dot product disagrees with the reference implementation\n");
            ++failures;
        }
    }

    // 3) 金字塔模式同样要命中。
    params.scaleMin = 0.75;
    params.scaleMax = 1.5;
    params.scaleStep = 1.1;
    const vis::SearchResult pyramid = vis::nccFind(screen, tmpl, params, vis::SearchMode::Pyramid);
    if (pyramid.matches.empty()) {
        std::printf("FAIL: pyramid returned no match (best=%.6f)\n", pyramid.bestScore);
        ++failures;
    } else {
        const vis::Match& best = pyramid.matches.front();
        std::printf("pyramid_best score=%.6f box=(%d,%d,%d,%d) scale=%.3f\n", best.score, best.box.x, best.box.y,
                    best.box.w, best.box.h, best.scale);
        if (std::abs(best.box.x - 74) > 2 || std::abs(best.box.y - 52) > 2) {
            std::printf("FAIL: pyramid best position drifts more than 2px\n");
            ++failures;
        }
    }

    // 3b) 粗搜层单独核对：对齐的 2x 盒式下采样对应该在 (37,26) 附近接近 1。
    const GrayPlane coarseScreen = vis::resample(screen, 0.5);
    const GrayPlane coarseTemplate = vis::resample(tmpl, 0.5);
    const double coarseReference = referenceNcc(coarseScreen, 37, 26, coarseTemplate);
    double coarseBest = 0.0;
    int coarseBestX = -1;
    int coarseBestY = -1;
    for (int y = 0; y + coarseTemplate.h <= coarseScreen.h; ++y) {
        for (int x = 0; x + coarseTemplate.w <= coarseScreen.w; ++x) {
            const double score = referenceNcc(coarseScreen, x, y, coarseTemplate);
            if (score > coarseBest) {
                coarseBest = score;
                coarseBestX = x;
                coarseBestY = y;
            }
        }
    }
    std::printf("coarse_reference_at_truth=%.6f coarse_best=%.6f at (%d,%d)\n", coarseReference, coarseBest,
                coarseBestX, coarseBestY);
    if (coarseReference < 0.9) {
        std::printf("FAIL: coarse reference correlation is low\n");
        ++failures;
    }
    if (coarseBestX != 37 || coarseBestY != 26) {
        std::printf("FAIL: coarse best position is not the truth\n");
        ++failures;
    }

    // 4) 纯色区域不得产生命中；最高分应保持有限值。
    GrayPlane flat;
    flat.w = 120;
    flat.h = 90;
    flat.px.assign(size_t(flat.w) * size_t(flat.h), 200);
    const vis::SearchResult flatResult = vis::nccFind(flat, tmpl, params, vis::SearchMode::Exact);
    std::printf("flat_screen_matches=%zu best=%.6f\n", flatResult.matches.size(), flatResult.bestScore);
    if (!flatResult.matches.empty() || !std::isfinite(flatResult.bestScore)) {
        std::printf("FAIL: flat screen produced a match or a non-finite score\n");
        ++failures;
    }

    std::printf(failures == 0 ? "SELFTEST OK\n" : "SELFTEST FAILED (%d)\n", failures);
    return failures == 0 ? 0 : 1;
}
