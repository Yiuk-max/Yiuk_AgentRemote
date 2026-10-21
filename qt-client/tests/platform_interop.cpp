// Platform interop harness: connects two PlatformClient instances (a device
// and an operator) to a running Go platform server and drives a full
// request/approve/close session flow. It exits 0 on success and 1 on timeout.
//
// Environment:
//   YIUK_PLATFORM_ADDRESS   default 127.0.0.1:25664
//   YIUK_PLATFORM_PASSWORD  platform player password
//   YIUK_PLATFORM_TLS       "1" to use TLS (default: plaintext)
//   YIUK_PLATFORM_PIN       pinned server certificate SHA-256 (hex)
//   YIUK_PLATFORM_CA        CA bundle path for TLS
//   YIUK_PLATFORM_INSECURE  "1" to skip certificate verification
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

    auto *device = new PlatformClient(&app);
    PlatformClient::Config deviceConfig;
    deviceConfig.address = address;
    deviceConfig.playerPassword = playerPassword;
    deviceConfig.deviceId = QStringLiteral("123456789");
    deviceConfig.deviceName = QStringLiteral("qt-device");
    deviceConfig.mode = QStringLiteral("approval");
    deviceConfig.temporaryPassword = QString::fromLatin1(kDevicePassword);
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
    bool operatorReady = false;
    bool requested = false;
    bool approved = false;
    bool commandSent = false;
    bool commandSeen = false;
    bool closed = false;
    int failures = 0;

    auto maybeRequest = [&] {
        if (!deviceReady || !operatorReady || requested) return;
        requested = true;
        qInfo() << "both clients registered; requesting session";
        operation->requestSession(QStringLiteral("123456789"), QStringLiteral("interop"),
                                  QStringList{QStringLiteral("execute_command")}, QString::fromLatin1(kDevicePassword));
    };

    QObject::connect(device, &PlatformClient::registered, &app, [&](const QJsonObject &) {
        deviceReady = true;
        maybeRequest();
    });
    QObject::connect(operation, &PlatformClient::registered, &app, [&](const QJsonObject &) {
        operatorReady = true;
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

    QTimer::singleShot(15000, &app, [&] {
        qWarning() << "interop timed out";
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