#include "platformclient.h"

#include <QCryptographicHash>
#include <QDateTime>
#include <QJsonDocument>
#include <QJsonArray>
#include <QJsonValue>
#include <QNetworkProxy>
#include <QNetworkProxyFactory>
#include <QNetworkProxyQuery>
#include <QRandomGenerator>
#include <QSslCertificate>
#include <QSslConfiguration>
#include <QSslSocket>
#include <QSignalBlocker>
#include <QtEndian>
#include <limits>

namespace {
constexpr int kProtocolVersion = 1;
constexpr int kMaxFrameBytes = 256 * 1024;
constexpr int kHeartbeatIntervalMs = 15000;
constexpr int kMaxReconnectDelayMs = 30000;

QString makeTemporaryPassword() {
    static constexpr char alphabet[] = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789";
    constexpr int alphabetSize = sizeof(alphabet) - 1;
    QString password;
    password.reserve(32);
    for (int i = 0; i < 32; ++i)
        password.append(QLatin1Char(alphabet[QRandomGenerator::system()->bounded(alphabetSize)]));
    return password;
}
} // namespace

PlatformClient::PlatformClient(QObject *parent) : QObject(parent) {
    QNetworkProxyFactory::setUseSystemConfiguration(true);
    heartbeatTimer_.setInterval(kHeartbeatIntervalMs);
    connect(&heartbeatTimer_, &QTimer::timeout, this, &PlatformClient::onHeartbeat);
    reconnectTimer_.setSingleShot(true);
    connect(&reconnectTimer_, &QTimer::timeout, this, &PlatformClient::onReconnect);
}

void PlatformClient::setConfig(const Config &config) {
    config_ = config;
    proxyFallbackAttempted_ = false;
    forceDirectConnection_ = false;
}

