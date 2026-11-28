#include "mainwindow.h"

#include "nativeops.h"
#include "vision/visionservice.h"

#include <QJsonArray>

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

    // 图像模板匹配（OpenCV）还没进 Qt 构建：明确报"未实现"，不要让 Agent 以为找不到。
    if (arguments.contains(QStringLiteral("image_path")) || arguments.contains(QStringLiteral("image_b64")))
        return envelopeError(QStringLiteral("UNSUPPORTED_OPERATION"),
                             QStringLiteral("当前客户端只支持 text 查找，图像模板匹配尚未实现"),
                             QStringLiteral("先用 text 定位；图像匹配需要升级到包含 OpenCV 的客户端版本"));

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
