#include "mainwindow.h"

#include "nativeops.h"
#include "vision/visionservice.h"

#include <QJsonArray>
#include <QImage>

namespace {

// find_on_screen 与 click_target 共用的"定位参数"：text / image_path / image_b64 三选一，
// 外加 match、control_type、region、limit、threshold。
//
// 抽出来只为一件事：两个工具的定位语义必须完全一样（同一份 UIA/OpenCV 调用、
// 同一份归一化与过滤），否则 Agent 先用 find 找到、再用 click_target 却点不中。
struct LocateArguments {
    bool useImage = false;
    QImage needle;
    QString text;
    QString match;
    QString controlType;
    QRect region;
    bool hasRegion = false;
    int limit = 0;          // 0 = 未给，由调用方决定默认值
    double threshold = 0;   // 0 = 未给
    bool thresholdGiven = false;
};

bool parseLocateArguments(const QJsonObject &arguments, LocateArguments *out, QString *code, QString *message) {
    const auto fail = [code, message](const QString &failureCode, const QString &failureMessage) {
        if (code) *code = failureCode;
        if (message) *message = failureMessage;
        return false;
    };

    const QString imageB64 = arguments.value(QStringLiteral("image_b64")).toString();
    const QString imagePath = arguments.value(QStringLiteral("image_path")).toString();
    out->text = arguments.value(QStringLiteral("text")).toString();

    if (!imageB64.isEmpty() || !imagePath.isEmpty()) {
        if (!imageB64.isEmpty() && !imagePath.isEmpty())
            return fail(QStringLiteral("INVALID_ARGUMENT"), QStringLiteral("image_b64 与 image_path 只能给一个"));
        if (!out->text.trimmed().isEmpty())
            return fail(QStringLiteral("INVALID_ARGUMENT"), QStringLiteral("text 与图像只能给一个"));
        if (!yiuk::vision::templateMatchingAvailable())
            return fail(QStringLiteral("UNSUPPORTED_OPERATION"),
                        QStringLiteral("当前客户端未编译 OpenCV，图像模板匹配不可用"));

        if (!imageB64.isEmpty()) {
            const QByteArray raw = QByteArray::fromBase64(imageB64.toLatin1());
            if (raw.isEmpty())
                return fail(QStringLiteral("INVALID_ARGUMENT"), QStringLiteral("image_b64 不是有效的 Base64"));
            // 64 KiB 是解码后的上限：base64 体积不做限制，避免把"差不多"的图悄悄截断。
            if (raw.size() > 64 * 1024)
                return fail(QStringLiteral("INVALID_ARGUMENT"),
                            QStringLiteral("image_b64 解码后 %1 字节，超过 64 KiB 上限").arg(raw.size()));
            if (!out->needle.loadFromData(raw))
                return fail(QStringLiteral("INVALID_ARGUMENT"),
                            QStringLiteral("image_b64 无法解码（需要 PNG/JPEG）"));
        } else if (!out->needle.load(imagePath)) {
            return fail(QStringLiteral("INVALID_ARGUMENT"),
                        QStringLiteral("image_path 无法读取或解码：%1").arg(imagePath));
        }
        if (out->needle.width() > 2048 || out->needle.height() > 2048)
            return fail(QStringLiteral("INVALID_ARGUMENT"), QStringLiteral("模板 %1x%2 超过 2048x2048 上限")
                                                                  .arg(out->needle.width())
                                                                  .arg(out->needle.height()));
        out->useImage = true;

        const QJsonValue threshold = arguments.value(QStringLiteral("threshold"));
        if (threshold.isDouble()) {
            out->threshold = threshold.toDouble();
            out->thresholdGiven = true;
        }
    } else {
        out->match = arguments.value(QStringLiteral("match")).toString();
        out->controlType = arguments.value(QStringLiteral("control_type")).toString();
    }

    const QJsonValue limitValue = arguments.value(QStringLiteral("limit"));
    if (limitValue.isDouble()) out->limit = limitValue.toInt();

    const QJsonValue regionValue = arguments.value(QStringLiteral("region"));
    if (!regionValue.isUndefined()) {
        const QJsonArray rect = regionValue.toArray();
        if (rect.size() != 4)
            return fail(QStringLiteral("INVALID_ARGUMENT"),
                        QStringLiteral("region 必须是 [left,top,right,bottom] 四个整数"));
        out->region = QRect(rect.at(0).toInt(), rect.at(1).toInt(), rect.at(2).toInt() - rect.at(0).toInt(),
                            rect.at(3).toInt() - rect.at(1).toInt());
        if (out->region.width() <= 0 || out->region.height() <= 0)
            return fail(QStringLiteral("INVALID_ARGUMENT"), QStringLiteral("region 的空区域无效"));
        out->hasRegion = true;
    }
    return true;
}

// 视觉类错误的下一步建议：不要只说"失败了"。
QString visionHintForCode(const QString &code) {
    if (code == QLatin1String("UNSUPPORTED_OPERATION"))
        return QStringLiteral("改用 text 查找，或升级到带 OpenCV 的客户端");
    if (code == QLatin1String("NOT_FOUND"))
        return QStringLiteral("用 find_on_screen 看现场；必要时降低 threshold、缩小 region，或等界面加载完再试");
    if (code == QLatin1String("AMBIGUOUS_TARGET"))
        return QStringLiteral("歧义时不点击：用 pick=best 或 pick=index:N 指定，或用 region/control_type 缩小范围");
    if (code == QLatin1String("STALE_SCREENSHOT"))
        return QStringLiteral("重新 take_screenshot，并用新截图的坐标重试");
    if (code == QLatin1String("UIA_UNAVAILABLE"))
        return QStringLiteral("确认设备未锁屏、目标窗口在前台；必要时用 take_screenshot 人工判断");
    if (code == QLatin1String("INPUT_FAILED") || code == QLatin1String("SCREENSHOT_FAILED"))
        return QStringLiteral("确认设备未锁屏、没有停留在 UAC 安全桌面");
    return QString();
}

}  // namespace