void PlatformClient::connectToPlatform() {
    manualDisconnect_ = false;
    reconnectTimer_.stop();
    if (socket_ && socket_->state() != QAbstractSocket::UnconnectedState)
        return;
    if (socket_) {
        socket_->deleteLater();
        socket_ = nullptr;
    }
    buffer_.clear();
    connectionId_.clear();
    authenticated_ = false;
    registered_ = false;
    sslErrorsSeen_ = false;
    tlsErrorReported_ = false;
    usingProxy_ = false;

    if (config_.address.trimmed().isEmpty()) {
        emit connectionError(QStringLiteral("未配置平台服务器地址"));
        setState(QStringLiteral("disconnected"));
        return;
    }
    const QString address = config_.address.trimmed();
    const int colon = address.lastIndexOf(QLatin1Char(':'));
    if (colon <= 0) {
        emit connectionError(QStringLiteral("平台地址必须为 host:port"));
        setState(QStringLiteral("disconnected"));
        return;
    }
    const QString host = address.left(colon);
    bool portOk = false;
    const int port = address.mid(colon + 1).toInt(&portOk);
    if (!portOk || port < 1 || port > 65535) {
        emit connectionError(QStringLiteral("平台端口无效"));
        setState(QStringLiteral("disconnected"));
        return;
    }

    auto *socket = new QSslSocket(this);
    socket_ = socket;
    // Use a VPN-configured system proxy first, then retry direct through the
    // Windows route table if that proxy cannot complete TLS.
    const QList<QNetworkProxy> proxies = QNetworkProxyFactory::systemProxyForQuery(
        QNetworkProxyQuery(host, static_cast<quint16>(port), QString(), QNetworkProxyQuery::TcpSocket));
    usingProxy_ = !forceDirectConnection_ && !proxies.isEmpty() &&
                  proxies.first().type() != QNetworkProxy::NoProxy;
    socket->setProxy(forceDirectConnection_ ? QNetworkProxy(QNetworkProxy::NoProxy)
                                            : QNetworkProxy(QNetworkProxy::DefaultProxy));
    connect(socket, &QTcpSocket::connected, this, &PlatformClient::onConnected);
    connect(socket, &QSslSocket::encrypted, this, &PlatformClient::onEncrypted);
    connect(socket, &QTcpSocket::readyRead, this, &PlatformClient::onReadyRead);
    connect(socket, &QTcpSocket::disconnected, this, &PlatformClient::onDisconnected);
    connect(socket, &QAbstractSocket::errorOccurred, this, &PlatformClient::onSocketError);
    connect(socket, &QSslSocket::sslErrors, this, [this, socket](const QList<QSslError> &errors) {
        if (config_.allowInsecure) {
            socket->ignoreSslErrors();
            return;
        }
        const QSslCertificate certificate = socket->peerCertificate();
        const QString fingerprint = QString::fromLatin1(certificate.digest(QCryptographicHash::Sha256).toHex());
        if (!config_.pinnedFingerprint.isEmpty() && !fingerprint.isEmpty() &&
            fingerprint.compare(config_.pinnedFingerprint, Qt::CaseInsensitive) == 0) {
            // QueryPeer still requires the matching certificate errors to be
            // explicitly ignored; otherwise Qt aborts before encrypted().
            socket->ignoreSslErrors();
            return;
        }
        // Complete the transport handshake so onEncrypted() can make the
        // certificate decision consistently on Schannel and OpenSSL. No
        // application frame is sent until that decision has succeeded.
        sslErrorsSeen_ = true;
        socket->ignoreSslErrors(errors);
    });

    setState(QStringLiteral("connecting"));
    if (config_.tls) {
        // A pinned certificate is verified after the TLS handshake using its
        // exact DER fingerprint. Avoid backend-specific CA validation here:
        // Schannel can abort a self-signed handshake before encrypted() and
        // surface only the misleading "remote host closed" error.
        if (!config_.pinnedFingerprint.trimmed().isEmpty())
            socket->setPeerVerifyMode(QSslSocket::VerifyNone);
        else
            socket->setPeerVerifyMode(QSslSocket::QueryPeer);
        if (config_.forceTls12) {
            QSslConfiguration sslConfiguration = socket->sslConfiguration();
            sslConfiguration.setProtocol(QSsl::TlsV1_2);
            socket->setSslConfiguration(sslConfiguration);
        }
        if (!config_.caFile.trimmed().isEmpty()) {
            const QList<QSslCertificate> certificates = QSslCertificate::fromPath(config_.caFile.trimmed());
            if (certificates.isEmpty()) {
                emit connectionError(QStringLiteral("无法加载 CA 证书：%1").arg(config_.caFile));
            } else {
                QSslConfiguration sslConfiguration = socket->sslConfiguration();
                sslConfiguration.setCaCertificates(certificates);
                socket->setSslConfiguration(sslConfiguration);
            }
        }
        const auto networkProtocol = config_.forceIPv4 ? QAbstractSocket::IPv4Protocol
                                                       : QAbstractSocket::AnyIPProtocol;
        // Keep the original hostname for SNI and certificate hostname checks;
        // constrain only the address family used for DNS/socket routing.
        socket->connectToHostEncrypted(host, static_cast<quint16>(port), host,
                                       QIODevice::ReadWrite, networkProtocol);
    } else {
        const auto networkProtocol = config_.forceIPv4 ? QAbstractSocket::IPv4Protocol
                                                       : QAbstractSocket::AnyIPProtocol;
        socket->connectToHost(host, static_cast<quint16>(port), QIODevice::ReadWrite,
                              networkProtocol);
    }
}

void PlatformClient::disconnectFromPlatform() {
    manualDisconnect_ = true;
    reconnectTimer_.stop();
    heartbeatTimer_.stop();
    QTcpSocket *socket = socket_;
    socket_ = nullptr;
    if (socket) {
        socket->abort();
        socket->deleteLater();
    }
    authenticated_ = false;
    registered_ = false;
    setState(QStringLiteral("disconnected"));
}

bool PlatformClient::isConnected() const {
    return socket_ && socket_->state() == QAbstractSocket::ConnectedState && authenticated_;
}

