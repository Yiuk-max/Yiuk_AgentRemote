#include "requestparser.h"

#include <QJsonArray>
#include <QJsonValue>

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
    out->locate.text = arguments.value(QStringLiteral("text")).toString();
    out->text.text = out->locate.text;

    if (!imageB64.isEmpty() || !imagePath.isEmpty()) {
        if (!imageB64.isEmpty() && !imagePath.isEmpty())
            return fail(code, message, QStringLiteral("INVALID_ARGUMENT"),
                        QStringLiteral("image_b64 与 image_path 只能给一个"));
        if (!out->locate.text.trimmed().isEmpty())
            return fail(code, message, QStringLiteral("INVALID_ARGUMENT"),
                        QStringLiteral("text 与图像只能给一个"));
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

}  // namespace yiuk::vision
