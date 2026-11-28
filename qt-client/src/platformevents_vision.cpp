#include "mainwindow.h"

#include "nativeops.h"
#include "vision/visionservice.h"

#include <QJsonArray>
#include <QImage>

// 原生操作 vision.find：UIA 文本定位（P3 第一步）。
//
// 这里只做参数解析与信封包装；"确保坐标基准 -> UIA 查找 -> 换算成截图坐标"
// 全在 VisionService::searchText 里，互操作程序也用同一份实现，避免两套语义。
QJsonObject MainWindow::runVisionFind(const QString &sessionId, const QJsonObject &arguments,
                                      const std::atomic_bool *cancelled) {
    // 静态成员函数不能 using 到函数作用域：这里用两个薄 lambda 保持调用点简洁。
    const auto envelopeError = [](const QString &code, const QString &message, const QString &hint = QString()) {
        return NativeOperationDispatcher::envelopeError(code, message, hint);
    };
    const auto envelopeOk = [](const QJsonObject &data) {
        return NativeOperationDispatcher::envelopeOk(data);
    };

    // 图像模板匹配（OpenCV）：image_b64 与 image_path 二选一，且不能和 text 同时给。
    const QString imageB64 = arguments.value(QStringLiteral("image_b64")).toString();
    const QString imagePath = arguments.value(QStringLiteral("image_path")).toString();
    if (!imageB64.isEmpty() || !imagePath.isEmpty()) {
        if (!imageB64.isEmpty() && !imagePath.isEmpty())
            return envelopeError(QStringLiteral("INVALID_ARGUMENT"), QStringLiteral("image_b64 与 image_path 只能给一个"));
        if (!arguments.value(QStringLiteral("text")).toString().trimmed().isEmpty())
            return envelopeError(QStringLiteral("INVALID_ARGUMENT"), QStringLiteral("text 与图像只能给一个"));
        if (!yiuk::vision::templateMatchingAvailable())
            return envelopeError(QStringLiteral("UNSUPPORTED_OPERATION"),
                                 QStringLiteral("当前客户端未编译 OpenCV，图像模板匹配不可用"),
                                 QStringLiteral("改用 text 查找，或升级到带 OpenCV 的客户端"));

        QImage needle;
        if (!imageB64.isEmpty()) {
            const QByteArray raw = QByteArray::fromBase64(imageB64.toLatin1());
            if (raw.isEmpty()) return envelopeError(QStringLiteral("INVALID_ARGUMENT"), QStringLiteral("image_b64 不是有效的 Base64"));
            // 64 KiB 是解码后的上限：base64 体积不做限制，避免把"差不多"的图悄悄截断。
            if (raw.size() > 64 * 1024)
                return envelopeError(QStringLiteral("INVALID_ARGUMENT"),
                                     QStringLiteral("image_b64 解码后 %1 字节，超过 64 KiB 上限").arg(raw.size()));
            if (!needle.loadFromData(raw))
                return envelopeError(QStringLiteral("INVALID_ARGUMENT"), QStringLiteral("image_b64 无法解码（需要 PNG/JPEG）"));
        } else if (!needle.load(imagePath)) {
            return envelopeError(QStringLiteral("INVALID_ARGUMENT"),
                                 QStringLiteral("image_path 无法读取或解码：%1").arg(imagePath));
        }
        if (needle.width() > 2048 || needle.height() > 2048)
            return envelopeError(QStringLiteral("INVALID_ARGUMENT"),
                                 QStringLiteral("模板 %1x%2 超过 2048x2048 上限").arg(needle.width()).arg(needle.height()));

        yiuk::vision::TemplateOptions options;
        const QJsonValue threshold = arguments.value(QStringLiteral("threshold"));
        if (threshold.isDouble())
            options.threshold = qBound(0.10, threshold.toDouble(), 0.999);
        const QJsonValue limitValue = arguments.value(QStringLiteral("limit"));
        if (limitValue.isDouble()) options.limit = qBound(1, limitValue.toInt(), 20);
        const QJsonValue regionValue = arguments.value(QStringLiteral("region"));
        if (!regionValue.isUndefined()) {
            const QJsonArray rect = regionValue.toArray();
            if (rect.size() != 4)
                return envelopeError(QStringLiteral("INVALID_ARGUMENT"),
                                     QStringLiteral("region 必须是 [left,top,right,bottom] 四个整数"));
            options.region = QRect(rect.at(0).toInt(), rect.at(1).toInt(), rect.at(2).toInt() - rect.at(0).toInt(),
                                   rect.at(3).toInt() - rect.at(1).toInt());
            if (options.region.width() <= 0 || options.region.height() <= 0)
                return envelopeError(QStringLiteral("INVALID_ARGUMENT"), QStringLiteral("region 的空区域无效"));
        }

        QJsonObject data;
        QString code;
        QString message;
        if (!vision->matchImage(sessionId, needle, options, cancelled, &data, &code, &message)) {
            const QString hint = code == QLatin1String("UNSUPPORTED_OPERATION")
                                     ? QStringLiteral("改用 text 查找或升级客户端")
                                     : (code == QLatin1String("INVALID_ARGUMENT")
                                            ? QStringLiteral("减小模板尺寸，或把搜索范围限制在 region 内")
                                            : QString());
            return envelopeError(code, message, hint);
        }
        return envelopeOk(data);
    }

    yiuk::vision::TextQuery query;
    query.text = arguments.value(QStringLiteral("text")).toString();
    if (query.text.trimmed().isEmpty())
        return envelopeError(QStringLiteral("INVALID_ARGUMENT"), QStringLiteral("text 不能为空"));

    const QString match = arguments.value(QStringLiteral("match")).toString();
    if (!match.isEmpty()) query.match = match;
    query.controlType = arguments.value(QStringLiteral("control_type")).toString();
    const QJsonValue limit = arguments.value(QStringLiteral("limit"));
    if (limit.isDouble()) query.limit = qBound(1, limit.toInt(), 20);
    const QJsonValue region = arguments.value(QStringLiteral("region"));
    if (!region.isUndefined()) {
        const QJsonArray rect = region.toArray();
        if (rect.size() != 4)
            return envelopeError(QStringLiteral("INVALID_ARGUMENT"),
                                 QStringLiteral("region 必须是 [left,top,right,bottom] 四个整数"));
        query.region = QRect(rect.at(0).toInt(), rect.at(1).toInt(), rect.at(2).toInt() - rect.at(0).toInt(),
                             rect.at(3).toInt() - rect.at(1).toInt());
        if (query.region.width() <= 0 || query.region.height() <= 0)
            return envelopeError(QStringLiteral("INVALID_ARGUMENT"), QStringLiteral("region 的空区域无效"));
    }

    QJsonObject data;
    QString code;
    QString message;
    if (!vision->searchText(sessionId, query, cancelled, &data, &code, &message)) {
        const QString hint = code == QLatin1String("UIA_UNAVAILABLE")
                                 ? QStringLiteral("确认设备未锁屏、目标窗口在前台；必要时用 take_screenshot 人工判断")
                                 : QString();
        return envelopeError(code, message, hint);
    }
    return envelopeOk(data);
}