void PlatformClient::onConnected() {
    auto *socket = qobject_cast<QTcpSocket *>(sender());
    if (!socket || socket != socket_) return;
    // TLS sessions start only after onEncrypted() has accepted the certificate.
    if (config_.tls) return;
    beginSession();
}

void PlatformClient::onEncrypted() {
    auto *socket = qobject_cast<QSslSocket *>(sender());
    if (!socket || socket != socket_) return;
    const QSslCertificate certificate = socket->peerCertificate();
    const QString fingerprint = QString::fromLatin1(certificate.digest(QCryptographicHash::Sha256).toHex());
    if (config_.allowInsecure) {
        beginSession();
        return;
    }
    if (!config_.pinnedFingerprint.isEmpty() && !fingerprint.isEmpty() &&
        fingerprint.compare(config_.pinnedFingerprint, Qt::CaseInsensitive) == 0) {
        beginSession();
        return;
    }
    if (!sslErrorsSeen_ && config_.pinnedFingerprint.isEmpty()) {
        // Certificate verified against the platform CA store.
        beginSession();
        return;
    }
    rejectCertificate(fingerprint, certificate.subjectDisplayName(),
                      QStringLiteral("系统证书库无法验证该服务器证书"),
                      !config_.pinnedFingerprint.isEmpty());
}

void PlatformClient::rejectCertificate(const QString &fingerprint, const QString &subject,
                                       const QString &details, bool changed) {
    manualDisconnect_ = true;
    tlsErrorReported_ = true;
    reconnectTimer_.stop();
    heartbeatTimer_.stop();
    if (socket_) socket_->abort();
    setState(QStringLiteral("disconnected"));
    emit connectionError(QStringLiteral("服务器证书未被信任：%1").arg(subject));
    if (!fingerprint.isEmpty())
        emit certificateUntrusted(fingerprint, subject, details, changed);
}

void PlatformClient::beginSession() {
    QJsonObject auth{
        {QStringLiteral("version"), kProtocolVersion},
        {QStringLiteral("business_type"), QStringLiteral("agent_assist")},
        {QStringLiteral("type"), QStringLiteral("authenticate_player")},
        {QStringLiteral("identity_type"), QStringLiteral("player")},
        {QStringLiteral("player_password"), config_.playerPassword},
    };
    send(auth);
}

void PlatformClient::onReadyRead() {
    auto *socket = qobject_cast<QTcpSocket *>(sender());
    if (!socket || socket != socket_) return;
    buffer_ += socket->readAll();
    while (buffer_.size() >= 4) {
        const quint32 length = qFromBigEndian<quint32>(reinterpret_cast<const uchar *>(buffer_.constData()));
        if (length == 0 || length > static_cast<quint32>(kMaxFrameBytes)) {
            emit connectionError(QStringLiteral("平台消息帧无效"));
            socket_->abort();
            return;
        }
        if (static_cast<quint32>(buffer_.size()) < 4 + length) return;
        const QByteArray payload = buffer_.mid(4, static_cast<int>(length));
        buffer_.remove(0, 4 + static_cast<int>(length));
        QJsonParseError parseError;
        const QJsonDocument document = QJsonDocument::fromJson(payload, &parseError);
        if (parseError.error != QJsonParseError::NoError || !document.isObject()) {
            emit connectionError(QStringLiteral("平台消息不是有效 JSON"));
            continue;
        }
        handleMessage(document.object());
    }
}

void PlatformClient::onDisconnected() {
    auto *socket = qobject_cast<QSslSocket *>(sender());
    if (!socket || socket != socket_) return;
    if (usingProxy_ && !proxyFallbackAttempted_ && config_.tls && !socket->isEncrypted()) {
        retryWithoutProxy();
        return;
    }
    socket_ = nullptr;
    socket->deleteLater();
    heartbeatTimer_.stop();
    authenticated_ = false;
    registered_ = false;
    setState(QStringLiteral("disconnected"));
    if (!manualDisconnect_) scheduleReconnect();
}