// 原生操作 vision.find：UIA 文本定位 / OpenCV 图像模板匹配（P3）。
//
// 这里只做参数解析与信封包装；"确保坐标基准 -> 查找 -> 换算成截图坐标"
// 全在 VisionService 里，互操作程序也调同一份实现，避免两套语义。
QJsonObject MainWindow::runVisionFind(const QString &sessionId, const QJsonObject &arguments,
                                      const std::atomic_bool *cancelled) {
    LocateArguments locate;
    QString code;
    QString message;
    if (!parseLocateArguments(arguments, &locate, &code, &message))
        return NativeOperationDispatcher::envelopeError(code, message, visionHintForCode(code));

    QJsonObject data;
    bool ok = false;
    if (locate.useImage) {
        yiuk::vision::TemplateOptions options;
        if (locate.thresholdGiven) options.threshold = qBound(0.10, locate.threshold, 0.999);
        if (locate.limit > 0) options.limit = qBound(1, locate.limit, 20);
        if (locate.hasRegion) options.region = locate.region;
        ok = vision->matchImage(sessionId, locate.needle, options, cancelled, &data, &code, &message);
    } else {
        if (locate.text.trimmed().isEmpty())
            return NativeOperationDispatcher::envelopeError(QStringLiteral("INVALID_ARGUMENT"),
                                                           QStringLiteral("text 不能为空"));
        yiuk::vision::TextQuery query;
        query.text = locate.text;
        if (!locate.match.isEmpty()) query.match = locate.match;
        query.controlType = locate.controlType;
        if (locate.limit > 0) query.limit = qBound(1, locate.limit, 20);
        if (locate.hasRegion) query.region = locate.region;
        ok = vision->searchText(sessionId, query, cancelled, &data, &code, &message);
    }
    if (!ok) {
        const QString hint = code == QLatin1String("INVALID_ARGUMENT")
                                 ? QStringLiteral("减小模板尺寸，或把搜索范围限制在 region 内")
                                 : visionHintForCode(code);
        return NativeOperationDispatcher::envelopeError(code, message, hint);
    }
    return NativeOperationDispatcher::envelopeOk(data);
}

// 原生操作 vision.click_target：客户端内 find -> pick -> click 一笔事务（P4）。
//
// 与"find 完再 click"相比，这里的关键差别是全程持设备门：定位用的那一帧就是点击的
// 坐标基准，中间不会插进别的会话抓屏或抢鼠标。找不到 / 歧义 / 取消时一个 down 都不发。
QJsonObject MainWindow::runVisionClickTarget(const QString &sessionId, const QJsonObject &arguments,
                                            const std::atomic_bool *cancelled) {
    LocateArguments locate;
    QString code;
    QString message;
    if (!parseLocateArguments(arguments, &locate, &code, &message))
        return NativeOperationDispatcher::envelopeError(code, message, visionHintForCode(code));

    yiuk::vision::ClickTargetOptions options;
    options.useImage = locate.useImage;
    if (locate.useImage) {
        options.needle = locate.needle;
        if (locate.thresholdGiven) options.image.threshold = qBound(0.10, locate.threshold, 0.999);
        if (locate.limit > 0) options.image.limit = qBound(1, locate.limit, 20);
        if (locate.hasRegion) options.image.region = locate.region;
    } else {
        if (locate.text.trimmed().isEmpty())
            return NativeOperationDispatcher::envelopeError(QStringLiteral("INVALID_ARGUMENT"),
                                                           QStringLiteral("text 不能为空"));
        options.text.text = locate.text;
        if (!locate.match.isEmpty()) options.text.match = locate.match;
        options.text.controlType = locate.controlType;
        if (locate.limit > 0) options.text.limit = qBound(1, locate.limit, 20);
        if (locate.hasRegion) options.text.region = locate.region;
    }

    const QString pick = arguments.value(QStringLiteral("pick")).toString();
    if (!pick.trimmed().isEmpty()) options.pick = pick;
    const QString button = arguments.value(QStringLiteral("button")).toString();
    if (!button.isEmpty()) options.button = button;
    options.doubleClick = arguments.value(QStringLiteral("double")).toBool();
    // detect_change 默认开（规格要求）；settle_ms 只认"给了就按给的来"，显式 0 也算给了。
    options.detectChange = arguments.value(QStringLiteral("detect_change")).toBool(true);
    if (arguments.contains(QStringLiteral("settle_ms")))
        options.settleMs = qBound(0, arguments.value(QStringLiteral("settle_ms")).toInt(), 2000);

    QJsonObject data;
    if (!vision->clickTarget(sessionId, options, cancelled, &data, &code, &message)) {
        // data 里带着现场（候选、得分、阈值）：失败也是信息，不是"什么都没发生"。
        return NativeOperationDispatcher::envelopeError(code, message, visionHintForCode(code), data);
    }
    return NativeOperationDispatcher::envelopeOk(data);
}
