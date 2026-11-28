// 平台互操作测试程序：将两个 PlatformClient 实例（设备端和操作端）连接到运行中的 Go 平台服务
// 并执行完整的请求、批准和关闭会话流程
// 成功时退出码为 0，超时时退出码为 1
//
// 环境变量：
//   YIUK_PLATFORM_ADDRESS   默认值 127.0.0.1:25664
//   YIUK_PLATFORM_PASSWORD  平台玩家密码
//   YIUK_PLATFORM_TLS       设置为 "1" 使用 TLS（默认：明文）
//   YIUK_PLATFORM_PIN       固定的服务器证书 SHA-256 指纹（十六进制）
//   YIUK_PLATFORM_CA        TLS 的 CA 证书包路径
//   YIUK_PLATFORM_INSECURE  设置为 "1" 跳过证书验证
//   YIUK_DEVICE_ID          模拟设备使用的设备 ID（默认 123456789）
//   YIUK_DEVICE_PASSWORD    设备的初始临时密码
//   YIUK_INTEROP_TIMEOUT_MS 总超时时间（默认 15000 毫秒）
//   YIUK_INTEROP_NATIVE    设置为 "1" 时额外验证原生操作（execute_native）往返；
//                          设备会声明 screen.capture 能力，平台据此才允许下发
//
// 当另一个有效连接仍占用相同设备 ID 时，预期会收到 DEVICE_ALREADY_ONLINE
// 客户端会延迟后重试，因此测试程序会报告该错误，但不会将其视为失败
#define WIN32_LEAN_AND_MEAN
#define NOMINMAX
#include <windows.h>

#include "input/inputcontroller.h"
#include "platformclient.h"
#include "nativeops.h"
#include "vision/screengrab.h"
#include "vision/visionservice.h"

#include <QCoreApplication>
#include <QCryptographicHash>
#include <QDebug>
#include <QJsonDocument>
#include <QFileInfo>
#include <QJsonObject>
#include <QThread>

#include <functional>
#include <QTimer>

namespace {
const char kDevicePassword[] = "0123456789abcdef0123456789abcdef";
const char kOperatorPassword[] = "fedcba9876543210fedcba9876543210";
}

