#include "requestparser.h"

#include <QJsonArray>
#include <QJsonValue>
#include <cmath>

namespace yiuk::vision {

namespace {

// click_target 的等待预算上限：平台单次原生操作 25 秒，这里留 5 秒给
// “找到之后再点一下 + settle 采样”，不然等到目标的瞬间就会先被平台超时杀掉。
constexpr int kMaxClickTargetWaitSeconds = 20;

bool fail(QString *code, QString *message, const QString &failureCode, const QString &failureMessage) {
    if (code) *code = failureCode;
    if (message) *message = failureMessage;
    return false;
}

// region 的语义：截图坐标 [left, top, right, bottom]，右下开区间，必须正面积。
bool parseRegion(const QJsonValue &value, QRect *out, bool *given, QString *code, QString *message) {
    if (value.isUndefined() || value.isNull()) {
        *given = false;
        return true;
    }
    const QJsonArray rect = value.toArray();
    if (rect.size() != 4)
        return fail(code, message, QStringLiteral("INVALID_ARGUMENT"),
                    QStringLiteral("region 必须是 [left,top,right,bottom] 四个整数"));
    *out = QRect(rect.at(0).toInt(), rect.at(1).toInt(), rect.at(2).toInt() - rect.at(0).toInt(),
                 rect.at(3).toInt() - rect.at(1).toInt());
    if (out->width() <= 0 || out->height() <= 0)
        return fail(code, message, QStringLiteral("INVALID_ARGUMENT"), QStringLiteral("region 的空区域无效"));
    *given = true;
    return true;
}

}  // namespace

bool buildLocateRequest(const QJsonObject &arguments, LocateRequest *out, QString *code, QString *message) {
    if (out == nullptr) return fail(code, message, QStringLiteral("INVALID_ARGUMENT"), QStringLiteral("out 为空"));

    const QString imageB64 = arguments.value(QStringLiteral("image_b64")).toString();
    const QString imagePath = arguments.value(QStringLiteral("image_path")).toString();
    const QString templateId = arguments.value(QStringLiteral("template_id")).toString().trimmed();
    out->locate.text = arguments.value(QStringLiteral("text")).toString();
    out->text.text = out->locate.text;

    // 四选一：text / image_path / image_b64 / template_id。
    const int given = (out->locate.text.trimmed().isEmpty() ? 0 : 1) + (imageB64.isEmpty() ? 0 : 1) +
                      (imagePath.isEmpty() ? 0 : 1) + (templateId.isEmpty() ? 0 : 1);
    if (given > 1)
        return fail(code, message, QStringLiteral("INVALID_ARGUMENT"),
                    QStringLiteral("text / image_path / image_b64 / template_id 只能给一个"));

    if (!templateId.isEmpty()) {
        if (!templateMatchingAvailable())
            return fail(code, message, QStringLiteral("UNSUPPORTED_OPERATION"),
                        QStringLiteral("当前客户端未编译 OpenCV，图像模板匹配不可用"));
        // 格式与存在性由 VisionService::loadTemplate 判定（那边是路径穿越防线的唯一入口）。
        out->locate.templateId = templateId;
        out->locate.useImage = true;
        const QJsonValue thresholdValue = arguments.value(QStringLiteral("threshold"));
        if (thresholdValue.isDouble()) {
            out->locate.threshold = thresholdValue.toDouble();
            out->locate.thresholdGiven = true;
            out->image.threshold = qBound(0.10, out->locate.threshold, 0.999);
        }
    } else if (!imageB64.isEmpty() || !imagePath.isEmpty()) {
        if (!imageB64.isEmpty() && !imagePath.isEmpty())
            return fail(code, message, QStringLiteral("INVALID_ARGUMENT"),
                        QStringLiteral("image_b64 与 image_path 只能给一个"));
        if (!templateMatchingAvailable())
            return fail(code, message, QStringLiteral("UNSUPPORTED_OPERATION"),
                        QStringLiteral("当前客户端未编译 OpenCV，图像模板匹配不可用"));

        if (!imageB64.isEmpty()) {
            const QByteArray raw = QByteArray::fromBase64(imageB64.toLatin1());
            if (raw.isEmpty())
                return fail(code, message, QStringLiteral("INVALID_ARGUMENT"),
                            QStringLiteral("image_b64 不是有效的 Base64"));
            // 64 KiB 是解码后的上限：base64 体积不做限制，避免把“差不多”的图悄悄截断。
            if (raw.size() > 64 * 1024)
                return fail(code, message, QStringLiteral("INVALID_ARGUMENT"),
                            QStringLiteral("image_b64 解码后 %1 字节，超过 64 KiB 上限").arg(raw.size()));
            if (!out->locate.needle.loadFromData(raw))
                return fail(code, message, QStringLiteral("INVALID_ARGUMENT"),
                            QStringLiteral("image_b64 无法解码（需要 PNG/JPEG）"));
        } else if (!out->locate.needle.load(imagePath)) {
            return fail(code, message, QStringLiteral("INVALID_ARGUMENT"),
                        QStringLiteral("image_path 无法读取或解码：%1").arg(imagePath));
        }
        if (out->locate.needle.width() > 2048 || out->locate.needle.height() > 2048)
            return fail(code, message, QStringLiteral("INVALID_ARGUMENT"),
                        QStringLiteral("模板 %1x%2 超过 2048x2048 上限")
                            .arg(out->locate.needle.width())
                            .arg(out->locate.needle.height()));
        out->locate.useImage = true;

        const QJsonValue threshold = arguments.value(QStringLiteral("threshold"));
        if (threshold.isDouble()) {
            out->locate.threshold = threshold.toDouble();
            out->locate.thresholdGiven = true;
            out->image.threshold = qBound(0.10, out->locate.threshold, 0.999);
        }
    } else {
        if (out->locate.text.trimmed().isEmpty())
            return fail(code, message, QStringLiteral("INVALID_ARGUMENT"), QStringLiteral("text 不能为空"));
        out->locate.match = arguments.value(QStringLiteral("match")).toString();
        out->locate.controlType = arguments.value(QStringLiteral("control_type")).toString();
        if (!out->locate.match.isEmpty()) out->text.match = out->locate.match;
        out->text.controlType = out->locate.controlType;
    }

    // 定位来源：auto（默认）/ uia / ocr。未知值直接拒绝，别让拼错的 "uai" 静默变成 auto。
    const QString source = arguments.value(QStringLiteral("source")).toString().trimmed().toLower();
    if (!source.isEmpty() && source != QLatin1String("auto") && source != QLatin1String("uia") &&
        source != QLatin1String("ocr"))
        return fail(code, message, QStringLiteral("INVALID_ARGUMENT"),
                    QStringLiteral("source 只能是 auto / uia / ocr"));
    if (!source.isEmpty()) out->text.source = source;

    if (!parseRegion(arguments.value(QStringLiteral("region")), &out->locate.region, &out->locate.hasRegion,
                     code, message))
        return false;
    if (out->locate.hasRegion) {
        out->text.region = out->locate.region;
        out->image.region = out->locate.region;
    }

    const QJsonValue limitValue = arguments.value(QStringLiteral("limit"));
    if (limitValue.isDouble()) {
        out->locate.limit = limitValue.toInt();
        const int limit = qBound(1, out->locate.limit, 20);
        out->text.limit = limit;
        out->image.limit = limit;
    }
    return true;
}

bool buildClickTargetOptions(const QJsonObject &arguments, ClickTargetOptions *out, QString *code,
                             QString *message) {
    if (out == nullptr) return fail(code, message, QStringLiteral("INVALID_ARGUMENT"), QStringLiteral("out 为空"));

    LocateRequest request;
    if (!buildLocateRequest(arguments, &request, code, message)) return false;

    out->useImage = request.locate.useImage;
    out->needle = request.locate.needle;
    out->templateId = request.locate.templateId;
    out->image = request.image;
    out->text = request.text;

    const QString pick = arguments.value(QStringLiteral("pick")).toString();
    if (!pick.trimmed().isEmpty()) out->pick = pick;
    const QString button = arguments.value(QStringLiteral("button")).toString();
    if (!button.isEmpty()) out->button = button;
    out->doubleClick = arguments.value(QStringLiteral("double")).toBool();

    // detect_change 默认开（规格要求）；settle_ms 只认“给了就按给的来”，显式 0 也算给了。
    out->detectChange = arguments.value(QStringLiteral("detect_change")).toBool(true);
    if (arguments.contains(QStringLiteral("settle_ms")))
        out->settleMs = qBound(0, arguments.value(QStringLiteral("settle_ms")).toInt(), 2000);

    out->waitUntilFound = arguments.value(QStringLiteral("wait_until_found")).toBool();
    if (out->waitUntilFound) {
        const int seconds = qBound(1, arguments.value(QStringLiteral("timeout_seconds")).toInt(10),
                                   kMaxClickTargetWaitSeconds);
        out->waitTimeoutMs = seconds * 1000;
    }
    return true;
}

QString failureHint(const QString &code, const QJsonObject &data) {
    const QString thresholdText = data.contains(QStringLiteral("threshold"))
                                      ? QString::number(data.value(QStringLiteral("threshold")).toDouble(), 'g', 2)
                                      : QString();
    if (code == QLatin1String("NOT_FOUND")) {
        if (data.value(QStringLiteral("uniform_area")).toBool())
            return QStringLiteral("搜索区域几乎是纯色（灰度标准差 %1），模板匹配没有可用纹理："
                                  "换一个 region，或改用 text 定位")
                .arg(QString::number(data.value(QStringLiteral("area_stddev")).toDouble(), 'g', 2));
        if (data.contains(QStringLiteral("max_score"))) {
            const double best = data.value(QStringLiteral("max_score")).toDouble();
            if (!thresholdText.isEmpty() && best > 0.0)
                return QStringLiteral("最高得分 %1 低于阈值 %2：先 take_screenshot 确认目标还在、"
                                      "并且模板与目标同缩放/同主题，再决定是否调整 threshold")
                    .arg(QString::number(best, 'g', 2), thresholdText);
            return QStringLiteral("目标可能不在画面上或被遮挡：先 take_screenshot 看一下，"
                                  "必要时缩小 region 或换 text 定位");
        }
        if (!data.value(QStringLiteral("ocr_note")).toString().isEmpty())
            return QStringLiteral("UIA 与 OCR 都没有命中：确认目标窗口在前台、没有被弹窗遮挡；"
                                  "OCR：%1")
                .arg(data.value(QStringLiteral("ocr_note")).toString());
        return QStringLiteral("没有命中：先 take_screenshot 看现场，或缩小 region、换 control_type");
    }
    if (code == QLatin1String("AMBIGUOUS_TARGET")) {
        const int count = data.value(QStringLiteral("candidate_count")).toInt();
        return QStringLiteral("命中 %1 个候选且得分接近，不点击：用 pick=best / pick=index:N 指定，"
                              "或用 region / control_type 缩小范围")
            .arg(count > 0 ? QString::number(count) : QStringLiteral("多"));
    }
    if (code == QLatin1String("OCR_UNAVAILABLE"))
        return QStringLiteral("设备上没有可用的 OCR 语言包：设置 → 时间和语言 → 语言里安装 OCR 组件，"
                              "或改用 image_path / template_id");
    if (code == QLatin1String("TEMPLATE_NOT_FOUND"))
        return QStringLiteral("模板不存在或已被清理：重新 save_template，或改用 image_path");
    if (code == QLatin1String("TEMPLATE_QUOTA_EXCEEDED"))
        return QStringLiteral("模板配额已满：删掉设备上 data 目录里的 templates 后重试");
    if (code == QLatin1String("STALE_SCREENSHOT"))
        return QStringLiteral("坐标基准已过期：重新 take_screenshot 后再操作");
    if (code == QLatin1String("SCREENSHOT_REQUIRED"))
        return QStringLiteral("先调用 take_screenshot 建立坐标基准");
    if (code == QLatin1String("INPUT_FAILED") || code == QLatin1String("SCREENSHOT_FAILED"))
        return QStringLiteral("确认设备未锁屏、没有停留在 UAC 安全桌面");
    if (code == QLatin1String("UIA_UNAVAILABLE"))
        return QStringLiteral("确认设备未锁屏、目标窗口在前台；必要时用 take_screenshot 人工判断");
    if (code == QLatin1String("UNSUPPORTED_OPERATION"))
        return QStringLiteral("改用 text 查找，或升级到带 OpenCV 的客户端");
    return QString();
}

}  // namespace yiuk::vision