void PlatformClient::onSocketError(QAbstractSocket::SocketError error) {
    auto *socket = qobject_cast<QSslSocket *>(sender());
    if (!socket || socket != socket_) return;
    if (usingProxy_ && !proxyFallbackAttempted_ && config_.tls && !socket->isEncrypted() &&
        (error == QAbstractSocket::RemoteHostClosedError ||
         error == QAbstractSocket::SslHandshakeFailedError ||
         error == QAbstractSocket::ProxyConnectionClosedError ||
         error == QAbstractSocket::ProxyConnectionRefusedError ||
         error == QAbstractSocket::ProxyConnectionTimeoutError)) {
        retryWithoutProxy();
        return;
    }
    if (tlsErrorReported_) {
        // The TLS dialog and message already explain the failure; the aborted
        // handshake typically surfaces here as RemoteHostClosedError.
        return;
    }
    QString hint;
    const bool tlsHandshakeClosed = config_.tls && !socket->isEncrypted() &&
                                    (error == QAbstractSocket::RemoteHostClosedError ||
                                     error == QAbstractSocket::SslHandshakeFailedError);
    if (tlsHandshakeClosed) {
        hint = QStringLiteral("TLS 握手失败：服务器在加密连接建立前关闭了连接，请检查服务端 TLS 配置或中间防火墙");
    } else {
        switch (error) {
        case QAbstractSocket::ConnectionRefusedError:
            hint = QStringLiteral("服务器拒绝连接：端口未开放，或平台服务未启动");
            break;
        case QAbstractSocket::HostNotFoundError:
            hint = QStringLiteral("找不到服务器地址，请检查域名或 IP");
            break;
        case QAbstractSocket::SocketTimeoutError:
            hint = QStringLiteral("连接超时，请检查网络或防火墙");
            break;
        case QAbstractSocket::RemoteHostClosedError:
            hint = QStringLiteral("服务器断开了连接（证书或协议可能不匹配）");
            break;
        case QAbstractSocket::SslHandshakeFailedError:
            hint = QStringLiteral("TLS 握手失败，请检查端口是否为平台服务端口");
            break;
        default:
            hint = socket->errorString();
            break;
        }
    }
    if (!hint.isEmpty()) emit connectionError(hint);
}

void PlatformClient::onHeartbeat() {
    if (!registered_) return;
    send(QJsonObject{
        {QStringLiteral("type"), QStringLiteral("device_heartbeat")},
        {QStringLiteral("device_id"), config_.deviceId},
    });
}

void PlatformClient::onReconnect() {
    if (manualDisconnect_ || socket_) return;
    connectToPlatform();
}

void PlatformClient::retryWithoutProxy() {
    if (proxyFallbackAttempted_ || manualDisconnect_) return;
    proxyFallbackAttempted_ = true;
    forceDirectConnection_ = true;
    QSslSocket *socket = qobject_cast<QSslSocket *>(socket_);
    socket_ = nullptr;
    if (socket) {
        socket->abort();
        socket->deleteLater();
    }
    connectToPlatform();
}

void PlatformClient::scheduleReconnect() {
    if (manualDisconnect_) return;
    ++reconnectAttempts_;
    const int delay = qMin(kMaxReconnectDelayMs, 1000 * (1 << qMin(reconnectAttempts_ - 1, 5)));
    reconnectTimer_.start(delay);
}

void PlatformClient::setState(const QString &state) {
    if (state_ == state) return;
    state_ = state;
    emit stateChanged(state_);
}

void PlatformClient::send(const QJsonObject &message) {
    if (!socket_ || socket_->state() != QAbstractSocket::ConnectedState) return;
    QJsonObject payload = message;
    payload.insert(QStringLiteral("version"), kProtocolVersion);
    if (!payload.contains(QStringLiteral("business_type")))
        payload.insert(QStringLiteral("business_type"), QStringLiteral("agent_assist"));
    const QByteArray json = QJsonDocument(payload).toJson(QJsonDocument::Compact);
    if (json.size() > kMaxFrameBytes) {
        emit connectionError(QStringLiteral("平台消息超过帧上限"));
        return;
    }
    QByteArray frame;
    frame.resize(4);
    qToBigEndian<quint32>(static_cast<quint32>(json.size()), reinterpret_cast<uchar *>(frame.data()));
    frame += json;
    socket_->write(frame);
}

