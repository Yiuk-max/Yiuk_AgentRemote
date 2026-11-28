#pragma once

#include <cstdint>
#include <algorithm>
#include <string>
#include <vector>

namespace vis {

struct Rect {
    int x = 0, y = 0, w = 0, h = 0;
    int right() const { return x + w; }
    int bottom() const { return y + h; }
    int area() const { return w > 0 && h > 0 ? w * h : 0; }
    bool empty() const { return w <= 0 || h <= 0; }
    Rect intersect(const Rect& other) const {
        const int left = std::max(x, other.x);
        const int top = std::max(y, other.y);
        const int right = std::min(x + w, other.x + other.w);
        const int bottom = std::min(y + h, other.y + other.h);
        return Rect{left, top, std::max(0, right - left), std::max(0, bottom - top)};
    }
    bool contains(const Rect& other) const {
        return other.x >= x && other.y >= y && other.right() <= right() && other.bottom() <= bottom();
    }
};

// 交并比。两个分析框都按 [left, top, right, bottom) 理解。
double iou(const Rect& a, const Rect& b);

struct Match {
    Rect box;
    double score = 0.0;
    double scale = 1.0;
};

// 单调时钟毫秒。
double nowMs();

struct MemSample {
    double workingSetMB = 0.0;
    double privateMB = 0.0;
};

// Windows: GetProcessMemoryInfo。其它平台返回 0，便于脚本保持同一输出格式。
MemSample sampleMemory();

std::string jsonEscape(const std::string& value);

// 将毫秒序列压缩成一行摘要，供脚本汇总与文档引用。
std::string timingSummary(std::vector<double> samples);

}  // namespace vis
