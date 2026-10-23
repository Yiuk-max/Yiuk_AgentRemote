#pragma once

#include <QAbstractSocket>
#include <QObject>
#include <QJsonArray>
#include <QJsonObject>
#include <QStringList>
#include <QTimer>

class QTcpSocket;

// PlatformClient speaks the Yiuk platform framed-JSON protocol.
//
// The same object can act as the local device (register_device, approve/reject
// sessions, execute commands) and as an operator (request_session). A single
// authenticated connection is shared because the platform binds a session to
// the connection that created it.
class PlatformClient final : public QObject {
    Q_OBJECT
public:
    struct Config {
        QString address;            // host:port
        QString playerPassword;     // platform player password
        QString deviceId;           // local device id (digits only)
        QString deviceName;         // display name
        QString temporaryPassword;  // 32-character temporary password
        int passwordVersion = 1;
        QString mode;               // "direct" or "approval"
        int ttlSeconds = 24 * 60 * 60;
        bool tls = true;
        bool allowInsecure = false; // dev only: skip certificate verification
        QString caFile;
        // SHA-256 fingerprint of the server certificate, hex encoded. When set,
        // the client accepts exactly this certificate instead of relying on a
        // public CA. This keeps self-signed deployments usable without
        // disabling certificate verification for arbitrary certificates.
        QString pinnedFingerprint;
        // Most TUN adapters provide IPv4 routes only. This also avoids an IPv6
        // route escaping the VPN when the server is configured with an address.
        bool forceIPv4 = true;
        // Some Windows Schannel builds fail the TLS 1.3 handshake against
        // certain Go server configurations while TLS 1.2 succeeds. Setting this
        // pins the connection to TLS 1.2.
        bool forceTls12 = false;
        bool enabled = true;
    };

    explicit PlatformClient(QObject *parent = nullptr);

    void setConfig(const Config &config);
    Config config() const { return config_; }

    void connectToPlatform();
    void disconnectFromPlatform();
    bool isConnected() const;
    bool isRegistered() const { return registered_; }
    QString connectionId() const { return connectionId_; }
    QString state() const { return state_; }

    // Device actions.
    void approveSession(const QString &sessionId);
    void rejectSession(const QString &sessionId);
    void sendCommandOutput(const QString &sessionId, const QString &requestId, const QString &stream,
                           const QString &data, quint64 sequence);
    void sendCommandResult(const QString &sessionId, const QString &requestId, const QString &status,
                           const QJsonValue &exitCode, bool resultKnown, bool truncated);
    void sendFileChunk(const QString &sessionId, const QString &requestId, quint64 sequence,
                       const QByteArray &data, bool eof);
    void sendFileDownloadResult(const QString &sessionId, const QString &requestId, const QString &status,
                                const QString &fileName, qint64 fileSize, const QString &sha256,
                                bool resultKnown);
    void sendFileUploadResult(const QString &sessionId, const QString &requestId, const QString &status,
                              const QString &remotePath, const QString &fileName, qint64 fileSize,
                              const QString &sha256, bool resultKnown);
    void updateTemporaryPassword(const QString &password, int version, const QString &mode, int ttlSeconds);
    void closeSession(const QString &sessionId);
    void cancelCommand(const QString &sessionId, const QString &requestId);

    // Operator actions.
    void listDevices(const QString &requestId);
    void requestSession(const QString &deviceId, const QString &purpose,
                        const QStringList &permissions, const QString &temporaryPassword);
    void executeCommand(const QString &sessionId, const QString &requestId, const QString &command,
                        const QString &workingDirectory, int timeoutSeconds);

signals:
    void stateChanged(const QString &state);
    void registered(const QJsonObject &device);
    void deviceListReceived(const QJsonArray &devices, const QString &requestId);
    void requestFailed(const QString &requestId, const QString &code, const QString &message);
    void sessionRequested(const QJsonObject &session);
    void sessionUpdated(const QJsonObject &session);
    void sessionCreated(const QJsonObject &session);
    void commandRequested(const QString &sessionId, const QString &requestId, const QString &command,
                          const QString &workingDirectory, int timeoutSeconds);
    void commandOutput(const QString &sessionId, const QString &requestId, const QString &stream,
                       const QString &data, quint64 sequence, bool truncated);
    void commandResult(const QString &sessionId, const QString &requestId, const QString &status,
                       const QJsonValue &exitCode, bool resultKnown, bool truncated);
    void fileDownloadRequested(const QString &sessionId, const QString &requestId, const QString &remotePath);
    void fileDownloadCanceled(const QString &sessionId, const QString &requestId);
    void fileUploadRequested(const QString &sessionId, const QString &requestId, const QString &remotePath,
                             const QString &fileName, qint64 fileSize, const QString &sha256, bool overwrite);
    void fileUploadChunkReceived(const QString &sessionId, const QString &requestId, quint64 sequence,
                                 const QString &data, bool eof);
    void fileUploadCanceled(const QString &sessionId, const QString &requestId);
    void cancelRequested(const QString &sessionId, const QString &requestId);
    void closeRequested(const QString &sessionId);
    void temporaryPasswordUpdated(const QString &password, int version, qint64 expiresAt);
    void credentialsRenewed(const QString &password, int version);
    void platformError(const QString &code, const QString &message);
    void connectionError(const QString &message);
    // Emitted when the server certificate cannot be verified. The user must
    // explicitly trust the fingerprint before the client will connect.
    void certificateUntrusted(const QString &fingerprint, const QString &subject,
                              const QString &details, bool changed);

private slots:
    void onConnected();
    void onEncrypted();
    void onReadyRead();
    void onDisconnected();
    void onSocketError(QAbstractSocket::SocketError error);
    void onHeartbeat();
    void onReconnect();

private:
    void send(const QJsonObject &message);
    void handleMessage(const QJsonObject &message);
    void setState(const QString &state);
    void scheduleReconnect();
    void retryWithoutProxy();
    void beginSession();
    void rejectCertificate(const QString &fingerprint, const QString &subject, const QString &details, bool changed);

    QTcpSocket *socket_ = nullptr;
    QTimer heartbeatTimer_;
    QTimer reconnectTimer_;
    Config config_;
    QByteArray buffer_;
    QString connectionId_;
    QString state_ = QStringLiteral("disconnected");
    bool manualDisconnect_ = false;
    bool authenticated_ = false;
    bool registered_ = false;
    bool rotatePasswordOnFirstRegistration_ = true;
    QString pendingTemporaryPassword_;
    bool proxyFallbackAttempted_ = false;
    bool forceDirectConnection_ = false;
    bool usingProxy_ = false;
    // Set when a TLS verification failure has already been reported for the
    // current connection, so the peer's abort does not replace the real cause
    // with the generic "remote host closed" message.
    // Set when the peer certificate could not be verified by the platform CA
    // store. The decision is made once the handshake completes so the result is
    // identical for the Schannel and OpenSSL backends.
    bool sslErrorsSeen_ = false;
    bool tlsErrorReported_ = false;
    int reconnectAttempts_ = 0;
};