void PlatformClient::handleMessage(const QJsonObject &message) {
    const QString type = message.value(QStringLiteral("type")).toString();
    if (type == QLatin1String("authenticated")) {
        connectionId_ = message.value(QStringLiteral("connection_id")).toString();
        authenticated_ = true;
        setState(QStringLiteral("authenticating"));
        send(QJsonObject{
            {QStringLiteral("type"), QStringLiteral("register_device")},
            {QStringLiteral("device_id"), config_.deviceId},
            {QStringLiteral("device_name"), config_.deviceName},
            {QStringLiteral("platform"), QStringLiteral("windows")},
            {QStringLiteral("mode"), config_.mode},
            {QStringLiteral("temporary_password"), config_.temporaryPassword},
            {QStringLiteral("password_version"), config_.passwordVersion},
            {QStringLiteral("expires_at"), QDateTime::currentSecsSinceEpoch() + config_.ttlSeconds},
        });
        return;
    }
    if (type == QLatin1String("device_registered")) {
        registered_ = true;
        const QString serverPassword = message.value(QStringLiteral("temporary_password")).toString();
        const int previousPasswordVersion = config_.passwordVersion;
        if (!serverPassword.isEmpty())
            config_.temporaryPassword = serverPassword;
        if (message.value(QStringLiteral("mode")).isString())
            config_.mode = message.value(QStringLiteral("mode")).toString();
        if (message.value(QStringLiteral("password_version")).isDouble())
            config_.passwordVersion = message.value(QStringLiteral("password_version")).toInt();
        const bool serverRotatedPassword = !serverPassword.isEmpty() ||
                                           config_.passwordVersion != previousPasswordVersion;
        if (serverRotatedPassword) {
            rotatePasswordOnFirstRegistration_ = false;
            emit credentialsRenewed(config_.temporaryPassword, config_.passwordVersion);
        } else if (rotatePasswordOnFirstRegistration_) {
            rotatePasswordOnFirstRegistration_ = false;
            pendingTemporaryPassword_ = makeTemporaryPassword();
            updateTemporaryPassword(pendingTemporaryPassword_, config_.passwordVersion + 1,
                                    config_.mode, config_.ttlSeconds);
        }
        reconnectAttempts_ = 0;
        setState(QStringLiteral("connected"));
        heartbeatTimer_.start();
        const QJsonObject device = message.value(QStringLiteral("device")).toObject();
        emit registered(device);
        return;
    }
    if (type == QLatin1String("device_list")) {
        emit deviceListReceived(message.value(QStringLiteral("devices")).toArray(),
                                message.value(QStringLiteral("correlation_id")).toString());
        return;
    }
    if (type == QLatin1String("session_request")) {
        emit sessionRequested(message.value(QStringLiteral("session")).toObject());
        return;
    }
    if (type == QLatin1String("session_created")) {
        emit sessionCreated(message.value(QStringLiteral("session")).toObject());
        return;
    }
    if (type == QLatin1String("session_status")) {
        emit sessionUpdated(message.value(QStringLiteral("session")).toObject());
        return;
    }
    if (type == QLatin1String("execute_command")) {
        emit commandRequested(message.value(QStringLiteral("session_id")).toString(),
                              message.value(QStringLiteral("request_id")).toString(),
                              message.value(QStringLiteral("command")).toString(),
                              message.value(QStringLiteral("working_directory")).toString(),
                              message.value(QStringLiteral("timeout_seconds")).toInt(60));
        return;
    }
    if (type == QLatin1String("command_output")) {
        emit commandOutput(message.value(QStringLiteral("session_id")).toString(),
                           message.value(QStringLiteral("request_id")).toString(),
                           message.value(QStringLiteral("stream")).toString(),
                           message.value(QStringLiteral("data")).toString(),
                           static_cast<quint64>(message.value(QStringLiteral("sequence")).toDouble()),
                           message.value(QStringLiteral("truncated")).toBool());
        return;
    }
    if (type == QLatin1String("command_result")) {
        emit commandResult(message.value(QStringLiteral("session_id")).toString(),
                           message.value(QStringLiteral("request_id")).toString(),
                           message.value(QStringLiteral("status")).toString(),
                           message.value(QStringLiteral("exit_code")),
                           message.value(QStringLiteral("result_known")).toBool(),
                           message.value(QStringLiteral("truncated")).toBool());
        return;
    }
    if (type == QLatin1String("download_file")) {
        emit fileDownloadRequested(message.value(QStringLiteral("session_id")).toString(),
                                   message.value(QStringLiteral("request_id")).toString(),
                                   message.value(QStringLiteral("remote_path")).toString());
        return;
    }
    if (type == QLatin1String("upload_file")) {
        emit fileUploadRequested(message.value(QStringLiteral("session_id")).toString(),
                                 message.value(QStringLiteral("request_id")).toString(),
                                 message.value(QStringLiteral("remote_path")).toString(),
                                 message.value(QStringLiteral("file_name")).toString(),
                                 static_cast<qint64>(message.value(QStringLiteral("file_size")).toDouble()),
                                 message.value(QStringLiteral("file_sha256")).toString(),
                                 message.value(QStringLiteral("overwrite")).toBool());
        return;
    }
    if (type == QLatin1String("file_upload_chunk")) {
        emit fileUploadChunkReceived(message.value(QStringLiteral("session_id")).toString(),
                                     message.value(QStringLiteral("request_id")).toString(),
                                     static_cast<quint64>(message.value(QStringLiteral("sequence")).toDouble()),
                                     message.value(QStringLiteral("data")).toString(),
                                     message.value(QStringLiteral("eof")).toBool());
        return;
    }
    if (type == QLatin1String("cancel_file_upload")) {
        emit fileUploadCanceled(message.value(QStringLiteral("session_id")).toString(),
                                message.value(QStringLiteral("request_id")).toString());
        return;
    }
    if (type == QLatin1String("cancel_file_download")) {
        emit fileDownloadCanceled(message.value(QStringLiteral("session_id")).toString(),
                                 message.value(QStringLiteral("request_id")).toString());
        return;
    }
    if (type == QLatin1String("cancel_command")) {
        emit cancelRequested(message.value(QStringLiteral("session_id")).toString(),
                             message.value(QStringLiteral("request_id")).toString());
        return;
    }
    if (type == QLatin1String("close_session")) {
        emit closeRequested(message.value(QStringLiteral("session_id")).toString());
        return;
    }
    if (type == QLatin1String("temporary_password_updated")) {
        if (message.value(QStringLiteral("password_version")).isDouble())
            config_.passwordVersion = message.value(QStringLiteral("password_version")).toInt();
        const QString serverPassword = message.value(QStringLiteral("temporary_password")).toString();
        if (!serverPassword.isEmpty())
            config_.temporaryPassword = serverPassword;
        else if (!pendingTemporaryPassword_.isEmpty())
            config_.temporaryPassword = pendingTemporaryPassword_;
        pendingTemporaryPassword_.clear();
        const qint64 expiresAt = static_cast<qint64>(message.value(QStringLiteral("expires_at")).toDouble());
        emit temporaryPasswordUpdated(config_.temporaryPassword, config_.passwordVersion, expiresAt);
        return;
    }
    if (type == QLatin1String("error")) {
        const QString code = message.value(QStringLiteral("code")).toString();
        const QString text = message.value(QStringLiteral("message")).toString();
        const QString requestId = message.value(QStringLiteral("correlation_id")).toString();
        if (!requestId.isEmpty()) emit requestFailed(requestId, code, text);
        emit platformError(code, text);
        if (code == QLatin1String("DEVICE_ALREADY_ONLINE")) {
            // Not permanent: the platform still holds the previous connection
            // for this device id, typically a half-open TCP socket that has not
            // reached its heartbeat deadline yet. Back off and retry instead of
            // staying offline until the user reconnects by hand.
            heartbeatTimer_.stop();
            if (socket_)
                socket_->abort(); // onDisconnected() schedules the backoff retry
            else if (!manualDisconnect_)
                scheduleReconnect();
            return;
        }
        if (code == QLatin1String("BAD_PASSWORD") || code == QLatin1String("BAD_IDENTITY") ||
            code == QLatin1String("BUSINESS_TYPE_NOT_SUPPORTED") || code == QLatin1String("BAD_VERSION") ||
            code == QLatin1String("RATE_LIMITED") ||
            code == QLatin1String("BAD_TEMPORARY_PASSWORD") || code == QLatin1String("TEMPORARY_PASSWORD_EXPIRED") ||
            code == QLatin1String("TEMPORARY_PASSWORD_REVOKED") || code == QLatin1String("STALE_PASSWORD_VERSION") ||
            code == QLatin1String("DEVICE_NOT_FOUND") || code == QLatin1String("DEVICE_LIMIT")) {
            // These errors will not be fixed by reconnecting. Stop the retry
            // loop and wait for the user to correct the setting or reconnect.
            manualDisconnect_ = true;
            heartbeatTimer_.stop();
            if (socket_) socket_->abort();
        }
        return;
    }
    // ping/pong/heartbeat_ack and other informational messages are ignored.
}

