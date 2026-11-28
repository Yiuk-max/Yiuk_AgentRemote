#pragma once

#include "ncc_matcher.h"

namespace vis {

// OpenCV 路线：TM_CCOEFF_NORMED + 多尺度，与 nccFind 使用同一组 SearchParams。
// 仅在 YIUK_SPIKE_HAVE_OPENCV 下编译。
SearchResult cvFind(const GrayPlane& screen, const GrayPlane& tmpl, const SearchParams& params, SearchMode mode);

// 实测带 mask 的 TM_CCOEFF_NORMED：
// 返回 true 表示“全 255 的 mask 与不传 mask 结果一致”；false 时 reason 说明原因。
// partialAccepted 单独报告“部分 mask 是否被接受”（数值正确性未验证）。
bool cvMaskedCcoeffFullAndPartial(std::string* reason, bool* partialAccepted);

}  // namespace vis