int main(int argc, char *argv[]) {
    QCoreApplication app(argc, argv);
    const QString address = qEnvironmentVariable("YIUK_PLATFORM_ADDRESS", "127.0.0.1:25664");
    const QString playerPassword = qEnvironmentVariable("YIUK_PLATFORM_PASSWORD", "interop-player-password-123456");
    const bool useTls = qEnvironmentVariable("YIUK_PLATFORM_TLS") == QLatin1String("1");
    const bool insecure = qEnvironmentVariable("YIUK_PLATFORM_INSECURE") == QLatin1String("1");
    const QString pin = qEnvironmentVariable("YIUK_PLATFORM_PIN");
    const QString caFile = qEnvironmentVariable("YIUK_PLATFORM_CA");
    const QString deviceId = qEnvironmentVariable("YIUK_DEVICE_ID", QStringLiteral("123456789"));
    const QString devicePassword =
        qEnvironmentVariable("YIUK_DEVICE_PASSWORD", QString::fromLatin1(kDevicePassword));
    const int timeoutMs = qEnvironmentVariableIntValue("YIUK_INTEROP_TIMEOUT_MS") > 0
                              ? qEnvironmentVariableIntValue("YIUK_INTEROP_TIMEOUT_MS")
                              : 15000;

    // 真机抓屏：设备端用真实分发器执行 screen.capture，验证"协议 → 客户端 → 抓屏"
    // 整条链路，而不是在测试里手写一个假信封。
    NativeOperationDispatcher dispatcher;
    // 与真实客户端一致：走 VisionService（设备门 + 会话级节流 + 快照），不绕过它直接调 GDI。
    auto *vision = new yiuk::vision::VisionService(&app);
    dispatcher.registerOperation(
        QStringLiteral("screen.capture"),
        [vision](const QString &sessionId, const QJsonObject &arguments, const std::atomic_bool *cancelled) {
            yiuk::vision::CaptureOptions options;
            const QJsonValue maxWidth = arguments.value(QStringLiteral("max_width"));
            if (maxWidth.isDouble()) options.maxWidth = maxWidth.toInt();
            const QString format = arguments.value(QStringLiteral("format")).toString();
            if (!format.isEmpty()) options.format = format;
            options.saveToDisk = arguments.value(QStringLiteral("save")).toBool(false);
            yiuk::vision::CaptureOutcome outcome;
            QString code;
            QString message;
            if (!vision->capture(sessionId, options, cancelled, &outcome, &code, &message))
                return NativeOperationDispatcher::envelopeError(code, message);
            QJsonObject data = yiuk::vision::geometryToJson(outcome.geometry);
            data.insert(QStringLiteral("captured"), true);
            data.insert(QStringLiteral("screenshot_id"), outcome.screenshotId);
            data.insert(QStringLiteral("layout_version"), outcome.layoutVersion);
            data.insert(QStringLiteral("size_bytes"), static_cast<double>(outcome.sizeBytes));
            if (!outcome.filePath.isEmpty()) data.insert(QStringLiteral("path"), outcome.filePath);
            return NativeOperationDispatcher::envelopeOk(data);
        });
    // 文本定位：与真实客户端共用 VisionService::searchText，连坐标换算都不另写一份。
    dispatcher.registerOperation(
        QStringLiteral("vision.find"),
        [vision](const QString &sessionId, const QJsonObject &arguments, const std::atomic_bool *cancelled) {
            yiuk::vision::TextQuery query;
            query.text = arguments.value(QStringLiteral("text")).toString();
            query.match = arguments.value(QStringLiteral("match")).toString(QStringLiteral("contains"));
            query.controlType = arguments.value(QStringLiteral("control_type")).toString();
            query.limit = qBound(1, arguments.value(QStringLiteral("limit")).toInt(5), 20);
            QJsonObject data;
            QString code;
            QString message;
            if (!vision->searchText(sessionId, query, cancelled, &data, &code, &message))
                return NativeOperationDispatcher::envelopeError(code, message);
            return NativeOperationDispatcher::envelopeOk(data);
        });
    // 原生输入：点击（含变化检测）委托给 VisionService::clickWithOptions，
    // 与真实客户端完全同一份实现，避免测试替身"漏掉采样"却看不出问题。
    dispatcher.registerOperation(
        QStringLiteral("input.click"),
        [vision](const QString &sessionId, const QJsonObject &arguments, const std::atomic_bool *cancelled) {
            QString button = arguments.value(QStringLiteral("button")).toString();
            if (button.isEmpty()) button = QStringLiteral("left");
            QJsonObject data;
            QString code;
            QString message;
            if (!vision->clickWithOptions(sessionId, arguments.value(QStringLiteral("screenshot_id")).toString(),
                                          arguments.value(QStringLiteral("x")).toInt(),
                                          arguments.value(QStringLiteral("y")).toInt(), button,
                                          arguments.value(QStringLiteral("double")).toBool(),
                                          arguments.value(QStringLiteral("detect_change")).toBool(),
                                          arguments.value(QStringLiteral("settle_ms")).toInt(), cancelled, &data,
                                          &code, &message))
                return NativeOperationDispatcher::envelopeError(code, message);
            return NativeOperationDispatcher::envelopeOk(data);
        });
    dispatcher.registerOperation(
        QStringLiteral("input.move"),
        [vision](const QString &sessionId, const QJsonObject &arguments, const std::atomic_bool *cancelled) {
            int screenX = 0;
            int screenY = 0;
            QString code;
            QString message;
            if (!vision->toScreenPoint(sessionId, arguments.value(QStringLiteral("screenshot_id")).toString(),
                                       arguments.value(QStringLiteral("x")).toInt(),
                                       arguments.value(QStringLiteral("y")).toInt(), &screenX, &screenY, &code,
                                       &message))
                return NativeOperationDispatcher::envelopeError(code, message);
            const int durationMs = arguments.value(QStringLiteral("duration_ms")).toInt();
            QString inputError;
            if (!yiuk::input::move(screenX, screenY, durationMs, cancelled, &inputError))
                return NativeOperationDispatcher::envelopeError(QStringLiteral("INPUT_FAILED"), inputError);
            return NativeOperationDispatcher::envelopeOk(
                QJsonObject{{QStringLiteral("x"), arguments.value(QStringLiteral("x")).toInt()},
                            {QStringLiteral("y"), arguments.value(QStringLiteral("y")).toInt()},
                            {QStringLiteral("screen_x"), screenX},
                            {QStringLiteral("screen_y"), screenY},
                            {QStringLiteral("duration_ms"), durationMs},
                            {QStringLiteral("foreground_title"), yiuk::input::foregroundTitle()}});
        });
    dispatcher.registerOperation(
        QStringLiteral("input.drag"),
        [vision](const QString &sessionId, const QJsonObject &arguments, const std::atomic_bool *cancelled) {
            const QJsonArray from = arguments.value(QStringLiteral("from")).toArray();
            const QJsonArray to = arguments.value(QStringLiteral("to")).toArray();
            if (from.size() != 2 || to.size() != 2)
                return NativeOperationDispatcher::envelopeError(QStringLiteral("INVALID_ARGUMENT"),
                                                               QStringLiteral("from/to must be [x,y]"));
            QString code;
            QString message;
            yiuk::vision::VisionService::DeviceLock deviceLock(*vision, cancelled, &code, &message);
            if (!deviceLock.held()) return NativeOperationDispatcher::envelopeError(code, message);
            int fromX = 0;
            int fromY = 0;
            int toX = 0;
            int toY = 0;
            const QString screenshotId = arguments.value(QStringLiteral("screenshot_id")).toString();
            if (!vision->toScreenPoint(sessionId, screenshotId, from.at(0).toInt(), from.at(1).toInt(), &fromX,
                                       &fromY, &code, &message))
                return NativeOperationDispatcher::envelopeError(code, message);
            if (!vision->toScreenPoint(sessionId, screenshotId, to.at(0).toInt(), to.at(1).toInt(), &toX, &toY,
                                       &code, &message))
                return NativeOperationDispatcher::envelopeError(code, message);
            const int durationMs = arguments.value(QStringLiteral("duration_ms")).toInt(400);
            const int holdMs = arguments.value(QStringLiteral("hold_ms")).toInt(50);
            QString inputError;
            if (!yiuk::input::drag(fromX, fromY, toX, toY, durationMs, holdMs, cancelled, &inputError))
                return NativeOperationDispatcher::envelopeError(QStringLiteral("INPUT_FAILED"), inputError);
            return NativeOperationDispatcher::envelopeOk(QJsonObject{
                {QStringLiteral("from"), QJsonArray{from.at(0).toInt(), from.at(1).toInt()}},
                {QStringLiteral("to"), QJsonArray{to.at(0).toInt(), to.at(1).toInt()}},
                {QStringLiteral("screen_from"), QJsonArray{fromX, fromY}},
                {QStringLiteral("screen_to"), QJsonArray{toX, toY}},
                {QStringLiteral("duration_ms"), durationMs},
                {QStringLiteral("hold_ms"), holdMs},
                {QStringLiteral("released"), true},
                {QStringLiteral("foreground_title"), yiuk::input::foregroundTitle()},
            });
        });
    // 临时截图回收：下载完由 MCP 调用，这里验证"只删截图目录内的文件"这条路径能跑通。
    dispatcher.registerOperation(
        QStringLiteral("screen.release"),
        [vision](const QString &sessionId, const QJsonObject &arguments, const std::atomic_bool *) {
            QString code;
            QString message;
            if (!vision->releaseScreenshotFile(sessionId, arguments.value(QStringLiteral("path")).toString(), &code,
                                               &message))
                return NativeOperationDispatcher::envelopeError(code, message);
            return NativeOperationDispatcher::envelopeOk(QJsonObject{{QStringLiteral("released"), true}});
        });

    auto *device = new PlatformClient(&app);
    PlatformClient::Config deviceConfig;
    deviceConfig.address = address;
    deviceConfig.playerPassword = playerPassword;
    deviceConfig.deviceId = deviceId;
    deviceConfig.deviceName = QStringLiteral("qt-device");
    deviceConfig.temporaryPassword = devicePassword;
    deviceConfig.tls = useTls;
    deviceConfig.allowInsecure = insecure;
    deviceConfig.pinnedFingerprint = pin;
    deviceConfig.caFile = caFile;
    deviceConfig.enabled = true;
    deviceConfig.clientVersion = QStringLiteral("interop");
    deviceConfig.capabilities = QStringList{QStringLiteral("screen.capture"), QStringLiteral("screen.release"),
                                            QStringLiteral("vision.find"), QStringLiteral("input.click"),
                                            QStringLiteral("input.move"), QStringLiteral("input.drag")};
    device->setConfig(deviceConfig);

    auto *operatorClient = new PlatformClient(&app);
    PlatformClient::Config operatorConfig;
    operatorConfig.address = address;
    operatorConfig.playerPassword = playerPassword;
    operatorConfig.deviceId = QStringLiteral("987654321");
    operatorConfig.deviceName = QStringLiteral("qt-operator");
    operatorConfig.temporaryPassword = QString::fromLatin1(kOperatorPassword);
    operatorConfig.tls = useTls;
    operatorConfig.allowInsecure = insecure;
    operatorConfig.pinnedFingerprint = pin;
    operatorConfig.caFile = caFile;
    const bool tls12 = qEnvironmentVariable("YIUK_PLATFORM_TLS12") == QLatin1String("1");
    deviceConfig.forceTls12 = tls12;
    operatorConfig.forceTls12 = tls12;
    operatorConfig.enabled = true;
    operatorClient->setConfig(operatorConfig);

    bool deviceReady = false;
    bool devicePasswordReady = false;
    bool operatorReady = false;
    bool sessionRequested = false;
    bool sessionApproved = false;
    const bool nativeEnabled = qEnvironmentVariable("YIUK_INTEROP_NATIVE") == QLatin1String("1");
    // 输入验收会真的动鼠标，默认关闭；需要配合 yiuk-input-fixture 显式开启。
    const bool inputEnabled = qEnvironmentVariable("YIUK_INTEROP_INPUT") == QLatin1String("1");
    // 变化检测验收：左半区点击会换色（changed=true），右半区点击不变（changed=false）。
    const bool detectChangeEnabled =
        qEnvironmentVariable("YIUK_INTEROP_DETECT_CHANGE") == QLatin1String("1");
    // 窗口拖动验收：值为目标窗口的类名（任务管理器是 TaskManagerWindow）。
    const QString windowDragClass = qEnvironmentVariable("YIUK_INTEROP_WINDOW_DRAG");
    bool commandSent = false;
    bool commandSeen = false;
    bool nativeSent = false;
    bool nativeSeen = false;
    bool nativeResultOk = false;
    bool savedResultOk = false;
    bool releasedResultOk = false;
    bool findResultOk = false;
    bool inputMoveOk = false;
    bool inputClickOk = false;
    bool changeDetectedOk = false;
    bool noChangeDetectedOk = false;
    int pendingClickX = 0;
    int pendingClickY = 0;
    int pendingHoverX = 0;
    int pendingHoverY = 0;
    QString pendingScreenshotId;
    // 窗口拖动状态：起点 + 每段的目标位置（物理像素，窗口左上角）。
    HWND dragWindow = nullptr;
    RECT dragInitialRect{};
    QVector<QPoint> dragTargets;
    int dragLeg = 0;
    QPoint dragExpected;
    // 标题栏上真正能拖动的位置（相对窗口左上角的偏移），用 WM_NCHITTEST 探测。
    QPoint dragCaptionOffset;
    bool dragCaptionOffsetReady = false;
    QString savedScreenshotPath;
    bool sessionClosed = false;
    int failures = 0;

    // 统计临时重连次数，以便观察重试较多的运行情况
    int alreadyOnlineRetries = 0;

    auto requestSessionWhenReady = [&] {
        // 设备首次注册时会轮换临时密码
        // 服务端会正确撤销轮换前创建的会话，因此先等待收到密码轮换确认
        if (!deviceReady || !devicePasswordReady || !operatorReady || sessionRequested) return;
        sessionRequested = true;
        qInfo() << "both clients registered; requesting session";
        operatorClient->requestSession(deviceId, QStringLiteral("interop"),
                                  QStringList{QStringLiteral("execute_command")},
                                  device->config().temporaryPassword);
    };

    QObject::connect(device, &PlatformClient::registered, &app, [&](const QJsonObject &) {
        deviceReady = true;
        requestSessionWhenReady();
    });
    QObject::connect(operatorClient, &PlatformClient::registered, &app, [&](const QJsonObject &) {
        operatorReady = true;
        requestSessionWhenReady();
    });
    QObject::connect(device, &PlatformClient::temporaryPasswordUpdated, &app,
                     [&](const QString &, int, qint64) {
        devicePasswordReady = true;
        requestSessionWhenReady();
    });
    // 清除过期在线记录后重新注册时，服务端可能返回已经轮换的凭据，而不是再次轮换
    // 无论哪种情况，设备现在都持有可用的临时密码
    QObject::connect(device, &PlatformClient::credentialsRenewed, &app,
                     [&](const QString &, int) {
        devicePasswordReady = true;
        requestSessionWhenReady();
    });
    QObject::connect(device, &PlatformClient::sessionRequested, &app, [&](const QJsonObject &session) {
        qInfo() << "device received session request" << session.value(QStringLiteral("status")).toString();
        device->approveSession(session.value(QStringLiteral("session_id")).toString());
    });
    QObject::connect(operatorClient, &PlatformClient::sessionCreated, &app, [&](const QJsonObject &session) {
        qInfo() << "operator session created" << session.value(QStringLiteral("status")).toString();
    });
    QObject::connect(operatorClient, &PlatformClient::sessionUpdated, &app, [&](const QJsonObject &session) {
        const QString status = session.value(QStringLiteral("status")).toString();
        if (status == QLatin1String("approved") && !sessionApproved) {
            sessionApproved = true;
            commandSent = true;
            qInfo() << "session approved; sending command";
            operatorClient->submitCommand(session.value(QStringLiteral("session_id")).toString(),
                                      QStringLiteral("interop-1"), QStringLiteral("whoami"), QString(), 10);
        }
        if (status == QLatin1String("closed") && !sessionClosed) {
            sessionClosed = true;
            qInfo() << "session closed; success";
            QTimer::singleShot(50, &app, [&] { app.quit(); });
        }
    });
    QObject::connect(device, &PlatformClient::commandRequested, &app,
                     [&](const QString &sessionId, const QString &requestId, const QString &command, const QString &, int) {
        commandSeen = true;
        qInfo() << "device received command" << command;
        device->sendCommandOutput(sessionId, requestId, QStringLiteral("stdout"), QStringLiteral("qt-harness\n"), 1);
        device->sendCommandResult(sessionId, requestId, QStringLiteral("succeeded"), QJsonValue(0), true, false);
    });
    QObject::connect(device, &PlatformClient::nativeRequested, &app,
                     [&](const QString &sessionId, const QString &requestId, const QString &operation,
                         const QJsonObject &arguments, int timeoutSeconds) {
        nativeSeen = true;
        qInfo() << "device received native operation" << operation;
        if (!dispatcher.start(sessionId, requestId, operation, arguments, timeoutSeconds)) {
            qWarning() << "dispatcher refused" << operation;
            ++failures;
        }
    });
    // 分发器在工作线程执行，结果回主线程后按协议回帧。
    QObject::connect(&dispatcher, &NativeOperationDispatcher::finished, &app,
                     [&](const QString &sessionId, const QString &requestId, const QString &status,
                         const QJsonObject &envelope) {
        const QByteArray payload = QJsonDocument(envelope).toJson(QJsonDocument::Compact);
        device->sendCommandOutput(sessionId, requestId, QStringLiteral("stdout"), QString::fromUtf8(payload), 1);
        device->sendCommandResult(sessionId, requestId, status, QJsonValue(), true, false);
    });
    // 原生结果走同一套 command_output/command_result 帧：这里只校验信封内容。
    QObject::connect(operatorClient, &PlatformClient::commandOutputReceived, &app,
                     [&](const QString &, const QString &requestId, const QString &, const QString &data,
                         quint64, bool) {
        // 只校验原生操作的输出：普通命令的 stdout 不是 JSON 信封，误判会直接让测试失败。
        if (!requestId.startsWith(QLatin1String("interop-native"))) return;
        const QJsonObject envelope = QJsonDocument::fromJson(data.toUtf8()).object();
        if (!envelope.value(QStringLiteral("ok")).toBool()) {
            qWarning() << "native envelope was not ok" << requestId << data;
            ++failures;
            return;
        }
        const QJsonObject payload = envelope.value(QStringLiteral("data")).toObject();
        if (requestId == QLatin1String("interop-native")) {
            if (!payload.value(QStringLiteral("captured")).toBool() ||
                payload.value(QStringLiteral("width")).toInt() <= 0 ||
                payload.value(QStringLiteral("height")).toInt() <= 0) {
                qWarning() << "native envelope payload is wrong" << payload;
                ++failures;
                return;
            }
            nativeResultOk = true;
            qInfo() << "operator saw native result" << payload;
            return;
        }
        if (requestId == QLatin1String("interop-native-save")) {
            savedScreenshotPath = payload.value(QStringLiteral("path")).toString();
            const QString screenshotId = payload.value(QStringLiteral("screenshot_id")).toString();
            const QFileInfo saved(savedScreenshotPath);
            if (savedScreenshotPath.isEmpty() || !saved.exists() || saved.size() <= 0 ||
                payload.value(QStringLiteral("size_bytes")).toDouble() <= 0 || screenshotId.size() != 12) {
                qWarning() << "saved screenshot is wrong" << payload;
                ++failures;
                return;
            }
            if (payload.value(QStringLiteral("width")).toInt() > 800) {
                qWarning() << "max_width was not applied" << payload;
                ++failures;
                return;
            }
            savedResultOk = true;
            qInfo() << "operator saw saved screenshot" << savedScreenshotPath << saved.size() << "bytes";
            return;
        }
        if (requestId == QLatin1String("interop-native-release")) {
            if (!payload.value(QStringLiteral("released")).toBool() || QFileInfo::exists(savedScreenshotPath)) {
                qWarning() << "screenshot was not released" << savedScreenshotPath;
                ++failures;
                return;
            }
            releasedResultOk = true;
            qInfo() << "operator saw screenshot release";
            return;
        }
        if (requestId == QLatin1String("interop-native-input-move")) {
            inputMoveOk = true;
            qInfo() << "operator saw native move" << payload;
            return;
        }
        if (requestId == QLatin1String("interop-native-input-click")) {
            inputClickOk = true;
            qInfo() << "operator saw native click" << payload;
            return;
        }
        if (requestId == QLatin1String("interop-native-change") ||
            requestId == QLatin1String("interop-native-static")) {
            // 判据与 Go 侧一致：强变化像素占比 >= 0.002 或平均差 >= 0.02。
            const double changedRatio = payload.value(QStringLiteral("changed_ratio")).toDouble();
            const double diffRatio = payload.value(QStringLiteral("diff_ratio")).toDouble();
            const bool changed = changedRatio >= 0.002 || diffRatio >= 0.02;
            const bool expectChange = requestId == QLatin1String("interop-native-change");
            qInfo() << requestId << "changed_ratio" << changedRatio << "diff_ratio" << diffRatio
                    << "changed" << changed << "expect" << expectChange;
            if (changed != expectChange) {
                qWarning() << "detect_change mismatch" << requestId << payload;
                ++failures;
                return;
            }
            if (expectChange) {
                changeDetectedOk = true;
            } else {
                noChangeDetectedOk = true;
            }
            return;
        }
        if (requestId == QLatin1String("interop-native-find")) {
            // 只要求链路通：前台窗口里本来就不该有这串测试文本，空结果也算成功。
            const QString screenshotId = payload.value(QStringLiteral("screenshot_id")).toString();
            if (!payload.value(QStringLiteral("captured")).toBool() || screenshotId.size() != 12 ||
                !payload.value(QStringLiteral("matches")).isArray() ||
                payload.value(QStringLiteral("width")).toInt() <= 0) {
                qWarning() << "vision.find payload is wrong" << payload;
                ++failures;
                return;
            }
            findResultOk = true;
            qInfo() << "operator saw vision.find result" << payload.value(QStringLiteral("visited_nodes")).toInt()
                    << "nodes" << payload.value(QStringLiteral("elapsed_ms")).toInt() << "ms";
        }
    });
    // 找出窗口里真正属于标题栏（HTCAPTION）的点：不同应用的自绘标题栏差别很大，
    // 猜 y=10 可能落在标签页上，点下去只会切换标签而不会移动窗口。
    auto findCaptionPoint = [](HWND hwnd, const RECT &rect) -> QPoint {
        RECT client{};
        GetClientRect(hwnd, &client);
        const int centerX = (rect.right - rect.left) / 2;
        for (int y = 6; y <= 48; y += 4) {
            for (int x = centerX; x > 32; x -= 24) {
                POINT probe{rect.left + x, rect.top + y};
                const LRESULT hit = SendMessageW(hwnd, WM_NCHITTEST, 0, MAKELPARAM(probe.x, probe.y));
                if (hit == HTCAPTION) return QPoint(probe.x, probe.y);
            }
        }
        return QPoint(rect.left + centerX, rect.top + 10);  // 探测失败时退回默认猜测
    };

    // 窗口拖动验收：按住标题栏，按"右 -> 右下 -> 左下 -> 左 -> 回起点"分 5 段绕屏一圈。
    // 每段都读 GetWindowRect 校验窗口真的跟着走了，而不是只看操作是否返回成功。
    std::function<void(const QString &)> submitNextDragLeg;
    submitNextDragLeg = [&](const QString &sessionId) {
        auto finishDrag = [&]() {
            if (dragWindow != nullptr) {
                SetWindowPos(dragWindow, HWND_NOTOPMOST, 0, 0, 0, 0, SWP_NOMOVE | SWP_NOSIZE | SWP_NOACTIVATE);
            }
            RECT finalRect{};
            if (dragWindow != nullptr) GetWindowRect(dragWindow, &finalRect);
            const bool backHome = dragWindow != nullptr && std::abs(finalRect.left - dragInitialRect.left) <= 8 &&
                                  std::abs(finalRect.top - dragInitialRect.top) <= 8;
            qInfo() << "window drag finished; legs:" << dragLeg << "back home:" << backHome << "final"
                    << finalRect.left << finalRect.top;
            if (!backHome) ++failures;
            dragWindow = nullptr;
            operatorClient->requestSessionClose(sessionId);
        };

        if (dragWindow == nullptr || dragLeg >= dragTargets.size()) {
            finishDrag();
            return;
        }
        RECT current{};
        GetWindowRect(dragWindow, &current);
        const QPoint target = dragTargets.at(dragLeg);
        const int dx = target.x() - current.left;
        const int dy = target.y() - current.top;
        qInfo() << "drag leg" << dragLeg << "window" << current.left << current.top << "-> target" << target
                << "delta" << dx << dy;
        if (std::abs(dx) <= 2 && std::abs(dy) <= 2) {
            // 已经到位（上一段被系统夹紧）：直接进入下一段。
            ++dragLeg;
            submitNextDragLeg(sessionId);
            return;
        }

        yiuk::vision::SnapshotRef ref;
        if (!vision->latestScreenshot(sessionId, &ref)) {
            qWarning() << "window drag has no coordinate base";
            ++failures;
            finishDrag();
            return;
        }
        const auto toShotX = [&](int value) {
            return qRound(static_cast<double>(value - ref.geometry.left) * ref.geometry.width /
                          ref.geometry.screenWidth);
        };
        const auto toShotY = [&](int value) {
            return qRound(static_cast<double>(value - ref.geometry.top) * ref.geometry.height /
                          ref.geometry.screenHeight);
        };
        // 抓标题栏：起点用探测到的偏移，终点平移同样的量。
        const int pressX = current.left + dragCaptionOffset.x();
        const int pressY = current.top + dragCaptionOffset.y();
        const int fromX = toShotX(pressX);
        const int fromY = toShotY(pressY);
        const int toX = toShotX(pressX + dx);
        const int toY = toShotY(pressY + dy);
        dragExpected = target;
        operatorClient->submitNative(
            sessionId, QStringLiteral("interop-drag-%1").arg(dragLeg), QStringLiteral("input.drag"),
            QJsonObject{{QStringLiteral("from"), QJsonArray{fromX, fromY}},
                        {QStringLiteral("to"), QJsonArray{toX, toY}},
                        {QStringLiteral("duration_ms"), 500},
                        {QStringLiteral("hold_ms"), 80},
                        {QStringLiteral("screenshot_id"), ref.id}},
            15);
    };

    auto startWindowDrag = [&](const QString &sessionId) {
        for (int attempt = 0; attempt < 30 && dragWindow == nullptr; ++attempt) {
            dragWindow = FindWindowW(reinterpret_cast<LPCWSTR>(windowDragClass.utf16()), nullptr);
            if (dragWindow == nullptr) QThread::msleep(100);
        }
        if (dragWindow == nullptr) {
            qWarning() << "window drag target not found:" << windowDragClass;
            ++failures;
            operatorClient->requestSessionClose(sessionId);
            return;
        }
        // 宿主是控制台程序：控制台窗口一出现就会盖住目标窗口，模拟点击会落到控制台上。
        // 由调用方用 -WindowStyle Hidden 启动（不要在这里最小化：那会误伤父进程的控制台）。
        // 目标窗口必须在前台且在最上面，否则按标题栏的那一下会落到别的窗口上。
        SetWindowPos(dragWindow, HWND_TOPMOST, 0, 0, 0, 0, SWP_NOMOVE | SWP_NOSIZE | SWP_SHOWWINDOW);
        SetForegroundWindow(dragWindow);
        BringWindowToTop(dragWindow);
        QThread::msleep(300);
        GetWindowRect(dragWindow, &dragInitialRect);

        // 有些应用（Win11 任务管理器的自绘标题栏）在窗口未激活时会忽略拖动：
        // 先用一次真实点击把窗口激活，再开始拖。只点一次，避免双击把窗口最大化。
        {
            const QPoint captionSpan = findCaptionPoint(dragWindow, dragInitialRect);
            QString clickError;
            if (yiuk::input::click(captionSpan.x(), captionSpan.y(), QStringLiteral("left"), 1, nullptr,
                                   &clickError)) {
                qInfo() << "caption activation click at" << captionSpan;
            } else {
                qWarning() << "caption activation click failed:" << clickError;
            }
            // 必须等过系统的双击时间（默认 500ms）：否则这次激活点击会和拖动时的按下
            // 被当成"双击标题栏"，窗口直接被最大化（位置变成 -9,-9），再也拖不动。
            QThread::msleep(700);
            if (IsZoomed(dragWindow)) {
                qInfo() << "target was maximized by the activation click; restoring";
                ShowWindow(dragWindow, SW_RESTORE);
                QThread::msleep(300);
            }
            GetWindowRect(dragWindow, &dragInitialRect);
        }
        MONITORINFO monitorInfo{};
        monitorInfo.cbSize = sizeof(monitorInfo);
        const HMONITOR monitor = MonitorFromWindow(dragWindow, MONITOR_DEFAULTTONEAREST);
        GetMonitorInfoW(monitor, &monitorInfo);
        const RECT work = monitorInfo.rcWork;
        const int width = dragInitialRect.right - dragInitialRect.left;
        const int height = dragInitialRect.bottom - dragInitialRect.top;
        qInfo() << "window drag start" << dragInitialRect.left << dragInitialRect.top << width << height << "work"
                << work.left << work.top << work.right << work.bottom;
        const QPoint captionSpan = findCaptionPoint(dragWindow, dragInitialRect);
        dragCaptionOffset = QPoint(captionSpan.x() - dragInitialRect.left, captionSpan.y() - dragInitialRect.top);
        dragCaptionOffsetReady = true;
        qInfo() << "window drag caption offset" << dragCaptionOffset;
        if (qEnvironmentVariableIsSet("YIUK_INTEROP_DRAG_SMOKE")) {
            // 冒烟模式：只在窗口附近走一个 300px 的闭环，不碰屏幕边缘。
            // 屏幕边缘可能有 Dock / 悬浮窗（例如 MyDockFinder）挡住点击。
            constexpr int step = 300;
            dragTargets = QVector<QPoint>{
                QPoint(dragInitialRect.left + step, dragInitialRect.top),
                QPoint(dragInitialRect.left + step, dragInitialRect.top + step),
                QPoint(dragInitialRect.left, dragInitialRect.top + step),
                QPoint(dragInitialRect.left, dragInitialRect.top)};
        } else {
            dragTargets = QVector<QPoint>{
                QPoint(work.right - width, dragInitialRect.top),   // 右边缘
                QPoint(work.right - width, work.bottom - height),  // 右下
                QPoint(work.left, work.bottom - height),           // 左下
                QPoint(work.left, dragInitialRect.top),            // 左边缘
                QPoint(dragInitialRect.left, dragInitialRect.top)  // 回到起点
            };
        }
        dragLeg = 0;
        submitNextDragLeg(sessionId);
    };

    // 输入真机验收：找到 fixture 窗口 -> 物理坐标换成截图坐标 -> 先移动（悬停）再点击。
    auto submitInputAcceptance = [&](const QString &sessionId) {
        HWND fixture = nullptr;
        for (int attempt = 0; attempt < 30 && fixture == nullptr; ++attempt) {
            fixture = FindWindowW(L"YiukInputFixtureWindow", nullptr);
            if (fixture == nullptr) QThread::msleep(100);
        }
        yiuk::vision::SnapshotRef ref;
        if (fixture == nullptr || !vision->latestScreenshot(sessionId, &ref)) {
            qWarning() << "input acceptance prerequisites missing (fixture =" << fixture << ")";
            ++failures;
            operatorClient->requestSessionClose(sessionId);
            return;
        }
        RECT rect{};
        GetWindowRect(fixture, &rect);
        const auto toShotX = [&](int value) {
            return qRound(static_cast<double>(value - ref.geometry.left) * ref.geometry.width /
                          ref.geometry.screenWidth);
        };
        const auto toShotY = [&](int value) {
            return qRound(static_cast<double>(value - ref.geometry.top) * ref.geometry.height /
                          ref.geometry.screenHeight);
        };
        const int width = rect.right - rect.left;
        const int height = rect.bottom - rect.top;
        const int hoverX = toShotX(rect.left + width * 3 / 4);
        const int hoverY = toShotY(rect.top + height / 2);
        pendingHoverX = hoverX;
        pendingHoverY = hoverY;
        pendingClickX = toShotX(rect.left + width / 4);
        pendingClickY = toShotY(rect.top + height / 2);
        pendingScreenshotId = ref.id;
        qInfo() << "input acceptance: fixture" << rect.left << rect.top << width << height << "hover" << hoverX
                << hoverY << "click" << pendingClickX << pendingClickY;
        operatorClient->submitNative(sessionId, QStringLiteral("interop-native-input-move"),
                                     QStringLiteral("input.move"),
                                     QJsonObject{{QStringLiteral("x"), hoverX},
                                                 {QStringLiteral("y"), hoverY},
                                                 {QStringLiteral("screenshot_id"), ref.id},
                                                 {QStringLiteral("duration_ms"), 300},
                                                 {QStringLiteral("settle_ms"), 150}},
                                     10);
    };

    QObject::connect(operatorClient, &PlatformClient::commandResultReceived, &app,
                     [&](const QString &sessionId, const QString &requestId, const QString &status,
                         const QJsonValue &, bool, bool) {
        qInfo() << "operator saw result" << status << requestId;
        if (status != QLatin1String("succeeded")) {
            ++failures;
            return;
        }
        if (requestId == QLatin1String("interop-native")) {
            if (!nativeResultOk) ++failures;
            // 第二段：带 max_width 落盘，验证"几何 + PNG + 临时文件"整条原生路径。
            qInfo() << "submitting native operation screen.capture (save)";
            operatorClient->submitNative(sessionId, QStringLiteral("interop-native-save"),
                                         QStringLiteral("screen.capture"),
                                         QJsonObject{{QStringLiteral("max_width"), 800},
                                                     {QStringLiteral("save"), true}},
                                         10);
            return;
        }
        if (requestId == QLatin1String("interop-native-save")) {
            if (!savedResultOk) ++failures;
            // 第三段：模拟 MCP 取走图片后的回收。
            qInfo() << "submitting native operation screen.release";
            operatorClient->submitNative(sessionId, QStringLiteral("interop-native-release"),
                                         QStringLiteral("screen.release"),
                                         QJsonObject{{QStringLiteral("path"), savedScreenshotPath}}, 10);
            return;
        }
        if (requestId == QLatin1String("interop-native-release")) {
            if (!releasedResultOk) ++failures;
            qInfo() << "submitting native operation vision.find";
            operatorClient->submitNative(sessionId, QStringLiteral("interop-native-find"),
                                         QStringLiteral("vision.find"),
                                         QJsonObject{{QStringLiteral("text"), QStringLiteral("yiuk-interop-absent-9f3a")},
                                                     {QStringLiteral("limit"), 3}},
                                         10);
            return;
        }
        if (requestId == QLatin1String("interop-native-find")) {
            if (!findResultOk) ++failures;
            if (inputEnabled) {
                submitInputAcceptance(sessionId);
            } else if (!windowDragClass.isEmpty()) {
                startWindowDrag(sessionId);
            } else {
                operatorClient->requestSessionClose(sessionId);
            }
            return;
        }
        if (requestId == QLatin1String("interop-native-input-move")) {
            if (!inputMoveOk) ++failures;
            // 移动阶段已经让 fixture 记录悬停；现在点左半区。
            operatorClient->submitNative(sessionId, QStringLiteral("interop-native-input-click"),
                                         QStringLiteral("input.click"),
                                         QJsonObject{{QStringLiteral("x"), pendingClickX},
                                                     {QStringLiteral("y"), pendingClickY},
                                                     {QStringLiteral("screenshot_id"), pendingScreenshotId},
                                                     {QStringLiteral("detect_change"), false}},
                                         10);
            return;
        }
        if (requestId == QLatin1String("interop-native-input-click")) {
            if (!inputClickOk) ++failures;
            if (!detectChangeEnabled) {
                operatorClient->requestSessionClose(sessionId);
                return;
            }
            // 左半区点击会换色：changed 必须为 true。
            qInfo() << "submitting native click with detect_change (left, expect changed)";
            operatorClient->submitNative(sessionId, QStringLiteral("interop-native-change"),
                                         QStringLiteral("input.click"),
                                         QJsonObject{{QStringLiteral("x"), pendingClickX},
                                                     {QStringLiteral("y"), pendingClickY},
                                                     {QStringLiteral("screenshot_id"), pendingScreenshotId},
                                                     {QStringLiteral("detect_change"), true},
                                                     {QStringLiteral("settle_ms"), 400}},
                                         10);
            return;
        }
        if (requestId == QLatin1String("interop-native-change")) {
            if (!changeDetectedOk) ++failures;
            // 右半区点击不改像素：changed 必须为 false，否则就是误报。
            qInfo() << "submitting native click with detect_change (right, expect unchanged)";
            operatorClient->submitNative(sessionId, QStringLiteral("interop-native-static"),
                                         QStringLiteral("input.click"),
                                         QJsonObject{{QStringLiteral("x"), pendingHoverX},
                                                     {QStringLiteral("y"), pendingHoverY},
                                                     {QStringLiteral("screenshot_id"), pendingScreenshotId},
                                                     {QStringLiteral("detect_change"), true},
                                                     {QStringLiteral("settle_ms"), 400}},
                                         10);
            return;
        }
        if (requestId == QLatin1String("interop-native-static")) {
            if (!noChangeDetectedOk) ++failures;
            // 最后补一次点击：fixture 在采样完成之后才允许退出，
            // 否则"点完就销毁窗口"会被当成一次巨大变化。
            operatorClient->submitNative(sessionId, QStringLiteral("interop-native-finish"),
                                         QStringLiteral("input.click"),
                                         QJsonObject{{QStringLiteral("x"), pendingHoverX},
                                                     {QStringLiteral("y"), pendingHoverY},
                                                     {QStringLiteral("screenshot_id"), pendingScreenshotId}},
                                         10);
            return;
        }
        if (requestId == QLatin1String("interop-native-finish")) {
            if (!windowDragClass.isEmpty()) {
                startWindowDrag(sessionId);
            } else {
                operatorClient->requestSessionClose(sessionId);
            }
            return;
        }
        if (requestId.startsWith(QLatin1String("interop-drag"))) {
            // 等窗口跟上（系统有动画/夹紧），再核对是否到达本段目标。
            RECT moved{};
            bool reached = false;
            for (int attempt = 0; attempt < 40; ++attempt) {
                GetWindowRect(dragWindow, &moved);
                if (std::abs(moved.left - dragExpected.x()) <= 6 && std::abs(moved.top - dragExpected.y()) <= 6) {
                    reached = true;
                    break;
                }
                QThread::msleep(50);
            }
            qInfo() << "drag leg" << dragLeg << "window now" << moved.left << moved.top << "expected"
                    << dragExpected << "reached" << reached;
            if (!reached) ++failures;
            ++dragLeg;
            submitNextDragLeg(sessionId);
            return;
        }
        if (nativeEnabled) {
            nativeSent = true;
            qInfo() << "submitting native operation screen.capture";
            operatorClient->submitNative(sessionId, QStringLiteral("interop-native"),
                                         QStringLiteral("screen.capture"), QJsonObject{}, 10);
            return;
        }
        operatorClient->requestSessionClose(sessionId);
    });
    QObject::connect(operatorClient, &PlatformClient::platformError, &app, [&](const QString &code, const QString &message) {
        qWarning() << "platform error" << code << message;
        ++failures;
    });
    QObject::connect(operatorClient, &PlatformClient::connectionError, &app, [&](const QString &message) {
        qWarning() << "operator connection error" << message;
    });
    QObject::connect(device, &PlatformClient::connectionError, &app, [&](const QString &message) {
        qWarning() << "device connection error" << message;
    });
    QObject::connect(device, &PlatformClient::certificateUntrusted, &app,
                     [&](const QString &fingerprint, const QString &subject, const QString &details, bool changed) {
        qInfo() << "device cert" << subject << "sha256" << fingerprint << "changed" << changed << details;
    });
    QObject::connect(device, &PlatformClient::platformError, &app, [&](const QString &code, const QString &message) {
        if (code == QLatin1String("DEVICE_ALREADY_ONLINE")) {
            // 平台仍保留此 ID 的旧连接；客户端会延迟后重试，这正是本测试验证的行为
            ++alreadyOnlineRetries;
            qInfo() << "device re-registration deferred" << code << "attempt" << alreadyOnlineRetries;
            return;
        }
        qWarning() << "device platform error" << code << message;
        ++failures;
    });
    QObject::connect(operatorClient, &PlatformClient::stateChanged, &app, [&](const QString &state) {
        if (state == QLatin1String("disconnected") && sessionClosed) app.quit();
    });
    QObject::connect(device, &PlatformClient::sessionUpdated, &app, [&](const QJsonObject &session) {
        if (session.value(QStringLiteral("status")).toString() == QLatin1String("closed") && !sessionClosed) {
            sessionClosed = true;
            qInfo() << "session closed on device; success";
            QTimer::singleShot(50, &app, [&] { app.quit(); });
        }
    });

    QTimer::singleShot(timeoutMs, &app, [&] {
        qWarning() << "interop timed out after" << timeoutMs << "ms; DEVICE_ALREADY_ONLINE retries:"
                   << alreadyOnlineRetries;
        app.exit(1);
    });

    device->connectToPlatform();
    operatorClient->connectToPlatform();
    const int result = app.exec();
    if (failures > 0) {
        qWarning() << "interop completed with" << failures << "errors";
        return 1;
    }
    if (!commandSent || !commandSeen) {
        qWarning() << "command round trip did not complete";
        return 1;
    }
    if (nativeEnabled && (!nativeSent || !nativeSeen || !nativeResultOk || !savedResultOk || !releasedResultOk ||
                          !findResultOk)) {
        qWarning() << "native round trip did not complete"
                   << "sent:" << nativeSent << "seen:" << nativeSeen << "ok:" << nativeResultOk
                   << "saved:" << savedResultOk << "released:" << releasedResultOk
                   << "find:" << findResultOk;
        return 1;
    }
    if (inputEnabled && (!inputMoveOk || !inputClickOk)) {
        qWarning() << "native input round trip did not complete"
                   << "move:" << inputMoveOk << "click:" << inputClickOk;
        return 1;
    }
    if (!windowDragClass.isEmpty() && dragWindow != nullptr) {
        qWarning() << "window drag did not finish";
        return 1;
    }
    if (detectChangeEnabled && (!changeDetectedOk || !noChangeDetectedOk)) {
        qWarning() << "detect_change round trip did not complete"
                   << "changed:" << changeDetectedOk << "unchanged:" << noChangeDetectedOk;
        return 1;
    }
    return result == 0 && sessionClosed ? 0 : 1;
}