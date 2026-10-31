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
//
// 当另一个有效连接仍占用相同设备 ID 时，预期会收到 DEVICE_ALREADY_ONLINE
// 客户端会延迟后重试，因此测试程序会报告该错误，但不会将其视为失败
#include "platformclient.h"

#include <QCoreApplication>
#include <QCryptographicHash>
#include <QDebug>
#include <QJsonObject>
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

    auto *device = new PlatformClient(&app);
    PlatformClient::Config deviceConfig;
    deviceConfig.address = address;
    deviceConfig.playerPassword = playerPassword;
    deviceConfig.deviceId = deviceId;
    deviceConfig.deviceName = QStringLiteral("qt-device");
    deviceConfig.mode = QStringLiteral("approval");
    deviceConfig.temporaryPassword = devicePassword;
    deviceConfig.tls = useTls;
    deviceConfig.allowInsecure = insecure;
    deviceConfig.pinnedFingerprint = pin;
    deviceConfig.caFile = caFile;
    deviceConfig.enabled = true;
    device->setConfig(deviceConfig);

    auto *operation = new PlatformClient(&app);
    PlatformClient::Config operatorConfig;
    operatorConfig.address = address;
    operatorConfig.playerPassword = playerPassword;
    operatorConfig.deviceId = QStringLiteral("987654321");
    operatorConfig.deviceName = QStringLiteral("qt-operator");
    operatorConfig.mode = QStringLiteral("approval");
    operatorConfig.temporaryPassword = QString::fromLatin1(kOperatorPassword);
    operatorConfig.tls = useTls;
    operatorConfig.allowInsecure = insecure;
    operatorConfig.pinnedFingerprint = pin;
    operatorConfig.caFile = caFile;
    const bool tls12 = qEnvironmentVariable("YIUK_PLATFORM_TLS12") == QLatin1String("1");
    deviceConfig.forceTls12 = tls12;
    operatorConfig.forceTls12 = tls12;
    operatorConfig.enabled = true;
    operation->setConfig(operatorConfig);

    bool deviceReady = false;
    bool devicePasswordReady = false;
    bool operatorReady = false;
    bool requested = false;
    bool approved = false;
    bool commandSent = false;
    bool commandSeen = false;
    bool closed = false;
    int failures = 0;

    // 统计临时重连次数，以便观察重试较多的运行情况
    int alreadyOnlineRetries = 0;

    auto maybeRequest = [&] {
        // 设备首次注册时会轮换临时密码
        // 服务端会正确撤销轮换前创建的会话，因此先等待收到密码轮换确认
        if (!deviceReady || !devicePasswordReady || !operatorReady || requested) return;
        requested = true;
        qInfo() << "both clients registered; requesting session";
        operation->requestSession(deviceId, QStringLiteral("interop"),
                                  QStringList{QStringLiteral("execute_command")},
                                  device->config().temporaryPassword);
    };

    QObject::connect(device, &PlatformClient::registered, &app, [&](const QJsonObject &) {
        deviceReady = true;
        maybeRequest();
    });
    QObject::connect(operation, &PlatformClient::registered, &app, [&](const QJsonObject &) {
        operatorReady = true;
        maybeRequest();
    });
    QObject::connect(device, &PlatformClient::temporaryPasswordUpdated, &app,
                     [&](const QString &, int, qint64) {
        devicePasswordReady = true;
        maybeRequest();
    });
    // 清除过期在线记录后重新注册时，服务端可能返回已经轮换的凭据，而不是再次轮换
    // 无论哪种情况，设备现在都持有可用的临时密码
    QObject::connect(device, &PlatformClient::credentialsRenewed, &app,
                     [&](const QString &, int) {
        devicePasswordReady = true;
        maybeRequest();
    });
    QObject::connect(device, &PlatformClient::sessionRequested, &app, [&](const QJsonObject &session) {
        qInfo() << "device received session request" << session.value(QStringLiteral("status")).toString();
        device->approveSession(session.value(QStringLiteral("session_id")).toString());
    });
    QObject::connect(operation, &PlatformClient::sessionCreated, &app, [&](const QJsonObject &session) {
        qInfo() << "operator session created" << session.value(QStringLiteral("status")).toString();
    });
    QObject::connect(operation, &PlatformClient::sessionUpdated, &app, [&](const QJsonObject &session) {
        const QString status = session.value(QStringLiteral("status")).toString();
        if (status == QLatin1String("approved") && !approved) {
            approved = true;
            commandSent = true;
            qInfo() << "session approved; sending command";
            operation->executeCommand(session.value(QStringLiteral("session_id")).toString(),
                                      QStringLiteral("interop-1"), QStringLiteral("whoami"), QString(), 10);
        }
        if (status == QLatin1String("closed") && !closed) {
            closed = true;
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
    QObject::connect(operation, &PlatformClient::commandOutput, &app,
                     [&](const QString &, const QString &, const QString &, const QString &data, quint64, bool) {
        qInfo() << "operator saw output" << data.trimmed();
    });
    QObject::connect(operation, &PlatformClient::commandResult, &app,
                     [&](const QString &sessionId, const QString &, const QString &status, const QJsonValue &, bool, bool) {
        qInfo() << "operator saw result" << status;
        if (status != QLatin1String("succeeded")) {
            ++failures;
            return;
        }
        operation->closeSession(sessionId);
    });
    QObject::connect(operation, &PlatformClient::platformError, &app, [&](const QString &code, const QString &message) {
        qWarning() << "platform error" << code << message;
        ++failures;
    });
    QObject::connect(operation, &PlatformClient::connectionError, &app, [&](const QString &message) {
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
    QObject::connect(operation, &PlatformClient::stateChanged, &app, [&](const QString &state) {
        if (state == QLatin1String("disconnected") && closed) app.quit();
    });
    QObject::connect(device, &PlatformClient::sessionUpdated, &app, [&](const QJsonObject &session) {
        if (session.value(QStringLiteral("status")).toString() == QLatin1String("closed") && !closed) {
            closed = true;
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
    operation->connectToPlatform();
    const int result = app.exec();
    if (failures > 0) {
        qWarning() << "interop completed with" << failures << "errors";
        return 1;
    }
    if (!commandSent || !commandSeen) {
        qWarning() << "command round trip did not complete";
        return 1;
    }
    return result == 0 && closed ? 0 : 1;
}