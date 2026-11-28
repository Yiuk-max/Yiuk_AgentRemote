#include "vision_common.h"

#include <algorithm>
#include <chrono>
#include <cmath>
#include <cstdio>

#ifdef _WIN32
#include <windows.h>
#include <psapi.h>
#endif

namespace vis {

double iou(const Rect& a, const Rect& b) {
    const int left = std::max(a.x, b.x);
    const int top = std::max(a.y, b.y);
    const int right = std::min(a.right(), b.right());
    const int bottom = std::min(a.bottom(), b.bottom());
    const int interW = right - left;
    const int interH = bottom - top;
    if (interW <= 0 || interH <= 0) return 0.0;
    const double inter = double(interW) * double(interH);
    const double uni = double(a.area()) + double(b.area()) - inter;
    return uni <= 0.0 ? 0.0 : inter / uni;
}

double nowMs() {
    using clock = std::chrono::steady_clock;
    static const clock::time_point start = clock::now();
    return std::chrono::duration<double, std::milli>(clock::now() - start).count();
}

MemSample sampleMemory() {
    MemSample sample;
#ifdef _WIN32
    PROCESS_MEMORY_COUNTERS_EX counters{};
    if (GetProcessMemoryInfo(GetCurrentProcess(), reinterpret_cast<PROCESS_MEMORY_COUNTERS*>(&counters),
                             sizeof(counters))) {
        sample.workingSetMB = double(counters.WorkingSetSize) / (1024.0 * 1024.0);
        sample.privateMB = double(counters.PrivateUsage) / (1024.0 * 1024.0);
    }
#endif
    return sample;
}

std::string jsonEscape(const std::string& value) {
    std::string out;
    out.reserve(value.size() + 8);
    for (char ch : value) {
        switch (ch) {
            case '"': out += "\\\""; break;
            case '\\': out += "\\\\"; break;
            case '\n': out += "\\n"; break;
            case '\r': out += "\\r"; break;
            case '\t': out += "\\t"; break;
            default:
                if (static_cast<unsigned char>(ch) < 0x20) {
                    char buffer[8];
                    std::snprintf(buffer, sizeof(buffer), "\\u%04x", static_cast<unsigned char>(ch));
                    out += buffer;
                } else {
                    out += ch;
                }
        }
    }
    return out;
}

namespace {
double percentile(std::vector<double> values, double fraction) {
    if (values.empty()) return 0.0;
    std::sort(values.begin(), values.end());
    const double index = fraction * double(values.size() - 1);
    const size_t low = size_t(std::floor(index));
    const size_t high = std::min(low + 1, values.size() - 1);
    const double weight = index - double(low);
    return values[low] * (1.0 - weight) + values[high] * weight;
}
}  // namespace

std::string timingSummary(std::vector<double> samples) {
    if (samples.empty()) return "{\"count\":0}";
    const double sum = [&] {
        double total = 0.0;
        for (double value : samples) total += value;
        return total;
    }();
    char buffer[256];
    std::snprintf(buffer, sizeof(buffer),
                  "{\"count\":%zu,\"min_ms\":%.1f,\"median_ms\":%.1f,\"p95_ms\":%.1f,\"max_ms\":%.1f,\"mean_ms\":%.1f}",
                  samples.size(), percentile(samples, 0.0), percentile(samples, 0.5), percentile(samples, 0.95),
                  percentile(samples, 1.0), sum / double(samples.size()));
    return buffer;
}

}  // namespace vis
