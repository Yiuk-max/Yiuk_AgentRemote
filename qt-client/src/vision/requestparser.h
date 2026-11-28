#pragma once

#include "visionservice.h"

#include <QJsonObject>
#include <QString>

namespace yiuk::vision {

// 原生操作参数 → VisionService 入参的唯一解析处。
//
// 以前这段解析在客户端 handler（platformevents_vision.cpp）和互操作程序里各写了一份，
// 结果是加新参数时两边必然漂移（click_target 的等待参数就漏过一次）。现在：
//   * 默认值只在这里冻结（threshold 0.88 / limit 5 / detect_change true / settle 250）；
//   * 边界检查只在这里做（三选一、region 形状、模板尺寸、timeout 上限）；
//   * 纯函数，不碰系统 API，可以在 vision_test 里直接测。
struct LocateArguments {
    bool useImage = false;
    QImage needle;
    // 引用设备上已保存的模板（vision.save_template 的产物）。
    // 解析阶段只带 id，像素由调用方用 VisionService::loadTemplate 读出来。
    QString templateId;
    QString text;
    QString match;
    QString controlType;
    QRect region;
    bool hasRegion = false;
    int limit = 0;          // 0 = 未给
    double threshold = 0;   // 0 = 未给
    bool thresholdGiven = false;
};

// 一次定位请求：定位参数 + 两份可直接交给 VisionService 的查询对象。
// TextQuery / TemplateOptions 的默认值由这里补齐，调用方不再各自 qBound。
struct LocateRequest {
    LocateArguments locate;
    TextQuery text;
    TemplateOptions image;
};

// 解析 find / click_target 共用的定位参数（text / image_path / image_b64 / template_id 四选一）。
// 失败写 code（INVALID_ARGUMENT / UNSUPPORTED_OPERATION）与 message。
bool buildLocateRequest(const QJsonObject &arguments, LocateRequest *out, QString *code, QString *message);

// 解析 click_target 的全部参数：定位 + pick + button + double + detect_change +
// settle_ms + 等待（wait_until_found / timeout_seconds）。
//
// 冻结的默认值：detect_change=true、settle_ms=250（省略时；显式 0 保留）、
// pick=unique、button=left、不等待；等待预算 1–20 秒（平台单次原生操作 25 秒上限留 5 秒余量）。
bool buildClickTargetOptions(const QJsonObject &arguments, ClickTargetOptions *out, QString *code,
                             QString *message);

}  // namespace yiuk::vision
