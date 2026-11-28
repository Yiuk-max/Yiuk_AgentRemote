#include "mainwindow.h"

#include "nativeops.h"
#include "vision/requestparser.h"
#include "vision/visionservice.h"

#include <QJsonArray>
#include <QImage>

namespace {

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

// template_id 在解析阶段只带 id；这里用 VisionService 把像素读出来。
// 格式校验、路径穿越、缺失与损坏的判定都在 VisionService::loadTemplate 里（唯一入口）。
bool resolveTemplate(yiuk::vision::VisionService *vision, yiuk::vision::LocateRequest *request, QString *code,
                     QString *message) {
    if (request->locate.templateId.isEmpty()) return true;
    QImage needle;
    if (!vision->loadTemplate(request->locate.templateId, &needle, code, message)) return false;
    request->locate.needle = needle;
    request->locate.useImage = true;
    return true;
}

}  // namespace

// 原生操作 vision.find：UIA 文本定位 / OpenCV 图像模板匹配（P3）。
//
// 参数解析全在 yiuk::vision::buildLocateRequest（与互操作程序共用一份），
// "确保坐标基准 -> 查找 -> 换算成截图坐标" 在 VisionService 里。
QJsonObject MainWindow::runVisionFind(const QString &sessionId, const QJsonObject &arguments,
                                      const std::atomic_bool *cancelled) {
    yiuk::vision::LocateRequest request;
    QString code;
    QString message;
    if (!yiuk::vision::buildLocateRequest(arguments, &request, &code, &message) ||
        !resolveTemplate(vision, &request, &code, &message))
        return NativeOperationDispatcher::envelopeError(code, message, visionHintForCode(code));

    QJsonObject data;
    const bool ok = request.locate.useImage
                        ? vision->matchImage(sessionId, request.locate.needle, request.image, cancelled, &data,
                                             &code, &message)
                        : vision->searchText(sessionId, request.text, cancelled, &data, &code, &message);
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
// 与"find 完再 click"相比，关键差别是每轮都持设备门：定位用的那一帧就是点击的
// 坐标基准，中间不会插进别的会话抓屏或抢鼠标。找不到 / 歧义 / 取消时一个 down 都不发。
QJsonObject MainWindow::runVisionClickTarget(const QString &sessionId, const QJsonObject &arguments,
                                            const std::atomic_bool *cancelled) {
    yiuk::vision::ClickTargetOptions options;
    QString code;
    QString message;
    if (!yiuk::vision::buildClickTargetOptions(arguments, &options, &code, &message))
        return NativeOperationDispatcher::envelopeError(code, message, visionHintForCode(code));

    QJsonObject data;
    if (!vision->clickTarget(sessionId, options, cancelled, &data, &code, &message)) {
        // data 里带着现场（候选、得分、阈值）：失败也是信息，不是"什么都没发生"。
        return NativeOperationDispatcher::envelopeError(code, message, visionHintForCode(code), data);
    }
    return NativeOperationDispatcher::envelopeOk(data);
}

// 原生操作 vision.save_template：从**最近一张快照保留的像素**裁剪模板并存盘。
//
// 与 find 的图像分支共享同一份 template_id 语义：这里的产物直接可以被
// find_on_screen / click_target 的 template_id 引用，省掉每次上传图片。
QJsonObject MainWindow::runVisionSaveTemplate(const QString &sessionId, const QJsonObject &arguments,
                                              const std::atomic_bool *) {
    const auto envelopeError = [](const QString &code, const QString &message, const QString &hint = QString()) {
        return NativeOperationDispatcher::envelopeError(code, message, hint);
    };
    const QJsonArray regionArray = arguments.value(QStringLiteral("region")).toArray();
    if (regionArray.size() != 4)
        return envelopeError(QStringLiteral("INVALID_ARGUMENT"),
                             QStringLiteral("region 必须是 [left,top,right,bottom] 四个整数"));
    const QRect region(regionArray.at(0).toInt(), regionArray.at(1).toInt(),
                       regionArray.at(2).toInt() - regionArray.at(0).toInt(),
                       regionArray.at(3).toInt() - regionArray.at(1).toInt());
    if (region.width() <= 0 || region.height() <= 0)
        return envelopeError(QStringLiteral("INVALID_ARGUMENT"), QStringLiteral("region 必须有正面积"));

    yiuk::vision::TemplateOutcome outcome;
    QString code;
    QString message;
    if (!vision->saveTemplate(sessionId, arguments.value(QStringLiteral("screenshot_id")).toString(), region,
                              &outcome, &code, &message)) {
        QString hint;
        if (code == QLatin1String("STALE_SCREENSHOT"))
            hint = QStringLiteral("重新 take_screenshot，模板只能从最近一帧裁剪");
        else if (code == QLatin1String("TEMPLATE_QUOTA_EXCEEDED"))
            hint = QStringLiteral("清理设备上的 data\templates 目录后重试");
        return NativeOperationDispatcher::envelopeError(code, message, hint);
    }

    QJsonObject data{
        {QStringLiteral("template_id"), outcome.templateId},
        {QStringLiteral("width"), outcome.width},
        {QStringLiteral("height"), outcome.height},
        {QStringLiteral("size_bytes"), static_cast<double>(outcome.sizeBytes)},
        {QStringLiteral("region"),
         QJsonArray{outcome.region.left(), outcome.region.top(), outcome.region.left() + outcome.region.width(),
                    outcome.region.top() + outcome.region.height()}},
    };
    return NativeOperationDispatcher::envelopeOk(data);
}
