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
    // 原生输入：坐标换算走 VisionService，投递走 yiuk::input（与真实客户端一致）。
    dispatcher.registerOperation(
        QStringLiteral("input.click"),
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
            QString inputError;
            if (!yiuk::input::click(screenX, screenY, arguments.value(QStringLiteral("button")).toString(
                                                       QStringLiteral("left")),
                                    arguments.value(QStringLiteral("double")).toBool() ? 2 : 1, cancelled,
                                    &inputError))
                return NativeOperationDispatcher::envelopeError(QStringLiteral("INPUT_FAILED"), inputError);
            return NativeOperationDispatcher::envelopeOk(
                QJsonObject{{QStringLiteral("x"), arguments.value(QStringLiteral("x")).toInt()},
                            {QStringLiteral("y"), arguments.value(QStringLiteral("y")).toInt()},
                            {QStringLiteral("screen_x"), screenX},
                            {QStringLiteral("screen_y"), screenY},
                            {QStringLiteral("foreground_title"), yiuk::input::foregroundTitle()}});
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
                                            QStringLiteral("input.move")};
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
    int pendingClickX = 0;
    int pendingClickY = 0;
    QString pendingScreenshotId;
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
            operatorClient->requestSessionClose(sessionId);
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
    return result == 0 && sessionClosed ? 0 : 1;
}