void PlatformClient::approveSession(const QString &sessionId) {
    send(QJsonObject{{QStringLiteral("type"), QStringLiteral("approve_session")},
                     {QStringLiteral("session_id"), sessionId}});
}

void PlatformClient::rejectSession(const QString &sessionId) {
    send(QJsonObject{{QStringLiteral("type"), QStringLiteral("reject_session")},
                     {QStringLiteral("session_id"), sessionId},
                     {QStringLiteral("message"), QStringLiteral("rejected_by_owner")}});
}

void PlatformClient::sendCommandOutput(const QString &sessionId, const QString &requestId, const QString &stream,
                                       const QString &data, quint64 sequence) {
    send(QJsonObject{{QStringLiteral("type"), QStringLiteral("command_output")},
                     {QStringLiteral("session_id"), sessionId},
                     {QStringLiteral("request_id"), requestId},
                     {QStringLiteral("stream"), stream},
                     {QStringLiteral("data"), data},
                     {QStringLiteral("sequence"), static_cast<double>(sequence)}});
}

void PlatformClient::sendCommandResult(const QString &sessionId, const QString &requestId, const QString &status,
                                       const QJsonValue &exitCode, bool resultKnown, bool truncated) {
    QJsonObject message{{QStringLiteral("type"), QStringLiteral("command_result")},
                        {QStringLiteral("session_id"), sessionId},
                        {QStringLiteral("request_id"), requestId},
                        {QStringLiteral("status"), status},
                        {QStringLiteral("result_known"), resultKnown},
                        {QStringLiteral("truncated"), truncated}};
    if (!exitCode.isNull() && !exitCode.isUndefined()) message.insert(QStringLiteral("exit_code"), exitCode);
    send(message);
}

