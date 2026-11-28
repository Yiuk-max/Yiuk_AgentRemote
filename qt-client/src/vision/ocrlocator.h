#pragma once

#include "textlocator.h"

#include <QImage>
#include <QRect>
#include <QString>
#include <QVector>

#include <atomic>

namespace yiuk::vision {

// Windows.Media.Ocr 的兜底文本定位（UIA 找不到时才用）。
//
// 为什么手写 ABI 而不是用 winrt/*.h 或 C++/WinRT：
//   * Windows SDK 的 winrt 头在 MinGW 下编译不过（Windows.Foundation.h 与
//     windows.foundation.h 在大小写不敏感的文件系统上互相覆盖）；
//   * C++/WinRT 只支持 MSVC。
// 所以这里按 ABI 声明最小的接口集合，IID 与 vtable 顺序来自 SDK 10.0.26100，
// 已在 `spike/vision/src/probe_winrt_ocr.cpp`（P0）逐项验证过。
//
// 两个必须记住的约束：
//   1. MinGW GCC 13.1 在 -O2/-O3 下会把这些“纯虚接口”的虚调用反优化成
//      `__cxa_pure_virtual`（运行时 0xC0000005），本文件必须用 -fno-devirtualize 编译；
//   2. 中文 OCR 的文本字间带空格（`收 藏 首 页`），匹配前必须走 normalizeText 去掉空白。
struct OcrWord {
    QString text;
    QRect rect;  // 图像像素坐标，右下开区间
};

struct OcrLine {
    QString text;  // 原始行文本（可能带字间空格）
    QVector<OcrWord> words;
};

struct OcrOutcome {
    QVector<OcrLine> lines;
    QString language;  // 实际使用的识别语言标签（可能是 "user-profile"）
    int elapsedMs = 0;
};

// 识别一张图（BGRA 由内部转换）。preferredText 用来挑语言：含 CJK 时优先中文引擎。
// 失败写 code：OCR_UNAVAILABLE（没有语言包/引擎不可用）、INVALID_ARGUMENT（图太大/为空）、
// OCR_FAILED（激活失败、识别失败）、CANCELLED。
bool recognizeText(const QImage &image, const QString &preferredText, const std::atomic_bool *cancelled,
                   OcrOutcome *outcome, QString *code, QString *message);

// 在识别结果里按 TextQuery 找目标。返回的 TextMatch.rect 是**图像坐标**
// （src/source 固定为 "ocr"），由调用方换算成截图/物理坐标。
//
// 匹配方式：每行内做 1..8 个词的滑动窗口，在归一化文本上做 exact/contains，
// 命中框取窗口内所有词框的并集；得分与 UIA 一致（完全一致 1.0，包含 0.9）。
void matchOcrText(const OcrOutcome &outcome, const TextQuery &query, const QRect &frameRegion,
                  QVector<TextMatch> *matches);

}  // namespace yiuk::vision