void PlatformClient::sendFileChunk(const QString &sessionId, const QString &requestId, quint64 sequence,
                                   const QByteArray &data, bool eof) {
    send(QJsonObject{{QStringLiteral("type"), QStringLiteral("file_chunk")},
                     {QStringLiteral("session_id"), sessionId},
                     {QStringLiteral("request_id"), requestId},
                     {QStringLiteral("sequence"), static_cast<double>(sequence)},
                     {QStringLiteral("data"), QString::fromLatin1(data.toBase64())},
                     {QStringLiteral("eof"), eof}});
}

void PlatformClient::sendFileDownloadResult(const QString &sessionId, const QString &requestId,
                                            const QString &status, const QString &fileName, qint64 fileSize,
                                            const QString &sha256, bool resultKnown) {
    send(QJsonObject{{QStringLiteral("type"), QStringLiteral("file_download_result")},
                     {QStringLiteral("session_id"), sessionId},
                     {QStringLiteral("request_id"), requestId},
                     {QStringLiteral("status"), status},
                     {QStringLiteral("file_name"), fileName},
                     {QStringLiteral("file_size"), static_cast<double>(fileSize)},
                     {QStringLiteral("file_sha256"), sha256},
                     {QStringLiteral("result_known"), resultKnown}});
}

void PlatformClient::sendFileUploadResult(const QString &sessionId, const QString &requestId,
                                          const QString &status, const QString &remotePath,
                                          const QString &fileName, qint64 fileSize,
                                          const QString &sha256, bool resultKnown) {
    send(QJsonObject{{QStringLiteral("type"), QStringLiteral("file_upload_result")},
                     {QStringLiteral("session_id"), sessionId},
                     {QStringLiteral("request_id"), requestId},
                     {QStringLiteral("status"), status},
                     {QStringLiteral("remote_path"), remotePath},
                     {QStringLiteral("file_name"), fileName},
                     {QStringLiteral("file_size"), static_cast<double>(fileSize)},
                     {QStringLiteral("file_sha256"), sha256},
                     {QStringLiteral("result_known"), resultKnown}});
}

void PlatformClient::updateTemporaryPassword(const QString &password, int version, const QString &mode, int ttlSeconds) {
    pendingTemporaryPassword_ = password;
    QJsonObject message{{QStringLiteral("type"), QStringLiteral("update_temporary_password")},
                        {QStringLiteral("temporary_password"), password},
                        {QStringLiteral("password_version"), version},
                        {QStringLiteral("mode"), mode}};
    if (ttlSeconds > 0) message.insert(QStringLiteral("expires_at"), QDateTime::currentSecsSinceEpoch() + ttlSeconds);
    send(message);
}

void PlatformClient::closeSession(const QString &sessionId) {
    send(QJsonObject{{QStringLiteral("type"), QStringLiteral("close_session")},
                     {QStringLiteral("session_id"), sessionId}});
}

void PlatformClient::cancelCommand(const QString &sessionId, const QString &requestId) {
    send(QJsonObject{{QStringLiteral("type"), QStringLiteral("cancel_command")},
                     {QStringLiteral("session_id"), sessionId},
                     {QStringLiteral("request_id"), requestId}});
}

void PlatformClient::listDevices(const QString &requestId) {
    send(QJsonObject{{QStringLiteral("type"), QStringLiteral("list_devices")},
                     {QStringLiteral("request_id"), requestId},
                     {QStringLiteral("correlation_id"), requestId}});
}


void PlatformClient::requestSession(const QString &deviceId, const QString &purpose,
                                    const QStringList &permissions, const QString &temporaryPassword) {
    QJsonArray permissionArray;
    for (const QString &permission : permissions) permissionArray.append(permission);
    send(QJsonObject{{QStringLiteral("type"), QStringLiteral("request_session")},
                     {QStringLiteral("device_id"), deviceId},
                     {QStringLiteral("purpose"), purpose},
                     {QStringLiteral("permissions"), permissionArray},
                     {QStringLiteral("temporary_password"), temporaryPassword}});
}

void PlatformClient::executeCommand(const QString &sessionId, const QString &requestId, const QString &command,
                                    const QString &workingDirectory, int timeoutSeconds) {
    QJsonObject message{{QStringLiteral("type"), QStringLiteral("execute_command")},
                        {QStringLiteral("session_id"), sessionId},
                        {QStringLiteral("request_id"), requestId},
                        {QStringLiteral("command"), command}};
    if (!workingDirectory.isEmpty()) message.insert(QStringLiteral("working_directory"), workingDirectory);
    if (timeoutSeconds > 0) message.insert(QStringLiteral("timeout_seconds"), timeoutSeconds);
    send(message);
}